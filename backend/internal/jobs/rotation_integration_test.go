package jobs_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

type dekAdapter struct{ repo *credential.PGRepository }

func (d dekAdapter) RotateOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (int, int, error) {
	res, err := d.repo.RotateOrgDEK(ctx, orgID, enc)
	if err != nil {
		return 0, 0, err
	}
	return res.KeyVersion, res.Rotated, nil
}

func (d dekAdapter) RewrapOrgDEK(ctx context.Context, orgID string, enc *crypto.EnvelopeEncryptor) (bool, error) {
	return d.repo.RewrapOrgDEK(ctx, orgID, enc)
}

// TestKeyRotationAgainstPostgres covers SEC-06 (WP-141) against
// PostgreSQL:
//   - master key rotation: after the rewrap run every data key (org DEK,
//     notification channel key) and the derivation root (platform_secret,
//     WP-459) use the new master key, so the previous key can be retired and
//     everything stays readable; the secrets derived from the root are the
//     same before and after, the old key alone no longer opens the root;
//   - the yearly org DEK rotation re-encrypts the credentials under a new
//     DEK with the next key version, audited, and leaves younger DEKs alone;
//   - the overlap of a rotated webhook secret ends after its time.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestKeyRotationAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "9d")
	bg := context.Background()
	// The derivation root goes with the master key this test draws.
	cleanRoot := func() { _, _ = f.Admin.Exec(bg, `DELETE FROM platform_secret`) }
	cleanRoot()
	t.Cleanup(cleanRoot)
	oldKey, _ := crypto.GenerateMasterKey()
	newKey, _ := crypto.GenerateMasterKey()
	oldEnc, _ := crypto.NewEnvelopeEncryptor(oldKey)
	if created, err := crypto.LoadDerivationRoot(bg, f.App, oldEnc); err != nil || !created {
		t.Fatalf("derivation root at the first start: created %t, %v", created, err)
	}
	derivedBefore := oldEnc.Derive("reticora/nats-edge/issuer")
	var sealedBefore []byte
	if err := f.Admin.QueryRow(bg, `SELECT sealed FROM platform_secret`).Scan(&sealedBefore); err != nil {
		t.Fatal(err)
	}
	credRepo := credential.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	cred, err := credential.NewService(credRepo, oldEnc).Create(ctx, credential.CreateRequest{OrganizationID: f.OrgA, Name: "snmp",
		Kind: "snmp_v2c", Secret: map[string]any{"community": "s3cret"}})
	if err != nil {
		t.Fatal(err)
	}
	channels := notify.NewPGRepository(f.App, notify.NewSealer(oldEnc))
	ch := &notify.Channel{OrganizationID: f.OrgA, Name: "own", Provider: notify.ProviderSMTP, Enabled: true,
		Config: map[string]any{"host": "mail.example.org", "from": "a@example.org", "username": "u", "password": "pw"}}
	if err = channels.Create(bg, ch); err != nil {
		t.Fatal(err)
	}

	// Master key rotation.
	rotating, _ := crypto.NewEnvelopeEncryptor(newKey, oldKey)
	hooks := webhook.NewPGRepository(f.App)
	if _, err = crypto.LoadDerivationRoot(bg, f.App, rotating); err != nil {
		t.Fatalf("derivation root with the previous key: %v", err)
	}
	rot := jobs.NewRotation(f.App, rotating, dekAdapter{repo: credRepo}, hooks)
	n, err := rot.RewrapMasterKey(bg)
	if err != nil || n < 3 {
		t.Fatalf("rewrap: %d %v", n, err)
	}
	if again, _ := rot.RewrapMasterKey(bg); again != 0 {
		t.Errorf("second rewrap changed %d", again)
	}
	retired, _ := crypto.NewEnvelopeEncryptor(newKey)
	if secret, decErr := credential.NewService(credRepo, retired).Decrypt(ctx, f.OrgA, cred.ID); decErr != nil || secret["community"] != "s3cret" {
		t.Errorf("credential after retiring the old key: %v %v", secret, decErr)
	}
	if got, getErr := notify.NewPGRepository(f.App, notify.NewSealer(retired)).Get(bg, f.OrgA, ch.ID); getErr != nil || got.Config["password"] != "pw" {
		t.Errorf("channel after retiring the old key: %v", getErr)
	}
	// The root is the same one, sealed with the new key: the new key alone
	// opens it and derives the same secrets, the old key alone no longer.
	var sealedAfter []byte
	if err = f.Admin.QueryRow(bg, `SELECT sealed FROM platform_secret`).Scan(&sealedAfter); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(sealedAfter, sealedBefore) || !retired.IsCurrent(sealedAfter) {
		t.Error("the derivation root was not resealed with the new master key")
	}
	if created, rootErr := crypto.LoadDerivationRoot(bg, f.App, retired); rootErr != nil || created ||
		!bytes.Equal(retired.Derive("reticora/nats-edge/issuer"), derivedBefore) {
		t.Errorf("derivation root after retiring the old key: created %t, %v, or other derived secrets", created, rootErr)
	}
	oldOnly, _ := crypto.NewEnvelopeEncryptor(oldKey)
	if _, rootErr := crypto.LoadDerivationRoot(bg, f.App, oldOnly); !errors.Is(rootErr, crypto.ErrDerivationRoot) {
		t.Errorf("derivation root with the retired key alone: %v, want ErrDerivationRoot", rootErr)
	}

	// The yearly org DEK rotation.
	retiredRot := jobs.NewRotation(f.App, retired, dekAdapter{repo: credRepo}, hooks)
	if n, err = retiredRot.RotateOrgDEKs(bg); err != nil || n != 0 {
		t.Errorf("young DEK rotated: %d %v", n, err)
	}
	if _, err = f.Admin.Exec(bg, `UPDATE org_dek SET created_at = now() - interval '400 days' WHERE organization_id = $1`, f.OrgA); err != nil {
		t.Fatal(err)
	}
	var oldWrapped []byte
	if err = f.Admin.QueryRow(bg, `SELECT encrypted_dek FROM org_dek WHERE organization_id = $1`, f.OrgA).Scan(&oldWrapped); err != nil {
		t.Fatal(err)
	}
	if n, err = retiredRot.RotateOrgDEKs(bg); err != nil || n != 1 {
		t.Fatalf("org DEK rotation: %d %v", n, err)
	}
	var dekVersion, credVersion, audited int
	var newWrapped []byte
	if err = f.Admin.QueryRow(bg, `SELECT d.key_version, d.encrypted_dek, c.key_version,
		(SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = 'key.org_dek_rotated')
		FROM org_dek d JOIN credential c ON c.organization_id = d.organization_id WHERE d.organization_id = $1`, f.OrgA).
		Scan(&dekVersion, &newWrapped, &credVersion, &audited); err != nil {
		t.Fatal(err)
	}
	if dekVersion != 2 || credVersion != 2 || audited != 1 || string(newWrapped) == string(oldWrapped) {
		t.Errorf("after rotation: dek %d, credential %d, audited %d", dekVersion, credVersion, audited)
	}
	if secret, decErr := credential.NewService(credRepo, retired).Decrypt(ctx, f.OrgA, cred.ID); decErr != nil || secret["community"] != "s3cret" {
		t.Errorf("credential after the DEK rotation: %v %v", secret, decErr)
	}
	if n, _ = retiredRot.RotateOrgDEKs(bg); n != 0 {
		t.Errorf("rotated again: %d", n)
	}

	// Webhook secret overlap.
	sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "hook", URL: "https://hooks.example.org/x", Secret: "first-secret-123",
		Events: []string{"ci.created"}, IsActive: true}
	if err = hooks.Create(ctx, sub); err != nil {
		t.Fatal(err)
	}
	rotated, err := hooks.RotateSecret(bg, f.OrgA, sub.ID, "second-secret-456", time.Now().Add(-time.Minute))
	if err != nil || rotated.Secret != "second-secret-456" || rotated.PreviousSecret != "first-secret-123" {
		t.Fatalf("rotate secret: %+v %v", rotated, err)
	}
	if cleared, clearErr := hooks.ClearExpiredSecrets(bg, time.Now()); clearErr != nil || cleared != 1 {
		t.Errorf("cleared %d (%v)", cleared, clearErr)
	}
	if got, _ := hooks.GetByID(ctx, f.OrgA, sub.ID); got.PreviousSecret != "" || got.PreviousSecretExpiresAt != nil || got.Secret != "second-secret-456" {
		t.Errorf("after the overlap: %+v", got)
	}
	if _, err = hooks.RotateSecret(bg, f.OrgA, sub.ID, "third-secret-789", time.Now().Add(webhook.SecretOverlap)); err != nil {
		t.Fatal(err)
	}
	if cleared, _ := hooks.ClearExpiredSecrets(bg, time.Now()); cleared != 0 {
		t.Errorf("an overlap ended early: %d", cleared)
	}
}

