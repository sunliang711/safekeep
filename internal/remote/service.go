package remote

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"path"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/robfig/cron/v3"
	"golang.org/x/crypto/ssh"
	"safekeep/internal/audit"
	"safekeep/internal/backup"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/storage"
)

var ErrBusy = errors.New("该任务正在执行，请等待本次执行结束")
var ErrInvalid = errors.New("远程备份配置无效")

const defaultConcurrency = 4
const maxTaskTimeout = 86400
const healthInterval = "@every 30m"

type Service struct {
	store    *db.Store
	cipher   *crypto.Cipher
	registry *storage.Registry
	backup   *backup.Service
	audit    *audit.Logger
	logger   *slog.Logger
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	configMu sync.Mutex
	cron     *cron.Cron
	entries  []cron.EntryID
	active   map[string]bool
	closed   bool
	wg       sync.WaitGroup
	slots    chan struct{}
}

func NewService(ctx context.Context, store *db.Store, cipher *crypto.Cipher, registry *storage.Registry, backups *backup.Service, audits *audit.Logger, logger *slog.Logger) *Service {
	ctx, cancel := context.WithCancel(ctx)
	return &Service{store: store, cipher: cipher, registry: registry, backup: backups, audit: audits, logger: logger, ctx: ctx, cancel: cancel, active: make(map[string]bool), slots: make(chan struct{}, defaultConcurrency)}
}

func (s *Service) Start() error {
	settings, err := s.store.GetSystemSettings(s.ctx)
	if err != nil {
		return err
	}
	s.slots = make(chan struct{}, max(1, settings.WorkerConcurrency))
	interrupted, err := s.store.ListInterruptedRemoteRuns(s.ctx)
	if err != nil {
		return err
	}
	for i := range interrupted {
		r := &interrupted[i]
		r.Status = "failed"
		r.Error = "服务重启中断了本次执行，可重试失败项"
		r.Progress = &db.RemoteRunProgress{Stage: "finished"}
		now := time.Now().UTC()
		r.FinishedAt = &now
		for j := range r.Archives {
			a := &r.Archives[j]
			if a.Status == "running" || a.Status == "pending" {
				a.Status = "failed"
				a.Error = r.Error
			}
		}
		if err := s.store.SaveRemoteRun(s.ctx, r); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.cron = cron.New()
	s.mu.Unlock()
	if err := s.Reload(s.ctx); err != nil {
		return err
	}
	_, err = s.cron.AddFunc(healthInterval, func() { s.backgroundHealth() })
	if err != nil {
		return err
	}
	s.cron.Start()
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	s.closed = true
	c := s.cron
	s.cancel()
	s.mu.Unlock()
	if c != nil {
		<-c.Stop().Done()
	}
	s.wg.Wait()
}

func (s *Service) Reload(ctx context.Context) error {
	jobs, err := s.store.ListRemoteJobs(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cron == nil {
		return nil
	}
	for _, id := range s.entries {
		s.cron.Remove(id)
	}
	s.entries = nil
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		jobID := j.ID
		id, err := s.cron.AddFunc("CRON_TZ="+j.Timezone+" "+j.CronExpr, func() {
			if _, err := s.Run(s.ctx, jobID, "schedule"); err != nil {
				s.logger.Error("remote backup schedule failed", "job_id", jobID, "err", err)
			}
		})
		if err != nil {
			return err
		}
		s.entries = append(s.entries, id)
	}
	return nil
}

func (s *Service) backgroundHealth() {
	s.mu.Lock()
	if s.closed || s.active["health"] {
		s.mu.Unlock()
		return
	}
	s.active["health"] = true
	s.wg.Add(1)
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.active, "health"); s.mu.Unlock(); s.wg.Done() }()
	hosts, err := s.store.ListRemoteHosts(s.ctx)
	if err != nil {
		s.logger.Error("list remote hosts failed", "err", err)
		return
	}
	for _, h := range hosts {
		if h.HostKey == "" {
			continue
		}
		if _, err := s.CheckHost(s.ctx, h.ID, false); err != nil {
			s.logger.Warn("remote host health check failed", "host_id", h.ID, "err", err)
		}
	}
	if err := s.cleanupSpools(s.ctx); err != nil {
		s.logger.Warn("remote spool cleanup failed", "err", err)
	}
}

func invalid(message string) error { return fmt.Errorf("%w: %s", ErrInvalid, message) }

