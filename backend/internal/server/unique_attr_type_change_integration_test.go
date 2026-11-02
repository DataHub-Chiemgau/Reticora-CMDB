package server_test

// WP-702 (MET-15, WP-142 AK2, CI-11; F-431): the manual type change
// POST /cis/{id}/type checks the unique attributes of the target type as
// POST /cis does. A value the CI already carries falls under a unique
// attribute of the target type, and another active CI of that type holds
// it. Through the complete server.NewRouter (authorization and OpenAPI
// validation, newAuditT3Router) against PostgreSQL, as an org-wide
// principal:
//   - without the unique index (the flag is set, the index job has not run
//     or failed, DB-04) the change answers 409
//     urn:reticora:problem:conflict with the violation of the attribute;
//     the CI keeps its type, one CI of the target type holds the value, and
//     the index job then builds the index;
//   - with the index built, the change answers the same 409 with the
//     attribute's key;
//   - a CI whose value is free changes its type (200).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run '^TestUniqueAttributeTypeChange$' ./internal/server/

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

func TestUniqueAttributeTypeChange(t *testing.T) {
	r := newAuditT3Router(t, "7b")
	f := r.f
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	types := citype.NewPGRepository(f.App)
	newType := func(key string) *citype.Type {
		t.Helper()
		typ := &citype.Type{OrganizationID: f.OrgA, Key: key, Name: key, IsObservable: true}
		if err := types.Create(orgCtx, typ); err != nil {
			t.Fatalf("create type %s: %v", key, err)
		}
		return typ
	}
	define := func(typ *citype.Type, key string, unique bool) *citype.AttributeDefinition {
		t.Helper()
		d := &citype.AttributeDefinition{CITypeID: typ.ID, Key: key, DataType: "string", Unique: unique}
		if unique {
			d.UniqueScope = "org"
		}
		if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, d); err != nil {
			t.Fatalf("define %s on %s: %v", key, typ.Key, err)
		}
		t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(d.ID, true)) })
		return d
	}
	// The source type carries both keys without the flag; the target type
	// defines them as unique: asset_tag without the index (the index job
	// has not run, DB-04), inventory_no with it.
	src, dst := newType("wp702-src"), newType("wp702-dst")
	define(src, "asset_tag", false)
	define(src, "inventory_no", false)
	tag := define(dst, "asset_tag", true)
	indexed := define(dst, "inventory_no", true)
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	if err := ddl.Ensure(bg, f.OrgA, indexed.ID); err != nil {
		t.Fatalf("index inventory_no: %v", err)
	}

	all := identity.AllPermissions()
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		return r.call(all, http.MethodPost, path, body)
	}
	createCI := func(name string, typ *citype.Type, attrs string) string {
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
	changeType := func(id string) *httptest.ResponseRecorder {
		t.Helper()
		return post("/api/v1/cis/"+id+"/type", `{"ci_type_id":"`+dst.ID+`"}`)
	}
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
	// holders counts the active CIs of the target type holding value.
	holders := func(key, value string) string {
		return r.str(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND ci_type_id = $2
			AND attributes ->> $3::text = $4`, f.OrgA, dst.ID, key, value)
	}
	keptType := func(what, id string) {
		t.Helper()
		if typ := r.str(`SELECT ci_type_id::text FROM ci WHERE id = $1`, id); typ != src.ID {
			t.Errorf("%s: the CI changed its type to %s, want it to keep %s", what, typ, src.Key)
		}
	}

	// Without the index: the value the CI carries is held by a CI of the
	// target type.
	createCI("wp702-holder", dst, `{"asset_tag":"T-1"}`)
	mover := createCI("wp702-mover", src, `{"asset_tag":"T-1"}`)
	refused("POST /cis/{id}/type", changeType(mover), "asset_tag")
	if n := holders("asset_tag", "T-1"); n != "1" {
		t.Errorf("type change: %s active CIs of the target type hold asset_tag T-1, want 1", n)
	}
	keptType("type change refused", mover)
	if err := ddl.Ensure(bg, f.OrgA, tag.ID); err != nil {
		t.Errorf("index job for asset_tag after the refused type change: %v", err)
	}

	// With the index the answer is the same and names the attribute.
	createCI("wp702-indexed-holder", dst, `{"inventory_no":"I-1"}`)
	indexedMover := createCI("wp702-indexed-mover", src, `{"inventory_no":"I-1"}`)
	refused("POST /cis/{id}/type with the index", changeType(indexedMover), "inventory_no")
	keptType("type change with the index refused", indexedMover)

	// Control: a free value changes the type.
	free := createCI("wp702-free", src, `{"asset_tag":"T-2","inventory_no":"I-2"}`)
	if w := changeType(free); w.Code != http.StatusOK {
		t.Errorf("POST /cis/{id}/type with free values: %d %.300s, want 200", w.Code, w.Body.String())
	}
}
