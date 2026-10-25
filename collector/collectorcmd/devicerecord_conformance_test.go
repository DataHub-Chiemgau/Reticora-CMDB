package collectorcmd

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gosnmp/gosnmp"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/internal/fakesnmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/snmp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins/sweep"
)

// TestCollectorOutputConformsToDeviceRecordSchema covers WP-083 (DIS-05,
// GLO-12): what the collector uploads in a discovery cycle is a DeviceRecord
// batch, and every record validates against api/devicerecord.schema.json.
// The cycle runs the real sweep plugin against a local listener (by address
// and by host name) and fixture plugins under the SNMP, power and Redfish
// protocol names that return results shaped like those plugins' results.
func TestCollectorOutputConformsToDeviceRecordSchema(t *testing.T) {
	schema := loadDeviceRecordSchema(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		for {
			conn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port

	swap := map[string]plugins.Plugin{
		"sweep": &sweep.Plugin{Timeout: time.Second, Concurrency: 4, TCPPorts: []int{port}},
		"snmp": fixturePlugin{"snmp", plugins.Result{
			CIType: "network.device", Name: "core-sw-1", IP: "127.0.0.1",
			Manufacturer: "Cisco", Model: "C9300", Serial: "FOC1234", Firmware: "17.9.4", MAC: "00:11:22:33:44:55",
			Attributes: map[string]any{"protocol": "snmp", "sysDescr": "Cisco IOS XE", "sysObjectID": "1.3.6.1.4.1.9.1.2494",
				"sysLocation": "R1", "snmpVersion": "v2c", "hostname": "core-sw-1"},
			Interfaces: []plugins.NetworkInterface{
				{Name: "Gi1/0/1", MAC: "00:11:22:33:44:56", Speed: 1000, MTU: 1500, AdminUp: true, OperUp: true, IfIndex: 1, VlanID: 10},
				{Name: "Gi1/0/2", IfIndex: 2},
			},
		}},
		"power": fixturePlugin{"power", plugins.Result{
			CIType: "power-device", Name: "ups-1", IP: "127.0.0.1", Manufacturer: "APC", Model: "Smart-UPS",
			Attributes: map[string]any{"category": "power", "protocol": "snmp", "deviceType": "ups", "upsCapable": true},
		}},
		"redfish": fixturePlugin{"redfish", plugins.Result{
			CIType: "bmc", Name: "127.0.0.1", IP: "127.0.0.1",
			Attributes: map[string]any{"api": "redfish", "redfishVersion": "1.15.0", "bmcUUID": "4c4c4544-0042-3510-8051-b4c04f4a4e33"},
		}},
	}
	before := time.Now().Add(-time.Second)
	records := discoveryUpload(t, swap, func(cfg *collectorConfig) {
		cfg.ScanSubnets = []string{"127.0.0.1/32", "localhost"}
		cfg.Protocols = []string{"sweep", "snmp", "power", "redfish"}
	})
	sources := map[string]int{}
	for i, raw := range records {
		var doc any
		if err = json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		for _, problem := range schema.validate(doc) {
			t.Errorf("record %d: %s\n%s", i, problem, raw)
		}
		var rec plugins.DeviceRecord
		if err = json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		if violations := rec.Validate(); len(violations) > 0 {
			t.Errorf("record %d breaks the Go contract: %v", i, violations)
		}
		if rec.ObservedAt.Before(before) || rec.ObservedAt.After(time.Now()) {
			t.Errorf("record %d observed_at %s is not the time of the cycle", i, rec.ObservedAt)
		}
		sources[rec.Source]++
	}
	// Sweep finds the listener by address and by name; SNMP and power report
	// as snmp; Redfish once.
	if sources["sweep"] != 2 || sources["snmp"] != 2 || sources["redfish"] != 1 || len(records) != 5 {
		t.Errorf("records by source: %v", sources)
	}
}

// TestCollectorUploadsSNMPNeighbors covers WP-416 (DIS-05, TOP-01): the
// neighbours the SNMP plugin reads from the LLDP and bridge tables of a
// switch arrive in the uploaded DeviceRecord as neighbors (protocol, local
// interface, remote MAC, IP, host name and interface) and the record
// validates against api/devicerecord.schema.json. The cycle runs the real
// SNMP plugin against the SNMPv2c fake (internal/fakesnmp).
func TestCollectorUploadsSNMPNeighbors(t *testing.T) {
	schema := loadDeviceRecordSchema(t)
	const community = "conformance-community"
	s := func(oid, v string) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.OctetString, Value: []byte(v)}
	}
	i := func(oid string, v int) gosnmp.SnmpPDU {
		return gosnmp.SnmpPDU{Name: "." + oid, Type: gosnmp.Integer, Value: v}
	}
	agent, err := fakesnmp.Listen("127.0.0.1:0", fakesnmp.Config{Community: community, SysName: "access-sw-1",
		SysDescr: "Reticora SNMP fake", SysObjectID: ".1.3.6.1.4.1.8072.3.2.10"},
		// IF-MIB ifDescr, BRIDGE-MIB dot1dBasePortIfIndex (bridge port 1, 2 = ifIndex 1, 2).
		s("1.3.6.1.2.1.2.2.1.2.1", "GigabitEthernet1/0/1"), s("1.3.6.1.2.1.2.2.1.2.2", "GigabitEthernet1/0/2"),
		i("1.3.6.1.2.1.17.1.4.1.2.1", 1), i("1.3.6.1.2.1.17.1.4.1.2.2", 2),
		// LLDP: dist-sw-2 (chassis MAC, port Te1/1/1, management address 192.0.2.12) on local port 1.
		i("1.0.8802.1.1.2.1.3.7.1.2.1", 5), s("1.0.8802.1.1.2.1.3.7.1.3.1", "GigabitEthernet1/0/1"),
		i("1.0.8802.1.1.2.1.4.1.1.4.0.1.1", 4),
		gosnmp.SnmpPDU{Name: ".1.0.8802.1.1.2.1.4.1.1.5.0.1.1", Type: gosnmp.OctetString, Value: []byte{0x00, 0x1b, 0x54, 0xaa, 0xbb, 0xcc}},
		i("1.0.8802.1.1.2.1.4.1.1.6.0.1.1", 5), s("1.0.8802.1.1.2.1.4.1.1.7.0.1.1", "Te1/1/1"),
		s("1.0.8802.1.1.2.1.4.1.1.9.0.1.1", "dist-sw-2"),
		i("1.0.8802.1.1.2.1.4.2.1.3.0.1.1.1.4.192.0.2.12", 2),
		// FDB: 00:50:56:01:02:03 learned on bridge port 2.
		i("1.3.6.1.2.1.17.4.3.1.2.0.80.86.1.2.3", 2), i("1.3.6.1.2.1.17.4.3.1.3.0.80.86.1.2.3", 3),
	)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan error, 1)
	go func() { served <- agent.Serve() }()
	defer func() {
		_ = agent.Close()
		<-served
	}()

	records := discoveryUpload(t, map[string]plugins.Plugin{"snmp": &snmp.Plugin{Timeout: time.Second, Port: agent.Addr().Port}},
		func(cfg *collectorConfig) {
			cfg.ScanSubnets = []string{"127.0.0.1/32"}
			cfg.Protocols = []string{"snmp"}
			cfg.Credentials = map[string]string{"snmp.community": community}
		})
	if len(records) != 1 {
		t.Fatalf("uploaded records: %d, want 1", len(records))
	}
	var doc map[string]any
	if err = json.Unmarshal(records[0], &doc); err != nil {
		t.Fatal(err)
	}
	for _, problem := range schema.validate(doc) {
		t.Errorf("record: %s\n%s", problem, records[0])
	}
	var rec plugins.DeviceRecord
	if err = json.Unmarshal(records[0], &rec); err != nil {
		t.Fatal(err)
	}
	if violations := rec.Validate(); len(violations) > 0 {
		t.Errorf("record breaks the Go contract: %v", violations)
	}
	want := []any{
		map[string]any{"protocol": "lldp", "local_interface": "GigabitEthernet1/0/1", "remote_mac": "00:1b:54:aa:bb:cc",
			"remote_ip": "192.0.2.12", "remote_hostname": "dist-sw-2", "remote_interface": "Te1/1/1"},
		map[string]any{"protocol": "fdb", "local_interface": "GigabitEthernet1/0/2", "remote_mac": "00:50:56:01:02:03"},
	}
	if !reflect.DeepEqual(doc["neighbors"], want) {
		t.Errorf("uploaded neighbors = %v, want %v\n%s", doc["neighbors"], want, records[0])
	}
	if rec.Source != "snmp" || rec.TargetIP != "127.0.0.1" || rec.System.Name != "access-sw-1" || len(rec.Interfaces) != 2 {
		t.Errorf("record: source %q, target %q, name %q, interfaces %+v", rec.Source, rec.TargetIP, rec.System.Name, rec.Interfaces)
	}
}

