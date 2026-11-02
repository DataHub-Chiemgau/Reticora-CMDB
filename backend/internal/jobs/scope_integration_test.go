package jobs_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// errRollback ends a transaction of the WITH CHECK cases after a write the
// policies admitted.
var errRollback = errors.New("rollback")

// TestJobScopeAgainstPostgres covers WP-404 (API-09, JOB-03, TEN-09; F-163,
// E-09) against PostgreSQL: a job carries the scope of its creator and row
// level security (job_isolation, job_scope_select of migration 000154)
// confines it to principals whose scope covers that snapshot. A site-1
// principal does not see the job of site 2 of the same client, also not
// by a plain SELECT (job_system_select alone would show it); an export job
// of client 1 that the worker tracks after the fact takes the snapshot of
// its export and is invisible to its creator in a client-2 scope, besides
// the creator rule of WP-347; a system job (queued without request) is
// visible to org-wide principals only; WITH CHECK refuses a job with a
// scope outside the writer's. Worker paths stay organization-wide.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestJobScopeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "92")
	bg := context.Background()
	s := jobs.NewService(jobs.NewPGRepository(f.App))
	s.Register(jobs.Kind{Name: "wp404.work", MaxAttempts: 1})
	siteOf := func(room string) string {
		t.Helper()
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`,
			room).Scan(&site); err != nil {
			t.Fatal(err)
		}
		return site
	}
	site1, site2 := siteOf(f.Room(t, f.Client1, "wp404-s1")), siteOf(f.Room(t, f.Client1, "wp404-s2"))
	creator, other, admin := f.AppUser(t, f.OrgA, "wp404-creator"), f.AppUser(t, f.OrgA, "wp404-other"), f.AppUser(t, f.OrgA, "wp404-admin")
	team := f.ID()

	type principal struct {
		name  string
		scope database.TenantScope
	}
	as := func(name, user string, clients, sites, teams []string) principal {
		scope := database.OrgWideScope(f.OrgA, user)
		if clients != nil {
			scope.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			scope.Sites = database.ScopeIDs(sites...)
		}
		if teams != nil {
			scope.Teams = database.ScopeIDs(teams...)
		}
		return principal{name: name, scope: scope}
	}
	ctxOf := func(p principal) context.Context { return database.ContextWithTenantScope(bg, &p.scope) }
	orgWide := as("org-wide", admin, nil, nil, nil)
	client1 := as("client 1", creator, []string{f.Client1}, nil, nil)
	client2 := as("client 2", creator, []string{f.Client2}, nil, nil)
	otherClient1 := as("other user of client 1", other, []string{f.Client1}, nil, nil)
	onSite1 := as("site 1", creator, []string{f.Client1}, []string{site1}, nil)
	onSite2 := as("site 2", other, []string{f.Client1}, []string{site2}, nil)
	inTeam := as("team", creator, nil, nil, []string{team})

	enqueue := func(ctx context.Context) *jobs.Job {
		t.Helper()
		job, _, err := s.Enqueue(ctx, f.OrgA, "wp404.work", map[string]string{"ci": "x"}, jobs.EnqueueOptions{CreatedBy: creator})
		if err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		return job
	}
	site1Job, site2Job := enqueue(ctxOf(onSite1)), enqueue(ctxOf(onSite2))
	clientJob := enqueue(ctxOf(client1))
	system := enqueue(bg)
	if site1Job.Scope == nil || !slices.Equal(site1Job.Scope.Sites, []string{site1}) || !slices.Equal(site1Job.Scope.Clients, []string{f.Client1}) ||
		site1Job.Scope.Teams != nil || system.Scope != nil {
		t.Fatalf("snapshots: site 1 job %+v, system job %+v", site1Job.Scope, system.Scope)
	}
	var stored string
	if err := f.Admin.QueryRow(bg, `SELECT scope_recorded::text || '|' || COALESCE(scope_clients::text, 'NULL') || '|' ||
		COALESCE(scope_sites::text, 'NULL') || '|' || COALESCE(scope_teams::text, 'NULL') FROM job WHERE id = $1`, site1Job.ID).Scan(&stored); err != nil ||
		stored != "true|{"+f.Client1+"}|{"+site1+"}|NULL" {
		t.Errorf("stored snapshot of the site 1 job = %q (%v)", stored, err)
	}

	// Export of client 1, created like export.PGJobRepository.CreateJob and
	// tracked by the worker only (Mirror without request scope): the job
	// takes creator and snapshot of the export.
	var exportID string
	if err := f.Admin.QueryRow(bg, `INSERT INTO export_job (organization_id, initiated_by, scope_recorded, scope_clients)
		VALUES ($1, $2, true, ARRAY[$3::uuid]) RETURNING id::text`, f.OrgA, creator, f.Client1).Scan(&exportID); err != nil {
		t.Fatal(err)
	}
	if err := s.Mirror(bg, f.OrgA, "export_job", exportID, jobs.StatusRunning, ""); err != nil {
		t.Fatal(err)
	}
	exported, err := s.Get(bg, f.OrgA, mustRefJob(t, f, exportID))
	if err != nil || exported.CreatedBy != creator || exported.Scope == nil || !slices.Equal(exported.Scope.Clients, []string{f.Client1}) {
		t.Fatalf("mirrored export job: %+v %v", exported, err)
	}
	// Tracked in the request of its creator (export.JobHandler.CreateJob).
	trackedID := f.ID()
	if err = s.Track(ctxOf(client1), f.OrgA, "export", "export_job", trackedID, creator); err != nil {
		t.Fatal(err)
	}
	tracked := mustRefJob(t, f, trackedID)

	all := []string{site1Job.ID, site2Job.ID, clientJob.ID, system.ID, exported.ID, tracked}
	names := map[string]string{site1Job.ID: "site 1 job", site2Job.ID: "site 2 job", clientJob.ID: "client 1 job",
		system.ID: "system job", exported.ID: "mirrored export", tracked: "tracked export"}
	for _, c := range []struct {
		p    principal
		want []string
	}{
		{orgWide, []string{site1Job.ID, site2Job.ID, clientJob.ID, system.ID}},
		{client1, []string{site1Job.ID, site2Job.ID, clientJob.ID, exported.ID, tracked}},
		{client2, nil},
		{otherClient1, []string{site1Job.ID, site2Job.ID, clientJob.ID}},
		{onSite1, []string{site1Job.ID}},
		{onSite2, []string{site2Job.ID}},
		{inTeam, nil},
	} {
		ctx := ctxOf(c.p)
		list, total, lerr := s.List(ctx, f.OrgA, jobs.Filter{}, api.PaginationParams{Limit: 50})
		if lerr != nil {
			t.Fatalf("list as %s: %v", c.p.name, lerr)
		}
		var listed []string
		for _, j := range list {
			listed = append(listed, j.ID)
		}
		// Plain SELECT under the principal's scope: the policies alone.
		var selected []string
		if err = database.WithTenant(bg, f.App, &c.p.scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, qerr := tx.Query(ctx, `SELECT id::text FROM job WHERE id = ANY($1::uuid[])`, all)
			if qerr != nil {
				return qerr
			}
			selected, qerr = pgx.CollectRows(rows, pgx.RowTo[string])
			return qerr
		}); err != nil {
			t.Fatalf("select as %s: %v", c.p.name, err)
		}
		for _, id := range all {
			want := slices.Contains(c.want, id)
			if got := slices.Contains(listed, id); got != want {
				t.Errorf("%s lists the %s: %v, want %v", c.p.name, names[id], got, want)
			}
			if _, gerr := s.Get(ctx, f.OrgA, id); (gerr == nil) != want || (gerr != nil && !errors.Is(gerr, jobs.ErrNotFound)) {
				t.Errorf("%s reads the %s: %v, visible %v", c.p.name, names[id], gerr, want)
			}
			// The export rows are visible to their creator only (ownExport):
			// the policies alone show them to every covering scope.
			wantRow := want || (id == exported.ID || id == tracked) && (c.p.name == "org-wide" || c.p.name == "other user of client 1")
			if got := slices.Contains(selected, id); got != wantRow {
				t.Errorf("SELECT as %s shows the %s: %v, want %v", c.p.name, names[id], got, wantRow)
			}
		}
		if total != len(c.want) {
			t.Errorf("%s counts %d jobs, want %d", c.p.name, total, len(c.want))
		}
	}

	// Outside its scope a principal cancels nothing.
	for _, c := range []struct {
		p  principal
		id string
	}{{onSite1, site2Job.ID}, {client2, clientJob.ID}, {client2, exported.ID}, {client1, system.ID}, {inTeam, system.ID}} {
		if _, cerr := s.Cancel(ctxOf(c.p), f.OrgA, c.id); !errors.Is(cerr, jobs.ErrNotFound) {
			t.Errorf("%s cancels the %s: %v, want ErrNotFound", c.p.name, names[c.id], cerr)
		}
	}
	var queued int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM job WHERE id = ANY($1::uuid[]) AND status = 'queued'`,
		[]string{site2Job.ID, clientJob.ID, system.ID, tracked}).Scan(&queued); err != nil || queued != 4 {
		t.Errorf("queued jobs after the refused cancels: %d (%v), want 4", queued, err)
	}

	// WITH CHECK: a client-1 writer stores jobs of its own scope only; a
	// job without snapshot, org-wide in the client dimension or of client
	// 2 is refused, and so is moving its own job out of its scope.
	write := func(p principal, sql string, args ...any) error {
		t.Helper()
		werr := database.WithTenant(bg, f.App, &p.scope, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, sql, args...); e != nil {
				return e
			}
			return errRollback
		})
		if errors.Is(werr, errRollback) {
			return nil
		}
		return werr
	}
	insert := `INSERT INTO job (organization_id, kind, scope_recorded, scope_clients) VALUES ($1, 'wp404.check', $2, $3::uuid[])`
	for _, c := range []struct {
		name     string
		p        principal
		recorded bool
		clients  []string
		ok       bool
	}{
		{"own client", client1, true, []string{f.Client1}, true},
		{"no snapshot", client1, false, nil, false},
		{"org-wide in the client dimension", client1, true, nil, false},
		{"client 2", client1, true, []string{f.Client2}, false},
		{"clients 1 and 2", client1, true, []string{f.Client1, f.Client2}, false},
		{"site writer, sites org-wide", onSite1, true, []string{f.Client1}, false},
		{"org-wide writer, no snapshot", orgWide, false, nil, true},
	} {
		ierr := write(c.p, insert, f.OrgA, c.recorded, c.clients)
		if c.ok && ierr != nil {
			t.Errorf("INSERT %s as %s refused: %v", c.name, c.p.name, ierr)
		}
		if !c.ok && !rlsRefused(ierr) {
			t.Errorf("INSERT %s as %s not refused by row level security: %v", c.name, c.p.name, ierr)
		}
	}
	for name, set := range map[string]string{
		"move to client 2":    `scope_clients = ARRAY['` + f.Client2 + `'::uuid]`,
		"drop the snapshot":   `scope_recorded = false, scope_clients = NULL`,
		"widen to org-wide":   `scope_clients = NULL`,
		"move to another org": `organization_id = '` + f.OrgB + `'`,
	} {
		if uerr := write(client1, `UPDATE job SET `+set+` WHERE id = $1`, clientJob.ID); !rlsRefused(uerr) {
			t.Errorf("UPDATE %s as client 1 not refused by row level security: %v", name, uerr)
		}
	}
	if uerr := write(client1, `UPDATE job SET progress = 5 WHERE id = $1`, clientJob.ID); uerr != nil {
		t.Errorf("UPDATE of the own job as client 1: %v", uerr)
	}
	var pgErr *pgconn.PgError
	if cerr := write(client1, `INSERT INTO job (organization_id, kind, scope_recorded, scope_clients) VALUES ($1, 'wp404.check', false, ARRAY[$2::uuid])`,
		f.OrgA, f.Client1); !errors.As(cerr, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "job_scope_check" {
		t.Errorf("a job without snapshot that names clients: %v, want job_scope_check", cerr)
	}

	// Worker paths stay organization-wide: they claim and finish the job of
	// site 2, whose principal then reads the result.
	claimed, err := s.ClaimJob(bg, f.OrgA, site2Job.ID)
	if err != nil || claimed == nil {
		t.Fatalf("worker claims the site 2 job: %+v %v", claimed, err)
	}
	if _, err = s.Succeed(bg, f.OrgA, site2Job.ID, map[string]int{"n": 1}); err != nil {
		t.Fatal(err)
	}
	if got, gerr := s.Get(ctxOf(onSite2), f.OrgA, site2Job.ID); gerr != nil || got.Status != jobs.StatusSucceeded || string(got.Result) != `{"n": 1}` {
		t.Errorf("site 2 reads its finished job: %+v %v", got, gerr)
	}
	if due, derr := s.Due(bg, 100000, "wp404.work"); derr != nil || !slices.ContainsFunc(due, func(j jobs.Job) bool { return j.ID == site1Job.ID }) {
		t.Errorf("the system path lists the due jobs of every scope: %d jobs, %v", len(due), derr)
	}

	// The catalog: ENABLE and FORCE, the scope GUCs in USING and WITH CHECK
	// of job_isolation, the restrictive SELECT policy, job_system_select as
	// in migration 000115.
	var rls, force bool
	if err = f.Admin.QueryRow(bg, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'job'::regclass`).Scan(&rls, &force); err != nil || !rls || !force {
		t.Errorf("job: rls %v, force %v (%v)", rls, force, err)
	}
	rows, err := f.Admin.Query(bg, `SELECT policyname, permissive, cmd, COALESCE(qual, ''), COALESCE(with_check, '') FROM pg_policies WHERE tablename = 'job'`)
	if err != nil {
		t.Fatal(err)
	}
	policies := map[string][4]string{}
	for rows.Next() {
		var name, permissive, cmd, qual, check string
		if err = rows.Scan(&name, &permissive, &cmd, &qual, &check); err != nil {
			t.Fatal(err)
		}
		policies[name] = [4]string{permissive, cmd, qual, check}
	}
	rows.Close()
	if len(policies) != 3 {
		t.Errorf("job policies %v, want job_isolation, job_scope_select and job_system_select", policies)
	}
	for _, guc := range []string{database.ClientScopeGUC, database.SiteScopeGUC, database.TeamScopeGUC} {
		iso, sel := policies["job_isolation"], policies["job_scope_select"]
		if iso[0] != "PERMISSIVE" || iso[1] != "ALL" || !strings.Contains(iso[2], "'"+guc+"'") || !strings.Contains(iso[3], "'"+guc+"'") ||
			!strings.Contains(iso[2], "'"+database.OrgGUC+"'") || !strings.Contains(iso[3], "'"+database.OrgGUC+"'") {
			t.Errorf("job_isolation does not check %s and the organization in USING and WITH CHECK: %v", guc, iso)
		}
		if sel[0] != "RESTRICTIVE" || sel[1] != "SELECT" || !strings.Contains(sel[2], "'"+guc+"'") {
			t.Errorf("job_scope_select does not check %s: %v", guc, sel)
		}
	}
	if sys := policies["job_system_select"]; sys[0] != "PERMISSIVE" || sys[1] != "SELECT" ||
		sys[2] != "((organization_id = (NULLIF(current_setting('app.org_id'::text, true), ''::text))::uuid) OR (current_setting('app.system'::text, true) = 'on'::text))" {
		t.Errorf("job_system_select changed: %v", sys)
	}
}

// mustRefJob returns the generic job of an export row, read without RLS.
func mustRefJob(t *testing.T, f *scopetest.Fixture, exportID string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM job WHERE ref_table = 'export_job' AND ref_id = $1`, exportID).Scan(&id); err != nil {
		t.Fatalf("generic job of export %s: %v", exportID, err)
	}
	return id
}

// rlsRefused reports whether err is a row level security violation.
func rlsRefused(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.Contains(pgErr.Message, "row-level security")
}
