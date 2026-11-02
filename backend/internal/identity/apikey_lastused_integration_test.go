package identity_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/jackc/pgx/v5"
)

// lastUsedKey creates a live key of organization A in the fixture and returns
// it with its plaintext.
func lastUsedKey(t *testing.T, f *scopetest.Fixture, store *identity.PGAPIKeyStore) (*identity.StoredAPIKey, string) {
	t.Helper()
	generated, err := identity.GenerateAPIKey(identity.APIKeyEnvironmentLive)
	if err != nil {
		t.Fatal(err)
	}
	key, err := store.Create(f.OrgCtx(f.OrgA), &identity.StoredAPIKey{
		OrganizationID: f.OrgA, Name: "last-used", KeyHash: generated.KeyHash, KeyPrefix: generated.KeyPrefix,
		Environment: generated.Environment, Permissions: []identity.Permission{identity.PermCIRead},
		CreatedBy: f.AppUser(t, f.OrgA, "last-used-owner"),
	})
	if err != nil {
		t.Fatalf("create key: %v", err)
	}
	return key, generated.Plaintext
}

// keyRow is the state of an api_key row: xmin changes with every write of
// the row, so an unchanged xmin proves that no request wrote it.
type keyRow struct {
	xmin     string
	lastUsed *time.Time
}

func readKeyRow(t *testing.T, f *scopetest.Fixture, id string) keyRow {
	t.Helper()
	var r keyRow
	if err := f.Admin.QueryRow(context.Background(), `SELECT xmin::text, last_used_at FROM api_key WHERE id = $1`, id).
		Scan(&r.xmin, &r.lastUsed); err != nil {
		t.Fatalf("read api_key: %v", err)
	}
	return r
}

// TestAPIKeyLastUsedOncePerMinute checks WP-610 (U4 of the WP-439 load runs,
// NFR-02): every request with a key used to write api_key.last_used_at, and
// all requests of the key queued on that row. Now many requests within a
// minute write it once, a request after the minute writes it again, and the
// key list shows the time of the last write. Expiry is unaffected.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAPIKeyLastUsedOncePerMinute(t *testing.T) {
	f := scopetest.Seed(t, "04")
	bg := context.Background()
	store := identity.NewPGAPIKeyStore(f.App)
	service := identity.NewAPIKeyServiceWithStore(store)
	key, plaintext := lastUsedKey(t, f, store)
	validate := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if _, err := service.Validate(bg, plaintext); err != nil {
				t.Fatalf("validate: %v", err)
			}
		}
	}
	setLastUsed := func(ago time.Duration) keyRow {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `UPDATE api_key SET last_used_at = now() - make_interval(secs => $2) WHERE id = $1`, key.ID, ago.Seconds()); err != nil {
			t.Fatal(err)
		}
		return readKeyRow(t, f, key.ID)
	}

	created := readKeyRow(t, f, key.ID)
	if created.lastUsed != nil {
		t.Fatalf("new key: last_used_at %v, want none", created.lastUsed)
	}

	// The first request writes last_used_at, the next 24 within the minute
	// do not: one write for 25 requests.
	validate(1)
	first := readKeyRow(t, f, key.ID)
	if first.lastUsed == nil || first.xmin == created.xmin {
		t.Fatalf("first request: last_used_at %v (xmin %s, before %s), want it written", first.lastUsed, first.xmin, created.xmin)
	}
	validate(24)
	if got := readKeyRow(t, f, key.ID); got.xmin != first.xmin || got.lastUsed == nil || !got.lastUsed.Equal(*first.lastUsed) {
		t.Errorf("24 more requests within the minute wrote the row: xmin %s -> %s, last_used_at %v -> %v",
			first.xmin, got.xmin, first.lastUsed, got.lastUsed)
	}
	// What the key list shows is the written time.
	shown := func(want *time.Time) {
		t.Helper()
		listed, err := store.Get(f.OrgCtx(f.OrgA), f.OrgA, key.ID)
		if err != nil {
			t.Fatalf("get key: %v", err)
		}
		if listed.LastUsedAt == nil || !listed.LastUsedAt.Equal(*want) {
			t.Errorf("key list: last_used_at %v, want %v", listed.LastUsedAt, want)
		}
	}
	shown(first.lastUsed)

	// 30 seconds after the last write the row stays as it is ...
	before := setLastUsed(30 * time.Second)
	validate(5)
	if got := readKeyRow(t, f, key.ID); got.xmin != before.xmin {
		t.Errorf("requests 30 s after the last write wrote the row: xmin %s -> %s", before.xmin, got.xmin)
	}

	// ... after a minute the next request writes it, and only that one.
	before = setLastUsed(61 * time.Second)
	validate(1)
	next := readKeyRow(t, f, key.ID)
	if next.xmin == before.xmin || next.lastUsed == nil || !next.lastUsed.After(before.lastUsed.Add(time.Minute)) {
		t.Fatalf("request a minute after the last write: last_used_at %v -> %v (xmin %s -> %s), want it written",
			before.lastUsed, next.lastUsed, before.xmin, next.xmin)
	}
	validate(10)
	if got := readKeyRow(t, f, key.ID); got.xmin != next.xmin {
		t.Errorf("requests after the new write wrote the row again: xmin %s -> %s", next.xmin, got.xmin)
	}
	shown(next.lastUsed)

	// An expired key is refused whatever last_used_at says, and its use is
	// not recorded.
	setLastUsed(2 * time.Minute)
	if _, err := f.Admin.Exec(bg, `UPDATE api_key SET expires_at = now() - interval '1 minute' WHERE id = $1`, key.ID); err != nil {
		t.Fatal(err)
	}
	before = readKeyRow(t, f, key.ID)
	if _, err := service.Validate(bg, plaintext); err == nil {
		t.Error("expired key was accepted")
	}
	if got := readKeyRow(t, f, key.ID); got.xmin != before.xmin {
		t.Errorf("the use of an expired key was recorded: xmin %s -> %s", before.xmin, got.xmin)
	}
}

