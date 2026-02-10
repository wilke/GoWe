package server

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/pkg/model"
)

// requestID generates a unique request identifier.
func requestID() string {
	return "req_" + uuid.New().String()[:8]
}

// respondOK writes a success response with the standard envelope.
func respondOK(w http.ResponseWriter, reqID string, data any) {
	respondJSON(w, http.StatusOK, reqID, data, nil, nil)
}

// respondCreated writes a 201 response with the standard envelope.
func respondCreated(w http.ResponseWriter, reqID string, data any) {
	respondJSON(w, http.StatusCreated, reqID, data, nil, nil)
}

// respondList writes a success response with pagination.
func respondList(w http.ResponseWriter, reqID string, data any, pg *model.Pagination) {
	respondJSON(w, http.StatusOK, reqID, data, pg, nil)
}

// respondError writes an error response with the standard envelope.
func respondError(w http.ResponseWriter, reqID string, status int, apiErr *model.APIError) {
	respondJSON(w, status, reqID, nil, nil, apiErr)
}

func respondJSON(w http.ResponseWriter, status int, reqID string, data any, pg *model.Pagination, apiErr *model.APIError) {
	resp := model.Response{
		RequestID: reqID,
		Timestamp: time.Now().UTC(),
		Data:      data,
		Pagination: pg,
		Error:     apiErr,
	}
	if apiErr != nil {
		resp.Status = "error"
	} else {
		resp.Status = "ok"
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(resp)
}
