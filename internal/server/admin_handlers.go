package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"safekeep/internal/audit"
	"safekeep/internal/backup"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/proxy"
	"safekeep/internal/storage"
	"safekeep/internal/storage/dropbox"
	"safekeep/internal/storage/gcs"
)

// --- Setup & Auth ---

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	n, err := s.store.CountAdmins(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"initialized": n > 0, "version": s.version})
}

func (s *Server) handleSetupInit(w http.ResponseWriter, r *http.Request) {
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
	if err := s.store.CreateInitialAdminIfEmpty(r.Context(), admin); err != nil {
		if errors.Is(err, db.ErrAlreadyInitialized) {
			writeError(w, http.StatusConflict, "already_initialized", "system is already initialized")
			return
		}
		writeError(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}

	tok, err := s.signAdminToken(admin)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "token_error", err.Error())
		return
	}
	s.setAdminCookie(w, r, tok)
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
	s.setAdminCookie(w, r, tok)
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
		Secure:   isHTTPS(r),
		MaxAge:   -1,
		SameSite: http.SameSiteLaxMode,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleAdminMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"admin": currentAdmin(r), "version": s.version})
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
	admin.PasswordHash = hash
	if tok, err := s.signAdminToken(admin); err == nil {
		s.setAdminCookie(w, r, tok)
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

func isHTTPS(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (s *Server) setAdminCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     adminCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   isHTTPS(r),
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
	ProxyID        string         `json:"proxy_id"`
	ProxyName      string         `json:"proxy_name"`
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
		case "r2":
			if m, err := storage.MaskR2Config([]byte(plain)); err == nil {
				cfgMap = m
			}
		case "gcs":
			if m, err := storage.MaskGCSConfig([]byte(plain)); err == nil {
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
		ProxyID:        u.ProxyID,
		ProxyName:      u.ProxyName,
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
	ProxyID       *string         `json:"proxy_id"`
	ProxyEnabled  bool            `json:"proxy_enabled"`
	ProxyType     string          `json:"proxy_type"`
	ProxyHost     string          `json:"proxy_host"`
	ProxyPort     int             `json:"proxy_port"`
	ProxyUsername string          `json:"proxy_username"`
	ProxyPassword *string         `json:"proxy_password"`
}

func isMaskedPlaceholder(v string) bool {
	return v == "" || v == "*****" || v == "••••••"
}

func (s *Server) handleRevealUpstreamSecrets(w http.ResponseWriter, r *http.Request) {
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

	out := map[string]string{}
	if plain, err := s.cipher.DecryptString(u.ConfigEncrypted); err == nil && plain != "" {
		switch u.Type {
		case "s3":
			var cfg storage.S3Config
			if err := json.Unmarshal([]byte(plain), &cfg); err == nil {
				out["secret_access_key"] = cfg.SecretAccessKey
			}
		case "r2":
			var cfg storage.R2Config
			if err := json.Unmarshal([]byte(plain), &cfg); err == nil {
				out["secret_access_key"] = cfg.SecretAccessKey
			}
		case "gcs":
			var cfg storage.GCSConfig
			if err := json.Unmarshal([]byte(plain), &cfg); err == nil {
				out["service_account_json"] = cfg.ServiceAccountJSON
			}
		case "dropbox":
			var cfg storage.DropboxConfig
			if err := json.Unmarshal([]byte(plain), &cfg); err == nil {
				out["app_secret"] = cfg.AppSecret
				out["refresh_token"] = cfg.RefreshToken
			}
		}
	}
	if u.ProxyPasswordEncrypted != "" {
		if pPass, err := s.cipher.DecryptString(u.ProxyPasswordEncrypted); err == nil {
			out["proxy_password"] = pPass
		}
	} else {
		out["proxy_password"] = ""
	}

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "upstream.reveal_secrets",
		ResourceType: "upstream",
		ResourceID:   u.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"name": u.Name, "type": u.Type},
	})
	writeJSON(w, http.StatusOK, out)
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
	if req.Name == "" || (req.Type != "s3" && req.Type != "r2" && req.Type != "gcs" && req.Type != "dropbox") {
		writeError(w, http.StatusBadRequest, "validation_error", "name is required and type must be 's3', 'r2', 'gcs', or 'dropbox'")
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

	if err := validateUpstreamConfig(req.Type, req.Config); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
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
	if req.ProxyID != nil {
		if err := s.applyProxySelection(r.Context(), u, *req.ProxyID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_proxy", "selected proxy does not exist")
			return
		}
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
	if err := validateUpstreamConfig(existing.Type, mergedConfig); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}

	pPass, _ := s.cipher.DecryptString(existing.ProxyPasswordEncrypted)
	if req.ProxyPassword != nil {
		if *req.ProxyPassword == "" {
			pPass = ""
		} else if !isMaskedPlaceholder(*req.ProxyPassword) {
			pPass = *req.ProxyPassword
		}
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

	if req.ProxyID != nil {
		if err := s.applyProxySelection(r.Context(), existing, *req.ProxyID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_proxy", "selected proxy does not exist")
			return
		}
	} else if req.ProxyEnabled || req.ProxyType != "" || req.ProxyHost != "" {
		existing.ProxyID, existing.ProxyName = "", ""
	} else if existing.ProxyID != "" {
		if err := s.applyProxySelection(r.Context(), existing, existing.ProxyID); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_proxy", "selected proxy does not exist")
			return
		}
	}

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

func validateUpstreamConfig(upstreamType string, raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("config is required")
	}
	switch upstreamType {
	case "s3":
		var cfg storage.S3Config
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("invalid s3 config JSON: %w", err)
		}
		if strings.TrimSpace(cfg.Bucket) == "" {
			return errors.New("s3 bucket is required")
		}
		if strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
			return errors.New("s3 access_key_id and secret_access_key are required")
		}
	case "r2":
		var cfg storage.R2Config
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("invalid r2 config JSON: %w", err)
		}
		if strings.TrimSpace(cfg.Bucket) == "" {
			return errors.New("r2 bucket is required")
		}
		if strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
			return errors.New("r2 access_key_id and secret_access_key are required")
		}
		if _, err := storage.ResolveR2Endpoint(cfg); err != nil {
			return err
		}
	case "gcs":
		var cfg storage.GCSConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("invalid gcs config JSON: %w", err)
		}
		if strings.TrimSpace(cfg.Bucket) == "" {
			return errors.New("gcs bucket is required")
		}
		if err := gcs.ValidateServiceAccountJSON(cfg.ServiceAccountJSON); err != nil {
			return err
		}
	case "dropbox":
		var cfg storage.DropboxConfig
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return fmt.Errorf("invalid dropbox config JSON: %w", err)
		}
		if strings.TrimSpace(cfg.AppKey) == "" {
			return errors.New("dropbox app_key is required")
		}
	}
	return nil
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
		if isMaskedPlaceholder(newCfg.SecretAccessKey) {
			newCfg.SecretAccessKey = oldCfg.SecretAccessKey
		}
		return json.Marshal(newCfg)
	case "r2":
		var oldCfg, newCfg storage.R2Config
		_ = json.Unmarshal(existingRaw, &oldCfg)
		if err := json.Unmarshal(incomingRaw, &newCfg); err != nil {
			return nil, err
		}
		if isMaskedPlaceholder(newCfg.SecretAccessKey) {
			newCfg.SecretAccessKey = oldCfg.SecretAccessKey
		}
		return json.Marshal(newCfg)
	case "gcs":
		var oldCfg, newCfg storage.GCSConfig
		_ = json.Unmarshal(existingRaw, &oldCfg)
		if err := json.Unmarshal(incomingRaw, &newCfg); err != nil {
			return nil, err
		}
		if isMaskedPlaceholder(newCfg.ServiceAccountJSON) {
			newCfg.ServiceAccountJSON = oldCfg.ServiceAccountJSON
		}
		if newCfg.APIBaseURL == "" {
			newCfg.APIBaseURL = oldCfg.APIBaseURL
		}
		if newCfg.TokenURL == "" {
			newCfg.TokenURL = oldCfg.TokenURL
		}
		if p, e, err := storage.ParseGCSServiceAccountSummary(newCfg.ServiceAccountJSON); err == nil {
			newCfg.ProjectID = p
			newCfg.ClientEmail = e
		}
		return json.Marshal(newCfg)
	case "dropbox":
		var oldCfg, newCfg storage.DropboxConfig
		_ = json.Unmarshal(existingRaw, &oldCfg)
		if err := json.Unmarshal(incomingRaw, &newCfg); err != nil {
			return nil, err
		}
		if isMaskedPlaceholder(newCfg.AppSecret) {
			newCfg.AppSecret = oldCfg.AppSecret
		}
		if isMaskedPlaceholder(newCfg.RefreshToken) {
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

type testProxyReq struct {
	ProxyID            string `json:"proxy_id"`
	UpstreamID         string `json:"upstream_id"`
	UpstreamType       string `json:"upstream_type"`
	ProxyType          string `json:"proxy_type"`
	ProxyHost          string `json:"proxy_host"`
	ProxyPort          int    `json:"proxy_port"`
	ProxyUsername      string `json:"proxy_username"`
	ProxyPassword      string `json:"proxy_password"`
	ClearProxyPassword bool   `json:"clear_proxy_password"`
	TargetURL          string `json:"target_url"`
}

func (s *Server) handleTestUpstreamProxy(w http.ResponseWriter, r *http.Request) {
	admin := currentAdmin(r)
	var req testProxyReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json", err.Error())
		return
	}

	pPass := req.ProxyPassword
	if req.ClearProxyPassword {
		pPass = ""
	} else if isMaskedPlaceholder(pPass) && strings.TrimSpace(req.UpstreamID) != "" {
		existing, err := s.store.GetUpstreamByID(r.Context(), strings.TrimSpace(req.UpstreamID))
		if err == nil && existing.ProxyPasswordEncrypted != "" {
			if dec, dErr := s.cipher.DecryptString(existing.ProxyPasswordEncrypted); dErr == nil {
				pPass = dec
			}
		} else {
			pPass = ""
		}
	} else if isMaskedPlaceholder(pPass) {
		pPass = ""
	}

	pCfg := proxy.Config{
		Enabled:  true,
		Type:     strings.TrimSpace(req.ProxyType),
		Host:     strings.TrimSpace(req.ProxyHost),
		Port:     req.ProxyPort,
		Username: strings.TrimSpace(req.ProxyUsername),
		Password: pPass,
	}
	if strings.TrimSpace(req.ProxyID) != "" {
		p, err := s.store.GetProxyByID(r.Context(), strings.TrimSpace(req.ProxyID))
		if err != nil {
			s.writeProxyError(w, err)
			return
		}
		if req.ProxyType == "" && req.ProxyHost == "" {
			pCfg.Type, pCfg.Host, pCfg.Port = p.Type, p.Host, p.Port
			pCfg.Username, err = s.cipher.DecryptString(p.UsernameEncrypted)
			if err != nil {
				s.writeProxyError(w, err)
				return
			}
		}
		if isMaskedPlaceholder(req.ProxyPassword) && !req.ClearProxyPassword {
			pCfg.Password, err = s.cipher.DecryptString(p.PasswordEncrypted)
			if err != nil {
				s.writeProxyError(w, err)
				return
			}
		}
	}
	if err := pCfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_proxy", err.Error())
		return
	}

	targetURL := strings.TrimSpace(req.TargetURL)
	if targetURL == "" {
		switch strings.ToLower(strings.TrimSpace(req.UpstreamType)) {
		case "s3":
			targetURL = "https://s3.amazonaws.com"
		case "r2":
			targetURL = "https://r2.cloudflarestorage.com"
		case "gcs":
			targetURL = "https://storage.googleapis.com"
		default:
			targetURL = "https://api.dropboxapi.com"
		}
	}

	tCtx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	httpStatus, latencyMs, err := proxy.TestConnectivity(tCtx, pCfg, targetURL)
	status := "ok"
	errMsg := ""
	if err != nil {
		status = "failed"
		errMsg = err.Error()
	}

	action, resourceType, resourceID := "upstream.test_proxy", "upstream", strings.TrimSpace(req.UpstreamID)
	if strings.TrimSpace(req.ProxyID) != "" {
		action, resourceType, resourceID = "proxy.test", "proxy", strings.TrimSpace(req.ProxyID)
	}

	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Status:       map[bool]string{true: "success", false: "failure"}[err == nil],
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail: map[string]any{
			"proxy_type":  pCfg.Type,
			"proxy_host":  pCfg.Host,
			"proxy_port":  pCfg.Port,
			"target_url":  targetURL,
			"http_status": httpStatus,
			"latency_ms":  latencyMs,
			"error":       errMsg,
		},
	})

	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"status":     status,
			"target_url": targetURL,
			"latency_ms": latencyMs,
			"error":      errMsg,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":      status,
		"target_url":  targetURL,
		"http_status": httpStatus,
		"latency_ms":  latencyMs,
	})
}

