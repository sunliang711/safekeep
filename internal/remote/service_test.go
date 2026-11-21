package remote

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"safekeep/internal/audit"
	"safekeep/internal/backup"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/proxy"
	"safekeep/internal/storage"
)

// 测试 SSH 服务只接受本次生成的客户端公钥，在临时目录执行真实 tar 命令。
type sshFixture struct {
	listener    net.Listener
	config      *ssh.ServerConfig
	failArchive atomic.Bool
	missingTool atomic.Bool
	commands    atomic.Int32
	mu          sync.Mutex
	conns       []net.Conn
}

func startSSH(t *testing.T, publicKey string) *sshFixture {
	t.Helper()
	_, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(private)
	if err != nil {
		t.Fatal(err)
	}
	expected, _, _, _, err := ssh.ParseAuthorizedKey([]byte(publicKey))
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if !bytes.Equal(key.Marshal(), expected.Marshal()) {
			return nil, errors.New("unexpected key")
		}
		return nil, nil
	}}
	cfg.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &sshFixture{listener: listener, config: cfg}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			f.mu.Lock()
			f.conns = append(f.conns, conn)
			f.mu.Unlock()
			go f.serve(conn)
		}
	}()
	t.Cleanup(func() {
		listener.Close()
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.conns {
			c.Close()
		}
	})
	return f
}
func (f *sshFixture) serve(conn net.Conn) {
	server, channels, reqs, err := ssh.NewServerConn(conn, f.config)
	if err != nil {
		conn.Close()
		return
	}
	defer server.Close()
	go ssh.DiscardRequests(reqs)
	for ch := range channels {
		if ch.ChannelType() != "session" {
			ch.Reject(ssh.UnknownChannelType, "session only")
			continue
		}
		channel, requests, err := ch.Accept()
		if err != nil {
			continue
		}
		go func() {
			defer channel.Close()
			for req := range requests {
				if req.Type != "exec" {
					req.Reply(false, nil)
					continue
				}
				var payload struct{ Command string }
				if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
					return
				}
				req.Reply(true, nil)
				f.commands.Add(1)
				if f.missingTool.Load() && strings.HasPrefix(payload.Command, "tar -czf /dev/null") {
					channel.Stderr().Write([]byte("tar: not found"))
					channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{127}))
					return
				}
				if f.failArchive.Load() && strings.HasPrefix(payload.Command, "cd ") {
					channel.Write([]byte("partial archive"))
					channel.Stderr().Write([]byte("tar: read failed"))
					channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{2}))
					return
				}
				cmd := exec.Command("sh", "-c", payload.Command)
				cmd.Stdout = channel
				cmd.Stderr = channel.Stderr()
				status := uint32(0)
				if err := cmd.Run(); err != nil {
					status = 1
				}
				channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Code uint32 }{status}))
				return
			}
		}()
	}
}

type fakeProvider struct {
	mu    sync.Mutex
	files map[string][]byte
	calls int
	fail  bool
}

