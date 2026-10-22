package cwloutput

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile creates a file with the given content inside dir and returns its
// absolute path.
func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func fileNode(path, basename string) map[string]any {
	return map[string]any{
		"class":    "File",
		"path":     path,
		"location": "file://" + path,
		"basename": basename,
	}
}

func TestNormalizeOutputFiles_RenameApplied(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "1.txt", "content")

	f := fileNode(p, "alpha_1.txt")
	if err := NormalizeOutputFiles(f, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}

	wantPath := filepath.Join(dir, "alpha_1.txt")
	if f["path"] != wantPath {
		t.Errorf("path = %v, want %v", f["path"], wantPath)
	}
	if f["location"] != "file://"+wantPath {
		t.Errorf("location = %v, want %v", f["location"], "file://"+wantPath)
	}
	if f["nameroot"] != "alpha_1" {
		t.Errorf("nameroot = %v, want alpha_1", f["nameroot"])
	}
	if f["nameext"] != ".txt" {
		t.Errorf("nameext = %v, want .txt", f["nameext"])
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("renamed file not found on disk: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("old file still present on disk: %v", err)
	}
}

func TestNormalizeOutputFiles_NoOpWhenConsistent(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "already.txt", "content")
	f := fileNode(p, "already.txt")

	if err := NormalizeOutputFiles(f, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}
	if f["path"] != p {
		t.Errorf("path changed: %v, want %v", f["path"], p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("file missing on disk: %v", err)
	}
}

func TestNormalizeOutputFiles_MissingPathTolerated(t *testing.T) {
	// A file literal (e.g. from an ExpressionTool) with no path/location on
	// disk. Behavior: tolerated, left untouched, no error.
	f := map[string]any{
		"class":    "File",
		"basename": "literal.txt",
		"contents": "hello",
	}
	if err := NormalizeOutputFiles(f, ""); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}
	if _, ok := f["path"]; ok {
		t.Errorf("path should remain unset for a file literal, got %v", f["path"])
	}
}

func TestNormalizeOutputFiles_ArrayOfFiles(t *testing.T) {
	dir := t.TempDir()
	p1 := writeFile(t, dir, "1.txt", "a")
	p2 := writeFile(t, dir, "2.txt", "b")
	p3 := writeFile(t, dir, "3.txt", "c")

	arr := []any{
		fileNode(p1, "alpha_1.txt"),
		fileNode(p2, "alpha_2.txt"),
		fileNode(p3, "alpha_3.txt"),
	}
	if err := NormalizeOutputFiles(arr, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}

	wantNames := []string{"alpha_1.txt", "alpha_2.txt", "alpha_3.txt"}
	for i, item := range arr {
		m := item.(map[string]any)
		wantPath := filepath.Join(dir, wantNames[i])
		if m["path"] != wantPath {
			t.Errorf("item %d: path = %v, want %v", i, m["path"], wantPath)
		}
		if filepath.Base(m["path"].(string)) != m["basename"] {
			t.Errorf("item %d: basename/path mismatch: basename=%v path=%v", i, m["basename"], m["path"])
		}
		if _, err := os.Stat(wantPath); err != nil {
			t.Errorf("item %d: expected file not found: %v", i, err)
		}
	}
}

func TestNormalizeOutputFiles_NestedRecord(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "1.txt", "content")

	outputs := map[string]any{
		"result": map[string]any{
			"nested": fileNode(p, "renamed.txt"),
		},
	}
	if err := NormalizeOutputFiles(outputs, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}
	nested := outputs["result"].(map[string]any)["nested"].(map[string]any)
	wantPath := filepath.Join(dir, "renamed.txt")
	if nested["path"] != wantPath {
		t.Errorf("path = %v, want %v", nested["path"], wantPath)
	}
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("renamed file not found: %v", err)
	}
}

