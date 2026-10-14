package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/blessing"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/pkg/model"
)

// Tool blessing admin API. A blessing records that an administrator reviewed a
// specific tool (identified by toolhash.Digest) and pinned the image digests
// it may run under. NOTHING ENFORCES BLESSINGS YET: these endpoints only
// manage the records; the scheduler/submit-time checks arrive in a later change.

// toolBlessingRoutes mounts the blessing endpoints under /admin/tool-blessings.
func (s *Server) toolBlessingRoutes(r chi.Router) {
	r.Get("/", s.handleListToolBlessings)
	r.Post("/", s.handleCreateToolBlessing)
	r.Get("/pending", s.handlePendingToolBlessings)
	r.Post("/inspect", s.handleInspectToolBlessing)
	r.Route("/{id}", func(r chi.Router) {
		r.Post("/images", s.handleAddBlessingImage)
		r.Delete("/", s.handleRevokeToolBlessing)
	})
}

type inspectRequest struct {
	// Workflow is a registered workflow id or name.
	Workflow string `json:"workflow"`
	// CWL is a raw CWL document (bare tool, or packed $graph); gowe:// run references are resolved.
	CWL string `json:"cwl"`
	// ToolID selects a tool when the document holds several.
	ToolID string `json:"tool_id"`
}

type createBlessingRequest struct {
	Workflow     string   `json:"workflow"`
	ToolID       string   `json:"tool_id"`
	ToolHash     string   `json:"tool_hash"`
	EnvNames     []string `json:"env_names"`
	WorkerGroups []string `json:"worker_groups"`
	ImageSHA256s []string `json:"image_sha256s"`
	Note         string   `json:"note"`
}

type revokeBlessingRequest struct {
	Reason string `json:"reason"`
}

type addImageRequest struct {
	ImageSHA256 string `json:"image_sha256"`
}

func validationErr(w http.ResponseWriter, reqID, msg string) {
	respondError(w, reqID, http.StatusBadRequest, model.NewValidationError(msg))
}

func internalErr(w http.ResponseWriter, reqID string, err error) {
	respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
}

func blessingErr(w http.ResponseWriter, reqID string, e *blessing.Error) {
	code := model.ErrValidation
	switch e.Status {
	case http.StatusNotFound:
		code = model.ErrNotFound
	case http.StatusConflict:
		code = model.ErrConflict
	case http.StatusInternalServerError:
		code = model.ErrInternal
	}
	respondError(w, reqID, e.Status, &model.APIError{Code: code, Message: e.Message})
}

func adminName(r *http.Request) string {
	if uc := UserFromContext(r.Context()); uc != nil && uc.User != nil {
		return uc.User.Username
	}
	return ""
}

// handleInspectToolBlessing computes the hash and review summary of a tool
// without blessing it.
// POST /api/v1/admin/tool-blessings/inspect
func (s *Server) handleInspectToolBlessing(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	var req inspectRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	var t *blessing.Target
	var be *blessing.Error
	switch {
	case req.Workflow != "" && req.CWL != "":
		validationErr(w, reqID, "give either workflow or cwl, not both")
		return
	case req.Workflow != "":
		t, be = blessing.TargetFromWorkflow(r.Context(), s.store, req.Workflow, req.ToolID)
	case req.CWL != "":
		resolved, err := resolveGoweRefs(r.Context(), s.store, req.CWL)
		if err != nil {
			validationErr(w, reqID, "resolve gowe:// references: "+err.Error())
			return
		}
		t, be = blessing.TargetFromRaw(resolved, req.ToolID)
	default:
		validationErr(w, reqID, "workflow or cwl is required")
		return
	}
	if be != nil {
		blessingErr(w, reqID, be)
		return
	}
	existing, err := s.store.GetActiveToolBlessingByHash(r.Context(), t.Result.Hash)
	if err != nil {
		internalErr(w, reqID, err)
		return
	}
	summary := toolhash.Summarize(t.Result.Tool)
	warnings := []string{}
	if blessing.HasExecutionUnderRequirements(t.Result.Tool) {
		warnings = append(warnings, "gowe:Execution is declared under requirements; only hints are honoured. Move it to hints before blessing.")
	}
	if t.Result.ImageRef == "" {
		warnings = append(warnings, "the tool declares no DockerRequirement.dockerPull; a blessing needs a pinned image")
	}
	respondOK(w, reqID, map[string]any{
		"tool_id":         t.Result.ToolID,
		"tool_hash":       t.Result.Hash,
		"hash_version":    toolhash.Version,
		"image_ref":       t.Result.ImageRef,
		"requests_token":  toolhash.ToolRequestsToken(t.Result.Tool),
		"workflow_id":     t.WorkflowID,
		"workflow_name":   t.WorkflowName,
		"summary":         summary,
		"warnings":        warnings,
		"active_blessing": existing,
	})
}

