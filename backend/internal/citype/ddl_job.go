package citype

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
)

// IndexJobKind is the job that brings the index of one attribute
// definition in line with its searchable/unique flags (DB-04, MET-15).
const IndexJobKind = "attribute.index"

// RebuildJobKind rebuilds the index of a definition that was built over
// attributes ->> key before migration 000162 (WP-435); the migration queues
// it, Ensure serves it like IndexJobKind. A server of an earlier version
// does not know the kind and leaves the jobs to the new one: an
// attribute.index job would be claimed by it during a rolling update and
// end without a rebuild.
const RebuildJobKind = "attribute.index.rebuild"

// KeyJobKind moves a definition the index of its key serves (UsesKeyIndex)
// from the index of the definition to the index of the key (migration
// 000164, WP-607); the migration queues it, Ensure serves it like
// IndexJobKind. A server of an earlier version does not know the kind and
// leaves the jobs to the new one: it would end an attribute.index or
// attribute.index.rebuild job with the index of the definition in place.
const KeyJobKind = "attribute.index.key"

// IndexPrefix starts the name of every attribute index (DB-04).
const IndexPrefix = "idx_attr_"

// KeyIndexPrefix starts the name of every index of a key (KeyIndexName);
// the name of a definition's index continues with hex digits instead.
const KeyIndexPrefix = IndexPrefix + "key"

// RebuildSuffix names the index that replaces an index built before
// migration 000162 until the old one is dropped (Ensure).
const RebuildSuffix = "_rebuild"

// maxIdentifier is the length of a PostgreSQL name in bytes (NAMEDATALEN-1).
const maxIdentifier = 63

// KeyIndexName is the index of an attribute key (E-73, supplement of
// 2026-10-26): idx_attr_key_<key> when the key consists of [a-z0-9_] and
// the name fits into a PostgreSQL name, else idx_attr_keyh_ with the first
// 32 hex digits of the SHA-256 of the key. The name depends on the key
// only, so every organization and every server finds the same index.
func KeyIndexName(key string) string {
	if name := KeyIndexPrefix + "_" + key; len(name) <= maxIdentifier && plainKey(key) {
		return name
	}
	sum := sha256.Sum256([]byte(key))
	return KeyIndexPrefix + "h_" + hex.EncodeToString(sum[:16])
}

// plainKey reports whether key is not empty and consists of [a-z0-9_].
func plainKey(key string) bool {
	for _, c := range key {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return false
		}
	}
	return key != ""
}

// UsesKeyIndex reports whether the index of its key serves d (E-73,
// supplement of 2026-10-26, WP-607): a searchable definition of an
// organization that is not unique and has no CI type. The index of a key
// holds the CIs of every organization, (organization_id, value) without a
// predicate on the organization, so the indexes on ci grow with the keys
// and not with the organizations that use one; the tenants stay apart by
// row level security and the organization the attribute filter names. A
// unique definition needs an index of its own for the uniqueness in its
// organization (and client), a type definition one with the predicate on
// its type.
func UsesKeyIndex(d *AttributeDefinition) bool {
	return d != nil && d.OrganizationID != "" && d.Searchable && !d.Unique && d.CITypeID == ""
}

// IndexName is the index of a definition after E-04: idx_attr_ with the
// definition id as 32 hex characters, unique indexes with the suffix _u.
// The name survives a renamed key and finds orphaned or invalid indexes by
// the definition.
func IndexName(defID string, unique bool) string {
	name := IndexPrefix + strings.ToLower(strings.ReplaceAll(defID, "-", ""))
	if unique {
		name += "_u"
	}
	return name
}

// IndexScheduler starts the index job of a definition whose flags changed
// and the rebuild a user asks for (POST /attribute-definitions/{id}/reindex,
// WP-796).
type IndexScheduler interface {
	ScheduleIndex(ctx context.Context, orgID, defID string) error
	// Reindex queues an attribute.index job of the definition for
	// createdBy unless a job of the definition (IndexJobKinds) is queued
	// or running; created reports whether it queued one. Concurrent calls
	// for the same definition leave at most one open job.
	Reindex(ctx context.Context, orgID, defID, createdBy string) (created bool, err error)
}

// IndexJobKinds are the job kinds that serve the index of a definition,
// each with params.definition_id; the index status (DB-04, WP-796) is the
// one of the newest of them.
var IndexJobKinds = []string{IndexJobKind, RebuildJobKind, KeyJobKind}

