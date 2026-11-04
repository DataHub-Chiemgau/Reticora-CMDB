// CI owner (CI-07, CI-01, TEN-05, DB-05, TLC-04; WP-280, E-11, E-124):
// owner_user_id and owner_team_id of a CI against PostgreSQL.
//
//   - TestCIOwnerWritePath: POST and PATCH /cis set the owner, "" and null
//     clear it; the DTO carries it; one ci_change row per owner field (UUIDs
//     only), no override in ci_field_value, audit ci.updated and the event
//     ci.changed with changed_fields; an unknown user or team, one of the
//     other organization and an ID that is no UUID are 422 on the field; a
//     stale If-Match after a concurrent owner change is 409 with the field.
//   - TestCIOwnerTeamScope: a team-scoped principal (TEN-05, E-11) reads the
//     CIs and assets of its team and those without a team, writes only
//     those of its team, creates CIs only with a team of its scope (403
//     otherwise) and cannot hand a CI or an asset to another team.
//   - TestCIOwnerMirrorsAsset: a linked CI has the owner of its asset
//     (DB-05): creating with asset_id and linking per PATCH take it, an owner
//     change of the asset reaches every linked CI, a direct write of
//     another owner is 422 ci_owner_from_asset, unlinking keeps the owner,
//     the asset facts show the same owner; deleting a team clears it at the
//     asset and its CIs; an owner change of the asset by a principal of
//     client 1 while a CI of the asset belongs to client 2 is 42501 and no
//     CI diverges.
//   - TestCIOwnerUserDeletion: TLC-04, the owner of an open CI cannot be
//     deleted (ErrUserOwnsOpenObjects), the owner of a decommissioned CI
//     neither (ErrUserReferenced, ci_owner_user_fkey).
//   - TestCIOwnerLifecycleRegression: status, lifecycle, health and owner
//     stay separate fields (WP-080, WP-122): lifecycle_state is omitted
//     without a model, a software model takes a state, a physical model or
//     none is refused (422 on lifecycle_state for a type change),
//     filter[lifecycle_state] works and health is read-only.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -run 'TestCIOwner' ./internal/ci/
package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// ownerFixture is the scenario of the owner tests: the server type, users
// and teams of both organizations.
type ownerFixture struct {
	f                      *scopetest.Fixture
	bg                     context.Context
	serverType             string
	userA, userA2, userB   string
	teamA, teamA2, teamB   string
	repo                   *ci.PGRepository
	api                    *tagAPI
	orgScope, orgTenantCtx context.Context
}

