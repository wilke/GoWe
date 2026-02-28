// cwl-runner is a reference CWL v1.2 runner implementation.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/me/gowe/internal/cwlrunner"
	"github.com/spf13/cobra"
)

var (
	outDir           string
	noContainer      bool
	forceDocker      bool
	containerRuntime string
	outputFormat     string
	verbose          bool
	quiet            bool
	parallel         bool
	maxJobs          int
	noFailFast       bool
	collectMetrics   bool
)

const version = "1.2.1-dev"

func main() {
	rootCmd := &cobra.Command{
		Use:     "cwl-runner [flags] <cwl-file> [job-file]",
		Short:   "CWL v1.2 reference runner",
		Version: version,
		Long:    `cwl-runner executes CWL v1.2 tools and workflows.

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
	rootCmd.PersistentFlags().BoolVar(&noContainer, "no-container", false, "Disable container execution")
	rootCmd.PersistentFlags().BoolVar(&forceDocker, "docker", false, "Force Docker execution (alias for --runtime docker)")
	rootCmd.PersistentFlags().StringVar(&containerRuntime, "runtime", "", "Container runtime: docker, apptainer, or none (default: auto-detect)")
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output-format", "json", "Output format (json|yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress informational output")

	// Parallel execution flags.
	rootCmd.PersistentFlags().BoolVar(&parallel, "parallel", false, "Enable parallel execution of independent steps and scatter iterations")
	rootCmd.PersistentFlags().IntVarP(&maxJobs, "jobs", "j", 0, "Maximum concurrent jobs (default: number of CPUs)")
	rootCmd.PersistentFlags().BoolVar(&noFailFast, "no-fail-fast", false, "Continue execution after errors (default: fail fast)")

	// Metrics flags.
	rootCmd.PersistentFlags().BoolVar(&collectMetrics, "metrics", false, "Collect and display execution metrics (duration, memory usage)")

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
	if quiet {
		level = slog.LevelError
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
}

func newRunner(logger *slog.Logger) *cwlrunner.Runner {
	r := cwlrunner.NewRunner(logger)
	r.OutDir = outDir
	r.OutputFormat = outputFormat

	// Wire container runtime: --runtime flag takes precedence over --docker/--no-container.
	switch containerRuntime {
	case "none":
		r.NoContainer = true
	case "docker", "apptainer":
		r.ContainerRuntime = containerRuntime
	default:
		r.NoContainer = noContainer
		r.ForceDocker = forceDocker
	}

	// Configure parallel execution.
	if parallel {
		r.Parallel.Enabled = true
		if maxJobs > 0 {
			r.Parallel.MaxWorkers = maxJobs
		}
		r.Parallel.FailFast = !noFailFast
	}

	// Configure metrics collection.
	r.CollectMetrics = collectMetrics

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

	// Parse the cwl path for #fragment (process ID selector).
	processID := ""
	if idx := strings.Index(cwlPath, "#"); idx != -1 {
		processID = cwlPath[idx+1:]
		cwlPath = cwlPath[:idx]
	}
	runner.ProcessID = processID

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

			cwlPath := args[0]
			// Strip #fragment if present (not needed for validation).
			if idx := strings.Index(cwlPath, "#"); idx != -1 {
				cwlPath = cwlPath[:idx]
			}

			ctx := context.Background()
			if err := runner.Validate(ctx, cwlPath); err != nil {
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
			// Parse #fragment for process ID selection.
			if idx := strings.Index(cwlPath, "#"); idx != -1 {
				runner.ProcessID = cwlPath[idx+1:]
				cwlPath = cwlPath[:idx]
			}

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
			// Parse #fragment for process ID selection.
			if idx := strings.Index(cwlPath, "#"); idx != -1 {
				runner.ProcessID = cwlPath[idx+1:]
				cwlPath = cwlPath[:idx]
			}

			jobPath := ""
			if len(args) > 1 {
				jobPath = args[1]
			}

			ctx := context.Background()
			return runner.PrintCommandLine(ctx, cwlPath, jobPath, os.Stdout)
		},
	}
}
