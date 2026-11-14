package tenantapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
)

// The purge outside the tables (TLC-06, SEC-05, WP-1155): after the rows
// of WP-1154 the purge shreds the vault of the organization (its data key
// first, then the subjects: from the first commit no value of the
// organization can be decrypted), removes its objects below org/<id>/ in
// every bucket and below its prefixes outside it (CI exports, document
// versions from before 000066) with all versions, delete markers and
// unfinished multipart uploads, its documents of the
// search index, its messages of the event stream and its cache entries.
// Every step is idempotent and counts what it removed; the counts of the
// finished steps lie in organization.settings (org_purge_external), so a
// run after a failure repeats only the open steps with the counts of the
// first. When all of them succeeded operator_audit receives
// org.deletion.external_purged with the counts, and only then the purge
// sets purged.

// purgeExternalKey is the key of the external progress in
// organization.settings.
const purgeExternalKey = "org_purge_external"

// TenantIndexDeleter removes the documents of an organization from the
// search index (search.OpenSearchBackend.DeleteTenant).
type TenantIndexDeleter interface {
	DeleteTenant(ctx context.Context, orgID string) (int64, error)
}

// StreamPurger removes the messages of an organization from the event
// stream (events.Publisher.PurgeOrganization).
type StreamPurger interface {
	PurgeOrganization(ctx context.Context, orgID string) (uint64, error)
}

// CacheInvalidator drops the cache entries of an organization (the
// entitlements, the field permission snapshot).
type CacheInvalidator func(ctx context.Context, orgID string) error

// ObjectPrefix is a prefix of keys of an organization outside org/<id>/
// (the CI exports, document versions from before migration 000066).
type ObjectPrefix struct {
	// Name names the step (objects:<bucket>:<name>).
	Name   string
	Bucket string
	// Prefix returns the prefix of the organization, a directory of keys.
	Prefix func(orgID string) string
}

// ExternalPurge are the stores outside the tables the purge empties; a nil
// store has nothing of the organization (no OpenSearch: the PostgreSQL
// index went with the rows; no NATS: no stream).
type ExternalPurge struct {
	// Blobs and Buckets: the object storage and every bucket with keys of
	// organizations (exports, reticora-documents, RETICORA_S3_BUCKET);
	// Prefixes are the keys of an organization outside org/<id>/.
	Blobs    blob.Store
	Buckets  []string
	Prefixes []ObjectPrefix
	Search   TenantIndexDeleter
	Stream   StreamPurger
	Caches   []CacheInvalidator
}

// WithExternal sets the stores outside the tables (WP-1155); without it the
// purge removes the rows only and keeps the vault.
func (p *Purger) WithExternal(x *ExternalPurge) *Purger {
	p.external = x
	return p
}

// externalProgress is organization.settings.org_purge_external: the counts
// of each finished step.
type externalProgress struct {
	Steps map[string]map[string]int64 `json:"steps"`
	// Recorded marks the entry org.deletion.external_purged written (in
	// its transaction), so a run after a failed finish writes no second.
	Recorded bool `json:"recorded,omitempty"`
}

// externalStep is one step of the external purge with its counts.
type externalStep struct {
	name string
	run  func(ctx context.Context, orgID string) (map[string]int64, error)
}

// steps are the external steps in their order: the vault first.
func (p *Purger) steps() []externalStep {
	x := p.external
	out := []externalStep{{name: "vault", run: p.shredVault}}
	objects := func(bucket string, prefix func(orgID string) string) func(ctx context.Context, orgID string) (map[string]int64, error) {
		return func(ctx context.Context, orgID string) (map[string]int64, error) {
			if x.Blobs == nil {
				return map[string]int64{"objects": 0}, nil
			}
			// A prefix outside org/<id>/ starts with the id: never one that
			// could name the keys of another organization.
			if !api.IsUUID(orgID) {
				return nil, fmt.Errorf("delete the objects of bucket %s: organization id %q is no UUID", bucket, orgID)
			}
			// An error (also more pages than one call reads,
			// blob.ErrListTruncated) leaves the step open: the next run
			// continues with what is left.
			n, err := x.Blobs.DeleteAll(ctx, bucket, prefix(orgID))
			if err != nil {
				return nil, fmt.Errorf("delete the objects of bucket %s: %w", bucket, err)
			}
			return map[string]int64{"objects": int64(n)}, nil
		}
	}
	for _, bucket := range x.Buckets {
		out = append(out, externalStep{name: "objects:" + bucket, run: objects(bucket, blob.OrgPrefix)})
	}
	for _, extra := range x.Prefixes {
		out = append(out, externalStep{name: "objects:" + extra.Bucket + ":" + extra.Name, run: objects(extra.Bucket, extra.Prefix)})
	}
	out = append(out,
		externalStep{name: "search", run: func(ctx context.Context, orgID string) (map[string]int64, error) {
			if x.Search == nil {
				return map[string]int64{"documents": 0}, nil
			}
			n, err := x.Search.DeleteTenant(ctx, orgID)
			if err != nil {
				return nil, fmt.Errorf("delete the search documents: %w", err)
			}
			slog.InfoContext(ctx, "org purge: search documents deleted", "organization_id", orgID, "documents", n)
			return map[string]int64{"documents": n}, nil
		}},
		externalStep{name: "stream", run: func(ctx context.Context, orgID string) (map[string]int64, error) {
			if x.Stream == nil {
				return map[string]int64{"messages": 0}, nil
			}
			n, err := x.Stream.PurgeOrganization(ctx, orgID)
			if err != nil {
				return nil, fmt.Errorf("purge the event stream: %w", err)
			}
			return map[string]int64{"messages": int64(n)}, nil //nolint:gosec // a stream holds far fewer than 2^63 messages
		}},
		externalStep{name: "cache", run: func(ctx context.Context, orgID string) (map[string]int64, error) {
			for i, invalidate := range x.Caches {
				if err := invalidate(ctx, orgID); err != nil {
					return nil, fmt.Errorf("invalidate cache %d: %w", i, err)
				}
			}
			return map[string]int64{"invalidated": int64(len(x.Caches))}, nil
		}},
	)
	return out
}

