package audit_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// TestAuditSealOnlyReplacesPersonalValues covers WP-680 (AUD-04, SEC-05;
// F-409, WP-241): audit_seal_pii, the only way the runtime role changes an
// audit entry, accepts nothing but non-empty string values of personal keys
// replaced by references to value subjects of the entry's organization; a
// changed other value, a reference to an unknown subject, to one of another
// organization or of another kind, a changed number text, a removed key or
// an entry of another organization are refused and leave the entry as it
// was. The pii.backfill run seals legacy entries through it without
// rewriting a hash (also an entry sealed in part before); audit.Verify
// checks a sealed entry's link with the digest of its original, its content
// with its seal and, with the vault, its values against the original: a
// value changed in the vault or a content changed after the seal breaks the
// chain at that entry, a deleted value leaves it unconfirmed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAuditSealOnlyReplacesPersonalValues(t *testing.T) {
	f := scopetest.Seed(t, "2a")
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

	// The personal keys of the database are those of the vault.
	var keys []string
	if err = f.Admin.QueryRow(bg, `SELECT audit_personal_keys()`).Scan(&keys); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(keys, pii.PersonalKeys()) {
		t.Errorf("audit_personal_keys() = %v, want pii.PersonalKeys() %v", keys, pii.PersonalKeys())
	}

	// Entries written before migration 000125, with plaintext.
	legacy := func(org, changes string) string {
		t.Helper()
		var id string
		if ierr := f.Admin.QueryRow(bg, `INSERT INTO audit_log (organization_id, actor_type, action, resource_type, changes, hash)
			VALUES ($1, 'system', 'contact.updated', 'contact', $2::jsonb, '') RETURNING id::text`, org, changes).Scan(&id); ierr != nil {
			t.Fatal(ierr)
		}
		return id
	}
	inOrg := func(org string, fn func(ctx context.Context, tx pgx.Tx) error) error {
		scope := database.OrgWideScope(org, "")
		return database.WithTenant(bg, f.App, &scope, fn)
	}
	first := legacy(f.OrgA, `{"after": {"email": "old@example.org", "role": "admin"}, "n": 2.50, "tags": ["a"]}`)
	second := legacy(f.OrgA, `{"display_name": "Max Muster", "phones": {"phone": ["+49 1", "+49 2"]}}`)
	third := legacy(f.OrgA, `{"email": "x@example.org", "user_name": "xuser"}`)
	foreign := legacy(f.OrgB, `{"email": "b@example.org"}`)
	if err = inOrg(f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, rerr := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: f.OrgA, ActorType: "system",
			Action: "wp680.after", ResourceType: "organization"})
		return rerr
	}); err != nil {
		t.Fatal(err)
	}
	type row struct{ id, changes, prev, hash string }
	chain := func() []row {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT id::text, changes::text, previous_hash, hash FROM audit_log
			WHERE organization_id = $1 ORDER BY seq`, f.OrgA)
		if qerr != nil {
			t.Fatal(qerr)
		}
		out, qerr := pgx.CollectRows(rows, func(r pgx.CollectableRow) (row, error) {
			var s row
			scanErr := r.Scan(&s.id, &s.changes, &s.prev, &s.hash)
			return s, scanErr
		})
		if qerr != nil {
			t.Fatal(qerr)
		}
		return out
	}
	changesOf := func(id string) string {
		t.Helper()
		var c string
		if qerr := f.Admin.QueryRow(bg, `SELECT changes::text FROM audit_log WHERE id = $1`, id).Scan(&c); qerr != nil {
			t.Fatal(qerr)
		}
		return c
	}
	verify := func() audit.VerifyResult {
		t.Helper()
		res, verr := audit.Verify(bg, f.App, f.OrgA)
		if verr != nil {
			t.Fatal(verr)
		}
		return res
	}
	if res := verify(); !res.Intact || res.Checked != 4 || res.Sealed != 0 {
		t.Fatalf("chain before sealing = %+v", res)
	}
	original := chain()

	// Subjects for the refusals: a value of organization B, a contact of A.
	var valueB, contactA, valueA string
	if valueB, err = vault.Create(f.OrgCtx(f.OrgB), f.OrgB, pii.KindValue, pii.Data{"v": "old@example.org"}); err != nil {
		t.Fatal(err)
	}
	if contactA, err = vault.Create(f.OrgCtx(f.OrgA), f.OrgA, pii.KindContact, pii.Data{"name": "old@example.org"}); err != nil {
		t.Fatal(err)
	}
	if valueA, err = vault.Create(f.OrgCtx(f.OrgA), f.OrgA, pii.KindValue, pii.Data{"v": "old@example.org"}); err != nil {
		t.Fatal(err)
	}
	ref := func(id string) string { return `{"$pii": "` + id + `"}` }
	seal := func(org, id, changes string) (bool, error) {
		var found bool
		serr := inOrg(org, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT audit_seal_pii($1, $2::jsonb)`, id, changes).Scan(&found)
		})
		return found, serr
	}
	for _, tc := range []struct{ name, changes string }{
		{"a value of another key", `{"after": {"email": ` + ref(valueA) + `, "role": "owner"}, "n": 2.50, "tags": ["a"]}`},
		{"a value of a non-personal key sealed", `{"after": {"email": "old@example.org", "role": ` + ref(valueA) + `}, "n": 2.50, "tags": ["a"]}`},
		{"an unknown subject", `{"after": {"email": ` + ref("00000000-0000-4000-8000-0000000006a0") + `, "role": "admin"}, "n": 2.50, "tags": ["a"]}`},
		{"a subject of another organization", `{"after": {"email": ` + ref(valueB) + `, "role": "admin"}, "n": 2.50, "tags": ["a"]}`},
		{"a contact subject", `{"after": {"email": ` + ref(contactA) + `, "role": "admin"}, "n": 2.50, "tags": ["a"]}`},
		{"no uuid", `{"after": {"email": {"$pii": "x"}, "role": "admin"}, "n": 2.50, "tags": ["a"]}`},
		{"a changed number text", `{"after": {"email": ` + ref(valueA) + `, "role": "admin"}, "n": 2.5, "tags": ["a"]}`},
		{"a removed key", `{"after": {"email": ` + ref(valueA) + `, "role": "admin"}, "n": 2.50}`},
		{"an added key", `{"after": {"email": ` + ref(valueA) + `, "role": "admin"}, "n": 2.50, "tags": ["a"], "x": 1}`},
		{"a reference with a second key", `{"after": {"email": {"$pii": "` + valueA + `", "v": 1}, "role": "admin"}, "n": 2.50, "tags": ["a"]}`},
		{"nothing sealed", `{"after": {"email": "old@example.org", "role": "admin"}, "n": 2.50, "tags": ["a"]}`},
	} {
		if _, serr := seal(f.OrgA, first, tc.changes); serr == nil || !strings.Contains(serr.Error(), "append-only") {
			t.Errorf("seal with %s: %v, want append-only refusal", tc.name, serr)
		}
	}
	if found, serr := seal(f.OrgA, foreign, `{"email": `+ref(valueA)+`}`); serr != nil || found {
		t.Errorf("seal of an entry of organization B from A: found %v, %v; want not found", found, serr)
	}
	if got := changesOf(foreign); got != `{"email": "b@example.org"}` {
		t.Errorf("entry of organization B = %s", got)
	}
	if !slices.Equal(chain(), original) {
		t.Fatalf("a refused seal changed the chain:\n%v\nwant\n%v", chain(), original)
	}

	// An entry sealed in part, then by the backfill run: hashes stay.
	var part map[string]any
	if err = inOrg(f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		var serr error
		part, serr = pii.SealValueTx(ctx, tx, f.OrgA, "x@example.org")
		return serr
	}); err != nil {
		t.Fatal(err)
	}
	partID, _ := pii.RefID(part)
	if found, serr := seal(f.OrgA, third, `{"email": `+ref(partID)+`, "user_name": "xuser"}`); serr != nil || !found {
		t.Fatalf("partial seal: %v, %v", found, serr)
	}
	var digest string
	if err = f.Admin.QueryRow(bg, `SELECT chained_digest FROM audit_log WHERE id = $1`, third).Scan(&digest); err != nil || digest == "" {
		t.Fatalf("chained digest after the partial seal: %q, %v", digest, err)
	}
	res, err := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA)
	if err != nil || res.Audit != 3 {
		t.Fatalf("backfill = %+v, %v; want 3 audit entries", res, err)
	}
	sealed := chain()
	for i := range original {
		if sealed[i].id != original[i].id || sealed[i].prev != original[i].prev || sealed[i].hash != original[i].hash {
			t.Errorf("entry %d re-chained by the backfill: %+v, was %+v", i+1, sealed[i], original[i])
		}
		for _, plain := range []string{"old@example", "Max Muster", "+49", "x@example", "xuser"} {
			if strings.Contains(sealed[i].changes, plain) {
				t.Errorf("entry %d keeps %q: %s", i+1, plain, sealed[i].changes)
			}
		}
	}
	if !strings.Contains(sealed[0].changes, `"n": 2.50`) || !strings.Contains(sealed[0].changes, `"role": "admin"`) {
		t.Errorf("sealed entry 1 changed other values: %s", sealed[0].changes)
	}
	var keptDigest string
	var refs []string
	if err = f.Admin.QueryRow(bg, `SELECT chained_digest, sealed_refs FROM audit_log WHERE id = $1`, third).Scan(&keptDigest, &refs); err != nil ||
		keptDigest != digest || len(refs) != 2 || refs[0] != partID {
		t.Errorf("entry sealed twice: digest %q (first %q), refs %v, %v", keptDigest, digest, refs, err)
	}
	if again, aerr := jobs.NewPIIBackfill(f.App).RunOrg(bg, f.OrgA); aerr != nil || again.Total() != 0 {
		t.Errorf("second backfill = %+v, %v", again, aerr)
	}
	if res := verify(); !res.Intact || res.Checked != 5 || res.Sealed != 3 || res.Unconfirmed != 0 {
		t.Errorf("chain after the backfill (with vault) = %+v; want intact, 5 checked, 3 sealed, all confirmed", res)
	}
	pii.SetDefault(nil)
	if res := verify(); !res.Intact || res.Sealed != 3 || res.Unconfirmed != 3 {
		t.Errorf("chain after the backfill (without vault) = %+v; want intact, 3 sealed, none confirmed", res)
	}
	pii.SetDefault(vault)

	// A value changed in the vault no longer gives back the original.
	var emailRef string
	if err = f.Admin.QueryRow(bg, `SELECT changes -> 'after' -> 'email' ->> '$pii' FROM audit_log WHERE id = $1`, first).Scan(&emailRef); err != nil {
		t.Fatal(err)
	}
	if err = vault.Update(f.OrgCtx(f.OrgA), f.OrgA, emailRef, pii.Data{"v": "evil@example.org"}); err != nil {
		t.Fatal(err)
	}
	if res := verify(); res.Intact || res.BrokenID != first || res.BrokenReason != "sealed value differs from the original" {
		t.Errorf("chain after changing a sealed value in the vault = %+v; want broken at %s", res, first)
	}
	if err = vault.Update(f.OrgCtx(f.OrgA), f.OrgA, emailRef, pii.Data{"v": "old@example.org"}); err != nil {
		t.Fatal(err)
	}
	if res := verify(); !res.Intact || res.Unconfirmed != 0 {
		t.Errorf("chain with the value restored = %+v", res)
	}
	// A deleted value (erasure) leaves the entry unconfirmed, not broken.
	if err = vault.Delete(f.OrgCtx(f.OrgA), f.OrgA, emailRef); err != nil {
		t.Fatal(err)
	}
	if res := verify(); !res.Intact || res.Sealed != 3 || res.Unconfirmed != 1 {
		t.Errorf("chain after deleting a sealed value = %+v; want intact with 1 unconfirmed", res)
	}

	// A sealed entry changed after its seal (bypassing the guard) breaks the
	// chain there, with and without the vault.
	conn, err := f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(bg, `SET session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	_, err = conn.Exec(bg, `UPDATE audit_log SET changes = jsonb_set(changes, '{phones}', '{}') WHERE id = $1`, second)
	_, _ = conn.Exec(bg, `SET session_replication_role = origin`)
	if err != nil {
		t.Fatal(err)
	}
	for _, withVault := range []bool{true, false} {
		if !withVault {
			pii.SetDefault(nil)
		}
		if res := verify(); res.Intact || res.BrokenID != second || res.BrokenReason != "sealed entry changed after its seal" {
			t.Errorf("chain after changing a sealed entry (vault %v) = %+v; want broken at %s", withVault, res, second)
		}
	}
}
