package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/storage"
)

// TaskEnqueuer schedules background execution for async tasks.
type TaskEnqueuer interface {
	Enqueue(taskID string)
}

// HTTPError represents a structured HTTP error returned during request ingestion.
type HTTPError struct {
	Status  int
	Code    string
	Message string
}

func (e *HTTPError) Error() string {
	return e.Message
}

// UploadRequest carries parsed parameters and the incoming file stream.
type UploadRequest struct {
	Client         *db.Client
	Filename       string
	ContentType    string
	Body           io.Reader
	Mode           string // "" | sync | async
	Targets        []string
	Subpath        string
	MetadataJSON   string
	IdempotencyKey string
	ExpectedSHA256 string
	SkipDedup      bool
}

// UploadResponse represents the result returned to the HTTP handler.
type UploadResponse struct {
	StatusCode       int            `json:"-"`
	Task             *db.BackupTask `json:"task"`
	TimedOutToAsync  bool           `json:"timed_out_to_async,omitempty"`
	IdempotentReplay bool           `json:"idempotent_replay,omitempty"`
}

// Service orchestrates file spooling, deduplication, sync/async uploads, webhooks, and downloads.
type Service struct {
	store    *db.Store
	cipher   *crypto.Cipher
	registry *storage.Registry
	enqueuer TaskEnqueuer
	appCtx   context.Context
	hookHTTP *http.Client
}

func NewService(appCtx context.Context, store *db.Store, cipher *crypto.Cipher, registry *storage.Registry) *Service {
	return &Service{
		store:    store,
		cipher:   cipher,
		registry: registry,
		appCtx:   appCtx,
		hookHTTP: &http.Client{Timeout: 15 * time.Second},
	}
}

func (s *Service) SetEnqueuer(e TaskEnqueuer) {
	s.enqueuer = e
}

