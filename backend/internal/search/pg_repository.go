package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGRepository struct{ pool *pgxpool.Pool }

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }
func (r *PGRepository) Ping(ctx context.Context) error { return r.pool.Ping(ctx) }
func (r *PGRepository) IndexDocument(ctx context.Context, doc *Document) error {
	return r.IndexDocuments(ctx, []Document{*doc})
}

// IndexDocuments upserts the documents of one organization with one
// statement (NFR-03, WP-333), each like IndexDocument. A document repeated in
// docs is written once, the last one wins: one statement cannot update a row
// twice. The same statement replaces the field rows of the documents
// (search_document_field, WP-266): rows of fields the document no longer
// has are deleted, changed ones updated and new ones inserted, each row by
// one part of the statement; an empty value gets no row. No ON CONFLICT on
// the field rows: it would check the new rows against the read policy,
// whose document the statement's snapshot does not show yet.
func (r *PGRepository) IndexDocuments(ctx context.Context, docs []Document) error {
	if len(docs) == 0 {
		return nil
	}
	orgID := docs[0].OrganizationID
	index := map[[2]string]int{}
	var types, ids, titles, summaries, urls, metas []string
	var fields []map[string]string
	for i := range docs {
		doc := &docs[i]
		if doc.OrganizationID == "" || doc.EntityType == "" || doc.EntityID == "" {
			return fmt.Errorf("organization_id, entity_type and entity_id are required")
		}
		if doc.OrganizationID != orgID {
			return fmt.Errorf("documents of several organizations in one batch")
		}
		meta, err := json.Marshal(doc.Metadata)
		if err != nil {
			return fmt.Errorf("marshal document metadata: %w", err)
		}
		k := [2]string{doc.EntityType, strings.ToLower(doc.EntityID)}
		if j, seen := index[k]; seen {
			titles[j], summaries[j], urls[j], metas[j], fields[j] = doc.Title, doc.Summary, doc.URL, string(meta), doc.Fields
			continue
		}
		index[k] = len(types)
		types, ids = append(types, doc.EntityType), append(ids, doc.EntityID)
		titles, summaries, urls, metas = append(titles, doc.Title), append(summaries, doc.Summary), append(urls, doc.URL), append(metas, string(meta))
		fields = append(fields, doc.Fields)
	}
	fieldDocs, fieldKeys, contents := []string{}, []string{}, []string{}
	for j := range fields {
		for key, content := range fields[j] {
			if content != "" {
				fieldDocs, fieldKeys, contents = append(fieldDocs, strconv.Itoa(j+1)), append(fieldKeys, key), append(contents, content)
			}
		}
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH doc AS (
			INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
			SELECT $1, d.entity_type, d.entity_id::uuid, d.title, d.summary, d.url, d.metadata::jsonb
			FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[])
			     WITH ORDINALITY AS d(entity_type, entity_id, title, summary, url, metadata, ord)
			ORDER BY d.ord
			ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE SET title=EXCLUDED.title, summary=EXCLUDED.summary,
				url=EXCLUDED.url, metadata=EXCLUDED.metadata, updated_at=now()
			RETURNING id, entity_type, entity_id
		), input AS (
			SELECT d.ord, d.entity_type, d.entity_id::uuid AS entity_id FROM unnest($2::text[], $3::text[]) WITH ORDINALITY AS d(entity_type, entity_id, ord)
		), field AS (
			SELECT doc.id AS document_id, f.field, f.content
			FROM unnest($8::bigint[], $9::text[], $10::text[]) AS f(ord, field, content)
			JOIN input ON input.ord = f.ord
			JOIN doc ON doc.entity_type = input.entity_type AND doc.entity_id = input.entity_id
		), stale AS (
			DELETE FROM search_document_field s USING doc
			WHERE s.organization_id = $1 AND s.document_id = doc.id
			  AND NOT EXISTS (SELECT 1 FROM field WHERE field.document_id = s.document_id AND field.field = s.field)
		), changed AS (
			UPDATE search_document_field s SET content = field.content FROM field
			WHERE s.organization_id = $1 AND s.document_id = field.document_id AND s.field = field.field AND s.content <> field.content
		)
		INSERT INTO search_document_field (organization_id, document_id, field, content)
		SELECT $1, field.document_id, field.field, field.content FROM field
		WHERE NOT EXISTS (SELECT 1 FROM search_document_field s WHERE s.document_id = field.document_id AND s.field = field.field)`,
			orgID, types, ids, titles, summaries, urls, metas, fieldDocs, fieldKeys, contents)
		return err
	})
}

func (r *PGRepository) Delete(ctx context.Context, orgID, entityType, entityID string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM search_document WHERE organization_id=$1 AND entity_type=$2 AND entity_id=$3`, orgID, entityType, entityID)
		return err
	})
}

