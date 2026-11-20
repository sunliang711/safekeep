package s3

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"

	"safekeep/internal/db"
	"safekeep/internal/proxy"
	"safekeep/internal/storage"
)

type Provider struct {
	providerType string
	cfg          storage.S3Config
	client       *awss3.Client
	uploader     *manager.Uploader
}

// Builder implements storage.Builder for S3-compatible backends.
func Builder(ctx context.Context, _ *db.Upstream, cfgJSON []byte, pCfg proxy.Config, _ func([]byte) error) (storage.Provider, error) {
	var cfg storage.S3Config
	if err := json.Unmarshal(cfgJSON, &cfg); err != nil {
		return nil, fmt.Errorf("parse s3 config: %w", err)
	}
	return NewWithType(ctx, "s3", cfg, pCfg)
}

// NewWithType constructs an S3-compatible Provider with an explicit provider type label (e.g. "s3" or "r2").
func NewWithType(ctx context.Context, providerType string, cfg storage.S3Config, pCfg proxy.Config) (*Provider, error) {
	if providerType == "" {
		providerType = "s3"
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, fmt.Errorf("%s bucket is required", providerType)
	}
	if strings.TrimSpace(cfg.AccessKeyID) == "" || strings.TrimSpace(cfg.SecretAccessKey) == "" {
		return nil, fmt.Errorf("%s access_key_id and secret_access_key are required", providerType)
	}
	if strings.TrimSpace(cfg.Region) == "" {
		if providerType == "r2" {
			cfg.Region = "auto"
		} else {
			cfg.Region = "us-east-1"
		}
	}

	httpClient, err := proxy.NewHTTPClient(pCfg)
	if err != nil {
		return nil, fmt.Errorf("build %s proxy client: %w", providerType, err)
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, "")),
		awsconfig.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	client := awss3.NewFromConfig(awsCfg, func(o *awss3.Options) {
		if strings.TrimSpace(cfg.Endpoint) != "" {
			o.BaseEndpoint = aws.String(strings.TrimSpace(cfg.Endpoint))
		}
		o.UsePathStyle = cfg.ForcePathStyle
		if providerType == "r2" {
			o.RequestChecksumCalculation = aws.RequestChecksumCalculationWhenRequired
			o.ResponseChecksumValidation = aws.ResponseChecksumValidationWhenRequired
		}
	})

	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = 16 * 1024 * 1024 // 16 MiB
		u.Concurrency = 3
	})

	return &Provider{
		providerType: providerType,
		cfg:          cfg,
		client:       client,
		uploader:     uploader,
	}, nil
}

func (p *Provider) Type() string {
	if p.providerType != "" {
		return p.providerType
	}
	return "s3"
}

func (p *Provider) fullKey(remotePath string) string {
	cleanPath := strings.TrimLeft(remotePath, "/")
	prefix := strings.Trim(p.cfg.BasePrefix, "/")
	if prefix == "" {
		return cleanPath
	}
	return path.Join(prefix, cleanPath)
}

func (p *Provider) Upload(ctx context.Context, remotePath string, body io.ReadSeeker, _ int64, contentType string) (*storage.UploadResult, error) {
	if _, err := body.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind body: %w", err)
	}
	key := p.fullKey(remotePath)
	if contentType == "" {
		contentType = "application/octet-stream"
	}

	input := &awss3.PutObjectInput{
		Bucket:      aws.String(p.cfg.Bucket),
		Key:         aws.String(key),
		Body:        body,
		ContentType: aws.String(contentType),
	}
	if sc := strings.TrimSpace(p.cfg.StorageClass); sc != "" {
		input.StorageClass = types.StorageClass(sc)
	}

	out, err := p.uploader.Upload(ctx, input)
	if err != nil {
		return nil, fmt.Errorf("s3 upload %s: %w", key, err)
	}

	return &storage.UploadResult{
		RemotePath:      key,
		RemoteETag:      strings.Trim(aws.ToString(out.ETag), `"`),
		RemoteVersionID: aws.ToString(out.VersionID),
	}, nil
}

func (p *Provider) Download(ctx context.Context, remotePath string) (io.ReadCloser, int64, string, error) {
	key := strings.TrimLeft(remotePath, "/")
	out, err := p.client.GetObject(ctx, &awss3.GetObjectInput{
		Bucket: aws.String(p.cfg.Bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, 0, "", fmt.Errorf("s3 get object %s: %w", key, err)
	}
	return out.Body, aws.ToInt64(out.ContentLength), aws.ToString(out.ContentType), nil
}

func (p *Provider) Delete(ctx context.Context, remotePath string) error {
	key := strings.TrimLeft(remotePath, "/")
	_, err := p.client.DeleteObject(ctx, &awss3.DeleteObjectInput{
		Bucket: aws.String(p.cfg.Bucket),
		Key:    aws.String(key),
	})
	return err
}

func (p *Provider) TestConnection(ctx context.Context) error {
	_, err := p.client.HeadBucket(ctx, &awss3.HeadBucketInput{
		Bucket: aws.String(p.cfg.Bucket),
	})
	if err != nil {
		return fmt.Errorf("s3 head bucket %q: %w", p.cfg.Bucket, err)
	}
	return nil
}