// HandleUpload processes a client backup request end-to-end.
func (s *Service) HandleUpload(ctx context.Context, req UploadRequest) (*UploadResponse, error) {
	if req.IdempotencyKey != "" {
		existing, err := s.store.GetTaskByIdempotencyKey(ctx, req.Client.ID, req.IdempotencyKey)
		if err == nil {
			code := http.StatusOK
			if existing.Status == "pending" || existing.Status == "running" {
				code = http.StatusAccepted
			} else if existing.Status == "partial_success" {
				code = http.StatusMultiStatus
			} else if existing.Status == "failed" {
				code = http.StatusBadGateway
			}
			return &UploadResponse{
				StatusCode:       code,
				Task:             existing,
				IdempotentReplay: true,
			}, nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			return nil, err
		}
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = req.Client.DefaultMode
	}
	if mode == "" {
		mode = "async"
	}
	if mode != "sync" && mode != "async" {
		return nil, &HTTPError{Status: http.StatusBadRequest, Code: "invalid_mode", Message: "mode must be 'sync' or 'async'"}
	}

	// Validate MIME type if restricted
	contentType := strings.TrimSpace(req.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := validateMimeType(req.Client.AllowedMimeTypesJSON, contentType); err != nil {
		return nil, err
	}

	// Resolve target upstreams for this client
	targets, err := s.resolveTargets(ctx, req.Client.ID, req.Targets)
	if err != nil {
		return nil, err
	}

	settings, err := s.store.GetSystemSettings(ctx)
	if err != nil {
		return nil, err
	}
	maxSize := settings.MaxFileSizeBytes
	if req.Client.MaxFileSizeBytes > 0 {
		maxSize = req.Client.MaxFileSizeBytes
	}
	if maxSize <= 0 {
		maxSize = 512 * 1024 * 1024
	}

	subpath, err := sanitizeSubpath(req.Subpath)
	if err != nil {
		return nil, &HTTPError{Status: http.StatusBadRequest, Code: "invalid_path", Message: err.Error()}
	}
	safeName := sanitizeFilename(req.Filename)
	taskID := crypto.NewULID()

	// Stream body to spool file while computing SHA-256
	if err := os.MkdirAll(settings.SpoolDir, 0o750); err != nil {
		return nil, fmt.Errorf("create spool dir: %w", err)
	}
	spoolPath := filepath.Join(settings.SpoolDir, taskID+".spool")
	f, err := os.OpenFile(spoolPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return nil, fmt.Errorf("create spool file: %w", err)
	}

	hasher := sha256.New()
	limited := io.LimitReader(req.Body, maxSize+1)
	written, copyErr := io.Copy(io.MultiWriter(f, hasher), limited)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(spoolPath)
		return nil, fmt.Errorf("read upload stream: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(spoolPath)
		return nil, fmt.Errorf("close spool file: %w", closeErr)
	}
	if written > maxSize {
		_ = os.Remove(spoolPath)
		return nil, &HTTPError{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    "file_too_large",
			Message: fmt.Sprintf("file exceeds max allowed size of %d bytes", maxSize),
		}
	}

	shaHex := hex.EncodeToString(hasher.Sum(nil))
	if req.ExpectedSHA256 != "" && !strings.EqualFold(strings.TrimSpace(req.ExpectedSHA256), shaHex) {
		_ = os.Remove(spoolPath)
		return nil, &HTTPError{
			Status:  http.StatusBadRequest,
			Code:    "checksum_mismatch",
			Message: "uploaded file sha256 does not match X-Content-SHA256",
		}
	}

	// Check SHA-256 deduplication for this client
	var matchedByUpstream map[string]db.BackupTaskItem
	if !req.SkipDedup {
		matchedByUpstream, err = s.store.FindSucceededItemsByHash(ctx, req.Client.ID, shaHex)
		if err != nil {
			_ = os.Remove(spoolPath)
			return nil, err
		}
	}

	now := time.Now().UTC()
	maxAttempts := settings.MaxRetryAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}

	metaJSON := "{}"
	if strings.TrimSpace(req.MetadataJSON) != "" {
		if json.Valid([]byte(req.MetadataJSON)) {
			metaJSON = req.MetadataJSON
		} else {
			_ = os.Remove(spoolPath)
			return nil, &HTTPError{Status: http.StatusBadRequest, Code: "invalid_metadata", Message: "metadata must be valid JSON"}
		}
	}

	items := make([]db.BackupTaskItem, 0, len(targets))
	allDeduped := true
	anyDeduped := false
	for _, target := range targets {
		remotePath := buildRemotePath(req.Client.PathPrefix, subpath, taskID, safeName, now)
		if matched, ok := matchedByUpstream[target.UpstreamID]; ok {
			anyDeduped = true
			fin := now
			items = append(items, db.BackupTaskItem{
				ID:              crypto.NewULID(),
				TaskID:          taskID,
				UpstreamID:      target.UpstreamID,
				UpstreamName:    target.UpstreamName,
				UpstreamType:    target.UpstreamType,
				RemotePath:      matched.RemotePath,
				RemoteETag:      matched.RemoteETag,
				RemoteVersionID: matched.RemoteVersionID,
				Status:          "succeeded",
				AttemptCount:    0,
				MaxAttempts:     maxAttempts,
				DurationMs:      0,
				StartedAt:       &now,
				FinishedAt:      &fin,
			})
			continue
		}
		allDeduped = false
		items = append(items, db.BackupTaskItem{
			ID:           crypto.NewULID(),
			TaskID:       taskID,
			UpstreamID:   target.UpstreamID,
			UpstreamName: target.UpstreamName,
			UpstreamType: target.UpstreamType,
			RemotePath:   remotePath,
			Status:       "pending",
			MaxAttempts:  maxAttempts,
		})
	}

	webhookStatus := "none"
	if req.Client.WebhookURL != "" {
		webhookStatus = "pending"
	}

	task := &db.BackupTask{
		ID:               taskID,
		ClientID:         req.Client.ID,
		ClientName:       req.Client.Name,
		IdempotencyKey:   req.IdempotencyKey,
		Mode:             mode,
		Status:           "pending",
		OriginalFilename: safeName,
		ContentType:      contentType,
		FileSize:         written,
		SHA256:           shaHex,
		SpoolPath:        spoolPath,
		SpoolCleaned:     false,
		CustomSubpath:    subpath,
		MetadataJSON:     metaJSON,
		Deduplicated:     allDeduped,
		WebhookStatus:    webhookStatus,
		CreatedAt:        now,
	}

	if allDeduped {
		_ = os.Remove(spoolPath)
		fin := now
		task.Status = "succeeded"
		task.SpoolPath = ""
		task.SpoolCleaned = true
		task.StartedAt = &now
		task.FinishedAt = &fin
		if err := s.store.CreateBackupTask(ctx, task, items); err != nil {
			return nil, err
		}
		task.Items = items
		return &UploadResponse{
			StatusCode: http.StatusOK,
			Task:       task,
		}, nil
	}

	if mode == "async" {
		if err := s.store.CreateBackupTask(ctx, task, items); err != nil {
			_ = os.Remove(spoolPath)
			return nil, err
		}
		task.Items = items
		if s.enqueuer != nil {
			s.enqueuer.Enqueue(task.ID)
		}
		return &UploadResponse{
			StatusCode: http.StatusAccepted,
			Task:       task,
		}, nil
	}

	// Sync mode: persist as running and execute concurrently; if sync timeout expires,
	// let the execution continue in background and return 202 Accepted.
	task.Status = "running"
	task.StartedAt = &now
	if err := s.store.CreateBackupTask(ctx, task, items); err != nil {
		_ = os.Remove(spoolPath)
		return nil, err
	}
	task.Items = items
	_ = anyDeduped

	syncTimeout := time.Duration(settings.SyncTimeoutSec) * time.Second
	if syncTimeout <= 0 {
		syncTimeout = 60 * time.Second
	}

	doneCh := make(chan struct{})
	go func() {
		s.executeTask(s.appCtx, task.ID, true)
		close(doneCh)
	}()

	select {
	case <-doneCh:
		updated, err := s.store.GetBackupTask(ctx, task.ID)
		if err != nil {
			return nil, err
		}
		code := http.StatusOK
		switch updated.Status {
		case "partial_success":
			code = http.StatusMultiStatus
		case "failed":
			code = http.StatusBadGateway
		case "pending", "running":
			code = http.StatusAccepted
		}
		return &UploadResponse{
			StatusCode: code,
			Task:       updated,
		}, nil
	case <-time.After(syncTimeout):
		updated, err := s.store.GetBackupTask(ctx, task.ID)
		if err != nil {
			updated = task
		}
		return &UploadResponse{
			StatusCode:      http.StatusAccepted,
			Task:            updated,
			TimedOutToAsync: true,
		}, nil
	}
}

