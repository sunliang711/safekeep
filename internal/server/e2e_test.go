package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"safekeep/internal/audit"
	"safekeep/internal/backup"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/migration"
	"safekeep/internal/queue"
	"safekeep/internal/scheduler"
	"safekeep/internal/storage"
	"safekeep/internal/storage/dropbox"
	"safekeep/internal/storage/s3"
	"safekeep/web"
)

type testEnv struct {
	srv     *httptest.Server
	store   *db.Store
	cipher  *crypto.Cipher
	pool    *queue.WorkerPool
	sched   *scheduler.Engine
	cancel  context.CancelFunc
	cookies []*http.Cookie
}

func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "test.db"), dir)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	cipher, _ := crypto.NewCipher(bytes.Repeat([]byte{0x77}, 32))
	appCtx, cancel := context.WithCancel(context.Background())

	reg := storage.NewRegistry(store, cipher)
	reg.RegisterBuilder("s3", s3.Builder)
	reg.RegisterBuilder("dropbox", dropbox.Builder)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	auditLog := audit.NewLogger(store)
	backupSvc := backup.NewService(appCtx, store, cipher, reg)
	pool := queue.NewWorkerPool(store, backupSvc, 2, logger)
	backupSvc.SetEnqueuer(pool)
	pool.Start(appCtx)

	sched := scheduler.NewEngine(appCtx, store, reg, auditLog, logger)
	if err := sched.Start(); err != nil {
		t.Fatalf("sched.Start: %v", err)
	}
	migSvc := migration.NewService(store, cipher)
	webFS, err := web.DistFS()
	if err != nil {
		t.Fatalf("web.DistFS: %v", err)
	}

	s := New(store, cipher, reg, backupSvc, pool, sched, migSvc, auditLog, logger, webFS)
	ts := httptest.NewServer(s.Router())

	t.Cleanup(func() {
		ts.Close()
		sched.Stop()
		cancel()
		pool.Wait()
		_ = store.Close()
	})

	return &testEnv{srv: ts, store: store, cipher: cipher, pool: pool, sched: sched, cancel: cancel}
}

func (e *testEnv) adminReq(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, c := range e.cookies {
		req.AddCookie(c)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Cookies()) > 0 {
		e.cookies = resp.Cookies()
	}
	return resp
}

