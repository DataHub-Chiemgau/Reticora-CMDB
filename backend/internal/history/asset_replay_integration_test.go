package history_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// assetAsOf is the answer of GET /assets/{id}/as-of.
type assetAsOf struct {
	EntityType        string         `json:"entity_type"`
	EntityID          string         `json:"entity_id"`
	At                time.Time      `json:"at"`
	BasedOnSnapshotAt *time.Time     `json:"based_on_snapshot_at"`
	Fields            map[string]any `json:"fields"`
	Deleted           bool           `json:"deleted"`
	NotHistorized     []string       `json:"not_historized"`
}

// customFields are the custom fields of the state.
func (s *assetAsOf) customFields() map[string]any {
	cf, _ := s.Fields["custom_fields"].(map[string]any)
	return cf
}

// TestAssetAsOfAgainstPostgres covers AUD-08 (WP-795) against PostgreSQL
// through the production router (server.NewRouter) behind the deprecation
// headers of api/openapi.yaml. An asset is created and changed through
// the asset repository, a stock movement and a lifecycle transition; the
// weekly job history.weekly_snapshot writes its asset_snapshot (only with
// the module inventory: without WithAssets it writes none), a second run
// without a change writes none, and GET /assets/{id}/as-of answers for
// every T the state of then, from the snapshot and the entity_change rows
// after it. An asset deleted for good answers its last state with deleted;
// a client-1 principal on a client-2 asset and a site-1 principal on an
// asset at site 2 get 404. After the deletion of the vault entry of a
// personal custom field the field is null. retention.history keeps the
// anchor snapshot and the rows after it, so as-of shortly after the
// cut-off is complete. GET /assets/{id}/state answers the same with the
// headers Deprecation, Sunset and Link.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssetAsOfAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "c7")
	bg := context.Background()
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	vault := pii.NewVault(f.App, encryptor)
	pii.SetDefault(vault)
	t.Cleanup(func() { pii.SetDefault(nil) })

	userAdmin := f.AppUser(t, f.OrgA, "wp795-admin")
	hfrExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userAdmin)
	admin := hfrPrincipal(f.OrgA, userAdmin)
	admin.Permissions = identity.AllPermissions()

	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-795", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	validator := middleware.NewOpenAPIValidator()
	if loaded, verr := validator.CheckSpec(true); !loaded || verr != nil {
		t.Fatalf("api/openapi.yaml not loaded: %v", verr)
	}
	router := middleware.RequestID(middleware.TenantMiddleware(validator.DeprecationHeaders(mux)))

	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userAdmin})
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
	site1 := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp795-s1"})
	building1 := newLocation(locations.CreateRequest{Kind: locations.KindBuilding, ParentID: site1, Name: "wp795-b1"})
	room1 := newLocation(locations.CreateRequest{Kind: locations.KindRoom, ParentID: building1, Name: "wp795-r1"})
	site2 := newLocation(locations.CreateRequest{Kind: locations.KindSite, ClientID: f.Client1, Name: "wp795-s2"})
	newAsset := func(a *asset.Asset) string {
		t.Helper()
		a.OrganizationID, a.Category, a.Status = f.OrgA, "hardware", "in_stock"
		if cerr := repo.Create(ctx, a); cerr != nil {
			t.Fatalf("create asset %s: %v", a.AssetTag, cerr)
		}
		return a.ID
	}
	rename := func(id, name string) {
		t.Helper()
		if _, uerr := repo.Update(ctx, f.OrgA, id, &asset.UpdateRequest{Name: &name}); uerr != nil {
			t.Fatalf("rename asset %s: %v", id, uerr)
		}
	}
	clock := func() time.Time {
		t.Helper()
		time.Sleep(20 * time.Millisecond)
		var now time.Time
		if qerr := f.Admin.QueryRow(bg, `SELECT clock_timestamp()`).Scan(&now); qerr != nil {
			t.Fatal(qerr)
		}
		time.Sleep(20 * time.Millisecond)
		return now
	}
	count := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if qerr := f.Admin.QueryRow(bg, sql, args...).Scan(&n); qerr != nil {
			t.Fatal(qerr)
		}
		return n
	}
	const ownerEmail = "wp795-owner@example.invalid"

	// The asset: created through the repository with a personal custom
	// field, renamed, moved into room 1 of site 1, transitioned.
	a := newAsset(&asset.Asset{ClientID: f.Client1, AssetTag: "WP795-A", Name: "Laptop",
		CustomFields: map[string]any{"email": ownerEmail, "color": "red"}})
	mid := clock()
	rename(a, "Laptop 2")
	if err = movement.NewPGRepository(f.App).Record(ctx, &movement.Movement{OrganizationID: f.OrgA, ItemKind: "asset", AssetID: a,
		MovementType: "transfer", ToLocationID: room1}); err != nil {
		t.Fatalf("movement: %v", err)
	}
	transitions := lifecycle.NewService(lifecycle.NewPGRepository(f.App), lifecycle.NewPGStateStore(f.App),
		lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App)))
	if _, err = transitions.Transition(ctx, f.OrgA, "asset", a, "00000000-0000-0000-0000-000000000101",
		lifecycle.TransitionRequest{ToState: "received", Reason: "wp795"}); err != nil {
		t.Fatalf("transition: %v", err)
	}
	// A client-2 asset, a client-1 asset at site 2, the asset deleted
	// later and one without a recorded creation (its trail removed).
	foreign := newAsset(&asset.Asset{ClientID: f.Client2, AssetTag: "WP795-C2", Name: "Client 2"})
	atSite2 := newAsset(&asset.Asset{ClientID: f.Client1, AssetTag: "WP795-S2", Name: "Site 2", LocationID: site2})
	gone := newAsset(&asset.Asset{ClientID: f.Client1, AssetTag: "WP795-GONE", Name: "Gone"})
	rename(gone, "Gone 2")
	legacy := f.Asset(t, f.OrgA, f.Client1, "WP795-LEGACY")
	hfrExec(t, f, `DELETE FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1`, legacy)
	t0 := clock()

	// Without the module inventory the weekly job writes no asset_snapshot.
	ahead := func() time.Time { return time.Now().Add(time.Hour) }
	if _, err = history.NewWeeklySnapshot(f.App).WithClock(ahead).RunOrg(bg, f.OrgA); err != nil {
		t.Fatalf("weekly snapshot without inventory: %v", err)
	}
	if n := count(`SELECT count(*) FROM asset_snapshot WHERE organization_id = $1`, f.OrgA); n != 0 {
		t.Fatalf("weekly snapshot without the module inventory wrote %d asset snapshots", n)
	}
	weekly := func() {
		t.Helper()
		if _, werr := history.NewWeeklySnapshot(f.App).WithAssets(true).WithClock(ahead).RunOrg(bg, f.OrgA); werr != nil {
			t.Fatalf("weekly snapshot: %v", werr)
		}
	}
	weekly()
	var takenAt time.Time
	var reason, snapClient, snapSite, stored string
	if err = f.Admin.QueryRow(bg, `SELECT taken_at, reason, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), state::text
		FROM asset_snapshot WHERE asset_id = $1`, a).Scan(&takenAt, &reason, &snapClient, &snapSite, &stored); err != nil {
		t.Fatalf("weekly snapshot of the asset: %v", err)
	}
	if reason != "weekly" || snapClient != f.Client1 || snapSite != site1 {
		t.Errorf("asset snapshot: reason %q client %q site %q, want weekly, %s, %s", reason, snapClient, snapSite, f.Client1, site1)
	}
	hfrHidden(t, "asset_snapshot.state", []byte(stored), ownerEmail)
	if err = f.Admin.QueryRow(bg, `SELECT reason FROM asset_snapshot WHERE asset_id = $1`, legacy).Scan(&reason); err != nil || reason != "initial" {
		t.Errorf("snapshot of the asset without a recorded creation: %q %v, want initial", reason, err)
	}
	// A second run without a change writes none.
	snapshots := count(`SELECT count(*) FROM asset_snapshot WHERE organization_id = $1`, f.OrgA)
	weekly()
	if n := count(`SELECT count(*) FROM asset_snapshot WHERE organization_id = $1`, f.OrgA); n != snapshots {
		t.Errorf("second run without a change: %d asset snapshots, want %d", n, snapshots)
	}

	// After the snapshot: renamed again.
	rename(a, "Laptop 3")
	t1 := clock()

	asOf := func(p *identity.Principal, path string, at *time.Time) (int, []byte, http.Header) {
		t.Helper()
		if at != nil {
			path += "?at=" + at.UTC().Format(time.RFC3339Nano)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		reqCtx := identity.WithPermissions(identity.WithPrincipal(req.Context(), *p), p.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(reqCtx))
		return w.Code, w.Body.Bytes(), w.Header()
	}
	state := func(p *identity.Principal, id string, at *time.Time) *assetAsOf {
		t.Helper()
		code, body, _ := asOf(p, "/api/v1/assets/"+id+"/as-of", at)
		if code != http.StatusOK {
			t.Fatalf("GET as-of of %s at %v: %d %s", id, at, code, body)
		}
		var s assetAsOf
		if uerr := json.Unmarshal(body, &s); uerr != nil {
			t.Fatal(uerr)
		}
		return &s
	}

	t.Run("state at T", func(t *testing.T) {
		for _, c := range []struct {
			label, name, location, lifecycle string
			at                               time.Time
			base                             bool
		}{
			{"after the creation", "Laptop", "", "", mid, false},
			{"before the snapshot (whole trail)", "Laptop 2", room1, "received", t0, false},
			{"after the snapshot (snapshot and replay)", "Laptop 3", room1, "received", t1, true},
		} {
			s := state(&admin, a, &c.at)
			if s.Fields["name"] != c.name || s.Fields["inventory_no"] != "WP795-A" || s.Deleted {
				t.Errorf("%s: %v deleted %v, want name %s", c.label, s.Fields, s.Deleted, c.name)
			}
			if c.location != "" && (s.Fields["location_id"] != c.location || s.Fields["site_id"] != site1) {
				t.Errorf("%s: location %v site %v, want %s at %s", c.label, s.Fields["location_id"], s.Fields["site_id"], c.location, site1)
			}
			if c.lifecycle != "" && s.Fields["lifecycle_state"] != c.lifecycle {
				t.Errorf("%s: lifecycle_state %v, want %s", c.label, s.Fields["lifecycle_state"], c.lifecycle)
			}
			if cf := s.customFields(); cf["email"] != ownerEmail || cf["color"] != "red" {
				t.Errorf("%s: custom fields %v, want the personal value revealed", c.label, cf)
			}
			if c.base != (s.BasedOnSnapshotAt != nil) || c.base && !s.BasedOnSnapshotAt.Equal(takenAt) {
				t.Errorf("%s: based on snapshot %v, want %v (taken %s)", c.label, s.BasedOnSnapshotAt, c.base, takenAt)
			}
			if s.EntityType != "asset" || s.EntityID != a || !s.At.Equal(c.at) || !slices.Equal(s.NotHistorized, history.NotHistorizedAssetFields) {
				t.Errorf("%s: %s %s at %s not_historized %v", c.label, s.EntityType, s.EntityID, s.At, s.NotHistorized)
			}
		}
		if s := state(&admin, legacy, nil); s.Fields["name"] != "WP795-LEGACY" || s.BasedOnSnapshotAt == nil {
			t.Errorf("asset without a recorded creation: %+v, want its initial snapshot", s)
		}
		before := mid.Add(-time.Hour)
		if code, body, _ := asOf(&admin, "/api/v1/assets/"+a+"/as-of", &before); code != http.StatusNotFound {
			t.Errorf("as-of before the creation: %d %s, want 404", code, body)
		}
	})

	t.Run("scope", func(t *testing.T) {
		client1 := hfrPrincipal(f.OrgA, userAdmin, "asset:read")
		client1.Scope = &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: identity.ScopeSet{All: true},
			Teams: identity.ScopeSet{All: true}}
		site1Principal := hfrPrincipal(f.OrgA, userAdmin, "asset:read")
		site1Principal.Scope = &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{IDs: []string{site1}},
			Teams: identity.ScopeSet{All: true}}
		for _, c := range []struct {
			label string
			p     *identity.Principal
			id    string
		}{
			{"client-1 principal on the client-2 asset", &client1, foreign},
			{"site-1 principal on the asset at site 2", &site1Principal, atSite2},
			{"client-1 principal on an unknown id", &client1, f.ID()},
		} {
			if code, body, _ := asOf(c.p, "/api/v1/assets/"+c.id+"/as-of", nil); code != http.StatusNotFound {
				t.Errorf("%s: %d %s, want 404", c.label, code, body)
			}
		}
		for _, p := range []*identity.Principal{&client1, &site1Principal} {
			if s := state(p, a, &t1); s.Fields["name"] != "Laptop 3" {
				t.Errorf("scoped principal on its asset: %v", s.Fields)
			}
		}
		noRead := hfrPrincipal(f.OrgA, userAdmin, "ci:read")
		if code, body, _ := asOf(&noRead, "/api/v1/assets/"+a+"/as-of", nil); code != http.StatusForbidden {
			t.Errorf("as-of without asset:read: %d %s, want 403", code, body)
		}
	})

	t.Run("alias state", func(t *testing.T) {
		code, asOfBody, asOfHeader := asOf(&admin, "/api/v1/assets/"+a+"/as-of", &t1)
		stateCode, stateBody, stateHeader := asOf(&admin, "/api/v1/assets/"+a+"/state", &t1)
		if code != http.StatusOK || stateCode != http.StatusOK || !bytes.Equal(asOfBody, stateBody) {
			t.Errorf("/state answers %d %s, /as-of %d %s; want the same", stateCode, stateBody, code, asOfBody)
		}
		if asOfHeader.Get("Deprecation") != "" {
			t.Errorf("/as-of is marked deprecated: %v", asOfHeader)
		}
		sunset, perr := http.ParseTime(stateHeader.Get("Sunset"))
		if stateHeader.Get("Deprecation") == "" || perr != nil || sunset.Before(time.Now().AddDate(0, 6, 0)) {
			t.Errorf("/state headers: Deprecation %q Sunset %q (%v)", stateHeader.Get("Deprecation"), stateHeader.Get("Sunset"), perr)
		}
		if got, want := stateHeader.Get("Link"), `</api/v1/assets/`+a+`/as-of>; rel="successor-version"`; got != want {
			t.Errorf("/state Link %q, want %q", got, want)
		}
	})

	t.Run("deleted for good", func(t *testing.T) {
		if derr := repo.Delete(ctx, f.OrgA, gone); derr != nil {
			t.Fatalf("delete: %v", derr)
		}
		s := state(&admin, gone, nil)
		if !s.Deleted || s.Fields["name"] != "Gone 2" || s.Fields["inventory_no"] != "WP795-GONE" || s.BasedOnSnapshotAt == nil {
			t.Errorf("as-of of the deleted asset: %+v, want its last state with deleted", s)
		}
		client1 := hfrPrincipal(f.OrgA, userAdmin, "asset:read")
		client1.Scope = &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: identity.ScopeSet{All: true},
			Teams: identity.ScopeSet{All: true}}
		if s := state(&client1, gone, nil); !s.Deleted {
			t.Errorf("client-1 principal on its deleted asset: %+v", s)
		}
		client2 := hfrPrincipal(f.OrgA, userAdmin, "asset:read")
		client2.Scope = &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client2}}, Sites: identity.ScopeSet{All: true},
			Teams: identity.ScopeSet{All: true}}
		if code, body, _ := asOf(&client2, "/api/v1/assets/"+gone+"/as-of", nil); code != http.StatusNotFound {
			t.Errorf("client-2 principal on the deleted client-1 asset: %d %s, want 404", code, body)
		}
	})

	t.Run("deleted vault entry", func(t *testing.T) {
		rows, qerr := f.Admin.Query(bg, `SELECT DISTINCT v FROM (
			SELECT new_value->'custom_fields'->'email'->>'$pii' AS v FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1
			UNION SELECT new_value->'email'->>'$pii' FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1
			UNION SELECT state->'fields'->'custom_fields'->'email'->>'$pii' FROM asset_snapshot WHERE asset_id = $1) s WHERE v IS NOT NULL`, a)
		if qerr != nil {
			t.Fatal(qerr)
		}
		subjects, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil || len(subjects) == 0 {
			t.Fatalf("surrogates of the personal custom field: %v %v", subjects, qerr)
		}
		for _, id := range subjects {
			if derr := vault.Delete(ctx, f.OrgA, id); derr != nil {
				t.Fatal(derr)
			}
		}
		for _, at := range []*time.Time{&t0, &t1} {
			cf := state(&admin, a, at).customFields()
			if v, has := cf["email"]; !has || v != nil || cf["color"] != "red" {
				t.Errorf("as-of at %s after the deletion of the vault entry: custom fields %v, want email null", at, cf)
			}
		}
	})

	t.Run("former field names", func(t *testing.T) {
		// The trail of an asset deleted before WP-794, recorded under the
		// former names (WP-516): the replay names the fields as the DTO.
		old := f.ID()
		hfrExec(t, f, `INSERT INTO entity_change (organization_id, entity_type, entity_id, change_type, field_name, old_value, new_value, created_at)
			VALUES ($1, 'asset', $2, 'create', NULL, NULL, '{"asset_tag": "WP795-OLD", "name": "Old", "purchase_cost": 12.5, "ci_id": null}', now() - interval '3 hours'),
			       ($1, 'asset', $2, 'update', 'warranty_end', NULL, '"2027-01-31"', now() - interval '2 hours'),
			       ($1, 'asset', $2, 'update', 'purchase_cost', '12.5', '20', now() - interval '2 hours'),
			       ($1, 'asset', $2, 'delete', NULL, '{"asset_tag": "WP795-OLD", "name": "Old"}', NULL, now() - interval '1 hour')`, f.OrgA, old)
		s := state(&admin, old, nil)
		if !s.Deleted || s.Fields["inventory_no"] != "WP795-OLD" || s.Fields["purchase_price"] != "20.00" || s.Fields["warranty_until"] != "2027-01-31" {
			t.Errorf("as-of of an asset with former field names: %+v", s)
		}
		for _, former := range []string{"asset_tag", "purchase_cost", "warranty_end", "ci_id"} {
			if _, has := s.Fields[former]; has {
				t.Errorf("as-of carries the former field %s: %v", former, s.Fields)
			}
		}
	})

	t.Run("retention keeps the anchor", func(t *testing.T) {
		// An asset whose trail and snapshot are moved 3000 days back: the
		// creation and a rename before the anchor, a rename after it, an
		// older snapshot before the anchor.
		kept := newAsset(&asset.Asset{ClientID: f.Client1, AssetTag: "WP795-R", Name: "R0", Notes: "n0"})
		rename(kept, "R1")
		weekly()
		rename(kept, "R2")
		hfrExec(t, f, `UPDATE entity_change SET created_at = created_at - interval '3000 days' WHERE entity_type = 'asset' AND entity_id = $1`, kept)
		hfrExec(t, f, `UPDATE asset_snapshot SET taken_at = taken_at - interval '3000 days' WHERE asset_id = $1`, kept)
		var anchorID string
		var anchorAt time.Time
		if qerr := f.Admin.QueryRow(bg, `SELECT id::text, taken_at FROM asset_snapshot WHERE asset_id = $1`, kept).Scan(&anchorID, &anchorAt); qerr != nil {
			t.Fatalf("anchor snapshot: %v", qerr)
		}
		hfrExec(t, f, `INSERT INTO asset_snapshot (organization_id, asset_id, taken_at, reason, state, schema_version)
			VALUES ($1, $2, $3, 'weekly', '{"fields": {"name": "old"}, "deleted": false}', 1)`, f.OrgA, kept, anchorAt.Add(-24*time.Hour))
		after := count(`SELECT count(*) FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1 AND created_at > $2`, kept, anchorAt)
		if after == 0 {
			t.Fatal("no row after the anchor")
		}

		runAt := time.Now().UTC()
		cutoff := runAt.Add(-2999 * 24 * time.Hour)
		r := jobs.NewRetention(f.App, jobs.RetentionFromDays(10, 2999, 90, 365)).WithClock(func() time.Time { return runAt })
		if rerr := r.RunHistory(bg); rerr != nil {
			t.Fatal(rerr)
		}
		var snapshotIDs []string
		if qerr := f.Admin.QueryRow(bg, `SELECT COALESCE(array_agg(id::text), '{}') FROM asset_snapshot WHERE asset_id = $1`, kept).
			Scan(&snapshotIDs); qerr != nil {
			t.Fatal(qerr)
		}
		if !slices.Equal(snapshotIDs, []string{anchorID}) {
			t.Errorf("asset snapshots after the run: %v, want the anchor %s", snapshotIDs, anchorID)
		}
		if n := count(`SELECT count(*) FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1 AND created_at <= $2`, kept, anchorAt); n != 0 {
			t.Errorf("%d rows up to the anchor remain, want none", n)
		}
		if n := count(`SELECT count(*) FROM entity_change WHERE entity_type = 'asset' AND entity_id = $1`, kept); n != after {
			t.Errorf("rows after the anchor: %d, want %d", n, after)
		}
		at := cutoff.Add(time.Hour)
		s := state(&admin, kept, &at)
		if s.Fields["name"] != "R2" || s.Fields["notes"] != "n0" || s.Fields["inventory_no"] != "WP795-R" ||
			s.BasedOnSnapshotAt == nil || !s.BasedOnSnapshotAt.Equal(anchorAt) {
			t.Errorf("as-of shortly after the cut-off: %v based on %v, want R2 with notes and inventory_no based on %s", s.Fields, s.BasedOnSnapshotAt, anchorAt)
		}
	})
}
