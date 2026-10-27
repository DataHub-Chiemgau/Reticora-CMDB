package history_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestStateSnapshotContentAgainstPostgres covers JOB-04 (WP-516, retest of
// F-164) as reticora_app: the state snapshot replays the real change trail.
// A CI renamed by PATCH has its new name in the snapshot of now and its old
// name at a time between its creation and the rename, with the personal
// value of its creation unsealed as GET /history/ci/{id} shows it; an asset
// writes its creation, update and deletion to entity_change in the
// transaction of the write and replays the same way. An unknown id and the
// client-1 CI for a client-2 principal are 404 without a job, also on GET
// /cis/{id}/state and /assets/{id}/state; a time before the creation fails
// the job with "no recorded state before <at>" (the kind as the server
// registers it, three attempts, in-process) and is 404 on the direct read.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestStateSnapshotContentAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "b1")
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })

	jobSvc := jobs.NewService(jobs.NewPGRepository(f.App))
	jobSvc.Register(jobs.Kind{Name: history.SnapshotJobKind, MaxAttempts: 3, Timeout: 15 * time.Minute, Cancellable: true})
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))).RegisterRoutes(mux)
	asset.NewHandler(asset.NewPGRepository(f.App)).RegisterRoutes(mux)
	history.NewHandler(history.NewPGRepository(f.App)).WithJobs(jobSvc).RegisterRoutes(mux)

	principal := func(ctx context.Context) context.Context {
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		return identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Permissions: identity.AllPermissions()})
	}
	orgCtx, client2Ctx := principal(f.OrgCtx(f.OrgA)), principal(f.ClientCtx(f.Client2))
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	jobCount := func() int {
		t.Helper()
		_, total, lerr := jobSvc.List(orgCtx, f.OrgA, jobs.Filter{Kind: history.SnapshotJobKind}, api.PaginationParams{Limit: 1})
		if lerr != nil {
			t.Fatal(lerr)
		}
		return total
	}
	// snapshotJob runs the snapshot job of entityType/id at at (nil: now)
	// and returns the job once it ended.
	snapshotJob := func(entityType, id string, at *time.Time) *jobs.Job {
		t.Helper()
		body := `{"entity_type":"` + entityType + `","entity_id":"` + id + `"`
		if at != nil {
			body += `,"at":"` + at.UTC().Format(time.RFC3339Nano) + `"`
		}
		w := call(orgCtx, http.MethodPost, "/api/v1/history/snapshots", body+"}")
		var accepted struct {
			JobID string `json:"job_id"`
		}
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &accepted) != nil {
			t.Fatalf("POST snapshot of %s %s: %d %s, want 202", entityType, id, w.Code, w.Body)
		}
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			job, gerr := jobSvc.Get(orgCtx, f.OrgA, accepted.JobID)
			if gerr != nil {
				t.Fatal(gerr)
			}
			if job.Status.Terminal() {
				return job
			}
		}
		t.Fatalf("snapshot job %s did not end", accepted.JobID)
		return nil
	}
	fields := func(job *jobs.Job) history.StateSnapshot {
		t.Helper()
		var s history.StateSnapshot
		if job.Status != jobs.StatusSucceeded || json.Unmarshal(job.Result, &s) != nil {
			t.Fatalf("snapshot job: %s %s %s, want succeeded with the state", job.Status, job.Error, job.Result)
		}
		return s
	}
	state := func(ctx context.Context, path string, at *time.Time) (int, history.StateSnapshot) {
		t.Helper()
		if at != nil {
			path += "?at=" + at.UTC().Format(time.RFC3339Nano)
		}
		w := call(ctx, http.MethodGet, path, "")
		var s history.StateSnapshot
		if w.Code == http.StatusOK {
			if uerr := json.Unmarshal(w.Body.Bytes(), &s); uerr != nil {
				t.Fatalf("GET %s: %v", path, uerr)
			}
		}
		return w.Code, s
	}
	// between is a time after the first and before the second write.
	between := func(first, second time.Time) time.Time {
		t.Helper()
		if !second.After(first) {
			t.Fatalf("writes at %s and %s: no time between", first, second)
		}
		return first.Add(second.Sub(first) / 2)
	}

	// CI: created with a personal attribute value, renamed by PATCH.
	var typeID string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	w := call(orgCtx, http.MethodPost, "/api/v1/cis", `{"ci_type_id":"`+typeID+`","client_id":"`+f.Client1+`","name":"snap-ci","status":"active",
		"attributes":{"contact_email":"snap-owner@example.invalid"}}`)
	var created ci.Item
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &created) != nil {
		t.Fatalf("POST /cis: %d %s", w.Code, w.Body)
	}
	time.Sleep(20 * time.Millisecond)
	if w = call(orgCtx, http.MethodPatch, "/api/v1/cis/"+created.ID, `{"name":"snap-ci-renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH /cis: %d %s", w.Code, w.Body)
	}
	var createdAt, renamedAt time.Time
	var sealed string
	if err = f.Admin.QueryRow(bg, `SELECT min(created_at) FILTER (WHERE change_type = 'create'), max(created_at) FILTER (WHERE field_name = 'name'),
			min(new_value->'attributes'->>'contact_email') FILTER (WHERE change_type = 'create')
		FROM ci_change WHERE ci_id = $1`, created.ID).Scan(&createdAt, &renamedAt, &sealed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sealed, pii.RefKey) {
		t.Fatalf("precondition: the personal value of the creation is stored as %q, want a surrogate (SEC-05)", sealed)
	}
	mid := between(createdAt, renamedAt)

	now := fields(snapshotJob("ci", created.ID, nil))
	attrs, _ := now.Fields["attributes"].(map[string]any)
	if now.EntityID != created.ID || now.Fields["name"] != "snap-ci-renamed" || now.Deleted || attrs["contact_email"] != "snap-owner@example.invalid" {
		t.Errorf("CI snapshot now: %+v, want the new name and the unsealed contact_email", now)
	}
	if old := fields(snapshotJob("ci", created.ID, &mid)); old.Fields["name"] != "snap-ci" || !old.At.Equal(mid) {
		t.Errorf("CI snapshot between creation and rename: %+v, want the old name", old)
	}
	if code, s := state(orgCtx, "/api/v1/cis/"+created.ID+"/state", &mid); code != http.StatusOK || s.Fields["name"] != "snap-ci" {
		t.Errorf("GET /cis/{id}/state between creation and rename: %d %+v, want the old name", code, s)
	}
	if code, s := state(orgCtx, "/api/v1/cis/"+created.ID+"/state", nil); code != http.StatusOK || s.Fields["name"] != "snap-ci-renamed" {
		t.Errorf("GET /cis/{id}/state: %d %+v, want the new name", code, s)
	}
	// The same unsealed value as the history of the CI.
	var hist history.ListResponse
	if w = call(orgCtx, http.MethodGet, "/api/v1/history/ci/"+created.ID, ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &hist) != nil {
		t.Fatalf("GET /history/ci: %d %s", w.Code, w.Body)
	}
	for _, c := range hist.Data {
		if c.Source == history.SourceCIChange && c.ChangeType == "create" {
			v, _ := c.NewValue.(map[string]any)
			a, _ := v["attributes"].(map[string]any)
			if a["contact_email"] != attrs["contact_email"] {
				t.Errorf("history of the creation shows contact_email %v, the snapshot %v", a["contact_email"], attrs["contact_email"])
			}
		}
	}

	// Before the creation: the job fails, the direct read is 404.
	before := createdAt.Add(-time.Hour)
	if job := snapshotJob("ci", created.ID, &before); job.Status != jobs.StatusFailed ||
		job.Error != "no recorded state before "+before.UTC().Format(time.RFC3339Nano) || len(job.Result) > 0 && string(job.Result) != "null" {
		t.Errorf("CI snapshot before the creation: %s %q %s, want failed with no recorded state", job.Status, job.Error, job.Result)
	}
	if code, _ := state(orgCtx, "/api/v1/cis/"+created.ID+"/state", &before); code != http.StatusNotFound {
		t.Errorf("GET /cis/{id}/state before the creation: %d, want 404", code)
	}

	// Unknown id and the client-1 CI for a client-2 principal: 404, no job.
	jobsBefore := jobCount()
	unknown := f.ID()
	for _, c := range []struct {
		name string
		ctx  context.Context
		body string
	}{
		{"unknown ci", orgCtx, `{"entity_type":"ci","entity_id":"` + unknown + `"}`},
		{"unknown asset", orgCtx, `{"entity_type":"asset","entity_id":"` + unknown + `"}`},
		{"client-1 ci for client 2", client2Ctx, `{"entity_type":"ci","entity_id":"` + created.ID + `"}`},
	} {
		if w = call(c.ctx, http.MethodPost, "/api/v1/history/snapshots", c.body); w.Code != http.StatusNotFound {
			t.Errorf("POST snapshot of the %s: %d %s, want 404", c.name, w.Code, w.Body)
		}
	}
	if n := jobCount(); n != jobsBefore {
		t.Errorf("refused snapshots created %d jobs", n-jobsBefore)
	}
	for _, c := range []struct {
		ctx  context.Context
		path string
	}{
		{orgCtx, "/api/v1/cis/" + unknown + "/state"},
		{orgCtx, "/api/v1/assets/" + unknown + "/state"},
		{orgCtx, "/api/v1/cis/not-a-uuid/state"},
		{client2Ctx, "/api/v1/cis/" + created.ID + "/state"},
	} {
		if code, _ := state(c.ctx, c.path, nil); code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", c.path, code)
		}
	}

	// Asset: creation and update in entity_change, replayed the same way.
	w = call(orgCtx, http.MethodPost, "/api/v1/assets", `{"asset_tag":"snap-a1","name":"snap-asset","client_id":"`+f.Client1+`","notes":"n1"}`)
	var a asset.Asset
	if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &a) != nil {
		t.Fatalf("POST /assets: %d %s", w.Code, w.Body)
	}
	time.Sleep(20 * time.Millisecond)
	if w = call(orgCtx, http.MethodPatch, "/api/v1/assets/"+a.ID, `{"name":"snap-asset-renamed","notes":"n1"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH /assets: %d %s", w.Code, w.Body)
	}
	type row struct {
		changeType, field, actor string
		oldValue, newValue       []byte
		at                       time.Time
	}
	trail := func() []row {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT change_type, COALESCE(field_name, ''), COALESCE(actor_id::text, ''), old_value, new_value, created_at
			FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1 ORDER BY created_at, id`, a.ID)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if serr := rows.Scan(&r.changeType, &r.field, &r.actor, &r.oldValue, &r.newValue, &r.at); serr != nil {
				t.Fatal(serr)
			}
			out = append(out, r)
		}
		return out
	}
	rows := trail()
	if len(rows) != 2 || rows[0].changeType != "create" || rows[0].actor != f.User || !strings.Contains(string(rows[0].newValue), `"snap-asset"`) ||
		rows[1].changeType != "update" || rows[1].field != "name" || string(rows[1].oldValue) != `"snap-asset"` || string(rows[1].newValue) != `"snap-asset-renamed"` {
		t.Fatalf("asset trail: %+v, want the creation and one update of name with old and new value", rows)
	}
	assetMid := between(rows[0].at, rows[1].at)
	if s := fields(snapshotJob("asset", a.ID, nil)); s.Fields["name"] != "snap-asset-renamed" || s.Fields["asset_tag"] != "snap-a1" || s.Deleted {
		t.Errorf("asset snapshot now: %+v, want the new name", s)
	}
	if s := fields(snapshotJob("asset", a.ID, &assetMid)); s.Fields["name"] != "snap-asset" {
		t.Errorf("asset snapshot between creation and update: %+v, want the old name", s)
	}
	if code, s := state(orgCtx, "/api/v1/assets/"+a.ID+"/state", &assetMid); code != http.StatusOK || s.Fields["name"] != "snap-asset" {
		t.Errorf("GET /assets/{id}/state between creation and update: %d %+v, want the old name", code, s)
	}
	assetBefore := rows[0].at.Add(-time.Hour)
	if job := snapshotJob("asset", a.ID, &assetBefore); job.Status != jobs.StatusFailed || !strings.HasPrefix(job.Error, "no recorded state before ") {
		t.Errorf("asset snapshot before the creation: %s %q, want failed", job.Status, job.Error)
	}
	if code, _ := state(client2Ctx, "/api/v1/assets/"+a.ID+"/state", nil); code != http.StatusNotFound {
		t.Errorf("GET /assets/{id}/state of a client-1 asset for client 2: %d, want 404", code)
	}
	if w = call(client2Ctx, http.MethodPost, "/api/v1/history/snapshots", `{"entity_type":"asset","entity_id":"`+a.ID+`"}`); w.Code != http.StatusNotFound {
		t.Errorf("POST snapshot of a client-1 asset for client 2: %d %s, want 404", w.Code, w.Body)
	}
	if w = call(orgCtx, http.MethodGet, "/api/v1/history/asset/"+a.ID, ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &hist) != nil || hist.Total != 2 {
		t.Errorf("GET /history/asset: %d %s, want the 2 changes", w.Code, w.Body)
	}
	if w = call(client2Ctx, http.MethodGet, "/api/v1/history/asset/"+a.ID, ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &hist) != nil || hist.Total != 0 {
		t.Errorf("GET /history/asset of a client-1 asset for client 2: %d %s, want no entries", w.Code, w.Body)
	}

	// The deletion is recorded with the asset as it was.
	if w = call(orgCtx, http.MethodDelete, "/api/v1/assets/"+a.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE /assets: %d %s", w.Code, w.Body)
	}
	if rows = trail(); len(rows) != 3 || rows[2].changeType != "delete" || !strings.Contains(string(rows[2].oldValue), `"snap-asset-renamed"`) || rows[2].newValue != nil {
		t.Errorf("asset trail after the deletion: %+v, want a delete row with the asset as it was", rows)
	}
}
