// Package dependency builds a reverse import graph over a workspace.
package dependency

import (
	"sort"

	"github.com/abdelrahmanmagdii/jevci/internal/golang"
)

// Impact describes how a package depends on a changed package.
type Impact struct {
	Distance int    // 0 = the package itself changed
	Via      string // import path of the neighbour one step closer to the changed package
	Root     string // changed import path reached
}

// Graph is a reverse import graph: edges point from an imported package to
// the packages that import it.
type Graph struct {
	dependents map[string][]string
	impacts    map[string]Impact
}

// New builds the graph from a workspace. Imports, TestImports and
// XTestImports all count as edges; only in-module packages are linked.
func New(ws *golang.Workspace) *Graph {
	g := &Graph{dependents: map[string][]string{}}
	for _, p := range ws.Packages {
		seen := map[string]bool{}
		for _, deps := range [][]string{p.Imports, p.TestImports, p.XTestImports} {
			for _, q := range deps {
				if q == p.ImportPath || seen[q] {
					continue
				}
				if _, ok := ws.Packages[q]; !ok {
					continue
				}
				seen[q] = true
				g.dependents[q] = append(g.dependents[q], p.ImportPath)
			}
		}
	}
	for _, l := range g.dependents {
		sort.Strings(l)
	}
	return g
}

// Dependents runs multi-source BFS from changed and returns the impact for
// every reached package (including the changed ones at distance 0).
func (g *Graph) Dependents(changed []string) map[string]Impact {
	impacts := map[string]Impact{}
	var queue []string
	for _, c := range changed {
		if _, seen := impacts[c]; seen {
			continue
		}
		impacts[c] = Impact{Distance: 0, Root: c}
		queue = append(queue, c)
	}
	sort.Strings(queue)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		ci := impacts[cur]
		for _, dep := range g.dependents[cur] {
			if _, seen := impacts[dep]; seen {
				continue
			}
			impacts[dep] = Impact{Distance: ci.Distance + 1, Via: cur, Root: ci.Root}
			queue = append(queue, dep)
		}
	}
	g.impacts = impacts
	return impacts
}

// Path reconstructs the import chain from a dependent back to the changed
// package by following Via links recorded by the last Dependents call.
// The result starts with the package nearest the changed root and ends
// with the neighbour that imports `from`... ordered root→dependent.
func (g *Graph) Path(from string) []string {
	var rev []string
	cur := from
	for {
		imp, ok := g.impacts[cur]
		if !ok || imp.Distance == 0 {
			break
		}
		rev = append(rev, imp.Via)
		cur = imp.Via
	}
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
