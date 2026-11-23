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
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"safekeep/internal/audit"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/notify"
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

// UploadRequest carries parsed parameters and the incoming file stream (or pre-spooled file).
type UploadRequest struct {
	Client           *db.Client
	Filename         string
	Rename           string
	ContentType      string
	Body             io.Reader
	PreSpooledPath   string
	PreSpooledSize   int64
	PreSpooledSHA256 string
	Mode             string // "" | sync | async
	Targets          []string
	Subpath          string
	MetadataJSON     string
	IdempotencyKey   string
	ExpectedSHA256   string
	SkipDedup        bool
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
	auditLog *audit.Logger
	notifier *notify.Dispatcher
	appCtx   context.Context
	hookHTTP *http.Client

	mu           sync.Mutex
	runningTasks map[string]bool
	wg           sync.WaitGroup
}

func NewService(appCtx context.Context, store *db.Store, cipher *crypto.Cipher, registry *storage.Registry) *Service {
	dialer := &net.Dialer{
		Timeout:   10 * time.Second,
		Control:   blockMetadataIPControl,
	}
	return &Service{
		store:        store,
		cipher:       cipher,
		registry:     registry,
		appCtx:       appCtx,
		runningTasks: make(map[string]bool),
		hookHTTP: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				DialContext: dialer.DialContext,
			},
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func blockMetadataIPControl(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return nil
	}
	// Block cloud metadata link-local range (169.254.0.0/16 and fe80::/10)
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
		return fmt.Errorf("webhook target IP %s is blocked", ip.String())
	}
	return nil
}

// ValidateWebhookURL checks that an optional webhook URL uses http or https.
func ValidateWebhookURL(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("webhook_url must be a valid http or https URL")
	}
	return nil
}

func (s *Service) SetEnqueuer(e TaskEnqueuer) {
	s.enqueuer = e
}

func (s *Service) SetAuditLogger(a *audit.Logger) {
	s.auditLog = a
}

func (s *Service) SetNotifier(n *notify.Dispatcher) {
	s.mu.Lock()
	s.notifier = n
	s.mu.Unlock()
}

func (s *Service) ensureNotifier() *notify.Dispatcher {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.notifier == nil {
		s.notifier = notify.NewDispatcher(s.appCtx,
			func(ctx context.Context) (notify.SMTPConfig, error) {
				return s.store.GetSMTPConfig(ctx, s.cipher)
			},
			func(ctx context.Context, action, resourceID, status string, detail map[string]any) {
				if s.auditLog != nil {
					s.auditLog.Record(ctx, audit.Event{
						ActorType: "system", ActorID: "notifier", ActorName: "smtp",
						Action: action, ResourceType: "alert_email", ResourceID: resourceID,
						Status: status, Detail: detail,
					})
				}
			},
		)
	}
	return s.notifier
}

// Wait blocks until all background sync-fallback, webhook, and notification goroutines finish.
func (s *Service) Wait() {
	s.wg.Wait()
	s.mu.Lock()
	n := s.notifier
	s.mu.Unlock()
	if n != nil {
		n.Wait()
	}
}

func (s *Service) tryStartTask(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.runningTasks[taskID] {
		return false
	}
	s.runningTasks[taskID] = true
	return true
}

func (s *Service) finishTask(taskID string) {
	s.mu.Lock()
	delete(s.runningTasks, taskID)
	s.mu.Unlock()
}

