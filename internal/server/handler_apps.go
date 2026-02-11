package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/me/gowe/pkg/model"
)

// appCache caches the result of enumerate_apps to avoid calling BV-BRC on
// every request. Entries expire after cacheTTL.
type appCache struct {
	mu      sync.Mutex
	apps    []map[string]any
	fetched time.Time
}

const cacheTTL = 5 * time.Minute

var appsCache appCache

// fetchApps returns apps from the live BV-BRC service (cached), or the
// static test apps if configured. Returns nil when neither is available.
func (s *Server) fetchApps(r *http.Request) []map[string]any {
	// Static test apps (injected via WithTestApps) bypass the RPC caller.
	if s.testApps != nil {
		return s.testApps
	}

	if s.bvbrcCaller == nil {
		return nil
	}

	appsCache.mu.Lock()
	defer appsCache.mu.Unlock()

	if appsCache.apps != nil && time.Since(appsCache.fetched) < cacheTTL {
		return appsCache.apps
	}

	result, err := s.bvbrcCaller.Call(r.Context(), "AppService.enumerate_apps", []any{})
	if err != nil {
		s.logger.Warn("enumerate_apps failed, using cache", "error", err)
		if appsCache.apps != nil {
			return appsCache.apps
		}
		return nil
	}

	// Result is [[app1, app2, ...]] — unwrap outer array.
	var outer []json.RawMessage
	if err := json.Unmarshal(result, &outer); err != nil || len(outer) == 0 {
		s.logger.Warn("enumerate_apps: unexpected shape", "raw", string(result[:min(len(result), 200)]))
		return nil
	}

	var apps []map[string]any
	if err := json.Unmarshal(outer[0], &apps); err != nil {
		s.logger.Warn("enumerate_apps: cannot parse app array", "error", err)
		return nil
	}

	appsCache.apps = apps
	appsCache.fetched = time.Now()
	return apps
}

// findApp returns the app with the given ID, or nil.
func findApp(apps []map[string]any, appID string) map[string]any {
	for _, app := range apps {
		if id, _ := app["id"].(string); id == appID {
			return app
		}
	}
	return nil
}

func (s *Server) handleListApps(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())

	apps := s.fetchApps(r)
	if apps == nil {
		apps = []map[string]any{}
	}

	respondList(w, reqID, apps, &model.Pagination{
		Total: len(apps), Limit: len(apps), Offset: 0, HasMore: false,
	})
}

func (s *Server) handleGetApp(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	appID := chi.URLParam(r, "appID")

	apps := s.fetchApps(r)
	if apps == nil {
		respondError(w, reqID, http.StatusServiceUnavailable, &model.APIError{
			Code:    model.ErrInternal,
			Message: "BV-BRC connection not configured",
		})
		return
	}

	app := findApp(apps, appID)
	if app == nil {
		respondError(w, reqID, http.StatusNotFound, &model.APIError{
			Code:    model.ErrNotFound,
			Message: "App '" + appID + "' not found in BV-BRC",
		})
		return
	}

	respondOK(w, reqID, app)
}

func (s *Server) handleGetAppCWLTool(w http.ResponseWriter, r *http.Request) {
	reqID := RequestIDFromContext(r.Context())
	appID := chi.URLParam(r, "appID")

	apps := s.fetchApps(r)
	if apps == nil {
		respondError(w, reqID, http.StatusServiceUnavailable, &model.APIError{
			Code:    model.ErrInternal,
			Message: "BV-BRC connection not configured",
		})
		return
	}

	app := findApp(apps, appID)
	if app == nil {
		respondError(w, reqID, http.StatusNotFound, &model.APIError{
			Code:    model.ErrNotFound,
			Message: "App '" + appID + "' not found in BV-BRC",
		})
		return
	}

	// Generate CWL inputs from the app's parameters if available.
	inputsYAML := defaultCWLInputs
	if params, ok := app["parameters"].([]any); ok && len(params) > 0 {
		inputsYAML = generateCWLInputs(params)
	}

	cwlTool := fmt.Sprintf(`cwlVersion: v1.2
class: CommandLineTool

hints:
  goweHint:
    bvbrc_app_id: %s
    executor: bvbrc

baseCommand: ["true"]

doc: "Auto-generated CWL tool wrapper for %s"

inputs:
%s
outputs:
  result:
    type: Directory
    outputBinding:
      glob: "."
`, appID, appID, inputsYAML)

	respondOK(w, reqID, map[string]any{
		"app_id":   appID,
		"cwl_tool": cwlTool,
	})
}

const defaultCWLInputs = `  output_path:
    type: string
    doc: "Workspace path for results"
  output_file:
    type: string
    doc: "Prefix for output file names"
`

// generateCWLInputs builds the YAML inputs block from BV-BRC app parameters.
func generateCWLInputs(params []any) string {
	var out string
	for _, raw := range params {
		p, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := p["id"].(string)
		if id == "" {
			continue
		}
		cwlType := "string"
		required, _ := p["required"].(float64)
		if required == 0 {
			cwlType += "?"
		}
		desc, _ := p["desc"].(string)
		if desc == "" {
			desc, _ = p["label"].(string)
		}
		out += fmt.Sprintf("  %s:\n    type: %s\n", id, cwlType)
		if desc != "" {
			out += fmt.Sprintf("    doc: %q\n", desc)
		}
	}
	if out == "" {
		return defaultCWLInputs
	}
	return out
}
