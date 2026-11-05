package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The undo of the bulk field operations on CIs (BLK-02, JOB-03; WP-305).
//
// Capture: a write under a bulk job (jobs.WithChangeSet, set by runBulk)
// records in PGRepository.Update, in its transaction, one row of the change
// set per changed field: the CI columns of diffCI without derivedFields
// and the attributes per changed key, named like in ci_field_value, with
// the value before and after and the state of the field's override before
// and after recordManualOverrides. One read of ci_field_value and one
// INSERT per CI (NFR-02).
//
// Revert: POST /jobs/{id}/revert of a ci.bulk job queues the job
// bulk.revert (BulkRevert.Revert). It writes the old values of the rows not
// reverted yet back per CI through Service.Update as a manual change of its
// creator; on the locked row PGRepository.Update leaves every field whose
// value changed since the bulk (revertExpectation) and marks each row
// reverted or skipped in the same transaction. The state of the overrides
// stays as the inverse write leaves it; WP-306 restores it.
//
// The other actions (WP-783) record in the transaction of their write
// path: the owner like a column, each tag actually added or removed (op
// add/remove, field tags) in the tag path, the template list and the
// defaults of a migration (TemplateMigrator) or of replace_template_id,
// the edges in relationship.PGRepository (field relationship) and the
// lifecycle state in lifecycle.Service.Transition. Their revert: tags,
// archive, owner and template change go back in the one inverse write of
// the CI (the tags decided per key on the locked row, a tag deleted from
// the catalog stays away); edges and lifecycle go through the ports of
// WP-780 (BulkRelationshipReverter, BulkLifecycleReverter), which mark
// their rows in their transaction, with the right of the original action
// per CI; a refused object is skipped with its code and its rows marked
// skipped.

// revertibleBulkActions are the bulk actions whose change set the revert
// undoes (WP-305: the field operations; WP-783: tags, archive, owner,
// edges, lifecycle and template change). export is no action of a job
// ci.bulk: its export job has no revert (E-25).
var revertibleBulkActions = map[string]bool{
	BulkSetStatus: true, BulkSetFields: true, BulkSetLocation: true, BulkSetOwner: true,
	BulkAddTags: true, BulkRemoveTags: true, BulkArchive: true,
	BulkAddRelationship: true, BulkRemoveRelationship: true, BulkLifecycleTransition: true, BulkChangeTemplate: true,
}

// Field names of the change set rows outside the CI columns and
// attributes (WP-783).
const (
	// ChangeFieldTags names a tag a bulk added (op add, the key in
	// new_value) or removed (op remove, the key in old_value).
	ChangeFieldTags = "tags"
	// ChangeFieldTemplates names the template list of a CI before and
	// after a template change (op set).
	ChangeFieldTemplates = "templates"
	// ChangeFieldRelationship names an edge a bulk created (op add, the
	// edge in new_value) or deleted (op remove, the row in old_value).
	ChangeFieldRelationship = "relationship"
	// ChangeFieldLifecycle names the lifecycle state of a transition (op
	// set).
	ChangeFieldLifecycle = "lifecycle_state"
)

// capturedOwnerFields are the derived fields the change set records all
// the same: the owner of set_owner (WP-783).
var capturedOwnerFields = map[string]bool{"owner_user_id": true, "owner_team_id": true}

// Refusals of a bulk revert (409).
var (
	ErrBulkActionNotRevertible = fmt.Errorf("%w: the bulk action has no revert", jobs.ErrNotRevertible)
	ErrBulkNothingToRevert     = fmt.Errorf("%w: the job changed nothing the caller can revert", jobs.ErrNotRevertible)
)

// Results of a CI in the report of a revert job.
const (
	revertResultReverted = "reverted"
	revertResultSkipped  = "skipped"
	revertResultError    = "error"
	// revertReason is the change reason of the inverse writes.
	revertReason = "bulk.revert"
)

// ciColumnFields are the CI columns diffCI names; every other field name
// of a change set row is an attribute.
var ciColumnFields = map[string]bool{
	"client_id": true, "asset_id": true, "parent_ci_id": true, "owner_user_id": true, "owner_team_id": true,
	"location_id": true, "site_id": true, "room_id": true, "ci_type_id": true, "name": true, "status": true,
	"manufacturer": true, "model": true, "serial_number": true, "hardware_uuid": true, "management_ip": true,
	"primary_mac": true, "hostname": true, "fqdn": true, "os_name": true, "os_version": true,
	"firmware_version": true, "sys_object_id": true, "attributes": true, "discovery_source": true,
	"first_seen_at": true, "is_manual": true, "tags": true,
}

