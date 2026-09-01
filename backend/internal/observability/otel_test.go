package observability

import (
	"context"
	"testing"
)

func TestInitNoEndpointIsNoop(t *testing.T) {
	shutdown, err := Init("", "svc", "test")
	if err != nil {
		t.Fatal(err)
	}
	if shutdown == nil {
		t.Fatal("expected a shutdown func")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestInitWithEndpointSetsProviders(t *testing.T) {
	shutdown, err := Init("localhost:4318", "svc", "test")
	if err != nil {
		t.Fatal(err)
	}
	if shutdown == nil {
		t.Fatal("expected a shutdown func")
	}
	// A tracer from the global provider must be usable (non-panicking) once a
	// real provider is installed.
	if Tracer("test") == nil {
		t.Fatal("expected a tracer")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	// Reset to no-op so other tests are unaffected.
	_, _ = Init("", "svc", "test")
}