// dekState is what an organization's data key protects, read as stored:
// the wrapped DEK with its version and rotation time, the ciphertexts of
// the credentials and of the PII vault with their key versions and the
// blind index of the vault.
type dekState struct {
	wrapped     []byte
	version     int
	rotatedAt   time.Time
	credentials map[string]storedCipher
	subjects    map[string]storedCipher
}

type storedCipher struct {
	ciphertext []byte
	version    int
	tokens     string
}

func readDEKState(t *testing.T, f *scopetest.Fixture, orgID string) dekState {
	t.Helper()
	bg := context.Background()
	s := dekState{credentials: map[string]storedCipher{}, subjects: map[string]storedCipher{}}
	if err := f.Admin.QueryRow(bg, `SELECT encrypted_dek, key_version, created_at FROM org_dek WHERE organization_id = $1`, orgID).
		Scan(&s.wrapped, &s.version, &s.rotatedAt); err != nil {
		t.Fatalf("org_dek of %s: %v", orgID, err)
	}
	read := func(into map[string]storedCipher, query string) {
		rows, err := f.Admin.Query(bg, query, orgID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var c storedCipher
			if err = rows.Scan(&id, &c.ciphertext, &c.version, &c.tokens); err != nil {
				t.Fatal(err)
			}
			into[id] = c
		}
		if err = rows.Err(); err != nil {
			t.Fatal(err)
		}
	}
	read(s.credentials, `SELECT id::text, ciphertext, key_version, '' FROM credential WHERE organization_id = $1`)
	read(s.subjects, `SELECT surrogate_id::text, encrypted_payload, dek_ref, COALESCE(search_tokens::text, '')
		FROM pii_subject WHERE organization_id = $1`)
	return s
}

