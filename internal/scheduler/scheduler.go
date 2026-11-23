package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"safekeep/internal/audit"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/migration"
	"safekeep/internal/notify"
	"safekeep/internal/storage"
	"safekeep/internal/storage/dropbox"
)

var cronParser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

type jobMeta struct {
	DefaultSchedule string
	DefaultTimeout  int
}

var builtinJobMeta = map[string]jobMeta{
	"cleanup_audit_logs":    {DefaultSchedule: "0 2 * * *", DefaultTimeout: 300},
	"cleanup_task_records":  {DefaultSchedule: "0 3 * * *", DefaultTimeout: 300},
	"cleanup_orphan_spool":  {DefaultSchedule: "0 * * * *", DefaultTimeout: 300},
	"upstream_health_check": {DefaultSchedule: "*/30 * * * *", DefaultTimeout: 180},
	"dropbox_token_refresh": {DefaultSchedule: "0 */4 * * *", DefaultTimeout: 120},
	"migration_backup":      {DefaultSchedule: "0 4 * * *", DefaultTimeout: 300},
}

// DefaultScheduleFor returns the built-in default cron expression for a job_key.
func DefaultScheduleFor(jobKey string) string {
	if m, ok := builtinJobMeta[jobKey]; ok {
		return m.DefaultSchedule
	}
	return "0 4 * * *"
}

// DefaultTimeoutFor returns the built-in default timeout in seconds for a job_key.
func DefaultTimeoutFor(jobKey string) int {
	if m, ok := builtinJobMeta[jobKey]; ok && m.DefaultTimeout > 0 {
		return m.DefaultTimeout
	}
	return 300
}

// PreviewCron validates a cron expression and returns the next count execution times in local time.
func PreviewCron(expr string, count int) ([]time.Time, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, errors.New("cron expression is required")
	}
	sched, err := cronParser.Parse(expr)
	if err != nil {
		return nil, err
	}
	if count <= 0 {
		count = 5
	}
	out := make([]time.Time, 0, count)
	at := time.Now()
	for i := 0; i < count; i++ {
		at = sched.Next(at)
		if at.IsZero() {
			break
		}
		out = append(out, at)
	}
	return out, nil
}

// NormalizeJobConfig validates and normalizes config JSON for non-migration built-in jobs.
func NormalizeJobConfig(jobKey string, incoming json.RawMessage) (string, error) {
	var raw map[string]any
	if len(incoming) > 0 {
		if err := json.Unmarshal(incoming, &raw); err != nil {
			return "", fmt.Errorf("invalid config JSON: %w", err)
		}
	}
	switch jobKey {
	case "cleanup_audit_logs":
		days := intFromMap(raw, "retention_days", 90)
		if days < 1 || days > 36500 {
			return "", errors.New("retention_days must be between 1 and 36500")
		}
		b, _ := json.Marshal(map[string]int{"retention_days": days})
		return string(b), nil

	case "cleanup_task_records":
		days := intFromMap(raw, "retention_days", 180)
		if days < 1 || days > 36500 {
			return "", errors.New("retention_days must be between 1 and 36500")
		}
		b, _ := json.Marshal(map[string]int{"retention_days": days})
		return string(b), nil

	case "cleanup_orphan_spool":
		maxAge := intFromMap(raw, "max_age_hours", 24)
		if maxAge < 1 || maxAge > 8760 {
			return "", errors.New("max_age_hours must be between 1 and 8760")
		}
		failedHours := intFromMap(raw, "failed_spool_retention_hours", 72)
		if failedHours < 1 || failedHours > 8760 {
			return "", errors.New("failed_spool_retention_hours must be between 1 and 8760")
		}
		b, _ := json.Marshal(map[string]int{
			"max_age_hours":                maxAge,
			"failed_spool_retention_hours": failedHours,
		})
		return string(b), nil

	case "upstream_health_check":
		probeTimeout := intFromMap(raw, "probe_timeout_sec", 20)
		if probeTimeout < 1 || probeTimeout > 120 {
			return "", errors.New("probe_timeout_sec must be between 1 and 120")
		}
		b, _ := json.Marshal(map[string]int{"probe_timeout_sec": probeTimeout})
		return string(b), nil

	case "dropbox_token_refresh":
		return "{}", nil

	default:
		if len(incoming) == 0 {
			return "{}", nil
		}
		return string(incoming), nil
	}
}

