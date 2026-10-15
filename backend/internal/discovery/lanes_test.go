package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestIngestLaneKey covers WP-334 (E-56, REC-02): the lane key is the
// strongest identity key in the order of identity resolution, normalized as
// identity resolution compares it, so spellings of one value share a lane.
func TestIngestLaneKey(t *testing.T) {
	const uuid = "4c4c4544-0042-3510-8048-b4c04f4d4e31"
	for _, c := range []struct {
		name string
		item IngestItem
		want string
	}{
		{"serial before the hardware UUID", IngestItem{SerialNumber: "SN-1", HardwareUUID: uuid}, "serial:sn-1"},
		{"serial case and blanks", IngestItem{SerialNumber: "  Sn-1 "}, "serial:sn-1"},
		{"placeholder serial falls back to the UUID", IngestItem{SerialNumber: "To Be Filled By O.E.M.", HardwareUUID: uuid}, "uuid:" + uuid},
		{"repeated characters are a placeholder", IngestItem{SerialNumber: "0000000", HardwareUUID: uuid}, "uuid:" + uuid},
		{"upper-case UUID", IngestItem{HardwareUUID: "4C4C4544-0042-3510-8048-B4C04F4D4E31"}, "uuid:" + uuid},
		{"UUID in braces without hyphens", IngestItem{HardwareUUID: "{4C4C454400423510 8048B4C04F4D4E31}"}, "uuid:{4c4c454400423510 8048b4c04f4d4e31}"},
		{"UUID of the fingerprint", IngestItem{Fingerprint: map[string]any{"hardware_uuid": uuid}}, "uuid:" + uuid},
		{"clone UUID falls back to the MAC", IngestItem{HardwareUUID: "03000200-0400-0500-0006-000700080009", PrimaryMAC: "00-1B-21-AA-00-01"},
			"mac:00:1b:21:aa:00:01"},
		{"primary MAC before other MACs", IngestItem{PrimaryMAC: "00:1b:21:aa:00:09",
			Interfaces: []wire.IngestInterface{{MAC: "00:1b:21:aa:00:01"}}}, "mac:00:1b:21:aa:00:09"},
		{"locally administered primary MAC skipped", IngestItem{PrimaryMAC: "02:00:00:00:00:01",
			Fingerprint: map[string]any{"mac_addresses": []any{"00:1b:21:aa:00:05", "00:1b:21:aa:00:03"}}}, "mac:00:1b:21:aa:00:03"},
		{"VRRP MAC skipped", IngestItem{PrimaryMAC: "00:00:5e:00:01:07", ManagementIP: "192.0.2.1"}, "ip:192.0.2.1"},
		{"IP with prefix length", IngestItem{ManagementIP: " 192.0.2.1/24"}, "ip:192.0.2.1"},
		{"IPv6 spelled long", IngestItem{ManagementIP: "2001:0db8:0000:0000:0000:0000:0000:0001"}, "ip:2001:db8::1"},
		{"host name only", IngestItem{Hostname: "srv-1", FQDN: "srv-1.example"}, ""},
		{"nothing", IngestItem{}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := laneKey(&c.item); got != c.want {
				t.Errorf("laneKey = %q, want %q", got, c.want)
			}
		})
	}
	// Spellings of one device share a lane; no key and one lane mean lane 0.
	pairs := [][2]IngestItem{
		{{SerialNumber: "AbC-77"}, {SerialNumber: "abc-77 "}},
		{{HardwareUUID: uuid}, {HardwareUUID: "4C4C4544-0042-3510-8048-B4C04F4D4E31"}},
		{{PrimaryMAC: "AA-BB-CC-00-11-22"}, {PrimaryMAC: "aa:bb:cc:00:11:22"}},
		{{ManagementIP: "10.0.0.1/24"}, {ManagementIP: "10.0.0.1"}},
	}
	for _, p := range pairs {
		for _, lanes := range []int{2, 4, 16} {
			if a, b := IngestLane(&p[0], lanes), IngestLane(&p[1], lanes); a != b {
				t.Errorf("%+v and %+v: lanes %d and %d of %d", p[0], p[1], a, b, lanes)
			}
		}
	}
	if IngestLane(&IngestItem{Hostname: "x"}, 4) != 0 || IngestLane(&IngestItem{SerialNumber: "SN-9"}, 1) != 0 ||
		IngestLane(&IngestItem{SerialNumber: "SN-9"}, 0) != 0 {
		t.Error("no key or one lane must be lane 0")
	}
}

// TestIngestLaneSpread: 10,000 serial numbers like those of the load test
// spread evenly over 4 lanes.
func TestIngestLaneSpread(t *testing.T) {
	counts := make([]int, 4)
	for i := range 10000 {
		counts[IngestLane(&IngestItem{SerialNumber: fmt.Sprintf("LOAD-1760000000000-%d-%d-%d", i%50, i/1000, i%20)}, 4)]++
	}
	for lane, n := range counts {
		if n < 2300 || n > 2700 {
			t.Errorf("lane %d: %d records of 10,000 (%v)", lane, n, counts)
		}
	}
}

