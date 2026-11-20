package scheduler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"safekeep/internal/crypto"
	"safekeep/internal/migration"
)

// MigrationBackupConfig defines persisted & DTO configuration for the migration_backup cron job.
type MigrationBackupConfig struct {
	APIBaseURL          string `json:"api_base_url"`
	ClientID            string `json:"client_id"`
	APIKey              string `json:"api_key,omitempty"`
	APIKeyEncrypted     string `json:"api_key_encrypted,omitempty"`
	HasAPIKey           bool   `json:"has_api_key,omitempty"`
	Targets             string `json:"targets"`
	Path                string `json:"path"`
	Mode                string `json:"mode"`
	Rename              string `json:"rename"`
	Passphrase          string `json:"passphrase,omitempty"`
	PassphraseEncrypted string `json:"passphrase_encrypted,omitempty"`
	HasPassphrase       bool   `json:"has_passphrase,omitempty"`
	IncludeAdmins       bool   `json:"include_admins"`
	IncludeTasks        bool   `json:"include_tasks"`
	IncludeAuditLogs    bool   `json:"include_audit_logs"`
}

func defaultMigrationBackupConfig() MigrationBackupConfig {
	return MigrationBackupConfig{
		Path:          "migration-backups",
		Mode:          "sync",
		IncludeAdmins: true,
	}
}

// ParseMigrationBackupConfig decodes stored config JSON with safe defaults.
func ParseMigrationBackupConfig(raw string) MigrationBackupConfig {
	cfg := defaultMigrationBackupConfig()
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	if cfg.Mode == "" {
		cfg.Mode = "sync"
	}
	return cfg
}

