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

	"github.com/me/gowe/internal/store"
	"github.com/me/gowe/internal/validate"
	"github.com/me/gowe/pkg/model"
)

// submissionInputsTestCWL declares one input of each kind exercised by
// HandleSubmissionCreatePost's value-building and validation (#273): File,
// Directory, an int array, a long, and a nullable enum (used to trigger a
// type mismatch for the enforce/warn/off tests).
const submissionInputsTestCWL = `cwlVersion: v1.2
class: Workflow
inputs:
  reads: File
  outdir: Directory
  items: int[]
  counts: int[]
  long_val: long
  chunk_method:
    type:
      - "null"
      - type: enum
        symbols: [fixed, sentence, semantic]
outputs: []
steps: []
`

func createSubmissionInputsTestWorkflow(t *testing.T, st *store.SQLiteStore) *model.Workflow {
	t.Helper()
	now := time.Now().UTC()
	wf := &model.Workflow{
		ID:         "wf_subinputs",
		Name:       "submission-inputs-test",
		Class:      "Workflow",
		CWLVersion: "v1.2",
		RawCWL:     submissionInputsTestCWL,
		CreatedAt:  now,
		UpdatedAt:  now,
		Inputs: []model.WorkflowInput{
			{ID: "reads", Type: "File", Required: true},
			{ID: "outdir", Type: "Directory", Required: true},
			{ID: "items", Type: "int[]", Required: true},
			{ID: "counts", Type: "int[]", Required: true},
			{ID: "long_val", Type: "long", Required: true},
			{ID: "chunk_method", Type: "string?", Required: false},
		},
	}
	if err := st.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	return wf
}

// baseSubmissionForm returns a fully valid form.Values for
// submissionInputsTestCWL: a workspace-form File path, a workspace-form
// Directory path, a JSON array, a line-form array, a long that overflows
// int32, and a valid enum value.
func baseSubmissionForm(workflowID string) url.Values {
	form := url.Values{}
	form.Set("workflow_id", workflowID)
	form.Set("inputs[reads]", "/awilke@bvbrc/home/data/contigs.fasta")
	form.Set("inputs[outdir]", "/awilke@bvbrc/home/out")
	form.Set("inputs[items]", "[1,2,3]")
	form.Set("inputs[counts]", "1\n2\n3")
	form.Set("inputs[long_val]", "9999999999")
	form.Set("inputs[chunk_method]", "fixed")
	return form
}

func postSubmissionCreate(t *testing.T, u *UI, sess *model.Session, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/submissions/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req = withSession(req, sess)
	rec := httptest.NewRecorder()
	u.HandleSubmissionCreatePost(rec, req)
	return rec
}

// locationSubmissionID extracts the "sub_..." id from a
// "/submissions/sub_xxx" or "/submissions/sub_xxx?warning=..." redirect
// Location header.
func locationSubmissionID(t *testing.T, location string) string {
	t.Helper()
	loc, err := url.Parse(location)
	if err != nil {
		t.Fatalf("parse redirect Location %q: %v", location, err)
	}
	id := strings.TrimPrefix(loc.Path, "/submissions/")
	if id == "" || id == loc.Path {
		t.Fatalf("redirect Location %q does not look like a submission detail page", location)
	}
	return id
}

