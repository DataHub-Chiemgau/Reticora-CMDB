package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// WP-1153 (TLC-06): a collector connected before the deletion request
// keeps its NATS connection, but every edge data request of the grace
// period is refused with 423 before its handler runs; only the heartbeat
// passes, so the collector learns that it is paused.
//
// Retest:
//   cd backend && go test ./internal/discovery/ -run TestEdgeDataRefusedPendingDeletion

// pendingRepo is a memory repository whose organization is pending
// deletion (CollectorAccess as PGRepository reads it).
type pendingRepo struct {
	*MemoryRepository
	pending bool
}

func (r *pendingRepo) CollectorAccess(ctx context.Context, orgID, collectorID string) (string, bool, error) {
	status, err := r.CollectorStatus(ctx, orgID, collectorID)
	return status, r.pending, err
}

func TestEdgeDataRefusedPendingDeletion(t *testing.T) {
	mem := NewMemoryRepository()
	mem.SeedCIType("server", "server")
	repo := &pendingRepo{MemoryRepository: mem, pending: true}
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
	if reply.Status != http.StatusLocked || reply.Type != api.ProblemOrgPendingDeletion {
		t.Fatalf("ingest during the grace period: %+v", reply)
	}
	if items, _, _ := cis.List(ctx, "org-1", &ci.FilterParams{}, api.PaginationParams{Limit: 10}); len(items) != 0 {
		t.Fatalf("the refused ingest wrote %d CIs", len(items))
	}
	if reply = h.EdgeMetrics(ctx, "org-1", col.ID, []byte(`{}`)); reply.Status != http.StatusLocked {
		t.Errorf("metrics during the grace period: %+v", reply)
	}
	// The dispatcher of the edge subjects: every data suffix refused, the
	// heartbeat passes.
	for _, suffix := range []string{IngestSuffix, MetricsSuffix, ProbeResultsSuffix, ProbeStateSuffix, ProbeAliveSuffix} {
		if err := h.checkCollectorFor(ctx, "org-1", col.ID, suffix == HeartbeatSuffix); !errors.Is(err, ErrOrgPendingDeletion) {
			t.Errorf("%s during the grace period: %v", suffix, err)
		}
	}
	if err := h.checkCollectorFor(ctx, "org-1", col.ID, true); err != nil {
		t.Errorf("heartbeat during the grace period: %v", err)
	}
	if reply = h.EdgeHeartbeat(ctx, "org-1", col.ID, nil); reply.Status != http.StatusNoContent {
		t.Errorf("heartbeat reply during the grace period: %+v", reply)
	}
	// After the cancel the records are taken again.
	repo.pending = false
	if reply = h.EdgeIngest(ctx, "org-1", col.ID, body); reply.Status != http.StatusAccepted {
		t.Fatalf("ingest after the cancel: %+v", reply)
	}
}

// LicensedUnlessPendingDeletion (the Licensed function cmd/server wires):
// paused without license or in the grace period; pending is not read
// without a license; errors answer false.
func TestLicensedUnlessPendingDeletion(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("boom")
	answer := func(ok bool, err error) func(context.Context, string) (bool, error) {
		return func(context.Context, string) (bool, error) { return ok, err }
	}
	read := 0
	counting := func(pending bool) func(context.Context, string) (bool, error) {
		return func(context.Context, string) (bool, error) { read++; return pending, nil }
	}
	for _, c := range []struct {
		name     string
		licensed func(context.Context, string) (bool, error)
		pending  func(context.Context, string) (bool, error)
		want     bool
		err      bool
	}{
		{"licensed, no deletion", answer(true, nil), counting(false), true, false},
		{"licensed, grace period", answer(true, nil), counting(true), false, false},
		{"not licensed", answer(false, nil), counting(false), false, false},
		{"license error", answer(true, boom), counting(false), false, true},
		{"state error", answer(true, nil), answer(false, boom), false, true},
	} {
		got, err := LicensedUnlessPendingDeletion(c.licensed, c.pending)(ctx, "org-1")
		if got != c.want || (err != nil) != c.err {
			t.Errorf("%s: %v %v", c.name, got, err)
		}
	}
	if read != 2 {
		t.Errorf("the deletion state was read %d times, want 2 (only with a license)", read)
	}
}
