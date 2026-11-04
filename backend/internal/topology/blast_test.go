package topology

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// countingTypes counts the reads of the CI types of an analysis.
type countingTypes struct {
	*citype.MemoryRepository
	lists int
}

func (c *countingTypes) List(ctx context.Context, orgID string, filter citype.FilterParams, page api.PaginationParams) ([]citype.Type, int, error) {
	c.lists++
	if !filter.IncludeInactive || page.Limit != maxCITypes {
		return nil, 0, nil
	}
	return c.MemoryRepository.List(ctx, orgID, filter, page)
}

// serviceChainFixture is the chain of IMP-08: business_service depends_on
// application, application uses database, database runs_on vm, vm
// hosted_on hypervisor, hypervisor uplink_to switch, switch powered_by
// pdu-a and pdu-b.
type serviceChainFixture struct {
	t     *testing.T
	cis   *ci.MemoryRepository
	rels  *relationship.MemoryRepository
	types *countingTypes
	ids   map[string]string
}

func newServiceChain(t *testing.T) *serviceChainFixture {
	t.Helper()
	f := &serviceChainFixture{t: t, cis: ci.NewMemoryRepository(), types: &countingTypes{MemoryRepository: citype.NewMemoryRepository()},
		ids: map[string]string{}}
	f.rels = relationship.NewMemoryRepository().WithNodeVisibility(ci.VisibleIDs(f.cis))
	for _, key := range []string{"business_service", "application", "database", "vm", "hypervisor", "switch", "pdu"} {
		f.types.SeedType(&citype.Type{ID: "type-" + key, Key: key, Name: key, IsSystem: true, IsActive: true})
	}
	f.add("business_service", "type-business_service")
	f.add("application", "type-application")
	f.add("database", "type-database")
	f.add("vm", "type-vm")
	f.add("hypervisor", "type-hypervisor")
	f.add("switch", "type-switch")
	f.add("pdu-a", "type-pdu")
	f.add("pdu-b", "type-pdu")
	f.link("business_service", "depends_on", "application")
	f.link("application", "uses", "database")
	f.link("database", "runs_on", "vm")
	f.link("vm", "hosted_on", "hypervisor")
	f.link("switch", "powered_by", "pdu-a")
	f.link("switch", "powered_by", "pdu-b")
	return f
}

func (f *serviceChainFixture) add(name, typeID string) {
	f.t.Helper()
	item := &ci.Item{OrganizationID: "org-1", Name: name, CITypeID: typeID, Status: "active",
		ClientID: "client-" + name, SiteID: "site-" + name}
	if err := f.cis.Create(context.Background(), item); err != nil {
		f.t.Fatal(err)
	}
	f.ids[name] = item.ID
}

func (f *serviceChainFixture) link(source, relType, target string) {
	f.t.Helper()
	if err := f.rels.Create(context.Background(), &relationship.Relationship{OrganizationID: "org-1",
		SourceCIID: f.ids[source], TargetCIID: f.ids[target], RelType: relType}); err != nil {
		f.t.Fatal(err)
	}
}

// serve builds the handler, with the CI types unless withoutTypes.
func (f *serviceChainFixture) serve(withoutTypes bool) chi.Router {
	h := NewHandler(f.cis, f.rels)
	if !withoutTypes {
		h.WithCITypes(f.types)
	}
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
}

func (f *serviceChainFixture) get(mux chi.Router, path string, want int) []byte {
	f.t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantReq("GET", path))
	if w.Code != want {
		f.t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

// chainAnswer is the service chain of an answer of /impact or blast-radius.
type chainAnswer struct {
	AffectedServices []struct {
		ID        string `json:"id"`
		Heuristic *bool  `json:"heuristic"`
	} `json:"affected_services"`
	ClientIDs     []string      `json:"client_ids"`
	SiteIDs       []string      `json:"site_ids"`
	ServiceSPOFs  []ServiceSPOF `json:"service_spofs"`
	SPOFTruncated *bool         `json:"spof_truncated"`
	BlastRadius   *BlastCounts  `json:"blast_radius"`
	Truncated     *bool         `json:"truncated"`
}

func (f *serviceChainFixture) chain(mux chi.Router, path string) chainAnswer {
	f.t.Helper()
	var a chainAnswer
	if err := json.Unmarshal(f.get(mux, path, http.StatusOK), &a); err != nil {
		f.t.Fatal(err)
	}
	return a
}

// names maps ids of the fixture back to their names.
func (f *serviceChainFixture) names(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		for name, known := range f.ids {
			if known == id {
				out = append(out, name)
			}
		}
	}
	return out
}

