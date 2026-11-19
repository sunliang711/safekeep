package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"safekeep/internal/audit"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/proxy"
	"safekeep/internal/storage"
	"safekeep/internal/storage/dropbox"
)

// --- Setup & Auth ---

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountAdmins(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"initialized": n > 0})
}

func (s *Server) handleSetupInit(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountAdmins(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	if n > 0 {
		writeError(w, http.StatusConflict, "already_initialized", "system is already initialized")
		return
	}

	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Username = strings.TrimSpace(req.Username)
	if len(req.Username) < 3 || len(req.Password) < 8 {
		writeError(w, http.StatusBadRequest, "validation_error", "username >= 3 chars and password >= 8 chars required")
		return
	}
	hash, err := crypto.HashPassword(req.Password)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", err.Error())
		return
	}
	admin := &db.Admin{
		ID:           crypto.NewULID(),
		Username:     req.Username,
		PasswordHash: hash,
		IsActive:     true,
	}
	if err := s.store.CreateAdmin(r.Context(), admin); err != nil {
		writeError(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}

	tok, err := s.signAdminToken(admin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", err.Error())
		return
	}
	s.setAdminCookie(w, tok)
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "system.setup_init",
		ResourceType: "admin",
		ResourceID:   admin.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})
	writeJSON(w, http.StatusCreated, map[string]any{"admin": admin, "token": tok})
}

func (s *Server) handleAdminLogin(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	admin, err := s.store.GetAdminByUsername(r.Context(), strings.TrimSpace(req.Username))
	if err != nil || !admin.IsActive || !crypto.VerifyPassword(admin.PasswordHash, req.Password) {
		s.auditLog.Record(r.Context(), audit.Event{
			ActorType:    "admin",
			ActorName:    strings.TrimSpace(req.Username),
			Action:       "admin.login",
			ResourceType: "admin",
			Status:       "failure",
			IPAddress:    clientIP(r),
			UserAgent:    r.UserAgent(),
			RequestID:    middleware.GetReqID(r.Context()),
		})
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password")
		return
	}

	_ = s.store.UpdateAdminLogin(r.Context(), admin.ID)
	tok, err := s.signAdminToken(admin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", err.Error())
		return
	}
	s.setAdminCookie(w, tok)
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "admin.login",
		ResourceType: "admin",
		ResourceID:   admin.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})
	writeJSON(w, http.StatusOK, map[string]any{"admin": admin, "token": tok})
}

func (s *Server) handleAdminLogout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"admin": currentAdmin(r)})
}

