package ci

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
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

// revertibleBulkActions are the bulk actions whose change set this revert
// undoes; the others follow with WP-783.
var revertibleBulkActions = map[string]bool{BulkSetStatus: true, BulkSetFields: true, BulkSetLocation: true}

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

// capturedField is one changed field of a write under a bulk job.
type capturedField struct {
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
// set: they follow their inputs (WP-717).
func captureBulkChanges(ctx context.Context, tx pgx.Tx, w *rowUpdate, req *UpdateRequest, computed map[string]bool) (*bulkCapture, error) {
	jobID, ok := jobs.ChangeSetFrom(ctx)
	if !ok || len(w.changes) == 0 {
		return nil, nil
	}
	c := &bulkCapture{jobID: jobID, reason: manualReason(req)}
	if req.Manual != nil {
		c.author = req.Manual.Author
	}
	for _, change := range w.changes {
		if !derivedFields[change.Field] {
			c.fields = append(c.fields, capturedField{name: change.Field, old: change.Old, new: change.New})
		}
	}
	keys := changedKeys(w.before.Attributes, w.after.Attributes)
	sort.Strings(keys)
	for _, key := range keys {
		if computed[key] {
			continue
		}
		c.fields = append(c.fields, capturedField{name: key, old: w.before.Attributes[key], new: w.after.Attributes[key]})
	}
	if len(c.fields) == 0 {
		return nil, nil
	}
	names := make([]string, len(c.fields))
	for i := range c.fields {
		names[i] = c.fields[i].name
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
			encoded, err := json.Marshal(v)
			if err != nil {
				return fmt.Errorf("encode bulk change set: %w", err)
			}
			raw[j] = encoded
		}
		changes[i] = jobs.Change{JobID: c.jobID, EntityType: jobs.EntityCI, EntityID: item.ID, Op: jobs.OpSet,
			FieldName: f.name, OldValue: raw[0], NewValue: raw[1], OverrideBefore: raw[2], OverrideAfter: raw[3],
			ClientID: item.ClientID, SiteID: item.SiteID}
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
// rows skipped and the others reverted. Without an expectation in ctx it
// does nothing.
func applyRevertExpectation(ctx context.Context, before *Item, req *UpdateRequest) {
	e := revertExpectationFrom(ctx)
	if e == nil {
		return
	}
	e.marks, e.reverted, e.skipped = nil, nil, nil
	var attributes map[string]any
	if req.Attributes != nil {
		attributes = make(map[string]any, len(req.Attributes))
		for k, v := range req.Attributes {
			attributes[k] = v
		}
	}
	for i := range e.rows {
		row := &e.rows[i]
		setter, writable := revertColumns[row.FieldName]
		var keep bool
		switch {
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
		if writable {
			setter(req, nil)
		} else {
			delete(attributes, row.FieldName)
		}
	}
	if len(attributes) == 0 {
		attributes = nil
	}
	req.Attributes = attributes
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

// Revert is the reverter of ci.bulk (jobs.Reverter): a bulk job of a field
// operation with change set rows the caller sees, finished within
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
// of each field the revert writes.
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
		if ciColumnFields[row.FieldName] {
			continue
		}
		var value any
		if err := json.Unmarshal(row.OldValue, &value); err != nil {
			continue
		}
		if req.Attributes == nil {
			req.Attributes = map[string]any{}
		}
		req.Attributes[row.FieldName] = value
	}
	return req
}

// revertCI writes the old values of the rows of one CI back as a manual
// change of the revert job's creator (If-Match "*", reason
// bulk.revert:<job_id>) and reports the CI: reverted when a field went
// back (Fields: the skipped ones), skipped when none did (409 with the
// fields changed since, 404 for a CI no longer visible or deleted, 403 for
// refused action or field permissions), error otherwise.
func (b *BulkRevert) revertCI(ctx context.Context, op *bulkOp, id string, rows []jobs.Change) BulkRevertItem {
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