// ProcessAsyncTask executes pending/retryable items for an async task.
func (s *Service) ProcessAsyncTask(ctx context.Context, taskID string) {
	s.executeTask(ctx, taskID, false)
}

func (s *Service) executeTask(ctx context.Context, taskID string, isSyncInitial bool) {
	task, err := s.store.GetBackupTask(ctx, taskID)
	if err != nil {
		return
	}

	now := time.Now().UTC()
	_ = s.store.UpdateTaskStatus(ctx, task.ID, "running", "", &now, nil)

	var wg sync.WaitGroup
	for i := range task.Items {
		it := task.Items[i]
		if it.Status != "pending" {
			continue
		}
		if it.NextRetryAt != nil && it.NextRetryAt.After(now) {
			continue
		}
		wg.Add(1)
		go func(item db.BackupTaskItem) {
			defer wg.Done()
			s.executeItem(ctx, task, &item, isSyncInitial)
		}(it)
	}
	wg.Wait()

	s.finalizeTaskState(ctx, task.ID)
}

func (s *Service) executeItem(ctx context.Context, task *db.BackupTask, item *db.BackupTaskItem, isSyncInitial bool) {
	start := time.Now().UTC()
	item.Status = "running"
	item.AttemptCount++
	item.StartedAt = &start
	_ = s.store.UpdateTaskItemResult(ctx, item)

	err := s.uploadItemOnce(ctx, task, item)
	fin := time.Now().UTC()
	item.DurationMs = fin.Sub(start).Milliseconds()

	if err == nil {
		item.Status = "succeeded"
		item.ErrorMessage = ""
		item.NextRetryAt = nil
		item.FinishedAt = &fin
		_ = s.store.UpdateTaskItemResult(ctx, item)
		return
	}

	item.ErrorMessage = err.Error()
	if item.AttemptCount < item.MaxAttempts && !isSyncInitial {
		// Exponential backoff: 15s, 60s, 300s...
		delay := time.Duration(15*(1<<min(item.AttemptCount-1, 4))) * time.Second
		nextRetry := fin.Add(delay)
		item.Status = "pending"
		item.NextRetryAt = &nextRetry
	} else if item.AttemptCount < item.MaxAttempts && isSyncInitial {
		// Hand off failed sync item for background retry rather than giving up immediately
		nextRetry := fin.Add(15 * time.Second)
		item.Status = "pending"
		item.NextRetryAt = &nextRetry
	} else {
		item.Status = "failed"
		item.NextRetryAt = nil
		item.FinishedAt = &fin
	}
	_ = s.store.UpdateTaskItemResult(ctx, item)
}

