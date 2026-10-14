package store

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

var testSHA = strings.Repeat("ab", 32)

func sampleBlessing(id, hash string) *model.ToolBlessing {
	return &model.ToolBlessing{
		ID: id, ToolHash: hash, ToolName: "bvbrc-helper",
		EnvNames: []string{"BVBRC_TOKEN"}, WorkerGroups: []string{"default"},
		ImageRef: "python.sif", ImageSHA256s: []string{testSHA},
		BlessedBy: "admin", BlessedAt: time.Now().UTC().Truncate(time.Millisecond),
		Note:    "reviewed",
		Summary: model.ToolSummary{Class: "CommandLineTool", Flags: []string{"declared-stdout: x"}, Scripts: []model.ScriptEntry{{Source: "a", Content: "b"}}},
	}
}

func TestToolBlessing_CreateGetRoundTrip(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	b := sampleBlessing("tb_1", "sha256:aaa")
	if err := st.CreateToolBlessing(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetToolBlessing(ctx, "tb_1")
	if err != nil || got == nil {
		t.Fatalf("get: %v %v", got, err)
	}
	if got.ToolHash != b.ToolHash || got.ImageRef != "python.sif" || len(got.ImageSHA256s) != 1 || got.ImageSHA256s[0] != testSHA ||
		got.EnvNames[0] != "BVBRC_TOKEN" || got.WorkerGroups[0] != "default" || got.Note != "reviewed" ||
		got.Summary.Scripts[0].Content != "b" || got.Summary.Flags[0] != "declared-stdout: x" || !got.BlessedAt.Equal(b.BlessedAt) {
		t.Errorf("round trip mismatch: %+v", got)
	}
	if !got.Active() {
		t.Error("new blessing must be active")
	}
	byHash, err := st.GetActiveToolBlessingByHash(ctx, "sha256:aaa")
	if err != nil || byHash == nil || byHash.ID != "tb_1" {
		t.Errorf("by hash: %v %v", byHash, err)
	}
	if none, err := st.GetActiveToolBlessingByHash(ctx, "sha256:zzz"); err != nil || none != nil {
		t.Errorf("unknown hash: %v %v", none, err)
	}
	if none, err := st.GetToolBlessing(ctx, "nope"); err != nil || none != nil {
		t.Errorf("unknown id: %v %v", none, err)
	}
}

func TestToolBlessing_RequiresPinnedImage(t *testing.T) {
	st := newTestStore(t)
	b := sampleBlessing("tb_1", "sha256:aaa")
	b.ImageSHA256s = nil
	if err := st.CreateToolBlessing(context.Background(), b); err == nil {
		t.Fatal("a blessing without image digests must be refused")
	}
}

func TestToolBlessing_UniqueActivePerHash(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.CreateToolBlessing(ctx, sampleBlessing("tb_1", "sha256:aaa")); err != nil {
		t.Fatal(err)
	}
	err := st.CreateToolBlessing(ctx, sampleBlessing("tb_2", "sha256:aaa"))
	if !errors.Is(err, ErrBlessingActive) {
		t.Fatalf("second active blessing for same hash: want ErrBlessingActive, got %v", err)
	}
	// Different hash is fine.
	if err := st.CreateToolBlessing(ctx, sampleBlessing("tb_3", "sha256:bbb")); err != nil {
		t.Fatal(err)
	}
	// After revoking, the hash can be blessed again.
	if err := st.RevokeToolBlessing(ctx, "tb_1", "admin", "rotate"); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateToolBlessing(ctx, sampleBlessing("tb_4", "sha256:aaa")); err != nil {
		t.Fatalf("re-bless after revoke: %v", err)
	}
	active, _ := st.GetActiveToolBlessingByHash(ctx, "sha256:aaa")
	if active == nil || active.ID != "tb_4" {
		t.Errorf("active blessing should be tb_4, got %+v", active)
	}
}

func TestToolBlessing_RevokeKeepsRow(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.CreateToolBlessing(ctx, sampleBlessing("tb_1", "sha256:aaa")); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeToolBlessing(ctx, "tb_1", "boss", "found echo"); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetToolBlessing(ctx, "tb_1")
	if got == nil || got.Active() || got.RevokedBy != "boss" || got.RevokeReason != "found echo" || got.RevokedAt == nil {
		t.Fatalf("revoked row: %+v", got)
	}
	if a, _ := st.GetActiveToolBlessingByHash(ctx, "sha256:aaa"); a != nil {
		t.Error("revoked blessing must not be returned as active")
	}
	if l, _ := st.ListToolBlessings(ctx, false); len(l) != 0 {
		t.Errorf("default list hides revoked, got %d", len(l))
	}
	if l, _ := st.ListToolBlessings(ctx, true); len(l) != 1 {
		t.Errorf("includeRevoked list keeps the row, got %d", len(l))
	}
	if err := st.RevokeToolBlessing(ctx, "tb_1", "boss", "again"); !errors.Is(err, ErrBlessingRevoked) {
		t.Errorf("double revoke: %v", err)
	}
	if err := st.RevokeToolBlessing(ctx, "missing", "boss", "x"); !errors.Is(err, ErrBlessingNotFound) {
		t.Errorf("revoke missing: %v", err)
	}
}

func TestToolBlessing_AddImageSHA256(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if err := st.CreateToolBlessing(ctx, sampleBlessing("tb_1", "sha256:aaa")); err != nil {
		t.Fatal(err)
	}
	second := strings.Repeat("cd", 32)
	if err := st.AddToolBlessingImageSHA256(ctx, "tb_1", "sha256:"+strings.ToUpper(second)); err != nil {
		t.Fatal(err)
	}
	// idempotent
	if err := st.AddToolBlessingImageSHA256(ctx, "tb_1", second); err != nil {
		t.Fatal(err)
	}
	got, _ := st.GetToolBlessing(ctx, "tb_1")
	if len(got.ImageSHA256s) != 2 || !got.AllowsImage(second) || !got.AllowsImage(testSHA) {
		t.Errorf("digests: %v", got.ImageSHA256s)
	}
	if err := st.AddToolBlessingImageSHA256(ctx, "tb_1", "not-hex"); err == nil {
		t.Error("invalid digest must be refused")
	}
	if err := st.AddToolBlessingImageSHA256(ctx, "nope", second); !errors.Is(err, ErrBlessingNotFound) {
		t.Errorf("missing id: %v", err)
	}
	_ = st.RevokeToolBlessing(ctx, "tb_1", "a", "b")
	if err := st.AddToolBlessingImageSHA256(ctx, "tb_1", strings.Repeat("ef", 32)); !errors.Is(err, ErrBlessingRevoked) {
		t.Errorf("add to revoked: %v", err)
	}
}
