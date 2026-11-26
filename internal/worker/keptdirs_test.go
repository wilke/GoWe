package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// keptTestNow is the reference time of the sweep tests.
var keptTestNow = time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)

func newKeptTestWorker(t *testing.T, serverURL string, rt Runtime) *Worker {
	t.Helper()
	return &Worker{
		client:  NewClient(serverURL, nil),
		runtime: rt,
		workDir: t.TempDir(),
		active:  newActiveTaskSet(),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

// readTestMarker returns the marker of base in workDir; ok is false when there
// is none.
func readTestMarker(t *testing.T, workDir, base string) (m keptMarker, ok bool) {
	t.Helper()
	data, err := os.ReadFile(keptMarkerPath(workDir, base))
	if os.IsNotExist(err) {
		return m, false
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("marker %s: %v (%q)", base, err, data)
	}
	return m, true
}

// writeTestMarker writes a marker kept at keptAt, with keptAt as the marker
// file's modification time too (the sweep uses the later of both).
func writeTestMarker(t *testing.T, workDir, base, taskID, reason string, keptAt time.Time) {
	t.Helper()
	m := keptMarker{TaskID: taskID, Reason: reason, KeptAt: keptAt.UTC().Format(time.RFC3339)}
	if err := writeKeptMarker(workDir, base, m); err != nil {
		t.Fatal(err)
	}
	setTestMtime(t, keptMarkerPath(workDir, base), keptAt)
}

func setTestMtime(t *testing.T, p string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}

// lockTestWorkDir gives w the workdir lock Run would take, released at the end
// of the test: the owner lock, as nobody else uses the test's workdir.
func lockTestWorkDir(t *testing.T, w *Worker) {
	t.Helper()
	l, err := openWorkDirLock(w.workDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.close() })
	if l.owner == nil {
		t.Fatal("not the owner of a fresh workdir")
	}
	w.wdLock = l
}

func keptLegacyTask(id string, cmd ...string) *model.Task {
	args := make([]any, len(cmd))
	for i, c := range cmd {
		args[i] = c
	}
	return &model.Task{ID: id, Inputs: map[string]any{"_base_command": args}}
}

// stateServer answers every request with status and records the reported
// task states.
type stateServer struct {
	mu     sync.Mutex
	states []string
}

func newStateServer(t *testing.T, status int) (*stateServer, string) {
	t.Helper()
	s := &stateServer{}
	ts := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		var res TaskResult
		body, _ := io.ReadAll(r.Body)
		if json.Unmarshal(body, &res) == nil && res.State != "" {
			s.mu.Lock()
			s.states = append(s.states, string(res.State))
			s.mu.Unlock()
		}
		rw.WriteHeader(status)
		_, _ = rw.Write([]byte(`{"status":"ok","data":{}}`))
	}))
	t.Cleanup(ts.Close)
	return s, ts.URL
}

func (s *stateServer) reported() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.states...)
}

