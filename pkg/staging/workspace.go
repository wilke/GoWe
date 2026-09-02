package staging

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/me/gowe/pkg/bvbrc"
)

// WorkspaceConfig contains configuration for the BV-BRC Workspace stager.
type WorkspaceConfig struct {
	// WorkspaceURL is the BV-BRC Workspace service URL.
	// Default: https://p3.theseed.org/services/Workspace
	WorkspaceURL string

	// Token is the default authentication token.
	// Can be overridden per-operation via StageOptions.
	Token string

	// Timeout is the HTTP request timeout.
	Timeout time.Duration

	// MaxRetries is the number of retry attempts for transient failures.
	MaxRetries int
}

// WorkspaceStager handles staging files to/from BV-BRC Workspace (ws:// URIs).
type WorkspaceStager struct {
	config WorkspaceConfig
	client *http.Client
	logger *slog.Logger
}

// NewWorkspaceStager creates a WorkspaceStager with the given configuration.
func NewWorkspaceStager(cfg WorkspaceConfig, logger *slog.Logger) *WorkspaceStager {
	if cfg.WorkspaceURL == "" {
		cfg.WorkspaceURL = bvbrc.DefaultWorkspaceURL
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 5 * time.Minute
	}
	if cfg.MaxRetries == 0 {
		cfg.MaxRetries = 3
	}
	if logger == nil {
		logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return &WorkspaceStager{
		config: cfg,
		client: &http.Client{Timeout: cfg.Timeout},
		logger: logger.With("component", "workspace-stager"),
	}
}

// Config returns the stager's configuration.
func (s *WorkspaceStager) Config() WorkspaceConfig {
	return s.config
}

// Supports returns true for the "ws" scheme.
func (s *WorkspaceStager) Supports(scheme string) bool {
	return scheme == "ws"
}

// WithToken returns a clone of the stager with the given token set as the default.
// This is needed for cases where StageOptions arrives empty (e.g., stageOutputValue).
func (s *WorkspaceStager) WithToken(token string) *WorkspaceStager {
	clone := *s
	clone.config.Token = token
	return &clone
}

// StageIn downloads a file from the BV-BRC Workspace to destPath.
// The location must be a ws:// URI, e.g. ws:///user@bvbrc/home/file.fasta
func (s *WorkspaceStager) StageIn(ctx context.Context, location string, destPath string, opts StageOptions) error {
	wsPath, err := parseWorkspaceURI(location)
	if err != nil {
		return fmt.Errorf("workspace stager: %w", err)
	}

	token := s.resolveToken(opts)
	if token == "" {
		return fmt.Errorf("workspace stager: no authentication token available")
	}

	// Ensure destination directory exists.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("workspace stager: mkdir: %w", err)
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

		err := s.download(ctx, wsPath, destPath, token)
		if err == nil {
			return nil
		}
		lastErr = err
		s.logger.Warn("workspace download attempt failed",
			"path", wsPath, "attempt", attempt+1, "error", err)
	}

	return fmt.Errorf("workspace stager: download failed after %d attempts: %w", s.config.MaxRetries, lastErr)
}

// StageOut uploads a file to the BV-BRC Workspace.
// The destination path is determined from opts.Metadata["destination"] + filename.
// Returns the ws:// URI of the uploaded object.
func (s *WorkspaceStager) StageOut(ctx context.Context, srcPath string, taskID string, opts StageOptions) (string, error) {
	token := s.resolveToken(opts)
	if token == "" {
		return "", fmt.Errorf("workspace stager: no authentication token available")
	}

	// Determine destination workspace path.
	destDir := ""
	if opts.Metadata != nil {
		destDir = opts.Metadata["destination"]
	}
	if destDir == "" {
		return "", fmt.Errorf("workspace stager: no destination in stage-out metadata")
	}
	// Strip ws:// prefix if present (callers may pass the full URI).
	if strings.HasPrefix(destDir, "ws://") {
		destDir = destDir[len("ws://"):]
		if !strings.HasPrefix(destDir, "/") {
			destDir = "/" + destDir
		}
	}

	// Ensure destination directory exists in the workspace.
	if err := s.ensureDir(ctx, destDir, token); err != nil {
		return "", fmt.Errorf("workspace stager: ensure dir: %w", err)
	}

	filename := filepath.Base(srcPath)
	destPath := strings.TrimRight(destDir, "/") + "/" + filename

	// Upload with retries. Each attempt re-opens the source and streams it:
	// the upload verifies what the service stored and a failed attempt deletes
	// its placeholder, so a fresh attempt always starts from a clean object.
	var lastErr error
	for attempt := 0; attempt < s.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		err := s.uploadFile(ctx, destPath, srcPath, token)
		if err == nil {
			return "ws://" + destPath, nil
		}
		lastErr = err
		s.logger.Warn("workspace upload attempt failed",
			"path", destPath, "attempt", attempt+1, "error", err)
	}

	return "", fmt.Errorf("workspace stager: upload failed after %d attempts: %w", s.config.MaxRetries, lastErr)
}

