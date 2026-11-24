package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTOMLAndEnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	content := `
[server]
listen = ":8333"
data_dir = "/tmp/from-toml"
cors_allowed_origins = ["https://from-toml.example"]

[defaults]
public_base_url = "http://localhost:8333"
spool_dir = "/tmp/from-toml/spool"
worker_concurrency = 6
sync_timeout_sec = 45
`
	if err := os.WriteFile(cfgFile, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// 1. Loaded from TOML directly (upload/circuit/scheduler params stay at built-in DB defaults)
	boot, err := Load(cfgFile, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if boot.ListenAddr != ":8333" || boot.DataDir != "/tmp/from-toml" || boot.Defaults.SpoolDir != "/tmp/from-toml/spool" || boot.Defaults.WorkerConcurrency != 4 || boot.Defaults.SyncTimeoutSec != 60 {
		t.Fatalf("unexpected toml config: %+v", boot)
	}

	// 2. Environment variables override deployment settings, while DB-managed limits/scheduler ignore env vars
	t.Setenv("SAFEKEEP_LISTEN", ":9333")
	t.Setenv("SAFEKEEP_DATA_DIR", "/data")
	t.Setenv("SAFEKEEP_SPOOL_DIR", "/data/spool")
	t.Setenv("SAFEKEEP_WORKER_CONCURRENCY", "12")
	t.Setenv("SAFEKEEP_MAX_FILE_SIZE_BYTES", "999")
	t.Setenv("SAFEKEEP_PUBLIC_BASE_URL", "https://backup.example.com")
	t.Setenv("SAFEKEEP_CORS_ORIGINS", "https://ui.example.com")

	boot2, err := Load(cfgFile, nil)
	if err != nil {
		t.Fatalf("Load with env: %v", err)
	}
	if boot2.ListenAddr != ":9333" || boot2.DataDir != "/data" || boot2.Defaults.SpoolDir != "/data/spool" || boot2.Defaults.WorkerConcurrency != 4 || boot2.Defaults.MaxFileSizeBytes != 100*1024*1024 || boot2.Defaults.PublicBaseURL != "https://backup.example.com" {
		t.Fatalf("expected deployment env overrides without overriding DB-managed defaults, got %+v", boot2)
	}
	if len(boot2.CORSAllowedOrigins) != 1 || boot2.CORSAllowedOrigins[0] != "https://ui.example.com" {
		t.Fatalf("expected SAFEKEEP_CORS_ORIGINS override, got %+v", boot2.CORSAllowedOrigins)
	}

	// 3. CLI flags override environment variables
	boot3, err := Load(cfgFile, map[string]string{"listen": ":7333"})
	if err != nil {
		t.Fatalf("Load with CLI: %v", err)
	}
	if boot3.ListenAddr != ":7333" {
		t.Fatalf("expected CLI override :7333, got %q", boot3.ListenAddr)
	}
}

func TestDefaultPortIs8333(t *testing.T) {
	boot, err := Load("", nil)
	if err != nil {
		t.Fatalf("Load defaults: %v", err)
	}
	if boot.ListenAddr != ":8333" || boot.Defaults.PublicBaseURL != "http://localhost:8333" {
		t.Fatalf("expected default port 8333, got listen=%q base=%q", boot.ListenAddr, boot.Defaults.PublicBaseURL)
	}
	if boot.Defaults.MaxFileSizeBytes != 100*1024*1024 {
		t.Fatalf("expected default max_file_size_bytes=100MiB, got %d", boot.Defaults.MaxFileSizeBytes)
	}
	if boot.Defaults.DailyTrafficLimitBytes != 1024*1024*1024 {
		t.Fatalf("expected default daily_traffic_limit_bytes=1GiB, got %d", boot.Defaults.DailyTrafficLimitBytes)
	}
}

func TestLoadResetAdmin(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(cfgFile, []byte("[server]\nreset_admin = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	boot, err := Load(cfgFile, nil)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !boot.ResetAdmin {
		t.Fatalf("expected reset_admin=true from config.toml")
	}

	// RESET_ADMIN=false overrides config.toml reset_admin=true
	t.Setenv("RESET_ADMIN", "false")
	boot, err = Load(cfgFile, nil)
	if err != nil {
		t.Fatalf("Load with RESET_ADMIN=false: %v", err)
	}
	if boot.ResetAdmin {
		t.Fatalf("expected RESET_ADMIN=false to override config.toml")
	}

	// RESET_ADMIN=true enables reset_admin without config.toml setting
	t.Setenv("RESET_ADMIN", "true")
	boot, err = Load("", nil)
	if err != nil {
		t.Fatalf("Load with RESET_ADMIN=true: %v", err)
	}
	if !boot.ResetAdmin {
		t.Fatalf("expected RESET_ADMIN=true to enable reset_admin")
	}

	// SAFEKEEP_RESET_ADMIN takes precedence over RESET_ADMIN
	t.Setenv("SAFEKEEP_RESET_ADMIN", "0")
	boot, err = Load("", nil)
	if err != nil {
		t.Fatalf("Load with SAFEKEEP_RESET_ADMIN=0: %v", err)
	}
	if boot.ResetAdmin {
		t.Fatalf("expected SAFEKEEP_RESET_ADMIN=0 to disable reset_admin")
	}
}