// TestKeptMarker_RunOutcomes: the end of a run writes the marker of a task
// dir kept because the run did not end with SUCCESS.
func TestKeptMarker_RunOutcomes(t *testing.T) {
	shrinkBackoff(t)

	t.Run("failed run", func(t *testing.T) {
		_, url := newStateServer(t, http.StatusOK)
		w := newKeptTestWorker(t, url, NewBareRuntime())
		before := time.Now().UTC().Add(-time.Second)
		if err := w.executeTask(context.Background(), keptLegacyTask("task_fail", "false")); err != nil {
			t.Fatalf("executeTask: %v", err)
		}
		m, ok := readTestMarker(t, w.workDir, "task_fail")
		if !ok {
			t.Fatal("no marker for a failed run")
		}
		if m.TaskID != "task_fail" || m.Reason != keptReasonFailed {
			t.Errorf("marker = %+v, want task_fail/failed", m)
		}
		keptAt, err := time.Parse(time.RFC3339, m.KeptAt)
		if err != nil || keptAt.Before(before.Truncate(time.Second)) || keptAt.After(time.Now().UTC().Add(time.Second)) {
			t.Errorf("kept_at = %q (%v), want about now", m.KeptAt, err)
		}
		fi, err := os.Stat(keptMarkerPath(w.workDir, "task_fail"))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o644 {
			t.Errorf("marker mode = %v, want 0644", fi.Mode().Perm())
		}
		for _, n := range dirEntries(t, w.workDir) {
			if strings.Contains(n, ".tmp-") {
				t.Errorf("temporary marker file left behind: %s", n)
			}
		}
	})

	t.Run("successful run removes dir and writes no marker", func(t *testing.T) {
		_, url := newStateServer(t, http.StatusOK)
		w := newKeptTestWorker(t, url, NewBareRuntime())
		if err := w.executeTask(context.Background(), keptLegacyTask("task_ok", "true")); err != nil {
			t.Fatalf("executeTask: %v", err)
		}
		if _, err := os.Stat(filepath.Join(w.workDir, "task_ok")); !os.IsNotExist(err) {
			t.Error("task dir still exists after SUCCESS")
		}
		if _, ok := readTestMarker(t, w.workDir, "task_ok"); ok {
			t.Error("marker written for a successful run")
		}
	})

	t.Run("success the server did not accept writes no marker", func(t *testing.T) {
		_, url := newStateServer(t, http.StatusInternalServerError)
		w := newKeptTestWorker(t, url, NewBareRuntime())
		if err := w.executeTask(context.Background(), keptLegacyTask("task_rej", "true")); err == nil {
			t.Fatal("expected an error from the rejected report")
		}
		if _, err := os.Stat(filepath.Join(w.workDir, "task_rej")); err != nil {
			t.Fatalf("task dir removed: %v", err)
		}
		if _, ok := readTestMarker(t, w.workDir, "task_rej"); ok {
			t.Error("marker written for an unaccepted SUCCESS")
		}
	})

	cancelTests := []struct {
		name      string
		serverCxl bool // cancel through w.active (server cancel) instead of the parent context (shutdown)
		wantState string
	}{
		{name: "server cancel", serverCxl: true, wantState: string(model.TaskStateSkipped)},
		{name: "shutdown leaves task for requeue", serverCxl: false},
	}
	for _, tt := range cancelTests {
		t.Run(tt.name, func(t *testing.T) {
			srv, url := newStateServer(t, http.StatusOK)
			rt := newBlockingRuntime()
			w := newKeptTestWorker(t, url, rt)
			const id = "task_cxl"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- w.executeTask(ctx, keptLegacyTask(id, "sleep", "100")) }()
			waitFor(t, "task running", func() bool { return rt.isRunning(id) })
			if tt.serverCxl {
				w.active.cancel(id)
			} else {
				cancel()
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("run did not end after cancellation")
			}
			got := srv.reported()
			if tt.wantState == "" && len(got) != 0 {
				t.Errorf("reported %v, want no report at shutdown", got)
			}
			if tt.wantState != "" && (len(got) != 1 || got[0] != tt.wantState) {
				t.Errorf("reported %v, want [%s]", got, tt.wantState)
			}
			m, ok := readTestMarker(t, w.workDir, id)
			if !ok {
				t.Fatal("no marker for a cancelled run")
			}
			if m.TaskID != id || m.Reason != keptReasonCancelled {
				t.Errorf("marker = %+v, want %s/cancelled", m, id)
			}
		})
	}
}

func TestKeptMarker_CleanupTaskDir(t *testing.T) {
	staged := map[string]any{"out": map[string]any{"class": "File", "location": "file:///shared/x/out.txt"}}
	tests := []struct {
		name        string
		keep        bool
		debug       bool
		inPlace     bool // output located in the task dir
		staleMarker bool // a marker exists before the call
		wantDir     bool
		wantReason  string // "" = no marker afterwards
	}{
		{name: "keep-task-dirs", keep: true, wantDir: true, wantReason: keptReasonKeepTaskDirs},
		{name: "debug submission", debug: true, wantDir: true, wantReason: keptReasonDebug},
		{name: "outputs in place", inPlace: true, wantDir: true},
		{name: "outputs in place removes an old marker", inPlace: true, staleMarker: true, wantDir: true},
		{name: "outputs in place win over keep-task-dirs", inPlace: true, keep: true, wantDir: true},
		{name: "removed dir", wantDir: false},
		{name: "removed dir removes a leftover marker", staleMarker: true, wantDir: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wd := t.TempDir()
			const id = "task_c"
			mkAttemptDirs(t, wd, id, id+"_tmp", id+"_staging")
			if tt.staleMarker {
				writeTestMarker(t, wd, id, id, keptReasonFailed, keptTestNow.Add(-1000*time.Hour))
			}
			w := &Worker{
				workDir:      wd,
				keepTaskDirs: tt.keep,
				active:       newActiveTaskSet(),
				logger:       slog.New(slog.NewTextHandler(io.Discard, nil)),
			}
			// As runTask would: the task is running and its run created the
			// task dir (prepareTaskDir records it; markers need it).
			w.active.add(id, func() {})
			if fi, err := os.Stat(filepath.Join(wd, id)); err != nil {
				t.Fatal(err)
			} else {
				w.active.setTaskDir(id, fi)
			}
			task := &model.Task{ID: id}
			if tt.debug {
				task.RuntimeHints = &model.RuntimeHints{Debug: true}
			}
			outputs := staged
			if tt.inPlace {
				abs, err := filepath.Abs(filepath.Join(wd, id, "f.txt"))
				if err != nil {
					t.Fatal(err)
				}
				outputs = map[string]any{"out": map[string]any{"class": "File", "location": "file://" + abs}}
			}

			w.cleanupTaskDir(task, outputs)

			_, err := os.Stat(filepath.Join(wd, id))
			if gotDir := err == nil; gotDir != tt.wantDir {
				t.Errorf("task dir exists = %v, want %v", gotDir, tt.wantDir)
			}
			m, ok := readTestMarker(t, wd, id)
			switch {
			case tt.wantReason == "" && ok:
				t.Errorf("marker %+v, want none", m)
			case tt.wantReason != "" && !ok:
				t.Errorf("no marker, want reason %q", tt.wantReason)
			case ok && (m.Reason != tt.wantReason || m.TaskID != id):
				t.Errorf("marker = %+v, want %s/%s", m, id, tt.wantReason)
			}
		})
	}
}

