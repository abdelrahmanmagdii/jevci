package dependency

import (
	"testing"

	"github.com/abdelrahmanmagdii/jevci/internal/golang"
)

func ws(pkgs map[string][]string, xtests map[string][]string) *golang.Workspace {
	w := &golang.Workspace{ModulePath: "m", Packages: map[string]*golang.Package{}}
	for name, imports := range pkgs {
		w.Packages["m/"+name] = &golang.Package{ImportPath: "m/" + name, Imports: imports}
	}
	for name, imports := range xtests {
		w.Packages["m/"+name].XTestImports = imports
	}
	return w
}

func TestDependentsDiamond(t *testing.T) {
	// core <- a, core <- b, a & b <- top
	g := New(ws(map[string][]string{
		"core": nil,
		"a":    {"m/core"},
		"b":    {"m/core"},
		"top":  {"m/a", "m/b"},
	}, nil))
	im := g.Dependents([]string{"m/core"})
	if im["m/a"].Distance != 1 || im["m/b"].Distance != 1 {
		t.Fatalf("direct dependents wrong: %+v", im)
	}
	top := im["m/top"]
	if top.Distance != 2 || top.Root != "m/core" {
		t.Fatalf("top: %+v", top)
	}
	if top.Via != "m/a" {
		t.Fatalf("via = %q want m/a", top.Via)
	}
	path := g.Path("m/top")
	if len(path) != 2 || path[0] != "m/core" || path[1] != "m/a" {
		t.Fatalf("path = %v", path)
	}
}

func TestDependentsCycle(t *testing.T) {
	g := New(ws(map[string][]string{
		"a": {"m/b"},
		"b": {"m/a"},
	}, nil))
	im := g.Dependents([]string{"m/a"})
	if im["m/b"].Distance != 1 || im["m/a"].Distance != 0 {
		t.Fatalf("%+v", im)
	}
}

func TestXTestImportIsEdge(t *testing.T) {
	g := New(ws(map[string][]string{
		"core": nil,
		"x":    nil,
	}, map[string][]string{"x": {"m/core"}}))
	im := g.Dependents([]string{"m/core"})
	if im["m/x"].Distance != 1 {
		t.Fatalf("xtest import should create edge: %+v", im)
	}
}

func TestSelfImportIgnored(t *testing.T) {
	g := New(ws(map[string][]string{
		"a": {"m/a"},
	}, nil))
	if im := g.Dependents([]string{"m/a"}); len(im) != 1 {
		t.Fatalf("%+v", im)
	}
}
