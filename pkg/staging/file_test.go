package staging

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestFileStager_StageOut_RelPath(t *testing.T) {
	tests := []struct {
		name    string
		rel     string
		want    string // path below <base>/<task>
		wantErr bool
	}{
		{"empty is basename", "", "src.txt", false},
		{"nested", "a/b/src.txt", "a/b/src.txt", false},
		{"dot segments cleaned", "a/./b/../c/src.txt", "a/c/src.txt", false},
		{"absolute rejected", "/etc/passwd", "", true},
		{"escape rejected", "../x", "", true},
		{"inner escape rejected", "a/../../x", "", true},
		{"dot rejected", ".", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			src := filepath.Join(t.TempDir(), "src.txt")
			writeFile(t, src, "payload")
			s := NewFileStager("file://" + base)
			loc, err := s.StageOut(context.Background(), src, "task1", StageOptions{RelPath: tc.rel})
			if tc.wantErr {
				if !errors.Is(err, ErrUnsafeRelPath) {
					t.Fatalf("err = %v, want ErrUnsafeRelPath", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(base, "task1", filepath.FromSlash(tc.want))
			if loc != "file://"+dest {
				t.Fatalf("location = %s, want file://%s", loc, dest)
			}
			b, err := os.ReadFile(dest)
			if err != nil || string(b) != "payload" {
				t.Fatalf("content = %q err=%v", b, err)
			}
		})
	}

	t.Run("local mode ignores RelPath", func(t *testing.T) {
		src := filepath.Join(t.TempDir(), "src.txt")
		writeFile(t, src, "x")
		loc, err := NewFileStager("local").StageOut(context.Background(), src, "t", StageOptions{RelPath: "a/b/src.txt"})
		if err != nil || loc != "file://"+src {
			t.Fatalf("loc=%s err=%v", loc, err)
		}
	})
}

func TestFileStager_StageOutDir(t *testing.T) {
	ctx := context.Background()

	t.Run("mkdir only", func(t *testing.T) {
		base := t.TempDir()
		src := t.TempDir()
		writeFile(t, filepath.Join(src, "f"), "x")
		loc, err := NewFileStager("file://"+base).StageOutDir(ctx, src, "t1", StageOptions{RelPath: "s/d"})
		if err != nil {
			t.Fatal(err)
		}
		dest := filepath.Join(base, "t1", "s", "d")
		if loc != "file://"+dest {
			t.Fatalf("loc = %s", loc)
		}
		entries, err := os.ReadDir(dest)
		if err != nil || len(entries) != 0 {
			t.Fatalf("expected empty dir, entries=%v err=%v", entries, err)
		}
	})

	t.Run("copy tree", func(t *testing.T) {
		base := t.TempDir()
		src := t.TempDir()
		writeFile(t, filepath.Join(src, "a", "f"), "A")
		writeFile(t, filepath.Join(src, "b", "f"), "B")
		_, err := NewFileStager("file://"+base).StageOutDir(ctx, src, "t1", StageOptions{RelPath: "out", CopyTree: true})
		if err != nil {
			t.Fatal(err)
		}
		for rel, want := range map[string]string{"a/f": "A", "b/f": "B"} {
			b, err := os.ReadFile(filepath.Join(base, "t1", "out", rel))
			if err != nil || string(b) != want {
				t.Fatalf("%s = %q err=%v", rel, b, err)
			}
		}
	})

	t.Run("empty RelPath uses basename", func(t *testing.T) {
		base := t.TempDir()
		src := filepath.Join(t.TempDir(), "mydir")
		writeFile(t, filepath.Join(src, "f"), "x")
		loc, err := NewFileStager("file://"+base).StageOutDir(ctx, src, "t1", StageOptions{CopyTree: true})
		if err != nil {
			t.Fatal(err)
		}
		if want := "file://" + filepath.Join(base, "t1", "mydir"); loc != want {
			t.Fatalf("loc = %s, want %s", loc, want)
		}
	})

	t.Run("unsafe rejected", func(t *testing.T) {
		for _, rel := range []string{"../x", "/abs", "."} {
			_, err := NewFileStager("file://"+t.TempDir()).StageOutDir(ctx, t.TempDir(), "t", StageOptions{RelPath: rel})
			if !errors.Is(err, ErrUnsafeRelPath) {
				t.Fatalf("rel %q: err = %v", rel, err)
			}
		}
	})

	t.Run("local mode in place", func(t *testing.T) {
		src := t.TempDir()
		loc, err := NewFileStager("local").StageOutDir(ctx, src, "t", StageOptions{RelPath: "x", CopyTree: true})
		if err != nil || loc != "file://"+src {
			t.Fatalf("loc=%s err=%v", loc, err)
		}
	})

	t.Run("symlinks followed", func(t *testing.T) {
		base := t.TempDir()
		src := t.TempDir()
		ext := t.TempDir()
		writeFile(t, filepath.Join(ext, "real", "inner"), "I")
		writeFile(t, filepath.Join(ext, "file"), "F")
		if err := os.Symlink(filepath.Join(ext, "real"), filepath.Join(src, "dirlink")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(ext, "file"), filepath.Join(src, "filelink")); err != nil {
			t.Fatal(err)
		}
		_, err := NewFileStager("file://"+base).StageOutDir(ctx, src, "t", StageOptions{RelPath: "o", CopyTree: true})
		if err != nil {
			t.Fatal(err)
		}
		for rel, want := range map[string]string{"dirlink/inner": "I", "filelink": "F"} {
			p := filepath.Join(base, "t", "o", rel)
			b, err := os.ReadFile(p)
			if err != nil || string(b) != want {
				t.Fatalf("%s = %q err=%v", rel, b, err)
			}
			if fi, _ := os.Lstat(p); fi.Mode()&os.ModeSymlink != 0 {
				t.Fatalf("%s is a symlink in the copy", rel)
			}
		}
	})

	t.Run("symlink cycle terminates", func(t *testing.T) {
		base := t.TempDir()
		src := t.TempDir()
		writeFile(t, filepath.Join(src, "d", "f"), "x")
		if err := os.Symlink(src, filepath.Join(src, "d", "loop")); err != nil {
			t.Fatal(err)
		}
		_, err := NewFileStager("file://"+base).StageOutDir(ctx, src, "t", StageOptions{RelPath: "o", CopyTree: true})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(base, "t", "o", "d", "f")); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(base, "t", "o", "d", "loop", "d", "loop")); err == nil {
			t.Fatal("cycle was followed")
		}
	})
}