// TestKeptMarker_Archive: the marker moves with the archived attempt; an
// archived attempt without one gets "earlier-attempt". archivePreviousAttempt
// itself is unchanged (TestArchivePreviousAttempt).
func TestKeptMarker_Archive(t *testing.T) {
	old := keptTestNow.Add(-48 * time.Hour)
	type marker struct {
		base, reason string
		keptAt       time.Time
	}
	tests := []struct {
		name         string
		dirs         []string
		markers      []marker
		wantArchived string            // base name, "" = nothing archived
		wantMarkers  map[string]string // base -> reason afterwards
		wantKeptAt   map[string]time.Time
		wantNoMarker []string
	}{
		{
			name:         "marker moves with the directories",
			dirs:         []string{"task_a", "task_a_tmp"},
			markers:      []marker{{"task_a", keptReasonFailed, old}},
			wantArchived: "task_a.attempt-1",
			wantMarkers:  map[string]string{"task_a.attempt-1": keptReasonFailed},
			wantKeptAt:   map[string]time.Time{"task_a.attempt-1": old},
			wantNoMarker: []string{"task_a"},
		},
		{
			name:         "missing marker is created",
			dirs:         []string{"task_a"},
			wantArchived: "task_a.attempt-1",
			wantMarkers:  map[string]string{"task_a.attempt-1": keptReasonEarlierAttempt},
			wantNoMarker: []string{"task_a"},
		},
		{
			name:         "stale attempt marker without directories is overwritten",
			dirs:         []string{"task_a"},
			markers:      []marker{{"task_a", keptReasonCancelled, old}, {"task_a.attempt-1", keptReasonDebug, old.Add(-time.Hour)}},
			wantArchived: "task_a.attempt-1",
			wantMarkers:  map[string]string{"task_a.attempt-1": keptReasonCancelled},
			wantKeptAt:   map[string]time.Time{"task_a.attempt-1": old},
			wantNoMarker: []string{"task_a"},
		},
		{
			name:         "next free number is chosen by the directories",
			dirs:         []string{"task_a", "task_a.attempt-1_staging"},
			markers:      []marker{{"task_a", keptReasonFailed, old}, {"task_a.attempt-1", keptReasonFailed, old.Add(-time.Hour)}},
			wantArchived: "task_a.attempt-2",
			wantMarkers:  map[string]string{"task_a.attempt-1": keptReasonFailed, "task_a.attempt-2": keptReasonFailed},
			wantKeptAt:   map[string]time.Time{"task_a.attempt-1": old.Add(-time.Hour), "task_a.attempt-2": old},
			wantNoMarker: []string{"task_a"},
		},
		{
			name:         "a marker alone is not an attempt and is removed",
			markers:      []marker{{"task_a", keptReasonFailed, old}},
			wantNoMarker: []string{"task_a", "task_a.attempt-1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wd := t.TempDir()
			mkAttemptDirs(t, wd, tt.dirs...)
			for _, m := range tt.markers {
				writeTestMarker(t, wd, m.base, "task_a", m.reason, m.keptAt)
			}
			w := &Worker{workDir: wd, logger: slog.New(slog.NewTextHandler(io.Discard, nil))}

			got, err := w.prepareTaskDir("task_a")
			if err != nil {
				t.Fatalf("prepareTaskDir: %v", err)
			}
			want := ""
			if tt.wantArchived != "" {
				want = filepath.Join(wd, tt.wantArchived)
			}
			if got != want {
				t.Errorf("archived = %q, want %q", got, want)
			}
			ents, err := os.ReadDir(filepath.Join(wd, "task_a"))
			if err != nil || len(ents) != 0 {
				t.Errorf("fresh task dir: %d entries, %v", len(ents), err)
			}
			for base, reason := range tt.wantMarkers {
				m, ok := readTestMarker(t, wd, base)
				if !ok {
					t.Errorf("no marker for %s", base)
					continue
				}
				if m.Reason != reason || m.TaskID != "task_a" {
					t.Errorf("%s marker = %+v, want reason %q", base, m, reason)
				}
				if at, ok := tt.wantKeptAt[base]; ok && m.KeptAt != at.Format(time.RFC3339) {
					t.Errorf("%s kept_at = %q, want %q", base, m.KeptAt, at.Format(time.RFC3339))
				}
			}
			for _, base := range tt.wantNoMarker {
				if m, ok := readTestMarker(t, wd, base); ok {
					t.Errorf("unexpected marker for %s: %+v", base, m)
				}
			}
		})
	}
}

