package ui

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// TestHandleWorkerDelete_OnlyOffline: deleting a worker requeues its running
// tasks, so the UI must refuse to delete a worker that may still be
// executing: one that is not offline, or offline but seen within 3 x the
// reaper's timeout (removalSilence), even past one timeout.
func TestHandleWorkerDelete_OnlyOffline(t *testing.T) {
	silent := time.Now().UTC().Add(-time.Hour)
	recent := time.Now().UTC()
	// Past the reaper's timeout (marked offline), within the removal wait.
	pastTimeout := time.Now().UTC().Add(-(DefaultWorkerTimeout + DefaultWorkerTimeout/2))
	tests := []struct {
		name        string
		state       model.WorkerState
		lastSeen    time.Time
		create      bool
		wantStatus  int
		wantDeleted bool
		wantBody    string
	}{
		{name: "offline silent worker is deleted", state: model.WorkerStateOffline, lastSeen: silent, create: true, wantStatus: http.StatusOK, wantDeleted: true},
		{name: "offline recently seen worker is refused", state: model.WorkerStateOffline, lastSeen: recent, create: true, wantStatus: http.StatusConflict, wantBody: "last seen"},
		{name: "offline worker silent for 1.5 x the timeout is refused", state: model.WorkerStateOffline, lastSeen: pastTimeout, create: true, wantStatus: http.StatusConflict, wantBody: "no heartbeat for 1m30s"},
		{name: "online worker is refused", state: model.WorkerStateOnline, lastSeen: silent, create: true, wantStatus: http.StatusConflict, wantBody: "stop it first"},
		{name: "draining worker is refused", state: model.WorkerStateDraining, lastSeen: silent, create: true, wantStatus: http.StatusConflict, wantBody: "stop it first"},
		{name: "unknown worker is not found", wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := setupTestStore(t)
			ctx := context.Background()
			const id = "wrk_ui-delete"
			if tt.create {
				wk := &model.Worker{
					ID: id, Name: "w", Hostname: "h", State: tt.state,
					Runtime: model.RuntimeNone, LastSeen: tt.lastSeen, RegisteredAt: tt.lastSeen,
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
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body = %q, want it to contain %q", rec.Body.String(), tt.wantBody)
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

// listRecordingStore remembers the workers its ListWorkers returned, so a
// test can see what the handler did to them before rendering.
type listRecordingStore struct {
	store.Store
	listed []*model.Worker
}

func (s *listRecordingStore) ListWorkers(ctx context.Context) ([]*model.Worker, error) {
	ws, err := s.Store.ListWorkers(ctx)
	s.listed = ws
	return ws, err
}

// The workers page is open to every signed-in user: the peer address is
// blanked before rendering unless the user is a server admin, on copies: the
// objects the store returned are not modified.
func TestHandleWorkerList_PeerAddrAdminOnly(t *testing.T) {
	tests := []struct {
		name  string
		sess  *model.Session
		admin bool
	}{
		{"plain user", &model.Session{ID: "s", Username: "alice", Role: string(model.RoleUser)}, false},
		{"admin", &model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			base := setupTestStore(t)
			now := time.Now().UTC()
			if err := base.CreateWorker(context.Background(), &model.Worker{
				ID: "wrk_peer", Name: "w", Hostname: "h", Group: "gpu", State: model.WorkerStateOnline,
				Runtime: model.RuntimeNone, LastSeen: now, RegisteredAt: now, PeerAddr: "192.0.2.44",
			}); err != nil {
				t.Fatal(err)
			}
			st := &listRecordingStore{Store: base}
			req := withSession(httptest.NewRequest(http.MethodGet, "/workers", nil), tt.sess)
			rec := httptest.NewRecorder()
			newTestUI(st).HandleWorkerList(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body: %s", rec.Code, rec.Body.String())
			}
			if len(st.listed) != 1 {
				t.Fatalf("listed %d workers, want 1", len(st.listed))
			}
			if got := st.listed[0].PeerAddr; got != "192.0.2.44" {
				t.Errorf("the store's worker object was modified: peer_addr = %q, want 192.0.2.44", got)
			}
			if !tt.admin && strings.Contains(rec.Body.String(), "192.0.2.44") {
				t.Error("the page shows the peer address to a non-admin")
			}
		})
	}
}

func TestWorkersWithoutPeerAddr(t *testing.T) {
	orig := []*model.Worker{{ID: "wrk_1", Name: "a", PeerAddr: "192.0.2.1"}, {ID: "wrk_2", Name: "b", PeerAddr: "192.0.2.2"}}
	got := workersWithoutPeerAddr(orig)
	if len(got) != 2 {
		t.Fatalf("got %d workers, want 2", len(got))
	}
	for i, wk := range got {
		if wk == orig[i] {
			t.Errorf("worker %d is the original object, want a copy", i)
		}
		if wk.PeerAddr != "" || wk.ID != orig[i].ID || wk.Name != orig[i].Name {
			t.Errorf("copy %d = %+v, want the original with peer_addr blanked", i, wk)
		}
	}
	if orig[0].PeerAddr != "192.0.2.1" || orig[1].PeerAddr != "192.0.2.2" {
		t.Errorf("the originals were modified: %+v %+v", orig[0], orig[1])
	}
}

// failingDeleteStore fails DeleteOfflineWorker for one worker id.
type failingDeleteStore struct {
	store.Store
	failID string
}

func (s *failingDeleteStore) DeleteOfflineWorker(ctx context.Context, id, group string, silentFor time.Duration) (bool, error) {
	if id == s.failID {
		return false, errors.New("database is locked")
	}
	return s.Store.DeleteOfflineWorker(ctx, id, group, silentFor)
}

// The purge removes the offline workers silent for 3 x the timeout, skips
// the other offline ones, and logs the counts at INFO and each store error
// at WARN.
func TestHandleWorkerPurgeOffline_Logs(t *testing.T) {
	base := setupTestStore(t)
	ctx := context.Background()
	old := time.Now().UTC().Add(-time.Hour)
	pastTimeout := time.Now().UTC().Add(-(DefaultWorkerTimeout + DefaultWorkerTimeout/2))
	for _, wk := range []struct {
		id       string
		state    model.WorkerState
		lastSeen time.Time
	}{
		{"wrk_old", model.WorkerStateOffline, old},
		{"wrk_recent", model.WorkerStateOffline, pastTimeout},
		{"wrk_online", model.WorkerStateOnline, old},
		{"wrk_err", model.WorkerStateOffline, old},
	} {
		if err := base.CreateWorker(ctx, &model.Worker{
			ID: wk.id, Name: wk.id, Hostname: "h", State: wk.state,
			Runtime: model.RuntimeNone, LastSeen: wk.lastSeen, RegisteredAt: wk.lastSeen,
		}); err != nil {
			t.Fatal(err)
		}
	}
	var logs bytes.Buffer
	u := New(&failingDeleteStore{Store: base, failID: "wrk_err"}, slog.New(slog.NewTextHandler(&logs, nil)), Config{})

	rec := httptest.NewRecorder()
	u.HandleWorkerPurgeOffline(rec, httptest.NewRequest(http.MethodPost, "/workers/purge-offline", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	for id, wantKept := range map[string]bool{"wrk_old": false, "wrk_recent": true, "wrk_online": true, "wrk_err": true} {
		got, err := base.GetWorker(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if (got != nil) != wantKept {
			t.Errorf("%s kept = %v, want %v", id, got != nil, wantKept)
		}
	}
	out := logs.String()
	for _, want := range []string{
		`level=INFO msg="purged offline workers via UI"`, "removed=1", "skipped_not_removable=1", "failed=1",
		`level=WARN msg="purge offline workers via UI: delete failed"`, "worker_id=wrk_err",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "level=WARN") != 1 {
		t.Errorf("want exactly one WARN line:\n%s", out)
	}
}