// Engine wraps robfig/cron and executes built-in maintenance jobs.
type Engine struct {
	store        *db.Store
	registry     *storage.Registry
	auditLog     *audit.Logger
	notifier     *notify.Dispatcher
	logger       *slog.Logger
	cipher       *crypto.Cipher
	migSvc       *migration.Service
	localBaseURL string
	httpClient   *http.Client

	mu       sync.Mutex
	c        *cron.Cron
	entryIDs map[string]cron.EntryID
	running  map[string]bool
	appCtx   context.Context
}

func NewEngine(appCtx context.Context, store *db.Store, registry *storage.Registry, auditLog *audit.Logger, logger *slog.Logger) *Engine {
	return &Engine{
		store:      store,
		registry:   registry,
		auditLog:   auditLog,
		logger:     logger,
		httpClient: &http.Client{Timeout: 5 * time.Minute},
		entryIDs:   make(map[string]cron.EntryID),
		running:    make(map[string]bool),
		appCtx:     appCtx,
	}
}

// SetMigrationBackupDeps configures dependencies needed by the migration_backup job.
func (e *Engine) SetMigrationBackupDeps(cipher *crypto.Cipher, migSvc *migration.Service, localBaseURL string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.cipher = cipher
	e.migSvc = migSvc
	e.localBaseURL = strings.TrimRight(strings.TrimSpace(localBaseURL), "/")
}

// SetNotifier configures the shared email notification dispatcher.
func (e *Engine) SetNotifier(n *notify.Dispatcher) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.notifier = n
}

func (e *Engine) ensureNotifier() *notify.Dispatcher {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.notifier == nil && e.cipher != nil {
		cipher := e.cipher
		e.notifier = notify.NewDispatcher(e.appCtx,
			func(ctx context.Context) (notify.SMTPConfig, error) {
				return e.store.GetSMTPConfig(ctx, cipher)
			},
			func(ctx context.Context, action, resourceID, status string, detail map[string]any) {
				if e.auditLog != nil {
					e.auditLog.Record(ctx, audit.Event{
						ActorType: "system", ActorID: "notifier", ActorName: "smtp",
						Action: action, ResourceType: "alert_email", ResourceID: resourceID,
						Status: status, Detail: detail,
					})
				}
			},
		)
	}
	return e.notifier
}

func (e *Engine) Start() error {
	e.mu.Lock()
	e.c = cron.New(cron.WithParser(cronParser))
	e.c.Start()
	e.mu.Unlock()

	return e.ReloadAll(e.appCtx)
}

func (e *Engine) Stop() {
	e.mu.Lock()
	c := e.c
	n := e.notifier
	e.mu.Unlock()
	if c != nil {
		ctx := c.Stop()
		<-ctx.Done()
	}
	if n != nil {
		n.Wait()
	}
}

// ValidateCron validates a standard 5-field cron expression or descriptor.
func ValidateCron(expr string) error {
	_, err := PreviewCron(expr, 1)
	return err
}