// TestHandleSubmissionCreatePost_BuildsTypedValues is the #273 UI e2e case:
// a fully valid form must land in the store as proper CWL values, not the
// raw strings/bare paths the form fields carried — this is the "build
// proper CWL values BEFORE validation and storage" precondition for
// enforce mode to be usable at all.
func TestHandleSubmissionCreatePost_BuildsTypedValues(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{InputValidation: validate.ModeOff})
	wf := createSubmissionInputsTestWorkflow(t, st)
	sess := &model.Session{ID: "s1", Username: "tester", Role: "user"}

	rec := postSubmissionCreate(t, u, sess, baseSubmissionForm(wf.ID))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	id := locationSubmissionID(t, rec.Header().Get("Location"))

	sub, err := st.GetSubmission(context.Background(), id)
	if err != nil || sub == nil {
		t.Fatalf("get submission %s: %v", id, err)
	}

	reads, ok := sub.Inputs["reads"].(map[string]any)
	if !ok || reads["class"] != "File" || reads["location"] != "ws:///awilke@bvbrc/home/data/contigs.fasta" {
		t.Errorf("reads = %#v, want File object with ws:// location", sub.Inputs["reads"])
	}
	outdir, ok := sub.Inputs["outdir"].(map[string]any)
	if !ok || outdir["class"] != "Directory" || outdir["location"] != "ws:///awilke@bvbrc/home/out" {
		t.Errorf("outdir = %#v, want Directory object with ws:// location", sub.Inputs["outdir"])
	}

	items, ok := sub.Inputs["items"].([]any)
	if !ok || len(items) != 3 {
		t.Fatalf("items = %#v, want a 3-element array (parsed from JSON)", sub.Inputs["items"])
	}
	for i, want := range []float64{1, 2, 3} {
		if got, ok := items[i].(float64); !ok || got != want {
			t.Errorf("items[%d] = %#v, want %v", i, items[i], want)
		}
	}

	// Round-tripped through the store's JSON persistence, numeric values
	// come back as float64 regardless of their in-memory Go type before
	// storage (int vs int64) — the assertion here is that each line became
	// a number, not that it stayed a string.
	counts, ok := sub.Inputs["counts"].([]any)
	if !ok || len(counts) != 3 {
		t.Fatalf("counts = %#v, want a 3-element array (parsed from lines)", sub.Inputs["counts"])
	}
	for i, want := range []float64{1, 2, 3} {
		if got, ok := counts[i].(float64); !ok || got != want {
			t.Errorf("counts[%d] = %#v (%T), want number %v", i, counts[i], counts[i], want)
		}
	}

	longVal, ok := sub.Inputs["long_val"].(float64)
	if !ok || longVal != 9999999999 {
		t.Errorf("long_val = %#v (%T), want number 9999999999", sub.Inputs["long_val"], sub.Inputs["long_val"])
	}

	if sub.Inputs["chunk_method"] != "fixed" {
		t.Errorf("chunk_method = %#v, want \"fixed\"", sub.Inputs["chunk_method"])
	}

	// SubmittedInputs is an immutable snapshot captured by
	// store.CreateSubmission from Inputs at creation time (like the API —
	// handler_submissions.go never sets it explicitly either).
	if sub.SubmittedInputs == nil {
		t.Fatal("SubmittedInputs is nil, want a snapshot of the submitted inputs")
	}
	if sub.SubmittedInputs["long_val"] != sub.Inputs["long_val"] {
		t.Errorf("SubmittedInputs[long_val] = %#v, want it to match Inputs[long_val] = %#v",
			sub.SubmittedInputs["long_val"], sub.Inputs["long_val"])
	}
}

// TestHandleSubmissionCreatePost_Enforce_RejectsBadValue: an enum value that
// doesn't match any declared symbol must be rejected in enforce mode — no
// submission created, redirected back to the form with an error naming the
// input.
func TestHandleSubmissionCreatePost_Enforce_RejectsBadValue(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{InputValidation: validate.ModeEnforce})
	wf := createSubmissionInputsTestWorkflow(t, st)
	sess := &model.Session{ID: "s1", Username: "tester", Role: "user"}

	_, before, err := st.ListSubmissions(context.Background(), model.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("list submissions before: %v", err)
	}

	form := baseSubmissionForm(wf.ID)
	form.Set("inputs[chunk_method]", "bogus_symbol")
	rec := postSubmissionCreate(t, u, sess, form)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "/submissions/new") {
		t.Fatalf("Location = %q, want a redirect back to the creation form", loc)
	}
	parsed, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("parse Location %q: %v", loc, err)
	}
	errMsg := parsed.Query().Get("error")
	if !strings.Contains(errMsg, "chunk_method") {
		t.Errorf("error message = %q, want it to name input chunk_method", errMsg)
	}

	_, after, err := st.ListSubmissions(context.Background(), model.ListOptions{Limit: 100})
	if err != nil {
		t.Fatalf("list submissions after: %v", err)
	}
	if after != before {
		t.Errorf("submission count = %d, want unchanged %d (enforce must not create)", after, before)
	}
}

