// Package bvbrc provides a Go client for the BV-BRC (Bacterial and Viral
// Bioinformatics Resource Center) JSON-RPC API services.
package bvbrc

import "time"

// Default service URLs for BV-BRC production environment.
const (
	DefaultAppServiceURL = "https://p3.theseed.org/services/app_service"
	DefaultWorkspaceURL  = "https://p3.theseed.org/services/Workspace"
	DefaultDataAPIURL    = "https://www.bv-brc.org/api/"
	DefaultAuthURL       = "https://user.patricbrc.org/authenticate"
	DefaultShockURL      = "https://p3.theseed.org/services/shock_api"
)

// Default client settings.
const (
	DefaultTimeout    = 30 * time.Second
	DefaultMaxRetries = 3
	DefaultRetryDelay = 1 * time.Second
)

// Config holds all configuration for the BV-BRC API client.
type Config struct {
	// AppServiceURL is the URL for the App Service (job management).
	AppServiceURL string

	// WorkspaceURL is the URL for the Workspace service.
	WorkspaceURL string

	// DataAPIURL is the URL for the Data API (REST/Solr).
	DataAPIURL string

	// AuthURL is the URL for the authentication service.
	AuthURL string

	// ShockURL is the URL for the Shock data storage service.
	ShockURL string

	// Token is the authentication token.
	Token string

	// Timeout is the HTTP client timeout for each request.
	Timeout time.Duration

	// MaxRetries is the maximum number of retry attempts for failed requests.
	MaxRetries int

	// RetryDelay is the initial delay between retries (exponential backoff applied).
	RetryDelay time.Duration
}

// DefaultConfig returns a Config with default production URLs and settings.
func DefaultConfig() Config {
	return Config{
		AppServiceURL: DefaultAppServiceURL,
		WorkspaceURL:  DefaultWorkspaceURL,
		DataAPIURL:    DefaultDataAPIURL,
		AuthURL:       DefaultAuthURL,
		ShockURL:      DefaultShockURL,
		Timeout:       DefaultTimeout,
		MaxRetries:    DefaultMaxRetries,
		RetryDelay:    DefaultRetryDelay,
	}
}

// WithToken returns a copy of the config with the specified token.
func (c Config) WithToken(token string) Config {
	c.Token = token
	return c
}

// WithTimeout returns a copy of the config with the specified timeout.
func (c Config) WithTimeout(timeout time.Duration) Config {
	c.Timeout = timeout
	return c
}

// WithRetries returns a copy of the config with the specified retry settings.
func (c Config) WithRetries(maxRetries int, retryDelay time.Duration) Config {
	c.MaxRetries = maxRetries
	c.RetryDelay = retryDelay
	return c
}