func TestLaneSubject(t *testing.T) {
	if got := LaneSubject("o-1", 3, "col.a b"); got != "ingest.o-1.3.col_a_b" {
		t.Errorf("subject %s", got)
	}
	if got := LaneSubject("o-1", 0, ""); got != "ingest.o-1.0.api" {
		t.Errorf("subject without collector %s", got)
	}
	if MaxIngestLanes != config.MaxIngestLanes {
		t.Errorf("discovery.MaxIngestLanes %d != config.MaxIngestLanes %d", MaxIngestLanes, config.MaxIngestLanes)
	}
	for _, c := range []struct {
		name string
		lane int
		ok   bool
	}{{"reticora-ingest", legacyLane, true}, {"reticora-ingest-lane-0", 0, true}, {"reticora-ingest-lane-15", 15, true},
		{"reticora-ingest-lane-07", 0, false}, {"reticora-ingest-lane-x", 0, false}, {"other", 0, false}} {
		if lane, ok := laneOf(c.name); lane != c.lane || ok != c.ok {
			t.Errorf("laneOf(%s) = %d, %v", c.name, lane, ok)
		}
	}
}

// TestIdentityLockKeys: the identity locks cover every value identity
// resolution may match on, normalized, but not the host name; they are
// sorted, distinct and per organization.
func TestIdentityLockKeys(t *testing.T) {
	ic := &IdentityContext{DefaultVRF: "vrf-default"}
	full := IngestItem{SerialNumber: " SN-1 ", HardwareUUID: "4C4C4544-0042-3510-8048-B4C04F4D4E31", PrimaryMAC: "00-1B-21-AA-00-01",
		Fingerprint: map[string]any{"mac_addresses": []any{"00:1b:21:aa:00:02"}}, ManagementIP: "192.0.2.7/24",
		Interfaces: []wire.IngestInterface{{MAC: "00:1b:21:aa:00:03"}}, Hostname: "srv-1"}
	keys := identityLockKeys("Org-1", ic, &full)
	if len(keys) != 6 || !slices.IsSorted(keys) {
		t.Fatalf("keys of a full record: %v", keys)
	}
	one := func(item IngestItem) int32 {
		t.Helper()
		k := identityLockKeys("org-1", ic, &item)
		if len(k) != 1 {
			t.Fatalf("%+v: %d keys", item, len(k))
		}
		return k[0]
	}
	// Each value alone is one of the keys of the full record, in any
	// spelling, also when the record carries it elsewhere.
	for _, item := range []IngestItem{
		{SerialNumber: "sn-1"},
		{HardwareUUID: "4c4c4544-0042-3510-8048-b4c04f4d4e31"},
		{Fingerprint: map[string]any{"hardware_uuid": "4c4c4544-0042-3510-8048-b4c04f4d4e31"}},
		{PrimaryMAC: "00:1b:21:aa:00:02"},
		{Interfaces: []wire.IngestInterface{{MAC: "00:1B:21:AA:00:01"}}},
		{ManagementIP: "192.0.2.7"},
		{ManagementIP: "192.0.2.7", VRFID: "vrf-default"},
	} {
		if k := one(item); !slices.Contains(keys, k) {
			t.Errorf("%+v: key %d not among the keys of the full record", item, k)
		}
	}
	if k := one(IngestItem{ManagementIP: "192.0.2.7", VRFID: "vrf-other"}); slices.Contains(keys, k) {
		t.Error("the same IP in another VRF shares a key")
	}
	if len(identityLockKeys("org-1", ic, &IngestItem{Hostname: "srv-1", FQDN: "srv-1.example", SerialNumber: "n/a",
		HardwareUUID: "00000000-0000-0000-0000-000000000000", PrimaryMAC: "02:00:00:00:00:01"})) != 0 {
		t.Error("host names, placeholders, clone UUIDs and local MACs must not be locked")
	}
	if k := identityLockKeys("org-2", ic, &IngestItem{SerialNumber: "sn-1"}); slices.Contains(keys, k[0]) {
		t.Error("another organization shares a key")
	}
	// Two records: the union, each key once.
	if both := identityLockKeys("org-1", ic, &full, &IngestItem{SerialNumber: "SN-1", ManagementIP: "198.51.100.1"}); len(both) != 7 {
		t.Errorf("keys of two records: %d, want 7", len(both))
	}
}

