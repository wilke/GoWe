package model

import "time"

// UserRole represents the role of a user in the system.
type UserRole string

const (
	// RoleUser is a standard authenticated user.
	RoleUser UserRole = "user"
	// RoleAdmin has elevated permissions for server administration.
	RoleAdmin UserRole = "admin"
	// RoleAnonymous is an unauthenticated user with limited access.
	RoleAnonymous UserRole = "anonymous"
)

// AuthProvider identifies the external authentication provider.
type AuthProvider string

const (
	// ProviderBVBRC authenticates via BV-BRC (PATRIC).
	ProviderBVBRC AuthProvider = "bvbrc"
	// ProviderMGRAST authenticates via MG-RAST.
	ProviderMGRAST AuthProvider = "mgrast"
	// ProviderLocal is used for the built-in anonymous user.
	ProviderLocal AuthProvider = "local"
)

// User represents a GoWe user account.
// Users are created on first login from an external provider.
type User struct {
	ID              string           `json:"id"`
	Username        string           `json:"username"`                    // e.g., "alice@bvbrc"
	Provider        AuthProvider     `json:"provider"`                    // Primary auth provider
	Role            UserRole         `json:"role"`                        // user, admin, or anonymous
	LinkedProviders []LinkedProvider `json:"linked_providers,omitempty"`  // Optional linked provider accounts
	CreatedAt       time.Time        `json:"created_at"`
	LastLoginAt     time.Time        `json:"last_login_at"`
}

// LinkedProvider represents an additional authentication provider linked to a user account.
type LinkedProvider struct {
	Provider AuthProvider `json:"provider"`
	Username string       `json:"username"`
}

// IsAdmin returns true if the user has admin role.
func (u *User) IsAdmin() bool {
	return u.Role == RoleAdmin
}

// IsAnonymous returns true if the user is the anonymous user.
func (u *User) IsAnonymous() bool {
	return u.Role == RoleAnonymous
}

// AnonymousUser is the built-in anonymous user for unauthenticated access.
var AnonymousUser = &User{
	ID:       "user_anonymous",
	Username: "anonymous",
	Provider: ProviderLocal,
	Role:     RoleAnonymous,
}
