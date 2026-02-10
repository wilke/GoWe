package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/logging"
	"github.com/me/gowe/internal/server"
)

func main() {
	cfg := config.DefaultServerConfig()

	flag.StringVar(&cfg.Addr, "addr", cfg.Addr, "Listen address")
	flag.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "Log level (debug, info, warn, error)")
	flag.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "Log format (text, json)")
	debug := flag.Bool("debug", false, "Shorthand for --log-level=debug")
	flag.Parse()

	if *debug {
		cfg.LogLevel = "debug"
	}

	logger := logging.NewLogger(logging.ParseLevel(cfg.LogLevel), cfg.LogFormat)
	srv := server.New(cfg, logger)

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: srv.Handler(),
	}

	// Graceful shutdown
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() {
		logger.Info("server starting", "addr", cfg.Addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	logger.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		fmt.Fprintf(os.Stderr, "shutdown error: %v\n", err)
		os.Exit(1)
	}
	logger.Info("server stopped")
}
