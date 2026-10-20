package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

type fakeLicense struct{ state, problem string }

func (f fakeLicense) DiscoveryLicense(context.Context, string) (state, problemType string, err error) {
	return f.state, f.problem, nil
}

// TestEdgeIngest covers the collectors' edge data path (COL-08): records
// sent over the mTLS NATS channel are processed like the HTTP ingest for the
// organization of the subject; an expired or missing discovery license is
// refused with the problem type the collector pauses on; invalid bodies are
// refused like on the API.
func TestEdgeIngest(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	h := NewHandler(repo, cis)
	ctx := context.Background()
	col := &Collector{OrganizationID: "org-1", Name: "edge"}
	if err := repo.RegisterCollector(ctx, col); err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(ingestBody{Records: []DeviceRecord{consumerRecord("m1", time.Now().UTC().Add(-time.Hour))}})
	if err != nil {
		t.Fatal(err)
	}

	reply := h.EdgeIngest(ctx, "org-1", col.ID, body)
	if reply.Status != http.StatusAccepted || reply.Result == nil || reply.Result.Created != 1 || reply.LicenseStatus != LicenseActive {
		t.Fatalf("ingest: %+v", reply)
	}
	items, _, err := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
	if err != nil || len(items) != 1 {
		t.Fatalf("CIs of org-1: %d, %v", len(items), err)
	}
	if other, _, _ := cis.List(ctx, "org-2", ci.FilterParams{}, api.PaginationParams{Limit: 10}); len(other) != 0 {
		t.Errorf("records reached another organization: %d", len(other))
	}

	if reply = h.EdgeIngest(ctx, "org-1", col.ID, []byte(`{"records":[]}`)); reply.Status != http.StatusBadRequest {
		t.Errorf("empty batch: %+v", reply)
	}
	if reply = h.EdgeIngest(ctx, "org-1", col.ID, []byte(`not json`)); reply.Status != http.StatusBadRequest {
		t.Errorf("invalid body: %+v", reply)
	}

	h.WithEdgeLicense(fakeLicense{state: LicenseExpired, problem: api.ProblemLicenseExpired})
	if reply = h.EdgeIngest(ctx, "org-1", col.ID, body); reply.Status != http.StatusForbidden ||
		reply.Type != api.ProblemLicenseExpired || reply.LicenseStatus != LicenseExpired {
		t.Errorf("expired license: %+v", reply)
	}
	h.WithEdgeLicense(fakeLicense{state: LicenseNotEntitled, problem: api.ProblemFeatureNotEntitled})
	if reply = h.EdgeIngest(ctx, "org-1", col.ID, body); reply.Status != http.StatusForbidden || reply.Type != api.ProblemFeatureNotEntitled {
		t.Errorf("not entitled: %+v", reply)
	}
}

// TestEdgeHeartbeat: a heartbeat over the edge activates the collector and
// reports its status and the license state; an unknown collector is 404.
func TestEdgeHeartbeat(t *testing.T) {
	repo := NewMemoryRepository()
	h := NewHandler(repo, ci.NewMemoryRepository()).WithEdgeLicense(fakeLicense{state: LicenseActive})
	ctx := context.Background()
	c := &Collector{OrganizationID: "org-1", Name: "edge"}
	if err := repo.RegisterCollector(ctx, c); err != nil {
		t.Fatal(err)
	}
	reply := h.EdgeHeartbeat(ctx, "org-1", c.ID, []byte(`{"spool":{"messages":1}}`))
	if reply.Status != http.StatusNoContent || reply.CollectorStatus != CollectorActive || reply.LicenseStatus != LicenseActive {
		t.Fatalf("heartbeat: %+v", reply)
	}
	if reply = h.EdgeHeartbeat(ctx, "org-1", "unknown", nil); reply.Status != http.StatusNotFound {
		t.Errorf("unknown collector: %+v", reply)
	}
	if reply = h.EdgeHeartbeat(ctx, "org-1", c.ID, []byte(`{`)); reply.Status != http.StatusBadRequest {
		t.Errorf("invalid body: %+v", reply)
	}
}

func TestEdgeSubjects(t *testing.T) {
	org, col := "6f0b6f0e-0000-4000-8000-000000000001", "6f0b6f0e-0000-4000-8000-000000000002"
	if o, c, ok := subjectCollector("collector."+org+"."+col+IngestSuffix, IngestSuffix); !ok || o != org || c != col {
		t.Errorf("ingest subject: %s %s %v", o, c, ok)
	}
	if _, _, ok := subjectCollector("collector.x.y"+IngestSuffix, IngestSuffix); ok {
		t.Error("non-UUID subject accepted")
	}
}
