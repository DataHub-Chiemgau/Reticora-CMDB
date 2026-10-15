package discovery_test

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// withoutIdentityLocks is the discovery repository without the identity
// locks: the control of TestIngestLanesIdentityLock.
type withoutIdentityLocks struct{ *discovery.PGRepository }

func (withoutIdentityLocks) LockIdentities(context.Context, string, discovery.IdentityLocks) error {
	return nil
}

// lanesCtx is the context of an org-wide ingest of orgID.
func lanesCtx(f *scopetest.Fixture, orgID string) context.Context {
	return tenant.WithTenant(f.OrgCtx(orgID), tenant.TenantInfo{OrganizationID: orgID, UserID: f.User})
}

// TestIngestLanesIdentityLock covers the identity lock of WP-334 (E-56
// option 1 with identity lock, PRI-10, REC-05) against PostgreSQL:
//   - control: without the lock, the records of one device that share only
//     the management IP and sysObjectID (no unique index) and run in two
//     lanes at once both create a CI;
//   - with the lock the same race ends with one CI in every round;
//   - without the lock, two records of one serial number created one by
//     one in two lanes at once: the slower insert hits the unique index,
//     the record runs again at once in a fresh transaction and matches the
//     CI (no error for the queue);
//   - an older observation never overwrites a newer value across lanes,
//     whichever lane commits first.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestLanesIdentityLock(t *testing.T) {
	f := scopetest.Seed(t, "e2")
	bg := context.Background()
	ctx := lanesCtx(f, f.OrgA)
	handler := func(repo discovery.Repository) (*discovery.Handler, *pairBarrier) {
		h := discovery.NewHandler(repo, ci.NewPGRepository(f.App)).
			WithProvenance(provenance{repo: override.NewPGRepository(f.App)}).WithIngestLanes(4)
		b := &pairBarrier{}
		discovery.SetBeforeCreate(h, func(context.Context) { b.wait(time.Second) })
		return h, b
	}
	byIP := func(ip string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND management_ip = $2 AND deleted_at IS NULL`,
			f.OrgA, ip).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	seen := time.Now().Add(-time.Minute)
	record := func(serial, ip string) plugins.DeviceRecord {
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "lock-" + ip, IP: ip, Serial: serial}, seen)
		rec.Classification.SysObjectID = "1.3.6.1.4.1.9.1.1208"
		return rec
	}
	race := func(h *discovery.Handler, ip string) {
		var wg sync.WaitGroup
		for lane, serial := range []string{"", "LOCK-" + ip} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := discovery.ProcessInLane(ctx, h, f.OrgA, lane, record(serial, ip)); err != nil {
					t.Errorf("%s, lane %d: %v", ip, lane, err)
				}
			}()
		}
		wg.Wait()
	}

	unlocked, control := handler(withoutIdentityLocks{discovery.NewPGRepository(f.App)})
	race(unlocked, "198.18.0.1")
	if control.met.Load() != 1 || byIP("198.18.0.1") != 2 {
		t.Fatalf("control without the identity lock: both decided together %d times, %d CIs; the race must create two",
			control.met.Load(), byIP("198.18.0.1"))
	}

	// The retry of a record the unique index refused (record path: an
	// unclassified device is created on its own).
	retriesBefore := discovery.IngestRecordRetries()
	for round := 0; round < 3; round++ {
		serial := fmt.Sprintf("LOCK-RETRY-%d", round)
		var wg sync.WaitGroup
		for lane := range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "generic_device", Name: serial, Serial: serial,
					IP: fmt.Sprintf("198.18.3.%d", 10*round+lane)}, seen)
				rec.Attributes = map[string]any{"classified_by": "fallback"}
				// Own UUIDs: the two reviews of the unclassified device differ
				// and do not wait for each other.
				rec.Identity.HardwareUUID = fmt.Sprintf("4c4c4544-0000-4000-8000-0000000003%d%d", round, lane)
				if _, err := discovery.ProcessInLane(ctx, unlocked, f.OrgA, lane, rec); err != nil {
					t.Errorf("retry round %d, lane %d: %v", round, lane, err)
				}
			}()
		}
		wg.Wait()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("retry round %d: %d CIs, want 1", round, n)
		}
	}
	if retried := discovery.IngestRecordRetries() - retriesBefore; retried < 3 {
		t.Errorf("%d records retried after the unique index refused them, want one per round", retried)
	}

	locked, barrier := handler(discovery.NewPGRepository(f.App))
	for round := 0; round < 5; round++ {
		ip := fmt.Sprintf("198.18.1.%d", round)
		race(locked, ip)
		if n := byIP(ip); n != 1 {
			t.Errorf("round %d: %d CIs for one device, want 1", round, n)
		}
	}
	if barrier.met.Load() != 0 {
		t.Errorf("with the identity lock two records of one device decided to create at once %d times", barrier.met.Load())
	}

	// Order across lanes (REC-05): the newer observation stays, whichever
	// lane commits first.
	discovery.SetBeforeCreate(locked, nil)
	for round, newerFirst := range []bool{true, false, true, false} {
		serial := fmt.Sprintf("LOCK-ORDER-%d", round)
		model := func(m string, at time.Time) plugins.DeviceRecord {
			return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: serial, IP: "198.18.2.1", Serial: serial, Model: m}, at)
		}
		if _, err := discovery.ProcessInLane(ctx, locked, f.OrgA, 0, model("m0", seen.Add(-time.Hour))); err != nil {
			t.Fatal(err)
		}
		newer, older := model("newer", seen.Add(-time.Minute)), model("older", seen.Add(-2*time.Minute))
		first, second := older, newer
		if newerFirst {
			first, second = newer, older
		}
		var wg sync.WaitGroup
		for lane, rec := range []plugins.DeviceRecord{first, second} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if lane == 1 {
					time.Sleep(20 * time.Millisecond)
				}
				if _, err := discovery.ProcessInLane(ctx, locked, f.OrgA, lane+1, rec); err != nil {
					t.Errorf("order round %d: %v", round, err)
				}
			}()
		}
		wg.Wait()
		var m string
		if err := f.Admin.QueryRow(bg, `SELECT model FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial).Scan(&m); err != nil {
			t.Fatal(err)
		}
		if m != "newer" {
			t.Errorf("order round %d (newer first: %v): model %q, want the newer observation", round, newerFirst, m)
		}
	}

	t.Run("bounded per transaction", func(t *testing.T) { identityLocksBounded(t, f) })
	t.Run("own lock space", func(t *testing.T) { identityLockSpace(t, f) })
	t.Run("lock timeout", func(t *testing.T) { identityLockTimeout(t, f) })
	t.Run("one lane from the queue", func(t *testing.T) { identityLockQueued(t, f) })
}

