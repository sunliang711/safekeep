package gcs

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"safekeep/internal/proxy"
	"safekeep/internal/storage"
)

func generateTestServiceAccountJSON(t *testing.T, tokenURI string) (string, *rsa.PublicKey) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate rsa key: %v", err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal pkcs8: %v", err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	sa := map[string]string{
		"type":           "service_account",
		"project_id":     "proj-safekeep",
		"private_key_id": "kid-123",
		"private_key":    string(pemBytes),
		"client_email":   "safekeep@proj-safekeep.iam.gserviceaccount.com",
		"token_uri":      tokenURI,
	}
	b, _ := json.Marshal(sa)
	return string(b), &priv.PublicKey
}

func TestValidateServiceAccountJSON(t *testing.T) {
	saJSON, _ := generateTestServiceAccountJSON(t, "https://oauth2.googleapis.com/token")
	if err := ValidateServiceAccountJSON(saJSON); err != nil {
		t.Fatalf("expected valid service account JSON, got: %v", err)
	}

	if err := ValidateServiceAccountJSON(`{}`); err == nil {
		t.Fatalf("expected error for empty service account JSON")
	}
	if err := ValidateServiceAccountJSON(`{"type":"authorized_user","client_email":"a@b","private_key":"x"}`); err == nil {
		t.Fatalf("expected error for non-service_account type")
	}
	if err := ValidateServiceAccountJSON(`{"type":"service_account","client_email":"a@b","private_key":"not-a-pem"}`); err == nil {
		t.Fatalf("expected error for invalid private key PEM")
	}
}