// --- Dropbox Web OAuth2 Flow ---

func (s *Server) resolveDropboxRedirectURI(r *http.Request) string {
	settings, _ := s.store.GetSystemSettings(r.Context())
	baseURL := strings.TrimRight(settings.PublicBaseURL, "/")
	isDefaultLocal := baseURL == "" || baseURL == "http://localhost:8080" || baseURL == "http://localhost:8333"
	if isDefaultLocal && r.Host != "" {
		scheme := "http"
		if isHTTPS(r) {
			scheme = "https"
		}
		baseURL = scheme + "://" + r.Host
	} else if baseURL == "" {
		baseURL = "http://localhost:8333"
	}
	return baseURL + "/admin/api/v1/oauth/dropbox/callback"
}

func (s *Server) pruneExpiredOAuthStatesLocked(now time.Time) {
	for k, v := range s.oauthStates {
		if now.After(v.ExpiresAt) {
			delete(s.oauthStates, k)
		}
	}
}

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

	redirectURI := s.resolveDropboxRedirectURI(r)

	state, err := crypto.RandomToken(16)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "state_error", err.Error())
		return
	}
	now := time.Now()
	s.oauthMu.Lock()
	s.pruneExpiredOAuthStatesLocked(now)
	s.oauthStates[state] = oauthStateEntry{
		UpstreamID: u.ID,
		AdminID:    admin.ID,
		AdminName:  admin.Username,
		ExpiresAt:  now.Add(10 * time.Minute),
	}
	s.oauthMu.Unlock()

	authURL := dropbox.BuildAuthorizeURL(cfg.AppKey, redirectURI, state)
	writeJSON(w, http.StatusOK, map[string]string{
		"authorize_url": authURL,
		"redirect_uri":  redirectURI,
	})
}

