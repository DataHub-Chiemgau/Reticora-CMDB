package ci_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// countingLookup counts the lookups of the reference check per target.
type countingLookup struct {
	inner fieldmeta.ReferenceLookup
	mu    sync.Mutex
	calls map[string]int
}

func (c *countingLookup) LookupReferences(ctx context.Context, orgID, target string, ids []string) (map[string]string, error) {
	c.mu.Lock()
	c.calls[target]++
	c.mu.Unlock()
	return c.inner.LookupReferences(ctx, orgID, target, ids)
}

// total returns the number of lookups so far.
func (c *countingLookup) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, v := range c.calls {
		n += v
	}
	return n
}

// refViolation returns the detail of the violation of field, "" when err
// does not refuse it.
func refViolation(err error, field string) string {
	var v *ci.ValidationError
	if !errors.As(err, &v) {
		return ""
	}
	for _, violation := range v.Violations {
		if violation.Field == field {
			return violation.Detail
		}
	}
	return ""
}

// TestReferenceAttributes covers WP-254 (MET-11, MET-12) against
// PostgreSQL: reference values (reference, the alias types, file, lists
// with multi_value, instance attributes) name a target of the same
// organization that exists and is visible in the caller's scope; the
// client of the target is compatible with the client of the CI (one of
// them org-wide, or the same). An invisible target is not found exactly
// like a missing one. Only values the write sets or changes are looked up,
// with one statement per target; a change of the client checks all of
// them; an unchanged value and a bulk set_status look nothing up.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReferenceAttributes(t *testing.T) {
	f := scopetest.Seed(t, "0b")
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	typeID := serverType(t, f)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}

	// Targets: CIs per client, org-wide and in organization B; deleted and
	// merged CIs; active and inactive users; teams; assets; documents;
	// locations of every kind in both clients and in organization B.
	ciC1 := f.CI(t, f.OrgA, f.Client1, "wp254-c1")
	ciC2 := f.CI(t, f.OrgA, f.Client2, "wp254-c2")
	ciOrg := f.CI(t, f.OrgA, "", "wp254-org")
	ciB := f.CI(t, f.OrgB, "", "wp254-b")
	ciDeleted := f.CI(t, f.OrgA, "", "wp254-deleted")
	exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, ciDeleted)
	ciMerged := f.CI(t, f.OrgA, "", "wp254-merged")
	exec(`UPDATE ci SET deleted_at = now(), merged_into_id = $2 WHERE id = $1`, ciMerged, ciOrg)
	userA := f.AppUser(t, f.OrgA, "wp254-a")
	userInactive := f.AppUser(t, f.OrgA, "wp254-inactive")
	exec(`UPDATE app_user SET is_active = false WHERE id = $1`, userInactive)
	userB := f.AppUser(t, f.OrgB, "wp254-b")
	teamA := scalar(`INSERT INTO team (organization_id, name) VALUES ($1, 'wp254 team') RETURNING id::text`, f.OrgA)
	teamB := scalar(`INSERT INTO team (organization_id, name) VALUES ($1, 'wp254 team') RETURNING id::text`, f.OrgB)
	assetOrg := f.Asset(t, f.OrgA, "", "WP254-ORG")
	assetC2 := f.Asset(t, f.OrgA, f.Client2, "WP254-C2")
	docA := scalar(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
		VALUES ($1, 'wp254', 'wp254.pdf', '', $2) RETURNING id::text`, f.OrgA, userA)
	docB := scalar(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
		VALUES ($1, 'wp254', 'wp254.pdf', '', $2) RETURNING id::text`, f.OrgB, userB)

	tree := locations.NewPGRepository(f.App)
	newLocation := func(ctx context.Context, org string, req locations.CreateRequest) string {
		t.Helper()
		loc, err := tree.Create(ctx, org, req)
		if err != nil {
			t.Fatalf("create %s: %v", req.Kind, err)
		}
		return loc.ID
	}
	// kinds builds a site and a node of every kind below it.
	kinds := func(ctx context.Context, org, client, prefix string) map[locations.Kind]string {
		site := newLocation(ctx, org, locations.CreateRequest{Kind: locations.KindSite, ClientID: client, Name: prefix + "-site"})
		out := map[locations.Kind]string{locations.KindSite: site}
		for _, chain := range [][]locations.Kind{
			{locations.KindBuilding, locations.KindRoom, locations.KindRack},
			{locations.KindWarehouse, locations.KindZone, locations.KindShelf, locations.KindBin},
		} {
			parent := site
			for _, kind := range chain {
				parent = newLocation(ctx, org, locations.CreateRequest{Kind: kind, ParentID: parent, Name: prefix + "-" + string(kind)})
				out[kind] = parent
			}
		}
		return out
	}
	locC1 := kinds(orgCtx, f.OrgA, f.Client1, "wp254-c1")
	locC2 := kinds(orgCtx, f.OrgA, f.Client2, "wp254-c2")
	clientB := scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'wp254 b', 'wp254-b') RETURNING id::text`, f.OrgB)
	orgBCtx := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: f.User})
	locB := kinds(orgBCtx, f.OrgB, clientB, "wp254-b")

	// Global attribute definitions of organization A.
	types := citype.NewPGRepository(f.App)
	yes := true
	for _, d := range []citype.AttributeDefinition{
		{Key: "wp254_host", DataType: fieldmeta.TypeCIRef},
		{Key: "wp254_hosts", DataType: fieldmeta.TypeReference, ReferenceTarget: "ci", MultiValue: yes},
		{Key: "wp254_owner", DataType: fieldmeta.TypeUser},
		{Key: "wp254_team", DataType: fieldmeta.TypeTeam},
		{Key: "wp254_place", DataType: fieldmeta.TypeLocation},
		{Key: "wp254_asset", DataType: fieldmeta.TypeAssetRef},
		{Key: "wp254_manual", DataType: fieldmeta.TypeFile},
	} {
		if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, &d); err != nil {
			t.Fatalf("definition %s: %v", d.Key, err)
		}
	}

	// The lookup runs on its own traced pool: its statements are counted.
	cfg := f.App.Config()
	trace := &statementLog{}
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	tracedPool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(tracedPool.Close)
	lookup := &countingLookup{inner: ci.NewPGReferenceLookup(tracedPool), calls: map[string]int{}}
	svc := ci.NewService(ci.NewPGRepository(f.App)).WithFieldResolver(types).WithReferences(lookup)
	// lookupStatements counts the lookup statements since mark.
	lookupStatements := func(mark int) int {
		n := 0
		for _, s := range trace.since(mark) {
			if strings.Contains(s.sql, "id = ANY($2::uuid[])") {
				n++
			}
		}
		return n
	}

	seq := 0
	create := func(ctx context.Context, client string, attrs map[string]any) (*ci.Item, error) {
		seq++
		item := &ci.Item{OrganizationID: f.OrgA, ClientID: client, CITypeID: typeID, Status: "active",
			Name: fmt.Sprintf("wp254-new-%d", seq), Attributes: attrs}
		return item, svc.Create(ctx, item)
	}
	mustCreate := func(ctx context.Context, client string, attrs map[string]any) *ci.Item {
		t.Helper()
		item, cerr := create(ctx, client, attrs)
		if cerr != nil {
			t.Fatalf("create with %v: %v", attrs, cerr)
		}
		return item
	}

	// Valid references of every target, one statement per target.
	mark, calls := trace.mark(), lookup.total()
	mustCreate(orgCtx, f.Client1, map[string]any{
		"wp254_host": ciC1, "wp254_hosts": []any{ciC1, ciOrg}, "wp254_owner": userA, "wp254_team": teamA,
		"wp254_place": locC1[locations.KindRoom], "wp254_asset": assetOrg, "wp254_manual": strings.ToUpper(docA),
	})
	if got := lookup.total() - calls; got != 6 {
		t.Errorf("lookups of a create with six targets: %d, want 6 (one per target)", got)
	}
	if got := lookupStatements(mark); got != 6 {
		t.Errorf("lookup statements of a create with six targets: %d, want 6", got)
	}

	for _, tc := range []struct {
		name   string
		ctx    context.Context
		client string
		attrs  map[string]any
		field  string
		detail string // "" when accepted
	}{
		{"CI of organization B", orgCtx, f.Client1, map[string]any{"wp254_host": ciB}, "wp254_host", "reference target not found"},
		{"user of organization B", orgCtx, f.Client1, map[string]any{"wp254_owner": userB}, "wp254_owner", "reference target not found"},
		{"team of organization B", orgCtx, "", map[string]any{"wp254_team": teamB}, "wp254_team", "reference target not found"},
		{"location of organization B", orgCtx, "", map[string]any{"wp254_place": locB[locations.KindSite]}, "wp254_place", "reference target not found"},
		{"document of organization B", orgCtx, "", map[string]any{"wp254_manual": docB}, "wp254_manual", "reference target not found"},
		{"unknown id", orgCtx, "", map[string]any{"wp254_host": "c0de0b00-0000-4000-8000-00000000dead"}, "wp254_host", "reference target not found"},
		{"CI of another client", orgCtx, f.Client1, map[string]any{"wp254_host": ciC2}, "wp254_host", "reference target belongs to another client"},
		{"location of another client", orgCtx, f.Client1, map[string]any{"wp254_place": locC2[locations.KindSite]}, "wp254_place",
			"reference target belongs to another client"},
		{"asset of another client", orgCtx, f.Client1, map[string]any{"wp254_asset": assetC2}, "wp254_asset", "reference target belongs to another client"},
		{"org-wide CI with a client target", orgCtx, "", map[string]any{"wp254_host": ciC2, "wp254_place": locC2[locations.KindRack]}, "", ""},
		{"client CI with an org-wide target", orgCtx, f.Client1, map[string]any{"wp254_host": ciOrg, "wp254_asset": assetOrg}, "", ""},
		// A CI of client 2 does not exist for a principal of client 1: the
		// same answer as an unknown id.
		{"invisible target", client1Ctx, f.Client1, map[string]any{"wp254_host": ciC2}, "wp254_host", "reference target not found"},
		{"invisible location", client1Ctx, f.Client1, map[string]any{"wp254_place": locC2[locations.KindBin]}, "wp254_place", "reference target not found"},
		{"visible target in scope", client1Ctx, f.Client1, map[string]any{"wp254_host": ciC1, "wp254_place": locC1[locations.KindShelf]}, "", ""},
		{"soft-deleted CI", orgCtx, "", map[string]any{"wp254_host": ciDeleted}, "wp254_host", "reference target not found"},
		{"merged CI", orgCtx, "", map[string]any{"wp254_host": ciMerged}, "wp254_host", "reference target not found"},
		{"inactive user", orgCtx, "", map[string]any{"wp254_owner": userInactive}, "wp254_owner", "reference target not found"},
		{"list of two valid ids", orgCtx, f.Client1, map[string]any{"wp254_hosts": []any{ciC1, ciOrg}}, "", ""},
		{"list with an id of organization B", orgCtx, f.Client1, map[string]any{"wp254_hosts": []any{ciC1, ciB}}, "wp254_hosts", "reference target not found"},
		{"list with a repeated id", orgCtx, f.Client1, map[string]any{"wp254_hosts": []any{ciC1, ciC1}}, "wp254_hosts", "is listed twice"},
		{"invalid form", orgCtx, "", map[string]any{"wp254_owner": "srv-01"}, "wp254_owner", "expected the UUID"},
	} {
		_, cerr := create(tc.ctx, tc.client, tc.attrs)
		switch {
		case tc.detail == "" && cerr != nil:
			t.Errorf("%s: refused: %v", tc.name, cerr)
		case tc.detail != "" && !strings.Contains(refViolation(cerr, tc.field), tc.detail):
			t.Errorf("%s: err = %v, want %q on %s", tc.name, cerr, tc.detail, tc.field)
		}
	}
	// A location of every kind is a target (CH28, LOC-10).
	for kind, id := range locC1 {
		if _, cerr := create(orgCtx, f.Client1, map[string]any{"wp254_place": id}); cerr != nil {
			t.Errorf("location of kind %s: %v", kind, cerr)
		}
	}

	// An instance attribute (_instance) is checked like a type attribute.
	holder := mustCreate(orgCtx, f.Client1, nil)
	if _, err = types.UpsertInstanceField(orgCtx, f.OrgA, holder.ID, &citype.UpsertInstanceFieldRequest{
		Name: "wp254_backup", DataType: fieldmeta.TypeCIRef}); err != nil {
		t.Fatalf("instance field: %v", err)
	}
	if _, err = svc.Update(orgCtx, f.OrgA, holder.ID, ci.UpdateRequest{Attributes: map[string]any{"wp254_backup": ciB}}); refViolation(err, "wp254_backup") != "reference target not found" {
		t.Errorf("instance attribute with a CI of organization B: %v", err)
	}
	if _, err = svc.Update(orgCtx, f.OrgA, holder.ID, ci.UpdateRequest{Attributes: map[string]any{"wp254_backup": ciC2}}); refViolation(err, "wp254_backup") != "reference target belongs to another client" {
		t.Errorf("instance attribute with a CI of another client: %v", err)
	}
	if _, err = svc.Update(orgCtx, f.OrgA, holder.ID, ci.UpdateRequest{Attributes: map[string]any{"wp254_backup": ciC1}}); err != nil {
		t.Errorf("instance attribute with a CI of the same client: %v", err)
	}

	// A change of the client checks every reference value against the new
	// client, also those the write does not name.
	moved := mustCreate(orgCtx, "", map[string]any{"wp254_host": ciC2, "wp254_owner": userA})
	client1 := f.Client1
	if _, err = svc.Update(orgCtx, f.OrgA, moved.ID, ci.UpdateRequest{ClientID: &client1}); refViolation(err, "wp254_host") != "reference target belongs to another client" {
		t.Errorf("client change to client 1 with a reference into client 2: %v", err)
	}
	client2 := f.Client2
	if _, err = svc.Update(orgCtx, f.OrgA, moved.ID, ci.UpdateRequest{ClientID: &client2}); err != nil {
		t.Errorf("client change to the client of the target: %v", err)
	}

	// An unchanged reference value, a write of other fields and a bulk
	// set_status look nothing up.
	mark, calls = trace.mark(), lookup.total()
	model := "wp254-model"
	if _, err = svc.Update(orgCtx, f.OrgA, moved.ID, ci.UpdateRequest{Attributes: map[string]any{"wp254_host": ciC2}, Model: &model}); err != nil {
		t.Errorf("unchanged reference: %v", err)
	}
	status := "maintenance"
	if _, err = svc.Update(orgCtx, f.OrgA, moved.ID, ci.UpdateRequest{Status: &status}); err != nil {
		t.Errorf("status change: %v", err)
	}
	result, _, err := svc.Bulk(orgCtx, f.OrgA, &ci.BulkRequest{Action: ci.BulkSetStatus, IDs: []string{moved.ID, holder.ID}, Status: "active"})
	if err != nil || result == nil || result.Succeeded != 2 {
		t.Errorf("bulk set_status: %+v %v", result, err)
	}
	if got := lookup.total() - calls; got != 0 {
		t.Errorf("lookups without a new reference value: %d, want 0", got)
	}
	if got := lookupStatements(mark); got != 0 {
		t.Errorf("lookup statements without a new reference value: %d, want 0", got)
	}

	// Without the module inventory asset is no target (E-03): the write is
	// refused without a lookup.
	fieldmeta.SetReferenceModules(func(module string) bool { return module != "inventory" })
	t.Cleanup(func() { fieldmeta.SetReferenceModules(nil) })
	calls = lookup.total()
	if _, cerr := create(orgCtx, "", map[string]any{"wp254_asset": assetOrg}); refViolation(cerr, "wp254_asset") != "reference target asset not available" {
		t.Errorf("asset without inventory: %v", cerr)
	}
	if lookup.total() != calls {
		t.Error("an unavailable target was looked up")
	}
}
