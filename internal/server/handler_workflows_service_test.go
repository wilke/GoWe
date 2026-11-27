package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// The registration rule for service workflows (gowe:Service, experimental):
// one step without scatter, a container, a non-default worker group, an int
// port input (400), and the caller a server admin or a manager of the group
// (403). newManagedGroupServer gives group g the manager mgr.

// svcHints returns tool hints with gowe:Service for group (none when ""),
// with a DockerRequirement when docker is set.
func svcHints(group string, docker bool) string {
	var b strings.Builder
	b.WriteString("    hints:\n")
	if docker {
		b.WriteString("      DockerRequirement:\n        dockerPull: vllm.sif\n")
	}
	if group != "" {
		b.WriteString("      gowe:Execution:\n        worker_group: " + group + "\n")
	}
	b.WriteString("      gowe:Service:\n        port_input: port\n")
	return b.String()
}

// svcWorkflowDoc returns a packed workflow whose step s runs tool srv (an int
// input port) with toolHints; stepExtra is inserted in step s, extraSteps
// after it.
func svcWorkflowDoc(toolHints, stepExtra, extraSteps string) string {
	return `cwlVersion: v1.2
$graph:
  - id: srv
    class: CommandLineTool
    baseCommand: [serve]
    inputs:
      port:
        type: int
    outputs:
      out:
        type: string
` + toolHints + `  - id: main
    class: Workflow
    inputs:
      port: int
    outputs: {}
    steps:
      s:
        run: "#srv"
` + stepExtra + `        in:
          port: port
        out: [out]
` + extraSteps
}

// svcDoc is the valid service workflow for group g.
func svcDoc() string { return svcWorkflowDoc(svcHints("g", true), "", "") }

// withToolInput adds input id of type typ to tool srv of an svcWorkflowDoc.
func withToolInput(doc, id, typ string) string {
	const ports = "    inputs:\n      port:\n        type: int\n"
	return strings.Replace(doc, ports, ports+"      "+id+":\n        type: "+typ+"\n", 1)
}

// wfRegisterNoForce is a registration request without force (dedup applies).
func wfRegisterNoForce(name, cwl string) string {
	b, _ := json.Marshal(map[string]any{"name": name, "cwl": cwl})
	return string(b)
}

// createdWorkflow decodes a create response: the workflow and its
// deduplicated flag.
func createdWorkflow(t *testing.T, env envelope) (model.Workflow, bool) {
	t.Helper()
	var wf model.Workflow
	var flag struct {
		Deduplicated bool `json:"deduplicated"`
	}
	if err := json.Unmarshal(env.Data, &wf); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(env.Data, &flag); err != nil {
		t.Fatal(err)
	}
	return wf, flag.Deduplicated
}

// seedServiceCopy stores a copy of workflow fromID under a new id, owned by
// owner, and returns the new id.
func seedServiceCopy(t *testing.T, st store.Store, fromID, owner string) string {
	t.Helper()
	ctx := context.Background()
	wf, err := st.GetWorkflow(ctx, fromID)
	if err != nil || wf == nil {
		t.Fatalf("GetWorkflow(%s): %v", fromID, err)
	}
	wf.ID = "wf_" + uuid.New().String()
	wf.CreatedBy = owner
	if err := st.CreateWorkflow(ctx, wf); err != nil {
		t.Fatal(err)
	}
	return wf.ID
}

// wfRegisterBody is a workflow registration request; force bypasses dedup, so every
// call stores (201) or is refused.
func wfRegisterBody(name, cwl string) string {
	b, _ := json.Marshal(map[string]any{"name": name, "cwl": cwl, "force": true})
	return string(b)
}

