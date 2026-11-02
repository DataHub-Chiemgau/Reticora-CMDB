package relationship_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// TestCleanupKeepsEdgeVerifiedDuringRun covers F-411 (TOP-02, REL-08,
// WP-159 AK1) against PostgreSQL: the cleanup removes only discovery edges
// that are still unverified and unconfirmed when it deletes them. An edge a
// user verifies, or discovery re-confirms, while a run is under way stays,
// and no relationship.removed is written for it.
//
//   - Deterministic: a transaction verifies one stale edge and re-confirms
//     another; the cleanup has read both as candidates and waits on the row
//     lock; after the commit both stay, a third stale edge goes.
//   - Concurrent: 200 stale edges, eight workers verify them through
//     PGRepository.Update (PATCH /relationships/{id}) while RunOrg runs;
//     no edge whose verification answered OK is deleted afterwards.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
// Retest: cd backend && go test -count=1 -v -run '^TestCleanupKeepsEdgeVerifiedDuringRun$' ./internal/relationship/
func TestCleanupKeepsEdgeVerifiedDuringRun(t *testing.T) {
	f := scopetest.Seed(t, "ab")
	bg := context.Background()
	stale := func(name string) string {
		t.Helper()
		var id string
		src, dst := f.CI(t, f.OrgA, "", "f411-"+name+"-src"), f.CI(t, f.OrgA, "", "f411-"+name+"-dst")
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source,
				verification_state, last_seen_at, last_confirmed_at, confidence)
			VALUES ($1, $2, $3, 'connected_to', 'discovery', 'unverified', now() - interval '8 days', now() - interval '8 days', 1.0) RETURNING id::text`,
			f.OrgA, src, dst).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	exists := func(id string) bool {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci_relationship WHERE id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n == 1
	}
	removedEvents := func(id string) int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'relationship.removed' AND entity_id = $2`,
			f.OrgA, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("verified and re-confirmed while the run waits", func(t *testing.T) {
		verified, confirmed, gone := stale("verified"), stale("confirmed"), stale("gone")
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err := tx.Exec(bg, `UPDATE ci_relationship SET verification_state = 'verified', verified_at = now() WHERE id = $1`, verified); err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(bg, `UPDATE ci_relationship SET last_confirmed_at = now(), last_seen_at = now() WHERE id = $1`, confirmed); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		var res relationship.CleanupResult
		var cerr error
		go func() {
			defer close(done)
			res, cerr = relationship.NewCleanup(f.App).RunOrg(bg, f.OrgA)
		}()
		// The cleanup has read the candidates and waits on a row lock of
		// the open transaction.
		deadline := time.Now().Add(20 * time.Second)
		for {
			var waiting int
			if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database()
				AND pid <> pg_backend_pid() AND wait_event_type = 'Lock' AND query LIKE '%DELETE FROM ci_relationship%'`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				break
			}
			select {
			case <-done:
				t.Fatalf("cleanup finished without waiting on the open transaction: %+v %v", res, cerr)
			default:
			}
			if time.Now().After(deadline) {
				t.Fatal("cleanup does not wait on the row lock")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if err := tx.Commit(bg); err != nil {
			t.Fatal(err)
		}
		<-done
		if cerr != nil {
			t.Fatal(cerr)
		}
		for _, e := range [][2]string{{"verified", verified}, {"re-confirmed", confirmed}} {
			name, id := e[0], e[1]
			if !exists(id) {
				t.Errorf("edge %s during the run was deleted by the cleanup", name)
			}
			if n := removedEvents(id); n != 0 {
				t.Errorf("edge %s during the run: %d relationship.removed events, want 0", name, n)
			}
		}
		if exists(gone) || removedEvents(gone) != 1 {
			t.Errorf("untouched stale edge: exists=%v events=%d, want removed with one event", exists(gone), removedEvents(gone))
		}
		if res.Stale != 1 || res.Suppressed != 0 {
			t.Errorf("cleanup result %+v, want Stale 1 (only the untouched edge)", res)
		}
	})

	t.Run("verifications answered OK during a run are kept", func(t *testing.T) {
		repo := relationship.NewPGRepository(f.App)
		const n = 200
		for round := 0; round < 2; round++ {
			ids := make([]string, n)
			for i := range ids {
				ids[i] = stale("race")
			}
			ok := make([]bool, n)
			var wg sync.WaitGroup
			start := make(chan struct{})
			for w := 0; w < 8; w++ {
				wg.Add(1)
				go func(w int) {
					defer wg.Done()
					<-start
					for i := w; i < n; i += 8 {
						v := "verified"
						if _, err := repo.Update(f.OrgCtx(f.OrgA), f.OrgA, ids[i], relationship.UpdateRequest{VerificationState: &v, ActorID: f.User}); err == nil {
							ok[i] = true
						}
					}
				}(w)
			}
			var res relationship.CleanupResult
			var cerr error
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				time.Sleep(time.Duration(round*3) * time.Millisecond)
				res, cerr = relationship.NewCleanup(f.App).RunOrg(bg, f.OrgA)
			}()
			close(start)
			wg.Wait()
			if cerr != nil {
				t.Fatal(cerr)
			}
			verifiedOK, lost, deleted, events := 0, 0, 0, 0
			for i, id := range ids {
				e := removedEvents(id)
				events += e
				if !exists(id) {
					deleted++
					if ok[i] {
						lost++
					}
				} else if e != 0 {
					t.Errorf("round %d: edge %s kept but has %d relationship.removed events", round, id, e)
				}
				if ok[i] {
					verifiedOK++
				}
			}
			t.Logf("round %d: cleanup %+v; verifications answered OK %d of %d; deleted %d; of the verified ones deleted %d", round, res, verifiedOK, n, deleted, lost)
			if lost != 0 {
				t.Errorf("round %d: %d of %d edges whose verification answered OK were deleted by the cleanup", round, lost, verifiedOK)
			}
			if verifiedOK+deleted != n || events != deleted || res.Stale != deleted {
				t.Errorf("round %d: verified %d + deleted %d of %d, events %d, result %+v: every edge is either verified or removed with one event", round, verifiedOK, deleted, n, events, res)
			}
		}
	})
}
