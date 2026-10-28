package rack

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// placementOf is a rack placement as ci_change and the audit record it.
func placementOf(m *RackMount) map[string]any {
	if m == nil {
		return nil
	}
	return map[string]any{"rack_id": m.RackID, "position_u": m.PositionU, "height_u": m.HeightU, "face": m.Face}
}

// placedCI is a CI whose location a placement sets: the placed CI or another
// CI of its asset, with its location and version before the placement.
type placedCI struct {
	id, oldLocation string
	version         int64
}

// recordPlacement applies a mount, move or unmount to the location of the
// CI and records it (RCK-03, DB-05), in the transaction of the change:
//   - the location becomes the rack (mount, move) or the rack's room
//     (unmount); for a CI linked to an asset the asset's location is set
//     and mirrored to every CI of the asset (trg_asset_propagate_location),
//     otherwise the CI's;
//   - every CI whose location changed, the placed CI and the other CIs of
//     its asset, gets ci.version + 1, a location_change row in ci_change
//     with that version and an audit entry (F-240, F-241);
//   - the placed CI gets a movement row and its audit entry in any case.
func recordPlacement(ctx context.Context, tx pgx.Tx, orgID, ciID, action string, before, after *RackMount) error {
	var location string
	if after != nil {
		location = after.RackID
	} else if err := tx.QueryRow(ctx, `SELECT room_id::text FROM rack WHERE organization_id = $1 AND id = $2`,
		orgID, before.RackID).Scan(&location); err != nil {
		return fmt.Errorf("room of rack %s: %w", before.RackID, err)
	}

	assetID, cis, foreignClient, err := placedCIs(ctx, tx, orgID, ciID, location)
	if err != nil {
		return err
	}
	// A shared rack of another client (colocation) cannot become the
	// location of a CI (ci_location_client): if the rack is foreign to the
	// placed CI or to another CI of its asset, the placement is kept, the
	// location stays and the audit names the reason.
	if foreignClient {
		location = cis[0].oldLocation
	}
	// raised holds the new version of every CI whose location changed.
	raised := map[string]int64{}
	if cis[0].oldLocation != location {
		if assetID != "" {
			_, err = tx.Exec(ctx, `UPDATE asset SET location_id = $3, updated_at = now()
				WHERE organization_id = $1 AND id = $2`, orgID, assetID, location)
		} else {
			_, err = tx.Exec(ctx, `UPDATE ci SET location_id = $3, updated_at = now()
				WHERE organization_id = $1 AND id = $2`, orgID, ciID, location)
		}
		if err != nil {
			return fmt.Errorf("set location from rack placement: %w", err)
		}
		if raised, err = raiseMovedVersions(ctx, tx, orgID, cis); err != nil {
			return err
		}
	}

	userID := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	change := func(id string, version int64, changeType, field string, oldValue, newValue any) error {
		oldJSON, err := json.Marshal(oldValue)
		if err != nil {
			return err
		}
		newJSON, err := json.Marshal(newValue)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO ci_change (organization_id, ci_id, actor_id, change_type, field_name,
			old_value, new_value, ci_version) VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $5, $6::jsonb, $7::jsonb, $8)`,
			orgID, id, userID, changeType, field, string(oldJSON), string(newJSON), version)
		if err != nil {
			return fmt.Errorf("record ci change: %w", err)
		}
		return nil
	}
	actorType := "user"
	if userID == "" {
		actorType = "system"
	}
	record := func(id, action string, changes map[string]any) error {
		if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorID: userID,
			ActorType: actorType, Action: action, ResourceType: "ci", ResourceID: id, Changes: changes}); err != nil {
			return fmt.Errorf("record audit: %w", err)
		}
		return nil
	}

	for i, c := range cis {
		version, moved := raised[c.id]
		if !moved {
			version = c.version
		}
		if i == 0 {
			if err := change(c.id, version, "movement", "rack_mount", placementOf(before), placementOf(after)); err != nil {
				return err
			}
		}
		if moved {
			if err := change(c.id, version, "location_change", "location_id", nilIfBlank(c.oldLocation), location); err != nil {
				return err
			}
		}
		to := c.oldLocation
		if moved {
			to = location
		}
		locationChange := map[string]any{"from": nilIfBlank(c.oldLocation), "to": nilIfBlank(to)}
		switch {
		case i == 0:
			changes := map[string]any{"before": placementOf(before), "after": placementOf(after), "location_id": locationChange}
			if assetID != "" {
				changes["asset_id"] = assetID
			}
			if foreignClient {
				changes["location_unchanged"] = "the rack belongs to another client"
			}
			if err := record(c.id, action, changes); err != nil {
				return err
			}
		case moved:
			// Another CI of the asset: its location followed the placement.
			if err := record(c.id, "ci.location_changed", map[string]any{"location_id": locationChange,
				"asset_id": assetID, "placed_ci_id": ciID, "cause": action}); err != nil {
				return err
			}
		}
	}
	return nil
}

// placedCIs reads the asset of the placed CI and the CIs the placement sets
// the location of, the placed CI first and then the other CIs of its asset
// visible in the transaction; foreignClient tells whether location belongs
// to another client than one of them.
func placedCIs(ctx context.Context, tx pgx.Tx, orgID, ciID, location string) (
	assetID string, cis []placedCI, foreignClient bool, err error) {
	if err = tx.QueryRow(ctx, `SELECT COALESCE(asset_id::text, '') FROM ci WHERE organization_id = $1 AND id = $2`,
		orgID, ciID).Scan(&assetID); err != nil {
		return "", nil, false, fmt.Errorf("placed ci %s: %w", ciID, err)
	}
	rows, err := tx.Query(ctx, `SELECT c.id::text, COALESCE(c.location_id::text, ''), c.version,
			COALESCE(c.client_id <> l.client_id, false)
		FROM ci c LEFT JOIN location l ON l.id = $4
		WHERE c.organization_id = $1 AND (c.id = $2 OR c.asset_id = NULLIF($3, '')::uuid)
		ORDER BY c.id <> $2, c.id`, orgID, ciID, assetID, location)
	if err != nil {
		return "", nil, false, fmt.Errorf("cis of the placement: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c placedCI
		var foreign bool
		if err := rows.Scan(&c.id, &c.oldLocation, &c.version, &foreign); err != nil {
			return "", nil, false, fmt.Errorf("cis of the placement: %w", err)
		}
		cis = append(cis, c)
		foreignClient = foreignClient || foreign
	}
	if err := rows.Err(); err != nil {
		return "", nil, false, fmt.Errorf("cis of the placement: %w", err)
	}
	if len(cis) == 0 || cis[0].id != ciID {
		return "", nil, false, fmt.Errorf("placed ci %s: %w", ciID, pgx.ErrNoRows)
	}
	return assetID, cis, foreignClient, nil
}

// raiseMovedVersions raises ci.version by 1 for every CI of cis whose
// location differs from its location before the placement and returns the
// new versions by CI.
func raiseMovedVersions(ctx context.Context, tx pgx.Tx, orgID string, cis []placedCI) (map[string]int64, error) {
	ids, olds := make([]string, len(cis)), make([]string, len(cis))
	for i, c := range cis {
		ids[i], olds[i] = c.id, c.oldLocation
	}
	rows, err := tx.Query(ctx, `UPDATE ci c SET version = c.version + 1, updated_at = now()
		FROM unnest($2::uuid[], $3::text[]) AS b(id, old_location)
		WHERE c.organization_id = $1 AND c.id = b.id
		  AND c.location_id IS DISTINCT FROM NULLIF(b.old_location, '')::uuid
		RETURNING c.id::text, c.version`, orgID, ids, olds)
	if err != nil {
		return nil, fmt.Errorf("raise ci version: %w", err)
	}
	defer rows.Close()
	raised := map[string]int64{}
	for rows.Next() {
		var id string
		var version int64
		if err := rows.Scan(&id, &version); err != nil {
			return nil, fmt.Errorf("raise ci version: %w", err)
		}
		raised[id] = version
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("raise ci version: %w", err)
	}
	return raised, nil
}

func nilIfBlank(v string) any {
	if v == "" {
		return nil
	}
	return v
}