// indexRelevant reports whether a change from before to after (nil for a
// missing definition) touches the attribute index.
func indexRelevant(before, after *AttributeDefinition) bool {
	indexed := func(d *AttributeDefinition) bool { return d != nil && (d.Searchable || d.Unique) }
	if !indexed(before) && !indexed(after) {
		return false
	}
	if before == nil || after == nil {
		return true
	}
	// inheritable decides whether the index of a type definition covers
	// the type or its subtree (WP-708).
	return before.Searchable != after.Searchable || before.Unique != after.Unique ||
		(after.Unique && before.UniqueScope != after.UniqueScope) ||
		(after.CITypeID != "" && before.Inheritable != after.Inheritable)
}

// ValueExpression is the attribute value as the indexes hold it and the
// attribute filter of the CI list compares it (WP-435, E-73):
// reticora_attr_text is attributes ->> key, but leakproof and not inlinable,
// so the comparison is an index condition under row level security.
// key is an SQL expression of type text.
func ValueExpression(key string) string {
	return "reticora_attr_text(attributes, " + key + ")"
}

// KeyExpression is ValueExpression over key as a literal, the expression
// of the attribute indexes. The attribute filter of the CI list compares
// it (WP-607): a parameter in place of the key matches the expression of an
// index in a custom plan only, never in a generic one.
func KeyExpression(key string) string {
	return ValueExpression(quoteLiteral(key))
}

// IndexStatement is the CREATE INDEX statement of a definition's own index;
// "" when the definition needs none: it is neither searchable nor unique,
// or the index of its key serves it (UsesKeyIndex, KeyIndexStatement). A
// unique index holds the value once per organization, or per organization
// and client (scope client, a CI without client counts as one client);
// soft-deleted CIs and CIs without the attribute are left out (MET-15). A
// type definition covers the CIs of its type, with inheritable those of
// the subtree of its type (MET-06, WP-708), a global definition every CI
// of the organization; system definitions cover every organization.
//
// subtree are the types of the subtree (IndexTypes) as the organization
// sees them: an index predicate allows no subquery, so they stand in it as
// literals, ci_type_id IN (…). Without them, or without inheritable, the
// predicate is ci_type_id = '<type>'.
func IndexStatement(d *AttributeDefinition, subtree ...string) string {
	if d == nil || (!d.Searchable && !d.Unique) || UsesKeyIndex(d) {
		return ""
	}
	return indexStatement(d, IndexName(d.ID, d.Unique), subtree...)
}

// IndexTypes are the CI types the predicate of a definition's index
// names, sorted: none for a global definition, the type for one without
// inheritable, else the subtree (without it the type alone).
func IndexTypes(d *AttributeDefinition, subtree ...string) []string {
	if d == nil || d.CITypeID == "" {
		return nil
	}
	types := []string{strings.ToLower(d.CITypeID)}
	if d.Inheritable {
		for _, t := range subtree {
			if t = strings.ToLower(t); !slices.Contains(types, t) {
				types = append(types, t)
			}
		}
	}
	slices.Sort(types)
	return types
}

// KeyIndexStatement is the CREATE INDEX statement of the index of key
// (UsesKeyIndex): the organization and the attribute value of every CI that
// is not soft-deleted and holds the attribute, in every organization.
func KeyIndexStatement(key string) string {
	value := KeyExpression(key)
	return fmt.Sprintf("CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON ci (organization_id, %s) WHERE deleted_at IS NULL AND %s IS NOT NULL",
		KeyIndexName(key), value, value)
}

// indexStatement is IndexStatement with the index named name.
func indexStatement(d *AttributeDefinition, name string, subtree ...string) string {
	value := ValueExpression(quoteLiteral(d.Key))
	columns := "organization_id, " + value
	nulls := ""
	if d.Unique && d.UniqueScope == "client" {
		columns = "organization_id, client_id, " + value
		nulls = " NULLS NOT DISTINCT"
	}
	where := []string{"deleted_at IS NULL", value + " IS NOT NULL"}
	if d.OrganizationID != "" {
		where = append(where, "organization_id = "+quoteLiteral(d.OrganizationID)+"::uuid")
	}
	if types := IndexTypes(d, subtree...); len(types) == 1 {
		where = append(where, "ci_type_id = "+quoteLiteral(types[0])+"::uuid")
	} else if len(types) > 1 {
		literals := make([]string, len(types))
		for i, t := range types {
			literals[i] = quoteLiteral(t) + "::uuid"
		}
		where = append(where, "ci_type_id IN ("+strings.Join(literals, ", ")+")")
	}
	unique := ""
	if d.Unique {
		unique = "UNIQUE "
	}
	return fmt.Sprintf("CREATE %sINDEX CONCURRENTLY IF NOT EXISTS %s ON ci (%s)%s WHERE %s",
		unique, name, columns, nulls, strings.Join(where, " AND "))
}

