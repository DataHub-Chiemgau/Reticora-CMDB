package database

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// The ingest locks (NFR-03, WP-334, E-56) and the max_cis admission
// (ENT-03, SR-09, WP-835): transaction-scoped advisory locks that every
// path creating CIs takes in the transaction that creates them, the
// discovery ingest and its review resolutions as well as ci.Service
// (ci.CIAdmitter). They live here so that every channel derives the same
// class and key (K21).
//
// They use the two-argument form of the advisory locks,
// pg_advisory_xact_lock(class, key), whose lock tag never equals the tag of
// a one-argument (bigint) lock: the scheduler's leader and job locks, the
// location tree and the operator audit stay out of reach of keys derived
// from what a tenant reports. Their classes differ from those of the audit
// chain (0x52657469), the event outbox (0x45766e74) and its publisher
// (0x45766e75), and from each other, so the organization's identity lock,
// taken before the keys, is never one of them.
const (
	// IdentityOrgLockClass is the class of an organization's identity
	// lock, held shared next to the identity keys or exclusive instead of
	// them.
	IdentityOrgLockClass int32 = 0x52496f72 // "RIor"
	// IdentityKeyLockClass is the class of the identity keys (serial
	// number, hardware UUID, MAC, management IP in its VRF).
	IdentityKeyLockClass int32 = 0x52496b79 // "RIky"
	// CICountLockClass is the class of an organization's max_cis lock.
	CICountLockClass int32 = 0x52436e74 // "RCnt"
)

// IngestLockTimeout is the default bound of the wait for the identity and
// max_cis locks; the statement fails with 55P03 when it passes.
const IngestLockTimeout = 20 * time.Second

// identityLockNamespace prefixes every hashed identity key and the
// organization's identity lock; ciCountLockNamespace the max_cis lock.
const (
	identityLockNamespace = "reticora/ingest-identity\x00"
	ciCountLockNamespace  = "reticora/max-cis\x00"
)

// ErrNoLockTx refuses an ingest lock or the max_cis admission outside a
// context transaction (fail closed): a transaction-scoped lock taken in a
// transaction of its own would end before the write it guards.
var ErrNoLockTx = errors.New("advisory lock: no context transaction")

// IngestLockKey is the key of text within its lock class: the first four
// bytes of its SHA-256. A tenant cannot choose a value whose key equals a
// given one at less than about 2^32 hashes, and two values that share a key
// only serialize their transactions, as one value would.
func IngestLockKey(text string) int32 {
	sum := sha256.Sum256([]byte(text))
	return int32(binary.BigEndian.Uint32(sum[:4])) //nolint:gosec // the bits of the hash
}

// CICountLockKey is the key of an organization's max_cis lock in
// CICountLockClass.
func CICountLockKey(orgID string) int32 {
	return IngestLockKey(ciCountLockNamespace + strings.ToLower(orgID))
}

// IdentityOrgLockKey is the key of an organization's identity lock in
// IdentityOrgLockClass.
func IdentityOrgLockKey(orgID string) int32 {
	return IngestLockKey(identityLockNamespace + strings.ToLower(orgID))
}

// IdentityValueLockKey is the key of an identity value of an organization
// in IdentityKeyLockClass; kind is "serial", "uuid", "mac" or "ip" and
// value its canonical form.
func IdentityValueLockKey(orgID, kind, value string) int32 {
	return IngestLockKey(identityLockNamespace + strings.ToLower(orgID) + "\x00" + kind + "\x00" + value)
}

// boundedLocksSQL is one statement that sets lock_timeout to the parameter
// timeout, takes the advisory locks of locks and restores the
// transaction's lock_timeout: prev is read before bound sets the timeout
// (bound reads prev), locks reads bound in its FROM list, so its lock
// functions run after it, and the outer set_config runs after count(*) has
// taken every lock.
func boundedLocksSQL(timeout, locks string) string {
	return `WITH prev AS MATERIALIZED (SELECT current_setting('lock_timeout') AS v),
bound AS MATERIALIZED (SELECT set_config('lock_timeout', ` + timeout + `, true) FROM prev),
locked AS MATERIALIZED (` + locks + `)
SELECT set_config('lock_timeout', prev.v, true), n.locks FROM prev, (SELECT count(*) AS locks FROM locked) n`
}

