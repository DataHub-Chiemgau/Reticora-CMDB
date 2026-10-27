package audit_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// Route snapshots (AUD-07, WP-505): the route audit stores per changed
// column {field, old, new}; secrets never appear, personal values only as
// the surrogate of their row.

func valueMap(t *testing.T, values []map[string]any) map[string][2]any {
	t.Helper()
	out := map[string][2]any{}
	for _, v := range values {
		field, _ := v["field"].(string)
		if _, dup := out[field]; dup || field == "" {
			t.Fatalf("value list %v: field %q twice or empty", values, field)
		}
		out[field] = [2]any{v["old"], v["new"]}
	}
	return out
}

func TestRouteSnapshotValueChanges(t *testing.T) {
	spec, ok := audit.RouteSnapshotFor("PATCH /api/v1/vlans/{id}")
	if !ok || spec.Table != "vlan" {
		t.Fatalf("register entry of PATCH /vlans/{id}: %+v %v", spec, ok)
	}
	before := map[string]any{"id": "v1", "organization_id": "o1", "name": `audit "old"`, "vlan_id": 217.0,
		"description": nil, "updated_at": "t1", "created_at": "t0"}
	after := map[string]any{"id": "v1", "organization_id": "o1", "name": `audit "new"`, "vlan_id": 217.0,
		"description": "now set", "updated_at": "t2", "created_at": "t0"}
	got := valueMap(t, spec.ValueChanges("", before, after))
	want := map[string][2]any{"name": {`audit "old"`, `audit "new"`}, "description": {nil, "now set"}}
	if len(got) != len(want) || got["name"] != want["name"] || got["description"] != want["description"] {
		t.Errorf("update values = %v, want %v (unchanged and bookkeeping columns left out)", got, want)
	}
	// The list is sorted by field.
	if list := spec.ValueChanges("", before, after); list[0]["field"] != "description" {
		t.Errorf("values not sorted: %v", list)
	}

	// A create stores the new values, a delete the old ones.
	created := valueMap(t, spec.ValueChanges("", nil, after))
	if created["name"] != [2]any{nil, `audit "new"`} || created["vlan_id"] != [2]any{nil, 217.0} || len(created) != 4 {
		t.Errorf("create values = %v", created)
	}
	deleted := valueMap(t, spec.ValueChanges("", before, nil))
	if deleted["name"] != [2]any{`audit "old"`, nil} || deleted["id"] != [2]any{"v1", nil} || len(deleted) != 3 {
		t.Errorf("delete values = %v (the null description is no change)", deleted)
	}
	if n := len(spec.ValueChanges("", before, before)); n != 0 {
		t.Errorf("unchanged row: %d values", n)
	}
}

func TestRouteSnapshotPersonalValuesAreSurrogates(t *testing.T) {
	spec, _ := audit.RouteSnapshotFor("PATCH /api/v1/contacts/{id}")
	const id = "11111111-2222-4333-8444-555555555555"
	before := map[string]any{"id": id, "display_name": "Erika Muster", "email": "erika@example.com", "phone": nil,
		"role": "dba", "pii_subject_id": nil}
	after := map[string]any{"id": id, "display_name": "Erika Mustermann", "email": "erika.m@example.com",
		"phone": "+49 30 1234", "role": "lead", "pii_subject_id": nil}
	values := spec.ValueChanges("", before, after)
	raw, _ := json.Marshal(values)
	for _, plain := range []string{"Erika", "erika", "+49"} {
		if strings.Contains(string(raw), plain) {
			t.Errorf("contact values hold %q: %s", plain, raw)
		}
	}
	got := valueMap(t, values)
	sur := map[string]any{audit.SurrogateKey: id}
	for _, field := range []string{"display_name", "email"} {
		if !jsonEqual(got[field][0], sur) || !jsonEqual(got[field][1], sur) {
			t.Errorf("%s = %v, want the contact id as surrogate", field, got[field])
		}
	}
	// A value that is set or cleared keeps null on its side.
	if got["phone"][0] != nil || !jsonEqual(got["phone"][1], sur) {
		t.Errorf("phone = %v", got["phone"])
	}
	if got["role"] != [2]any{"dba", "lead"} {
		t.Errorf("role = %v: other columns keep their values", got["role"])
	}

	// Personal field names are personal in every table (internal/pii).
	user, _ := audit.RouteSnapshotFor("PATCH /api/v1/users/{id}")
	raw, _ = json.Marshal(user.ValueChanges("", map[string]any{"id": id, "email": "a@example.com", "oidc_subject": "sub-a"},
		map[string]any{"id": id, "email": "b@example.com", "oidc_subject": "sub-b"}))
	if strings.Contains(string(raw), "example.com") || strings.Contains(string(raw), "sub-") {
		t.Errorf("user values hold personal data: %s", raw)
	}
}

