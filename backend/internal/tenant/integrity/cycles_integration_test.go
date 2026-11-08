package integrity_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Type and composition cycles of ABN-03 in phase 2 (WP-825) against
// PostgreSQL (TEST_DATABASE_URL, scopetest.Seed):
//
//	cd backend && go test -count=1 -p 1 -run 'TestCycles' ./internal/tenant/integrity/
//
// A→B means "A has the parent B". Type parents change over
// PATCH /api/v1/ci-types/{id} (WP-248), CI parents over
// ci.PGRepository.Update (WP-273). Every case runs sequentially and in
// parallel: (a) A→B ∥ B→A; (b) with A→B in place, B→C ∥ C→A. In a
// parallel round each write runs in a request transaction that waits at the
// barrier of the matrix (gate, gateTimeout) after the write and before its
// commit. At most one write of a round is accepted; the other ends with 422
// (cycle on parent_ci_id), 409 (type cycle) or 409 conflict (40P01/40001),
// never with 5xx. Afterwards scopetest.IntegrityViolations reports no cycle.
//
// The three-party case on parent_ci_id also runs deterministically: T2 sets
// C→A and keeps its transaction open, T1 then sets B→C, and T2 commits. With
// the advisory lock of ci_check_parent_cycle (migration 000273) T1 waits for
// T2 and ends with 23514 ci_parent_cycle; without it both commit and the
// cycle A→B→C→A is stored.

// cycleCase is one test run of the cycles: the fixture, the CI type API
// and the CI repository, as an org-wide principal of organization A.
type cycleCase struct {
	t     *testing.T
	f     *scopetest.Fixture
	actor string
	types *chi.Mux
	cis   *ci.PGRepository
	seq   int
}

func newCycleCase(t *testing.T, f *scopetest.Fixture) *cycleCase {
	t.Helper()
	mux := chi.NewRouter()
	citype.NewHandler(citype.NewPGRepository(f.App)).RegisterRoutes(mux)
	return &cycleCase{t: t, f: f, actor: f.AppUser(t, f.OrgA, "wp825-actor"), types: mux, cis: ci.NewPGRepository(f.App)}
}

// ctx is the request context of the principal.
func (c *cycleCase) ctx() context.Context {
	ctx := tenant.WithTenant(c.f.OrgCtx(c.f.OrgA), tenant.TenantInfo{OrganizationID: c.f.OrgA, UserID: c.actor})
	return audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "wp825", CorrelationID: "wp825", Source: audit.SourceUI})
}

// write is one parent change; it answers an HTTP status.
type write func(ctx context.Context) int

// statusOf maps the error of a write onto the status the API answers: a
// cycle on parent_ci_id is 422 (ci.locationError), 40P01 and 40001 are 409
// (api.ClassifyDBError); any other error is 500.
func (c *cycleCase) statusOf(err error) int {
	if err == nil {
		return http.StatusOK
	}
	var verr *ci.ValidationError
	if errors.As(err, &verr) {
		for _, v := range verr.Violations {
			if v.Field == "parent_ci_id" {
				return http.StatusUnprocessableEntity
			}
		}
	}
	if status, _, _, ok := api.ClassifyDBError(err); ok && status == http.StatusConflict {
		return status
	}
	c.t.Logf("write failed: %v", err)
	return http.StatusInternalServerError
}

// parentCI is the write child→parent through ci.PGRepository.Update in a
// request transaction that waits at the barrier before its commit.
func (c *cycleCase) parentCI(child, parent string) write {
	return func(ctx context.Context) int {
		return c.statusOf(database.WithRequestTenantTx(ctx, c.f.App, c.f.OrgA, func(txCtx context.Context) error {
			if _, err := c.cis.Update(txCtx, c.f.OrgA, child, ci.UpdateRequest{ParentCIID: &parent}); err != nil {
				return err
			}
			arriveIn(txCtx)
			return nil
		}))
	}
}

// parentType is the write child→parent over PATCH /api/v1/ci-types/{id}.
// The handler runs in a request transaction, which the repository joins;
// after a 2xx answer the transaction waits at the barrier before its
// commit. A failed commit after 2xx counts as the answer the API would
// give for its error.
func (c *cycleCase) parentType(child, parent string) write {
	return func(ctx context.Context) int {
		w := httptest.NewRecorder()
		err := database.WithRequestTenantTx(ctx, c.f.App, c.f.OrgA, func(txCtx context.Context) error {
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/ci-types/"+child,
				strings.NewReader(`{"parent_type_id":"`+parent+`"}`)).WithContext(txCtx)
			req.Header.Set("Content-Type", "application/json")
			c.types.ServeHTTP(w, req)
			if w.Code < http.StatusMultipleChoices {
				arriveIn(txCtx)
			}
			return nil
		})
		if w.Code >= http.StatusMultipleChoices {
			if w.Code >= http.StatusInternalServerError {
				c.t.Logf("type write %s→%s: %d %s", child, parent, w.Code, w.Body.String())
			}
			return w.Code
		}
		if err != nil {
			return c.statusOf(err)
		}
		return w.Code
	}
}

