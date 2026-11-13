package profiles

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The profile section monitor (PRT-20, WP-1000): parsing, validation on
// load (also without ciType), the selection per device and the override
// from RETICORA_PROFILES_DIR.

const monitorProfiles = `
- name: test-bgp-v2
  vendor: Test
  ciType: router
  sysObjectIds: ["1.3.6.1.4.1.99999"]
  monitor:
    bgp:
      tables:
        peers:
          index: inet
          columns:
            state: 1.3.6.1.4.1.99999.5.1.2.1.13
            remote_as: .1.3.6.1.4.1.99999.5.1.2.1.10
            prefixes: 1.3.6.1.4.1.99999.5.1.6.1.3
          enums:
            state:
              "1": down
              "6": up
              "3": active
        optics:
          index: composite:[int, int]
          scale: 0.01
          unit: dBm
          columns:
            rx: 1.3.6.1.4.1.99999.7.1.1
            rx_low: 1.3.6.1.4.1.99999.7.1.2
          threshold_columns:
            rx_power_low_alarm: rx_low
- name: test-bgp-model
  vendor: Test
  ciType: router
  sysObjectIds: ["1.3.6.1.4.1.99999.1.42"]
  monitor:
    bgp:
      tables:
        peers:
          index: ipv4
          columns:
            state: 1.3.6.1.4.1.99999.42.1
- name: test-collection-only
  sysObjectIds: ["1.3.6.1.4.1.88888"]
  monitor:
    optical:
      tables:
        lanes:
          index: int
          columns:
            rx: 1.3.6.1.4.1.88888.1.1
`

func loadMonitor(t *testing.T, doc string) (*Registry, error) {
	t.Helper()
	r := NewRegistry()
	return r, r.LoadYAML(strings.NewReader(doc))
}

func TestMonitorParse(t *testing.T) {
	r, err := loadMonitor(t, monitorProfiles)
	if err != nil {
		t.Fatalf("LoadYAML: %v", err)
	}
	p, _ := r.Get("test-bgp-v2")
	peers := p.Monitor["bgp"].Tables["peers"]
	if peers.Index != IndexInet || len(peers.Columns) != 3 || peers.Enums["state"]["6"] != "up" {
		t.Fatalf("peers table %+v", peers)
	}
	if oid, ok := peers.ColumnOID("remote_as"); !ok || oid != "1.3.6.1.4.1.99999.5.1.2.1.10" {
		t.Errorf("ColumnOID(remote_as) = %q, %v", oid, ok)
	}
	if state, ok := peers.MapEnum("state", 6); !ok || state != "up" {
		t.Errorf("MapEnum(state, 6) = %q, %v", state, ok)
	}
	if _, ok := peers.MapEnum("state", 2); ok {
		t.Error("MapEnum of an unmapped value reports ok")
	}
	optics := p.Monitor["bgp"].Tables["optics"]
	if kinds, err := ParseIndex(optics.Index); err != nil || len(kinds) != 2 {
		t.Errorf("composite index %q: %v %v", optics.Index, kinds, err)
	}
	if optics.Factor() != 0.01 || optics.Unit != "dBm" || optics.ThresholdColumns["rx_power_low_alarm"] != "rx_low" {
		t.Errorf("optics table %+v", optics)
	}
	if peers.Factor() != 1 {
		t.Errorf("factor without scale %v, want 1", peers.Factor())
	}
	// A profile without ciType carries a monitor section as well.
	if p, ok := r.Get("test-collection-only"); !ok || len(p.Monitor["optical"].Tables) != 1 {
		t.Errorf("collection-only profile %+v", p)
	}
}

// monitorDoc is a profile whose bgp table is replaced by table.
func monitorDoc(probeType, table string) string {
	return `
- name: broken
  monitor:
    ` + probeType + `:
      tables:
        peers:
` + table
}

func expectRejected(t *testing.T, doc string, want ...string) {
	t.Helper()
	_, err := loadMonitor(t, doc)
	if err == nil {
		t.Fatalf("profile accepted, want an error naming %v", want)
	}
	for _, w := range append([]string{"broken"}, want...) {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("error %q does not name %q", err, w)
		}
	}
}

func TestMonitorRejectsUnknownProbeType(t *testing.T) {
	expectRejected(t, monitorDoc("bgpv9", `          index: int
          columns: {state: 1.3.6.1.2.1.15.3.1.2}
`), "monitor.bgpv9", "unknown probe type")
	expectRejected(t, `
- name: broken
  monitor:
    bgp: {tables: {}}
`, "monitor.bgp.tables")
}

