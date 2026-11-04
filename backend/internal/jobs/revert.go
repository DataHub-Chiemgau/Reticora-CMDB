package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The generic part of the bulk undo (BLK-02, JOB-03; WP-305): the change
// set of a bulk job in bulk_change_set (migration 000243). The bulk run
// names its job in the context (WithChangeSet); the writer of an entity
// records each changed field in the transaction of its change
// (RecordChangeTx), and the revert marks the rows it handled in the
// transaction of the inverse change (MarkChangeTx). The domain logic of
// the revert lives with the entity (ci/bulk_revert.go): jobs must not
// import ci.

// Entity types and operations of a change set row.
const (
	EntityCI    = "ci"
	EntityAsset = "asset"
	OpSet       = "set"
	OpAdd       = "add"
	OpRemove    = "remove"
	// RevertKind is the job kind of a bulk revert; it has no revert.
	RevertKind = "bulk.revert"
	// RevertWindow is how long a bulk job can be reverted after it
	// finished (BLK-02).
	RevertWindow = 24 * time.Hour
	// Outcomes of a change set row a revert handled.
	OutcomeReverted = "reverted"
	OutcomeSkipped  = "skipped"
	// ProblemRevertExpired is the problem type of a revert after the
	// window (409).
	ProblemRevertExpired = "https://reticora.io/problems/revert-expired"
)

// RevertExpiredError refuses the revert of a bulk job that finished more
// than RevertWindow ago (409, type ProblemRevertExpired).
type RevertExpiredError struct{}

func (RevertExpiredError) Error() string {
	return "the revert window of the job has expired"
}

// ProblemType implements api.TypedProblem.
func (RevertExpiredError) ProblemType() string { return ProblemRevertExpired }

// ErrRevertExpired is the refusal of a revert after the window.
var ErrRevertExpired error = RevertExpiredError{}

// RevertExpired reports whether the revert window of a job that finished at
// finished has passed at now.
func RevertExpired(finished *time.Time, now time.Time) bool {
	return finished != nil && now.Sub(*finished) > RevertWindow
}

// changeSetKey is the context key of the bulk job whose change set the
// writes of the context record.
type changeSetKey struct{}

// WithChangeSet returns ctx in which the writes record their changes into
// the change set of job jobID.
func WithChangeSet(ctx context.Context, jobID string) context.Context {
	return context.WithValue(ctx, changeSetKey{}, jobID)
}

// ChangeSetFrom returns the job whose change set the writes of ctx record.
func ChangeSetFrom(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(changeSetKey{}).(string)
	return id, ok && id != ""
}

