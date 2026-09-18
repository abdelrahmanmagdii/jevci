// Package metrics formats numbers for display. It does not read or write
// application data; it only converts numbers to strings.
package metrics

import (
	"strconv"

	"example.com/fixture/api"
)

// Format renders an integer.
func Format(n int) string { return strconv.Itoa(n) }

// FormatLookup renders the length of an api lookup result.
func FormatLookup(h *api.Handler, k string) string {
	return Format(len(h.Lookup(k)))
}