func (s *Server) handleDropboxOAuthCallback(w http.ResponseWriter, r *http.Request) {
	redirectErr := func(reason string) {
		http.Redirect(w, r, "/upstreams?oauth=error&reason="+url.QueryEscape(reason), http.StatusFound)
	}

	state := r.URL.Query().Get("state")
	if state != "" {
		s.oauthMu.Lock()
		entry, ok := s.oauthStates[state]
		delete(s.oauthStates, state)
		s.pruneExpiredOAuthStatesLocked(time.Now())
		s.oauthMu.Unlock()

		if errParam := r.URL.Query().Get("error"); errParam != "" {
			desc := r.URL.Query().Get("error_description")
			if desc == "" {
				desc = errParam
			}
			redirectErr(desc)
			return
		}
		if !ok || time.Now().After(entry.ExpiresAt) {
			redirectErr("invalid or expired OAuth state")
			return
		}
		code := r.URL.Query().Get("code")
		if code == "" {
			redirectErr("missing authorization code")
			return
		}

		u, err := s.store.GetUpstreamByID(r.Context(), entry.UpstreamID)
		if err != nil {
			redirectErr("upstream not found")
			return
		}
		plain, err := s.cipher.DecryptString(u.ConfigEncrypted)
		if err != nil {
			redirectErr(err.Error())
			return
		}
		var cfg storage.DropboxConfig
		if err := json.Unmarshal([]byte(plain), &cfg); err != nil {
			redirectErr(err.Error())
			return
		}

		pCfg, err := storage.DecryptProxyConfig(s.cipher, u)
		if err != nil {
			redirectErr(err.Error())
			return
		}
		httpClient, err := proxy.NewHTTPClient(pCfg)
		if err != nil {
			redirectErr(err.Error())
			return
		}

		redirectURI := s.resolveDropboxRedirectURI(r)
		tokRes, err := dropbox.ExchangeCode(r.Context(), httpClient, cfg.APIBaseURL, cfg.AppKey, cfg.AppSecret, code, redirectURI)
		if err != nil {
			redirectErr(fmt.Sprintf("Dropbox OAuth failed: %v", err))
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
			redirectErr(err.Error())
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
		return
	}

	redirectErr("missing OAuth state")
}

// --- Clients / API Keys ---

type clientUpstreamInput struct {
	UpstreamID string `json:"upstream_id"`
	IsDefault  bool   `json:"is_default"`
	Priority   int    `json:"priority"`
}

type clientDTO struct {
	db.Client
	HasAPIKey        bool                `json:"has_api_key"`
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
		HasAPIKey:        c.KeyEncrypted != "",
		HasWebhookSecret: c.WebhookSecretEncrypted != "",
		AllowedMimeTypes: mimes,
		Upstreams:        ups,
	}
}

