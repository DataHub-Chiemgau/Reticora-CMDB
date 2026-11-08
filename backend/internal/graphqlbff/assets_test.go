package graphqlbff

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The tests of this file cover GQL-03 (WP-311): the asset detail aggregate
// asset(id) with its CIs (ci.asset_id), the child CIs of those CIs
// (ci.parent_ci_id with childMode, AST-05), the movements of the ledger and
// the documents; every list is read once per request (no N+1); rights,
// entitlements and module gates as in REST (GQL-04, E-03, WP-214); the
// asset fields follow the field permissions (RBA-07); the complexity of
// the default pages stays within the budget (SEC-10).

// readCounter counts the reads of a request per source.
type readCounter struct {
	mu    sync.Mutex
	reads map[string]int
}

func (c *readCounter) hit(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads[name]++
}

func (c *readCounter) get(name string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reads[name]
}

func (c *readCounter) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads = map[string]int{}
}

// listCountingCIs counts the CI lists and the single and batch reads.
type listCountingCIs struct {
	*ci.MemoryRepository
	n *readCounter
}

func (c listCountingCIs) List(ctx context.Context, orgID string, filter *ci.FilterParams, page api.PaginationParams) ([]ci.Item, int, error) {
	field := ""
	if len(filter.Filters) > 0 {
		field = filter.Filters[0].Field
	}
	c.n.hit("ci.list:" + field)
	return c.MemoryRepository.List(ctx, orgID, filter, page)
}

func (c listCountingCIs) GetByID(ctx context.Context, orgID, id string) (*ci.Item, error) {
	c.n.hit("ci.get")
	return c.MemoryRepository.GetByID(ctx, orgID, id)
}

func (c listCountingCIs) GetByIDs(ctx context.Context, orgID string, ids []string) ([]ci.Item, error) {
	c.n.hit("ci.batch")
	return c.MemoryRepository.GetByIDs(ctx, orgID, ids)
}

type countingAssets struct {
	*asset.MemoryRepository
	n *readCounter
}

func (c countingAssets) GetByID(ctx context.Context, orgID, id string) (*asset.Asset, error) {
	c.n.hit("asset")
	return c.MemoryRepository.GetByID(ctx, orgID, id)
}

type countingMovements struct {
	*movement.MemoryRepository
	n *readCounter
}

func (c countingMovements) ListMovements(ctx context.Context, orgID string, filter *movement.MovementFilter, page api.PaginationParams) ([]movement.Movement, int, error) {
	c.n.hit("movements")
	return c.MemoryRepository.ListMovements(ctx, orgID, filter, page)
}

type countingDocuments struct {
	*document.MemoryRepository
	n *readCounter
}

func (c countingDocuments) GetLinksForEntity(ctx context.Context, orgID, entityType, entityID string, page api.PaginationParams) ([]document.Document, int, error) {
	c.n.hit("documents")
	return c.MemoryRepository.GetLinksForEntity(ctx, orgID, entityType, entityID, page)
}

// modules is a module set for the tests.
type modules map[string]bool

func (m modules) Enabled(key string) bool { return m[key] }

type assetFixture struct {
	h        *Handler
	n        *readCounter
	asset    string
	other    string
	cis      []string
	children map[string]string // child CI id → its child mode
	strayKid string            // child of a CI of another asset
}

const fixtureOrg = "org-1"

