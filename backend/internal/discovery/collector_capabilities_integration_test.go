package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// The capability report of a collector (PRT-18, WP-966) on PostgreSQL and
// the memory repository: stored from the heartbeat (HTTP and edge) and
// from the monitoring alive message (probe.Edge OnAlive, as the router
// wires it), only when it changed, kept by a heartbeat without it, and
// returned in the collector DTO.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -p 1 -run 'TestHeartbeatStoresProbeCapabilities|TestAliveStoresProbeCapabilities|TestHeartbeatWithoutCapabilitiesKeepsStoredValue' ./internal/discovery/

// capsEnv is a handler on one repository with one collector.
type capsEnv struct {
	t         *testing.T
	ctx       context.Context
	org, id   string
	handler   *discovery.Handler
	mux       chi.Router
	mu        sync.Mutex
	triggered []string // collectors whose change triggered the assignment
}

// capsBackends runs fn on the memory repository and, with
// TEST_DATABASE_URL, on PostgreSQL.
func capsBackends(t *testing.T, fn func(t *testing.T, e *capsEnv)) {
	t.Run("memory", func(t *testing.T) {
		repo := discovery.NewMemoryRepository()
		c := &discovery.Collector{OrganizationID: "org-1", Name: "caps", Status: discovery.CollectorActive, Config: map[string]any{}}
		if err := repo.RegisterCollector(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		fn(t, newCapsEnv(t, context.Background(), repo, "org-1", c.ID))
	})
	t.Run("postgres", func(t *testing.T) {
		f := scopetest.Seed(t, "c6")
		repo := discovery.NewPGRepository(f.App)
		c := &discovery.Collector{OrganizationID: f.OrgA, Name: "wp966-caps", Status: discovery.CollectorActive, Config: map[string]any{}}
		if err := repo.RegisterCollector(f.OrgCtx(f.OrgA), c); err != nil {
			t.Fatal(err)
		}
		fn(t, newCapsEnv(t, f.OrgCtx(f.OrgA), repo, f.OrgA, c.ID))
	})
}

func newCapsEnv(t *testing.T, ctx context.Context, repo discovery.Repository, org, id string) *capsEnv {
	e := &capsEnv{t: t, ctx: ctx, org: org, id: id, mux: chi.NewRouter()}
	e.handler = discovery.NewHandler(repo, nil).WithCapabilityChanged(func(_ context.Context, orgID, collectorID string) {
		e.mu.Lock()
		e.triggered = append(e.triggered, orgID+"/"+collectorID)
		e.mu.Unlock()
	})
	e.handler.RegisterRoutes(e.mux)
	return e
}

func (e *capsEnv) triggers() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.triggered)
}

// heartbeat posts a heartbeat body over HTTP.
func (e *capsEnv) heartbeat(body any) {
	e.t.Helper()
	raw, _ := json.Marshal(body)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/collectors/"+e.id+"/heartbeat", bytes.NewReader(raw))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(tenant.WithTenant(e.ctx, tenant.TenantInfo{OrganizationID: e.org}))
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	if w.Code != http.StatusNoContent {
		e.t.Fatalf("heartbeat = %d %s", w.Code, w.Body.String())
	}
}

// stored returns the report of the collector from GET /api/v1/collectors
// and checks that GET /api/v1/collectors/{id} returns the same (AK2).
func (e *capsEnv) stored() *probewire.ProbeCapabilities {
	e.t.Helper()
	listed := e.listed()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/collectors/"+e.id, nil)
	r = r.WithContext(tenant.WithTenant(e.ctx, tenant.TenantInfo{OrganizationID: e.org}))
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /collectors/{id} = %d %s", w.Code, w.Body.String())
	}
	var one discovery.Collector
	if err := json.Unmarshal(w.Body.Bytes(), &one); err != nil {
		e.t.Fatal(err)
	}
	if one.ID != e.id || !one.ProbeCapabilities.Equal(listed) ||
		(listed != nil && !one.ProbeCapabilities.DetectedAt.Equal(listed.DetectedAt)) {
		e.t.Fatalf("GET /collectors/{id} = %s, report %+v; the list has %+v", one.ID, one.ProbeCapabilities, listed)
	}
	return one.ProbeCapabilities
}