// IdentityLockSQL takes the identity locks of a transaction in one
// statement: the organization's lock ($1, IdentityOrgLockClass) first,
// shared, or exclusive with $3, then the keys ($2, IdentityKeyLockClass)
// exclusively in ascending order, each wait bounded by $4. The lock
// functions run after the sort (EXPLAIN: Result above Sort), so
// transactions that share keys never wait for each other in a cycle.
var IdentityLockSQL = boundedLocksSQL("$4", `SELECT CASE WHEN s.o = 0 AND NOT $3 THEN pg_advisory_xact_lock_shared(s.c, s.k) ELSE pg_advisory_xact_lock(s.c, s.k) END
	FROM bound, (SELECT 0 AS o, `+strconv.Itoa(int(IdentityOrgLockClass))+` AS c, $1::int AS k
		UNION ALL SELECT DISTINCT 1, `+strconv.Itoa(int(IdentityKeyLockClass))+`, unnest($2::int[])) s
	ORDER BY s.o, s.k`)

// CICountLockSQL takes the max_cis lock ($2 in CICountLockClass), shared or
// exclusive with $3, its wait bounded by $1.
var CICountLockSQL = boundedLocksSQL("$1", `SELECT CASE WHEN $3 THEN pg_advisory_xact_lock(`+strconv.Itoa(int(CICountLockClass))+`, $2::int)
	ELSE pg_advisory_xact_lock_shared(`+strconv.Itoa(int(CICountLockClass))+`, $2::int) END FROM bound`)

// lockTimeoutSetting renders timeout as a lock_timeout value; a timeout
// that is not positive is IngestLockTimeout.
func lockTimeoutSetting(timeout time.Duration) string {
	if timeout <= 0 {
		timeout = IngestLockTimeout
	}
	return strconv.FormatInt(timeout.Milliseconds(), 10) + "ms"
}

// inLockTx runs fn in the context transaction of orgID's request scope
// (WithRequestTenant joins it); without one it fails with ErrNoLockTx.
func inLockTx(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	if !InTx(ctx) {
		return ErrNoLockTx
	}
	// The pool is never used: the call joins the context transaction.
	return WithRequestTenant(ctx, nil, orgID, fn)
}

// LockCICount takes orgID's max_cis lock, exclusive or shared, until the
// end of the context transaction and then counts the active CIs of the
// organization, also those outside the scope of the transaction
// (ci_active_count, migration 000186, F-403). The count is a statement of
// its own, so it sees what every holder before committed. timeout bounds
// the wait (IngestLockTimeout when not positive). Without a context
// transaction it fails with ErrNoLockTx.
func LockCICount(ctx context.Context, orgID string, exclusive bool, timeout time.Duration) (int64, error) {
	var count int64
	err := inLockTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, CICountLockSQL, lockTimeoutSetting(timeout), CICountLockKey(orgID), exclusive); err != nil {
			return fmt.Errorf("lock max_cis: %w", err)
		}
		if err := tx.QueryRow(ctx, `SELECT ci_active_count()`).Scan(&count); err != nil {
			return fmt.Errorf("count cis: %w", err)
		}
		return nil
	})
	return count, err
}

// LockQuota takes the lock of quota for orgID until the end of the context
// transaction (PKG-04, WP-881; pattern LockCICount): every admission of
// the quota (entitlement.Service.AdmitCount) runs one after the other, so
// its count sees what the holder before committed. The key is
// hashtextextended('quota:' || quota || ':' || organization_id, 0). Without
// a context transaction it fails with ErrNoLockTx.
func LockQuota(ctx context.Context, orgID, quota string) error {
	return inLockTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('quota:' || $1 || ':' || $2, 0))`, quota, orgID); err != nil {
			return fmt.Errorf("lock quota %s: %w", quota, err)
		}
		return nil
	})
}

// LockIdentities takes the identity locks of the context transaction with
// IdentityLockSQL: the organization's lock org (IdentityOrgLockKey) shared,
// or exclusive with exclusive, then keys (IdentityValueLockKey; ignored
// with exclusive). It must be the transaction's first statement, so that a
// lookup after it sees every transaction that held one of the keys before.
// Without a context transaction it fails with ErrNoLockTx.
func LockIdentities(ctx context.Context, orgID string, org int32, exclusive bool, keys []int32, timeout time.Duration) error {
	if keys == nil || exclusive {
		keys = []int32{}
	}
	return inLockTx(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, IdentityLockSQL, org, keys, exclusive, lockTimeoutSetting(timeout)); err != nil {
			return fmt.Errorf("lock identity keys: %w", err)
		}
		return nil
	})
}
