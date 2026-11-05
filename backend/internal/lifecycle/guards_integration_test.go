package lifecycle_test

// WP-279 (LCY-03): the system guards of the asset lifecycle.
//
//   - Each guard positive and negative: in_stock on a bin and on a room;
//     deployed at an operations location, with a user, with a team and
//     without both (422 with missing location_id, assignee); disposed with
//     and without disposal_record; reserved with and without an active
//     reservation.
//   - Scope: a location of another client and a disposal record of an
//     asset outside the scope count as missing (422, no 403/404); an
//     asset outside the scope is 404.
//   - Atomicity: disposal record and transition commit together in one
//     database.WithRequestTenantTx; a return that holds the asset lock
//     makes a parallel transition to deployed see the asset without
//     holder.
//   - An organization model with deployed on the asset type has the guard
//     as well; a CI model has none. Without a checker (memory) a guarded
//     transition fails closed.
//   - A violation leaves no state change, no event lifecycle.changed and
//     no movement. The problem body carries guard and missing.
//   - The migration 000263 left no required context fields at in_stock,
//     deployed and disposed of physical_asset.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run 'TestLifecycleGuards' ./internal/lifecycle/

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// guardProblem is the RFC 7807 body of a refused guarded transition.
type guardProblem struct {
	Type       string                     `json:"type"`
	Status     int                        `json:"status"`
	Detail     string                     `json:"detail"`
	Guard      string                     `json:"guard"`
	Missing    []string                   `json:"missing"`
	Field      string                     `json:"field"`
	Violations []lifecycle.GuardViolation `json:"violations"`
}

// guardTrail is what a transition leaves on an asset.
type guardTrail struct {
	state             string
	events, movements int
}

