package ci

// Refresh and backfill of computed attributes (WP-718, JOB-02 [P2]): the
// write path (WP-717) computes the values of a CI only when the CI is
// written. Expressions with today or an asset field age without such a
// write, and existing CIs get no value (or keep a stale one) when a
// computed definition is created, changed or deleted. The system job
// attribute.computed_refresh recomputes them every hour; the job
// attribute.computed_backfill, queued by the citype repository
// (citype.ComputedScheduler), recomputes the CIs of the changed definition.
//
// Both write only the values that changed, per batch of at most
// RefreshBatch CIs with one statement for the values and one for their
// provenance (ci_field_value, source computed, rank 100). Neither raises
// ci.version nor writes history, audit entries, ci.changed or the outbox:
// the audit and outbox section of the organization (SR-08) stays free. The
// search document holds no attributes (IndexDocumentFor), so the search
// index stays as it is; once WP-308 adds attributes to it, the job updates
// it after the commit (database.AfterCommit).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// RefreshJobKind is the system job that recomputes the computed attributes
// every hour (JOB-02 [P2]); BackfillJobKind the job a change of a computed
// definition queues.
const (
	RefreshJobKind  = "attribute.computed_refresh"
	BackfillJobKind = citype.ComputedBackfillJobKind
)

// RefreshBatch bounds the CIs of one transaction of the refresh and the
// backfill.
const RefreshBatch = 100

// ComputedRefresh runs the refresh (Schedule) and the backfill
// (BackfillSchedule, ScheduleBackfill) of computed attributes.
type ComputedRefresh struct {
	pool   *pgxpool.Pool
	fields FieldResolver
	jobs   *jobs.Service
	now    func() time.Time
	// Batch bounds the CIs of one transaction (RefreshBatch).
	Batch int
	// DueBatch bounds the backfill jobs of one run.
	DueBatch int
}

// NewComputedRefresh creates the runner; fields resolves the definitions
// (citype's repository).
func NewComputedRefresh(pool *pgxpool.Pool, fields FieldResolver) *ComputedRefresh {
	return &ComputedRefresh{pool: pool, fields: fields, now: func() time.Time { return time.Now().UTC() },
		Batch: RefreshBatch, DueBatch: 50}
}

// WithJobs runs the backfill jobs through svc and registers their kind.
func (j *ComputedRefresh) WithJobs(svc *jobs.Service) *ComputedRefresh {
	svc.Register(jobs.Kind{Name: BackfillJobKind, MaxAttempts: 3, Timeout: time.Hour})
	j.jobs = svc
	return j
}

// WithClock replaces the clock of the computation (tests).
func (j *ComputedRefresh) WithClock(now func() time.Time) *ComputedRefresh {
	j.now = now
	return j
}

// Schedule is the hourly refresh on the scheduler: with "today" in the
// organization's time zone a value is current at most an hour after local
// midnight.
func (j *ComputedRefresh) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: RefreshJobKind, Spec: "@every 1h", MaxCatchUp: 1, Timeout: time.Hour,
		Run: func(ctx context.Context) error {
			_, err := j.Run(ctx)
			return err
		}}
}

// BackfillSchedule runs the due backfill jobs every minute on the
// scheduler, as the attribute index jobs (citype.DDLJob).
func (j *ComputedRefresh) BackfillSchedule() jobs.Schedule {
	return jobs.Schedule{Kind: BackfillJobKind, Spec: "@every 1m", Timeout: time.Hour, Run: func(ctx context.Context) error {
		_, err := j.RunDue(ctx)
		return err
	}}
}

// RefreshResult counts what a run did: the organizations it went through,
// the CIs it checked and the CIs whose computed values changed.
type RefreshResult struct {
	Organizations int `json:"organizations"`
	Checked       int `json:"checked"`
	Changed       int `json:"changed"`
}

// Run refreshes the computed values of every organization. The
// organizations are read with a system read (database.OrganizationIDs),
// each organization is computed in its own tenant transactions. A failing
// organization does not stop the others; the errors are joined at the end.
func (j *ComputedRefresh) Run(ctx context.Context) (RefreshResult, error) {
	var total RefreshResult
	orgs, err := database.OrganizationIDs(ctx, j.pool)
	if err != nil {
		return total, fmt.Errorf("list organizations: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		res, oerr := j.RunOrg(ctx, org)
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
			continue
		}
		total.Organizations++
		total.Checked += res.Checked
		total.Changed += res.Changed
	}
	slog.Info("computed attributes refreshed", "organizations", total.Organizations, "checked", total.Checked,
		"changed", total.Changed, "failed", len(errs))
	return total, errors.Join(errs...)
}

