package gcs

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
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
	defaultAPIBase     = "https://storage.googleapis.com"
	defaultTokenURL    = "https://oauth2.googleapis.com/token"
	gcsScope           = "https://www.googleapis.com/auth/devstorage.read_write"
	singleUploadLimit  = 16 * 1024 * 1024 // 16 MiB multipart upload threshold
	resumableChunkSize = 16 * 1024 * 1024 // 16 MiB (multiple of 256 KiB required by GCS)
)

type serviceAccountKey struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

type Provider struct {
	cfg        storage.GCSConfig
	sa         serviceAccountKey
	rsaKey     *rsa.PrivateKey
	tokenURL   string
	httpClient *http.Client

	mu          sync.Mutex
	accessToken string
	expiresAt   time.Time
}

// Builder implements storage.Builder for Google Cloud Storage backends.
func Builder(_ context.Context, _ *db.Upstream, cfgJSON []byte, pCfg proxy.Config, _ func([]byte) error) (storage.Provider, error) {
	var cfg storage.GCSConfig
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		return nil, fmt.Errorf("parse gcs config: %w", err)
	}
	cfg.Bucket = strings.TrimSpace(cfg.Bucket)
	if cfg.Bucket == "" {
		return nil, fmt.Errorf("gcs bucket is required")
	}

	sa, rsaKey, err := parseServiceAccount(cfg.ServiceAccountJSON)
	if err != nil {
		return nil, err
	}

	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = defaultAPIBase
	}
	tokenURL := strings.TrimSpace(cfg.TokenURL)
	if tokenURL == "" {
		tokenURL = strings.TrimSpace(sa.TokenURI)
	}
	if tokenURL == "" {
		tokenURL = defaultTokenURL
	}

	httpClient, err := proxy.NewHTTPClient(pCfg)
	if err != nil {
		return nil, fmt.Errorf("build gcs proxy client: %w", err)
	}

	return &Provider{
		cfg:        cfg,
		sa:         sa,
		rsaKey:     rsaKey,
		tokenURL:   tokenURL,
		httpClient: httpClient,
	}, nil
}

// ValidateServiceAccountJSON verifies that a GCP service account JSON contains valid RSA credentials.
func ValidateServiceAccountJSON(saJSON string) error {
	_, _, err := parseServiceAccount(saJSON)
	return err
}

func parseServiceAccount(saJSON string) (serviceAccountKey, *rsa.PrivateKey, error) {
	if strings.TrimSpace(saJSON) == "" {
		return serviceAccountKey{}, nil, fmt.Errorf("gcs service_account_json is required")
	}
	var sa serviceAccountKey
	if err := json.Unmarshal([]byte(saJSON), &sa); err != nil {
		return serviceAccountKey{}, nil, fmt.Errorf("invalid gcs service_account_json: %w", err)
	}
	if sa.Type != "" && sa.Type != "service_account" {
		return serviceAccountKey{}, nil, fmt.Errorf("gcs service_account_json type must be 'service_account', got %q", sa.Type)
	}
	if strings.TrimSpace(sa.ClientEmail) == "" || strings.TrimSpace(sa.PrivateKey) == "" {
		return serviceAccountKey{}, nil, fmt.Errorf("gcs service_account_json must contain client_email and private_key")
	}

	block, _ := pem.Decode([]byte(sa.PrivateKey))
	if block == nil {
		return serviceAccountKey{}, nil, fmt.Errorf("invalid gcs service account private_key PEM")
	}
	if keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		rsaKey, ok := keyAny.(*rsa.PrivateKey)
		if !ok {
			return serviceAccountKey{}, nil, fmt.Errorf("gcs service account private_key is not an RSA key")
		}
		return sa, rsaKey, nil
	}
	if rsaKey, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return sa, rsaKey, nil
	}
	return serviceAccountKey{}, nil, fmt.Errorf("failed to parse gcs service account private_key")
}

func (p *Provider) Type() string {
	return "gcs"
}

func (p *Provider) fullObjectKey(remotePath string) string {
	cleanPath := strings.TrimLeft(remotePath, "/")
	prefix := strings.Trim(p.cfg.BasePrefix, "/")
	if prefix == "" {
		return cleanPath
	}
	return path.Join(prefix, cleanPath)
}