// ReloadAll clears and re-registers all enabled cron jobs from SQLite.
func (e *Engine) ReloadAll(ctx context.Context) error {
	jobs, err := e.store.ListCronJobs(ctx)
	if err != nil {
		return err
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if e.c == nil {
		return nil
	}
	for id, entryID := range e.entryIDs {
		e.c.Remove(entryID)
		delete(e.entryIDs, id)
	}

	for _, job := range jobs {
		if !job.Enabled {
			_ = e.store.UpdateCronJobNextRun(ctx, job.ID, nil)
			continue
		}
		j := job
		entryID, err := e.c.AddFunc(j.CronExpr, func() {
			e.executeJob(e.appCtx, j.ID, "schedule", "scheduler")
		})
		if err != nil {
			e.logger.Error("register cron job failed", "job_key", j.JobKey, "expr", j.CronExpr, "err", err)
			continue
		}
		e.entryIDs[j.ID] = entryID
		entry := e.c.Entry(entryID)
		if !entry.Next.IsZero() {
			nxt := entry.Next.UTC()
			_ = e.store.UpdateCronJobNextRun(ctx, j.ID, &nxt)
		}
	}
	return nil
}

// RunNow triggers a cron job immediately detached from HTTP request cancellation.
func (e *Engine) RunNow(_ context.Context, jobID, actorName string) (*db.CronJobRun, error) {
	if _, err := e.store.GetCronJobByID(e.appCtx, jobID); err != nil {
		return nil, err
	}
	run := e.executeJob(e.appCtx, jobID, "manual", actorName)
	return run, nil
}

func (e *Engine) executeJob(ctx context.Context, jobID, triggerType, triggeredBy string) *db.CronJobRun {
	e.mu.Lock()
	if e.running[jobID] {
		e.mu.Unlock()
		now := time.Now().UTC()
		run := &db.CronJobRun{
			ID:          crypto.NewULID(),
			JobID:       jobID,
			TriggerType: triggerType,
			TriggeredBy: triggeredBy,
			Status:      "skipped",
			OutputLog:   "上一次执行尚未结束，跳过本次",
			StartedAt:   now,
			FinishedAt:  &now,
		}
		_ = e.store.InsertCronJobRun(ctx, run)
		return run
	}
	e.running[jobID] = true
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.running, jobID)
		e.mu.Unlock()
	}()

	job, err := e.store.GetCronJobByID(ctx, jobID)
	if err != nil {
		return &db.CronJobRun{JobID: jobID, Status: "failed", OutputLog: err.Error(), StartedAt: time.Now().UTC()}
	}

	timeoutSec := job.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = DefaultTimeoutFor(job.JobKey)
	}
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	start := time.Now().UTC()
	run := &db.CronJobRun{
		ID:          crypto.NewULID(),
		JobID:       job.ID,
		TriggerType: triggerType,
		TriggeredBy: triggeredBy,
		Status:      "running",
		StartedAt:   start,
	}
	_ = e.store.InsertCronJobRun(ctx, run)

	affected, summary, execErr := e.dispatchJob(runCtx, job)
	fin := time.Now().UTC()
	durationMs := fin.Sub(start).Milliseconds()

	status := "succeeded"
	if execErr != nil {
		status = "failed"
		if errors.Is(execErr, context.DeadlineExceeded) || runCtx.Err() == context.DeadlineExceeded {
			status = "timeout"
		}
		if summary == "" {
			summary = execErr.Error()
		} else {
			summary = summary + "; err: " + execErr.Error()
		}
	}

	run.Status = status
	run.AffectedRows = affected
	run.OutputLog = summary
	run.FinishedAt = &fin
	finishCtx, cancelFinish := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancelFinish()
	_ = e.store.FinishCronJobRun(finishCtx, run.ID, status, affected, summary, fin)

	var nextRun *time.Time
	e.mu.Lock()
	if e.c != nil {
		if entryID, ok := e.entryIDs[job.ID]; ok {
			nxt := e.c.Entry(entryID).Next
			if !nxt.IsZero() {
				u := nxt.UTC()
				nextRun = &u
			}
		}
	}
	e.mu.Unlock()

	_ = e.store.UpdateCronJobExecution(finishCtx, job.ID, status, summary, durationMs, start, nextRun)

	if e.auditLog != nil {
		actorType := "system"
		if triggerType == "manual" {
			actorType = "admin"
		}
		e.auditLog.Record(finishCtx, audit.Event{
			ActorType:    actorType,
			ActorID:      triggeredBy,
			ActorName:    triggeredBy,
			Action:       "cron.execute",
			ResourceType: "cron_job",
			ResourceID:   job.ID,
			Status:       map[bool]string{true: "success", false: "failure"}[execErr == nil],
			Detail: map[string]any{
				"job_key":       job.JobKey,
				"trigger_type":  triggerType,
				"affected_rows": affected,
				"summary":       summary,
				"duration_ms":   durationMs,
			},
		})
	}
	if job.JobKey == "migration_backup" {
		e.notifyMigrationBackup(job, triggerType, triggeredBy, status, summary, durationMs)
	}

	return run
}

