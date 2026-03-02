package staging

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/feature/s3/manager"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// S3Config contains S3 stager configuration.
type S3Config struct {
	// Endpoint is a custom S3 endpoint for MinIO, Wasabi, etc.
	// Leave empty for AWS S3.
	Endpoint string

	// Region is the AWS region (default: us-east-1).
	Region string

	// AccessKeyID is the AWS access key (from config or env).
	AccessKeyID string

	// SecretAccessKey is the AWS secret key (from config or env).
	SecretAccessKey string

	// UsePathStyle enables path-style addressing (required for MinIO).
	UsePathStyle bool

	// DisableSSL disables HTTPS (for local development).
	DisableSSL bool

	// DefaultBucket is used for stage-out if no bucket is specified.
	DefaultBucket string

	// StageOutPrefix is the key prefix for staged-out files.
	// Supports {taskID} placeholder.
	StageOutPrefix string
}

// S3Stager handles S3 and S3-compatible storage.
type S3Stager struct {
	config S3Config
	client *s3.Client
}

// NewS3Stager creates an S3Stager with the given configuration.
func NewS3Stager(cfg S3Config) (*S3Stager, error) {
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	if cfg.StageOutPrefix == "" {
		cfg.StageOutPrefix = "outputs/{taskID}/"
	}

	// Build AWS config.
	awsOpts := []func(*config.LoadOptions) error{
		config.WithRegion(cfg.Region),
	}

	// Use static credentials if provided.
	if cfg.AccessKeyID != "" && cfg.SecretAccessKey != "" {
		awsOpts = append(awsOpts, config.WithCredentialsProvider(
			credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
		))
	}

	awsCfg, err := config.LoadDefaultConfig(context.Background(), awsOpts...)
	if err != nil {
		return nil, fmt.Errorf("load AWS config: %w", err)
	}

	// Build S3 client options.
	s3Opts := []func(*s3.Options){
		func(o *s3.Options) {
			o.UsePathStyle = cfg.UsePathStyle
		},
	}

	// Add custom endpoint if specified.
	if cfg.Endpoint != "" {
		endpoint := cfg.Endpoint
		if cfg.DisableSSL && !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "http://" + endpoint
		} else if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
			endpoint = "https://" + endpoint
		}

		s3Opts = append(s3Opts, func(o *s3.Options) {
			o.BaseEndpoint = aws.String(endpoint)
		})
	}

	client := s3.NewFromConfig(awsCfg, s3Opts...)

	return &S3Stager{
		config: cfg,
		client: client,
	}, nil
}

// StageIn downloads a file from S3 to destPath.
// Supports URIs like: s3://bucket/key or s3://bucket/path/to/file
func (s *S3Stager) StageIn(ctx context.Context, location string, destPath string, opts StageOptions) error {
	bucket, key, err := parseS3URI(location)
	if err != nil {
		return fmt.Errorf("s3 stager: %w", err)
	}

	// Create client with per-request credentials if provided.
	client := s.clientWithOpts(opts)

	// Ensure destination directory exists.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("s3 stager: mkdir: %w", err)
	}

	// Create destination file.
	tmpPath := destPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("s3 stager: create file: %w", err)
	}

	// Use downloader for efficient streaming.
	downloader := manager.NewDownloader(client, func(d *manager.Downloader) {
		d.PartSize = 64 * 1024 * 1024 // 64MB parts
		d.Concurrency = 4
	})

	_, err = downloader.Download(ctx, f, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("s3 stager: download %s/%s: %w", bucket, key, err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("s3 stager: rename: %w", err)
	}

	return nil
}

