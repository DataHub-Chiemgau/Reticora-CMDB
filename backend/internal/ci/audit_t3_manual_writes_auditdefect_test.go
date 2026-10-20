//go:build auditdefect

package ci_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed). The harness (auditT3CIRouter) is in
// audit_t3_ci_harness_test.go. The bulk status change of WP-057/WP-061
// (F-020) is fixed by WP-368; its test TestAuditT3BulkStatusIsManualWrite
// is in bulk_integration_test.go.
//
// WP-061 (API-07, OVR-01) P1 "Override set/clear does not raise ci.version,
// so a stale If-Match PATCH silently overwrites the newer manual override":
// override/pg_repository.go SetOverride/ClearOverride -> syncCIField update
// the ci row without version = version + 1 and without a ci_change row, so
// checkPrecondition never sees the override.
//
// WP-057 (CI-04: JSON Merge Patch RFC 7396) P2 "PATCH null on top-level CI
// fields is ignored": UpdateRequest decodes null into a nil *string, which
// means "unchanged"; the client gets 200 and the value stays.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3OverrideWritesRaiseVersion|TestAuditT3MergePatchNullClearsTypedField' ./internal/ci/

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestAuditT3OverrideWritesRaiseVersion(t *testing.T) {
	r := newAuditT3CIRouter(t, "6b")

	t.Run("PUT override raises the version and blocks a stale PATCH of the same field", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-ovr-put")
		base := "/api/v1/cis/" + id
		g := r.call(http.MethodGet, base, "", "")
		etag0 := g.Header().Get("ETag")
		if g.Code != http.StatusOK || etag0 == "" {
			t.Fatalf("GET: %d etag=%q", g.Code, etag0)
		}
		if w := r.call(http.MethodPut, base+"/fields/model/override", "", `{"value":"manual-model","reason":"audit","protected":true}`); w.Code != http.StatusOK {
			t.Fatalf("PUT override: %d %s", w.Code, w.Body.String())
		}
		if etag1 := r.call(http.MethodGet, base, "", "").Header().Get("ETag"); etag1 == etag0 {
			t.Errorf("ETag after PUT override = %s, unchanged (API-07: an override is a manual write and raises the version)", etag1)
		}
		w := r.call(http.MethodPatch, base, etag0, `{"model":"stale-model"}`)
		if w.Code != http.StatusConflict {
			t.Errorf("PATCH model with the If-Match read before the override (%s) = %d, want 409 fields=[model]", etag0, w.Code)
		} else if !strings.Contains(w.Body.String(), "model") {
			t.Errorf("409 body does not name the field model: %s", w.Body.String())
		}
		if got := r.str(`SELECT coalesce(model, '') FROM ci WHERE id = $1`, id); got != "manual-model" {
			t.Errorf("ci.model after the stale PATCH = %q, want the protected override manual-model (silent lost update)", got)
		}
		// A stale PATCH of a different field is not a conflict (field-granular API-07).
		if w := r.call(http.MethodPatch, base, etag0, `{"hostname":"audit-t3-other-field"}`); w.Code != http.StatusOK {
			t.Errorf("PATCH of another field with the same If-Match = %d, want 200", w.Code)
		}
	})

	t.Run("DELETE override restoring the observed value raises the version", func(t *testing.T) {
		id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-ovr-del")
		base := "/api/v1/cis/" + id
		if w := r.call(http.MethodPut, base+"/fields/model/override", "", `{"value":"manual-model","reason":"audit"}`); w.Code != http.StatusOK {
			t.Fatalf("PUT override: %d %s", w.Code, w.Body.String())
		}
		if _, err := r.f.Admin.Exec(context.Background(), `UPDATE ci_field_value SET discovered_value = '"observed-model"',
			discovered_source = 'snmp', discovered_at = now(), discovered_rank = 80 WHERE ci_id = $1 AND field_name = 'model'`, id); err != nil {
			t.Fatal(err)
		}
		v0 := r.version(id)
		if w := r.call(http.MethodDelete, base+"/fields/model/override", "", ""); w.Code != http.StatusOK {
			t.Fatalf("DELETE override: %d %s", w.Code, w.Body.String())
		}
		if got := r.str(`SELECT coalesce(model, '') FROM ci WHERE id = $1`, id); got != "observed-model" {
			t.Fatalf("precondition: ci.model after clear = %q, want observed-model", got)
		}
		if v1 := r.version(id); v1 <= v0 {
			t.Errorf("version after clearing the override (effective value manual-model -> observed-model) = %d, want > %d", v1, v0)
		}
	})
}

func TestAuditT3MergePatchNullClearsTypedField(t *testing.T) {
	r := newAuditT3CIRouter(t, "6c")
	id := r.f.CI(t, r.f.OrgA, r.f.Client1, "audit-t3-null")
	if _, err := r.f.Admin.Exec(context.Background(), `UPDATE ci SET hostname = 'host-a', model = 'model-a', firmware_version = '1.2.3' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"hostname", "model", "firmware_version"} {
		t.Run(field, func(t *testing.T) {
			w := r.call(http.MethodPatch, "/api/v1/cis/"+id, "", `{"`+field+`":null}`)
			got := r.str(`SELECT coalesce(`+field+`, '<NULL>') FROM ci WHERE id = $1`, id)
			if w.Code != http.StatusOK || got != "<NULL>" {
				t.Errorf("PATCH {%q:null} = %d, stored %s = %q; want 200 and NULL (RFC 7396: null removes the member)", field, w.Code, field, got)
			}
		})
	}
}