func TestNormalizeOutputFiles_NestedArrayOfArrays(t *testing.T) {
	dir := t.TempDir()
	p1 := writeFile(t, dir, "1.txt", "a")
	p2 := writeFile(t, dir, "2.txt", "b")

	outputs := map[string]any{
		"all_files": []any{
			[]any{fileNode(p1, "esm_1.txt")},
			[]any{fileNode(p2, "pmpnn_1.txt")},
		},
	}
	if err := NormalizeOutputFiles(outputs, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}
	outer := outputs["all_files"].([]any)
	inner0 := outer[0].([]any)[0].(map[string]any)
	inner1 := outer[1].([]any)[0].(map[string]any)
	if inner0["path"] != filepath.Join(dir, "esm_1.txt") {
		t.Errorf("inner0 path = %v", inner0["path"])
	}
	if inner1["path"] != filepath.Join(dir, "pmpnn_1.txt") {
		t.Errorf("inner1 path = %v", inner1["path"])
	}
}

func TestNormalizeOutputFiles_SecondaryFiles(t *testing.T) {
	dir := t.TempDir()
	p := writeFile(t, dir, "main.txt", "content")
	sp := writeFile(t, dir, "main.idx", "index")

	f := fileNode(p, "renamed.txt")
	f["secondaryFiles"] = []any{fileNode(sp, "renamed.idx")}

	if err := NormalizeOutputFiles(f, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}
	wantMain := filepath.Join(dir, "renamed.txt")
	wantSec := filepath.Join(dir, "renamed.idx")
	if f["path"] != wantMain {
		t.Errorf("main path = %v, want %v", f["path"], wantMain)
	}
	sf := f["secondaryFiles"].([]any)[0].(map[string]any)
	if sf["path"] != wantSec {
		t.Errorf("secondary path = %v, want %v", sf["path"], wantSec)
	}
	if _, err := os.Stat(wantMain); err != nil {
		t.Errorf("main file not found: %v", err)
	}
	if _, err := os.Stat(wantSec); err != nil {
		t.Errorf("secondary file not found: %v", err)
	}
}

func TestNormalizeOutputFiles_DirectoryListing(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "olddir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	childPath := writeFile(t, subDir, "old_child.txt", "content")

	dirNode := map[string]any{
		"class":    "Directory",
		"path":     subDir,
		"location": "file://" + subDir,
		"basename": "newdir", // Directory itself renamed.
		"listing": []any{
			fileNode(childPath, "new_child.txt"), // Child also renamed.
		},
	}

	if err := NormalizeOutputFiles(dirNode, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}

	wantDirPath := filepath.Join(dir, "newdir")
	if dirNode["path"] != wantDirPath {
		t.Fatalf("dir path = %v, want %v", dirNode["path"], wantDirPath)
	}
	if _, err := os.Stat(wantDirPath); err != nil {
		t.Fatalf("renamed directory not found on disk: %v", err)
	}

	child := dirNode["listing"].([]any)[0].(map[string]any)
	wantChildPath := filepath.Join(wantDirPath, "new_child.txt")
	if child["path"] != wantChildPath {
		t.Errorf("child path = %v, want %v (must reflect BOTH the child's own rename and the parent directory's move)", child["path"], wantChildPath)
	}
	if _, err := os.Stat(wantChildPath); err != nil {
		t.Errorf("child file not found at expected final location: %v", err)
	}
}

