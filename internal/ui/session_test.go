package ui

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

func TestSessionManager_CreateAndGet(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	sm := NewSessionManager(st)
	ctx := context.Background()

	// Create a session.
	tokenExp := time.Now().Add(24 * time.Hour)
	sess, err := sm.CreateSession(ctx, "user1", "testuser", model.RoleUser, "test-token", tokenExp)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	if sess.ID == "" {
		t.Error("expected session ID to be set")
	}
	if sess.UserID != "user1" {
		t.Errorf("expected UserID 'user1', got %q", sess.UserID)
	}
	if sess.Username != "testuser" {
		t.Errorf("expected Username 'testuser', got %q", sess.Username)
	}
	if sess.Role != model.RoleUser {
		t.Errorf("expected Role 'user', got %q", sess.Role)
	}
	if sess.Token != "test-token" {
		t.Errorf("expected Token 'test-token', got %q", sess.Token)
	}

	// Get the session.
	retrieved, err := sm.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected session to be found")
	}
	if retrieved.Username != sess.Username {
		t.Errorf("expected Username %q, got %q", sess.Username, retrieved.Username)
	}
}

func TestSessionManager_GetSession_NotFound(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	sm := NewSessionManager(st)
	ctx := context.Background()

	sess, err := sm.GetSession(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if sess != nil {
		t.Error("expected nil session for nonexistent ID")
	}
}

func TestSessionManager_GetSession_Expired(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	sm := NewSessionManager(st)
	ctx := context.Background()

	// Create an expired session directly.
	expiredTime := time.Now().Add(-1 * time.Hour)
	sess := &model.Session{
		ID:        "sess_expired",
		UserID:    "user1",
		Username:  "testuser",
		Role:      model.RoleUser,
		Token:     "test-token",
		TokenExp:  time.Now().Add(24 * time.Hour),
		CreatedAt: time.Now().Add(-2 * time.Hour),
		ExpiresAt: expiredTime,
	}
	if err := st.CreateSession(ctx, sess); err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// GetSession should return nil for expired sessions.
	retrieved, err := sm.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil session for expired session")
	}
}

func TestSessionManager_DeleteSession(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	sm := NewSessionManager(st)
	ctx := context.Background()

	// Create a session.
	tokenExp := time.Now().Add(24 * time.Hour)
	sess, err := sm.CreateSession(ctx, "user1", "testuser", model.RoleUser, "test-token", tokenExp)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Delete the session.
	if err := sm.DeleteSession(ctx, sess.ID); err != nil {
		t.Fatalf("DeleteSession failed: %v", err)
	}

	// Verify it's gone.
	retrieved, err := sm.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatalf("GetSession failed: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil session after deletion")
	}
}

func TestSessionManager_GetSessionFromRequest(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	sm := NewSessionManager(st)
	ctx := context.Background()

	// Create a session.
	tokenExp := time.Now().Add(24 * time.Hour)
	sess, err := sm.CreateSession(ctx, "user1", "testuser", model.RoleUser, "test-token", tokenExp)
	if err != nil {
		t.Fatalf("CreateSession failed: %v", err)
	}

	// Create request with session cookie.
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{
		Name:  SessionCookieName,
		Value: sess.ID,
	})

	retrieved, err := sm.GetSessionFromRequest(req)
	if err != nil {
		t.Fatalf("GetSessionFromRequest failed: %v", err)
	}
	if retrieved == nil {
		t.Fatal("expected session to be found")
	}
	if retrieved.Username != sess.Username {
		t.Errorf("expected Username %q, got %q", sess.Username, retrieved.Username)
	}
}

func TestSessionManager_GetSessionFromRequest_NoCookie(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	sm := NewSessionManager(st)

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	retrieved, err := sm.GetSessionFromRequest(req)
	if err != nil {
		t.Fatalf("GetSessionFromRequest failed: %v", err)
	}
	if retrieved != nil {
		t.Error("expected nil session when no cookie")
	}
}

func TestSetSessionCookie(t *testing.T) {
	sess := &model.Session{
		ID:        "sess_test123",
		ExpiresAt: time.Now().Add(24 * time.Hour),
	}

	w := httptest.NewRecorder()
	SetSessionCookie(w, sess, false)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}

	cookie := cookies[0]
	if cookie.Name != SessionCookieName {
		t.Errorf("expected cookie name %q, got %q", SessionCookieName, cookie.Name)
	}
	if cookie.Value != sess.ID {
		t.Errorf("expected cookie value %q, got %q", sess.ID, cookie.Value)
	}
	if !cookie.HttpOnly {
		t.Error("expected HttpOnly to be true")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("expected SameSite Strict, got %v", cookie.SameSite)
	}
}

func TestClearSessionCookie(t *testing.T) {
	w := httptest.NewRecorder()
	ClearSessionCookie(w)

	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}

	cookie := cookies[0]
	if cookie.Name != SessionCookieName {
		t.Errorf("expected cookie name %q, got %q", SessionCookieName, cookie.Name)
	}
	if cookie.MaxAge != -1 {
		t.Errorf("expected MaxAge -1, got %d", cookie.MaxAge)
	}
}

func TestSession_IsExpired(t *testing.T) {
	tests := []struct {
		name     string
		expires  time.Time
		expected bool
	}{
		{"future", time.Now().Add(time.Hour), false},
		{"past", time.Now().Add(-time.Hour), true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sess := &model.Session{ExpiresAt: tt.expires}
			if got := sess.IsExpired(); got != tt.expected {
				t.Errorf("IsExpired() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestSession_IsAdmin(t *testing.T) {
	tests := []struct {
		role     string
		expected bool
	}{
		{model.RoleAdmin, true},
		{model.RoleUser, false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.role, func(t *testing.T) {
			sess := &model.Session{Role: tt.role}
			if got := sess.IsAdmin(); got != tt.expected {
				t.Errorf("IsAdmin() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func setupTestStore(t *testing.T) *store.SQLiteStore {
	t.Helper()

	logger := slog.Default()
	st, err := store.NewSQLiteStore(":memory:", logger)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if err := st.Migrate(context.Background()); err != nil {
		st.Close()
		t.Fatalf("failed to migrate: %v", err)
	}

	return st
}
