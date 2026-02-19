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

	// Server connection flags.
	flag.StringVar(&cfg.ServerURL, "server", "http://localhost:8080", "GoWe server URL")
	flag.StringVar(&cfg.Name, "name", "", "Worker name (default: hostname)")
	flag.StringVar(&cfg.Runtime, "runtime", "none", "Container runtime (docker, apptainer, none)")
	flag.StringVar(&cfg.WorkDir, "workdir", "", "Local working directory (default: $TMPDIR/gowe-worker)")
	flag.StringVar(&cfg.StageOut, "stage-out", "local", "Output staging mode (local, file:///path, http://upload.example.com)")
	flag.DurationVar(&cfg.Poll, "poll", 5*time.Second, "Poll interval")

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

	// Logging flags.
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

	// Build stager config.
	cfg.Stager = worker.DefaultStagerConfig()
	cfg.Stager.StageOutMode = cfg.StageOut
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
