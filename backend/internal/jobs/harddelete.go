package jobs

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// HardDeleteCI removes a soft-deleted CI for good (CI-05, JOB-02) in tx:
// first the IP addresses of its interfaces (which would otherwise remain
// without interface), then the references that do not cascade — assets,
// assignments, tickets, stocktake scans and discovery results keep their
// row and lose the link — and finally the CI; its interfaces,
// relationships, contacts, mounts and history go with it.
func HardDeleteCI(ctx context.Context, tx pgx.Tx, orgID, ciID string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM ip_address WHERE organization_id = $1 AND interface_id IN
		(SELECT id FROM network_interface WHERE ci_id = $2)`, orgID, ciID); err != nil {
		return fmt.Errorf("delete IP addresses: %w", err)
	}
	for _, release := range []string{
		`UPDATE asset SET ci_id = NULL WHERE ci_id = $1`,
		`UPDATE assignment SET ci_id = NULL WHERE ci_id = $1`,
		`UPDATE ticket SET related_ci_id = NULL WHERE related_ci_id = $1`,
		`UPDATE stock_scan SET ci_id = NULL WHERE ci_id = $1`,
		`UPDATE discovery_result SET matched_ci_id = NULL WHERE matched_ci_id = $1`,
	} {
		if _, err := tx.Exec(ctx, release, ciID); err != nil {
			return fmt.Errorf("release reference: %w", err)
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM ci WHERE id = $1 AND organization_id = $2 AND deleted_at IS NOT NULL`, ciID, orgID)
	if err != nil {
		return fmt.Errorf("delete ci: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("ci %s is not deleted", ciID)
	}
	return nil
}
