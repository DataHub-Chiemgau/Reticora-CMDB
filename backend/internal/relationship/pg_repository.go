package relationship

import (
	"cmp"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
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
	updated_at,
	COALESCE(source_interface_id::text, ''),
	COALESCE(target_interface_id::text, ''),
	last_confirmed_at,
	COALESCE(verified_by::text, ''),
	verified_at
`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
	// bulkFrontier is the frontier size beyond which a traversal reads every
	// edge of the organization at once instead of the next level.
	bulkFrontier int
	// outbox receives relationship.created and relationship.removed
	// (WithEvents).
	outbox *events.Outbox
}

// DefaultBulkFrontier: a traversal whose frontier exceeds this many nodes
// reads all edges of the organization in one statement instead of the next
// levels (NFR-02, WP-335).
const DefaultBulkFrontier = 1000

// NewPGRepository creates a new PostgreSQL-backed relationship repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool, bulkFrontier: DefaultBulkFrontier}
}

// WithBulkFrontier sets the frontier size beyond which a traversal switches
// to the organization read; 0 switches before the first level (tests run
// IMP-07 in both modes).
func (r *PGRepository) WithBulkFrontier(n int) *PGRepository {
	r.bulkFrontier = max(n, 0)
	return r
}

// WithEvents makes Create, Delete and DeleteWithPolicy write
// relationship.created and relationship.removed into the outbox of their
// transaction (EVT-01, EVT-02): a refused or rolled back mutation leaves no
// event.
func (r *PGRepository) WithEvents(outbox *events.Outbox) *PGRepository {
	r.outbox = outbox
	return r
}

// recordEvent writes the event of an edge mutation in tx, with the edge as
// written or deleted as data: in a context transaction (the discovery
// ingest) right before its commit, after every other write, else at once
// (database.BeforeCommit: the outbox takes an organization-wide lock until
// the commit, WP-334).
func (r *PGRepository) recordEvent(ctx context.Context, tx pgx.Tx, eventType string, rel *Relationship) error {
	if r.outbox == nil {
		return nil
	}
	// The caller may change its edge before a later commit.
	edge := *rel
	edge.Attributes = maps.Clone(rel.Attributes)
	return database.BeforeCommit(ctx, func() error {
		_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: edge.OrganizationID, Type: eventType, EntityID: edge.ID, Data: &edge})
		return err
	})
}

// visibleEndpoints restricts relationship rows to edges whose source and
// target CI are both visible under the transaction's tenant scope: the ci
// policy filters the subqueries by client scope, while ci_relationship itself
// carries no client column yet (WP-025). A client-scoped principal therefore
// neither sees nor changes edges that touch another client's CI.
const visibleEndpoints = `EXISTS (SELECT 1 FROM ci WHERE ci.id = ci_relationship.source_ci_id)
	AND EXISTS (SELECT 1 FROM ci WHERE ci.id = ci_relationship.target_ci_id)`

// projectedEdges are the structural relationships of ci_structure_edge in
// the column layout of relationshipSelectColumns (DB-05, CH27). The id is
// derived from the edge so it is stable across reads.
const projectedEdges = `
	SELECT md5(e.rel_type || ':' || e.source_ci_id::text || ':' ||
	           COALESCE(e.target_ci_id::text, e.target_location_id::text))::uuid AS id,
	       e.organization_id, e.source_ci_id, e.target_ci_id, e.rel_type,
	       '{}'::jsonb AS attributes, 'projection'::text AS source, NULL::numeric AS confidence,
	       NULL::timestamptz AS first_seen_at, NULL::timestamptz AS last_seen_at,
	       'verified'::text AS verification_state, NULL::text AS source_system, NULL::text AS notes,
	       src.created_at, src.updated_at, NULL::uuid AS source_interface_id, NULL::uuid AS target_interface_id,
	       NULL::timestamptz AS last_confirmed_at, NULL::uuid AS verified_by, NULL::timestamptz AS verified_at,
	       true AS projected, e.target_location_id
	FROM ci_structure_edge e
	JOIN ci src ON src.id = e.source_ci_id
	WHERE e.target_ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = e.target_ci_id)`

// graphEdges are the stored relationships plus the projected containment
// (CI → CI), the edges topology and impact traverse.
const graphEdges = `(
	SELECT id, organization_id, source_ci_id, target_ci_id, rel_type, source FROM ci_relationship
	UNION ALL
	SELECT md5('contains:' || source_ci_id::text || ':' || target_ci_id::text)::uuid, organization_id,
	       source_ci_id, target_ci_id, rel_type, 'projection'
	FROM ci_structure_edge WHERE rel_type = 'contains'
)`

// listEdges is the edge set List counts and pages: the stored relationships
// of the organization ($1) with visible endpoints and the projected ones,
// with byCI those of the CI $2.
//
// The edges of one CI are one arm per end of the edge, in the stored and
// in the projected edges (WP-609): source = $2, and target = $2 for edges
// whose source is another CI, so an edge of the CI to itself is listed
// once. As an equality the CI reaches every branch of ci_structure_edge
// and the join with ci src. An OR of both ends stays a filter above the
// projection; with RLS the planner estimates one visible CI and, once
// rack_mount is large (it holds the mounts of all organizations), joins
// every CI of the organization with the projection (NFR-02).
func listEdges(byCI bool) string {
	stored := `
			SELECT id, organization_id, source_ci_id, target_ci_id, rel_type, attributes, source, confidence,
			       first_seen_at, last_seen_at, verification_state, source_system, notes, created_at, updated_at,
			       source_interface_id, target_interface_id, last_confirmed_at, verified_by, verified_at,
			       false AS projected, NULL::uuid AS target_location_id
			FROM ci_relationship WHERE organization_id = $1 AND ` + visibleEndpoints
	projected := `
			SELECT * FROM (` + projectedEdges + `) p WHERE p.organization_id = $1`
	arms := []string{stored, projected}
	if byCI {
		arms = make([]string, 0, 4)
		for _, edges := range []string{stored, projected} {
			arms = append(arms,
				edges+` AND source_ci_id = $2`,
				edges+` AND target_ci_id = $2 AND source_ci_id IS DISTINCT FROM $2`)
		}
	}
	return strings.Join(arms, `
			UNION ALL`)
}

// List returns the relationships of the organization (of one CI when ciID
// is set): the stored ones and, after them, the projected structural ones.
func (r *PGRepository) List(ctx context.Context, orgID string, ciID string, page api.PaginationParams) ([]Relationship, int, error) {
	items := make([]Relationship, 0)
	var total int

	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		args := []any{orgID}
		if ciID != "" {
			args = append(args, ciID)
		}
		edges := listEdges(ciID != "")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM ("+edges+") e", args...).Scan(&total); err != nil {
			return fmt.Errorf("count relationships: %w", err)
		}

		limitPos := len(args) + 1
		query := fmt.Sprintf(`SELECT id::text, organization_id::text, source_ci_id::text, COALESCE(target_ci_id::text, ''),
				rel_type, attributes, source, confidence, first_seen_at, last_seen_at, verification_state,
				COALESCE(source_system, ''), COALESCE(notes, ''), created_at, updated_at,
				COALESCE(source_interface_id::text, ''), COALESCE(target_interface_id::text, ''), last_confirmed_at,
				COALESCE(verified_by::text, ''), verified_at,
				projected, COALESCE(target_location_id::text, '')
			FROM (%s) e ORDER BY projected, created_at DESC, id LIMIT $%d OFFSET $%d`, edges, limitPos, limitPos+1)
		rows, err := tx.Query(ctx, query, append(args, page.Limit, page.Offset)...)
		if err != nil {
			return fmt.Errorf("list relationships: %w", err)
		}
		defer rows.Close()

		for rows.Next() {
			item, err := scanRelationship(rows, true)
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
		// The endpoints are checked under the caller's scope in the same
		// statement as the insert: a CI the caller cannot see (another
		// organization, a client or site outside the scope, an unknown id)
		// inserts nothing and is named by its sentinel (F-145).
		query := `
			WITH ends AS (
				SELECT EXISTS (SELECT 1 FROM ci WHERE ci.id = $2::uuid) AS source_visible,
				       EXISTS (SELECT 1 FROM ci WHERE ci.id = $3::uuid) AS target_visible
			), created AS (
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
					notes,
					source_interface_id,
					target_interface_id,
					last_confirmed_at,
					verified_by,
					verified_at
				)
				SELECT $1::uuid, $2::uuid, $3::uuid, $4::text, $5::jsonb, $6::text, $7::numeric,
					$8::timestamptz, $9::timestamptz, $10::text, $11::text, $12::text,
					$13::uuid, $14::uuid, now(),
					(SELECT id FROM app_user WHERE id::text = $15::text),
					CASE WHEN $10::text = 'verified' THEN now() END
				FROM ends WHERE ends.source_visible AND ends.target_visible
				RETURNING id::text AS id, created_at, updated_at, last_confirmed_at,
					COALESCE(verified_by::text, '') AS verified_by, verified_at
			)
			SELECT ends.source_visible, ends.target_visible, created.*
			FROM ends LEFT JOIN created ON true
		`
		var sourceVisible, targetVisible bool
		var id, verifiedBy *string
		var createdAt, updatedAt, confirmedAt, verifiedAt *time.Time
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
			nilIfEmptyStr(rel.SourceInterfaceID),
			nilIfEmptyStr(rel.TargetInterfaceID),
			rel.VerifiedBy,
		).Scan(&sourceVisible, &targetVisible, &id, &createdAt, &updatedAt, &confirmedAt, &verifiedBy, &verifiedAt); err != nil {
			return fmt.Errorf("create relationship: %w", err)
		}
		var invisible []error
		if !sourceVisible {
			invisible = append(invisible, ErrSourceCINotFound)
		}
		if !targetVisible {
			invisible = append(invisible, ErrTargetCINotFound)
		}
		if len(invisible) > 0 {
			return errors.Join(invisible...)
		}
		if id == nil {
			return fmt.Errorf("create relationship: no row inserted")
		}
		rel.ID, rel.VerifiedBy = *id, *verifiedBy
		rel.CreatedAt = formatTime(createdAt)
		rel.UpdatedAt = formatTime(updatedAt)
		rel.LastConfirmedAt = formatTime(confirmedAt)
		rel.VerifiedAt = formatTime(verifiedAt)
		return r.recordEvent(ctx, tx, events.RelationshipCreated, rel)
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
			// Verifying records who and when; leaving the verified state
			// clears both (REL-02).
			sets = append(sets, fmt.Sprintf("verification_state = $%d", pos),
				fmt.Sprintf("verified_by = CASE WHEN $%d = 'verified' THEN (SELECT id FROM app_user WHERE id::text = $%d) END", pos, pos+1),
				fmt.Sprintf("verified_at = CASE WHEN $%d = 'verified' THEN now() END", pos))
			args = append(args, *req.VerificationState, req.ActorID)
			pos += 2
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
		deleted, err := scanRelationship(tx.QueryRow(ctx, "DELETE FROM ci_relationship WHERE id = $1 AND organization_id = $2 AND "+
			visibleEndpoints+" RETURNING "+relationshipSelectColumns, id, orgID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return fmt.Errorf("delete relationship: %w", err)
		}
		return r.recordEvent(ctx, tx, events.RelationshipRemoved, deleted)
	})
}

// DeleteWithPolicy implements PolicyDeleter: a discovery edge is deleted
// only with force and then recorded in relationship_suppression for its
// CIs and type, so discovery does not recreate it (REL-03, REL-04); other
// edges are deleted as asked.
func (r *PGRepository) DeleteWithPolicy(ctx context.Context, orgID, id string, force bool) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		edge, err := scanRelationship(tx.QueryRow(ctx, `SELECT `+relationshipSelectColumns+` FROM ci_relationship
			WHERE id = $1 AND organization_id = $2 AND `+visibleEndpoints+` FOR UPDATE`, id, orgID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return fmt.Errorf("delete relationship: %w", err)
		}
		source, relType, src, dst := edge.Source, edge.RelType, edge.SourceCIID, edge.TargetCIID
		if source == "discovery" && !force {
			return ErrForceRequired
		}
		if _, err = tx.Exec(ctx, `DELETE FROM ci_relationship WHERE id = $1 AND organization_id = $2`, id, orgID); err != nil {
			return fmt.Errorf("delete relationship: %w", err)
		}
		if source != "discovery" {
			return r.recordEvent(ctx, tx, events.RelationshipRemoved, edge)
		}
		tag, err := tx.Exec(ctx, `INSERT INTO relationship_suppression (organization_id, source_ci_id, target_ci_id, relationship_type_id)
			SELECT $1, $2, $3, rt.id FROM relationship_type rt
			WHERE rt.key = $4 AND (rt.organization_id = $1 OR rt.organization_id IS NULL)
			ORDER BY rt.organization_id NULLS LAST LIMIT 1
			ON CONFLICT (organization_id, source_ci_id, target_ci_id, relationship_type_id) DO NOTHING`, orgID, src, dst, relType)
		if err != nil {
			return fmt.Errorf("suppress relationship: %w", err)
		}
		if tag.RowsAffected() == 0 {
			// The type has no relationship_type row, or the edge was
			// suppressed already; without a row the deletion would not
			// stick, so it is refused.
			var exists bool
			if qerr := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM relationship_suppression s JOIN relationship_type rt ON rt.id = s.relationship_type_id
				WHERE s.organization_id = $1 AND s.source_ci_id = $2 AND s.target_ci_id = $3 AND rt.key = $4)`, orgID, src, dst, relType).Scan(&exists); qerr != nil {
				return qerr
			}
			if !exists {
				return fmt.Errorf("suppress relationship: unknown relationship type %q", relType)
			}
		}
		return r.recordEvent(ctx, tx, events.RelationshipRemoved, edge)
	})
}

