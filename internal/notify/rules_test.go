package notify

import (
	"bufio"
	"context"
	"net"
	"strings"
	"sync"
	"testing"
)

func TestNotifyRulesDefaultsNormalizeAndValidate(t *testing.T) {
	def := DefaultNotifyRules()
	if !def.CircuitBreakerTripped || !def.OversizeRejected {
		t.Fatalf("expected alert rules enabled by default: %+v", def)
	}
	if def.BackupUpload != NotifyFailure || def.RemoteBackup != NotifyFailure || def.MigrationBackup != NotifyFailure {
		t.Fatalf("expected backup/remote/migration rules to default to failure: %+v", def)
	}

	if NotifyOff.ShouldNotify(true) || NotifyOff.ShouldNotify(false) {
		t.Fatal("off mode must never notify")
	}
	if NotifyFailure.ShouldNotify(true) || !NotifyFailure.ShouldNotify(false) {
		t.Fatal("failure mode must notify only on failure")
	}
	if !NotifyAlways.ShouldNotify(true) || !NotifyAlways.ShouldNotify(false) {
		t.Fatal("always mode must notify on both success and failure")
	}

	bad := SMTPConfig{
		Enabled: false,
		Notifications: NotifyRules{
			BackupUpload: "sometimes",
		},
	}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected invalid notification mode to fail validation")
	}

	norm := (NotifyRules{BackupUpload: " ALWAYS ", RemoteBackup: "invalid"}).Normalize()
	if norm.BackupUpload != NotifyAlways || norm.RemoteBackup != NotifyFailure {
		t.Fatalf("unexpected normalized rules: %+v", norm)
	}
}

func TestDispatcherGatingAndAudit(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	sentCount := 0
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				_, _ = c.Write([]byte("220 mock-smtp\r\n"))
				inData := false
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if inData {
						if line == ".\r\n" {
							inData = false
							mu.Lock()
							sentCount++
							mu.Unlock()
							_, _ = c.Write([]byte("250 OK\r\n"))
						}
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

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port := 0
	for _, ch := range portStr {
		port = port*10 + int(ch-'0')
	}

	cfg := DefaultSMTPConfig()
	cfg.Enabled = true
	cfg.Host = host
	cfg.Port = port
	cfg.TLSMode = "none"
	cfg.FromEmail = "safekeep@example.com"
	cfg.ToEmails = []string{"ops@example.com"}

	var audits []string
	d := NewDispatcher(
		context.Background(),
		func(ctx context.Context) (SMTPConfig, error) { return cfg, nil },
		func(ctx context.Context, action, resourceID, status string, detail map[string]any) {
			mu.Lock()
			audits = append(audits, action+":"+status)
			mu.Unlock()
		},
	)

	// Should NOT send when ShouldSend returns false
	d.Dispatch(Event{
		EventKey:   "backup.upload",
		ResourceID: "task-1",
		Subject:    "ignored",
		Body:       "ignored",
		ShouldSend: func(r NotifyRules) bool { return r.BackupUpload.ShouldNotify(true) },
	})
	d.Wait()

	// Should send when ShouldSend returns true
	d.Dispatch(Event{
		EventKey:   "backup.upload",
		ResourceID: "task-2",
		Subject:    "failed",
		Body:       "body",
		ShouldSend: func(r NotifyRules) bool { return r.BackupUpload.ShouldNotify(false) },
	})
	d.Wait()

	mu.Lock()
	defer mu.Unlock()
	if sentCount != 1 {
		t.Fatalf("expected 1 sent email, got %d", sentCount)
	}
	if len(audits) != 1 || audits[0] != "notify.email:success" {
		t.Fatalf("unexpected audits: %+v", audits)
	}
}