func (p *Provider) ensureToken(ctx context.Context, forceRefresh bool) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !forceRefresh && p.accessToken != "" && time.Now().Add(2*time.Minute).Before(p.expiresAt) {
		return p.accessToken, nil
	}

	assertion, err := p.buildJWTAssertion(time.Now().UTC())
	if err != nil {
		return "", fmt.Errorf("sign gcs jwt assertion: %w", err)
	}

	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("gcs token request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("gcs token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(bodyBytes)))
	}

	var tokResp struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(bodyBytes, &tokResp); err != nil {
		return "", fmt.Errorf("decode gcs token response: %w", err)
	}
	if tokResp.AccessToken == "" {
		return "", fmt.Errorf("gcs returned empty access_token")
	}

	ttl := time.Duration(tokResp.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = 55 * time.Minute
	}
	p.accessToken = tokResp.AccessToken
	p.expiresAt = time.Now().Add(ttl)
	return p.accessToken, nil
}

func (p *Provider) buildJWTAssertion(now time.Time) (string, error) {
	headerMap := map[string]string{
		"alg": "RS256",
		"typ": "JWT",
	}
	if p.sa.PrivateKeyID != "" {
		headerMap["kid"] = p.sa.PrivateKeyID
	}
	headerBytes, err := json.Marshal(headerMap)
	if err != nil {
		return "", err
	}

	claimsMap := map[string]any{
		"iss":   p.sa.ClientEmail,
		"scope": gcsScope,
		"aud":   p.tokenURL,
		"iat":   now.Unix(),
		"exp":   now.Add(1 * time.Hour).Unix(),
	}
	claimsBytes, err := json.Marshal(claimsMap)
	if err != nil {
		return "", err
	}

	signingInput := base64.RawURLEncoding.EncodeToString(headerBytes) + "." + base64.RawURLEncoding.EncodeToString(claimsBytes)
	digest := sha256.Sum256([]byte(signingInput))
	sig, err := rsa.SignPKCS1v15(rand.Reader, p.rsaKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", err
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

type gcsObjectMeta struct {
	Name       string `json:"name"`
	ETag       string `json:"etag"`
	MD5Hash    string `json:"md5Hash"`
	Generation string `json:"generation"`
}

func (m gcsObjectMeta) toUploadResult(fallbackKey string) *storage.UploadResult {
	remotePath := m.Name
	if remotePath == "" {
		remotePath = fallbackKey
	}
	etag := strings.Trim(m.ETag, `"`)
	if etag == "" {
		etag = m.MD5Hash
	}
	return &storage.UploadResult{
		RemotePath:      remotePath,
		RemoteETag:      etag,
		RemoteVersionID: m.Generation,
	}
}

func (p *Provider) Upload(ctx context.Context, remotePath string, body io.ReadSeeker, size int64, contentType string) (*storage.UploadResult, error) {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if size <= singleUploadLimit {
		return p.uploadMultipart(ctx, remotePath, body, size, contentType)
	}
	return p.uploadResumable(ctx, remotePath, body, size, contentType)
}

func (p *Provider) objectMetadataJSON(objectKey, contentType string) ([]byte, error) {
	meta := map[string]any{
		"name":        objectKey,
		"contentType": contentType,
	}
	if sc := strings.TrimSpace(p.cfg.StorageClass); sc != "" {
		meta["storageClass"] = sc
	}
	return json.Marshal(meta)
}

func (p *Provider) uploadMultipart(ctx context.Context, remotePath string, body io.ReadSeeker, size int64, contentType string) (*storage.UploadResult, error) {
	objectKey := p.fullObjectKey(remotePath)
	metaBytes, err := p.objectMetadataJSON(objectKey, contentType)
	if err != nil {
		return nil, err
	}

	var rnd [12]byte
	_, _ = rand.Read(rnd[:])
	boundary := "safekeep-" + base64.RawURLEncoding.EncodeToString(rnd[:])

	var headerBuf bytes.Buffer
	headerBuf.WriteString("--" + boundary + "\r\n")
	headerBuf.WriteString("Content-Type: application/json; charset=UTF-8\r\n\r\n")
	headerBuf.Write(metaBytes)
	headerBuf.WriteString("\r\n--" + boundary + "\r\n")
	headerBuf.WriteString("Content-Type: " + contentType + "\r\n\r\n")

	footer := []byte("\r\n--" + boundary + "--\r\n")
	uploadURL := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=multipart",
		strings.TrimRight(p.cfg.APIBaseURL, "/"),
		url.PathEscape(p.cfg.Bucket),
	)

	for attempt := 0; attempt < 2; attempt++ {
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("rewind body: %w", err)
		}
		tok, err := p.ensureToken(ctx, attempt > 0)
		if err != nil {
			return nil, err
		}

		var reader io.Reader
		if size > 0 {
			reader = io.MultiReader(bytes.NewReader(headerBuf.Bytes()), io.LimitReader(body, size), bytes.NewReader(footer))
		} else {
			reader = io.MultiReader(bytes.NewReader(headerBuf.Bytes()), bytes.NewReader(footer))
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, reader)
		if err != nil {
			return nil, err
		}
		if size >= 0 {
			req.ContentLength = int64(headerBuf.Len()) + size + int64(len(footer))
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "multipart/related; boundary="+boundary)

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("gcs upload %s: %w", objectKey, err)
		}
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return nil, fmt.Errorf("gcs upload failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
		}

		var meta gcsObjectMeta
		if err := json.Unmarshal(respBytes, &meta); err != nil {
			return nil, fmt.Errorf("decode gcs upload response: %w", err)
		}
		return meta.toUploadResult(objectKey), nil
	}
	return nil, fmt.Errorf("gcs upload unauthorized after token refresh")
}

