package store

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

func TestWorkerGroupPolicyCRUD(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()

	if got, err := st.ListWorkerGroupPolicies(ctx); err != nil || got == nil || len(got) != 0 {
		t.Fatalf("empty list = %v, %v; want [], nil", got, err)
	}

	created := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	gpu := &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"alice", "bob"}, Admins: true,
		UpdatedBy: "root", CreatedAt: created, UpdatedAt: created}
	if err := st.UpsertWorkerGroupPolicy(ctx, gpu); err != nil {
		t.Fatalf("Upsert gpu: %v", err)
	}
	// No users at all: stored and read back as an empty list.
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "default", UpdatedBy: "root"}); err != nil {
		t.Fatalf("Upsert default: %v", err)
	}
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "Zeta", Users: []string{"carol"}}); err != nil {
		t.Fatalf("Upsert Zeta: %v", err)
	}

	got, err := st.ListWorkerGroupPolicies(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var names []string
	for _, p := range got {
		names = append(names, p.Group)
	}
	// Byte order on every backend: upper case before lower case.
	if want := []string{"Zeta", "default", "gpu"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("groups = %v, want %v", names, want)
	}
	if g := got[2]; !reflect.DeepEqual(g.Users, []string{"alice", "bob"}) || !g.Admins || g.UpdatedBy != "root" ||
		!g.CreatedAt.Equal(created) || !g.UpdatedAt.Equal(created) {
		t.Errorf("gpu round trip = %+v", g)
	}
	if d := got[1]; d.Users == nil || len(d.Users) != 0 || d.Admins || d.CreatedAt.IsZero() || !d.CreatedAt.Equal(d.UpdatedAt) {
		t.Errorf("default round trip = %+v (want users [], admins false, created_at = updated_at)", d)
	}

	// Replace: users, admins and updated_* change, created_at stays.
	later := created.Add(time.Hour)
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"dave"},
		UpdatedBy: "root2", CreatedAt: later, UpdatedAt: later}); err != nil {
		t.Fatalf("Upsert gpu again: %v", err)
	}
	got, err = st.ListWorkerGroupPolicies(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 after replacing a rule", len(got))
	}
	if g := got[2]; !reflect.DeepEqual(g.Users, []string{"dave"}) || g.Admins || g.UpdatedBy != "root2" ||
		!g.CreatedAt.Equal(created) || !g.UpdatedAt.Equal(later) {
		t.Errorf("gpu after replace = %+v", g)
	}

	existed, err := st.DeleteWorkerGroupPolicy(ctx, "gpu")
	if err != nil || !existed {
		t.Fatalf("Delete gpu = %v, %v; want true, nil", existed, err)
	}
	existed, err = st.DeleteWorkerGroupPolicy(ctx, "gpu")
	if err != nil || existed {
		t.Fatalf("Delete gpu again = %v, %v; want false, nil", existed, err)
	}
	if got, _ := st.ListWorkerGroupPolicies(ctx); len(got) != 2 {
		t.Errorf("len after delete = %d, want 2", len(got))
	}

	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{}); err == nil {
		t.Error("Upsert without a group name must fail")
	}
}

// A row that cannot be decoded is an error, not skipped: a missing rule
// would open its group.
func TestListWorkerGroupPolicies_CorruptRowIsAnError(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{"alice"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE worker_group_policies SET users = ? WHERE group_name = ?`, `{not json`, "gpu"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ListWorkerGroupPolicies(ctx); err == nil || !strings.Contains(err.Error(), "users") {
		t.Fatalf("List error = %v, want a corrupt users error", err)
	}
}

func TestServerSettings(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	const key = "worker_groups.default_deny"

	if v, found, err := st.GetServerSetting(ctx, key); err != nil || found || v != "" {
		t.Fatalf("missing key = %q, %v, %v; want \"\", false, nil", v, found, err)
	}
	if err := st.SetServerSetting(ctx, key, "true", "root"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if v, found, err := st.GetServerSetting(ctx, key); err != nil || !found || v != "true" {
		t.Fatalf("after set = %q, %v, %v; want \"true\", true, nil", v, found, err)
	}
	if err := st.SetServerSetting(ctx, key, "false", "root2"); err != nil {
		t.Fatalf("Set again: %v", err)
	}
	if v, found, err := st.GetServerSetting(ctx, key); err != nil || !found || v != "false" {
		t.Fatalf("after replace = %q, %v, %v; want \"false\", true, nil", v, found, err)
	}
	var updatedBy string
	if err := st.db.QueryRowContext(ctx, `SELECT updated_by FROM server_settings WHERE key = ?`, key).Scan(&updatedBy); err != nil {
		t.Fatal(err)
	}
	if updatedBy != "root2" {
		t.Errorf("updated_by = %q, want root2", updatedBy)
	}
	if n := countRows(t, st, "server_settings"); n != 1 {
		t.Errorf("server_settings rows = %d, want 1", n)
	}
}
