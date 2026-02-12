package ui

import (
	"context"
	"net/http"

	"github.com/me/gowe/pkg/model"
)

// Context keys for session data.
type contextKey string

const (
	sessionContextKey contextKey = "session"
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
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		// Add session to context.
		ctx := context.WithValue(r.Context(), sessionContextKey, sess)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// AdminMiddleware ensures the user has admin role.
// Must be used after AuthMiddleware.
func (ui *UI) AdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess := SessionFromContext(r.Context())
		if sess == nil {
			http.Redirect(w, r, "/login", http.StatusSeeOther)
			return
		}

		if !sess.IsAdmin() {
			http.Error(w, "Forbidden: Admin access required", http.StatusForbidden)
			return
		}

		next.ServeHTTP(w, r)
	})
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
