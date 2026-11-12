package discovery_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// Schema of the collector groups (PRB-03, PRB-11, COL-06; WP-936, migration
// <n>_collector_group): collector_group, collector_group_member,
// probe_assignment and probe.collector_group_id with a group of its own for
// every collector. Seeds go through the maintenance pool, scope checks run
// as the restricted runtime login (scopetest.AppDSN).

type cgEnv struct {
	t *testing.T
	f *scopetest.Fixture
}

func newCGEnv(t *testing.T) *cgEnv {
	t.Helper()
	return &cgEnv{t: t, f: scopetest.Seed(t, "94")}
}

func (e *cgEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *cgEnv) exec(sql string, args ...any) error {
	_, err := e.f.Admin.Exec(context.Background(), sql, args...)
	return err
}

func (e *cgEnv) mustExec(sql string, args ...any) {
	e.t.Helper()
	if err := e.exec(sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func orNull(id string) any {
	if id == "" {
		return nil
	}
	return id
}

// collector inserts a collector (clientID empty: org-wide) and returns its id.
func (e *cgEnv) collector(orgID, clientID, name string) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO collector (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
		orgID, orNull(clientID), name)
}

// group inserts a collector group without members.
func (e *cgEnv) group(orgID, clientID, siteID, name string) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO collector_group (organization_id, client_id, site_id, name) VALUES ($1, $2, $3, $4) RETURNING id::text`,
		orgID, orNull(clientID), orNull(siteID), name)
}

// groupOf returns the group of the collector.
func (e *cgEnv) groupOf(collectorID string) string {
	e.t.Helper()
	return e.scalar(`SELECT group_id::text FROM collector_group_member WHERE collector_id = $1`, collectorID)
}

func (e *cgEnv) site(clientID, name string) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, e.f.OrgA, clientID, name)
}

// probe inserts an icmp probe on the CI in the collector group.
func (e *cgEnv) probe(orgID, ciID, groupID, target string) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', $4) RETURNING id::text`,
		orgID, ciID, groupID, target)
}

// visible reports whether the principal of ctx sees the row of table.
func (e *cgEnv) visible(ctx context.Context, orgID, table, column, id string) bool {
	e.t.Helper()
	var ok bool
	if err := database.WithRequestTenant(ctx, e.f.App, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE `+column+` = $1)`, id).Scan(&ok)
	}); err != nil {
		e.t.Fatalf("visible %s: %v", table, err)
	}
	return ok
}

func pgCode(err error) (code, constraint string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName
	}
	return "", ""
}

// migrationBlock returns the SQL between "-- <marker>:begin" and
// "-- <marker>:end" of the collector group migration (up or down).
func migrationBlock(t *testing.T, direction, marker string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "..", "migrations", "*_collector_group."+direction+".sql"))
	if err != nil || len(files) != 1 {
		t.Fatalf("collector group migration (%s): %v %v", direction, files, err)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "-- "+marker+":begin")
	block, _, ok2 := strings.Cut(rest, "-- "+marker+":end")
	if !ok || !ok2 {
		t.Fatalf("markers %s missing in %s", marker, files[0])
	}
	return block
}

// TestDefaultGroupPerExistingCollector covers PRB-03 and COL-06 (WP-936
// AK5): after the migration every collector has exactly one group with
// itself as member; the data migration gives a collector without group a
// group of its own with its name, client and site in mode distributed.
func TestDefaultGroupPerExistingCollector(t *testing.T) {
	e := newCGEnv(t)
	e.collector(e.f.OrgA, e.f.Client1, "wp936-existing")
	if n := e.scalar(`SELECT count(*)::text FROM collector c
		WHERE (SELECT count(*) FROM collector_group_member m WHERE m.collector_id = c.id) <> 1`); n != "0" {
		t.Errorf("%s collectors without exactly one group", n)
	}

	// The data migration on a collector created without the trigger.
	ctx := context.Background()
	tx, err := e.f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `ALTER TABLE collector DISABLE TRIGGER collector_default_group`); err != nil {
		t.Fatal(err)
	}
	site := ""
	if err = tx.QueryRow(ctx, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp936-backfill') RETURNING id::text`,
		e.f.OrgA, e.f.Client1).Scan(&site); err != nil {
		t.Fatal(err)
	}
	var legacy string
	if err = tx.QueryRow(ctx, `INSERT INTO collector (organization_id, site_id, name) VALUES ($1, $2, 'wp936-legacy') RETURNING id::text`,
		e.f.OrgA, site).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, migrationBlock(t, "up", "default-groups")); err != nil {
		t.Fatalf("data migration: %v", err)
	}
	var name, client, groupSite, mode string
	var members int
	if err = tx.QueryRow(ctx, `SELECT g.name, COALESCE(g.client_id::text, ''), COALESCE(g.site_id::text, ''), g.mode,
			(SELECT count(*) FROM collector_group_member x WHERE x.group_id = g.id)
		FROM collector_group_member m JOIN collector_group g ON g.id = m.group_id WHERE m.collector_id = $1`, legacy).
		Scan(&name, &client, &groupSite, &mode, &members); err != nil {
		t.Fatalf("group of the legacy collector: %v", err)
	}
	if name != "wp936-legacy" || client != e.f.Client1 || groupSite != site || mode != "distributed" || members != 1 {
		t.Errorf("group of the legacy collector: %s %s %s %s %d", name, client, groupSite, mode, members)
	}
	var n int
	if err = tx.QueryRow(ctx, `SELECT count(*) FROM collector c
		WHERE (SELECT count(*) FROM collector_group_member m WHERE m.collector_id = c.id) <> 1`).Scan(&n); err != nil || n != 0 {
		t.Errorf("after the data migration %d collectors without exactly one group (%v)", n, err)
	}
}

