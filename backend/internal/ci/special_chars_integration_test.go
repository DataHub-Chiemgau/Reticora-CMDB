package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestCIWritesWithQuotesAndControlChars covers CI-03, MET-11 and AUD-04
// (WP-423, F-045) against PostgreSQL with the audit recorder of the server:
// create, PATCH, DELETE and restore of a CI with quotes, a backslash and TAB
// in the name and the model and with quotes, LF and TAB in a multiline
// attribute answer 201/200/204/200 and store the values (the audit chain
// refused them with 400; so it did the CI type of the test, a metamodel
// write with a quote in its name). The audit entries carry the values in their
// before/after snapshots, each stored hash equals audit.ComputeHash and the
// chain verifies. A line break in the name stays refused by its data type
// (text is single-line, MET-11): 422 on name, nothing stored.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIWritesWithQuotesAndControlChars(t *testing.T) {
	f := scopetest.Seed(t, "4f")
	bg := context.Background()
	ctx := audit.WithRequestInfo(
		tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}),
		audit.RequestInfo{RequestID: `req "423"\x`, Source: audit.SourceAPI})
	types := citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp423-rack", Name: `WP-423 19" Rack`}
	if err := types.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	if err := types.CreateAttributeDefinition(ctx, f.OrgA,
		&citype.AttributeDefinition{CITypeID: typ.ID, Key: "notes", DataType: "multiline"}); err != nil {
		t.Fatalf("define notes: %v", err)
	}
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())).WithFieldResolver(types)).
		RegisterRoutes(mux)
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, req.WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	stored := func(id string) (name, model, notes string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT name, COALESCE(model, ''), COALESCE(attributes->>'notes', '') FROM ci WHERE id = $1`,
			id).Scan(&name, &model, &notes); err != nil {
			t.Fatalf("read ci: %v", err)
		}
		return name, model, notes
	}

	name, notes := "lc \"Rack 7\"\tC:\\Racks", "Zeile 1\nZeile \"2\"\tTab \\ Ende"
	code, out := call(http.MethodPost, "/api/v1/cis", map[string]any{"name": name, "ci_type_id": typ.ID,
		"model": `R740 "XL"`, "attributes": map[string]any{"notes": notes}})
	id, _ := out["id"].(string)
	if code != http.StatusCreated || id == "" {
		t.Fatalf("create: %d %v", code, out)
	}
	if gotName, gotModel, gotNotes := stored(id); gotName != name || gotModel != `R740 "XL"` || gotNotes != notes {
		t.Errorf("stored after create: %q %q %q", gotName, gotModel, gotNotes)
	}

	newName, newNotes := "lc 'Rack 7' \"XL\"\t2", "neu\r\n\"zitiert\"\t\\n\n"
	code, out = call(http.MethodPatch, "/api/v1/cis/"+id, map[string]any{"name": newName, "model": "R750 \"XXL\"\t2HE",
		"attributes": map[string]any{"notes": newNotes}})
	if code != http.StatusOK {
		t.Fatalf("patch: %d %v", code, out)
	}
	if gotName, gotModel, gotNotes := stored(id); gotName != newName || gotModel != "R750 \"XXL\"\t2HE" || gotNotes != newNotes {
		t.Errorf("stored after patch: %q %q %q", gotName, gotModel, gotNotes)
	}
	if code, out = call(http.MethodDelete, "/api/v1/cis/"+id, nil); code != http.StatusNoContent {
		t.Fatalf("delete: %d %v", code, out)
	}
	if code, out = call(http.MethodPost, "/api/v1/cis/"+id+"/restore", nil); code != http.StatusOK || out["name"] != newName {
		t.Fatalf("restore: %d %v", code, out)
	}

	// A line break in the name is a data type violation, not an audit error.
	var before int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1`, f.OrgA).Scan(&before); err != nil {
		t.Fatal(err)
	}
	code, out = call(http.MethodPost, "/api/v1/cis", map[string]any{"name": "Zeile 1\nZeile 2", "ci_type_id": typ.ID})
	var field any
	if violations, _ := out["violations"].([]any); len(violations) == 1 {
		if v, ok := violations[0].(map[string]any); ok {
			field = v["field"]
		}
	}
	var after int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1`, f.OrgA).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if code != http.StatusUnprocessableEntity || field != "name" || after != before {
		t.Errorf("name with a line break: %d %v, CIs %d -> %d", code, out, before, after)
	}

	// The audit trail of the CI carries the values and verifies.
	rows, err := f.Admin.Query(bg, `SELECT id::text, organization_id::text, seq, timestamp, COALESCE(actor_id::text, ''),
		actor_type, action, resource_type, COALESCE(resource_id::text, ''), COALESCE(changes, '{}'::jsonb), source,
		COALESCE(request_id, ''), COALESCE(correlation_id, ''), previous_hash, hash
		FROM audit_log WHERE organization_id = $1 AND resource_id = $2 ORDER BY seq`, f.OrgA, id)
	if err != nil {
		t.Fatal(err)
	}
	var entries []audit.Entry
	for rows.Next() {
		var e audit.Entry
		var changes []byte
		if err := rows.Scan(&e.ID, &e.OrganizationID, &e.Seq, &e.Timestamp, &e.ActorID, &e.ActorType, &e.Action,
			&e.ResourceType, &e.ResourceID, &changes, &e.Source, &e.RequestID, &e.CorrelationID, &e.PreviousHash, &e.Hash); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(changes, &e.Changes); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	var actions []string
	for i := range entries {
		actions = append(actions, entries[i].Action)
		if audit.ComputeHash(&entries[i]) != entries[i].Hash {
			t.Errorf("%s: stored hash %s is not ComputeHash %s", entries[i].Action, entries[i].Hash, audit.ComputeHash(&entries[i]))
		}
		if entries[i].RequestID != `req "423"\x` {
			t.Errorf("%s: request_id %q", entries[i].Action, entries[i].RequestID)
		}
	}
	if strings.Join(actions, ",") != "ci.created,ci.updated,ci.deleted,ci.restored" {
		t.Fatalf("audit entries of the CI: %v", actions)
	}
	snapshot := func(e audit.Entry, key string) map[string]any {
		s, _ := e.Changes[key].(map[string]any)
		return s
	}
	attrs := func(s map[string]any) map[string]any {
		a, _ := s["attributes"].(map[string]any)
		return a
	}
	created, updatedBefore, updatedAfter, deleted := snapshot(entries[0], "after"), snapshot(entries[1], "before"),
		snapshot(entries[1], "after"), snapshot(entries[2], "before")
	if created["name"] != name || attrs(created)["notes"] != notes || updatedBefore["name"] != name ||
		updatedAfter["name"] != newName || attrs(updatedAfter)["notes"] != newNotes || deleted["name"] != newName ||
		attrs(deleted)["notes"] != newNotes {
		t.Errorf("snapshots: created %v, updated %v -> %v, deleted %v", created, updatedBefore, updatedAfter, deleted)
	}
	if result, err := audit.Verify(bg, f.App, f.OrgA); err != nil || !result.Intact {
		t.Errorf("audit chain: %+v %v", result, err)
	}
}
