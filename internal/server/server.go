package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/golang-jwt/jwt/v5"

	"safekeep/internal/audit"
	"safekeep/internal/backup"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/migration"
	"safekeep/internal/queue"
	"safekeep/internal/scheduler"
	"safekeep/internal/storage"
)

const adminCookieName = "safekeep_admin_token"

type ctxKey string

const (
	ctxAdminKey  ctxKey = "admin"
	ctxClientKey ctxKey = "client"
)

type Server struct {
	store     *db.Store
	cipher    *crypto.Cipher
	jwtSecret []byte
	registry  *storage.Registry
	backupSvc *backup.Service
	pool      *queue.WorkerPool
	sched     *scheduler.Engine
	migSvc    *migration.Service
	auditLog    *audit.Logger
	logger      *slog.Logger
	webFS       fs.FS
	version     string
	corsOrigins []string

	rateMu      sync.Mutex
	rateBuckets map[string]*rateWindow

	oauthMu     sync.Mutex
	oauthStates map[string]oauthStateEntry
}

type rateWindow struct {
	windowStart time.Time
	count       int
}

type oauthStateEntry struct {
	UpstreamID string
	AdminID    string
	AdminName  string
	ExpiresAt  time.Time
}

func New(
	store *db.Store,
	cipher *crypto.Cipher,
	registry *storage.Registry,
	backupSvc *backup.Service,
	pool *queue.WorkerPool,
	sched *scheduler.Engine,
	migSvc *migration.Service,
	auditLog *audit.Logger,
	logger *slog.Logger,
	webFS fs.FS,
) *Server {
	return &Server{
		store:       store,
		cipher:      cipher,
		jwtSecret:   cipher.DeriveSubKey("admin-jwt-signing"),
		registry:    registry,
		backupSvc:   backupSvc,
		pool:        pool,
		sched:       sched,
		migSvc:      migSvc,
		auditLog:    auditLog,
		logger:      logger,
		webFS:       webFS,
		version:     "dev",
		rateBuckets: make(map[string]*rateWindow),
		oauthStates: make(map[string]oauthStateEntry),
	}
}

// SetVersion sets the build version reported by status and auth endpoints.
func (s *Server) SetVersion(v string) {
	if strings.TrimSpace(v) != "" {
		s.version = strings.TrimSpace(v)
	}
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(s.corsMiddleware)

	// Health & Readiness
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.version})
	})
	r.Get("/readyz", s.handleReadyz)

	// Client Data Plane API
	r.Route("/api/v1", func(r chi.Router) {
		r.Use(s.clientAuthMiddleware)
		r.Post("/backups", s.handleClientUpload)
		r.Get("/backups", s.handleClientListBackups)
		r.Get("/backups/{taskID}", s.handleClientGetBackup)
		r.Get("/backups/{taskID}/download", s.handleClientDownloadBackup)
		r.Get("/upstreams", s.handleClientListUpstreams)
	})

	// Web Admin API
	r.Route("/admin/api/v1", func(r chi.Router) {
		r.Get("/setup/status", s.handleSetupStatus)
		r.Post("/setup/init", s.handleSetupInit)
		r.Post("/auth/login", s.handleAdminLogin)
		r.Post("/auth/logout", s.handleAdminLogout)
		r.Get("/oauth/dropbox/callback", s.handleDropboxOAuthCallback)

		r.Group(func(r chi.Router) {
			r.Use(s.adminAuthMiddleware)
			r.Get("/auth/me", s.handleAdminMe)
			r.Put("/auth/password", s.handleAdminChangePassword)

			r.Get("/dashboard/summary", s.handleDashboardSummary)

			r.Get("/upstreams", s.handleListUpstreams)
			r.Post("/upstreams", s.handleCreateUpstream)
			r.Post("/upstreams/test-proxy", s.handleTestUpstreamProxy)
			r.Get("/upstreams/{id}/secrets", s.handleRevealUpstreamSecrets)
			r.Put("/upstreams/{id}", s.handleUpdateUpstream)
			r.Delete("/upstreams/{id}", s.handleDeleteUpstream)
			r.Post("/upstreams/{id}/test", s.handleTestUpstream)
			r.Get("/upstreams/{id}/oauth/dropbox/start", s.handleDropboxOAuthStart)

			r.Get("/clients", s.handleListClients)
			r.Post("/clients", s.handleCreateClient)
			r.Get("/clients/{id}/secrets", s.handleRevealClientSecrets)
			r.Put("/clients/{id}", s.handleUpdateClient)
			r.Delete("/clients/{id}", s.handleDeleteClient)
			r.Post("/clients/{id}/rotate-key", s.handleRotateClientKey)
			r.Put("/clients/{id}/upstreams", s.handleSetClientUpstreams)

			r.Get("/tasks", s.handleAdminListTasks)
			r.Post("/backups/upload", s.handleAdminUploadBackup)
			r.Get("/tasks/{id}", s.handleAdminGetTask)
			r.Post("/tasks/{id}/retry", s.handleAdminRetryTask)
			r.Get("/tasks/{id}/download", s.handleAdminDownloadTask)

			r.Get("/cron-jobs", s.handleListCronJobs)
			r.Put("/cron-jobs/{id}", s.handleUpdateCronJob)
			r.Post("/cron-jobs/{id}/run", s.handleRunCronJob)
			r.Get("/cron-jobs/{id}/runs", s.handleListCronJobRuns)

			r.Get("/audit-logs", s.handleListAuditLogs)
			r.Get("/audit-logs/export", s.handleExportAuditLogs)

			r.Get("/settings", s.handleGetSettings)
			r.Put("/settings", s.handleUpdateSettings)
			r.Get("/settings/smtp", s.handleGetSMTPSettings)
			r.Put("/settings/smtp", s.handleUpdateSMTPSettings)
			r.Post("/settings/smtp/test", s.handleTestSMTPSettings)
			r.Post("/circuit-breaker/reset", s.handleResetCircuitBreaker)

			r.Get("/admins", s.handleListAdmins)
			r.Post("/admins", s.handleCreateAdmin)
			r.Put("/admins/{id}", s.handleUpdateAdmin)

			r.Post("/migration/export", s.handleMigrationExport)
			r.Post("/migration/inspect", s.handleMigrationInspect)
			r.Post("/migration/import", s.handleMigrationImport)
		})
	})

	// Embedded React SPA
	r.NotFound(s.handleSPA)
	return r
}