// alone runs w outside any barrier.
func (c *cycleCase) alone(w write) int { return w(c.ctx()) }

// race runs the writes as one round of the barrier: started together, each
// a party of the round. No answer may be 5xx.
func (c *cycleCase) race(writes ...write) []int {
	c.t.Helper()
	g := newGate(len(writes))
	out := make([]int, len(writes))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, w := range writes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p := &party{g: g}
			ctx := context.WithValue(c.ctx(), partyKey{}, p)
			<-start
			out[i] = w(ctx)
			p.mark()
		}()
	}
	close(start)
	wg.Wait()
	for _, code := range out {
		if code >= http.StatusInternalServerError {
			c.t.Fatalf("answers %v: no write may end with 5xx", out)
		}
	}
	return out
}

// atMostOne fails unless at most one answer is 200 and every other one of
// refused; it returns the index of the accepted write or -1.
func (c *cycleCase) atMostOne(out []int, refused ...int) int {
	c.t.Helper()
	won := -1
	for i, code := range out {
		switch {
		case code == http.StatusOK && won < 0:
			won = i
		case code == http.StatusOK:
			c.t.Fatalf("answers %v: two writes of a cycle were accepted", out)
		default:
			ok := false
			for _, r := range refused {
				ok = ok || code == r
			}
			if !ok {
				c.t.Fatalf("answers %v: a refused write must answer one of %v", out, refused)
			}
		}
	}
	return won
}

// noCycles fails when the integrity check of organization A reports a
// violation, a cycle above all.
func (c *cycleCase) noCycles(when string) {
	c.t.Helper()
	v := scopetest.IntegrityViolations(c.t, c.f.Admin, c.f.OrgA)
	for _, name := range []string{"cycle: composition", "cycle: CI type inheritance", "cycle: CI type lineage"} {
		if v[name] > 0 {
			c.t.Fatalf("after %s: the integrity check reports %q (%d)", when, name, v[name])
		}
	}
	if len(v) > 0 {
		c.t.Fatalf("integrity violations after %s: %v", when, v)
	}
}

// name returns a new unique name with prefix.
func (c *cycleCase) name(prefix string) string {
	c.seq++
	return fmt.Sprintf("wp825-%s-%d", prefix, c.seq)
}

// typ creates an organization type without parent over the API.
func (c *cycleCase) typ(prefix string) string {
	c.t.Helper()
	key := c.name(prefix)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/ci-types",
		strings.NewReader(`{"key":"`+key+`","name":"`+key+`"}`)).WithContext(c.ctx())
	req.Header.Set("Content-Type", "application/json")
	c.types.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		c.t.Fatalf("create type %s: %d %s", key, w.Code, w.Body.String())
	}
	var out citype.Type
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		c.t.Fatal(err)
	}
	return out.ID
}

// ciOf creates a CI of client 1 without parent.
func (c *cycleCase) ciOf(prefix string) string {
	return c.f.CI(c.t, c.f.OrgA, c.f.Client1, c.name(prefix))
}