func (s *Server) handleAdminChangePassword(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	var req struct {
		OldPassword string `json:"old_password"`
		NewPassword string `json:"new_password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if !crypto.VerifyPassword(admin.PasswordHash, req.OldPassword) {
		writeError(w, http.StatusBadRequest, "invalid_password", "current password is incorrect")
		return
	}
	if len(req.NewPassword) < 8 {
		writeError(w, http.StatusBadRequest, "weak_password", "new password must be at least 8 characters")
		return
	}
	hash, err := crypto.HashPassword(req.NewPassword)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "hash_error", err.Error())
		return
	}
	if err := s.store.UpdateAdminPassword(r.Context(), admin.ID, hash); err != nil {
		writeError(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "admin.change_password",
		ResourceType: "admin",
		ResourceID:   admin.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) setAdminCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   86400,
	})
}

// --- Dashboard ---

func (s *Server) handleDashboardSummary(w http.ResponseWriter, r *http.Request) {
	sum, err := s.store.GetDashboardSummary(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "summary_error", err.Error())
		return
	}
	if settings, err := s.store.GetSystemSettings(r.Context()); err == nil {
		var spoolBytes int64
		_ = filepath.WalkDir(settings.SpoolDir, func(_ string, d os.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				if info, iErr := d.Info(); iErr == nil {
					spoolBytes += info.Size()
				}
			}
			return nil
		})
		sum.SpoolBytesUsed = spoolBytes
	}
	writeJSON(w, http.StatusOK, sum)
}

// --- Upstreams ---

type upstreamDTO struct {
	ID             string         `json:"id"`
	Name           string         `json:"name"`
	Type           string         `json:"type"`
	Enabled        bool           `json:"enabled"`
	Config         map[string]any `json:"config"`
	ProxyEnabled   bool           `json:"proxy_enabled"`
	ProxyType      string         `json:"proxy_type"`
	ProxyHost      string         `json:"proxy_host"`
	ProxyPort      int            `json:"proxy_port"`
	ProxyUsername  string         `json:"proxy_username,omitempty"`
	HasProxyPass   bool           `json:"has_proxy_password"`
	LastTestStatus string         `json:"last_test_status"`
	LastTestError  string         `json:"last_test_error,omitempty"`
	LastTestedAt   *time.Time     `json:"last_tested_at,omitempty"`
	CreatedAt      time.Time      `json:"created_at"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

func (s *Server) toUpstreamDTO(u db.Upstream) upstreamDTO {
	cfgMap := map[string]any{}
	if plain, err := s.cipher.DecryptString(u.ConfigEncrypted); err == nil && plain != "" {
		switch u.Type {
		case "s3":
			if m, err := storage.MaskS3Config([]byte(plain)); err == nil {
				cfgMap = m
			}
		case "dropbox":
			if m, err := storage.MaskDropboxConfig([]byte(plain)); err == nil {
				cfgMap = m
			}
		}
	}
	pUser, _ := s.cipher.DecryptString(u.ProxyUsernameEncrypted)
	pPass, _ := s.cipher.DecryptString(u.ProxyPasswordEncrypted)
	return upstreamDTO{
		ID:             u.ID,
		Name:           u.Name,
		Type:           u.Type,
		Enabled:        u.Enabled,
		Config:         cfgMap,
		ProxyEnabled:   u.ProxyEnabled,
		ProxyType:      u.ProxyType,
		ProxyHost:      u.ProxyHost,
		ProxyPort:      u.ProxyPort,
		ProxyUsername:  pUser,
		HasProxyPass:   pPass != "",
		LastTestStatus: u.LastTestStatus,
		LastTestError:  u.LastTestError,
		LastTestedAt:   u.LastTestedAt,
		CreatedAt:      u.CreatedAt,
		UpdatedAt:      u.UpdatedAt,
	}
}

type upstreamWriteReq struct {
	Name          string          `json:"name"`
	Type          string          `json:"type"`
	Enabled       bool            `json:"enabled"`
	Config        json.RawMessage `json:"config"`
	ProxyEnabled  bool            `json:"proxy_enabled"`
	ProxyType     string          `json:"proxy_type"`
	ProxyHost     string          `json:"proxy_host"`
	ProxyPort     int             `json:"proxy_port"`
	ProxyUsername string          `json:"proxy_username"`
	ProxyPassword *string         `json:"proxy_password"`
}

func (s *Server) handleListUpstreams(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListUpstreams(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	dtos := make([]upstreamDTO, 0, len(list))
	for _, u := range list {
		dtos = append(dtos, s.toUpstreamDTO(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}

func (s *Server) handleCreateUpstream(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	var req upstreamWriteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if req.Name == "" || (req.Type != "s3" && req.Type != "dropbox") {
		writeError(w, http.StatusBadRequest, "validation_error", "name is required and type must be 's3' or 'dropbox'")
		return
	}

	pPass := ""
	if req.ProxyPassword != nil {
		pPass = *req.ProxyPassword
	}
	pCfg := proxy.Config{
		Enabled:  req.ProxyEnabled,
		Type:     req.ProxyType,
		Host:     req.ProxyHost,
		Port:     req.ProxyPort,
		Username: req.ProxyUsername,
		Password: pPass,
	}
	if err := pCfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_proxy", err.Error())
		return
	}

	cfgEnc, err := s.cipher.EncryptString(string(req.Config))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encrypt_error", err.Error())
		return
	}
	pUserEnc, _ := s.cipher.EncryptString(req.ProxyUsername)
	pPassEnc, _ := s.cipher.EncryptString(pPass)

	u := &db.Upstream{
		ID:                     crypto.NewULID(),
		Name:                   req.Name,
		Type:                   req.Type,
		Enabled:                req.Enabled,
		ConfigEncrypted:        cfgEnc,
		ProxyEnabled:           req.ProxyEnabled,
		ProxyType:              strings.ToLower(strings.TrimSpace(req.ProxyType)),
		ProxyHost:              strings.TrimSpace(req.ProxyHost),
		ProxyPort:              req.ProxyPort,
		ProxyUsernameEncrypted: pUserEnc,
		ProxyPasswordEncrypted: pPassEnc,
	}
	if err := s.store.CreateUpstream(r.Context(), u); err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "upstream.create",
		ResourceType: "upstream",
		ResourceID:   u.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"name": u.Name, "type": u.Type, "proxy_enabled": u.ProxyEnabled},
	})
	writeJSON(w, http.StatusCreated, s.toUpstreamDTO(*u))
}

func (s *Server) handleUpdateUpstream(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	existing, err := s.store.GetUpstreamByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	var req upstreamWriteReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		req.Name = existing.Name
	}

	// Merge secrets if masked/omitted on update
	existingPlain, _ := s.cipher.DecryptString(existing.ConfigEncrypted)
	mergedConfig, err := mergeUpstreamConfigSecrets(existing.Type, []byte(existingPlain), req.Config)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}

	pPass, _ := s.cipher.DecryptString(existing.ProxyPasswordEncrypted)
	if req.ProxyPassword != nil {
		pPass = *req.ProxyPassword
	}
	pCfg := proxy.Config{
		Enabled:  req.ProxyEnabled,
		Type:     req.ProxyType,
		Host:     req.ProxyHost,
		Port:     req.ProxyPort,
		Username: req.ProxyUsername,
		Password: pPass,
	}
	if err := pCfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_proxy", err.Error())
		return
	}

	cfgEnc, err := s.cipher.EncryptString(string(mergedConfig))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encrypt_error", err.Error())
		return
	}
	pUserEnc, _ := s.cipher.EncryptString(req.ProxyUsername)
	pPassEnc, _ := s.cipher.EncryptString(pPass)

	existing.Name = req.Name
	existing.Enabled = req.Enabled
	existing.ConfigEncrypted = cfgEnc
	existing.ProxyEnabled = req.ProxyEnabled
	existing.ProxyType = strings.ToLower(strings.TrimSpace(req.ProxyType))
	existing.ProxyHost = strings.TrimSpace(req.ProxyHost)
	existing.ProxyPort = req.ProxyPort
	existing.ProxyUsernameEncrypted = pUserEnc
	existing.ProxyPasswordEncrypted = pPassEnc

	if err := s.store.UpdateUpstream(r.Context(), existing); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}
	s.registry.Invalidate(existing.ID)

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "upstream.update",
		ResourceType: "upstream",
		ResourceID:   existing.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"name": existing.Name, "enabled": existing.Enabled, "proxy_enabled": existing.ProxyEnabled},
	})
	writeJSON(w, http.StatusOK, s.toUpstreamDTO(*existing))
}

