package model

import "testing"

func TestAPIError_Error(t *testing.T) {
	err := &APIError{Code: ErrNotFound, Message: "Workflow 'wf_123' not found"}
	want := "NOT_FOUND: Workflow 'wf_123' not found"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestNewNotFoundError(t *testing.T) {
	err := NewNotFoundError("Submission", "sub_abc")
	if err.Code != ErrNotFound {
		t.Errorf("Code = %q, want %q", err.Code, ErrNotFound)
	}
	if err.Message != "Submission 'sub_abc' not found" {
		t.Errorf("Message = %q, want %q", err.Message, "Submission 'sub_abc' not found")
	}
}

func TestNewValidationError(t *testing.T) {
	err := NewValidationError("Invalid request",
		FieldError{Field: "inputs.reads_r1", Message: "required"},
		FieldError{Field: "inputs.taxonomy_id", Message: "expected int"},
	)
	if err.Code != ErrValidation {
		t.Errorf("Code = %q, want %q", err.Code, ErrValidation)
	}
	if len(err.Details) != 2 {
		t.Errorf("Details length = %d, want 2", len(err.Details))
	}
}

func TestInvalidTransitionError(t *testing.T) {
	err := &InvalidTransitionError{
		Entity: "Task",
		ID:     "task_123",
		From:   "SUCCESS",
		To:     "PENDING",
	}
	want := "invalid Task state transition: SUCCESS → PENDING (entity task_123)"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}
