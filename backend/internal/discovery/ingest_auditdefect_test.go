//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT tests of the
// server-side ingest (must fail until the defects are fixed).
//
// WP-145 (COL-04), WP-151 (DIS-02).
//
// Findings encoded here:
//   - TestAuditEdgeIngestRefusesDisabledOrUnknownCollector – WP-145 P1
//     "Disabled collector keeps its open NATS connection and can keep
//     ingesting (no disconnect, EdgeIngest unchecked)": EdgeIngest
//     (edge_ingest.go) checks only the license, so records of a disabled or
//     unknown collector are processed. COL-04: after disable further data is
//     refused.
//   - TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices – WP-151 P1
//     "Every swept device gets a (recurring) false unclassified_device
//     review, even when SNMP/SSH/Redfish classified it": the server opens the
//     review for the sweep record of a CI that is already a switch, and
//     reviews.Open re-opens a dismissed review on the next sweep. DIS-02: only
//     a device without any profile match becomes generic_device plus an
//     unclassified_device review; a dismissal is kept.
//
// The records are classified exactly as the collector does
// (collector/profiles.Default().ClassifyRecord) and go through the server's
// ingest pipeline (processItems) with the memory repositories.
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditEdgeIngestRefusesDisabledOrUnknownCollector|TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices' ./internal/discovery/
package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
)

func TestAuditEdgeIngestRefusesDisabledOrUnknownCollector(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name        string
		setup       func(t *testing.T, repo *MemoryRepository) string
		wantRefused bool
	}{
		{name: "active collector (control)", setup: func(t *testing.T, repo *MemoryRepository) string {
			c := &Collector{OrganizationID: "org-1", Name: "edge-active"}
			if err := repo.RegisterCollector(ctx, c); err != nil {
				t.Fatal(err)
			}
			return c.ID
		}},
		{name: "disabled collector", wantRefused: true, setup: func(t *testing.T, repo *MemoryRepository) string {
			c := &Collector{OrganizationID: "org-1", Name: "edge-disabled"}
			if err := repo.RegisterCollector(ctx, c); err != nil {
				t.Fatal(err)
			}
			disabled := CollectorDisabled
			if _, err := repo.UpdateCollector(ctx, "org-1", c.ID, &CollectorPatch{Status: &disabled}); err != nil {
				t.Fatal(err)
			}
			return c.ID
		}},
		{name: "unknown collector", wantRefused: true, setup: func(*testing.T, *MemoryRepository) string {
			return "00000000-0000-4000-8000-00000000dead"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := NewMemoryRepository()
			repo.SeedCIType("server", "server")
			cis := ci.NewMemoryRepository()
			h := NewHandler(repo, cis)
			colID := tc.setup(t, repo)
			body, err := json.Marshal(ingestBody{Records: []DeviceRecord{consumerRecord("audit-m1", time.Now().UTC().Add(-time.Minute))}})
			if err != nil {
				t.Fatal(err)
			}
			reply := h.EdgeIngest(ctx, "org-1", colID, body)
			items, _, err := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
			if err != nil {
				t.Fatal(err)
			}
			if !tc.wantRefused {
				if reply.Status != http.StatusAccepted || len(items) != 1 {
					t.Fatalf("control: active collector ingest = %+v, CIs=%d", reply, len(items))
				}
				return
			}
			if reply.Status != http.StatusForbidden && reply.Status != http.StatusNotFound {
				t.Errorf("COL-04 violated: edge ingest for the %s answered %d (want 403/404)", tc.name, reply.Status)
			}
			if len(items) != 0 {
				t.Errorf("COL-04 violated: the records of the %s created %d CI(s)", tc.name, len(items))
			}
		})
	}
}

