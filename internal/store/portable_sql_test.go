package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func TestCreateLabelVocabulary_DuplicateIsErrDuplicate(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	mk := func(id string) *model.LabelVocabulary {
		return &model.LabelVocabulary{ID: id, Key: "project", Value: "alpha", CreatedAt: time.Now().UTC()}
	}
	if err := st.CreateLabelVocabulary(ctx, mk("lv_1")); err != nil {
		t.Fatalf("first insert: %v", err)
	}
	tests := []struct {
		name string
		lv   *model.LabelVocabulary
	}{
		{"same key and value", mk("lv_2")},
		{"same id", &model.LabelVocabulary{ID: "lv_1", Key: "project", Value: "beta", CreatedAt: time.Now().UTC()}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := st.CreateLabelVocabulary(ctx, tc.lv)
			if !errors.Is(err, ErrDuplicate) {
				t.Fatalf("err = %v, want ErrDuplicate", err)
			}
		})
	}
	other := mk("lv_3")
	other.Value = "gamma"
	if err := st.CreateLabelVocabulary(ctx, other); err != nil {
		t.Fatalf("different value: %v", err)
	}
}

// A duplicate primary key is not "an active blessing exists".
func TestToolBlessing_DuplicateIDIsNotErrBlessingActive(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.CreateToolBlessing(ctx, sampleBlessing("tb_1", "sha256:aaa")); err != nil {
		t.Fatal(err)
	}
	err := st.CreateToolBlessing(ctx, sampleBlessing("tb_1", "sha256:bbb"))
	if err == nil {
		t.Fatal("duplicate id must fail")
	}
	if errors.Is(err, ErrBlessingActive) {
		t.Fatalf("duplicate id reported as ErrBlessingActive: %v", err)
	}
}

func TestSearchIsCaseInsensitive(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	wf := sampleWorkflow()
	wf.Name = "Protein-Fold"
	wf.Description = "Runs ESMFold"
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sub := sampleSubmission(wf.ID)
	sub.WorkflowName = wf.Name
	if err := st.CreateSubmission(ctx, sub); err != nil {
		t.Fatalf("create submission: %v", err)
	}

	for _, term := range []string{"protein", "PROTEIN", "esmfold", "WF_TEST", "nomatch"} {
		t.Run("workflows/"+term, func(t *testing.T) {
			opts := model.DefaultListOptions()
			opts.Search = term
			_, total, err := st.ListWorkflows(ctx, opts)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			want := 1
			if term == "nomatch" {
				want = 0
			}
			if total != want {
				t.Errorf("total = %d, want %d", total, want)
			}
		})
	}
	for _, term := range []string{"protein", "PROTEIN", "SUB_TEST", "nomatch"} {
		t.Run("submissions/"+term, func(t *testing.T) {
			opts := model.DefaultListOptions()
			opts.Search = term
			_, total, err := st.ListSubmissions(ctx, opts)
			if err != nil {
				t.Fatalf("list: %v", err)
			}
			want := 1
			if term == "nomatch" {
				want = 0
			}
			if total != want {
				t.Errorf("total = %d, want %d", total, want)
			}
		})
	}
}

func TestLinkProvider_Upsert(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	user, err := st.GetOrCreateUser(ctx, "alice", model.ProviderLocal)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	for _, name := range []string{"alice@bvbrc", "alice2@bvbrc"} {
		if err := st.LinkProvider(ctx, user.ID, model.ProviderBVBRC, name); err != nil {
			t.Fatalf("link %s: %v", name, err)
		}
	}
	// Another provider of the same user is a separate row and stays.
	if err := st.LinkProvider(ctx, user.ID, model.ProviderMGRAST, "alice@mgrast"); err != nil {
		t.Fatalf("link second provider: %v", err)
	}
	var total int
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM linked_providers WHERE user_id = ?`, user.ID).Scan(&total); err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Errorf("linked providers = %d, want 2", total)
	}
	var n int
	var username string
	if err := st.db.QueryRowContext(ctx,
		`SELECT COUNT(*), MAX(username) FROM linked_providers WHERE user_id = ? AND provider = ?`,
		user.ID, string(model.ProviderBVBRC)).Scan(&n, &username); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if n != 1 || username != "alice2@bvbrc" {
		t.Errorf("rows = %d, username = %q; want 1 row with the newer username", n, username)
	}
}
