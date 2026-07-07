package config

// ServerConfig holds configuration for the GoWe server.
type ServerConfig struct {
	Addr            string // Listen address (default ":8080")
	LogLevel        string // Log level: debug, info, warn, error
	LogFormat       string // Log format: text, json
	DBPath          string // SQLite database path (default ~/.gowe/gowe.db, ":memory:" for testing)
	DefaultExecutor string // Default executor when no CWL hint is set: "local", "docker", "worker", "" (auto)
	ForceExecutor   string // Force all tasks to this executor, ignoring CWL hints (testing only)

	// TLS termination. When both TLSCertFile and TLSKeyFile are set, the server
	// terminates TLS itself (ListenAndServeTLS). When empty, the server serves
	// plain HTTP and is expected to sit behind an external TLS terminator.
	TLSCertFile string // Path to PEM-encoded certificate (enables native HTTPS)
	TLSKeyFile  string // Path to PEM-encoded private key (enables native HTTPS)

	// Session cookie hardening.
	SecureCookies bool // Force the Secure attribute on session cookies regardless of per-request detection
	BehindProxy   bool // Trust X-Forwarded-Proto to decide the Secure attribute (only enable behind a trusted proxy)
}

// TLSEnabled reports whether native in-process TLS is configured.
func (c ServerConfig) TLSEnabled() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

// DefaultServerConfig returns sensible defaults.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Addr:      ":8080",
		LogLevel:  "info",
		LogFormat: "text",
	}
}
