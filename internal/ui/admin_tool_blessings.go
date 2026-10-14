package ui

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/me/gowe/internal/blessing"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/pkg/model"
)

// Admin page for tool blessings. It mirrors the REST handlers in
// internal/server/handler_tool_blessings.go and shares their logic through
// internal/blessing. Nothing enforces blessings yet.

// blessingInspectView is the review report rendered above the bless form.
type blessingInspectView struct {
	WorkflowRef string
	ToolID      string
	ToolHash    string
	ImageRef    string
	Requests    bool
	Summary     model.ToolSummary
	Warnings    []string
	Active      *model.ToolBlessing
}

func splitList(s string, seps string) []string {
	var out []string
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(seps, r) }) {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (ui *UI) blessingsRedirect(w http.ResponseWriter, r *http.Request, key, msg string) {
	qv := url.Values{}
	qv.Set(key, msg)
	http.Redirect(w, r, ui.href("/admin/tool-blessings?"+qv.Encode()), http.StatusSeeOther)
}

// renderBlessings renders the page; inspect may be nil.
func (ui *UI) renderBlessings(w http.ResponseWriter, r *http.Request, inspect *blessingInspectView, formErr string) {
	showAll := r.URL.Query().Get("all") == "1"
	list, err := ui.store.ListToolBlessings(r.Context(), showAll)
	if err != nil {
		ui.renderError(w, "Failed to load tool blessings", err)
		return
	}
	pending, err := blessing.Pending(r.Context(), ui.store)
	if err != nil {
		ui.renderError(w, "Failed to scan registered tools", err)
		return
	}
	data := map[string]any{
		"Title":      "Tool Blessings - GoWe",
		"Session":    SessionFromContext(r.Context()),
		"Blessings":  list,
		"Pending":    pending,
		"ShowAll":    showAll,
		"Inspect":    inspect,
		"Flash":      r.URL.Query().Get("msg"),
		"FlashError": r.URL.Query().Get("error"),
		"FormError":  formErr,
	}
	ui.render(w, "admin/blessings", data)
}

// HandleAdminToolBlessings lists blessings and registered tools awaiting one.
func (ui *UI) HandleAdminToolBlessings(w http.ResponseWriter, r *http.Request) {
	ui.renderBlessings(w, r, nil, "")
}

// HandleAdminToolBlessingInspect shows the review report for a registered tool.
func (ui *UI) HandleAdminToolBlessingInspect(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ref := strings.TrimSpace(r.FormValue("workflow"))
	toolID := strings.TrimSpace(r.FormValue("tool_id"))
	if ref == "" {
		ui.renderBlessings(w, r, nil, "workflow is required")
		return
	}
	t, be := blessing.TargetFromWorkflow(r.Context(), ui.store, ref, toolID)
	if be != nil {
		ui.renderBlessings(w, r, nil, be.Message)
		return
	}
	existing, err := ui.store.GetActiveToolBlessingByHash(r.Context(), t.Result.Hash)
	if err != nil {
		ui.renderError(w, "Failed to look up blessing", err)
		return
	}
	v := &blessingInspectView{
		WorkflowRef: ref,
		ToolID:      t.Result.ToolID,
		ToolHash:    t.Result.Hash,
		ImageRef:    t.Result.ImageRef,
		Requests:    toolhash.ToolRequestsToken(t.Result.Tool),
		Summary:     toolhash.Summarize(t.Result.Tool),
		Active:      existing,
	}
	if blessing.HasExecutionUnderRequirements(t.Result.Tool) {
		v.Warnings = append(v.Warnings, "gowe:Execution is declared under requirements; only hints are honoured. Move it to hints before blessing.")
	}
	if t.Result.ImageRef == "" {
		v.Warnings = append(v.Warnings, "the tool declares no DockerRequirement.dockerPull; a blessing needs a pinned image")
	}
	ui.renderBlessings(w, r, v, "")
}

// HandleAdminToolBlessingCreate blesses a tool from the inspect form. The hash
// is recomputed from the registered CWL; a stale tool_hash is refused. The
// reviewer must tick the acknowledgement box.
func (ui *UI) HandleAdminToolBlessingCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	ref := strings.TrimSpace(r.FormValue("workflow"))
	toolID := strings.TrimSpace(r.FormValue("tool_id"))
	suppliedHash := strings.TrimSpace(r.FormValue("tool_hash"))
	if r.FormValue("ack") == "" {
		ui.blessingsRedirect(w, r, "error", "tick the acknowledgement: you must confirm you read the scripts and review flags")
		return
	}
	t, be := blessing.TargetFromWorkflow(r.Context(), ui.store, ref, toolID)
	if be != nil {
		ui.blessingsRedirect(w, r, "error", be.Message)
		return
	}
	if suppliedHash != "" && suppliedHash != t.Result.Hash {
		ui.blessingsRedirect(w, r, "error", "the registered tool changed since it was inspected (hash mismatch); inspect it again")
		return
	}
	var by string
	if sess := SessionFromContext(r.Context()); sess != nil {
		by = sess.Username
	}
	b, be := blessing.Create(r.Context(), ui.store, blessing.CreateParams{
		Target:       t,
		EnvNames:     splitList(r.FormValue("env_names"), ", \n\t"),
		WorkerGroups: splitList(r.FormValue("worker_groups"), ", \n\t"),
		ImageSHA256s: splitList(r.FormValue("image_sha256s"), ", \n\t"),
		Note:         strings.TrimSpace(r.FormValue("note")),
		BlessedBy:    by,
	})
	if be != nil {
		ui.blessingsRedirect(w, r, "error", be.Message)
		return
	}
	ui.metrics.IncToolBlessingEvent("create")
	ui.logger.Warn("AUDIT tool blessing created", "audit", "tool_blessing", "action", "create", "via", "ui",
		"blessing_id", b.ID, "tool_hash", b.ToolHash, "tool", b.ToolName, "image_ref", b.ImageRef,
		"image_sha256s", b.ImageSHA256s, "blessed_by", by, "review_flags", len(b.Summary.Flags))
	ui.blessingsRedirect(w, r, "msg", "Blessed "+b.ToolName+" as "+b.ID)
}

