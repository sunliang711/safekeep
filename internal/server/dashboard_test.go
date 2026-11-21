package server

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"testing"
	"time"

	"safekeep/internal/db"
)

func TestDashboardAPIUploadStats(t *testing.T) {
	env := setupTestEnv(t)
	unauthorized := env.adminReq(t, http.MethodGet, "/admin/api/v1/dashboard/summary", nil)
	unauthorized.Body.Close()
	if unauthorized.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", unauthorized.StatusCode)
	}
	setup := env.adminReq(t, http.MethodPost, "/admin/api/v1/setup/init", map[string]string{
		"username": "admin",
		"password": "password123",
	})
	setup.Body.Close()
	if setup.StatusCode != http.StatusCreated {
		t.Fatalf("setup failed: %d", setup.StatusCode)
	}

	for _, count := range []int{0, 1} {
		if count > 0 {
			if err := env.store.InsertAuditLog(context.Background(), &db.AuditLog{
				ActorType: "client", ActorID: "api-client", ActorName: "API Client", Action: "backup.upload",
				ResourceType: "backup_task", ResourceID: "accepted-file", Status: "success", CreatedAt: time.Now().UTC(),
			}); err != nil {
				t.Fatal(err)
			}
		}
		resp := env.adminReq(t, http.MethodGet, "/admin/api/v1/dashboard/summary", nil)
		var summary db.DashboardSummary
		err := json.NewDecoder(resp.Body).Decode(&summary)
		resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK || summary.APIUploads == nil {
			t.Fatalf("unexpected summary: status=%d uploads=%+v", resp.StatusCode, summary.APIUploads)
		}
		want := []db.UploadCount{}
		if count > 0 {
			want = append(want, db.UploadCount{ClientID: "api-client", ClientName: "API Client", UploadCount: 1})
		}
		for _, got := range [][]db.UploadCount{summary.APIUploads.Today, summary.APIUploads.Week, summary.APIUploads.Month} {
			if !reflect.DeepEqual(got, want) {
				t.Errorf("got %+v, want %+v", got, want)
			}
		}
		if summary.APIUploads.Timezone == "" {
			t.Error("expected server timezone")
		}
	}
}