func uploadMultipart(t *testing.T, url, apiKey, filename string, content []byte) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(content)
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, url, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestEndToEndSyncDedupAsyncTimeoutAndMigration(t *testing.T) {
	env := setupTestEnv(t)

	// Mock S3 HTTP Server
	var s3Mu sync.Mutex
	s3Objects := map[string][]byte{}
	var s3UploadCount atomic.Int32
	var s3Delay atomic.Int64
	mockS3 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if d := s3Delay.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		key := strings.TrimPrefix(r.URL.Path, "/test-bucket/")
		switch r.Method {
		case http.MethodHead:
			if r.URL.Path == "/test-bucket" {
				w.WriteHeader(http.StatusOK)
				return
			}
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			s3Mu.Lock()
			s3Objects[key] = b
			s3Mu.Unlock()
			s3UploadCount.Add(1)
			sum := sha256.Sum256(b)
			w.Header().Set("ETag", `"`+hex.EncodeToString(sum[:8])+`"`)
			w.WriteHeader(http.StatusOK)
			return
		case http.MethodGet:
			s3Mu.Lock()
			b, ok := s3Objects[key]
			s3Mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer mockS3.Close()

	// Mock Dropbox HTTP Server (OAuth refresh + upload + download + account)
	var dbxMu sync.Mutex
	dbxFiles := map[string][]byte{}
	var dbxUploadCount atomic.Int32
	mockDropbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth2/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": "mock-dbx-access-token",
				"expires_in":   3600,
			})
		case "/2/users/get_current_account":
			_ = json.NewEncoder(w).Encode(map[string]any{"email": "ops@example.com"})
		case "/2/files/upload":
			var arg struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal([]byte(r.Header.Get("Dropbox-API-Arg")), &arg)
			b, _ := io.ReadAll(r.Body)
			dbxMu.Lock()
			dbxFiles[arg.Path] = b
			dbxMu.Unlock()
			dbxUploadCount.Add(1)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"path_display": arg.Path,
				"content_hash": "dbxhash123",
				"rev":          "rev1",
				"size":         len(b),
			})
		case "/2/files/download":
			var arg struct {
				Path string `json:"path"`
			}
			_ = json.Unmarshal([]byte(r.Header.Get("Dropbox-API-Arg")), &arg)
			dbxMu.Lock()
			b, ok := dbxFiles[arg.Path]
			dbxMu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer mockDropbox.Close()

	// 1. Check embedded SPA serves HTML
	spaResp, err := http.Get(env.srv.URL + "/")
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	spaBody, _ := io.ReadAll(spaResp.Body)
	spaResp.Body.Close()
	if spaResp.StatusCode != 200 || !strings.Contains(string(spaBody), "SafeKeep") {
		t.Fatalf("unexpected SPA response: %d", spaResp.StatusCode)
	}

	// 2. Setup Wizard Init
	resp := env.adminReq(t, http.MethodPost, "/admin/api/v1/setup/init", map[string]string{
		"username": "admin",
		"password": "password123",
	})
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("setup init failed: %d", resp.StatusCode)
	}

	// 3. Create S3 & Dropbox upstreams
	r1 := env.adminReq(t, http.MethodPost, "/admin/api/v1/upstreams", map[string]any{
		"name":    "mock-s3",
		"type":    "s3",
		"enabled": true,
		"config": map[string]any{
			"endpoint":          mockS3.URL,
			"region":            "us-east-1",
			"bucket":            "test-bucket",
			"access_key_id":     "ak",
			"secret_access_key": "sk",
			"force_path_style":  true,
		},
	})
	var upS3 struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r1.Body).Decode(&upS3)
	r1.Body.Close()
	if r1.StatusCode != http.StatusCreated {
		t.Fatalf("create s3 upstream failed: %d", r1.StatusCode)
	}

	r2 := env.adminReq(t, http.MethodPost, "/admin/api/v1/upstreams", map[string]any{
		"name":    "mock-dropbox",
		"type":    "dropbox",
		"enabled": true,
		"config": map[string]any{
			"app_key":          "dbx-key",
			"app_secret":       "dbx-secret",
			"refresh_token":    "dbx-refresh",
			"root_path":        "/safekeep",
			"api_base_url":     mockDropbox.URL,
			"content_base_url": mockDropbox.URL,
		},
	})
	var upDbx struct {
		ID string `json:"id"`
	}
	_ = json.NewDecoder(r2.Body).Decode(&upDbx)
	r2.Body.Close()
	if r2.StatusCode != http.StatusCreated {
		t.Fatalf("create dropbox upstream failed: %d", r2.StatusCode)
	}

	// Test connections
	t1 := env.adminReq(t, http.MethodPost, "/admin/api/v1/upstreams/"+upS3.ID+"/test", nil)
	var tr1 map[string]string
	_ = json.NewDecoder(t1.Body).Decode(&tr1)
	t1.Body.Close()
	if tr1["status"] != "ok" {
		t.Fatalf("s3 test failed: %+v", tr1)
	}

	t2 := env.adminReq(t, http.MethodPost, "/admin/api/v1/upstreams/"+upDbx.ID+"/test", nil)
	var tr2 map[string]string
	_ = json.NewDecoder(t2.Body).Decode(&tr2)
	t2.Body.Close()
	if tr2["status"] != "ok" {
		t.Fatalf("dropbox test failed: %+v", tr2)
	}

	// 4. Create Client bound to both upstreams
	rc := env.adminReq(t, http.MethodPost, "/admin/api/v1/clients", map[string]any{
		"name":         "client-a",
		"enabled":      true,
		"default_mode": "sync",
		"path_prefix":  "tenant-a",
		"upstreams": []map[string]any{
			{"upstream_id": upS3.ID, "is_default": true, "priority": 10},
			{"upstream_id": upDbx.ID, "is_default": true, "priority": 20},
		},
	})
	var clOut struct {
		Client struct {
			ID string `json:"id"`
		} `json:"client"`
		APIKey string `json:"api_key"`
	}
	_ = json.NewDecoder(rc.Body).Decode(&clOut)
	rc.Body.Close()
	if rc.StatusCode != http.StatusCreated || clOut.APIKey == "" {
		t.Fatalf("create client failed: %d", rc.StatusCode)
	}

	// 5. Sync upload
	payload := []byte("hello safekeep multi-cloud backup!")
	upResp := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&path=reports", clOut.APIKey, "report.txt", payload)
	var upRes backup.UploadResponse
	_ = json.NewDecoder(upResp.Body).Decode(&upRes)
	upResp.Body.Close()
	if upResp.StatusCode != http.StatusOK || upRes.Task.Status != "succeeded" {
		t.Fatalf("expected 200 succeeded, got %d %+v", upResp.StatusCode, upRes.Task)
	}
	if s3UploadCount.Load() != 1 || dbxUploadCount.Load() != 1 {
		t.Fatalf("expected 1 upload to each upstream, got s3=%d dbx=%d", s3UploadCount.Load(), dbxUploadCount.Load())
	}

	// 6. Repeat upload of exact same file -> SHA-256 deduplication returns 200 immediately without re-uploading
	dedupResp := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&path=reports", clOut.APIKey, "report-copy.txt", payload)
	var dedupRes backup.UploadResponse
	_ = json.NewDecoder(dedupResp.Body).Decode(&dedupRes)
	dedupResp.Body.Close()
	if dedupResp.StatusCode != http.StatusOK || !dedupRes.Task.Deduplicated || dedupRes.Task.Status != "succeeded" {
		t.Fatalf("expected deduplicated 200 OK, got %d %+v", dedupResp.StatusCode, dedupRes.Task)
	}
	if s3UploadCount.Load() != 1 || dbxUploadCount.Load() != 1 {
		t.Fatalf("expected zero additional upstream uploads on dedup, got s3=%d dbx=%d", s3UploadCount.Load(), dbxUploadCount.Load())
	}

	// 7. Download / Restore file via API
	dlReq, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/api/v1/backups/"+upRes.Task.ID+"/download", nil)
	dlReq.Header.Set("X-API-Key", clOut.APIKey)
	dlResp, err := http.DefaultClient.Do(dlReq)
	if err != nil {
		t.Fatalf("download error: %v", err)
	}
	dlBytes, _ := io.ReadAll(dlResp.Body)
	dlResp.Body.Close()
	if dlResp.StatusCode != http.StatusOK || !bytes.Equal(dlBytes, payload) {
		t.Fatalf("download mismatch: status=%d body=%q", dlResp.StatusCode, dlBytes)
	}

	// 8. Sync timeout fallback to background async
	settings, _ := env.store.GetSystemSettings(context.Background())
	settings.SyncTimeoutSec = 1
	_ = env.store.UpdateSystemSettings(context.Background(), settings, "test")

	s3Delay.Store(1400) // 1.4s > 1s sync timeout
	slowPayload := []byte("slow-sync-file-payload-12345")
	slowResp := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&targets=mock-s3", clOut.APIKey, "slow.bin", slowPayload)
	var slowRes backup.UploadResponse
	_ = json.NewDecoder(slowResp.Body).Decode(&slowRes)
	slowResp.Body.Close()
	if slowResp.StatusCode != http.StatusAccepted || !slowRes.TimedOutToAsync {
		t.Fatalf("expected 202 timed_out_to_async, got status=%d res=%+v", slowResp.StatusCode, slowRes)
	}

	// Wait for background task to complete
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		task, err := env.store.GetBackupTask(context.Background(), slowRes.Task.ID)
		if err == nil && task.Status == "succeeded" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	finalSlow, _ := env.store.GetBackupTask(context.Background(), slowRes.Task.ID)
	if finalSlow.Status != "succeeded" {
		t.Fatalf("expected background-continued task to succeed, got %s", finalSlow.Status)
	}
	s3Delay.Store(0)

	// 9. Run built-in cron job manually & check audit logs
	cronListResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/cron-jobs", nil)
	var cronList struct {
		Items []db.CronJob `json:"items"`
	}
	_ = json.NewDecoder(cronListResp.Body).Decode(&cronList)
	cronListResp.Body.Close()
	if len(cronList.Items) < 5 {
		t.Fatalf("expected 5 built-in cron jobs, got %d", len(cronList.Items))
	}
	runResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/cron-jobs/"+cronList.Items[0].ID+"/run", nil)
	runResp.Body.Close()
	if runResp.StatusCode != http.StatusOK {
		t.Fatalf("manual cron run failed: %d", runResp.StatusCode)
	}

	auditResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/audit-logs", nil)
	var auditList struct {
		Total int64 `json:"total"`
	}
	_ = json.NewDecoder(auditResp.Body).Decode(&auditList)
	auditResp.Body.Close()
	if auditList.Total < 5 {
		t.Fatalf("expected audit logs recorded, got total=%d", auditList.Total)
	}

	// 10. Multipart fields AFTER file (targets=mock-s3, dedup=false) must be honored
	var mpBuf bytes.Buffer
	mw := multipart.NewWriter(&mpBuf)
	fw, _ := mw.CreateFormFile("file", "after.txt")
	_, _ = fw.Write(payload) // same hash as step 5, but dedup=false sent AFTER file!
	_ = mw.WriteField("mode", "sync")
	_ = mw.WriteField("targets", "mock-s3")
	_ = mw.WriteField("dedup", "false")
	_ = mw.Close()

	mpReq, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/api/v1/backups", &mpBuf)
	mpReq.Header.Set("Content-Type", mw.FormDataContentType())
	mpReq.Header.Set("X-API-Key", clOut.APIKey)
	mpResp, err := http.DefaultClient.Do(mpReq)
	if err != nil {
		t.Fatal(err)
	}
	var mpOut backup.UploadResponse
	_ = json.NewDecoder(mpResp.Body).Decode(&mpOut)
	mpResp.Body.Close()
	if mpResp.StatusCode != http.StatusOK || mpOut.Task.Deduplicated || len(mpOut.Task.Items) != 1 || mpOut.Task.Items[0].UpstreamName != "mock-s3" {
		t.Fatalf("expected trailing multipart fields honored (1 item mock-s3, dedup=false), got status=%d task=%+v", mpResp.StatusCode, mpOut.Task)
	}

	// 11. Untrusted CORS origin must not be reflected
	corsReq, _ := http.NewRequest(http.MethodOptions, env.srv.URL+"/admin/api/v1/auth/me", nil)
	corsReq.Header.Set("Origin", "https://evil.example.com")
	corsResp, err := http.DefaultClient.Do(corsReq)
	if err != nil {
		t.Fatal(err)
	}
	corsResp.Body.Close()
	if gotOrigin := corsResp.Header.Get("Access-Control-Allow-Origin"); gotOrigin != "" {
		t.Fatalf("expected untrusted CORS origin to be rejected, got %q", gotOrigin)
	}

	// 12. Changing admin password must revoke old JWT session
	oldCookies := append([]*http.Cookie(nil), env.cookies...)
	chgResp := env.adminReq(t, http.MethodPut, "/admin/api/v1/auth/password", map[string]string{
		"old_password": "password123",
		"new_password": "newpassword456",
	})
	chgResp.Body.Close()
	if chgResp.StatusCode != http.StatusOK {
		t.Fatalf("change password failed: %d", chgResp.StatusCode)
	}
	staleReq, _ := http.NewRequest(http.MethodGet, env.srv.URL+"/admin/api/v1/auth/me", nil)
	for _, c := range oldCookies {
		staleReq.AddCookie(c)
	}
	staleResp, err := http.DefaultClient.Do(staleReq)
	if err != nil {
		t.Fatal(err)
	}
	staleResp.Body.Close()
	if staleResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old session token revoked after password change, got %d", staleResp.StatusCode)
	}

	// 13. Dropbox OAuth start must derive redirect_uri from request host when public_base_url is default localhost
	oauthStartResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/upstreams/"+upDbx.ID+"/oauth/dropbox/start", nil)
	var oauthOut struct {
		RedirectURI string `json:"redirect_uri"`
	}
	_ = json.NewDecoder(oauthStartResp.Body).Decode(&oauthOut)
	oauthStartResp.Body.Close()
	expectedRedirect := env.srv.URL + "/admin/api/v1/oauth/dropbox/callback"
	if oauthStartResp.StatusCode != http.StatusOK || oauthOut.RedirectURI != expectedRedirect {
		t.Fatalf("expected redirect_uri %q, got status=%d redirect_uri=%q", expectedRedirect, oauthStartResp.StatusCode, oauthOut.RedirectURI)
	}

	// 13b. Upstream proxy connectivity test endpoint (/admin/api/v1/upstreams/test-proxy)
	proxyTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyTarget.Close()
	mockProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") == "" {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		fwdReq, _ := http.NewRequestWithContext(r.Context(), r.Method, r.RequestURI, nil)
		resp, err := http.DefaultClient.Do(fwdReq)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
	}))
	defer mockProxy.Close()
	mpURL, _ := url.Parse(mockProxy.URL)
	mpHost, mpPortStr, _ := net.SplitHostPort(mpURL.Host)
	mpPort, _ := strconv.Atoi(mpPortStr)

	proxyTestResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/upstreams/test-proxy", map[string]any{
		"proxy_type":     "http",
		"proxy_host":     mpHost,
		"proxy_port":     mpPort,
		"proxy_username": "u",
		"proxy_password": "p",
		"target_url":     proxyTarget.URL,
	})
	var proxyTestOut struct {
		Status     string `json:"status"`
		HTTPStatus int    `json:"http_status"`
	}
	if err := json.NewDecoder(proxyTestResp.Body).Decode(&proxyTestOut); err != nil {
		t.Fatal(err)
	}
	proxyTestResp.Body.Close()
	if proxyTestResp.StatusCode != http.StatusOK || proxyTestOut.Status != "ok" || proxyTestOut.HTTPStatus != http.StatusNoContent {
		t.Fatalf("expected proxy test ok with 204, got code=%d out=%+v", proxyTestResp.StatusCode, proxyTestOut)
	}

	proxyFailResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/upstreams/test-proxy", map[string]any{
		"proxy_type": "http",
		"proxy_host": mpHost,
		"proxy_port": mpPort,
		"target_url": proxyTarget.URL,
	})
	var proxyFailOut struct {
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	_ = json.NewDecoder(proxyFailResp.Body).Decode(&proxyFailOut)
	proxyFailResp.Body.Close()
	if proxyFailResp.StatusCode != http.StatusOK || proxyFailOut.Status != "failed" || proxyFailOut.Error == "" {
		t.Fatalf("expected proxy test auth failure, got code=%d out=%+v", proxyFailResp.StatusCode, proxyFailOut)
	}

	// 14. SMTP alerts, oversize file rejection (>max_file_size_bytes), and daily circuit breaker (>daily_traffic_limit_bytes)
	smtpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer smtpLn.Close()

	var smtpMu sync.Mutex
	var receivedEmails []string
	go func() {
		for {
			conn, err := smtpLn.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				_, _ = c.Write([]byte("220 mock-smtp\r\n"))
				inData := false
				var dataBuf strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if inData {
						if line == ".\r\n" {
							inData = false
							parts := strings.SplitN(dataBuf.String(), "\r\n\r\n", 2)
							bodyText := dataBuf.String()
							if len(parts) == 2 {
								if dec, dErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", "")); dErr == nil {
									bodyText = string(dec)
								}
							}
							smtpMu.Lock()
							receivedEmails = append(receivedEmails, bodyText)
							smtpMu.Unlock()
							dataBuf.Reset()
							_, _ = c.Write([]byte("250 OK\r\n"))
							continue
						}
						dataBuf.WriteString(line)
						continue
					}
					upper := strings.ToUpper(strings.TrimSpace(line))
					switch {
					case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
						_, _ = c.Write([]byte("250-mock-smtp\r\n250 OK\r\n"))
					case upper == "DATA":
						inData = true
						_, _ = c.Write([]byte("354 End with <CRLF>.<CRLF>\r\n"))
					case upper == "QUIT":
						_, _ = c.Write([]byte("221 Bye\r\n"))
						return
					default:
						_, _ = c.Write([]byte("250 OK\r\n"))
					}
				}
			}(conn)
		}
	}()

	smtpHost, smtpPortStr, _ := net.SplitHostPort(smtpLn.Addr().String())
	smtpPort, _ := strconv.Atoi(smtpPortStr)

	const smtpAuthSecret = "smtp-auth-secret-code"
	smtpSaveResp := env.adminReq(t, http.MethodPut, "/admin/api/v1/settings/smtp", map[string]any{
		"enabled":    true,
		"host":       smtpHost,
		"port":       smtpPort,
		"tls_mode":   "none",
		"password":   smtpAuthSecret,
		"from_email": "safekeep@example.com",
		"from_name":  "SafeKeep",
		"to_emails":  []string{"ops@example.com"},
	})
	smtpSaveResp.Body.Close()
	if smtpSaveResp.StatusCode != http.StatusOK {
		t.Fatalf("save smtp failed: %d", smtpSaveResp.StatusCode)
	}

	smtpGetResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/settings/smtp", nil)
	var smtpGetOut struct {
		HasPassword bool   `json:"has_password"`
		Password    string `json:"password"`
	}
	if err := json.NewDecoder(smtpGetResp.Body).Decode(&smtpGetOut); err != nil {
		t.Fatal(err)
	}
	smtpGetResp.Body.Close()
	if !smtpGetOut.HasPassword || smtpGetOut.Password != smtpAuthSecret {
		t.Fatalf("expected admin smtp GET to return password for copy, got has=%v pwd=%q", smtpGetOut.HasPassword, smtpGetOut.Password)
	}
	auditItems, _, err := env.store.ListAuditLogs(context.Background(), db.AuditFilter{Action: "settings.smtp_update", Limit: 5})
	if err != nil || len(auditItems) == 0 {
		t.Fatalf("expected settings.smtp_update audit log: %v", err)
	}
	if strings.Contains(auditItems[0].DetailJSON, smtpAuthSecret) {
		t.Fatalf("audit log must not store plaintext smtp password: %s", auditItems[0].DetailJSON)
	}

	smtpTestResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/settings/smtp/test", nil)
	var smtpTestOut struct {
		Status string   `json:"status"`
		To     []string `json:"to"`
	}
	if err := json.NewDecoder(smtpTestResp.Body).Decode(&smtpTestOut); err != nil {
		t.Fatal(err)
	}
	smtpTestResp.Body.Close()
	if smtpTestResp.StatusCode != http.StatusOK || smtpTestOut.Status != "ok" || len(smtpTestOut.To) != 1 {
		t.Fatalf("test smtp failed: status=%d out=%+v", smtpTestResp.StatusCode, smtpTestOut)
	}

	smtpFailResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/settings/smtp/test", map[string]any{
		"host":       "127.0.0.1",
		"port":       1,
		"tls_mode":   "none",
		"from_email": "safekeep@example.com",
		"to_emails":  []string{"ops@example.com"},
	})
	smtpFailResp.Body.Close()
	if smtpFailResp.StatusCode == http.StatusOK {
		t.Fatalf("expected unreachable smtp test to fail")
	}

	// Reset today's counter from earlier test steps and set max_file_size_bytes=50, daily_traffic_limit_bytes=120
	initReset := env.adminReq(t, http.MethodPost, "/admin/api/v1/circuit-breaker/reset", nil)
	initReset.Body.Close()
	curSet, _ := env.store.GetSystemSettings(context.Background())
	curSet.MaxFileSizeBytes = 50
	curSet.DailyTrafficLimitBytes = 120
	if err := env.store.UpdateSystemSettings(context.Background(), curSet, "test"); err != nil {
		t.Fatal(err)
	}

	// Oversize upload (80 > 50) -> 413 + alert email
	overResp := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&targets=mock-s3&dedup=false", clOut.APIKey, "huge.bin", bytes.Repeat([]byte("A"), 80))
	overResp.Body.Close()
	if overResp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 for oversize upload, got %d", overResp.StatusCode)
	}

	// Allow up to 80 bytes per file, daily limit stays 120
	curSet.MaxFileSizeBytes = 80
	_ = env.store.UpdateSystemSettings(context.Background(), curSet, "test")

	// First upload 70 bytes -> succeeds (used=70 <= 120)
	ok1 := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&targets=mock-s3&dedup=false", clOut.APIKey, "part1.bin", bytes.Repeat([]byte("B"), 70))
	ok1.Body.Close()
	if ok1.StatusCode != http.StatusOK {
		t.Fatalf("expected first 70B upload 200, got %d", ok1.StatusCode)
	}

	// Second upload 70 bytes -> 70+70=140 > 120 -> 503 circuit breaker tripped + alert email
	cbResp := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&targets=mock-s3&dedup=false", clOut.APIKey, "part2.bin", bytes.Repeat([]byte("C"), 70))
	cbResp.Body.Close()
	if cbResp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 circuit_breaker_open, got %d", cbResp.StatusCode)
	}

	// Third upload 10 bytes -> still 503 while tripped (should NOT send duplicate circuit-breaker email)
	cbResp2 := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&targets=mock-s3&dedup=false", clOut.APIKey, "part3.bin", bytes.Repeat([]byte("D"), 10))
	cbResp2.Body.Close()
	if cbResp2.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 while tripped, got %d", cbResp2.StatusCode)
	}

	// Wait briefly for async alert emails
	deadlineEmails := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadlineEmails) {
		smtpMu.Lock()
		n := len(receivedEmails)
		smtpMu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	smtpMu.Lock()
	emailsCopy := append([]string(nil), receivedEmails...)
	smtpMu.Unlock()
	if len(emailsCopy) != 3 {
		t.Fatalf("expected exactly 3 emails (test, oversize, single circuit-breaker), got %d: %+v", len(emailsCopy), emailsCopy)
	}

	// Reset circuit breaker via Admin API -> uploads work again
	rstResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/circuit-breaker/reset", nil)
	rstResp.Body.Close()
	if rstResp.StatusCode != http.StatusOK {
		t.Fatalf("reset circuit breaker failed: %d", rstResp.StatusCode)
	}
	okAfterReset := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&targets=mock-s3&dedup=false", clOut.APIKey, "part4.bin", bytes.Repeat([]byte("E"), 10))
	okAfterReset.Body.Close()
	if okAfterReset.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 after circuit breaker reset, got %d", okAfterReset.StatusCode)
	}

	// 15. Admin Web upload endpoint (/admin/api/v1/backups/upload) mirrors client API behavior
	var webBuf bytes.Buffer
	wmw := multipart.NewWriter(&webBuf)
	wfw, _ := wmw.CreateFormFile("file", "web-upload.txt")
	_, _ = wfw.Write([]byte("hello from web ui"))
	_ = wmw.Close()
	webReq, _ := http.NewRequest(http.MethodPost, env.srv.URL+"/admin/api/v1/backups/upload?client_id="+clOut.Client.ID+"&mode=sync&path=web", &webBuf)
	webReq.Header.Set("Content-Type", wmw.FormDataContentType())
	for _, c := range env.cookies {
		webReq.AddCookie(c)
	}
	webResp, err := http.DefaultClient.Do(webReq)
	if err != nil {
		t.Fatal(err)
	}
	var webOut backup.UploadResponse
	_ = json.NewDecoder(webResp.Body).Decode(&webOut)
	webResp.Body.Close()
	if webResp.StatusCode != http.StatusOK || webOut.Task.Status != "succeeded" || len(webOut.Task.Items) != 2 {
		t.Fatalf("expected web upload 200 succeeded across 2 upstreams, got %d %+v", webResp.StatusCode, webOut.Task)
	}
}
