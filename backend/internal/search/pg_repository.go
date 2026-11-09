package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

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
// (search_document_field, WP-266) and their identifier rows
// (search_identifier, WP-308): rows of fields the document no longer has
// are deleted, changed ones updated and new ones inserted, each row by one
// part of the statement; an empty value gets no row. No ON CONFLICT on the
// field rows: it would check the new rows against the read policy, whose
// document the statement's snapshot does not show yet.
//
// Only field rows of source 'indexer' are replaced; an attribute field
// (attributes.<key>) of a CI is written when its definition is searchable
// for the CI type (search_attribute_searchable, migration 000233). The
// identifier fields of Document.Fields (identifierKinds) are no field rows:
// they are written to search_identifier in normal form, the inventory
// number of the linked asset with source_id = ci.asset_id read in the
// statement. A document without that field keeps the row of its current
// asset, which the trigger of asset keeps. network.* rows belong to the
// triggers of the interfaces and addresses.
//
// Users and tags (WP-788): the owner of a CI (core.owner_user_id) comes
// from Document.Fields, the holder of its asset (asset.assigned_user_id,
// source_id = ci.asset_id) is read in the statement, so linking a CI to an
// asset sets it. The field row core.tags of a CI document (source
// 'trigger', search_tag_text of migration 000271) is rebuilt in the same
// statement: the tags of a new CI are written before its document exists,
// so the triggers of ci_tag find no document then.
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
	identDocs, identFields, identKinds, identValues := []string{}, []string{}, []string{}, []string{}
	for j := range fields {
		for key, content := range fields[j] {
			if content == "" {
				continue
			}
			if kind, ok := identifierKinds[key]; ok {
				if value, valid := identifierValue(kind, content); valid && types[j] == "ci" {
					identDocs, identFields = append(identDocs, strconv.Itoa(j+1)), append(identFields, key)
					identKinds, identValues = append(identKinds, kind), append(identValues, value)
				}
				continue
			}
			fieldDocs, fieldKeys, contents = append(fieldDocs, strconv.Itoa(j+1)), append(fieldKeys, key), append(contents, content)
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
			WHERE NOT starts_with(f.field, 'attributes.') OR (input.entity_type = 'ci' AND EXISTS (SELECT 1 FROM ci
				WHERE ci.id = input.entity_id AND search_attribute_searchable($1, ci.ci_type_id, substr(f.field, 12))))
			  AND NOT (f.field = $18 AND input.entity_type IN ('ci', 'asset'))
		), stale AS (
			DELETE FROM search_document_field s USING doc
			WHERE s.organization_id = $1 AND s.document_id = doc.id AND s.source = 'indexer'
			  AND NOT EXISTS (SELECT 1 FROM field WHERE field.document_id = s.document_id AND field.field = s.field)
		), changed AS (
			UPDATE search_document_field s SET content = field.content FROM field
			WHERE s.organization_id = $1 AND s.document_id = field.document_id AND s.field = field.field AND s.source = 'indexer'
			  AND s.content <> field.content
		), fresh AS (
			INSERT INTO search_document_field (organization_id, document_id, field, content)
			SELECT $1, field.document_id, field.field, field.content FROM field
			WHERE NOT EXISTS (SELECT 1 FROM search_document_field s WHERE s.document_id = field.document_id AND s.field = field.field)
		), ident AS (
			SELECT input.entity_type, input.entity_id, i.field,
				CASE WHEN i.field = $16 THEN (SELECT ci.asset_id FROM ci WHERE ci.id = input.entity_id) END AS source_id,
				CASE i.kind WHEN 'mac' THEN search_mac_norm(i.value::macaddr) WHEN 'ip' THEN host(i.value::inet)
					ELSE lower(btrim(i.value)) END AS norm,
				CASE WHEN i.kind = 'ip' THEN host(i.value::inet)::inet END AS addr
			FROM unnest($11::bigint[], $12::text[], $13::text[], $14::text[]) AS i(ord, field, kind, value)
			JOIN input ON input.ord = i.ord
			UNION ALL
			SELECT input.entity_type, input.entity_id, $17::text, ci.asset_id, asset.assigned_user_id::text, NULL::inet
			FROM input JOIN ci ON ci.id = input.entity_id JOIN asset ON asset.id = ci.asset_id
			WHERE input.entity_type = 'ci' AND asset.assigned_user_id IS NOT NULL
		), ident_new AS (
			SELECT * FROM ident WHERE norm <> '' AND (field <> $16 OR source_id IS NOT NULL)
		), ident_stale AS (
			DELETE FROM search_identifier s USING input
			WHERE s.organization_id = $1 AND input.entity_type = 'ci' AND s.entity_type = input.entity_type AND s.entity_id = input.entity_id
			  AND s.field = ANY($15::text[])
			  AND NOT EXISTS (SELECT 1 FROM ident_new n WHERE n.entity_type = s.entity_type AND n.entity_id = s.entity_id
				AND n.field = s.field AND n.source_id IS NOT DISTINCT FROM s.source_id)
			  AND NOT (s.field = $16 AND s.source_id = (SELECT ci.asset_id FROM ci WHERE ci.id = s.entity_id)
				AND NOT EXISTS (SELECT 1 FROM ident i WHERE i.entity_type = s.entity_type AND i.entity_id = s.entity_id AND i.field = s.field))
		), ident_changed AS (
			UPDATE search_identifier s SET norm = n.norm, addr = n.addr FROM ident_new n
			WHERE s.organization_id = $1 AND s.entity_type = n.entity_type AND s.entity_id = n.entity_id AND s.field = n.field
			  AND s.source_id IS NOT DISTINCT FROM n.source_id AND (s.norm <> n.norm OR s.addr IS DISTINCT FROM n.addr)
		), tags AS (
			SELECT doc.id AS document_id, search_tag_text('ci', doc.entity_id) AS content FROM doc WHERE doc.entity_type = 'ci'
		), tags_gone AS (
			DELETE FROM search_document_field s USING tags
			WHERE s.organization_id = $1 AND s.document_id = tags.document_id AND s.field = $18 AND tags.content IS NULL
		), tags_changed AS (
			UPDATE search_document_field s SET content = tags.content, source = 'trigger' FROM tags
			WHERE s.organization_id = $1 AND s.document_id = tags.document_id AND s.field = $18 AND tags.content IS NOT NULL
			  AND (s.content <> tags.content OR s.source <> 'trigger')
		), tags_fresh AS (
			INSERT INTO search_document_field (organization_id, document_id, field, content, source)
			SELECT $1, tags.document_id, $18, tags.content, 'trigger' FROM tags
			WHERE tags.content IS NOT NULL
			  AND NOT EXISTS (SELECT 1 FROM search_document_field s WHERE s.document_id = tags.document_id AND s.field = $18)
		)
		INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, source_id, norm, addr)
		SELECT $1, n.entity_type, n.entity_id, n.field, n.source_id, n.norm, n.addr FROM ident_new n
		WHERE NOT EXISTS (SELECT 1 FROM search_identifier s WHERE s.organization_id = $1 AND s.entity_type = n.entity_type
			AND s.entity_id = n.entity_id AND s.field = n.field AND s.source_id IS NOT DISTINCT FROM n.source_id)
		ON CONFLICT (source_id, field, entity_id) WHERE source_id IS NOT NULL DO NOTHING`,
			orgID, types, ids, titles, summaries, urls, metas, fieldDocs, fieldKeys, contents,
			identDocs, identFields, identKinds, identValues, indexerIdentifiers, IdentifierAssetInventory, UserFieldAssetHolder, tagField)
		return err
	})
}

// identifierValue checks the value of an identifier field of the kind for
// the statement of IndexDocuments, which normalizes it like the triggers: a
// MAC in a macaddr input format, an IP address (with or without prefix
// length); any text otherwise. An invalid value is left out.
func identifierValue(kind, value string) (string, bool) {
	switch kind {
	case identifierMAC:
		return NormalizeMAC(value)
	case identifierUser:
		return strings.ToLower(value), userIDPattern.MatchString(value)
	case identifierIP:
		if addr, err := netip.ParseAddr(value); err == nil {
			return value, addr.Zone() == ""
		}
		if _, err := netip.ParsePrefix(value); err == nil {
			return value, true
		}
		return "", false
	}
	return value, true
}

// userIDPattern is the text form of a user id (app_user.id).
var userIDPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)

// Delete removes the document with its field rows (cascade) and its
// identifier rows, except network.* of the interfaces, which their
// triggers keep (WP-308).
func (r *PGRepository) Delete(ctx context.Context, orgID, entityType, entityID string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH ident AS (
			DELETE FROM search_identifier WHERE organization_id=$1 AND entity_type=$2 AND entity_id=$3 AND NOT starts_with(field, 'network.')
		)
		DELETE FROM search_document WHERE organization_id=$1 AND entity_type=$2 AND entity_id=$3`, orgID, entityType, entityID)
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

