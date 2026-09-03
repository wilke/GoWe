package ui

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// seedSWTask is a small convenience wrapper around store.CreateTask that
// fills in the boilerplate fields every task row needs (Inputs/Outputs/Job,
// non-scattered ScatterIndex).
func seedSWTask(t *testing.T, st store.Store, task *model.Task) {
	t.Helper()
	if task.Inputs == nil {
		task.Inputs = map[string]any{}
	}
	if task.Outputs == nil {
		task.Outputs = map[string]any{}
	}
	if task.Job == nil {
		task.Job = map[string]any{}
	}
	if task.ScatterIndex == 0 {
		task.ScatterIndex = -1
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("seed task %s: %v", task.ID, err)
	}
}

func seedSWSubmission(t *testing.T, st store.Store, sub *model.Submission) {
	t.Helper()
	if sub.Inputs == nil {
		sub.Inputs = map[string]any{}
	}
	if err := st.CreateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("seed submission %s: %v", sub.ID, err)
	}
}

// buildSubworkflowTree seeds a three-level submission tree under the given
// store:
//
//	sub_root
//	  task_plain      (local, own task)
//	  task_proxy1     (subworkflow proxy) --> sub_mid
//	                                            task_mid_a   (local)
//	                                            task_mid_proxy (subworkflow proxy) --> sub_leaf
//	                                                                                     task_leaf_a/b/c (local x3)
//
// sub_mid's descendant total is 3 (sub_leaf's own tasks, no further
// nesting). sub_root's descendant total for task_proxy1 is 5: sub_mid's own
// 2 tasks + sub_mid's descendant total of 3.
func buildSubworkflowTree(t *testing.T, st store.Store, owner string) (root *model.Submission, proxy1, midProxy *model.Task) {
	t.Helper()

	root = &model.Submission{
		ID: "sub_root", WorkflowName: "root-wf", State: model.SubmissionStateRunning,
		SubmittedBy: owner, CreatedAt: uiAt(0),
	}
	seedSWSubmission(t, st, root)

	proxy1 = &model.Task{
		ID: "task_proxy1", SubmissionID: root.ID, StepID: "subwf1",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeSubworkflow,
		CreatedAt: uiAt(0), StartedAt: uiAtp(1), CompletedAt: uiAtp(50),
	}
	seedSWTask(t, st, &model.Task{
		ID: "task_plain", SubmissionID: root.ID, StepID: "prep",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal,
		CreatedAt: uiAt(0), StartedAt: uiAtp(0), CompletedAt: uiAtp(5),
	})
	seedSWTask(t, st, proxy1)

	mid := &model.Submission{
		ID: "sub_mid", WorkflowName: "mid-wf", State: model.SubmissionStateCompleted,
		SubmittedBy: owner, ParentTaskID: proxy1.ID, CreatedAt: uiAt(1), CompletedAt: uiAtp(49),
	}
	seedSWSubmission(t, st, mid)

	midProxy = &model.Task{
		ID: "task_mid_proxy", SubmissionID: mid.ID, StepID: "mid-subwf",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeSubworkflow,
		CreatedAt: uiAt(1), StartedAt: uiAtp(2), CompletedAt: uiAtp(48),
	}
	seedSWTask(t, st, &model.Task{
		ID: "task_mid_a", SubmissionID: mid.ID, StepID: "mid-a",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal,
		CreatedAt: uiAt(1), StartedAt: uiAtp(1), CompletedAt: uiAtp(10),
	})
	seedSWTask(t, st, midProxy)

	leaf := &model.Submission{
		ID: "sub_leaf", WorkflowName: "leaf-wf", State: model.SubmissionStateCompleted,
		SubmittedBy: owner, ParentTaskID: midProxy.ID, CreatedAt: uiAt(2), CompletedAt: uiAtp(47),
	}
	seedSWSubmission(t, st, leaf)

	for _, id := range []string{"a", "b", "c"} {
		seedSWTask(t, st, &model.Task{
			ID: "task_leaf_" + id, SubmissionID: leaf.ID, StepID: "leaf-" + id,
			State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal,
			CreatedAt: uiAt(2), StartedAt: uiAtp(2), CompletedAt: uiAtp(20),
		})
	}

	return root, proxy1, midProxy
}

