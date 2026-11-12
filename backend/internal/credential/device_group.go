package credential

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// pgMembershipQuery reads the device groups of the organization $1 from
// monitoring_group_member (CRD-04, E-178 (b): the device group is the
// monitoring group). group_rank orders the groups once per organization:
// the higher priority first, on a tie the smaller name (unique per
// organization), so the result does not depend on the read order. The
// query runs in the caller's transaction and scope (RLS).
const pgMembershipQuery = `SELECT m.ci_id, m.group_id,
	(dense_rank() OVER (ORDER BY g.priority DESC, g.name, g.id))::integer AS group_rank
	FROM monitoring_group_member m
	JOIN monitoring_group g ON g.organization_id = m.organization_id AND g.id = m.group_id
	WHERE m.organization_id = $1`

// PGDeviceGroupSource is the DeviceGroupSource of the PostgreSQL store
// (WP-935): the memberships of monitoring_group_member. In the resolution on
// PostgreSQL its query is a CTE of the one resolution statement (no
// statement of its own, WP-891 AK5); Memberships reads the pairs of a set of
// CIs in one statement.
type PGDeviceGroupSource struct {
	pool *pgxpool.Pool
}

// NewPGDeviceGroupSource returns the membership source on pool. A source
// without pool serves MembershipQuery only (the resolution in a caller's
// transaction).
func NewPGDeviceGroupSource(pool *pgxpool.Pool) PGDeviceGroupSource {
	return PGDeviceGroupSource{pool: pool}
}

// MembershipQuery implements DeviceGroupSource.
func (PGDeviceGroupSource) MembershipQuery() string { return pgMembershipQuery }

// errNoPool is returned by Memberships of a source without pool.
var errNoPool = errors.New("device group source without database pool")

// Memberships implements DeviceGroupSource: the groups of ciIDs in rank
// order, read in the caller's scope in one statement.
func (s PGDeviceGroupSource) Memberships(ctx context.Context, orgID string, ciIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	ids := make([]string, 0, len(ciIDs))
	for _, id := range ciIDs {
		if api.IsUUID(id) {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	if s.pool == nil {
		return nil, errNoPool
	}
	err := database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = MembershipsInTx(ctx, tx, orgID, ids)
		return err
	})
	return out, err
}

// MembershipsInTx reads the groups of ciIDs in rank order in tx.
func MembershipsInTx(ctx context.Context, tx pgx.Tx, orgID string, ciIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	rows, err := tx.Query(ctx, `SELECT ci_id::text, group_id::text FROM (`+pgMembershipQuery+`) m
		WHERE m.ci_id = ANY ($2::uuid[]) ORDER BY m.ci_id, m.group_rank`, orgID, ciIDs)
	if err != nil {
		return nil, fmt.Errorf("read device group memberships: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ci, group string
		if err := rows.Scan(&ci, &group); err != nil {
			return nil, fmt.Errorf("scan device group membership: %w", err)
		}
		out[ci] = append(out[ci], group)
	}
	return out, rows.Err()
}

// deviceGroupsOf is the membership source of a store: monitoring_group_member
// for PostgreSQL (WP-935), none for the memory store.
func deviceGroupsOf(repo Repository) DeviceGroupSource {
	if pg, ok := repo.(*PGRepository); ok {
		return NewPGDeviceGroupSource(pg.pool)
	}
	return NoDeviceGroups{}
}

var _ DeviceGroupSource = PGDeviceGroupSource{}
