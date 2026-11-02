package discovery

// WP-700 (MET-15, ENT-03; F-429): the adoption of the held devices after
// max_cis was raised (AdoptUnlicensed) checks the unique attributes as
// POST /cis does, also while the unique index does not exist yet. A held
// device whose attribute value another active CI holds is not adopted, its
// item stays open, and the items after it are still adopted.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run '^TestAdoptUnlicensedChecksUniqueAttributes$' ./internal/discovery/

import (
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

func TestAdoptUnlicensedChecksUniqueAttributes(t *testing.T) {
	f := scopetest.Seed(t, "7b")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	types := citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp700-pdu", Name: "wp700-pdu", IsObservable: true}
	if err := types.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	// The flag only: the index job has not run (DB-04 window).
	d := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "asset_tag", DataType: "string", Unique: true, UniqueScope: "org"}
	if err := types.CreateAttributeDefinition(ctx, f.OrgA, d); err != nil {
		t.Fatalf("define asset_tag: %v", err)
	}
	ciRepo := ci.NewPGRepository(f.App)
	holder := &ci.Item{OrganizationID: f.OrgA, CITypeID: typ.ID, Name: "wp700-holder", Status: "active",
		Attributes: map[string]any{"asset_tag": "A-1"}}
	if err := ci.NewService(ciRepo).Create(ctx, holder); err != nil {
		t.Fatalf("holder: %v", err)
	}
	str := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(t.Context(), sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	held := func(name, tag, age string) string {
		return str(`INSERT INTO review_item (organization_id, kind, status, payload, created_at) VALUES ($1, 'unlicensed_ci', 'open',
			jsonb_build_object('ci_type_id', $2::text, 'source', 'sweep', 'device',
				jsonb_build_object('name', $3::text, 'attributes', jsonb_build_object('asset_tag', $4::text))), now() - $5::interval)
			RETURNING id::text`, f.OrgA, typ.ID, name, tag, age)
	}
	// Oldest first: the item with the taken value comes first.
	taken := held("wp700-held-taken", "A-1", "2 minutes")
	free := held("wp700-held-free", "A-2", "1 minute")

	h := NewHandler(NewPGRepository(f.App), ciRepo)
	adopted, err := h.AdoptUnlicensed(ctx, f.OrgA)
	if err != nil || adopted != 1 {
		t.Errorf("AdoptUnlicensed: %d, %v, want 1 adopted and no error", adopted, err)
	}
	for _, c := range []struct{ id, want string }{{taken, "open"}, {free, "resolved"}} {
		if got := str(`SELECT status FROM review_item WHERE id = $1`, c.id); got != c.want {
			t.Errorf("item %s: %s, want %s", c.id, got, c.want)
		}
	}
	for _, tag := range []string{"A-1", "A-2"} {
		if n := str(`SELECT count(*)::text FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND attributes ->> 'asset_tag' = $2`,
			f.OrgA, tag); n != "1" {
			t.Errorf("%s active CIs hold asset_tag %s, want 1", n, tag)
		}
	}
}
