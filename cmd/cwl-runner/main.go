// cwl-runner is a reference CWL v1.2 runner implementation.
package main

import (
	"context"
	"fmt"
	"github.com/me/gowe/internal/hostres"
	"github.com/me/gowe/internal/toolexec"
	"log/slog"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/me/gowe/internal/cwlrunner"
	"github.com/me/gowe/internal/validate"
	"github.com/spf13/cobra"
)

var (
	outDir            string
	noContainer       bool
	forceDocker       bool
	forceApptainer    bool
	containerRuntime  string
	outputFormat      string
	verbose           bool
	quiet             bool
	parallel          bool
	maxJobs           int
	coresBudget       int
	noFailFast        bool
	collectMetrics    bool
	imageDir          string
	noInputValidation bool

	preserveEnv       []string
	preserveEntireEnv bool

	// Worker-parity host resource flags (shared parsing: internal/hostres).
	preStageDir    string
	datasetAliases map[string]string
	extraBindPaths []string
	envVars        map[string]string
	envFile        string
)

// reservedEnvNames can not be set via --env/--env-file: the CWL spec fixes
// HOME to the output directory and TMPDIR to the temp directory.
var reservedEnvNames = []string{"HOME", "TMPDIR"}

const version = "1.2.1-dev"

// Version is the build version, stamped at release time via -ldflags "-X main.Version=...".
// (Distinct from `version` above, which reports the supported CWL spec version.)
var Version = "dev"

