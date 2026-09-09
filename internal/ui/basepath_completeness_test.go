package ui

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// basePathUnderTest is the completeness test's configured mount prefix —
// deliberately multi-segment, matching a realistic reverse-proxy path (e.g.
// nginx location /ragstack/services/gowe/ui/).
const basePathUnderTest = "/ragstack/services/gowe/ui"

// scanForUnprefixedURLs asserts body contains zero unprefixed root-absolute
// emission-surface attributes or fetch()/EventSource() argument URLs. An
// external https:// URL (e.g. the Grafana nav link) is never flagged since
// it can never match the leading '="/'  pattern this checks for.
func scanForUnprefixedURLs(t *testing.T, source, body, basePath string) {
	t.Helper()

	attrRe := regexp.MustCompile(`(href|src|action|hx-get|hx-post|hx-delete)="(/[^"]*)"`)
	for _, m := range attrRe.FindAllStringSubmatch(body, -1) {
		attr, value := m[1], m[2]
		if strings.HasPrefix(value, basePath+"/") || value == basePath {
			continue
		}
		t.Errorf("%s: unprefixed root-absolute %s=%q (want prefixed with %q)", source, attr, value, basePath)
	}

	fetchRe := regexp.MustCompile(`(?:fetch|EventSource|WebSocket)\(\s*['"](/[^'"]*)['"]`)
	for _, m := range fetchRe.FindAllStringSubmatch(body, -1) {
		value := m[1]
		if strings.HasPrefix(value, basePath+"/") || value == basePath {
			continue
		}
		t.Errorf("%s: unprefixed root-absolute fetch/EventSource/WebSocket URL %q (want prefixed with %q)", source, value, basePath)
	}

	// window.location(.href) assignments to a root-absolute literal.
	locRe := regexp.MustCompile(`window\.location(?:\.href)?\s*=\s*['"](/[^'"]*)['"]`)
	for _, m := range locRe.FindAllStringSubmatch(body, -1) {
		value := m[1]
		if strings.HasPrefix(value, basePath+"/") || value == basePath {
			continue
		}
		t.Errorf("%s: unprefixed root-absolute window.location URL %q (want prefixed with %q)", source, value, basePath)
	}
}

