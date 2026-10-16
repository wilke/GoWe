package staging

import (
	"context"
	"errors"
	"testing"
)

// plainStager is a Stager that is not a DirectoryStager.
type plainStager struct{}

func (plainStager) StageIn(context.Context, string, string, StageOptions) error { return nil }
func (plainStager) StageOut(context.Context, string, string, StageOptions) (string, error) {
	return "plain://x", nil
}
func (plainStager) Supports(string) bool { return true }

func TestCompositeStager_StageOutDir(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()

	t.Run("delegated to fallback", func(t *testing.T) {
		fs := NewFileStager("file://" + base)
		c := NewCompositeStager(map[string]Stager{"file": fs}, fs)
		loc, err := c.StageOutDir(ctx, t.TempDir(), "t1", StageOptions{RelPath: "d"})
		if err != nil || loc == "" {
			t.Fatalf("loc=%q err=%v", loc, err)
		}
	})

	t.Run("fallback without directory support", func(t *testing.T) {
		c := NewCompositeStager(nil, plainStager{})
		_, err := c.StageOutDir(ctx, t.TempDir(), "t1", StageOptions{})
		if !errors.Is(err, ErrDirectoryStagingUnsupported) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("ws routed is unsupported", func(t *testing.T) {
		fs := NewFileStager("file://" + base)
		c := NewCompositeStager(map[string]Stager{"ws": plainStager{}}, fs)
		_, err := c.StageOutDir(ctx, t.TempDir(), "t1", StageOptions{Metadata: map[string]string{"destination": "ws:///u/home/x"}})
		if !errors.Is(err, ErrDirectoryStagingUnsupported) {
			t.Fatalf("err = %v", err)
		}
	})

	t.Run("no fallback", func(t *testing.T) {
		if _, err := NewCompositeStager(nil, nil).StageOutDir(ctx, t.TempDir(), "t", StageOptions{}); err == nil {
			t.Fatal("expected error")
		}
	})
}

func TestStageOutRelSlash(t *testing.T) {
	tests := []struct {
		src, rel, want string
		wantErr        bool
	}{
		{"/x/f.txt", "", "f.txt", false},
		{"/x/f.txt", "a/b/f.txt", "a/b/f.txt", false},
		{"/x/f.txt", "../f", "", true},
	}
	for _, tc := range tests {
		got, err := StageOutRelSlash(tc.src, tc.rel)
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("(%q,%q) = %q, %v", tc.src, tc.rel, got, err)
		}
	}
}
