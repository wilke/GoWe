package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/toolhash"
	"github.com/me/gowe/pkg/model"
)

const blessToolBody = `    class: CommandLineTool
    baseCommand: [python3, run.py]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
      InitialWorkDirRequirement:
        listing:
          - entryname: run.py
            entry: |
              import os
              print("hello")
    hints:
      gowe:Execution:
        inject_bvbrc_token: true
    stdout: out.txt
    inputs:
      x: string
    outputs:
      out:
        type: stdout
`

func blessPacked(toolID string) string {
	return "cwlVersion: v1.2\n$graph:\n  - id: " + toolID + "\n" + blessToolBody + `  - id: main
    class: Workflow
    inputs: {x: string}
    outputs: {}
    steps:
      s:
        run: "#` + toolID + `"
        in: {x: x}
        out: [out]
`
}

var (
	shaA = strings.Repeat("ab", 32)
	shaB = strings.Repeat("cd", 32)
)

func blessingRouter(s *Server, user *model.User) http.Handler {
	r := chi.NewRouter()
	r.Use(requestIDMiddleware)
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			if user != nil {
				ctx := context.WithValue(req.Context(), ctxKeyUserAuth, &UserContext{User: user})
				req = req.WithContext(ctx)
			}
			next.ServeHTTP(w, req)
		})
	})
	r.Route("/api/v1/admin", func(r chi.Router) {
		r.Use(requireAdmin(s.logger))
		r.Route("/tool-blessings", s.toolBlessingRoutes)
	})
	return r
}

func seedWorkflow(t *testing.T, st store.Store, id, name, raw string) {
	t.Helper()
	if err := st.CreateWorkflow(context.Background(), &model.Workflow{
		ID: id, Name: name, Class: "Workflow", RawCWL: raw, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatalf("seed workflow: %v", err)
	}
}

type blessingEnv struct {
	t      *testing.T
	srv    *Server
	st     store.Store
	router http.Handler
}

func newBlessingEnv(t *testing.T) *blessingEnv {
	srv, st := testServerWithStore()
	return &blessingEnv{t: t, srv: srv, st: st,
		router: blessingRouter(srv, &model.User{Username: "admin", Role: model.RoleAdmin})}
}

func (e *blessingEnv) do(method, path string, body any) (*httptest.ResponseRecorder, envelope) {
	e.t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, "/api/v1/admin/tool-blessings"+path, rdr)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	var env envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &env)
	return rec, env
}

func decode[T any](t *testing.T, env envelope) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(env.Data, &v); err != nil {
		t.Fatalf("decode data %s: %v", env.Data, err)
	}
	return v
}

func (e *blessingEnv) bless(wf string, extra map[string]any) (*httptest.ResponseRecorder, envelope) {
	body := map[string]any{"workflow": wf, "image_sha256s": []string{shaA}, "note": "ok"}
	for k, v := range extra {
		body[k] = v
	}
	return e.do("POST", "", body)
}

func TestToolBlessing_NonAdminForbidden(t *testing.T) {
	srv := testServer()
	user := blessingRouter(srv, &model.User{Username: "bob", Role: model.RoleUser})
	for _, c := range []struct{ method, path string }{
		{"GET", ""}, {"POST", ""}, {"GET", "/pending"}, {"POST", "/inspect"},
		{"POST", "/tb_1/images"}, {"DELETE", "/tb_1"},
	} {
		rec := httptest.NewRecorder()
		user.ServeHTTP(rec, httptest.NewRequest(c.method, "/api/v1/admin/tool-blessings"+c.path, strings.NewReader("{}")))
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s as user: status=%d, want 403", c.method, c.path, rec.Code)
		}
	}
	anon := blessingRouter(srv, nil)
	rec := httptest.NewRecorder()
	anon.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/admin/tool-blessings", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated: status=%d, want 401", rec.Code)
	}
}