func mergeUpstreamConfigSecrets(upstreamType string, existingRaw, incomingRaw json.RawMessage) ([]byte, error) {
	if len(incomingRaw) == 0 || string(incomingRaw) == "null" {
		return existingRaw, nil
	}
	switch upstreamType {
	case "s3":
		var oldCfg, newCfg storage.S3Config
		_ = json.Unmarshal(existingRaw, &oldCfg)
		if err := json.Unmarshal(incomingRaw, &newCfg); err != nil {
			return nil, err
		}
		if newCfg.SecretAccessKey == "" {
			newCfg.SecretAccessKey = oldCfg.SecretAccessKey
		}
		return json.Marshal(newCfg)
	case "dropbox":
		var oldCfg, newCfg storage.DropboxConfig
		_ = json.Unmarshal(existingRaw, &oldCfg)
		if err := json.Unmarshal(incomingRaw, &newCfg); err != nil {
			return nil, err
		}
		if newCfg.AppSecret == "" {
			newCfg.AppSecret = oldCfg.AppSecret
		}
		if newCfg.RefreshToken == "" {
			newCfg.RefreshToken = oldCfg.RefreshToken
		}
		if newCfg.AccessToken == "" {
			newCfg.AccessToken = oldCfg.AccessToken
			newCfg.TokenExpiresAt = oldCfg.TokenExpiresAt
		}
		if newCfg.AccountEmail == "" {
			newCfg.AccountEmail = oldCfg.AccountEmail
		}
		return json.Marshal(newCfg)
	default:
		return incomingRaw, nil
	}
}

func (s *Server) handleDeleteUpstream(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteUpstream(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	s.registry.Invalidate(id)
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "upstream.delete",
		ResourceType: "upstream",
		ResourceID:   id,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleTestUpstream(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	u, err := s.store.GetUpstreamByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "upstream not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	tCtx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	p, err := s.registry.BuildFromUpstream(tCtx, u)
	if err == nil {
		err = p.TestConnection(tCtx)
	}

	status := "ok"
	errMsg := ""
	if err != nil {
		status = "failed"
		errMsg = err.Error()
	}
	_ = s.store.UpdateUpstreamTestStatus(r.Context(), u.ID, status, errMsg)

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "upstream.test",
		ResourceType: "upstream",
		ResourceID:   u.ID,
		Status:       map[bool]string{true: "success", false: "failure"}[err == nil],
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"name": u.Name, "error": errMsg},
	})

	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "failed", "error": errMsg})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// --- Dropbox Web OAuth2 Flow ---

