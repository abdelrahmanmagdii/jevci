package api

import (
	"testing"

	"example.com/fixture/core"
)

func TestLookup(t *testing.T) {
	h := New(core.NewStore())
	if h.Lookup("x") != "" {
		t.Fatal("want empty")
	}
}
