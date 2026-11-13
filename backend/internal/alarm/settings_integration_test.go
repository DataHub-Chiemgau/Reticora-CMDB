package alarm_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The alarm settings on PostgreSQL (WP-1030): the row round-trips, a
// principal restricted to a client may not write them, the organizations
// stay apart (RLS), and the PUT is audited by the route audit with the old
// and the new values of the row (snapshot register).

type settingsEnv struct {
	t   *testing.T
	f   *scopetest.Fixture
	svc *alarm.Service
	mux http.Handler
}

func newSettingsEnv(t *testing.T) *settingsEnv {
	t.Helper()
	f := scopetest.Seed(t, alarmTag)
	svc := alarm.NewPGService(f.App)
	inner := chi.NewRouter()
	alarm.NewHandler(svc).RegisterRoutes(inner)
	mux := chi.NewRouter()
	mux.Use(middleware.RequestID)
	auditor := audit.NewPGRouteAuditor(f.App)
	for _, rt := range []struct {
		method string
		mode   middleware.AuditMode
	}{{http.MethodGet, middleware.AuditNone}, {http.MethodPut, middleware.AuditIntent}} {
		mux.With(server.AuthorizeRoute(rt.method, settingsPath), middleware.AuditRoute(rt.method, settingsPath, auditor, rt.mode, "")).
			Method(rt.method, settingsPath, inner)
	}
	return &settingsEnv{t: t, f: f, svc: svc, mux: mux}
}

// do sends a request as the fixture's user of org with perms, org-wide or
// restricted to client.
func (e *settingsEnv) do(org, client string, perms []identity.Permission, method, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	return e.doScoped(org, client, "", perms, method, body)
}

// doScoped is do, restricted to client and/or site when given.
func (e *settingsEnv) doScoped(org, client, site string, perms []identity.Permission, method, body string) *httptest.ResponseRecorder {
	e.t.Helper()
	scope := database.OrgWideScope(org, e.f.User)
	idScope := &identity.Scope{Clients: identity.ScopeSet{All: true}, Sites: identity.ScopeSet{All: true}, Teams: identity.ScopeSet{All: true}}
	if client != "" {
		scope.Clients = database.ScopeIDs(client)
		idScope.Clients = identity.ScopeSet{IDs: []string{client}}
	}
	if site != "" {
		scope.Sites = database.ScopeIDs(site)
		idScope.Sites = identity.ScopeSet{IDs: []string{site}}
	}
	req := httptest.NewRequest(method, settingsPath, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	p := identity.Principal{Subject: e.f.User, OrganizationID: org, Type: identity.PrincipalTypeUser, Scope: idScope, Permissions: perms}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), perms)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: e.f.User})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req.WithContext(ctx))
	return rec
}

