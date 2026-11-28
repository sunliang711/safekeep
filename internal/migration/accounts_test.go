package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"safekeep/internal/crypto"
	"safekeep/internal/db"
)

func TestAccountRolesMigrationAndLegacyBundle(t *testing.T) {
	ctx := context.Background()
	open := func() (*db.Store, *Service) {
		t.Helper()
		dir := t.TempDir()
		store, err := db.Open(filepath.Join(dir, "accounts.db"), dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = store.Close() })
		cipher, err := crypto.NewCipher(bytes.Repeat([]byte{0x31}, 32))
		if err != nil {
			t.Fatal(err)
		}
		return store, NewService(store, cipher)
	}
	source, exporter := open()
	for _, account := range []db.Admin{
		{ID: "owner", Username: "owner", PasswordHash: "owner-hash", Role: db.RoleAdmin, IsActive: true},
		{ID: "reader", Username: "reader", PasswordHash: "reader-hash", Role: db.RoleViewer, IsActive: true},
	} {
		if err := source.CreateAdmin(ctx, &account); err != nil {
			t.Fatal(err)
		}
	}
	blob, err := exporter.Export(ctx, ExportOptions{IncludeAdmins: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, legacy := range []bool{false, true} {
		t.Run(map[bool]string{false: "roles", true: "legacy"}[legacy], func(t *testing.T) {
			store, importer := open()
			input := blob
			if legacy {
				// 解密后移除角色字段并重新计算校验值，模拟旧版本迁移包。
				var envelope map[string]any
				plain, err := crypto.DecryptWithPassphrase(blob, "")
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(plain, &envelope); err != nil {
					t.Fatal(err)
				}
				data, ok := envelope["data"].(map[string]any)
				if !ok {
					t.Fatal("missing clear bundle data")
				}
				for _, value := range data["admins"].([]any) {
					delete(value.(map[string]any), "role")
				}
				rawData, err := json.Marshal(data)
				if err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(rawData)
				envelope["checksum_sha256"] = hex.EncodeToString(sum[:])
				plain, err = json.Marshal(envelope)
				if err != nil {
					t.Fatal(err)
				}
				input, err = crypto.EncryptWithPassphrase(plain, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			if _, err := importer.Import(ctx, input, ImportOptions{Mode: "merge", ConflictStrategy: "overwrite", IncludeAdmins: true}); err != nil {
				t.Fatal(err)
			}
			reader, err := store.GetAdminByID(ctx, "reader")
			if err != nil {
				t.Fatal(err)
			}
			want := db.RoleViewer
			if legacy {
				want = db.RoleAdmin
			}
			if reader.Role != want {
				t.Fatalf("reader role = %s, want %s", reader.Role, want)
			}
		})
	}
}
