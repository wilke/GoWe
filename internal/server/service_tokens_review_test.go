package server

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

// Fixes of the service token security review: no BV-BRC executor, no
// manager rights, header forms, cross-user access, stored admin role,
// invalid stored expiry, start-up WARN for realm rows.

// svcBVBRCStepHint routes the gpDoc step to the BV-BRC executor.
const svcBVBRCStepHint = `        hints:
          gowe:Execution:
            executor: bvbrc
            bvbrc_app_id: Date
`

// svcRegister registers cwl as the caller of token ("" = anonymous) and
// returns the workflow id.
func svcRegister(t *testing.T, srv *Server, token, name, cwl string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"name": name, "cwl": cwl})
	var w *httptest.ResponseRecorder
	var env envelope
	if token == "" {
		w, env = doPost(t, srv, "/api/v1/workflows", string(body))
	} else {
		w, env = doPostAs(t, srv, "/api/v1/workflows", string(body), token)
	}
	if w.Code != http.StatusCreated {
		t.Fatalf("register %s: %d %s", name, w.Code, w.Body.String())
	}
	var wf struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &wf); err != nil || wf.ID == "" {
		t.Fatalf("register %s answer: %v %s", name, err, env.Data)
	}
	return wf.ID
}

// svcSubmit submits wfID as the caller of token ("" = anonymous).
func svcSubmit(t *testing.T, srv *Server, token, wfID string) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"workflow_id": wfID, "inputs": map[string]any{}})
	if token == "" {
		return doPost(t, srv, "/api/v1/submissions", string(body))
	}
	return doPostAs(t, srv, "/api/v1/submissions", string(body), token)
}

