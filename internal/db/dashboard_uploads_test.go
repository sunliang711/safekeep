package db

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func openUploadStatsStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	store, err := Open(filepath.Join(dir, "test.db"), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	return store
}

func insertUploadAudit(t *testing.T, store *Store, log AuditLog) {
	t.Helper()
	if err := store.InsertAuditLog(context.Background(), &log); err != nil {
		t.Fatal(err)
	}
}

func TestGetAPIUploadStatsPeriods(t *testing.T) {
	singapore := time.FixedZone("SGT", 8*60*60)
	newYork, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name                string
		now                 time.Time
		today, week, month  string
		wantToday, wantWeek int64
		wantMonth           int64
	}{
		{"normal", time.Date(2026, 11, 21, 12, 0, 0, 0, singapore), "2026-11-21", "2026-11-16", "2026-11-01", 2, 4, 5},
		{"week crosses year", time.Date(2026, 1, 1, 12, 0, 0, 0, singapore), "2026-01-01", "2025-12-29", "2026-01-01", 3, 5, 3},
		{"sunday starts month", time.Date(2026, 11, 1, 12, 0, 0, 0, singapore), "2026-11-01", "2026-10-26", "2026-11-01", 3, 5, 3},
		{"monday", time.Date(2026, 11, 2, 12, 0, 0, 0, singapore), "2026-11-02", "2026-11-02", "2026-11-01", 3, 3, 5},
		{"DST transition", time.Date(2026, 3, 8, 12, 0, 0, 0, newYork), "2026-03-08", "2026-03-02", "2026-03-01", 2, 4, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := openUploadStatsStore(t)
			parse := func(value string) time.Time {
				date, err := time.ParseInLocation("2006-01-02", value, tt.now.Location())
				if err != nil {
					t.Fatal(err)
				}
				return date
			}
			today, week, month := parse(tt.today), parse(tt.week), parse(tt.month)
			start := month
			if week.Before(start) {
				start = week
			}
			for i, at := range []time.Time{tt.now, today, today.Add(-time.Nanosecond), week, month, start.Add(-time.Nanosecond), tt.now.Add(time.Nanosecond)} {
				insertUploadAudit(t, store, AuditLog{
					ActorType: "client", ActorID: "client-a", ActorName: "Client A", Action: "backup.upload",
					ResourceType: "backup_task", ResourceID: fmt.Sprintf("task-%d", i), Status: "success", CreatedAt: at.UTC(),
				})
			}
			stats, err := store.GetAPIUploadStats(context.Background(), tt.now)
			if err != nil {
				t.Fatal(err)
			}
			for _, period := range []struct {
				name string
				got  []UploadCount
				want int64
			}{{"today", stats.Today, tt.wantToday}, {"week", stats.Week, tt.wantWeek}, {"month", stats.Month, tt.wantMonth}} {
				if len(period.got) != 1 || period.got[0].UploadCount != period.want {
					t.Errorf("%s: got %+v, want %d", period.name, period.got, period.want)
				}
			}
		})
	}
}

func TestGetAPIUploadStatsFiltersAndClients(t *testing.T) {
	store := openUploadStatsStore(t)
	ctx := context.Background()
	now := time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC)
	base := AuditLog{
		ActorType: "client", ActorID: "client-a", ActorName: "Original Name", Action: "backup.upload",
		ResourceType: "backup_task", ResourceID: "task-a", Status: "success", CreatedAt: now,
	}
	// 客户改名后按当前名称显示；已删除客户仍保留审计中的名称。
	if _, err := store.db.ExecContext(ctx, `INSERT INTO clients(id, name, key_prefix, key_hash, created_at, updated_at) VALUES (?, ?, '', ?, ?, ?)`, "client-a", "Renamed Client", "hash-a", now, now); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*AuditLog){
		func(l *AuditLog) {},
		func(l *AuditLog) { l.ActorName = "Previous Name" },
		func(l *AuditLog) { l.ResourceID = "task-dedup"; l.DetailJSON = `{"deduplicated":true}` },
		func(l *AuditLog) { l.ResourceID = "task-async"; l.DetailJSON = `{"task_status":"pending"}` },
		func(l *AuditLog) { l.ResourceID = "task-replay"; l.DetailJSON = `{"idempotent_replay":true}` },
		func(l *AuditLog) { l.ResourceID = "task-failed"; l.Status = "failure" },
		func(l *AuditLog) { l.ResourceID = "task-sync-failed"; l.DetailJSON = `{"task_status":"failed"}` },
		func(l *AuditLog) { l.ResourceID = "task-web"; l.Action = "backup.web_upload"; l.ActorType = "admin" },
		func(l *AuditLog) { l.ResourceID = "task-download"; l.Action = "backup.download" },
		func(l *AuditLog) { l.ResourceID = "task-system"; l.ActorType = "system" },
		func(l *AuditLog) { l.ResourceID = "task-wrong-resource"; l.ResourceType = "client" },
		func(l *AuditLog) { l.ResourceID = "" },
		func(l *AuditLog) { l.ActorID = "client-b"; l.ActorName = "Deleted Client"; l.ResourceID = "task-b" },
		func(l *AuditLog) {
			l.ActorID = "client-c"
			l.ActorName = ""
			l.ResourceID = "task-c"
			l.DetailJSON = "invalid JSON"
		},
		func(l *AuditLog) { l.ActorID = "client-d"; l.ActorName = "Renamed Client"; l.ResourceID = "task-d" },
		func(l *AuditLog) {
			l.ResourceID = "old-task-replay"
			l.DetailJSON = `{"idempotent_replay":true}`
			l.CreatedAt = now.Add(-time.Hour)
		},
	} {
		log := base
		change(&log)
		insertUploadAudit(t, store, log)
	}
	stats, err := store.GetAPIUploadStats(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	want := []UploadCount{{"client-a", "Renamed Client", 3}, {"client-b", "Deleted Client", 1}, {"client-c", "client-c", 1}, {"client-d", "Renamed Client", 1}}
	for _, got := range [][]UploadCount{stats.Today, stats.Week, stats.Month} {
		if !reflect.DeepEqual(got, want) {
			t.Errorf("got %+v, want %+v", got, want)
		}
	}
	if stats.Timezone != "UTC (UTC+00:00)" {
		t.Errorf("unexpected timezone: %s", stats.Timezone)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAPIUploadStats(ctx, now); err == nil {
		t.Fatal("expected closed database error")
	}
}

func TestGetDashboardSummaryEmptyUploadStats(t *testing.T) {
	store := openUploadStatsStore(t)
	summary, err := store.GetDashboardSummary(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	stats := summary.APIUploads
	if stats == nil || stats.Today == nil || stats.Week == nil || stats.Month == nil || len(stats.Today)+len(stats.Week)+len(stats.Month) != 0 {
		t.Fatalf("expected empty arrays for all periods, got %+v", stats)
	}
}
