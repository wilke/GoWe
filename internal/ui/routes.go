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

	// Every authenticated route refuses a state-changing request sent from
	// another site (SameOriginMiddleware) before anything else.

	// Worker management routes (admin required for destructive actions).
	r.Group(func(r chi.Router) {
		r.Use(ui.SameOriginMiddleware, ui.AuthMiddleware, ui.AdminMiddleware)
		r.Delete("/workers/{id}", ui.HandleWorkerDelete)
		r.Post("/workers/purge-offline", ui.HandleWorkerPurgeOffline)
	})

	// Protected routes (auth required).
	r.Group(func(r chi.Router) {
		r.Use(ui.SameOriginMiddleware, ui.AuthMiddleware)

		// Dashboard
		r.Get("/", ui.HandleDashboard)

		// Workers (view only, auth required)
		r.Get("/workers", ui.HandleWorkerList)
		r.Get("/logout", ui.HandleLogout)

		// Workflows
		r.Route("/workflows", func(r chi.Router) {
			r.Get("/", ui.HandleWorkflowList)
			r.Get("/new", ui.HandleWorkflowCreate)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", ui.HandleWorkflowDetail)
				r.Get("/edit", ui.HandleWorkflowEdit)
				r.Delete("/", ui.HandleWorkflowDelete)
			})
		})

		// Submissions
		r.Route("/submissions", func(r chi.Router) {
			r.Get("/", ui.HandleSubmissionList)
			r.Get("/new", ui.HandleSubmissionCreate)
			r.Post("/", ui.HandleSubmissionCreatePost)
			r.Get("/export", ui.HandleSubmissionExport)
			r.Route("/{id}", func(r chi.Router) {
				r.Get("/", ui.HandleSubmissionDetail)
				r.Post("/cancel", ui.HandleSubmissionCancel)
				r.Delete("/", ui.HandleSubmissionDelete)
				r.Post("/resume", ui.HandleSubmissionResume)
				r.Post("/recompute-failed", ui.HandleRecomputeFailed)
				r.Get("/timing-panel", ui.HandleSubmissionTimingPanel)
				r.Route("/tasks/{tid}", func(r chi.Router) {
					r.Get("/logs", ui.HandleTaskLogs)
					r.Post("/recompute", ui.HandleTaskRecompute)
					r.Get("/children", ui.HandleSubmissionTaskChildren)
				})
			})
		})

		// Worker groups the user manages (server admins: all). Who may see
		// or change a group is decided by the group operations (GroupAdmin),
		// not here; LiveRoleMiddleware gives them the user's current role.
		r.Route("/worker-groups", func(r chi.Router) {
			r.Use(ui.LiveRoleMiddleware)
			r.Get("/", ui.HandleWorkerGroups)
			r.Route("/{group}", func(r chi.Router) {
				r.Get("/", ui.HandleWorkerGroup)
				r.Post("/users", ui.HandleWorkerGroupUserAdd)
				r.Post("/users/remove", ui.HandleWorkerGroupUserRemove)
				r.Post("/keys", ui.HandleWorkerGroupKeyCreate)
				r.Post("/keys/revoke", ui.HandleWorkerGroupKeyRevoke)
				r.Post("/workers/remove", ui.HandleWorkerGroupWorkerRemove)
			})
		})

		// Workspace
		r.Get("/workspace", ui.HandleWorkspace)
		r.Route("/api/workspace", func(r chi.Router) {
			r.Get("/ls", ui.HandleWorkspaceAPI)
			r.Post("/upload", ui.HandleWorkspaceUpload)
		})

		// Admin routes (admin now, by the REST API's rule: AdminMiddleware).
		r.Route("/admin", func(r chi.Router) {
			r.Use(ui.AdminMiddleware)
			r.Get("/", ui.HandleAdminStats)
			r.Get("/stats", ui.HandleAdminStats)
			r.Get("/health", ui.HandleAdminHealth)
			r.Get("/tasks", ui.HandleAdminActiveTasks)
			r.Get("/labels", ui.HandleAdminLabels)
			r.Post("/labels", ui.HandleAdminLabelCreate)
			r.Delete("/labels/{id}", ui.HandleAdminLabelDelete)

			// Worker fleet (full listing, unpaginated, admin-only).
			r.Get("/fleet", ui.HandleAdminFleet)

			// Worker keys.
			r.Get("/worker-keys", ui.HandleAdminWorkerKeys)
			r.Post("/worker-keys", ui.HandleAdminWorkerKeyCreate)
			r.Delete("/worker-keys/{id}", ui.HandleAdminWorkerKeyRevoke)

			// Worker group access policy (rules, default_deny).
			r.Get("/worker-groups", ui.HandleAdminWorkerGroups)
			r.Post("/worker-groups", ui.HandleAdminWorkerGroupPut)
			r.Post("/worker-groups/delete", ui.HandleAdminWorkerGroupDelete)
			r.Post("/worker-groups/default-deny", ui.HandleAdminWorkerGroupsDefaultDeny)

			// Tool blessings (recorded, not yet enforced).
			r.Get("/tool-blessings", ui.HandleAdminToolBlessings)
			r.Post("/tool-blessings", ui.HandleAdminToolBlessingCreate)
			r.Post("/tool-blessings/inspect", ui.HandleAdminToolBlessingInspect)
			r.Post("/tool-blessings/{id}/images", ui.HandleAdminToolBlessingAddImage)
			r.Post("/tool-blessings/{id}/revoke", ui.HandleAdminToolBlessingRevoke)

			// Output verification / redelivery.
			r.Get("/outputs", ui.HandleAdminOutputs)
			r.Post("/outputs/verify", ui.HandleAdminOutputsVerify)
			r.Post("/outputs/redeliver", ui.HandleAdminOutputsRedeliver)
		})
	})
}

// StaticHandler returns an http.Handler that serves static files from the given directory.
func StaticHandler(dir string) http.Handler {
	fs := http.FileServer(http.Dir(dir))
	return http.StripPrefix("/static/", fs)
}
