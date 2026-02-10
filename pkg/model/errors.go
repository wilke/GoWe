package model

import "fmt"

// ErrorCode represents a structured API error code.
type ErrorCode string

const (
	ErrValidation   ErrorCode = "VALIDATION_ERROR"
	ErrNotFound     ErrorCode = "NOT_FOUND"
	ErrConflict     ErrorCode = "CONFLICT"
	ErrUnauthorized ErrorCode = "UNAUTHORIZED"
	ErrInternal     ErrorCode = "INTERNAL_ERROR"
)

// APIError is a structured error returned by the GoWe API.
type APIError struct {
	Code    ErrorCode     `json:"code"`
	Message string        `json:"message"`
	Details []FieldError  `json:"details,omitempty"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
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

// InvalidTransitionError is returned when a state transition is invalid.
type InvalidTransitionError struct {
	Entity  string
	ID      string
	From    string
	To      string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("invalid %s state transition: %s → %s (entity %s)", e.Entity, e.From, e.To, e.ID)
}