// revertColumns are the CI columns the revert writes back, with their
// member of UpdateRequest; a row of any other column is skipped.
var revertColumns = map[string]func(*UpdateRequest, *string){
	"name":             func(r *UpdateRequest, v *string) { r.Name = v },
	"owner_user_id":    func(r *UpdateRequest, v *string) { r.OwnerUserID = v },
	"owner_team_id":    func(r *UpdateRequest, v *string) { r.OwnerTeamID = v },
	"status":           func(r *UpdateRequest, v *string) { r.Status = v },
	"client_id":        func(r *UpdateRequest, v *string) { r.ClientID = v },
	"location_id":      func(r *UpdateRequest, v *string) { r.LocationID = v },
	"manufacturer":     func(r *UpdateRequest, v *string) { r.Manufacturer = v },
	"model":            func(r *UpdateRequest, v *string) { r.Model = v },
	"serial_number":    func(r *UpdateRequest, v *string) { r.SerialNumber = v },
	"hardware_uuid":    func(r *UpdateRequest, v *string) { r.HardwareUUID = v },
	"management_ip":    func(r *UpdateRequest, v *string) { r.ManagementIP = v },
	"primary_mac":      func(r *UpdateRequest, v *string) { r.PrimaryMAC = v },
	"hostname":         func(r *UpdateRequest, v *string) { r.Hostname = v },
	"fqdn":             func(r *UpdateRequest, v *string) { r.FQDN = v },
	"os_name":          func(r *UpdateRequest, v *string) { r.OSName = v },
	"os_version":       func(r *UpdateRequest, v *string) { r.OSVersion = v },
	"firmware_version": func(r *UpdateRequest, v *string) { r.FirmwareVersion = v },
	"sys_object_id":    func(r *UpdateRequest, v *string) { r.SysObjectID = v },
}

// ─── Capture ─────────────────────────────────────────────────────────────────

// overrideState is the state of a field's override in ci_field_value, as
// override_before and override_after hold it.
type overrideState struct {
	Value     any       `json:"override_value"`
	Author    *string   `json:"override_author"`
	Reason    *string   `json:"override_reason"`
	At        time.Time `json:"override_at"`
	Protected bool      `json:"protected"`
}

// capturedField is one changed field of a write under a bulk job: op set
// with the value before and after, or a tag added (op add) or removed (op
// remove) with its key in new or old.
type capturedField struct {
	op       string
	name     string
	old, new any
}

// bulkCapture holds the change set rows of one CI write under a bulk job
// until recordUpdate writes them (WP-305).
type bulkCapture struct {
	jobID  string
	fields []capturedField
	// before is the override state of the fields before the write.
	before map[string]*overrideState
	// now is the transaction time, the override_at recordManualOverrides
	// writes (now()).
	now            time.Time
	author, reason string
	written        map[string]any
}

// captureBulkChanges takes the changed fields of w and the state of their
// overrides before recordManualOverrides; nil outside a bulk job or
// without a change. The computed attributes are no field of the change
// set: they follow their inputs (WP-717). Of the derived fields the owner
// is recorded like a column, each tag added or removed as a row of its
// own and a replaced template list as the field templates (WP-783).
func captureBulkChanges(ctx context.Context, tx pgx.Tx, w *rowUpdate, req *UpdateRequest, computed map[string]bool) (*bulkCapture, error) {
	jobID, ok := jobs.ChangeSetFrom(ctx)
	if !ok || (len(w.changes) == 0 && w.templates == nil) {
		return nil, nil
	}
	c := &bulkCapture{jobID: jobID, reason: manualReason(req)}
	if req.Manual != nil {
		c.author = req.Manual.Author
	}
	var names []string
	for _, change := range w.changes {
		switch {
		case change.Field == ChangeFieldTags:
			c.fields = append(c.fields, tagChanges(w.before.Tags, w.after.Tags)...)
		case capturedOwnerFields[change.Field]:
			c.fields = append(c.fields, capturedField{op: jobs.OpSet, name: change.Field, old: change.Old, new: change.New})
		case !derivedFields[change.Field]:
			c.fields = append(c.fields, capturedField{op: jobs.OpSet, name: change.Field, old: change.Old, new: change.New})
			names = append(names, change.Field)
		}
	}
	keys := changedKeys(w.before.Attributes, w.after.Attributes)
	sort.Strings(keys)
	for _, key := range keys {
		if computed[key] {
			continue
		}
		c.fields = append(c.fields, capturedField{op: jobs.OpSet, name: key, old: w.before.Attributes[key], new: w.after.Attributes[key]})
		names = append(names, key)
	}
	if w.templates != nil {
		c.fields = append(c.fields, capturedField{op: jobs.OpSet, name: ChangeFieldTemplates, old: w.templates.before, new: w.templates.after})
	}
	if len(c.fields) == 0 {
		return nil, nil
	}
	if len(names) == 0 {
		// No field of the write can carry an override.
		return c, nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, `
		SELECT now(), COALESCE(jsonb_object_agg(field_name, jsonb_build_object('override_value', override_value,
			'override_author', override_author, 'override_reason', override_reason, 'override_at', override_at,
			'protected', protected)), '{}'::jsonb)
		FROM ci_field_value
		WHERE ci_id = $1 AND field_name = ANY($2::text[]) AND override_at IS NOT NULL`,
		w.before.ID, names).Scan(&c.now, &raw); err != nil {
		return nil, fmt.Errorf("read overrides of the bulk change set: %w", err)
	}
	if err := json.Unmarshal(raw, &c.before); err != nil {
		return nil, fmt.Errorf("decode overrides of the bulk change set: %w", err)
	}
	return c, nil
}