func newOwnerFixture(t *testing.T) *ownerFixture {
	t.Helper()
	f := scopetest.Seed(t, "cf")
	o := &ownerFixture{f: f, bg: context.Background()}
	o.serverType = o.scalar(t, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`)
	o.userA, o.userA2, o.userB = f.AppUser(t, f.OrgA, "owner-a"), f.AppUser(t, f.OrgA, "owner-a2"), f.AppUser(t, f.OrgB, "owner-b")
	o.teamA = o.scalar(t, `INSERT INTO team (organization_id, name) VALUES ($1, 'owner team a') RETURNING id::text`, f.OrgA)
	o.teamA2 = o.scalar(t, `INSERT INTO team (organization_id, name) VALUES ($1, 'owner team a2') RETURNING id::text`, f.OrgA)
	o.teamB = o.scalar(t, `INSERT INTO team (organization_id, name) VALUES ($1, 'owner team b') RETURNING id::text`, f.OrgB)
	o.orgScope = f.OrgCtx(f.OrgA)
	o.orgTenantCtx = tenant.WithTenant(o.orgScope, tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	o.repo = ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	o.api = newTagAPI(t, o.orgTenantCtx, o.repo)
	return o
}

func (o *ownerFixture) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	var out string
	if err := o.f.Admin.QueryRow(o.bg, query, args...).Scan(&out); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return out
}

func (o *ownerFixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := o.f.Admin.Exec(o.bg, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// owner returns "user|team" of a CI or an asset as stored.
func (o *ownerFixture) owner(t *testing.T, table, id string) string {
	t.Helper()
	return o.scalar(t, `SELECT COALESCE(owner_user_id::text, '') || '|' || COALESCE(owner_team_id::text, '') FROM `+table+` WHERE id = $1`, id)
}

// teamCtx is the request context of a principal of organization A
// restricted to the teams.
func (o *ownerFixture) teamCtx(teams ...string) context.Context {
	scope := database.OrgWideScope(o.f.OrgA, o.f.User)
	scope.Teams = database.ScopeIDs(teams...)
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: o.f.OrgA, UserID: o.f.User})
}

// inScope runs sql in a transaction of the scope of ctx.
func (o *ownerFixture) inScope(ctx context.Context, sql string, args ...any) (int64, error) {
	var n int64
	err := database.WithRequestTenant(ctx, o.f.App, o.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		n = tag.RowsAffected()
		return err
	})
	return n, err
}

// createCI creates a CI through the API and returns it.
func (o *ownerFixture) createCI(t *testing.T, a *tagAPI, body map[string]any, status int) ci.Item {
	t.Helper()
	if _, ok := body["ci_type_id"]; !ok {
		body["ci_type_id"] = o.serverType
	}
	raw, _ := json.Marshal(body)
	var item ci.Item
	w := a.call(http.MethodPost, "/api/v1/cis", string(raw))
	if w.Code != status {
		t.Fatalf("POST /cis %s: %d %s, want %d", raw, w.Code, w.Body.String(), status)
	}
	if status == http.StatusCreated {
		if err := json.Unmarshal(w.Body.Bytes(), &item); err != nil {
			t.Fatal(err)
		}
	}
	return item
}

// patch sends a PATCH /cis/{id} and returns the recorder.
func patchCI(a *tagAPI, id, body string, header ...string) (int, string) {
	w := a.call(http.MethodPatch, "/api/v1/cis/"+id, body, header...)
	return w.Code, w.Body.String()
}

func violationOn(t *testing.T, code int, body, field string) bool {
	t.Helper()
	if code != http.StatusUnprocessableEntity {
		return false
	}
	var problem struct {
		Violations []struct {
			Field string `json:"field"`
		} `json:"violations"`
		Field string `json:"field"`
	}
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		return false
	}
	if problem.Field == field {
		return true
	}
	for _, v := range problem.Violations {
		if v.Field == field {
			return true
		}
	}
	return false
}

func TestCIOwnerWritePath(t *testing.T) {
	o := newOwnerFixture(t)
	f, a := o.f, o.api

	created := o.createCI(t, a, map[string]any{"name": "owner-write", "owner_user_id": o.userA, "owner_team_id": o.teamA}, http.StatusCreated)
	if created.OwnerUserID != o.userA || created.OwnerTeamID != o.teamA {
		t.Fatalf("created owner: %q %q", created.OwnerUserID, created.OwnerTeamID)
	}
	if got := o.owner(t, "ci", created.ID); got != o.userA+"|"+o.teamA {
		t.Errorf("stored owner %s", got)
	}

	// Change the user, clear the team ("") and read the DTO.
	code, body := patchCI(a, created.ID, `{"owner_user_id":"`+o.userA2+`","owner_team_id":""}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH owner: %d %s", code, body)
	}
	if !strings.Contains(body, `"owner_user_id":"`+o.userA2+`"`) || strings.Contains(body, `"owner_team_id"`) {
		t.Errorf("DTO after the PATCH: %s", body)
	}
	for field, want := range map[string][2]string{"owner_user_id": {o.userA, o.userA2}, "owner_team_id": {o.teamA, ""}} {
		var n int
		var oldValue, newValue string
		if err := f.Admin.QueryRow(o.bg, `SELECT count(*), COALESCE(min(old_value #>> '{}'), ''), COALESCE(min(new_value #>> '{}'), '')
			FROM ci_change WHERE ci_id = $1 AND field_name = $2`, created.ID, field).Scan(&n, &oldValue, &newValue); err != nil {
			t.Fatal(err)
		}
		if n != 1 || oldValue != want[0] || newValue != want[1] {
			t.Errorf("ci_change %s: %d rows, %q -> %q; want one row %q -> %q", field, n, oldValue, newValue, want[0], want[1])
		}
	}
	if n := countRows(t, f, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND field_name LIKE 'owner_%'`, created.ID); n != 0 {
		t.Errorf("a manual owner change created %d overrides", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.updated'
		AND changes -> 'after' ->> 'owner_user_id' = $2`, created.ID, o.userA2); n != 1 {
		t.Errorf("audit ci.updated with the owner: %d", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM event_outbox WHERE entity_id = $1::text AND type = 'ci.changed'
		AND data -> 'changed_fields' @> '["owner_user_id", "owner_team_id"]'::jsonb`, created.ID); n != 1 {
		t.Errorf("event ci.changed with the owner fields: %d", n)
	}
	// null clears the user (RFC 7396).
	if code, body = patchCI(a, created.ID, `{"owner_user_id":null}`); code != http.StatusOK || strings.Contains(body, `"owner_user_id"`) {
		t.Errorf("PATCH owner_user_id null: %d %s", code, body)
	}

	// Unknown, foreign and malformed references: 422 on the field.
	unknown := "00000000-0000-4000-8000-0000000000cf"
	for _, c := range []struct{ body, field string }{
		{`{"owner_user_id":"` + unknown + `"}`, "owner_user_id"},
		{`{"owner_user_id":"` + o.userB + `"}`, "owner_user_id"},
		{`{"owner_user_id":"not-a-uuid"}`, "owner_user_id"},
		{`{"owner_team_id":"` + unknown + `"}`, "owner_team_id"},
		{`{"owner_team_id":"` + o.teamB + `"}`, "owner_team_id"},
	} {
		if code, body = patchCI(a, created.ID, c.body); !violationOn(t, code, body, c.field) {
			t.Errorf("PATCH %s: %d %s, want 422 on %s", c.body, code, body, c.field)
		}
		var item map[string]any
		_ = json.Unmarshal([]byte(c.body), &item)
		item["name"] = "owner-refused"
		o.createCI(t, a, item, http.StatusUnprocessableEntity)
	}
	if n := countRows(t, f, `SELECT count(*) FROM ci WHERE organization_id = $1 AND name = 'owner-refused'`, f.OrgA); n != 0 {
		t.Errorf("%d refused CIs were stored", n)
	}

	// A stale If-Match after a concurrent owner change: 409 naming it.
	version := o.scalar(t, `SELECT version::text FROM ci WHERE id = $1`, created.ID)
	if code, body = patchCI(a, created.ID, `{"owner_team_id":"`+o.teamA+`"}`, "If-Match", `"`+version+`"`); code != http.StatusOK {
		t.Fatalf("PATCH with the current If-Match: %d %s", code, body)
	}
	code, body = patchCI(a, created.ID, `{"owner_team_id":"`+o.teamA2+`"}`, "If-Match", `"`+version+`"`)
	var conflict struct {
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal([]byte(body), &conflict); code != http.StatusConflict || err != nil || !slices.Contains(conflict.Fields, "owner_team_id") {
		t.Errorf("stale If-Match on owner_team_id: %d %s, want 409 with the field", code, body)
	}
	if got := o.owner(t, "ci", created.ID); got != "|"+o.teamA {
		t.Errorf("owner after the refused PATCH: %s", got)
	}
}

func TestCIOwnerTeamScope(t *testing.T) {
	o := newOwnerFixture(t)
	f := o.f
	own := f.CI(t, f.OrgA, "", "team-own")
	none := f.CI(t, f.OrgA, "", "team-none")
	other := f.CI(t, f.OrgA, "", "team-other")
	o.exec(t, `UPDATE ci SET owner_team_id = $2 WHERE id = $1`, own, o.teamA)
	o.exec(t, `UPDATE ci SET owner_team_id = $2 WHERE id = $1`, other, o.teamA2)
	ctx := o.teamCtx(o.teamA)
	a := newTagAPI(t, ctx, o.repo)

	// Reading: the own team and no team.
	for id, want := range map[string]int{own: http.StatusOK, none: http.StatusOK, other: http.StatusNotFound} {
		if w := a.call(http.MethodGet, "/api/v1/cis/"+id, ""); w.Code != want {
			t.Errorf("GET %s as team: %d, want %d", id, w.Code, want)
		}
	}
	w := a.call(http.MethodGet, "/api/v1/cis?limit=200", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), own) || !strings.Contains(w.Body.String(), none) || strings.Contains(w.Body.String(), other) {
		t.Errorf("GET /cis as team: %d %s", w.Code, w.Body.String())
	}
	// Writing: only the own team.
	for id, want := range map[string]int{own: http.StatusOK, none: http.StatusNotFound, other: http.StatusNotFound} {
		if code, body := patchCI(a, id, `{"model":"team-scope"}`); code != want {
			t.Errorf("PATCH %s as team: %d %s, want %d", id, code, body, want)
		}
	}
	if code, body := patchCI(a, own, `{"owner_team_id":"`+o.teamA2+`"}`); code != http.StatusForbidden {
		t.Errorf("PATCH to another team as team: %d %s, want 403", code, body)
	}
	if got := o.owner(t, "ci", own); got != "|"+o.teamA {
		t.Errorf("owner after the refused hand-over: %s", got)
	}
	// Creating: only with a team of the scope.
	o.createCI(t, a, map[string]any{"name": "team-new-none"}, http.StatusForbidden)
	o.createCI(t, a, map[string]any{"name": "team-new-other", "owner_team_id": o.teamA2}, http.StatusForbidden)
	created := o.createCI(t, a, map[string]any{"name": "team-new-own", "owner_team_id": o.teamA}, http.StatusCreated)
	if created.OwnerTeamID != o.teamA {
		t.Errorf("created as team: %+v", created)
	}
	if n := countRows(t, f, `SELECT count(*) FROM ci WHERE organization_id = $1 AND name IN ('team-new-none', 'team-new-other')`, f.OrgA); n != 0 {
		t.Errorf("%d refused CIs were stored", n)
	}

	// Assets: the same predicate on asset.owner_team_id.
	assetOwn, assetNone, assetOther := f.Asset(t, f.OrgA, "", "TEAM-OWN"), f.Asset(t, f.OrgA, "", "TEAM-NONE"), f.Asset(t, f.OrgA, "", "TEAM-OTHER")
	o.exec(t, `UPDATE asset SET owner_team_id = $2 WHERE id = $1`, assetOwn, o.teamA)
	o.exec(t, `UPDATE asset SET owner_team_id = $2 WHERE id = $1`, assetOther, o.teamA2)
	var visible []string
	if err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM asset WHERE id = ANY($1::uuid[]) ORDER BY id`, []string{assetOwn, assetNone, assetOther})
		if err != nil {
			return err
		}
		visible, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	}); err != nil {
		t.Fatal(err)
	}
	wantVisible := []string{assetOwn, assetNone}
	slices.Sort(wantVisible)
	if !slices.Equal(visible, wantVisible) {
		t.Errorf("assets visible to the team: %v, want %v", visible, wantVisible)
	}
	for id, want := range map[string]int64{assetOwn: 1, assetNone: 0, assetOther: 0} {
		if n, err := o.inScope(ctx, `UPDATE asset SET notes = 'team-scope' WHERE id = $1`, id); err != nil || n != want {
			t.Errorf("UPDATE asset %s as team: %d rows, %v; want %d", id, n, err, want)
		}
	}
	insufficient := func(err error) bool {
		var pgErr *pgconn.PgError
		return errors.As(err, &pgErr) && pgErr.Code == "42501"
	}
	if _, err := o.inScope(ctx, `UPDATE asset SET owner_team_id = $2 WHERE id = $1`, assetOwn, o.teamA2); !insufficient(err) {
		t.Errorf("hand an asset to another team as team: %v, want 42501", err)
	}
	if _, err := o.inScope(ctx, `INSERT INTO asset (organization_id, asset_tag, name) VALUES ($1, 'TEAM-NEW', 'TEAM-NEW')`, f.OrgA); !insufficient(err) {
		t.Errorf("insert an asset without team as team: %v, want 42501", err)
	}
	if _, err := o.inScope(ctx, `INSERT INTO asset (organization_id, asset_tag, name, owner_team_id) VALUES ($1, 'TEAM-NEW', 'TEAM-NEW', $2)`,
		f.OrgA, o.teamA); err != nil {
		t.Errorf("insert an asset of the team as team: %v", err)
	}
}

