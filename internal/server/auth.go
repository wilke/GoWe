package server

import (
	"context"
	"crypto/subtle"
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
	Token    string             // Raw provider token (for downstream calls); empty for a service identity
	Provider model.AuthProvider // Auth provider used for this request
	Expiry   time.Time          // Token expiration time
	// IsService is set when the request was authenticated with a GoWe
	// service token. Such an identity is never an admin.
	IsService bool
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

			// More than one credential header of a kind is ambiguous:
			// extractToken reads only the first, so an empty first
			// Authorization header followed by a service token would
			// otherwise be served as anonymous. Refuse instead (fail closed).
			if len(r.Header.Values("Authorization")) > 1 || len(r.Header.Values("X-MG-RAST-Token")) > 1 {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "invalid token format",
				})
				return
			}

			// A credential header that is present but empty (after the
			// "Bearer" scheme) is a broken credential, not "no credential":
			// refuse it instead of serving the request as anonymous.
			if emptyCredentialHeader(r) {
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "invalid token format",
				})
				return
			}

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

			// A GoWe service token is handled only here: it never reaches
			// the BV-BRC verifier or the unverified parser below. Any
			// credential carrying the prefix counts, so no header form can
			// route one there. In the X-MG-RAST-Token header it is never
			// accepted.
			if model.LooksLikeServiceToken(token) {
				if provider == model.ProviderMGRAST {
					respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
						Code:    model.ErrUnauthorized,
						Message: "invalid token format",
					})
					return
				}
				userCtx := authenticateServiceToken(w, r, st, logger, denylist, token)
				if userCtx == nil {
					return // answered
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
			case model.ProviderService:
				// Service tokens are authenticated above, never here
				// (extractToken does not return this provider).
				respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
					Code:    model.ErrUnauthorized,
					Message: "invalid token format",
				})
				return
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

			// The service realm is reserved: only a service token reaches
			// it, never a provider token (verified or not).
			if model.IsServiceUsername(username) {
				logger.Warn("provider token claims a username in the reserved service realm; refused",
					"username", username, "provider", provider)
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

// serviceTokenTouchInterval is how often, at most, last_used_at of a service
// token is written.
const serviceTokenTouchInterval = time.Minute

// authenticateServiceToken authenticates raw, a credential carrying the
// service token prefix, and returns the user context of its service
// identity. On failure it answers the request and returns nil: 401 with the
// generic message for a malformed, unknown, expired or denylisted token, 503
// when the store cannot be read. Neither the raw token nor its hash is ever
// logged.
func authenticateServiceToken(w http.ResponseWriter, r *http.Request, st store.Store, logger *slog.Logger, denylist *AuthDenylist, raw string) *UserContext {
	reqID := RequestIDFromContext(r.Context())
	invalid := func() *UserContext {
		respondError(w, reqID, http.StatusUnauthorized, &model.APIError{
			Code:    model.ErrUnauthorized,
			Message: "invalid token format",
		})
		return nil
	}

	if !model.WellFormedServiceToken(raw) {
		return invalid()
	}
	hash := model.HashServiceToken(raw)
	tok, err := st.GetServiceTokenByHash(r.Context(), hash)
	if err != nil {
		logger.Error("service token lookup failed", "error", err)
		respondError(w, reqID, http.StatusServiceUnavailable, &model.APIError{
			Code:    model.ErrUnavailable,
			Message: "authentication temporarily unavailable",
		})
		return nil
	}
	if tok == nil || subtle.ConstantTimeCompare([]byte(tok.TokenHash), []byte(hash)) != 1 {
		return invalid()
	}
	now := time.Now()
	if tok.ExpiryInvalid {
		// An empty or unparsable expires_at never means "no expiry".
		logger.Error("service token has an empty or unparsable stored expiry; refused", "token_id", tok.ID, "name", tok.Name)
		return invalid()
	}
	if tok.Expired(now) {
		logger.Debug("expired service token used", "token_id", tok.ID, "name", tok.Name)
		return invalid()
	}
	if err := model.ValidateServiceName(tok.Name); err != nil {
		logger.Error("service token has an invalid stored name; refused", "token_id", tok.ID, "error", err)
		return invalid()
	}
	username := tok.Username()
	if denylist.Denied(username, "") {
		return invalid()
	}

	// last_used_at at most once per interval; best-effort.
	if tok.LastUsedAt == nil || now.Sub(*tok.LastUsedAt) >= serviceTokenTouchInterval {
		if err := st.TouchServiceToken(r.Context(), tok.ID, now.UTC()); err != nil {
			logger.Warn("service token last_used_at update failed", "token_id", tok.ID, "error", err)
		}
	}

	user, err := st.GetOrCreateUser(r.Context(), username, model.ProviderService)
	if err != nil {
		logger.Error("user lookup/create failed", "username", username, "error", err)
		respondError(w, reqID, http.StatusInternalServerError, &model.APIError{
			Code:    model.ErrInternal,
			Message: "authentication error",
		})
		return nil
	}
	// Never an admin, whatever the stored row or the admin lists say: the
	// request sees role user (model.User.IsAdmin refuses the realm too).
	u := *user
	u.Role = model.RoleUser

	logger.Debug("service token used", "token_id", tok.ID, "name", tok.Name)
	// No provider token: Token and Expiry stay empty, as for an anonymous
	// submission (no Workspace staging, no BV-BRC executor token, no token
	// injection).
	return &UserContext{
		User:      &u,
		Provider:  model.ProviderService,
		IsService: true,
	}
}

// WarnServiceRealmUsers logs one WARN (at start-up) naming the user rows
// whose username is in the reserved service realm but whose provider is not
// "service": rows made before the realm was reserved (for example claimed
// with --insecure-skip-token-verify) or by hand. A service token of such a
// name acts as that user (its submissions, workflows, worker group rules),
// never as an admin. Names only; a failed read is a WARN too. Never fatal.
func WarnServiceRealmUsers(ctx context.Context, st store.Store, logger *slog.Logger) {
	users, err := st.ListUsers(ctx)
	if err != nil {
		logger.Warn("cannot list users to check the reserved service realm", "error", err)
		return
	}
	var names []string
	for _, u := range users {
		if u != nil && model.IsServiceUsername(u.Username) && u.Provider != model.ProviderService {
			names = append(names, u.Username)
		}
	}
	if len(names) > 0 {
		logger.Warn("user rows in the reserved service realm (@"+model.ServiceRealm+") were not created by a service token; a service token of such a name acts as that user (its submissions, workflows and worker group rules), never as an admin",
			"usernames", names)
	}
}

// extractToken checks headers for provider tokens and returns the token and provider.
// Returns empty strings if no token is found. Only the first Authorization
// value is read: apiAuthMiddleware refuses a request with more than one
// Authorization or X-MG-RAST-Token header before calling it. The "Bearer "
// prefix is matched case-sensitively; any other form (including "bearer ")
// is taken as the token verbatim, so a service token behind it is not
// well-formed and is refused (401), never served as anonymous.
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

// emptyCredentialHeader reports whether r carries an Authorization or
// X-MG-RAST-Token header whose value is empty once white space and the
// "Bearer" scheme are removed ("", "  ", "Bearer", "Bearer   "). A request
// without either header is not affected (it may be anonymous).
func emptyCredentialHeader(r *http.Request) bool {
	for _, v := range r.Header.Values("Authorization") {
		v = strings.TrimSpace(v)
		if v == "Bearer" || strings.TrimSpace(strings.TrimPrefix(v, "Bearer ")) == "" {
			return true
		}
	}
	for _, v := range r.Header.Values("X-MG-RAST-Token") {
		if strings.TrimSpace(v) == "" {
			return true
		}
	}
	return false
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

			if userCtx.IsService || !userCtx.User.IsAdmin() {
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
