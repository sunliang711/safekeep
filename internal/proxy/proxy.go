package proxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	netproxy "golang.org/x/net/proxy"
)

// Config defines per-upstream outbound proxy settings.
type Config struct {
	Enabled  bool   `json:"proxy_enabled"`
	Type     string `json:"proxy_type"` // http | https | socks5
	Host     string `json:"proxy_host"`
	Port     int    `json:"proxy_port"`
	Username string `json:"proxy_username,omitempty"`
	Password string `json:"proxy_password,omitempty"`
}

// Validate checks proxy configuration fields when enabled.
func (c Config) Validate() error {
	if !c.Enabled {
		return nil
	}
	t := strings.ToLower(strings.TrimSpace(c.Type))
	if t != "http" && t != "https" && t != "socks5" {
		return fmt.Errorf("unsupported proxy_type %q (must be http, https, or socks5)", c.Type)
	}
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("proxy_host is required when proxy is enabled")
	}
	if c.Port <= 0 || c.Port > 65535 {
		return fmt.Errorf("proxy_port must be between 1 and 65535")
	}
	return nil
}

// NewHTTPClient builds an *http.Client configured with the given HTTP, HTTPS, or SOCKS5 proxy.
// Overall request cancellation is driven by context rather than http.Client.Timeout so large
// streaming uploads are not aborted mid-transfer.
func NewHTTPClient(cfg Config) (*http.Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	baseDialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	tr := &http.Transport{
		DialContext:           baseDialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
	}

	if !cfg.Enabled {
		return &http.Client{Transport: tr}, nil
	}

	addr := net.JoinHostPort(strings.TrimSpace(cfg.Host), strconv.Itoa(cfg.Port))
	switch strings.ToLower(strings.TrimSpace(cfg.Type)) {
	case "http", "https":
		u := &url.URL{
			Scheme: strings.ToLower(strings.TrimSpace(cfg.Type)),
			Host:   addr,
		}
		if cfg.Username != "" || cfg.Password != "" {
			u.User = url.UserPassword(cfg.Username, cfg.Password)
		}
		tr.Proxy = http.ProxyURL(u)
	case "socks5":
		var auth *netproxy.Auth
		if cfg.Username != "" || cfg.Password != "" {
			auth = &netproxy.Auth{
				User:     cfg.Username,
				Password: cfg.Password,
			}
		}
		dialer, err := netproxy.SOCKS5("tcp", addr, auth, baseDialer)
		if err != nil {
			return nil, fmt.Errorf("create socks5 dialer: %w", err)
		}
		if cd, ok := dialer.(netproxy.ContextDialer); ok {
			tr.DialContext = cd.DialContext
		} else {
			tr.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
				return dialer.Dial(network, target)
			}
		}
	}

	return &http.Client{Transport: tr}, nil
}
