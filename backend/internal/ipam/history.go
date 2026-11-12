package ipam

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
)

// IPAM change history (IPM-08, WP-908, E-213). The table ipam_change
// (migration 000302) is written by statement triggers on subnet,
// ip_address, vlan, vrf and ipam_section in the writing transaction; the
// code only names the source of a transaction's rows
// (app.ipam_change_source: allocation in allocate.go, discovery in
// SyncDiscoveredInterfaces) and reads the history.

// Sources of the history rows (app.ipam_change_source).
const (
	ChangeSourceManual     = "manual"
	ChangeSourceDiscovery  = "discovery"
	ChangeSourceAllocation = "allocation"
)

// Object kinds of the history rows.
const (
	ChangeKindSubnet    = "subnet"
	ChangeKindIPAddress = "ip_address"
)

// Change is a row of the IPAM history: a created or deleted object (the
// object as new_value or old_value) or a changed field (field_name with
// old_value and new_value as JSON). actor_id is the user surrogate
// (Konvention 16), empty for a writer without user (Discovery).
type Change struct {
	ID         string          `json:"id"`
	ObjectKind string          `json:"object_kind"`
	ObjectID   string          `json:"object_id"`
	ActorID    string          `json:"actor_id,omitempty"`
	Source     string          `json:"source"`
	ChangeType string          `json:"change_type"`
	FieldName  string          `json:"field_name,omitempty"`
	OldValue   json.RawMessage `json:"old_value,omitempty"`
	NewValue   json.RawMessage `json:"new_value,omitempty"`
	ChangedAt  time.Time       `json:"changed_at"`
}

// HistoryRepository reads the IPAM history; only the PostgreSQL
// repository has one (the routes answer 501 without it).
type HistoryRepository interface {
	// ListChanges lists the history rows of the object kind/objectID the
	// caller sees, the newest first. ErrNotFound when the caller does not
	// see the object and, for a deleted object, no row of its deletion.
	ListChanges(ctx context.Context, orgID, kind, objectID string, page api.PaginationParams) ([]Change, int, error)
}

// setChangeSource names the source of the IPAM history rows the
// transaction writes (app.ipam_change_source, transaction-local).
func setChangeSource(ctx context.Context, tx pgx.Tx, source string) error {
	if _, err := tx.Exec(ctx, "SELECT set_config('app.ipam_change_source', $1, true)", source); err != nil {
		return fmt.Errorf("set change source: %w", err)
	}
	return nil
}

// historyObjectVisible is the visibility of the object of a history
// request under the caller's row level security, by kind.
var historyObjectVisible = map[string]string{
	ChangeKindSubnet:    "SELECT EXISTS (SELECT 1 FROM subnet WHERE organization_id = $1 AND id = $2)",
	ChangeKindIPAddress: "SELECT EXISTS (SELECT 1 FROM ip_address WHERE organization_id = $1 AND id = $2 AND " + ipVisible + ")",
}

