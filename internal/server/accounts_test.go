package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"safekeep/internal/db"
)

type accountTestClient struct {
	env     *testEnv
	cookies []*http.Cookie
}

func (c *accountTestClient) request(t *testing.T, method, path string, body any, want int) []byte {
	t.Helper()
	var input io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		input = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, c.env.srv.URL+"/admin/api/v1"+path, input)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "SafeKeep-session-test")
	for _, cookie := range c.cookies {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != want {
		t.Fatalf("%s %s: got %d, want %d: %s", method, path, resp.StatusCode, want, data)
	}
	if cookies := resp.Cookies(); len(cookies) > 0 {
		c.cookies = cookies
	}
	return data
}

func accountFixture(t *testing.T) (*testEnv, *accountTestClient) {
	t.Helper()
	env := setupTestEnv(t)
	owner := &accountTestClient{env: env}
	owner.request(t, "POST", "/setup/init", map[string]string{"username": "owner", "password": "owner-password"}, 201)
	return env, owner
}

func (c *accountTestClient) login(t *testing.T, username, password string) {
	t.Helper()
	c.request(t, "POST", "/auth/login", map[string]string{"username": username, "password": password}, 200)
}

func (c *accountTestClient) sessions(t *testing.T) []adminSessionView {
	t.Helper()
	data := c.request(t, "GET", "/auth/sessions", nil, 200)
	if strings.Contains(string(data), "token_hash") || strings.Contains(string(data), "password_hash") {
		t.Fatal("session response contains credentials")
	}
	var result struct{ Items []adminSessionView }
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result.Items
}

func (c *accountTestClient) create(t *testing.T, username, role string) db.Admin {
	t.Helper()
	data := c.request(t, "POST", "/admins", map[string]string{"username": username, "password": "account-password", "role": role, "current_password": "owner-password"}, 201)
	var admin db.Admin
	if err := json.Unmarshal(data, &admin); err != nil {
		t.Fatal(err)
	}
	return admin
}

func TestAdminSessionLifecycle(t *testing.T) {
	env, owner := accountFixture(t)
	second := &accountTestClient{env: env}
	second.login(t, "owner", "owner-password")
	sessions := owner.sessions(t)
	if len(sessions) != 2 || sessions[0].ID == sessions[1].ID {
		t.Fatalf("expected two independent sessions: %+v", sessions)
	}
	var ownID, otherID string
	for _, session := range sessions {
		if session.Current {
			ownID = session.ID
		} else {
			otherID = session.ID
		}
		if session.UserAgent != "SafeKeep-session-test" || session.ClientIP == "" || !session.ExpiresAt.After(session.CreatedAt) {
			t.Fatalf("missing session metadata: %+v", session)
		}
	}
	if ownID == "" || otherID == "" {
		t.Fatal("current session not identified")
	}
	stolen := second.cookies[0].Value
	owner.request(t, "DELETE", "/auth/sessions/"+otherID, nil, 200)
	owner.request(t, "DELETE", "/auth/sessions/"+otherID, nil, 200)
	second.request(t, "GET", "/auth/me", nil, 401)
	req, _ := http.NewRequest("GET", env.srv.URL+"/admin/api/v1/auth/me", nil)
	req.Header.Set("Authorization", "Bearer "+stolen)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("revoked bearer token still authenticates")
	}
	if len(owner.sessions(t)) != 1 {
		t.Fatal("wrong session removed")
	}
	oldCookies := append([]*http.Cookie(nil), owner.cookies...)
	owner.request(t, "DELETE", "/auth/sessions/"+ownID, nil, 200)
	if owner.cookies[0].MaxAge >= 0 {
		t.Fatal("current session cookie was not cleared")
	}
	owner.cookies = oldCookies
	owner.request(t, "GET", "/auth/me", nil, 401)
	owner.login(t, "owner", "owner-password")
	oldCookies = append([]*http.Cookie(nil), owner.cookies...)
	owner.request(t, "POST", "/auth/logout", nil, 200)
	owner.cookies = oldCookies
	owner.request(t, "GET", "/auth/me", nil, 401)
	owner.request(t, "POST", "/auth/logout", nil, 200)
}

func TestSessionIsolationExpiryAndTouch(t *testing.T) {
	env, owner := accountFixture(t)
	viewer := owner.create(t, "reader", "viewer")
	reader := &accountTestClient{env: env}
	reader.login(t, "reader", "account-password")
	readerID := reader.sessions(t)[0].ID
	owner.request(t, "DELETE", "/auth/sessions/"+readerID, nil, 200)
	reader.request(t, "GET", "/auth/me", nil, 200)
	reader.request(t, "DELETE", "/auth/sessions/"+owner.sessions(t)[0].ID, nil, 200)
	owner.request(t, "GET", "/auth/me", nil, 200)
	past := time.Now().UTC().Add(-5 * time.Minute)
	if _, err := env.store.DB().ExecContext(context.Background(), `UPDATE admin_sessions SET last_seen_at = ? WHERE id = ?`, past, readerID); err != nil {
		t.Fatal(err)
	}
	reader.request(t, "GET", "/auth/me", nil, 200)
	stored, err := env.store.ListAdminSessions(context.Background(), viewer.ID)
	if err != nil || len(stored) != 1 || !stored[0].LastSeenAt.After(past) {
		t.Fatalf("last-seen not updated: %+v %v", stored, err)
	}
	if _, err := env.store.DB().ExecContext(context.Background(), `UPDATE admin_sessions SET expires_at = ? WHERE id = ?`, past, readerID); err != nil {
		t.Fatal(err)
	}
	stored, err = env.store.ListAdminSessions(context.Background(), viewer.ID)
	if err != nil || len(stored) != 0 {
		t.Fatalf("expired session listed: %+v %v", stored, err)
	}
	reader.request(t, "GET", "/auth/me", nil, 401)
}

