package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
}
