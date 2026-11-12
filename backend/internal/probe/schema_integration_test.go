package probe_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
)

// Schema of the probe definitions (PRB-01, WP-935, migration
// 000307_probe_definition): probe, monitoring_group, monitoring_group_member
// and monitoring_profile with RLS, scope policies, scope triggers and
// composite foreign keys. Seeds go through the maintenance pool, the checks
// run as the restricted runtime login (scopetest.AppDSN).

type schemaEnv struct {
	t *testing.T
	f *scopetest.Fixture
}

func newSchemaEnv(t *testing.T) *schemaEnv {
	t.Helper()
	e := &schemaEnv{t: t, f: scopetest.Seed(t, "93")}
	// Every probe runs in a collector group (WP-936): one org-wide group per
	// organization for the probes of the tests.
	for _, org := range []string{e.f.OrgA, e.f.OrgB} {
		e.mustExec(`INSERT INTO collector_group (organization_id, name) VALUES ($1, 'wp935-probes')`, org)
	}
	return e
}

func (e *schemaEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var out string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&out); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return out
}

func (e *schemaEnv) exec(sql string, args ...any) error {
	_, err := e.f.Admin.Exec(context.Background(), sql, args...)
	return err
}

func (e *schemaEnv) mustExec(sql string, args ...any) {
	e.t.Helper()
	if err := e.exec(sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func nullable(id string) any {
	if id == "" {
		return nil
	}
	return id
}

func (e *schemaEnv) site(orgID, clientID, name string) string {
	return e.scalar(`INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, orgID, clientID, name)
}

// place puts the CI at the site node of the location tree (the node has the
// site's id; the site of a CI follows its location); clientID non-empty
// moves it to that client.
func (e *schemaEnv) place(ciID, clientID, siteID string) {
	e.t.Helper()
	e.mustExec(`UPDATE ci SET client_id = COALESCE($2::uuid, client_id),
		location_id = $3 WHERE id = $1`,
		ciID, nullable(clientID), siteID)
}

func (e *schemaEnv) group(orgID, clientID, name string) string {
	return e.scalar(`INSERT INTO monitoring_group (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
		orgID, nullable(clientID), name)
}

func (e *schemaEnv) probeOnCI(orgID, ciID, target string) string {
	return e.scalar(`INSERT INTO probe (organization_id, collector_group_id, ci_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'icmp', $3) RETURNING id::text`,
		orgID, ciID, target)
}

func (e *schemaEnv) probeOfClient(orgID, clientID, target string) string {
	return e.scalar(`INSERT INTO probe (organization_id, collector_group_id, client_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'tcp', $3) RETURNING id::text`,
		orgID, clientID, target)
}

// as runs fn as the runtime login with the scope of ctx (OrgCtx/ClientCtx).
func (e *schemaEnv) as(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	return database.WithRequestTenant(ctx, e.f.App, orgID, fn)
}

// count counts the visible rows of table, optionally restricted to ids.
func (e *schemaEnv) count(ctx context.Context, orgID, table string) int {
	e.t.Helper()
	var n int
	if err := e.as(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n)
	}); err != nil {
		e.t.Fatalf("count %s: %v", table, err)
	}
	return n
}

func (e *schemaEnv) visible(ctx context.Context, orgID, table, column, id string) bool {
	e.t.Helper()
	var ok bool
	if err := e.as(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM `+table+` WHERE `+column+` = $1)`, id).Scan(&ok)
	}); err != nil {
		e.t.Fatalf("visible %s: %v", table, err)
	}
	return ok
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

func constraintOf(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
}

// TestProbeTablesIsolateOrganizations covers PRB-01 (WP-935 AK5):
// organization B sees no row of organization A in any of the four tables.
func TestProbeTablesIsolateOrganizations(t *testing.T) {
	e := newSchemaEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp935-iso")
	group := e.group(e.f.OrgA, "", "wp935-iso")
	e.mustExec(`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id) VALUES ($1, $2, $3)`, e.f.OrgA, group, ci)
	e.probeOnCI(e.f.OrgA, ci, "192.0.2.1")
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, probe_type, params) VALUES ($1, 'org', 'icmp', '{"interval_s":60}')`, e.f.OrgA)
	e.mustExec(`INSERT INTO monitoring_profile (organization_id, level, ci_id, probe_type, params) VALUES ($1, 'ci', $2, 'icmp', '{}')`, e.f.OrgA, ci)

	for _, table := range []string{"probe", "monitoring_group", "monitoring_group_member", "monitoring_profile"} {
		if n := e.count(e.f.OrgCtx(e.f.OrgB), e.f.OrgB, table); n != 0 {
			t.Errorf("organization B sees %d rows of %s", n, table)
		}
		if n := e.count(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, table); n == 0 {
			t.Errorf("organization A sees no row of %s", table)
		}
	}
	// Organization B cannot write into organization A.
	err := e.as(e.f.OrgCtx(e.f.OrgB), e.f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO monitoring_group (organization_id, name) VALUES ($1, 'wp935-foreign')`, e.f.OrgA)
		return err
	})
	if sqlState(err) != "42501" {
		t.Errorf("insert into organization A as B: %v, want 42501", err)
	}
}

// TestProbeTablesFollowClientScope covers WP-935 AK5: a principal of client
// 1 sees its own and the org-wide groups and profiles, no probe of client 2,
// and cannot write an org-wide group.
func TestProbeTablesFollowClientScope(t *testing.T) {
	e := newSchemaEnv(t)
	g1 := e.group(e.f.OrgA, e.f.Client1, "wp935-c1")
	g2 := e.group(e.f.OrgA, e.f.Client2, "wp935-c2")
	gOrg := e.group(e.f.OrgA, "", "wp935-org")
	p1 := e.probeOfClient(e.f.OrgA, e.f.Client1, "192.0.2.11:22")
	p2 := e.probeOfClient(e.f.OrgA, e.f.Client2, "192.0.2.12:22")
	prof1 := e.scalar(`INSERT INTO monitoring_profile (organization_id, level, client_id, probe_type, params) VALUES ($1, 'client', $2, 'icmp', '{}') RETURNING id::text`, e.f.OrgA, e.f.Client1)
	prof2 := e.scalar(`INSERT INTO monitoring_profile (organization_id, level, client_id, probe_type, params) VALUES ($1, 'client', $2, 'icmp', '{}') RETURNING id::text`, e.f.OrgA, e.f.Client2)
	profOrg := e.scalar(`INSERT INTO monitoring_profile (organization_id, level, probe_type, params) VALUES ($1, 'org', 'icmp', '{}') RETURNING id::text`, e.f.OrgA)

	c1 := e.f.ClientCtx(e.f.Client1)
	for _, c := range []struct {
		table, id string
		want      bool
	}{
		{"monitoring_group", g1, true}, {"monitoring_group", gOrg, true}, {"monitoring_group", g2, false},
		{"probe", p1, true}, {"probe", p2, false},
		{"monitoring_profile", prof1, true}, {"monitoring_profile", profOrg, true}, {"monitoring_profile", prof2, false},
	} {
		if got := e.visible(c1, e.f.OrgA, c.table, "id", c.id); got != c.want {
			t.Errorf("client 1 sees %s %s: %v, want %v", c.table, c.id, got, c.want)
		}
	}
	for name, stmt := range map[string]string{
		"org-wide group":   `INSERT INTO monitoring_group (organization_id, name) VALUES (current_setting('app.org_id')::uuid, 'wp935-org-2')`,
		"org profile":      `INSERT INTO monitoring_profile (organization_id, level, probe_type, params) VALUES (current_setting('app.org_id')::uuid, 'org', 'tcp', '{}')`,
		"group of client2": `INSERT INTO monitoring_group (organization_id, client_id, name) VALUES (current_setting('app.org_id')::uuid, '` + e.f.Client2 + `', 'wp935-c2-2')`,
		"probe of client2": `INSERT INTO probe (organization_id, collector_group_id, client_id, type, target) VALUES (current_setting('app.org_id')::uuid, (SELECT g.id FROM collector_group g WHERE g.organization_id = current_setting('app.org_id')::uuid AND g.client_id IS NULL ORDER BY g.id LIMIT 1), '` + e.f.Client2 + `', 'tcp', 'x:1')`,
	} {
		err := e.as(c1, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, stmt)
			return err
		})
		if sqlState(err) != "42501" {
			t.Errorf("client 1 writes %s: %v, want 42501", name, err)
		}
	}
	// Its own group and probe it writes.
	if err := e.as(c1, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO monitoring_group (organization_id, client_id, name) VALUES (current_setting('app.org_id')::uuid, $1, 'wp935-c1-2')`, e.f.Client1); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE probe SET enabled = false WHERE id = $1`, p1)
		return err
	}); err != nil {
		t.Errorf("client 1 writes its own rows: %v", err)
	}
	// The org-wide group is read-only for client 1: an update changes nothing.
	var n int64
	if err := e.as(c1, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE monitoring_group SET priority = 9 WHERE id = $1`, gOrg)
		n = tag.RowsAffected()
		return err
	}); err != nil || n != 0 {
		t.Errorf("client 1 updates the org-wide group: %d rows, %v", n, err)
	}
}

