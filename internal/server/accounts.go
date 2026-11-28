package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"safekeep/internal/audit"
	"safekeep/internal/crypto"
	"safekeep/internal/db"
)

const adminSessionTTL = 24 * time.Hour

func adminTokenFromRequest(r *http.Request) string {
	if cookie, err := r.Cookie(adminCookieName); err == nil && cookie.Value != "" {
		return cookie.Value
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[7:])
	}
	return ""
}

func adminTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func (s *Server) createAdminSession(r *http.Request, admin *db.Admin) (string, error) {
	now := time.Now().UTC().Truncate(time.Second)
	session := &db.AdminSession{ID: crypto.NewULID(), AdminID: admin.ID, ClientIP: clientIP(r), UserAgent: r.UserAgent(),
		CreatedAt: now, LastSeenAt: now, ExpiresAt: now.Add(adminSessionTTL)}
	token, err := s.signAdminToken(admin, session)
	if err != nil {
		return "", err
	}
	session.TokenHash = adminTokenHash(token)
	if err := s.store.CreateAdminSession(r.Context(), session, admin); err != nil {
		return "", err
	}
	return token, nil
}

func currentAdminSession(r *http.Request) *db.AdminSession {
	session, _ := r.Context().Value(ctxSessionKey).(*db.AdminSession)
	return session
}

// accountPermissionMiddleware 对只读账号采用明确的路由白名单，未声明的接口默认拒绝。
func (s *Server) accountPermissionMiddleware(next http.Handler) http.Handler {
	readable := chi.NewRouter()
	for _, path := range []string{
		"/auth/sessions", "/dashboard/summary", "/upstreams", "/proxies", "/clients",
		"/tasks", "/tasks/{id}", "/tasks/{id}/download", "/cron-jobs", "/cron-jobs/{id}/runs",
		"/audit-logs", "/audit-logs/export", "/remote-backups/hosts", "/remote-backups/jobs",
		"/remote-backups/runs", "/remote-backups/runs/{id}",
	} {
		readable.Get(path, func(http.ResponseWriter, *http.Request) {})
	}
	readable.Delete("/auth/sessions/{id}", func(http.ResponseWriter, *http.Request) {})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		admin := currentAdmin(r)
		if admin.Role != db.RoleAdmin && !(admin.Role == db.RoleViewer && readable.Match(chi.NewRouteContext(), r.Method, strings.TrimPrefix(r.URL.Path, "/admin/api/v1"))) {
			writeError(w, http.StatusForbidden, "forbidden", "this account has read-only access")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type adminSessionView struct {
	ID         string    `json:"id"`
	ClientIP   string    `json:"client_ip"`
	UserAgent  string    `json:"user_agent"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	ExpiresAt  time.Time `json:"expires_at"`
	Current    bool      `json:"current"`
}

func (s *Server) handleListAdminSessions(w http.ResponseWriter, r *http.Request) {
	sessions, err := s.store.ListAdminSessions(r.Context(), currentAdmin(r).ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", "unable to load sessions")
		return
	}
	out := make([]adminSessionView, 0, len(sessions))
	for _, session := range sessions {
		out = append(out, adminSessionView{ID: session.ID, ClientIP: session.ClientIP, UserAgent: session.UserAgent,
			CreatedAt: session.CreatedAt, LastSeenAt: session.LastSeenAt, ExpiresAt: session.ExpiresAt,
			Current: session.ID == currentAdminSession(r).ID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

func (s *Server) handleRevokeAdminSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteAdminSession(r.Context(), currentAdmin(r).ID, id); err != nil {
		writeError(w, http.StatusInternalServerError, "session_error", "unable to revoke session")
		return
	}
	if id == currentAdminSession(r).ID {
		s.clearAdminCookie(w, r)
	}
	s.auditLog.Record(r.Context(), audit.Event{ActorType: "admin", ActorID: currentAdmin(r).ID,
		ActorName: currentAdmin(r).Username, Action: "auth.session_revoke", ResourceType: "session", ResourceID: id,
		IPAddress: clientIP(r), UserAgent: r.UserAgent(), RequestID: middleware.GetReqID(r.Context())})
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) verifyAccountChange(w http.ResponseWriter, r *http.Request, password string) bool {
	actor := currentAdmin(r)
	if actor.Role != db.RoleAdmin {
		writeError(w, http.StatusForbidden, "forbidden", "administrator access required")
		return false
	}
	if !crypto.VerifyPassword(actor.PasswordHash, password) {
		writeError(w, http.StatusForbidden, "invalid_password", "current password is incorrect")
		return false
	}
	return true
}

func writeAccountError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, db.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "account not found")
	case errors.Is(err, db.ErrLastAdmin):
		writeError(w, http.StatusConflict, "last_admin", "at least one active administrator is required")
	default:
		writeError(w, http.StatusInternalServerError, "account_error", "unable to update account")
	}
}
