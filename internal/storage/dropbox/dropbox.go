package dropbox

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"

	"safekeep/internal/db"
	"safekeep/internal/proxy"
	"safekeep/internal/storage"
)

const (
	defaultAPIBase     = "https://api.dropboxapi.com"
	defaultContentBase = "https://content.dropboxapi.com"
	singleUploadLimit  = 140 * 1024 * 1024 // 140 MiB (Dropbox single upload limit is 150 MB)
	chunkSize          = 8 * 1024 * 1024   // 8 MiB per session append
)

type Provider struct {
	mu            sync.Mutex
	cfg           storage.DropboxConfig
	httpClient    *http.Client
	onTokenUpdate func([]byte) error
}

// Builder implements storage.Builder for Dropbox backends.
func Builder(_ context.Context, _ *db.Upstream, cfgJSON []byte, pCfg proxy.Config, onTokenUpdate func([]byte) error) (storage.Provider, error) {
	var cfg storage.DropboxConfig
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		return nil, fmt.Errorf("parse dropbox config: %w", err)
	}
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = defaultAPIBase
	}
	if strings.TrimSpace(cfg.ContentBaseURL) == "" {
		cfg.ContentBaseURL = defaultContentBase
	}

	httpClient, err := proxy.NewHTTPClient(pCfg)
	if err != nil {
		return nil, fmt.Errorf("build dropbox proxy client: %w", err)
	}

	return &Provider{
		cfg:           cfg,
		httpClient:    httpClient,
		onTokenUpdate: onTokenUpdate,
	}, nil
}

func (p *Provider) Type() string {
	return "dropbox"
}

func (p *Provider) fullPath(remotePath string) string {
	clean := strings.TrimLeft(remotePath, "/")
	root := strings.Trim(p.cfg.RootPath, "/")
	if root == "" {
		return "/" + clean
	}
	return "/" + path.Join(root, clean)
}

func (p *Provider) ensureToken(ctx context.Context, forceRefresh bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !forceRefresh && p.cfg.AccessToken != "" && (p.cfg.TokenExpiresAt.IsZero() || time.Now().Add(5*time.Minute).Before(p.cfg.TokenExpiresAt)) {
		return p.cfg.AccessToken, nil
	}
	if p.cfg.RefreshToken == "" {
		if p.cfg.AccessToken != "" && !forceRefresh {
			return p.cfg.AccessToken, nil
		}
		return "", fmt.Errorf("dropbox is not authorized: missing refresh_token (please authorize via Web UI)")
	}
	if p.cfg.AppKey == "" || p.cfg.AppSecret == "" {
		return "", fmt.Errorf("dropbox app_key and app_secret are required to refresh token")
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", p.cfg.RefreshToken)
	form.Set("client_id", p.cfg.AppKey)
	form.Set("client_secret", p.cfg.AppSecret)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.APIBaseURL, "/")+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("dropbox refresh token request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dropbox refresh token failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var tokResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(bodyBytes, &tokResp); err != nil {
		return "", fmt.Errorf("decode dropbox token response: %w", err)
	}
	if tokResp.AccessToken == "" {
		return "", fmt.Errorf("dropbox returned empty access_token")
	}

	p.cfg.AccessToken = tokResp.AccessToken
	if tokResp.ExpiresIn > 0 {
		p.cfg.TokenExpiresAt = time.Now().UTC().Add(time.Duration(tokResp.ExpiresIn) * time.Second)
	}
	if p.onTokenUpdate != nil {
		if raw, err := json.Marshal(p.cfg); err == nil {
			_ = p.onTokenUpdate(raw)
		}
	}
	return p.cfg.AccessToken, nil
}

// RefreshNow explicitly refreshes the Dropbox access token (used by cron job).
func (p *Provider) RefreshNow(ctx context.Context) error {
	_, err := p.ensureToken(ctx, true)
	return err
}

func (p *Provider) Upload(ctx context.Context, remotePath string, body io.ReadSeeker, size int64, _ string) (*storage.UploadResult, error) {
	if size <= singleUploadLimit {
		return p.uploadSingle(ctx, remotePath, body)
	}
	return p.uploadSession(ctx, remotePath, body, size)
}

type dropboxFileMeta struct {
	PathDisplay string `json:"path_display"`
	ContentHash string `json:"content_hash"`
	Rev         string `json:"rev"`
	Size        int64  `json:"size"`
}