// tagChanges are the tags a write added (op add) and removed (op remove):
// only the keys that actually changed (WP-783).
func tagChanges(before, after []string) []capturedField {
	var out []capturedField
	for _, key := range after {
		if !slices.Contains(before, key) {
			out = append(out, capturedField{op: jobs.OpAdd, name: ChangeFieldTags, new: key})
		}
	}
	for _, key := range before {
		if !slices.Contains(after, key) {
			out = append(out, capturedField{op: jobs.OpRemove, name: ChangeFieldTags, old: key})
		}
	}
	return out
}

// overridden takes the fields recordManualOverrides gave an override.
func (c *bulkCapture) overridden(written map[string]any) {
	if c != nil {
		c.written = written
	}
}

// record writes the change set rows of the write with one INSERT; client
// and site are those of the CI after the write. Nothing for nil.
func (c *bulkCapture) record(ctx context.Context, tx pgx.Tx, orgID string, item *Item) error {
	if c == nil {
		return nil
	}
	changes := make([]jobs.Change, len(c.fields))
	for i := range c.fields {
		f := &c.fields[i]
		before := c.before[f.name]
		afterState := before
		if value, ok := c.written[f.name]; ok {
			reason := c.reason
			afterState = &overrideState{Value: value, Reason: &reason, At: c.now, Protected: true}
			if api.IsUUID(c.author) {
				author := c.author
				afterState.Author = &author
			}
		}
		raw := make([]json.RawMessage, 4)
		for j, v := range []any{f.old, f.new, before, afterState} {
			if state, isState := v.(*overrideState); isState && state == nil {
				continue
			}
			if v == nil && f.op != jobs.OpSet {
				// The side of a tag that was not there (NULL).
				continue
			}
			encoded, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("encode bulk change set: %w", err)
			}
			raw[j] = encoded
		}
		changes[i] = jobs.Change{JobID: c.jobID, EntityType: jobs.EntityCI, EntityID: item.ID, Op: f.op,
			FieldName: f.name, OldValue: raw[0], NewValue: raw[1], OverrideBefore: raw[2], OverrideAfter: raw[3],
			ClientID: item.ClientID, SiteID: item.SiteID}
	}
	return jobs.RecordChangeTx(ctx, tx, orgID, changes)
}

// recordTemplateMigration writes the change set rows of a template
// migration under a bulk job (change_template, WP-783) in tx: the template
// list before and after (field templates) and one row per default the
// migration filled, with old_value NULL. Nothing outside a bulk job.
func recordTemplateMigration(ctx context.Context, tx pgx.Tx, orgID string, before, after *Item, defaults map[string]any) error {
	jobID, ok := jobs.ChangeSetFrom(ctx)
	if !ok {
		return nil
	}
	prior := before.Templates
	if prior == nil {
		prior = []citype.CITemplate{}
	}
	oldList, err := json.Marshal(prior)
	if err != nil {
		return fmt.Errorf("encode bulk change set: %w", err)
	}
	newList, err := json.Marshal(after.Templates)
	if err != nil {
		return fmt.Errorf("encode bulk change set: %w", err)
	}
	row := jobs.Change{JobID: jobID, EntityType: jobs.EntityCI, EntityID: after.ID, Op: jobs.OpSet,
		ClientID: after.ClientID, SiteID: after.SiteID}
	templates := row
	templates.FieldName, templates.OldValue, templates.NewValue = ChangeFieldTemplates, oldList, newList
	changes := []jobs.Change{templates}
	keys := make([]string, 0, len(defaults))
	for key := range defaults {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value, encErr := json.Marshal(defaults[key])
		if encErr != nil {
			return fmt.Errorf("encode bulk change set: %w", encErr)
		}
		filled := row
		filled.FieldName, filled.NewValue = key, value
		changes = append(changes, filled)
	}
	return jobs.RecordChangeTx(ctx, tx, orgID, changes)
}

// ─── Revert expectation ──────────────────────────────────────────────────────

// revertExpectation is what the inverse write of one CI expects: the
// change set rows of the CI not reverted yet. applyRevertExpectation
// decides on the locked row which it reverts (marks, reverted, skipped).
type revertExpectation struct {
	rows     []jobs.Change
	marks    []jobs.ChangeMark
	reverted []string
	skipped  []string
}

type revertExpectationKey struct{}

func withRevertExpectation(ctx context.Context, e *revertExpectation) context.Context {
	return context.WithValue(ctx, revertExpectationKey{}, e)
}

func revertExpectationFrom(ctx context.Context) *revertExpectation {
	e, _ := ctx.Value(revertExpectationKey{}).(*revertExpectation)
	return e
}

// sameRawJSON reports whether value is the JSON raw holds.
func sameRawJSON(value any, raw json.RawMessage) bool {
	encoded, err := json.Marshal(value)
	return err == nil && sameJSON(encoded, raw)
}

// columnValue is the value of the CI column name in item as diffCI
// records it ("" for an empty column).
func columnValue(item *Item, name string) any {
	for _, change := range diffCI(&Item{}, item) {
		if change.Field == name {
			return change.New
		}
	}
	return ""
}

