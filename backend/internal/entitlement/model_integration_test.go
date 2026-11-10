package entitlement_test

import (
	"context"
	"maps"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/jackc/pgx/v5"
)

// TestEntitlementModel covers WP-071 (ENT-01) against PostgreSQL: rows carry
// limits as JSONB, valid_until and a source; the database itself refuses an
// unknown source and a disabled or expiring cmdb_core; a stored quota
// replaces the plan default.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEntitlementModel(t *testing.T) {
	f := scopetest.Seed(t, "63")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := entitlement.NewPGRepository(f.App)
	svc := entitlement.NewService(repo, entitlement.Options{Enforce: true})

	until := time.Now().UTC().Add(48 * time.Hour).Truncate(time.Second)
	if _, err := svc.Grant(ctx, &entitlement.Entitlement{OrganizationID: f.OrgA, FeatureKey: entitlement.FeatureDiscovery,
		Plan: entitlement.PlanStandard, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxCollectors: 7},
		ValidUntil: &until, Source: "billing"}); err != nil {
		t.Fatal(err)
	}
	items, err := repo.List(ctx, f.OrgA)
	if err != nil || len(items) != 1 {
		t.Fatalf("list: %+v, %v", items, err)
	}
	got := items[0]
	if got.Limits[entitlement.LimitMaxCollectors] != 7 || got.ValidUntil == nil || !got.ValidUntil.Equal(until) || got.Source != "billing" {
		t.Errorf("stored entitlement %+v", got)
	}
	if q, _ := svc.Quota(ctx, f.OrgA, entitlement.LimitMaxCollectors); q == nil || *q != 7 {
		t.Errorf("quota max_collectors %v, want 7", q)
	}
	if q, _ := svc.Quota(ctx, f.OrgA, entitlement.LimitMaxCIs); q != nil {
		t.Errorf("quota max_cis %v, want unlimited without a stored limit (E-34)", *q)
	}

	for name, stmt := range map[string]string{
		"unknown source":    `INSERT INTO entitlement (organization_id, feature_key, source) VALUES ($1, 'webhooks', 'gift')`,
		"disabled core":     `INSERT INTO entitlement (organization_id, feature_key, enabled) VALUES ($1, 'cmdb_core', false)`,
		"expiring core":     `INSERT INTO entitlement (organization_id, feature_key, valid_until) VALUES ($1, 'cmdb_core', now())`,
		"limits not object": `INSERT INTO entitlement (organization_id, feature_key, limits) VALUES ($1, 'topology', '[1]')`,
	} {
		if _, err = f.Admin.Exec(bg, stmt, f.OrgA); err == nil {
			t.Errorf("%s accepted by the database", name)
		}
	}
}

// migratedRow is an entitlement row as the migration test compares it.
type migratedRow struct {
	ID, Plan, Limits, ValidUntil, Source, CreatedAt, UpdatedAt string
	Enabled                                                    bool
}

// sameFields reports whether two rows carry the same plan, enabled, limits,
// valid_until and source.
func (r migratedRow) sameFields(o migratedRow) bool {
	return r.Plan == o.Plan && r.Enabled == o.Enabled && r.Limits == o.Limits && r.ValidUntil == o.ValidUntil && r.Source == o.Source
}

