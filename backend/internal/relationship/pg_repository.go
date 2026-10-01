package relationship

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const relationshipSelectColumns = `
	id::text,
	organization_id::text,
	source_ci_id::text,
	target_ci_id::text,
	rel_type,
	attributes,
	source,
	confidence,
	first_seen_at,
	last_seen_at,
	verification_state,
	COALESCE(source_system, ''),
	COALESCE(notes, ''),
	created_at,
	updated_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a new PostgreSQL-backed relationship repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

// visibleEndpoints restricts relationship rows to edges whose source and
// target CI are both visible under the transaction's tenant scope: the ci
// policy filters the subqueries by client scope, while ci_relationship itself
// carries no client column yet (WP-025). A client-scoped principal therefore
// neither sees nor changes edges that touch another client's CI.
const visibleEndpoints = `EXISTS (SELECT 1 FROM ci WHERE ci.id = ci_relationship.source_ci_id)
	AND EXISTS (SELECT 1 FROM ci WHERE ci.id = ci_relationship.target_ci_id)`

func (r *PGRepository) List(ctx context.Context, orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error) {
	items := make([]Relationship, 0)
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		whereParts := []string{"organization_id = $1", visibleEndpoints}
		args := []any{orgID}
		argPos := 2
		if ciID != "" {
			whereParts = append(whereParts, fmt.Sprintf("(source_ci_id = $%d OR target_ci_id = $%d)", argPos, argPos))
			args = append(args, ciID)
			argPos++
		}

		whereClause := strings.Join(whereParts, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ci_relationship WHERE "+whereClause, args...).Scan(&total); err != nil {
			return fmt.Errorf("count relationships: %w", err)
		}

		listArgs := append(append([]any{}, args...), page.Limit, page.Offset)
		query := fmt.Sprintf(
			"SELECT %s FROM ci_relationship WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d",
			relationshipSelectColumns,
			whereClause,
			argPos,
			argPos+1,
		)
		rows, err := tx.Query(ctx, query, listArgs...)
		if err != nil {
			return fmt.Errorf("list relationships: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanRelationship(rows)
			if err != nil {
				return fmt.Errorf("scan relationship: %w", err)
			}
			items = append(items, *item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate relationships: %w", err)
		}
		return nil
	})

	return items, total, err
}

func (r *PGRepository) Create(ctx context.Context, rel *Relationship) error {
	return database.WithRequestTenant(ctx, r.pool, rel.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if rel.Attributes == nil {
			rel.Attributes = make(map[string]any)
		}
		if rel.Source == "" {
			rel.Source = "manual"
		}
		query := `
			INSERT INTO ci_relationship (
				organization_id,
				source_ci_id,
				target_ci_id,
				rel_type,
				attributes,
				source,
				confidence,
				first_seen_at,
				last_seen_at,
				verification_state,
				source_system,
				notes
			)
			SELECT $1::uuid, $2::uuid, $3::uuid, $4::text, $5::jsonb, $6::text, $7::numeric,
				$8::timestamptz, $9::timestamptz, $10::text, $11::text, $12::text
			WHERE EXISTS (SELECT 1 FROM ci WHERE ci.id = $2)
			  AND EXISTS (SELECT 1 FROM ci WHERE ci.id = $3)
			RETURNING id::text, created_at, updated_at
		`
		var createdAt time.Time
		var updatedAt time.Time
		firstSeen := any(nil)
		if rel.Source != "manual" {
			firstSeen = time.Now().UTC()
		}
		verification := rel.VerificationState
		if verification == "" {
			verification = "unverified"
		}
		if err := tx.QueryRow(ctx, query,
			rel.OrganizationID,
			rel.SourceCIID,
			rel.TargetCIID,
			rel.RelType,
			rel.Attributes,
			rel.Source,
			rel.Confidence,
			firstSeen,
			firstSeen,
			verification,
			nilIfEmptyStr(rel.SourceSystem),
			nilIfEmptyStr(rel.Notes),
		).Scan(&rel.ID, &createdAt, &updatedAt); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("create relationship: %w", err)
		}
		rel.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
		rel.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
		return nil
	})
}

// Update edits the provenance and verification metadata of a relationship.
// The edge endpoints and rel_type are intentionally immutable.
func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Relationship, error) {
	var out *Relationship
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{"updated_at = now()"}
		args := []any{id, orgID}
		pos := 3
		if req.Attributes != nil {
			sets = append(sets, fmt.Sprintf("attributes = COALESCE(attributes, '{}'::jsonb) || $%d", pos))
			args = append(args, req.Attributes)
			pos++
		}
		if req.Confidence != nil {
			sets = append(sets, fmt.Sprintf("confidence = $%d", pos))
			args = append(args, *req.Confidence)
			pos++
		}
		if req.VerificationState != nil {
			sets = append(sets, fmt.Sprintf("verification_state = $%d", pos))
			args = append(args, *req.VerificationState)
			pos++
		}
		if req.SourceSystem != nil {
			sets = append(sets, fmt.Sprintf("source_system = $%d", pos))
			args = append(args, *req.SourceSystem)
			pos++
		}
		if req.Notes != nil {
			sets = append(sets, fmt.Sprintf("notes = $%d", pos))
			args = append(args, *req.Notes)
			pos++
		}

		row := tx.QueryRow(ctx, fmt.Sprintf(`
			UPDATE ci_relationship SET %s
			WHERE id = $1 AND organization_id = $2 AND %s
			RETURNING %s`, strings.Join(sets, ", "), visibleEndpoints, relationshipSelectColumns), args...)
		scanned, err := scanRelationship(row)
		if err != nil {
			if err == pgx.ErrNoRows {
				return fmt.Errorf("not found")
			}
			return fmt.Errorf("update relationship: %w", err)
		}
		out = scanned
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmdTag, err := tx.Exec(ctx, "DELETE FROM ci_relationship WHERE id = $1 AND organization_id = $2 AND "+visibleEndpoints, id, orgID)
		if err != nil {
			return fmt.Errorf("delete relationship: %w", err)
		}
		if cmdTag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// TraverseFrom walks the configuration graph from rootCIID with a single
// recursive CTE instead of hop-by-hop queries. The traversal follows
// relationships in both directions, is limited to maxDepth hops, stops
// expanding once maxNodes distinct CIs are on the frontier, and is
// cycle-guarded by the visited set. The recursive term joins on the endpoint
// reached in the previous step (target, falling back to source for the root's
// own rows is unnecessary because the anchor records both directions), and
// UNION (not UNION ALL) deduplicates relationship rows during recursion so
// dense graphs cannot fan out combinatorially. Tenant isolation is enforced
// twice: inside the query and by the RLS policy on ci_relationship.
func (r *PGRepository) TraverseFrom(ctx context.Context, orgID, rootCIID string, maxDepth, maxNodes int) ([]Relationship, error) {
	items := make([]Relationship, 0)

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH RECURSIVE walk AS (
				SELECT
					r.id,
					r.source_ci_id,
					r.target_ci_id,
					r.rel_type,
					r.source,
					-- The endpoint the walk entered through; expansion
					-- continues from the other endpoint.
					CASE WHEN r.source_ci_id = $2 THEN r.target_ci_id ELSE r.source_ci_id END AS frontier_ci_id,
					1 AS depth,
					ARRAY[r.source_ci_id, r.target_ci_id] AS visited
				FROM ci_relationship r
				WHERE r.organization_id = $1
				  AND (r.source_ci_id = $2 OR r.target_ci_id = $2)

				UNION

				SELECT
					r.id,
					r.source_ci_id,
					r.target_ci_id,
					r.rel_type,
					r.source,
					CASE WHEN r.source_ci_id = w.frontier_ci_id THEN r.target_ci_id ELSE r.source_ci_id END,
					w.depth + 1,
					w.visited || r.source_ci_id || r.target_ci_id
				FROM ci_relationship r
				JOIN walk w
				  ON (r.source_ci_id = w.frontier_ci_id OR r.target_ci_id = w.frontier_ci_id)
				WHERE r.organization_id = $1
				  AND w.depth < $3
				  AND cardinality(w.visited) < $4
				  AND NOT (r.source_ci_id = ANY (w.visited) AND r.target_ci_id = ANY (w.visited))
			)
			SELECT DISTINCT `+traversalSelectColumns+`
			FROM walk
		`, orgID, rootCIID, maxDepth, maxNodes)
		if err != nil {
			return fmt.Errorf("traverse relationships: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			var item Relationship
			if err := rows.Scan(&item.ID, &item.SourceCIID, &item.TargetCIID, &item.RelType, &item.Source); err != nil {
				return fmt.Errorf("scan traversed relationship: %w", err)
			}
			items = append(items, item)
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate traversed relationships: %w", err)
		}
		return nil
	})

	return items, err
}

