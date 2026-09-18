package web

import (
	"testing"

	"example.com/fixture/api"
)

func TestPage(t *testing.T) {
	got := Page(api.NewDefault(), "k")
	if got != "value: " {
		t.Fatal(got)
	}
}
