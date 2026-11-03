package metamodel

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Repository stores metamodel drafts. Every change locks the draft row,
// raises version, and writes its audit entry last in its transaction.
type Repository interface {
	// ListDrafts returns the drafts of a page newest first (at most one
	// more than the limit, without items) and the number of drafts;
	// status "" does not filter.
	ListDrafts(ctx context.Context, orgID, status string, page api.PaginationParams) ([]Draft, int, error)
	GetDraft(ctx context.Context, orgID, id string) (*Draft, error)
	CreateDraft(ctx context.Context, orgID string, in *DraftInput) (*Draft, error)
	UpdateDraft(ctx context.Context, orgID, id string, patch *DraftPatch, ifMatch *int64) (*Draft, error)
	// DiscardDraft sets the status discarded.
	DiscardDraft(ctx context.Context, orgID, id string, ifMatch *int64) (*Draft, error)
	// AddItem captures a checked item with the base of its target.
	AddItem(ctx context.Context, orgID, id string, c *capture, ifMatch *int64) (*Draft, *Item, error)
	RemoveItem(ctx context.Context, orgID, id, itemID string, ifMatch *int64) (*Draft, error)
}

// PGRepository is the PostgreSQL Repository; it works only under the
// request tenant (database.WithRequestTenant) and row level security.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository returns the PostgreSQL draft repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

const draftColumns = `id::text, organization_id::text, name, status, created_by::text, items, test_report, version,
	created_at, updated_at, published_at`

func scanDraft(row pgx.Row) (*Draft, error) {
	var d Draft
	var items, report []byte
	if err := row.Scan(&d.ID, &d.OrganizationID, &d.Name, &d.Status, &d.CreatedBy, &items, &report, &d.Version,
		&d.CreatedAt, &d.UpdatedAt, &d.PublishedAt); err != nil {
		return nil, err
	}
	d.Items = []Item{}
	if len(items) > 0 {
		if err := json.Unmarshal(items, &d.Items); err != nil {
			return nil, fmt.Errorf("decode draft items: %w", err)
		}
	}
	d.ItemCount = len(d.Items)
	if len(report) > 0 {
		d.TestReport = report
	}
	return &d, nil
}

// actor is the user of the request when it is a UUID.
func actor(ctx context.Context) *string {
	user := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	if !audit.IsUUID(user) {
		return nil
	}
	return &user
}

// ListDrafts implements Repository.
func (r *PGRepository) ListDrafts(ctx context.Context, orgID, status string, page api.PaginationParams) ([]Draft, int, error) {
	out := []Draft{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where, tail, args, err := api.NewestFirstSQL(page, 2)
		if err != nil {
			return err
		}
		filter := `($1 = '' OR status = $1)`
		if err = tx.QueryRow(ctx, `SELECT count(*) FROM metamodel_draft WHERE `+filter, status).Scan(&total); err != nil {
			return fmt.Errorf("count metamodel drafts: %w", err)
		}
		rows, err := tx.Query(ctx, `SELECT `+draftColumns+` FROM metamodel_draft WHERE `+filter+` AND `+where+` `+tail,
			append([]any{status}, args...)...)
		if err != nil {
			return fmt.Errorf("list metamodel drafts: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			d, serr := scanDraft(rows)
			if serr != nil {
				return fmt.Errorf("read metamodel draft: %w", serr)
			}
			d.Items = nil
			out = append(out, *d)
		}
		return rows.Err()
	})
	return out, total, err
}

