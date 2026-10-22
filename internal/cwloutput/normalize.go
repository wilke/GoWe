package cwloutput

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// NormalizeOutputFiles walks a collected CWL output object (as produced by
// outputBinding/outputEval evaluation) and enforces the invariant that, for
// every materialized File or Directory node, `basename` equals
// filepath.Base(path). Per the CWL spec, `basename` is authoritative for a
// materialized file: when outputEval mutates it in memory (e.g. to rename a
// glob result), the on-disk file must follow.
//
// v is typically the map[string]any returned by CollectOutputs, but any
// value shape (map, []any, or a bare File/Directory map) is accepted; the
// walker recurses into records, arrays, secondaryFiles, and Directory
// listings.
//
// rootDir, when non-empty, restricts renaming to files rooted under it
// (compared via filepath.Abs). A node whose path resolves outside rootDir is
// left untouched rather than errored: outputEval can legally return an input
// File object (passed through with a mutated basename) whose path points at
// staged/original data outside the tool's own output directory, and renaming
// that would corrupt data GoWe does not own. Pass "" to disable the guard
// (e.g. in unit tests that already scope every path under one temp dir).
//
// File objects with no resolvable path (file literals materialized only in
// memory, e.g. from an ExpressionTool) are tolerated and left untouched —
// there is nothing on disk to keep consistent.
//
// Renames within a single sibling group (the elements of one glob result, one
// secondaryFiles list, or one Directory listing) are collision-safe: if two
// siblings swap basenames, both move through unique temporary names first so
// neither clobbers the other. If a rename target already exists on disk and
// is not being vacated by a sibling in the same batch, NormalizeOutputFiles
// returns an error naming both the source and the pre-existing file rather
// than silently overwriting it.
func NormalizeOutputFiles(v any, rootDir string) error {
	return normalizeValue(v, rootDir)
}

// fsNode wraps a File/Directory map so normalizeBatch can report back
// whether it renamed the node (needed by the caller to decide whether a
// Directory's listing needs its path prefix rewritten).
type fsNode struct {
	node    map[string]any
	renamed bool
}

// renameOp is a single pending on-disk rename within one batch.
type renameOp struct {
	n       *fsNode
	oldPath string
	newPath string
	tmpPath string
}

func normalizeValue(v any, rootDir string) error {
	switch val := v.(type) {
	case map[string]any:
		return normalizeMapValue(val, rootDir)
	case []any:
		return normalizeArrayValue(val, rootDir)
	default:
		// outputEval (goja) can export a homogeneous JS array of objects as
		// a concretely-typed Go slice (e.g. []map[string]any) rather than
		// []any. Handle any slice kind generically: the elements are still
		// the same map[string]any instances (maps are reference types), so
		// mutating them here is visible through the caller's original slice
		// regardless of which slice header we walk.
		rv := reflect.ValueOf(v)
		if rv.Kind() != reflect.Slice {
			return nil
		}
		arr := make([]any, rv.Len())
		for i := 0; i < rv.Len(); i++ {
			arr[i] = rv.Index(i).Interface()
		}
		return normalizeArrayValue(arr, rootDir)
	}
}

// normalizeMapValue handles a single map: either a File/Directory node (a
// "batch of one", since there are no declared siblings at this level) or a
// generic record, whose fields are recursed into independently.
func normalizeMapValue(m map[string]any, rootDir string) error {
	class, _ := m["class"].(string)
	switch class {
	case "File":
		if sf, ok := m["secondaryFiles"]; ok {
			if err := normalizeValue(sf, rootDir); err != nil {
				return err
			}
		}
		return normalizeBatch([]*fsNode{{node: m}}, rootDir)
	case "Directory":
		return normalizeDirectory(m, rootDir)
	default:
		for _, item := range m {
			if err := normalizeValue(item, rootDir); err != nil {
				return err
			}
		}
		return nil
	}
}

