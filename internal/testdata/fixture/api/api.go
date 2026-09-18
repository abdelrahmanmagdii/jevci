// Package api exposes core over a simple handler abstraction.
package api

import "example.com/fixture/core"

// Handler resolves keys via a Store.
type Handler struct {
	s *core.Store
}

func New(s *core.Store) *Handler { return &Handler{s: s} }

// NewDefault returns a Handler backed by an empty store.
func NewDefault() *Handler { return New(core.NewStore()) }

func (h *Handler) Lookup(k string) string { return h.s.Get(k) }