// handleListToolBlessings lists blessings; ?include_revoked=true adds revoked ones.
// GET /api/v1/admin/tool-blessings
func (s *Server) handleListToolBlessings(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	inc := r.URL.Query().Get("include_revoked") == "true" || r.URL.Query().Get("all") == "true"
	list, err := s.store.ListToolBlessings(r.Context(), inc)
	if err != nil {
		internalErr(w, reqID, err)
		return
	}
	respondOK(w, reqID, map[string]any{"total": len(list), "blessings": list})
}

// handlePendingToolBlessings lists registered tools that request the token but
// have no active blessing.
// GET /api/v1/admin/tool-blessings/pending
func (s *Server) handlePendingToolBlessings(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	out, err := blessing.Pending(r.Context(), s.store)
	if err != nil {
		internalErr(w, reqID, err)
		return
	}
	respondOK(w, reqID, map[string]any{"total": len(out), "pending": out})
}

// handleCreateToolBlessing blesses a tool. The server recomputes the hash from
// the registered CWL and refuses a mismatching supplied tool_hash.
// POST /api/v1/admin/tool-blessings
func (s *Server) handleCreateToolBlessing(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	var req createBlessingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	// Fail on a missing pin before any (comparatively expensive) lookup.
	if _, be := blessing.ParseDigests(req.ImageSHA256s); be != nil {
		blessingErr(w, reqID, be)
		return
	}

	var target *blessing.Target
	switch {
	case req.Workflow != "":
		t, be := blessing.TargetFromWorkflow(r.Context(), s.store, req.Workflow, req.ToolID)
		if be != nil {
			blessingErr(w, reqID, be)
			return
		}
		if req.ToolHash != "" && req.ToolHash != t.Result.Hash {
			validationErr(w, reqID, fmt.Sprintf("tool_hash mismatch: the registered tool hashes to %s, not %s (re-inspect before blessing)", t.Result.Hash, req.ToolHash))
			return
		}
		target = t
	case req.ToolHash != "":
		t, err := blessing.FindToolByHash(r.Context(), s.store, req.ToolHash)
		if err != nil {
			internalErr(w, reqID, err)
			return
		}
		if t == nil {
			respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("registered tool with hash", req.ToolHash))
			return
		}
		target = t
	default:
		validationErr(w, reqID, "workflow (with optional tool_id) or tool_hash is required")
		return
	}

	blessedBy := adminName(r)
	b, be := blessing.Create(r.Context(), s.store, blessing.CreateParams{
		Target: target, EnvNames: req.EnvNames, WorkerGroups: req.WorkerGroups,
		ImageSHA256s: req.ImageSHA256s, Note: req.Note, BlessedBy: blessedBy,
	})
	if be != nil {
		blessingErr(w, reqID, be)
		return
	}
	s.metrics.IncToolBlessingEvent("create")
	s.logger.Warn("AUDIT tool blessing created", "audit", "tool_blessing", "action", "create",
		"blessing_id", b.ID, "tool_hash", b.ToolHash, "tool", b.ToolName, "image_ref", b.ImageRef,
		"image_sha256s", b.ImageSHA256s, "env_names", b.EnvNames, "worker_groups", b.WorkerGroups,
		"blessed_by", blessedBy, "review_flags", len(b.Summary.Flags))
	respondCreated(w, reqID, b)
}

