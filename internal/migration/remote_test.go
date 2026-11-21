package migration

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"safekeep/internal/db"
)

func TestRemoteMigrationReencryptsKeysAndRemapsReferences(t *testing.T) {
	ctx := context.Background()
	source, cipher, svc := proxyMigrationEnv(t, 0x31)
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "")
	if err != nil {
		t.Fatal(err)
	}
	plain := string(pem.EncodeToMemory(block))
	encrypted, err := cipher.EncryptString(plain)
	if err != nil {
		t.Fatal(err)
	}
	pub, _ := ssh.NewPublicKey(key.Public())
	h := &db.RemoteHost{ID: "source-host", Name: "vm001", Address: "backup.local", Port: 22, Username: "backup", PublicKey: string(ssh.MarshalAuthorizedKey(pub)), PrivateKeyEncrypted: encrypted}
	if err := source.SaveRemoteHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err := source.CreateUpstream(ctx, &db.Upstream{ID: "source-upstream", Name: "cloud", Type: "s3", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	j := &db.RemoteJob{ID: "source-job", Name: "daily", HostID: h.ID, UpstreamIDs: []string{"source-upstream"}, Directories: []db.RemoteDirectory{{Path: "/var/lib/my_data"}}, ParentPath: "vm001", CronExpr: "0 2 * * *", Timezone: "Asia/Singapore", TimeoutSec: 3600}
	if err := source.SaveRemoteJob(ctx, j); err != nil {
		t.Fatal(err)
	}
	r := &db.RemoteRun{ID: "run", JobID: j.ID, HostID: h.ID, StartedAt: time.Now().UTC(), Status: "running", Spools: map[string]string{"/var/lib/my_data": "/private/source.spool"}}
	if err := source.SaveRemoteRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	blob, err := svc.Export(ctx, ExportOptions{Passphrase: "test-passphrase", IncludeTasks: true})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(blob, []byte("PRIVATE KEY")) {
		t.Fatal("key leaked outside encrypted bundle")
	}
	for _, mode := range []string{"replace", "merge"} {
		t.Run(mode, func(t *testing.T) {
			target, targetCipher, targetSvc := proxyMigrationEnv(t, 0x32)
			if mode == "merge" {
				if err := target.SaveRemoteHost(ctx, &db.RemoteHost{ID: "target-host", Name: h.Name, PrivateKeyEncrypted: encrypted}); err != nil {
					t.Fatal(err)
				}
				if err := target.CreateUpstream(ctx, &db.Upstream{ID: "target-upstream", Name: "cloud", Type: "s3", Enabled: true}); err != nil {
					t.Fatal(err)
				}
			}
			counts, err := targetSvc.Import(ctx, blob, ImportOptions{Passphrase: "test-passphrase", Mode: mode, ConflictStrategy: "overwrite", IncludeTasks: true})
			if err != nil {
				t.Fatal(err)
			}
			if counts.RemoteHosts != 1 || counts.RemoteJobs != 1 || counts.RemoteRuns != 1 {
				t.Fatalf("unexpected counts: %+v", counts)
			}
			hostID, upstreamID := h.ID, "source-upstream"
			if mode == "merge" {
				hostID, upstreamID = "target-host", "target-upstream"
			}
			restored, err := target.GetRemoteHost(ctx, hostID)
			if err != nil {
				t.Fatal(err)
			}
			secret, err := targetCipher.DecryptString(restored.PrivateKeyEncrypted)
			if err != nil || secret != plain {
				t.Fatalf("key not reencrypted: %v", err)
			}
			job, err := target.GetRemoteJob(ctx, j.ID)
			if err != nil || job.HostID != hostID || job.UpstreamIDs[0] != upstreamID {
				t.Fatalf("references not remapped: %+v %v", job, err)
			}
			run, err := target.GetRemoteRun(ctx, r.ID)
			if err != nil || run.Status != "failed" || len(run.Spools) != 0 || run.HostID != hostID {
				t.Fatalf("bad restored run: %+v %v", run, err)
			}
		})
	}
}
