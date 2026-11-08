package rls_test

// Cross-tenant references of the phase-2 write paths (ABN-03, TEN-05,
// SR-03; WP-827). Foreign keys are checked without row level security
// (000136), so every P2 write path has to refuse a reference into another
// organization itself, exactly like an unknown id (no existence oracle,
// pattern WP-639): 422 for a foreign reference in the body, 404 for a
// foreign object in the path (matrix row reference, WP-409). The database
// is the second line: the composite foreign keys with the organization
// refuse such a row with 23503 even when a writer skips the check.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// frSide holds the objects of one organization of the scenario.
type frSide struct {
	p                                           *channelPrincipal
	org, client, site, room, bin, user, user2   string
	team, supplier, contract, asset, consumable string
	consumable2                                 string // without stock
	document, ci, ci2, ticket, order, profile   string
	refType, refCI                              string // organization B only
	assetIDs, ciIDs                             []string
	tag                                         string
}

// frEnv is the scenario of TestForeignReferencesP2.
type frEnv struct {
	f     *scopetest.Fixture
	env   *channelEnv
	blobs blob.Store
	bg    context.Context
	a, b  *frSide
	seq   int
}

// frProbe is one write of organization B: with a reference of A it must
// answer want, with an unknown id the same status, with the own reference
// (own != "") 2xx.
type frProbe struct {
	name, method string
	path         func(ref string) string
	body         func(ref string) any
	foreign, own string
	want         int
	// deviation: the path answers a foreign or unknown reference in the
	// body with 404 instead of the 422 of the matrix row reference (WP-409),
	// for both alike, so without an existence oracle; handed to WP-329 as
	// finding SR-P2-01 (docs/acceptance/abn-03-phase2.md).
	deviation bool
}

