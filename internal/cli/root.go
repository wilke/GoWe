package cli

import (
	"log/slog"
	"os"

	"github.com/me/gowe/internal/logging"
	"github.com/spf13/cobra"
)

var (
	flagServer    string
	flagDebug     bool
	flagLogLevel  string
	flagLogFormat string

	logger *slog.Logger
	client *Client
)

// defaultServer returns the default server URL, checking GOWE_SERVER env var first.
func defaultServer() string {
	if s := os.Getenv("GOWE_SERVER"); s != "" {
		return s
	}
	return "http://localhost:8080"
}

// NewRootCmd creates the root cobra command for the gowe CLI.
func NewRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "gowe",
		Short: "GoWe — CWL workflow engine for BV-BRC",
		Long:  "GoWe submits, monitors, and manages CWL workflows on BV-BRC.",
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if flagDebug {
				flagLogLevel = "debug"
			}
			logger = logging.NewLogger(logging.ParseLevel(flagLogLevel), flagLogFormat)
			client = NewClient(flagServer, logger)
		},
		SilenceUsage: true,
	}

	root.PersistentFlags().StringVar(&flagServer, "server", defaultServer(), "GoWe server URL (or GOWE_SERVER env)")
	root.PersistentFlags().BoolVar(&flagDebug, "debug", false, "Enable debug logging")
	root.PersistentFlags().StringVar(&flagLogLevel, "log-level", "info", "Log level (debug, info, warn, error)")
	root.PersistentFlags().StringVar(&flagLogFormat, "log-format", "text", "Log format (text, json)")

	root.AddCommand(
		newLoginCmd(),
		newSubmitCmd(),
		newStatusCmd(),
		newListCmd(),
		newCancelCmd(),
		newLogsCmd(),
		newAppsCmd(),
		newRunCmd(),
	)

	return root
}
