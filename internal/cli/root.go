package cli

import (
	"log/slog"

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

	root.PersistentFlags().StringVar(&flagServer, "server", "http://localhost:8080", "GoWe server URL")
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