// TestForeignReferencesP2 covers WP-827 (ABN-03 cross-tenant references,
// TEN-05, SR-03): organization B writes with references to objects of
// organization A through every P2 write path: reservation (asset and
// quantity of an article in a bin), assignment and transfer to a user or
// team, stock booking, document link (foreign document, foreign target),
// reference attributes of every reference type, asset and CI link, asset
// with supplier and contract, import, CI and asset bulk, ticket link,
// order item and stocktake. A synchronous write answers exactly 422 for a
// foreign reference in the body and 404 for a foreign object in the path,
// like an unknown id (the paths marked deviation answer 404 for a foreign
// reference in the body, for an unknown one alike: finding SR-P2-01 for
// WP-329); the import names every affected row in its report,
// the bulk every affected id in its partial failure report. No row of
// either organization holds the foreign reference, and each write
// succeeds with the own references. Second line (SR-03): as the
// application role in a transaction of organization B, the composite
// foreign keys refuse an id of A with 23503, among them those migration
// 000275 adds to reservation (assignee, ticket, order: before, POST
// /reservations stored a ticket or order of A). A document link goes with
// its deleted target (000275). Afterwards scopetest.IntegrityViolations
// finds no violation in either organization.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… go test -count=1 -p 1 -v -run 'TestForeignReferencesP2' ./internal/tenant/rls/
func TestForeignReferencesP2(t *testing.T) {
	e := newFREnv(t)
	a, b := e.a, e.b

	at := time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339)
	fixed := func(v any) func(string) any { return func(string) any { return v } }
	at0 := func(s string) func(string) string { return func(string) string { return s } }
	probes := []frProbe{
		// Reservations (WP-277, WP-284).
		{name: "reserve an asset for a user", method: http.MethodPost,
			path:    func(string) string { return "/api/v1/assets/" + e.freshAsset(t, b) + "/reserve" },
			body:    func(ref string) any { return map[string]any{"reserved_until": at, "assignee_id": ref} },
			foreign: a.user, own: b.user, want: http.StatusUnprocessableEntity},
		{name: "reserve an asset of the path", method: http.MethodPost,
			path: func(ref string) string { return "/api/v1/assets/" + ref + "/reserve" },
			body: fixed(map[string]any{"reserved_until": at, "project_ref": "wp827"}), foreign: a.asset, own: "", want: http.StatusNotFound},
		{name: "reservation of an asset", method: http.MethodPost, path: at0("/api/v1/reservations"),
			body: func(ref string) any {
				return map[string]any{"item_kind": "asset", "asset_id": ref, "reserved_until": at}
			},
			foreign: a.asset, own: "fresh", want: http.StatusNotFound, deviation: true},
		{name: "reservation of an article", method: http.MethodPost, path: at0("/api/v1/reservations"),
			body: func(ref string) any {
				return map[string]any{"item_kind": "consumable", "consumable_id": ref, "location_id": b.bin, "quantity": 1, "project_ref": "wp827"}
			}, foreign: a.consumable, own: b.consumable, want: http.StatusNotFound, deviation: true},
		{name: "reservation in a bin", method: http.MethodPost, path: at0("/api/v1/reservations"),
			body: func(ref string) any {
				return map[string]any{"item_kind": "consumable", "consumable_id": b.consumable, "location_id": ref, "quantity": 1, "project_ref": "wp827"}
			}, foreign: a.bin, own: b.bin, want: http.StatusNotFound, deviation: true},
		// Columns of reservation without a foreign key.
		{name: "reservation for a user", method: http.MethodPost, path: at0("/api/v1/reservations"),
			body: func(ref string) any {
				return map[string]any{"item_kind": "asset", "asset_id": e.freshAsset(t, b), "reserved_until": at, "assignee_id": ref}
			}, foreign: a.user, own: b.user, want: http.StatusUnprocessableEntity},
		{name: "reservation for a ticket", method: http.MethodPost, path: at0("/api/v1/reservations"),
			body: func(ref string) any {
				return map[string]any{"item_kind": "asset", "asset_id": e.freshAsset(t, b), "reserved_until": at, "project_ref": "wp827", "ticket_id": ref}
			}, foreign: a.ticket, own: b.ticket, want: http.StatusUnprocessableEntity},
		{name: "reservation for an order", method: http.MethodPost, path: at0("/api/v1/reservations"),
			body: func(ref string) any {
				return map[string]any{"item_kind": "asset", "asset_id": e.freshAsset(t, b), "reserved_until": at, "project_ref": "wp827", "order_id": ref}
			}, foreign: a.order, own: b.order, want: http.StatusUnprocessableEntity},
		// Assignment and transfer (WP-275).
		{name: "assignment to a user", method: http.MethodPost,
			path:    func(string) string { return "/api/v1/assets/" + e.freshAsset(t, b) + "/assign" },
			body:    func(ref string) any { return map[string]any{"assigned_user_id": ref} },
			foreign: a.user, own: b.user, want: http.StatusUnprocessableEntity},
		{name: "assignment to a team", method: http.MethodPost,
			path:    func(string) string { return "/api/v1/assets/" + e.freshAsset(t, b) + "/assign" },
			body:    func(ref string) any { return map[string]any{"assigned_team_id": ref} },
			foreign: a.team, own: b.team, want: http.StatusUnprocessableEntity},
		{name: "assignment of an asset of the path", method: http.MethodPost,
			path: func(ref string) string { return "/api/v1/assets/" + ref + "/assign" },
			body: fixed(map[string]any{"assigned_user_id": b.user}), foreign: a.asset, want: http.StatusNotFound},
		{name: "transfer to a user", method: http.MethodPost,
			path: func(string) string { return "/api/v1/assets/" + e.heldAsset(t, b) + "/transfer" },
			body: func(ref string) any {
				return map[string]any{"expected_holder": b.user, "assigned_user_id": ref}
			}, foreign: a.user, own: b.user2, want: http.StatusUnprocessableEntity},
		{name: "transfer to a team", method: http.MethodPost,
			path: func(string) string { return "/api/v1/assets/" + e.heldAsset(t, b) + "/transfer" },
			body: func(ref string) any {
				return map[string]any{"expected_holder": b.user, "assigned_team_id": ref}
			}, foreign: a.team, own: b.team, want: http.StatusUnprocessableEntity},
		// Stock bookings (WP-756).
		{name: "booking of an article", method: http.MethodPost, path: at0("/api/v1/stock-movements"),
			body: func(ref string) any {
				return map[string]any{"movement_type": "receipt", "consumable_id": ref, "to_location_id": b.bin, "quantity": 1, "reason": "wp827"}
			}, foreign: a.consumable, own: b.consumable, want: http.StatusNotFound, deviation: true},
		{name: "booking into a bin", method: http.MethodPost, path: at0("/api/v1/stock-movements"),
			body: func(ref string) any {
				return map[string]any{"movement_type": "receipt", "consumable_id": b.consumable, "to_location_id": ref, "quantity": 1, "reason": "wp827"}
			}, foreign: a.bin, own: b.bin, want: http.StatusUnprocessableEntity},
		{name: "booking out of a bin", method: http.MethodPost, path: at0("/api/v1/stock-movements"),
			body: func(ref string) any {
				return map[string]any{"movement_type": "retrieval", "consumable_id": b.consumable, "from_location_id": ref, "quantity": 1, "reason": "wp827"}
			}, foreign: a.bin, own: b.bin, want: http.StatusUnprocessableEntity},
		{name: "transfer of an asset", method: http.MethodPost, path: at0("/api/v1/stock-movements"),
			body: func(ref string) any {
				return map[string]any{"movement_type": "transfer", "asset_id": ref, "to_location_id": b.room, "reason": "wp827"}
			}, foreign: a.asset, own: "fresh", want: http.StatusNotFound, deviation: true},
		{name: "transfer of an asset to a location", method: http.MethodPost, path: at0("/api/v1/stock-movements"),
			body: func(ref string) any {
				return map[string]any{"movement_type": "transfer", "asset_id": e.freshAsset(t, b), "to_location_id": ref, "reason": "wp827"}
			}, foreign: a.room, own: b.room, want: http.StatusUnprocessableEntity},
		{name: "booking for a ticket", method: http.MethodPost, path: at0("/api/v1/stock-movements"),
			body: func(ref string) any {
				return map[string]any{"movement_type": "receipt", "consumable_id": b.consumable, "to_location_id": b.bin, "quantity": 1, "reason": "wp827", "ticket_id": ref}
			}, foreign: a.ticket, own: b.ticket, want: http.StatusUnprocessableEntity},
		{name: "booking for an order", method: http.MethodPost, path: at0("/api/v1/stock-movements"),
			body: func(ref string) any {
				return map[string]any{"movement_type": "receipt", "consumable_id": b.consumable, "to_location_id": b.bin, "quantity": 1, "reason": "wp827", "order_id": ref}
			}, foreign: a.order, own: b.order, want: http.StatusUnprocessableEntity},
		{name: "booking with a document", method: http.MethodPost, path: at0("/api/v1/stock-movements"),
			body: func(ref string) any {
				return map[string]any{"movement_type": "receipt", "consumable_id": b.consumable, "to_location_id": b.bin, "quantity": 1, "reason": "wp827", "document_id": ref}
			}, foreign: a.document, own: b.document, want: http.StatusUnprocessableEntity},
		// Asset (WP-269, WP-272).
		{name: "asset of a supplier", method: http.MethodPost, path: at0("/api/v1/assets"),
			body: func(ref string) any {
				return map[string]any{"name": e.name("wp827-asset"), "inventory_no": e.name("wp827-inv"), "client_id": b.client, "supplier_id": ref}
			}, foreign: a.supplier, own: b.supplier, want: http.StatusUnprocessableEntity},
		{name: "asset under a contract", method: http.MethodPost, path: at0("/api/v1/assets"),
			body: func(ref string) any {
				return map[string]any{"name": e.name("wp827-asset"), "inventory_no": e.name("wp827-inv"), "client_id": b.client, "contract_id": ref}
			}, foreign: a.contract, own: b.contract, want: http.StatusUnprocessableEntity},
		{name: "link of a CI", method: http.MethodPost,
			path:    func(string) string { return "/api/v1/assets/" + e.freshAsset(t, b) + "/cis" },
			body:    func(ref string) any { return map[string]any{"ci_id": ref} },
			foreign: a.ci, own: "freshci", want: http.StatusNotFound, deviation: true},
		{name: "link of a CI to an asset of the path", method: http.MethodPost,
			path: func(ref string) string { return "/api/v1/assets/" + ref + "/cis" },
			body: func(string) any { return map[string]any{"ci_id": e.freshCI(t, b)} }, foreign: a.asset, want: http.StatusNotFound},
		// Ticket links (WP-764).
		{name: "ticket link of a ticket of the path", method: http.MethodPost,
			path: func(ref string) string { return "/api/v1/tickets/" + ref + "/links" },
			body: fixed(map[string]any{"entity_type": "asset", "entity_id": b.asset}), foreign: a.ticket, want: http.StatusNotFound},
		// Orders (WP-286) and stocktakes (WP-287).
		{name: "order item of an article", method: http.MethodPost, path: at0("/api/v1/orders/" + b.order + "/items"),
			body: func(ref string) any {
				return map[string]any{"description": "wp827", "quantity": 1, "consumable_id": ref}
			}, foreign: a.consumable, own: b.consumable, want: http.StatusUnprocessableEntity},
		{name: "order item of an asset", method: http.MethodPost, path: at0("/api/v1/orders/" + b.order + "/items"),
			body: func(ref string) any {
				return map[string]any{"description": "wp827", "quantity": 1, "asset_id": ref}
			}, foreign: a.asset, own: b.asset, want: http.StatusUnprocessableEntity},
		{name: "order item of an order of the path", method: http.MethodPost,
			path: func(ref string) string { return "/api/v1/orders/" + ref + "/items" },
			body: fixed(map[string]any{"description": "wp827", "quantity": 1}), foreign: a.order, want: http.StatusNotFound},
		{name: "stocktake of a location", method: http.MethodPost, path: at0("/api/v1/stocktakes"),
			body: func(ref string) any {
				return map[string]any{"title": e.name("wp827-stocktake"), "location_id": ref}
			}, foreign: a.room, own: b.room, want: http.StatusUnprocessableEntity},
	}
	// Document links (WP-267): a foreign target of every kind, a foreign
	// document of the path.
	for _, target := range []struct{ kind, foreign, own string }{
		{"ci", a.ci, b.ci}, {"asset", a.asset, b.asset}, {"ticket", a.ticket, b.ticket},
		{"location", a.room, b.room}, {"contract", a.contract, b.contract},
	} {
		probes = append(probes, frProbe{name: "document link to a " + target.kind, method: http.MethodPost,
			path:    at0("/api/v1/documents/" + b.document + "/links"),
			body:    func(ref string) any { return map[string]any{"entity_type": target.kind, "entity_id": ref} },
			foreign: target.foreign, own: target.own, want: http.StatusNotFound, deviation: true})
	}
	probes = append(probes, frProbe{name: "document link of a document of the path", method: http.MethodPost,
		path: func(ref string) string { return "/api/v1/documents/" + ref + "/links" },
		body: fixed(map[string]any{"entity_type": "asset", "entity_id": b.asset}), foreign: a.document, want: http.StatusNotFound})
	// Ticket links (WP-764): a foreign object of every kind.
	for _, target := range []struct{ kind, foreign, own string }{
		{"ci", a.ci, b.ci}, {"asset", a.asset, b.asset}, {"location", a.room, b.room}, {"contract", a.contract, b.contract},
	} {
		probes = append(probes, frProbe{name: "ticket link to a " + target.kind, method: http.MethodPost,
			path:    at0("/api/v1/tickets/" + b.ticket + "/links"),
			body:    func(ref string) any { return map[string]any{"entity_type": target.kind, "entity_id": ref} },
			foreign: target.foreign, own: target.own, want: http.StatusNotFound, deviation: true})
	}
	// Reference attributes (WP-254): one value of every reference type.
	for _, r := range frRefAttributes(a, b) {
		probes = append(probes, frProbe{name: "reference attribute " + r.key, method: http.MethodPatch, path: at0("/api/v1/cis/" + b.refCI),
			body:    func(ref string) any { return map[string]any{"attributes": map[string]any{r.key: ref}} },
			foreign: r.foreign, own: r.own, want: http.StatusUnprocessableEntity})
	}

	for _, pr := range probes {
		t.Run(pr.name, func(t *testing.T) {
			refs := []struct{ id, label string }{{pr.foreign, "of organization A"}, {unknownID, "unknown"}}
			for _, ref := range refs {
				code, body := e.env.call(b.p, pr.method, pr.path(ref.id), pr.body(ref.id))
				if code != pr.want {
					t.Errorf("%s %s: %d %s, want %d as for an unknown id (deviation SR-P2-01: %v)", pr.name, ref.label, code, trim(body), pr.want, pr.deviation)
				}
				if ref.id == pr.foreign && strings.Contains(body, pr.foreign) {
					t.Errorf("%s %s: the answer names the object of organization A: %s", pr.name, ref.label, trim(body))
				}
			}
			own := pr.own
			switch own {
			case "":
				return
			case "fresh":
				own = e.freshAsset(t, b)
			case "freshci":
				own = e.freshCI(t, b)
			}
			if code, body := e.env.call(b.p, pr.method, pr.path(own), pr.body(own)); code < 200 || code > 299 {
				t.Errorf("%s with the own reference: %d %s, want 2xx", pr.name, code, trim(body))
			}
		})
	}

	t.Run("import", func(t *testing.T) { e.importRows(t) })
	t.Run("bulk", func(t *testing.T) { e.bulk(t) })
	t.Run("composite foreign keys", func(t *testing.T) { e.compositeKeys(t) })
	t.Run("deleted link targets", func(t *testing.T) { e.deletedTargets(t) })

	// No row of either organization references an object of the other.
	t.Run("no foreign reference stored", func(t *testing.T) {
		for _, side := range [][2]*frSide{{b, a}, {a, b}} {
			if n := e.storedForeign(t, side[0].org, side[1].org); len(n) > 0 {
				t.Errorf("rows of organization %s reference organization %s: %v", side[0].tag, side[1].tag, n)
			}
		}
	})
	t.Run("integrity check", func(t *testing.T) {
		for _, side := range []*frSide{a, b} {
			if v := scopetest.IntegrityViolations(t, e.f.Admin, side.org); len(v) > 0 {
				t.Errorf("ABN-03: tests/integrity-check.sql for organization %s: %v", side.tag, v)
			}
		}
	})
}

