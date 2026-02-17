// cwl-runner is a reference CWL v1.2 runner implementation.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/me/gowe/internal/cwlrunner"
	"github.com/spf13/cobra"
)

var (
	outDir       string
	noContainer  bool
	forceDocker  bool
	outputFormat string
	verbose      bool
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "cwl-runner [flags] <cwl-file> [job-file]",
		Short: "CWL v1.2 reference runner",
		Long: `cwl-runner executes CWL v1.2 tools and workflows.

Examples:
  # Execute a tool with inputs
  cwl-runner tool.cwl job.yml

  # Validate a CWL document
  cwl-runner validate tool.cwl

  # Print the workflow DAG
  cwl-runner dag workflow.cwl job.yml

  # Print the command line without executing
  cwl-runner --print-commandline tool.cwl job.yml
`,
		Args: cobra.MinimumNArgs(1),
		RunE: runExecute,
	}

	// Flags.
	rootCmd.PersistentFlags().StringVar(&outDir, "outdir", "./cwl-output", "Output directory")
	rootCmd.PersistentFlags().BoolVar(&noContainer, "no-container", false, "Disable Docker execution")
	rootCmd.PersistentFlags().BoolVar(&forceDocker, "docker", false, "Force Docker execution")
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output-format", "json", "Output format (json|yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")

	// Subcommands.
	rootCmd.AddCommand(validateCmd())
	rootCmd.AddCommand(dagCmd())
	rootCmd.AddCommand(printCmdCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if verbose {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func newRunner(logger *slog.Logger) *cwlrunner.Runner {
	r := cwlrunner.NewRunner(logger)
	r.OutDir = outDir
	r.NoContainer = noContainer
	r.ForceDocker = forceDocker
	r.OutputFormat = outputFormat
	return r
}

func runExecute(cmd *cobra.Command, args []string) error {
	logger := newLogger()
	runner := newRunner(logger)

	cwlPath := args[0]
	jobPath := ""
	if len(args) > 1 {
		jobPath = args[1]
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		logger.Info("received interrupt, cancelling...")
		cancel()
	}()

	return runner.Execute(ctx, cwlPath, jobPath, os.Stdout)
}

func validateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "validate <cwl-file>",
		Short: "Validate a CWL document",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger()
			runner := newRunner(logger)

			ctx := context.Background()
			if err := runner.Validate(ctx, args[0]); err != nil {
				return err
			}
			fmt.Println("Document is valid")
			return nil
		},
	}
}

func dagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dag <cwl-file> [job-file]",
		Short: "Print the workflow DAG as JSON",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger()
			runner := newRunner(logger)

			cwlPath := args[0]
			jobPath := ""
			if len(args) > 1 {
				jobPath = args[1]
			}

			ctx := context.Background()
			return runner.PrintDAG(ctx, cwlPath, jobPath, os.Stdout)
		},
	}
}

func printCmdCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "print-command <cwl-file> [job-file]",
		Short: "Print the command line without executing",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := newLogger()
			runner := newRunner(logger)

			cwlPath := args[0]
			jobPath := ""
			if len(args) > 1 {
				jobPath = args[1]
			}

			ctx := context.Background()
			return runner.PrintCommandLine(ctx, cwlPath, jobPath, os.Stdout)
		},
	}
}
