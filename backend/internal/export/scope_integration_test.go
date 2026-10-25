package export_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// TestExportJobRunsWithCreatorScope covers WP-035 (EXP-01, TEN-06): an
// export job records its creator and the creator's scope, the worker renders
// it with exactly that scope — client, site (WP-347) and team snapshots —,
// only the creator reads the job, and a queued job without a snapshot is
// failed instead of exported org-wide.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestExportJobRunsWithCreatorScope(t *testing.T) {
	f := scopetest.Seed(t, "4f")
	bg := context.Background()
	f.CI(t, f.OrgA, f.Client1, "export-scope-own")
	f.CI(t, f.OrgA, f.Client2, "export-scope-foreign")
	other := f.AppUser(t, f.OrgA, "export-other")

	jobs := export.NewPGJobRepository(f.App)
	blobs := blob.NewFileStore(t.TempDir())
	worker := export.NewJobWorker(jobs, ci.NewPGRepository(f.App), blobs)
	mux := chi.NewRouter()
	export.NewJobHandler(jobs, worker, blobs).RegisterRoutes(mux)

	request := func(method, path, body, userID string, scope *database.TenantScope) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userID})
		req = req.WithContext(database.ContextWithTenantScope(ctx, scope))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	otherScope := database.OrgWideScope(f.OrgA, other)

	create := func(scope *database.TenantScope) string {
		t.Helper()
		w := request(http.MethodPost, "/api/v1/export/jobs", `{"format":"json"}`, scope.UserID, scope)
		if w.Code != http.StatusAccepted {
			t.Fatalf("create job: status %d: %s", w.Code, w.Body.String())
		}
		var job struct {
			ID    string           `json:"id"`
			Scope *export.JobScope `json:"scope"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &job); err != nil {
			t.Fatalf("decode job: %v", err)
		}
		if job.Scope == nil {
			t.Fatal("created job carries no scope snapshot")
		}
		return job.ID
	}
	restrictedJob := create(&client1)
	orgWideJob := create(&orgWide)

	// A queued job from before migration 000068 has no snapshot.
	var legacyJob string
	if err := f.Admin.QueryRow(bg, `INSERT INTO export_job (organization_id, initiated_by, format, status) VALUES ($1, $2, 'json', 'pending') RETURNING id::text`,
		f.OrgA, f.User).Scan(&legacyJob); err != nil {
		t.Fatalf("seed legacy job: %v", err)
	}

	for worker.ProcessOne(bg) {
	}

	content := func(jobID string) (status, body string) {
		t.Helper()
		var key string
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(object_key, '') FROM export_job WHERE id = $1`, jobID).Scan(&status, &key); err != nil {
			t.Fatalf("read job %s: %v", jobID, err)
		}
		if key == "" {
			return status, ""
		}
		rc, err := blobs.Get(bg, export.ExportBucket, key)
		if err != nil {
			t.Fatalf("read export object: %v", err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("read export object: %v", err)
		}
		return status, string(data)
	}
	if status, out := content(restrictedJob); status != export.JobStatusCompleted ||
		!strings.Contains(out, "export-scope-own") || strings.Contains(out, "export-scope-foreign") {
		t.Errorf("client-1 job: status %s, content %s; want only the client-1 CI", status, out)
	}
	if status, out := content(orgWideJob); status != export.JobStatusCompleted ||
		!strings.Contains(out, "export-scope-own") || !strings.Contains(out, "export-scope-foreign") {
		t.Errorf("org-wide job: status %s, content %s; want both CIs", status, out)
	}
	if status, _ := content(legacyJob); status != export.JobStatusFailed {
		t.Errorf("job without scope snapshot: status %s, want failed", status)
	}

	// Only the creator reads its jobs.
	if w := request(http.MethodGet, "/api/v1/export/jobs/"+restrictedJob, "", f.User, &client1); w.Code != http.StatusOK ||
		!strings.Contains(w.Body.String(), "download_url") {
		t.Errorf("creator reads own job: status %d body %s", w.Code, w.Body.String())
	}
	if w := request(http.MethodGet, "/api/v1/export/jobs/"+restrictedJob, "", other, &otherScope); w.Code != http.StatusNotFound {
		t.Errorf("other user reads the job: status %d, want 404", w.Code)
	}
	w := request(http.MethodGet, "/api/v1/export/jobs", "", other, &otherScope)
	var list struct {
		Total int `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || list.Total != 0 {
		t.Errorf("other user lists %d jobs (status %d), want none", list.Total, w.Code)
	}
	// The creator reads a job only while its scope covers the job's
	// snapshot, as for the job it is mirrored into (WP-404, WP-409): not the
	// client-1 job restricted to client 2, not the org-wide job (nor the one
	// without snapshot) restricted to client 1, and no download URL.
	client2 := database.OrgWideScope(f.OrgA, f.User)
	client2.Clients = database.ScopeIDs(f.Client2)
	for job, scope := range map[string]*database.TenantScope{restrictedJob: &client2, orgWideJob: &client1, legacyJob: &client1} {
		if w := request(http.MethodGet, "/api/v1/export/jobs/"+job, "", f.User, scope); w.Code != http.StatusNotFound ||
			strings.Contains(w.Body.String(), "download_url") {
			t.Errorf("creator reads job %s outside its scope: status %d body %s, want 404", job, w.Code, w.Body.String())
		}
	}
	for _, c := range []struct {
		scope *database.TenantScope
		want  int
	}{{&client2, 0}, {&client1, 1}, {&orgWide, 3}} {
		w := request(http.MethodGet, "/api/v1/export/jobs", "", f.User, c.scope)
		if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || list.Total != c.want {
			t.Errorf("creator with clients %v lists %d jobs (status %d), want %d", c.scope.Clients.IDs(), list.Total, w.Code, c.want)
		}
	}

	// Site and team snapshots (WP-347, open since WP-035): the export of a
	// site- or team-scoped user holds exactly the CIs that scope reads.
	room := func(name string) string {
		t.Helper()
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`,
			f.Room(t, f.Client1, name)).Scan(&site); err != nil {
			t.Fatalf("site of room %s: %v", name, err)
		}
		return site
	}
	site1, site2 := room("export-site-1"), room("export-site-2")
	for name, site := range map[string]string{"export-site-own": site1, "export-site-foreign": site2} {
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET location_id = $2 WHERE id = $1`, f.CI(t, f.OrgA, f.Client1, name), site); err != nil {
			t.Fatalf("place CI %s: %v", name, err)
		}
	}
	var team string
	if err := f.Admin.QueryRow(bg, `INSERT INTO team (organization_id, name) VALUES ($1, 'export team') RETURNING id::text`, f.OrgA).Scan(&team); err != nil {
		t.Fatalf("seed team: %v", err)
	}
	names := []string{"export-scope-own", "export-scope-foreign", "export-site-own", "export-site-foreign"}
	// readable lists the seeded CIs scope reads under RLS.
	readable := func(scope *database.TenantScope) map[string]bool {
		t.Helper()
		out := map[string]bool{}
		if err := database.WithTenant(bg, f.App, scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT name FROM ci WHERE name = ANY($1)`, names)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					return err
				}
				out[name] = true
			}
			return rows.Err()
		}); err != nil {
			t.Fatalf("read CIs: %v", err)
		}
		return out
	}
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(site1)
	teamScope := database.OrgWideScope(f.OrgA, f.User)
	teamScope.Teams = database.ScopeIDs(team)
	teamClientScope := teamScope
	teamClientScope.Clients = database.ScopeIDs(f.Client1)
	for _, c := range []struct {
		name          string
		scope         *database.TenantScope
		sites, teams  string
		include, omit []string
	}{
		{"site 1", &siteScope, "{" + site1 + "}", "", []string{"export-site-own"}, []string{"export-site-foreign"}},
		{"team", &teamScope, "", "{" + team + "}", nil, nil},
		{"team and client 1", &teamClientScope, "", "{" + team + "}", []string{"export-scope-own"}, []string{"export-scope-foreign"}},
	} {
		jobID := create(c.scope)
		var sites, teams string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(scope_sites::text, ''), COALESCE(scope_teams::text, '') FROM export_job WHERE id = $1`,
			jobID).Scan(&sites, &teams); err != nil || sites != c.sites || teams != c.teams {
			t.Errorf("%s job: snapshot sites %q teams %q (%v), want %q %q", c.name, sites, teams, err, c.sites, c.teams)
		}
		for worker.ProcessOne(bg) {
		}
		status, out := content(jobID)
		if status != export.JobStatusCompleted {
			t.Errorf("%s job: status %s", c.name, status)
			continue
		}
		want := readable(c.scope)
		for _, name := range c.include {
			if !want[name] {
				t.Fatalf("setup: %s does not read %s", c.name, name)
			}
		}
		for _, name := range c.omit {
			if want[name] {
				t.Fatalf("setup: %s reads %s", c.name, name)
			}
		}
		for _, name := range names {
			if strings.Contains(out, `"`+name+`"`) != want[name] {
				t.Errorf("%s job: CI %s exported %v, readable in the scope %v; content %s", c.name, name, !want[name], want[name], out)
			}
		}
	}

	// Resumption after a restart (TEC-14, WP-130): a replica claimed a job
	// and died; after StaleAfter the job is queued again and completed, and
	// the late original run cannot complete it a second time.
	resumed := create(&orgWide)
	claimed, err := jobs.ClaimPending(bg, 10)
	if err != nil || len(claimed) == 0 {
		t.Fatalf("claim: %+v %v", claimed, err)
	}
	if status, _ := content(resumed); status != export.JobStatusRunning {
		t.Fatalf("claimed job: %s", status)
	}
	if n := worker.ResumeStale(bg); n != 0 {
		t.Errorf("a fresh running job was resumed (%d)", n)
	}
	worker.Now = func() time.Time { return time.Now().UTC().Add(export.DefaultStaleAfter + time.Minute) }
	if n := worker.ResumeStale(bg); n < 1 {
		t.Errorf("stale jobs resumed: %d", n)
	}
	if status, _ := content(resumed); status != export.JobStatusPending {
		t.Fatalf("stale job after resumption: %s", status)
	}
	for worker.ProcessOne(bg) {
	}
	if status, out := content(resumed); status != export.JobStatusCompleted || !strings.Contains(out, "export-scope-own") {
		t.Errorf("resumed job: %s %s", status, out)
	}
	if err = jobs.CompleteJob(bg, f.OrgA, resumed, "late", 0, 0, time.Now(), time.Now()); err == nil {
		t.Error("a completed job was completed a second time")
	}
}
