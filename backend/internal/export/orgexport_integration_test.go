package export_test

// WP-1149 (TLC-05 part): the full export of an organization as the job
// org.export — the catalog of all tables with organization_id, JSONL per
// table in one REPEATABLE READ snapshot with a manifest, personal data
// revealed (E-165), secret columns left out, resume after a crash and
// removal after a cancel. PostgreSQL integration through the application
// pool (row level security as in production).
//
// Retest: cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestOrgExport' ./internal/export/

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// recordingStore records the keys stored and deleted.
type recordingStore struct {
	blob.Store
	mu      sync.Mutex
	put     []string
	deleted []string
	// failPut, when set, refuses a Put of key (the process ends there);
	// last is the key of the previous Put.
	failPut func(key, last string) bool
}

func (s *recordingStore) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	s.mu.Lock()
	last := ""
	if len(s.put) > 0 {
		last = s.put[len(s.put)-1]
	}
	if s.failPut != nil && s.failPut(key, last) {
		s.mu.Unlock()
		return errCrash
	}
	s.put = append(s.put, key)
	s.mu.Unlock()
	return s.Store.Put(ctx, bucket, key, r, size, contentType)
}

func (s *recordingStore) Delete(ctx context.Context, bucket, key string) error {
	s.mu.Lock()
	s.deleted = append(s.deleted, key)
	s.mu.Unlock()
	return s.Store.Delete(ctx, bucket, key)
}

// recordingProgress records the reported progress of the export.
type recordingProgress struct {
	mu  sync.Mutex
	pct []int
	ids []string
}

func (p *recordingProgress) Progress(_ context.Context, orgID, id string, percent int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.pct = append(p.pct, percent)
	p.ids = append(p.ids, orgID+"/"+id)
	return nil
}

// orgExportEnv is a seeded organization A with P2 data (CIs, an asset, a
// user, a contact in the vault, a CI change with a sealed e-mail, secrets
// of a webhook, a credential and an API key) and a neighbour B.
type orgExportEnv struct {
	f     *scopetest.Fixture
	store *recordingStore
	email string
	// foreignEmail is the e-mail of the contact of organization B.
	foreignEmail string
}

const (
	orgExportWebhookSecret = "wp1149-webhook-secret-value"
	orgExportPrevSecret    = "wp1149-previous-secret-value"
	orgExportHeaderToken   = "wp1149-header-token-value"
)

