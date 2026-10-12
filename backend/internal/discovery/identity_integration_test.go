package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

// TestIdentityOverWholeInventory covers WP-086 (REC-02, CH29, NFR-01)
// against PostgreSQL with more than 10,000 CIs: the candidate query finds a
// CI beyond the first 10,000 by serial number, a hostname hit creates a new
// CI and a probable_duplicate review, the client binding keeps a record of
// client 1 away from a client-2 CI and the new CI does not take the other
// CI's serial number.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIdentityOverWholeInventory(t *testing.T) {
	f := scopetest.Seed(t, "78")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	const inventory = 10050
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, status, serial_number, hostname, created_at)
		SELECT $1, $2, 'bulk-' || n, 'active', 'BULK-' || n, 'bulk-host-' || n, now() - make_interval(secs => $3 - n)
		FROM generate_series(1, $3) AS n`, f.OrgA, serverType, inventory); err != nil {
		t.Fatal(err)
	}
	foreign := f.CI(t, f.OrgA, f.Client2, "identity-c2")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = 'C2-SERIAL' WHERE id = $1`, foreign); err != nil {
		t.Fatal(err)
	}

	cis := ci.NewPGRepository(f.App)
	repo := discovery.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(repo, cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	post := func(rec plugins.DeviceRecord) discovery.BulkIngestResponse {
		t.Helper()
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
		var resp discovery.BulkIngestResponse
		if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}
	record := func(name string) plugins.DeviceRecord {
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: name, IP: "192.0.2.78"}, time.Now().Add(-time.Minute))
	}
	count := func(where string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND `+where, append([]any{f.OrgA}, args...)...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// The newest CI lies beyond the first 10,000.
	late := record("late")
	late.Identity.SerialNumber = "BULK-10040"
	start := time.Now()
	if resp := post(late); resp.Updated != 1 || resp.Created != 0 {
		t.Fatalf("serial beyond the first 10,000: %+v", resp)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("ingest against %d CIs took %s", inventory, elapsed)
	}

	// Hostname: a new CI and a probable_duplicate review.
	byName := record("dup")
	byName.Identity.Hostname = "BULK-HOST-9999"
	if resp := post(byName); resp.Created != 1 || resp.ReviewItems != 1 {
		t.Fatalf("hostname hit: %+v", resp)
	}
	var candidates []string
	var newCI string
	if err := f.Admin.QueryRow(bg, `SELECT candidate_ci_ids::text[], payload ->> 'ci_id' FROM review_item
		WHERE organization_id = $1 AND kind = 'probable_duplicate'`, f.OrgA).Scan(&candidates, &newCI); err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || count(`id = $2 AND hostname = 'bulk-host-9999'`, candidates[0]) != 1 || count(`id = $2`, newCI) != 1 {
		t.Errorf("probable_duplicate: candidates %v, new CI %s", candidates, newCI)
	}

	// Client binding: a client-1 record does not match the client-2 CI and
	// leaves its serial number to it.
	scoped := record("client-1-device")
	scoped.Identity.SerialNumber = "C2-SERIAL"
	scoped.ClientID = f.Client1
	if resp := post(scoped); resp.Created != 1 || resp.Updated != 0 {
		t.Fatalf("client-1 record: %+v", resp)
	}
	if n := count(`name = 'client-1-device' AND serial_number IS NULL AND client_id = $2`, f.Client1); n != 1 {
		t.Errorf("client-1 CI without the taken serial: %d", n)
	}
	if n := count(`id = $2 AND serial_number = 'C2-SERIAL'`, foreign); n != 1 {
		t.Errorf("client-2 CI changed")
	}
}