// download fetches a file from the workspace via the download URL API.
func (s *WorkspaceStager) download(ctx context.Context, wsPath string, destPath string, token string) error {
	urls, err := s.newClient(token).WorkspaceGetDownloadURL(ctx, []string{wsPath})
	if err != nil {
		return fmt.Errorf("get download URL for %s: %w", wsPath, err)
	}

	downloadURL, ok := urls[wsPath]
	if !ok || downloadURL == "" {
		return fmt.Errorf("no download URL returned for %s", wsPath)
	}

	// HTTP GET the download URL with auth.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "OAuth "+token)

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("download request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("download HTTP %d: %s", resp.StatusCode, string(body))
	}

	// Write to temp file, then atomic rename.
	tmpPath := destPath + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}

	// A completed copy and a correct copy are different claims: a nil error
	// from io.Copy only means the write syscalls were accepted, not that the
	// bytes reached stable storage (delayed-write errors surface at
	// Sync/Close) and not that the byte count matches what the server
	// advertised. Sync and Close are both checked, and when the workspace
	// download URL sent Content-Length, the written count is verified
	// against it.
	written, err := io.Copy(f, resp.Body)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); closeErr != nil && err == nil {
		err = closeErr
	}
	if err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("write file: %w", err)
	}
	if resp.ContentLength >= 0 && written != resp.ContentLength {
		os.Remove(tmpPath)
		return fmt.Errorf("workspace download truncated for %s: expected %d bytes (Content-Length), got %d bytes", destPath, resp.ContentLength, written)
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("rename: %w", err)
	}

	return nil
}

// UploadContent uploads string content directly to a workspace path (no local file needed).
// Returns the ws:// URI of the created object.
func (s *WorkspaceStager) UploadContent(ctx context.Context, destPath string, content string, opts StageOptions) (string, error) {
	token := s.resolveToken(opts)
	if token == "" {
		return "", fmt.Errorf("workspace stager: no authentication token available")
	}

	// Ensure parent directory exists.
	dir := destPath[:strings.LastIndex(destPath, "/")]
	if dir != "" {
		if err := s.ensureDir(ctx, dir, token); err != nil {
			return "", fmt.Errorf("workspace stager: ensure dir: %w", err)
		}
	}

	var lastErr error
	for attempt := 0; attempt < s.config.MaxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * time.Second):
			}
		}

		err := s.upload(ctx, destPath, []byte(content), token)
		if err == nil {
			return "ws://" + destPath, nil
		}
		lastErr = err
		s.logger.Warn("workspace upload content attempt failed",
			"path", destPath, "attempt", attempt+1, "error", err)
	}

	return "", fmt.Errorf("workspace stager: upload content failed after %d attempts: %w", s.config.MaxRetries, lastErr)
}

// newClient builds a Workspace client for one operation with the given token.
// The client retries transient JSON-RPC failures itself (MaxRetries with the
// package default backoff), so a hiccup on the metadata refresh that follows
// a good Shock PUT is retried at the RPC level rather than by throwing the
// upload away and streaming the whole file again.
func (s *WorkspaceStager) newClient(token string) *bvbrc.Client {
	return bvbrc.NewClient(bvbrc.Config{
		WorkspaceURL: s.config.WorkspaceURL,
		Token:        token,
		Timeout:      s.config.Timeout,
		MaxRetries:   s.config.MaxRetries,
		RetryDelay:   bvbrc.DefaultRetryDelay,
	}, s.logger)
}

