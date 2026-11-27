package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/pkg/model"
)

// The submission page shows one line for a service task that has a status:
// state, scheme://host:port, GPU indexes, ready since (HTML-escaped).
func TestHandleSubmissionDetail_ServiceStatusLine(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()

	if err := st.CreateWorkflow(ctx, &model.Workflow{
		ID: "wf_svc", Name: "svc", Class: "Workflow", Steps: []model.Step{{ID: "serve"}},
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateSubmission(ctx, &model.Submission{
		ID: "sub_svc", WorkflowID: "wf_svc", WorkflowName: "svc", State: model.SubmissionStateRunning,
		Inputs: map[string]any{}, Outputs: map[string]any{}, Labels: map[string]string{},
		SubmittedBy: testWSUser, CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	readyAt := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	staleAt := now.Add(-10 * time.Minute)
	for _, tk := range []struct {
		id     string
		status *model.ServiceStatus
	}{
		{"task_svc", &model.ServiceStatus{State: model.ServiceStateReady, Host: "gpu01", Port: 8000, Scheme: "http",
			GPUs: []model.ServiceGPU{{Index: 0}, {Index: 1}}, ReadyAt: &readyAt, UpdatedAt: now}},
		// Stored bypassing validation: the page must escape it.
		{"task_evil", &model.ServiceStatus{State: model.ServiceStateStarting, Host: "<script>x</script>", UpdatedAt: now}},
		// Last reported 10 minutes ago: marked stale.
		{"task_stale", &model.ServiceStatus{State: model.ServiceStateUnhealthy, Host: "gpu02", Port: 9000, UpdatedAt: staleAt}},
		{"task_none", nil},
	} {
		if err := st.CreateTask(ctx, &model.Task{
			ID: tk.id, SubmissionID: "sub_svc", StepID: "serve", State: model.TaskStateRunning,
			ExecutorType: model.ExecutorTypeWorker, ExternalID: "wrk_svc", StartedAt: &now,
			Inputs: map[string]any{}, Outputs: map[string]any{}, Job: map[string]any{}, ScatterIndex: -1,
			RuntimeHints:  &model.RuntimeHints{WorkerGroup: "llm", Service: &model.ServiceSpec{PortInput: "port"}},
			ServiceStatus: tk.status,
		}); err != nil {
			t.Fatalf("create task %s: %v", tk.id, err)
		}
	}

	req := httptest.NewRequest(http.MethodGet, "/submissions/sub_svc", nil)
	req.SetPathValue("id", "sub_svc")
	req = withSession(req, testSession())
	w := httptest.NewRecorder()
	newTestUI(st).HandleSubmissionDetail(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", w.Code, w.Body.String())
	}
	body := w.Body.String()

	// A fresh status has no staleness suffix: the line ends at the </div>.
	if want := "service: ready  http://gpu01:8000  GPUs 0,1  ready since 2026-11-27T10:00:00Z</div>"; !strings.Contains(body, want) {
		t.Errorf("page lacks %q", want)
	}
	if strings.Contains(body, "<script>x</script>") {
		t.Error("a reported host is rendered unescaped")
	}
	if !strings.Contains(body, "service: starting  http://&lt;script&gt;x&lt;/script&gt;") {
		t.Error("the escaped status line of task_evil is missing")
	}
	if want := "service: unhealthy  http://gpu02:9000 (last reported " + staleAt.UTC().Format(time.RFC3339) + ")</div>"; !strings.Contains(body, want) {
		t.Errorf("page lacks the stale line %q", want)
	}
	if n := strings.Count(body, "last reported"); n != 1 {
		t.Errorf("stale marks = %d, want 1 (task_stale only)", n)
	}
	if n := strings.Count(body, "service: "); n != 3 {
		t.Errorf("service lines = %d, want 3 (none for the task without a status)", n)
	}
}
