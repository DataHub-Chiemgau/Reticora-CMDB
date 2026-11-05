package jobs_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
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
	historyAsset := f.ID()
	for _, age := range []time.Duration{4 * 365 * 24 * time.Hour, 365 * 24 * time.Hour} {
		exec(`INSERT INTO ci_change (organization_id, ci_id, change_type, created_at) VALUES ($1, $2, 'update', $3)`, f.OrgA, ci, now.Add(-age))
		exec(`INSERT INTO entity_change (organization_id, entity_type, entity_id, change_type, created_at) VALUES ($1, 'asset', $2, 'updated', $3)`,
			f.OrgA, historyAsset, now.Add(-age))
	}
	// The anchor of the asset before the cut-off (WP-795): its old row goes.
	exec(`INSERT INTO asset_snapshot (organization_id, asset_id, taken_at, reason, state, schema_version)
		VALUES ($1, $2, $3, 'weekly', '{"fields":{}}', 1)`, f.OrgA, historyAsset, now.Add(-3*365*24*time.Hour-24*time.Hour))
	// The anchor of the CI before the cut-off (WP-793): its old row goes.
	exec(`INSERT INTO ci_snapshot (organization_id, ci_id, taken_at, reason, state, schema_version)
		VALUES ($1, $2, $3, 'weekly', '{"fields":{}}', 1)`, f.OrgA, ci, now.Add(-3*365*24*time.Hour-24*time.Hour))
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
	// The seeded rows (change_type updated); the trigger asset_history
	// adds young rows of the fixture asset (WP-794).
	if n := count(`SELECT count(*) FROM entity_change WHERE organization_id = $1 AND change_type = 'updated'`, f.OrgA); n != 1 {
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

// TestRetentionDeletesOldLocationChanges covers NFR-06 for the location
// history (WP-548, open evidence of WP-135): retention.history deletes the
// location_change rows older than RETICORA_RETENTION_HISTORY_DAYS (here
// 1200 days instead of the default 1095, so the setting is what decides),
// younger rows stay, and organization B, which has no expired row, keeps
// all of its rows and gets no retention entry.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRetentionDeletesOldLocationChanges(t *testing.T) {
	f := scopetest.Seed(t, "97")
	bg := context.Background()
	now := time.Now().UTC()
	day := 24 * time.Hour

	t.Setenv("RETICORA_RETENTION_HISTORY_DAYS", "1200")
	cfg := config.Load()
	if err := cfg.Validate(true); err != nil || cfg.RetentionHistoryDays != 1200 {
		t.Fatalf("configured history retention: %d, %v", cfg.RetentionHistoryDays, err)
	}
	r := jobs.NewRetention(f.App, jobs.RetentionFromDays(cfg.RetentionAuditYears, cfg.RetentionHistoryDays,
		cfg.RetentionSoftDeleteDays, cfg.RetentionReviewItemDays))

	// insert writes a move of a location of org that happened age ago and
	// returns the row id.
	insert := func(org string, age time.Duration) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO location_change (organization_id, client_id, site_id, location_id, location_name,
			old_path, new_path, changed_at) VALUES ($1, $2, $3, $4, 'Rack room', 'site.a', 'site.b', $5) RETURNING id::text`,
			org, f.ID(), f.ID(), f.ID(), now.Add(-age)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	expired := insert(f.OrgA, 1300*day)
	keptByTheSetting := insert(f.OrgA, 1150*day) // expired with the default 1095 days
	youngA := insert(f.OrgA, 10*day)
	oldB := insert(f.OrgB, 1150*day)
	youngB := insert(f.OrgB, 10*day)

	if err := r.RunHistory(bg); err != nil {
		t.Fatal(err)
	}
	left := func(org string) []string {
		t.Helper()
		var ids []string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(array_agg(id::text ORDER BY changed_at), '{}') FROM location_change
			WHERE organization_id = $1`, org).Scan(&ids); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if got := left(f.OrgA); len(got) != 2 || got[0] != keptByTheSetting || got[1] != youngA {
		t.Errorf("location_change of A after the run: %v, want %s and %s (deleted: %s)", got, keptByTheSetting, youngA, expired)
	}
	if got := left(f.OrgB); len(got) != 2 || got[0] != oldB || got[1] != youngB {
		t.Errorf("location_change of B after the run: %v, want both rows %s and %s", got, oldB, youngB)
	}
	retentionEntries := func(org string) []map[string]any {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT changes FROM audit_log WHERE organization_id = $1 AND action = $2 ORDER BY seq`,
			org, jobs.RetentionHistory)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := retentionEntries(f.OrgA); len(got) != 1 || got[0]["deleted"] != float64(1) {
		t.Errorf("retention.history entries of A: %v, want one with deleted 1", got)
	}
	if got := retentionEntries(f.OrgB); len(got) != 0 {
		t.Errorf("retention.history entries of B: %v, want none", got)
	}
}

// TestRetentionEndsMergeSnapshotsAndTombstones is the retest of F-251
// (WP-572, CI-12): retention.merge_snapshots empties the snapshot of a merge
// 31 days old (the unmerge period of 30 days passed) and keeps its row (the
// tombstone lasts), deletes the merge row 13 months old (the tombstone of 12
// months passed) and leaves the merge 29 days old with its snapshot. The run
// of A writes one retention entry; organization B, whose merges have not
// expired, keeps its rows unchanged and gets no entry.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRetentionEndsMergeSnapshotsAndTombstones(t *testing.T) {
	f := scopetest.Seed(t, "97")
	bg := context.Background()
	now := time.Now().UTC()
	const snapshot = `{"changed": [{"row": {"email": "merge@example.org"}, "table": "contact"}], "victim": {"name": "v"}, "survivor": {"name": "s"}}`

	// insert records a merge of org at merged with the periods of a merge
	// (snapshot 30 days, tombstone 12 months) and returns its id.
	insert := func(org, client string, merged time.Time) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_merge (organization_id, client_id, survivor_id, victim_id, snapshot,
			merged_at, snapshot_until, tombstone_until)
			VALUES ($1, NULLIF($2, '')::uuid, $3, $4, $5::jsonb, $6, $6::timestamptz + interval '30 days', $6::timestamptz + interval '12 months')
			RETURNING id::text`, org, client, f.ID(), f.ID(), snapshot, merged).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	snapshotExpired := insert(f.OrgA, f.Client1, now.AddDate(0, 0, -31))
	tombstoneExpired := insert(f.OrgA, f.Client1, now.AddDate(0, -13, 0))
	young := insert(f.OrgA, f.Client1, now.AddDate(0, 0, -29))
	youngB := insert(f.OrgB, "", now.AddDate(0, 0, -29))
	recentB := insert(f.OrgB, "", now.AddDate(0, 0, -1))

	r := jobs.NewRetention(f.App, jobs.RetentionFromDays(10, 3*365, 90, 365))
	if err := r.RunMergeSnapshots(bg); err != nil {
		t.Fatal(err)
	}
	// state returns whether the merge row exists and its snapshot.
	state := func(id string) (bool, string) {
		t.Helper()
		var snap string
		err := f.Admin.QueryRow(bg, `SELECT snapshot::text FROM ci_merge WHERE id = $1`, id).Scan(&snap)
		if errors.Is(err, pgx.ErrNoRows) {
			return false, ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return true, snap
	}
	var kept string
	if err := f.Admin.QueryRow(bg, `SELECT $1::jsonb::text`, snapshot).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if exists, snap := state(snapshotExpired); !exists || snap != "{}" {
		t.Errorf("merge 31 days old: row %v, snapshot %s, want the row with an empty snapshot", exists, snap)
	}
	if exists, _ := state(tombstoneExpired); exists {
		t.Error("merge 13 months old: the row with the expired tombstone stays")
	}
	for name, id := range map[string]string{"merge 29 days old": young, "merge of B 29 days old": youngB, "merge of B 1 day old": recentB} {
		if exists, snap := state(id); !exists || snap != kept {
			t.Errorf("%s: row %v, snapshot %s, want it unchanged", name, exists, snap)
		}
	}
	entries := func(org string) []map[string]any {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT changes FROM audit_log WHERE organization_id = $1 AND action = $2 ORDER BY seq`,
			org, jobs.RetentionMergeSnapshots)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if got := entries(f.OrgA); len(got) != 1 || got[0]["deleted"] != float64(2) {
		t.Errorf("retention.merge_snapshots entries of A: %v, want one with deleted 2", got)
	}
	if got := entries(f.OrgB); len(got) != 0 {
		t.Errorf("retention.merge_snapshots entries of B: %v, want none", got)
	}
	if result, err := audit.Verify(bg, f.App, f.OrgA); err != nil || !result.Intact {
		t.Errorf("chain of A after the run: %+v %v", result, err)
	}
}

// fakeBlobs records the deleted keys of the document bucket and fails the
// delete of the keys in fail.
type fakeBlobs struct {
	mu      sync.Mutex
	deleted []string
	fail    map[string]bool
}

func (b *fakeBlobs) Put(context.Context, string, string, io.Reader, int64, string) error { return nil }

func (b *fakeBlobs) Get(context.Context, string, string) (io.ReadCloser, error) {
	return nil, errors.New("not stored")
}

func (b *fakeBlobs) PresignedGetURL(context.Context, string, string) (string, error) { return "", nil }

func (b *fakeBlobs) Delete(_ context.Context, bucket, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if bucket != "reticora-documents" {
		return fmt.Errorf("bucket %s", bucket)
	}
	if b.fail[key] {
		return errors.New("blob store unavailable")
	}
	b.deleted = append(b.deleted, key)
	return nil
}

// TestRetentionPurgesSoftDeletedDocuments covers WP-732 (MGT-01, NFR-06):
// retention.soft_deleted removes a document soft-deleted 91 days ago with
// its versions and links, deletes every version key from the blob store
// (delete markers), lowers organization_usage by the version sizes and
// writes document.purged (source system) and the run entry. A document
// deleted 89 days ago stays. A blob error, or a run without blob store,
// keeps an expired document with content: it counts as kept, and the next
// run with a working store purges it. An expired document without content
// goes without a blob store.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRetentionPurgesSoftDeletedDocuments(t *testing.T) {
	f := scopetest.Seed(t, "97")
	bg := context.Background()
	now := time.Now().UTC()
	user := f.AppUser(t, f.OrgA, "retention-doc")
	asset := f.Asset(t, f.OrgA, f.Client1, "retention-doc-asset")

	scalar := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}
	// doc stores a document of A deleted age days ago with versions of the
	// given sizes and one link to the asset; it returns id and keys.
	doc := func(title string, age int, sizes ...int64) (string, []string) {
		t.Helper()
		id := scalar(`INSERT INTO document (organization_id, title, file_name, mime_type, storage_key, uploaded_by, deleted_at)
			VALUES ($1, $2, $2 || '.pdf', 'application/pdf', '', $3, $4) RETURNING id::text`, f.OrgA, title, user, now.AddDate(0, 0, -age))
		var keys []string
		for i, size := range sizes {
			key := "org/" + f.OrgA + "/documents/" + id + "/" + f.ID()
			if _, err := f.Admin.Exec(bg, `INSERT INTO document_version (organization_id, document_id, version, storage_key, size, mime)
				VALUES ($1, $2, $3, $4, $5, 'application/pdf')`, f.OrgA, id, i+1, key, size); err != nil {
				t.Fatal(err)
			}
			if _, err := f.Admin.Exec(bg, `UPDATE document SET version = $2, storage_key = $3, file_size = $4 WHERE id = $1`,
				id, i+1, key, size); err != nil {
				t.Fatal(err)
			}
			keys = append(keys, key)
		}
		if _, err := f.Admin.Exec(bg, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id)
			VALUES ($1, $2, 'asset', $3)`, f.OrgA, id, asset); err != nil {
			t.Fatal(err)
		}
		return id, keys
	}
	expired, expiredKeys := doc("retention-expired-doc", 91, 100, 200)
	young, _ := doc("retention-young-doc", 89, 50)
	failing, failingKeys := doc("retention-failing-doc", 120, 30)
	empty, _ := doc("retention-empty-doc", 95)
	if _, err := f.Admin.Exec(bg, `INSERT INTO organization_usage (organization_id, storage_bytes) VALUES ($1, 380)
		ON CONFLICT (organization_id) DO UPDATE SET storage_bytes = EXCLUDED.storage_bytes`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	exists := func(id string) bool {
		t.Helper()
		return scalar(`SELECT EXISTS (SELECT 1 FROM document WHERE id = $1)::text`, id) == "true"
	}
	usage := func() string {
		t.Helper()
		return scalar(`SELECT storage_bytes::text FROM organization_usage WHERE organization_id = $1`, f.OrgA)
	}
	purgedEntries := func(id string) int {
		t.Helper()
		n, _ := strconv.Atoi(scalar(`SELECT count(*)::text FROM audit_log WHERE organization_id = $1 AND action = 'document.purged'
			AND resource_id = $2 AND source = 'system'`, f.OrgA, id))
		return n
	}

	// Without a blob store only the document without content goes.
	policy := jobs.RetentionFromDays(10, 3*365, 90, 365)
	if err := jobs.NewRetention(f.App, policy).RunSoftDeleted(bg); err != nil {
		t.Fatal(err)
	}
	if exists(empty) || !exists(expired) || !exists(failing) || !exists(young) {
		t.Errorf("without blob store: empty %v expired %v failing %v young %v, want only the empty one purged",
			exists(empty), exists(expired), exists(failing), exists(young))
	}
	if got := usage(); got != "380" {
		t.Errorf("usage after the run without blob store: %s, want 380", got)
	}

	// With a blob store that fails for one key.
	store := &fakeBlobs{fail: map[string]bool{failingKeys[0]: true}}
	if err := jobs.NewRetention(f.App, policy).WithBlobs(store).RunSoftDeleted(bg); err != nil {
		t.Fatal(err)
	}
	if exists(expired) {
		t.Error("the document deleted 91 days ago stays")
	}
	if !exists(young) {
		t.Error("the document deleted 89 days ago is gone")
	}
	if !exists(failing) {
		t.Error("the document with a failing blob delete is gone")
	}
	for _, key := range expiredKeys {
		if !slices.Contains(store.deleted, key) {
			t.Errorf("blob %s not deleted (deleted: %v)", key, store.deleted)
		}
	}
	if n := scalar(`SELECT ((SELECT count(*) FROM document_version WHERE document_id = $1) + (SELECT count(*) FROM document_link WHERE document_id = $1))::text`,
		expired); n != "0" {
		t.Errorf("versions and links of the purged document: %s", n)
	}
	if got := usage(); got != "80" {
		t.Errorf("usage after the purge: %s, want 80 (380 - 300)", got)
	}
	if purgedEntries(expired) != 1 || purgedEntries(empty) != 1 || purgedEntries(failing) != 0 || purgedEntries(young) != 0 {
		t.Errorf("document.purged entries: expired %d empty %d failing %d young %d",
			purgedEntries(expired), purgedEntries(empty), purgedEntries(failing), purgedEntries(young))
	}
	rows, err := f.Admin.Query(bg, `SELECT changes FROM audit_log WHERE organization_id = $1 AND action = $2 ORDER BY seq`, f.OrgA, jobs.RetentionSoftDeleted)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := pgx.CollectRows(rows, pgx.RowTo[map[string]any])
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0]["deleted"] != float64(1) || runs[1]["deleted"] != float64(1) {
		t.Errorf("retention.soft_deleted entries: %v, want two with deleted 1", runs)
	}

	// The next run with a working store purges the kept document.
	store.fail = nil
	if err = jobs.NewRetention(f.App, policy).WithBlobs(store).RunSoftDeleted(bg); err != nil {
		t.Fatal(err)
	}
	if exists(failing) || !exists(young) {
		t.Errorf("second run: failing %v young %v, want the failing one purged", exists(failing), exists(young))
	}
	if got := usage(); got != "50" {
		t.Errorf("usage after the second purge: %s, want 50", got)
	}
	if result, verr := audit.Verify(bg, f.App, f.OrgA); verr != nil || !result.Intact {
		t.Errorf("chain after the purges: %+v %v", result, verr)
	}
}

