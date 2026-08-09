package worker

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

func TestOutputsReferenceDir(t *testing.T) {
	dir := "/work/task_abc"

	tests := []struct {
		name    string
		outputs map[string]any
		want    bool
	}{
		{
			name: "staged file elsewhere",
			outputs: map[string]any{
				"out": map[string]any{
					"class":    "File",
					"path":     "/work/task_abc/result.txt", // stale local path is ignored
					"location": "file:///shared/task_abc/result.txt",
				},
			},
			want: false,
		},
		{
			name: "in-place stage-out location",
			outputs: map[string]any{
				"out": map[string]any{
					"class":    "File",
					"location": "file:///work/task_abc/result.txt",
				},
			},
			want: true,
		},
		{
			name: "failed stage-out leaves bare path",
			outputs: map[string]any{
				"out": map[string]any{
					"class": "File",
					"path":  "/work/task_abc/result.txt",
				},
			},
			want: true,
		},
		{
			name: "shock location",
			outputs: map[string]any{
				"out": map[string]any{
					"class":    "File",
					"location": "shock://shock.example.org/node/123",
				},
			},
			want: false,
		},
		{
			name: "secondary file in place",
			outputs: map[string]any{
				"out": map[string]any{
					"class":    "File",
					"location": "file:///shared/task_abc/result.bam",
					"secondaryFiles": []any{
						map[string]any{
							"class":    "File",
							"location": "file:///work/task_abc/result.bam.bai",
						},
					},
				},
			},
			want: true,
		},
		{
			name: "directory listing staged elsewhere",
			outputs: map[string]any{
				"out": map[string]any{
					"class":    "Directory",
					"location": "file:///shared/task_abc/outdir",
					"listing": []any{
						map[string]any{
							"class":    "File",
							"path":     "/work/task_abc/outdir/a.txt",
							"location": "file:///shared/task_abc/outdir/a.txt",
						},
					},
				},
			},
			want: false,
		},
		{
			name: "legacy string location in dir",
			outputs: map[string]any{
				"out": "file:///work/task_abc/result.txt",
			},
			want: true,
		},
		{
			name: "legacy string slice staged elsewhere",
			outputs: map[string]any{
				"out": []string{"file:///shared/task_abc/a", "file:///shared/task_abc/b"},
			},
			want: false,
		},
		{
			name: "prefix must be a path boundary",
			outputs: map[string]any{
				"out": map[string]any{
					"class":    "File",
					"location": "file:///work/task_abc_output/result.txt",
				},
			},
			want: false,
		},
		{
			name: "array of files one in place",
			outputs: map[string]any{
				"out": []any{
					map[string]any{"class": "File", "location": "file:///shared/x"},
					map[string]any{"class": "File", "location": "file:///work/task_abc/y"},
				},
			},
			want: true,
		},
		{
			name:    "nil and scalars",
			outputs: map[string]any{"count": 3, "flag": true, "none": nil},
			want:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := outputsReferenceDir(tt.outputs, dir); got != tt.want {
				t.Errorf("outputsReferenceDir() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCleanupTaskDir(t *testing.T) {
	newWorker := func(workDir string, keep bool) *Worker {
		return &Worker{
			workDir:      workDir,
			keepTaskDirs: keep,
			logger:       slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		}
	}

	mkTaskDirs := func(t *testing.T, workDir, taskID string) (string, string) {
		t.Helper()
		taskDir := filepath.Join(workDir, taskID)
		tmpDir := taskDir + "_tmp"
		for _, d := range []string{taskDir, tmpDir, taskDir + "_staging"} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(taskDir, "out.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return taskDir, tmpDir
	}

	t.Run("removes task dir and its siblings after staged outputs", func(t *testing.T) {
		workDir := t.TempDir()
		taskDir, tmpDir := mkTaskDirs(t, workDir, "task_1")
		w := newWorker(workDir, false)

		w.cleanupTaskDir(&model.Task{ID: "task_1"}, map[string]any{
			"out": map[string]any{"class": "File", "location": "file:///shared/task_1/out.txt"},
		})

		for _, d := range []string{taskDir, tmpDir, taskDir + "_staging"} {
			if _, err := os.Stat(d); !os.IsNotExist(err) {
				t.Errorf("%s still exists after cleanup", d)
			}
		}
	})

	t.Run("keeps dir when outputs are in place", func(t *testing.T) {
		workDir := t.TempDir()
		taskDir, _ := mkTaskDirs(t, workDir, "task_2")
		w := newWorker(workDir, false)

		w.cleanupTaskDir(&model.Task{ID: "task_2"}, map[string]any{
			"out": map[string]any{"class": "File", "location": "file://" + filepath.Join(taskDir, "out.txt")},
		})

		if _, err := os.Stat(filepath.Join(taskDir, "out.txt")); err != nil {
			t.Errorf("in-place output was deleted: %v", err)
		}
	})

	t.Run("keeps dir with keep-task-dirs", func(t *testing.T) {
		workDir := t.TempDir()
		taskDir, _ := mkTaskDirs(t, workDir, "task_3")
		w := newWorker(workDir, true)

		w.cleanupTaskDir(&model.Task{ID: "task_3"}, map[string]any{
			"out": map[string]any{"class": "File", "location": "file:///shared/task_3/out.txt"},
		})

		if _, err := os.Stat(taskDir); err != nil {
			t.Errorf("task dir was deleted despite keep-task-dirs: %v", err)
		}
	})

	t.Run("keeps dir for debug submission", func(t *testing.T) {
		workDir := t.TempDir()
		taskDir, _ := mkTaskDirs(t, workDir, "task_5")
		w := newWorker(workDir, false)

		w.cleanupTaskDir(&model.Task{
			ID:           "task_5",
			RuntimeHints: &model.RuntimeHints{Debug: true},
		}, map[string]any{
			"out": map[string]any{"class": "File", "location": "file:///shared/task_5/out.txt"},
		})

		if _, err := os.Stat(taskDir); err != nil {
			t.Errorf("task dir was deleted despite debug submission: %v", err)
		}
	})

	t.Run("symlink targets survive cleanup", func(t *testing.T) {
		workDir := t.TempDir()
		shared := t.TempDir()
		taskDir, _ := mkTaskDirs(t, workDir, "task_4")

		target := filepath.Join(shared, "input.dat")
		if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(taskDir, "input.dat")); err != nil {
			t.Fatal(err)
		}

		w := newWorker(workDir, false)
		w.cleanupTaskDir(&model.Task{ID: "task_4"}, map[string]any{
			"out": map[string]any{"class": "File", "location": "file:///shared/task_4/out.txt"},
		})

		if _, err := os.Stat(taskDir); !os.IsNotExist(err) {
			t.Errorf("task dir still exists after cleanup")
		}
		if _, err := os.Stat(target); err != nil {
			t.Errorf("symlink target was deleted: %v", err)
		}
	})
}

// TestExecuteTaskCleanupWiring exercises the legacy execute path end-to-end
// against a stub server, verifying cleanup fires only when the SUCCESS result
// was actually accepted.
func TestExecuteTaskCleanupWiring(t *testing.T) {
	newTestWorker := func(t *testing.T, serverURL string) (*Worker, string) {
		t.Helper()
		workDir := t.TempDir()
		return &Worker{
			client:  NewClient(serverURL, nil),
			runtime: NewBareRuntime(),
			workDir: workDir,
			active:  newActiveTaskSet(),
			logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		}, workDir
	}

	legacyTask := func(id string, cmd ...string) *model.Task {
		args := make([]any, len(cmd))
		for i, c := range cmd {
			args[i] = c
		}
		return &model.Task{ID: id, Inputs: map[string]any{"_base_command": args}}
	}

	t.Run("success accepted by server removes dir", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		w, workDir := newTestWorker(t, srv.URL)

		if err := w.executeTask(context.Background(), legacyTask("task_ok", "true")); err != nil {
			t.Fatalf("executeTask: %v", err)
		}
		if _, err := os.Stat(filepath.Join(workDir, "task_ok")); !os.IsNotExist(err) {
			t.Error("task dir still exists after accepted SUCCESS report")
		}
	})

	t.Run("rejected report keeps dir", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()
		w, workDir := newTestWorker(t, srv.URL)

		if err := w.executeTask(context.Background(), legacyTask("task_rej", "true")); err == nil {
			t.Fatal("expected error from rejected report")
		}
		if _, err := os.Stat(filepath.Join(workDir, "task_rej")); err != nil {
			t.Errorf("task dir was deleted despite rejected report: %v", err)
		}
	})

	t.Run("failed task keeps dir", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()
		w, workDir := newTestWorker(t, srv.URL)

		if err := w.executeTask(context.Background(), legacyTask("task_fail", "false")); err != nil {
			t.Fatalf("executeTask: %v", err)
		}
		if _, err := os.Stat(filepath.Join(workDir, "task_fail")); err != nil {
			t.Errorf("failed task's dir was deleted: %v", err)
		}
	})
}

// failingStager errors on every StageOut, simulating an unreachable staging
// destination.
type failingStager struct{}

func (failingStager) StageIn(context.Context, string, string, staging.StageOptions) error {
	return errors.New("stager down")
}
func (failingStager) StageOut(context.Context, string, string, staging.StageOptions) (string, error) {
	return "", errors.New("stager down")
}
func (failingStager) Supports(string) bool { return true }

// fixedStager reports every file staged to a fixed remote location.
type fixedStager struct{ loc string }

func (fixedStager) StageIn(context.Context, string, string, staging.StageOptions) error { return nil }
func (s fixedStager) StageOut(context.Context, string, string, staging.StageOptions) (string, error) {
	return s.loc, nil
}
func (fixedStager) Supports(string) bool { return true }

// TestLegacyStageOutFailureKeepsData: when stage-out fails on the legacy path,
// the local location must be reported (so the guard retains the only copy)
// rather than the output being silently dropped and the dir deleted.
func TestLegacyStageOutFailureKeepsData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	workDir := t.TempDir()
	w := &Worker{
		client:  NewClient(srv.URL, nil),
		runtime: NewBareRuntime(),
		stager:  failingStager{},
		workDir: workDir,
		active:  newActiveTaskSet(),
		logger:  slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}

	task := &model.Task{ID: "task_so", Inputs: map[string]any{
		"_base_command": []any{"sh", "-c", "echo data > out.txt"},
		"_output_globs": map[string]any{"out": "out.txt"},
	}}
	if err := w.executeTask(context.Background(), task); err != nil {
		t.Fatalf("executeTask: %v", err)
	}

	outFile := filepath.Join(workDir, "task_so", "out.txt")
	if _, err := os.Stat(outFile); err != nil {
		t.Fatalf("only copy of output was deleted after failed stage-out: %v", err)
	}
}

