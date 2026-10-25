package rls_test

// WP-404 (API-09, JOB-03, TEN-09; F-163, probe f5): the job channel follows
// the scope of the job's creator through the complete PostgreSQL router. A
// client-1 user starts a bulk job over 100 client-1 CIs (POST /cis/bulk,
// 202); a client-2 session finds it neither in GET /api/v1/jobs nor in the
// BFF query { jobs { id } }, and GET /jobs/{id}, /result, DELETE and
// /revert answer 404 without the CI ids. An org-wide session sees the job
// in both channels, and so does the creator. A system job (no scope
// snapshot) reaches the org-wide session only.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -v -run '^TestJobChannelHonoursScope$' ./internal/tenant/rls/

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

func TestJobChannelHonoursScope(t *testing.T) {
	f := scopetest.Seed(t, "c3")
	bg := context.Background()
	env := newChannelEnv(t, f)
	creatorUser, otherUser, adminUser := f.AppUser(t, f.OrgA, "job-c1"), f.AppUser(t, f.OrgA, "job-c2"), f.AppUser(t, f.OrgA, "job-admin")
	creator := &channelPrincipal{kind: "creator client 1", org: f.OrgA, user: creatorUser,
		scope: identity.Scope{Clients: only(f.Client1), Sites: all(), Teams: all()}}
	client2 := &channelPrincipal{kind: "client 2", org: f.OrgA, user: otherUser,
		scope: identity.Scope{Clients: only(f.Client2), Sites: all(), Teams: all()}}
	orgWide := &channelPrincipal{kind: "org-wide", org: f.OrgA, user: adminUser,
		scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}

	rows, err := f.Admin.Query(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL), 'job-channel-' || g, 'active'
		FROM generate_series(1, $3::int) g RETURNING id::text`, f.OrgA, f.Client1, ci.BulkJobFromIDs)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	code, body := env.call(creator, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": ci.BulkSetStatus, "ids": ids, "status": "inactive"})
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if err = json.Unmarshal([]byte(body), &accepted); err != nil || code != http.StatusAccepted || accepted.JobID == "" {
		t.Fatalf("bulk over %d CIs as client 1: %d %s, want 202 with a job", len(ids), code, trim(body))
	}
	jobID := accepted.JobID
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		var status string
		if err = f.Admin.QueryRow(bg, `SELECT status FROM job WHERE id = $1`, jobID).Scan(&status); err == nil && jobs.Status(status).Terminal() {
			if status != string(jobs.StatusSucceeded) {
				t.Fatalf("bulk job ended %s", status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bulk job %s not finished after 60 s (%v)", jobID, err)
		}
	}
	var systemJob string
	if err = f.Admin.QueryRow(bg, `INSERT INTO job (organization_id, kind, status) VALUES ($1, 'wp404.system', 'queued') RETURNING id::text`,
		f.OrgA).Scan(&systemJob); err != nil {
		t.Fatal(err)
	}

	channels := map[string]func(p *channelPrincipal) (int, string){
		"rest": func(p *channelPrincipal) (int, string) {
			return env.call(p, http.MethodGet, "/api/v1/jobs?limit=200", nil)
		},
		"bff": func(p *channelPrincipal) (int, string) {
			return env.call(p, http.MethodPost, "/bff/graphql", map[string]any{"query": "{ jobs(first: 100) { id kind status } }"})
		},
	}
	for _, c := range []struct {
		p            *channelPrincipal
		bulk, system bool
	}{
		{creator, true, false},
		{client2, false, false},
		{orgWide, true, true},
	} {
		for name, read := range channels {
			code, body := read(c.p)
			if code != http.StatusOK || strings.Contains(body, `"errors"`) {
				t.Errorf("%s as %s: %d %s", name, c.p.kind, code, trim(body))
				continue
			}
			if got := strings.Contains(body, jobID); got != c.bulk {
				t.Errorf("%s as %s lists the client-1 bulk job: %v, want %v", name, c.p.kind, got, c.bulk)
			}
			if got := strings.Contains(body, systemJob); got != c.system {
				t.Errorf("%s as %s lists the system job: %v, want %v", name, c.p.kind, got, c.system)
			}
		}
	}

	for _, req := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/jobs/" + jobID},
		{http.MethodGet, "/api/v1/jobs/" + jobID + "/result"},
		{http.MethodDelete, "/api/v1/jobs/" + jobID},
		{http.MethodPost, "/api/v1/jobs/" + jobID + "/revert"},
		{http.MethodGet, "/api/v1/jobs/" + systemJob},
		{http.MethodDelete, "/api/v1/jobs/" + systemJob},
	} {
		code, body := env.call(client2, req.method, req.path, nil)
		if code != http.StatusNotFound || strings.Contains(body, ids[0]) {
			t.Errorf("client 2 %s %s: %d %s, want 404 without the CI ids", req.method, req.path, code, trim(body))
		}
	}
	for _, p := range []*channelPrincipal{creator, orgWide} {
		if code, body := env.call(p, http.MethodGet, "/api/v1/jobs/"+jobID+"/result", nil); code != http.StatusOK || !strings.Contains(body, ids[0]) {
			t.Errorf("%s GET the result: %d %s, want 200 with the CI ids", p.kind, code, trim(body))
		}
	}
	var status string
	if err = f.Admin.QueryRow(bg, `SELECT status FROM job WHERE id = $1`, systemJob).Scan(&status); err != nil || status != string(jobs.StatusQueued) {
		t.Errorf("the system job is %s (%v) after the refused cancel, want queued", status, err)
	}
}
