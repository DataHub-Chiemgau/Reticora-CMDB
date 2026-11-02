package relationship_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestRelationshipProvenanceAndSourcePolicy covers WP-583 (REL-04, REL-08)
// against PostgreSQL as the application role: the provenance of an edge
// cannot be overwritten by PATCH (F-270), a user POST cannot claim the
// system sources discovery and agent (F-271), and DELETE answers 404 only
// for an edge it does not find (F-272).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRelationshipProvenanceAndSourcePolicy(t *testing.T) {
	f := scopetest.Seed(t, "ac")
	bg := context.Background()
	a, b, c := f.CI(t, f.OrgA, "", "wp583-a"), f.CI(t, f.OrgA, "", "wp583-b"), f.CI(t, f.OrgA, "", "wp583-c")
	foreignA, foreignB := f.CI(t, f.OrgB, "", "wp583-b-a"), f.CI(t, f.OrgB, "", "wp583-b-b")
	rels := relationship.NewPGRepository(f.App).WithEvents(events.NewOutbox())
	mux := chi.NewRouter()
	relationship.NewHandler(rels).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			if raw, err = json.Marshal(body); err != nil {
				t.Fatal(err)
			}
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(raw)).WithContext(ctx))
		return w
	}
	violations := func(w *httptest.ResponseRecorder) []string {
		t.Helper()
		var problem struct {
			Violations []struct {
				Field string `json:"field"`
			} `json:"violations"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		var fields []string
		for _, v := range problem.Violations {
			fields = append(fields, v.Field)
		}
		return fields
	}
	scalar := func(query string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return out
	}

	// F-271: discovery and agent are reserved to their paths; the user
	// sources are created as before (control).
	for _, source := range []string{"discovery", "agent", "Discovery"} {
		w := call(http.MethodPost, "/api/v1/relationships", map[string]any{"source_ci_id": c, "target_ci_id": a, "rel_type": "depends_on", "source": source})
		if w.Code != http.StatusUnprocessableEntity || !slices.Equal(violations(w), []string{"source"}) {
			t.Errorf("user POST source=%s: %d %s, want 422 on source", source, w.Code, w.Body.String())
		}
	}
	if n := scalar(`SELECT count(*)::text FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2`, c, a); n != "0" {
		t.Errorf("refused POSTs stored %s edges", n)
	}
	created := map[string]relationship.Relationship{}
	for i, source := range []string{"manual", "api", "import", "integration", "workflow"} {
		target := []string{b, c}[i%2]
		w := call(http.MethodPost, "/api/v1/relationships", map[string]any{"source_ci_id": a, "target_ci_id": target,
			"rel_type": []string{"depends_on", "uses", "monitors", "backs_up", "manages"}[i], "source": source,
			"attributes": map[string]any{"port": "eth0"}})
		var rel relationship.Relationship
		if w.Code != http.StatusCreated || json.Unmarshal(w.Body.Bytes(), &rel) != nil || rel.Source != source {
			t.Fatalf("POST source=%s: %d %s, want 201", source, w.Code, w.Body.String())
		}
		created[source] = rel
	}
	manual := created["manual"]
	if p, _ := manual.Attributes["provenance"].(map[string]any); p["actor"] != f.User || p["source"] != "manual" {
		t.Fatalf("provenance of the manual edge: %v", manual.Attributes)
	}

	// F-270: PATCH with attributes.provenance is 422 and changes nothing
	// in the database, not even updated_at.
	row := `SELECT attributes::text || '|' || updated_at::text || '|' || COALESCE(notes, '') FROM ci_relationship WHERE id = $1`
	before := scalar(row, manual.ID)
	for _, body := range []map[string]any{
		{"attributes": map[string]any{"provenance": map[string]any{"actor": "forged", "at": "1999-01-01T00:00:00Z"}}},
		{"attributes": map[string]any{"provenance": nil}},
		{"attributes": map[string]any{"port": "eth1", "provenance": "forged"}, "notes": "forged"},
	} {
		w := call(http.MethodPatch, "/api/v1/relationships/"+manual.ID, body)
		if w.Code != http.StatusUnprocessableEntity || !slices.Equal(violations(w), []string{"attributes.provenance"}) {
			t.Errorf("PATCH %v: %d %s, want 422 on attributes.provenance", body, w.Code, w.Body.String())
		}
	}
	if after := scalar(row, manual.ID); after != before {
		t.Errorf("refused PATCHes changed the edge:\n before %s\n after  %s", before, after)
	}
	provenance := `SELECT attributes->'provenance' FROM ci_relationship WHERE id = $1`
	var stored, got json.RawMessage
	if err := f.Admin.QueryRow(bg, provenance, manual.ID).Scan(&stored); err != nil || len(stored) == 0 {
		t.Fatalf("stored provenance: %s %v", stored, err)
	}
	// Other attributes are merged; the repository keeps the stored
	// provenance also when a caller passes one (defence in depth).
	if w := call(http.MethodPatch, "/api/v1/relationships/"+manual.ID, map[string]any{"attributes": map[string]any{"port": "eth1"}}); w.Code != http.StatusOK {
		t.Fatalf("PATCH port: %d %s", w.Code, w.Body.String())
	}
	if _, err := rels.Update(ctx, f.OrgA, manual.ID, relationship.UpdateRequest{Attributes: map[string]any{"provenance": "forged", "port": "eth2"}}); err != nil {
		t.Fatalf("repository update: %v", err)
	}
	if err := f.Admin.QueryRow(bg, provenance, manual.ID).Scan(&got); err != nil || !bytes.Equal(got, stored) {
		t.Errorf("provenance after updates: %s, want %s (%v)", got, stored, err)
	}
	if port := scalar(`SELECT attributes->>'port' FROM ci_relationship WHERE id = $1`, manual.ID); port != "eth2" {
		t.Errorf("port = %s, want eth2", port)
	}

	// F-272: a discovery edge whose type has no relationship_type row
	// cannot be suppressed; the forced DELETE is 422 on rel_type (before
	// the fix 404), the edge stays and nothing is suppressed or announced.
	untyped := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: b, TargetCIID: c, RelType: "wp583_untyped",
		Source: "discovery", VerificationState: "unverified"}
	if err := rels.Create(ctx, untyped); err != nil {
		t.Fatal(err)
	}
	refused := call(http.MethodDelete, "/api/v1/relationships/"+untyped.ID+"?force=true", nil)
	if refused.Code != http.StatusUnprocessableEntity || !slices.Equal(violations(refused), []string{"rel_type"}) {
		t.Errorf("forced DELETE of a discovery edge without relationship_type row: %d %s, want 422 on rel_type", refused.Code, refused.Body.String())
	}
	if n := scalar(`SELECT (SELECT count(*) FROM ci_relationship WHERE id = $1) || '/' ||
		(SELECT count(*) FROM relationship_suppression WHERE source_ci_id = $2 AND target_ci_id = $3) || '/' ||
		(SELECT count(*) FROM event_outbox WHERE type = 'relationship.removed' AND entity_id::text = $4)`, untyped.ID, b, c, untyped.ID); n != "1/0/0" {
		t.Errorf("refused forced DELETE: edge/suppressions/events = %s, want 1/0/0", n)
	}

	// DELETE: 404 only for an edge the caller does not find; 409 for a
	// discovery edge without force; with force 204 and a suppression.
	foreign := &relationship.Relationship{OrganizationID: f.OrgB, SourceCIID: foreignA, TargetCIID: foreignB, RelType: "depends_on", Source: "manual"}
	if err := rels.Create(tenant.WithTenant(f.OrgCtx(f.OrgB), tenant.TenantInfo{OrganizationID: f.OrgB}), foreign); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{f.ID(), foreign.ID, "not-a-uuid"} {
		if w := call(http.MethodDelete, "/api/v1/relationships/"+id+"?force=true", nil); w.Code != http.StatusNotFound {
			t.Errorf("DELETE %s: %d %s, want 404", id, w.Code, w.Body.String())
		}
	}
	if n := scalar(`SELECT count(*)::text FROM ci_relationship WHERE id = $1`, foreign.ID); n != "1" {
		t.Errorf("the edge of organization B was deleted")
	}
	discovered := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: b, TargetCIID: c, RelType: "connected_to",
		Source: "discovery", VerificationState: "unverified"}
	if err := rels.Create(ctx, discovered); err != nil {
		t.Fatal(err)
	}
	if w := call(http.MethodDelete, "/api/v1/relationships/"+discovered.ID, nil); w.Code != http.StatusConflict {
		t.Errorf("DELETE of a discovery edge without force: %d %s, want 409", w.Code, w.Body.String())
	}
	if w := call(http.MethodDelete, "/api/v1/relationships/"+discovered.ID+"?force=true", nil); w.Code != http.StatusNoContent {
		t.Errorf("forced DELETE of a discovery edge: %d %s, want 204", w.Code, w.Body.String())
	}
	if n := scalar(`SELECT count(*)::text FROM relationship_suppression s JOIN relationship_type rt ON rt.id = s.relationship_type_id
		WHERE s.source_ci_id = $1 AND s.target_ci_id = $2 AND rt.key = 'connected_to'`, b, c); n != "1" {
		t.Errorf("suppressions after the forced DELETE: %s, want 1", n)
	}
	for source, rel := range created {
		if w := call(http.MethodDelete, "/api/v1/relationships/"+rel.ID, nil); w.Code != http.StatusNoContent {
			t.Errorf("DELETE of the %s edge without force: %d %s, want 204", source, w.Code, w.Body.String())
		}
	}

	// Any other failure goes through api.WriteRepoError: a repository on
	// a closed pool answers 500 without internals, not 404.
	closed, err := pgxpool.New(bg, scopetest.AppDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	broken := chi.NewRouter()
	relationship.NewHandler(relationship.NewPGRepository(closed)).RegisterRoutes(broken)
	failed := httptest.NewRecorder()
	broken.ServeHTTP(failed, httptest.NewRequest(http.MethodDelete, "/api/v1/relationships/"+untyped.ID+"?force=true", nil).WithContext(ctx))
	if failed.Code != http.StatusInternalServerError || bytes.Contains(failed.Body.Bytes(), []byte("pool")) {
		t.Errorf("DELETE on a closed pool: %d %s, want 500 without internals", failed.Code, failed.Body.String())
	}
}