// StageOut uploads a file to S3 and returns the location URI.
func (s *S3Stager) StageOut(ctx context.Context, srcPath string, taskID string, opts StageOptions) (string, error) {
	bucket := s.config.DefaultBucket
	if bucket == "" {
		return "", fmt.Errorf("s3 stager: no default bucket configured for stage-out")
	}

	// Build key with prefix.
	prefix := strings.ReplaceAll(s.config.StageOutPrefix, "{taskID}", taskID)
	key := prefix + filepath.Base(srcPath)

	// Open source file.
	f, err := os.Open(srcPath)
	if err != nil {
		return "", fmt.Errorf("s3 stager: open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("s3 stager: stat file: %w", err)
	}

	// Create client with per-request credentials if provided.
	client := s.clientWithOpts(opts)

	// Use uploader for efficient multipart uploads.
	uploader := manager.NewUploader(client, func(u *manager.Uploader) {
		u.PartSize = 64 * 1024 * 1024 // 64MB parts
		u.Concurrency = 4
		// Use multipart for files > 100MB
		if stat.Size() > 100*1024*1024 {
			u.LeavePartsOnError = false
		}
	})

	input := &s3.PutObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
		Body:   f,
	}

	// Add metadata if provided.
	if len(opts.Metadata) > 0 {
		input.Metadata = opts.Metadata
	}

	_, err = uploader.Upload(ctx, input)
	if err != nil {
		return "", fmt.Errorf("s3 stager: upload %s/%s: %w", bucket, key, err)
	}

	return fmt.Sprintf("s3://%s/%s", bucket, key), nil
}

// Supports returns true for s3 scheme.
func (s *S3Stager) Supports(scheme string) bool {
	return scheme == "s3"
}

// clientWithOpts creates a client with per-request credentials if provided.
func (s *S3Stager) clientWithOpts(opts StageOptions) *s3.Client {
	// Check for per-request credentials.
	if opts.Credentials != nil && opts.Credentials.AccessKeyID != "" {
		// Create new client with custom credentials.
		awsCfg, _ := config.LoadDefaultConfig(context.Background(),
			config.WithRegion(s.config.Region),
			config.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(
					opts.Credentials.AccessKeyID,
					opts.Credentials.SecretKey,
					"",
				),
			),
		)

		s3Opts := []func(*s3.Options){
			func(o *s3.Options) {
				o.UsePathStyle = s.config.UsePathStyle
			},
		}

		if s.config.Endpoint != "" {
			endpoint := s.config.Endpoint
			if s.config.DisableSSL && !strings.HasPrefix(endpoint, "http://") {
				endpoint = "http://" + endpoint
			} else if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
				endpoint = "https://" + endpoint
			}
			s3Opts = append(s3Opts, func(o *s3.Options) {
				o.BaseEndpoint = aws.String(endpoint)
			})
		}

		return s3.NewFromConfig(awsCfg, s3Opts...)
	}

	return s.client
}

// GetObject retrieves an object from S3 as a reader.
// The caller is responsible for closing the reader.
func (s *S3Stager) GetObject(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return nil, fmt.Errorf("get object %s/%s: %w", bucket, key, err)
	}
	return output.Body, nil
}

// parseS3URI parses an S3 URI and returns bucket and key.
// Supports formats:
//   - s3://bucket/key
//   - s3://bucket/path/to/key
func parseS3URI(uri string) (bucket, key string, err error) {
	scheme, path := ParseLocationScheme(uri)
	if scheme != "s3" {
		return "", "", fmt.Errorf("unsupported scheme %q, expected s3", scheme)
	}

	// Parse the path part.
	// The path after s3:// is bucket/key
	// First part is bucket, rest is key.
	parts := strings.SplitN(path, "/", 2)
	if len(parts) < 1 || parts[0] == "" {
		return "", "", fmt.Errorf("invalid S3 URI: missing bucket")
	}

	bucket = parts[0]
	if len(parts) > 1 {
		key = parts[1]
	}

	if key == "" {
		return "", "", fmt.Errorf("invalid S3 URI: missing key")
	}

	// URL decode the key in case it contains encoded characters.
	key, err = url.PathUnescape(key)
	if err != nil {
		return "", "", fmt.Errorf("invalid S3 key encoding: %w", err)
	}

	return bucket, key, nil
}

// Verify interface compliance.
var _ Stager = (*S3Stager)(nil)