// TestNewCollectorGetsOwnGroup covers WP-936 AK5: a new collector gets a
// group of its own (name, client, site, distributed) with itself as member,
// also when a principal of its client creates it.
func TestNewCollectorGetsOwnGroup(t *testing.T) {
	e := newCGEnv(t)
	c := e.collector(e.f.OrgA, e.f.Client1, "wp936-new")
	got := e.scalar(`SELECT g.name || '/' || COALESCE(g.client_id::text, '') || '/' || g.mode || '/' || g.status || '/' ||
			extract(epoch FROM g.liveness_timeout)::int || '/' || m.weight || '/' || m.member_status || '/' || m.config_version
		FROM collector_group_member m JOIN collector_group g ON g.id = m.group_id WHERE m.collector_id = $1`, c)
	if want := "wp936-new/" + e.f.Client1 + "/distributed/reachable/30/100/healthy/0"; got != want {
		t.Errorf("group of the new collector %s, want %s", got, want)
	}

	var scoped string
	if err := database.WithRequestTenant(e.f.ClientCtx(e.f.Client1), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO collector (organization_id, client_id, name) VALUES (current_setting('app.org_id')::uuid, $1, 'wp936-scoped')
			RETURNING id::text`, e.f.Client1).Scan(&scoped)
	}); err != nil {
		t.Fatalf("collector of a client principal: %v", err)
	}
	if g := e.groupOf(scoped); !e.visible(e.f.ClientCtx(e.f.Client1), e.f.OrgA, "collector_group", "id", g) {
		t.Errorf("client 1 does not see the group %s of its collector", g)
	}
	// The single member carries its group to a new site; an org-wide
	// collector placed at a site of client 2 carries its group to client 2.
	site1 := e.site(e.f.Client1, "wp936-new-site")
	e.mustExec(`UPDATE collector SET site_id = $2 WHERE id = $1`, c, site1)
	if got := e.scalar(`SELECT client_id::text || '/' || site_id::text FROM collector_group WHERE id = $1`, e.groupOf(c)); got != e.f.Client1+"/"+site1 {
		t.Errorf("group after moving its only collector: %s", got)
	}
	cOrg := e.collector(e.f.OrgA, "", "wp936-new-org")
	site2 := e.site(e.f.Client2, "wp936-new-site-2")
	e.mustExec(`UPDATE collector SET site_id = $2 WHERE id = $1`, cOrg, site2)
	if got := e.scalar(`SELECT client_id::text || '/' || site_id::text FROM collector_group WHERE id = $1`, e.groupOf(cOrg)); got != e.f.Client2+"/"+site2 {
		t.Errorf("group after placing its org-wide collector: %s", got)
	}
}

// TestGroupMemberMustMatchClient covers WP-936 AK3: a collector of client 1
// does not join a group of client 2 or an org-wide group (23514); an
// org-wide collector joins only an org-wide group.
func TestGroupMemberMustMatchClient(t *testing.T) {
	e := newCGEnv(t)
	c1 := e.collector(e.f.OrgA, e.f.Client1, "wp936-c1")
	cOrg := e.collector(e.f.OrgA, "", "wp936-org")
	g2 := e.group(e.f.OrgA, e.f.Client2, "", "wp936-g2")
	gOrg := e.group(e.f.OrgA, "", "", "wp936-gorg")
	g1 := e.group(e.f.OrgA, e.f.Client1, "", "wp936-g1")
	e.mustExec(`DELETE FROM collector_group_member WHERE collector_id IN ($1, $2)`, c1, cOrg)
	join := func(group, collector string) error {
		return e.exec(`INSERT INTO collector_group_member (group_id, collector_id, organization_id) VALUES ($1, $2, $3)`, group, collector, e.f.OrgA)
	}
	for name, c := range map[string][2]string{
		"client 1 collector in a client 2 group":  {g2, c1},
		"client 1 collector in an org-wide group": {gOrg, c1},
		"org-wide collector in a client 1 group":  {g1, cOrg},
	} {
		if code, constraint := pgCode(join(c[0], c[1])); code != "23514" || constraint != "collector_group_member_client_rule" {
			t.Errorf("%s: %s %s, want 23514 collector_group_member_client_rule", name, code, constraint)
		}
	}
	if err := join(g1, c1); err != nil {
		t.Errorf("client 1 collector in a client 1 group: %v", err)
	}
	if err := join(gOrg, cOrg); err != nil {
		t.Errorf("org-wide collector in an org-wide group: %v", err)
	}
	// A collector of a shared group cannot move to another client.
	c1b := e.collector(e.f.OrgA, e.f.Client1, "wp936-c1b")
	e.mustExec(`DELETE FROM collector_group_member WHERE collector_id = $1`, c1b)
	if err := join(g1, c1b); err != nil {
		t.Fatal(err)
	}
	if _, constraint := pgCode(e.exec(`UPDATE collector SET client_id = $2 WHERE id = $1`, c1b, e.f.Client2)); constraint != "collector_group_member_client_rule" {
		t.Errorf("moving a member of a shared group to client 2: %s", constraint)
	}
	// A site of a group belongs to its client.
	site2 := e.site(e.f.Client2, "wp936-g-site")
	if _, constraint := pgCode(e.exec(`UPDATE collector_group SET site_id = $2 WHERE id = $1`, g1, site2)); constraint != "collector_group_site_client" {
		t.Errorf("group of client 1 at a site of client 2: %s", constraint)
	}
	for name, stmt := range map[string]string{
		"mode":         `UPDATE collector_group SET mode = 'active' WHERE id = $1`,
		"liveness":     `UPDATE collector_group SET liveness_timeout = '10 seconds' WHERE id = $1`,
		"liveness max": `UPDATE collector_group SET liveness_timeout = '301 seconds' WHERE id = $1`,
		"status":       `UPDATE collector_group SET status = 'down' WHERE id = $1`,
	} {
		if code, _ := pgCode(e.exec(stmt, g1)); code != "23514" {
			t.Errorf("%s out of range: %s, want 23514", name, code)
		}
	}
	if err := e.exec(`UPDATE collector_group SET mode = 'failover', liveness_timeout = '300 seconds' WHERE id = $1`, g1); err != nil {
		t.Errorf("failover with 300 s: %v", err)
	}
}

// TestCollectorInOneGroupOnly covers WP-936 AK3: a second membership of a
// collector is 23505.
func TestCollectorInOneGroupOnly(t *testing.T) {
	e := newCGEnv(t)
	c := e.collector(e.f.OrgA, "", "wp936-one")
	other := e.group(e.f.OrgA, "", "", "wp936-other")
	err := e.exec(`INSERT INTO collector_group_member (group_id, collector_id, organization_id) VALUES ($1, $2, $3)`, other, c, e.f.OrgA)
	if code, constraint := pgCode(err); code != "23505" || constraint != "collector_group_member_collector_key" {
		t.Errorf("second membership: %s %s, want 23505", code, constraint)
	}
	if code, _ := pgCode(e.exec(`UPDATE collector_group_member SET weight = 0 WHERE collector_id = $1`, c)); code != "23514" {
		t.Errorf("weight 0: %s, want 23514", code)
	}
}

// TestProbeGroupMustMatchClient covers WP-936 AK4: the collector group of a
// probe is one of the probe's client or an org-wide group (23514
// probe_collector_group_client).
func TestProbeGroupMustMatchClient(t *testing.T) {
	e := newCGEnv(t)
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp936-probe-ci")
	g1 := e.group(e.f.OrgA, e.f.Client1, "", "wp936-pg1")
	g2 := e.group(e.f.OrgA, e.f.Client2, "", "wp936-pg2")
	gOrg := e.group(e.f.OrgA, "", "", "wp936-pgorg")
	insert := func(group, target string) error {
		return e.exec(`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', $4)`,
			e.f.OrgA, ci1, group, target)
	}
	if code, constraint := pgCode(insert(g2, "192.0.2.60")); code != "23514" || constraint != "probe_collector_group_client" {
		t.Errorf("probe of client 1 in a group of client 2: %s %s", code, constraint)
	}
	if err := insert(g1, "192.0.2.61"); err != nil {
		t.Errorf("group of the client: %v", err)
	}
	if err := insert(gOrg, "192.0.2.62"); err != nil {
		t.Errorf("org-wide group: %v", err)
	}
	if code, _ := pgCode(e.exec(`INSERT INTO probe (organization_id, ci_id, type, target) VALUES ($1, $2, 'icmp', '192.0.2.63')`, e.f.OrgA, ci1)); code != "23502" {
		t.Errorf("probe without collector group: %s, want 23502", code)
	}
	// Moving the CI to client 2 breaks the rule of the client 1 group.
	if _, constraint := pgCode(e.exec(`UPDATE ci SET client_id = $2 WHERE id = $1`, ci1, e.f.Client2)); constraint != "probe_collector_group_client" {
		t.Errorf("moving the CI of a probe in a client 1 group: %s", constraint)
	}
	// ... as does moving the group, and deleting a group with probes.
	if _, constraint := pgCode(e.exec(`UPDATE collector_group SET client_id = $2 WHERE id = $1`, g1, e.f.Client2)); constraint != "probe_collector_group_client" {
		t.Errorf("moving the group of a client 1 probe: %s", constraint)
	}
	if code, _ := pgCode(e.exec(`DELETE FROM collector_group WHERE id = $1`, gOrg)); code != "23001" && code != "23503" {
		t.Errorf("deleting a group with probes: %s, want a restrict violation", code)
	}
}