// A marker does not count as something to archive.
func TestArchivePreviousAttempt_IgnoresMarker(t *testing.T) {
	wd := t.TempDir()
	writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow)
	got, err := archivePreviousAttempt(wd, "task_a")
	if err != nil || got != "" {
		t.Fatalf("archivePreviousAttempt = %q, %v; want nothing archived", got, err)
	}
	if _, ok := readTestMarker(t, wd, "task_a"); !ok {
		t.Error("archivePreviousAttempt touched the marker")
	}
}

func TestSweepKeptTaskDirs(t *testing.T) {
	const ttl = time.Hour
	oldAt := keptTestNow.Add(-2 * time.Hour)
	all := func(base string) []string { return []string{base, base + "_tmp", base + "_staging"} }
	writeRaw := func(t *testing.T, p, content string) {
		t.Helper()
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name        string
		ttl         time.Duration
		active      []string
		setup       func(t *testing.T, wd string)
		wantRemoved int
		wantPresent []string // relative to the workdir
		wantAbsent  []string
		wantWarn    string // substring of the WARN log, "" = not checked
	}{
		{
			name: "older than the ttl: dirs and marker removed",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, all("task_a")...)
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, oldAt)
			},
			wantRemoved: 1,
			wantAbsent:  append(all("task_a"), "task_a.kept"),
		},
		{
			name: "attempt dir older than the ttl removed",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, all("task_a.attempt-12")...)
				writeTestMarker(t, wd, "task_a.attempt-12", "task_a", keptReasonEarlierAttempt, oldAt)
			},
			wantRemoved: 1,
			wantAbsent:  append(all("task_a.attempt-12"), "task_a.attempt-12.kept"),
		},
		{
			name: "younger than the ttl kept",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, all("task_a")...)
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(-30*time.Minute))
			},
			wantPresent: append(all("task_a"), "task_a.kept"),
		},
		{
			name: "exactly the ttl kept",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a")
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(-ttl))
			},
			wantPresent: []string{"task_a", "task_a.kept"},
		},
		{
			name: "directory without a marker never touched",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, append(all("task_b"), "task_b.attempt-1")...)
				old := keptTestNow.Add(-1000 * time.Hour)
				for _, d := range append(all("task_b"), "task_b.attempt-1") {
					if err := os.Chtimes(filepath.Join(wd, d), old, old); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantPresent: append(all("task_b"), "task_b.attempt-1"),
		},
		{
			name:   "live dir of an active task kept, its old attempt removed",
			ttl:    ttl,
			active: []string{"task_a"},
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, append(all("task_a"), all("task_a.attempt-1")...)...)
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, oldAt)
				writeTestMarker(t, wd, "task_a.attempt-1", "task_a", keptReasonFailed, oldAt)
			},
			wantRemoved: 1,
			wantPresent: append(all("task_a"), "task_a.kept"),
			wantAbsent:  append(all("task_a.attempt-1"), "task_a.attempt-1.kept"),
		},
		{
			name: "file name does not match the task id",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a", "task_b")
				writeTestMarker(t, wd, "task_a", "task_b", keptReasonFailed, oldAt)
			},
			wantPresent: []string{"task_a", "task_b", "task_a.kept"},
		},
		{
			name: "attempt suffix that is not a number",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a.attempt-1x", "task_a.attempt-")
				writeTestMarker(t, wd, "task_a.attempt-1x", "task_a", keptReasonFailed, oldAt)
				writeTestMarker(t, wd, "task_a.attempt-", "task_a", keptReasonFailed, oldAt)
			},
			wantPresent: []string{"task_a.attempt-1x", "task_a.attempt-", "task_a.attempt-1x.kept", "task_a.attempt-.kept"},
		},
		{
			name: "task id with dot-dot",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a")
				writeRaw(t, filepath.Join(wd, "task_a.kept"),
					`{"task_id":"../task_a","reason":"failed","kept_at":"`+oldAt.Format(time.RFC3339)+`"}`)
				// "..kept" names base "." and "...kept" base ".."; the
				// parent of the workdir holds a sentinel (see below).
				writeRaw(t, filepath.Join(wd, "..kept"),
					`{"task_id":".","reason":"failed","kept_at":"`+oldAt.Format(time.RFC3339)+`"}`)
				writeRaw(t, filepath.Join(wd, "...kept"),
					`{"task_id":"..","reason":"failed","kept_at":"`+oldAt.Format(time.RFC3339)+`"}`)
			},
			wantPresent: []string{"task_a", "task_a.kept", "..kept", "...kept", "../sentinel"},
		},
		{
			name: "task id with a path separator",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a", "x")
				writeRaw(t, filepath.Join(wd, "task_a.kept"),
					`{"task_id":"x/task_a","reason":"failed","kept_at":"`+oldAt.Format(time.RFC3339)+`"}`)
			},
			wantPresent: []string{"task_a", "x", "task_a.kept", "../sentinel"},
		},
		{
			name: "invalid json",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, all("task_a")...)
				writeRaw(t, filepath.Join(wd, "task_a.kept"), `{"task_id": "task_a", "kept_at": `)
			},
			wantPresent: append(all("task_a"), "task_a.kept"),
		},
		{
			name: "invalid kept_at",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a")
				writeRaw(t, filepath.Join(wd, "task_a.kept"), `{"task_id":"task_a","reason":"failed","kept_at":"yesterday"}`)
			},
			wantPresent: []string{"task_a", "task_a.kept"},
		},
		{
			name: "symlinked base unlinked, target untouched",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				target := filepath.Join(filepath.Dir(wd), "target")
				mkAttemptDirs(t, filepath.Dir(wd), "target")
				if err := os.Symlink(target, filepath.Join(wd, "task_a")); err != nil {
					t.Fatal(err)
				}
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, oldAt)
			},
			wantRemoved: 1,
			wantPresent: []string{"../target/f.txt"},
			wantAbsent:  []string{"task_a", "task_a.kept"},
		},
		{
			name: "ttl 0 never sweeps",
			ttl:  0,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, all("task_a")...)
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(-10000*time.Hour))
			},
			wantPresent: append(all("task_a"), "task_a.kept"),
		},
		{
			// F2: the base is itself the id of a running task.
			name:   "base that is the id of a running task kept",
			ttl:    ttl,
			active: []string{"task_a.attempt-1"},
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, all("task_a.attempt-1")...)
				writeTestMarker(t, wd, "task_a.attempt-1", "task_a", keptReasonEarlierAttempt, oldAt)
			},
			wantPresent: append(all("task_a.attempt-1"), "task_a.attempt-1.kept"),
		},
		{
			// F5: a marker file written recently never makes an old kept_at count.
			name: "old kept_at with a fresh marker mtime kept",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a")
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(-1000*time.Hour))
				setTestMtime(t, keptMarkerPath(wd, "task_a"), keptTestNow.Add(-10*time.Minute))
			},
			wantPresent: []string{"task_a", "task_a.kept"},
		},
		{
			name: "fresh kept_at with an old marker mtime kept",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a")
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(-10*time.Minute))
				setTestMtime(t, keptMarkerPath(wd, "task_a"), keptTestNow.Add(-1000*time.Hour))
			},
			wantPresent: []string{"task_a", "task_a.kept"},
		},
		{
			name: "kept_at in the future kept",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a")
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(24*time.Hour))
				setTestMtime(t, keptMarkerPath(wd, "task_a"), oldAt)
			},
			wantPresent: []string{"task_a", "task_a.kept"},
		},
		{
			name: "zero kept_at kept with a warning",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, "task_a", "task_b")
				writeRaw(t, filepath.Join(wd, "task_a.kept"), `{"task_id":"task_a","reason":"failed","kept_at":"0001-01-01T00:00:00Z"}`)
				writeRaw(t, filepath.Join(wd, "task_b.kept"), `{"task_id":"task_b","reason":"failed","kept_at":"1970-01-01T00:00:00Z"}`)
				setTestMtime(t, filepath.Join(wd, "task_a.kept"), oldAt)
				setTestMtime(t, filepath.Join(wd, "task_b.kept"), oldAt)
			},
			wantPresent: []string{"task_a", "task_a.kept", "task_b", "task_b.kept"},
			wantWarn:    "is zero",
		},
		{
			// F4: leftovers of an interrupted earlier sweep go; a name that only
			// starts like one stays.
			name: "tombstones of an earlier sweep removed",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, ".gowe-swept-task_z-1764158400000000000", ".gowe-swept-task_z_tmp-1764158400000000000", ".gowe-swept-other")
				if err := os.Symlink(filepath.Join(filepath.Dir(wd), "target"), filepath.Join(wd, ".gowe-swept-task_y-17")); err != nil {
					t.Fatal(err)
				}
				mkAttemptDirs(t, filepath.Dir(wd), "target")
			},
			wantPresent: []string{".gowe-swept-other", "../target/f.txt"},
			wantAbsent:  []string{".gowe-swept-task_z-1764158400000000000", ".gowe-swept-task_z_tmp-1764158400000000000", ".gowe-swept-task_y-17"},
		},
		{
			name: "symlinks inside a swept tree not followed",
			ttl:  ttl,
			setup: func(t *testing.T, wd string) {
				mkAttemptDirs(t, wd, all("task_a")...)
				mkAttemptDirs(t, filepath.Dir(wd), "outside")
				if err := os.Symlink(filepath.Join(filepath.Dir(wd), "outside"), filepath.Join(wd, "task_a", "link")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(filepath.Dir(wd), "outside", "f.txt"), filepath.Join(wd, "task_a_tmp", "flink")); err != nil {
					t.Fatal(err)
				}
				writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, oldAt)
			},
			wantRemoved: 1,
			wantPresent: []string{"../outside/f.txt"},
			wantAbsent:  append(all("task_a"), "task_a.kept"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// The workdir is a subdirectory, so "../x" checks that nothing
			// outside it is touched.
			parent := t.TempDir()
			writeRaw(t, filepath.Join(parent, "sentinel"), "keep")
			wd := filepath.Join(parent, "wd")
			if err := os.Mkdir(wd, 0o755); err != nil {
				t.Fatal(err)
			}
			tt.setup(t, wd)
			var logs syncBuffer
			w := &Worker{
				workDir:        wd,
				keptTaskDirTTL: tt.ttl,
				active:         newActiveTaskSet(),
				logger:         slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelWarn})),
			}
			lockTestWorkDir(t, w)
			for _, id := range tt.active {
				w.active.add(id, func() {})
			}

			if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != tt.wantRemoved {
				t.Errorf("removed = %d, want %d", got, tt.wantRemoved)
			}
			if tt.wantWarn != "" && !strings.Contains(logs.String(), tt.wantWarn) {
				t.Errorf("WARN log %q does not contain %q", logs.String(), tt.wantWarn)
			}
			for _, n := range dirEntries(t, wd) {
				if isSweptName(n) {
					t.Errorf("moved-aside directory left behind: %s", n)
				}
			}
			for _, p := range tt.wantPresent {
				if _, err := os.Lstat(filepath.Join(wd, p)); err != nil {
					t.Errorf("%s: %v, want it kept", p, err)
				}
			}
			for _, p := range tt.wantAbsent {
				if _, err := os.Lstat(filepath.Join(wd, p)); !os.IsNotExist(err) {
					t.Errorf("%s still exists (err=%v)", p, err)
				}
			}
			if _, err := os.Stat(filepath.Join(parent, "sentinel")); err != nil {
				t.Errorf("file outside the workdir touched: %v", err)
			}
		})
	}
}

