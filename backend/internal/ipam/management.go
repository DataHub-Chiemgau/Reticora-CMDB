package ipam

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// Management IP and IPAM (IPM-13, WP-913, E-215 option 1): ci.management_ip
// and the address of the CI with role management are kept consistent in
// the writing transaction of the CI. The CI repository calls
// SyncManagementAddressTx after it wrote the row, only when management_ip
// or its VRF changed (router.go wires it; without it nothing happens).

// MgmtBackfillJobKind is the job that brings the existing CIs into the
// state of SyncManagementAddressTx (migration 000323 queues it once per
// organization).
const MgmtBackfillJobKind = "ipam.mgmt_backfill"

// mgmtBackfillBatch is the number of CIs of one transaction of the backfill.
const mgmtBackfillBatch = 500

// ManagementSync implements the synchronisation on the PostgreSQL
// repository; reviews receives the duplicate_ip items of the check after
// the commit (nil: no check).
type ManagementSync struct {
	repo    *PGRepository
	reviews *reviews.Service
}

// NewManagementSync builds the synchronisation.
func NewManagementSync(repo *PGRepository, svc *reviews.Service) *ManagementSync {
	return &ManagementSync{repo: repo, reviews: svc}
}

// WithReviews attaches the review queue of the duplicate check (the one of
// discovery, router.go).
func (m *ManagementSync) WithReviews(svc *reviews.Service) *ManagementSync {
	m.reviews = svc
	return m
}

// managementHost is the host of a management IP ("" when there is none or
// it does not parse).
func managementHost(ip string) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	if a, err := netip.ParseAddr(ip); err == nil {
		return a.Unmap().String()
	}
	if p, err := netip.ParsePrefix(ip); err == nil {
		return p.Addr().Unmap().String()
	}
	return ""
}

// SyncManagementAddressTx brings the addresses of CI ciID in line with its
// management IP newIP in VRF vrfID (empty or unknown: the default VRF), in
// tx after the CI row is written (AK1, AK2): SyncManagementAddressesTx for
// one CI. oldIP is the former management IP; the former management address
// is found by its role, so it serves the log only.
func (m *ManagementSync) SyncManagementAddressTx(ctx context.Context, tx pgx.Tx, orgID, ciID, vrfID, oldIP, newIP string, discovered bool) error {
	slog.DebugContext(ctx, "management address", "event", "ipam.management_sync", "organization_id", orgID,
		"ci_id", ciID, "from", oldIP != "")
	return m.SyncManagementAddressesTx(ctx, tx, orgID, []string{ciID}, []string{vrfID}, []string{newIP}, discovered)
}