// TestLifecycleGuards runs the system guards against PostgreSQL.
func TestLifecycleGuards(t *testing.T) {
	f := scopetest.Seed(t, "18")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wp279-admin")
	ctxOf := func(clients ...string) context.Context {
		scope := database.OrgWideScope(f.OrgA, user)
		if len(clients) > 0 {
			scope.Clients = database.ScopeIDs(clients...)
		}
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
			tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp279", CorrelationID: "wp279", Source: audit.SourceUI})
	}
	orgA := ctxOf()
	client1 := ctxOf(f.Client1)
	admin := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}

	t.Run("migration 000263", func(t *testing.T) {
		got := admin(`SELECT string_agg(key || '=' || required_fields::text, ',' ORDER BY key) FROM lifecycle_state
			WHERE definition_id = '00000000-0000-0000-0000-000000000101' AND organization_id IS NULL
			  AND key IN ('in_stock', 'deployed', 'disposed')`)
		if got != "deployed=[],disposed=[],in_stock=[]" {
			t.Errorf("required fields of physical_asset: %s, want none", got)
		}
		for _, st := range lifecycle.DefaultPhysicalAssetLifecycle().States {
			if len(st.RequiredFields) > 0 {
				t.Errorf("memory seed %s requires %v", st.Key, st.RequiredFields)
			}
		}
	})

	repo := lifecycle.NewPGRepository(f.App)
	store := lifecycle.NewPGStateStore(f.App)
	guards := lifecycle.NewPGGuards(f.App)
	service := lifecycle.NewService(repo, store, lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App))).
		WithGuards(guards)
	h := newModelAPI(t, lifecycle.NewHandler(repo, service, lifecycle.NewResolver(f.App)))
	assignments := assignment.NewService(f.App, movement.NewService(movement.NewPGRepository(f.App)), events.NewOutbox())

	// Storage chain site → warehouse → zone → shelf → bin of client 1
	// through the locations repository (parent matrix of 000062).
	locs := locations.NewPGRepository(f.App)
	node := func(kind locations.Kind, parent, client, name string) string {
		t.Helper()
		n, err := locs.Create(orgA, f.OrgA, locations.CreateRequest{Kind: kind, ParentID: parent, ClientID: client, Name: name})
		if err != nil {
			t.Fatalf("create %s %s: %v", kind, name, err)
		}
		return n.ID
	}
	site := node(locations.KindSite, "", f.Client1, "wp279-site")
	warehouse := node(locations.KindWarehouse, site, "", "wp279-warehouse")
	bin := node(locations.KindBin, node(locations.KindShelf, node(locations.KindZone, warehouse, "", "wp279-zone"), "", "wp279-shelf"), "", "wp279-bin")
	room := f.Room(t, f.Client1, "wp279-room")
	foreignRoom := f.Room(t, f.Client2, "wp279-room-c2")
	team := admin(`INSERT INTO team (organization_id, name) VALUES ($1, 'wp279-team') RETURNING id::text`, f.OrgA)

	// assetIn creates an asset of client in state with the location.
	assetIn := func(tag, client, state, location string) string {
		t.Helper()
		id := f.Asset(t, f.OrgA, client, tag)
		var loc any
		if location != "" {
			loc = location
		}
		admin(`UPDATE asset SET lifecycle_state = $2, location_id = $3 WHERE id = $1 RETURNING id::text`, id, state, loc)
		return id
	}
	trailOf := func(id string) guardTrail {
		t.Helper()
		var tr guardTrail
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(lifecycle_state, ''),
			(SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'lifecycle.changed'),
			(SELECT count(*) FROM stock_movement WHERE asset_id = $1::uuid)
			FROM asset WHERE id = $1::uuid`, id).Scan(&tr.state, &tr.events, &tr.movements); err != nil {
			t.Fatalf("trail of %s: %v", id, err)
		}
		return tr
	}
	post := func(m *modelAPI, ctx context.Context, id, body string) (int, guardProblem) {
		m.t.Helper()
		w := m.call(ctx, http.MethodPost, "/api/v1/assets/"+id+"/lifecycle-transitions", body)
		var p guardProblem
		if w.Code >= 300 {
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				m.t.Fatalf("decode %s: %v", w.Body.String(), err)
			}
		}
		return w.Code, p
	}
	// passes posts a transition that must succeed.
	passes := func(t *testing.T, ctx context.Context, id, to string) {
		t.Helper()
		if code, p := post(h.with(t), ctx, id, `{"to_state":"`+to+`"}`); code != http.StatusOK {
			t.Fatalf("%s into %s: %d %+v, want 200", id, to, code, p)
		}
		if got := trailOf(id); got.state != to || got.events != 1 {
			t.Errorf("%s after %s: %+v, want the state and one event", id, to, got)
		}
	}
	// refused posts a transition that must fail guard with missing and
	// leave the asset unchanged.
	refused := func(t *testing.T, ctx context.Context, id, to, guard string, missing ...string) {
		t.Helper()
		before := trailOf(id)
		code, p := post(h.with(t), ctx, id, `{"to_state":"`+to+`"}`)
		if code != http.StatusUnprocessableEntity || p.Type != lifecycle.ProblemLifecycleGuard || p.Guard != guard ||
			!slices.Equal(p.Missing, missing) || len(p.Violations) != len(missing) {
			t.Fatalf("%s into %s: %d %+v, want 422 lifecycle-guard %s missing %v", id, to, code, p, guard, missing)
		}
		if after := trailOf(id); after != before {
			t.Errorf("%s after the refused transition: %+v, before %+v", id, after, before)
		}
	}

	t.Run("in_stock: storage location", func(t *testing.T) {
		passes(t, orgA, assetIn("wp279-bin", f.Client1, "received", bin), "in_stock")
		refused(t, orgA, assetIn("wp279-room", f.Client1, "received", room), "in_stock",
			lifecycle.GuardStorageLocation, "location_id")
		refused(t, orgA, assetIn("wp279-nowhere", f.Client1, "received", ""), "in_stock",
			lifecycle.GuardStorageLocation, "location_id")
	})

	t.Run("deployed: operations location or holder", func(t *testing.T) {
		passes(t, orgA, assetIn("wp279-dep-room", f.Client1, "in_stock", room), "deployed")
		passes(t, orgA, assetIn("wp279-dep-site", f.Client1, "in_stock", site), "deployed")

		withUser := assetIn("wp279-dep-user", f.Client1, "in_stock", bin)
		if _, err := assignments.Assign(orgA, f.OrgA, &assignment.AssignInput{AssetID: withUser,
			Holder: assignment.Holder{UserID: user}}); err != nil {
			t.Fatalf("assign: %v", err)
		}
		if code, p := post(h.with(t), orgA, withUser, `{"to_state":"deployed"}`); code != http.StatusOK {
			t.Fatalf("deployed with a user: %d %+v", code, p)
		}

		withTeam := assetIn("wp279-dep-team", f.Client1, "in_stock", "")
		admin(`UPDATE asset SET assigned_team_id = $2 WHERE id = $1 RETURNING id::text`, withTeam, team)
		passes(t, orgA, withTeam, "deployed")

		refused(t, orgA, assetIn("wp279-dep-none", f.Client1, "in_stock", bin), "deployed",
			lifecycle.GuardOperationsLocationOrHolder, "location_id", "assignee")
	})

	t.Run("disposed: disposal record", func(t *testing.T) {
		with := assetIn("wp279-disp", f.Client1, "retired", "")
		admin(`INSERT INTO disposal_record (organization_id, asset_id, method, performed_at)
			VALUES ($1, $2, 'recycling', now()) RETURNING id::text`, f.OrgA, with)
		passes(t, orgA, with, "disposed")
		refused(t, orgA, assetIn("wp279-disp-none", f.Client1, "retired", ""), "disposed",
			lifecycle.GuardDisposalRecord, "disposal_record")
	})

	t.Run("reserved: active reservation", func(t *testing.T) {
		with := assetIn("wp279-res", f.Client1, "in_stock", bin)
		admin(`INSERT INTO reservation (organization_id, item_kind, asset_id, state)
			VALUES ($1, 'asset', $2, 'active') RETURNING id::text`, f.OrgA, with)
		passes(t, orgA, with, "reserved")
		released := assetIn("wp279-res-released", f.Client1, "in_stock", bin)
		admin(`INSERT INTO reservation (organization_id, item_kind, asset_id, state)
			VALUES ($1, 'asset', $2, 'released') RETURNING id::text`, f.OrgA, released)
		refused(t, orgA, released, "reserved", lifecycle.GuardActiveReservation, "reservation")
	})

	t.Run("scope", func(t *testing.T) {
		// A location of client 2 is not visible to a principal of client 1.
		foreign := assetIn("wp279-foreign-loc", f.Client1, "in_stock", foreignRoom)
		refused(t, client1, foreign, "deployed", lifecycle.GuardOperationsLocationOrHolder, "location_id", "assignee")
		passes(t, orgA, foreign, "deployed")

		// The disposal record of an asset of client 2 counts as missing
		// for a principal of client 1; the asset itself is 404.
		hidden := assetIn("wp279-hidden", f.Client2, "retired", "")
		admin(`INSERT INTO disposal_record (organization_id, asset_id, method, performed_at)
			VALUES ($1, $2, 'recycling', now()) RETURNING id::text`, f.OrgA, hidden)
		var guardErr *lifecycle.GuardError
		if err := guards.CheckGuard(client1, lifecycle.GuardDisposalRecord, &lifecycle.TransitionCheck{OrgID: f.OrgA,
			EntityType: "asset", EntityID: hidden, To: "disposed"}); !errors.As(err, &guardErr) ||
			guardErr.Guard != lifecycle.GuardDisposalRecord {
			t.Errorf("disposal record of an invisible asset: %v, want a guard error", err)
		}
		if code, _ := post(h.with(t), client1, hidden, `{"to_state":"disposed"}`); code != http.StatusNotFound {
			t.Errorf("transition of an invisible asset: %d, want 404", code)
		}
		if got := trailOf(hidden); got.state != "retired" {
			t.Errorf("invisible asset: %+v", got)
		}
	})

	t.Run("problem body and invalid transition", func(t *testing.T) {
		id := assetIn("wp279-problem", f.Client1, "in_stock", bin)
		code, p := post(h.with(t), orgA, id, `{"to_state":"deployed"}`)
		if code != http.StatusUnprocessableEntity || p.Status != http.StatusUnprocessableEntity ||
			p.Guard != lifecycle.GuardOperationsLocationOrHolder || p.Detail == "" ||
			p.Violations[0].Field != "location_id" || p.Violations[1].Field != "assignee" {
			t.Errorf("problem %d %+v", code, p)
		}
		code, p = post(h.with(t), orgA, id, `{"to_state":"disposed"}`)
		if code != http.StatusUnprocessableEntity || p.Type != lifecycle.ProblemInvalidTransition || p.Field != "to_state" {
			t.Errorf("missing edge: %d %+v, want 422 invalid-transition", code, p)
		}
		code, p = post(h.with(t), orgA, id, `{"to_state":"lost_in_space"}`)
		if code != http.StatusUnprocessableEntity || p.Type != lifecycle.ProblemInvalidTransition {
			t.Errorf("unknown state: %d %+v, want 422 invalid-transition", code, p)
		}
		if code, _ = post(h.with(t), orgA, f.ID(), `{"to_state":"deployed"}`); code != http.StatusNotFound {
			t.Errorf("unknown asset: %d, want 404", code)
		}
	})

	t.Run("disposal record and transition in one transaction", func(t *testing.T) {
		id := assetIn("wp279-atomic", f.Client1, "retired", "")
		insertRecord := func(ctx context.Context) error {
			return database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `INSERT INTO disposal_record (organization_id, asset_id, method, performed_at)
					VALUES ($1, $2, 'recycling', now())`, f.OrgA, id)
				return err
			})
		}
		// Rolled back: neither record nor state.
		forced := errors.New("forced rollback")
		err := database.WithRequestTenantTx(orgA, f.App, f.OrgA, func(ctx context.Context) error {
			if insErr := insertRecord(ctx); insErr != nil {
				return insErr
			}
			if _, trErr := service.Transition(ctx, f.OrgA, "asset", id, physicalAssetModel,
				lifecycle.TransitionRequest{ToState: "disposed"}); trErr != nil {
				return trErr
			}
			return forced
		})
		if !errors.Is(err, forced) {
			t.Fatalf("rolled back transaction: %v", err)
		}
		if got := admin(`SELECT count(*)::text FROM disposal_record WHERE asset_id = $1`, id); got != "0" || trailOf(id).state != "retired" {
			t.Fatalf("after the rollback: %s records, %+v", got, trailOf(id))
		}
		// Committed: the transition sees the record of the same commit.
		if err = database.WithRequestTenantTx(orgA, f.App, f.OrgA, func(ctx context.Context) error {
			if insErr := insertRecord(ctx); insErr != nil {
				return insErr
			}
			_, trErr := service.Transition(ctx, f.OrgA, "asset", id, physicalAssetModel,
				lifecycle.TransitionRequest{ToState: "disposed"})
			return trErr
		}); err != nil {
			t.Fatalf("disposal with transition: %v", err)
		}
		if got := trailOf(id); got.state != "disposed" || got.events != 1 {
			t.Errorf("after the commit: %+v", got)
		}
	})

	t.Run("return holds the lock against deployed", func(t *testing.T) {
		id := assetIn("wp279-race", f.Client1, "in_stock", bin)
		if _, err := assignments.Assign(orgA, f.OrgA, &assignment.AssignInput{AssetID: id,
			Holder: assignment.Holder{UserID: user}}); err != nil {
			t.Fatalf("assign: %v", err)
		}
		returned := make(chan struct{})
		release := make(chan struct{})
		done := make(chan error, 1)
		go func() {
			done <- database.WithRequestTenantTx(orgA, f.App, f.OrgA, func(ctx context.Context) error {
				if _, err := assignments.Return(ctx, f.OrgA, &assignment.ReturnInput{AssetID: id, ExpectedHolder: user}); err != nil {
					close(returned)
					return err
				}
				close(returned)
				<-release
				return nil
			})
		}()
		<-returned
		result := make(chan error, 1)
		go func() {
			_, err := service.Transition(orgA, f.OrgA, "asset", id, physicalAssetModel, lifecycle.TransitionRequest{ToState: "deployed"})
			result <- err
		}()
		// The transition waits for the asset lock of the open return.
		deadline := time.Now().Add(10 * time.Second)
		for admin(`SELECT count(*)::text FROM pg_stat_activity WHERE wait_event_type = 'Lock' AND query LIKE '%FOR UPDATE%'`) == "0" {
			if time.Now().After(deadline) {
				t.Fatal("the transition did not wait for the asset lock")
			}
			time.Sleep(20 * time.Millisecond)
		}
		close(release)
		if err := <-done; err != nil {
			t.Fatalf("return: %v", err)
		}
		var guardErr *lifecycle.GuardError
		if err := <-result; !errors.As(err, &guardErr) || guardErr.Guard != lifecycle.GuardOperationsLocationOrHolder {
			t.Fatalf("transition after the return: %v, want the guard error", err)
		}
		if got := admin(`SELECT lifecycle_state || '|' || (assigned_user_id IS NULL)::text FROM asset WHERE id = $1`, id); got != "in_stock|true" {
			t.Errorf("after the race: %s, want in_stock without holder", got)
		}
	})

	t.Run("organization model on the asset type", func(t *testing.T) {
		own := &lifecycle.Definition{OrganizationID: f.OrgA, Key: "wp279_assets", Name: "WP-279 assets", AppliesTo: "asset"}
		for i, key := range []string{"in_stock", "reserved", "deployed", "repair", "retired", "disposed"} {
			own.States = append(own.States, lifecycle.State{Key: key, Label: key, IsInitial: i == 0, SortOrder: i})
		}
		own.Transitions = []lifecycle.Transition{{FromStateKey: "", ToStateKey: "deployed", Key: "deploy"}}
		if err := repo.Create(orgA, own); err != nil {
			t.Fatal(err)
		}
		assetType := admin(`INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
			VALUES ($1, 'wp279-asset', 'WP-279 asset', $2) RETURNING id::text`, f.OrgA, own.ID)
		typed := assetIn("wp279-typed", f.Client1, "repair", "")
		admin(`UPDATE asset SET asset_type_id = $2 WHERE id = $1 RETURNING id::text`, typed, assetType)
		refused(t, orgA, typed, "deployed", lifecycle.GuardOperationsLocationOrHolder, "location_id", "assignee")
		admin(`UPDATE asset SET location_id = $2 WHERE id = $1 RETURNING id::text`, typed, room)
		passes(t, orgA, typed, "deployed")
	})

	t.Run("CI model without system guards", func(t *testing.T) {
		model := h.with(t).create(orgA, `{"key":"wp279_ci","name":"WP-279 CI","applies_to":"ci","states":[
			{"key":"planned","label":"Planned","is_initial":true,"sort_order":10},
			{"key":"deployed","label":"Deployed","sort_order":20}],
			"transitions":[{"from_state_key":"planned","to_state_key":"deployed","key":"deploy"}]}`)
		ciType := admin(`INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
			VALUES ($1, 'wp279-ci', 'WP-279 CI', $2) RETURNING id::text`, f.OrgA, model.ID)
		ci := f.CI(t, f.OrgA, f.Client1, "wp279-ci")
		admin(`UPDATE ci SET ci_type_id = $2, lifecycle_state = 'planned' WHERE id = $1 RETURNING id::text`, ci, ciType)
		if w := h.with(t).call(orgA, http.MethodPost, "/api/v1/cis/"+ci+"/lifecycle-transitions", `{"to_state":"deployed"}`); w.Code != http.StatusOK {
			t.Errorf("CI into deployed: %d %s, want 200", w.Code, w.Body)
		}
	})
}

// TestLifecycleGuardsFailClosed: without a checker (profile --no-db) a
// transition of an asset into a guarded state is refused, other states
// pass.
func TestLifecycleGuardsFailClosed(t *testing.T) {
	repo := lifecycle.NewMemoryRepository()
	store := lifecycle.NewMemoryStateStore()
	store.SeedEntity("asset", "a1", "physical_asset")
	defs, _, err := repo.List(context.Background(), "org", api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var def *lifecycle.Definition
	for i := range defs {
		if defs[i].Key == "physical_asset" {
			def = &defs[i]
		}
	}
	if def == nil {
		t.Fatal("memory seed without physical_asset")
	}
	service := lifecycle.NewService(repo, store, nil)
	ctx := context.Background()
	for _, to := range []string{"ordered", "received"} {
		if _, err = service.Transition(ctx, "org", "asset", "a1", def.ID, lifecycle.TransitionRequest{ToState: to}); err != nil {
			t.Fatalf("into %s: %v", to, err)
		}
	}
	_, err = service.Transition(ctx, "org", "asset", "a1", def.ID, lifecycle.TransitionRequest{ToState: "in_stock"})
	var guardErr *lifecycle.GuardError
	if !errors.As(err, &guardErr) || guardErr.Guard != lifecycle.GuardStorageLocation ||
		!slices.Equal(guardErr.Missing, []string{"location_id"}) {
		t.Fatalf("in_stock without a checker: %v, want the guard error", err)
	}
	if state, _ := store.CurrentState(ctx, "org", "asset", "a1"); state != "received" {
		t.Errorf("state %q after the refused transition", state)
	}
}