// attributeMatch finds ci and asset documents whose custom field rows
// (attributes.<key>, WP-308) or tag row (core.tags, WP-788) match the text
// of parameter textArg. The field rows have no index on vector (K22). The
// matching rows are read once per query (an uncorrelated IN, a hashed
// subplan): a correlated EXISTS read the field rows of every ci and asset
// document of the organization by document, one index probe each, while
// the match is evaluated for every row of search_document.
func attributeMatch(textArg int) string {
	return fmt.Sprintf(`(search_document.entity_type IN ('ci', 'asset') AND search_document.id IN (SELECT f.document_id FROM search_document_field f
		WHERE (starts_with(f.field, 'attributes.') OR f.field = '%s')
		  AND f.vector @@ websearch_to_tsquery('simple', $%d)))`, tagField, textArg)
}

// userIn finds the documents with a user field of the fields that holds
// one of the users the text resolved to (SRC-02, WP-788): equality on norm,
// served by idx_search_identifier_norm under row level security.
func userIn(a *sqlArgs, orgArg int, fields, users []string) string {
	return fmt.Sprintf(`(search_document.entity_type, search_document.entity_id) IN (SELECT si.entity_type, si.entity_id
		FROM search_identifier si WHERE si.organization_id = $%d AND si.field = ANY($%d::text[]) AND si.norm = ANY($%d::text[]))`,
		orgArg, a.add(fields), a.add(users))
}

// userMatch adds the user fields (openUserFields) to the match of a query
// when the text resolved to users.
func userMatch(a *sqlArgs, orgArg int, q *Query, restricted []string) string {
	if len(q.userIDs) == 0 {
		return ""
	}
	fields := openUserFields(q, restricted)
	if len(fields) == 0 {
		return ""
	}
	return " OR " + userIn(a, orgArg, fields, q.userIDs)
}

// identifierCond is the condition of the identifier conditions on the row
// si of search_identifier: equality on norm or addr, a range of addr, a
// prefix of norm (B-tree operators that are leakproof, so the indexes
// idx_search_identifier_norm and idx_search_identifier_addr serve them under
// row level security; SR-04, E-133).
func identifierCond(a *sqlArgs, ids *Identifiers) string {
	prefix := func(fieldCond, low, high string) string {
		cond := fmt.Sprintf("(%s AND si.norm ~>=~ $%d", fieldCond, a.add(low))
		if high != "" {
			cond += fmt.Sprintf(" AND si.norm ~<~ $%d", a.add(high))
		}
		return cond + ")"
	}
	switch {
	case ids.MAC != "":
		return fmt.Sprintf("si.norm = $%d", a.add(ids.MAC))
	case ids.Addr != "":
		return fmt.Sprintf("si.addr = $%d::inet", a.add(ids.Addr))
	case ids.Low != "":
		return fmt.Sprintf("(si.addr >= $%d::inet AND si.addr <= $%d::inet)", a.add(ids.Low), a.add(ids.High))
	}
	macs := a.add(macFields)
	cond := prefix(fmt.Sprintf("si.field <> ALL($%d::text[])", macs), ids.Prefix, ids.PrefixEnd)
	if ids.MACPrefix != "" {
		cond = "(" + cond + " OR " + prefix(fmt.Sprintf("si.field = ANY($%d::text[])", macs), ids.MACPrefix, ids.MACPrefixEnd) + ")"
	}
	return cond
}

// identifierIn finds the documents with an identifier of the fields that
// meets the identifier conditions (SRC-02, WP-308). search_identifier is
// read in the organization of parameter orgArg; client and site come from
// the policy of search_document.
func identifierIn(a *sqlArgs, orgArg int, fields []string, ids *Identifiers) string {
	return fmt.Sprintf(`(search_document.entity_type, search_document.entity_id) IN (SELECT si.entity_type, si.entity_id
		FROM search_identifier si WHERE si.organization_id = $%d AND si.field = ANY($%d::text[]) AND %s)`,
		orgArg, a.add(fields), identifierCond(a, ids))
}

