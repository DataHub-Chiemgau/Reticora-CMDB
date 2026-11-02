package agent_test

// WP-484 (t3-agent-merge; AGT-05, OVR-02, F-083): agent telemetry follows a
// merge of the agent's CI. The merge moves endpoint_agent.ci_id to the
// survivor; anchorCI follows merged_into for a link that still names the
// victim; only a linked CI deleted without tombstone is reconciled afresh
// (E-20); every other read error refuses the telemetry with 503, and no CI
// is created.
//
// PostgreSQL repositories (agent, CI with the real merge, discovery type
// resolver, override guard) like the production wiring.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/ratchet?sslmode=disable \
//     go test -count=1 -v -run 'TestAuditT3AgentTelemetryFollowsMergedCI|TestAgentTelemetry' ./internal/agent/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// mergeEnv is one organization with an agent on a hostname and the
// production wiring of the telemetry handler.
type mergeEnv struct {
	t     *testing.T
	f     *scopetest.Fixture
	ctx   context.Context
	host  string
	agent string
	repo  *agent.PGRepository
	cis   *ci.PGRepository
}

func newMergeEnv(t *testing.T, host, agentID string) *mergeEnv {
	t.Helper()
	f := scopetest.Seed(t, "ce")
	e := &mergeEnv{t: t, f: f, host: host, agent: agentID,
		ctx:  tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA}),
		repo: agent.NewPGRepository(f.App), cis: ci.NewPGRepository(f.App)}
	if err := e.repo.Register(e.ctx, &agent.Agent{OrganizationID: f.OrgA, AgentID: agentID, Hostname: host, Status: "online",
		Policy: agent.DefaultPolicy()}); err != nil {
		t.Fatal(err)
	}
	return e
}

// hostCI inserts an active CI carrying the hostname.
func (e *mergeEnv) hostCI() string {
	e.t.Helper()
	id := e.f.CI(e.t, e.f.OrgA, "", e.host)
	e.exec(`UPDATE ci SET hostname = $2 WHERE id = $1`, id, e.host)
	return id
}

func (e *mergeEnv) exec(query string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), query, args...); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
}

// active counts the active CIs carrying the hostname.
func (e *mergeEnv) active() int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM ci WHERE organization_id = $1 AND hostname = $2 AND deleted_at IS NULL`,
		e.f.OrgA, e.host).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// linked is the CI the agent row names.
func (e *mergeEnv) linked() string {
	e.t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(ci_id::text, '') FROM endpoint_agent WHERE organization_id = $1 AND agent_id = $2`,
		e.f.OrgA, e.agent).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// telemetry posts one telemetry of the agent through the handler on cis.