func (s *Server) handleRevealClientSecrets(w http.ResponseWriter, r *http.Request) {
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
	apiKey := ""
	if c.KeyEncrypted != "" {
		var decErr error
		apiKey, decErr = s.cipher.DecryptString(c.KeyEncrypted)
		if decErr != nil {
			writeError(w, http.StatusInternalServerError, "decrypt_failed", "failed to decrypt api key")
			return
		}
	}
	whSecret := ""
	if c.WebhookSecretEncrypted != "" {
		whSecret, _ = s.cipher.DecryptString(c.WebhookSecretEncrypted)
	}
	s.auditLog.Record(r.Context(), audit.Event{
		ActorType:    "admin",
		ActorID:      admin.ID,
		ActorName:    admin.Username,
		Action:       "client.reveal_secrets",
		ResourceType: "client",
		ResourceID:   c.ID,
		IPAddress:    clientIP(r),
		UserAgent:    r.UserAgent(),
		RequestID:    middleware.GetReqID(r.Context()),
		Detail:       map[string]any{"name": c.Name},
	})
	writeJSON(w, http.StatusOK, map[string]string{
		"api_key":        apiKey,
		"webhook_secret": whSecret,
	})
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
	if err := backup.ValidateWebhookURL(req.WebhookURL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_webhook_url", err.Error())
		return
	}
	if strings.Contains(req.PathPrefix, "..") {
		writeError(w, http.StatusBadRequest, "invalid_path_prefix", "path_prefix cannot contain '..'")
		return
	}

	plainKey, prefix, hash, err := crypto.GenerateAPIKey()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "keygen_error", err.Error())
		return
	}
	keyEnc, err := s.cipher.EncryptString(plainKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encrypt_error", err.Error())
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
		KeyEncrypted:           keyEnc,
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
	if strings.Contains(req.PathPrefix, "..") {
		writeError(w, http.StatusBadRequest, "invalid_path_prefix", "path_prefix cannot contain '..'")
		return
	}
	if err := backup.ValidateWebhookURL(req.WebhookURL); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_webhook_url", err.Error())
		return
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
		if *req.WebhookSecret == "" {
			c.WebhookSecretEncrypted = ""
		} else if !isMaskedPlaceholder(*req.WebhookSecret) {
			c.WebhookSecretEncrypted, _ = s.cipher.EncryptString(*req.WebhookSecret)
		}
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
	c, err := s.store.GetClientByID(r.Context(), id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "client not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "get_failed", err.Error())
		return
	}
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
		Detail:       map[string]any{"name": c.Name, "key_prefix": c.KeyPrefix},
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
	keyEnc, err := s.cipher.EncryptString(plainKey)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encrypt_error", err.Error())
		return
	}
	if err := s.store.RotateClientKey(r.Context(), id, prefix, hash, keyEnc); err != nil {
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
		if errors.Is(err, db.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not_found", "client not found")
			return
		}
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