func (s *Service) uploadItemOnce(ctx context.Context, task *db.BackupTask, item *db.BackupTaskItem) error {
	if task.SpoolPath == "" {
		return errors.New("spool file path is empty")
	}
	f, err := os.Open(task.SpoolPath)
	if err != nil {
		return fmt.Errorf("open spool file: %w", err)
	}
	defer f.Close()

	provider, _, err := s.registry.Get(ctx, item.UpstreamID)
	if err != nil {
		return err
	}

	res, err := provider.Upload(ctx, item.RemotePath, f, task.FileSize, task.ContentType)
	if err != nil {
		return err
	}
	if res.RemotePath != "" {
		item.RemotePath = res.RemotePath
	}
	item.RemoteETag = res.RemoteETag
	item.RemoteVersionID = res.RemoteVersionID
	return nil
}

func (s *Service) finalizeTaskState(ctx context.Context, taskID string) {
	task, err := s.store.GetBackupTask(ctx, taskID)
	if err != nil {
		return
	}

	var succeeded, failed, pending int
	var errs []string
	for _, it := range task.Items {
		switch it.Status {
		case "succeeded":
			succeeded++
		case "failed":
			failed++
			if it.ErrorMessage != "" {
				errs = append(errs, fmt.Sprintf("%s: %s", it.UpstreamName, it.ErrorMessage))
			}
		case "pending", "running":
			pending++
		}
	}

	if pending > 0 {
		// Still waiting on retry
		status := "pending"
		if succeeded > 0 {
			status = "partial_success"
		}
		_ = s.store.UpdateTaskStatus(ctx, task.ID, status, strings.Join(errs, "; "), nil, nil)
		return
	}

	fin := time.Now().UTC()
	finalStatus := "succeeded"
	if failed > 0 && succeeded > 0 {
		finalStatus = "partial_success"
	} else if failed > 0 && succeeded == 0 {
		finalStatus = "failed"
	}

	_ = s.store.UpdateTaskStatus(ctx, task.ID, finalStatus, strings.Join(errs, "; "), nil, &fin)

	if finalStatus == "succeeded" && task.SpoolPath != "" && !task.SpoolCleaned {
		if err := os.Remove(task.SpoolPath); err == nil || errors.Is(err, os.ErrNotExist) {
			_ = s.store.MarkSpoolCleaned(ctx, task.ID)
		}
	}

	if task.WebhookStatus == "pending" {
		go s.dispatchWebhook(context.Background(), task.ID)
	}
}

