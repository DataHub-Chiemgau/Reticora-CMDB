// The unclassified_device review of the server-side ingest (WP-417; DIS-02,
// F-030): the review is opened once the identity of the record is resolved
// and only while its CI is a generic_device, one per CI, and a dismissed
// review stays dismissed; it carries the client and site of the CI
// (WP-342). A CI that discovery classifies later closes its review as
// reclassified.
//
//   - TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices (moved from
//     ingest_auditdefect_test.go): no review for a CI that SNMP classified as
//     switch, also when its sweep record arrives first; a dismissal survives
//     the next sweep; control: a device without any profile match has
//     exactly one open review with reason no_profile_match.
//   - TestUnclassifiedReviewResolvedWhenReclassified: a generic_device CI
//     with an open review that SNMP, Redfish or SSH classifies later closes
//     the review with resolution reclassified.
//   - TestUnclassifiedReviewScopeOnPostgres: on PostgreSQL the review of a
//     client-2 record has client 2, also for an org-wide record of a client-2
//     CI, and only client-2 principals see it; dismissal and reclassification
//     as above (needs TEST_DATABASE_URL).
//
// The records are classified exactly as the collector does
// (collector/profiles.Default().ClassifyRecord) and go through the server's
// ingest pipeline (processItems).
//
// Retest (F-030):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable go test -count=1 -v \
//	  -run 'TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices|TestUnclassifiedReviewResolvedWhenReclassified|TestUnclassifiedReviewScopeOnPostgres' ./internal/discovery/
package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
)

// classifiedRecords builds records as the collector's plugins do and
// classifies them with the collector's chain.
type classifiedRecords struct{ at time.Time }

func (c classifiedRecords) record(protocol string, res *plugins.Result) DeviceRecord {
	r := plugins.RecordFromResult(protocol, res, c.at)
	profiles.Default().ClassifyRecord(&r)
	return r
}

// sweep: open ports and the MAC from ARP, no evidence.
func (c classifiedRecords) sweep(ip, mac string) DeviceRecord {
	return c.record("sweep", &plugins.Result{CIType: "network.device", Name: ip, IP: ip, MAC: mac,
		Attributes: map[string]any{"collectedBy": "sweep", "openPorts": []int{22, 443}, "reachable": true}})
}

// snmp: a Catalyst 9300 (switch).
func (c classifiedRecords) snmp(ip, mac string) DeviceRecord {
	return c.record("snmp", &plugins.Result{CIType: "network.device", Name: "core-sw-1", IP: ip, MAC: mac,
		Attributes: map[string]any{"sysObjectID": "1.3.6.1.4.1.9.1.2494", "sysDescr": "Cisco IOS XE Software, Catalyst L3 Switch Software (CAT9K_IOSXE), C9300-48P"}})
}

// redfish: a Dell PowerEdge (server).
func (c classifiedRecords) redfish(ip, mac string) DeviceRecord {
	return c.record("redfish", &plugins.Result{CIType: "bmc", Name: "r750", IP: ip, MAC: mac, Manufacturer: "Dell Inc.", Model: "PowerEdge R750",
		Attributes: map[string]any{"api": "redfish"}})
}

// ssh: a Debian host (server).
func (c classifiedRecords) ssh(ip, mac string) DeviceRecord {
	return c.record("ssh", &plugins.Result{CIType: "server", Name: "web01", IP: ip, MAC: mac,
		Attributes: map[string]any{"sshBanner": "SSH-2.0-OpenSSH_9.2p1 Debian-2", "uname": "Linux web01 6.1.0-18-amd64"}})
}

// memoryIngest is the ingest over the memory repositories with the types of
// the classification chain.
func memoryIngest(t *testing.T) (*Handler, *MemoryRepository, *ci.MemoryRepository, context.Context) {
	t.Helper()
	repo := NewMemoryRepository()
	cis := ci.NewMemoryRepository()
	for _, key := range []string{"generic_device", "switch", "server"} {
		repo.SeedCIType(key, key)
		cis.SetType(key, key)
	}
	ctx := discoveryTenantCtx(httptest.NewRequest(http.MethodGet, "/", http.NoBody)).Context()
	return NewHandler(repo, cis), repo, cis, ctx
}

