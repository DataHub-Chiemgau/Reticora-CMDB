package probe_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
)

// Schema of the probe state (PRB-05, PRB-07, WP-944, migration
// 000311_probe_state): probe_state, probe_state_change and
// probe_ingest_cursor with RLS, scope policies, the scope derivation from
// the probe, the idempotency key and composite foreign keys. Seeds go
// through the maintenance pool, the checks run as the runtime login.

// stateOf inserts the state of the probe through the maintenance pool.
func (e *schemaEnv) stateOf(orgID, probeID, state string) {
	e.t.Helper()
	e.mustExec(`INSERT INTO probe_state (probe_id, organization_id, state, since) VALUES ($1, $2, $3, now())`, probeID, orgID, state)
}

// change inserts a state change of the probe and returns its id.
func (e *schemaEnv) change(orgID, probeID string, seq int) string {
	e.t.Helper()
	return e.scalar(`INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, reason, observed_at, epoch, seq)
		VALUES ($1, $2, 'up', 'down', 'timeout', now(), 1, $3) RETURNING id::text`, orgID, probeID, seq)
}

func (e *schemaEnv) siteCtx(orgID, siteID string) context.Context {
	scope := database.OrgWideScope(orgID, e.f.User)
	scope.Sites = database.ScopeIDs(siteID)
	return database.ContextWithTenantScope(context.Background(), &scope)
}

