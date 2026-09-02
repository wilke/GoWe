package staging

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/me/gowe/pkg/bvbrc"
	"github.com/me/gowe/pkg/bvbrc/bvbrctest"
)

// seedWorkspaceFile creates (or overwrites) a text object at wsPath in the
// fake, going through the real create+Shock-upload protocol so it is
// downloadable via Workspace.get_download_url — the same path
// stageInFile/download exercise.
func seedWorkspaceFile(t *testing.T, client *bvbrc.Client, wsPath, content string) {
	t.Helper()
	if _, err := client.WorkspaceUploadFile(context.Background(), wsPath, []byte(content), bvbrc.WorkspaceTypeUnspecified); err != nil {
		t.Fatalf("seed file %s: %v", wsPath, err)
	}
}

func seedWorkspaceFolder(t *testing.T, client *bvbrc.Client, wsPath string) {
	t.Helper()
	if _, err := client.WorkspaceCreateFolder(context.Background(), wsPath); err != nil {
		t.Fatalf("seed folder %s: %v", wsPath, err)
	}
}

func newSeedClient(f *bvbrctest.Server) *bvbrc.Client {
	return bvbrc.NewClient(bvbrc.Config{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester|sig=abc",
		MaxRetries:   1,
		RetryDelay:   0,
	}, nil)
}

// TestWorkspaceStageIn_RecursiveDirectoryPreservesTree seeds a small nested
// tree (including an empty subfolder) in the fake workspace and confirms a
// single StageIn on the top folder reconstructs it locally byte-for-byte.
func TestWorkspaceStageIn_RecursiveDirectoryPreservesTree(t *testing.T) {
	f := bvbrctest.New(t)
	client := newSeedClient(f)

	const root = "/tester@bvbrc/home/results/asm"
	seedWorkspaceFolder(t, client, root)
	seedWorkspaceFile(t, client, root+"/summary.txt", "top level file")
	seedWorkspaceFolder(t, client, root+"/contigs")
	seedWorkspaceFile(t, client, root+"/contigs/1.fasta", ">seq1\nACGT")
	seedWorkspaceFile(t, client, root+"/contigs/2.fasta", ">seq2\nTTTT")
	seedWorkspaceFolder(t, client, root+"/contigs/deep")
	seedWorkspaceFile(t, client, root+"/contigs/deep/3.fasta", ">seq3\nGGGG")
	seedWorkspaceFolder(t, client, root+"/empty")

	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester|sig=abc",
		MaxRetries:   1,
	}, nil)

	dest := filepath.Join(t.TempDir(), "asm")
	if err := stager.StageIn(context.Background(), "ws://"+root, dest, StageOptions{}); err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	wantFiles := map[string]string{
		"summary.txt":          "top level file",
		"contigs/1.fasta":      ">seq1\nACGT",
		"contigs/2.fasta":      ">seq2\nTTTT",
		"contigs/deep/3.fasta": ">seq3\nGGGG",
	}
	for rel, want := range wantFiles {
		got, err := os.ReadFile(filepath.Join(dest, rel))
		if err != nil {
			t.Errorf("read %s: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s content = %q, want %q", rel, got, want)
		}
	}

	if info, err := os.Stat(filepath.Join(dest, "empty")); err != nil || !info.IsDir() {
		t.Errorf("empty subfolder not materialized: %v", err)
	}
}

// TestWorkspaceStageIn_JobResultTreatedAsFolder confirms a workspace object
// typed "job_result" (a BV-BRC job's {output_path}/{output_file} container —
// the most common real-world shape of a ws:// Directory input, per the
// #154/#198 live promotion round-trip) is recursed into the same way a plain
// "folder" is, not skipped or treated as a single file.
func TestWorkspaceStageIn_JobResultTreatedAsFolder(t *testing.T) {
	f := bvbrctest.New(t)
	client := newSeedClient(f)

	const root = "/tester@bvbrc/home/results/.asm"
	if _, err := client.WorkspaceCreate(context.Background(), bvbrc.WorkspaceCreateInput{
		Path: root, Type: bvbrc.WorkspaceTypeJobResult,
	}); err != nil {
		t.Fatalf("seed job_result container: %v", err)
	}
	seedWorkspaceFile(t, client, root+"/out.txt", "job result contents")

	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester|sig=abc",
		MaxRetries:   1,
	}, nil)

	dest := filepath.Join(t.TempDir(), "asm")
	if err := stager.StageIn(context.Background(), "ws://"+root, dest, StageOptions{}); err != nil {
		t.Fatalf("StageIn (job_result container): %v", err)
	}

	got, err := os.ReadFile(filepath.Join(dest, "out.txt"))
	if err != nil {
		t.Fatalf("read out.txt: %v", err)
	}
	if string(got) != "job result contents" {
		t.Errorf("content = %q, want %q", got, "job result contents")
	}
}