// frRefAttribute is a reference attribute of the CI type of organization B
// with a value of A and an own value.
type frRefAttribute struct{ key, dataType, foreign, own string }

func frRefAttributes(a, b *frSide) []frRefAttribute {
	return []frRefAttribute{
		{"wp827_reference", fieldmeta.TypeReference, a.ci, b.ci2},
		{"wp827_ci_ref", fieldmeta.TypeCIRef, a.ci, b.ci2},
		{"wp827_asset_ref", fieldmeta.TypeAssetRef, a.asset, b.asset},
		{"wp827_contract_ref", fieldmeta.TypeContractRef, a.contract, b.contract},
		{"wp827_user", fieldmeta.TypeUser, a.user, b.user},
		{"wp827_team", fieldmeta.TypeTeam, a.team, b.team},
		{"wp827_location", fieldmeta.TypeLocation, a.room, b.room},
		{"wp827_file", fieldmeta.TypeFile, a.document, b.document},
	}
}

func newFREnv(t *testing.T) *frEnv {
	f := scopetest.Seed(t, "e5")
	blobs := blob.NewFileStore(t.TempDir())
	e := &frEnv{f: f, env: newChannelEnvWithBlobs(t, f, blobs), blobs: blobs, bg: context.Background()}
	clientB := e.scalar(t, `INSERT INTO client (organization_id, name, slug) VALUES ($1, 'wp827 client b', 'scopetest-e5-b-c1') RETURNING id::text`, f.OrgB)
	e.a = e.side(t, f.OrgA, f.Client1, "a")
	e.b = e.side(t, f.OrgB, clientB, "b")

	// A CI type of organization B with an attribute of every reference
	// type and a CI of it (WP-254).
	b := e.b
	ctxB := tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB, UserID: b.user})
	types := citype.NewPGRepository(f.App)
	refType := &citype.Type{OrganizationID: f.OrgB, Key: "wp827-ref", Name: "wp827-ref", IsObservable: true}
	if err := types.Create(ctxB, refType); err != nil {
		t.Fatalf("setup: CI type of organization B: %v", err)
	}
	for _, r := range frRefAttributes(e.a, b) {
		if err := types.CreateAttributeDefinition(ctxB, f.OrgB, &citype.AttributeDefinition{CITypeID: refType.ID, Key: r.key, DataType: r.dataType}); err != nil {
			t.Fatalf("setup: attribute %s: %v", r.key, err)
		}
	}
	b.refType = refType.ID
	b.refCI = e.create(t, b.p, "/api/v1/cis", map[string]any{"ci_type_id": b.refType, "name": "wp827-ref-b", "client_id": b.client})
	return e
}

