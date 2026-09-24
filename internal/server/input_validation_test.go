package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/me/gowe/internal/metrics"
	"github.com/me/gowe/internal/validate"
)

// enumToolCWL is a minimal bare CommandLineTool (ParseGraph wraps it as a
// one-input workflow, per #273's SubmissionInputs contract) with a
// chunk_method enum input, used to exercise input value/type validation at
// submission time.
const enumToolCWL = `cwlVersion: v1.2
class: CommandLineTool
id: enum-tool
baseCommand: echo
inputs:
  chunk_method:
    type:
      type: enum
      symbols: [fixed, semantic]
    inputBinding:
      position: 1
outputs: []
`

// createEnumWorkflow registers enumToolCWL and returns its workflow id.
func createEnumWorkflow(t *testing.T, srv *Server) string {
	t.Helper()
	bodyJSON, _ := json.Marshal(map[string]string{
		"name": "enum-tool-workflow",
		"cwl":  enumToolCWL,
	})
	w, env := doPost(t, srv, "/api/v1/workflows/", string(bodyJSON))
	if w.Code != http.StatusCreated {
		t.Fatalf("create enum workflow: status=%d, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	id, ok := data["id"].(string)
	if !ok {
		t.Fatalf("created enum workflow missing id, data=%v", data)
	}
	return id
}

// inputValidationFailureCount sums gowe_input_validation_failures_total
// samples across every label combination, mirroring runSecondsSampleCount's
// pattern in handler_workers_test.go.
func inputValidationFailureCount(t *testing.T, reg *metrics.Registry) float64 {
	t.Helper()
	mfs, err := reg.Gatherer().Gather()
	if err != nil {
		t.Fatalf("gather metrics: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "gowe_input_validation_failures_total" {
			continue
		}
		var total float64
		for _, m := range mf.GetMetric() {
			total += m.GetCounter().GetValue()
		}
		return total
	}
	return 0
}

func submitEnum(t *testing.T, srv *Server, wfID, dryRun string, chunkMethod any) (*httptest.ResponseRecorder, envelope) {
	t.Helper()
	body := map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"chunk_method": chunkMethod},
	}
	bodyJSON, _ := json.Marshal(body)
	path := "/api/v1/submissions/"
	if dryRun != "" {
		path += "?dry_run=" + dryRun
	}
	return doPost(t, srv, path, string(bodyJSON))
}

// TestInputValidation_Enforce covers the enforce-mode path end to end: a bad
// enum value is rejected with 400, naming the input and its allowed symbols
// in both the top-level Message and Details (field "inputs.chunk_method");
// a valid value is accepted with 201.
func TestInputValidation_Enforce(t *testing.T) {
	srv := testServer(WithInputValidation(validate.ModeEnforce))
	wfID := createEnumWorkflow(t, srv)

	t.Run("bad enum value -> 400", func(t *testing.T) {
		w, env := submitEnum(t, srv, wfID, "", "not-a-symbol")
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d, want 400, body=%s", w.Code, w.Body.String())
		}
		if env.Error == nil {
			t.Fatal("expected an error envelope")
		}
		if !strings.Contains(env.Error.Message, "chunk_method") {
			t.Errorf("message = %q, want it to name the input chunk_method", env.Error.Message)
		}
		if !strings.Contains(env.Error.Message, "fixed") || !strings.Contains(env.Error.Message, "semantic") {
			t.Errorf("message = %q, want it to list the allowed symbols", env.Error.Message)
		}
		if len(env.Error.Details) == 0 {
			t.Fatal("expected Details to be populated")
		}
		found := false
		for _, d := range env.Error.Details {
			if d.Field == "inputs.chunk_method" {
				found = true
			}
		}
		if !found {
			t.Errorf("Details = %+v, want a FieldError with Field \"inputs.chunk_method\"", env.Error.Details)
		}
	})

	t.Run("valid enum value -> 201", func(t *testing.T) {
		w, env := submitEnum(t, srv, wfID, "", "fixed")
		if w.Code != http.StatusCreated {
			t.Fatalf("status=%d, want 201, body=%s", w.Code, w.Body.String())
		}
		var data map[string]any
		json.Unmarshal(env.Data, &data)
		if _, ok := data["warnings"]; ok {
			t.Errorf("unexpected warnings on a valid submission: %v", data["warnings"])
		}
	})
}

// TestInputValidation_Warn covers warn mode: the submission is still
// accepted (201), but the response carries a warnings array and the
// gowe_input_validation_failures_total metric is incremented.
func TestInputValidation_Warn(t *testing.T) {
	reg := metrics.NewRegistry(metrics.Config{})
	srv := testServer(WithInputValidation(validate.ModeWarn), WithMetrics(reg))
	wfID := createEnumWorkflow(t, srv)

	before := inputValidationFailureCount(t, reg)

	w, env := submitEnum(t, srv, wfID, "", "not-a-symbol")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201 (warn mode never blocks), body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	warnings, ok := data["warnings"].([]any)
	if !ok || len(warnings) == 0 {
		t.Fatalf("expected a non-empty warnings array, got %v", data["warnings"])
	}
	first, _ := warnings[0].(map[string]any)
	if first["field"] != "inputs.chunk_method" {
		t.Errorf("warnings[0].field = %v, want \"inputs.chunk_method\"", first["field"])
	}

	after := inputValidationFailureCount(t, reg)
	if after <= before {
		t.Errorf("gowe_input_validation_failures_total did not increase: before=%v after=%v", before, after)
	}
}