// TraverseFrom walks the configuration graph from rootCIID like WalkFrom and
// returns the kept edges as relationships, ordered by id. The result follows
// SelectTraversal, the semantics the memory repository shares.
func (r *PGRepository) TraverseFrom(ctx context.Context, orgID, rootCIID string, maxDepth, maxNodes int) ([]Relationship, error) {
	walk, err := r.WalkFrom(ctx, orgID, rootCIID, maxDepth, maxNodes)
	if err != nil {
		return nil, err
	}
	result := make([]Relationship, 0, len(walk.Edges))
	for i := range walk.Edges {
		e := &walk.Edges[i]
		result = append(result, Relationship{ID: e.ID, SourceCIID: walk.CIs[e.SourceCI], TargetCIID: walk.CIs[e.TargetCI],
			RelType: e.RelType, Source: e.Source})
	}
	return result, nil
}

// WalkFrom implements Walker. It walks the configuration graph from
// rootCIID breadth first, one edge read per hop, under the caller's full
// tenant scope (RLS on ci and ci_relationship). Each node is expanded once,
// so a walk costs at most two statements per hop and reads each edge at
// most twice, whatever cycles the graph has (NFR-02, WP-206; the former
// recursive CTE re-expanded every node at every depth). Once a level
// reaches more than bulkFrontier CIs, the walk reads every edge of the
// organization in one statement instead of the further levels (WP-335):
// the statements of a walk then no longer grow with the graph. The walk
// only passes through CIs that are visible and not deleted: the CIs a level
// reaches are checked against ci under the same scope, the root with the
// first of them, and an edge is followed only when both endpoints passed,
// so an invisible or deleted intermediate node never connects visible ones
// (IMP-07). The result follows SelectTraversal.
//
// The walk reads only what it needs of an edge (id, source, target, type
// and origin), the ids in their binary form, and works on CI numbers: the
// adjacency, the hop distances and the selection are slices indexed by
// number instead of maps keyed by id strings (NFR-02, WP-611). Only the CIs
// and edges it returns get their text form.
func (r *PGRepository) WalkFrom(ctx context.Context, orgID, rootCIID string, maxDepth, maxNodes int) (*Walk, error) {
	if maxDepth < 1 || maxNodes < 1 {
		return &Walk{CIs: []string{rootCIID}, Edges: []WalkEdge{}}, nil
	}
	var root pgtype.UUID
	if err := root.Scan(rootCIID); err != nil {
		return nil, fmt.Errorf("traverse relationships: root: %w", err)
	}
	g := newWalkGraph(root.Bytes)
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return r.walk(ctx, tx, orgID, g, maxDepth, maxNodes)
	})
	if err != nil {
		return nil, err
	}
	return g.selectWalk(maxDepth, maxNodes), nil
}