// newAssetFixture builds an asset with three CIs, each with a configuration
// and a standalone child, three movements and two documents; a second
// asset with a CI and a child of its own.
func newAssetFixture(t *testing.T) *assetFixture {
	t.Helper()
	bg := context.Background()
	n := &readCounter{reads: map[string]int{}}
	cis := listCountingCIs{MemoryRepository: ci.NewMemoryRepository(), n: n}
	assets := countingAssets{MemoryRepository: asset.NewMemoryRepository(), n: n}
	moves := countingMovements{MemoryRepository: movement.NewMemoryRepository(), n: n}
	docs := countingDocuments{MemoryRepository: document.NewMemoryRepository(), n: n}
	f := &assetFixture{n: n, children: map[string]string{}}

	addAsset := func(name string) string {
		a := &asset.Asset{OrganizationID: fixtureOrg, Name: name, AssetTag: "INV-" + name, Category: "server", Status: "active",
			SerialNumber: "SN-" + name, Manufacturer: "ACME", CustomFields: map[string]any{"rack_slot": "A1", "secret": "S"},
			Tags: []string{"critical"}}
		if err := assets.Create(bg, a); err != nil {
			t.Fatal(err)
		}
		return a.ID
	}
	addCI := func(item *ci.Item) string {
		item.OrganizationID, item.CITypeID, item.Status, item.Attributes = fixtureOrg, "server", "active", map[string]any{}
		if err := cis.Create(bg, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	f.asset, f.other = addAsset("main"), addAsset("other")
	for _, name := range []string{"chassis-1", "chassis-2", "chassis-3"} {
		id := addCI(&ci.Item{Name: name, AssetID: f.asset})
		f.cis = append(f.cis, id)
		for _, mode := range []string{ci.ChildModeConfiguration, ci.ChildModeStandalone} {
			f.children[addCI(&ci.Item{Name: name + "-" + mode, ParentCIID: id, ChildMode: mode})] = mode
		}
	}
	otherCI := addCI(&ci.Item{Name: "foreign", AssetID: f.other})
	f.strayKid = addCI(&ci.Item{Name: "foreign-blade", ParentCIID: otherCI, ChildMode: ci.ChildModeStandalone})

	for _, kind := range []string{"receipt", "transfer", "transfer"} {
		if err := moves.Record(bg, &movement.Movement{OrganizationID: fixtureOrg, AssetID: f.asset, MovementType: kind}); err != nil {
			t.Fatal(err)
		}
	}
	if err := moves.Record(bg, &movement.Movement{OrganizationID: fixtureOrg, AssetID: f.other, MovementType: "receipt"}); err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"invoice", "manual"} {
		d := &document.Document{OrganizationID: fixtureOrg, Title: title, FileName: title + ".pdf", MimeType: "application/pdf",
			Category: "general", Tags: []string{}}
		if err := docs.Create(bg, d); err != nil {
			t.Fatal(err)
		}
		if err := docs.LinkDocument(bg, &document.DocumentLink{OrganizationID: fixtureOrg, DocumentID: d.ID,
			EntityType: "asset", EntityID: f.asset}); err != nil {
			t.Fatal(err)
		}
	}

	f.h = NewHandler(cis, relationship.NewMemoryRepository()).
		WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureAssetsBasic: true, entitlement.FeatureDocuments: true}).
		WithSources(&Sources{Assets: assets, Movements: moves, Documents: docs}).
		WithModules(modules{ModuleInventory: true, ModuleDocuments: true})
	return f
}

var assetRead = []identity.Permission{identity.PermAssetRead, identity.PermCIRead, identity.PermDocumentRead}

// assetResponse is a GraphQL answer with the path of each error.
type assetResponse struct {
	Data   map[string]any `json:"data"`
	Errors []struct {
		Message    string         `json:"message"`
		Path       []any          `json:"path"`
		Extensions map[string]any `json:"extensions"`
	} `json:"errors"`
}

// codeAt returns the code of the error at the field path (dotted), "" when
// none.
func (r *assetResponse) codeAt(path string) string {
	for _, e := range r.Errors {
		parts := make([]string, len(e.Path))
		for i, p := range e.Path {
			s, _ := p.(string)
			parts[i] = s
		}
		if strings.Join(parts, ".") == path {
			code, _ := e.Extensions["code"].(string)
			return code
		}
	}
	return ""
}

// runAsset sends query with perms and policy (nil: unrestricted for perms)
// through handler h and resets the read counter first.
func (f *assetFixture) run(t *testing.T, h *Handler, perms []identity.Permission, policy *permission.FieldPolicy, query string) assetResponse {
	t.Helper()
	f.n.reset()
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(gql(query)))
	req.Header.Set("Content-Type", "application/json")
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: fixtureOrg, UserID: "user-1"})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: "user-1", OrganizationID: fixtureOrg, Permissions: perms,
		Type: identity.PrincipalTypeUser})
	if policy == nil {
		policy = permission.UnrestrictedPolicy(perms)
	}
	ctx = permission.WithPolicy(ctx, policy)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(ctx))
	var out assetResponse
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body, err)
	}
	return out
}

const ciFields = "id name status ciTypeId clientId siteId locationId hostname fqdn serialNumber manufacturer model managementIp " +
	"lifecycleState health version createdAt updatedAt attributes tags assetId parentCiId childMode"