func TestServiceWorkflow_RegisterCallers(t *testing.T) {
	srv, st, _ := newManagedGroupServer(t)
	svcRaw, _ := mintServiceToken(t, st, svcTestName, nil)
	tests := []struct {
		name  string
		token string
		want  int
	}{
		{"admin", adminAuthToken, http.StatusCreated},
		{"manager of the group", mgrToken, http.StatusCreated},
		{"ordinary user", plainToken, http.StatusForbidden},
		{"service identity", svcRaw, http.StatusForbidden},
		{"anonymous", "", http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc", svcDoc()), tt.token)
			if w.Code != tt.want {
				t.Fatalf("register: %d %s, want %d", w.Code, w.Body.String(), tt.want)
			}
			if tt.want == http.StatusForbidden {
				if env.Error == nil || env.Error.Code != model.ErrForbidden || !strings.Contains(env.Error.Message, `"g"`) {
					t.Errorf("error = %+v, want FORBIDDEN naming group g", env.Error)
				}
				return
			}
			var wf model.Workflow
			if err := json.Unmarshal(env.Data, &wf); err != nil {
				t.Fatal(err)
			}
			stored, err := st.GetWorkflow(context.Background(), wf.ID)
			if err != nil || stored == nil {
				t.Fatalf("GetWorkflow: %v", err)
			}
			if h := stored.Steps[0].Hints; h == nil || h.Service == nil || h.Service.PortInput != "port" ||
				h.Service.Health.Path != "/health" {
				t.Errorf("stored step hints = %+v, want the service spec with defaults", h)
			}
		})
	}

	// A manager of g is not a manager of h.
	doc := svcWorkflowDoc(svcHints("h", true), "", "")
	if w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc-h", doc), mgrToken); w.Code != http.StatusForbidden ||
		env.Error == nil || env.Error.Code != model.ErrForbidden {
		t.Errorf("manager of g registering for h: %d %s, want 403", w.Code, w.Body.String())
	}
}

