package locationnode

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const nodeSelectColumns = `
	id::text, organization_id::text, COALESCE(client_id::text, ''),
	COALESCE(parent_id::text, ''), node_type, name,
	COALESCE(site_id::text, ''), COALESCE(building_id::text, ''),
	COALESCE(room_id::text, ''), COALESCE(rack_id::text, ''),
	COALESCE(barcode, ''), attributes, sort_order, created_at, updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed location node repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// List returns nodes filtered by the given parameters.
func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams) ([]Node, error) {
	var out []Node
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if filter.RootOnly {
			where = append(where, "parent_id IS NULL")
		}
		if filter.ParentID != "" {
			where = append(where, fmt.Sprintf("parent_id = $%d", pos))
			args = append(args, filter.ParentID)
			pos++
		}
		if filter.NodeType != "" {
			where = append(where, fmt.Sprintf("node_type = $%d", pos))
			args = append(args, filter.NodeType)
			pos++
		}
		if filter.Search != "" {
			where = append(where, fmt.Sprintf("name ILIKE $%d", pos))
			args = append(args, "%"+filter.Search+"%")
			pos++
		}
		rows, err := tx.Query(ctx, fmt.Sprintf(
			"SELECT %s FROM location_node WHERE %s ORDER BY sort_order ASC, name ASC",
			nodeSelectColumns, strings.Join(where, " AND ")), args...)
		if err != nil {
			return fmt.Errorf("list location nodes: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			n, err := scanNode(rows)
			if err != nil {
				return err
			}
			out = append(out, *n)
		}
		return rows.Err()
	})
	return out, err
}

// Tree returns the full hierarchy rooted at the root nodes.
func (r *PGRepository) Tree(ctx context.Context, orgID string) ([]Node, error) {
	all, err := r.List(ctx, orgID, FilterParams{})
	if err != nil {
		return nil, err
	}
	return BuildTree(all), nil
}

// GetByID returns one node.
func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Node, error) {
	var out *Node
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		n, err := scanNode(tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT %s FROM location_node WHERE id = $1 AND organization_id = $2",
			nodeSelectColumns), id, orgID))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("get location node: %w", err)
		}
		out = n
		return nil
	})
	return out, err
}

// Create inserts a node.
func (r *PGRepository) Create(ctx context.Context, node *Node) error {
	if !NodeTypes[node.NodeType] {
		return fmt.Errorf("invalid node_type %q", node.NodeType)
	}
	return database.WithRequestTenant(ctx, r.pool, node.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if node.Attributes == nil {
			node.Attributes = map[string]any{}
		}
		return tx.QueryRow(ctx, `
			INSERT INTO location_node (
				organization_id, client_id, parent_id, node_type, name,
				site_id, building_id, room_id, rack_id, barcode, attributes, sort_order
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
			RETURNING id::text, created_at, updated_at`,
			node.OrganizationID, nilIfEmpty(node.ClientID), nilIfEmpty(node.ParentID),
			node.NodeType, node.Name, nilIfEmpty(node.SiteID), nilIfEmpty(node.BuildingID),
			nilIfEmpty(node.RoomID), nilIfEmpty(node.RackID), nilIfEmpty(node.Barcode),
			node.Attributes, node.SortOrder,
		).Scan(&node.ID, &node.CreatedAt, &node.UpdatedAt)
	})
}

// Update modifies a node; reparenting is cycle-guarded.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Node, error) {
	var out *Node
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if req.ParentID != nil && *req.ParentID != "" {
			// Cycle guard: the new parent must not sit below the node.
			var cycle bool
			if err := tx.QueryRow(ctx, `
				WITH RECURSIVE ancestors AS (
					SELECT id, parent_id FROM location_node WHERE id = $1
					UNION ALL
					SELECT n.id, n.parent_id FROM location_node n
					JOIN ancestors a ON n.id = a.parent_id
				) SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $2)`,
				*req.ParentID, id).Scan(&cycle); err != nil {
				return fmt.Errorf("check location cycle: %w", err)
			}
			if cycle {
				return fmt.Errorf("moving a location below its own descendant would create a cycle")
			}
		}
		sets := []string{}
		args := []any{id, orgID}
		pos := 3
		if req.Name != nil {
			sets = append(sets, fmt.Sprintf("name = $%d", pos))
			args = append(args, *req.Name)
			pos++
		}
		if req.ParentID != nil {
			sets = append(sets, fmt.Sprintf("parent_id = $%d", pos))
			args = append(args, nilIfEmpty(*req.ParentID))
			pos++
		}
		if req.Barcode != nil {
			sets = append(sets, fmt.Sprintf("barcode = $%d", pos))
			args = append(args, nilIfEmpty(*req.Barcode))
			pos++
		}
		if req.Attributes != nil {
			sets = append(sets, fmt.Sprintf("attributes = $%d", pos))
			args = append(args, req.Attributes)
			pos++
		}
		if req.SortOrder != nil {
			sets = append(sets, fmt.Sprintf("sort_order = $%d", pos))
			args = append(args, *req.SortOrder)
			pos++
		}
		if len(sets) == 0 {
			n, err := scanNode(tx.QueryRow(ctx, fmt.Sprintf(
				"SELECT %s FROM location_node WHERE id = $1 AND organization_id = $2",
				nodeSelectColumns), id, orgID))
			if err != nil {
				if err == pgx.ErrNoRows {
					return fmt.Errorf("not found")
				}
				return err
			}
			out = n
			return nil
		}
		n, err := scanNode(tx.QueryRow(ctx, fmt.Sprintf(
			"UPDATE location_node SET %s WHERE id = $1 AND organization_id = $2 RETURNING %s",
			strings.Join(sets, ", "), nodeSelectColumns), args...))
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update location node: %w", err)
		}
		out = n
		return nil
	})
	return out, err
}

// Delete removes a node without children.
func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var children bool
		if err := tx.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM location_node WHERE parent_id = $1)", id).Scan(&children); err != nil {
			return fmt.Errorf("check location children: %w", err)
		}
		if children {
			return fmt.Errorf("location has child nodes")
		}
		cmd, err := tx.Exec(ctx,
			"DELETE FROM location_node WHERE id = $1 AND organization_id = $2", id, orgID)
		if err != nil {
			return fmt.Errorf("delete location node: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

type nodeScanner interface {
	Scan(dest ...any) error
}

func scanNode(scanner nodeScanner) (*Node, error) {
	n := &Node{}
	var createdAt, updatedAt time.Time
	if err := scanner.Scan(
		&n.ID, &n.OrganizationID, &n.ClientID, &n.ParentID, &n.NodeType, &n.Name,
		&n.SiteID, &n.BuildingID, &n.RoomID, &n.RackID, &n.Barcode,
		&n.Attributes, &n.SortOrder, &createdAt, &updatedAt,
	); err != nil {
		return nil, err
	}
	if n.Attributes == nil {
		n.Attributes = map[string]any{}
	}
	n.CreatedAt = createdAt.UTC()
	n.UpdatedAt = updatedAt.UTC()
	return n, nil
}

func nilIfEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
