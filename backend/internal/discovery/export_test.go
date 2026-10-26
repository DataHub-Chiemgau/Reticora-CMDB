package discovery

import (
	"context"
	"encoding/json"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
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

// ProcessInLane processes records in a lane of the organization, whatever
// lane their keys select (WP-334): records of one device with different
// strongest keys, a lane count changed under way, or two instances. It
// processes as the HTTP ingest without a queue does, which with more than
// one lane takes the same locks as a lane's consumer; ConsumeRecords
// consumes as a consumer of the queue does with any lane count. ctx carries
// the tenant and its scope.
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

// ConsumeRecords consumes records of the organization as a consumer of the
// ingest queue does (consumeBatch), one delivery per record in the tenant
// scope of ctx, and returns how each delivery ended ("ack", "nak", "term")
// in the order of observed_at.
func ConsumeRecords(ctx context.Context, h *Handler, orgID string, recs ...DeviceRecord) []string {
	items := make([]IngestItem, len(recs))
	for i := range recs {
		items[i] = itemFromRecord(&recs[i])
	}
	msgs := newIngestMessages(ctx, tenant.TenantInfo{OrganizationID: orgID}, "", items)
	fakes := make([]*fakeDelivery, len(msgs))
	deliveries := make([]ingestDelivery, len(msgs))
	for i := range msgs {
		data, err := json.Marshal(&msgs[i])
		if err != nil {
			panic(err)
		}
		fakes[i] = &fakeDelivery{data: data, delivered: 1}
		deliveries[i] = fakes[i]
	}
	consumeBatch(ctx, h, deliveries)
	results := make([]string, len(fakes))
	for i, d := range fakes {
		results[i] = d.result
	}
	return results
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

// CALockClass is the class of the CA lock (WP-430): a change of the
// collector CA holds it exclusively, the issuance of a collector certificate
// shared.
const CALockClass = caLockClass

// LoadedCAFingerprint is the SHA-256 (hex) of the active CA in ca's memory,
// without the version check of a use; empty before the first use.
func LoadedCAFingerprint(ca *CA) string {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	if ca.st == nil {
		return ""
	}
	return ca.st.active.info().Fingerprint
}

// CAChangeMessage is the announcement AnnounceCAChange publishes for the
// version of collector_ca at the time at.
func CAChangeMessage(ca *CA, version string, at time.Time) ([]byte, error) {
	return ca.caChangeMessage(version, at)
}

// VerifyCAChange is the check of an announcement on
// CollectorCAChangedSubject at now.
func VerifyCAChange(ca *CA, data []byte, now time.Time) error { return ca.verifyCAChange(data, now) }