// TestStageOutputValueStripsStalePath: once an output is staged to a different
// location, the stale local path/dirname must be dropped so server-side
// consumers resolve via the staged location; in-place stage-out keeps them.
func TestStageOutputValueStripsStalePath(t *testing.T) {
	w := &Worker{
		logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	task := &model.Task{ID: "task_sp"}

	t.Run("moved output loses stale path", func(t *testing.T) {
		val := map[string]any{
			"class":   "File",
			"path":    "/work/task_sp/out.txt",
			"dirname": "/work/task_sp",
		}
		got := w.stageOutputValue(context.Background(), val, task,
			fixedStager{loc: "file:///shared/task_sp/out.txt"}, "").(map[string]any)

		if got["location"] != "file:///shared/task_sp/out.txt" {
			t.Errorf("location = %v", got["location"])
		}
		if _, ok := got["path"]; ok {
			t.Error("stale path survived staging to a different location")
		}
		if _, ok := got["dirname"]; ok {
			t.Error("stale dirname survived staging to a different location")
		}
	})

	t.Run("in-place output keeps path", func(t *testing.T) {
		val := map[string]any{
			"class": "File",
			"path":  "/work/task_sp/out.txt",
		}
		got := w.stageOutputValue(context.Background(), val, task,
			fixedStager{loc: "file:///work/task_sp/out.txt"}, "").(map[string]any)

		if got["path"] != "/work/task_sp/out.txt" {
			t.Errorf("in-place path was stripped: %v", got["path"])
		}
	})
}
