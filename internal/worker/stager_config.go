package worker

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/me/gowe/pkg/staging"
)

// StagerConfig holds configuration for all stager backends.
type StagerConfig struct {
	// StageOutMode specifies where outputs are staged:
	// - "local": return file:// URI in-place (no copy)
	// - "file:///path": copy to shared path
	// - "http://upload.example.com" or "https://...": upload via HTTP PUT/POST
	// - "s3://bucket": upload to S3
	// - "shock://host": upload to Shock
	StageOutMode string

	// StageMode determines how files are staged (copy, symlink, reference).
	StageMode staging.StageMode

	// HTTP contains HTTP/HTTPS stager settings.
	HTTP HTTPStagerConfig

	// S3 contains S3/S3-compatible stager settings.
	S3 S3StagerConfig

	// Shock contains Shock stager settings.
	Shock ShockStagerConfig

	// Shared contains shared filesystem stager settings.
	Shared SharedStagerConfig

	// TLS contains TLS settings shared across worker and stagers.
	TLS TLSConfig
}

// TLSConfig contains TLS settings shared across worker-server communication
// and HTTPS stager operations.
type TLSConfig struct {
	// CACertPath is the path to a PEM-encoded CA certificate file.
	// When set, this CA is added to the trust pool for all HTTPS connections.
	CACertPath string

	// InsecureSkipVerify disables certificate verification.
	// WARNING: Only use for testing. Never enable in production.
	InsecureSkipVerify bool

	// certPool is the parsed CA pool (lazily initialized).
	certPool *x509.CertPool
}

// HTTPStagerConfig contains HTTP/HTTPS stager settings.
type HTTPStagerConfig struct {
	// Timeout is the HTTP request timeout (default: 5 minutes).
	Timeout time.Duration

	// MaxRetries is the number of retry attempts for failed requests (default: 3).
	MaxRetries int

	// RetryDelay is the initial delay between retries (default: 1 second).
	// Subsequent retries use exponential backoff.
	RetryDelay time.Duration

	// Credentials maps hostnames to authentication credentials.
	// Supports wildcard patterns like "*.example.com".
	Credentials map[string]CredentialSet

	// DefaultHeaders are headers added to all HTTP requests.
	DefaultHeaders map[string]string

	// UploadMethod is the HTTP method for uploads: "PUT" (default) or "POST".
	UploadMethod string

	// UploadPath is the URL template for StageOut uploads.
	// Supports placeholders: {taskID}, {filename}, {basename}
	// Example: "https://upload.example.com/files/{taskID}/{filename}"
	UploadPath string
}