// seedSvcSubmission stores a submission of wfID by user in state.
func seedSvcSubmission(t *testing.T, st store.Store, wfID, user string, state model.SubmissionState) string {
	t.Helper()
	sub := &model.Submission{
		ID:           "sub_" + uuid.New().String(),
		WorkflowID:   wfID,
		WorkflowName: "seeded",
		State:        state,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		Labels:       map[string]string{},
		SubmittedBy:  user,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateSubmission(context.Background(), sub); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	return sub.ID
}

// H1: a service identity is refused (403) a workflow with a step on the
// BV-BRC executor; other workflows, and normal users, are not affected.
func TestServiceToken_BVBRCSubmissionRefused(t *testing.T) {
	srv, st, _ := svcServer(t, nil)
	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	bvbrcWF := svcRegister(t, srv, raw, "svc-bvbrc", gpDoc(svcBVBRCStepHint))
	plainWF := svcRegister(t, srv, raw, "svc-plain", gpDoc(""))

	w, env := svcSubmit(t, srv, raw, bvbrcWF)
	if w.Code != http.StatusForbidden || env.Error == nil || env.Error.Code != model.ErrForbidden ||
		env.Error.Message != model.ServiceBVBRCDenied {
		t.Fatalf("service + bvbrc: %d %s, want 403 %q", w.Code, w.Body.String(), model.ServiceBVBRCDenied)
	}
	// Also refused as a dry run (the check comes before it).
	body, _ := json.Marshal(map[string]any{"workflow_id": bvbrcWF, "inputs": map[string]any{}})
	if w, _ := doPostAs(t, srv, "/api/v1/submissions?dry_run=true", string(body), raw); w.Code != http.StatusForbidden {
		t.Errorf("service + bvbrc dry run: %d, want 403", w.Code)
	}
	if w, _ := svcSubmit(t, srv, raw, plainWF); w.Code != http.StatusCreated {
		t.Errorf("service + local workflow: %d %s, want 201", w.Code, w.Body.String())
	}
	if w, _ := svcSubmit(t, srv, userAuthToken, bvbrcWF); w.Code != http.StatusCreated {
		t.Errorf("normal user + bvbrc workflow: %d %s, want 201", w.Code, w.Body.String())
	}
	// Only the local workflow was stored for the service.
	w, env = doGetAs(t, srv, "/api/v1/submissions/", raw)
	if w.Code != http.StatusOK || env.Pagination == nil || env.Pagination.Total != 1 {
		t.Errorf("service submissions: %d %s, want exactly one", w.Code, w.Body.String())
	}

	// A server whose default executor is bvbrc refuses every step of it.
	srv.config.DefaultExecutor = string(model.ExecutorTypeBVBRC)
	if w, env := svcSubmit(t, srv, raw, plainWF); w.Code != http.StatusForbidden || env.Error == nil || env.Error.Message != model.ServiceBVBRCDenied {
		t.Errorf("service, default executor bvbrc: %d %s, want 403", w.Code, w.Body.String())
	}
}

// H1: a retry re-runs the submission for its submitter; a service
// submission whose workflow uses the BV-BRC executor is refused (403) and
// left FAILED; one without it is retried.
func TestServiceToken_RetryBVBRCRefused(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := svcServer(t, nil)
	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	bvbrcWF := svcRegister(t, srv, raw, "svc-bvbrc", gpDoc(svcBVBRCStepHint))
	plainWF := svcRegister(t, srv, raw, "svc-plain", gpDoc(""))

	denied := seedSvcSubmission(t, st, bvbrcWF, svcTestUser, model.SubmissionStateFailed)
	w, env := doPutAs(t, srv, "/api/v1/submissions/"+denied+"/retry", "", raw)
	if w.Code != http.StatusForbidden || env.Error == nil || env.Error.Message != model.ServiceBVBRCDenied {
		t.Fatalf("retry service + bvbrc: %d %s, want 403", w.Code, w.Body.String())
	}
	if sub, err := st.GetSubmission(ctx, denied); err != nil || sub == nil || sub.State != model.SubmissionStateFailed {
		t.Errorf("refused retry changed the submission: %+v %v", sub, err)
	}
	// An admin retrying it is refused as well: it would run as the service.
	if w, _ := doPutAs(t, srv, "/api/v1/submissions/"+denied+"/retry", "", adminAuthToken); w.Code != http.StatusForbidden {
		t.Errorf("admin retry of service + bvbrc: %d, want 403", w.Code)
	}

	allowed := seedSvcSubmission(t, st, plainWF, svcTestUser, model.SubmissionStateFailed)
	if w, _ := doPutAs(t, srv, "/api/v1/submissions/"+allowed+"/retry", "", raw); w.Code != http.StatusOK {
		t.Errorf("retry service + local workflow: %d %s, want 200", w.Code, w.Body.String())
	}
}

// A caller without a provider token (anonymous) is refused the BV-BRC
// executor up front (403), also when --anonymous-executors lists bvbrc, on
// dry run and on retry: the executor needs the submitter's own token. Where
// --anonymous-executors does not list bvbrc, its own refusal comes first, as
// before. A caller with a token is not affected.
func TestAnonymousBVBRCSubmission_Refused(t *testing.T) {
	ctx := context.Background()
	restricted := testServer(WithAnonymousConfig(&AnonymousConfig{Enabled: true, AllowedExecutors: []model.ExecutorType{model.ExecutorTypeLocal}}))
	wfID := svcRegister(t, restricted, "", "anon-bvbrc", gpDoc(svcBVBRCStepHint))
	w, env := svcSubmit(t, restricted, "", wfID)
	if w.Code != http.StatusForbidden || env.Error == nil ||
		env.Error.Message != `anonymous users cannot use executor type "bvbrc"; authentication required` {
		t.Errorf("anonymous + bvbrc, not allowed: %d %s", w.Code, w.Body.String())
	}

	open, st := testServerWithStore() // anonymous may use local, container, worker and bvbrc
	wfID = svcRegister(t, open, "", "anon-bvbrc", gpDoc(svcBVBRCStepHint))
	w, env = svcSubmit(t, open, "", wfID)
	if w.Code != http.StatusForbidden || env.Error == nil || env.Error.Code != model.ErrForbidden ||
		env.Error.Message != model.BVBRCTokenRequired {
		t.Errorf("anonymous + bvbrc, allowed: %d %s, want 403 %q", w.Code, w.Body.String(), model.BVBRCTokenRequired)
	}
	body, _ := json.Marshal(map[string]any{"workflow_id": wfID, "inputs": map[string]any{}})
	if w, env := doPost(t, open, "/api/v1/submissions?dry_run=true", string(body)); w.Code != http.StatusForbidden ||
		env.Error == nil || env.Error.Message != model.BVBRCTokenRequired {
		t.Errorf("anonymous + bvbrc dry run: %d %s, want 403", w.Code, w.Body.String())
	}
	if env := doGet(t, open, "/api/v1/submissions/"); env.Pagination == nil || env.Pagination.Total != 0 {
		t.Errorf("a refused submission was stored: %+v", env.Pagination)
	}

	// Retry of a FAILED anonymous submission of the workflow: refused, left
	// FAILED.
	anonSub := seedSvcSubmission(t, st, wfID, model.AnonymousUser.Username, model.SubmissionStateFailed)
	if w, env := doPut(t, open, "/api/v1/submissions/"+anonSub+"/retry", ""); w.Code != http.StatusForbidden ||
		env.Error == nil || env.Error.Message != model.BVBRCTokenRequired {
		t.Errorf("anonymous retry + bvbrc: %d %s, want 403", w.Code, w.Body.String())
	}
	if sub, err := st.GetSubmission(ctx, anonSub); err != nil || sub == nil || sub.State != model.SubmissionStateFailed {
		t.Errorf("refused retry changed the submission: %+v %v", sub, err)
	}

	// Control: a caller with a token submits it, and retries its own FAILED
	// submission that carries the token.
	const userTok = "un=bvbrc-user@bvbrc|tokenid=u1|expiry=4102444800|sig=x"
	if w, _ := svcSubmit(t, open, userTok, wfID); w.Code != http.StatusCreated {
		t.Errorf("user with a token + bvbrc: %d %s, want 201", w.Code, w.Body.String())
	}
	own := &model.Submission{
		ID: "sub_" + uuid.New().String(), WorkflowID: wfID, WorkflowName: "seeded",
		State: model.SubmissionStateFailed, Inputs: map[string]any{}, Outputs: map[string]any{},
		Labels: map[string]string{}, SubmittedBy: "bvbrc-user@bvbrc", UserToken: userTok,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateSubmission(ctx, own); err != nil {
		t.Fatalf("CreateSubmission: %v", err)
	}
	if w, _ := doPutAs(t, open, "/api/v1/submissions/"+own.ID+"/retry", "", userTok); w.Code != http.StatusOK {
		t.Errorf("user retry of its own bvbrc submission: %d %s, want 200", w.Code, w.Body.String())
	}
}

// probeHeaders sends one request with the given header values to h.
func probeHeaders(t *testing.T, h http.Handler, headers http.Header) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	var env envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

// The header forms, with anonymous access on (so any fall-through to the
// anonymous path would show as a 200 with the anonymous user):
//   - "Bearer " is matched case-sensitively; "bearer "/"BEARER " are taken
//     verbatim as the credential, which is then not a well-formed service
//     token: 401, never anonymous. Extra white space after "Bearer " and a
//     bare token are tolerated.
//   - More than one Authorization (or X-MG-RAST-Token) header is refused
//     with 401, whatever the values: before, only the first was read, so an
//     empty first header made a request carrying a service token anonymous.
//   - Authorization wins over X-MG-RAST-Token.
func TestServiceToken_HeaderForms(t *testing.T) {
	st := testAuthStore(t)
	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	h, got := authProbe(st, authProbeOpts{anon: &AnonymousConfig{Enabled: true}})
	const bvbrcTok = "un=alice@bvbrc|tokenid=x|expiry=4102444800|sig=y"

	cases := []struct {
		name     string
		headers  http.Header
		wantUser string // "" = 401 invalid token format, handler not reached
	}{
		{"Bearer", http.Header{"Authorization": {"Bearer " + raw}}, svcTestUser},
		{"bare token", http.Header{"Authorization": {raw}}, svcTestUser},
		{"Bearer with extra spaces", http.Header{"Authorization": {"Bearer   " + raw + "  "}}, svcTestUser},
		{"lower-case bearer", http.Header{"Authorization": {"bearer " + raw}}, ""},
		{"upper-case BEARER", http.Header{"Authorization": {"BEARER " + raw}}, ""},
		{"two Authorization headers, both the token", http.Header{"Authorization": {"Bearer " + raw, "Bearer " + raw}}, ""},
		{"empty Authorization, then the token", http.Header{"Authorization": {"", "Bearer " + raw}}, ""},
		{"user token, then the service token", http.Header{"Authorization": {"Bearer " + bvbrcTok, "Bearer " + raw}}, ""},
		{"service token, then a user token", http.Header{"Authorization": {"Bearer " + raw, "Bearer " + bvbrcTok}}, ""},
		{"two X-MG-RAST-Token headers", http.Header{"X-Mg-Rast-Token": {raw, raw}}, ""},
		{"Authorization wins over X-MG-RAST-Token", http.Header{"Authorization": {"Bearer " + raw}, "X-Mg-Rast-Token": {bvbrcTok}}, svcTestUser},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := got()
			rec, env := probeHeaders(t, h, c.headers)
			if c.wantUser == "" {
				wantInvalidToken(t, rec, env)
				if got() != before {
					t.Errorf("the request reached the handler as %+v", got().User)
				}
				return
			}
			uc := got()
			if rec.Code != http.StatusOK || uc == nil || uc.User == nil || uc.User.Username != c.wantUser || !uc.IsService {
				t.Errorf("status %d, context %+v; want %s as a service", rec.Code, uc, c.wantUser)
			}
		})
	}
}

