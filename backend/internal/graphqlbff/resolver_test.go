package graphqlbff

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// The tests of this file cover GQL-02 (WP-185): topology, rackLayout,
// searchEverything, reviewInbox and jobs resolve read-only with their
// permissions; nested CIs are read through the dataloader in one batch.

// countingCIs counts the CI reads of a request.
type countingCIs struct {
	*ci.MemoryRepository
	mu            sync.Mutex
	single, batch int
}

func (c *countingCIs) GetByID(ctx context.Context, orgID, id string) (*ci.Item, error) {
	c.mu.Lock()
	c.single++
	c.mu.Unlock()
	return c.MemoryRepository.GetByID(ctx, orgID, id)
}

func (c *countingCIs) GetByIDs(ctx context.Context, orgID string, ids []string) ([]ci.Item, error) {
	c.mu.Lock()
	c.batch++
	c.mu.Unlock()
	return c.MemoryRepository.GetByIDs(ctx, orgID, ids)
}

func (c *countingCIs) reset() {
	c.mu.Lock()
	c.single, c.batch = 0, 0
	c.mu.Unlock()
}

type bffFixture struct {
	h    *Handler
	cis  *countingCIs
	hub  string
	ids  []string
	rack string
}

func newBFFFixture(t *testing.T) *bffFixture {
	t.Helper()
	bg := context.Background()
	f := &bffFixture{cis: &countingCIs{MemoryRepository: ci.NewMemoryRepository()}}
	rels := relationship.NewMemoryRepository()
	add := func(name string) string {
		item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: "server", Status: "active", Attributes: map[string]any{}}
		if err := f.cis.Create(bg, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	f.hub = add("hub")
	for i := 0; i < 20; i++ {
		id := add(fmt.Sprintf("leaf-%02d", i))
		f.ids = append(f.ids, id)
		if err := rels.Create(bg, &relationship.Relationship{OrganizationID: "org-1", SourceCIID: id, TargetCIID: f.hub, RelType: "depends_on"}); err != nil {
			t.Fatal(err)
		}
	}
	racks := rack.NewMemoryRepository()
	rk := &rack.Rack{OrganizationID: "org-1", RoomID: "room-1", Name: "R1", HeightUnits: 4}
	if err := racks.CreateRack(bg, rk); err != nil {
		t.Fatal(err)
	}
	f.rack = rk.ID
	for i, id := range f.ids[:2] {
		if err := racks.CreateMount(bg, &rack.RackMount{OrganizationID: "org-1", RackID: rk.ID, CIID: id, PositionU: i + 1, HeightU: 1, Face: "front"}); err != nil {
			t.Fatal(err)
		}
	}
	idx := search.NewMemoryRepository()
	for _, doc := range []search.Document{
		{ID: "d1", OrganizationID: "org-1", EntityType: "ci", EntityID: f.hub, Title: "hub switch"},
		{ID: "d2", OrganizationID: "org-1", EntityType: "ticket", EntityID: "t1", Title: "hub outage"},
	} {
		if err := idx.IndexDocument(bg, doc); err != nil {
			t.Fatal(err)
		}
	}
	reviewRepo := reviews.NewMemoryRepository()
	if err := reviewRepo.CreateReviewItem(bg, &reviews.Item{OrganizationID: "org-1", Kind: reviews.KindProbableDuplicate,
		Status: reviews.StatusOpen, CandidateCIIDs: f.ids[:3], ObjectKey: "x"}); err != nil {
		t.Fatal(err)
	}
	jobSvc := jobs.NewService(jobs.NewMemoryRepository())
	jobSvc.Register(jobs.Kind{Name: "ci.bulk"})
	if _, _, err := jobSvc.Enqueue(bg, "org-1", "ci.bulk", nil, jobs.EnqueueOptions{}); err != nil {
		t.Fatal(err)
	}
	f.h = NewHandler(f.cis, rels).
		WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureTopology: true}).
		WithSources(&Sources{Topology: topology.NewHandler(f.cis, rels), Racks: racks, Search: idx, Reviews: reviewRepo, Jobs: jobSvc})
	return f
}

func (f *bffFixture) query(t *testing.T, perms []identity.Permission, q string) gqlResponse {
	t.Helper()
	f.cis.reset()
	_, resp := run(t, f.h, perms, gql(q))
	return resp
}

var allRead = []identity.Permission{identity.PermCIRead, identity.PermRelationshipRead, identity.PermTopologyRead,
	identity.PermRackRead, identity.PermSearchRead, identity.PermDiscoveryRead, identity.PermJobRead}