func (s *Service) dispatchWebhook(ctx context.Context, taskID string) {
	task, err := s.store.GetBackupTask(ctx, taskID)
	if err != nil {
		return
	}
	client, err := s.store.GetClientByID(ctx, task.ClientID)
	if err != nil || client.WebhookURL == "" {
		return
	}

	payload, err := json.Marshal(map[string]any{
		"event":     "backup." + task.Status,
		"timestamp": time.Now().UTC(),
		"task":      task,
	})
	if err != nil {
		return
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.WebhookURL, bytes.NewReader(payload))
	if err != nil {
		_ = s.store.UpdateTaskWebhookStatus(ctx, task.ID, "failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if client.WebhookSecretEncrypted != "" {
		if secret, err := s.cipher.DecryptString(client.WebhookSecretEncrypted); err == nil && secret != "" {
			req.Header.Set("X-SafeKeep-Signature", crypto.SignWebhook(secret, payload))
		}
	}

	resp, err := s.hookHTTP.Do(req)
	if err != nil {
		_ = s.store.UpdateTaskWebhookStatus(ctx, task.ID, "failed")
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		_ = s.store.UpdateTaskWebhookStatus(ctx, task.ID, "sent")
	} else {
		_ = s.store.UpdateTaskWebhookStatus(ctx, task.ID, "failed")
	}
}

// DownloadTaskFile streams a backed-up file from the requested or highest-priority succeeded upstream.
func (s *Service) DownloadTaskFile(ctx context.Context, task *db.BackupTask, preferredUpstreamID string) (io.ReadCloser, int64, string, string, error) {
	var candidates []db.BackupTaskItem
	for _, it := range task.Items {
		if it.Status != "succeeded" {
			continue
		}
		if preferredUpstreamID != "" && it.UpstreamID != preferredUpstreamID {
			continue
		}
		candidates = append(candidates, it)
	}
	if len(candidates) == 0 {
		return nil, 0, "", "", &HTTPError{
			Status:  http.StatusNotFound,
			Code:    "no_succeeded_upstream",
			Message: "no succeeded upstream copy found for this task",
		}
	}

	// Order by client upstream priority if available
	if cuList, err := s.store.GetClientUpstreams(ctx, task.ClientID); err == nil {
		prio := make(map[string]int, len(cuList))
		for _, cu := range cuList {
			prio[cu.UpstreamID] = cu.Priority
		}
		for i := 0; i < len(candidates); i++ {
			for j := i + 1; j < len(candidates); j++ {
				pi, ok1 := prio[candidates[i].UpstreamID]
				pj, ok2 := prio[candidates[j].UpstreamID]
				if !ok1 {
					pi = 9999
				}
				if !ok2 {
					pj = 9999
				}
				if pj < pi {
					candidates[i], candidates[j] = candidates[j], candidates[i]
				}
			}
		}
	}

	var lastErr error
	for _, item := range candidates {
		provider, _, err := s.registry.Get(ctx, item.UpstreamID)
		if err != nil {
			lastErr = err
			continue
		}
		rc, size, ct, err := provider.Download(ctx, item.RemotePath)
		if err != nil {
			lastErr = err
			continue
		}
		if ct == "" || ct == "application/octet-stream" {
			if task.ContentType != "" {
				ct = task.ContentType
			}
		}
		if size <= 0 {
			size = task.FileSize
		}
		return rc, size, ct, task.OriginalFilename, nil
	}

	return nil, 0, "", "", fmt.Errorf("download from upstream failed: %w", lastErr)
}

func (s *Service) resolveTargets(ctx context.Context, clientID string, requested []string) ([]db.ClientUpstream, error) {
	allowed, err := s.store.GetClientUpstreams(ctx, clientID)
	if err != nil {
		return nil, err
	}
	if len(allowed) == 0 {
		return nil, &HTTPError{Status: http.StatusBadRequest, Code: "no_upstreams", Message: "no upstreams bound to this client"}
	}

	if len(requested) == 0 {
		var defaults []db.ClientUpstream
		for _, cu := range allowed {
			if cu.IsDefault && cu.Enabled {
				defaults = append(defaults, cu)
			}
		}
		if len(defaults) == 0 {
			return nil, &HTTPError{Status: http.StatusBadRequest, Code: "no_default_upstreams", Message: "no enabled default upstreams configured for this client"}
		}
		return defaults, nil
	}

	byID := make(map[string]db.ClientUpstream, len(allowed))
	byName := make(map[string]db.ClientUpstream, len(allowed))
	for _, cu := range allowed {
		byID[cu.UpstreamID] = cu
		byName[strings.ToLower(cu.UpstreamName)] = cu
	}

	seen := make(map[string]bool)
	var chosen []db.ClientUpstream
	for _, raw := range requested {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		cu, ok := byID[t]
		if !ok {
			cu, ok = byName[strings.ToLower(t)]
		}
		if !ok {
			return nil, &HTTPError{
				Status:  http.StatusForbidden,
				Code:    "upstream_not_allowed",
				Message: fmt.Sprintf("upstream %q is not permitted for this client", t),
			}
		}
		if !cu.Enabled {
			return nil, &HTTPError{
				Status:  http.StatusBadRequest,
				Code:    "upstream_disabled",
				Message: fmt.Sprintf("upstream %q is currently disabled", cu.UpstreamName),
			}
		}
		if !seen[cu.UpstreamID] {
			seen[cu.UpstreamID] = true
			chosen = append(chosen, cu)
		}
	}
	if len(chosen) == 0 {
		return nil, &HTTPError{Status: http.StatusBadRequest, Code: "empty_targets", Message: "no valid targets specified"}
	}
	return chosen, nil
}

func validateMimeType(allowedJSON, actual string) error {
	var allowed []string
	if err := json.Unmarshal([]byte(allowedJSON), &allowed); err != nil || len(allowed) == 0 {
		return nil
	}
	baseActual := strings.ToLower(strings.TrimSpace(strings.Split(actual, ";")[0]))
	for _, pattern := range allowed {
		p := strings.ToLower(strings.TrimSpace(pattern))
		if p == "" {
			continue
		}
		if p == "*/*" || p == baseActual {
			return nil
		}
		if strings.HasSuffix(p, "/*") && strings.HasPrefix(baseActual, strings.TrimSuffix(p, "*")) {
			return nil
		}
	}
	return &HTTPError{
		Status:  http.StatusUnsupportedMediaType,
		Code:    "mime_not_allowed",
		Message: fmt.Sprintf("content type %q is not permitted for this client", baseActual),
	}
}

func sanitizeSubpath(raw string) (string, error) {
	clean := strings.ReplaceAll(strings.TrimSpace(raw), "\\", "/")
	clean = strings.Trim(clean, "/")
	if clean == "" {
		return "", nil
	}
	parts := strings.Split(clean, "/")
	var safe []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return "", errors.New("path traversal ('..') is not allowed")
		}
		safe = append(safe, p)
	}
	return path.Join(safe...), nil
}

func sanitizeFilename(name string) string {
	base := filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if base == "" || base == "." || base == "/" || base == ".." {
		return "file.bin"
	}
	var b strings.Builder
	for _, r := range base {
		if r < 32 || r == '/' || r == '\\' || r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|' {
			b.WriteByte('_')
		} else {
			b.WriteRune(r)
		}
	}
	res := strings.TrimSpace(b.String())
	if res == "" {
		return "file.bin"
	}
	return res
}

func buildRemotePath(clientPrefix, subpath, taskID, filename string, now time.Time) string {
	uniqueName := fmt.Sprintf("%s_%s_%s", now.UTC().Format("20060102T150405Z"), taskID, filename)
	var parts []string
	if cp := strings.Trim(strings.ReplaceAll(clientPrefix, "\\", "/"), "/"); cp != "" {
		parts = append(parts, cp)
	}
	if subpath != "" {
		parts = append(parts, subpath)
	}
	parts = append(parts, uniqueName)
	return path.Join(parts...)
}
