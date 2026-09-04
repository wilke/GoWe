package model

import (
	"strings"
	"time"
)

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

// MaxListLimit is the maximum number of items a list endpoint will return per page.
const MaxListLimit = 100

// ListOptions configures list queries with pagination and filtering.
type ListOptions struct {
	Limit        int
	Offset       int
	State        string   // Optional state filter
	WorkflowID   string   // Optional workflow ID filter (exact match, single workflow version)
	WorkflowName string   // Optional workflow name filter (matches all versions/IDs sharing this name)
	DateStart    string   // Optional start date filter (YYYY-MM-DD)
	DateEnd      string   // Optional end date filter (YYYY-MM-DD)
	Search       string   // Optional search term (name, ID)
	Class        string   // Optional class filter: Workflow, CommandLineTool, ExpressionTool, or Tool (matches both CommandLineTool and ExpressionTool)
	Labels       []string // Optional label filters: "key:value" (exact) or "value" (any key match)
	SubmittedBy  string   // Filter submissions by owner (empty = no filter)
	SortBy       string   // Optional column to sort by (validated per-query)
	SortDir      string   // Sort direction: "asc" or "desc" (default: "desc")
	// ExcludeChildren omits child submissions (those spawned by a parent's
	// sub-workflow proxy task, i.e. parent_task_id set) from submission
	// listings. User-facing lists set this so scatter fan-out does not flood
	// the view; the scheduler must leave it unset because children need
	// scheduling like any other submission.
	ExcludeChildren bool
	// OutputState filters submissions by output delivery state ("delivered",
	// "upload_failed", ...). A comma-separated list matches any of the given
	// states. Empty means no filter.
	OutputState string
}

// OutputStates splits the OutputState filter into its individual values,
// trimming whitespace and dropping empties.
func (o ListOptions) OutputStates() []string {
	var out []string
	for _, v := range strings.Split(o.OutputState, ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// DefaultListOptions returns sensible defaults.
func DefaultListOptions() ListOptions {
	return ListOptions{Limit: 20, Offset: 0}
}

// Clamp enforces limits (max MaxListLimit, min 1).
func (o *ListOptions) Clamp() {
	if o.Limit <= 0 {
		o.Limit = 20
	}
	if o.Limit > MaxListLimit {
		o.Limit = MaxListLimit
	}
	if o.Offset < 0 {
		o.Offset = 0
	}
}
