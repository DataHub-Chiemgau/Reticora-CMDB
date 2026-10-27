package database_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Fixture ids of this file; distinct from the other integration tests that
// share the database.
const (
	scopeOrgA    = "c0c0a000-0000-4000-8000-000000000001"
	scopeOrgB    = "c0c0b000-0000-4000-8000-000000000002"
	scopeClient1 = "c0c0a000-0000-4000-8000-0000000000c1"
	scopeClient2 = "c0c0a000-0000-4000-8000-0000000000c2"
	scopeUser    = "c0c0a000-0000-4000-8000-0000000000f1"
)

var scopeGUCs = []string{
	database.OrgGUC, database.UserGUC, database.ClientScopeGUC, database.SiteScopeGUC, database.TeamScopeGUC,
}

// seedScopeFixture creates organization A with two clients and an empty
// organization B through a maintenance connection, which is not subject to
// RLS, and returns a pool through the production constructor.
func seedScopeFixture(t *testing.T) (context.Context, *pgxpool.Pool) {
	t.Helper()
	dsn := testDatabaseURL(t)
	ctx := context.Background()

	admin, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatalf("maintenance pool: %v", err)
	}
	t.Cleanup(admin.Close)
	cleanup := func() {
		// client rows go with their organization (ON DELETE CASCADE).
		if _, delErr := admin.Exec(ctx, `DELETE FROM organization WHERE id IN ($1::uuid, $2::uuid)`, scopeOrgA, scopeOrgB); delErr != nil {
			t.Errorf("cleanup: %v", delErr)
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO organization (id, name, slug) VALUES ($1, 'Scope Org A', 'tenant-scope-org-a')`, []any{scopeOrgA}},
		{`INSERT INTO organization (id, name, slug) VALUES ($1, 'Scope Org B', 'tenant-scope-org-b')`, []any{scopeOrgB}},
		{`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client 1', 'scope-client-1')`, []any{scopeClient1, scopeOrgA}},
		{`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'Client 2', 'scope-client-2')`, []any{scopeClient2, scopeOrgA}},
	} {
		if _, seedErr := admin.Exec(ctx, s.sql, s.args...); seedErr != nil {
			t.Fatalf("seed %q: %v", s.sql, seedErr)
		}
	}

	pool, err := database.NewPool(ctx, scopetest.AppDSN(t))
	if err != nil {
		t.Fatalf("application pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return ctx, pool
}

func readGUCs(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (map[string]string, error) {
	values := make(map[string]string, len(scopeGUCs))
	for _, name := range scopeGUCs {
		var v *string
		if err := q.QueryRow(ctx, `SELECT current_setting($1, true)`, name).Scan(&v); err != nil {
			return nil, err
		}
		if v != nil {
			values[name] = *v
		}
	}
	return values, nil
}

// TestWithTenantSetsAllGUCs: every TEN-04 variable is set inside the
// transaction, with the restricted lists and the no-access marker.
func TestWithTenantSetsAllGUCs(t *testing.T) {
	ctx, pool := seedScopeFixture(t)
	scope := database.TenantScope{
		OrgID:   scopeOrgA,
		UserID:  scopeUser,
		Clients: database.ScopeIDs(scopeClient1, scopeClient2),
		Sites:   database.ScopeIDs(),
		Teams:   database.AllScopes(),
	}
	var got map[string]string
	err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		got, err = readGUCs(ctx, tx)
		return err
	})
	if err != nil {
		t.Fatalf("WithTenant: %v", err)
	}
	want := map[string]string{
		database.OrgGUC:         scopeOrgA,
		database.UserGUC:        scopeUser,
		database.ClientScopeGUC: scopeClient1 + "," + scopeClient2,
		database.SiteScopeGUC:   "00000000-0000-0000-0000-000000000000",
		database.TeamScopeGUC:   "",
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s = %q, want %q", name, got[name], w)
		}
	}
}

