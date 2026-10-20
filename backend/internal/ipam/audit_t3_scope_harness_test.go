package ipam_test

// Phase-1 acceptance audit, group T3-data-ingest: test harness (no test of
// its own) of the client-scope tests of ipam against PostgreSQL. Used by
// TestAuditT3VLANReferenceMustBeVisible and TestVLANReferenceStaysWithinTheClient
// (vlan_integration_test.go, WP-371) and by the defect test
// TestAuditT3VRFWritesNeedOrgScope (audit_t3_client_scope_auditdefect_test.go,
// until WP-372).

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// auditT3Site inserts a site of client in organization A through the
// maintenance pool and returns its id.
func auditT3Site(t *testing.T, f *scopetest.Fixture, client, name string) string {
	t.Helper()
	var id string
	if err := f.Admin.QueryRow(context.Background(), `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
		f.OrgA, client, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// auditT3Str returns the single text value sql selects through the
// maintenance pool.
func auditT3Str(t *testing.T, f *scopetest.Fixture, sql string, args ...any) string {
	t.Helper()
	var s string
	if err := f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}
