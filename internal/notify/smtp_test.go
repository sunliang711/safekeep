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