// quoteLiteral quotes s as an SQL string literal that means the same under
// every setting of standard_conforming_strings: the quote is doubled, and a
// value with a backslash is an escape string constant (E'…') with the
// backslashes doubled as well. A key is free text and reaches the index DDL
// and the filter SQL of the CI list this way (KeyExpression).
func quoteLiteral(s string) string {
	q := strings.ReplaceAll(s, "'", "''")
	if strings.Contains(s, `\`) {
		return "E'" + strings.ReplaceAll(q, `\`, `\\`) + "'"
	}
	return "'" + q + "'"
}

// indexColumns is the column count of a definition's index; a changed
// unique scope changes it and rebuilds the index (key, type and
// organization of a definition are immutable, unique is in the name).
func indexColumns(d *AttributeDefinition) int {
	if d.Unique && d.UniqueScope == "client" {
		return 3
	}
	return 2
}

// ErrDuplicateValues: a unique index cannot be built because CIs already
// share a value.
var ErrDuplicateValues = errors.New("CIs share a value of the unique attribute")

// DDLJob runs the attribute index jobs (DB-04, CH19): it creates the
// indexes concurrently, outside a transaction, on a dedicated connection
// that switches to the owner role for the DDL (role contract of WP-064),
// and drops failed (invalid) and no longer wanted indexes.
type DDLJob struct {
	pool *pgxpool.Pool
	jobs *jobs.Service
	// OwnerRole owns the tables; the DDL runs after SET ROLE to it.
	OwnerRole string
	// Batch bounds the jobs of one run.
	Batch int
}

// NewDDLJob creates the job runner and registers its job kinds.
func NewDDLJob(pool *pgxpool.Pool, svc *jobs.Service) *DDLJob {
	svc.Register(jobs.Kind{Name: IndexJobKind, MaxAttempts: 3, Timeout: time.Hour})
	svc.Register(jobs.Kind{Name: RebuildJobKind, MaxAttempts: 3, Timeout: time.Hour})
	svc.Register(jobs.Kind{Name: KeyJobKind, MaxAttempts: 3, Timeout: time.Hour})
	return &DDLJob{pool: pool, jobs: svc, OwnerRole: database.DefaultOwnerRole, Batch: 50}
}

type indexJobParams struct {
	DefinitionID string `json:"definition_id"`
}

// ScheduleIndex implements IndexScheduler.
func (j *DDLJob) ScheduleIndex(ctx context.Context, orgID, defID string) error {
	_, _, err := j.jobs.Enqueue(ctx, orgID, IndexJobKind, indexJobParams{DefinitionID: defID}, jobs.EnqueueOptions{})
	return err
}

// reindexLock serializes the rebuild requests of a definition (Reindex);
// it is not the lock of Ensure, which a running job holds for the build.
const reindexLock = IndexJobKind + ".reindex/"

// Reindex implements IndexScheduler. The check for an open job and the
// insert of the new one run under a transaction advisory lock on the
// definition, which is held until the new job is committed: a concurrent
// call waits and finds that job open. The job carries the scope snapshot of
// the request (jobs.Service.Enqueue); the check reads the jobs
// organization-wide, so a job a principal of another scope queued counts as
// well.
func (j *DDLJob) Reindex(ctx context.Context, orgID, defID, createdBy string) (bool, error) {
	if !isUUIDText(defID) {
		return false, fmt.Errorf("%w: %q", ErrAttributeNotFound, defID)
	}
	created := false
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, j.pool, &scope, func(txCtx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(txCtx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, reindexLock+strings.ToLower(defID)); err != nil {
			return fmt.Errorf("lock the rebuild of attribute definition %s: %w", defID, err)
		}
		var open bool
		if err := tx.QueryRow(txCtx, `SELECT EXISTS (SELECT 1 FROM job WHERE organization_id = $1::uuid
			AND kind = ANY($2::text[]) AND lower(params->>'definition_id') = $3 AND status IN ('queued', 'running'))`,
			orgID, IndexJobKinds, strings.ToLower(defID)).Scan(&open); err != nil {
			return fmt.Errorf("read the open index jobs of attribute definition %s: %w", defID, err)
		}
		if open {
			return nil
		}
		// The job is committed by its own transaction (the context of the
		// request: its scope snapshot), before this one releases the lock.
		if _, _, err := j.jobs.Enqueue(ctx, orgID, IndexJobKind, indexJobParams{DefinitionID: strings.ToLower(defID)},
			jobs.EnqueueOptions{CreatedBy: createdBy}); err != nil {
			return fmt.Errorf("queue the index job of attribute definition %s: %w", defID, err)
		}
		created = true
		return nil
	})
	return created, err
}

// Index states of a definition (AttributeIndexStatus, DB-04): the status of
// its newest index job, IndexStateNone for a definition that needs no
// index and has no job.
const (
	IndexStateNone      = "none"
	IndexStateQueued    = string(jobs.StatusQueued)
	IndexStateRunning   = string(jobs.StatusRunning)
	IndexStateSucceeded = string(jobs.StatusSucceeded)
	IndexStateFailed    = string(jobs.StatusFailed)
	IndexStateCancelled = string(jobs.StatusCancelled)
)

// IndexMissing is the error of an indexed definition of the
// organization without index job and without valid index (its job could
// not be queued).
const IndexMissing = "index missing"

// AttributeIndexStatus is the index status of a definition (DB-04, UI-11,
// WP-796): the state, attempts and error of its newest index job
// (IndexJobKinds), the index that serves it and whether that index is
// valid (pg_index.indisvalid).
type AttributeIndexStatus struct {
	State       string     `json:"state"`
	IndexName   string     `json:"index_name"`
	Valid       bool       `json:"valid"`
	Error       string     `json:"error,omitempty"`
	Attempts    int        `json:"attempts"`
	MaxAttempts int        `json:"max_attempts"`
	JobID       string     `json:"job_id,omitempty"`
	UpdatedAt   *time.Time `json:"updated_at,omitempty"`
}

// ServingIndexName is the index that serves d: the index of its key for a
// definition UsesKeyIndex is true for (KeyIndexName, WP-607), else its own
// (IndexName).
func ServingIndexName(d *AttributeDefinition) string {
	if UsesKeyIndex(d) {
		return KeyIndexName(d.Key)
	}
	return IndexName(d.ID, d.Unique)
}

// indexJobState is the newest index job of a definition, as
// indexStatusesTx reads it.
type indexJobState struct {
	id, status, err      string
	attempts, maxAttempt int
	updatedAt            *time.Time
}

// indexStatus derives the status of d from the validity of the index that
// serves it and its newest index job (nil: none). A system definition is
// indexed by its migration (Ensure skips it) and has no job of the
// organization; a definition of the organization with flags but without
// job (queueing failed) reports the index as it finds it.
func indexStatus(d *AttributeDefinition, valid bool, job *indexJobState) AttributeIndexStatus {
	s := AttributeIndexStatus{IndexName: ServingIndexName(d), Valid: valid}
	indexed := d.Searchable || d.Unique
	switch {
	case d.IsSystem && indexed:
		s.State = IndexStateSucceeded
	case d.IsSystem:
		s.State = IndexStateNone
	case job != nil:
		s.State, s.Error, s.Attempts, s.MaxAttempts = job.status, job.err, job.attempts, job.maxAttempt
		if strings.HasPrefix(s.Error, ErrDuplicateValues.Error()) {
			// The detail names the shared value of CIs the reader may not
			// see; the status holds no object data.
			s.Error = ErrDuplicateValues.Error()
		}
		s.JobID, s.UpdatedAt = job.id, job.updatedAt
	case !indexed:
		s.State = IndexStateNone
	case valid:
		s.State = IndexStateSucceeded
	default:
		s.State, s.Error = IndexStateFailed, IndexMissing
	}
	return s
}

// indexStatuses returns the index status of every definition of defs by
// its id, read with one query: the newest job of IndexJobKinds of the
// organization with params.definition_id of the definition, and the
// validity of the index that serves it. The jobs are read
// organization-wide: job_scope_select (migration 000154) hides the jobs of
// org-wide creators from a scoped reader, and the status holds no object
// data. The jobs are matched by params, not by ref_table/ref_id
// (uq_job_ref admits one job per reference) or an idempotency key.
func indexStatuses(ctx context.Context, pool *pgxpool.Pool, orgID string, defs []AttributeDefinition) (map[string]AttributeIndexStatus, error) {
	out := make(map[string]AttributeIndexStatus, len(defs))
	if len(defs) == 0 {
		return out, nil
	}
	ids, names := make([]string, len(defs)), make([]string, len(defs))
	for i := range defs {
		ids[i], names[i] = strings.ToLower(defs[i].ID), ServingIndexName(&defs[i])
	}
	valid := make(map[string]bool, len(defs))
	latest := make(map[string]*indexJobState, len(defs))
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH d AS (SELECT * FROM unnest($2::text[], $3::text[]) AS d(id, index_name)),
			latest AS (
				SELECT DISTINCT ON (lower(j.params->>'definition_id')) lower(j.params->>'definition_id') AS def,
					j.id::text AS id, j.status, COALESCE(j.error, '') AS error, j.attempts, j.max_attempts, j.updated_at
				FROM job j
				WHERE j.organization_id = $1::uuid AND j.kind = ANY($4::text[])
				  AND lower(j.params->>'definition_id') = ANY($2::text[])
				ORDER BY lower(j.params->>'definition_id'), j.created_at DESC, j.id DESC)
			SELECT d.id, COALESCE(i.indisvalid, false), l.id IS NOT NULL, COALESCE(l.id, ''), COALESCE(l.status, ''),
				COALESCE(l.error, ''), COALESCE(l.attempts, 0), COALESCE(l.max_attempts, 0), l.updated_at
			FROM d
			LEFT JOIN latest l ON l.def = d.id
			LEFT JOIN pg_class c ON c.relname = d.index_name AND c.relnamespace = 'public'::regnamespace
			LEFT JOIN pg_index i ON i.indexrelid = c.oid`, orgID, ids, names, IndexJobKinds)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var isValid, hasJob bool
			job := &indexJobState{}
			if serr := rows.Scan(&id, &isValid, &hasJob, &job.id, &job.status, &job.err, &job.attempts, &job.maxAttempt,
				&job.updatedAt); serr != nil {
				return serr
			}
			valid[id] = isValid
			if hasJob {
				latest[id] = job
			}
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read attribute index status: %w", err)
	}
	for i := range defs {
		id := strings.ToLower(defs[i].ID)
		out[defs[i].ID] = indexStatus(&defs[i], valid[id], latest[id])
	}
	return out, nil
}