// identityLockQueued: records consumed from the ingest queue take the
// identity locks with one lane too (holistic review of WP-334). Two
// instances with RETICORA_INGEST_LANES=1 consume records of one device at
// the same time (the lane 0 consumer of one, the former consumer or a drain
// lane of the other, each with its own MaxAckPending); the mutex of one
// process does not hold the other. The records share only the management
// IP and sysObjectID, which no unique index catches. Control: the HTTP
// ingest without a queue takes no locks (the WP-332/333 budgets), so two
// instances create the device twice there.
func identityLockQueued(t *testing.T, f *scopetest.Fixture) {
	bg := context.Background()
	ctx := lanesCtx(f, f.OrgB)
	instance := func(b *pairBarrier) *discovery.Handler {
		h := discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).
			WithProvenance(provenance{repo: override.NewPGRepository(f.App)}).WithIngestLanes(1)
		discovery.SetBeforeCreate(h, func(context.Context) { b.wait(time.Second) })
		return h
	}
	seen := time.Now().Add(-time.Minute)
	record := func(serial, ip string) plugins.DeviceRecord {
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "queued-" + ip, IP: ip, Serial: serial}, seen)
		rec.Classification.SysObjectID = "1.3.6.1.4.1.9.1.1208"
		return rec
	}
	byIP := func(ip string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND management_ip = $2 AND deleted_at IS NULL`,
			f.OrgB, ip).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	control := &pairBarrier{}
	one, two := instance(control), instance(control)
	var wg sync.WaitGroup
	for i, h := range []*discovery.Handler{one, two} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := discovery.ProcessInLane(ctx, h, f.OrgB, 0, record([]string{"", "QUEUED-HTTP"}[i], "198.18.50.1")); err != nil {
				t.Errorf("control, instance %d: %v", i, err)
			}
		}()
	}
	wg.Wait()
	if control.met.Load() != 1 || byIP("198.18.50.1") != 2 {
		t.Fatalf("control (HTTP without a queue, one lane, two instances): decided together %d times, %d CIs; the race must create two",
			control.met.Load(), byIP("198.18.50.1"))
	}

	queued := &pairBarrier{}
	one, two = instance(queued), instance(queued)
	for round := 0; round < 5; round++ {
		ip := fmt.Sprintf("198.18.51.%d", round)
		results := make([][]string, 2)
		for i, h := range []*discovery.Handler{one, two} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				results[i] = discovery.ConsumeRecords(ctx, h, f.OrgB, record([]string{"", fmt.Sprintf("QUEUED-%d", round)}[i], ip))
			}()
		}
		wg.Wait()
		if results[0][0] != "ack" || results[1][0] != "ack" {
			t.Errorf("round %d: deliveries %v, want both acknowledged", round, results)
		}
		if n := byIP(ip); n != 1 {
			t.Errorf("round %d: %d CIs for one device consumed by two instances with one lane, want 1", round, n)
		}
	}
	if queued.met.Load() != 0 {
		t.Errorf("records consumed from the queue with one lane decided to create one device together %d times", queued.met.Load())
	}
}

// schedulerLeaderKey is the bigint of the scheduler's leader lock
// (jobs.lockKey of "reticora.scheduler"), a session lock the leader holds
// for as long as it leads.
func schedulerLeaderKey() int64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte("reticora.scheduler"))
	return int64(h.Sum64()) //nolint:gosec // the bits of the key
}

// advisoryTag is the lock tag of an advisory lock in pg_locks: classid and
// objid hold the two keys of the two-argument form or the high and low half
// of the bigint key; objsubid is 2 for the two-argument form, 1 otherwise.
type advisoryTag struct {
	classid, objid int64
	objsubid       int16
}

// identityLockSpace: the ingest locks cannot meet a lock of another
// subsystem (review finding, WP-334). The identity keys derive from values a
// tenant reports; in the one-argument (bigint) space a crafted serial number
// could hash to the key of the scheduler's leader lock, which the leader
// holds for as long as it leads, and every lane that takes it would wait
// forever. The identity and max_cis locks are two-argument locks of their
// own classes, and a session that holds the scheduler leader's lock and a
// one-argument lock with the bits of every one of them does not block them.
func identityLockSpace(t *testing.T, f *scopetest.Fixture) {
	bg := context.Background()
	ctx := lanesCtx(f, f.OrgB)
	repo := discovery.NewPGRepository(f.App)
	item := discovery.IngestItem{SerialNumber: "LOCKSPACE-1", PrimaryMAC: "00:1b:21:40:00:01", ManagementIP: "198.18.40.1"}
	locks := discovery.IdentityLocksOf(f.OrgB, &item)
	if len(locks.Keys) != 3 || locks.Exclusive {
		t.Fatalf("identity locks of the record: %+v, want three keys", locks)
	}
	// take takes the identity and max_cis locks in a transaction and
	// returns the tags of the advisory locks the transaction holds; the
	// lock statements leave the transaction's lock_timeout as it was.
	take := func(ctx context.Context) ([]advisoryTag, error) {
		var tags []advisoryTag
		err := repo.RunInTx(ctx, f.OrgB, func(ctx context.Context) error {
			inTx := func(fn func(tx pgx.Tx) error) error {
				return database.WithRequestTenant(ctx, f.App, f.OrgB, func(_ context.Context, tx pgx.Tx) error { return fn(tx) })
			}
			if err := inTx(func(tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `SELECT set_config('lock_timeout', '7s', true)`)
				return err
			}); err != nil {
				return err
			}
			if err := repo.LockIdentities(ctx, f.OrgB, locks); err != nil {
				return err
			}
			if _, err := repo.LockCICount(ctx, f.OrgB, true); err != nil {
				return err
			}
			var timeout string
			if err := inTx(func(tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT current_setting('lock_timeout')`).Scan(&timeout)
			}); err != nil || timeout != "7s" {
				return fmt.Errorf("lock_timeout after the lock statements: %q, want the transaction's 7s (%v)", timeout, err)
			}
			return database.WithRequestTenant(ctx, f.App, f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
				rows, err := tx.Query(ctx, `SELECT classid::bigint, objid::bigint, objsubid FROM pg_locks
					WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND granted ORDER BY 1, 2`)
				if err != nil {
					return err
				}
				tags, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (advisoryTag, error) {
					var tag advisoryTag
					return tag, row.Scan(&tag.classid, &tag.objid, &tag.objsubid)
				})
				return err
			})
		})
		return tags, err
	}
	tags, err := take(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != len(locks.Keys)+2 {
		t.Fatalf("the transaction holds %d advisory locks, want the organization's, %d keys and max_cis: %+v", len(tags), len(locks.Keys), tags)
	}
	classes := map[int64]int{}
	for _, tag := range tags {
		if tag.objsubid != 2 {
			t.Errorf("advisory lock %+v is not of the two-argument form", tag)
		}
		classes[tag.classid]++
	}
	if classes[int64(discovery.IdentityOrgLockClass)] != 1 || classes[int64(discovery.IdentityKeyLockClass)] != len(locks.Keys) ||
		classes[int64(discovery.CICountLockClass)] != 1 || len(classes) != 3 {
		t.Errorf("lock classes %v, want one organization lock, %d keys and one max_cis lock in their classes", classes, len(locks.Keys))
	}

	// Another session holds the scheduler leader's lock (or finds it held)
	// and the one-argument locks with the bits of the identity locks.
	conn, err := f.Admin.Acquire(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = conn.Exec(bg, `SELECT pg_advisory_unlock_all()`)
		conn.Release()
	}()
	if _, err := conn.Exec(bg, `SELECT pg_try_advisory_lock($1)`, schedulerLeaderKey()); err != nil {
		t.Fatal(err)
	}
	for _, tag := range tags {
		var ok bool
		if err := conn.QueryRow(bg, `SELECT pg_try_advisory_lock(($1::bigint << 32) | $2::bigint)`, tag.classid, tag.objid).Scan(&ok); err != nil || !ok {
			t.Fatalf("one-argument lock with the bits of %+v: %v %v", tag, ok, err)
		}
	}
	waitCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := take(waitCtx); err != nil {
		t.Errorf("ingest locks while another session holds the scheduler leader's lock and one-argument locks of the same bits: %v", err)
	}
}

