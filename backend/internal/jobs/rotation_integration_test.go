package jobs_test

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
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