func TestToolBlessing_Inspect(t *testing.T) {
	e := newBlessingEnv(t)
	raw := blessPacked("helper")
	seedWorkflow(t, e.st, "wf_1", "bvbrc-helper", raw)

	rec, env := e.do("POST", "/inspect", map[string]any{"workflow": "bvbrc-helper"})
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", rec.Code, rec.Body.String())
	}
	got := decode[struct {
		ToolID        string            `json:"tool_id"`
		ToolHash      string            `json:"tool_hash"`
		ImageRef      string            `json:"image_ref"`
		RequestsToken bool              `json:"requests_token"`
		Summary       model.ToolSummary `json:"summary"`
	}](t, env)
	want, err := toolhash.DigestStandalone(raw, "helper")
	if err != nil {
		t.Fatal(err)
	}
	if got.ToolHash != want.Hash || got.ToolID != "helper" || got.ImageRef != "python.sif" || !got.RequestsToken {
		t.Errorf("inspect result: %+v want hash %s", got, want.Hash)
	}
	if len(got.Summary.Scripts) != 1 || !strings.Contains(got.Summary.Scripts[0].Content, `print("hello")`) {
		t.Errorf("scripts must be returned verbatim: %+v", got.Summary.Scripts)
	}
	var sawStdout bool
	for _, f := range got.Summary.Flags {
		if strings.HasPrefix(f, toolhash.FlagStdoutOutput) {
			sawStdout = true
		}
	}
	if !sawStdout {
		t.Errorf("declared stdout must be flagged: %v", got.Summary.Flags)
	}

	// Inspect of a raw bare tool document gives the same hash as the packed registered copy.
	bare := "cwlVersion: v1.2\nid: whatever\n" + strings.ReplaceAll(blessToolBody, "\n    ", "\n")[4:]
	rec, env = e.do("POST", "/inspect", map[string]any{"cwl": bare})
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect cwl: %d %s", rec.Code, rec.Body.String())
	}
	if h := decode[struct {
		ToolHash string `json:"tool_hash"`
	}](t, env).ToolHash; h != want.Hash {
		t.Errorf("bare tool hash %s != packed registered %s", h, want.Hash)
	}

	// Errors.
	if rec, _ := e.do("POST", "/inspect", map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("empty inspect: %d", rec.Code)
	}
	if rec, _ := e.do("POST", "/inspect", map[string]any{"workflow": "nope"}); rec.Code != http.StatusNotFound {
		t.Errorf("unknown workflow: %d", rec.Code)
	}
	if rec, _ := e.do("POST", "/inspect", map[string]any{"workflow": "bvbrc-helper", "cwl": bare}); rec.Code != http.StatusBadRequest {
		t.Errorf("both workflow and cwl: %d", rec.Code)
	}
}