// SanitizeMigrationBackupConfigJSON strips encrypted/plaintext secrets and populates boolean presence flags.
func SanitizeMigrationBackupConfigJSON(raw string) string {
	cfg := ParseMigrationBackupConfig(raw)
	cfg.HasAPIKey = cfg.APIKeyEncrypted != "" || cfg.APIKey != ""
	cfg.HasPassphrase = cfg.PassphraseEncrypted != "" || cfg.Passphrase != ""
	cfg.APIKey = ""
	cfg.APIKeyEncrypted = ""
	cfg.Passphrase = ""
	cfg.PassphraseEncrypted = ""
	b, err := json.Marshal(cfg)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func isMaskedPlaceholder(v string) bool {
	return v == "*****" || v == "••••••"
}

// MergeMigrationBackupConfig validates incoming configuration and preserves/encrypts secret fields.
func MergeMigrationBackupConfig(c *crypto.Cipher, existingRaw string, incoming json.RawMessage) (string, error) {
	if c == nil {
		return "", errors.New("cipher is required")
	}
	existing := ParseMigrationBackupConfig(existingRaw)

	var req struct {
		APIBaseURL       string  `json:"api_base_url"`
		ClientID         string  `json:"client_id"`
		APIKey           *string `json:"api_key"`
		Targets          string  `json:"targets"`
		Path             string  `json:"path"`
		Mode             string  `json:"mode"`
		Rename           string  `json:"rename"`
		Passphrase       *string `json:"passphrase"`
		IncludeAdmins    *bool   `json:"include_admins"`
		IncludeTasks     *bool   `json:"include_tasks"`
		IncludeAuditLogs *bool   `json:"include_audit_logs"`
	}
	if len(incoming) > 0 {
		if err := json.Unmarshal(incoming, &req); err != nil {
			return "", fmt.Errorf("invalid migration_backup config: %w", err)
		}
	}

	baseURL := strings.TrimSpace(req.APIBaseURL)
	if baseURL != "" {
		u, err := url.Parse(baseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return "", errors.New("api_base_url must be a valid http or https URL")
		}
		baseURL = strings.TrimRight(baseURL, "/")
	}

	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "sync"
	}
	if mode != "sync" && mode != "async" {
		return "", errors.New("mode must be 'sync' or 'async'")
	}

	rename := strings.TrimSpace(req.Rename)
	if rename == "." || rename == ".." || rename == "/" {
		return "", errors.New("rename filename is invalid")
	}

	apiKeyEnc := existing.APIKeyEncrypted
	if req.APIKey != nil {
		val := strings.TrimSpace(*req.APIKey)
		if val == "" {
			apiKeyEnc = ""
		} else if !isMaskedPlaceholder(val) {
			enc, err := c.EncryptString(val)
			if err != nil {
				return "", err
			}
			apiKeyEnc = enc
		}
	}

	passEnc := existing.PassphraseEncrypted
	if req.Passphrase != nil {
		val := *req.Passphrase
		if val == "" {
			passEnc = ""
		} else if !isMaskedPlaceholder(strings.TrimSpace(val)) {
			if len(val) < 6 {
				return "", errors.New("passphrase must be at least 6 characters (or leave empty)")
			}
			enc, err := c.EncryptString(val)
			if err != nil {
				return "", err
			}
			passEnc = enc
		}
	}

	includeAdmins := existing.IncludeAdmins
	if req.IncludeAdmins != nil {
		includeAdmins = *req.IncludeAdmins
	}
	includeTasks := existing.IncludeTasks
	if req.IncludeTasks != nil {
		includeTasks = *req.IncludeTasks
	}
	includeAuditLogs := existing.IncludeAuditLogs
	if req.IncludeAuditLogs != nil {
		includeAuditLogs = *req.IncludeAuditLogs
	}

	out := MigrationBackupConfig{
		APIBaseURL:          baseURL,
		ClientID:            strings.TrimSpace(req.ClientID),
		APIKeyEncrypted:     apiKeyEnc,
		Targets:             strings.TrimSpace(req.Targets),
		Path:                strings.TrimSpace(req.Path),
		Mode:                mode,
		Rename:              rename,
		PassphraseEncrypted: passEnc,
		IncludeAdmins:       includeAdmins,
		IncludeTasks:        includeTasks,
		IncludeAuditLogs:    includeAuditLogs,
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// RevealMigrationBackupSecrets decrypts the optional API key and passphrase stored in migration_backup config.
func RevealMigrationBackupSecrets(c *crypto.Cipher, raw string) (apiKey, passphrase string, err error) {
	if c == nil {
		return "", "", errors.New("cipher is required")
	}
	cfg := ParseMigrationBackupConfig(raw)
	if cfg.APIKeyEncrypted != "" {
		apiKey, err = c.DecryptString(cfg.APIKeyEncrypted)
		if err != nil {
			return "", "", fmt.Errorf("decrypt api_key: %w", err)
		}
	}
	if cfg.PassphraseEncrypted != "" {
		passphrase, err = c.DecryptString(cfg.PassphraseEncrypted)
		if err != nil {
			return "", "", fmt.Errorf("decrypt passphrase: %w", err)
		}
	}
	return apiKey, passphrase, nil
}

// DeriveLocalBaseURL turns a server listen address (e.g. ":8333", "0.0.0.0:8333") into a loopback HTTP base URL.
func DeriveLocalBaseURL(listenAddr string) string {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		return ""
	}
	if strings.HasPrefix(addr, "http://") || strings.HasPrefix(addr, "https://") {
		return strings.TrimRight(addr, "/")
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return fmt.Sprintf("http://%s", net.JoinHostPort(host, port))
}

func (e *Engine) runMigrationBackup(ctx context.Context, rawCfg string) (int64, string, error) {
	if e.migSvc == nil || e.cipher == nil {
		return 0, "", errors.New("migration backup dependencies are not configured")
	}
	cfg := ParseMigrationBackupConfig(rawCfg)

	baseURL := strings.TrimRight(strings.TrimSpace(cfg.APIBaseURL), "/")
	if baseURL == "" {
		baseURL = strings.TrimRight(strings.TrimSpace(e.localBaseURL), "/")
	}
	if baseURL == "" {
		return 0, "", errors.New("api_base_url is empty and local server URL is unavailable")
	}

	apiKey := ""
	if cfg.APIKeyEncrypted != "" {
		var err error
		apiKey, err = e.cipher.DecryptString(cfg.APIKeyEncrypted)
		if err != nil {
			return 0, "", fmt.Errorf("decrypt configured api_key: %w", err)
		}
	} else if cfg.ClientID != "" {
		cl, err := e.store.GetClientByID(ctx, cfg.ClientID)
		if err != nil {
			return 0, "", fmt.Errorf("load client %s: %w", cfg.ClientID, err)
		}
		if cl.KeyEncrypted == "" {
			return 0, "", fmt.Errorf("client %q has no stored encrypted api_key (please rotate its key or enter api_key manually)", cl.Name)
		}
		apiKey, err = e.cipher.DecryptString(cl.KeyEncrypted)
		if err != nil {
			return 0, "", fmt.Errorf("decrypt client api_key: %w", err)
		}
	}
	if strings.TrimSpace(apiKey) == "" {
		return 0, "", errors.New("missing api_key or client_id in migration_backup configuration")
	}

	passphrase := ""
	if cfg.PassphraseEncrypted != "" {
		var err error
		passphrase, err = e.cipher.DecryptString(cfg.PassphraseEncrypted)
		if err != nil {
			return 0, "", fmt.Errorf("decrypt passphrase: %w", err)
		}
	}

	bundleBytes, err := e.migSvc.Export(ctx, migration.ExportOptions{
		IncludeAdmins:    cfg.IncludeAdmins,
		IncludeTasks:     cfg.IncludeTasks,
		IncludeAuditLogs: cfg.IncludeAuditLogs,
		Passphrase:       passphrase,
	})
	if err != nil {
		return 0, "", fmt.Errorf("export migration bundle: %w", err)
	}

	filename := fmt.Sprintf("safekeep-migration-%s.skbundle", time.Now().UTC().Format("20060102-150405"))
	var bodyBuf bytes.Buffer
	mw := multipart.NewWriter(&bodyBuf)
	_ = mw.WriteField("mode", cfg.Mode)
	if cfg.Path != "" {
		_ = mw.WriteField("path", cfg.Path)
	}
	if cfg.Targets != "" {
		_ = mw.WriteField("targets", cfg.Targets)
	}
	if cfg.Rename != "" {
		_ = mw.WriteField("rename", cfg.Rename)
	}
	_ = mw.WriteField("dedup", "false")
	metaJSON, _ := json.Marshal(map[string]any{
		"source":             "cron_migration_backup",
		"include_admins":     cfg.IncludeAdmins,
		"include_tasks":      cfg.IncludeTasks,
		"include_audit_logs": cfg.IncludeAuditLogs,
		"encrypted_with_pwd": passphrase != "",
	})
	_ = mw.WriteField("metadata", string(metaJSON))

	fw, err := mw.CreateFormFile("file", filename)
	if err != nil {
		return 0, "", fmt.Errorf("create multipart form file: %w", err)
	}
	if _, err := fw.Write(bundleBytes); err != nil {
		return 0, "", fmt.Errorf("write bundle to multipart body: %w", err)
	}
	if err := mw.Close(); err != nil {
		return 0, "", fmt.Errorf("close multipart writer: %w", err)
	}

	endpoint := baseURL + "/api/v1/backups"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(bodyBuf.Bytes()))
	if err != nil {
		return 0, "", fmt.Errorf("build backup request: %w", err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", apiKey)

	httpClient := e.httpClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Minute}
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, "", fmt.Errorf("POST %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
		var out struct {
			TimedOutToAsync bool `json:"timed_out_to_async"`
			Task            struct {
				ID               string `json:"id"`
				Status           string `json:"status"`
				OriginalFilename string `json:"original_filename"`
				RemoteFilename   string `json:"remote_filename"`
			} `json:"task"`
		}
		_ = json.Unmarshal(respBytes, &out)
		storedName := out.Task.RemoteFilename
		if storedName == "" {
			if cfg.Rename != "" {
				storedName = cfg.Rename
			} else {
				storedName = filename
			}
		}
		modeNote := out.Task.Status
		if resp.StatusCode == http.StatusAccepted && out.TimedOutToAsync {
			modeNote = "timed_out_to_async"
		}
		return 1, fmt.Sprintf("uploaded %s (%d bytes) to %s -> task %s (%s)", storedName, len(bundleBytes), endpoint, out.Task.ID, modeNote), nil
	}

	var errOut struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		Task struct {
			ID           string `json:"id"`
			Status       string `json:"status"`
			ErrorSummary string `json:"error_summary"`
		} `json:"task"`
	}
	_ = json.Unmarshal(respBytes, &errOut)
	if errOut.Task.ID != "" {
		return 0, "", fmt.Errorf("backup API %d (task %s, %s): %s", resp.StatusCode, errOut.Task.ID, errOut.Task.Status, errOut.Task.ErrorSummary)
	}
	if errOut.Error.Message != "" {
		return 0, "", fmt.Errorf("backup API %d (%s): %s", resp.StatusCode, errOut.Error.Code, errOut.Error.Message)
	}
	return 0, "", fmt.Errorf("backup API returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
}