// TestProbeCrossOrgReferenceRejected covers WP-935 AK4: CI, client, site,
// parent probe and group of another organization are 23503.
func TestProbeCrossOrgReferenceRejected(t *testing.T) {
	e := newSchemaEnv(t)
	clientB := e.scalar(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'wp935 B', 'wp935-b') RETURNING id::text`, e.f.OrgB)
	siteB := e.site(e.f.OrgB, clientB, "wp935-site-b")
	ciB := e.f.CI(t, e.f.OrgB, clientB, "wp935-ci-b")
	probeB := e.probeOnCI(e.f.OrgB, ciB, "192.0.2.20")
	groupB := e.group(e.f.OrgB, "", "wp935-group-b")
	ciA := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp935-ci-a")
	groupA := e.group(e.f.OrgA, "", "wp935-group-a")
	org := e.f.OrgA

	for name, c := range map[string]struct {
		sql  string
		args []any
	}{
		"probe ci":            {`INSERT INTO probe (organization_id, collector_group_id, ci_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'icmp', 'x')`, []any{org, ciB}},
		"probe client":        {`INSERT INTO probe (organization_id, collector_group_id, client_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'icmp', 'x')`, []any{org, clientB}},
		"probe site":          {`INSERT INTO probe (organization_id, collector_group_id, client_id, site_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, $3, 'icmp', 'x')`, []any{org, e.f.Client1, siteB}},
		"probe parent":        {`INSERT INTO probe (organization_id, collector_group_id, ci_id, type, target, parent_probe_id) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'icmp', 'x', $3)`, []any{org, ciA, probeB}},
		"group client":        {`INSERT INTO monitoring_group (organization_id, client_id, name) VALUES ($1, $2, 'x')`, []any{org, clientB}},
		"member group":        {`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id) VALUES ($1, $2, $3)`, []any{org, groupB, ciA}},
		"member ci":           {`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id) VALUES ($1, $2, $3)`, []any{org, groupA, ciB}},
		"profile client":      {`INSERT INTO monitoring_profile (organization_id, level, client_id, probe_type, params) VALUES ($1, 'client', $2, 'icmp', '{}')`, []any{org, clientB}},
		"profile group":       {`INSERT INTO monitoring_profile (organization_id, level, group_id, probe_type, params) VALUES ($1, 'group', $2, 'icmp', '{}')`, []any{org, groupB}},
		"profile ci":          {`INSERT INTO monitoring_profile (organization_id, level, ci_id, probe_type, params) VALUES ($1, 'ci', $2, 'icmp', '{}')`, []any{org, ciB}},
		"binding group":       {`INSERT INTO credential_binding (organization_id, credential_id, target_kind, device_group_id, namespace) VALUES ($1, gen_random_uuid(), 'device_group', $2, 'snmp')`, []any{org, groupB}},
		"probe unknown ci":    {`INSERT INTO probe (organization_id, collector_group_id, ci_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), gen_random_uuid(), 'icmp', 'x')`, []any{org}},
		"profile unknown grp": {`INSERT INTO monitoring_profile (organization_id, level, group_id, probe_type, params) VALUES ($1, 'group', gen_random_uuid(), 'icmp', '{}')`, []any{org}},
	} {
		if err := e.exec(c.sql, c.args...); sqlState(err) != "23503" {
			t.Errorf("%s of organization B: %v, want 23503", name, err)
		}
	}
	// The level CHECK of a profile: exactly the column of the level.
	if err := e.exec(`INSERT INTO monitoring_profile (organization_id, level, client_id, group_id, probe_type, params) VALUES ($1, 'client', $2, $3, 'icmp', '{}')`,
		org, e.f.Client1, groupA); constraintOf(err) != "monitoring_profile_level_target_check" {
		t.Errorf("client profile with a group: %v", err)
	}
	if err := e.exec(`INSERT INTO monitoring_profile (organization_id, level, probe_type, params) VALUES ($1, 'org', 'icmp', '{}')`, org); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(`INSERT INTO monitoring_profile (organization_id, level, probe_type, params) VALUES ($1, 'org', 'icmp', '{"x":1}')`, org); sqlState(err) != "23505" {
		t.Errorf("second org profile of icmp: %v, want 23505", err)
	}
	// Deleting the parent probe keeps the child (SET NULL), deleting the CI
	// removes its probes.
	parent := e.probeOnCI(org, ciA, "192.0.2.30")
	child := e.scalar(`INSERT INTO probe (organization_id, collector_group_id, ci_id, type, target, parent_probe_id) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'tcp', '192.0.2.30:22', $3) RETURNING id::text`, org, ciA, parent)
	e.mustExec(`DELETE FROM probe WHERE id = $1`, parent)
	if got := e.scalar(`SELECT COALESCE(parent_probe_id::text, 'null') FROM probe WHERE id = $1`, child); got != "null" {
		t.Errorf("child after deleting the parent: parent %s", got)
	}
	e.mustExec(`DELETE FROM ci WHERE id = $1`, ciA)
	if got := e.scalar(`SELECT count(*)::text FROM probe WHERE ci_id = $1`, ciA); got != "0" {
		t.Errorf("probes of a deleted CI: %s", got)
	}
}

// TestProbeUniqueTarget covers WP-935 AK1: one probe per CI, type and
// target, also without CI. Without CI the client is part of the key: two
// clients may check the same private target, and the conflict of one
// client reveals nothing about another.
func TestProbeUniqueTarget(t *testing.T) {
	e := newSchemaEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp935-unique")
	e.probeOnCI(e.f.OrgA, ci, "192.0.2.40")
	if err := e.exec(`INSERT INTO probe (organization_id, collector_group_id, ci_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'icmp', '192.0.2.40')`, e.f.OrgA, ci); sqlState(err) != "23505" {
		t.Errorf("same CI, type and target: %v, want 23505", err)
	}
	e.probeOfClient(e.f.OrgA, e.f.Client1, "192.0.2.41:443")
	if err := e.exec(`INSERT INTO probe (organization_id, collector_group_id, client_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'tcp', '192.0.2.41:443')`, e.f.OrgA, e.f.Client1); sqlState(err) != "23505" {
		t.Errorf("same client and target without CI: %v, want 23505", err)
	}
	if err := e.exec(`INSERT INTO probe (organization_id, collector_group_id, client_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'tcp', '192.0.2.41:443')`, e.f.OrgA, e.f.Client2); err != nil {
		t.Errorf("same target without CI for another client: %v, want allowed", err)
	}
	// Another type or target is fine.
	if err := e.exec(`INSERT INTO probe (organization_id, collector_group_id, ci_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, 'tcp', '192.0.2.40')`, e.f.OrgA, ci); err != nil {
		t.Errorf("other type: %v", err)
	}
}

// TestProbeTakesScopeFromCI covers WP-935 AK1: with a CI client and site
// are the CI's (also after a move of the CI); without CI the client is
// required and a site must be the client's.
func TestProbeTakesScopeFromCI(t *testing.T) {
	e := newSchemaEnv(t)
	site1 := e.site(e.f.OrgA, e.f.Client1, "wp935-site-1")
	site2 := e.site(e.f.OrgA, e.f.Client2, "wp935-site-2")
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp935-scope")
	e.place(ci, "", site1)
	p := e.scalar(`INSERT INTO probe (organization_id, collector_group_id, ci_id, client_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, $3, 'icmp', '192.0.2.50') RETURNING id::text`,
		e.f.OrgA, ci, e.f.Client2)
	scope := func() string {
		return e.scalar(`SELECT COALESCE(client_id::text, '') || '/' || COALESCE(site_id::text, '') FROM probe WHERE id = $1`, p)
	}
	if got := scope(); got != e.f.Client1+"/"+site1 {
		t.Errorf("scope %s, want client 1 and site 1 of the CI", got)
	}
	e.place(ci, e.f.Client2, site2)
	if got := scope(); got != e.f.Client2+"/"+site2 {
		t.Errorf("after moving the CI: %s, want client 2 and site 2", got)
	}
	// A profile of the CI takes the CI's client and site, that of a group
	// the group's client.
	prof := e.scalar(`INSERT INTO monitoring_profile (organization_id, level, ci_id, client_id, probe_type, params)
		VALUES ($1, 'ci', $2, $3, 'icmp', '{}') RETURNING id::text`, e.f.OrgA, ci, e.f.Client1)
	if got := e.scalar(`SELECT client_id::text || '/' || site_id::text FROM monitoring_profile WHERE id = $1`, prof); got != e.f.Client2+"/"+site2 {
		t.Errorf("profile of the CI: scope %s, want client 2 and site 2", got)
	}
	g1 := e.group(e.f.OrgA, e.f.Client1, "wp935-scope-group")
	gprof := e.scalar(`INSERT INTO monitoring_profile (organization_id, level, group_id, probe_type, params)
		VALUES ($1, 'group', $2, 'icmp', '{}') RETURNING id::text`, e.f.OrgA, g1)
	if got := e.scalar(`SELECT client_id::text FROM monitoring_profile WHERE id = $1`, gprof); got != e.f.Client1 {
		t.Errorf("profile of the group: client %s, want client 1", got)
	}
	if err := e.exec(`INSERT INTO probe (organization_id, collector_group_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), 'icmp', '192.0.2.51')`, e.f.OrgA); constraintOf(err) != "probe_client_required" {
		t.Errorf("probe without CI and client: %v", err)
	}
	if err := e.exec(`INSERT INTO probe (organization_id, collector_group_id, client_id, site_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, $3, 'icmp', '192.0.2.52')`,
		e.f.OrgA, e.f.Client1, site2); constraintOf(err) != "probe_site_client" {
		t.Errorf("probe of client 1 at a site of client 2: %v", err)
	}
	// A client principal cannot move a probe out of its scope: the client is
	// the CI's, not the written one.
	pc := e.probeOfClient(e.f.OrgA, e.f.Client1, "192.0.2.53:22")
	err := e.as(e.f.ClientCtx(e.f.Client1), e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE probe SET client_id = $2 WHERE id = $1`, pc, e.f.Client2)
		return err
	})
	if sqlState(err) != "42501" {
		t.Errorf("client 1 moves its probe to client 2: %v, want 42501", err)
	}
}