// side writes the objects of one organization, through the API where it
// has a write path.
func (e *frEnv) side(t *testing.T, org, client, tag string) *frSide {
	t.Helper()
	f := e.f
	s := &frSide{org: org, client: client, tag: tag}
	s.user = f.AppUser(t, org, "wp827-"+tag)
	s.user2 = f.AppUser(t, org, "wp827-"+tag+"2")
	e.exec(t, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, org, s.user)
	s.p = &channelPrincipal{kind: "org-" + tag, org: org, user: s.user, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	s.team = e.scalar(t, `INSERT INTO team (organization_id, name) VALUES ($1, 'wp827 team') RETURNING id::text`, org)
	if err := f.Admin.QueryRow(e.bg, `
		WITH st AS (INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id),
		     bd AS (INSERT INTO building (organization_id, site_id, name) SELECT $1, id, $3 FROM st RETURNING id, site_id)
		INSERT INTO room (organization_id, building_id, name) SELECT $1, id, $3 FROM bd
		RETURNING site_id::text, id::text`, org, client, "wp827-"+tag).Scan(&s.site, &s.room); err != nil {
		t.Fatalf("setup: site of %s: %v", tag, err)
	}
	parent := s.site
	for _, kind := range []locations.Kind{locations.KindWarehouse, locations.KindZone, locations.KindShelf, locations.KindBin} {
		l, err := locations.NewPGRepository(f.App).Create(f.OrgCtx(org), org, locations.CreateRequest{Kind: kind, ParentID: parent, Name: "wp827-" + tag + "-" + string(kind)})
		if err != nil {
			t.Fatalf("setup: %s of %s: %v", kind, tag, err)
		}
		parent = l.ID
	}
	s.bin = parent

	p := s.p
	s.supplier = e.create(t, p, "/api/v1/suppliers", map[string]any{"name": "wp827-sup-" + tag, "client_id": client})
	s.contract = e.create(t, p, "/api/v1/contracts", map[string]any{"number": "wp827-con-" + tag, "client_id": client,
		"supplier_id": s.supplier, "start_date": "2024-01-01", "end_date": "2030-01-01"})
	s.asset = e.create(t, p, "/api/v1/assets", map[string]any{"name": "wp827-asset-" + tag, "inventory_no": "wp827-inv-" + tag,
		"client_id": client, "supplier_id": s.supplier, "contract_id": s.contract})
	e.create(t, p, "/api/v1/stock-movements", map[string]any{"movement_type": "transfer", "asset_id": s.asset, "to_location_id": s.room, "reason": "wp827"})
	s.consumable = e.create(t, p, "/api/v1/consumables", map[string]any{"name": "wp827-cons-" + tag, "client_id": client, "unit": "Stk"})
	s.consumable2 = e.create(t, p, "/api/v1/consumables", map[string]any{"name": "wp827-cons2-" + tag, "client_id": client, "unit": "Stk"})
	e.create(t, p, "/api/v1/stock-movements", map[string]any{"movement_type": "receipt", "consumable_id": s.consumable,
		"to_location_id": s.bin, "quantity": 50, "reason": "wp827"})
	s.ci = e.create(t, p, "/api/v1/cis", map[string]any{"ci_type_id": e.serverType(t), "name": "wp827-ci-" + tag, "client_id": client, "location_id": s.site})
	s.ci2 = e.create(t, p, "/api/v1/cis", map[string]any{"ci_type_id": e.serverType(t), "name": "wp827-ci2-" + tag, "client_id": client, "location_id": s.site})
	s.document = e.create(t, p, "/api/v1/documents", map[string]any{"title": "wp827-doc-" + tag, "file_name": "wp827-" + tag + ".txt"})
	s.ticket = e.create(t, p, "/api/v1/tickets", map[string]any{"title": "wp827-ticket-" + tag, "related_asset_id": s.asset})
	s.order = e.create(t, p, "/api/v1/orders", map[string]any{"title": "wp827-order-" + tag, "client_id": client, "supplier_id": s.supplier})
	s.profile = e.create(t, p, "/api/v1/import-profiles", map[string]any{"name": "wp827-profile-" + tag, "entity": "asset",
		"format": "json", "mode": "create_missing", "mapping": map[string]any{"columns": map[string]any{
			"inventory_no": map[string]any{"target": "inventory_no"}, "name": map[string]any{"target": "name"},
			"client_id": map[string]any{"target": "client_id"}, "supplier_id": map[string]any{"target": "supplier_id"},
			"contract_id": map[string]any{"target": "contract_id"}, "owner_user_id": map[string]any{"target": "owner_user_id"},
			"owner_team_id": map[string]any{"target": "owner_team_id"}}}})
	return s
}

// importRows imports rows of organization B with a client, supplier,
// contract, owner user or owner team of A: the run ends with every such
// row in its report and creates none of them; the row with the own
// references is created (IO-01, WP-303).
func (e *frEnv) importRows(t *testing.T) {
	a, b := e.a, e.b
	own := map[string]any{"client_id": b.client, "supplier_id": b.supplier, "contract_id": b.contract, "owner_user_id": b.user, "owner_team_id": b.team}
	foreign := map[string]string{"client_id": a.client, "supplier_id": a.supplier, "contract_id": a.contract, "owner_user_id": a.user, "owner_team_id": a.team}
	fields := []string{"client_id", "supplier_id", "contract_id", "owner_user_id", "owner_team_id"}
	var rows []map[string]any
	var names []string
	for _, field := range append(fields, "") {
		name := e.name("wp827-imp")
		row := map[string]any{"inventory_no": name, "name": name}
		for k, v := range own {
			row[k] = v
		}
		if field != "" {
			row[field] = foreign[field]
		}
		rows = append(rows, row)
		names = append(names, name)
	}
	code, resp := e.env.call(b.p, http.MethodPost, "/api/v1/imports", map[string]any{"profile_id": b.profile, "rows": rows})
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(resp), &started); err != nil || code != http.StatusAccepted || started.JobID == "" {
		t.Fatalf("import: %d %s", code, trim(resp))
	}
	if status := e.awaitJob(t, started.JobID); status != string(jobs.StatusSucceeded) {
		t.Fatalf("import ended %s", status)
	}
	report := e.read(t, importer.ReportKey(b.org, started.JobID))
	for i, name := range names {
		created := e.scalar(t, `SELECT count(*)::text FROM asset WHERE inventory_no = $1`, name)
		if i < len(fields) {
			// The row is refused on the foreign field or on a field that does
			// not fit it (a supplier of B for a client of A).
			line := fmt.Sprintf("\n%d;", i+1)
			if created != "0" || !strings.Contains(report, line) || strings.Contains(report, fmt.Sprintf("\n%d;;created;", i+1)) ||
				strings.Contains(report, foreign[fields[i]]) {
				t.Errorf("import row with the %s of organization A: %s created, report %q, want none and a line %q without the id",
					fields[i], created, report, line)
			}
		} else if created != "1" || !strings.Contains(report, fmt.Sprintf("\n%d;;created;", i+1)) {
			t.Errorf("import row with the own references: %s created, report %q, want 1 and the line created", created, report)
		}
	}
}