// applyRevertExpectation leaves every field of req whose value in the
// locked row before differs from the value the bulk wrote (new_value),
// and every row of a field the revert does not write; it marks those
// rows skipped and the others reverted. The tags it decides per key on
// the locked row (WP-783): a tag the bulk added goes when the CI still
// has it, a tag the bulk removed comes back when the CI still lacks it and
// the catalog still holds it (read FOR SHARE in tx); the resulting key
// list replaces the tags like PUT /cis/{id}/tags. The template list goes
// back when it is the one the bulk left. Without an expectation in ctx it
// does nothing.
func applyRevertExpectation(ctx context.Context, tx pgx.Tx, orgID string, before *Item, req *UpdateRequest) error {
	e := revertExpectationFrom(ctx)
	if e == nil {
		return nil
	}
	e.marks, e.reverted, e.skipped = nil, nil, nil
	var attributes map[string]any
	if req.Attributes != nil {
		attributes = make(map[string]any, len(req.Attributes))
		for k, v := range req.Attributes {
			attributes[k] = v
		}
	}
	catalog, err := revertTagCatalog(ctx, tx, orgID, e.rows)
	if err != nil {
		return err
	}
	tags := slices.Clone(before.Tags)
	tagsChanged := false
	for i := range e.rows {
		row := &e.rows[i]
		setter, writable := revertColumns[row.FieldName]
		var keep bool
		switch {
		case row.FieldName == ChangeFieldTags && row.Op != jobs.OpSet:
			keep, tags = revertTag(row, tags, catalog)
			tagsChanged = tagsChanged || keep
		case row.FieldName == ChangeFieldTemplates:
			var list []citype.CITemplate
			keep = req.Templates != nil && json.Unmarshal(row.NewValue, &list) == nil && sameTemplates(before.Templates, list)
			if !keep {
				req.Templates = nil
			}
		case writable:
			keep = sameRawJSON(columnValue(before, row.FieldName), row.NewValue)
		case ciColumnFields[row.FieldName]:
			keep = false
		default:
			_, sent := attributes[row.FieldName]
			keep = sent && sameRawJSON(before.Attributes[row.FieldName], row.NewValue)
		}
		if keep {
			e.marks = append(e.marks, jobs.ChangeMark{ID: row.ID, Outcome: jobs.OutcomeReverted})
			e.reverted = append(e.reverted, row.FieldName)
			continue
		}
		e.marks = append(e.marks, jobs.ChangeMark{ID: row.ID, Outcome: jobs.OutcomeSkipped})
		e.skipped = append(e.skipped, row.FieldName)
		switch {
		case writable:
			setter(req, nil)
		case row.FieldName != ChangeFieldTags && row.FieldName != ChangeFieldTemplates:
			delete(attributes, row.FieldName)
		}
	}
	if tagsChanged {
		sort.Strings(tags)
		req.LegacyTags = &tags
	}
	if len(attributes) == 0 {
		attributes = nil
	}
	req.Attributes = attributes
	return nil
}

// revertTag decides one tag row on the tags of the locked row: a tag the
// bulk added is removed when the CI has it, one the bulk removed is added
// back when the CI lacks it and the catalog holds it. It reports whether
// the row is reverted and returns the tags after it.
func revertTag(row *jobs.Change, tags []string, catalog map[string]bool) (reverted bool, after []string) {
	var key string
	raw := row.NewValue
	if row.Op == jobs.OpRemove {
		raw = row.OldValue
	}
	if json.Unmarshal(raw, &key) != nil || key == "" {
		return false, tags
	}
	has := slices.Contains(tags, key)
	switch {
	case row.Op == jobs.OpAdd && has:
		return true, slices.DeleteFunc(tags, func(k string) bool { return k == key })
	case row.Op == jobs.OpRemove && !has && catalog[key]:
		return true, append(tags, key)
	}
	return false, tags
}