func (p *Provider) uploadSingle(ctx context.Context, remotePath string, body io.ReadSeeker) (*storage.UploadResult, error) {
	targetPath := p.fullPath(remotePath)
	argJSON, _ := json.Marshal(map[string]any{
		"path":       targetPath,
		"mode":       "add",
		"autorename": true,
		"mute":       true,
	})

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return nil, err
		}
		tok, err := p.ensureToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.ContentBaseURL, "/")+"/2/files/upload", body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/octet-stream")
		req.Header.Set("Dropbox-API-Arg", string(argJSON))

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("dropbox upload %s: %w", targetPath, err)
		}
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("dropbox upload failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
		}

		var meta dropboxFileMeta
		if err := json.Unmarshal(respBytes, &meta); err != nil {
			return nil, fmt.Errorf("decode dropbox upload response: %w", err)
		}
		resPath := meta.PathDisplay
		if resPath == "" {
			resPath = targetPath
		}
		return &storage.UploadResult{
			RemotePath:      resPath,
			RemoteETag:      meta.ContentHash,
			RemoteVersionID: meta.Rev,
		}, nil
	}
	return nil, fmt.Errorf("dropbox upload unauthorized after refresh")
}

func (p *Provider) uploadSession(ctx context.Context, remotePath string, body io.ReadSeeker, totalSize int64) (*storage.UploadResult, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	tok, err := p.ensureToken(ctx, false)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, chunkSize)
	n, readErr := io.ReadFull(body, buf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return nil, readErr
	}

	// 1. Start session
	startArg, _ := json.Marshal(map[string]any{"close": false})
	startReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.ContentBaseURL, "/")+"/2/files/upload_session/start", bytes.NewReader(buf[:n]))
	if err != nil {
		return nil, err
	}
	startReq.Header.Set("Authorization", "Bearer "+tok)
	startReq.Header.Set("Content-Type", "application/octet-stream")
	startReq.Header.Set("Dropbox-API-Arg", string(startArg))

	startResp, err := p.httpClient.Do(startReq)
	if err != nil {
		return nil, fmt.Errorf("dropbox session start: %w", err)
	}
	startBytes, _ := io.ReadAll(io.LimitReader(startResp.Body, 1<<20))
	startResp.Body.Close()
	if startResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dropbox session start (%d): %s", startResp.StatusCode, strings.TrimSpace(string(startBytes)))
	}
	var startOut struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(startBytes, &startOut); err != nil || startOut.SessionID == "" {
		return nil, fmt.Errorf("invalid dropbox session start response")
	}

	offset := int64(n)
	for offset < totalSize {
		n, readErr = io.ReadFull(body, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return nil, readErr
		}
		if n == 0 {
			break
		}
		tok, err = p.ensureToken(ctx, false)
		if err != nil {
			return nil, err
		}

		isLast := offset+int64(n) >= totalSize
		if isLast {
			targetPath := p.fullPath(remotePath)
			finArg, _ := json.Marshal(map[string]any{
				"cursor": map[string]any{
					"session_id": startOut.SessionID,
					"offset":     offset,
				},
				"commit": map[string]any{
					"path":       targetPath,
					"mode":       "add",
					"autorename": true,
					"mute":       true,
				},
			})
			finReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.ContentBaseURL, "/")+"/2/files/upload_session/finish", bytes.NewReader(buf[:n]))
			if err != nil {
				return nil, err
			}
			finReq.Header.Set("Authorization", "Bearer "+tok)
			finReq.Header.Set("Content-Type", "application/octet-stream")
			finReq.Header.Set("Dropbox-API-Arg", string(finArg))

			finResp, err := p.httpClient.Do(finReq)
			if err != nil {
				return nil, fmt.Errorf("dropbox session finish: %w", err)
			}
			finBytes, _ := io.ReadAll(io.LimitReader(finResp.Body, 1<<20))
			finResp.Body.Close()
			if finResp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("dropbox session finish (%d): %s", finResp.StatusCode, strings.TrimSpace(string(finBytes)))
			}
			var meta dropboxFileMeta
			if err := json.Unmarshal(finBytes, &meta); err != nil {
				return nil, err
			}
			resPath := meta.PathDisplay
			if resPath == "" {
				resPath = targetPath
			}
			return &storage.UploadResult{
				RemotePath:      resPath,
				RemoteETag:      meta.ContentHash,
				RemoteVersionID: meta.Rev,
			}, nil
		}

		appendArg, _ := json.Marshal(map[string]any{
			"cursor": map[string]any{
				"session_id": startOut.SessionID,
				"offset":     offset,
			},
			"close": false,
		})
		appReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.ContentBaseURL, "/")+"/2/files/upload_session/append_v2", bytes.NewReader(buf[:n]))
		if err != nil {
			return nil, err
		}
		appReq.Header.Set("Authorization", "Bearer "+tok)
		appReq.Header.Set("Content-Type", "application/octet-stream")
		appReq.Header.Set("Dropbox-API-Arg", string(appendArg))

		appResp, err := p.httpClient.Do(appReq)
		if err != nil {
			return nil, fmt.Errorf("dropbox session append: %w", err)
		}
		appBytes, _ := io.ReadAll(io.LimitReader(appResp.Body, 1<<20))
		appResp.Body.Close()
		if appResp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("dropbox session append (%d): %s", appResp.StatusCode, strings.TrimSpace(string(appBytes)))
		}
		offset += int64(n)
	}

	return nil, fmt.Errorf("dropbox upload session ended unexpectedly")
}

