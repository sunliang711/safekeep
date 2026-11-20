package r2

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"safekeep/internal/proxy"
	"safekeep/internal/storage"
)

func TestResolveR2Endpoint(t *testing.T) {
	cases := []struct {
		name    string
		cfg     storage.R2Config
		want    string
		wantErr bool
	}{
		{
			name: "default jurisdiction",
			cfg:  storage.R2Config{AccountID: "acct123"},
			want: "https://acct123.r2.cloudflarestorage.com",
		},
		{
			name: "eu jurisdiction",
			cfg:  storage.R2Config{AccountID: "acct123", Jurisdiction: "eu"},
			want: "https://acct123.eu.r2.cloudflarestorage.com",
		},
		{
			name: "fedramp jurisdiction",
			cfg:  storage.R2Config{AccountID: "acct123", Jurisdiction: "fedramp"},
			want: "https://acct123.fedramp.r2.cloudflarestorage.com",
		},
		{
			name: "custom endpoint without scheme",
			cfg:  storage.R2Config{Endpoint: "r2.example.com/"},
			want: "https://r2.example.com",
		},
		{
			name:    "missing account and endpoint",
			cfg:     storage.R2Config{},
			wantErr: true,
		},
		{
			name:    "invalid jurisdiction",
			cfg:     storage.R2Config{AccountID: "acct123", Jurisdiction: "mars"},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := storage.ResolveR2Endpoint(tc.cfg)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestR2ProviderLifecycle(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}

	mockR2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/my-r2-bucket/")
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			b, _ := io.ReadAll(r.Body)
			mu.Lock()
			objects[key] = b
			mu.Unlock()
			w.Header().Set("ETag", `"r2-etag-1"`)
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			mu.Lock()
			b, ok := objects[key]
			mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/plain")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(b)
		case http.MethodDelete:
			mu.Lock()
			delete(objects, key)
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer mockR2.Close()

	cfgJSON, _ := json.Marshal(storage.R2Config{
		AccountID:       "acct123",
		Endpoint:        mockR2.URL,
		Bucket:          "my-r2-bucket",
		AccessKeyID:     "r2-ak",
		SecretAccessKey: "r2-sk",
		BasePrefix:      "backups",
		ForcePathStyle:  true,
		StorageClass:    "STANDARD_IA",
	})

	p, err := Builder(context.Background(), nil, cfgJSON, proxy.Config{}, nil)
	if err != nil {
		t.Fatalf("Builder: %v", err)
	}
	if p.Type() != "r2" {
		t.Fatalf("expected type r2, got %q", p.Type())
	}

	if err := p.TestConnection(context.Background()); err != nil {
		t.Fatalf("TestConnection: %v", err)
	}

	payload := []byte("hello cloudflare r2")
	res, err := p.Upload(context.Background(), "tenant/doc.txt", bytes.NewReader(payload), int64(len(payload)), "text/plain")
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if res.RemotePath != "backups/tenant/doc.txt" || res.RemoteETag != "r2-etag-1" {
		t.Fatalf("unexpected upload result: %+v", res)
	}

	rc, _, ct, err := p.Download(context.Background(), res.RemotePath)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	gotBytes, _ := io.ReadAll(rc)
	_ = rc.Close()
	if string(gotBytes) != string(payload) || ct != "text/plain" {
		t.Fatalf("download mismatch: ct=%q body=%q", ct, gotBytes)
	}

	if err := p.Delete(context.Background(), res.RemotePath); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, _, _, err := p.Download(context.Background(), res.RemotePath); err == nil {
		t.Fatalf("expected error downloading deleted object")
	}
}
