package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"safekeep/internal/notify"
)

const DefaultListenAddr = ":8333"
const DefaultPublicBaseURL = "http://localhost:8333"

// BootstrapConfig holds startup configuration resolved from defaults, config.toml, env vars, and CLI flags.
type BootstrapConfig struct {
	ConfigPath         string
	ListenAddr         string
	DataDir            string
	MasterKeyHex       string
	CORSAllowedOrigins []string
	Defaults           SystemSettings
	SMTPDefaults       notify.SMTPConfig
	HasSMTPSection     bool
}

// FileConfig maps the config.toml file structure.
type FileConfig struct {
	Server struct {
		Listen             string   `toml:"listen"`
		DataDir            string   `toml:"data_dir"`
		MasterKey          string   `toml:"master_key"`
		CORSAllowedOrigins []string `toml:"cors_allowed_origins"`
	} `toml:"server"`
	Defaults struct {
		PublicBaseURL string `toml:"public_base_url"`
		SpoolDir      string `toml:"spool_dir"`
	} `toml:"defaults"`
	SMTP notify.SMTPConfig `toml:"smtp"`
}

// SystemSettings holds dynamic runtime settings managed via the Web UI and stored in SQLite.
type SystemSettings struct {
	SpoolDir               string `json:"spool_dir"`
	MaxFileSizeBytes       int64  `json:"max_file_size_bytes"`
	DailyTrafficLimitBytes int64  `json:"daily_traffic_limit_bytes"`
	MaxSpoolBytes          int64  `json:"max_spool_bytes"`
	WorkerConcurrency      int    `json:"worker_concurrency"`
	SyncTimeoutSec         int    `json:"sync_timeout_sec"`
	MaxRetryAttempts       int    `json:"max_retry_attempts"`
	PublicBaseURL          string `json:"public_base_url"`
}

// DefaultSystemSettings returns sensible out-of-the-box settings for the given data directory.
func DefaultSystemSettings(dataDir string) SystemSettings {
	return SystemSettings{
		SpoolDir:               filepath.Join(dataDir, "spool"),
		MaxFileSizeBytes:       100 * 1024 * 1024,       // 100 MiB
		DailyTrafficLimitBytes: 1024 * 1024 * 1024,      // 1 GiB
		MaxSpoolBytes:          10 * 1024 * 1024 * 1024, // 10 GiB
		WorkerConcurrency:      4,
		SyncTimeoutSec:         60,
		MaxRetryAttempts:       3,
		PublicBaseURL:          DefaultPublicBaseURL,
	}
}

