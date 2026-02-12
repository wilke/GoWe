package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/me/gowe/internal/bvbrc"
	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/executor"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/scheduler"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/ui"
)

// Server is the GoWe REST API server.
type Server struct {
	router          chi.Router
	logger          *slog.Logger
	config          config.ServerConfig
	startTime       time.Time
	parser          *parser.Parser
	validator       *parser.Validator
	store           store.Store
	scheduler       scheduler.Scheduler
	registry        *executor.Registry // optional; used by dry-run to check executor availability
	bvbrcCaller     bvbrc.RPCCaller    // optional; AppService caller, nil when no BV-BRC token
	workspaceCaller bvbrc.RPCCaller    // optional; Workspace service caller
	testApps        []map[string]any   // optional; static app list for testing without BV-BRC
	ui              *ui.UI             // UI handler for web interface
}

// Option configures optional Server dependencies.
type Option func(*Server)

// WithBVBRCCaller sets the BV-BRC RPC caller used by /apps endpoints.
func WithBVBRCCaller(caller bvbrc.RPCCaller) Option {
	return func(s *Server) {
		s.bvbrcCaller = caller
	}
}

// WithExecutorRegistry sets the executor registry for dry-run validation.
func WithExecutorRegistry(reg *executor.Registry) Option {
	return func(s *Server) {
		s.registry = reg
	}
}

// WithWorkspaceCaller sets the BV-BRC Workspace service caller.
func WithWorkspaceCaller(caller bvbrc.RPCCaller) Option {
	return func(s *Server) {
		s.workspaceCaller = caller
	}
}

// WithTestApps injects a static app list for smoke-testing without BV-BRC connectivity.
func WithTestApps(apps []map[string]any) Option {
	return func(s *Server) {
		s.testApps = apps
	}
}

// New creates a new Server with all routes registered.
// sched may be nil if no scheduling is desired (e.g. in tests).
func New(cfg config.ServerConfig, st store.Store, sched scheduler.Scheduler, logger *slog.Logger, opts ...Option) *Server {
	s := &Server{
		router:    chi.NewRouter(),
		logger:    logger.With("component", "server"),
		config:    cfg,
		startTime: time.Now(),
		parser:    parser.New(logger),
		validator: parser.NewValidator(logger),
		store:     st,
		scheduler: sched,
	}
	for _, opt := range opts {
		opt(s)
	}

	// Create UI handler.
	s.ui = ui.New(st, logger, ui.Config{
		Secure: false, // TODO: Make configurable based on TLS
	})
	if s.bvbrcCaller != nil {
		s.ui.WithBVBRCCaller(s.bvbrcCaller)
	}
	if s.workspaceCaller != nil {
		s.ui.WithWorkspaceCaller(s.workspaceCaller)
	}

	s.routes()
	return s
}

// StartScheduler begins the scheduling loop in a background goroutine.
func (s *Server) StartScheduler(ctx context.Context) {
	if s.scheduler == nil {
		return
	}
	go func() {
		if err := s.scheduler.Start(ctx); err != nil && err != context.Canceled {
			s.logger.Error("scheduler stopped", "error", err)
		}
	}()
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.router.ServeHTTP(w, r)
}

// Handler returns the http.Handler for this server.
func (s *Server) Handler() http.Handler {
	return s.router
}

func (s *Server) routes() {
	r := s.router

	// Global middleware
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestIDMiddleware)
	r.Use(loggingMiddleware(s.logger))

	// Static files (JS, CSS, images)
	r.Handle("/static/*", ui.StaticHandler("ui/assets"))

	// UI routes (HTML)
	s.ui.RegisterRoutes(r)

	// API routes (JSON)
	r.Route("/api/v1", func(r chi.Router) {
		// Discovery
		r.Get("/", s.handleDiscovery)

		// Health
		r.Get("/health", s.handleHealth)

		// Workflows
		r.Route("/workflows", func(r chi.Router) {
			r.Get("/", s.handleListWorkflows)
			r.Post("/", s.handleCreateWorkflow)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", s.handleGetWorkflow)
				r.Put("/", s.handleUpdateWorkflow)
				r.Delete("/", s.handleDeleteWorkflow)
				r.Post("/validate", s.handleValidateWorkflow)
			})
		})

		// Submissions
		r.Route("/submissions", func(r chi.Router) {
			r.Get("/", s.handleListSubmissions)
			r.Post("/", s.handleCreateSubmission)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", s.handleGetSubmission)
				r.Put("/cancel", s.handleCancelSubmission)
				// Tasks nested under submissions
				r.Route("/tasks", func(r chi.Router) {
					r.Get("/", s.handleListTasks)
					r.Route("/{tid}", func(r chi.Router) {
						r.Get("/", s.handleGetTask)
						r.Get("/logs", s.handleGetTaskLogs)
					})
				})
			})
		})

		// Apps (BV-BRC proxy)
		r.Route("/apps", func(r chi.Router) {
			r.Get("/", s.handleListApps)
			r.Route("/{appID}", func(r chi.Router) {
				r.Get("/", s.handleGetApp)
				r.Get("/cwl-tool", s.handleGetAppCWLTool)
			})
		})

		// Workspace (BV-BRC proxy)
		r.Get("/workspace", s.handleListWorkspace)

		// SSE endpoints for real-time updates
		r.Route("/sse", func(r chi.Router) {
			r.Get("/submissions/{id}", s.handleSSESubmission)
		})
	})
}
