package migration

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"safekeep/internal/crypto"
	"safekeep/internal/db"
)

func proxyMigrationEnv(t *testing.T, key byte) (*db.Store, *crypto.Cipher, *Service) {
	t.Helper()
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "proxy.db"), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cipher, err := crypto.NewCipher(bytes.Repeat([]byte{key}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return store, cipher, NewService(store, cipher)
}

func TestSharedProxyBundleRoundTripAndConflicts(t *testing.T) {
	ctx := context.Background()
	source, cipher, svc := proxyMigrationEnv(t, 0x11)
	user, err := cipher.EncryptString("source-user")
	if err != nil {
		t.Fatal(err)
	}
	pass, err := cipher.EncryptString("source-password")
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"shared", "unused"} {
		if err := source.CreateProxy(ctx, &db.Proxy{ID: id, Name: id, Type: "socks5", Host: "source.local", Port: 1080,
			UsernameEncrypted: user, PasswordEncrypted: pass}); err != nil {
			t.Fatal(err)
		}
	}
	config, err := cipher.EncryptString(`{"bucket":"bucket","access_key_id":"key","secret_access_key":"secret"}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		if err := source.CreateUpstream(ctx, &db.Upstream{ID: id, Name: id, Type: "s3", Enabled: true,
			ConfigEncrypted: config, ProxyID: "shared"}); err != nil {
			t.Fatal(err)
		}
	}
	bundle, err := svc.Export(ctx, ExportOptions{Passphrase: "test-passphrase"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ mode, conflict, wantID, wantHost, wantPassword string }{
		{"replace", "skip", "shared", "source.local", "source-password"},
		{"merge", "skip", "target-shared", "target.local", "target-password"},
		{"merge", "overwrite", "target-shared", "source.local", "source-password"},
	} {
		t.Run(tc.mode+"-"+tc.conflict, func(t *testing.T) {
			target, targetCipher, targetSvc := proxyMigrationEnv(t, 0x22)
			targetPass, err := targetCipher.EncryptString("target-password")
			if err != nil {
				t.Fatal(err)
			}
			if err := target.CreateProxy(ctx, &db.Proxy{ID: "target-shared", Name: "shared", Type: "http", Host: "target.local", Port: 8080, PasswordEncrypted: targetPass}); err != nil {
				t.Fatal(err)
			}
			summary, err := targetSvc.Inspect(ctx, bundle, "test-passphrase")
			if err != nil || summary.Counts.Proxies != 2 || len(summary.ProxyConflicts) != 1 {
				t.Fatalf("unexpected inspection: %+v error=%v", summary, err)
			}
			if _, err := targetSvc.Import(ctx, bundle, ImportOptions{Passphrase: "test-passphrase", Mode: tc.mode, ConflictStrategy: tc.conflict}); err != nil {
				t.Fatal(err)
			}
			proxies, err := target.ListProxies(ctx)
			if err != nil || len(proxies) != 2 {
				t.Fatalf("unused proxy was lost: %+v error=%v", proxies, err)
			}
			for _, id := range []string{"one", "two"} {
				u, err := target.GetUpstreamByID(ctx, id)
				if err != nil || u.ProxyID != tc.wantID || u.ProxyHost != tc.wantHost || !u.ProxyEnabled {
					t.Fatalf("shared binding not preserved: %+v error=%v", u, err)
				}
				plain, err := targetCipher.DecryptString(u.ProxyPasswordEncrypted)
				if err != nil || plain != tc.wantPassword {
					t.Fatal("proxy password not re-encrypted with target master key")
				}
			}
		})
	}
}

func TestLegacyProxyBundleImportAndMissingReferenceRollback(t *testing.T) {
	ctx := context.Background()
	store, cipher, svc := proxyMigrationEnv(t, 0x33)
	now := time.Now().UTC()
	legacy := Bundle{FormatVersion: FormatVersion, Data: BundleData{Upstreams: []UpstreamRecord{{
		ID: "legacy", Name: "legacy", Type: "s3", Enabled: true, ConfigPlainJSON: `{}`,
		ProxyEnabled: true, ProxyType: "http", ProxyHost: "localhost", ProxyPort: 8888,
		ProxyPassword: "legacy-password", LastTestStatus: "untested", CreatedAt: now, UpdatedAt: now,
	}}}}
	encode := func(b Bundle) []byte {
		raw, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		blob, err := crypto.EncryptWithPassphrase(raw, "")
		if err != nil {
			t.Fatal(err)
		}
		return blob
	}
	if _, err := svc.Import(ctx, encode(legacy), ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	u, err := store.GetUpstreamByID(ctx, "legacy")
	if err != nil || u.ProxyID == "" || !u.ProxyEnabled {
		t.Fatalf("legacy proxy not migrated: %+v error=%v", u, err)
	}
	pass, err := cipher.DecryptString(u.ProxyPasswordEncrypted)
	if err != nil || pass != "legacy-password" {
		t.Fatal("legacy proxy credentials changed")
	}
	legacy.Data.Upstreams[0].ProxyID = "missing"
	if _, err := svc.Import(ctx, encode(legacy), ImportOptions{Mode: "replace"}); err == nil {
		t.Fatal("expected missing proxy reference to fail")
	}
	preserved, err := store.GetUpstreamByID(ctx, "legacy")
	if err != nil || preserved.ProxyID != u.ProxyID {
		t.Fatal("failed replace import did not roll back original data")
	}
}
