package worker

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
)

// mkAttemptDirs creates each named directory below workDir holding a file
// "f.txt" whose content is the directory's name.
func mkAttemptDirs(t *testing.T, workDir string, names ...string) {
	t.Helper()
	for _, n := range names {
		d := filepath.Join(workDir, n)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "f.txt"), []byte(n), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// dirEntries returns the names of workDir's entries.
func dirEntries(t *testing.T, workDir string) []string {
	t.Helper()
	ents, err := os.ReadDir(workDir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

func TestArchivePreviousAttempt(t *testing.T) {
	tests := []struct {
		name         string
		existing     []string          // dirs created before the call
		wantArchived string            // base name, "" = nothing archived
		wantMoved    map[string]string // original name -> archived name
		wantAbsent   []string          // must not exist afterwards
		wantEntries  int               // number of entries in workDir afterwards
	}{
		{
			name:        "nothing exists",
			wantEntries: 0,
		},
		{
			name:         "all three exist",
			existing:     []string{"task_a", "task_a_tmp", "task_a_staging"},
			wantArchived: "task_a.attempt-1",
			wantMoved: map[string]string{
				"task_a":         "task_a.attempt-1",
				"task_a_tmp":     "task_a.attempt-1_tmp",
				"task_a_staging": "task_a.attempt-1_staging",
			},
			wantAbsent:  []string{"task_a", "task_a_tmp", "task_a_staging"},
			wantEntries: 3,
		},
		{
			name:         "only task dir and staging exist",
			existing:     []string{"task_a", "task_a_staging"},
			wantArchived: "task_a.attempt-1",
			wantMoved: map[string]string{
				"task_a":         "task_a.attempt-1",
				"task_a_staging": "task_a.attempt-1_staging",
			},
			wantAbsent:  []string{"task_a", "task_a_tmp", "task_a_staging", "task_a.attempt-1_tmp"},
			wantEntries: 2,
		},
		{
			name:         "attempt-1 task dir taken",
			existing:     []string{"task_a", "task_a.attempt-1"},
			wantArchived: "task_a.attempt-2",
			wantMoved:    map[string]string{"task_a": "task_a.attempt-2"},
			wantAbsent:   []string{"task_a"},
			wantEntries:  2,
		},
		{
			name:         "attempt-1 tmp taken",
			existing:     []string{"task_a", "task_a.attempt-1_tmp"},
			wantArchived: "task_a.attempt-2",
			wantMoved:    map[string]string{"task_a": "task_a.attempt-2"},
			wantAbsent:   []string{"task_a", "task_a.attempt-1"},
			wantEntries:  2,
		},
		{
			name:         "attempt-1 staging taken",
			existing:     []string{"task_a_tmp", "task_a.attempt-1_staging"},
			wantArchived: "task_a.attempt-2",
			wantMoved:    map[string]string{"task_a_tmp": "task_a.attempt-2_tmp"},
			wantAbsent:   []string{"task_a_tmp", "task_a.attempt-1", "task_a.attempt-1_tmp"},
			wantEntries:  2,
		},
		{
			name:        "other task with same prefix untouched, nothing of own",
			existing:    []string{"task_ab", "task_ab_tmp", "task_ab_staging"},
			wantEntries: 3,
		},
		{
			name: "other task with same prefix untouched",
			existing: []string{
				"task_a", "task_a_tmp",
				"task_ab", "task_ab_tmp", "task_ab_staging",
			},
			wantArchived: "task_a.attempt-1",
			wantMoved: map[string]string{
				"task_a":     "task_a.attempt-1",
				"task_a_tmp": "task_a.attempt-1_tmp",
			},
			wantAbsent:  []string{"task_a", "task_a_tmp", "task_ab.attempt-1"},
			wantEntries: 5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workDir := t.TempDir()
			mkAttemptDirs(t, workDir, tt.existing...)

			got, err := archivePreviousAttempt(workDir, "task_a")
			if err != nil {
				t.Fatalf("archivePreviousAttempt: %v", err)
			}
			want := ""
			if tt.wantArchived != "" {
				want = filepath.Join(workDir, tt.wantArchived)
			}
			if got != want {
				t.Errorf("archived = %q, want %q", got, want)
			}

			for from, to := range tt.wantMoved {
				b, err := os.ReadFile(filepath.Join(workDir, to, "f.txt"))
				if err != nil {
					t.Errorf("%s not moved to %s: %v", from, to, err)
					continue
				}
				if string(b) != from {
					t.Errorf("%s/f.txt = %q, want %q", to, b, from)
				}
			}
			for _, n := range tt.wantAbsent {
				if _, err := os.Lstat(filepath.Join(workDir, n)); !os.IsNotExist(err) {
					t.Errorf("%s exists after archiving (err=%v)", n, err)
				}
			}
			// Pre-existing dirs that were not moved keep their contents.
			for _, n := range tt.existing {
				if _, moved := tt.wantMoved[n]; moved {
					continue
				}
				b, err := os.ReadFile(filepath.Join(workDir, n, "f.txt"))
				if err != nil || string(b) != n {
					t.Errorf("%s was touched: %q, %v", n, b, err)
				}
			}
			if ents := dirEntries(t, workDir); len(ents) != tt.wantEntries {
				t.Errorf("workDir entries = %v, want %d", ents, tt.wantEntries)
			}
		})
	}
}

// TestRunTask_RerunStartsInFreshTaskDir: a re-run of a task whose earlier
// attempt already staged its InitialWorkDirRequirement file must not fail with
// "file exists"; the earlier attempt's dir is kept as "<id>.attempt-1".
func TestRunTask_RerunStartsInFreshTaskDir(t *testing.T) {
	var logs bytes.Buffer
	w := newCWLTestWorker(t, "", &logs)
	w.containerRuntime = "none"

	inDir := t.TempDir()
	input := filepath.Join(inDir, "input.txt")
	if err := os.WriteFile(input, []byte("hello-from-input"), 0o644); err != nil {
		t.Fatal(err)
	}

	// What the first attempt left: the IWDR symlink in the task dir.
	const taskID = "task_rerun"
	oldDir := filepath.Join(w.workDir, taskID)
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(input, filepath.Join(oldDir, "in.txt")); err != nil {
		t.Fatal(err)
	}

	task := &model.Task{
		ID: taskID,
		Tool: map[string]any{
			"class": "CommandLineTool", "cwlVersion": "v1.2",
			"baseCommand": []any{"cat", "in.txt"},
			"inputs":      map[string]any{"f": map[string]any{"type": "File"}},
			"outputs":     map[string]any{},
			"requirements": map[string]any{
				"InitialWorkDirRequirement": map[string]any{
					"listing": []any{
						map[string]any{"entryname": "in.txt", "entry": "$(inputs.f)"},
					},
				},
			},
		},
		Job: map[string]any{
			"f": map[string]any{
				"class":    "File",
				"location": "file://" + input,
				"path":     input,
				"basename": "input.txt",
			},
		},
	}

	res := runCWLTask(t, w, task)
	if res.State != model.TaskStateSuccess {
		t.Fatalf("state=%q stderr=%q", res.State, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hello-from-input") {
		t.Errorf("stdout = %q, want the input's content", res.Stdout)
	}

	kept := filepath.Join(w.workDir, taskID+".attempt-1", "in.txt")
	target, err := os.Readlink(kept)
	if err != nil {
		t.Fatalf("earlier attempt's file not kept: %v", err)
	}
	if target != input {
		t.Errorf("kept symlink -> %q, want %q", target, input)
	}
	if !strings.Contains(logs.String(), "previous attempt's task dir kept") {
		t.Errorf("missing INFO log: %s", logs.String())
	}
}

// A second rerun archives to the next free number and keeps the first archive.
func TestArchivePreviousAttempt_SecondRerun(t *testing.T) {
	wd := t.TempDir()
	for i, want := range []string{"task_x.attempt-1", "task_x.attempt-2"} {
		dir := filepath.Join(wd, "task_x")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		marker := filepath.Join(dir, "run"+strconv.Itoa(i))
		if err := os.WriteFile(marker, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := archivePreviousAttempt(wd, "task_x")
		if err != nil {
			t.Fatal(err)
		}
		if got != filepath.Join(wd, want) {
			t.Fatalf("archived = %q, want %q", got, filepath.Join(wd, want))
		}
		if _, err := os.Stat(filepath.Join(wd, want, "run"+strconv.Itoa(i))); err != nil {
			t.Errorf("attempt %d content not kept: %v", i+1, err)
		}
	}
	if _, err := os.Stat(filepath.Join(wd, "task_x.attempt-1", "run0")); err != nil {
		t.Errorf("first archive disturbed by the second: %v", err)
	}
}

// A failed rename is an error, and nothing is deleted.
func TestArchivePreviousAttempt_RenameFails(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	wd := t.TempDir()
	dir := filepath.Join(wd, "task_x")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wd, 0o555); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(wd, 0o755)
	if _, err := archivePreviousAttempt(wd, "task_x"); err == nil {
		t.Fatal("want an error when the work dir is not writable")
	}
	if _, err := os.Stat(filepath.Join(dir, "keep")); err != nil {
		t.Errorf("original lost after a failed rename: %v", err)
	}
}