func (e *Engine) dispatchJob(ctx context.Context, job *db.CronJob) (int64, string, error) {
	var cfg map[string]any
	_ = json.Unmarshal([]byte(job.ConfigJSON), &cfg)

	switch job.JobKey {
	case "cleanup_audit_logs":
		days := intFromMap(cfg, "retention_days", 90)
		if days < 1 {
			days = 1
		}
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		n, err := e.store.DeleteAuditLogsBefore(ctx, cutoff)
		if err != nil {
			return 0, "", err
		}
		return n, fmt.Sprintf("deleted %d audit logs older than %d days", n, days), nil

	case "cleanup_task_records":
		days := intFromMap(cfg, "retention_days", 180)
		if days < 1 {
			days = 1
		}
		cutoff := time.Now().UTC().AddDate(0, 0, -days)
		n, err := e.store.DeleteCompletedTasksBefore(ctx, cutoff)
		if err != nil {
			return 0, "", err
		}
		return n, fmt.Sprintf("deleted %d completed task/cron records older than %d days", n, days), nil

	case "cleanup_orphan_spool":
		maxAgeHours := intFromMap(cfg, "max_age_hours", 24)
		if maxAgeHours < 1 {
			maxAgeHours = 1
		}
		failedHours := intFromMap(cfg, "failed_spool_retention_hours", 72)
		if failedHours < 1 {
			failedHours = 72
		}
		settings, err := e.store.GetSystemSettings(ctx)
		if err != nil {
			return 0, "", err
		}

		// Clean spool files for permanently failed tasks older than failedHours so they don't leak forever.
		var failedRemoved int64
		if failedTasks, fErr := e.store.ListExpiredFailedTaskSpools(ctx, time.Now().UTC().Add(-time.Duration(failedHours)*time.Hour)); fErr == nil {
			for _, ft := range failedTasks {
				if err := os.Remove(ft.SpoolPath); err == nil || os.IsNotExist(err) {
					_ = e.store.MarkSpoolCleaned(ctx, ft.ID)
					failedRemoved++
				}
			}
		}

		activeSet, err := e.store.ListActiveSpoolPaths(ctx)
		if err != nil {
			return 0, "", err
		}
		entries, err := os.ReadDir(settings.SpoolDir)
		if err != nil {
			if os.IsNotExist(err) {
				return failedRemoved, "spool directory does not exist yet", nil
			}
			return failedRemoved, "", err
		}
		cutoff := time.Now().Add(-time.Duration(maxAgeHours) * time.Hour)
		var removed int64
		for _, ent := range entries {
			if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".spool") {
				continue
			}
			full := filepath.Join(settings.SpoolDir, ent.Name())
			if _, keep := activeSet[full]; keep {
				continue
			}
			info, err := ent.Info()
			if err != nil {
				continue
			}
			if info.ModTime().Before(cutoff) {
				if err := os.Remove(full); err == nil {
					removed++
				}
			}
		}
		total := removed + failedRemoved
		return total, fmt.Sprintf("removed %d orphan .spool files and %d expired failed-task spools", removed, failedRemoved), nil

	case "upstream_health_check":
		probeTimeoutSec := intFromMap(cfg, "probe_timeout_sec", 20)
		if probeTimeoutSec < 1 {
			probeTimeoutSec = 1
		}
		if probeTimeoutSec > 120 {
			probeTimeoutSec = 120
		}
		ups, err := e.store.ListUpstreams(ctx)
		if err != nil {
			return 0, "", err
		}
		var checked, healthy, failed int64
		var newlyFailed []string
		var allFailed []string
		for _, u := range ups {
			if !u.Enabled {
				continue
			}
			checked++
			p, err := e.registry.BuildFromUpstream(ctx, &u)
			if err == nil {
				tCtx, cancel := context.WithTimeout(ctx, time.Duration(probeTimeoutSec)*time.Second)
				err = p.TestConnection(tCtx)
				cancel()
			}
			if err != nil {
				failed++
				_ = e.store.UpdateUpstreamTestStatus(ctx, u.ID, "failed", err.Error())
				line := fmt.Sprintf("  - %s (%s): %v", u.Name, u.Type, err)
				allFailed = append(allFailed, line)
				if u.LastTestStatus != "failed" {
					newlyFailed = append(newlyFailed, line)
				}
			} else {
				healthy++
				_ = e.store.UpdateUpstreamTestStatus(ctx, u.ID, "ok", "")
			}
		}
		if len(newlyFailed) > 0 {
			if n := e.ensureNotifier(); n != nil {
				subject := fmt.Sprintf("[SafeKeep] 上游存储健康检查异常 (%d 个不可用)", failed)
				body := fmt.Sprintf(
					"SafeKeep 定时上游健康检查发现新异常：\n\n"+
						"- 本次检查启用的上游数: %d (正常 %d / 异常 %d)\n"+
						"- 新增异常上游:\n%s\n",
					checked, healthy, failed,
					strings.Join(newlyFailed, "\n"),
				)
				if len(allFailed) > len(newlyFailed) {
					body += fmt.Sprintf("- 当前全部异常上游:\n%s\n", strings.Join(allFailed, "\n"))
				}
				body += fmt.Sprintf("- 检查时间: %s\n", time.Now().Format(time.RFC3339))
				n.Dispatch(notify.Event{
					EventKey:   "upstream_health_check",
					ResourceID: job.ID,
					Subject:    subject,
					Body:       body,
					ShouldSend: func(r notify.NotifyRules) bool {
						return r.UpstreamHealthCheck
					},
				})
			}
		}
		return checked, fmt.Sprintf("checked %d enabled upstreams: %d ok, %d failed", checked, healthy, failed), nil

	case "dropbox_token_refresh":
		ups, err := e.store.ListUpstreams(ctx)
		if err != nil {
			return 0, "", err
		}
		var refreshed, failed int64
		for _, u := range ups {
			if !u.Enabled || u.Type != "dropbox" {
				continue
			}
			p, err := e.registry.BuildFromUpstream(ctx, &u)
			if err != nil {
				failed++
				continue
			}
			if dp, ok := p.(*dropbox.Provider); ok {
				rCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				err = dp.RefreshNow(rCtx)
				cancel()
				if err != nil {
					failed++
				} else {
					refreshed++
					e.registry.Invalidate(u.ID)
				}
			}
		}
		if failed > 0 {
			return refreshed, fmt.Sprintf("refreshed %d dropbox upstreams (%d failed)", refreshed, failed), fmt.Errorf("%d dropbox refresh failed", failed)
		}
		return refreshed, fmt.Sprintf("refreshed %d dropbox upstreams", refreshed), nil

	case "migration_backup":
		return e.runMigrationBackup(ctx, job.ConfigJSON)

	default:
		return 0, "", fmt.Errorf("unknown job_key %q", job.JobKey)
	}
}