// historyDeletedVisible is the visibility of a deleted object by the row of
// its deletion, by kind. A live address is visible only when its subnet and
// the CI of its interface are visible as well (ipVisible), while the scope
// columns of its rows follow the interface (derive_ip_scope, 000061): an
// address of an org-wide CI in a subnet of another client carries rows
// without client. The deleted address therefore applies ipVisible to the
// object of its deletion row: its subnet is visible, or the deletion row of
// that subnet is; its interface is visible with its CI, or the interface no
// longer exists (network_interface has no client scope of its own; the rows
// then carry the scope the interface gave them).
//
// The rights per section (IPM-10, WP-910, E-214) apply to the deleted
// object as well: ipam_change carries no section policy, so the section the
// deletion row names (old_value section_id; for an address also the one of
// its deleted subnet, which covers rows written before ip_address had a
// section) must not be denied for reading (ipam_section_denied, evaluated
// once per statement).
var historyDeletedVisible = map[string]string{
	ChangeKindSubnet: "SELECT EXISTS (SELECT 1 FROM ipam_change WHERE " + historyWhere + " AND change_type = 'delete'" +
		" AND COALESCE(old_value->>'section_id', '') <> ALL (" + sectionReadDenied + "))",
	ChangeKindIPAddress: `SELECT EXISTS (SELECT 1 FROM ipam_change d
		CROSS JOIN LATERAL (SELECT (d.old_value->>'subnet_id')::uuid AS subnet_id,
		                           (d.old_value->>'interface_id')::uuid AS interface_id) o
		WHERE d.organization_id = $1 AND d.object_kind = $2 AND d.object_id = $3 AND d.change_type = 'delete'
		  AND COALESCE(d.old_value->>'section_id', '') <> ALL (` + sectionReadDenied + `)
		  AND (o.subnet_id IS NULL
		       OR EXISTS (SELECT 1 FROM subnet WHERE subnet.organization_id = $1 AND subnet.id = o.subnet_id)
		       OR EXISTS (SELECT 1 FROM ipam_change s WHERE s.organization_id = $1 AND s.object_kind = 'subnet'
		                  AND s.object_id = o.subnet_id AND s.change_type = 'delete'
		                  AND COALESCE(s.old_value->>'section_id', '') <> ALL (` + sectionReadDenied + `)))
		  AND (o.interface_id IS NULL
		       OR NOT EXISTS (SELECT 1 FROM network_interface WHERE network_interface.organization_id = $1
		                      AND network_interface.id = o.interface_id)
		       OR EXISTS (SELECT 1 FROM network_interface JOIN ci ON ci.id = network_interface.ci_id
		                  WHERE network_interface.organization_id = $1 AND network_interface.id = o.interface_id)))`,
}

// sectionReadDenied is the set of sections the caller may not read, as text
// (ipam_section_denied, migration 000320).
const sectionReadDenied = "(SELECT ipam_section_denied('read'))::text[]"

// historyWhere selects the visible history rows of one object
// (idx_ipam_change_object).
const historyWhere = "organization_id = $1 AND object_kind = $2 AND object_id = $3"

// ListChanges implements HistoryRepository: a visible object lists its
// visible rows; an object the caller does not see lists them only when the
// caller sees the row of its deletion and, for an address, the subnet and
// interface it had (historyDeletedVisible), else ErrNotFound.
func (r *PGRepository) ListChanges(ctx context.Context, orgID, kind, objectID string, page api.PaginationParams) ([]Change, int, error) {
	visibleQuery, ok := historyObjectVisible[kind]
	if !ok || !api.IsUUID(objectID) {
		return nil, 0, ErrNotFound
	}
	var out []Change
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var visible bool
		if err := tx.QueryRow(ctx, visibleQuery, orgID, objectID).Scan(&visible); err != nil {
			return fmt.Errorf("check object: %w", err)
		}
		if !visible {
			if err := tx.QueryRow(ctx, historyDeletedVisible[kind], orgID, kind, objectID).Scan(&visible); err != nil {
				return fmt.Errorf("check deleted object: %w", err)
			}
			if !visible {
				return ErrNotFound
			}
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM ipam_change WHERE "+historyWhere, orgID, kind, objectID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT id::text, object_kind, object_id::text, COALESCE(actor_id::text, ''), source, change_type,
				COALESCE(field_name, ''), old_value, new_value, changed_at
			FROM ipam_change WHERE `+historyWhere+` ORDER BY changed_at DESC, id DESC LIMIT $4 OFFSET $5`,
			orgID, kind, objectID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c Change
			var oldValue, newValue []byte
			if err := rows.Scan(&c.ID, &c.ObjectKind, &c.ObjectID, &c.ActorID, &c.Source, &c.ChangeType, &c.FieldName,
				&oldValue, &newValue, &c.ChangedAt); err != nil {
				return err
			}
			c.OldValue, c.NewValue = jsonValue(oldValue), jsonValue(newValue)
			c.ChangedAt = c.ChangedAt.UTC()
			out = append(out, c)
		}
		return rows.Err()
	})
	if errors.Is(err, ErrNotFound) {
		return nil, 0, ErrNotFound
	}
	return out, total, err
}

// jsonValue is a stored JSON value; SQL NULL stays absent, JSON null is
// kept.
func jsonValue(b []byte) json.RawMessage {
	if b == nil {
		return nil
	}
	return json.RawMessage(b)
}
