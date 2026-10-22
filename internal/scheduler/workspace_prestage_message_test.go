package scheduler

import (
	"errors"
	"strings"
	"testing"
)

// TestPrestageFailMessage_ObjectNotFoundHint verifies that a PRESTAGE_FAILED
// message built from a Workspace "object not found" error (observed
// verbatim from BV-BRC as "_ERROR_Object not found!_ERROR_") is rewritten
// into an actionable, GoWe-generic hint instead of surfacing the raw,
// cryptic service error inline — while every other stager error keeps its
// original inline message unchanged.
func TestPrestageFailMessage_ObjectNotFoundHint(t *testing.T) {
	loc := WSLocation{Location: "ws:///user@bvbrc/home/reads.fastq", Path: "/user@bvbrc/home/reads.fastq"}

	t.Run("object not found gets the actionable hint", func(t *testing.T) {
		err := errors.New("workspace stager: determine object type for /user@bvbrc/home/reads.fastq: _ERROR_Object not found!_ERROR_")
		msg := prestageFailMessage(loc, err)
		if !strings.Contains(msg, loc.Path) {
			t.Errorf("message = %q, want it to name the path %q", msg, loc.Path)
		}
		if !strings.Contains(msg, "does not exist") {
			t.Errorf("message = %q, want the actionable not-found hint", msg)
		}
		if strings.Contains(msg, "_ERROR_") {
			t.Errorf("message = %q, want the raw service error text NOT inlined into the message", msg)
		}
	})

	t.Run("matches case-insensitively", func(t *testing.T) {
		err := errors.New("OBJECT NOT FOUND: /user@bvbrc/home/reads.fastq")
		msg := prestageFailMessage(loc, err)
		if !strings.Contains(msg, "does not exist") {
			t.Errorf("message = %q, want the actionable not-found hint regardless of case", msg)
		}
	})

	t.Run("other errors keep the original inline message", func(t *testing.T) {
		err := errors.New("connection refused")
		msg := prestageFailMessage(loc, err)
		if !strings.Contains(msg, "connection refused") {
			t.Errorf("message = %q, want the raw stager error inlined for a non-not-found failure", msg)
		}
		if strings.Contains(msg, "does not exist") {
			t.Errorf("message = %q, want no not-found hint for an unrelated error", msg)
		}
	})
}
