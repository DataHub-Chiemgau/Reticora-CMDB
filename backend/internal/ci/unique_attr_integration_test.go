package ci_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
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