func TestRouteSnapshotSecretsNeverAppear(t *testing.T) {
	for route, row := range map[string]map[string]any{
		"POST /api/v1/webhooks": {"id": "w1", "name": "hook", "secret": "s3cr3t-value", "previous_secret": "s3cr3t-old",
			"headers": map[string]any{"Authorization": "Bearer s3cr3t-header"}},
		"POST /api/v1/notification-channels":    {"id": "c1", "name": "mail", "config_ciphertext": "s3cr3t-cipher", "config_dek": "s3cr3t-dek"},
		"POST /api/v1/credentials/":             {"id": "k1", "name": "snmp", "ciphertext": "s3cr3t-cred"},
		"POST /api/v1/agents/enrollment-tokens": {"id": "e1", "token_hash": "s3cr3t-hash", "description": "lab"},
		"POST /api/v1/iga/connectors":           {"id": "i1", "name": "idp", "config": map[string]any{"password": "s3cr3t-idp"}},
	} {
		spec, ok := audit.RouteSnapshotFor(route)
		if !ok || spec.Table == "" {
			t.Fatalf("%s: no table in the register", route)
		}
		raw, _ := json.Marshal(spec.ValueChanges("", nil, row))
		if strings.Contains(string(raw), "s3cr3t") {
			t.Errorf("%s: values hold a secret: %s", route, raw)
		}
		if !strings.Contains(string(raw), `"field":"id"`) {
			t.Errorf("%s: values lost the other columns: %s", route, raw)
		}
	}
}

func TestRouteSnapshotBoundsLargeValues(t *testing.T) {
	spec, _ := audit.RouteSnapshotFor("PATCH /api/v1/documents/{id}")
	big := strings.Repeat("x", 9<<10)
	got := valueMap(t, spec.ValueChanges("", map[string]any{"id": "d1", "description": "short"},
		map[string]any{"id": "d1", "description": big}))
	if got["description"][0] != "short" || !jsonEqual(got["description"][1], map[string]any{"$omitted": len(big) + 2}) {
		t.Errorf("large value = %v", got["description"])
	}
}

func TestRouteSnapshotKeyValues(t *testing.T) {
	const id = "11111111-2222-4333-8444-555555555555"
	lookup := func(values map[string]string) func(string) string {
		return func(from string) string { return values[from] }
	}
	spec, _ := audit.RouteSnapshotFor("PATCH /api/v1/vlans/{id}")
	if keys, ok := spec.KeyValues(lookup(map[string]string{"id": id})); !ok || len(keys) != 1 || keys[0] != id {
		t.Errorf("keys of PATCH /vlans/{id} = %v %v", keys, ok)
	}
	// An id that is no UUID names no row.
	if _, ok := spec.KeyValues(lookup(map[string]string{"id": "not-a-uuid"})); ok {
		t.Error("a non-UUID id resolved")
	}
	create, _ := audit.RouteSnapshotFor("POST /api/v1/vlans")
	if !create.FromResponse() || spec.FromResponse() {
		t.Error("FromResponse: the create reads the row the response names, the update the route's")
	}
	if keys, ok := create.KeyValues(lookup(map[string]string{"@id": id})); !ok || keys[0] != id {
		t.Errorf("keys of the create = %v %v", keys, ok)
	}
	rel, _ := audit.RouteSnapshotFor("PATCH /api/v1/relationship-types/{key}")
	if keys, ok := rel.KeyValues(lookup(map[string]string{"key": "runs_on", "$org": id})); !ok || keys[0] != "runs_on" || keys[1] != id {
		t.Errorf("keys of a relationship type = %v %v", keys, ok)
	}
	none, _ := audit.RouteSnapshotFor("POST /api/v1/search/reindex")
	if _, ok := none.KeyValues(lookup(map[string]string{"id": id})); ok || none.NoValues == "" {
		t.Errorf("a route without row values resolved keys: %+v", none)
	}
}

