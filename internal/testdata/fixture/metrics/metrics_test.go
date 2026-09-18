package metrics

import (
	"testing"

	"example.com/fixture/api"
)

func TestFormat(t *testing.T) {
	if Format(3) != "3" {
		t.Fatal("format")
	}
}

func TestFormatLookup(t *testing.T) {
	if FormatLookup(api.NewDefault(), "k") != "0" {
		t.Fatal("lookup")
	}
}