// Schedule runs the due index jobs every minute on the scheduler.
func (j *DDLJob) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: IndexJobKind, Spec: "@every 1m", Timeout: time.Hour, Run: func(ctx context.Context) error {
		_, err := j.RunDue(ctx)
		return err
	}}
}

// RunDue claims and runs the due index jobs of every organization and
// returns how many succeeded. A failed job is retried after the backoff
// of its kind; the run itself fails only when the queue cannot be read.
func (j *DDLJob) RunDue(ctx context.Context) (int, error) {
	due, err := j.jobs.Due(ctx, j.Batch, IndexJobKind, RebuildJobKind, KeyJobKind)
	if err != nil {
		return 0, fmt.Errorf("list due index jobs: %w", err)
	}
	done := 0
	for i := range due {
		job, cerr := j.jobs.ClaimJob(ctx, due[i].OrganizationID, due[i].ID)
		if cerr != nil || job == nil {
			continue
		}
		var p indexJobParams
		if err = json.Unmarshal(job.Params, &p); err == nil {
			err = j.Ensure(ctx, job.OrganizationID, p.DefinitionID)
		}
		if err != nil {
			slog.Error("attribute index job failed", "job", job.ID, "definition", p.DefinitionID, "error", err)
			if _, ferr := j.jobs.Fail(ctx, job.OrganizationID, job.ID, err); ferr != nil {
				slog.Error("record failed attribute index job", "job", job.ID, "error", ferr)
			}
			continue
		}
		if _, serr := j.jobs.Succeed(ctx, job.OrganizationID, job.ID, map[string]string{"definition_id": p.DefinitionID}); serr != nil {
			slog.Error("record attribute index job", "job", job.ID, "error", serr)
			continue
		}
		done++
	}
	return done, nil
}