// TestGroupMemberOfOtherClientRejected covers WP-935 AK2: a group of client 1
// takes no CI of client 2 (23514 monitoring_group_member_client_rule); an
// org-wide group takes CIs of every client.
func TestGroupMemberOfOtherClientRejected(t *testing.T) {
	e := newSchemaEnv(t)
	g1 := e.group(e.f.OrgA, e.f.Client1, "wp935-member-c1")
	gOrg := e.group(e.f.OrgA, "", "wp935-member-org")
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp935-member-1")
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp935-member-2")
	ciOrg := e.f.CI(t, e.f.OrgA, "", "wp935-member-org")
	insert := func(group, ci string) error {
		return e.exec(`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id) VALUES ($1, $2, $3)`, e.f.OrgA, group, ci)
	}
	if err := insert(g1, ci1); err != nil {
		t.Fatal(err)
	}
	for name, ci := range map[string]string{"CI of client 2": ci2, "org-wide CI": ciOrg} {
		if err := insert(g1, ci); sqlState(err) != "23514" || constraintOf(err) != "monitoring_group_member_client_rule" {
			t.Errorf("%s in a group of client 1: %v, want 23514 monitoring_group_member_client_rule", name, err)
		}
	}
	for _, ci := range []string{ci1, ci2, ciOrg} {
		if err := insert(gOrg, ci); err != nil {
			t.Errorf("org-wide group: %v", err)
		}
	}
	// A member CI cannot move to another client while in a client's group,
	// and the group cannot change its client.
	if err := e.exec(`UPDATE ci SET client_id = $2 WHERE id = $1`, ci1, e.f.Client2); constraintOf(err) != "monitoring_group_member_client_rule" {
		t.Errorf("moving a member CI to client 2: %v", err)
	}
	if err := e.exec(`UPDATE monitoring_group SET client_id = $2 WHERE id = $1`, g1, e.f.Client2); constraintOf(err) != "monitoring_group_member_client_rule" {
		t.Errorf("moving the group to client 2: %v", err)
	}
}

