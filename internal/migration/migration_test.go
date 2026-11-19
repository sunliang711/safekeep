package migration

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
	"time"

	"safekeep/internal/crypto"
	"safekeep/internal/db"
)

func TestExportInspectImportReencryptsSecrets(t *testing.T) {
	ctx := context.Background()

	// Source environment A with masterKeyA
	dirA := t.TempDir()
	storeA, err := db.Open(filepath.Join(dirA, "a.db"), dirA)
	if err != nil {
		t.Fatalf("open storeA: %v", err)
	}
	defer storeA.Close()

	cipherA, _ := crypto.NewCipher(bytes.Repeat([]byte{0xAA}, 32))
	cfgEncA, _ := cipherA.EncryptString(`{"bucket":"bkt-a","access_key_id":"ak","secret_access_key":"super-s3-secret"}`)
	pPassEncA, _ := cipherA.EncryptString("proxy-secret-pass")

	up := &db.Upstream{
		ID:                     "up_1",
		Name:                   "prod-s3",
		Type:                   "s3",
		Enabled:                true,
		ConfigEncrypted:        cfgEncA,
		ProxyEnabled:           true,
		ProxyType:              "socks5",
		ProxyHost:              "127.0.0.1",
		ProxyPort:              1080,
		ProxyPasswordEncrypted: pPassEncA,
	}
	if err := storeA.CreateUpstream(ctx, up); err != nil {
		t.Fatalf("CreateUpstream: %v", err)
	}

	plainKey, prefix, hash, _ := crypto.GenerateAPIKey()
	whEncA, _ := cipherA.EncryptString("wh-secret")
	cl := &db.Client{
		ID:                     "cl_1",
		Name:                   "acme",
		KeyPrefix:              prefix,
		KeyHash:                hash,
		Enabled:                true,
		DefaultMode:            "sync",
		PathPrefix:             "acme",
		WebhookURL:             "https://example.com/hook",
		WebhookSecretEncrypted: whEncA,
	}
	if err := storeA.CreateClient(ctx, cl, []db.ClientUpstream{{ClientID: cl.ID, UpstreamID: up.ID, IsDefault: true, Priority: 10}}); err != nil {
		t.Fatalf("CreateClient: %v", err)
	}

	task := &db.BackupTask{
		ID:               "task_1",
		ClientID:         cl.ID,
		Mode:             "sync",
		Status:           "succeeded",
		OriginalFilename: "db.sql",
		ContentType:      "application/sql",
		FileSize:         123,
		SHA256:           "deadbeef",
		SpoolCleaned:     true,
		CreatedAt:        time.Now().UTC(),
	}
	item := db.BackupTaskItem{
		ID:           "item_1",
		TaskID:       task.ID,
		UpstreamID:   up.ID,
		UpstreamName: up.Name,
		UpstreamType: up.Type,
		RemotePath:   "acme/20261119_task_1_db.sql",
		Status:       "succeeded",
		MaxAttempts:  3,
	}
	if err := storeA.CreateBackupTask(ctx, task, []db.BackupTaskItem{item}); err != nil {
		t.Fatalf("CreateBackupTask: %v", err)
	}

	migA := NewService(storeA, cipherA)
	bundle, err := migA.Export(ctx, ExportOptions{
		IncludeTasks: true,
		Passphrase:   "transfer-pass-123",
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Target environment B with completely different masterKeyB
	dirB := t.TempDir()
	storeB, err := db.Open(filepath.Join(dirB, "b.db"), dirB)
	if err != nil {
		t.Fatalf("open storeB: %v", err)
	}
	defer storeB.Close()

	cipherB, _ := crypto.NewCipher(bytes.Repeat([]byte{0xBB}, 32))
	migB := NewService(storeB, cipherB)

	summary, err := migB.Inspect(ctx, bundle, "transfer-pass-123")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if summary.Counts.Upstreams != 1 || summary.Counts.Clients != 1 || summary.Counts.BackupTasks != 1 {
		t.Fatalf("unexpected inspect counts: %+v", summary.Counts)
	}

	counts, err := migB.Import(ctx, bundle, ImportOptions{
		Passphrase:   "transfer-pass-123",
		Mode:         "replace",
		IncludeTasks: true,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if counts.Upstreams != 1 || counts.Clients != 1 || counts.BackupTasks != 1 {
		t.Fatalf("unexpected import counts: %+v", counts)
	}

	// Verify secrets in B are decryptable by cipherB (NOT cipherA!)
	gotUp, err := storeB.GetUpstreamByID(ctx, "up_1")
	if err != nil {
		t.Fatalf("GetUpstreamByID in B: %v", err)
	}
	plainCfgB, err := cipherB.DecryptString(gotUp.ConfigEncrypted)
	if err != nil || plainCfgB == "" {
		t.Fatalf("expected cipherB to decrypt upstream config, err=%v", err)
	}
	plainPassB, err := cipherB.DecryptString(gotUp.ProxyPasswordEncrypted)
	if err != nil || plainPassB != "proxy-secret-pass" {
		t.Fatalf("expected proxy password preserved, got %q, err=%v", plainPassB, err)
	}

	// Verify original API key works seamlessly in environment B
	gotClient, err := storeB.GetClientByKeyHash(ctx, crypto.HashAPIKey(plainKey))
	if err != nil || gotClient.ID != "cl_1" {
		t.Fatalf("expected original API key to resolve client in B, err=%v", err)
	}
}
