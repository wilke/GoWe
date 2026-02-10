package model

import "time"

// Response is the standard API response envelope.
type Response struct {
	Status     string      `json:"status"`
	RequestID  string      `json:"request_id"`
	Timestamp  time.Time   `json:"timestamp"`
	Data       any         `json:"data"`
	Pagination *Pagination `json:"pagination,omitempty"`
	Error      *APIError   `json:"error"`
}

// Pagination holds pagination metadata for list endpoints.
type Pagination struct {
	Total   int  `json:"total"`
	Limit   int  `json:"limit"`
	Offset  int  `json:"offset"`
	HasMore bool `json:"has_more"`
}

// ListOptions configures list queries with pagination and filtering.
type ListOptions struct {
	Limit  int
	Offset int
	State  string // Optional state filter
}

// DefaultListOptions returns sensible defaults.
func DefaultListOptions() ListOptions {
	return ListOptions{Limit: 20, Offset: 0}
}

// Clamp enforces limits (max 100, min 1).
func (o *ListOptions) Clamp() {
	if o.Limit <= 0 {
		o.Limit = 20
	}
	if o.Limit > 100 {
		o.Limit = 100
	}
	if o.Offset < 0 {
		o.Offset = 0
	}
}
