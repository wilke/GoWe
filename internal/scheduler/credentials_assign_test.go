package scheduler

// Single-writer invariant for the credential fields (provider-token policy).
//
// RuntimeHints.InjectBVBRCToken, RuntimeHints.TokenBlessingID and
// StagerOverrides.HTTPCredential decide whether a user's token is placed in a
// tool's environment. They may be written only by:
//
//	internal/scheduler/credentials.go   (attachCredentials: the policy)
//	pkg/model/task.go                   (ScrubSecrets: clearing)
//	internal/store/tokens.go            (copy-on-write encrypt/decrypt)
//
// Whole-struct copies of RuntimeHints (cp := *h) are not flagged by design: the
// store guard (ErrUnblessedInject) and the per-decision reset in
// applyTokenGrant cover that case.
//
// The check is TYPE-AWARE (go/types): it resolves each selector to the field
// object of model.RuntimeHints / model.StagerOverrides, so same-named fields
// on other types (parser/model StepHints.InjectBVBRCToken, the worker's
// execution.StagerOverrides) are not flagged, and a write through an alias, an
// embedded struct or a method value of the right type is.

import (
	"fmt"
	"go/ast"
	"go/build"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/me/gowe"

var credentialWriterAllowList = map[string]bool{
	"internal/scheduler/credentials.go": true,
	"pkg/model/task.go":                 true,
	"internal/store/tokens.go":          true,
}

// guardedFields lists "TypeName.Field" in package pkg/model.
var guardedFields = []string{
	"RuntimeHints.InjectBVBRCToken",
	"RuntimeHints.TokenBlessingID",
	"StagerOverrides.HTTPCredential",
}

type moduleLoader struct {
	root  string
	fset  *token.FileSet
	std   types.ImporterFrom
	pkgs  map[string]*loadedPkg
	stack map[string]bool
}

type loadedPkg struct {
	path  string
	dir   string
	files []*ast.File
	names []string // file paths relative to root, parallel to files
	types *types.Package
	info  *types.Info
}

func newModuleLoader(root string) *moduleLoader {
	fset := token.NewFileSet()
	return &moduleLoader{
		root:  root,
		fset:  fset,
		std:   importer.ForCompiler(fset, "source", nil).(types.ImporterFrom),
		pkgs:  map[string]*loadedPkg{},
		stack: map[string]bool{},
	}
}

// Import implements types.Importer.
func (m *moduleLoader) Import(path string) (*types.Package, error) {
	return m.ImportFrom(path, "", 0)
}

// ImportFrom resolves module packages from source (non-test files), the
// standard library via the source importer, and fakes third-party packages
// (selectors into them stay untyped, which is irrelevant to the guarded
// fields).
func (m *moduleLoader) ImportFrom(path, dir string, mode types.ImportMode) (*types.Package, error) {
	if path == modulePath || strings.HasPrefix(path, modulePath+"/") {
		lp, err := m.load(path)
		if err != nil {
			return nil, err
		}
		return lp.types, nil
	}
	if !strings.Contains(strings.SplitN(path, "/", 2)[0], ".") {
		if p, err := m.std.ImportFrom(path, dir, mode); err == nil {
			return p, nil
		}
	}
	name := path[strings.LastIndex(path, "/")+1:]
	p := types.NewPackage(path, name)
	p.MarkComplete()
	return p, nil
}

func (m *moduleLoader) load(path string) (*loadedPkg, error) {
	if lp, ok := m.pkgs[path]; ok {
		return lp, nil
	}
	if m.stack[path] {
		return nil, fmt.Errorf("import cycle through %s", path)
	}
	m.stack[path] = true
	defer delete(m.stack, path)

	rel := strings.TrimPrefix(strings.TrimPrefix(path, modulePath), "/")
	dir := filepath.Join(m.root, filepath.FromSlash(rel))
	bp, err := build.Default.ImportDir(dir, 0)
	if err != nil {
		return nil, fmt.Errorf("import dir %s: %w", dir, err)
	}
	lp := &loadedPkg{path: path, dir: dir}
	for _, name := range bp.GoFiles { // non-test files honouring build tags
		full := filepath.Join(dir, name)
		f, err := parser.ParseFile(m.fset, full, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, err
		}
		lp.files = append(lp.files, f)
		r, _ := filepath.Rel(m.root, full)
		lp.names = append(lp.names, filepath.ToSlash(r))
	}
	lp.info = &types.Info{
		Selections: map[*ast.SelectorExpr]*types.Selection{},
		Uses:       map[*ast.Ident]types.Object{},
		Types:      map[ast.Expr]types.TypeAndValue{},
	}
	cfg := types.Config{
		Importer: m,
		Error:    func(error) {}, // tolerate unresolved third-party symbols
	}
	lp.types, _ = cfg.Check(path, m.fset, lp.files, lp.info)
	m.pkgs[path] = lp
	return lp, nil
}

// guardedObjects resolves the field objects of the guarded fields.
func (m *moduleLoader) guardedObjects(t *testing.T) map[types.Object]string {
	t.Helper()
	mp, err := m.load(modulePath + "/pkg/model")
	if err != nil {
		t.Fatalf("load pkg/model: %v", err)
	}
	out := map[types.Object]string{}
	for _, g := range guardedFields {
		parts := strings.SplitN(g, ".", 2)
		obj := mp.types.Scope().Lookup(parts[0])
		if obj == nil {
			t.Fatalf("model.%s not found", parts[0])
		}
		st, ok := obj.Type().Underlying().(*types.Struct)
		if !ok {
			t.Fatalf("model.%s is not a struct", parts[0])
		}
		found := false
		for i := 0; i < st.NumFields(); i++ {
			if st.Field(i).Name() == parts[1] {
				out[st.Field(i)] = g
				found = true
			}
		}
		if !found {
			t.Fatalf("field %s not found (renamed? update guardedFields)", g)
		}
	}
	return out
}

// violationsIn reports every write to a guarded field in the loaded package.
func (m *moduleLoader) violationsIn(lp *loadedPkg, guarded map[types.Object]string, allow map[string]bool) []string {
	var out []string
	for i, f := range lp.files {
		name := lp.names[i]
		if allow[name] {
			continue
		}
		report := func(pos token.Pos, what, field string) {
			out = append(out, fmt.Sprintf("%s: %s of %s", m.fset.Position(pos), what, field))
		}
		// fieldOf returns the guarded field a selector expression denotes.
		fieldOf := func(e ast.Expr) string {
			for {
				p, ok := e.(*ast.ParenExpr)
				if !ok {
					break
				}
				e = p.X
			}
			se, ok := e.(*ast.SelectorExpr)
			if !ok {
				return ""
			}
			if sel := lp.info.Selections[se]; sel != nil {
				return guarded[sel.Obj()]
			}
			return guarded[lp.info.Uses[se.Sel]]
		}
		ast.Inspect(f, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				for _, l := range x.Lhs {
					if g := fieldOf(l); g != "" {
						report(l.Pos(), "assignment", g)
					}
				}
			case *ast.IncDecStmt:
				if g := fieldOf(x.X); g != "" {
					report(x.Pos(), "inc/dec", g)
				}
			case *ast.UnaryExpr:
				if x.Op == token.AND { // &h.Field lets the holder write it
					if g := fieldOf(x.X); g != "" {
						report(x.Pos(), "address-of", g)
					}
				}
			case *ast.CompositeLit:
				tv, ok := lp.info.Types[x]
				if !ok {
					return true
				}
				typ := tv.Type
				if ptr, ok := typ.(*types.Pointer); ok {
					typ = ptr.Elem()
				}
				st, ok := typ.Underlying().(*types.Struct)
				if !ok {
					return true
				}
				for i, el := range x.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if id, ok := kv.Key.(*ast.Ident); ok {
							if g := guarded[lp.info.Uses[id]]; g != "" {
								report(kv.Pos(), "composite literal", g)
							}
						}
						continue
					}
					if i < st.NumFields() { // positional literal
						if g := guarded[st.Field(i)]; g != "" {
							report(el.Pos(), "positional composite literal", g)
						}
					}
				}
			}
			return true
		})
	}
	return out
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

