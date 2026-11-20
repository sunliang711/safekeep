package scheduler

import (
	"encoding/json"
	"testing"
)

func TestPreviewCron(t *testing.T) {
	times, err := PreviewCron("0 4 * * *", 5)
	if err != nil {
		t.Fatalf("PreviewCron valid failed: %v", err)
	}
	if len(times) != 5 {
		t.Fatalf("expected 5 upcoming times, got %d", len(times))
	}
	for i := 1; i < len(times); i++ {
		if !times[i].After(times[i-1]) {
			t.Fatalf("expected strictly increasing times, got %v", times)
		}
	}

	if _, err := PreviewCron("not-a-cron", 5); err == nil {
		t.Fatal("expected error for invalid cron expression")
	}
	if _, err := PreviewCron("   ", 5); err == nil {
		t.Fatal("expected error for empty cron expression")
	}
}

func TestNormalizeJobConfig(t *testing.T) {
	got, err := NormalizeJobConfig("cleanup_audit_logs", json.RawMessage(`{"retention_days":30}`))
	if err != nil || got != `{"retention_days":30}` {
		t.Fatalf("unexpected cleanup_audit_logs normalize: got=%q err=%v", got, err)
	}
	if _, err := NormalizeJobConfig("cleanup_audit_logs", json.RawMessage(`{"retention_days":0}`)); err == nil {
		t.Fatal("expected error for retention_days=0")
	}

	got, err = NormalizeJobConfig("cleanup_orphan_spool", json.RawMessage(`{"max_age_hours":12,"failed_spool_retention_hours":48}`))
	if err != nil {
		t.Fatalf("cleanup_orphan_spool normalize failed: %v", err)
	}
	var spool map[string]int
	if err := json.Unmarshal([]byte(got), &spool); err != nil || spool["max_age_hours"] != 12 || spool["failed_spool_retention_hours"] != 48 {
		t.Fatalf("unexpected spool config: %s", got)
	}

	got, err = NormalizeJobConfig("upstream_health_check", json.RawMessage(`{"probe_timeout_sec":15}`))
	if err != nil || got != `{"probe_timeout_sec":15}` {
		t.Fatalf("unexpected upstream_health_check normalize: got=%q err=%v", got, err)
	}
	if _, err := NormalizeJobConfig("upstream_health_check", json.RawMessage(`{"probe_timeout_sec":999}`)); err == nil {
		t.Fatal("expected error for probe_timeout_sec > 120")
	}
}
