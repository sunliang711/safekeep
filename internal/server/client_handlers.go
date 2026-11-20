package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"safekeep/internal/audit"
	"safekeep/internal/backup"
	"safekeep/internal/db"
)

func (s *Server) handleClientUpload(w http.ResponseWriter, r *http.Request) {
	client := currentClient(r)
	s.executeUploadRequest(w, r, client, "client", client.ID, client.Name, "backup.upload")
}

func (s *Server) handleAdminUploadBackup(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	clientID := strings.TrimSpace(r.URL.Query().Get("client_id"))
	if clientID == "" {
		writeError(w, http.StatusBadRequest, "client_id_required", "query parameter client_id is required")
		return
	}
	client, err := s.store.GetClientByID(r.Context(), clientID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "client_not_found", "selected client does not exist")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_client_failed", err.Error())
		return
	}
	if !client.Enabled {
		writeError(w, http.StatusForbidden, "client_disabled", "selected client is disabled")
		return
	}
	s.executeUploadRequest(w, r, client, "admin", admin.ID, admin.Username, "backup.web_upload")
}

func (s *Server) executeUploadRequest(w http.ResponseWriter, r *http.Request, client *db.Client, actorType, actorID, actorName, action string) {
	q := r.URL.Query()

	mode := firstNonEmpty(q.Get("mode"), r.Header.Get("X-Backup-Mode"))
	targetsRaw := firstNonEmpty(q.Get("targets"), r.Header.Get("X-Backup-Targets"))
	subpath := firstNonEmpty(q.Get("path"), r.Header.Get("X-Backup-Path"))
	rename := firstNonEmpty(q.Get("rename"), r.Header.Get("X-Backup-Rename"))
	expectedSHA := r.Header.Get("X-Content-SHA256")
	idempKey := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	skipDedup := strings.EqualFold(firstNonEmpty(q.Get("dedup"), r.Header.Get("X-Skip-Dedup")), "false") ||
		strings.EqualFold(r.Header.Get("X-Skip-Dedup"), "true")

	var filename, contentType, metadataJSON string
	var body io.Reader
	var preSpooledPath, preSpooledSHA string
	var preSpooledSize int64

	ct := r.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(ct), "multipart/form-data") {
		mr, err := r.MultipartReader()
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_multipart", err.Error())
			return
		}
		var fileFound bool
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				if preSpooledPath != "" {
					_ = os.Remove(preSpooledPath)
				}
				writeError(w, http.StatusBadRequest, "multipart_read_error", err.Error())
				return
			}
			switch part.FormName() {
			case "mode":
				if mode == "" {
					b, _ := io.ReadAll(io.LimitReader(part, 32))
					mode = strings.TrimSpace(string(b))
				}
				_ = part.Close()
			case "targets":
				if targetsRaw == "" {
					b, _ := io.ReadAll(io.LimitReader(part, 1024))
					targetsRaw = strings.TrimSpace(string(b))
				}
				_ = part.Close()
			case "path":
				if subpath == "" {
					b, _ := io.ReadAll(io.LimitReader(part, 1024))
					subpath = strings.TrimSpace(string(b))
				}
				_ = part.Close()
			case "rename":
				if rename == "" {
					b, _ := io.ReadAll(io.LimitReader(part, 512))
					rename = strings.TrimSpace(string(b))
				}
				_ = part.Close()
			case "metadata":
				b, _ := io.ReadAll(io.LimitReader(part, 32*1024))
				metadataJSON = strings.TrimSpace(string(b))
				_ = part.Close()
			case "dedup":
				b, _ := io.ReadAll(io.LimitReader(part, 16))
				if strings.EqualFold(strings.TrimSpace(string(b)), "false") {
					skipDedup = true
				}
				_ = part.Close()
			case "file":
				if !fileFound {
					filename = part.FileName()
					contentType = part.Header.Get("Content-Type")
					sp, sz, sha, spErr := s.backupSvc.SpoolStream(r.Context(), client, filename, part)
					_ = part.Close()
					if spErr != nil {
						status := http.StatusInternalServerError
						code := "spool_failed"
						var he *backup.HTTPError
						if errors.As(spErr, &he) {
							status = he.Status
							code = he.Code
						}
						writeError(w, status, code, spErr.Error())
						return
					}
					preSpooledPath = sp
					preSpooledSize = sz
					preSpooledSHA = sha
					fileFound = true
				} else {
					_ = part.Close()
				}
			default:
				_ = part.Close()
			}
		}
		if !fileFound {
			writeError(w, http.StatusBadRequest, "missing_file", "multipart field 'file' is required")
			return
		}
	} else {
		filename = firstNonEmpty(r.Header.Get("X-File-Name"), q.Get("filename"), "upload.bin")
		contentType = ct
		metadataJSON = q.Get("metadata")
		body = r.Body
	}

	var targets []string
	if targetsRaw != "" {
		for _, t := range strings.Split(targetsRaw, ",") {
			if tr := strings.TrimSpace(t); tr != "" {
				targets = append(targets, tr)
			}
		}
	}

	res, err := s.backupSvc.HandleUpload(r.Context(), backup.UploadRequest{
		Client:           client,
		Filename:         filename,
		Rename:           rename,
		ContentType:      contentType,
		Body:             body,
		PreSpooledPath:   preSpooledPath,
		PreSpooledSize:   preSpooledSize,
		PreSpooledSHA256: preSpooledSHA,
		Mode:             mode,
		Targets:          targets,
		Subpath:          subpath,
		MetadataJSON:     metadataJSON,
		IdempotencyKey:   idempKey,
		ExpectedSHA256:   expectedSHA,
		SkipDedup:        skipDedup,
	})
	if err != nil {
		status := http.StatusInternalServerError
		code := "upload_failed"
		var he *backup.HTTPError
		if errors.As(err, &he) {
			status = he.Status
			code = he.Code
		}
		s.auditLog.Record(r.Context(), audit.Event{
			ActorType:    actorType,
			ActorID:      actorID,
			ActorName:    actorName,
			Action:       action,
			ResourceType: "backup_task",
			Status:       "failure",
			IPAddress:    clientIP(r),
			UserAgent:    r.UserAgent(),
			RequestID:    middleware.GetReqID(r.Context()),
			Detail:       map[string]any{"client_id": client.ID, "client_name": client.Name, "filename": filename, "error": err.Error()},
		})
		writeError(w, status, code, err.Error())
		return
	}

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    actorType,
		ActorID:      actorID,
		ActorName:    actorName,
		Action:       action,
		ResourceType: "backup_task",
		ResourceID:   res.Task.ID,
		Status:       "success",
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail: map[string]any{
			"client_id":         client.ID,
			"client_name":       client.Name,
			"filename":          res.Task.OriginalFilename,
			"remote_filename":   res.Task.RemoteFilename,
			"file_size":         res.Task.FileSize,
			"sha256":            res.Task.SHA256,
			"mode":              res.Task.Mode,
			"task_status":       res.Task.Status,
			"deduplicated":      res.Task.Deduplicated,
			"timed_out_async":   res.TimedOutToAsync,
			"idempotent_replay": res.IdempotentReplay,
		},
	})

	writeJSON(w, res.StatusCode, res)
}