func ingestRecordsInto(t *testing.T, h *Handler, ctx context.Context, orgID string, recs ...DeviceRecord) BulkIngestResponse {
	t.Helper()
	var items []IngestItem
	for i := range recs {
		items = append(items, itemFromRecord(&recs[i]))
	}
	resp, err := h.processItems(ctx, orgID, items)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func unclassifiedItems(t *testing.T, repo Repository, ctx context.Context, orgID, status string) []ReviewItem {
	t.Helper()
	items, _, err := repo.ListReviewItems(ctx, orgID, ReviewFilter{Kind: ReviewKindUnclassifiedDevice, Status: status}, api.PaginationParams{Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices(t *testing.T) {
	now := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)
	recs := classifiedRecords{at: now}
	later := classifiedRecords{at: now.Add(time.Minute)}

	t.Run("SNMP-classified switch plus its sweep record: no unclassified_device review", func(t *testing.T) {
		h, repo, cis, ctx := memoryIngest(t)
		ingestRecordsInto(t, h, ctx, "org-1", recs.snmp("10.0.0.1", "00:11:22:33:44:01"))
		ingestRecordsInto(t, h, ctx, "org-1", later.sweep("10.0.0.1", "00:11:22:33:44:01"))
		list, _, _ := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
		if len(list) != 1 || list[0].CITypeID != "switch" {
			t.Fatalf("control: want one CI of type switch, got %d (%+v)", len(list), list)
		}
		if n := len(unclassifiedItems(t, repo, ctx, "org-1", "")); n != 0 {
			t.Errorf("DIS-02 violated: %d unclassified_device review(s) for a CI that SNMP classified as switch", n)
		}
	})

	t.Run("sweep record first in the batch of an SNMP-classified switch: no open review", func(t *testing.T) {
		h, repo, cis, ctx := memoryIngest(t)
		ingestRecordsInto(t, h, ctx, "org-1", recs.sweep("10.0.0.2", "00:11:22:33:44:02"), later.snmp("10.0.0.2", "00:11:22:33:44:02"))
		list, _, _ := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
		if len(list) != 1 || list[0].CITypeID != "switch" {
			t.Fatalf("control: want one CI of type switch, got %d (%+v)", len(list), list)
		}
		if n := len(unclassifiedItems(t, repo, ctx, "org-1", ReviewStatusOpen)); n != 0 {
			t.Errorf("DIS-02 violated: %d unclassified_device review(s) open for a CI that SNMP classified as switch", n)
		}
	})

	t.Run("sweep-only device: exactly one review, a dismissal survives the next sweep", func(t *testing.T) {
		h, repo, cis, ctx := memoryIngest(t)
		if resp := ingestRecordsInto(t, h, ctx, "org-1", recs.sweep("10.0.0.9", "00:11:22:33:44:09")); resp.ReviewItems != 1 {
			t.Errorf("response counts %d review items, want 1", resp.ReviewItems)
		}
		list, _, _ := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
		items := unclassifiedItems(t, repo, ctx, "org-1", ReviewStatusOpen)
		if len(list) != 1 || list[0].CITypeID != "generic_device" || len(items) != 1 {
			t.Fatalf("control: want one generic_device CI with exactly 1 open unclassified_device review, got CIs %+v, reviews %+v", list, items)
		}
		if it := items[0]; it.Payload["reason"] != ReasonNoProfileMatch || it.Payload["ci_id"] != list[0].ID ||
			it.ObjectKey != "ci:"+list[0].ID || len(it.CandidateCIIDs) != 1 || it.CandidateCIIDs[0] != list[0].ID {
			t.Errorf("control: review %+v, want reason no_profile_match about CI %s", it, list[0].ID)
		}
		// The same device again: still one review.
		ingestRecordsInto(t, h, ctx, "org-1", later.sweep("10.0.0.9", "00:11:22:33:44:09"))
		if n := len(unclassifiedItems(t, repo, ctx, "org-1", "")); n != 1 {
			t.Errorf("the next sweep of the unclassified device: %d reviews, want 1", n)
		}
		if _, err := h.ReviewService().Resolve(ctx, "org-1", items[0].ID, &Resolution{Action: "dismiss"}); err != nil {
			t.Fatalf("dismiss: %v", err)
		}
		ingestRecordsInto(t, h, ctx, "org-1", classifiedRecords{at: now.Add(24 * time.Hour)}.sweep("10.0.0.9", "00:11:22:33:44:09"))
		if n := len(unclassifiedItems(t, repo, ctx, "org-1", ReviewStatusOpen)); n != 0 {
			t.Errorf("DIS-02 violated: the dismissed unclassified_device review was re-opened by the next sweep (%d open)", n)
		}
	})
}

func TestUnclassifiedReviewResolvedWhenReclassified(t *testing.T) {
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	for _, tc := range []struct {
		protocol, ciType string
		record           func(c classifiedRecords, ip, mac string) DeviceRecord
	}{
		{"snmp", "switch", classifiedRecords.snmp},
		{"redfish", "server", classifiedRecords.redfish},
		{"ssh", "server", classifiedRecords.ssh},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			h, repo, cis, ctx := memoryIngest(t)
			ingestRecordsInto(t, h, ctx, "org-1", classifiedRecords{at: now}.sweep("10.0.1.1", "00:1b:21:41:71:01"))
			open := unclassifiedItems(t, repo, ctx, "org-1", ReviewStatusOpen)
			if len(open) != 1 {
				t.Fatalf("control: %d open reviews after the sweep, want 1", len(open))
			}
			ingestRecordsInto(t, h, ctx, "org-1", tc.record(classifiedRecords{at: now.Add(time.Minute)}, "10.0.1.1", "00:1b:21:41:71:01"))
			list, _, _ := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
			if len(list) != 1 || list[0].CITypeID != tc.ciType {
				t.Fatalf("control: want the CI raised to %s, got %+v", tc.ciType, list)
			}
			closed, err := repo.GetReviewItem(ctx, "org-1", open[0].ID)
			if err != nil {
				t.Fatal(err)
			}
			if closed.Status != ReviewStatusResolved || closed.Resolution != ReasonReclassified || closed.ResolvedBy != "" {
				t.Errorf("review after the %s classification: status %q, resolution %q, resolved_by %q; want resolved, reclassified, the system",
					tc.protocol, closed.Status, closed.Resolution, closed.ResolvedBy)
			}
			// The classified CI gets no new review from its sweep record.
			ingestRecordsInto(t, h, ctx, "org-1", classifiedRecords{at: now.Add(2 * time.Minute)}.sweep("10.0.1.1", "00:1b:21:41:71:01"))
			if n := len(unclassifiedItems(t, repo, ctx, "org-1", ReviewStatusOpen)); n != 0 {
				t.Errorf("%d open reviews after the next sweep of the classified CI, want 0", n)
			}
		})
	}
}

func TestUnclassifiedReviewScopeOnPostgres(t *testing.T) {
	f := scopetest.Seed(t, "6b")
	bg := context.Background()
	sys := systemContext(bg, f.OrgA)
	repo := NewPGRepository(f.App)
	h := NewHandler(repo, ci.NewPGRepository(f.App))
	now := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	ingest := func(at time.Duration, recs ...DeviceRecord) {
		t.Helper()
		for i := range recs {
			recs[i].ObservedAt = now.Add(at)
		}
		ingestRecordsInto(t, h, sys, f.OrgA, recs...)
	}
	type row struct {
		status, client, site, objectKey, candidates, reason, resolution string
	}
	reviewOf := func(ciID string) []row {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT status, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), COALESCE(object_key, ''),
				COALESCE(array_to_string(candidate_ci_ids, ','), ''), COALESCE(payload->>'reason', ''), COALESCE(resolution, '')
			FROM review_item WHERE organization_id = $1 AND kind = 'unclassified_device' AND candidate_ci_ids @> ARRAY[$2::uuid]
			ORDER BY created_at`, f.OrgA, ciID)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []row
		for rows.Next() {
			var r row
			if err := rows.Scan(&r.status, &r.client, &r.site, &r.objectKey, &r.candidates, &r.reason, &r.resolution); err != nil {
				t.Fatal(err)
			}
			out = append(out, r)
		}
		return out
	}
	ciOf := func(ip string) (id, typeKey, client string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT c.id::text, t.key, COALESCE(c.client_id::text, '') FROM ci c JOIN ci_type t ON t.id = c.ci_type_id
			WHERE c.organization_id = $1 AND c.management_ip = $2 AND c.deleted_at IS NULL`, f.OrgA, ip).Scan(&id, &typeKey, &client); err != nil {
			t.Fatalf("CI %s: %v", ip, err)
		}
		return id, typeKey, client
	}
	visible := func(ctx context.Context) int {
		t.Helper()
		items, _, err := h.ReviewService().List(ctx, f.OrgA, ReviewFilter{Kind: ReviewKindUnclassifiedDevice}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return len(items)
	}
	recs := classifiedRecords{at: now}
	clientTwo := func(rec DeviceRecord) DeviceRecord {
		rec.ClientID = f.Client2
		return rec
	}

	// A client-2 sweep record of a device without any profile match.
	ingest(0, clientTwo(recs.sweep("198.51.100.61", "00:1b:21:41:72:01")))
	gd, typeKey, client := ciOf("198.51.100.61")
	got := reviewOf(gd)
	if typeKey != "generic_device" || client != f.Client2 || len(got) != 1 {
		t.Fatalf("control: CI %s of type %s and client %q with reviews %+v; want a client-2 generic_device with one review", gd, typeKey, client, got)
	}
	if r := got[0]; r.status != ReviewStatusOpen || r.client != f.Client2 || r.objectKey != "ci:"+gd || r.candidates != gd || r.reason != ReasonNoProfileMatch {
		t.Errorf("review of the client-2 record = %+v; want open, client 2, about CI %s, reason no_profile_match", r, gd)
	}
	if n := visible(f.ClientCtx(f.Client2)); n != 1 {
		t.Errorf("a client-2 principal sees %d unclassified_device reviews, want 1", n)
	}
	if n := visible(f.ClientCtx(f.Client1)); n != 0 {
		t.Errorf("a client-1 principal sees %d unclassified_device reviews of client 2, want 0", n)
	}

	// An org-wide record of a client-2 generic_device CI: the CI decides the
	// client of the review, not the record.
	var genericType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'generic_device' AND organization_id IS NULL`).Scan(&genericType); err != nil {
		t.Fatal(err)
	}
	known := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, primary_mac, management_ip)
		VALUES ($1, $2, $3, $4, 'wp417-known', 'active', '00:1b:21:41:72:02', '198.51.100.62')`, known, f.OrgA, f.Client2, genericType); err != nil {
		t.Fatal(err)
	}
	ingest(time.Minute, recs.sweep("198.51.100.62", "00:1b:21:41:72:02"))
	if got = reviewOf(known); len(got) != 1 || got[0].client != f.Client2 || got[0].status != ReviewStatusOpen {
		t.Errorf("review of an org-wide record of a client-2 CI = %+v; want one open review of client 2", got)
	}

	// A dismissal by the client-2 principal survives the next sweep.
	open := unclassifiedItems(t, repo, f.ClientCtx(f.Client2), f.OrgA, ReviewStatusOpen)
	for i := range open {
		if open[i].Payload["ci_id"] != known {
			continue
		}
		if _, err := h.ReviewService().Resolve(f.ClientCtx(f.Client2), f.OrgA, open[i].ID, &Resolution{Action: "dismiss"}); err != nil {
			t.Fatalf("dismiss: %v", err)
		}
	}
	ingest(2*time.Minute, recs.sweep("198.51.100.62", "00:1b:21:41:72:02"))
	if got = reviewOf(known); len(got) != 1 || got[0].status != ReviewStatusDismissed {
		t.Errorf("reviews of the dismissed device after the next sweep = %+v; want only the dismissed one", got)
	}

	// SNMP classifies the first device later: the CI becomes a switch and its
	// review closes as reclassified, announced as review.resolved.
	ingest(3*time.Minute, clientTwo(recs.snmp("198.51.100.61", "00:1b:21:41:72:01")))
	if id, typeKey, _ := ciOf("198.51.100.61"); id != gd || typeKey != "switch" {
		t.Fatalf("control: CI %s of type %s after the SNMP record, want %s raised to switch", id, typeKey, gd)
	}
	if got = reviewOf(gd); len(got) != 1 || got[0].status != ReviewStatusResolved || got[0].resolution != ReasonReclassified {
		t.Errorf("review after the SNMP classification = %+v; want resolved as reclassified", got)
	}
	var announced int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox e JOIN review_item r ON e.entity_id::text = r.id::text
		WHERE e.organization_id = $1 AND e.type = 'review.resolved' AND r.candidate_ci_ids @> ARRAY[$2::uuid]
		  AND e.data->>'resolution' = 'reclassified'`, f.OrgA, gd).Scan(&announced); err != nil {
		t.Fatal(err)
	}
	if announced != 1 {
		t.Errorf("%d review.resolved events for the reclassified review, want 1", announced)
	}
	// The classified CI gets no new review from its sweep record.
	ingest(4*time.Minute, clientTwo(recs.sweep("198.51.100.61", "00:1b:21:41:72:01")))
	if got = reviewOf(gd); len(got) != 1 || got[0].status != ReviewStatusResolved {
		t.Errorf("reviews of the switch after the next sweep = %+v; want only the resolved one", got)
	}
}