// openIdentifierFields are the identifier fields whose rows count for a hit
// without a check per document: the fields of the queried entity types,
// network.* only with Query.NetworkReadable, the inventory number of the
// asset at a CI only when the policy does not restrict assets. A type in
// restricted keeps all its fields: the restricted query checks them per
// document.
func openIdentifierFields(q *Query, restricted []string) []string {
	out := []string{}
	for _, t := range fieldTypes {
		if len(q.EntityTypes) > 0 && !slices.Contains(q.EntityTypes, t) {
			continue
		}
		for _, f := range identifierFields[t] {
			switch {
			case strings.HasPrefix(f, "network.") && !q.NetworkReadable:
			case f == IdentifierAssetInventory && !slices.Contains(restricted, t) && q.Policy != nil && q.Policy.Restricted(permission.KindAsset):
			default:
				out = append(out, f)
			}
		}
	}
	return out
}

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
			match += " OR " + fmt.Sprintf(contactTokenMatch, pos)
			args = append(args, tokens)
		}
		// Custom fields and identifiers (SRC-02, WP-308).
		a := &sqlArgs{args: args}
		match += " OR " + attributeMatch(2)
		if ids, ok := ParseIdentifiers(q.Text); ok {
			if fields := openIdentifierFields(q, nil); len(fields) > 0 {
				match += " OR " + identifierIn(a, 1, fields, &ids)
			}
		}
		match += userMatch(a, 1, q, nil)
		args, pos = a.args, len(a.args)+1
		where = append(where, "("+match+")")
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
	query = fmt.Sprintf(`SELECT id::text, organization_id::text, entity_type, entity_id::text, title, summary, url, metadata, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), updated_at, score, %s AS snippet, NULL::text AS read_fields, COUNT(*) OVER() FROM (SELECT id, organization_id, entity_type, entity_id, title, summary, url, metadata, client_id, site_id, updated_at, %s AS score FROM search_document WHERE %s ORDER BY score DESC, updated_at DESC LIMIT GREATEST(%d, $%d::bigint + $%d::bigint + 1)) AS hit ORDER BY score DESC, updated_at DESC LIMIT $%d OFFSET $%d`,
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

// hitAssetRef is the reference of the asset linked to a ci candidate for
// the field permission of its inventory number (asset.inventory_no, WP-308):
// "<id>|<asset type>|<client>|<site>" with the id only when an object grant
// names it (parameter of the asset object grants), "" without a visible
// asset.
func hitAssetRef(a *sqlArgs, assetGrants []string) string {
	return fmt.Sprintf(`COALESCE(CASE search_document.entity_type WHEN 'ci' THEN (SELECT concat_ws('|',
		CASE WHEN asset.id::text = ANY($%d::text[]) THEN asset.id::text ELSE '' END, COALESCE(asset.asset_type_id::text, ''),
		COALESCE(asset.client_id::text, ''), COALESCE(asset.site_id::text, ''))
		FROM ci JOIN asset ON asset.id = ci.asset_id WHERE ci.id = search_document.entity_id) END, '')`, a.add(assetGrants))
}

// restrictedWhere is the condition of the candidates of a search with
// restricted field permissions: the conditions of the phase 1 query, with
// search_vector as the prefilter. A query with a negation is no monotone
// condition (a word that only an unreadable field holds would exclude the
// hit), so it does not prefilter the restricted types. A phrase is none
// either (an adjacency could arise between readable fields once an
// unreadable one between them is left out), but it implies its words: a
// restricted type is prefiltered with the phrase operators of the query
// turned into AND (relaxedPhrase). The words of title and readable fields
// are words of search_vector (title and summary, the summary being the
// summary fields), so this drops no hit; the hit still has to match its
// readable text (restrictedQuery). Without the prefilter every document of
// a restricted type went through the readable text of restrictedQuery for
// a hyphenated name such as srv-05001 ('srv' <-> '-05001'). The custom
// field rows and the identifiers (WP-308) prefilter with all fields of a
// restricted type and with the open ones of the others
// (openIdentifierFields). It returns the conditions, the parameter of the
// organization and the one of the text (0 without text).
func restrictedWhere(a *sqlArgs, q *Query, tokens, restricted []string) (where []string, org, text int) {
	org = a.add(q.OrganizationID)
	where = []string{fmt.Sprintf("organization_id = $%d", org), hitVisible}
	if strings.TrimSpace(q.Text) != "" {
		text = a.add(q.Text)
		match := fmt.Sprintf("search_vector @@ websearch_to_tsquery('simple', $%d)", text)
		if len(tokens) > 0 {
			match += " OR " + fmt.Sprintf(contactTokenMatch, a.add(tokens))
		}
		match += fmt.Sprintf(" OR (entity_type = ANY($%d::text[]) AND (websearch_to_tsquery('simple', $%d)::text ~ '!' OR search_vector @@ %s))",
			a.add(restricted), text, relaxedPhrase(text))
		match += " OR " + attributeMatch(text)
		if ids, ok := ParseIdentifiers(q.Text); ok {
			if fields := openIdentifierFields(q, restricted); len(fields) > 0 {
				match += " OR " + identifierIn(a, org, fields, &ids)
			}
		}
		match += userMatch(a, org, q, restricted)
		where = append(where, "("+match+")")
	}
	if len(q.EntityTypes) > 0 {
		where = append(where, fmt.Sprintf("entity_type = ANY($%d)", a.add(q.EntityTypes)))
	}
	return where, org, text
}

// relaxedPhrase is the query of parameter textArg with every phrase
// operator (<-> and <N>) turned into AND: it matches every document the
// query matches, for a query without negation (restrictedWhere). The text
// form of a tsquery quotes each lexeme, and a lexeme holds no space, so
// only the operators between lexemes have the form " <...> "; <-> binds
// tighter than &, which binds tighter than |, and the text form sets
// parentheses where a phrase holds & or |, so the replacement keeps the
// structure of the query.
func relaxedPhrase(textArg int) string {
	return fmt.Sprintf(`regexp_replace(websearch_to_tsquery('simple', $%d)::text, ' <(-|[0-9]+)> ', ' & ', 'g')::tsquery`, textArg)
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
	// assetRef is hitAssetRef of a ci candidate; attributes are the custom
	// field keys (attributes.<key>) of the candidates of the scope.
	assetRef   string
	attributes []string
}

// readable returns the fields of a scope whose rows count for a hit: the
// readable summary fields, identifier fields and custom fields (WP-266,
// WP-308). A custom field key with a comma is left out: the list is joined
// by commas (fail-closed).
func (fs *fieldScope) readable(q *Query) []string {
	ref := permission.ObjectRef{ID: fs.objectID, ClientID: fs.clientID, SiteID: fs.siteID, TeamID: fs.teamID}
	out := readableFields(q.Policy, fs.entityType, fs.typeID, &ref)
	out = append(out, readableIdentifiers(q, fs.entityType, fs.typeID, &ref, fs.assetRef)...)
	out = append(out, readableUsers(q, fs.entityType, fs.typeID, &ref, fs.assetRef)...)
	if tagsReadable(q, fs.entityType, fs.typeID, &ref) {
		out = append(out, tagField)
	}
	kind := permission.ObjectKind(fs.entityType)
	for _, key := range fs.attributes {
		if !strings.Contains(key, ",") && q.Policy.Level(kind, fs.typeID, key, ref).Readable() {
			out = append(out, key)
		}
	}
	return out
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

	assetGrants := q.Policy.ObjectGrantIDs(permission.KindAsset)
	if assetGrants == nil {
		assetGrants = []string{}
	}

	var scopes sqlArgs
	where, _, _ := restrictedWhere(&scopes, q, tokens, restricted)
	scopeSQL := fmt.Sprintf(`SELECT c.entity_type, c.type_id, c.scope_client, c.scope_site, c.team_id, c.object_id, c.asset_ref,
		COALESCE(array_agg(DISTINCT f.field) FILTER (WHERE f.field IS NOT NULL), '{}')
		FROM (SELECT id, entity_type, %s AS type_id, %s AS scope_client, %s AS scope_site, %s AS team_id, %s AS object_id, %s AS asset_ref
			FROM search_document WHERE %s AND entity_type = ANY($%d::text[])) AS c
		LEFT JOIN search_document_field f ON f.document_id = c.id AND starts_with(f.field, 'attributes.')
		GROUP BY 1, 2, 3, 4, 5, 6, 7`,
		hitTypeID, hitClientID, hitSiteID, hitTeamID, hitObjectID(&scopes), hitAssetRef(&scopes, assetGrants), strings.Join(where, " AND "), scopes.add(restricted))
	rows, err := tx.Query(ctx, scopeSQL, scopes.args...)
	if err != nil {
		return "", nil, fmt.Errorf("read search scopes: %w", err)
	}
	var found []fieldScope
	for rows.Next() {
		var fs fieldScope
		if serr := rows.Scan(&fs.entityType, &fs.typeID, &fs.clientID, &fs.siteID, &fs.teamID, &fs.objectID, &fs.assetRef, &fs.attributes); serr != nil {
			rows.Close()
			return "", nil, serr
		}
		found = append(found, fs)
	}
	rows.Close()
	if rerr := rows.Err(); rerr != nil {
		return "", nil, rerr
	}
	cols := make([][]string, 8)
	for i := range found {
		fs := &found[i]
		for j, v := range []string{fs.entityType, fs.typeID, fs.clientID, fs.siteID, fs.teamID, fs.objectID, fs.assetRef,
			strings.Join(fs.readable(q), ",")} {
			cols[j] = append(cols[j], v)
		}
	}
	for j := range cols {
		if cols[j] == nil {
			cols[j] = []string{}
		}
	}

	a := &sqlArgs{}
	where, org, text := restrictedWhere(a, q, tokens, restricted)
	restrictedArg := a.add(restricted)
	allowed := fmt.Sprintf(`SELECT s.fields FROM unnest($%d::text[], $%d::text[], $%d::text[], $%d::text[], $%d::text[], $%d::text[], $%d::text[], $%d::text[])
		AS s(entity_type, type_id, client_id, site_id, team_id, object_id, asset_ref, fields)
		WHERE s.entity_type = cand.entity_type AND s.type_id = cand.type_id AND s.client_id = cand.scope_client
		  AND s.site_id = cand.scope_site AND s.team_id = cand.team_id AND s.object_id = cand.object_id AND s.asset_ref = cand.asset_ref`,
		a.add(cols[0]), a.add(cols[1]), a.add(cols[2]), a.add(cols[3]), a.add(cols[4]), a.add(cols[5]), a.add(cols[6]), a.add(cols[7]))
	// A candidate without a resolved scope (written after the first
	// statement) shows its title only (fail-closed). The summary is built
	// from the readable summary fields; custom fields and identifiers only
	// match.
	readSummary := fmt.Sprintf(`CASE WHEN x.entity_type = ANY($%d::text[]) THEN COALESCE((SELECT string_agg(f.content, ' ' ORDER BY array_position(%s, f.field))
		FROM search_document_field f WHERE f.document_id = x.id AND f.field = ANY(string_to_array(x.read_fields, ','))
		  AND array_position(%s, f.field) IS NOT NULL), '') ELSE x.summary END`,
		restrictedArg, fieldOrder("x.entity_type"), fieldOrder("x.entity_type"))
	candidates := fmt.Sprintf(`SELECT id, organization_id, entity_type, entity_id, title, summary, url, metadata, client_id, site_id, updated_at, search_vector,
		%s AS type_id, %s AS scope_client, %s AS scope_site, %s AS team_id, %s AS object_id, %s AS asset_ref FROM search_document WHERE %s`,
		hitTypeID, hitClientID, hitSiteID, hitTeamID, hitObjectID(a), hitAssetRef(a, assetGrants), strings.Join(where, " AND "))
	vector := fmt.Sprintf(`SELECT doc.*, CASE WHEN doc.entity_type = ANY($%d::text[]) THEN to_tsvector('simple', doc.title || ' ' || doc.read_summary) ELSE doc.search_vector END AS vec
		FROM (SELECT x.*, %s AS read_summary FROM (SELECT cand.*, COALESCE((%s), '') AS read_fields FROM (%s) AS cand) AS x) AS doc`,
		restrictedArg, readSummary, allowed, candidates)
	score, match, snippet := "0.0", "true", "''"
	if text > 0 {
		tsq := fmt.Sprintf("websearch_to_tsquery('simple', $%d)", text)
		// The other types passed the match of phase 1 in the candidates
		// (contacts also by their blind tokens). A restricted type also
		// matches by its readable custom fields and identifiers (WP-308).
		readable := fmt.Sprintf(`EXISTS (SELECT 1 FROM search_document_field f WHERE f.document_id = v.id
			AND (starts_with(f.field, 'attributes.') OR f.field = '%s')
			AND f.field = ANY(string_to_array(v.read_fields, ',')) AND f.vector @@ %s)`, tagField, tsq)
		if ids, ok := ParseIdentifiers(q.Text); ok {
			readable += fmt.Sprintf(` OR EXISTS (SELECT 1 FROM search_identifier si WHERE si.organization_id = $%d AND si.entity_type = v.entity_type
				AND si.entity_id = v.entity_id AND si.field = ANY(string_to_array(v.read_fields, ',')) AND %s)`, org, identifierCond(a, &ids))
		}
		// The users the text resolved to, in the readable user fields
		// (WP-788).
		if len(q.userIDs) > 0 {
			readable += fmt.Sprintf(` OR EXISTS (SELECT 1 FROM search_identifier si WHERE si.organization_id = $%d AND si.entity_type = v.entity_type
				AND si.entity_id = v.entity_id AND si.field = ANY(string_to_array(v.read_fields, ',')) AND si.field = ANY($%d::text[])
				AND si.norm = ANY($%d::text[]))`, org, a.add(allUserFields), a.add(q.userIDs))
		}
		score, match = "ts_rank(vec, "+tsq+")", fmt.Sprintf("(entity_type <> ALL($%d::text[]) OR vec @@ %s OR %s)", restrictedArg, tsq, readable)
		if q.Highlight {
			snippet = "ts_headline('simple', title || ' ' || read_summary, " + tsq + ", 'StartSel=<mark>, StopSel=</mark>, MaxFragments=2')"
		}
	}
	offsetArg, limitArg := a.add(q.Offset), a.add(limit)
	// The count is capped like the phase 1 query (E-104).
	// read_fields are the readable fields of a hit of a restricted type,
	// for its matched fields (WP-786); NULL for the other types.
	query = fmt.Sprintf(`SELECT id::text, organization_id::text, entity_type, entity_id::text, title, read_summary, url, metadata, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), updated_at, score, %s AS snippet, read_fields, COUNT(*) OVER() FROM (SELECT id, organization_id, entity_type, entity_id, title, read_summary, url, metadata, client_id, site_id, updated_at, %s AS score, CASE WHEN entity_type = ANY($%d::text[]) THEN read_fields END AS read_fields FROM (%s) AS v WHERE %s ORDER BY score DESC, updated_at DESC LIMIT GREATEST(%d, $%d::bigint + $%d::bigint + 1)) AS hit ORDER BY score DESC, updated_at DESC LIMIT $%d OFFSET $%d`,
		snippet, score, restrictedArg, vector, match, api.TotalCountLimit, limitArg, offsetArg, limitArg, offsetArg)
	return query, a.args, nil
}

// resolveUsersSQL resolves a search text to the active users of the
// organization whose name or e-mail address it matches (SRC-02, WP-788):
// the e-mail address as a whole or the words of the text in name and
// e-mail address. A text with a negation resolves to none: it would match
// every user without the word.
const resolveUsersSQL = `SELECT u.id::text FROM app_user u
	WHERE u.organization_id = $1 AND u.is_active AND websearch_to_tsquery('simple', $2)::text !~ '!'
	  AND (lower(u.email) = lower(btrim($2))
	       OR to_tsvector('simple', u.display_name || ' ' || u.email) @@ websearch_to_tsquery('simple', $2))
	ORDER BY u.id LIMIT $3`

// resolveUsersTx returns the users the search text resolves to, at most
// maxResolvedUsers, or none when the user fields do not count for the
// query (Query.UsersReadable) or there is no text. Names and e-mail
// addresses are read here only; the index holds the ids.
func resolveUsersTx(ctx context.Context, tx pgx.Tx, q *Query) ([]string, error) {
	if !q.UsersReadable || strings.TrimSpace(q.Text) == "" {
		return nil, nil
	}
	rows, err := tx.Query(ctx, resolveUsersSQL, q.OrganizationID, q.Text, maxResolvedUsers)
	if err != nil {
		return nil, fmt.Errorf("resolve search users: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("resolve search users: %w", err)
	}
	return ids, nil
}

// ResolveUsers resolves the search text of the query to users under the
// tenant scope of the request, like Query does (WP-788); HybridBackend
// searches the remote index with them.
func (r *PGRepository) ResolveUsers(ctx context.Context, q *Query) ([]string, error) {
	if !q.UsersReadable || strings.TrimSpace(q.Text) == "" {
		return nil, nil
	}
	var ids []string
	err := database.WithRequestTenant(ctx, r.pool, q.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		ids, err = resolveUsersTx(ctx, tx, q)
		return err
	})
	return ids, err
}

// customPlanSQL keeps the search transaction on custom plans. pgx prepares
// every statement, and after five executions on a connection PostgreSQL
// may switch to the generic plan, in which websearch_to_tsquery of the
// text parameter is no constant: it is computed again for every document
// and field row it is compared with instead of once at planning. The
// statements of a search are planned anew each time (a few ms) instead.
const customPlanSQL = `SET LOCAL plan_cache_mode = force_custom_plan`

func (r *PGRepository) Query(ctx context.Context, in *Query) (Result, error) {
	// The users of the text are resolved per query on a copy (WP-788).
	query := *in
	q := &query
	limit := q.Limit
	if limit <= 0 || limit > api.MaxPageLimit {
		limit = api.DefaultPageLimit
	}
	var res Result
	var counted int
	err := database.WithRequestTenant(ctx, r.pool, q.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		// The statements of the search are planned with their parameters
		// (customPlanSQL).
		if _, err := tx.Exec(ctx, customPlanSQL); err != nil {
			return err
		}
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
		if q.userIDs, err = resolveUsersTx(ctx, tx, q); err != nil {
			return err
		}
		restricted := q.restrictedTypes()
		if len(restricted) > 0 {
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
		var readFields []*string
		for rows.Next() {
			var h Hit
			var meta []byte
			var snippet string
			var read *string
			if err := rows.Scan(&h.ID, &h.OrganizationID, &h.EntityType, &h.EntityID, &h.Title, &h.Summary, &h.URL, &meta, &h.ClientID, &h.SiteID, &h.UpdatedAt, &h.Score, &snippet, &read, &counted); err != nil {
				return err
			}
			readFields = append(readFields, read)
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
		rows.Close()
		if q.Highlight && strings.TrimSpace(q.Text) != "" {
			if err := matchedFields(ctx, tx, q, restricted, res.Data, readFields); err != nil {
				return err
			}
		}
		return contactDetails(ctx, tx, q.OrganizationID, res.Data)
	})
	res.Limit, res.Offset = limit, q.Offset
	res.HasMore = q.Offset+len(res.Data) < counted
	res.Total, res.TotalCapped = api.CapTotal(counted)
	return res, err
}

// headlineOptions are the options of the ts_headline fragments of the
// matched fields, the ones of Hit.Highlights.
const headlineOptions = `'StartSel=<mark>, StopSel=</mark>, MaxFragments=2'`

// matchedFields sets the matched fields of the hits of a query with text
// (SRC-02, WP-786): the title, the summary of a type without field rows,
// the field rows (core.<column>, attributes.<key>) with their ts_headline
// fragment, and the identifiers that meet the identifier conditions of the
// text with their value in canonical form. A text field counts when its
// own text matches the query or when the fragment marks a word of it, so a
// hit matched by several fields together names each of them. read holds
// the readable fields of a hit of a restricted type (restrictedQuery): only
// those and the title count (RBA-07, WP-266); the identifiers of the other
// hits are those that count for a hit (openIdentifierFields). A contact
// hit names no field: its name, e-mail and phone are personal data of the
// PII vault (AUD-01).
func matchedFields(ctx context.Context, tx pgx.Tx, q *Query, restricted []string, hits []Hit, read []*string) error {
	var at []int
	var docIDs, types, entityIDs, identAllowed, usersAllowed []string
	var allowed []*string
	open := openIdentifierFields(q, restricted)
	openUsers := openUserFields(q, restricted)
	for i := range hits {
		h := &hits[i]
		if h.EntityType == "contact" {
			continue
		}
		var readable []string
		if read[i] != nil {
			readable = strings.Split(*read[i], ",")
		}
		var idents []string
		for _, f := range identifierFields[h.EntityType] {
			if (read[i] != nil && slices.Contains(readable, f)) || (read[i] == nil && slices.Contains(open, f)) {
				idents = append(idents, f)
			}
		}
		var users []string
		for _, f := range userFields[h.EntityType] {
			if (read[i] != nil && slices.Contains(readable, f)) || (read[i] == nil && slices.Contains(openUsers, f)) {
				users = append(users, f)
			}
		}
		at = append(at, i)
		docIDs, types, entityIDs = append(docIDs, h.ID), append(types, h.EntityType), append(entityIDs, h.EntityID)
		allowed, identAllowed = append(allowed, read[i]), append(identAllowed, strings.Join(idents, ","))
		usersAllowed = append(usersAllowed, strings.Join(users, ","))
	}
	if len(at) == 0 {
		return nil
	}
	found := make([][]MatchedField, len(hits))
	a := &sqlArgs{}
	ids, typ, allow := a.add(docIDs), a.add(types), a.add(allowed)
	tsq := fmt.Sprintf("websearch_to_tsquery('simple', $%d)", a.add(q.Text))
	fieldTypesArg := a.add(fieldTypes)
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT h.ord, m.field, m.hl
		FROM unnest($%[1]d::uuid[], $%[2]d::text[], $%[3]d::text[]) WITH ORDINALITY AS h(id, entity_type, allowed, ord)
		CROSS JOIN LATERAL (
			SELECT 'title' AS field, d.title AS content, ts_headline('simple', d.title, %[4]s, %[5]s) AS hl,
				to_tsvector('simple', d.title) @@ %[4]s AS matched
			FROM search_document d WHERE d.id = h.id
			UNION ALL
			SELECT 'summary', d.summary, ts_headline('simple', d.summary, %[4]s, %[5]s), to_tsvector('simple', d.summary) @@ %[4]s
			FROM search_document d WHERE d.id = h.id AND h.entity_type <> ALL($%[6]d::text[])
			UNION ALL
			SELECT f.field, f.content, ts_headline('simple', f.content, %[4]s, %[5]s), f.vector @@ %[4]s
			FROM search_document_field f
			WHERE f.document_id = h.id AND (h.allowed IS NULL OR f.field = ANY(string_to_array(h.allowed, ',')))
		) AS m
		WHERE m.matched OR (strpos(m.hl, '<mark>') > 0 AND strpos(m.content, '<mark>') = 0)`,
		ids, typ, allow, tsq, headlineOptions, fieldTypesArg), a.args...)
	if err != nil {
		return fmt.Errorf("matched fields: %w", err)
	}
	for rows.Next() {
		var ord int
		var m MatchedField
		if err = rows.Scan(&ord, &m.Field, &m.Highlight); err != nil {
			rows.Close()
			return err
		}
		found[at[ord-1]] = append(found[at[ord-1]], m)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return fmt.Errorf("matched fields: %w", err)
	}
	if conds, ok := ParseIdentifiers(q.Text); ok {
		b := &sqlArgs{}
		org, typArg, eid, fl := b.add(q.OrganizationID), b.add(types), b.add(entityIDs), b.add(identAllowed)
		cond := identifierCond(b, &conds)
		rows, err = tx.Query(ctx, fmt.Sprintf(`SELECT DISTINCT h.ord, si.field, si.norm
			FROM unnest($%d::text[], $%d::uuid[], $%d::text[]) WITH ORDINALITY AS h(entity_type, entity_id, fields, ord)
			JOIN search_identifier si ON si.organization_id = $%d AND si.entity_type = h.entity_type AND si.entity_id = h.entity_id
				AND si.field = ANY(string_to_array(h.fields, ','))
			WHERE %s`, typArg, eid, fl, org, cond), b.args...)
		if err != nil {
			return fmt.Errorf("matched identifiers: %w", err)
		}
		for rows.Next() {
			var ord int
			var m MatchedField
			if err = rows.Scan(&ord, &m.Field, &m.Highlight); err != nil {
				rows.Close()
				return err
			}
			if slices.Contains(macFields, m.Field) {
				m.Highlight = canonicalMAC(m.Highlight)
			}
			found[at[ord-1]] = append(found[at[ord-1]], m)
		}
		rows.Close()
		if err = rows.Err(); err != nil {
			return fmt.Errorf("matched identifiers: %w", err)
		}
	}
	if len(q.userIDs) > 0 {
		if uerr := matchedUsers(ctx, tx, q, types, entityIDs, usersAllowed, func(ord int, m MatchedField) {
			found[at[ord-1]] = append(found[at[ord-1]], m)
		}); uerr != nil {
			return uerr
		}
	}
	for i := range hits {
		if len(found[i]) == 0 {
			continue
		}
		sortMatchedFields(hits[i].EntityType, found[i])
		hits[i].MatchedFields = found[i]
	}
	return nil
}

// matchedUsers reports the user fields of the hits (ordinal from 1) that
// hold a user the text resolved to (WP-788), of the fields per hit
// (comma-separated). The field carries no highlight: the index holds the
// id only, and the name or e-mail address is no field of the hit.
func matchedUsers(ctx context.Context, tx pgx.Tx, q *Query, types, entityIDs, fields []string, add func(ord int, m MatchedField)) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT h.ord, si.field
		FROM unnest($2::text[], $3::uuid[], $4::text[]) WITH ORDINALITY AS h(entity_type, entity_id, fields, ord)
		JOIN search_identifier si ON si.organization_id = $1 AND si.entity_type = h.entity_type AND si.entity_id = h.entity_id
			AND si.field = ANY(string_to_array(h.fields, ','))
		WHERE si.norm = ANY($5::text[])`, q.OrganizationID, types, entityIDs, fields, q.userIDs)
	if err != nil {
		return fmt.Errorf("matched users: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ord int
		var m MatchedField
		if err := rows.Scan(&ord, &m.Field); err != nil {
			return err
		}
		add(ord, m)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("matched users: %w", err)
	}
	return nil
}

// sortMatchedFields orders the matched fields of a hit by fieldRank, then
// by field and highlight.
func sortMatchedFields(entityType string, fields []MatchedField) {
	slices.SortStableFunc(fields, func(x, y MatchedField) int {
		if c := fieldRank(entityType, x.Field) - fieldRank(entityType, y.Field); c != 0 {
			return c
		}
		return strings.Compare(x.Field+"\x00"+x.Highlight, y.Field+"\x00"+y.Highlight)
	})
}

// fieldRank orders the matched fields of a hit: the title, the summary,
// the summary fields in summary order, other columns, the custom fields,
// the identifiers in the order of their fields.
func fieldRank(entityType, field string) int {
	switch field {
	case "title":
		return 0
	case "summary":
		return 1
	}
	if i := slices.Index(SummaryFields[entityType], field); i >= 0 {
		return 10 + i
	}
	if i := slices.Index(identifierFields[entityType], field); i >= 0 {
		return 300 + i
	}
	if i := slices.Index(userFields[entityType], field); i >= 0 {
		return 400 + i
	}
	if strings.HasPrefix(field, "attributes.") {
		return 200
	}
	return 100
}

// canonicalMAC writes a MAC in normal form (twelve hex digits) with colons.
func canonicalMAC(norm string) string {
	if len(norm) != 12 {
		return norm
	}
	parts := make([]string, 0, 6)
	for i := 0; i < 12; i += 2 {
		parts = append(parts, norm[i:i+2])
	}
	return strings.Join(parts, ":")
}

// reindexSource is an entity type the rebuild of the index reads: its table
// with the condition of a live entity, the expressions of title, summary
// and url of its document (the ones of the index triggers and indexers) and
// the statement of the field rows of its documents ("" for a type without).
type reindexSource struct {
	entityType, table, live, title, summary, url, fields string
}

// reindexSources are the indexed entity types in the order of the rebuild.
// A location links to its node in the tree, a contact has no page and no
// url (migration 000182, F-334; the asset summary of migration 000233). The
// field rows of a ci, document and ticket come from the columns of their
// summary (WP-266, backfill of migration 000228), a CI also gets its
// searchable attributes (search_ci_attribute_fields), the field rows of an
// asset are the ones of its trigger (search_asset_fields, source 'trigger';
// WP-308, migration 000233); an empty value gets no row. CIs and assets get
// the row core.tags of the triggers of their tags (reindexTags, WP-788).
var reindexSources = []reindexSource{
	{entityType: "ci", table: "ci", live: "e.deleted_at IS NULL", title: "e.name",
		summary: "concat_ws(' ', e.hostname, e.manufacturer, e.model, e.serial_number, e.os_name, e.os_version)", url: "'/cmdb/' || e.id",
		fields: `SELECT d.id AS document_id, f.field, f.content, 'indexer' AS source
  FROM search_document d JOIN ci e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.hostname', e.hostname), ('core.manufacturer', e.manufacturer), ('core.model', e.model),
       ('core.serial_number', e.serial_number), ('core.os_name', e.os_name), ('core.os_version', e.os_version)) AS f(field, content)
 WHERE d.organization_id = $1 AND d.entity_type = 'ci' AND d.entity_id = ANY($2::uuid[]) AND f.content <> ''
UNION ALL
SELECT d.id, f.field, f.content, 'indexer'
  FROM search_document d JOIN ci e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL search_ci_attribute_fields(e) AS f
 WHERE d.organization_id = $1 AND d.entity_type = 'ci' AND d.entity_id = ANY($2::uuid[])
UNION ALL
` + reindexTags("ci")},
	{entityType: "asset", table: "asset", live: "true", title: "e.name", summary: "search_asset_summary(e)", url: "'/assets'",
		fields: `SELECT d.id AS document_id, f.field, f.content, 'trigger' AS source
  FROM search_document d JOIN asset e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL search_asset_fields(e) AS f
 WHERE d.organization_id = $1 AND d.entity_type = 'asset' AND d.entity_id = ANY($2::uuid[])
UNION ALL
` + reindexTags("asset")},
	{entityType: "document", table: "document", live: "e.deleted_at IS NULL", title: "e.title",
		summary: "concat_ws(' ', e.description, e.file_name, e.category, array_to_string(e.tags, ' '))", url: "'/documents'",
		fields: `SELECT d.id AS document_id, f.field, f.content, 'indexer' AS source
  FROM search_document d JOIN document e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.description', e.description), ('core.file_name', e.file_name), ('core.category', e.category),
       ('core.tags', array_to_string(e.tags, ' '))) AS f(field, content)
 WHERE d.organization_id = $1 AND d.entity_type = 'document' AND d.entity_id = ANY($2::uuid[]) AND f.content <> ''`},
	{entityType: "ticket", table: "ticket", live: "true", title: "e.title",
		summary: `concat_ws(' ', e.description, e.status, e.priority, e.category, (SELECT string_agg(tg.key, ' ' ORDER BY tg.key)
			FROM ticket_tag tt JOIN tag tg ON tg.id = tt.tag_id WHERE tt.ticket_id = e.id))`, url: "'/tickets'",
		fields: `SELECT d.id AS document_id, f.field, f.content, 'indexer' AS source
  FROM search_document d JOIN ticket e ON e.id = d.entity_id AND e.organization_id = d.organization_id
 CROSS JOIN LATERAL (VALUES ('core.description', e.description), ('core.status', e.status), ('core.priority', e.priority),
       ('core.category', e.category),
       ('core.tags', (SELECT string_agg(tg.key, ' ' ORDER BY tg.key) FROM ticket_tag tt JOIN tag tg ON tg.id = tt.tag_id
                       WHERE tt.ticket_id = e.id))) AS f(field, content)
 WHERE d.organization_id = $1 AND d.entity_type = 'ticket' AND d.entity_id = ANY($2::uuid[]) AND f.content <> ''`},
	{entityType: "contact", table: "contact", live: "true", title: "COALESCE(e.display_name, '')",
		summary: "concat_ws(' ', e.email, e.phone, e.role, e.department, e.notes)", url: "''"},
	{entityType: "location", table: "location", live: "true", title: "e.name", summary: "e.kind", url: "'/locations?node=' || e.id"},
	{entityType: "reservation", table: "reservation", live: "true", title: "COALESCE(e.project_ref, e.reason, 'reservation')",
		summary: "concat_ws(' ', e.state, e.reason, e.project_ref)", url: "'/reservations'"},
}

// reindexTags is the statement of the field rows core.tags of the documents
// of a ci or asset batch (source 'trigger', search_tag_text of migration
// 000271, WP-788).
func reindexTags(entityType string) string {
	return `SELECT d.id, '` + tagField + `', t.content, 'trigger'
  FROM search_document d CROSS JOIN LATERAL (SELECT search_tag_text(d.entity_type, d.entity_id) AS content) AS t
 WHERE d.organization_id = $1 AND d.entity_type = '` + entityType + `' AND d.entity_id = ANY($2::uuid[]) AND t.content IS NOT NULL`
}

// reindexLive is the condition that the entity of a row d of
// search_document is live for the rebuild (reindexSources); the document
// of another type has none.
func reindexLive() string {
	var b strings.Builder
	b.WriteString("CASE d.entity_type")
	for i := range reindexSources {
		src := &reindexSources[i]
		fmt.Fprintf(&b, " WHEN '%s' THEN EXISTS (SELECT 1 FROM %s e WHERE e.id = d.entity_id AND e.organization_id = d.organization_id AND %s)",
			src.entityType, src.table, src.live)
	}
	b.WriteString(" ELSE false END")
	return b.String()
}

// ReindexTenant rebuilds the index of the whole organization (SRC-02,
// WP-786). The index must stay complete for every principal, so the
// rebuild runs org-wide even when a client-scoped administrator triggers
// it; it returns only a count (E-08). It reads every entity type
// (reindexSources) in batches of ReindexBatchSize entities ordered by id,
// each in a transaction of its own: the batch upserts the documents of its
// entities, their field rows (WP-266, WP-308) and their identifier rows,
// including network.* (search_identifier_rows, migration 000233). So no
// transaction holds the locks of the whole index, and every search during
// the rebuild finds the documents of the entities it can see. A last
// transaction deletes the documents the run did not touch (updated_at
// before its start) whose entity is gone, with their field rows (cascade)
// and their identifier rows without source_id (the others are the rows of
// the triggers of their source). A failed batch ends the rebuild with its
// error; the batches written before stay.
func (r *PGRepository) ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error) {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return ReindexResult{}, database.ErrNoTenantScope
	}
	if !strings.EqualFold(scope.OrgID, orgID) {
		return ReindexResult{}, database.ErrTenantMismatch
	}
	orgWide := database.OrgWideScope(scope.OrgID, scope.UserID)
	var start time.Time
	if err := database.WithTenant(ctx, r.pool, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&start)
	}); err != nil {
		return ReindexResult{}, err
	}
	count := 0
	for i := range reindexSources {
		src := &reindexSources[i]
		after := "00000000-0000-0000-0000-000000000000"
		for {
			n, last, err := r.reindexBatch(ctx, &orgWide, src, after)
			if err != nil {
				return ReindexResult{Indexed: count}, fmt.Errorf("reindex %s: %w", src.entityType, err)
			}
			count += n
			if n < ReindexBatchSize {
				break
			}
			after = last
		}
	}
	err := database.WithTenant(ctx, r.pool, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `WITH gone AS (
			DELETE FROM search_document d WHERE d.organization_id = $1 AND d.updated_at < $2 AND NOT (`+reindexLive()+`)
			RETURNING d.entity_type, d.entity_id
		)
		DELETE FROM search_identifier s USING gone
		WHERE s.organization_id = $1 AND s.entity_type = gone.entity_type AND s.entity_id = gone.entity_id AND s.source_id IS NULL`,
			orgID, start)
		return err
	})
	if err != nil {
		return ReindexResult{Indexed: count}, fmt.Errorf("reindex cleanup: %w", err)
	}
	return ReindexResult{Indexed: count}, nil
}

