package discovery_test

// WP-703 (MET-15, WP-142 AK2, CI-03, REC-01; F-432): the discovery ingest
// checks the unique attributes as a manual write does (ci.CheckUnique),
// also while the unique index does not exist yet (the flag is set, the
// index job has not run or failed, DB-04). A discovered value of a unique
// attribute that another active CI holds is dropped like a value the field
// metadata rejects (invalid_values, warning in the log); the rest of the
// record is stored and the other records of the batch go on. Through
// POST /api/v1/ingest/bulk against PostgreSQL:
//   - new devices of the batch (ci.BatchCreator, WP-333) whose value
//     another CI holds, and two new devices of one batch with the same
//     value: one CI holds the value;
//   - with the index built, the new device is created without the value
//     instead of failing its record (before: rejected, the device lost);
//   - a device no profile classified (generic_device, created on its own,
//     createDiscoveredCI) with the value of a global unique attribute;
//   - an update of a known device with a taken value, without and with
//     the index (before: duplicate, or the record rejected);
//   - the reclassification of a generic_device (CI-11) to a type whose
//     unique attribute another CI holds with the value the CI carries: the
//     CI stays a generic_device (its unclassified_device item stays open),
//     the record is accepted.
//
// Afterwards the index job builds the indexes.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run '^TestIngestUniqueAttributes$' ./internal/discovery/

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestIngestUniqueAttributes(t *testing.T) {
	f := scopetest.Seed(t, "7a")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	types := citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp703-ups", Name: "wp703-ups", IsObservable: true}
	if err := types.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	define := func(typeID, key string) *citype.AttributeDefinition {
		t.Helper()
		d := &citype.AttributeDefinition{CITypeID: typeID, Key: key, DataType: "string", Unique: true, UniqueScope: "org"}
		if err := types.CreateAttributeDefinition(ctx, f.OrgA, d); err != nil {
			t.Fatalf("define %s: %v", key, err)
		}
		t.Cleanup(func() {
			_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(d.ID, true))
			_, _ = f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE id = $1`, d.ID)
		})
		return d
	}
	// The flags only (DB-04 window): asset_tag of the type and the global
	// wp703_tag; inventory_no has its index from the start.
	defs := []*citype.AttributeDefinition{define(typ.ID, "asset_tag"), define("", "wp703_tag")}
	indexed := define(typ.ID, "inventory_no")
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	if err := ddl.Ensure(bg, f.OrgA, indexed.ID); err != nil {
		t.Fatalf("index inventory_no: %v", err)
	}

	cis := ci.NewPGRepository(f.App)
	svc := ci.NewService(cis).WithFieldResolver(types)
	for _, h := range []struct{ name, attrs string }{
		{"wp703-holder-u", `{"asset_tag":"U-1"}`}, {"wp703-holder-i", `{"inventory_no":"I-1"}`}, {"wp703-holder-g", `{"wp703_tag":"G-1"}`},
	} {
		var attrs map[string]any
		_ = json.Unmarshal([]byte(h.attrs), &attrs)
		if err := svc.Create(ctx, &ci.Item{OrganizationID: f.OrgA, CITypeID: typ.ID, Name: h.name, Status: "active", Attributes: attrs}); err != nil {
			t.Fatalf("holder %s: %v", h.name, err)
		}
	}

	mux := chi.NewRouter()
	// Wiring as server/router.go: the discovery handler validates observed
	// values with the CI service.
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).WithObservedValidator(svc).RegisterRoutes(mux)
	ingest := func(what, items string) map[string]any {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk",
			bytes.NewReader([]byte(`{"collector_id":"wp703-collector","items":[`+items+`]}`))).WithContext(ctx)
		req.Header.Set("X-Collector-ID", "wp703-collector")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		var resp map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &resp)
		if w.Code != http.StatusAccepted {
			t.Errorf("%s: ingest %d %s, want 202", what, w.Code, w.Body.String())
		}
		if rejected, _ := resp["rejected"].(float64); rejected != 0 {
			t.Errorf("%s: %s, want no rejected record (a taken value fails no record)", what, w.Body.String())
		}
		return resp
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	// holders counts the active CIs holding value under key; of the type,
	// unless global.
	holders := func(key, value string, global bool) string {
		return str(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL
			AND ($2 OR ci_type_id = $3::uuid) AND attributes ->> $4::text = $5`, f.OrgA, global, typ.ID, key, value)
	}
	single := func(what, key, value string, global bool) {
		t.Helper()
		if n := holders(key, value, global); n != "1" {
			t.Errorf("%s: %s active CIs hold %s %s, want 1", what, n, key, value)
		}
	}
	// stored reports whether the CI of the device exists and its value of key.
	stored := func(what, name, key string) {
		t.Helper()
		if n := str(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND name = $2`, f.OrgA, name); n != "1" {
			t.Errorf("%s: %s CIs of device %s, want 1 (the device is stored without the taken value)", what, n, name)
			return
		}
		if v := str(`SELECT COALESCE(attributes ->> $3::text, '') FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND name = $2`,
			f.OrgA, name, key); v != "" {
			t.Errorf("%s: device %s stores %s %q, want it without the taken value", what, name, key, v)
		}
	}
	invalid := func(what string, resp map[string]any, want int) {
		t.Helper()
		if got, _ := resp["invalid_values"].(float64); int(got) != want {
			t.Errorf("%s: invalid_values = %v, want %d (the dropped values)", what, resp["invalid_values"], want)
		}
	}
	item := func(typeName, name, serial, attrs string) string {
		return `{"ci_type_name":"` + typeName + `","name":"` + name + `","serial_number":"` + serial + `","attributes":` + attrs + `}`
	}

	// New devices in the batch: a value another CI holds, two new devices
	// with the same value, and a free value.
	resp := ingest("batch", item("wp703-ups", "wp703-new-taken", "SN-703-1", `{"asset_tag":"U-1"}`)+","+
		item("wp703-ups", "wp703-new-twin-a", "SN-703-2", `{"asset_tag":"U-2"}`)+","+
		item("wp703-ups", "wp703-new-twin-b", "SN-703-3", `{"asset_tag":"U-2"}`)+","+
		item("wp703-ups", "wp703-new-free", "SN-703-4", `{"asset_tag":"U-3"}`))
	single("batch, value of another CI", "asset_tag", "U-1", false)
	stored("batch, value of another CI", "wp703-new-taken", "asset_tag")
	single("batch, two new devices with one value", "asset_tag", "U-2", false)
	single("batch, free value", "asset_tag", "U-3", false)
	invalid("batch", resp, 2)

	// With the index: the device is stored without the value.
	resp = ingest("batch with the index", item("wp703-ups", "wp703-new-indexed", "SN-703-5", `{"inventory_no":"I-1"}`)+","+
		item("wp703-ups", "wp703-new-indexed-next", "SN-703-6", `{"inventory_no":"I-2"}`))
	stored("batch with the index", "wp703-new-indexed", "inventory_no")
	single("batch with the index, next record", "inventory_no", "I-2", false)
	invalid("batch with the index", resp, 1)

	// A device no profile classified is created on its own.
	resp = ingest("generic_device", item("generic_device", "wp703-generic", "SN-703-7", `{"classified_by":"fallback","wp703_tag":"G-1"}`))
	single("generic_device", "wp703_tag", "G-1", true)
	stored("generic_device", "wp703-generic", "wp703_tag")
	invalid("generic_device", resp, 1)

	// Updates of a known device: the taken value is not written, the
	// stored one stays, and the record is accepted.
	ingest("known device, first record", item("wp703-ups", "wp703-known", "SN-703-8", `{"asset_tag":"U-8","inventory_no":"I-8"}`))
	resp = ingest("known device", item("wp703-ups", "wp703-known", "SN-703-8", `{"asset_tag":"U-1"}`))
	single("known device", "asset_tag", "U-1", false)
	invalid("known device", resp, 1)
	resp = ingest("known device with the index", item("wp703-ups", "wp703-known", "SN-703-8", `{"inventory_no":"I-1"}`))
	single("known device with the index", "inventory_no", "I-1", false)
	invalid("known device with the index", resp, 1)
	if v := str(`SELECT (attributes ->> 'asset_tag') || '/' || (attributes ->> 'inventory_no') FROM ci WHERE organization_id = $1
		AND deleted_at IS NULL AND name = 'wp703-known'`, f.OrgA); v != "U-8/I-8" {
		t.Errorf("known device stores %s, want its values U-8/I-8", v)
	}

	// Reclassification of a generic_device whose value the target type's
	// unique attribute holds elsewhere.
	ingest("generic_device to reclassify", item("generic_device", "wp703-reclassify", "SN-703-9", `{"classified_by":"fallback","asset_tag":"U-1"}`))
	ingest("reclassification", item("wp703-ups", "wp703-reclassify", "SN-703-9", `{}`))
	single("reclassification", "asset_tag", "U-1", false)
	if key := str(`SELECT t.key FROM ci c JOIN ci_type t ON t.id = c.ci_type_id WHERE c.organization_id = $1 AND c.deleted_at IS NULL
		AND c.name = 'wp703-reclassify'`, f.OrgA); key != "generic_device" {
		t.Errorf("reclassification: the CI became %s, want it to stay generic_device", key)
	}
	if status := str(`SELECT COALESCE(string_agg(r.status, ','), 'none') FROM review_item r JOIN ci c ON r.object_key = 'ci:' || c.id::text
		WHERE r.organization_id = $1 AND r.kind = 'unclassified_device' AND c.name = 'wp703-reclassify'`, f.OrgA); status != "open" {
		t.Errorf("reclassification: unclassified_device item %s, want open", status)
	}

	// Nothing to clean up: the index job builds the indexes.
	for _, d := range defs {
		if err := ddl.Ensure(bg, f.OrgA, d.ID); err != nil {
			t.Errorf("index job for %s after the ingest: %v", d.Key, err)
		}
	}
}