// walk reads the edges and CIs of a walk from the root (number 0) into g.
func (r *PGRepository) walk(ctx context.Context, tx pgx.Tx, orgID string, g *walkGraph, maxDepth, maxNodes int) error {
	// A wide level reaches most of the graph: one read of all edges
	// replaces the further levels, and the selection re-derives the walk
	// from it. The edges come first, then the CIs, so an edge to a CI
	// deleted or moved out of the scope in between is dropped.
	readAll := func() error {
		edges, err := g.readEdges(ctx, tx, `SELECT `+walkSelectColumns+` FROM `+graphEdges+` r WHERE r.organization_id = $1`, orgID)
		if err != nil {
			return err
		}
		g.edges = edges
		return g.checkOrganization(ctx, tx, orgID)
	}
	seenEdge := map[edgeKey]struct{}{}
	reached := 1
	frontier := []int32{0}
	hops := maxDepth
	for hop := 0; hop < hops && len(frontier) > 0; hop++ {
		if len(frontier) > r.bulkFrontier {
			return readAll()
		}
		ids := make([]uuid, len(frontier))
		for i, n := range frontier {
			ids[i] = g.ids[n]
		}
		// The edge reads carry no join with ci: the edge rows pass the RLS
		// of ci_relationship and of the security_invoker view
		// ci_structure_edge, which leave edges to CIs outside the scope or
		// deleted ones (and the parent of a containment) unchecked. The
		// CIs read afterwards under the same scope close that gap (IMP-07).
		level, err := g.readEdges(ctx, tx, `SELECT `+walkSelectColumns+` FROM `+graphEdges+` r
			WHERE r.organization_id = $1 AND (r.source_ci_id = ANY($2::uuid[]) OR r.target_ci_id = ANY($2::uuid[]))`, orgID, ids)
		if err != nil {
			return err
		}
		// The CIs this level reaches first; the root is checked with them.
		var unchecked []int32
		fresh := level[:0]
		for i := range level {
			if _, seen := seenEdge[level[i].id]; seen {
				continue
			}
			seenEdge[level[i].id] = struct{}{}
			fresh = append(fresh, level[i])
			for _, n := range [2]int32{level[i].source, level[i].target} {
				if g.state[n] == ciUnchecked && n != 0 {
					g.state[n] = ciHidden
					unchecked = append(unchecked, n)
				}
			}
		}
		if len(unchecked) > r.bulkFrontier && hop+1 < hops {
			return readAll()
		}
		check := unchecked
		if g.state[0] == ciUnchecked {
			check = append([]int32{0}, unchecked...)
		}
		if err := g.checkCIs(ctx, tx, orgID, check); err != nil {
			return err
		}
		if g.state[0] != ciVisible {
			return nil
		}
		for i := range fresh {
			if g.state[fresh[i].source] == ciVisible && g.state[fresh[i].target] == ciVisible {
				g.edges = append(g.edges, fresh[i])
			}
		}
		next := unchecked[:0]
		for _, n := range unchecked {
			if g.state[n] == ciVisible {
				next = append(next, n)
			}
		}
		reached += len(next)
		// Beyond the node limit the walk ends after collecting the edges
		// of the nodes just reached: nodes further away are dropped by the
		// limit anyway.
		if reached > maxNodes && len(next) > 0 {
			hops = min(hops, hop+2)
		}
		frontier = next
	}
	return nil
}

