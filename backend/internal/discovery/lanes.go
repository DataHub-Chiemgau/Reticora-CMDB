package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"log/slog"
	"maps"
	"math"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
)

// Parallel ingest lanes (NFR-03, WP-334, E-56 option 1 with identity lock).
// The records of an organization are spread over RETICORA_INGEST_LANES
// lanes by their strongest identity key in REC-02 order; each lane has its
// own JetStream consumer and is processed serially per organization, the
// lanes of one organization run side by side. Records that share any
// identity key (serial number, hardware UUID, MAC, management IP in its
// VRF) never run at the same time: every record and batch transaction that
// another one may run next to (see locking) first takes the advisory locks
// of its identity keys, so identity resolution sees what a serial run would
// have seen. One lane is the serial processing of the releases before
// within a process.

// MaxIngestLanes is the largest number of ingest lanes
// (RETICORA_INGEST_LANES, 1-16).
const MaxIngestLanes = 16

// clampLanes keeps a lane count within 1..MaxIngestLanes.
func clampLanes(n int) int {
	return max(1, min(n, MaxIngestLanes))
}

// WithIngestLanes sets the number of ingest lanes (WP-334): records are
// grouped and serialized per organization and lane, and with more than one
// lane and a repository that can lock (parallel) the lanes of an
// organization run side by side. NATSIngestQueue.Attach sets the queue's
// lane count; without a queue the ingest stays serial unless this is
// called.
func (h *Handler) WithIngestLanes(n int) *Handler {
	h.ingestLanes = clampLanes(n)
	return h
}

// canLock reports whether the repository takes the ingest locks: it runs
// every record and batch in a transaction (TxRunner) and takes the identity
// and max_cis locks in it (IdentityLocker, CICountLocker). The PostgreSQL
// repository can; the memory repository of --no-db cannot.
func (h *Handler) canLock() bool {
	_, identities := h.repo.(IdentityLocker)
	_, counts := h.repo.(CICountLocker)
	return h.tx != nil && identities && counts
}

// parallel reports whether the lanes of an organization run side by side in
// this process: with more than one lane and a repository that can lock.
// Without the locks nothing would keep two lanes from creating one device
// twice, so a process without them (--no-db with NATS) processes every
// organization serially whatever the lane count; the subjects stay per
// lane.
func (h *Handler) parallel() bool { return h.ingestLanes > 1 && h.canLock() }

// serialLane is the lane whose mutex serializes the ingest of records of
// lane: lane itself when the lanes of an organization run side by side,
// else 0, the organization's one mutex.
func (h *Handler) serialLane(lane int) int {
	if h.parallel() {
		return lane
	}
	return 0
}

// queuedKey marks the context of records consumed from the ingest queue.
type queuedKey struct{}

// withQueued marks ctx as processing records consumed from the ingest queue
// (consumeBatch).
func withQueued(ctx context.Context) context.Context {
	return context.WithValue(ctx, queuedKey{}, true)
}

// fromQueue reports whether ctx processes records consumed from the ingest
// queue.
func fromQueue(ctx context.Context) bool {
	queued, _ := ctx.Value(queuedKey{}).(bool)
	return queued
}

// locking reports whether the transactions of an ingest take the identity
// locks and the max_cis lock, and start again after losing a race
// (retryReason): whenever another transaction may process records of the
// same organization at the same time. That is so when the lanes of an
// organization run side by side in this process (parallel), and for every
// record consumed from the ingest queue whatever this instance's lane
// count: the other instances consume the same lanes, and the former
// consumer and the drain lanes run next to them with a MaxAckPending of
// their own, with one lane too, while the lane count changes and while
// instances with different counts run side by side. The HTTP ingest
// without a queue processes in the request, serially per organization in
// this process, and takes none of them (the budgets of WP-332/333).
func (h *Handler) locking(ctx context.Context) bool {
	return h.canLock() && (h.ingestLanes > 1 || fromQueue(ctx))
}

