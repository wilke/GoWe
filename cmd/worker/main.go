package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/me/gowe/internal/hostres"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/me/gowe/internal/logging"
	"github.com/me/gowe/internal/worker"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// Version is set at build time via -ldflags.
var Version = "dev"

func main() {
	var cfg worker.Config

	// Server connection flags.
	showVersion := flag.Bool("version", false, "Print the build version and exit")
	flag.StringVar(&cfg.ServerURL, "server", "http://localhost:8080", "GoWe server URL")
	flag.StringVar(&cfg.Name, "name", "", "Worker name (default: hostname)")
	flag.StringVar(&cfg.Group, "group", "default", "Worker group for task scheduling")
	var workerKeyArgv, workerKeyFile string
	flag.StringVar(&workerKeyArgv, "worker-key", "", "REFUSED (any use, even empty): the key would be visible in /proc/<pid>/cmdline; use --worker-key-file")
	flag.StringVar(&workerKeyFile, "worker-key-file", "", "File holding the worker authentication key sent as X-Worker-Key (regular file, mode 0600 or stricter, and not under --workdir/--image-dir/--pre-stage-dir/--extra-bind/--dataset paths, which tools can read). Key file only: --worker-key (argv) and "+workerKeyEnv+" (environment) are refused because both are readable by same-uid host processes (/proc/<pid>/cmdline, /proc/<pid>/environ), and tools without DockerRequirement run on the host on every worker")
	flag.StringVar(&cfg.Runtime, "runtime", "none", "Container runtime(s), comma-separated (docker, apptainer, none)")
	flag.StringVar(&cfg.WorkDir, "workdir", "", "Local working directory (default: $TMPDIR/gowe-worker)")
	flag.StringVar(&cfg.StageOut, "stage-out", "local", "Output staging mode (local, file:///path, http://..., s3://bucket, shock://host)")
	flag.DurationVar(&cfg.Poll, "poll", 5*time.Second, "Poll interval")
	flag.BoolVar(&cfg.KeepTaskDirs, "keep-task-dirs", false, "Retain task working directories after successful completion (failed tasks are always kept)")

	// Staging mode flags.
	var stageMode string
	flag.StringVar(&stageMode, "stage-mode", "copy", "File staging mode (copy, symlink, reference)")

	// Docker-in-Docker path mapping (also reads DOCKER_HOST_PATH_MAP env var).
	var dockerHostPathMapStr string
	flag.StringVar(&dockerHostPathMapStr, "docker-host-path-map", "", "Path mapping for DinD: 'container1=host1:container2=host2' (or DOCKER_HOST_PATH_MAP env)")

	// Docker volume name for sharing files between worker and tool containers.
	var dockerVolume string
	flag.StringVar(&dockerVolume, "docker-volume", "", "Named Docker volume shared with tool containers (or DOCKER_VOLUME env)")

	// Input path mapping - translates host paths in task inputs to local container paths.
	var inputPathMapStr string
	flag.StringVar(&inputPathMapStr, "input-path-map", "", "Input path mapping: 'hostpath1=localpath1:hostpath2=localpath2' (or INPUT_PATH_MAP env)")

	// Degraded-mode flags (reduced tool isolation).
	flag.BoolVar(&cfg.InheritHostEnv, "inherit-host-env", false, "DEGRADED: let tools run directly on the host (--runtime none) inherit the worker's full environment instead of an allow-list")
	flag.BoolVar(&cfg.ApptainerNoContainall, "apptainer-no-containall", false, "DEGRADED: run Apptainer without --containall (--cleanenv is kept), for hosts without namespace support")

	// GPU flags.
	flag.BoolVar(&cfg.GPU.Enabled, "gpu", false, "Enable GPU support (passes --nv to Apptainer, --gpus to Docker)")
	flag.StringVar(&cfg.GPU.DeviceID, "gpu-id", "", "Specific GPU device ID (e.g., '0', '1', '0,1') - empty means all/auto")

	// Resource limit flags.
	flag.Int64Var(&cfg.Resources.MaxMemMB, "max-mem", 0, "Maximum memory in MiB for containers (0 = auto-detect from system)")
	flag.IntVar(&cfg.Resources.MaxCPUs, "max-cpus", 0, "Maximum CPUs for containers (0 = auto-detect from system)")

	// Image directory for local SIF files.
	flag.StringVar(&cfg.ImageDir, "image-dir", "", "Base directory for resolving relative .sif image paths in DockerRequirement")

	// Pre-staged reference data.
	var preStageDir string
	var extraBindPaths []string
	var datasetAliases map[string]string
	flag.StringVar(&preStageDir, "pre-stage-dir", "", "Directory with pre-staged datasets (auto-scanned, bind-mounted into containers)")
	flag.Var(&hostres.StringSliceFlag{Values: &extraBindPaths}, "extra-bind", "Extra bind mount path (repeatable, also comma-separated)")
	flag.Var(&hostres.StringMapFlag{Values: &datasetAliases}, "dataset", "Dataset alias 'id=path' (repeatable, also comma-separated)")

	// Secret environment variables for containers.
	var secretVars map[string]string
	var secretFile string
	flag.Var(&hostres.StringMapFlag{Values: &secretVars}, "secret", "Secret env var 'NAME=value' injected into containers (repeatable, never sent to server)")
	flag.StringVar(&secretFile, "secret-file", "", "File with secret env vars (NAME=value per line, # comments)")

	// Non-secret environment variables for containers.
	var envVars map[string]string
	var envFile string
	flag.Var(&hostres.StringMapFlag{Values: &envVars}, "env", "Environment var 'NAME=value' injected into containers (repeatable)")
	flag.StringVar(&envFile, "env-file", "", "File with environment vars (NAME=value per line, # comments)")

	// TLS flags (applies to server API + HTTPS staging).
	var caCert string
	var insecure bool
	flag.StringVar(&caCert, "ca-cert", "", "Path to CA certificate PEM file for internal PKI")
	flag.BoolVar(&insecure, "insecure", false, "Skip TLS verification (testing only)")

	// HTTP stager flags.
	var httpTimeout time.Duration
	var httpRetries int
	var httpRetryDelay time.Duration
	var httpCredentials string
	var httpUploadURL string
	var httpUploadMethod string
	flag.DurationVar(&httpTimeout, "http-timeout", 5*time.Minute, "HTTP request timeout")
	flag.IntVar(&httpRetries, "http-retries", 3, "HTTP retry attempts")
	flag.DurationVar(&httpRetryDelay, "http-retry-delay", 1*time.Second, "Initial HTTP retry delay")
	flag.StringVar(&httpCredentials, "http-credentials", "", "Path to credentials JSON file")
	flag.StringVar(&httpUploadURL, "http-upload-url", "", "URL template for StageOut uploads (e.g., https://data.example.com/outputs/{taskID}/{filename})")
	flag.StringVar(&httpUploadMethod, "http-upload-method", "PUT", "HTTP method for uploads (PUT or POST)")

	// S3 stager flags.
	var s3Endpoint string
	var s3Region string
	var s3AccessKey string
	var s3SecretKey string
	var s3Bucket string
	var s3PathStyle bool
	var s3DisableSSL bool
	flag.StringVar(&s3Endpoint, "s3-endpoint", "", "S3-compatible endpoint URL (e.g., minio.example.com:9000)")
	flag.StringVar(&s3Region, "s3-region", "us-east-1", "S3 region")
	flag.StringVar(&s3AccessKey, "s3-access-key", "", "S3 access key ID (or AWS_ACCESS_KEY_ID env)")
	flag.StringVar(&s3SecretKey, "s3-secret-key", "", "S3 secret access key (or AWS_SECRET_ACCESS_KEY env)")
	flag.StringVar(&s3Bucket, "s3-bucket", "", "Default S3 bucket for stage-out")
	flag.BoolVar(&s3PathStyle, "s3-path-style", false, "Use path-style S3 addressing (required for MinIO)")
	flag.BoolVar(&s3DisableSSL, "s3-disable-ssl", false, "Disable SSL for S3 (local development only)")

	// Workspace stager flags.
	var workspaceStager bool
	var workspaceURL string
	flag.BoolVar(&workspaceStager, "workspace-stager", false, "Enable workspace (ws://) staging for BV-BRC workspace files")
	flag.StringVar(&workspaceURL, "workspace-url", "", "BV-BRC Workspace service URL (default: production)")

	// Shock stager flags.
	var shockHost string
	var shockToken string
	var shockUseHTTP bool
	flag.StringVar(&shockHost, "shock-host", "", "Default Shock server host (e.g., p3.theseed.org)")
	flag.StringVar(&shockToken, "shock-token", "", "Shock authentication token (or SHOCK_TOKEN env)")
	flag.BoolVar(&shockUseHTTP, "shock-use-http", false, "Use HTTP instead of HTTPS for Shock (local development)")

	// Shared filesystem stager flags.
	var sharedEnabled bool
	var sharedPathMapStr string
	var sharedStageOutDir string
	flag.BoolVar(&sharedEnabled, "shared-fs", false, "Enable shared filesystem stager")
	flag.StringVar(&sharedPathMapStr, "shared-path-map", "", "Shared filesystem path mapping: 'host1=local1:host2=local2'")
	flag.StringVar(&sharedStageOutDir, "shared-stage-out-dir", "", "Shared filesystem stage-out directory")

	// Logging flags.
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	logFormat := flag.String("log-format", "text", "Log format (text, json)")
	debug := flag.Bool("debug", false, "Shorthand for --log-level=debug")
	flag.Parse()

	if *showVersion {
		fmt.Println("gowe-worker " + Version)
		os.Exit(0)
	}

	if *debug {
		*logLevel = "debug"
	}

	logger := logging.NewLogger(logging.ParseLevel(*logLevel), *logFormat)

	// Resolve the worker key (file or env only; argv is refused).
	workerKeyOnArgv := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "worker-key" {
			workerKeyOnArgv = true
		}
	})
	workerKey, err := resolveWorkerKey(workerKeyOnArgv, workerKeyFile, os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	cfg.WorkerKey = workerKey

	if cfg.InheritHostEnv {
		logger.Warn("--inherit-host-env is set: tools running directly on the host inherit the worker's full environment, including any provider tokens or secrets in it")
	}
	if cfg.ApptainerNoContainall {
		logger.Warn("--apptainer-no-containall is set: Apptainer containers share the host PID namespace and /tmp; only --cleanenv remains")
	}

	// Validate runtime flag.
	if err := model.ValidateRuntimes(cfg.Runtime); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	// Validate declared runtimes are actually installed.
	cfg.Runtime = validateInstalledRuntimes(cfg.Runtime, logger)

	// Parse Docker host path map from flag or environment variable.
	if dockerHostPathMapStr == "" {
		dockerHostPathMapStr = os.Getenv("DOCKER_HOST_PATH_MAP")
	}
	if dockerHostPathMapStr != "" {
		cfg.DockerHostPathMap = parsePathMap(dockerHostPathMapStr)
	}

	// Parse Docker volume from flag or environment variable.
	if dockerVolume == "" {
		dockerVolume = os.Getenv("DOCKER_VOLUME")
	}
	if dockerVolume != "" {
		cfg.DockerVolume = dockerVolume
	}

	// Parse input path map from flag or environment variable.
	if inputPathMapStr == "" {
		inputPathMapStr = os.Getenv("INPUT_PATH_MAP")
	}
	if inputPathMapStr != "" {
		cfg.InputPathMap = parsePathMap(inputPathMapStr)
	}

	// Wire pre-stage-dir, extra-bind, and dataset flags into config.
	cfg.PreStageDir = preStageDir
	cfg.ExtraBinds = extraBindPaths
	cfg.Datasets = datasetAliases

	// The key file must not be readable by tools: they run as the worker uid.
	if workerKeyFile != "" {
		workDir := cfg.WorkDir
		if workDir == "" {
			workDir = filepath.Join(os.TempDir(), "gowe-worker")
		}
		bound := []string{cfg.PreStageDir, cfg.ImageDir, workDir}
		bound = append(bound, cfg.ExtraBinds...)
		for _, p := range cfg.Datasets {
			bound = append(bound, p)
		}
		if err := checkKeyFileNotBound(workerKeyFile, bound); err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
	}

	// Wire build version.
	cfg.Version = Version

	// Wire secrets: merge --secret flags with --secret-file.
	secrets := make(map[string]string)
	if secretFile != "" {
		loaded, err := hostres.ParseEnvFile(secretFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load secret file: %v\n", err)
			os.Exit(1)
		}
		for k, v := range loaded {
			secrets[k] = v
		}
	}
	for k, v := range secretVars {
		secrets[k] = v // CLI flags override file entries
	}
	if len(secrets) > 0 {
		cfg.Secrets = secrets
		// Log secret names only, never values.
		names := make([]string, 0, len(secrets))
		for k := range secrets {
			names = append(names, k)
		}
		logger.Info("loaded secrets", "names", names)
	}

	// Wire env vars: merge --env flags with --env-file.
	envMap := make(map[string]string)
	if envFile != "" {
		loaded, err := hostres.ParseEnvFile(envFile) // same format: NAME=value
		if err != nil {
			fmt.Fprintf(os.Stderr, "load env file: %v\n", err)
			os.Exit(1)
		}
		for k, v := range loaded {
			envMap[k] = v
		}
	}
	for k, v := range envVars {
		envMap[k] = v
	}
	if _, ok := envMap[workerKeyEnv]; ok {
		fmt.Fprintf(os.Stderr, "%s must not be set via --env/--env-file: it would be injected into every tool\n", workerKeyEnv)
		os.Exit(1)
	}
	if len(envMap) > 0 {
		cfg.EnvVars = envMap
		// Names only: values may still be sensitive.
		names := make([]string, 0, len(envMap))
		for k := range envMap {
			names = append(names, k)
		}
		logger.Info("loaded env vars", "names", names)
	}

	// Default worker name to hostname.
	if cfg.Name == "" {
		h, err := os.Hostname()
		if err != nil {
			cfg.Name = "worker"
		} else {
			cfg.Name = h
		}
	}

	// Resolve hostname for registration.
	cfg.Hostname, _ = os.Hostname()

	// Build stager config.
	cfg.Stager = worker.DefaultStagerConfig()
	cfg.Stager.StageOutMode = cfg.StageOut
	cfg.Stager.StageMode = parseStageMode(stageMode)
	cfg.Stager.TLS.CACertPath = caCert
	cfg.Stager.TLS.InsecureSkipVerify = insecure
	cfg.Stager.HTTP.Timeout = httpTimeout
	cfg.Stager.HTTP.MaxRetries = httpRetries
	cfg.Stager.HTTP.RetryDelay = httpRetryDelay
	cfg.Stager.HTTP.UploadMethod = httpUploadMethod
	cfg.Stager.HTTP.UploadPath = httpUploadURL

	// Load credentials from file if specified.
	if httpCredentials != "" {
		creds, err := worker.LoadCredentialsFile(httpCredentials)
		if err != nil {
			fmt.Fprintf(os.Stderr, "load credentials: %v\n", err)
			os.Exit(1)
		}
		cfg.Stager.HTTP.Credentials = creds
		logger.Info("loaded HTTP credentials", "hosts", len(creds))
	}

	// S3 configuration.
	if s3AccessKey == "" {
		s3AccessKey = os.Getenv("AWS_ACCESS_KEY_ID")
	}
	if s3SecretKey == "" {
		s3SecretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
	}
	cfg.Stager.S3.Endpoint = s3Endpoint
	cfg.Stager.S3.Region = s3Region
	cfg.Stager.S3.AccessKeyID = s3AccessKey
	cfg.Stager.S3.SecretAccessKey = s3SecretKey
	cfg.Stager.S3.DefaultBucket = s3Bucket
	cfg.Stager.S3.UsePathStyle = s3PathStyle
	cfg.Stager.S3.DisableSSL = s3DisableSSL

	// Workspace stager configuration.
	cfg.WorkspaceStager = workspaceStager
	cfg.WorkspaceURL = workspaceURL

	// Shock configuration.
	if shockToken == "" {
		shockToken = os.Getenv("SHOCK_TOKEN")
	}
	cfg.Stager.Shock.DefaultHost = shockHost
	cfg.Stager.Shock.Token = shockToken
	cfg.Stager.Shock.UseHTTP = shockUseHTTP

	// Shared filesystem configuration.
	cfg.Stager.Shared.Enabled = sharedEnabled
	if sharedPathMapStr != "" {
		cfg.Stager.Shared.PathMap = parsePathMap(sharedPathMapStr)
	}
	cfg.Stager.Shared.StageOutDir = sharedStageOutDir

	w, err := worker.New(cfg, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init worker: %v\n", err)
		os.Exit(1)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting worker",
		"server", cfg.ServerURL,
		"runtime", cfg.Runtime,
		"workdir", cfg.WorkDir,
		"poll", cfg.Poll,
		"gpu", cfg.GPU.Enabled,
		"gpu_id", cfg.GPU.DeviceID,
		"max_cpus", cfg.Resources.MaxCPUs,
		"max_mem_mb", cfg.Resources.MaxMemMB,
		"image_dir", cfg.ImageDir,
	)

	if err := w.Run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "worker error: %v\n", err)
		os.Exit(1)
	}

	logger.Info("worker stopped")
}

