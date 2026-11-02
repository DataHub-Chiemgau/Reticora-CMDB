package citype

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
		!strings.Contains(got.def, "reticora_attr_text(attributes, 'asset_tag'::text)") || strings.Contains(got.def, "->>") ||
		!strings.Contains(got.def, typ.ID) {
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

// legacyStatement is the CREATE INDEX statement of d as the DDL job wrote
// it before migration 000162: over attributes ->> key.
func legacyStatement(d *AttributeDefinition, name string) string {
	return strings.ReplaceAll(indexStatement(d, name), ValueExpression(quoteLiteral(d.Key)), "(attributes ->> "+quoteLiteral(d.Key)+")")
}

// indexOID is the oid of the index name, 0 when there is none.
func indexOID(t *testing.T, admin *pgxpool.Pool, name string) uint32 {
	t.Helper()
	var oid *uint32
	if err := admin.QueryRow(context.Background(), `SELECT to_regclass($1)::oid`, name).Scan(&oid); err != nil {
		t.Fatal(err)
	}
	if oid == nil {
		return 0
	}
	return *oid
}

// TestAttributeIndexRebuild covers WP-435 (E-73 option A, DB-04, MET-15):
// the indexes the DDL job built over attributes ->> key before migration
// 000162 are rebuilt over reticora_attr_text by the attribute.index.rebuild
// jobs the migration queues, under their names, with their unique
// semantics, and the old index is dropped only after the new one is valid:
//   - the rebuilt indexes hold reticora_attr_text, no ->>, keep unique,
//     client and NULLS NOT DISTINCT; a second run changes nothing (same
//     index);
//   - unique per organization and client: the same value in the same client
//     and twice without a client is refused by the index, another client, a
//     soft-deleted CI and a CI without the attribute are not;
//   - every interrupted state is completed by the next run without a
//     second build where the new index is valid: build interrupted (the old
//     index stays valid, the next run builds), build done, drop of the old
//     index interrupted (old index invalid), rename not done; a leftover
//     invalid rebuild is built again;
//   - a run waits for another run on the same definition (advisory lock)
//     and changes nothing meanwhile.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeIndexRebuild(t *testing.T) {
	f := scopetest.Seed(t, "f8")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	ddl := NewDDLJob(f.App, svc)
	// Without the scheduler: the indexes are set up as before migration
	// 000162.
	repo := NewPGRepository(f.App)

	typ := &Type{OrganizationID: f.OrgA, Key: "wp435-rebuild", Name: "wp435-rebuild", IsObservable: true}
	if err := repo.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	host := &AttributeDefinition{CITypeID: typ.ID, Key: "wp435_host", DataType: "text", Searchable: true}
	serial := &AttributeDefinition{Key: "wp435_serial", DataType: "text", Unique: true, UniqueScope: "client"}
	for _, d := range []*AttributeDefinition{host, serial} {
		if err := repo.CreateAttributeDefinition(ctx, f.OrgA, d); err != nil {
			t.Fatalf("create definition %s: %v", d.Key, err)
		}
	}
	defs := []*AttributeDefinition{host, serial}
	t.Cleanup(func() {
		for _, d := range defs {
			for _, unique := range []bool{false, true} {
				name := IndexName(d.ID, unique)
				_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+name+`, `+name+RebuildSuffix)
			}
		}
	})
	// 30 CIs: ten each in client 1, client 2 and without a client, serials
	// S0…S9 in each.
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes)
		SELECT $1, (ARRAY[$3::uuid, $4::uuid, NULL])[i / 10 + 1], $2, 'wp435-r-' || i, 'active',
			jsonb_build_object('wp435_host', 'h' || (i % 5), 'wp435_serial', 'S' || (i % 10))
		FROM generate_series(0, 29) AS i`, f.OrgA, typ.ID, f.Client1, f.Client2); err != nil {
		t.Fatalf("seed CIs: %v", err)
	}

	exec := func(sql string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// legacy replaces the indexes of d by the one built before 000162.
	legacy := func(d *AttributeDefinition) {
		t.Helper()
		name := IndexName(d.ID, d.Unique)
		exec(`DROP INDEX IF EXISTS ` + name + `, ` + name + RebuildSuffix)
		exec(legacyStatement(d, name))
		if got := readIndex(t, f.Admin, name); !got.valid || !strings.Contains(got.def, "->>") {
			t.Fatalf("legacy index = %+v", got)
		}
	}
	// current requires the index of d over reticora_attr_text, valid, with
	// its unique semantics, and no rebuild index next to it.
	current := func(d *AttributeDefinition) {
		t.Helper()
		name := IndexName(d.ID, d.Unique)
		got := readIndex(t, f.Admin, name)
		if !got.found || !got.valid || got.unique != d.Unique || strings.Contains(got.def, "->>") ||
			!strings.Contains(got.def, "reticora_attr_text(attributes, '"+d.Key+"'::text)") ||
			(d.UniqueScope == "client") != strings.Contains(got.def, "client_id") ||
			(d.UniqueScope == "client") != strings.Contains(got.def, "NULLS NOT DISTINCT") {
			t.Fatalf("index of %s = %+v, want a valid index over reticora_attr_text with the semantics of the definition", d.Key, got)
		}
		if readIndex(t, f.Admin, name+RebuildSuffix).found {
			t.Fatalf("the rebuild index of %s is left over", d.Key)
		}
	}

	// The migration queues the rebuild jobs; the DDL job runs them.
	for _, d := range defs {
		legacy(d)
		if _, _, err := svc.Enqueue(ctx, f.OrgA, RebuildJobKind, indexJobParams{DefinitionID: d.ID}, jobs.EnqueueOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if done, err := ddl.RunDue(bg); err != nil || done < 2 {
		t.Fatalf("RunDue = %d, %v; want both rebuild jobs done", done, err)
	}
	list, _, err := svc.List(bg, f.OrgA, jobs.Filter{Kind: RebuildJobKind}, api.PaginationParams{Limit: 10})
	if err != nil || len(list) != 2 || list[0].Status != jobs.StatusSucceeded || list[1].Status != jobs.StatusSucceeded {
		t.Fatalf("rebuild jobs = %+v, %v; want two succeeded", list, err)
	}
	for _, d := range defs {
		current(d)
		name := IndexName(d.ID, d.Unique)
		before := indexOID(t, f.Admin, name)
		if err = ddl.Ensure(bg, f.OrgA, d.ID); err != nil {
			t.Fatal(err)
		}
		current(d)
		if indexOID(t, f.Admin, name) != before {
			t.Errorf("a second run rebuilt the current index of %s", d.Key)
		}
	}

	// MET-15: unique per organization and client over the rebuilt index.
	serialIndex := IndexName(serial.ID, true)
	insert := func(client any, value string, deleted bool) error {
		var deletedAt any
		if deleted {
			deletedAt = "2026-01-01T00:00:00Z"
		}
		_, ierr := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, attributes, deleted_at)
			VALUES ($1, $2, $3, 'wp435-u-' || gen_random_uuid(), 'active', $4, $5::timestamptz)`,
			f.OrgA, client, typ.ID, `{"wp435_serial": "`+value+`"}`, deletedAt)
		return ierr
	}
	for _, c := range []struct {
		name    string
		client  any
		value   string
		deleted bool
		refused bool
	}{
		{"same value, same client", f.Client1, "S3", false, true},
		{"same value, both without a client", nil, "S3", false, true},
		{"same value, soft-deleted", f.Client1, "S3", true, false},
		{"new value", f.Client2, "S99", false, false},
	} {
		ierr := insert(c.client, c.value, c.deleted)
		var pgErr *pgconn.PgError
		refused := errors.As(ierr, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == serialIndex
		if refused != c.refused || (ierr != nil && !refused) {
			t.Errorf("%s: %v, want refused by %s: %v", c.name, ierr, serialIndex, c.refused)
		}
	}
	if _, err = f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status) VALUES ($1, $2, $3, 'wp435-u-none', 'active')`,
		f.OrgA, f.Client1, typ.ID); err != nil {
		t.Errorf("a CI without the attribute: %v", err)
	}

	// Interrupted rebuilds: the next run completes them.
	for _, d := range defs {
		name := IndexName(d.ID, d.Unique)
		rebuild := name + RebuildSuffix
		for _, s := range []struct {
			state   string
			prepare func()
			renamed bool // the next run renames the rebuild index instead of building one
		}{
			{"build done, old index not dropped", func() { exec(indexStatement(d, rebuild)) }, true},
			{"drop of the old index interrupted", func() {
				exec(indexStatement(d, rebuild))
				exec(`UPDATE pg_index SET indisvalid = false WHERE indexrelid = '` + name + `'::regclass`)
			}, true},
			{"rename not done", func() {
				exec(indexStatement(d, rebuild))
				exec(`DROP INDEX ` + name)
			}, true},
			{"invalid rebuild left over", func() {
				exec(indexStatement(d, rebuild))
				exec(`UPDATE pg_index SET indisvalid = false WHERE indexrelid = '` + rebuild + `'::regclass`)
			}, false},
		} {
			legacy(d)
			s.prepare()
			prepared := indexOID(t, f.Admin, rebuild)
			if err = ddl.Ensure(bg, f.OrgA, d.ID); err != nil {
				t.Fatalf("%s, %s: %v", d.Key, s.state, err)
			}
			current(d)
			if renamed := indexOID(t, f.Admin, name) == prepared; renamed != s.renamed {
				t.Errorf("%s, %s: the index was renamed %v, want %v", d.Key, s.state, renamed, s.renamed)
			}
		}
	}

	// Build interrupted: a transaction that wrote to ci keeps the
	// concurrent build waiting until the run is cancelled. The old index
	// stays valid; the next run builds the new one.
	legacy(serial)
	blocker, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(bg, `UPDATE ci SET name = name WHERE organization_id = $1 AND name = 'wp435-r-0'`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(bg, 3*time.Second)
	err = ddl.Ensure(short, f.OrgA, serial.ID)
	cancel()
	_ = blocker.Rollback(bg)
	if err == nil {
		t.Fatal("the run finished although a transaction kept the build waiting")
	}
	if got := readIndex(t, f.Admin, serialIndex); !got.valid || !strings.Contains(got.def, "->>") {
		t.Fatalf("after the interrupted build the old index = %+v, want it valid", got)
	}
	if got := readIndex(t, f.Admin, serialIndex+RebuildSuffix); got.found && got.valid {
		t.Fatalf("the interrupted build left a valid index: %+v", got)
	} else {
		t.Logf("interrupted build (%v): rebuild index left behind %v (invalid), old index valid", err, got.found)
	}
	if err = ddl.Ensure(bg, f.OrgA, serial.ID); err != nil {
		t.Fatal(err)
	}
	current(serial)

	// Another run holds the definition: this one waits and changes nothing.
	legacy(serial)
	holder, err := f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	lockKey := IndexJobKind + "/" + serial.ID
	if _, err = holder.Exec(bg, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		t.Fatal(err)
	}
	short, cancel = context.WithTimeout(bg, 2500*time.Millisecond)
	err = ddl.Ensure(short, f.OrgA, serial.ID)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "lock attribute definition") {
		t.Fatalf("run while another holds the definition: %v, want it to wait for the lock", err)
	}
	if got := readIndex(t, f.Admin, serialIndex); !strings.Contains(got.def, "->>") || readIndex(t, f.Admin, serialIndex+RebuildSuffix).found {
		t.Fatalf("the waiting run changed the indexes: %+v", got)
	}
	if _, err = holder.Exec(bg, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey); err != nil {
		t.Fatal(err)
	}
	if err = ddl.Ensure(bg, f.OrgA, serial.ID); err != nil {
		t.Fatal(err)
	}
	current(serial)
}

// TestKeyIndexName covers the name and statement of the index of a key
// (WP-607, E-73 supplement of 2026-10-26): idx_attr_key_<key> for a key of
// [a-z0-9_] that fits, else idx_attr_keyh_ with 32 hex digits of the
// SHA-256 of the key; deterministic, a valid unquoted PostgreSQL name of at
// most 63 bytes, different for different keys, and never the name of a
// definition's index (idx_attr_<32 hex>). The statement has no predicate
// on the organization; the literal of the key is an escape string constant
// when the key holds a backslash.
func TestKeyIndexName(t *testing.T) {
	long := strings.Repeat("k", 50)
	for _, c := range []struct {
		key, want string
	}{
		{"cost_center", "idx_attr_key_cost_center"},
		{"1st_level_2", "idx_attr_key_1st_level_2"},
		{long, "idx_attr_key_" + long},
		{long + "k", "idx_attr_keyh_"},
		{"Cost_Center", "idx_attr_keyh_"},
		{"cost-center", "idx_attr_keyh_"},
		{"cost center", "idx_attr_keyh_"},
		{"kosten'stelle", "idx_attr_keyh_"},
		{`back\slash`, "idx_attr_keyh_"},
		{"größe", "idx_attr_keyh_"},
		{"idx", "idx_attr_key_idx"},
	} {
		name := KeyIndexName(c.key)
		if strings.HasSuffix(c.want, "_") {
			sum := sha256.Sum256([]byte(c.key))
			c.want += hex.EncodeToString(sum[:16])
		}
		if name != c.want || name != KeyIndexName(c.key) || len(name) > 63 || !plainKey(name) || !strings.HasPrefix(name, KeyIndexPrefix) {
			t.Errorf("KeyIndexName(%q) = %q, want %q (at most 63 bytes of [a-z0-9_])", c.key, name, c.want)
		}
	}
	if KeyIndexName("Cost_Center") == KeyIndexName("cost_center") || KeyIndexName("a b") == KeyIndexName("a_b") {
		t.Error("different keys share an index name")
	}
	// A definition's index starts with idx_attr_ and a hex digit.
	if def := IndexName("abcdef01-0000-4000-8000-000000000001", false); strings.HasPrefix(def, KeyIndexPrefix) {
		t.Errorf("the index of a definition %s starts like the index of a key", def)
	}
	if got, want := KeyIndexStatement("cost_center"), "CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_attr_key_cost_center ON ci "+
		"(organization_id, reticora_attr_text(attributes, 'cost_center')) WHERE deleted_at IS NULL AND "+
		"reticora_attr_text(attributes, 'cost_center') IS NOT NULL"; got != want {
		t.Errorf("KeyIndexStatement = %s\nwant %s", got, want)
	}
	if got, want := KeyExpression(`a'b\c`), `reticora_attr_text(attributes, E'a''b\\c')`; got != want {
		t.Errorf("KeyExpression = %s, want %s", got, want)
	}
	for _, d := range []*AttributeDefinition{
		{OrganizationID: "o", Key: "k", Searchable: true},
		{OrganizationID: "o", Key: "k", Searchable: true, Unique: true},
		{OrganizationID: "o", Key: "k", Searchable: true, CITypeID: "t"},
		{Key: "k", Searchable: true},
		{OrganizationID: "o", Key: "k"},
	} {
		want := d.OrganizationID != "" && d.Searchable && !d.Unique && d.CITypeID == ""
		if UsesKeyIndex(d) != want || (IndexStatement(d) == "") != (want || (!d.Searchable && !d.Unique)) {
			t.Errorf("%+v: UsesKeyIndex %v, own statement %q; want key index %v", d, UsesKeyIndex(d), IndexStatement(d), want)
		}
	}
}