// TestGroupMemberTakesScopeFromParent covers WP-935 AK2: client and site of
// a membership are those of the member CI; a principal of client 2 sees no
// membership of a CI of client 1 in an org-wide group.
func TestGroupMemberTakesScopeFromParent(t *testing.T) {
	e := newSchemaEnv(t)
	site1 := e.site(e.f.OrgA, e.f.Client1, "wp935-msite-1")
	gOrg := e.group(e.f.OrgA, "", "wp935-parent-org")
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp935-parent-1")
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp935-parent-2")
	e.place(ci1, "", site1)
	e.mustExec(`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id, client_id, site_id) VALUES ($1, $2, $3, $4, NULL)`,
		e.f.OrgA, gOrg, ci1, e.f.Client2)
	e.mustExec(`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id) VALUES ($1, $2, $3)`, e.f.OrgA, gOrg, ci2)
	if got := e.scalar(`SELECT client_id::text || '/' || site_id::text FROM monitoring_group_member WHERE ci_id = $1`, ci1); got != e.f.Client1+"/"+site1 {
		t.Errorf("membership scope %s, want client 1 and site 1 of the CI", got)
	}
	c2 := e.f.ClientCtx(e.f.Client2)
	if e.visible(c2, e.f.OrgA, "monitoring_group_member", "ci_id", ci1) {
		t.Error("client 2 sees the membership of client 1's CI")
	}
	if !e.visible(c2, e.f.OrgA, "monitoring_group_member", "ci_id", ci2) || !e.visible(c2, e.f.OrgA, "monitoring_group", "id", gOrg) {
		t.Error("client 2 misses its own membership or the org-wide group")
	}
	// Client 2 cannot add client 1's CI to the org-wide group.
	err := e.as(c2, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM monitoring_group_member WHERE ci_id = $1`, ci1)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO monitoring_group_member (organization_id, group_id, ci_id) VALUES (current_setting('app.org_id')::uuid, $1, $2)`, gOrg, ci1)
		return err
	})
	if sqlState(err) != "42501" {
		t.Errorf("client 2 adds client 1's CI: %v, want 42501", err)
	}
	// Moving the CI moves its membership.
	e.mustExec(`UPDATE ci SET client_id = $2, location_id = NULL WHERE id = $1`, ci1, e.f.Client2)
	if !e.visible(c2, e.f.OrgA, "monitoring_group_member", "ci_id", ci1) {
		t.Error("membership did not follow the moved CI")
	}
}

