package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"safekeep/internal/audit"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/notify"
	"safekeep/internal/proxy"
)

type proxyDTO struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Type          string    `json:"type"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	Username      string    `json:"username"`
	HasPassword   bool      `json:"has_password"`
	UpstreamCount int       `json:"upstream_count"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type proxyWriteReq struct {
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Host     string  `json:"host"`
	Port     int     `json:"port"`
	Username string  `json:"username"`
	Password *string `json:"password"`
}

func (s *Server) toProxyDTO(p db.Proxy) proxyDTO {
	username, _ := s.cipher.DecryptString(p.UsernameEncrypted)
	return proxyDTO{ID: p.ID, Name: p.Name, Type: p.Type, Host: p.Host, Port: p.Port,
		Username: username, HasPassword: p.PasswordEncrypted != "", UpstreamCount: p.UpstreamCount,
		CreatedAt: p.CreatedAt, UpdatedAt: p.UpdatedAt}
}

func (s *Server) handleListProxies(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListProxies(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", "failed to list proxies")
		return
	}
	items := make([]proxyDTO, 0, len(list))
	for _, p := range list {
		items = append(items, s.toProxyDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) recordProxyAudit(r *http.Request, action string, p *db.Proxy) {
	admin := currentAdmin(r)
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType: "admin", ActorID: admin.ID, ActorName: admin.Username,
		Action: action, ResourceType: "proxy", ResourceID: p.ID,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(), RequestID: middleware.GetReqID(r.Context()),
		Detail: map[string]any{"name": p.Name, "type": p.Type},
	})
}

func (s *Server) handleSaveProxy(w http.ResponseWriter, r *http.Request) {
	p := &db.Proxy{ID: crypto.NewULID()}
	updating := r.Method == http.MethodPut
	if updating {
		var err error
		p, err = s.store.GetProxyByID(r.Context(), chi.URLParam(r, "id"))
		if err != nil {
			s.writeProxyError(w, err)
			return
		}
	}
	var req proxyWriteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", "invalid proxy request")
		return
	}
	req.Name, req.Type, req.Host = strings.TrimSpace(req.Name), strings.ToLower(strings.TrimSpace(req.Type)), strings.TrimSpace(req.Host)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "proxy name is required")
		return
	}
	if err := (proxy.Config{Enabled: true, Type: req.Type, Host: req.Host, Port: req.Port}).Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_proxy", err.Error())
		return
	}
	username, err := s.cipher.EncryptString(req.Username)
	if err != nil {
		s.writeProxyError(w, err)
		return
	}
	// 省略密码时保留已有密文，显式空字符串用于清除认证密码。
	if req.Password != nil && !(*req.Password != "" && isMaskedPlaceholder(*req.Password)) {
		p.PasswordEncrypted, err = s.cipher.EncryptString(*req.Password)
		if err != nil {
			s.writeProxyError(w, err)
			return
		}
	}
	p.Name, p.Type, p.Host, p.Port, p.UsernameEncrypted = req.Name, req.Type, req.Host, req.Port, username
	status, action := http.StatusCreated, "proxy.create"
	if updating {
		err = s.store.UpdateProxy(r.Context(), p)
		status, action = http.StatusOK, "proxy.update"
	} else {
		err = s.store.CreateProxy(r.Context(), p)
	}
	if err != nil {
		s.writeProxyError(w, err)
		return
	}
	s.recordProxyAudit(r, action, p)
	writeJSON(w, status, s.toProxyDTO(*p))
}

func (s *Server) handleDeleteProxy(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProxyByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeProxyError(w, err)
		return
	}
	if err := s.store.DeleteProxy(r.Context(), p.ID); err != nil {
		s.writeProxyError(w, err)
		return
	}
	s.recordProxyAudit(r, "proxy.delete", p)
	admin := currentAdmin(r)
	s.notifyAdminEvent(
		"proxy.delete",
		p.ID,
		fmt.Sprintf("[SafeKeep 通知] 网络代理已删除 (%s)", p.Name),
		fmt.Sprintf("管理员删除了网络代理：\n\n- 管理员: %s\n- 代理: %s (%s, %s:%d)\n- 来源 IP: %s\n- 时间: %s\n", admin.Username, p.Name, p.Type, p.Host, p.Port, clientIP(r), time.Now().Format(time.RFC3339)),
		func(rules notify.NotifyRules) bool { return rules.ResourceDelete },
	)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleRevealProxySecrets(w http.ResponseWriter, r *http.Request) {
	p, err := s.store.GetProxyByID(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		s.writeProxyError(w, err)
		return
	}
	password, err := s.cipher.DecryptString(p.PasswordEncrypted)
	if err != nil {
		s.writeProxyError(w, err)
		return
	}
	s.recordProxyAudit(r, "proxy.reveal_secrets", p)
	admin := currentAdmin(r)
	s.notifyAdminEvent(
		"proxy.reveal_secrets",
		p.ID,
		fmt.Sprintf("[SafeKeep 安全通知] 代理认证密码被查看 (%s)", p.Name),
		fmt.Sprintf("管理员查看了代理密码：\n\n- 管理员: %s\n- 代理: %s (%s)\n- 来源 IP: %s\n- 时间: %s\n", admin.Username, p.Name, p.ID, clientIP(r), time.Now().Format(time.RFC3339)),
		func(rules notify.NotifyRules) bool { return rules.SecretsReveal },
	)
	writeJSON(w, http.StatusOK, map[string]string{"password": password})
}

func (s *Server) writeProxyError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "proxy not found")
	case errors.Is(err, db.ErrProxyInUse):
		writeError(w, http.StatusConflict, "proxy_in_use", "proxy is used by upstreams; unbind them before deleting")
	case strings.Contains(err.Error(), "UNIQUE constraint failed"):
		writeError(w, http.StatusConflict, "proxy_conflict", "proxy name already exists")
	default:
		writeError(w, http.StatusInternalServerError, "proxy_failed", "failed to save proxy configuration")
	}
}

func (s *Server) applyProxySelection(ctx context.Context, u *db.Upstream, id string) error {
	u.ProxyID, u.ProxyName = strings.TrimSpace(id), ""
	u.ProxyEnabled = u.ProxyID != ""
	u.ProxyType, u.ProxyHost, u.ProxyPort = "", "", 0
	u.ProxyUsernameEncrypted, u.ProxyPasswordEncrypted = "", ""
	if u.ProxyID == "" {
		return nil
	}
	p, err := s.store.GetProxyByID(ctx, u.ProxyID)
	if err != nil {
		return err
	}
	u.ProxyName, u.ProxyType, u.ProxyHost, u.ProxyPort = p.Name, p.Type, p.Host, p.Port
	u.ProxyUsernameEncrypted, u.ProxyPasswordEncrypted = p.UsernameEncrypted, p.PasswordEncrypted
	return nil
}
