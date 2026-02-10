package bvbrc

import (
	"errors"
	"fmt"
)

// Standard JSON-RPC error codes.
const (
	ErrCodeParseError     = -32700 // Parse error - invalid JSON
	ErrCodeInvalidRequest = -32600 // Invalid Request - not a valid JSON-RPC request
	ErrCodeMethodNotFound = -32601 // Method not found
	ErrCodeInvalidParams  = -32602 // Invalid params
	ErrCodeInternalError  = -32603 // Internal error

	// Server error range: -32000 to -32099
	ErrCodeServerError = -32000
)

// BV-BRC specific error codes (in server error range).
const (
	ErrCodeAuthFailed    = -32400 // Authentication failed
	ErrCodeNotAuthorized = -32401 // Not authorized
	ErrCodeNotFound      = -32404 // Resource not found
)

// Error types for common failure scenarios.
var (
	// ErrNotAuthenticated indicates no authentication token is configured.
	ErrNotAuthenticated = errors.New("not authenticated: no token configured")

	// ErrTokenExpired indicates the authentication token has expired.
	ErrTokenExpired = errors.New("authentication token has expired")

	// ErrNoTokenFile indicates no token file was found.
	ErrNoTokenFile = errors.New("no token file found")

	// ErrInvalidTokenFormat indicates the token string could not be parsed.
	ErrInvalidTokenFormat = errors.New("invalid token format")
)

// HTTPError represents an HTTP-level error (non-200 response).
type HTTPError struct {
	StatusCode int
	Body       string
}

// Error implements the error interface.
func (e *HTTPError) Error() string {
	if e.Body != "" {
		return fmt.Sprintf("HTTP %d: %s", e.StatusCode, e.Body)
	}
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

// IsRetryableHTTP returns true if the HTTP error is retryable.
func (e *HTTPError) IsRetryable() bool {
	// 5xx errors are generally retryable (server issues)
	// 429 Too Many Requests is retryable after delay
	return e.StatusCode >= 500 || e.StatusCode == 429
}

// Error wraps a BV-BRC API error with additional context.
type Error struct {
	// Op is the operation that failed.
	Op string

	// Code is the error code (if from RPC).
	Code int

	// Message is the error message.
	Message string

	// Err is the underlying error.
	Err error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Op, e.Message, e.Err)
	}
	if e.Code != 0 {
		return fmt.Sprintf("%s: [%d] %s", e.Op, e.Code, e.Message)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Message)
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error {
	return e.Err
}

// NewError creates a new Error with the given operation and message.
func NewError(op, message string) *Error {
	return &Error{Op: op, Message: message}
}

// WrapError wraps an error with operation context.
func WrapError(op string, err error) *Error {
	return &Error{Op: op, Err: err, Message: err.Error()}
}

// FromRPCError converts an RPCError to an Error.
func FromRPCError(op string, rpcErr *RPCError) *Error {
	return &Error{
		Op:      op,
		Code:    rpcErr.Code,
		Message: rpcErr.Message,
	}
}

// IsAuthError returns true if the error is an authentication/authorization error.
func IsAuthError(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == ErrCodeAuthFailed || e.Code == ErrCodeNotAuthorized
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == ErrCodeAuthFailed || rpcErr.Code == ErrCodeNotAuthorized
	}
	return errors.Is(err, ErrNotAuthenticated) || errors.Is(err, ErrTokenExpired)
}

// IsNotFoundError returns true if the error indicates a resource was not found.
func IsNotFoundError(err error) bool {
	var e *Error
	if errors.As(err, &e) {
		return e.Code == ErrCodeNotFound || e.Code == ErrCodeMethodNotFound
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		return rpcErr.Code == ErrCodeNotFound || rpcErr.Code == ErrCodeMethodNotFound
	}
	return false
}

// IsRetryable returns true if the error is likely transient and the request
// should be retried.
func IsRetryable(err error) bool {
	// Check for HTTP errors first
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.IsRetryable()
	}

	var e *Error
	if errors.As(err, &e) {
		// Server errors in the -32000 to -32099 range may be retryable
		// (except auth errors)
		if e.Code >= -32099 && e.Code <= -32000 {
			return e.Code != ErrCodeAuthFailed && e.Code != ErrCodeNotAuthorized
		}
		return e.Code == ErrCodeInternalError
	}
	var rpcErr *RPCError
	if errors.As(err, &rpcErr) {
		if rpcErr.Code >= -32099 && rpcErr.Code <= -32000 {
			return rpcErr.Code != ErrCodeAuthFailed && rpcErr.Code != ErrCodeNotAuthorized
		}
		return rpcErr.Code == ErrCodeInternalError
	}
	return false
}
