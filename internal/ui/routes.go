package ui

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

// RegisterRoutes registers all UI routes on the given router.
func (ui *UI) RegisterRoutes(r chi.Router) {
	// Public routes (no auth required).
	r.Get("/login", ui.HandleLogin)
	r.Post("/login", ui.HandleLoginPost)

	// Protected routes (auth required).
	r.Group(func(r chi.Router) {
		r.Use(ui.AuthMiddleware)

		// Dashboard
		r.Get("/", ui.HandleDashboard)
		r.Get("/logout", ui.HandleLogout)

		// Workflows
		r.Route("/workflows", func(r chi.Router) {
			r.Get("/", ui.HandleWorkflowList)
			r.Get("/new", ui.HandleWorkflowCreate)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", ui.HandleWorkflowDetail)
				r.Delete("/", ui.HandleWorkflowDelete)
			})
		})

		// Submissions
		r.Route("/submissions", func(r chi.Router) {
			r.Get("/", ui.HandleSubmissionList)
			r.Get("/new", ui.HandleSubmissionCreate)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", ui.HandleSubmissionDetail)
				r.Post("/cancel", ui.HandleSubmissionCancel)
				r.Route("/tasks/{tid}", func(r chi.Router) {
					r.Get("/logs", ui.HandleTaskLogs)
				})
			})
		})

		// Workspace
		r.Get("/workspace", ui.HandleWorkspace)

		// Admin routes (admin role required).
		r.Route("/admin", func(r chi.Router) {
			r.Use(ui.AdminMiddleware)
			r.Get("/", ui.HandleAdminStats)
			r.Get("/stats", ui.HandleAdminStats)
			r.Get("/health", ui.HandleAdminHealth)
		})
	})
}

// StaticHandler returns an http.Handler that serves static files from the given directory.
func StaticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.StripPrefix("/static/", fs)
}
