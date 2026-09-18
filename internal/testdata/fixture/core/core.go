// Package core provides the key-value store used across the fixture.
package core

// Store is an in-memory key-value store.
type Store struct {
	m map[string]string
}

// NewStore creates an empty store.
func NewStore() *Store {
	return &Store{m: map[string]string{}}
}

// Get returns a value by key.
func (s *Store) Get(k string) string {
	return s.m[k]
}

// Set stores a value.
func (s *Store) Set(k, v string) {
	s.m[k] = v
}
