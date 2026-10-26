package relationship_test

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// TestConfirmManyMatchesConfirm covers WP-608 (TOP-02, NFR-03) against
// PostgreSQL: ConfirmMany re-confirms several discovery edges with one
// statement exactly as Confirm one after the other does. Two organizations
// hold the same edges; A is confirmed with ConfirmMany, B with Confirm in
// the same order:
//   - the last reported confidence of an edge wins, nil keeps the stored one;
//   - a manual edge is not changed (source policy, REL-08), nor an edge of
//     a client outside the principal's scope (row level security, TEN-05);
//   - last_seen_at and last_confirmed_at become now for the edges changed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestConfirmManyMatchesConfirm(t *testing.T) {
	f := scopetest.Seed(t, "ab")
	bg := context.Background()
	repo := relationship.NewPGRepository(f.App)
	old := time.Now().UTC().Add(-48 * time.Hour)
	type edges struct{ a, b, manual, other string }
	seed := func(org, client1, client2 string) edges {
		ci := func(name, client string) string { return f.CI(t, org, client, "wp608-"+name) }
		sw, a, b, c := ci("sw", ""), ci("a", client1), ci("b", client1), ci("c", client2)
		edge := func(src, dst, source string) string {
			t.Helper()
			var id string
			if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
					last_seen_at, last_confirmed_at, confidence)
				VALUES ($1, $2, $3, 'connected_to', $4, $5, $5, 0.6) RETURNING id::text`, org, src, dst, source, old).Scan(&id); err != nil {
				t.Fatal(err)
			}
			return id
		}
		return edges{a: edge(a, sw, "discovery"), b: edge(b, sw, "discovery"), manual: edge(a, b, "manual"), other: edge(c, sw, "discovery")}
	}
	// The clients of the fixture belong to organization A; the CIs of B
	// have none.
	inA, inB := seed(f.OrgA, f.Client1, f.Client2), seed(f.OrgB, "", "")
	half, high := 0.5, 0.9
	confirmations := func(e edges) []relationship.Confirmation {
		return []relationship.Confirmation{{ID: e.a, Confidence: &half}, {ID: e.a, Confidence: nil}, {ID: e.b, Confidence: nil},
			{ID: e.manual, Confidence: &high}, {ID: e.b, Confidence: &high}}
	}
	if err := repo.ConfirmMany(f.OrgCtx(f.OrgA), f.OrgA, confirmations(inA)); err != nil {
		t.Fatal(err)
	}
	for _, c := range confirmations(inB) {
		_ = repo.Confirm(f.OrgCtx(f.OrgB), f.OrgB, c.ID, c.Confidence)
	}
	// A principal of client 1 cannot confirm the edge of client 2, with
	// either method.
	if err := repo.ConfirmMany(f.ClientCtx(f.Client1), f.OrgA, []relationship.Confirmation{{ID: inA.other, Confidence: &high}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.Confirm(f.ClientCtx(f.Client1), f.OrgA, inA.other, &high); err == nil {
		t.Error("a principal of client 1 confirmed the edge of client 2")
	}

	type state struct {
		confidence float64
		confirmed  bool
	}
	read := func(id string) state {
		var s state
		if err := f.Admin.QueryRow(bg, `SELECT confidence::float8, last_confirmed_at > $2 AND last_seen_at > $2 FROM ci_relationship WHERE id = $1`,
			id, old.Add(time.Hour)).Scan(&s.confidence, &s.confirmed); err != nil {
			t.Fatal(err)
		}
		return s
	}
	for name, want := range map[string]struct {
		a, b string
		s    state
	}{
		"edge confirmed twice":              {inA.a, inB.a, state{0.5, true}},
		"edge confirmed with nil, then 0.9": {inA.b, inB.b, state{0.9, true}},
		"manual edge":                       {inA.manual, inB.manual, state{0.6, false}},
	} {
		if got, ref := read(want.a), read(want.b); got != want.s || ref != want.s {
			t.Errorf("%s: ConfirmMany %+v, Confirm %+v, want %+v", name, got, ref, want.s)
		}
	}
	if got := read(inA.other); got != (state{0.6, false}) {
		t.Errorf("edge of client 2 after a confirmation by client 1: %+v", got)
	}
	if err := repo.ConfirmMany(f.OrgCtx(f.OrgA), f.OrgA, nil); err != nil {
		t.Errorf("no confirmation: %v", err)
	}
}