// GetDraft implements Repository.
func (r *PGRepository) GetDraft(ctx context.Context, orgID, id string) (*Draft, error) {
	if !api.IsUUID(id) {
		return nil, ErrDraftNotFound
	}
	var out *Draft
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftColumns+` FROM metamodel_draft WHERE id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDraftNotFound
		}
		if err != nil {
			return fmt.Errorf("read metamodel draft: %w", err)
		}
		out = d
		return nil
	})
	return out, err
}

// CreateDraft implements Repository.
func (r *PGRepository) CreateDraft(ctx context.Context, orgID string, in *DraftInput) (*Draft, error) {
	name, err := checkName(in.Name)
	if err != nil {
		return nil, err
	}
	var out *Draft
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d, ierr := scanDraft(tx.QueryRow(ctx, `INSERT INTO metamodel_draft (organization_id, name, created_by)
			VALUES ($1, $2, $3) RETURNING `+draftColumns, orgID, name, actor(ctx)))
		if ierr != nil {
			return fmt.Errorf("create metamodel draft: %w", ierr)
		}
		out = d
		return recordDraftAudit(ctx, tx, orgID, "metamodel_draft.created", d.ID, nil, d)
	})
	return out, err
}

// UpdateDraft implements Repository: the name.
func (r *PGRepository) UpdateDraft(ctx context.Context, orgID, id string, patch *DraftPatch, ifMatch *int64) (*Draft, error) {
	if patch.Name == nil {
		return nil, fieldError("name", "name is required")
	}
	name, err := checkName(*patch.Name)
	if err != nil {
		return nil, err
	}
	return r.mutate(ctx, orgID, id, ifMatch, "metamodel_draft.updated", func(_ context.Context, _ pgx.Tx, d *Draft) error {
		d.Name = name
		return nil
	})
}

// DiscardDraft implements Repository.
func (r *PGRepository) DiscardDraft(ctx context.Context, orgID, id string, ifMatch *int64) (*Draft, error) {
	return r.mutate(ctx, orgID, id, ifMatch, "metamodel_draft.discarded", func(_ context.Context, _ pgx.Tx, d *Draft) error {
		d.Status = StatusDiscarded
		return nil
	})
}

// AddItem implements Repository: base and schema_versions are read in
// the transaction of the change.
func (r *PGRepository) AddItem(ctx context.Context, orgID, id string, c *capture, ifMatch *int64) (*Draft, *Item, error) {
	var added *Item
	d, err := r.mutate(ctx, orgID, id, ifMatch, "metamodel_draft.item_added", func(ctx context.Context, tx pgx.Tx, d *Draft) error {
		itemID, err := newUUID()
		if err != nil {
			return err
		}
		if refErr := checkRefs(d.Items, itemID, c.Refs); refErr != nil {
			return refErr
		}
		it := &Item{ID: itemID, Kind: c.Kind, Op: c.Op, Payload: c.Payload, Base: json.RawMessage("null"),
			SchemaVersions: map[string]int64{}, CreatedBy: actor(ctx), CreatedAt: time.Now().UTC()}
		if c.TargetID != "" {
			it.TargetID = &c.TargetID
		}
		if c.TargetKey != "" {
			it.TargetKey = &c.TargetKey
		}
		if capErr := captureTarget(ctx, tx, c, it); capErr != nil {
			return capErr
		}
		d.Items = append(d.Items, *it)
		resetTest(d)
		added = it
		return nil
	})
	return d, added, err
}

// RemoveItem implements Repository: an item another item references stays
// (409).
func (r *PGRepository) RemoveItem(ctx context.Context, orgID, id, itemID string, ifMatch *int64) (*Draft, error) {
	return r.mutate(ctx, orgID, id, ifMatch, "metamodel_draft.item_removed", func(_ context.Context, _ pgx.Tx, d *Draft) error {
		at := -1
		for i := range d.Items {
			if d.Items[i].ID == itemID {
				at = i
				continue
			}
			for _, ref := range itemRefs(d.Items[i].Payload) {
				if ref == itemID {
					return ErrItemReferenced
				}
			}
		}
		if at < 0 {
			return ErrItemNotFound
		}
		d.Items = append(d.Items[:at:at], d.Items[at+1:]...)
		resetTest(d)
		return nil
	})
}

// resetTest returns a tested draft to draft: its test report no longer
// describes its items.
func resetTest(d *Draft) {
	if d.Status == StatusTested {
		d.Status = StatusDraft
	}
	d.TestReport = nil
}

// mutate locks a draft (SELECT … FOR UPDATE), refuses a final status (409)
// and a stale If-Match (412), applies fn, writes the draft with version + 1
// and records the audit entry last.
func (r *PGRepository) mutate(ctx context.Context, orgID, id string, ifMatch *int64, action string,
	fn func(ctx context.Context, tx pgx.Tx, d *Draft) error) (*Draft, error) {
	if !api.IsUUID(id) {
		return nil, ErrDraftNotFound
	}
	var out *Draft
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftColumns+` FROM metamodel_draft WHERE id = $1 FOR UPDATE`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDraftNotFound
		}
		if err != nil {
			return fmt.Errorf("lock metamodel draft: %w", err)
		}
		switch {
		case d.Status == StatusPublished || d.Status == StatusDiscarded:
			return ErrDraftImmutable
		case ifMatch != nil && *ifMatch != api.IfMatchAny && *ifMatch != d.Version:
			return ErrVersionMismatch
		}
		before, err := json.Marshal(d)
		if err != nil {
			return fmt.Errorf("encode metamodel draft: %w", err)
		}
		if fnErr := fn(ctx, tx, d); fnErr != nil {
			return fnErr
		}
		items, err := json.Marshal(d.Items)
		if err != nil {
			return fmt.Errorf("encode draft items: %w", err)
		}
		var report any
		if len(d.TestReport) > 0 {
			report = []byte(d.TestReport)
		}
		after, err := scanDraft(tx.QueryRow(ctx, `UPDATE metamodel_draft
			SET name = $2, status = $3, items = $4, test_report = $5, version = version + 1
			WHERE id = $1 RETURNING `+draftColumns, id, d.Name, d.Status, items, report))
		if err != nil {
			return fmt.Errorf("update metamodel draft: %w", err)
		}
		out = after
		return recordDraftAudit(ctx, tx, orgID, action, id, json.RawMessage(before), after)
	})
	return out, err
}

// recordDraftAudit writes the audit entry of a draft change with before
// (nil on create) and after (AUD-07); it marks the request as audited.
func recordDraftAudit(ctx context.Context, tx pgx.Tx, orgID, action, id string, before, after any) error {
	changes := map[string]any{}
	for key, v := range map[string]any{"before": before, "after": after} {
		if v == nil {
			continue
		}
		raw, err := json.Marshal(v)
		if err != nil {
			return fmt.Errorf("encode audit %s: %w", key, err)
		}
		var decoded any
		if err = json.Unmarshal(raw, &decoded); err != nil {
			return fmt.Errorf("decode audit %s: %w", key, err)
		}
		changes[key] = decoded
	}
	actorID, actorType := "", "system"
	if a := actor(ctx); a != nil {
		actorID, actorType = *a, "user"
	} else if strings.TrimSpace(tenant.FromContext(ctx).UserID) != "" {
		actorType = "user"
	}
	if _, err := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{
		OrganizationID: orgID, ActorID: actorID, ActorType: actorType, Action: action,
		ResourceType: "metamodel_draft", ResourceID: id, Changes: changes,
	}); err != nil {
		return fmt.Errorf("record audit: %w", err)
	}
	return nil
}

// newUUID returns a random UUID (version 4) for a draft item.
func newUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("item id: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ─── Capture of the target ─────────────────────────────────────────────────

// target is what captureTarget reads about the target of an item.
type target struct {
	system    bool
	updatedAt *time.Time
	typeID    *string
}

// captureTarget reads the target of an item in the transaction of the
// capture: a missing target is 404, a read-only system row 403, both as
// the resource route answers. It sets base (the state of the target, null
// for a new part), schema_versions of the directly affected types and the
// informative route.
func captureTarget(ctx context.Context, tx pgx.Tx, c *capture, it *Item) error {
	id := c.TargetID
	if id != "" && !api.IsUUID(id) {
		return ErrTargetNotFound
	}
	it.Route = route(c.Spec.Route, "{id}", id, "{name}", c.TargetKey)
	var types []string
	if c.CITypeID != "" {
		types = append(types, c.CITypeID)
	}
	var base any
	switch c.Kind {
	case KindCIType:
		if c.Op == OpCreate {
			break
		}
		t, err := readTarget(ctx, tx, `SELECT organization_id IS NULL, updated_at, id::text FROM ci_type WHERE id = $1`, id)
		if err != nil {
			return err
		}
		switch {
		case t.system && c.Op == OpUpdate:
			return ErrTargetNotFound
		case t.system && (c.Op == OpActivate || c.Op == OpDeactivate):
			return ErrTargetReadOnly
		case t.system && c.Op == OpAssign:
			return fieldError("target_id", "system ci types have no attribute sets of an organization")
		}
		types = append(types, id)
		base = t.updatedAt
		if c.Op == OpAssign {
			sets, err := assignedSets(ctx, tx, id)
			if err != nil {
				return err
			}
			base = sets
		}
	case KindCITypeField:
		t, err := readTarget(ctx, tx, `SELECT organization_id IS NULL, updated_at, id::text FROM ci_type WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if t.system && c.Op == OpUpsert {
			return fieldError("target_id", "system ci types cannot get organization attributes")
		}
		types = append(types, id)
		f, err := readField(ctx, tx, `SELECT organization_id IS NULL, updated_at, ci_type_id::text FROM attribute_definition
			WHERE ci_type_id = $1 AND key = $2`, id, c.TargetKey)
		if err != nil {
			return err
		}
		if c.Op == OpDelete && (f == nil || f.system) {
			return ErrTargetNotFound
		}
		if f != nil {
			base = f.updatedAt
		}
	case KindGlobalField:
		f, err := readField(ctx, tx, `SELECT organization_id IS NULL, updated_at, NULL::text FROM attribute_definition
			WHERE scope = 'global' AND organization_id IS NOT NULL AND key = $1`, c.TargetKey)
		if err != nil {
			return err
		}
		if c.Op == OpDelete && f == nil {
			return ErrTargetNotFound
		}
		if f != nil {
			base = f.updatedAt
		}
	case KindAttributeDefinition:
		if c.Op == OpCreate {
			break
		}
		t, err := readTarget(ctx, tx, `SELECT organization_id IS NULL, updated_at, ci_type_id::text FROM attribute_definition
			WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if t.system {
			return ErrTargetReadOnly
		}
		types = appendType(types, t.typeID)
		base = t.updatedAt
	case KindAttributeSet:
		if c.Op == OpCreate {
			break
		}
		t, err := readTarget(ctx, tx, `SELECT organization_id IS NULL, updated_at, NULL::text FROM attribute_set WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if t.system {
			return ErrTargetReadOnly
		}
		assigned, err := typesOfSet(ctx, tx, id)
		if err != nil {
			return err
		}
		types = append(types, assigned...)
		base = t.updatedAt
	case KindTemplate:
		if c.Op == OpCreate {
			break
		}
		t, err := readTarget(ctx, tx, `SELECT organization_id IS NULL, updated_at, ci_type_id::text FROM template WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if t.system && c.Op != OpClone {
			return ErrTargetReadOnly
		}
		types = appendType(types, t.typeID)
		base = t.updatedAt
	case KindTemplateVersion:
		b, typeID, err := captureTemplateVersion(ctx, tx, c, it)
		if err != nil {
			return err
		}
		types = appendType(types, typeID)
		base = b
	case KindRule, KindLayout:
		if c.Op == OpCreate {
			break
		}
		query := `SELECT organization_id IS NULL, version, ci_type_id::text FROM metamodel_rule WHERE id = $1`
		if c.Kind == KindLayout {
			query = `SELECT false, version, ci_type_id::text FROM form_layout WHERE id = $1`
		}
		var system bool
		var version int64
		var typeID *string
		err := tx.QueryRow(ctx, query, id).Scan(&system, &version, &typeID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return ErrTargetNotFound
		case err != nil:
			return fmt.Errorf("read %s: %w", c.Kind, err)
		case system:
			return ErrTargetReadOnly
		}
		types = appendType(types, typeID)
		base = version
	}
	raw, err := json.Marshal(base)
	if err != nil {
		return fmt.Errorf("encode base: %w", err)
	}
	it.Base = raw
	return schemaVersions(ctx, tx, types, it.SchemaVersions)
}

// captureTemplateVersion reads the target of a template_version item: for
// create the template (base: its current_version), otherwise the version
// (base: its status). A system template is read-only (403).
func captureTemplateVersion(ctx context.Context, tx pgx.Tx, c *capture, it *Item) (base any, typeID *string, err error) {
	if c.Op == OpCreate {
		var system bool
		var current *int64
		err = tx.QueryRow(ctx, `SELECT organization_id IS NULL, current_version, ci_type_id::text FROM template WHERE id = $1`,
			c.TargetID).Scan(&system, &current, &typeID)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return nil, nil, ErrTargetNotFound
		case err != nil:
			return nil, nil, fmt.Errorf("read template: %w", err)
		case system:
			return nil, nil, ErrTargetReadOnly
		}
		return current, typeID, nil
	}
	var system bool
	var status, templateID string
	var version int
	err = tx.QueryRow(ctx, `SELECT t.organization_id IS NULL, v.status, v.template_id::text, v.version, t.ci_type_id::text
		FROM template_version v JOIN template t ON t.id = v.template_id WHERE v.id = $1`, c.TargetID).
		Scan(&system, &status, &templateID, &version, &typeID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, nil, ErrTargetNotFound
	case err != nil:
		return nil, nil, fmt.Errorf("read template version: %w", err)
	case system:
		return nil, nil, ErrTargetReadOnly
	}
	it.Route = route(c.Spec.Route, "{id}", templateID, "{version}", strconv.Itoa(version))
	return status, typeID, nil
}

// readTarget reads system flag, updated_at and type of a target row; none
// is ErrTargetNotFound.
func readTarget(ctx context.Context, tx pgx.Tx, query, id string) (*target, error) {
	var t target
	err := tx.QueryRow(ctx, query, id).Scan(&t.system, &t.updatedAt, &t.typeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrTargetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read item target: %w", err)
	}
	return &t, nil
}

// readField reads a field definition; nil when there is none.
func readField(ctx context.Context, tx pgx.Tx, query string, args ...any) (*target, error) {
	var t target
	err := tx.QueryRow(ctx, query, args...).Scan(&t.system, &t.updatedAt, &t.typeID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read field definition: %w", err)
	}
	return &t, nil
}

// assignedSets is the ordered list of the attribute sets of a type.
func assignedSets(ctx context.Context, tx pgx.Tx, typeID string) ([]string, error) {
	return queryIDs(ctx, tx, `SELECT attribute_set_id::text FROM ci_type_attribute_set WHERE ci_type_id = $1 ORDER BY position`, typeID)
}

// typesOfSet are the types an attribute set is assigned to.
func typesOfSet(ctx context.Context, tx pgx.Tx, setID string) ([]string, error) {
	return queryIDs(ctx, tx, `SELECT ci_type_id::text FROM ci_type_attribute_set WHERE attribute_set_id = $1 ORDER BY ci_type_id`, setID)
}

func queryIDs(ctx context.Context, tx pgx.Tx, query, arg string) ([]string, error) {
	rows, err := tx.Query(ctx, query, arg)
	if err != nil {
		return nil, fmt.Errorf("read item target: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("read item target: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func appendType(types []string, typeID *string) []string {
	if typeID != nil && *typeID != "" {
		return append(types, *typeID)
	}
	return types
}

// schemaVersions stores ci_type.schema_version of the visible types.
func schemaVersions(ctx context.Context, tx pgx.Tx, types []string, out map[string]int64) error {
	if len(types) == 0 {
		return nil
	}
	rows, err := tx.Query(ctx, `SELECT id::text, schema_version FROM ci_type WHERE id = ANY($1::uuid[])`, types)
	if err != nil {
		return fmt.Errorf("read schema versions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var version int64
		if err = rows.Scan(&id, &version); err != nil {
			return fmt.Errorf("read schema versions: %w", err)
		}
		out[id] = version
	}
	return rows.Err()
}

// errStatusFilter refuses an unknown status filter (400).
func errStatusFilter() error {
	return &api.FieldError{Field: "status", Message: "status must be one of draft, tested, published, discarded",
		Status: http.StatusBadRequest}
}
