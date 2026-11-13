package sla_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The availability targets (ALR-06, WP-1048) on the memory repository:
// validation, the scope taken from the CI and the CRUD routes. The
// PostgreSQL cases are in availability_scope_integration_test.go.

const (
	availOrg    = "00000000-0000-4000-8000-00000000a0a1"
	serverCI    = "00000000-0000-4000-8000-00000000c001"
	serviceCI   = "00000000-0000-4000-8000-00000000c002"
	clientA     = "00000000-0000-4000-8000-00000000d001"
	clientB     = "00000000-0000-4000-8000-00000000d002"
	siteA       = "00000000-0000-4000-8000-00000000e001"
	otherClient = "00000000-0000-4000-8000-00000000d009"
)

// fakeObjects answers two CIs: a server of client A at site A and a
// business service of client A.
type fakeObjects struct{}

func (fakeObjects) ObjectCI(_ context.Context, orgID, ciID string) (sla.ObjectCI, bool, error) {
	if orgID != availOrg {
		return sla.ObjectCI{}, false, nil
	}
	c, s := clientA, siteA
	switch ciID {
	case serverCI:
		return sla.ObjectCI{ClientID: &c, SiteID: &s}, true, nil
	case serviceCI:
		return sla.ObjectCI{ClientID: &c, Service: true}, true, nil
	}
	return sla.ObjectCI{}, false, nil
}

func ptr[T any](v T) *T { return &v }

func TestAvailabilityTargetValidate(t *testing.T) {
	repo := sla.NewMemoryAvailabilityRepository(fakeObjects{}, sla.NewMemoryRepository())
	ctx := context.Background()
	base := func() *sla.AvailabilityTarget {
		return &sla.AvailabilityTarget{OrganizationID: availOrg, Name: "core", ScopeKind: sla.ScopeCI, CIID: ptr(serverCI),
			TargetPercent: 99.95, Period: sla.PeriodMonth, BudgetWarnPercent: 80, Enabled: true}
	}
	for name, tc := range map[string]struct {
		edit  func(t *sla.AvailabilityTarget)
		field string
	}{
		"target 0":          {func(t *sla.AvailabilityTarget) { t.TargetPercent = 0 }, "target_percent"},
		"target 100.001":    {func(t *sla.AvailabilityTarget) { t.TargetPercent = 100.001 }, "target_percent"},
		"target 99.9505":    {func(t *sla.AvailabilityTarget) { t.TargetPercent = 99.9505 }, "target_percent"},
		"warn 0":            {func(t *sla.AvailabilityTarget) { t.BudgetWarnPercent = 0 }, "budget_warn_percent"},
		"warn 101":          {func(t *sla.AvailabilityTarget) { t.BudgetWarnPercent = 101 }, "budget_warn_percent"},
		"client with ci":    {func(t *sla.AvailabilityTarget) { t.ScopeKind, t.ClientID = sla.ScopeClient, ptr(clientA) }, "ci_id"},
		"client no client":  {func(t *sla.AvailabilityTarget) { t.ScopeKind, t.CIID = sla.ScopeClient, nil }, "client_id"},
		"ci without ci":     {func(t *sla.AvailabilityTarget) { t.CIID = nil }, "ci_id"},
		"service on server": {func(t *sla.AvailabilityTarget) { t.ScopeKind = sla.ScopeService }, "ci_id"},
		"connection on service": {func(t *sla.AvailabilityTarget) {
			t.ScopeKind, t.CIID = sla.ScopeConnection, ptr(serviceCI)
		}, "ci_id"},
		"unknown ci":   {func(t *sla.AvailabilityTarget) { t.CIID = ptr(otherClient) }, "ci_id"},
		"bad kind":     {func(t *sla.AvailabilityTarget) { t.ScopeKind = "port" }, "scope_kind"},
		"bad period":   {func(t *sla.AvailabilityTarget) { t.Period = "week" }, "period"},
		"empty name":   {func(t *sla.AvailabilityTarget) { t.Name = "" }, "name"},
		"bad calendar": {func(t *sla.AvailabilityTarget) { t.CalendarID = ptr(otherClient) }, "calendar_id"},
	} {
		tgt := base()
		tc.edit(tgt)
		err := repo.CreateTarget(ctx, tgt)
		fe, ok := err.(*api.FieldError)
		if !ok || fe.Field != tc.field || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422 on %s", name, err, tc.field)
		}
	}
	for _, v := range []float64{99.95, 100, 0.001, 99.999} {
		tgt := base()
		tgt.TargetPercent = v
		tgt.Period = []string{sla.PeriodMonth, sla.PeriodQuarter, sla.PeriodYear, sla.PeriodMonth}[int(v)%3]
		tgt.Name = "ok"
		if err := tgt.Validate(); err != nil {
			t.Errorf("target %g: %v", v, err)
		}
	}
	// A service target on the business service, a connection on the server.
	for _, tgt := range []*sla.AvailabilityTarget{
		{OrganizationID: availOrg, Name: "svc", ScopeKind: sla.ScopeService, CIID: ptr(serviceCI), TargetPercent: 99.9,
			Period: sla.PeriodYear, BudgetWarnPercent: 80},
		{OrganizationID: availOrg, Name: "port", ScopeKind: sla.ScopeConnection, CIID: ptr(serverCI), TargetPercent: 99.9,
			Period: sla.PeriodYear, BudgetWarnPercent: 80},
	} {
		if err := repo.CreateTarget(ctx, tgt); err != nil {
			t.Errorf("%s: %v", tgt.Name, err)
		}
	}
}