func TestAccountRolesAndSecondVerification(t *testing.T) {
	env, owner := accountFixture(t)
	for _, password := range []string{"", "wrong-password"} {
		owner.request(t, "POST", "/admins", map[string]string{"username": "reader", "password": "account-password", "role": "viewer", "current_password": password}, 403)
	}
	owner.request(t, "POST", "/admins", map[string]string{"username": "reader", "password": "account-password", "role": "root", "current_password": "owner-password"}, 400)
	viewer := owner.create(t, "reader", "viewer")
	if viewer.Role != db.RoleViewer {
		t.Fatal("created account has wrong role")
	}
	reader := &accountTestClient{env: env}
	reader.login(t, "reader", "account-password")
	for _, path := range []string{"/dashboard/summary", "/tasks", "/upstreams", "/clients", "/proxies", "/cron-jobs", "/audit-logs", "/auth/sessions"} {
		reader.request(t, "GET", path, nil, 200)
	}
	for _, path := range []string{"/admins", "/settings", "/settings/smtp", "/clients/example/secrets", "/upstreams/example/secrets", "/proxies/example/secrets", "/cron-jobs/example/secrets", "/upstreams/example/oauth/dropbox/start"} {
		reader.request(t, "GET", path, nil, 403)
	}
	for _, test := range []struct{ method, path string }{
		{"POST", "/admins"}, {"PUT", "/admins/" + viewer.ID}, {"PUT", "/settings"}, {"POST", "/settings/smtp/test"},
		{"POST", "/upstreams"}, {"DELETE", "/clients/example"}, {"POST", "/clients/example/rotate-key"},
		{"POST", "/tasks/example/retry"}, {"POST", "/backups/upload"}, {"POST", "/cron-jobs/example/run"},
		{"POST", "/remote-backups/jobs/example/run"}, {"POST", "/migration/export"}, {"POST", "/migration/import"},
		{"POST", "/circuit-breaker/reset"},
	} {
		reader.request(t, test.method, test.path, map[string]string{}, 403)
	}
	owner.request(t, "PUT", "/admins/"+viewer.ID, map[string]any{"role": "admin", "current_password": "wrong"}, 403)
	owner.request(t, "PUT", "/admins/"+viewer.ID, map[string]any{"role": "admin", "current_password": "owner-password"}, 200)
	reader.request(t, "GET", "/auth/me", nil, 401)
	reader.login(t, "reader", "account-password")
	reader.request(t, "GET", "/admins", nil, 200)
	owner.request(t, "PUT", "/admins/"+viewer.ID, map[string]any{"is_active": false, "current_password": "owner-password"}, 200)
	oldCookies := append([]*http.Cookie(nil), reader.cookies...)
	reader.request(t, "GET", "/auth/me", nil, 401)
	owner.request(t, "PUT", "/admins/"+viewer.ID, map[string]any{"is_active": true, "current_password": "owner-password"}, 200)
	reader.cookies = oldCookies
	reader.request(t, "GET", "/auth/me", nil, 401)
	reader.login(t, "reader", "account-password")
	owner.request(t, "PUT", "/admins/"+viewer.ID, map[string]any{"password": "new-account-password", "current_password": "owner-password"}, 200)
	reader.request(t, "GET", "/auth/me", nil, 401)
	reader.login(t, "reader", "new-account-password")
}

func TestLastAdminAndPasswordChange(t *testing.T) {
	env, owner := accountFixture(t)
	admin, err := env.store.GetAdminByUsername(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	owner.request(t, "PUT", "/admins/"+admin.ID, map[string]string{"role": "viewer", "current_password": "owner-password"}, 409)
	owner.request(t, "PUT", "/admins/"+admin.ID, map[string]any{"is_active": false, "current_password": "owner-password"}, 400)
	second := &accountTestClient{env: env}
	second.login(t, "owner", "owner-password")
	owner.request(t, "PUT", "/auth/password", map[string]string{"old_password": "owner-password", "new_password": "new-owner-password"}, 200)
	owner.request(t, "GET", "/auth/me", nil, 200)
	second.request(t, "GET", "/auth/me", nil, 401)
	if len(owner.sessions(t)) != 1 {
		t.Fatal("password change must keep exactly one session")
	}
	owner.request(t, "PUT", "/admins/"+admin.ID, map[string]string{"password": "reset-owner-password", "current_password": "new-owner-password"}, 200)
	owner.request(t, "GET", "/auth/me", nil, 200)
	if len(owner.sessions(t)) != 1 {
		t.Fatal("own password reset must rotate the current login")
	}
}

func TestConcurrentLastAdminProtection(t *testing.T) {
	env, owner := accountFixture(t)
	other := owner.create(t, "another", "admin")
	primary, err := env.store.GetAdminByUsername(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, id := range []string{primary.ID, other.ID} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			role := db.RoleViewer
			results <- env.store.UpdateAdminAccount(context.Background(), id, &role, nil, nil)
		}(id)
	}
	wg.Wait()
	close(results)
	var success, denied int
	for err := range results {
		if err == nil {
			success++
		} else if err == db.ErrLastAdmin {
			denied++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || denied != 1 {
		t.Fatalf("last admin race: success=%d denied=%d", success, denied)
	}
}
