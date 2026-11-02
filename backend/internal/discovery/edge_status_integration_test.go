// The collector status on the edge data path and the scope channel, and the
// 404 of PATCH /collectors/{id} (WP-380; F-025 data part, F-257).
//
//   - TestAuditEdgeIngestRefusesDisabledOrUnknownCollector – WP-145 P1
//     (moved from ingest_auditdefect_test.go): EdgeIngest refuses the records
//     of a disabled or unknown collector and creates no CI (COL-04); the
//     active collector is the control.
//   - TestAuditDisabledCollectorScopeChannelRefused – WP-145, WP-153 P1
//     (moved from disabled_collector_channel_auditdefect_test.go):
//     RecordCollectorScopeRun (scopes.result) and SuggestScopes
//     (subnets.report) refuse a disabled collector on PostgreSQL; its scope
//     keeps its health and no suggestion is opened.
//   - TestCollectorEdgeRefusesUnknownOrDisabledCollector: over NATS, every
//     subject the dispatchers of ServeCollectorEdge and ServeCollectorScopes
//     register is refused for a disabled and an unknown collector before its
//     handler runs, and nothing is persisted (no CI, no heartbeat, no scope
//     run, no suggestion, no job status); the active collector gets the
//     unchanged answers.
//     The table must cover every registered suffix, so a later one gets a
//     case too.
//   - TestUpdateCollectorUnknownOrForeignIs404: PATCH on a random UUID, a
//     malformed id and a collector of another organization answers 404 and
//     changes nothing; the own collector 200 (F-257).
//
// Retest (F-025 data part, F-257):
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_URL=nats://localhost:54222 go test -count=1 -v -p 1 \
//	  -run 'TestAuditEdgeIngestRefusesDisabledOrUnknownCollector|TestAuditDisabledCollectorScopeChannelRefused|TestCollectorEdgeRefusesUnknownOrDisabledCollector|TestUpdateCollectorUnknownOrForeignIs404' ./internal/discovery/
package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
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

func TestAuditDisabledCollectorScopeChannelRefused(t *testing.T) {
	f := scopetest.Seed(t, "de")
	org := f.OrgCtx(f.OrgA)
	repo := NewPGRepository(f.App)
	h := NewHandler(repo, nil)
	disabledStatus := CollectorDisabled

	for _, tc := range []struct {
		name    string
		disable bool
		host    string // the collector's scope
		cidr    string // the subnet it reports
	}{
		{name: "control: active collector", host: "10.99.0.1", cidr: "10.99.1.0/24"},
		{name: "disabled collector", disable: true, host: "10.99.0.2", cidr: "10.99.2.0/24"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			col := &Collector{OrganizationID: f.OrgA, Name: "audit-dis-" + tc.cidr, Status: CollectorActive, Config: map[string]any{}}
			if err := repo.RegisterCollector(org, col); err != nil {
				t.Fatal(err)
			}
			scope := NewScope(f.OrgA, &ScopeInput{CollectorID: col.ID, Kind: "host", Target: tc.host})
			if err := repo.CreateScope(org, scope); err != nil {
				t.Fatal(err)
			}
			if tc.disable {
				if _, err := repo.UpdateCollector(org, f.OrgA, col.ID, &CollectorPatch{Status: &disabledStatus}); err != nil {
					t.Fatal(err)
				}
			}
			refused := 0
			runErr := h.RecordCollectorScopeRun(org, f.OrgA, col.ID, &ScopeResultReport{ScopeID: scope.ID, At: time.Now().UTC(),
				NextRun: time.Now().Add(24 * time.Hour).UTC(), Result: map[string]any{"records": 7, "error_count": 0}})
			got, gerr := repo.GetScope(org, f.OrgA, scope.ID)
			if gerr != nil {
				t.Fatal(gerr)
			}
			if runErr != nil && got.LastRunAt == "" {
				refused++
			} else if tc.disable {
				t.Errorf("COL-04 violated: scopes.result of the disabled collector was recorded (err=%v, last_run_at=%q)", runErr, got.LastRunAt)
			}
			n, sugErr := h.SuggestScopes(org, f.OrgA, col.ID, []string{tc.cidr})
			if sugErr != nil && n == 0 {
				refused++
			} else if tc.disable {
				t.Errorf("COL-04 violated: subnets.report of the disabled collector opened %d scope suggestion(s) (err=%v)", n, sugErr)
			}
			if !tc.disable && refused != 0 {
				t.Errorf("control: the active collector's report or subnets were refused (%d): run err=%v, suggest err=%v", refused, runErr, sugErr)
			}
		})
	}
}