// normalizeArrayValue handles a []any, treating its direct File/Directory
// elements as one collision-safe sibling batch (this is the shape of a glob
// result / File[] output — the case the #212 bug actually manifests in).
func normalizeArrayValue(arr []any, rootDir string) error {
	var nodes []*fsNode
	dirOldPath := map[*fsNode]string{}

	for _, it := range arr {
		m, ok := it.(map[string]any)
		if !ok {
			if err := normalizeValue(it, rootDir); err != nil {
				return err
			}
			continue
		}
		class, _ := m["class"].(string)
		switch class {
		case "File":
			if sf, ok := m["secondaryFiles"]; ok {
				if err := normalizeValue(sf, rootDir); err != nil {
					return err
				}
			}
			nodes = append(nodes, &fsNode{node: m})
		case "Directory":
			oldPath := nodePath(m)
			if listing, ok := m["listing"]; ok {
				if err := normalizeValue(listing, rootDir); err != nil {
					return err
				}
			}
			n := &fsNode{node: m}
			nodes = append(nodes, n)
			dirOldPath[n] = oldPath
		default:
			if err := normalizeValue(it, rootDir); err != nil {
				return err
			}
		}
	}

	if len(nodes) == 0 {
		return nil
	}
	if err := normalizeBatch(nodes, rootDir); err != nil {
		return err
	}
	for _, n := range nodes {
		oldPath, isDir := dirOldPath[n]
		if !isDir || !n.renamed || oldPath == "" {
			continue
		}
		if listing, ok := n.node["listing"]; ok {
			newPath := nodePath(n.node)
			rewritePathPrefix(listing, oldPath, newPath)
		}
	}
	return nil
}

// normalizeDirectory normalizes a single (non-batched) Directory node: its
// listing first (children move while this directory's own path is still
// valid), then itself, then rewrites the prefix of the already-normalized
// listing to reflect this directory's new location.
func normalizeDirectory(m map[string]any, rootDir string) error {
	oldPath := nodePath(m)
	if listing, ok := m["listing"]; ok {
		if err := normalizeValue(listing, rootDir); err != nil {
			return err
		}
	}
	n := &fsNode{node: m}
	if err := normalizeBatch([]*fsNode{n}, rootDir); err != nil {
		return err
	}
	if n.renamed && oldPath != "" {
		if listing, ok := m["listing"]; ok {
			rewritePathPrefix(listing, oldPath, nodePath(m))
		}
	}
	return nil
}

// normalizeBatch computes and executes the renames needed for one sibling
// group. It is collision-safe: ambiguous or clobbering targets are reported
// as an error (naming both files involved) before anything is touched on
// disk; legal permutations (including full swaps) are executed via a
// two-phase temporary rename so no sibling is ever overwritten mid-batch.
func normalizeBatch(nodes []*fsNode, rootDir string) error {
	var ops []*renameOp
	for _, n := range nodes {
		path := nodePath(n.node)
		basename, _ := n.node["basename"].(string)
		if path == "" || basename == "" {
			continue // file/dir literal or incomplete object; tolerated.
		}
		if filepath.Base(path) == basename {
			continue // already consistent.
		}
		if !withinRoot(path, rootDir) {
			// outputEval may legally return an input File object whose path
			// points outside this tool's own output tree (e.g. staged input
			// data). Never rename data GoWe doesn't own.
			continue
		}
		ops = append(ops, &renameOp{n: n, oldPath: path, newPath: filepath.Join(filepath.Dir(path), basename)})
	}
	if len(ops) == 0 {
		return nil
	}

	var errs []string

	// Ambiguous: two or more sources want the same final name.
	byTarget := make(map[string][]*renameOp)
	for _, op := range ops {
		byTarget[op.newPath] = append(byTarget[op.newPath], op)
	}
	for target, group := range byTarget {
		if len(group) > 1 {
			var srcs []string
			for _, op := range group {
				srcs = append(srcs, op.oldPath)
			}
			sort.Strings(srcs)
			errs = append(errs, fmt.Sprintf("multiple outputs rename to %q: %s", target, strings.Join(srcs, ", ")))
		}
	}

	// Clobber: target exists on disk and isn't being vacated by a sibling in
	// this same batch (a swap/chain is fine; a foreign file is not).
	movingFrom := make(map[string]bool, len(ops))
	for _, op := range ops {
		movingFrom[op.oldPath] = true
	}
	for _, op := range ops {
		if _, err := os.Lstat(op.newPath); err == nil {
			if !movingFrom[op.newPath] {
				errs = append(errs, fmt.Sprintf("cannot rename %q to %q: target already exists (refusing to overwrite)", op.oldPath, op.newPath))
			}
		}
	}

	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("cwloutput: output basename rename conflict(s):\n  %s", strings.Join(errs, "\n  "))
	}

	// Phase 1: move every source to a unique temp name in the same
	// directory, so same-directory permutations (e.g. A<->B swaps) never
	// clobber a sibling that hasn't moved yet.
	for i, op := range ops {
		tmp := fmt.Sprintf("%s.gowe-rename-tmp-%d-%d", op.oldPath, os.Getpid(), i)
		if err := os.Rename(op.oldPath, tmp); err != nil {
			for j := 0; j < i; j++ {
				_ = os.Rename(ops[j].tmpPath, ops[j].oldPath)
			}
			return fmt.Errorf("cwloutput: rename %q to temp name: %w", op.oldPath, err)
		}
		op.tmpPath = tmp
	}

	// Phase 2: move every temp name to its final target.
	for _, op := range ops {
		if err := os.Rename(op.tmpPath, op.newPath); err != nil {
			return fmt.Errorf("cwloutput: rename %q to %q: %w", op.tmpPath, op.newPath, err)
		}
	}

	for _, op := range ops {
		op.n.renamed = true
		updateNodePaths(op.n.node, op.newPath)
	}
	return nil
}

