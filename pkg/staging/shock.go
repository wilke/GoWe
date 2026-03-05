package staging

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ShockConfig contains Shock stager configuration.
type ShockConfig struct {
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

// ShockStager handles Shock data storage (BV-BRC).
type ShockStager struct {
	config ShockConfig
	client *http.Client
}

// ShockNode represents a Shock node response.
type ShockNode struct {
	ID         string         `json:"id"`
	File       ShockFile      `json:"file"`
	Attributes map[string]any `json:"attributes"`
	ACL        ShockACL       `json:"acl,omitempty"`
}

// ShockFile represents file metadata in a Shock node.
type ShockFile struct {
	Name     string            `json:"name"`
	Size     int64             `json:"size"`
	Checksum map[string]string `json:"checksum,omitempty"`
}

// ShockACL represents access control for a Shock node.
type ShockACL struct {
	Read   []string `json:"read,omitempty"`
	Write  []string `json:"write,omitempty"`
	Delete []string `json:"delete,omitempty"`
	Owner  string   `json:"owner,omitempty"`
	Public struct {
		Read   bool `json:"read"`
		Write  bool `json:"write"`
		Delete bool `json:"delete"`
	} `json:"public,omitempty"`
}

// shockResponse is the standard Shock API response wrapper.
type shockResponse struct {
	Status int        `json:"status"`
	Error  []string   `json:"error"`
	Data   *ShockNode `json:"data"`
}

// NewShockStager creates a ShockStager with the given configuration.
func NewShockStager(cfg ShockConfig) *ShockStager {
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}

	return &ShockStager{
		config: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
		},
	}
}

// scheme returns "http" or "https" based on config.
func (s *ShockStager) scheme() string {
	if s.config.UseHTTP {
		return "http"
	}
	return "https"
}

// StageIn downloads a file from Shock to destPath.
// Supports URIs like:
//   - shock://host/node/{nodeID}
//   - shock://host/node/{nodeID}?download
func (s *ShockStager) StageIn(ctx context.Context, location string, destPath string, opts StageOptions) error {
	host, nodeID, err := parseShockURI(location, s.config.DefaultHost)
	if err != nil {
		return fmt.Errorf("shock stager: %w", err)
	}

	// Build download URL.
	downloadURL := fmt.Sprintf("%s://%s/node/%s?download", s.scheme(), host, nodeID)

	// Ensure destination directory exists.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("shock stager: mkdir: %w", err)
	}

	// Download with retries.
	var lastErr error
	for attempt := 0; attempt < s.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		err := s.download(ctx, downloadURL, destPath, opts)
		if err == nil {
			return nil
		}
		lastErr = err
	}

	return fmt.Errorf("shock stager: download failed after %d attempts: %w", s.config.MaxRetries, lastErr)
}

// StageOut uploads a file to Shock and returns the location URI.
func (s *ShockStager) StageOut(ctx context.Context, srcPath string, taskID string, opts StageOptions) (string, error) {
	host := s.config.DefaultHost
	if host == "" {
		return "", fmt.Errorf("shock stager: no default host configured")
	}

	// Upload with retries.
	var lastErr error
	var node *ShockNode
	for attempt := 0; attempt < s.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		var err error
		node, err = s.upload(ctx, host, srcPath, taskID, opts)
		if err == nil {
			break
		}
		lastErr = err
	}

	if node == nil {
		return "", fmt.Errorf("shock stager: upload failed after %d attempts: %w", s.config.MaxRetries, lastErr)
	}

	return fmt.Sprintf("shock://%s/node/%s", host, node.ID), nil
}

// Supports returns true for shock scheme.
func (s *ShockStager) Supports(scheme string) bool {
	return scheme == "shock"
}

// download fetches a file from Shock.
func (s *ShockStager) download(ctx context.Context, downloadURL string, destPath string, opts StageOptions) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	s.applyAuth(req, opts)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Write to temp file first (atomic).
	tmpPath := destPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create file: %w", err)
	}

	_, err = io.Copy(f, resp.Body)
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write file: %w", err)
	}

	// Atomic rename.
	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// upload creates a new node and uploads a file to Shock.
func (s *ShockStager) upload(ctx context.Context, host string, srcPath string, taskID string, opts StageOptions) (*ShockNode, error) {
	// Open source file.
	f, err := os.Open(srcPath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	// Build multipart form.
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Add attributes if metadata provided.
	attrs := map[string]any{
		"task_id": taskID,
	}
	if len(opts.Metadata) > 0 {
		for k, v := range opts.Metadata {
			attrs[k] = v
		}
	}
	attrsJSON, _ := json.Marshal(attrs)
	if err := writer.WriteField("attributes_str", string(attrsJSON)); err != nil {
		return nil, fmt.Errorf("write attributes: %w", err)
	}

	// Add file.
	part, err := writer.CreateFormFile("upload", filepath.Base(srcPath))
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}

	// For small files, read into buffer.
	// For large files, we'd need streaming multipart, but Shock API typically
	// requires Content-Length, so we buffer anyway.
	if stat.Size() < 100*1024*1024 { // < 100MB
		if _, err := io.Copy(part, f); err != nil {
			return nil, fmt.Errorf("copy file: %w", err)
		}
	} else {
		// For large files, stream the upload.
		if _, err := io.Copy(part, f); err != nil {
			return nil, fmt.Errorf("copy large file: %w", err)
		}
	}

	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close multipart: %w", err)
	}

	// Create node with upload.
	uploadURL := fmt.Sprintf("%s://%s/node", s.scheme(), host)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, &body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Content-Type", writer.FormDataContentType())
	s.applyAuth(req, opts)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Parse response.
	var shockResp shockResponse
	if err := json.NewDecoder(resp.Body).Decode(&shockResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if shockResp.Status != 200 || len(shockResp.Error) > 0 {
		return nil, fmt.Errorf("shock error: %v", shockResp.Error)
	}

	if shockResp.Data == nil {
		return nil, fmt.Errorf("no node data in response")
	}

	return shockResp.Data, nil
}