func TestRouteSnapshotRegisterIsValid(t *testing.T) {
	routes := audit.RouteSnapshotRoutes()
	if len(routes) < 200 {
		t.Fatalf("only %d register entries", len(routes))
	}
	tables := 0
	for _, route := range routes {
		spec, _ := audit.RouteSnapshotFor(route)
		if err := spec.Validate(); err != nil {
			t.Errorf("%s: %v", route, err)
		}
		if spec.Table != "" {
			tables++
		}
	}
	if tables < len(routes)-12 {
		t.Errorf("only %d of %d entries name a table", tables, len(routes))
	}
	for _, bad := range []audit.RouteSnapshot{
		{},
		{Table: "vlan"},
		{Table: "vlan; drop table vlan", Keys: []audit.SnapshotKey{{Column: "id", From: "id"}}},
		{Table: "vlan", Keys: []audit.SnapshotKey{{Column: "id", From: "id"}}, NoValues: "both"},
		{NoValues: "reason", Keys: []audit.SnapshotKey{{Column: "id", From: "id"}}},
	} {
		if bad.Validate() == nil {
			t.Errorf("invalid entry %+v passes", bad)
		}
	}
}

// TestRouteSnapshotQueriesOnPostgreSQL runs the statement of every register
// entry with a table as the application role in a tenant scope: tables,
// key columns and selects exist (no row for unknown keys, no error). A row
// of client 2 is no row for a principal restricted to client 1, a row of
// organization A none for organization B (RLS of the principal's scope).
func TestRouteSnapshotQueriesOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "2a")
	auditor := audit.NewPGRouteAuditor(f.App)
	for _, route := range audit.RouteSnapshotRoutes() {
		spec, _ := audit.RouteSnapshotFor(route)
		if spec.Table == "" {
			continue
		}
		keys, ok := spec.KeyValues(func(from string) string {
			if from == "$org" {
				return f.OrgA
			}
			return f.ID()
		})
		if !ok {
			t.Errorf("%s: keys did not resolve", route)
			continue
		}
		row, _, err := auditor.RouteSnapshot(f.OrgCtx(f.OrgA), f.OrgA, &spec, keys)
		// The organization's own settings row exists.
		if err != nil || (row != nil && spec.Keys[0].From != "$org") {
			t.Errorf("%s: snapshot of unknown keys = %v, %v", route, row, err)
		}
	}

	bg := context.Background()
	contact := func(client, role string) string {
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO contact (organization_id, client_id, role) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, client, role).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	own, foreign := contact(f.Client1, "snap-c1"), contact(f.Client2, "snap-c2")
	spec, _ := audit.RouteSnapshotFor("PATCH /api/v1/contacts/{id}")
	read := func(ctx context.Context, org, id string) map[string]any {
		t.Helper()
		row, table, err := auditor.RouteSnapshot(ctx, org, &spec, []any{id})
		if err != nil {
			t.Fatalf("snapshot of %s: %v", id, err)
		}
		if row != nil && table != "contact" {
			t.Errorf("table = %q", table)
		}
		return row
	}
	if row := read(f.ClientCtx(f.Client1), f.OrgA, own); row == nil || row["role"] != "snap-c1" {
		t.Errorf("own contact = %v", row)
	}
	if row := read(f.ClientCtx(f.Client1), f.OrgA, foreign); row != nil {
		t.Errorf("a principal of client 1 read the contact of client 2: %v", row)
	}
	if row := read(f.OrgCtx(f.OrgB), f.OrgB, own); row != nil {
		t.Errorf("organization B read a contact of organization A: %v", row)
	}
	// Without the principal's scope there is no read (no org-wide fallback).
	if _, _, err := auditor.RouteSnapshot(bg, f.OrgA, &spec, []any{own}); err == nil {
		t.Error("snapshot without a tenant scope succeeded")
	}
}

func jsonEqual(a, b any) bool {
	ra, _ := json.Marshal(a)
	rb, _ := json.Marshal(b)
	return string(ra) == string(rb)
}