// identityLockTimeout: no holder of an identity key stalls a lane for longer
// than the ingest lock timeout (review finding, WP-334). The lane goroutine
// serves every organization; a transaction that holds a key of a record and
// does not end makes the record's transactions fail with 55P03, the record
// runs again after a pause, and processing returns the error to the queue
// (which redelivers it) instead of waiting forever. Once the holder ends,
// the record is processed.
func identityLockTimeout(t *testing.T, f *scopetest.Fixture) {
	bg := context.Background()
	ctx := lanesCtx(f, f.OrgB)
	repo := discovery.NewPGRepository(f.App)
	const timeout = 300 * time.Millisecond
	discovery.SetLockTimeout(repo, timeout)
	h := discovery.NewHandler(repo, ci.NewPGRepository(f.App)).WithIngestLanes(4)
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "lockwait-1", Serial: "LOCKWAIT-1", IP: "198.18.41.1"},
		time.Now().Add(-time.Minute))
	item := discovery.ItemFromRecord(&rec)

	// The holder: a transaction that took the record's serial number key
	// and does not end until released.
	holder := discovery.IdentityLocksOf(f.OrgB, &discovery.IngestItem{SerialNumber: item.SerialNumber})
	held, release, done := make(chan error, 1), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = repo.RunInTx(lanesCtx(f, f.OrgB), f.OrgB, func(ctx context.Context) error {
			err := repo.LockIdentities(ctx, f.OrgB, holder)
			held <- err
			if err == nil {
				<-release
			}
			return err
		})
	}()
	if err := <-held; err != nil {
		t.Fatal(err)
	}
	retriesBefore := discovery.IngestRecordRetries()
	start := time.Now()
	// Without the lock timeout the lane would wait until this deadline.
	waitCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	_, err := discovery.ProcessItemsInLane(waitCtx, h, f.OrgB, 2, item)
	cancel()
	elapsed := time.Since(start)
	close(release)
	<-done
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("record behind a held identity key: %v, want the lock timeout (55P03)", err)
	}
	if retried := discovery.IngestRecordRetries() - retriesBefore; retried < 3 {
		t.Errorf("%d retries after the lock timeout, want 3", retried)
	}
	// The batch attempt, four record attempts and three pauses, with room.
	if elapsed > 5*timeout+600*time.Millisecond+5*time.Second {
		t.Errorf("the lane waited %v for the held key", elapsed)
	}
	if _, err := discovery.ProcessItemsInLane(ctx, h, f.OrgB, 2, item); err != nil {
		t.Fatalf("after the holder ended: %v", err)
	}
	var n int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = 'LOCKWAIT-1' AND deleted_at IS NULL`,
		f.OrgB).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d CIs of the record after the holder ended, want 1 (%v)", n, err)
	}
	t.Logf("the lane gave the record back after %v (lock timeout %v)", elapsed, timeout)
}

// lockProbe is the discovery repository that reads, in every
// identity-locking transaction right after the identity locks, how many
// advisory locks the transaction holds (pg_locks).
type lockProbe struct {
	*discovery.PGRepository
	pool *pgxpool.Pool

	mu sync.Mutex
	// most is the most advisory locks one transaction held, exclusive the
	// transactions that held one exclusive lock alone (the organization's).
	most, exclusive int
	failed          []string
}

func (p *lockProbe) LockIdentities(ctx context.Context, orgID string, locks discovery.IdentityLocks) error {
	if err := p.PGRepository.LockIdentities(ctx, orgID, locks); err != nil {
		return err
	}
	var held, exclusive int
	if err := database.WithRequestTenant(ctx, p.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE mode = 'ExclusiveLock')
			FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid() AND granted`).Scan(&held, &exclusive)
	}); err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.most = max(p.most, held)
	switch {
	case locks.Exclusive && held == 1 && exclusive == 1:
		p.exclusive++
	case locks.Exclusive || held != len(locks.Keys)+1 || exclusive != len(locks.Keys):
		p.failed = append(p.failed, fmt.Sprintf("%d keys (exclusive %v): %d advisory locks held, %d exclusive",
			len(locks.Keys), locks.Exclusive, held, exclusive))
	}
	return nil
}

