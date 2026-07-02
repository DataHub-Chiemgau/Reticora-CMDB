package ci

import "testing"

func TestDefaultTypes(t *testing.T) {
	types := DefaultTypes()
	if len(types) == 0 {
		t.Fatal("expected default types")
	}

	names := make(map[string]bool)
	for _, ct := range types {
		if ct.Name == "" {
			t.Error("CI type has empty name")
		}
		if names[ct.Name] {
			t.Errorf("duplicate CI type name: %s", ct.Name)
		}
		names[ct.Name] = true

		if len(ct.Attributes) == 0 {
			t.Errorf("CI type %s has no attributes", ct.Name)
		}
	}

	// Verify expected types exist
	expected := []string{"server", "switch", "router", "firewall", "pdu", "ups", "nas", "client"}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("expected default type %s not found", name)
		}
	}
}
