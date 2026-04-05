package server

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

// validLabelKey matches alphanumeric keys with hyphens and underscores.
var validLabelKey = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

// handleListLabelVocabulary returns all controlled vocabulary entries.
// GET /api/v1/labels (authenticated) or GET /api/v1/admin/labels
func (s *Server) handleListLabelVocabulary(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	entries, err := s.store.ListLabelVocabulary(r.Context())
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}
	respondOK(w, reqID, entries)
}

// handleCreateLabelVocabulary creates a new CV entry.
// POST /api/v1/admin/labels
func (s *Server) handleCreateLabelVocabulary(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	var req struct {
		Key         string `json:"key"`
		Value       string `json:"value"`
		Description string `json:"description"`
		Color       string `json:"color"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, reqID, http.StatusBadRequest, &model.APIError{
			Code:    model.ErrValidation,
			Message: "invalid JSON body: " + err.Error(),
		})
		return
	}

	req.Key = strings.TrimSpace(req.Key)
	req.Value = strings.TrimSpace(req.Value)
	if req.Key == "" || req.Value == "" {
		respondError(w, reqID, http.StatusBadRequest,
			model.NewValidationError("key and value are required"))
		return
	}

	lv := &model.LabelVocabulary{
		ID:          "lv_" + uuid.New().String(),
		Key:         req.Key,
		Value:       req.Value,
		Description: req.Description,
		Color:       req.Color,
		CreatedAt:   time.Now().UTC(),
	}

	if err := s.store.CreateLabelVocabulary(r.Context(), lv); err != nil {
		if strings.Contains(err.Error(), "UNIQUE constraint") {
			respondError(w, reqID, http.StatusConflict, &model.APIError{
				Code:    model.ErrConflict,
				Message: "label vocabulary entry already exists for key:value " + req.Key + ":" + req.Value,
			})
			return
		}
		respondError(w, reqID, http.StatusInternalServerError,
			model.NewInternalError(err.Error()))
		return
	}

	s.logger.Info("label vocabulary created", "id", lv.ID, "key", lv.Key, "value", lv.Value)
	respondCreated(w, reqID, lv)
}

// handleDeleteLabelVocabulary deletes a CV entry.
// DELETE /api/v1/admin/labels/{id}
func (s *Server) handleDeleteLabelVocabulary(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	id := chi.URLParam(r, "id")

	if err := s.store.DeleteLabelVocabulary(r.Context(), id); err != nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("label vocabulary", id))
		return
	}

	s.logger.Info("label vocabulary deleted", "id", id)
	respondOK(w, reqID, map[string]any{"deleted": true})
}

// validateLabelsAgainstCV checks that label values match the controlled vocabulary
// for any keys that have CV entries. Keys without CV entries pass through (free-form).
func (s *Server) validateLabelsAgainstCV(ctx context.Context, labels map[string]string) *model.APIError {
	if len(labels) == 0 {
		return nil
	}

	entries, err := s.store.ListLabelVocabulary(ctx)
	if err != nil {
		return model.NewInternalError(err.Error())
	}

	// Build map of key -> allowed values.
	allowed := make(map[string]map[string]bool)
	for _, e := range entries {
		if allowed[e.Key] == nil {
			allowed[e.Key] = make(map[string]bool)
		}
		allowed[e.Key][e.Value] = true
	}

	var details []model.FieldError
	for k, v := range labels {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !validLabelKey.MatchString(k) {
			details = append(details, model.FieldError{
				Field:   "labels." + k,
				Message: "label key must match [a-zA-Z0-9_-]+",
			})
			continue
		}
		if vals, ok := allowed[k]; ok {
			if !vals[v] {
				details = append(details, model.FieldError{
					Field:   "labels." + k,
					Message: "value '" + v + "' is not in the controlled vocabulary for key '" + k + "'",
				})
			}
		}
		// Keys without CV entries are free-form — no validation needed.
	}

	if len(details) > 0 {
		return model.NewValidationError("label validation failed", details...)
	}
	return nil
}