func (s *Server) handleDropboxOAuthStart(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	u, err := s.store.GetUpstreamByID(r.Context(), id)
	if err != nil || u.Type != "dropbox" {
		writeError(w, http.StatusBadRequest, "invalid_upstream", "dropbox upstream not found")
		return
	}
	plain, err := s.cipher.DecryptString(u.ConfigEncrypted)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "decrypt_error", err.Error())
		return
	}
	var cfg storage.DropboxConfig
	if err := json.Unmarshal([]byte(plain), &cfg); err != nil || cfg.AppKey == "" {
		writeError(w, http.StatusBadRequest, "missing_app_key", "please save Dropbox App Key and App Secret first")
		return
	}

	settings, _ := s.store.GetSystemSettings(r.Context())
	baseURL := strings.TrimRight(settings.PublicBaseURL, "/")
	if baseURL == "" {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		baseURL = scheme + "://" + r.Host
	}
	redirectURI := baseURL + "/admin/api/v1/oauth/dropbox/callback"

	state, err := crypto.RandomToken(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_error", err.Error())
		return
	}
	s.oauthMu.Lock()
	s.oauthStates[state] = oauthStateEntry{
		UpstreamID: u.ID,
		AdminID:    admin.ID,
		AdminName:  admin.Username,
		ExpiresAt:  time.Now().Add(10 * time.Minute),
	}
	s.oauthMu.Unlock()

	authURL := dropbox.BuildAuthorizeURL(cfg.AppKey, redirectURI, state)
	writeJSON(w, http.StatusOK, map[string]string{
		"authorize_url": authURL,
		"redirect_uri":  redirectURI,
	})
}

func (s *Server) handleDropboxOAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	s.oauthMu.Lock()
	entry, ok := s.oauthStates[state]
	delete(s.oauthStates, state)
	s.oauthMu.Unlock()
	if !ok || time.Now().After(entry.ExpiresAt) {
		http.Error(w, "invalid or expired OAuth state", http.StatusBadRequest)
		return
	}

	u, err := s.store.GetUpstreamByID(r.Context(), entry.UpstreamID)
	if err != nil {
		http.Error(w, "upstream not found", http.StatusNotFound)
		return
	}
	plain, err := s.cipher.DecryptString(u.ConfigEncrypted)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var cfg storage.DropboxConfig
	if err := json.Unmarshal([]byte(plain), &cfg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	pCfg, err := storage.DecryptProxyConfig(s.cipher, u)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpClient, err := proxy.NewHTTPClient(pCfg)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	settings, _ := s.store.GetSystemSettings(r.Context())
	baseURL := strings.TrimRight(settings.PublicBaseURL, "/")
	if baseURL == "" {
		baseURL = "http://" + r.Host
	}
	redirectURI := baseURL + "/admin/api/v1/oauth/dropbox/callback"

	tokRes, err := dropbox.ExchangeCode(r.Context(), httpClient, cfg.APIBaseURL, cfg.AppKey, cfg.AppSecret, code, redirectURI)
	if err != nil {
		http.Error(w, fmt.Sprintf("Dropbox OAuth failed: %v", err), http.StatusBadGateway)
		return
	}

	cfg.AccessToken = tokRes.AccessToken
	if tokRes.RefreshToken != "" {
		cfg.RefreshToken = tokRes.RefreshToken
	}
	if tokRes.ExpiresIn > 0 {
		cfg.TokenExpiresAt = time.Now().UTC().Add(time.Duration(tokRes.ExpiresIn) * time.Second)
	}
	if tokRes.Email != "" {
		cfg.AccountEmail = tokRes.Email
	}
	newJSON, _ := json.Marshal(cfg)
	enc, _ := s.cipher.EncryptString(string(newJSON))
	if err := s.store.UpdateUpstreamConfigEncrypted(r.Context(), u.ID, enc); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	_ = s.store.UpdateUpstreamTestStatus(r.Context(), u.ID, "ok", "")
	s.registry.Invalidate(u.ID)

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      entry.AdminID,
		ActorName:    entry.AdminName,
		Action:       "upstream.dropbox_oauth",
		ResourceType: "upstream",
		ResourceID:   u.ID,
		IPAddress:    clientIP(r),
		Detail:       map[string]any{"account_email": tokRes.Email},
	})

	http.Redirect(w, r, "/upstreams?oauth=success", http.StatusFound)
}