// TestIngestLockSpace pins the keys and the form of the ingest locks
// (review finding, WP-334): the keys derive from values a tenant reports
// (serial number, hardware UUID, MACs, IPs) and must not reach the locks of
// other subsystems. They are two-argument advisory locks in classes of
// their own, whose tags never equal a one-argument lock's (scheduler leader
// and jobs, location tree, operator audit), and the key within the class is
// the first four bytes of a SHA-256 (FNV-1a can be inverted byte by byte,
// so a serial number with a chosen FNV key takes hours to find).
func TestIngestLockSpace(t *testing.T) {
	if k := identityLockKeys("Org-1", nil, &IngestItem{SerialNumber: " SN-1 "}); len(k) != 1 || k[0] != 2030715095 {
		t.Errorf("identity key of serial SN-1 in org-1: %v, want SHA-256 of the namespaced value (2030715095)", k)
	}
	if k := identityOrgLockKey("ORG-1"); k != 1243962943 {
		t.Errorf("organization identity key of org-1: %d, want 1243962943", k)
	}
	if k := ingestLockKey(ciCountLockNamespace + "org-1"); k != 522053624 {
		t.Errorf("max_cis key of org-1: %d, want 522053624", k)
	}
	classes := map[int32]string{
		0x52657469: "audit chain (audit.AdvisoryLockID, migration 000117)",
		1165389428: "event outbox (migration 000120)",
		1165389429: "event publisher (events.publisherLockKey)",
	}
	for class, name := range map[int32]string{identityOrgLockClass: "identity organization", identityKeyLockClass: "identity keys",
		ciCountLockClass: "max_cis"} {
		if other, taken := classes[class]; taken {
			t.Errorf("lock class %d of the %s locks is the class of the %s locks", class, name, other)
		}
		classes[class] = name
	}
	// Only the two-argument form, with the classes, and a bounded wait that
	// ends with the statement.
	oneArg := regexp.MustCompile(`pg_advisory_xact_lock(_shared)?\(\s*[^,()]+\s*\)`)
	for sql, classes := range map[string][]int32{identityLockSQL: {identityOrgLockClass, identityKeyLockClass}, ciCountLockSQL: {ciCountLockClass}} {
		if oneArg.MatchString(sql) {
			t.Errorf("one-argument advisory lock: %s", sql)
		}
		for _, class := range classes {
			if !strings.Contains(sql, strconv.Itoa(int(class))) {
				t.Errorf("lock class %d missing: %s", class, sql)
			}
		}
		if strings.Count(sql, "set_config('lock_timeout'") != 2 {
			t.Errorf("lock statement does not set and restore lock_timeout: %s", sql)
		}
	}
	if !strings.Contains(identityLockSQL, "pg_advisory_xact_lock(s.c, s.k)") {
		t.Errorf("identity keys not locked in their class: %s", identityLockSQL)
	}
}

// switchRecord is an SNMP record of a switch with ports interface MACs:
// ports+3 identity keys (serial number, primary MAC, the port MACs, the
// management IP).
func switchRecord(n, ports int) IngestItem {
	item := IngestItem{SerialNumber: fmt.Sprintf("SW-%04d", n), PrimaryMAC: fmt.Sprintf("00:1b:21:%02x:%02x:00", n>>8, n&0xff),
		ManagementIP: fmt.Sprintf("10.0.%d.%d", n>>8, n&0xff)}
	for p := range ports {
		item.Interfaces = append(item.Interfaces, wire.IngestInterface{MAC: fmt.Sprintf("00:1b:%02x:%02x:%02x:%02x", 0x22+p>>8, p&0xff, n>>8, n&0xff)})
	}
	return item
}