// seedCompletenessData populates the store with enough of every entity type
// (workflow with varied input types, a three-level sub-workflow submission
// tree, a worker, a label, a worker key) that every page template in the
// `templates` map can be reached through its real handler with real data,
// rather than synthetic/empty data that would under-exercise the emitted
// markup.
func seedCompletenessData(t *testing.T, st *store.SQLiteStore) (wf *model.Workflow, root *model.Submission, proxyTask, midProxyTask *model.Task) {
	t.Helper()
	ctx := context.Background()

	wf = &model.Workflow{
		ID:         "wf_basepath_complete",
		Name:       "basepath-completeness-wf",
		CWLVersion: "v1.2",
		Class:      "Workflow",
		RawCWL:     "cwlVersion: v1.2\nclass: Workflow\n",
		Inputs: []model.WorkflowInput{
			{ID: "reads", Type: "File", Required: true, Doc: "input reads"},
			{ID: "outdir", Type: "Directory"},
			{ID: "samples", Type: "string[]"},
			{ID: "threads", Type: "int", Default: "4"},
		},
		Outputs: []model.WorkflowOutput{
			{ID: "result", Type: "File"},
		},
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}

	root, proxyTask, midProxyTask = buildSubworkflowTree(t, st, "admin")
	// Point the root submission at the seeded workflow so submissions/detail
	// exercises its DAG-visualization branch too.
	root.WorkflowID = wf.ID
	if err := st.UpdateSubmission(ctx, root); err != nil {
		t.Fatalf("attach workflow to root submission: %v", err)
	}

	worker := &model.Worker{
		ID:           "worker_basepath",
		Name:         "basepath-worker",
		Hostname:     "host.example",
		Group:        "default",
		State:        model.WorkerStateOnline,
		Runtime:      model.RuntimeDocker,
		LastSeen:     time.Now().UTC(),
		RegisteredAt: time.Now().UTC(),
	}
	if err := st.CreateWorker(ctx, worker); err != nil {
		t.Fatalf("seed worker: %v", err)
	}

	label := &model.LabelVocabulary{
		ID:        "label_basepath",
		Key:       "project",
		Value:     "completeness",
		Color:     "blue",
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateLabelVocabulary(ctx, label); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	rawKey, hash, prefix, err := model.GenerateWorkerKey()
	if err != nil {
		t.Fatalf("generate worker key: %v", err)
	}
	_ = rawKey
	key := &model.WorkerKey{
		ID:        "wk_basepath",
		Label:     "basepath-key",
		KeyHash:   hash,
		KeyPrefix: prefix,
		Groups:    []string{"default"},
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateWorkerKey(ctx, key); err != nil {
		t.Fatalf("seed worker key: %v", err)
	}

	return wf, root, proxyTask, midProxyTask
}

// TestBasePathCompleteness is the #250 merge gate: construct the UI mounted
// under a base path, render every page template and fragment through the
// real render paths (real handlers wherever practical — sessions created
// directly via the SessionManager, exactly like the rest of this package's
// tests — falling back to a direct renderTemplate call only for the one
// page, workspace/browser, whose handler requires a live BV-BRC network
// call), and assert the rendered HTML contains zero unprefixed
// root-absolute href/src/action/hx-* attributes or fetch/EventSource/
// WebSocket/window.location URLs.
func TestBasePathCompleteness(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	u := New(st, logger, Config{
		BasePath:   basePathUnderTest,
		GrafanaURL: "https://grafana.example.com/d/gowe-overview",
	})

	wf, root, proxyTask, _ := seedCompletenessData(t, st)

	r := chi.NewRouter()
	u.RegisterRoutes(r)

	_, adminCookie := newTestSession(t, u, "admin", string(model.RoleAdmin))

	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(adminCookie)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		return rec
	}

	// One task off the sub-workflow tree, for the task-logs page.
	tasks, err := st.ListTasksBySubmission(context.Background(), root.ID)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("list root tasks: %v (n=%d)", err, len(tasks))
	}
	var plainTaskID string
	for _, tk := range tasks {
		if tk.ExecutorType != model.ExecutorTypeSubworkflow {
			plainTaskID = tk.ID
			break
		}
	}
	if plainTaskID == "" {
		t.Fatal("no non-subworkflow task found on root submission")
	}

	routes := map[string]string{
		"login":                  "/login",
		"dashboard":              "/",
		"workflows/list":         "/workflows",
		"workflows/create":       "/workflows/new",
		"workflows/detail":       "/workflows/" + wf.ID,
		"workflows/edit":         "/workflows/" + wf.ID + "/edit",
		"submissions/list":       "/submissions",
		"submissions/create":     "/submissions/new?workflow_id=" + wf.ID,
		"submissions/detail":     "/submissions/" + root.ID,
		"submissions/task_logs":  "/submissions/" + root.ID + "/tasks/" + plainTaskID + "/logs",
		"timing_panel fragment":  "/submissions/" + root.ID + "/timing-panel",
		"subworkflow_children":   "/submissions/" + root.ID + "/tasks/" + proxyTask.ID + "/children",
		"workers":                "/workers",
		"admin/stats":            "/admin",
		"admin/health":           "/admin/health",
		"admin/tasks":            "/admin/tasks",
		"admin/labels":           "/admin/labels",
		"admin/fleet":            "/admin/fleet",
		"admin/keys":             "/admin/worker-keys",
		"admin/outputs":          "/admin/outputs",
		"error (workflow 404)":   "/workflows/does-not-exist",
		"error (submission 404)": "/submissions/does-not-exist",
	}

	for name, path := range routes {
		t.Run(name, func(t *testing.T) {
			rec := get(path)
			if rec.Code >= 500 {
				t.Fatalf("GET %s: status=%d, body=%s", path, rec.Code, rec.Body.String())
			}
			scanForUnprefixedURLs(t, name+" ("+path+")", rec.Body.String(), basePathUnderTest)
		})
	}

	// login (unauthenticated) is a separate case: no admin cookie.
	t.Run("login (unauthenticated)", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/login", nil)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code >= 500 {
			t.Fatalf("GET /login: status=%d, body=%s", rec.Code, rec.Body.String())
		}
		scanForUnprefixedURLs(t, "login (unauthenticated)", rec.Body.String(), basePathUnderTest)
	})

	// workspace/browser cannot go through its real handler (it makes a live
	// BV-BRC RPC call); render it directly with crafted data, exactly the
	// shape HandleWorkspace passes (see handlers.go), so its markup is still
	// covered.
	t.Run("workspace/browser", func(t *testing.T) {
		var buf bytes.Buffer
		err := renderTemplate(&buf, "workspace/browser", map[string]any{
			"Title":    "Workspace - GoWe",
			"Session":  nil,
			"Path":     "/admin@bvbrc/home",
			"BasePath": basePathUnderTest,
			"Items": []struct {
				Path string
				Name string
				Type string
			}{
				{Path: "/admin@bvbrc/home/data", Name: "data", Type: "folder"},
				{Path: "/admin@bvbrc/home/result.txt", Name: "result.txt", Type: "reads"},
			},
		})
		if err != nil {
			t.Fatalf("render workspace/browser: %v", err)
		}
		scanForUnprefixedURLs(t, "workspace/browser", buf.String(), basePathUnderTest)
	})
}
