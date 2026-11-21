package server

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

func proxyAPIRequest(t *testing.T, env *testEnv, method, path string, body any, status int, out any) string {
	t.Helper()
	resp := env.adminReq(t, method, path, body)
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != status {
		t.Fatalf("%s %s: expected %d, got %d: %s", method, path, status, resp.StatusCode, raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatal(err)
		}
	}
	return string(raw)
}

func TestSharedProxyManagementAndRouting(t *testing.T) {
	env := setupTestEnv(t)
	proxyAPIRequest(t, env, http.MethodGet, "/admin/api/v1/proxies", nil, http.StatusUnauthorized, nil)
	proxyAPIRequest(t, env, http.MethodPost, "/admin/api/v1/setup/init", map[string]string{
		"username": "admin", "password": "password123",
	}, http.StatusCreated, nil)

	var aHits, bHits, directHits atomic.Int32
	serve := func(hits *atomic.Int32, authenticated bool) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if authenticated && r.Header.Get("Proxy-Authorization") == "" {
				w.WriteHeader(http.StatusProxyAuthRequired)
				return
			}
			hits.Add(1)
			if r.Method == http.MethodGet {
				_, _ = io.WriteString(w, "backup contents")
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
	}
	proxyA, proxyB, origin := serve(&aHits, true), serve(&bHits, true), serve(&directHits, false)
	defer proxyA.Close()
	defer proxyB.Close()
	defer origin.Close()
	proxyPayload := func(name, address string) map[string]any {
		u, err := url.Parse(address)
		if err != nil {
			t.Fatal(err)
		}
		host, port, err := net.SplitHostPort(u.Host)
		if err != nil {
			t.Fatal(err)
		}
		p, err := strconv.Atoi(port)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{"name": name, "type": "http", "host": host, "port": p, "username": "proxy-user", "password": "proxy-test-secret"}
	}
	payload := proxyPayload("shared", proxyA.URL)
	var p proxyDTO
	raw := proxyAPIRequest(t, env, http.MethodPost, "/admin/api/v1/proxies", payload, http.StatusCreated, &p)
	if !p.HasPassword || strings.Contains(raw, "proxy-test-secret") {
		t.Fatal("proxy response must mask saved password")
	}
	proxyAPIRequest(t, env, http.MethodPost, "/admin/api/v1/proxies", payload, http.StatusConflict, nil)
	invalid := proxyPayload("invalid", proxyA.URL)
	invalid["port"] = 65536
	proxyAPIRequest(t, env, http.MethodPost, "/admin/api/v1/proxies", invalid, http.StatusBadRequest, nil)
	upPayload := func(name, proxyID string) map[string]any {
		return map[string]any{"name": name, "type": "s3", "enabled": true, "proxy_id": proxyID,
			"config": map[string]any{"endpoint": origin.URL, "bucket": "bucket", "region": "us-east-1",
				"access_key_id": "test-key", "secret_access_key": "test-secret", "force_path_style": true}}
	}
	var ups [2]upstreamDTO
	for i, name := range []string{"first", "second"} {
		proxyAPIRequest(t, env, http.MethodPost, "/admin/api/v1/upstreams", upPayload(name, p.ID), http.StatusCreated, &ups[i])
		if ups[i].ProxyID != p.ID || ups[i].ProxyName != "shared" || !ups[i].ProxyEnabled {
			t.Fatalf("upstream did not bind proxy: %+v", ups[i])
		}
	}
	proxyAPIRequest(t, env, http.MethodPost, "/admin/api/v1/upstreams", upPayload("missing", "missing-proxy"), http.StatusBadRequest, nil)
	proxyAPIRequest(t, env, http.MethodDelete, "/admin/api/v1/proxies/"+p.ID, nil, http.StatusConflict, nil)
	var listed struct {
		Items []proxyDTO `json:"items"`
	}
	raw = proxyAPIRequest(t, env, http.MethodGet, "/admin/api/v1/proxies", nil, http.StatusOK, &listed)
	if len(listed.Items) != 1 || listed.Items[0].UpstreamCount != 2 || strings.Contains(raw, "proxy-test-secret") {
		t.Fatalf("unexpected proxy list: %s", raw)
	}
	var client struct {
		APIKey string `json:"api_key"`
	}
	proxyAPIRequest(t, env, http.MethodPost, "/admin/api/v1/clients", map[string]any{
		"name": "proxy-client", "enabled": true, "default_mode": "sync",
		"upstreams": []map[string]any{{"upstream_id": ups[0].ID, "is_default": true}, {"upstream_id": ups[1].ID, "is_default": true}},
	}, http.StatusCreated, &client)
	upload := func(filename string) string {
		resp := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&dedup=false", client.APIKey, filename, []byte(filename))
		defer resp.Body.Close()
		var result struct {
			Task struct {
				ID string `json:"id"`
			} `json:"task"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("proxy upload failed: %d", resp.StatusCode)
		}
		return result.Task.ID
	}
	upload("before-change.txt")
	if aHits.Load() != 2 || bHits.Load() != 0 || directHits.Load() != 0 {
		t.Fatalf("initial routing: A=%d B=%d direct=%d", aHits.Load(), bHits.Load(), directHits.Load())
	}
	updated := proxyPayload("shared-updated", proxyB.URL)
	delete(updated, "password")
	proxyAPIRequest(t, env, http.MethodPut, "/admin/api/v1/proxies/"+p.ID, updated, http.StatusOK, &p)
	if !p.HasPassword {
		t.Fatal("omitted password must preserve saved credentials")
	}
	taskID := upload("after-change.txt")
	if aHits.Load() != 2 || bHits.Load() != 2 || directHits.Load() != 0 {
		t.Fatalf("cached clients did not rebuild: A=%d B=%d direct=%d", aHits.Load(), bHits.Load(), directHits.Load())
	}
	resp := env.adminReq(t, http.MethodGet, "/admin/api/v1/tasks/"+taskID+"/download", nil)
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK || string(body) != "backup contents" || bHits.Load() != 3 {
		t.Fatalf("download did not use selected proxy: status=%d body=%s error=%v", resp.StatusCode, body, err)
	}
	var testResult struct {
		Status string `json:"status"`
	}
	proxyAPIRequest(t, env, http.MethodPost, "/admin/api/v1/upstreams/test-proxy", map[string]any{
		"proxy_id": p.ID, "target_url": origin.URL,
	}, http.StatusOK, &testResult)
	if testResult.Status != "ok" {
		t.Fatal("saved proxy connectivity test failed")
	}
	var secret struct {
		Password string `json:"password"`
	}
	proxyAPIRequest(t, env, http.MethodGet, "/admin/api/v1/proxies/"+p.ID+"/secrets", nil, http.StatusOK, &secret)
	if secret.Password != "proxy-test-secret" {
		t.Fatal("saved password changed unexpectedly")
	}
	updated["password"] = ""
	proxyAPIRequest(t, env, http.MethodPut, "/admin/api/v1/proxies/"+p.ID, updated, http.StatusOK, &p)
	if p.HasPassword {
		t.Fatal("explicit empty password must clear saved credentials")
	}
	for _, u := range ups {
		proxyAPIRequest(t, env, http.MethodPut, "/admin/api/v1/upstreams/"+u.ID, upPayload(u.Name, ""), http.StatusOK, nil)
	}
	upload("direct.txt")
	if directHits.Load() != 2 {
		t.Fatalf("expected direct uploads after unbinding, got %d", directHits.Load())
	}
	proxyAPIRequest(t, env, http.MethodDelete, "/admin/api/v1/proxies/"+p.ID, nil, http.StatusOK, nil)
	proxyAPIRequest(t, env, http.MethodDelete, "/admin/api/v1/proxies/"+p.ID, nil, http.StatusNotFound, nil)
}