func (s *Server) handleClientListBackups(w http.ResponseWriter, r *http.Request) {
	client := currentClient(r)
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	offset, _ := strconv.Atoi(q.Get("offset"))
	tasks, total, err := s.store.ListBackupTasks(r.Context(), db.TaskFilter{
		ClientID: client.ID,
		Status:   q.Get("status"),
		Mode:     q.Get("mode"),
		Query:    q.Get("q"),
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": tasks,
		"total": total,
	})
}

func (s *Server) handleClientGetBackup(w http.ResponseWriter, r *http.Request) {
	client := currentClient(r)
	taskID := chi.URLParam(r, "taskID")
	task, err := s.store.GetBackupTask(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "backup task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	if task.ClientID != client.ID {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return
	}
	writeJSON(w, http.StatusOK, task)
}

func (s *Server) handleClientDownloadBackup(w http.ResponseWriter, r *http.Request) {
	client := currentClient(r)
	taskID := chi.URLParam(r, "taskID")
	task, err := s.store.GetBackupTask(r.Context(), taskID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "backup task not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
	if task.ClientID != client.ID {
		writeError(w, http.StatusForbidden, "forbidden", "access denied")
		return
	}

	rc, size, ct, filename, err := s.backupSvc.DownloadTaskFile(r.Context(), task, r.URL.Query().Get("upstream_id"))
	if err != nil {
		var he *backup.HTTPError
		if errors.As(err, &he) {
			writeError(w, he.Status, he.Code, he.Message)
			return
		}
		writeError(w, http.StatusBadGateway, "download_failed", err.Error())
		return
	}
	defer rc.Close()

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "client",
		ActorID:      client.ID,
		ActorName:    client.Name,
		Action:       "backup.download",
		ResourceType: "backup_task",
		ResourceID:   task.ID,
		Status:       "success",
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})

	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename*=UTF-8''%s`, url.PathEscape(filename)))
	if size > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	}
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, rc)
}

func (s *Server) handleClientListUpstreams(w http.ResponseWriter, r *http.Request) {
	client := currentClient(r)
	list, err := s.store.GetClientUpstreams(r.Context(), client.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