func TestNormalizeOutputFiles_ClobberError(t *testing.T) {
	dir := t.TempDir()
	src := writeFile(t, dir, "a.txt", "source")
	// A foreign file already sits at the rename target and is not part of
	// this batch (nothing vacates it).
	target := writeFile(t, dir, "c.txt", "unrelated pre-existing content")

	f := fileNode(src, "c.txt")
	err := NormalizeOutputFiles(f, dir)
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	// Error must name both files involved.
	if !strings.Contains(err.Error(), src) || !strings.Contains(err.Error(), "c.txt") {
		t.Errorf("error does not name both source and target: %v", err)
	}
	// Neither file should have moved.
	if _, statErr := os.Stat(src); statErr != nil {
		t.Errorf("source file should be untouched: %v", statErr)
	}
	if _, statErr := os.Stat(target); statErr != nil {
		t.Errorf("pre-existing target file should be untouched: %v", statErr)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "unrelated pre-existing content" {
		t.Errorf("pre-existing target file was clobbered: content = %q", got)
	}
}

func TestNormalizeOutputFiles_SwapCase(t *testing.T) {
	dir := t.TempDir()
	pa := writeFile(t, dir, "a.txt", "content-a")
	pb := writeFile(t, dir, "b.txt", "content-b")

	// outputEval swaps the two basenames.
	arr := []any{
		fileNode(pa, "b.txt"),
		fileNode(pb, "a.txt"),
	}
	if err := NormalizeOutputFiles(arr, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}

	fa := arr[0].(map[string]any)
	fb := arr[1].(map[string]any)
	if fa["path"] != filepath.Join(dir, "b.txt") {
		t.Errorf("fa path = %v", fa["path"])
	}
	if fb["path"] != filepath.Join(dir, "a.txt") {
		t.Errorf("fb path = %v", fb["path"])
	}

	gotA, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	gotB, err := os.ReadFile(filepath.Join(dir, "b.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotA) != "content-b" {
		t.Errorf("a.txt content = %q, want content-b (original b.txt content)", gotA)
	}
	if string(gotB) != "content-a" {
		t.Errorf("b.txt content = %q, want content-a (original a.txt content)", gotB)
	}
}

func TestNormalizeOutputFiles_AmbiguousDuplicateTarget(t *testing.T) {
	dir := t.TempDir()
	p1 := writeFile(t, dir, "1.txt", "a")
	p2 := writeFile(t, dir, "2.txt", "b")

	// Two different sources both want to become "same.txt".
	arr := []any{
		fileNode(p1, "same.txt"),
		fileNode(p2, "same.txt"),
	}
	err := NormalizeOutputFiles(arr, dir)
	if err == nil {
		t.Fatal("expected an error for ambiguous duplicate target, got nil")
	}
	if !strings.Contains(err.Error(), p1) || !strings.Contains(err.Error(), p2) {
		t.Errorf("error does not name both sources: %v", err)
	}
	// Neither file should have moved.
	if _, statErr := os.Stat(p1); statErr != nil {
		t.Errorf("source 1 should be untouched: %v", statErr)
	}
	if _, statErr := os.Stat(p2); statErr != nil {
		t.Errorf("source 2 should be untouched: %v", statErr)
	}
}

func TestNormalizeOutputFiles_OutsideRootDirSkipped(t *testing.T) {
	root := t.TempDir()
	workDir := filepath.Join(root, "work")
	if err := os.Mkdir(workDir, 0755); err != nil {
		t.Fatal(err)
	}
	outsideDir := filepath.Join(root, "staged-input")
	if err := os.Mkdir(outsideDir, 0755); err != nil {
		t.Fatal(err)
	}
	// Simulates outputEval passing through an *input* File whose path lives
	// outside the tool's own output directory (e.g. staged input data), with
	// a mutated basename. NormalizeOutputFiles must not touch it.
	p := writeFile(t, outsideDir, "input.txt", "original input data")
	f := fileNode(p, "renamed.txt")

	if err := NormalizeOutputFiles(f, workDir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}
	if f["path"] != p {
		t.Errorf("path was changed for an out-of-root file: %v, want %v", f["path"], p)
	}
	if _, err := os.Stat(p); err != nil {
		t.Errorf("original file should be untouched: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outsideDir, "renamed.txt")); !os.IsNotExist(err) {
		t.Errorf("file should not have been renamed on disk")
	}
}

// TestNormalizeOutputFiles_DirectoryRenameUpdatesDescendantDirname is the
// regression test for GoWe#214 (gap 2): when a Directory is renamed,
// rewritePathPrefix updates descendants' path/location, and must also keep a
// present `dirname` field consistent rather than leaving it pointing at the
// directory's old (pre-rename) location.
func TestNormalizeOutputFiles_DirectoryRenameUpdatesDescendantDirname(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "olddir")
	if err := os.Mkdir(subDir, 0755); err != nil {
		t.Fatal(err)
	}
	childPath := writeFile(t, subDir, "child.txt", "content")

	child := fileNode(childPath, "child.txt") // Not itself renamed.
	child["dirname"] = subDir

	dirNode := map[string]any{
		"class":    "Directory",
		"path":     subDir,
		"location": "file://" + subDir,
		"basename": "newdir", // Directory itself renamed.
		"listing":  []any{child},
	}

	if err := NormalizeOutputFiles(dirNode, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}

	wantDirPath := filepath.Join(dir, "newdir")
	wantChildPath := filepath.Join(wantDirPath, "child.txt")
	if child["path"] != wantChildPath {
		t.Fatalf("child path = %v, want %v", child["path"], wantChildPath)
	}
	wantChildDirname := filepath.Dir(wantChildPath)
	if child["dirname"] != wantChildDirname {
		t.Errorf("child dirname = %v, want %v (must not still reference the pre-rename directory %q)", child["dirname"], wantChildDirname, subDir)
	}
	wantChildLoc := "file://" + wantChildPath
	if child["location"] != wantChildLoc {
		t.Errorf("child location = %v, want %v", child["location"], wantChildLoc)
	}
}

// TestNormalizeOutputFiles_DirectoryRenameNestedDescendantDirname covers a
// descendant one level deeper than the renamed directory's immediate
// listing, confirming rewritePathPrefix's recursive walk keeps `dirname` in
// sync at every level, not just the top of the listing.
func TestNormalizeOutputFiles_DirectoryRenameNestedDescendantDirname(t *testing.T) {
	dir := t.TempDir()
	subDir := filepath.Join(dir, "olddir")
	nestedDir := filepath.Join(subDir, "nested")
	if err := os.MkdirAll(nestedDir, 0755); err != nil {
		t.Fatal(err)
	}
	grandchildPath := writeFile(t, nestedDir, "leaf.txt", "content")

	grandchild := fileNode(grandchildPath, "leaf.txt")
	grandchild["dirname"] = nestedDir

	nestedDirNode := map[string]any{
		"class":    "Directory",
		"path":     nestedDir,
		"location": "file://" + nestedDir,
		"basename": "nested", // Not itself renamed.
		"listing":  []any{grandchild},
		"dirname":  subDir,
	}

	dirNode := map[string]any{
		"class":    "Directory",
		"path":     subDir,
		"location": "file://" + subDir,
		"basename": "newdir", // Renamed.
		"listing":  []any{nestedDirNode},
	}

	if err := NormalizeOutputFiles(dirNode, dir); err != nil {
		t.Fatalf("NormalizeOutputFiles: %v", err)
	}

	wantDirPath := filepath.Join(dir, "newdir")
	wantNestedPath := filepath.Join(wantDirPath, "nested")
	wantGrandchildPath := filepath.Join(wantNestedPath, "leaf.txt")

	if nestedDirNode["path"] != wantNestedPath {
		t.Fatalf("nested dir path = %v, want %v", nestedDirNode["path"], wantNestedPath)
	}
	if nestedDirNode["dirname"] != wantDirPath {
		t.Errorf("nested dir dirname = %v, want %v", nestedDirNode["dirname"], wantDirPath)
	}
	if grandchild["path"] != wantGrandchildPath {
		t.Fatalf("grandchild path = %v, want %v", grandchild["path"], wantGrandchildPath)
	}
	if grandchild["dirname"] != wantNestedPath {
		t.Errorf("grandchild dirname = %v, want %v", grandchild["dirname"], wantNestedPath)
	}
}
