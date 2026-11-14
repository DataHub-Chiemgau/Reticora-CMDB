package fakesnmp

import (
	"reflect"
	"strings"
	"testing"

	"github.com/gosnmp/gosnmp"
)

// TestLoadWalkAllTypes covers WP-992 AK1 (PRT-21): every line of
// testdata/all-types.walk with its type and value.
func TestLoadWalkAllTypes(t *testing.T) {
	got, err := LoadWalkFile("testdata/all-types.walk")
	if err != nil {
		t.Fatal(err)
	}
	want := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gosnmp.OctetString, Value: []byte("Reticora all-types fixture")},
		{Name: ".1.3.6.1.2.1.1.2.0", Type: gosnmp.ObjectIdentifier, Value: ".1.3.6.1.4.1.8072.3.2.10"},
		{Name: ".1.3.6.1.2.1.1.3.0", Type: gosnmp.TimeTicks, Value: uint32(12345)},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gosnmp.OctetString, Value: []byte("fixture-01")},
		{Name: ".1.3.6.1.2.1.2.2.1.6.1", Type: gosnmp.OctetString, Value: []byte{0x00, 0x1a, 0x2b, 0x3c, 0x4d, 0x5e}},
		{Name: ".1.3.6.1.2.1.2.2.1.7.1", Type: gosnmp.Integer, Value: 1},
		{Name: ".1.3.6.1.2.1.2.2.1.8.1", Type: gosnmp.Integer, Value: -2},
		{Name: ".1.3.6.1.2.1.2.2.1.10.1", Type: gosnmp.Counter32, Value: uint32(4294967290)},
		{Name: ".1.3.6.1.2.1.2.2.1.18.1", Type: gosnmp.OctetString, Value: []byte{}},
		{Name: ".1.3.6.1.2.1.4.20.1.1.192.0.2.1", Type: gosnmp.IPAddress, Value: "192.0.2.1"},
		{Name: ".1.3.6.1.2.1.31.1.1.1.6.1", Type: gosnmp.Counter64, Value: uint64(18446744073709551610)},
		{Name: ".1.3.6.1.2.1.31.1.1.1.15.1", Type: gosnmp.Gauge32, Value: uint32(1000)},
		{Name: ".1.3.6.1.4.1.2021.10.1.6.1", Type: gosnmp.OpaqueFloat, Value: float32(1.5)},
		{Name: ".1.3.6.1.4.1.2021.10.1.6.2", Type: gosnmp.OpaqueDouble, Value: 2.25},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d variables, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Type != want[i].Type || !reflect.DeepEqual(got[i].Value, want[i].Value) {
			t.Errorf("line %d: got %s %v %#v, want %s %v %#v", i, got[i].Name, got[i].Type, got[i].Value,
				want[i].Name, want[i].Type, want[i].Value)
		}
	}
	// The agent serves every type: each variable encodes in a response.
	pkt := &gosnmp.SnmpPacket{Version: gosnmp.Version2c, Community: "c", PDUType: gosnmp.GetResponse,
		Variables: got, Logger: gosnmp.NewLogger(nil)}
	if _, err := pkt.MarshalMsg(); err != nil {
		t.Fatalf("the variables of the walk do not encode: %v", err)
	}
}