// definition reads the definition in the organization; nil when it was
// deleted. subtree are the types of the subtree of a type definition with
// inheritable as the organization sees them (IndexTypes, WP-708).
func (j *DDLJob) definition(ctx context.Context, orgID, defID string) (out *AttributeDefinition, subtree []string, err error) {
	scope := database.OrgWideScope(orgID, "")
	err = database.WithTenant(ctx, j.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		d, gerr := getAttributeTx(ctx, tx, defID, false)
		if errors.Is(gerr, ErrAttributeNotFound) {
			return nil
		}
		if gerr != nil {
			return gerr
		}
		out = d
		if d.CITypeID == "" || !d.Inheritable {
			return nil
		}
		rows, qerr := tx.Query(ctx, `SELECT s.id::text FROM ci_type_subtree($1::uuid) s ORDER BY 1`, d.CITypeID)
		if qerr != nil {
			return fmt.Errorf("read ci type subtree: %w", qerr)
		}
		subtree, qerr = pgx.CollectRows(rows, pgx.RowTo[string])
		return qerr
	})
	return out, subtree, err
}

// existingIndex is an attribute index found in the catalog.
type existingIndex struct {
	valid   bool
	columns int
	// current: built over reticora_attr_text (ValueExpression); an index
	// built before migration 000162 holds attributes ->> key, which the
	// application role cannot use under row level security.
	current bool
	// types are the CI types its predicate names (IndexTypes), sorted.
	types []string
}

// predicateUUID finds the UUID literals of an index predicate.
var predicateUUID = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)

// predicateTypeClause is the condition on ci_type_id at the end of an
// index predicate as pg_get_expr prints it: = '<id>'::uuid, or = ANY
// (ARRAY[…]) for IN (…). The key is a literal before it, so a key that
// reads like the condition never matches here.
var predicateTypeClause = regexp.MustCompile(`\(ci_type_id = ('[^']*'::uuid|ANY \(ARRAY\[[^\]]*\]\))\)\)?$`)