func (s *Service) SaveHost(ctx context.Context, id, name, address, username string, port int) (*db.RemoteHost, error) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	name, address, username = strings.TrimSpace(name), strings.TrimSpace(address), strings.TrimSpace(username)
	if name == "" || len(name) > 100 || address == "" || username == "" || strings.ContainsFunc(name+address+username, unicode.IsControl) {
		return nil, invalid("主机名称、地址和登录用户不能为空或包含控制字符")
	}
	if strings.ContainsAny(address, "/\\ @") {
		return nil, invalid("请填写主机名或 IP，不要包含协议和端口")
	}
	if strings.Contains(address, ":") && net.ParseIP(address) == nil {
		return nil, invalid("IPv6 地址请勿加方括号或端口")
	}
	if port < 1 || port > 65535 {
		return nil, invalid("SSH 端口必须在 1–65535 之间")
	}
	now := time.Now().UTC()
	h := &db.RemoteHost{ID: id, CreatedAt: now, HealthStatus: "untested"}
	if id != "" {
		old, err := s.store.GetRemoteHost(ctx, id)
		if err != nil {
			return nil, err
		}
		h = old
		if h.Address != address || h.Port != port || h.Username != username {
			h.HostKey = ""
			h.Fingerprint = ""
			h.Platform = ""
			h.ArchiveTool = ""
			h.HealthStatus = "untested"
			h.HealthError = ""
			h.CheckedAt = nil
		}
	} else {
		h.ID = crypto.NewULID()
		private, public, err := generateKey()
		if err != nil {
			return nil, err
		}
		encrypted, err := s.cipher.EncryptString(private)
		if err != nil {
			return nil, err
		}
		h.PrivateKeyEncrypted = encrypted
		h.PublicKey = public + " safekeep-" + h.ID
	}
	h.Name, h.Address, h.Username, h.Port, h.UpdatedAt = name, address, username, port, now
	if err := s.store.SaveRemoteHost(ctx, h); err != nil {
		return nil, err
	}
	return h, nil
}

func (s *Service) CheckHost(ctx context.Context, id string, enroll bool) (*db.RemoteHost, error) {
	// 与配置修改串行，避免健康检测把检测期间的新配置覆盖掉。
	s.configMu.Lock()
	defer s.configMu.Unlock()
	h, err := s.store.GetRemoteHost(ctx, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	c, observed, checkErr := s.connect(ctx, h, enroll)
	h.Platform, h.ArchiveTool = "", ""
	if checkErr == nil {
		if h.HostKey == "" {
			h.HostKey = observed
			pub, _, _, _, parseErr := ssh.ParseAuthorizedKey([]byte(observed))
			if parseErr != nil {
				c.Close()
				return nil, parseErr
			}
			h.Fingerprint = ssh.FingerprintSHA256(pub)
		}
		h.Platform, h.ArchiveTool, checkErr = detectPlatform(c)
		c.Close()
	}
	now := time.Now().UTC()
	h.CheckedAt = &now
	h.HealthStatus = "ok"
	h.HealthError = ""
	if checkErr != nil {
		h.HealthStatus = "failed"
		h.HealthError = checkErr.Error()
	}
	if err := s.store.SaveRemoteHost(context.WithoutCancel(ctx), h); err != nil {
		return nil, err
	}
	return h, checkErr
}

func (s *Service) SaveJob(ctx context.Context, j *db.RemoteJob) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	j.Name, j.ParentPath, j.CronExpr, j.Timezone = strings.TrimSpace(j.Name), strings.TrimSpace(j.ParentPath), strings.TrimSpace(j.CronExpr), strings.TrimSpace(j.Timezone)
	if j.Name == "" || len(j.Name) > 100 {
		return invalid("任务名称不能为空且不能超过 100 字节")
	}
	if _, err := s.store.GetRemoteHost(ctx, j.HostID); err != nil {
		return invalid("请选择有效的远程主机")
	}
	if len(j.Directories) < 1 || len(j.Directories) > 100 {
		return invalid("请配置 1–100 个源目录")
	}
	seen := map[string]bool{}
	for i := range j.Directories {
		d := &j.Directories[i]
		if d.Excludes == nil {
			d.Excludes = []string{}
		}
		if err := validateDirectory(d.Path); err != nil {
			return invalid(err.Error())
		}
		if seen[d.Path] {
			return invalid("源目录不能重复")
		}
		seen[d.Path] = true
		if len(d.Excludes) > 100 {
			return invalid("每个目录最多配置 100 条排除规则")
		}
		for _, rule := range d.Excludes {
			if rule == "" || len(rule) > 1000 || strings.ContainsFunc(rule, unicode.IsControl) {
				return invalid("排除规则不能为空、过长或包含控制字符")
			}
		}
	}
	if err := validateParent(j.ParentPath); err != nil {
		return invalid(err.Error())
	}
	if j.Timezone == "" {
		return invalid("请选择任务时区")
	}
	if _, err := time.LoadLocation(j.Timezone); err != nil {
		return invalid("任务时区无效")
	}
	if _, err := cron.ParseStandard(j.CronExpr); err != nil {
		return invalid("Cron 表达式无效")
	}
	if j.TimeoutSec < 1 || j.TimeoutSec > maxTaskTimeout {
		return invalid("任务超时必须在 1–86400 秒之间")
	}
	if len(j.UpstreamIDs) < 1 || len(j.UpstreamIDs) > 100 {
		return invalid("请选择 1–100 个目标上游")
	}
	seen = map[string]bool{}
	for _, id := range j.UpstreamIDs {
		u, err := s.store.GetUpstreamByID(ctx, id)
		if err != nil || !u.Enabled {
			return invalid("目标上游不存在或未启用")
		}
		if seen[id] {
			return invalid("目标上游不能重复")
		}
		seen[id] = true
	}
	now := time.Now().UTC()
	if j.ID == "" {
		j.ID = crypto.NewULID()
		j.CreatedAt = now
	} else {
		old, err := s.store.GetRemoteJob(ctx, j.ID)
		if err != nil {
			return err
		}
		j.CreatedAt = old.CreatedAt
	}
	j.UpdatedAt = now
	if err := s.store.SaveRemoteJob(ctx, j); err != nil {
		return err
	}
	return s.Reload(ctx)
}