// TestWithTenantResetsGUCsOnPooledConnections: after commit and after a
// rolled-back transaction no tenant variable survives on any idle connection
// of the pool, i.e. on the connection that served the transaction.
func TestWithTenantResetsGUCsOnPooledConnections(t *testing.T) {
	ctx, pool := seedScopeFixture(t)
	scope := database.OrgWideScope(scopeOrgA, scopeUser)
	scope.Clients = database.ScopeIDs(scopeClient1)
	errAbort := errors.New("abort")

	for _, tc := range []struct {
		name string
		fn   func(context.Context, pgx.Tx) error
		want error
	}{
		{"commit", func(context.Context, pgx.Tx) error { return nil }, nil},
		{"rollback", func(context.Context, pgx.Tx) error { return errAbort }, errAbort},
	} {
		if err := database.WithTenant(ctx, pool, &scope, tc.fn); !errors.Is(err, tc.want) {
			t.Fatalf("%s: WithTenant() = %v, want %v", tc.name, err, tc.want)
		}
		conns := pool.AcquireAllIdle(ctx)
		if len(conns) == 0 {
			t.Fatalf("%s: no idle connection to inspect", tc.name)
		}
		for _, c := range conns {
			values, err := readGUCs(ctx, c)
			c.Release()
			if err != nil {
				t.Fatalf("%s: read GUCs: %v", tc.name, err)
			}
			for name, v := range values {
				if v != "" {
					t.Errorf("%s: %s = %q survived the transaction", tc.name, name, v)
				}
			}
		}
	}
}