// A service identity cannot read, stream, cancel, retry or delete another
// user's submission, its tasks or logs: it gets what any other non-owner
// gets (403), and the listing does not show it. The owner can (control).
func TestServiceToken_CannotAccessOthersSubmissions(t *testing.T) {
	srv, st, _ := svcServer(t, nil)
	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	wfID := svcRegister(t, srv, userAuthToken, "owner-wf", gpDoc(""))
	subID := seedSvcSubmission(t, st, wfID, "tester", model.SubmissionStateFailed)
	const secretOut = "stdout-of-tester-only"
	task := &model.Task{
		ID:           "task_" + uuid.New().String(),
		SubmissionID: subID,
		StepID:       "s",
		State:        model.TaskStateFailed,
		ExecutorType: model.ExecutorTypeLocal,
		Stdout:       secretOut,
		Inputs:       map[string]any{},
		Outputs:      map[string]any{},
		ScatterIndex: -1,
		CreatedAt:    time.Now().UTC(),
	}
	if err := st.CreateTask(context.Background(), task); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	base := "/api/v1/submissions/" + subID
	routes := []struct{ method, path string }{
		{"GET", base},
		{"GET", base + "/timing"},
		{"GET", base + "/tasks/"},
		{"GET", base + "/tasks/" + task.ID},
		{"GET", base + "/tasks/" + task.ID + "/logs"},
		{"GET", base + "/tasks/" + task.ID + "/attempts"},
		{"GET", "/api/v1/sse/submissions/" + subID},
		{"PUT", base + "/cancel"},
		{"PUT", base + "/retry"},
		{"DELETE", base + "/secrets"},
		{"DELETE", base},
	}
	send := func(method, path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		return rec
	}
	for _, rt := range routes {
		svc := send(rt.method, rt.path, raw)
		other := send(rt.method, rt.path, plainToken)
		if svc.Code != http.StatusForbidden && svc.Code != http.StatusNotFound {
			t.Errorf("%s %s as the service: %d, want 403 or 404", rt.method, rt.path, svc.Code)
		}
		if svc.Code != other.Code {
			t.Errorf("%s %s: the service got %d, another user %d; want the same", rt.method, rt.path, svc.Code, other.Code)
		}
		if strings.Contains(svc.Body.String(), secretOut) {
			t.Errorf("%s %s: the service read the owner's task output", rt.method, rt.path)
		}
	}
	w, env := doGetAs(t, srv, "/api/v1/submissions/", raw)
	if w.Code != http.StatusOK || env.Pagination == nil || env.Pagination.Total != 0 || strings.Contains(w.Body.String(), subID) {
		t.Errorf("the service's listing shows another user's submission: %s", w.Body.String())
	}
	if sub, _ := st.GetSubmission(context.Background(), subID); sub == nil || sub.State != model.SubmissionStateFailed {
		t.Errorf("the submission was changed or deleted: %+v", sub)
	}

	// Control: the owner reads the logs.
	if rec := send("GET", base+"/tasks/"+task.ID+"/logs", userAuthToken); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), secretOut) {
		t.Errorf("owner logs: %d %s", rec.Code, rec.Body.String())
	}
}