// TestMigration190MovesEntitlementKeys covers WP-244 (ENT-06, E-44, E-34):
// migration 000190 runs up and down inside a rolled-back transaction on an
// essential, a standard and a pro organization with the former keys
// inventory, ticketing and stocktake, a trial organization (source
// selfsignup, valid_until) and an organization without rows. After up each
// organization holds the phase-2 keys of its plan: the successors and the
// copies carry plan, enabled, limits, valid_until and source of their
// former row, an essential organization gets no stock, the other keys are
// added enabled without limits; existing rows, phase-1 and P3/P4 rows stay
// unchanged and none is added; the organization without rows stays empty.
// After down the rows are exactly the former ones.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMigration190MovesEntitlementKeys(t *testing.T) {
	f := scopetest.Seed(t, "63")
	bg := context.Background()
	up, err := os.ReadFile("../../migrations/000190_entitlement_p2_keys.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000190_entitlement_p2_keys.down.sql")
	if err != nil {
		t.Fatal(err)
	}

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%.60s: %v", sql, execErr)
		}
	}

	orgs := map[string]string{}
	for _, name := range []string{"essential", "standard", "pro", "trial", "empty"} {
		plan := name
		switch name {
		case "trial":
			plan = "standard"
		case "empty":
			plan = "pro"
		}
		orgs[name] = f.ID()
		exec(`INSERT INTO organization (id, name, slug, plan) VALUES ($1, $2, $2, $3)`, orgs[name], "mig190-"+name, plan)
	}
	trialEnd := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	insert := func(org, key, plan string, enabled bool, limits string, validUntil *time.Time, source string) {
		t.Helper()
		exec(`INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`, org, key, plan, enabled, limits, validUntil, source)
	}
	licensed := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	// Essential with operator grants of the former standard keys.
	insert(orgs["essential"], "cmdb_core", "essential", true, `{"max_cis": 50}`, nil, "manual")
	insert(orgs["essential"], "discovery", "essential", true, `{}`, &licensed, "billing")
	insert(orgs["essential"], "inventory", "essential", true, `{"limit": 3}`, &licensed, "billing")
	insert(orgs["essential"], "ticketing", "standard", false, `{}`, nil, "manual")
	// Standard.
	insert(orgs["standard"], "cmdb_core", "standard", true, `{}`, nil, "reseller")
	insert(orgs["standard"], "inventory", "standard", true, `{}`, nil, "reseller")
	insert(orgs["standard"], "ticketing", "standard", true, `{}`, &licensed, "reseller")
	insert(orgs["standard"], "stocktake", "standard", false, `{}`, nil, "manual")
	insert(orgs["standard"], "webhooks", "standard", false, `{}`, nil, "manual")
	// Pro with an existing documents row and a P3 key.
	insert(orgs["pro"], "cmdb_core", "pro", true, `{"max_cis": 9000}`, nil, "billing")
	insert(orgs["pro"], "inventory", "pro", true, `{}`, nil, "billing")
	insert(orgs["pro"], "ticketing", "pro", true, `{}`, nil, "billing")
	insert(orgs["pro"], "stocktake", "pro", true, `{}`, nil, "billing")
	insert(orgs["pro"], "documents", "pro", true, `{"max_storage_mb": 50000}`, nil, "billing")
	insert(orgs["pro"], "monitoring", "pro", true, `{}`, nil, "billing")
	// Trial of the standard plan (signup.TrialEntitlements before WP-244).
	insert(orgs["trial"], "cmdb_core", "standard", true, `{"max_cis": 100}`, nil, "selfsignup")
	insert(orgs["trial"], "discovery", "standard", true, `{}`, nil, "selfsignup")
	insert(orgs["trial"], "inventory", "standard", true, `{}`, nil, "selfsignup")
	insert(orgs["trial"], "documents", "standard", true, `{}`, &trialEnd, "selfsignup")
	insert(orgs["trial"], "stocktake", "standard", true, `{}`, &trialEnd, "selfsignup")
	insert(orgs["trial"], "ticketing", "standard", true, `{}`, &trialEnd, "selfsignup")

	rows := func(org string) map[string]migratedRow {
		t.Helper()
		res, queryErr := tx.Query(bg, `SELECT feature_key, id::text, plan, enabled, limits::text,
			coalesce(valid_until::text, ''), source, created_at::text, updated_at::text
			FROM entitlement WHERE organization_id = $1`, org)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		out, collectErr := pgx.CollectRows(res, func(r pgx.CollectableRow) (struct {
			key string
			row migratedRow
		}, error) {
			var v struct {
				key string
				row migratedRow
			}
			scanErr := r.Scan(&v.key, &v.row.ID, &v.row.Plan, &v.row.Enabled, &v.row.Limits, &v.row.ValidUntil,
				&v.row.Source, &v.row.CreatedAt, &v.row.UpdatedAt)
			return v, scanErr
		})
		if collectErr != nil {
			t.Fatal(collectErr)
		}
		m := map[string]migratedRow{}
		for _, v := range out {
			m[v.key] = v.row
		}
		return m
	}
	before := map[string]map[string]migratedRow{}
	for name, org := range orgs {
		before[name] = rows(org)
	}

	exec(string(up))

	former := map[string]bool{"inventory": true, "ticketing": true, "stocktake": true}
	everyPlan := []string{"assets_basic", "documents", "import", "ticketing_essential"}
	fromStandard := []string{"ticketing_standard", "signature", "stock", "stocktake_standard", "barcode", "gps_standard",
		"workplace", "export_datev"}
	plans := map[string][]string{
		"essential": everyPlan,
		"standard":  slices.Concat(everyPlan, fromStandard),
		"pro":       slices.Concat(everyPlan, fromStandard, []string{"internal_orders"}),
		"trial":     slices.Concat(everyPlan, fromStandard),
	}
	// successor → former row whose fields it carries.
	copies := map[string]map[string]string{
		"essential": {"assets_basic": "inventory", "ticketing_essential": "ticketing"},
		"standard": {"assets_basic": "inventory", "stock": "inventory", "ticketing_essential": "ticketing",
			"ticketing_standard": "ticketing", "stocktake_standard": "stocktake"},
		"pro": {"assets_basic": "inventory", "stock": "inventory", "ticketing_essential": "ticketing",
			"ticketing_standard": "ticketing", "stocktake_standard": "stocktake"},
		"trial": {"assets_basic": "inventory", "stock": "inventory", "ticketing_essential": "ticketing",
			"ticketing_standard": "ticketing", "stocktake_standard": "stocktake"},
	}
	orgPlan := map[string]string{"essential": "essential", "standard": "standard", "pro": "pro", "trial": "standard"}
	for name, org := range orgs {
		after := rows(org)
		if name == "empty" {
			if len(after) != 0 {
				t.Errorf("organization without rows got %v", slices.Sorted(maps.Keys(after)))
			}
			continue
		}
		want := map[string]bool{}
		for key := range before[name] {
			if !former[key] {
				want[key] = true
			}
		}
		for _, key := range plans[name] {
			want[key] = true
		}
		if got, wantKeys := slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(want)); !slices.Equal(got, wantKeys) {
			t.Errorf("%s: keys %v, want %v", name, got, wantKeys)
		}
		for key, row := range after {
			if old, ok := before[name][key]; ok {
				if row != old {
					t.Errorf("%s: existing row %s changed: %+v, was %+v", name, key, row, old)
				}
				continue
			}
			if src, ok := copies[name][key]; ok {
				if !row.sameFields(before[name][src]) {
					t.Errorf("%s: %s = %+v, want the fields of %s %+v", name, key, row, src, before[name][src])
				}
				continue
			}
			if row.Plan != orgPlan[name] || !row.Enabled || row.Limits != "{}" || row.ValidUntil != "" || row.Source != "manual" {
				t.Errorf("%s: added %s = %+v, want plan %s, enabled, no limits, no valid_until, source manual", name, key, row, orgPlan[name])
			}
		}
	}
	if after := rows(orgs["essential"]); after["stocktake_standard"] != (migratedRow{}) || after["stock"] != (migratedRow{}) {
		t.Errorf("essential: %+v", after)
	}
	if got := rows(orgs["trial"])["ticketing_standard"]; got.Source != "selfsignup" || got.ValidUntil == "" {
		t.Errorf("trial ticketing_standard %+v, want source selfsignup with the end of the trial", got)
	}
	var kept int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE migration = '000190'
		AND source_table = 'entitlement' AND reason = 'ENT-06 key change (WP-244): former row'
		AND organization_id = ANY ($1::uuid[])`, slices.Collect(maps.Values(orgs))).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 11 {
		t.Errorf("former rows kept in migration_quarantine: %d, want 11", kept)
	}

	exec(string(down))
	for name, org := range orgs {
		if after := rows(org); !maps.Equal(after, before[name]) {
			t.Errorf("%s after down: %+v, want %+v", name, after, before[name])
		}
	}
	var left int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE migration = '000190'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("migration_quarantine keeps %d entries of 000190 after down", left)
	}
}

// TestMigration284MovesEntitlementKeysOfPhase3 covers WP-862 (ENT-06,
// PKG-08, E-151, E-152): migration 000284 runs up and down inside a
// rolled-back transaction on organizations of every plan, a trial
// organization (source selfsignup, valid_until) and an organization without
// rows. After up workflow_forms and compliance are renamed to workflows and
// compliance_findings with plan, enabled, limits, valid_until and source of
// the former row, each organization holds the phase-3 keys of its plan
// (standard: monitoring and ipam; pro: also monitoring_network,
// credential_vault, notifications_sms_pager, workflows and
// compliance_findings; enterprise: also monitoring_access,
// monitoring_analytics and compliance_automation), no plan gets the add-on
// admp, existing rows stay unchanged and the organization without rows stays
// empty. After down the rows are exactly the former ones.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMigration284MovesEntitlementKeysOfPhase3(t *testing.T) {
	f := scopetest.Seed(t, "63")
	bg := context.Background()
	up, err := os.ReadFile("../../migrations/000284_entitlement_p3_keys.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	down, err := os.ReadFile("../../migrations/000284_entitlement_p3_keys.down.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, sql, args...); execErr != nil {
			t.Fatalf("%.60s: %v", sql, execErr)
		}
	}

	orgPlan := map[string]string{"essential": "essential", "standard": "standard", "pro": "pro",
		"enterprise": "enterprise", "trial": "standard", "empty": "pro"}
	orgs := map[string]string{}
	for name, plan := range orgPlan {
		orgs[name] = f.ID()
		exec(`INSERT INTO organization (id, name, slug, plan) VALUES ($1, $2, $2, $3)`, orgs[name], "mig284-"+name, plan)
	}
	insert := func(org, key, plan string, enabled bool, limits string, validUntil *time.Time, source string) {
		t.Helper()
		exec(`INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source)
			VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7)`, org, key, plan, enabled, limits, validUntil, source)
	}
	licensed := time.Date(2027, 3, 1, 0, 0, 0, 0, time.UTC)
	trialEnd := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	insert(orgs["essential"], "cmdb_core", "essential", true, `{}`, nil, "manual")
	// An operator grant of a former key on a smaller plan keeps its fields.
	insert(orgs["standard"], "cmdb_core", "standard", true, `{}`, nil, "reseller")
	insert(orgs["standard"], "workflow_forms", "pro", false, `{"limit": 2}`, &licensed, "manual")
	insert(orgs["pro"], "cmdb_core", "pro", true, `{"max_cis": 9000}`, nil, "billing")
	insert(orgs["pro"], "monitoring", "pro", true, `{}`, &licensed, "billing")
	insert(orgs["pro"], "workflow_forms", "pro", true, `{}`, &licensed, "billing")
	insert(orgs["enterprise"], "cmdb_core", "enterprise", true, `{}`, nil, "billing")
	insert(orgs["enterprise"], "workflow_forms", "enterprise", true, `{}`, nil, "billing")
	insert(orgs["enterprise"], "compliance", "enterprise", true, `{}`, &licensed, "billing")
	insert(orgs["trial"], "cmdb_core", "standard", true, `{"max_cis": 100}`, nil, "selfsignup")
	insert(orgs["trial"], "compliance", "standard", true, `{}`, &trialEnd, "selfsignup")

	rows := func(org string) map[string]migratedRow {
		t.Helper()
		res, queryErr := tx.Query(bg, `SELECT feature_key, id::text, plan, enabled, limits::text,
			coalesce(valid_until::text, ''), source, created_at::text, updated_at::text
			FROM entitlement WHERE organization_id = $1`, org)
		if queryErr != nil {
			t.Fatal(queryErr)
		}
		m := map[string]migratedRow{}
		for res.Next() {
			var key string
			var r migratedRow
			if scanErr := res.Scan(&key, &r.ID, &r.Plan, &r.Enabled, &r.Limits, &r.ValidUntil, &r.Source,
				&r.CreatedAt, &r.UpdatedAt); scanErr != nil {
				t.Fatal(scanErr)
			}
			m[key] = r
		}
		if res.Err() != nil {
			t.Fatal(res.Err())
		}
		return m
	}
	before := map[string]map[string]migratedRow{}
	for name, org := range orgs {
		before[name] = rows(org)
	}

	exec(string(up))

	fromStandard := []string{"monitoring", "ipam"}
	fromPro := slices.Concat(fromStandard, []string{"monitoring_network", "credential_vault", "notifications_sms_pager",
		"workflows", "compliance_findings"})
	plans := map[string][]string{
		"standard":   fromStandard,
		"trial":      fromStandard,
		"pro":        fromPro,
		"enterprise": slices.Concat(fromPro, []string{"monitoring_access", "monitoring_analytics", "compliance_automation"}),
	}
	// successor → former row whose fields it carries.
	renamed := map[string]map[string]string{
		"standard":   {"workflows": "workflow_forms"},
		"pro":        {"workflows": "workflow_forms"},
		"enterprise": {"workflows": "workflow_forms", "compliance_findings": "compliance"},
		"trial":      {"compliance_findings": "compliance"},
	}
	former := map[string]bool{"workflow_forms": true, "compliance": true}
	for name, org := range orgs {
		after := rows(org)
		if name == "empty" {
			if len(after) != 0 {
				t.Errorf("organization without rows got %v", slices.Sorted(maps.Keys(after)))
			}
			continue
		}
		want := map[string]bool{}
		for key := range before[name] {
			if !former[key] {
				want[key] = true
			}
		}
		for _, key := range plans[name] {
			want[key] = true
		}
		for key := range renamed[name] {
			want[key] = true
		}
		if got, wantKeys := slices.Sorted(maps.Keys(after)), slices.Sorted(maps.Keys(want)); !slices.Equal(got, wantKeys) {
			t.Errorf("%s: keys %v, want %v", name, got, wantKeys)
		}
		if _, ok := after["admp"]; ok {
			t.Errorf("%s: the add-on admp is part of no plan", name)
		}
		for key, row := range after {
			if old, ok := before[name][key]; ok {
				if row != old {
					t.Errorf("%s: existing row %s changed: %+v, was %+v", name, key, row, old)
				}
				continue
			}
			if src, ok := renamed[name][key]; ok {
				if !row.sameFields(before[name][src]) {
					t.Errorf("%s: %s = %+v, want the fields of %s %+v", name, key, row, src, before[name][src])
				}
				continue
			}
			if row.Plan != orgPlan[name] || !row.Enabled || row.Limits != "{}" || row.ValidUntil != "" || row.Source != "manual" {
				t.Errorf("%s: added %s = %+v, want plan %s, enabled, no limits, no valid_until, source manual", name, key, row, orgPlan[name])
			}
		}
	}

	exec(string(down))
	for name, org := range orgs {
		if after := rows(org); !maps.Equal(after, before[name]) {
			t.Errorf("%s after down: %+v, want %+v", name, after, before[name])
		}
	}
	var left int
	if err = tx.QueryRow(bg, `SELECT count(*) FROM migration_quarantine WHERE migration = '000284'`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("migration_quarantine keeps %d entries of 000284 after down", left)
	}
}
