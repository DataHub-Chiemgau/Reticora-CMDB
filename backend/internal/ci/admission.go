package ci

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// ErrAdmissionNoTx refuses a max_cis admission outside a transaction (fail
// closed): the lock would end before the write it admits.
var ErrAdmissionNoTx = fmt.Errorf("max_cis admission: %w", database.ErrNoLockTx)

// TxRunner runs fn in one transaction of the context's tenant scope, which
// every repository call with fn's context joins (database.ContextWithTx).
// The PostgreSQL repository implements it; Create, Restore and Unmerge of
// Service admit and write in it (WP-835).
type TxRunner interface {
	RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error
}

// CIAdmitter admits new active CIs against max_cis (ENT-03, SR-09) exactly,
// also next to other channels creating CIs at the same time: in the context
// transaction it takes the organization's max_cis lock exclusively (the
// lock of the ingest, database.LockCICount, K21), counts the active CIs
// again under it (ci_active_count, migration 000186) and asks the limit
// guard. The lock holds until the transaction ends, so a concurrent
// admission counts what this transaction creates. Every path that creates
// or reactivates CIs admits them in the transaction that writes them
// (ci.Service, the discovery review resolutions and AdoptUnlicensed;
// import WP-302).
type CIAdmitter struct {
	limit LimitGuard
}

// NewCIAdmitter returns the admission of limit; a nil limit admits
// everything.
func NewCIAdmitter(limit LimitGuard) *CIAdmitter {
	return &CIAdmitter{limit: limit}
}

// AdmitNew admits n more active CIs of orgID in the context transaction.
// Without a stored limit it takes no lock (like the ingest's
// admissionCount). When the headroom under the lock is below n it returns
// the guard's error, an *entitlement.LimitExceededError (403
// entitlement-limit), and the caller writes nothing. Without a context
// transaction it fails with ErrAdmissionNoTx.
func (a *CIAdmitter) AdmitNew(ctx context.Context, orgID string, n int64) error {
	if !database.InTx(ctx) {
		return ErrAdmissionNoTx
	}
	if a == nil || a.limit == nil || n <= 0 {
		return nil
	}
	// The guard reports the stored limit for any count; without one (or
	// without enforcement) there is nothing to lock. Another error (feature
	// not entitled, lookup failed) refuses as before.
	err := a.limit.AllowCreate(ctx, orgID, LimitFeatureKey, math.MaxInt64)
	var exceeded *entitlement.LimitExceededError
	if !errors.As(err, &exceeded) {
		return err
	}
	count, err := database.LockCICount(ctx, orgID, true, 0)
	if err != nil {
		return err
	}
	// n more fit when count+n <= limit: the guard refuses current >= limit.
	err = a.limit.AllowCreate(ctx, orgID, LimitFeatureKey, count+n-1)
	if errors.As(err, &exceeded) {
		exceeded.Current = count
	}
	return err
}