// dekStateDiff reports how b differs from a; empty when nothing changed.
func dekStateDiff(a, b dekState) string {
	switch {
	case !bytes.Equal(a.wrapped, b.wrapped) || a.version != b.version || !a.rotatedAt.Equal(b.rotatedAt):
		return fmt.Sprintf("org_dek changed (version %d → %d)", a.version, b.version)
	case len(a.credentials) != len(b.credentials) || len(a.subjects) != len(b.subjects):
		return "rows added or removed"
	}
	for id, c := range a.credentials {
		if d := b.credentials[id]; !bytes.Equal(c.ciphertext, d.ciphertext) || c.version != d.version {
			return "credential " + id + " changed"
		}
	}
	for id, c := range a.subjects {
		if d := b.subjects[id]; !bytes.Equal(c.ciphertext, d.ciphertext) || c.version != d.version || c.tokens != d.tokens {
			return "pii subject " + id + " changed"
		}
	}
	return ""
}

// openedBy splits the ciphertexts of s into those dek decrypts and those
// it does not.
func openedBy(s dekState, dek []byte) (opened, closed []string) {
	try := func(name string, ciphertext []byte) {
		if _, err := crypto.Decrypt(dek, ciphertext); err == nil {
			opened = append(opened, name)
		} else {
			closed = append(closed, name)
		}
	}
	for id, c := range s.credentials {
		try("credential "+id, c.ciphertext)
	}
	for id, c := range s.subjects {
		try("pii subject "+id, c.ciphertext)
	}
	return opened, closed
}

