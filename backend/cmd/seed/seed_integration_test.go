package main

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// counts reads the seeded rows of one organization through the maintenance
// pool.
func counts(t *testing.T, f *scopetest.Fixture, orgID string) map[string]int {
	t.Helper()
	out := map[string]int{}
	for table, query := range map[string]string{
		"client":            `SELECT count(*) FROM client WHERE organization_id = $1`,
		"rack":              `SELECT count(*) FROM rack WHERE organization_id = $1`,
		"ci":                `SELECT count(*) FROM ci WHERE organization_id = $1`,
		"rack_mount":        `SELECT count(*) FROM rack_mount WHERE organization_id = $1`,
		"network_interface": `SELECT count(*) FROM network_interface WHERE organization_id = $1`,
		"ip_address":        `SELECT count(*) FROM ip_address WHERE organization_id = $1`,
		"lldp":              `SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND source_system = 'lldp'`,
		"ci_relationship":   `SELECT count(*) FROM ci_relationship WHERE organization_id = $1`,
		"metric_sample":     `SELECT count(*) FROM metric_sample WHERE organization_id = $1`,
		"collector":         `SELECT count(*) FROM collector WHERE organization_id = $1`,
		"discovery_scope":   `SELECT count(*) FROM discovery_scope WHERE organization_id = $1`,
		"review_item":       `SELECT count(*) FROM review_item WHERE organization_id = $1 AND status = 'open'`,
		"webhook":           `SELECT count(*) FROM webhook_subscription WHERE organization_id = $1`,
		"vrf":               `SELECT count(*) FROM vrf WHERE organization_id = $1`,
		"entitlement":       `SELECT count(*) FROM entitlement WHERE organization_id = $1 AND plan = 'pro'`,
		"org_admin": `SELECT count(*) FROM role_assignment a JOIN role r ON r.id = a.role_id
			WHERE a.organization_id = $1 AND r.name = 'org_admin'`,
	} {
		var n int
		if err := f.Admin.QueryRow(context.Background(), query, orgID).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		out[table] = n
	}
	return out
}

// TestSeedSIM01 covers SIM-01 and the load seed of NFR-10 (WP-203): the
// demo organization and acme hold the SIM-01 data set, the demo
// organization scales with -objects, every id is deterministic and a second
// run changes nothing. The seed runs through the application pool, subject
// to the tenant policies.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSeedSIM01(t *testing.T) {
	f := scopetest.Seed(t, "c3")
	ctx := context.Background()
	at := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	opts := Options{Prefix: "seedtest-", Objects: 1000, Metrics: 2000, At: at}

	summary, err := Run(ctx, f.App, opts)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	if len(summary.Orgs) != 2 || summary.Orgs[0].Slug != "seedtest-demo" || summary.Orgs[0].Admin != "admin@demo.local" ||
		summary.Orgs[1].Slug != "seedtest-acme" {
		t.Fatalf("organizations = %+v", summary.Orgs)
	}
	demo, acme := summary.Orgs[0].ID, summary.Orgs[1].ID
	if demo != ID("seedtest-", "org", "demo") {
		t.Errorf("demo id %s is not deterministic", demo)
	}

	features := len(entitlement.PlanFeatures(entitlement.PlanPro))
	want := func(objects, metrics int) map[string]int {
		return map[string]int{
			"client": 2, "rack": 2, "ci": objects, "rack_mount": 40, "network_interface": objects,
			"ip_address": objects, "lldp": objects - 4 + 1, "ci_relationship": 2*(objects-4) + 1,
			"metric_sample": metrics, "collector": 1, "discovery_scope": 2, "review_item": 3, "webhook": 1,
			"vrf": 2, "entitlement": features, "org_admin": 1,
		}
	}
	check := func(round string) {
		for org, expected := range map[string]map[string]int{demo: want(1000, 2000), acme: want(40, 40)} {
			got := counts(t, f, org)
			for table, n := range expected {
				if got[table] != n {
					t.Errorf("%s, org %s: %s = %d, want %d", round, org, table, got[table], n)
				}
			}
		}
	}
	check("first run")

	// Determinism: the Go ids and the ids PostgreSQL derives agree, and a
	// second run with the same options adds nothing.
	var name string
	if err := f.Admin.QueryRow(ctx, `SELECT name FROM ci WHERE id = $1`, ID("seedtest-demo", "ci", "7")).Scan(&name); err != nil || name != "srv-00007" {
		t.Errorf("ci 7: name %q, err %v", name, err)
	}
	var rackID string
	var position int
	if err := f.Admin.QueryRow(ctx, `SELECT rack_id::text, position_u FROM rack_mount WHERE ci_id = $1`,
		ID("seedtest-demo", "ci", "21")).Scan(&rackID, &position); err != nil ||
		rackID != ID("seedtest-demo", "rack", "2") || position != 1 {
		t.Errorf("ci 21: rack %s position %d (err %v), want rack 2 position 1", rackID, position, err)
	}
	if _, err := Run(ctx, f.App, opts); err != nil {
		t.Fatalf("second seed: %v", err)
	}
	check("second run")

	// The organizations are isolated: the demo data is invisible in acme.
	var leaked int
	if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM ci_relationship r JOIN ci c ON c.id = r.target_ci_id
		WHERE r.organization_id = $1 AND c.organization_id <> $1`, acme).Scan(&leaked); err != nil || leaked != 0 {
		t.Errorf("cross-organization relationships: %d, err %v", leaked, err)
	}
}

func TestSeedOptionsAreBounded(t *testing.T) {
	at := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	for _, o := range []Options{{Objects: 39, At: at}, {Objects: MaxObjects + 1, At: at}, {Objects: 40}, {Objects: 40, Metrics: -1, At: at}} {
		if err := o.Validate(); err == nil {
			t.Errorf("options %+v accepted", o)
		}
	}
	if err := (&Options{Objects: 10000, Metrics: 2000, At: at}).Validate(); err != nil {
		t.Errorf("the NFR-10 seed is refused: %v", err)
	}
	if got := ID("demo", "ci", "1"); len(got) != 36 || got != ID("demo", "ci", "1") {
		t.Errorf("ID = %q", got)
	}
}
