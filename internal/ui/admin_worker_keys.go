package ui

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

// HandleAdminWorkerKeys renders the worker-key management page: every issued
// key (hashes and raw secrets are never exposed — model.WorkerKey.KeyHash is
// json:"-" and the raw value is shown only once at issuance).
func (ui *UI) HandleAdminWorkerKeys(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	keys, err := ui.store.ListWorkerKeys(r.Context())
	if err != nil {
		ui.renderError(w, "Failed to load worker keys", err)
		return
	}

	data := map[string]any{
		"Title":   "Worker Keys - GoWe",
		"Session": sess,
		"Keys":    keys,
	}
	ui.render(w, "admin/keys", data)
}

// HandleAdminWorkerKeyCreate mints a new worker key, mirroring the store
// operations behind the API's handleCreateWorkerKey (internal/server/handler_worker_keys.go):
// model.GenerateWorkerKey followed by store.CreateWorkerKey. The raw secret
// is only ever available in this one response, so — unlike the label-create
// flow — success renders the page directly (with the secret embedded)
// instead of redirecting, which would either drop it or leak it into a URL.
func (ui *UI) HandleAdminWorkerKeyCreate(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}

	label := strings.TrimSpace(r.FormValue("label"))
	description := strings.TrimSpace(r.FormValue("description"))

	var groups []string
	for _, g := range strings.Split(r.FormValue("groups"), ",") {
		g = strings.TrimSpace(g)
		if g != "" {
			groups = append(groups, g)
		}
	}
	// An empty groups list means "any group" to the scheduler (see
	// CanJoinGroup), which would silently grant a key fleet-wide reach.
	// Default to the least-privilege "default" group, same as the API.
	if len(groups) == 0 {
		groups = []string{"default"}
	}

	var expiresAt *time.Time
	if raw := strings.TrimSpace(r.FormValue("expires_at")); raw != "" {
		t, err := time.Parse("2006-01-02", raw)
		if err != nil {
			qv := url.Values{}
			qv.Set("error", "invalid expiry date")
			http.Redirect(w, r, "/admin/worker-keys?"+qv.Encode(), http.StatusSeeOther)
			return
		}
		if !t.After(time.Now()) {
			qv := url.Values{}
			qv.Set("error", "expiry must be in the future")
			http.Redirect(w, r, "/admin/worker-keys?"+qv.Encode(), http.StatusSeeOther)
			return
		}
		expiresAt = &t
	}

	raw, hash, prefix, err := model.GenerateWorkerKey()
	if err != nil {
		ui.renderError(w, "Failed to generate worker key", err)
		return
	}

	var createdBy string
	if sess != nil {
		createdBy = sess.Username
	}

	key := &model.WorkerKey{
		ID:          "wk_" + uuid.New().String(),
		Label:       label,
		KeyHash:     hash,
		KeyPrefix:   prefix,
		Groups:      groups,
		Description: description,
		CreatedBy:   createdBy,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   expiresAt,
	}

	if err := ui.store.CreateWorkerKey(r.Context(), key); err != nil {
		ui.renderError(w, "Failed to create worker key", err)
		return
	}

	ui.logger.Info("worker key issued via UI", "key_id", key.ID, "label", key.Label, "groups", key.Groups, "created_by", createdBy)

	keys, err := ui.store.ListWorkerKeys(r.Context())
	if err != nil {
		ui.renderError(w, "Failed to load worker keys", err)
		return
	}

	data := map[string]any{
		"Title":   "Worker Keys - GoWe",
		"Session": sess,
		"Keys":    keys,
		"RawKey":  raw,
		"NewID":   key.ID,
	}
	ui.render(w, "admin/keys", data)
}

// HandleAdminWorkerKeyRevoke permanently revokes (deletes) a worker key
// (HTMX), mirroring the store operation behind the API's
// handleRevokeWorkerKey.
func (ui *UI) HandleAdminWorkerKeyRevoke(w http.ResponseWriter, r *http.Request) {
	id := ui.pathParam(r, "id")

	if err := ui.store.DeleteWorkerKey(r.Context(), id); err != nil {
		w.Header().Set("HX-Reswap", "none")
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	ui.logger.Info("worker key revoked via UI", "key_id", id)
	w.WriteHeader(http.StatusOK)
}
