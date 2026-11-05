package relationship_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// changeJSON is a row of GET /relationships/{id}/changes.
type changeJSON struct {
	ID             string         `json:"id"`
	RelationshipID string         `json:"relationship_id"`
	SourceCIID     string         `json:"source_ci_id"`
	TargetCIID     string         `json:"target_ci_id"`
	RelType        string         `json:"rel_type"`
	ChangeKind     string         `json:"change_kind"`
	ChangedFields  []string       `json:"changed_fields"`
	Before         map[string]any `json:"before"`
	After          map[string]any `json:"after"`
	ActorID        string         `json:"actor_id"`
	ActorType      string         `json:"actor_type"`
	Source         string         `json:"source"`
	RequestID      string         `json:"request_id"`
	CorrelationID  string         `json:"correlation_id"`
	ObservedAt     time.Time      `json:"observed_at"`
}

// historyJSON is an entry of GET /history/relationship/{id}.
type historyJSON struct {
	ID            string         `json:"id"`
	EntityType    string         `json:"entity_type"`
	EntityID      string         `json:"entity_id"`
	ChangeType    string         `json:"change_type"`
	OldValue      map[string]any `json:"old_value"`
	NewValue      map[string]any `json:"new_value"`
	Source        string         `json:"source"`
	ChangeSource  string         `json:"change_source"`
	RequestID     string         `json:"request_id"`
	CorrelationID string         `json:"correlation_id"`
	CreatedAt     time.Time      `json:"created_at"`
	ObservedAt    *time.Time     `json:"observed_at"`
}

