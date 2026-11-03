package lifecycle_test

// WP-724 (LCY-02, MET-45): the transactional lifecycle transition.
//
//   - Success: lifecycle_state is set, ci.version rises (the ETag changes),
//     one entity_change row (lifecycle_transition), one audit entry
//     ci.lifecycle_changed and one event lifecycle.changed with
//     entity_type, from, to and model_key arise.
//   - Rollback: an error after SetState (history, outbox) and an outer
//     transaction that rolls back leave no state change, history, audit
//     or event.
//   - Guards and error contract: failed required fields and guard rules
//     are 422 lifecycle-guard with violations; a missing edge 422
//     invalid-transition; a CI without model 422; a CI of a foreign client
//     404; a from_state that is not the current state 409.
//   - Concurrency: of two parallel transitions from the same state with
//     from_state one succeeds and the other is 409; without from_state the
//     second is checked against the new state. An organization model with
//     the key physical_asset on an asset type is the model of its assets.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run 'TestLifecycleTransition' ./internal/lifecycle/

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// transitionProblem is the RFC 7807 body of a refused transition.
type transitionProblem struct {
	Type         string                     `json:"type"`
	Status       int                        `json:"status"`
	Field        string                     `json:"field"`
	CurrentState string                     `json:"current_state"`
	Violations   []lifecycle.GuardViolation `json:"violations"`
}

// failingRecorder is a ChangeRecorder whose history write fails.
type failingRecorder struct{}

func (failingRecorder) RecordChange(context.Context, string, string, string, string, string, any, any, string) error {
	return errors.New("forced history failure")
}

// trail is what a transition leaves on a CI or asset.
type trail struct {
	state            string
	version          int64
	changes, entries int
	events           int
}

const serviceModelBody = `{"key":"wp724_service","name":"Service","applies_to":"ci","states":[
	{"key":"draft","label":"Draft","is_initial":true,"sort_order":10},
	{"key":"production","label":"Production","sort_order":20,"required_fields":["owner"]},
	{"key":"retired","label":"Retired","is_terminal":true,"sort_order":30}],
	"transitions":[{"from_state_key":"draft","to_state_key":"production","key":"go_live",
		"guard":{"fields":{"version":{"op":"all","rules":[{"op":"required"},{"op":"pattern","pattern":"^v[0-9]+$"}]}}}},
	{"from_state_key":"","to_state_key":"retired","key":"retire"}]}`

