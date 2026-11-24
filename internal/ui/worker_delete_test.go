package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// TestHandleWorkerDelete_OnlyOffline: deleting a worker requeues its running
// tasks, so the UI must refuse to delete a worker that may still be executing.
func TestHandleWorkerDelete_OnlyOffline(t *testing.T) {
	tests := []struct {
		name        string
		state       model.WorkerState
		create      bool
		wantStatus  int
		wantDeleted bool
	}{
		{name: "offline worker is deleted", state: model.WorkerStateOffline, create: true, wantStatus: http.StatusOK, wantDeleted: true},
		{name: "online worker is refused", state: model.WorkerStateOnline, create: true, wantStatus: http.StatusConflict},
		{name: "draining worker is refused", state: model.WorkerStateDraining, create: true, wantStatus: http.StatusConflict},
		{name: "unknown worker is not found", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupTestStore(t)
			ctx := context.Background()
			const id = "wrk_ui-delete"
			if tt.create {
				now := time.Now().UTC()
				wk := &model.Worker{
					ID: id, Name: "w", Hostname: "h", State: tt.state,
					Runtime: model.RuntimeNone, LastSeen: now, RegisteredAt: now,
				}
				if err := st.CreateWorker(ctx, wk); err != nil {
					t.Fatalf("create worker: %v", err)
				}
			}

			req := httptest.NewRequest(http.MethodDelete, "/workers/"+id, nil)
			req.SetPathValue("id", id)
			rec := httptest.NewRecorder()
			newTestUI(st).HandleWorkerDelete(rec, req)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			got, err := st.GetWorker(ctx, id)
			if err != nil {
				t.Fatalf("get worker: %v", err)
			}
			if tt.create && (got == nil) != tt.wantDeleted {
				t.Errorf("worker deleted = %v, want %v", got == nil, tt.wantDeleted)
			}
		})
	}
}
