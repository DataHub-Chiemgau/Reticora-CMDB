package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestLifecycleModelAndHealth covers WP-080 (DB-05, E-36) and WP-480
// (F-087) against PostgreSQL:
//   - ci.lifecycle_state exists only for CI types with a lifecycle model and
//     must be a state of that model (trigger). Over HTTP the state is set by
//     a lifecycle transition (lifecycle_state is read-only in PATCH /cis):
//     on a CI of a type without a model it is 422 naming the field
//     to_state, on a CI of a type with a model 200 and the state is stored;
//   - ci.health is read-only and computed from the monitoring state by the
//     worker health (monitoring.HealthWorker), without the alert evaluator:
//     critical or warning for a breached enabled rule of that severity, ok
//     for a fresh sample without a breach, unknown without a sample or with
//     only a sample older than the health window.
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
	transitioned := f.CI(t, f.OrgA, "", "lc-http")
	for _, id := range []string{modeled, transitioned} {
		if err := inTenant(`UPDATE ci SET ci_type_id = $2 WHERE id = $1`, id, modelType); err != nil {
			t.Fatal(err)
		}
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

	// HTTP: the lifecycle transition sets lifecycle_state.
	router := chi.NewRouter()
	lifecycle.NewHandler(lifecycle.NewPGRepository(f.App),
		lifecycle.NewService(lifecycle.NewPGRepository(f.App), lifecycle.NewPGStateStore(f.App), nil),
		lifecycle.NewResolver(f.App)).RegisterRoutes(router)
	transition := func(ciID string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/cis/"+ciID+"/lifecycle-transitions",
			strings.NewReader(`{"to_state":"ordered"}`))
		req = req.WithContext(tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := transition(plain)
	var problem struct {
		Status int    `json:"status"`
		Field  string `json:"field"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &problem); rec.Code != http.StatusUnprocessableEntity || err != nil ||
		problem.Field != "to_state" {
		t.Errorf("transition of a CI without a lifecycle model: %d %s, want 422 with field to_state", rec.Code, rec.Body)
	}
	if got, err := repo.GetByID(ctx, f.OrgA, plain); err != nil || got.LifecycleState != "" {
		t.Errorf("CI without a model after the refused transition: %+v, %v", got, err)
	}
	if rec = transition(transitioned); rec.Code != http.StatusOK {
		t.Errorf("transition of a CI with a lifecycle model: %d %s, want 200", rec.Code, rec.Body)
	}
	if got, err := repo.GetByID(ctx, f.OrgA, transitioned); err != nil || got.LifecycleState != "ordered" {
		t.Errorf("lifecycle state after the transition: %+v, %v; want ordered", got, err)
	}

	// Health from monitoring: critical, warning, ok, unknown.
	critical := f.CI(t, f.OrgA, "", "health-critical")
	warning := f.CI(t, f.OrgA, "", "health-warning")
	healthy := f.CI(t, f.OrgA, "", "health-ok")
	silent := f.CI(t, f.OrgA, "", "health-unknown")
	stale := f.CI(t, f.OrgA, "", "health-stale")
	if err := inTenant(`INSERT INTO alert_rule (organization_id, name, metric_name, condition, threshold, severity)
		 VALUES ($1, 'cpu critical', 'cpu72', 'gt', 90, 'critical'), ($1, 'cpu warning', 'cpu72', 'gt', 50, 'warning')`,
		f.OrgA); err != nil {
		t.Fatal(err)
	}
	for ciID, value := range map[string]float64{critical: 95, warning: 60, healthy: 10} {
		if err := inTenant(`INSERT INTO metric_sample_v (time, organization_id, ci_id, metric_name, value)
			VALUES (now(), $1, $2, 'cpu72', $3)`, f.OrgA, ciID, value); err != nil {
			t.Fatal(err)
		}
	}
	// A sample older than the health window does not count (15 minutes).
	if err := inTenant(`INSERT INTO metric_sample_v (time, organization_id, ci_id, metric_name, value)
		VALUES (now() - interval '1 hour', $1, $2, 'cpu72', 10)`, f.OrgA, stale); err != nil {
		t.Fatal(err)
	}

	// The worker health refreshes without the alert evaluator (F-087).
	store := monitoring.NewPGMetricStore(f.App)
	monitoring.NewHealthWorker(store.HealthRefresher()).Refresh(bg)
	for ciID, want := range map[string]string{critical: "critical", warning: "warning", healthy: "ok", silent: "unknown", stale: "unknown"} {
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

	// The fresh sample of the healthy CI ages out: the next refresh makes it
	// unknown.
	if _, err := f.Admin.Exec(bg, `WITH aged AS (DELETE FROM metric_sample WHERE ci_id = $1 RETURNING *)
		INSERT INTO metric_sample (time, organization_id, ci_id, metric_name, value, labels)
		SELECT time - interval '1 hour', organization_id, ci_id, metric_name, value, labels FROM aged`, healthy); err != nil {
		t.Fatal(err)
	}
	if err := store.HealthRefresher().RefreshHealth(bg); err != nil {
		t.Fatal(err)
	}
	if got, err := repo.GetByID(ctx, f.OrgA, healthy); err != nil || got.Health != "unknown" {
		t.Errorf("health of a CI whose sample aged out: %+v, %v; want unknown", got, err)
	}

	if err := inTenant(`UPDATE ci SET health = 'ok' WHERE id = $1`, critical); !violates(err, "ci_health_read_only") {
		t.Errorf("direct health write: %v, want ci_health_read_only", err)
	}
}
