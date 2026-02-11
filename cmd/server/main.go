package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/logging"
	"github.com/me/gowe/internal/scheduler"
	"github.com/me/gowe/internal/server"
	"github.com/me/gowe/internal/store"
)

func main() {
	cfg := config.DefaultServerConfig()

	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "Listen address")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level (debug, info, warn, error)")
	flag.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log format (text, json)")
	flag.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Database path (default ~/.gowe/gowe.db)")
	debug := flag.Bool("debug", false, "Shorthand for --log-level=debug")
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

	// Register BVBRCExecutor if a token is available.
	if tok, err := bvbrc.ResolveToken(); err == nil {
		tokenInfo := bvbrc.ParseToken(tok)
		if tokenInfo.IsExpired() {
			logger.Warn("BV-BRC token expired; bvbrc executor not registered")
		} else {
			bvbrcCfg := bvbrc.DefaultClientConfig()
			bvbrcCfg.Token = tok
			caller := bvbrc.NewHTTPRPCCaller(bvbrcCfg, logger)
			reg.Register(executor.NewBVBRCExecutor(caller, tokenInfo.Username, logger))
			logger.Info("bvbrc executor registered", "username", tokenInfo.Username)
		}
	} else {
		logger.Info("bvbrc executor not registered (no token)", "hint", "set BVBRC_TOKEN or run gowe login")
	}

	// Create scheduler.
	sched := scheduler.NewLoop(st, reg, scheduler.DefaultConfig(), logger)

	srv := server.New(cfg, st, sched, logger)

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
