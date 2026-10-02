package rls_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/jackc/pgx/v5"
)

// TestSystemWorkersPerOrganization covers the background workers after
// WP-022 (TEN-05, TEN-06, OPS-05): the system flag only finds work across
// tenants, the work itself runs per organization. The webhook retry claim,
// the export claim, the reservation sweeper and the enrollment-code
// redemption each process two organizations, and a system transaction
// cannot change any row.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSystemWorkersPerOrganization(t *testing.T) {
	f := scopetest.Seed(t, "41")
	bg := context.Background()
	orgs := []string{f.OrgA, f.OrgB}
	past := time.Now().UTC().Add(-time.Minute)

	// Webhook deliveries due in both organizations.
	deliveries := webhook.NewPGDeliveryStore(f.App)
	for _, org := range orgs {
		var subID string
		if err := f.Admin.QueryRow(bg,
			`INSERT INTO webhook_subscription (organization_id, name, url, secret) VALUES ($1, 's', 'https://hook.example.invalid', 's') RETURNING id::text`,
			org).Scan(&subID); err != nil {
			t.Fatalf("seed subscription: %v", err)
		}
		if _, err := deliveries.Enqueue(bg, webhook.DeliveryRecord{OrganizationID: org, SubscriptionID: subID, Event: "ci.created", Payload: []byte(`{}`), Status: "pending", NextRetryAt: &past}); err != nil {
			t.Fatalf("enqueue delivery for %s: %v", org, err)
		}
	}
	claimed, claimErr := deliveries.ClaimDue(bg, time.Now().UTC(), 100, time.Minute)
	if claimErr != nil {
		t.Fatalf("claim deliveries: %v", claimErr)
	}
	if got := orgsOf(claimed, func(r webhook.DeliveryRecord) string { return r.OrganizationID }); !got[f.OrgA] || !got[f.OrgB] {
		t.Fatalf("claimed deliveries of %v, want both organizations", got)
	}

	// Export jobs pending in both organizations.
	jobs := export.NewPGJobRepository(f.App)
	for _, org := range orgs {
		if err := jobs.CreateJob(f.OrgCtx(org), org, &export.Job{Format: "csv", InitiatedBy: f.User, Scope: &export.JobScope{}}); err != nil {
			t.Fatalf("create export job for %s: %v", org, err)
		}
	}
	pending, pendErr := jobs.ClaimPending(bg, 100)
	if pendErr != nil {
		t.Fatalf("claim export jobs: %v", pendErr)
	}
	if got := orgsOf(pending, func(j export.Job) string { return j.OrganizationID }); !got[f.OrgA] || !got[f.OrgB] {
		t.Fatalf("claimed export jobs of %v, want both organizations", got)
	}

	// Overdue reservations in both organizations.
	for _, org := range orgs {
		assetID := f.Asset(t, org, "", "scopetest-41-"+org[len(org)-4:])
		if _, err := f.Admin.Exec(bg,
			`INSERT INTO reservation (organization_id, item_kind, asset_id, quantity, state, expires_at) VALUES ($1, 'asset', $2, 1, 'active', $3)`,
			org, assetID, past); err != nil {
			t.Fatalf("seed reservation: %v", err)
		}
	}
	reservation.NewSweeper(reservation.NewPGRepository(f.App), nil, time.Minute).SweepOnce(bg)
	var active int
	if err := f.Admin.QueryRow(bg,
		`SELECT count(*) FROM reservation WHERE organization_id = ANY($1::uuid[]) AND state = 'active'`, orgs).Scan(&active); err != nil {
		t.Fatalf("count reservations: %v", err)
	}
	if active != 0 {
		t.Fatalf("%d overdue reservations still active after the sweep", active)
	}

	// Enrollment codes of both organizations are redeemed exactly once.
	codes := discovery.NewPGRepository(f.App)
	for i, org := range orgs {
		raw := "scopetest-41-code-" + string(rune('a'+i))
		sum := sha256.Sum256([]byte(raw))
		if _, err := f.Admin.Exec(bg,
			`INSERT INTO collector_enrollment_code (organization_id, code_hash, expires_at) VALUES ($1, $2, now() + interval '1 hour')`,
			org, hex.EncodeToString(sum[:])); err != nil {
			t.Fatalf("seed enrollment code: %v", err)
		}
		got, err := codes.RedeemEnrollmentCode(bg, raw, "")
		if err != nil || got != org {
			t.Fatalf("redeem code of %s: org=%s err=%v", org, got, err)
		}
		if _, err := codes.RedeemEnrollmentCode(bg, raw, ""); err == nil {
			t.Fatalf("code of %s redeemed twice", org)
		}
	}

	// The system flag reads across tenants but writes nothing.
	if err := database.WithSystem(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE export_job SET status = 'failed'`)
		return err
	}); err == nil {
		t.Fatal("a system transaction changed export jobs")
	}
	tx, beginErr := f.App.Begin(bg)
	if beginErr != nil {
		t.Fatalf("begin: %v", beginErr)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err := tx.Exec(bg, `SELECT set_config('app.system', 'on', true), set_config('app.org_id', '', true)`); err != nil {
		t.Fatalf("set system flag: %v", err)
	}
	for _, table := range []string{"webhook_delivery", "webhook_dead_letter", "export_job", "alert_rule", "collector_enrollment_code"} {
		tag, err := tx.Exec(bg, `UPDATE `+table+` SET organization_id = organization_id WHERE organization_id = ANY($1::uuid[])`, orgs)
		if err != nil {
			t.Fatalf("update %s under the system flag: %v", table, err)
		}
		if tag.RowsAffected() != 0 {
			t.Fatalf("system flag updated %d rows of %s", tag.RowsAffected(), table)
		}
	}
}

func orgsOf[T any](items []T, org func(T) string) map[string]bool {
	out := map[string]bool{}
	for _, it := range items {
		out[org(it)] = true
	}
	return out
}
