package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/me/gowe/pkg/model"
)

// parseListOptions extracts pagination, filtering, and sorting parameters
// from the request query string. Mirrors the UI's parseListOptions.
func parseListOptions(r *http.Request) model.ListOptions {
	opts := model.ListOptions{
		Limit:  20,
		Offset: 0,
	}

	q := r.URL.Query()

	if limit := q.Get("limit"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n > 0 {
			opts.Limit = n
		}
	}

	if offset := q.Get("offset"); offset != "" {
		if n, err := strconv.Atoi(offset); err == nil && n >= 0 {
			opts.Offset = n
		}
	}

	if state := q.Get("state"); state != "" {
		opts.State = strings.ToUpper(state)
	}

	if search := q.Get("search"); search != "" {
		opts.Search = strings.TrimSpace(search)
	}

	if sortBy := q.Get("sort"); sortBy != "" {
		opts.SortBy = sortBy
	}

	if sortDir := q.Get("dir"); sortDir != "" {
		opts.SortDir = strings.ToLower(sortDir)
	}

	// Endpoint-specific filters — parsed here to avoid re-parsing the query string.
	opts.Class = q.Get("class")
	opts.DateStart = q.Get("date_start")
	opts.DateEnd = q.Get("date_end")
	opts.WorkflowID = q.Get("workflow_id")

	opts.Clamp()
	return opts
}

// paginateBounds returns clamped start/end indices for in-memory pagination.
func paginateBounds(total, offset, limit int) (start, end int) {
	start = offset
	if start > total {
		start = total
	}
	end = start + limit
	if end > total {
		end = total
	}
	return
}
