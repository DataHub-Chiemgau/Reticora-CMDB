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

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// asOfState is the answer of GET /cis/{id}/as-of; Relationships stays nil
// when the member is missing.
type asOfState struct {
	EntityID          string               `json:"entity_id"`
	At                time.Time            `json:"at"`
	BasedOnSnapshotAt *time.Time           `json:"based_on_snapshot_at"`
	Fields            map[string]any       `json:"fields"`
	Relationships     *[]history.EdgeState `json:"relationships"`
	Deleted           bool                 `json:"deleted"`
	MergedIntoID      string               `json:"merged_into_id"`
	NotHistorized     []string             `json:"not_historized"`
}

// edgeIDs are the sorted ids of the edges of a state.
func (s *asOfState) edgeIDs() []string {
	ids := []string{}
	if s.Relationships == nil {
		return ids
	}
	for _, e := range *s.Relationships {
		id, _ := e["id"].(string)
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}

// sortedIDs returns ids sorted.
func sortedIDs(ids ...string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

// TestCIAsOfAgainstPostgres covers AUD-08 (WP-793) against PostgreSQL
// through the production router (server.NewRouter) behind the deprecation
// headers of api/openapi.yaml: GET /cis/{id}/as-of replays the state of a
// CI with its edges from the weekly ci_snapshot and relationship_change.
// An edge created before T and removed after T is there, one created after
// T or removed before T is not, also when the cleanup or the merge removed
// it; without a snapshot the whole trail is replayed. A client-1 principal
// sees no edge to a client-2 CI, also not from the snapshot, and gets 404
// for the client-2 CI and an unknown id. Without relationship:read the
// answer has no relationships; a field without read permission is missing
// (WP-265); after the deletion of the vault entry the personal value is
// null. The victim of a merge answers its own state before the merge and
// deleted with merged_into_id after it; a CI deleted for good answers from
// its last snapshot. GET /cis/{id}/state answers the same with the headers
// Deprecation, Sunset and Link (successor-version); the result of POST
// /history/snapshots holds the surrogate, not the personal value.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIAsOfAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "d3")
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

	// Principals: the administrator, a client-1 reader, a reader without
	// relationship:read and a reader without the field model.
	userAdmin := f.AppUser(t, f.OrgA, "wp793-admin")
	hfrExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, userAdmin)
	noModelRole := f.ID()
	hfrExec(t, f, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp793-no-model', '["ci:read","relationship:read"]'::jsonb)`,
		noModelRole, f.OrgA)
	userNoModel := f.AppUser(t, f.OrgA, "wp793-no-model")
	hfrExec(t, f, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, userNoModel, noModelRole)
	hfrExec(t, f, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', 'core.model', 'none')`,
		f.OrgA, noModelRole)
	admin := hfrPrincipal(f.OrgA, userAdmin)
	admin.Permissions = identity.AllPermissions()
	client1 := hfrPrincipal(f.OrgA, userAdmin, "ci:read", "relationship:read")
	client1.Scope = &identity.Scope{Clients: identity.ScopeSet{IDs: []string{f.Client1}}, Sites: identity.ScopeSet{All: true},
		Teams: identity.ScopeSet{All: true}}
	noRelationships := hfrPrincipal(f.OrgA, userAdmin, "ci:read")
	noModel := hfrPrincipal(f.OrgA, userNoModel, "ci:read", "relationship:read")

	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp-793", Entitlements: entitlements, CIService: ci.NewServiceWithLimits(repos.CI, entitlements).WithFieldResolver(repos.CIType),
		Credentials: credential.NewService(repos.Credential, encryptor), OIDC: identity.NewOIDCProvider(identity.OIDCConfig{}),
	})
	if err != nil {
		t.Fatal(err)
	}
	validator := middleware.NewOpenAPIValidator()
	if loaded, verr := validator.CheckSpec(true); !loaded || verr != nil {
		t.Fatalf("api/openapi.yaml not loaded: %v", verr)
	}
	e := &hfrEnv{t: t, router: middleware.RequestID(middleware.TenantMiddleware(validator.DeprecationHeaders(mux)))}

	// The writes go through the repositories with the organization-wide
	// scope of the administrator.
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: userAdmin})
	svc := ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()))
	var typeID string
	if err = f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	mk := func(name, clientID, model string, attrs map[string]any) string {
		t.Helper()
		item := &ci.Item{OrganizationID: f.OrgA, ClientID: clientID, CITypeID: typeID, Name: name, Status: "active", Model: model, Attributes: attrs}
		if cerr := svc.Create(ctx, item); cerr != nil {
			t.Fatalf("create %s: %v", name, cerr)
		}
		return item.ID
	}
	a := mk("wp793-a", f.Client1, "m-a", map[string]any{"contact_email": "wp793-owner@example.invalid"})
	b := mk("wp793-b", f.Client1, "", nil)
	foreign := mk("wp793-c2", f.Client2, "", nil)
	victim := mk("wp793-victim", f.Client1, "m-victim", nil)
	gone := mk("wp793-gone", f.Client1, "", nil)
	rels := relationship.NewPGRepository(f.App)
	edge := func(target, relType, source string) string {
		t.Helper()
		rel := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: a, TargetCIID: target, RelType: relType, Source: source}
		if cerr := rels.Create(ctx, rel); cerr != nil {
			t.Fatalf("create %s edge to %s: %v", relType, target, cerr)
		}
		return rel.ID
	}
	remove := func(id string) {
		t.Helper()
		if derr := rels.Delete(ctx, f.OrgA, id); derr != nil {
			t.Fatalf("delete edge %s: %v", id, derr)
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

	// Before the snapshot: one edge created and removed, three that stay
	// (one to the client-2 CI, one discovery edge the cleanup removes
	// later).
	removedEarly := edge(b, "uses", "")
	remove(removedEarly)
	kept := edge(b, "runs_on", "")
	toForeign := edge(foreign, "depends_on", "")
	stale := edge(b, "connected_to", "discovery")
	hfrExec(t, f, `UPDATE ci_relationship SET created_at = now() - interval '60 days', last_seen_at = now() - interval '60 days',
		last_confirmed_at = now() - interval '60 days' WHERE id = $1`, stale)
	t0 := clock()
	// The weekly snapshot, taken now.
	if _, err = history.NewWeeklySnapshot(f.App).WithClock(func() time.Time { return time.Now().Add(time.Hour) }).RunOrg(bg, f.OrgA); err != nil {
		t.Fatalf("weekly snapshot: %v", err)
	}
	var takenAt time.Time
	if err = f.Admin.QueryRow(bg, `SELECT taken_at FROM ci_snapshot WHERE ci_id = $1`, a).Scan(&takenAt); err != nil {
		t.Fatalf("weekly snapshot of A: %v", err)
	}
	// After the snapshot, before T1: an edge removed after T1.
	mid := edge(b, "backed_up_by", "")
	t1 := clock()
	// After T1: an edge to the victim, the removal of two edges, the
	// cleanup of the discovery edge.
	toVictim := edge(victim, "depends_on", "")
	remove(kept)
	remove(mid)
	if res, cerr := relationship.NewCleanup(f.App).RunOrg(bg, f.OrgA); cerr != nil || res.Stale != 1 {
		t.Fatalf("cleanup: %+v %v, want the stale discovery edge removed", res, cerr)
	}
	t2 := clock()
	// After T2: the duplicate of the edge to the victim on B, then the
	// merge of the victim into B removes the victim's edge as duplicate.
	dup := edge(b, "depends_on", "")
	if _, err = svc.Merge(ctx, f.OrgA, b, victim); err != nil {
		t.Fatalf("merge: %v", err)
	}
	t3 := clock()

	asOf := func(p *identity.Principal, path string, at *time.Time) (int, []byte, http.Header) {
		t.Helper()
		if at != nil {
			path += "?at=" + at.UTC().Format(time.RFC3339Nano)
		}
		req := httptest.NewRequest(http.MethodGet, path, nil)
		reqCtx := identity.WithPermissions(identity.WithPrincipal(req.Context(), *p), p.Permissions)
		w := httptest.NewRecorder()
		e.router.ServeHTTP(w, req.WithContext(reqCtx))
		return w.Code, w.Body.Bytes(), w.Header()
	}
	state := func(p *identity.Principal, id string, at *time.Time) *asOfState {
		t.Helper()
		code, body, _ := asOf(p, "/api/v1/cis/"+id+"/as-of", at)
		if code != http.StatusOK {
			t.Fatalf("GET as-of of %s at %v: %d %s", id, at, code, body)
		}
		var s asOfState
		if uerr := json.Unmarshal(body, &s); uerr != nil {
			t.Fatal(uerr)
		}
		return &s
	}

	t.Run("edges at T", func(t *testing.T) {
		for _, c := range []struct {
			label string
			at    time.Time
			want  []string
			base  bool
		}{
			{"before the snapshot (whole trail)", t0, sortedIDs(kept, toForeign, stale), false},
			{"T1 (snapshot and replay)", t1, sortedIDs(kept, toForeign, stale, mid), true},
			{"T2 (after removal and cleanup)", t2, sortedIDs(toForeign, toVictim), true},
			{"T3 (after the merge)", t3, sortedIDs(toForeign, dup), true},
		} {
			s := state(&admin, a, &c.at)
			if got := s.edgeIDs(); !slices.Equal(got, c.want) {
				t.Errorf("%s: edges %v, want %v (removed early %s)", c.label, got, c.want, removedEarly)
			}
			if c.base != (s.BasedOnSnapshotAt != nil) || c.base && !s.BasedOnSnapshotAt.Equal(takenAt) {
				t.Errorf("%s: based on snapshot %v, want %v (taken %s)", c.label, s.BasedOnSnapshotAt, c.base, takenAt)
			}
			if !s.At.Equal(c.at) || s.EntityID != a || !slices.Equal(s.NotHistorized, history.NotHistorizedCIFields) {
				t.Errorf("%s: at %s entity %s not_historized %v", c.label, s.At, s.EntityID, s.NotHistorized)
			}
		}
		// The values of an edge from the snapshot are those of its last row.
		for _, edgeState := range *state(&admin, a, &t1).Relationships {
			if edgeState["id"] == kept && (edgeState["rel_type"] != "runs_on" || edgeState["source_ci_id"] != a || edgeState["target_ci_id"] != b ||
				edgeState["source"] != "manual") {
				t.Errorf("edge %s at T1: %v, want runs_on from A to B with its source", kept, edgeState)
			}
		}
	})

	t.Run("scope and permissions", func(t *testing.T) {
		// Client 1: no edge to the client-2 CI, also not from the snapshot.
		for _, at := range []time.Time{t0, t1, t3} {
			s := state(&client1, a, &at)
			if got := s.edgeIDs(); slices.Contains(got, toForeign) {
				t.Errorf("client-1 principal at %s sees the edge to the client-2 CI: %v", at, got)
			}
		}
		if got, want := state(&client1, a, &t1).edgeIDs(), sortedIDs(kept, stale, mid); !slices.Equal(got, want) {
			t.Errorf("client-1 principal at T1: edges %v, want %v", got, want)
		}
		for _, id := range []string{foreign, f.ID()} {
			if code, body, _ := asOf(&client1, "/api/v1/cis/"+id+"/as-of", nil); code != http.StatusNotFound {
				t.Errorf("client-1 principal as-of of %s: %d %s, want 404", id, code, body)
			}
		}
		// Without relationship:read no relationships member.
		code, body, _ := asOf(&noRelationships, "/api/v1/cis/"+a+"/as-of", &t1)
		var raw map[string]json.RawMessage
		if code != http.StatusOK || json.Unmarshal(body, &raw) != nil {
			t.Fatalf("as-of without relationship:read: %d %s", code, body)
		}
		if _, has := raw["relationships"]; has {
			t.Errorf("as-of without relationship:read carries relationships: %s", body)
		}
		// A field without read permission is missing (WP-265).
		if s := state(&noModel, a, nil); s.Fields["model"] != nil || s.Fields["name"] != "wp793-a" {
			t.Errorf("as-of without the field model: %v", s.Fields)
		}
		if s := state(&admin, a, nil); s.Fields["model"] != "m-a" {
			t.Errorf("as-of of the administrator: model %v, want m-a", s.Fields["model"])
		}
	})

	t.Run("merged victim", func(t *testing.T) {
		before := state(&admin, victim, &t2)
		if before.Deleted || before.MergedIntoID != "" || before.Fields["name"] != "wp793-victim" || before.Fields["model"] != "m-victim" ||
			!slices.Equal(before.edgeIDs(), []string{toVictim}) {
			t.Errorf("victim before the merge: %+v, edges %v; want its own state with the edge %s", before, before.edgeIDs(), toVictim)
		}
		after := state(&admin, victim, &t3)
		if !after.Deleted || after.MergedIntoID != b || len(after.edgeIDs()) != 0 {
			t.Errorf("victim after the merge: deleted %v merged into %q edges %v; want deleted, %s, none", after.Deleted, after.MergedIntoID,
				after.edgeIDs(), b)
		}
	})

	t.Run("alias state", func(t *testing.T) {
		code, asOfBody, asOfHeader := asOf(&admin, "/api/v1/cis/"+a+"/as-of", &t1)
		stateCode, stateBody, stateHeader := asOf(&admin, "/api/v1/cis/"+a+"/state", &t1)
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
		if got, want := stateHeader.Get("Link"), `</api/v1/cis/`+a+`/as-of>; rel="successor-version"`; got != want {
			t.Errorf("/state Link %q, want %q", got, want)
		}
	})

	t.Run("snapshot job keeps the surrogate", func(t *testing.T) {
		code, body := e.do(&admin, http.MethodPost, "/api/v1/history/snapshots", map[string]any{"entity_type": "ci", "entity_id": a})
		var accepted struct {
			JobID string `json:"job_id"`
		}
		if code != http.StatusAccepted || json.Unmarshal(body, &accepted) != nil {
			t.Fatalf("POST snapshot: %d %s", code, body)
		}
		for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
			code, body = e.do(&admin, http.MethodGet, "/api/v1/jobs/"+accepted.JobID+"/result", nil)
			if code == http.StatusOK || time.Now().After(deadline) {
				break
			}
		}
		var result history.StateSnapshot
		if code != http.StatusOK || json.Unmarshal(body, &result) != nil {
			t.Fatalf("snapshot result: %d %s", code, body)
		}
		hfrHidden(t, "snapshot result", body, "wp793-owner@example.invalid")
		attrs, _ := result.Fields["attributes"].(map[string]any)
		if _, isRef := pii.RefID(attrs["contact_email"]); !isRef || result.Relationships != nil {
			t.Errorf("snapshot result: contact_email %v relationships %v; want the surrogate and no edges", attrs["contact_email"], result.Relationships)
		}
		var stored []byte
		if err := f.Admin.QueryRow(bg, `SELECT result::text FROM job WHERE id = $1`, accepted.JobID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		hfrHidden(t, "job.result", stored, "wp793-owner@example.invalid")
	})

	t.Run("deleted vault entry", func(t *testing.T) {
		attrsOf := func(s *asOfState) map[string]any {
			attrs, _ := s.Fields["attributes"].(map[string]any)
			return attrs
		}
		now := state(&admin, a, nil)
		if attrsOf(now)["contact_email"] != "wp793-owner@example.invalid" {
			t.Fatalf("as-of before the deletion: %v, want the personal value", attrsOf(now))
		}
		rows, err := f.Admin.Query(bg, `SELECT DISTINCT v FROM (
			SELECT new_value->'attributes'->'contact_email'->>'$pii' AS v FROM ci_change WHERE ci_id = $1
			UNION SELECT state->'fields'->'attributes'->'contact_email'->>'$pii' FROM ci_snapshot WHERE ci_id = $1) s WHERE v IS NOT NULL`, a)
		if err != nil {
			t.Fatal(err)
		}
		subjects, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil || len(subjects) == 0 {
			t.Fatalf("surrogates of the personal value: %v %v", subjects, err)
		}
		for _, id := range subjects {
			if derr := vault.Delete(ctx, f.OrgA, id); derr != nil {
				t.Fatal(derr)
			}
		}
		for _, at := range []*time.Time{nil, &t0} {
			s := state(&admin, a, at)
			v, has := attrsOf(s)["contact_email"]
			if !has || v != nil {
				t.Errorf("as-of at %v after the deletion of the vault entry: contact_email %v (present %v), want null", at, v, has)
			}
		}
	})

	t.Run("deleted for good", func(t *testing.T) {
		if _, err := svc.Delete(ctx, f.OrgA, gone); err != nil {
			t.Fatal(err)
		}
		if err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			return jobs.HardDeleteCI(ctx, tx, f.OrgA, gone)
		}); err != nil {
			t.Fatalf("hard delete: %v", err)
		}
		s := state(&admin, gone, nil)
		if s.Fields["name"] != "wp793-gone" || s.BasedOnSnapshotAt == nil || !s.BasedOnSnapshotAt.Equal(takenAt) {
			t.Errorf("as-of of the CI deleted for good: %+v, want its last snapshot", s)
		}
		if code, body, _ := asOf(&client1, "/api/v1/cis/"+gone+"/as-of", nil); code != http.StatusOK {
			t.Errorf("client-1 principal as-of of its CI deleted for good: %d %s, want 200", code, body)
		}
	})
}