func newOrgExportEnv(t *testing.T) *orgExportEnv {
	t.Helper()
	f := scopetest.Seed(t, "1c")
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	vault := pii.NewVault(f.App, enc)
	pii.SetDefault(vault)
	t.Cleanup(func() { pii.SetDefault(nil) })
	env := &orgExportEnv{f: f, store: &recordingStore{Store: blob.NewFileStore(t.TempDir())},
		email: "wp1149.person@example.test", foreignEmail: "wp1149.neighbour@example.test"}

	f.CI(t, f.OrgA, f.Client1, "wp1149-ci-1")
	ciID := f.CI(t, f.OrgA, "", "wp1149-ci-2")
	f.Asset(t, f.OrgA, f.Client1, "WP1149-A1")
	userID := f.AppUser(t, f.OrgA, "wp1149-user")
	f.CI(t, f.OrgB, "", "wp1149-ci-b")

	contact := func(orgID, email string) (subject, value string) {
		t.Helper()
		scope := database.OrgWideScope(orgID, "")
		if terr := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var cerr error
			if subject, cerr = vault.CreateTx(ctx, tx, orgID, pii.KindContact, pii.Data{"email": email, "name": "WP1149 Person"}); cerr != nil {
				return cerr
			}
			if value, cerr = vault.CreateTx(ctx, tx, orgID, pii.KindValue, pii.Data{"v": email}); cerr != nil {
				return cerr
			}
			_, cerr = tx.Exec(ctx, `INSERT INTO contact (id, organization_id, display_name, pii_subject_id) VALUES (gen_random_uuid(), $1, 'WP1149', $2)`,
				orgID, subject)
			return cerr
		}); terr != nil {
			t.Fatalf("seed contact: %v", terr)
		}
		return subject, value
	}
	// metric_sample has no foreign key to the organization: the fixture
	// cleanup does not reach it.
	clearSamples := func() {
		if _, cerr := f.Admin.Exec(bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB}); cerr != nil {
			t.Errorf("remove metric samples: %v", cerr)
		}
	}
	clearSamples()
	t.Cleanup(clearSamples)
	_, value := contact(f.OrgA, env.email)
	contact(f.OrgB, env.foreignEmail)
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ci_change (id, organization_id, ci_id, change_type, field_name, old_value, new_value, change_kind)
			VALUES (gen_random_uuid(), $1, $2, 'update', 'owner', 'null', jsonb_build_object('email', jsonb_build_object('$pii', $3::text)), 'updated')`,
			[]any{f.OrgA, ciID, value}},
		{`INSERT INTO webhook_subscription (id, organization_id, name, url, secret, previous_secret, previous_secret_expires_at, events, headers)
			VALUES (gen_random_uuid(), $1, 'wp1149', 'https://hooks.example.test/x', $2, $3, now() + interval '1 day', '{ci.created}', jsonb_build_object('Authorization', $4::text))`,
			[]any{f.OrgA, orgExportWebhookSecret, orgExportPrevSecret, orgExportHeaderToken}},
		// metric_sample has no primary key and is read through its view
		// metric_sample_v (migration 000071).
		{`INSERT INTO metric_sample (time, organization_id, ci_id, metric_name, value) VALUES (now(), $1, $2, 'cpu', 0.5), (now(), $1, $2, 'mem', 0.7)`,
			[]any{f.OrgA, ciID}},
		{`INSERT INTO credential (id, organization_id, name, kind, ciphertext, key_version, scope)
			VALUES (gen_random_uuid(), $1, 'wp1149', 'snmp_v2c', '\xdeadbeef'::bytea, 1, 'org')`, []any{f.OrgA}},
		{`INSERT INTO api_key (id, organization_id, name, key_hash, key_prefix, permissions, created_by)
			VALUES (gen_random_uuid(), $1, 'wp1149', 'wp1149-api-key-hash', 'rtc_wp1', '{ci:read}', $2)`, []any{f.OrgA, userID}},
	} {
		if _, serr := f.Admin.Exec(bg, s.sql, s.args...); serr != nil {
			t.Fatalf("seed %q: %v", s.sql, serr)
		}
	}
	return env
}

// job returns a new job org.export of organization A.
func (env *orgExportEnv) job(t *testing.T) *jobs.Job {
	t.Helper()
	var id string
	if err := env.f.Admin.QueryRow(context.Background(), `SELECT gen_random_uuid()::text`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return &jobs.Job{ID: id, OrganizationID: env.f.OrgA, Kind: export.OrgExportJobKind, CreatedBy: env.f.User}
}

// run runs the job to its end and returns the manifest.
func (env *orgExportEnv) run(t *testing.T, e *export.OrgExporter, job *jobs.Job) *export.OrgExportManifest {
	t.Helper()
	res, err := e.RunJob(context.Background(), job)
	if err != nil {
		t.Fatalf("org export: %v", err)
	}
	m, ok := res.(*export.OrgExportManifest)
	if !ok || m.CompletedAt == nil {
		t.Fatalf("org export result = %#v", res)
	}
	return m
}

// read returns the content of an object of the export.
func (env *orgExportEnv) read(t *testing.T, key string) []byte {
	t.Helper()
	rc, err := env.store.Get(context.Background(), export.ExportBucket, key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// lines returns the lines of every part of a table, checking bytes and
// SHA-256 of each part against the manifest.
func (env *orgExportEnv) lines(t *testing.T, table *export.OrgExportTable) []string {
	t.Helper()
	var out []string
	for _, p := range table.Parts {
		raw := env.read(t, p.Key)
		sum := sha256.Sum256(raw)
		if int64(len(raw)) != p.Bytes || hex.EncodeToString(sum[:]) != p.SHA256 {
			t.Errorf("%s: %d bytes %x, manifest %d %s", p.Key, len(raw), sum, p.Bytes, p.SHA256)
		}
		sc := bufio.NewScanner(bytes.NewReader(raw))
		sc.Buffer(make([]byte, 1<<20), 1<<26)
		for sc.Scan() {
			out = append(out, sc.Text())
		}
	}
	return out
}

func tableOf(m *export.OrgExportManifest, name string) *export.OrgExportTable {
	for i := range m.Tables {
		if m.Tables[i].Name == name {
			return &m.Tables[i]
		}
	}
	return nil
}

// adminCatalog lists the tables of public with organization_id through the
// maintenance connection q, independently of export.OrgTables.
func adminCatalog(t *testing.T, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) []string {
	t.Helper()
	rows, err := q.Query(context.Background(), `
		SELECT DISTINCT c.relname::text FROM pg_class c
		  JOIN pg_namespace n ON n.oid = c.relnamespace
		  JOIN pg_attribute a ON a.attrelid = c.oid AND a.attname = 'organization_id' AND NOT a.attisdropped
		 WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

