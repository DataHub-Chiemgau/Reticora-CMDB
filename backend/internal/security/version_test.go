package security

import (
	"errors"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// The version comparison of the version match (SEC-02, WP-1121, E-236):
// generic dotted versions and the schemes of Cisco IOS, IOS-XE, NX-OS and
// Juniper Junos.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestCompare|TestInRange|TestUnparseable' ./internal/security/

func wantOrder(t *testing.T, scheme string, ordered ...string) {
	t.Helper()
	for i := 0; i+1 < len(ordered); i++ {
		c, err := CompareVersions(scheme, ordered[i], ordered[i+1])
		if err != nil || c != -1 {
			t.Errorf("%s: %s vs %s = %d (%v), want -1", scheme, ordered[i], ordered[i+1], c, err)
		}
		if r, _ := CompareVersions(scheme, ordered[i+1], ordered[i]); r != 1 {
			t.Errorf("%s: %s vs %s = %d, want 1", scheme, ordered[i+1], ordered[i], r)
		}
	}
}

func wantEqual(t *testing.T, scheme, a, b string) {
	t.Helper()
	if c, err := CompareVersions(scheme, a, b); err != nil || c != 0 {
		t.Errorf("%s: %s vs %s = %d (%v), want 0", scheme, a, b, c, err)
	}
}

func TestCompareGeneric(t *testing.T) {
	wantOrder(t, SchemeGeneric, "1.2.9", "1.2.10")
	wantEqual(t, SchemeGeneric, "1.2", "1.2.0")
	wantOrder(t, SchemeGeneric, "2.0beta2", "2.0rc1", "2.0", "2.0.1")
	wantOrder(t, SchemeGeneric, "7.1", "7.1.5", "7.12")
}

func TestCompareCiscoIOSXE(t *testing.T) {
	wantOrder(t, SchemeCiscoIOSXE, "17.9.4", "17.9.4a", "17.9.5")
	wantOrder(t, SchemeCiscoIOSXE, "16.12.9", "17.3.1")
}

func TestCompareCiscoIOS(t *testing.T) {
	wantOrder(t, SchemeCiscoIOS, "15.2(4)M7", "15.2(4)M10")
	wantOrder(t, SchemeCiscoIOS, "15.2(4)M10", "15.2(5)M1")
}

func TestCompareCiscoNXOS(t *testing.T) {
	wantOrder(t, SchemeCiscoNXOS, "9.3(9)", "9.3(10)")
	wantOrder(t, SchemeCiscoNXOS, "9.3(10)", "10.2(1)")
}

func TestCompareJunos(t *testing.T) {
	wantOrder(t, SchemeJunos, "22.4R3", "22.4R3-S1", "22.4R3-S2")
	wantOrder(t, SchemeJunos, "22.4R3-S2", "23.2R1")
}

func TestInRangeInclusivity(t *testing.T) {
	r := &vulnfeed.Affected{VersionStart: "17.9.1", VersionStartInclusive: true, VersionEnd: "17.9.5"}
	for v, want := range map[string]bool{"17.9.1": true, "17.9.4a": true, "17.9.5": false, "17.9.0": false, "17.10.1": false} {
		if got, err := InRange(SchemeCiscoIOSXE, v, r); err != nil || got != want {
			t.Errorf("%s in [17.9.1, 17.9.5): %v (%v), want %v", v, got, err, want)
		}
	}
	r = &vulnfeed.Affected{VersionStart: "17.9.1", VersionEnd: "17.9.5", VersionEndInclusive: true}
	for v, want := range map[string]bool{"17.9.1": false, "17.9.5": true} {
		if got, _ := InRange(SchemeCiscoIOSXE, v, r); got != want {
			t.Errorf("%s in (17.9.1, 17.9.5]: %v, want %v", v, got, want)
		}
	}
	if got, _ := InRange(SchemeGeneric, "7.1", &vulnfeed.Affected{VersionExact: "7.1.0"}); !got {
		t.Error("7.1 is the exact version 7.1.0")
	}
	if got, _ := InRange(SchemeGeneric, "9.9", &vulnfeed.Affected{VersionEnd: "10.0"}); !got {
		t.Error("9.9 below the open end 10.0")
	}
	// No exact version and no bound: every version is affected (a CSAF
	// product without a version, vers:x/*, a CPE version *).
	if got, err := InRange(SchemeCiscoIOSXE, "17.9.4a", &vulnfeed.Affected{}); err != nil || !got {
		t.Errorf("17.9.4a in a range without bounds: %v %v, want affected", got, err)
	}
	// The exact Junos version of an NVD CPE with its update field
	// (vulnfeed.parseCPE: 22.4 + r3-s1).
	if got, err := InRange(SchemeJunos, "22.4R3-S1", &vulnfeed.Affected{VersionExact: "22.4R3-S1"}); err != nil || !got {
		t.Errorf("22.4R3-S1 is the exact NVD version 22.4R3-S1: %v %v", got, err)
	}
	if _, err := InRange(SchemeJunos, "22.4", &vulnfeed.Affected{}); !errors.Is(err, ErrUnparseableVersion) {
		t.Errorf("unreadable version in a range without bounds: %v, want ErrUnparseableVersion", err)
	}
}

// TestNexusMapsToNXOS: the model names of the Nexus 9000 contain the
// Catalyst pattern c9nnn; they map to NX-OS, a Catalyst to IOS-XE.
func TestNexusMapsToNXOS(t *testing.T) {
	for model, want := range map[string]string{"N9K-C93180YC-EX": SchemeCiscoNXOS, "N9K-C9336C-FX2": SchemeCiscoNXOS, "N9K-C9504": SchemeCiscoNXOS,
		"Nexus 3048": SchemeCiscoNXOS, "C9300-48P": SchemeCiscoIOSXE, "ISR4331": SchemeCiscoIOSXE} {
		m := ResolveMapping(nil, SystemMappings(), &CIProduct{Manufacturer: "Cisco", Model: model})
		if m == nil || m.VersionScheme != want || (want == SchemeCiscoNXOS && !slices.Contains(m.Products, "nx-os")) {
			t.Errorf("%s: %+v, want scheme %s", model, m, want)
		}
	}
}

func TestUnparseableVersion(t *testing.T) {
	for scheme, v := range map[string]string{SchemeGeneric: "latest", SchemeCiscoIOS: "15.2.4", SchemeCiscoNXOS: "abc",
		SchemeJunos: "22.4.3", SchemeCiscoIOSXE: ""} {
		if _, err := CompareVersions(scheme, v, "1.0(1)R1"); !errors.Is(err, ErrUnparseableVersion) {
			t.Errorf("%s %q: %v, want ErrUnparseableVersion", scheme, v, err)
		}
		if ok, err := InRange(scheme, v, &vulnfeed.Affected{VersionEnd: "99"}); ok || !errors.Is(err, ErrUnparseableVersion) {
			t.Errorf("%s %q in range: %v %v, want ErrUnparseableVersion", scheme, v, ok, err)
		}
	}
}