// predicateTypes are the CI types the predicate of an index names
// (pg_get_expr of indpred): the UUID literals of the condition on
// ci_type_id, which indexStatement puts last; none without it.
func predicateTypes(predicate string) []string {
	m := predicateTypeClause.FindStringSubmatch(predicate)
	if m == nil {
		return nil
	}
	types := predicateUUID.FindAllString(strings.ToLower(m[1]), -1)
	slices.Sort(types)
	return slices.Compact(types)
}

// lockPoll is the interval in which Ensure tries the lock of a definition
// or of the index of a key.
const lockPoll = time.Second

// Ensure brings the indexes of a definition in line with its flags: the
// wanted index exists, is valid and was built from the current statement;
// the other variant (unique or not) and the indexes of a deleted
// definition are dropped. A build that fails leaves an invalid index,
// which is dropped before the error is returned.
//
// A definition the index of its key serves (UsesKeyIndex, WP-607) has no
// index of its own: Ensure builds the index of the key unless it exists and
// drops the definition's own index only then, so the filter keeps an index
// throughout (also for a definition whose own index migration 000164
// replaces). The index of a key goes when no definition of any organization
// uses the key any more: after the change of a definition that no longer
// uses it (unique now, not searchable), and after the deletion of any
// definition, whose key is gone with it, for every index of a key.
//
// An index built before migration 000162 is rebuilt once (WP-435): the new
// index is built concurrently next to it (RebuildSuffix), the old one is
// dropped only after the new one is valid, then the new one takes its
// name. Every step may be interrupted and the job run again: there is no
// state in which the old index is gone and the new one missing or
// invalid, so the filter and a unique attribute keep a valid index. Runs
// for the same definition (two jobs, two servers) take turns, as do runs
// that build or drop the index of the same key.
func (j *DDLJob) Ensure(ctx context.Context, orgID, defID string) error {
	if !isUUIDText(defID) {
		return fmt.Errorf("%w: %q", ErrAttributeNotFound, defID)
	}
	d, subtree, err := j.definition(ctx, orgID, defID)
	if err != nil {
		return fmt.Errorf("read attribute definition: %w", err)
	}
	if d != nil && d.OrganizationID != orgID {
		return nil // a system definition is indexed by its migration
	}

	conn, err := j.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	// The role switch stays on the connection, so it never returns to the
	// pool.
	pc := conn.Hijack()
	defer func() { _ = pc.Close(context.WithoutCancel(ctx)) }()
	if _, err = pc.Exec(ctx, "SET ROLE "+pgx.Identifier{j.OwnerRole}.Sanitize()); err != nil {
		return fmt.Errorf("switch to the owner role: %w", err)
	}
	if lerr := lockDefinition(ctx, pc, defID); lerr != nil {
		return lerr
	}
	if UsesKeyIndex(d) {
		if kerr := ensureKeyIndex(ctx, pc, d.Key); kerr != nil {
			return kerr
		}
	}
	if derr := ensureDefinitionIndex(ctx, pc, d, defID, subtree); derr != nil {
		return derr
	}
	switch {
	case d == nil:
		// The indexes of other organizations' keys: their names (the keys)
		// stay out of the error, which the job of this one records.
		if rerr := j.releaseKeyIndexes(ctx, pc, ""); rerr != nil {
			slog.Error("drop unused attribute key indexes", "definition", defID, "error", rerr)
			return errors.New("drop unused attribute key indexes (details in the server log)")
		}
		return nil
	case !UsesKeyIndex(d) && d.CITypeID == "":
		// A type definition never used the index of its key.
		return j.releaseKeyIndexes(ctx, pc, KeyIndexName(d.Key))
	}
	return nil
}

