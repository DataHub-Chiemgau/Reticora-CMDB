package discovery

import (
	"context"
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
// VRF) never run at the same time: with more than one lane every record and
// batch transaction first takes the advisory locks of its identity keys, so
// identity resolution sees what a serial run would have seen. One lane is
// the serial processing of the releases before.

// MaxIngestLanes is the largest number of ingest lanes
// (RETICORA_INGEST_LANES, 1-16).
const MaxIngestLanes = 16

// clampLanes keeps a lane count within 1..MaxIngestLanes.
func clampLanes(n int) int {
	return max(1, min(n, MaxIngestLanes))
}

// WithIngestLanes sets the number of ingest lanes (WP-334): records are
// grouped and serialized per organization and lane, and with more than one
// lane the transactions take the identity locks. NATSIngestQueue.Attach sets
// the queue's lane count; without a queue the ingest stays serial unless
// this is called.
func (h *Handler) WithIngestLanes(n int) *Handler {
	h.ingestLanes = clampLanes(n)
	return h
}

// lanesOn reports whether more than one lane may process an organization at
// a time.
func (h *Handler) lanesOn() bool { return h.ingestLanes > 1 }

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
// keys as one statement of the context transaction. The discovery PG
// repository implements it.
type IdentityLocker interface {
	LockIdentities(ctx context.Context, orgID string, keys []int64) error
}

// identityLockNamespace prefixes every hashed identity key: the keys use the
// one-argument (bigint) form of the advisory locks, a lock space of its own
// next to the two-argument keys of the audit chain and the event outbox
// (migrations 000117, 000120), and the prefix keeps them apart from the
// other bigint keys (location tree, scheduler leader).
const identityLockNamespace = "reticora/ingest-identity\x00"

// identityLockKeys returns the advisory lock keys of the identity values of
// items, sorted and each once: the usable serial number (case folded), the
// usable hardware UUID, every canonical MAC (primary, fingerprint and
// interface MACs) and the management IP in its VRF; never the host name,
// which never merges (CH29) and would make "localhost" a hot key.
func identityLockKeys(orgID string, ic *IdentityContext, items ...*IngestItem) []int64 {
	if ic == nil {
		ic = &IdentityContext{}
	}
	org := strings.ToLower(orgID)
	var keys []int64
	add := func(kind, value string) {
		h := fnv.New64a()
		_, _ = h.Write([]byte(identityLockNamespace + org + "\x00" + kind + "\x00" + value))
		keys = append(keys, int64(h.Sum64())) //nolint:gosec // a hash, its sign does not matter
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
// transaction when more than one lane can process the organization; it must
// be the transaction's first statement, so that the candidate query after it
// sees every transaction that held one of the keys before (WP-334, E-56).
func (h *Handler) lockIdentities(ctx context.Context, orgID string, ic *IdentityContext, items ...*IngestItem) error {
	if !h.lanesOn() || !database.InTx(ctx) {
		return nil
	}
	locker, ok := h.repo.(IdentityLocker)
	if !ok {
		return nil
	}
	keys := identityLockKeys(orgID, ic, items...)
	if len(keys) == 0 {
		return nil
	}
	return locker.LockIdentities(ctx, orgID, keys)
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
// admitted against (max_cis, ENT-03). With one lane it is the frame's count,
// as before. With more lanes and a limit, the transaction takes the
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
	if !h.lanesOn() || h.limits == nil || !database.InTx(ctx) {
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
	inFlight := int64(h.ingestLanes+1) * ingestBatch
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
// transaction lost against another lane.
const maxRecordRetries = 3

// retryReason names why a failed record transaction is started again at
// once with more than one lane, or returns "": a deadlock (40P01) or
// serialization failure (40001) with another lane, a unique identity index
// another lane or instance won (23505 on serial number, hardware UUID or
// primary MAC; the new attempt matches that CI), or a max_cis admission that
// must run exclusively.
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
	case "23505":
		switch pgErr.ConstraintName {
		case "uq_ci_org_serial", "uq_ci_org_hardware_uuid", "uq_ci_org_primary_mac":
			return "identity"
		}
	}
	return ""
}

// runRecord processes record i of the frame in its own transaction. With
// more than one lane a transaction that lost against another lane (see
// retryReason) runs again at once, at most maxRecordRetries times, in a
// fresh transaction and from the record as reported; the queue does not
// see these attempts. With one lane the record runs once, as before.
func (h *Handler) runRecord(ctx context.Context, orgID string, frame *batchFrame, i int) (recordOutcome, error) {
	var reported IngestItem
	if h.lanesOn() {
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
		if !h.lanesOn() || reason == "" || attempt >= maxRecordRetries {
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
