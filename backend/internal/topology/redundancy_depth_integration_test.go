package topology_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// redundantSeed is the seed of F-160 in client 1 of organization A: server
// powered_by pdu-a and pdu-b, vm hosted_on server, pdu-a powered_by ups.
type redundantSeed struct {
	f                           *scopetest.Fixture
	mux                         *chi.Mux
	ups, pduA, pduB, server, vm string
	names                       map[string]string
}

func seedRedundant(t *testing.T) *redundantSeed {
	t.Helper()
	f := scopetest.Seed(t, "b8")
	s := &redundantSeed{f: f, names: map[string]string{}}
	for name, id := range map[string]*string{"ups": &s.ups, "pdu-a": &s.pduA, "pdu-b": &s.pduB, "server": &s.server, "vm": &s.vm} {
		*id = f.CI(t, f.OrgA, f.Client1, "redundant-"+name)
		s.names[*id] = name
	}
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES
		($1, $2, $3, 'powered_by'), ($1, $2, $4, 'powered_by'), ($1, $5, $2, 'hosted_on'), ($1, $3, $6, 'powered_by')`,
		f.OrgA, s.server, s.pduA, s.pduB, s.vm, s.ups); err != nil {
		t.Fatal(err)
	}
	s.mux = chi.NewRouter()
	topology.NewHandler(ci.NewPGRepository(f.App), relationship.NewPGRepository(f.App)).
		WithRelationshipTypes(relationshiptype.NewPGRepository(f.App)).RegisterRoutes(s.mux)
	return s
}

// get answers path for a principal of organization A: org-wide without
// clientID, else scoped to that client.
func (s *redundantSeed) get(clientID, path string) (int, []byte) {
	ctx := s.f.OrgCtx(s.f.OrgA)
	if clientID != "" {
		ctx = s.f.ClientCtx(clientID)
	}
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: s.f.OrgA, UserID: s.f.User})
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(ctx))
	return w.Code, w.Body.Bytes()
}

// sorted returns the names of nodes, sorted.
func (s *redundantSeed) sorted(nodes []topology.Node) []string {
	out := make([]string, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, s.names[n.ID])
	}
	slices.Sort(out)
	return out
}

// TestRedundancyAcrossDepthLimitAgainstPostgres is the retest of F-160
// (IMP-04, IMP-05, WP-513) against PostgreSQL: the walk of depth 1 from
// pdu-a (or of depth 2 from the ups) does not keep the edge server
// powered_by pdu-b; the analysis reads the redundant edges of the
// dependents it reached beyond the depth (EdgesOf), so pdu-b keeps the
// server, and with it the vm, up on /impact/{id}, /impact?ci_ids= and the
// topology path. Both supplies failing take the server down at depth 1 and
// the vm as well at depth 2 (the vm is two hops from the supplies).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRedundancyAcrossDepthLimitAgainstPostgres(t *testing.T) {
	s := seedRedundant(t)
	impacted := func(path string) []string {
		t.Helper()
		code, body := s.get("", path)
		var a struct {
			Impacted []topology.ImpactNode `json:"impacted"`
		}
		if code != http.StatusOK || json.Unmarshal(body, &a) != nil {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		nodes := make([]topology.Node, 0, len(a.Impacted))
		for i := range a.Impacted {
			nodes = append(nodes, a.Impacted[i].Node)
		}
		return s.sorted(nodes)
	}
	for _, c := range []struct {
		path string
		want []string
	}{
		{"/api/v1/impact/" + s.pduA + "?depth=1", []string{}},
		{"/api/v1/impact?ci_ids=" + s.pduA + "&depth=1", []string{}},
		{"/api/v1/topology/cis/" + s.pduA + "/impact?depth=1", []string{}},
		{"/api/v1/impact/" + s.ups + "?depth=2", []string{"pdu-a"}},
		{"/api/v1/impact/" + s.pduA + "?depth=20", []string{}},
		{"/api/v1/impact?ci_ids=" + s.pduA + "," + s.pduB + "&depth=1", []string{"server"}},
		{"/api/v1/impact?ci_ids=" + s.pduA + "," + s.pduB + "&depth=2", []string{"server", "vm"}},
	} {
		if got := impacted(c.path); !slices.Equal(got, c.want) {
			t.Errorf("%s: impacted %v, want %v", c.path, got, c.want)
		}
	}
}

// TestBlastRadiusAgainstPostgres covers IMP-04 for GET
// /cis/{id}/blast-radius (WP-513) against PostgreSQL on the seed of F-160:
// the failure of pdu-a leaves the server protected by pdu-b and nothing
// affected; the failure of the ups makes pdu-a, its only supply, a single
// point of failure while the server stays up; the failure of the server
// takes the vm behind it down (hosted_on has no redundancy). A client-2
// principal does not find a CI of client 1 (404), a client-1 principal
// gets the same answer as an org-wide one (200). A supply beyond the 20
// hops of the blast radius keeps its dependent up as well.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestBlastRadiusAgainstPostgres(t *testing.T) {
	s := seedRedundant(t)
	blast := func(clientID, id string) topology.BlastRadius {
		t.Helper()
		code, body := s.get(clientID, "/api/v1/cis/"+id+"/blast-radius")
		var b topology.BlastRadius
		if code != http.StatusOK || json.Unmarshal(body, &b) != nil {
			t.Fatalf("blast radius of %s: %d %s", s.names[id], code, body)
		}
		return b
	}
	for _, c := range []struct {
		failed                    string
		affected, spof, protected []string
	}{
		{s.pduA, []string{}, []string{}, []string{"server"}},
		{s.ups, []string{"pdu-a"}, []string{"pdu-a"}, []string{}},
		{s.server, []string{"vm"}, []string{"vm"}, []string{}},
	} {
		for _, clientID := range []string{"", s.f.Client1} {
			b := blast(clientID, c.failed)
			affected := make([]topology.Node, 0, len(b.AffectedCIs))
			for i := range b.AffectedCIs {
				affected = append(affected, b.AffectedCIs[i].Node)
			}
			if got := s.sorted(affected); !slices.Equal(got, c.affected) || b.AffectedCount != len(c.affected) || b.HeuristicCount != 0 {
				t.Errorf("%s (client %q): affected %v (%d), want %v", s.names[c.failed], clientID, got, b.AffectedCount, c.affected)
			}
			if got := s.sorted(b.SinglePointsOfFailure); !slices.Equal(got, c.spof) {
				t.Errorf("%s (client %q): single points of failure %v, want %v", s.names[c.failed], clientID, got, c.spof)
			}
			if got := s.sorted(b.ProtectedByRedundancy); !slices.Equal(got, c.protected) {
				t.Errorf("%s (client %q): protected %v, want %v", s.names[c.failed], clientID, got, c.protected)
			}
		}
	}
	for _, id := range []string{s.pduA, s.server} {
		if code, body := s.get(s.f.Client2, "/api/v1/cis/"+id+"/blast-radius"); code != http.StatusNotFound {
			t.Errorf("client 2 on %s of client 1: %d %s, want 404", s.names[id], code, body)
		}
	}

	// The blast radius walks 20 hops: a chain of 19 CIs hosted on the
	// root, the dependent at hop 20 powered by the last of them and by a
	// supply at hop 21. The supply beyond the walk keeps it up (F-160).
	f, bg := s.f, context.Background()
	root := f.CI(t, f.OrgA, f.Client1, "chain-root")
	chain := []string{root}
	for i := 1; i <= 19; i++ {
		chain = append(chain, f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("chain-%02d", i)))
	}
	dependent := f.CI(t, f.OrgA, f.Client1, "chain-dependent")
	supply := f.CI(t, f.OrgA, f.Client1, "chain-supply")
	insert := func(source, target, relType string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type)
			VALUES ($1, $2, $3, $4)`, f.OrgA, source, target, relType); err != nil {
			t.Fatal(err)
		}
	}
	for i := 1; i < len(chain); i++ {
		insert(chain[i], chain[i-1], "hosted_on")
	}
	insert(dependent, chain[len(chain)-1], "powered_by")
	insert(dependent, supply, "powered_by")
	b := blast(f.Client1, root)
	for i := range b.AffectedCIs {
		if id := b.AffectedCIs[i].ID; id == dependent || id == supply {
			t.Errorf("blast radius of the chain lists %s, kept up by the supply at hop 21", b.AffectedCIs[i].Name)
		}
	}
	if b.AffectedCount != 19 {
		t.Errorf("blast radius of the chain: %d affected, want the 19 CIs of the chain", b.AffectedCount)
	}
}
