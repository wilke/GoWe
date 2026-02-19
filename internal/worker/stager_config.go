package worker

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"os"
	"time"
)

// StagerConfig holds configuration for all stager backends.
type StagerConfig struct {
	// StageOutMode specifies where outputs are staged:
	// - "local": return file:// URI in-place (no copy)
	// - "file:///path": copy to shared path
	// - "http://upload.example.com" or "https://...": upload via HTTP PUT/POST
	StageOutMode string

	// HTTP contains HTTP/HTTPS stager settings.
	HTTP HTTPStagerConfig

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
		HTTP: HTTPStagerConfig{
			Timeout:      5 * time.Minute,
			MaxRetries:   3,
			RetryDelay:   1 * time.Second,
			UploadMethod: "PUT",
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
