package ci_test

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestLifecycleModelAndHealth covers WP-080 (DB-05, E-36) against
// PostgreSQL:
//   - ci.lifecycle_state exists only for CI types with a lifecycle model and
//     must be a state of that model (trigger, 422 at the lifecycle API);
//   - ci.health is read-only and computed from the monitoring state by the
//     alert evaluator: critical or warning for a breached enabled rule of
//     that severity, ok for samples without a breach, unknown without
//     samples.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestLifecycleModelAndHealth(t *testing.T) {
	f := scopetest.Seed(t, "72")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := ci.NewPGRepository(f.App)
	inTenant := func(sql string, args ...any) error {
		scope := database.OrgWideScope(f.OrgA, f.User)
		return database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		})
	}
	violates := func(err error, constraint string) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.ConstraintName == constraint
	}

	// A type with the physical_asset lifecycle model and one without.
	var modelType string
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name, lifecycle_definition_id)
		VALUES ($1, 'lc-model-72', 'With lifecycle', '00000000-0000-0000-0000-000000000101') RETURNING id::text`,
		f.OrgA).Scan(&modelType); err != nil {
		t.Fatal(err)
	}
	plain := f.CI(t, f.OrgA, "", "lc-plain")
	modeled := f.CI(t, f.OrgA, "", "lc-modeled")
	if err := inTenant(`UPDATE ci SET ci_type_id = $2 WHERE id = $1`, modeled, modelType); err != nil {
		t.Fatal(err)
	}

	if err := inTenant(`UPDATE ci SET lifecycle_state = 'deployed' WHERE id = $1`, plain); !violates(err, "ci_lifecycle_requires_model") {
		t.Errorf("lifecycle state on a type without a model: %v", err)
	}
	if err := inTenant(`UPDATE ci SET lifecycle_state = 'flying' WHERE id = $1`, modeled); !violates(err, "ci_lifecycle_requires_model") {
		t.Errorf("unknown lifecycle state: %v", err)
	}
	if err := inTenant(`UPDATE ci SET lifecycle_state = 'deployed' WHERE id = $1`, modeled); err != nil {
		t.Errorf("state of the model: %v", err)
	}
	if got, err := repo.GetByID(ctx, f.OrgA, modeled); err != nil || got.LifecycleState != "deployed" {
		t.Errorf("lifecycle state read back: %+v, %v", got, err)
	}
	req := httptest.NewRequest("POST", "/", nil).WithContext(ctx)
	if _, err := lifecycle.NewResolver(f.App).LifecycleKeyFor(req, f.OrgA, "ci", plain); !errors.Is(err, lifecycle.ErrNoLifecycleModel) {
		t.Errorf("lifecycle transition of a CI without a model: %v, want ErrNoLifecycleModel (422)", err)
	}

	// Health from monitoring: critical, warning, ok, unknown.
	critical := f.CI(t, f.OrgA, "", "health-critical")
	warning := f.CI(t, f.OrgA, "", "health-warning")
	healthy := f.CI(t, f.OrgA, "", "health-ok")
	silent := f.CI(t, f.OrgA, "", "health-unknown")
	for _, sql := range []string{
		`INSERT INTO alert_rule (organization_id, name, metric_name, condition, threshold, severity)
		 VALUES ($1, 'cpu critical', 'cpu72', 'gt', 90, 'critical'), ($1, 'cpu warning', 'cpu72', 'gt', 50, 'warning')`,
	} {
		if err := inTenant(sql, f.OrgA); err != nil {
			t.Fatal(err)
		}
	}
	for ciID, value := range map[string]float64{critical: 95, warning: 60, healthy: 10} {
		if err := inTenant(`INSERT INTO metric_sample_v (time, organization_id, ci_id, metric_name, value)
			VALUES (now(), $1, $2, 'cpu72', $3)`, f.OrgA, ciID, value); err != nil {
			t.Fatal(err)
		}
	}

	store := monitoring.NewPGMetricStore(f.App)
	monitoring.NewEvaluator(store, store.AlertStore(), nil).WithHealth(store.HealthRefresher()).Evaluate(bg)
	for ciID, want := range map[string]string{critical: "critical", warning: "warning", healthy: "ok", silent: "unknown"} {
		got, err := repo.GetByID(ctx, f.OrgA, ciID)
		if err != nil {
			t.Fatal(err)
		}
		if got.Health != want {
			t.Errorf("health of %s: %q, want %q", got.Name, got.Health, want)
		}
		if want != "unknown" && got.HealthUpdatedAt == nil {
			t.Errorf("health of %s without a timestamp", got.Name)
		}
	}

	if err := inTenant(`UPDATE ci SET health = 'ok' WHERE id = $1`, critical); !violates(err, "ci_health_read_only") {
		t.Errorf("direct health write: %v, want ci_health_read_only", err)
	}
}
