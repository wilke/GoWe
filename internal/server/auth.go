package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

const ctxKeyUserAuth ctxKey = "user_auth"

// UserContext holds authenticated user info for a request.
type UserContext struct {
	User     *model.User        // GoWe user account
	Token    string             // Raw provider token (for downstream calls)
	Provider model.AuthProvider // Auth provider used for this request
	Expiry   time.Time          // Token expiration time
}

// UserFromContext extracts the UserContext from request context.
func UserFromContext(ctx context.Context) *UserContext {
	if uc, ok := ctx.Value(ctxKeyUserAuth).(*UserContext); ok {
		return uc
	}
	return nil
}

// AnonymousConfig controls anonymous access settings.
type AnonymousConfig struct {
	// Enabled allows unauthenticated requests as the anonymous user.
	Enabled bool
	// AllowedExecutors restricts which executors anonymous users can use.
	AllowedExecutors []model.ExecutorType
	// RateLimit is the max submissions per hour for anonymous users (0 = no limit).
	RateLimit int
}

// IsExecutorAllowed checks if an executor type is allowed for anonymous users.
func (c *AnonymousConfig) IsExecutorAllowed(execType model.ExecutorType) bool {
	if len(c.AllowedExecutors) == 0 {
		return true // No restrictions
	}
	for _, allowed := range c.AllowedExecutors {
		if allowed == execType {
			return true
		}
	}
	return false
}

// apiAuthMiddleware validates tokens and manages user accounts.
// It supports multiple auth providers (BV-BRC, MG-RAST) and anonymous access.
func apiAuthMiddleware(
	st store.Store,
	adminConfig *AdminConfig,
	anonConfig *AnonymousConfig,
	logger *slog.Logger,
) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestIDFromContext(r.Context())

			// Extract token from headers.
			token, provider := extractToken(r)

			if token == "" {
				// No token provided - try anonymous access.
				if anonConfig == nil || !anonConfig.Enabled {
					respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
						Code:    model.ErrUnauthorized,
						Message: "authentication required",
					})
					return
				}

				// Use anonymous user.
				userCtx := &UserContext{
					User:     model.AnonymousUser,
					Provider: model.ProviderLocal,
				}
				ctx := context.WithValue(r.Context(), ctxKeyUserAuth, userCtx)
				next.ServeHTTP(w, r.WithContext(ctx))
				return
			}

			// Parse and validate token based on provider.
			var tokenInfo bvbrc.TokenInfo
			switch provider {
			case model.ProviderBVBRC, model.ProviderMGRAST:
				// Both use the same pipe-delimited format.
				tokenInfo = bvbrc.ParseToken(token)
			default:
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "unsupported authentication provider",
				})
				return
			}

			// Check token validity.
			if tokenInfo.Username == "" {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "invalid token format",
				})
				return
			}

			if tokenInfo.IsExpired() {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "token expired",
				})
				return
			}

			// Lookup or create GoWe user account.
			user, err := st.GetOrCreateUser(r.Context(), tokenInfo.Username, provider)
			if err != nil {
				logger.Error("user lookup/create failed", "username", tokenInfo.Username, "error", err)
				respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
					Code:    model.ErrInternal,
					Message: "authentication error",
				})
				return
			}

			// Check and update admin status.
			if adminConfig != nil && adminConfig.IsAdmin(tokenInfo.Username) && user.Role != model.RoleAdmin {
				user.Role = model.RoleAdmin
				if err := st.UpdateUser(r.Context(), user); err != nil {
					logger.Warn("failed to update user role", "username", tokenInfo.Username, "error", err)
				}
			}

			// Build user context.
			userCtx := &UserContext{
				User:     user,
				Token:    token,
				Provider: provider,
				Expiry:   tokenInfo.Expiry,
			}

			ctx := context.WithValue(r.Context(), ctxKeyUserAuth, userCtx)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// extractToken checks headers for provider tokens and returns the token and provider.
// Returns empty strings if no token is found.
func extractToken(r *http.Request) (string, model.AuthProvider) {
	// Check Authorization header (BV-BRC format).
	if auth := r.Header.Get("Authorization"); auth != "" {
		// Strip "Bearer " prefix if present.
		token := strings.TrimPrefix(auth, "Bearer ")
		token = strings.TrimSpace(token)

		// Detect provider from token format.
		if strings.Contains(token, "un=") {
			// BV-BRC pipe-delimited format: un=user|tokenid=...|expiry=...
			return token, model.ProviderBVBRC
		}
		// Could be MG-RAST in Authorization header.
		if isMGRASTToken(token) {
			return token, model.ProviderMGRAST
		}
		// Default to BV-BRC for backward compatibility.
		return token, model.ProviderBVBRC
	}

	// Check X-MG-RAST-Token header.
	if token := r.Header.Get("X-MG-RAST-Token"); token != "" {
		return strings.TrimSpace(token), model.ProviderMGRAST
	}

	return "", ""
}

// isMGRASTToken checks if a token looks like an MG-RAST token.
// MG-RAST tokens use a similar pipe-delimited format to BV-BRC.
func isMGRASTToken(token string) bool {
	// MG-RAST tokens typically have different field names or can be
	// identified by the username domain. For now, we rely on the
	// X-MG-RAST-Token header for explicit provider selection.
	return false
}

// requireAdmin is middleware that checks if the user has admin role.
func requireAdmin(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqID := RequestIDFromContext(r.Context())
			userCtx := UserFromContext(r.Context())

			if userCtx == nil || userCtx.User == nil {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "authentication required",
				})
				return
			}

			if !userCtx.User.IsAdmin() {
				respondError(w, reqID, http.StatusForbidden, &model.APIError{
					Code:    model.ErrForbidden,
					Message: "admin access required",
				})
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
