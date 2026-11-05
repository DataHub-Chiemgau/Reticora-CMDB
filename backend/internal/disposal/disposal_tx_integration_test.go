package disposal_test

// WP-278 (MGT-02): a disposal runs in one transaction. The record, the
// movement disposal of the asset (movement service, WP-274) and the
// lifecycle transition to disposed (lifecycle service, WP-724) with their
// audit entries and the events stock.movement and lifecycle.changed commit
// together, or nothing remains.
//
//   - An asset in retired: 201 with the record, exactly one movement
//     disposal, state disposed, the audit entries and the outbox events.
//   - An asset in in_stock or without state: 422 invalid-transition with
//     the allowed source states of the model, without record, movement,
//     audit entry or event. A failing movement rolls everything back.
//   - A CI whose type has no lifecycle model: 201 with the record only; a
//     CI with a model enters disposed.
//   - Of two parallel disposals of the same asset one succeeds, the other
//     waits for the row lock and is refused with 422.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run 'TestDisposalTransaction' ./internal/disposal/

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// failingMovements books the movement and then fails, so the disposal
// must roll back the booked row and its event as well.
type failingMovements struct {
	svc *movement.Service
}

func (f failingMovements) Record(ctx context.Context, m *movement.Movement) error {
	if err := f.svc.Record(ctx, m); err != nil {
		return err
	}
	return errors.New("forced movement failure")
}

// disposalTrail is what a disposal leaves on an asset or CI.
type disposalTrail struct {
	state                         string
	records, movements            int
	recordEntries, lifecycleAudit int
	movementEvents, stateEvents   int
}

// disposalProblem is the RFC 7807 body of a refused disposal.
type disposalProblem struct {
	Type              string   `json:"type"`
	Status            int      `json:"status"`
	Field             string   `json:"field"`
	FromState         string   `json:"from_state"`
	ToState           string   `json:"to_state"`
	AllowedFromStates []string `json:"allowed_from_states"`
}