// walkSelectColumns are the columns a walk reads of an edge: the ids in
// their binary form (readEdges).
const walkSelectColumns = `r.id, r.source_ci_id, r.target_ci_id, r.rel_type, r.source`

// uuid is an id in its binary form.
type uuid = [16]byte

// ciState is what a walk knows of a CI's visibility.
type ciState uint8

const (
	// ciUnchecked: not checked yet.
	ciUnchecked ciState = iota
	// ciHidden: not visible under the scope, deleted, or being checked.
	ciHidden
	// ciVisible: visible and not deleted.
	ciVisible
)

// walkGraph is what a walk reads: every CI it met as a number (the root is
// 0) with its id and visibility, and the edges with the numbers of their
// ends. The types and origins of the edges are interned (text).
type walkGraph struct {
	number map[uuid]int32
	ids    []uuid
	state  []ciState
	edges  []walkRow
	text   []string
	index  map[string]int32
}

// walkRow is an edge a walk read.
type walkRow struct {
	id             edgeKey
	source, target int32
	relType        int32
	origin         int32
}

// edgeKey is an edge id as its two big-endian halves: compared like the
// text form, and cheaper to sort than the bytes.
type edgeKey [2]uint64

func keyOf(id *uuid) edgeKey {
	return edgeKey{binary.BigEndian.Uint64(id[:8]), binary.BigEndian.Uint64(id[8:])}
}