func (f *serviceChainFixture) spofNames(a *chainAnswer) []string {
	out := make([]string, 0, len(a.ServiceSPOFs))
	for i := range a.ServiceSPOFs {
		out = append(out, a.ServiceSPOFs[i].Name)
	}
	return out
}

func (f *serviceChainFixture) serviceNames(a *chainAnswer) (certain, heuristic []string) {
	for _, s := range a.AffectedServices {
		if s.Heuristic == nil {
			f.t.Fatalf("affected service %s without heuristic", s.ID)
		}
		if *s.Heuristic {
			heuristic = append(heuristic, f.names([]string{s.ID})...)
		} else {
			certain = append(certain, f.names([]string{s.ID})...)
		}
	}
	return certain, heuristic
}

func sortedNames(names []string) []string { return slices.Sorted(slices.Values(names)) }

// TestServiceChainSPOF covers IMP-08 (WP-297) on the chain of the plan: the
// failure of pdu-a takes no service down, pdu-a is no SPOF; pdu-a and pdu-b
// failing together take the services down, and then the switch is a SPOF
// but neither PDU; the failure of the switch makes switch, hypervisor, vm,
// database and application SPOF, business_service not.
func TestServiceChainSPOF(t *testing.T) {
	f := newServiceChain(t)
	f.link("hypervisor", "uplink_to", "switch")
	mux := f.serve(false)
	const include = "include=services,clients,sites,spof"

	for _, path := range []string{"/api/v1/cis/" + f.ids["pdu-a"] + "/blast-radius",
		"/api/v1/impact/" + f.ids["pdu-a"] + "?" + include, "/api/v1/impact?ci_ids=" + f.ids["pdu-a"] + "&" + include} {
		a := f.chain(mux, path)
		if len(a.AffectedServices) != 0 || len(a.ServiceSPOFs) != 0 || a.BlastRadius == nil || a.BlastRadius.CIs != 0 ||
			*a.BlastRadius.Services != 0 || *a.BlastRadius.SPOF != 0 {
			t.Errorf("%s: pdu-a alone takes nothing down: %+v", path, a)
		}
	}

	both := f.chain(mux, "/api/v1/impact?ci_ids="+f.ids["pdu-a"]+","+f.ids["pdu-b"]+"&"+include)
	certain, heuristic := f.serviceNames(&both)
	if !slices.Equal(certain, []string{"application", "business_service"}) || len(heuristic) != 0 {
		t.Errorf("services of both PDUs: certain %v, heuristic %v", certain, heuristic)
	}
	if got := f.spofNames(&both); !slices.Equal(got, []string{"switch", "hypervisor", "vm", "database", "application"}) {
		t.Errorf("SPOF of both PDUs: %v", got)
	}
	if both.SPOFTruncated == nil || *both.SPOFTruncated {
		t.Errorf("spof_truncated: %v", both.SPOFTruncated)
	}
	// Clients and sites of the certainly affected CIs, the failed ones not.
	wantClients := []string{"client-application", "client-business_service", "client-database", "client-hypervisor",
		"client-switch", "client-vm"}
	if !slices.Equal(both.ClientIDs, wantClients) || len(both.SiteIDs) != len(wantClients) {
		t.Errorf("clients %v, sites %v", both.ClientIDs, both.SiteIDs)
	}
	if b := both.BlastRadius; b == nil || b.CIs != 6 || *b.Services != 2 || *b.ServicesHeuristic != 0 || *b.Clients != 6 ||
		*b.Sites != 6 || *b.SPOF != 5 {
		t.Errorf("blast radius of both PDUs: %+v", both.BlastRadius)
	}

	for _, path := range []string{"/api/v1/cis/" + f.ids["switch"] + "/blast-radius",
		"/api/v1/impact/" + f.ids["switch"] + "?" + include} {
		a := f.chain(mux, path)
		if got := f.spofNames(&a); !slices.Equal(got, []string{"switch", "hypervisor", "vm", "database", "application"}) {
			t.Errorf("%s: SPOF %v", path, got)
		}
		for _, s := range a.ServiceSPOFs {
			want := []string{"application", "business_service"}
			if s.Name == "application" {
				want = []string{"business_service"}
			}
			if got := f.names(s.ServiceIDs); !slices.Equal(got, want) || s.CIType != "type-"+map[string]string{
				"switch": "switch", "hypervisor": "hypervisor", "vm": "vm", "database": "database", "application": "application"}[s.Name] {
				t.Errorf("%s: SPOF %s (%s) takes down %v, want %v", path, s.Name, s.CIType, got, want)
			}
		}
	}
	if f.types.lists == 0 {
		t.Error("the CI types were not read")
	}
}