func (p *Provider) Download(ctx context.Context, remotePath string) (io.ReadCloser, int64, string, error) {
	targetPath := remotePath
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}
	argJSON, _ := json.Marshal(map[string]string{"path": targetPath})

	for attempt := 0; attempt < 2; attempt++ {
		tok, err := p.ensureToken(ctx, attempt > 0)
		if err != nil {
			return nil, 0, "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.ContentBaseURL, "/")+"/2/files/download", nil)
		if err != nil {
			return nil, 0, "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Dropbox-API-Arg", string(argJSON))

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return nil, 0, "", fmt.Errorf("dropbox download %s: %w", targetPath, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			return nil, 0, "", fmt.Errorf("dropbox download failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}

		var size int64
		if cl := resp.Header.Get("Content-Length"); cl != "" {
			if v, err := strconv.ParseInt(cl, 10, 64); err == nil {
				size = v
			}
		}
		if hdr := resp.Header.Get("Dropbox-API-Result"); hdr != "" {
			var meta dropboxFileMeta
			if json.Unmarshal([]byte(hdr), &meta) == nil && meta.Size > 0 {
				size = meta.Size
			}
		}
		return resp.Body, size, "application/octet-stream", nil
	}
	return nil, 0, "", fmt.Errorf("dropbox download unauthorized after refresh")
}

func (p *Provider) Delete(ctx context.Context, remotePath string) error {
	targetPath := remotePath
	if !strings.HasPrefix(targetPath, "/") {
		targetPath = "/" + targetPath
	}
	bodyJSON, _ := json.Marshal(map[string]string{"path": targetPath})

	tok, err := p.ensureToken(ctx, false)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.APIBaseURL, "/")+"/2/files/delete_v2", bytes.NewReader(bodyJSON))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("dropbox delete failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	return nil
}

func (p *Provider) TestConnection(ctx context.Context) error {
	for attempt := 0; attempt < 2; attempt++ {
		tok, err := p.ensureToken(ctx, attempt > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(p.cfg.APIBaseURL, "/")+"/2/users/get_current_account", nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		resp, err := p.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("dropbox get_current_account: %w", err)
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("dropbox test failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(b)))
		}
		return nil
	}
	return fmt.Errorf("dropbox test unauthorized")
}

// BuildAuthorizeURL constructs the Dropbox OAuth2 authorization URL.
func BuildAuthorizeURL(appKey, redirectURI, state string) string {
	v := url.Values{}
	v.Set("client_id", appKey)
	v.Set("response_type", "code")
	v.Set("token_access_type", "offline")
	v.Set("redirect_uri", redirectURI)
	v.Set("state", state)
	return "https://www.dropbox.com/oauth2/authorize?" + v.Encode()
}

// OAuthTokenResult holds exchanged Dropbox OAuth2 tokens and account info.
type OAuthTokenResult struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    int
	Email        string
}

// ExchangeCode exchanges an OAuth2 authorization code using the provided proxy-aware HTTP client.
func ExchangeCode(ctx context.Context, httpClient *http.Client, apiBase, appKey, appSecret, code, redirectURI string) (*OAuthTokenResult, error) {
	if strings.TrimSpace(apiBase) == "" {
		apiBase = defaultAPIBase
	}
	form := url.Values{}
	form.Set("code", code)
	form.Set("grant_type", "authorization_code")
	form.Set("client_id", appKey)
	form.Set("client_secret", appSecret)
	form.Set("redirect_uri", redirectURI)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(apiBase, "/")+"/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dropbox oauth exchange: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dropbox oauth exchange failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, err
	}

	res := &OAuthTokenResult{
		AccessToken:  out.AccessToken,
		RefreshToken: out.RefreshToken,
		ExpiresIn:    out.ExpiresIn,
	}

	// Best-effort fetch account email
	accReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(apiBase, "/")+"/2/users/get_current_account", nil)
	if err == nil {
		accReq.Header.Set("Authorization", "Bearer "+out.AccessToken)
		if accResp, err := httpClient.Do(accReq); err == nil {
			defer accResp.Body.Close()
			if accResp.StatusCode == http.StatusOK {
				var acc struct {
					Email string `json:"email"`
				}
				if json.NewDecoder(accResp.Body).Decode(&acc) == nil {
					res.Email = acc.Email
				}
			}
		}
	}

	return res, nil
}
