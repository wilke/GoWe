package ui

import (
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/model"
)

const uiTokenToolCWL = `cwlVersion: v1.2
$graph:
  - id: tok
    class: CommandLineTool
    baseCommand: [echo]
    requirements:
      DockerRequirement:
        dockerPull: python.sif
    hints:
      gowe:Execution:
        executor: worker
        inject_bvbrc_token: true
    inputs: {}
    outputs:
      out:
        type: string
  - id: main
    class: Workflow
    inputs: {}
    outputs: {}
    steps:
      s:
        run: "#tok"
        in: {}
        out: [out]
`

// The UI submission path runs the same provider-token fast-fail as the API:
// an unblessed token-requesting tool is refused with a visible error, and
// dev mode lets it through.
func TestHandleSubmissionCreatePost_UnblessedTokenToolRefused(t *testing.T) {
	for _, dev := range []bool{false, true} {
		st := setupTestStore(t)
		u := New(st, slog.Default(), Config{InputValidation: validate.ModeOff, TokenBlessingDev: dev})
		now := time.Now().UTC()
		wf := &model.Workflow{ID: "wf_tok", Name: "tok-wf", Class: "Workflow", CWLVersion: "v1.2",
			RawCWL: uiTokenToolCWL, CreatedAt: now, UpdatedAt: now}
		if err := st.CreateWorkflow(t.Context(), wf); err != nil {
			t.Fatal(err)
		}
		sess := &model.Session{ID: "s1", Username: "tester", Role: "user", Token: "un=tester|tokenid=t|expiry=4102444800|sig=s"}
		form := url.Values{}
		form.Set("workflow_id", wf.ID)
		rec := postSubmissionCreate(t, u, sess, form)
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("dev=%v status %d", dev, rec.Code)
		}
		loc := rec.Header().Get("Location")
		if dev {
			if !strings.HasPrefix(loc, "/submissions/sub_") {
				t.Errorf("dev mode must accept: %s", loc)
			}
		} else if !strings.Contains(loc, "/submissions/new") || !strings.Contains(loc, "TOOL_NOT_BLESSED") {
			t.Errorf("enforce mode must refuse with TOOL_NOT_BLESSED: %s", loc)
		}
		st.Close()
	}
}