func (e *mergeEnv) telemetry(cis agent.CIUpserter) *httptest.ResponseRecorder {
	e.t.Helper()
	mux := chi.NewRouter()
	agent.NewHandler(e.repo, nil, cis, discovery.NewPGRepository(e.f.App)).
		WithFieldGuard(override.NewGuard(override.NewPGRepository(e.f.App))).RegisterRoutes(mux)
	body, err := json.Marshal(agent.TelemetryPayload{AgentID: e.agent, Hostname: e.host, OS: "windows", Version: "1.0"})
	if err != nil {
		e.t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/agents/telemetry", bytes.NewReader(body)).WithContext(e.ctx))
	return w
}

// anchored is the ci_id of an accepted telemetry.
func (e *mergeEnv) anchored(w *httptest.ResponseRecorder) string {
	e.t.Helper()
	if w.Code != http.StatusAccepted {
		e.t.Fatalf("telemetry: %d %s", w.Code, w.Body.String())
	}
	var out struct {
		CIID string `json:"ci_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		e.t.Fatal(err)
	}
	return out.CIID
}

// TestAuditT3AgentTelemetryFollowsMergedCI: after the merge of the agent's
// CI the agent names the survivor, its telemetry updates the survivor and
// exactly one active CI carries the hostname (no duplicate, the merge is
// not undone).
func TestAuditT3AgentTelemetryFollowsMergedCI(t *testing.T) {
	e := newMergeEnv(t, "audit-t3-laptop", "audit-t3-agent")
	victim, survivor := e.hostCI(), e.hostCI()
	if err := e.repo.SetCI(e.ctx, e.f.OrgA, e.agent, victim); err != nil {
		t.Fatal(err)
	}
	res, err := e.cis.Merge(e.ctx, e.f.OrgA, survivor, victim, "")
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if res.Moved["agents"] != 1 || e.linked() != survivor {
		t.Errorf("merge moved %d agents, agent linked to %s; want 1 and the survivor %s", res.Moved["agents"], e.linked(), survivor)
	}
	before := e.active()
	if got := e.anchored(e.telemetry(e.cis)); got != survivor {
		t.Errorf("telemetry after the merge anchored to CI %s, want the merge survivor %s", got, survivor)
	}
	if after := e.active(); after != before || after != 1 {
		t.Errorf("active CIs with hostname %s: %d before the telemetry, %d after; want exactly 1 (duplicate created, merge silently undone)",
			e.host, before, after)
	}
}

// TestAgentTelemetryFollowsTombstoneOfStaleLink: an agent link that still
// names the merged victim (merged before the merge moved agents) follows
// merged_into to the survivor and is relinked; no CI is created.
func TestAgentTelemetryFollowsTombstoneOfStaleLink(t *testing.T) {
	e := newMergeEnv(t, "wp484-stale", "wp484-stale-agent")
	victim, survivor := e.hostCI(), e.hostCI()
	if _, err := e.cis.Merge(e.ctx, e.f.OrgA, survivor, victim, ""); err != nil {
		t.Fatalf("merge: %v", err)
	}
	e.exec(`UPDATE endpoint_agent SET ci_id = $3 WHERE organization_id = $1 AND agent_id = $2`, e.f.OrgA, e.agent, victim)
	if got := e.anchored(e.telemetry(e.cis)); got != survivor {
		t.Errorf("telemetry anchored to CI %s, want the survivor %s", got, survivor)
	}
	if e.linked() != survivor || e.active() != 1 {
		t.Errorf("agent linked to %s (want %s), %d active CIs with the hostname (want 1)", e.linked(), survivor, e.active())
	}
}

// TestAgentTelemetryDeletedLinkedCIReconciledAfresh: a linked CI deleted
// without tombstone reconciles the agent like an unlinked one (E-20): no
// CI carries the hostname, so the endpoint CI is created and linked;
// several CIs carry it, so the agent stays unlinked and none is created.
func TestAgentTelemetryDeletedLinkedCIReconciledAfresh(t *testing.T) {
	e := newMergeEnv(t, "wp484-deleted", "wp484-deleted-agent")
	gone := e.hostCI()
	if err := e.repo.SetCI(e.ctx, e.f.OrgA, e.agent, gone); err != nil {
		t.Fatal(err)
	}
	e.exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, gone)
	created := e.anchored(e.telemetry(e.cis))
	if created == "" || created == gone || e.linked() != created || e.active() != 1 {
		t.Fatalf("deleted linked CI: anchored %q, linked %s, %d active; want a new CI, linked, 1 active", created, e.linked(), e.active())
	}

	e.exec(`UPDATE ci SET deleted_at = now() WHERE id = $1`, created)
	e.hostCI()
	e.hostCI()
	if got := e.anchored(e.telemetry(e.cis)); got != "" {
		t.Errorf("hostname matches two CIs: anchored to %s, want none (CH29)", got)
	}
	if e.active() != 2 {
		t.Errorf("%d active CIs with the hostname, want the 2 existing ones", e.active())
	}
}

// failingCIs answers the CI reads with a transient error.
type failingCIs struct {
	*ci.PGRepository
	getErr, tombstoneErr error
}

func (r *failingCIs) GetByID(ctx context.Context, orgID, id string) (*ci.Item, error) {
	if r.getErr != nil {
		return nil, r.getErr
	}
	return r.PGRepository.GetByID(ctx, orgID, id)
}

func (r *failingCIs) MergedInto(ctx context.Context, orgID, victimID string) (string, bool, error) {
	if r.tombstoneErr != nil {
		return "", false, r.tombstoneErr
	}
	return r.PGRepository.MergedInto(ctx, orgID, victimID)
}

// TestAgentTelemetryTransientCIErrorIsUnavailable: a read error of the
// linked CI or of its tombstone other than "not found" refuses the
// telemetry with 503 and Retry-After (the collector repeats it); no CI is
// created and the link stays.
func TestAgentTelemetryTransientCIErrorIsUnavailable(t *testing.T) {
	e := newMergeEnv(t, "wp484-transient", "wp484-transient-agent")
	victim, survivor := e.hostCI(), e.hostCI()
	if _, err := e.cis.Merge(e.ctx, e.f.OrgA, survivor, victim, ""); err != nil {
		t.Fatalf("merge: %v", err)
	}
	timeout := &pgconn.PgError{Code: "57014", Message: "canceling statement due to statement timeout"}
	for name, c := range map[string]struct {
		link string
		cis  *failingCIs
	}{
		"linked CI read":    {survivor, &failingCIs{PGRepository: e.cis, getErr: errors.New("conn reset by peer")}},
		"statement timeout": {survivor, &failingCIs{PGRepository: e.cis, getErr: timeout}},
		"tombstone read":    {victim, &failingCIs{PGRepository: e.cis, tombstoneErr: errors.New("conn reset by peer")}},
	} {
		t.Run(name, func(t *testing.T) {
			e.exec(`UPDATE endpoint_agent SET ci_id = $3 WHERE organization_id = $1 AND agent_id = $2`, e.f.OrgA, e.agent, c.link)
			before := e.active()
			w := e.telemetry(c.cis)
			if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
				t.Errorf("telemetry: %d (Retry-After %q) %s; want 503 with Retry-After", w.Code, w.Header().Get("Retry-After"), w.Body.String())
			}
			if after := e.active(); after != before {
				t.Errorf("active CIs with the hostname: %d before, %d after; want unchanged", before, after)
			}
			if e.linked() != c.link {
				t.Errorf("agent linked to %s, want unchanged %s", e.linked(), c.link)
			}
		})
	}
}