// blessingFromPath resolves {id} (a blessing id, or an active tool hash
// "sha256:...") to a blessing, writing the error response itself on failure.
func (s *Server) blessingFromPath(w http.ResponseWriter, r *http.Request) *model.ToolBlessing {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")
	var b *model.ToolBlessing
	var err error
	if strings.HasPrefix(id, "sha256:") {
		b, err = s.store.GetActiveToolBlessingByHash(r.Context(), id)
	} else {
		b, err = s.store.GetToolBlessing(r.Context(), id)
	}
	if err != nil {
		internalErr(w, reqID, err)
		return nil
	}
	if b == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("tool blessing", id))
		return nil
	}
	return b
}

// handleAddBlessingImage adds an allowed image digest to an active blessing.
// POST /api/v1/admin/tool-blessings/{id}/images
func (s *Server) handleAddBlessingImage(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	var req addImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	if !model.IsValidSHA256(req.ImageSHA256) {
		validationErr(w, reqID, fmt.Sprintf("image_sha256: %q is not a 64-character hex sha256 digest", req.ImageSHA256))
		return
	}
	b := s.blessingFromPath(w, r)
	if b == nil {
		return
	}
	if err := s.store.AddToolBlessingImageSHA256(r.Context(), b.ID, req.ImageSHA256); err != nil {
		switch {
		case errors.Is(err, store.ErrBlessingRevoked):
			respondError(w, reqID, http.StatusConflict, &model.APIError{Code: model.ErrConflict, Message: "blessing is revoked"})
		case errors.Is(err, store.ErrBlessingNotFound):
			respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("tool blessing", b.ID))
		default:
			internalErr(w, reqID, err)
		}
		return
	}
	s.metrics.IncToolBlessingEvent("add_image")
	s.logger.Warn("AUDIT tool blessing image added", "audit", "tool_blessing", "action", "add_image",
		"blessing_id", b.ID, "tool_hash", b.ToolHash, "image_sha256", model.NormalizeSHA256(req.ImageSHA256), "by", adminName(r))
	updated, err := s.store.GetToolBlessing(r.Context(), b.ID)
	if err != nil || updated == nil {
		internalErr(w, reqID, fmt.Errorf("reload blessing: %v", err))
		return
	}
	respondOK(w, reqID, updated)
}

// handleRevokeToolBlessing revokes a blessing (the row is kept).
// DELETE /api/v1/admin/tool-blessings/{id}   body: {"reason": "..."}
func (s *Server) handleRevokeToolBlessing(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	var req revokeBlessingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		validationErr(w, reqID, "invalid JSON body: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Reason) == "" {
		validationErr(w, reqID, "reason is required")
		return
	}
	b := s.blessingFromPath(w, r)
	if b == nil {
		return
	}
	by := adminName(r)
	if err := s.store.RevokeToolBlessing(r.Context(), b.ID, by, req.Reason); err != nil {
		switch {
		case errors.Is(err, store.ErrBlessingRevoked):
			respondError(w, reqID, http.StatusConflict, &model.APIError{Code: model.ErrConflict, Message: "blessing is already revoked"})
		case errors.Is(err, store.ErrBlessingNotFound):
			respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("tool blessing", b.ID))
		default:
			internalErr(w, reqID, err)
		}
		return
	}
	s.metrics.IncToolBlessingEvent("revoke")
	s.logger.Warn("AUDIT tool blessing revoked", "audit", "tool_blessing", "action", "revoke",
		"blessing_id", b.ID, "tool_hash", b.ToolHash, "revoked_by", by, "reason", req.Reason)
	respondOK(w, reqID, map[string]any{"id": b.ID, "revoked": true})
}