// TestWorkspaceStageIn_PlainFileStillWorks is a regression check that a
// non-folder ws:// location still takes the single-file path (no
// unnecessary Workspace.ls calls).
func TestWorkspaceStageIn_PlainFileStillWorks(t *testing.T) {
	f := bvbrctest.New(t)
	client := newSeedClient(f)

	const wsPath = "/tester@bvbrc/home/results/report.txt"
	seedWorkspaceFile(t, client, wsPath, "hello")

	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester|sig=abc",
		MaxRetries:   1,
	}, nil)

	dest := filepath.Join(t.TempDir(), "report.txt")
	if err := stager.StageIn(context.Background(), "ws://"+wsPath, dest, StageOptions{}); err != nil {
		t.Fatalf("StageIn: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read dest: %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("content = %q, want hello", got)
	}
	if calls := f.CallsTo("Workspace.ls"); len(calls) != 0 {
		t.Errorf("Workspace.ls calls = %d, want 0 for a plain file StageIn", len(calls))
	}
}

// folderTuple builds a 12-slot ObjectMeta tuple (the shape bvbrctest's
// metaLocked emits) for a folder at path, for use with Server.LsReply.
func folderTuple(path string) []any {
	dir, name := path, ""
	if i := len(path) - 1; i >= 0 {
		for j := i; j >= 0; j-- {
			if path[j] == '/' {
				dir, name = path[:j+1], path[j+1:]
				break
			}
		}
	}
	return []any{
		name, "folder", dir, "2026-08-20T12:00:00Z", "id-" + name, "tester@bvbrc",
		float64(0), map[string]any{}, map[string]any{"is_folder": "1"}, "o", "n",
	}
}

// TestWorkspaceStageIn_DirectoryEntryCapEnforced fabricates a single-level
// listing with more entries than maxDirectoryEntries and confirms StageIn
// aborts with a clear error before downloading anything.
func TestWorkspaceStageIn_DirectoryEntryCapEnforced(t *testing.T) {
	f := bvbrctest.New(t)
	const root = "/tester@bvbrc/home/results/huge"
	f.Put(bvbrctest.Object{Path: root, Type: "folder"})

	f.LsReply = func(paths []string) (map[string][][]any, bool) {
		dir := paths[0]
		tuples := make([][]any, 0, maxDirectoryEntries+1)
		for i := 0; i < maxDirectoryEntries+1; i++ {
			tuples = append(tuples, folderTuple(fmt.Sprintf("%s/dir%d", dir, i)))
		}
		return map[string][][]any{dir: tuples}, true
	}

	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester|sig=abc",
		MaxRetries:   1,
	}, nil)

	dest := filepath.Join(t.TempDir(), "huge")
	err := stager.StageIn(context.Background(), "ws://"+root, dest, StageOptions{})
	if err == nil {
		t.Fatal("expected an error when the directory exceeds the entry cap")
	}

	// No child directory should have been created — the cap check happens
	// before any per-entry work for the offending level.
	entries, _ := os.ReadDir(dest)
	if len(entries) != 0 {
		t.Errorf("created %d child entries before hitting the cap, want 0", len(entries))
	}
}

// TestWorkspaceStageIn_DirectoryDepthCapEnforced fabricates an infinitely
// (for practical purposes) nested chain of single-subfolder directories and
// confirms StageIn aborts once maxDirectoryDepth is exceeded rather than
// recursing forever.
func TestWorkspaceStageIn_DirectoryDepthCapEnforced(t *testing.T) {
	f := bvbrctest.New(t)
	const root = "/tester@bvbrc/home/results/deep"
	f.Put(bvbrctest.Object{Path: root, Type: "folder"})

	f.LsReply = func(paths []string) (map[string][][]any, bool) {
		dir := paths[0]
		// Every directory has exactly one subfolder, "next" — the chain
		// never terminates naturally.
		return map[string][][]any{dir: {folderTuple(dir + "/next")}}, true
	}

	stager := NewWorkspaceStager(WorkspaceConfig{
		WorkspaceURL: f.WorkspaceURL(),
		Token:        "un=tester|sig=abc",
		MaxRetries:   1,
	}, nil)

	dest := filepath.Join(t.TempDir(), "deep")
	err := stager.StageIn(context.Background(), "ws://"+root, dest, StageOptions{})
	if err == nil {
		t.Fatal("expected an error when the directory chain exceeds the depth cap")
	}
}
