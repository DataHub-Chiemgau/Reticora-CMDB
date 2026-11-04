package metamodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Publication of a tested draft (WP-261, MET-53, AUD-07, EVT-03): POST
// /api/v1/metamodel/drafts/{id}/publish locks the draft, checks the targets
// of its items against their base, applies every item in one transaction
// through its resource route (the handlers and repositories of the package
// citype, so the checks, the error contract, the audit entries per element
// and the index jobs are those of the direct path), materializes the
// schemas of the affected types once and writes the audit entry and the
// event metamodel.published of the publication last. Any failure rolls back
// every element.

// regenerationTriggers are the constraint triggers that materialize the
// schemas of the types (regenerate_attribute_schema, migration 000224 and
// 000200): the publication defers them and fires them after its last item.
const regenerationTriggers = "trg_attribute_definition_regenerate, trg_ci_type_parent_regenerate, " +
	"trg_attribute_set_regenerate, trg_ci_type_attribute_set_regenerate"

// Errors of the publication.
var (
	// ErrNotTested: only a tested draft is published (409).
	ErrNotTested = errors.New("only a tested draft can be published; test it first")
)

// StaleItem is an item whose target changed or vanished since it was
// captured.
type StaleItem struct {
	ItemID    string  `json:"item_id"`
	Kind      string  `json:"kind"`
	Op        string  `json:"op"`
	TargetID  *string `json:"target_id"`
	TargetKey *string `json:"target_key"`
	// Reason is changed or missing.
	Reason string `json:"reason"`
}

// StaleError refuses a publication whose items rest on a stale base (409);
// nothing was changed.
type StaleError struct {
	Items []StaleItem
}

func (e *StaleError) Error() string {
	return "the targets of items of the draft changed since they were captured"
}

// ItemError is the refusal of one item by its resource route: the status
// and problem body of the route; every element was rolled back.
type ItemError struct {
	ItemID, Kind, Op string
	Status           int
	Body             []byte
}

func (e *ItemError) Error() string {
	return fmt.Sprintf("item %s (%s/%s) was refused with %d", e.ItemID, e.Kind, e.Op, e.Status)
}

// Applier runs the resource route of an item (method and path, payload as
// request body) in the context of the publication and returns the status
// and body of its answer; the routes join the transaction of the context.
type Applier interface {
	Apply(ctx context.Context, method, path string, payload []byte) (status int, body []byte)
}

// Publisher publishes tested drafts; PGRepository implements it. Without
// it (no database) POST …/publish answers 503.
type Publisher interface {
	// Publish applies the items of the tested draft id through apply in
	// one transaction and marks the draft published: ErrDraftNotFound,
	// ErrNotTested and ErrDraftImmutable (409), *StaleError (409),
	// *ItemError (the answer of the route) or a field error.
	Publish(ctx context.Context, orgID, id string, apply Applier) (*PublishResult, error)
}

// PublishResult is the answer of POST …/publish: the published draft, the
// number of applied elements per kind and the affected types with their
// schema_version after the publication.
type PublishResult struct {
	Draft  *Draft                          `json:"draft"`
	Counts map[string]int                  `json:"counts"`
	Types  []events.MetamodelPublishedType `json:"types"`
}

