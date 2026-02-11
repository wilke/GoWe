package ui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

const (
	// SessionCookieName is the name of the session cookie.
	SessionCookieName = "gowe_session"
	// SessionDuration is the default session lifetime.
	SessionDuration = 24 * time.Hour
)

// SessionManager handles session creation, validation, and cleanup.
type SessionManager struct {
	store store.Store
}

// NewSessionManager creates a new session manager.
func NewSessionManager(st store.Store) *SessionManager {
	return &SessionManager{store: st}
}

// CreateSession creates a new session for the authenticated user.
func (sm *SessionManager) CreateSession(ctx context.Context, userID, username, role, token string, tokenExp time.Time) (*model.Session, error) {
	sessionID, err := generateSessionID()
	if err != nil {
		return nil, fmt.Errorf("generate session id: %w", err)
	}

	now := time.Now()
	sess := &model.Session{
		ID:        sessionID,
		UserID:    userID,
		Username:  username,
		Role:      role,
		Token:     token,
		TokenExp:  tokenExp,
		CreatedAt: now,
		ExpiresAt: now.Add(SessionDuration),
	}

	// Limit session expiry to token expiry if token expires sooner.
	if !tokenExp.IsZero() && tokenExp.Before(sess.ExpiresAt) {
		sess.ExpiresAt = tokenExp
	}

	if err := sm.store.CreateSession(ctx, sess); err != nil {
		return nil, fmt.Errorf("store session: %w", err)
	}

	return sess, nil
}

// GetSession retrieves a session by ID from the store.
// Returns nil if the session doesn't exist or has expired.
func (sm *SessionManager) GetSession(ctx context.Context, sessionID string) (*model.Session, error) {
	sess, err := sm.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("get session: %w", err)
	}
	if sess == nil {
		return nil, nil
	}

	// Check if session or token has expired.
	if sess.IsExpired() || sess.IsTokenExpired() {
		_ = sm.store.DeleteSession(ctx, sessionID)
		return nil, nil
	}

	return sess, nil
}

// DeleteSession removes a session from the store.
func (sm *SessionManager) DeleteSession(ctx context.Context, sessionID string) error {
	return sm.store.DeleteSession(ctx, sessionID)
}

// CleanupExpiredSessions removes all expired sessions from the store.
func (sm *SessionManager) CleanupExpiredSessions(ctx context.Context) (int64, error) {
	return sm.store.DeleteExpiredSessions(ctx)
}

// GetSessionFromRequest extracts the session from the request cookie.
func (sm *SessionManager) GetSessionFromRequest(r *http.Request) (*model.Session, error) {
	cookie, err := r.Cookie(SessionCookieName)
	if err != nil {
		return nil, nil // No cookie, no session
	}
	return sm.GetSession(r.Context(), cookie.Value)
}

// SetSessionCookie sets the session cookie on the response.
func SetSessionCookie(w http.ResponseWriter, sess *model.Session, secure bool) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    sess.ID,
		Path:     "/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		Expires:  sess.ExpiresAt,
	})
}

// ClearSessionCookie removes the session cookie.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// generateSessionID generates a cryptographically secure random session ID.
func generateSessionID() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "sess_" + hex.EncodeToString(b), nil
}