func TestAvailabilityTargetCopiesScopeFromCI(t *testing.T) {
	repo := sla.NewMemoryAvailabilityRepository(fakeObjects{}, sla.NewMemoryRepository())
	ctx := context.Background()
	// client_id and site_id of the request are ignored for a CI target.
	tgt := &sla.AvailabilityTarget{OrganizationID: availOrg, Name: "core", ScopeKind: sla.ScopeCI, CIID: ptr(serverCI),
		ClientID: ptr(clientB), SiteID: ptr(otherClient), TargetPercent: 99.5, Period: sla.PeriodQuarter, BudgetWarnPercent: 80}
	if err := repo.CreateTarget(ctx, tgt); err != nil {
		t.Fatal(err)
	}
	if tgt.ClientID == nil || *tgt.ClientID != clientA || tgt.SiteID == nil || *tgt.SiteID != siteA {
		t.Errorf("scope of the target %v/%v, want the CI's %s/%s", tgt.ClientID, tgt.SiteID, clientA, siteA)
	}
	// A client target keeps its client and has no site.
	ct := &sla.AvailabilityTarget{OrganizationID: availOrg, Name: "kunde", ScopeKind: sla.ScopeClient, ClientID: ptr(clientB),
		SiteID: ptr(siteA), TargetPercent: 99, Period: sla.PeriodYear, BudgetWarnPercent: 80}
	if err := repo.CreateTarget(ctx, ct); err != nil {
		t.Fatal(err)
	}
	if *ct.ClientID != clientB || ct.SiteID != nil {
		t.Errorf("client target %v/%v", ct.ClientID, ct.SiteID)
	}
	// A principal of client B neither sees nor writes the target of client A.
	scope := database.OrgWideScope(availOrg, "")
	scope.Clients = database.ScopeIDs(clientB)
	bctx := database.ContextWithTenantScope(ctx, &scope)
	if _, err := repo.GetTarget(bctx, availOrg, tgt.ID); err != sla.ErrTargetNotFound {
		t.Errorf("target of client A for client B: %v, want not found", err)
	}
	other := &sla.AvailabilityTarget{OrganizationID: availOrg, Name: "x", ScopeKind: sla.ScopeService, CIID: ptr(serviceCI),
		TargetPercent: 99, Period: sla.PeriodMonth, BudgetWarnPercent: 80}
	if err := repo.CreateTarget(bctx, other); err != sla.ErrTargetOutOfScope {
		t.Errorf("target on a CI of client A by client B: %v, want out of scope", err)
	}
}