// TestServiceChainVariants covers the variants of IMP-08: a second
// application on the database is a SPOF off the via chain; over connected_to
// the services are heuristic and have no SPOF; a subtype of application and
// a deactivated service type count as services; a lowered candidate limit
// sets spof_truncated; without CI types there are no services.
func TestServiceChainVariants(t *testing.T) {
	t.Run("second application", func(t *testing.T) {
		f := newServiceChain(t)
		f.link("hypervisor", "uplink_to", "switch")
		f.add("application-2", "type-application")
		f.link("business_service", "depends_on", "application-2")
		f.link("application-2", "uses", "database")
		a := f.chain(f.serve(false), "/api/v1/cis/"+f.ids["switch"]+"/blast-radius")
		got := f.spofNames(&a)
		if !slices.Contains(got, "application-2") || !slices.Contains(got, "application") || slices.Contains(got, "business_service") {
			t.Errorf("SPOF %v", got)
		}
	})

	t.Run("heuristic services", func(t *testing.T) {
		f := newServiceChain(t)
		f.link("hypervisor", "connected_to", "switch")
		for _, path := range []string{"/api/v1/cis/" + f.ids["switch"] + "/blast-radius",
			"/api/v1/impact/" + f.ids["switch"] + "?include=services,spof"} {
			a := f.chain(f.serve(false), path)
			certain, heuristic := f.serviceNames(&a)
			if len(certain) != 0 || !slices.Equal(sortedNames(heuristic), []string{"application", "business_service"}) {
				t.Errorf("%s: services certain %v, heuristic %v", path, certain, heuristic)
			}
			if len(a.ServiceSPOFs) != 0 || a.ServiceSPOFs == nil {
				t.Errorf("%s: SPOF of heuristic services %v", path, a.ServiceSPOFs)
			}
			if b := a.BlastRadius; *b.Services != 0 || *b.ServicesHeuristic != 2 || *b.SPOF != 0 {
				t.Errorf("%s: blast radius %+v", path, b)
			}
		}
	})

	t.Run("subtype and deactivated type", func(t *testing.T) {
		f := newServiceChain(t)
		f.link("hypervisor", "uplink_to", "switch")
		f.types.SeedType(&citype.Type{ID: "type-webapp", Key: "webapp", Name: "webapp", OrganizationID: "org-1",
			ParentTypeID: "type-application", IsActive: true})
		f.types.SeedType(&citype.Type{ID: "type-legacy", Key: "legacy_service", Name: "legacy_service", OrganizationID: "org-1",
			ParentTypeID: "type-business_service", IsActive: false})
		// A type of another organization that would be a service stays
		// out: its CIs are no service of org-1.
		f.types.SeedType(&citype.Type{ID: "type-foreign", Key: "business_service", Name: "foreign", OrganizationID: "org-2", IsActive: true})
		f.add("webapp", "type-webapp")
		f.add("legacy", "type-legacy")
		f.add("foreign-typed", "type-foreign")
		f.link("webapp", "runs_on", "vm")
		f.link("legacy", "depends_on", "webapp")
		f.link("foreign-typed", "runs_on", "vm")
		a := f.chain(f.serve(false), "/api/v1/cis/"+f.ids["switch"]+"/blast-radius")
		certain, _ := f.serviceNames(&a)
		if !slices.Equal(sortedNames(certain), []string{"application", "business_service", "legacy", "webapp"}) {
			t.Errorf("services %v", certain)
		}
		if got := f.spofNames(&a); !slices.Contains(got, "webapp") || slices.Contains(got, "legacy") {
			t.Errorf("SPOF %v", got)
		}
	})

	t.Run("candidate limit", func(t *testing.T) {
		f := newServiceChain(t)
		f.link("hypervisor", "uplink_to", "switch")
		defer func(limit int) { maxSPOFCandidates = limit }(maxSPOFCandidates)
		maxSPOFCandidates = 2
		a := f.chain(f.serve(false), "/api/v1/cis/"+f.ids["switch"]+"/blast-radius")
		if a.SPOFTruncated == nil || !*a.SPOFTruncated || !slices.Equal(f.spofNames(&a), []string{"switch", "hypervisor"}) {
			t.Errorf("SPOF %v, truncated %v", f.spofNames(&a), a.SPOFTruncated)
		}
	})

	t.Run("without CI types", func(t *testing.T) {
		f := newServiceChain(t)
		f.link("hypervisor", "uplink_to", "switch")
		a := f.chain(f.serve(true), "/api/v1/impact/"+f.ids["switch"]+"?include=services,spof")
		if a.AffectedServices == nil || len(a.AffectedServices) != 0 || a.ServiceSPOFs == nil || len(a.ServiceSPOFs) != 0 {
			t.Errorf("without CI types: %+v", a)
		}
		b := f.chain(f.serve(true), "/api/v1/cis/"+f.ids["switch"]+"/blast-radius")
		if len(b.AffectedServices) != 0 || len(b.ServiceSPOFs) != 0 || b.BlastRadius.CIs != 5 {
			t.Errorf("blast radius without CI types: %+v", b)
		}
	})
}