// listed returns the report of the collector from GET /api/v1/collectors.
func (e *capsEnv) listed() *probewire.ProbeCapabilities {
	e.t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/collectors", nil)
	r = r.WithContext(tenant.WithTenant(e.ctx, tenant.TenantInfo{OrganizationID: e.org}))
	w := httptest.NewRecorder()
	e.mux.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		e.t.Fatalf("GET /collectors = %d %s", w.Code, w.Body.String())
	}
	var page struct {
		Data []discovery.Collector `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
		e.t.Fatal(err)
	}
	for i := range page.Data {
		if page.Data[i].ID == e.id {
			return page.Data[i].ProbeCapabilities
		}
	}
	e.t.Fatalf("collector %s not listed", e.id)
	return nil
}

func sampleCaps(at time.Time) *probewire.ProbeCapabilities {
	return &probewire.ProbeCapabilities{ICMP4: probewire.CapabilityUnprivileged, ICMP6: probewire.CapabilityNone,
		UDP67: false, UDP68: false, TimeSync: probewire.TimeSynchronized, ProbeTypes: []string{"icmp", "tcp"}, DetectedAt: at.UTC()}
}

// TestHeartbeatStoresProbeCapabilities covers AK2 (HTTP and edge
// heartbeat): the report is stored and listed in the collector DTO; the
// same report again (a later detected_at) writes nothing and triggers no
// assignment, a changed one is stored and triggers it.
func TestHeartbeatStoresProbeCapabilities(t *testing.T) {
	capsBackends(t, func(t *testing.T, e *capsEnv) {
		first := time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC)
		e.heartbeat(discovery.CollectorHeartbeatRequest{ProbeCapabilities: sampleCaps(first)})
		got := e.stored()
		if got == nil || !got.Equal(sampleCaps(first)) || !got.DetectedAt.Equal(first) {
			t.Fatalf("stored report %+v, want %+v", got, sampleCaps(first))
		}
		if e.triggers() != 1 {
			t.Fatalf("%d assignment triggers after the first report, want 1", e.triggers())
		}
		e.heartbeat(discovery.CollectorHeartbeatRequest{ProbeCapabilities: sampleCaps(first.Add(15 * time.Minute))})
		if got = e.stored(); !got.DetectedAt.Equal(first) || e.triggers() != 1 {
			t.Fatalf("unchanged report: detected_at %v, %d triggers; want the stored one kept and no trigger", got.DetectedAt, e.triggers())
		}
		// Over the edge with ICMPv6 now available.
		changed := sampleCaps(first.Add(30 * time.Minute))
		changed.ICMP6 = probewire.CapabilityRaw
		raw, _ := json.Marshal(discovery.CollectorHeartbeatRequest{ProbeCapabilities: changed})
		if reply := e.handler.EdgeHeartbeat(e.ctx, e.org, e.id, raw); reply.Status != http.StatusNoContent {
			t.Fatalf("edge heartbeat = %+v", reply)
		}
		if got = e.stored(); got.ICMP6 != probewire.CapabilityRaw || e.triggers() != 2 {
			t.Fatalf("changed report: icmp6 %q, %d triggers; want raw and 2", got.ICMP6, e.triggers())
		}
	})
}

// TestAliveStoresProbeCapabilities covers AK2 (edge, .probe.alive): the
// report of a monitoring alive message is stored through the hook OnAlive
// of the probe edge, as the router wires it.
func TestAliveStoresProbeCapabilities(t *testing.T) {
	capsBackends(t, func(t *testing.T, e *capsEnv) {
		edge := probe.NewEdge(nil, probe.StreamConfig{})
		// The router's hook itself: it runs on a background context without
		// a tenant scope, so a hook that does not open one fails here.
		edge.OnAlive(e.handler.RecordAliveProbeCapabilities)
		now := time.Now().UTC()
		alive := &probewire.Alive{CollectorTime: now, QueueState: probewire.QueueOK, ICMPCapability: probewire.ICMPUnprivileged,
			ProbeCapabilities: sampleCaps(now)}
		raw, err := probewire.Encode(alive)
		if err != nil {
			t.Fatal(err)
		}
		if reply, ok := edge.ProbeAlive(e.ctx, e.org, e.id, raw).(probe.Reply); !ok || reply.Status != http.StatusOK {
			t.Fatalf("alive = %+v", reply)
		}
		if got := e.stored(); got == nil || !got.Equal(sampleCaps(now)) || e.triggers() != 1 {
			t.Fatalf("stored report %+v after the alive message, %d triggers", got, e.triggers())
		}
	})
}

// TestHeartbeatWithoutCapabilitiesKeepsStoredValue covers AK2: a heartbeat
// or alive message without the field (an older collector) leaves the
// stored report unchanged.
func TestHeartbeatWithoutCapabilitiesKeepsStoredValue(t *testing.T) {
	capsBackends(t, func(t *testing.T, e *capsEnv) {
		if got := e.stored(); got != nil {
			t.Fatalf("report %+v before any, want none", got)
		}
		at := time.Date(2026, 11, 14, 9, 0, 0, 0, time.UTC)
		e.heartbeat(discovery.CollectorHeartbeatRequest{ProbeCapabilities: sampleCaps(at)})
		e.heartbeat(map[string]any{"spool": map[string]any{"messages": 1}})
		if reply := e.handler.EdgeHeartbeat(e.ctx, e.org, e.id, nil); reply.Status != http.StatusNoContent {
			t.Fatalf("edge heartbeat = %+v", reply)
		}
		e.handler.RecordProbeCapabilities(e.ctx, e.org, e.id, nil)
		if got := e.stored(); got == nil || !got.Equal(sampleCaps(at)) || !got.DetectedAt.Equal(at) || e.triggers() != 1 {
			t.Fatalf("report %+v, %d triggers after heartbeats without it; want the stored one and 1", got, e.triggers())
		}
	})
}
