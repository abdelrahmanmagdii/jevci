// Package golang wraps `go list` and Go source analysis for jevci.
package golang

import (
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/abdelrahmanmagdii/jevci/internal/gitdiff"
)

// Package mirrors the subset of `go list -json` jevci needs.
type Package struct {
	ImportPath                         string
	Dir                                string
	Name                               string
	GoFiles, TestGoFiles, XTestGoFiles []string
	Imports, TestImports, XTestImports []string
	Module                             string
	Error                              string
}

type listPackage struct {
	ImportPath, Dir, Name              string
	GoFiles, TestGoFiles, XTestGoFiles []string
	Imports, TestImports, XTestImports []string
	Module                             *struct{ Path string }
	Error                              *struct{ Err string }
}

// HasTests reports whether the package contains any test files.
func (p *Package) HasTests() bool {
	return len(p.TestGoFiles) > 0 || len(p.XTestGoFiles) > 0
}

// Workspace is the result of `go list ./...` for a module.
type Workspace struct {
	Root          string
	ModulePath    string
	Packages      map[string]*Package
	NestedModules []string // repo-relative dirs containing their own go.mod
	byDir         map[string]*Package
}

// List runs `go list -e -json ... ./...` in dir.
func List(ctx context.Context, dir string) (*Workspace, error) {
	const fields = "ImportPath,Dir,Name,GoFiles,TestGoFiles,XTestGoFiles,Imports,TestImports,XTestImports,Module,Error"
	cmd := exec.CommandContext(ctx, "go", "list", "-e", "-json="+fields, "./...")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list: %s", strings.TrimSpace(string(ee.Stderr)))
		}
		return nil, fmt.Errorf("go list: %w", err)
	}
	ws := &Workspace{Packages: map[string]*Package{}, byDir: map[string]*Package{}}
	dec := json.NewDecoder(strings.NewReader(string(out)))
	for dec.More() {
		var lp listPackage
		if err := dec.Decode(&lp); err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		p := &Package{
			ImportPath: lp.ImportPath, Dir: lp.Dir, Name: lp.Name,
			GoFiles: lp.GoFiles, TestGoFiles: lp.TestGoFiles, XTestGoFiles: lp.XTestGoFiles,
			Imports: lp.Imports, TestImports: lp.TestImports, XTestImports: lp.XTestImports,
		}
		if lp.Module != nil {
			p.Module = lp.Module.Path
		}
		if lp.Error != nil {
			p.Error = lp.Error.Err
		}
		ws.Packages[p.ImportPath] = p
		if abs, err := filepath.Abs(p.Dir); err == nil {
			ws.byDir[abs] = p
		}
		if ws.ModulePath == "" && p.Module != "" && strings.HasPrefix(p.ImportPath, p.Module) {
			ws.ModulePath = p.Module
		}
	}
	ws.Root, _ = filepath.Abs(dir)
	if ws.ModulePath == "" {
		for _, p := range ws.Packages {
			if p.Module != "" {
				ws.ModulePath = p.Module
				break
			}
		}
	}
	ws.NestedModules = findNestedModules(ws.Root)
	return ws, nil
}