// purgeExternal runs the open external steps and then writes
// org.deletion.external_purged with the counts of all of them (once); an
// error leaves the organization in purging.
func (p *Purger) purgeExternal(ctx context.Context, orgID string) (map[string]map[string]int64, error) {
	progress, err := p.readExternal(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("org purge: read external progress: %w", err)
	}
	for _, step := range p.steps() {
		if _, done := progress.Steps[step.name]; done {
			continue
		}
		counts, serr := step.run(ctx, orgID)
		if serr != nil {
			return nil, fmt.Errorf("org purge: %s: %w", step.name, serr)
		}
		progress.Steps[step.name] = counts
		if werr := p.saveExternal(ctx, orgID, progress); werr != nil {
			return nil, fmt.Errorf("org purge: save external progress: %w", werr)
		}
	}
	if !progress.Recorded {
		if err := p.recordExternal(ctx, orgID, progress); err != nil {
			return nil, err
		}
	}
	return progress.Steps, nil
}

// shredVault is the crypto-shredding of the organization (AK1): each call
// of pii.ShredOrganizationTx in a transaction of its own; the first removes
// the data key.
func (p *Purger) shredVault(ctx context.Context, orgID string) (map[string]int64, error) {
	var keys, subjects int64
	for range maxShredBatches {
		var res pii.ShredResult
		err := database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			var serr error
			res, serr = pii.ShredOrganizationTx(ctx, tx, orgID, p.batch)
			return serr
		})
		if err != nil {
			return nil, fmt.Errorf("shred the vault: %w", err)
		}
		keys += res.Keys
		subjects += res.Subjects
		if res.Keys == 0 && res.Subjects == 0 {
			return map[string]int64{"keys": keys, "subjects": subjects}, nil
		}
	}
	return nil, fmt.Errorf("shred the vault: subjects left after %d batches", maxShredBatches)
}

// maxShredBatches bounds the batches of one vault step (a further run
// continues).
const maxShredBatches = 100000

func (p *Purger) readExternal(ctx context.Context, orgID string) (*externalProgress, error) {
	var raw []byte
	err := database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT COALESCE(settings -> $2, 'null'::jsonb) FROM organization WHERE id = $1`, orgID,
			purgeExternalKey).Scan(&raw)
	})
	if err != nil {
		return nil, err
	}
	out := &externalProgress{Steps: map[string]map[string]int64{}}
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, out); err != nil {
			return nil, fmt.Errorf("decode external progress: %w", err)
		}
		if out.Steps == nil {
			out.Steps = map[string]map[string]int64{}
		}
	}
	return out, nil
}

func (p *Purger) saveExternal(ctx context.Context, orgID string, progress *externalProgress) error {
	raw, err := json.Marshal(progress)
	if err != nil {
		return err
	}
	return database.WithRequestTenant(orgScope(ctx, orgID), p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE organization SET settings = jsonb_set(COALESCE(settings, '{}'::jsonb), ARRAY[$2], $3::jsonb)
			WHERE id = $1 AND deletion_state = 'purging'`, orgID, purgeExternalKey, raw)
		return err
	})
}

// recordExternal writes org.deletion.external_purged with the counts of
// the steps (AK6) and marks the progress recorded in the same transaction.
func (p *Purger) recordExternal(ctx context.Context, orgID string, progress *externalProgress) error {
	var objects int64
	for name, c := range progress.Steps {
		if strings.HasPrefix(name, "objects:") {
			objects += c["objects"]
		}
	}
	details := map[string]any{"steps": progress.Steps, "objects": objects}
	err := p.auditor.RecordChange(orgScope(ctx, orgID), orgID, func(ctx context.Context) (*operator.AuditEntry, error) {
		progress.Recorded = true
		if err := p.saveExternal(ctx, orgID, progress); err != nil {
			progress.Recorded = false
			return nil, err
		}
		return &operator.AuditEntry{OperatorID: purgeOperator, OperatorKind: operator.KindSystem, Action: "org.deletion.external_purged",
			TargetOrg: orgID, Status: 200, Details: details}, nil
	})
	if err != nil {
		progress.Recorded = false
		return fmt.Errorf("org purge: record the external purge: %w", err)
	}
	return nil
}
