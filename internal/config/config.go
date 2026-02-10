package config

// ServerConfig holds configuration for the GoWe server.
type ServerConfig struct {
	Addr     string // Listen address (default ":8080")
	LogLevel string // Log level: debug, info, warn, error
	LogFormat string // Log format: text, json
}

// DefaultServerConfig returns sensible defaults.
func DefaultServerConfig() ServerConfig {
	return ServerConfig{
		Addr:      ":8080",
		LogLevel:  "info",
		LogFormat: "text",
	}
}