// Change is one row of a change set: one field of one entity a bulk job
// changed. The values are JSON; NULL (nil) in the overrides is a field
// without override.
type Change struct {
	ID             string          `json:"id,omitempty"`
	JobID          string          `json:"job_id"`
	EntityType     string          `json:"entity_type"`
	EntityID       string          `json:"entity_id"`
	Op             string          `json:"op"`
	FieldName      string          `json:"field_name,omitempty"`
	OldValue       json.RawMessage `json:"old_value,omitempty"`
	NewValue       json.RawMessage `json:"new_value,omitempty"`
	OverrideBefore json.RawMessage `json:"override_before,omitempty"`
	OverrideAfter  json.RawMessage `json:"override_after,omitempty"`
	// ClientID and SiteID are the scope of the entity after the change.
	ClientID      string     `json:"client_id,omitempty"`
	SiteID        string     `json:"site_id,omitempty"`
	RevertedAt    *time.Time `json:"reverted_at,omitempty"`
	RevertOutcome string     `json:"revert_outcome,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

// ChangeMark is the outcome a revert records on a change set row.
type ChangeMark struct {
	ID      string
	Outcome string
}

// ChangeSetStore reads the change set of a job; PGRepository implements
// it, the memory repository does not (no revert without a database).
type ChangeSetStore interface {
	ChangeSet(ctx context.Context, orgID, jobID string) ([]Change, error)
}

func jsonOrNil(raw json.RawMessage) *string {
	if len(raw) == 0 {
		return nil
	}
	s := string(raw)
	return &s
}

// RecordChangeTx writes the changes of one write in tx with one statement
// (NFR-02): the rows of the organization orgID, each with its job, entity,
// field and scope.
func RecordChangeTx(ctx context.Context, tx pgx.Tx, orgID string, changes []Change) error {
	if len(changes) == 0 {
		return nil
	}
	n := len(changes)
	jobIDs, types, entities, ops := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	fields, clients, sites := make([]*string, n), make([]*string, n), make([]*string, n)
	olds, news, before, after := make([]*string, n), make([]*string, n), make([]*string, n), make([]*string, n)
	text := func(s string) *string {
		if s == "" {
			return nil
		}
		return &s
	}
	for i := range changes {
		c := &changes[i]
		jobIDs[i], types[i], entities[i], ops[i] = c.JobID, c.EntityType, c.EntityID, c.Op
		fields[i], clients[i], sites[i] = text(c.FieldName), text(c.ClientID), text(c.SiteID)
		olds[i], news[i] = jsonOrNil(c.OldValue), jsonOrNil(c.NewValue)
		before[i], after[i] = jsonOrNil(c.OverrideBefore), jsonOrNil(c.OverrideAfter)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO bulk_change_set (organization_id, job_id, entity_type, entity_id, op, field_name,
			old_value, new_value, override_before, override_after, client_id, site_id)
		SELECT $1, c.job_id::uuid, c.entity_type, c.entity_id::uuid, c.op, c.field_name, c.old_value::jsonb,
			c.new_value::jsonb, c.override_before::jsonb, c.override_after::jsonb, c.client_id::uuid, c.site_id::uuid
		FROM unnest($2::text[], $3::text[], $4::text[], $5::text[], $6::text[], $7::text[], $8::text[],
			$9::text[], $10::text[], $11::text[], $12::text[]) WITH ORDINALITY
			AS c(job_id, entity_type, entity_id, op, field_name, old_value, new_value, override_before,
				override_after, client_id, site_id, ord)
		ORDER BY c.ord`,
		orgID, jobIDs, types, entities, ops, fields, olds, news, before, after, clients, sites); err != nil {
		return fmt.Errorf("record bulk change set: %w", err)
	}
	return nil
}

// MarkChangeTx records in tx the outcome of a revert on the change set rows
// of marks that no revert handled yet.
func MarkChangeTx(ctx context.Context, tx pgx.Tx, orgID string, marks []ChangeMark) error {
	if len(marks) == 0 {
		return nil
	}
	ids, outcomes := make([]string, len(marks)), make([]string, len(marks))
	for i := range marks {
		ids[i], outcomes[i] = marks[i].ID, marks[i].Outcome
	}
	if _, err := tx.Exec(ctx, `
		UPDATE bulk_change_set s SET reverted_at = now(), revert_outcome = m.outcome
		FROM unnest($2::text[], $3::text[]) AS m(id, outcome)
		WHERE s.organization_id = $1 AND s.id = m.id::uuid AND s.reverted_at IS NULL`,
		orgID, ids, outcomes); err != nil {
		return fmt.Errorf("mark bulk change set: %w", err)
	}
	return nil
}

// ChangeSet implements ChangeSetStore: the change set rows of job jobID in
// the order they were written. On request paths row level security shows
// the rows in the caller's scope only.
func (r *PGRepository) ChangeSet(ctx context.Context, orgID, jobID string) ([]Change, error) {
	if !isUUID(jobID) {
		return nil, ErrNotFound
	}
	var out []Change
	err := r.inScope(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, job_id::text, entity_type, entity_id::text, op, COALESCE(field_name, ''),
				old_value, new_value, override_before, override_after, COALESCE(client_id::text, ''),
				COALESCE(site_id::text, ''), reverted_at, COALESCE(revert_outcome, ''), created_at
			FROM bulk_change_set
			WHERE organization_id = $1 AND job_id = $2
			ORDER BY created_at, entity_id, id`, orgID, jobID)
		if err != nil {
			return fmt.Errorf("read bulk change set: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var c Change
			var olds, news, before, after []byte
			if err := rows.Scan(&c.ID, &c.JobID, &c.EntityType, &c.EntityID, &c.Op, &c.FieldName, &olds, &news,
				&before, &after, &c.ClientID, &c.SiteID, &c.RevertedAt, &c.RevertOutcome, &c.CreatedAt); err != nil {
				return fmt.Errorf("scan bulk change set: %w", err)
			}
			c.OldValue, c.NewValue, c.OverrideBefore, c.OverrideAfter = olds, news, before, after
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// ErrNothingToRevert: the job has no change set row the caller sees.
var ErrNothingToRevert = errors.New("the job changed nothing the caller can revert")
