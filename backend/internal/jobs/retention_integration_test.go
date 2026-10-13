package jobs_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// TestRetentionRunsAgainstPostgres covers NFR-06 and AUD-09 (WP-135):
//   - audit_log: entries older than ten years are removed, the newest entry
//     always stays, an audit.retention entry anchors the rest and the chain
//     verifies; the database refuses to delete younger entries and any
//     entry outside a retention run;
//   - CI, entity history older than three years, CIs soft-deleted longer
//     than 90 days (references are released, CI-05) and review items resolved more
//     than a year ago are deleted, younger data stays;
//   - each run that deleted something writes its own audit entry.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRetentionRunsAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "97")
	bg := context.Background()
	now := time.Now().UTC()
	inOrg := func(org string, fn func(ctx context.Context, tx pgx.Tx) error) error {
		scope := database.OrgWideScope(org, "")
		return database.WithTenant(bg, f.App, &scope, fn)
	}
	record := func(org string, at time.Time, action string) {
		t.Helper()
		if err := inOrg(org, func(ctx context.Context, tx pgx.Tx) error {
			_, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: org, Timestamp: at, Action: action, ResourceType: "ci"})
			return err
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := range 3 {
		record(f.OrgA, now.AddDate(-11, 0, i), "old")
	}
	record(f.OrgA, now.Add(-time.Hour), "recent")
	record(f.OrgA, now, "recent")
	record(f.OrgB, now.AddDate(-12, 0, 0), "only")

	// The database guard.
	for name, sql := range map[string]string{
		"a young entry within a retention run": `SELECT set_config('app.audit_retention', 'on', true);
			DELETE FROM audit_log WHERE organization_id = current_setting('app.org_id')::uuid AND action = 'recent'`,
		"an old entry outside a retention run": `DELETE FROM audit_log WHERE organization_id = current_setting('app.org_id')::uuid AND action = 'old'`,
	} {
		if err := inOrg(f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, sql)
			return err
		}); err == nil {
			t.Errorf("deleted %s", name)
		}
	}

	policy := jobs.RetentionFromDays(10, 3*365, 90, 365)
	r := jobs.NewRetention(f.App, policy)
	if err := r.RunAuditLog(bg); err != nil {
		t.Fatal(err)
	}
	var actions []string
	if err := f.Admin.QueryRow(bg, `SELECT array_agg(action ORDER BY seq) FROM audit_log WHERE organization_id = $1`, f.OrgA).Scan(&actions); err != nil {
		t.Fatal(err)
	}
	if len(actions) != 3 || actions[0] != "recent" || actions[2] != audit.RetentionAction {
		t.Fatalf("audit log after the run: %v", actions)
	}
	var minSeq int
	var changes map[string]any
	if err := f.Admin.QueryRow(bg, `SELECT (SELECT min(seq) FROM audit_log WHERE organization_id = $1),
		(SELECT changes FROM audit_log WHERE organization_id = $1 AND action = $2)`, f.OrgA, audit.RetentionAction).Scan(&minSeq, &changes); err != nil {
		t.Fatal(err)
	}
	if minSeq != 4 || changes["deleted"] != float64(3) || changes["deleted_through_seq"] != float64(3) || changes["anchor_hash"] == "" {
		t.Errorf("retention entry: min seq %d, changes %v", minSeq, changes)
	}
	for _, org := range []string{f.OrgA, f.OrgB} {
		if result, err := audit.Verify(bg, f.App, org); err != nil || !result.Intact {
			t.Errorf("chain of %s after the run: %+v %v", org, result, err)
		}
	}
	var onlyB int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1`, f.OrgB).Scan(&onlyB); err != nil || onlyB != 1 {
		t.Errorf("organization B keeps its single entry: %d %v", onlyB, err)
	}
	// A second run finds nothing to remove; a removed entry without its
	// anchor would be a gap.
	if err := r.RunAuditLog(bg); err != nil {
		t.Fatal(err)
	}
	var retentionEntries int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = $2`, f.OrgA, audit.RetentionAction).Scan(&retentionEntries); err != nil || retentionEntries != 1 {
		t.Errorf("retention entries after a second run: %d %v", retentionEntries, err)
	}

	// History, soft-deleted CIs and review items.
	ci := f.CI(t, f.OrgA, f.Client1, "retention-ci")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	for _, age := range []time.Duration{4 * 365 * 24 * time.Hour, 365 * 24 * time.Hour} {
		exec(`INSERT INTO ci_change (organization_id, ci_id, change_type, created_at) VALUES ($1, $2, 'update', $3)`, f.OrgA, ci, now.Add(-age))
		exec(`INSERT INTO entity_change (organization_id, entity_type, entity_id, change_type, created_at) VALUES ($1, 'asset', $2, 'updated', $3)`,
			f.OrgA, f.ID(), now.Add(-age))
	}
	expired := f.CI(t, f.OrgA, f.Client1, "retention-expired")
	young := f.CI(t, f.OrgA, f.Client1, "retention-young")
	referenced := f.CI(t, f.OrgA, f.Client1, "retention-referenced")
	exec(`UPDATE ci SET deleted_at = $2 WHERE id = ANY($1::uuid[])`, []string{expired, referenced}, now.AddDate(0, 0, -100))
	exec(`UPDATE ci SET deleted_at = $2 WHERE id = $1`, young, now.AddDate(0, 0, -10))
	asset := f.Asset(t, f.OrgA, f.Client1, "retention-asset")
	exec(`UPDATE asset SET ci_id = $2 WHERE id = $1`, asset, referenced)
	for _, resolved := range []any{now.AddDate(0, 0, -400), now.AddDate(0, 0, -10), nil} {
		status := "resolved"
		if resolved == nil {
			status = "open"
		}
		exec(`INSERT INTO review_item (organization_id, kind, status, resolved_at) VALUES ($1, 'probable_duplicate', $2, $3)`, f.OrgA, status, resolved)
	}

	for _, run := range []func(context.Context) error{r.RunHistory, r.RunSoftDeleted, r.RunReviewItems} {
		if err := run(bg); err != nil {
			t.Fatal(err)
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
	if n := count(`SELECT count(*) FROM ci_change WHERE organization_id = $1 AND created_at < $2`, f.OrgA, now.AddDate(-3, 0, 0)); n != 0 {
		t.Errorf("old ci_change rows: %d", n)
	}
	if n := count(`SELECT count(*) FROM ci_change WHERE ci_id = $1`, ci); n < 1 {
		t.Errorf("the young ci_change row is gone")
	}
	if n := count(`SELECT count(*) FROM entity_change WHERE organization_id = $1`, f.OrgA); n != 1 {
		t.Errorf("entity_change rows: %d, want the young one", n)
	}
	// CI-05: the hard delete also removes a CI an asset still pointed to;
	// the asset stays and loses the link.
	if n := count(`SELECT count(*) FROM ci WHERE id = ANY($1::uuid[])`, []string{expired, young, referenced}); n != 1 {
		t.Errorf("CIs after the soft-delete run: %d, want the young one", n)
	}
	if n := count(`SELECT count(*) FROM asset WHERE id = $1 AND ci_id IS NULL`, asset); n != 1 {
		t.Errorf("the asset of the referenced CI is gone or still linked")
	}
	if n := count(`SELECT count(*) FROM ci WHERE id = $1`, expired); n != 0 {
		t.Error("the expired CI stays")
	}
	if n := count(`SELECT count(*) FROM review_item WHERE organization_id = $1`, f.OrgA); n != 2 {
		t.Errorf("review items: %d, want the young and the open one", n)
	}
	var kinds []string
	if err := f.Admin.QueryRow(bg, `SELECT array_agg(action ORDER BY seq) FROM audit_log WHERE organization_id = $1 AND resource_type = 'retention'`,
		f.OrgA).Scan(&kinds); err != nil {
		t.Fatal(err)
	}
	if len(kinds) != 3 || kinds[0] != jobs.RetentionHistory || kinds[1] != jobs.RetentionSoftDeleted || kinds[2] != jobs.RetentionReviewItems {
		t.Errorf("self-audit of the runs: %v", kinds)
	}
	if result, err := audit.Verify(bg, f.App, f.OrgA); err != nil || !result.Intact {
		t.Errorf("chain after all runs: %+v %v", result, err)
	}
}