// SpoolStream streams an incoming reader to the spool directory while enforcing circuit breaker, file size, and spool limits.
func (s *Service) SpoolStream(ctx context.Context, client *db.Client, filename string, r io.Reader) (string, int64, string, error) {
	settings, err := s.store.GetSystemSettings(ctx)
	if err != nil {
		return "", 0, "", err
	}
	if tripped, used, cbErr := s.store.IsCircuitBreakerTripped(ctx, db.TodayDateKey(), settings.DailyTrafficLimitBytes); cbErr == nil && tripped {
		return "", 0, "", &HTTPError{
			Status:  http.StatusServiceUnavailable,
			Code:    "circuit_breaker_open",
			Message: fmt.Sprintf("daily backup quota exceeded (%d / %d bytes), service circuit breaker is open", used, settings.DailyTrafficLimitBytes),
		}
	}
	if err := s.checkSpoolQuota(settings.SpoolDir, settings.MaxSpoolBytes); err != nil {
		return "", 0, "", err
	}
	maxSize := settings.MaxFileSizeBytes
	if client != nil && client.MaxFileSizeBytes > 0 {
		maxSize = client.MaxFileSizeBytes
	}
	if maxSize <= 0 {
		maxSize = 100 * 1024 * 1024
	}
	if err := os.MkdirAll(settings.SpoolDir, 0o750); err != nil {
		return "", 0, "", fmt.Errorf("create spool dir: %w", err)
	}
	spoolPath := filepath.Join(settings.SpoolDir, crypto.NewULID()+".spool")
	f, err := os.OpenFile(spoolPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return "", 0, "", fmt.Errorf("create spool file: %w", err)
	}

	hasher := sha256.New()
	written, copyErr := io.Copy(io.MultiWriter(f, hasher), io.LimitReader(r, maxSize+1))
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(spoolPath)
		return "", 0, "", fmt.Errorf("read upload stream: %w", copyErr)
	}
	if closeErr != nil {
		_ = os.Remove(spoolPath)
		return "", 0, "", fmt.Errorf("close spool file: %w", closeErr)
	}
	if written > maxSize {
		_ = os.Remove(spoolPath)
		s.notifyOversizeRejection(client, sanitizeFilename(filename), written, maxSize)
		return "", 0, "", &HTTPError{
			Status:  http.StatusRequestEntityTooLarge,
			Code:    "file_too_large",
			Message: fmt.Sprintf("file exceeds max allowed size of %d bytes", maxSize),
		}
	}
	return spoolPath, written, hex.EncodeToString(hasher.Sum(nil)), nil
}

func (s *Service) notifyOversizeRejection(client *db.Client, filename string, attemptedBytes, maxBytes int64) {
	clientName := "unknown"
	clientID := ""
	if client != nil {
		clientName = client.Name
		clientID = client.ID
	}
	subject := fmt.Sprintf("[SafeKeep 告警] 单文件超过限制已拒绝 (%s)", filename)
	body := fmt.Sprintf(
		"SafeKeep 拒绝了一次超大文件上传请求：\n\n"+
			"- 客户名称: %s (%s)\n"+
			"- 文件名称: %s\n"+
			"- 拦截时已接收字节: >= %d bytes (%.2f MB)\n"+
			"- 允许单文件上限: %d bytes (%.2f MB)\n"+
			"- 触发时间: %s\n",
		clientName, clientID, filename,
		attemptedBytes, float64(attemptedBytes)/(1024*1024),
		maxBytes, float64(maxBytes)/(1024*1024),
		time.Now().Format(time.RFC3339),
	)
	s.spawnAlertEmail("alert.oversize_rejected", clientID, subject, body)
}

func (s *Service) notifyCircuitBreakerTripped(client *db.Client, filename string, attemptedBytes, currentUsed, dailyLimit int64) {
	clientName := "unknown"
	clientID := ""
	if client != nil {
		clientName = client.Name
		clientID = client.ID
	}
	subject := fmt.Sprintf("[SafeKeep 熔断告警] 每日备份流量已达上限 (%s)", db.TodayDateKey())
	body := fmt.Sprintf(
		"SafeKeep 今日备份流量超过每日熔断阈值，服务已自动熔断（新上传请求将返回 503）：\n\n"+
			"- 日期: %s\n"+
			"- 今日已用流量: %d bytes (%.2f GB)\n"+
			"- 触发本次请求大小: %d bytes (%.2f MB)\n"+
			"- 每日熔断阈值: %d bytes (%.2f GB)\n"+
			"- 触发客户: %s (%s)\n"+
			"- 触发文件: %s\n"+
			"- 触发时间: %s\n\n"+
			"如需提前恢复服务，请登录 SafeKeep Web 管理端仪表盘点击「解除熔断 / 重置今日流量」。\n",
		db.TodayDateKey(),
		currentUsed, float64(currentUsed)/(1024*1024*1024),
		attemptedBytes, float64(attemptedBytes)/(1024*1024),
		dailyLimit, float64(dailyLimit)/(1024*1024*1024),
		clientName, clientID, filename,
		time.Now().Format(time.RFC3339),
	)
	s.spawnAlertEmail("alert.circuit_breaker_tripped", db.TodayDateKey(), subject, body)
}

