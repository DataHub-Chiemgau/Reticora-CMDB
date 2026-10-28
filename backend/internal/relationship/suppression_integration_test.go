package relationship_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestSuppression covers WP-160 (REL-03, REL-04, REL-05) against
// PostgreSQL: deleting a discovery edge needs force and records a
// suppression with its type; discovery then does not recreate that edge
// but still creates another type between the same CIs; discovery neither
// changes nor removes a manual edge.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSuppression(t *testing.T) {
	f := scopetest.Seed(t, "ac")
	bg := context.Background()
	mac := map[string]string{"sw": "00:1b:21:16:00:01", "peer": "00:1b:21:16:00:02", "pdu": "00:1b:21:16:00:03", "manual": "00:1b:21:16:00:04"}
	ids := map[string]string{}
	for name, m := range mac {
		id := f.CI(t, f.OrgA, "", "wp160-"+name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, id, m); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	rels := relationship.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), rels).RegisterRoutes(mux)
	relationship.NewHandler(rels).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx))
		return w
	}

	// A manual edge sw → manual exists before discovery sees that neighbor.
	manual := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: ids["sw"], TargetCIID: ids["manual"], RelType: "connected_to",
		Source: "manual", VerificationState: "verified", Attributes: map[string]any{"note": "patched by hand"}}
	if err := rels.Create(ctx, manual); err != nil {
		t.Fatal(err)
	}

	ingest := func() {
		t.Helper()
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "wp160-sw", IP: "192.0.2.160", MAC: mac["sw"]}, time.Now())
		rec.Neighbors = []plugins.DeviceNeighbor{
			{Protocol: "lldp", LocalInterface: "Gi1/0/1", RemoteMAC: mac["peer"]},
			{Protocol: "lldp", LocalInterface: "Gi1/0/2", RemoteMAC: mac["manual"]},
		}
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if err != nil {
			t.Fatal(err)
		}
		if w := call(http.MethodPost, "/api/v1/discovery/ingest", body); w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	edgeOf := func(src, dst, relType string) (id, source string, confidence *float64, lastSeen *time.Time) {
		t.Helper()
		err := f.Admin.QueryRow(bg, `SELECT id::text, source, confidence::float8, last_seen_at FROM ci_relationship
			WHERE source_ci_id = $1 AND target_ci_id = $2 AND rel_type = $3`, src, dst, relType).Scan(&id, &source, &confidence, &lastSeen)
		if err != nil {
			return "", "", nil, nil
		}
		return id, source, confidence, lastSeen
	}

	ingest()
	discovered, source, _, _ := edgeOf(ids["sw"], ids["peer"], "connected_to")
	if discovered == "" || source != "discovery" {
		t.Fatalf("discovered edge %q (%s)", discovered, source)
	}
	// REL-05: the manual edge is untouched by discovery.
	manualID, manualSource, manualConf, manualSeen := edgeOf(ids["sw"], ids["manual"], "connected_to")
	if manualID != manual.ID || manualSource != "manual" || manualConf != nil || manualSeen != nil {
		t.Errorf("manual edge after discovery: id %s source %s confidence %v last_seen %v", manualID, manualSource, manualConf, manualSeen)
	}

	// REL-04: a discovery edge needs force; then it is suppressed (REL-03).
	if w := call(http.MethodDelete, "/api/v1/relationships/"+discovered, nil); w.Code != http.StatusConflict {
		t.Fatalf("delete without force: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodDelete, "/api/v1/relationships/"+discovered+"?force=true", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete with force: %d %s", w.Code, w.Body.String())
	}
	var suppressedType string
	if err := f.Admin.QueryRow(bg, `SELECT rt.key FROM relationship_suppression s JOIN relationship_type rt ON rt.id = s.relationship_type_id
		WHERE s.source_ci_id = $1 AND s.target_ci_id = $2`, ids["sw"], ids["peer"]).Scan(&suppressedType); err != nil || suppressedType != "connected_to" {
		t.Fatalf("suppression = %q, %v", suppressedType, err)
	}
	ingest()
	if id, _, _, _ := edgeOf(ids["sw"], ids["peer"], "connected_to"); id != "" {
		t.Error("discovery recreated a suppressed edge")
	}
	// The suppression is per type: powered_by between the same CIs is new.
	body, _ := json.Marshal(map[string]any{"collector_id": "c", "items": []map[string]any{{"ci_type_name": "switch", "name": "wp160-sw",
		"primary_mac": mac["sw"], "management_ip": "192.0.2.160",
		"relationships": []map[string]any{{"target_mac": mac["peer"], "type_key": "powered_by"}}}}})
	if w := call(http.MethodPost, "/api/v1/ingest/bulk", body); w.Code != http.StatusAccepted {
		t.Fatalf("legacy ingest: %d %s", w.Code, w.Body.String())
	}
	if id, _, _, _ := edgeOf(ids["sw"], ids["peer"], "powered_by"); id == "" {
		t.Error("a suppression of connected_to blocked powered_by")
	}

	// A manual edge is deleted without force and leaves no suppression.
	if w := call(http.MethodDelete, "/api/v1/relationships/"+manual.ID, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete manual: %d", w.Code)
	}
	var n int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM relationship_suppression WHERE source_ci_id = $1 AND target_ci_id = $2`, ids["sw"], ids["manual"]).Scan(&n); err != nil || n != 0 {
		t.Errorf("manual deletion suppressed: %d %v", n, err)
	}
}

// TestSuppressionLifecycle is the retest of F-273 (WP-584; REL-03, REL-04)
// through the real router (authorization, tenant middleware, route audit) on
// PostgreSQL:
//   - the forced deletion of a discovery edge stores the suppression with
//     the acting user and the reason and audits it
//     (relationship.suppression_created, actor, reason); a reason over 1000
//     characters is 422 on reason and deletes nothing;
//   - GET /relationship-suppressions?ci_id= lists it with reason and actor;
//     the administrator of another organization sees none and gets 404 when
//     lifting it, a viewer (relationship:read only) gets 403; nothing is
//     lifted by them;
//   - lifting it is 204 and audited (relationship.suppression_lifted, actor,
//     reason); the next ingest creates the edge again, while it was
//     suppressed the ingest did not; lifting it again is 404;
//   - client separation (policies since 000061/000063): a client-1
//     technician does not see a suppression between client-2 CIs and gets
//     404 when lifting it; its own suppression between client-1 CIs is
//     listed and lifted (204).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSuppressionLifecycle(t *testing.T) {
	f := scopetest.Seed(t, "58")
	bg := context.Background()
	mac := map[string]string{"sw": "00:1b:21:58:00:01", "peer": "00:1b:21:58:00:02"}
	ids := map[string]string{}
	for name, m := range mac {
		id := f.CI(t, f.OrgA, "", "wp584-"+name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, id, m); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}

	// The ingest runs on the discovery handler, as in TestSuppression.
	rels := relationship.NewPGRepository(f.App)
	ingestMux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), rels).RegisterRoutes(ingestMux)
	ingestCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ingest := func() {
		t.Helper()
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "wp584-sw", IP: "192.0.2.58", MAC: mac["sw"]}, time.Now())
		rec.Neighbors = []plugins.DeviceNeighbor{{Protocol: "lldp", LocalInterface: "Gi1/0/1", RemoteMAC: mac["peer"]}}
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		ingestMux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ingestCtx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	edge := func() (id, source string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT id::text, source FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2
			AND rel_type = 'connected_to'`, ids["sw"], ids["peer"]).Scan(&id, &source); err != nil {
			return "", ""
		}
		return id, source
	}

	// The suppression endpoints run through the real router.
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version: "wp584", Entitlements: entitlements,
		CIService:   ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials: credential.NewService(repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Egress:      egress.Options{AllowPrivate: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	router := middleware.TenantMiddleware(mux)
	all := identity.ScopeSet{All: true}
	principal := func(org, role, name string, clients identity.ScopeSet) identity.Principal {
		keys, ok := permission.StandardRolePermissions(role)
		if !ok {
			t.Fatalf("unknown standard role %s", role)
		}
		perms := make([]identity.Permission, 0, len(keys))
		for _, k := range keys {
			perms = append(perms, identity.Permission(k))
		}
		p := identity.Principal{Subject: f.AppUser(t, org, name), OrganizationID: org, Type: identity.PrincipalTypeUser,
			Scope: &identity.Scope{Clients: clients, Sites: all, Teams: all}, Permissions: perms, Credential: "wp584-" + name}
		p.ClientScope = p.Scope.LegacyClientScope()
		return p
	}
	engineer := principal(f.OrgA, "engineer", "wp584-engineer", all)
	viewer := principal(f.OrgA, "viewer", "wp584-viewer", all)
	technician := principal(f.OrgA, "client_technician", "wp584-tech", identity.ScopeSet{IDs: []string{f.Client1}})
	foreignAdmin := principal(f.OrgB, "org_admin", "wp584-foreign", all)
	call := func(p identity.Principal, method, path string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, http.NoBody)
		ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), p), p.Permissions)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	type suppression struct {
		ID         string `json:"id"`
		SourceCIID string `json:"source_ci_id"`
		TargetCIID string `json:"target_ci_id"`
		RelType    string `json:"rel_type"`
		Reason     string `json:"reason"`
		CreatedBy  string `json:"created_by"`
		CreatedAt  string `json:"created_at"`
	}
	list := func(p identity.Principal, query string) []suppression {
		t.Helper()
		w := call(p, http.MethodGet, "/api/v1/relationship-suppressions"+query)
		if w.Code != http.StatusOK {
			t.Fatalf("GET /relationship-suppressions%s: %d %s", query, w.Code, w.Body.String())
		}
		var page struct {
			Data  []suppression `json:"data"`
			Total int           `json:"total"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if page.Total != len(page.Data) {
			t.Errorf("GET /relationship-suppressions%s: total %d for %d rows", query, page.Total, len(page.Data))
		}
		return page.Data
	}
	stored := func(id string) bool {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM relationship_suppression WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	type auditEntry struct {
		actor, actorType, reason, createdBy, relType string
	}
	audited := func(action, id string) []auditEntry {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT COALESCE(actor_id::text, ''), actor_type, COALESCE(changes->>'reason', ''),
				COALESCE(changes->>'created_by', ''), COALESCE(changes->>'rel_type', '')
			FROM audit_log WHERE organization_id = $1 AND action = $2 AND resource_type = 'relationship_suppression' AND resource_id = $3`,
			f.OrgA, action, id)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []auditEntry
		for rows.Next() {
			var e auditEntry
			if err := rows.Scan(&e.actor, &e.actorType, &e.reason, &e.createdBy, &e.relType); err != nil {
				t.Fatal(err)
			}
			out = append(out, e)
		}
		return out
	}

	ingest()
	discovered, source := edge()
	if discovered == "" || source != "discovery" {
		t.Fatalf("discovered edge %q (%s)", discovered, source)
	}

	// A reason over 1000 characters is refused; nothing is deleted.
	long := strings.Repeat("x", relationship.MaxSuppressionReason+1)
	if w := call(engineer, http.MethodDelete, "/api/v1/relationships/"+discovered+"?force=true&reason="+long); w.Code != http.StatusUnprocessableEntity ||
		!strings.Contains(w.Body.String(), `"field":"reason"`) {
		t.Errorf("reason over the limit: %d %s, want 422 on reason", w.Code, w.Body.String())
	}
	if id, _ := edge(); id != discovered {
		t.Fatal("a refused reason deleted the edge")
	}

	// The forced deletion with a reason stores actor and reason (F-273).
	const reason = "Fehlverkabelung, LLDP meldet einen alten Nachbarn"
	if w := call(engineer, http.MethodDelete, "/api/v1/relationships/"+discovered+"?force=true&reason="+url.QueryEscape(" "+reason+" ")); w.Code != http.StatusNoContent {
		t.Fatalf("delete with force and reason: %d %s", w.Code, w.Body.String())
	}
	listed := list(viewer, "?ci_id="+ids["sw"])
	if len(listed) != 1 {
		t.Fatalf("suppressions of the switch: %+v, want one", listed)
	}
	s := listed[0]
	if s.SourceCIID != ids["sw"] || s.TargetCIID != ids["peer"] || s.RelType != "connected_to" || s.Reason != reason ||
		s.CreatedBy != engineer.Subject || s.CreatedAt == "" {
		t.Errorf("listed suppression %+v, want sw → peer connected_to, reason %q, created_by %s", s, reason, engineer.Subject)
	}
	if peer := list(viewer, "?ci_id="+ids["peer"]); len(peer) != 1 || peer[0].ID != s.ID {
		t.Errorf("suppressions of the peer: %+v, want %s", peer, s.ID)
	}
	if got := audited(relationship.ActionSuppressionCreated, s.ID); len(got) != 1 ||
		got[0] != (auditEntry{actor: engineer.Subject, actorType: "user", reason: reason, createdBy: engineer.Subject, relType: "connected_to"}) {
		t.Errorf("audit %s: %+v, want one entry with actor and reason", relationship.ActionSuppressionCreated, got)
	}
	ingest()
	if id, _ := edge(); id != "" {
		t.Fatal("discovery recreated a suppressed edge")
	}

	// Another organization neither sees nor lifts it; a viewer may not lift.
	if foreign := list(foreignAdmin, ""); len(foreign) != 0 {
		t.Errorf("another organization lists %+v", foreign)
	}
	if foreign := list(foreignAdmin, "?ci_id="+ids["sw"]); len(foreign) != 0 {
		t.Errorf("another organization lists %+v for the switch", foreign)
	}
	for _, tc := range []struct {
		name string
		p    identity.Principal
		want int
	}{
		{"administrator of another organization", foreignAdmin, http.StatusNotFound},
		{"viewer (relationship:read only)", viewer, http.StatusForbidden},
	} {
		if w := call(tc.p, http.MethodDelete, "/api/v1/relationship-suppressions/"+s.ID); w.Code != tc.want || !stored(s.ID) {
			t.Errorf("%s lifts: %d (want %d), suppression kept=%v", tc.name, w.Code, tc.want, stored(s.ID))
		}
	}
	if w := call(engineer, http.MethodDelete, "/api/v1/relationship-suppressions/not-a-uuid"); w.Code != http.StatusNotFound {
		t.Errorf("malformed id: %d, want 404", w.Code)
	}
	if w := call(engineer, http.MethodGet, "/api/v1/relationship-suppressions?ci_id=not-a-uuid"); w.Code != http.StatusBadRequest {
		t.Errorf("malformed ci_id: %d, want 400", w.Code)
	}

	// Lifting: audited with actor and reason; the next ingest creates the
	// edge again.
	if w := call(engineer, http.MethodDelete, "/api/v1/relationship-suppressions/"+s.ID); w.Code != http.StatusNoContent || stored(s.ID) {
		t.Fatalf("lift: %d %s, suppression kept=%v", w.Code, w.Body.String(), stored(s.ID))
	}
	if got := audited(relationship.ActionSuppressionLifted, s.ID); len(got) != 1 ||
		got[0] != (auditEntry{actor: engineer.Subject, actorType: "user", reason: reason, createdBy: engineer.Subject, relType: "connected_to"}) {
		t.Errorf("audit %s: %+v, want one entry with actor and reason", relationship.ActionSuppressionLifted, got)
	}
	if rest := list(engineer, "?ci_id="+ids["sw"]); len(rest) != 0 {
		t.Errorf("suppressions after lifting: %+v", rest)
	}
	if w := call(engineer, http.MethodDelete, "/api/v1/relationship-suppressions/"+s.ID); w.Code != http.StatusNotFound {
		t.Errorf("repeated lift: %d, want 404", w.Code)
	}
	ingest()
	if id, source := edge(); id == "" || source != "discovery" {
		t.Errorf("after lifting the next ingest did not create the edge again (%q, %s)", id, source)
	}

	// Client separation: the technician of client 1 sees and lifts only
	// suppressions between CIs it sees.
	suppress := func(src, dst string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id, reason)
			SELECT $1, $2, $3, id, 'wp584' FROM relationship_type WHERE key = 'connected_to' AND organization_id IS NULL RETURNING id::text`,
			f.OrgA, src, dst).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	c1a, c1b := f.CI(t, f.OrgA, f.Client1, "wp584-c1a"), f.CI(t, f.OrgA, f.Client1, "wp584-c1b")
	c2a, c2b := f.CI(t, f.OrgA, f.Client2, "wp584-c2a"), f.CI(t, f.OrgA, f.Client2, "wp584-c2b")
	own, foreign := suppress(c1a, c1b), suppress(c2a, c2b)
	var techIDs []string
	for _, s := range list(technician, "") {
		techIDs = append(techIDs, s.ID)
	}
	if !slices.Contains(techIDs, own) || slices.Contains(techIDs, foreign) {
		t.Errorf("client-1 technician lists %v, want %s and not %s", techIDs, own, foreign)
	}
	if got := list(technician, "?ci_id="+c2a); len(got) != 0 {
		t.Errorf("client-1 technician lists %+v for a client-2 CI", got)
	}
	if w := call(technician, http.MethodDelete, "/api/v1/relationship-suppressions/"+foreign); w.Code != http.StatusNotFound || !stored(foreign) {
		t.Errorf("client-1 technician lifts a client-2 suppression: %d (want 404), kept=%v", w.Code, stored(foreign))
	}
	if w := call(technician, http.MethodDelete, "/api/v1/relationship-suppressions/"+own); w.Code != http.StatusNoContent || stored(own) {
		t.Errorf("client-1 technician lifts its own suppression: %d (want 204), kept=%v", w.Code, stored(own))
	}
	if got := audited(relationship.ActionSuppressionLifted, own); len(got) != 1 || got[0].actor != technician.Subject || got[0].reason != "wp584" {
		t.Errorf("audit of the technician's lift: %+v", got)
	}
}