// TestHandleSubmissionCreatePost_Warn_CreatesWithNotice: the same bad enum
// value in warn mode must not block creation, and the resulting page must
// show a non-blocking notice naming the input.
func TestHandleSubmissionCreatePost_Warn_CreatesWithNotice(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{InputValidation: validate.ModeWarn})
	wf := createSubmissionInputsTestWorkflow(t, st)
	sess := &model.Session{ID: "s1", Username: "tester", Role: "user"}

	form := baseSubmissionForm(wf.ID)
	form.Set("inputs[chunk_method]", "bogus_symbol")
	rec := postSubmissionCreate(t, u, sess, form)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "warning=") {
		t.Fatalf("Location = %q, want a ?warning= query param", loc)
	}
	id := locationSubmissionID(t, loc)

	sub, err := st.GetSubmission(context.Background(), id)
	if err != nil || sub == nil {
		t.Fatalf("get submission %s: %v", id, err)
	}
	if sub.Inputs["chunk_method"] != "bogus_symbol" {
		t.Errorf("chunk_method = %#v, want the value stored as submitted (warn never blocks)", sub.Inputs["chunk_method"])
	}

	// Render the detail page the redirect points to and confirm the warning
	// notice is visible.
	detailReq := httptest.NewRequest(http.MethodGet, loc, nil)
	detailReq = withSession(detailReq, sess)
	detailReq.SetPathValue("id", id)
	detailRec := httptest.NewRecorder()
	u.HandleSubmissionDetail(detailRec, detailReq)
	if detailRec.Code != http.StatusOK {
		t.Fatalf("submission detail status = %d, body: %s", detailRec.Code, detailRec.Body.String())
	}
	body := detailRec.Body.String()
	if !strings.Contains(body, "Input validation warning") {
		t.Errorf("submission detail page missing the warning notice:\n%s", body)
	}
	if !strings.Contains(body, "chunk_method") {
		t.Errorf("submission detail page warning does not name chunk_method:\n%s", body)
	}
}

// TestHandleSubmissionCreatePost_Off_SkipsValidation: with input validation
// off, a bad enum value is stored verbatim and no warning is attached to
// the redirect.
func TestHandleSubmissionCreatePost_Off_SkipsValidation(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{InputValidation: validate.ModeOff})
	wf := createSubmissionInputsTestWorkflow(t, st)
	sess := &model.Session{ID: "s1", Username: "tester", Role: "user"}

	form := baseSubmissionForm(wf.ID)
	form.Set("inputs[chunk_method]", "bogus_symbol")
	rec := postSubmissionCreate(t, u, sess, form)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "warning=") {
		t.Errorf("Location = %q, want no ?warning= param when validation is off", loc)
	}
	id := locationSubmissionID(t, loc)
	sub, err := st.GetSubmission(context.Background(), id)
	if err != nil || sub == nil {
		t.Fatalf("get submission %s: %v", id, err)
	}
	if sub.Inputs["chunk_method"] != "bogus_symbol" {
		t.Errorf("chunk_method = %#v, want the raw submitted value", sub.Inputs["chunk_method"])
	}
}

// TestHandleSubmissionCreatePost_ParseErrorNeverBlocks: even in enforce
// mode, a workflow whose stored RawCWL fails to parse must never block
// submission creation — a validation parse failure means "could not
// validate", not "rejected".
func TestHandleSubmissionCreatePost_ParseErrorNeverBlocks(t *testing.T) {
	st := setupTestStore(t)
	defer st.Close()
	u := New(st, slog.Default(), Config{InputValidation: validate.ModeEnforce})
	now := time.Now().UTC()
	wf := &model.Workflow{
		ID:         "wf_unparseable",
		Name:       "unparseable",
		Class:      "Workflow",
		CWLVersion: "v1.2",
		RawCWL:     "::: not valid cwl :::",
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := st.CreateWorkflow(context.Background(), wf); err != nil {
		t.Fatalf("create workflow: %v", err)
	}
	sess := &model.Session{ID: "s1", Username: "tester", Role: "user"}

	form := url.Values{}
	form.Set("workflow_id", wf.ID)
	rec := postSubmissionCreate(t, u, sess, form)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d, body: %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	loc := rec.Header().Get("Location")
	if strings.Contains(loc, "/submissions/new") {
		t.Fatalf("Location = %q, a RawCWL parse failure must not block creation", loc)
	}
}
