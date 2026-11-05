package assignment_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// TestAssignmentSignatureKeepsSurrogate covers F-385 (WP-656, SEC-05,
// AUD-04, WP-241 AK1) against PostgreSQL after the drop of assignment
// (WP-843): the signer names of the handover and return signatures live in
// the PII vault, the legacy signatures keep only their surrogates. The
// signer names are sealed into the vault as pii.backfill did before the
// drop; in a rolled-back transaction the down part of 000266 rebuilds
// assignment, the rows get the signatures with the surrogates and their
// movements of the same id (000257), and the up part moves the signatures
// into assignment_signature_legacy. There the surrogates stand unchanged,
// their vault subjects stay and resolve to the names in organization A
// only; organization B neither sees the rows nor resolves the signers.
// Deleting a vault entry shreds the name while the signature stays.
// pii.backfill runs without assignment.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssignmentSignatureKeepsSurrogate(t *testing.T) {
	f := scopetest.Seed(t, "a6")
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
	dropUp, dropDown := readDropMigration(t)

	asset := f.Asset(t, f.OrgA, f.Client1, "wp656-asset")
	holder := f.AppUser(t, f.OrgA, "wp656holder")
	successor := f.AppUser(t, f.OrgA, "wp656next")
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: holder})

	// The signer names in the vault of organization A, as pii.backfill
	// sealed them before the drop (WP-656).
	names := []string{"Max Verifier", "Lena Legacy", "Rita Rueckgabe"}
	refs := make([]map[string]any, len(names))
	scope := database.OrgWideScope(f.OrgA, "")
	if err = database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		for i, name := range names {
			ref, serr := pii.SealValueTx(ctx, tx, f.OrgA, name)
			if serr != nil {
				return serr
			}
			refs[i] = ref
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	subjects := make([]string, len(refs))
	for i, ref := range refs {
		id, ok := pii.RefID(ref)
		if !ok || !uuidPattern.MatchString(id) {
			t.Fatalf("surrogate of %q = %v", names[i], ref)
		}
		subjects[i] = id
	}
	signature := func(ref map[string]any, fields map[string]any) string {
		t.Helper()
		fields["signer_name"] = ref
		raw, merr := json.Marshal(fields)
		if merr != nil {
			t.Fatal(merr)
		}
		return string(raw)
	}
	handoverSig := signature(refs[0], map[string]any{"method": "typed", "signed_at": "2026-10-28T10:00:00Z"})
	legacySig := signature(refs[1], map[string]any{"method": "drawn", "image_base64": "aGVsbG8="})
	returnSig := signature(refs[2], map[string]any{"method": "typed", "signed_at": "2026-10-28T11:00:00Z"})

	// The vault resolves the surrogates to the names in organization A
	// only.
	name := func(orgID, id string) (string, error) {
		subject, rerr := vault.Resolve(f.OrgCtx(orgID), orgID, id)
		if rerr != nil {
			return "", rerr
		}
		return subject.Data["v"], nil
	}
	for i, subject := range subjects {
		if got, rerr := name(f.OrgA, subject); rerr != nil || got != names[i] {
			t.Fatalf("signer %s = %q, %v, want %q", subject, got, rerr, names[i])
		}
		if got, rerr := name(f.OrgB, subject); rerr == nil {
			t.Fatalf("signer resolvable in another organization: %q", got)
		}
	}

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, query, args...); execErr != nil {
			t.Fatalf("%.70s...: %v", strings.TrimSpace(query), execErr)
		}
	}
	exists := func(query string, args ...any) bool {
		t.Helper()
		var found bool
		if qerr := tx.QueryRow(bg, `SELECT EXISTS (`+query+`)`, args...).Scan(&found); qerr != nil {
			t.Fatal(qerr)
		}
		return found
	}
	exec(dropDown)
	insert := func(assignee, checkout, ret string) string {
		t.Helper()
		id := f.ID()
		exec(`INSERT INTO assignment (id, organization_id, asset_id, assigned_to, assigned_by, assignment_type, status,
				checkout_signature, return_signature)
			VALUES ($1, $2, $3, $4, $4, 'assignment', 'returned', $5::jsonb, NULLIF($6, '')::jsonb)`,
			id, f.OrgA, asset, assignee, checkout, ret)
		exec(`INSERT INTO stock_movement (id, organization_id, item_kind, asset_id, movement_type, assignee_user_id)
			VALUES ($1, $2, 'asset', $3, 'assignment', $4)`, id, f.OrgA, asset, assignee)
		return id
	}
	handover := insert(holder, handoverSig, "")
	returned := insert(successor, legacySig, returnSig)
	exec(dropUp)

	// The archive keeps the signatures with their surrogates unchanged.
	stored := func(movement, kind string) string {
		t.Helper()
		var payload string
		if qerr := tx.QueryRow(bg, `SELECT payload::text FROM assignment_signature_legacy WHERE movement_id = $1 AND kind = $2`,
			movement, kind).Scan(&payload); qerr != nil {
			t.Fatalf("%s signature of %s: %v", kind, movement, qerr)
		}
		return payload
	}
	for _, c := range []struct{ movement, kind, want, subject string }{
		{handover, "checkout", handoverSig, subjects[0]},
		{returned, "checkout", legacySig, subjects[1]},
		{returned, "return", returnSig, subjects[2]},
	} {
		if !exists(`SELECT 1 FROM assignment_signature_legacy WHERE movement_id = $1 AND kind = $2 AND payload = $3::jsonb`, c.movement, c.kind, c.want) {
			t.Fatalf("%s signature of %s = %s, want %s", c.kind, c.movement, stored(c.movement, c.kind), c.want)
		}
		var parsed map[string]any
		if uerr := json.Unmarshal([]byte(stored(c.movement, c.kind)), &parsed); uerr != nil {
			t.Fatal(uerr)
		}
		if id, ok := pii.RefID(parsed["signer_name"]); !ok || id != c.subject {
			t.Fatalf("%s signature of %s holds signer %v, want the surrogate %s", c.kind, c.movement, parsed["signer_name"], c.subject)
		}
		if !exists(`SELECT 1 FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2 AND dek_ref > 0`, f.OrgA, c.subject) {
			t.Fatalf("the vault entry of %s is gone", c.subject)
		}
	}
	if exists(`SELECT 1 FROM assignment_signature_legacy WHERE organization_id = $1 AND payload::text ~ '(Verifier|Rueckgabe|Legacy)'`, f.OrgA) ||
		exists(`SELECT 1 FROM migration_quarantine WHERE migration = '000266' AND row_data::text ~ '(Verifier|Rueckgabe|Legacy)'`) {
		t.Fatal("plaintext signer names in the archive or the quarantine")
	}
	if exists(`SELECT 1 FROM pii_subject WHERE encode(encrypted_payload, 'escape') ~ '(Verifier|Rueckgabe|Legacy)'`) {
		t.Fatal("the vault holds the signer names in plaintext")
	}
	// Organization B sees no row of the archive.
	visibleIn := func(org string) bool {
		t.Helper()
		sp, spErr := tx.Begin(bg)
		if spErr != nil {
			t.Fatal(spErr)
		}
		defer func() { _ = sp.Rollback(bg) }()
		if _, setErr := sp.Exec(bg, `SELECT set_config('app.org_id', $1, true)`, org); setErr != nil {
			t.Fatal(setErr)
		}
		if _, setErr := sp.Exec(bg, `SET LOCAL ROLE reticora_app`); setErr != nil {
			t.Fatal(setErr)
		}
		var found bool
		if qerr := sp.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM assignment_signature_legacy WHERE movement_id = ANY ($1::uuid[]))`,
			[]string{handover, returned}).Scan(&found); qerr != nil {
			t.Fatal(qerr)
		}
		return found
	}
	if !visibleIn(f.OrgA) || visibleIn(f.OrgB) {
		t.Fatal("the archive is not visible in organization A only")
	}

	// Deleting the vault entry shreds the name (in the transaction, which
	// holds locks on the referenced tables); the signature with its
	// surrogate stays in the archive, the other signers stay resolvable.
	if err = pii.DeleteTx(ctx, tx, f.OrgA, subjects[0]); err != nil {
		t.Fatal(err)
	}
	left, err := vault.ResolveTx(ctx, tx, f.OrgA, subjects)
	if err != nil {
		t.Fatal(err)
	}
	if left[subjects[0]] != nil || left[subjects[2]] == nil || left[subjects[2]].Data["v"] != names[2] {
		t.Fatalf("signers after shredding the handover signer = %v, want all but %s", left, subjects[0])
	}
	if exists(`SELECT 1 FROM pii_subject WHERE organization_id = $1 AND surrogate_id = $2`, f.OrgA, subjects[0]) {
		t.Fatal("the vault entry of the shredded signer is left")
	}
	if !exists(`SELECT 1 FROM assignment_signature_legacy WHERE movement_id = $1 AND kind = 'checkout' AND payload = $2::jsonb`, handover, handoverSig) {
		t.Fatalf("the signature changed by shredding: %s, want %s", stored(handover, "checkout"), handoverSig)
	}
	if err = tx.Rollback(bg); err != nil {
		t.Fatal(err)
	}

	// pii.backfill runs without assignment and finds nothing to seal there.
	var dropped bool
	if err = f.Admin.QueryRow(bg, `SELECT to_regclass('public.assignment') IS NULL`).Scan(&dropped); err != nil || !dropped {
		t.Fatalf("assignment dropped = %v, %v", dropped, err)
	}
	if _, err = jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA); err != nil {
		t.Fatalf("backfill without assignment: %v", err)
	}
}
