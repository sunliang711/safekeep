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

	"safekeep/internal/audit"
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
}
