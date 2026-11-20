package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"safekeep/internal/crypto"
	"safekeep/internal/db"
	"safekeep/internal/proxy"
)

// UploadResult describes the outcome of uploading a file to an upstream provider.
type UploadResult struct {
	RemotePath      string
	RemoteETag      string
	RemoteVersionID string
}

// Provider defines the unified interface every storage backend must implement.
type Provider interface {
	Type() string
	Upload(ctx context.Context, remotePath string, body io.ReadSeeker, size int64, contentType string) (*UploadResult, error)
	Download(ctx context.Context, remotePath string) (io.ReadCloser, int64, string, error)
	Delete(ctx context.Context, remotePath string) error
	TestConnection(ctx context.Context) error
}

// S3Config stores decrypted configuration for an S3-compatible upstream.
type S3Config struct {
	Endpoint        string `json:"endpoint"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	BasePrefix      string `json:"base_prefix"`
	ForcePathStyle  bool   `json:"force_path_style"`
	StorageClass    string `json:"storage_class"`
}

// R2Config stores decrypted configuration for a Cloudflare R2 upstream.
type R2Config struct {
	AccountID       string `json:"account_id"`
	Jurisdiction    string `json:"jurisdiction,omitempty"` // "" (default) | "eu" | "fedramp"
	Endpoint        string `json:"endpoint,omitempty"`     // optional custom endpoint override
	Region          string `json:"region,omitempty"`       // defaults to "auto"
	Bucket          string `json:"bucket"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	BasePrefix      string `json:"base_prefix"`
	ForcePathStyle  bool   `json:"force_path_style"`
	StorageClass    string `json:"storage_class,omitempty"`
}

// ResolveR2Endpoint returns the effective S3 API endpoint URL for a Cloudflare R2 config.
func ResolveR2Endpoint(cfg R2Config) (string, error) {
	ep := strings.TrimSpace(cfg.Endpoint)
	if ep != "" {
		if !strings.Contains(ep, "://") {
			ep = "https://" + ep
		}
		return strings.TrimRight(ep, "/"), nil
	}
	accountID := strings.TrimSpace(cfg.AccountID)
	if accountID == "" {
		return "", fmt.Errorf("r2 account_id or endpoint is required")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Jurisdiction)) {
	case "", "default":
		return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", accountID), nil
	case "eu":
		return fmt.Sprintf("https://%s.eu.r2.cloudflarestorage.com", accountID), nil
	case "fedramp":
		return fmt.Sprintf("https://%s.fedramp.r2.cloudflarestorage.com", accountID), nil
	default:
		return "", fmt.Errorf("unsupported r2 jurisdiction %q (must be empty, eu, or fedramp)", cfg.Jurisdiction)
	}
}

// GCSConfig stores decrypted configuration for a Google Cloud Storage upstream.
type GCSConfig struct {
	Bucket             string `json:"bucket"`
	ServiceAccountJSON string `json:"service_account_json"`
	BasePrefix         string `json:"base_prefix"`
	StorageClass       string `json:"storage_class,omitempty"`
	ProjectID          string `json:"project_id,omitempty"`
	ClientEmail        string `json:"client_email,omitempty"`
	APIBaseURL         string `json:"api_base_url,omitempty"` // optional override for tests
	TokenURL           string `json:"token_url,omitempty"`    // optional override for tests
}

// ParseGCSServiceAccountSummary extracts non-secret metadata (project_id, client_email) from a GCP service account JSON key.
func ParseGCSServiceAccountSummary(saJSON string) (projectID, clientEmail string, err error) {
	if strings.TrimSpace(saJSON) == "" {
		return "", "", fmt.Errorf("gcs service_account_json is required")
	}
	var sa struct {
		Type        string `json:"type"`
		ProjectID   string `json:"project_id"`
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
	}
	if err := json.Unmarshal([]byte(saJSON), &sa); err != nil {
		return "", "", fmt.Errorf("invalid gcs service_account_json: %w", err)
	}
	if sa.Type != "" && sa.Type != "service_account" {
		return "", "", fmt.Errorf("gcs service_account_json type must be 'service_account', got %q", sa.Type)
	}
	if strings.TrimSpace(sa.ClientEmail) == "" || strings.TrimSpace(sa.PrivateKey) == "" {
		return "", "", fmt.Errorf("gcs service_account_json must contain client_email and private_key")
	}
	return strings.TrimSpace(sa.ProjectID), strings.TrimSpace(sa.ClientEmail), nil
}