// findNestedModules returns repo-relative dirs containing a go.mod other
// than the root module. .git, vendor, node_modules and testdata subtrees
// are skipped.
func findNestedModules(root string) []string {
	var out []string
	filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && p != root {
			switch d.Name() {
			case ".git", "vendor", "node_modules", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "go.mod" && filepath.Dir(p) != root {
			rel, err := filepath.Rel(root, filepath.Dir(p))
			if err == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// Rel returns the display path for an import path ("./pkg/foo", "." for root).
func (w *Workspace) Rel(importPath string) string {
	if importPath == w.ModulePath {
		return "."
	}
	return "./" + strings.TrimPrefix(importPath, w.ModulePath+"/")
}

// ImportPath converts a display path back to an import path.
func (w *Workspace) ImportPath(rel string) string {
	if rel == "." {
		return w.ModulePath
	}
	return w.ModulePath + "/" + strings.TrimPrefix(rel, "./")
}

// PackageForFile maps a repo-relative file path to the package containing
// it. A non-Go file whose own directory has no package is attributed to the
// nearest ancestor package directory (up to the repo root); .go files are
// never walked up — a .go file outside any package is unattributed.
func (w *Workspace) PackageForFile(relPath string) (*Package, bool) {
	dir := filepath.Dir(filepath.Join(w.Root, relPath))
	if p, ok := w.byDir[dir]; ok {
		return p, true
	}
	if strings.HasSuffix(relPath, ".go") {
		return nil, false
	}
	for dir != w.Root {
		dir = filepath.Dir(dir)
		if p, ok := w.byDir[dir]; ok {
			return p, true
		}
		if dir == "/" || dir == "." {
			break
		}
	}
	return nil, false
}

// TestFunc is a discovered test-like function.
type TestFunc struct {
	Name, File, Doc string
}

var testPrefixes = []string{"Test", "Benchmark", "Fuzz", "Example"}

// TestFunctions returns the test-like top-level functions of a package.
func TestFunctions(pkg *Package) ([]TestFunc, error) {
	var out []TestFunc
	for _, f := range append(append([]string{}, pkg.TestGoFiles...), pkg.XTestGoFiles...) {
		path := filepath.Join(pkg.Dir, f)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			for _, pre := range testPrefixes {
				if strings.HasPrefix(fn.Name.Name, pre) {
					doc := ""
					if fn.Doc != nil {
						doc = firstLine(fn.Doc.Text(), 160)
					}
					out = append(out, TestFunc{Name: fn.Name.Name, File: f, Doc: doc})
					break
				}
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// PackageDoc returns the first paragraph of the package doc comment.
func PackageDoc(pkg *Package) string {
	for _, f := range pkg.GoFiles {
		path := filepath.Join(pkg.Dir, f)
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.SkipObjectResolution)
		if err != nil || file.Doc == nil {
			continue
		}
		text := file.Doc.Text()
		if i := strings.Index(text, "\n\n"); i >= 0 {
			text = text[:i]
		}
		text = strings.TrimSpace(text)
		if len(text) > 400 {
			text = text[:400]
		}
		if text != "" {
			return text
		}
	}
	return ""
}

func firstLine(s string, max int) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if len(s) > max {
		s = s[:max]
	}
	return s
}

// Side selects which side of a hunk to compare against.
type Side int

const (
	Old Side = iota
	New
)

// ChangedSymbols returns sorted names of top-level declarations in src whose
// line span (including doc comments) overlaps any hunk on the given side.
// A zero-length hunk at line L counts as touching line L.
func ChangedSymbols(src []byte, hunks []gitdiff.Hunk, side Side) []string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "src.go", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	type span struct{ start, end int }
	rangeOf := func(h gitdiff.Hunk) (int, int) {
		if side == Old {
			end := h.OldStart + h.OldLines - 1
			if h.OldLines == 0 {
				end = h.OldStart
			}
			return h.OldStart, end
		}
		end := h.NewStart + h.NewLines - 1
		if h.NewLines == 0 {
			end = h.NewStart
		}
		return h.NewStart, end
	}
	var ranges []span
	for _, h := range hunks {
		s, e := rangeOf(h)
		ranges = append(ranges, span{s, e})
	}
	overlaps := func(start, end int) bool {
		for _, r := range ranges {
			if start <= r.end && end >= r.start {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	var out []string
	add := func(name string, n ast.Node, doc *ast.CommentGroup) {
		if name == "" || seen[name] {
			return
		}
		start := n.Pos()
		if doc != nil {
			start = doc.Pos()
		}
		s := fset.Position(start).Line
		e := fset.Position(n.End()).Line
		if overlaps(s, e) {
			seen[name] = true
			out = append(out, name)
		}
	}
	for _, d := range file.Decls {
		switch decl := d.(type) {
		case *ast.FuncDecl:
			name := decl.Name.Name
			if decl.Recv != nil && len(decl.Recv.List) > 0 {
				name = recvTypeName(decl.Recv.List[0].Type) + "." + name
			}
			add(name, decl, decl.Doc)
		case *ast.GenDecl:
			for _, spec := range decl.Specs {
				switch sp := spec.(type) {
				case *ast.TypeSpec:
					add(sp.Name.Name, sp, decl.Doc)
				case *ast.ValueSpec:
					for _, n := range sp.Names {
						add(n.Name, sp, decl.Doc)
					}
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func recvTypeName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.StarExpr:
		return recvTypeName(e.X)
	case *ast.Ident:
		return e.Name
	case *ast.IndexExpr:
		return recvTypeName(e.X)
	case *ast.IndexListExpr:
		return recvTypeName(e.X)
	}
	return ""
}