// ensureDefinitionIndex is the part of Ensure for the definition's own
// index (IndexStatement): d is nil for a deleted definition, subtree the
// types of the subtree of its type (IndexTypes). An index whose predicate
// names other types (the subtree changed, inheritable changed, WP-708) is
// rebuilt like one built before migration 000162.
func ensureDefinitionIndex(ctx context.Context, pc *pgx.Conn, d *AttributeDefinition, defID string, subtree []string) error {
	stmt, wanted, columns := IndexStatement(d, subtree...), "", 0
	var types []string
	if stmt != "" {
		wanted, columns, types = IndexName(defID, d.Unique), indexColumns(d), IndexTypes(d, subtree...)
	}
	// fresh: built from the current statement.
	fresh := func(idx existingIndex) bool { return idx.current && slices.Equal(idx.types, types) }
	kept := map[string]existingIndex{}
	for _, name := range []string{IndexName(defID, false), IndexName(defID, true)} {
		for _, n := range []string{name, name + RebuildSuffix} {
			idx, found, lerr := lookupIndex(ctx, pc, n)
			if lerr != nil {
				return lerr
			}
			if !found {
				continue
			}
			// Kept: the wanted index, one over ->> as well until its
			// rebuild is valid, and a valid rebuild of it.
			if name == wanted && idx.valid && idx.columns == columns && (n == name || fresh(idx)) {
				kept[n] = idx
				continue
			}
			if derr := dropIndex(ctx, pc, n); derr != nil {
				return derr
			}
		}
	}
	if stmt == "" {
		return nil
	}
	rebuild := wanted + RebuildSuffix
	old, hasOld := kept[wanted]
	_, hasNew := kept[rebuild]
	switch {
	case hasOld && fresh(old):
		if hasNew {
			return dropIndex(ctx, pc, rebuild)
		}
		return nil
	case !hasOld && !hasNew:
		return build(ctx, pc, d.Key, stmt, wanted)
	}
	// The wanted index holds attributes ->> key or names other types, or
	// it is gone after a rebuild was valid: build the new index (unless it
	// is), drop the old one, rename.
	if !hasNew {
		if berr := build(ctx, pc, d.Key, indexStatement(d, rebuild, subtree...), rebuild); berr != nil {
			return berr
		}
	}
	if hasOld {
		if derr := dropIndex(ctx, pc, wanted); derr != nil {
			return derr
		}
	}
	if _, err := pc.Exec(ctx, "ALTER INDEX "+pgx.Identifier{rebuild}.Sanitize()+" RENAME TO "+pgx.Identifier{wanted}.Sanitize()); err != nil {
		return fmt.Errorf("rename attribute index %s: %w", rebuild, err)
	}
	return nil
}

// build runs the CREATE INDEX statement stmt for the index name of the
// attribute key. The failed build leaves an invalid index behind, which is
// dropped.
func build(ctx context.Context, conn *pgx.Conn, key, stmt, name string) error {
	if _, err := conn.Exec(ctx, stmt); err != nil {
		if derr := dropIndex(ctx, conn, name); derr != nil {
			slog.Error("drop failed attribute index", "index", name, "error", derr)
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return fmt.Errorf("%w %q: %s", ErrDuplicateValues, key, pgErr.Detail)
		}
		return fmt.Errorf("create attribute index %s: %w", name, err)
	}
	return nil
}

// lockDefinition takes a session advisory lock on the definition; it ends
// with the connection. The lock is tried in a loop instead of waited for:
// a session that waits inside a statement holds a snapshot, for which
// CREATE INDEX CONCURRENTLY of the holder would wait.
func lockDefinition(ctx context.Context, conn *pgx.Conn, defID string) error {
	if err := advisoryLock(ctx, conn, IndexJobKind+"/"+strings.ToLower(defID)); err != nil {
		return fmt.Errorf("lock attribute definition %s: %w", defID, err)
	}
	return nil
}

// lockIndex takes a session advisory lock on the index name of a key, like
// lockDefinition; unlockIndex releases it.
func lockIndex(ctx context.Context, conn *pgx.Conn, name string) error {
	if err := advisoryLock(ctx, conn, IndexJobKind+"/"+name); err != nil {
		return fmt.Errorf("lock attribute index %s: %w", name, err)
	}
	return nil
}

func unlockIndex(ctx context.Context, conn *pgx.Conn, name string) {
	if _, err := conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock(hashtextextended($1, 0))`, IndexJobKind+"/"+name); err != nil {
		slog.Error("unlock attribute index", "index", name, "error", err)
	}
}

// advisoryLock tries the session advisory lock of key every lockPoll until
// it holds it or ctx ends.
func advisoryLock(ctx context.Context, conn *pgx.Conn, key string) error {
	for {
		var locked bool
		if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock(hashtextextended($1, 0))`, key).Scan(&locked); err != nil {
			return err
		}
		if locked {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}

// ensureKeyIndex builds the index of key (KeyIndexStatement) unless it
// exists and is valid; an invalid one (a build that died) is dropped
// first. The index name is locked meanwhile, so the run never overlaps
// with one that drops the index (releaseKeyIndexes).
func ensureKeyIndex(ctx context.Context, conn *pgx.Conn, key string) error {
	name := KeyIndexName(key)
	if err := lockIndex(ctx, conn, name); err != nil {
		return err
	}
	defer unlockIndex(ctx, conn, name)
	idx, found, err := lookupIndex(ctx, conn, name)
	if err != nil {
		return err
	}
	if found && idx.valid && idx.current && idx.columns == 2 {
		return nil
	}
	if found {
		if derr := dropIndex(ctx, conn, name); derr != nil {
			return derr
		}
	}
	return build(ctx, conn, key, KeyIndexStatement(key), name)
}