// hitVisible restricts hits to entities that are visible under the
// transaction's tenant scope and still exist. The policy of search_document
// already filters by the client and site derived from the entity (migration
// 000067); the subqueries add the entity's own policy, which covers documents
// and tickets (links, teams) and index rows that are out of date. A
// soft-deleted document (WP-732) is no hit: the policies of document still
// show it, so the subquery checks deleted_at itself.
const hitVisible = `CASE search_document.entity_type
	WHEN 'ci' THEN EXISTS (SELECT 1 FROM ci WHERE ci.id = search_document.entity_id)
	WHEN 'asset' THEN EXISTS (SELECT 1 FROM asset WHERE asset.id = search_document.entity_id)
	WHEN 'contact' THEN EXISTS (SELECT 1 FROM contact WHERE contact.id = search_document.entity_id)
	WHEN 'location' THEN EXISTS (SELECT 1 FROM location WHERE location.id = search_document.entity_id)
	WHEN 'document' THEN EXISTS (SELECT 1 FROM document WHERE document.id = search_document.entity_id AND document.deleted_at IS NULL)
	WHEN 'ticket' THEN EXISTS (SELECT 1 FROM ticket WHERE ticket.id = search_document.entity_id)
	WHEN 'reservation' THEN EXISTS (SELECT 1 FROM reservation WHERE reservation.id = search_document.entity_id)
	ELSE true END`

// contactTokenMatch finds contacts whose vault entry carries every blind
// token of the search text (name, e-mail, phone; AUD-01, SEC-05).
const contactTokenMatch = `(search_document.entity_type = 'contact' AND EXISTS (
	SELECT 1 FROM contact c JOIN pii_subject s ON s.surrogate_id = c.pii_subject_id
	WHERE c.id = search_document.entity_id AND s.search_tokens @> $%d::text[]))`

func buildPostgresQuery(q *Query) (query string, args []any, err error) {
	return buildPostgresQueryWithTokens(q, nil)
}