// TestLoadWalkRejectsBadLine: a malformed line or an unknown type names its
// line number.
func TestLoadWalkRejectsBadLine(t *testing.T) {
	for _, tc := range []struct{ walk, line string }{
		{".1.3.6.1.2.1.1.5.0 = STRING: ok\nno walk line here\n", "line 2"},
		{"# comment\n\n.1.3.6.1.2.1.1.5.0 = Bits: 80\n", "line 3"},
		{".1.3.6.1.2.1.2.2.1.10.1 = Counter32: -1\n", "line 1"},
		{".1.3.6.1.2.1.2.2.1.10.1 = Counter32: 4294967296\n", "line 1"},
		{".1.3.6.1.2.1.1.3.0 = Timeticks: (abc) 0:00:00.00\n", "line 1"},
		{".1.3.6.1.2.1.2.2.1.6.1 = Hex-STRING: 0G\n", "line 1"},
		{".1.3.6.1.2.1.4.20.1.1.1 = IpAddress: 2001:db8::1\n", "line 1"},
		{".1.3.6.1.2.1.1.2.0 = OID: iso.3.6\n", "line 1"},
		{".1.3.6.1.4.1.2021.10.1.6.1 = Opaque: Int: 5\n", "line 1"},
		{"1.3.x.1 = INTEGER: 1\n", "line 1"},
		{".1.3.6.1.2.1.1.5.0 = no type\n", "line 1"},
		// A string that never closes names its first line.
		{"# c\n.1.3.6.1.2.1.1.1.0 = STRING: \"open\nstill open\n", "line 2"},
		// Hex pairs continue a Hex-STRING only.
		{".1.3.6.1.2.1.1.5.0 = STRING: ok\n00 1A 2B\n", "line 2"},
		{".1.3.6.1.2.1.2.2.1.6.1 = Hex-STRING: 00 1A\n00 1A 2BX\n", "line 2"},
	} {
		_, err := LoadWalk(strings.NewReader(tc.walk))
		if err == nil || !strings.Contains(err.Error(), tc.line+":") {
			t.Errorf("%q: error %v, want one naming %s", tc.walk, err, tc.line)
		}
	}
	if _, err := LoadWalkFile("testdata/missing.walk"); err == nil {
		t.Error("a missing file loaded without error")
	}
}

// TestLoadWalkSkipsComments: empty lines and # comments are no variables.
func TestLoadWalkSkipsComments(t *testing.T) {
	got, err := LoadWalk(strings.NewReader("# header\n\n   \n  # indented comment\n.1.3.6.1.2.1.1.5.0 = STRING: \"a # b\"\n# trailer\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0].Value.([]byte)) != "a # b" {
		t.Fatalf("got %v, want the one variable with its # in the value", got)
	}
}

// TestLoadWalkMultiLineValues: values net-snmp spreads over several lines,
// a Hex-STRING longer than 16 bytes and a STRING with embedded line breaks
// (a Cisco sysDescr), load as one variable each.
func TestLoadWalkMultiLineValues(t *testing.T) {
	walk := ".1.3.6.1.2.1.1.1.0 = STRING: \"Cisco IOS Software, C2960 Software\r\n" +
		"Technical Support: http://www.cisco.com/techsupport\r\n" +
		"Copyright (c) 1986-2016 by \\\"Cisco\\\" Systems\"\r\n" +
		".1.3.6.1.4.1.9.9.23.1.2.1.1.6.1 = Hex-STRING: 00 01 02 03 04 05 06 07 08 09 0A 0B 0C 0D 0E 0F \n" +
		"10 11 12 13 \n" +
		".1.3.6.1.2.1.1.5.0 = STRING: \"line one\n\n\"\n" +
		".1.3.6.1.2.1.1.6.0 = STRING: \"\"\n"
	got, err := LoadWalk(strings.NewReader(walk))
	if err != nil {
		t.Fatal(err)
	}
	hex20 := make([]byte, 20)
	for i := range hex20 {
		hex20[i] = byte(i)
	}
	want := []gosnmp.SnmpPDU{
		{Name: ".1.3.6.1.2.1.1.1.0", Type: gosnmp.OctetString, Value: []byte("Cisco IOS Software, C2960 Software\n" +
			"Technical Support: http://www.cisco.com/techsupport\nCopyright (c) 1986-2016 by \"Cisco\" Systems")},
		{Name: ".1.3.6.1.4.1.9.9.23.1.2.1.1.6.1", Type: gosnmp.OctetString, Value: hex20},
		{Name: ".1.3.6.1.2.1.1.5.0", Type: gosnmp.OctetString, Value: []byte("line one\n\n")},
		{Name: ".1.3.6.1.2.1.1.6.0", Type: gosnmp.OctetString, Value: []byte{}},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d variables, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Name != want[i].Name || got[i].Type != want[i].Type || !reflect.DeepEqual(got[i].Value, want[i].Value) {
			t.Errorf("variable %d: got %s %v %q, want %s %v %q", i, got[i].Name, got[i].Type, got[i].Value,
				want[i].Name, want[i].Type, want[i].Value)
		}
	}
}
