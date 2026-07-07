package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

const ctxKeyWorkerAuth ctxKey = "worker_auth"

// WorkerAuthContext holds authenticated worker info for a request.
type WorkerAuthContext struct {
	KeyID  string   // Stable key identifier (or key hash) for attribution/logging
	Groups []string // Groups this key can join
}

// WorkerAuthFromContext extracts the WorkerAuthContext from request context.
func WorkerAuthFromContext(ctx context.Context) *WorkerAuthContext {
	if wc, ok := ctx.Value(ctxKeyWorkerAuth).(*WorkerAuthContext); ok {
		return wc
	}
	return nil
}

// WorkerKeyConfig holds statically-configured worker keys (from a JSON file or
// the GOWE_WORKER_KEYS env var). These are the legacy/bootstrap keys; keys minted
// via the admin API are stored hashed in the database instead (see model.WorkerKey).
type WorkerKeyConfig struct {
	Keys map[string]WorkerKeyEntry `json:"keys"`

	// hashIndex maps sha256(rawKey) -> entry so lookups do not require iterating
	// or holding the raw key for comparison. Built lazily from Keys.
	hashIndex map[string]*WorkerKeyEntry
}

// WorkerKeyEntry defines the groups, identity, and lifecycle for a worker key.
type WorkerKeyEntry struct {
	// ID is a stable, human-attributable identifier/label for the key. It is
	// logged (never the raw key) so a specific key can be traced and revoked.
	ID          string   `json:"id,omitempty"`
	Groups      []string `json:"groups"`
	Description string   `json:"description,omitempty"`
	// Disabled revokes the key without removing it from the config.
	Disabled bool `json:"disabled,omitempty"`
	// ExpiresAt, when set, makes the key invalid at and after that time.
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// hashSentinelPrefix lets operators store a key by its hash in config instead of
// the raw secret, e.g. {"sha256:<hex>": {"groups": [...]}}. Keys given as raw
// secrets are hashed on load, so the raw value never needs to leave config.
const hashSentinelPrefix = "sha256:"

// LoadWorkerKeyConfig loads worker key configuration from multiple sources.
// Priority: 1. JSON file, 2. Environment variable (GOWE_WORKER_KEYS).
//
// A configured source that cannot be read or parsed is a hard error rather than
// a silent fallback: degrading to an empty config would leave worker auth open
// (see Enabled) while the operator believes it is enforced. Callers MUST NOT
// enable the server on error.
func LoadWorkerKeyConfig(configFile string) (*WorkerKeyConfig, error) {
	cfg := &WorkerKeyConfig{
		Keys: make(map[string]WorkerKeyEntry),
	}

	// Load from config file, if one was configured.
	if configFile != "" {
		data, err := os.ReadFile(configFile)
		if err != nil {
			return nil, fmt.Errorf("read worker-keys file %q: %w", configFile, err)
		}
		var fileCfg WorkerKeyConfig
		if err := json.Unmarshal(data, &fileCfg); err != nil {
			return nil, fmt.Errorf("parse worker-keys file %q: %w", configFile, err)
		}
		for k, v := range fileCfg.Keys {
			cfg.Keys[k] = v
		}
	}

	// Load from environment variable (JSON format).
	// Format: {"key1": ["group1", "group2"], "key2": ["default"]}
	if envVal := os.Getenv("GOWE_WORKER_KEYS"); envVal != "" {
		var envKeys map[string][]string
		if err := json.Unmarshal([]byte(envVal), &envKeys); err != nil {
			return nil, fmt.Errorf("parse GOWE_WORKER_KEYS: %w", err)
		}
		for key, groups := range envKeys {
			cfg.Keys[key] = WorkerKeyEntry{Groups: groups}
		}
	}

	cfg.build()
	return cfg, nil
}

// build constructs the hash index from Keys. Entries whose map key uses the
// "sha256:" sentinel are indexed by that hash directly; raw keys are hashed.
func (c *WorkerKeyConfig) build() {
	c.hashIndex = make(map[string]*WorkerKeyEntry, len(c.Keys))
	for k, entry := range c.Keys {
		entry := entry // capture per-iteration copy
		var hash string
		if strings.HasPrefix(k, hashSentinelPrefix) {
			hash = strings.TrimPrefix(k, hashSentinelPrefix)
		} else {
			hash = model.HashWorkerKey(k)
		}
		c.hashIndex[hash] = &entry
	}
}

// ValidateKey checks if a key is present in the static config and returns its
// entry (regardless of lifecycle state). Returns nil if the key is unknown.
func (c *WorkerKeyConfig) ValidateKey(key string) *WorkerKeyEntry {
	if c == nil {
		return nil
	}
	if c.hashIndex == nil {
		c.build()
	}
	if entry, ok := c.hashIndex[model.HashWorkerKey(key)]; ok {
		return entry
	}
	return nil
}

// IsEnabled returns true if any static worker keys are configured.
func (c *WorkerKeyConfig) IsEnabled() bool {
	return c != nil && len(c.Keys) > 0
}

// hashKey creates a short hash of the key for logging purposes.
func hashKey(key string) string {
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:8]) // First 16 hex chars
}