// releaseKeyIndexes drops the indexes of keys that no definition uses any
// more (UsesKeyIndex): the index name, or every index of a key on ci when
// name is "" (the key of a deleted definition is unknown). The indexes are
// locked in the order of their names (two runs never wait for each other
// in a circle) before the definitions are read: a run that built the index
// for a definition created meanwhile ran after its commit, so it either
// finished before and the definition counts, or it builds the index again
// after this run.
func (j *DDLJob) releaseKeyIndexes(ctx context.Context, conn *pgx.Conn, name string) error {
	rows, err := conn.Query(ctx, `
		SELECT c.relname::text FROM pg_index i JOIN pg_class c ON c.oid = i.indexrelid
		WHERE i.indrelid = 'public.ci'::regclass AND starts_with(c.relname::text, $1) AND ($2 = '' OR c.relname = $2)
		ORDER BY 1`, KeyIndexPrefix, name)
	if err != nil {
		return fmt.Errorf("list attribute key indexes: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("list attribute key indexes: %w", err)
	}
	if len(names) == 0 {
		return nil
	}
	for i, n := range names {
		if lerr := lockIndex(ctx, conn, n); lerr != nil {
			for _, held := range names[:i] {
				unlockIndex(ctx, conn, held)
			}
			return lerr
		}
	}
	defer func() {
		for _, n := range names {
			unlockIndex(ctx, conn, n)
		}
	}()
	used, err := j.keyIndexesInUse(ctx, names)
	if err != nil {
		return err
	}
	for _, n := range names {
		if used[n] {
			continue
		}
		if derr := dropIndex(ctx, conn, n); derr != nil {
			return derr
		}
	}
	return nil
}

// keyIndexesInUse returns which of the index names of keys a definition of
// any organization uses (UsesKeyIndex). The policies of
// attribute_definition show one organization at a time, so the
// organizations are listed in a system transaction and their definitions
// read in their tenant transaction (E-08); the walk ends once every name
// is in use.
func (j *DDLJob) keyIndexesInUse(ctx context.Context, names []string) (map[string]bool, error) {
	var orgs []string
	err := database.WithSystem(ctx, j.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list organizations: %w", err)
	}
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		wanted[n] = true
	}
	used := map[string]bool{}
	for _, org := range orgs {
		scope := database.OrgWideScope(org, "")
		err = database.WithTenant(ctx, j.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			// The definitions UsesKeyIndex is true for.
			rows, qerr := tx.Query(ctx, `SELECT key FROM attribute_definition
				WHERE organization_id = $1::uuid AND searchable AND NOT is_unique AND ci_type_id IS NULL`, org)
			if qerr != nil {
				return qerr
			}
			keys, qerr := pgx.CollectRows(rows, pgx.RowTo[string])
			for _, k := range keys {
				if n := KeyIndexName(k); wanted[n] {
					used[n] = true
				}
			}
			return qerr
		})
		if err != nil {
			// The job of another organization records the error.
			slog.Error("read attribute definitions for the index of a key", "organization", org, "error", err)
			return nil, fmt.Errorf("read the attribute definitions of an organization: %w", err)
		}
		if len(used) == len(wanted) {
			break
		}
	}
	return used, nil
}

func lookupIndex(ctx context.Context, conn *pgx.Conn, name string) (existingIndex, bool, error) {
	var idx existingIndex
	var predicate string
	err := conn.QueryRow(ctx, `
		SELECT i.indisvalid, i.indnatts, EXISTS (
			SELECT 1 FROM pg_depend d
			WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.refclassid = 'pg_proc'::regclass
			  AND d.refobjid = to_regprocedure('public.reticora_attr_text(jsonb, text)')),
			COALESCE(pg_get_expr(i.indpred, i.indrelid), '')
		FROM pg_class c JOIN pg_index i ON i.indexrelid = c.oid
		WHERE c.relname = $1 AND c.relnamespace = 'public'::regnamespace`, name).Scan(&idx.valid, &idx.columns, &idx.current, &predicate)
	if errors.Is(err, pgx.ErrNoRows) {
		return idx, false, nil
	}
	if err != nil {
		return idx, false, fmt.Errorf("look up attribute index %s: %w", name, err)
	}
	idx.types = predicateTypes(predicate)
	return idx, true, nil
}

func dropIndex(ctx context.Context, conn *pgx.Conn, name string) error {
	if _, err := conn.Exec(ctx, "DROP INDEX CONCURRENTLY IF EXISTS "+pgx.Identifier{name}.Sanitize()); err != nil {
		return fmt.Errorf("drop attribute index %s: %w", name, err)
	}
	return nil
}