// TestAPIKeyMarkUsedDoesNotQueue checks that concurrent requests with one key
// no longer serialize on its api_key row (WP-610, U4): after the write of the
// minute, eight transactions mark the key as used at the same time and all
// stay open. None waits for another and none holds a lock on the row. Before
// WP-610 every transaction locked the row until its commit, so the second one
// waited for the first.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAPIKeyMarkUsedDoesNotQueue(t *testing.T) {
	f := scopetest.Seed(t, "05")
	bg := context.Background()
	store := identity.NewPGAPIKeyStore(f.App)
	key, _ := lastUsedKey(t, f, store)
	// The write of the minute.
	if err := store.MarkUsed(bg, f.OrgA, key.ID); err != nil {
		t.Fatalf("mark used: %v", err)
	}
	written := readKeyRow(t, f, key.ID)
	if written.lastUsed == nil {
		t.Fatal("last_used_at was not set")
	}

	const n = 8
	scope := database.OrgWideScope(f.OrgA, "")
	marked := make(chan error, n)
	release := make(chan struct{})
	var once sync.Once
	releaseAll := func() { once.Do(func() { close(release) }) }
	defer releaseAll()
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(bg, 30*time.Second)
			defer cancel()
			// Each request in its own transaction that stays open until
			// all of them have marked the key.
			_ = database.WithTenant(ctx, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
				marked <- store.MarkUsed(database.ContextWithTx(ctx, tx), f.OrgA, key.ID)
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil
			})
		}()
	}

	done := 0
	timeout := time.After(5 * time.Second)
wait:
	for done < n {
		select {
		case err := <-marked:
			if err != nil {
				t.Errorf("mark used: %v", err)
			}
			done++
		case <-timeout:
			break wait
		}
	}
	if done < n {
		t.Errorf("%d of %d open transactions marked the key within 5 s; the others wait for the api_key row", done, n)
	}
	// While all of them are open, the row is free.
	lockErr := func() error {
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(bg) }()
		_, err = tx.Exec(bg, `SELECT 1 FROM api_key WHERE id = $1 FOR UPDATE NOWAIT`, key.ID)
		return err
	}()
	if lockErr != nil {
		t.Errorf("the open transactions that marked the key hold its row: %v", lockErr)
	}
	releaseAll()
	wg.Wait()

	if got := readKeyRow(t, f, key.ID); got.xmin != written.xmin {
		t.Errorf("marking the key within the minute wrote the row: xmin %s -> %s", written.xmin, got.xmin)
	}
}

// TestMemoryAPIKeyStoreLastUsedOncePerMinute checks that the store of
// --no-db and the unit tests records a use at most once a minute, like the
// PostgreSQL store (WP-610).
func TestMemoryAPIKeyStoreLastUsedOncePerMinute(t *testing.T) {
	ctx := context.Background()
	store := identity.NewMemoryAPIKeyStore()
	org := "c0de0400-0000-4000-8000-000000000001"
	for _, c := range []struct {
		name  string
		ago   time.Duration
		write bool
	}{
		{"30 seconds ago", 30 * time.Second, false},
		{"61 seconds ago", 61 * time.Second, true},
	} {
		last := time.Now().UTC().Add(-c.ago)
		key, err := store.Create(ctx, &identity.StoredAPIKey{OrganizationID: org, Name: c.name, KeyPrefix: c.name, LastUsedAt: &last})
		if err != nil {
			t.Fatal(err)
		}
		if err = store.MarkUsed(ctx, org, key.ID); err != nil {
			t.Fatal(err)
		}
		got, err := store.Get(ctx, org, key.ID)
		if err != nil {
			t.Fatal(err)
		}
		if written := !got.LastUsedAt.Equal(last); written != c.write {
			t.Errorf("last use %s: last_used_at %v -> %v, written %t, want %t", c.name, last, got.LastUsedAt, written, c.write)
		}
	}
}
