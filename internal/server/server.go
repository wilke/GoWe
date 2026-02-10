package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/me/gowe/internal/config"
	"github.com/me/gowe/internal/parser"
	"github.com/me/gowe/internal/store"
)

// Server is the GoWe REST API server.
type Server struct {
	router    chi.Router
	logger    *slog.Logger
	config    config.ServerConfig
	startTime time.Time
	parser    *parser.Parser
	validator *parser.Validator
	store     store.Store
}

// New creates a new Server with all routes registered.
func New(cfg config.ServerConfig, st store.Store, logger *slog.Logger) *Server {
	s := &Server{
		router:    chi.NewRouter(),
		logger:    logger.With("component", "server"),
		config:    cfg,
		startTime: time.Now(),
		parser:    parser.New(logger),
		validator: parser.NewValidator(logger),
		store:     st,
	}
	s.routes()
	return s
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
	})
}