// TestCollectorGroupIsolation covers WP-936 AK6: organization B and a
// principal of client 2 see no group, membership or assignment of
// organization A's client 1; a collector or probe of organization B is
// 23503.
func TestCollectorGroupIsolation(t *testing.T) {
	e := newCGEnv(t)
	c1 := e.collector(e.f.OrgA, e.f.Client1, "wp936-iso")
	g1 := e.groupOf(c1)
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp936-iso-ci")
	p1 := e.probe(e.f.OrgA, ci1, g1, "192.0.2.70")
	e.mustExec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, 1)`, p1, e.f.OrgA, c1)
	cB := e.collector(e.f.OrgB, "", "wp936-iso-b")
	gB := e.groupOf(cB)
	ciB := e.f.CI(t, e.f.OrgB, "", "wp936-iso-ci-b")
	pB := e.probe(e.f.OrgB, ciB, gB, "192.0.2.71")
	gOrgA := e.group(e.f.OrgA, "", "", "wp936-iso-org")
	p1b := e.probe(e.f.OrgA, ci1, g1, "192.0.2.72")
	// Without its own membership the collector of B reaches the foreign key.
	e.mustExec(`DELETE FROM collector_group_member WHERE collector_id = $1`, cB)

	for _, c := range []struct{ table, column, id string }{
		{"collector_group", "id", g1}, {"collector_group_member", "collector_id", c1}, {"probe_assignment", "probe_id", p1},
	} {
		if e.visible(e.f.OrgCtx(e.f.OrgB), e.f.OrgB, c.table, c.column, c.id) {
			t.Errorf("organization B sees %s of organization A", c.table)
		}
		if e.visible(e.f.ClientCtx(e.f.Client2), e.f.OrgA, c.table, c.column, c.id) {
			t.Errorf("client 2 sees %s of client 1", c.table)
		}
		if !e.visible(e.f.ClientCtx(e.f.Client1), e.f.OrgA, c.table, c.column, c.id) {
			t.Errorf("client 1 misses its %s", c.table)
		}
	}
	for name, c := range map[string]struct {
		sql  string
		args []any
	}{
		"member collector": {`INSERT INTO collector_group_member (group_id, collector_id, organization_id) VALUES ($1, $2, $3)`,
			[]any{gOrgA, cB, e.f.OrgA}},
		"member group":         {`INSERT INTO collector_group_member (group_id, collector_id, organization_id) VALUES ($1, gen_random_uuid(), $2)`, []any{gB, e.f.OrgA}},
		"assignment collector": {`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, 2)`, []any{p1b, e.f.OrgA, cB}},
		"assignment probe":     {`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, 1)`, []any{pB, e.f.OrgA, c1}},
		"probe group":          {`INSERT INTO probe (organization_id, ci_id, collector_group_id, type, target) VALUES ($1, $2, $3, 'icmp', 'x')`, []any{e.f.OrgA, ci1, gB}},
		"group anchor probe":   {`UPDATE collector_group SET anchor_probe_id = $2 WHERE id = $1`, []any{g1, pB}},
	} {
		if code, _ := pgCode(e.exec(c.sql, c.args...)); code != "23503" {
			t.Errorf("%s of organization B: %s, want 23503", name, code)
		}
	}
	// Organization B cannot write into organization A.
	err := database.WithRequestTenant(e.f.OrgCtx(e.f.OrgB), e.f.App, e.f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp936-foreign')`, e.f.OrgA)
		return err
	})
	if code, _ := pgCode(err); code != "42501" {
		t.Errorf("insert into organization A as B: %s, want 42501", code)
	}
	// The anchor probe goes with its probe (SET NULL).
	e.mustExec(`UPDATE collector_group SET anchor_probe_id = $2 WHERE id = $1`, g1, p1)
	e.mustExec(`DELETE FROM probe WHERE id = $1`, p1)
	if got := e.scalar(`SELECT COALESCE(anchor_probe_id::text, 'null') FROM collector_group WHERE id = $1`, g1); got != "null" {
		t.Errorf("anchor after deleting the probe: %s", got)
	}
}

