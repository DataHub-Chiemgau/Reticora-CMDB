package discovery

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

// TestDeviceRecordGoTypeMatchesSchema covers WP-083 (DIS-05): the shared Go
// type and api/devicerecord.schema.json describe the same contract. Every
// object of the schema has exactly the JSON fields of its Go struct, the
// required fields are the Go fields without omitempty, and the enums are the
// values the Go validation accepts.
func TestDeviceRecordGoTypeMatchesSchema(t *testing.T) {
	raw, err := os.ReadFile("../../../api/devicerecord.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	defs := schema["$defs"].(map[string]any)
	resolve := func(node map[string]any) map[string]any {
		if ref, ok := node["$ref"].(string); ok {
			return defs[strings.TrimPrefix(ref, "#/$defs/")].(map[string]any)
		}
		return node
	}

	var compare func(path string, typ reflect.Type, node map[string]any)
	compare = func(path string, typ reflect.Type, node map[string]any) {
		props, _ := node["properties"].(map[string]any)
		var required []string
		for _, r := range asSlice(node["required"]) {
			required = append(required, r.(string))
		}
		if node["additionalProperties"] != false {
			t.Errorf("%s: schema object allows unknown properties", path)
		}
		var goRequired []string
		seen := map[string]bool{}
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			name, opts, _ := strings.Cut(field.Tag.Get("json"), ",")
			seen[name] = true
			if !strings.Contains(opts, "omitempty") {
				goRequired = append(goRequired, name)
			}
			prop, ok := props[name].(map[string]any)
			if !ok {
				t.Errorf("%s.%s: Go field missing in the schema", path, name)
				continue
			}
			ft := field.Type
			for ft.Kind() == reflect.Pointer || ft.Kind() == reflect.Slice {
				if ft.Kind() == reflect.Slice {
					prop = prop["items"].(map[string]any)
				}
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft != reflect.TypeOf(time.Time{}) {
				compare(path+"."+name, ft, prop)
			}
		}
		for name := range props {
			if !seen[name] {
				t.Errorf("%s.%s: schema property without a Go field", path, name)
			}
		}
		sort.Strings(required)
		sort.Strings(goRequired)
		if !reflect.DeepEqual(required, goRequired) {
			t.Errorf("%s: schema requires %v, Go requires %v", path, required, goRequired)
		}
	}
	compare("$", reflect.TypeOf(DeviceRecord{}), schema)

	props := schema["properties"].(map[string]any)
	enum := func(node map[string]any) []string {
		var out []string
		for _, v := range asSlice(resolve(node)["enum"]) {
			out = append(out, v.(string))
		}
		sort.Strings(out)
		return out
	}
	sorted := func(in []string) []string {
		out := append([]string(nil), in...)
		sort.Strings(out)
		return out
	}
	ifc := props["interfaces"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	neighbor := props["neighbors"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	for name, pair := range map[string][2][]string{
		"source":       {enum(props["source"].(map[string]any)), sorted(plugins.DeviceRecordSources)},
		"oper_status":  {enum(ifc["oper_status"].(map[string]any)), sorted(plugins.InterfaceStatuses)},
		"admin_status": {enum(ifc["admin_status"].(map[string]any)), sorted(plugins.InterfaceStatuses)},
		"protocol":     {enum(neighbor["protocol"].(map[string]any)), sorted(plugins.NeighborProtocols)},
	} {
		if !reflect.DeepEqual(pair[0], pair[1]) {
			t.Errorf("enum %s: schema %v, Go %v", name, pair[0], pair[1])
		}
	}
}

func asSlice(v any) []any {
	s, _ := v.([]any)
	return s
}

// TestIngestAcceptsDeviceRecords covers WP-083 (DIS-05, GLO-12) at the
// ingest endpoints: a gzip-compressed DeviceRecord batch from a collector
// creates the CI with the record's identity, classification and system data;
// invalid records are rejected with 422 naming the field; the old
// collector_id/items contract still works but carries a Deprecation header.
func TestIngestAcceptsDeviceRecords(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(discoveryRepo, ciRepo).RegisterRoutes(mux)

	post := func(path string, body any, compress bool) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if compress {
			gz := gzip.NewWriter(&buf)
			_, _ = gz.Write(raw)
			_ = gz.Close()
		} else {
			buf.Write(raw)
		}
		req := httptest.NewRequest(http.MethodPost, path, &buf)
		if compress {
			req.Header.Set("Content-Encoding", "gzip")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, discoveryTenantCtx(req))
		return w
	}

	observed := time.Date(2026, 10, 12, 8, 0, 0, 0, time.UTC)
	rec := plugins.RecordFromResult("redfish", &plugins.Result{
		CIType: "server", Name: "srv-rec", IP: "192.0.2.20", Manufacturer: "Dell", Model: "R760", Serial: "SN-REC",
		Firmware: "2.1", Attributes: map[string]any{"rack_unit": "U12"},
	}, observed)
	rec.Identity.HardwareUUID = "4c4c4544-0042-3510-8051-b4c04f4a4e33"
	rec.Interfaces = []plugins.DeviceInterface{{Name: "eth0", MAC: "aa:bb:cc:dd:ee:01",
		IPs: []plugins.DeviceIP{{Address: "192.0.2.20", PrefixLength: 24}}}}
	rec.Neighbors = []plugins.DeviceNeighbor{{Protocol: "lldp", RemoteMAC: "aa:bb:cc:dd:ee:99", LocalInterface: "eth0"}}

	for _, path := range []string{"/api/v1/discovery/ingest", "/api/v1/ingest/bulk"} {
		w := post(path, DeviceRecordBatch{Records: []DeviceRecord{rec}}, true)
		if w.Code != http.StatusAccepted || w.Header().Get("Deprecation") != "" {
			t.Fatalf("%s: %d %s (Deprecation %q)", path, w.Code, w.Body.String(), w.Header().Get("Deprecation"))
		}
	}
	items, _, err := ciRepo.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("CIs after two identical records: %d, %v", len(items), err)
	}
	got := items[0]
	if got.Name != "srv-rec" || got.SerialNumber != "SN-REC" || got.Manufacturer != "Dell" || got.Model != "R760" ||
		got.ManagementIP != "192.0.2.20" || got.HardwareUUID != rec.Identity.HardwareUUID || got.DiscoverySource != "redfish" {
		t.Errorf("CI from the record: %+v", got)
	}
	if got.Attributes["firmware_version"] != "2.1" || got.Attributes["rack_unit"] != "U12" {
		t.Errorf("attributes from the record: %v", got.Attributes)
	}
	rawData, _ := got.Attributes["raw_data"].(map[string]any)
	if rawData["target_ip"] != "192.0.2.20" || rawData["record_id"] != nil || rawData["observed_at"] != nil {
		t.Errorf("raw_data describes the device, not the record: %v", rawData)
	}

	item := itemFromRecord(&rec)
	if !item.ObservedAt.Equal(observed) || item.RecordID != rec.RecordID || len(item.Relationships) != 1 ||
		item.Relationships[0].TargetMAC != "aa:bb:cc:dd:ee:99" || len(item.Interfaces) != 1 ||
		!item.Interfaces[0].Management || item.Interfaces[0].IPs[0] != "192.0.2.20/24" {
		t.Errorf("ingest item from the record: %+v", item)
	}

	bad := rec
	bad.RecordID = "nope"
	bad.Classification.CIType = ""
	w := post("/api/v1/discovery/ingest", DeviceRecordBatch{Records: []DeviceRecord{rec, bad}}, true)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "records[1].record_id") ||
		!strings.Contains(w.Body.String(), "records[1].classification.ci_type") {
		t.Errorf("invalid record: %d %s", w.Code, w.Body.String())
	}

	legacy := map[string]any{"collector_id": "col-1", "items": []map[string]any{{"ci_type_name": "server", "name": "legacy-1"}}}
	if w = post("/api/v1/ingest/bulk", legacy, false); w.Code != http.StatusAccepted || w.Header().Get("Deprecation") != legacyIngestDeprecation {
		t.Errorf("legacy items: %d, Deprecation %q", w.Code, w.Header().Get("Deprecation"))
	}
	both := map[string]any{"records": []DeviceRecord{rec}, "items": legacy["items"]}
	if w = post("/api/v1/ingest/bulk", both, false); w.Code != http.StatusBadRequest {
		t.Errorf("records and items together: %d", w.Code)
	}
	// Reserved namespaces are rejected (GLO-13, WP-091).
	reserved := rec
	reserved.Attributes = map[string]any{"_instance": map[string]any{"x": 1}}
	if w = post("/api/v1/discovery/ingest", DeviceRecordBatch{Records: []DeviceRecord{reserved}}, true); w.Code != http.StatusBadRequest ||
		!strings.Contains(w.Body.String(), "records[0].attributes._instance") {
		t.Errorf("reserved attribute key: %d %s", w.Code, w.Body.String())
	}
	if w = post("/api/v1/ingest/bulk", map[string]any{"collector_id": "c", "items": []map[string]any{{"ci_type_name": "server",
		"name": "x", "attributes": map[string]any{"_overrides": 1}}}}, false); w.Code != http.StatusBadRequest {
		t.Errorf("reserved attribute key in a legacy item: %d", w.Code)
	}
	if w = post("/api/v1/discovery/ingest", []plugins.Result{{Name: "x"}}, true); w.Code != http.StatusBadRequest {
		t.Errorf("the old collector result array: %d, want 400", w.Code)
	}
}
