package search_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// hitsSearch runs GET /api/v1/search?q= org-wide as a principal with the
// policy, with or without ipam:read, and returns the hits by entity id and
// the raw hit objects of the answer.
func hitsSearch(t *testing.T, w *sfWorld, policy *permission.FieldPolicy, ipamRead bool, text string) (map[string]search.Hit, []map[string]any) {
	t.Helper()
	granted := map[string]bool{"ci:read": true, "asset:read": true, "location:read": true, "reservation:read": true, "ipam:read": ipamRead}
	mux := chi.NewRouter()
	search.NewHandler(w.repo, permissionStub{granted: granted}).RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/search?"+url.Values{"q": {text}, "limit": {"100"}}.Encode(), nil)
	scope := database.OrgWideScope(w.f.OrgA, w.f.User)
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: w.f.OrgA, UserID: w.f.User})
	ctx = permission.WithPolicy(database.ContextWithTenantScope(ctx, &scope), policy)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req.WithContext(ctx))
	var body struct {
		Data []search.Hit `json:"data"`
	}
	var raw struct {
		Data []map[string]any `json:"data"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || json.Unmarshal(rec.Body.Bytes(), &raw) != nil {
		t.Fatalf("GET /api/v1/search?q=%s: %d %s", text, rec.Code, rec.Body.String())
	}
	out := map[string]search.Hit{}
	for i := range body.Data {
		out[body.Data[i].EntityID] = body.Data[i]
	}
	return out, raw.Data
}

// matched renders the matched fields of a hit as field=highlight, in order.
func matched(h *search.Hit) []string {
	out := []string{}
	for _, m := range h.MatchedFields {
		out = append(out, m.Field+"="+m.Highlight)
	}
	return out
}

// searchHitSchema reads the required properties, the properties and the
// enum entity_type of SearchHit and the required properties of
// SearchMatchedField from api/openapi.yaml.
func searchHitSchema(t *testing.T) (required, properties, entityTypes, matchedRequired []string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Components struct {
			Schemas map[string]struct {
				Required   []string `yaml:"required"`
				Properties map[string]struct {
					Enum []string `yaml:"enum"`
				} `yaml:"properties"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err = yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	hit := spec.Components.Schemas["SearchHit"]
	for key := range hit.Properties {
		properties = append(properties, key)
	}
	return hit.Required, properties, hit.Properties["entity_type"].Enum, spec.Components.Schemas["SearchMatchedField"].Required
}

