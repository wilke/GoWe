package scheduler

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// wsStagerInterface is the subset of WorkspaceStager methods needed by the scheduler.
type wsStagerInterface interface {
	StageIn(ctx context.Context, location string, destPath string, opts staging.StageOptions) error
	StageOut(ctx context.Context, srcPath string, taskID string, opts staging.StageOptions) (string, error)
	WithToken(token string) *staging.WorkspaceStager
}

// SetWorkspaceStager configures server-side workspace staging.
func (l *Loop) SetWorkspaceStager(ws wsStagerInterface) {
	l.wsStager = ws
}

// prestageWorkspaceInputs downloads ws:// inputs for PENDING submissions,
// rewrites them to file:// locations, and updates the submission in the store.
func (l *Loop) prestageWorkspaceInputs(ctx context.Context, affected map[string]bool) error {
	// Find PENDING submissions that may have ws:// inputs.
	subs, _, err := l.store.ListSubmissions(ctx, model.ListOptions{State: "PENDING", Limit: 100})
	if err != nil {
		return fmt.Errorf("list pending submissions: %w", err)
	}

	for _, sub := range subs {
		if sub.UserToken == "" {
			continue // No token, skip (can't auth to workspace)
		}

		// Check if any input has a ws:// location.
		wsLocations := findWSLocations(sub.Inputs)
		if len(wsLocations) == 0 {
			continue
		}

		// Create a per-submission staging directory.
		stageDir := filepath.Join(os.TempDir(), "gowe-ws-stage", sub.ID)
		if err := os.MkdirAll(stageDir, 0o755); err != nil {
			l.logger.Error("create ws stage dir", "submission_id", sub.ID, "error", err)
			continue
		}

		// Create a token-scoped stager for this submission.
		stager := l.wsStager.WithToken(sub.UserToken)

		allOK := true
		for _, loc := range wsLocations {
			basename := filepath.Base(loc.path)
			destPath := filepath.Join(stageDir, basename)

			l.logger.Info("pre-staging workspace input",
				"submission_id", sub.ID,
				"ws_path", loc.path,
				"dest", destPath,
			)

			err := stager.StageIn(ctx, loc.location, destPath, staging.StageOptions{})
			if err != nil {
				l.logger.Error("pre-stage workspace input failed",
					"submission_id", sub.ID,
					"location", loc.location,
					"error", err,
				)
				allOK = false
				break
			}

			// Rewrite the location in the inputs map to file://.
			rewriteLocation(sub.Inputs, loc.location, "file://"+destPath)
		}

		if !allOK {
			continue // Leave inputs unchanged; worker will try ws:// if it has the stager.
		}

		// Persist the rewritten inputs.
		if err := l.store.UpdateSubmissionInputs(ctx, sub.ID, sub.Inputs); err != nil {
			l.logger.Error("update submission inputs after pre-stage",
				"submission_id", sub.ID, "error", err)
		} else {
			l.cache.invalidateSubmission(sub.ID)
			l.logger.Info("pre-staged workspace inputs",
				"submission_id", sub.ID, "count", len(wsLocations))
		}
		affected[sub.ID] = true
	}

	return nil
}

// poststageWorkspaceOutputs uploads outputs for completed submissions that have
// an OutputDestination, updates output locations to ws:// URIs, and marks delivery.
func (l *Loop) poststageWorkspaceOutputs(ctx context.Context, affected map[string]bool) error {
	// Find COMPLETED submissions with output_destination that haven't been uploaded yet.
	subs, _, err := l.store.ListSubmissions(ctx, model.ListOptions{State: "COMPLETED", Limit: 100})
	if err != nil {
		return fmt.Errorf("list completed submissions: %w", err)
	}

	for _, sub := range subs {
		if sub.OutputDestination == "" || sub.OutputState != "" {
			continue // No destination or already processed.
		}
		if sub.UserToken == "" {
			l.logger.Warn("post-stage failed: no user token",
				"submission_id", sub.ID)
			l.failOutputStaging(ctx, sub, "no authentication token for workspace upload")
			affected[sub.ID] = true
			continue
		}
		if !strings.HasPrefix(sub.OutputDestination, "ws://") {
			l.logger.Warn("post-stage failed: unsupported destination scheme",
				"submission_id", sub.ID, "destination", sub.OutputDestination)
			l.failOutputStaging(ctx, sub, "unsupported output destination scheme: "+sub.OutputDestination)
			affected[sub.ID] = true
			continue
		}

		// Mark as uploading.
		sub.OutputState = "uploading"
		if err := l.updateSubmission(ctx, sub); err != nil {
			l.logger.Error("mark uploading", "submission_id", sub.ID, "error", err)
			continue
		}

		stager := l.wsStager.WithToken(sub.UserToken)
		baseDest := parseWSPath(sub.OutputDestination)

		allOK := true
		stageFileInTree(sub.Outputs, "", func(filePath, location, subPath string) bool {
			dest := baseDest
			if subPath != "" {
				dest = strings.TrimRight(dest, "/") + "/" + subPath
			}

			l.logger.Info("post-staging workspace output",
				"submission_id", sub.ID,
				"src", filePath,
				"dest", dest,
			)

			opts := staging.StageOptions{
				Metadata: map[string]string{"destination": dest},
			}

			wsURI, err := stager.StageOut(ctx, filePath, sub.ID, opts)
			if err != nil {
				l.logger.Error("post-stage workspace output failed",
					"submission_id", sub.ID,
					"file", filePath,
					"error", err,
				)
				allOK = false
				return false // stop
			}

			rewriteLocation(sub.Outputs, location, wsURI)
			return true // continue
		})

		if allOK {
			sub.OutputState = "delivered"
			if err := l.updateSubmission(ctx, sub); err != nil {
				l.logger.Error("update submission after post-stage",
					"submission_id", sub.ID, "error", err)
			} else {
				l.logger.Info("post-staged workspace outputs",
					"submission_id", sub.ID,
					"state", sub.OutputState,
				)
			}
		} else {
			l.failOutputStaging(ctx, sub, "workspace output upload failed")
		}
		affected[sub.ID] = true
	}

	return nil
}

