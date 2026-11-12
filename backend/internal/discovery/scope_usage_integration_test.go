package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// TestScopeRunUsageOnlyScopeBundle (CRD-06, WP-898): the usage a scope run
// reports is checked against the bundle part of the reporting scope. A
// collector without client runs scope X (client 1) and scope Y (client 2);
// a credential of scope X reported by scope Y for a CI of client 2 is
// rejected: no usage row, no credential.resolution_failed.
func TestScopeRunUsageOnlyScopeBundle(t *testing.T) {
	f := scopetest.Seed(t, "a7")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	credX, credY, collector, scopeX, scopeY := f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO credential (id, organization_id, client_id, name, kind, ciphertext)
		VALUES ($1, $2, $3, 'wp898-scope-x', 'ssh_password', '\x00')`, credX, f.OrgA, f.Client1)
	exec(`INSERT INTO credential (id, organization_id, client_id, name, kind, ciphertext)
		VALUES ($1, $2, $3, 'wp898-scope-y', 'ssh_password', '\x00')`, credY, f.OrgA, f.Client2)
	exec(`INSERT INTO collector (id, organization_id, name, status) VALUES ($1, $2, 'wp898-two-clients', 'active')`,
		collector, f.OrgA)
	exec(`INSERT INTO discovery_scope (id, organization_id, collector_id, client_id, name, kind, target, credential_ids)
		VALUES ($1, $2, $3, $4, 'wp898-x', 'host', '10.96.1.1', ARRAY[$5]::uuid[])`, scopeX, f.OrgA, collector, f.Client1, credX)
	exec(`INSERT INTO discovery_scope (id, organization_id, collector_id, client_id, name, kind, target, credential_ids)
		VALUES ($1, $2, $3, $4, 'wp898-y', 'host', '10.96.2.1', ARRAY[$5]::uuid[])`, scopeY, f.OrgA, collector, f.Client2, credY)
	ci := f.CI(t, f.OrgA, f.Client2, "wp898-ci-client2")
	exec(`UPDATE ci SET management_ip = '10.96.2.1' WHERE id = $1`, ci)

	repo := discovery.NewPGRepository(f.App)
	report := func(cred string) credential.UsageOutcome {
		t.Helper()
		out, err := repo.RecordScopeRunUsage(f.OrgCtx(f.OrgA), f.OrgA, collector, scopeY,
			&discovery.ScopeRun{At: time.Now(), Result: map[string]any{"records": 1}},
			[]credential.UsageReport{{Target: "10.96.2.1", Namespace: "ssh", CredentialID: cred, Cause: credential.UsageAuthFailed}})
		if err != nil {
			t.Fatalf("RecordScopeRunUsage: %v", err)
		}
		return out
	}

	if out := report(credX); out.Rejected != 1 || out.Recorded != 0 || out.Events != 0 {
		t.Errorf("credential of scope X reported by scope Y: outcome %+v, want one rejected", out)
	}
	if n := count(`SELECT count(*) FROM credential_usage WHERE credential_id = $1`, credX); n != 0 {
		t.Errorf("credential of scope X has %d usage rows at a CI of client 2", n)
	}
	if n := count(`SELECT count(*) FROM event_outbox WHERE type = $1 AND entity_id = $2`,
		events.CredentialResolutionFailed, credX); n != 0 {
		t.Errorf("credential of scope X raised %d resolution_failed events", n)
	}

	// The scope's own credential is recorded (control).
	if out := report(credY); out.Recorded != 1 || out.Events != 1 {
		t.Errorf("credential of scope Y: outcome %+v, want recorded with one event", out)
	}
}

// TestJobReportsCredentialUsage (CRD-06, WP-898): a finished ad-hoc job
// records the credential usage of its scope passes like a scope run,
// checked per scope, without touching the scope's run.
func TestJobReportsCredentialUsage(t *testing.T) {
	f := scopetest.Seed(t, "a7")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	cred, otherCred, collector, scope, job := f.ID(), f.ID(), f.ID(), f.ID(), f.ID()
	exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext)
		VALUES ($1, $2, 'wp898-job', 'ssh_password', '\x00')`, cred, f.OrgA)
	exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext)
		VALUES ($1, $2, 'wp898-job-other', 'ssh_password', '\x00')`, otherCred, f.OrgA)
	exec(`INSERT INTO collector (id, organization_id, name, status) VALUES ($1, $2, 'wp898-job', 'active')`, collector, f.OrgA)
	exec(`INSERT INTO discovery_scope (id, organization_id, collector_id, name, kind, target, credential_ids)
		VALUES ($1, $2, $3, 'wp898-job', 'host', '10.96.3.1', ARRAY[$4]::uuid[])`, scope, f.OrgA, collector, cred)
	exec(`INSERT INTO discovery_job (id, organization_id, collector_id, job_type, status)
		VALUES ($1, $2, $3, 'poll', 'running')`, job, f.OrgA, collector)
	ci := f.CI(t, f.OrgA, "", "wp898-job-ci")
	exec(`UPDATE ci SET management_ip = '10.96.3.1' WHERE id = $1`, ci)

	h := discovery.NewHandler(discovery.NewPGRepository(f.App), nil)
	if _, err := h.RecordCollectorJobStatus(f.OrgCtx(f.OrgA), f.OrgA, collector, &discovery.JobStatusReport{
		JobID: job, Status: discovery.JobStatusCompleted, Result: map[string]any{"records": 1},
		CredentialUsage: []discovery.JobScopeUsage{{ScopeID: scope, Usage: []credential.UsageReport{
			{Target: "10.96.3.1", Namespace: "ssh", CredentialID: cred, OK: true},
			{Target: "10.96.3.1", Namespace: "ssh", CredentialID: otherCred, OK: true},
		}}},
	}); err != nil {
		t.Fatalf("RecordCollectorJobStatus: %v", err)
	}
	var rows, otherRows int
	var lastRun *time.Time
	if err := f.Admin.QueryRow(bg, `SELECT
			(SELECT count(*) FROM credential_usage WHERE credential_id = $1 AND ci_id = $3 AND last_success_at IS NOT NULL),
			(SELECT count(*) FROM credential_usage WHERE credential_id = $2),
			(SELECT last_run_at FROM discovery_scope WHERE id = $4)`, cred, otherCred, ci, scope).
		Scan(&rows, &otherRows, &lastRun); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || otherRows != 0 {
		t.Errorf("usage rows: scope credential %d (want 1), credential outside the bundle %d (want 0)", rows, otherRows)
	}
	if lastRun != nil {
		t.Errorf("the job set the scope's last run to %v", lastRun)
	}
}