const assetFields = "id name clientId version createdAt updatedAt inventoryNo category status manufacturer model serialNumber " +
	"purchaseDate purchasePrice currency warrantyUntil costCenter supplierId contractId ownerUserId ownerTeamId invoiceNumber " +
	"rfidTag barcode locationId lifecycleState primaryCiId notes customFields tags"

const movementFields = "id itemKind movementType assetId fromLocationId toLocationId quantity actorId assigneeUserId " +
	"assigneeTeamId reason ticketId orderId documentId notes occurredAt createdAt"

const documentFields = "id title description fileName fileSize mimeType version category tags uploadedBy scanStatus createdAt updatedAt"

// detailQuery is the asset detail with every field and the given pages.
func detailQuery(id, alias string, cis, children, movements, documents int) string {
	page := func(n int) string { return "(first: " + itoa(n) + ")" }
	return alias + `asset(id: "` + id + `") { ` + assetFields +
		" cis" + page(cis) + " { " + ciFields + " }" +
		" children" + page(children) + " { " + ciFields + " }" +
		" movements" + page(movements) + " { nodes { " + movementFields + " } pageInfo { hasNextPage endCursor } totalCount }" +
		" documents" + page(documents) + " { " + documentFields + " } }"
}

func itoa(n int) string {
	raw, _ := json.Marshal(n)
	return string(raw)
}

func names(t *testing.T, raw any) []string {
	t.Helper()
	list, ok := raw.([]any)
	if !ok {
		t.Fatalf("not a list: %#v", raw)
	}
	out := make([]string, 0, len(list))
	for _, e := range list {
		obj, _ := e.(map[string]any)
		name, _ := obj["name"].(string)
		out = append(out, name)
	}
	slices.Sort(out)
	return out
}

// TestAssetAggregate: the asset with its CIs, the children of those CIs
// over parent_ci_id with their childMode (not the composition table, not
// the children of another asset's CIs), its movements and documents.
func TestAssetAggregate(t *testing.T) {
	f := newAssetFixture(t)
	resp := f.run(t, f.h, assetRead, nil, "{ "+detailQuery(f.asset, "", 50, 50, 20, 50)+" }")
	if len(resp.Errors) != 0 {
		t.Fatalf("errors: %+v", resp.Errors)
	}
	a, _ := resp.Data["asset"].(map[string]any)
	if a["id"] != f.asset || a["inventoryNo"] != "INV-main" || a["serialNumber"] != "SN-main" {
		t.Errorf("asset fields: %v", a)
	}
	if got := names(t, a["cis"]); !slices.Equal(got, []string{"chassis-1", "chassis-2", "chassis-3"}) {
		t.Errorf("cis = %v", got)
	}
	for _, raw := range a["cis"].([]any) {
		if obj := raw.(map[string]any); obj["assetId"] != f.asset {
			t.Errorf("CI %v: assetId %v", obj["name"], obj["assetId"])
		}
	}
	kids, _ := a["children"].([]any)
	if len(kids) != len(f.children) {
		t.Fatalf("children: %d, want %d: %v", len(kids), len(f.children), kids)
	}
	for _, raw := range kids {
		obj := raw.(map[string]any)
		id, _ := obj["id"].(string)
		mode, ok := f.children[id]
		if !ok || id == f.strayKid {
			t.Errorf("unexpected child %v", obj["name"])
			continue
		}
		if obj["childMode"] != mode || !slices.Contains(f.cis, obj["parentCiId"].(string)) {
			t.Errorf("child %v: childMode %v parentCiId %v", obj["name"], obj["childMode"], obj["parentCiId"])
		}
	}
	moves, _ := a["movements"].(map[string]any)
	if moves["totalCount"] != float64(3) || len(moves["nodes"].([]any)) != 3 {
		t.Errorf("movements: %v", moves)
	}
	for _, raw := range moves["nodes"].([]any) {
		if obj := raw.(map[string]any); obj["assetId"] != f.asset || obj["itemKind"] != "asset" {
			t.Errorf("movement %v", obj)
		}
	}
	if docs, _ := a["documents"].([]any); len(docs) != 2 {
		t.Errorf("documents: %v", docs)
	}
}

