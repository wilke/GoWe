package worker

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

func quietWorker(t *testing.T) *Worker {
	t.Helper()
	return &Worker{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), workDir: t.TempDir()}
}

func writeOut(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileObj(p string) map[string]any {
	return map[string]any{"class": "File", "path": p, "location": "file://" + p, "basename": filepath.Base(p), "dirname": filepath.Dir(p)}
}

func readStaged(t *testing.T, loc string) string {
	t.Helper()
	b, err := os.ReadFile(strings.TrimPrefix(loc, "file://"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// recordingStager records the options each StageOut/StageOutDir call received.
type recordingStager struct {
	inner *staging.FileStager
	files []staging.StageOptions
	dirs  []staging.StageOptions
}

func (r *recordingStager) StageIn(ctx context.Context, l, d string, o staging.StageOptions) error {
	return r.inner.StageIn(ctx, l, d, o)
}
func (r *recordingStager) StageOut(ctx context.Context, s, id string, o staging.StageOptions) (string, error) {
	r.files = append(r.files, o)
	return r.inner.StageOut(ctx, s, id, o)
}
func (r *recordingStager) StageOutDir(ctx context.Context, s, id string, o staging.StageOptions) (string, error) {
	r.dirs = append(r.dirs, o)
	return r.inner.StageOutDir(ctx, s, id, o)
}
func (r *recordingStager) Supports(s string) bool { return r.inner.Supports(s) }

func TestOutputStager_RelPath(t *testing.T) {
	w := quietWorker(t)
	s := w.newOutputStager(context.Background(), &model.Task{ID: "t1"}, nil, "/w/t1")
	tests := []struct {
		name, p, sub, base, want string
	}{
		{"inside", "/w/t1/a/b/f.js", "", "", "a/b/f.js"},
		{"top level inside", "/w/t1/f.js", "", "", "f.js"},
		{"top-level outside", "/elsewhere/f.js", "", "", "f.js"},
		{"nested outside", "/elsewhere/f.js", "d/s", "", "d/s/f.js"},
		{"basename override", "/elsewhere/f.js", "d", "g.js", "d/g.js"},
		{"outdir itself", "/w/t1", "x", "", "x/t1"},
		{"sibling prefix dir", "/w/t10/f", "", "", "f"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := s.relPath(tc.p, tc.sub, tc.base); got != tc.want {
				t.Fatalf("relPath = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOutputStager_NestedDirectoryDuplicateBasenames(t *testing.T) {
	w := quietWorker(t)
	stage := t.TempDir()
	task := &model.Task{ID: "t1"}
	taskDir := filepath.Join(w.workDir, task.ID)
	fa := filepath.Join(taskDir, "s", "x", "reactable.js")
	fb := filepath.Join(taskDir, "s", "y", "reactable.js")
	writeOut(t, fa, "AAA")
	writeOut(t, fb, "BBB")

	dir := map[string]any{
		"class": "Directory", "path": filepath.Join(taskDir, "s"), "basename": "s",
		"location": "file://" + filepath.Join(taskDir, "s"),
		"listing": []any{
			map[string]any{"class": "Directory", "path": filepath.Join(taskDir, "s", "x"), "basename": "x", "listing": []any{fileObj(fa)}},
			map[string]any{"class": "Directory", "path": filepath.Join(taskDir, "s", "y"), "basename": "y", "listing": []any{fileObj(fb)}},
		},
	}
	st := staging.NewFileStager("file://" + stage)
	out := w.newOutputStager(context.Background(), task, st, taskDir).stage(dir, "").(map[string]any)

	if got := out["location"]; got != "file://"+filepath.Join(stage, "t1", "s") {
		t.Fatalf("dir location = %v", got)
	}
	if _, ok := out["path"]; ok {
		t.Error("directory path not removed")
	}
	listing := out["listing"].([]any)
	la := listing[0].(map[string]any)["listing"].([]any)[0].(map[string]any)
	lb := listing[1].(map[string]any)["listing"].([]any)[0].(map[string]any)
	if la["location"] != "file://"+filepath.Join(stage, "t1", "s", "x", "reactable.js") ||
		lb["location"] != "file://"+filepath.Join(stage, "t1", "s", "y", "reactable.js") {
		t.Fatalf("locations: %v %v", la["location"], lb["location"])
	}
	if readStaged(t, la["location"].(string)) != "AAA" || readStaged(t, lb["location"].(string)) != "BBB" {
		t.Error("staged contents mixed up")
	}
	if outputsReferenceDir(out, taskDir) {
		t.Error("staged outputs still reference the task dir")
	}
}

func TestOutputStager_GlobFromSubdirs(t *testing.T) {
	w := quietWorker(t)
	stage := t.TempDir()
	task := &model.Task{ID: "t1"}
	taskDir := filepath.Join(w.workDir, task.ID)
	f1, f2 := filepath.Join(taskDir, "d", "a", "f"), filepath.Join(taskDir, "d", "b", "f")
	writeOut(t, f1, "1")
	writeOut(t, f2, "2")
	files := []any{fileObj(f1), fileObj(f2)}
	st := staging.NewFileStager("file://" + stage)
	out := w.newOutputStager(context.Background(), task, st, taskDir).stage(files, "").([]any)
	if readStaged(t, out[0].(map[string]any)["location"].(string)) != "1" || readStaged(t, out[1].(map[string]any)["location"].(string)) != "2" {
		t.Fatal("contents wrong")
	}
	if out[0].(map[string]any)["location"] == out[1].(map[string]any)["location"] {
		t.Fatal("same location for different files")
	}
}

func TestOutputStager_DirectoryWithoutListingIsCopied(t *testing.T) {
	w := quietWorker(t)
	stage := t.TempDir()
	task := &model.Task{ID: "t1"}
	taskDir := filepath.Join(w.workDir, task.ID)
	writeOut(t, filepath.Join(taskDir, "d", "sub", "f"), "F")
	dir := map[string]any{"class": "Directory", "path": filepath.Join(taskDir, "d"), "basename": "d", "location": "file://" + filepath.Join(taskDir, "d")}
	st := staging.NewFileStager("file://" + stage)
	out := w.newOutputStager(context.Background(), task, st, taskDir).stage(dir, "").(map[string]any)
	loc := out["location"].(string)
	if loc != "file://"+filepath.Join(stage, "t1", "d") {
		t.Fatalf("location = %s", loc)
	}
	if readStaged(t, loc+"/sub/f") != "F" {
		t.Fatal("tree not copied")
	}
	if outputsReferenceDir(out, taskDir) {
		t.Error("still references task dir")
	}
}

func TestOutputStager_ShallowListingSubdirsCopied(t *testing.T) {
	w := quietWorker(t)
	stage := t.TempDir()
	task := &model.Task{ID: "t1"}
	taskDir := filepath.Join(w.workDir, task.ID)
	writeOut(t, filepath.Join(taskDir, "d", "top"), "T")
	writeOut(t, filepath.Join(taskDir, "d", "sub", "deep", "f"), "D")
	dir := map[string]any{
		"class": "Directory", "path": filepath.Join(taskDir, "d"), "basename": "d",
		"listing": []any{
			fileObj(filepath.Join(taskDir, "d", "top")),
			map[string]any{"class": "Directory", "path": filepath.Join(taskDir, "d", "sub"), "basename": "sub"},
		},
	}
	st := staging.NewFileStager("file://" + stage)
	w.newOutputStager(context.Background(), task, st, taskDir).stage(dir, "")
	if readStaged(t, "file://"+filepath.Join(stage, "t1", "d", "top")) != "T" ||
		readStaged(t, "file://"+filepath.Join(stage, "t1", "d", "sub", "deep", "f")) != "D" {
		t.Fatal("shallow listing contents not staged")
	}
}

func TestOutputStager_SymlinkedOutputCopiesContent(t *testing.T) {
	w := quietWorker(t)
	stage := t.TempDir()
	task := &model.Task{ID: "t1"}
	taskDir := filepath.Join(w.workDir, task.ID)
	real := filepath.Join(t.TempDir(), "real.txt")
	writeOut(t, real, "REAL")
	link := filepath.Join(taskDir, "in", "link.txt")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	st := staging.NewFileStager("file://" + stage)
	out := w.newOutputStager(context.Background(), task, st, taskDir).stage(fileObj(link), "").(map[string]any)
	loc := out["location"].(string)
	if loc != "file://"+filepath.Join(stage, "t1", "in", "link.txt") {
		t.Fatalf("location = %s", loc)
	}
	if fi, _ := os.Lstat(strings.TrimPrefix(loc, "file://")); fi.Mode()&os.ModeSymlink != 0 {
		t.Error("staged file is a symlink")
	}
	if readStaged(t, loc) != "REAL" {
		t.Error("content not copied")
	}
}

func TestOutputStager_SameSourceStagedOnce(t *testing.T) {
	w := quietWorker(t)
	task := &model.Task{ID: "t1"}
	taskDir := filepath.Join(w.workDir, task.ID)
	f := filepath.Join(taskDir, "d", "x")
	writeOut(t, f, "X")
	rec := &recordingStager{inner: staging.NewFileStager("file://" + t.TempDir())}
	s := w.newOutputStager(context.Background(), task, rec, taskDir)
	a := s.stage(fileObj(f), "").(map[string]any)
	b := s.stage(fileObj(f), "").(map[string]any)
	if len(rec.files) != 1 {
		t.Fatalf("StageOut calls = %d, want 1", len(rec.files))
	}
	if a["location"] != b["location"] {
		t.Fatalf("locations differ: %v %v", a["location"], b["location"])
	}
	if s.err() != nil {
		t.Fatalf("unexpected error: %v", s.err())
	}
}

func TestOutputStager_DistinctSourcesSameDestFails(t *testing.T) {
	w := quietWorker(t)
	task := &model.Task{ID: "t1"}
	taskDir := filepath.Join(w.workDir, task.ID)
	// Two top-level outputs from outside the output directory, same basename.
	ext := t.TempDir()
	e1, e2 := filepath.Join(ext, "one", "r.txt"), filepath.Join(ext, "two", "r.txt")
	writeOut(t, e1, "1")
	writeOut(t, e2, "2")
	stage := t.TempDir()
	s := w.newOutputStager(context.Background(), task, staging.NewFileStager("file://"+stage), taskDir)
	s.stage(fileObj(e1), "")
	second := s.stage(fileObj(e2), "").(map[string]any)
	err := s.err()
	if err == nil {
		t.Fatal("expected collision error")
	}
	for _, want := range []string{"stage-out: destination r.txt claimed by both", e1, e2} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
	if second["path"] != e2 {
		t.Error("colliding file should keep its local path")
	}
	if got := readStaged(t, "file://"+filepath.Join(stage, "t1", "r.txt")); got != "1" {
		t.Errorf("first file overwritten: %q", got)
	}
}

func TestOutputStager_WSDestinationUnchanged(t *testing.T) {
	w := quietWorker(t)
	task := &model.Task{ID: "t1", RuntimeHints: &model.RuntimeHints{OutputDestination: "ws:///u@bvbrc/home/res/"}}
	taskDir := filepath.Join(w.workDir, task.ID)
	f := filepath.Join(taskDir, "s", "x", "f.js")
	writeOut(t, f, "x")
	dir := map[string]any{
		"class": "Directory", "path": filepath.Join(taskDir, "s"), "basename": "s",
		"listing": []any{map[string]any{"class": "Directory", "path": filepath.Join(taskDir, "s", "x"), "basename": "x", "listing": []any{fileObj(f)}}},
	}
	// Composite with a ws handler that cannot stage directories.
	rec := &recordingStager{inner: staging.NewFileStager("file://" + t.TempDir())}
	c := staging.NewCompositeStager(map[string]staging.Stager{"ws": plainWS{rec}}, rec)
	out := w.newOutputStager(context.Background(), task, c, taskDir).stage(dir, "").(map[string]any)
	if _, ok := out["path"]; !ok {
		t.Error("ws-routed Directory must stay untouched")
	}
	if out["location"] != nil {
		t.Errorf("ws-routed Directory location rewritten: %v", out["location"])
	}
	if got := plainWSDest; len(got) != 1 || got[0] != "ws:///u@bvbrc/home/res/s/x" {
		t.Fatalf("ws destination metadata = %v", got)
	}
}

var plainWSDest []string

// plainWS is a ws:// handler that is not a DirectoryStager.
type plainWS struct{ rec *recordingStager }

func (p plainWS) StageIn(context.Context, string, string, staging.StageOptions) error { return nil }
func (p plainWS) StageOut(_ context.Context, _ string, _ string, o staging.StageOptions) (string, error) {
	plainWSDest = append(plainWSDest, o.Metadata["destination"])
	return "ws:///stored", nil
}
func (p plainWS) Supports(string) bool { return true }

func TestOutputStager_DotDotRelPathRejectedByStager(t *testing.T) {
	w := quietWorker(t)
	task := &model.Task{ID: "t1"}
	taskDir := filepath.Join(w.workDir, task.ID)
	f := filepath.Join(t.TempDir(), "evil")
	writeOut(t, f, "x")
	stage := t.TempDir()
	obj := fileObj(f)
	obj["basename"] = ".."
	s := w.newOutputStager(context.Background(), task, staging.NewFileStager("file://"+stage), taskDir)
	out := s.stage(obj, "").(map[string]any)
	if out["location"] != "file://"+f {
		t.Errorf("unsafe output should stay local, location = %v", out["location"])
	}
	if _, err := os.Stat(filepath.Join(stage, "evil")); err == nil {
		t.Error("file escaped the task root")
	}
}

// Collision in a full run fails the task and keeps the task dir.
func TestExecuteWithCWLTool_StageOutLayoutAndCollision(t *testing.T) {
	treeTool := func(shell string, outputs map[string]any) map[string]any {
		return map[string]any{
			"class": "CommandLineTool", "cwlVersion": "v1.2",
			"baseCommand": []any{"sh", "-c", shell},
			"inputs":      map[string]any{},
			"outputs":     outputs,
		}
	}
	setup := func(t *testing.T) (*Worker, string) {
		var logs bytes.Buffer
		w := newCWLTestWorker(t, "", &logs)
		w.containerRuntime = "none"
		stage := t.TempDir()
		w.stageOut = "file://" + stage
		w.stager = staging.NewFileStager(w.stageOut)
		return w, stage
	}

	t.Run("directory and glob keep layout, task dir removed", func(t *testing.T) {
		w, stage := setup(t)
		task := &model.Task{ID: "task_lay", Job: map[string]any{}, Tool: treeTool(
			"mkdir -p d/a d/b && echo A > d/a/f && echo B > d/b/f",
			map[string]any{
				"dir":   map[string]any{"type": "Directory", "outputBinding": map[string]any{"glob": "d"}},
				"files": map[string]any{"type": "File[]", "outputBinding": map[string]any{"glob": "d/*/f"}},
			})}
		res := runCWLTask(t, w, task)
		if res.State != model.TaskStateSuccess {
			t.Fatalf("state=%q stderr=%q", res.State, res.Stderr)
		}
		root := filepath.Join(stage, "task_lay")
		for rel, want := range map[string]string{"d/a/f": "A\n", "d/b/f": "B\n"} {
			b, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil || string(b) != want {
				t.Fatalf("%s = %q err=%v", rel, b, err)
			}
		}
		dir := res.Outputs["dir"].(map[string]any)
		if dir["location"] != "file://"+filepath.Join(root, "d") {
			t.Errorf("dir location = %v", dir["location"])
		}
		files := res.Outputs["files"].([]any)
		if len(files) != 2 || files[0].(map[string]any)["location"] == files[1].(map[string]any)["location"] {
			t.Errorf("files = %v", files)
		}
		if _, err := os.Stat(filepath.Join(w.workDir, "task_lay")); !os.IsNotExist(err) {
			t.Error("task dir not removed after SUCCESS")
		}
	})

	t.Run("collision fails the task and keeps the dir", func(t *testing.T) {
		w, stage := setup(t)
		ext := t.TempDir()
		js := `{"o1":{"class":"File","location":"file://` + ext + `/p/r.txt","path":"` + ext + `/p/r.txt"},"o2":{"class":"File","location":"file://` + ext + `/q/r.txt","path":"` + ext + `/q/r.txt"}}`
		task := &model.Task{ID: "task_col", Job: map[string]any{}, Tool: treeTool(
			"mkdir -p "+ext+"/p "+ext+"/q && echo 1 > "+ext+"/p/r.txt && echo 2 > "+ext+"/q/r.txt && printf '%s' '"+js+"' > cwl.output.json",
			map[string]any{
				"o1": map[string]any{"type": "File"},
				"o2": map[string]any{"type": "File"},
			})}
		res := runCWLTask(t, w, task)
		if res.State != model.TaskStateFailed {
			t.Fatalf("expected FAILED from collision (state=%q outputs=%v)", res.State, res.Outputs)
		}
		if !strings.Contains(res.Stderr, "stage-out: destination r.txt claimed by both") {
			t.Errorf("stderr = %q", res.Stderr)
		}
		if _, err := os.Stat(filepath.Join(w.workDir, "task_col")); err != nil {
			t.Error("task dir must be kept on failure")
		}
		_ = stage
	})
}
