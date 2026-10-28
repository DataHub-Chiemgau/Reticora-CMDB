// Package tenantguard is the fixture of TestTenantGuardFixtures (WP-561,
// F-190). Every line marked "want:" is a database access outside WithTenant
// and WithSystem that the tenant guard must report: "call" for a method
// call, method value or method expression on a pool or connection and for
// a function that opens one, "name" for a *pgxpool.Pool value that breaks
// the naming convention. allowed.go holds what the guard must not report.
package tenantguard

import (
	"context"
	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type repo struct {
	pool *pgxpool.Pool
	db   *pgxpool.Pool // want: name
}

// embedded promotes the pool methods.
type embedded struct {
	*pgxpool.Pool
}

// directCall is the case the name-based guard already saw.
func directCall(ctx context.Context, r *repo) error {
	_, err := r.pool.Exec(ctx, `DELETE FROM ci`) // want: call
	return err
}

// localAlias hides the pool behind a local variable.
func localAlias(ctx context.Context, r *repo) error {
	db := r.pool                             // want: name
	_, err := db.Exec(ctx, `DELETE FROM ci`) // want: call
	return err
}

// methodValue takes the method without calling it.
func methodValue(ctx context.Context, r *repo) error {
	exec := r.pool.Exec // want: call
	_, err := exec(ctx, `DELETE FROM ci`)
	return err
}

// methodExpression names the method through the type.
func methodExpression(ctx context.Context, r *repo) error {
	_, err := (*pgxpool.Pool).Exec(r.pool, ctx, `DELETE FROM ci`) // want: call
	return err
}

// misnamedField calls through a field that does not end in pool.
func misnamedField(ctx context.Context, r *repo) error {
	rows, err := r.db.Query(ctx, `SELECT 1 FROM ci`) // want: call
	if err != nil {
		return err
	}
	rows.Close()
	return nil
}

// embeddedPool calls a promoted pool method.
func embeddedPool(ctx context.Context, e *embedded) error {
	_, err := e.Exec(ctx, `DELETE FROM ci`) // want: call
	return err
}

// acquiredConn takes a pooled connection and runs SQL on it.
func acquiredConn(ctx context.Context, r *repo) error {
	conn, err := r.pool.Acquire(ctx) // want: call
	if err != nil {
		return err
	}
	defer conn.Release()
	_, err = conn.Exec(ctx, `DELETE FROM ci`) // want: call
	return err
}

// pgxConnect opens a connection of its own.
func pgxConnect(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn) // want: call
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `DELETE FROM ci`) // want: call
	return err
}

// connectValue takes the opening function as a value.
func connectValue(ctx context.Context, dsn string) error {
	connect := pgx.Connect // want: call
	conn, err := connect(ctx, dsn)
	if err != nil {
		return err
	}
	return conn.Close(ctx)
}

// sqlOpen opens a database/sql handle (WP-041 retest).
func sqlOpen(ctx context.Context, dsn string) error {
	handle, err := sql.Open("pgx", dsn) // want: call
	if err != nil {
		return err
	}
	defer handle.Close()
	_, err = handle.ExecContext(ctx, `DELETE FROM ci`) // want: call
	return err
}

// poolNew opens a pool of its own (WP-041 retest).
func poolNew(ctx context.Context, dsn string) error {
	ownPool, err := pgxpool.New(ctx, dsn) // want: call
	if err != nil {
		return err
	}
	ownPool.Close()
	return nil
}