func (s *Service) Run(ctx context.Context, jobID, trigger string) (*db.RemoteRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	j, err := s.store.GetRemoteJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if s.closed {
		return nil, errors.New("服务正在关闭")
	}
	if s.active[jobID] {
		return nil, ErrBusy
	}
	if trigger == "schedule" && !j.Enabled {
		return nil, invalid("任务的定时执行已关闭")
	}
	r := &db.RemoteRun{ID: crypto.NewULID(), JobID: j.ID, JobName: j.Name, HostID: j.HostID, Trigger: trigger, Status: "running", StartedAt: time.Now().UTC(), Spools: map[string]string{}, Archives: make([]db.RemoteArchive, len(j.Directories))}
	r.Progress = &db.RemoteRunProgress{Stage: "queued"}
	loc, err := time.LoadLocation(j.Timezone)
	if err != nil {
		return nil, err
	}
	at := r.StartedAt.In(loc)
	for i, d := range j.Directories {
		filename := EncodePath(d.Path) + "_" + r.ID + ".tar.gz"
		a := db.RemoteArchive{SourcePath: d.Path, Filename: filename, Status: "pending", Uploads: []db.RemoteUpload{}}
		for _, id := range j.UpstreamIDs {
			u, err := s.store.GetUpstreamByID(ctx, id)
			if err != nil {
				return nil, err
			}
			a.Uploads = append(a.Uploads, db.RemoteUpload{UpstreamID: id, UpstreamName: u.Name, RemotePath: path.Join(j.ParentPath, at.Format("2006/01"), filename), Status: "pending"})
		}
		r.Archives[i] = a
	}
	if err := s.store.SaveRemoteRun(ctx, r); err != nil {
		return nil, err
	}
	// 返回独立快照，后台写入不会与 HTTP JSON 编码并发访问同一结构体。
	snapshot, err := s.store.GetRemoteRun(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	s.active[jobID] = true
	s.wg.Add(1)
	go s.execute(j, r, false)
	return snapshot, nil
}

func (s *Service) Retry(ctx context.Context, runID string) (*db.RemoteRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, errors.New("服务正在关闭")
	}
	r, err := s.store.GetRemoteRun(ctx, runID)
	if err != nil {
		return nil, err
	}
	if s.active[r.JobID] || r.Status == "running" {
		return nil, ErrBusy
	}
	if r.Status == "succeeded" {
		return nil, invalid("该执行已全部成功")
	}
	j, err := s.store.GetRemoteJob(ctx, r.JobID)
	if err != nil {
		return nil, err
	}
	// 上传重试只使用原执行的归档、目标和对象路径，不受任务配置修改影响。
	r.Status = "running"
	r.Error = ""
	r.Progress = &db.RemoteRunProgress{Stage: "queued"}
	r.FinishedAt = nil
	if err := s.store.SaveRemoteRun(ctx, r); err != nil {
		return nil, err
	}
	snapshot, err := s.store.GetRemoteRun(ctx, r.ID)
	if err != nil {
		return nil, err
	}
	s.active[j.ID] = true
	s.wg.Add(1)
	go s.execute(j, r, true)
	return snapshot, nil
}

func (s *Service) persist(r *db.RemoteRun) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(s.ctx), 5*time.Second)
	defer cancel()
	if err := s.store.SaveRemoteRun(ctx, r); err != nil {
		s.logger.Error("save remote backup run failed", "run_id", r.ID, "err", err)
	}
}

func (s *Service) DeleteJob(ctx context.Context, id string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.mu.Lock()
	if s.active[id] {
		s.mu.Unlock()
		return ErrBusy
	}
	err := s.store.DeleteRemoteJob(ctx, id)
	s.mu.Unlock()
	if err != nil {
		return err
	}
	// 删除调度入口；已完成的执行记录保留，方便查询历史备份位置。
	return s.Reload(ctx)
}

func (s *Service) DeleteHost(ctx context.Context, id string) error {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.store.DeleteRemoteHost(ctx, id)
}