// parentOf reads the stored parent of a type or a CI ("" for none).
func (c *cycleCase) parentOf(table, id string) string {
	c.t.Helper()
	col := map[string]string{"ci_type": "parent_type_id", "ci": "parent_ci_id"}[table]
	var parent string
	if err := c.f.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(`+col+`::text, '') FROM `+table+` WHERE id = $1`, id).Scan(&parent); err != nil {
		c.t.Fatalf("parent of %s %s: %v", table, id, err)
	}
	return parent
}

// cycleKind is the type or the composition side of the cases.
type cycleKind struct {
	table   string
	node    func(prefix string) string
	parent  func(child, parent string) write
	refused []int
}

// TestCycles runs the type and composition cycles of ABN-03 in phase 2
// (WP-825), sequentially and in parallel, and the deterministic
// three-party case on parent_ci_id.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCycles(t *testing.T) {
	f := scopetest.Seed(t, "a8")
	c := newCycleCase(t, f)
	kinds := map[string]cycleKind{
		// A type cycle is 409 on parent_type_id (WP-248), a conflict 409.
		"type": {table: "ci_type", node: c.typ, parent: c.parentType, refused: []int{http.StatusConflict}},
		// A composition cycle is 422 on parent_ci_id, a conflict 409.
		"composition": {table: "ci", node: c.ciOf, parent: c.parentCI,
			refused: []int{http.StatusUnprocessableEntity, http.StatusConflict}},
	}

	for _, kindName := range []string{"type", "composition"} {
		k := kinds[kindName]
		cycleRefused := http.StatusUnprocessableEntity
		if kindName == "type" {
			cycleRefused = http.StatusConflict
		}

		t.Run(kindName+" sequential", func(t *testing.T) {
			c.t = t
			// (a) A→B, then B→A.
			a, b := k.node("seq-a"), k.node("seq-b")
			if code := c.alone(k.parent(a, b)); code != http.StatusOK {
				t.Fatalf("A→B: %d", code)
			}
			if code := c.alone(k.parent(b, a)); code != cycleRefused {
				t.Fatalf("B→A after A→B: %d, want %d", code, cycleRefused)
			}
			// (b) A→B, B→C, then C→A.
			a, b, cc := k.node("seq-a"), k.node("seq-b"), k.node("seq-c")
			for _, step := range [][2]string{{a, b}, {b, cc}} {
				if code := c.alone(k.parent(step[0], step[1])); code != http.StatusOK {
					t.Fatalf("%s→%s: %d", step[0], step[1], code)
				}
			}
			if code := c.alone(k.parent(cc, a)); code != cycleRefused {
				t.Fatalf("C→A after A→B→C: %d, want %d", code, cycleRefused)
			}
			c.noCycles(kindName + " sequential cycles")
		})

		t.Run(kindName+" parallel", func(t *testing.T) {
			c.t = t
			for round := range 3 {
				// (a) A→B ∥ B→A.
				a, b := k.node("par-a"), k.node("par-b")
				out := c.race(k.parent(a, b), k.parent(b, a))
				won := c.atMostOne(out, k.refused...)
				pa, pb := c.parentOf(k.table, a), c.parentOf(k.table, b)
				switch won {
				case 0:
					if pa != b || pb != "" {
						t.Fatalf("round %d (a): A→B won, stored A→%q, B→%q", round, pa, pb)
					}
				case 1:
					if pb != a || pa != "" {
						t.Fatalf("round %d (a): B→A won, stored A→%q, B→%q", round, pa, pb)
					}
				default:
					if pa != "" || pb != "" {
						t.Fatalf("round %d (a): both refused, stored A→%q, B→%q", round, pa, pb)
					}
				}

				// (b) with A→B: B→C ∥ C→A.
				a, b, cc := k.node("par-a"), k.node("par-b"), k.node("par-c")
				if code := c.alone(k.parent(a, b)); code != http.StatusOK {
					t.Fatalf("round %d: A→B: %d", round, code)
				}
				out = c.race(k.parent(b, cc), k.parent(cc, a))
				won = c.atMostOne(out, k.refused...)
				pb, pc := c.parentOf(k.table, b), c.parentOf(k.table, cc)
				if (won == 0) != (pb == cc) || (won == 1) != (pc == a) {
					t.Fatalf("round %d (b): answers %v, stored B→%q, C→%q", round, out, pb, pc)
				}
				t.Logf("round %d: answers of (b) %v", round, out)
				c.noCycles(fmt.Sprintf("round %d of the parallel %s cycles", round, kindName))
			}
		})
	}

	t.Run("composition three parties deterministic", func(t *testing.T) {
		c.t = t
		a, b, cc := c.ciOf("det-a"), c.ciOf("det-b"), c.ciOf("det-c")
		if code := c.alone(c.parentCI(a, b)); code != http.StatusOK {
			t.Fatalf("A→B: %d", code)
		}
		scope := database.OrgWideScope(f.OrgA, c.actor)
		ctx := context.Background()

		// T2 sets C→A and holds its transaction open until release.
		holding, release, t2Done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
		go func() {
			t2Done <- database.WithTenant(ctx, f.App, &scope, func(txCtx context.Context, tx pgx.Tx) error {
				if _, err := tx.Exec(txCtx, `UPDATE ci SET parent_ci_id = $1 WHERE id = $2`, a, cc); err != nil {
					return err
				}
				close(holding)
				<-release
				return nil
			})
		}()
		select {
		case <-holding:
		case err := <-t2Done:
			t.Fatalf("T2 C→A: %v", err)
		}

		// T1 sets B→C while T2 is open.
		t1Done := make(chan error, 1)
		go func() {
			t1Done <- database.WithTenant(ctx, f.App, &scope, func(txCtx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(txCtx, `UPDATE ci SET parent_ci_id = $1 WHERE id = $2 /* wp825-t1 */`, cc, b)
				return err
			})
		}()
		// With the lock T1 waits for the advisory lock of T2; without it T1
		// commits at once.
		var t1Err error
		t1Ended := false
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
			select {
			case t1Err = <-t1Done:
				t1Ended = true
			default:
			}
			if t1Ended {
				break
			}
			var waiting int
			if err := f.Admin.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
				WHERE wait_event_type = 'Lock' AND wait_event = 'advisory' AND query LIKE '%wp825-t1%'`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting > 0 {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		close(release)
		if err := <-t2Done; err != nil {
			t.Fatalf("T2 C→A: %v", err)
		}
		if t1Ended && t1Err == nil {
			t.Fatalf("T1 B→C committed while T2 (C→A) was open: ci_check_parent_cycle does not serialize, "+
				"the integrity check reports %v", scopetest.IntegrityViolations(t, f.Admin, f.OrgA))
		}
		if !t1Ended {
			t1Err = <-t1Done
		}
		var pgErr *pgconn.PgError
		if !errors.As(t1Err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ci_parent_cycle" {
			t.Fatalf("T1 B→C after T2 committed C→A: %v, want 23514 ci_parent_cycle", t1Err)
		}
		if pb, pc := c.parentOf("ci", b), c.parentOf("ci", cc); pb != "" || pc != a {
			t.Fatalf("stored B→%q, C→%q; want B without parent and C→A", pb, pc)
		}
		c.noCycles("the deterministic three-party case")
	})
}
