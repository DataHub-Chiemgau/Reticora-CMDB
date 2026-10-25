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

// recordPlacement applies a mount, move or unmount to the location of the
// CI and records it (RCK-03, DB-05), in the transaction of the change:
//   - the location becomes the rack (mount, move) or the rack's room
//     (unmount); for a CI linked to an asset the asset's location is set
//     and mirrored to its CIs, otherwise the CI's;
//   - ci_change gets a movement row and, when the location changed, a
//     location_change row; the audit gets one entry.
func recordPlacement(ctx context.Context, tx pgx.Tx, orgID, ciID, action string, before, after *RackMount) error {
	var location string
	if after != nil {
		location = after.RackID
	} else if err := tx.QueryRow(ctx, `SELECT room_id::text FROM rack WHERE organization_id = $1 AND id = $2`,
		orgID, before.RackID).Scan(&location); err != nil {
		return fmt.Errorf("room of rack %s: %w", before.RackID, err)
	}

	var assetID, oldLocation string
	var version int64
	var foreignClient bool
	// A shared rack of another client (colocation) cannot become the
	// location of the CI (ci_location_client): the placement is kept, the
	// location stays and the audit names the reason.
	if err := tx.QueryRow(ctx, `SELECT COALESCE(c.asset_id::text, ''), COALESCE(c.location_id::text, ''), c.version,
			COALESCE(c.client_id <> l.client_id, false)
		FROM ci c LEFT JOIN location l ON l.id = $3
		WHERE c.organization_id = $1 AND c.id = $2`, orgID, ciID, location).Scan(&assetID, &oldLocation, &version, &foreignClient); err != nil {
		return fmt.Errorf("placed ci %s: %w", ciID, err)
	}
	if foreignClient {
		location = oldLocation
	}
	if oldLocation != location {
		var err error
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
	}

	userID := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	change := func(changeType, field string, oldValue, newValue any) error {
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
			orgID, ciID, userID, changeType, field, string(oldJSON), string(newJSON), version)
		if err != nil {
			return fmt.Errorf("record ci change: %w", err)
		}
		return nil
	}
	if err := change("movement", "rack_mount", placementOf(before), placementOf(after)); err != nil {
		return err
	}
	if oldLocation != location {
		if err := change("location_change", "location_id", nilIfBlank(oldLocation), location); err != nil {
			return err
		}
	}

	actorType := "user"
	if userID == "" {
		actorType = "system"
	}
	changes := map[string]any{"before": placementOf(before), "after": placementOf(after),
		"location_id": map[string]any{"from": nilIfBlank(oldLocation), "to": location}}
	if assetID != "" {
		changes["asset_id"] = assetID
	}
	if foreignClient {
		changes["location_unchanged"] = "the rack belongs to another client"
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorID: userID,
		ActorType: actorType, Action: action, ResourceType: "ci", ResourceID: ciID, Changes: changes}); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

func nilIfBlank(v string) any {
	if v == "" {
		return nil
	}
	return v
}