// --- Clients / API Keys ---

type clientUpstreamInput struct {
	UpstreamID string `json:"upstream_id"`
	IsDefault  bool   `json:"is_default"`
	Priority   int    `json:"priority"`
}

type clientDTO struct {
	db.Client
	HasWebhookSecret bool                `json:"has_webhook_secret"`
	AllowedMimeTypes []string            `json:"allowed_mime_types"`
	Upstreams        []db.ClientUpstream `json:"upstreams"`
}

func (s *Server) toClientDTO(ctx context.Context, c db.Client) clientDTO {
	var mimes []string
	_ = json.Unmarshal([]byte(c.AllowedMimeTypesJSON), &mimes)
	if mimes == nil {
		mimes = []string{}
	}
	ups, _ := s.store.GetClientUpstreams(ctx, c.ID)
	if ups == nil {
		ups = []db.ClientUpstream{}
	}
	return clientDTO{
		Client:           c,
		HasWebhookSecret: c.WebhookSecretEncrypted != "",
		AllowedMimeTypes: mimes,
		Upstreams:        ups,
	}
}

func (s *Server) handleListClients(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListClients(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list_failed", err.Error())
		return
	}
	dtos := make([]clientDTO, 0, len(list))
	for _, c := range list {
		dtos = append(dtos, s.toClientDTO(r.Context(), c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": dtos})
}

func (s *Server) handleCreateClient(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	var req struct {
		Name             string                `json:"name"`
		Description      string                `json:"description"`
		Enabled          bool                  `json:"enabled"`
		DefaultMode      string                `json:"default_mode"`
		PathPrefix       string                `json:"path_prefix"`
		MaxFileSizeBytes int64                 `json:"max_file_size_bytes"`
		AllowedMimeTypes []string              `json:"allowed_mime_types"`
		RateLimitRPM     int                   `json:"rate_limit_rpm"`
		WebhookURL       string                `json:"webhook_url"`
		WebhookSecret    string                `json:"webhook_secret"`
		ExpiresAt        *time.Time            `json:"expires_at"`
		Upstreams        []clientUpstreamInput `json:"upstreams"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "validation_error", "client name is required")
		return
	}
	if req.DefaultMode == "" {
		req.DefaultMode = "async"
	}
	if req.DefaultMode != "sync" && req.DefaultMode != "async" {
		writeError(w, http.StatusBadRequest, "validation_error", "default_mode must be sync or async")
		return
	}

	plainKey, prefix, hash, err := crypto.GenerateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "keygen_error", err.Error())
		return
	}
	mimesBytes, _ := json.Marshal(req.AllowedMimeTypes)
	whEnc, _ := s.cipher.EncryptString(req.WebhookSecret)

	c := &db.Client{
		ID:                     crypto.NewULID(),
		Name:                   req.Name,
		Description:            req.Description,
		KeyPrefix:              prefix,
		KeyHash:                hash,
		Enabled:                req.Enabled,
		DefaultMode:            req.DefaultMode,
		PathPrefix:             strings.Trim(req.PathPrefix, "/"),
		MaxFileSizeBytes:       req.MaxFileSizeBytes,
		AllowedMimeTypesJSON:   string(mimesBytes),
		RateLimitRPM:           req.RateLimitRPM,
		WebhookURL:             strings.TrimSpace(req.WebhookURL),
		WebhookSecretEncrypted: whEnc,
		ExpiresAt:              req.ExpiresAt,
	}

	var cuList []db.ClientUpstream
	for i, u := range req.Upstreams {
		prio := u.Priority
		if prio == 0 {
			prio = (i + 1) * 10
		}
		cuList = append(cuList, db.ClientUpstream{
			ClientID:   c.ID,
			UpstreamID: u.UpstreamID,
			IsDefault:  u.IsDefault,
			Priority:   prio,
		})
	}

	if err := s.store.CreateClient(r.Context(), c, cuList); err != nil {
		writeError(w, http.StatusBadRequest, "create_failed", err.Error())
		return
	}

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "client.create",
		ResourceType: "client",
		ResourceID:   c.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"name": c.Name, "key_prefix": prefix},
	})

	writeJSON(w, http.StatusCreated, map[string]any{
		"client":  s.toClientDTO(r.Context(), *c),
		"api_key": plainKey,
	})
}

func (s *Server) handleUpdateClient(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	c, err := s.store.GetClientByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "client not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}

	var req struct {
		Name             string                `json:"name"`
		Description      string                `json:"description"`
		Enabled          bool                  `json:"enabled"`
		DefaultMode      string                `json:"default_mode"`
		PathPrefix       string                `json:"path_prefix"`
		MaxFileSizeBytes int64                 `json:"max_file_size_bytes"`
		AllowedMimeTypes []string              `json:"allowed_mime_types"`
		RateLimitRPM     int                   `json:"rate_limit_rpm"`
		WebhookURL       string                `json:"webhook_url"`
		WebhookSecret    *string               `json:"webhook_secret"`
		ExpiresAt        *time.Time            `json:"expires_at"`
		Upstreams        []clientUpstreamInput `json:"upstreams"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(req.Name) != "" {
		c.Name = strings.TrimSpace(req.Name)
	}
	c.Description = req.Description
	c.Enabled = req.Enabled
	if req.DefaultMode == "sync" || req.DefaultMode == "async" {
		c.DefaultMode = req.DefaultMode
	}
	c.PathPrefix = strings.Trim(req.PathPrefix, "/")
	c.MaxFileSizeBytes = req.MaxFileSizeBytes
	if req.AllowedMimeTypes != nil {
		b, _ := json.Marshal(req.AllowedMimeTypes)
		c.AllowedMimeTypesJSON = string(b)
	}
	c.RateLimitRPM = req.RateLimitRPM
	c.WebhookURL = strings.TrimSpace(req.WebhookURL)
	if req.WebhookSecret != nil {
		c.WebhookSecretEncrypted, _ = s.cipher.EncryptString(*req.WebhookSecret)
	}
	c.ExpiresAt = req.ExpiresAt

	if err := s.store.UpdateClient(r.Context(), c); err != nil {
		writeError(w, http.StatusBadRequest, "update_failed", err.Error())
		return
	}

	if req.Upstreams != nil {
		var cuList []db.ClientUpstream
		for i, u := range req.Upstreams {
			prio := u.Priority
			if prio == 0 {
				prio = (i + 1) * 10
			}
			cuList = append(cuList, db.ClientUpstream{
				ClientID:   c.ID,
				UpstreamID: u.UpstreamID,
				IsDefault:  u.IsDefault,
				Priority:   prio,
			})
		}
		if err := s.store.SetClientUpstreams(r.Context(), c.ID, cuList); err != nil {
			writeError(w, http.StatusBadRequest, "set_upstreams_failed", err.Error())
			return
		}
	}

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "client.update",
		ResourceType: "client",
		ResourceID:   c.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"name": c.Name, "enabled": c.Enabled},
	})
	writeJSON(w, http.StatusOK, s.toClientDTO(r.Context(), *c))
}

func (s *Server) handleDeleteClient(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteClient(r.Context(), id); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "client not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "client.delete",
		ResourceType: "client",
		ResourceID:   id,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func (s *Server) handleRotateClientKey(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	plainKey, prefix, hash, err := crypto.GenerateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "keygen_error", err.Error())
		return
	}
	if err := s.store.RotateClientKey(r.Context(), id, prefix, hash); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "client not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "rotate_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "client.rotate_key",
		ResourceType: "client",
		ResourceID:   id,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"new_prefix": prefix},
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"api_key":    plainKey,
		"key_prefix": prefix,
	})
}

func (s *Server) handleSetClientUpstreams(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	id := chi.URLParam(r, "id")
	var req struct {
		Upstreams []clientUpstreamInput `json:"upstreams"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}
	var cuList []db.ClientUpstream
	for i, u := range req.Upstreams {
		prio := u.Priority
		if prio == 0 {
			prio = (i + 1) * 10
		}
		cuList = append(cuList, db.ClientUpstream{
			ClientID:   id,
			UpstreamID: u.UpstreamID,
			IsDefault:  u.IsDefault,
			Priority:   prio,
		})
	}
	if err := s.store.SetClientUpstreams(r.Context(), id, cuList); err != nil {
		writeError(w, http.StatusBadRequest, "set_upstreams_failed", err.Error())
		return
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "client.set_upstreams",
		ResourceType: "client",
		ResourceID:   id,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
	})
	ups, _ := s.store.GetClientUpstreams(r.Context(), id)
	writeJSON(w, http.StatusOK, map[string]any{"items": ups})
}
