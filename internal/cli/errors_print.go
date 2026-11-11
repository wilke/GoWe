package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/me/gowe/pkg/model"
)

// printAPIErrorDetails writes one line per FieldError carried by err's
// Details — a VALIDATION_ERROR response from input validation (#273) — in
// the form "  <field><path>: <message>", e.g.
// "  inputs.chunk_method: expected one of [...]". It is a no-op when err is
// not a *model.APIError or carries no Details.
func printAPIErrorDetails(w io.Writer, err error) {
	var apiErr *model.APIError
	if !errors.As(err, &apiErr) || len(apiErr.Details) == 0 {
		return
	}
	for _, d := range apiErr.Details {
		fmt.Fprintf(w, "  %s%s: %s\n", d.Field, d.Path, d.Message)
	}
}

// submissionWarnings unmarshals just the "warnings" field of a successful
// POST /api/v1/submissions response (#273 warn mode): a list of
// {field,path,message} entries describing input values that didn't match
// their declared CWL type but were accepted anyway.
type submissionWarnings struct {
	Warnings []model.FieldError `json:"warnings"`
}

// printSubmissionWarnings parses data — a submission create response body —
// for a "warnings" field and prints one line per entry (same
// "  <field><path>: <message>" form as printAPIErrorDetails), preceded by a
// "Warnings:" header. It is a no-op if data has no warnings or fails to
// parse (a parse failure here is unexpected since the caller already parsed
// the same body once for its own purposes; treated as "no warnings" rather
// than a hard error, since printing warnings must never fail the command).
func printSubmissionWarnings(w io.Writer, data json.RawMessage) {
	var sw submissionWarnings
	if err := json.Unmarshal(data, &sw); err != nil || len(sw.Warnings) == 0 {
		return
	}
	fmt.Fprintln(w, "Warnings:")
	for _, wn := range sw.Warnings {
		fmt.Fprintf(w, "  %s%s: %s\n", wn.Field, wn.Path, wn.Message)
	}
}

// printRegistrationWarnings prints the "warnings" field of a workflow
// create/update response — plain "<field>: <message>" strings such as an
// undeclared, unused step input or a tool that asks for the token but is not
// blessed — one per line after a "Warnings:" header. A no-op when there are
// none or data does not parse.
func printRegistrationWarnings(w io.Writer, data json.RawMessage) {
	var resp struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal(data, &resp); err != nil || len(resp.Warnings) == 0 {
		return
	}
	fmt.Fprintln(w, "Warnings:")
	for _, wn := range resp.Warnings {
		fmt.Fprintf(w, "  %s\n", wn)
	}
}