func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if err := s.store.Ping(r.Context()); err != nil {
		writeError(w, http.StatusServiceUnavailable, "db_unready", err.Error())
		return
	}
	settings, err := s.store.GetSystemSettings(r.Context())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unready", err.Error())
		return
	}
	if err := os.MkdirAll(settings.SpoolDir, 0o750); err != nil {
		writeError(w, http.StatusServiceUnavailable, "spool_unready", err.Error())
		return
	}
	tmp, err := os.CreateTemp(settings.SpoolDir, ".readyz-*")
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "spool_unwritable", err.Error())
		return
	}
	name := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(name)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// SetCORSOrigins configures additional allowed CORS origins (from config.toml or env).
func (s *Server) SetCORSOrigins(origins []string) {
	s.corsOrigins = append([]string(nil), origins...)
}

func (s *Server) isAllowedOrigin(r *http.Request, origin string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	if strings.EqualFold(u.Host, r.Host) {
		return true
	}
	if settings, err := s.store.GetSystemSettings(r.Context()); err == nil && settings.PublicBaseURL != "" {
		if pub, pErr := url.Parse(settings.PublicBaseURL); pErr == nil && strings.EqualFold(pub.Scheme+"://"+pub.Host, u.Scheme+"://"+u.Host) {
			return true
		}
	}
	for _, item := range s.corsOrigins {
		if strings.EqualFold(strings.TrimSpace(item), origin) {
			return true
		}
	}
	if extra := os.Getenv("SAFEKEEP_CORS_ORIGINS"); extra != "" {
		for _, item := range strings.Split(extra, ",") {
			if strings.EqualFold(strings.TrimSpace(item), origin) {
				return true
			}
		}
	}
	return false
}