// A user row in the realm stored with role admin (e.g. made before the realm
// was reserved, with another provider) gives a valid service token of that
// name no admin right, end to end; the start-up check names the row.
func TestServiceToken_StoredAdminRowEndToEnd(t *testing.T) {
	ctx := context.Background()
	srv, st, _ := svcServer(t, nil)
	u, err := st.GetOrCreateUser(ctx, svcTestUser, model.ProviderBVBRC)
	if err != nil {
		t.Fatal(err)
	}
	u.Role = model.RoleAdmin
	if err := st.UpdateUser(ctx, u); err != nil {
		t.Fatal(err)
	}
	// Rows the start-up check must not name: a service row, a normal user.
	if _, err := st.GetOrCreateUser(ctx, "other@service.gowe", model.ProviderService); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetOrCreateUser(ctx, "alice@bvbrc", model.ProviderBVBRC); err != nil {
		t.Fatal(err)
	}

	// L2: one WARN naming the row, names only.
	var logs bytes.Buffer
	WarnServiceRealmUsers(ctx, st, slog.New(slog.NewTextHandler(&logs, nil)))
	out := logs.String()
	if strings.Count(out, "level=WARN") != 1 || !strings.Contains(out, svcTestUser) ||
		strings.Contains(out, "other@service.gowe") || strings.Contains(out, "alice@bvbrc") {
		t.Errorf("start-up WARN:\n%s", out)
	}
	logs.Reset()
	WarnServiceRealmUsers(ctx, testAuthStore(t), slog.New(slog.NewTextHandler(&logs, nil)))
	if logs.Len() != 0 {
		t.Errorf("WARN on a store without realm rows:\n%s", logs.String())
	}

	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	for _, c := range []struct{ method, path, body string }{
		{"GET", "/api/v1/admin/users", ""},
		{"GET", "/api/v1/admin/tasks/active", ""},
		{"GET", "/api/v1/admin/service-tokens", ""},
		{"POST", "/api/v1/admin/service-tokens", `{"name":"other"}`},
		{"GET", "/api/v1/admin/worker-groups", ""},
		{"PUT", "/api/v1/admin/users/alice@bvbrc/role", `{"role":"admin"}`},
	} {
		req := httptest.NewRequest(c.method, c.path, strings.NewReader(c.body))
		req.Header.Set("Authorization", "Bearer "+raw)
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as the service with a stored admin row: %d, want 403", c.method, c.path, rec.Code)
		}
	}
	// Not admin for submission access either.
	wfID := svcRegister(t, srv, userAuthToken, "owner-wf", gpDoc(""))
	subID := seedSvcSubmission(t, st, wfID, "tester", model.SubmissionStateFailed)
	if w, _ := doGetAs(t, srv, "/api/v1/submissions/"+subID, raw); w.Code != http.StatusForbidden {
		t.Errorf("another user's submission: %d, want 403", w.Code)
	}
	if w, env := doGetAs(t, srv, "/api/v1/submissions/", raw); w.Code != http.StatusOK || env.Pagination == nil || env.Pagination.Total != 0 {
		t.Errorf("listing as the service: %d %s, want only its own (none)", w.Code, w.Body.String())
	}
	if stored, _ := st.GetUser(ctx, svcTestUser); stored == nil || stored.IsAdmin() {
		t.Errorf("stored row = %+v; IsAdmin must be false for the realm", stored)
	}
}