func (e *settingsEnv) settings(rec *httptest.ResponseRecorder) alarm.Settings {
	e.t.Helper()
	if rec.Code != http.StatusOK {
		e.t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	var s alarm.Settings
	if err := json.Unmarshal(rec.Body.Bytes(), &s); err != nil {
		e.t.Fatal(err)
	}
	return s
}

const fullSettings = `{"settle_seconds":45,"flap_transitions":7,"flap_window_seconds":1200,"flap_hold_seconds":600,
	"notify_rate_per_min":200,"unknown_mode":"up","notify_after_maintenance":false}`

func TestAlarmSettingsRoundTrip(t *testing.T) {
	e := newSettingsEnv(t)
	if got := e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodGet, "")); got != alarm.Defaults() {
		t.Fatalf("without row: %+v, want the defaults", got)
	}
	put := e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodPut, fullSettings))
	got := e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodGet, ""))
	if got.SettleSeconds != 45 || got.FlapTransitions != 7 || got.FlapWindowSeconds != 1200 || got.FlapHoldSeconds != 600 ||
		got.NotifyRatePerMin != 200 || got.UnknownMode != alarm.UnknownUp || got.NotifyAfterMaintenance ||
		got.UpdatedAt == nil || got.UpdatedBy == nil || *got.UpdatedBy != e.f.User || !got.UpdatedAt.Equal(*put.UpdatedAt) {
		t.Errorf("stored %+v (PUT answered %+v)", got, put)
	}
	// A second PUT replaces the row; absent members take their defaults.
	got = e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodPut, `{"unknown_mode":"down"}`))
	if want := alarm.Defaults(); got.UnknownMode != alarm.UnknownDown || got.SettleSeconds != want.SettleSeconds ||
		!got.NotifyAfterMaintenance {
		t.Errorf("replaced %+v", got)
	}
	// The CHECKs of the table hold the bounds as well.
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE alarm_settings SET settle_seconds = 301 WHERE organization_id = $1`,
		e.f.OrgA); err == nil {
		t.Error("alarm_settings accepts settle_seconds 301")
	}
}

func TestAlarmSettingsClientScopedForbidden(t *testing.T) {
	e := newSettingsEnv(t)
	rec := e.do(e.f.OrgA, e.f.Client1, settingsWriter, http.MethodPut, fullSettings)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("PUT restricted to a client: %d %s, want 403", rec.Code, rec.Body.String())
	}
	if got := e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodGet, "")); got != alarm.Defaults() {
		t.Errorf("the refused PUT changed the settings: %+v", got)
	}
	// Restricted to a site (all clients) is not organization-wide either.
	if rec := e.doScoped(e.f.OrgA, "", e.f.ID(), settingsWriter, http.MethodPut, fullSettings); rec.Code != http.StatusForbidden {
		t.Fatalf("PUT restricted to a site: %d %s, want 403", rec.Code, rec.Body.String())
	}
	if got := e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodGet, "")); got != alarm.Defaults() {
		t.Errorf("the refused site PUT changed the settings: %+v", got)
	}
	// Reading is allowed in a client scope.
	if rec := e.do(e.f.OrgA, e.f.Client1, settingsWriter, http.MethodGet, ""); rec.Code != http.StatusOK {
		t.Errorf("GET restricted to a client: %d", rec.Code)
	}
}

func TestAlarmSettingsOrgIsolation(t *testing.T) {
	e := newSettingsEnv(t)
	e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodPut, fullSettings))
	if got := e.settings(e.do(e.f.OrgB, "", settingsWriter, http.MethodGet, "")); got != alarm.Defaults() {
		t.Errorf("organization B reads %+v, want its defaults", got)
	}
	// In the tenant scope of B the row of A is invisible.
	var n int
	if err := database.WithRequestTenant(e.f.OrgCtx(e.f.OrgB), e.f.App, e.f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM alarm_settings`).Scan(&n)
	}); err != nil || n != 0 {
		t.Errorf("rows of alarm_settings visible to B: %d (%v), want 0", n, err)
	}
}

func TestAlarmSettingsAudited(t *testing.T) {
	e := newSettingsEnv(t)
	e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodPut, `{"settle_seconds":60}`))
	e.settings(e.do(e.f.OrgA, "", settingsWriter, http.MethodPut, `{"settle_seconds":90,"unknown_mode":"down"}`))
	rows, err := e.f.Admin.Query(context.Background(), `SELECT changes FROM audit_log
		WHERE organization_id = $1 AND changes->>'route' = $2 ORDER BY seq`, e.f.OrgA, "PUT "+settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var entries []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		var c map[string]any
		_ = json.Unmarshal(raw, &c)
		entries = append(entries, c)
	}
	if len(entries) != 2 {
		t.Fatalf("audit entries of the PUT: %d, want 2", len(entries))
	}
	values, _ := entries[1]["values"].([]any)
	changed := map[string][2]any{}
	for _, v := range values {
		m, _ := v.(map[string]any)
		field, _ := m["field"].(string)
		changed[field] = [2]any{m["old"], m["new"]}
	}
	if c := changed["settle_seconds"]; c[0] != float64(60) || c[1] != float64(90) {
		t.Errorf("settle_seconds old/new %v, want 60/90 (values %v)", c, values)
	}
	if c := changed["unknown_mode"]; c[0] != "excluded" || c[1] != "down" {
		t.Errorf("unknown_mode old/new %v, want excluded/down", c)
	}
}
