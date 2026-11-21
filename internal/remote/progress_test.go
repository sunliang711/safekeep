package remote

import (
	"context"
	"strings"
	"testing"
	"time"

	"safekeep/internal/db"
)

func TestRunProgressTracksDirectoriesAndUpstreams(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()
	if _, err := f.s.CheckHost(ctx, f.host.ID, true); err != nil {
		t.Fatal(err)
	}
	steps := make(chan string, 16)
	advance := make(chan struct{})
	t.Cleanup(func() { close(advance) })
	block := func(stage string) {
		steps <- stage
		select {
		case <-advance:
		case <-f.s.ctx.Done():
		}
	}
	f.ssh.mu.Lock()
	f.ssh.beforeCommand = func(command string) {
		if command == "uname -s" {
			block("checking")
		} else if strings.HasPrefix(command, "cd ") {
			block("archiving")
		}
	}
	f.ssh.mu.Unlock()
	for _, provider := range f.providers {
		provider.mu.Lock()
		provider.beforeUpload = func() { block("uploading") }
		provider.mu.Unlock()
	}
	// 占满并发槽位，确认立即执行的响应能准确表示排队状态。
	for i := 0; i < cap(f.s.slots); i++ {
		f.s.slots <- struct{}{}
	}
	j := f.job(t, db.RemoteDirectory{Path: makeDir(t, f.dir, "first")}, db.RemoteDirectory{Path: makeDir(t, f.dir, "second")})
	snapshot, err := f.s.Run(ctx, j.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Progress == nil || snapshot.Progress.Stage != "queued" {
		t.Fatalf("missing initial queued progress: %+v", snapshot.Progress)
	}
	for i := 0; i < cap(f.s.slots); i++ {
		<-f.s.slots
	}
	expectStep := func(stage string, directory, upstream int) *db.RemoteRun {
		t.Helper()
		select {
		case observed := <-steps:
			if observed != stage {
				t.Fatalf("expected %s, observed %s", stage, observed)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", stage)
		}
		run, err := f.store.GetRemoteRun(ctx, snapshot.ID)
		if err != nil {
			t.Fatal(err)
		}
		if run.Status != "running" || run.Progress == nil || run.Progress.Stage != stage || run.Progress.DirectoryIndex != directory || run.Progress.UpstreamIndex != upstream {
			t.Fatalf("wrong persisted progress: %+v", run)
		}
		return run
	}
	expectStep("checking", 0, 0)
	advance <- struct{}{}
	for directory := 1; directory <= 2; directory++ {
		expectStep("archiving", directory, 0)
		advance <- struct{}{}
		for upstream := 1; upstream <= 2; upstream++ {
			run := expectStep("uploading", directory, upstream)
			archive := run.Archives[directory-1]
			if archive.Size == 0 || archive.SHA256 == "" || archive.Uploads[upstream-1].Attempts != 1 {
				t.Fatalf("upload progress missing archive data: %+v", archive)
			}
			if upstream == 2 && archive.Uploads[0].Status != "succeeded" {
				t.Fatal("completed upload not visible when the next upstream starts")
			}
			if directory == 2 && run.Archives[0].Status != "succeeded" {
				t.Fatal("completed directory not visible when the next directory starts")
			}
			advance <- struct{}{}
		}
	}
	done := waitRun(t, f, snapshot.ID)
	if done.Status != "succeeded" || done.Progress == nil || done.Progress.Stage != "finished" || done.Progress.DirectoryIndex != 0 || done.Progress.UpstreamIndex != 0 || done.Progress.RetryAt != nil {
		t.Fatalf("completed progress still active: %+v", done)
	}
	if snapshot.Progress.Stage != "queued" {
		t.Fatal("background execution mutated the initial response snapshot")
	}
}

func TestRunProgressRetryWaitAndCancellation(t *testing.T) {
	f := setupFixture(t)
	ctx := context.Background()
	if _, err := f.s.CheckHost(ctx, f.host.ID, true); err != nil {
		t.Fatal(err)
	}
	settings, err := f.store.GetSystemSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	settings.MaxRetryAttempts = 2
	if err := f.store.UpdateSystemSettings(ctx, settings, "test"); err != nil {
		t.Fatal(err)
	}
	f.providers["one"].fail = true
	j := f.job(t, db.RemoteDirectory{Path: makeDir(t, f.dir, "retry")})
	snapshot, err := f.s.Run(ctx, j.ID, "manual")
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	var waiting *db.RemoteRun
	for time.Now().Before(deadline) {
		waiting, err = f.store.GetRemoteRun(ctx, snapshot.ID)
		if err != nil {
			t.Fatal(err)
		}
		if waiting.Progress != nil && waiting.Progress.Stage == "retry_wait" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if waiting.Progress == nil || waiting.Progress.Stage != "retry_wait" || waiting.Progress.DirectoryIndex != 1 || waiting.Progress.UpstreamIndex != 1 || waiting.Progress.RetryAt == nil {
		t.Fatalf("missing retry wait progress: %+v", waiting)
	}
	if !waiting.Progress.RetryAt.After(time.Now()) || time.Until(*waiting.Progress.RetryAt) > 20*time.Second || waiting.Archives[0].Uploads[0].Attempts != 1 {
		t.Fatalf("incorrect retry time or attempt count: %+v", waiting)
	}
	f.s.cancel()
	done := waitRun(t, f, snapshot.ID)
	if done.Status != "failed" || done.Progress.Stage != "finished" || done.Progress.RetryAt != nil {
		t.Fatalf("cancelled execution still waiting to retry: %+v", done)
	}
}
