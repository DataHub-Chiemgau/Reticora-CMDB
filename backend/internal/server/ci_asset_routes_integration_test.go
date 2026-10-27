package server_test

// WP-486 (DB-05, AST-04; F-239): the asset-owned fields of a linked CI
// through the complete router (authorization, OpenAPI validation) against
// PostgreSQL. Until now the 422 on a direct write existed only as a probe
// against the real binary. Asserted here:
//   - PATCH /api/v1/cis/{id} with location_id or serial_number on a CI
//     linked to an asset -> 422 with the field in violations; the CI keeps
//     its values;
//   - an observed serial number that differs from the asset's, ingested
//     through POST /api/v1/ingest/bulk, is stored on the CI and opens one
//     review item serial_mismatch;
//   - the same writes on a CI without asset -> 200;
//   - linking a CI in a room with an asset without location over PATCH
//     keeps the CI's location; the asset takes it (E-81 option 1).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//     go test -count=1 -v -run 'TestCIAssetOwnedFieldsThroughRouter' ./internal/server/

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestCIAssetOwnedFieldsThroughRouter(t *testing.T) {
	r := newAuditT3Router(t, "01")
	f := r.f
	ctx := f.OrgCtx(f.OrgA)
	tree := locations.NewPGRepository(f.App)
	newLocation := func(req locations.CreateRequest) string {
		t.Helper()
		loc, err := tree.Create(ctx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return loc.ID
	}
	site := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp486-s1"})
	building := newLocation(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site, Name: "wp486-b1"})
	room1 := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "wp486-r1"})
	room2 := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "wp486-r2"})

	all := identity.AllPermissions()
	patch := func(id, body string) (int, string) {
		t.Helper()
		w := r.call(all, http.MethodPatch, "/api/v1/cis/"+id, body)
		return w.Code, w.Body.String()
	}
	violationFields := func(body string) []string {
		t.Helper()
		var problem struct {
			Violations []struct {
				Field string `json:"field"`
			} `json:"violations"`
		}
		if err := json.Unmarshal([]byte(body), &problem); err != nil {
			t.Fatalf("422 body %s: %v", body, err)
		}
		fields := make([]string, len(problem.Violations))
		for i, v := range problem.Violations {
			fields[i] = v.Field
		}
		return fields
	}
	ingest := func(serial string) {
		t.Helper()
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "wp486-device", IP: "192.0.2.86",
			Serial: serial, Attributes: map[string]any{"hardwareUUID": "4860e4a2-0486-4486-8486-000000000486"}}, time.Now().Add(-time.Minute))
		raw, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if err != nil {
			t.Fatal(err)
		}
		if w := r.call(all, http.MethodPost, "/api/v1/ingest/bulk", string(raw)); w.Code != http.StatusAccepted {
			t.Fatalf("ingest %s: %d %s", serial, w.Code, w.Body.String())
		}
	}
	column := func(table, col, id string) string {
		t.Helper()
		return r.str(`SELECT COALESCE(`+col+`::text, '') FROM `+table+` WHERE id = $1`, id)
	}
	openMismatches := func(ciID string) string {
		t.Helper()
		return r.str(`SELECT count(*)::text FROM review_item WHERE organization_id = $1 AND kind = 'serial_mismatch'
			AND status = 'open' AND payload ->> 'ci_id' = $2`, f.OrgA, ciID)
	}

	// A discovered CI (identified by its hardware UUID, no serial number observed yet),
	// linked over PATCH to an asset in room 1: it takes the asset's location.
	ingest("")
	device := r.str(`SELECT id::text FROM ci WHERE organization_id = $1 AND name = 'wp486-device' AND deleted_at IS NULL`, f.OrgA)
	asset := r.str(`INSERT INTO asset (organization_id, asset_tag, name, serial_number, location_id)
		VALUES ($1, 'WP486-A', 'WP486-A', 'WP486-SN-ASSET', $2) RETURNING id::text`, f.OrgA, room1)
	if code, body := patch(device, `{"asset_id":"`+asset+`"}`); code != http.StatusOK {
		t.Fatalf("link the discovered CI: %d %s", code, body)
	}
	if loc := column("ci", "location_id", device); loc != room1 {
		t.Errorf("linked CI location %q, want the asset's %s", loc, room1)
	}
	if n := openMismatches(device); n != "0" {
		t.Errorf("open serial_mismatch reviews after the link without observed serial: %s, want 0", n)
	}

	// Direct writes of the asset-owned fields: 422 naming the field.
	for _, c := range []struct{ field, body string }{
		{"location_id", `{"location_id":"` + room2 + `"}`},
		{"serial_number", `{"serial_number":"WP486-MANUAL"}`},
	} {
		code, body := patch(device, c.body)
		if code != http.StatusUnprocessableEntity {
			t.Errorf("PATCH %s on the linked CI: %d %s, want 422", c.field, code, body)
			continue
		}
		if fields := violationFields(body); len(fields) != 1 || fields[0] != c.field {
			t.Errorf("PATCH %s on the linked CI: violations %q, want [%s]", c.field, fields, c.field)
		}
	}
	if loc, serial := column("ci", "location_id", device), column("ci", "serial_number", device); loc != room1 || serial != "" {
		t.Errorf("linked CI after the refused writes: location %q, serial %q", loc, serial)
	}

	// A differing observed serial number from discovery is stored on the CI
	// and opens exactly one review serial_mismatch; the asset keeps its own.
	ingest("WP486-SN-OBSERVED")
	if serial := column("ci", "serial_number", device); serial != "WP486-SN-OBSERVED" {
		t.Errorf("observed serial number on the CI: %q, want WP486-SN-OBSERVED", serial)
	}
	if serial := column("asset", "serial_number", asset); serial != "WP486-SN-ASSET" {
		t.Errorf("asset serial number: %q, want WP486-SN-ASSET", serial)
	}
	if n := openMismatches(device); n != "1" {
		t.Errorf("open serial_mismatch reviews after the differing ingest: %s, want 1", n)
	}

	// A CI without asset: the same writes are 200.
	plain := f.CI(t, f.OrgA, f.Client1, "wp486-plain")
	if code, body := patch(plain, `{"location_id":"`+room1+`"}`); code != http.StatusOK {
		t.Errorf("PATCH location_id on a CI without asset: %d %s, want 200", code, body)
	}
	if code, body := patch(plain, `{"serial_number":"WP486-PLAIN"}`); code != http.StatusOK {
		t.Errorf("PATCH serial_number on a CI without asset: %d %s, want 200", code, body)
	}
	if loc, serial := column("ci", "location_id", plain), column("ci", "serial_number", plain); loc != room1 || serial != "WP486-PLAIN" {
		t.Errorf("CI without asset after the writes: location %q, serial %q", loc, serial)
	}

	// Linking it with an asset without location keeps its location, site
	// and room (F-239); the asset takes room 1.
	site0, room0 := column("ci", "site_id", plain), column("ci", "room_id", plain)
	unlocated := r.str(`INSERT INTO asset (organization_id, asset_tag, name) VALUES ($1, 'WP486-B', 'WP486-B') RETURNING id::text`, f.OrgA)
	if code, body := patch(plain, `{"asset_id":"`+unlocated+`"}`); code != http.StatusOK {
		t.Fatalf("link with an asset without location: %d %s", code, body)
	}
	if loc, s, rm := column("ci", "location_id", plain), column("ci", "site_id", plain), column("ci", "room_id", plain); loc != room1 ||
		s != site0 || rm != room0 || s == "" || rm == "" {
		t.Errorf("CI after the link: location %q site %q room %q, want %s %s %s", loc, s, rm, room1, site0, room0)
	}
	if loc := column("asset", "location_id", unlocated); loc != room1 {
		t.Errorf("asset location after the link: %q, want %s", loc, room1)
	}
}
