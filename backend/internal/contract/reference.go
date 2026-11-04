package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
)

// contractTarget is a contract as the target of a reference value: its client
// ("" for an org-wide contract) and its number, the display name.
type contractTarget struct {
	clientID string
	number   string
}

// referenceSource reads the contracts among ids that the caller sees, in
// one statement; the keys of the result are lower case.
type referenceSource interface {
	contractReferences(ctx context.Context, orgID string, ids []string) (map[string]contractTarget, error)
}

// ReferenceResolver is the existence check of the reference target
// contract (MET-12, WP-737) for the registry of WP-254
// (fieldmeta.ReferenceLookup): a reference to a contract holds when the
// contract exists in the organization and is visible in the caller's scope
// (row level security of contract under database.WithRequestTenant); a
// deleted contract no longer exists. The client of the contract lets the
// CI service check the compatibility with the referencing object
// (org-wide contract or the same client). It also names the contracts
// (display_name = number) for include=references.
type ReferenceResolver struct {
	source referenceSource
}

// The resolver serves the registry interface of WP-254.
var _ fieldmeta.ReferenceLookup = (*ReferenceResolver)(nil)

// NewReferenceResolver returns the resolver on repo; nil when repo cannot
// resolve references.
func NewReferenceResolver(repo ContractRepository) *ReferenceResolver {
	source, ok := repo.(referenceSource)
	if !ok {
		return nil
	}
	return &ReferenceResolver{source: source}
}

// errOtherTarget answers a target other than contract.
func errOtherTarget(target string) error {
	return fmt.Errorf("reference target %s is not resolved by contracts", target)
}

// LookupReferences implements fieldmeta.ReferenceLookup: the visible
// contracts among ids with their client_id ("" for an org-wide contract).
func (r *ReferenceResolver) LookupReferences(ctx context.Context, orgID, target string, ids []string) (map[string]string, error) {
	if target != fieldmeta.TargetContract {
		return nil, errOtherTarget(target)
	}
	refs, err := r.source.contractReferences(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(refs))
	for id, ref := range refs {
		out[id] = ref.clientID
	}
	return out, nil
}

// ReferenceDisplayNames names the visible contracts among ids by their
// number (ci.TargetDisplayNames).
func (r *ReferenceResolver) ReferenceDisplayNames(ctx context.Context, orgID, target string, ids []string) (map[string]string, error) {
	if target != fieldmeta.TargetContract {
		return nil, errOtherTarget(target)
	}
	refs, err := r.source.contractReferences(ctx, orgID, ids)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(refs))
	for id, ref := range refs {
		out[id] = ref.number
	}
	return out, nil
}

// uuidIDs keeps the ids in UUID form, lower case; any other id names no
// contract and never reaches SQL.
func uuidIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if api.IsUUID(id) {
			out = append(out, strings.ToLower(id))
		}
	}
	return out
}

// contractReferences implements referenceSource with one statement under
// the request's tenant scope.
func (r *PGContracts) contractReferences(ctx context.Context, orgID string, ids []string) (map[string]contractTarget, error) {
	found := map[string]contractTarget{}
	ids = uuidIDs(ids)
	if len(ids) == 0 {
		return found, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, `SELECT id::text, COALESCE(client_id::text, ''), number FROM contract
			WHERE organization_id = $1 AND id = ANY($2::uuid[])`, orgID, ids)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var ref contractTarget
			if serr := rows.Scan(&id, &ref.clientID, &ref.number); serr != nil {
				return serr
			}
			ref.clientID = strings.ToLower(ref.clientID)
			found[strings.ToLower(id)] = ref
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("resolve contract references: %w", err)
	}
	return found, nil
}

// contractReferences implements referenceSource in memory (no client
// scope).
func (m *MemoryContracts) contractReferences(ctx context.Context, orgID string, ids []string) (map[string]contractTarget, error) {
	found := map[string]contractTarget{}
	for _, id := range uuidIDs(ids) {
		c, err := m.GetContract(ctx, orgID, id)
		if errors.Is(err, ErrContractNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		found[id] = contractTarget{clientID: strings.ToLower(deref(c.ClientID)), number: c.Number}
	}
	return found, nil
}
