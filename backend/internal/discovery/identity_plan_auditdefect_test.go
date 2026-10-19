//go:build auditdefect

// Phase-1 acceptance audit, group T4-collector-load – DEFECT test (must fail
// until the defect is fixed).
//
// WP-205 (NFR-01, REC-02 "indexbasiert"). Finding P2 "Identity search with
// MACs is an org-wide Seq Scan under RLS; on the queue path it runs per
// known record, not only per batch": identityFilter (pg_repository.go) adds
// `attributes @> ANY($n::jsonb[])` for the interface MACs; jsonb_contains is
// not leakproof, so below the RLS barrier of the application role the whole
// OR cannot become index conditions and PostgreSQL scans every CI of the
// organization for each incoming record.
//
// The test seeds 10,000 CIs into one organization, captures the SQL that
// PGRepository.IdentityCandidates really sends (pgx tracer on a copy of the
// application pool), and EXPLAINs it as the application role inside the
// tenant transaction. Requirement: only identity indexes serve the lookup
// (no Seq Scan on ci, no organization-wide index scan with a heap filter).
// The same lookup without MACs is the control: it shows that the setup lets
// the planner use the identity indexes under RLS.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -tags auditdefect -count=1 -v -p 1 -run 'TestAuditIdentitySearchWithMACsUsesIndexes' ./internal/discovery/
package discovery_test

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// auditSQLCapture records the identity candidate statements.
type auditSQLCapture struct {
	mu   sync.Mutex
	sql  []string
	args [][]any
}

func (c *auditSQLCapture) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "SELECT id::text, created_at FROM ci") {
		c.mu.Lock()
		c.sql = append(c.sql, data.SQL)
		c.args = append(c.args, append([]any(nil), data.Args...))
		c.mu.Unlock()
	}
	return ctx
}

func (c *auditSQLCapture) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *auditSQLCapture) last(t *testing.T) (string, []any) {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sql) == 0 {
		t.Fatal("IdentityCandidates sent no candidate query")
	}
	return c.sql[len(c.sql)-1], c.args[len(c.args)-1]
}

func TestAuditIdentitySearchWithMACsUsesIndexes(t *testing.T) {
	f := scopetest.Seed(t, "dd")
	bg := context.Background()
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	// 10,000 discovered CIs with serial, MAC, IP and an interface MAC
	// fingerprint, like a seeded inventory.
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name, serial_number, primary_mac, management_ip, attributes, discovery_source)
		SELECT $1, $2, 'audit-idx-' || i, 'AUDSN' || i,
			('02:00:' || lpad(to_hex((i >> 24) & 255), 2, '0') || ':' || lpad(to_hex((i >> 16) & 255), 2, '0') || ':' ||
				lpad(to_hex((i >> 8) & 255), 2, '0') || ':' || lpad(to_hex(i & 255), 2, '0'))::macaddr,
			('10.201.' || (i / 256) || '.' || (i % 256))::inet,
			jsonb_build_object('fingerprint', jsonb_build_object('mac_addresses', jsonb_build_array('06:00:00:00:' ||
				lpad(to_hex((i >> 8) & 255), 2, '0') || ':' || lpad(to_hex(i & 255), 2, '0')))),
			'snmp'
		FROM generate_series(1, 10000) AS i`, f.OrgA, typeID); err != nil {
		t.Fatalf("seed 10k CIs: %v", err)
	}
	if _, err := f.Admin.Exec(bg, `ANALYZE ci`); err != nil {
		t.Fatal(err)
	}

	capture := &auditSQLCapture{}
	cfg := f.App.Config()
	cfg.ConnConfig.Tracer = capture
	cfg.MinConns = 0
	traced, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer traced.Close()
	repo := discovery.NewPGRepository(traced)

	scanLine := regexp.MustCompile(`(?m)^.*\b(Seq Scan|Index Scan|Index Only Scan|Bitmap Index Scan)\b.*$`)
	identityIndexes := []string{"idx_ci_serial", "uq_ci_org_serial", "idx_ci_primary_mac", "uq_ci_org_primary_mac", "idx_ci_mgmt_ip",
		"idx_ci_mgmt_ip_sys_object", "idx_ci_attributes_path", "idx_ci_hardware_uuid", "uq_ci_org_hardware_uuid", "idx_ci_hostname_lc", "idx_ci_fqdn_lc"}
	explain := func(t *testing.T, q *discovery.IdentityQuery) (plan string, offending []string) {
		t.Helper()
		ids, qerr := repo.IdentityCandidates(f.OrgCtx(f.OrgA), f.OrgA, q)
		if qerr != nil || len(ids) != 1 {
			t.Fatalf("IdentityCandidates = %v, %v; want the one matching CI", ids, qerr)
		}
		sql, args := capture.last(t)
		if werr := database.WithRequestTenant(f.OrgCtx(f.OrgA), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			rows, eerr := tx.Query(ctx, "EXPLAIN (COSTS OFF) "+sql, args...)
			if eerr != nil {
				return eerr
			}
			defer rows.Close()
			var lines []string
			for rows.Next() {
				var line string
				if serr := rows.Scan(&line); serr != nil {
					return serr
				}
				lines = append(lines, line)
			}
			plan = strings.Join(lines, "\n")
			return rows.Err()
		}); werr != nil {
			t.Fatalf("explain: %v", werr)
		}
		for _, line := range scanLine.FindAllString(plan, -1) {
			ok := false
			for _, idx := range identityIndexes {
				if strings.Contains(line, " "+idx+" ") || strings.HasSuffix(line, " "+idx) {
					ok = true
				}
			}
			if !ok {
				offending = append(offending, strings.TrimSpace(line))
			}
		}
		return plan, offending
	}

	// Control: serial number and management IP only.
	if plan, bad := explain(t, &discovery.IdentityQuery{SerialNumber: "AUDSN4242", ManagementIP: "10.201.16.146"}); len(bad) > 0 {
		t.Fatalf("control: the lookup without MACs is not index-based under RLS: %v\n%s", bad, plan)
	}
	plan, bad := explain(t, &discovery.IdentityQuery{SerialNumber: "AUDSN4242", MACs: []string{"02:00:00:00:10:92"}, ManagementIP: "10.201.16.146"})
	if len(bad) > 0 {
		t.Errorf("NFR-01/REC-02 violated: the identity lookup of a record with a MAC scans the organization's inventory under RLS (%v) instead of using only identity indexes:\n%s",
			bad, plan)
	}
}
