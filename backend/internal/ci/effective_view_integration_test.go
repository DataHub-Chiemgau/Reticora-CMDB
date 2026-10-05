package ci_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestEffectiveViewAndOverrideClear covers WP-060 (OVR-01, CI-10): GET and
// list return the effective value (override before observed value) alike;
// setting an override updates the CI at once; clearing it returns the CI to
// the current observed value in one transaction with an audit entry.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEffectiveViewAndOverrideClear(t *testing.T) {
	f := scopetest.Seed(t, "5d")
	bg := context.Background()
	ciID := f.CI(t, f.OrgA, f.Client1, "eff-ci")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET model = 'row-model', attributes = '{"owner":"row","rack":"R1"}' WHERE id = $1`, ciID); err != nil {
		t.Fatal(err)
	}
	user := f.AppUser(t, f.OrgA, "eff-editor")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	repo := ci.NewPGRepository(f.App)
	overrides := override.NewPGRepository(f.App)

	// An override written without updating the row (as before WP-060) is
	// still what every read returns.
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
		VALUES ($1, $2, 'model', '"override-model"', 'legacy', now(), true), ($1, $2, 'rack', NULL, 'removed', now(), true)`, f.OrgA, ciID); err != nil {
		t.Fatal(err)
	}
	effective := func() *ci.Item {
		t.Helper()
		item, err := repo.GetByID(ctx, f.OrgA, ciID)
		if err != nil {
			t.Fatal(err)
		}
		items, _, err := repo.List(ctx, f.OrgA, ci.FilterParams{Search: "eff-ci"}, api.PaginationParams{Limit: 10})
		if err != nil || len(items) != 1 {
			t.Fatalf("list: %d items, %v", len(items), err)
		}
		if items[0].Model != item.Model || items[0].Attributes["owner"] != item.Attributes["owner"] {
			t.Errorf("list and GET differ: %q/%v vs %q/%v", items[0].Model, items[0].Attributes, item.Model, item.Attributes)
		}
		return item
	}
	item := effective()
	if item.Model != "override-model" {
		t.Errorf("model %q, want the override", item.Model)
	}
	if _, ok := item.Attributes["rack"]; ok {
		t.Errorf("attribute removed by override still shown: %v", item.Attributes)
	}

	// Setting an override updates the CI row in the same transaction.
	if _, err := overrides.SetOverride(ctx, f.OrgA, ciID, "owner", "ops", user, "handover", true); err != nil {
		t.Fatal(err)
	}
	var rowOwner string
	if err := f.Admin.QueryRow(bg, `SELECT attributes->>'owner' FROM ci WHERE id = $1`, ciID).Scan(&rowOwner); err != nil || rowOwner != "ops" {
		t.Errorf("CI row after setting the override: owner %q, %v", rowOwner, err)
	}
	if item = effective(); item.Attributes["owner"] != "ops" {
		t.Errorf("effective owner %v, want ops", item.Attributes["owner"])
	}

	// Clearing returns to the current observed value.
	if _, err := overrides.RecordDiscovered(ctx, f.OrgA, ciID, "model", "discovered-model", "snmp"); err != nil {
		t.Fatal(err)
	}
	if _, err := overrides.ClearOverride(ctx, f.OrgA, ciID, "model"); err != nil {
		t.Fatal(err)
	}
	var rowModel string
	if err := f.Admin.QueryRow(bg, `SELECT model FROM ci WHERE id = $1`, ciID).Scan(&rowModel); err != nil || rowModel != "discovered-model" {
		t.Errorf("CI row after clearing: model %q, %v", rowModel, err)
	}
	if item = effective(); item.Model != "discovered-model" {
		t.Errorf("effective model after clearing %q, want the observed value", item.Model)
	}
	// Without an observation the value stays as it is.
	if _, err := overrides.ClearOverride(ctx, f.OrgA, ciID, "owner"); err != nil {
		t.Fatal(err)
	}
	if item = effective(); item.Attributes["owner"] != "ops" {
		t.Errorf("owner after clearing without observation: %v", item.Attributes["owner"])
	}

	var audited []string
	rows, err := f.Admin.Query(bg, `SELECT action || ':' || (changes->>'field') FROM audit_log
		WHERE organization_id = $1 AND resource_id = $2 AND action LIKE 'ci.override_%' AND actor_id = $3 ORDER BY timestamp`, f.OrgA, ciID, user)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var a string
		if err = rows.Scan(&a); err != nil {
			t.Fatal(err)
		}
		audited = append(audited, a)
	}
	rows.Close()
	want := []string{"ci.override_set:owner", "ci.override_cleared:model", "ci.override_cleared:owner"}
	if len(audited) != len(want) || audited[0] != want[0] || audited[1] != want[1] || audited[2] != want[2] {
		t.Errorf("audit entries %v, want %v", audited, want)
	}

	// Structural columns cannot be overridden.
	if _, err = overrides.SetOverride(ctx, f.OrgA, ciID, "location_id", f.ID(), user, "x", true); !errors.Is(err, override.ErrNotOverridable) {
		t.Errorf("override of location_id: %v, want ErrNotOverridable", err)
	}
}