// TestInputValidation_Off skips validation entirely: a bad value is
// accepted with no warnings.
func TestInputValidation_Off(t *testing.T) {
	srv := testServer(WithInputValidation(validate.ModeOff))
	wfID := createEnumWorkflow(t, srv)

	w, env := submitEnum(t, srv, wfID, "", "not-a-symbol")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201, body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if _, ok := data["warnings"]; ok {
		t.Errorf("mode=off must never populate warnings, got %v", data["warnings"])
	}
}

// TestInputValidation_DryRun asserts the dry-run report surfaces field
// errors (inputs_valid=false, valid=false, an "inputs.chunk_method" error)
// even when the server runs in warn mode (dry-run is diagnostic and never
// softens what it reports).
func TestInputValidation_DryRun(t *testing.T) {
	for _, mode := range []validate.Mode{validate.ModeEnforce, validate.ModeWarn} {
		t.Run(string(mode), func(t *testing.T) {
			srv := testServer(WithInputValidation(mode))
			wfID := createEnumWorkflow(t, srv)

			w, env := submitEnum(t, srv, wfID, "true", "not-a-symbol")
			if w.Code != http.StatusOK {
				t.Fatalf("dry-run status=%d, want 200, body=%s", w.Code, w.Body.String())
			}
			var report map[string]any
			json.Unmarshal(env.Data, &report)
			if report["inputs_valid"] != false {
				t.Errorf("inputs_valid = %v, want false", report["inputs_valid"])
			}
			if report["valid"] != false {
				t.Errorf("valid = %v, want false", report["valid"])
			}
			errs, _ := report["errors"].([]any)
			found := false
			for _, e := range errs {
				em, _ := e.(map[string]any)
				if em["field"] == "inputs.chunk_method" {
					found = true
				}
			}
			if !found {
				t.Errorf("errors = %v, want an entry for inputs.chunk_method", errs)
			}
		})
	}
}

// TestInputValidation_AnonymousSubmission confirms an anonymous submission
// (no auth header — testServerWithStore enables anonymous access by
// default) is validated exactly like an authenticated one.
func TestInputValidation_AnonymousSubmission(t *testing.T) {
	srv := testServer(WithInputValidation(validate.ModeEnforce))
	wfID := createEnumWorkflow(t, srv)

	w, env := submitEnum(t, srv, wfID, "", "not-a-symbol")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("anonymous submission status=%d, want 400, body=%s", w.Code, w.Body.String())
	}
	if env.Error == nil || !strings.Contains(env.Error.Message, "chunk_method") {
		t.Errorf("error = %+v, want it to name chunk_method", env.Error)
	}
}

// TestInputValidation_RawCWLParseFailure: if the workflow's stored RawCWL
// cannot be re-parsed at submission time (e.g. corrupted between
// registration and submission), validation is skipped — never treated as a
// rejection — and the submission is accepted normally.
func TestInputValidation_RawCWLParseFailure(t *testing.T) {
	srv, st := testServerWithStore(WithInputValidation(validate.ModeEnforce))
	wfID := createEnumWorkflow(t, srv)

	wf, err := st.GetWorkflow(context.Background(), wfID)
	if err != nil || wf == nil {
		t.Fatalf("get workflow: %v", err)
	}
	wf.RawCWL = "{ not: valid: yaml: ["
	if err := st.UpdateWorkflow(context.Background(), wf); err != nil {
		t.Fatalf("update workflow with corrupted RawCWL: %v", err)
	}

	w, env := submitEnum(t, srv, wfID, "", "not-a-symbol")
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d, want 201 (parse failure must never block), body=%s", w.Code, w.Body.String())
	}
	var data map[string]any
	json.Unmarshal(env.Data, &data)
	if _, ok := data["warnings"]; ok {
		t.Errorf("unexpected warnings when RawCWL could not be parsed: %v", data["warnings"])
	}
}

// TestInputValidation_BodyTooLarge: POST /submissions bodies over the 16 MiB
// cap are refused rather than fully buffered.
func TestInputValidation_BodyTooLarge(t *testing.T) {
	srv := testServer()
	wfID := createEnumWorkflow(t, srv)

	huge := strings.Repeat("x", (16<<20)+1024)
	bodyJSON, _ := json.Marshal(map[string]any{
		"workflow_id": wfID,
		"inputs":      map[string]any{"chunk_method": "fixed", "padding": huge},
	})
	w, _ := doPost(t, srv, "/api/v1/submissions/", string(bodyJSON))
	if w.Code != http.StatusBadRequest && w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status=%d, want 400 or 413 for an oversized body", w.Code)
	}
}
