package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/logging"
	"github.com/me/gowe/internal/scheduler"
	"github.com/me/gowe/internal/server"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

func main() {
	cfg := config.DefaultServerConfig()

	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "Listen address")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level (debug, info, warn, error)")
	flag.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log format (text, json)")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Database path (default ~/.gowe/gowe.db)")
	flag.StringVar(&cfg.DefaultExecutor, "default-executor", cfg.DefaultExecutor, "Default executor type: local, docker, worker (empty for hint-based)")
	debug := flag.Bool("debug", false, "Shorthand for --log-level=debug")

	// Authentication options
	allowAnonymous := flag.Bool("allow-anonymous", false, "Allow unauthenticated access as anonymous user")
	anonymousExecutors := flag.String("anonymous-executors", "local,docker,worker", "Comma-separated list of executors allowed for anonymous users")
	configFile := flag.String("config", "", "Path to server config file (for admins, worker keys)")
	workerKeyFile := flag.String("worker-keys", "", "Path to worker keys JSON file")

	flag.Parse()

	if *debug {
		cfg.LogLevel = "debug"
	}

	logger := logging.NewLogger(logging.ParseLevel(cfg.LogLevel), cfg.LogFormat)

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

	// Create executor registry and register executors.
	reg := executor.NewRegistry(logger)
	reg.Register(executor.NewLocalExecutor("", logger))
	reg.Register(executor.NewDockerExecutor("", logger))
	reg.Register(executor.NewWorkerExecutor(st, logger))

	// Register BVBRCExecutor and create RPC callers if a token is available.
	const workspaceURL = "https://p3.theseed.org/services/Workspace"

	serverOpts := []server.Option{server.WithExecutorRegistry(reg)}

	// Configure admin role assignment.
	adminConfig := server.NewAdminConfig(st, "GOWE_ADMINS", *configFile)
	serverOpts = append(serverOpts, server.WithAdminConfig(adminConfig))
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
	workerKeyConfig := server.LoadWorkerKeyConfig(*workerKeyFile)
	if workerKeyConfig.IsEnabled() {
		serverOpts = append(serverOpts, server.WithWorkerKeyConfig(workerKeyConfig))
		logger.Info("worker key authentication enabled", "keys", len(workerKeyConfig.Keys))
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

			// Workspace caller for workspace browsing.
			wsCfg := bvbrc.ClientConfig{AppServiceURL: workspaceURL, Token: tok}
			wsCaller := bvbrc.NewHTTPRPCCaller(wsCfg, logger)
			serverOpts = append(serverOpts, server.WithWorkspaceCaller(wsCaller))

			logger.Info("bvbrc service account ready", "username", tokenInfo.Username)
		}
	} else {
		logger.Info("bvbrc service account not available (no token)", "hint", "set BVBRC_TOKEN or run gowe login")
	}

	// Register BVBRCExecutor (uses per-task tokens for job submission).
	reg.Register(executor.NewBVBRCExecutor(bvbrc.DefaultAppServiceURL, defaultBVBRCCaller, logger))

	// Create scheduler.
	sched := scheduler.NewLoop(st, reg, scheduler.DefaultConfig(), logger)

	srv := server.New(cfg, st, sched, logger, serverOpts...)

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.Handler(),
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start scheduler in background.
	srv.StartScheduler(ctx)

	go func() {
		logger.Info("server starting", "addr", cfg.Addr)
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

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown error: %v\n", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}