// TestAssetMovementsPage: movements page with the keyset cursor of the
// ledger; a cursor that is not one is bad input.
func TestAssetMovementsPage(t *testing.T) {
	f := newAssetFixture(t)
	q := func(after string) string {
		arg := ""
		if after != "" {
			arg = `, after: "` + after + `"`
		}
		return `{ asset(id: "` + f.asset + `") { movements(first: 2` + arg + `) { nodes { id } pageInfo { hasNextPage endCursor } totalCount } } }`
	}
	resp := f.run(t, f.h, assetRead, nil, q(""))
	moves := resp.Data["asset"].(map[string]any)["movements"].(map[string]any)
	info := moves["pageInfo"].(map[string]any)
	first := moves["nodes"].([]any)
	if len(first) != 2 || info["hasNextPage"] != true {
		t.Fatalf("first page: %v", moves)
	}
	cursor, _ := info["endCursor"].(string)
	resp = f.run(t, f.h, assetRead, nil, q(cursor))
	moves = resp.Data["asset"].(map[string]any)["movements"].(map[string]any)
	second := moves["nodes"].([]any)
	if len(second) != 1 || moves["pageInfo"].(map[string]any)["hasNextPage"] != false {
		t.Fatalf("second page: %v", moves)
	}
	seen := map[any]bool{}
	for _, n := range append(first, second...) {
		seen[n.(map[string]any)["id"]] = true
	}
	if len(seen) != 3 {
		t.Errorf("pages overlap: %v %v", first, second)
	}
	if resp := f.run(t, f.h, assetRead, nil, q("not-a-cursor")); resp.codeAt("asset.movements") != codeBadInput {
		t.Errorf("bad cursor: %+v", resp.Errors)
	}
}

// TestAssetDetailWithoutNPlusOne: the detail reads the asset, its CIs, the
// children, the movements and the documents once each, whatever the
// number of CIs; the CIs land in the dataloader (no single reads).
func TestAssetDetailWithoutNPlusOne(t *testing.T) {
	f := newAssetFixture(t)
	resp := f.run(t, f.h, assetRead, nil, "{ "+detailQuery(f.asset, "", 50, 50, 20, 50)+" }")
	if len(resp.Errors) != 0 {
		t.Fatalf("errors: %+v", resp.Errors)
	}
	for name, want := range map[string]int{"asset": 1, "ci.list:asset_id": 1, "ci.list:parent_ci_id": 1, "movements": 1,
		"documents": 1, "ci.get": 0, "ci.batch": 0} {
		if got := f.n.get(name); got != want {
			t.Errorf("%s: %d reads, want %d", name, got, want)
		}
	}
}

// TestAssetRights: asset:read, ci:read and document:read guard their
// fields (FORBIDDEN), as do the entitlements assets_basic and documents.
func TestAssetRights(t *testing.T) {
	f := newAssetFixture(t)
	q := `{ asset(id: "` + f.asset + `") { id cis { id } children { id } documents { id } } }`
	if resp := f.run(t, f.h, []identity.Permission{identity.PermCIRead, identity.PermDocumentRead}, nil, q); resp.codeAt("asset") != codeForbidden {
		t.Errorf("without asset:read: %+v", resp.Errors)
	}
	resp := f.run(t, f.h, []identity.Permission{identity.PermAssetRead, identity.PermDocumentRead}, nil, q)
	if resp.codeAt("asset.cis") != codeForbidden || resp.codeAt("asset.children") != codeForbidden {
		t.Errorf("without ci:read: %+v", resp.Errors)
	}
	if f.n.get("ci.list:asset_id")+f.n.get("ci.list:parent_ci_id") != 0 {
		t.Error("CIs read without ci:read")
	}
	resp = f.run(t, f.h, []identity.Permission{identity.PermAssetRead, identity.PermCIRead}, nil, q)
	if resp.codeAt("asset.documents") != codeForbidden || resp.codeAt("asset.cis") != "" {
		t.Errorf("without document:read: %+v", resp.Errors)
	}

	noAssets := newAssetFixture(t)
	noAssets.h.WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureDocuments: true})
	if denied := noAssets.run(t, noAssets.h, assetRead, nil, q); denied.codeAt("asset") != codeForbidden {
		t.Errorf("without assets_basic: %+v", denied.Errors)
	}
	noDocs := newAssetFixture(t)
	noDocs.h.WithEntitlements(entitled{FeatureCMDBCore: true, entitlement.FeatureAssetsBasic: true})
	resp = noDocs.run(t, noDocs.h, assetRead, nil, q)
	if resp.codeAt("asset.documents") != codeForbidden || resp.codeAt("asset.cis") != "" {
		t.Errorf("without documents: %+v", resp.Errors)
	}
}

