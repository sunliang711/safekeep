package server

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"safekeep/internal/audit"
	"safekeep/internal/backup"
	"safekeep/internal/config"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/migration"
	"safekeep/internal/notify"
	"safekeep/internal/scheduler"
)

// --- Backup Tasks & Restore ---

func (s *Server) handleAdminListTasks(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	tasks, total, err := s.store.ListBackupTasks(r.Context(), db.TaskFilter{
		ClientID: q.Get("client_id"),
		Status:   q.Get("status"),
		Mode:     q.Get("mode"),
		Query:    q.Get("q"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": tasks,
		"total": total,
	})
}

func (s *Server) handleAdminGetTask(w http.ResponseWriter, r *http.Request) {
	task, err := s.store.GetBackupTask(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleAdminRetryTask(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	settings, _ := s.store.GetSystemSettings(r.Context())
	maxAttempts := settings.MaxRetryAttempts
	if maxAttempts <= 0 {
		maxAttempts = 3
	}
	n, err := s.store.RequeueFailedItems(r.Context(), id, maxAttempts)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "retry_failed", err.Error())
		return
	}
	if n == 0 {
		writeError(w, http.StatusBadRequest, "nothing_to_retry", "no failed items found for this task")
		return
	}
	if s.pool != nil {
		s.pool.Enqueue(id)
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "task.retry",
		ResourceType: "backup_task",
		ResourceID:   id,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"requeued_items": n},
	})
	task, _ := s.store.GetBackupTask(r.Context(), id)
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleAdminDownloadTask(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	task, err := s.store.GetBackupTask(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	rc, size, ct, filename, err := s.backupSvc.DownloadTaskFile(r.Context(), task, r.URL.Query().Get("upstream_id"))
	if err != nil {
		var he *backup.HTTPError
		if errors.As(err, &he) {
			writeError(w, he.Status, he.Code, he.Message)
			return
		}
		writeError(w, http.StatusBadGateway, "download_failed", err.Error())
		return
	}
	defer rc.Close()

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "task.download",
		ResourceType: "backup_task",
		ResourceID:   id,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename*=UTF-8''%s`, url.PathEscape(filename)))
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

// --- Cron Jobs ---

func (s *Server) handleListCronJobs(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListCronJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": jobs})
}

func (s *Server) handleUpdateCronJob(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	var req struct {
		CronExpr string          `json:"cron_expr"`
		Enabled  bool            `json:"enabled"`
		Config   json.RawMessage `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if err := scheduler.ValidateCron(req.CronExpr); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_cron", fmt.Sprintf("invalid cron expression: %v", err))
		return
	}
	cfgStr := "{}"
	if len(req.Config) > 0 && json.Valid(req.Config) {
		cfgStr = string(req.Config)
	}
	if err := s.store.UpdateCronJob(r.Context(), id, strings.TrimSpace(req.CronExpr), req.Enabled, cfgStr); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "cron job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	if s.sched != nil {
		_ = s.sched.ReloadAll(r.Context())
	}

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "cron.update",
		ResourceType: "cron_job",
		ResourceID:   id,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"cron_expr": req.CronExpr, "enabled": req.Enabled},
	})
	job, _ := s.store.GetCronJobByID(r.Context(), id)
	writeJSON(w, http.StatusOK, job)
}

func (s *Server) handleRunCronJob(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	if s.sched == nil {
		writeError(w, http.StatusServiceUnavailable, "no_scheduler", "scheduler not running")
		return
	}
	run, err := s.sched.RunNow(r.Context(), id, admin.Username)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "cron job not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "run_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleListCronJobRuns(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	runs, err := s.store.ListCronJobRuns(r.Context(), id, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs})
}

// --- Audit Logs ---

func (s *Server) handleListAuditLogs(w http.ResponseWriter, r *http.Request) {
	f := parseAuditFilter(r)
	logs, total, err := s.store.ListAuditLogs(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": logs, "total": total})
}

func (s *Server) handleExportAuditLogs(w http.ResponseWriter, r *http.Request) {
	f := parseAuditFilter(r)
	logs, err := s.store.ExportAllAuditLogs(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export_failed", err.Error())
		return
	}
	format := strings.ToLower(r.URL.Query().Get("format"))
	if format == "json" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="audit-logs.json"`)
		_ = json.NewEncoder(w).Encode(logs)
		return
	}

	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="audit-logs.csv"`)
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{"id", "created_at", "actor_type", "actor_name", "action", "resource_type", "resource_id", "status", "ip_address", "detail_json"})
	for _, l := range logs {
		_ = cw.Write([]string{
			strconv.FormatInt(l.ID, 10),
			l.CreatedAt.Format(time.RFC3339),
			l.ActorType,
			l.ActorName,
			l.Action,
			l.ResourceType,
			l.ResourceID,
			l.Status,
			l.IPAddress,
			l.DetailJSON,
		})
	}
	cw.Flush()
}

