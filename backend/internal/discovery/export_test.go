package discovery

import (
	"context"
	"time"
)

// Hooks of the batch candidate queries for the integration tests of
// package discovery_test (WP-333).
var (
	BatchKeys        = batchKeys
	AssignCandidates = assignCandidates
	AssignDeleted    = assignDeleted
)

// IdentityBranchQuery returns the batch candidate query, live or of the
// soft-deleted CIs, reduced to its branch name, with the arguments of its
// first page; ok is false when keys yield no such branch. keys should carry
// only the kind of that branch, so that every argument is referenced.
func IdentityBranchQuery(orgID string, keys *IdentityKeys, deleted bool, name string) (query string, args []any, ok bool) {
	branches, base := identityBranches(keys, deleted)
	for _, b := range branches {
		if b.name != name {
			continue
		}
		args = append([]any{orgID}, base...)
		if deleted {
			return deletedHitsSQL([]identityBranch{b}), args, true
		}
		return identityHitsSQL([]identityBranch{b}, len(base)+2), append(args, "", time.Time{}, "", int64(0), identityPage), true
	}
	return "", nil, false
}

// ProcessInLane processes records in a lane of the organization as that
// lane's consumer would, whatever lane their keys select (WP-334): records
// of one device with different strongest keys, a lane count changed under
// way, or two instances. ctx carries the tenant and its scope.
func ProcessInLane(ctx context.Context, h *Handler, orgID string, lane int, recs ...DeviceRecord) (BulkIngestResponse, error) {
	items := make([]IngestItem, len(recs))
	for i := range recs {
		items[i] = itemFromRecord(&recs[i])
	}
	return h.processSerially(ctx, orgID, lane, items)
}

// ProcessItemsInLane processes ingest items in a lane of the organization
// like ProcessInLane.
func ProcessItemsInLane(ctx context.Context, h *Handler, orgID string, lane int, items ...IngestItem) (BulkIngestResponse, error) {
	return h.processSerially(ctx, orgID, lane, items)
}

// ItemFromRecord is the ingest item of a record.
func ItemFromRecord(rec *DeviceRecord) IngestItem { return itemFromRecord(rec) }

// IdentityLockLimit is the most identity keys one transaction locks
// (WP-334); over it, the transaction holds the organization's identity lock
// exclusively instead.
const IdentityLockLimit = identityLockLimit

// SetBeforeCreate installs a hook that runs after a transaction decided to
// create new CIs and before it inserts them.
func SetBeforeCreate(h *Handler, fn func(context.Context)) { h.beforeCreate = fn }

// QueueConsumers lists the consumers the queue fetches from.
func QueueConsumers(q *NATSIngestQueue) []string { return q.names() }

// RefreshQueue runs the periodic check of the queue at once.
func RefreshQueue(ctx context.Context, q *NATSIngestQueue) { q.refresh(ctx) }

// IdentityLocksOf returns the identity locks a transaction that processes
// items takes (WP-334), with an empty identity context.
func IdentityLocksOf(orgID string, items ...*IngestItem) IdentityLocks {
	return identityLocks(orgID, &IdentityContext{}, items...)
}

// Lock classes of the ingest locks (two-argument advisory locks).
const (
	IdentityOrgLockClass = identityOrgLockClass
	IdentityKeyLockClass = identityKeyLockClass
	CICountLockClass     = ciCountLockClass
)

// SetLockTimeout replaces the lock timeout of r's ingest lock statements.
func SetLockTimeout(r *PGRepository, d time.Duration) { r.lockTimeout = d }
