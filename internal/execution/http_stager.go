package execution

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/me/gowe/pkg/cwl"
)

// HTTPStagerConfig contains HTTP/HTTPS stager settings.
type HTTPStagerConfig struct {
	// Timeout is the HTTP request timeout.
	Timeout time.Duration

	// MaxRetries is the number of retry attempts.
	MaxRetries int

	// RetryDelay is the initial delay between retries.
	RetryDelay time.Duration

	// Credentials maps hostnames to authentication credentials.
	Credentials map[string]CredentialSet

	// DefaultHeaders are headers added to all requests.
	DefaultHeaders map[string]string

	// UploadMethod is "PUT" or "POST" (default: PUT).
	UploadMethod string

	// UploadPath is the URL template for uploads.
	UploadPath string
}

// CredentialSet holds authentication for a host.
type CredentialSet struct {
	Type        string `json:"type"`         // "bearer", "basic", "header"
	Token       string `json:"token"`        // for bearer
	Username    string `json:"username"`     // for basic
	Password    string `json:"password"`     // for basic
	HeaderName  string `json:"header_name"`  // for header
	HeaderValue string `json:"header_value"` // for header
}

// StagerOverrides allows per-task stager customization.
type StagerOverrides struct {
	// HTTPHeaders are additional headers for this task's HTTP requests.
	HTTPHeaders map[string]string `json:"http_headers,omitempty"`

	// HTTPTimeout overrides the default HTTP timeout.
	HTTPTimeout *time.Duration `json:"http_timeout,omitempty"`

	// HTTPCredential overrides credentials for this task.
	HTTPCredential *CredentialSet `json:"http_credential,omitempty"`
}

// HTTPStager handles HTTP/HTTPS file staging.
type HTTPStager struct {
	config    HTTPStagerConfig
	tlsConfig *tls.Config
	client    *http.Client
	overrides *StagerOverrides
}

// NewHTTPStager creates an HTTPStager with the given configuration.
func NewHTTPStager(cfg HTTPStagerConfig, tlsCfg *tls.Config) *HTTPStager {
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 10,
		IdleConnTimeout:     90 * time.Second,
		TLSClientConfig:     tlsCfg,
	}

	timeout := cfg.Timeout
	if timeout == 0 {
		timeout = 5 * time.Minute
	}

	return &HTTPStager{
		config:    cfg,
		tlsConfig: tlsCfg,
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
		},
	}
}

// WithOverrides creates a copy of the HTTPStager with per-task overrides applied.
func (s *HTTPStager) WithOverrides(o *StagerOverrides) *HTTPStager {
	if o == nil {
		return s
	}

	// Create a shallow copy.
	clone := *s
	clone.overrides = o

	// Apply timeout override.
	if o.HTTPTimeout != nil {
		clone.client = &http.Client{
			Timeout:   *o.HTTPTimeout,
			Transport: s.client.Transport,
		}
	}

	return &clone
}

// StageIn downloads a file from an HTTP/HTTPS URL to destPath.
func (s *HTTPStager) StageIn(ctx context.Context, location string, destPath string) error {
	scheme, _ := cwl.ParseLocationScheme(location)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("http stager: unsupported scheme %q", scheme)
	}

	// Ensure destination directory exists.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("http stager: mkdir: %w", err)
	}

	// Download with retries.
	var lastErr error
	maxRetries := s.config.MaxRetries
	if maxRetries == 0 {
		maxRetries = 1
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := s.retryDelay(attempt)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		err := s.download(ctx, location, destPath)
		if err == nil {
			return nil
		}
		lastErr = err

		// Don't retry on client errors (4xx).
		if isClientError(err) {
			return err
		}
	}

	return fmt.Errorf("http stager: download failed after %d attempts: %w", maxRetries, lastErr)
}

// StageOut uploads a file to the configured HTTP endpoint.
func (s *HTTPStager) StageOut(ctx context.Context, srcPath string, taskID string) (string, error) {
	uploadPath := s.config.UploadPath
	if uploadPath == "" {
		return "", fmt.Errorf("http stager: no upload path configured")
	}

	// Expand URL template.
	filename := filepath.Base(srcPath)
	basename := strings.TrimSuffix(filename, filepath.Ext(filename))

	uploadURL := strings.ReplaceAll(uploadPath, "{taskID}", taskID)
	uploadURL = strings.ReplaceAll(uploadURL, "{filename}", filename)
	uploadURL = strings.ReplaceAll(uploadURL, "{basename}", basename)

	// Upload with retries.
	var lastErr error
	maxRetries := s.config.MaxRetries
	if maxRetries == 0 {
		maxRetries = 1
	}

	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			delay := s.retryDelay(attempt)
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(delay):
			}
		}

		err := s.upload(ctx, srcPath, uploadURL)
		if err == nil {
			return uploadURL, nil
		}
		lastErr = err

		// Don't retry on client errors (4xx).
		if isClientError(err) {
			return "", err
		}
	}

	return "", fmt.Errorf("http stager: upload failed after %d attempts: %w", maxRetries, lastErr)
}

