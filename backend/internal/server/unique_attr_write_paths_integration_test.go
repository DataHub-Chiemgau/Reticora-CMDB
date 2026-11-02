package server_test

// WP-700 (MET-15, WP-142 AK2; F-429): every CI write path that sets the
// values of a CI checks the unique attributes as POST /cis does, also while
// the unique index does not exist yet (the flag is set, the index job has
// not run or failed, DB-04). Through the complete server.NewRouter
// (authorization and OpenAPI validation, newAuditT3Router) against
// PostgreSQL, as an org-wide principal, each path meets a value another
// active CI holds:
//   - POST /cis (control) and POST /cis/{id}/restore of a CI whose value
//     was taken after its deletion;
//   - POST /cis/{id}/unmerge of a victim whose value was taken after the
//     merge;
//   - the resolution merge of a resurrected_device item (reactivation of
//     the deleted CI), new of an unlicensed_ci item (adoption of the held
//     device with its attributes) and new of an unclassified_device item
//     (the CI of the device record).
//
// Each answers 409 urn:reticora:problem:conflict with violations of the
// attribute; nothing is written: the CI stays deleted or merged, the item
// stays open, one active CI holds the value, and the index job then builds
// the indexes. With the index built, the restore and new of unlicensed_ci
// still answer 409 with the violations (before the fix: 500).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run '^TestUniqueAttributeEveryWritePath$' ./internal/server/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestUniqueAttributeEveryWritePath(t *testing.T) {
	r := newAuditT3Router(t, "7a")
	f := r.f
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	types := citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp700-ups", Name: "wp700-ups", IsObservable: true}
	if err := types.Create(orgCtx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	// The flags only: the index job has not run (DB-04 window).
	var defs []*citype.AttributeDefinition
	for key, dataType := range map[string]string{"asset_tag": "string", "fingerprint": "json"} {
		d := &citype.AttributeDefinition{CITypeID: typ.ID, Key: key, DataType: dataType, Unique: true, UniqueScope: "org"}
		if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, d); err != nil {
			t.Fatalf("define %s: %v", key, err)
		}
		t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(d.ID, true)) })
		defs = append(defs, d)
	}
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	// inventory_no has its index from the start.
	indexed := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "inventory_no", DataType: "string", Unique: true, UniqueScope: "org"}
	if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, indexed); err != nil {
		t.Fatalf("define inventory_no: %v", err)
	}
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(indexed.ID, true)) })
	if err := ddl.Ensure(bg, f.OrgA, indexed.ID); err != nil {
		t.Fatalf("index inventory_no: %v", err)
	}
	all := identity.AllPermissions()
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return r.call(all, http.MethodPost, path, body)
	}
	createCI := func(name, attrs string) string {
		t.Helper()
		w := post("/api/v1/cis", `{"name":"`+name+`","ci_type_id":"`+typ.ID+`","status":"active","attributes":`+attrs+`}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST /cis %s: %d %s", name, w.Code, w.Body.String())
		}
		var body struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.ID
	}
	deleteCI := func(id string) {
		t.Helper()
		if w := r.call(all, http.MethodDelete, "/api/v1/cis/"+id, ""); w.Code/100 != 2 {
			t.Fatalf("DELETE /cis/%s: %d %s", id, w.Code, w.Body.String())
		}
	}
	// holders counts the active CIs holding value (JSON) under key.
	holders := func(key, value string) string {
		return r.str(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL
			AND attributes -> $2::text = $3::jsonb`, f.OrgA, key, value)
	}
	// refused asserts 409 conflict with exactly the violation of field.
	refused := func(what string, w *httptest.ResponseRecorder, field string) {
		t.Helper()
		var body struct {
			Type       string `json:"type"`
			Violations []struct {
				Field string `json:"field"`
			} `json:"violations"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusConflict || body.Type != "urn:reticora:problem:conflict" ||
			len(body.Violations) != 1 || body.Violations[0].Field != field {
			t.Errorf("%s: %d %.300s, want 409 urn:reticora:problem:conflict with the violation of %s", what, w.Code, w.Body.String(), field)
		}
	}
	single := func(what, key, value string) {
		t.Helper()
		if n := holders(key, value); n != "1" {
			t.Errorf("%s: %s active CIs hold %s %s, want 1", what, n, key, value)
		}
	}
	itemOpen := func(what, id string) {
		t.Helper()
		if status := r.str(`SELECT status FROM review_item WHERE id = $1`, id); status != "open" {
			t.Errorf("%s: the item is %s, want it open", what, status)
		}
	}

	// Control: POST /cis checks without the index.
	createCI("wp700-holder", `{"asset_tag":"T-1"}`)
	refused("POST /cis with a taken value", post("/api/v1/cis", `{"name":"wp700-dup","ci_type_id":"`+typ.ID+
		`","status":"active","attributes":{"asset_tag":"T-1"}}`), "asset_tag")

	// Restore of a CI whose value another CI took after the deletion.
	restored := createCI("wp700-restore", `{"asset_tag":"R-1"}`)
	deleteCI(restored)
	createCI("wp700-restore-taker", `{"asset_tag":"R-1"}`)
	refused("POST /cis/{id}/restore", post("/api/v1/cis/"+restored+"/restore", ""), "asset_tag")
	single("restore", "asset_tag", `"R-1"`)
	if gone := r.str(`SELECT (deleted_at IS NOT NULL)::text FROM ci WHERE id = $1`, restored); gone != "true" {
		t.Errorf("restore refused: the CI is active again, want it deleted")
	}

	// Reactivation through a resurrected_device item.
	resurrected := createCI("wp700-resurrected", `{"asset_tag":"D-1"}`)
	deleteCI(resurrected)
	createCI("wp700-resurrected-taker", `{"asset_tag":"D-1"}`)
	item := r.str(`INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids)
		VALUES ($1, 'resurrected_device', 'open', '{}', ARRAY[$2::uuid]) RETURNING id::text`, f.OrgA, resurrected)
	refused("resurrected_device merge", post("/api/v1/review-items/"+item+"/resolve", `{"action":"merge","ci_id":"`+resurrected+`"}`), "asset_tag")
	single("resurrected_device merge", "asset_tag", `"D-1"`)
	itemOpen("resurrected_device merge", item)

	// Unmerge of a victim whose value another CI took after the merge: the
	// survivor kept its own asset_tag, the victim's became free.
	survivor := createCI("wp700-survivor", `{"asset_tag":"S-1"}`)
	victim := createCI("wp700-victim", `{"asset_tag":"V-1"}`)
	if w := post("/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`); w.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", w.Code, w.Body.String())
	}
	createCI("wp700-victim-taker", `{"asset_tag":"V-1"}`)
	refused("POST /cis/{id}/unmerge", post("/api/v1/cis/"+victim+"/unmerge", ""), "asset_tag")
	single("unmerge", "asset_tag", `"V-1"`)
	if merged := r.str(`SELECT (merged_into_id IS NOT NULL)::text FROM ci WHERE id = $1`, victim); merged != "true" {
		t.Errorf("unmerge refused: the victim is no longer merged, want the merge to stay")
	}

	// Adoption of an unlicensed_ci item: the held device carries the value.
	createCI("wp700-adopt-holder", `{"asset_tag":"A-1"}`)
	item = r.str(`INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, 'unlicensed_ci', 'open',
		jsonb_build_object('ci_type_id', $2::text, 'source', 'sweep', 'device',
			jsonb_build_object('name', 'wp700-adopt', 'management_ip', '192.0.2.71', 'attributes', jsonb_build_object('asset_tag', 'A-1'))))
		RETURNING id::text`, f.OrgA, typ.ID)
	refused("unlicensed_ci new", post("/api/v1/review-items/"+item+"/resolve", `{"action":"new"}`), "asset_tag")
	single("unlicensed_ci new", "asset_tag", `"A-1"`)
	itemOpen("unlicensed_ci new", item)

	// new of an unclassified_device item: the device record carries the
	// fingerprint another CI holds.
	createCI("wp700-new-holder", `{"fingerprint":{"sys_descr":"wp700"}}`)
	item = r.str(`INSERT INTO review_item (organization_id, kind, status, payload, object_key) VALUES ($1, 'unclassified_device', 'open',
		jsonb_build_object('ci_type_name', 'wp700-ups', 'name', 'wp700-new', 'management_ip', '192.0.2.72', 'client_id', '',
			'fingerprint', jsonb_build_object('sys_descr', 'wp700')), 'wp700-new') RETURNING id::text`, f.OrgA)
	refused("unclassified_device new", post("/api/v1/review-items/"+item+"/resolve", `{"action":"new"}`), "fingerprint")
	single("unclassified_device new", "fingerprint", `{"sys_descr":"wp700"}`)
	itemOpen("unclassified_device new", item)

	// Nothing to clean up: the index job builds the indexes.
	for _, d := range defs {
		if err := ddl.Ensure(bg, f.OrgA, d.ID); err != nil {
			t.Errorf("index job for %s after the refused writes: %v", d.Key, err)
		}
	}

	// With the index the answers are the same.
	restored = createCI("wp700-indexed-restore", `{"inventory_no":"I-1"}`)
	deleteCI(restored)
	createCI("wp700-indexed-taker", `{"inventory_no":"I-1"}`)
	refused("POST /cis/{id}/restore with the index", post("/api/v1/cis/"+restored+"/restore", ""), "inventory_no")
	createCI("wp700-indexed-holder", `{"inventory_no":"I-2"}`)
	item = r.str(`INSERT INTO review_item (organization_id, kind, status, payload) VALUES ($1, 'unlicensed_ci', 'open',
		jsonb_build_object('ci_type_id', $2::text, 'source', 'sweep', 'device',
			jsonb_build_object('name', 'wp700-indexed-adopt', 'attributes', jsonb_build_object('inventory_no', 'I-2'))))
		RETURNING id::text`, f.OrgA, typ.ID)
	refused("unlicensed_ci new with the index", post("/api/v1/review-items/"+item+"/resolve", `{"action":"new"}`), "inventory_no")
	itemOpen("unlicensed_ci new with the index", item)
}
