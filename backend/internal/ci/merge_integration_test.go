package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestMergeAgainstPostgres covers POST /cis/{survivor}/merge (CI-12,
// WP-173) against PostgreSQL: relationships, interfaces with IPs, contacts,
// history, document links and the asset link move to the survivor; the
// relationship between both and duplicates are removed; contradicting
// overrides open an override_conflict review; the victim is soft-deleted
// with merged_into_id and GET on it answers 301; a snapshot with 30 days and
// a tombstone with 12 months are recorded; a merge of a CI another merge
// holds (lock or done) is 409.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMergeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b3")
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

	survivor := f.CI(t, f.OrgA, f.Client1, "merge-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "merge-victim")
	other := f.CI(t, f.OrgA, f.Client1, "merge-other")
	asset := f.Asset(t, f.OrgA, f.Client1, "MRG-1")
	exec(`UPDATE ci SET asset_id = $2, serial_number = 'SN-MERGE-1', attributes = '{"tags": ["b"], "rack": "R1"}' WHERE id = $1`, victim, asset)
	exec(`UPDATE asset SET ci_id = $1 WHERE id = $2`, victim, asset)
	exec(`UPDATE ci SET attributes = '{"tags": ["a"]}' WHERE id = $1`, survivor)
	exec(`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'depends_on'), ($1, $4, $3, 'depends_on'), ($1, $2, $4, 'depends_on'), ($1, $2, $5, 'connected_to')`,
		f.OrgA, victim, other, survivor, other)
	nicS, nicV := f.ID(), f.ID()
	exec(`INSERT INTO network_interface (id, organization_id, ci_id, name) VALUES ($1, $2, $3, 'eth0'), ($4, $2, $5, 'eth0')`,
		nicS, f.OrgA, survivor, nicV, victim)
	exec(`INSERT INTO ip_address (organization_id, interface_id, address, type) VALUES ($1, $2, '10.77.0.1', 'static')`, f.OrgA, nicV)
	contact := f.ID()
	exec(`INSERT INTO contact (id, organization_id, client_id, display_name, email) VALUES ($1, $2, $3, 'Merge Contact', 'merge@example.org')`,
		contact, f.OrgA, f.Client1)
	exec(`INSERT INTO ci_contact (organization_id, ci_id, contact_id, relationship_type, client_id) VALUES ($1, $2, $3, 'owner', $4)`,
		f.OrgA, victim, contact, f.Client1)
	doc := f.ID()
	exec(`INSERT INTO document (id, organization_id, title, file_name, storage_key, uploaded_by) VALUES ($1, $2, 'merge', 'f', '', $3)`, doc, f.OrgA, f.AppUser(t, f.OrgA, "merge-uploader"))
	exec(`INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'ci', $3)`, f.OrgA, doc, victim)
	exec(`INSERT INTO ci_field_value (organization_id, ci_id, field_name, override_value, override_reason, override_at, protected)
		VALUES ($1, $2, 'model', '"model-s"', 'mine', now(), true), ($1, $3, 'model', '"model-v"', 'theirs', now(), true),
			($1, $3, 'os_name', '"os-v"', 'only victim', now(), true)`, f.OrgA, survivor, victim)
	exec(`INSERT INTO ci_change (organization_id, ci_id, change_type, field_name, ci_version) VALUES ($1, $2, 'update', 'name', 1)`, f.OrgA, victim)

	// A CI another transaction holds cannot be merged (parallel merge).
	lock, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lock.Exec(bg, "SELECT id FROM ci WHERE id = $1 FOR UPDATE", victim); err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`); w.Code != http.StatusConflict {
		t.Errorf("merge of a locked CI: %d %s", w.Code, w.Body.String())
	}
	_ = lock.Rollback(bg)

	if w := call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+survivor+`"}`); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("self merge: %d", w.Code)
	}
	w := call(http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", w.Code, w.Body.String())
	}
	var res ci.MergeResult
	if err = json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if res.Survivor.AssetID != asset || res.Survivor.SerialNumber != "SN-MERGE-1" || len(res.OverrideConflicts) != 1 ||
		res.OverrideConflicts[0] != "model" || res.Moved["interfaces"] != 1 || res.Moved["contacts"] != 1 || res.Moved["documents"] != 1 {
		t.Errorf("merge result: %+v", res)
	}
	if tags, _ := res.Survivor.Attributes["tags"].([]any); len(tags) != 2 || res.Survivor.Attributes["rack"] != "R1" {
		t.Errorf("merged attributes: %v", res.Survivor.Attributes)
	}
	if d := time.Until(res.TombstoneUntil); d < 360*24*time.Hour {
		t.Errorf("tombstone until %v", res.TombstoneUntil)
	}
	if d := time.Until(res.SnapshotUntil); d < 29*24*time.Hour || d > 31*24*time.Hour {
		t.Errorf("snapshot until %v", res.SnapshotUntil)
	}

	var victimRels, survivorRels, selfRels, ips, contacts, docs, history, conflicts, audits int
	var assetCI, nicName, mergedInto, osOverride string
	var victimDeleted bool
	if err = f.Admin.QueryRow(bg, `SELECT
		(SELECT count(*) FROM ci_relationship WHERE $2 IN (source_ci_id, target_ci_id)),
		(SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $3),
		(SELECT count(*) FROM ci_relationship WHERE source_ci_id = target_ci_id AND source_ci_id = $1),
		(SELECT count(*) FROM ip_address ip JOIN network_interface n ON n.id = ip.interface_id WHERE n.ci_id = $1),
		(SELECT count(*) FROM ci_contact WHERE ci_id = $1),
		(SELECT count(*) FROM document_link WHERE entity_id = $1),
		(SELECT count(*) FROM ci_change WHERE ci_id = $1 AND field_name = 'name'),
		(SELECT count(*) FROM review_item WHERE kind = 'override_conflict' AND object_key = 'ci:' || $1 || '/model' AND status = 'open'),
		(SELECT count(*) FROM audit_log WHERE action = 'ci.merged' AND resource_id = $1),
		(SELECT COALESCE(ci_id::text, '') FROM asset WHERE id = $4),
		(SELECT name FROM network_interface WHERE id = $5),
		(SELECT COALESCE(merged_into_id::text, '') FROM ci WHERE id = $2),
		(SELECT deleted_at IS NOT NULL FROM ci WHERE id = $2),
		(SELECT COALESCE(override_value #>> '{}', '') FROM ci_field_value WHERE ci_id = $1 AND field_name = 'os_name')`,
		survivor, victim, other, asset, nicV).Scan(&victimRels, &survivorRels, &selfRels, &ips, &contacts, &docs, &history,
		&conflicts, &audits, &assetCI, &nicName, &mergedInto, &victimDeleted, &osOverride); err != nil {
		t.Fatal(err)
	}
	if victimRels != 0 || survivorRels != 2 || selfRels != 0 {
		t.Errorf("relationships: victim %d, survivor->other %d, self %d", victimRels, survivorRels, selfRels)
	}
	if ips != 1 || contacts != 1 || docs != 1 || history != 1 || conflicts != 1 || audits != 1 {
		t.Errorf("ips %d, contacts %d, documents %d, history %d, conflicts %d, audits %d", ips, contacts, docs, history, conflicts, audits)
	}
	if assetCI != survivor || nicName == "eth0" || mergedInto != survivor || !victimDeleted || osOverride != "os-v" {
		t.Errorf("asset %s, interface %q, merged into %s, deleted %v, os override %q", assetCI, nicName, mergedInto, victimDeleted, osOverride)
	}

	// Tombstone: GET on the victim is 301 to the survivor.
	if w = call(http.MethodGet, "/api/v1/cis/"+victim, ""); w.Code != http.StatusMovedPermanently ||
		w.Header().Get("Location") != "/api/v1/cis/"+survivor {
		t.Errorf("GET victim: %d %s", w.Code, w.Header().Get("Location"))
	}
	// A merged victim is merged again or restored only through the unmerge.
	if w = call(http.MethodPost, "/api/v1/cis/"+other+"/merge", `{"victim_id":"`+victim+`"}`); w.Code != http.StatusConflict {
		t.Errorf("second merge of the victim: %d", w.Code)
	}
	if w = call(http.MethodPost, "/api/v1/cis/"+victim+"/restore", ""); w.Code != http.StatusConflict {
		t.Errorf("restore of a merged CI: %d", w.Code)
	}
	var snapshot []byte
	if err = f.Admin.QueryRow(bg, `SELECT snapshot FROM ci_merge WHERE victim_id = $1`, victim).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	var snap struct {
		Victim  map[string]any   `json:"victim"`
		Changed []map[string]any `json:"changed"`
		Removed []map[string]any `json:"removed"`
	}
	if err = json.Unmarshal(snapshot, &snap); err != nil || snap.Victim["id"] != victim || len(snap.Changed) == 0 || len(snap.Removed) < 3 {
		t.Errorf("snapshot: %v changed %d removed %d", err, len(snap.Changed), len(snap.Removed))
	}
}

