//go:build auditdefect

package rls_test

// Phase-1 acceptance audit, group T1-tenancy: DEFECT tests (they fail until
// the product is fixed).
//
// WP-025 (TEN-02, TEN-05, TEN-06, TEN-09) P1 "Review items without
// candidate CIs leak across clients through GET /api/v1/review-items":
// review_item has no client_id/site_id and reviews/pg_repository.go treats an
// empty candidate list as visible, so a client-1 principal lists, reads and
// resolves the unclassified_device / scope_suggested items that discovery
// opened for devices and subnets of client 2.
//
// The rack channel defect (WP-208, F-002, F-180) is fixed (WP-340); its test
// runs in the regular suite as TestRackChannelSiteScope
// (rack_channel_scope_integration_test.go).
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -tags auditdefect -count=1 -v \
//     -run '^TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope$' ./internal/tenant/rls/

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope opens review
// items without candidate CIs through the product's own write paths for a
// device and a subnet of client 2 and checks that a client-1 principal can
// neither list, read nor resolve them, while the org-wide principal sees
// them.
func TestAuditT1ReviewItemsWithoutCandidatesStayInClientScope(t *testing.T) {
	w := newT1World(t, "d3")
	f := w.f
	bg := context.Background()
	client1 := w.as("client1", only(f.Client1), all())
	collector2 := w.scalar(`INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 't1rev-collector-c2', 'active') RETURNING id::text`,
		f.OrgA, f.Client2)

	// unclassified_device: a record of the client-2 discovery scope whose CI
	// type the server does not know (discovery.go openFrame).
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "t1rev-unknown-type", Name: "t1rev-c2-firewall", IP: "10.77.2.1", Serial: "T1REV-C2-SN"},
		time.Now().Add(-time.Minute))
	rec.ClientID = f.Client2
	if code, body := w.do(w.orgA, http.MethodPost, "/api/v1/ingest/bulk", plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}}); code != http.StatusAccepted {
		t.Fatalf("ingest the client-2 record: %d %s", code, trim(body))
	}
	// scope_suggested: the client-2 collector reports an uncovered subnet
	// (scope_handler.go SuggestScopes, the NATS subnets report path).
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	if n, err := discovery.NewHandler(repos.Discovery, repos.CI).SuggestScopes(f.OrgCtx(f.OrgA), f.OrgA, collector2, []string{"10.77.9.0/24"}); err != nil || n != 1 {
		t.Fatalf("suggest the client-2 subnet: n=%d err=%v", n, err)
	}

	items := map[string]string{}
	rows, err := f.Admin.Query(bg, `SELECT kind, id::text FROM review_item WHERE organization_id = $1 AND cardinality(candidate_ci_ids) = 0
		AND kind IN ('unclassified_device', 'scope_suggested')`, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind, id string
		if err := rows.Scan(&kind, &id); err != nil {
			t.Fatal(err)
		}
		items[kind] = id
	}
	rows.Close()
	if items["unclassified_device"] == "" || items["scope_suggested"] == "" {
		t.Fatalf("setup: review items without candidates %v, want unclassified_device and scope_suggested", items)
	}

	// Positive control: the org-wide principal sees both.
	_, orgBody := w.do(w.orgA, http.MethodGet, "/api/v1/review-items?limit=200", nil)
	for kind, id := range items {
		if !strings.Contains(orgBody, id) {
			t.Fatalf("org-wide principal does not see the %s item %s", kind, id)
		}
	}

	code, body := w.do(client1, http.MethodGet, "/api/v1/review-items?limit=200", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /review-items as client 1: %d %s", code, trim(body))
	}
	for _, marker := range []string{"t1rev-c2-firewall", "T1REV-C2-SN", "10.77.9.0/24", "t1rev-collector-c2", f.Client2} {
		if strings.Contains(body, marker) {
			t.Errorf("TEN-06/TEN-09: GET /review-items as client 1 discloses %q of client 2", marker)
		}
	}
	for kind, id := range items {
		if code, _ := w.do(client1, http.MethodGet, "/api/v1/review-items/"+id, nil); code != http.StatusNotFound {
			t.Errorf("GET /review-items/{%s of client 2} as client 1: %d, want 404", kind, code)
		}
		if code, _ := w.do(client1, http.MethodPost, "/api/v1/review-items/"+id+"/resolve", map[string]any{"action": "dismiss"}); code != http.StatusNotFound {
			t.Errorf("POST /review-items/{%s of client 2}/resolve as client 1: %d, want 404", kind, code)
		}
		if st := w.scalar(`SELECT status FROM review_item WHERE id = $1`, id); st != "open" {
			t.Errorf("%s item of client 2 after the client-1 resolve: status %q, want open", kind, st)
		}
	}
	// GraphQL reviewInbox is the same channel.
	if _, gql := w.do(client1, http.MethodPost, "/bff/graphql", map[string]any{"query": "{ reviewInbox(first: 100) { id } }"}); strings.Contains(gql, items["unclassified_device"]) ||
		strings.Contains(gql, items["scope_suggested"]) {
		t.Errorf("TEN-09: GraphQL reviewInbox as client 1 lists review items of client 2")
	}
}