// TestIdentityLocksBounded: no transaction locks more than
// identityLockLimit identity keys plus the organization's lock (PostgreSQL's
// shared lock table, 53200). A batch of switches is cut into windows below
// the limit, which still batch several records; a record over the limit is
// a window of its own and takes the organization's lock exclusively, alone;
// with one lane windows keep ingestBatch records.
func TestIdentityLocksBounded(t *testing.T) {
	ic := &IdentityContext{DefaultVRF: "vrf-default"}
	org := identityOrgLockKey("org-1")
	if org != identityOrgLockKey("ORG-1") || org == identityOrgLockKey("org-2") {
		t.Error("the organization's identity lock must be one per organization, in any spelling")
	}
	switch48 := switchRecord(1, 48)
	if n := len(identityLockKeys("org-1", ic, &switch48)); n != 51 {
		t.Fatalf("a 48-port switch has %d identity keys, want 51", n)
	}

	// The locks of one record around the limit.
	for _, c := range []struct {
		ports     int
		exclusive bool
	}{{identityLockLimit - 3, false}, {identityLockLimit - 2, true}} {
		item := switchRecord(2, c.ports)
		locks := identityLocks("org-1", ic, &item)
		if locks.Org != org || locks.Exclusive != c.exclusive {
			t.Errorf("%d keys: org lock %d exclusive %v, want %d %v", c.ports+3, locks.Org, locks.Exclusive, org, c.exclusive)
		}
		if want := c.ports + 3; c.exclusive && locks.Keys != nil || !c.exclusive && len(locks.Keys) != want {
			t.Errorf("%d keys: %d keys locked", want, len(locks.Keys))
		}
	}
	hostile := IngestItem{SerialNumber: "SN-HOSTILE", Fingerprint: map[string]any{}}
	var macs []any
	for i := range 20000 {
		macs = append(macs, fmt.Sprintf("00:1b:%02x:%02x:%02x:01", i>>16, (i>>8)&0xff, i&0xff))
	}
	hostile.Fingerprint["mac_addresses"] = macs
	if locks := identityLocks("org-1", ic, &hostile); !locks.Exclusive || len(locks.Keys) != 0 {
		t.Errorf("a record with 20,000 MACs locks %d keys (exclusive %v), want the organization's lock alone", len(locks.Keys), locks.Exclusive)
	}

	// windows cuts the frame of items into the windows of processItems and
	// checks what each locks; lanes > 1 stands for transactions that take
	// the identity locks (locking), one lane for those that do not.
	windows := func(lanes int, items []IngestItem) [][2]int {
		t.Helper()
		h := &Handler{}
		frame := &batchFrame{items: items, typeIDs: make([]string, len(items)), identity: ic, locking: lanes > 1}
		for i := range items {
			frame.typeIDs[i] = "type"
		}
		var out [][2]int
		for start, end := 0, 0; start < len(items); start = end {
			end = h.windowEnd("org-1", frame, start)
			if end <= start || end-start > ingestBatch {
				t.Fatalf("window [%d, %d)", start, end)
			}
			locked := make([]*IngestItem, 0, end-start)
			for i := start; i < end; i++ {
				locked = append(locked, &items[i])
			}
			locks := identityLocks("org-1", ic, locked...)
			if lanes > 1 && (len(locks.Keys) > identityLockLimit || locks.Exclusive && end-start != 1) {
				t.Errorf("window [%d, %d) locks %d keys, exclusive %v", start, end, len(locks.Keys), locks.Exclusive)
			}
			out = append(out, [2]int{start, end})
		}
		return out
	}
	fleet := make([]IngestItem, 100)
	for i := range fleet {
		fleet[i] = switchRecord(i, 48)
	}
	if w := windows(4, fleet); len(w) != 20 || w[0] != [2]int{0, 5} {
		t.Errorf("100 switches of 51 keys with lanes: windows %v, want 20 of 5", w)
	}
	if w := windows(1, fleet); len(w) != 1 {
		t.Errorf("100 switches with one lane: windows %v, want one", w)
	}
	// Servers with few keys still batch ingestBatch records.
	servers := make([]IngestItem, 150)
	for i := range servers {
		servers[i] = IngestItem{SerialNumber: fmt.Sprintf("SRV-%d", i), ManagementIP: fmt.Sprintf("10.1.%d.%d", i>>8, i&0xff)}
	}
	if w := windows(4, servers); len(w) != 2 || w[0] != [2]int{0, ingestBatch} {
		t.Errorf("150 servers of 2 keys with lanes: windows %v, want %d and the rest", w, ingestBatch)
	}
	// A record over the limit is alone, the records around it batch.
	mixed := append(append(slices.Clone(fleet[:3]), hostile), fleet[3:6]...)
	if w := windows(4, mixed); !slices.Equal(w, [][2]int{{0, 3}, {3, 4}, {4, 7}}) {
		t.Errorf("switches around a record over the limit: windows %v", w)
	}
	if w := windows(4, append([]IngestItem{hostile}, fleet[:2]...)); !slices.Equal(w, [][2]int{{0, 1}, {1, 3}}) {
		t.Errorf("a record over the limit first: windows %v", w)
	}
	// Records the frame queued for review (no type) are not locked and do
	// not count.
	h := &Handler{}
	frame := &batchFrame{items: fleet[:12], typeIDs: make([]string, 12), identity: ic, locking: true}
	for i := range 12 {
		if i%2 == 0 {
			frame.typeIDs[i] = "type"
		}
	}
	if end := h.windowEnd("org-1", frame, 0); end != 10 {
		t.Errorf("window over records without type ends at %d, want 10 (five typed switches)", end)
	}
}

func TestRetryReason(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{fmt.Errorf("create ci: %w", &pgconn.PgError{Code: "23505", ConstraintName: "uq_ci_org_serial"}), "identity"},
		{&pgconn.PgError{Code: "23505", ConstraintName: "uq_ci_org_hardware_uuid"}, "identity"},
		{&pgconn.PgError{Code: "23505", ConstraintName: "uq_ci_org_primary_mac"}, "identity"},
		{&pgconn.PgError{Code: "23505", ConstraintName: "idx_attr_0123_u"}, ""},
		{fmt.Errorf("x: %w", &pgconn.PgError{Code: "40P01"}), "deadlock"},
		{&pgconn.PgError{Code: "40001"}, "serialization"},
		{fmt.Errorf("x: %w", errNearLimit), "near_limit"},
		{fmt.Errorf("lock identity keys: %w", &pgconn.PgError{Code: "53200", Message: "out of shared memory"}), "out_of_memory"},
		{fmt.Errorf("lock identity keys: %w", &pgconn.PgError{Code: "55P03", Message: "canceling statement due to lock timeout"}), "lock_timeout"},
		{errors.New("database down"), ""},
	} {
		if got := retryReason(c.err); got != c.want {
			t.Errorf("retryReason(%v) = %q, want %q", c.err, got, c.want)
		}
	}
	// Only shared memory and a lock timeout make the new attempt wait,
	// longer each time, and never past the context.
	start := time.Now()
	if !retryPause(context.Background(), "deadlock", 2) || time.Since(start) >= outOfMemoryPause {
		t.Error("a retry after losing against another lane waits")
	}
	start = time.Now()
	if !retryPause(context.Background(), "out_of_memory", 1) || time.Since(start) < 2*outOfMemoryPause {
		t.Errorf("second retry after out of shared memory waited %v, want %v", time.Since(start), 2*outOfMemoryPause)
	}
	start = time.Now()
	if !retryPause(context.Background(), "lock_timeout", 0) || time.Since(start) < outOfMemoryPause {
		t.Errorf("first retry after a lock timeout waited %v, want %v", time.Since(start), outOfMemoryPause)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if retryPause(ctx, "out_of_memory", 0) {
		t.Error("the pause outlasts the context")
	}
}

