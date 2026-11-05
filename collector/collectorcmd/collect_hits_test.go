package collectorcmd

import (
	"context"
	"fmt"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// goroutineCollectPlugin records the goroutine peak seen inside Collect.
type goroutineCollectPlugin struct {
	peak      atomic.Int64
	collected atomic.Int64
}

func (g *goroutineCollectPlugin) Name() string { return "snmp" }
func (g *goroutineCollectPlugin) Probe(_ context.Context, _ []string, _ map[string]string) ([]plugins.Result, error) {
	return nil, nil
}
func (g *goroutineCollectPlugin) Collect(_ context.Context, _ string, _ map[string]string) (*plugins.DeviceRecord, error) {
	n := int64(runtime.NumGoroutine())
	for {
		p := g.peak.Load()
		if n <= p || g.peak.CompareAndSwap(p, n) {
			break
		}
	}
	g.collected.Add(1)
	time.Sleep(time.Millisecond)
	return &plugins.DeviceRecord{}, nil
}

// TestCollectHitsGoroutinePeakBounded checks that collectHits never holds
// more than collectParallelism Collect goroutines, however many hits a probe
// returned (F-262, F-384).
func TestCollectHitsGoroutinePeakBounded(t *testing.T) {
	hits := make([]plugins.Result, 2048)
	for i := range hits {
		ip := fmt.Sprintf("192.0.%d.%d", 2+i/256, i%256)
		hits[i] = plugins.Result{CIType: "network.device", Name: ip, IP: ip}
	}
	plug := &goroutineCollectPlugin{}
	base := int64(runtime.NumGoroutine())
	records, failed := collectHits(context.Background(), "snmp", plug, hits, nil)
	if len(records) != len(hits) || len(failed) != 0 || plug.collected.Load() != int64(len(hits)) {
		t.Fatalf("records = %d, failed = %d, collected = %d, want %d, 0, %d",
			len(records), len(failed), plug.collected.Load(), len(hits), len(hits))
	}
	if extra := plug.peak.Load() - base; extra > collectParallelism+50 {
		t.Fatalf("goroutine peak = %d above the %d before collectHits, want at most collectParallelism+50 = %d",
			extra, base, collectParallelism+50)
	}
}

// TestCollectHitsAfterCancelCollectsEveryHit pins that collectHits does not
// stop at a cancelled context: every hit is still passed to Collect and gets
// a record.
func TestCollectHitsAfterCancelCollectsEveryHit(t *testing.T) {
	hits := make([]plugins.Result, 4*collectParallelism)
	for i := range hits {
		ip := fmt.Sprintf("192.0.2.%d", i)
		hits[i] = plugins.Result{CIType: "network.device", Name: ip, IP: ip}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	plug := &goroutineCollectPlugin{}
	records, failed := collectHits(ctx, "snmp", plug, hits, nil)
	if len(records) != len(hits) || len(failed) != 0 || plug.collected.Load() != int64(len(hits)) {
		t.Fatalf("records = %d, failed = %d, collected = %d, want %d, 0, %d",
			len(records), len(failed), plug.collected.Load(), len(hits), len(hits))
	}
}