// bulk runs the CI and asset bulk of organization B on ids of A (each in
// the report with 404) and on own ids with a location of A (each in the
// report with 422 on the location); with the own location every id
// succeeds (WP-304, WP-781).
func (e *frEnv) bulk(t *testing.T) {
	a, b := e.a, e.b
	for _, c := range []struct {
		path, statusValue, locationKey string
		foreignIDs                     []string
		fresh                          func() string
		wrongLocation, ownLocation     string
		locationField                  string
	}{
		{"/api/v1/cis/bulk", "inactive", "location_id", []string{a.ci, a.ci2}, func() string { return e.freshCI(t, b) },
			a.site, b.site, "location_id"},
		{"/api/v1/assets/bulk", "maintenance", "to_location_id", []string{a.asset, e.freshAsset(t, a)}, func() string { return e.freshAsset(t, b) },
			a.room, b.room, "to_location_id"},
	} {
		res := e.bulkCall(t, c.path, map[string]any{"action": "set_status", "ids": c.foreignIDs, "status": c.statusValue})
		frItems(t, c.path+" with ids of organization A", &res, c.foreignIDs, http.StatusNotFound, "")
		ownIDs := []string{c.fresh(), c.fresh()}
		res = e.bulkCall(t, c.path, map[string]any{"action": "set_location", "ids": ownIDs, "params": map[string]any{c.locationKey: c.wrongLocation}})
		frItems(t, c.path+" to a location of organization A", &res, ownIDs, http.StatusUnprocessableEntity, c.locationField)
		res = e.bulkCall(t, c.path, map[string]any{"action": "set_location", "ids": ownIDs, "params": map[string]any{c.locationKey: unknownID}})
		frItems(t, c.path+" to an unknown location", &res, ownIDs, http.StatusUnprocessableEntity, c.locationField)
		res = e.bulkCall(t, c.path, map[string]any{"action": "set_location", "ids": ownIDs, "params": map[string]any{c.locationKey: c.ownLocation}})
		if res.Succeeded != len(ownIDs) {
			t.Errorf("%s to the own location: %+v, want every id", c.path, res)
		}
	}
}