// TestToolBlessing_StandaloneEqualsGowePackedCopy: the hash of a registered
// tool equals the hash of the copy resolveGoweRefs inlines into a pipeline.
func TestToolBlessing_StandaloneEqualsGowePackedCopy(t *testing.T) {
	e := newBlessingEnv(t)
	seedWorkflow(t, e.st, "wf_1", "bvbrc-helper", blessPacked("helper"))
	standalone, _ := toolhash.DigestStandalone(blessPacked("helper"), "helper")

	pipeline := `cwlVersion: v1.2
class: Workflow
inputs: {x: string}
outputs: {}
steps:
  s:
    run: gowe://bvbrc-helper
    in: {x: x}
    out: [out]
`
	resolved, err := resolveGoweRefs(context.Background(), e.st, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	res, err := toolhash.DigestForStep(resolved, "s", nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Hash != standalone.Hash {
		t.Errorf("gowe://-packed copy %s != standalone %s", res.Hash, standalone.Hash)
	}
}

func TestToolBlessing_CreateListRevoke(t *testing.T) {
	e := newBlessingEnv(t)
	seedWorkflow(t, e.st, "wf_1", "bvbrc-helper", blessPacked("helper"))
	hash, _ := toolhash.DigestStandalone(blessPacked("helper"), "helper")

	// Pending shows the unblessed token-requesting tool.
	rec, env := e.do("GET", "/pending", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("pending: %d", rec.Code)
	}
	pend := decode[struct {
		Total   int `json:"total"`
		Pending []struct {
			ToolHash     string `json:"tool_hash"`
			WorkflowName string `json:"workflow_name"`
		} `json:"pending"`
	}](t, env)
	if pend.Total != 1 || pend.Pending[0].ToolHash != hash.Hash || pend.Pending[0].WorkflowName != "bvbrc-helper" {
		t.Fatalf("pending: %+v", pend)
	}

	// Create.
	rec, env = e.bless("bvbrc-helper", map[string]any{"env_names": []string{"BVBRC_TOKEN"}, "worker_groups": []string{"default"}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	b := decode[model.ToolBlessing](t, env)
	if b.ToolHash != hash.Hash || b.ImageRef != "python.sif" || len(b.ImageSHA256s) != 1 || b.ImageSHA256s[0] != shaA ||
		b.BlessedBy != "admin" || !b.Active() || b.EnvNames[0] != "BVBRC_TOKEN" || b.Note != "ok" || len(b.Summary.Scripts) != 1 {
		t.Errorf("blessing: %+v", b)
	}

	// No longer pending.
	_, env = e.do("GET", "/pending", nil)
	if decode[struct {
		Total int `json:"total"`
	}](t, env).Total != 0 {
		t.Error("blessed tool must leave the pending list")
	}

	// Duplicate -> 409.
	if rec, _ := e.bless("bvbrc-helper", nil); rec.Code != http.StatusConflict {
		t.Errorf("duplicate bless: %d", rec.Code)
	}

	// Add image.
	rec, env = e.do("POST", "/"+b.ID+"/images", map[string]any{"image_sha256": "sha256:" + shaB})
	if rec.Code != http.StatusOK {
		t.Fatalf("add image: %d %s", rec.Code, rec.Body.String())
	}
	if got := decode[model.ToolBlessing](t, env); len(got.ImageSHA256s) != 2 || !got.AllowsImage(shaB) {
		t.Errorf("after add: %v", got.ImageSHA256s)
	}
	if rec, _ := e.do("POST", "/"+b.ID+"/images", map[string]any{"image_sha256": "zz"}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad digest: %d", rec.Code)
	}
	if rec, _ := e.do("POST", "/tb_missing/images", map[string]any{"image_sha256": shaB}); rec.Code != http.StatusNotFound {
		t.Errorf("missing blessing: %d", rec.Code)
	}

	// List.
	_, env = e.do("GET", "", nil)
	if l := decode[struct {
		Total int `json:"total"`
	}](t, env); l.Total != 1 {
		t.Errorf("list: %+v", l)
	}

	// Revoke requires a reason.
	if rec, _ := e.do("DELETE", "/"+b.ID, map[string]any{}); rec.Code != http.StatusBadRequest {
		t.Errorf("revoke without reason: %d", rec.Code)
	}
	if rec, _ := e.do("DELETE", "/tb_missing", map[string]any{"reason": "x"}); rec.Code != http.StatusNotFound {
		t.Errorf("revoke missing: %d", rec.Code)
	}
	// Revoke by tool hash.
	rec, _ = e.do("DELETE", "/"+hash.Hash, map[string]any{"reason": "found echo"})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := e.do("DELETE", "/"+b.ID, map[string]any{"reason": "again"}); rec.Code != http.StatusConflict {
		t.Errorf("double revoke: %d", rec.Code)
	}
	if rec, _ := e.do("POST", "/"+b.ID+"/images", map[string]any{"image_sha256": strings.Repeat("ef", 32)}); rec.Code != http.StatusConflict {
		t.Errorf("add image to revoked: %d", rec.Code)
	}

	// Revoked rows are kept and visible on request.
	_, env = e.do("GET", "", nil)
	if decode[struct {
		Total int `json:"total"`
	}](t, env).Total != 0 {
		t.Error("default list hides revoked")
	}
	_, env = e.do("GET", "?include_revoked=true", nil)
	all := decode[struct {
		Total     int                  `json:"total"`
		Blessings []model.ToolBlessing `json:"blessings"`
	}](t, env)
	if all.Total != 1 || all.Blessings[0].Active() || all.Blessings[0].RevokeReason != "found echo" || all.Blessings[0].RevokedBy != "admin" {
		t.Errorf("revoked row: %+v", all)
	}

	// Tool is pending again, and can be blessed afresh.
	_, env = e.do("GET", "/pending", nil)
	if decode[struct {
		Total int `json:"total"`
	}](t, env).Total != 1 {
		t.Error("revoked tool returns to pending")
	}
	if rec, _ := e.bless("bvbrc-helper", nil); rec.Code != http.StatusCreated {
		t.Errorf("re-bless after revoke: %d", rec.Code)
	}
}

func TestToolBlessing_CreateValidation(t *testing.T) {
	e := newBlessingEnv(t)
	seedWorkflow(t, e.st, "wf_1", "bvbrc-helper", blessPacked("helper"))
	good, _ := toolhash.DigestStandalone(blessPacked("helper"), "helper")

	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"no images", map[string]any{"workflow": "bvbrc-helper"}, 400},
		{"empty images", map[string]any{"workflow": "bvbrc-helper", "image_sha256s": []string{}}, 400},
		{"bad digest", map[string]any{"workflow": "bvbrc-helper", "image_sha256s": []string{"latest"}}, 400},
		{"bad env", map[string]any{"workflow": "bvbrc-helper", "image_sha256s": []string{shaA}, "env_names": []string{"A B"}}, 400},
		{"hash mismatch", map[string]any{"workflow": "bvbrc-helper", "image_sha256s": []string{shaA}, "tool_hash": "sha256:" + strings.Repeat("0", 64)}, 400},
		{"neither workflow nor hash", map[string]any{"image_sha256s": []string{shaA}}, 400},
		{"unknown workflow", map[string]any{"workflow": "zzz", "image_sha256s": []string{shaA}}, 404},
		{"unknown hash", map[string]any{"tool_hash": "sha256:" + strings.Repeat("1", 64), "image_sha256s": []string{shaA}}, 404},
		{"matching hash ok", map[string]any{"workflow": "bvbrc-helper", "image_sha256s": []string{shaA}, "tool_hash": good.Hash}, 201},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec, _ := e.do("POST", "", c.body)
			if rec.Code != c.want {
				t.Errorf("status=%d want %d body=%s", rec.Code, c.want, rec.Body.String())
			}
		})
	}
	// Nothing was created by the failed attempts.
	list, _ := e.st.ListToolBlessings(context.Background(), true)
	if len(list) != 1 {
		t.Errorf("only the final valid request creates a blessing, got %d", len(list))
	}
}

