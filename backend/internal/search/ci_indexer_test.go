package search

import "testing"

func TestNewCIIndexer_NilBackendReturnsNil(t *testing.T) {
	if got := NewCIIndexer(nil); got != nil {
		t.Fatalf("expected nil adapter for nil backend, got %#v", got)
	}
}
