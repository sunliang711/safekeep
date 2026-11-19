package notify

import (
	"bufio"
	"context"
	"encoding/base64"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSMTPSendPlainMockServer(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var receivedFrom, receivedTo, receivedData string
	done := make(chan struct{})

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(done)

		r := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 localhost ESMTP\r\n"))

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
					mu.Lock()
					receivedData = dataBuf.String()
					mu.Unlock()
					_, _ = conn.Write([]byte("250 OK\r\n"))
					continue
				}
				dataBuf.WriteString(line)
				continue
			}

			upper := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(upper, "EHLO"), strings.HasPrefix(upper, "HELO"):
				_, _ = conn.Write([]byte("250-localhost\r\n250 AUTH PLAIN\r\n"))
			case strings.HasPrefix(upper, "AUTH PLAIN"):
				_, _ = conn.Write([]byte("235 Authentication succeeded\r\n"))
			case strings.HasPrefix(upper, "MAIL FROM:"):
				mu.Lock()
				receivedFrom = strings.TrimSpace(line[10:])
				mu.Unlock()
				_, _ = conn.Write([]byte("250 OK\r\n"))
			case strings.HasPrefix(upper, "RCPT TO:"):
				mu.Lock()
				receivedTo = strings.TrimSpace(line[8:])
				mu.Unlock()
				_, _ = conn.Write([]byte("250 OK\r\n"))
			case upper == "DATA":
				inData = true
				_, _ = conn.Write([]byte("354 Start mail input\r\n"))
			case upper == "QUIT":
				_, _ = conn.Write([]byte("221 Bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("250 OK\r\n"))
			}
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cfg := SMTPConfig{
		Enabled:   true,
		Host:      host,
		Port:      port,
		TLSMode:   "none",
		FromEmail: "alerts@safekeep.local",
		FromName:  "SafeKeep 告警",
		ToEmails:  []string{"ops@safekeep.local"},
	}
	if err := Send(ctx, cfg, "SafeKeep 熔断告警", "每日发送超过 1GB，服务已熔断。"); err != nil {
		t.Fatalf("Send failed: %v", err)
	}
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(receivedFrom, "alerts@safekeep.local") {
		t.Fatalf("unexpected MAIL FROM: %q", receivedFrom)
	}
	if !strings.Contains(receivedTo, "ops@safekeep.local") {
		t.Fatalf("unexpected RCPT TO: %q", receivedTo)
	}
	parts := strings.SplitN(receivedData, "\r\n\r\n", 2)
	if len(parts) != 2 {
		t.Fatalf("unexpected MIME payload: %q", receivedData)
	}
	decodedBody, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(parts[1], "\r\n", ""))
	if err != nil || !strings.Contains(string(decodedBody), "每日发送超过 1GB") {
		t.Fatalf("unexpected body decoded=%q err=%v", string(decodedBody), err)
	}
}