// The sweep loop removes an expired dir right away (not only after the first
// interval) and ends with the context.
func TestKeptDirSweepLoop(t *testing.T) {
	orig := keptDirSweepInterval
	keptDirSweepInterval = time.Hour
	t.Cleanup(func() { keptDirSweepInterval = orig })

	wd := t.TempDir()
	mkAttemptDirs(t, wd, "task_a")
	writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, time.Now().Add(-3*time.Hour))
	w := &Worker{
		workDir:        wd,
		keptTaskDirTTL: time.Hour,
		active:         newActiveTaskSet(),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	lockTestWorkDir(t, w)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		w.keptDirSweepLoop(ctx)
		close(done)
	}()
	waitFor(t, "expired dir removed", func() bool {
		_, err := os.Lstat(filepath.Join(wd, "task_a"))
		return os.IsNotExist(err)
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("sweep loop did not end with its context")
	}
}

// F1b: a marker is only written for the task dir the run created, not for a
// directory that replaced it while the run was going on.
func TestKeptMarker_OnlyForTheRunsTaskDir(t *testing.T) {
	t.Run("sameTaskDir", func(t *testing.T) {
		wd := t.TempDir()
		dir := filepath.Join(wd, "task_a")
		mkAttemptDirs(t, wd, "task_a")
		created, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !sameTaskDir(dir, created) {
			t.Error("the created directory is not recognised")
		}
		if sameTaskDir(dir, nil) {
			t.Error("nothing recorded counts as the created directory")
		}
		if err := os.Rename(dir, filepath.Join(wd, "elsewhere")); err != nil {
			t.Fatal(err)
		}
		if sameTaskDir(dir, created) {
			t.Error("a missing directory counts as the created one")
		}
		mkAttemptDirs(t, wd, "task_a")
		if sameTaskDir(dir, created) {
			t.Error("a directory that replaced the created one counts as it")
		}
	})

	markRun := func(w *Worker, id string) { w.markUnsuccessfulRun(id) }
	keepDirs := func(w *Worker, id string) {
		w.keepTaskDirs = true
		w.cleanupTaskDir(&model.Task{ID: id}, nil)
	}
	debugJob := func(w *Worker, id string) {
		w.cleanupTaskDir(&model.Task{ID: id, RuntimeHints: &model.RuntimeHints{Debug: true}}, nil)
	}
	tests := []struct {
		name       string
		replace    bool // the task dir is replaced by another directory during the run
		end        func(w *Worker, id string)
		wantReason string // "" = no marker
	}{
		{name: "failed run", end: markRun, wantReason: keptReasonFailed},
		{name: "failed run, dir replaced", replace: true, end: markRun},
		{name: "keep-task-dirs", end: keepDirs, wantReason: keptReasonKeepTaskDirs},
		{name: "keep-task-dirs, dir replaced", replace: true, end: keepDirs},
		{name: "debug submission", end: debugJob, wantReason: keptReasonDebug},
		{name: "debug submission, dir replaced", replace: true, end: debugJob},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wd := t.TempDir()
			const id = "task_a"
			w := &Worker{workDir: wd, active: newActiveTaskSet(), logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
			w.active.add(id, func() {})
			if _, err := w.prepareTaskDir(id); err != nil {
				t.Fatalf("prepareTaskDir: %v", err)
			}
			if tt.replace {
				// E.g. another worker process on the same workdir moved the
				// run's dir away and created its own in its place.
				if err := os.Rename(filepath.Join(wd, id), filepath.Join(wd, "moved")); err != nil {
					t.Fatal(err)
				}
				mkAttemptDirs(t, wd, id)
			}

			tt.end(w, id)

			m, ok := readTestMarker(t, wd, id)
			switch {
			case tt.wantReason == "" && ok:
				t.Errorf("marker %+v written for a directory this run did not create", m)
			case tt.wantReason != "" && !ok:
				t.Errorf("no marker, want reason %q", tt.wantReason)
			case ok && m.Reason != tt.wantReason:
				t.Errorf("marker reason = %q, want %q", m.Reason, tt.wantReason)
			}
			if _, err := os.Stat(filepath.Join(wd, id)); err != nil {
				t.Errorf("task dir: %v, want it kept", err)
			}
		})
	}
}

