package profiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegisterAndGet(t *testing.T) {
	r := NewRegistry()
	p := Profile{Name: "dell-idrac", Vendor: "Dell", Protocol: "redfish"}
	if err := r.Register(p); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, ok := r.Get("dell-idrac")
	if !ok {
		t.Fatal("Get: profile not found")
	}
	if got.Vendor != "Dell" || got.Protocol != "redfish" {
		t.Errorf("Get returned %+v", got)
	}
}

func TestRegisterRequiresName(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Profile{Vendor: "Dell"}); err == nil {
		t.Error("Register without name should fail")
	}
}

func TestGetMissingProfile(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("nope"); ok {
		t.Error("Get should return false for missing profile")
	}
}

func TestRegisterReplacesExisting(t *testing.T) {
	r := NewRegistry()
	r.Register(Profile{Name: "x", Vendor: "A"})
	r.Register(Profile{Name: "x", Vendor: "B"})
	got, _ := r.Get("x")
	if got.Vendor != "B" {
		t.Errorf("Vendor = %q, want replaced value B", got.Vendor)
	}
}

func TestListDeterministicOrder(t *testing.T) {
	r := NewRegistry()
	for _, name := range []string{"zeta", "alpha", "mid"} {
		if err := r.Register(Profile{Name: name}); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	list := r.List()
	if len(list) != 3 {
		t.Fatalf("List returned %d profiles, want 3", len(list))
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, p := range list {
		if p.Name != want[i] {
			t.Errorf("List()[%d].Name = %q, want %q", i, p.Name, want[i])
		}
	}
}

func TestLoadJSONArray(t *testing.T) {
	r := NewRegistry()
	payload := `[
		{"name": "a", "vendor": "VendorA"},
		{"name": "b", "snmpOids": {"sysDescr": "1.3.6.1.2.1.1.1.0"}}
	]`
	if err := r.LoadJSON(strings.NewReader(payload)); err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}
	if _, ok := r.Get("a"); !ok {
		t.Error("profile a not registered")
	}
	b, ok := r.Get("b")
	if !ok || b.SNMPOIDs["sysDescr"] == "" {
		t.Errorf("profile b missing or incomplete: %+v", b)
	}
}

func TestLoadJSONNamedMapAssignsName(t *testing.T) {
	r := NewRegistry()
	payload := `{"hpe-ilo": {"vendor": "HPE", "protocol": "redfish"}}`
	if err := r.LoadJSON(strings.NewReader(payload)); err != nil {
		t.Fatalf("LoadJSON: %v", err)
	}
	p, ok := r.Get("hpe-ilo")
	if !ok {
		t.Fatal("named-map profile not registered")
	}
	if p.Name != "hpe-ilo" {
		t.Errorf("Name = %q, want hpe-ilo assigned from map key", p.Name)
	}
}

func TestLoadJSONInvalid(t *testing.T) {
	r := NewRegistry()
	if err := r.LoadJSON(strings.NewReader(`{not json`)); err == nil {
		t.Error("LoadJSON should fail on malformed JSON")
	}
}

func TestBySysObjectIDLongestPrefixWins(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Profile{Name: "vendor", Vendor: "Cisco", SysObjectIDs: []string{"1.3.6.1.4.1.9"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.Register(Profile{Name: "model", Vendor: "Cisco", Model: "Catalyst", SysObjectIDs: []string{"1.3.6.1.4.1.9.1"}}); err != nil {
		t.Fatal(err)
	}

	got, ok := r.BySysObjectID("1.3.6.1.4.1.9.1.275")
	if !ok || got.Name != "model" {
		t.Fatalf("BySysObjectID = %q, %v; want model, true", got.Name, ok)
	}

	got, ok = r.BySysObjectID("1.3.6.1.4.1.9.5.42")
	if !ok || got.Name != "vendor" {
		t.Fatalf("BySysObjectID vendor fallback = %q, %v; want vendor, true", got.Name, ok)
	}

	// Dot-boundary: 1.3.6.1.4.1.9.10 must not match the model prefix
	// 1.3.6.1.4.1.9.1 (string prefix), only the broader vendor prefix.
	got, ok = r.BySysObjectID("1.3.6.1.4.1.9.10")
	if !ok || got.Name != "vendor" {
		t.Fatalf("1.3.6.1.4.1.9.10 = %q, %v; want vendor via broader prefix", got.Name, ok)
	}

	if _, ok := r.BySysObjectID(""); ok {
		t.Error("empty sysObjectID must not match")
	}
	if _, ok := r.BySysObjectID("1.3.6.1.4.1.99999.1"); ok {
		t.Error("unknown enterprise OID must not match")
	}
}

func TestVendorByMAC(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(Profile{Name: "mikrotik", Vendor: "MikroTik", OUIPrefixes: []string{"48:8F:5A"}}); err != nil {
		t.Fatal(err)
	}

	for _, mac := range []string{"48:8F:5A:01:02:03", "48-8F-5A-01-02-03", "488f5a010203", "48:8f:5a:AA:BB:CC"} {
		vendor, ok := r.VendorByMAC(mac)
		if !ok || vendor != "MikroTik" {
			t.Errorf("VendorByMAC(%q) = %q, %v; want MikroTik, true", mac, vendor, ok)
		}
	}

	if _, ok := r.VendorByMAC("00:11:22:33:44:55"); ok {
		t.Error("unknown OUI must not resolve")
	}
	if _, ok := r.VendorByMAC("not-a-mac"); ok {
		t.Error("garbage MAC must not resolve")
	}
	if _, ok := r.VendorByMAC(""); ok {
		t.Error("empty MAC must not resolve")
	}
}

func TestLoadEmbeddedRegistersShippedProfiles(t *testing.T) {
	r := NewRegistry()
	if err := r.LoadEmbedded(); err != nil {
		t.Fatalf("LoadEmbedded: %v", err)
	}
	list := r.List()
	if len(list) < 10 {
		t.Fatalf("LoadEmbedded registered %d profiles, want at least 10", len(list))
	}
	// Spot-check the sysObjectID classification path against shipped data.
	if _, ok := r.BySysObjectID("1.3.6.1.4.1.14988.1"); !ok {
		t.Error("MikroTik enterprise sysObjectID did not resolve")
	}
	if _, ok := r.BySysObjectID("1.3.6.1.4.1.9.1.1208"); !ok {
		t.Error("Cisco model sysObjectID did not resolve")
	}
	if vendor, ok := r.VendorByMAC("48:8F:5A:10:20:30"); !ok || vendor != "MikroTik" {
		t.Errorf("VendorByMAC MikroTik OUI = %q, %v", vendor, ok)
	}
}

// TestEmbeddedProfileData validates every JSON profile shipped in data/ so
// broken profile files fail in CI instead of at runtime.
func TestEmbeddedProfileData(t *testing.T) {
	entries, err := filepath.Glob(filepath.Join("data", "*.json"))
	if err != nil || len(entries) == 0 {
		t.Fatalf("no profile data files found: %v", err)
	}
	for _, path := range entries {
		t.Run(filepath.Base(path), func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			defer f.Close()

			r := NewRegistry()
			if err := r.LoadJSON(f); err != nil {
				t.Fatalf("LoadJSON(%s): %v", path, err)
			}
			if len(r.List()) == 0 {
				t.Errorf("%s registered zero profiles", path)
			}
			for _, p := range r.List() {
				if p.Name == "" {
					t.Errorf("%s contains a profile without a name", path)
				}
			}
		})
	}
}
