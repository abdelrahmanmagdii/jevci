package golang

import (
	"reflect"
	"testing"

	"github.com/abdelrahmanmagdii/jevci/internal/gitdiff"
)

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
