package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
)

// adminOutputsHTTPTimeout bounds one verify/redeliver call. Redelivery can
// stream large files through the server's staging path, so this is generous
// on purpose; it only guards against a truly stuck request.
const adminOutputsHTTPTimeout = 10 * time.Minute

// adminOutputFileReport mirrors the per-file JSON shape written by
// internal/server/handler_admin_outputs.go's outputFileReport. That type is
// unexported (this package cannot import internal/server without an import
// cycle — server already imports ui), so the UI decodes the same wire
// format independently rather than re-running the verification logic.
type adminOutputFileReport struct {
	SubmissionID     string `json:"submission_id"`
	Location         string `json:"location"`
	ExpectedChecksum string `json:"expected_checksum,omitempty"`
	ActualChecksum   string `json:"actual_checksum,omitempty"`
	ExpectedSize     int64  `json:"expected_size"`
	ActualSize       int64  `json:"actual_size"`
	OK               bool   `json:"ok"`
	Error            string `json:"error,omitempty"`
	Action           string `json:"action,omitempty"`
	Source           string `json:"source,omitempty"`
}

// adminOutputSummary mirrors outputReportSummary.
type adminOutputSummary struct {
	Total           int `json:"total"`
	OK              int `json:"ok"`
	Mismatched      int `json:"mismatched"`
	Errors          int `json:"errors"`
	Reuploaded      int `json:"reuploaded,omitempty"`
	WouldReupload   int `json:"would_reupload,omitempty"`
	OriginalMissing int `json:"original_missing,omitempty"`
	Failed          int `json:"failed,omitempty"`
}

// adminOutputReport mirrors outputReport, the response body of both
// /api/v1/admin/submissions/{id}/verify-outputs and .../redeliver.
type adminOutputReport struct {
	SubmissionID     string                  `json:"submission_id"`
	State            string                  `json:"state"`
	OutputState      string                  `json:"output_state"`
	DryRun           bool                    `json:"dry_run,omitempty"`
	Submissions      []string                `json:"submissions"`
	Files            []adminOutputFileReport `json:"files"`
	Summary          adminOutputSummary      `json:"summary"`
	Updated          bool                    `json:"updated,omitempty"`
	StateRestored    bool                    `json:"state_restored,omitempty"`
	ManifestUploaded bool                    `json:"manifest_uploaded,omitempty"`
	ManifestError    string                  `json:"manifest_error,omitempty"`
}

// adminAPIEnvelope mirrors the {status, request_id, data, error} response
// envelope every /api/v1 endpoint uses (see pkg/model.Response), read here
// with Data left as raw JSON so it can be unmarshalled into the specific
// report shape once the envelope itself is known to carry no error.
type adminAPIEnvelope struct {
	Status    string          `json:"status"`
	RequestID string          `json:"request_id"`
	Data      json.RawMessage `json:"data"`
	Error     *model.APIError `json:"error"`
}

// errNoLocalAddr is returned when the incoming request carries no local
// address in its context, so the admin API's own base URL cannot be derived.
// This only happens for requests that never passed through a real
// net.Listener (e.g. a handler invoked directly in a test without an
// ui.apiBaseURL override).
var errNoLocalAddr = errors.New("cannot determine this server's own address to call its admin API")