func (k edgeKey) uuid() uuid {
	var id uuid
	binary.BigEndian.PutUint64(id[:8], k[0])
	binary.BigEndian.PutUint64(id[8:], k[1])
	return id
}

func compareKeys(a, b edgeKey) int {
	if a[0] != b[0] {
		return cmp.Compare(a[0], b[0])
	}
	return cmp.Compare(a[1], b[1])
}

func newWalkGraph(root uuid) *walkGraph {
	g := &walkGraph{number: map[uuid]int32{}, index: map[string]int32{}}
	g.ci(root)
	return g
}

// ci returns the number of the CI id, a new one for a CI not met before.
func (g *walkGraph) ci(id uuid) int32 {
	if n, ok := g.number[id]; ok {
		return n
	}
	n := int32(len(g.ids))
	g.number[id] = n
	g.ids = append(g.ids, id)
	g.state = append(g.state, ciUnchecked)
	return n
}

// intern returns the number of a type or origin; the lookup of raw does
// not copy it.
func (g *walkGraph) intern(raw []byte) int32 {
	if n, ok := g.index[string(raw)]; ok {
		return n
	}
	s := string(raw)
	n := int32(len(g.text))
	g.index[s] = n
	g.text = append(g.text, s)
	return n
}

// readEdges reads the edges of query (walkSelectColumns) from the raw rows
// and gives the CIs at their ends numbers.
func (g *walkGraph) readEdges(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]walkRow, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("traverse relationships: %w", err)
	}
	defer rows.Close()
	var edges []walkRow
	var formats []pgconn.FieldDescription
	for rows.Next() {
		if formats == nil {
			formats = rows.FieldDescriptions()
		}
		raw := rows.RawValues()
		if len(raw) != 5 || raw[3] == nil || raw[4] == nil {
			return nil, errors.New("scan traversed relationship: unexpected row")
		}
		var ids [3]uuid
		for i := range ids {
			if ids[i], err = uuidValue(raw[i], formats[i].Format); err != nil {
				return nil, fmt.Errorf("scan traversed relationship: %w", err)
			}
		}
		e := walkRow{id: keyOf(&ids[0]), source: g.ci(ids[1]), target: g.ci(ids[2])}
		e.relType, e.origin = g.intern(raw[3]), g.intern(raw[4])
		edges = append(edges, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan traversed relationship: %w", err)
	}
	return edges, nil
}