// TestQueuedDevicesRollback: the unlicensed devices a failed record queued
// are taken back, the ones before stay; keys loaded in the failed attempt
// are loaded again.
func TestQueuedDevicesRollback(t *testing.T) {
	var q queuedDevices
	if m := q.mark(); m != -1 {
		t.Fatalf("mark before load: %d", m)
	}
	q.keys = map[string]bool{"serial:a": true}
	q.queue("serial:b")
	mark := q.mark()
	q.queue("serial:c")
	q.queue("serial:b")
	q.rollback(mark)
	if !q.keys["serial:a"] || !q.keys["serial:b"] || q.keys["serial:c"] {
		t.Errorf("after rollback: %v", q.keys)
	}
	q.rollback(-1)
	if q.keys != nil {
		t.Errorf("rollback before the load keeps keys: %v", q.keys)
	}
}

// goneSource is an ingest source whose consumer was deleted: every fetch
// finds no responder.
type goneSource struct {
	err      error
	rechecks atomic.Int32
	again    bool
}

func (s *goneSource) Fetch(context.Context, int) ([]ingestDelivery, error) {
	return nil, fmt.Errorf("fetch: %w", s.err)
}

func (s *goneSource) Recheck(context.Context) bool {
	s.rechecks.Add(1)
	return s.again
}

// TestConsumerGoneIsNoWorkerError covers critique B1 of WP-334: a fetch at a
// deleted consumer (no responders, or deleted while pulling) checks the
// consumer instead of counting a worker error, and a lane whose consumer an
// operator removed ends.
func TestConsumerGoneIsNoWorkerError(t *testing.T) {
	errorsBefore := testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("ingest"))
	for _, err := range []error{nats.ErrNoResponders, jetstream.ErrConsumerDeleted, jetstream.ErrConsumerNotFound} {
		src := &goneSource{err: err}
		done := make(chan struct{})
		go func() {
			runIngestConsumer(context.Background(), src, NewHandler(NewMemoryRepository(), ci.NewMemoryRepository()), make(chan struct{}, 1))
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatalf("%v: the lane did not end", err)
		}
		if src.rechecks.Load() != 1 {
			t.Errorf("%v: %d rechecks, want 1", err, src.rechecks.Load())
		}
	}
	// An own lane is checked (and created again) as long as it runs.
	src := &goneSource{err: nats.ErrNoResponders, again: true}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	runIngestConsumer(ctx, src, NewHandler(NewMemoryRepository(), ci.NewMemoryRepository()), nil)
	if src.rechecks.Load() < 2 {
		t.Errorf("own lane: %d rechecks", src.rechecks.Load())
	}
	if after := testutil.ToFloat64(observability.WorkerErrors.WithLabelValues("ingest")); after != errorsBefore {
		t.Errorf("worker errors %v -> %v; a deleted consumer is no worker failure", errorsBefore, after)
	}
}

// barrier lets two parties through together, or each alone after timeout.
type barrier struct {
	mu      sync.Mutex
	waiting chan struct{}
	met     atomic.Int32
}

func (b *barrier) wait(timeout time.Duration) {
	b.mu.Lock()
	if b.waiting != nil {
		close(b.waiting)
		b.waiting = nil
		b.met.Add(1)
		b.mu.Unlock()
		return
	}
	ch := make(chan struct{})
	b.waiting = ch
	b.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(timeout):
		b.mu.Lock()
		if b.waiting == ch {
			b.waiting = nil
		}
		b.mu.Unlock()
	}
}

// laneSerials returns two serial numbers in different lanes of 4, and two
// in the same lane.
func laneSerials(t *testing.T, prefix string) (a, b, c string) {
	t.Helper()
	a = prefix + "-0"
	la := IngestLane(&IngestItem{SerialNumber: a}, 4)
	for i := 1; b == "" || c == ""; i++ {
		s := fmt.Sprintf("%s-%d", prefix, i)
		switch l := IngestLane(&IngestItem{SerialNumber: s}, 4); {
		case l != la && b == "":
			b = s
		case l == la && c == "":
			c = s
		}
	}
	return a, b, c
}