// reindexBatch rebuilds the documents of the next ReindexBatchSize live
// entities of the source after the id in one transaction: the documents
// (upsert, updated_at = now()), their field rows (stale rows deleted, the
// others upserted) and their identifier rows (replaced). It returns the
// number of entities and the last id.
func (r *PGRepository) reindexBatch(ctx context.Context, scope *database.TenantScope, src *reindexSource, after string) (n int, last string, err error) {
	err = database.WithTenant(ctx, r.pool, scope, func(ctx context.Context, tx pgx.Tx) error {
		orgID := scope.OrgID
		rows, qerr := tx.Query(ctx, fmt.Sprintf(`SELECT e.id::text FROM %s e WHERE e.organization_id = $1 AND e.id > $2::uuid AND %s
			ORDER BY e.id LIMIT $3`, src.table, src.live), orgID, after, ReindexBatchSize)
		if qerr != nil {
			return qerr
		}
		ids, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil || len(ids) == 0 {
			return qerr
		}
		if _, qerr = tx.Exec(ctx, fmt.Sprintf(`INSERT INTO search_document (organization_id, entity_type, entity_id, title, summary, url, metadata)
			SELECT e.organization_id, '%s', e.id, %s, %s, %s, '{}'::jsonb FROM %s e
			WHERE e.organization_id = $1 AND e.id = ANY($2::uuid[]) ORDER BY e.id
			ON CONFLICT (organization_id, entity_type, entity_id) DO UPDATE SET title = EXCLUDED.title, summary = EXCLUDED.summary,
				url = EXCLUDED.url, metadata = EXCLUDED.metadata, updated_at = now()`,
			src.entityType, src.title, src.summary, src.url, src.table), orgID, ids); qerr != nil {
			return qerr
		}
		// The stale rows are the field rows of the batch without a fresh
		// row: NOT IN over fresh is a hashed subplan. The planner estimates
		// fresh at a few rows, so NOT EXISTS became a nested loop anti join
		// that scanned fresh (about 5,000 rows of 1,000 CIs) once per field
		// row, seconds per batch. Neither column of fresh is NULL (the id
		// of the document, the field key), so NOT IN keeps the result of
		// NOT EXISTS.
		if src.fields != "" {
			if _, qerr = tx.Exec(ctx, `WITH fresh AS (`+src.fields+`), stale AS (
				DELETE FROM search_document_field s USING search_document d
				WHERE d.id = s.document_id AND d.organization_id = $1 AND d.entity_type = $3 AND d.entity_id = ANY($2::uuid[])
				  AND s.organization_id = $1
				  AND (s.document_id, s.field) NOT IN (SELECT fresh.document_id, fresh.field FROM fresh)
			)
			INSERT INTO search_document_field (organization_id, document_id, field, content, source)
			SELECT $1, fresh.document_id, fresh.field, fresh.content, fresh.source FROM fresh
			ON CONFLICT (document_id, field) DO UPDATE SET content = EXCLUDED.content, source = EXCLUDED.source
				WHERE search_document_field.content <> EXCLUDED.content OR search_document_field.source <> EXCLUDED.source`,
				orgID, ids, src.entityType); qerr != nil {
				return qerr
			}
		}
		if _, ok := identifierFields[src.entityType]; ok {
			if _, qerr = tx.Exec(ctx, `DELETE FROM search_identifier WHERE organization_id = $1 AND entity_type = $2 AND entity_id = ANY($3::uuid[])`,
				orgID, src.entityType, ids); qerr != nil {
				return qerr
			}
			if _, qerr = tx.Exec(ctx, `INSERT INTO search_identifier (organization_id, entity_type, entity_id, field, source_id, norm, addr)
				SELECT $1, r.entity_type, r.entity_id, r.field, r.source_id, r.norm, r.addr FROM search_identifier_rows($1) AS r
				WHERE r.entity_type = $2 AND r.entity_id = ANY($3::uuid[])
				ON CONFLICT (source_id, field, entity_id) WHERE source_id IS NOT NULL DO NOTHING`, orgID, src.entityType, ids); qerr != nil {
				return qerr
			}
		}
		n, last = len(ids), ids[len(ids)-1]
		return nil
	})
	if err != nil {
		return 0, after, err
	}
	return n, last, nil
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

// FullDocument is a document of the PostgreSQL index as a remote index
// takes it (SRC-02, WP-787): the document, in Fields its field rows of both
// sources (the indexer's and the triggers': core.<column>,
// attributes.<key>), and its identifier rows.
type FullDocument struct {
	Document
	Identifiers []IdentifierRow
}

// IdentifierRow is a row of search_identifier: the identifier field, the
// normal form and, for an IP, the address (host()).
type IdentifierRow struct {
	Field string `json:"field"`
	Norm  string `json:"norm"`
	Addr  string `json:"addr,omitempty"`
}

// DocumentRef names the document of an entity.
type DocumentRef struct {
	EntityType, EntityID string
}

// FullDocuments reads the full documents of the entities with one
// statement, ordered by document id; an entity without document is left
// out. Like ReindexTenant it reads organization-wide: the documents go to
// the remote index, nothing read here is returned to the caller.
func (r *PGRepository) FullDocuments(ctx context.Context, orgID string, refs []DocumentRef) ([]FullDocument, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	types, ids := make([]string, len(refs)), make([]string, len(refs))
	for i := range refs {
		types[i], ids[i] = refs[i].EntityType, strings.ToLower(refs[i].EntityID)
	}
	return r.readFullDocuments(ctx, orgID,
		`(d.entity_type, d.entity_id::text) IN (SELECT * FROM unnest($2::text[], $3::text[])) ORDER BY d.id`, types, ids)
}

// FullDocumentPage reads up to limit full documents of the organization
// with a document id after the given one ("" for the first page), ordered
// by id, with one statement; organization-wide like FullDocuments.
func (r *PGRepository) FullDocumentPage(ctx context.Context, orgID, after string, limit int) ([]FullDocument, error) {
	if after == "" {
		after = "00000000-0000-0000-0000-000000000000"
	}
	return r.readFullDocuments(ctx, orgID, `d.id > $2::uuid ORDER BY d.id LIMIT $3`, after, limit)
}

// placementDocuments reads the full documents of a placed CI and, for a CI
// bound to an asset, of the asset and all its CIs (HybridBackend.
// ReindexPlacement) with one statement.
func (r *PGRepository) placementDocuments(ctx context.Context, orgID, ciID string) ([]FullDocument, error) {
	return r.readFullDocuments(ctx, orgID, `(
	      (d.entity_type = 'ci' AND d.entity_id IN (SELECT c.id FROM ci c WHERE c.organization_id = $1 AND c.id::text = $2
	                                                UNION SELECT c.id FROM ci c JOIN ci p ON c.asset_id = p.asset_id
	                                                WHERE c.organization_id = $1 AND p.organization_id = $1 AND p.id::text = $2))
	   OR (d.entity_type = 'asset' AND d.entity_id IN (SELECT c.asset_id FROM ci c WHERE c.organization_id = $1 AND c.id::text = $2)))
	ORDER BY d.id`, strings.ToLower(ciID))
}

// readFullDocuments reads the documents of the organization that meet cond
// (with its ORDER BY and LIMIT; parameter $1 is the organization) with
// their field rows and identifier rows in one statement, under the
// organization-wide scope of the context's principal.
func (r *PGRepository) readFullDocuments(ctx context.Context, orgID, cond string, args ...any) ([]FullDocument, error) {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return nil, database.ErrNoTenantScope
	}
	if !strings.EqualFold(scope.OrgID, orgID) {
		return nil, database.ErrTenantMismatch
	}
	orgWide := database.OrgWideScope(scope.OrgID, scope.UserID)
	var out []FullDocument
	err := database.WithTenant(ctx, r.pool, &orgWide, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT d.id::text, d.organization_id::text, d.entity_type, d.entity_id::text, d.title, d.summary,
			       d.url, d.metadata, COALESCE(d.client_id::text, ''), COALESCE(d.site_id::text, ''), d.updated_at,
			       COALESCE((SELECT jsonb_object_agg(f.field, f.content) FROM search_document_field f
			                 WHERE f.document_id = d.id AND f.organization_id = d.organization_id), '{}'::jsonb),
			       COALESCE((SELECT jsonb_agg(jsonb_build_object('field', i.field, 'norm', i.norm, 'addr', i.addr) ORDER BY i.field, i.norm)
			                 FROM (SELECT DISTINCT si.field, si.norm, COALESCE(host(si.addr), '') AS addr FROM search_identifier si
			                       WHERE si.organization_id = d.organization_id AND si.entity_type = d.entity_type
			                         AND si.entity_id = d.entity_id) AS i), '[]'::jsonb)
			FROM search_document d
			WHERE d.organization_id = $1 AND `+cond, append([]any{orgID}, args...)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d FullDocument
			var meta, fields, idents []byte
			if err := rows.Scan(&d.ID, &d.OrganizationID, &d.EntityType, &d.EntityID, &d.Title, &d.Summary, &d.URL, &meta,
				&d.ClientID, &d.SiteID, &d.UpdatedAt, &fields, &idents); err != nil {
				return err
			}
			if err := json.Unmarshal(meta, &d.Metadata); err != nil {
				return fmt.Errorf("unmarshal document metadata for %s: %w", d.ID, err)
			}
			if err := json.Unmarshal(fields, &d.Fields); err != nil {
				return fmt.Errorf("unmarshal field rows of %s: %w", d.ID, err)
			}
			if err := json.Unmarshal(idents, &d.Identifiers); err != nil {
				return fmt.Errorf("unmarshal identifiers of %s: %w", d.ID, err)
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read full search documents: %w", err)
	}
	return out, nil
}
