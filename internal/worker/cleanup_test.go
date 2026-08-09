package worker

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/model"
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
		for _, d := range []string{taskDir, tmpDir} {
			if err := os.MkdirAll(d, 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(filepath.Join(taskDir, "out.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return taskDir, tmpDir
	}

	t.Run("removes task dir and tmp sibling after staged outputs", func(t *testing.T) {
		workDir := t.TempDir()
		taskDir, tmpDir := mkTaskDirs(t, workDir, "task_1")
		w := newWorker(workDir, false)

		w.cleanupTaskDir(&model.Task{ID: "task_1"}, map[string]any{
			"out": map[string]any{"class": "File", "location": "file:///shared/task_1/out.txt"},
		})

		for _, d := range []string{taskDir, tmpDir} {
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