func (s *Service) spawnAlertEmail(action, resourceID, subject, body string) {
	s.ensureNotifier().Dispatch(notify.Event{
		EventKey:    action,
		AuditAction: action,
		ResourceID:  resourceID,
		Subject:     subject,
		Body:        body,
		ShouldSend: func(r notify.NotifyRules) bool {
			switch action {
			case "alert.oversize_rejected":
				return r.OversizeRejected
			case "alert.circuit_breaker_tripped":
				return r.CircuitBreakerTripped
			default:
				return true
			}
		},
	})
}

func (s *Service) notifyBackupTaskResult(task *db.BackupTask) {
	if task == nil {
		return
	}
	succeeded := task.Status == "succeeded"
	statusLabel := "成功"
	switch task.Status {
	case "partial_success":
		statusLabel = "部分成功"
	case "failed":
		statusLabel = "失败"
	}
	clientLabel := task.ClientName
	if clientLabel == "" {
		clientLabel = task.ClientID
	}
	fileLabel := task.RemoteFilename
	if fileLabel == "" {
		fileLabel = task.OriginalFilename
	}
	var targetLines []string
	for _, it := range task.Items {
		line := fmt.Sprintf("  - %s (%s): %s", it.UpstreamName, it.UpstreamType, it.Status)
		if it.ErrorMessage != "" {
			line += " — " + it.ErrorMessage
		}
		targetLines = append(targetLines, line)
	}
	subject := fmt.Sprintf("[SafeKeep] 备份上传%s (%s)", statusLabel, fileLabel)
	body := fmt.Sprintf(
		"SafeKeep 备份任务已结束：\n\n"+
			"- 任务 ID: %s\n"+
			"- 客户: %s (%s)\n"+
			"- 文件: %s (%d bytes)\n"+
			"- 模式: %s%s\n"+
			"- 结果: %s\n",
		task.ID,
		clientLabel, task.ClientID,
		fileLabel, task.FileSize,
		task.Mode, map[bool]string{true: " [秒传去重]", false: ""}[task.Deduplicated],
		statusLabel,
	)
	if len(targetLines) > 0 {
		body += "- 目标上游:\n" + strings.Join(targetLines, "\n") + "\n"
	}
	if task.ErrorSummary != "" {
		body += fmt.Sprintf("- 错误摘要: %s\n", task.ErrorSummary)
	}
	body += fmt.Sprintf("- 时间: %s\n", time.Now().Format(time.RFC3339))

	s.ensureNotifier().Dispatch(notify.Event{
		EventKey:   "backup.upload",
		ResourceID: task.ID,
		Subject:    subject,
		Body:       body,
		ShouldSend: func(r notify.NotifyRules) bool {
			return r.BackupUpload.ShouldNotify(succeeded)
		},
	})
}

func (s *Service) checkSpoolQuota(spoolDir string, maxSpoolBytes int64) error {
	if maxSpoolBytes <= 0 {
		return nil
	}
	var used int64
	_ = filepath.WalkDir(spoolDir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), ".spool") {
			if info, iErr := d.Info(); iErr == nil {
				used += info.Size()
			}
		}
		return nil
	})
	if used >= maxSpoolBytes {
		return &HTTPError{
			Status:  http.StatusInsufficientStorage,
			Code:    "spool_full",
			Message: fmt.Sprintf("local spool quota reached (%d / %d bytes)", used, maxSpoolBytes),
		}
	}
	return nil
}