func TestAuditUnclassifiedReviewOnlyForUnclassifiedDevices(t *testing.T) {
	cls := profiles.Default()
	sweepRecord := func(ip, mac string, at time.Time) DeviceRecord {
		// As the collector's sweep plugin builds it (open ports, MAC from ARP),
		// classified by the collector's chain.
		r := plugins.RecordFromResult("sweep", &plugins.Result{CIType: "network.device", Name: ip, IP: ip, MAC: mac,
			Attributes: map[string]any{"collectedBy": "sweep", "openPorts": []int{22, 443}, "reachable": true}}, at)
		cls.ClassifyRecord(&r)
		return r
	}
	snmpRecord := func(ip, mac string, at time.Time) DeviceRecord {
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "network.device", Name: "core-sw-1", IP: ip, MAC: mac,
			Attributes: map[string]any{"sysObjectID": "1.3.6.1.4.1.9.1.2494", "sysDescr": "Cisco IOS XE Software, Catalyst L3 Switch Software (CAT9K_IOSXE), C9300-48P"}}, at)
		cls.ClassifyRecord(&r)
		return r
	}
	setup := func(t *testing.T) (*Handler, *MemoryRepository, *ci.MemoryRepository, context.Context) {
		t.Helper()
		repo := NewMemoryRepository()
		repo.SeedCIType("generic_device", "generic_device")
		repo.SeedCIType("switch", "switch")
		cis := ci.NewMemoryRepository()
		h := NewHandler(repo, cis)
		ctx := discoveryTenantCtx(httptest.NewRequest(http.MethodGet, "/", http.NoBody)).Context()
		return h, repo, cis, ctx
	}
	ingest := func(t *testing.T, h *Handler, ctx context.Context, recs ...DeviceRecord) {
		t.Helper()
		var items []IngestItem
		for i := range recs {
			items = append(items, itemFromRecord(&recs[i]))
		}
		if _, err := h.processItems(ctx, "org-1", items); err != nil {
			t.Fatal(err)
		}
	}
	openUnclassified := func(t *testing.T, repo *MemoryRepository, ctx context.Context) int {
		t.Helper()
		items, _, err := repo.ListReviewItems(ctx, "org-1", ReviewFilter{Kind: ReviewKindUnclassifiedDevice, Status: "open"}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		return len(items)
	}
	now := time.Now().UTC().Add(-48 * time.Hour).Truncate(time.Second)

	t.Run("SNMP-classified switch plus its sweep record: no unclassified_device review", func(t *testing.T) {
		h, repo, cis, ctx := setup(t)
		ingest(t, h, ctx, snmpRecord("10.0.0.1", "00:11:22:33:44:01", now))
		ingest(t, h, ctx, sweepRecord("10.0.0.1", "00:11:22:33:44:01", now.Add(time.Minute)))
		list, _, _ := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
		if len(list) != 1 || list[0].CITypeID != "switch" {
			t.Fatalf("control: want one CI of type switch, got %d (%+v)", len(list), list)
		}
		if n := openUnclassified(t, repo, ctx); n != 0 {
			t.Errorf("DIS-02 violated: %d unclassified_device review(s) open for a CI that SNMP classified as switch", n)
		}
	})

	t.Run("sweep-only device: exactly one review, a dismissal survives the next sweep", func(t *testing.T) {
		h, repo, _, ctx := setup(t)
		ingest(t, h, ctx, sweepRecord("10.0.0.9", "00:11:22:33:44:09", now))
		items, _, err := repo.ListReviewItems(ctx, "org-1", ReviewFilter{Kind: ReviewKindUnclassifiedDevice, Status: "open"}, api.PaginationParams{Limit: 50})
		if err != nil || len(items) != 1 {
			t.Fatalf("control: want exactly 1 open unclassified_device review for the unclassified device, got %d (%v)", len(items), err)
		}
		if _, err = h.ReviewService().Resolve(ctx, "org-1", items[0].ID, &Resolution{Action: "dismiss"}); err != nil {
			t.Fatalf("dismiss: %v", err)
		}
		ingest(t, h, ctx, sweepRecord("10.0.0.9", "00:11:22:33:44:09", now.Add(24*time.Hour)))
		if n := openUnclassified(t, repo, ctx); n != 0 {
			t.Errorf("DIS-02 violated: the dismissed unclassified_device review was re-opened by the next sweep (%d open)", n)
		}
	})
}
