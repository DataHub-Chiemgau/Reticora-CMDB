package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestDiscoveryConcurrency covers the discovery part of the concurrency
// matrix of ABN-03 (WP-207) with real parallel transactions against
// PostgreSQL:
//   - double ingest: two server instances (each with its own handler and
//     per-process serialization) ingest the same new device at once; the
//     organization ends with exactly one CI for it;
//   - discovery during edit: a manual change and an ingest of the same field
//     run at once; the manual value stays, protected by its override, in
//     every round;
//   - the integrity check finds no orphans, duplicates or cycles afterwards.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDiscoveryConcurrency(t *testing.T) {
	f := scopetest.Seed(t, "c8")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "abn03-editor")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})

	instance := func() *chi.Mux {
		mux := chi.NewRouter()
		discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).
			WithProvenance(provenance{repo: override.NewPGRepository(f.App)}).RegisterRoutes(mux)
		return mux
	}
	instances := []*chi.Mux{instance(), instance()}
	ingest := func(mux *chi.Mux, rec plugins.DeviceRecord) int {
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if err != nil {
			t.Error(err)
			return 0
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		return w.Code
	}
	device := func(serial, model string) plugins.DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, IP: "192.0.2.207",
			Manufacturer: "Reticora Labs", Model: model, Serial: serial}, time.Now().Add(-time.Second))
	}
	count := func(serial string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`,
			f.OrgA, serial).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	together := func(fns ...func()) {
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, fn := range fns {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				fn()
			}()
		}
		close(start)
		wg.Wait()
	}

	for round := 0; round < 5; round++ {
		serial := fmt.Sprintf("ABN03-DOUBLE-%d", round)
		codes := make([]int, len(instances))
		together(
			func() { codes[0] = ingest(instances[0], device(serial, "M1")) },
			func() { codes[1] = ingest(instances[1], device(serial, "M1")) },
		)
		// A refused write (the other instance won the unique serial) is
		// redelivered by the queue; the retry matches the new CI.
		for i, code := range codes {
			if code != http.StatusAccepted {
				if retry := ingest(instances[i], device(serial, "M1")); retry != http.StatusAccepted {
					t.Errorf("round %d: instance %d: %d, retry %d", round, i, code, retry)
				}
			}
		}
		if n := count(serial); n != 1 {
			t.Errorf("round %d: double ingest left %d CIs for one device, want 1", round, n)
		}
	}

	editor := ci.NewService(ci.NewPGRepository(f.App))
	for round := 0; round < 5; round++ {
		serial := fmt.Sprintf("ABN03-EDIT-%d", round)
		if code := ingest(instances[0], device(serial, "M0")); code != http.StatusAccepted {
			t.Fatalf("round %d: first ingest: %d", round, code)
		}
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial).Scan(&id); err != nil {
			t.Fatal(err)
		}
		manual := "manual-model"
		var editErr error
		together(
			func() {
				_, editErr = editor.Update(ctx, f.OrgA, id, ci.UpdateRequest{Model: &manual, Manual: &ci.ManualChange{Author: user},
					ChangeReason: "abn03"})
			},
			func() { _ = ingest(instances[1], device(serial, "auto-model")) },
		)
		if editErr != nil {
			t.Fatalf("round %d: manual change: %v", round, editErr)
		}
		// A later ingest must not overwrite the manual value either.
		_ = ingest(instances[0], device(serial, "auto-model-2"))
		var model string
		var protected bool
		if err := f.Admin.QueryRow(bg, `SELECT c.model, COALESCE(v.protected, false) FROM ci c
			LEFT JOIN ci_field_value v ON v.ci_id = c.id AND v.field_name = 'model' AND v.override_at IS NOT NULL
			WHERE c.id = $1`, id).Scan(&model, &protected); err != nil {
			t.Fatal(err)
		}
		if model != manual || !protected {
			t.Errorf("round %d: model %q (override protected %v), want the manual value", round, model, protected)
		}
	}

	if v := integrityViolations(t, f, f.OrgA); len(v) > 0 {
		t.Errorf("integrity violations after the concurrency matrix: %v", v)
	}
}

// integrityViolations runs tests/integrity-check.sql for one organization
// and returns the checks with violations (ABN-03).
func integrityViolations(t *testing.T, f *scopetest.Fixture, orgID string) map[string]int {
	t.Helper()
	script, err := os.ReadFile("../../../tests/integrity-check.sql")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	tx, err := f.Admin.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT set_config('integrity.org', $1, true)`, orgID); err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(ctx, string(script))
	if err != nil {
		t.Fatalf("integrity check: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		var n int
		if err := rows.Scan(&name, &n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			out[name] = n
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}