// Publish implements Repository: the whole publication runs in one
// transaction of the request (database.WithRequestTenantTx, NFR-03).
func (r *PGRepository) Publish(ctx context.Context, orgID, id string, apply Applier) (*PublishResult, error) {
	if !api.IsUUID(id) {
		return nil, ErrDraftNotFound
	}
	var out *PublishResult
	err := database.WithRequestTenantTx(ctx, r.pool, orgID, func(txCtx context.Context) error {
		// Joins the transaction for the statements of the publication.
		return database.WithRequestTenant(txCtx, r.pool, orgID, func(txCtx context.Context, tx pgx.Tx) error {
			res, perr := publishTx(txCtx, tx, orgID, id, apply)
			out = res
			return perr
		})
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// publishTx is the publication in the transaction tx (ctx carries it).
func publishTx(ctx context.Context, tx pgx.Tx, orgID, id string, apply Applier) (*PublishResult, error) {
	d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftColumns+` FROM metamodel_draft WHERE id = $1 FOR UPDATE`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrDraftNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lock metamodel draft: %w", err)
	}
	switch d.Status {
	case StatusPublished, StatusDiscarded:
		return nil, ErrDraftImmutable
	case StatusTested:
	default:
		return nil, ErrNotTested
	}
	// The targets of all items, locked, against their base before the
	// first item changes anything.
	stale, err := staleItems(ctx, tx, d.Items)
	if err != nil {
		return nil, err
	}
	if len(stale) > 0 {
		return nil, &StaleError{Items: stale}
	}
	before, err := typeVersions(ctx, tx)
	if err != nil {
		return nil, err
	}
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS `+regenerationTriggers+` DEFERRED`); err != nil {
		return nil, fmt.Errorf("defer the schema regeneration: %w", err)
	}
	counts, err := applyItems(citype.WithPublication(ctx), d.Items, apply)
	if err != nil {
		return nil, err
	}
	// The generator materializes every affected type once; a composition
	// conflict it finds is the 422 of the direct path.
	if _, err = tx.Exec(ctx, `SET CONSTRAINTS `+regenerationTriggers+` IMMEDIATE`); err != nil {
		var field *api.FieldError
		if mapped := citype.AttributeDBError(err); errors.As(mapped, &field) {
			return nil, field
		}
		return nil, fmt.Errorf("regenerate the schemas: %w", err)
	}
	after, err := typeVersions(ctx, tx)
	if err != nil {
		return nil, err
	}
	types := affectedTypes(d.Items, before, after)
	published, err := scanDraft(tx.QueryRow(ctx, `UPDATE metamodel_draft
		SET status = $2, published_at = now(), version = version + 1
		WHERE id = $1 RETURNING `+draftColumns, id, StatusPublished))
	if err != nil {
		return nil, fmt.Errorf("publish metamodel draft: %w", err)
	}
	// Last in the transaction: the audit entry of the publication as a
	// unit next to the entries of its elements (AUD-07), then its event.
	if aerr := recordDraftAudit(ctx, tx, orgID, events.MetamodelPublished, id, d, published); aerr != nil {
		return nil, aerr
	}
	if _, err = events.NewOutbox().Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.MetamodelPublished, EntityID: id,
		Data: events.MetamodelPublishedData{ID: id, DraftID: id, Counts: counts, Types: types}}); err != nil {
		return nil, fmt.Errorf("append metamodel.published: %w", err)
	}
	return &PublishResult{Draft: published, Counts: counts, Types: types}, nil
}

// applyItems applies the items in their order through their resource
// routes. An item references only items added before it (checkRefs), so
// the order of the draft is an order of the dependencies; the reference
// {"$item": "<item id>"} is replaced by the id the referenced item
// created (E-112). The first refusal stops the publication.
func applyItems(ctx context.Context, items []Item, apply Applier) (map[string]int, error) {
	counts := map[string]int{}
	created := map[string]string{}
	for i := range items {
		it := &items[i]
		method, path, ok := strings.Cut(it.Route, " ")
		if !ok {
			return nil, fmt.Errorf("item %s has no route", it.ID)
		}
		payload, err := resolveRefs(it.Payload, created)
		if err != nil {
			return nil, err
		}
		if method == http.MethodDelete {
			payload = nil
		}
		status, body := apply.Apply(ctx, method, path, payload)
		if status < 200 || status >= 300 {
			return nil, &ItemError{ItemID: it.ID, Kind: it.Kind, Op: it.Op, Status: status, Body: body}
		}
		if database.TxFailed(ctx) {
			return nil, fmt.Errorf("item %s: %w", it.ID, database.ErrTxAborted)
		}
		if it.Op == OpCreate || it.Op == OpClone {
			var obj struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(body, &obj) == nil && obj.ID != "" {
				created[it.ID] = obj.ID
			}
		}
		counts[it.Kind]++
	}
	return counts, nil
}

// resolveRefs replaces the item references of a payload by the ids the
// referenced items created.
// validation: fixed texts.
func resolveRefs(payload json.RawMessage, created map[string]string) ([]byte, error) {
	refs := itemRefs(payload)
	if len(refs) == 0 {
		return payload, nil
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, fieldError("payload", "payload must be a JSON object")
	}
	var missing bool
	var walk func(v any) any
	walk = func(v any) any {
		switch t := v.(type) {
		case map[string]any:
			if ref, isRef := t["$item"].(string); isRef && len(t) == 1 {
				id, ok := created[ref]
				if !ok {
					missing = true
				}
				return id
			}
			for k, child := range t {
				t[k] = walk(child)
			}
		case []any:
			for i, child := range t {
				t[i] = walk(child)
			}
		}
		return v
	}
	resolved := walk(generic)
	if missing {
		return nil, fieldError("payload", "an item reference names an item that created no part")
	}
	out, err := json.Marshal(resolved)
	if err != nil {
		return nil, fieldError("payload", "payload must be a JSON object")
	}
	return out, nil
}

// staleItems locks the target of every item with a base (FOR UPDATE) and
// reads it again as the capture did (captureTarget): an item whose target
// vanished or whose base differs is stale. Items without base (new parts)
// have nothing to compare.
func staleItems(ctx context.Context, tx pgx.Tx, items []Item) ([]StaleItem, error) {
	out := []StaleItem{}
	for i := range items {
		it := &items[i]
		if isNull(it.Base) {
			continue
		}
		c := &capture{Kind: it.Kind, Op: it.Op, Spec: ItemKinds[it.Kind][it.Op]}
		if it.TargetID != nil {
			c.TargetID = *it.TargetID
		}
		if it.TargetKey != nil {
			c.TargetKey = *it.TargetKey
		}
		if err := lockTarget(ctx, tx, c); err != nil {
			return nil, err
		}
		probe := &Item{SchemaVersions: map[string]int64{}}
		err := captureTarget(ctx, tx, c, probe)
		var field *api.FieldError
		reason := ""
		switch {
		case errors.Is(err, ErrTargetNotFound), errors.Is(err, ErrTargetReadOnly), errors.As(err, &field):
			reason = "missing"
		case err != nil:
			return nil, err
		case !sameBase(it.Base, probe.Base):
			reason = "changed"
		}
		if reason != "" {
			out = append(out, StaleItem{ItemID: it.ID, Kind: it.Kind, Op: it.Op, TargetID: it.TargetID,
				TargetKey: it.TargetKey, Reason: reason})
		}
	}
	return out, nil
}

// lockTarget locks the rows the base of an item describes. Rows the
// organization may not update (system rows) are not locked; they do not
// change under it.
func lockTarget(ctx context.Context, tx pgx.Tx, c *capture) error {
	query, args := "", []any{c.TargetID}
	switch c.Kind {
	case KindCIType:
		query = `SELECT 1 FROM ci_type WHERE id = $1 FOR UPDATE`
	case KindCITypeField:
		query, args = `SELECT 1 FROM attribute_definition WHERE ci_type_id = $1 AND key = $2 FOR UPDATE`, []any{c.TargetID, c.TargetKey}
	case KindGlobalField:
		query, args = `SELECT 1 FROM attribute_definition WHERE scope = 'global' AND organization_id IS NOT NULL AND key = $1
			FOR UPDATE`, []any{c.TargetKey}
	case KindAttributeDefinition:
		query = `SELECT 1 FROM attribute_definition WHERE id = $1 FOR UPDATE`
	case KindAttributeSet:
		query = `SELECT 1 FROM attribute_set WHERE id = $1 FOR UPDATE`
	case KindTemplate:
		query = `SELECT 1 FROM template WHERE id = $1 FOR UPDATE`
	case KindTemplateVersion:
		query = `SELECT 1 FROM template_version WHERE id = $1 FOR UPDATE`
		if c.Op == OpCreate {
			query = `SELECT 1 FROM template WHERE id = $1 FOR UPDATE`
		}
	case KindRule:
		query = `SELECT 1 FROM metamodel_rule WHERE id = $1 FOR UPDATE`
	case KindLayout:
		query = `SELECT 1 FROM form_layout WHERE id = $1 FOR UPDATE`
	default:
		return nil
	}
	if c.Kind != KindGlobalField && !api.IsUUID(c.TargetID) {
		return nil
	}
	if _, err := tx.Exec(ctx, query, args...); err != nil {
		return fmt.Errorf("lock the target of an item: %w", err)
	}
	return nil
}

// isNull reports whether a base is absent (JSON null).
func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null"))
}

// sameBase compares two bases: equal JSON, or the same instant.
func sameBase(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) == nil && json.Compact(&cb, b) == nil && bytes.Equal(ca.Bytes(), cb.Bytes()) {
		return true
	}
	var ta, tb time.Time
	if json.Unmarshal(a, &ta) == nil && json.Unmarshal(b, &tb) == nil {
		return ta.Equal(tb)
	}
	return false
}

// typeVersions reads schema_version of the organization's types.
func typeVersions(ctx context.Context, tx pgx.Tx) (map[string]int64, error) {
	rows, err := tx.Query(ctx, `SELECT id::text, schema_version FROM ci_type WHERE organization_id IS NOT NULL`)
	if err != nil {
		return nil, fmt.Errorf("read schema versions: %w", err)
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var typeID string
		var version int64
		if err = rows.Scan(&typeID, &version); err != nil {
			return nil, fmt.Errorf("read schema versions: %w", err)
		}
		out[typeID] = version
	}
	return out, rows.Err()
}

// affectedTypes are the types of the publication with their schema_version
// after it: every type whose schema_version changed, every new type and
// every existing type an item names (schema_versions of the capture),
// sorted by id.
func affectedTypes(items []Item, before, after map[string]int64) []events.MetamodelPublishedType {
	named := map[string]bool{}
	for typeID, version := range after {
		if old, ok := before[typeID]; !ok || old != version {
			named[typeID] = true
		}
	}
	for i := range items {
		for typeID := range items[i].SchemaVersions {
			if _, ok := after[typeID]; ok {
				named[typeID] = true
			}
		}
	}
	out := make([]events.MetamodelPublishedType, 0, len(named))
	for typeID := range named {
		out = append(out, events.MetamodelPublishedType{ID: typeID, SchemaVersion: after[typeID]})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ─── Webhooks of the applied elements ──────────────────────────────────────

// webhookEvent is a webhook event a resource route dispatched during the
// publication.
type webhookEvent struct {
	event   string
	payload any
}

// webhookBuffer collects the webhook events of the applied elements; the
// draft handler dispatches them after the commit, none after a rollback.
type webhookBuffer struct {
	mu     sync.Mutex
	events []webhookEvent
}

type webhookBufferKey struct{}

func withWebhookBuffer(ctx context.Context, b *webhookBuffer) context.Context {
	return context.WithValue(ctx, webhookBufferKey{}, b)
}

// bufferingDispatcher is the citype.EventDispatcher of the resource routes
// the publication runs: it keeps the events in the buffer of the context.
type bufferingDispatcher struct{}

// Dispatch implements citype.EventDispatcher.
func (bufferingDispatcher) Dispatch(ctx context.Context, _, event string, payload any) {
	b, ok := ctx.Value(webhookBufferKey{}).(*webhookBuffer)
	if !ok || b == nil {
		return
	}
	b.mu.Lock()
	b.events = append(b.events, webhookEvent{event: event, payload: payload})
	b.mu.Unlock()
}

// flush dispatches the collected events through d.
func (b *webhookBuffer) flush(ctx context.Context, orgID string, d citype.EventDispatcher) {
	if d == nil {
		return
	}
	b.mu.Lock()
	collected := b.events
	b.events = nil
	b.mu.Unlock()
	for _, e := range collected {
		d.Dispatch(ctx, orgID, e.event, e.payload)
	}
}

// ─── Application through the resource routes ───────────────────────────────

// routeApplier runs the resource routes of the items on the routes of a
// citype.Handler.
type routeApplier struct {
	routes http.Handler
}

// newRouteApplier serves the resource routes with the repository catalog
// (the instance with the index scheduler) and the buffering dispatcher.
func newRouteApplier(catalog citype.Repository) *routeApplier {
	mux := chi.NewRouter()
	citype.NewHandler(catalog, bufferingDispatcher{}).RegisterRoutes(mux)
	return &routeApplier{routes: mux}
}

// Apply implements Applier.
func (a *routeApplier) Apply(ctx context.Context, method, path string, payload []byte) (status int, body []byte) {
	// The router of the publication request must not route the inner one.
	ctx = context.WithValue(ctx, chi.RouteCtxKey, nil)
	req, err := http.NewRequestWithContext(ctx, method, path, bytes.NewReader(payload))
	if err != nil {
		raw, _ := json.Marshal(api.ProblemDetail{Type: "https://reticora.io/problems/500", Title: "Internal Error",
			Status: http.StatusInternalServerError, Detail: "the route of the item could not be built"})
		return http.StatusInternalServerError, raw
	}
	req.Header.Set("Content-Type", "application/json")
	rec := &responseRecorder{header: http.Header{}}
	a.routes.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	return rec.status, rec.body.Bytes()
}

// responseRecorder keeps the answer of a resource route.
type responseRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *responseRecorder) Header() http.Header { return r.header }

func (r *responseRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}
