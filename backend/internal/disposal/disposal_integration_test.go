package disposal_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// queryer is a pool or a transaction.
type queryer interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// jsonText is the canonical JSON text of a nullable value: a string as
// PostgreSQL's jsonb prints it (no HTML escaping), NULL as null.
func jsonText(t *testing.T, v *string) string {
	t.Helper()
	if v == nil {
		return "null"
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(*v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// recomputeHash reads the stored row of disposal record id and recomputes
// record_hash in Go after migration 000245, independent of the database
// function: SHA-256 (hex) of the canonical JSON of the business fields in
// their fixed order, timestamps in UTC with microseconds. It returns the
// stored and the recomputed hash.
func recomputeHash(t *testing.T, q queryer, id string) (stored, computed string) {
	t.Helper()
	var (
		recID, org, method                                        string
		asset, ci, level, carrier, cert, supplier, doc, perf, wit *string
		notes                                                     *string
		protection                                                *int16
		performedAt, createdAt                                    time.Time
	)
	if err := q.QueryRow(context.Background(), `SELECT id::text, organization_id::text, asset_id::text, ci_id::text, method,
			protection_class, security_level, data_carrier, certificate_ref, supplier_id::text, evidence_document_id::text,
			performer_surrogate::text, witness_surrogate::text, performed_at, notes, created_at, record_hash
		FROM disposal_record WHERE id = $1`, id).Scan(&recID, &org, &asset, &ci, &method, &protection, &level, &carrier,
		&cert, &supplier, &doc, &perf, &wit, &performedAt, &notes, &createdAt, &stored); err != nil {
		t.Fatalf("read disposal record %s: %v", id, err)
	}
	ts := func(v time.Time) string {
		s := v.UTC().Truncate(time.Microsecond).Format("2006-01-02T15:04:05.000000Z")
		return jsonText(t, &s)
	}
	pc := "null"
	if protection != nil {
		pc = strconv.Itoa(int(*protection))
	}
	canonical := `{"id":` + jsonText(t, &recID) + `,"organization_id":` + jsonText(t, &org) +
		`,"asset_id":` + jsonText(t, asset) + `,"ci_id":` + jsonText(t, ci) + `,"method":` + jsonText(t, &method) +
		`,"protection_class":` + pc + `,"security_level":` + jsonText(t, level) +
		`,"data_carrier":` + jsonText(t, carrier) + `,"certificate_ref":` + jsonText(t, cert) +
		`,"supplier_id":` + jsonText(t, supplier) + `,"evidence_document_id":` + jsonText(t, doc) +
		`,"performer_surrogate":` + jsonText(t, perf) + `,"witness_surrogate":` + jsonText(t, wit) +
		`,"performed_at":` + ts(performedAt) + `,"notes":` + jsonText(t, notes) + `,"created_at":` + ts(createdAt) + `}`
	sum := sha256.Sum256([]byte(canonical))
	return stored, hex.EncodeToString(sum[:])
}

// requireHash fails unless the stored record_hash of id equals the Go
// recomputation and the database function disposal_record_hash.
func requireHash(t *testing.T, q queryer, id, when string) string {
	t.Helper()
	stored, computed := recomputeHash(t, q, id)
	var function string
	if err := q.QueryRow(context.Background(), `SELECT disposal_record_hash(id, organization_id, asset_id, ci_id, method,
			protection_class, security_level, data_carrier, certificate_ref, supplier_id, evidence_document_id,
			performer_surrogate, witness_surrogate, performed_at, notes, created_at)
		FROM disposal_record WHERE id = $1`, id).Scan(&function); err != nil {
		t.Fatalf("%s: disposal_record_hash: %v", when, err)
	}
	if len(stored) != 64 || stored != computed || stored != function {
		t.Fatalf("%s: record_hash %q, recomputed %q, database function %q", when, stored, computed, function)
	}
	return stored
}

type normFixture struct {
	*scopetest.Fixture
	repo                      *disposal.PGRepository
	asset, ci, supplier, doc  string
	foreignAsset, foreignSupp string
	foreignDoc                string
	performedAt               time.Time
	destructionWithNormFields func(assetID string) *disposal.Record
	countRecords              func() (int, int)
}

// seedNorm creates the fixture of the WP-751 tests with a vault (pattern
// notify/events_integration_test.go): in organization A an asset, a CI, an
// org-wide supplier and an unlinked document, plus an asset, a supplier
// and a document of client 2.
func seedNorm(t *testing.T) *normFixture {
	t.Helper()
	f := scopetest.Seed(t, "32")
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

	bg := context.Background()
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var id string
		if qErr := f.Admin.QueryRow(bg, sql, args...).Scan(&id); qErr != nil {
			t.Fatalf("%s: %v", sql, qErr)
		}
		return id
	}
	uploader := f.AppUser(t, f.OrgA, "disposal-uploader")
	n := &normFixture{Fixture: f, repo: disposal.NewPGRepository(f.App),
		performedAt: time.Date(2026, 11, 2, 9, 30, 15, 123456000, time.UTC)}
	n.asset = f.Asset(t, f.OrgA, f.Client1, "scopetest-32-norm")
	n.foreignAsset = f.Asset(t, f.OrgA, f.Client2, "scopetest-32-norm-foreign")
	n.ci = f.CI(t, f.OrgA, f.Client1, "scopetest-32-norm-ci")
	n.supplier = scalar(`INSERT INTO supplier (organization_id, name) VALUES ($1, 'scopetest-32 Entsorger GmbH') RETURNING id::text`, f.OrgA)
	n.foreignSupp = scalar(`INSERT INTO supplier (organization_id, client_id, name) VALUES ($1, $2, 'scopetest-32 Fremd GmbH') RETURNING id::text`,
		f.OrgA, f.Client2)
	n.doc = scalar(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
		VALUES ($1, 'Vernichtungsprotokoll', 'protokoll.pdf', '', $2) RETURNING id::text`, f.OrgA, uploader)
	n.foreignDoc = scalar(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
		VALUES ($1, 'Fremdes Protokoll', 'fremd.pdf', '', $2) RETURNING id::text`, f.OrgA, uploader)
	if _, err = f.Admin.Exec(bg, `INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'asset', $3)`,
		f.OrgA, n.foreignDoc, n.foreignAsset); err != nil {
		t.Fatalf("link foreign document: %v", err)
	}
	pc := 2
	n.destructionWithNormFields = func(assetID string) *disposal.Record {
		return &disposal.Record{OrganizationID: f.OrgA, AssetID: assetID, Method: "destruction", ProtectionClass: &pc,
			SecurityLevel: "P-4", DataCarrier: "SSD Samsung SN-4711", SupplierID: n.supplier, EvidenceDocumentID: n.doc,
			CertificateRef: "ZERT-2026-11", PerformedBy: "Erika Mustermann", Witness: "Max Zeugmann",
			PerformedAt: n.performedAt, Notes: "Schredder \"S-3\"\nPartikel ≤ 320 mm² & <b>"}
	}
	n.countRecords = func() (records, entries int) {
		t.Helper()
		if qErr := f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM disposal_record WHERE organization_id = $1),
				(SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type = 'disposal_record')`, f.OrgA).
			Scan(&records, &entries); qErr != nil {
			t.Fatal(qErr)
		}
		return records, entries
	}
	return n
}

// TestDisposalRecordNormFieldsHashAndAudit covers WP-751 (MGT-02, E-55):
// the record keeps the DIN 66399 fields, the names only as vault
// surrogates and a record_hash the database sets; the audit entry
// disposal_record.created in the same transaction carries the hash and the
// chain stays intact. GET resolves the names; after the vault subjects are
// deleted (crypto-shredding) the hash stays verifiable. A record written
// before 000245 shows its plaintext performed_by.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDisposalRecordNormFieldsHashAndAudit(t *testing.T) {
	n := seedNorm(t)
	bg := context.Background()
	ctx := n.OrgCtx(n.OrgA)

	rec := n.destructionWithNormFields(n.asset)
	if err := n.repo.Create(ctx, rec); err != nil {
		t.Fatalf("create: %v", err)
	}
	hash := requireHash(t, n.Admin, rec.ID, "after create")
	if rec.RecordHash != hash {
		t.Errorf("Create returned record_hash %q, stored %q", rec.RecordHash, hash)
	}

	// No plaintext name in the row; the surrogates are person subjects.
	var row string
	var performer, witness *string
	if err := n.Admin.QueryRow(bg, `SELECT to_jsonb(d)::text, performer_surrogate::text, witness_surrogate::text
		FROM disposal_record d WHERE id = $1`, rec.ID).Scan(&row, &performer, &witness); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Erika Mustermann", "Max Zeugmann"} {
		if strings.Contains(row, name) {
			t.Errorf("disposal_record row holds the plaintext name %q", name)
		}
	}
	if performer == nil || witness == nil {
		t.Fatalf("surrogates performer %v witness %v, want both", performer, witness)
	}
	var persons int
	if err := n.Admin.QueryRow(bg, `SELECT count(*) FROM pii_subject WHERE organization_id = $1 AND kind = 'person'
		AND surrogate_id IN ($2, $3)`, n.OrgA, *performer, *witness).Scan(&persons); err != nil || persons != 2 {
		t.Fatalf("person subjects: %d (%v), want 2", persons, err)
	}

	// The audit entry: last in the transaction, with the hash, no PII.
	var entries int
	var changes map[string]any
	var changesText string
	if err := n.Admin.QueryRow(bg, `SELECT count(*) OVER (), changes, changes::text FROM audit_log
		WHERE organization_id = $1 AND action = $2 AND resource_type = $3 AND resource_id = $4`,
		n.OrgA, disposal.ActionCreated, disposal.ResourceType, rec.ID).Scan(&entries, &changes, &changesText); err != nil {
		t.Fatalf("audit entry %s: %v", disposal.ActionCreated, err)
	}
	want := map[string]any{"record_hash": hash, "asset_id": n.asset, "method": "destruction",
		"protection_class": float64(2), "security_level": "P-4"}
	if entries != 1 || len(changes) != len(want) {
		t.Errorf("audit entries %d with changes %v, want 1 with %v", entries, changes, want)
	}
	for k, v := range want {
		if changes[k] != v {
			t.Errorf("audit changes[%s] = %v, want %v", k, changes[k], v)
		}
	}
	for _, name := range []string{"Erika", "Zeugmann", *performer, *witness} {
		if strings.Contains(changesText, name) {
			t.Errorf("audit changes hold %q", name)
		}
	}
	if result, err := audit.Verify(bg, n.App, n.OrgA); err != nil || !result.Intact {
		t.Fatalf("audit chain after the disposal: %+v %v", result, err)
	}

	// GET resolves the names and returns the standard fields.
	got, err := n.repo.GetByID(ctx, n.OrgA, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PerformedBy != "Erika Mustermann" || got.Witness != "Max Zeugmann" || got.RecordHash != hash ||
		got.ProtectionClass == nil || *got.ProtectionClass != 2 || got.SecurityLevel != "P-4" ||
		got.SupplierID != n.supplier || got.EvidenceDocumentID != n.doc || !got.PerformedAt.Equal(n.performedAt) {
		t.Errorf("GET: %+v", got)
	}
	list, _, err := n.repo.List(ctx, n.OrgA, disposal.FilterParams{AssetID: n.asset}, api.PaginationParams{Limit: 10})
	if err != nil || len(list) != 1 || list[0].PerformedBy != "Erika Mustermann" || list[0].Witness != "Max Zeugmann" {
		t.Errorf("list: %+v %v", list, err)
	}

	// Crypto-shredding: the names are gone, the hash stays verifiable.
	if _, err = n.Admin.Exec(bg, `DELETE FROM pii_subject WHERE surrogate_id IN ($1, $2)`, *performer, *witness); err != nil {
		t.Fatal(err)
	}
	if got, err = n.repo.GetByID(ctx, n.OrgA, rec.ID); err != nil || got.PerformedBy != "" || got.Witness != "" {
		t.Errorf("GET after shredding: %+v %v, want no names", got, err)
	}
	if after := requireHash(t, n.Admin, rec.ID, "after shredding"); after != hash {
		t.Errorf("record_hash changed after shredding: %q, want %q", after, hash)
	}

	// A record written before 000245 (plaintext performed_by, no
	// surrogate) shows the plaintext; a client-sent record_hash is
	// overwritten by the trigger.
	var legacy string
	if err = n.Admin.QueryRow(bg, `INSERT INTO disposal_record (organization_id, ci_id, method, performed_by, performed_at, record_hash)
		VALUES ($1, $2, 'recycling', 'Alt Bestand', $3, 'forged') RETURNING id::text`, n.OrgA, n.ci, n.performedAt).Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	if h := requireHash(t, n.Admin, legacy, "client-sent hash"); h == "forged" {
		t.Error("client-sent record_hash kept")
	}
	if got, err = n.repo.GetByID(ctx, n.OrgA, legacy); err != nil || got.PerformedBy != "Alt Bestand" {
		t.Errorf("GET of a record without surrogate: %+v %v, want performed_by Alt Bestand", got, err)
	}

	// Without a vault no name is stored, neither as surrogate nor as
	// plaintext.
	pii.SetDefault(nil)
	records, entriesBefore := n.countRecords()
	if err = n.repo.Create(ctx, &disposal.Record{OrganizationID: n.OrgA, AssetID: n.asset, Method: "reuse",
		PerformedBy: "Ohne Tresor", PerformedAt: n.performedAt}); !errors.Is(err, pii.ErrNoKey) {
		t.Errorf("create without vault: %v, want pii.ErrNoKey", err)
	}
	if r, e := n.countRecords(); r != records || e != entriesBefore {
		t.Errorf("create without vault left %d records and %d audit entries, want %d and %d", r, e, records, entriesBefore)
	}
}

// TestDisposalRecordRollbackLeavesNoAudit: in a joined transaction (the
// disposal path of WP-278) the audit entry is written before the commit; a
// rollback leaves neither the record nor the entry nor vault subjects.
func TestDisposalRecordRollbackLeavesNoAudit(t *testing.T) {
	n := seedNorm(t)
	records, entries := n.countRecords()
	var subjects int
	countSubjects := func() int {
		var c int
		if err := n.Admin.QueryRow(context.Background(), `SELECT count(*) FROM pii_subject WHERE organization_id = $1`, n.OrgA).Scan(&c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	subjects = countSubjects()
	errLater := errors.New("a later step of the disposal failed")
	err := database.WithRequestTenantTx(n.OrgCtx(n.OrgA), n.App, n.OrgA, func(ctx context.Context) error {
		if createErr := n.repo.Create(ctx, n.destructionWithNormFields(n.asset)); createErr != nil {
			return createErr
		}
		return errLater
	})
	if !errors.Is(err, errLater) {
		t.Fatalf("joined transaction: %v, want the later error", err)
	}
	if r, e := n.countRecords(); r != records || e != entries {
		t.Errorf("rollback left %d records and %d audit entries, want %d and %d", r, e, records, entries)
	}
	if c := countSubjects(); c != subjects {
		t.Errorf("rollback left %d vault subjects, want %d", c, subjects)
	}
}

// TestDisposalRecordReferencesInScope: Create checks asset, supplier and
// evidence document under the principal's scope before the insert (foreign
// keys skip row level security); a reference the principal does not see is
// api.ErrNotFound without record and audit entry. The propagation of a
// client change (000145) leaves record_hash verifiable.
func TestDisposalRecordReferencesInScope(t *testing.T) {
	n := seedNorm(t)
	bg := context.Background()
	client1 := n.ClientCtx(n.Client1)
	records, entries := n.countRecords()
	for _, c := range []struct {
		name   string
		mutate func(r *disposal.Record)
	}{
		{"foreign asset", func(r *disposal.Record) { r.AssetID = n.foreignAsset }},
		{"foreign supplier", func(r *disposal.Record) { r.SupplierID = n.foreignSupp }},
		{"foreign document", func(r *disposal.Record) { r.EvidenceDocumentID = n.foreignDoc }},
	} {
		rec := n.destructionWithNormFields(n.asset)
		c.mutate(rec)
		if err := n.repo.Create(client1, rec); !errors.Is(err, api.ErrNotFound) {
			t.Errorf("%s: %v, want api.ErrNotFound", c.name, err)
		}
	}
	if r, e := n.countRecords(); r != records || e != entries {
		t.Errorf("refused creates left %d records and %d audit entries, want %d and %d", r, e, records, entries)
	}

	// A soft-deleted evidence document counts as missing.
	if _, err := n.Admin.Exec(bg, `UPDATE document SET deleted_at = now() WHERE id = $1`, n.foreignDoc); err != nil {
		t.Fatal(err)
	}
	rec := n.destructionWithNormFields(n.foreignAsset)
	rec.EvidenceDocumentID = n.foreignDoc
	if err := n.repo.Create(n.OrgCtx(n.OrgA), rec); !errors.Is(err, api.ErrNotFound) {
		t.Errorf("soft-deleted evidence document: %v, want api.ErrNotFound", err)
	}

	own := n.destructionWithNormFields(n.asset)
	if err := n.repo.Create(client1, own); err != nil {
		t.Fatalf("client-1 disposal of its asset: %v", err)
	}
	hash := requireHash(t, n.Admin, own.ID, "client 1")
	if _, err := n.Admin.Exec(bg, `UPDATE asset SET client_id = $2 WHERE id = $1`, n.asset, n.Client2); err != nil {
		t.Fatalf("move asset to client 2: %v", err)
	}
	var client string
	if err := n.Admin.QueryRow(bg, `SELECT client_id::text FROM disposal_record WHERE id = $1`, own.ID).Scan(&client); err != nil || client != n.Client2 {
		t.Fatalf("record client after the move: %q %v, want client 2", client, err)
	}
	if after := requireHash(t, n.Admin, own.ID, "after the client change"); after != hash {
		t.Errorf("record_hash after the client change %q, want %q", after, hash)
	}
}

// TestDisposalRecordAppendOnlyAndRestrict: the record refuses UPDATE and
// DELETE; an asset, a CI (jobs.HardDeleteCI), an evidence document and a
// supplier with a disposal record cannot be deleted for good: the foreign
// key refuses with 23503 instead of the append-only guard with P0001, and
// the record stays unchanged.
func TestDisposalRecordAppendOnlyAndRestrict(t *testing.T) {
	n := seedNorm(t)
	bg := context.Background()
	ctx := n.OrgCtx(n.OrgA)
	onAsset := n.destructionWithNormFields(n.asset)
	if err := n.repo.Create(ctx, onAsset); err != nil {
		t.Fatal(err)
	}
	onCI := &disposal.Record{OrganizationID: n.OrgA, CIID: n.ci, Method: "recycling", PerformedBy: "Erika Mustermann",
		PerformedAt: n.performedAt}
	if err := n.repo.Create(ctx, onCI); err != nil {
		t.Fatal(err)
	}
	hashes := map[string]string{onAsset.ID: requireHash(t, n.Admin, onAsset.ID, "asset"), onCI.ID: requireHash(t, n.Admin, onCI.ID, "ci")}

	var pgErr *pgconn.PgError
	for _, stmt := range []string{`UPDATE disposal_record SET notes = 'x' WHERE id = $1`, `DELETE FROM disposal_record WHERE id = $1`} {
		if _, err := n.Admin.Exec(bg, stmt, onAsset.ID); !errors.As(err, &pgErr) || pgErr.Code != "P0001" {
			t.Errorf("%s: %v, want P0001 (append-only)", stmt, err)
		}
	}

	restricted := func(name, constraint string, del func(tx pgx.Tx) error) {
		t.Helper()
		tx, err := n.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		err = del(tx)
		if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != constraint {
			t.Errorf("delete %s: %v, want 23503 on %s", name, err, constraint)
		}
	}
	exec := func(sql string, args ...any) func(tx pgx.Tx) error {
		return func(tx pgx.Tx) error {
			_, err := tx.Exec(bg, sql, args...)
			return err
		}
	}
	restricted("asset", "disposal_record_asset_id_fkey", exec(`DELETE FROM asset WHERE id = $1`, n.asset))
	restricted("evidence document", "disposal_record_evidence_document_id_fkey", exec(`DELETE FROM document WHERE id = $1`, n.doc))
	restricted("supplier", "disposal_record_supplier_id_fkey", exec(`DELETE FROM supplier WHERE id = $1`, n.supplier))
	if _, err := n.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() - interval '1 year' WHERE id = $1`, n.ci); err != nil {
		t.Fatal(err)
	}
	restricted("ci (HardDeleteCI)", "disposal_record_ci_id_fkey", func(tx pgx.Tx) error {
		return jobs.HardDeleteCI(bg, tx, n.OrgA, n.ci)
	})

	for id, hash := range hashes {
		var asset, ci *string
		if err := n.Admin.QueryRow(bg, `SELECT asset_id::text, ci_id::text FROM disposal_record WHERE id = $1`, id).Scan(&asset, &ci); err != nil {
			t.Fatalf("record %s after the refused deletes: %v", id, err)
		}
		if (asset == nil && ci == nil) || requireHash(t, n.Admin, id, "after the refused deletes") != hash {
			t.Errorf("record %s changed: asset %v ci %v", id, asset, ci)
		}
	}
}

// TestDisposalRecordLegacyBackfill replays migration 000245 over a record
// written before it (pattern audit/pg_roundtrip_integration_test.go), in a
// transaction that is rolled back: down, a former row with plaintext
// performed_by, up. The row then has a record_hash equal to the
// recomputation, empty standard fields and its performed_by.
func TestDisposalRecordLegacyBackfill(t *testing.T) {
	n := seedNorm(t)
	bg := context.Background()
	read := func(name string) string {
		b, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	tx, err := n.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, read("000245_disposal_norm_fields.down.sql")); err != nil {
		t.Fatalf("down: %v", err)
	}
	var legacy string
	if err = tx.QueryRow(bg, `INSERT INTO disposal_record (organization_id, asset_id, method, performed_by, performed_at, notes)
		VALUES ($1, $2, 'recycling', 'Alt Bestand', $3, 'vor 000245') RETURNING id::text`, n.OrgA, n.asset, n.performedAt).Scan(&legacy); err != nil {
		t.Fatalf("former row: %v", err)
	}
	if _, err = tx.Exec(bg, read("000245_disposal_norm_fields.up.sql")); err != nil {
		t.Fatalf("up: %v", err)
	}
	requireHash(t, tx, legacy, "backfill")
	var performedBy string
	var empty bool
	if err = tx.QueryRow(bg, `SELECT performed_by, protection_class IS NULL AND security_level IS NULL AND supplier_id IS NULL
			AND evidence_document_id IS NULL AND performer_surrogate IS NULL AND witness_surrogate IS NULL
		FROM disposal_record WHERE id = $1`, legacy).Scan(&performedBy, &empty); err != nil {
		t.Fatal(err)
	}
	if performedBy != "Alt Bestand" || !empty {
		t.Errorf("former row after 000245: performed_by %q, standard fields empty %v", performedBy, empty)
	}
	var notNull bool
	if err = tx.QueryRow(bg, `SELECT attnotnull FROM pg_attribute WHERE attrelid = 'disposal_record'::regclass AND attname = 'record_hash'`).
		Scan(&notNull); err != nil || !notNull {
		t.Errorf("record_hash NOT NULL: %v %v", notNull, err)
	}
}