// DropboxConfig stores decrypted configuration and OAuth2 tokens for a Dropbox upstream.
type DropboxConfig struct {
	AppKey         string    `json:"app_key"`
	AppSecret      string    `json:"app_secret"`
	RefreshToken   string    `json:"refresh_token"`
	AccessToken    string    `json:"access_token"`
	TokenExpiresAt time.Time `json:"token_expires_at"`
	RootPath       string    `json:"root_path"`
	AccountEmail   string    `json:"account_email"`
	APIBaseURL     string    `json:"api_base_url,omitempty"`     // optional override for tests
	ContentBaseURL string    `json:"content_base_url,omitempty"` // optional override for tests
}

// Builder creates a Provider instance from an upstream row and decrypted config/proxy.
type Builder func(ctx context.Context, upstream *db.Upstream, cfgJSON []byte, pCfg proxy.Config, onTokenUpdate func(newCfgJSON []byte) error) (Provider, error)

// Registry caches instantiated providers per upstream and rebuilds them when updated or invalidated.
type Registry struct {
	store    *db.Store
	cipher   *crypto.Cipher
	builders map[string]Builder

	mu    sync.RWMutex
	cache map[string]cachedProvider
}

type cachedProvider struct {
	updatedAt time.Time
	provider  Provider
}

func NewRegistry(store *db.Store, cipher *crypto.Cipher) *Registry {
	return &Registry{
		store:    store,
		cipher:   cipher,
		builders: make(map[string]Builder),
		cache:    make(map[string]cachedProvider),
	}
}

func (r *Registry) RegisterBuilder(upstreamType string, b Builder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.builders[strings.ToLower(upstreamType)] = b
}

func (r *Registry) Invalidate(upstreamID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.cache, upstreamID)
}

func (r *Registry) InvalidateAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cache = make(map[string]cachedProvider)
}

// Get returns a cached or freshly constructed Provider for the given upstream ID.
func (r *Registry) Get(ctx context.Context, upstreamID string) (Provider, *db.Upstream, error) {
	u, err := r.store.GetUpstreamByID(ctx, upstreamID)
	if err != nil {
		return nil, nil, err
	}
	if !u.Enabled {
		return nil, u, fmt.Errorf("upstream %q is disabled", u.Name)
	}

	r.mu.RLock()
	cached, ok := r.cache[u.ID]
	r.mu.RUnlock()
	if ok && cached.updatedAt.Equal(u.UpdatedAt) {
		return cached.provider, u, nil
	}

	p, err := r.BuildFromUpstream(ctx, u)
	if err != nil {
		return nil, u, err
	}

	r.mu.Lock()
	r.cache[u.ID] = cachedProvider{updatedAt: u.UpdatedAt, provider: p}
	r.mu.Unlock()
	return p, u, nil
}

// BuildFromUpstream builds a Provider without caching (also useful for testing unsaved/updated configs).
func (r *Registry) BuildFromUpstream(ctx context.Context, u *db.Upstream) (Provider, error) {
	r.mu.RLock()
	b, ok := r.builders[strings.ToLower(u.Type)]
	r.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("unsupported upstream type %q", u.Type)
	}

	cfgPlain, err := r.cipher.DecryptString(u.ConfigEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt upstream config: %w", err)
	}
	proxyUser, err := r.cipher.DecryptString(u.ProxyUsernameEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt proxy username: %w", err)
	}
	proxyPass, err := r.cipher.DecryptString(u.ProxyPasswordEncrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt proxy password: %w", err)
	}

	pCfg := proxy.Config{
		Enabled:  u.ProxyEnabled,
		Type:     u.ProxyType,
		Host:     u.ProxyHost,
		Port:     u.ProxyPort,
		Username: proxyUser,
		Password: proxyPass,
	}

	onUpdate := func(newCfgJSON []byte) error {
		enc, err := r.cipher.EncryptString(string(newCfgJSON))
		if err != nil {
			return err
		}
		return r.store.UpdateUpstreamConfigEncrypted(context.Background(), u.ID, enc)
	}

	return b(ctx, u, []byte(cfgPlain), pCfg, onUpdate)
}