// GetNode retrieves node metadata from Shock.
func (s *ShockStager) GetNode(ctx context.Context, host, nodeID string, opts StageOptions) (*ShockNode, error) {
	nodeURL := fmt.Sprintf("%s://%s/node/%s", s.scheme(), host, nodeID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, nodeURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}

	s.applyAuth(req, opts)

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	var shockResp shockResponse
	if err := json.NewDecoder(resp.Body).Decode(&shockResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}

	if shockResp.Status != 200 || len(shockResp.Error) > 0 {
		return nil, fmt.Errorf("shock error: %v", shockResp.Error)
	}

	return shockResp.Data, nil
}

// SetACL updates the ACL for a Shock node.
func (s *ShockStager) SetACL(ctx context.Context, host, nodeID string, acl ShockACL, opts StageOptions) error {
	// Shock ACL API: PUT /node/{id}/acl/{type}?users={user1,user2}
	// or for public: PUT /node/{id}/acl/public_{type}

	// Set public read if specified.
	if acl.Public.Read {
		if err := s.setPublicACL(ctx, host, nodeID, "read", opts); err != nil {
			return err
		}
	}

	// Add read users.
	for _, user := range acl.Read {
		if err := s.addACLUser(ctx, host, nodeID, "read", user, opts); err != nil {
			return err
		}
	}

	// Add write users.
	for _, user := range acl.Write {
		if err := s.addACLUser(ctx, host, nodeID, "write", user, opts); err != nil {
			return err
		}
	}

	return nil
}

// setPublicACL makes a node publicly readable/writable.
func (s *ShockStager) setPublicACL(ctx context.Context, host, nodeID, aclType string, opts StageOptions) error {
	aclURL := fmt.Sprintf("%s://%s/node/%s/acl/public_%s", s.scheme(), host, nodeID, aclType)

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, aclURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	s.applyAuth(req, opts)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("set public ACL failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// addACLUser adds a user to an ACL.
func (s *ShockStager) addACLUser(ctx context.Context, host, nodeID, aclType, user string, opts StageOptions) error {
	aclURL := fmt.Sprintf("%s://%s/node/%s/acl/%s?users=%s", s.scheme(), host, nodeID, aclType, url.QueryEscape(user))

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, aclURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	s.applyAuth(req, opts)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("add ACL user failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// DeleteNode deletes a node from Shock.
func (s *ShockStager) DeleteNode(ctx context.Context, host, nodeID string, opts StageOptions) error {
	deleteURL := fmt.Sprintf("%s://%s/node/%s", s.scheme(), host, nodeID)

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, deleteURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	s.applyAuth(req, opts)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("delete node failed: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// applyAuth adds authentication to the request.
func (s *ShockStager) applyAuth(req *http.Request, opts StageOptions) {
	// Priority: opts.Token > opts.Credentials > config.Token

	if opts.Token != "" {
		req.Header.Set("Authorization", "OAuth "+opts.Token)
		return
	}

	if opts.Credentials != nil && opts.Credentials.Token != "" {
		req.Header.Set("Authorization", "OAuth "+opts.Credentials.Token)
		return
	}

	if s.config.Token != "" {
		req.Header.Set("Authorization", "OAuth "+s.config.Token)
	}
	// No auth = anonymous access
}

// parseShockURI parses a Shock URI and returns host and node ID.
// Supports formats:
//   - shock://host/node/{nodeID}
//   - shock://{nodeID} (uses default host)
//   - shock://host/node/{nodeID}?download
func parseShockURI(uri string, defaultHost string) (host, nodeID string, err error) {
	scheme, path := ParseLocationScheme(uri)
	if scheme != "shock" {
		return "", "", fmt.Errorf("unsupported scheme %q, expected shock", scheme)
	}

	// Remove query string if present.
	if idx := strings.Index(path, "?"); idx >= 0 {
		path = path[:idx]
	}

	// Parse path: could be "host/node/id" or just "id" (with default host).
	parts := strings.Split(path, "/")

	if len(parts) == 1 && parts[0] != "" {
		// Just nodeID.
		if defaultHost == "" {
			return "", "", fmt.Errorf("no host in URI and no default host configured")
		}
		return defaultHost, parts[0], nil
	}

	if len(parts) >= 3 && parts[1] == "node" {
		// Format: host/node/nodeID
		return parts[0], parts[2], nil
	}

	if len(parts) >= 2 && parts[0] == "node" {
		// Format: node/nodeID (use default host)
		if defaultHost == "" {
			return "", "", fmt.Errorf("no host in URI and no default host configured")
		}
		return defaultHost, parts[1], nil
	}

	return "", "", fmt.Errorf("invalid Shock URI format: %s", uri)
}

// Verify interface compliance.
var _ Stager = (*ShockStager)(nil)
