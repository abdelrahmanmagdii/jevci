package core

import "testing"

func TestStoreSetGet(t *testing.T) {
	s := NewStore()
	s.Set("a", "1")
	if s.Get("a") != "1" {
		t.Fatal("get")
	}
}