// SyncManagementAddressesTx brings the addresses of the CIs ciIDs in line
// with their management IPs ips in the VRFs vrfIDs (same index; empty or
// unknown: the default VRF), set-based in tx after the CI rows are written
// (AK1, AK2): the statements are the same for one CI as for a batch
// (CreateMany, the backfill). discovered marks the ingest: a new address is
// of type discovered and the history names the source discovery, else
// static and manual.
//
//   - An address of the CI in the VRF (at an interface of its own or bound
//     with ci_id) gets role management; an unbound address without
//     interface is bound to the CI; otherwise a row without interface is
//     created with ci_id, role management and its longest prefix
//     (ipam_longest_prefix_subnet). Every other address of the CI with role
//     management becomes primary. An empty IP only takes the role away.
//   - The VRF locks of the tree (subnet_tree_lock, WP-901) are the only
//     locks taken after the identity locks of the caller (E-56), all target
//     VRFs in one statement in the order of their ids, so two transactions
//     with CIs in several VRFs cannot wait for each other.
//   - Another CI holding the address without interface keeps its row and
//     no second row is created; after the commit the duplicate check opens
//     or updates duplicate_ip with both CIs (AK4). The writer gets no error:
//     a refusal of the address write (row level security, a section
//     without write right, a unique index) rolls back only the address
//     write (savepoint).
//
// The source of the IPAM history stays set for the rest of the transaction
// (app.ipam_change_source): the CI write paths write no other IPAM rows
// after it but those of the same source.
func (m *ManagementSync) SyncManagementAddressesTx(ctx context.Context, tx pgx.Tx, orgID string, ciIDs, vrfIDs, ips []string, discovered bool) error {
	source, ipType := ChangeSourceManual, IPTypeStatic
	if discovered {
		source, ipType = ChangeSourceDiscovery, IPTypeDiscovered
	}
	var cleared, setCIs, setVRFs, setHosts []string
	seen := make(map[string]bool, len(ciIDs))
	for i, id := range ciIDs {
		if seen[id] || i >= len(vrfIDs) || i >= len(ips) {
			continue
		}
		seen[id] = true
		if host := managementHost(ips[i]); host != "" {
			setCIs, setVRFs, setHosts = append(setCIs, id), append(setVRFs, vrfIDs[i]), append(setHosts, host)
		} else {
			cleared = append(cleared, id)
		}
	}
	if len(cleared) > 0 {
		// The role goes, the addresses stay.
		_, err := tx.Exec(ctx, `UPDATE ip_address SET role = 'primary'
			WHERE organization_id = $1 AND ci_id = ANY($2::uuid[]) AND role = 'management'
			  AND (SELECT set_config('app.ipam_change_source', $3, true)) IS NOT NULL`, orgID, cleared, source)
		if err != nil {
			return fmt.Errorf("clear management address: %w", err)
		}
	}
	if len(setCIs) == 0 {
		return nil
	}
	// The target VRFs, locked in the order of their ids, and the source.
	rows, err := tx.Query(ctx, `WITH r AS MATERIALIZED (
			SELECT i.ci, i.n, COALESCE((SELECT v.id FROM vrf v WHERE v.organization_id = $1
				AND v.id = (CASE WHEN i.vrf ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$' THEN i.vrf::uuid END)),
				default_vrf_of($1)) AS vrf
			FROM unnest($2::text[], $3::text[]) WITH ORDINALITY AS i(ci, vrf, n)
		), l AS MATERIALIZED (
			SELECT count(*) AS n FROM (SELECT vrf FROM r GROUP BY vrf ORDER BY vrf::text) s
			CROSS JOIN LATERAL subnet_tree_lock($1, s.vrf) k
		)
		SELECT r.vrf::text FROM r, l WHERE set_config('app.ipam_change_source', $4, true) IS NOT NULL ORDER BY r.n`,
		orgID, setCIs, setVRFs, source)
	if err != nil {
		return fmt.Errorf("lock management vrfs: %w", err)
	}
	resolved := make([]string, 0, len(setCIs))
	for rows.Next() {
		var v string
		if scanErr := rows.Scan(&v); scanErr != nil {
			rows.Close()
			return fmt.Errorf("lock management vrfs: %w", scanErr)
		}
		resolved = append(resolved, v)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("lock management vrfs: %w", err)
	}
	if len(resolved) != len(setCIs) {
		return fmt.Errorf("lock management vrfs: %d of %d resolved", len(resolved), len(setCIs))
	}
	type outcome struct {
		ci, vrf, host                  string
		claimed, inserted, heldByOther bool
	}
	var out []outcome
	err = pgx.BeginFunc(ctx, tx, func(sp pgx.Tx) error {
		res, qerr := sp.Query(ctx, `WITH k AS (
				SELECT * FROM unnest($2::uuid[], $3::uuid[], $4::text[]) AS k(ci, vrf, host)
			), target AS (
				SELECT DISTINCT ON (k.ci) k.ci, a.id FROM k
				  JOIN ip_address a ON a.organization_id = $1 AND a.vrf_id = k.vrf AND a.address_host = k.host
				   AND (a.ci_id = k.ci OR (a.ci_id IS NULL AND a.interface_id IS NULL))
				 ORDER BY k.ci, a.ci_id IS NULL, a.interface_id IS NULL, a.id
			), claimed AS (
				UPDATE ip_address a SET role = 'management', ci_id = t.ci FROM target t
				 WHERE a.organization_id = $1 AND a.id = t.id AND (a.role <> 'management' OR a.ci_id IS DISTINCT FROM t.ci)
				RETURNING a.id
			), inserted AS (
				INSERT INTO ip_address (organization_id, ci_id, role, address, vrf_id, type, subnet_id)
				SELECT $1, k.ci, 'management', k.host::inet, k.vrf, $5, ipam_longest_prefix_subnet($1, k.vrf, k.host::inet)
				  FROM k WHERE NOT EXISTS (SELECT 1 FROM target t WHERE t.ci = k.ci)
				ON CONFLICT (organization_id, vrf_id, address) WHERE interface_id IS NULL DO NOTHING
				RETURNING ci_id
			), demoted AS (
				UPDATE ip_address a SET role = 'primary' FROM k
				 WHERE a.organization_id = $1 AND a.ci_id = k.ci AND a.role = 'management'
				   AND NOT EXISTS (SELECT 1 FROM target t WHERE t.id = a.id)
				RETURNING a.id
			)
			SELECT k.ci::text, k.vrf::text, k.host,
			       EXISTS (SELECT 1 FROM target t WHERE t.ci = k.ci),
			       EXISTS (SELECT 1 FROM inserted i WHERE i.ci_id = k.ci),
			       EXISTS (SELECT 1 FROM ip_address a WHERE a.organization_id = $1 AND a.vrf_id = k.vrf AND a.address_host = k.host
			                  AND a.interface_id IS NULL AND a.ci_id IS DISTINCT FROM k.ci)
			  FROM k
			 WHERE (SELECT count(*) FROM claimed) + (SELECT count(*) FROM demoted) >= 0`,
			orgID, setCIs, resolved, setHosts, ipType)
		if qerr != nil {
			return qerr
		}
		defer res.Close()
		for res.Next() {
			var o outcome
			if scanErr := res.Scan(&o.ci, &o.vrf, &o.host, &o.claimed, &o.inserted, &o.heldByOther); scanErr != nil {
				return scanErr
			}
			out = append(out, o)
		}
		return res.Err()
	})
	if err != nil {
		if !refusedAddressWrite(err) {
			return fmt.Errorf("sync management address: %w", err)
		}
		// Only the address write rolls back (savepoint); the CIs are written.
		slog.WarnContext(ctx, "management address not synchronised", "event", "ipam.management_sync",
			"organization_id", orgID, "cis", len(setCIs), "error", err)
		out = out[:0]
		for i := range setCIs {
			out = append(out, outcome{ci: setCIs[i], vrf: resolved[i], host: setHosts[i]})
		}
	}
	// The check reads the committed rows: it runs after the commit of tx.
	var keys []DuplicateKey
	var claims []DuplicateClaim
	for _, o := range out {
		key := DuplicateKey{VRFID: o.vrf, Host: o.host}
		if o.heldByOther && !o.claimed && !o.inserted {
			claims = append(claims, DuplicateClaim{DuplicateKey: key, CIID: o.ci})
		} else {
			keys = append(keys, key)
		}
	}
	m.checkAfterCommit(database.ContextWithTx(ctx, tx), orgID, keys, claims)
	return nil
}

