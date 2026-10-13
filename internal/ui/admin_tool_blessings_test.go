package ui

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/pkg/model"
)

const uiBlessCWL = `cwlVersion: v1.2
$graph:
  - id: helper
    class: CommandLineTool
    baseCommand: [python3, run.py]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
      InitialWorkDirRequirement:
        listing:
          - entryname: run.py
            entry: |
              print("<b>hello</b>")
    hints:
      gowe:Execution:
        inject_bvbrc_token: true
    stdout: out.txt
    inputs: {x: string}
    outputs:
      out:
        type: stdout
  - id: main
    class: Workflow
    inputs: {x: string}
    outputs: {}
    steps:
      s:
        run: "#helper"
        in: {x: x}
        out: [out]
`

var uiSHA = strings.Repeat("ab", 32)

func seedBlessWF(t *testing.T, st store.Store) {
	t.Helper()
	if err := st.CreateWorkflow(context.Background(), &model.Workflow{
		ID: "wf_1", Name: "bvbrc-helper", Class: "Workflow", RawCWL: uiBlessCWL,
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
}

func postForm(u *UI, h http.HandlerFunc, path string, form url.Values, id string) *httptest.ResponseRecorder {
	req := withSession(httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode())),
		&model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)})
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if id != "" {
		req.SetPathValue("id", id)
	}
	rec := httptest.NewRecorder()
	h(rec, req)
	return rec
}