// checkCIs marks those of the CIs nums that are visible, not deleted CIs
// under the transaction's scope; the others are hidden.
func (g *walkGraph) checkCIs(ctx context.Context, tx pgx.Tx, orgID string, nums []int32) error {
	if len(nums) == 0 {
		return nil
	}
	ids := make([]uuid, len(nums))
	for i, n := range nums {
		ids[i] = g.ids[n]
		g.state[n] = ciHidden
	}
	return g.markVisible(ctx, tx, `SELECT id FROM ci WHERE organization_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL`, orgID, ids)
}

// checkOrganization reads the visible, not deleted CIs of the organization
// and marks the CIs of g by them.
func (g *walkGraph) checkOrganization(ctx context.Context, tx pgx.Tx, orgID string) error {
	for n := range g.state {
		g.state[n] = ciHidden
	}
	return g.markVisible(ctx, tx, `SELECT id FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, orgID)
}

// markVisible marks the CIs of g among the ids query returns visible.
func (g *walkGraph) markVisible(ctx context.Context, tx pgx.Tx, query string, args ...any) error {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("read visible cis: %w", err)
	}
	defer rows.Close()
	var format int16
	first := true
	for rows.Next() {
		if first {
			format, first = rows.FieldDescriptions()[0].Format, false
		}
		raw := rows.RawValues()
		if len(raw) != 1 {
			return errors.New("read visible cis: unexpected row")
		}
		id, err := uuidValue(raw[0], format)
		if err != nil {
			return fmt.Errorf("read visible cis: %w", err)
		}
		if n, ok := g.number[id]; ok {
			g.state[n] = ciVisible
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read visible cis: %w", err)
	}
	return nil
}

// uuidValue reads a uuid column of a raw row: 16 bytes in the binary
// format, the text form in the text format.
func uuidValue(raw []byte, format int16) (uuid, error) {
	var id uuid
	switch {
	case raw == nil:
		return id, errors.New("null id")
	case format == pgx.BinaryFormatCode && len(raw) == len(id):
		copy(id[:], raw)
		return id, nil
	case format == pgx.TextFormatCode:
		var text pgtype.UUID
		if err := text.Scan(string(raw)); err != nil {
			return id, err
		}
		return text.Bytes, nil
	}
	return id, fmt.Errorf("id of %d bytes in format %d", len(raw), format)
}

// compareUUID orders ids like their text form.
func compareUUID(a, b *uuid) int {
	return compareKeys(keyOf(a), keyOf(b))
}

// formatUUID returns the text form of id as PostgreSQL writes it.
func formatUUID(id *uuid) string {
	var b [36]byte
	hex.Encode(b[0:8], id[0:4])
	b[8] = '-'
	hex.Encode(b[9:13], id[4:6])
	b[13] = '-'
	hex.Encode(b[14:18], id[6:8])
	b[18] = '-'
	hex.Encode(b[19:23], id[8:10])
	b[23] = '-'
	hex.Encode(b[24:], id[10:])
	return string(b[:])
}

// selectWalk is SelectTraversal on the CI numbers of g: every node gets
// its hop distance from the root (at most maxDepth); the maxNodes nodes
// with the smallest distance (ties by id) are kept, and the walk holds each
// edge between visible kept nodes that leaves a node closer than maxDepth,
// ordered by id. The CIs are numbered anew in the order the walk reaches
// them, over the edges in the order of their ids, so the numbering does not
// depend on the order the rows were read in.
func (g *walkGraph) selectWalk(maxDepth, maxNodes int) *Walk {
	walk := &Walk{CIs: []string{formatUUID(&g.ids[0])}, Edges: []WalkEdge{}}
	if g.state[0] != ciVisible {
		return walk
	}
	edges := g.edges[:0]
	for i := range g.edges {
		if g.state[g.edges[i].source] == ciVisible && g.state[g.edges[i].target] == ciVisible {
			edges = append(edges, g.edges[i])
		}
	}
	slices.SortFunc(edges, func(a, b walkRow) int { return compareKeys(a.id, b.id) })

	// Adjacency index: the edges of each node in the order of their ids,
	// each node expands its own edges only (NFR-02).
	n := len(g.ids)
	first := make([]int32, n+1)
	for i := range edges {
		first[edges[i].source+1]++
		if edges[i].target != edges[i].source {
			first[edges[i].target+1]++
		}
	}
	for v := 0; v < n; v++ {
		first[v+1] += first[v]
	}
	incident := make([]int32, first[n])
	next := slices.Clone(first[:n])
	for i := range edges {
		incident[next[edges[i].source]] = int32(i)
		next[edges[i].source]++
		if edges[i].target != edges[i].source {
			incident[next[edges[i].target]] = int32(i)
			next[edges[i].target]++
		}
	}

	depth := make([]int32, n)
	for v := range depth {
		depth[v] = -1
	}
	depth[0] = 0
	order := []int32{0}
	for d, start := int32(1), 0; int(d) <= maxDepth && start < len(order); d++ {
		end := len(order)
		for _, v := range order[start:end] {
			for _, i := range incident[first[v]:first[v+1]] {
				neighbor := edges[i].target
				if neighbor == v {
					neighbor = edges[i].source
				}
				if depth[neighbor] < 0 {
					depth[neighbor] = d
					order = append(order, neighbor)
				}
			}
		}
		start = end
	}

	// Beyond the node limit only the nodes closest to the root are kept.
	kept := order
	if len(order) > maxNodes {
		kept = slices.Clone(order)
		slices.SortFunc(kept, func(a, b int32) int {
			if depth[a] != depth[b] {
				return cmp.Compare(depth[a], depth[b])
			}
			return compareUUID(&g.ids[a], &g.ids[b])
		})
		limited := make([]bool, n)
		for _, v := range kept[:maxNodes] {
			limited[v] = true
		}
		kept = order[:0:0]
		for _, v := range order {
			if limited[v] {
				kept = append(kept, v)
			}
		}
	}
	number := make([]int32, n)
	for v := range number {
		number[v] = -1
	}
	walk.CIs = make([]string, len(kept))
	walk.Edges = make([]WalkEdge, 0, len(edges))
	for k, v := range kept {
		number[v] = int32(k)
		walk.CIs[k] = formatUUID(&g.ids[v])
	}
	for i := range edges {
		e := &edges[i]
		source, target := number[e.source], number[e.target]
		if source < 0 || target < 0 || min(depth[e.source], depth[e.target]) >= int32(maxDepth) {
			continue
		}
		id := e.id.uuid()
		walk.Edges = append(walk.Edges, WalkEdge{ID: formatUUID(&id), SourceCI: source, TargetCI: target,
			RelType: g.text[e.relType], Source: g.text[e.origin]})
	}
	return walk
}

const traversalSelectColumns = `
	r.id::text,
	r.source_ci_id::text,
	r.target_ci_id::text,
	r.rel_type,
	r.source
`

// edgeRow is what EdgesAmong reads of an edge, the ids as text (a walk
// reads them binary, walkSelectColumns).
type edgeRow struct{ id, sourceCIID, targetCIID, relType, source string }

func edgeRowEnds(e *edgeRow) (source, target string) { return e.sourceCIID, e.targetCIID }

// EdgesAmong implements EdgeReader under the caller's full tenant scope
// (NFR-02, WP-602), like a level of WalkFrom: one statement reads the
// edges of graphEdges, the stored relationships and the projected
// containment as List and WalkFrom read them, between two of ids; a
// second one the CIs among ids that are visible and not deleted under the
// same scope (visibleCIs). An edge is kept when both of its CIs are, as
// visibleEndpoints keeps it for List. Neither statement joins edges with
// ci: the plan of such a join follows the statistics, and with stale ones
// it read the CIs of the organization once per edge (seconds for 2,000
// CIs). Both statements are filtered by the constant ids alone.
func (r *PGRepository) EdgesAmong(ctx context.Context, orgID string, ids []string) ([]Relationship, error) {
	result := make([]Relationship, 0)
	if len(ids) == 0 {
		return result, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		edges, err := readEdges(ctx, tx, `SELECT `+traversalSelectColumns+` FROM `+graphEdges+` r
			WHERE r.organization_id = $1 AND r.source_ci_id = ANY($2::uuid[]) AND r.target_ci_id = ANY($2::uuid[])
			ORDER BY r.id`, orgID, ids)
		if err != nil {
			return err
		}
		visible := make(map[string]bool, len(ids))
		if err := visibleCIs(ctx, tx, orgID, ids, visible); err != nil {
			return err
		}
		for _, e := range visibleEdges(edges, edgeRowEnds, visible) {
			result = append(result, Relationship{ID: e.id, SourceCIID: e.sourceCIID, TargetCIID: e.targetCIID, RelType: e.relType, Source: e.source})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func readEdges(ctx context.Context, tx pgx.Tx, query string, args ...any) ([]edgeRow, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("traverse relationships: %w", err)
	}
	edges, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (edgeRow, error) {
		var e edgeRow
		serr := row.Scan(&e.id, &e.sourceCIID, &e.targetCIID, &e.relType, &e.source)
		return e, serr
	})
	if err != nil {
		return nil, fmt.Errorf("scan traversed relationship: %w", err)
	}
	return edges, nil
}

// visibleCIs marks those of ids in visible that are visible, not deleted
// CIs under the transaction's scope.
func visibleCIs(ctx context.Context, tx pgx.Tx, orgID string, ids []string, visible map[string]bool) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM ci WHERE organization_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL`, orgID, ids)
	if err != nil {
		return fmt.Errorf("read visible cis: %w", err)
	}
	found, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read visible cis: %w", err)
	}
	for _, id := range found {
		visible[id] = true
	}
	return nil
}