// appTables runs export.OrgTables in a tenant snapshot of organization A.
func appTables(t *testing.T, f *scopetest.Fixture) []export.OrgTable {
	t.Helper()
	var tables []export.OrgTable
	scope := database.OrgWideScope(f.OrgA, "")
	if err := database.WithTenantSnapshot(context.Background(), f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		tables, err = export.OrgTables(ctx, tx)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return tables
}

// TestOrgExportCatalogComplete: every table of the catalog with
// organization_id is exported or excluded with a reason (AK1); the
// organization row is part of the export, the operator tables are not. A
// table the catalog has and the export lacks fails the check.
func TestOrgExportCatalogComplete(t *testing.T) {
	f := scopetest.Seed(t, "1c")
	bg := context.Background()
	tables := appTables(t, f)
	catalog := adminCatalog(t, f.Admin)
	if err := export.CheckCatalogComplete(catalog, tables); err != nil {
		t.Fatal(err)
	}
	names := make([]string, len(tables))
	for i := range tables {
		names[i] = tables[i].Name
	}
	for _, want := range []string{"organization", "ci", "contact", "audit_log", "event_outbox", "metric_sample"} {
		if !slices.Contains(names, want) {
			t.Errorf("table %s missing from the export", want)
		}
	}
	for name, reason := range export.ExcludedOrgTables() {
		if slices.Contains(names, name) || strings.TrimSpace(reason) == "" {
			t.Errorf("excluded table %s is exported or has no reason (%q)", name, reason)
		}
	}
	for _, operator := range []string{"operator_audit", "signup_request", "platform_secret"} {
		if slices.Contains(names, operator) {
			t.Errorf("operator table %s in the export", operator)
		}
	}
	for i := range tables {
		if tables[i].Name == "organization" && tables[i].OrgColumn != "id" {
			t.Errorf("organization is selected by %s, want id", tables[i].OrgColumn)
		}
	}

	// Negative: a new table with organization_id the export does not know
	// fails the check. The catalog read in the transaction that creates it
	// sees it; OrgTables reads it as well (the catalog is not a list).
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, cerr := tx.Exec(bg, `CREATE TABLE wp1149_probe (id uuid PRIMARY KEY, organization_id uuid NOT NULL)`); cerr != nil {
		t.Fatal(cerr)
	}
	withProbe := adminCatalog(t, tx)
	err = export.CheckCatalogComplete(withProbe, tables)
	if err == nil || !strings.Contains(err.Error(), "wp1149_probe") {
		t.Errorf("check with an unknown table = %v, want it named", err)
	}
	inTx, err := export.OrgTables(bg, tx)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := export.CheckCatalogComplete(withProbe, inTx); cerr != nil {
		t.Errorf("OrgTables does not pick up a new table: %v", cerr)
	}
}

// TestOrgExportRowCountsMatch: per table the exported rows equal COUNT(*)
// of the organization, pii_subject included; the manifest lies next to the
// parts and in the job result, its parts match bytes and SHA-256.
func TestOrgExportRowCountsMatch(t *testing.T) {
	env := newOrgExportEnv(t)
	bg := context.Background()
	job := env.job(t)
	progress := &recordingProgress{}
	m := env.run(t, export.NewOrgExporter(env.f.App, env.store, progress), job)

	// The progress rises with every completed table and stays below 100
	// until the job succeeds (AK5).
	if len(progress.pct) != len(m.Tables)+1 || progress.pct[0] != 0 {
		t.Errorf("progress reported %d times %v, want %d from 0", len(progress.pct), progress.pct, len(m.Tables)+1)
	}
	for i, p := range progress.pct {
		if p > 99 || (i > 0 && p < progress.pct[i-1]) {
			t.Errorf("progress %v falls or reaches 100 at %d", progress.pct, i)
			break
		}
	}
	if n := len(progress.pct); n < 2 || progress.pct[n-1] <= progress.pct[0] || progress.pct[n-1] < 90 {
		t.Errorf("progress does not rise: %v", progress.pct)
	}
	for _, id := range progress.ids {
		if id != env.f.OrgA+"/"+job.ID {
			t.Errorf("progress of %s, want job %s", id, job.ID)
		}
	}

	var stored export.OrgExportManifest
	if err := json.Unmarshal(env.read(t, export.OrgExportManifestKey(env.f.OrgA, job.ID)), &stored); err != nil {
		t.Fatal(err)
	}
	if len(stored.Tables) != len(m.Tables) || stored.CompletedAt == nil || stored.Pending != nil {
		t.Errorf("stored manifest has %d tables (completed %v, pending %v), result %d", len(stored.Tables), stored.CompletedAt, stored.Pending, len(m.Tables))
	}
	tables := appTables(t, env.f)
	if len(m.Tables) != len(tables)+1 {
		t.Errorf("manifest has %d tables, want %d and pii_subject", len(m.Tables), len(tables))
	}
	nonEmpty := 0
	for i := range m.Tables {
		tb := &m.Tables[i]
		col := "organization_id"
		if tb.Name == "organization" {
			col = "id"
		}
		var want int64
		if err := env.f.Admin.QueryRow(bg, `SELECT count(*) FROM `+pgx.Identifier{tb.Name}.Sanitize()+` WHERE `+col+` = $1`, env.f.OrgA).Scan(&want); err != nil {
			t.Fatal(err)
		}
		lines := env.lines(t, tb)
		if tb.Rows != want || int64(len(lines)) != want {
			t.Errorf("%s: manifest %d rows, parts %d lines, COUNT(*) %d", tb.Name, tb.Rows, len(lines), want)
		}
		if want > 0 {
			nonEmpty++
		}
		if tb.Snapshot == "" || tb.SnapshotAt.IsZero() {
			t.Errorf("%s: no snapshot in the manifest", tb.Name)
		}
		for _, p := range tb.Parts {
			if !strings.HasPrefix(p.Key, export.OrgExportPrefix(env.f.OrgA, job.ID)+"/") {
				t.Errorf("%s: part %s outside the job prefix", tb.Name, p.Key)
			}
		}
	}
	if nonEmpty < 8 {
		t.Errorf("only %d tables with rows; the seed did not reach the export", nonEmpty)
	}
	if ms := tableOf(m, "metric_sample"); ms == nil || ms.Rows != 2 {
		t.Errorf("metric_sample = %+v, want the 2 samples through the view", ms)
	}
	if ci := tableOf(m, "ci"); ci == nil || len(ci.Parts) != 1 || !strings.HasSuffix(ci.Parts[0].Key, "/tables/ci/part-0001.jsonl") {
		t.Errorf("ci parts = %+v", ci)
	}

	// Parts are split at the part limit; the rows stay complete.
	small := env.run(t, export.NewOrgExporter(env.f.App, env.store, nil).WithLimits(2, 200), env.job(t))
	if ci := tableOf(small, "ci"); ci == nil || ci.Rows != 2 || len(ci.Parts) != 2 || len(env.lines(t, ci)) != 2 {
		t.Errorf("ci with a part limit of 200 bytes = %+v", ci)
	}
	// The cursor of a table without primary key fetches in batches as well.
	if ms := tableOf(small, "metric_sample"); ms == nil || ms.Rows != 2 || len(env.lines(t, ms)) != 2 {
		t.Errorf("metric_sample in batches of 2 = %+v", ms)
	}
}

// TestOrgExportNoForeignRows: no exported line belongs to the neighbour.
func TestOrgExportNoForeignRows(t *testing.T) {
	env := newOrgExportEnv(t)
	m := env.run(t, export.NewOrgExporter(env.f.App, env.store, nil), env.job(t))
	for i := range m.Tables {
		tb := &m.Tables[i]
		for _, line := range env.lines(t, tb) {
			if strings.Contains(line, env.f.OrgB) || strings.Contains(line, env.foreignEmail) {
				t.Errorf("%s: line of organization B: %s", tb.Name, line)
			}
			if tb.Name == "pii_subject" {
				continue
			}
			var row map[string]any
			if err := json.Unmarshal([]byte(line), &row); err != nil {
				t.Fatalf("%s: %v", tb.Name, err)
			}
			col := "organization_id"
			if tb.Name == "organization" {
				col = "id"
			}
			if row[col] != env.f.OrgA {
				t.Errorf("%s: %s = %v, want %s", tb.Name, col, row[col], env.f.OrgA)
			}
		}
	}
}

// TestOrgExportRevealsPII: the e-mail of the contact is plaintext in the
// CI change and in pii_subject.jsonl, no part holds a $pii reference, the
// data key never appears (AK3, AK4, E-165).
func TestOrgExportRevealsPII(t *testing.T) {
	env := newOrgExportEnv(t)
	job := env.job(t)
	m := env.run(t, export.NewOrgExporter(env.f.App, env.store, nil), job)
	for i := range m.Tables {
		tb := &m.Tables[i]
		if tb.Name == "org_dek" {
			t.Error("org_dek in the export")
		}
		for _, line := range env.lines(t, tb) {
			if strings.Contains(line, `"$pii"`) {
				t.Errorf("%s: surrogate reference left: %s", tb.Name, line)
			}
		}
	}
	for _, key := range env.store.put {
		if strings.Contains(key, "org_dek") {
			t.Errorf("object %s", key)
		}
	}
	changes := tableOf(m, "ci_change")
	if changes == nil || len(changes.Parts) == 0 || !strings.Contains(strings.Join(env.lines(t, changes), "\n"), `"email":"`+env.email+`"`) {
		t.Errorf("ci_change without the revealed e-mail: %+v", changes)
	}
	subjects := tableOf(m, "pii_subject")
	if subjects == nil || len(subjects.Parts) == 0 || subjects.Parts[0].Key != export.OrgExportPrefix(env.f.OrgA, job.ID)+"/pii_subject.jsonl" {
		t.Fatalf("pii_subject = %+v", subjects)
	}
	var contact, value bool
	for _, line := range env.lines(t, subjects) {
		var s struct {
			SurrogateID string            `json:"surrogate_id"`
			Kind        string            `json:"kind"`
			ParentID    *string           `json:"parent_id"`
			Data        map[string]string `json:"data"`
		}
		if err := json.Unmarshal([]byte(line), &s); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(line, "encrypted_payload") || strings.Contains(line, "search_tokens") || s.SurrogateID == "" {
			t.Errorf("pii_subject line: %s", line)
		}
		contact = contact || (s.Kind == pii.KindContact && s.Data["email"] == env.email)
		value = value || (s.Kind == pii.KindValue && s.Data["v"] == env.email)
	}
	if !contact || !value {
		t.Errorf("pii_subject.jsonl: contact %v, value %v", contact, value)
	}
	// The contact row keeps its surrogate; its data is in pii_subject.jsonl.
	if c := tableOf(m, "contact"); c == nil || !strings.Contains(strings.Join(env.lines(t, c), "\n"), `"pii_subject_id"`) {
		t.Errorf("contact = %+v", c)
	}
}

// TestOrgExportOmitsSecretColumns: secret columns are not selected (AK2):
// the naming rule and orgExportSecretColumns cover every column of the
// catalog whose name suggests a secret, and none of the seeded secrets is
// in a part.
func TestOrgExportOmitsSecretColumns(t *testing.T) {
	env := newOrgExportEnv(t)
	suspicious := regexp.MustCompile(`(secret|password|passphrase|private_key|cipher|(^|_)dek$|_hash$|_enc$|^encrypted_|^headers$)`)
	// harmless are columns whose name matches suspicious but hold no secret.
	harmless := map[string]string{
		"webhook_subscription.previous_secret_expires_at": "the end of the rotation window, a timestamp",
	}
	for _, tb := range appTables(t, env.f) {
		for _, c := range tb.Columns {
			if _, ok := harmless[tb.Name+"."+c]; ok {
				continue
			}
			if suspicious.MatchString(c) || export.IsSecretColumn(tb.Name, c) {
				t.Errorf("%s.%s is exported", tb.Name, c)
			}
		}
		for _, c := range tb.Omitted {
			if !export.IsSecretColumn(tb.Name, c) {
				t.Errorf("%s.%s omitted without rule", tb.Name, c)
			}
		}
	}
	for table, column := range map[string]string{"credential": "ciphertext", "webhook_subscription": "secret", "api_key": "key_hash",
		"notification_channel": "config_ciphertext", "collector_enrollment_code": "code_hash"} {
		if !export.IsSecretColumn(table, column) {
			t.Errorf("%s.%s is no secret column", table, column)
		}
	}

	m := env.run(t, export.NewOrgExporter(env.f.App, env.store, nil), env.job(t))
	for i := range m.Tables {
		for _, line := range env.lines(t, &m.Tables[i]) {
			for _, secret := range []string{orgExportWebhookSecret, orgExportPrevSecret, orgExportHeaderToken, "wp1149-api-key-hash", `"ciphertext"`, "deadbeef"} {
				if strings.Contains(line, secret) {
					t.Errorf("%s: secret %q in %s", m.Tables[i].Name, secret, line)
				}
			}
		}
	}
	hook := tableOf(m, "webhook_subscription")
	if hook == nil || hook.Rows != 1 || !slices.Contains(hook.OmittedColumns, "secret") || !slices.Contains(hook.OmittedColumns, "previous_secret") {
		t.Errorf("webhook_subscription = %+v", hook)
	}
}

// TestOrgExportSnapshotIsRepeatableRead: a CI inserted while the export
// reads the table ci does not change its row count; the snapshot is one.
func TestOrgExportSnapshotIsRepeatableRead(t *testing.T) {
	env := newOrgExportEnv(t)
	inserted := false
	e := export.NewOrgExporter(env.f.App, env.store, nil).WithLimits(1, 0).WithStepHook(func(_ context.Context, table string) error {
		if table == "ci" && !inserted {
			inserted = true
			env.f.CI(t, env.f.OrgA, "", "wp1149-ci-during-export")
		}
		return nil
	})
	m := env.run(t, e, env.job(t))
	if !inserted {
		t.Fatal("the hook did not run inside the table ci")
	}
	ci := tableOf(m, "ci")
	if ci == nil || ci.Rows != 2 {
		t.Errorf("ci rows = %+v, want the 2 of the snapshot", ci)
	}
	var now int
	if err := env.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ci WHERE organization_id = $1`, env.f.OrgA).Scan(&now); err != nil || now != 3 {
		t.Fatalf("ci rows after the export = %d %v", now, err)
	}
	snapshots := map[string]bool{}
	for i := range m.Tables {
		snapshots[m.Tables[i].Snapshot] = true
	}
	if len(snapshots) != 1 {
		t.Errorf("one attempt read %d snapshots", len(snapshots))
	}
}

var errCrash = errors.New("process ended")

// TestOrgExportResumesAfterRestart: after a crash the next attempt keeps
// the tables the manifest lists as complete and exports the others in a
// new snapshot (AK6); every table is in the manifest once.
func TestOrgExportResumesAfterRestart(t *testing.T) {
	env := newOrgExportEnv(t)
	bg := context.Background()
	job := env.job(t)
	crashed := false
	first := export.NewOrgExporter(env.f.App, env.store, nil).WithStepHook(func(_ context.Context, table string) error {
		if table == "audit_log" && !crashed {
			crashed = true
			return errCrash
		}
		return nil
	})
	if _, err := first.RunJob(bg, job); err == nil || !crashed {
		t.Fatalf("first attempt = %v (crashed %v)", err, crashed)
	}
	var partial export.OrgExportManifest
	if err := json.Unmarshal(env.read(t, export.OrgExportManifestKey(env.f.OrgA, job.ID)), &partial); err != nil {
		t.Fatal(err)
	}
	if partial.CompletedAt != nil || tableOf(&partial, "audit_log") == nil || tableOf(&partial, "ci") != nil {
		t.Fatalf("manifest after the crash: completed %v, %d tables", partial.CompletedAt, len(partial.Tables))
	}
	kept := map[string]export.OrgExportTable{}
	for _, tb := range partial.Tables {
		kept[tb.Name] = tb
	}
	// A row added between the attempts is in the tables not exported yet.
	env.f.CI(t, env.f.OrgA, "", "wp1149-ci-between")

	m := env.run(t, export.NewOrgExporter(env.f.App, env.store, nil), job)
	seen := map[string]int{}
	var resumedSnapshot string
	for i := range m.Tables {
		tb := &m.Tables[i]
		seen[tb.Name]++
		if old, ok := kept[tb.Name]; ok {
			if !tb.SnapshotAt.Equal(old.SnapshotAt) || tb.Snapshot != old.Snapshot || tb.Rows != old.Rows {
				t.Errorf("%s was exported again: %+v, before %+v", tb.Name, tb, old)
			}
			continue
		}
		resumedSnapshot = tb.Snapshot
		if !tb.SnapshotAt.After(partial.Tables[0].SnapshotAt) {
			t.Errorf("%s: snapshot %v not after the first attempt %v", tb.Name, tb.SnapshotAt, partial.Tables[0].SnapshotAt)
		}
	}
	for name, n := range seen {
		if n != 1 {
			t.Errorf("%s %d times in the manifest", name, n)
		}
	}
	if resumedSnapshot == "" || resumedSnapshot == partial.Tables[0].Snapshot {
		t.Errorf("resumed snapshot %q, first %q", resumedSnapshot, partial.Tables[0].Snapshot)
	}
	if cis := tableOf(m, "ci"); cis == nil || cis.Rows != 3 {
		t.Errorf("ci = %+v, want the CI added between the attempts", cis)
	}
	if len(m.Tables) != len(appTables(t, env.f))+1 {
		t.Errorf("manifest has %d tables", len(m.Tables))
	}
}

// TestOrgExportCancelRemovesObjects: a cancel by the user (JOB-03) ends
// the attempt before its next batch and removes every object of the job.
func TestOrgExportCancelRemovesObjects(t *testing.T) {
	env := newOrgExportEnv(t)
	bg := context.Background()
	job := env.job(t)
	ctx, cancel := context.WithCancelCause(bg)
	defer cancel(nil)
	var afterCancel []string
	e := export.NewOrgExporter(env.f.App, env.store, nil).WithStepHook(func(_ context.Context, table string) error {
		if table == "ci" {
			cancel(jobs.ErrCancelled)
		}
		if context.Cause(ctx) != nil && table != "ci" {
			afterCancel = append(afterCancel, table)
		}
		return nil
	})
	if _, err := e.RunJob(ctx, job); !errors.Is(err, jobs.ErrCancelled) {
		t.Fatalf("cancelled export = %v, want ErrCancelled", err)
	}
	if len(afterCancel) > 0 {
		t.Errorf("tables after the cancel: %v", afterCancel)
	}
	if len(env.store.put) == 0 {
		t.Fatal("nothing was stored before the cancel")
	}
	for _, key := range append(env.store.put, export.OrgExportManifestKey(env.f.OrgA, job.ID)) {
		if ok, err := blob.Exists(bg, env.store, export.ExportBucket, key); err != nil || ok {
			t.Errorf("object %s after the cancel: %v %v", key, ok, err)
		}
	}

	// A job cancelled before it ran stores nothing.
	cancelled, stop := context.WithCancelCause(bg)
	stop(jobs.ErrCancelled)
	before := len(env.store.put)
	if _, err := export.NewOrgExporter(env.f.App, env.store, nil).RunJob(cancelled, env.job(t)); !errors.Is(err, jobs.ErrCancelled) || len(env.store.put) != before {
		t.Errorf("export cancelled before the start = %v, stored %d", err, len(env.store.put)-before)
	}
}

// leftObjects lists the objects stored under the prefix of the job that
// still exist and the manifest m does not list (nil m: every one).
func (env *orgExportEnv) leftObjects(t *testing.T, job *jobs.Job, m *export.OrgExportManifest) []string {
	t.Helper()
	listed := map[string]bool{export.OrgExportManifestKey(env.f.OrgA, job.ID): m != nil}
	if m != nil {
		for _, tb := range m.Tables {
			for _, p := range tb.Parts {
				listed[p.Key] = true
			}
		}
	}
	var left []string
	seen := map[string]bool{}
	for _, key := range append(env.store.put, export.OrgExportManifestKey(env.f.OrgA, job.ID)) {
		if seen[key] || !strings.HasPrefix(key, export.OrgExportPrefix(env.f.OrgA, job.ID)+"/") {
			continue
		}
		seen[key] = true
		if ok, err := blob.Exists(context.Background(), env.store, export.ExportBucket, key); err != nil {
			t.Fatal(err)
		} else if ok && !listed[key] {
			left = append(left, key)
		}
	}
	return left
}

// TestOrgExportPendingPartsRemoved: a crash inside a table leaves its
// parts pending in the manifest; the next attempt removes them and
// exports the table again, no object outside the manifest is left.
func TestOrgExportPendingPartsRemoved(t *testing.T) {
	env := newOrgExportEnv(t)
	bg := context.Background()
	job := env.job(t)
	crashed, ciSteps := false, 0
	first := export.NewOrgExporter(env.f.App, env.store, nil).WithLimits(1, 1).WithStepHook(func(_ context.Context, table string) error {
		// The hook runs after every batch: after the second batch of ci its
		// first part is stored (the second waits in the buffer).
		if table == "ci" && !crashed {
			if ciSteps++; ciSteps == 2 {
				crashed = true
				return errCrash
			}
		}
		return nil
	})
	if _, err := first.RunJob(bg, job); err == nil || !crashed {
		t.Fatalf("first attempt = %v (crashed %v)", err, crashed)
	}
	var partial export.OrgExportManifest
	if err := json.Unmarshal(env.read(t, export.OrgExportManifestKey(env.f.OrgA, job.ID)), &partial); err != nil {
		t.Fatal(err)
	}
	if partial.Pending == nil || partial.Pending.Name != "ci" || len(partial.Pending.Parts) != 1 {
		t.Fatalf("pending after the crash = %+v", partial.Pending)
	}
	pendingKey := partial.Pending.Parts[0].Key
	// A row deleted between the attempts: ci has fewer parts now.
	if _, err := env.f.Admin.Exec(bg, `DELETE FROM ci WHERE organization_id = $1 AND name = 'wp1149-ci-1'`, env.f.OrgA); err != nil {
		t.Fatal(err)
	}
	m := env.run(t, export.NewOrgExporter(env.f.App, env.store, nil).WithLimits(1, 1), job)
	if ci := tableOf(m, "ci"); ci == nil || ci.Rows != 1 || len(ci.Parts) != 1 {
		t.Errorf("ci after the resume = %+v", ci)
	}
	if left := env.leftObjects(t, job, m); len(left) > 0 {
		t.Errorf("objects outside the manifest: %v (pending was %s)", left, pendingKey)
	}

	// The process ends after a part was stored, before the manifest that
	// follows: the part was recorded as pending before it was stored.
	other := env.job(t)
	failed := false
	env.store.failPut = func(key, last string) bool {
		if !failed && strings.HasSuffix(key, "/manifest.json") && strings.Contains(last, "/tables/ci/part-") {
			failed = true
			return true
		}
		return false
	}
	if _, err := export.NewOrgExporter(env.f.App, env.store, nil).WithLimits(1, 1).RunJob(bg, other); err == nil || !failed {
		t.Fatalf("attempt with a failing manifest = %v (failed %v)", err, failed)
	}
	env.store.failPut = nil
	if _, err := env.f.Admin.Exec(bg, `DELETE FROM ci WHERE organization_id = $1`, env.f.OrgA); err != nil {
		t.Fatal(err)
	}
	m = env.run(t, export.NewOrgExporter(env.f.App, env.store, nil).WithLimits(1, 1), other)
	if left := env.leftObjects(t, other, m); len(left) > 0 {
		t.Errorf("objects outside the manifest after a failed manifest: %v", left)
	}
}

// TestOrgExportLastAttemptRemovesObjects: when the last attempt fails the
// parts (with personal data decrypted) and the manifest are removed; an
// earlier attempt keeps them for the next one.
func TestOrgExportLastAttemptRemovesObjects(t *testing.T) {
	env := newOrgExportEnv(t)
	bg := context.Background()
	// Every attempt completes one table and crashes.
	crash := func(context.Context, string) error { return errCrash }
	job := env.job(t)
	job.Attempts, job.MaxAttempts = 2, 3
	if _, err := export.NewOrgExporter(env.f.App, env.store, nil).WithStepHook(crash).RunJob(bg, job); err == nil {
		t.Fatal("the attempt did not fail")
	}
	if ok, err := blob.Exists(bg, env.store, export.ExportBucket, export.OrgExportManifestKey(env.f.OrgA, job.ID)); err != nil || !ok {
		t.Fatalf("manifest after an attempt before the last: %v %v", ok, err)
	}
	job.Attempts = 3
	if _, err := export.NewOrgExporter(env.f.App, env.store, nil).WithStepHook(crash).RunJob(bg, job); err == nil {
		t.Fatal("the last attempt did not fail")
	}
	if left := env.leftObjects(t, job, nil); len(left) > 0 {
		t.Errorf("objects after the last attempt failed: %v", left)
	}
}

// TestOrgExportWithoutVaultFails: without a vault a table with surrogate
// references fails the attempt; no table with values replaced by null is
// recorded as complete.
func TestOrgExportWithoutVaultFails(t *testing.T) {
	env := newOrgExportEnv(t)
	bg := context.Background()
	pii.SetDefault(nil)
	job := env.job(t)
	_, err := export.NewOrgExporter(env.f.App, env.store, nil).RunJob(bg, job)
	if err == nil || !strings.Contains(err.Error(), "no PII vault") {
		t.Fatalf("export without vault = %v", err)
	}
	var partial export.OrgExportManifest
	if err := json.Unmarshal(env.read(t, export.OrgExportManifestKey(env.f.OrgA, job.ID)), &partial); err != nil {
		t.Fatal(err)
	}
	if tb := tableOf(&partial, "ci_change"); tb != nil {
		t.Errorf("ci_change recorded complete without vault: %+v", tb)
	}
}
