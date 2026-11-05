package relationship

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"slices"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// The history of the stored relationships (REL-09, AUD-07, WP-296): every
// mutation of an edge writes one row of relationship_change in its
// transaction, with the edge before and after it, the changed fields, who
// made it and the request it came with. The row outlives the edge; its
// client and site scope is that of both endpoints (trigger
// relationship_change_scope), so it is visible like the edge was
// (IMP-07). The route audit stays the audit_log trail of the write routes.

// Change kinds of relationship_change (chk_relationship_change_kind).
// ChangeRefreshed, ChangeRewired and ChangeRestored are written by the
// system paths of WP-771.
const (
	ChangeCreated             = "created"
	ChangeUpdated             = "updated"
	ChangeVerificationChanged = "verification_changed"
	ChangeRefreshed           = "refreshed"
	ChangeRemoved             = "removed"
	ChangeSuppressed          = "suppressed"
	ChangeRewired             = "rewired"
	ChangeRestored            = "restored"
)

// The fields changed_fields names (the editable members of an edge).
const (
	fieldAttributes        = "attributes"
	fieldConfidenceMember  = "confidence"
	fieldVerificationState = "verification_state"
	fieldSourceSystem      = "source_system"
	fieldNotesMember       = "notes"
)

// Change is one row of the history of an edge (GET
// /api/v1/relationships/{id}/changes): before is null for created, after
// for removed and suppressed.
type Change struct {
	ID             string         `json:"id"`
	RelationshipID string         `json:"relationship_id"`
	SourceCIID     string         `json:"source_ci_id"`
	TargetCIID     string         `json:"target_ci_id"`
	RelType        string         `json:"rel_type"`
	ChangeKind     string         `json:"change_kind"`
	ChangedFields  []string       `json:"changed_fields"`
	Before         map[string]any `json:"before"`
	After          map[string]any `json:"after"`
	ActorID        string         `json:"actor_id,omitempty"`
	ActorType      string         `json:"actor_type"`
	Source         string         `json:"source"`
	Reason         string         `json:"reason,omitempty"`
	RequestID      string         `json:"request_id,omitempty"`
	CorrelationID  string         `json:"correlation_id,omitempty"`
	ObservedAt     time.Time      `json:"observed_at"`

	// The relationship type id (the organization's row before the system
	// row) and the scope of both endpoints, for the field permissions.
	relTypeID                    string
	sourceClientID, sourceSiteID string
	targetClientID, targetSiteID string
}

// ChangeRecord is a mutation of an edge for RecordChangeTx: Before is the
// edge before it (nil for created), After the edge after it (nil for
// removed and suppressed). Reason is one of the reasons of
// chk_relationship_change_reason, empty for none. DefaultSource is the
// source without a request context (audit.SourceSystem for the system
// runs: cleanup, merge, unmerge); empty follows the edge (changeSource).
type ChangeRecord struct {
	Kind          string
	Before, After *Relationship
	ChangedFields []string
	Reason        string
	DefaultSource string
}

// ChangeLister lists the history of an edge (the PostgreSQL repository).
type ChangeLister interface {
	// ListChanges returns the rows of the edge, newest first; api.ErrNotFound
	// when the caller sees neither the edge nor a row of its history.
	ListChanges(ctx context.Context, orgID, relationshipID string, page api.PaginationParams) ([]Change, int, error)
}

// RecordChangeTx writes the history row of an edge mutation in tx, the
// transaction of the mutation: personal values of the edge are sealed as
// vault surrogates (SEC-05), the actor is the user of the tenant context
// (none without one) with the type of the principal (user, api_key, system
// without principal), source, request and correlation id come from the
// request context. Without one (the discovery ingest) the source is
// DefaultSource, or collector for an edge of discovery or an agent and
// system otherwise. The client and site scope is derived by the trigger
// relationship_change_scope.
func RecordChangeTx(ctx context.Context, tx pgx.Tx, rec *ChangeRecord) error {
	return RecordChangesTx(ctx, tx, []*ChangeRecord{rec})
}