func TestServiceWorkflow_RegisterInvalid(t *testing.T) {
	srv, _, _ := newManagedGroupServer(t)
	secondStep := `      t:
        run: "#srv"
        in:
          port: port
        out: [out]
`
	tests := []struct {
		name    string
		doc     string
		wantErr string
	}{
		{"two steps", svcWorkflowDoc(svcHints("g", true), "", secondStep), "exactly one step"},
		{"scatter", svcWorkflowDoc(svcHints("g", true), "        scatter: port\n", ""), "must not scatter"},
		{"no DockerRequirement", svcWorkflowDoc(svcHints("g", false), "", ""), "must run in a container"},
		{"default group", svcWorkflowDoc(svcHints("default", true), "", ""), "worker group other than"},
		{"no group", svcWorkflowDoc(svcHints("", true), "", ""), "worker group other than"},
		{"executor local", strings.Replace(svcDoc(), "worker_group: g", "worker_group: g\n        executor: local", 1),
			`gowe:Execution.executor "local" is not allowed`},
		{"port input not an input", strings.Replace(svcDoc(), "port_input: port", "port_input: nope", 1), `port_input "nope" is not an input`},
		{"port input not int", strings.Replace(withToolInput(svcDoc(), "name", "string"), "port_input: port", "port_input: name", 1),
			`port_input "name" must name an input of type int, it is "string"`},
		{"port input optional int", strings.Replace(withToolInput(svcDoc(), "maybe", "int?"), "port_input: port", "port_input: maybe", 1),
			`port_input "maybe" must name an input of type int, it is "int?"`},
		{"group not a valid name", svcWorkflowDoc(svcHints(`"default "`, true), "", ""), `"default " is not a valid group name`},
		{"unknown key", strings.Replace(svcDoc(), "port_input: port", "port_input: port\n        stop_grase: 5", 1), `unknown key "stop_grase"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Even an admin is refused: the structure comes first.
			w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc", tt.doc), adminAuthToken)
			if w.Code != http.StatusBadRequest || env.Error == nil || env.Error.Code != model.ErrValidation ||
				!strings.Contains(env.Error.Message, tt.wantErr) {
				t.Fatalf("register: %d %s, want 400 containing %q", w.Code, w.Body.String(), tt.wantErr)
			}
		})
	}
}

// A workflow without the hint registers and updates as before, for every
// caller.
func TestServiceWorkflow_OrdinaryWorkflowUnaffected(t *testing.T) {
	srv, st, _ := newManagedGroupServer(t)
	svcRaw, _ := mintServiceToken(t, st, svcTestName, nil)
	plain := svcWorkflowDoc(`    hints:
      DockerRequirement:
        dockerPull: vllm.sif
      gowe:Execution:
        worker_group: g
`, "", "")
	for _, c := range []struct{ name, token string }{
		{"admin", adminAuthToken}, {"manager", mgrToken}, {"user", plainToken},
		{"service identity", svcRaw}, {"anonymous", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("plain-"+c.name, plain), c.token)
			if w.Code != http.StatusCreated {
				t.Fatalf("register: %d %s", w.Code, w.Body.String())
			}
			var wf model.Workflow
			if err := json.Unmarshal(env.Data, &wf); err != nil {
				t.Fatal(err)
			}
			if h := wf.Steps[0].Hints; h == nil || h.Service != nil {
				t.Errorf("step hints = %+v, want hints without a service", h)
			}
			body, _ := json.Marshal(map[string]any{"cwl": gpDoc("")})
			if w, _ := mgDo(t, srv, "PUT", "/api/v1/workflows/"+wf.ID, string(body), c.token); w.Code != http.StatusOK {
				t.Errorf("update: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

func TestServiceWorkflow_Update(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	put := func(id, body, token string) (int, envelope) {
		w, env := mgDo(t, srv, "PUT", "/api/v1/workflows/"+id, body, token)
		return w.Code, env
	}
	cwlBody := func(cwl string) string {
		b, _ := json.Marshal(map[string]any{"cwl": cwl})
		return string(b)
	}

	// A normal workflow of plain cannot be turned into a service one by plain.
	w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("normal", gpDoc("")), plainToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("register normal: %d %s", w.Code, w.Body.String())
	}
	var normal model.Workflow
	if err := json.Unmarshal(env.Data, &normal); err != nil {
		t.Fatal(err)
	}
	if code, env := put(normal.ID, cwlBody(svcDoc()), plainToken); code != http.StatusForbidden ||
		env.Error == nil || env.Error.Code != model.ErrForbidden {
		t.Errorf("plain turns its workflow into a service: %d %+v, want 403", code, env.Error)
	}
	// The new content is checked structurally too.
	if code, _ := put(normal.ID, cwlBody(svcWorkflowDoc(svcHints("default", true), "", "")), adminAuthToken); code != http.StatusBadRequest {
		t.Errorf("admin publishes an invalid service version: %d, want 400", code)
	}
	// An admin may.
	if code, env := put(normal.ID, cwlBody(svcDoc()), adminAuthToken); code != http.StatusOK {
		t.Errorf("admin turns it into a service: %d %+v", code, env.Error)
	}

	// An existing service workflow owned by plain, who does not manage its
	// group (seeded in the store): plain may change it in no way.
	w, env = mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc", svcDoc()), mgrToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("register service: %d %s", w.Code, w.Body.String())
	}
	var created model.Workflow
	if err := json.Unmarshal(env.Data, &created); err != nil {
		t.Fatal(err)
	}
	owned, err := st.GetWorkflow(ctx, created.ID)
	if err != nil || owned == nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	owned.ID = "wf_" + uuid.New().String()
	owned.CreatedBy = "plain"
	if err := st.CreateWorkflow(ctx, owned); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ name, body string }{
		{"into an ordinary workflow", cwlBody(gpDoc(""))},
		{"into another service", cwlBody(svcDoc())},
		{"metadata only", `{"description":"renamed"}`},
	} {
		if code, env := put(owned.ID, c.body, plainToken); code != http.StatusForbidden ||
			env.Error == nil || env.Error.Code != model.ErrForbidden || !strings.Contains(env.Error.Message, "service workflow") {
			t.Errorf("owner plain, %s: %d %+v, want 403", c.name, code, env.Error)
		}
	}
	// An admin may change it.
	if code, env := put(owned.ID, cwlBody(gpDoc("")), adminAuthToken); code != http.StatusOK {
		t.Errorf("admin changes the service workflow: %d %+v", code, env.Error)
	}
	// Its manager, as owner, may too.
	if code, env := put(created.ID, `{"description":"by mgr"}`, mgrToken); code != http.StatusOK {
		t.Errorf("manager changes their service workflow: %d %+v", code, env.Error)
	}
}

// The group name is checked with the server's group-name rule: "Default" is
// a valid name distinct from "default" (allowed), "default " is not (400).
func TestServiceWorkflow_GroupName(t *testing.T) {
	srv, _, _ := newManagedGroupServer(t)
	w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc-Default", svcWorkflowDoc(svcHints("Default", true), "", "")), adminAuthToken)
	if w.Code != http.StatusCreated {
		t.Errorf(`group "Default": %d %s, want 201`, w.Code, w.Body.String())
	}
	w, env = mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc-space", svcWorkflowDoc(svcHints(`"default "`, true), "", "")), adminAuthToken)
	if w.Code != http.StatusBadRequest || env.Error == nil || env.Error.Code != model.ErrValidation ||
		!strings.Contains(env.Error.Message, `"default " is not a valid group name`) {
		t.Errorf(`group "default ": %d %s, want 400 naming the group`, w.Code, w.Body.String())
	}
}

// Dedup of a service workflow returns an existing row only when that row is
// a valid service workflow itself; a row with the same text stored without
// the service (an older parse) is no hit, and a new row is stored.
func TestServiceWorkflow_DedupChecksStoredRow(t *testing.T) {
	ctx := context.Background()

	t.Run("row without the service is not returned", func(t *testing.T) {
		srv, st, _ := newManagedGroupServer(t)
		doc := svcDoc()
		graph, err := srv.parser.ParseGraph([]byte(doc))
		if err != nil {
			t.Fatal(err)
		}
		old, err := srv.parser.ToModel(graph, "svc")
		if err != nil {
			t.Fatal(err)
		}
		for i := range old.Steps {
			if h := old.Steps[i].Hints; h != nil {
				h.Service = nil
			}
			if ti := old.Steps[i].ToolInline; ti != nil && ti.Hints != nil {
				ti.Hints.Service = nil
			}
		}
		old.ID = "wf_" + uuid.New().String()
		old.Class = "Workflow"
		old.RawCWL = doc
		old.ContentHash = computeContentHash(parseSchemaVersion, doc)
		old.CreatedBy = "plain"
		if err := st.CreateWorkflow(ctx, old); err != nil {
			t.Fatal(err)
		}

		w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterNoForce("svc", doc), adminAuthToken)
		if w.Code != http.StatusCreated {
			t.Fatalf("register: %d %s, want 201 (a new row)", w.Code, w.Body.String())
		}
		got, dedup := createdWorkflow(t, env)
		if dedup || got.ID == old.ID {
			t.Fatalf("got row %s (deduplicated %v), want a new row, not %s", got.ID, dedup, old.ID)
		}
		if h := got.Steps[0].Hints; h == nil || h.Service == nil {
			t.Errorf("new row step hints = %+v, want the service", h)
		}
	})

	t.Run("service row is returned", func(t *testing.T) {
		srv, _, _ := newManagedGroupServer(t)
		w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterNoForce("svc", svcDoc()), adminAuthToken)
		if w.Code != http.StatusCreated {
			t.Fatalf("first register: %d %s", w.Code, w.Body.String())
		}
		first, _ := createdWorkflow(t, env)
		w, env = mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterNoForce("svc", svcDoc()), adminAuthToken)
		if w.Code != http.StatusOK {
			t.Fatalf("second register: %d %s, want 200", w.Code, w.Body.String())
		}
		if again, dedup := createdWorkflow(t, env); !dedup || again.ID != first.ID {
			t.Errorf("second register = %s (deduplicated %v), want %s deduplicated", again.ID, dedup, first.ID)
		}
	})

	// The rule is checked before dedup: content a manager registered is not
	// handed to an ordinary user who posts it again without force.
	t.Run("ordinary user after a manager", func(t *testing.T) {
		srv, _, _ := newManagedGroupServer(t)
		if w, _ := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc", svcDoc()), mgrToken); w.Code != http.StatusCreated {
			t.Fatalf("manager register: %d %s", w.Code, w.Body.String())
		}
		w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterNoForce("svc", svcDoc()), plainToken)
		if w.Code != http.StatusForbidden || env.Error == nil || env.Error.Code != model.ErrForbidden {
			t.Errorf("ordinary user, same CWL without force: %d %s, want 403", w.Code, w.Body.String())
		}
	})
}

// PATCH labels and DELETE of an existing service workflow are restricted
// like PUT: its owner, if not an admin or a manager of the group, is
// refused; an ordinary workflow is unaffected.
func TestServiceWorkflow_LabelsAndDelete(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	labels := `{"labels":{"team":"a"}}`

	w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc", svcDoc()), mgrToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("register service: %d %s", w.Code, w.Body.String())
	}
	created, _ := createdWorkflow(t, env)
	owned := seedServiceCopy(t, st, created.ID, "plain")

	for _, c := range []struct{ method, path, body string }{
		{"PATCH", "/api/v1/workflows/" + owned + "/labels", labels},
		{"DELETE", "/api/v1/workflows/" + owned, ""},
	} {
		w, env := mgDo(t, srv, c.method, c.path, c.body, plainToken)
		if w.Code != http.StatusForbidden || env.Error == nil || env.Error.Code != model.ErrForbidden ||
			!strings.Contains(env.Error.Message, "service workflow") {
			t.Errorf("owner plain, %s %s: %d %s, want 403", c.method, c.path, w.Code, w.Body.String())
		}
	}
	if wf, err := st.GetWorkflow(ctx, owned); err != nil || wf == nil || len(wf.Labels) != 0 {
		t.Fatalf("after the refusals: %+v, %v; want the row unchanged", wf, err)
	}

	// The manager (owner of created) may label and delete it; an admin may
	// delete plain's copy.
	if w, _ := mgDo(t, srv, "PATCH", "/api/v1/workflows/"+created.ID+"/labels", labels, mgrToken); w.Code != http.StatusOK {
		t.Errorf("manager labels: %d %s", w.Code, w.Body.String())
	}
	if w, _ := mgDo(t, srv, "DELETE", "/api/v1/workflows/"+created.ID, "", mgrToken); w.Code != http.StatusOK {
		t.Errorf("manager deletes: %d %s", w.Code, w.Body.String())
	}
	if w, _ := mgDo(t, srv, "DELETE", "/api/v1/workflows/"+owned, "", adminAuthToken); w.Code != http.StatusOK {
		t.Errorf("admin deletes: %d %s", w.Code, w.Body.String())
	}

	// An ordinary workflow of plain: labels and delete as before.
	w, env = mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("normal", gpDoc("")), plainToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("register normal: %d %s", w.Code, w.Body.String())
	}
	normal, _ := createdWorkflow(t, env)
	if w, _ := mgDo(t, srv, "PATCH", "/api/v1/workflows/"+normal.ID+"/labels", labels, plainToken); w.Code != http.StatusOK {
		t.Errorf("plain labels its ordinary workflow: %d %s", w.Code, w.Body.String())
	}
	if w, _ := mgDo(t, srv, "DELETE", "/api/v1/workflows/"+normal.ID, "", plainToken); w.Code != http.StatusOK {
		t.Errorf("plain deletes its ordinary workflow: %d %s", w.Code, w.Body.String())
	}
}

// The web UI's workflow delete applies the same rule through
// GroupAdmin.MayChangeWorkflow: a plain user is refused for a service
// workflow, a manager of its group and a server admin are not; an ordinary
// workflow is deleted as before.
func TestServiceWorkflow_UIDelete(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := newManagedGroupServer(t)
	w, env := mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("svc", svcDoc()), mgrToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("register service: %d %s", w.Code, w.Body.String())
	}
	created, _ := createdWorkflow(t, env)
	byMgr := seedServiceCopy(t, st, created.ID, "mgr")
	byAdmin := seedServiceCopy(t, st, created.ID, "plain")

	exists := func(id string) bool {
		wf, err := st.GetWorkflow(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return wf != nil
	}

	plain := uiCookie(t, st, "plain", false)
	wantStatus(t, "plain UI delete of a service workflow", uiDo(t, srv, "DELETE", "/workflows/"+created.ID, nil, plain), http.StatusForbidden)
	if !exists(created.ID) {
		t.Fatal("a plain user deleted a service workflow through the UI")
	}
	wantStatus(t, "manager UI delete", uiDo(t, srv, "DELETE", "/workflows/"+byMgr, nil, uiCookie(t, st, "mgr", false)), http.StatusOK)
	wantStatus(t, "admin UI delete", uiDo(t, srv, "DELETE", "/workflows/"+byAdmin, nil, uiCookie(t, st, "admin", true)), http.StatusOK)
	if exists(byMgr) || exists(byAdmin) {
		t.Error("the manager's or the admin's UI delete left the row")
	}

	w, env = mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("normal", gpDoc("")), plainToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("register normal: %d %s", w.Code, w.Body.String())
	}
	normal, _ := createdWorkflow(t, env)
	// Ownership: another non-admin user is refused, the owner is not; a
	// legacy workflow without a recorded owner stays deletable.
	wantStatus(t, "other user's UI delete of an ordinary workflow", uiDo(t, srv, "DELETE", "/workflows/"+normal.ID, nil, uiCookie(t, st, "mgr", false)), http.StatusForbidden)
	if !exists(normal.ID) {
		t.Fatal("a non-owner deleted an ordinary workflow through the UI")
	}
	wantStatus(t, "plain UI delete of an ordinary workflow", uiDo(t, srv, "DELETE", "/workflows/"+normal.ID, nil, plain), http.StatusOK)
	if exists(normal.ID) {
		t.Error("the owner's UI delete left the row")
	}
	w, env = mgDo(t, srv, "POST", "/api/v1/workflows", wfRegisterBody("legacy", gpDoc("")), plainToken)
	if w.Code != http.StatusCreated {
		t.Fatalf("register legacy: %d %s", w.Code, w.Body.String())
	}
	legacySrc, _ := createdWorkflow(t, env)
	legacy := seedServiceCopy(t, st, legacySrc.ID, "")
	wantStatus(t, "UI delete of a workflow without an owner", uiDo(t, srv, "DELETE", "/workflows/"+legacy, nil, uiCookie(t, st, "mgr", false)), http.StatusOK)
}