// M1: a service identity cannot be named a manager (400 naming it) but can
// be a user; a manager entry stored anyway gives it no manager right on the
// manager routes, while the rule's other managers keep theirs.
func TestServiceToken_NeverWorkerGroupManager(t *testing.T) {
	ctx := context.Background()
	srv, st, policy := newWorkerGroupServer(t)

	w, env := wgDo(t, srv, "PUT", workerGroupsPath+"/gpu", `{"users":["alice"],"managers":["mgr","`+svcTestUser+`"]}`, adminAuthToken)
	if w.Code != http.StatusBadRequest || env.Error == nil || !strings.Contains(env.Error.Message, svcTestUser) {
		t.Fatalf("PUT with a service manager: %d %s, want 400 naming it", w.Code, w.Body.String())
	}
	if w, _ := wgDo(t, srv, "PUT", workerGroupsPath+"/gpu", `{"users":["`+svcTestUser+`"],"managers":["mgr"]}`, adminAuthToken); w.Code != http.StatusOK {
		t.Fatalf("PUT with the service as a user: %d %s", w.Code, w.Body.String())
	}

	// Stored behind the API's back (SQL, an older server): the reload
	// ignores the entry and does not fail.
	if err := st.UpsertWorkerGroupPolicy(ctx, &model.WorkerGroupPolicy{Group: "gpu", Users: []string{svcTestUser},
		Managers: []string{"mgr", svcTestUser}, UpdatedBy: "sql", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	policy.SetLogger(slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)))
	if err := policy.Reload(ctx, st); err != nil {
		t.Fatalf("Reload with a stored service manager: %v", err)
	}
	if policy.IsManager(svcTestUser, "gpu") || !policy.IsManager("mgr", "gpu") || !policy.Allowed(svcTestUser, "gpu") {
		t.Fatal("the stored service manager must be ignored, mgr kept, the service still a user")
	}

	raw, _ := mintServiceToken(t, st, svcTestName, nil)
	w, env = wgDo(t, srv, "GET", managedGroupsPath, "", raw)
	if w.Code != http.StatusOK {
		t.Fatalf("GET managed groups as the service: %d %s", w.Code, w.Body.String())
	}
	if body := decode[managedGroupsBody](t, env); len(body.Groups) != 0 {
		t.Errorf("the service manages %+v, want none", body.Groups)
	}
	for _, c := range []struct{ method, path, body string }{
		{"GET", managedGroupsPath + "/gpu", ""},
		{"POST", managedGroupsPath + "/gpu/users", `{"username":"mallory"}`},
		{"GET", managedGroupsPath + "/gpu/keys", ""},
		{"POST", managedGroupsPath + "/gpu/keys", `{"label":"x"}`},
		{"GET", managedGroupsPath + "/gpu/workers", ""},
	} {
		if w, _ := wgDo(t, srv, c.method, c.path, c.body, raw); w.Code != http.StatusForbidden {
			t.Errorf("%s %s as the service: %d, want 403", c.method, c.path, w.Code)
		}
	}
	if keys, err := st.ListWorkerKeys(ctx); err != nil || len(keys) != 0 {
		t.Errorf("worker keys after the service's attempts: %d, %v; want none", len(keys), err)
	}
	// Control: the other manager keeps the right.
	if w, _ := wgDo(t, srv, "GET", managedGroupsPath+"/gpu", "", mgrToken); w.Code != http.StatusOK {
		t.Errorf("GET gpu as mgr: %d %s, want 200", w.Code, w.Body.String())
	}

	// requestActor: never admin for a service identity, whatever its role.
	req := httptest.NewRequest("GET", "/", nil)
	req = req.WithContext(context.WithValue(req.Context(), ctxKeyUserAuth,
		&UserContext{User: &model.User{Username: svcTestUser, Role: model.RoleAdmin}, IsService: true}))
	if actor := requestActor(req); actor.IsAdmin || actor.Username != svcTestUser {
		t.Errorf("requestActor = %+v, want the service, not admin", actor)
	}
	if srv.mayManageGroup(requestActor(req), "gpu") {
		t.Error("mayManageGroup is true for a service identity")
	}
}