// revertTagCatalog reads the catalog keys of the tags the bulk removed
// (FOR SHARE: they stay until the commit); a tag deleted from the catalog
// since does not come back.
func revertTagCatalog(ctx context.Context, tx pgx.Tx, orgID string, rows []jobs.Change) (map[string]bool, error) {
	var keys []string
	for i := range rows {
		var key string
		if rows[i].FieldName == ChangeFieldTags && rows[i].Op == jobs.OpRemove && json.Unmarshal(rows[i].OldValue, &key) == nil {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return nil, nil
	}
	found, err := tx.Query(ctx, `SELECT key FROM tag WHERE organization_id = $1 AND key = ANY($2) FOR SHARE`, orgID, keys)
	if err != nil {
		return nil, fmt.Errorf("read catalog tags of the revert: %w", err)
	}
	known, err := pgx.CollectRows(found, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read catalog tags of the revert: %w", err)
	}
	catalog := make(map[string]bool, len(known))
	for _, key := range known {
		catalog[key] = true
	}
	return catalog, nil
}

// markRevertTx records the outcome of the rows of the expectation in ctx
// in tx (jobs.MarkChangeTx); nothing without one.
func markRevertTx(ctx context.Context, tx pgx.Tx, orgID string) error {
	e := revertExpectationFrom(ctx)
	if e == nil {
		return nil
	}
	return jobs.MarkChangeTx(ctx, tx, orgID, e.marks)
}

// ─── Revert job ──────────────────────────────────────────────────────────────

// RevertJobs is the job service of the bulk revert; jobs.Service
// implements it.
type RevertJobs interface {
	Enqueue(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
	ClaimJob(ctx context.Context, orgID, id string) (*jobs.Job, error)
	Succeed(ctx context.Context, orgID, id string, result any) (*jobs.Job, error)
	Fail(ctx context.Context, orgID, id string, cause error) (*jobs.Job, error)
	MarkReverted(ctx context.Context, orgID, id, by string) (*jobs.Job, error)
}

// RevertQueue submits revert jobs to the job queue on NATS; jobs.Queue
// implements it.
type RevertQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// BulkRevert reverts the bulk jobs of CIs (the reverter of ci.bulk and the
// handler of bulk.revert, WP-305).
type BulkRevert struct {
	svc   *Service
	jobs  RevertJobs
	store jobs.ChangeSetStore
	queue RevertQueue
	now   func() time.Time
}

// NewBulkRevert creates the bulk revert of svc on the job service and the
// change set store.
func NewBulkRevert(svc *Service, js RevertJobs, store jobs.ChangeSetStore) *BulkRevert {
	return &BulkRevert{svc: svc, jobs: js, store: store, now: func() time.Time { return time.Now().UTC() }}
}

// WithQueue submits the revert jobs to the job queue (TEC-14).
func (b *BulkRevert) WithQueue(q RevertQueue) *BulkRevert {
	b.queue = q
	return b
}

// revertParams are the parameters of a bulk.revert job: the bulk job and
// the request facts of its trigger (no rights, WP-728).
type revertParams struct {
	BulkJobID string `json:"bulk_job_id"`
	jobs.ScopeParams
}

// BulkRevertItem is the outcome of one CI of a revert job.
type BulkRevertItem struct {
	ID     string `json:"id"`
	Result string `json:"result"`
	Code   int    `json:"code"`
	Type   string `json:"type,omitempty"`
	Error  string `json:"error,omitempty"`
	// Fields are the fields the revert skipped (changed since the bulk)
	// or the fields an error names.
	Fields []string `json:"fields,omitempty"`
}

// BulkRevertResult is the report of a revert job (GET /jobs/{id}/result).
type BulkRevertResult struct {
	BulkJobID string           `json:"bulk_job_id"`
	Total     int              `json:"total"`
	Reverted  int              `json:"reverted"`
	Skipped   int              `json:"skipped"`
	Failed    int              `json:"failed"`
	Items     []BulkRevertItem `json:"items"`
}

// Revert is the reverter of ci.bulk (jobs.Reverter): a bulk job of a
// revertible action with change set rows the caller sees, finished within
// jobs.RevertWindow, gets a revert job bulk.revert with the idempotency key
// revert:<job_id> and the caller as creator, on the job queue when there is
// one, otherwise run in this process with the caller's tenant and scope. A
// repeated call returns the same revert job.
func (b *BulkRevert) Revert(ctx context.Context, job *jobs.Job) (*jobs.Job, error) {
	if jobs.RevertExpired(job.FinishedAt, b.now()) {
		return nil, jobs.ErrRevertExpired
	}
	var params struct {
		Action string `json:"action"`
	}
	if err := json.Unmarshal(job.Params, &params); err != nil {
		return nil, &jobs.RevertRefusedError{Reason: ErrBulkActionNotRevertible}
	}
	if params.Action == "" {
		params.Action = BulkSetStatus
	}
	if !revertibleBulkActions[params.Action] {
		return nil, &jobs.RevertRefusedError{Reason: ErrBulkActionNotRevertible}
	}
	rows, err := b.store.ChangeSet(ctx, job.OrganizationID, job.ID)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, &jobs.RevertRefusedError{Reason: ErrBulkNothingToRevert}
	}
	opts := jobs.EnqueueOptions{IdempotencyKey: "revert:" + job.ID, CreatedBy: tenant.FromContext(ctx).UserID}
	p := revertParams{BulkJobID: job.ID, ScopeParams: jobs.CaptureScope(ctx)}
	if b.queue != nil {
		revertJob, _, submitErr := b.queue.Submit(ctx, job.OrganizationID, jobs.RevertKind, p, opts)
		if submitErr != nil {
			return nil, fmt.Errorf("submit bulk revert job: %w", submitErr)
		}
		return revertJob, nil
	}
	revertJob, created, err := b.jobs.Enqueue(ctx, job.OrganizationID, jobs.RevertKind, p, opts)
	if err != nil {
		return nil, fmt.Errorf("enqueue bulk revert job: %w", err)
	}
	if created {
		// The revert runs with the caller's tenant and scope, after the
		// request.
		go b.runLocal(context.WithoutCancel(ctx), revertJob, job.ID)
	}
	return revertJob, nil
}

// runLocal claims the revert job, runs it and records its report.
func (b *BulkRevert) runLocal(ctx context.Context, revertJob *jobs.Job, bulkJobID string) {
	claimed, err := b.jobs.ClaimJob(ctx, revertJob.OrganizationID, revertJob.ID)
	if err != nil || claimed == nil {
		slog.ErrorContext(ctx, "claim bulk revert job", "job", revertJob.ID, "error", err)
		return
	}
	result, err := b.run(ctx, claimed, bulkJobID)
	if err != nil {
		if _, failErr := b.jobs.Fail(ctx, claimed.OrganizationID, claimed.ID, err); failErr != nil {
			slog.ErrorContext(ctx, "record failed bulk revert job", "job", claimed.ID, "error", failErr)
		}
		return
	}
	if _, err = b.jobs.Succeed(ctx, claimed.OrganizationID, claimed.ID, result); err != nil {
		slog.ErrorContext(ctx, "record bulk revert job", "job", claimed.ID, "error", err)
	}
}

// RunJob runs a revert job of the job queue (handler of bulk.revert): with
// the tenant and scope of its creator (jobs.ScopeParams.Context; without
// scope snapshot it fails) and the policy of the creator at the time of
// the run (WithCreatorPolicy, fail-closed like RunBulkJob).
func (b *BulkRevert) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	var p revertParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, fmt.Errorf("decode bulk revert job parameters: %w", err)
	}
	runCtx, err := p.Context(ctx, job)
	if err != nil {
		return nil, err
	}
	if b.svc.creatorPolicy == nil {
		return nil, ErrNoCreatorPolicy
	}
	policy, err := b.svc.creatorPolicy(runCtx, job)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNoCreatorPolicy, err)
	}
	return b.run(permission.WithPolicy(runCtx, policy), job, p.BulkJobID)
}