// TestProbeStateIsolation: organization A sees no row of B; a principal
// restricted to a client sees only its client's states and changes, one
// restricted to a site only those of its site (AK4); client and site come
// from the probe and follow it.
func TestProbeStateIsolation(t *testing.T) {
	e := newSchemaEnv(t)
	site1 := e.site(e.f.OrgA, e.f.Client1, "wp944-site-1")
	site2 := e.site(e.f.OrgA, e.f.Client1, "wp944-site-2")
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp944-ci-1")
	e.place(ci1, "", site1)
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp944-ci-2")
	e.place(ci2, "", site2)
	ci3 := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp944-ci-3")
	p1 := e.probeOnCI(e.f.OrgA, ci1, "192.0.2.1")
	p2 := e.probeOnCI(e.f.OrgA, ci2, "192.0.2.2")
	p3 := e.probeOnCI(e.f.OrgA, ci3, "192.0.2.3")
	ciB := e.f.CI(t, e.f.OrgB, "", "wp944-ci-b")
	pB := e.probeOnCI(e.f.OrgB, ciB, "192.0.2.9")
	for _, p := range []string{p1, p2, p3} {
		e.stateOf(e.f.OrgA, p, probe.StateUp)
		e.change(e.f.OrgA, p, 1)
	}
	e.stateOf(e.f.OrgB, pB, probe.StateDown)
	e.change(e.f.OrgB, pB, 1)

	if got := e.scalar(`SELECT client_id::text || '/' || site_id::text || '/' || ci_id::text FROM probe_state_change WHERE probe_id = $1`, p1); got != e.f.Client1+"/"+site1+"/"+ci1 {
		t.Errorf("change of probe 1 scope %s, want client 1, site 1 and the CI", got)
	}
	if got := e.scalar(`SELECT client_id::text || '/' || site_id::text FROM probe_state WHERE probe_id = $1`, p2); got != e.f.Client1+"/"+site2 {
		t.Errorf("state of probe 2 scope %s, want client 1 and site 2", got)
	}

	for _, c := range []struct {
		name  string
		ctx   context.Context
		org   string
		probe map[string]bool
	}{
		{"org A", e.f.OrgCtx(e.f.OrgA), e.f.OrgA, map[string]bool{p1: true, p2: true, p3: true, pB: false}},
		{"org B", e.f.OrgCtx(e.f.OrgB), e.f.OrgB, map[string]bool{p1: false, pB: true}},
		{"client 1", e.f.ClientCtx(e.f.Client1), e.f.OrgA, map[string]bool{p1: true, p2: true, p3: false}},
		{"client 2", e.f.ClientCtx(e.f.Client2), e.f.OrgA, map[string]bool{p1: false, p3: true}},
		{"site 1", e.siteCtx(e.f.OrgA, site1), e.f.OrgA, map[string]bool{p1: true, p2: false}},
	} {
		for p, want := range c.probe {
			for _, table := range []string{"probe_state", "probe_state_change"} {
				if got := e.visible(c.ctx, c.org, table, "probe_id", p); got != want {
					t.Errorf("%s sees %s of %s: %v, want %v", c.name, table, p, got, want)
				}
			}
		}
	}

	// A client principal writes no state of another client.
	err := e.as(e.f.ClientCtx(e.f.Client1), e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, observed_at, epoch, seq)
			VALUES (current_setting('app.org_id')::uuid, $1, 'up', 'down', now(), 1, 2)`, p3)
		return err
	})
	if sqlState(err) != "42501" {
		t.Errorf("client 1 writes a change of client 2: %v, want 42501", err)
	}

	// Moving the CI moves state and history with the probe.
	e.mustExec(`UPDATE ci SET client_id = $2, location_id = NULL WHERE id = $1`, ci1, e.f.Client2)
	for _, table := range []string{"probe_state", "probe_state_change"} {
		if got := e.scalar(`SELECT client_id::text || '/' || COALESCE(site_id::text, '') FROM `+table+` WHERE probe_id = $1`, p1); got != e.f.Client2+"/" {
			t.Errorf("%s after the move: %s, want client 2 without site", table, got)
		}
	}
	if e.visible(e.f.ClientCtx(e.f.Client1), e.f.OrgA, "probe_state", "probe_id", p1) {
		t.Error("client 1 still sees the state of the moved probe")
	}
}

// TestProbeStateCheckConstraints: an unknown state and an unknown stream
// are refused (AK1–AK3).
func TestProbeStateCheckConstraints(t *testing.T) {
	e := newSchemaEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp944-check")
	p := e.probeOnCI(e.f.OrgA, ci, "192.0.2.20")
	collector := e.scalar(`INSERT INTO collector (organization_id, name) VALUES ($1, 'wp944-check') RETURNING id::text`, e.f.OrgA)
	for name, c := range map[string]struct {
		sql        string
		args       []any
		constraint string
	}{
		"state": {`INSERT INTO probe_state (probe_id, organization_id, state, since) VALUES ($1, $2, 'unreachable', now())`,
			[]any{p, e.f.OrgA}, "probe_state_state_check"},
		"from state": {`INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, observed_at, epoch, seq) VALUES ($2, $1, 'gone', 'up', now(), 1, 1)`,
			[]any{p, e.f.OrgA}, "probe_state_change_from_state_check"},
		"to state": {`INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, observed_at, epoch, seq) VALUES ($2, $1, 'up', 'unreachable', now(), 1, 1)`,
			[]any{p, e.f.OrgA}, "probe_state_change_to_state_check"},
		"stream": {`INSERT INTO probe_ingest_cursor (organization_id, collector_id, stream) VALUES ($1, $2, 'metrics')`,
			[]any{e.f.OrgA, collector}, "probe_ingest_cursor_stream_check"},
		"negative count": {`INSERT INTO probe_state (probe_id, organization_id, state, since, consecutive_fail) VALUES ($1, $2, 'up', now(), -1)`,
			[]any{p, e.f.OrgA}, "probe_state_consecutive_fail_check"},
	} {
		err := e.exec(c.sql, c.args...)
		if sqlState(err) != "23514" || constraintOf(err) != c.constraint {
			t.Errorf("%s: %v (%s), want 23514 %s", name, err, constraintOf(err), c.constraint)
		}
	}
	for _, s := range probe.States {
		if !probe.ValidState(s) {
			t.Errorf("ValidState(%q) = false", s)
		}
		if err := e.exec(`INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, observed_at, epoch, seq)
			VALUES ($1, $2, 'unknown', $3, now(), 1, (SELECT count(*) FROM probe_state_change WHERE probe_id = $2))`, e.f.OrgA, p, s); err != nil {
			t.Errorf("state %s refused: %v", s, err)
		}
	}
	if probe.ValidState("unreachable") {
		t.Error("unreachable is a state")
	}
	for _, s := range []string{probe.StreamState, probe.StreamResults} {
		e.mustExec(`INSERT INTO probe_ingest_cursor (organization_id, collector_id, stream) VALUES ($1, $2, $3)`, e.f.OrgA, collector, s)
	}
}

// TestProbeStateChangeIsIdempotent: a second insert with the same
// (probe_id, collector_id, epoch, seq) and ON CONFLICT DO NOTHING leaves one
// row (PRB-07).
func TestProbeStateChangeIsIdempotent(t *testing.T) {
	e := newSchemaEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp944-idem")
	p := e.probeOnCI(e.f.OrgA, ci, "192.0.2.30")
	collector := e.scalar(`INSERT INTO collector (organization_id, name) VALUES ($1, 'wp944-idem') RETURNING id::text`, e.f.OrgA)
	insert := func(ctx context.Context, tx pgx.Tx, seq int) (int64, error) {
		tag, err := tx.Exec(ctx, `INSERT INTO probe_state_change (organization_id, probe_id, collector_id, from_state, to_state, reason, observed_at, epoch, seq)
			VALUES (current_setting('app.org_id')::uuid, $1, $2, 'up', 'down', 'timeout', now(), 4, $3)
			ON CONFLICT (probe_id, collector_id, epoch, seq) DO NOTHING`, p, collector, seq)
		return tag.RowsAffected(), err
	}
	for i, want := range []int64{1, 0, 0} {
		var n int64
		if err := e.as(e.f.ClientCtx(e.f.Client1), e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			var err error
			n, err = insert(ctx, tx, 17)
			return err
		}); err != nil {
			t.Fatalf("delivery %d: %v", i+1, err)
		}
		if n != want {
			t.Errorf("delivery %d inserted %d rows, want %d", i+1, n, want)
		}
	}
	if got := e.scalar(`SELECT count(*)::text FROM probe_state_change WHERE probe_id = $1`, p); got != "1" {
		t.Errorf("%s rows after the double delivery, want 1", got)
	}
	// Another sequence number or epoch is a new change.
	if err := e.as(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := insert(ctx, tx, 18)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := e.exec(`INSERT INTO probe_state_change (organization_id, probe_id, collector_id, from_state, to_state, observed_at, epoch, seq)
		VALUES ($1, $2, $3, 'down', 'up', now(), 4, 17)`, e.f.OrgA, p, collector); sqlState(err) != "23505" {
		t.Errorf("plain duplicate: %v, want 23505", err)
	}
	if got := e.scalar(`SELECT count(*)::text FROM probe_state_change WHERE probe_id = $1`, p); got != "2" {
		t.Errorf("%s rows, want 2", got)
	}
}

// TestProbeStateForeignKeys: a probe, collector or client of another
// organization is refused, a change with a CI other than its probe's too;
// deleting the probe removes state and history, deleting the recorded CI of
// a moved probe sets ci_id to NULL, deleting the collector its reference
// (AK1, AK2, AK5).
func TestProbeStateForeignKeys(t *testing.T) {
	e := newSchemaEnv(t)
	ciProbe := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp944-fk-probe")
	p := e.probeOnCI(e.f.OrgA, ciProbe, "192.0.2.40")
	pClient := e.probeOfClient(e.f.OrgA, e.f.Client1, "192.0.2.41:22")
	ciOther := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp944-fk-other")
	ciB := e.f.CI(t, e.f.OrgB, "", "wp944-fk-b")
	pB := e.probeOnCI(e.f.OrgB, ciB, "192.0.2.49")
	collector := e.scalar(`INSERT INTO collector (organization_id, name) VALUES ($1, 'wp944-fk') RETURNING id::text`, e.f.OrgA)
	collectorB := e.scalar(`INSERT INTO collector (organization_id, name) VALUES ($1, 'wp944-fk-b') RETURNING id::text`, e.f.OrgB)

	for name, c := range map[string]struct {
		sql  string
		args []any
	}{
		"state of a foreign probe":  {`INSERT INTO probe_state (probe_id, organization_id, state, since) VALUES ($1, $2, 'up', now())`, []any{pB, e.f.OrgA}},
		"change of a foreign probe": {`INSERT INTO probe_state_change (organization_id, probe_id, from_state, to_state, observed_at, epoch, seq) VALUES ($2, $1, 'up', 'down', now(), 1, 1)`, []any{pB, e.f.OrgA}},
		"foreign collector":         {`INSERT INTO probe_state (probe_id, organization_id, state, since, collector_id) VALUES ($1, $2, 'up', now(), $3)`, []any{p, e.f.OrgA, collectorB}},
		"cursor of foreign coll.":   {`INSERT INTO probe_ingest_cursor (organization_id, collector_id, stream) VALUES ($1, $2, 'state')`, []any{e.f.OrgA, collectorB}},
	} {
		if err := e.exec(c.sql, c.args...); sqlState(err) != "23503" {
			t.Errorf("%s: %v, want 23503", name, err)
		}
	}

	e.mustExec(`INSERT INTO probe_state (probe_id, organization_id, state, since, collector_id) VALUES ($1, $2, 'down', now(), $3)`, p, e.f.OrgA, collector)
	e.mustExec(`INSERT INTO probe_state_change (organization_id, probe_id, collector_id, from_state, to_state, observed_at, epoch, seq) VALUES ($1, $2, $3, 'up', 'down', now(), 1, 1)`, e.f.OrgA, p, collector)
	e.mustExec(`INSERT INTO probe_ingest_cursor (organization_id, collector_id, stream, epoch, last_seq) VALUES ($1, $2, 'state', 1, 1)`, e.f.OrgA, collector)
	e.mustExec(`INSERT INTO probe_state (probe_id, organization_id, state, since) VALUES ($1, $2, 'up', now())`, pClient, e.f.OrgA)

	// A change carries the CI of its probe: another CI of the same client,
	// of another client, of another organization, an unknown id or a CI on
	// a probe without CI is refused with the same answer before the foreign
	// key runs, also for a principal restricted to the probe's client.
	ciClient2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "wp944-fk-client2")
	insertCI := `INSERT INTO probe_state_change (organization_id, probe_id, ci_id, from_state, to_state, observed_at, epoch, seq) VALUES ($1, $2, $3, 'up', 'down', now(), 9, 1)`
	for name, c := range map[string][]any{
		"other ci of the client":     {e.f.OrgA, p, ciOther},
		"ci of another client":       {e.f.OrgA, p, ciClient2},
		"ci of another organization": {e.f.OrgA, p, ciB},
		"unknown ci":                 {e.f.OrgA, p, "00000000-0000-4000-8000-000000000944"},
		"ci on a probe without ci":   {e.f.OrgA, pClient, ciOther},
	} {
		if err := e.exec(insertCI, c...); sqlState(err) != "23514" || constraintOf(err) != "probe_state_change_ci_of_probe" {
			t.Errorf("%s: %v (%s), want 23514 probe_state_change_ci_of_probe", name, err, constraintOf(err))
		}
	}
	for name, ci := range map[string]string{"ci of another client": ciClient2, "unknown ci": "00000000-0000-4000-8000-000000000945"} {
		err := e.as(e.f.ClientCtx(e.f.Client1), e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO probe_state_change (organization_id, probe_id, ci_id, from_state, to_state, observed_at, epoch, seq)
				VALUES (current_setting('app.org_id')::uuid, $1, $2, 'up', 'down', now(), 9, 2)`, p, ci)
			return err
		})
		if sqlState(err) != "23514" {
			t.Errorf("client 1 writes a change with the %s: %v, want 23514", name, err)
		}
	}
	// The probe's own CI is accepted; a probe without CI records none.
	e.mustExec(insertCI, e.f.OrgA, p, ciProbe)
	noCI := e.change(e.f.OrgA, pClient, 1)
	if got := e.scalar(`SELECT COALESCE(ci_id::text, 'null') FROM probe_state_change WHERE id = $1`, noCI); got != "null" {
		t.Errorf("change of a probe without CI carries %s", got)
	}

	// The CI of a recorded change cannot be moved, neither to another CI of
	// the organization nor of another client.
	for name, ci := range map[string]string{"other ci": ciOther, "ci of another client": ciClient2} {
		if err := e.exec(`UPDATE probe_state_change SET ci_id = $2 WHERE probe_id = $1 AND epoch = 9`, p, ci); sqlState(err) != "23514" {
			t.Errorf("update to the %s: %v, want 23514", name, err)
		}
	}

	// A change keeps the CI recorded at the time when the probe moves to
	// another CI; deleting that CI sets ci_id to NULL, the change stays.
	ciOld := e.f.CI(t, e.f.OrgA, e.f.Client1, "wp944-fk-old")
	pMove := e.probeOnCI(e.f.OrgA, ciOld, "192.0.2.42")
	other := e.change(e.f.OrgA, pMove, 1)
	e.mustExec(`UPDATE probe SET ci_id = $2 WHERE id = $1`, pMove, ciOther)
	if got := e.scalar(`SELECT ci_id::text FROM probe_state_change WHERE id = $1`, other); got != ciOld {
		t.Errorf("ci_id after the probe moved: %s, want the old CI", got)
	}

	// Deleting the collector clears its references and drops its cursor.
	e.mustExec(`DELETE FROM collector WHERE id = $1`, collector)
	if got := e.scalar(`SELECT (SELECT count(*) FROM probe_state WHERE probe_id = $1 AND collector_id IS NULL)::text || '/' ||
		(SELECT count(*) FROM probe_state_change WHERE probe_id = $1 AND epoch = 1 AND collector_id IS NULL)::text || '/' ||
		(SELECT count(*) FROM probe_ingest_cursor WHERE collector_id = $2)::text`, p, collector); got != "1/1/0" {
		t.Errorf("after deleting the collector: %s, want 1/1/0", got)
	}

	// Deleting the CI of a change sets ci_id to NULL, the change stays.
	e.mustExec(`DELETE FROM ci WHERE id = $1`, ciOld)
	if got := e.scalar(`SELECT COALESCE(ci_id::text, 'null') FROM probe_state_change WHERE id = $1`, other); got != "null" {
		t.Errorf("ci_id after deleting the CI: %s, want null", got)
	}

	// Deleting the probe removes its state and history.
	e.mustExec(`DELETE FROM probe WHERE id = $1`, p)
	if got := e.scalar(`SELECT ((SELECT count(*) FROM probe_state WHERE probe_id = $1) + (SELECT count(*) FROM probe_state_change WHERE probe_id = $1))::text`, p); got != "0" {
		t.Errorf("%s rows left after deleting the probe, want 0", got)
	}
	// The same for a probe without CI.
	e.mustExec(`DELETE FROM probe WHERE id = $1`, pClient)
	if got := e.scalar(`SELECT count(*)::text FROM probe_state WHERE probe_id = $1`, pClient); got != "0" {
		t.Errorf("%s states left, want 0", got)
	}
}