// refusedAddressWrite reports an error the address write may end with
// without failing the CI write: a policy (42501), a unique index (23505)
// or a check (23514).
func refusedAddressWrite(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case "42501", "23505", "23514":
		return true
	}
	return false
}

// checkAfterCommit runs the duplicate check of the management addresses
// after the commit of the transaction of ctx (WP-300): keys are the
// addresses of the CIs, claims the management IPs of CIs without a row of
// their own because another CI holds the address without interface (the
// check names such a CI as a holder through its management IP).
func (m *ManagementSync) checkAfterCommit(ctx context.Context, orgID string, keys []DuplicateKey, claims []DuplicateClaim) {
	if m.reviews == nil || (len(keys) == 0 && len(claims) == 0) {
		return
	}
	database.AfterCommit(ctx, func(ctx context.Context) {
		sources := make([]DuplicateSource, 0, 1+len(claims))
		if len(keys) > 0 {
			sources = append(sources, DuplicateSource{Keys: keys})
		}
		for i := range claims {
			sources = append(sources, DuplicateSource{Claim: &claims[i]})
		}
		for _, src := range sources {
			if err := CheckDuplicates(ctx, m.repo, m.reviews, orgID, src); err != nil {
				slog.WarnContext(ctx, "duplicate address check failed", "event", "ipam.duplicate_ip", "organization_id", orgID,
					"error", err)
			}
		}
	})
}