// buildPostgresQueryWithTokens adds the contacts found by the blind tokens
// of the text to the full-text hits.
func buildPostgresQueryWithTokens(q *Query, tokens []string) (query string, args []any, err error) {
	if verr := validateQuery(q.Text); verr != nil {
		return "", nil, verr
	}
	limit := q.Limit
	if limit <= 0 || limit > api.MaxPageLimit {
		limit = api.DefaultPageLimit
	}
	where := []string{"organization_id = $1", hitVisible}
	args = []any{q.OrganizationID}
	pos := 2
	if strings.TrimSpace(q.Text) != "" {
		match := fmt.Sprintf("search_vector @@ websearch_to_tsquery('simple', $%d)", pos)
		args = append(args, q.Text)
		pos++
		if len(tokens) > 0 {
			match = "(" + match + " OR " + fmt.Sprintf(contactTokenMatch, pos) + ")"
			args = append(args, tokens)
			pos++
		}
		where = append(where, match)
	}
	if len(q.EntityTypes) > 0 {
		where = append(where, fmt.Sprintf("entity_type = ANY($%d)", pos))
		args = append(args, q.EntityTypes)
		pos++
	}
	score := "0.0"
	if strings.TrimSpace(q.Text) != "" {
		score = fmt.Sprintf("ts_rank(search_vector, websearch_to_tsquery('simple', $%d))", 2)
	}
	snippet := "''"
	if q.Highlight && strings.TrimSpace(q.Text) != "" {
		snippet = fmt.Sprintf("ts_headline('simple', title || ' ' || summary, websearch_to_tsquery('simple', $%d), 'StartSel=<mark>, StopSel=</mark>, MaxFragments=2')", 2)
	}
	// The count of the hits is capped (E-104, WP-839): the inner query
	// keeps the best api.TotalCountLimit hits, or those up to the hit after
	// the page when that lies further, and the window counts them. A count
	// beyond api.TotalCap means more hits; offset + page < count still
	// tells whether more follow. search_document is read once (E-133).
	args = append(args, limit, q.Offset)
	query = fmt.Sprintf(`SELECT id::text, organization_id::text, entity_type, entity_id::text, title, summary, url, metadata, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), updated_at, score, %s AS snippet, COUNT(*) OVER() FROM (SELECT id, organization_id, entity_type, entity_id, title, summary, url, metadata, client_id, site_id, updated_at, %s AS score FROM search_document WHERE %s ORDER BY score DESC, updated_at DESC LIMIT GREATEST(%d, $%d::bigint + $%d::bigint + 1)) AS hit ORDER BY score DESC, updated_at DESC LIMIT $%d OFFSET $%d`,
		snippet, score, strings.Join(where, " AND "), api.TotalCountLimit, pos+1, pos, pos, pos+1)
	return query, args, nil
}

// sqlArgs numbers the parameters of a statement in the order they are
// added.
type sqlArgs struct{ args []any }

func (a *sqlArgs) add(v any) int {
	a.args = append(a.args, v)
	return len(a.args)
}

// Expressions of a candidate row of search_document for the field
// permissions (WP-266): the CI type of a ci or asset, read at query time
// (the type chain is resolved in Go), and the team of a ticket, which
// limits the role assignments with a team scope.
const (
	hitTypeID = `COALESCE(CASE search_document.entity_type
		WHEN 'ci' THEN (SELECT ci.ci_type_id::text FROM ci WHERE ci.id = search_document.entity_id)
		WHEN 'asset' THEN (SELECT asset.asset_type_id::text FROM asset WHERE asset.id = search_document.entity_id) END, '')`
	hitTeamID = `COALESCE(CASE search_document.entity_type
		WHEN 'ticket' THEN (SELECT ticket.team_id::text FROM ticket WHERE ticket.id = search_document.entity_id) END, '')`
	hitClientID = `COALESCE(search_document.client_id::text, '')`
	hitSiteID   = `COALESCE(search_document.site_id::text, '')`
)

// restrictedWhere is the condition of the candidates of a search with
// restricted field permissions: the conditions of the phase 1 query, with
// search_vector as the prefilter. A query with a negation or a phrase is
// no monotone condition (a word that only an unreadable field holds would
// exclude the hit, an adjacency could arise between readable fields), so
// it does not prefilter the restricted types. It returns the conditions
// and the parameter of the text (0 without text).
func restrictedWhere(a *sqlArgs, q *Query, tokens, restricted []string) (where []string, text int) {
	where = []string{fmt.Sprintf("organization_id = $%d", a.add(q.OrganizationID)), hitVisible}
	if strings.TrimSpace(q.Text) != "" {
		text = a.add(q.Text)
		match := fmt.Sprintf("search_vector @@ websearch_to_tsquery('simple', $%d)", text)
		if len(tokens) > 0 {
			match += " OR " + fmt.Sprintf(contactTokenMatch, a.add(tokens))
		}
		match += fmt.Sprintf(" OR (entity_type = ANY($%d::text[]) AND websearch_to_tsquery('simple', $%d)::text ~ '[!<]')", a.add(restricted), text)
		where = append(where, "("+match+")")
	}
	if len(q.EntityTypes) > 0 {
		where = append(where, fmt.Sprintf("entity_type = ANY($%d)", a.add(q.EntityTypes)))
	}
	return where, text
}