func (p *fakeProvider) Type() string { return "fake" }
func (p *fakeProvider) Upload(_ context.Context, key string, body io.ReadSeeker, _ int64, _ string) (*storage.UploadResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	if p.fail {
		return nil, errors.New("upstream unavailable")
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	p.files[key] = data
	return &storage.UploadResult{RemotePath: key}, nil
}
func (p *fakeProvider) Download(context.Context, string) (io.ReadCloser, int64, string, error) {
	return nil, 0, "", errors.New("unused")
}
func (p *fakeProvider) Delete(context.Context, string) error { return nil }
func (p *fakeProvider) TestConnection(context.Context) error { return nil }

type fixture struct {
	s         *Service
	store     *db.Store
	host      *db.RemoteHost
	ssh       *sshFixture
	providers map[string]*fakeProvider
	ids       []string
	dir       string
	cipher    *crypto.Cipher
}

func setupFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	store, err := db.Open(filepath.Join(dir, "test.db"), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	cipher, err := crypto.NewCipher(bytes.Repeat([]byte{7}, 32))
	if err != nil {
		t.Fatal(err)
	}
	registry := storage.NewRegistry(store, cipher)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	backups := backup.NewService(context.Background(), store, cipher, registry)
	s := NewService(context.Background(), store, cipher, registry, backups, audit.NewLogger(store), logger)
	f := &fixture{s: s, store: store, dir: dir, cipher: cipher, providers: map[string]*fakeProvider{}}
	registry.RegisterBuilder("fake", func(_ context.Context, u *db.Upstream, _ []byte, _ proxy.Config, _ func([]byte) error) (storage.Provider, error) {
		return f.providers[u.ID], nil
	})
	settings, err := store.GetSystemSettings(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	settings.MaxRetryAttempts = 1
	if err := store.UpdateSystemSettings(context.Background(), settings, "test"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		p := &fakeProvider{files: map[string][]byte{}}
		f.providers[id] = p
		f.ids = append(f.ids, id)
		u := &db.Upstream{ID: id, Name: id, Type: "fake", Enabled: true, ConfigEncrypted: ""}
		if err := store.CreateUpstream(context.Background(), u); err != nil {
			t.Fatal(err)
		}
	}
	h, err := s.SaveHost(context.Background(), "", "vm001", "127.0.0.1", "backup", 22)
	if err != nil {
		t.Fatal(err)
	}
	f.ssh = startSSH(t, h.PublicKey)
	port, _ := strconv.Atoi(strings.Split(f.ssh.listener.Addr().String(), ":")[1])
	h, err = s.SaveHost(context.Background(), h.ID, h.Name, h.Address, h.Username, port)
	if err != nil {
		t.Fatal(err)
	}
	f.host = h
	if err := s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return f
}
func (f *fixture) job(t *testing.T, dirs ...db.RemoteDirectory) *db.RemoteJob {
	t.Helper()
	j := &db.RemoteJob{Name: crypto.NewULID(), HostID: f.host.ID, Directories: dirs, UpstreamIDs: f.ids, ParentPath: "vm001", CronExpr: "0 2 * * *", Timezone: "Asia/Singapore", TimeoutSec: 30}
	if err := f.s.SaveJob(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	return j
}
func waitRun(t *testing.T, f *fixture, id string) *db.RemoteRun {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		run, err := f.store.GetRemoteRun(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "running" {
			return run
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("run did not finish")
	return nil
}
func makeDir(t *testing.T, base, name string) string {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, file := range []string{"keep.txt", "omit.log", "cache/data.txt"} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(file), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}
func TestRemoteBackupAndRetry(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()
	if _, err := f.s.CheckHost(ctx, f.host.ID, true); err != nil {
		t.Fatal(err)
	}
	dirs := []db.RemoteDirectory{{Path: makeDir(t, f.dir, "data !~'_$()"), Excludes: []string{"*.log", "./cache"}}, {Path: makeDir(t, f.dir, "other")}}
	f.providers["two"].fail = true
	j := f.job(t, dirs...)
	run, err := f.s.Run(ctx, j.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	done := waitRun(t, f, run.ID)
	if done.Status != "partial_success" {
		t.Fatalf("unexpected result: %+v", done)
	}
	if len(done.Spools) != 2 {
		t.Fatalf("failed uploads must retain archives: %+v", done.Spools)
	}
	for _, a := range done.Archives {
		if a.SHA256 == "" || a.Size == 0 {
			t.Fatalf("missing archive integrity data: %+v", a)
		}
		if !strings.HasPrefix(a.Uploads[0].RemotePath, "vm001/"+done.StartedAt.In(mustLocation(t, "Asia/Singapore")).Format("2006/01")+"/") {
			t.Fatal("wrong destination path")
		}
	}
	p := f.providers["one"]
	p.mu.Lock()
	for key, data := range p.files {
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		for {
			header, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(key, EncodePath(dirs[0].Path)) && (strings.Contains(header.Name, "cache") || strings.HasSuffix(header.Name, ".log")) {
				t.Fatalf("excluded file in archive: %s", header.Name)
			}
		}
		gz.Close()
	}
	calls := p.calls
	p.mu.Unlock()
	commands := f.ssh.commands.Load()
	f.providers["two"].mu.Lock()
	f.providers["two"].fail = false
	f.providers["two"].mu.Unlock()
	f.ssh.listener.Close()
	if _, err := f.s.Retry(ctx, run.ID); err != nil {
		t.Fatal(err)
	}
	done = waitRun(t, f, run.ID)
	if done.Status != "succeeded" {
		t.Fatalf("retry result: %+v", done)
	}
	if len(done.Spools) != 0 {
		t.Fatal("successful uploads must release archives")
	}
	if f.ssh.commands.Load() != commands {
		t.Fatal("upload retry reconnected to SSH")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.calls != calls {
		t.Fatal("retry uploaded successful targets again")
	}
}
func mustLocation(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestFailedArchiveNeverUploads(t *testing.T) {
	f := setupFixture(t)
	if _, err := f.s.CheckHost(context.Background(), f.host.ID, true); err != nil {
		t.Fatal(err)
	}
	f.ssh.failArchive.Store(true)
	j := f.job(t, db.RemoteDirectory{Path: makeDir(t, f.dir, "data")})
	r, err := f.s.Run(context.Background(), j.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	done := waitRun(t, f, r.ID)
	if done.Status != "failed" || done.Archives[0].SHA256 != "" {
		t.Fatalf("bad archive accepted: %+v", done)
	}
	for _, p := range f.providers {
		p.mu.Lock()
		calls := p.calls
		p.mu.Unlock()
		if calls != 0 {
			t.Fatal("partial archive uploaded")
		}
	}
}

func TestMissingToolAndChangedHostKey(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()
	f.ssh.missingTool.Store(true)
	h, err := f.s.CheckHost(ctx, f.host.ID, true)
	if err == nil || h.HealthStatus != "failed" || h.HostKey == "" {
		t.Fatalf("missing tool not reflected: %+v %v", h, err)
	}
	f.ssh.missingTool.Store(false)
	if _, err := f.s.CheckHost(ctx, f.host.ID, false); err != nil {
		t.Fatal(err)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	pub, _ := ssh.NewPublicKey(key.Public())
	h.HostKey = string(ssh.MarshalAuthorizedKey(pub))
	if err := f.store.SaveRemoteHost(ctx, h); err != nil {
		t.Fatal(err)
	}
	h, err = f.s.CheckHost(ctx, h.ID, true)
	if err == nil || h.HealthStatus != "failed" {
		t.Fatal("changed host key was accepted")
	}
}

func TestJobValidationAndRestartRecovery(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()
	j := f.job(t, db.RemoteDirectory{Path: "/var/lib/mysql"})
	for _, mutate := range []func(*db.RemoteJob){func(j *db.RemoteJob) { j.ParentPath = "../bad" }, func(j *db.RemoteJob) { j.CronExpr = "bad" }, func(j *db.RemoteJob) { j.Timezone = "invalid" }, func(j *db.RemoteJob) { j.Directories[0].Path = "relative" }, func(j *db.RemoteJob) { j.UpstreamIDs = []string{"missing"} }} {
		copy := *j
		copy.Directories = append([]db.RemoteDirectory(nil), j.Directories...)
		mutate(&copy)
		if err := f.s.SaveJob(ctx, &copy); !errors.Is(err, ErrInvalid) {
			t.Fatalf("bad configuration accepted: %v", err)
		}
	}
	r := &db.RemoteRun{ID: crypto.NewULID(), JobID: j.ID, Status: "running", StartedAt: time.Now(), Archives: []db.RemoteArchive{{SourcePath: "/var/lib/mysql", Status: "running"}}, Spools: map[string]string{}}
	if err := f.store.SaveRemoteRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	other := NewService(ctx, f.store, f.cipher, f.s.registry, f.s.backup, nil, f.s.logger)
	if err := other.Start(); err != nil {
		t.Fatal(err)
	}
	defer other.Stop()
	got, err := f.store.GetRemoteRun(ctx, r.ID)
	if err != nil || got.Status != "failed" || got.FinishedAt == nil {
		t.Fatalf("interrupted run recovery failed: %+v %v", got, err)
	}
	if err := f.s.DeleteJob(ctx, j.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.GetRemoteRun(ctx, r.ID); err != nil {
		t.Fatal("deleting job removed history")
	}
}

func TestPrivateKeyIsEncrypted(t *testing.T) {
	f := setupFixture(t)
	if strings.Contains(f.host.PrivateKeyEncrypted, "PRIVATE KEY") {
		t.Fatal("private key persisted without encryption")
	}
	plain, err := f.cipher.DecryptString(f.host.PrivateKeyEncrypted)
	if err != nil || !strings.Contains(plain, "PRIVATE KEY") {
		t.Fatal("private key unavailable")
	}
	var count int
	if err := f.store.DB().QueryRow(`SELECT count(*) FROM remote_hosts WHERE data_json LIKE '%PRIVATE KEY%'`).Scan(&count); err != nil || count != 0 {
		t.Fatal("private key leaked in host JSON")
	}
}

func TestDirectoryFailureDoesNotStopOtherDirectories(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()
	if _, err := f.s.CheckHost(ctx, f.host.ID, true); err != nil {
		t.Fatal(err)
	}
	j := f.job(t, db.RemoteDirectory{Path: filepath.Join(f.dir, "missing")}, db.RemoteDirectory{Path: makeDir(t, f.dir, "readable")})
	run, err := f.s.Run(ctx, j.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	done := waitRun(t, f, run.ID)
	if done.Status != "partial_success" || done.Archives[0].Status != "failed" || done.Archives[1].Status != "succeeded" {
		t.Fatalf("directory isolation failed: %+v", done)
	}
	for _, u := range done.Archives[0].Uploads {
		if u.Status != "failed" {
			t.Fatal("unstarted upload did not finish as failed")
		}
	}
}

func TestHandshakeHonorsCancellation(t *testing.T) {
	f := setupFixture(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		conn, err := listener.Accept()
		if err == nil {
			accepted <- conn
		}
	}()
	h := *f.host
	h.Port = listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, _, err := f.s.connect(ctx, &h, true); err == nil {
		t.Fatal("expected canceled SSH handshake")
	}
	if time.Since(started) > 2*time.Second {
		t.Fatal("SSH handshake ignored cancellation")
	}
	select {
	case conn := <-accepted:
		conn.Close()
	default:
	}
}

func TestBufferedArchiveCleanupPreservesActiveRuns(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()
	j := f.job(t, db.RemoteDirectory{Path: "/data"})
	settings, err := f.store.GetSystemSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(settings.SpoolDir, "remote")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-73 * time.Hour)
	spool := filepath.Join(dir, "old.spool")
	orphan := filepath.Join(dir, "orphan.spool")
	for _, name := range []string{spool, orphan} {
		if err := os.WriteFile(name, []byte("archive"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(name, old, old); err != nil {
			t.Fatal(err)
		}
	}
	r := &db.RemoteRun{ID: crypto.NewULID(), JobID: j.ID, Status: "failed", StartedAt: old, Spools: map[string]string{"/data": spool}, Archives: []db.RemoteArchive{{SourcePath: "/data", Status: "failed"}}}
	if err := f.store.SaveRemoteRun(ctx, r); err != nil {
		t.Fatal(err)
	}
	f.s.mu.Lock()
	f.s.active[j.ID] = true
	f.s.mu.Unlock()
	if err := f.s.cleanupSpools(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spool); err != nil {
		t.Fatal("active archive was removed")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Fatal("orphan archive was not removed")
	}
	f.s.mu.Lock()
	delete(f.s.active, j.ID)
	f.s.mu.Unlock()
	if err := f.s.cleanupSpools(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spool); !os.IsNotExist(err) {
		t.Fatal("expired archive was not removed")
	}
	updated, err := f.store.GetRemoteRun(ctx, r.ID)
	if err != nil || len(updated.Spools) != 0 {
		t.Fatal("cleanup did not update run")
	}
}