// frItems checks that the report names every id with code (and field).
func frItems(t *testing.T, name string, res *ci.BulkResult, ids []string, code int, field string) {
	t.Helper()
	if res.Succeeded != 0 || res.Failed != len(ids) {
		t.Errorf("%s: %d succeeded, %d failed, want 0 and %d", name, res.Succeeded, res.Failed, len(ids))
	}
	for _, id := range ids {
		found := false
		for _, it := range res.Items {
			if it.ID != id {
				continue
			}
			found = true
			if it.Code != code || (field != "" && (len(it.Fields) != 1 || it.Fields[0] != field)) {
				t.Errorf("%s: report of %s is %+v, want %d on %q", name, id, it, code, field)
			}
		}
		if !found {
			t.Errorf("%s: no report of %s in %+v", name, id, res.Items)
		}
	}
}

func (e *frEnv) bulkCall(t *testing.T, path string, body map[string]any) ci.BulkResult {
	t.Helper()
	code, resp := e.env.call(e.b.p, http.MethodPost, path, body)
	var res ci.BulkResult
	if code != http.StatusOK || json.Unmarshal([]byte(resp), &res) != nil {
		t.Fatalf("bulk %s %v: %d %s, want 200 with the report", path, body["action"], code, trim(resp))
	}
	return res
}

// compositeKeys writes, as the application role in a transaction of
// organization B, rows that name an object of A: the composite foreign
// keys with the organization refuse each with 23503 (SR-03, second line);
// the same write with the own object is accepted (and rolled back).
func (e *frEnv) compositeKeys(t *testing.T) {
	a, b := e.a, e.b
	scope := database.OrgWideScope(b.org, b.user)
	for _, c := range []struct {
		constraint, stmt string
		foreign, own     [2]string
	}{
		{"reservation_stock_level_fkey", `INSERT INTO reservation (organization_id, item_kind, consumable_id, location_id, quantity, state, project_ref)
			VALUES ($1, 'consumable', $2, $3, 1, 'active', 'wp827')`,
			[2]string{a.consumable, a.bin}, [2]string{b.consumable, b.bin}},
		{"reservation_stock_level_fkey", `INSERT INTO reservation (organization_id, item_kind, consumable_id, location_id, quantity, state, project_ref)
			VALUES ($1, 'consumable', $2, $3, 1, 'active', 'wp827')`,
			[2]string{b.consumable, a.bin}, [2]string{b.consumable, b.bin}},
		{"stock_level_consumable_fkey", `INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 0)`,
			[2]string{a.consumable, b.bin}, [2]string{b.consumable2, b.bin}},
		{"stock_level_location_fkey", `INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 0)`,
			[2]string{b.consumable2, a.bin}, [2]string{b.consumable2, b.bin}},
		{"reservation_asset_id_fkey", `INSERT INTO reservation (organization_id, item_kind, asset_id, state) VALUES ($1, 'asset', $2, 'active')`,
			[2]string{a.asset}, [2]string{"fresh"}},
		// Since migration 000275 (WP-827).
		{"reservation_assignee_id_fkey", `INSERT INTO reservation (organization_id, item_kind, asset_id, state, assignee_id) VALUES ($1, 'asset', $2, 'active', $3)`,
			[2]string{"fresh", a.user}, [2]string{"fresh", b.user}},
		{"reservation_ticket_id_fkey", `INSERT INTO reservation (organization_id, item_kind, asset_id, state, ticket_id) VALUES ($1, 'asset', $2, 'active', $3)`,
			[2]string{"fresh", a.ticket}, [2]string{"fresh", b.ticket}},
		{"reservation_order_id_fkey", `INSERT INTO reservation (organization_id, item_kind, asset_id, state, order_id) VALUES ($1, 'asset', $2, 'active', $3)`,
			[2]string{"fresh", a.order}, [2]string{"fresh", b.order}},
		{"stock_movement_consumable_id_fkey", `INSERT INTO stock_movement (organization_id, item_kind, consumable_id, movement_type, to_location_id, quantity)
			VALUES ($1, 'consumable', $2, 'receipt', $3, 1)`,
			[2]string{a.consumable, b.bin}, [2]string{b.consumable, b.bin}},
		{"asset_supplier_id_fkey", `INSERT INTO asset (organization_id, client_id, asset_tag, name, supplier_id) VALUES ($1, '` + b.client + `', $2, $2, $3)`,
			[2]string{"tag", a.supplier}, [2]string{"tag", b.supplier}},
		{"asset_contract_id_fkey", `INSERT INTO asset (organization_id, client_id, asset_tag, name, contract_id) VALUES ($1, '` + b.client + `', $2, $2, $3)`,
			[2]string{"tag", a.contract}, [2]string{"tag", b.contract}},
		{"ticket_link_asset_fkey", `INSERT INTO ticket_link (organization_id, ticket_id, entity_type, asset_id) VALUES ($1, '` + b.ticket + `', 'asset', $2)`,
			[2]string{a.asset}, [2]string{"fresh"}},
		{"internal_order_item_consumable_id_fkey", `INSERT INTO internal_order_item (organization_id, order_id, description, quantity, consumable_id)
			VALUES ($1, '` + b.order + `', 'wp827', 1, $2)`, [2]string{a.consumable}, [2]string{b.consumable}},
		{"stocktake_location_fkey", `INSERT INTO stocktake (organization_id, title, location_id) VALUES ($1, 'wp827', $2)`,
			[2]string{a.room}, [2]string{b.room}},
	} {
		args := func(v [2]string) []any {
			out := []any{b.org}
			for _, x := range v {
				switch x {
				case "":
				case "fresh":
					out = append(out, e.freshAsset(t, b))
				case "tag":
					out = append(out, e.name("wp827-fk"))
				default:
					out = append(out, x)
				}
			}
			return out
		}
		t.Run(c.constraint, func(t *testing.T) {
			err := appWrite(e.bg, e.f, &scope, c.stmt, args(c.foreign))
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23503" || pgErr.ConstraintName != c.constraint {
				t.Errorf("write with an object of organization A: %v, want 23503 on %s", err, c.constraint)
			}
			if ownErr := appWrite(e.bg, e.f, &scope, c.stmt, args(c.own)); ownErr != nil {
				t.Errorf("write with the own object: %v", ownErr)
			}
		})
	}
}

