package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"slices"

	"github.com/me/gowe/pkg/model"
)

const ctxKeyWorkerAuth ctxKey = "worker_auth"

// WorkerAuthContext holds authenticated worker info for a request.
type WorkerAuthContext struct {
	KeyID  string   // Hash of the key (for logging, not the raw key)
	Groups []string // Groups this key can join
}

// WorkerAuthFromContext extracts the WorkerAuthContext from request context.
func WorkerAuthFromContext(ctx context.Context) *WorkerAuthContext {
	if wc, ok := ctx.Value(ctxKeyWorkerAuth).(*WorkerAuthContext); ok {
		return wc
	}
	return nil
}

// WorkerKeyConfig holds worker key to group mappings.
type WorkerKeyConfig struct {
	Keys map[string]WorkerKeyEntry `json:"keys"`
}

// WorkerKeyEntry defines the groups and metadata for a worker key.
type WorkerKeyEntry struct {
	Groups      []string `json:"groups"`
	Description string   `json:"description,omitempty"`
}

// LoadWorkerKeyConfig loads worker key configuration from multiple sources.
// Priority: 1. JSON file, 2. Environment variable (GOWE_WORKER_KEYS)
func LoadWorkerKeyConfig(configFile string) *WorkerKeyConfig {
	cfg := &WorkerKeyConfig{
		Keys: make(map[string]WorkerKeyEntry),
	}

	// Try loading from config file.
	if configFile != "" {
		if data, err := os.ReadFile(configFile); err == nil {
			var fileCfg WorkerKeyConfig
			if err := json.Unmarshal(data, &fileCfg); err == nil {
				for k, v := range fileCfg.Keys {
					cfg.Keys[k] = v
				}
			}
		}
	}

	// Load from environment variable (JSON format).
	// Format: {"key1": ["group1", "group2"], "key2": ["default"]}
	if envVal := os.Getenv("GOWE_WORKER_KEYS"); envVal != "" {
		var envKeys map[string][]string
		if err := json.Unmarshal([]byte(envVal), &envKeys); err == nil {
			for key, groups := range envKeys {
				cfg.Keys[key] = WorkerKeyEntry{Groups: groups}
			}
		}
	}

	return cfg
}

// ValidateKey checks if a key is valid and returns its allowed groups.
// Returns nil if the key is invalid.
func (c *WorkerKeyConfig) ValidateKey(key string) *WorkerKeyEntry {
	if entry, ok := c.Keys[key]; ok {
		return &entry
	}
	return nil
}

// IsEnabled returns true if any worker keys are configured.
func (c *WorkerKeyConfig) IsEnabled() bool {
	return len(c.Keys) > 0
}

// hashKey creates a short hash of the key for logging purposes.
func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:8]) // First 16 hex chars
}

// workerAuthMiddleware validates X-Worker-Key header for worker endpoints.
// If no keys are configured, authentication is disabled (open access).
func workerAuthMiddleware(keyConfig *WorkerKeyConfig, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestIDFromContext(r.Context())

			// If no keys are configured, allow open access.
			if keyConfig == nil || !keyConfig.IsEnabled() {
				// Create anonymous worker context with default group.
				workerCtx := &WorkerAuthContext{
					KeyID:  "none",
					Groups: []string{"default"},
				}
				ctx := context.WithValue(r.Context(), ctxKeyWorkerAuth, workerCtx)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Extract worker key from header.
			key := r.Header.Get("X-Worker-Key")
			if key == "" {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "worker authentication required (X-Worker-Key header missing)",
				})
				return
			}

			// Validate key.
			entry := keyConfig.ValidateKey(key)
			if entry == nil {
				logger.Warn("invalid worker key", "key_hash", hashKey(key))
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "invalid worker key",
				})
				return
			}

			// Build worker auth context.
			workerCtx := &WorkerAuthContext{
				KeyID:  hashKey(key),
				Groups: entry.Groups,
			}

			ctx := context.WithValue(r.Context(), ctxKeyWorkerAuth, workerCtx)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// CanJoinGroup checks if the worker auth context allows joining the specified group.
func (c *WorkerAuthContext) CanJoinGroup(group string) bool {
	if c == nil {
		return false
	}
	// Empty groups list means any group is allowed.
	if len(c.Groups) == 0 {
		return true
	}
	return slices.Contains(c.Groups, group)
}
