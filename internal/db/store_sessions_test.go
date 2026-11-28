package db

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestLegacyAdminRoleMigrationAndSessionPersistence(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE admins (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL,
		is_active INTEGER NOT NULL DEFAULT 1, last_login_at DATETIME, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	if _, err := legacy.Exec(`INSERT INTO admins(id, username, password_hash, created_at, updated_at) VALUES ('legacy', 'legacy', 'hash', ?, ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	admin, err := store.GetAdminByID(ctx, "legacy")
	if err != nil || admin.Role != RoleAdmin {
		t.Fatalf("legacy admin must retain full access: %+v %v", admin, err)
	}
	session := &AdminSession{ID: "session", AdminID: admin.ID, TokenHash: "token-digest", ClientIP: "127.0.0.1", UserAgent: "test", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := store.CreateAdminSession(ctx, session, admin); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = Open(path, dir)
	if err != nil {
		t.Fatal(err)
	}
	persisted, err := store.GetAdminSession(ctx, session.TokenHash)
	if err != nil || persisted.ID != session.ID {
		t.Fatalf("session did not survive reopen: %+v %v", persisted, err)
	}
	changed := *admin
	changed.PasswordHash = "stale-password-hash"
	if err := store.CreateAdminSession(ctx, &AdminSession{ID: "stale", TokenHash: "stale-digest", CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(time.Hour)}, &changed); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stale credentials created a session: %v", err)
	}
	if err := store.UpdateAdminPassword(ctx, admin.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAdminSession(ctx, session.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Fatalf("password change did not revoke persisted session: %v", err)
	}
}