// IngestLane returns the lane of a record: FNV-1a/32 of its strongest
// identity key modulo lanes; 0 without a key or with at most one lane. The
// hash and the key format are part of the subject contract: changing either
// moves devices to other lanes like changing the lane count does.
func IngestLane(item *IngestItem, lanes int) int {
	if lanes <= 1 {
		return 0
	}
	key := laneKey(item)
	if key == "" {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return int(h.Sum32() % uint32(min(lanes, MaxIngestLanes))) //nolint:gosec // lanes is 2..16
}

// laneKey is the strongest identity key of a record in the order of REC-02
// (identity resolution): a usable serial number (placeholders dropped, case
// folded), a usable hardware UUID (clone UUIDs dropped), a canonical MAC
// (the primary MAC first; locally administered and virtual router MACs
// dropped), the management IP. The SNMP and SSH records of a server share
// the serial number while only SSH reports the UUID, so serial first keeps
// one device in one lane. A host name never merges (CH29) and is no key.
func laneKey(item *IngestItem) string {
	if usableSerial(item.SerialNumber) {
		return "serial:" + strings.ToLower(strings.TrimSpace(item.SerialNumber))
	}
	if uuid := incomingUUID(item); usableUUID(uuid) {
		return "uuid:" + uuidKey(uuid)
	}
	if mac := canonicalMAC(item.PrimaryMAC); mac != "" {
		return "mac:" + mac
	}
	if macs := incomingMACs(item); len(macs) > 0 {
		return "mac:" + slices.Sorted(maps.Keys(macs))[0]
	}
	if ip := net.ParseIP(hostOnly(item.ManagementIP)); ip != nil {
		return "ip:" + ip.String()
	}
	return ""
}

// uuidKey is a hardware UUID in canonical lower-case form; a value
// PostgreSQL cannot read keeps its trimmed lower-case spelling.
func uuidKey(uuid string) string {
	u := strings.ToLower(strings.TrimSpace(uuid))
	if canonical, ok := canonicalUUID(u); ok {
		return canonical
	}
	return u
}

// LaneSubject is the subject of a record of an organization in a lane:
// ingest.<org>.<lane>.<collector>, "api" without a collector. The former
// subjects ingest.<org>.<collector> have one token less, so the filters of
// the lanes (ingest.*.<lane>.*) and of the former consumer (ingest.*.*) are
// disjoint, as a work-queue stream requires.
func LaneSubject(orgID string, lane int, collectorID string) string {
	if collectorID == "" {
		collectorID = "api"
	}
	return "ingest." + orgID + "." + strconv.Itoa(lane) + "." + subjectToken(collectorID)
}

// laneMutexKey names the mutex of an organization's lane.
type laneMutexKey struct {
	org  string
	lane int
}

// laneMutex returns the mutex that serializes the ingests of an
// organization's lane in this process (REC-01).
func (h *Handler) laneMutex(orgID string, lane int) *sync.Mutex {
	lock, _ := h.serial.LoadOrStore(laneMutexKey{org: orgID, lane: lane}, &sync.Mutex{})
	return lock.(*sync.Mutex)
}

// IdentityLocker takes the transaction-scoped advisory locks of identity
// keys as one statement of the context transaction: first Org
// (identityOrgLockClass), shared, or exclusive with Exclusive, then every key
// of Keys (identityKeyLockClass) in ascending order. The discovery PG
// repository implements it.
type IdentityLocker interface {
	LockIdentities(ctx context.Context, orgID string, locks IdentityLocks) error
}

// IdentityLocks are the identity locks of one transaction: the
// organization's identity lock Org, held shared next to the sorted distinct
// keys of the transaction's records, or exclusive without keys when the
// records have more than identityLockLimit keys.
type IdentityLocks struct {
	Org       int32
	Exclusive bool
	Keys      []int32
}

// identityLockLimit bounds the identity keys one transaction locks.
// Advisory locks never take the fast path: each is an entry of PostgreSQL's
// shared lock table, which every relation lock of every session of the
// cluster shares and which holds about max_locks_per_transaction ×
// max_connections entries (64 × 100 by default). A transaction locking
// thousands of keys (a batch of switches with all their interface MACs, a
// record with thousands of MACs) makes the transactions of every tenant and
// service fail with "out of shared memory" (53200). A transaction over the
// limit therefore takes the organization's identity lock exclusively
// instead of its keys: every identity-locking transaction of the
// organization holds that lock shared, so it still runs alone against each
// of them. A window of the batch path ends before the record that would
// pass the limit (windowEnd), so only a record over the limit on its own
// runs exclusively. A process holds at most processingSlots() ×
// (identityLockLimit+1) identity locks.
const identityLockLimit = 256

// Lock classes of the ingest locks. They use the two-argument form of the
// advisory locks, pg_advisory_xact_lock(class, key), whose lock tag never
// equals the tag of a one-argument (bigint) lock: the scheduler's leader
// and job locks, the location tree and the operator audit stay out of
// reach of keys derived from what a tenant reports. Their classes differ
// from those of the audit chain (0x52657469), the event outbox (0x45766e74)
// and its publisher (0x45766e75), and from each other, so the organization's
// identity lock, taken before the keys, is never one of them.
const (
	identityOrgLockClass int32 = 0x52496f72 // "RIor"
	identityKeyLockClass int32 = 0x52496b79 // "RIky"
	ciCountLockClass     int32 = 0x52436e74 // "RCnt"
)

// identityLockNamespace prefixes every hashed identity key and the
// organization's identity lock.
const identityLockNamespace = "reticora/ingest-identity\x00"

// ingestLockKey is the key of text within its lock class: the first four
// bytes of its SHA-256. A tenant cannot choose a value whose key equals a
// given one at less than about 2^32 hashes, and two values that share a key
// only serialize their transactions, as one value would.
func ingestLockKey(text string) int32 {
	sum := sha256.Sum256([]byte(text))
	return int32(binary.BigEndian.Uint32(sum[:4])) //nolint:gosec // the bits of the hash
}

// identityOrgLockKey is the advisory lock key of an organization's identity
// lock in identityOrgLockClass.
func identityOrgLockKey(orgID string) int32 {
	return ingestLockKey(identityLockNamespace + strings.ToLower(orgID))
}

// identityLocks returns the identity locks of a transaction that processes
// items: the organization's lock shared and the keys of items, or the
// organization's lock exclusive when items have more than
// identityLockLimit keys. A transaction takes them once, as its first
// statement, so that it never asks for the exclusive lock while holding the
// shared one.
func identityLocks(orgID string, ic *IdentityContext, items ...*IngestItem) IdentityLocks {
	locks := IdentityLocks{Org: identityOrgLockKey(orgID), Keys: identityLockKeys(orgID, ic, items...)}
	if len(locks.Keys) > identityLockLimit {
		locks.Exclusive, locks.Keys = true, nil
	}
	return locks
}

// identityLockKeys returns the advisory lock keys (identityKeyLockClass) of
// the identity values of items, sorted and each once: the usable serial
// number (case folded), the usable hardware UUID, every canonical MAC
// (primary, fingerprint and interface MACs) and the management IP in its
// VRF; never the host name, which never merges (CH29) and would make
// "localhost" a hot key.
func identityLockKeys(orgID string, ic *IdentityContext, items ...*IngestItem) []int32 {
	if ic == nil {
		ic = &IdentityContext{}
	}
	org := strings.ToLower(orgID)
	var keys []int32
	add := func(kind, value string) {
		keys = append(keys, ingestLockKey(identityLockNamespace+org+"\x00"+kind+"\x00"+value))
	}
	for _, item := range items {
		if usableSerial(item.SerialNumber) {
			add("serial", strings.ToLower(strings.TrimSpace(item.SerialNumber)))
		}
		if uuid := incomingUUID(item); usableUUID(uuid) {
			add("uuid", uuidKey(uuid))
		}
		for mac := range incomingMACs(item) {
			add("mac", mac)
		}
		if ip := net.ParseIP(hostOnly(item.ManagementIP)); ip != nil {
			add("ip", ip.String()+"@"+ic.vrf(item.VRFID))
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// lockIdentities takes the identity locks of items in the context
// transaction when the frame's records take locks (locking); it must be the
// transaction's first statement, so that the candidate query after it sees
// every transaction that held one of the keys before (WP-334, E-56).
// Records without an identity key match nothing and lock nothing.
func (h *Handler) lockIdentities(ctx context.Context, orgID string, frame *batchFrame, items ...*IngestItem) error {
	if !frame.locking || !database.InTx(ctx) {
		return nil
	}
	locker, ok := h.repo.(IdentityLocker)
	if !ok {
		return nil
	}
	locks := identityLocks(orgID, frame.identity, items...)
	if !locks.Exclusive && len(locks.Keys) == 0 {
		return nil
	}
	return locker.LockIdentities(ctx, orgID, locks)
}

// windowEnd returns the end of the batch window of the frame's records that
// starts at start: at most ingestBatch records and, when the records take
// the identity locks (locking), only as many as keep the identity keys of
// the records the batch locks within identityLockLimit; a record over the
// limit on its own is a window of its own. Without locks windows keep
// ingestBatch records.
func (h *Handler) windowEnd(orgID string, frame *batchFrame, start int) int {
	end := min(start+ingestBatch, len(frame.items))
	if !frame.locking {
		return end
	}
	keys := map[int32]struct{}{}
	locked := false
	for i := start; i < end; i++ {
		if frame.typeIDs[i] == "" {
			// Queued for review in the frame; the batch does not lock it.
			continue
		}
		for _, k := range identityLockKeys(orgID, frame.identity, &frame.items[i]) {
			keys[k] = struct{}{}
		}
		if len(keys) > identityLockLimit {
			if !locked {
				return i + 1
			}
			return i
		}
		locked = true
	}
	return end
}

// CICountLocker takes the organization's max_cis lock until the end of the
// context transaction, exclusive or shared, and then counts the CIs the
// transaction sees, as the frame does (ENT-03, WP-334). The discovery PG
// repository implements it.
type CICountLocker interface {
	LockCICount(ctx context.Context, orgID string, exclusive bool) (int64, error)
}

// errNearLimit makes a transaction that admitted new CIs under the shared
// max_cis lock start again under the exclusive one: its count showed that
// the lanes together could pass the limit.
var errNearLimit = errors.New("ingest: max_cis headroom below the creations the lanes may have in flight")

// admissionCount returns the CI count the new CIs of a transaction are
// admitted against (max_cis, ENT-03). Without locks (locking) it is the
// frame's count, as before. With them and a limit, the transaction takes the
// organization's max_cis lock and counts again inside the transaction: the
// exclusive lock near the limit, so that admission is exact; the shared lock
// otherwise, which lanes hold side by side and which an exclusive holder
// waits for, so that its count includes every creation in flight.
//
// Far from the limit means that the creations still in flight cannot reach
// it: a record is acknowledged only after its transaction committed, and the
// lane consumers and the former consumer hold at most ingestBatch
// unacknowledged messages each across all instances (MaxAckPending), so at
// most (lanes+1)·ingestBatch creations are in flight. A shared holder whose
// count shows less headroom rolls back and starts again exclusively
// (errNearLimit). Lanes being drained after a lane count was lowered are not
// counted: until they are empty, each may add up to ingestBatch.
func (h *Handler) admissionCount(ctx context.Context, orgID string, frame *batchFrame, snapshot int64) (int64, error) {
	if !frame.locking || h.limits == nil || !database.InTx(ctx) {
		return snapshot, nil
	}
	locker, ok := h.repo.(CICountLocker)
	if !ok {
		return snapshot, nil
	}
	// The limit of the organization: AllowCreate reports it for any count
	// once a limit is stored. Without one there is nothing to lock; another
	// failure is reported by admitNewCI as before.
	var exceeded *entitlement.LimitExceededError
	if err := h.limits.AllowCreate(ctx, orgID, entitlement.LimitMaxCIs, math.MaxInt64); !errors.As(err, &exceeded) {
		return snapshot, nil
	}
	inFlight := int64(max(h.ingestLanes, 1)+1) * ingestBatch
	exclusive := frame.limitExclusive || snapshot+inFlight > exceeded.Limit
	count, err := locker.LockCICount(ctx, orgID, exclusive)
	if err != nil {
		return 0, err
	}
	if !exclusive && count+inFlight > exceeded.Limit {
		return 0, errNearLimit
	}
	return count, nil
}

// maxRecordRetries bounds the immediate retries of a record whose
// transaction lost against another lane or instance.
const maxRecordRetries = 3

// outOfMemoryPause is the pause before the first new attempt of a
// transaction that failed because PostgreSQL ran out of shared memory or
// gave up waiting for a lock; the next attempts wait one more pause each.
const outOfMemoryPause = 100 * time.Millisecond

// retryReason names why a failed record transaction that takes the ingest
// locks (locking) is started again, or returns "": a deadlock (40P01) or
// serialization failure (40001) with another lane or instance, a unique
// identity index another lane or instance won (23505 on serial number, hardware UUID or primary MAC; the
// new attempt matches that CI), a max_cis admission that must run
// exclusively, PostgreSQL out of shared memory (53200, mostly a full lock
// table) for a moment, or a lock wait longer than the ingest lock timeout
// (55P03, see ingestLockTimeout).
func retryReason(err error) string {
	if errors.Is(err, errNearLimit) {
		return "near_limit"
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	switch pgErr.Code {
	case "40P01":
		return "deadlock"
	case "40001":
		return "serialization"
	case "53200":
		return "out_of_memory"
	case "55P03":
		return "lock_timeout"
	case "23505":
		switch pgErr.ConstraintName {
		case "uq_ci_org_serial", "uq_ci_org_hardware_uuid", "uq_ci_org_primary_mac":
			return "identity"
		}
	}
	return ""
}

// retryPause waits before the new attempt of a transaction that failed for
// reason, attempt being the number of the failed one (0 first): not at all
// after losing against another lane; after running out of shared memory or
// a lock timeout (attempt+1)·outOfMemoryPause, so that other sessions can
// release their locks first. It reports false when ctx ended first.
func retryPause(ctx context.Context, reason string, attempt int) bool {
	if reason != "out_of_memory" && reason != "lock_timeout" {
		return true
	}
	t := time.NewTimer(time.Duration(attempt+1) * outOfMemoryPause)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// runRecord processes record i of the frame in its own transaction. When
// the frame's records take the ingest locks (locking), a transaction that
// lost against another one, ran out of shared memory or waited too long for
// a lock (see retryReason) runs again, at most maxRecordRetries times, in a
// fresh transaction and from the record as reported (retryPause); the queue
// does not see these attempts. Without locks the record runs once, as
// before.
func (h *Handler) runRecord(ctx context.Context, orgID string, frame *batchFrame, i int) (recordOutcome, error) {
	var reported IngestItem
	if frame.locking {
		reported = cloneItem(&frame.items[i])
	}
	for attempt := 0; ; attempt++ {
		var out recordOutcome
		mark := frame.unlicensed.mark()
		err := h.inTx(ctx, orgID, func(ctx context.Context) error {
			return h.processRecord(ctx, orgID, frame, i, &out)
		})
		if err == nil {
			return out, nil
		}
		// Devices the failed transaction queued as unlicensed are not.
		frame.unlicensed.rollback(mark)
		reason := retryReason(err)
		if !frame.locking || reason == "" || attempt >= maxRecordRetries || !retryPause(ctx, reason, attempt) {
			return out, err
		}
		if reason == "near_limit" {
			frame.limitExclusive = true
		}
		recordRetries.WithLabelValues(reason).Inc()
		ingestRetries.Add(1)
		slog.DebugContext(ctx, "ingest record retried", "event", "ingest.record.retry", "organization_id", orgID,
			"record_id", frame.items[i].RecordID, "reason", reason, "attempt", attempt+1, "error", err)
		frame.items[i] = cloneItem(&reported)
	}
}

var ingestRetries atomic.Int64

// IngestRecordRetries is the number of immediate record retries since start
// (WP-334).
func IngestRecordRetries() int64 { return ingestRetries.Load() }

// Metrics of the ingest lanes (WP-334, OPS-05).
var (
	ingestLanesGauge = prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: "reticora",
		Name:      "ingest_lanes",
		Help:      "Ingest lanes this instance publishes to and consumes (RETICORA_INGEST_LANES).",
	})
	laneBacklog = prometheus.NewGaugeVec(prometheus.GaugeOpts{
		Namespace: "reticora",
		Name:      "ingest_lane_backlog",
		Help:      "Messages waiting per ingest lane consumer (pending plus unacknowledged), sampled every 30 s; lane \"legacy\" drains the subjects of earlier releases.",
	}, []string{"lane"})
	laneRecords = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "ingest_lane_records_total",
		Help:      "Ingest messages acknowledged after processing, by the lane of their record.",
	}, []string{"lane"})
	recordRetries = prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "ingest_record_retries_total",
		Help:      "Record transactions started again at once after losing against another ingest lane, by reason.",
	}, []string{"reason"})
)