// fieldOrder orders the field rows of a document like its summary.
func fieldOrder(entityType string) string {
	var b strings.Builder
	b.WriteString("CASE " + entityType)
	for _, t := range fieldTypes {
		b.WriteString(" WHEN '" + t + "' THEN ARRAY['" + strings.Join(SummaryFields[t], "','") + "']")
	}
	b.WriteString(" END")
	return b.String()
}

// fieldScope is the type and scope of candidates for which the policy
// gives the same readable fields.
type fieldScope struct {
	entityType, typeID, clientID, siteID, teamID, objectID string
}

// restrictedQuery builds the query of a search with restricted field
// permissions (RBA-07, WP-266). A ci, asset, document or ticket document of
// a restricted type is a hit only when the text matches its title together
// with the field rows the policy lets the principal read for its type and
// scope (client, site, team of a ticket; object grants by id); summary,
// snippet and score come from them, and the count of the hits from the
// same query. Other documents keep the phase 1 match over search_vector.
// The readable fields are resolved in Go once per type and scope of the
// candidates, which a first statement reads.
func restrictedQuery(ctx context.Context, tx pgx.Tx, q *Query, tokens, restricted []string) (query string, args []any, err error) {
	if verr := validateQuery(q.Text); verr != nil {
		return "", nil, verr
	}
	limit := q.Limit
	if limit <= 0 || limit > api.MaxPageLimit {
		limit = api.DefaultPageLimit
	}
	objectIDs := []string{}
	for _, t := range restricted {
		objectIDs = append(objectIDs, q.Policy.ObjectGrantIDs(permission.ObjectKind(t))...)
	}
	hitObjectID := func(a *sqlArgs) string {
		return fmt.Sprintf("CASE WHEN search_document.entity_id::text = ANY($%d::text[]) THEN search_document.entity_id::text ELSE '' END", a.add(objectIDs))
	}

	var scopes sqlArgs
	where, _ := restrictedWhere(&scopes, q, tokens, restricted)
	scopeSQL := fmt.Sprintf(`SELECT DISTINCT entity_type, %s, %s, %s, %s, %s FROM search_document WHERE %s AND entity_type = ANY($%d::text[])`,
		hitTypeID, hitClientID, hitSiteID, hitTeamID, hitObjectID(&scopes), strings.Join(where, " AND "), scopes.add(restricted))
	rows, err := tx.Query(ctx, scopeSQL, scopes.args...)
	if err != nil {
		return "", nil, fmt.Errorf("read search scopes: %w", err)
	}
	var found []fieldScope
	for rows.Next() {
		var fs fieldScope
		if serr := rows.Scan(&fs.entityType, &fs.typeID, &fs.clientID, &fs.siteID, &fs.teamID, &fs.objectID); serr != nil {
			rows.Close()
			return "", nil, serr
		}
		found = append(found, fs)
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return "", nil, rerr
	}
	cols := make([][]string, 7)
	for i := range found {
		fs := &found[i]
		ref := permission.ObjectRef{ID: fs.objectID, ClientID: fs.clientID, SiteID: fs.siteID, TeamID: fs.teamID}
		for j, v := range []string{fs.entityType, fs.typeID, fs.clientID, fs.siteID, fs.teamID, fs.objectID,
			strings.Join(readableFields(q.Policy, fs.entityType, fs.typeID, &ref), ",")} {
			cols[j] = append(cols[j], v)
		}
	}
	for j := range cols {
		if cols[j] == nil {
			cols[j] = []string{}
		}
	}

	a := &sqlArgs{}
	where, text := restrictedWhere(a, q, tokens, restricted)
	restrictedArg := a.add(restricted)
	allowed := fmt.Sprintf(`SELECT s.fields FROM unnest($%d::text[], $%d::text[], $%d::text[], $%d::text[], $%d::text[], $%d::text[], $%d::text[])
		AS s(entity_type, type_id, client_id, site_id, team_id, object_id, fields)
		WHERE s.entity_type = cand.entity_type AND s.type_id = cand.type_id AND s.client_id = cand.scope_client
		  AND s.site_id = cand.scope_site AND s.team_id = cand.team_id AND s.object_id = cand.object_id`,
		a.add(cols[0]), a.add(cols[1]), a.add(cols[2]), a.add(cols[3]), a.add(cols[4]), a.add(cols[5]), a.add(cols[6]))
	// A candidate without a resolved scope (written after the first
	// statement) shows its title only (fail-closed).
	readSummary := fmt.Sprintf(`CASE WHEN cand.entity_type = ANY($%d::text[]) THEN COALESCE((SELECT string_agg(f.content, ' ' ORDER BY array_position(%s, f.field))
		FROM search_document_field f WHERE f.document_id = cand.id AND f.field = ANY(string_to_array((%s), ','))), '') ELSE cand.summary END`,
		restrictedArg, fieldOrder("cand.entity_type"), allowed)
	candidates := fmt.Sprintf(`SELECT id, organization_id, entity_type, entity_id, title, summary, url, metadata, client_id, site_id, updated_at, search_vector,
		%s AS type_id, %s AS scope_client, %s AS scope_site, %s AS team_id, %s AS object_id FROM search_document WHERE %s`,
		hitTypeID, hitClientID, hitSiteID, hitTeamID, hitObjectID(a), strings.Join(where, " AND "))
	vector := fmt.Sprintf(`SELECT doc.*, CASE WHEN doc.entity_type = ANY($%d::text[]) THEN to_tsvector('simple', doc.title || ' ' || doc.read_summary) ELSE doc.search_vector END AS vec
		FROM (SELECT cand.*, %s AS read_summary FROM (%s) AS cand) AS doc`, restrictedArg, readSummary, candidates)
	score, match, snippet := "0.0", "true", "''"
	if text > 0 {
		tsq := fmt.Sprintf("websearch_to_tsquery('simple', $%d)", text)
		// The other types passed the match of phase 1 in the candidates
		// (contacts also by their blind tokens).
		score, match = "ts_rank(vec, "+tsq+")", fmt.Sprintf("(entity_type <> ALL($%d::text[]) OR vec @@ %s)", restrictedArg, tsq)
		if q.Highlight {
			snippet = "ts_headline('simple', title || ' ' || read_summary, " + tsq + ", 'StartSel=<mark>, StopSel=</mark>, MaxFragments=2')"
		}
	}
	offsetArg, limitArg := a.add(q.Offset), a.add(limit)
	// The count is capped like the phase 1 query (E-104).
	query = fmt.Sprintf(`SELECT id::text, organization_id::text, entity_type, entity_id::text, title, read_summary, url, metadata, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), updated_at, score, %s AS snippet, COUNT(*) OVER() FROM (SELECT id, organization_id, entity_type, entity_id, title, read_summary, url, metadata, client_id, site_id, updated_at, %s AS score FROM (%s) AS v WHERE %s ORDER BY score DESC, updated_at DESC LIMIT GREATEST(%d, $%d::bigint + $%d::bigint + 1)) AS hit ORDER BY score DESC, updated_at DESC LIMIT $%d OFFSET $%d`,
		snippet, score, vector, match, api.TotalCountLimit, limitArg, offsetArg, limitArg, offsetArg)
	return query, a.args, nil
}