func (s *Server) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Add("Vary", "Origin")
			if s.isAllowedOrigin(r, origin) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-API-Key, Idempotency-Key, X-File-Name, X-Content-SHA256, X-Backup-Mode, X-Backup-Targets, X-Backup-Path, X-Skip-Dedup")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			}
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) clientAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawKey := strings.TrimSpace(r.Header.Get("X-API-Key"))
		if rawKey == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				rawKey = strings.TrimSpace(auth[7:])
			}
		}
		if rawKey == "" {
			writeError(w, http.StatusUnauthorized, "missing_api_key", "X-API-Key or Bearer token is required")
			return
		}

		client, err := s.store.GetClientByKeyHash(r.Context(), crypto.HashAPIKey(rawKey))
		if err != nil {
			if errors.Is(err, db.ErrNotFound) {
				writeError(w, http.StatusUnauthorized, "invalid_api_key", "invalid API key")
				return
			}
			writeError(w, http.StatusInternalServerError, "auth_error", err.Error())
			return
		}
		if !client.Enabled {
			writeError(w, http.StatusForbidden, "client_disabled", "this API key is disabled")
			return
		}
		if client.ExpiresAt != nil && time.Now().UTC().After(*client.ExpiresAt) {
			writeError(w, http.StatusForbidden, "api_key_expired", "this API key has expired")
			return
		}
		if !s.allowClientRequest(client.ID, client.RateLimitRPM) {
			writeError(w, http.StatusTooManyRequests, "rate_limited", "rate limit exceeded")
			return
		}

		s.store.TouchClientLastUsed(r.Context(), client.ID)
		ctx := context.WithValue(r.Context(), ctxClientKey, client)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) allowClientRequest(clientID string, rpm int) bool {
	if rpm <= 0 {
		return true
	}
	now := time.Now().UTC()
	s.rateMu.Lock()
	defer s.rateMu.Unlock()
	w, ok := s.rateBuckets[clientID]
	if !ok || now.Sub(w.windowStart) >= time.Minute {
		s.rateBuckets[clientID] = &rateWindow{windowStart: now, count: 1}
		return true
	}
	if w.count >= rpm {
		return false
	}
	w.count++
	return true
}

func (s *Server) adminAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var tokStr string
		if c, err := r.Cookie(adminCookieName); err == nil {
			tokStr = c.Value
		}
		if tokStr == "" {
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				tokStr = strings.TrimSpace(auth[7:])
			}
		}
		if tokStr == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "admin login required")
			return
		}

		claims := jwt.MapClaims{}
		tok, err := jwt.ParseWithClaims(tokStr, claims, func(token *jwt.Token) (any, error) {
			if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return s.jwtSecret, nil
		})
		if err != nil || !tok.Valid {
			writeError(w, http.StatusUnauthorized, "invalid_token", "session expired or invalid")
			return
		}
		adminID, _ := claims["sub"].(string)
		admin, err := s.store.GetAdminByID(r.Context(), adminID)
		if err != nil || !admin.IsActive {
			writeError(w, http.StatusUnauthorized, "unauthorized", "admin account not active")
			return
		}
		pwdV, _ := claims["pwd_v"].(string)
		if pwdV != passwordVersion(admin.PasswordHash) {
			writeError(w, http.StatusUnauthorized, "session_revoked", "session revoked due to credential change")
			return
		}
		ctx := context.WithValue(r.Context(), ctxAdminKey, admin)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func passwordVersion(hash string) string {
	sum := sha256.Sum256([]byte(hash))
	return hex.EncodeToString(sum[:8])
}

func (s *Server) signAdminToken(admin *db.Admin) (string, error) {
	now := time.Now().UTC()
	claims := jwt.MapClaims{
		"sub":      admin.ID,
		"username": admin.Username,
		"pwd_v":    passwordVersion(admin.PasswordHash),
		"iat":      now.Unix(),
		"exp":      now.Add(24 * time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return tok.SignedString(s.jwtSecret)
}

func currentAdmin(r *http.Request) *db.Admin {
	a, _ := r.Context().Value(ctxAdminKey).(*db.Admin)
	return a
}

func currentClient(r *http.Request) *db.Client {
	c, _ := r.Context().Value(ctxClientKey).(*db.Client)
	return c
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]string{
			"code":    code,
			"message": msg,
		},
	})
}

func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.Split(xff, ",")[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}

func (s *Server) handleSPA(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/admin/api/") {
		writeError(w, http.StatusNotFound, "not_found", "endpoint not found")
		return
	}
	if s.webFS == nil {
		writeError(w, http.StatusNotFound, "no_ui", "web UI not bundled")
		return
	}

	reqPath := strings.TrimPrefix(r.URL.Path, "/")
	if reqPath == "" {
		reqPath = "index.html"
	}

	if f, err := s.webFS.Open(reqPath); err == nil {
		stat, statErr := f.Stat()
		_ = f.Close()
		if statErr == nil && !stat.IsDir() {
			http.FileServer(http.FS(s.webFS)).ServeHTTP(w, r)
			return
		}
	}

	// SPA fallback to index.html
	indexBytes, err := fs.ReadFile(s.webFS, "index.html")
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "index.html missing")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(indexBytes)
}