// failOutputStaging marks a submission as FAILED due to output staging issues.
func (l *Loop) failOutputStaging(ctx context.Context, sub *model.Submission, reason string) {
	sub.OutputState = "upload_failed"
	sub.State = model.SubmissionStateFailed
	now := time.Now().UTC()
	sub.CompletedAt = &now
	sub.Error = &model.SubmissionError{
		Code:    "OUTPUT_STAGING_FAILED",
		Message: reason,
	}
	if err := l.updateSubmission(ctx, sub); err != nil {
		l.logger.Error("mark output staging failed", "submission_id", sub.ID, "error", err)
	} else {
		l.logger.Info("submission failed: output staging", "submission_id", sub.ID, "reason", reason)
	}
}

// wsLocation describes a ws:// file/directory reference found in inputs.
type wsLocation struct {
	location string // Full URI: ws:///user@bvbrc/home/file.fasta
	path     string // Workspace path: /user@bvbrc/home/file.fasta
}

// findWSLocations walks an inputs map and returns all ws:// File/Directory locations.
func findWSLocations(inputs map[string]any) []wsLocation {
	var locs []wsLocation
	walkLocations(inputs, func(loc string) {
		if strings.HasPrefix(loc, "ws://") {
			path := loc[len("ws://"):]
			if !strings.HasPrefix(path, "/") {
				path = "/" + strings.TrimLeft(path, "/")
			}
			locs = append(locs, wsLocation{location: loc, path: path})
		}
	})
	return locs
}

// walkLocations visits all "location" fields in File/Directory CWL objects.
func walkLocations(v any, fn func(string)) {
	switch val := v.(type) {
	case map[string]any:
		if class, ok := val["class"].(string); ok && (class == "File" || class == "Directory") {
			if loc, ok := val["location"].(string); ok && loc != "" {
				fn(loc)
			}
		}
		for _, item := range val {
			walkLocations(item, fn)
		}
	case []any:
		for _, item := range val {
			walkLocations(item, fn)
		}
	}
}

// rewriteLocation recursively replaces oldLoc with newLoc in File/Directory objects.
func rewriteLocation(v any, oldLoc, newLoc string) {
	switch val := v.(type) {
	case map[string]any:
		if loc, ok := val["location"].(string); ok && loc == oldLoc {
			val["location"] = newLoc
		}
		for _, item := range val {
			rewriteLocation(item, oldLoc, newLoc)
		}
	case []any:
		for _, item := range val {
			rewriteLocation(item, oldLoc, newLoc)
		}
	}
}

// stageFileInTree walks the output tree, tracking directory paths, and calls fn for each
// file:// File. fn receives (filePath, location, subPath) where subPath is the relative
// directory path from accumulated Directory basenames. Returns false to stop.
func stageFileInTree(v any, subPath string, fn func(filePath, location, subPath string) bool) bool {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" {
			loc, _ := val["location"].(string)
			if strings.HasPrefix(loc, "file://") {
				filePath := loc[len("file://"):]
				if !fn(filePath, loc, subPath) {
					return false
				}
			}
			return true
		}
		if class == "Directory" {
			childSubPath := subPath
			if basename, ok := val["basename"].(string); ok && basename != "" {
				if childSubPath != "" {
					childSubPath = childSubPath + "/" + basename
				} else {
					childSubPath = basename
				}
			}
			if listing, ok := val["listing"].([]any); ok {
				for _, item := range listing {
					if !stageFileInTree(item, childSubPath, fn) {
						return false
					}
				}
			}
			return true
		}
		for _, item := range val {
			if !stageFileInTree(item, subPath, fn) {
				return false
			}
		}
	case []any:
		for _, item := range val {
			if !stageFileInTree(item, subPath, fn) {
				return false
			}
		}
	}
	return true
}

// parseWSPath extracts the workspace path from a ws:// URI, for use as the destination dir.
func parseWSPath(uri string) string {
	path := uri
	if strings.HasPrefix(path, "ws://") {
		path = path[len("ws://"):]
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + strings.TrimLeft(path, "/")
	}
	return path
}