// run reverts the change set of the bulk job bulkJobID that ctx sees, per
// CI in the order of the change set and bulkWorkers at once, and records
// the revert on the bulk job (reverted_at, reverted_by: the creator of the
// revert job).
func (b *BulkRevert) run(ctx context.Context, revertJob *jobs.Job, bulkJobID string) (*BulkRevertResult, error) {
	orgID := revertJob.OrganizationID
	rows, err := b.store.ChangeSet(ctx, orgID, bulkJobID)
	if err != nil {
		return nil, err
	}
	var order []string
	byCI := map[string][]jobs.Change{}
	for i := range rows {
		row := &rows[i]
		if row.EntityType != jobs.EntityCI || row.RevertedAt != nil {
			continue
		}
		if _, seen := byCI[row.EntityID]; !seen {
			order = append(order, row.EntityID)
		}
		byCI[row.EntityID] = append(byCI[row.EntityID], *row)
	}
	result := &BulkRevertResult{BulkJobID: bulkJobID, Total: len(order), Items: make([]BulkRevertItem, len(order))}
	itemCtx := context.WithoutCancel(ctx)
	op := bulkOp{orgID: orgID, author: revertJob.CreatedBy, reason: revertReason + ":" + bulkJobID}
	var wg sync.WaitGroup
	slots := make(chan struct{}, bulkWorkers)
	for i, id := range order {
		wg.Add(1)
		slots <- struct{}{}
		go func(i int, id string) {
			defer func() { <-slots; wg.Done() }()
			result.Items[i] = b.revertCI(itemCtx, &op, id, byCI[id])
		}(i, id)
	}
	wg.Wait()
	for i := range result.Items {
		switch result.Items[i].Result {
		case revertResultReverted:
			result.Reverted++
		case revertResultSkipped:
			result.Skipped++
		default:
			result.Failed++
		}
	}
	if _, markErr := b.jobs.MarkReverted(ctx, orgID, bulkJobID, revertJob.CreatedBy); markErr != nil && !errors.Is(markErr, jobs.ErrAlreadyReverted) {
		return nil, fmt.Errorf("record the revert of the bulk job: %w", markErr)
	}
	return result, nil
}

// revertRequest is the inverse write of the rows of one CI: the old value
// of each field the revert writes, the template list before a template
// change and null for an attribute the bulk filled (old_value NULL, the
// default of a template migration). The tags applyRevertExpectation
// decides on the locked row.
func revertRequest(rows []jobs.Change) UpdateRequest {
	var req UpdateRequest
	for i := range rows {
		row := &rows[i]
		if setter, ok := revertColumns[row.FieldName]; ok {
			var value string
			if err := json.Unmarshal(row.OldValue, &value); err == nil {
				setter(&req, &value)
			}
			continue
		}
		if row.FieldName == ChangeFieldTemplates {
			var list []citype.CITemplate
			if err := json.Unmarshal(row.OldValue, &list); err == nil {
				req.Templates = &list
			}
			continue
		}
		if ciColumnFields[row.FieldName] {
			continue
		}
		var value any
		if len(row.OldValue) > 0 {
			if err := json.Unmarshal(row.OldValue, &value); err != nil {
				continue
			}
		}
		if req.Attributes == nil {
			req.Attributes = map[string]any{}
		}
		req.Attributes[row.FieldName] = value
	}
	return req
}

// BulkEdgeRevert is the inverse operation of one edge of
// add_relationship (Op add: delete the edge Edge names by its id when it
// exists unchanged) or remove_relationship (Op remove: create the deleted
// row Edge holds again). Marks are the change set rows the operation
// records as reverted in its transaction.
type BulkEdgeRevert struct {
	CIID  string
	Op    string
	Edge  json.RawMessage
	Actor string
	Marks []jobs.ChangeMark
}

