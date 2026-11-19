package config

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// BootstrapConfig holds startup configuration resolved from flags and environment variables.
type BootstrapConfig struct {
	ListenAddr   string
	DataDir      string
	MasterKeyHex string
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
		PublicBaseURL:     "http://localhost:8080",
	}
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