func (r *PGRepository) Query(ctx context.Context, q *Query) (Result, error) {
	limit := q.Limit
	if limit <= 0 || limit > api.MaxPageLimit {
		limit = api.DefaultPageLimit
	}
	var res Result
	var counted int
	err := database.WithRequestTenant(ctx, r.pool, q.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var tokens []string
		if vault := pii.Default(); vault != nil {
			var err error
			if tokens, err = vault.QueryTokensTx(ctx, tx, q.OrganizationID, q.Text); err != nil {
				return err
			}
		}
		var sql string
		var args []any
		var err error
		if restricted := q.restrictedTypes(); len(restricted) > 0 {
			sql, args, err = restrictedQuery(ctx, tx, q, tokens, restricted)
		} else {
			sql, args, err = buildPostgresQueryWithTokens(q, tokens)
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var h Hit
			var meta []byte
			var snippet string
			if err := rows.Scan(&h.ID, &h.OrganizationID, &h.EntityType, &h.EntityID, &h.Title, &h.Summary, &h.URL, &meta, &h.ClientID, &h.SiteID, &h.UpdatedAt, &h.Score, &snippet, &counted); err != nil {
				return err
			}
			if err := json.Unmarshal(meta, &h.Metadata); err != nil {
				return fmt.Errorf("unmarshal document metadata for %s: %w", h.ID, err)
			}
			if snippet != "" {
				h.Highlights = []string{snippet}
			}
			res.Data = append(res.Data, h)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		return contactDetails(ctx, tx, q.OrganizationID, res.Data)
	})
	res.Limit, res.Offset = limit, q.Offset
	res.HasMore = q.Offset+len(res.Data) < counted
	res.Total, res.TotalCapped = api.CapTotal(counted)
	return res, err
}