type relationshipScanner interface {
	Scan(dest ...any) error
}

func scanRelationship(scanner relationshipScanner, withProjection ...bool) (*Relationship, error) {
	item := &Relationship{}
	var createdAt time.Time
	var updatedAt time.Time
	var confidence *float64
	var firstSeen, lastSeen, confirmedAt, verifiedAt *time.Time
	var verification *string
	dest := []any{
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
		&item.SourceInterfaceID,
		&item.TargetInterfaceID,
		&confirmedAt,
		&item.VerifiedBy,
		&verifiedAt,
	}
	if len(withProjection) > 0 && withProjection[0] {
		dest = append(dest, &item.Projected, &item.TargetLocationID)
	}
	if err := scanner.Scan(dest...); err != nil {
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
	item.LastConfirmedAt = formatTime(confirmedAt)
	item.VerifiedAt = formatTime(verifiedAt)
	return item, nil
}

func formatTime(t *time.Time) string {
	if t == nil {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func nilIfEmptyStr(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

// Confirm records that discovery saw an edge again (TOP-02): last_seen_at
// and last_confirmed_at become now, the confidence the reported one. Only
// discovery edges are confirmed this way (source policy, REL-08).
func (r *PGRepository) Confirm(ctx context.Context, orgID, id string, confidence *float64) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE ci_relationship
			SET last_seen_at = now(), last_confirmed_at = now(), confidence = COALESCE($3::numeric, confidence)
			WHERE id = $1 AND organization_id = $2 AND source = 'discovery'`, id, orgID, confidence)
		if err == nil && tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return err
	})
}

// ConfirmMany implements BatchConfirmer with one UPDATE: the confirmations
// of an edge apply as one Confirm after the other would (the last reported
// confidence wins), and the rows are taken in the order of their ids. An
// edge that is gone or no discovery edge is not changed, as Confirm does
// not change it.
func (r *PGRepository) ConfirmMany(ctx context.Context, orgID string, confirmations []Confirmation) error {
	confidence := make(map[string]*float64, len(confirmations))
	for _, c := range confirmations {
		if _, seen := confidence[c.ID]; seen && c.Confidence == nil {
			continue
		}
		confidence[c.ID] = c.Confidence
	}
	if len(confidence) == 0 {
		return nil
	}
	ids := slices.Sorted(maps.Keys(confidence))
	values := make([]*float64, len(ids))
	for i, id := range ids {
		values[i] = confidence[id]
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ci_relationship r
			SET last_seen_at = now(), last_confirmed_at = now(), confidence = COALESCE(c.confidence, r.confidence)
			FROM unnest($2::uuid[], $3::numeric[]) AS c(id, confidence)
			WHERE r.id = c.id AND r.organization_id = $1 AND r.source = 'discovery'`, orgID, ids, values)
		return err
	})
}