// TestMergeMovesEndpointAgentLink covers WP-484 (F-083) against PostgreSQL:
// the merge moves endpoint_agent.ci_id of the victim to the survivor
// (moved "agents", the row in the snapshot), an agent of another CI stays;
// the unmerge links the agent to the victim again and restores only the
// link, so a later kill-switch of the agent stays.
func TestMergeMovesEndpointAgentLink(t *testing.T) {
	f := scopetest.Seed(t, "b3")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	survivor := f.CI(t, f.OrgA, f.Client1, "agent-merge-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "agent-merge-victim")
	other := f.CI(t, f.OrgA, f.Client1, "agent-merge-other")
	agentV, agentO := f.ID(), f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO endpoint_agent (id, organization_id, client_id, agent_id, hostname, ci_id) VALUES
		($1, $2, $3, 'wp484-victim-agent', 'agent-merge-victim', $4), ($5, $2, $3, 'wp484-other-agent', 'agent-merge-other', $6)`,
		agentV, f.OrgA, f.Client1, victim, agentO, other); err != nil {
		t.Fatal(err)
	}
	link := func(id string) (ciID, status string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(ci_id::text, ''), status FROM endpoint_agent WHERE id = $1`, id).Scan(&ciID, &status); err != nil {
			t.Fatal(err)
		}
		return ciID, status
	}

	res, err := repo.Merge(ctx, f.OrgA, survivor, victim, "")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.Moved["agents"] != 1 {
		t.Errorf("moved agents %d, want 1 (%v)", res.Moved["agents"], res.Moved)
	}
	if got, _ := link(agentV); got != survivor {
		t.Errorf("agent of the victim linked to %s, want the survivor %s", got, survivor)
	}
	if got, _ := link(agentO); got != other {
		t.Errorf("agent of another CI linked to %s, want unchanged %s", got, other)
	}
	var inSnapshot bool
	if err = f.Admin.QueryRow(bg, `SELECT snapshot->'changed' @> jsonb_build_array(jsonb_build_object('table', 'endpoint_agent',
		'row', jsonb_build_object('id', $2::text, 'ci_id', $3::text))) FROM ci_merge WHERE victim_id = $1`, victim, agentV, victim).Scan(&inSnapshot); err != nil {
		t.Fatal(err)
	}
	if !inSnapshot {
		t.Error("the merge snapshot holds no endpoint_agent row with the victim link")
	}

	// The kill-switch after the merge is no part of the merge.
	if _, err = f.Admin.Exec(bg, `UPDATE endpoint_agent SET status = 'disabled' WHERE id = $1`, agentV); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Unmerge(ctx, f.OrgA, victim, false); err != nil {
		t.Fatalf("unmerge: %v", err)
	}
	if got, status := link(agentV); got != victim || status != "disabled" {
		t.Errorf("after the unmerge the agent is linked to %s with status %s, want the victim %s and disabled", got, status, victim)
	}
	if got, _ := link(agentO); got != other {
		t.Errorf("after the unmerge the other agent is linked to %s, want %s", got, other)
	}
}

