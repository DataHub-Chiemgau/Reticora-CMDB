package ci_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestUniqueAttributes covers WP-142 (MET-15, DB-04) against PostgreSQL:
// the CI service refuses a value of a unique attribute that another
// active CI holds (409) per organization or, with scope client, per
// client; soft-deleted CIs do not count, and reactivating one whose value
// was taken in the meantime is refused; parallel writes past the check
// are held off by the unique index (exactly one wins, the others get the
// conflict of the attribute).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUniqueAttributes(t *testing.T) {
	f := scopetest.Seed(t, "9f")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	types := citype.NewPGRepository(f.App)
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))

	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp142-ups", Name: "wp142-ups", IsObservable: true}
	if err := types.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	define := func(key, scope string) *citype.AttributeDefinition {
		t.Helper()
		d := &citype.AttributeDefinition{CITypeID: typ.ID, Key: key, DataType: "string", Unique: true, UniqueScope: scope}
		if err := types.CreateAttributeDefinition(ctx, f.OrgA, d); err != nil {
			t.Fatalf("define %s: %v", key, err)
		}
		t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(d.ID, true)) })
		if err := ddl.Ensure(bg, f.OrgA, d.ID); err != nil {
			t.Fatalf("index %s: %v", key, err)
		}
		return d
	}
	define("inventory_no", "org")
	define("slot", "client")

	repo := ci.NewPGRepository(f.App)
	svc := ci.NewService(repo)
	create := func(name, client string, attrs map[string]any) (*ci.Item, error) {
		item := &ci.Item{OrganizationID: f.OrgA, ClientID: client, CITypeID: typ.ID, Name: name, Status: "active", Attributes: attrs}
		return item, svc.Create(ctx, item)
	}
	taken := func(err error, field string) bool {
		var ue *ci.UniqueError
		return errors.As(err, &ue) && len(ue.Fields) == 1 && ue.Fields[0] == field
	}

	first, err := create("wp142-a", f.Client1, map[string]any{"inventory_no": "INV-1", "slot": "S1"})
	if err != nil {
		t.Fatalf("first CI: %v", err)
	}
	if _, err = create("wp142-b", f.Client2, map[string]any{"inventory_no": "INV-1"}); !taken(err, "inventory_no") {
		t.Fatalf("same inventory_no in the organization: %v, want a conflict on inventory_no", err)
	}
	// Scope client: another client may use the slot, the same client not.
	if _, err = create("wp142-c", f.Client2, map[string]any{"inventory_no": "INV-2", "slot": "S1"}); err != nil {
		t.Fatalf("same slot in another client: %v", err)
	}
	if _, err = create("wp142-d", f.Client1, map[string]any{"inventory_no": "INV-3", "slot": "S1"}); !taken(err, "slot") {
		t.Fatalf("same slot in the client: %v, want a conflict on slot", err)
	}
	// Updates are checked against the merged attributes, the CI itself
	// does not count.
	if _, err = svc.Update(ctx, f.OrgA, first.ID, ci.UpdateRequest{Attributes: map[string]any{"slot": "S1", "note": "x"}}); err != nil {
		t.Fatalf("update keeping its own values: %v", err)
	}
	second, err := create("wp142-e", f.Client1, map[string]any{"inventory_no": "INV-4"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = svc.Update(ctx, f.OrgA, second.ID, ci.UpdateRequest{Attributes: map[string]any{"inventory_no": "INV-1"}}); !taken(err, "inventory_no") {
		t.Fatalf("update to a taken value: %v, want a conflict", err)
	}

	// Soft-deleted CIs release their values; reactivation is refused once
	// the value is taken again.
	if _, err = svc.Delete(ctx, f.OrgA, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = create("wp142-f", f.Client1, map[string]any{"inventory_no": "INV-1"}); err != nil {
		t.Fatalf("value of a soft-deleted CI: %v", err)
	}
	if _, err = repo.Restore(ctx, f.OrgA, first.ID); !taken(err, "inventory_no") {
		t.Fatalf("restore with a taken value: %v, want a conflict on inventory_no", err)
	}

	// Parallel writes past the service check: the index lets one through.
	const writers = 8
	var wg sync.WaitGroup
	errs := make([]error, writers)
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			item := &ci.Item{OrganizationID: f.OrgA, CITypeID: typ.ID, Name: fmt.Sprintf("wp142-p%d", i), Status: "active",
				Attributes: map[string]any{"inventory_no": "INV-P"}}
			errs[i] = repo.Create(ctx, item)
		}(i)
	}
	wg.Wait()
	won := 0
	for _, e := range errs {
		switch {
		case e == nil:
			won++
		case !taken(e, "inventory_no"):
			t.Errorf("parallel write: %v, want a conflict on inventory_no", e)
		}
	}
	if won != 1 {
		t.Fatalf("%d of %d parallel writes stored the same value, want 1", won, writers)
	}
}