// injectFault installs a trigger that lets the first `after` updates of the
// rows of organization orgID in table pass within one transaction and fails
// the next one (fault injection in the middle of the rewrap); remove drops
// it.
func injectFault(t *testing.T, f *scopetest.Fixture, table, orgID string, after int) (remove func()) {
	t.Helper()
	bg := context.Background()
	fn := fmt.Sprintf("wp547_fault_%s_%d", table, time.Now().UnixNano())
	if _, err := f.Admin.Exec(bg, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
	n int := COALESCE(NULLIF(current_setting('wp547.fault_rows', true), ''), '0')::int + 1;
BEGIN
	PERFORM set_config('wp547.fault_rows', n::text, true);
	IF n > %d THEN
		RAISE EXCEPTION 'wp547: injected fault after %% rows of %s', n - 1;
	END IF;
	RETURN NEW;
END $$`, fn, after, table)); err != nil {
		t.Fatalf("fault function: %v", err)
	}
	removed := false
	remove = func() {
		if removed {
			return
		}
		removed = true
		if _, err := f.Admin.Exec(context.Background(), `DROP FUNCTION IF EXISTS `+fn+`() CASCADE`); err != nil {
			t.Errorf("drop fault trigger: %v", err)
		}
	}
	t.Cleanup(remove)
	if _, err := f.Admin.Exec(bg, fmt.Sprintf(`CREATE TRIGGER %s BEFORE UPDATE ON %s FOR EACH ROW
		WHEN (NEW.organization_id = '%s'::uuid) EXECUTE FUNCTION %s()`, fn, table, orgID, fn)); err != nil {
		t.Fatalf("fault trigger: %v", err)
	}
	return remove
}

// TestRotateOrgDEKIsAtomic covers the atomicity of the yearly org DEK
// rotation (SEC-06, WP-141, WP-547) against PostgreSQL, as the runtime role
// in the tenant context of the organization: a fault in the middle of the
// re-encryption (after two of four credentials, after one of two PII vault
// entries, at the replacement of the DEK after every row) fails the
// rotation and rolls the whole transaction back: DEK, key versions, every
// ciphertext and the blind index are as before and open with the old DEK.
// A run without a fault re-encrypts every credential and vault entry under
// a new DEK with the next key version; the old DEK opens none of them and
// is no longer stored (retired). Organization B is untouched throughout.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestRotateOrgDEKIsAtomic' ./internal/jobs/
func TestRotateOrgDEKIsAtomic(t *testing.T) {
	f := scopetest.Seed(t, "9d")
	bg := context.Background()
	key, _ := crypto.GenerateMasterKey()
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repo := credential.NewPGRepository(f.App)
	creds := credential.NewService(repo, enc)
	vault := pii.NewVault(f.App, enc)
	secrets := map[string]map[string]string{}
	people := map[string]map[string]string{}
	for _, org := range []string{f.OrgA, f.OrgB} {
		secrets[org], people[org] = map[string]string{}, map[string]string{}
		n := 4
		if org == f.OrgB {
			n = 2
		}
		for i := 0; i < n; i++ {
			community := fmt.Sprintf("community-%s-%d", org[:8], i)
			c, cerr := creds.Create(f.OrgCtx(org), credential.CreateRequest{OrganizationID: org, Name: fmt.Sprintf("snmp-%d", i),
				Kind: "snmp_v2c", Secret: map[string]any{"community": community}})
			if cerr != nil {
				t.Fatal(cerr)
			}
			secrets[org][c.ID] = community
		}
		for i := 0; i < 2; i++ {
			name := fmt.Sprintf("Person %s %d", org[:8], i)
			id, verr := vault.Create(f.OrgCtx(org), org, pii.KindContact, pii.Data{"name": name, "email": fmt.Sprintf("p%d@example.org", i)})
			if verr != nil {
				t.Fatal(verr)
			}
			people[org][id] = name
		}
	}
	// readable checks through the application path (credential service,
	// vault) that every secret and person of org opens with the stored DEK.
	readable := func(when, org string) {
		t.Helper()
		for id, want := range secrets[org] {
			if got, derr := creds.Decrypt(f.OrgCtx(org), org, id); derr != nil || got["community"] != want {
				t.Errorf("%s: credential %s of %s: %v %v", when, id, org, got, derr)
			}
		}
		for id, want := range people[org] {
			if got, rerr := vault.Resolve(f.OrgCtx(org), org, id); rerr != nil || got.Data["name"] != want {
				t.Errorf("%s: pii subject %s of %s: %v", when, id, org, rerr)
			}
		}
	}
	beforeA, beforeB := readDEKState(t, f, f.OrgA), readDEKState(t, f, f.OrgB)
	if len(beforeA.credentials) != 4 || len(beforeA.subjects) != 2 || len(beforeB.credentials) != 2 || len(beforeB.subjects) != 2 {
		t.Fatalf("fixture: A %d credentials %d subjects, B %d/%d", len(beforeA.credentials), len(beforeA.subjects),
			len(beforeB.credentials), len(beforeB.subjects))
	}
	oldDEK, err := enc.UnwrapDEK(beforeA.wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if _, closed := openedBy(beforeA, oldDEK); len(closed) > 0 {
		t.Fatalf("fixture: the DEK of A does not open %v", closed)
	}

	// Faults in the middle of the rewrap.
	for _, fault := range []struct {
		name, table string
		after       int
	}{
		{"after two of four credentials", "credential", 2},
		{"after one of two PII vault entries", "pii_subject", 1},
		{"at the replacement of the DEK after every row", "org_dek", 0},
	} {
		remove := injectFault(t, f, fault.table, f.OrgA, fault.after)
		res, rerr := repo.RotateOrgDEK(bg, f.OrgA, enc)
		remove()
		if rerr == nil || !strings.Contains(rerr.Error(), "wp547: injected fault") {
			t.Fatalf("%s: rotation %+v, error %v; want the injected fault", fault.name, res, rerr)
		}
		afterA := readDEKState(t, f, f.OrgA)
		if diff := dekStateDiff(beforeA, afterA); diff != "" {
			t.Errorf("%s: not rolled back: %s", fault.name, diff)
		}
		if _, closed := openedBy(afterA, oldDEK); len(closed) > 0 {
			t.Errorf("%s: the old DEK no longer opens %v", fault.name, closed)
		}
		if stored, uerr := enc.UnwrapDEK(afterA.wrapped); uerr != nil || !bytes.Equal(stored, oldDEK) {
			t.Errorf("%s: the stored DEK is not the old one (%v)", fault.name, uerr)
		}
		readable(fault.name, f.OrgA)
		if diff := dekStateDiff(beforeB, readDEKState(t, f, f.OrgB)); diff != "" {
			t.Errorf("%s: organization B: %s", fault.name, diff)
		}
	}

	// The rotation without a fault.
	res, err := repo.RotateOrgDEK(bg, f.OrgA, enc)
	if err != nil {
		t.Fatalf("rotation: %v", err)
	}
	if res.KeyVersion != beforeA.version+1 || res.Rotated != 4 || res.Subjects != 2 {
		t.Errorf("rotation result %+v, want version %d, 4 credentials, 2 subjects", res, beforeA.version+1)
	}
	afterA := readDEKState(t, f, f.OrgA)
	newDEK, err := enc.UnwrapDEK(afterA.wrapped)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(newDEK, oldDEK) || afterA.version != beforeA.version+1 || !afterA.rotatedAt.After(beforeA.rotatedAt) {
		t.Errorf("org_dek after the rotation: new key %t, version %d, rotated at %s (before %s)",
			!bytes.Equal(newDEK, oldDEK), afterA.version, afterA.rotatedAt, beforeA.rotatedAt)
	}
	if _, closed := openedBy(afterA, newDEK); len(closed) > 0 {
		t.Errorf("the new DEK does not open %v", closed)
	}
	if opened, _ := openedBy(afterA, oldDEK); len(opened) > 0 {
		t.Errorf("the old DEK still opens %v: it is not retired", opened)
	}
	for id, c := range afterA.credentials {
		if c.version != afterA.version {
			t.Errorf("credential %s has key version %d, want %d", id, c.version, afterA.version)
		}
	}
	for id, c := range afterA.subjects {
		if c.version != afterA.version || c.tokens == beforeA.subjects[id].tokens {
			t.Errorf("pii subject %s: dek_ref %d (want %d), blind index renewed %t", id, c.version, afterA.version,
				c.tokens != beforeA.subjects[id].tokens)
		}
	}
	readable("after the rotation", f.OrgA)
	if diff := dekStateDiff(beforeB, readDEKState(t, f, f.OrgB)); diff != "" {
		t.Errorf("after the rotation: organization B: %s", diff)
	}
	readable("after the rotation", f.OrgB)
}
