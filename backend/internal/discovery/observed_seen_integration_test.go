package discovery_test

import (
	"context"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// seenFixture runs the ingest route on PostgreSQL for organization A of the
// fixture, with max_cis enforced by limits when it is set; seen reads
// first_seen_at, last_seen_at and status of a CI by serial number.
type seenFixture struct {
	f      *scopetest.Fixture
	mux    *chi.Mux
	ctx    context.Context
	limits *entitlement.Service
}

func newSeenFixture(t *testing.T, withLimits bool) *seenFixture {
	t.Helper()
	f := scopetest.Seed(t, "82")
	s := &seenFixture{f: f, ctx: tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})}
	h := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))
	if withLimits {
		s.limits = entitlement.NewService(entitlement.NewPGRepository(f.App), entitlement.Options{Enforce: true})
		h.WithLimits(s.limits)
		s.limits.WithUnlicensed(h)
	}
	s.mux = chi.NewRouter()
	h.RegisterRoutes(s.mux)
	return s
}

func (s *seenFixture) record(serial, ip, source string, observed time.Time) plugins.DeviceRecord {
	return plugins.RecordFromResult(source, &plugins.Result{CIType: "server", Name: "seen-" + serial, IP: ip, Serial: serial}, observed)
}

func (s *seenFixture) seen(t *testing.T, serial string) (first, last time.Time, status string) {
	t.Helper()
	if err := s.f.Admin.QueryRow(context.Background(), `SELECT first_seen_at, last_seen_at, status FROM ci
		WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, s.f.OrgA, serial).Scan(&first, &last, &status); err != nil {
		t.Fatalf("CI %s: %v", serial, err)
	}
	return first, last, status
}

// TestSightingDatedByObservedAt is the retest of F-245 (WP-580, GLO-12,
// REC-07) against PostgreSQL: a spooled record observed 20 hours ago
// creates its CI with first_seen_at and last_seen_at equal to its
// observed_at, not the arrival time; a record observed earlier than the last
// sighting does not set last_seen_at back and brings no unknown CI back to
// active; a later record moves last_seen_at to its observed_at.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSightingDatedByObservedAt(t *testing.T) {
	s := newSeenFixture(t, false)
	observed := time.Now().UTC().Add(-20 * time.Hour).Truncate(time.Microsecond)

	if resp := ingestRecords(t, s.mux, s.ctx, s.record("SEEN-580", "192.0.2.180", "snmp", observed)); resp.Created != 1 {
		t.Fatalf("spooled record: %+v, want 1 created", resp)
	}
	first, last, _ := s.seen(t, "SEEN-580")
	if !first.Equal(observed) || !last.Equal(observed) {
		t.Fatalf("new CI: first_seen_at %v, last_seen_at %v, want both the observed_at %v (age %v)",
			first, last, observed, time.Since(last).Round(time.Second))
	}

	// An older record: last_seen_at stays.
	if resp := ingestRecords(t, s.mux, s.ctx, s.record("SEEN-580", "192.0.2.180", "snmp", observed.Add(-10*time.Hour))); resp.Updated != 1 {
		t.Fatalf("older record: %+v, want 1 updated", resp)
	}
	if first, last, _ = s.seen(t, "SEEN-580"); !first.Equal(observed) || !last.Equal(observed) {
		t.Errorf("after an older record: first_seen_at %v, last_seen_at %v, want both %v", first, last, observed)
	}

	// An unknown CI stays unknown for a record older than its last sighting.
	if _, err := s.f.Admin.Exec(context.Background(), `UPDATE ci SET status = 'unknown' WHERE organization_id = $1 AND serial_number = 'SEEN-580'`,
		s.f.OrgA); err != nil {
		t.Fatal(err)
	}
	ingestRecords(t, s.mux, s.ctx, s.record("SEEN-580", "192.0.2.180", "snmp", observed.Add(-time.Hour)))
	if _, last, status := s.seen(t, "SEEN-580"); status != "unknown" || !last.Equal(observed) {
		t.Errorf("after an older record on an unknown CI: status %q, last_seen_at %v, want unknown and %v", status, last, observed)
	}

	// A later record: last_seen_at is its observed_at, the CI is active.
	later := observed.Add(2 * time.Hour)
	ingestRecords(t, s.mux, s.ctx, s.record("SEEN-580", "192.0.2.180", "snmp", later))
	if first, last, status := s.seen(t, "SEEN-580"); !first.Equal(observed) || !last.Equal(later) || status != "active" {
		t.Errorf("after a later record: first_seen_at %v, last_seen_at %v, status %q, want %v, %v, active", first, last, status, observed, later)
	}
}

// TestHeldDeviceFirstSeenAtObservedAt is the retest of F-245 on the
// unlicensed path (WP-580, GLO-12, ENT-03): a device held as unlicensed_ci
// at max_cis becomes a CI when the limit is raised, first and last seen at
// the observed_at of its record, not at the adoption.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHeldDeviceFirstSeenAtObservedAt(t *testing.T) {
	s := newSeenFixture(t, true)
	bg := context.Background()
	setLimit := func(limit int64) {
		t.Helper()
		if _, err := s.limits.Grant(s.f.OrgCtx(s.f.OrgA), entitlement.Entitlement{OrganizationID: s.f.OrgA, FeatureKey: entitlement.FeatureCMDBCore,
			Plan: entitlement.PlanEssential, Enabled: true, Limits: map[string]int64{entitlement.LimitMaxCIs: limit}}); err != nil {
			t.Fatal(err)
		}
	}
	var base int64
	if err := s.f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, s.f.OrgA).Scan(&base); err != nil {
		t.Fatal(err)
	}
	setLimit(base)
	observed := time.Now().UTC().Add(-20 * time.Hour).Truncate(time.Microsecond)
	if resp := ingestRecords(t, s.mux, s.ctx, s.record("HELD-580", "192.0.2.183", "snmp", observed)); resp.Unlicensed != 1 || resp.Created != 0 {
		t.Fatalf("record at max_cis: %+v, want 1 unlicensed", resp)
	}
	setLimit(base + 1)
	first, last, _ := s.seen(t, "HELD-580")
	if !first.Equal(observed) || !last.Equal(observed) {
		t.Errorf("adopted CI: first_seen_at %v, last_seen_at %v, want both the observed_at %v", first, last, observed)
	}
}

// TestSerialNumberMatchesCaseInsensitively is the retest of F-246 (WP-580,
// REC-02, REC-06) against PostgreSQL: the candidate search compares serial
// numbers case-insensitively like identity resolution. 'AB-1' and then
// 'ab-1' from another address update the one CI (Updated=1, no second CI);
// from the same address with a source of lower rank the serial number in
// other case is no conflicting value. The stored serial number keeps its
// case. The candidate search on its own finds the CI in either case.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSerialNumberMatchesCaseInsensitively(t *testing.T) {
	s := newSeenFixture(t, false)
	bg := context.Background()
	at := time.Now().UTC().Add(-time.Minute)
	count := func(q string) int {
		t.Helper()
		var n int
		if err := s.f.Admin.QueryRow(bg, q, s.f.OrgA).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return n
	}
	cis := func() int {
		return count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND lower(serial_number) = 'ab-1' AND deleted_at IS NULL`)
	}
	conflicts := func() int {
		return count(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind IN ('conflicting_values', 'serial_mismatch')`)
	}

	if resp := ingestRecords(t, s.mux, s.ctx, s.record("AB-1", "192.0.2.181", "snmp", at)); resp.Created != 1 {
		t.Fatalf("AB-1: %+v, want 1 created", resp)
	}
	resp := ingestRecords(t, s.mux, s.ctx, s.record("ab-1", "192.0.2.182", "snmp", at.Add(time.Second)))
	if resp.Updated != 1 || resp.Created != 0 || resp.Conflicts != 0 || resp.ReviewItems != 0 {
		t.Errorf("ab-1 from another address: %+v, want Updated=1 and nothing else", resp)
	}
	if n := cis(); n != 1 {
		t.Errorf("%d CIs with serial number ab-1 in any case, want 1", n)
	}

	resp = ingestRecords(t, s.mux, s.ctx, s.record("ab-1", "192.0.2.181", "sweep", at.Add(2*time.Second)))
	if resp.Updated != 1 || resp.Created != 0 || resp.ReviewItems != 0 {
		t.Errorf("ab-1 from the same address, lower rank: %+v, want Updated=1 without review", resp)
	}
	if n := conflicts(); n != 0 {
		t.Errorf("%d conflicting_values/serial_mismatch items for a serial number in other case, want 0", n)
	}
	if n := cis(); n != 1 {
		t.Errorf("%d CIs with serial number ab-1 in any case, want 1", n)
	}
	if n := count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = 'AB-1'`); n != 1 {
		t.Errorf("the stored serial number changed its case")
	}

	repo := discovery.NewPGRepository(s.f.App)
	for _, serial := range []string{"AB-1", "ab-1", "Ab-1"} {
		ids, err := repo.IdentityCandidates(s.f.OrgCtx(s.f.OrgA), s.f.OrgA, &discovery.IdentityQuery{SerialNumber: serial})
		if err != nil || len(ids) != 1 {
			t.Errorf("IdentityCandidates(%s) = %v, %v; want the one CI", serial, ids, err)
		}
		hits, err := repo.IdentityHits(s.f.OrgCtx(s.f.OrgA), s.f.OrgA, &discovery.IdentityKeys{Serials: []string{serial}})
		if err != nil || len(hits) != 1 {
			t.Errorf("IdentityHits(%s) = %v, %v; want the one CI", serial, hits, err)
		}
	}
}