func TestToolBlessing_CreateByHashOnly(t *testing.T) {
	e := newBlessingEnv(t)
	seedWorkflow(t, e.st, "wf_1", "bvbrc-helper", blessPacked("helper"))
	h, _ := toolhash.DigestStandalone(blessPacked("helper"), "helper")
	rec, env := e.do("POST", "", map[string]any{"tool_hash": h.Hash, "image_sha256s": []string{"sha256:" + strings.ToUpper(shaA)}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("by hash: %d %s", rec.Code, rec.Body.String())
	}
	if b := decode[model.ToolBlessing](t, env); b.ImageSHA256s[0] != shaA {
		t.Errorf("digest must be normalised: %v", b.ImageSHA256s)
	}
}

func TestToolBlessing_RejectsRequirementsSpellingAndMissingImage(t *testing.T) {
	e := newBlessingEnv(t)
	reqSpelling := strings.Replace(blessToolBody, "    hints:\n      gowe:Execution:\n        inject_bvbrc_token: true\n",
		"      gowe:Execution:\n        inject_bvbrc_token: true\n", 1)
	seedWorkflow(t, e.st, "wf_req", "req-spelling", "cwlVersion: v1.2\n$graph:\n  - id: t\n"+reqSpelling+"  - id: main\n    class: Workflow\n    inputs: {x: string}\n    outputs: {}\n    steps:\n      s:\n        run: '#t'\n        in: {x: x}\n        out: [out]\n")
	rec, env := e.bless("req-spelling", nil)
	if rec.Code != http.StatusBadRequest || env.Error == nil || !strings.Contains(env.Error.Message, "requirements") {
		t.Errorf("gowe:Execution under requirements: %d %s", rec.Code, rec.Body.String())
	}

	noImage := strings.Replace(blessToolBody, "      DockerRequirement:\n        dockerPull: python.sif\n", "", 1)
	seedWorkflow(t, e.st, "wf_noimg", "no-image", "cwlVersion: v1.2\n$graph:\n  - id: t\n"+noImage+"  - id: main\n    class: Workflow\n    inputs: {x: string}\n    outputs: {}\n    steps:\n      s:\n        run: '#t'\n        in: {x: x}\n        out: [out]\n")
	rec, env = e.bless("no-image", nil)
	if rec.Code != http.StatusBadRequest || env.Error == nil || !strings.Contains(env.Error.Message, "pinned image") {
		t.Errorf("no image: %d %s", rec.Code, rec.Body.String())
	}
}

func TestToolBlessing_EventsMetric(t *testing.T) {
	reg := metrics.NewRegistry(metrics.Config{})
	srv, st := testServerWithStore(WithMetrics(reg))
	e := &blessingEnv{t: t, srv: srv, st: st, router: blessingRouter(srv, &model.User{Username: "admin", Role: model.RoleAdmin})}
	seedWorkflow(t, st, "wf_1", "bvbrc-helper", blessPacked("helper"))
	rec, env := e.bless("bvbrc-helper", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d", rec.Code)
	}
	b := decode[model.ToolBlessing](t, env)
	e.do("POST", "/"+b.ID+"/images", map[string]any{"image_sha256": shaB})
	e.do("DELETE", "/"+b.ID, map[string]any{"reason": "r"})

	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, mf := range mfs {
		if mf.GetName() != "gowe_tool_blessing_events_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			got[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
		}
	}
	for _, a := range []string{"create", "add_image", "revoke"} {
		if got[a] != 1 {
			t.Errorf("gowe_tool_blessing_events_total{action=%q} = %v, want 1 (all: %v)", a, got[a], got)
		}
	}
}

