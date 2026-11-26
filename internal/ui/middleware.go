package ui

import (
	"context"
	"net/http"
	"net/url"
	"strings"

	"github.com/me/gowe/pkg/model"
)

// Context keys for session data.
type contextKey string

const (
	sessionContextKey contextKey = "session"
	// liveRoleContextKey marks a request whose session (in the context) has
	// the role the user has now, derived for this request by withLiveRole,
	// instead of the role recorded at login.
	liveRoleContextKey contextKey = "live_role"
)

// SessionFromContext retrieves the session from the request context.
func SessionFromContext(ctx context.Context) *model.Session {
	sess, _ := ctx.Value(sessionContextKey).(*model.Session)
	return sess
}

// AuthMiddleware validates the session and adds it to the request context.
// If no valid session exists, it redirects to the login page.
func (ui *UI) AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, err := ui.sessions.GetSessionFromRequest(r)
		if err != nil {
			ui.logger.Error("session lookup failed", "error", err)
			http.Redirect(w, r, ui.href("/login"), http.StatusSeeOther)
			return
		}

		if sess == nil {
			http.Redirect(w, r, ui.href("/login"), http.StatusSeeOther)
			return
		}

		// Add session to context.
		ctx := context.WithValue(r.Context(), sessionContextKey, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// isAdminNow reports whether username is a server admin now, by the rule the
// REST API applies on every request (apiAuthMiddleware in
// internal/server/auth.go): a configured admin (the admin checker), or a
// stored user whose role is admin. The REST middleware stores the admin role
// for a configured admin, so there too a user removed from the configured
// admins stays an admin while the stored role is admin. A configured admin
// costs no store read, anyone else one (GetUser); a user without a stored
// account is not an admin.
func (ui *UI) isAdminNow(ctx context.Context, username string) (bool, error) {
	if username == "" {
		return false, nil
	}
	if ui.isAdminUser(username) {
		return true, nil
	}
	user, err := ui.store.GetUser(ctx, username)
	if err != nil {
		return false, err
	}
	return user != nil && user.IsAdmin(), nil
}

// withLiveRole returns r with the session in its context replaced by a copy
// whose Role is the role the user has now (isAdminNow), marked as such, so
// it is derived once per request. The stored session keeps the role of the
// login; only this request's copy changes. r is returned unchanged when it
// has no session or was already marked.
func (ui *UI) withLiveRole(r *http.Request) (*http.Request, error) {
	ctx := r.Context()
	if live, _ := ctx.Value(liveRoleContextKey).(bool); live {
		return r, nil
	}
	sess := SessionFromContext(ctx)
	if sess == nil {
		return r, nil
	}
	admin, err := ui.isAdminNow(ctx, sess.Username)
	if err != nil {
		return nil, err
	}
	cp := *sess
	cp.Role = string(model.RoleUser)
	if admin {
		cp.Role = string(model.RoleAdmin)
	}
	ctx = context.WithValue(ctx, sessionContextKey, &cp)
	ctx = context.WithValue(ctx, liveRoleContextKey, true)
	return r.WithContext(ctx), nil
}

// LiveRoleMiddleware replaces the session's role, for this request, by the
// role the user has now (withLiveRole). The worker group pages use it, so
// that they act for a server admin only while the user is one. Must be used
// after AuthMiddleware.
func (ui *UI) LiveRoleMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lr, err := ui.withLiveRole(r)
		if err != nil {
			ui.logger.Error("current user role lookup failed", "path", r.URL.Path, "error", err)
			http.Error(w, "Internal Server Error: the user's role could not be read", http.StatusInternalServerError)
			return
		}
		next.ServeHTTP(w, lr)
	})
}

// AdminMiddleware ensures the user is a server admin now (isAdminNow, the
// REST API's rule), not merely at login: a demoted admin is refused on the
// next request, a promoted user is let in without logging in again. The
// handlers see the session with the current role. Must be used after
// AuthMiddleware.
func (ui *UI) AdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if SessionFromContext(r.Context()) == nil {
			http.Redirect(w, r, ui.href("/login"), http.StatusSeeOther)
			return
		}

		lr, err := ui.withLiveRole(r)
		if err != nil {
			ui.logger.Error("current user role lookup failed", "path", r.URL.Path, "error", err)
			http.Error(w, "Internal Server Error: the user's role could not be read", http.StatusInternalServerError)
			return
		}
		if !SessionFromContext(lr.Context()).IsAdmin() {
			http.Error(w, "Forbidden: Admin access required", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, lr)
	})
}

// SameOriginMiddleware refuses (403) a state-changing request (any method
// other than GET, HEAD and OPTIONS) that a browser sent from another site:
//   - a Sec-Fetch-Site header other than "same-origin" or "none" is refused;
//   - otherwise an Origin header must name this server: its host (host:port)
//     must equal r.Host or, behind a proxy (Config.BehindProxy), the first
//     value of X-Forwarded-Host;
//   - a request with neither header (a non-browser client) is let through:
//     the session cookie is SameSite=Strict.
//
// Each refusal logs one WARN with the path and those header values (never a
// cookie).
func (ui *UI) SameOriginMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}
		if reason := ui.crossSiteReason(r); reason != "" {
			ui.logger.Warn("cross-site request refused", "method", r.Method, "path", r.URL.Path, "reason", reason,
				"sec_fetch_site", r.Header.Get("Sec-Fetch-Site"), "origin", r.Header.Get("Origin"),
				"host", r.Host, "x_forwarded_host", r.Header.Get("X-Forwarded-Host"))
			http.Error(w, "Forbidden: cross-site request refused", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// crossSiteReason returns why SameOriginMiddleware refuses r, "" when it may
// pass.
func (ui *UI) crossSiteReason(r *http.Request) string {
	if vals := r.Header.Values("Sec-Fetch-Site"); len(vals) > 0 {
		switch strings.ToLower(strings.TrimSpace(vals[0])) {
		case "same-origin", "none":
			// The browser itself vouches for the origin; do not also compare
			// Origin with Host, which a proxy may have rewritten.
			return ""
		default:
			return "Sec-Fetch-Site is not same-origin"
		}
	}
	vals := r.Header.Values("Origin")
	if len(vals) == 0 {
		return ""
	}
	u, err := url.Parse(strings.TrimSpace(vals[0]))
	if err != nil || u.Host == "" {
		return "Origin is not a URL"
	}
	if strings.EqualFold(u.Host, r.Host) {
		return ""
	}
	if ui.behindProxy {
		if fwd := firstForwardedHost(r); fwd != "" && strings.EqualFold(u.Host, fwd) {
			return ""
		}
	}
	return "Origin does not match the host"
}

// firstForwardedHost is the first value of r's X-Forwarded-Host header (""
// when there is none); a proxy chain may send a comma-separated list.
func firstForwardedHost(r *http.Request) string {
	vals := r.Header.Values("X-Forwarded-Host")
	if len(vals) == 0 {
		return ""
	}
	first, _, _ := strings.Cut(vals[0], ",")
	return strings.TrimSpace(first)
}

// OptionalAuthMiddleware adds the session to context if available but doesn't require it.
func (ui *UI) OptionalAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, _ := ui.sessions.GetSessionFromRequest(r)
		if sess != nil {
			ctx := context.WithValue(r.Context(), sessionContextKey, sess)
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}