// TestMemberAndAssignmentTakeScopeFromParent covers WP-936 AK3, AK4 and
// PRB-11: client and site of a member are its group's, those of an
// assignment its probe's (also after a move); a principal of client 2 sees
// neither.
func TestMemberAndAssignmentTakeScopeFromParent(t *testing.T) {
	e := newCGEnv(t)
	site1 := e.site(e.f.Client1, "wp936-scope-site")
	c := e.scalar(`INSERT INTO collector (organization_id, site_id, name) VALUES ($1, $2, 'wp936-scope') RETURNING id::text`, e.f.OrgA, site1)
	g := e.groupOf(c)
	e.mustExec(`UPDATE collector_group_member SET priority = 5, last_alive_at = now(), clock_offset_ms = 12, capacity_hint = 500, config_version = 3
		WHERE collector_id = $1`, c)
	if got := e.scalar(`SELECT client_id::text || '/' || site_id::text FROM collector_group_member WHERE collector_id = $1`, c); got != e.f.Client1+"/"+site1 {
		t.Errorf("member scope %s, want client 1 and its site", got)
	}
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp936-scope-ci")
	e.mustExec(`UPDATE ci SET location_id = $2 WHERE id = $1`, ci, site1)
	p := e.probe(e.f.OrgA, ci, g, "192.0.2.80")
	e.mustExec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch, client_id, site_id) VALUES ($1, $2, $3, 7, $4, NULL)`,
		p, e.f.OrgA, c, e.f.Client2)
	if got := e.scalar(`SELECT client_id::text || '/' || site_id::text || '/' || epoch FROM probe_assignment WHERE probe_id = $1`, p); got != e.f.Client1+"/"+site1+"/7" {
		t.Errorf("assignment scope %s, want client 1, site 1 and epoch 7", got)
	}
	c2 := e.f.ClientCtx(e.f.Client2)
	if e.visible(c2, e.f.OrgA, "collector_group_member", "collector_id", c) || e.visible(c2, e.f.OrgA, "probe_assignment", "probe_id", p) {
		t.Error("client 2 sees the member or the assignment of client 1")
	}
	c1 := e.f.ClientCtx(e.f.Client1)
	if !e.visible(c1, e.f.OrgA, "collector_group_member", "collector_id", c) || !e.visible(c1, e.f.OrgA, "probe_assignment", "probe_id", p) {
		t.Error("client 1 misses its member or assignment")
	}
	// The assignment follows its probe when the CI leaves the site.
	e.mustExec(`UPDATE ci SET location_id = NULL WHERE id = $1`, ci)
	if got := e.scalar(`SELECT client_id::text || '/' || COALESCE(site_id::text, 'none') FROM probe_assignment WHERE probe_id = $1`, p); got != e.f.Client1+"/none" {
		t.Errorf("assignment after the CI left its site: %s", got)
	}
	if code, _ := pgCode(e.exec(`UPDATE probe_assignment SET epoch = 0 WHERE probe_id = $1`, p)); code != "23514" {
		t.Errorf("epoch 0: %s, want 23514", code)
	}
}

// TestCollectorKeyCreatedOnlyIfMissing covers WP-936 AK1: the key block of
// the migration runs without error when the key exists; the down block
// keeps the key while a foreign key references it and drops it otherwise;
// the up block creates a missing key.
func TestCollectorKeyCreatedOnlyIfMissing(t *testing.T) {
	e := newCGEnv(t)
	ctx := context.Background()
	keys := func(tx pgx.Tx) int {
		t.Helper()
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_constraint WHERE conrelid = 'collector'::regclass
			AND conname = 'collector_id_organization_key'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	tx, err := e.f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	up, down := migrationBlock(t, "up", "collector-key"), migrationBlock(t, "down", "collector-key")
	if keys(tx) != 1 {
		t.Fatal("collector_id_organization_key missing after the migration")
	}
	if _, err = tx.Exec(ctx, up); err != nil || keys(tx) != 1 {
		t.Fatalf("key block with an existing key: %v, %d keys", err, keys(tx))
	}
	if _, err = tx.Exec(ctx, down); err != nil || keys(tx) != 1 {
		t.Fatalf("down block while referenced: %v, %d keys, want the key kept", err, keys(tx))
	}
	// Drop every foreign key on the key (collector_group_member,
	// probe_assignment and any later one) inside the transaction.
	if _, err = tx.Exec(ctx, `DO $$
		DECLARE r record;
		BEGIN
			FOR r IN SELECT f.conrelid::regclass AS tbl, f.conname FROM pg_constraint f
				JOIN pg_constraint k ON k.conrelid = 'collector'::regclass AND k.conname = 'collector_id_organization_key'
				WHERE f.contype = 'f' AND f.confrelid = 'collector'::regclass AND f.conindid = k.conindid
			LOOP
				EXECUTE format('ALTER TABLE %s DROP CONSTRAINT %I', r.tbl, r.conname);
			END LOOP;
		END $$`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, down); err != nil || keys(tx) != 0 {
		t.Fatalf("down block without references: %v, %d keys, want none", err, keys(tx))
	}
	if _, err = tx.Exec(ctx, up); err != nil || keys(tx) != 1 {
		t.Fatalf("key block with a missing key: %v, %d keys", err, keys(tx))
	}
}

// TestAssignmentRequiresGroupMember covers WP-936 AK4 and PRB-03: a probe
// is assigned only to a member of its collector group (23514
// probe_assignment_member_rule), never to a collector of another client;
// an assignment goes when its probe changes the group or its collector
// leaves the group.
func TestAssignmentRequiresGroupMember(t *testing.T) {
	e := newCGEnv(t)
	c1 := e.collector(e.f.OrgA, e.f.Client1, "wp936-assign-c1")
	c2 := e.collector(e.f.OrgA, e.f.Client2, "wp936-assign-c2")
	g1 := e.groupOf(c1)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp936-assign-ci")
	p := e.probe(e.f.OrgA, ci, g1, "192.0.2.90")
	assign := func(collector string) error {
		return e.exec(`INSERT INTO probe_assignment (probe_id, organization_id, collector_id, epoch) VALUES ($1, $2, $3, 1)
			ON CONFLICT (probe_id) DO UPDATE SET collector_id = EXCLUDED.collector_id, epoch = probe_assignment.epoch + 1`, p, e.f.OrgA, collector)
	}
	if code, constraint := pgCode(assign(c2)); code != "23514" || constraint != "probe_assignment_member_rule" {
		t.Errorf("probe of client 1 assigned to a collector of client 2: %s %s, want 23514 probe_assignment_member_rule", code, constraint)
	}
	if err := assign(c1); err != nil {
		t.Fatalf("assignment to the group member: %v", err)
	}
	if code, constraint := pgCode(assign(c2)); code != "23514" || constraint != "probe_assignment_member_rule" {
		t.Errorf("reassigning to a collector of client 2: %s %s", code, constraint)
	}
	count := func() string {
		return e.scalar(`SELECT count(*)::text FROM probe_assignment WHERE probe_id = $1`, p)
	}
	// The probe moves to another group: the assignment goes.
	other := e.group(e.f.OrgA, e.f.Client1, "", "wp936-assign-other")
	e.mustExec(`UPDATE probe SET collector_group_id = $2 WHERE id = $1`, p, other)
	if got := count(); got != "0" {
		t.Errorf("assignments after the probe changed its group: %s, want 0", got)
	}
	// The collector leaves the group (moved, then deleted): the assignment
	// goes; other members keep theirs.
	e.mustExec(`UPDATE probe SET collector_group_id = $2 WHERE id = $1`, p, g1)
	c1b := e.collector(e.f.OrgA, e.f.Client1, "wp936-assign-c1b")
	e.mustExec(`DELETE FROM collector_group_member WHERE collector_id = $1`, c1b)
	e.mustExec(`INSERT INTO collector_group_member (group_id, collector_id, organization_id) VALUES ($1, $2, $3)`, g1, c1b, e.f.OrgA)
	if err := assign(c1); err != nil {
		t.Fatal(err)
	}
	e.mustExec(`UPDATE collector_group_member SET group_id = $2 WHERE collector_id = $1`, c1, other)
	if got := count(); got != "0" {
		t.Errorf("assignments after the collector moved to another group: %s, want 0", got)
	}
	if err := assign(c1b); err != nil {
		t.Fatal(err)
	}
	e.mustExec(`UPDATE collector_group_member SET priority = 3, config_version = 9 WHERE collector_id = $1`, c1b)
	if got := count(); got != "1" {
		t.Errorf("assignments after an update of the member: %s, want 1", got)
	}
	e.mustExec(`DELETE FROM collector_group_member WHERE collector_id = $1`, c1b)
	if got := count(); got != "0" {
		t.Errorf("assignments after the collector left the group: %s, want 0", got)
	}
}

// TestCollectorGroupNameFromCollectorName covers WP-936 AK5: collector.name
// has no length or blank check; the own group of a collector takes the
// name trimmed to 200 characters, 'collector <id>' when it is blank, both
// for new collectors (trigger) and in the data migration.
func TestCollectorGroupNameFromCollectorName(t *testing.T) {
	e := newCGEnv(t)
	long := strings.Repeat("n", 250)
	name := func(collector string) string {
		return e.scalar(`SELECT g.name FROM collector_group_member m JOIN collector_group g ON g.id = m.group_id WHERE m.collector_id = $1`, collector)
	}
	cLong := e.collector(e.f.OrgA, "", long)
	cBlank := e.collector(e.f.OrgA, "", "   ")
	if got := name(cLong); got != long[:200] {
		t.Errorf("group of a 250-character name: %d characters", len(got))
	}
	if got := name(cBlank); got != "collector "+cBlank {
		t.Errorf("group of a blank name: %q", got)
	}

	ctx := context.Background()
	tx, err := e.f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `ALTER TABLE collector DISABLE TRIGGER collector_default_group`); err != nil {
		t.Fatal(err)
	}
	var legacyLong, legacyBlank string
	for _, c := range []struct {
		name string
		id   *string
	}{{long, &legacyLong}, {"  ", &legacyBlank}} {
		if err = tx.QueryRow(ctx, `INSERT INTO collector (organization_id, name) VALUES ($1, $2) RETURNING id::text`, e.f.OrgA, c.name).Scan(c.id); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = tx.Exec(ctx, migrationBlock(t, "up", "default-groups")); err != nil {
		t.Fatalf("data migration with a long and a blank collector name: %v", err)
	}
	for id, want := range map[string]string{legacyLong: long[:200], legacyBlank: "collector " + legacyBlank} {
		var got string
		if err = tx.QueryRow(ctx, `SELECT g.name FROM collector_group_member m JOIN collector_group g ON g.id = m.group_id WHERE m.collector_id = $1`, id).Scan(&got); err != nil || got != want {
			t.Errorf("data migration: group name %q (%v), want %q", got, err, want)
		}
	}
}

// TestCollectorMoveWithClientProbesRefused: a collector whose own group
// holds probes of its client cannot move to another client (23514
// probe_collector_group_client; the API maps it to 422 on site_id).
func TestCollectorMoveWithClientProbesRefused(t *testing.T) {
	e := newCGEnv(t)
	c := e.collector(e.f.OrgA, e.f.Client1, "wp936-move")
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp936-move-ci")
	e.probe(e.f.OrgA, ci, e.groupOf(c), "192.0.2.95")
	site2 := e.site(e.f.Client2, "wp936-move-site")
	if code, constraint := pgCode(e.exec(`UPDATE collector SET client_id = $2, site_id = $3 WHERE id = $1`, c, e.f.Client2, site2)); code != "23514" || constraint != "probe_collector_group_client" {
		t.Errorf("moving a collector with client 1 probes to client 2: %s %s, want 23514 probe_collector_group_client", code, constraint)
	}
}
