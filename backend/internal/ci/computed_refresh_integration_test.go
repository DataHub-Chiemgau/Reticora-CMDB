package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// refreshClock is a settable clock of the refresh.
type refreshClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *refreshClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *refreshClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// TestComputedAttributeRefresh covers WP-718 (JOB-02 [P2], MET-10
// computed) against PostgreSQL:
//   - the seed 000225: the system definition warranty_days_left raises the
//     schema_version of every type by one and is readOnly with x-computed;
//   - the refresh computes "today" in organization.timezone with a fixed
//     clock across local midnight, writes only changed values with their
//     provenance (source computed, rank 100, observed_at = the run), keeps
//     ci.version and writes no history, audit entry or outbox event; a
//     changed asset takes effect with the next run;
//   - it walks the organization in batches over WithTenant (a system
//     transaction in the context is refused, ErrTxNotJoinable);
//   - filter[attributes.warranty_days_left][lte]=30 finds the CIs whose
//     warranty ends within 30 days;
//   - on the scheduler with the memory locker the kind
//     attribute.computed_refresh runs once per slot (MaxCatchUp 1).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestComputedAttributeRefresh(t *testing.T) {
	f := scopetest.Seed(t, "c7")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	types := citype.NewPGRepository(f.App)
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()).WithComputed(types)
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo).WithFieldResolver(types)).RegisterRoutes(mux)
	clock := &refreshClock{}
	refresh := ci.NewComputedRefresh(f.App, types).WithClock(clock.now)
	if refresh.Batch != ci.RefreshBatch || ci.RefreshBatch != 100 {
		t.Errorf("batch %d (RefreshBatch %d), want 100", refresh.Batch, ci.RefreshBatch)
	}
	if s := refresh.Schedule(); s.Kind != ci.RefreshJobKind || s.Spec != "@every 1h" || s.MaxCatchUp != 1 {
		t.Errorf("schedule %s %q catch-up %d, want %s @every 1h, 1", s.Kind, s.Spec, s.MaxCatchUp, ci.RefreshJobKind)
	}

	t.Run("seed", func(t *testing.T) {
		up, err := os.ReadFile("../../migrations/000225_computed_seed.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		down, err := os.ReadFile("../../migrations/000225_computed_seed.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		server := func() (version int, property map[string]any) {
			t.Helper()
			var raw []byte
			if qerr := tx.QueryRow(bg, `SELECT schema_version, attribute_schema -> 'properties' -> 'warranty_days_left'
				FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&version, &raw); qerr != nil {
				t.Fatal(qerr)
			}
			_ = json.Unmarshal(raw, &property)
			return version, property
		}
		seeded, property := server()
		if property["readOnly"] != true || property["x-computed"] == nil || property["type"] != "integer" {
			t.Errorf("seeded property = %v, want readOnly integer with x-computed", property)
		}
		if _, err = tx.Exec(bg, string(down)); err != nil {
			t.Fatalf("down: %v", err)
		}
		without, property := server()
		if property != nil || without != seeded+1 {
			t.Errorf("after down: v%d %v, want v%d without the attribute", without, property, seeded+1)
		}
		if _, err = tx.Exec(bg, string(up)); err != nil {
			t.Fatalf("up: %v", err)
		}
		with, property := server()
		if property["readOnly"] != true || with != without+1 {
			t.Errorf("after up: v%d %v, want v%d with the attribute", with, property, without+1)
		}
		var system, searchable bool
		var org *string
		if err = tx.QueryRow(bg, `SELECT organization_id::text, organization_id IS NULL AND ci_type_id IS NULL AND scope = 'global', searchable
			FROM attribute_definition WHERE id = 'a7180000-0000-4000-8000-000000000001'`).Scan(&org, &system, &searchable); err != nil {
			t.Fatal(err)
		}
		if !system || !searchable {
			t.Errorf("seeded definition: system %v searchable %v", system, searchable)
		}
		// No index and no index job (K22).
		var indexes, indexJobs int
		if err = tx.QueryRow(bg, `SELECT (SELECT count(*) FROM pg_indexes WHERE indexname IN ($1, $2)),
			(SELECT count(*) FROM job WHERE params ->> 'definition_id' = 'a7180000-0000-4000-8000-000000000001')`,
			citype.IndexName("a7180000-0000-4000-8000-000000000001", false), citype.KeyIndexName("warranty_days_left")).Scan(&indexes, &indexJobs); err != nil {
			t.Fatal(err)
		}
		if indexes != 0 || indexJobs != 0 {
			t.Errorf("seed: %d indexes and %d jobs, want none", indexes, indexJobs)
		}
	})

	if _, err := f.Admin.Exec(bg, `UPDATE organization SET timezone = 'Europe/Berlin' WHERE id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	asset := func(tag, warranty string) string {
		t.Helper()
		id := f.Asset(t, f.OrgA, f.Client1, tag)
		if _, err := f.Admin.Exec(bg, `UPDATE asset SET warranty_end = $2 WHERE id = $1`, id, warranty); err != nil {
			t.Fatal(err)
		}
		return id
	}
	create := func(name, assetID string) string {
		t.Helper()
		body := `{"ci_type_id":"` + serverType(t, f) + `","client_id":"` + f.Client1 + `","name":"` + name + `"`
		if assetID != "" {
			body += `,"asset_id":"` + assetID + `"`
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/cis", strings.NewReader(body+"}")).WithContext(ctx))
		if w.Code != http.StatusCreated {
			t.Fatalf("create %s: %d %s", name, w.Code, w.Body.String())
		}
		var created struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	type state struct {
		value    any
		version  int64
		rank     int
		observed time.Time
		changes  int
	}
	read := func(id string) state {
		t.Helper()
		var s state
		var raw []byte
		var rank *int
		var observed *time.Time
		if err := f.Admin.QueryRow(bg, `SELECT c.attributes -> 'warranty_days_left', c.version, v.discovered_rank, v.observed_at,
				(SELECT count(*) FROM ci_change h WHERE h.ci_id = c.id)
			FROM ci c LEFT JOIN ci_field_value v ON v.ci_id = c.id AND v.field_name = 'warranty_days_left' AND v.discovered_source = 'computed'
			WHERE c.id = $1`, id).Scan(&raw, &s.version, &rank, &observed, &s.changes); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &s.value)
		if rank != nil {
			s.rank = *rank
		}
		if observed != nil {
			s.observed = observed.UTC()
		}
		return s
	}
	orgRows := func() (outbox, audits int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM event_outbox WHERE organization_id = $1),
			(SELECT count(*) FROM audit_log WHERE organization_id = $1)`, f.OrgA).Scan(&outbox, &audits); err != nil {
			t.Fatal(err)
		}
		return outbox, audits
	}
	run := func(at string) ci.RefreshResult {
		t.Helper()
		ts, err := time.Parse(time.RFC3339, at)
		if err != nil {
			t.Fatal(err)
		}
		clock.set(ts)
		res, err := refresh.RunOrg(bg, f.OrgA)
		if err != nil {
			t.Fatalf("refresh at %s: %v", at, err)
		}
		return res
	}

	soon := create("wp718-soon", asset("WP718-SOON", "2026-03-12"))
	later := create("wp718-later", asset("WP718-LATER", "2026-06-30"))
	bare := create("wp718-bare", "")
	before := map[string]state{soon: read(soon), later: read(later), bare: read(bare)}
	outbox, audits := orgRows()

	// 23:30 in Berlin on 1 March: 11 days to 12 March.
	res := run("2026-03-01T22:30:00Z")
	if res.Changed < 2 || res.Checked < 3 {
		t.Errorf("first run: %+v, want at least 3 checked and 2 changed", res)
	}
	first := read(soon)
	if first.value != float64(11) || first.rank != ci.RankComputed || !first.observed.Equal(clock.now()) {
		t.Errorf("before local midnight: %+v, want 11 with rank 100 observed at the run", first)
	}
	for id, b := range before {
		if s := read(id); s.version != b.version || s.changes != b.changes {
			t.Errorf("%s: version %d → %d, changes %d → %d; want both unchanged", id, b.version, s.version, b.changes, s.changes)
		}
	}
	if s := read(bare); s.value != nil || s.rank != 0 {
		t.Errorf("CI without asset: %+v, want no value and no provenance", s)
	}
	if o, a := orgRows(); o != outbox || a != audits {
		t.Errorf("outbox %d → %d, audit %d → %d; want no entries", outbox, o, audits, a)
	}

	// The same instant again: nothing changes, nothing is written.
	if res = run("2026-03-01T22:30:00Z"); res.Changed != 0 {
		t.Errorf("second run at the same time: %+v, want nothing changed", res)
	}
	// 00:30 in Berlin on 2 March (still 1 March in UTC): 10 days.
	run("2026-03-01T23:30:00Z")
	if s := read(soon); s.value != float64(10) || !s.observed.Equal(clock.now()) || s.version != before[soon].version {
		t.Errorf("after local midnight: %+v, want 10 observed at the run, version %d", s, before[soon].version)
	}
	if s := read(later); s.value != float64(120) {
		t.Errorf("later warranty: %v, want 120", s.value)
	}

	// A changed asset takes effect with the next run.
	if _, err := f.Admin.Exec(bg, `UPDATE asset SET warranty_end = '2026-03-20' WHERE id = (SELECT asset_id FROM ci WHERE id = $1)`, soon); err != nil {
		t.Fatal(err)
	}
	if s := read(soon); s.value != float64(10) {
		t.Errorf("before the next run: %v, want 10", s.value)
	}
	run("2026-03-01T23:30:00Z")
	if s := read(soon); s.value != float64(18) {
		t.Errorf("after the asset change: %v, want 18", s.value)
	}

	// The filter on the computed attribute.
	t.Run("filter", func(t *testing.T) {
		q := url.Values{"filter[attributes.warranty_days_left][lte]": {"30"}, "limit": {"200"}}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis?"+q.Encode(), nil).WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("filter: %d %s", w.Code, w.Body.String())
		}
		var page struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		var ids []string
		for _, item := range page.Data {
			ids = append(ids, item.ID)
		}
		if !slices.Contains(ids, soon) || slices.Contains(ids, later) || slices.Contains(ids, bare) {
			t.Errorf("filter[attributes.warranty_days_left][lte]=30: %v, want %s without %s and %s", ids, soon, later, bare)
		}
	})

	t.Run("batches", func(t *testing.T) {
		small := ci.NewComputedRefresh(f.App, types).WithClock(clock.now)
		small.Batch = 2
		var many []string
		for _, tag := range []string{"WP718-B1", "WP718-B2", "WP718-B3", "WP718-B4", "WP718-B5"} {
			many = append(many, create(strings.ToLower(tag), asset(tag, "2026-03-05")))
		}
		clock.set(time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC))
		res, err := small.RunOrg(bg, f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		if res.Checked < len(many)+3 {
			t.Errorf("checked %d CIs, want at least %d", res.Checked, len(many)+3)
		}
		for _, id := range many {
			if s := read(id); s.value != float64(3) {
				t.Errorf("%s: %v, want 3", id, s.value)
			}
		}
		// Not under a system transaction: each batch is a tenant
		// transaction of the organization.
		err = database.WithSystem(bg, f.App, func(sysCtx context.Context, tx pgx.Tx) error {
			_, rerr := small.RunOrg(database.ContextWithTx(sysCtx, tx), f.OrgA)
			return rerr
		})
		if !errors.Is(err, database.ErrTxNotJoinable) {
			t.Errorf("refresh in a system transaction: %v, want ErrTxNotJoinable", err)
		}
	})

	t.Run("all organizations", func(t *testing.T) {
		clock.set(time.Date(2026, 3, 3, 12, 0, 0, 0, time.UTC))
		res, err := refresh.Run(bg)
		if err != nil {
			t.Fatalf("run: %v", err)
		}
		if res.Organizations < 2 || res.Changed < 1 {
			t.Errorf("run: %+v, want every organization and the changed CIs", res)
		}
		if s := read(soon); s.value != float64(17) {
			t.Errorf("after the run over all organizations: %v, want 17", s.value)
		}
	})

	t.Run("scheduler", func(t *testing.T) {
		at := time.Date(2026, 3, 4, 12, 0, 0, 0, time.UTC)
		tick := &refreshClock{t: at}
		sched := jobs.NewScheduler(jobs.NewMemoryLocker(), jobs.NewMemoryStateStore()).WithClock(tick.now)
		s := refresh.Schedule()
		runs := 0
		s.Run = func(runCtx context.Context) error {
			runs++
			clock.set(tick.now())
			_, err := refresh.RunOrg(runCtx, f.OrgA)
			return err
		}
		if err := sched.Register(&s); err != nil {
			t.Fatal(err)
		}
		advance := func(d time.Duration) int {
			at = at.Add(d)
			tick.set(at)
			return sched.Tick(bg)
		}
		if n := advance(0); n != 0 {
			t.Errorf("first tick: %d runs, want 0 (a new kind waits for its slot)", n)
		}
		if n := advance(time.Hour); n != 1 {
			t.Errorf("after an hour: %d runs, want 1", n)
		}
		if n := advance(0); n != 0 {
			t.Errorf("same slot: %d runs, want 0", n)
		}
		if n := advance(5 * time.Hour); n != 1 {
			t.Errorf("after a pause: %d runs, want 1 (MaxCatchUp 1)", n)
		}
		if runs != 2 {
			t.Errorf("%d runs of %s, want 2", runs, ci.RefreshJobKind)
		}
		if s := read(soon); s.value != float64(16) {
			t.Errorf("after the scheduled runs: %v, want 16", s.value)
		}
	})
}

// TestComputedAttributeBackfill covers WP-718 (MET-10 computed): creating
// a computed definition, changing its expression and deleting it, also
// through DELETE /api/v1/ci-types/{id}/fields/{name}, queues the job
// attribute.computed_backfill through the type repository; the RunDue run
// recomputes the existing CIs (values and provenance, no version step) and
// removes the values and provenance of a deleted definition.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestComputedAttributeBackfill(t *testing.T) {
	f := scopetest.Seed(t, "c8")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	types := citype.NewPGRepository(f.App)
	refresh := ci.NewComputedRefresh(f.App, types).WithJobs(svc)
	types.WithComputedScheduler(refresh)
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithComputed(types)
	cis := ci.NewService(repo).WithFieldResolver(types)
	mux := chi.NewRouter()
	citype.NewHandler(types).RegisterRoutes(mux)

	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp718-backfill", Name: "wp718-backfill"}
	if err := types.Create(ctx, typ); err != nil {
		t.Fatal(err)
	}
	host := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "wp718_host", DataType: "text"}
	if err := types.CreateAttributeDefinition(ctx, f.OrgA, host); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, name := range []string{"Alpha", "Beta"} {
		item := &ci.Item{OrganizationID: f.OrgA, CITypeID: typ.ID, Name: "wp718-" + name, Status: "active",
			Attributes: map[string]any{"wp718_host": name}}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		ids = append(ids, item.ID)
	}
	expr := func(s string) *rules.Expr {
		t.Helper()
		e, err := rules.ParseExpr([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		return &e
	}
	queued := func() int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM job WHERE organization_id = $1 AND kind = $2 AND status = 'queued'`,
			f.OrgA, ci.BackfillJobKind).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	runDue := func(step string) {
		t.Helper()
		if n := queued(); n != 1 {
			t.Errorf("%s: %d queued backfill jobs, want 1", step, n)
		}
		if _, err := refresh.RunDue(bg); err != nil {
			t.Fatalf("%s: run due: %v", step, err)
		}
		var open, done int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE status IN ('queued', 'running')), count(*) FILTER (WHERE status = 'succeeded')
			FROM job WHERE organization_id = $1 AND kind = $2`, f.OrgA, ci.BackfillJobKind).Scan(&open, &done); err != nil {
			t.Fatal(err)
		}
		if open != 0 || done == 0 {
			t.Errorf("%s: %d open and %d succeeded backfill jobs, want none open", step, open, done)
		}
	}
	type value struct {
		attr    any
		has     bool
		rank    int
		version int64
	}
	read := func(id, key string) value {
		t.Helper()
		var v value
		var raw []byte
		var rank *int
		if err := f.Admin.QueryRow(bg, `SELECT c.attributes -> $2, c.attributes ? $2, v.discovered_rank, c.version
			FROM ci c LEFT JOIN ci_field_value v ON v.ci_id = c.id AND v.field_name = $2 AND v.discovered_source = 'computed'
			WHERE c.id = $1`, id, key).Scan(&raw, &v.has, &rank, &v.version); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(raw, &v.attr)
		if rank != nil {
			v.rank = *rank
		}
		return v
	}
	versions := map[string]int64{}
	for _, id := range ids {
		versions[id] = read(id, "wp718_host").version
	}

	// Created: the existing CIs get the value with the backfill.
	upper := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "wp718_upper", DataType: "text",
		Computed: expr(`{"op":"upper","args":[{"op":"field","field":"wp718_host"}]}`)}
	if err := types.CreateAttributeDefinition(ctx, f.OrgA, upper); err != nil {
		t.Fatal(err)
	}
	other := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "wp718_tag", DataType: "text",
		Computed: expr(`{"op":"concat","args":[{"op":"literal","value":"#"},{"op":"field","field":"wp718_host"}]}`)}
	if err := types.CreateAttributeDefinition(ctx, f.OrgA, other); err != nil {
		t.Fatal(err)
	}
	if v := read(ids[0], "wp718_upper"); v.has {
		t.Errorf("before the backfill: %+v, want no value", v)
	}
	if n := queued(); n != 2 {
		t.Errorf("after two computed definitions: %d queued backfill jobs, want 2", n)
	}
	if _, err := refresh.RunDue(bg); err != nil {
		t.Fatal(err)
	}
	if v := read(ids[0], "wp718_upper"); v.attr != "ALPHA" || v.rank != ci.RankComputed || v.version != versions[ids[0]] {
		t.Errorf("after create: %+v, want ALPHA with rank 100 and version %d", v, versions[ids[0]])
	}
	if v := read(ids[1], "wp718_tag"); v.attr != "#Beta" {
		t.Errorf("after create: %+v, want #Beta", v)
	}

	// An entered definition and a change without effect queue nothing.
	plain := &citype.AttributeDefinition{CITypeID: typ.ID, Key: "wp718_note", DataType: "text"}
	if err := types.CreateAttributeDefinition(ctx, f.OrgA, plain); err != nil {
		t.Fatal(err)
	}
	group := "wp718"
	if _, err := types.UpdateAttributeDefinition(ctx, f.OrgA, upper.ID, &citype.AttributeDefinitionPatch{Group: &group}); err != nil {
		t.Fatal(err)
	}
	if n := queued(); n != 0 {
		t.Errorf("after an entered definition and a group change: %d queued backfill jobs, want 0", n)
	}

	// The expression changed.
	if _, err := types.UpdateAttributeDefinition(ctx, f.OrgA, upper.ID, &citype.AttributeDefinitionPatch{
		Computed: json.RawMessage(`{"op":"lower","args":[{"op":"field","field":"wp718_host"}]}`)}); err != nil {
		t.Fatal(err)
	}
	runDue("expression changed")
	if v := read(ids[1], "wp718_upper"); v.attr != "beta" || v.version != versions[ids[1]] {
		t.Errorf("after the change: %+v, want beta with version %d", v, versions[ids[1]])
	}

	// Deleted through DELETE /ci-types/{id}/fields/{name}.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodDelete, "/api/v1/ci-types/"+typ.ID+"/fields/wp718_upper", nil).WithContext(ctx))
	if w.Code != http.StatusNoContent {
		t.Fatalf("DELETE field: %d %s", w.Code, w.Body.String())
	}
	runDue("field deleted")
	for _, id := range ids {
		if v := read(id, "wp718_upper"); v.has || v.rank != 0 || v.version != versions[id] {
			t.Errorf("after DELETE of the field: %+v, want neither value nor provenance, version %d", v, versions[id])
		}
		if v := read(id, "wp718_tag"); !v.has {
			t.Errorf("after DELETE of another field: wp718_tag gone (%+v)", v)
		}
	}

	// Deleted through DELETE /attribute-definitions/{id}.
	if err := types.DeleteAttributeDefinition(ctx, f.OrgA, other.ID); err != nil {
		t.Fatal(err)
	}
	runDue("definition deleted")
	for _, id := range ids {
		if v := read(id, "wp718_tag"); v.has || v.rank != 0 {
			t.Errorf("after DELETE of the definition: %+v, want neither value nor provenance", v)
		}
		if v := read(id, "wp718_host"); v.attr == nil {
			t.Errorf("the input wp718_host is gone: %+v", v)
		}
	}
}