func TestAdminToolBlessings_GatedOnAdminRole(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})
	r := chi.NewRouter()
	u.RegisterRoutes(r)
	_, userCookie := newTestSession(t, u, "regular", string(model.RoleUser))
	_, adminCookie := newTestSession(t, u, "boss", string(model.RoleAdmin))

	req := httptest.NewRequest(http.MethodGet, "/admin/tool-blessings", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Errorf("unauthenticated: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/admin/tool-blessings", nil)
	req.AddCookie(userCookie)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusSeeOther {
		t.Errorf("non-admin: %d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/admin/tool-blessings", nil)
	req.AddCookie(adminCookie)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Tool Blessings") {
		t.Errorf("admin: %d %s", rec.Code, rec.Body.String())
	}
	// Every admin nav carries the Blessings tab.
	if !strings.Contains(rec.Body.String(), "/admin/tool-blessings") {
		t.Error("nav tab missing")
	}
	// Non-admin POST must not create anything either.
	req = httptest.NewRequest(http.MethodPost, "/admin/tool-blessings/inspect", strings.NewReader("workflow=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(userCookie)
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("non-admin inspect POST must not succeed: %d", rec.Code)
	}
}

func TestAdminToolBlessings_InspectBlessAddImageRevoke(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{})
	seedBlessWF(t, st)
	ctx := context.Background()

	// Inspect renders the script verbatim (HTML-escaped) and the flags.
	rec := postForm(u, u.HandleAdminToolBlessingInspect, "/admin/tool-blessings/inspect", url.Values{"workflow": {"bvbrc-helper"}}, "")
	body := rec.Body.String()
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect: %d %s", rec.Code, body)
	}
	for _, want := range []string{"sha256:", "python.sif", "&lt;b&gt;hello&lt;/b&gt;", "Review flags (1)", "declared-stdout", "Bless this tool"} {
		if !strings.Contains(body, want) {
			t.Errorf("inspect page missing %q", want)
		}
	}
	if strings.Contains(body, "<b>hello</b>") {
		t.Error("script content must be escaped")
	}
	// Awaiting list shows the pending tool.
	if !strings.Contains(body, "Awaiting a blessing (1)") {
		t.Errorf("pending count missing")
	}

	// Extract the hash from the inspect result for the form.
	pend, _ := st.ListToolBlessings(ctx, true)
	if len(pend) != 0 {
		t.Fatal("inspect must not create a blessing")
	}

	// Without the acknowledgement: refused.
	form := url.Values{"workflow": {"bvbrc-helper"}, "image_sha256s": {uiSHA}}
	rec = postForm(u, u.HandleAdminToolBlessingCreate, "/admin/tool-blessings", form, "")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Errorf("no ack: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	if l, _ := st.ListToolBlessings(ctx, true); len(l) != 0 {
		t.Fatal("blessing created without ack")
	}

	// Stale hash: refused.
	form.Set("ack", "1")
	form.Set("tool_hash", "sha256:"+strings.Repeat("0", 64))
	rec = postForm(u, u.HandleAdminToolBlessingCreate, "/admin/tool-blessings", form, "")
	if l, _ := st.ListToolBlessings(ctx, true); len(l) != 0 || !strings.Contains(rec.Header().Get("Location"), "mismatch") {
		t.Errorf("stale hash must be refused: %s", rec.Header().Get("Location"))
	}

	// Missing digest: refused.
	form.Del("tool_hash")
	form.Set("image_sha256s", "")
	rec = postForm(u, u.HandleAdminToolBlessingCreate, "/admin/tool-blessings", form, "")
	if l, _ := st.ListToolBlessings(ctx, true); len(l) != 0 {
		t.Fatalf("blessing without a pinned image: %s", rec.Header().Get("Location"))
	}

	// Valid.
	form.Set("image_sha256s", uiSHA)
	form.Set("worker_groups", "default, anlseq")
	form.Set("note", "ui test")
	rec = postForm(u, u.HandleAdminToolBlessingCreate, "/admin/tool-blessings", form, "")
	if rec.Code != http.StatusSeeOther || !strings.Contains(rec.Header().Get("Location"), "msg=") {
		t.Fatalf("bless: %d %s", rec.Code, rec.Header().Get("Location"))
	}
	list, _ := st.ListToolBlessings(ctx, false)
	if len(list) != 1 {
		t.Fatalf("want 1 blessing, got %d", len(list))
	}
	b := list[0]
	if b.BlessedBy != "boss" || b.ImageRef != "python.sif" || len(b.ImageSHA256s) != 1 || len(b.WorkerGroups) != 2 || b.Note != "ui test" {
		t.Errorf("blessing: %+v", b)
	}

	// Add image.
	second := strings.Repeat("cd", 32)
	rec = postForm(u, u.HandleAdminToolBlessingAddImage, "/x", url.Values{"image_sha256": {second}}, b.ID)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add image: %d", rec.Code)
	}
	got, _ := st.GetToolBlessing(ctx, b.ID)
	if !got.AllowsImage(second) {
		t.Errorf("digest not added: %v", got.ImageSHA256s)
	}
	rec = postForm(u, u.HandleAdminToolBlessingAddImage, "/x", url.Values{"image_sha256": {"bad"}}, b.ID)
	if !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Error("bad digest must be refused")
	}

	// Revoke needs a reason.
	rec = postForm(u, u.HandleAdminToolBlessingRevoke, "/x", url.Values{}, b.ID)
	if !strings.Contains(rec.Header().Get("Location"), "error=") {
		t.Error("revoke without reason must be refused")
	}
	if g, _ := st.GetToolBlessing(ctx, b.ID); !g.Active() {
		t.Fatal("revoked without reason")
	}
	rec = postForm(u, u.HandleAdminToolBlessingRevoke, "/x", url.Values{"reason": {"found echo"}}, b.ID)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("revoke: %d", rec.Code)
	}
	g, _ := st.GetToolBlessing(ctx, b.ID)
	if g.Active() || g.RevokedBy != "boss" || g.RevokeReason != "found echo" {
		t.Errorf("revoked row: %+v", g)
	}

	// The list page shows revoked rows on request and keeps the record.
	req := withSession(httptest.NewRequest(http.MethodGet, "/admin/tool-blessings?all=1", nil),
		&model.Session{ID: "s", Username: "boss", Role: string(model.RoleAdmin)})
	lrec := httptest.NewRecorder()
	u.HandleAdminToolBlessings(lrec, req)
	if !strings.Contains(lrec.Body.String(), "found echo") {
		t.Errorf("revoked row not listed with ?all=1")
	}
}