func parseAuditFilter(r *http.Request) db.AuditFilter {
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	f := db.AuditFilter{
		ActorType: q.Get("actor_type"),
		Action:    q.Get("action"),
		Status:    q.Get("status"),
		Query:     q.Get("q"),
		Limit:     limit,
		Offset:    offset,
	}
	if fromStr := q.Get("from"); fromStr != "" {
		if t, err := time.Parse(time.RFC3339, fromStr); err == nil {
			f.From = &t
		}
	}
	if toStr := q.Get("to"); toStr != "" {
		if t, err := time.Parse(time.RFC3339, toStr); err == nil {
			f.To = &t
		}
	}
	return f
}

// --- System Settings & Admins ---

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetSystemSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, settings)
}

func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	var settings config.SystemSettings
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(settings.SpoolDir) == "" || settings.WorkerConcurrency <= 0 || settings.SyncTimeoutSec <= 0 || settings.MaxFileSizeBytes <= 0 || settings.DailyTrafficLimitBytes < 0 {
		writeError(w, http.StatusBadRequest, "validation_error", "spool_dir, max_file_size_bytes > 0, daily_traffic_limit_bytes >= 0, worker_concurrency > 0, and sync_timeout_sec > 0 are required")
		return
	}
	if filepath.Clean(settings.SpoolDir) == filepath.Clean(s.store.DataDir()) {
		writeError(w, http.StatusBadRequest, "invalid_spool_dir", "spool_dir must be a dedicated subdirectory, not the root data_dir")
		return
	}
	if err := s.store.UpdateSystemSettings(r.Context(), settings, admin.Username); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "settings.update",
		ResourceType: "system_settings",
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       settings,
	})
	writeJSON(w, http.StatusOK, settings)
}

// --- SMTP & Circuit Breaker ---

type smtpDTO struct {
	Enabled     bool     `json:"enabled"`
	Host        string   `json:"host"`
	Port        int      `json:"port"`
	TLSMode     string   `json:"tls_mode"`
	Username    string   `json:"username"`
	HasPassword bool     `json:"has_password"`
	FromEmail   string   `json:"from_email"`
	FromName    string   `json:"from_name"`
	ToEmails    []string `json:"to_emails"`
}

// smtpAdminDTO is returned only to authenticated admin UI requests so the
// saved authorization code can be copied while audit logs stay redacted.
type smtpAdminDTO struct {
	smtpDTO
	Password string `json:"password,omitempty"`
}

func toSMTPDTO(c notify.SMTPConfig) smtpDTO {
	to := c.ToEmails
	if to == nil {
		to = []string{}
	}
	return smtpDTO{
		Enabled:     c.Enabled,
		Host:        c.Host,
		Port:        c.Port,
		TLSMode:     c.TLSMode,
		Username:    c.Username,
		HasPassword: c.Password != "" || c.PasswordEncrypted != "",
		FromEmail:   c.FromEmail,
		FromName:    c.FromName,
		ToEmails:    to,
	}
}

func toSMTPAdminDTO(c notify.SMTPConfig) smtpAdminDTO {
	return smtpAdminDTO{
		smtpDTO:  toSMTPDTO(c),
		Password: c.Password,
	}
}

type smtpUpdateReq struct {
	Enabled       bool     `json:"enabled"`
	Host          string   `json:"host"`
	Port          int      `json:"port"`
	TLSMode       string   `json:"tls_mode"`
	Username      string   `json:"username"`
	Password      *string  `json:"password"`
	ClearPassword bool     `json:"clear_password"`
	FromEmail     string   `json:"from_email"`
	FromName      string   `json:"from_name"`
	ToEmails      []string `json:"to_emails"`
}