// mergeJobTrail is the job tracker of a merge that notes the status of the
// ci.merge job in the database after every step of the merge.
type mergeJobTrail struct {
	svc      *jobs.Service
	repo     *jobs.PGRepository
	statuses map[string][]jobs.Status
}

func (m *mergeJobTrail) note(ctx context.Context, orgID, refID string) {
	if job, err := m.repo.GetByRef(ctx, orgID, "ci_merge", refID); err == nil {
		m.statuses[refID] = append(m.statuses[refID], job.Status)
	}
}

func (m *mergeJobTrail) Track(ctx context.Context, orgID, kind, refTable, refID, createdBy string) error {
	err := m.svc.Track(ctx, orgID, kind, refTable, refID, createdBy)
	m.note(ctx, orgID, refID)
	return err
}

func (m *mergeJobTrail) Mirror(ctx context.Context, orgID, refTable, refID string, status jobs.Status, errMsg string) error {
	err := m.svc.Mirror(ctx, orgID, refTable, refID, status, errMsg)
	m.note(ctx, orgID, refID)
	return err
}

func (m *mergeJobTrail) MirrorFailure(ctx context.Context, orgID, refTable, refID string, cause error, public ...error) error {
	err := m.svc.MirrorFailure(ctx, orgID, refTable, refID, cause, public...)
	m.note(ctx, orgID, refID)
	return err
}