// TestRetentionDeletesOldRelationshipChanges covers WP-771 (REL-09,
// NFR-06): retention.history deletes the relationship_change rows created
// before the history period (RETICORA_RETENTION_HISTORY_DAYS, 1095 days),
// as it does for ci_change, per organization; younger rows stay, and
// organization B, which has no expired row, keeps its rows.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRetentionDeletesOldRelationshipChanges(t *testing.T) {
	f := scopetest.Seed(t, "a7")
	bg := context.Background()
	now := time.Now().UTC()
	day := 24 * time.Hour
	r := jobs.NewRetention(f.App, jobs.RetentionFromDays(10, 1095, 90, 365))
	endpoints := map[string][2]string{
		f.OrgA: {f.CI(t, f.OrgA, "", "wp771-ret-a1"), f.CI(t, f.OrgA, "", "wp771-ret-a2")},
		f.OrgB: {f.CI(t, f.OrgB, "", "wp771-ret-b1"), f.CI(t, f.OrgB, "", "wp771-ret-b2")},
	}
	// insert writes a history row of org created age ago and returns its id.
	insert := func(org string, age time.Duration) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO relationship_change (organization_id, relationship_id, source_ci_id, target_ci_id,
				rel_type, change_kind, actor_type, source, observed_at, created_at)
			VALUES ($1, gen_random_uuid(), $2, $3, 'connected_to', 'created', 'system', 'collector', $4, $4) RETURNING id::text`,
			org, endpoints[org][0], endpoints[org][1], now.Add(-age)).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	expired := insert(f.OrgA, 1100*day)
	// Both endpoints of A have an anchor after the expired row (WP-793).
	for _, ciID := range endpoints[f.OrgA] {
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_snapshot (organization_id, ci_id, taken_at, reason, state, schema_version)
			VALUES ($1, $2, $3, 'weekly', '{"fields":{}}', 1)`, f.OrgA, ciID, now.Add(-1098*day)); err != nil {
			t.Fatal(err)
		}
	}
	youngA := insert(f.OrgA, 1000*day)
	youngB := insert(f.OrgB, 10*day)

	if err := r.RunHistory(bg); err != nil {
		t.Fatal(err)
	}
	left := func(org string) []string {
		t.Helper()
		var ids []string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(array_agg(id::text ORDER BY created_at), '{}') FROM relationship_change
			WHERE organization_id = $1`, org).Scan(&ids); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if got := left(f.OrgA); !slices.Equal(got, []string{youngA}) {
		t.Errorf("relationship_change of A after the run: %v, want only %s (deleted: %s)", got, youngA, expired)
	}
	if got := left(f.OrgB); !slices.Equal(got, []string{youngB}) {
		t.Errorf("relationship_change of B after the run: %v, want %s", got, youngB)
	}
}

// TestRetentionKeepsTheHistoryAnchor covers AUD-08 and NFR-06 (WP-793):
// retention.history with the clock moved on (WithClock) keeps per CI its
// youngest ci_snapshot before the cut-off (the anchor) and deletes the
// older ones; it deletes the ci_change rows up to the anchor and keeps
// those after it, also the rows of a merged victim (found by origin_ci_id
// with the anchor of the victim); a CI without an anchor keeps its rows,
// and so does an edge row whose endpoint has none. The state of the CI
// shortly after the cut-off is complete: the anchor and the row after it.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRetentionKeepsTheHistoryAnchor(t *testing.T) {
	f := scopetest.Seed(t, "79")
	bg := context.Background()
	day := 24 * time.Hour
	// The clock of the run: 30 days ahead; the cut-off 1095 days before it.
	runAt := time.Now().UTC().Add(30 * day).Truncate(time.Second)
	cutoff := runAt.Add(-1095 * day)
	r := jobs.NewRetention(f.App, jobs.RetentionFromDays(10, 1095, 90, 365)).WithClock(func() time.Time { return runAt })
	before := func(d time.Duration) time.Time { return cutoff.Add(-d) }

	anchored := f.CI(t, f.OrgA, f.Client1, "wp793-anchored")
	survivor := f.CI(t, f.OrgA, f.Client1, "wp793-survivor")
	victim := f.CI(t, f.OrgA, f.Client1, "wp793-victim")
	unanchored := f.CI(t, f.OrgA, f.Client1, "wp793-unanchored")
	// change writes a ci_change row of ciID (origin: the victim it came
	// from, empty for none) setting name at at and returns its id.
	change := func(ciID, origin, kind, name string, at time.Time) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_change (organization_id, ci_id, origin_ci_id, change_type, field_name, new_value, created_at)
			VALUES ($1, $2, NULLIF($3, '')::uuid, $4, CASE WHEN $4 = 'update' THEN 'name' END, $5::jsonb, $6) RETURNING id::text`,
			f.OrgA, ciID, origin, kind, name, at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	snapshot := func(ciID, name string, at time.Time) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_snapshot (organization_id, ci_id, taken_at, reason, state, schema_version)
			VALUES ($1, $2, $3, 'weekly', jsonb_build_object('fields', jsonb_build_object('name', $4::text), 'deleted', false), 1)
			RETURNING id::text`, f.OrgA, ciID, at, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	// The anchored CI: created and renamed before its anchor, renamed again
	// between the anchor and the cut-off, a snapshot older than the anchor
	// and one after the cut-off.
	created := change(anchored, "", "create", `{"name": "wp793-a0"}`, before(60*day))
	renamed := change(anchored, "", "update", `"wp793-a1"`, before(40*day))
	oldSnapshot := snapshot(anchored, "wp793-a0", before(50*day))
	anchor := snapshot(anchored, "wp793-a1", before(20*day))
	late := change(anchored, "", "update", `"wp793-a2"`, before(10*day))
	young := snapshot(anchored, "wp793-a2", cutoff.Add(5*day))
	// The victim's rows hang on the survivor (origin_ci_id); the victim
	// has an anchor between them, the survivor none.
	victimOld := change(survivor, victim, "update", `"wp793-v1"`, before(40*day))
	victimLate := change(survivor, victim, "update", `"wp793-v2"`, before(10*day))
	victimAnchor := snapshot(victim, "wp793-v1", before(20*day))
	survivorOwn := change(survivor, "", "update", `"wp793-s1"`, before(40*day))
	// Without an anchor.
	unanchoredOld := change(unanchored, "", "update", `"wp793-u1"`, before(40*day))
	// Edge rows: both endpoints anchored (goes), one endpoint without an
	// anchor (stays).
	edge := func(source, target string, at time.Time) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO relationship_change (organization_id, relationship_id, source_ci_id, target_ci_id,
				rel_type, change_kind, actor_type, source, observed_at, created_at)
			VALUES ($1, gen_random_uuid(), $2, $3, 'connected_to', 'created', 'system', 'collector', $4, $4) RETURNING id::text`,
			f.OrgA, source, target, at).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	edgeAnchored := edge(anchored, victim, before(40*day))
	edgeOpen := edge(anchored, unanchored, before(40*day))
	edgeLate := edge(anchored, victim, before(10*day))

	if err := r.RunHistory(bg); err != nil {
		t.Fatal(err)
	}
	ids := func(sql string, args ...any) []string {
		t.Helper()
		var out []string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(array_agg(id::text ORDER BY id), '{}') FROM (`+sql+`) q`, args...).Scan(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	sorted := func(v ...string) []string {
		slices.Sort(v)
		return v
	}
	if got, want := ids(`SELECT id FROM ci_change WHERE organization_id = $1`, f.OrgA),
		sorted(late, victimLate, survivorOwn, unanchoredOld); !slices.Equal(got, want) {
		t.Errorf("ci_change after the run: %v, want %v (deleted: %s %s %s)", got, want, created, renamed, victimOld)
	}
	if got, want := ids(`SELECT id FROM ci_snapshot WHERE organization_id = $1`, f.OrgA), sorted(anchor, young, victimAnchor); !slices.Equal(got, want) {
		t.Errorf("ci_snapshot after the run: %v, want %v (deleted: %s)", got, want, oldSnapshot)
	}
	if got, want := ids(`SELECT id FROM relationship_change WHERE organization_id = $1`, f.OrgA), sorted(edgeOpen, edgeLate); !slices.Equal(got, want) {
		t.Errorf("relationship_change after the run: %v, want %v (deleted: %s)", got, want, edgeAnchored)
	}

	// The state shortly after the cut-off: the anchor and the row after it.
	state, err := history.NewPGRepository(f.App).StateAt(f.OrgCtx(f.OrgA), f.OrgA,
		&history.StateRequest{EntityType: "ci", EntityID: anchored, At: cutoff.Add(time.Hour), Reveal: true})
	if err != nil {
		t.Fatal(err)
	}
	if state.Fields["name"] != "wp793-a2" || state.BasedOnSnapshotAt == nil || !state.BasedOnSnapshotAt.Equal(before(20*day)) {
		t.Errorf("state after the cut-off: %v based on %v, want wp793-a2 based on the anchor %s", state.Fields, state.BasedOnSnapshotAt, before(20*day))
	}
}