// Load resolves configuration with precedence:
// CLI flags > Environment variables (SAFEKEEP_*) > config.toml > built-in defaults.
func Load(explicitConfigPath string, cliOverrides map[string]string) (BootstrapConfig, error) {
	boot := BootstrapConfig{
		ListenAddr: DefaultListenAddr,
		DataDir:    "./data",
	}

	cfgPath := strings.TrimSpace(explicitConfigPath)
	explicit := cfgPath != ""
	if !explicit {
		if envPath := strings.TrimSpace(os.Getenv("SAFEKEEP_CONFIG")); envPath != "" {
			cfgPath = envPath
			explicit = true
		} else if _, err := os.Stat("config.toml"); err == nil {
			cfgPath = "config.toml"
		}
	}

	fc := FileConfig{SMTP: notify.DefaultSMTPConfig()}
	if cfgPath != "" {
		if _, err := os.Stat(cfgPath); err != nil {
			if explicit {
				return boot, fmt.Errorf("config file %q: %w", cfgPath, err)
			}
		} else {
			if _, err := toml.DecodeFile(cfgPath, &fc); err != nil {
				return boot, fmt.Errorf("parse %q: %w", cfgPath, err)
			}
			boot.ConfigPath = cfgPath
		}
	}

	// 1. Apply config.toml server section
	if strings.TrimSpace(fc.Server.Listen) != "" {
		boot.ListenAddr = strings.TrimSpace(fc.Server.Listen)
	}
	if strings.TrimSpace(fc.Server.DataDir) != "" {
		boot.DataDir = strings.TrimSpace(fc.Server.DataDir)
	}
	if strings.TrimSpace(fc.Server.MasterKey) != "" {
		boot.MasterKeyHex = strings.TrimSpace(fc.Server.MasterKey)
	}
	if len(fc.Server.CORSAllowedOrigins) > 0 {
		boot.CORSAllowedOrigins = append(boot.CORSAllowedOrigins, fc.Server.CORSAllowedOrigins...)
	}

	// 2. Apply environment overrides for server bootstrap
	if v, ok := os.LookupEnv("SAFEKEEP_LISTEN"); ok && strings.TrimSpace(v) != "" {
		boot.ListenAddr = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("SAFEKEEP_DATA_DIR"); ok && strings.TrimSpace(v) != "" {
		boot.DataDir = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("SAFEKEEP_MASTER_KEY"); ok && strings.TrimSpace(v) != "" {
		boot.MasterKeyHex = strings.TrimSpace(v)
	}
	if v, ok := os.LookupEnv("SAFEKEEP_CORS_ORIGINS"); ok {
		boot.CORSAllowedOrigins = parseCSV(v)
	}

	// 3. Apply CLI flag overrides (highest precedence)
	if v, ok := cliOverrides["listen"]; ok && strings.TrimSpace(v) != "" {
		boot.ListenAddr = strings.TrimSpace(v)
	}
	if v, ok := cliOverrides["data-dir"]; ok && strings.TrimSpace(v) != "" {
		boot.DataDir = strings.TrimSpace(v)
	}
	if v, ok := cliOverrides["master-key"]; ok && strings.TrimSpace(v) != "" {
		boot.MasterKeyHex = strings.TrimSpace(v)
	}

	// 4. Build initial SystemSettings defaults from config.toml + env
	// Note: upload limits, circuit breaker, and task scheduling params are managed solely in SQLite / Web UI.
	defaults := DefaultSystemSettings(boot.DataDir)
	if strings.TrimSpace(fc.Defaults.PublicBaseURL) != "" {
		defaults.PublicBaseURL = strings.TrimSpace(fc.Defaults.PublicBaseURL)
	}
	if strings.TrimSpace(fc.Defaults.SpoolDir) != "" {
		defaults.SpoolDir = strings.TrimSpace(fc.Defaults.SpoolDir)
	}

	defaults, _ = ApplyEnvOverrides(defaults)
	boot.Defaults = defaults

	smtpCfg := fc.SMTP
	if smtpCfg.Port == 0 {
		smtpCfg.Port = 587
	}
	if smtpCfg.TLSMode == "" {
		smtpCfg.TLSMode = "starttls"
	}
	if smtpCfg.FromName == "" {
		smtpCfg.FromName = "SafeKeep"
	}
	smtpCfg, smtpChanged := ApplySMTPEnvOverrides(smtpCfg)
	smtpCfg.Notifications = smtpCfg.Notifications.Normalize()
	boot.SMTPDefaults = smtpCfg
	boot.HasSMTPSection = smtpChanged || smtpCfg.Enabled || smtpCfg.Host != ""
	return boot, nil
}

// ApplyEnvOverrides applies explicit deployment SAFEKEEP_* environment variables onto SystemSettings
// (public_base_url and spool_dir) and reports whether any override variable was set.
// Upload limits, circuit breaker, and task scheduling parameters are configured exclusively in the database / Web UI.
func ApplyEnvOverrides(s SystemSettings) (SystemSettings, bool) {
	changed := false
	if v, ok := os.LookupEnv("SAFEKEEP_PUBLIC_BASE_URL"); ok && strings.TrimSpace(v) != "" {
		s.PublicBaseURL = strings.TrimSpace(v)
		changed = true
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SPOOL_DIR"); ok && strings.TrimSpace(v) != "" {
		s.SpoolDir = strings.TrimSpace(v)
		changed = true
	}
	return s, changed
}

// ApplySMTPEnvOverrides applies SAFEKEEP_SMTP_* environment variables onto SMTPConfig.
func ApplySMTPEnvOverrides(c notify.SMTPConfig) (notify.SMTPConfig, bool) {
	changed := false
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_ENABLED"); ok {
		c.Enabled = strings.EqualFold(strings.TrimSpace(v), "true") || strings.TrimSpace(v) == "1"
		changed = true
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_HOST"); ok && strings.TrimSpace(v) != "" {
		c.Host = strings.TrimSpace(v)
		changed = true
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_PORT"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			c.Port = n
			changed = true
		}
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_TLS_MODE"); ok && strings.TrimSpace(v) != "" {
		c.TLSMode = strings.ToLower(strings.TrimSpace(v))
		changed = true
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_USERNAME"); ok {
		c.Username = strings.TrimSpace(v)
		changed = true
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_PASSWORD"); ok {
		c.Password = v
		changed = true
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_FROM_EMAIL"); ok && strings.TrimSpace(v) != "" {
		c.FromEmail = strings.TrimSpace(v)
		changed = true
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_FROM_NAME"); ok && strings.TrimSpace(v) != "" {
		c.FromName = strings.TrimSpace(v)
		changed = true
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SMTP_TO_EMAILS"); ok {
		c.ToEmails = parseCSV(v)
		changed = true
	}
	return c, changed
}

func parseCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// EnsureMasterKey loads the 32-byte master key from env/config or <dataDir>/master.key,
// generating and persisting one with 0600 permissions on first boot.
func EnsureMasterKey(dataDir, configuredKey string) ([]byte, error) {
	if err := os.MkdirAll(dataDir, 0o750); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}

	if strings.TrimSpace(configuredKey) != "" {
		return decodeKeyMaterial(configuredKey)
	}

	keyPath := filepath.Join(dataDir, "master.key")
	raw, err := os.ReadFile(keyPath)
	if err == nil {
		key, decErr := decodeKeyMaterial(string(raw))
		if decErr != nil {
			return nil, fmt.Errorf("decode %s: %w", keyPath, decErr)
		}
		return key, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read %s: %w", keyPath, err)
	}

	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate master key: %w", err)
	}
	encoded := hex.EncodeToString(key) + "\n"
	if err := os.WriteFile(keyPath, []byte(encoded), 0o600); err != nil {
		return nil, fmt.Errorf("persist master key: %w", err)
	}
	return key, nil
}

func decodeKeyMaterial(s string) ([]byte, error) {
	trimmed := strings.TrimSpace(s)
	if len(trimmed) == 64 {
		b, err := hex.DecodeString(trimmed)
		if err == nil && len(b) == 32 {
			return b, nil
		}
	}
	if b, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(b) == 32 {
		return b, nil
	}
	if b, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil && len(b) == 32 {
		return b, nil
	}
	return nil, errors.New("master key must be 32 bytes (64 hex chars or base64)")
}
