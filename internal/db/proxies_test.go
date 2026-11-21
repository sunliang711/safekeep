package db

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyProxyUpgradeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	old, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.ExecContext(ctx, `CREATE TABLE upstreams (
		id TEXT PRIMARY KEY, name TEXT NOT NULL UNIQUE, type TEXT NOT NULL,
		enabled INTEGER NOT NULL DEFAULT 1, config_encrypted TEXT NOT NULL,
		proxy_enabled INTEGER NOT NULL DEFAULT 0, proxy_type TEXT NOT NULL DEFAULT '',
		proxy_host TEXT NOT NULL DEFAULT '', proxy_port INTEGER NOT NULL DEFAULT 0,
		proxy_username_encrypted TEXT NOT NULL DEFAULT '', proxy_password_encrypted TEXT NOT NULL DEFAULT '',
		last_test_status TEXT NOT NULL DEFAULT 'untested', last_test_error TEXT NOT NULL DEFAULT '',
		last_tested_at DATETIME, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`)
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{true, false} {
		id := "direct"
		if enabled {
			id = "proxied"
		}
		_, err = old.ExecContext(ctx, `INSERT INTO upstreams(id, name, type, config_encrypted,
			proxy_enabled, proxy_type, proxy_host, proxy_port, proxy_username_encrypted, proxy_password_encrypted,
			created_at, updated_at) VALUES (?, ?, 's3', 'config-ciphertext', ?, 'socks5', 'localhost', 1080,
			'user-ciphertext', 'pass-ciphertext', ?, ?)`, id, id, boolToInt(enabled), time.Now().UTC(), time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	var proxyID string
	for i := 0; i < 2; i++ {
		s, err := Open(path, dir)
		if err != nil {
			t.Fatal(err)
		}
		list, err := s.ListProxies(ctx)
		if err != nil || len(list) != 1 {
			t.Fatalf("upgrade %d: proxies=%+v error=%v", i, list, err)
		}
		u, err := s.GetUpstreamByID(ctx, "proxied")
		if err != nil || u.ProxyID != list[0].ID || !u.ProxyEnabled || u.ProxyPasswordEncrypted != "pass-ciphertext" || u.ProxyUsernameEncrypted != "user-ciphertext" {
			t.Fatalf("proxy credentials or binding changed: %+v error=%v", u, err)
		}
		if i > 0 && u.ProxyID != proxyID {
			t.Fatal("reopening database changed proxy identity")
		}
		proxyID = u.ProxyID
		direct, err := s.GetUpstreamByID(ctx, "direct")
		if err != nil || direct.ProxyID != "" || direct.ProxyEnabled {
			t.Fatalf("direct upstream changed: %+v error=%v", direct, err)
		}
		// 外键约束也保护绕过管理接口的并发删除。
		if _, err := s.DB().ExecContext(ctx, `DELETE FROM proxies WHERE id=?`, proxyID); err == nil {
			t.Fatal("database allowed deletion of a referenced proxy")
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	}
}