// TestRelationshipChangeHistory covers WP-296 (REL-09, EVT-03, AUD-07)
// against PostgreSQL: every mutation of a stored edge through the API and
// a new discovery edge write one relationship_change row with the edge
// before and after, the changed fields, actor, source and request context,
// and exactly one event (relationship.created/removed as since WP-400,
// relationship.changed for a PATCH, without attribute values). A PATCH
// without a changed value and a refused DELETE write neither. Both read
// paths return the same rows with the personal values resolved, page by
// cursor, keep the history of a deleted edge visible to a principal of its
// client, answer 404 for an edge outside the scope and a projected id, and
// project the fields by the relationship grant. A change of the client of
// an endpoint re-scopes its rows.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipChangeHistory(t *testing.T) {
	x := seedRelRights(t)
	f := x.f
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

	x.repo.WithEvents(events.NewOutbox())
	history.NewHandler(history.NewPGRepository(f.App)).RegisterRoutes(x.mux)
	user := f.AppUser(t, f.OrgA, "wp296")
	perms := []identity.Permission{"ci:read", "relationship:read", "relationship:write"}

	// request is the context of an API request of the user: its principal
	// (a session or an API key), the field policy, the scope of base and
	// the request context the audit middleware attaches.
	request := func(base context.Context, policy *permission.FieldPolicy, typ identity.PrincipalType, source, requestID string) context.Context {
		ctx := identity.WithPrincipal(base, identity.Principal{Subject: user, OrganizationID: f.OrgA, Type: typ, Permissions: perms})
		ctx = tenant.WithTenant(permission.WithPolicy(ctx, policy), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: user})
		return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: requestID, CorrelationID: "corr-" + requestID, Source: source})
	}
	unrestricted := permission.UnrestrictedPolicy(nil)
	orgReq := func(typ identity.PrincipalType, source, requestID string) context.Context {
		return request(f.OrgCtx(f.OrgA), unrestricted, typ, source, requestID)
	}
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body)).WithContext(ctx)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		w := httptest.NewRecorder()
		x.mux.ServeHTTP(w, req)
		return w
	}
	changes := func(ctx context.Context, path string) (rows []changeJSON, nextCursor string) {
		t.Helper()
		w := call(ctx, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var body struct {
			Data       []changeJSON `json:"data"`
			Total      int          `json:"total"`
			NextCursor string       `json:"next_cursor"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return body.Data, body.NextCursor
	}
	generic := func(ctx context.Context, id string) []historyJSON {
		t.Helper()
		path := "/api/v1/history/relationship/" + id
		w := call(ctx, http.MethodGet, path, "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, w.Code, w.Body.String())
		}
		var body struct {
			Data []historyJSON `json:"data"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return body.Data
	}
	countRows := func(id string) int {
		t.Helper()
		var n int
		if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM relationship_change WHERE relationship_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	type eventRow struct {
		typ  string
		data map[string]any
	}
	eventsOf := func(id string) []eventRow {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT type, data FROM event_outbox WHERE organization_id = $1 AND entity_id::text = $2 ORDER BY sequence`, f.OrgA, id)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		var out []eventRow
		for rows.Next() {
			var e eventRow
			if scanErr := rows.Scan(&e.typ, &e.data); scanErr != nil {
				t.Fatal(scanErr)
			}
			out = append(out, e)
		}
		return out
	}
	types := func(evs []eventRow) []string {
		out := make([]string, len(evs))
		for i, e := range evs {
			out[i] = e.typ
		}
		return out
	}
	// checkRow compares the members of a history row other than the edges.
	checkRow := func(step string, got *changeJSON, kind string, fields []string, actorType, source, requestID string) {
		t.Helper()
		if got.ChangeKind != kind || !slices.Equal(got.ChangedFields, fields) || got.ActorType != actorType || got.Source != source ||
			got.RequestID != requestID {
			t.Errorf("%s: row %s %v actor %s source %s request %q; want %s %v %s %s %q", step, got.ChangeKind, got.ChangedFields,
				got.ActorType, got.Source, got.RequestID, kind, fields, actorType, source, requestID)
		}
		wantCorrelation := ""
		if requestID != "" {
			wantCorrelation = "corr-" + requestID
		}
		if got.CorrelationID != wantCorrelation {
			t.Errorf("%s: correlation_id %q, want %q", step, got.CorrelationID, wantCorrelation)
		}
		if actorType == "system" && got.ActorID != "" || actorType != "system" && got.ActorID != user {
			t.Errorf("%s: actor_id %q (actor type %s)", step, got.ActorID, actorType)
		}
		if got.ObservedAt.IsZero() {
			t.Errorf("%s: observed_at is empty", step)
		}
	}

	// POST: created, with the personal value sealed in the table.
	w := call(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "req-create"), http.MethodPost, "/api/v1/relationships",
		`{"source_ci_id":"`+x.cis["a1"]+`","target_ci_id":"`+x.cis["a2"]+`","rel_type":"wp770-cables",`+
			`"attributes":{"contact_email":"alice@example.com","port":"1"},"notes":"n1","confidence":0.5}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST: %d %s", w.Code, w.Body.String())
	}
	var created relationship.Relationship
	if err = json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created.ID
	var storedAfter string
	if err = f.Admin.QueryRow(bg, `SELECT after::text FROM relationship_change WHERE relationship_id = $1`, id).Scan(&storedAfter); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(storedAfter, "alice@example.com") {
		t.Errorf("relationship_change holds the personal value in clear text: %s", storedAfter)
	}

	// A PATCH without a changed value writes neither row nor event.
	if w = call(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "req-same"), http.MethodPatch, "/api/v1/relationships/"+id,
		`{"notes":"n1","confidence":0.5,"attributes":{"port":"1"}}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH without change: %d %s", w.Code, w.Body.String())
	}
	if n := countRows(id); n != 1 {
		t.Errorf("rows after a PATCH without change: %d, want 1", n)
	}
	if got := types(eventsOf(id)); !slices.Equal(got, []string{events.RelationshipCreated}) {
		t.Errorf("events after a PATCH without change: %v", got)
	}

	// updated: attributes and notes, through an API key.
	if w = call(orgReq(identity.PrincipalTypeAPIKey, audit.SourceAPI, "req-update"), http.MethodPatch, "/api/v1/relationships/"+id,
		`{"notes":"n2","attributes":{"port":"2"}}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	// verification_changed: the manual edge is verified on creation; it is
	// disputed together with its confidence, then verified again.
	if w = call(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "req-dispute"), http.MethodPatch, "/api/v1/relationships/"+id,
		`{"verification_state":"disputed","confidence":0.9}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH dispute: %d %s", w.Code, w.Body.String())
	}
	if w = call(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "req-verify"), http.MethodPatch, "/api/v1/relationships/"+id,
		`{"verification_state":"verified"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH verify: %d %s", w.Code, w.Body.String())
	}

	evs := eventsOf(id)
	if got := types(evs); !slices.Equal(got, []string{events.RelationshipCreated, events.RelationshipChanged, events.RelationshipChanged,
		events.RelationshipChanged}) {
		t.Fatalf("events: %v", got)
	}
	for i, want := range []struct {
		kind   string
		fields []any
	}{
		{relationship.ChangeUpdated, []any{"attributes", "notes"}},
		{relationship.ChangeVerificationChanged, []any{"confidence", "verification_state"}},
		{relationship.ChangeVerificationChanged, []any{"verification_state"}},
	} {
		data := evs[i+1].data
		fields, _ := data["changed_fields"].([]any)
		if data["id"] != id || data["change_kind"] != want.kind || !slices.Equal(fields, want.fields) || data["source"] != "manual" ||
			data["rel_type"] != "wp770-cables" || data["source_ci_id"] != x.cis["a1"] || data["target_ci_id"] != x.cis["a2"] {
			t.Errorf("relationship.changed %d: %v", i, data)
		}
		if _, ok := data["attributes"]; ok {
			t.Errorf("relationship.changed %d carries attribute values: %v", i, data)
		}
	}

	rows, _ := changes(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "read"), "/api/v1/relationships/"+id+"/changes")
	if len(rows) != 4 {
		t.Fatalf("rows: %d, want 4: %+v", len(rows), rows)
	}
	verify, dispute, update, create := rows[0], rows[1], rows[2], rows[3]
	checkRow("created", &create, relationship.ChangeCreated, []string{}, "user", audit.SourceUI, "req-create")
	checkRow("updated", &update, relationship.ChangeUpdated, []string{"attributes", "notes"}, "api_key", audit.SourceAPI, "req-update")
	checkRow("disputed", &dispute, relationship.ChangeVerificationChanged, []string{"confidence", "verification_state"},
		"user", audit.SourceUI, "req-dispute")
	checkRow("verified", &verify, relationship.ChangeVerificationChanged, []string{"verification_state"}, "user", audit.SourceUI, "req-verify")
	for _, r := range rows {
		if r.RelationshipID != id || r.SourceCIID != x.cis["a1"] || r.TargetCIID != x.cis["a2"] || r.RelType != "wp770-cables" {
			t.Errorf("row %s: edge %s %s→%s %s", r.ChangeKind, r.RelationshipID, r.SourceCIID, r.TargetCIID, r.RelType)
		}
	}
	if create.Before != nil || create.After["id"] != id || create.After["notes"] != "n1" || create.After["verification_state"] != "verified" ||
		create.After["verified_by"] != user {
		t.Errorf("created: before %v after %v", create.Before, create.After)
	}
	if attrs, _ := create.After["attributes"].(map[string]any); attrs["contact_email"] != "alice@example.com" || attrs["port"] != "1" {
		t.Errorf("created: attributes %v, want the personal value resolved", create.After["attributes"])
	}
	if update.Before["notes"] != "n1" || update.After["notes"] != "n2" {
		t.Errorf("updated: notes %v → %v", update.Before["notes"], update.After["notes"])
	}
	if attrs, _ := update.After["attributes"].(map[string]any); attrs["port"] != "2" || attrs["contact_email"] != "alice@example.com" {
		t.Errorf("updated: attributes %v", update.After["attributes"])
	}
	if dispute.Before["verification_state"] != "verified" || dispute.Before["verified_by"] != user ||
		dispute.After["verification_state"] != "disputed" || dispute.After["verified_by"] != nil || dispute.After["verified_at"] != nil ||
		dispute.Before["confidence"] != 0.5 || dispute.After["confidence"] != 0.9 {
		t.Errorf("disputed: before %v after %v", dispute.Before, dispute.After)
	}
	if verify.Before["verification_state"] != "disputed" || verify.After["verification_state"] != "verified" || verify.After["verified_by"] != user {
		t.Errorf("verified: before %v after %v", verify.Before, verify.After)
	}
	if at, _ := verify.After["verified_at"].(string); at == "" {
		t.Error("verification_changed: verified_at is empty")
	} else if _, perr := time.Parse(time.RFC3339Nano, at); perr != nil {
		t.Errorf("verification_changed: verified_at %q: %v", at, perr)
	}
	if !verify.ObservedAt.After(dispute.ObservedAt) || !dispute.ObservedAt.After(update.ObservedAt) || !update.ObservedAt.After(create.ObservedAt) {
		t.Errorf("rows are not newest first: %v %v %v %v", verify.ObservedAt, dispute.ObservedAt, update.ObservedAt, create.ObservedAt)
	}

	// limit, cursor and next_cursor.
	page, next := changes(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "read"), "/api/v1/relationships/"+id+"/changes?limit=3")
	if len(page) != 3 || page[0].ID != verify.ID || page[1].ID != dispute.ID || page[2].ID != update.ID || next == "" {
		t.Fatalf("first page: %d rows, next_cursor %q", len(page), next)
	}
	page, next = changes(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "read"), "/api/v1/relationships/"+id+"/changes?limit=3&cursor="+next)
	if len(page) != 1 || page[0].ID != create.ID || next != "" {
		t.Fatalf("second page: %d rows, next_cursor %q", len(page), next)
	}

	// The generic history returns the same rows.
	entries := generic(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "read"), id)
	if len(entries) != len(rows) {
		t.Fatalf("generic history: %d entries, want %d", len(entries), len(rows))
	}
	for i := range entries {
		e, r := &entries[i], &rows[i]
		if e.ID != r.ID || e.EntityType != "relationship" || e.EntityID != id || e.ChangeType != r.ChangeKind ||
			e.Source != history.SourceRelationshipChange || e.ChangeSource != r.Source || e.RequestID != r.RequestID ||
			e.CorrelationID != r.CorrelationID || !e.CreatedAt.Equal(r.ObservedAt) || e.ObservedAt == nil || !e.ObservedAt.Equal(r.ObservedAt) {
			t.Errorf("generic entry %d: %+v, row %+v", i, e, r)
		}
		if (e.OldValue == nil) != (r.Before == nil) || (e.NewValue == nil) != (r.After == nil) || e.NewValue != nil && e.NewValue["notes"] != r.After["notes"] {
			t.Errorf("generic entry %d values: %v → %v", i, e.OldValue, e.NewValue)
		}
	}
	if attrs, _ := entries[3].NewValue["attributes"].(map[string]any); attrs["contact_email"] != "alice@example.com" {
		t.Errorf("generic created: attributes %v, want the personal value resolved", entries[3].NewValue["attributes"])
	}

	// With grant none on the type the attributes are left out of before,
	// after and changed_fields, in both read paths.
	x.grant(t, "full", "wp770-cables", "", "none")
	restricted := request(f.OrgCtx(f.OrgA), policyOf(t, x, assignment{role: "full"}), identity.PrincipalTypeUser, audit.SourceUI, "read")
	projected, _ := changes(restricted, "/api/v1/relationships/"+id+"/changes")
	if len(projected) != 4 {
		t.Fatalf("projected rows: %d", len(projected))
	}
	for _, r := range projected {
		for _, edge := range []map[string]any{r.Before, r.After} {
			if _, ok := edge["attributes"]; ok {
				t.Errorf("%s: attributes visible without read right: %v", r.ChangeKind, edge)
			}
		}
		if slices.Contains(r.ChangedFields, "attributes") {
			t.Errorf("%s: changed_fields %v names attributes without read right", r.ChangeKind, r.ChangedFields)
		}
	}
	for _, e := range generic(restricted, id) {
		for _, v := range []map[string]any{e.OldValue, e.NewValue} {
			if _, ok := v["attributes"]; ok {
				t.Errorf("generic %s: attributes visible without read right: %v", e.ChangeType, v)
			}
		}
	}
	x.reset(t)

	// removed: the history stays visible to a principal of client 1.
	if w = call(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "req-delete"), http.MethodDelete, "/api/v1/relationships/"+id, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE: %d %s", w.Code, w.Body.String())
	}
	client1 := request(f.ClientCtx(f.Client1), unrestricted, identity.PrincipalTypeUser, audit.SourceUI, "read")
	rows, _ = changes(client1, "/api/v1/relationships/"+id+"/changes")
	if len(rows) != 5 {
		t.Fatalf("history of the deleted edge for client 1: %d rows", len(rows))
	}
	checkRow("removed", &rows[0], relationship.ChangeRemoved, []string{}, "user", audit.SourceUI, "req-delete")
	if rows[0].After != nil || rows[0].Before["id"] != id || rows[0].Before["notes"] != "n2" {
		t.Errorf("removed: before %v after %v", rows[0].Before, rows[0].After)
	}
	if got := types(eventsOf(id)); len(got) != 5 || got[4] != events.RelationshipRemoved {
		t.Errorf("events after DELETE: %v", got)
	}

	// An edge with an endpoint of client 2 and a projected id are 404.
	cross := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: x.cis["a1"], TargetCIID: x.cis["b1"], RelType: "wp770-cables"}
	if err = x.repo.Create(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "req-cross"), cross); err != nil {
		t.Fatal(err)
	}
	if w = call(client1, http.MethodGet, "/api/v1/relationships/"+cross.ID+"/changes", ""); w.Code != http.StatusNotFound {
		t.Errorf("history of an edge to client 2 for client 1: %d %s", w.Code, w.Body.String())
	}
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET parent_ci_id = $1 WHERE id = $2`, x.cis["a1"], x.cis["a2"]); err != nil {
		t.Fatal(err)
	}
	var projectedID string
	if err = f.Admin.QueryRow(bg, `SELECT md5('contains:' || $1::text || ':' || $2::text)::uuid::text`, x.cis["a1"], x.cis["a2"]).Scan(&projectedID); err != nil {
		t.Fatal(err)
	}
	listed := x.list(t, orgReq(identity.PrincipalTypeUser, audit.SourceUI, "read"), "/api/v1/cis/"+x.cis["a1"]+"/relationships")
	if _, ok := listed[projectedID]; !ok {
		t.Fatalf("projected edge %s is not listed", projectedID)
	}
	if w = call(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "read"), http.MethodGet, "/api/v1/relationships/"+projectedID+"/changes", ""); w.Code != http.StatusNotFound {
		t.Errorf("history of a projected edge: %d %s", w.Code, w.Body.String())
	}
	if n := countRows(projectedID); n != 0 {
		t.Errorf("projected edge has %d history rows", n)
	}

	// A new discovery edge without request context: created by the
	// collector, actor system.
	discovered := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: x.cis["a1"], TargetCIID: x.cis["ow"], RelType: "wp770-cables",
		Source: "discovery"}
	if err = x.repo.Create(tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA}), discovered); err != nil {
		t.Fatal(err)
	}
	rows, _ = changes(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "read"), "/api/v1/relationships/"+discovered.ID+"/changes")
	if len(rows) != 1 {
		t.Fatalf("discovery edge: %d rows", len(rows))
	}
	checkRow("discovery created", &rows[0], relationship.ChangeCreated, []string{}, "system", audit.SourceCollector, "")
	if rows[0].After["source"] != "discovery" || rows[0].After["first_seen_at"] == nil || rows[0].After["last_seen_at"] == nil {
		t.Errorf("discovery created: after %v", rows[0].After)
	}
	// DELETE without force stays 409 without a row; with force the edge is
	// suppressed.
	if w = call(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "req-noforce"), http.MethodDelete, "/api/v1/relationships/"+discovered.ID, ""); w.Code != http.StatusConflict {
		t.Fatalf("DELETE without force: %d %s", w.Code, w.Body.String())
	}
	if n := countRows(discovered.ID); n != 1 {
		t.Errorf("rows after a refused DELETE: %d, want 1", n)
	}
	if w = call(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "req-force"), http.MethodDelete, "/api/v1/relationships/"+discovered.ID+"?force=true&reason=gone", ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE with force: %d %s", w.Code, w.Body.String())
	}
	rows, _ = changes(orgReq(identity.PrincipalTypeUser, audit.SourceUI, "read"), "/api/v1/relationships/"+discovered.ID+"/changes")
	if len(rows) != 2 {
		t.Fatalf("suppressed edge: %d rows", len(rows))
	}
	checkRow("suppressed", &rows[0], relationship.ChangeSuppressed, []string{}, "user", audit.SourceUI, "req-force")
	if rows[0].After != nil || rows[0].Before["source"] != "discovery" {
		t.Errorf("suppressed: before %v after %v", rows[0].Before, rows[0].After)
	}
	var suppressions int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM relationship_suppression WHERE source_ci_id = $1 AND target_ci_id = $2`,
		x.cis["a1"], x.cis["ow"]).Scan(&suppressions); err != nil || suppressions != 1 {
		t.Errorf("suppressions: %d %v", suppressions, err)
	}
	if got := types(eventsOf(discovered.ID)); !slices.Equal(got, []string{events.RelationshipCreated, events.RelationshipRemoved}) {
		t.Errorf("events of the discovery edge: %v", got)
	}

	// A change of the client of an endpoint re-scopes its rows.
	if _, err = f.Admin.Exec(bg, `UPDATE ci SET parent_ci_id = NULL, client_id = $1 WHERE id = $2`, f.Client2, x.cis["a2"]); err != nil {
		t.Fatal(err)
	}
	var stale int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM relationship_change WHERE relationship_id = $1 AND target_client_id IS DISTINCT FROM $2::uuid`,
		id, f.Client2).Scan(&stale); err != nil || stale != 0 {
		t.Errorf("rows without the new client of the target: %d %v", stale, err)
	}
	if w = call(client1, http.MethodGet, "/api/v1/relationships/"+id+"/changes", ""); w.Code != http.StatusNotFound {
		t.Errorf("history after the target moved to client 2, for client 1: %d %s", w.Code, w.Body.String())
	}
}

// policyOf is the field policy of an org-wide principal with the
// assignments, resolved from the stored field permissions.
func policyOf(t *testing.T, x *relRightsFixture, assignments ...assignment) *permission.FieldPolicy {
	t.Helper()
	policy, err := permission.PolicyFrom(x.ctx(t, assignments...))
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// TestRelationshipChangesMemoryProfile: the memory profile has no history
// and no outbox; its mutations work and the history route answers an empty
// list (WP-296).
func TestRelationshipChangesMemoryProfile(t *testing.T) {
	repo := relationship.NewMemoryRepository()
	mux := chi.NewRouter()
	relationship.NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "00000000-0000-4000-8000-000000000001"})
	rel := &relationship.Relationship{OrganizationID: "00000000-0000-4000-8000-000000000001", SourceCIID: "00000000-0000-4000-8000-000000000002",
		TargetCIID: "00000000-0000-4000-8000-000000000003", RelType: "depends_on"}
	if err := repo.Create(ctx, rel); err != nil {
		t.Fatal(err)
	}
	notes := "changed"
	if _, err := repo.Update(ctx, rel.OrganizationID, rel.ID, relationship.UpdateRequest{Notes: &notes}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{rel.ID, "00000000-0000-4000-8000-0000000000ff"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/relationships/"+id+"/changes", http.NoBody).WithContext(ctx))
		var body struct {
			Data  []json.RawMessage `json:"data"`
			Total int               `json:"total"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.Data == nil || len(body.Data) != 0 || body.Total != 0 {
			t.Errorf("GET changes of %s: %d %s", id, w.Code, w.Body.String())
		}
	}
	if err := repo.Delete(ctx, rel.OrganizationID, rel.ID); err != nil {
		t.Fatal(err)
	}
}