// TestMergeRecordsItsJob covers F-251 (WP-572, CI-12) against PostgreSQL:
// POST /cis/{survivor}/merge runs as a job of kind ci.merge, queued →
// running → succeeded, and the merge row names it (ci_merge.job_id), also
// for a principal restricted to a client; a failing merge (the victim is
// locked by another merge) leaves its job failed with the error text and
// no ci_merge row. After the retention run retention.merge_snapshots the
// merge row of an expired tombstone is gone and GET on the victim is 404.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestMergeRecordsItsJob(t *testing.T) {
	f := scopetest.Seed(t, "b3")
	bg := context.Background()
	jobRepo := jobs.NewPGRepository(f.App)
	trail := &mergeJobTrail{svc: jobs.NewService(jobRepo), repo: jobRepo, statuses: map[string][]jobs.Status{}}
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())).WithJobs(trail)).RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	clientCtx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx))
		return w
	}
	survivor := f.CI(t, f.OrgA, f.Client1, "merge-job-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "merge-job-victim")
	merge := func(ctx context.Context, survivor, victim string) *httptest.ResponseRecorder {
		return call(ctx, http.MethodPost, "/api/v1/cis/"+survivor+"/merge", `{"victim_id":"`+victim+`"}`)
	}
	// jobsOf returns the ci.merge jobs tracked since the last call.
	seen := map[string]bool{}
	newJobs := func() []string {
		var out []string
		for ref := range trail.statuses {
			if !seen[ref] {
				seen[ref] = true
				out = append(out, ref)
			}
		}
		return out
	}
	type jobRow struct {
		id, kind, status, errText string
		attempts                  int
		started, finished         bool
	}
	jobOf := func(ref string) jobRow {
		t.Helper()
		var j jobRow
		if err := f.Admin.QueryRow(bg, `SELECT id::text, kind, status, COALESCE(error, ''), attempts, started_at IS NOT NULL,
			finished_at IS NOT NULL FROM job WHERE ref_table = 'ci_merge' AND ref_id = $1`, ref).
			Scan(&j.id, &j.kind, &j.status, &j.errText, &j.attempts, &j.started, &j.finished); err != nil {
			t.Fatalf("job of merge %s: %v", ref, err)
		}
		return j
	}
	mergeRows := func(ref string) (rows int, jobID string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT count(*), COALESCE(max(job_id::text), '') FROM ci_merge WHERE id = $1`, ref).
			Scan(&rows, &jobID); err != nil {
			t.Fatal(err)
		}
		return rows, jobID
	}

	// A failing merge: another transaction holds the victim.
	lock, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = lock.Exec(bg, "SELECT id FROM ci WHERE id = $1 FOR UPDATE", victim); err != nil {
		t.Fatal(err)
	}
	if w := merge(orgCtx, survivor, victim); w.Code != http.StatusConflict {
		t.Errorf("merge of a locked CI: %d %s", w.Code, w.Body.String())
	}
	_ = lock.Rollback(bg)
	failed := newJobs()
	if len(failed) != 1 {
		t.Fatalf("jobs of the failing merge: %v", failed)
	}
	if got := trail.statuses[failed[0]]; len(got) != 3 || got[0] != jobs.StatusQueued || got[1] != jobs.StatusRunning || got[2] != jobs.StatusFailed {
		t.Errorf("status of the failing merge job: %v, want queued, running, failed", got)
	}
	if j := jobOf(failed[0]); j.kind != ci.MergeJobKind || j.status != string(jobs.StatusFailed) || j.errText != ci.ErrMergeConflict.Error() || !j.finished {
		t.Errorf("failed merge job: %+v", j)
	}
	if rows, _ := mergeRows(failed[0]); rows != 0 {
		t.Errorf("the failing merge left %d ci_merge rows", rows)
	}

	// The merge succeeds: its row names its job.
	w := merge(orgCtx, survivor, victim)
	if w.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", w.Code, w.Body.String())
	}
	var res ci.MergeResult
	if err = json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	if got := newJobs(); len(got) != 1 || got[0] != res.MergeID {
		t.Fatalf("jobs of the merge %s: %v", res.MergeID, got)
	}
	if got := trail.statuses[res.MergeID]; len(got) != 3 || got[0] != jobs.StatusQueued || got[1] != jobs.StatusRunning || got[2] != jobs.StatusSucceeded {
		t.Errorf("status of the merge job: %v, want queued, running, succeeded", got)
	}
	j := jobOf(res.MergeID)
	if j.kind != ci.MergeJobKind || j.status != string(jobs.StatusSucceeded) || j.errText != "" || j.attempts != 1 || !j.started || !j.finished {
		t.Errorf("merge job: %+v", j)
	}
	if rows, jobID := mergeRows(res.MergeID); rows != 1 || jobID != j.id {
		t.Errorf("ci_merge %s: %d rows, job_id %q, want the job %s", res.MergeID, rows, jobID, j.id)
	}

	// A principal restricted to the client sees its job: the row names it.
	survivor2 := f.CI(t, f.OrgA, f.Client1, "merge-job-survivor-2")
	victim2 := f.CI(t, f.OrgA, f.Client1, "merge-job-victim-2")
	if w = merge(clientCtx, survivor2, victim2); w.Code != http.StatusOK {
		t.Fatalf("merge of the client principal: %d %s", w.Code, w.Body.String())
	}
	if err = json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	newJobs()
	if rows, jobID := mergeRows(res.MergeID); rows != 1 || jobID == "" || jobID != jobOf(res.MergeID).id {
		t.Errorf("ci_merge of the client principal: %d rows, job_id %q", rows, jobID)
	}

	// The tombstone of the first merge expired: the retention run deletes
	// the merge row, GET on the victim is 404.
	if _, err = f.Admin.Exec(bg, `UPDATE ci_merge SET merged_at = now() - interval '13 months',
		tombstone_until = now() - interval '1 month', snapshot_until = now() - interval '12 months' WHERE victim_id = $1`, victim); err != nil {
		t.Fatal(err)
	}
	if err = jobs.NewRetention(f.App, jobs.RetentionFromDays(10, 1095, 90, 365)).RunMergeSnapshots(bg); err != nil {
		t.Fatal(err)
	}
	var left int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_merge WHERE victim_id = $1`, victim).Scan(&left); err != nil || left != 0 {
		t.Errorf("merge rows of the expired tombstone: %d %v", left, err)
	}
	if w = call(orgCtx, http.MethodGet, "/api/v1/cis/"+victim, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET victim after the tombstone: %d %s", w.Code, w.Header().Get("Location"))
	}
	if w = call(orgCtx, http.MethodGet, "/api/v1/cis/"+victim2, ""); w.Code != http.StatusMovedPermanently {
		t.Errorf("GET victim of the young merge: %d", w.Code)
	}
}
