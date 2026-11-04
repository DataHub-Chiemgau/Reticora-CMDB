package ci_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// admissionLimit is a stored max_cis limit: the entitlement error from
// limit on, as entitlement.Service.AllowCreate reports it.
type admissionLimit struct{ limit int64 }

func (l *admissionLimit) AllowCreate(_ context.Context, _, quota string, current int64) error {
	if current >= l.limit {
		return &entitlement.LimitExceededError{FeatureKey: quota, Limit: l.limit, Current: current}
	}
	return nil
}

// unlimited is an organization without a stored max_cis limit.
type unlimited struct{}

func (unlimited) AllowCreate(context.Context, string, string, int64) error { return nil }

// nopIndexer is a search index that keeps nothing: the CI repository runs
// behind the indexing decorator as in production (RunInTx forwarded).
type nopIndexer struct{}

func (nopIndexer) IndexDocument(context.Context, *ci.Document) error            { return nil }
func (nopIndexer) DeleteDocument(context.Context, string, string, string) error { return nil }

// TestCreateAdmission covers the max_cis admission of ci.Service (ENT-03,
// SR-09, WP-835) against PostgreSQL. Create, Restore and Unmerge admit and
// write in one transaction with the organization's max_cis lock
// (ci.CIAdmitter, the lock of the ingest lanes) and count again under it:
//   - 10 parallel creations (POST /cis) one below the limit create exactly
//     one CI, the other nine are 403 entitlement-limit; before WP-835 the
//     count ran outside the transaction without a lock and several passed;
//   - restore: 10 parallel restores of deleted CIs one below the limit
//     bring back exactly one;
//   - a creation costs at most two statements more than one without a
//     limit (the lock and the count);
//   - the admission without a context transaction fails closed, without a
//     stored limit it takes no lock.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCreateAdmission(t *testing.T) {
	f := scopetest.Seed(t, "3f")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	active := func() int64 {
		t.Helper()
		var n int64
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	repo := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()), nopIndexer{})
	limit := &admissionLimit{}
	svc := ci.NewServiceWithLimits(repo, limit)

	// race runs op 10 times at once and returns how many succeeded; every
	// failure must be the entitlement error, answered with 403.
	race := func(name string, op func(i int) error) int {
		t.Helper()
		start := make(chan struct{})
		errs := make([]error, 10)
		var wg sync.WaitGroup
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				errs[i] = op(i)
			}()
		}
		close(start)
		wg.Wait()
		ok := 0
		for i, err := range errs {
			if err == nil {
				ok++
				continue
			}
			var exceeded *entitlement.LimitExceededError
			w := httptest.NewRecorder()
			if !errors.As(err, &exceeded) || !api.WriteEntitlementError(w, err) || w.Code != http.StatusForbidden {
				t.Errorf("%s %d: %v (status %d), want 403 entitlement-limit", name, i, err, w.Code)
			}
		}
		return ok
	}

	t.Run("create", func(t *testing.T) {
		limit.limit = active() + 1
		created := race("create", func(i int) error {
			return svc.Create(ctx, &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: serverType,
				Name: fmt.Sprintf("admission-create-%d", i), Status: "active"})
		})
		if n := active(); created != 1 || n != limit.limit {
			t.Errorf("10 parallel creations one below the limit: %d created, %d active CIs; want 1 and the limit %d", created, n, limit.limit)
		}
	})

	t.Run("restore", func(t *testing.T) {
		ids := make([]string, 10)
		for i := range ids {
			ids[i] = f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("admission-restore-%d", i))
			if _, err := f.Admin.Exec(bg, `UPDATE ci SET deleted_at = now() - interval '1 hour' WHERE id = $1`, ids[i]); err != nil {
				t.Fatal(err)
			}
		}
		limit.limit = active() + 1
		restored := race("restore", func(i int) error {
			_, err := svc.Restore(ctx, f.OrgA, ids[i])
			return err
		})
		if n := active(); restored != 1 || n != limit.limit {
			t.Errorf("10 parallel restores one below the limit: %d restored, %d active CIs; want 1 and the limit %d", restored, n, limit.limit)
		}
	})

	t.Run("statements", func(t *testing.T) {
		trace := newQueryTrace(t, f.App)
		traced := ci.NewPGRepositoryWithAudit(trace.pool, audit.NewPGRecorder())
		far := &admissionLimit{limit: active() + 1000}
		create := func(svc *ci.Service, name string) int {
			return len(trace.run(t, func() error {
				return svc.Create(ctx, &ci.Item{OrganizationID: f.OrgA, ClientID: f.Client1, CITypeID: serverType, Name: name, Status: "active"})
			}))
		}
		// Warm the pool: the first connection switches its role.
		create(ci.NewService(traced), "admission-warm")
		without := create(ci.NewService(traced), "admission-unlimited")
		with := create(ci.NewServiceWithLimits(traced, far), "admission-limited")
		if with > without+2 {
			t.Errorf("creation with max_cis: %d statements, without: %d; the admission may add two (lock, count)", with, without)
		}
	})

	t.Run("fail closed", func(t *testing.T) {
		admitter := ci.NewCIAdmitter(&admissionLimit{limit: active() + 1})
		if err := admitter.AdmitNew(ctx, f.OrgA, 1); !errors.Is(err, database.ErrNoLockTx) {
			t.Errorf("admission without a transaction: %v, want ErrNoLockTx", err)
		}
		// Within the transaction: admitted up to the headroom, refused
		// beyond it; the lock is the ingest's (database.CICountLockKey).
		pg := ci.NewPGRepository(f.App)
		err := pg.RunInTx(ctx, f.OrgA, func(ctx context.Context) error {
			if err := admitter.AdmitNew(ctx, f.OrgA, 1); err != nil {
				return fmt.Errorf("one within the headroom: %w", err)
			}
			var exceeded *entitlement.LimitExceededError
			if err := admitter.AdmitNew(ctx, f.OrgA, 2); !errors.As(err, &exceeded) || exceeded.Current != active() {
				return fmt.Errorf("two with a headroom of one: %v, want the limit error with the count", err)
			}
			if held := maxCIsLockHeld(ctx, t, f); !held {
				return errors.New("the max_cis lock of the ingest is not held")
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
		// Without a stored limit no lock is taken.
		err = pg.RunInTx(ctx, f.OrgA, func(ctx context.Context) error {
			if aerr := ci.NewCIAdmitter(unlimited{}).AdmitNew(ctx, f.OrgA, 1); aerr != nil {
				return aerr
			}
			if maxCIsLockHeld(ctx, t, f) {
				return errors.New("max_cis lock taken without a stored limit")
			}
			return nil
		})
		if err != nil {
			t.Error(err)
		}
	})
}

// maxCIsLockHeld reports whether the transaction of ctx holds the max_cis
// lock of organization A (class database.CICountLockClass, key
// database.CICountLockKey) exclusively.
func maxCIsLockHeld(ctx context.Context, t *testing.T, f *scopetest.Fixture) bool {
	t.Helper()
	var pid int32
	var held bool
	err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&pid)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Admin.QueryRow(context.Background(), `SELECT EXISTS (SELECT 1 FROM pg_locks WHERE locktype = 'advisory'
		AND pid = $1 AND classid::bigint = $2 AND objid::bigint = $3 AND objsubid = 2 AND mode = 'ExclusiveLock' AND granted)`,
		pid, int64(uint32(database.CICountLockClass)), int64(uint32(database.CICountLockKey(f.OrgA)))).Scan(&held); err != nil {
		t.Fatal(err)
	}
	return held
}