// TestServiceChainContract covers the contract of IMP-08: include=foo is
// 422; without include /impact and /impact/{ci_id} answer as before and
// read no CI type; the deprecated path ignores include; include combines
// with fields=; only services and spof read the CI types, once; the fields
// of blast-radius keep their content (WP-179, WP-490).
func TestServiceChainContract(t *testing.T) {
	f := newServiceChain(t)
	f.link("hypervisor", "uplink_to", "switch")
	mux := f.serve(false)
	plain := f.serve(true)
	sw := f.ids["switch"]

	for _, path := range []string{"/api/v1/impact/" + sw + "?include=foo", "/api/v1/impact?ci_ids=" + sw + "&include=services,foo"} {
		if body := string(f.get(mux, path, http.StatusUnprocessableEntity)); !strings.Contains(body, `"field":"include"`) {
			t.Errorf("%s: %s", path, body)
		}
	}

	f.types.lists = 0
	for _, path := range []string{"/api/v1/impact/" + sw, "/api/v1/impact?ci_ids=" + sw, "/api/v1/impact/" + sw + "?fields=hops",
		"/api/v1/impact/" + sw + "?include="} {
		if with, without := string(f.get(mux, path, http.StatusOK)), string(f.get(plain, path, http.StatusOK)); with != without {
			t.Errorf("%s without include differs:\n%s\n%s", path, with, without)
		}
	}
	legacy := "/api/v1/topology/cis/" + sw + "/impact"
	if with, without := string(f.get(mux, legacy+"?include=foo", http.StatusOK)), string(f.get(plain, legacy, http.StatusOK)); with != without {
		t.Errorf("the deprecated path does not ignore include:\n%s\n%s", with, without)
	}
	if f.types.lists != 0 {
		t.Errorf("%d reads of the CI types without include", f.types.lists)
	}

	var compact struct {
		Impacted         []map[string]json.RawMessage `json:"impacted"`
		AffectedServices []AffectedService            `json:"affected_services"`
		ClientIDs        []string                     `json:"client_ids"`
		ServiceSPOFs     []ServiceSPOF                `json:"service_spofs"`
		BlastRadius      map[string]int               `json:"blast_radius"`
	}
	if err := json.Unmarshal(f.get(mux, "/api/v1/impact/"+sw+"?fields=hops&include=%20clients,,services", http.StatusOK), &compact); err != nil {
		t.Fatal(err)
	}
	for _, entry := range compact.Impacted {
		if len(entry) != 2 {
			t.Errorf("fields=hops with include: entry %v", entry)
		}
	}
	if len(compact.AffectedServices) != 2 || len(compact.ClientIDs) != 5 || compact.ServiceSPOFs != nil ||
		len(compact.BlastRadius) != 4 || compact.BlastRadius["services"] != 2 || compact.BlastRadius["clients"] != 5 {
		t.Errorf("include=clients,services: %+v", compact)
	}
	if f.types.lists != 1 {
		t.Errorf("%d reads of the CI types for one analysis, want 1", f.types.lists)
	}
	f.get(mux, "/api/v1/impact/"+sw+"?include=clients,sites", http.StatusOK)
	if f.types.lists != 1 {
		t.Errorf("include=clients,sites read the CI types")
	}

	// blast-radius keeps its fields: single_points_of_failure are the
	// direct dependents (WP-179), protected_by_redundancy those with a
	// working provider, heuristic_count and the mark of WP-490.
	var blast BlastRadius
	if err := json.Unmarshal(f.get(mux, "/api/v1/cis/"+sw+"/blast-radius", http.StatusOK), &blast); err != nil {
		t.Fatal(err)
	}
	if blast.AffectedCount != 5 || blast.HeuristicCount != 0 || len(blast.SinglePointsOfFailure) != 1 ||
		blast.SinglePointsOfFailure[0].Name != "hypervisor" || len(blast.AffectedServices) != 2 || blast.Truncated {
		t.Errorf("blast radius of the switch: %+v", blast)
	}
	if err := json.Unmarshal(f.get(mux, "/api/v1/cis/"+f.ids["pdu-a"]+"/blast-radius", http.StatusOK), &blast); err != nil {
		t.Fatal(err)
	}
	if blast.AffectedCount != 0 || len(blast.ProtectedByRedundancy) != 1 || blast.ProtectedByRedundancy[0].Name != "switch" {
		t.Errorf("blast radius of pdu-a: %+v", blast)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(f.get(mux, "/api/v1/cis/"+sw+"/blast-radius", http.StatusOK), &raw); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"service_spofs", "spof_truncated", "blast_radius", "affected_services"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("blast radius without %s", key)
		}
	}
	if _, ok := raw["truncated"]; ok {
		t.Error("truncated on a walk that was not cut")
	}
}

// TestServiceTypeIDs: the service types by key along the type chain; a
// cycle in the chain ends.
func TestServiceTypeIDs(t *testing.T) {
	got := serviceTypeIDs([]citype.Type{
		{ID: "a", Key: "application"}, {ID: "b", Key: "business_service"}, {ID: "c", Key: "custom", ParentTypeID: "a"},
		{ID: "d", Key: "deep", ParentTypeID: "c"}, {ID: "x", Key: "x", ParentTypeID: "y"}, {ID: "y", Key: "y", ParentTypeID: "x"},
		{ID: "s", Key: "server"},
	})
	if want := map[string]bool{"a": true, "b": true, "c": true, "d": true}; len(got) != len(want) || !got["a"] || !got["b"] || !got["c"] || !got["d"] {
		t.Errorf("service types %v, want %v", got, want)
	}
}