// invalidExpiryStore returns every service token as the store reads an
// empty or unparsable expires_at.
type invalidExpiryStore struct{ store.Store }

func (s *invalidExpiryStore) GetServiceTokenByHash(ctx context.Context, hash string) (*model.ServiceToken, error) {
	tok, err := s.Store.GetServiceTokenByHash(ctx, hash)
	if tok != nil {
		zero := time.Time{}
		tok.ExpiresAt, tok.ExpiryInvalid = &zero, true
	}
	return tok, err
}

// L5: a token whose stored expiry is invalid is refused with the generic
// 401 and an ERROR naming the token id (never the token or its hash).
func TestServiceToken_InvalidStoredExpiryRefused(t *testing.T) {
	st := testAuthStore(t)
	raw, tok := mintServiceToken(t, st, svcTestName, nil)
	var logs bytes.Buffer
	h, got := authProbe(&invalidExpiryStore{st}, authProbeOpts{
		anon:   &AnonymousConfig{Enabled: true},
		logger: slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	rec, env := probe(t, h, "Authorization", "Bearer "+raw)
	wantInvalidToken(t, rec, env)
	if got() != nil {
		t.Errorf("the request went through as %+v", got().User)
	}
	out := logs.String()
	if !strings.Contains(out, "level=ERROR") || !strings.Contains(out, tok.ID) {
		t.Errorf("want an ERROR with the token id:\n%s", out)
	}
	if strings.Contains(out, raw) || strings.Contains(out, tok.TokenHash) {
		t.Errorf("the log holds the token or its hash:\n%s", out)
	}
	// The same token with its real (NULL) expiry authenticates.
	h, got = authProbe(st, authProbeOpts{})
	if rec, _ := probe(t, h, "Authorization", "Bearer "+raw); rec.Code != http.StatusOK || got() == nil || !got().IsService {
		t.Errorf("control: status %d", rec.Code)
	}
}