func TestCIOwnerMirrorsAsset(t *testing.T) {
	o := newOwnerFixture(t)
	f, a := o.f, o.api
	asset := f.Asset(t, f.OrgA, "", "OWNER-MIRROR")
	o.exec(t, `UPDATE asset SET owner_user_id = $2, owner_team_id = $3 WHERE id = $1`, asset, o.userA, o.teamA)
	assetOwner := o.userA + "|" + o.teamA

	// Created with the asset: the asset's owner.
	linked := o.createCI(t, a, map[string]any{"name": "mirror-linked", "asset_id": asset}, http.StatusCreated)
	if linked.OwnerUserID != o.userA || linked.OwnerTeamID != o.teamA || linked.Asset == nil ||
		linked.Asset.OwnerUserID != linked.OwnerUserID || linked.Asset.OwnerTeamID != linked.OwnerTeamID {
		t.Errorf("CI created with the asset: owner %q %q, facts %+v", linked.OwnerUserID, linked.OwnerTeamID, linked.Asset)
	}
	same := o.createCI(t, a, map[string]any{"name": "mirror-same", "asset_id": asset, "owner_user_id": o.userA}, http.StatusCreated)
	if got := o.owner(t, "ci", same.ID); got != assetOwner {
		t.Errorf("CI created with the asset and its owner: %s", got)
	}
	// Another owner than the asset's: 422 on the field.
	for field, value := range map[string]string{"owner_user_id": o.userA2, "owner_team_id": o.teamA2} {
		raw, _ := json.Marshal(map[string]any{"ci_type_id": o.serverType, "name": "mirror-refused", "asset_id": asset, field: value})
		if w := a.call(http.MethodPost, "/api/v1/cis", string(raw)); !violationOn(t, w.Code, w.Body.String(), field) {
			t.Errorf("POST with the asset and another %s: %d %s, want 422 on the field", field, w.Code, w.Body.String())
		}
		if code, body := patchCI(a, linked.ID, `{"`+field+`":"`+value+`"}`); !violationOn(t, code, body, field) ||
			!strings.Contains(body, "the owner of a CI linked to an asset") {
			t.Errorf("PATCH %s of a linked CI: %d %s, want 422 on the field", field, code, body)
		}
	}
	if got := o.owner(t, "ci", linked.ID); got != assetOwner {
		t.Errorf("owner of the linked CI after the refused writes: %s", got)
	}

	// Linking per PATCH takes the asset's owner.
	loose := o.createCI(t, a, map[string]any{"name": "mirror-loose", "owner_user_id": o.userA2}, http.StatusCreated)
	if code, body := patchCI(a, loose.ID, `{"asset_id":"`+asset+`"}`); code != http.StatusOK ||
		!strings.Contains(body, `"owner_user_id":"`+o.userA+`"`) || !strings.Contains(body, `"owner_team_id":"`+o.teamA+`"`) {
		t.Errorf("PATCH asset_id: %d %s", code, body)
	}

	// An owner change of the asset reaches every linked CI.
	if _, err := o.inScope(o.orgScope, `UPDATE asset SET owner_user_id = $2, owner_team_id = $3 WHERE id = $1`, asset, o.userA2, o.teamA2); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{linked.ID, same.ID, loose.ID} {
		if got := o.owner(t, "ci", id); got != o.userA2+"|"+o.teamA2 {
			t.Errorf("CI %s after the owner change of the asset: %s", id, got)
		}
	}
	var facts ci.Item
	if w := a.call(http.MethodGet, "/api/v1/cis/"+linked.ID, ""); w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &facts) != nil ||
		facts.Asset == nil || facts.Asset.OwnerUserID != facts.OwnerUserID || facts.Asset.OwnerTeamID != facts.OwnerTeamID {
		t.Errorf("GET of the linked CI: %d %s", w.Code, w.Body.String())
	}

	// Unlinking keeps the owner mirrored last.
	if code, body := patchCI(a, loose.ID, `{"asset_id":""}`); code != http.StatusOK {
		t.Fatalf("unlink: %d %s", code, body)
	}
	if got := o.owner(t, "ci", loose.ID); got != o.userA2+"|"+o.teamA2 {
		t.Errorf("owner after unlinking: %s", got)
	}

	// Deleting the team that owns the asset clears it at the asset and its
	// CIs without an error, also at an unlinked CI.
	if _, err := o.inScope(o.orgScope, `DELETE FROM team WHERE id = $1`, o.teamA2); err != nil {
		t.Fatalf("delete the owning team: %v", err)
	}
	if got := o.owner(t, "asset", asset); got != o.userA2+"|" {
		t.Errorf("asset after the team was deleted: %s", got)
	}
	for _, id := range []string{linked.ID, same.ID, loose.ID} {
		if got := o.owner(t, "ci", id); got != o.userA2+"|" {
			t.Errorf("CI %s after the team was deleted: %s", id, got)
		}
	}

	// An asset of client 1 with a CI of client 2 (written past the client
	// check, as in rack/placement_scope_integration_test.go): a principal
	// of client 1 cannot change its owner (42501), no CI diverges.
	z1, z2 := f.CI(t, f.OrgA, f.Client1, "mirror-z1"), f.CI(t, f.OrgA, f.Client2, "mirror-z2")
	shared := f.Asset(t, f.OrgA, "", "OWNER-SHARED")
	o.exec(t, `UPDATE ci SET asset_id = $2 WHERE id = ANY($1::uuid[])`, []string{z1, z2}, shared)
	if err := pgx.BeginFunc(o.bg, f.Admin, func(tx pgx.Tx) error {
		for _, stmt := range []string{`ALTER TABLE asset DISABLE TRIGGER trg_asset_ci_client`,
			`UPDATE asset SET client_id = '` + f.Client1 + `', owner_user_id = '` + o.userA + `' WHERE id = '` + shared + `'`,
			`ALTER TABLE asset ENABLE TRIGGER trg_asset_ci_client`} {
			if _, err := tx.Exec(o.bg, stmt); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{z1, z2} {
		if got := o.owner(t, "ci", id); got != o.userA+"|" {
			t.Fatalf("CI %s of the shared asset: %s", id, got)
		}
	}
	_, err := o.inScope(f.ClientCtx(f.Client1), `UPDATE asset SET owner_user_id = $2 WHERE id = $1`, shared, o.userA2)
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" || pgErr.ConstraintName != "asset_owner_ci_scope" {
		t.Errorf("owner change by a principal of client 1: %v, want 42501 asset_owner_ci_scope", err)
	}
	if got := o.owner(t, "asset", shared); got != o.userA+"|" {
		t.Errorf("asset after the refused owner change: %s", got)
	}
	for _, id := range []string{z1, z2} {
		if got := o.owner(t, "ci", id); got != o.userA+"|" {
			t.Errorf("CI %s after the refused owner change: %s", id, got)
		}
	}
}

func TestCIOwnerUserDeletion(t *testing.T) {
	o := newOwnerFixture(t)
	f := o.f
	users := user.NewPGRepository(f.App)
	open := f.CI(t, f.OrgA, "", "owned-open")
	o.exec(t, `UPDATE ci SET owner_user_id = $2 WHERE id = $1`, open, o.userA)
	if err := users.DeleteUser(o.orgScope, f.OrgA, o.userA); !errors.Is(err, user.ErrUserOwnsOpenObjects) {
		t.Errorf("delete the owner of an open CI: %v, want ErrUserOwnsOpenObjects", err)
	}
	retired := f.CI(t, f.OrgA, "", "owned-retired")
	o.exec(t, `UPDATE ci SET owner_user_id = $2, status = 'decommissioned' WHERE id = $1`, retired, o.userA2)
	if err := users.DeleteUser(o.orgScope, f.OrgA, o.userA2); !errors.Is(err, user.ErrUserReferenced) {
		t.Errorf("delete the owner of a decommissioned CI: %v, want ErrUserReferenced", err)
	}
	if n := countRows(t, f, `SELECT count(*) FROM app_user WHERE id = ANY($1::uuid[])`, []string{o.userA, o.userA2}); n != 2 {
		t.Errorf("%d of 2 owners kept", n)
	}
	// Without the CI the user can go.
	o.exec(t, `UPDATE ci SET owner_user_id = NULL WHERE id = $1`, retired)
	if err := users.DeleteUser(o.orgScope, f.OrgA, o.userA2); err != nil {
		t.Errorf("delete a user without CIs: %v", err)
	}
}

func TestCIOwnerLifecycleRegression(t *testing.T) {
	o := newOwnerFixture(t)
	f, a := o.f, o.api
	softwareType := o.scalar(t, `INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
		VALUES ($1, 'owner-lc-sw', 'Owner software', '00000000-0000-0000-0000-000000000102') RETURNING id::text`, f.OrgA)
	physicalType := o.scalar(t, `INSERT INTO ci_type (organization_id, key, name, lifecycle_model_id)
		VALUES ($1, 'owner-lc-phys', 'Owner physical', '00000000-0000-0000-0000-000000000101') RETURNING id::text`, f.OrgA)
	plain := o.createCI(t, a, map[string]any{"name": "lc-plain", "owner_user_id": o.userA}, http.StatusCreated)
	software := o.createCI(t, a, map[string]any{"name": "lc-software", "ci_type_id": softwareType, "owner_team_id": o.teamA}, http.StatusCreated)
	physical := o.createCI(t, a, map[string]any{"name": "lc-physical", "ci_type_id": physicalType}, http.StatusCreated)

	// Without a model the DTO has no lifecycle_state; status, health and
	// owner are their own members.
	w := a.call(http.MethodGet, "/api/v1/cis/"+plain.ID, "")
	var members map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &members); err != nil || w.Code != http.StatusOK {
		t.Fatalf("GET plain: %d %s", w.Code, w.Body.String())
	}
	if _, ok := members["lifecycle_state"]; ok {
		t.Errorf("lifecycle_state without a model: %s", w.Body.String())
	}
	if members["status"] != "active" || members["health"] == nil || members["owner_user_id"] != o.userA {
		t.Errorf("separate members: %s", w.Body.String())
	}

	if _, err := o.inScope(o.orgScope, `UPDATE ci SET lifecycle_state = 'production' WHERE id = $1`, software.ID); err != nil {
		t.Errorf("software model takes production: %v", err)
	}
	for _, id := range []string{plain.ID, physical.ID} {
		_, err := o.inScope(o.orgScope, `UPDATE ci SET lifecycle_state = 'production' WHERE id = $1`, id)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.ConstraintName != "ci_lifecycle_requires_model" {
			t.Errorf("lifecycle state of CI %s: %v, want ci_lifecycle_requires_model", id, err)
		}
	}
	// A type change of the CI with a state to a type without a software
	// model: 422 on lifecycle_state.
	for _, typeID := range []string{physicalType, o.serverType} {
		w = a.call(http.MethodPost, "/api/v1/cis/"+software.ID+"/type", `{"ci_type_id":"`+typeID+`"}`)
		if !violationOn(t, w.Code, w.Body.String(), "lifecycle_state") {
			t.Errorf("type change of a CI with a lifecycle state to %s: %d %s, want 422 on lifecycle_state", typeID, w.Code, w.Body.String())
		}
	}
	// filter[lifecycle_state] and read-only health and lifecycle_state.
	w = a.call(http.MethodGet, "/api/v1/cis?filter[lifecycle_state]=production&limit=200", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), software.ID) || strings.Contains(w.Body.String(), plain.ID) {
		t.Errorf("filter[lifecycle_state]: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"health":"critical"}`, `{"lifecycle_state":"retired"}`} {
		if code, resp := patchCI(a, software.ID, body); code != http.StatusBadRequest {
			t.Errorf("PATCH %s: %d %s, want 400", body, code, resp)
		}
	}
	if got := o.scalar(t, `SELECT lifecycle_state || '|' || health || '|' || COALESCE(owner_team_id::text, '') FROM ci WHERE id = $1`, software.ID); got != "production|unknown|"+o.teamA {
		t.Errorf("software CI after the refused writes: %s", got)
	}
	// Health is not historized.
	if n := countRows(t, f, `SELECT count(*) FROM ci_change WHERE field_name = 'health' AND ci_id = ANY($1::uuid[])`,
		[]string{plain.ID, software.ID, physical.ID}); n != 0 {
		t.Errorf("%d ci_change rows for health", n)
	}
}