// packageDirs lists every directory under the roots holding non-test Go files,
// skipping testdata and hidden dirs.
func packageDirs(t *testing.T, root string, roots ...string) []string {
	t.Helper()
	var dirs []string
	for _, r := range roots {
		_ = filepath.WalkDir(filepath.Join(root, r), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if d.IsDir() {
				if n := d.Name(); n == "testdata" || strings.HasPrefix(n, ".") {
					return filepath.SkipDir
				}
				if bp, err := build.Default.ImportDir(p, 0); err == nil && len(bp.GoFiles) > 0 {
					dirs = append(dirs, p)
				}
			}
			return nil
		})
	}
	sort.Strings(dirs)
	return dirs
}

func TestRuntimeHintsCredentialFields_AssignedOnlyInCredentialsGo(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the whole module")
	}
	root := findModuleRoot(t)
	m := newModuleLoader(root)
	guarded := m.guardedObjects(t)

	var violations []string
	checked := 0
	for _, dir := range packageDirs(t, root, "internal", "cmd", "pkg") {
		rel, _ := filepath.Rel(root, dir)
		lp, err := m.load(modulePath + "/" + filepath.ToSlash(rel))
		if err != nil {
			t.Fatalf("load %s: %v", rel, err)
		}
		checked += len(lp.files)
		violations = append(violations, m.violationsIn(lp, guarded, credentialWriterAllowList)...)
	}
	if checked < 100 {
		t.Fatalf("meta-test scanned only %d files; loader is broken", checked)
	}
	if len(violations) > 0 {
		t.Fatalf("RuntimeHints.InjectBVBRCToken / TokenBlessingID and StagerOverrides.HTTPCredential may be written only in %v; found:\n  %s",
			allowListNames(), strings.Join(violations, "\n  "))
	}
}

