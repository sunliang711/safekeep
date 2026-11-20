package scheduler

import (
	"context"
	"encoding/json"
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
	"safekeep/internal/storage"
	"safekeep/internal/storage/dropbox"
)

// Engine wraps robfig/cron and executes built-in maintenance jobs.
type Engine struct {
	store        *db.Store
	registry     *storage.Registry
	auditLog     *audit.Logger
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

func (e *Engine) Start() error {
	e.mu.Lock()
	e.c = cron.New(cron.WithParser(cron.NewParser(
		cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
	)))
	e.c.Start()
	e.mu.Unlock()

	return e.ReloadAll(e.appCtx)
}

func (e *Engine) Stop() {
	e.mu.Lock()
	c := e.c
	e.mu.Unlock()
	if c != nil {
		ctx := c.Stop()
		<-ctx.Done()
	}
}

// ValidateCron validates a standard 5-field cron expression or descriptor.
func ValidateCron(expr string) error {
	p := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor)
	_, err := p.Parse(strings.TrimSpace(expr))
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
	runCtx, cancel := context.WithTimeout(e.appCtx, 5*time.Minute)
	defer cancel()
	if _, err := e.store.GetCronJobByID(runCtx, jobID); err != nil {
		return nil, err
	}
	run := e.executeJob(runCtx, jobID, "manual", actorName)
	return run, nil
}

func (e *Engine) executeJob(ctx context.Context, jobID, triggerType, triggeredBy string) *db.CronJobRun {
	e.mu.Lock()
	if e.running[jobID] {
		e.mu.Unlock()
		return &db.CronJobRun{
			JobID:     jobID,
			Status:    "failed",
			OutputLog: "job is already running",
			StartedAt: time.Now().UTC(),
		}
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

	affected, summary, execErr := e.dispatchJob(ctx, job)
	fin := time.Now().UTC()
	durationMs := fin.Sub(start).Milliseconds()

	status := "succeeded"
	if execErr != nil {
		status = "failed"
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
	_ = e.store.FinishCronJobRun(ctx, run.ID, status, affected, summary, fin)

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

	_ = e.store.UpdateCronJobExecution(ctx, job.ID, status, summary, durationMs, start, nextRun)

	if e.auditLog != nil {
		actorType := "system"
		if triggerType == "manual" {
			actorType = "admin"
		}
		e.auditLog.Record(ctx, audit.Event{
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
		ups, err := e.store.ListUpstreams(ctx)
		if err != nil {
			return 0, "", err
		}
		var checked, healthy, failed int64
		for _, u := range ups {
			if !u.Enabled {
				continue
			}
			checked++
			p, err := e.registry.BuildFromUpstream(ctx, &u)
			if err == nil {
				tCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
				err = p.TestConnection(tCtx)
				cancel()
			}
			if err != nil {
				failed++
				_ = e.store.UpdateUpstreamTestStatus(ctx, u.ID, "failed", err.Error())
			} else {
				healthy++
				_ = e.store.UpdateUpstreamTestStatus(ctx, u.ID, "ok", "")
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
