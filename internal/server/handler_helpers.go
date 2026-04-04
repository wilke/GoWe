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

	if limit := r.URL.Query().Get("limit"); limit != "" {
		if n, err := strconv.Atoi(limit); err == nil && n > 0 && n <= 100 {
			opts.Limit = n
		}
	}

	if offset := r.URL.Query().Get("offset"); offset != "" {
		if n, err := strconv.Atoi(offset); err == nil && n >= 0 {
			opts.Offset = n
		}
	}

	if state := r.URL.Query().Get("state"); state != "" {
		opts.State = strings.ToUpper(state)
	}

	if search := r.URL.Query().Get("search"); search != "" {
		opts.Search = strings.TrimSpace(search)
	}

	if sortBy := r.URL.Query().Get("sort"); sortBy != "" {
		opts.SortBy = sortBy
	}

	if sortDir := r.URL.Query().Get("dir"); sortDir != "" {
		opts.SortDir = strings.ToLower(sortDir)
	}

	opts.Clamp()
	return opts
}
