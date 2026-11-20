package migration

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
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
	keyEncA, _ := cipherA.EncryptString(plainKey)
	whEncA, _ := cipherA.EncryptString("wh-secret")
	cl := &db.Client{
		ID:                     "cl_1",
		Name:                   "acme",
		KeyPrefix:              prefix,
		KeyHash:                hash,
		KeyEncrypted:           keyEncA,
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

	// Verify original API key works seamlessly in environment B and re-encrypted KeyEncrypted is decryptable by cipherB
	gotClient, err := storeB.GetClientByKeyHash(ctx, crypto.HashAPIKey(plainKey))
	if err != nil || gotClient.ID != "cl_1" {
		t.Fatalf("expected original API key to resolve client in B, err=%v", err)
	}
	plainKeyB, err := cipherB.DecryptString(gotClient.KeyEncrypted)
	if err != nil || plainKeyB != plainKey {
		t.Fatalf("expected re-encrypted api key in B, got %q err=%v", plainKeyB, err)
	}

	// Empty-passphrase export/inspect/import roundtrip + migration_backup cron secret re-encryption
	passEncA, _ := cipherA.EncryptString("cron-pass-123")
	apiKeyEncA, _ := cipherA.EncryptString("sk_cron_secret_123")
	cronJobsA, _ := storeA.ListCronJobs(ctx)
	for _, cj := range cronJobsA {
		if cj.JobKey == "migration_backup" {
			cfg := `{"api_base_url":"https://ext.example.com","client_id":"cl_1","api_key_encrypted":"` + apiKeyEncA + `","passphrase_encrypted":"` + passEncA + `","path":"mig","mode":"sync","include_admins":true}`
			if err := storeA.UpdateCronJob(ctx, cj.ID, "0 5 * * *", true, cfg); err != nil {
				t.Fatalf("UpdateCronJob: %v", err)
			}
		}
	}
	noPassBlob, err := migA.Export(ctx, ExportOptions{Passphrase: "", IncludeAdmins: true})
	if err != nil {
		t.Fatalf("Export with empty passphrase: %v", err)
	}
	noPassSummary, err := migB.Inspect(ctx, noPassBlob, "")
	if err != nil || noPassSummary.Counts.CronJobs < 6 {
		t.Fatalf("Inspect empty passphrase failed: err=%v summary=%+v", err, noPassSummary)
	}
	if _, err := migB.Import(ctx, noPassBlob, ImportOptions{Passphrase: "", Mode: "merge", ConflictStrategy: "overwrite", IncludeAdmins: true}); err != nil {
		t.Fatalf("Import empty passphrase: %v", err)
	}
	cronJobsB, _ := storeB.ListCronJobs(ctx)
	var checkedMB bool
	for _, cj := range cronJobsB {
		if cj.JobKey == "migration_backup" {
			checkedMB = true
			if strings.Contains(cj.ConfigJSON, "sk_cron_secret_123") || strings.Contains(cj.ConfigJSON, "cron-pass-123") {
				t.Fatalf("cron config_json must not store plaintext secrets: %s", cj.ConfigJSON)
			}
		}
	}
	if !checkedMB {
		t.Fatal("expected migration_backup cron job in storeB")
	}
}

