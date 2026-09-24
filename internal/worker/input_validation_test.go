package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/model"
)

// TestReportFailure_Permanent covers #273 item 6's worker side: reportFailure
// sets TaskResult.Permanent exactly when the execution error wraps
// validate.ErrInputValidation, and never otherwise.
func TestReportFailure_Permanent(t *testing.T) {
	tests := []struct {
		name          string
		execErr       error
		wantPermanent bool
	}{
		{
			name:          "error wraps ErrInputValidation",
			execErr:       fmt.Errorf("execute: %w: input %q: expected one of [fixed, semantic]", validate.ErrInputValidation, "chunk_method"),
			wantPermanent: true,
		},
		{
			name:          "nested wrap still detected via errors.Is",
			execErr:       fmt.Errorf("task t1: execute: %w", fmt.Errorf("tool-level: %w: bad value", validate.ErrInputValidation)),
			wantPermanent: true,
		},
		{
			name:          "ordinary execution error",
			execErr:       fmt.Errorf("execute: exit status 1"),
			wantPermanent: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured TaskResult
			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if err := json.Unmarshal(body, &captured); err != nil {
					t.Fatalf("decode request body: %v", err)
				}
				w.WriteHeader(http.StatusOK)
				w.Write([]byte(`{"status":"ok"}`))
			}))
			defer ts.Close()

			w := newReportTestWorker(ts.URL)
			_ = w.reportFailure(context.Background(), &model.Task{ID: "task_1"}, tt.execErr)

			if captured.State != model.TaskStateFailed {
				t.Errorf("reported state = %q, want FAILED", captured.State)
			}
			if captured.Permanent != tt.wantPermanent {
				t.Errorf("Permanent = %v, want %v", captured.Permanent, tt.wantPermanent)
			}
		})
	}
}