func TestAvailabilityTargetHandlerCRUD(t *testing.T) {
	slaRepo := sla.NewMemoryRepository()
	cal := &sla.BusinessCalendar{OrganizationID: availOrg, Name: "Bürozeit",
		WeeklyHours: []sla.Window{{Weekday: 1, Start: "08:00", End: "17:00"}}}
	if err := slaRepo.CreateCalendar(context.Background(), cal); err != nil {
		t.Fatal(err)
	}
	r := chi.NewRouter()
	sla.NewAvailabilityHandler(sla.NewMemoryAvailabilityRepository(fakeObjects{}, slaRepo)).RegisterRoutes(r)
	send := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: availOrg}))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
	const base = "/api/v1/monitoring/sla/targets"
	w := send(http.MethodPost, base, `{"name":"core","scope_kind":"ci","ci_id":"`+serverCI+`","target_percent":99.95,
		"period":"month","calendar_id":"`+cal.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	var created sla.AvailabilityTarget
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.BudgetWarnPercent != 80 || !created.Enabled || created.TargetPercent != 99.95 || *created.ClientID != clientA {
		t.Errorf("created %+v", created)
	}
	if w = send(http.MethodPost, base, `{"name":"core2","scope_kind":"ci","ci_id":"`+serverCI+`","target_percent":99,
		"period":"month"}`); w.Code != http.StatusConflict {
		t.Errorf("second target of the CI and period: %d, want 409", w.Code)
	}
	for body, field := range map[string]string{
		`{"name":"x","scope_kind":"ci","ci_id":"` + serverCI + `","target_percent":0,"period":"month"}`:      "target_percent",
		`{"name":"x","scope_kind":"ci","ci_id":"` + serverCI + `","period":"year"}`:                          "target_percent",
		`{"name":"x","scope_kind":"service","ci_id":"` + serverCI + `","target_percent":99,"period":"year"}`: "ci_id",
	} {
		if w = send(http.MethodPost, base, body); w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"field":"`+field+`"`) {
			t.Errorf("%s: %d %s, want 422 %s", body, w.Code, w.Body.String(), field)
		}
	}
	if w = send(http.MethodGet, base+"?scope_kind=ci&ci_id="+serverCI, ""); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), created.ID) {
		t.Errorf("list: %d %s", w.Code, w.Body.String())
	}
	if w = send(http.MethodGet, base+"?scope_kind=client", ""); w.Code != http.StatusOK || strings.Contains(w.Body.String(), created.ID) {
		t.Errorf("list of client targets: %d %s", w.Code, w.Body.String())
	}
	if w = send(http.MethodGet, base+"?scope_kind=port", ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("list with a bad kind: %d", w.Code)
	}
	w = send(http.MethodPatch, base+"/"+created.ID, `{"target_percent":99.5,"calendar_id":null,"enabled":false}`)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"target_percent":99.5`) ||
		!strings.Contains(w.Body.String(), `"calendar_id":null`) || !strings.Contains(w.Body.String(), `"enabled":false`) {
		t.Errorf("patch: %d %s", w.Code, w.Body.String())
	}
	if w = send(http.MethodPatch, base+"/"+created.ID, `{"budget_warn_percent":101}`); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"field":"budget_warn_percent"`) {
		t.Errorf("patch out of bounds: %d %s", w.Code, w.Body.String())
	}
	if w = send(http.MethodGet, base+"/"+created.ID, ""); w.Code != http.StatusOK {
		t.Errorf("get: %d", w.Code)
	}
	if w = send(http.MethodDelete, base+"/"+created.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("delete: %d", w.Code)
	}
	if w = send(http.MethodGet, base+"/"+created.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("get after delete: %d", w.Code)
	}
}

// siteCtx is a principal restricted to site A (all clients).
func siteCtx() context.Context {
	scope := database.OrgWideScope(availOrg, "")
	scope.Sites = database.ScopeIDs(siteA)
	return database.ContextWithTenantScope(context.Background(), &scope)
}

// TestAvailabilityTargetSitePrincipal: a site principal reads the targets
// of its site and those without site, but writes only those of its site,
// as the policies on PostgreSQL (WITH CHECK, restrictive UPDATE/DELETE).
func TestAvailabilityTargetSitePrincipal(t *testing.T) {
	repo := sla.NewMemoryAvailabilityRepository(fakeObjects{}, sla.NewMemoryRepository())
	org := context.Background()
	newTarget := func(kind string, ci, client *string, name string) *sla.AvailabilityTarget {
		return &sla.AvailabilityTarget{OrganizationID: availOrg, Name: name, ScopeKind: kind, CIID: ci, ClientID: client,
			TargetPercent: 99.9, Period: sla.PeriodMonth, BudgetWarnPercent: 80, Enabled: true}
	}
	noSite := newTarget(sla.ScopeService, ptr(serviceCI), nil, "service")
	if err := repo.CreateTarget(org, noSite); err != nil {
		t.Fatal(err)
	}
	site := siteCtx()
	if err := repo.CreateTarget(site, newTarget(sla.ScopeCI, ptr(serverCI), nil, "server")); err != nil {
		t.Errorf("target of a CI at the site: %v", err)
	}
	for name, tgt := range map[string]*sla.AvailabilityTarget{
		"client target":   newTarget(sla.ScopeClient, nil, ptr(clientA), "client"),
		"CI without site": newTarget(sla.ScopeCI, ptr(serviceCI), nil, "service-ci"),
	} {
		if err := repo.CreateTarget(site, tgt); !errors.Is(err, sla.ErrTargetOutOfScope) {
			t.Errorf("%s by the site principal: %v, want out of scope", name, err)
		}
	}
	if list, err := repo.ListTargets(site, availOrg, sla.AvailabilityFilter{}); err != nil || len(list) != 2 {
		t.Errorf("the site principal lists %+v (%v), want both", list, err)
	}
	if _, err := repo.UpdateTarget(site, availOrg, noSite.ID, &sla.UpdateAvailabilityTargetRequest{Enabled: ptr(false)}); !errors.Is(err, sla.ErrTargetNotFound) {
		t.Errorf("update of a target without site: %v, want not found", err)
	}
	if err := repo.DeleteTarget(site, availOrg, noSite.ID); !errors.Is(err, sla.ErrTargetNotFound) {
		t.Errorf("delete of a target without site: %v, want not found", err)
	}
}
