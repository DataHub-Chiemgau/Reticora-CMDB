package crypto_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// cleanDerivationRoot removes the derivation root of the test database
// before and after a test: the root goes with the master key a test draws.
func cleanDerivationRoot(t *testing.T, f *scopetest.Fixture) {
	t.Helper()
	clean := func() { _, _ = f.Admin.Exec(context.Background(), `DELETE FROM platform_secret`) }
	clean()
	t.Cleanup(clean)
}

// TestDerivationRootStore covers the derivation root in platform_secret
// (SEC-06, WP-459) against PostgreSQL with the runtime login:
//   - a replica that finds no root while another one's insert is pending
//     waits for it, stores nothing and opens the other root; eight
//     replicas starting at once with the same master key end with one
//     root, stored by exactly one of them, and derive the same secrets; a
//     restart opens the stored root;
//   - a root the master key does not unseal (another key, a tampered
//     ciphertext) fails with ErrDerivationRoot and is neither replaced,
//     rewrapped nor joined by a new root;
//   - the rewrap seals the root with the current key, once;
//   - only the system context reaches the table: without a context, in a
//     tenant or an operator transaction and as reticora_owner the runtime
//     role reads nothing and writes nothing; WithSystem reads but cannot
//     write; nobody deletes or truncates the root;
//   - the root appears in no log record, no error and no audit entry.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDerivationRootStore(t *testing.T) {
	f := scopetest.Seed(t, "0e")
	cleanDerivationRoot(t, f)
	bg := context.Background()
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)
	var errs []error

	key, _ := crypto.GenerateMasterKey()
	// The insert race, step by step: the other replica's insert is pending
	// when this one finds no root.
	winner, _ := crypto.NewEnvelopeEncryptor(key)
	winnerRoot, err := winner.NewDerivationRoot()
	if err != nil {
		t.Fatal(err)
	}
	pending, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pending.Rollback(bg) }()
	if _, err = pending.Exec(bg, `INSERT INTO platform_secret (name, sealed) VALUES ('derivation_root', $1)`, winnerRoot); err != nil {
		t.Fatal(err)
	}
	loser, _ := crypto.NewEnvelopeEncryptor(key)
	type loadResult struct {
		created bool
		err     error
	}
	lost := make(chan loadResult, 1)
	go func() {
		c, lerr := crypto.LoadDerivationRoot(bg, f.App, loser)
		lost <- loadResult{c, lerr}
	}()
	waiting := false
	for range 200 {
		if err = f.Admin.QueryRow(bg, `SELECT EXISTS (SELECT 1 FROM pg_stat_activity
			WHERE wait_event_type = 'Lock' AND query LIKE 'INSERT INTO platform_secret%')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err = pending.Commit(bg); err != nil {
		t.Fatal(err)
	}
	res := <-lost
	if !waiting || res.err != nil || res.created {
		t.Fatalf("replica behind a pending insert: waited %t, created %t, %v", waiting, res.created, res.err)
	}
	if err = winner.OpenDerivationRoot(winnerRoot); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(loser.Derive("reticora/pagination-cursor/v1"), winner.Derive("reticora/pagination-cursor/v1")) {
		t.Fatal("the replica behind a pending insert opened another root")
	}
	if _, err = f.Admin.Exec(bg, `DELETE FROM platform_secret`); err != nil {
		t.Fatal(err)
	}

	const replicas = 8
	encs := make([]*crypto.EnvelopeEncryptor, replicas)
	created := make([]bool, replicas)
	loadErrs := make([]error, replicas)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range encs {
		encs[i], _ = crypto.NewEnvelopeEncryptor(key)
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			created[i], loadErrs[i] = crypto.LoadDerivationRoot(bg, f.App, encs[i])
		}(i)
	}
	close(start)
	wg.Wait()
	stored := 0
	for i := range encs {
		if loadErrs[i] != nil {
			t.Fatalf("replica %d: %v", i, loadErrs[i])
		}
		if created[i] {
			stored++
		}
		if !bytes.Equal(encs[i].Derive("reticora/pagination-cursor/v1"), encs[0].Derive("reticora/pagination-cursor/v1")) {
			t.Errorf("replica %d derives other secrets than replica 0", i)
		}
	}
	type row struct {
		sealed  []byte
		created string
	}
	rows := func() []row {
		t.Helper()
		r, qerr := f.Admin.Query(bg, `SELECT sealed, created_at::text FROM platform_secret ORDER BY name`)
		if qerr != nil {
			t.Fatal(qerr)
		}
		out, qerr := pgx.CollectRows(r, func(cr pgx.CollectableRow) (row, error) {
			var x row
			serr := cr.Scan(&x.sealed, &x.created)
			return x, serr
		})
		if qerr != nil {
			t.Fatal(qerr)
		}
		return out
	}
	first := rows()
	if stored != 1 || len(first) != 1 {
		t.Fatalf("%d replicas stored a root, %d rows; want one", stored, len(first))
	}
	root, err := encs[0].UnwrapDEK(first[0].sealed)
	if err != nil || len(root) != crypto.KeySize || !encs[0].IsCurrent(first[0].sealed) {
		t.Fatalf("stored root: %d bytes, %v", len(root), err)
	}
	restarted, _ := crypto.NewEnvelopeEncryptor(key)
	if again, lerr := crypto.LoadDerivationRoot(bg, f.App, restarted); lerr != nil || again ||
		!bytes.Equal(restarted.Derive("reticora/pagination-cursor/v1"), encs[0].Derive("reticora/pagination-cursor/v1")) {
		t.Fatalf("restart: created %t, %v, or other secrets", again, lerr)
	}

	// Another master key: the start fails, the root stays as it is.
	otherKey, _ := crypto.GenerateMasterKey()
	other, _ := crypto.NewEnvelopeEncryptor(otherKey)
	again, err := crypto.LoadDerivationRoot(bg, f.App, other)
	errs = append(errs, err)
	if !errors.Is(err, crypto.ErrDerivationRoot) || again {
		t.Errorf("root of another master key: created %t, %v; want ErrDerivationRoot", again, err)
	}
	if got := rows(); len(got) != 1 || !bytes.Equal(got[0].sealed, first[0].sealed) || got[0].created != first[0].created {
		t.Error("a root that did not unseal was replaced")
	}
	// A tampered root: the same.
	if _, err = f.Admin.Exec(bg, `UPDATE platform_secret SET sealed = set_byte(sealed, length(sealed) - 1, get_byte(sealed, length(sealed) - 1) # 1)`); err != nil {
		t.Fatal(err)
	}
	tamperedEnc, _ := crypto.NewEnvelopeEncryptor(key)
	again, err = crypto.LoadDerivationRoot(bg, f.App, tamperedEnc)
	errs = append(errs, err)
	if !errors.Is(err, crypto.ErrDerivationRoot) || again {
		t.Errorf("tampered root: created %t, %v; want ErrDerivationRoot", again, err)
	}
	newKey, _ := crypto.GenerateMasterKey()
	rotating, _ := crypto.NewEnvelopeEncryptor(newKey, key)
	_, err = crypto.RewrapDerivationRoot(bg, f.App, rotating)
	errs = append(errs, err)
	if !errors.Is(err, crypto.ErrDerivationRoot) {
		t.Errorf("rewrap of a tampered root: %v; want ErrDerivationRoot", err)
	}
	if got := rows(); len(got) != 1 || bytes.Equal(got[0].sealed, first[0].sealed) {
		t.Error("the tampered root was replaced")
	}
	if _, err = f.Admin.Exec(bg, `UPDATE platform_secret SET sealed = $1`, first[0].sealed); err != nil {
		t.Fatal(err)
	}

	// The rewrap: with the current key nothing to do; with a new key the
	// root is sealed with it once, and the new key alone opens it.
	if changed, rerr := crypto.RewrapDerivationRoot(bg, f.App, restarted); rerr != nil || changed {
		t.Errorf("rewrap with the current key: %t, %v", changed, rerr)
	}
	if changed, rerr := crypto.RewrapDerivationRoot(bg, f.App, rotating); rerr != nil || !changed {
		t.Fatalf("rewrap with a new key: %t, %v", changed, rerr)
	}
	if changed, _ := crypto.RewrapDerivationRoot(bg, f.App, rotating); changed {
		t.Error("the root was rewrapped twice")
	}
	retired, _ := crypto.NewEnvelopeEncryptor(newKey)
	if _, err = crypto.LoadDerivationRoot(bg, f.App, retired); err != nil ||
		!bytes.Equal(retired.Derive("reticora/pagination-cursor/v1"), encs[0].Derive("reticora/pagination-cursor/v1")) {
		t.Fatalf("rewrapped root with the new key alone: %v", err)
	}
	if got := rows(); len(got) != 1 || !rotating.IsCurrent(got[0].sealed) || got[0].created != first[0].created {
		t.Error("the rewrap did not reseal the one root with the new key")
	}

	// Access of the runtime role: only the system context.
	countRows := func(ctx context.Context, tx pgx.Tx) (n int, err error) {
		err = tx.QueryRow(ctx, `SELECT count(*) FROM platform_secret`).Scan(&n)
		return n, err
	}
	var n int
	if err = f.App.QueryRow(bg, `SELECT count(*) FROM platform_secret`).Scan(&n); err != nil || n != 0 {
		t.Errorf("runtime role without a context reads %d rows (%v)", n, err)
	}
	scope := database.OrgWideScope(f.OrgA, f.User)
	if err = database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		if n, err = countRows(ctx, tx); err != nil || n != 0 {
			t.Errorf("tenant transaction reads %d rows (%v)", n, err)
		}
		tag, uerr := tx.Exec(ctx, `UPDATE platform_secret SET sealed = '\x00'`)
		if uerr != nil || tag.RowsAffected() != 0 {
			t.Errorf("tenant transaction updated %d rows (%v)", tag.RowsAffected(), uerr)
		}
		// The owner role is bound by the policies as well (FORCE).
		if _, serr := tx.Exec(ctx, `SET LOCAL ROLE reticora_owner`); serr != nil {
			return serr
		}
		if n, err = countRows(ctx, tx); err != nil || n != 0 {
			t.Errorf("reticora_owner in a tenant transaction reads %d rows (%v)", n, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	insertErr := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		_, ierr := tx.Exec(ctx, `INSERT INTO platform_secret (name, sealed) VALUES ('tenant', '\x00')`)
		return ierr
	})
	requireSQLState(t, "tenant insert", insertErr, "42501")
	if err = database.WithOperator(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
		if n, err = countRows(ctx, tx); err != nil || n != 0 {
			t.Errorf("operator transaction reads %d rows (%v)", n, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err = database.WithSystem(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
		if n, err = countRows(ctx, tx); err != nil || n != 1 {
			t.Errorf("system transaction reads %d rows (%v)", n, err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	readOnlyErr := database.WithSystem(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
		_, uerr := tx.Exec(ctx, `UPDATE platform_secret SET sealed = '\x00'`)
		return uerr
	})
	requireSQLState(t, "update in WithSystem", readOnlyErr, "25006")
	for _, stmt := range []string{`DELETE FROM platform_secret`, `TRUNCATE platform_secret`, `SET LOCAL ROLE reticora_owner; TRUNCATE platform_secret`} {
		delErr := database.WithSystemWrite(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
			_, derr := tx.Exec(ctx, stmt)
			return derr
		})
		requireSQLState(t, stmt, delErr, "42501")
	}
	if err = database.WithSystemWrite(bg, f.App, func(ctx context.Context, tx pgx.Tx) error {
		if _, serr := tx.Exec(ctx, `SET LOCAL ROLE reticora_owner`); serr != nil {
			return serr
		}
		tag, derr := tx.Exec(ctx, `DELETE FROM platform_secret`)
		if derr != nil || tag.RowsAffected() != 0 {
			t.Errorf("reticora_owner deleted %d rows (%v)", tag.RowsAffected(), derr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if got := rows(); len(got) != 1 {
		t.Fatalf("%d rows after the access probes", len(got))
	}

	// The root itself appears nowhere.
	for _, needle := range []string{hex.EncodeToString(root), base64.StdEncoding.EncodeToString(root), base64.RawURLEncoding.EncodeToString(root)} {
		if strings.Contains(logs.String(), needle) {
			t.Error("a log record contains the derivation root")
		}
		for _, e := range errs {
			if e != nil && strings.Contains(e.Error(), needle) {
				t.Errorf("an error contains the derivation root: %v", e)
			}
		}
		var audited int
		if err = f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM audit_log WHERE strpos(changes::text, $1) > 0)
			+ (SELECT count(*) FROM operator_audit WHERE strpos(details::text, $1) > 0)`, needle).Scan(&audited); err != nil || audited != 0 {
			t.Errorf("%d audit entries contain the derivation root (%v)", audited, err)
		}
	}
}

// requireSQLState fails unless err is a PostgreSQL error with the code.
func requireSQLState(t *testing.T, what string, err error, code string) {
	t.Helper()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != code {
		t.Errorf("%s: %v, want SQLSTATE %s", what, err, code)
	}
}
