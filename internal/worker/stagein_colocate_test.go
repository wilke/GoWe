package worker

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/staging"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func localFileObj(path string) map[string]any {
	return map[string]any{"class": "File", "location": "file://" + path, "path": path}
}

// Uploads put each file in its own folder; staging must still present the
// secondary files beside the primary (#349 regression in server-worker
// conformance tests 103, 203, 204, 206).
func TestStageRemoteInputs_ColocatesSecondaryFiles(t *testing.T) {
	uploads := t.TempDir()
	fasta := filepath.Join(uploads, "20261111-181136-1", "ref.fasta")
	fai := filepath.Join(uploads, "20261111-181136-2", "ref.fasta.fai")
	writeFile(t, fasta, ">x\nACGT\n")
	writeFile(t, fai, "x\t4\t3\t4\t5\n")

	primary := localFileObj(fasta)
	primary["basename"] = "ref.fasta"
	sec := localFileObj(fai)
	primary["secondaryFiles"] = []any{sec}
	plain := localFileObj(fasta) // same upload, no secondaries: must stay in place
	inputs := map[string]any{"ref": primary, "again": plain}

	workDir := filepath.Join(t.TempDir(), "task_1", "work")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := stageRemoteInputs(context.Background(), nil, inputs, workDir, logger); err != nil {
		t.Fatalf("stageRemoteInputs: %v", err)
	}

	pPath := primary["path"].(string)
	sPath := sec["path"].(string)
	if filepath.Dir(pPath) != filepath.Dir(sPath) {
		t.Fatalf("primary %s and secondary %s are not in the same folder", pPath, sPath)
	}
	if filepath.Base(pPath) != "ref.fasta" || filepath.Base(sPath) != "ref.fasta.fai" {
		t.Errorf("basenames = %s, %s", filepath.Base(pPath), filepath.Base(sPath))
	}
	stagingDir := workDir + "_staging"
	if rel, err := filepath.Rel(stagingDir, pPath); err != nil || rel == filepath.Base(pPath) || rel[:2] == ".." {
		t.Errorf("co-located files should be in a folder under %s, got %s", stagingDir, pPath)
	}
	if primary["location"] != "file://"+pPath || primary["dirname"] != filepath.Dir(pPath) {
		t.Errorf("location/dirname not updated: %v %v", primary["location"], primary["dirname"])
	}
	if got, err := os.ReadFile(sPath); err != nil || string(got) != "x\t4\t3\t4\t5\n" {
		t.Errorf("read through link: %q %v", got, err)
	}
	if plain["path"] != fasta {
		t.Errorf("input without secondaries moved to %v", plain["path"])
	}

	// Mounts: the real file appears both in place and at the co-located path.
	mounts := staging.CollectInputMounts(inputs)
	if mounts[pPath] != fasta || mounts[sPath] != fai || mounts[fasta] != fasta {
		t.Errorf("mounts = %v", mounts)
	}
}

// The same secondary listed twice (two uploads of it) is linked once and both
// entries point at the link.
func TestStageRemoteInputs_DuplicateSecondary(t *testing.T) {
	uploads := t.TempDir()
	fasta := filepath.Join(uploads, "a", "ref.fasta")
	fai1 := filepath.Join(uploads, "b", "ref.fasta.fai")
	fai2 := filepath.Join(uploads, "c", "ref.fasta.fai")
	writeFile(t, fasta, ">x\n")
	writeFile(t, fai1, "x\n")
	writeFile(t, fai2, "x\n")
	primary := localFileObj(fasta)
	s1, s2 := localFileObj(fai1), localFileObj(fai2)
	primary["secondaryFiles"] = []any{s1, s2}

	workDir := filepath.Join(t.TempDir(), "work")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := stageRemoteInputs(context.Background(), nil, map[string]any{"ref": primary}, workDir, logger); err != nil {
		t.Fatalf("stageRemoteInputs: %v", err)
	}
	want := filepath.Join(filepath.Dir(primary["path"].(string)), "ref.fasta.fai")
	if s1["path"] != want || s2["path"] != want {
		t.Errorf("secondary paths = %v, %v; want both %s", s1["path"], s2["path"], want)
	}
}

// Secondary files already beside the primary are left alone.
func TestStageRemoteInputs_SecondaryAlreadyBeside(t *testing.T) {
	dir := t.TempDir()
	fasta := filepath.Join(dir, "ref.fasta")
	writeFile(t, fasta, ">x\n")
	writeFile(t, fasta+".fai", "x\n")
	primary := localFileObj(fasta)
	sec := localFileObj(fasta + ".fai")
	primary["secondaryFiles"] = []any{sec}

	workDir := filepath.Join(t.TempDir(), "work")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := stageRemoteInputs(context.Background(), nil, map[string]any{"ref": primary}, workDir, logger); err != nil {
		t.Fatal(err)
	}
	if primary["path"] != fasta || sec["path"] != fasta+".fai" {
		t.Errorf("paths changed: %v %v", primary["path"], sec["path"])
	}
	if _, err := os.Stat(workDir + "_staging"); err == nil {
		t.Errorf("staging dir created although nothing needed co-location")
	}
}