func main() {
	rootCmd := &cobra.Command{
		Use:     "cwl-runner [flags] <cwl-file> [job-file]",
		Short:   "CWL v1.2 reference runner",
		Version: fmt.Sprintf("%s (CWL spec %s)", Version, version),
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
	rootCmd.PersistentFlags().BoolVar(&noContainer, "no-container", false, "Disable container execution")
	rootCmd.PersistentFlags().BoolVar(&forceDocker, "docker", false, "Force Docker execution (alias for --runtime docker)")
	rootCmd.PersistentFlags().BoolVar(&forceApptainer, "apptainer", false, "Force Apptainer execution (alias for --runtime apptainer)")
	rootCmd.PersistentFlags().StringVar(&containerRuntime, "runtime", "", "Container runtime: docker, apptainer, or none (default: auto-detect from PATH)")
	rootCmd.PersistentFlags().StringVar(&outputFormat, "output-format", "json", "Output format (json|yaml)")
	rootCmd.PersistentFlags().BoolVarP(&verbose, "verbose", "v", false, "Enable verbose output")
	rootCmd.PersistentFlags().BoolVarP(&quiet, "quiet", "q", false, "Suppress informational output")

	// Parallel execution flags.
	rootCmd.PersistentFlags().BoolVar(&parallel, "parallel", false, "Enable parallel execution of independent steps and scatter iterations")
	rootCmd.PersistentFlags().IntVarP(&maxJobs, "jobs", "j", 0, "Maximum concurrent tool executions across the entire run -- top-level steps, scatter iterations, nested sub-workflow steps, and scatter-over-subworkflow iterations all share this one budget (default: number of CPUs)")
	rootCmd.PersistentFlags().IntVar(&coresBudget, "cores", 0, "Machine core budget: each concurrent tool execution additionally consumes its evaluated ResourceRequirement cores (default 1) from this budget; a requirement larger than the budget is clamped with a warning (default: 0, disabled)")
	rootCmd.PersistentFlags().BoolVar(&noFailFast, "no-fail-fast", false, "Continue execution after errors (default: fail fast)")

	// Metrics flags.
	rootCmd.PersistentFlags().BoolVar(&collectMetrics, "metrics", false, "Collect and display execution metrics (duration, memory usage)")

	// Image directory for local SIF files.
	rootCmd.PersistentFlags().StringVar(&imageDir, "image-dir", "", "Base directory for resolving relative .sif image paths in DockerRequirement")

	// Input validation (#273): cwl-runner enforces declared CWL input types
	// by default (standalone spec-conformance runner); this is the escape
	// hatch.
	rootCmd.PersistentFlags().BoolVar(&noInputValidation, "no-input-validation", false, "Disable validation of input values against their declared CWL types (default: enforced)")

	// Tools run directly on the host get an allow-listed environment
	// (PATH, LANG, LC_*, TZ, USER, LOGNAME, SHELL, TERM) rather than the
	// full host environment; these flags (cwltool parity) opt variables back in.
	rootCmd.PersistentFlags().StringArrayVar(&preserveEnv, "preserve-environment", nil, "Host environment variable NAME to pass to tools run on the host, in addition to the allow-list (repeatable)")
	rootCmd.PersistentFlags().BoolVar(&preserveEntireEnv, "preserve-entire-environment", false, "Pass the entire host environment to tools run on the host")

	registerHostResourceFlags(rootCmd)

	// Subcommands.
	rootCmd.AddCommand(validateCmd())
	rootCmd.AddCommand(dagCmd())
	rootCmd.AddCommand(printCmdCmd())

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// registerHostResourceFlags adds the worker-parity bind and env flags: same
// names and parsing as gowe-worker. Containers run with --containall
// --cleanenv, so these are the only way host databases/weights and settings
// reach a container.
func registerHostResourceFlags(rootCmd *cobra.Command) {
	rootCmd.PersistentFlags().StringVar(&preStageDir, "pre-stage-dir", "", "Directory with pre-staged datasets: bind-mounted (read-write, same path) into every container; no-op for host-mode tools")
	rootCmd.PersistentFlags().Var(&hostres.StringMapFlag{Values: &datasetAliases}, "dataset", "Dataset 'id=path' bind-mounted into every container at the same path (repeatable, also comma-separated; no-op for host-mode tools). Unlike the worker there is no scheduler here, so the id is informational")
	rootCmd.PersistentFlags().Var(&hostres.StringSliceFlag{Values: &extraBindPaths}, "extra-bind", "Extra host path bind-mounted (read-write, same path) into every container (repeatable, also comma-separated; no-op for host-mode tools)")
	rootCmd.PersistentFlags().Var(&hostres.StringMapFlag{Values: &envVars}, "env", "Environment variable 'NAME=value' injected into every tool, in containers and on the host (repeatable). Unlike --preserve-environment, which copies a host variable to host-mode tools only, this sets a value for all tools")
	rootCmd.PersistentFlags().StringVar(&envFile, "env-file", "", "File with environment variables injected into every tool (NAME=value per line, # comments); --env overrides file entries")
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

	// Wire container runtime: --runtime flag takes precedence over --docker/--apptainer/--no-container.
	switch containerRuntime {
	case "none":
		r.NoContainer = true
	case "docker", "apptainer":
		r.ContainerRuntime = containerRuntime
	default:
		r.NoContainer = noContainer
		if forceApptainer {
			r.ContainerRuntime = "apptainer"
		} else {
			r.ForceDocker = forceDocker
		}
	}

	// Configure parallel execution.
	if parallel {
		r.Parallel.Enabled = true
		if maxJobs > 0 {
			r.Parallel.MaxWorkers = maxJobs
		}
		if coresBudget > 0 {
			r.Parallel.CoresBudget = coresBudget
		}
		r.Parallel.FailFast = !noFailFast
	}

	// Configure metrics collection.
	r.CollectMetrics = collectMetrics

	// Configure image directory for local SIF files.
	r.ImageDir = imageDir

	r.PreserveEnv = preserveEnv
	r.PreserveEntireEnv = preserveEntireEnv

	// Configure input validation (#273): enforce by default (set in
	// cwlrunner.NewRunner), --no-input-validation disables it entirely.
	if noInputValidation {
		r.InputValidation = validate.ModeOff
	}

	return r
}

// coveredByBind reports whether path is a bind's host path or lies under it.
func coveredByBind(binds []toolexec.ExtraBind, path string) bool {
	for _, b := range binds {
		if path == b.HostPath || strings.HasPrefix(path, strings.TrimSuffix(b.HostPath, "/")+"/") {
			return true
		}
	}
	return false
}

// applyHostResources resolves the worker-parity bind and env flags onto the
// runner.
func applyHostResources(r *cwlrunner.Runner, logger *slog.Logger) error {
	datasets, binds, err := hostres.ResolveBinds(logger, preStageDir, datasetAliases, extraBindPaths)
	if err != nil {
		return err
	}
	// The worker registers --dataset aliases for scheduler affinity only;
	// a standalone run has no scheduler, so an alias is useful only as a
	// bind. Mount each --dataset path (unless an existing bind covers it).
	ids := make([]string, 0, len(datasetAliases))
	for id := range datasetAliases {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if p := datasets[id]; !coveredByBind(binds, p) {
			binds = append(binds, toolexec.ExtraBind{HostPath: p, ContainerPath: p})
		}
	}
	r.ExtraBinds = binds
	env, err := hostres.MergeEnv(envFile, envVars, reservedEnvNames...)
	if err != nil {
		return err
	}
	r.EnvVars = env
	if len(env) > 0 {
		names := make([]string, 0, len(env))
		for k := range env {
			names = append(names, k)
		}
		logger.Debug("loaded env vars", "names", names) // names only: values may be sensitive
	}
	return nil
}

func runExecute(cmd *cobra.Command, args []string) error {
	logger := newLogger()
	runner := newRunner(logger)
	if err := applyHostResources(runner, logger); err != nil {
		return err
	}

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