func TestMigrationNoTruncationAndMergeOverwritePreservesTasks(t *testing.T) {
	ctx := context.Background()
	dirA := t.TempDir()
	storeA, err := db.Open(filepath.Join(dirA, "a.db"), dirA)
	if err != nil {
		t.Fatal(err)
	}
	defer storeA.Close()
	cipherA, _ := crypto.NewCipher(bytes.Repeat([]byte{0x11}, 32))

	_ = storeA.CreateAdmin(ctx, &db.Admin{ID: "adm_A", Username: "admin", PasswordHash: "hA", IsActive: true})
	cfgEncA, _ := cipherA.EncryptString(`{"bucket":"b1","access_key_id":"ak","secret_access_key":"sk"}`)
	_ = storeA.CreateUpstream(ctx, &db.Upstream{ID: "up_A", Name: "shared-s3", Type: "s3", Enabled: true, ConfigEncrypted: cfgEncA})
	_, p1, h1, _ := crypto.GenerateAPIKey()
	_ = storeA.CreateClient(ctx, &db.Client{ID: "cl_A", Name: "shared-client", KeyPrefix: p1, KeyHash: h1, Enabled: true, DefaultMode: "async"},
		[]db.ClientUpstream{{ClientID: "cl_A", UpstreamID: "up_A", IsDefault: true, Priority: 10}})

	// Create 30 tasks and 65 audit logs (> old 20 / 50 caps)
	for i := 0; i < 30; i++ {
		tid := crypto.NewULID()
		_ = storeA.CreateBackupTask(ctx, &db.BackupTask{
			ID: tid, ClientID: "cl_A", Mode: "sync", Status: "succeeded",
			OriginalFilename: "f.bin", ContentType: "application/octet-stream", FileSize: 10, SHA256: tid, SpoolCleaned: true,
		}, []db.BackupTaskItem{{
			ID: crypto.NewULID(), TaskID: tid, UpstreamID: "up_A", UpstreamName: "shared-s3", UpstreamType: "s3",
			RemotePath: "p/" + tid, Status: "succeeded", MaxAttempts: 3,
		}})
	}
	for i := 0; i < 65; i++ {
		_ = storeA.InsertAuditLog(ctx, &db.AuditLog{
			ActorType: "admin", ActorID: "adm_A", ActorName: "admin",
			Action: "test.action", ResourceType: "test", ResourceID: "r", Status: "success",
		})
	}

	migA := NewService(storeA, cipherA)
	bundle, err := migA.Export(ctx, ExportOptions{
		IncludeTasks:     true,
		IncludeAdmins:    true,
		IncludeAuditLogs: true,
		Passphrase:       "bundle-pass-99",
	})
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Target B already has different IDs with colliding names + its own task on shared-client
	dirB := t.TempDir()
	storeB, err := db.Open(filepath.Join(dirB, "b.db"), dirB)
	if err != nil {
		t.Fatal(err)
	}
	defer storeB.Close()
	cipherB, _ := crypto.NewCipher(bytes.Repeat([]byte{0x22}, 32))

	_ = storeB.CreateAdmin(ctx, &db.Admin{ID: "adm_B", Username: "admin", PasswordHash: "hB", IsActive: true})
	cfgEncB, _ := cipherB.EncryptString(`{"bucket":"b_old","access_key_id":"ak","secret_access_key":"sk"}`)
	_ = storeB.CreateUpstream(ctx, &db.Upstream{ID: "up_B", Name: "shared-s3", Type: "s3", Enabled: true, ConfigEncrypted: cfgEncB})
	_, p2, h2, _ := crypto.GenerateAPIKey()
	_ = storeB.CreateClient(ctx, &db.Client{ID: "cl_B", Name: "shared-client", KeyPrefix: p2, KeyHash: h2, Enabled: true, DefaultMode: "async"},
		[]db.ClientUpstream{{ClientID: "cl_B", UpstreamID: "up_B", IsDefault: true, Priority: 10}})
	_ = storeB.CreateBackupTask(ctx, &db.BackupTask{
		ID: "existing_task_B", ClientID: "cl_B", Mode: "sync", Status: "succeeded",
		OriginalFilename: "keep.bin", ContentType: "application/octet-stream", FileSize: 5, SHA256: "keephash", SpoolCleaned: true,
	}, nil)

	migB := NewService(storeB, cipherB)
	summary, err := migB.Inspect(ctx, bundle, "bundle-pass-99")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if summary.Counts.BackupTasks != 30 || summary.Counts.AuditLogs != 65 {
		t.Fatalf("expected 30 tasks and 65 audit logs (no truncation), got %+v", summary.Counts)
	}
	if len(summary.AdminConflicts) != 1 || len(summary.UpstreamConflicts) != 1 || len(summary.ClientConflicts) != 1 {
		t.Fatalf("expected conflicts detected, got %+v", summary)
	}

	counts, err := migB.Import(ctx, bundle, ImportOptions{
		Passphrase:       "bundle-pass-99",
		Mode:             "merge",
		ConflictStrategy: "overwrite",
		IncludeTasks:     true,
		IncludeAdmins:    true,
		IncludeAuditLogs: true,
	})
	if err != nil {
		t.Fatalf("Import merge/overwrite failed: %v", err)
	}
	if counts.BackupTasks != 30 || counts.AuditLogs != 65 || counts.Admins != 1 {
		t.Fatalf("unexpected counts: %+v", counts)
	}

	// Ensure pre-existing task in B was NOT cascade-deleted!
	if _, err := storeB.GetBackupTask(ctx, "existing_task_B"); err != nil {
		t.Fatalf("expected existing_task_B to survive merge/overwrite, got err=%v", err)
	}
}