// reindexFields writes the field rows of the ci, asset, document and
// ticket documents of the organization from the columns of their summary
// (WP-266, backfill of migration 000228); an empty value gets no row.
const reindexFields = `INSERT INTO search_document_field (organization_id, document_id, field, content)
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN ci e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.hostname', e.hostname), ('core.manufacturer', e.manufacturer), ('core.model', e.model),
       ('core.serial_number', e.serial_number), ('core.os_name', e.os_name), ('core.os_version', e.os_version)) AS f(field, content)
 WHERE d.organization_id = $1 AND d.entity_type = 'ci' AND f.content <> ''
UNION ALL
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN asset e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.asset_tag', e.asset_tag), ('core.category', e.category), ('core.status', e.status),
       ('core.supplier', e.supplier), ('core.serial_number', e.serial_number), ('core.location', e.location),
       ('core.notes', e.notes)) AS f(field, content)
 WHERE d.organization_id = $1 AND d.entity_type = 'asset' AND f.content <> ''
UNION ALL
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN document e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.description', e.description), ('core.file_name', e.file_name), ('core.category', e.category),
       ('core.tags', array_to_string(e.tags, ' '))) AS f(field, content)
 WHERE d.organization_id = $1 AND d.entity_type = 'document' AND f.content <> ''
UNION ALL
SELECT d.organization_id, d.id, f.field, f.content
  FROM search_document d JOIN ticket e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.description', e.description), ('core.status', e.status), ('core.priority', e.priority),
       ('core.category', e.category),
       ('core.tags', (SELECT string_agg(tg.key, ' ' ORDER BY tg.key) FROM ticket_tag tt JOIN tag tg ON tg.id = tt.tag_id
                       WHERE tt.ticket_id = e.id))) AS f(field, content)
 WHERE d.organization_id = $1 AND d.entity_type = 'ticket' AND f.content <> ''`