func (e *Engine) notifyMigrationBackup(job *db.CronJob, triggerType, triggeredBy, status, summary string, durationMs int64) {
	n := e.ensureNotifier()
	if n == nil || job == nil {
		return
	}
	succeeded := status == "succeeded"
	statusLabel := "成功"
	if !succeeded {
		statusLabel = "失败"
		if status == "timeout" {
			statusLabel = "超时"
		}
	}
	triggerLabel := "定时调度"
	if triggerType == "manual" {
		triggerLabel = "手动触发 (" + triggeredBy + ")"
	}
	subject := fmt.Sprintf("[SafeKeep] 环境迁移包自动备份%s", statusLabel)
	body := fmt.Sprintf(
		"SafeKeep 环境迁移包自动备份任务执行结束：\n\n"+
			"- 触发方式: %s\n"+
			"- 执行结果: %s\n"+
			"- 耗时: %d ms\n"+
			"- 摘要: %s\n"+
			"- 完成时间: %s\n",
		triggerLabel, statusLabel, durationMs, summary, time.Now().Format(time.RFC3339),
	)
	n.Dispatch(notify.Event{
		EventKey:   "migration_backup",
		ResourceID: job.ID,
		Subject:    subject,
		Body:       body,
		ShouldSend: func(r notify.NotifyRules) bool {
			return r.MigrationBackup.ShouldNotify(succeeded)
		},
	})
}

func intFromMap(m map[string]any, key string, fallback int) int {
	if m == nil {
		return fallback
	}
	v, ok := m[key]
	if !ok {
		return fallback
	}
	switch t := v.(type) {
	case float64:
		return int(t)
	case int:
		return t
	default:
		return fallback
	}
}
