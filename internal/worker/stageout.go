package worker

import (
	"context"
	"errors"
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/me/gowe/internal/execution"
	"github.com/me/gowe/pkg/cwl"
	"github.com/me/gowe/pkg/model"
	"github.com/me/gowe/pkg/staging"
)

// outputStager stages one task's File/Directory outputs, keeping each
// object's layout below the task output directory (outDir) at the
// destination, and detecting distinct sources that claim the same
// destination path.
type outputStager struct {
	w      *Worker
	ctx    context.Context
	task   *model.Task
	stager execution.Stager
	outDir string // absolute CWL output directory of the task

	staged   map[string]string // source path -> staged location
	dests    map[string]string // destination RelPath -> source path
	collided []string
}

func (w *Worker) newOutputStager(ctx context.Context, task *model.Task, stager execution.Stager, taskDir string) *outputStager {
	outDir := filepath.Clean(taskDir)
	if abs, err := filepath.Abs(outDir); err == nil {
		outDir = abs
	}
	return &outputStager{
		w: w, ctx: ctx, task: task, stager: stager, outDir: outDir,
		staged: make(map[string]string),
		dests:  make(map[string]string),
	}
}

// stageOutputValue stages the outputs in v with a fresh walker rooted at the
// task's directory under the worker's work dir.
func (w *Worker) stageOutputValue(ctx context.Context, v any, task *model.Task, stager execution.Stager, destSubPath string) any {
	return w.newOutputStager(ctx, task, stager, filepath.Join(w.workDir, task.ID)).stage(v, destSubPath)
}

// err reports destination collisions found while staging.
func (s *outputStager) err() error {
	if len(s.collided) == 0 {
		return nil
	}
	return errors.New(strings.Join(s.collided, "\n"))
}

// relPath returns the destination path (slash-separated, below the task root)
// for the object at p: its location relative to the output directory when it
// lies inside it, otherwise destSubPath/basename. The relative path is
// computed lexically (no symlink resolution): outputs may legitimately be
// symlinks inside the output directory.
func (s *outputStager) relPath(p, destSubPath, basename string) string {
	if rel, err := filepath.Rel(s.outDir, p); err == nil && rel != "." && filepath.IsLocal(rel) {
		return filepath.ToSlash(rel)
	}
	if basename == "" {
		basename = filepath.Base(p)
	}
	return path.Join(destSubPath, basename)
}

func (s *outputStager) stage(v any, destSubPath string) any {
	switch val := v.(type) {
	case map[string]any:
		switch class, _ := val["class"].(string); class {
		case "File":
			s.stageFile(val, destSubPath)
			return val
		case "Directory":
			s.stageDirectory(val, destSubPath)
			return val
		}
		for k, item := range val {
			val[k] = s.stage(item, destSubPath)
		}
		return val
	case []any:
		for i, item := range val {
			val[i] = s.stage(item, destSubPath)
		}
		return val
	case []map[string]any:
		result := make([]any, len(val))
		for i, item := range val {
			result[i] = s.stage(item, destSubPath)
		}
		return result
	default:
		return v
	}
}

// markStaged records the staged location on a File/Directory object. If the
// output actually moved (not an in-place file:// URI), the stale local
// path/dirname are dropped so server-side consumers resolve via the staged
// location instead of a path inside a task dir that cleanupTaskDir may delete.
func markStaged(val map[string]any, p, loc string) {
	val["location"] = loc
	if loc != cwl.BuildLocation(cwl.SchemeFile, p) {
		delete(val, "path")
		delete(val, "dirname")
	}
}

func (s *outputStager) stageFile(val map[string]any, destSubPath string) {
	if p, ok := val["path"].(string); ok {
		s.stageFilePath(val, p, destSubPath)
	}
	// Also stage secondary files.
	if secFiles, ok := val["secondaryFiles"].([]any); ok {
		for i, sf := range secFiles {
			secFiles[i] = s.stage(sf, destSubPath)
		}
	}
}

func (s *outputStager) stageFilePath(val map[string]any, p, destSubPath string) {
	w := s.w
	src := filepath.Clean(p)
	basename, _ := val["basename"].(string)
	rel := s.relPath(src, destSubPath, basename)

	if prev, ok := s.dests[rel]; ok && prev != src {
		s.collided = append(s.collided, fmt.Sprintf("stage-out: destination %s claimed by both %s and %s", rel, prev, src))
		return
	}
	if loc, ok := s.staged[src]; ok {
		markStaged(val, p, loc)
		return
	}

	opts := execution.StageOptions{RelPath: rel}
	if s.task.RuntimeHints != nil && s.task.RuntimeHints.OutputDestination != "" {
		dest := s.task.RuntimeHints.OutputDestination
		if destSubPath != "" {
			dest = strings.TrimRight(dest, "/") + "/" + destSubPath
		}
		opts.Metadata = map[string]string{"destination": dest}
		w.logger.Debug("staging file to ws", "file", filepath.Base(p), "dest", dest)
	}
	loc, err := s.stager.StageOut(s.ctx, p, s.task.ID, opts)
	if err != nil {
		w.logger.Warn("stage-out failed", "path", p, "error", err)
		return
	}
	w.logger.Debug("staged file", "file", filepath.Base(p), "location", loc)
	s.dests[rel] = src
	s.staged[src] = loc
	markStaged(val, p, loc)
}

func (s *outputStager) stageDirectory(val map[string]any, destSubPath string) {
	basename, _ := val["basename"].(string)

	// Recurse into listing, appending this directory's basename to the subpath.
	childSubPath := destSubPath
	if basename != "" {
		childSubPath = path.Join(destSubPath, basename)
	}
	hasListing := false
	switch listing := val["listing"].(type) {
	case []any:
		hasListing = true
		for i, item := range listing {
			listing[i] = s.stage(item, childSubPath)
		}
	case []map[string]any:
		hasListing = true
		converted := make([]any, len(listing))
		for i, item := range listing {
			converted[i] = s.stage(item, childSubPath)
		}
		val["listing"] = converted
	}

	p, ok := val["path"].(string)
	if !ok || p == "" {
		return
	}
	ds, ok := s.stager.(staging.DirectoryStager)
	if !ok {
		return
	}
	rel := s.relPath(filepath.Clean(p), destSubPath, basename)
	opts := execution.StageOptions{RelPath: rel, CopyTree: !hasListing}
	if s.task.RuntimeHints != nil && s.task.RuntimeHints.OutputDestination != "" {
		dest := strings.TrimRight(s.task.RuntimeHints.OutputDestination, "/") + "/" + rel
		opts.Metadata = map[string]string{"destination": dest}
	}
	loc, err := ds.StageOutDir(s.ctx, p, s.task.ID, opts)
	switch {
	case err == nil:
		markStaged(val, p, loc)
	case errors.Is(err, staging.ErrDirectoryStagingUnsupported):
		s.w.logger.Debug("directory stage-out unsupported, leaving directory local", "path", p)
	default:
		// Leave the object untouched: the local location keeps the task
		// dir pinned so the data is not deleted.
		s.w.logger.Warn("directory stage-out failed", "path", p, "error", err)
	}
}