// RunOrg refreshes the computed values of one organization.
func (j *ComputedRefresh) RunOrg(ctx context.Context, orgID string) (RefreshResult, error) {
	return j.runOrg(ctx, orgID, nil)
}

// ScheduleBackfill implements citype.ComputedScheduler: it queues a
// backfill job of the organization for the changed definition.
func (j *ComputedRefresh) ScheduleBackfill(ctx context.Context, orgID string, b citype.ComputedBackfill) error {
	if j.jobs == nil {
		return errors.New("computed attribute backfill: no job service")
	}
	_, _, err := j.jobs.Enqueue(ctx, orgID, BackfillJobKind, b, jobs.EnqueueOptions{})
	return err
}

// RunDue claims and runs the due backfill jobs of every organization and
// returns how many succeeded. A failed job is retried after the backoff of
// its kind; the run itself fails only when the queue cannot be read.
func (j *ComputedRefresh) RunDue(ctx context.Context) (int, error) {
	if j.jobs == nil {
		return 0, nil
	}
	due, err := j.jobs.Due(ctx, j.DueBatch, BackfillJobKind)
	if err != nil {
		return 0, fmt.Errorf("list due computed attribute backfills: %w", err)
	}
	done := 0
	for i := range due {
		job, cerr := j.jobs.ClaimJob(ctx, due[i].OrganizationID, due[i].ID)
		if cerr != nil || job == nil {
			continue
		}
		var b citype.ComputedBackfill
		var res RefreshResult
		if err = json.Unmarshal(job.Params, &b); err == nil {
			res, err = j.runOrg(ctx, job.OrganizationID, &b)
		}
		if err != nil {
			slog.Error("computed attribute backfill failed", "job", job.ID, "definition", b.DefinitionID, "error", err)
			if _, ferr := j.jobs.Fail(ctx, job.OrganizationID, job.ID, err); ferr != nil {
				slog.Error("record failed computed attribute backfill", "job", job.ID, "error", ferr)
			}
			continue
		}
		slog.Info("computed attributes backfilled", "job", job.ID, "definition", b.DefinitionID, "key", b.Key,
			"checked", res.Checked, "changed", res.Changed)
		if _, serr := j.jobs.Succeed(ctx, job.OrganizationID, job.ID, res); serr != nil {
			slog.Error("record computed attribute backfill", "job", job.ID, "error", serr)
			continue
		}
		done++
	}
	return done, nil
}

// zeroUUID is the start of the walk over the CIs by id.
const zeroUUID = "00000000-0000-0000-0000-000000000000"