// TestNestedCIsUseTheDataloader: 20 relationships with both ends read the
// CIs in one batch, not 40 single reads (GQL-02, no N+1).
func TestNestedCIsUseTheDataloader(t *testing.T) {
	f := newBFFFixture(t)
	resp := f.query(t, allRead, `{ relationships(ciId: "`+f.hub+`") { relType sourceCi { name } targetCi { name } } }`)
	rels, _ := resp.Data["relationships"].([]any)
	if len(resp.Errors) != 0 || len(rels) != 20 {
		t.Fatalf("relationships: %+v", resp)
	}
	if target := rels[0].(map[string]any)["targetCi"].(map[string]any)["name"]; target != "hub" {
		t.Errorf("target %v", target)
	}
	if f.cis.single != 0 || f.cis.batch != 1 {
		t.Errorf("CI reads: %d single, %d batch", f.cis.single, f.cis.batch)
	}
	// Without ci:read the nested CIs are refused (GQL-04).
	resp = f.query(t, []identity.Permission{identity.PermRelationshipRead}, `{ relationships(ciId: "`+f.hub+`") { sourceCi { name } } }`)
	if resp.code() != codeForbidden {
		t.Errorf("nested CI without ci:read: %+v", resp.Errors)
	}
}

// TestNewQueries resolves topology, rackLayout, searchEverything,
// reviewInbox and jobs.
func TestNewQueries(t *testing.T) {
	f := newBFFFixture(t)
	resp := f.query(t, allRead, `{ topology(rootCiId: "`+f.hub+`", depth: 1) { nodes { id name ci { status } } edges { relType } } }`)
	graph, _ := resp.Data["topology"].(map[string]any)
	if nodes, _ := graph["nodes"].([]any); len(resp.Errors) != 0 || len(nodes) != 21 {
		t.Errorf("topology: %+v", resp)
	}
	if f.cis.single > 2 || f.cis.batch > 2 {
		t.Errorf("topology CI reads: %d single, %d batch", f.cis.single, f.cis.batch)
	}

	resp = f.query(t, allRead, `{ rackLayout(rackId: "`+f.rack+`") { rack { name heightUnits } mounts { positionU ci { name } } freeFront freeRear } }`)
	raw, _ := json.Marshal(resp.Data)
	if len(resp.Errors) != 0 || !strings.Contains(string(raw), `"freeFront":[3,4]`) || !strings.Contains(string(raw), `"name":"leaf-00"`) {
		t.Errorf("rackLayout: %s %+v", raw, resp.Errors)
	}
	if resp = f.query(t, allRead, `{ rackLayout(rackId: "missing") { rack { name } } }`); resp.Data["rackLayout"] != nil {
		t.Errorf("missing rack: %+v", resp)
	}

	// The ticket hit needs ticket:read, which the caller lacks (SRC-01).
	resp = f.query(t, allRead, `{ searchEverything(q: "hub") { total hits { entityType entityId title } } }`)
	if raw, _ = json.Marshal(resp.Data); len(resp.Errors) != 0 || !strings.Contains(string(raw), `"total":1`) || strings.Contains(string(raw), "ticket") {
		t.Errorf("searchEverything: %s %+v", raw, resp.Errors)
	}

	resp = f.query(t, allRead, `{ reviewInbox(status: "open") { kind escalated candidates { name } } }`)
	if raw, _ = json.Marshal(resp.Data); len(resp.Errors) != 0 || strings.Count(string(raw), `"name"`) != 3 || f.cis.batch != 1 || f.cis.single != 0 {
		t.Errorf("reviewInbox: %s %+v (%d single, %d batch)", raw, resp.Errors, f.cis.single, f.cis.batch)
	}

	resp = f.query(t, allRead, `{ jobs(status: "queued") { kind status progress createdAt } }`)
	if raw, _ = json.Marshal(resp.Data); len(resp.Errors) != 0 || !strings.Contains(string(raw), `"kind":"ci.bulk"`) || !strings.Contains(string(raw), `"status":"queued"`) {
		t.Errorf("jobs: %s %+v", raw, resp.Errors)
	}
}

// TestNewQueriesCheckTheirPermission: each query needs its read permission;
// topology the topology entitlement.
func TestNewQueriesCheckTheirPermission(t *testing.T) {
	f := newBFFFixture(t)
	for name, q := range map[string]string{
		"topology":         `{ topology(rootCiId: "` + f.hub + `") { nodes { id } } }`,
		"rackLayout":       `{ rackLayout(rackId: "` + f.rack + `") { freeFront } }`,
		"searchEverything": `{ searchEverything(q: "hub") { total } }`,
		"reviewInbox":      `{ reviewInbox { kind } }`,
		"jobs":             `{ jobs { kind } }`,
	} {
		if resp := f.query(t, []identity.Permission{identity.PermCIRead}, q); resp.code() != codeForbidden {
			t.Errorf("%s without its permission: %+v", name, resp.Errors)
		}
	}
	f.h.WithEntitlements(entitled{FeatureCMDBCore: true})
	if resp := f.query(t, allRead, `{ topology(rootCiId: "`+f.hub+`") { nodes { id } } }`); resp.code() != codeForbidden {
		t.Errorf("topology without the entitlement: %+v", resp.Errors)
	}
}
