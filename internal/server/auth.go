package server

import (
	"context"
	"errors"
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
//
// verifier, when non-nil, enables cryptographic verification of BV-BRC
// provider token signatures against a pinned issuer allowlist; nil disables
// verification and preserves the previous (un-verified) behavior. When
// verification is enabled, the X-MG-RAST-Token header path — which
// establishes identity with no signature check — is rejected unless
// allowUnverifiedMGRAST is set. denylist, when non-nil, rejects requests
// from specific usernames or token IDs after identity is established,
// whether or not the token was cryptographically verified.
func apiAuthMiddleware(
	st store.Store,
	adminConfig *AdminConfig,
	anonConfig *AnonymousConfig,
	logger *slog.Logger,
	verifier *bvbrc.Verifier,
	allowUnverifiedMGRAST bool,
	denylist *AuthDenylist,
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

				if denylist.Denied(model.AnonymousUser.Username, "") {
					respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
						Code:    model.ErrUnauthorized,
						Message: "invalid token format",
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

			// MG-RAST establishes identity with no signature verification.
			// Gate that path behind an explicit opt-in once verification is
			// enabled, so enabling the verifier doesn't leave an unverified
			// side door open.
			if provider == model.ProviderMGRAST && verifier != nil && !allowUnverifiedMGRAST {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "unverified MG-RAST authentication is disabled",
				})
				return
			}

			var username, tokenID string
			var expiry time.Time

			switch provider {
			case model.ProviderBVBRC:
				if verifier != nil {
					verified, err := verifier.Verify(r.Context(), token)
					if err != nil {
						switch {
						case errors.Is(err, bvbrc.ErrTokenInvalid):
							respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
								Code:    model.ErrUnauthorized,
								Message: "invalid token format",
							})
						case errors.Is(err, bvbrc.ErrKeyUnavailable):
							logger.Error("token verification unavailable", "error", err)
							respondError(w, reqID, http.StatusServiceUnavailable, &model.APIError{
								Code:    model.ErrUnavailable,
								Message: "authentication temporarily unavailable",
							})
						default:
							logger.Error("token verification failed", "error", err)
							respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
								Code:    model.ErrInternal,
								Message: "authentication error",
							})
						}
						return
					}
					username, tokenID, expiry = verified.Username, verified.TokenID, verified.Expiry
				} else {
					// Verification disabled: preserve prior behavior.
					tokenInfo := bvbrc.ParseToken(token)
					username, tokenID, expiry = tokenInfo.Username, tokenInfo.TokenID, tokenInfo.Expiry
				}
			case model.ProviderMGRAST:
				// Unverified path (reached only when verifier is nil, or
				// verifier is set and allowUnverifiedMGRAST is true).
				tokenInfo := bvbrc.ParseToken(token)
				username, tokenID, expiry = tokenInfo.Username, tokenInfo.TokenID, tokenInfo.Expiry
			default:
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "unsupported authentication provider",
				})
				return
			}

			// Check token validity.
			if username == "" {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "invalid token format",
				})
				return
			}

			if !expiry.IsZero() && time.Now().After(expiry) {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "token expired",
				})
				return
			}

			if denylist.Denied(username, tokenID) {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "invalid token format",
				})
				return
			}

			// Lookup or create GoWe user account.
			user, err := st.GetOrCreateUser(r.Context(), username, provider)
			if err != nil {
				logger.Error("user lookup/create failed", "username", username, "error", err)
				respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
					Code:    model.ErrInternal,
					Message: "authentication error",
				})
				return
			}

			// Check and update admin status.
			if adminConfig != nil && adminConfig.IsAdmin(username) && user.Role != model.RoleAdmin {
				user.Role = model.RoleAdmin
				if err := st.UpdateUser(r.Context(), user); err != nil {
					logger.Warn("failed to update user role", "username", username, "error", err)
				}
			}

			// Build user context.
			userCtx := &UserContext{
				User:     user,
				Token:    token,
				Provider: provider,
				Expiry:   expiry,
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