func (p *Provider) initiateResumableSession(ctx context.Context, objectKey string, totalSize int64, contentType string) (string, error) {
	metaBytes, err := p.objectMetadataJSON(objectKey, contentType)
	if err != nil {
		return "", err
	}
	initURL := fmt.Sprintf("%s/upload/storage/v1/b/%s/o?uploadType=resumable",
		strings.TrimRight(p.cfg.APIBaseURL, "/"),
		url.PathEscape(p.cfg.Bucket),
	)

	for attempt := 0; attempt < 2; attempt++ {
		tok, err := p.ensureToken(ctx, attempt > 0)
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, initURL, bytes.NewReader(metaBytes))
		if err != nil {
			return "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json; charset=UTF-8")
		req.Header.Set("X-Upload-Content-Type", contentType)
		if totalSize >= 0 {
			req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(totalSize, 10))
		}

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return "", fmt.Errorf("gcs resumable init: %w", err)
		}
		respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
			return "", fmt.Errorf("gcs resumable init failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
		}
		loc := resp.Header.Get("Location")
		if loc == "" {
			return "", fmt.Errorf("gcs resumable init missing Location header")
		}
		return loc, nil
	}
	return "", fmt.Errorf("gcs resumable init unauthorized after token refresh")
}

func (p *Provider) uploadResumable(ctx context.Context, remotePath string, body io.ReadSeeker, totalSize int64, contentType string) (*storage.UploadResult, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind body: %w", err)
	}
	objectKey := p.fullObjectKey(remotePath)
	sessionURL, err := p.initiateResumableSession(ctx, objectKey, totalSize, contentType)
	if err != nil {
		return nil, err
	}

	buf := make([]byte, resumableChunkSize)
	var offset int64
	for offset < totalSize {
		n, readErr := io.ReadFull(body, buf)
		if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
			return nil, readErr
		}
		if n == 0 {
			break
		}
		chunk := buf[:n]
		end := offset + int64(n) - 1
		contentRange := fmt.Sprintf("bytes %d-%d/%d", offset, end, totalSize)

		var finalBytes []byte
		var completed bool
		for attempt := 0; attempt < 2; attempt++ {
			tok, err := p.ensureToken(ctx, attempt > 0)
			if err != nil {
				return nil, err
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, sessionURL, bytes.NewReader(chunk))
			if err != nil {
				return nil, err
			}
			req.ContentLength = int64(n)
			req.Header.Set("Authorization", "Bearer "+tok)
			req.Header.Set("Content-Type", contentType)
			req.Header.Set("Content-Range", contentRange)

			resp, err := p.httpClient.Do(req)
			if err != nil {
				return nil, fmt.Errorf("gcs resumable put %s: %w", contentRange, err)
			}
			respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()

			if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
				continue
			}
			if resp.StatusCode == http.StatusPermanentRedirect || resp.StatusCode == 308 {
				break
			}
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
				finalBytes = respBytes
				completed = true
				break
			}
			return nil, fmt.Errorf("gcs resumable put failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(respBytes)))
		}

		offset += int64(n)
		if completed {
			var meta gcsObjectMeta
			if err := json.Unmarshal(finalBytes, &meta); err != nil {
				return nil, fmt.Errorf("decode gcs resumable response: %w", err)
			}
			return meta.toUploadResult(objectKey), nil
		}
	}

	return nil, fmt.Errorf("gcs resumable upload ended unexpectedly before completion")
}