// S3StagerConfig contains S3/S3-compatible stager settings.
type S3StagerConfig struct {
	// Endpoint is a custom S3 endpoint for MinIO, Wasabi, etc.
	// Leave empty for AWS S3.
	Endpoint string

	// Region is the AWS region (default: us-east-1).
	Region string

	// AccessKeyID is the AWS access key.
	AccessKeyID string

	// SecretAccessKey is the AWS secret key.
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

// ShockStagerConfig contains Shock stager settings.
type ShockStagerConfig struct {
	// DefaultHost is the default Shock server host (e.g., "p3.theseed.org").
	DefaultHost string

	// Token is the default authentication token.
	// Can be empty for anonymous access.
	Token string

	// Timeout is the HTTP request timeout.
	Timeout time.Duration

	// MaxRetries is the number of retry attempts.
	MaxRetries int

	// UseHTTP uses HTTP instead of HTTPS (for local development).
	UseHTTP bool
}

// SharedStagerConfig contains shared filesystem stager settings.
type SharedStagerConfig struct {
	// Enabled enables the shared filesystem stager.
	Enabled bool

	// PathMap maps source paths to target paths.
	// Example: {"/host/data": "/container/data"}
	PathMap map[string]string

	// StageOutDir is the base directory for output staging.
	// Leave empty for local mode (files stay in place).
	StageOutDir string
}

// CredentialSet holds authentication credentials for a host.
type CredentialSet struct {
	// Type specifies the authentication type: "bearer", "basic", or "header".
	Type string `json:"type"`

	// Token is the bearer token (for type="bearer").
	Token string `json:"token,omitempty"`

	// Username is the username for basic auth (for type="basic").
	Username string `json:"username,omitempty"`

	// Password is the password for basic auth (for type="basic").
	Password string `json:"password,omitempty"`

	// HeaderName is the custom header name (for type="header").
	HeaderName string `json:"header_name,omitempty"`

	// HeaderValue is the custom header value (for type="header").
	HeaderValue string `json:"header_value,omitempty"`
}

// DefaultStagerConfig returns a StagerConfig with sensible defaults.
func DefaultStagerConfig() StagerConfig {
	return StagerConfig{
		StageOutMode: "local",
		StageMode:    staging.StageModeCopy,
		HTTP: HTTPStagerConfig{
			Timeout:      5 * time.Minute,
			MaxRetries:   3,
			RetryDelay:   1 * time.Second,
			UploadMethod: "PUT",
		},
		S3: S3StagerConfig{
			Region:         "us-east-1",
			StageOutPrefix: "outputs/{taskID}/",
		},
		Shock: ShockStagerConfig{
			Timeout:    5 * time.Minute,
			MaxRetries: 3,
		},
	}
}

// BuildTLSConfig creates a *tls.Config from TLSConfig settings.
// Returns nil if no custom TLS configuration is needed.
func (c *TLSConfig) BuildTLSConfig() (*tls.Config, error) {
	if c.InsecureSkipVerify {
		return &tls.Config{InsecureSkipVerify: true}, nil
	}

	if c.CACertPath == "" {
		return nil, nil // Use system CA pool
	}

	// Load custom CA certificate.
	caCert, err := os.ReadFile(c.CACertPath)
	if err != nil {
		return nil, fmt.Errorf("read CA cert %s: %w", c.CACertPath, err)
	}

	// Create cert pool and add CA.
	certPool := x509.NewCertPool()
	if !certPool.AppendCertsFromPEM(caCert) {
		return nil, fmt.Errorf("failed to parse CA cert %s", c.CACertPath)
	}

	c.certPool = certPool

	return &tls.Config{
		RootCAs: certPool,
	}, nil
}

// LoadCredentialsFile loads credentials from a JSON file.
// The file format is: {"hostname": {"type": "bearer", "token": "..."}, ...}
func LoadCredentialsFile(path string) (map[string]CredentialSet, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials file: %w", err)
	}

	var creds map[string]CredentialSet
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials file: %w", err)
	}

	return creds, nil
}

// StagerCredentials represents the full credentials file structure.
// This supports multiple backend types in a single file.
type StagerCredentials struct {
	// HTTP credentials by hostname.
	HTTP map[string]CredentialSet `json:"http,omitempty"`

	// S3 credentials by endpoint/profile.
	S3 map[string]S3Credentials `json:"s3,omitempty"`

	// Shock credentials by hostname.
	Shock map[string]ShockCredentials `json:"shock,omitempty"`
}

// S3Credentials holds S3-specific credentials.
type S3Credentials struct {
	Endpoint        string `json:"endpoint,omitempty"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	UsePathStyle    bool   `json:"use_path_style,omitempty"`
}

// ShockCredentials holds Shock-specific credentials.
type ShockCredentials struct {
	Token string `json:"token,omitempty"` // Can be empty for anonymous.
}

// LoadStagerCredentialsFile loads the full credentials file.
func LoadStagerCredentialsFile(path string) (*StagerCredentials, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read credentials file: %w", err)
	}

	var creds StagerCredentials
	if err := json.Unmarshal(data, &creds); err != nil {
		return nil, fmt.Errorf("parse credentials file: %w", err)
	}

	return &creds, nil
}
