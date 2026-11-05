package relationship_test

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// sqlTrace records the statements of a pool.
type sqlTrace struct {
	mu  sync.Mutex
	sql []string
}

func (s *sqlTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sql = append(s.sql, strings.ToLower(strings.Join(strings.Fields(d.SQL), " ")))
	return ctx
}

func (s *sqlTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// take returns the statements recorded since the last call.
func (s *sqlTrace) take() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.sql
	s.sql = nil
	return out
}

// systemChange is a relationship_change row as the system paths write it.
type systemChange struct {
	kind, source, actorType, reason, requestID string
	fields                                     []string
	beforeConfidence, afterConfidence          string
	beforeAttrs, afterAttrs                    string
	hasBefore, hasAfter                        bool
}

// TestRelationshipHistorySystemPaths covers WP-771 (REL-09) against
// PostgreSQL: the system paths that change stored edges write
// relationship_change rows:
//   - Confirm with an unchanged confidence writes no row and no event, with
//     a changed one exactly one row refreshed (changed_fields confidence,
//     source collector) and no event;
//   - ConfirmMany with three edges (unchanged, new confidence, changed
//     attribute) writes exactly two rows refreshed with the matching
//     changed_fields, the edges updated with one statement and the rows
//     written with one more, and no event;
//   - the interface sync removing an interface after three observations
//     without it and DeleteInterface write a row removed with reason
//     interface_removed per removed discovery edge (source collector
//     without a request, the request's with one); the ci_change entry stays;
//   - the TOP-02 cleanup writes a row removed with reason stale or
//     suppressed, source system and actor type system, next to the
//     relationship.removed of the outbox.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipHistorySystemPaths(t *testing.T) {
	f := scopetest.Seed(t, "71")
	bg := context.Background()
	trace := &sqlTrace{}
	cfg := f.App.Config()
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	repo := relationship.NewPGRepository(pool).WithEvents(events.NewOutbox())
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := f.Admin.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%s: %v", sql, execErr)
		}
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	edge := func(src, dst, attrs string, ifaces ...string) string {
		t.Helper()
		srcIf, dstIf := "", ""
		if len(ifaces) == 2 {
			srcIf, dstIf = ifaces[0], ifaces[1]
		}
		var id string
		if qerr := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				attributes, last_seen_at, last_confirmed_at, confidence, source_interface_id, target_interface_id)
			VALUES ($1, $2, $3, 'connected_to', 'discovery', $4::jsonb, $5, $5, 0.6, NULLIF($6, '')::uuid, NULLIF($7, '')::uuid)
			RETURNING id::text`, f.OrgA, src, dst, attrs, old, srcIf, dstIf).Scan(&id); qerr != nil {
			t.Fatal(qerr)
		}
		return id
	}
	changes := func(id string) []systemChange {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT change_kind, source, actor_type, COALESCE(reason, ''), COALESCE(request_id, ''), changed_fields,
				COALESCE(before->>'confidence', ''), COALESCE(after->>'confidence', ''),
				COALESCE(before->>'attributes', ''), COALESCE(after->>'attributes', ''), before IS NOT NULL, after IS NOT NULL
			FROM relationship_change WHERE organization_id = $1 AND relationship_id = $2 ORDER BY observed_at, id`, f.OrgA, id)
		if qerr != nil {
			t.Fatal(qerr)
		}
		out, qerr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (systemChange, error) {
			var c systemChange
			scanErr := row.Scan(&c.kind, &c.source, &c.actorType, &c.reason, &c.requestID, &c.fields,
				&c.beforeConfidence, &c.afterConfidence, &c.beforeAttrs, &c.afterAttrs, &c.hasBefore, &c.hasAfter)
			return c, scanErr
		})
		if qerr != nil {
			t.Fatal(qerr)
		}
		return out
	}
	edgeEvents := func(ids ...string) int {
		t.Helper()
		var n int
		if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type LIKE 'relationship.%'
			AND entity_id = ANY($2::text[])`, f.OrgA, ids).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}
	sw := f.CI(t, f.OrgA, "", "wp771-sw")
	ci := func(name string) string { return f.CI(t, f.OrgA, "", "wp771-"+name) }

	t.Run("confirm", func(t *testing.T) {
		e := edge(sw, ci("confirm"), `{}`)
		same, higher := 0.6, 0.8
		if err = repo.Confirm(ctx, f.OrgA, e, &same); err != nil {
			t.Fatal(err)
		}
		if err = repo.Confirm(ctx, f.OrgA, e, nil); err != nil {
			t.Fatal(err)
		}
		if got := changes(e); len(got) != 0 {
			t.Errorf("confirm without a changed confidence wrote %+v", got)
		}
		if err = repo.Confirm(ctx, f.OrgA, e, &higher); err != nil {
			t.Fatal(err)
		}
		got := changes(e)
		if len(got) != 1 {
			t.Fatalf("confirm with a changed confidence wrote %d rows, want 1: %+v", len(got), got)
		}
		c := got[0]
		if c.kind != relationship.ChangeRefreshed || !slices.Equal(c.fields, []string{"confidence"}) || c.source != audit.SourceCollector ||
			c.beforeConfidence != "0.6" || c.afterConfidence != "0.8" || c.reason != "" {
			t.Errorf("refreshed row: %+v", c)
		}
		if n := edgeEvents(e); n != 0 {
			t.Errorf("confirm wrote %d edge events", n)
		}
	})

	t.Run("confirm many", func(t *testing.T) {
		unchanged := edge(sw, ci("many-a"), `{"speed": 1000}`)
		confidence := edge(sw, ci("many-b"), `{"speed": 1000}`)
		attribute := edge(sw, ci("many-c"), `{"speed": 1000}`)
		same, higher := 0.6, 0.9
		trace.take()
		if err = repo.ConfirmMany(ctx, f.OrgA, []relationship.Confirmation{
			{ID: unchanged, Confidence: &same, Attributes: map[string]any{"speed": 1000}},
			{ID: confidence, Confidence: &higher},
			{ID: attribute, Attributes: map[string]any{"speed": 10000}},
		}); err != nil {
			t.Fatal(err)
		}
		edgeStatements, historyStatements := 0, 0
		for _, sql := range trace.take() {
			if regexp.MustCompile(`\bupdate ci_relationship\b`).MatchString(sql) {
				edgeStatements++
			}
			if strings.HasPrefix(sql, "insert into relationship_change") {
				historyStatements++
			}
		}
		if edgeStatements != 1 || historyStatements != 1 {
			t.Errorf("ConfirmMany: %d edge statements and %d history statements, want one of each", edgeStatements, historyStatements)
		}
		if got := changes(unchanged); len(got) != 0 {
			t.Errorf("unchanged edge: %+v", got)
		}
		if got := changes(confidence); len(got) != 1 || got[0].kind != relationship.ChangeRefreshed ||
			!slices.Equal(got[0].fields, []string{"confidence"}) || got[0].afterConfidence != "0.9" || got[0].source != audit.SourceCollector {
			t.Errorf("edge with a new confidence: %+v", got)
		}
		if got := changes(attribute); len(got) != 1 || got[0].kind != relationship.ChangeRefreshed ||
			!slices.Equal(got[0].fields, []string{"attributes"}) || got[0].beforeAttrs != `{"speed": 1000}` ||
			got[0].afterAttrs != `{"speed": 10000}` {
			t.Errorf("edge with a changed attribute: %+v", got)
		}
		if n := edgeEvents(unchanged, confidence, attribute); n != 0 {
			t.Errorf("ConfirmMany wrote %d edge events", n)
		}
	})

	t.Run("interface removal", func(t *testing.T) {
		host := ci("host")
		port := func(ciID, name string) string {
			t.Helper()
			var id string
			if qerr := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name, origin) VALUES ($1, $2, $3, 'discovered')
				RETURNING id::text`, f.OrgA, ciID, name).Scan(&id); qerr != nil {
				t.Fatal(qerr)
			}
			return id
		}
		swPort := port(sw, "Gi1/0/71")
		missing, deleted := port(host, "eth1"), port(host, "eth2")
		port(host, "eth0")
		bySync := edge(host, sw, `{}`, missing, swPort)
		byDelete := edge(host, sw, `{}`, deleted, "")
		nics := ipam.NewPGRepository(f.App)
		observed := []ipam.ObservedInterface{{Name: "eth0"}, {Name: "eth2"}}
		for range ipam.MaxMissedObservations {
			if _, err = nics.SyncDiscoveredInterfaces(ctx, f.OrgA, host, observed); err != nil {
				t.Fatal(err)
			}
		}
		request := audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "req-wp771", Source: audit.SourceUI})
		if err = nics.DeleteInterface(request, f.OrgA, deleted); err != nil {
			t.Fatal(err)
		}
		for name, tc := range map[string]struct {
			id, source, requestID string
		}{
			"sync":   {bySync, audit.SourceCollector, ""},
			"delete": {byDelete, audit.SourceUI, "req-wp771"},
		} {
			got := changes(tc.id)
			if len(got) != 1 || got[0].kind != relationship.ChangeRemoved || got[0].reason != "interface_removed" ||
				got[0].source != tc.source || got[0].requestID != tc.requestID || !got[0].hasBefore || got[0].hasAfter {
				t.Errorf("%s: history of the removed edge %+v", name, got)
			}
			var logged int
			if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_change WHERE organization_id = $1 AND ci_id = $2
				AND change_type = 'relationship_change' AND old_value->>'id' = $3`, f.OrgA, host, tc.id).Scan(&logged); qerr != nil || logged != 1 {
				t.Errorf("%s: %d ci_change entries of the removed edge, want 1 (%v)", name, logged, qerr)
			}
		}
		if n := edgeEvents(bySync, byDelete); n != 0 {
			t.Errorf("interface removal wrote %d edge events", n)
		}
	})

	t.Run("cleanup", func(t *testing.T) {
		stale := edge(sw, ci("stale"), `{}`)
		exec(`UPDATE ci_relationship SET last_seen_at = $2, last_confirmed_at = $2 WHERE id = $1`, stale, time.Now().UTC().Add(-30*24*time.Hour))
		a, b := ci("supp-a"), ci("supp-b")
		suppressed := edge(a, b, `{}`)
		exec(`INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id)
			SELECT $1, $2, $3, id FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL`, f.OrgA, a, b)
		if _, err = relationship.NewCleanup(f.App).RunOrg(bg, f.OrgA); err != nil {
			t.Fatal(err)
		}
		for reason, id := range map[string]string{"stale": stale, "suppressed": suppressed} {
			got := changes(id)
			if len(got) != 1 || got[0].kind != relationship.ChangeRemoved || got[0].reason != reason ||
				got[0].source != audit.SourceSystem || got[0].actorType != "system" || !got[0].hasBefore || got[0].hasAfter {
				t.Errorf("%s: history of the removed edge %+v", reason, got)
			}
			var removed int
			if qerr := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'relationship.removed'
				AND entity_id = $2 AND data->>'reason' = $3`, f.OrgA, id, reason).Scan(&removed); qerr != nil || removed != 1 {
				t.Errorf("%s: %d relationship.removed events, want 1 (%v)", reason, removed, qerr)
			}
		}
	})
}