// identityLocksBounded: the identity locks of a transaction stay within
// PostgreSQL's shared lock table (53200, WP-334) also for a switch fleet
// with an interface MAC per port and for a record with 20,000 MACs, while
// four lanes ingest at once: every transaction holds at most
// IdentityLockLimit+1 advisory locks, windows of switches still batch
// several records, and the record over the limit holds the organization's
// lock exclusively, alone. Every device becomes one CI.
func identityLocksBounded(t *testing.T, f *scopetest.Fixture) {
	bg := context.Background()
	ctx := lanesCtx(f, f.OrgB)
	probe := &lockProbe{PGRepository: discovery.NewPGRepository(f.App), pool: f.App}
	h := discovery.NewHandler(probe, ci.NewPGRepository(f.App)).WithIngestLanes(4)
	seen := time.Now().Add(-time.Minute)
	switchRec := func(lane, n int) plugins.DeviceRecord {
		res := &plugins.Result{CIType: "switch", Name: fmt.Sprintf("locktbl-%d-%02d", lane, n), Serial: fmt.Sprintf("LOCKTBL-%d-%02d", lane, n),
			IP: fmt.Sprintf("198.18.%d.%d", 20+lane, n), MAC: fmt.Sprintf("00:1b:21:%02x:%02x:00", lane, n)}
		for p := range 48 {
			res.Interfaces = append(res.Interfaces, plugins.NetworkInterface{Name: fmt.Sprintf("ge-0/0/%d", p), IfIndex: p + 1,
				MAC: fmt.Sprintf("00:1b:22:%02x:%02x:%02x", lane, n, p)})
		}
		return plugins.RecordFromResult("snmp", res, seen)
	}
	big := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "locktbl-macs", Serial: "LOCKTBL-MACS", IP: "198.18.30.1"}, seen)
	bigItem := discovery.ItemFromRecord(&big)
	macs := make([]any, 20000)
	for i := range macs {
		macs[i] = fmt.Sprintf("00:1b:23:%02x:%02x:%02x", i>>16, (i>>8)&0xff, i&0xff)
	}
	bigItem.Fingerprint["mac_addresses"] = macs

	var wg sync.WaitGroup
	for lane := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			recs := make([]plugins.DeviceRecord, 25)
			for n := range recs {
				recs[n] = switchRec(lane, n)
			}
			if _, err := discovery.ProcessInLane(ctx, h, f.OrgB, lane, recs...); err != nil {
				t.Errorf("lane %d: %v", lane, err)
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := discovery.ProcessItemsInLane(ctx, h, f.OrgB, 1, bigItem); err != nil {
			t.Errorf("record with 20,000 MACs: %v", err)
		}
	}()
	wg.Wait()

	var cis int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number LIKE 'LOCKTBL-%' AND deleted_at IS NULL`,
		f.OrgB).Scan(&cis); err != nil {
		t.Fatal(err)
	}
	if cis != 101 {
		t.Errorf("%d CIs of 100 switches and the record with 20,000 MACs, want 101", cis)
	}
	if probe.most > discovery.IdentityLockLimit+1 || probe.most <= 52 {
		t.Errorf("a transaction held up to %d advisory locks; want at most %d and windows of several switches (51 keys each)",
			probe.most, discovery.IdentityLockLimit+1)
	}
	if probe.exclusive == 0 {
		t.Error("the record with 20,000 MACs did not hold the organization's identity lock exclusively")
	}
	for _, msg := range probe.failed {
		t.Error("identity locks: " + msg)
	}
	t.Logf("at most %d advisory locks per transaction, %d transactions with the organization's lock exclusively", probe.most, probe.exclusive)
}

// fixedLimit is a max_cis limit for the organization of the test.
type fixedLimit struct{ limit int64 }

func (l fixedLimit) AllowCreate(_ context.Context, _, quota string, current int64) error {
	if current >= l.limit {
		return &entitlement.LimitExceededError{FeatureKey: quota, Limit: l.limit, Current: current}
	}
	return nil
}

// TestIngestLanesMaxCIs covers max_cis with lanes (WP-334, ENT-03, CH21)
// against PostgreSQL: four lanes create new devices at once; the
// organization ends with exactly as many CIs as its limit allows and holds
// every other device as unlicensed_ci. Organization A starts near the limit
// (every creation exclusive), organization B far from it, so its lanes
// start under the shared lock and turn exclusive on the way. Then four
// instances with one lane consume new devices of organization A from the
// queue at once (holistic review): the limit holds across them too.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestLanesMaxCIs(t *testing.T) {
	f := scopetest.Seed(t, "e7")
	bg := context.Background()
	counts := func(orgID string) (cis, unlicensed int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL),
			(SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'unlicensed_ci' AND status = 'open')`,
			orgID).Scan(&cis, &unlicensed); err != nil {
			t.Fatal(err)
		}
		return cis, unlicensed
	}
	for _, c := range []struct {
		org          string
		limit        int64
		rounds       int
		wantCIs      int
		wantHeldOpen int
		queued       bool
	}{
		{f.OrgA, 150, 1, 150, 250, false},
		{f.OrgB, 500, 2, 500, 300, false},
		// Four instances with one lane consume from the queue at once
		// (holistic review): 150 more on top of the 150 of the first case.
		{f.OrgA, 300, 1, 300, 500, true},
	} {
		lanes := 4
		if c.queued {
			lanes = 1
		}
		instance := func() *discovery.Handler {
			return discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App)).
				WithLimits(fixedLimit{c.limit}).WithIngestLanes(lanes)
		}
		h := instance()
		ctx := lanesCtx(f, c.org)
		var wg sync.WaitGroup
		for lane := range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				h := h
				if c.queued {
					h = instance()
				}
				for round := range c.rounds {
					recs := make([]plugins.DeviceRecord, 100)
					for i := range recs {
						serial := fmt.Sprintf("LIMIT-%v-%d-%d-%03d", c.queued, lane, round, i)
						recs[i] = plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: serial, Serial: serial,
							IP: fmt.Sprintf("10.%d.%d.%d", 40+lane, round+c.rounds*btoi(c.queued), i)}, time.Now().Add(-time.Minute))
					}
					if c.queued {
						for _, result := range discovery.ConsumeRecords(ctx, h, c.org, recs...) {
							if result != "ack" {
								t.Errorf("instance %d: delivery %s", lane, result)
								break
							}
						}
						continue
					}
					if _, err := discovery.ProcessInLane(ctx, h, c.org, lane, recs...); err != nil {
						t.Errorf("lane %d, round %d: %v", lane, round, err)
					}
				}
			}()
		}
		wg.Wait()
		if cis, held := counts(c.org); cis != c.wantCIs || held != c.wantHeldOpen {
			t.Errorf("limit %d, %d rounds of 4 x 100 devices (one lane from the queue: %v): %d CIs and %d unlicensed_ci, want %d and %d",
				c.limit, c.rounds, c.queued, cis, held, c.wantCIs, c.wantHeldOpen)
		}
	}
}

// btoi is 1 for true.
func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}