func (p *Provider) Download(ctx context.Context, remotePath string) (io.ReadCloser, int64, string, error) {
	objectKey := strings.TrimLeft(remotePath, "/")
	dlURL := fmt.Sprintf("%s/storage/v1/b/%s/o/%s?alt=media",
		strings.TrimRight(p.cfg.APIBaseURL, "/"),
		url.PathEscape(p.cfg.Bucket),
		url.PathEscape(objectKey),
	)

	for attempt := 0; attempt < 2; attempt++ {
		tok, err := p.ensureToken(ctx, attempt > 0)
		if err != nil {
			return nil, 0, "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, dlURL, nil)
		if err != nil {
			return nil, 0, "", err
		}
		req.Header.Set("Authorization", "Bearer "+tok)

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return nil, 0, "", fmt.Errorf("gcs download %s: %w", objectKey, err)
		}
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			resp.Body.Close()
			return nil, 0, "", fmt.Errorf("gcs download failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(errBytes)))
		}
		ct := resp.Header.Get("Content-Type")
		if ct == "" {
			ct = "application/octet-stream"
		}
		return resp.Body, resp.ContentLength, ct, nil
	}
	return nil, 0, "", fmt.Errorf("gcs download unauthorized after token refresh")
}

func (p *Provider) Delete(ctx context.Context, remotePath string) error {
	objectKey := strings.TrimLeft(remotePath, "/")
	delURL := fmt.Sprintf("%s/storage/v1/b/%s/o/%s",
		strings.TrimRight(p.cfg.APIBaseURL, "/"),
		url.PathEscape(p.cfg.Bucket),
		url.PathEscape(objectKey),
	)

	for attempt := 0; attempt < 2; attempt++ {
		tok, err := p.ensureToken(ctx, attempt > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, delURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("gcs delete %s: %w", objectKey, err)
		}
		errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent {
			return fmt.Errorf("gcs delete failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(errBytes)))
		}
		return nil
	}
	return fmt.Errorf("gcs delete unauthorized after token refresh")
}

func (p *Provider) TestConnection(ctx context.Context) error {
	metaURL := fmt.Sprintf("%s/storage/v1/b/%s",
		strings.TrimRight(p.cfg.APIBaseURL, "/"),
		url.PathEscape(p.cfg.Bucket),
	)

	for attempt := 0; attempt < 2; attempt++ {
		tok, err := p.ensureToken(ctx, attempt > 0)
		if err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+tok)

		resp, err := p.httpClient.Do(req)
		if err != nil {
			return fmt.Errorf("gcs bucket check %q: %w", p.cfg.Bucket, err)
		}
		errBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()

		if resp.StatusCode == http.StatusUnauthorized && attempt == 0 {
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("gcs bucket check %q failed (%d): %s", p.cfg.Bucket, resp.StatusCode, strings.TrimSpace(string(errBytes)))
		}
		return nil
	}
	return fmt.Errorf("gcs bucket check unauthorized after token refresh")
}