// deletedTargets links the document of organization B to a ticket, an asset
// and a location and deletes them through the API: the links go with their
// targets (delete_target_document_links, migration 000275), the document
// stays, so no link names a row that does not exist (ABN-03 orphans).
func (e *frEnv) deletedTargets(t *testing.T) {
	b := e.b
	loc, err := locations.NewPGRepository(e.f.App).Create(e.f.OrgCtx(b.org), b.org,
		locations.CreateRequest{Kind: locations.KindWarehouse, ParentID: b.site, Name: e.name("wp827-gone")})
	if err != nil {
		t.Fatalf("setup: location: %v", err)
	}
	targets := []struct{ kind, id, path string }{
		{"ticket", e.create(t, b.p, "/api/v1/tickets", map[string]any{"title": e.name("wp827-gone")}), "/api/v1/tickets/"},
		{"asset", e.scalar(t, `INSERT INTO asset (organization_id, client_id, asset_tag, name) VALUES ($1, $2, $3, $3) RETURNING id::text`,
			b.org, b.client, e.name("wp827-gone")), "/api/v1/assets/"},
		{"location", loc.ID, "/api/v1/locations/"},
	}
	count := `SELECT link_count::text FROM document WHERE id = $1 AND deleted_at IS NULL`
	before := e.scalar(t, count, b.document)
	for _, target := range targets {
		e.create(t, b.p, "/api/v1/documents/"+b.document+"/links", map[string]any{"entity_type": target.kind, "entity_id": target.id})
		if code, resp := e.env.call(b.p, http.MethodDelete, target.path+target.id, nil); code != http.StatusNoContent {
			t.Fatalf("delete %s: %d %s", target.kind, code, trim(resp))
		}
		if n := e.scalar(t, `SELECT count(*)::text FROM document_link WHERE entity_id = $1`, target.id); n != "0" {
			t.Errorf("links of the deleted %s: %s, want 0", target.kind, n)
		}
	}
	if n := e.scalar(t, count, b.document); n != before {
		t.Errorf("link_count of the document after the deletes: %s, want %s", n, before)
	}
}

