package ci_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestBulkStatementBudget bounds the statements and transactions a bulk
// sends per CI (NFR-02, bulk_500_duration and bulk_fields_500_duration of
// load run 42619469662: 500 ids in 40 to 60 s on 4 vCPU, budget 30 s). The
// repository is composed as in cmd/server (audit in the transaction,
// outbox, computed attributes, search index after the commit).
//
// The reads of Service.Update (the CI, its overrides, the field
// definitions of its type, its instance fields) join the transaction of
// the write (bulkUpdate): per CI one write transaction and the search
// index after its commit. Before, each read opened a transaction of its
// own: six transactions and 26 to 27 statements per CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestBulkStatementBudget(t *testing.T) {
	f := scopetest.Seed(t, "b9")
	bg := context.Background()
	const n = 10
	rows, err := f.Admin.Query(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status)
		SELECT $1, $2, (SELECT id FROM ci_type WHERE key = 'server' AND organization_id IS NULL), 'bulk-budget-' || g, 'active'
		FROM generate_series(1, $3::int) g RETURNING id::text`, f.OrgA, f.Client1, n)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()

	trace := scopetest.NewQueryTrace(t, f.App)
	types := citype.NewPGRepository(trace.Pool)
	repo := ci.NewIndexingRepository(
		ci.NewPGRepositoryWithAudit(trace.Pool, audit.NewPGRecorder()).WithEvents(events.NewOutbox()).WithComputed(types),
		search.NewCIIndexer(search.NewPGRepository(trace.Pool)))
	svc := ci.NewService(repo).WithFieldResolver(types)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	// Budget per CI: 2 transactions, 19 statements (BEGIN with the tenant
	// GUCs and COMMIT included; the SET ROLE of a new connection not).
	const txBudget, stmtBudget = 2, 19
	cases := []struct {
		name string
		req  *ci.BulkRequest
	}{
		{"set status", &ci.BulkRequest{Action: ci.BulkSetStatus, IDs: ids, Status: ci.StatusMaintenance}},
		{"set_fields", &ci.BulkRequest{Action: ci.BulkSetFields, IDs: ids, Params: json.RawMessage(`{"fields":{"firmware_version":"budget-1"}}`)}},
	}
	for _, c := range cases {
		// The pool opens its connections first: their SET ROLE is no
		// statement of the bulk.
		if err = trace.Pool.Ping(bg); err != nil {
			t.Fatal(err)
		}
		var res *ci.BulkResult
		stmts := trace.Run(t, func() error {
			var berr error
			res, _, berr = svc.Bulk(ctx, f.OrgA, c.req)
			return berr
		})
		if res == nil || res.Succeeded != n {
			t.Fatalf("%s: %+v", c.name, res)
		}
		var sent, txs int
		for _, s := range stmts {
			sql := strings.TrimSpace(s.SQL)
			if strings.HasPrefix(sql, "SET ROLE") {
				continue
			}
			sent++
			if strings.HasPrefix(sql, "BEGIN") {
				txs++
			}
		}
		t.Logf("%s of %d CIs: %d statements, %d transactions", c.name, n, sent, txs)
		if txs > txBudget*n {
			t.Errorf("%s: %d transactions for %d CIs, budget %d per CI", c.name, txs, n, txBudget)
		}
		if sent > stmtBudget*n {
			t.Errorf("%s: %d statements for %d CIs, budget %d per CI", c.name, sent, n, stmtBudget)
		}
	}
}