// The scanner must catch every write form on the guarded types, and must not
// flag same-named fields on other types. testdata/metafixture holds both.
func TestCredentialWriterScanner_Self(t *testing.T) {
	if testing.Short() {
		t.Skip("type-checks the module")
	}
	root := findModuleRoot(t)
	m := newModuleLoader(root)
	guarded := m.guardedObjects(t)
	lp, err := m.load(modulePath + "/internal/scheduler/testdata/metafixture")
	if err != nil {
		t.Fatalf("load fixture: %v", err)
	}
	got := m.violationsIn(lp, guarded, nil)
	wantKinds := []string{
		"assignment of RuntimeHints.InjectBVBRCToken",
		"assignment of RuntimeHints.TokenBlessingID",
		"assignment of StagerOverrides.HTTPCredential",
		"composite literal of RuntimeHints.InjectBVBRCToken",
		"composite literal of StagerOverrides.HTTPCredential",
		"address-of of RuntimeHints.InjectBVBRCToken",
	}
	for _, w := range wantKinds {
		found := false
		for _, g := range got {
			if strings.Contains(g, w) {
				found = true
			}
		}
		if !found {
			t.Errorf("scanner missed %q; got:\n  %s", w, strings.Join(got, "\n  "))
		}
	}
	for _, g := range got {
		if strings.Contains(g, "lookalike") {
			t.Errorf("false positive on a same-named field of another type: %s", g)
		}
	}
	if len(got) != len(wantKinds) {
		t.Errorf("expected exactly %d violations, got %d:\n  %s", len(wantKinds), len(got), strings.Join(got, "\n  "))
	}
}

func allowListNames() []string {
	var ks []string
	for k := range credentialWriterAllowList {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