// nodePath returns the best-known on-disk path for a File/Directory node,
// falling back to a file:// (or scheme-less) location when path is absent.
func nodePath(node map[string]any) string {
	if p, ok := node["path"].(string); ok && p != "" {
		return p
	}
	if loc, ok := node["location"].(string); ok {
		if strings.HasPrefix(loc, "file://") {
			return strings.TrimPrefix(loc, "file://")
		}
		if !strings.Contains(loc, "://") && loc != "" {
			return loc
		}
	}
	return ""
}

// withinRoot reports whether path is rootDir itself or is nested under it.
// An empty rootDir disables the check (always true).
func withinRoot(path, rootDir string) bool {
	if rootDir == "" {
		return true
	}
	absPath, err1 := filepath.Abs(path)
	absRoot, err2 := filepath.Abs(rootDir)
	if err1 != nil || err2 != nil {
		return false
	}
	if absPath == absRoot {
		return true
	}
	return strings.HasPrefix(absPath, absRoot+string(filepath.Separator))
}

// updateNodePaths updates a File/Directory node's path, location, and (for
// Files) nameroot/nameext to reflect a completed on-disk rename to newPath.
// basename is left untouched — it is the field that drove the rename and is
// already correct.
func updateNodePaths(node map[string]any, newPath string) {
	node["path"] = newPath
	if loc, ok := node["location"].(string); ok {
		if strings.HasPrefix(loc, "file://") {
			node["location"] = "file://" + newPath
		} else if !strings.Contains(loc, "://") {
			node["location"] = newPath
		}
		// Other schemes (e.g. remote/ws://) are left untouched: a local
		// rename doesn't apply to them.
	}
	if class, _ := node["class"].(string); class == "File" {
		base := filepath.Base(newPath)
		nameroot, nameext := splitNameExt(base)
		node["nameroot"] = nameroot
		node["nameext"] = nameext
	}
}

// rewritePathPrefix rewrites the path/location prefix of every File/Directory
// node reachable from v whose path starts with oldPrefix, replacing it with
// newPrefix. Used after a Directory rename: its descendants moved on disk for
// free (they're nested inside), but their recorded path/location strings
// still reference the directory's old absolute path.
func rewritePathPrefix(v any, oldPrefix, newPrefix string) {
	switch val := v.(type) {
	case map[string]any:
		class, _ := val["class"].(string)
		if class == "File" || class == "Directory" {
			if p, ok := val["path"].(string); ok && p != "" {
				if p == oldPrefix || strings.HasPrefix(p, oldPrefix+string(filepath.Separator)) {
					newP := newPrefix + strings.TrimPrefix(p, oldPrefix)
					val["path"] = newP
					if loc, ok := val["location"].(string); ok {
						if strings.HasPrefix(loc, "file://") {
							val["location"] = "file://" + newP
						} else if !strings.Contains(loc, "://") {
							val["location"] = newP
						}
					}
					// dirname (when present) is set elsewhere in the
					// codebase as filepath.Dir(path) (e.g.
					// internal/cwltool/helpers.go, internal/toolexec/
					// outputs.go); keep it in sync with the rewritten path
					// rather than leaving a stale parent-dir string behind
					// (#214).
					if _, ok := val["dirname"]; ok {
						val["dirname"] = filepath.Dir(newP)
					}
				}
			}
		}
		if sf, ok := val["secondaryFiles"]; ok {
			rewritePathPrefix(sf, oldPrefix, newPrefix)
		}
		if listing, ok := val["listing"]; ok {
			rewritePathPrefix(listing, oldPrefix, newPrefix)
		}
	case []any:
		for _, it := range val {
			rewritePathPrefix(it, oldPrefix, newPrefix)
		}
	}
}

// splitNameExt splits a basename into nameroot and nameext, per CWL
// semantics (extension is everything from the last '.' onward, unless the
// '.' is the first character).
func splitNameExt(basename string) (string, string) {
	for i := len(basename) - 1; i > 0; i-- {
		if basename[i] == '.' {
			return basename[:i], basename[i:]
		}
	}
	return basename, ""
}
