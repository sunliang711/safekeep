package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"safekeep/internal/audit"
	"safekeep/internal/db"
	"safekeep/internal/remote"
)

func TestRemoteAdminAPIProtectsPrivateKeys(t *testing.T) {
	env := setupTestEnv(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	audits := audit.NewLogger(env.store)
	service := remote.NewService(context.Background(), env.store, env.cipher, nil, nil, audits, logger)
	defer service.Stop()
	server := New(env.store, env.cipher, nil, nil, nil, nil, nil, audits, logger, nil)
	server.SetRemoteService(service)
	router := server.Router()
	var cookies []*http.Cookie
	request := func(method, path string, payload any) *httptest.ResponseRecorder {
		t.Helper()
		var body bytes.Buffer
		if payload != nil {
			if err := json.NewEncoder(&body).Encode(payload); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, &body)
		req.Header.Set("Content-Type", "application/json")
		for _, cookie := range cookies {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	unauth := request(http.MethodGet, "/admin/api/v1/remote-backups/hosts", nil)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("remote API exposed without auth: %d", unauth.Code)
	}
	setup := request(http.MethodPost, "/admin/api/v1/setup/init", map[string]any{"username": "admin", "password": "secret-test-password"})
	if setup.Code != http.StatusCreated && setup.Code != http.StatusOK {
		t.Fatalf("setup failed: %s", setup.Body.String())
	}
	cookies = setup.Result().Cookies()
	created := request(http.MethodPost, "/admin/api/v1/remote-backups/hosts", map[string]any{"name": "vm001", "address": "127.0.0.1", "port": 1, "username": "backup"})
	if created.Code != http.StatusCreated {
		t.Fatalf("create failed: %s", created.Body.String())
	}
	raw := created.Body.String()
	if strings.Contains(raw, "PRIVATE KEY") || strings.Contains(raw, "private_key_encrypted") {
		t.Fatal("SSH private key leaked to client")
	}
	var host remoteHostDTO
	if err := json.Unmarshal(created.Body.Bytes(), &host); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(host.PublicKey, "ssh-ed25519 ") {
		t.Fatal("public key unavailable")
	}
	before, err := env.store.GetRemoteHost(context.Background(), host.ID)
	if err != nil {
		t.Fatal(err)
	}
	updated := request(http.MethodPut, "/admin/api/v1/remote-backups/hosts/"+host.ID, map[string]any{"name": "renamed", "address": "127.0.0.1", "port": 1, "username": "backup"})
	if updated.Code != http.StatusOK {
		t.Fatal(updated.Body.String())
	}
	after, err := env.store.GetRemoteHost(context.Background(), host.ID)
	if err != nil || before.PrivateKeyEncrypted != after.PrivateKeyEncrypted {
		t.Fatal("editing host regenerated SSH key")
	}
	checked := request(http.MethodPost, "/admin/api/v1/remote-backups/hosts/"+host.ID+"/check", nil)
	if checked.Code != http.StatusOK || !strings.Contains(checked.Body.String(), `"health_status":"failed"`) {
		t.Fatalf("failed connection not reflected in health: %s", checked.Body.String())
	}
	missing := request(http.MethodPut, "/admin/api/v1/remote-backups/hosts/missing", map[string]any{"name": "missing", "address": "127.0.0.1", "port": 22, "username": "backup"})
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing host update returned %d", missing.Code)
	}
	list := request(http.MethodGet, "/admin/api/v1/remote-backups/hosts", nil)
	if list.Code != http.StatusOK || strings.Contains(list.Body.String(), before.PrivateKeyEncrypted) {
		t.Fatal("encrypted key exposed in host list")
	}

	upstream := db.Upstream{ID: "schedule-upstream", Name: "schedule-upstream", Type: "s3", Enabled: true, ConfigEncrypted: "unused"}
	if err := env.store.CreateUpstream(context.Background(), &upstream); err != nil {
		t.Fatal(err)
	}
	var jobID string
	for _, tc := range []struct {
		name     string
		timezone string
		enabled  bool
	}{
		{name: "Singapore", timezone: "Asia/Singapore", enabled: true},
		{name: "UTC", timezone: "UTC", enabled: true},
		{name: "New York", timezone: "America/New_York", enabled: true},
		{name: "Paused", timezone: "Asia/Singapore", enabled: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := remoteJobDTO{ID: jobID, Name: "schedule-test", HostID: host.ID, Directories: []db.RemoteDirectory{{Path: "/var/lib/data"}}, UpstreamIDs: []string{upstream.ID}, ParentPath: "vm001", CronExpr: "0 2 * * *", Timezone: tc.timezone, Enabled: tc.enabled, TimeoutSec: 3600}
			method, path := http.MethodPost, "/admin/api/v1/remote-backups/jobs"
			if jobID != "" {
				method, path = http.MethodPut, path+"/"+jobID
			}
			saved := request(method, path, payload)
			if saved.Code != http.StatusOK && saved.Code != http.StatusCreated {
				t.Fatalf("save job failed: %s", saved.Body.String())
			}
			var job remoteJobResponse
			if err := json.Unmarshal(saved.Body.Bytes(), &job); err != nil {
				t.Fatal(err)
			}
			jobID = job.ID
			listed := request(http.MethodGet, "/admin/api/v1/remote-backups/jobs", nil)
			var out struct {
				Items []remoteJobResponse `json:"items"`
			}
			if err := json.Unmarshal(listed.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if listed.Code != http.StatusOK || len(out.Items) != 1 {
				t.Fatalf("list jobs failed: %s", listed.Body.String())
			}
			for _, result := range []remoteJobResponse{job, out.Items[0]} {
				if !tc.enabled {
					if result.NextRunAt != nil {
						t.Fatal("paused job returned a next execution time")
					}
					continue
				}
				if result.NextRunAt == nil || !result.NextRunAt.After(time.Now()) {
					t.Fatal("enabled job missing a future execution time")
				}
				location, err := time.LoadLocation(tc.timezone)
				if err != nil {
					t.Fatal(err)
				}
				local := result.NextRunAt.In(location)
				if local.Hour() != 2 || local.Minute() != 0 || local.Second() != 0 || time.Until(local) > 26*time.Hour {
					t.Fatalf("next execution does not match daily 02:00 in %s: %s", tc.timezone, local)
				}
			}
			preview := request(http.MethodPost, "/admin/api/v1/cron-jobs/preview", map[string]string{"schedule": "CRON_TZ=" + tc.timezone + " 0 2 * * *"})
			var upcoming struct {
				NextRuns []time.Time `json:"next_runs"`
			}
			if err := json.Unmarshal(preview.Body.Bytes(), &upcoming); err != nil {
				t.Fatal(err)
			}
			if preview.Code != http.StatusOK || len(upcoming.NextRuns) != 5 {
				t.Fatalf("expected five preview times: %s", preview.Body.String())
			}
			location, err := time.LoadLocation(tc.timezone)
			if err != nil {
				t.Fatal(err)
			}
			for _, next := range upcoming.NextRuns {
				if local := next.In(location); local.Hour() != 2 || local.Minute() != 0 {
					t.Fatalf("preview ignored task time zone %s: %s", tc.timezone, local)
				}
			}
		})
	}
}