// discoveryUpload runs the discovery loop with the plugins of swap in the
// registry against a capturing ingest endpoint and returns the records of
// the one batch it uploads; configure completes the configuration.
func discoveryUpload(t *testing.T, swap map[string]plugins.Plugin, configure func(*collectorConfig)) []json.RawMessage {
	t.Helper()
	var (
		mu      sync.Mutex
		batches [][]byte
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/discovery/ingest") {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		gz, gzErr := gzip.NewReader(r.Body)
		if gzErr != nil {
			t.Errorf("gzip: %v", gzErr)
			return
		}
		var raw json.RawMessage
		if decErr := json.NewDecoder(gz).Decode(&raw); decErr != nil {
			t.Errorf("decode: %v", decErr)
			return
		}
		mu.Lock()
		batches = append(batches, raw)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	for name, plug := range swap {
		previous, had := pluginRegistry[name]
		pluginRegistry[name] = plug
		defer func(name string, previous plugins.Plugin, had bool) {
			if had {
				pluginRegistry[name] = previous
			} else {
				delete(pluginRegistry, name)
			}
		}(name, previous, had)
	}

	cfg := testConfig(srv.URL, "")
	cfg.DiscoveryInterval = time.Hour
	configure(&cfg)
	up, err := newUploader(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runDiscoveryLoop(ctx, cfg, up)
		close(done)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for {
		mu.Lock()
		n := len(batches)
		mu.Unlock()
		if n > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(batches) != 1 {
		t.Fatalf("uploaded batches: %d, want 1", len(batches))
	}
	var batch struct {
		Records []json.RawMessage `json:"records"`
	}
	if err = json.Unmarshal(batches[0], &batch); err != nil {
		t.Fatalf("batch is not a DeviceRecord batch: %v", err)
	}
	return batch.Records
}

// TestDeviceRecordSchemaAndGoValidationAgree feeds malformed records to both
// the JSON schema and DeviceRecord.Validate: both must reject each of them.
func TestDeviceRecordSchemaAndGoValidationAgree(t *testing.T) {
	schema := loadDeviceRecordSchema(t)
	valid := func() plugins.DeviceRecord {
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "sw", IP: "192.0.2.1"}, time.Now())
		rec.Interfaces = []plugins.DeviceInterface{{Name: "eth0", IPs: []plugins.DeviceIP{{Address: "192.0.2.1", PrefixLength: 24}}}}
		rec.Neighbors = []plugins.DeviceNeighbor{{Protocol: "lldp", RemoteMAC: "00:11:22:33:44:55"}}
		return rec
	}
	cases := map[string]func(*plugins.DeviceRecord){
		"record_id":          func(r *plugins.DeviceRecord) { r.RecordID = "not-a-uuid" },
		"source":             func(r *plugins.DeviceRecord) { r.Source = "carrier-pigeon" },
		"target_ip":          func(r *plugins.DeviceRecord) { r.TargetIP = "999.1.1.1" },
		"vrf_id":             func(r *plugins.DeviceRecord) { r.VRFID = "vrf-1" },
		"client_id":          func(r *plugins.DeviceRecord) { r.ClientID = "acme" },
		"ci_type":            func(r *plugins.DeviceRecord) { r.Classification.CIType = "" },
		"primary_mac":        func(r *plugins.DeviceRecord) { r.Identity.PrimaryMAC = "00:11" },
		"hardware_uuid":      func(r *plugins.DeviceRecord) { r.Identity.HardwareUUID = "xyz" },
		"uptime":             func(r *plugins.DeviceRecord) { r.System.UptimeSeconds = -1 },
		"interface name":     func(r *plugins.DeviceRecord) { r.Interfaces[0].Name = "" },
		"interface status":   func(r *plugins.DeviceRecord) { r.Interfaces[0].OperStatus = "flapping" },
		"vlan":               func(r *plugins.DeviceRecord) { r.Interfaces[0].VlanID = 5000 },
		"interface ip":       func(r *plugins.DeviceRecord) { r.Interfaces[0].IPs[0].Address = "192.0.2" },
		"prefix length":      func(r *plugins.DeviceRecord) { r.Interfaces[0].IPs[0].PrefixLength = 129 },
		"neighbor protocol":  func(r *plugins.DeviceRecord) { r.Neighbors[0].Protocol = "smoke" },
		"neighbor anonymous": func(r *plugins.DeviceRecord) { r.Neighbors[0].RemoteMAC = "" },
		"power":              func(r *plugins.DeviceRecord) { r.Power = &plugins.DevicePower{DrawWatts: -5} },
		"outlet":             func(r *plugins.DeviceRecord) { r.Power = &plugins.DevicePower{Outlets: []plugins.DeviceOutlet{{}}} },
		"reserved attribute": func(r *plugins.DeviceRecord) { r.Attributes = map[string]any{"_overrides": map[string]any{}} },
	}
	base := valid()
	if v := base.Validate(); len(v) > 0 {
		t.Fatalf("base record invalid: %v", v)
	}
	if problems := schema.validate(toJSONDoc(t, base)); len(problems) > 0 {
		t.Fatalf("base record breaks the schema: %v", problems)
	}
	for name, breakIt := range cases {
		rec := valid()
		breakIt(&rec)
		if len(rec.Validate()) == 0 {
			t.Errorf("%s: Validate accepted the record", name)
		}
		if len(schema.validate(toJSONDoc(t, rec))) == 0 {
			t.Errorf("%s: the schema accepted the record", name)
		}
	}
	// A missing observed_at is a zero time in Go and an absent key in JSON.
	doc := toJSONDoc(t, base).(map[string]any)
	delete(doc, "observed_at")
	if len(schema.validate(doc)) == 0 {
		t.Error("the schema accepted a record without observed_at")
	}
	if rec := (plugins.DeviceRecord{}); len(rec.Validate()) == 0 {
		t.Error("Validate accepted an empty record")
	}
}

type fixturePlugin struct {
	name   string
	result plugins.Result
}

func (p fixturePlugin) Name() string { return p.name }
func (p fixturePlugin) Probe(context.Context, []string, map[string]string) ([]plugins.Result, error) {
	return []plugins.Result{p.result}, nil
}
func (p fixturePlugin) Collect(_ context.Context, target string, _ map[string]string) (*plugins.DeviceRecord, error) {
	res := p.result
	res.IP = target
	return plugins.CollectRecord(p.name, &res, nil, time.Now())
}

func toJSONDoc(t *testing.T, v any) any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err = json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// jsonSchema validates documents against the subset of JSON Schema 2020-12
// that api/devicerecord.schema.json uses. Unknown keywords fail the test, so
// the schema cannot grow a rule this check silently ignores.
type jsonSchema struct {
	t    *testing.T
	root map[string]any
}

func loadDeviceRecordSchema(t *testing.T) *jsonSchema {
	t.Helper()
	raw, err := os.ReadFile("../../api/devicerecord.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err = json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	return &jsonSchema{t: t, root: root}
}

func (s *jsonSchema) validate(doc any) []string {
	return s.check(s.root, doc, "$")
}

var annotationKeywords = map[string]bool{"$schema": true, "$id": true, "title": true, "description": true, "$defs": true}

func (s *jsonSchema) check(schema map[string]any, v any, path string) []string {
	var out []string
	fail := func(format string, args ...any) { out = append(out, path+": "+fmt.Sprintf(format, args...)) }
	for key, rule := range schema {
		switch key {
		case "$ref":
			ref := rule.(string)
			name, ok := strings.CutPrefix(ref, "#/$defs/")
			if !ok {
				s.t.Fatalf("unsupported $ref %s", ref)
			}
			out = append(out, s.check(s.root["$defs"].(map[string]any)[name].(map[string]any), v, path)...)
		case "type":
			if !hasType(v, rule.(string)) {
				fail("want %s, got %T", rule, v)
			}
		case "enum":
			found := false
			for _, allowed := range rule.([]any) {
				if allowed == v {
					found = true
				}
			}
			if !found {
				fail("%v is not one of %v", v, rule)
			}
		case "pattern":
			if str, ok := v.(string); ok && !regexp.MustCompile(rule.(string)).MatchString(str) {
				fail("%q does not match %s", str, rule)
			}
		case "minLength":
			if str, ok := v.(string); ok && float64(len(str)) < rule.(float64) {
				fail("shorter than %v", rule)
			}
		case "minimum":
			if n, ok := v.(float64); ok && n < rule.(float64) {
				fail("%v below %v", n, rule)
			}
		case "maximum":
			if n, ok := v.(float64); ok && n > rule.(float64) {
				fail("%v above %v", n, rule)
			}
		case "format":
			if str, ok := v.(string); ok && !hasFormat(str, rule.(string)) {
				fail("%q is not a %s", str, rule)
			}
		case "required":
			if obj, ok := v.(map[string]any); ok {
				for _, name := range rule.([]any) {
					if _, present := obj[name.(string)]; !present {
						fail("missing %s", name)
					}
				}
			}
		case "properties":
			if obj, ok := v.(map[string]any); ok {
				for name, sub := range rule.(map[string]any) {
					if value, present := obj[name]; present {
						out = append(out, s.check(sub.(map[string]any), value, path+"."+name)...)
					}
				}
			}
		case "additionalProperties":
			obj, ok := v.(map[string]any)
			if !ok || rule != false {
				continue
			}
			known, _ := schema["properties"].(map[string]any)
			for name := range obj {
				if _, declared := known[name]; !declared {
					fail("unknown property %s", name)
				}
			}
		case "items":
			if arr, ok := v.([]any); ok {
				for i, item := range arr {
					out = append(out, s.check(rule.(map[string]any), item, fmt.Sprintf("%s[%d]", path, i))...)
				}
			}
		case "not":
			if len(s.check(rule.(map[string]any), v, path)) == 0 {
				fail("matches the excluded schema")
			}
		case "propertyNames":
			if obj, ok := v.(map[string]any); ok {
				for name := range obj {
					out = append(out, s.check(rule.(map[string]any), name, path+"."+name)...)
				}
			}
		case "anyOf":
			matched := false
			for _, option := range rule.([]any) {
				if len(s.check(option.(map[string]any), v, path)) == 0 {
					matched = true
					break
				}
			}
			if !matched {
				fail("matches none of anyOf")
			}
		default:
			if !annotationKeywords[key] {
				s.t.Fatalf("schema keyword %q at %s is not supported by this check", key, path)
			}
		}
	}
	return out
}

func hasType(v any, want string) bool {
	switch want {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n == math.Trunc(n)
	case "boolean":
		_, ok := v.(bool)
		return ok
	}
	return false
}

func hasFormat(v, format string) bool {
	switch format {
	case "date-time":
		_, err := time.Parse(time.RFC3339Nano, v)
		return err == nil
	case "ipv4":
		ip := net.ParseIP(v)
		return ip != nil && ip.To4() != nil && !strings.Contains(v, ":")
	case "ipv6":
		ip := net.ParseIP(v)
		return ip != nil && strings.Contains(v, ":")
	}
	return false
}