// TestAssetModuleGates: with inventory off asset answers UNAVAILABLE, with
// documents off only documents; without a module checker every
// module-bound field is refused (fail-closed); a missing source is
// UNAVAILABLE.
func TestAssetModuleGates(t *testing.T) {
	q := func(f *assetFixture) string {
		return `{ asset(id: "` + f.asset + `") { id cis { id } movements { totalCount } documents { id } } }`
	}
	f := newAssetFixture(t)
	f.h.WithModules(modules{ModuleDocuments: true})
	if resp := f.run(t, f.h, assetRead, nil, q(f)); resp.codeAt("asset") != "UNAVAILABLE" || resp.Data["asset"] != nil {
		t.Errorf("inventory off: %+v", resp)
	}
	if f.n.get("asset") != 0 {
		t.Error("asset read with inventory off")
	}

	f.h.WithModules(modules{ModuleInventory: true})
	resp := f.run(t, f.h, assetRead, nil, q(f))
	if resp.codeAt("asset.documents") != "UNAVAILABLE" || len(resp.Errors) != 1 {
		t.Errorf("documents off: %+v", resp.Errors)
	}
	if a, _ := resp.Data["asset"].(map[string]any); a == nil || a["id"] != f.asset {
		t.Errorf("documents off: asset %v", resp.Data)
	}

	f.h.WithModules(nil)
	if resp := f.run(t, f.h, assetRead, nil, q(f)); resp.codeAt("asset") != "UNAVAILABLE" {
		t.Errorf("no module checker: %+v", resp.Errors)
	}

	bare := NewHandler(ci.NewMemoryRepository(), relationship.NewMemoryRepository()).
		WithEntitlements(entitled{entitlement.FeatureAssetsBasic: true}).WithModules(modules{ModuleInventory: true})
	if resp := f.run(t, bare, assetRead, nil, q(f)); resp.codeAt("asset") != "UNAVAILABLE" {
		t.Errorf("no asset source: %+v", resp.Errors)
	}
}

// TestInvisibleAssetIsNull: an asset outside the scope or unknown is null
// without an error.
func TestInvisibleAssetIsNull(t *testing.T) {
	f := newAssetFixture(t)
	for _, id := range []string{"00000000-0000-4000-8000-00000000ffff", "not-an-id"} {
		resp := f.run(t, f.h, assetRead, nil, `{ asset(id: "`+id+`") { id cis { id } } }`)
		if len(resp.Errors) != 0 || resp.Data["asset"] != nil {
			t.Errorf("asset %s: %+v", id, resp)
		}
	}
}

// TestAssetFieldPermissions: the asset fields follow the asset policy
// (RBA-07): an unreadable field is null, an unreadable custom field is left
// out, the structural members stay.
func TestAssetFieldPermissions(t *testing.T) {
	f := newAssetFixture(t)
	ctx := context.Background()
	repo := permission.NewMemoryFieldGrants()
	role := frRole
	for _, g := range []permission.FieldGrant{
		{RoleID: &role, ObjectKind: permission.KindAsset, FieldKey: ptr("core.serial_number"), Level: permission.LevelNone},
		{RoleID: &role, ObjectKind: permission.KindAsset, FieldKey: ptr("attributes.secret"), Level: permission.LevelNone},
	} {
		if _, err := repo.CreateFieldGrant(ctx, fixtureOrg, &g); err != nil {
			t.Fatal(err)
		}
	}
	policy, err := permission.NewFieldResolver(nil, nil, repo, nil).PolicyForGrants(ctx, fixtureOrg,
		[]identity.Grant{{RoleID: frRole, Permissions: assetRead}}, nil)
	if err != nil || !policy.Restricted(permission.KindAsset) {
		t.Fatalf("policy: %v", err)
	}
	resp := f.run(t, f.h, assetRead, policy, `{ asset(id: "`+f.asset+`") { id name inventoryNo serialNumber customFields } }`)
	if len(resp.Errors) != 0 {
		t.Fatalf("errors: %+v", resp.Errors)
	}
	a := resp.Data["asset"].(map[string]any)
	if a["serialNumber"] != nil || a["inventoryNo"] != "INV-main" || a["name"] != "main" {
		t.Errorf("asset: %v", a)
	}
	custom, _ := a["customFields"].(map[string]any)
	if _, leaked := custom["secret"]; leaked || custom["rack_slot"] != "A1" {
		t.Errorf("customFields: %v", custom)
	}
	// Without a policy source the fields fail closed.
	h := f.h
	f.n.reset()
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	req := httptest.NewRequest(http.MethodPost, Path, strings.NewReader(gql(`{ asset(id: "`+f.asset+`") { serialNumber } }`)))
	req.Header.Set("Content-Type", "application/json")
	reqCtx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: fixtureOrg, UserID: "user-1"})
	reqCtx = identity.WithPrincipal(reqCtx, identity.Principal{Subject: "user-1", OrganizationID: fixtureOrg, Permissions: assetRead,
		Type: identity.PrincipalTypeUser})
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req.WithContext(reqCtx))
	if strings.Contains(w.Body.String(), "SN-main") || !strings.Contains(w.Body.String(), codeInternal) {
		t.Errorf("without policy: %s", w.Body)
	}
}

