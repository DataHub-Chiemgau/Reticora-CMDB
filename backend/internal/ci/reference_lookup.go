package ci

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// referenceQueries select, per reference target, the visible ids among $2
// that count as present with their client_id (empty for an org-wide row):
// a CI that is neither deleted nor merged, an active user, a team, a
// location of any kind (CH28, LOC-10), an asset, a document (policy
// document_tenant_isolation: unlinked or with a visible link). Row level
// security of the caller's scope applies; organization_id is named as
// well.
var referenceQueries = map[string]string{
	fieldmeta.TargetCI: `SELECT id::text, COALESCE(client_id::text, '') FROM ci
		WHERE organization_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL AND merged_into_id IS NULL`,
	fieldmeta.TargetUser: `SELECT id::text, '' FROM app_user
		WHERE organization_id = $1 AND id = ANY($2::uuid[]) AND is_active`,
	fieldmeta.TargetTeam: `SELECT id::text, '' FROM team
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	fieldmeta.TargetLocation: `SELECT id::text, client_id::text FROM location
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	fieldmeta.TargetAsset: `SELECT id::text, COALESCE(client_id::text, '') FROM asset
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	fieldmeta.TargetDocument: `SELECT id::text, '' FROM document
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
}

// PGReferenceLookup is the existence check of reference values in
// PostgreSQL (fieldmeta.ReferenceLookup, MET-12): one statement per target
// and write, in the request's tenant scope.
type PGReferenceLookup struct {
	pool *pgxpool.Pool
}

// NewPGReferenceLookup creates the lookup on pool.
func NewPGReferenceLookup(pool *pgxpool.Pool) *PGReferenceLookup {
	return &PGReferenceLookup{pool: pool}
}

// LookupReferences implements fieldmeta.ReferenceLookup; the ids of the
// result are lower case.
func (l *PGReferenceLookup) LookupReferences(ctx context.Context, orgID, target string, ids []string) (map[string]string, error) {
	query, ok := referenceQueries[target]
	if !ok {
		return nil, fmt.Errorf("reference target %s has no lookup", target)
	}
	found := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return found, nil
	}
	err := database.WithRequestTenant(ctx, l.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, query, orgID, ids)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var id, client string
			if serr := rows.Scan(&id, &client); serr != nil {
				return serr
			}
			found[strings.ToLower(id)] = strings.ToLower(client)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}