// TestWithTenantScopesRows checks the scope against the real client policy:
// an empty client scope sees nothing (E-08), a restricted scope sees its
// clients, the org-wide marker sees all, and another org sees 0 rows (TEN-06).
func TestWithTenantScopesRows(t *testing.T) {
	ctx, pool := seedScopeFixture(t)
	count := func(scope database.TenantScope) int {
		t.Helper()
		var n int
		err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM client`).Scan(&n)
		})
		if err != nil {
			t.Fatalf("WithTenant: %v", err)
		}
		return n
	}
	withClients := func(org string, clients database.ScopeSet) database.TenantScope {
		s := database.OrgWideScope(org, scopeUser)
		s.Clients = clients
		return s
	}

	for _, tc := range []struct {
		name  string
		scope database.TenantScope
		want  int
	}{
		{"org-wide", database.OrgWideScope(scopeOrgA, scopeUser), 2},
		{"one client", withClients(scopeOrgA, database.ScopeIDs(scopeClient1)), 1},
		{"empty client scope fails closed", withClients(scopeOrgA, database.ScopeIDs()), 0},
		{"wrong org", database.OrgWideScope(scopeOrgB, scopeUser), 0},
		{"client of another org", withClients(scopeOrgB, database.ScopeIDs(scopeClient1)), 0},
	} {
		if got := count(tc.scope); got != tc.want {
			t.Errorf("%s: %d clients visible, want %d", tc.name, got, tc.want)
		}
	}
}

// TestWithTenantRejectsIncompleteScope: fn never runs without a complete scope.
func TestWithTenantRejectsIncompleteScope(t *testing.T) {
	ctx, pool := seedScopeFixture(t)
	err := database.WithTenant(ctx, pool, &database.TenantScope{OrgID: scopeOrgA}, func(context.Context, pgx.Tx) error {
		t.Fatal("fn must not run with an incomplete scope")
		return nil
	})
	if !errors.Is(err, database.ErrIncompleteScope) {
		t.Fatalf("WithTenant() = %v, want ErrIncompleteScope", err)
	}
}

// sqlLog records the statements a pool sends.
type sqlLog struct {
	mu  sync.Mutex
	sql []string
}

func (l *sqlLog) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	l.mu.Lock()
	l.sql = append(l.sql, strings.ToLower(strings.TrimSpace(data.SQL)))
	l.mu.Unlock()
	return ctx
}

func (l *sqlLog) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// take returns the statements since the last call and how many of them
// begin and commit a transaction.
func (l *sqlLog) take() (statements []string, begins, commits int) {
	l.mu.Lock()
	statements, l.sql = l.sql, nil
	l.mu.Unlock()
	for _, s := range statements {
		switch {
		case strings.HasPrefix(s, "begin"):
			begins++
		case s == "commit":
			commits++
		}
	}
	return statements, begins, commits
}

// tracedPool is the application pool with a statement log.
func tracedPool(t *testing.T, app *pgxpool.Pool) (*pgxpool.Pool, *sqlLog) {
	t.Helper()
	log := &sqlLog{}
	cfg := app.Config()
	cfg.ConnConfig.Tracer = log
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err = pool.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	return pool, log
}

// TestContextTxJoin covers the transaction in the context of WP-332 (NFR-03)
// against PostgreSQL:
//   - repository calls with the context of WithRequestTenantTx join its
//     transaction: one BEGIN (with the GUCs in the same round trip), one
//     COMMIT, one transaction id;
//   - another organization, another scope, a narrowed context scope, a
//     WithSystem transaction and a context that outlived its transaction
//     are refused without opening a transaction;
//   - a failed statement whose error was swallowed rolls everything back
//     (ErrTxAborted); a savepoint keeps the transaction usable after a
//     unique violation;
//   - AfterCommit runs after the commit only, detached from the transaction;
//     AfterCommitBatch drops the items of a savepoint that rolled back.
func TestContextTxJoin(t *testing.T) {
	ctx, app := seedScopeFixture(t)
	pool, log := tracedPool(t, app)
	scope := database.OrgWideScope(scopeOrgA, scopeUser)
	reqCtx := database.ContextWithTenantScope(ctx, &scope)
	noop := func(context.Context, pgx.Tx) error { return nil }
	clients := func(slug string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(ctx, app, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM client WHERE slug = $1`, slug).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	insertClient := func(ctx context.Context, id, slug string) error {
		return database.WithRequestTenant(ctx, pool, scopeOrgA, func(ctx context.Context, tx pgx.Tx) error {
			if id == "" {
				_, err := tx.Exec(ctx, `INSERT INTO client (organization_id, name, slug) VALUES ($1, $2, $2)`, scopeOrgA, slug)
				return err
			}
			_, err := tx.Exec(ctx, `INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, $3, $3)`, id, scopeOrgA, slug)
			return err
		})
	}
	log.take()

	// Join: two repository calls, one transaction.
	var outer, inner int64
	var user string
	joinErr := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		if err := database.WithRequestTenant(ctx, pool, scopeOrgA, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT txid_current()`).Scan(&outer)
		}); err != nil {
			return err
		}
		return database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT txid_current(), current_setting('app.user_id')`).Scan(&inner, &user)
		})
	})
	if joinErr != nil {
		t.Fatalf("join: %v", joinErr)
	}
	statements, begins, commits := log.take()
	if outer == 0 || outer != inner || user != scopeUser {
		t.Errorf("joined calls ran in transactions %d and %d (user %q)", outer, inner, user)
	}
	if begins != 1 || commits != 1 || len(statements) != 4 {
		t.Errorf("join sent %d statements with %d begins and %d commits, want 4 with 1 and 1: %q",
			len(statements), begins, commits, statements)
	}

	// Refusals open no transaction and leave the context transaction usable.
	refusalErr := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		if err := database.WithRequestTenant(ctx, pool, scopeOrgB, noop); !errors.Is(err, database.ErrTenantMismatch) {
			t.Errorf("other organization: %v, want ErrTenantMismatch", err)
		}
		system := database.OrgWideScope(scopeOrgA, "")
		if err := database.WithTenant(ctx, pool, &system, noop); !errors.Is(err, database.ErrTxScopeMismatch) {
			t.Errorf("other scope: %v, want ErrTxScopeMismatch", err)
		}
		narrowed := scope
		narrowed.Clients = database.ScopeIDs(scopeClient1)
		if err := database.WithRequestTenant(database.ContextWithTenantScope(ctx, &narrowed), pool, scopeOrgA, noop); !errors.Is(err, database.ErrTxScopeMismatch) {
			t.Errorf("narrowed scope: %v, want ErrTxScopeMismatch", err)
		}
		return insertClient(ctx, "", "join-after-refusals")
	})
	if refusalErr != nil {
		t.Fatalf("refusals: %v", refusalErr)
	}
	if _, begins, _ = log.take(); begins != 1 {
		t.Errorf("refused calls opened transactions: %d begins", begins)
	}
	if clients("join-after-refusals") != 1 {
		t.Error("the context transaction did not commit after the refusals")
	}

	// A WithSystem transaction is not joinable.
	systemErr := database.WithSystem(ctx, pool, func(_ context.Context, tx pgx.Tx) error {
		return database.WithRequestTenant(database.ContextWithTx(reqCtx, tx), pool, scopeOrgA, noop)
	})
	if !errors.Is(systemErr, database.ErrTxNotJoinable) {
		t.Errorf("WithSystem transaction: %v, want ErrTxNotJoinable", systemErr)
	}

	// A context that outlived its transaction is refused; detached, it
	// opens its own.
	var leaked context.Context
	if leakErr := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		leaked = ctx
		return nil
	}); leakErr != nil {
		t.Fatal(leakErr)
	}
	if doneErr := database.WithRequestTenant(leaked, pool, scopeOrgA, noop); !errors.Is(doneErr, database.ErrTxDone) {
		t.Errorf("ended transaction: %v, want ErrTxDone", doneErr)
	}
	if detachedErr := database.WithRequestTenant(database.WithoutTx(leaked), pool, scopeOrgA, noop); detachedErr != nil {
		t.Errorf("detached context: %v", detachedErr)
	}

	// A swallowed failed statement rolls back the whole transaction.
	abortErr := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		if err := insertClient(ctx, "", "join-aborted"); err != nil {
			return err
		}
		_ = database.WithRequestTenant(ctx, pool, scopeOrgA, func(ctx context.Context, tx pgx.Tx) error {
			var n int
			return tx.QueryRow(ctx, `SELECT 1 / 0`).Scan(&n)
		})
		if !database.TxFailed(ctx) {
			t.Error("TxFailed after a division by zero")
		}
		return nil
	})
	if !errors.Is(abortErr, database.ErrTxAborted) {
		t.Errorf("swallowed error: %v, want ErrTxAborted", abortErr)
	}
	if clients("join-aborted") != 0 {
		t.Error("the write before the failed statement was committed")
	}

	// A savepoint undoes a unique violation, its callbacks with it, and the
	// transaction goes on.
	var ran []string
	savepointErr := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		spErr := database.Savepoint(ctx, func(ctx context.Context) error {
			database.AfterCommit(ctx, func(context.Context) { ran = append(ran, "undone") })
			return insertClient(ctx, scopeClient1, "join-duplicate")
		})
		var pgErr *pgconn.PgError
		if !errors.As(spErr, &pgErr) || pgErr.Code != "23505" {
			t.Errorf("savepoint: %v, want a unique violation", spErr)
		}
		if database.TxFailed(ctx) {
			t.Error("transaction failed after the savepoint rollback")
		}
		database.AfterCommit(ctx, func(ctx context.Context) {
			if database.InTx(ctx) {
				t.Error("callback context carries the committed transaction")
			}
			// Detached: a repository call opens its own transaction.
			if err := insertClient(ctx, "", "join-after-commit"); err != nil {
				t.Errorf("write after the commit: %v", err)
			}
			ran = append(ran, "committed")
		})
		if len(ran) != 0 {
			t.Error("callbacks ran before the commit")
		}
		return insertClient(ctx, "", "join-savepoint")
	})
	if savepointErr != nil {
		t.Fatalf("savepoint transaction: %v", savepointErr)
	}
	if strings.Join(ran, ",") != "committed" || clients("join-savepoint") != 1 || clients("join-after-commit") != 1 {
		t.Errorf("after the savepoint: callbacks %v, clients %d/%d", ran, clients("join-savepoint"), clients("join-after-commit"))
	}

	// A savepoint whose fn swallows a failed statement and returns nil is
	// rolled back too (ErrTxAborted) instead of released: the transaction
	// goes on, the writes and callbacks of fn are undone.
	swallowedErr := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		spErr := database.Savepoint(ctx, func(ctx context.Context) error {
			database.AfterCommit(ctx, func(context.Context) { t.Error("callback of the rolled back savepoint ran") })
			if err := insertClient(ctx, "", "join-swallowed-inside"); err != nil {
				return err
			}
			_ = insertClient(ctx, scopeClient1, "join-swallowed-duplicate")
			return nil
		})
		if !errors.Is(spErr, database.ErrTxAborted) {
			t.Errorf("savepoint with a swallowed error: %v, want ErrTxAborted", spErr)
		}
		if database.TxFailed(ctx) {
			t.Error("transaction failed after the savepoint with a swallowed error")
		}
		return insertClient(ctx, "", "join-swallowed-after")
	})
	if swallowedErr != nil {
		t.Fatalf("transaction around the savepoint with a swallowed error: %v", swallowedErr)
	}
	if clients("join-swallowed-inside") != 0 || clients("join-swallowed-after") != 1 {
		t.Errorf("after the savepoint with a swallowed error: clients %d inside (want 0), %d after (want 1)",
			clients("join-swallowed-inside"), clients("join-swallowed-after"))
	}

	// AfterCommitBatch (WP-489): the items of a savepoint that rolled back
	// leave the batch, a batch opened under it goes with it; the others
	// reach fn once after the commit.
	var batches []string
	collect := func(_ context.Context, items []any) {
		parts := make([]string, 0, len(items))
		for _, item := range items {
			parts = append(parts, item.(string))
		}
		batches = append(batches, strings.Join(parts, "+"))
	}
	batchErr := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		database.AfterCommitBatch(ctx, "kept", "before", collect)
		spErr := database.Savepoint(ctx, func(ctx context.Context) error {
			database.AfterCommitBatch(ctx, "kept", "undone", collect)
			database.AfterCommitBatch(ctx, "opened", "undone", collect)
			return insertClient(ctx, scopeClient1, "join-batch-duplicate")
		})
		if spErr == nil {
			t.Error("savepoint with a duplicate client did not fail")
		}
		if spErr = database.Savepoint(ctx, func(ctx context.Context) error {
			database.AfterCommitBatch(ctx, "opened", "released", collect)
			return nil
		}); spErr != nil {
			t.Errorf("released savepoint: %v", spErr)
		}
		database.AfterCommitBatch(ctx, "kept", "after", collect)
		return nil
	})
	if batchErr != nil {
		t.Fatalf("batch transaction: %v", batchErr)
	}
	if strings.Join(batches, ",") != "before+after,released" {
		t.Errorf("batches after the savepoints: %v, want before+after,released", batches)
	}

	// A rollback drops the callbacks.
	errAbort := errors.New("abort")
	if rollbackErr := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		database.AfterCommit(ctx, func(context.Context) { t.Error("callback of a rolled back transaction ran") })
		return errAbort
	}); !errors.Is(rollbackErr, errAbort) {
		t.Errorf("rollback: %v", rollbackErr)
	}
}