// DecryptProxyConfig returns the decrypted ProxyConfig for an upstream.
func DecryptProxyConfig(c *crypto.Cipher, u *db.Upstream) (proxy.Config, error) {
	user, err := c.DecryptString(u.ProxyUsernameEncrypted)
	if err != nil {
		return proxy.Config{}, err
	}
	pass, err := c.DecryptString(u.ProxyPasswordEncrypted)
	if err != nil {
		return proxy.Config{}, err
	}
	return proxy.Config{
		Enabled:  u.ProxyEnabled,
		Type:     u.ProxyType,
		Host:     u.ProxyHost,
		Port:     u.ProxyPort,
		Username: user,
		Password: pass,
	}, nil
}

// MaskS3Config returns a redacted map suitable for Web UI display.
func MaskS3Config(raw []byte) (map[string]any, error) {
	var cfg S3Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return map[string]any{
		"endpoint":         cfg.Endpoint,
		"region":           cfg.Region,
		"bucket":           cfg.Bucket,
		"access_key_id":    cfg.AccessKeyID,
		"has_secret_key":   cfg.SecretAccessKey != "",
		"base_prefix":      cfg.BasePrefix,
		"force_path_style": cfg.ForcePathStyle,
		"storage_class":    cfg.StorageClass,
	}, nil
}

// MaskDropboxConfig returns a redacted map suitable for Web UI display.
func MaskDropboxConfig(raw []byte) (map[string]any, error) {
	var cfg DropboxConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	return map[string]any{
		"app_key":           cfg.AppKey,
		"has_app_secret":    cfg.AppSecret != "",
		"has_refresh_token": cfg.RefreshToken != "",
		"authorized":        cfg.RefreshToken != "" || cfg.AccessToken != "",
		"token_expires_at":  cfg.TokenExpiresAt,
		"root_path":         cfg.RootPath,
		"account_email":     cfg.AccountEmail,
	}, nil
}

// MaskR2Config returns a redacted map suitable for Web UI display.
func MaskR2Config(raw []byte) (map[string]any, error) {
	var cfg R2Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	resolvedEndpoint, _ := ResolveR2Endpoint(cfg)
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}
	return map[string]any{
		"account_id":        cfg.AccountID,
		"jurisdiction":      cfg.Jurisdiction,
		"endpoint":          cfg.Endpoint,
		"resolved_endpoint": resolvedEndpoint,
		"region":            region,
		"bucket":            cfg.Bucket,
		"access_key_id":     cfg.AccessKeyID,
		"has_secret_key":    cfg.SecretAccessKey != "",
		"base_prefix":       cfg.BasePrefix,
		"force_path_style":  cfg.ForcePathStyle,
		"storage_class":     cfg.StorageClass,
	}, nil
}

// MaskGCSConfig returns a redacted map suitable for Web UI display.
func MaskGCSConfig(raw []byte) (map[string]any, error) {
	var cfg GCSConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	projectID := strings.TrimSpace(cfg.ProjectID)
	clientEmail := strings.TrimSpace(cfg.ClientEmail)
	if (projectID == "" || clientEmail == "") && strings.TrimSpace(cfg.ServiceAccountJSON) != "" {
		if p, e, err := ParseGCSServiceAccountSummary(cfg.ServiceAccountJSON); err == nil {
			if projectID == "" {
				projectID = p
			}
			if clientEmail == "" {
				clientEmail = e
			}
		}
	}
	return map[string]any{
		"bucket":                   cfg.Bucket,
		"base_prefix":              cfg.BasePrefix,
		"storage_class":            cfg.StorageClass,
		"project_id":               projectID,
		"client_email":             clientEmail,
		"has_service_account_json": strings.TrimSpace(cfg.ServiceAccountJSON) != "",
	}, nil
}
