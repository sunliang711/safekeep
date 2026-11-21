package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"safekeep/internal/audit"
	"safekeep/internal/db"
)

func (s *Service) execute(j *db.RemoteJob, r *db.RemoteRun, retry bool) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, time.Duration(j.TimeoutSec)*time.Second)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			s.logger.Error("remote backup panic", "run_id", r.ID, "panic", recovered)
			r.Error = "执行发生内部异常"
		}
		succeeded, failed := 0, 0
		for i := range r.Archives {
			a := &r.Archives[i]
			ok := 0
			for index := range a.Uploads {
				u := &a.Uploads[index]
				if u.Status == "succeeded" {
					ok++
					succeeded++
				} else {
					failed++
					u.Status = "failed"
					if u.Error == "" {
						u.Error = a.Error
						if u.Error == "" {
							u.Error = "归档或上传尚未完成"
						}
					}
				}
			}
			switch {
			case ok == len(a.Uploads) && ok > 0:
				a.Status = "succeeded"
				a.Error = ""
			case ok > 0:
				a.Status = "partial_success"
			default:
				a.Status = "failed"
			}
			if a.Status == "succeeded" {
				s.removeSpool(r, a.SourcePath)
			}
		}
		if ctx.Err() != nil {
			r.Error = "执行被取消或超过任务超时时间"
		}
		r.Status = "succeeded"
		if failed > 0 || r.Error != "" {
			r.Status = "failed"
			if succeeded > 0 {
				r.Status = "partial_success"
			}
		}
		now := time.Now().UTC()
		r.FinishedAt = &now
		r.Progress = &db.RemoteRunProgress{Stage: "finished"}
		s.mu.Lock()
		s.persist(r)
		delete(s.active, j.ID)
		s.mu.Unlock()
		if s.audit != nil {
			status := "success"
			if r.Status != "succeeded" {
				status = "failure"
			}
			s.audit.Record(context.WithoutCancel(s.ctx), audit.Event{ActorType: "system", ActorID: "remote-backup", ActorName: "remote-backup", Action: "remote_backup.execute", ResourceType: "remote_backup_run", ResourceID: r.ID, Status: status, Detail: map[string]any{"job_id": j.ID, "status": r.Status, "trigger": r.Trigger}})
		}
	}()
	select {
	case s.slots <- struct{}{}:
		defer func() { <-s.slots }()
	case <-ctx.Done():
		r.Error = "等待执行时任务已取消或超时"
		return
	}
	var h *db.RemoteHost
	if !retry {
		var err error
		s.updateProgress(r, "checking", 0, 0)
		h, err = s.CheckHost(ctx, r.HostID, false)
		if err != nil {
			r.Error = err.Error()
			for i := range r.Archives {
				r.Archives[i].Error = r.Error
			}
			return
		}
	}
	for i := range r.Archives {
		a := &r.Archives[i]
		if a.Status == "succeeded" {
			continue
		}
		if ctx.Err() != nil {
			a.Error = "任务已取消或超时"
			continue
		}
		if retry {
			a.Status = "running"
			a.Error = ""
			s.updateProgress(r, "verifying", i+1, 0)
			if r.Spools[a.SourcePath] == "" {
				a.Error = "归档不可用，请重新执行备份任务"
				continue
			}
			if err := verifySpool(r.Spools[a.SourcePath], a); err != nil {
				a.Error = err.Error()
				continue
			}
		} else {
			a.Status = "running"
			// 远程打包与 SSH 拉取使用同一条字节流，合并为一个进度步骤。
			s.updateProgress(r, "archiving", i+1, 0)
			if err := s.pullArchive(ctx, h, j.Directories[i], r, a); err != nil {
				a.Status = "failed"
				a.Error = err.Error()
				s.persist(r)
				continue
			}
		}
		a.Status = "running"
		a.Error = ""
		s.uploadArchive(ctx, r, a, i+1)
		allSucceeded := true
		for _, upload := range a.Uploads {
			allSucceeded = allSucceeded && upload.Status == "succeeded"
		}
		if allSucceeded {
			a.Status = "succeeded"
			s.removeSpool(r, a.SourcePath)
		}
		s.persist(r)
	}
}

func (s *Service) updateProgress(r *db.RemoteRun, stage string, directoryIndex, upstreamIndex int) {
	r.Progress = &db.RemoteRunProgress{Stage: stage, DirectoryIndex: directoryIndex, UpstreamIndex: upstreamIndex}
	s.persist(r)
}

