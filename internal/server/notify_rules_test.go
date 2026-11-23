package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"safekeep/internal/db"
	"safekeep/internal/notify"
)

func TestSMTPNotificationRulesEndToEnd(t *testing.T) {
	env := setupTestEnv(t)

	initResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/setup/init", map[string]string{
		"username": "admin",
		"password": "password123",
	})
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup init expected 201, got %d", initResp.StatusCode)
	}

	// Mock SMTP server capturing decoded bodies and subjects
	smtpLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer smtpLn.Close()

	var smtpMu sync.Mutex
	var captured []string
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
							raw := dataBuf.String()
							parts := strings.SplitN(raw, "\r\n\r\n", 2)
							bodyText := raw
							if len(parts) == 2 {
								if dec, dErr := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", "")); dErr == nil {
									bodyText = parts[0] + "\n" + string(dec)
								}
							}
							smtpMu.Lock()
							captured = append(captured, bodyText)
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
						_, _ = c.Write([]byte("354 End\r\n"))
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

	// Mock S3 origin server
	s3Srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("ETag", `"mock-etag"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer s3Srv.Close()

	// Create upstream + client
	upResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/upstreams", map[string]any{
		"name":    "notify-s3",
		"type":    "s3",
		"enabled": true,
		"config": map[string]any{
			"endpoint":         s3Srv.URL,
			"bucket":           "bucket",
			"region":           "us-east-1",
			"access_key_id":    "ak",
			"secret_access_key": "sk",
			"force_path_style": true,
		},
	})
	var upOut upstreamDTO
	if err := json.NewDecoder(upResp.Body).Decode(&upOut); err != nil {
		t.Fatal(err)
	}
	upResp.Body.Close()
	if upResp.StatusCode != http.StatusCreated {
		t.Fatalf("create upstream expected 201, got %d", upResp.StatusCode)
	}

	clResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/clients", map[string]any{
		"name":         "notify-client",
		"enabled":      true,
		"default_mode": "sync",
		"upstreams": []map[string]any{
			{"upstream_id": upOut.ID, "is_default": true, "priority": 10},
		},
	})
	var clOut struct {
		Client clientDTO `json:"client"`
		APIKey string    `json:"api_key"`
	}
	if err := json.NewDecoder(clResp.Body).Decode(&clOut); err != nil {
		t.Fatal(err)
	}
	clResp.Body.Close()
	if clResp.StatusCode != http.StatusCreated {
		t.Fatalf("create client expected 201, got %d", clResp.StatusCode)
	}

	// Verify GET returns default notifications
	getResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/settings/smtp", nil)
	var getOut smtpAdminDTO
	if err := json.NewDecoder(getResp.Body).Decode(&getOut); err != nil {
		t.Fatal(err)
	}
	getResp.Body.Close()
	if !getOut.Notifications.CircuitBreakerTripped || getOut.Notifications.BackupUpload != notify.NotifyFailure {
		t.Fatalf("unexpected default smtp notifications: %+v", getOut.Notifications)
	}

	// Reject invalid mode
	smtpHost, smtpPortStr, _ := net.SplitHostPort(smtpLn.Addr().String())
	smtpPort, _ := strconv.Atoi(smtpPortStr)
	badResp := env.adminReq(t, http.MethodPut, "/admin/api/v1/settings/smtp", map[string]any{
		"enabled": true,
		"host":    smtpHost,
		"port":    smtpPort,
		"notifications": map[string]any{
			"backup_upload": "invalid-mode",
		},
	})
	badResp.Body.Close()
	if badResp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid notification mode, got %d", badResp.StatusCode)
	}

	// Enable SMTP with backup_upload=always, client_delete=true, migration_export=true
	rules := notify.DefaultNotifyRules()
	rules.BackupUpload = notify.NotifyAlways
	rules.ClientDelete = true
	rules.MigrationExport = true
	saveResp := env.adminReq(t, http.MethodPut, "/admin/api/v1/settings/smtp", map[string]any{
		"enabled":       true,
		"host":          smtpHost,
		"port":          smtpPort,
		"tls_mode":      "none",
		"from_email":    "safekeep@example.com",
		"from_name":     "SafeKeep",
		"to_emails":     []string{"ops@example.com"},
		"notifications": rules,
	})
	saveResp.Body.Close()
	if saveResp.StatusCode != http.StatusOK {
		t.Fatalf("save smtp expected 200, got %d", saveResp.StatusCode)
	}

	// 1. API upload -> triggers notification (backup_upload=always)
	upFileResp := uploadMultipart(t, env.srv.URL+"/api/v1/backups?mode=sync&dedup=false", clOut.APIKey, "db.sql", []byte("SELECT 1;"))
	upFileResp.Body.Close()
	if upFileResp.StatusCode != http.StatusOK {
		t.Fatalf("upload expected 200, got %d", upFileResp.StatusCode)
	}

	// 2. Migration export -> triggers notification (migration_export=true)
	expResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/migration/export", map[string]any{
		"include_admins": true,
	})
	expResp.Body.Close()
	if expResp.StatusCode != http.StatusOK {
		t.Fatalf("migration export expected 200, got %d", expResp.StatusCode)
	}

	// 3. Delete client -> triggers notification (client_delete=true)
	delResp := env.adminReq(t, http.MethodDelete, "/admin/api/v1/clients/"+clOut.Client.ID, nil)
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete client expected 200, got %d", delResp.StatusCode)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		smtpMu.Lock()
		n := len(captured)
		smtpMu.Unlock()
		if n >= 3 {
			break
		}
		time.Sleep(30 * time.Millisecond)
	}

	smtpMu.Lock()
	got := append([]string(nil), captured...)
	smtpMu.Unlock()
	if len(got) != 3 {
		t.Fatalf("expected 3 notification emails, got %d: %+v", len(got), got)
	}
	joined := strings.Join(got, "\n---\n")
	for _, needle := range []string{"db.sql", "环境迁移包", "notify-client"} {
		if !strings.Contains(joined, needle) {
			t.Fatalf("expected captured emails to contain %q, got:\n%s", needle, joined)
		}
	}

	logs, total, err := env.store.ListAuditLogs(context.Background(), db.AuditFilter{
		Action: "notify.email",
		Limit:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 3 || len(logs) != 3 {
		t.Fatalf("expected 3 notify.email audit logs, got total=%d len=%d", total, len(logs))
	}
}