func TestGCSProviderLifecycleAndResumable(t *testing.T) {
	var tokenCalls atomic.Int32
	var forceNext401 atomic.Bool
	var mu sync.Mutex
	objects := map[string]struct {
		data         []byte
		contentType  string
		storageClass string
	}{}
	sessions := map[string]struct {
		name         string
		contentType  string
		storageClass string
		buf          bytes.Buffer
	}{}

	var pubKey *rsa.PublicKey
	var srvURL string

	mockGCS := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			n := tokenCalls.Add(1)
			_ = r.ParseForm()
			assertion := r.Form.Get("assertion")
			parts := strings.Split(assertion, ".")
			if len(parts) != 3 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			sig, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
			if err := rsa.VerifyPKCS1v15(pubKey, crypto.SHA256, digest[:], sig); err != nil {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte("invalid jwt signature"))
				return
			}
			tok := "tok-1"
			if n > 1 {
				tok = "tok-2"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token": tok,
				"expires_in":   3600,
			})
			return
		}

		if forceNext401.Swap(false) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte("expired"))
			return
		}

		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer tok-") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}

		// TestConnection: GET /storage/v1/b/{bucket}
		if r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/test-gcs-bucket" {
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "test-gcs-bucket"})
			return
		}

		// Upload: POST /upload/storage/v1/b/{bucket}/o
		if r.Method == http.MethodPost && r.URL.Path == "/upload/storage/v1/b/test-gcs-bucket/o" {
			uploadType := r.URL.Query().Get("uploadType")
			switch uploadType {
			case "multipart":
				_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				mr := multipart.NewReader(r.Body, params["boundary"])
				p1, err := mr.NextPart()
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				var meta struct {
					Name         string `json:"name"`
					ContentType  string `json:"contentType"`
					StorageClass string `json:"storageClass"`
				}
				_ = json.NewDecoder(p1).Decode(&meta)
				p2, err := mr.NextPart()
				if err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				data, _ := io.ReadAll(p2)
				mu.Lock()
				objects[meta.Name] = struct {
					data         []byte
					contentType  string
					storageClass string
				}{data: data, contentType: meta.ContentType, storageClass: meta.StorageClass}
				mu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]string{
					"name":       meta.Name,
					"etag":       `"gcs-etag-mp"`,
					"generation": "1001",
				})
				return
			case "resumable":
				var meta struct {
					Name         string `json:"name"`
					ContentType  string `json:"contentType"`
					StorageClass string `json:"storageClass"`
				}
				_ = json.NewDecoder(r.Body).Decode(&meta)
				mu.Lock()
				sessions["sess-1"] = struct {
					name         string
					contentType  string
					storageClass string
					buf          bytes.Buffer
				}{name: meta.Name, contentType: meta.ContentType, storageClass: meta.StorageClass}
				mu.Unlock()
				w.Header().Set("Location", srvURL+"/upload/session/sess-1")
				w.WriteHeader(http.StatusOK)
				return
			}
		}

		// Resumable chunk PUT
		if r.Method == http.MethodPut && r.URL.Path == "/upload/session/sess-1" {
			chunk, _ := io.ReadAll(r.Body)
			cr := r.Header.Get("Content-Range") // bytes start-end/total
			mu.Lock()
			s := sessions["sess-1"]
			s.buf.Write(chunk)
			sessions["sess-1"] = s
			totalLen := s.buf.Len()
			mu.Unlock()

			// Parse total from Content-Range
			slashIdx := strings.LastIndex(cr, "/")
			var expectedTotal int
			if slashIdx > 0 {
				_, _ = json.Marshal(0)
				for _, ch := range cr[slashIdx+1:] {
					expectedTotal = expectedTotal*10 + int(ch-'0')
				}
			}
			if totalLen < expectedTotal {
				w.WriteHeader(308)
				return
			}
			mu.Lock()
			objects[s.name] = struct {
				data         []byte
				contentType  string
				storageClass string
			}{data: append([]byte(nil), s.buf.Bytes()...), contentType: s.contentType, storageClass: s.storageClass}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]string{
				"name":       s.name,
				"etag":       `"gcs-etag-res"`,
				"generation": "2002",
			})
			return
		}

		// Download / Delete: /storage/v1/b/{bucket}/o/{object}
		const objPrefix = "/storage/v1/b/test-gcs-bucket/o/"
		if strings.HasPrefix(r.RequestURI, objPrefix) {
			raw := strings.TrimPrefix(r.RequestURI, objPrefix)
			if idx := strings.IndexByte(raw, '?'); idx >= 0 {
				raw = raw[:idx]
			}
			objName, _ := url.PathUnescape(raw)
			switch r.Method {
			case http.MethodGet:
				mu.Lock()
				obj, ok := objects[objName]
				mu.Unlock()
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", obj.contentType)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(obj.data)
				return
			case http.MethodDelete:
				mu.Lock()
				delete(objects, objName)
				mu.Unlock()
				w.WriteHeader(http.StatusNoContent)
				return
			}
		}

		w.WriteHeader(http.StatusNotFound)
	}))
	defer mockGCS.Close()
	srvURL = mockGCS.URL

	saJSON, pub := generateTestServiceAccountJSON(t, mockGCS.URL+"/token")
	pubKey = pub

	cfgJSON, _ := json.Marshal(storage.GCSConfig{
		Bucket:             "test-gcs-bucket",
		ServiceAccountJSON: saJSON,
		BasePrefix:         "archive",
		StorageClass:       "NEARLINE",
		APIBaseURL:         mockGCS.URL,
	})

	pRaw, err := Builder(context.Background(), nil, cfgJSON, proxy.Config{}, nil)
	if err != nil {
		t.Fatalf("Builder: %v", err)
	}
	p := pRaw.(*Provider)
	if p.Type() != "gcs" {
		t.Fatalf("expected type gcs, got %q", p.Type())
	}

	if err := p.TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}
	// Second TestConnection should reuse cached token
	if err := p.TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection cached: %v", err)
	}
	if got := tokenCalls.Load(); got != 1 {
		t.Fatalf("expected 1 token fetch due to caching, got %d", got)
	}

	// Multipart upload + 401 automatic token refresh retry
	forceNext401.Store(true)
	payload := []byte("hello google cloud storage")
	res, err := p.Upload(context.Background(), "tenant/a/file.txt", bytes.NewReader(payload), int64(len(payload)), "text/plain")
	if err != nil {
		t.Fatalf("Upload multipart: %v", err)
	}
	if res.RemotePath != "archive/tenant/a/file.txt" || res.RemoteETag != "gcs-etag-mp" || res.RemoteVersionID != "1001" {
		t.Fatalf("unexpected upload result: %+v", res)
	}
	if got := tokenCalls.Load(); got != 2 {
		t.Fatalf("expected token refresh on 401 (2 calls), got %d", got)
	}
	mu.Lock()
	if objects[res.RemotePath].storageClass != "NEARLINE" {
		mu.Unlock()
		t.Fatalf("expected storageClass NEARLINE, got %q", objects[res.RemotePath].storageClass)
	}
	mu.Unlock()

	// Download
	rc, _, ct, err := p.Download(context.Background(), res.RemotePath)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	dlBytes, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(dlBytes) != string(payload) || ct != "text/plain" {
		t.Fatalf("download mismatch: ct=%q body=%q", ct, dlBytes)
	}

	// Resumable upload path (>16 MiB uses resumable; test uploadResumable directly with 17 MiB payload)
	large := bytes.Repeat([]byte("G"), 17*1024*1024+123)
	res2, err := p.Upload(context.Background(), "tenant/large.bin", bytes.NewReader(large), int64(len(large)), "application/octet-stream")
	if err != nil {
		t.Fatalf("Upload resumable: %v", err)
	}
	if res2.RemotePath != "archive/tenant/large.bin" || res2.RemoteETag != "gcs-etag-res" || res2.RemoteVersionID != "2002" {
		t.Fatalf("unexpected resumable upload result: %+v", res2)
	}
	rc2, _, _, err := p.Download(context.Background(), res2.RemotePath)
	if err != nil {
		t.Fatalf("Download resumable: %v", err)
	}
	dlLarge, _ := io.ReadAll(rc2)
	_ = rc2.Close()
	if !bytes.Equal(dlLarge, large) {
		t.Fatalf("large download mismatch: got %d bytes, want %d bytes", len(dlLarge), len(large))
	}

	// Delete
	if err := p.Delete(context.Background(), res.RemotePath); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, _, err := p.Download(context.Background(), res.RemotePath); err == nil {
		t.Fatalf("expected error downloading deleted gcs object")
	}
}
