package sla_test

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
)

// The availability targets on PostgreSQL (ALR-06, WP-1048): the client
// scope of the caller (RLS and handler rules), the organizations apart, the
// policies on direct writes and the targets of a deleted CI.
//
// They run only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).

type availEnv struct {
	t    *testing.T
	f    *scopetest.Fixture
	repo *sla.PGAvailabilityRepository
}

func newAvailEnv(t *testing.T) *availEnv {
	t.Helper()
	f := scopetest.Seed(t, "39")
	return &availEnv{t: t, f: f, repo: sla.NewPGAvailabilityRepository(f.App)}
}

// serviceCI inserts a CI of the global type business_service.
func (e *availEnv) serviceCI(client, name string) string {
	e.t.Helper()
	id := e.f.ID()
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status)
		VALUES ($1, $2, $3, (SELECT id FROM ci_type WHERE key = 'business_service' AND organization_id IS NULL), $4, 'active')`,
		id, e.f.OrgA, client, name); err != nil {
		e.t.Fatal(err)
	}
	return id
}

func (e *availEnv) target(ctx context.Context, kind string, ciID, clientID *string, name string) (*sla.AvailabilityTarget, error) {
	t := &sla.AvailabilityTarget{OrganizationID: e.f.OrgA, Name: name, ScopeKind: kind, CIID: ciID, ClientID: clientID,
		TargetPercent: 99.95, Period: sla.PeriodMonth, BudgetWarnPercent: 80, Enabled: true}
	return t, e.repo.CreateTarget(ctx, t)
}

func TestAvailabilityTargetClientScope(t *testing.T) {
	e := newAvailEnv(t)
	org := e.f.OrgCtx(e.f.OrgA)
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "avail-c1")
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "avail-c2")
	own, err := e.target(org, sla.ScopeCI, &ci1, nil, "own")
	if err != nil {
		t.Fatal(err)
	}
	if own.ClientID == nil || *own.ClientID != e.f.Client1 {
		t.Errorf("client of the target %v, want the CI's", own.ClientID)
	}
	foreign, err := e.target(org, sla.ScopeCI, &ci2, nil, "foreign")
	if err != nil {
		t.Fatal(err)
	}
	orgWideCI := e.f.CI(t, e.f.OrgA, "", "avail-org")
	orgWide, err := e.target(org, sla.ScopeConnection, &orgWideCI, nil, "org-wide")
	if err != nil {
		t.Fatal(err)
	}
	c1 := e.f.ClientCtx(e.f.Client1)
	list, err := e.repo.ListTargets(c1, e.f.OrgA, sla.AvailabilityFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != own.ID {
		t.Errorf("client 1 lists %+v, want only its target", list)
	}
	for _, id := range []string{foreign.ID, orgWide.ID} {
		if _, err = e.repo.GetTarget(c1, e.f.OrgA, id); !errors.Is(err, sla.ErrTargetNotFound) {
			t.Errorf("target %s for client 1: %v, want not found", id, err)
		}
		if _, err = e.repo.UpdateTarget(c1, e.f.OrgA, id, &sla.UpdateAvailabilityTargetRequest{Name: ptr("x")}); !errors.Is(err, sla.ErrTargetNotFound) {
			t.Errorf("update of %s by client 1: %v, want not found", id, err)
		}
		if err = e.repo.DeleteTarget(c1, e.f.OrgA, id); !errors.Is(err, sla.ErrTargetNotFound) {
			t.Errorf("delete of %s by client 1: %v, want not found", id, err)
		}
	}
	// Writing on a CI or client outside the scope is out of scope (403),
	// an unknown CI a field error (422).
	if _, err = e.target(c1, sla.ScopeCI, &ci2, nil, "x"); !errors.Is(err, sla.ErrTargetOutOfScope) {
		t.Errorf("target on a CI of client 2 by client 1: %v, want out of scope", err)
	}
	if _, err = e.target(c1, sla.ScopeClient, nil, &e.f.Client2, "x"); !errors.Is(err, sla.ErrTargetOutOfScope) {
		t.Errorf("target of client 2 by client 1: %v, want out of scope", err)
	}
	unknown := e.f.ID()
	_, err = e.target(c1, sla.ScopeCI, &unknown, nil, "x")
	var fe *api.FieldError
	if !errors.As(err, &fe) || fe.Field != "ci_id" || fe.Status != http.StatusUnprocessableEntity {
		t.Errorf("unknown CI: %v, want 422 ci_id", err)
	}
	// Client 1 writes the target of its client and of a service of it.
	if _, err = e.target(c1, sla.ScopeClient, nil, &e.f.Client1, "kunde"); err != nil {
		t.Errorf("client target by client 1: %v", err)
	}
	svc := e.serviceCI(e.f.Client1, "avail-svc")
	if _, err = e.target(c1, sla.ScopeService, &svc, nil, "service"); err != nil {
		t.Errorf("service target: %v", err)
	}
	if _, err = e.target(c1, sla.ScopeConnection, &svc, nil, "conn"); !errors.As(err, &fe) || fe.Field != "ci_id" {
		t.Errorf("connection on a service: %v, want 422 ci_id", err)
	}
	if _, err = e.target(c1, sla.ScopeService, &ci1, nil, "svc-on-server"); !errors.As(err, &fe) || fe.Field != "ci_id" {
		t.Errorf("service on a server: %v, want 422 ci_id", err)
	}
	// A second target of the CI and period is a conflict.
	if _, err = e.target(org, sla.ScopeCI, &ci1, nil, "again"); !errors.Is(err, sla.ErrTargetExists) {
		t.Errorf("second target: %v, want exists", err)
	}
}

func TestAvailabilityTargetOrgIsolation(t *testing.T) {
	e := newAvailEnv(t)
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "avail-iso")
	own, err := e.target(e.f.OrgCtx(e.f.OrgA), sla.ScopeCI, &ci1, nil, "iso")
	if err != nil {
		t.Fatal(err)
	}
	b := e.f.OrgCtx(e.f.OrgB)
	if _, err := e.repo.GetTarget(b, e.f.OrgB, own.ID); !errors.Is(err, sla.ErrTargetNotFound) {
		t.Errorf("target of A in B: %v", err)
	}
	if list, err := e.repo.ListTargets(b, e.f.OrgB, sla.AvailabilityFilter{}); err != nil || len(list) != 0 {
		t.Errorf("B lists %+v (%v)", list, err)
	}
	// A target in B on the CI of A fails: the CI is not B's.
	t2 := &sla.AvailabilityTarget{OrganizationID: e.f.OrgB, Name: "x", ScopeKind: sla.ScopeCI, CIID: &ci1, TargetPercent: 99,
		Period: sla.PeriodYear, BudgetWarnPercent: 80}
	var fe *api.FieldError
	if err := e.repo.CreateTarget(b, t2); !errors.As(err, &fe) || fe.Field != "ci_id" {
		t.Errorf("target in B on a CI of A: %v, want 422 ci_id", err)
	}
}

func TestAvailabilityTargetRLS(t *testing.T) {
	e := newAvailEnv(t)
	ci2 := e.f.CI(t, e.f.OrgA, e.f.Client2, "avail-rls")
	// Directly in the transaction of a client-1 principal the policies
	// refuse a row of client 2 and an organization-wide one.
	for name, client := range map[string]any{"client 2": e.f.Client2, "org-wide": nil} {
		err := database.WithRequestTenant(e.f.ClientCtx(e.f.Client1), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `INSERT INTO availability_target (organization_id, client_id, name, scope_kind, ci_id, target_percent, period)
				VALUES ($1, $2, 'rls', 'ci', $3, 99, 'month')`, e.f.OrgA, client, ci2)
			return err
		})
		if err == nil {
			t.Errorf("%s: insert by client 1 accepted", name)
		}
	}
}

func TestAvailabilityTargetDeletedWithCI(t *testing.T) {
	e := newAvailEnv(t)
	ci1 := e.f.CI(t, e.f.OrgA, e.f.Client1, "avail-del")
	tgt, err := e.target(e.f.OrgCtx(e.f.OrgA), sla.ScopeCI, &ci1, nil, "del")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM ci WHERE id = $1`, ci1); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM availability_target WHERE id = $1`, tgt.ID).Scan(&n); err != nil || n != 0 {
		t.Errorf("targets of the deleted CI: %d (%v), want 0", n, err)
	}
}

// TestAvailabilityTargetFollowsCIMove: client and site of a target are
// those of its CI in the database too: a direct INSERT cannot put the
// target into another client, and a CI moved to client 2 takes its target
// along (client 1 no longer sees it, client 2 does).
func TestAvailabilityTargetFollowsCIMove(t *testing.T) {
	e := newAvailEnv(t)
	org := e.f.OrgCtx(e.f.OrgA)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "avail-move")
	tgt, err := e.target(org, sla.ScopeCI, &ci, nil, "move")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.f.Admin.Exec(context.Background(), `UPDATE ci SET client_id = $2 WHERE id = $1`, ci, e.f.Client2); err != nil {
		t.Fatal(err)
	}
	if _, err = e.repo.GetTarget(e.f.ClientCtx(e.f.Client1), e.f.OrgA, tgt.ID); !errors.Is(err, sla.ErrTargetNotFound) {
		t.Errorf("client 1 after the move: %v, want not found", err)
	}
	got, err := e.repo.GetTarget(e.f.ClientCtx(e.f.Client2), e.f.OrgA, tgt.ID)
	if err != nil || got.ClientID == nil || *got.ClientID != e.f.Client2 {
		t.Errorf("client 2 after the move: %+v %v, want the target of client 2", got, err)
	}
	// A direct INSERT with another client than the CI's takes the CI's.
	other := e.f.CI(t, e.f.OrgA, e.f.Client2, "avail-direct")
	var client string
	if err := database.WithRequestTenant(org, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO availability_target (organization_id, client_id, name, scope_kind, ci_id, target_percent, period)
			VALUES ($1, $2, 'direct', 'ci', $3, 99, 'month') RETURNING client_id::text`, e.f.OrgA, e.f.Client1, other).Scan(&client)
	}); err != nil {
		t.Fatal(err)
	}
	if client != e.f.Client2 {
		t.Errorf("direct insert stored client %s, want the CI's %s", client, e.f.Client2)
	}
}