// BulkRelationshipReverter is the revert part of the port
// BulkRelationships (server/report_adapters.go, WP-783): the edge goes
// through the paths of DELETE and POST /relationships. A changed or
// missing edge, an equal edge that exists already (uq_ci_relationship_edge)
// or a refusal is a *BulkItemError with its status.
type BulkRelationshipReverter interface {
	RevertRelationship(ctx context.Context, orgID string, rev *BulkEdgeRevert) error
}

// BulkLifecycleReverter is the revert part of the port BulkLifecycle: the
// transition back with the guards of the model, which records marks as
// reverted in its transaction (WP-783); errors as in Transition.
type BulkLifecycleReverter interface {
	RevertTransition(ctx context.Context, orgID, ciID string, req *BulkTransition, marks []jobs.ChangeMark) error
}

// revertCI reverts the rows of one CI: the edges and the lifecycle state
// through the ports of WP-780, every other row through the field path
// (revertFields). A bulk job has one action, so the rows of a CI are of
// one kind.
func (b *BulkRevert) revertCI(ctx context.Context, op *bulkOp, id string, rows []jobs.Change) BulkRevertItem {
	var fields []jobs.Change
	var edges, transitions []jobs.Change
	for i := range rows {
		switch rows[i].FieldName {
		case ChangeFieldRelationship:
			edges = append(edges, rows[i])
		case ChangeFieldLifecycle:
			transitions = append(transitions, rows[i])
		default:
			fields = append(fields, rows[i])
		}
	}
	var items []BulkRevertItem
	if len(fields) > 0 {
		items = append(items, b.revertFields(ctx, op, id, fields))
	}
	for i := range edges {
		items = append(items, b.revertEdge(ctx, op, id, &edges[i]))
	}
	for i := range transitions {
		items = append(items, b.revertTransition(ctx, op, id, &transitions[i]))
	}
	return mergeRevertItems(id, items)
}

// mergeRevertItems is the outcome of a CI from the outcomes of its parts:
// the first error, otherwise reverted when a part went back (Fields: the
// skipped ones), otherwise the first skipped part.
func mergeRevertItems(id string, items []BulkRevertItem) BulkRevertItem {
	if len(items) == 1 {
		return items[0]
	}
	var reverted *BulkRevertItem
	var skipped []string
	for i := range items {
		switch items[i].Result {
		case revertResultError:
			return items[i]
		case revertResultReverted:
			if reverted == nil {
				reverted = &items[i]
			}
		}
		skipped = append(skipped, items[i].Fields...)
	}
	if reverted != nil {
		return BulkRevertItem{ID: id, Result: revertResultReverted, Code: http.StatusOK, Fields: skipped}
	}
	if len(items) == 0 {
		return BulkRevertItem{ID: id, Result: revertResultSkipped, Code: http.StatusConflict, Type: problemType(nil, http.StatusConflict)}
	}
	return items[0]
}

// revertFields writes the old values of the rows of one CI back as a manual
// change of the revert job's creator (If-Match "*", reason
// bulk.revert:<job_id>) and reports the CI: reverted when a field went
// back (Fields: the skipped ones), skipped when none did (409 with the
// fields changed since, 404 for a CI no longer visible or deleted, 403 for
// refused action or field permissions), error otherwise. The tags, the
// status of archive, the owner and the template list with the defaults of
// a migration go back in this one write (WP-783).
func (b *BulkRevert) revertFields(ctx context.Context, op *bulkOp, id string, rows []jobs.Change) BulkRevertItem {
	expectation := &revertExpectation{rows: rows}
	_, err := b.svc.Update(withRevertExpectation(ctx, expectation), op.orgID, id, op.manual(revertRequest(rows)))
	var forbidden *api.FieldForbiddenError
	switch {
	case err == nil:
	case errors.Is(err, ErrNotFound) || err.Error() == "not found":
		return BulkRevertItem{ID: id, Result: revertResultSkipped, Code: http.StatusNotFound, Error: "CI not found",
			Type: problemType(nil, http.StatusNotFound)}
	case errors.As(err, &forbidden), errors.Is(err, ErrInstanceAttributeForbidden):
		f := bulkError(err)
		return BulkRevertItem{ID: id, Result: revertResultSkipped, Code: f.code, Error: f.msg, Type: problemType(err, f.code), Fields: f.fields}
	default:
		f := bulkError(err)
		return BulkRevertItem{ID: id, Result: revertResultError, Code: f.code, Error: f.msg, Type: problemType(err, f.code), Fields: f.fields}
	}
	if len(expectation.reverted) == 0 {
		return BulkRevertItem{ID: id, Result: revertResultSkipped, Code: http.StatusConflict,
			Error: "the fields changed since the bulk", Type: problemType(nil, http.StatusConflict), Fields: expectation.skipped}
	}
	return BulkRevertItem{ID: id, Result: revertResultReverted, Code: http.StatusOK, Fields: expectation.skipped}
}

// revertAllowed checks the right of the original operation on the CI like
// the bulk did (WP-783): the principal of the request must hold it, and
// the action grants of the policy (a queue job: the creator's rights at
// run time) must allow it on the CI's type and scope.
func revertAllowed(ctx context.Context, perm identity.Permission, item *Item) error {
	if p, ok := identity.PrincipalFromContext(ctx); ok && !p.Has(perm) {
		return &api.FieldForbiddenError{Action: string(perm)}
	}
	return permission.CheckActionOn(ctx, permission.KindCI, perm, func(context.Context) ([]permission.ActionTarget, error) {
		return []permission.ActionTarget{ciTarget(item)}, nil
	})
}