// TestSearchMatchedFields covers WP-786 (SRC-02): a hit of GET
// /api/v1/search names the fields that matched, each with its highlight:
// title, a field row (core.<column>) and a custom field (attributes.<key>)
// with their ts_headline fragment, the management IP, an interface address
// (network.ip), the primary MAC and an interface MAC (network.mac) and the
// inventory number (at the asset and as asset.inventory_no at its CIs) in
// canonical form. With a restricted field policy the list names no field
// the principal cannot read (WP-266); without ipam:read no network.*.
// highlights stays byte for byte the fragment over title and summary.
// Hits of a location and a reservation are valid SearchHit objects of the
// specification.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSearchMatchedFields(t *testing.T) {
	w := newSFWorld(t)
	f := w.f
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	w.exec(t, `UPDATE ci SET hostname = 'alpha', manufacturer = 'alpha systems', model = 'm786' WHERE id = $1`, w.alpha)
	var ifc string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM network_interface WHERE ci_id = $1`, w.alpha).Scan(&ifc); err != nil {
		t.Fatal(err)
	}
	w.exec(t, `INSERT INTO ip_address (organization_id, interface_id, address) VALUES ($1, $2, '10.38.0.77')`, f.OrgA, ifc)
	site, err := locations.NewPGRepository(f.App).Create(orgCtx, f.OrgA,
		locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp786lager Nord"})
	if err != nil {
		t.Fatal(err)
	}
	var reservation string
	if err = f.Admin.QueryRow(bg, `INSERT INTO reservation (organization_id, item_kind, asset_id, quantity, state, reason)
		VALUES ($1, 'asset', $2, 1, 'active', 'wp786reserve') RETURNING id::text`, f.OrgA, w.asset).Scan(&reservation); err != nil {
		t.Fatal(err)
	}
	if _, err = w.repo.ReindexTenant(orgCtx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	open := permission.UnrestrictedPolicy(frPerms)

	for _, c := range []struct {
		label, q string
		id       string
		want     []string
	}{
		{"title and field rows", "alpha", w.alpha, []string{"title=wp308 <mark>alpha</mark>", "core.hostname=<mark>alpha</mark>",
			"core.manufacturer=<mark>alpha</mark> systems"}},
		{"field row", "m786", w.alpha, []string{"core.model=<mark>m786</mark>"}},
		{"attribute", "typeval38", w.alpha, []string{"attributes.wp308_type=<mark>typeval38</mark>"}},
		{"management IP", "10.38.0.5", w.alpha, []string{"core.management_ip=10.38.0.5"}},
		{"CIDR", "10.38.0.0/24", w.alpha, []string{"core.management_ip=10.38.0.5", "network.ip=10.38.0.77"}},
		{"interface IP", "192.168.38.10", w.alpha, []string{"network.ip=192.168.38.10"}},
		{"primary MAC", "0800.2b38.0005", w.alpha, []string{"core.primary_mac=08:00:2b:38:00:05"}},
		{"interface MAC", "08-00-2B-38-AA-01", w.alpha, []string{"network.mac=08:00:2b:38:aa:01"}},
		{"inventory number at the CI", "INV-38-A", w.beta, []string{"asset.inventory_no=inv-38-a"}},
		{"inventory number at the asset", "INV-38-A", w.asset, []string{"core.asset_tag=<mark>INV</mark><mark>-38</mark>-<mark>A</mark>", "core.inventory_no=inv-38-a"}},
		{"asset custom field", "customval38", w.asset, []string{"attributes.cf38=<mark>customval38</mark>"}},
		{"location", "wp786lager", site.ID, []string{"title=<mark>wp786lager</mark> Nord"}},
		{"reservation", "wp786reserve", reservation, []string{"title=<mark>wp786reserve</mark>",
			"summary=active <mark>wp786reserve</mark>"}},
	} {
		hits, _ := hitsSearch(t, w, open, true, c.q)
		h, ok := hits[c.id]
		if !ok {
			t.Errorf("%s: %s is no hit", c.label, c.id)
			continue
		}
		if got := matched(&h); !slices.Equal(got, c.want) {
			t.Errorf("%s: matched_fields = %q, want %q", c.label, got, c.want)
		}
	}

	// highlights is the fragment over title and summary, as before.
	hits, _ := hitsSearch(t, w, open, true, "alpha")
	h := hits[w.alpha]
	var want string
	if err = f.Admin.QueryRow(bg, `SELECT ts_headline('simple', title || ' ' || summary, websearch_to_tsquery('simple', 'alpha'),
		'StartSel=<mark>, StopSel=</mark>, MaxFragments=2') FROM search_document WHERE entity_id = $1`, w.alpha).Scan(&want); err != nil {
		t.Fatal(err)
	}
	if len(h.Highlights) != 1 || h.Highlights[0] != want {
		t.Errorf("highlights = %q, want [%q]", h.Highlights, want)
	}

	// Field permissions: only readable fields.
	fr := &frWorld{f: f, repo: w.repo}
	_, noHost := fr.role(t, "wp786-nohost", [4]string{"ci", "", "core.hostname", "none"})
	hits, _ = hitsSearch(t, w, noHost, true, "alpha")
	h = hits[w.alpha]
	if got := matched(&h); !slices.Equal(got, []string{"title=wp308 <mark>alpha</mark>", "core.manufacturer=<mark>alpha</mark> systems"}) {
		t.Errorf("core.hostname none: matched_fields = %q", got)
	}
	_, noIP := fr.role(t, "wp786-noip", [4]string{"ci", "", "core.management_ip", "none"})
	hits, _ = hitsSearch(t, w, noIP, true, "10.38.0.0/24")
	h = hits[w.alpha]
	if got := matched(&h); !slices.Equal(got, []string{"network.ip=10.38.0.77"}) {
		t.Errorf("core.management_ip none: matched_fields = %q", got)
	}
	hits, _ = hitsSearch(t, w, open, false, "10.38.0.0/24")
	h = hits[w.alpha]
	if got := matched(&h); !slices.Equal(got, []string{"core.management_ip=10.38.0.5"}) {
		t.Errorf("without ipam:read: matched_fields = %q", got)
	}
	_, noAttr := fr.role(t, "wp786-noattr", [4]string{"ci", "", "attributes.wp308_type", "none"})
	w.exec(t, `UPDATE ci SET attributes = attributes || '{"wp308_global": "alpha"}' WHERE id = $1`, w.alpha)
	w.exec(t, `UPDATE ci SET attributes = attributes || '{"wp308_type": "alpha"}' WHERE id = $1`, w.alpha)
	if _, err = w.repo.ReindexTenant(orgCtx, f.OrgA); err != nil {
		t.Fatal(err)
	}
	hits, _ = hitsSearch(t, w, noAttr, true, "alpha")
	h = hits[w.alpha]
	if got := matched(&h); slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, "attributes.wp308_type=") }) ||
		!slices.Contains(got, "attributes.wp308_global=<mark>alpha</mark>") {
		t.Errorf("attributes.wp308_type none: matched_fields = %q", got)
	}
	inventoryKey, _, _ := permission.FieldKeyOf(permission.KindAsset, "inventory_no")
	_, ciRestricted := fr.role(t, "wp786-cirestricted", [4]string{"ci", "", "core.fqdn", "none"}, [4]string{"asset", "", inventoryKey, "none"})
	hits, _ = hitsSearch(t, w, ciRestricted, true, "INV-38")
	for _, id := range []string{w.beta, w.gamma, w.asset} {
		if h, ok := hits[id]; ok {
			t.Errorf("inventory number none: %s is a hit with %q", id, matched(&h))
		}
	}

	// Schema: every hit of a location and a reservation (and the others)
	// carries the required properties, an entity type of the enum and
	// well-formed matched fields.
	required, properties, entityTypes, matchedRequired := searchHitSchema(t)
	if !slices.Contains(properties, "matched_fields") || !slices.Contains(entityTypes, "location") || !slices.Contains(entityTypes, "reservation") {
		t.Fatalf("SearchHit: properties %v, entity types %v", properties, entityTypes)
	}
	for _, q := range []string{"wp786lager", "wp786reserve", "alpha", "10.38.0.0/24"} {
		_, raw := hitsSearch(t, w, open, true, q)
		if len(raw) == 0 {
			t.Errorf("%s: no hit", q)
		}
		for _, obj := range raw {
			for _, key := range required {
				if _, ok := obj[key]; !ok {
					t.Errorf("%s: hit without %s: %v", q, key, obj)
				}
			}
			if et, _ := obj["entity_type"].(string); !slices.Contains(entityTypes, et) {
				t.Errorf("%s: entity_type %q not in %v", q, et, entityTypes)
			}
			fields, _ := obj["matched_fields"].([]any)
			for _, m := range fields {
				mf, _ := m.(map[string]any)
				for _, key := range matchedRequired {
					if s, ok := mf[key].(string); !ok || s == "" {
						t.Errorf("%s: matched field without %s: %v", q, key, mf)
					}
				}
			}
		}
	}
}