// lockingMemory is the memory repository as one that can take the ingest
// locks (TxRunner, IdentityLocker, CICountLocker), so that the lanes of an
// organization run side by side; it runs no transaction and takes no lock.
type lockingMemory struct{ *MemoryRepository }

func (lockingMemory) RunInTx(ctx context.Context, _ string, fn func(context.Context) error) error {
	return fn(ctx)
}

func (lockingMemory) LockIdentities(context.Context, string, IdentityLocks) error { return nil }

func (lockingMemory) LockCICount(context.Context, string, bool) (int64, error) { return 0, nil }

// TestLanesRunConcurrentlyPerOrg covers WP-334 (AC 1): the records of one
// organization in different lanes are processed at the same time, those of
// one lane, and all with one lane, one after another. Without a repository
// that can lock (--no-db with NATS) the lanes of an organization never run
// side by side (holistic review).
func TestLanesRunConcurrentlyPerOrg(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	run := func(lockable bool, lanes int, s1, s2 string) bool {
		repo := NewMemoryRepository()
		repo.SeedCIType("server", "server")
		var r Repository = repo
		if lockable {
			r = lockingMemory{repo}
		}
		h := NewHandler(r, ci.NewMemoryRepository()).WithIngestLanes(lanes)
		b := &barrier{}
		h.beforeCreate = func(context.Context) { b.wait(500 * time.Millisecond) }
		msg := func(serial string) []IngestMessage {
			r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, Serial: serial}, time.Now().Add(-time.Minute))
			return newIngestMessages(ctx, tenant.TenantInfo{OrganizationID: "org-1"}, "col-1", []IngestItem{itemFromRecord(&r)})
		}
		var wg sync.WaitGroup
		for _, s := range []string{s1, s2} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if resp, err := h.ProcessMessages(ctx, msg(s)); err != nil || resp.Created != 1 {
					t.Errorf("%s: %+v, %v", s, resp, err)
				}
			}()
		}
		wg.Wait()
		return b.met.Load() == 1
	}
	a, other, same := laneSerials(t, "LANES-CONC")
	if !run(true, 4, a, other) {
		t.Error("two lanes of one organization did not run at the same time")
	}
	if run(true, 4, a, same) {
		t.Error("one lane ran two batches at the same time")
	}
	if run(true, 1, a, other) {
		t.Error("with one lane two batches ran at the same time")
	}
	if run(false, 4, a, other) {
		t.Error("without the ingest locks two lanes of one organization ran at the same time")
	}
}

// TestNoDBLanesKeepOneDevice is the scenario of the holistic review of
// WP-334: --no-db with NATS (memory repositories, RETICORA_INGEST_LANES=4).
// Two records of one switch, one with serial number, IP and MAC and one of
// a sweep with MAC and IP only, are consumed at the same time, each from the
// lane IngestLane picks for it. The memory repository takes no identity
// lock and enforces no unique key, so the lanes of an organization must not
// run side by side: one CI, and the second record never reached the
// creation together with the first.
func TestNoDBLanesKeepOneDevice(t *testing.T) {
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})
	const mac, ip = "00:1b:21:cc:00:01", "10.9.0.1"
	seen := time.Now().Add(-time.Minute)
	sweep := plugins.RecordFromResult("sweep", &plugins.Result{CIType: "switch", Name: "sweep-" + ip, IP: ip, MAC: mac}, seen)
	var snmp plugins.DeviceRecord
	for i := 0; ; i++ {
		serial := fmt.Sprintf("SW-NODB-%d", i)
		snmp = plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "core-sw", IP: ip, MAC: mac, Serial: serial}, seen)
		a, b := itemFromRecord(&snmp), itemFromRecord(&sweep)
		if IngestLane(&a, 4) != IngestLane(&b, 4) {
			break
		}
	}
	for _, lanes := range []int{1, 4} {
		repo := NewMemoryRepository()
		repo.SeedCIType("switch", "switch")
		cis := ci.NewMemoryRepository()
		h := NewHandler(repo, cis).WithIngestLanes(lanes)
		b := &barrier{}
		h.beforeCreate = func(context.Context) { b.wait(500 * time.Millisecond) }
		var wg sync.WaitGroup
		for _, rec := range []plugins.DeviceRecord{snmp, sweep} {
			item := itemFromRecord(&rec)
			msgs := newIngestMessages(ctx, tenant.TenantInfo{OrganizationID: "org-1"}, "col-1", []IngestItem{item})
			raw, err := json.Marshal(&msgs[0])
			if err != nil {
				t.Fatal(err)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				d := &fakeDelivery{data: raw, delivered: 1}
				consumeBatch(context.Background(), h, []ingestDelivery{d})
				if d.result != "ack" {
					t.Errorf("%d lanes, lane %d: delivery %s", lanes, IngestLane(&item, lanes), d.result)
				}
			}()
		}
		wg.Wait()
		items, _, err := cis.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || b.met.Load() != 0 {
			t.Errorf("%d lanes without the ingest locks: %d CIs for one switch, both records decided to create together %d times; want 1 and 0",
				lanes, len(items), b.met.Load())
		}
	}
}

