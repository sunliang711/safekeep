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
		PublicBaseURL     string `toml:"public_base_url"`
		SpoolDir          string `toml:"spool_dir"`
		MaxFileSizeBytes  int64  `toml:"max_file_size_bytes"`
		MaxSpoolBytes     int64  `toml:"max_spool_bytes"`
		WorkerConcurrency int    `toml:"worker_concurrency"`
		SyncTimeoutSec    int    `toml:"sync_timeout_sec"`
		MaxRetryAttempts  int    `toml:"max_retry_attempts"`
	} `toml:"defaults"`
}

// SystemSettings holds dynamic runtime settings managed via the Web UI and stored in SQLite.
type SystemSettings struct {
	SpoolDir          string `json:"spool_dir"`
	MaxFileSizeBytes  int64  `json:"max_file_size_bytes"`
	MaxSpoolBytes     int64  `json:"max_spool_bytes"`
	WorkerConcurrency int    `json:"worker_concurrency"`
	SyncTimeoutSec    int    `json:"sync_timeout_sec"`
	MaxRetryAttempts  int    `json:"max_retry_attempts"`
	PublicBaseURL     string `json:"public_base_url"`
}

// DefaultSystemSettings returns sensible out-of-the-box settings for the given data directory.
func DefaultSystemSettings(dataDir string) SystemSettings {
	return SystemSettings{
		SpoolDir:          filepath.Join(dataDir, "spool"),
		MaxFileSizeBytes:  512 * 1024 * 1024,        // 512 MiB
		MaxSpoolBytes:     10 * 1024 * 1024 * 1024, // 10 GiB
		WorkerConcurrency: 4,
		SyncTimeoutSec:    60,
		MaxRetryAttempts:  3,
		PublicBaseURL:     DefaultPublicBaseURL,
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

	var fc FileConfig
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
	defaults := DefaultSystemSettings(boot.DataDir)
	if strings.TrimSpace(fc.Defaults.PublicBaseURL) != "" {
		defaults.PublicBaseURL = strings.TrimSpace(fc.Defaults.PublicBaseURL)
	}
	if strings.TrimSpace(fc.Defaults.SpoolDir) != "" {
		defaults.SpoolDir = strings.TrimSpace(fc.Defaults.SpoolDir)
	}
	if fc.Defaults.MaxFileSizeBytes > 0 {
		defaults.MaxFileSizeBytes = fc.Defaults.MaxFileSizeBytes
	}
	if fc.Defaults.MaxSpoolBytes > 0 {
		defaults.MaxSpoolBytes = fc.Defaults.MaxSpoolBytes
	}
	if fc.Defaults.WorkerConcurrency > 0 {
		defaults.WorkerConcurrency = fc.Defaults.WorkerConcurrency
	}
	if fc.Defaults.SyncTimeoutSec > 0 {
		defaults.SyncTimeoutSec = fc.Defaults.SyncTimeoutSec
	}
	if fc.Defaults.MaxRetryAttempts > 0 {
		defaults.MaxRetryAttempts = fc.Defaults.MaxRetryAttempts
	}

	defaults, _ = ApplyEnvOverrides(defaults)
	boot.Defaults = defaults
	return boot, nil
}

// ApplyEnvOverrides applies explicit SAFEKEEP_* environment variables onto SystemSettings
// and reports whether any runtime override variable was set.
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
	if v, ok := os.LookupEnv("SAFEKEEP_MAX_FILE_SIZE_BYTES"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
			s.MaxFileSizeBytes = n
			changed = true
		}
	}
	if v, ok := os.LookupEnv("SAFEKEEP_MAX_SPOOL_BYTES"); ok {
		if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n >= 0 {
			s.MaxSpoolBytes = n
			changed = true
		}
	}
	if v, ok := os.LookupEnv("SAFEKEEP_WORKER_CONCURRENCY"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			s.WorkerConcurrency = n
			changed = true
		}
	}
	if v, ok := os.LookupEnv("SAFEKEEP_SYNC_TIMEOUT_SEC"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			s.SyncTimeoutSec = n
			changed = true
		}
	}
	if v, ok := os.LookupEnv("SAFEKEEP_MAX_RETRY_ATTEMPTS"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			s.MaxRetryAttempts = n
			changed = true
		}
	}
	return s, changed
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
