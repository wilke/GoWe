package model

import (
	"errors"
	"fmt"
	"io"
)

// ErrorCode represents a structured API error code.
type ErrorCode string

const (
	ErrValidation             ErrorCode = "VALIDATION_ERROR"
	ErrNotFound               ErrorCode = "NOT_FOUND"
	ErrConflict               ErrorCode = "CONFLICT"
	ErrUnauthorized           ErrorCode = "UNAUTHORIZED"
	ErrForbidden              ErrorCode = "FORBIDDEN"
	ErrInternal               ErrorCode = "INTERNAL_ERROR"
	ErrUnsupportedRequirement ErrorCode = "UNSUPPORTED_REQUIREMENT"
	// ErrUnavailable marks a dependency outage (e.g. the provider-token
	// key server is unreachable) that keeps authentication from being
	// evaluated at all, as distinct from a token that was evaluated and
	// rejected (ErrUnauthorized).
	ErrUnavailable ErrorCode = "UNAVAILABLE"
	// ErrPayloadTooLarge marks a request body that was refused for
	// exceeding a size cap (http.MaxBytesReader) before it could even be
	// decoded, as distinct from a body that decoded but failed validation
	// (ErrValidation).
	ErrPayloadTooLarge ErrorCode = "PAYLOAD_TOO_LARGE"
)

// APIError is a structured error returned by the GoWe API.
type APIError struct {
	Code    ErrorCode    `json:"code"`
	Message string       `json:"message"`
	Details []FieldError `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// WriteErrorDetails writes one line per FieldError carried by err, in the
// form "  <field><path>: <message>". It is a no-op when err (or anything it
// wraps) is not an *APIError or carries no Details. Error() deliberately
// leaves the details out (it is used in API responses and logs), so a CLI
// that prints only err loses them; both gowe and cwl-runner call this.
func WriteErrorDetails(w io.Writer, err error) {
	var apiErr *APIError
	if !errors.As(err, &apiErr) || len(apiErr.Details) == 0 {
		return
	}
	for _, d := range apiErr.Details {
		fmt.Fprintf(w, "  %s%s: %s\n", d.Field, d.Path, d.Message)
	}
}

// FieldError describes a validation error on a specific field.
type FieldError struct {
	Field   string `json:"field,omitempty"`
	Path    string `json:"path,omitempty"`
	Message string `json:"message"`
}

// NewValidationError creates an APIError with validation details.
func NewValidationError(msg string, details ...FieldError) *APIError {
	return &APIError{Code: ErrValidation, Message: msg, Details: details}
}

// NewNotFoundError creates a NOT_FOUND APIError.
func NewNotFoundError(resource, id string) *APIError {
	return &APIError{
		Code:    ErrNotFound,
		Message: fmt.Sprintf("%s '%s' not found", resource, id),
	}
}

// NewInternalError creates an INTERNAL_ERROR APIError.
func NewInternalError(msg string) *APIError {
	return &APIError{
		Code:    ErrInternal,
		Message: msg,
	}
}

// NewUnauthorizedError creates an UNAUTHORIZED APIError.
func NewUnauthorizedError(msg string) *APIError {
	return &APIError{
		Code:    ErrUnauthorized,
		Message: msg,
	}
}

// InvalidTransitionError is returned when a state transition is invalid.
type InvalidTransitionError struct {
	Entity string
	ID     string
	From   string
	To     string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("invalid %s state transition: %s → %s (entity %s)", e.Entity, e.From, e.To, e.ID)
}

// ErrUnblessedInject is returned by the store when asked to persist a
// non-terminal task whose RuntimeHints request token injection
// (InjectBVBRCToken) without a TokenBlessingID. Only the scheduler's
// attachCredentials may set the flag, always together with the blessing id;
// this is the belt-and-braces check behind it.
var ErrUnblessedInject = errors.New("task has InjectBVBRCToken without TokenBlessingID")