// storedForeign counts, per table and column, the rows of organization org
// that name an object of organization other.
func (e *frEnv) storedForeign(t *testing.T, org, other string) []string {
	t.Helper()
	checks := map[string]string{
		"reservation":         `SELECT count(*) FROM reservation r WHERE r.organization_id = $1 AND (r.asset_id IN (SELECT id FROM asset WHERE organization_id = $2) OR r.consumable_id IN (SELECT id FROM consumable WHERE organization_id = $2) OR r.location_id IN (SELECT id FROM location WHERE organization_id = $2) OR r.assignee_id IN (SELECT id FROM app_user WHERE organization_id = $2))`,
		"stock_movement":      `SELECT count(*) FROM stock_movement m WHERE m.organization_id = $1 AND (m.asset_id IN (SELECT id FROM asset WHERE organization_id = $2) OR m.consumable_id IN (SELECT id FROM consumable WHERE organization_id = $2) OR m.from_location_id IN (SELECT id FROM location WHERE organization_id = $2) OR m.to_location_id IN (SELECT id FROM location WHERE organization_id = $2) OR m.assignee_user_id IN (SELECT id FROM app_user WHERE organization_id = $2) OR m.assignee_team_id IN (SELECT id FROM team WHERE organization_id = $2))`,
		"asset":               `SELECT count(*) FROM asset x WHERE x.organization_id = $1 AND (x.supplier_id IN (SELECT id FROM supplier WHERE organization_id = $2) OR x.contract_id IN (SELECT id FROM contract WHERE organization_id = $2) OR x.client_id IN (SELECT id FROM client WHERE organization_id = $2) OR x.location_id IN (SELECT id FROM location WHERE organization_id = $2) OR x.owner_user_id IN (SELECT id FROM app_user WHERE organization_id = $2) OR x.owner_team_id IN (SELECT id FROM team WHERE organization_id = $2) OR x.assigned_user_id IN (SELECT id FROM app_user WHERE organization_id = $2) OR x.assigned_team_id IN (SELECT id FROM team WHERE organization_id = $2))`,
		"ci":                  `SELECT count(*) FROM ci c WHERE c.organization_id = $1 AND (c.asset_id IN (SELECT id FROM asset WHERE organization_id = $2) OR c.location_id IN (SELECT id FROM location WHERE organization_id = $2) OR EXISTS (SELECT 1 FROM jsonb_each_text(c.attributes) v WHERE v.value IN (SELECT id::text FROM ci WHERE organization_id = $2 UNION ALL SELECT id::text FROM asset WHERE organization_id = $2 UNION ALL SELECT id::text FROM contract WHERE organization_id = $2 UNION ALL SELECT id::text FROM app_user WHERE organization_id = $2 UNION ALL SELECT id::text FROM team WHERE organization_id = $2 UNION ALL SELECT id::text FROM location WHERE organization_id = $2 UNION ALL SELECT id::text FROM document WHERE organization_id = $2)))`,
		"document_link":       `SELECT count(*) FROM document_link l WHERE l.organization_id = $1 AND (l.document_id IN (SELECT id FROM document WHERE organization_id = $2) OR l.entity_id IN (SELECT id FROM ci WHERE organization_id = $2 UNION ALL SELECT id FROM asset WHERE organization_id = $2 UNION ALL SELECT id FROM ticket WHERE organization_id = $2 UNION ALL SELECT id FROM location WHERE organization_id = $2 UNION ALL SELECT id FROM contract WHERE organization_id = $2))`,
		"ticket_link":         `SELECT count(*) FROM ticket_link l WHERE l.organization_id = $1 AND (l.ci_id IN (SELECT id FROM ci WHERE organization_id = $2) OR l.asset_id IN (SELECT id FROM asset WHERE organization_id = $2) OR l.location_id IN (SELECT id FROM location WHERE organization_id = $2) OR l.contract_id IN (SELECT id FROM contract WHERE organization_id = $2))`,
		"internal_order_item": `SELECT count(*) FROM internal_order_item i WHERE i.organization_id = $1 AND (i.consumable_id IN (SELECT id FROM consumable WHERE organization_id = $2) OR i.asset_id IN (SELECT id FROM asset WHERE organization_id = $2))`,
		"stocktake":           `SELECT count(*) FROM stocktake s WHERE s.organization_id = $1 AND s.location_id IN (SELECT id FROM location WHERE organization_id = $2)`,
	}
	var out []string
	for table, query := range checks {
		if n := e.scalar(t, `SELECT (`+query+`)::text`, org, other); n != "0" {
			out = append(out, table+"="+n)
		}
	}
	sort.Strings(out)
	return out
}

// freshAsset inserts an asset of s in its room and returns its id.
func (e *frEnv) freshAsset(t *testing.T, s *frSide) string {
	t.Helper()
	name := e.name("wp827-fresh-" + s.tag)
	id := e.scalar(t, `INSERT INTO asset (organization_id, client_id, location_id, asset_tag, inventory_no, name)
		VALUES ($1, $2, $3, $4, $4, $4) RETURNING id::text`, s.org, s.client, s.room, name)
	s.assetIDs = append(s.assetIDs, id)
	return id
}

// heldAsset returns a fresh asset of s assigned to s.user.
func (e *frEnv) heldAsset(t *testing.T, s *frSide) string {
	t.Helper()
	id := e.freshAsset(t, s)
	e.create(t, s.p, "/api/v1/assets/"+id+"/assign", map[string]any{"assigned_user_id": s.user})
	return id
}

// freshCI creates a server CI of s at its site through the API.
func (e *frEnv) freshCI(t *testing.T, s *frSide) string {
	t.Helper()
	id := e.create(t, s.p, "/api/v1/cis", map[string]any{"ci_type_id": e.serverType(t), "name": e.name("wp827-fresh-" + s.tag),
		"client_id": s.client, "location_id": s.site})
	s.ciIDs = append(s.ciIDs, id)
	return id
}

func (e *frEnv) serverType(t *testing.T) string {
	t.Helper()
	return e.scalar(t, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`)
}

func (e *frEnv) name(prefix string) string {
	e.seq++
	return fmt.Sprintf("%s-%d", prefix, e.seq)
}

func (e *frEnv) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := e.f.Admin.Exec(e.bg, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func (e *frEnv) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	var v string
	if err := e.f.Admin.QueryRow(e.bg, query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

// create sends a POST that must answer 201 or 202 and returns the new id.
func (e *frEnv) create(t *testing.T, p *channelPrincipal, path string, body any) string {
	t.Helper()
	code, resp := e.env.call(p, http.MethodPost, path, body)
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(resp), &out)
	if (code != http.StatusCreated && code != http.StatusAccepted) || out.ID == "" {
		t.Fatalf("setup POST %s as %s: %d %s", path, p.kind, code, trim(resp))
	}
	return out.ID
}

func (e *frEnv) awaitJob(t *testing.T, id string) string {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		status := e.scalar(t, `SELECT status FROM job WHERE id = $1`, id)
		if jobs.Status(status).Terminal() {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s not finished after 60 s: %s", id, status)
		}
	}
}

// read returns an object of the bucket exports; empty when there is none.
func (e *frEnv) read(t *testing.T, key string) string {
	t.Helper()
	rc, err := e.blobs.Get(e.bg, export.ExportBucket, key)
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, rerr := rc.Read(buf)
		sb.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	return sb.String()
}
