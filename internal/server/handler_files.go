package server

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// FileUploadConfig configures the file upload proxy endpoint.
type FileUploadConfig struct {
	// Enabled enables the /api/v1/files upload endpoint.
	Enabled bool

	// Backend specifies where uploaded files are stored.
	// Supported values: "shock", "s3", "local"
	Backend string

	// MaxSize is the maximum upload size in bytes (default: 1GB).
	MaxSize int64

	// TempDir is the temporary directory for buffering uploads.
	// If empty, uses os.TempDir().
	TempDir string

	// AllowedDownloadDirs is the list of directories from which files
	// can be downloaded via the GET /api/v1/files/download endpoint.
	// Paths are validated to prevent directory traversal.
	AllowedDownloadDirs []string

	// Shock configuration (when Backend == "shock")
	Shock ShockUploadConfig

	// S3 configuration (when Backend == "s3")
	S3 S3UploadConfig

	// Local configuration (when Backend == "local")
	Local LocalUploadConfig
}

// ShockUploadConfig configures Shock uploads.
type ShockUploadConfig struct {
	Host    string // Shock server host (e.g., "localhost:7445")
	UseHTTP bool   // Use HTTP instead of HTTPS
	Token   string // Default authentication token (optional)
}

// S3UploadConfig configures S3 uploads.
type S3UploadConfig struct {
	Endpoint        string // Custom endpoint for MinIO/Wasabi (empty = AWS)
	Region          string // AWS region (default: us-east-1)
	Bucket          string // Target bucket for uploads
	Prefix          string // Key prefix (default: "uploads/")
	AccessKeyID     string // AWS access key
	SecretAccessKey string // AWS secret key
	UsePathStyle    bool   // Required for MinIO
	DisableSSL      bool   // For local development
}

// LocalUploadConfig configures local filesystem uploads.
type LocalUploadConfig struct {
	Dir string // Directory to store uploaded files
}

// DefaultFileUploadConfig returns sensible defaults.
func DefaultFileUploadConfig() FileUploadConfig {
	return FileUploadConfig{
		Enabled: false,
		Backend: "local",
		MaxSize: 1 << 30, // 1GB
		S3: S3UploadConfig{
			Region: "us-east-1",
			Prefix: "uploads/",
		},
	}
}

// WithFileUploadConfig sets the file upload configuration.
func WithFileUploadConfig(cfg *FileUploadConfig) Option {
	return func(s *Server) {
		s.fileUploadConfig = cfg
	}
}

// handleUploadFile handles POST /api/v1/files
// Accepts multipart form with a "file" field.
// Returns the location URI of the uploaded file.
func (s *Server) handleUploadFile(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	if s.fileUploadConfig == nil || !s.fileUploadConfig.Enabled {
		respondError(w, reqID, http.StatusNotFound, &model.APIError{
			Code:    "FILE_UPLOAD_DISABLED",
			Message: "file upload endpoint is not enabled",
		})
		return
	}

	cfg := s.fileUploadConfig

	// Limit request size
	r.Body = http.MaxBytesReader(w, r.Body, cfg.MaxSize)

	// Parse multipart form
	if err := r.ParseMultipartForm(32 << 20); err != nil { // 32MB memory limit
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: fmt.Sprintf("parse form: %v", err),
		})
		return
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "no file provided in 'file' field",
		})
		return
	}
	defer file.Close()

	// Create temporary file
	tempDir := cfg.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	tmpFile, err := os.CreateTemp(tempDir, "upload-*")
	if err != nil {
		s.logger.Error("create temp file", "error", err)
		respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
			Code:    model.ErrInternal,
			Message: "failed to create temp file",
		})
		return
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	// Copy uploaded file to temp
	if _, err := io.Copy(tmpFile, file); err != nil {
		tmpFile.Close()
		s.logger.Error("write temp file", "error", err)
		respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
			Code:    model.ErrInternal,
			Message: "failed to write temp file",
		})
		return
	}
	tmpFile.Close()

	// Stage to configured backend
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()

	var location string
	switch cfg.Backend {
	case "shock":
		location, err = s.uploadToShock(ctx, tmpPath, header.Filename, cfg)
	case "s3":
		location, err = s.uploadToS3(ctx, tmpPath, header.Filename, cfg)
	case "local":
		location, err = s.uploadToLocal(ctx, tmpPath, header.Filename, cfg)
	default:
		respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
			Code:    model.ErrInternal,
			Message: fmt.Sprintf("unknown upload backend: %s", cfg.Backend),
		})
		return
	}

	if err != nil {
		s.logger.Error("upload file", "backend", cfg.Backend, "error", err)
		respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
			Code:    "UPLOAD_FAILED",
			Message: fmt.Sprintf("upload failed: %v", err),
		})
		return
	}

	s.logger.Info("file uploaded", "backend", cfg.Backend, "filename", header.Filename, "location", location)

	respondCreated(w, reqID, map[string]any{
		"location": location,
		"filename": header.Filename,
		"size":     header.Size,
	})
}

// uploadToShock uploads a file to Shock and returns the location URI.
func (s *Server) uploadToShock(ctx context.Context, tmpPath, filename string, cfg *FileUploadConfig) (string, error) {
	stager := staging.NewShockStager(staging.ShockConfig{
		DefaultHost: cfg.Shock.Host,
		Token:       cfg.Shock.Token,
		UseHTTP:     cfg.Shock.UseHTTP,
		Timeout:     5 * time.Minute,
		MaxRetries:  3,
	})

	// Use filename as "taskID" for metadata
	location, err := stager.StageOut(ctx, tmpPath, "upload-"+filepath.Base(filename), staging.StageOptions{
		Metadata: map[string]string{
			"original_filename": filename,
			"upload_time":       time.Now().UTC().Format(time.RFC3339),
		},
	})
	if err != nil {
		return "", fmt.Errorf("shock upload: %w", err)
	}

	return location, nil
}

