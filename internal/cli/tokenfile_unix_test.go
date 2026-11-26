//go:build unix

package cli

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ownedFileInfo is an fs.FileInfo whose Sys() is a *syscall.Stat_t with the
// given owner (a file owned by someone else cannot be made without root).
type ownedFileInfo struct{ uid uint32 }

func (o ownedFileInfo) Name() string       { return "token" }
func (o ownedFileInfo) Size() int64        { return 0 }
func (o ownedFileInfo) Mode() fs.FileMode  { return 0o600 }
func (o ownedFileInfo) ModTime() time.Time { return time.Time{} }
func (o ownedFileInfo) IsDir() bool        { return false }
func (o ownedFileInfo) Sys() any {
	st := &syscall.Stat_t{}
	st.Uid = o.uid
	return st
}

// --token-file must be owned by the current user.
func TestCheckFileOwner(t *testing.T) {
	me := uint32(os.Getuid())
	if err := checkFileOwner(ownedFileInfo{uid: me}); err != nil {
		t.Errorf("own file refused: %v", err)
	}
	err := checkFileOwner(ownedFileInfo{uid: me + 1})
	if err == nil || !strings.Contains(err.Error(), "not by the current user") {
		t.Errorf("file of another uid: err = %v, want the owner refusal", err)
	}

	// A real file the test created is the current user's.
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(fakeSvcToken), 0o600); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkFileOwner(fi); err != nil {
		t.Errorf("file created by the test refused: %v", err)
	}
	if got, err := readTokenFile(path); err != nil || got != fakeSvcToken {
		t.Errorf("readTokenFile = %q, %v", got, err)
	}
}

// O_NOFOLLOW: opening a symbolic link fails even when it points to a valid
// file (the race readTokenFile's Lstat alone would leave open).
func TestOpenNoFollow_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "token")
	if err := os.WriteFile(target, []byte(fakeSvcToken), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if f, err := openNoFollow(link); err == nil {
		f.Close()
		t.Error("openNoFollow followed a symbolic link")
	}
	f, err := openNoFollow(target)
	if err != nil {
		t.Fatalf("openNoFollow(regular file): %v", err)
	}
	f.Close()
}
