package golang

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/abdelrahmanmagdii/jevci/internal/gitdiff"
)

func TestPackageForFile(t *testing.T) {
	root := t.TempDir()
	mk := func(rel string) *Package {
		return &Package{ImportPath: "m/" + rel, Dir: filepath.Join(root, rel)}
	}
	ws := &Workspace{Root: root, ModulePath: "m", Packages: map[string]*Package{}, byDir: map[string]*Package{}}
	pCompat := mk("test/compat")
	pRoot := &Package{ImportPath: "m", Dir: root}
	ws.byDir[pCompat.Dir] = pCompat

	if p, ok := ws.PackageForFile("test/compat/reference/x.yaml"); !ok || p != pCompat {
		t.Fatalf("yaml under package dir subtree: %v %v", p, ok)
	}
	if _, ok := ws.PackageForFile("charts/x/values.yaml"); ok {
		t.Fatal("charts file should be unattributed without root package")
	}
	ws.byDir[root] = pRoot
	if p, ok := ws.PackageForFile("charts/x/values.yaml"); !ok || p != pRoot {
		t.Fatal("charts file should attribute to root package")
	}
	if _, ok := ws.PackageForFile("test/compat/reference/x.go"); ok {
		t.Fatal(".go file must not walk up to ancestor package")
	}
}

const symbolSrc = `// Package p does things.
package p

// Get reads a value.
func Get() int { return 1 }

// Set writes a value.
func (s *Store) Set(v int) {}

type Store struct{ v int }

var Default = 1

const Name = "x"
`

func TestChangedSymbols(t *testing.T) {
	src := []byte(symbolSrc)
	cases := []struct {
		name  string
		hunks []gitdiff.Hunk
		side  Side
		want  []string
	}{
		{"func body", []gitdiff.Hunk{{OldStart: 5, OldLines: 1, NewStart: 5, NewLines: 1}}, New, []string{"Get"}},
		{"method", []gitdiff.Hunk{{NewStart: 8, NewLines: 1}}, New, []string{"Store.Set"}},
		{"type", []gitdiff.Hunk{{NewStart: 10, NewLines: 1}}, New, []string{"Store"}},
		{"var", []gitdiff.Hunk{{NewStart: 12, NewLines: 1}}, New, []string{"Default"}},
		{"const", []gitdiff.Hunk{{NewStart: 14, NewLines: 1}}, New, []string{"Name"}},
		{"zero-length touches line", []gitdiff.Hunk{{NewStart: 5, NewLines: 0}}, New, []string{"Get"}},
		{"doc counts", []gitdiff.Hunk{{NewStart: 4, NewLines: 1}}, New, []string{"Get"}},
		{"no overlap", []gitdiff.Hunk{{NewStart: 2, NewLines: 1}}, New, nil},
		{"old side", []gitdiff.Hunk{{OldStart: 8, OldLines: 1, NewStart: 100, NewLines: 1}}, Old, []string{"Store.Set"}},
		{"multiple", []gitdiff.Hunk{{NewStart: 5, NewLines: 1}, {NewStart: 14, NewLines: 1}}, New, []string{"Get", "Name"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ChangedSymbols(src, c.hunks, c.side)
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("got %v want %v", got, c.want)
			}
		})
	}
}