func TestSMTPSendDisplayNameAndAuthLogin(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	var mu sync.Mutex
	var gotFrom, gotTo, gotUser, gotPass string
	done := make(chan struct{})

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(done)

		r := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 localhost ESMTP\r\n"))

		inData := false
		loginStep := 0
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					_, _ = conn.Write([]byte("250 OK\r\n"))
				}
				continue
			}
			if loginStep == 1 {
				b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
				mu.Lock()
				gotUser = string(b)
				mu.Unlock()
				loginStep = 2
				_, _ = conn.Write([]byte("334 " + base64.StdEncoding.EncodeToString([]byte("Password:")) + "\r\n"))
				continue
			}
			if loginStep == 2 {
				b, _ := base64.StdEncoding.DecodeString(strings.TrimSpace(line))
				mu.Lock()
				gotPass = string(b)
				mu.Unlock()
				loginStep = 0
				_, _ = conn.Write([]byte("235 Authentication succeeded\r\n"))
				continue
			}

			upper := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(upper, "EHLO"):
				_, _ = conn.Write([]byte("250-localhost\r\n250 AUTH LOGIN\r\n"))
			case upper == "AUTH LOGIN":
				loginStep = 1
				_, _ = conn.Write([]byte("334 " + base64.StdEncoding.EncodeToString([]byte("Username:")) + "\r\n"))
			case strings.HasPrefix(upper, "MAIL FROM:"):
				mu.Lock()
				gotFrom = strings.TrimSpace(line[10:])
				mu.Unlock()
				_, _ = conn.Write([]byte("250 OK\r\n"))
			case strings.HasPrefix(upper, "RCPT TO:"):
				mu.Lock()
				gotTo = strings.TrimSpace(line[8:])
				mu.Unlock()
				_, _ = conn.Write([]byte("250 OK\r\n"))
			case upper == "DATA":
				inData = true
				_, _ = conn.Write([]byte("354 Start mail input\r\n"))
			case upper == "QUIT":
				_, _ = conn.Write([]byte("221 Bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("250 OK\r\n"))
			}
		}
	}()

	// Verify LOGIN auth refuses unencrypted connection ("none") when username is set
	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	err = Send(context.Background(), SMTPConfig{
		Enabled:   true,
		Host:      host,
		Port:      port,
		TLSMode:   "none",
		Username:  "user@example.com",
		Password:  "secret-pass",
		FromEmail: "SafeKeep <alerts@safekeep.local>",
		ToEmails:  []string{"Ops <ops@safekeep.local>"},
	}, "test", "body")
	if err == nil || !strings.Contains(err.Error(), "unencrypted") {
		t.Fatalf("expected unencrypted connection refusal, got %v", err)
	}
	<-done

	// Re-test with display names without auth to ensure pure addresses are sent in MAIL FROM / RCPT TO
	ln2, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln2.Close()
	done2 := make(chan struct{})
	go func() {
		conn, err := ln2.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer close(done2)
		r := bufio.NewReader(conn)
		_, _ = conn.Write([]byte("220 localhost ESMTP\r\n"))
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			if inData {
				if line == ".\r\n" {
					inData = false
					_, _ = conn.Write([]byte("250 OK\r\n"))
				}
				continue
			}
			upper := strings.ToUpper(strings.TrimSpace(line))
			switch {
			case strings.HasPrefix(upper, "EHLO"):
				_, _ = conn.Write([]byte("250-localhost\r\n250 OK\r\n"))
			case strings.HasPrefix(upper, "MAIL FROM:"):
				mu.Lock()
				gotFrom = strings.TrimSpace(line[10:])
				mu.Unlock()
				_, _ = conn.Write([]byte("250 OK\r\n"))
			case strings.HasPrefix(upper, "RCPT TO:"):
				mu.Lock()
				gotTo = strings.TrimSpace(line[8:])
				mu.Unlock()
				_, _ = conn.Write([]byte("250 OK\r\n"))
			case upper == "DATA":
				inData = true
				_, _ = conn.Write([]byte("354 Start mail input\r\n"))
			case upper == "QUIT":
				_, _ = conn.Write([]byte("221 Bye\r\n"))
				return
			default:
				_, _ = conn.Write([]byte("250 OK\r\n"))
			}
		}
	}()

	host2, portStr2, _ := net.SplitHostPort(ln2.Addr().String())
	port2, _ := strconv.Atoi(portStr2)
	if err := Send(context.Background(), SMTPConfig{
		Enabled:   true,
		Host:      host2,
		Port:      port2,
		TLSMode:   "none",
		FromEmail: "SafeKeep <alerts@safekeep.local>",
		ToEmails:  []string{"Ops <ops@safekeep.local>"},
	}, "test display", "hello"); err != nil {
		t.Fatalf("Send display-name failed: %v", err)
	}
	<-done2
	mu.Lock()
	defer mu.Unlock()
	_ = gotUser
	_ = gotPass
	if gotFrom != "<alerts@safekeep.local>" {
		t.Fatalf("expected clean MAIL FROM <alerts@safekeep.local>, got %q", gotFrom)
	}
	if gotTo != "<ops@safekeep.local>" {
		t.Fatalf("expected clean RCPT TO <ops@safekeep.local>, got %q", gotTo)
	}
}

func TestSMTPSendRespectsCallerTimeout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Accept connection and intentionally never send 220 banner.
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		time.Sleep(500 * time.Millisecond)
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	start := time.Now()
	err = Send(ctx, SMTPConfig{
		Enabled:   true,
		Host:      host,
		Port:      port,
		TLSMode:   "none",
		FromEmail: "alerts@safekeep.local",
		ToEmails:  []string{"ops@safekeep.local"},
	}, "timeout", "test")
	if err == nil {
		t.Fatal("expected timeout error from unresponsive SMTP server")
	}
	if time.Since(start) >= time.Second {
		t.Fatalf("expected Send to abort promptly on caller context deadline, took %v", time.Since(start))
	}
}
