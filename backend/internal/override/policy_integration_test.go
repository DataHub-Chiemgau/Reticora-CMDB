package override

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Source ranks per attribute (REC-04, WP-1105, E-233 option 1): the
// central write decision ranks both sources by the entry of the CI type,
// else the organization-wide entry, else REC-03; manual stays 100; PUT
// source-policy is gone; a change is audited, announced over the
// dispatcher (not the outbox) and applies at once on the writing instance.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestAttributeRankSNMPOverAgent|TestTypeEntryBeforeOrgEntry|TestManualRankRejected|TestOverrideStillWins|TestSourcePolicyPutGone|TestSourceRanksAuditAndEvent|TestSourceRanksCacheInvalidated' ./internal/override/

// recordingDispatcher records the events the handler dispatches.
type recordingDispatcher struct {
	mu     sync.Mutex
	events []dispatched
}

type dispatched struct {
	org, event string
	payload    any
}

func (d *recordingDispatcher) Dispatch(_ context.Context, orgID, event string, payload any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, dispatched{orgID, event, payload})
}

type rankEnv struct {
	f        *scopetest.Fixture
	repo     *PGRepository
	router   chi.Router
	events   *recordingDispatcher
	user     string
	server   string // CI type server (system)
	app      string // CI type application (system)
	ctx      context.Context
	observed time.Time
}

func newRankEnv(t *testing.T) *rankEnv {
	t.Helper()
	f := scopetest.Seed(t, "17")
	ranksOfInstance.invalidate(f.OrgA)
	t.Cleanup(func() { ranksOfInstance.invalidate(f.OrgA) })
	e := &rankEnv{f: f, repo: NewPGRepository(f.App), events: &recordingDispatcher{}, observed: time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)}
	e.router = chi.NewRouter()
	NewHandler(e.repo, e.events).RegisterRoutes(e.router)
	e.user = f.AppUser(t, f.OrgA, "rank-admin")
	scope := database.OrgWideScope(f.OrgA, e.user)
	e.ctx = tenant.WithTenant(database.ContextWithTenantScope(context.Background(), &scope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: e.user})
	for key, dst := range map[string]*string{"server": &e.server, "application": &e.app} {
		if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).Scan(dst); err != nil {
			t.Fatalf("CI type %s: %v", key, err)
		}
	}
	return e
}

// ci creates a CI of the type.
func (e *rankEnv) ci(t *testing.T, name, typeID string) string {
	t.Helper()
	id := e.f.CI(t, e.f.OrgA, "", name)
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, id, typeID); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *rankEnv) put(t *testing.T, entries []RankEntry) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(ReplaceSourceRanksRequest{Entries: entries})
	if err != nil {
		t.Fatal(err)
	}
	return e.do(t, http.MethodPut, "/api/v1/reconciliation/source-ranks", raw)
}

func (e *rankEnv) do(t *testing.T, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(e.ctx)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	e.router.ServeHTTP(rec, req)
	return rec
}

// agentValue records an agent value of field at e.observed.
func (e *rankEnv) agentValue(t *testing.T, ciID, field string) {
	t.Helper()
	if _, err := e.repo.RecordDiscovered(e.ctx, e.f.OrgA, ciID, field, "agent-value", "agent", e.observed); err != nil {
		t.Fatal(err)
	}
}

func (e *rankEnv) decide(ciID, field, source string, observed time.Time) Decision {
	return DecideAutomatedWrite(e.ctx, e.repo, &Write{OrganizationID: e.f.OrgA, CIID: ciID, Field: field, Value: source + "-value",
		Source: source, ObservedAt: observed, Current: "agent-value"})
}

