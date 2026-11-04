package asset_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
)

// trailRow is an entity_change row of an asset.
type trailRow struct {
	changeType, field, actor, client, site string
	oldValue, newValue                     map[string]any
	oldRaw, newRaw                         []byte
	version                                *int64
}

// assetTrail reads the entity_change rows of an asset through the
// maintenance pool, in the order of their writes.
func assetTrail(t *testing.T, f *scopetest.Fixture, id string) []trailRow {
	t.Helper()
	rows, err := f.Admin.Query(context.Background(), `SELECT change_type, COALESCE(field_name, ''), COALESCE(actor_id::text, ''),
		COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), old_value, new_value, entity_version
		FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1 ORDER BY created_at, id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []trailRow
	for rows.Next() {
		var r trailRow
		if err = rows.Scan(&r.changeType, &r.field, &r.actor, &r.client, &r.site, &r.oldRaw, &r.newRaw, &r.version); err != nil {
			t.Fatal(err)
		}
		for _, v := range []struct {
			raw []byte
			out *map[string]any
		}{{r.oldRaw, &r.oldValue}, {r.newRaw, &r.newValue}} {
			if len(v.raw) > 0 {
				_ = json.Unmarshal(v.raw, v.out)
			}
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// fieldRows counts the update rows of a trail per field.
func fieldRows(trail []trailRow) map[string]int {
	n := map[string]int{}
	for i := range trail {
		if trail[i].changeType == "update" {
			n[trail[i].field]++
		}
	}
	return n
}

// TestAssetValueHistory covers WP-794 (AUD-08, SEC-05, TEN-02, TEN-05,
// RBA-07) against PostgreSQL: the trigger asset_history writes the trail of
// the typed columns for every writer (asset repository, stock movement,
// rack placement, lifecycle transition), one row per field and change with
// the actor and the version; the repository adds only the custom fields,
// whose personal keys are surrogates; the delete row carries the previous
// row without custom fields; entity_change carries the client and site of
// the asset and follows their change; a client-1 or site-1 principal
// neither reads nor writes the trail of an asset outside its scope; the
// history of an asset leaves out the fields the principal may not read;
// the typed columns of asset hold no personal key.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssetValueHistory(t *testing.T) {
	f := scopetest.Seed(t, "2b")
	bg := context.Background()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	pii.SetDefault(pii.NewVault(f.App, enc))
	t.Cleanup(func() { pii.SetDefault(nil) })

	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := asset.NewPGRepository(f.App)
	locs := locations.NewPGRepository(f.App)
	newLocation := func(req locations.CreateRequest) string {
		t.Helper()
		loc, lerr := locs.Create(ctx, f.OrgA, req)
		if lerr != nil {
			t.Fatalf("create %s %s: %v", req.Kind, req.Name, lerr)
		}
		return loc.ID
	}
	site1 := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp794-s1"})
	building := newLocation(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site1, Name: "wp794-b1"})
	room := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building, Name: "wp794-r1"})
	rack1 := newLocation(locations.CreateRequest{Kind: locations.KindRack, ParentID: room, Name: "wp794-k1"})
	site2 := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp794-s2"})

	// Create through the repository: one create row with the whole row
	// from the trigger, one with the sealed custom fields.
	ciID := f.CI(t, f.OrgA, f.Client1, "wp794-ci")
	a := &asset.Asset{OrganizationID: f.OrgA, ClientID: f.Client1, CIID: ciID, AssetTag: "WP794-1", Name: "Laptop",
		Category: "hardware", Status: "in_stock", SerialNumber: "SN-794", PurchaseCost: "1299.5",
		CustomFields: map[string]any{"email": "wp794-owner@example.invalid", "color": "red"}}
	if err = repo.Create(ctx, a); err != nil {
		t.Fatalf("create: %v", err)
	}
	trail := assetTrail(t, f, a.ID)
	if len(trail) != 2 || trail[0].changeType != "create" || trail[1].changeType != "create" {
		t.Fatalf("trail of the creation: %+v, want two create rows", trail)
	}
	var whole, custom trailRow
	for _, r := range trail {
		if r.field == "" {
			whole = r
		} else {
			custom = r
		}
	}
	if whole.newValue["inventory_no"] != "WP794-1" || whole.newValue["primary_ci_id"] != ciID || whole.newValue["purchase_price"] != "1299.50" ||
		whole.newValue["lifecycle_state"] == nil || whole.oldValue != nil {
		t.Errorf("create row: %s", whole.newRaw)
	}
	for _, left := range []string{"custom_fields", "created_at", "updated_at", "version", "asset_tag", "purchase_cost", "warranty_end", "supplier", "serial_number_norm", "ci_id"} {
		if _, ok := whole.newValue[left]; ok {
			t.Errorf("create row carries %s: %s", left, whole.newRaw)
		}
	}
	if custom.field != "custom_fields" || strings.Contains(string(custom.newRaw), "wp794-owner@example.invalid") {
		t.Fatalf("custom fields of the creation: %q %s, want the field custom_fields without the plain e-mail", custom.field, custom.newRaw)
	}
	cf, _ := custom.newValue["custom_fields"].(map[string]any)
	if ref, _ := cf["email"].(map[string]any); ref[pii.RefKey] == nil || cf["color"] != "red" {
		t.Errorf("custom fields of the creation: %s, want email as surrogate (SEC-05)", custom.newRaw)
	}
	for _, r := range trail {
		if r.actor != f.User || r.client != f.Client1 || r.site != "" || r.version == nil || *r.version != 1 {
			t.Errorf("creation row %q: actor %q client %q site %q version %v", r.field, r.actor, r.client, r.site, r.version)
		}
	}

	// PATCH through the repository: one row per changed field, the custom
	// fields from the repository, nothing twice.
	name, notes := "Laptop 2", "patched"
	if _, err = repo.Update(ctx, f.OrgA, a.ID, &asset.UpdateRequest{Name: &name, Notes: &notes,
		CustomFields: map[string]any{"color": "blue"}}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if n := fieldRows(assetTrail(t, f, a.ID)); len(n) != 3 || n["name"] != 1 || n["notes"] != 1 || n["custom_fields"] != 1 {
		t.Fatalf("update rows after the PATCH: %v, want name, notes and custom_fields once", n)
	}

	// Stock movement into the room of site 1, rack placement, lifecycle
	// transition: writers outside the asset package.
	moves := movement.NewPGRepository(f.App)
	if err = moves.Record(ctx, &movement.Movement{OrganizationID: f.OrgA, ItemKind: "asset", AssetID: a.ID,
		MovementType: "transfer", ToLocationID: room}); err != nil {
		t.Fatalf("movement: %v", err)
	}
	if err = rack.NewPGRepository(f.App).CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rack1, CIID: ciID,
		PositionU: 1, HeightU: 1, Face: "front"}); err != nil {
		t.Fatalf("rack mount: %v", err)
	}
	service := lifecycle.NewService(lifecycle.NewPGRepository(f.App), lifecycle.NewPGStateStore(f.App),
		lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App)))
	if _, err = service.Transition(ctx, f.OrgA, "asset", a.ID, "00000000-0000-0000-0000-000000000101",
		lifecycle.TransitionRequest{ToState: "received", Reason: "wp794"}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	trail = assetTrail(t, f, a.ID)
	n := fieldRows(trail)
	if n["location_id"] != 2 || n["site_id"] != 1 || n["lifecycle_state"] != 0 || n["name"] != 1 {
		t.Errorf("update rows after movement, placement and transition: %v, want location_id twice, site_id once, no lifecycle_state", n)
	}
	transitions := 0
	for _, r := range trail {
		switch {
		case r.changeType == "lifecycle_transition":
			transitions++
			if r.field != "lifecycle_state" || r.client != f.Client1 || r.site == "" {
				t.Errorf("lifecycle row: field %q client %q site %q", r.field, r.client, r.site)
			}
		case r.actor != f.User:
			t.Errorf("row %s %q without the acting user: %q", r.changeType, r.field, r.actor)
		case r.version == nil:
			t.Errorf("row %s %q without entity_version", r.changeType, r.field)
		}
	}
	if transitions != 1 {
		t.Errorf("lifecycle_transition rows: %d, want 1", transitions)
	}
	var lastLocation trailRow
	for _, r := range trail {
		if r.field == "location_id" {
			lastLocation = r
		}
	}
	if string(lastLocation.oldRaw) != `"`+room+`"` || string(lastLocation.newRaw) != `"`+rack1+`"` {
		t.Errorf("location row of the placement: %s → %s, want the room and the rack", lastLocation.oldRaw, lastLocation.newRaw)
	}

	// A site-1 principal reads the trail while the asset is at site 1.
	var site1ID string
	if err = f.Admin.QueryRow(bg, `SELECT site_id::text FROM asset WHERE id = $1`, a.ID).Scan(&site1ID); err != nil {
		t.Fatal(err)
	}
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(site1ID)
	siteCtx := database.ContextWithTenantScope(bg, &siteScope)
	hist := history.NewPGRepository(f.App)
	page := api.PaginationParams{Limit: 200}
	if _, total, lerr := hist.List(siteCtx, f.OrgA, "asset", a.ID, page, history.ListOptions{}); lerr != nil || total != len(trail) {
		t.Fatalf("site-1 trail of the asset at site 1: %d rows (%v), want %d", total, lerr, len(trail))
	}

	// A move to site 2 re-scopes every row of the trail.
	if err = moves.Record(ctx, &movement.Movement{OrganizationID: f.OrgA, ItemKind: "asset", AssetID: a.ID,
		MovementType: "transfer", ToLocationID: site2}); err == nil {
		var site2ID string
		if err = f.Admin.QueryRow(bg, `SELECT site_id::text FROM asset WHERE id = $1`, a.ID).Scan(&site2ID); err != nil {
			t.Fatal(err)
		}
		for _, r := range assetTrail(t, f, a.ID) {
			if r.site != site2ID || r.client != f.Client1 {
				t.Errorf("row %s %q after the move to site 2: client %q site %q", r.changeType, r.field, r.client, r.site)
			}
		}
	} else {
		t.Fatalf("movement to site 2: %v", err)
	}
	if _, total, lerr := hist.List(siteCtx, f.OrgA, "asset", a.ID, page, history.ListOptions{}); lerr != nil || total != 0 {
		t.Errorf("site-1 trail of the asset at site 2: %d rows (%v), want none", total, lerr)
	}
	if rows := scopedCount(siteCtx, t, f, a.ID); rows != 0 {
		t.Errorf("site-1 principal reads %d entity_change rows of the asset at site 2, want none (RLS)", rows)
	}

	// Client scope: a client-2 asset is neither read nor extended by a
	// client-1 principal; a client change re-scopes the trail.
	other := &asset.Asset{OrganizationID: f.OrgA, ClientID: f.Client2, AssetTag: "WP794-2", Name: "Client 2", Category: "hardware", Status: "in_stock"}
	if err = repo.Create(ctx, other); err != nil {
		t.Fatalf("create client-2 asset: %v", err)
	}
	clientCtx := f.ClientCtx(f.Client1)
	if _, total, lerr := hist.List(clientCtx, f.OrgA, "asset", other.ID, page, history.ListOptions{}); lerr != nil || total != 0 {
		t.Errorf("client-1 trail of a client-2 asset: %d rows (%v), want none", total, lerr)
	}
	if rows := scopedCount(clientCtx, t, f, other.ID); rows != 0 {
		t.Errorf("client-1 principal reads %d entity_change rows of a client-2 asset, want none (RLS)", rows)
	}
	if err = hist.Record(clientCtx, &history.Change{OrganizationID: f.OrgA, EntityType: "asset", EntityID: other.ID, ChangeType: "comment"}); err == nil {
		t.Error("client-1 principal extended the trail of a client-2 asset through the history repository")
	}
	for _, scope := range []string{"NULL", "'" + f.Client1 + "'"} {
		werr := database.WithRequestTenant(clientCtx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			_, xerr := tx.Exec(ctx, `INSERT INTO entity_change (organization_id, entity_type, entity_id, change_type, client_id)
				VALUES ($1, 'asset', $2, 'comment', `+scope+`)`, f.OrgA, other.ID)
			return xerr
		})
		if werr == nil {
			t.Errorf("client-1 principal wrote a row of a client-2 asset with client %s", scope)
		}
	}
	if _, err = f.Admin.Exec(bg, `UPDATE asset SET client_id = $2 WHERE id = $1`, other.ID, f.Client1); err != nil {
		t.Fatal(err)
	}
	otherTrail := assetTrail(t, f, other.ID)
	for _, r := range otherTrail {
		if r.client != f.Client1 {
			t.Errorf("row %s %q after the client change: client %q, want client 1", r.changeType, r.field, r.client)
		}
	}
	if n := fieldRows(otherTrail); n["client_id"] != 1 {
		t.Errorf("update rows of the client change: %v, want client_id once", n)
	}
	if _, total, lerr := hist.List(clientCtx, f.OrgA, "asset", other.ID, page, history.ListOptions{}); lerr != nil || total != len(otherTrail) {
		t.Errorf("client-1 trail after the client change: %d rows (%v), want %d", total, lerr, len(otherTrail))
	}

	// Delete: the row carries the previous row without custom fields.
	doomed := &asset.Asset{OrganizationID: f.OrgA, ClientID: f.Client1, AssetTag: "WP794-3", Name: "Doomed", Category: "hardware",
		Status: "in_stock", Notes: "last words", CustomFields: map[string]any{"color": "green"}}
	if err = repo.Create(ctx, doomed); err != nil {
		t.Fatalf("create doomed: %v", err)
	}
	if err = repo.Delete(ctx, f.OrgA, doomed.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	var deletes []trailRow
	for _, r := range assetTrail(t, f, doomed.ID) {
		if r.changeType == "delete" {
			deletes = append(deletes, r)
		}
	}
	if len(deletes) != 1 || deletes[0].newRaw != nil || deletes[0].oldValue["notes"] != "last words" || deletes[0].oldValue["inventory_no"] != "WP794-3" ||
		deletes[0].actor != f.User || deletes[0].client != f.Client1 {
		t.Fatalf("delete rows: %+v", deletes)
	}
	if _, ok := deletes[0].oldValue["custom_fields"]; ok {
		t.Errorf("delete row carries the custom fields: %s", deletes[0].oldRaw)
	}

	// Field permissions: the history leaves out serial_number and the
	// custom field color for a principal that may not read them.
	mem := permission.NewMemoryFieldGrants()
	role := "00000000-0000-4000-8000-000000000794"
	for _, fk := range []string{"core.serial_number", "attributes.color"} {
		if _, gerr := mem.CreateFieldGrant(bg, f.OrgA, &permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindAsset,
			FieldKey: &fk, Level: permission.LevelNone}); gerr != nil {
			t.Fatal(gerr)
		}
	}
	policy, err := permission.NewFieldResolver(nil, nil, mem, nil).PolicyForGrants(bg, f.OrgA,
		[]identity.Grant{{RoleID: role, Permissions: []identity.Permission{identity.PermAssetRead}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Update(ctx, f.OrgA, a.ID, &asset.UpdateRequest{SerialNumber: ptr("SN-794-B")}); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	history.NewHandler(hist).RegisterRoutes(mux)
	get := func(p *permission.FieldPolicy, path string) []byte {
		t.Helper()
		reqCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		reqCtx = identity.WithPrincipal(reqCtx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA,
			Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()})
		reqCtx = permission.WithPolicy(reqCtx, p)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, http.NoBody).WithContext(reqCtx))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		return rec.Body.Bytes()
	}
	historyPath := "/api/v1/history/asset/" + a.ID + "?limit=200"
	statePath := "/api/v1/assets/" + a.ID + "/state?at=" + time.Now().Add(time.Minute).UTC().Format(time.RFC3339)
	full := string(get(permission.UnrestrictedPolicy(identity.AllPermissions()), historyPath))
	if !strings.Contains(full, "SN-794-B") || !strings.Contains(full, `"blue"`) {
		t.Fatalf("unrestricted history lacks the serial number or the color: %s", full)
	}
	for _, path := range []string{historyPath, statePath} {
		body := string(get(policy, path))
		for _, secret := range []string{"SN-794", `"serial_number"`, `"red"`, `"blue"`} {
			if strings.Contains(body, secret) {
				t.Errorf("GET %s under the restricted policy leaks %s: %s", path, secret, body)
			}
		}
		if !strings.Contains(body, "Laptop 2") {
			t.Errorf("GET %s under the restricted policy lacks the readable name: %s", path, body)
		}
	}

	// The typed columns of asset hold no personal key: the trigger writes
	// them without pii.SealFieldTx.
	cols, err := f.Admin.Query(bg, `SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'asset'`)
	if err != nil {
		t.Fatal(err)
	}
	columns, err := pgx.CollectRows(cols, pgx.RowTo[string])
	if err != nil || len(columns) == 0 {
		t.Fatalf("asset columns: %v (%v)", columns, err)
	}
	personal := map[string]bool{}
	for _, k := range pii.PersonalKeys() {
		personal[k] = true
	}
	for _, c := range columns {
		if personal[c] || pii.IsPersonalKey(c) {
			t.Errorf("asset column %s is a personal key: the trigger asset_history would write it unsealed", c)
		}
	}
}

// scopedCount counts the entity_change rows of an entity under the tenant
// scope of ctx.
func scopedCount(ctx context.Context, t *testing.T, f *scopetest.Fixture, id string) int {
	t.Helper()
	var n int
	if err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM entity_change WHERE entity_id = $1`, id).Scan(&n)
	}); err != nil {
		t.Fatal(err)
	}
	return n
}