// TestUniqueAttributeOrgScopeForClientWriter covers WP-692 (MET-15, WP-142
// AK2; F-421) against PostgreSQL: a unique attribute with scope org holds
// for a writer restricted to client 1 also while the unique index does not
// exist yet (the flag is set, the index job has not run). The value held by
// a CI of client 2, which the writer does not see, is refused with the
// conflict of the attribute (409) on create and update, without naming the
// other CI; nothing is stored, and the index job then builds the index.
// With scope client the value of another client stays free, and two CIs of
// one transaction of the writer cannot share a value either.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUniqueAttributeOrgScopeForClientWriter(t *testing.T) {
	f := scopetest.Seed(t, "9f")
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	c1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User, ClientID: f.Client1})
	types := citype.NewPGRepository(f.App)
	ddl := citype.NewDDLJob(f.App, jobs.NewService(jobs.NewPGRepository(f.App)))
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp692-pdu", Name: "wp692-pdu", IsObservable: true}
	if err := types.Create(orgCtx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	// The flags only: the index job has not run (DB-04 window).
	var serial *citype.AttributeDefinition
	for key, scope := range map[string]string{"serial": "org", "slot": "client"} {
		d := &citype.AttributeDefinition{CITypeID: typ.ID, Key: key, DataType: "string", Unique: true, UniqueScope: scope}
		if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, d); err != nil {
			t.Fatalf("define %s: %v", key, err)
		}
		t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(d.ID, true)) })
		if key == "serial" {
			serial = d
		}
	}
	svc := ci.NewService(ci.NewPGRepository(f.App))
	create := func(ctx context.Context, name, client string, attrs map[string]any) (*ci.Item, error) {
		item := &ci.Item{OrganizationID: f.OrgA, ClientID: client, CITypeID: typ.ID, Name: name, Status: "active", Attributes: attrs}
		return item, svc.Create(ctx, item)
	}
	taken := func(err error, field string) bool {
		var ue *ci.UniqueError
		return errors.As(err, &ue) && len(ue.Fields) == 1 && ue.Fields[0] == field
	}
	holders := func(key, value string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND ci_type_id = $2
			AND attributes ->> $3 = $4 AND deleted_at IS NULL`, f.OrgA, typ.ID, key, value).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	foreign, err := create(orgCtx, "wp692-foreign", f.Client2, map[string]any{"serial": "SN-1", "slot": "S-1"})
	if err != nil {
		t.Fatalf("org-wide writer, CI of client 2: %v", err)
	}
	foreign2, err := create(orgCtx, "wp692-foreign-2", f.Client2, map[string]any{"serial": "SN-4"})
	if err != nil {
		t.Fatalf("org-wide writer, second CI of client 2: %v", err)
	}
	leak := func(what string, err error) {
		t.Helper()
		for _, other := range []*ci.Item{foreign, foreign2} {
			if err != nil && (strings.Contains(err.Error(), other.ID) || strings.Contains(err.Error(), other.Name)) {
				t.Errorf("%s: %q names a CI of client 2", what, err)
			}
		}
	}
	_, err = create(c1Ctx, "wp692-c1-new", f.Client1, map[string]any{"serial": "SN-1"})
	leak("create", err)
	if !taken(err, "serial") {
		t.Errorf("client-1 writer creates a CI with the serial of a CI of client 2: %v, want a conflict on serial", err)
	}
	own, err := create(c1Ctx, "wp692-c1-own", f.Client1, map[string]any{"serial": "SN-2"})
	if err != nil {
		t.Fatalf("client-1 writer, free serial: %v", err)
	}
	_, err = svc.Update(c1Ctx, f.OrgA, own.ID, ci.UpdateRequest{Attributes: map[string]any{"serial": "SN-4"}})
	leak("update", err)
	if !taken(err, "serial") {
		t.Errorf("client-1 writer changes its CI to the serial of a CI of client 2: %v, want a conflict on serial", err)
	}
	for _, serial := range []string{"SN-1", "SN-4"} {
		if n := holders("serial", serial); n != 1 {
			t.Errorf("%d active CIs hold serial %s, want only the CI of client 2", n, serial)
		}
	}
	// Scope client: the slot of client 2 is free in client 1.
	if _, err = create(c1Ctx, "wp692-c1-slot", f.Client1, map[string]any{"slot": "S-1"}); err != nil {
		t.Errorf("client-1 writer, slot of client 2: %v, want it free in client 1", err)
	}
	// One transaction of the writer: its second CI sees the first.
	err = database.WithRequestTenantTx(c1Ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if _, cerr := create(ctx, "wp692-tx-1", f.Client1, map[string]any{"serial": "SN-3"}); cerr != nil {
			return cerr
		}
		_, cerr := create(ctx, "wp692-tx-2", f.Client1, map[string]any{"serial": "SN-3"})
		return cerr
	})
	if !taken(err, "serial") {
		t.Errorf("two CIs with serial SN-3 in one transaction: %v, want a conflict on serial", err)
	}
	// Nothing to undo: the index job builds the index.
	if err = ddl.Ensure(bg, f.OrgA, serial.ID); err != nil {
		t.Errorf("index job after the refused writes: %v", err)
	}
}

// TestUniqueAttributeRestoreForClientWriter covers WP-700 (MET-15; F-429)
// against PostgreSQL: the restore checks the unique attributes as the
// service checks a create, also while the unique index does not exist yet
// and for a writer restricted to client 1. The value of its deleted CI,
// taken meanwhile by a CI of client 2 the writer does not see, refuses the
// restore with the conflict of the attribute without naming the other CI;
// the CI stays deleted.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUniqueAttributeRestoreForClientWriter(t *testing.T) {
	f := scopetest.Seed(t, "9f")
	bg := context.Background()
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	c1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User, ClientID: f.Client1})
	types := citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp700-rack", Name: "wp700-rack", IsObservable: true}
	if err := types.Create(orgCtx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	// The flag only: the index job has not run (DB-04 window).
	d := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "serial", DataType: "string", Unique: true, UniqueScope: "org"}
	if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, d); err != nil {
		t.Fatalf("define serial: %v", err)
	}
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+citype.IndexName(d.ID, true)) })
	svc := ci.NewService(ci.NewPGRepository(f.App))
	own := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: typ.ID, Name: "wp700-own", Status: "active",
		Attributes: map[string]any{"serial": "SN-7"}}
	if err := svc.Create(c1Ctx, own); err != nil {
		t.Fatalf("client-1 CI: %v", err)
	}
	if _, err := svc.Delete(c1Ctx, f.OrgA, own.ID); err != nil {
		t.Fatal(err)
	}
	foreign := &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client2, CITypeID: typ.ID, Name: "wp700-foreign", Status: "active",
		Attributes: map[string]any{"serial": "SN-7"}}
	if err := svc.Create(orgCtx, foreign); err != nil {
		t.Fatalf("org-wide writer, CI of client 2 with the released serial: %v", err)
	}
	_, err := svc.Restore(c1Ctx, f.OrgA, own.ID)
	var ue *ci.UniqueError
	if !errors.As(err, &ue) || len(ue.Fields) != 1 || ue.Fields[0] != "serial" {
		t.Errorf("client-1 writer restores its CI with the serial a CI of client 2 took: %v, want a conflict on serial", err)
	}
	if err != nil && (strings.Contains(err.Error(), foreign.ID) || strings.Contains(err.Error(), foreign.Name)) {
		t.Errorf("restore: %q names the CI of client 2", err)
	}
	var deleted bool
	if qerr := f.Admin.QueryRow(bg, `SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1`, own.ID).Scan(&deleted); qerr != nil || !deleted {
		t.Errorf("restore refused: deleted=%v (%v), want the CI to stay deleted", deleted, qerr)
	}
}