// deadlockOnce fails the first creation with a deadlock (40P01), as a lost
// race against another lane or instance does.
type deadlockOnce struct {
	ci.Repository
	failed atomic.Bool
}

func (r *deadlockOnce) Create(ctx context.Context, item *ci.Item) error {
	if r.failed.CompareAndSwap(false, true) {
		return &pgconn.PgError{Code: "40P01", Message: "deadlock detected"}
	}
	return r.Repository.Create(ctx, item)
}

// TestQueuedRecordsTakeLocks covers the holistic review of WP-334: records
// consumed from the ingest queue take the ingest locks and retry a lost race
// whatever the lane count, since other instances, the former consumer and
// drain lanes process the same organizations with one lane too; the HTTP
// ingest without a queue keeps processing as before (WP-332/333 budgets),
// and nothing locks without a repository that can.
func TestQueuedRecordsTakeLocks(t *testing.T) {
	queued := withQueued(context.Background())
	lockable := NewHandler(lockingMemory{NewMemoryRepository()}, ci.NewMemoryRepository())
	memory := NewHandler(NewMemoryRepository(), ci.NewMemoryRepository())
	for _, c := range []struct {
		name             string
		h                *Handler
		lanes            int
		ctx              context.Context
		locking, sideway bool
	}{
		{"lockable, one lane, HTTP", lockable, 1, context.Background(), false, false},
		{"lockable, one lane, queue", lockable, 1, queued, true, false},
		{"lockable, four lanes, HTTP", lockable, 4, context.Background(), true, true},
		{"lockable, four lanes, queue", lockable, 4, queued, true, true},
		{"memory, one lane, queue", memory, 1, queued, false, false},
		{"memory, four lanes, queue", memory, 4, queued, false, false},
	} {
		c.h.WithIngestLanes(c.lanes)
		if got := c.h.locking(c.ctx); got != c.locking {
			t.Errorf("%s: locking %v, want %v", c.name, got, c.locking)
		}
		if got := c.h.parallel(); got != c.sideway {
			t.Errorf("%s: lanes side by side %v, want %v", c.name, got, c.sideway)
		}
	}

	// One lane: a record consumed from the queue that loses a race runs
	// again at once and is acknowledged; through HTTP it fails as before.
	record := func(serial string) IngestMessage {
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, Serial: serial}, time.Now().Add(-time.Minute))
		return newIngestMessages(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"}, "col-1",
			[]IngestItem{itemFromRecord(&r)})[0]
	}
	handler := func() *Handler {
		repo := NewMemoryRepository()
		repo.SeedCIType("server", "server")
		return NewHandler(lockingMemory{repo}, &deadlockOnce{Repository: ci.NewMemoryRepository()}).WithIngestLanes(1)
	}
	retriesBefore := IngestRecordRetries()
	msg := record("QUEUED-1")
	raw, err := json.Marshal(&msg)
	if err != nil {
		t.Fatal(err)
	}
	d := &fakeDelivery{data: raw, delivered: 1}
	consumeBatch(context.Background(), handler(), []ingestDelivery{d})
	if d.result != "ack" || IngestRecordRetries()-retriesBefore != 1 {
		t.Errorf("one lane, from the queue: delivery %s after %d retries, want ack after 1", d.result, IngestRecordRetries()-retriesBefore)
	}
	if _, err := handler().ProcessMessages(context.Background(), []IngestMessage{record("HTTP-1")}); err == nil {
		t.Error("one lane, HTTP without a queue: the lost race was retried; want the error as before")
	}
}

// flakyCIs fails the creation of one serial number.
type flakyCIs struct {
	ci.Repository
	fail string
}

func (r flakyCIs) Create(ctx context.Context, item *ci.Item) error {
	if item.SerialNumber == r.fail {
		return errors.New("injected failure")
	}
	return r.Repository.Create(ctx, item)
}

