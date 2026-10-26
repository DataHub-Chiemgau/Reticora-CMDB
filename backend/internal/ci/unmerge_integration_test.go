package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestUnmergeAgainstPostgres covers POST /cis/{id}/unmerge (CI-12,
// WP-174) against PostgreSQL: within 30 days both CIs and their references
// return from the snapshot (moved and removed rows alike), the tombstone
// ends; a second unmerge is 404, after the snapshot retention 410. Without
// a later change of the survivor the unmerge needs no force (200, E-80),
// and its audit says so; with one, the retention still answers 410.
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
	var auditForce, overwritten string
	if err := f.Admin.QueryRow(bg, `SELECT changes->>'force', changes->>'overwritten_fields' FROM audit_log
		WHERE action = 'ci.unmerged' AND resource_id = $1`, survivor).Scan(&auditForce, &overwritten); err != nil ||
		auditForce != "false" || overwritten != "[]" {
		t.Errorf("audit of the unmerge without a later change: force=%q overwritten=%q, %v", auditForce, overwritten, err)
	}

	// After the snapshot retention the unmerge is gone (410), also after a
	// later change of the survivor and with force.
	if w := call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`); w.Code != http.StatusOK {
		t.Fatalf("second merge: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPatch, "/api/v1/cis/"+survivor, `{"name":"unmerge-survivor-later"}`); w.Code != http.StatusOK {
		t.Fatalf("edit after the second merge: %d %s", w.Code, w.Body.String())
	}
	exec(`UPDATE ci_merge SET snapshot_until = now() - interval '1 day' WHERE victim_id = $1 AND unmerged_at IS NULL`, victim)
	if w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge", ""); w.Code != http.StatusGone {
		t.Errorf("unmerge after the retention: %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge?force=true", ""); w.Code != http.StatusGone {
		t.Errorf("forced unmerge after the retention: %d", w.Code)
	}
}

// TestAuditT3UnmergeKeepsLaterSurvivorEdits (WP-174, F-097; moved from
// audit_t3_integrity_auditdefect_test.go by WP-485): an unmerge without
// force after a later edit of the survivor refuses with the changed fields
// (E-80, option 1) and leaves the survivor as edited.
func TestAuditT3UnmergeKeepsLaterSurvivorEdits(t *testing.T) {
	f := scopetest.Seed(t, "6e")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	svc := ci.NewService(repo)
	survivor := f.CI(t, f.OrgA, f.Client1, "audit-t3-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "audit-t3-victim")
	if _, err := svc.Merge(ctx, f.OrgA, survivor, victim); err != nil {
		t.Fatalf("merge: %v", err)
	}
	renamed := "audit-t3-survivor-renamed-after-merge"
	if _, err := svc.Update(ctx, f.OrgA, survivor, ci.UpdateRequest{Name: &renamed, Manual: &ci.ManualChange{Author: f.User}}); err != nil {
		t.Fatalf("interim edit of the survivor: %v", err)
	}

	_, unmergeErr := svc.Unmerge(ctx, f.OrgA, victim, false)
	var name string
	if err := f.Admin.QueryRow(context.Background(), `SELECT name FROM ci WHERE id = $1`, survivor).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if name != renamed {
		t.Errorf("survivor name after unmerge (err=%v) = %q, want the later edit %q: the unmerge silently discarded a change made after the merge",
			unmergeErr, name, renamed)
	}
	var conflict *ci.UnmergeConflictError
	if !errors.As(unmergeErr, &conflict) || !errors.Is(unmergeErr, ci.ErrUnmergeConflict) || !slices.Contains(conflict.Fields, "name") {
		t.Errorf("unmerge after a later edit: %v, want the conflict naming the field name", unmergeErr)
	}
}

// TestUnmergeConflictAndForceAgainstPostgres covers the contract of E-80
// (CI-12, WP-485) through the production chain of main.go
// (ci.NewIndexingRepository over the PGRepository): a survivor changed
// after the merge answers 409 with the fields of the later ci_change rows
// and nothing changes; the job revert refuses in the same way; force=true
// restores the snapshot and the audit names force and the overwritten
// fields.
func TestUnmergeConflictAndForceAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "03")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()),
		search.NewCIIndexer(search.NewPGRepository(f.App)))
	svc := ci.NewService(repo)
	mux := chi.NewRouter()
	ci.NewHandler(svc).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		return w
	}
	// state reads what a refused unmerge must leave alone.
	state := func(survivor, victim string) [6]any {
		t.Helper()
		var name, serial string
		var version int64
		var merged, open bool
		var audits int
		if err := f.Admin.QueryRow(bg, `SELECT
			(SELECT name FROM ci WHERE id = $1), (SELECT version FROM ci WHERE id = $1),
			(SELECT COALESCE(serial_number, '') FROM ci WHERE id = $1),
			(SELECT merged_into_id IS NOT NULL FROM ci WHERE id = $2),
			(SELECT unmerged_at IS NULL FROM ci_merge WHERE victim_id = $2 ORDER BY merged_at DESC LIMIT 1),
			(SELECT count(*) FROM audit_log WHERE action = 'ci.unmerged' AND resource_id = $1)`, survivor, victim).
			Scan(&name, &version, &serial, &merged, &open, &audits); err != nil {
			t.Fatal(err)
		}
		return [6]any{name, version, serial, merged, open, audits}
	}

	survivor := f.CI(t, f.OrgA, f.Client1, "force-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "force-victim")
	if w := call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`); w.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", w.Code, w.Body.String())
	}
	var mergeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_merge WHERE victim_id = $1`, victim).Scan(&mergeID); err != nil {
		t.Fatal(err)
	}
	renamed, serial := "force-survivor-renamed", "SN-AFTER-MERGE"
	if _, err := svc.Update(ctx, f.OrgA, survivor, ci.UpdateRequest{Name: &renamed, SerialNumber: &serial,
		Manual: &ci.ManualChange{Author: f.User}}); err != nil {
		t.Fatalf("edit after the merge: %v", err)
	}
	before := state(survivor, victim)

	w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge", "")
	if w.Code != http.StatusConflict {
		t.Fatalf("unmerge after a later edit: %d %s, want 409", w.Code, w.Body.String())
	}
	var problem struct {
		Status         int      `json:"status"`
		Fields         []string `json:"fields"`
		CurrentVersion int64    `json:"current_version"`
		TraceID        string   `json:"trace_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
		t.Fatalf("409 body %s: %v", w.Body.String(), err)
	}
	if !slices.Equal(problem.Fields, []string{"name", "serial_number"}) || problem.CurrentVersion != before[1] ||
		problem.Status != http.StatusConflict || problem.TraceID == "" {
		t.Errorf("409 body %s, want fields [name serial_number] and current_version %v", w.Body.String(), before[1])
	}
	if after := state(survivor, victim); after != before {
		t.Errorf("the refused unmerge changed %v, before %v", after, before)
	}
	if w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge?force=perhaps", ""); w.Code != http.StatusBadRequest {
		t.Errorf("unmerge with an invalid force: %d", w.Code)
	}
	if w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge?force=false", ""); w.Code != http.StatusConflict {
		t.Errorf("unmerge with force=false: %d", w.Code)
	}
	// The job revert is an unmerge without force.
	if _, err := svc.RevertMerge(ctx, f.OrgA, mergeID); !errors.Is(err, ci.ErrUnmergeConflict) {
		t.Errorf("revert of the merge job after a later edit: %v, want ErrUnmergeConflict", err)
	}
	if after := state(survivor, victim); after != before {
		t.Errorf("the refused revert changed %v, before %v", after, before)
	}

	if w := call(http.MethodPost, "/api/v1/cis/"+victim+"/unmerge?force=true", ""); w.Code != http.StatusOK {
		t.Fatalf("unmerge with force: %d %s", w.Code, w.Body.String())
	}
	after := state(survivor, victim)
	if after[0] != "force-survivor" || after[2] != "" || after[3] != false || after[4] != false || after[5] != 1 {
		t.Errorf("after the forced unmerge %v, want the snapshot state of the survivor and the victim restored", after)
	}
	var force bool
	var overwritten []string
	if err := f.Admin.QueryRow(bg, `SELECT (changes->>'force')::boolean, ARRAY(SELECT jsonb_array_elements_text(changes->'overwritten_fields'))
		FROM audit_log WHERE action = 'ci.unmerged' AND resource_id = $1`, survivor).Scan(&force, &overwritten); err != nil {
		t.Fatal(err)
	}
	if !force || !slices.Equal(overwritten, []string{"name", "serial_number"}) {
		t.Errorf("audit of the forced unmerge: force=%v overwritten=%v", force, overwritten)
	}
}