func TestCollectorEdgeRefusesUnknownOrDisabledCollector(t *testing.T) {
	f := scopetest.Seed(t, "de")
	url := os.Getenv("TEST_NATS_URL")
	if url == "" {
		t.Fatal("TEST_NATS_URL is required next to TEST_DATABASE_URL: the edge data path and the scope channel run over NATS")
	}
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)
	repo := NewPGRepository(f.App)
	h := NewHandler(repo, ci.NewPGRepository(f.App))
	disabled := CollectorDisabled

	// sender is a collector of the table: the scope it reports a run of, the
	// discovery job it asks for and reports running (WP-632), the device and
	// the subnet it reports, and the edge status of its refusal (0 for the
	// active control).
	type sender struct {
		name, id, scopeID, jobID, ip, cidr string
		status                             int
	}
	register := func(name, host string, disable bool) *sender {
		t.Helper()
		c := &Collector{OrganizationID: f.OrgA, ClientID: f.Client1, Name: "edge-status-" + name, Status: CollectorActive, Config: map[string]any{}}
		if err := repo.RegisterCollector(org, c); err != nil {
			t.Fatal(err)
		}
		s := NewScope(f.OrgA, &ScopeInput{CollectorID: c.ID, Kind: "host", Target: host})
		if err := repo.CreateScope(org, s); err != nil {
			t.Fatal(err)
		}
		job := &Job{OrganizationID: f.OrgA, CollectorID: c.ID, JobType: JobTypeSweep}
		if err := repo.CreateJob(org, job); err != nil {
			t.Fatal(err)
		}
		if disable {
			if _, err := repo.UpdateCollector(org, f.OrgA, c.ID, &CollectorPatch{Status: &disabled}); err != nil {
				t.Fatal(err)
			}
		}
		// An old heartbeat, so that a recorded one shows.
		if _, err := f.Admin.Exec(bg, `UPDATE collector SET last_heartbeat_at = now() - interval '1 hour' WHERE id = $1`, c.ID); err != nil {
			t.Fatal(err)
		}
		return &sender{name: name, id: c.ID, scopeID: s.ID, jobID: job.ID}
	}
	active := register("active", "10.96.0.1", false)
	active.ip, active.cidr = "192.0.2.91", "10.96.1.0/24"
	off := register("disabled", "10.96.0.2", true)
	off.ip, off.cidr, off.status = "192.0.2.92", "10.96.2.0/24", http.StatusForbidden
	// The unknown collector reports a run of the disabled collector's scope
	// and its job: the dispatcher refuses it before the owner is compared.
	unknown := &sender{name: "unknown", id: f.ID(), scopeID: off.scopeID, jobID: off.jobID, ip: "192.0.2.93", cidr: "10.96.3.0/24", status: http.StatusNotFound}

	count := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	decode := func(t *testing.T, raw []byte, out any) {
		t.Helper()
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("reply %s: %v", raw, err)
		}
	}
	serial := func(s *sender) string { return "EDGE-STATUS-" + strings.ToUpper(s.name) }
	cases := []struct {
		suffix string
		body   func(s *sender) any
		check  func(t *testing.T, s *sender, raw []byte)
	}{
		{IngestSuffix, func(s *sender) any {
			return DeviceRecordBatch{Records: []DeviceRecord{plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server",
				Name: "edge-status-" + s.name, IP: s.ip, Serial: serial(s), Model: "m"}, time.Now().UTC().Add(-time.Minute))}}
		}, func(t *testing.T, s *sender, raw []byte) {
			var r EdgeReply
			decode(t, raw, &r)
			n := count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial(s))
			if s.status == 0 {
				if r.Status != http.StatusAccepted || r.Result == nil || r.Result.Created != 1 || n != 1 {
					t.Errorf("control: ingest = %s, CIs %d; want 202 with one created CI", raw, n)
				}
			} else if r.Status != s.status || n != 0 {
				t.Errorf("COL-04 violated: ingest of the %s collector = %s, CIs %d; want %d and no CI", s.name, raw, n, s.status)
			}
		}},
		{HeartbeatSuffix, func(*sender) any { return map[string]any{} }, func(t *testing.T, s *sender, raw []byte) {
			var r EdgeReply
			decode(t, raw, &r)
			recent := count(t, `SELECT count(*) FROM collector WHERE id = $1 AND last_heartbeat_at > now() - interval '10 minutes'`, s.id)
			switch {
			case s.status == 0:
				if r.Status != http.StatusNoContent || r.CollectorStatus != CollectorActive || recent != 1 {
					t.Errorf("control: heartbeat = %s, recorded %d; want 204, status active and the heartbeat recorded", raw, recent)
				}
			case r.Status != s.status || recent != 0:
				t.Errorf("COL-04 violated: heartbeat of the %s collector = %s, recorded %d; want %d and no heartbeat", s.name, raw, recent, s.status)
			case s == off && count(t, `SELECT count(*) FROM collector WHERE id = $1 AND status = 'disabled'`, s.id) != 1:
				t.Errorf("the heartbeat changed the status of the disabled collector")
			case s == unknown && count(t, `SELECT count(*) FROM collector WHERE id = $1`, s.id) != 0:
				t.Errorf("the heartbeat of the unknown collector created a collector")
			}
		}},
		{ScopesRequestSuffix, func(*sender) any { return map[string]any{} }, func(t *testing.T, s *sender, raw []byte) {
			var r ScopesResponse
			decode(t, raw, &r)
			if s.status == 0 {
				if r.Error != "" || len(r.Scopes) != 1 || r.Scopes[0].ID != s.scopeID {
					t.Errorf("control: scopes.request = %s; want the collector's scope", raw)
				}
			} else if r.Error != "not available" || len(r.Scopes) != 0 {
				t.Errorf("COL-04 violated: scopes.request of the %s collector = %s; want error not available, no scopes", s.name, raw)
			}
		}},
		{ScopesResultSuffix, func(s *sender) any {
			return ScopeResultReport{ScopeID: s.scopeID, At: time.Now().UTC().Truncate(time.Second),
				NextRun: time.Now().Add(24 * time.Hour).UTC().Truncate(time.Second), Result: map[string]any{"records": 3, "error_count": 0}}
		}, func(t *testing.T, s *sender, raw []byte) {
			var r map[string]string
			decode(t, raw, &r)
			ran := count(t, `SELECT count(*) FROM discovery_scope WHERE id = $1 AND last_run_at IS NOT NULL`, s.scopeID)
			if s.status == 0 {
				if r["status"] != "recorded" || ran != 1 {
					t.Errorf("control: scopes.result = %s, scope run stored %d; want recorded", raw, ran)
				}
			} else if r["error"] != "not available" || ran != 0 {
				t.Errorf("COL-04 violated: scopes.result of the %s collector = %s, scope run stored %d; want the dispatcher's refusal and no run", s.name, raw, ran)
			}
		}},
		{SubnetsReportSuffix, func(s *sender) any { return SubnetsReport{Subnets: []string{s.cidr}} }, func(t *testing.T, s *sender, raw []byte) {
			var r map[string]any
			decode(t, raw, &r)
			n := count(t, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'scope_suggested' AND payload->>'collector_id' = $2`, f.OrgA, s.id)
			if s.status == 0 {
				if r["suggested"] != float64(1) || n != 1 {
					t.Errorf("control: subnets.report = %s, suggestions %d; want one", raw, n)
				}
			} else if r["error"] != "not available" || n != 0 {
				t.Errorf("COL-04 violated: subnets.report of the %s collector = %s, suggestions %d; want the dispatcher's refusal and none", s.name, raw, n)
			}
		}},
		{JobsRequestSuffix, func(*sender) any { return map[string]any{} }, func(t *testing.T, s *sender, raw []byte) {
			var r JobsResponse
			decode(t, raw, &r)
			if s.status == 0 {
				if r.Error != "" || len(r.Jobs) != 1 || r.Jobs[0].ID != s.jobID {
					t.Errorf("control: jobs.request = %s; want the collector's job", raw)
				}
			} else if r.Error != "not available" || len(r.Jobs) != 0 {
				t.Errorf("COL-04 violated: jobs.request of the %s collector = %s; want error not available, no jobs", s.name, raw)
			}
		}},
		{JobsStatusSuffix, func(s *sender) any { return JobStatusReport{JobID: s.jobID, Status: JobStatusRunning} }, func(t *testing.T, s *sender, raw []byte) {
			var r map[string]string
			decode(t, raw, &r)
			running := count(t, `SELECT count(*) FROM discovery_job WHERE id = $1 AND status = 'running'`, s.jobID)
			if s.status == 0 {
				if r["status"] != "recorded" || running != 1 {
					t.Errorf("control: jobs.status = %s, job running %d; want recorded", raw, running)
				}
			} else if r["error"] != "not available" || running != 0 {
				t.Errorf("COL-04 violated: jobs.status of the %s collector = %s, job running %d; want the dispatcher's refusal and no change", s.name, raw, running)
			}
		}},
	}
	covered := make([]string, 0, len(cases))
	for _, tc := range cases {
		covered = append(covered, tc.suffix)
	}
	registered := slices.Concat(slices.Collect(maps.Keys(h.edgeHandlers())), slices.Collect(maps.Keys(h.scopeHandlers())))
	slices.Sort(covered)
	slices.Sort(registered)
	if !slices.Equal(covered, registered) {
		t.Fatalf("the table covers %v, the dispatchers register %v: every registered suffix needs a case", covered, registered)
	}

	nc, err := nats.Connect(url, nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("connect %s: %v", url, err)
	}
	defer nc.Close()
	ctx, cancel := context.WithCancel(bg)
	served := make(chan error, 2)
	go func() { served <- h.ServeCollectorEdge(ctx, nc) }()
	go func() { served <- h.ServeCollectorScopes(ctx, nc) }()
	defer func() {
		cancel()
		for range 2 {
			if serr := <-served; serr != nil {
				t.Errorf("serve: %v", serr)
			}
		}
	}()
	request := func(t *testing.T, subject string, body any) []byte {
		t.Helper()
		raw, _ := json.Marshal(body)
		deadline := time.Now().Add(5 * time.Second)
		for {
			msg, rerr := nc.Request(subject, raw, 5*time.Second)
			if errors.Is(rerr, nats.ErrNoResponders) && time.Now().Before(deadline) {
				time.Sleep(50 * time.Millisecond) // subscriptions not active yet
				continue
			}
			if rerr != nil {
				t.Fatalf("request %s: %v", subject, rerr)
			}
			return msg.Data
		}
	}

	// The refused collectors first: the totals below show that they added
	// nothing next to the control.
	for _, s := range []*sender{off, unknown, active} {
		for _, tc := range cases {
			t.Run(s.name+" "+tc.suffix, func(t *testing.T) {
				tc.check(t, s, request(t, "collector."+f.OrgA+"."+s.id+tc.suffix, tc.body(s)))
			})
		}
	}
	if n := count(t, `SELECT count(*) FROM ci WHERE organization_id = $1`, f.OrgA); n != 1 {
		t.Errorf("CIs of the organization: %d, want only the control's", n)
	}
	if n := count(t, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'scope_suggested'`, f.OrgA); n != 1 {
		t.Errorf("scope suggestions of the organization: %d, want only the control's", n)
	}
	if n := count(t, `SELECT count(*) FROM discovery_scope WHERE organization_id = $1 AND last_run_at IS NOT NULL`, f.OrgA); n != 1 {
		t.Errorf("scopes with a stored run: %d, want only the control's", n)
	}
	if n := count(t, `SELECT count(*) FROM discovery_job WHERE organization_id = $1 AND status <> 'pending'`, f.OrgA); n != 1 {
		t.Errorf("discovery jobs moved on: %d, want only the control's", n)
	}
}

func TestUpdateCollectorUnknownOrForeignIs404(t *testing.T) {
	f := scopetest.Seed(t, "de")
	bg := context.Background()
	repo := NewPGRepository(f.App)
	mux := chi.NewRouter()
	NewHandler(repo, nil).RegisterRoutes(mux)
	own := &Collector{OrganizationID: f.OrgA, Name: "f257-own", Status: CollectorActive, Config: map[string]any{}}
	if err := repo.RegisterCollector(f.OrgCtx(f.OrgA), own); err != nil {
		t.Fatal(err)
	}
	foreign := &Collector{OrganizationID: f.OrgB, Name: "f257-foreign", Status: CollectorActive, Config: map[string]any{}}
	if err := repo.RegisterCollector(f.OrgCtx(f.OrgB), foreign); err != nil {
		t.Fatal(err)
	}
	// An administrator of organization A.
	patch := func(id, body string) (int, string) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPatch, "/api/v1/collectors/"+id, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r = r.WithContext(tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code, w.Body.String()
	}
	state := func(id string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT name || '|' || status || '|' || updated_at::text FROM collector WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	foreignBefore, ownBefore := state(foreign.ID), state(own.ID)

	for _, tc := range []struct{ name, id string }{
		{"random UUID", f.ID()},
		{"malformed id", "not-a-uuid"},
		{"collector of another organization", foreign.ID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, body := patch(tc.id, `{"name":"f257-renamed","status":"disabled"}`)
			var problem api.ProblemDetail
			_ = json.Unmarshal([]byte(body), &problem)
			if code != http.StatusNotFound || problem.Status != http.StatusNotFound {
				t.Errorf("F-257: PATCH /collectors/{%s} = %d %s, want 404", tc.name, code, body)
			}
		})
	}
	if got := state(foreign.ID); got != foreignBefore {
		t.Errorf("the PATCH of organization A changed the collector of organization B: %q, was %q", got, foreignBefore)
	}
	if got := state(own.ID); got != ownBefore {
		t.Errorf("the refused PATCHes changed the own collector: %q, was %q", got, ownBefore)
	}
	if n := func() int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM collector WHERE name = 'f257-renamed'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}(); n != 0 {
		t.Errorf("%d collector(s) renamed by a refused PATCH", n)
	}

	code, body := patch(own.ID, `{"name":"f257-own-renamed"}`)
	var updated Collector
	if err := json.Unmarshal([]byte(body), &updated); err != nil || code != http.StatusOK || updated.ID != own.ID || updated.Name != "f257-own-renamed" {
		t.Errorf("PATCH of the own collector = %d %s, want 200 with the new name", code, body)
	}
}