const traversalSelectColumns = `
	id::text,
	source_ci_id::text,
	target_ci_id::text,
	rel_type,
	source
`

type relationshipScanner interface {
	Scan(dest ...any) error
}

func scanRelationship(scanner relationshipScanner) (*Relationship, error) {
	item := &Relationship{}
	var createdAt time.Time
	var updatedAt time.Time
	var confidence *float64
	var firstSeen, lastSeen *time.Time
	var verification *string
	if err := scanner.Scan(
		&item.ID,
		&item.OrganizationID,
		&item.SourceCIID,
		&item.TargetCIID,
		&item.RelType,
		&item.Attributes,
		&item.Source,
		&confidence,
		&firstSeen,
		&lastSeen,
		&verification,
		&item.SourceSystem,
		&item.Notes,
		&createdAt,
		&updatedAt,
	); err != nil {
		return nil, err
	}
	if item.Attributes == nil {
		item.Attributes = make(map[string]any)
	}
	item.Confidence = confidence
	if firstSeen != nil {
		item.FirstSeenAt = firstSeen.UTC().Format(time.RFC3339Nano)
	}
	if lastSeen != nil {
		item.LastSeenAt = lastSeen.UTC().Format(time.RFC3339Nano)
	}
	if verification != nil {
		item.VerificationState = *verification
	}
	item.CreatedAt = createdAt.UTC().Format(time.RFC3339Nano)
	item.UpdatedAt = updatedAt.UTC().Format(time.RFC3339Nano)
	return item, nil
}

func nilIfEmptyStr(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