// WorkerKeyAuthenticator validates X-Worker-Key values against both statically
// configured keys and DB-backed per-worker keys (hashed at rest). It centralizes
// the "no keys configured = open access" policy.
type WorkerKeyAuthenticator struct {
	static *WorkerKeyConfig // optional; legacy/bootstrap keys from file/env
	store  store.Store      // optional; DB-backed minted keys
	logger *slog.Logger
}

// NewWorkerKeyAuthenticator builds an authenticator from the (optional) static
// config and the store.
func NewWorkerKeyAuthenticator(static *WorkerKeyConfig, st store.Store, logger *slog.Logger) *WorkerKeyAuthenticator {
	return &WorkerKeyAuthenticator{static: static, store: st, logger: logger}
}

// Enabled reports whether worker-key enforcement is active. It is active when
// any static key OR any DB-backed key is configured. On a store error it fails
// closed (returns true) so a transient DB fault cannot silently open the fleet.
func (a *WorkerKeyAuthenticator) Enabled(ctx context.Context) bool {
	if a.static.IsEnabled() {
		return true
	}
	if a.store != nil {
		n, err := a.store.CountWorkerKeys(ctx)
		if err != nil {
			a.logger.Error("count worker keys failed; enforcing auth", "error", err)
			return true // fail closed
		}
		return n > 0
	}
	return false
}

// Authenticate validates a raw worker key and returns the resulting auth context,
// or nil if the key is unknown, disabled, or expired.
func (a *WorkerKeyAuthenticator) Authenticate(ctx context.Context, raw string) *WorkerAuthContext {
	now := time.Now()

	// 1. Static config (file/env). Legacy keys, matched by hash.
	if a.static != nil {
		if entry := a.static.ValidateKey(raw); entry != nil {
			if entry.Disabled {
				a.logger.Warn("disabled worker key used", "key_id", entry.ID, "key_hash", hashKey(raw))
				return nil
			}
			if entry.ExpiresAt != nil && !now.Before(*entry.ExpiresAt) {
				a.logger.Warn("expired worker key used", "key_id", entry.ID, "key_hash", hashKey(raw))
				return nil
			}
			id := entry.ID
			if id == "" {
				id = hashKey(raw)
			}
			return &WorkerAuthContext{KeyID: id, Groups: entry.Groups}
		}
	}

	// 2. DB-backed per-worker keys (hashed at rest).
	if a.store != nil {
		wk, err := a.store.GetWorkerKeyByHash(ctx, model.HashWorkerKey(raw))
		if err != nil {
			a.logger.Error("worker key lookup failed", "error", err)
			return nil
		}
		if wk != nil {
			if !wk.IsActive(now) {
				a.logger.Warn("inactive worker key used", "key_id", wk.ID, "disabled", wk.Disabled)
				return nil
			}
			// Record usage; best-effort, never blocks auth.
			if err := a.store.TouchWorkerKey(ctx, wk.ID, now); err != nil {
				a.logger.Debug("touch worker key failed", "key_id", wk.ID, "error", err)
			}
			return &WorkerAuthContext{KeyID: wk.ID, Groups: wk.Groups}
		}
	}

	return nil
}

// workerAuthMiddleware validates the X-Worker-Key header for worker endpoints.
// If no keys are configured (static or DB), authentication is disabled (open access).
func workerAuthMiddleware(auth *WorkerKeyAuthenticator, logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestIDFromContext(r.Context())

			// If no keys are configured, allow open access.
			if auth == nil || !auth.Enabled(r.Context()) {
				// Create anonymous worker context — allow any group.
				workerCtx := &WorkerAuthContext{
					KeyID:  "none",
					Groups: nil, // nil means any group is allowed
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

			// Validate key against static + DB-backed keys.
			workerCtx := auth.Authenticate(r.Context(), key)
			if workerCtx == nil {
				logger.Warn("invalid worker key", "key_hash", hashKey(key))
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "invalid worker key",
				})
				return
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