func (s *Server) mergeSMTPRequest(existing notify.SMTPConfig, req smtpUpdateReq) notify.SMTPConfig {
	cfg := notify.SMTPConfig{
		Enabled:   req.Enabled,
		Host:      strings.TrimSpace(req.Host),
		Port:      req.Port,
		TLSMode:   strings.ToLower(strings.TrimSpace(req.TLSMode)),
		Username:  strings.TrimSpace(req.Username),
		FromEmail: strings.TrimSpace(req.FromEmail),
		FromName:  strings.TrimSpace(req.FromName),
		ToEmails:  req.ToEmails,
	}
	if cfg.Port == 0 {
		cfg.Port = 587
	}
	if cfg.TLSMode == "" {
		cfg.TLSMode = "starttls"
	}
	if cfg.FromName == "" {
		cfg.FromName = "SafeKeep"
	}
	if req.ClearPassword {
		cfg.Password = ""
		cfg.PasswordEncrypted = ""
	} else if req.Password != nil && *req.Password != "" {
		cfg.Password = *req.Password
	} else {
		cfg.Password = existing.Password
		cfg.PasswordEncrypted = existing.PasswordEncrypted
	}
	return cfg
}

func (s *Server) handleGetSMTPSettings(w http.ResponseWriter, r *http.Request) {
	cfg, err := s.store.GetSMTPConfig(r.Context(), s.cipher)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get_smtp_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toSMTPAdminDTO(cfg))
}

func (s *Server) handleUpdateSMTPSettings(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	var req smtpUpdateReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	existing, _ := s.store.GetSMTPConfig(r.Context(), s.cipher)
	cfg := s.mergeSMTPRequest(existing, req)
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_smtp", err.Error())
		return
	}
	if err := s.store.SaveSMTPConfig(r.Context(), s.cipher, cfg, admin.Username); err != nil {
		writeError(w, http.StatusInternalServerError, "save_smtp_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "settings.smtp_update",
		ResourceType: "smtp_config",
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       toSMTPDTO(cfg),
	})
	saved, _ := s.store.GetSMTPConfig(r.Context(), s.cipher)
	writeJSON(w, http.StatusOK, toSMTPAdminDTO(saved))
}

func (s *Server) handleTestSMTPSettings(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	existing, err := s.store.GetSMTPConfig(r.Context(), s.cipher)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "get_smtp_failed", err.Error())
		return
	}

	cfg := existing
	var req smtpUpdateReq
	if r.Body != nil {
		decErr := json.NewDecoder(r.Body).Decode(&req)
		if decErr == nil {
			cfg = s.mergeSMTPRequest(existing, req)
			cfg.Enabled = true
		} else if !errors.Is(decErr, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid_json", decErr.Error())
			return
		}
	}
	cfg.Enabled = true

	sendCtx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	sendErr := notify.Send(sendCtx, cfg, "[SafeKeep] SMTP 测试邮件", fmt.Sprintf("这是一封来自 SafeKeep 的 SMTP 测试邮件。\n\n触发管理员: %s\n时间: %s\n", admin.Username, time.Now().Format(time.RFC3339)))
	detail := map[string]any{"host": cfg.Host, "port": cfg.Port, "to": cfg.ToEmails}
	if sendErr != nil {
		detail["error"] = sendErr.Error()
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "settings.smtp_test",
		ResourceType: "smtp_config",
		Status:       map[bool]string{true: "success", false: "failure"}[sendErr == nil],
		IPAddress:    clientIP(r),
		Detail:       detail,
	})
	if sendErr != nil {
		writeError(w, http.StatusBadGateway, "smtp_test_failed", sendErr.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status": "ok",
		"host":   cfg.Host,
		"port":   cfg.Port,
		"to":     cfg.ToEmails,
	})
}

func (s *Server) handleResetCircuitBreaker(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	dateKey := db.TodayDateKey()
	if err := s.store.ResetDailyCircuitBreaker(r.Context(), dateKey); err != nil {
		writeError(w, http.StatusInternalServerError, "reset_failed", err.Error())
		return
	}
	settings, _ := s.store.GetSystemSettings(r.Context())
	dt, _ := s.store.GetDailyTraffic(r.Context(), dateKey, settings.DailyTrafficLimitBytes)
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "circuit_breaker.reset",
		ResourceType: "daily_traffic",
		ResourceID:   dateKey,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})
	writeJSON(w, http.StatusOK, dt)
}