// TestProbeAtSiteReportsProbeOnSiteDelete: a probe without CI holds its
// site itself; deleting the site reports the kind probe, not ci.
func TestProbeAtSiteReportsProbeOnSiteDelete(t *testing.T) {
	e := newSchemaEnv(t)
	site := e.site(e.f.OrgA, e.f.Client1, "wp935-probe-site")
	e.mustExec(`INSERT INTO probe (organization_id, collector_group_id, client_id, site_id, type, target) VALUES ($1, (SELECT g.id FROM collector_group g WHERE g.organization_id = $1 AND g.client_id IS NULL ORDER BY g.id LIMIT 1), $2, $3, 'icmp', '192.0.2.60')`,
		e.f.OrgA, e.f.Client1, site)
	ctx := context.Background()
	err := e.as(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return locations.CheckDeletable(ctx, tx, site)
	})
	var de *locations.DependencyError
	if !errors.As(err, &de) || !slices.Equal(de.Kinds, []string{locations.DependencyProbe}) {
		t.Errorf("CheckDeletable = %v, want the kind probe", err)
	}
	tx, err := e.f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `DELETE FROM location WHERE id = $1`, site)
	if !errors.As(locations.DeleteError(err), &de) || !slices.Equal(de.Kinds, []string{locations.DependencyProbe}) {
		t.Errorf("delete of the site: %v, want the kind probe", err)
	}
}

// TestMemberScopeWaitsForGroupChange: the scope trigger reads the group
// FOR SHARE, so a membership inserted while the group moves to a client
// waits for the move and then sees the new client (no member of another
// client slips into the group under READ COMMITTED).
func TestMemberScopeWaitsForGroupChange(t *testing.T) {
	e := newSchemaEnv(t)
	ctx := context.Background()
	g := e.group(e.f.OrgA, "", "wp935-race")
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp935-race-2")
	move, err := e.f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = move.Rollback(ctx) }()
	if _, err = move.Exec(ctx, `UPDATE monitoring_group SET client_id = $2 WHERE id = $1`, g, e.f.Client1); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- e.exec(`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id) VALUES ($1, $2, $3)`, e.f.OrgA, g, ci2)
	}()
	select {
	case err = <-done:
		t.Fatalf("insert did not wait for the group change: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err = move.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; sqlState(err) != "23514" || constraintOf(err) != "monitoring_group_member_client_rule" {
		t.Errorf("member of client 2 in the group moved to client 1: %v, want 23514 monitoring_group_member_client_rule", err)
	}
}