// TestAttributeRankSNMPOverAgent: snmp with rank 85 for firmware_version
// beats an agent value (85) only with a newer observed_at; for os_version
// snmp keeps 80 < 85.
func TestAttributeRankSNMPOverAgent(t *testing.T) {
	e := newRankEnv(t)
	host := e.ci(t, "rank-host", e.server)
	e.agentValue(t, host, "firmware_version")
	e.agentValue(t, host, "os_version")
	if d := e.decide(host, "firmware_version", "snmp", e.observed.Add(time.Hour)); d.Write {
		t.Fatalf("snmp before the entry: %+v, want refused", d)
	}
	if rec := e.put(t, []RankEntry{{AttributeKey: "firmware_version", Source: "snmp", Rank: 85}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	if d := e.decide(host, "firmware_version", "snmp", e.observed); d.Write || d.Reason != ReasonStale {
		t.Errorf("snmp 85 with the same observed_at: %+v, want stale", d)
	}
	if d := e.decide(host, "firmware_version", "snmp", e.observed.Add(time.Minute)); !d.Write {
		t.Errorf("snmp 85 with a newer observed_at: %+v, want a write", d)
	}
	if d := e.decide(host, "os_version", "snmp", e.observed.Add(time.Minute)); d.Write || d.Reason != ReasonLowerRank {
		t.Errorf("snmp on os_version: %+v, want lower_rank (80 < 85)", d)
	}
	// discovered_rank records the effective rank of the recording.
	fv, err := e.repo.RecordDiscovered(e.ctx, e.f.OrgA, host, "firmware_version", "snmp-value", "snmp", e.observed.Add(time.Minute))
	if err != nil || fv.DiscoveredRank != 85 {
		t.Errorf("recorded snmp value: %+v %v, want discovered_rank 85", fv, err)
	}
}

// TestTypeEntryBeforeOrgEntry: a type entry 70 beats an organization entry
// 90 for CIs of that type; CIs of other types use 90.
func TestTypeEntryBeforeOrgEntry(t *testing.T) {
	e := newRankEnv(t)
	server := e.ci(t, "rank-server", e.server)
	app := e.ci(t, "rank-app", e.app)
	for _, id := range []string{server, app} {
		e.agentValue(t, id, "firmware_version")
	}
	rec := e.put(t, []RankEntry{
		{CITypeID: e.server, AttributeKey: "firmware_version", Source: "snmp", Rank: 70},
		{AttributeKey: "firmware_version", Source: "snmp", Rank: 90},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	later := e.observed.Add(time.Hour)
	if d := e.decide(server, "firmware_version", "snmp", later); d.Write || d.Reason != ReasonLowerRank {
		t.Errorf("server CI: %+v, want lower_rank (type entry 70 < agent 85)", d)
	}
	if d := e.decide(app, "firmware_version", "snmp", later); !d.Write {
		t.Errorf("application CI: %+v, want a write (org entry 90)", d)
	}
	rec = e.do(t, http.MethodGet, "/api/v1/reconciliation/source-ranks?ci_type_id="+e.server+"&attribute_key=firmware_version", nil)
	var got SourceRanksResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("GET: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	if len(got.Entries) != 2 || len(got.Effective) != 1 || len(got.Effective[0].Ranks) != len(ConfigurableSources) {
		t.Fatalf("GET: %+v, want both entries and one effective table of every source", got)
	}
	origins := map[string]string{}
	for _, r := range got.Effective[0].Ranks {
		origins[r.Source] = r.Origin
		if r.Source == "snmp" && r.Rank != 70 {
			t.Errorf("effective snmp rank %d, want 70", r.Rank)
		}
	}
	if origins["snmp"] != OriginType || origins["agent"] != OriginDefault {
		t.Errorf("origins %v, want snmp type and agent default", origins)
	}
	// A type id in upper case names the same type: PUT accepts it, a
	// duplicate in other case is refused, GET resolves the type entry.
	upper := strings.ToUpper(e.server)
	if rec = e.put(t, []RankEntry{{CITypeID: upper, AttributeKey: "firmware_version", Source: "snmp", Rank: 70},
		{AttributeKey: "firmware_version", Source: "snmp", Rank: 90}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT with an upper-case type id: %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rec = e.put(t, []RankEntry{{CITypeID: upper, AttributeKey: "x", Source: "snmp", Rank: 50},
		{CITypeID: e.server, AttributeKey: "x", Source: "snmp", Rank: 60}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("duplicate in other case: %d %s, want 422", rec.Code, rec.Body.String())
	}
	rec = e.do(t, http.MethodGet, "/api/v1/reconciliation/source-ranks?ci_type_id="+upper+"&attribute_key=firmware_version", nil)
	got = SourceRanksResponse{}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || len(got.Entries) != 2 || len(got.Effective) != 1 {
		t.Fatalf("GET with an upper-case type id: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	for _, r := range got.Effective[0].Ranks {
		if r.Source == "snmp" && (r.Rank != 70 || r.Origin != OriginType) {
			t.Errorf("effective snmp for the upper-case type id: %+v, want 70 from the type entry", r)
		}
	}
	// An unknown CI type is 422.
	if rec = e.put(t, []RankEntry{{CITypeID: e.f.ID(), AttributeKey: "x", Source: "snmp", Rank: 50}}); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown CI type: %d %s, want 422", rec.Code, rec.Body.String())
	}
}

// TestManualRankRejected: source manual and rank 100 are 422 and store
// nothing.
func TestManualRankRejected(t *testing.T) {
	e := newRankEnv(t)
	for _, entries := range [][]RankEntry{
		{{AttributeKey: "firmware_version", Source: "manual", Rank: 50}},
		{{AttributeKey: "firmware_version", Source: "snmp", Rank: 100}},
		{{AttributeKey: "firmware_version", Source: "override", Rank: 10}},
	} {
		if rec := e.put(t, entries); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("PUT %+v: %d %s, want 422", entries, rec.Code, rec.Body.String())
		}
	}
	stored, err := e.repo.SourceRankEntries(e.ctx, e.f.OrgA)
	if err != nil || len(stored) != 0 {
		t.Errorf("stored entries %+v %v, want none", stored, err)
	}
}

// TestOverrideStillWins: a manual override stays at any rank; the value
// becomes a conflict.
func TestOverrideStillWins(t *testing.T) {
	e := newRankEnv(t)
	host := e.ci(t, "rank-override", e.server)
	if _, err := e.repo.SetOverride(e.ctx, e.f.OrgA, host, "firmware_version", "manual-value", e.user, "pinned", true); err != nil {
		t.Fatal(err)
	}
	if rec := e.put(t, []RankEntry{{AttributeKey: "firmware_version", Source: "import", Rank: 99}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	d := DecideAutomatedWrite(e.ctx, e.repo, &Write{OrganizationID: e.f.OrgA, CIID: host, Field: "firmware_version", Value: "imported",
		Source: "import", ObservedAt: time.Now().Add(time.Hour), Current: "manual-value"})
	if d.Write || !d.Conflict || d.Reason != ReasonOverrideConflict {
		t.Errorf("import 99 against the override: %+v, want a conflict", d)
	}
}

// TestSourcePolicyPutGone: PUT source-policy is 410, GET answers 200 with
// Deprecation and the successor link.
func TestSourcePolicyPutGone(t *testing.T) {
	e := newRankEnv(t)
	rec := e.do(t, http.MethodPut, "/api/v1/reconciliation/source-policy", []byte(`{"name":"x","priorities":["snmp"]}`))
	if rec.Code != http.StatusGone || rec.Header().Get("Link") == "" {
		t.Errorf("PUT source-policy: %d Link %q %s, want 410 with a link", rec.Code, rec.Header().Get("Link"), rec.Body.String())
	}
	var stored int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM source_priority_policy WHERE organization_id = $1`, e.f.OrgA).Scan(&stored); err != nil || stored != 0 {
		t.Errorf("source_priority_policy rows %d %v, want none", stored, err)
	}
	rec = e.do(t, http.MethodGet, "/api/v1/reconciliation/source-policy", nil)
	var policy SourcePolicy
	if err := json.Unmarshal(rec.Body.Bytes(), &policy); err != nil || rec.Code != http.StatusOK || rec.Header().Get("Deprecation") == "" {
		t.Fatalf("GET source-policy: %d Deprecation %q %v", rec.Code, rec.Header().Get("Deprecation"), err)
	}
	if len(policy.Priorities) != len(DefaultPriorities) || policy.Priorities[0] != DefaultPriorities[0] {
		t.Errorf("GET source-policy priorities %v, want DefaultPriorities", policy.Priorities)
	}
}

// TestSourceRanksAuditAndEvent: a change writes the audit entry with
// before and after; the dispatcher gets reconciliation.policy_changed with
// the changed keys; the outbox holds no row for it.
func TestSourceRanksAuditAndEvent(t *testing.T) {
	e := newRankEnv(t)
	if rec := e.put(t, []RankEntry{{AttributeKey: "firmware_version", Source: "snmp", Rank: 85}, {AttributeKey: "model", Source: "ssh", Rank: 65}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT 1: %d %s", rec.Code, rec.Body.String())
	}
	// The second PUT changes model only (firmware_version stays).
	if rec := e.put(t, []RankEntry{{AttributeKey: "firmware_version", Source: "snmp", Rank: 85}, {AttributeKey: "model", Source: "ssh", Rank: 66}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT 2: %d %s", rec.Code, rec.Body.String())
	}
	bg := context.Background()
	var audits int
	var changes []byte
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) OVER (), changes::text::bytea FROM audit_log
		WHERE organization_id = $1 AND action = $2 ORDER BY seq DESC LIMIT 1`, e.f.OrgA, AuditSourceRanksUpdated).Scan(&audits, &changes); err != nil {
		t.Fatalf("audit: %v", err)
	}
	var diff struct{ Before, After []map[string]any }
	if err := json.Unmarshal(changes, &diff); err != nil || audits != 2 || len(diff.Before) != 2 || len(diff.After) != 2 {
		t.Fatalf("audit entries %d, changes %s %v; want 2 with before and after", audits, changes, err)
	}
	e.events.mu.Lock()
	events := append([]dispatched(nil), e.events.events...)
	e.events.mu.Unlock()
	if len(events) != 2 || events[1].event != "reconciliation.policy_changed" || events[1].org != e.f.OrgA {
		t.Fatalf("events %+v, want two reconciliation.policy_changed", events)
	}
	keys, _ := events[1].payload.(map[string]any)["attribute_keys"].([]string)
	if len(keys) != 1 || keys[0] != "model" {
		t.Errorf("changed keys %v, want [model]", keys)
	}
	var outbox int
	if err := e.f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'reconciliation.policy_changed'`, e.f.OrgA).Scan(&outbox); err != nil || outbox != 0 {
		t.Errorf("outbox rows %d %v, want none", outbox, err)
	}
}

// TestSourceRanksCacheInvalidated: after PUT the new rank applies at once
// on the same instance, although the table was cached before.
func TestSourceRanksCacheInvalidated(t *testing.T) {
	e := newRankEnv(t)
	host := e.ci(t, "rank-cache", e.server)
	e.agentValue(t, host, "firmware_version")
	later := e.observed.Add(time.Hour)
	// The first decision caches the empty table.
	if d := e.decide(host, "firmware_version", "snmp", later); d.Write {
		t.Fatalf("before: %+v, want refused", d)
	}
	if _, _, ok := ranksOfInstance.get(e.f.OrgA); !ok {
		t.Fatal("the decision cached no rank table")
	}
	if rec := e.put(t, []RankEntry{{AttributeKey: "firmware_version", Source: "snmp", Rank: 95}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT: %d %s", rec.Code, rec.Body.String())
	}
	if d := e.decide(host, "firmware_version", "snmp", later); !d.Write {
		t.Errorf("after PUT: %+v, want a write with rank 95", d)
	}
}