func TestMonitorRejectsBadOID(t *testing.T) {
	for _, oid := range []string{"1.3.6.x.1", "ifOperStatus", "1", "1..3"} {
		expectRejected(t, monitorDoc("bgp", `          index: int
          columns: {state: "`+oid+`"}
`), "monitor.bgp.tables.peers.columns.state", "invalid OID")
	}
}

func TestMonitorRejectsBadIndex(t *testing.T) {
	for _, index := range []string{"string", "", "composite:[int]", "composite:[int,mac]", "composite:int,int"} {
		expectRejected(t, monitorDoc("bgp", `          index: "`+index+`"
          columns: {state: 1.3.6.1.2.1.15.3.1.2}
`), "monitor.bgp.tables.peers.index")
	}
	for _, scale := range []string{"-1", "0", ".nan", ".inf", "-.inf"} {
		expectRejected(t, monitorDoc("bgp", `          index: int
          scale: `+scale+`
          columns: {state: 1.3.6.1.2.1.15.3.1.2}
`), "monitor.bgp.tables.peers.scale")
	}
}

func TestMonitorRejectsEnumOnUnknownColumn(t *testing.T) {
	expectRejected(t, monitorDoc("bgp", `          index: int
          columns: {state: 1.3.6.1.2.1.15.3.1.2}
          enums: {admin: {"1": up}}
`), "monitor.bgp.tables.peers.enums.admin", "unknown column")
	expectRejected(t, monitorDoc("bgp", `          index: int
          columns: {state: 1.3.6.1.2.1.15.3.1.2}
          enums: {state: {"1": "up down"}}
`), "monitor.bgp.tables.peers.enums.state.1")
	for _, raw := range []string{"established", "06", "+1", "1.0", "99999999999999999999"} {
		expectRejected(t, monitorDoc("bgp", `          index: int
          columns: {state: 1.3.6.1.2.1.15.3.1.2}
          enums: {state: {"`+raw+`": up}}
`), "monitor.bgp.tables.peers.enums.state."+raw, "raw value must be an integer")
	}
	expectRejected(t, monitorDoc("bgp", `          index: int
          columns: {state: 1.3.6.1.2.1.15.3.1.2}
          threshold_columns: {high: missing}
`), "monitor.bgp.tables.peers.threshold_columns.high")
	// Unknown members of a table are refused by the strict decoder.
	if _, err := loadMonitor(t, monitorDoc("bgp", `          index: int
          columns: {state: 1.3.6.1.2.1.15.3.1.2}
          colums: {}
`)); err == nil {
		t.Error("unknown member of a table accepted")
	}
}

func TestMonitorForBySysObjectID(t *testing.T) {
	r, err := loadMonitor(t, monitorProfiles)
	if err != nil {
		t.Fatal(err)
	}
	// The longest prefix wins: the model profile below the vendor prefix.
	spec, name, ok := r.MonitorFor("bgp", "1.3.6.1.4.1.99999.1.42.7", "")
	if !ok || name != "test-bgp-model" || spec.Tables["peers"].Index != IndexIPv4 {
		t.Errorf("model device: %s %v, want test-bgp-model", name, ok)
	}
	spec, name, ok = r.MonitorFor("bgp", "1.3.6.1.4.1.99999.1.7", "")
	if !ok || name != "test-bgp-v2" || spec.Tables["peers"].Index != IndexInet {
		t.Errorf("other model of the vendor: %s %v, want test-bgp-v2", name, ok)
	}
	// Only profiles with a section of the type count.
	if _, name, ok = r.MonitorFor("optical", "1.3.6.1.4.1.99999.1.42", ""); ok {
		t.Errorf("optical on a profile without optical section: %s", name)
	}
	if _, name, ok = r.MonitorFor("optical", "1.3.6.1.4.1.88888.3", ""); !ok || name != "test-collection-only" {
		t.Errorf("optical of the collection-only profile: %s %v", name, ok)
	}
	// A matching sysDescr pattern beats a longer prefix, as in the
	// classification.
	descr := monitorProfiles + `
- name: test-bgp-os
  ciType: router
  sysObjectIds: ["1.3.6.1.4.1.99999"]
  sysDescrPattern: "TestOS 9"
  monitor:
    bgp:
      tables:
        peers:
          index: ipv6
          columns: {state: 1.3.6.1.4.1.99999.9.1}
`
	if r, err = loadMonitor(t, descr); err != nil {
		t.Fatal(err)
	}
	if _, name, ok = r.MonitorFor("bgp", "1.3.6.1.4.1.99999.1.42.7", "TestOS 9.1 build 4"); !ok || name != "test-bgp-os" {
		t.Errorf("sysDescr match: %s %v, want test-bgp-os", name, ok)
	}
	if _, name, ok = r.MonitorFor("bgp", "1.3.6.1.4.1.99999.1.42.7", "OtherOS"); !ok || name != "test-bgp-model" {
		t.Errorf("sysDescr mismatch: %s %v, want test-bgp-model", name, ok)
	}
}