// download performs the actual HTTP GET.
func (s *HTTPStager) download(ctx context.Context, url string, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	s.applyAuth(req)
	s.applyHeaders(req)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &httpError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	// Write to temp file first (atomic).
	tmpPath := destPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	_, err = io.Copy(out, resp.Body)
	if closeErr := out.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write file: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename temp file: %w", err)
	}

	return nil
}

// upload performs the actual HTTP PUT/POST.
func (s *HTTPStager) upload(ctx context.Context, srcPath, uploadURL string) error {
	file, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open file: %w", err)
	}
	defer file.Close()

	// Get file size for Content-Length.
	stat, err := file.Stat()
	if err != nil {
		return fmt.Errorf("stat file: %w", err)
	}

	method := s.config.UploadMethod
	if method == "" {
		method = http.MethodPut
	}

	req, err := http.NewRequestWithContext(ctx, method, uploadURL, file)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.ContentLength = stat.Size()
	req.Header.Set("Content-Type", "application/octet-stream")

	s.applyAuth(req)
	s.applyHeaders(req)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Accept 2xx status codes.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return &httpError{
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	return nil
}

// applyAuth adds authentication to the request based on credentials.
func (s *HTTPStager) applyAuth(req *http.Request) {
	// Check for per-task credential override.
	if s.overrides != nil && s.overrides.HTTPCredential != nil {
		s.applyCred(req, *s.overrides.HTTPCredential)
		return
	}

	// Look up credentials by host.
	cred := s.lookupCredential(req.URL.Host)
	if cred != nil {
		s.applyCred(req, *cred)
	}
}

// applyCred applies a specific credential to the request.
func (s *HTTPStager) applyCred(req *http.Request, cred CredentialSet) {
	switch cred.Type {
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+cred.Token)
	case "basic":
		req.SetBasicAuth(cred.Username, cred.Password)
	case "header":
		if cred.HeaderName != "" {
			req.Header.Set(cred.HeaderName, cred.HeaderValue)
		}
	}
}

// lookupCredential finds credentials for a host, supporting wildcards.
func (s *HTTPStager) lookupCredential(host string) *CredentialSet {
	if s.config.Credentials == nil {
		return nil
	}

	// Exact match first.
	if cred, ok := s.config.Credentials[host]; ok {
		return &cred
	}

	// Try wildcard patterns.
	// Remove port from host for matching.
	hostOnly := host
	if idx := strings.LastIndex(host, ":"); idx > 0 {
		hostOnly = host[:idx]
	}

	// Check *.domain.com patterns.
	parts := strings.Split(hostOnly, ".")
	if len(parts) >= 2 {
		// Try *.example.com
		wildcard := "*." + strings.Join(parts[1:], ".")
		if cred, ok := s.config.Credentials[wildcard]; ok {
			return &cred
		}
	}

	return nil
}

// applyHeaders adds default and override headers to the request.
func (s *HTTPStager) applyHeaders(req *http.Request) {
	// Apply default headers.
	for k, v := range s.config.DefaultHeaders {
		req.Header.Set(k, v)
	}

	// Apply per-task override headers.
	if s.overrides != nil {
		for k, v := range s.overrides.HTTPHeaders {
			req.Header.Set(k, v)
		}
	}
}

// retryDelay calculates the delay for a retry attempt using exponential backoff.
func (s *HTTPStager) retryDelay(attempt int) time.Duration {
	delay := s.config.RetryDelay
	if delay == 0 {
		delay = time.Second
	}

	// Exponential backoff: delay * 2^attempt
	for i := 0; i < attempt; i++ {
		delay *= 2
	}

	// Cap at 30 seconds.
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}

	return delay
}

// httpError represents an HTTP error response.
type httpError struct {
	StatusCode int
	Body       string
}

func (e *httpError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

// isClientError returns true if the error is a 4xx client error.
func isClientError(err error) bool {
	if he, ok := err.(*httpError); ok {
		return he.StatusCode >= 400 && he.StatusCode < 500
	}
	return false
}
