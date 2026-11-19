package proxy

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestHTTPProxyRouting(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello-from-target"))
	}))
	defer target.Close()

	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		if r.Header.Get("Proxy-Authorization") == "" {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), r.Method, r.RequestURI, r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer proxySrv.Close()

	pu, _ := url.Parse(proxySrv.URL)
	host, portStr, _ := net.SplitHostPort(pu.Host)
	port, _ := strconv.Atoi(portStr)

	client, err := NewHTTPClient(Config{
		Enabled:  true,
		Type:     "http",
		Host:     host,
		Port:     port,
		Username: "u1",
		Password: "p1",
	})
	if err != nil {
		t.Fatalf("NewHTTPClient: %v", err)
	}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("GET via HTTP proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-from-target" {
		t.Fatalf("unexpected body: %q", body)
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("expected 1 proxy hit, got %d", proxyHits.Load())
	}
}

func TestSOCKS5ProxyRouting(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello-via-socks5"))
	}))
	defer target.Close()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen socks5: %v", err)
	}
	defer ln.Close()

	var socksHits atomic.Int32
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleMinimalSOCKS5(conn, &socksHits)
		}
	}()

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	client, err := NewHTTPClient(Config{
		Enabled: true,
		Type:    "socks5",
		Host:    host,
		Port:    port,
	})
	if err != nil {
		t.Fatalf("NewHTTPClient socks5: %v", err)
	}

	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("GET via SOCKS5 proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "hello-via-socks5" {
		t.Fatalf("unexpected body: %q", body)
	}
	if socksHits.Load() != 1 {
		t.Fatalf("expected 1 socks5 connection, got %d", socksHits.Load())
	}
}

func handleMinimalSOCKS5(c net.Conn, hits *atomic.Int32) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 260)
	if _, err := io.ReadFull(c, buf[:2]); err != nil || buf[0] != 0x05 {
		return
	}
	nMethods := int(buf[1])
	if _, err := io.ReadFull(c, buf[:nMethods]); err != nil {
		return
	}
	_, _ = c.Write([]byte{0x05, 0x00}) // no auth

	if _, err := io.ReadFull(c, buf[:4]); err != nil || buf[1] != 0x01 {
		return
	}
	var host string
	switch buf[3] {
	case 0x01: // IPv4
		if _, err := io.ReadFull(c, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case 0x03: // Domain
		if _, err := io.ReadFull(c, buf[:1]); err != nil {
			return
		}
		dLen := int(buf[0])
		if _, err := io.ReadFull(c, buf[:dLen]); err != nil {
			return
		}
		host = string(buf[:dLen])
	default:
		return
	}
	if _, err := io.ReadFull(c, buf[:2]); err != nil {
		return
	}
	port := int(buf[0])<<8 | int(buf[1])
	remote, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), 3*time.Second)
	if err != nil {
		return
	}
	defer remote.Close()
	hits.Add(1)
	_, _ = c.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(remote, c); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, remote); done <- struct{}{} }()
	<-done
}

func TestProxyConfigValidate(t *testing.T) {
	if err := (Config{Enabled: true, Type: "ftp", Host: "127.0.0.1", Port: 8080}).Validate(); err == nil {
		t.Fatal("expected error for unsupported proxy type")
	}
	if err := (Config{Enabled: true, Type: "socks5", Host: "127.0.0.1", Port: 0}).Validate(); err == nil {
		t.Fatal("expected error for invalid port")
	}
	if err := (Config{Enabled: true, Type: "socks5", Host: "127.0.0.1", Port: 1080}).Validate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_ = strings.TrimSpace
}