// uploadFile streams the local file at srcPath into the workspace object at
// wsPath, verifying the stored size against the local one.
//
// The bytes travel through Shock (Workspace.create with createUploadNodes, then a
// multipart PUT to the returned node URL), never through Workspace.create's inline
// JSON string field — that field runs the content through encoding/json, which
// replaces every byte that is not valid UTF-8 with U+FFFD and silently corrupts
// every binary output. See issue #172 and bvbrc.Client.WorkspaceUploadReader.
func (s *WorkspaceStager) uploadFile(ctx context.Context, wsPath, srcPath, token string) error {
	f, err := os.Open(srcPath)
	if err != nil {
		return fmt.Errorf("open source: %w", err)
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat source: %w", err)
	}

	obj, err := s.newClient(token).WorkspaceUploadReader(ctx, wsPath, filepath.Base(srcPath), f, st.Size(), bvbrc.WorkspaceTypeUnspecified)
	if err != nil {
		return fmt.Errorf("upload to %s: %w", wsPath, err)
	}

	s.logger.Info("workspace upload verified", "path", wsPath, "size", obj.Size)
	return nil
}

// upload creates/overwrites a file in the workspace from in-memory bytes
// (manifests). Routed through the same verified Shock path as uploadFile.
func (s *WorkspaceStager) upload(ctx context.Context, wsPath string, data []byte, token string) error {
	_, err := s.newClient(token).WorkspaceUploadFile(ctx, wsPath, data, bvbrc.WorkspaceTypeUnspecified)
	if err != nil {
		return fmt.Errorf("upload to %s: %w", wsPath, err)
	}

	return nil
}

// ensureDir creates the destination directory (and any missing ancestors) in the workspace.
// It walks from the user's home directory down to destDir, creating any missing folders.
// Already-existing directories are skipped; other failures are logged and the
// walk continues, since the subsequent upload reports a missing directory anyway.
func (s *WorkspaceStager) ensureDir(ctx context.Context, destDir string, token string) error {
	destDir = strings.TrimRight(destDir, "/")
	if destDir == "" {
		return nil
	}

	wsClient := s.newClient(token)

	// Split path into components: /user@bvbrc/home/Reports/1
	// The first two components (/user@bvbrc/home) always exist, so start from the third.
	parts := strings.Split(destDir, "/")
	// parts[0] = "", parts[1] = "user@bvbrc", parts[2] = "home", parts[3..] = subdirs
	if len(parts) <= 3 {
		return nil // /user@bvbrc/home always exists
	}

	for i := 3; i < len(parts); i++ {
		dir := strings.Join(parts[:i+1], "/")
		_, err := wsClient.WorkspaceCreateFolder(ctx, dir)
		switch {
		case err == nil:
			s.logger.Info("workspace mkdir created", "path", dir)
		case bvbrc.IsExistsError(err):
			s.logger.Debug("workspace mkdir skipped, folder exists", "path", dir)
		default:
			s.logger.Warn("workspace mkdir failed", "path", dir, "error", err)
		}
	}

	return nil
}

// resolveToken determines the authentication token for an operation.
// Priority: opts.Token > opts.Credentials.Token > config.Token
func (s *WorkspaceStager) resolveToken(opts StageOptions) string {
	if opts.Token != "" {
		return opts.Token
	}
	if opts.Credentials != nil && opts.Credentials.Token != "" {
		return opts.Credentials.Token
	}
	return s.config.Token
}

// parseWorkspaceURI extracts the workspace path from a ws:// URI.
// ws:///user@bvbrc/home/file.fasta → /user@bvbrc/home/file.fasta
func parseWorkspaceURI(uri string) (string, error) {
	scheme, path := ParseLocationScheme(uri)
	if scheme != "ws" {
		return "", fmt.Errorf("unsupported scheme %q, expected ws", scheme)
	}
	// Normalize: ensure leading slash.
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if path == "/" || path == "" {
		return "", fmt.Errorf("empty workspace path in URI: %s", uri)
	}
	return path, nil
}

// Verify interface compliance.
var _ Stager = (*WorkspaceStager)(nil)