func (s *Server) handleListAdmins(w http.ResponseWriter, r *http.Request) {
	admins, err := s.store.ListAdmins(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": admins})
}

func (s *Server) handleCreateAdmin(w http.ResponseWriter, r *http.Request) {
	actor := currentAdmin(r)
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "validation_error", "username >= 3 chars and password >= 8 chars required")
		return
	}
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", err.Error())
		return
	}
	a := &db.Admin{
		ID:           crypto.NewULID(),
		Username:     req.Username,
		PasswordHash: hash,
		IsActive:     true,
	}
	if err := s.store.CreateAdmin(r.Context(), a); err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      actor.ID,
		ActorName:    actor.Username,
		Action:       "admin.create",
		ResourceType: "admin",
		ResourceID:   a.ID,
		IPAddress:    clientIP(r),
		Detail:       map[string]any{"username": a.Username},
	})
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleUpdateAdmin(w http.ResponseWriter, r *http.Request) {
	actor := currentAdmin(r)
	id := chi.URLParam(r, "id")
	var req struct {
		IsActive *bool   `json:"is_active"`
		Password *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if req.IsActive != nil {
		if id == actor.ID && !*req.IsActive {
			writeError(w, http.StatusBadRequest, "cannot_disable_self", "cannot disable your own admin account")
			return
		}
		if err := s.store.UpdateAdminActive(r.Context(), id, *req.IsActive); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "admin not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
			return
		}
	}
	if req.Password != nil && *req.Password != "" {
		if len(*req.Password) < 8 {
			writeError(w, http.StatusBadRequest, "weak_password", "password must be at least 8 characters")
			return
		}
		hash, err := crypto.HashPassword(*req.Password)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "hash_error", err.Error())
			return
		}
		if err := s.store.UpdateAdminPassword(r.Context(), id, hash); err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusNotFound, "not_found", "admin not found")
				return
			}
			writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
			return
		}
	}
	a, err := s.store.GetAdminByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "admin not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      actor.ID,
		ActorName:    actor.Username,
		Action:       "admin.update",
		ResourceType: "admin",
		ResourceID:   id,
		IPAddress:    clientIP(r),
	})
	writeJSON(w, http.StatusOK, a)
}

// --- Migration Export & Import ---

func (s *Server) handleMigrationExport(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	var opts migration.ExportOptions
	if err := json.NewDecoder(r.Body).Decode(&opts); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	bundleBytes, err := s.migSvc.Export(r.Context(), opts)
	if err != nil {
		writeError(w, http.StatusBadRequest, "export_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "migration.export",
		ResourceType: "migration",
		IPAddress:    clientIP(r),
		Detail: map[string]any{
			"include_tasks":      opts.IncludeTasks,
			"include_admins":     opts.IncludeAdmins,
			"include_audit_logs": opts.IncludeAuditLogs,
			"size_bytes":         len(bundleBytes),
		},
	})
	filename := fmt.Sprintf("safekeep-migration-%s.skbundle", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bundleBytes)
}

func (s *Server) handleMigrationInspect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_multipart", err.Error())
		return
	}
	passphrase := r.FormValue("passphrase")
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_bundle", "bundle file is required")
		return
	}
	defer file.Close()
	blob, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_failed", err.Error())
		return
	}
	summary, err := s.migSvc.Inspect(r.Context(), blob, passphrase)
	if err != nil {
		writeError(w, http.StatusBadRequest, "inspect_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, summary)
}

func (s *Server) handleMigrationImport(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_multipart", err.Error())
		return
	}
	file, _, err := r.FormFile("bundle")
	if err != nil {
		writeError(w, http.StatusBadRequest, "missing_bundle", "bundle file is required")
		return
	}
	defer file.Close()
	blob, err := io.ReadAll(io.LimitReader(file, 64<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read_failed", err.Error())
		return
	}

	opts := migration.ImportOptions{
		Passphrase:       r.FormValue("passphrase"),
		Mode:             r.FormValue("mode"),
		ConflictStrategy: r.FormValue("conflict_strategy"),
		IncludeTasks:     r.FormValue("include_tasks") == "true",
		IncludeAdmins:    r.FormValue("include_admins") == "true",
		IncludeAuditLogs: r.FormValue("include_audit_logs") == "true",
	}

	counts, err := s.migSvc.Import(r.Context(), blob, opts)
	if err != nil {
		s.auditLog.Record(r.Context(), audit.Event{
			ActorType:    "admin",
			ActorID:      admin.ID,
			ActorName:    admin.Username,
			Action:       "migration.import",
			ResourceType: "migration",
			Status:       "failure",
			IPAddress:    clientIP(r),
			Detail:       map[string]any{"error": err.Error()},
		})
		writeError(w, http.StatusBadRequest, "import_failed", err.Error())
		return
	}

	s.registry.InvalidateAll()
	if s.sched != nil {
		_ = s.sched.ReloadAll(r.Context())
	}

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "migration.import",
		ResourceType: "migration",
		IPAddress:    clientIP(r),
		Detail:       map[string]any{"mode": opts.Mode, "conflict_strategy": opts.ConflictStrategy, "counts": counts},
	})
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts})
}