// TestLifecycleTransition runs the transition service against PostgreSQL.
func TestLifecycleTransition(t *testing.T) {
	f := scopetest.Seed(t, "18")
	bg := context.Background()
	user := f.AppUser(t, f.OrgA, "wp724-admin")
	ctxOf := func(org string, clients ...string) context.Context {
		scope := database.OrgWideScope(org, user)
		if len(clients) > 0 {
			scope.Clients = database.ScopeIDs(clients...)
		}
		ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
			tenant.TenantInfo{OrganizationID: org, UserID: user})
		return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp724", CorrelationID: "wp724", Source: audit.SourceUI})
	}
	orgA := ctxOf(f.OrgA)
	admin := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	repo := lifecycle.NewPGRepository(f.App)
	store := lifecycle.NewPGStateStore(f.App)
	hist := history.NewPGRepository(f.App)
	service := lifecycle.NewService(repo, store, lifecycle.NewHistoryRecorder(hist))
	root := newModelAPI(t, lifecycle.NewHandler(repo, service, lifecycle.NewResolver(f.App)))

	model := root.create(orgA, serviceModelBody)
	serviceType := admin(`INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
		VALUES ($1, 'wp724-service', 'WP-724 service', $2) RETURNING id::text`, f.OrgA, model.ID)
	ciOfModel := func(name, client string) string {
		t.Helper()
		id := f.CI(t, f.OrgA, client, name)
		admin(`UPDATE ci SET ci_type_id = $2 WHERE id = $1 RETURNING id::text`, id, serviceType)
		return id
	}
	trailOf := func(table, id string) trail {
		t.Helper()
		var tr trail
		version := "0"
		if table == "ci" {
			version = "version"
		}
		if err := f.Admin.QueryRow(bg, fmt.Sprintf(`SELECT COALESCE(lifecycle_state, ''), %s,
			(SELECT count(*) FROM entity_change WHERE entity_id = $1::uuid AND change_type = 'lifecycle_transition'),
			(SELECT count(*) FROM audit_log WHERE resource_id::text = $1::text),
			(SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'lifecycle.changed')
			FROM %s WHERE id = $1::uuid`, version, table), id).Scan(&tr.state, &tr.version, &tr.changes, &tr.entries, &tr.events); err != nil {
			t.Fatalf("trail of %s: %v", id, err)
		}
		return tr
	}
	post := func(m *modelAPI, ctx context.Context, kind, id, body string) (int, transitionProblem) {
		m.t.Helper()
		w := m.call(ctx, http.MethodPost, "/api/v1/"+kind+"/"+id+"/lifecycle-transitions", body)
		var p transitionProblem
		if w.Code >= 300 {
			if err := json.Unmarshal(w.Body.Bytes(), &p); err != nil {
				m.t.Fatalf("decode %s: %v", w.Body.String(), err)
			}
		}
		return w.Code, p
	}

	t.Run("success with history, audit and event", func(t *testing.T) {
		m := root.with(t)
		ci := ciOfModel("wp724-ci", f.Client1)
		before := trailOf("ci", ci)
		var result lifecycle.TransitionResult
		m.want(m.call(orgA, http.MethodPost, "/api/v1/cis/"+ci+"/lifecycle-transitions",
			`{"to_state":"draft","from_state":"","reason":"intake"}`), http.StatusOK, &result)
		if result.ToState != "draft" || result.FromState != "" || result.EntityID != ci {
			t.Errorf("result %+v", result)
		}
		after := trailOf("ci", ci)
		if after.state != "draft" || after.version != before.version+1 || api.ETag(after.version) == api.ETag(before.version) {
			t.Errorf("after the transition: %+v, before %+v", after, before)
		}
		if after.changes != 1 || after.entries != 1 || after.events != 1 {
			t.Errorf("history %d, audit %d, events %d; want one each", after.changes, after.entries, after.events)
		}
		if got := admin(`SELECT concat_ws('|', field_name, old_value::text, new_value::text, comment, actor_id::text)
			FROM entity_change WHERE entity_id = $1`, ci); got != "lifecycle_state|\"draft\"|intake|"+user {
			t.Errorf("entity_change %s", got)
		}
		if got := admin(`SELECT concat_ws('|', action, resource_type, actor_id, changes->'lifecycle_state', changes->>'reason',
			(changes ? 'route')::text, request_id) FROM audit_log WHERE resource_id = $1`, ci); got !=
			`ci.lifecycle_changed|ci|`+user+`|[null, "draft"]|intake|false|wp724` {
			t.Errorf("audit entry %s", got)
		}
		var data map[string]any
		if err := json.Unmarshal([]byte(admin(`SELECT data::text FROM event_outbox WHERE entity_id = $1 AND type = 'lifecycle.changed'`, ci)),
			&data); err != nil {
			t.Fatal(err)
		}
		want := map[string]any{"entity_type": "ci", "entity_id": ci, "client_id": f.Client1, "to": "draft",
			"model_id": model.ID, "model_key": "wp724_service", "reason": "intake"}
		for k, v := range want {
			if data[k] != v {
				t.Errorf("event data %s = %v, want %v (%v)", k, data[k], v, data)
			}
		}
		if _, ok := data["from"]; ok {
			t.Errorf("event of the initial state carries from: %v", data)
		}
		changes, _, err := hist.List(orgA, f.OrgA, "ci", ci, api.PaginationParams{Limit: 10}, history.ListOptions{})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, c := range changes {
			found = found || (c.ChangeType == "lifecycle_transition" && c.NewValue == "draft")
		}
		if !found {
			t.Errorf("history of the CI lacks the transition: %+v", changes)
		}
	})

	t.Run("guards and error contract", func(t *testing.T) {
		m := root.with(t)
		ci := ciOfModel("wp724-guarded", f.Client1)
		admin(`UPDATE ci SET lifecycle_state = 'draft' WHERE id = $1 RETURNING id::text`, ci)
		before := trailOf("ci", ci)

		code, p := post(m, orgA, "cis", ci, `{"to_state":"production"}`)
		if code != http.StatusUnprocessableEntity || p.Type != lifecycle.ProblemLifecycleGuard || len(p.Violations) != 2 ||
			p.Violations[0] != (lifecycle.GuardViolation{Field: "owner", Code: "required", Message: `lifecycle transition requires field "owner"`}) ||
			p.Violations[1].Field != "version" || p.Violations[1].Code != "required" {
			t.Errorf("missing fields: %d %+v", code, p)
		}
		code, p = post(m, orgA, "cis", ci, `{"to_state":"production","context":{"owner":"ops","version":"1.0"}}`)
		if code != http.StatusUnprocessableEntity || p.Type != lifecycle.ProblemLifecycleGuard || len(p.Violations) != 1 ||
			p.Violations[0].Field != "version" || p.Violations[0].Code != "pattern" {
			t.Errorf("failed guard rule: %d %+v", code, p)
		}
		if after := trailOf("ci", ci); after != before {
			t.Errorf("a refused transition changed %+v to %+v", before, after)
		}

		code, p = post(m, orgA, "cis", ci, `{"to_state":"draft"}`)
		if code != http.StatusUnprocessableEntity || p.Type != lifecycle.ProblemInvalidTransition || p.Field != "to_state" {
			t.Errorf("missing edge: %d %+v", code, p)
		}
		code, p = post(m, orgA, "cis", ci, `{"to_state":"unknown"}`)
		if code != http.StatusUnprocessableEntity || p.Type != lifecycle.ProblemInvalidTransition {
			t.Errorf("unknown state: %d %+v", code, p)
		}
		code, p = post(m, orgA, "cis", ci, `{"to_state":"production","from_state":"retired","context":{"owner":"ops","version":"v2"}}`)
		if code != http.StatusConflict || p.Type != lifecycle.ProblemConflict || p.CurrentState != "draft" {
			t.Errorf("stale from_state: %d %+v", code, p)
		}

		m.want(m.call(orgA, http.MethodPost, "/api/v1/cis/"+ci+"/lifecycle-transitions",
			`{"to_state":"production","from_state":"draft","context":{"owner":"ops","version":"v2"}}`), http.StatusOK, nil)
		// The edge without from state applies from every state.
		m.want(m.call(orgA, http.MethodPost, "/api/v1/cis/"+ci+"/lifecycle-transitions", `{"to_state":"retired"}`),
			http.StatusOK, nil)
		if after := trailOf("ci", ci); after.state != "retired" || after.version != before.version+2 || after.changes != 2 ||
			after.entries != 2 || after.events != 2 {
			t.Errorf("after two transitions: %+v (before %+v)", after, before)
		}

		code, p = post(m, orgA, "cis", f.CI(t, f.OrgA, f.Client1, "wp724-server"), `{"to_state":"draft"}`)
		if code != http.StatusUnprocessableEntity || p.Field != "to_state" {
			t.Errorf("CI without model: %d %+v", code, p)
		}
		foreign := ciOfModel("wp724-client2", f.Client2)
		if code, _ = post(m, ctxOf(f.OrgA, f.Client1), "cis", foreign, `{"to_state":"draft"}`); code != http.StatusNotFound {
			t.Errorf("CI of a foreign client: %d, want 404", code)
		}
		if code, _ = post(m, ctxOf(f.OrgB), "cis", ci, `{"to_state":"draft"}`); code != http.StatusNotFound {
			t.Errorf("CI of another organization: %d, want 404", code)
		}
		if tr := trailOf("ci", foreign); tr.state != "" || tr.changes != 0 {
			t.Errorf("foreign CI changed: %+v", tr)
		}
	})

	t.Run("rollback", func(t *testing.T) {
		ci := ciOfModel("wp724-rollback", f.Client1)
		before := trailOf("ci", ci)
		draft := lifecycle.TransitionRequest{ToState: "draft", Reason: "rollback"}
		unchanged := func(what string) {
			t.Helper()
			if after := trailOf("ci", ci); after != before {
				t.Errorf("%s left %+v (before %+v)", what, after, before)
			}
		}

		// The history write after SetState fails.
		failing := lifecycle.NewService(repo, store, failingRecorder{})
		if _, err := failing.Transition(orgA, f.OrgA, "ci", ci, model.ID, draft); err == nil {
			t.Error("a failed history write was accepted")
		}
		unchanged("a failed history write")

		// The outbox refuses the event, right before the commit.
		exec := func(sql string) {
			t.Helper()
			if _, err := f.Admin.Exec(bg, sql); err != nil {
				t.Fatalf("%s: %v", sql, err)
			}
		}
		exec(fmt.Sprintf(`CREATE FUNCTION wp724_refuse_event() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN
				IF NEW.entity_id = '%s' THEN RAISE EXCEPTION 'wp724 forced outbox failure'; END IF;
				RETURN NEW;
			END $$`, ci))
		exec(`CREATE TRIGGER wp724_refuse_event BEFORE INSERT ON event_outbox
			FOR EACH ROW EXECUTE FUNCTION wp724_refuse_event()`)
		t.Cleanup(func() {
			_, _ = f.Admin.Exec(bg, `DROP TRIGGER IF EXISTS wp724_refuse_event ON event_outbox`)
			_, _ = f.Admin.Exec(bg, `DROP FUNCTION IF EXISTS wp724_refuse_event()`)
		})
		if _, err := service.Transition(orgA, f.OrgA, "ci", ci, model.ID, draft); err == nil {
			t.Error("a failed outbox write was accepted")
		}
		unchanged("a failed outbox write")
		exec(`DROP TRIGGER wp724_refuse_event ON event_outbox`)

		// An outer transaction that rolls back takes the transition along.
		errOuter := errors.New("outer rollback")
		err := database.WithRequestTenantTx(orgA, f.App, f.OrgA, func(ctx context.Context) error {
			if _, tErr := service.Transition(ctx, f.OrgA, "ci", ci, model.ID, draft); tErr != nil {
				return tErr
			}
			return errOuter
		})
		if !errors.Is(err, errOuter) {
			t.Errorf("outer transaction: %v", err)
		}
		unchanged("a rolled back outer transaction")

		// Committed, the outer transaction keeps it.
		if err = database.WithRequestTenantTx(orgA, f.App, f.OrgA, func(ctx context.Context) error {
			_, tErr := service.Transition(ctx, f.OrgA, "ci", ci, model.ID, draft)
			return tErr
		}); err != nil {
			t.Fatal(err)
		}
		if after := trailOf("ci", ci); after.state != "draft" || after.changes != 1 || after.entries != 1 || after.events != 1 {
			t.Errorf("committed outer transaction: %+v", after)
		}
	})

	t.Run("concurrency", func(t *testing.T) {
		golive := func(from *string) lifecycle.TransitionRequest {
			return lifecycle.TransitionRequest{ToState: "production", FromState: from,
				Context: map[string]any{"owner": "ops", "version": "v1"}}
		}
		draft := "draft"
		// parallel holds the first transition's transaction open until the
		// second one waits for the row lock, then commits.
		parallel := func(ci string, second lifecycle.TransitionRequest) error {
			t.Helper()
			done := make(chan error, 1)
			err := database.WithRequestTenantTx(orgA, f.App, f.OrgA, func(ctx context.Context) error {
				if _, err := service.Transition(ctx, f.OrgA, "ci", ci, model.ID, golive(&draft)); err != nil {
					return err
				}
				go func() {
					_, err := service.Transition(orgA, f.OrgA, "ci", ci, model.ID, second)
					done <- err
				}()
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					var waiting int
					if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM pg_stat_activity
						WHERE datname = current_database() AND wait_event_type = 'Lock'`).Scan(&waiting); err != nil {
						return err
					}
					if waiting > 0 {
						return nil
					}
					time.Sleep(20 * time.Millisecond)
				}
				return errors.New("the second transition did not wait for the row lock")
			})
			if err != nil {
				t.Fatalf("first transition: %v", err)
			}
			return <-done
		}

		ci := ciOfModel("wp724-race", f.Client1)
		admin(`UPDATE ci SET lifecycle_state = 'draft' WHERE id = $1 RETURNING id::text`, ci)
		var conflict *lifecycle.StateConflictError
		if err := parallel(ci, golive(&draft)); !errors.As(err, &conflict) || conflict.CurrentState != "production" {
			t.Errorf("second transition with from_state: %v, want a conflict with current_state production", err)
		}
		// Without from_state the second one is checked against the new
		// state: production has no edge to production.
		ci2 := ciOfModel("wp724-race2", f.Client1)
		admin(`UPDATE ci SET lifecycle_state = 'draft' WHERE id = $1 RETURNING id::text`, ci2)
		var invalid *lifecycle.InvalidTransitionError
		if err := parallel(ci2, golive(nil)); !errors.As(err, &invalid) {
			t.Errorf("second transition without from_state: %v, want an invalid transition from production", err)
		}
		for _, id := range []string{ci, ci2} {
			if tr := trailOf("ci", id); tr.state != "production" || tr.changes != 1 || tr.events != 1 {
				t.Errorf("after the race: %+v, want production with one transition", tr)
			}
		}
	})

	t.Run("organization model with the key physical_asset", func(t *testing.T) {
		m := root.with(t)
		own := &lifecycle.Definition{OrganizationID: f.OrgA, Key: "physical_asset", Name: "Own assets", AppliesTo: "asset"}
		for i, key := range []string{"in_stock", "reserved", "deployed", "repair", "retired", "disposed"} {
			own.States = append(own.States, lifecycle.State{Key: key, Label: key, IsInitial: i == 0, SortOrder: i})
		}
		if err := repo.Create(orgA, own); err != nil {
			t.Fatal(err)
		}
		assetType := admin(`INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
			VALUES ($1, 'wp724-asset', 'WP-724 asset', $2) RETURNING id::text`, f.OrgA, own.ID)
		typed := f.Asset(t, f.OrgA, f.Client1, "wp724-typed")
		admin(`UPDATE asset SET asset_type_id = $2 WHERE id = $1 RETURNING id::text`, typed, assetType)
		// in_stock is the initial state of the organization model; the
		// system model starts at ordered and requires a storage location.
		m.want(m.call(orgA, http.MethodPost, "/api/v1/assets/"+typed+"/lifecycle-transitions", `{"to_state":"in_stock"}`),
			http.StatusOK, nil)
		if got := admin(`SELECT data->>'model_id' || '|' || (data->>'entity_type') FROM event_outbox
			WHERE entity_id = $1 AND type = 'lifecycle.changed'`, typed); got != own.ID+"|asset" {
			t.Errorf("event of the typed asset: %s, want the organization model %s", got, own.ID)
		}
		if got := admin(`SELECT action FROM audit_log WHERE resource_id = $1`, typed); got != "asset.lifecycle_changed" {
			t.Errorf("audit action %s", got)
		}
		untyped := f.Asset(t, f.OrgA, f.Client1, "wp724-untyped")
		code, p := post(m, orgA, "assets", untyped, `{"to_state":"in_stock"}`)
		if code != http.StatusUnprocessableEntity || p.Type != lifecycle.ProblemInvalidTransition {
			t.Errorf("untyped asset into in_stock: %d %+v, want the system model's initial state rule", code, p)
		}
	})
}
