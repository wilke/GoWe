package config

// ServerConfig holds configuration for the GoWe server.
type ServerConfig struct {
	Addr            string // Listen address (default ":8080")
	LogLevel        string // Log level: debug, info, warn, error
	LogFormat       string // Log format: text, json
	DBPath          string // SQLite database path (default ~/.gowe/gowe.db, ":memory:" for testing)
	DefaultExecutor string // Default executor type: "local", "docker", "worker", "" (auto/hint-based)
}

// DefaultServerConfig returns sensible defaults.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Addr:      ":8080",
		LogLevel:  "info",
		LogFormat: "text",
	}
}
