package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
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

// TestUnmergeRefusesWhileSurvivorIsMergedVictim (WP-687, F-416): an unmerge
// restores exactly the state before its merge and never touches another
// open merge (CI-12). After a->b and b->c the unmerge of a is 409
// unmerge-later-merge naming the merge b->c, also with force=true and as
// the revert of the merge job; nothing changes, b stays a tombstone (301
// to c). Unmerged in order (b->c, then a->b) the references return to
// their CI: the relationship a->x starts at a again, and the 409 for the
// later change of b names the fields. Another CI merged into the survivor
// later blocks the unmerge in the same way.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestUnmergeRefusesWhileSurvivorIsMergedVictim(t *testing.T) {
	f := scopetest.Seed(t, "d7")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	svc := ci.NewService(ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()),
		search.NewCIIndexer(search.NewPGRepository(f.App))))
	mux := chi.NewRouter()
	ci.NewHandler(svc).RegisterRoutes(mux)
	call := func(method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		return w
	}
	merge := func(survivor, victim string) string {
		t.Helper()
		w := call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`)
		var out struct {
			MergeID string `json:"merge_id"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &out) != nil {
			t.Fatalf("merge %s into %s: %d %s", victim, survivor, w.Code, w.Body.String())
		}
		return out.MergeID
	}
	// state reads the CI rows, the open merges, the source of the
	// relationship and the unmerge audits: what a refused unmerge must
	// leave alone.
	state := func(rel string, ids ...string) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, `SELECT string_agg(format('%s deleted=%s into=%s v=%s open_as_victim=%s', c.id, c.deleted_at IS NOT NULL,
				COALESCE(c.merged_into_id::text, '-'), c.version,
				(SELECT count(*) FROM ci_merge m WHERE m.victim_id = c.id AND m.unmerged_at IS NULL)), '; ' ORDER BY c.id)
				|| ' rel_source=' || COALESCE((SELECT source_ci_id::text FROM ci_relationship WHERE id = $2::uuid), '-')
				|| ' audits=' || (SELECT count(*) FROM audit_log WHERE action = 'ci.unmerged' AND resource_id = ANY($1::uuid[]))
			FROM ci c WHERE c.id = ANY($1::uuid[])`, ids, rel).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	refused := func(what string, w *httptest.ResponseRecorder, mergeID, victim, survivor string) {
		t.Helper()
		var p struct {
			Type                 string `json:"type"`
			Status               int    `json:"status"`
			Detail               string `json:"detail"`
			LaterMergeID         string `json:"later_merge_id"`
			LaterMergeVictimID   string `json:"later_merge_victim_id"`
			LaterMergeSurvivorID string `json:"later_merge_survivor_id"`
		}
		if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Type != "urn:reticora:problem:unmerge-later-merge" ||
			p.Status != http.StatusConflict || p.LaterMergeID != mergeID || p.LaterMergeVictimID != victim ||
			p.LaterMergeSurvivorID != survivor || !strings.Contains(p.Detail, mergeID) ||
			!strings.HasPrefix(w.Header().Get("Content-Type"), "application/problem+json") {
			t.Errorf("%s: %d %s, want 409 unmerge-later-merge naming the later merge %s of %s into %s", what, w.Code, w.Body.String(),
				mergeID, victim, survivor)
		}
	}

	a, b, c, x := f.CI(t, f.OrgA, f.Client1, "chain-a"), f.CI(t, f.OrgA, f.Client1, "chain-b"),
		f.CI(t, f.OrgA, f.Client1, "chain-c"), f.CI(t, f.OrgA, f.Client1, "chain-x")
	for i, id := range []string{a, b, c} {
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = $2 WHERE id = $1`, id, fmt.Sprintf("SN-CHAIN-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	var rel string
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
		VALUES ($1, $2, $3, 'depends_on') RETURNING id::text`, f.OrgA, a, x).Scan(&rel); err != nil {
		t.Fatal(err)
	}
	mergeAB := merge(b, a)
	mergeBC := merge(c, b)
	before := state(rel, a, b, c)

	refused("unmerge of a while b is merged into c", call(http.MethodPost, "/api/v1/cis/"+a+"/unmerge", ""), mergeBC, b, c)
	refused("forced unmerge of a while b is merged into c", call(http.MethodPost, "/api/v1/cis/"+a+"/unmerge?force=true", ""), mergeBC, b, c)
	// The job revert is an unmerge without force and refuses in the same way.
	_, err := svc.RevertMerge(ctx, f.OrgA, mergeAB)
	if err == nil || !strings.Contains(err.Error(), "unmerge that merge first: merge "+mergeBC) {
		t.Errorf("revert of the merge a->b while b is merged into c: %v, want the refusal naming the merge %s", err, mergeBC)
	}
	if after := state(rel, a, b, c); after != before {
		t.Errorf("the refused unmerges changed\n%s\nbefore\n%s", after, before)
	}
	if g := call(http.MethodGet, "/api/v1/cis/"+b, ""); g.Code != http.StatusMovedPermanently || !strings.Contains(g.Header().Get("Location"), c) {
		t.Errorf("GET b (victim of the open merge b->c): %d %q, want 301 to c", g.Code, g.Header().Get("Location"))
	}

	// In order: b->c first, then a->b; the relationship returns step by step.
	if w := call(http.MethodPost, "/api/v1/cis/"+b+"/unmerge", ""); w.Code != http.StatusOK {
		t.Fatalf("unmerge of b: %d %s", w.Code, w.Body.String())
	}
	var src string
	if err = f.Admin.QueryRow(bg, `SELECT source_ci_id::text FROM ci_relationship WHERE id = $1`, rel).Scan(&src); err != nil || src != b {
		t.Errorf("relationship after the unmerge of b->c starts at %s (err %v), want b %s", src, err, b)
	}
	// b changed after the merge a->b (merged into c and back): the 409 names
	// the fields of those changes.
	w := call(http.MethodPost, "/api/v1/cis/"+a+"/unmerge", "")
	var conflict struct {
		Detail string   `json:"detail"`
		Fields []string `json:"fields"`
	}
	if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &conflict) != nil ||
		!slices.Contains(conflict.Fields, "merged_into_id") || strings.HasSuffix(conflict.Detail, ": ") {
		t.Errorf("unmerge of a after b was merged and unmerged: %d %s, want 409 naming the changed fields", w.Code, w.Body.String())
	}
	if w = call(http.MethodPost, "/api/v1/cis/"+a+"/unmerge?force=true", ""); w.Code != http.StatusOK {
		t.Fatalf("forced unmerge of a: %d %s", w.Code, w.Body.String())
	}
	if err = f.Admin.QueryRow(bg, `SELECT source_ci_id::text FROM ci_relationship WHERE id = $1`, rel).Scan(&src); err != nil || src != a {
		t.Errorf("relationship after both unmerges starts at %s (err %v), want a %s", src, err, a)
	}
	var open int
	if err = f.Admin.QueryRow(bg, `SELECT
			(SELECT count(*) FROM ci WHERE id = ANY($1::uuid[]) AND (deleted_at IS NOT NULL OR merged_into_id IS NOT NULL))
			+ (SELECT count(*) FROM ci_merge WHERE (victim_id = ANY($1::uuid[]) OR survivor_id = ANY($1::uuid[])) AND unmerged_at IS NULL)`,
		[]string{a, b, c}).Scan(&open); err != nil || open != 0 {
		t.Errorf("after both unmerges %d CIs still merged or merges open (err %v): %s", open, err, state(rel, a, b, c))
	}

	// A CI merged into the survivor later: its merge is unmerged first.
	s2, v1, v2 := f.CI(t, f.OrgA, f.Client1, "later-s"), f.CI(t, f.OrgA, f.Client1, "later-v1"), f.CI(t, f.OrgA, f.Client1, "later-v2")
	merge(s2, v1)
	mergeV2 := merge(s2, v2)
	before = state(rel, s2, v1, v2)
	refused("forced unmerge of v1 after v2 was merged into the same survivor",
		call(http.MethodPost, "/api/v1/cis/"+v1+"/unmerge?force=true", ""), mergeV2, v2, s2)
	if after := state(rel, s2, v1, v2); after != before {
		t.Errorf("the refused unmerge changed\n%s\nbefore\n%s", after, before)
	}
	if w = call(http.MethodPost, "/api/v1/cis/"+v2+"/unmerge", ""); w.Code != http.StatusOK {
		t.Fatalf("unmerge of v2: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodPost, "/api/v1/cis/"+v1+"/unmerge?force=true", ""); w.Code != http.StatusOK {
		t.Errorf("forced unmerge of v1 after v2 was unmerged: %d %s", w.Code, w.Body.String())
	}

	// The later merge into a CI of another client is outside the scope of a
	// client-1 principal (the merge row carries the client of its
	// survivor): the tombstone of b still blocks, the merge stays unnamed.
	a3, b3, c3 := f.CI(t, f.OrgA, f.Client1, "scoped-a"), f.CI(t, f.OrgA, f.Client1, "scoped-b"), f.CI(t, f.OrgA, f.Client2, "scoped-c")
	merge(b3, a3)
	mergeBC3 := merge(c3, b3)
	before = state(rel, a3, b3, c3)
	scoped := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/cis/"+a3+"/unmerge?force=true", nil).WithContext(scoped))
	var p struct {
		Type               string `json:"type"`
		LaterMergeVictimID string `json:"later_merge_victim_id"`
	}
	if w.Code != http.StatusConflict || json.Unmarshal(w.Body.Bytes(), &p) != nil || p.Type != "urn:reticora:problem:unmerge-later-merge" ||
		p.LaterMergeVictimID != b3 || strings.Contains(w.Body.String(), c3) || strings.Contains(w.Body.String(), mergeBC3) {
		t.Errorf("forced unmerge of a by a client-1 principal while b is merged into a CI of client 2: %d %s, "+
			"want 409 unmerge-later-merge naming b only", w.Code, w.Body.String())
	}
	if after := state(rel, a3, b3, c3); after != before {
		t.Errorf("the refused scoped unmerge changed\n%s\nbefore\n%s", after, before)
	}
}