// makeUnsafeMarker creates p as a marker that must never be read: a symlink
// to a valid expired marker outside the workdir, a FIFO, a directory, or a
// valid expired marker larger than keptMarkerMaxBytes.
func makeUnsafeMarker(t *testing.T, kind, p, taskID string) {
	t.Helper()
	old := keptTestNow.Add(-1000 * time.Hour)
	valid := fmt.Sprintf(`{"task_id":%q,"reason":"failed","kept_at":%q}`, taskID, old.Format(time.RFC3339))
	switch kind {
	case "symlink":
		target := filepath.Join(t.TempDir(), "target.kept")
		if err := os.WriteFile(target, []byte(valid), 0o644); err != nil {
			t.Fatal(err)
		}
		setTestMtime(t, target, old)
		if err := os.Symlink(target, p); err != nil {
			t.Fatal(err)
		}
	case "fifo":
		if err := syscall.Mkfifo(p, 0o644); err != nil {
			t.Fatal(err)
		}
	case "directory":
		if err := os.Mkdir(p, 0o755); err != nil {
			t.Fatal(err)
		}
	case "oversized":
		big := strings.TrimSuffix(valid, "}") + `,"pad":"` + strings.Repeat("x", keptMarkerMaxBytes) + `"}`
		if err := os.WriteFile(p, []byte(big), 0o644); err != nil {
			t.Fatal(err)
		}
		setTestMtime(t, p, old)
	default:
		t.Fatalf("unknown marker kind %q", kind)
	}
}

