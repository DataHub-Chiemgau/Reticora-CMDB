package ci_test

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestUnmergeAgainstPostgres covers POST /cis/{id}/unmerge (CI-12,
// WP-174) against PostgreSQL: within 30 days both CIs and their references
// return from the snapshot (moved and removed rows alike), the tombstone
// ends; a second unmerge is 404, after the snapshot retention 410.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUnmergeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b4")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		return w
	}
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	// state reads what the merge changes and the unmerge must bring back.
	state := func(survivor, victim, asset, nic string) [9]any {
		t.Helper()
		var victimRels, selfRels, contacts, fieldValues int
		var assetCI, nicName, serial string
		var deleted, merged bool
		if err := f.Admin.QueryRow(bg, `SELECT
			(SELECT count(*) FROM ci_relationship WHERE $2 IN (source_ci_id, target_ci_id)),
			(SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2),
			(SELECT count(*) FROM ci_contact WHERE ci_id = $2),
			(SELECT count(*) FROM ci_field_value WHERE ci_id = $2),
			(SELECT COALESCE(ci_id::text, '') FROM asset WHERE id = $3),
			(SELECT name FROM network_interface WHERE id = $4),
			(SELECT COALESCE(serial_number, '') FROM ci WHERE id = $2),
			(SELECT deleted_at IS NOT NULL FROM ci WHERE id = $2),
			(SELECT merged_into_id IS NOT NULL FROM ci WHERE id = $2)`, survivor, victim, asset, nic).
			Scan(&victimRels, &selfRels, &contacts, &fieldValues, &assetCI, &nicName, &serial, &deleted, &merged); err != nil {
			t.Fatal(err)
		}
		return [9]any{victimRels, selfRels, contacts, fieldValues, assetCI, nicName, serial, deleted, merged}
	}

	survivor := f.CI(t, f.OrgA, f.Client1, "unmerge-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "unmerge-victim")
	other := f.CI(t, f.OrgA, f.Client1, "unmerge-other")
	asset := f.Asset(t, f.OrgA, f.Client1, "UMRG-1")
	exec(`UPDATE ci SET asset_id = $2, serial_number = 'SN-UNMERGE-1' WHERE id = $1`, victim, asset)
	exec(`UPDATE asset SET ci_id = $1 WHERE id = $2`, victim, asset)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'depends_on'), ($1, $4, $3, 'depends_on'), ($1, $4, $2, 'depends_on')`, f.OrgA, victim, other, survivor)
	nicS, nicV := f.ID(), f.ID()
	exec(`INSERT INTO network_interface (id, organization_id, ci_id, name) VALUES ($1, $2, $3, 'eth0'), ($4, $2, $5, 'eth0')`,
		nicS, f.OrgA, survivor, nicV, victim)
	contact := f.ID()
	exec(`INSERT INTO contact (id, organization_id, client_id, display_name, email) VALUES ($1, $2, $3, 'Unmerge Contact', 'unmerge@example.org')`,
		contact, f.OrgA, f.Client1)
	exec(`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type, client_id) VALUES ($1, $2, $3, 'owner', $4), ($1, $5, $3, 'owner', $4)`,
		f.OrgA, victim, contact, f.Client1, survivor)
	exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
		VALUES ($1, $2, 'model', '"model-s"', 'mine', now(), true), ($1, $3, 'model', '"model-v"', 'theirs', now(), true)`,
		f.OrgA, survivor, victim)
	before := state(survivor, victim, asset, nicV)

	if w := call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`); w.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", w.Code, w.Body.String())
	}
	if merged := state(survivor, victim, asset, nicV); merged == before {
		t.Fatalf("the merge changed nothing: %v", merged)
	}
	if w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge", ""); w.Code != http.StatusOK {
		t.Fatalf("unmerge: %d %s", w.Code, w.Body.String())
	}
	if after := state(survivor, victim, asset, nicV); after != before {
		t.Errorf("after the unmerge %v, before the merge %v", after, before)
	}
	if w := call(http.MethodGet, "/api/v1/cis/"+victim, ""); w.Code != http.StatusOK {
		t.Errorf("GET victim after the unmerge: %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge", ""); w.Code != http.StatusNotFound {
		t.Errorf("second unmerge: %d", w.Code)
	}
	var audits int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE action = 'ci.unmerged' AND resource_id = $1`, survivor).Scan(&audits); err != nil || audits != 1 {
		t.Errorf("audit entries %d, %v", audits, err)
	}

	// After the snapshot retention the unmerge is gone (410).
	if w := call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`); w.Code != http.StatusOK {
		t.Fatalf("second merge: %d %s", w.Code, w.Body.String())
	}
	exec(`UPDATE ci_merge SET snapshot_until = now() - interval '1 day' WHERE victim_id = $1 AND unmerged_at IS NULL`, victim)
	if w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge", ""); w.Code != http.StatusGone {
		t.Errorf("unmerge after the retention: %d", w.Code)
	}
}