// TestAvailabilityTargetOfSoftDeletedCIChangeable: the target of a
// soft-deleted CI can still be disabled or renamed.
func TestAvailabilityTargetOfSoftDeletedCIChangeable(t *testing.T) {
	e := newAvailEnv(t)
	org := e.f.OrgCtx(e.f.OrgA)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "avail-soft")
	tgt, err := e.target(org, sla.ScopeCI, &ci, nil, "soft")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.f.Admin.Exec(context.Background(), `UPDATE ci SET deleted_at = now() WHERE id = $1`, ci); err != nil {
		t.Fatal(err)
	}
	got, err := e.repo.UpdateTarget(org, e.f.OrgA, tgt.ID, &sla.UpdateAvailabilityTargetRequest{Enabled: ptr(false), Name: ptr("soft off")})
	if err != nil || got.Enabled || got.Name != "soft off" {
		t.Errorf("PATCH of the target of a soft-deleted CI: %+v %v, want disabled", got, err)
	}
}

// TestAvailabilityTargetSitePrincipalOnPostgres: as in memory, a site
// principal reads the targets without site but writes only those of its
// site.
func TestAvailabilityTargetSitePrincipalOnPostgres(t *testing.T) {
	e := newAvailEnv(t)
	org := e.f.OrgCtx(e.f.OrgA)
	// The site of a CI follows its location (trg_ci_derive_location).
	var site string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'avail-site')
		RETURNING id::text`, e.f.OrgA, e.f.Client1).Scan(&site); err != nil {
		t.Fatal(err)
	}
	atSite := e.f.CI(t, e.f.OrgA, e.f.Client1, "avail-at-site")
	var derived string
	if err := e.f.Admin.QueryRow(context.Background(), `UPDATE ci SET location_id = $2 WHERE id = $1 RETURNING COALESCE(site_id::text, '')`,
		atSite, site).Scan(&derived); err != nil || derived != site {
		t.Fatalf("place the CI at the site: %q %v", derived, err)
	}
	noSiteCI := e.f.CI(t, e.f.OrgA, e.f.Client1, "avail-no-site")
	noSite, err := e.target(org, sla.ScopeCI, &noSiteCI, nil, "no-site")
	if err != nil {
		t.Fatal(err)
	}
	scope := database.OrgWideScope(e.f.OrgA, e.f.User)
	scope.Sites = database.ScopeIDs(site)
	sctx := database.ContextWithTenantScope(context.Background(), &scope)
	if _, err := e.target(sctx, sla.ScopeCI, &atSite, nil, "at-site"); err != nil {
		t.Errorf("target of a CI at the site: %v", err)
	}
	if _, err := e.target(sctx, sla.ScopeClient, nil, &e.f.Client1, "client"); !errors.Is(err, sla.ErrTargetOutOfScope) {
		t.Errorf("client target by the site principal: %v, want out of scope", err)
	}
	if list, err := e.repo.ListTargets(sctx, e.f.OrgA, sla.AvailabilityFilter{}); err != nil || len(list) != 2 {
		t.Errorf("the site principal lists %+v (%v), want both", list, err)
	}
	if _, err := e.repo.UpdateTarget(sctx, e.f.OrgA, noSite.ID, &sla.UpdateAvailabilityTargetRequest{Enabled: ptr(false)}); !errors.Is(err, sla.ErrTargetNotFound) {
		t.Errorf("update of a target without site: %v, want not found", err)
	}
	if err := e.repo.DeleteTarget(sctx, e.f.OrgA, noSite.ID); !errors.Is(err, sla.ErrTargetNotFound) {
		t.Errorf("delete of a target without site: %v, want not found", err)
	}
}