// parsePathMap parses a path mapping string in the format "src1=dst1:src2=dst2".
func parsePathMap(s string) map[string]string {
	result := make(map[string]string)
	if s == "" {
		return result
	}
	for _, pair := range strings.Split(s, ":") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) == 2 {
			result[parts[0]] = parts[1]
		}
	}
	return result
}

// validateInstalledRuntimes checks that each declared runtime binary actually
// exists on the system. Runtimes that aren't found are dropped with a warning.
// Returns "none" if no declared runtimes are available.
func validateInstalledRuntimes(declared string, logger *slog.Logger) string {
	var valid []string
	for _, rt := range strings.Split(declared, ",") {
		rt = strings.TrimSpace(rt)
		switch rt {
		case "docker", "apptainer":
			if _, err := exec.LookPath(rt); err != nil {
				// Also check for "singularity" as apptainer alias.
				if rt == "apptainer" {
					if _, err2 := exec.LookPath("singularity"); err2 == nil {
						valid = append(valid, rt)
						continue
					}
				}
				logger.Warn("declared runtime not found, skipping", "runtime", rt)
				continue
			}
			valid = append(valid, rt)
		case "none":
			valid = append(valid, rt)
		}
	}
	if len(valid) == 0 {
		return "none"
	}
	return strings.Join(valid, ",")
}

// parseStageMode parses a stage mode string.
func parseStageMode(s string) staging.StageMode {
	switch strings.ToLower(s) {
	case "symlink":
		return staging.StageModeSymlink
	case "reference", "ref":
		return staging.StageModeReference
	default:
		return staging.StageModeCopy
	}
}
