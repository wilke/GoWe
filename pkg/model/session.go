package model

import "time"

// Session represents an authenticated user session.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	Token     string    `json:"-"` // BV-BRC token (not exposed via JSON)
	TokenExp  time.Time `json:"-"` // Token expiration (not exposed via JSON)
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// IsExpired reports whether the session has expired.
func (s *Session) IsExpired() bool {
	return time.Now().After(s.ExpiresAt)
}

// IsTokenExpired reports whether the BV-BRC token has expired.
func (s *Session) IsTokenExpired() bool {
	return time.Now().After(s.TokenExp)
}

// IsAdmin reports whether the session has admin role.
func (s *Session) IsAdmin() bool {
	return s.Role == string(RoleAdmin)
}