// uploadToS3 uploads a file to S3 and returns the location URI.
func (s *Server) uploadToS3(ctx context.Context, tmpPath, filename string, cfg *FileUploadConfig) (string, error) {
	stager, err := staging.NewS3Stager(staging.S3Config{
		Endpoint:        cfg.S3.Endpoint,
		Region:          cfg.S3.Region,
		AccessKeyID:     cfg.S3.AccessKeyID,
		SecretAccessKey: cfg.S3.SecretAccessKey,
		UsePathStyle:    cfg.S3.UsePathStyle,
		DisableSSL:      cfg.S3.DisableSSL,
		DefaultBucket:   cfg.S3.Bucket,
		StageOutPrefix:  cfg.S3.Prefix,
	})
	if err != nil {
		return "", fmt.Errorf("create s3 stager: %w", err)
	}

	// Use timestamp + filename as key
	taskID := time.Now().UTC().Format("20060102-150405")
	location, err := stager.StageOut(ctx, tmpPath, taskID, staging.StageOptions{})
	if err != nil {
		return "", fmt.Errorf("s3 upload: %w", err)
	}

	return location, nil
}

// uploadToLocal copies a file to local storage and returns a file:// URI.
func (s *Server) uploadToLocal(ctx context.Context, tmpPath, filename string, cfg *FileUploadConfig) (string, error) {
	if cfg.Local.Dir == "" {
		return "", fmt.Errorf("local upload directory not configured")
	}

	// Create unique subdirectory
	subDir := time.Now().UTC().Format("20060102-150405")
	destDir := filepath.Join(cfg.Local.Dir, subDir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", fmt.Errorf("create dir: %w", err)
	}

	destPath := filepath.Join(destDir, filepath.Base(filename))

	// Copy file
	if err := staging.CopyFile(tmpPath, destPath); err != nil {
		return "", fmt.Errorf("copy file: %w", err)
	}

	return "file://" + destPath, nil
}

// handleDownloadFile handles GET /api/v1/files/download?location=file:///path/to/file
// For regular files, it serves the raw bytes via http.ServeFile.
// For directories, it returns a JSON listing of entries.
func (s *Server) handleDownloadFile(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	if s.fileUploadConfig == nil || !s.fileUploadConfig.Enabled {
		respondError(w, reqID, http.StatusNotFound, &model.APIError{
			Code:    "FILE_DOWNLOAD_DISABLED",
			Message: "file download endpoint is not enabled",
		})
		return
	}

	location := r.URL.Query().Get("location")
	if location == "" {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "missing 'location' query parameter",
		})
		return
	}

	// Parse file:// URI to local path.
	filePath := location
	if strings.HasPrefix(filePath, "file://") {
		filePath = strings.TrimPrefix(filePath, "file://")
	}

	// Clean the path to prevent directory traversal.
	filePath = filepath.Clean(filePath)

	// Validate path is under an allowed directory.
	if !s.isPathAllowed(filePath) {
		respondError(w, reqID, http.StatusForbidden, &model.APIError{
			Code:    "DOWNLOAD_FORBIDDEN",
			Message: "path is not in an allowed download directory",
		})
		return
	}

	info, err := os.Stat(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			respondError(w, reqID, http.StatusNotFound, &model.APIError{
				Code:    "FILE_NOT_FOUND",
				Message: fmt.Sprintf("file not found: %s", location),
			})
			return
		}
		respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
			Code:    model.ErrInternal,
			Message: fmt.Sprintf("stat file: %v", err),
		})
		return
	}

	if info.IsDir() {
		// Return JSON listing of directory entries.
		s.serveDirectoryListing(w, reqID, filePath)
		return
	}

	// Serve file as raw bytes.
	s.logger.Debug("serving file download", "path", filePath, "size", info.Size())
	http.ServeFile(w, r, filePath)
}

// serveDirectoryListing returns a JSON listing of directory entries.
func (s *Server) serveDirectoryListing(w http.ResponseWriter, reqID string, dirPath string) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
			Code:    model.ErrInternal,
			Message: fmt.Sprintf("read directory: %v", err),
		})
		return
	}

	listing := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		entryPath := filepath.Join(dirPath, entry.Name())
		item := map[string]any{
			"basename": entry.Name(),
			"location": "file://" + entryPath,
			"is_dir":   entry.IsDir(),
		}
		if info, err := entry.Info(); err == nil {
			item["size"] = info.Size()
		}
		listing = append(listing, item)
	}

	respondOK(w, reqID, listing)
}

// isPathAllowed checks if a file path is under one of the allowed download directories.
func (s *Server) isPathAllowed(filePath string) bool {
	if s.fileUploadConfig == nil || len(s.fileUploadConfig.AllowedDownloadDirs) == 0 {
		return false
	}

	resolved, err := filepath.EvalSymlinks(filePath)
	if err != nil {
		return false
	}
	cleanPath := filepath.Clean(resolved)
	for _, dir := range s.fileUploadConfig.AllowedDownloadDirs {
		resolvedDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			continue
		}
		cleanDir := filepath.Clean(resolvedDir)
		// Path must be under the allowed directory (prefix + separator check).
		if cleanPath == cleanDir || strings.HasPrefix(cleanPath, cleanDir+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