func TestMonitorForFallbackToCore(t *testing.T) {
	r, err := loadMonitor(t, monitorProfiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ probeType, oid string }{
		{"bgp", "1.3.6.1.4.1.12345.1"},  // no profile of the vendor
		{"bgp", "1.3.6.1.4.1.999990.1"}, // prefix only by string, not by arc
		{"ospf", "1.3.6.1.4.1.99999.1"}, // profile without ospf section
		{"bgp", ""},                     // nothing known about the device
	} {
		if spec, name, ok := r.MonitorFor(c.probeType, c.oid, ""); ok || name != "" || spec.Tables != nil {
			t.Errorf("MonitorFor(%s, %q) = %s, want the core (false)", c.probeType, c.oid, name)
		}
	}
}

func TestMonitorOverrideFromDir(t *testing.T) {
	r := Default()
	shipped, ok := r.Get("cisco-ios-snmp")
	if !ok {
		shipped = r.List()[0]
	}
	dir := t.TempDir()
	if _, has := shipped.Monitor["bgp"]; has {
		t.Fatalf("shipped profile %s has a bgp section already", shipped.Name)
	}
	override := `
- name: ` + shipped.Name + `
  vendor: ` + shipped.Vendor + `
  ciType: ` + shipped.CIType + `
  sysObjectIds: [` + strings.Join(shipped.SysObjectIDs, ", ") + `]
  monitor:
    bgp:
      tables:
        peers:
          index: inet
          columns: {state: 1.3.6.1.4.1.9.9.187.1.2.5.1.3}
          enums: {state: {"6": up}}
`
	if err := os.WriteFile(filepath.Join(dir, "override.yaml"), []byte(override), 0o600); err != nil {
		t.Fatal(err)
	}
	r = NewRegistry()
	if err := r.LoadEmbedded(); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadDir(dir); err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	got, _ := r.Get(shipped.Name)
	if got.Monitor["bgp"].Tables["peers"].Index != IndexInet {
		t.Fatalf("monitor section of %s after LoadDir: %+v", shipped.Name, got.Monitor)
	}
	if len(shipped.SysObjectIDs) > 0 {
		if _, name, ok := r.MonitorFor("bgp", strings.TrimSpace(shipped.SysObjectIDs[0])+".1", ""); !ok || name == "" {
			t.Errorf("MonitorFor after the override: %q %v", name, ok)
		}
	}
	// A later profile with the same name replaces the section as a whole
	// (no merge of the tables).
	second := t.TempDir()
	replaced := strings.Replace(override, `
        peers:
          index: inet
          columns: {state: 1.3.6.1.4.1.9.9.187.1.2.5.1.3}
          enums: {state: {"6": up}}`, `
        sessions:
          index: ipv4
          columns: {state: 1.3.6.1.4.1.9.9.187.1.2.9.1.3}`, 1)
	if replaced == override {
		t.Fatal("override document not rewritten")
	}
	if err := os.WriteFile(filepath.Join(second, "override.yaml"), []byte(replaced), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadDir(second); err != nil {
		t.Fatalf("LoadDir of the second override: %v", err)
	}
	got, _ = r.Get(shipped.Name)
	if tables := got.Monitor["bgp"].Tables; len(tables) != 1 || tables["sessions"].Index != IndexIPv4 {
		t.Errorf("monitor section after the second override: %+v, want only the sessions table", tables)
	}
	// An invalid profile of the directory is refused with its path.
	bad := filepath.Join(dir, "zz-bad.yaml")
	if err := os.WriteFile(bad, []byte(monitorDoc("bgp", "          index: nope\n          columns: {state: 1.3.6.1.2.1.1}\n")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.LoadDir(dir); err == nil || !strings.Contains(err.Error(), "zz-bad.yaml") || !strings.Contains(err.Error(), "monitor.bgp.tables.peers.index") {
		t.Errorf("LoadDir with an invalid monitor section: %v", err)
	}
}