// TestConsumeBatchAcksSettledRecords covers WP-334: when a group fails, its
// records processed before the failure and those processed before the batch
// are acknowledged (they are in the ledger), the others redelivered.
func TestConsumeBatchAcksSettledRecords(t *testing.T) {
	repo := NewMemoryRepository()
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	h := NewHandler(repo, flakyCIs{Repository: cis, fail: "ACK-2"})
	t0 := time.Now().UTC().Add(-time.Hour)
	rec := func(serial string, at time.Time) IngestMessage {
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, Serial: serial}, at)
		return newIngestMessages(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"}, "col-1",
			[]IngestItem{itemFromRecord(&r)})[0]
	}
	earlier := rec("ACK-0", t0)
	if _, err := h.ProcessMessages(context.Background(), []IngestMessage{earlier}); err != nil {
		t.Fatal(err)
	}
	msgs := []IngestMessage{earlier, rec("ACK-1", t0.Add(time.Second)), rec("ACK-2", t0.Add(2*time.Second)), rec("ACK-3", t0.Add(3*time.Second))}
	var deliveries []ingestDelivery
	var fakes []*fakeDelivery
	for i := range msgs {
		raw, _ := json.Marshal(&msgs[i])
		d := &fakeDelivery{data: raw, delivered: 1}
		fakes = append(fakes, d)
		deliveries = append(deliveries, d)
	}
	recordsBefore := testutil.ToFloat64(laneRecords.WithLabelValues("0"))
	consumeBatch(context.Background(), h, deliveries)
	want := []string{"ack", "ack", "nak", "nak"}
	for i, d := range fakes {
		if d.result != want[i] {
			t.Errorf("%s: %s, want %s", msgs[i].Item.SerialNumber, d.result, want[i])
		}
	}
	if n := testutil.ToFloat64(laneRecords.WithLabelValues("0")) - recordsBefore; n != 2 {
		t.Errorf("reticora_ingest_lane_records_total grew by %v, want 2", n)
	}
	items, _, _ := cis.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
	if len(items) != 2 {
		t.Errorf("%d CIs, want ACK-0 and ACK-1", len(items))
	}
}

// TestLaneMetricsRegistered: the lane metrics are part of RegisterMetrics.
func TestLaneMetricsRegistered(t *testing.T) {
	reg := prometheus.NewRegistry()
	RegisterMetrics(reg)
	laneBacklog.Reset()
	laneBacklog.WithLabelValues("legacy").Set(3)
	recordRetries.WithLabelValues("deadlock").Add(0)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range families {
		names[f.GetName()] = true
	}
	for _, name := range []string{"reticora_ingest_lanes", "reticora_ingest_lane_backlog", "reticora_ingest_record_retries_total"} {
		if !names[name] {
			t.Errorf("%s not registered", name)
		}
	}
	laneBacklog.Reset()
}

// listingStream is a stream whose consumer listing counts its calls and
// lists infos; every other method is unused.
type listingStream struct {
	jetstream.Stream
	listings atomic.Int32
	infos    []*jetstream.ConsumerInfo
}

func (s *listingStream) ListConsumers(context.Context) jetstream.ConsumerInfoLister {
	s.listings.Add(1)
	ch := make(chan *jetstream.ConsumerInfo, len(s.infos))
	for _, info := range s.infos {
		ch <- info
	}
	close(ch)
	return consumerInfos{ch}
}

type consumerInfos struct{ ch chan *jetstream.ConsumerInfo }

func (l consumerInfos) Info() <-chan *jetstream.ConsumerInfo { return l.ch }
func (consumerInfos) Err() error                             { return nil }

// TestReadinessDetailFromSample covers the holistic review of WP-334: the
// ingest detail of the unauthenticated /readyz is the backlog the 30-s
// sampler (Backlog) read last, with the time it was read; a request never
// lists the stream's consumers. Before the first sample it fails with
// ErrNoBacklogSample, which /readyz reports as "unavailable" without a WARN.
func TestReadinessDetailFromSample(t *testing.T) {
	stream := &listingStream{infos: []*jetstream.ConsumerInfo{
		{Name: IngestConsumer, NumPending: 2},
		{Name: laneConsumerName(0), NumPending: 5, NumAckPending: 1},
		{Name: laneConsumerName(1)},
		{Name: "reticora-other"},
	}}
	q := &NATSIngestQueue{stream: stream, lanes: 2}
	if detail, err := q.ReadinessDetail(context.Background()); !errors.Is(err, ErrNoBacklogSample) {
		t.Errorf("detail before the first sample: %v, %v; want ErrNoBacklogSample", detail, err)
	}
	before := time.Now().Add(-time.Second)
	if total, err := q.Backlog(context.Background()); err != nil || total != 8 {
		t.Fatalf("Backlog = %d, %v; want 8", total, err)
	}
	laneBacklog.Reset()
	for range 50 {
		detail, err := q.ReadinessDetail(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		b, ok := detail.(*IngestBacklog)
		if !ok || b.Lanes != 2 || b.Total != 8 || len(b.Backlog) != 3 || b.Backlog["legacy"] != 2 || b.Backlog["0"] != 6 ||
			b.SampledAt.Before(before) || b.SampledAt.After(time.Now()) {
			t.Fatalf("detail %+v, want the sample of 8 with its time", detail)
		}
	}
	if n := stream.listings.Load(); n != 1 {
		t.Errorf("%d consumer listings for one sample and 50 details, want 1", n)
	}
	raw, err := json.Marshal(q.sample.Load())
	if err != nil || !regexp.MustCompile(`"sampled_at":"\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d{1,3})?Z"`).Match(raw) {
		t.Errorf("JSON %s (%v), want sampled_at in UTC with at most milliseconds", raw, err)
	}
}