func (s *Service) pullArchive(ctx context.Context, h *db.RemoteHost, d db.RemoteDirectory, r *db.RemoteRun, a *db.RemoteArchive) error {
	c, _, err := s.connect(ctx, h, false)
	if err != nil {
		return err
	}
	defer c.Close()
	reader, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := runCommand(c, archiveCommand(h.Platform, d), writer)
		writer.CloseWithError(err)
	}()
	spool, size, sha, spoolErr := s.backup.SpoolStream(ctx, nil, a.Filename, reader)
	reader.Close()
	if spoolErr != nil {
		c.Close()
	}
	<-done
	if spoolErr != nil {
		return fmt.Errorf("打包或拉取失败: %w", spoolErr)
	}
	// 归档只在首次拉取时计入流量；后续上传重试复用同一个配额预留。
	if err := s.backup.ReserveRemoteTraffic(ctx, a.Filename, size); err != nil {
		os.Remove(spool)
		return err
	}
	targetDir := filepath.Join(filepath.Dir(spool), "remote")
	if err := os.MkdirAll(targetDir, 0o750); err != nil {
		os.Remove(spool)
		return err
	}
	target := filepath.Join(targetDir, filepath.Base(spool))
	if err := os.Rename(spool, target); err != nil {
		os.Remove(spool)
		return err
	}
	a.Size, a.SHA256 = size, sha
	r.Spools[a.SourcePath] = target
	s.persist(r)
	return nil
}

func (s *Service) uploadArchive(ctx context.Context, r *db.RemoteRun, a *db.RemoteArchive, directoryIndex int) {
	settings, err := s.store.GetSystemSettings(ctx)
	if err != nil {
		a.Error = "无法读取上传配置"
		return
	}
	attempts := max(1, settings.MaxRetryAttempts)
	for i := range a.Uploads {
		u := &a.Uploads[i]
		if u.Status == "succeeded" {
			continue
		}
		for n := 0; n < attempts; n++ {
			if ctx.Err() != nil {
				u.Status = "failed"
				u.Error = "上传已取消或超时"
				break
			}
			u.Status = "running"
			u.Attempts++
			s.updateProgress(r, "uploading", directoryIndex, i+1)
			err := s.uploadOnce(ctx, r.Spools[a.SourcePath], a, u)
			if err == nil {
				u.Status = "succeeded"
				u.Error = ""
				break
			}
			u.Status = "failed"
			u.Error = err.Error()
			s.persist(r)
			if n+1 < attempts {
				delay := time.Duration(15*(1<<min(n, 4))) * time.Second
				retryAt := time.Now().Add(delay).UTC()
				r.Progress = &db.RemoteRunProgress{Stage: "retry_wait", DirectoryIndex: directoryIndex, UpstreamIndex: i + 1, RetryAt: &retryAt}
				s.persist(r)
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
				}
			}
		}
	}
}

func (s *Service) uploadOnce(ctx context.Context, spool string, a *db.RemoteArchive, u *db.RemoteUpload) error {
	provider, _, err := s.registry.Get(ctx, u.UpstreamID)
	if err != nil {
		return err
	}
	f, err := os.Open(spool)
	if err != nil {
		return errors.New("本地归档不可用，请重新执行备份任务")
	}
	defer f.Close()
	res, err := provider.Upload(ctx, u.RemotePath, f, a.Size, "application/gzip")
	if err != nil {
		return err
	}
	if res == nil {
		return errors.New("上游没有返回上传结果")
	}
	if res.RemotePath != "" {
		u.RemotePath = res.RemotePath
	}
	return nil
}

func (s *Service) removeSpool(r *db.RemoteRun, source string) {
	spool := r.Spools[source]
	if spool == "" {
		return
	}
	if err := os.Remove(spool); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("remove remote spool failed", "run_id", r.ID, "err", err)
		return
	}
	delete(r.Spools, source)
}

func (s *Service) cleanupSpools(ctx context.Context) error {
	runs, err := s.store.ListRemoteBufferedRuns(ctx, time.Now().Add(-72*time.Hour))
	if err != nil {
		return err
	}
	for i := range runs {
		r := &runs[i]
		s.mu.Lock()
		if s.active[r.JobID] {
			s.mu.Unlock()
			continue
		}
		// 持锁重读并清理，防止覆盖刚完成的重试或删除正在重试的归档。
		r, err = s.store.GetRemoteRun(ctx, r.ID)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		for source := range r.Spools {
			s.removeSpool(r, source)
		}
		s.persist(r)
		s.mu.Unlock()
	}
	// 回收崩溃窗口中尚未来得及写入执行记录的孤儿归档。
	paths, err := s.store.RemoteSpoolPaths(ctx)
	if err != nil {
		return err
	}
	settings, err := s.store.GetSystemSettings(ctx)
	if err != nil {
		return err
	}
	dir := filepath.Join(settings.SpoolDir, "remote")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-72 * time.Hour)
	for _, entry := range entries {
		name := filepath.Join(dir, entry.Name())
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".spool") || paths[name] {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}
