package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"safekeep/internal/db"
)

func TestCronSchedulePreviewTimeoutAndInlineToggle(t *testing.T) {
	env := setupTestEnv(t)

	// Setup admin
	resp := env.adminReq(t, http.MethodPost, "/admin/api/v1/setup/init", map[string]string{
		"username": "admin",
		"password": "password123",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup init failed: %d", resp.StatusCode)
	}

	// 1. Preview valid & invalid cron schedule
	prevResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/cron-jobs/preview", map[string]string{
		"schedule": "*/15 * * * *",
	})
	var prevOut struct {
		NextRuns []string `json:"next_runs"`
	}
	_ = json.NewDecoder(prevResp.Body).Decode(&prevOut)
	prevResp.Body.Close()
	if prevResp.StatusCode != http.StatusOK || len(prevOut.NextRuns) != 5 {
		t.Fatalf("expected 5 preview times, got status=%d out=%+v", prevResp.StatusCode, prevOut)
	}

	badPrev := env.adminReq(t, http.MethodPost, "/admin/api/v1/cron-jobs/preview", map[string]string{
		"schedule": "invalid cron",
	})
	badPrev.Body.Close()
	if badPrev.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid schedule preview, got %d", badPrev.StatusCode)
	}

	// 2. List jobs includes default_schedule, timeout_sec, and next_runs
	listResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/cron-jobs", nil)
	var listOut struct {
		Items []db.CronJob `json:"items"`
	}
	_ = json.NewDecoder(listResp.Body).Decode(&listOut)
	listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK || len(listOut.Items) < 6 {
		t.Fatalf("expected >= 6 cron jobs, got status=%d len=%d", listResp.StatusCode, len(listOut.Items))
	}

	var auditJob, mbJob db.CronJob
	for _, j := range listOut.Items {
		if j.DefaultSchedule == "" || j.TimeoutSec <= 0 || len(j.NextRuns) != 5 {
			t.Fatalf("expected default_schedule, timeout_sec, and 5 next_runs on %s, got %+v", j.JobKey, j)
		}
		if j.JobKey == "cleanup_audit_logs" {
			auditJob = j
		}
		if j.JobKey == "migration_backup" {
			mbJob = j
		}
	}

	// 3. Update timeout_sec and typed config; reject invalid timeout
	badTimeout := env.adminReq(t, http.MethodPut, "/admin/api/v1/cron-jobs/"+auditJob.ID, map[string]any{
		"cron_expr":   "0 2 * * *",
		"enabled":     true,
		"timeout_sec": 0,
	})
	badTimeout.Body.Close()
	if badTimeout.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for timeout_sec=0, got %d", badTimeout.StatusCode)
	}

	updAudit := env.adminReq(t, http.MethodPut, "/admin/api/v1/cron-jobs/"+auditJob.ID, map[string]any{
		"cron_expr":   "0 3 * * *",
		"enabled":     true,
		"timeout_sec": 420,
		"config":      map[string]any{"retention_days": 45},
	})
	var gotAudit db.CronJob
	_ = json.NewDecoder(updAudit.Body).Decode(&gotAudit)
	updAudit.Body.Close()
	if updAudit.StatusCode != http.StatusOK || gotAudit.TimeoutSec != 420 || !strings.Contains(gotAudit.ConfigJSON, `"retention_days":45`) {
		t.Fatalf("unexpected updated audit job: status=%d job=%+v", updAudit.StatusCode, gotAudit)
	}

	// 4. Inline toggle (omitting config) must preserve existing config & secrets for migration_backup
	saveMB := env.adminReq(t, http.MethodPut, "/admin/api/v1/cron-jobs/"+mbJob.ID, map[string]any{
		"cron_expr":   "0 5 * * *",
		"enabled":     true,
		"timeout_sec": 600,
		"config": map[string]any{
			"api_base_url":   "https://backup.example.com",
			"api_key":        "sk_keep_me",
			"path":           "prod",
			"mode":           "sync",
			"passphrase":     "secret-pass-123",
			"include_admins": true,
		},
	})
	saveMB.Body.Close()
	if saveMB.StatusCode != http.StatusOK {
		t.Fatalf("save migration_backup failed: %d", saveMB.StatusCode)
	}

	toggleResp := env.adminReq(t, http.MethodPut, "/admin/api/v1/cron-jobs/"+mbJob.ID, map[string]any{
		"cron_expr":   "0 5 * * *",
		"enabled":     false,
		"timeout_sec": 600,
	})
	var toggledMB db.CronJob
	_ = json.NewDecoder(toggleResp.Body).Decode(&toggledMB)
	toggleResp.Body.Close()
	if toggleResp.StatusCode != http.StatusOK || toggledMB.Enabled || !strings.Contains(toggledMB.ConfigJSON, `"has_api_key":true`) || !strings.Contains(toggledMB.ConfigJSON, `"has_passphrase":true`) {
		t.Fatalf("inline toggle should preserve secrets & config, got status=%d job=%+v", toggleResp.StatusCode, toggledMB)
	}

	secretResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/cron-jobs/"+mbJob.ID+"/secrets", nil)
	var secrets map[string]string
	_ = json.NewDecoder(secretResp.Body).Decode(&secrets)
	secretResp.Body.Close()
	if secrets["api_key"] != "sk_keep_me" || secrets["passphrase"] != "secret-pass-123" {
		t.Fatalf("expected secrets preserved after inline toggle, got %+v", secrets)
	}
}
