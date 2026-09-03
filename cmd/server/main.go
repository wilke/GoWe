package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/cwltool"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/logging"
	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/scheduler"
	"github.com/me/gowe/internal/server"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/tokencrypt"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// Version is the build version, stamped at release time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	cfg := config.DefaultServerConfig()

	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "Listen address")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level (debug, info, warn, error)")
	flag.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log format (text, json)")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Database path (default ~/.gowe/gowe.db)")
	flag.StringVar(&cfg.DefaultExecutor, "default-executor", cfg.DefaultExecutor, "Default executor when no CWL hint is set: local, docker, worker (empty for auto)")
	forceExecutor := flag.String("force-executor", "", "Force all tasks to this executor, ignoring CWL hints (testing only)")
	imageDir := flag.String("image-dir", "", "Base directory for resolving relative .sif image paths in DockerRequirement")
	debug := flag.Bool("debug", false, "Shorthand for --log-level=debug")
	showVersion := flag.Bool("version", false, "Print the build version and exit")

	// TLS / secure-cookie options.
	flag.StringVar(&cfg.TLSCertFile, "tls-cert", cfg.TLSCertFile, "Path to PEM certificate; enables native HTTPS when set together with --tls-key")
	flag.StringVar(&cfg.TLSKeyFile, "tls-key", cfg.TLSKeyFile, "Path to PEM private key; enables native HTTPS when set together with --tls-cert")
	flag.BoolVar(&cfg.SecureCookies, "secure-cookies", cfg.SecureCookies, "Always set the Secure attribute on session cookies (implied by --tls-cert/--tls-key)")
	flag.BoolVar(&cfg.BehindProxy, "behind-proxy", cfg.BehindProxy, "Server sits behind a trusted TLS-terminating proxy: force Secure cookies and emit HSTS (enable only when the public leg is HTTPS)")
	flag.StringVar(&cfg.GrafanaURL, "grafana-url", cfg.GrafanaURL, "External Grafana URL, linked from the web UI nav (e.g. the GoWe Overview dashboard fed by --metrics-addr); empty hides the link")
	corsOrigins := flag.String("cors-origins", "", "Comma-separated list of exact browser origins allowed to call /api/v1 cross-origin (e.g. https://app.example.com); empty disables CORS entirely (default: no CORS headers, OPTIONS 405s as before). Prefer a same-origin reverse proxy that injects the token server-side over this flag for browser clients — see docs/PRODUCTION.md")

	// Scheduler options
	schedulerPoll := flag.Duration("scheduler-poll", 2*time.Second, "Scheduler poll interval")
	workspaceStaging := flag.String("workspace-staging", "", "Workspace staging mode: 'server' (pre/post-stage ws:// on server) or empty (passthrough to workers)")
	wsStagingURL := flag.String("workspace-url", "", "BV-BRC Workspace service URL for server-side staging and the web UI workspace browser (default: production)")
	redeliverSourceDirs := flag.String("redeliver-source-dirs", "", "Comma-separated directories the admin re-delivery endpoint may read staged originals from (e.g. the shared stage-out dir); empty refuses local re-upload")
	preflightDeferral := flag.Int("preflight-deferral", 30, "Ticks to defer worker task dispatch when no capable worker exists (0=disable)")
	stuckTaskThreshold := flag.Int("stuck-task-threshold", 30, "Consecutive zero-progress ticks before QUEUED tasks are flagged as stuck (0=disable)")
	stuckTaskAction := flag.String("stuck-task-action", "warn", "Action for stuck tasks: 'warn' (log only) or 'fail' (also fail oldest task)")
	tokenInjectGroups := flag.String("token-inject-groups", "", "Comma-separated worker groups whose tasks auto-receive the submitter's provider token without the per-tool inject_bvbrc_token opt-in (e.g. bvbrc,esmfold). Empty = opt-in only")

	// Metrics options
	metricsAddr := flag.String("metrics-addr", "", "Listen address for a second, unauthenticated HTTP server exposing only /metrics (Prometheus); empty disables metrics entirely. Bind to localhost or a private interface — this endpoint has no auth")
	metricsWorkflowLabel := flag.Bool("metrics-workflow-label", true, "Include the (unbounded, user-authored) workflow name as a Prometheus label; false maps every observation to workflow=\"_all\" instead")
	metricsLabelCap := flag.Int("metrics-label-cap", metrics.DefaultLabelCap, "Per-label distinct-value cap for the user-authored workflow/step Prometheus labels; values beyond the cap collapse into \"_other\"")

	// Authentication options
	allowAnonymous := flag.Bool("allow-anonymous", false, "Allow unauthenticated access as anonymous user")
	anonymousExecutors := flag.String("anonymous-executors", "local,docker,worker", "Comma-separated list of executors allowed for anonymous users")
	admins := flag.String("admins", "", "Comma-separated list of admin usernames (also: GOWE_ADMINS env)")
	configFile := flag.String("config", "", "Path to server config file (for admins, worker keys)")
	workerKeyFile := flag.String("worker-keys", "", "Path to worker keys JSON file")

	// Provider-token encryption at rest
	tokenKeyFile := flag.String("token-key-file", "", "Path to a file holding the token-encryption key (base64 or hex, 32 bytes); overrides GOWE_TOKEN_KEY")
	allowPlaintextTokens := flag.Bool("allow-plaintext-tokens", false, "Permit storing provider tokens in plaintext when no encryption key is set (migration/dev only)")

	// File upload proxy options
	uploadBackend := flag.String("upload-backend", "", "Enable file upload proxy with backend: shock, s3, local")
	uploadMaxSize := flag.Int64("upload-max-size", 1<<30, "Maximum upload size in bytes for the file proxy and web UI workspace uploads (default: 1GB)")

	// Shock upload options
	uploadShockHost := flag.String("upload-shock-host", "", "Shock server host for uploads (e.g., localhost:7445)")
	uploadShockHTTP := flag.Bool("upload-shock-http", false, "Use HTTP instead of HTTPS for Shock uploads")
	uploadShockToken := flag.String("upload-shock-token", "", "Shock authentication token for uploads")

	// S3 upload options
	uploadS3Endpoint := flag.String("upload-s3-endpoint", "", "S3 endpoint for uploads (empty = AWS)")
	uploadS3Region := flag.String("upload-s3-region", "us-east-1", "S3 region for uploads")
	uploadS3Bucket := flag.String("upload-s3-bucket", "", "S3 bucket for uploads")
	uploadS3Prefix := flag.String("upload-s3-prefix", "uploads/", "S3 key prefix for uploads")
	uploadS3AccessKey := flag.String("upload-s3-access-key", "", "S3 access key (or AWS_ACCESS_KEY_ID env)")
	uploadS3SecretKey := flag.String("upload-s3-secret-key", "", "S3 secret key (or AWS_SECRET_ACCESS_KEY env)")
	uploadS3PathStyle := flag.Bool("upload-s3-path-style", false, "Use path-style S3 addressing")
	uploadS3DisableSSL := flag.Bool("upload-s3-disable-ssl", false, "Disable SSL for S3 uploads")

	// Local upload options
	uploadLocalDir := flag.String("upload-local-dir", "", "Local directory for file uploads")

	// Download options
	uploadDownloadDirs := flag.String("upload-download-dirs", "", "Comma-separated list of directories allowed for file download")

	flag.Parse()

	if *showVersion {
		fmt.Println("gowe-server " + Version)
		os.Exit(0)
	}
	cfg.Version = Version

	if *debug {
		cfg.LogLevel = "debug"
	}

	logger := logging.NewLogger(logging.ParseLevel(cfg.LogLevel), cfg.LogFormat)

	// Validate TLS flags: cert and key must be provided together.
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		fmt.Fprintln(os.Stderr, "both --tls-cert and --tls-key must be provided to enable native HTTPS")
		os.Exit(1)
	}
	if cfg.TLSEnabled() {
		// Native TLS implies Secure cookies.
		cfg.SecureCookies = true
	} else if cfg.BehindProxy {
		// A TLS-terminating proxy means the browser leg is always HTTPS even
		// though this server sees plain HTTP. Force Secure cookies so a missing
		// or non-standard X-Forwarded-Proto header can't silently downgrade the
		// session cookie to non-Secure and leak it on a plaintext hop (GH #136).
		cfg.SecureCookies = true
	} else if cfg.SecureCookies {
		// Forcing Secure cookies on a plaintext deployment (no TLS, no proxy)
		// means browsers will refuse to send the cookie over plain HTTP,
		// breaking sessions. Warn so misconfiguration is visible.
		logger.Warn("--secure-cookies is set without native TLS or --behind-proxy; session cookies will not be sent over plain HTTP")
	}

	// CORS is opt-in: a token-issuing API must not become browser-reachable
	// by accident. Empty (the default) leaves /api/v1 exactly as it always
	// behaved. When set, only the listed exact origins get CORS headers.
	if *corsOrigins != "" {
		var origins []string
		for _, o := range strings.Split(*corsOrigins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				origins = append(origins, o)
			}
		}
		cfg.CORSOrigins = origins
		if len(origins) > 0 {
			logger.Info("CORS enabled for /api/v1", "origins", origins)
		}
	}

	// Resolve database path.
	dbPath := cfg.DBPath
	if dbPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "cannot determine home directory: %v\n", err)
			os.Exit(1)
		}
		dir := filepath.Join(home, ".gowe")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "cannot create %s: %v\n", dir, err)
			os.Exit(1)
		}
		dbPath = filepath.Join(dir, "gowe.db")
	}

	// Open store and run migrations.
	st, err := store.NewSQLiteStore(dbPath, logger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "open database: %v\n", err)
		os.Exit(1)
	}
	defer st.Close()

	if err := st.Migrate(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "migrate database: %v\n", err)
		os.Exit(1)
	}
	logger.Info("database ready", "path", dbPath)

	// Configure provider-token encryption at rest. A configured key encrypts the
	// submitter's token in submissions.user_token and any bearer credential in
	// tasks.runtime_hints. With no key, delegated submissions (those carrying a
	// provider token) are refused unless --allow-plaintext-tokens is set.
	tokenCipher, err := loadTokenCipher(*tokenKeyFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "token encryption key: %v\n", err)
		os.Exit(1)
	}
	switch {
	case tokenCipher != nil:
		st.ConfigureTokenEncryption(tokenCipher, true)
		if nSub, nTask, rerr := st.ReencryptPlaintextTokens(context.Background()); rerr != nil {
			logger.Warn("re-encrypting existing plaintext tokens", "error", rerr)
		} else if nSub > 0 || nTask > 0 {
			logger.Info("upgraded existing plaintext tokens to encrypted", "submissions", nSub, "tasks", nTask)
		}
		logger.Info("provider-token encryption at rest enabled")
	case *allowPlaintextTokens:
		st.ConfigureTokenEncryption(nil, false)
		logger.Warn("provider tokens will be stored in PLAINTEXT (--allow-plaintext-tokens); set GOWE_TOKEN_KEY to encrypt at rest")
	default:
		// Fail closed: no key, no explicit opt-in.
		st.ConfigureTokenEncryption(nil, true)
		logger.Warn("no token-encryption key configured; delegated submissions carrying a provider token will be refused — set GOWE_TOKEN_KEY (or pass --allow-plaintext-tokens to allow plaintext)")
	}

	// Create executor registry and register executors.
	reg := executor.NewRegistry(logger)
	localExec := executor.NewLocalExecutor("", logger)
	if *imageDir != "" {
		localExec.SetImageDir(*imageDir)
	}
	reg.Register(localExec)
	// Detect container runtime and register the appropriate executor.
	containerRuntime := cwltool.DetectContainerRuntime()
	switch containerRuntime {
	case "docker":
		reg.Register(executor.NewDockerExecutor("", logger))
		logger.Info("container executor registered", "runtime", "docker")
	case "apptainer":
		apptainerExec := executor.NewApptainerExecutor("", logger)
		if *imageDir != "" {
			apptainerExec.SetImageDir(*imageDir)
		}
		reg.Register(apptainerExec)
		logger.Info("container executor registered", "runtime", "apptainer")
	default:
		logger.Info("no container runtime detected (docker, apptainer); container executor not registered")
	}
	reg.Register(executor.NewWorkerExecutor(st, logger))

	// Prometheus metrics registry. Constructing it unconditionally (nil only
	// when --metrics-addr is left empty) keeps every instrumentation call
	// site in server/scheduler guard-free — a nil *metrics.Registry no-ops
	// every method — while the actual HTTP listener stays opt-in.
	var metricsReg *metrics.Registry
	if *metricsAddr != "" {
		metricsReg = metrics.NewRegistry(metrics.Config{
			LabelCap:             *metricsLabelCap,
			DisableWorkflowLabel: !*metricsWorkflowLabel,
		})
	}

	// Register BVBRCExecutor and create RPC callers if a token is available.
	serverOpts := []server.Option{
		server.WithExecutorRegistry(reg),
		// The web UI browses and uploads to the Workspace as the logged-in
		// user (session token), never as the server's service account.
		server.WithWorkspaceURL(*wsStagingURL),
		server.WithUIUploadMaxSize(*uploadMaxSize),
		server.WithMetrics(metricsReg),
	}

	// Configure admin role assignment.
	adminConfig := server.NewAdminConfig(st, "GOWE_ADMINS", *configFile)
	if *admins != "" {
		var cliAdmins []string
		for _, u := range strings.Split(*admins, ",") {
			u = strings.TrimSpace(u)
			if u != "" {
				cliAdmins = append(cliAdmins, u)
			}
		}
		adminConfig.WithCLIAdmins(cliAdmins)
	}
	serverOpts = append(serverOpts, server.WithAdminConfig(adminConfig))
	if len(adminConfig.CLIAdmins()) > 0 {
		logger.Info("admin users from flag", "admins", adminConfig.CLIAdmins())
	}
	if len(adminConfig.EnvAdmins()) > 0 {
		logger.Info("admin users from env", "admins", adminConfig.EnvAdmins())
	}
	if len(adminConfig.FileAdmins()) > 0 {
		logger.Info("admin users from config", "admins", adminConfig.FileAdmins())
	}

	// Configure anonymous access.
	if *allowAnonymous {
		var allowedExecutors []model.ExecutorType
		for _, exec := range strings.Split(*anonymousExecutors, ",") {
			exec = strings.TrimSpace(exec)
			if exec != "" {
				allowedExecutors = append(allowedExecutors, model.ExecutorType(exec))
			}
		}
		anonConfig := &server.AnonymousConfig{
			Enabled:          true,
			AllowedExecutors: allowedExecutors,
		}
		serverOpts = append(serverOpts, server.WithAnonymousConfig(anonConfig))
		logger.Info("anonymous access enabled", "allowed_executors", allowedExecutors)
	}

	// Configure worker key authentication.
	workerKeyConfig, err := server.LoadWorkerKeyConfig(*workerKeyFile)
	if err != nil {
		// Fail closed: a configured key source we cannot load would otherwise
		// silently leave worker auth open. Refuse to start instead.
		fmt.Fprintf(os.Stderr, "worker key configuration: %v\n", err)
		os.Exit(1)
	}
	if workerKeyConfig.IsEnabled() {
		serverOpts = append(serverOpts, server.WithWorkerKeyConfig(workerKeyConfig))
		logger.Info("worker key authentication enabled", "keys", len(workerKeyConfig.Keys))
	}

	// Configure file upload proxy.
	if *uploadBackend != "" {
		// Resolve S3 credentials from env if not provided
		s3AccessKey := *uploadS3AccessKey
		s3SecretKey := *uploadS3SecretKey
		if s3AccessKey == "" {
			s3AccessKey = os.Getenv("AWS_ACCESS_KEY_ID")
		}
		if s3SecretKey == "" {
			s3SecretKey = os.Getenv("AWS_SECRET_ACCESS_KEY")
		}

		// Parse allowed download directories.
		var downloadDirs []string
		if *uploadDownloadDirs != "" {
			for _, d := range strings.Split(*uploadDownloadDirs, ",") {
				d = strings.TrimSpace(d)
				if d != "" {
					downloadDirs = append(downloadDirs, d)
				}
			}
		}
		// If no download dirs specified, default to the local upload dir.
		if len(downloadDirs) == 0 && *uploadLocalDir != "" {
			downloadDirs = []string{*uploadLocalDir}
		}

		uploadCfg := &server.FileUploadConfig{
			Enabled:             true,
			Backend:             *uploadBackend,
			MaxSize:             *uploadMaxSize,
			AllowedDownloadDirs: downloadDirs,
			Shock: server.ShockUploadConfig{
				Host:    *uploadShockHost,
				UseHTTP: *uploadShockHTTP,
				Token:   *uploadShockToken,
			},
			S3: server.S3UploadConfig{
				Endpoint:        *uploadS3Endpoint,
				Region:          *uploadS3Region,
				Bucket:          *uploadS3Bucket,
				Prefix:          *uploadS3Prefix,
				AccessKeyID:     s3AccessKey,
				SecretAccessKey: s3SecretKey,
				UsePathStyle:    *uploadS3PathStyle,
				DisableSSL:      *uploadS3DisableSSL,
			},
			Local: server.LocalUploadConfig{
				Dir: *uploadLocalDir,
			},
		}
		serverOpts = append(serverOpts, server.WithFileUploadConfig(uploadCfg))
		logger.Info("file upload proxy enabled", "backend", *uploadBackend)
	}

	// Set up BV-BRC callers.
	var defaultBVBRCCaller bvbrc.RPCCaller
	if tok, err := bvbrc.ResolveToken(); err == nil {
		tokenInfo := bvbrc.ParseToken(tok)
		if tokenInfo.IsExpired() {
			logger.Warn("BV-BRC token expired; server token not available")
		} else {
			// AppService caller for /apps listing (read-only, service account).
			bvbrcCfg := bvbrc.DefaultClientConfig()
			bvbrcCfg.Token = tok
			defaultBVBRCCaller = bvbrc.NewHTTPRPCCaller(bvbrcCfg, logger)
			serverOpts = append(serverOpts, server.WithBVBRCCaller(defaultBVBRCCaller))

			logger.Info("bvbrc service account ready", "username", tokenInfo.Username)
		}
	} else {
		logger.Info("bvbrc service account not available (no token)", "hint", "set BVBRC_TOKEN or run gowe login")
	}

	// Register BVBRCExecutor (uses per-task tokens for job submission).
	reg.Register(executor.NewBVBRCExecutor(bvbrc.DefaultAppServiceURL, defaultBVBRCCaller, logger))

	// Create scheduler with configurable poll interval and default executor.
	schedCfg := scheduler.DefaultConfig()
	schedCfg.PollInterval = *schedulerPoll
	schedCfg.DefaultExecutor = cfg.DefaultExecutor
	cfg.ForceExecutor = *forceExecutor
	schedCfg.ForceExecutor = cfg.ForceExecutor
	schedCfg.WorkspaceStaging = *workspaceStaging
	schedCfg.PreflightDeferralTicks = *preflightDeferral
	schedCfg.StuckTaskThreshold = *stuckTaskThreshold
	schedCfg.StuckTaskAction = *stuckTaskAction
	for _, g := range strings.Split(*tokenInjectGroups, ",") {
		if g = strings.TrimSpace(g); g != "" {
			schedCfg.TokenInjectGroups = append(schedCfg.TokenInjectGroups, g)
		}
	}
	if len(schedCfg.TokenInjectGroups) > 0 {
		logger.Info("token auto-injection enabled for worker groups", "groups", schedCfg.TokenInjectGroups)
	}
	sched := scheduler.NewLoop(st, reg, schedCfg, logger)
	sched.SetMetrics(metricsReg)

	// One Workspace stager serves both the scheduler (server-side pre/post
	// staging, only in "server" mode) and the admin output verification /
	// re-delivery endpoints (always: they act with each submission's stored
	// token, so verifying a single submission works in any staging mode).
	wsStager := staging.NewWorkspaceStager(staging.WorkspaceConfig{
		WorkspaceURL: *wsStagingURL,
		Timeout:      5 * time.Minute,
		MaxRetries:   3,
	}, logger)
	serverOpts = append(serverOpts, server.WithWorkspaceStager(wsStager))
	if *redeliverSourceDirs != "" {
		dirs := strings.Split(*redeliverSourceDirs, ",")
		serverOpts = append(serverOpts, server.WithRedeliverSourceDirs(dirs))
		logger.Info("admin re-delivery source directories", "dirs", dirs)
	}
	if *workspaceStaging == "server" {
		sched.SetWorkspaceStager(wsStager)
		logger.Info("server-side workspace staging enabled")
	}

	srv := server.New(cfg, st, sched, logger, serverOpts...)

	// When the client reaches us over HTTPS (native TLS or via a TLS-terminating
	// proxy), tell browsers to pin HTTPS for future visits.
	handler := srv.Handler()
	if cfg.TLSEnabled() || cfg.BehindProxy {
		handler = withHSTS(handler)
	}

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,
		// Explicit TLS floor. Go's server default is already TLS 1.2, but pin it
		// so a GODEBUG or future default change can't silently regress it.
		TLSConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		// Bound header reads so a slow client can't hold a connection open
		// indefinitely (Slowloris).
		ReadHeaderTimeout: 30 * time.Second,
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start scheduler and worker reaper in background.
	srv.StartScheduler(ctx)
	srv.StartWorkerReaper(ctx)

	// Second, unauthenticated listener exposing ONLY /metrics — deliberately
	// separate from the main API/UI server (no auth middleware, no request
	// logging, no other routes) so a Prometheus scrape target never shares
	// the main server's auth surface. Bind to localhost or a private
	// interface; this endpoint has no access control of its own.
	var metricsServer *http.Server
	if metricsReg != nil {
		metricsMux := http.NewServeMux()
		metricsMux.Handle("/metrics", promhttp.HandlerFor(metricsReg.Gatherer(), promhttp.HandlerOpts{}))
		metricsServer = &http.Server{
			Addr:              *metricsAddr,
			Handler:           metricsMux,
			ReadHeaderTimeout: 30 * time.Second,
		}
		go func() {
			logger.Info("metrics server starting", "addr", *metricsAddr)
			if err := metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Error("metrics server failed", "error", err)
			}
		}()
	}

	go func() {
		if cfg.TLSEnabled() {
			logger.Info("server starting", "addr", cfg.Addr, "scheme", "https", "tls_cert", cfg.TLSCertFile)
			if err := httpServer.ListenAndServeTLS(cfg.TLSCertFile, cfg.TLSKeyFile); err != nil && err != http.ErrServerClosed {
				logger.Error("server failed", "error", err)
				os.Exit(1)
			}
			return
		}
		scheme := "http"
		if cfg.BehindProxy {
			scheme = "http (behind TLS-terminating proxy)"
		}
		logger.Info("server starting", "addr", cfg.Addr, "scheme", scheme)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	// Stop scheduler before HTTP server.
	if err := sched.Stop(); err != nil {
		logger.Error("scheduler stop error", "error", err)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if metricsServer != nil {
		if err := metricsServer.Shutdown(shutdownCtx); err != nil {
			logger.Error("metrics server shutdown error", "error", err)
		}
	}

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown error: %v\n", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}

// withHSTS wraps a handler to emit a Strict-Transport-Security header, telling
// browsers to use HTTPS for future visits. Only applied when the client reaches
// the server over TLS (native or via a TLS-terminating proxy).
func withHSTS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		next.ServeHTTP(w, r)
	})
}

// loadTokenCipher builds the at-rest token cipher from --token-key-file (if set)
// or the GOWE_TOKEN_KEY environment variable. Returns (nil, nil) when neither is
// configured, so the caller can decide the no-key policy. A present-but-invalid
// key is a hard error.
func loadTokenCipher(keyFile string) (*tokencrypt.Cipher, error) {
	if keyFile != "" {
		data, err := os.ReadFile(keyFile)
		if err != nil {
			return nil, fmt.Errorf("read token key file: %w", err)
		}
		key, err := tokencrypt.DecodeKey(string(data))
		if err != nil {
			return nil, err
		}
		return tokencrypt.New(key)
	}
	return tokencrypt.FromEnv()
}
