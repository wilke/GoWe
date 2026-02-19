package server

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// AdminConfig manages admin role assignment from multiple sources.
// Admin status is checked in priority order: database > environment > config file.
type AdminConfig struct {
	store      store.Store // Database source (highest priority)
	envAdmins  []string    // From GOWE_ADMINS environment variable
	fileAdmins []string    // From config file
}

// NewAdminConfig creates an AdminConfig that checks multiple sources for admin status.
// Parameters:
//   - st: Store for database admin lookups (can be nil)
//   - envVar: Name of environment variable containing comma-separated admin usernames
//   - configFile: Path to JSON config file with admins array (can be empty)
func NewAdminConfig(st store.Store, envVar string, configFile string) *AdminConfig {
	cfg := &AdminConfig{
		store: st,
	}

	// Load admins from environment variable.
	if envVal := os.Getenv(envVar); envVal != "" {
		for _, username := range strings.Split(envVal, ",") {
			username = strings.TrimSpace(username)
			if username != "" {
				cfg.envAdmins = append(cfg.envAdmins, username)
			}
		}
	}

	// Load admins from config file.
	if configFile != "" {
		if data, err := os.ReadFile(configFile); err == nil {
			var fileConfig struct {
				Admins []string `json:"admins"`
			}
			if err := json.Unmarshal(data, &fileConfig); err == nil {
				cfg.fileAdmins = fileConfig.Admins
			}
		}
	}

	return cfg
}

// IsAdmin checks if the given username should have admin role.
// Checks in priority order: database > environment > config file.
func (c *AdminConfig) IsAdmin(username string) bool {
	// 1. Check database (highest priority).
	if c.store != nil {
		user, err := c.store.GetUser(context.Background(), username)
		if err == nil && user != nil && user.Role == model.RoleAdmin {
			return true
		}
	}

	// 2. Check environment variable.
	if slices.Contains(c.envAdmins, username) {
		return true
	}

	// 3. Check config file.
	if slices.Contains(c.fileAdmins, username) {
		return true
	}

	return false
}

// AddAdmin adds a username to the admin list in the database.
// This is the recommended way to grant admin access persistently.
func (c *AdminConfig) AddAdmin(ctx context.Context, username string, provider model.AuthProvider) error {
	if c.store == nil {
		return nil
	}

	// Get or create user.
	user, err := c.store.GetOrCreateUser(ctx, username, provider)
	if err != nil {
		return err
	}

	// Update role to admin.
	user.Role = model.RoleAdmin
	return c.store.UpdateUser(ctx, user)
}

// RemoveAdmin removes admin role from a user in the database.
func (c *AdminConfig) RemoveAdmin(ctx context.Context, username string) error {
	if c.store == nil {
		return nil
	}

	user, err := c.store.GetUser(ctx, username)
	if err != nil || user == nil {
		return err
	}

	user.Role = model.RoleUser
	return c.store.UpdateUser(ctx, user)
}

// EnvAdmins returns the list of admins from the environment variable.
func (c *AdminConfig) EnvAdmins() []string {
	return c.envAdmins
}

// FileAdmins returns the list of admins from the config file.
func (c *AdminConfig) FileAdmins() []string {
	return c.fileAdmins
}