// RecordChangesTx is RecordChangeTx for several mutations with one INSERT
// (the system paths of WP-771: re-confirmation, cleanup, interface removal,
// merge and unmerge); the rows are written in the order of recs. No record
// writes nothing.
func RecordChangesTx(ctx context.Context, tx pgx.Tx, recs []*ChangeRecord) error {
	if len(recs) == 0 {
		return nil
	}
	n := len(recs)
	orgs, ids, sources, targets := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	relTypes, kinds, fieldSets := make([]string, n), make([]string, n), make([]string, n)
	befores, afters := make([]*string, n), make([]*string, n)
	actorIDs, actorTypes, changeSources := make([]*string, n), make([]string, n), make([]string, n)
	reasons, requestIDs, correlationIDs := make([]*string, n), make([]*string, n), make([]*string, n)
	actorID, actorType := changeActor(ctx)
	if id, ok := actorID.(string); ok {
		for i := range actorIDs {
			actorIDs[i] = &id
		}
	}
	for i, rec := range recs {
		edge := rec.After
		if edge == nil {
			edge = rec.Before
		}
		if edge == nil {
			return errors.New("record relationship change: no edge")
		}
		var err error
		if befores[i], err = sealedJSON(ctx, tx, edge.OrganizationID, rec.Before); err != nil {
			return err
		}
		if afters[i], err = sealedJSON(ctx, tx, edge.OrganizationID, rec.After); err != nil {
			return err
		}
		source, requestID, correlationID := changeSource(ctx, edge.Source, rec.DefaultSource)
		fields := rec.ChangedFields
		if fields == nil {
			fields = []string{}
		}
		rawFields, err := json.Marshal(fields)
		if err != nil {
			return fmt.Errorf("record relationship change: %w", err)
		}
		orgs[i], ids[i], sources[i], targets[i], relTypes[i] = edge.OrganizationID, edge.ID, edge.SourceCIID, edge.TargetCIID, edge.RelType
		kinds[i], fieldSets[i], actorTypes[i], changeSources[i] = rec.Kind, string(rawFields), actorType, source
		reasons[i], requestIDs[i], correlationIDs[i] = optional(rec.Reason), optional(requestID), optional(correlationID)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO relationship_change (organization_id, relationship_id, source_ci_id, target_ci_id, rel_type,
			change_kind, changed_fields, before, after, actor_id, actor_type, source, reason, request_id, correlation_id, observed_at)
		SELECT r.org, r.id, r.src, r.dst, r.rel_type, r.kind, ARRAY(SELECT jsonb_array_elements_text(r.fields::jsonb)),
			r.before::jsonb, r.after::jsonb, r.actor_id, r.actor_type, r.source, r.reason, r.request_id, r.correlation_id, clock_timestamp()
		FROM unnest($1::uuid[], $2::uuid[], $3::uuid[], $4::uuid[], $5::text[], $6::text[], $7::text[], $8::text[], $9::text[],
			$10::uuid[], $11::text[], $12::text[], $13::text[], $14::text[], $15::text[]) WITH ORDINALITY
			AS r(org, id, src, dst, rel_type, kind, fields, before, after, actor_id, actor_type, source, reason, request_id, correlation_id, n)
		ORDER BY r.n`,
		orgs, ids, sources, targets, relTypes, kinds, fieldSets, befores, afters,
		actorIDs, actorTypes, changeSources, reasons, requestIDs, correlationIDs); err != nil {
		return fmt.Errorf("record relationship change: %w", err)
	}
	return nil
}

// optional is nil for an empty value (a NULL column).
func optional(v string) *string {
	if v == "" {
		return nil
	}
	return &v
}

// sealedJSON is the JSON text of the edge with its personal values sealed;
// nil for no edge.
func sealedJSON(ctx context.Context, tx pgx.Tx, orgID string, rel *Relationship) (*string, error) {
	sealed, err := sealEdge(ctx, tx, orgID, rel)
	if err != nil || sealed == nil {
		return nil, err
	}
	raw, err := json.Marshal(sealed)
	if err != nil {
		return nil, fmt.Errorf("seal relationship change: %w", err)
	}
	s := string(raw)
	return &s, nil
}

// sealEdge is the JSON form of the edge with its personal values sealed;
// nil for no edge.
func sealEdge(ctx context.Context, tx pgx.Tx, orgID string, rel *Relationship) (any, error) {
	if rel == nil {
		return nil, nil
	}
	sealed, _, err := pii.SealTx(ctx, tx, orgID, rel)
	if err != nil {
		return nil, fmt.Errorf("seal relationship change: %w", err)
	}
	return sealed, nil
}

// changeActor is the actor of a change made with ctx, as the route audit
// names it (middleware.auditActor): the user of the tenant context (nil
// when it is no UUID) and the type of the principal.
func changeActor(ctx context.Context) (actorID any, actorType string) {
	if userID := tenant.FromContext(ctx).UserID; audit.IsUUID(userID) {
		actorID = userID
	}
	p, ok := identity.PrincipalFromContext(ctx)
	switch {
	case !ok:
		return actorID, "system"
	case p.Type == identity.PrincipalTypeAPIKey:
		return actorID, "api_key"
	}
	return actorID, "user"
}

// changeSource is the source of a change made with ctx and the request and
// correlation id of its request; without a request context the source is
// fallback or, without one, follows the source of the edge.
func changeSource(ctx context.Context, edgeSource, fallback string) (source, requestID, correlationID string) {
	if info, ok := audit.RequestInfoFrom(ctx); ok && info.Source != "" {
		return info.Source, info.RequestID, info.CorrelationID
	}
	if fallback != "" {
		return fallback, "", ""
	}
	if edgeSource == "discovery" || edgeSource == "agent" {
		return audit.SourceCollector, "", ""
	}
	return audit.SourceSystem, "", ""
}

// changedFields names the editable fields whose values differ between
// before and after, in the order of changed_fields.
func changedFields(before, after *Relationship) []string {
	var fields []string
	if !reflect.DeepEqual(nonNilAttributes(before.Attributes), nonNilAttributes(after.Attributes)) {
		fields = append(fields, fieldAttributes)
	}
	if !equalConfidence(before.Confidence, after.Confidence) {
		fields = append(fields, fieldConfidenceMember)
	}
	if before.VerificationState != after.VerificationState {
		fields = append(fields, fieldVerificationState)
	}
	if before.SourceSystem != after.SourceSystem {
		fields = append(fields, fieldSourceSystem)
	}
	if before.Notes != after.Notes {
		fields = append(fields, fieldNotesMember)
	}
	return fields
}

func nonNilAttributes(attrs map[string]any) map[string]any {
	if attrs == nil {
		return map[string]any{}
	}
	return attrs
}

func equalConfidence(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// updateKind is the change kind of a PATCH with the changed fields.
func updateKind(fields []string) string {
	if slices.Contains(fields, fieldVerificationState) {
		return ChangeVerificationChanged
	}
	return ChangeUpdated
}

// recordChangedEvent writes relationship.changed of a PATCH in tx, after
// the history row (database.BeforeCommit, as recordEvent): the edge, its
// source, the change kind and the changed fields, no attribute value.
func (r *PGRepository) recordChangedEvent(ctx context.Context, tx pgx.Tx, rel *Relationship, kind string, fields []string) error {
	if r.outbox == nil {
		return nil
	}
	data := &events.RelationshipChangedData{ID: rel.ID, SourceCIID: rel.SourceCIID, TargetCIID: rel.TargetCIID,
		RelType: rel.RelType, Source: rel.Source, ChangeKind: kind, ChangedFields: slices.Clone(fields)}
	orgID := rel.OrganizationID
	return database.BeforeCommit(ctx, func() error {
		_, err := r.outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.RelationshipChanged, EntityID: data.ID, Data: data})
		return err
	})
}

// changeColumns are the columns of a history row (alias c) in the order of
// scanChange; the relationship type id is the row the caller sees, the
// organization's before the system one.
const changeColumns = `c.id::text, c.relationship_id::text, c.source_ci_id::text, c.target_ci_id::text, c.rel_type,
	c.change_kind, c.changed_fields, c.before, c.after, COALESCE(c.actor_id::text, ''), c.actor_type, c.source,
	COALESCE(c.reason, ''), COALESCE(c.request_id, ''), COALESCE(c.correlation_id, ''), c.observed_at,
	COALESCE(c.source_client_id::text, ''), COALESCE(c.source_site_id::text, ''),
	COALESCE(c.target_client_id::text, ''), COALESCE(c.target_site_id::text, ''),
	COALESCE((SELECT rt.id::text FROM relationship_type rt WHERE rt.key = c.rel_type
	          ORDER BY rt.organization_id NULLS LAST LIMIT 1), '')`

// ListChanges implements ChangeLister under the caller's tenant scope.
func (r *PGRepository) ListChanges(ctx context.Context, orgID, relationshipID string, page api.PaginationParams) ([]Change, int, error) {
	var out []Change
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, total, err = ListChangesTx(ctx, tx, orgID, relationshipID, page)
		return err
	})
	return out, total, err
}

// ListChangesTx lists the history of an edge in tx (a tenant transaction),
// newest first (observed_at, then id), with the personal values revealed
// (deleted ones are null). An edge of which the caller sees neither the
// row (both endpoints visible) nor a history row is api.ErrNotFound: an
// unknown id, a projected structural edge, an edge with an endpoint
// outside the caller's scope. The history package reads the generic
// history of a relationship through it.
func ListChangesTx(ctx context.Context, tx pgx.Tx, orgID, relationshipID string, page api.PaginationParams) ([]Change, int, error) {
	out := []Change{}
	if !api.IsUUID(relationshipID) {
		return out, 0, api.ErrNotFound
	}
	var visible bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci_relationship WHERE id = $1 AND organization_id = $2 AND `+visibleEndpoints+`)
		OR EXISTS (SELECT 1 FROM relationship_change WHERE organization_id = $2 AND relationship_id = $1)`,
		relationshipID, orgID).Scan(&visible); err != nil {
		return nil, 0, fmt.Errorf("check relationship: %w", err)
	}
	if !visible {
		return out, 0, api.ErrNotFound
	}
	var total int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM relationship_change WHERE organization_id = $1 AND relationship_id = $2`,
		orgID, relationshipID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count relationship changes: %w", err)
	}
	rows, err := tx.Query(ctx, `SELECT `+changeColumns+` FROM relationship_change c
		WHERE c.organization_id = $1 AND c.relationship_id = $2
		ORDER BY c.observed_at DESC, c.id LIMIT $3 OFFSET $4`, orgID, relationshipID, page.Limit, page.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("list relationship changes: %w", err)
	}
	if out, err = scanChanges(ctx, tx, orgID, rows); err != nil {
		return nil, 0, err
	}
	return out, total, nil
}

// ChangesTx reads the rows of relationship_change with the ids in tx (a
// tenant transaction) like ListChangesTx, in no particular order, with the
// personal values revealed; a row the caller does not see is missing. The
// history package reads the entries of the edges of a CI through it
// (history.CITimeline, WP-792), so they keep the type and the scope their
// field permissions need (ProjectChanges).
func ChangesTx(ctx context.Context, tx pgx.Tx, orgID string, ids []string) ([]Change, error) {
	if len(ids) == 0 {
		return []Change{}, nil
	}
	rows, err := tx.Query(ctx, `SELECT `+changeColumns+` FROM relationship_change c
		WHERE c.organization_id = $1 AND c.id = ANY ($2::uuid[])`, orgID, ids)
	if err != nil {
		return nil, fmt.Errorf("read relationship changes: %w", err)
	}
	return scanChanges(ctx, tx, orgID, rows)
}

// scanChanges reads the history rows of changeColumns and reveals their
// personal values (deleted ones are null); it closes rows.
func scanChanges(ctx context.Context, tx pgx.Tx, orgID string, rows pgx.Rows) ([]Change, error) {
	defer rows.Close()
	out := []Change{}
	values := []any{}
	for rows.Next() {
		var c Change
		var beforeRaw, afterRaw []byte
		if err := rows.Scan(&c.ID, &c.RelationshipID, &c.SourceCIID, &c.TargetCIID, &c.RelType,
			&c.ChangeKind, &c.ChangedFields, &beforeRaw, &afterRaw, &c.ActorID, &c.ActorType, &c.Source,
			&c.Reason, &c.RequestID, &c.CorrelationID, &c.ObservedAt,
			&c.sourceClientID, &c.sourceSiteID, &c.targetClientID, &c.targetSiteID, &c.relTypeID); err != nil {
			return nil, fmt.Errorf("scan relationship change: %w", err)
		}
		before, err := decodeEdge(beforeRaw)
		if err != nil {
			return nil, err
		}
		after, err := decodeEdge(afterRaw)
		if err != nil {
			return nil, err
		}
		values = append(values, before, after)
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	revealed, err := pii.RevealTx(ctx, tx, orgID, values)
	if err != nil {
		return nil, fmt.Errorf("resolve personal values: %w", err)
	}
	resolved, ok := revealed.([]any)
	if !ok || len(resolved) != 2*len(out) {
		return nil, fmt.Errorf("resolve personal values: %d of %d values", len(resolved), 2*len(out))
	}
	for i := range out {
		out[i].Before, _ = resolved[2*i].(map[string]any)
		out[i].After, _ = resolved[2*i+1].(map[string]any)
	}
	return out, nil
}

// decodeEdge decodes a stored edge; nil for NULL.
func decodeEdge(raw []byte) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var v map[string]any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("decode relationship change: %w", err)
	}
	return v, nil
}

// ListChanges implements ChangeLister: the memory profile keeps no history.
func (r *MemoryRepository) ListChanges(_ context.Context, _, _ string, _ api.PaginationParams) ([]Change, int, error) {
	return []Change{}, 0, nil
}

// ProjectChanges returns the history rows with the fields the principal of
// ctx may read (RBA-07, Konvention 11), with the relationship grant of the
// type and the scope of both endpoints as for the edge (edgeRights):
// unreadable attribute keys, notes and confidence are left out of before
// and after, an attributes member without a readable key altogether; in
// changed_fields attributes stays only when a readable key changed, notes
// and confidence only when readable. Without a restriction of the object
// kind relationship the rows are returned unchanged. types may be nil: the
// rows carry the id of their relationship type.
func ProjectChanges(ctx context.Context, orgID string, changes []Change, types TypeChecker) ([]Change, error) {
	rights, err := newEdgeRights(ctx, orgID, types)
	if err != nil || rights == nil || len(changes) == 0 {
		return changes, err
	}
	out := make([]Change, len(changes))
	copy(out, changes)
	for i := range out {
		if projectErr := rights.projectChange(ctx, &out[i]); projectErr != nil {
			return nil, projectErr
		}
	}
	return out, nil
}

// projectChange projects one history row (ProjectChanges).
func (e *edgeRights) projectChange(ctx context.Context, c *Change) error {
	typeID := c.relTypeID
	if typeID == "" {
		var err error
		if typeID, err = e.typeID(ctx, c.RelType); err != nil {
			return err
		}
	}
	edge := &Relationship{ID: c.RelationshipID, SourceClientID: c.sourceClientID, SourceSiteID: c.sourceSiteID,
		TargetClientID: c.targetClientID, TargetSiteID: c.targetSiteID}
	readable := func(field string) bool { return e.level(typeID, field, edge).Readable() }
	attrReadable := func(key string) bool { return readable(permission.NamespaceAttributes + "." + key) }
	c.Before = projectEdgeValue(c.Before, readable, attrReadable)
	attrsBefore, _ := c.Before[fieldAttributes].(map[string]any)
	c.After = projectEdgeValue(c.After, readable, attrReadable)
	attrsAfter, _ := c.After[fieldAttributes].(map[string]any)
	fields := make([]string, 0, len(c.ChangedFields))
	for _, f := range c.ChangedFields {
		switch f {
		case fieldAttributes:
			// The projected attributes hold only readable keys: they
			// differ when a readable key changed.
			if reflect.DeepEqual(nonNilAttributes(attrsBefore), nonNilAttributes(attrsAfter)) {
				continue
			}
		case fieldNotesMember:
			if !readable(fieldNotes) {
				continue
			}
		case fieldConfidenceMember:
			if !readable(fieldConfidence) {
				continue
			}
		}
		fields = append(fields, f)
	}
	c.ChangedFields = fields
	return nil
}

// projectEdgeValue returns a copy of the stored edge without the fields
// the principal may not read; nil stays nil.
func projectEdgeValue(edge map[string]any, readable, attrReadable func(string) bool) map[string]any {
	if edge == nil {
		return nil
	}
	out := maps.Clone(edge)
	if attrs, ok := edge[fieldAttributes].(map[string]any); ok {
		kept := make(map[string]any, len(attrs))
		for k, v := range attrs {
			if attrReadable(k) {
				kept[k] = v
			}
		}
		if len(kept) == 0 && len(attrs) > 0 {
			delete(out, fieldAttributes)
		} else {
			out[fieldAttributes] = kept
		}
	}
	if !readable(fieldNotes) {
		delete(out, fieldNotesMember)
	}
	if !readable(fieldConfidence) {
		delete(out, fieldConfidenceMember)
	}
	return out
}

// ListChanges handles GET /api/v1/relationships/{id}/changes: the history
// of the edge, newest first, paged by cursor (K6), with the fields the
// principal may read (RBA-07). 404 when the caller sees neither the edge
// nor its history (unknown or malformed id, a projected structural edge,
// an endpoint outside the scope).
func (h *Handler) ListChanges(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", "relationship not found")
	if !ok {
		return
	}
	page, ok := api.ParseOffsetPage(w, r)
	if !ok {
		return
	}
	items, total := []Change{}, 0
	if lister, isLister := h.repo.(ChangeLister); isLister {
		var err error
		items, total, err = lister.ListChanges(r.Context(), t.OrganizationID, id, page)
		if errors.Is(err, api.ErrNotFound) {
			api.WriteError(w, http.StatusNotFound, "Not Found", "relationship not found")
			return
		}
		if err != nil {
			api.WriteRepoError(w, err)
			return
		}
		if items, err = ProjectChanges(r.Context(), t.OrganizationID, items, h.types); err != nil {
			api.WriteRepoError(w, err)
			return
		}
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Change]{
		Data: items, Total: total, Limit: page.Limit, Offset: page.Offset, HasMore: page.Offset+page.Limit < total,
	})
}
