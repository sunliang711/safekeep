package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"safekeep/internal/db"
)

func TestBootstrapResetAdminAndForcedPasswordChange(t *testing.T) {
	env := setupTestEnv(t)

	// 1. Initialize first admin with custom username and log in
	initResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/setup/init", map[string]string{
		"username": "eagle",
		"password": "original-password-123",
	})
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup init expected 201, got %d", initResp.StatusCode)
	}
	oldCookies := append([]*http.Cookie(nil), env.cookies...)

	// Disable the admin account to verify RESET_ADMIN also reactivates it.
	createdAdmin, err := env.store.GetAdminByUsername(context.Background(), "eagle")
	if err != nil {
		t.Fatalf("GetAdminByUsername: %v", err)
	}
	if err := env.store.UpdateAdminActive(context.Background(), createdAdmin.ID, false); err != nil {
		t.Fatalf("UpdateAdminActive: %v", err)
	}

	// 2. Run BootstrapResetAdmin -> generates temporary password, forces change, revokes old session
	var out bytes.Buffer
	resetAdmin, tempPass, err := BootstrapResetAdmin(context.Background(), env.store, env.auditLog, &out)
	if err != nil {
		t.Fatalf("BootstrapResetAdmin: %v", err)
	}
	if resetAdmin.Username != "eagle" || !resetAdmin.IsActive || !resetAdmin.MustChangePassword {
		t.Fatalf("unexpected reset admin state: %+v", resetAdmin)
	}
	if !strings.HasPrefix(tempPass, "adm_") || len(tempPass) < 16 {
		t.Fatalf("unexpected temporary password format: %q", tempPass)
	}
	banner := out.String()
	if !strings.Contains(banner, "Username           : eagle") || !strings.Contains(banner, "Temporary Password : "+tempPass) {
		t.Fatalf("expected credentials banner in output, got:\n%s", banner)
	}
	env.server.SetResetAdminEnvActive(true)

	// Old session cookie must be rejected (401)
	env.cookies = oldCookies
	staleResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/auth/me", nil)
	staleResp.Body.Close()
	if staleResp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected old session revoked (401), got %d", staleResp.StatusCode)
	}

	// setup/status reports initialized=true and reset_admin_env_active=true
	env.cookies = nil
	statusResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/setup/status", nil)
	var statusOut struct {
		Initialized         bool `json:"initialized"`
		ResetAdminEnvActive bool `json:"reset_admin_env_active"`
	}
	_ = json.NewDecoder(statusResp.Body).Decode(&statusOut)
	statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK || !statusOut.Initialized || !statusOut.ResetAdminEnvActive {
		t.Fatalf("unexpected setup/status output: status=%d %+v", statusResp.StatusCode, statusOut)
	}

	// 3. Login with temporary password -> must_change_password=true
	loginResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/auth/login", map[string]string{
		"username": "eagle",
		"password": tempPass,
	})
	var loginOut struct {
		Admin               db.Admin `json:"admin"`
		ResetAdminEnvActive bool     `json:"reset_admin_env_active"`
	}
	_ = json.NewDecoder(loginResp.Body).Decode(&loginOut)
	loginResp.Body.Close()
	if loginResp.StatusCode != http.StatusOK || !loginOut.Admin.MustChangePassword || !loginOut.ResetAdminEnvActive {
		t.Fatalf("expected must_change_password=true and reset_admin_env_active=true on login, got status=%d %+v", loginResp.StatusCode, loginOut)
	}

	// /auth/me still works while must_change_password=true
	meResp := env.adminReq(t, http.MethodGet, "/admin/api/v1/auth/me", nil)
	var meOut struct {
		Admin               db.Admin `json:"admin"`
		ResetAdminEnvActive bool     `json:"reset_admin_env_active"`
	}
	_ = json.NewDecoder(meResp.Body).Decode(&meOut)
	meResp.Body.Close()
	if meResp.StatusCode != http.StatusOK || !meOut.Admin.MustChangePassword || !meOut.ResetAdminEnvActive {
		t.Fatalf("unexpected /auth/me response: status=%d %+v", meResp.StatusCode, meOut)
	}

	// Protected management endpoints must return 403 password_change_required
	dashBlocked := env.adminReq(t, http.MethodGet, "/admin/api/v1/dashboard/summary", nil)
	var errOut struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.NewDecoder(dashBlocked.Body).Decode(&errOut)
	dashBlocked.Body.Close()
	if dashBlocked.StatusCode != http.StatusForbidden || errOut.Error.Code != "password_change_required" {
		t.Fatalf("expected 403 password_change_required on dashboard before changing password, got status=%d code=%q", dashBlocked.StatusCode, errOut.Error.Code)
	}

	// 4. Forced password change succeeds without old_password
	chgResp := env.adminReq(t, http.MethodPut, "/admin/api/v1/auth/password", map[string]string{
		"old_password": "",
		"new_password": "brand-new-password-456",
	})
	var chgOut struct {
		Status              string   `json:"status"`
		Admin               db.Admin `json:"admin"`
		ResetAdminEnvActive bool     `json:"reset_admin_env_active"`
	}
	_ = json.NewDecoder(chgResp.Body).Decode(&chgOut)
	chgResp.Body.Close()
	if chgResp.StatusCode != http.StatusOK || chgOut.Admin.MustChangePassword {
		t.Fatalf("expected forced password change to succeed and clear must_change_password, got status=%d %+v", chgResp.StatusCode, chgOut)
	}

	// Protected management endpoints are now accessible
	dashOK := env.adminReq(t, http.MethodGet, "/admin/api/v1/dashboard/summary", nil)
	dashOK.Body.Close()
	if dashOK.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 on dashboard after password change, got %d", dashOK.StatusCode)
	}

	// Temporary password is invalidated; new password logs in with must_change_password=false
	env.cookies = nil
	oldTempLogin := env.adminReq(t, http.MethodPost, "/admin/api/v1/auth/login", map[string]string{
		"username": "eagle",
		"password": tempPass,
	})
	oldTempLogin.Body.Close()
	if oldTempLogin.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected temporary password rejected after change, got %d", oldTempLogin.StatusCode)
	}

	newLogin := env.adminReq(t, http.MethodPost, "/admin/api/v1/auth/login", map[string]string{
		"username": "eagle",
		"password": "brand-new-password-456",
	})
	var newLoginOut struct {
		Admin db.Admin `json:"admin"`
	}
	_ = json.NewDecoder(newLogin.Body).Decode(&newLoginOut)
	newLogin.Body.Close()
	if newLogin.StatusCode != http.StatusOK || newLoginOut.Admin.MustChangePassword {
		t.Fatalf("expected new password login with must_change_password=false, got status=%d %+v", newLogin.StatusCode, newLoginOut)
	}
}

