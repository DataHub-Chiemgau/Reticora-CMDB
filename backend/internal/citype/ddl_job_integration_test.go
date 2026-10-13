package citype

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// indexState is an attribute index as the catalog shows it.
type indexState struct {
	found, valid, unique bool
	def                  string
}

func readIndex(t *testing.T, admin *pgxpool.Pool, name string) indexState {
	t.Helper()
	var s indexState
	err := admin.QueryRow(context.Background(), `
		SELECT true, i.indisvalid, i.indisunique, pg_get_indexdef(c.oid)
		FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid WHERE c.relname = $1`, name).
		Scan(&s.found, &s.valid, &s.unique, &s.def)
	if err != nil && !strings.Contains(err.Error(), "no rows") {
		t.Fatal(err)
	}
	return s
}

// TestAttributeIndexJob covers WP-142 (DB-04, MET-15, CH19) against
// PostgreSQL: setting searchable or unique on a definition queues an
// attribute.index job; the job builds the index concurrently outside a
// transaction (CREATE INDEX CONCURRENTLY fails inside one) as
// idx_attr_<id> or idx_attr_<id>_u (E-04), over the organization (and the
// client for scope client) and the attribute value, without soft-deleted
// CIs; a changed scope rebuilds it, a build that fails on duplicate values
// drops its invalid index and retries, and the index of a deleted
// definition is dropped. Making an attribute unique while CIs share a value
// is refused with 409.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeIndexJob(t *testing.T) {
	f := scopetest.Seed(t, "9e")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	ddl := NewDDLJob(f.App, svc)
	repo := NewPGRepository(f.App).WithIndexScheduler(ddl)

	typ := &Type{OrganizationID: f.OrgA, Key: "wp142-switch", Name: "wp142-switch", IsObservable: true}
	if err := repo.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	def := &AttributeDefinition{CITypeID: typ.ID, Key: "asset_tag", DataType: "string", Searchable: true}
	if err := repo.CreateAttributeDefinition(ctx, f.OrgA, def); err != nil {
		t.Fatalf("create definition: %v", err)
	}
	plain, uniq := IndexName(def.ID, false), IndexName(def.ID, true)
	t.Cleanup(func() {
		for _, name := range []string{plain, uniq} {
			_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+name)
		}
	})
	if want := "idx_attr_" + strings.ReplaceAll(def.ID, "-", ""); plain != want || uniq != want+"_u" || len(uniq) > 63 {
		t.Fatalf("index names %q %q", plain, uniq)
	}

	queued := func() []jobs.Job {
		t.Helper()
		list, _, err := svc.List(bg, f.OrgA, jobs.Filter{Kind: IndexJobKind}, api.PaginationParams{Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		return list
	}
	run := func() {
		t.Helper()
		if _, err := ddl.RunDue(bg); err != nil {
			t.Fatalf("run index jobs: %v", err)
		}
	}
	if list := queued(); len(list) != 1 || list[0].Status != jobs.StatusQueued {
		t.Fatalf("jobs after create = %+v, want one queued", list)
	}
	run()
	if list := queued(); list[0].Status != jobs.StatusSucceeded {
		t.Fatalf("job = %s %s, want succeeded", list[0].Status, list[0].Error)
	}
	got := readIndex(t, f.Admin, plain)
	if !got.found || !got.valid || got.unique || !strings.Contains(got.def, "deleted_at IS NULL") ||
		!strings.Contains(got.def, "'asset_tag'") || !strings.Contains(got.def, typ.ID) {
		t.Fatalf("searchable index = %+v", got)
	}

	// A change that does not touch the index queues nothing.
	desc := "inventory label"
	if _, err := repo.UpdateAttributeDefinition(ctx, f.OrgA, def.ID, &AttributeDefinitionPatch{Description: &desc}); err != nil {
		t.Fatal(err)
	}
	if n := len(queued()); n != 1 {
		t.Fatalf("%d jobs after a description change, want 1", n)
	}

	// unique: the plain index gives way to the unique one.
	yes := true
	if _, err := repo.UpdateAttributeDefinition(ctx, f.OrgA, def.ID, &AttributeDefinitionPatch{Unique: &yes}); err != nil {
		t.Fatal(err)
	}
	run()
	if got = readIndex(t, f.Admin, uniq); !got.valid || !got.unique || strings.Contains(got.def, "client_id") {
		t.Fatalf("unique index = %+v", got)
	}
	if readIndex(t, f.Admin, plain).found {
		t.Fatal("the plain index survived the switch to unique")
	}

	// Scope client: the index is rebuilt over the client, NULLS NOT DISTINCT.
	client := "client"
	if _, err := repo.UpdateAttributeDefinition(ctx, f.OrgA, def.ID, &AttributeDefinitionPatch{UniqueScope: &client}); err != nil {
		t.Fatal(err)
	}
	run()
	if got = readIndex(t, f.Admin, uniq); !got.valid || !strings.Contains(got.def, "client_id") ||
		!strings.Contains(got.def, "NULLS NOT DISTINCT") {
		t.Fatalf("client-scoped index = %+v", got)
	}

	// Duplicates written past the server-side check (here directly) make the
	// build fail: the invalid index is dropped, the job retries later.
	ciA := f.CI(t, f.OrgA, f.Client1, "wp142-a")
	ciB := f.CI(t, f.OrgA, f.Client1, "wp142-b")
	if _, err := f.Admin.Exec(bg, `DROP INDEX `+uniq); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $1, attributes = '{"asset_tag": "T-1"}' WHERE id = ANY($2)`,
		typ.ID, []string{ciA, ciB}); err != nil {
		t.Fatal(err)
	}
	err := ddl.Ensure(bg, f.OrgA, def.ID)
	if !errors.Is(err, ErrDuplicateValues) {
		t.Fatalf("build over duplicates: %v, want ErrDuplicateValues", err)
	}
	if readIndex(t, f.Admin, uniq).found {
		t.Fatal("the failed build left its invalid index behind")
	}
	// The values are unique again once a duplicate is soft-deleted.
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() WHERE id = $1`, ciB); err != nil {
		t.Fatal(err)
	}
	if err = ddl.Ensure(bg, f.OrgA, def.ID); err != nil {
		t.Fatalf("build without the soft-deleted duplicate: %v", err)
	}
	// An invalid index (a build that died) is dropped and rebuilt.
	if _, err = f.Admin.Exec(bg, `UPDATE pg_index SET indisvalid = false WHERE indexrelid = $1::regclass`, uniq); err != nil {
		t.Fatal(err)
	}
	if err = ddl.Ensure(bg, f.OrgA, def.ID); err != nil {
		t.Fatal(err)
	}
	if !readIndex(t, f.Admin, uniq).valid {
		t.Fatal("the invalid index was not rebuilt")
	}

	// Making another attribute unique while CIs share its value is refused.
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET attributes = attributes || '{"asset_tag": "T-2"}' WHERE id = $1`, ciB); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET attributes = attributes || '{"rack": "R1"}', deleted_at = NULL WHERE id = ANY($1)`,
		[]string{ciA, ciB}); err != nil {
		t.Fatal(err)
	}
	rack := &AttributeDefinition{CITypeID: typ.ID, Key: "rack", DataType: "string", Unique: true}
	err = repo.CreateAttributeDefinition(ctx, f.OrgA, rack)
	var fe *api.FieldError
	if !errors.As(err, &fe) || fe.Field != "unique" || fe.Status != 409 {
		t.Fatalf("unique over duplicates: %v, want a 409 on unique", err)
	}

	// Deleting the definition drops its index.
	if err = repo.DeleteAttributeDefinition(ctx, f.OrgA, def.ID); err != nil {
		t.Fatal(err)
	}
	run()
	if readIndex(t, f.Admin, uniq).found || readIndex(t, f.Admin, plain).found {
		t.Fatal("the index of the deleted definition survived")
	}
}