// adminAPIBaseURL returns the base URL this server's own /api/v1 admin
// endpoints are reachable at. It prefers ui.apiBaseURL (set only in tests,
// pointed at an httptest.Server) and otherwise derives it from the
// connection the request itself arrived on via http.LocalAddrContextKey —
// the exact socket the server is listening on, never client-supplied input
// such as the Host header, so a malicious Host cannot redirect the admin's
// BV-BRC token to an attacker-controlled endpoint. Note: when the server
// terminates TLS itself, this yields "https://<ip>:<port>", and the default
// http.Client will fail certificate verification because the cert is issued
// for a hostname, not the raw local IP; the call then fails closed with a
// visible "Call failed: x509: …" on the page rather than silently. This is
// fine for the documented deployments (plain HTTP, or TLS terminated by a
// reverse proxy in front of GoWe).
func (ui *UI) adminAPIBaseURL(r *http.Request) (string, error) {
	if ui.apiBaseURL != "" {
		return ui.apiBaseURL, nil
	}
	addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	if !ok || addr == nil {
		return "", errNoLocalAddr
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + addr.String(), nil
}

// callAdminOutputsAPI invokes one of the existing admin output-recovery
// operations (POST /api/v1/admin/submissions/{id}/verify-outputs or
// .../redeliver) as a loopback HTTP call against this same server, carrying
// the admin's own session token — the endpoint re-checks admin role itself,
// so this adds no new authorization surface. It returns the decoded report,
// the API error (if the envelope carried one), and the HTTP status.
func (ui *UI) callAdminOutputsAPI(r *http.Request, path, token string) (*adminOutputReport, *model.APIError, int, error) {
	base, err := ui.adminAPIBaseURL(r)
	if err != nil {
		return nil, nil, 0, err
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, base+path, nil)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("build request: %w", err)
	}
	if token != "" {
		req.Header.Set("Authorization", token)
	}

	client := &http.Client{Timeout: adminOutputsHTTPTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, 0, fmt.Errorf("call admin API: %w", err)
	}
	defer resp.Body.Close()

	var env adminAPIEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return nil, nil, resp.StatusCode, fmt.Errorf("decode response: %w", err)
	}
	if env.Error != nil {
		return nil, env.Error, resp.StatusCode, nil
	}

	var report adminOutputReport
	if len(env.Data) > 0 {
		if err := json.Unmarshal(env.Data, &report); err != nil {
			return nil, nil, resp.StatusCode, fmt.Errorf("decode report: %w", err)
		}
	}
	return &report, nil, resp.StatusCode, nil
}

// HandleAdminOutputs renders the output-verification/redelivery page: a
// per-submission trigger form plus (once run) the result of the last
// verify or redeliver call.
func (ui *UI) HandleAdminOutputs(w http.ResponseWriter, r *http.Request) {
	sess := SessionFromContext(r.Context())

	data := map[string]any{
		"Title":        "Output Verification - GoWe",
		"Session":      sess,
		"SubmissionID": strings.TrimSpace(r.URL.Query().Get("submission_id")),
	}
	ui.render(w, "admin/outputs", data)
}

// HandleAdminOutputsVerify triggers a read-only verify-outputs run for one
// submission and renders the result inline.
func (ui *UI) HandleAdminOutputsVerify(w http.ResponseWriter, r *http.Request) {
	ui.runAdminOutputsOp(w, r, false)
}

// HandleAdminOutputsRedeliver triggers a redeliver run (optionally dry-run)
// for one submission and renders the result inline. Redelivery mutates
// submission state and re-uploads files, so the template gates this button
// behind an explicit confirmation.
func (ui *UI) HandleAdminOutputsRedeliver(w http.ResponseWriter, r *http.Request) {
	ui.runAdminOutputsOp(w, r, true)
}

func (ui *UI) runAdminOutputsOp(w http.ResponseWriter, r *http.Request, redeliver bool) {
	sess := SessionFromContext(r.Context())

	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(r.FormValue("submission_id"))
	if id == "" {
		data := map[string]any{
			"Title":        "Output Verification - GoWe",
			"Session":      sess,
			"FormError":    "submission ID is required",
			"Redeliver":    redeliver,
			"SubmissionID": "",
		}
		ui.render(w, "admin/outputs", data)
		return
	}

	path := "/api/v1/admin/submissions/" + url.PathEscape(id) + "/verify-outputs"
	dryRun := false
	if redeliver {
		dryRun = r.FormValue("dry_run") != ""
		path = "/api/v1/admin/submissions/" + url.PathEscape(id) + "/redeliver"
		if dryRun {
			path += "?dry_run=true"
		}
	}

	var token string
	if sess != nil {
		token = sess.Token
	}

	report, apiErr, status, err := ui.callAdminOutputsAPI(r, path, token)

	data := map[string]any{
		"Title":        "Output Verification - GoWe",
		"Session":      sess,
		"SubmissionID": id,
		"Redeliver":    redeliver,
		"DryRun":       dryRun,
		"HTTPStatus":   status,
	}
	if err != nil {
		ui.logger.Error("admin outputs call failed", "submission_id", id, "redeliver", redeliver, "error", err)
		data["CallError"] = err.Error()
	}
	if apiErr != nil {
		data["APIError"] = apiErr.Message
	}
	if report != nil {
		data["Report"] = report
	}

	ui.render(w, "admin/outputs", data)
}
