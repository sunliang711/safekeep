package r2

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"safekeep/internal/db"
	"safekeep/internal/proxy"
	"safekeep/internal/storage"
	"safekeep/internal/storage/s3"
)

// Builder implements storage.Builder for Cloudflare R2 backends using S3 compatibility.
func Builder(ctx context.Context, _ *db.Upstream, cfgJSON []byte, pCfg proxy.Config, _ func([]byte) error) (storage.Provider, error) {
	var cfg storage.R2Config
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		return nil, fmt.Errorf("parse r2 config: %w", err)
	}
	endpoint, err := storage.ResolveR2Endpoint(cfg)
	if err != nil {
		return nil, err
	}
	region := strings.TrimSpace(cfg.Region)
	if region == "" {
		region = "auto"
	}
	s3Cfg := storage.S3Config{
		Endpoint:        endpoint,
		Region:          region,
		Bucket:          strings.TrimSpace(cfg.Bucket),
		AccessKeyID:     strings.TrimSpace(cfg.AccessKeyID),
		SecretAccessKey: cfg.SecretAccessKey,
		BasePrefix:      cfg.BasePrefix,
		ForcePathStyle:  cfg.ForcePathStyle,
		StorageClass:    strings.TrimSpace(cfg.StorageClass),
	}
	return s3.NewWithType(ctx, "r2", s3Cfg, pCfg)
}
