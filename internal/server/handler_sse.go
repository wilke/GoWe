package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// handleSSESubmission streams submission updates via Server-Sent Events.
// GET /api/v1/sse/submissions/{id}
func (s *Server) handleSSESubmission(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	reqID := RequestIDFromContext(r.Context())

	// Check if submission exists.
	sub, err := s.store.GetSubmission(r.Context(), id)
	if err != nil {
		respondError(w, reqID, http.StatusInternalServerError, model.NewInternalError(err.Error()))
		return
	}
	if sub == nil {
		respondError(w, reqID, http.StatusNotFound, model.NewNotFoundError("submission", id))
		return
	}

	// Set headers for SSE.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	// Send initial state.
	if err := sendSSEEvent(w, flusher, "init", sub); err != nil {
		s.logger.Debug("sse client disconnected", "id", id, "error", err)
		return
	}

	// Poll for updates until submission is terminal or client disconnects.
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	lastState := sub.State

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// Fetch latest state.
			sub, err = s.store.GetSubmission(r.Context(), id)
			if err != nil {
				s.logger.Error("sse fetch error", "id", id, "error", err)
				continue
			}
			if sub == nil {
				return
			}

			// Send update if state changed.
			if sub.State != lastState {
				if err := sendSSEEvent(w, flusher, "update", sub); err != nil {
					s.logger.Debug("sse client disconnected", "id", id)
					return
				}
				lastState = sub.State
			} else {
				// Send heartbeat.
				fmt.Fprintf(w, ": heartbeat\n\n")
				flusher.Flush()
			}

			// Stop streaming if submission is terminal.
			if sub.State.IsTerminal() {
				if err := sendSSEEvent(w, flusher, "complete", sub); err != nil {
					return
				}
				return
			}
		}
	}
}

func sendSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, data any) error {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return err
	}

	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
	if err != nil {
		return err
	}

	flusher.Flush()
	return nil
}