// revertRefusal reports a part whose inverse operation was refused: a
// refusal of the object (403, 404, 409, 422) is skipped and its rows are
// marked skipped, anything else is an error that leaves them for another
// revert.
func (b *BulkRevert) revertRefusal(ctx context.Context, orgID, id, field string, row *jobs.Change, err error) BulkRevertItem {
	f := bulkError(err)
	item := BulkRevertItem{ID: id, Result: revertResultError, Code: f.code, Error: f.msg, Type: problemType(err, f.code), Fields: f.fields}
	switch f.code {
	case http.StatusForbidden, http.StatusNotFound, http.StatusConflict, http.StatusUnprocessableEntity:
	default:
		return item
	}
	item.Result, item.Fields = revertResultSkipped, []string{field}
	if markErr := b.markSkipped(ctx, orgID, row); markErr != nil {
		slog.ErrorContext(ctx, "mark skipped bulk change set row", "row", row.ID, "error", markErr)
	}
	return item
}

// markSkipped records the outcome skipped on a row whose inverse operation
// was refused, in a transaction of the repository (TxRunner) of its own.
func (b *BulkRevert) markSkipped(ctx context.Context, orgID string, row *jobs.Change) error {
	runner, ok := b.svc.repo.(TxRunner)
	if !ok {
		return nil
	}
	marks := []jobs.ChangeMark{{ID: row.ID, Outcome: jobs.OutcomeSkipped}}
	return runner.RunInTx(ctx, orgID, func(ctx context.Context) error {
		if !database.InTx(ctx) {
			return nil
		}
		// The repository's transaction of ctx is joined; no pool is needed.
		return database.WithRequestTenant(ctx, nil, orgID, func(ctx context.Context, tx pgx.Tx) error {
			return jobs.MarkChangeTx(ctx, tx, orgID, marks)
		})
	})
}

// revertEdge reverts the edge of one row of add_relationship or
// remove_relationship through the relationship port, after the right
// relationship:write on the CI (403 skipped without it).
func (b *BulkRevert) revertEdge(ctx context.Context, op *bulkOp, id string, row *jobs.Change) BulkRevertItem {
	rev, ok := b.svc.delegates.Relationships.(BulkRelationshipReverter)
	if !ok {
		f := unavailableFailure()
		return BulkRevertItem{ID: id, Result: revertResultError, Code: f.code, Error: f.msg, Type: problemType(nil, f.code)}
	}
	item, err := b.svc.repo.GetByID(ctx, op.orgID, id)
	if err == nil {
		err = revertAllowed(ctx, identity.PermRelationshipWrite, item)
	}
	if err == nil {
		edge := row.NewValue
		if row.Op == jobs.OpRemove {
			edge = row.OldValue
		}
		err = rev.RevertRelationship(ctx, op.orgID, &BulkEdgeRevert{CIID: id, Op: row.Op, Edge: edge, Actor: op.author,
			Marks: []jobs.ChangeMark{{ID: row.ID, Outcome: jobs.OutcomeReverted}}})
	}
	if err != nil {
		return b.revertRefusal(ctx, op.orgID, id, ChangeFieldRelationship, row, err)
	}
	return BulkRevertItem{ID: id, Result: revertResultReverted, Code: http.StatusOK}
}

// revertTransition takes one CI back to the lifecycle state before the
// bulk (old_value) with the guards of its model, from the state the bulk
// set (from_state = new_value), after the right lifecycle:transition on
// the CI. A missing edge, a guard (lifecycle-guard), another current state
// or a CI that had no state are skipped with their problem.
func (b *BulkRevert) revertTransition(ctx context.Context, op *bulkOp, id string, row *jobs.Change) BulkRevertItem {
	rev, ok := b.svc.delegates.Lifecycle.(BulkLifecycleReverter)
	if !ok {
		f := unavailableFailure()
		return BulkRevertItem{ID: id, Result: revertResultError, Code: f.code, Error: f.msg, Type: problemType(nil, f.code)}
	}
	var from, to string
	_ = json.Unmarshal(row.NewValue, &from)
	if len(row.OldValue) > 0 {
		_ = json.Unmarshal(row.OldValue, &to)
	}
	item, err := b.svc.repo.GetByID(ctx, op.orgID, id)
	if err == nil {
		err = revertAllowed(ctx, identity.PermLifecycleTransition, item)
	}
	if err == nil && to == "" {
		err = &BulkItemError{Status: http.StatusConflict, Detail: "the CI had no lifecycle state before the bulk", Fields: []string{"to_state"}}
	}
	if err == nil {
		err = rev.RevertTransition(ctx, op.orgID, id, &BulkTransition{ToState: to, FromState: &from, Reason: op.reason},
			[]jobs.ChangeMark{{ID: row.ID, Outcome: jobs.OutcomeReverted}})
	}
	if err != nil {
		return b.revertRefusal(ctx, op.orgID, id, ChangeFieldLifecycle, row, err)
	}
	return BulkRevertItem{ID: id, Result: revertResultReverted, Code: http.StatusOK}
}