// runOrg recomputes the CIs of the organization in batches of j.Batch, each
// in a tenant transaction of the organization (organization-wide scope):
// the CIs of the types with computed definitions, the CIs with templates or
// instance definitions (their definitions are resolved per CI) and the CIs
// with a computed value (whose definition may be gone). only, for a
// backfill of a type definition, narrows them to the subtree of its type
// and the CIs with a computed value of its key.
func (j *ComputedRefresh) runOrg(ctx context.Context, orgID string, only *citype.ComputedBackfill) (RefreshResult, error) {
	res := RefreshResult{Organizations: 1}
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	byType := map[string][]fieldmeta.FieldDefinition{}
	var computedTypes []string
	err := database.WithTenant(ctx, j.pool, &scope, func(txCtx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(txCtx, `SELECT DISTINCT ci_type_id::text FROM ci
			WHERE organization_id = $1 AND deleted_at IS NULL AND ci_type_id IS NOT NULL ORDER BY 1`, orgID)
		if qerr != nil {
			return fmt.Errorf("read ci types: %w", qerr)
		}
		ids, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
		if qerr != nil {
			return fmt.Errorf("read ci types: %w", qerr)
		}
		for _, id := range ids {
			defs, rerr := j.definitions(txCtx, tx, orgID, id, "")
			if rerr != nil {
				return rerr
			}
			if len(defs) > 0 {
				byType[strings.ToLower(id)] = defs
				computedTypes = append(computedTypes, id)
			}
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	batch := j.Batch
	if batch <= 0 || batch > RefreshBatch {
		batch = RefreshBatch
	}
	after := zeroUUID
	for {
		var n, changed int
		var last string
		err = database.WithTenant(ctx, j.pool, &scope, func(txCtx context.Context, tx pgx.Tx) error {
			var berr error
			n, changed, last, berr = j.batch(txCtx, tx, orgID, &refreshBatch{byType: byType, types: computedTypes,
				after: after, limit: batch, only: only})
			return berr
		})
		if err != nil {
			return res, err
		}
		res.Checked += n
		res.Changed += changed
		if n < batch || ctx.Err() != nil {
			return res, ctx.Err()
		}
		after = last
	}
}

// definitions resolves the computed definitions of a CI of ciTypeID (ciID
// "" for the definitions of the type) in tx.
func (j *ComputedRefresh) definitions(ctx context.Context, tx pgx.Tx, orgID, ciTypeID, ciID string) ([]fieldmeta.FieldDefinition, error) {
	defs, err := resolveFields(database.ContextWithTx(ctx, tx), j.fields, orgID, ciTypeID, ciID)
	if err != nil {
		return nil, fmt.Errorf("resolve the definitions of ci type %s: %w", ciTypeID, err)
	}
	var out []fieldmeta.FieldDefinition
	for i := range defs {
		if defs[i].Computed != nil {
			out = append(out, defs[i])
		}
	}
	return out, nil
}

// refreshBatch is what one batch of runOrg reads.
type refreshBatch struct {
	byType map[string][]fieldmeta.FieldDefinition
	types  []string
	after  string
	limit  int
	only   *citype.ComputedBackfill
}

// refreshCandidate is a CI of a batch: its stored state, the keys of its
// computed provenance rows and whether it has templates or instance
// definitions of its own.
type refreshCandidate struct {
	item   *Item
	stale  []string
	layers bool
	defs   []fieldmeta.FieldDefinition
}

// candidateScanner appends the computed provenance keys and the layer flag
// of a CI to a scan of ciSelectColumns.
type candidateScanner struct {
	ciScanner
	names  *[]string
	layers *bool
}

func (s candidateScanner) Scan(dest ...any) error {
	return s.ciScanner.Scan(append(dest, s.names, s.layers)...)
}

// batch recomputes the next CIs after b.after in tx: it locks them (a CI a
// write holds is skipped: that write computes its values), computes their
// values with one read of the inputs and writes the changed ones with one
// statement for the values and one for the provenance, without a version
// step. It returns the CIs read, the CIs changed and the last id.
func (j *ComputedRefresh) batch(ctx context.Context, tx pgx.Tx, orgID string, b *refreshBatch) (n, changedCIs int, last string, err error) {
	var onlyType, onlyKey *string
	if b.only != nil && b.only.CITypeID != "" {
		onlyType, onlyKey = &b.only.CITypeID, &b.only.Key
	}
	rows, err := tx.Query(ctx, fmt.Sprintf(`SELECT %s,
			COALESCE((SELECT array_agg(v.field_name ORDER BY v.field_name) FROM ci_field_value v
				WHERE v.ci_id = ci.id AND v.discovered_source = 'computed' AND v.override_at IS NULL), '{}'),
			EXISTS (SELECT 1 FROM ci_template t WHERE t.ci_id = ci.id)
				OR EXISTS (SELECT 1 FROM ci_instance_field_definition d WHERE d.ci_id = ci.id)
		FROM ci
		WHERE organization_id = $1 AND deleted_at IS NULL AND id > $2::uuid
		  AND (ci_type_id = ANY($3::uuid[])
		       OR EXISTS (SELECT 1 FROM ci_field_value v WHERE v.ci_id = ci.id AND v.discovered_source = 'computed')
		       OR EXISTS (SELECT 1 FROM ci_template t WHERE t.ci_id = ci.id)
		       OR EXISTS (SELECT 1 FROM ci_instance_field_definition d WHERE d.ci_id = ci.id))
		  AND ($4::uuid IS NULL OR ci_type_id IN (SELECT s.id FROM ci_type_subtree($4::uuid) s)
		       OR EXISTS (SELECT 1 FROM ci_field_value v WHERE v.ci_id = ci.id AND v.field_name = $5
		                  AND v.discovered_source = 'computed'))
		ORDER BY id
		LIMIT $6
		FOR NO KEY UPDATE OF ci SKIP LOCKED`, ciSelectColumns), orgID, b.after, b.types, onlyType, onlyKey, b.limit)
	if err != nil {
		return 0, 0, "", fmt.Errorf("read cis for computed attributes: %w", err)
	}
	var candidates []refreshCandidate
	for rows.Next() {
		c := refreshCandidate{}
		item, serr := scanCI(candidateScanner{ciScanner: rows, names: &c.stale, layers: &c.layers})
		if serr != nil {
			rows.Close()
			return 0, 0, "", fmt.Errorf("scan ci for computed attributes: %w", serr)
		}
		c.item = item
		candidates = append(candidates, c)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return 0, 0, "", fmt.Errorf("read cis for computed attributes: %w", err)
	}
	if len(candidates) == 0 {
		return 0, 0, "", nil
	}
	last = candidates[len(candidates)-1].item.ID

	// The definitions: of the type, or resolved for a CI with layers of
	// its own; the inputs with one read for the batch.
	var all []fieldmeta.FieldDefinition
	var assets []string
	for i := range candidates {
		c := &candidates[i]
		if c.layers {
			if c.defs, err = j.definitions(ctx, tx, orgID, c.item.CITypeID, c.item.ID); err != nil {
				return 0, 0, "", err
			}
		} else {
			c.defs = b.byType[strings.ToLower(c.item.CITypeID)]
		}
		if len(c.defs) > 0 {
			all = append(all, c.defs...)
			assets = append(assets, c.item.AssetID)
		}
	}
	env := &computeEnv{}
	if len(all) > 0 {
		if env, err = loadComputeEnv(ctx, tx, orgID, all, assets); err != nil {
			return 0, 0, "", err
		}
	}
	env.now = j.now().UTC()

	var ids, sets, removals []string
	var provenance []computedValue
	for i := range candidates {
		c := &candidates[i]
		changed := refreshChanges(env, c)
		if len(changed) == 0 {
			continue
		}
		set, removed := map[string]any{}, []string{}
		for key, value := range changed {
			if value == nil {
				removed = append(removed, key)
			} else {
				set[key] = value
			}
		}
		rawSet, merr := json.Marshal(set)
		if merr != nil {
			return 0, 0, "", fmt.Errorf("encode computed attributes: %w", merr)
		}
		rawRemoved, merr := json.Marshal(removed)
		if merr != nil {
			return 0, 0, "", fmt.Errorf("encode computed attributes: %w", merr)
		}
		ids, sets, removals = append(ids, c.item.ID), append(sets, string(rawSet)), append(removals, string(rawRemoved))
		provenance = append(provenance, computedValuesOf(c.item.ID, changed)...)
	}
	if len(ids) == 0 {
		return len(candidates), 0, last, nil
	}
	// The values without a version step (as the writes below rank 92).
	if _, err = tx.Exec(ctx, `UPDATE ci SET attributes =
			(COALESCE(ci.attributes, '{}'::jsonb) - ARRAY(SELECT jsonb_array_elements_text(u.removed::jsonb))) || u.set::jsonb
		FROM unnest($2::uuid[], $3::text[], $4::text[]) AS u(id, set, removed)
		WHERE ci.id = u.id AND ci.organization_id = $1`, orgID, ids, sets, removals); err != nil {
		return 0, 0, "", fmt.Errorf("write computed attributes: %w", err)
	}
	if perr := writeComputedProvenance(ctx, tx, orgID, env.now, provenance); perr != nil {
		return 0, 0, "", perr
	}
	return len(candidates), len(ids), last, nil
}

// refreshChanges computes the values of a candidate and returns the ones
// that differ from the stored ones (nil: the value goes); a computed value
// whose key has no computed definition any more goes with its provenance.
func refreshChanges(env *computeEnv, c *refreshCandidate) map[string]any {
	results := map[string]any{}
	if len(c.defs) > 0 {
		results = env.evaluate(c.defs, withTyped(c.item.Attributes, typedValues(c.item)), c.item.AssetID)
	}
	var gone []string
	for _, key := range c.stale {
		if _, defined := results[key]; !defined {
			results[key] = nil
			gone = append(gone, key)
		}
	}
	changed := computedChanges(c.item.Attributes, results)
	for _, key := range gone {
		// The provenance row goes even when the attribute is gone already.
		changed[key] = nil
	}
	return changed
}
