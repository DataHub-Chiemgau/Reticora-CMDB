//go:build auditdefect

package ci_test

// Phase-1 acceptance audit, group T3-data-ingest: DEFECT tests (they fail
// until the product is fixed). The harness (auditT3CIRouter) is in
// audit_t3_ci_harness_test.go. The bulk status change of WP-057/WP-061
// (F-020) is fixed by WP-368; its test TestAuditT3BulkStatusIsManualWrite
// is in bulk_integration_test.go. The override writes of WP-061 (F-021) are
// fixed by WP-412; their test TestAuditT3OverrideWritesRaiseVersion is in
// concurrency_integration_test.go.
//
// WP-057 (CI-04: JSON Merge Patch RFC 7396) P2 "PATCH null on top-level CI
// fields is ignored": UpdateRequest decodes null into a nil *string, which
// means "unchanged"; the client gets 200 and the value stays.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_03?sslmode=disable \
//     go test -tags auditdefect -count=1 -v -run 'TestAuditT3MergePatchNullClearsTypedField' ./internal/ci/

import (
	"context"
	"net/http"
	"testing"
)

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