// TestDisposalTransaction runs POST /disposal-records with the PostgreSQL
// repositories and services against PostgreSQL.
func TestDisposalTransaction(t *testing.T) {
	f := scopetest.Seed(t, "32")
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

	user := f.AppUser(t, f.OrgA, "wp278-admin")
	scope := database.OrgWideScope(f.OrgA, user)
	ctx := tenant.WithTenant(database.ContextWithTenantScope(bg, &scope),
		tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
	ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp278", CorrelationID: "wp278", Source: audit.SourceUI})

	admin := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if qErr := f.Admin.QueryRow(bg, sql, args...).Scan(&s); qErr != nil {
			t.Fatalf("%s: %v", sql, qErr)
		}
		return s
	}
	lcRepo := lifecycle.NewPGRepository(f.App)
	lcService := lifecycle.NewService(lcRepo, lifecycle.NewPGStateStore(f.App),
		lifecycle.NewHistoryRecorder(history.NewPGRepository(f.App))).WithGuards(lifecycle.NewPGGuards(f.App))
	movements := movement.NewService(movement.NewPGRepository(f.App))
	newRouter := func(recorder disposal.MovementRecorder) http.Handler {
		r := chi.NewRouter()
		disposal.NewHandler(disposal.NewPGRepository(f.App)).
			WithLifecycle(recorder, lcService, lifecycle.NewResolver(f.App), lcRepo).RegisterRoutes(r)
		return r
	}
	router := newRouter(movements)
	post := func(h http.Handler, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/disposal-records", strings.NewReader(body)).WithContext(ctx)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		return w
	}
	assetBody := func(assetID string) string {
		return `{"method":"recycling","asset_id":"` + assetID + `","performed_at":"2026-11-03T10:00:00Z","performed_by":"Erika Mustermann"}`
	}
	trailOf := func(table, column, id string) disposalTrail {
		t.Helper()
		var tr disposalTrail
		if qErr := f.Admin.QueryRow(bg, `SELECT COALESCE((SELECT lifecycle_state FROM `+table+` WHERE id = $1::uuid), ''),
			(SELECT count(*) FROM disposal_record WHERE `+column+` = $1::uuid),
			(SELECT count(*) FROM stock_movement WHERE asset_id = $1::uuid AND movement_type = 'disposal'),
			(SELECT count(*) FROM audit_log WHERE action = 'disposal_record.created' AND changes->>$2 = $1::text),
			(SELECT count(*) FROM audit_log WHERE action = $3 AND resource_id::text = $1::text),
			(SELECT count(*) FROM event_outbox WHERE type = 'stock.movement' AND entity_id = $1::text),
			(SELECT count(*) FROM event_outbox WHERE type = 'lifecycle.changed' AND entity_id = $1::text)`,
			id, column, table+".lifecycle_changed").Scan(&tr.state, &tr.records, &tr.movements, &tr.recordEntries,
			&tr.lifecycleAudit, &tr.movementEvents, &tr.stateEvents); qErr != nil {
			t.Fatalf("trail of %s: %v", id, qErr)
		}
		return tr
	}
	assetIn := func(tag, state string) string {
		t.Helper()
		id := f.Asset(t, f.OrgA, f.Client1, tag)
		if state != "" {
			admin(`UPDATE asset SET lifecycle_state = $2 WHERE id = $1 RETURNING id::text`, id, state)
		}
		return id
	}
	refused := func(t *testing.T, w *httptest.ResponseRecorder, from string) {
		t.Helper()
		if w.Code != http.StatusUnprocessableEntity {
			t.Fatalf("status %d, want 422: %s", w.Code, w.Body.String())
		}
		var p disposalProblem
		if jsonErr := json.Unmarshal(w.Body.Bytes(), &p); jsonErr != nil {
			t.Fatal(jsonErr)
		}
		if p.Type != lifecycle.ProblemInvalidTransition || p.Field != "asset_id" || p.FromState != from ||
			p.ToState != "disposed" || !slices.Equal(p.AllowedFromStates, []string{"repair", "retired"}) {
			t.Errorf("problem %+v, want invalid-transition from %q with allowed source states repair, retired", p, from)
		}
	}

	t.Run("asset in retired", func(t *testing.T) {
		asset := assetIn("wp278-retired", "retired")
		w := post(router, assetBody(asset))
		if w.Code != http.StatusCreated {
			t.Fatalf("status %d, want 201: %s", w.Code, w.Body.String())
		}
		var rec disposal.Record
		if jsonErr := json.Unmarshal(w.Body.Bytes(), &rec); jsonErr != nil {
			t.Fatal(jsonErr)
		}
		got := trailOf("asset", "asset_id", asset)
		want := disposalTrail{state: "disposed", records: 1, movements: 1, recordEntries: 1, lifecycleAudit: 1,
			movementEvents: 1, stateEvents: 1}
		if got != want {
			t.Fatalf("trail %+v, want %+v", got, want)
		}
		if reason := admin(`SELECT concat_ws('|', reason, COALESCE(document_id::text, '')) FROM stock_movement
			WHERE asset_id = $1 AND movement_type = 'disposal'`, asset); reason != "recycling|" {
			t.Errorf("movement reason|document %q, want recycling|", reason)
		}
		if recordID := admin(`SELECT id::text FROM disposal_record WHERE asset_id = $1`, asset); recordID != rec.ID {
			t.Errorf("record %s, response %s", recordID, rec.ID)
		}
		if entry := admin(`SELECT concat_ws('|', changes->'lifecycle_state'->>0, changes->'lifecycle_state'->>1, changes->>'reason')
			FROM audit_log WHERE action = 'asset.lifecycle_changed' AND resource_id::text = $1`, asset); entry != "retired|disposed|disposal: recycling" {
			t.Errorf("lifecycle audit entry %q", entry)
		}
	})

	t.Run("asset in in_stock and without state", func(t *testing.T) {
		for _, state := range []string{"in_stock", ""} {
			asset := assetIn("wp278-src-"+state, state)
			refused(t, post(router, assetBody(asset)), state)
			if got := trailOf("asset", "asset_id", asset); got != (disposalTrail{state: state}) {
				t.Errorf("state %q: trail %+v, want nothing", state, got)
			}
		}
	})

	t.Run("failing movement rolls back", func(t *testing.T) {
		asset := assetIn("wp278-failing", "retired")
		w := post(newRouter(failingMovements{svc: movements}), assetBody(asset))
		if w.Code != http.StatusInternalServerError {
			t.Fatalf("status %d, want 500: %s", w.Code, w.Body.String())
		}
		if got := trailOf("asset", "asset_id", asset); got != (disposalTrail{state: "retired"}) {
			t.Errorf("trail %+v, want nothing but the state retired", got)
		}
	})

	t.Run("CI without and with lifecycle model", func(t *testing.T) {
		plain := f.CI(t, f.OrgA, f.Client1, "wp278-ci-plain")
		model := lifecycle.Definition{OrganizationID: f.OrgA, Key: "wp278_ci", Name: "WP-278 CI", AppliesTo: "ci",
			States: []lifecycle.State{{Key: "active", Label: "Active", IsInitial: true, SortOrder: 10},
				{Key: "retired", Label: "Retired", SortOrder: 20},
				{Key: "disposed", Label: "Disposed", IsTerminal: true, SortOrder: 30, RequiredFields: []string{"disposal_record"}}},
			Transitions: []lifecycle.Transition{{FromStateKey: "active", ToStateKey: "retired", Key: "retire"},
				{FromStateKey: "retired", ToStateKey: "disposed", Key: "dispose"}}}
		if createErr := lcRepo.Create(ctx, &model); createErr != nil {
			t.Fatal(createErr)
		}
		ciType := admin(`INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
			VALUES ($1, 'wp278-ci-type', 'WP-278 CI type', $2) RETURNING id::text`, f.OrgA, model.ID)
		modelled := f.CI(t, f.OrgA, f.Client1, "wp278-ci-modelled")
		admin(`UPDATE ci SET ci_type_id = $2, lifecycle_state = 'retired' WHERE id = $1 RETURNING id::text`, modelled, ciType)

		ciBody := func(ciID string) string {
			return `{"method":"reuse","ci_id":"` + ciID + `","performed_at":"2026-11-03T11:00:00Z","performed_by":"Max Zeugmann"}`
		}
		if w := post(router, ciBody(plain)); w.Code != http.StatusCreated {
			t.Fatalf("CI without model: status %d, want 201: %s", w.Code, w.Body.String())
		}
		if got := trailOf("ci", "ci_id", plain); got != (disposalTrail{records: 1, recordEntries: 1}) {
			t.Errorf("CI without model: trail %+v, want the record and its audit entry only", got)
		}
		if w := post(router, ciBody(modelled)); w.Code != http.StatusCreated {
			t.Fatalf("CI with model: status %d, want 201: %s", w.Code, w.Body.String())
		}
		if got := trailOf("ci", "ci_id", modelled); got != (disposalTrail{state: "disposed", records: 1, recordEntries: 1,
			lifecycleAudit: 1, stateEvents: 1}) {
			t.Errorf("CI with model: trail %+v, want disposed with record, audit entries and lifecycle.changed", got)
		}
	})

	t.Run("parallel disposals of the same asset", func(t *testing.T) {
		asset := assetIn("wp278-parallel", "retired")
		codes := make([]int, 2)
		var wg sync.WaitGroup
		for i := range codes {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				req := httptest.NewRequest(http.MethodPost, "/api/v1/disposal-records", strings.NewReader(assetBody(asset))).WithContext(ctx)
				req.Header.Set("Content-Type", "application/json")
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				codes[i] = w.Code
			}(i)
		}
		wg.Wait()
		slices.Sort(codes)
		if !slices.Equal(codes, []int{http.StatusCreated, http.StatusUnprocessableEntity}) {
			t.Fatalf("status codes %v, want one 201 and one 422", codes)
		}
		want := disposalTrail{state: "disposed", records: 1, movements: 1, recordEntries: 1, lifecycleAudit: 1,
			movementEvents: 1, stateEvents: 1}
		if got := trailOf("asset", "asset_id", asset); got != want {
			t.Errorf("trail %+v, want %+v", got, want)
		}
	})
}