// TestProbeIngestCursorSystemOnly: the application role without app.system
// reads only the cursors of its organization; the system flag reads all of
// them but writes none (AK4, 000060).
func TestProbeIngestCursorSystemOnly(t *testing.T) {
	e := newSchemaEnv(t)
	cA := e.scalar(`INSERT INTO collector (organization_id, name) VALUES ($1, 'wp944-cursor-a') RETURNING id::text`, e.f.OrgA)
	cB := e.scalar(`INSERT INTO collector (organization_id, name) VALUES ($1, 'wp944-cursor-b') RETURNING id::text`, e.f.OrgB)
	e.mustExec(`INSERT INTO probe_ingest_cursor (organization_id, collector_id, stream, epoch, last_seq) VALUES ($1, $2, 'state', 2, 10)`, e.f.OrgA, cA)
	e.mustExec(`INSERT INTO probe_ingest_cursor (organization_id, collector_id, stream, epoch, last_seq) VALUES ($1, $2, 'results', 2, 11)`, e.f.OrgA, cA)
	e.mustExec(`INSERT INTO probe_ingest_cursor (organization_id, collector_id, stream, epoch, last_seq) VALUES ($1, $2, 'state', 1, 5)`, e.f.OrgB, cB)

	ids := func(ctx context.Context, org string) map[string]int {
		out := map[string]int{}
		if err := e.as(ctx, org, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT collector_id::text FROM probe_ingest_cursor WHERE collector_id = ANY ($1::uuid[])`, []string{cA, cB})
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id string
				if err := rows.Scan(&id); err != nil {
					return err
				}
				out[id]++
			}
			return rows.Err()
		}); err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := ids(e.f.OrgCtx(e.f.OrgA), e.f.OrgA); got[cA] != 2 || got[cB] != 0 {
		t.Errorf("org A reads %v, want two cursors of its collector", got)
	}
	if got := ids(e.f.ClientCtx(e.f.Client1), e.f.OrgA); got[cA] != 2 || got[cB] != 0 {
		t.Errorf("client principal of A reads %v, want the organization's cursors", got)
	}
	if got := ids(e.f.OrgCtx(e.f.OrgB), e.f.OrgB); got[cB] != 1 || got[cA] != 0 {
		t.Errorf("org B reads %v, want its own cursor", got)
	}
	// Org A cannot write a cursor of B.
	if err := e.as(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO probe_ingest_cursor (organization_id, collector_id, stream) VALUES ($1, $2, 'results')`, e.f.OrgB, cB)
		return err
	}); sqlState(err) != "42501" {
		t.Errorf("org A writes a cursor of B: %v, want 42501", err)
	}

	// The system flag reads across organizations and writes nothing.
	var n int
	var updated int64
	if err := database.WithSystem(context.Background(), e.f.App, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM probe_ingest_cursor WHERE collector_id = ANY ($1::uuid[])`, []string{cA, cB}).Scan(&n); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("system reads %d cursors, want 3", n)
	}
	conn, err := e.f.App.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	tx, err := conn.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(context.Background(), `SELECT set_config('app.system', 'on', true), set_config('app.org_id', '', true)`); err != nil {
		t.Fatal(err)
	}
	tag, err := tx.Exec(context.Background(), `UPDATE probe_ingest_cursor SET last_seq = last_seq + 1 WHERE collector_id = ANY ($1::uuid[])`, []string{cA, cB})
	if err == nil {
		updated = tag.RowsAffected()
	}
	if err == nil && updated != 0 {
		t.Errorf("system flag updated %d cursors, want none", updated)
	}
}
