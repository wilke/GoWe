package staging

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCopyFile_HappyPath verifies the ordinary (non-error) copy path still
// works: content and size are preserved end to end.
func TestCopyFile_HappyPath(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	src := filepath.Join(srcDir, "in.bin")
	content := strings.Repeat("x", 300000) // spans a 256 KiB boundary
	if err := os.WriteFile(src, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dstDir, "nested", "out.bin")
	if err := CopyFile(src, dst); err != nil {
		t.Fatalf("CopyFile failed: %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content mismatch: got %d bytes, want %d bytes", len(got), len(content))
	}

	srcInfo, _ := os.Stat(src)
	dstInfo, _ := os.Stat(dst)
	if srcInfo.Size() != dstInfo.Size() {
		t.Fatalf("size mismatch: src=%d dst=%d", srcInfo.Size(), dstInfo.Size())
	}
}

// TestCopyFile_SourceMissing verifies error propagation when the source
// file does not exist.
func TestCopyFile_SourceMissing(t *testing.T) {
	dstDir := t.TempDir()
	err := CopyFile(filepath.Join(t.TempDir(), "does-not-exist"), filepath.Join(dstDir, "out"))
	if err == nil {
		t.Fatal("expected error for missing source file, got nil")
	}
}

// TestCopyDirectory_HappyPath verifies recursive copy still works and
// inherits CopyFile's verification for every file it copies.
func TestCopyDirectory_HappyPath(t *testing.T) {
	srcDir := t.TempDir()
	dstDir := t.TempDir()

	if err := os.MkdirAll(filepath.Join(srcDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "sub", "b.txt"), []byte("world"), 0o644); err != nil {
		t.Fatal(err)
	}

	dst := filepath.Join(dstDir, "copy")
	if err := CopyDirectory(srcDir, dst); err != nil {
		t.Fatalf("CopyDirectory failed: %v", err)
	}

	a, err := os.ReadFile(filepath.Join(dst, "a.txt"))
	if err != nil || string(a) != "hello" {
		t.Fatalf("a.txt: content=%q err=%v", a, err)
	}
	b, err := os.ReadFile(filepath.Join(dst, "sub", "b.txt"))
	if err != nil || string(b) != "world" {
		t.Fatalf("sub/b.txt: content=%q err=%v", b, err)
	}
}

// TestVerifyCopySize exercises the size-verification helper directly,
// covering the truncation signature from GoWe issue #215: a transfer
// accepted as complete despite ending short (the captured case wrote
// exactly 262144 bytes -- a 256 KiB buffer boundary -- against an expected
// 268866).
func TestVerifyCopySize(t *testing.T) {
	writeFile := func(t *testing.T, dir, name string, size int) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}

	tests := []struct {
		name       string
		srcSize    int
		dstSize    int
		written    int64
		wantErr    bool
		wantSubstr []string // substrings that must all appear in the error
	}{
		{
			name:    "matching sizes ok",
			srcSize: 268866,
			dstSize: 268866,
			written: 268866,
			wantErr: false,
		},
		{
			name:       "issue #215 signature: truncated at 256 KiB boundary",
			srcSize:    268866,
			dstSize:    262144,
			written:    262144, // io.Copy honestly reported what it wrote
			wantErr:    true,
			wantSubstr: []string{"268866", "262144"},
		},
		{
			name:       "dst on disk smaller than what io.Copy claims it wrote",
			srcSize:    268866,
			dstSize:    262144,
			written:    268866, // io.Copy claimed full write, disk disagrees
			wantErr:    true,
			wantSubstr: []string{"268866", "262144", "truncated"},
		},
		{
			name:    "empty file ok",
			srcSize: 0,
			dstSize: 0,
			written: 0,
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			src := writeFile(t, dir, "src", tt.srcSize)
			dst := writeFile(t, dir, "dst", tt.dstSize)

			err := verifyCopySize(src, dst, tt.written)
			if tt.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("expected no error, got: %v", err)
			}
			for _, sub := range tt.wantSubstr {
				if err != nil && !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q does not contain %q", err.Error(), sub)
				}
			}
		})
	}
}

// TestVerifyCopySize_MissingFiles verifies stat errors are surfaced with
// context rather than swallowed.
func TestVerifyCopySize_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "exists")
	if err := os.WriteFile(existing, []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "missing")

	if err := verifyCopySize(missing, existing, 2); err == nil {
		t.Error("expected error when src is missing")
	}
	if err := verifyCopySize(existing, missing, 2); err == nil {
		t.Error("expected error when dst is missing")
	}
}
