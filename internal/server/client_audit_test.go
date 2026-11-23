package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"safekeep/internal/db"
)

func TestDeleteClientAuditIncludesName(t *testing.T) {
	env := setupTestEnv(t)

	initResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/setup/init", map[string]string{
		"username": "admin",
		"password": "password123",
	})
	initResp.Body.Close()
	if initResp.StatusCode != http.StatusCreated {
		t.Fatalf("setup init expected 201, got %d", initResp.StatusCode)
	}

	createResp := env.adminReq(t, http.MethodPost, "/admin/api/v1/clients", map[string]any{
		"name":         "acme-client",
		"enabled":      true,
		"default_mode": "async",
	})
	defer createResp.Body.Close()
	rawCreate, err := io.ReadAll(createResp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if createResp.StatusCode != http.StatusCreated {
		t.Fatalf("create client expected 201, got %d: %s", createResp.StatusCode, rawCreate)
	}
	var created struct {
		Client clientDTO `json:"client"`
	}
	if err := json.Unmarshal(rawCreate, &created); err != nil {
		t.Fatal(err)
	}

	delResp := env.adminReq(t, http.MethodDelete, "/admin/api/v1/clients/"+created.Client.ID, nil)
	delResp.Body.Close()
	if delResp.StatusCode != http.StatusOK {
		t.Fatalf("delete client expected 200, got %d", delResp.StatusCode)
	}

	missingResp := env.adminReq(t, http.MethodDelete, "/admin/api/v1/clients/"+created.Client.ID, nil)
	missingResp.Body.Close()
	if missingResp.StatusCode != http.StatusNotFound {
		t.Fatalf("delete missing client expected 404, got %d", missingResp.StatusCode)
	}

	logs, total, err := env.store.ListAuditLogs(context.Background(), db.AuditFilter{
		Action: "client.delete",
		Limit:  10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 || len(logs) != 1 {
		t.Fatalf("expected 1 client.delete audit log, got total=%d len=%d", total, len(logs))
	}
	if logs[0].ResourceID != created.Client.ID {
		t.Fatalf("expected resource_id %q, got %q", created.Client.ID, logs[0].ResourceID)
	}

	var detail struct {
		Name      string `json:"name"`
		KeyPrefix string `json:"key_prefix"`
	}
	if err := json.Unmarshal([]byte(logs[0].DetailJSON), &detail); err != nil {
		t.Fatalf("invalid detail_json %q: %v", logs[0].DetailJSON, err)
	}
	if detail.Name != "acme-client" {
		t.Fatalf("expected audit detail name %q, got %q (raw=%s)", "acme-client", detail.Name, logs[0].DetailJSON)
	}
	if detail.KeyPrefix != created.Client.KeyPrefix {
		t.Fatalf("expected audit detail key_prefix %q, got %q", created.Client.KeyPrefix, detail.KeyPrefix)
	}
}