// RunBackfillJob runs the job ipam.mgmt_backfill (AK6): every CI of the
// organization with a management IP whose address in its VRF does not have
// role management yet is synchronised as by a manual write, in batches of
// mgmtBackfillBatch CIs, one transaction per batch. The job is idempotent:
// a CI already in line is not read again, so a run after an abort goes on
// where the previous one stopped and a second run writes nothing.
func (m *ManagementSync) RunBackfillJob(ctx context.Context, job *jobs.Job) (any, error) {
	scope := database.OrgWideScope(job.OrganizationID, "")
	ctx = database.ContextWithTenantScope(database.WithoutTx(ctx), &scope)
	synced, err := m.Backfill(ctx, job.OrganizationID, mgmtBackfillBatch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"synchronised": synced}, nil
}

// Backfill synchronises the CIs of orgID that are not in line, batch CIs
// per transaction, and returns their number.
func (m *ManagementSync) Backfill(ctx context.Context, orgID string, batch int) (int, error) {
	total, after := 0, ""
	for {
		// A cancel stops between two batches; the batches written stay.
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, last, err := m.backfillBatch(ctx, orgID, after, batch)
		total += n
		if err != nil || last == "" {
			return total, err
		}
		after = last
	}
}

// backfillBatch synchronises up to batch CIs after the id after and
// returns their number and the last id ("" when none was left).
func (m *ManagementSync) backfillBatch(ctx context.Context, orgID, after string, batch int) (n int, last string, err error) {
	type pending struct{ id, ip, vrf string }
	err = database.WithRequestTenant(ctx, m.repo.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// In line is a CI whose address in its VRF has role management,
		// and one whose address another CI holds without interface (AK4:
		// no row of its own) once a duplicate_ip item of the address names
		// it, whatever its status (the check after the first run's commit).
		rows, qerr := tx.Query(ctx, `SELECT c.id::text, host(c.management_ip), COALESCE(c.attributes -> 'raw_data' ->> 'vrf_id', '')
			FROM ci c
			CROSS JOIN LATERAL (SELECT COALESCE((SELECT v.id FROM vrf v WHERE v.organization_id = c.organization_id
			        AND v.id::text = c.attributes -> 'raw_data' ->> 'vrf_id'), default_vrf_of(c.organization_id)) AS id) t
			WHERE c.organization_id = $1 AND c.deleted_at IS NULL AND c.management_ip IS NOT NULL AND c.id::text > $2
			  AND NOT EXISTS (SELECT 1 FROM ip_address a
			       WHERE a.organization_id = c.organization_id AND a.vrf_id = t.id AND a.address_host = host(c.management_ip)
			         AND ((a.ci_id = c.id AND a.role = 'management')
			              OR (a.interface_id IS NULL AND a.ci_id IS NOT NULL AND a.ci_id <> c.id
			                  AND EXISTS (SELECT 1 FROM review_item r
			                       WHERE r.organization_id = c.organization_id AND r.kind = 'duplicate_ip'
			                         AND r.object_key = 'ip:' || t.id::text || '/' || host(c.management_ip)
			                         AND c.id = ANY (r.candidate_ci_ids)))))
			ORDER BY c.id::text
			LIMIT $3`, orgID, after, batch)
		if qerr != nil {
			return fmt.Errorf("read management IPs: %w", qerr)
		}
		var todo []pending
		for rows.Next() {
			var p pending
			if scanErr := rows.Scan(&p.id, &p.ip, &p.vrf); scanErr != nil {
				rows.Close()
				return scanErr
			}
			todo = append(todo, p)
		}
		rows.Close()
		if rows.Err() != nil {
			return rows.Err()
		}
		if len(todo) == 0 {
			return nil
		}
		ciIDs, vrfIDs, ips := make([]string, len(todo)), make([]string, len(todo)), make([]string, len(todo))
		for i, p := range todo {
			ciIDs[i], vrfIDs[i], ips[i] = p.id, p.vrf, p.ip
		}
		if syncErr := m.SyncManagementAddressesTx(ctx, tx, orgID, ciIDs, vrfIDs, ips, false); syncErr != nil {
			return syncErr
		}
		n, last = len(todo), todo[len(todo)-1].id
		return nil
	})
	if err != nil {
		return 0, "", err
	}
	return n, last, nil
}