// TestAttributeIndexPerKeyJob covers the DDL job of WP-607 (DB-04, NFR-01,
// NFR-02; E-73, supplement of 2026-10-26) against PostgreSQL:
//   - migration 000164: the attribute.index.key jobs replace the indexes of
//     the not unique, searchable definitions without CI type (built per
//     definition and organization before) by the one index of their key;
//     the unique definition and the type definition of the same key keep
//     their own index (the same index);
//   - the index of the key is valid before the definition's own index
//     goes: a build that is cancelled leaves the own index valid, the next
//     run completes the switch;
//   - a key with other characters than [a-z0-9_] gets the hashed name; the
//     literal of the key means the key under both settings of
//     standard_conforming_strings, and the index serves the filter;
//   - reference counting on flag changes: unique or not searchable any
//     more keeps the index while another organization uses the key and
//     drops it with the last one; not unique again builds it again and
//     drops the definition's unique index;
//   - the deletion of a definition (its key is unknown then) drops the
//     indexes of keys no definition uses, also one left over, and keeps the
//     others;
//   - a run waits for another run on the same key (advisory lock on the
//     index name) and changes nothing meanwhile.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAttributeIndexPerKeyJob(t *testing.T) {
	fixtures := []*scopetest.Fixture{scopetest.Seed(t, "9e"), scopetest.Seed(t, "f8")}
	f := fixtures[0]
	bg := context.Background()
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	ddl := NewDDLJob(f.App, svc)
	// Without the scheduler: the jobs are queued and run by the test.
	repo := NewPGRepository(f.App)
	// Keys of this run (an index of a key outlives a deleted fixture).
	run := time.Now().UnixNano()
	key := fmt.Sprintf("wp607_rack_%d", run)
	odd := fmt.Sprintf(`WP607 Kosten'stelle\%d`, run)
	orphan := fmt.Sprintf("wp607_orphan_%d", run)
	other := fmt.Sprintf("wp607_other_%d", run)
	t.Cleanup(func() {
		for _, k := range []string{key, odd, orphan, other} {
			_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+KeyIndexName(k))
		}
	})
	type orgOf struct {
		ctx context.Context
		org string
	}
	var orgs []orgOf
	for _, x := range fixtures {
		for _, org := range []string{x.OrgA, x.OrgB} {
			orgs = append(orgs, orgOf{tenant.WithTenant(x.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: x.User}), org})
		}
	}
	create := func(o orgOf, d *AttributeDefinition) *AttributeDefinition {
		t.Helper()
		if err := repo.CreateAttributeDefinition(o.ctx, o.org, d); err != nil {
			t.Fatalf("create definition %s in %s: %v", d.Key, o.org, err)
		}
		t.Cleanup(func() {
			for _, unique := range []bool{false, true} {
				name := IndexName(d.ID, unique)
				_, _ = f.Admin.Exec(bg, `DROP INDEX IF EXISTS `+name+`, `+name+RebuildSuffix)
			}
		})
		return d
	}
	update := func(o orgOf, d *AttributeDefinition, patch *AttributeDefinitionPatch) {
		t.Helper()
		if _, err := repo.UpdateAttributeDefinition(o.ctx, o.org, d.ID, patch); err != nil {
			t.Fatalf("update definition %s in %s: %v", d.Key, o.org, err)
		}
		if err := ddl.Ensure(bg, o.org, d.ID); err != nil {
			t.Fatalf("index job of %s in %s: %v", d.Key, o.org, err)
		}
	}
	exec := func(sql string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	keyIndex := KeyIndexName(key)
	// Ten CIs per organization with the attributes.
	for _, o := range orgs {
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, attributes)
			SELECT $1, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1), 'wp607-j-' || i, 'active',
				jsonb_build_object($2::text, 'R' || i, $3::text, 'V' || i)
			FROM generate_series(1, 10) AS i`, o.org, key, odd); err != nil {
			t.Fatalf("seed CIs: %v", err)
		}
	}

	// Before migration 000164: three organizations make the key searchable,
	// each with its own index; a fourth has a unique definition of the key,
	// the first a type definition of it.
	typ := &Type{OrganizationID: orgs[0].org, Key: fmt.Sprintf("wp607-type-%d", run), Name: "wp607-type", IsObservable: true}
	if err := repo.Create(orgs[0].ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	var plain []*AttributeDefinition
	for _, o := range orgs[:3] {
		d := create(o, &AttributeDefinition{Key: key, DataType: "text", Searchable: true})
		d.OrganizationID = o.org
		exec(indexStatement(d, IndexName(d.ID, false)))
		plain = append(plain, d)
	}
	unique := create(orgs[3], &AttributeDefinition{Key: key, DataType: "text", Searchable: true, Unique: true})
	typed := create(orgs[0], &AttributeDefinition{CITypeID: typ.ID, Key: key, DataType: "text", Searchable: true})
	if err := ddl.Ensure(bg, orgs[3].org, unique.ID); err != nil {
		t.Fatalf("index of the unique definition: %v", err)
	}
	if err := ddl.Ensure(bg, orgs[0].org, typed.ID); err != nil {
		t.Fatalf("index of the type definition: %v", err)
	}
	own := map[string]uint32{IndexName(unique.ID, true): indexOID(t, f.Admin, IndexName(unique.ID, true)),
		IndexName(typed.ID, false): indexOID(t, f.Admin, IndexName(typed.ID, false))}
	for name, oid := range own {
		if oid == 0 {
			t.Fatalf("the index %s of the unique or type definition is missing", name)
		}
	}
	for _, d := range plain {
		if _, _, err := svc.Enqueue(bg, d.OrganizationID, KeyJobKind, indexJobParams{DefinitionID: d.ID}, jobs.EnqueueOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if done, err := ddl.RunDue(bg); err != nil || done < len(plain) {
		t.Fatalf("RunDue = %d, %v; want the %d key jobs done", done, err, len(plain))
	}
	for _, d := range plain {
		list, _, err := svc.List(bg, d.OrganizationID, jobs.Filter{Kind: KeyJobKind}, api.PaginationParams{Limit: 10})
		if err != nil || len(list) != 1 || list[0].Status != jobs.StatusSucceeded {
			t.Fatalf("key job of %s = %+v, %v; want succeeded", d.OrganizationID, list, err)
		}
		if readIndex(t, f.Admin, IndexName(d.ID, false)).found {
			t.Errorf("the own index of the definition in %s survived its key job", d.OrganizationID)
		}
	}
	got := readIndex(t, f.Admin, keyIndex)
	if !got.found || !got.valid || got.unique || strings.Contains(got.def, "organization_id = ") ||
		!strings.Contains(got.def, "(organization_id, reticora_attr_text(attributes, '"+key+"'::text))") {
		t.Fatalf("index of the key = %+v, want (organization_id, value) without a predicate on the organization", got)
	}
	for name, oid := range own {
		if indexOID(t, f.Admin, name) != oid {
			t.Errorf("the index %s of a unique or type definition changed", name)
		}
	}

	// The switch is cancelled while the index of the key is built: the own
	// index stays valid; the next run completes the switch.
	d0 := plain[0]
	exec(`DROP INDEX ` + keyIndex)
	exec(indexStatement(d0, IndexName(d0.ID, false)))
	blocker, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = blocker.Exec(bg, `UPDATE ci SET name = name WHERE organization_id = $1 AND name = 'wp607-j-1'`, d0.OrganizationID); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(bg, 3*time.Second)
	err = ddl.Ensure(short, d0.OrganizationID, d0.ID)
	cancel()
	_ = blocker.Rollback(bg)
	if err == nil {
		t.Fatal("the switch finished although a transaction kept the build of the key index waiting")
	}
	if own := readIndex(t, f.Admin, IndexName(d0.ID, false)); !own.valid {
		t.Fatalf("after the cancelled build the own index = %+v, want it valid (no time without an index)", own)
	}
	if err = ddl.Ensure(bg, d0.OrganizationID, d0.ID); err != nil {
		t.Fatal(err)
	}
	if !readIndex(t, f.Admin, keyIndex).valid || readIndex(t, f.Admin, IndexName(d0.ID, false)).found {
		t.Fatal("the next run did not complete the switch to the index of the key")
	}

	// A key outside [a-z0-9_]: hashed name, the literal holds under both
	// settings of standard_conforming_strings, the index serves the filter.
	oddDef := create(orgs[1], &AttributeDefinition{Key: odd, DataType: "text", Searchable: true})
	if err = ddl.Ensure(bg, orgs[1].org, oddDef.ID); err != nil {
		t.Fatal(err)
	}
	if got := readIndex(t, f.Admin, KeyIndexName(odd)); !got.valid || !strings.HasPrefix(KeyIndexName(odd), KeyIndexPrefix+"h_") {
		t.Fatalf("index of the key %q = %+v (%s)", odd, got, KeyIndexName(odd))
	}
	for _, setting := range []string{"on", "off"} {
		var same bool
		err = pgx.BeginFunc(bg, f.Admin, func(tx pgx.Tx) error {
			if _, serr := tx.Exec(bg, `SET LOCAL standard_conforming_strings = `+setting); serr != nil {
				return serr
			}
			// Parsed under the setting (no statement cached from the other).
			return tx.QueryRow(bg, `SELECT `+quoteLiteral(odd)+` = $1`, pgx.QueryExecModeExec, odd).Scan(&same)
		})
		if err != nil || !same {
			t.Errorf("standard_conforming_strings %s: the literal %s is not the key: %v", setting, quoteLiteral(odd), err)
		}
	}
	var plan string
	err = pgx.BeginFunc(bg, f.Admin, func(tx pgx.Tx) error {
		if _, serr := tx.Exec(bg, `SET LOCAL enable_seqscan = off`); serr != nil {
			return serr
		}
		return tx.QueryRow(bg, `EXPLAIN (COSTS OFF) SELECT id FROM ci WHERE deleted_at IS NULL AND organization_id = $1::uuid AND `+
			KeyExpression(odd)+` = 'V3'`, orgs[1].org).Scan(&plan)
	})
	if err != nil || !strings.Contains(plan, KeyIndexName(odd)) {
		t.Errorf("the filter on %q does not use %s: %s %v", odd, KeyIndexName(odd), plan, err)
	}

	// Reference counting on flag changes.
	yes, no := true, false
	update(orgs[0], plain[0], &AttributeDefinitionPatch{Unique: &yes})
	if !readIndex(t, f.Admin, IndexName(plain[0].ID, true)).valid || !readIndex(t, f.Admin, keyIndex).valid {
		t.Fatal("unique in one organization: want its unique index and the index of the key the others use")
	}
	update(orgs[1], plain[1], &AttributeDefinitionPatch{Searchable: &no})
	if !readIndex(t, f.Admin, keyIndex).valid {
		t.Fatal("not searchable in one organization: the index of the key went while another uses it")
	}
	update(orgs[2], plain[2], &AttributeDefinitionPatch{Unique: &yes})
	if readIndex(t, f.Admin, keyIndex).found {
		t.Fatal("the last definition of the key became unique: the index of the key survived")
	}
	if !readIndex(t, f.Admin, IndexName(plain[2].ID, true)).valid {
		t.Fatal("the unique index of the last definition is missing")
	}
	update(orgs[0], plain[0], &AttributeDefinitionPatch{Unique: &no})
	if !readIndex(t, f.Admin, keyIndex).valid || readIndex(t, f.Admin, IndexName(plain[0].ID, true)).found {
		t.Fatal("not unique again: want the index of the key and no unique index of the definition")
	}
	for name, oid := range own {
		if indexOID(t, f.Admin, name) != oid {
			t.Errorf("the index %s of a unique or type definition changed", name)
		}
	}

	// A deletion drops the indexes of keys no definition uses: the one left
	// over, not the ones in use.
	exec(KeyIndexStatement(orphan))
	if err = repo.DeleteAttributeDefinition(orgs[0].ctx, orgs[0].org, typed.ID); err != nil {
		t.Fatal(err)
	}
	if err = ddl.Ensure(bg, orgs[0].org, typed.ID); err != nil {
		t.Fatal(err)
	}
	if readIndex(t, f.Admin, KeyIndexName(orphan)).found {
		t.Error("the index of a key no definition uses survived a deletion")
	}
	if readIndex(t, f.Admin, IndexName(typed.ID, false)).found {
		t.Error("the index of the deleted type definition survived")
	}
	if !readIndex(t, f.Admin, keyIndex).valid || !readIndex(t, f.Admin, KeyIndexName(odd)).valid {
		t.Error("a deletion dropped the index of a key in use")
	}

	// Another run holds the index of the key: this one waits and changes
	// nothing.
	exec(`DROP INDEX ` + keyIndex)
	holder, err := f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	lockKey := IndexJobKind + "/" + keyIndex
	if _, err = holder.Exec(bg, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		t.Fatal(err)
	}
	short, cancel = context.WithTimeout(bg, 2500*time.Millisecond)
	err = ddl.Ensure(short, orgs[0].org, plain[0].ID)
	cancel()
	if err == nil || !strings.Contains(err.Error(), "lock attribute index") || readIndex(t, f.Admin, keyIndex).found {
		t.Fatalf("run while another holds the index of the key: %v, want it to wait for the lock and build nothing", err)
	}
	if _, err = holder.Exec(bg, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, lockKey); err != nil {
		t.Fatal(err)
	}
	if err = ddl.Ensure(bg, orgs[0].org, plain[0].ID); err != nil || !readIndex(t, f.Admin, keyIndex).valid {
		t.Fatalf("run after the lock: %v, index %+v", err, readIndex(t, f.Admin, keyIndex))
	}

	// A deletion waits for another run on the index of a key it would drop;
	// the error its job records does not name that index (the key may be
	// one of another organization). After the lock the deletion completes.
	exec(KeyIndexStatement(other))
	otherLock := IndexJobKind + "/" + KeyIndexName(other)
	if _, err = holder.Exec(bg, `SELECT pg_advisory_lock(hashtextextended($1, 0))`, otherLock); err != nil {
		t.Fatal(err)
	}
	if err = repo.DeleteAttributeDefinition(orgs[1].ctx, orgs[1].org, oddDef.ID); err != nil {
		t.Fatal(err)
	}
	short, cancel = context.WithTimeout(bg, 2500*time.Millisecond)
	err = ddl.Ensure(short, orgs[1].org, oddDef.ID)
	cancel()
	if err == nil || strings.Contains(err.Error(), other) || strings.Contains(err.Error(), KeyIndexName(other)) {
		t.Errorf("deletion while another run holds the index of a key: %v; want it to wait, and an error without the name of the index", err)
	}
	if _, err = holder.Exec(bg, `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, otherLock); err != nil {
		t.Fatal(err)
	}
	if err = ddl.Ensure(bg, orgs[1].org, oddDef.ID); err != nil || readIndex(t, f.Admin, KeyIndexName(other)).found ||
		readIndex(t, f.Admin, KeyIndexName(odd)).found || !readIndex(t, f.Admin, keyIndex).valid {
		t.Errorf("deletion after the lock: %v; want the unused indexes of keys dropped, the one in use kept", err)
	}
}
