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
