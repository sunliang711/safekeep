package notify

import (
	"context"
	"sync"
)

// ConfigLoader loads the current effective SMTPConfig.
type ConfigLoader func(ctx context.Context) (SMTPConfig, error)

// AuditRecorder records outbound email delivery attempts into the audit log.
type AuditRecorder func(ctx context.Context, action, resourceID, status string, detail map[string]any)

// Event describes an asynchronous email notification request gated by NotifyRules.
type Event struct {
	EventKey    string // logical event key, e.g. "backup.upload", "remote_backup.execute"
	AuditAction string // audit_logs.action; defaults to "notify.email" if empty
	ResourceID  string
	Subject     string
	Body        string
	ShouldSend  func(NotifyRules) bool
}

// Dispatcher evaluates notification rules and sends outbound emails asynchronously.
type Dispatcher struct {
	appCtx   context.Context
	loadCfg  ConfigLoader
	record   AuditRecorder
	wg       sync.WaitGroup
}

// NewDispatcher creates a shared asynchronous notification dispatcher.
func NewDispatcher(appCtx context.Context, loadCfg ConfigLoader, record AuditRecorder) *Dispatcher {
	if appCtx == nil {
		appCtx = context.Background()
	}
	return &Dispatcher{
		appCtx:  appCtx,
		loadCfg: loadCfg,
		record:  record,
	}
}

// Wait blocks until all in-flight notification deliveries finish.
func (d *Dispatcher) Wait() {
	if d == nil {
		return
	}
	d.wg.Wait()
}

// Dispatch loads SMTP configuration, checks whether the event should trigger an email,
// sends it asynchronously, and writes an audit record when enabled.
func (d *Dispatcher) Dispatch(ev Event) {
	if d == nil || d.loadCfg == nil {
		return
	}
	d.wg.Add(1)
	go func() {
		defer d.wg.Done()
		ctx, cancel := context.WithTimeout(d.appCtx, Timeout)
		defer cancel()

		auditAction := ev.AuditAction
		if auditAction == "" {
			auditAction = "notify.email"
		}

		cfg, err := d.loadCfg(ctx)
		if err != nil {
			if d.record != nil {
				detail := map[string]any{"error": err.Error()}
				if ev.EventKey != "" {
					detail["event"] = ev.EventKey
				}
				d.record(ctx, auditAction, ev.ResourceID, "failure", detail)
			}
			return
		}
		if !cfg.Enabled {
			return
		}
		rules := cfg.Notifications.Normalize()
		if ev.ShouldSend != nil && !ev.ShouldSend(rules) {
			return
		}

		sendErr := Send(ctx, cfg, ev.Subject, ev.Body)
		if d.record != nil {
			status := "success"
			detail := map[string]any{
				"subject": ev.Subject,
				"to":      cfg.ToEmails,
			}
			if ev.EventKey != "" {
				detail["event"] = ev.EventKey
			}
			if sendErr != nil {
				status = "failure"
				detail["error"] = sendErr.Error()
			}
			d.record(ctx, auditAction, ev.ResourceID, status, detail)
		}
	}()
}