func TestToolBlessing_UnpinnedImageAndGroupValidation(t *testing.T) {
	e := newBlessingEnv(t)
	mk := func(pull string) string {
		return "cwlVersion: v1.2\n$graph:\n  - id: t\n" +
			strings.Replace(blessToolBody, "python.sif", pull, 1) +
			"  - id: main\n    class: Workflow\n    inputs: {x: string}\n    outputs: {}\n    steps:\n      s:\n        run: '#t'\n        in: {x: x}\n        out: [out]\n"
	}
	pinned := "dxkb/foo@sha256:" + strings.Repeat("a", 64)
	seedWorkflow(t, e.st, "wf_tag", "tagged", mk("dxkb/foo:latest"))
	seedWorkflow(t, e.st, "wf_dig", "digest", mk(pinned))
	seedWorkflow(t, e.st, "wf_sif", "sif", mk("/scout/containers/python.sif"))

	rec, env := e.bless("tagged", nil)
	if rec.Code != http.StatusBadRequest || env.Error == nil || !strings.Contains(env.Error.Message, "not pinned") {
		t.Errorf("tag ref must be refused: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := e.bless("digest", nil); rec.Code != http.StatusCreated {
		t.Errorf("digest-pinned ref: %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := e.bless("sif", map[string]any{"worker_groups": []string{"bad group!"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad group name: %d", rec.Code)
	}
	if rec, _ := e.bless("sif", map[string]any{"worker_groups": []string{"anlseq", "default"}}); rec.Code != http.StatusCreated {
		t.Errorf("good .sif + groups: %d %s", rec.Code, rec.Body.String())
	}
}

func TestToolBlessing_WorkerKeyAloneIs401(t *testing.T) {
	srv := testServer()
	r := blessingRouter(srv, nil)
	req := httptest.NewRequest("GET", "/api/v1/admin/tool-blessings", nil)
	req.Header.Set("X-Worker-Key", "gwk_anything")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status=%d, want 401", rec.Code)
	}
}