func TestBootstrapResetAdminEmptyCreatesAdminAndPrefersAdminUsername(t *testing.T) {
	env := setupTestEnv(t)

	// When admins table is empty, BootstrapResetAdmin creates "admin"
	var out bytes.Buffer
	a, tempPass, err := BootstrapResetAdmin(context.Background(), env.store, env.auditLog, &out)
	if err != nil {
		t.Fatalf("BootstrapResetAdmin on empty db: %v", err)
	}
	if a.Username != "admin" || !a.MustChangePassword || tempPass == "" {
		t.Fatalf("expected initial admin 'admin' with must_change_password=true, got %+v", a)
	}

	// When both another earlier user and "admin" exist, prefer "admin"
	// (Clear and seed "ops" first, then "admin")
	if _, err := env.store.DB().Exec(`DELETE FROM admins`); err != nil {
		t.Fatal(err)
	}
	env.cookies = nil
	initResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/setup/init", map[string]string{
		"username": "ops",
		"password": "ops-password-123",
	})
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup init ops: %d", initResp.StatusCode)
	}
	createAdminResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/admins", map[string]string{
		"username": "admin",
		"password": "admin-password-123",
	})
	createAdminResp.Body.Close()
	if createAdminResp.StatusCode != http.StatusCreated {
		t.Fatalf("create admin: %d", createAdminResp.StatusCode)
	}

	out.Reset()
	resetAdmin, _, err := BootstrapResetAdmin(context.Background(), env.store, env.auditLog, &out)
	if err != nil {
		t.Fatalf("BootstrapResetAdmin prefer admin: %v", err)
	}
	if resetAdmin.Username != "admin" {
		t.Fatalf("expected BootstrapResetAdmin to prefer 'admin', got %q", resetAdmin.Username)
	}
}