var unsafeMarkerKinds = []string{"symlink", "fifo", "directory", "oversized"}

// withinTimeout runs f and fails the test if it does not return in time
// (e.g. blocked opening a FIFO).
func withinTimeout(t *testing.T, what string, f func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		f()
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("%s blocked", what)
	}
}

// F3: readKeptMarker refuses anything but a small regular file, without
// following a symlink or blocking on a FIFO.
func TestReadKeptMarker_Unsafe(t *testing.T) {
	for _, kind := range unsafeMarkerKinds {
		t.Run(kind, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "task_a.kept")
			makeUnsafeMarker(t, kind, p, "task_a")
			var err error
			withinTimeout(t, "readKeptMarker", func() { _, _, _, err = readKeptMarker(p) })
			if err == nil {
				t.Errorf("readKeptMarker accepted a %s marker", kind)
			}
		})
	}

	t.Run("regular file at the size limit", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "task_a.kept")
		valid := `{"task_id":"task_a","reason":"failed","kept_at":"2026-01-01T00:00:00Z"}`
		data := valid + strings.Repeat(" ", keptMarkerMaxBytes-len(valid))
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := readKeptMarker(p); err != nil {
			t.Errorf("readKeptMarker: %v", err)
		}
	})
}

// F3: the sweep skips such markers and removes nothing.
func TestSweepKeptTaskDirs_UnsafeMarkers(t *testing.T) {
	wd := t.TempDir()
	for _, kind := range unsafeMarkerKinds {
		id := "task_" + kind
		mkAttemptDirs(t, wd, id)
		makeUnsafeMarker(t, kind, keptMarkerPath(wd, id), id)
	}
	w := &Worker{
		workDir:        wd,
		keptTaskDirTTL: time.Hour,
		active:         newActiveTaskSet(),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	lockTestWorkDir(t, w)
	var removed int
	withinTimeout(t, "sweep", func() { removed = w.sweepKeptTaskDirs(context.Background(), keptTestNow) })
	if removed != 0 {
		t.Errorf("removed = %d, want 0", removed)
	}
	for _, kind := range unsafeMarkerKinds {
		id := "task_" + kind
		for _, p := range []string{filepath.Join(wd, id, "f.txt"), keptMarkerPath(wd, id)} {
			if _, err := os.Lstat(p); err != nil {
				t.Errorf("%s: %v, want it kept", p, err)
			}
		}
	}
}

// F4: a cancelled context stops the sweep before it removes anything; the
// next sweep does the work.
func TestSweepKeptTaskDirs_Cancelled(t *testing.T) {
	wd := t.TempDir()
	const tomb = ".gowe-swept-task_z-1"
	mkAttemptDirs(t, wd, "task_a", tomb)
	writeTestMarker(t, wd, "task_a", "task_a", keptReasonFailed, keptTestNow.Add(-3*time.Hour))
	w := &Worker{
		workDir:        wd,
		keptTaskDirTTL: time.Hour,
		active:         newActiveTaskSet(),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	lockTestWorkDir(t, w)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := w.sweepKeptTaskDirs(ctx, keptTestNow); got != 0 {
		t.Errorf("cancelled sweep removed %d, want 0", got)
	}
	for _, p := range []string{"task_a", "task_a.kept", tomb} {
		if _, err := os.Lstat(filepath.Join(wd, p)); err != nil {
			t.Errorf("%s after a cancelled sweep: %v, want it kept", p, err)
		}
	}

	if got := w.sweepKeptTaskDirs(context.Background(), keptTestNow); got != 1 {
		t.Errorf("removed = %d, want 1", got)
	}
	for _, p := range []string{"task_a", "task_a.kept", tomb} {
		if _, err := os.Lstat(filepath.Join(wd, p)); !os.IsNotExist(err) {
			t.Errorf("%s still exists (err=%v)", p, err)
		}
	}
}

// F4: sweeps running while other tasks start (prepareTaskDir) and end
// (markers written); run with -race.
func TestSweepKeptTaskDirs_ConcurrentTaskStarts(t *testing.T) {
	wd := t.TempDir()
	oldAt := time.Now().Add(-3 * time.Hour)
	const expired = 20
	for i := range expired {
		base := fmt.Sprintf("task_old%d", i)
		mkAttemptDirs(t, wd, base, base+"_tmp", base+"_staging")
		writeTestMarker(t, wd, base, base, keptReasonFailed, oldAt)
	}
	w := &Worker{
		workDir:        wd,
		keptTaskDirTTL: time.Hour,
		active:         newActiveTaskSet(),
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	lockTestWorkDir(t, w)

	const starters, perStarter = 4, 10
	start := make(chan struct{})
	var wg sync.WaitGroup
	removedCh := make(chan int, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		n := 0
		for range 3 {
			n += w.sweepKeptTaskDirs(context.Background(), time.Now())
		}
		removedCh <- n
	}()
	for g := range starters {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := range perStarter {
				id := fmt.Sprintf("task_new%d_%d", g, j)
				w.active.add(id, func() {})
				if _, err := w.prepareTaskDir(id); err != nil {
					t.Errorf("prepareTaskDir(%s): %v", id, err)
				}
				w.markUnsuccessfulRun(id) // a fresh marker, never swept
				w.active.remove(id)
			}
		}()
	}
	close(start)
	wg.Wait()

	if n := <-removedCh; n != expired {
		t.Errorf("removed = %d, want %d", n, expired)
	}
	for i := range expired {
		base := fmt.Sprintf("task_old%d", i)
		for _, p := range []string{base, base + "_tmp", base + "_staging", base + keptMarkerSuffix} {
			if _, err := os.Lstat(filepath.Join(wd, p)); !os.IsNotExist(err) {
				t.Errorf("%s still exists (err=%v)", p, err)
			}
		}
	}
	for g := range starters {
		for j := range perStarter {
			id := fmt.Sprintf("task_new%d_%d", g, j)
			if _, err := os.Stat(filepath.Join(wd, id)); err != nil {
				t.Errorf("task dir %s: %v", id, err)
			}
			if _, ok := readTestMarker(t, wd, id); !ok {
				t.Errorf("no marker for %s", id)
			}
		}
	}
	for _, n := range dirEntries(t, wd) {
		if isSweptName(n) {
			t.Errorf("moved-aside directory left behind: %s", n)
		}
	}
}

func TestValidateKeptTaskDirTTL(t *testing.T) {
	tests := []struct {
		ttl     time.Duration
		wantErr string // substring, "" = valid
	}{
		{ttl: 0},
		{ttl: time.Hour},
		{ttl: 720 * time.Hour},
		{ttl: -time.Second, wantErr: "must not be negative"},
		{ttl: 30 * time.Minute, wantErr: "must be at least 1h"},
		{ttl: time.Hour - time.Second, wantErr: "must be at least 1h"},
	}
	for _, tt := range tests {
		t.Run(tt.ttl.String(), func(t *testing.T) {
			err := ValidateKeptTaskDirTTL(tt.ttl)
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// A marker for one of the worker's own dot-files (lock files, tombstones)
// is never valid: sweeping the owner lock file would void the lock.
func TestValidKeptBase_RejectsDotNames(t *testing.T) {
	for _, name := range []string{".gowe-worker.lock", ".gowe-worker.shared", ".gowe-swept-x-1", ".hidden"} {
		if validKeptBase(name, name) {
			t.Errorf("validKeptBase(%q, %q) = true, want false", name, name)
		}
		if validKeptBase(name+".attempt-1", name) {
			t.Errorf("validKeptBase(%q, %q) = true, want false", name+".attempt-1", name)
		}
	}
	if !validKeptBase("task_a", "task_a") || !validKeptBase("task_a.attempt-2", "task_a") {
		t.Error("ordinary task names must stay valid")
	}
}
