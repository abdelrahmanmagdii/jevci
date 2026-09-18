// Package web renders api lookups as pages.
package web

import "example.com/fixture/api"

func Page(h *api.Handler, k string) string {
	return "value: " + h.Lookup(k)
}
