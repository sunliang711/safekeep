package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"safekeep/internal/audit"
	"safekeep/internal/db"
	"safekeep/internal/remote"
)

type remoteHostDTO db.RemoteHost
type remoteJobDTO db.RemoteJob
type remoteRunDTO db.RemoteRun

type remoteHostRequest struct {
	Name     string `json:"name"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	Username string `json:"username"`
}

func (s *Server) SetRemoteService(service *remote.Service) { s.remoteSvc = service }

func (s *Server) registerRemoteRoutes(r chi.Router) {
	r.Route("/remote-backups", func(r chi.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if s.remoteSvc == nil {
					writeError(w, http.StatusServiceUnavailable, "remote_unavailable", "远程备份服务未启动")
					return
				}
				next.ServeHTTP(w, r)
			})
		})
		r.Get("/hosts", s.handleListRemoteHosts)
		r.Post("/hosts", s.handleSaveRemoteHost)
		r.Put("/hosts/{id}", s.handleSaveRemoteHost)
		r.Delete("/hosts/{id}", s.handleDeleteRemoteHost)
		r.Post("/hosts/{id}/check", s.handleCheckRemoteHost)
		r.Get("/jobs", s.handleListRemoteJobs)
		r.Post("/jobs", s.handleSaveRemoteJob)
		r.Put("/jobs/{id}", s.handleSaveRemoteJob)
		r.Delete("/jobs/{id}", s.handleDeleteRemoteJob)
		r.Post("/jobs/{id}/run", s.handleRunRemoteJob)
		r.Get("/runs", s.handleListRemoteRuns)
		r.Get("/runs/{id}", s.handleGetRemoteRun)
		r.Post("/runs/{id}/retry", s.handleRetryRemoteRun)
	})
}

func (s *Server) remoteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "远程主机、任务或执行记录不存在")
	case errors.Is(err, remote.ErrBusy):
		writeError(w, http.StatusConflict, "remote_busy", err.Error())
	case errors.Is(err, remote.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_remote_config", err.Error())
	case strings.Contains(err.Error(), "UNIQUE constraint"):
		writeError(w, http.StatusConflict, "remote_conflict", "名称已存在或任务正在执行")
	case strings.Contains(err.Error(), "FOREIGN KEY constraint"):
		writeError(w, http.StatusConflict, "remote_in_use", "配置仍被备份任务引用，请先删除关联任务")
	default:
		s.logger.Error("remote backup operation failed", "err", err)
		writeError(w, http.StatusInternalServerError, "remote_failed", "远程备份操作失败")
	}
}

func (s *Server) remoteAudit(r *http.Request, action, resource, id, status string) {
	admin := currentAdmin(r)
	s.auditLog.Record(r.Context(), audit.Event{ActorType: "admin", ActorID: admin.ID, ActorName: admin.Username, Action: action, ResourceType: resource, ResourceID: id, Status: status, IPAddress: clientIP(r), UserAgent: r.UserAgent()})
}

func (s *Server) handleListRemoteHosts(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRemoteHosts(r.Context())
	if err != nil {
		s.remoteError(w, err)
		return
	}
	items := make([]remoteHostDTO, 0, len(list))
	for _, h := range list {
		items = append(items, remoteHostDTO(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSaveRemoteHost(w http.ResponseWriter, r *http.Request) {
	var req remoteHostRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "主机配置格式无效")
		return
	}
	h, err := s.remoteSvc.SaveHost(r.Context(), chi.URLParam(r, "id"), req.Name, req.Address, req.Username, req.Port)
	if err != nil {
		s.remoteError(w, err)
		return
	}
	s.remoteAudit(r, "remote_host.save", "remote_host", h.ID, "success")
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, remoteHostDTO(*h))
}

func (s *Server) handleDeleteRemoteHost(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.remoteSvc.DeleteHost(r.Context(), id); err != nil {
		s.remoteError(w, err)
		return
	}
	s.remoteAudit(r, "remote_host.delete", "remote_host", id, "success")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleCheckRemoteHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.remoteSvc.CheckHost(r.Context(), chi.URLParam(r, "id"), true)
	if h == nil {
		s.remoteError(w, err)
		return
	}
	status := "success"
	if err != nil {
		status = "failure"
	}
	s.remoteAudit(r, "remote_host.check", "remote_host", h.ID, status)
	writeJSON(w, http.StatusOK, remoteHostDTO(*h))
}

func (s *Server) handleListRemoteJobs(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRemoteJobs(r.Context())
	if err != nil {
		s.remoteError(w, err)
		return
	}
	items := make([]remoteJobDTO, 0, len(list))
	for _, j := range list {
		items = append(items, remoteJobDTO(j))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleSaveRemoteJob(w http.ResponseWriter, r *http.Request) {
	var j remoteJobDTO
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&j); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "任务配置格式无效")
		return
	}
	j.ID = chi.URLParam(r, "id")
	model := db.RemoteJob(j)
	if err := s.remoteSvc.SaveJob(r.Context(), &model); err != nil {
		s.remoteError(w, err)
		return
	}
	s.remoteAudit(r, "remote_backup.save", "remote_backup_job", model.ID, "success")
	status := http.StatusOK
	if r.Method == http.MethodPost {
		status = http.StatusCreated
	}
	writeJSON(w, status, remoteJobDTO(model))
}

func (s *Server) handleDeleteRemoteJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.remoteSvc.DeleteJob(r.Context(), id); err != nil {
		s.remoteError(w, err)
		return
	}
	s.remoteAudit(r, "remote_backup.delete", "remote_backup_job", id, "success")
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRunRemoteJob(w http.ResponseWriter, r *http.Request) {
	run, err := s.remoteSvc.Run(r.Context(), chi.URLParam(r, "id"), "manual")
	if err != nil {
		s.remoteError(w, err)
		return
	}
	s.remoteAudit(r, "remote_backup.run", "remote_backup_run", run.ID, "success")
	writeJSON(w, http.StatusAccepted, remoteRunDTO(*run))
}

func (s *Server) handleListRemoteRuns(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListRemoteRuns(r.Context(), r.URL.Query().Get("job_id"), 50)
	if err != nil {
		s.remoteError(w, err)
		return
	}
	items := make([]remoteRunDTO, 0, len(list))
	for _, run := range list {
		items = append(items, remoteRunDTO(run))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) handleGetRemoteRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.store.GetRemoteRun(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.remoteError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, remoteRunDTO(*run))
}

func (s *Server) handleRetryRemoteRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.remoteSvc.Retry(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.remoteError(w, err)
		return
	}
	s.remoteAudit(r, "remote_backup.retry", "remote_backup_run", run.ID, "success")
	writeJSON(w, http.StatusAccepted, remoteRunDTO(*run))
}