// ReindexTenant rebuilds the index of the whole organization. The index must
// stay complete for every principal, so the rebuild runs org-wide even when a
// client-scoped administrator triggers it; it returns only a count (E-08).
// The expressions are the ones of the index triggers: a location links to
// its node in the tree, a contact has no page and no url (migration 000182,
// F-334). The field rows of the old documents go with them (cascade), the
// new ones are written in the same transaction (WP-266).
func (r *PGRepository) ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error) {
	var count int64
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return ReindexResult{}, database.ErrNoTenantScope
	}
	if !strings.EqualFold(scope.OrgID, orgID) {
		return ReindexResult{}, database.ErrTenantMismatch
	}
	orgWide := database.OrgWideScope(scope.OrgID, scope.UserID)
	err := database.WithTenant(ctx, r.pool, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM search_document WHERE organization_id=$1`, orgID); err != nil {
			return err
		}
		cmd, err := tx.Exec(ctx, `INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
SELECT organization_id,'ci',id,name,concat_ws(' ',hostname,manufacturer,model,serial_number,os_name,os_version),'/cmdb/'||id,'{}'::jsonb FROM ci WHERE organization_id=$1 AND deleted_at IS NULL
UNION ALL SELECT organization_id,'asset',id,name,concat_ws(' ',asset_tag,category,status,supplier,serial_number,location,notes),'/assets','{}'::jsonb FROM asset WHERE organization_id=$1
UNION ALL SELECT organization_id,'document',id,title,concat_ws(' ',description,file_name,category,array_to_string(tags,' ')),'/documents','{}'::jsonb FROM document WHERE organization_id=$1 AND deleted_at IS NULL
UNION ALL SELECT organization_id,'ticket',id,title,concat_ws(' ',description,status,priority,category,(SELECT string_agg(tg.key,' ' ORDER BY tg.key) FROM ticket_tag tt JOIN tag tg ON tg.id=tt.tag_id WHERE tt.ticket_id=ticket.id)),'/tickets','{}'::jsonb FROM ticket WHERE organization_id=$1
UNION ALL SELECT organization_id,'contact',id,COALESCE(display_name,''),concat_ws(' ',email,phone,role,department,notes),'','{}'::jsonb FROM contact WHERE organization_id=$1
UNION ALL SELECT organization_id,'location',id,name,kind,'/locations?node='||id,'{}'::jsonb FROM location WHERE organization_id=$1
UNION ALL SELECT organization_id,'reservation',id,COALESCE(project_ref, reason, 'reservation'),concat_ws(' ',state, reason, project_ref),'/reservations','{}'::jsonb FROM reservation WHERE organization_id=$1`, orgID)
		if err != nil {
			return err
		}
		count = cmd.RowsAffected()
		_, err = tx.Exec(ctx, reindexFields, orgID)
		return err
	})
	return ReindexResult{Indexed: int(count)}, err
}

// contactDetails completes contact hits for the contact card of the command
// palette (SRC-01, F-334): contacts have no page of their own, the hit
// carries name (title), e-mail, phone and role (metadata). The index keeps
// no personal data: name, e-mail and phone come from the PII vault (AUD-01,
// SEC-05), or from the contact row when no vault is configured, the role
// from the row. They are read at query time under the caller's tenant
// scope, so a hit shows what GET /contacts/{id} shows (contact:read).
func contactDetails(ctx context.Context, tx pgx.Tx, orgID string, hits []Hit) error {
	var ids []string
	for i := range hits {
		if hits[i].EntityType == "contact" {
			ids = append(ids, hits[i].EntityID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(pii_subject_id::text, ''), COALESCE(email, ''), COALESCE(phone, ''), COALESCE(role, '')
		FROM contact WHERE organization_id = $1 AND id::text = ANY($2)`, orgID, ids)
	if err != nil {
		return fmt.Errorf("contact details: %w", err)
	}
	type details struct{ subject, email, phone, role string }
	byID := map[string]*details{}
	var subjectIDs []string
	for rows.Next() {
		var id string
		var d details
		if err = rows.Scan(&id, &d.subject, &d.email, &d.phone, &d.role); err != nil {
			rows.Close()
			return err
		}
		byID[id] = &d
		if d.subject != "" {
			subjectIDs = append(subjectIDs, d.subject)
		}
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return rerr
	}
	var subjects map[string]*pii.Subject
	if vault := pii.Default(); vault != nil && len(subjectIDs) > 0 {
		if subjects, err = vault.ResolveTx(ctx, tx, orgID, subjectIDs); err != nil {
			return fmt.Errorf("contact details: %w", err)
		}
	}
	for i := range hits {
		d := byID[hits[i].EntityID]
		if hits[i].EntityType != "contact" || d == nil {
			continue
		}
		if s := subjects[d.subject]; d.subject != "" && s != nil {
			hits[i].Title, d.email, d.phone = s.Data["name"], s.Data["email"], s.Data["phone"]
		}
		if hits[i].Metadata == nil {
			hits[i].Metadata = map[string]string{}
		}
		for key, value := range map[string]string{"email": d.email, "phone": d.phone, "role": d.role} {
			if value != "" {
				hits[i].Metadata[key] = value
			}
		}
	}
	return nil
}