// HandleAdminToolBlessingRevoke revokes a blessing (the row is kept).
func (ui *UI) HandleAdminToolBlessingRevoke(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	id := ui.pathParam(r, "id")
	reason := strings.TrimSpace(r.FormValue("reason"))
	if reason == "" {
		ui.blessingsRedirect(w, r, "error", "a revocation reason is required")
		return
	}
	var by string
	if sess := SessionFromContext(r.Context()); sess != nil {
		by = sess.Username
	}
	if err := ui.store.RevokeToolBlessing(r.Context(), id, by, reason); err != nil {
		ui.blessingsRedirect(w, r, "error", blessingStoreMsg(err))
		return
	}
	ui.metrics.IncToolBlessingEvent("revoke")
	ui.logger.Warn("AUDIT tool blessing revoked", "audit", "tool_blessing", "action", "revoke", "via", "ui",
		"blessing_id", id, "revoked_by", by, "reason", reason)
	ui.blessingsRedirect(w, r, "msg", "Revoked "+id)
}

// HandleAdminToolBlessingAddImage adds an allowed image digest to a blessing.
func (ui *UI) HandleAdminToolBlessingAddImage(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	id := ui.pathParam(r, "id")
	sha := strings.TrimSpace(r.FormValue("image_sha256"))
	if !model.IsValidSHA256(sha) {
		ui.blessingsRedirect(w, r, "error", "image sha256 must be a 64-character hex digest")
		return
	}
	if err := ui.store.AddToolBlessingImageSHA256(r.Context(), id, sha); err != nil {
		ui.blessingsRedirect(w, r, "error", blessingStoreMsg(err))
		return
	}
	var by string
	if sess := SessionFromContext(r.Context()); sess != nil {
		by = sess.Username
	}
	ui.metrics.IncToolBlessingEvent("add_image")
	ui.logger.Warn("AUDIT tool blessing image added", "audit", "tool_blessing", "action", "add_image", "via", "ui",
		"blessing_id", id, "image_sha256", model.NormalizeSHA256(sha), "by", by)
	ui.blessingsRedirect(w, r, "msg", "Added image digest to "+id)
}

func blessingStoreMsg(err error) string {
	switch {
	case errors.Is(err, store.ErrBlessingNotFound):
		return "blessing not found"
	case errors.Is(err, store.ErrBlessingRevoked):
		return "blessing is revoked"
	default:
		return err.Error()
	}
}
