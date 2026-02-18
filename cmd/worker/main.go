package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/me/gowe/internal/logging"
	"github.com/me/gowe/internal/worker"
)

func main() {
	var cfg worker.Config

	flag.StringVar(&cfg.ServerURL, "server", "http://localhost:8080", "GoWe server URL")
	flag.StringVar(&cfg.Name, "name", "", "Worker name (default: hostname)")
	flag.StringVar(&cfg.Runtime, "runtime", "none", "Container runtime (docker, apptainer, none)")
	flag.StringVar(&cfg.WorkDir, "workdir", "", "Local working directory (default: $TMPDIR/gowe-worker)")
	flag.StringVar(&cfg.StageOut, "stage-out", "local", "Output staging mode (local, file:///path)")
	flag.DurationVar(&cfg.Poll, "poll", 5*time.Second, "Poll interval")
	logLevel := flag.String("log-level", "info", "Log level (debug, info, warn, error)")
	logFormat := flag.String("log-format", "text", "Log format (text, json)")
	debug := flag.Bool("debug", false, "Shorthand for --log-level=debug")
	flag.Parse()

	if *debug {
		*logLevel = "debug"
	}

	logger := logging.NewLogger(logging.ParseLevel(*logLevel), *logFormat)

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
	)

	if err := w.Run(ctx, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "worker error: %v\n", err)
		os.Exit(1)
	}

	logger.Info("worker stopped")
}