// TestDocumentFieldPermissions: the document fields of Asset.documents
// follow the document policy (RBA-07, WP-267, finding F-1 of WP-326): a
// field with the grant none and a sensitive one without grant are null,
// the structural members and the readable fields stay.
func TestDocumentFieldPermissions(t *testing.T) {
	f := newAssetFixture(t)
	ctx := context.Background()
	repo := permission.NewMemoryFieldGrants()
	role := frRole
	g := permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindDocument, FieldKey: ptr("core.tags"), Level: permission.LevelNone}
	if _, err := repo.CreateFieldGrant(ctx, fixtureOrg, &g); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSensitiveField(ctx, fixtureOrg, &permission.SensitiveField{ObjectKind: permission.KindDocument,
		FieldKey: "core.file_name"}); err != nil {
		t.Fatal(err)
	}
	policy, err := permission.NewFieldResolver(nil, nil, repo, nil).PolicyForGrants(ctx, fixtureOrg,
		[]identity.Grant{{RoleID: frRole, Permissions: assetRead}}, nil)
	if err != nil || !policy.Restricted(permission.KindDocument) {
		t.Fatalf("policy: %v", err)
	}
	resp := f.run(t, f.h, assetRead, policy, `{ asset(id: "`+f.asset+`") { documents { `+documentFields+` } } }`)
	if len(resp.Errors) != 0 {
		t.Fatalf("errors: %+v", resp.Errors)
	}
	docs, _ := resp.Data["asset"].(map[string]any)["documents"].([]any)
	if len(docs) != 2 {
		t.Fatalf("documents: %v", docs)
	}
	for _, raw := range docs {
		d := raw.(map[string]any)
		if d["fileName"] != nil || d["tags"] != nil {
			t.Errorf("hidden fields delivered: %v", d)
		}
		if d["title"] == nil || d["mimeType"] != "application/pdf" || d["category"] != "general" {
			t.Errorf("readable fields missing: %v", d)
		}
	}
	// Without a restriction every field is there.
	open := f.run(t, f.h, assetRead, nil, `{ asset(id: "`+f.asset+`") { documents { fileName tags } } }`)
	for _, raw := range open.Data["asset"].(map[string]any)["documents"].([]any) {
		if d := raw.(map[string]any); d["fileName"] == nil || d["tags"] == nil {
			t.Errorf("unrestricted: %v", d)
		}
	}
}

// TestAssetDetailComplexity: the detail with every field and the default
// pages stays within the budget (SEC-10, E-16); three of them in one
// request are refused.
func TestAssetDetailComplexity(t *testing.T) {
	f := newAssetFixture(t)
	one := detailQuery(f.asset, "a: ", 50, 50, 20, 50)
	if resp := f.run(t, f.h, assetRead, nil, "{ "+one+" }"); len(resp.Errors) != 0 {
		t.Errorf("one detail: %+v", resp.Errors)
	}
	three := "{ " + one + " " + detailQuery(f.asset, "b: ", 50, 50, 20, 50) + " " + detailQuery(f.asset, "c: ", 50, 50, 20, 50) + " }"
	resp := f.run(t, f.h, assetRead, nil, three)
	if len(resp.Errors) == 0 || !strings.Contains(resp.Errors[0].Message, "complexity") {
		t.Errorf("three details: %+v", resp.Errors)
	}
	if f.n.get("asset") != 0 {
		t.Error("a refused query read the asset")
	}
}