// TestWithTenantQuotesHostileUserID: the user id travels as a literal in the
// statement that opens the transaction; whatever it contains, it arrives
// unchanged in app.user_id, sets nothing else and RLS still restricts the
// transaction to its organization and client. It holds with
// standard_conforming_strings and backslash_quote off as well, and for a
// database URL that asks for the SJIS client encoding: the pool keeps UTF-8.
// TestBeforeCommit covers the writes a transaction puts last (WP-334): they
// run after everything else of the context transaction, in the order they
// were registered, also when a joined call registered them; a savepoint
// rolled back drops its writes, a failing write rolls the transaction back,
// a rolled-back transaction runs none, and without a context transaction a
// write runs at once.
func TestBeforeCommit(t *testing.T) {
	ctx, app := seedScopeFixture(t)
	pool, log := tracedPool(t, app)
	scope := database.OrgWideScope(scopeOrgA, scopeUser)
	reqCtx := database.ContextWithTenantScope(ctx, &scope)
	clients := func(slug string) int {
		t.Helper()
		var n int
		if err := database.WithTenant(ctx, app, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT count(*) FROM client WHERE slug = $1`, slug).Scan(&n)
		}); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var order []string
	write := func(ctx context.Context, name string) error {
		return database.WithRequestTenant(ctx, pool, scopeOrgA, func(ctx context.Context, tx pgx.Tx) error {
			order = append(order, name)
			_, err := tx.Exec(ctx, `INSERT INTO client (organization_id, name, slug) VALUES ($1, $2, $2)`, scopeOrgA, "before-commit-"+name)
			return err
		})
	}
	last := func(ctx context.Context, name string) error {
		return database.WithRequestTenant(ctx, pool, scopeOrgA, func(ctx context.Context, tx pgx.Tx) error {
			return database.BeforeCommit(ctx, func() error {
				order = append(order, name)
				_, err := tx.Exec(ctx, `INSERT INTO client (organization_id, name, slug) VALUES ($1, $2, $2)`, scopeOrgA, "before-commit-"+name)
				return err
			})
		})
	}
	log.take()
	err := database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		if err := write(ctx, "a"); err != nil {
			return err
		}
		if err := last(ctx, "x"); err != nil {
			return err
		}
		// A savepoint that fails drops the writes registered in it.
		if err := database.Savepoint(ctx, func(ctx context.Context) error {
			if err := last(ctx, "dropped"); err != nil {
				return err
			}
			return errors.New("refused")
		}); err == nil {
			return errors.New("savepoint did not fail")
		}
		if err := last(ctx, "y"); err != nil {
			return err
		}
		return write(ctx, "b")
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(order, ",") != "a,b,x,y" {
		t.Errorf("order %v, want the registered writes last: a,b,x,y", order)
	}
	if statements, begins, commits := log.take(); begins != 1 || commits != 1 {
		t.Errorf("%d begins and %d commits, want one transaction: %q", begins, commits, statements)
	}
	for _, slug := range []string{"a", "b", "x", "y"} {
		if clients("before-commit-"+slug) != 1 {
			t.Errorf("write %s not committed", slug)
		}
	}
	if clients("before-commit-dropped") != 0 {
		t.Error("the write of a rolled-back savepoint ran")
	}

	// A failing write rolls the transaction back; a transaction that fails
	// runs none.
	order = nil
	err = database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		if werr := write(ctx, "c"); werr != nil {
			return werr
		}
		return database.BeforeCommit(ctx, func() error { return errors.New("late failure") })
	})
	if err == nil || err.Error() != "late failure" || clients("before-commit-c") != 0 {
		t.Errorf("failing write: %v, %d rows of the transaction kept", err, clients("before-commit-c"))
	}
	err = database.WithRequestTenantTx(reqCtx, pool, scopeOrgA, func(ctx context.Context) error {
		if lerr := last(ctx, "never"); lerr != nil {
			return lerr
		}
		return errors.New("rolled back")
	})
	if err == nil || slices.Contains(order, "never") || clients("before-commit-never") != 0 {
		t.Errorf("rolled-back transaction ran its writes: %v, %v", err, order)
	}

	// Without a context transaction the write runs at once.
	ran := false
	if err = database.BeforeCommit(ctx, func() error { ran = true; return nil }); err != nil || !ran {
		t.Errorf("without a transaction: ran %v, %v", ran, err)
	}
}

func TestWithTenantQuotesHostileUserID(t *testing.T) {
	ctx, app := seedScopeFixture(t)
	cfg := app.Config()
	cfg.ConnConfig.RuntimeParams["standard_conforming_strings"] = "off"
	cfg.ConnConfig.RuntimeParams["backslash_quote"] = "off"
	legacy, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(legacy.Close)
	var conforming, backslashQuote string
	if err = legacy.QueryRow(ctx, `SELECT current_setting('standard_conforming_strings'), current_setting('backslash_quote')`).
		Scan(&conforming, &backslashQuote); err != nil || conforming != "off" || backslashQuote != "off" {
		t.Fatalf("legacy settings: standard_conforming_strings %q, backslash_quote %q, %v", conforming, backslashQuote, err)
	}

	// A database URL that asks for a client-only multi-byte encoding gets
	// UTF-8 all the same. In SJIS the bytes 0x81 0x5C ("ā" is C4 81 in UTF-8,
	// then the backslash) are one character, so with backslash_quote on the
	// doubled backslash would leave \' to escape the doubled quote, the next
	// quote would end the literal and the dollar-quoted rest of the user id
	// would set another organization.
	appDSN := scopetest.AppDSN(t)
	sep := "?"
	if strings.Contains(appDSN, "?") {
		sep = "&"
	}
	sjis, err := database.NewPool(ctx, appDSN+sep+"client_encoding=SJIS&backslash_quote=on&standard_conforming_strings=off")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sjis.Close)
	var encoding string
	if err = sjis.QueryRow(ctx, `SELECT current_setting('client_encoding'), current_setting('backslash_quote')`).
		Scan(&encoding, &backslashQuote); err != nil || encoding != "UTF8" || backslashQuote != "on" {
		t.Fatalf("pool from a URL with client_encoding=SJIS: client_encoding %q, backslash_quote %q, %v; want UTF8 and on",
			encoding, backslashQuote, err)
	}

	hostile := []string{`'`, `\`, `;`, `--`, `\'`, `''`, "line\nbreak", `E'\x41'`, "ünïcödé",
		`x'); SELECT set_config('app.org_id', '` + scopeOrgB + `', true); --`,
		"ā\\', true), set_config($$app.org_id$$, $$" + scopeOrgB + "$$, true) --"}
	for name, pool := range map[string]*pgxpool.Pool{"default": app, "legacy string settings": legacy, "client_encoding=SJIS requested": sjis} {
		for _, user := range hostile {
			scope := database.OrgWideScope(scopeOrgA, user)
			scope.Clients = database.ScopeIDs(scopeClient1)
			var gotUser, gotOrg string
			var visible int
			err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT current_setting('app.user_id'), current_setting('app.org_id'),
					(SELECT count(*) FROM client)`).Scan(&gotUser, &gotOrg, &visible)
			})
			if err != nil {
				t.Errorf("%s: %q: %v", name, user, err)
				continue
			}
			if gotUser != user || gotOrg != scopeOrgA || visible != 1 {
				t.Errorf("%s: %q: app.user_id %q, app.org_id %s, %d clients visible, want the user id unchanged in organization A with 1 client",
					name, user, gotUser, gotOrg, visible)
			}
		}
	}
}