// HandleUpload processes a client backup request end-to-end.
func (s *Service) HandleUpload(ctx context.Context, req UploadRequest) (*UploadResponse, error) {
	cleanupPreSpool := func() {
		if req.PreSpooledPath != "" {
			_ = os.Remove(req.PreSpooledPath)
		}
	}

	if req.IdempotencyKey != "" {
		existing, err := s.store.GetTaskByIdempotencyKey(ctx, req.Client.ID, req.IdempotencyKey)
		if err == nil {
			cleanupPreSpool()
			return idempotentResponse(existing), nil
		}
		if !errors.Is(err, db.ErrNotFound) {
			cleanupPreSpool()
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
		cleanupPreSpool()
		return nil, &HTTPError{Status: http.StatusBadRequest, Code: "invalid_mode", Message: "mode must be 'sync' or 'async'"}
	}

	contentType := strings.TrimSpace(req.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if err := validateMimeType(req.Client.AllowedMimeTypesJSON, contentType); err != nil {
		cleanupPreSpool()
		return nil, err
	}

	targets, err := s.resolveTargets(ctx, req.Client.ID, req.Targets)
	if err != nil {
		cleanupPreSpool()
		return nil, err
	}

	settings, err := s.store.GetSystemSettings(ctx)
	if err != nil {
		cleanupPreSpool()
		return nil, err
	}
	if tripped, used, cbErr := s.store.IsCircuitBreakerTripped(ctx, db.TodayDateKey(), settings.DailyTrafficLimitBytes); cbErr == nil && tripped {
		cleanupPreSpool()
		return nil, &HTTPError{
			Status:  http.StatusServiceUnavailable,
			Code:    "circuit_breaker_open",
			Message: fmt.Sprintf("daily backup quota exceeded (%d / %d bytes), service circuit breaker is open", used, settings.DailyTrafficLimitBytes),
		}
	}

	subpath, err := sanitizeSubpath(req.Subpath)
	if err != nil {
		cleanupPreSpool()
		return nil, &HTTPError{Status: http.StatusBadRequest, Code: "invalid_path", Message: err.Error()}
	}
	clientPrefix, err := sanitizeSubpath(req.Client.PathPrefix)
	if err != nil {
		clientPrefix = ""
	}
	safeName := sanitizeFilename(req.Filename)
	remoteFilename, err := sanitizeRename(req.Rename)
	if err != nil {
		cleanupPreSpool()
		return nil, err
	}
	if remoteFilename == "" {
		remoteFilename = safeName
	}
	taskID := crypto.NewULID()

	spoolPath := req.PreSpooledPath
	written := req.PreSpooledSize
	shaHex := req.PreSpooledSHA256
	if spoolPath == "" {
		spoolPath, written, shaHex, err = s.SpoolStream(ctx, req.Client, safeName, req.Body)
		if err != nil {
			return nil, err
		}
	}

	if req.ExpectedSHA256 != "" && !strings.EqualFold(strings.TrimSpace(req.ExpectedSHA256), shaHex) {
		_ = os.Remove(spoolPath)
		return nil, &HTTPError{
			Status:  http.StatusBadRequest,
			Code:    "checksum_mismatch",
			Message: "uploaded file sha256 does not match X-Content-SHA256",
		}
	}

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
	for _, target := range targets {
		remotePath := buildRemotePath(clientPrefix, subpath, remoteFilename)
		if matched, ok := matchedByUpstream[target.UpstreamID]; ok {
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
		RemoteFilename:   remoteFilename,
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

	createOrReplay := func() (*UploadResponse, error) {
		err := s.store.CreateBackupTask(ctx, task, items)
		if err == nil {
			return nil, nil
		}
		_ = os.Remove(spoolPath)
		if req.IdempotencyKey != "" && strings.Contains(strings.ToLower(err.Error()), "unique") {
			if existing, gErr := s.store.GetTaskByIdempotencyKey(ctx, req.Client.ID, req.IdempotencyKey); gErr == nil {
				return idempotentResponse(existing), nil
			}
		}
		return nil, err
	}

	if allDeduped {
		_ = os.Remove(spoolPath)
		fin := now
		task.Status = "succeeded"
		task.SpoolPath = ""
		task.SpoolCleaned = true
		task.StartedAt = &now
		task.FinishedAt = &fin
		if replay, err := createOrReplay(); err != nil || replay != nil {
			return replay, err
		}
		task.Items = items
		if req.Client != nil {
			task.ClientName = req.Client.Name
		}
		if task.WebhookStatus == "pending" {
			s.spawnWebhook(task.ID)
		}
		s.notifyBackupTaskResult(task)
		return &UploadResponse{
			StatusCode: http.StatusOK,
			Task:       task,
		}, nil
	}

	if settings.DailyTrafficLimitBytes > 0 {
		allowed, shouldNotify, used, cbErr := s.store.CheckAndReserveDailyTraffic(ctx, db.TodayDateKey(), written, settings.DailyTrafficLimitBytes)
		if cbErr != nil {
			_ = os.Remove(spoolPath)
			return nil, cbErr
		}
		if !allowed {
			_ = os.Remove(spoolPath)
			if shouldNotify {
				s.notifyCircuitBreakerTripped(req.Client, safeName, written, used, settings.DailyTrafficLimitBytes)
			}
			return nil, &HTTPError{
				Status:  http.StatusServiceUnavailable,
				Code:    "circuit_breaker_open",
				Message: fmt.Sprintf("daily backup quota exceeded (%d / %d bytes), service circuit breaker tripped", used, settings.DailyTrafficLimitBytes),
			}
		}
	}

	if mode == "async" {
		if replay, err := createOrReplay(); err != nil || replay != nil {
			return replay, err
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

	// Sync mode: persist as running and execute concurrently; if sync timeout expires
	// or client disconnects, continue in background and return 202 Accepted.
	task.Status = "running"
	task.StartedAt = &now
	if replay, err := createOrReplay(); err != nil || replay != nil {
		return replay, err
	}
	task.Items = items

	syncTimeout := time.Duration(settings.SyncTimeoutSec) * time.Second
	if syncTimeout <= 0 {
		syncTimeout = 60 * time.Second
	}

	doneCh := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.executeTask(s.appCtx, task.ID, true)
		close(doneCh)
	}()

	timer := time.NewTimer(syncTimeout)
	defer timer.Stop()

	select {
	case <-doneCh:
		updated, err := s.store.GetBackupTask(s.appCtx, task.ID)
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
	case <-timer.C:
		updated, err := s.store.GetBackupTask(s.appCtx, task.ID)
		if err != nil {
			updated = task
		}
		return &UploadResponse{
			StatusCode:      http.StatusAccepted,
			Task:            updated,
			TimedOutToAsync: true,
		}, nil
	case <-ctx.Done():
		updated, err := s.store.GetBackupTask(s.appCtx, task.ID)
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

func idempotentResponse(existing *db.BackupTask) *UploadResponse {
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
	}
}

// ProcessAsyncTask executes pending/retryable items for an async task.
func (s *Service) ProcessAsyncTask(ctx context.Context, taskID string) {
	s.executeTask(ctx, taskID, false)
}

func (s *Service) executeTask(ctx context.Context, taskID string, isSyncInitial bool) {
	if !s.tryStartTask(taskID) {
		return
	}
	defer s.finishTask(taskID)
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

	errSummary := strings.Join(errs, "; ")
	_ = s.store.UpdateTaskStatus(ctx, task.ID, finalStatus, errSummary, nil, &fin)
	task.Status = finalStatus
	task.ErrorSummary = errSummary
	task.FinishedAt = &fin

	if finalStatus == "succeeded" && task.SpoolPath != "" && !task.SpoolCleaned {
		if err := os.Remove(task.SpoolPath); err == nil || errors.Is(err, os.ErrNotExist) {
			_ = s.store.MarkSpoolCleaned(ctx, task.ID)
		}
	}

	if task.WebhookStatus == "pending" {
		s.spawnWebhook(task.ID)
	}
	s.notifyBackupTaskResult(task)
}

func (s *Service) spawnWebhook(taskID string) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		wCtx, cancel := context.WithTimeout(s.appCtx, 20*time.Second)
		defer cancel()
		s.dispatchWebhook(wCtx, taskID)
	}()
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
	var stripped strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if r < 32 {
			continue
		}
		stripped.WriteRune(r)
	}
	clean := strings.ReplaceAll(stripped.String(), "\\", "/")
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

func sanitizeFilenameParts(name string) (string, bool) {
	base := filepath.Base(strings.ReplaceAll(strings.TrimSpace(name), "\\", "/"))
	if base == "" || base == "." || base == "/" || base == ".." {
		return "", false
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
	if res == "" || res == "." || res == ".." {
		return "", false
	}
	return res, true
}

func sanitizeFilename(name string) string {
	if res, ok := sanitizeFilenameParts(name); ok {
		return res
	}
	return "file.bin"
}

func sanitizeRename(name string) (string, error) {
	if strings.TrimSpace(name) == "" {
		return "", nil
	}
	res, ok := sanitizeFilenameParts(name)
	if !ok {
		return "", &HTTPError{
			Status:  http.StatusBadRequest,
			Code:    "invalid_filename",
			Message: "rename filename is invalid",
		}
	}
	return res, nil
}

func buildRemotePath(clientPrefix, subpath, filename string) string {
	var parts []string
	if cp := strings.Trim(strings.ReplaceAll(clientPrefix, "\\", "/"), "/"); cp != "" {
		parts = append(parts, cp)
	}
	if subpath != "" {
		parts = append(parts, subpath)
	}
	parts = append(parts, filename)
	return path.Join(parts...)
}