func newSubworkflowTestUI(st store.Store) *UI {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return New(st, logger, Config{})
}

// TestHandleSubmissionDetail_SubworkflowCountsReflectWholeTree proves #225's
// headline example ("4 tasks (+132 in sub-workflows)"): the root submission
// page must show its own task count plus the RECURSIVE descendant total
// across every nested sub-workflow, computed without expanding anything.
func TestHandleSubmissionDetail_SubworkflowCountsReflectWholeTree(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	root, proxy1, _ := buildSubworkflowTree(t, st, testWSUser)
	u := newSubworkflowTestUI(st)

	req := httptest.NewRequest(http.MethodGet, "/submissions/"+root.ID, nil)
	req.SetPathValue("id", root.ID)
	req = withSession(req, testSession())
	w := httptest.NewRecorder()

	u.HandleSubmissionDetail(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// Root: 2 own tasks (task_plain + proxy1), descendant total 5 (sub_mid's
	// 2 own tasks + sub_mid's own descendant total of 3 from sub_leaf).
	if !strings.Contains(body, "2 total, +5 in sub-workflows") {
		t.Errorf("tasks header missing whole-tree count; body=%s", body)
	}
	// The proxy row itself should also show its own branch's count — a
	// second, distinct occurrence of "+5 in sub-workflows" beyond the header
	// (the header assertion above already covers one).
	if got := strings.Count(body, "+5 in sub-workflows"); got < 2 {
		t.Errorf("expected +5 in sub-workflows to appear at least twice (header + proxy row), got %d", got)
	}
	// Collapsed by default: the expand target is present but the fetch
	// happens lazily via hx-get, not eagerly rendered into the page.
	if !strings.Contains(body, "/submissions/"+root.ID+"/tasks/"+proxy1.ID+"/children") {
		t.Error("expand hx-get target for the proxy task not found")
	}
	if !strings.Contains(body, `id="sw-row-`+proxy1.ID+`" class="hidden"`) {
		t.Error("expansion row should be present but collapsed (hidden) by default")
	}
	// The grandchild's tasks must NOT be present in the initial render —
	// only fetched on expand (no N+1 explosion).
	if strings.Contains(body, "task_leaf_a") {
		t.Error("grandchild task rendered eagerly; expansion must be lazy")
	}
}

// TestHandleSubmissionTaskChildren_RendersImmediateChild proves expanding a
// proxy row fetches exactly that node's child submission (steps/tasks), with
// its own nested proxy carrying a correct (3) descendant count for the next
// level down.
func TestHandleSubmissionTaskChildren_RendersImmediateChild(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	root, proxy1, midProxy := buildSubworkflowTree(t, st, testWSUser)
	u := newSubworkflowTestUI(st)

	req := httptest.NewRequest(http.MethodGet, "/submissions/"+root.ID+"/tasks/"+proxy1.ID+"/children", nil)
	req.SetPathValue("id", root.ID)
	req.SetPathValue("tid", proxy1.ID)
	req = withSession(req, testSession())
	w := httptest.NewRecorder()

	u.HandleSubmissionTaskChildren(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, "sub_mid") {
		t.Error("child submission ID not rendered")
	}
	if !strings.Contains(body, "mid-a") {
		t.Error("child's own task (mid-a) not rendered")
	}
	if !strings.Contains(body, "+3 in sub-workflows") {
		t.Errorf("mid-proxy's own descendant count (3) not rendered; body=%s", body)
	}
	if !strings.Contains(body, "/submissions/"+root.ID+"/tasks/"+midProxy.ID+"/children") {
		t.Error("nested expand target for the grandchild proxy not found")
	}
	// Grandchild rows are not fetched yet — only their own lazy expand hook.
	if strings.Contains(body, "task_leaf_a") {
		t.Error("grandchild's own tasks rendered eagerly; must wait for its own expand")
	}
	// No full-page layout/nav should leak into a fragment response.
	if strings.Contains(body, "<nav") {
		t.Error("fragment response should not include the page layout/nav")
	}
}

// TestHandleSubmissionTaskChildren_RecursesToGrandchild expands the second
// level directly, proving the tree drills down correctly and terminates
// (sub_leaf has no further sub-workflow proxies).
func TestHandleSubmissionTaskChildren_RecursesToGrandchild(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	root, _, midProxy := buildSubworkflowTree(t, st, testWSUser)
	u := newSubworkflowTestUI(st)

	req := httptest.NewRequest(http.MethodGet, "/submissions/"+root.ID+"/tasks/"+midProxy.ID+"/children", nil)
	req.SetPathValue("id", root.ID)
	req.SetPathValue("tid", midProxy.ID)
	req = withSession(req, testSession())
	w := httptest.NewRecorder()

	u.HandleSubmissionTaskChildren(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, "sub_leaf") {
		t.Error("grandchild submission ID not rendered")
	}
	for _, step := range []string{"leaf-a", "leaf-b", "leaf-c"} {
		if !strings.Contains(body, step) {
			t.Errorf("leaf task %s not rendered", step)
		}
	}
	if strings.Contains(body, "in sub-workflows") {
		t.Error("sub_leaf has no nested sub-workflows; no descendant badge should render")
	}
}

// TestHandleSubmissionTaskChildren_ForbiddenForOtherUser proves the
// ownership check applies at every nesting level, not just the root — even
// though the request only names a deeply nested proxy task.
func TestHandleSubmissionTaskChildren_ForbiddenForOtherUser(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	root, _, midProxy := buildSubworkflowTree(t, st, testWSUser)
	u := newSubworkflowTestUI(st)

	req := httptest.NewRequest(http.MethodGet, "/submissions/"+root.ID+"/tasks/"+midProxy.ID+"/children", nil)
	req.SetPathValue("id", root.ID)
	req.SetPathValue("tid", midProxy.ID)
	req = withSession(req, &model.Session{ID: "s2", Username: "someone-else@bvbrc", Role: "user"})
	w := httptest.NewRecorder()

	u.HandleSubmissionTaskChildren(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", w.Code)
	}
}

// TestHandleSubmissionDetail_TimingPanelRenders proves the submission view's
// timing section is backed by internal/timing.BuildReport — the same
// computation GET /api/v1/submissions/{id}/timing exposes — with seeded
// timestamps producing deterministic wall/compute values and a queue/run bar
// per task.
func TestHandleSubmissionDetail_TimingPanelRenders(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	sub := &model.Submission{
		ID: "sub_timing_ui", WorkflowName: "timing-wf", State: model.SubmissionStateCompleted,
		SubmittedBy: testWSUser, CreatedAt: uiAt(0), CompletedAt: uiAtp(60),
	}
	seedSWSubmission(t, st, sub)
	seedSWTask(t, st, &model.Task{
		ID: "task_timing_1", SubmissionID: sub.ID, StepID: "compute-step",
		State: model.TaskStateSuccess, ExecutorType: model.ExecutorTypeLocal,
		CreatedAt: uiAt(0), StartedAt: uiAtp(10), CompletedAt: uiAtp(40),
	})

	u := newSubworkflowTestUI(st)
	req := httptest.NewRequest(http.MethodGet, "/submissions/"+sub.ID, nil)
	req.SetPathValue("id", sub.ID)
	req = withSession(req, testSession())
	w := httptest.NewRecorder()

	u.HandleSubmissionDetail(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	for _, want := range []string{"Timing", "Wall", "Scheduling", "Compute", "Critical Path", "60.0s", "compute-step"} {
		if !strings.Contains(body, want) {
			t.Errorf("timing panel missing %q; body=%s", want, body)
		}
	}
	// Queue/run bar segments for the one task (queue 10s, run 30s).
	if !strings.Contains(body, "bg-amber-400") || !strings.Contains(body, "bg-blue-500") {
		t.Error("timing panel missing queue/run bar segments")
	}
	if !strings.Contains(body, "Include sub-workflow trees") {
		t.Error("timing panel missing the include-sub-workflows toggle")
	}
}

// TestHandleSubmissionTimingPanel_IncludeChildren proves the toggle fragment
// recurses into a sub-workflow child's own timing when asked, reusing the
// exact same internal/timing.BuildReport recursion the JSON API's
// ?include_children=true does.
func TestHandleSubmissionTimingPanel_IncludeChildren(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	root, _, _ := buildSubworkflowTree(t, st, testWSUser)
	u := newSubworkflowTestUI(st)

	req := httptest.NewRequest(http.MethodGet, "/submissions/"+root.ID+"/timing-panel?include_children=true", nil)
	req.SetPathValue("id", root.ID)
	req = withSession(req, testSession())
	w := httptest.NewRecorder()

	u.HandleSubmissionTimingPanel(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, "Sub-workflow trees") {
		t.Error("nested children section not rendered with include_children=true")
	}
	if !strings.Contains(body, "sub_mid") {
		t.Error("child submission's timing not nested into the fragment")
	}
	if !strings.Contains(body, "Hide sub-workflow trees") {
		t.Error("toggle should now offer to hide (state carried via IncludeChildren)")
	}
}

// TestHandleSubmissionTimingPanel_Forbidden mirrors the other timing/detail
// handlers' ownership check.
func TestHandleSubmissionTimingPanel_Forbidden(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	sub := &model.Submission{
		ID: "sub_timing_forbidden", WorkflowName: "wf", State: model.SubmissionStateRunning,
		SubmittedBy: testWSUser, CreatedAt: time.Now().UTC(),
	}
	seedSWSubmission(t, st, sub)

	u := newSubworkflowTestUI(st)
	req := httptest.NewRequest(http.MethodGet, "/submissions/"+sub.ID+"/timing-panel", nil)
	req.SetPathValue("id", sub.ID)
	req = withSession(req, &model.Session{ID: "s2", Username: "someone-else@bvbrc", Role: "user"})
	w := httptest.NewRecorder()

	u.HandleSubmissionTimingPanel(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("status=%d, want 403", w.Code)
	}
}

// TestGrafanaLink_RenderedWhenConfigured proves --grafana-url plumbs through
// ui.Config into every authenticated page's nav.
func TestGrafanaLink_RenderedWhenConfigured(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := New(st, logger, Config{GrafanaURL: "https://grafana.example.com/d/gowe"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSession(req, testSession())
	w := httptest.NewRecorder()

	u.HandleDashboard(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, `href="https://grafana.example.com/d/gowe"`) {
		t.Error("Grafana link href not rendered")
	}
	if !strings.Contains(body, `target="_blank"`) {
		t.Error("Grafana link should open in a new tab")
	}
	if !strings.Contains(body, ">Grafana<") && !strings.Contains(body, ">\n                            Grafana") {
		t.Error("Grafana link label not rendered")
	}
}

// TestGrafanaLink_HiddenWhenNotConfigured proves the link is entirely absent
// when --grafana-url is unset (the default), not just visually hidden.
func TestGrafanaLink_HiddenWhenNotConfigured(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := New(st, logger, Config{})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req = withSession(req, testSession())
	w := httptest.NewRecorder()

	u.HandleDashboard(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	if strings.Contains(body, "Grafana") {
		t.Error("Grafana link rendered despite empty GrafanaURL")
	}
}
