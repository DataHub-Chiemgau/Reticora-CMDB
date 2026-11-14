package configsnap_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm/configsnap"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// Store of the configuration versions (ALR-07, WP-1052, migration
// 000314_config_snapshot) against PostgreSQL as the restricted runtime
// login (scopetest.AppDSN); seeds go through the maintenance pool.

type env struct {
	t     *testing.T
	f     *scopetest.Fixture
	store *configsnap.PGStore
	ctx   context.Context
}

func newEnv(t *testing.T) *env {
	t.Helper()
	f := scopetest.Seed(t, "e9")
	return &env{t: t, f: f, store: configsnap.NewPGStore(f.App), ctx: f.OrgCtx(f.OrgA)}
}

var t0 = time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)

func snapshot(ciID, text string, at time.Time) *configsnap.Snapshot {
	return &configsnap.Snapshot{
		CIID: ciID, CollectedAt: at, Source: configsnap.SourceSSH, Trigger: configsnap.TriggerSchedule,
		Platform: "cisco_ios", Content: []byte(text), MaskedCount: 2,
	}
}

func (e *env) insert(ctx context.Context, ciID, text string, at time.Time) (stored, previous *configsnap.Snapshot, created bool) {
	e.t.Helper()
	stored, previous, created, err := e.store.InsertIfChanged(ctx, e.f.OrgA, snapshot(ciID, text, at))
	if err != nil {
		e.t.Fatalf("insert %q: %v", text, err)
	}
	return stored, previous, created
}

func (e *env) rows(ciID string) int {
	e.t.Helper()
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM config_snapshot WHERE ci_id = $1`, ciID).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

const configA = "hostname edge1\nusername admin secret <masked>\n"
const configB = "hostname edge1\nusername admin secret <masked>\nsnmp-server community <masked> RO\n"

func TestInsertIfChangedSameHashNoRow(t *testing.T) {
	e := newEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "cfg-same")
	first, prev, created := e.insert(e.ctx, ci, configA, t0)
	if !created || prev != nil || first.SizeBytes != len(configA) || len(first.SHA256) != 64 || first.MaskedCount != 2 {
		t.Fatalf("first version: created %v, previous %v, %+v", created, prev, first)
	}
	if first.ClientID == nil || *first.ClientID != e.f.Client1 {
		t.Errorf("client %v, want the CI's %s", first.ClientID, e.f.Client1)
	}
	again, prev, created := e.insert(e.ctx, ci, configA, t0.Add(time.Hour))
	if created || prev != nil || again.ID != first.ID {
		t.Errorf("same hash: created %v, previous %v, stored %s; want the unchanged version %s", created, prev, again.ID, first.ID)
	}
	if n := e.rows(ci); n != 1 {
		t.Errorf("%d rows, want 1", n)
	}
	got, err := e.store.Get(e.ctx, e.f.OrgA, first.ID)
	if err != nil || string(got.Content) != configA {
		t.Errorf("get: %q, %v; want the decompressed text", got.Content, err)
	}
	latest, err := e.store.Latest(e.ctx, e.f.OrgA, ci)
	if err != nil || latest.ID != first.ID || latest.Content != nil {
		t.Errorf("latest: %+v, %v", latest, err)
	}
	// The stored content is gzip, not the text.
	var raw []byte
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT content FROM config_snapshot WHERE id = $1`, first.ID).Scan(&raw); err != nil ||
		!bytes.HasPrefix(raw, []byte{0x1f, 0x8b}) {
		t.Errorf("stored content is not gzip: % x, %v", raw[:min(4, len(raw))], err)
	}
	// A given hash must be that of the content.
	bad := snapshot(ci, configB, t0.Add(2*time.Hour))
	bad.SHA256 = first.SHA256
	if _, _, _, err := e.store.InsertIfChanged(e.ctx, e.f.OrgA, bad); !errors.Is(err, configsnap.ErrHashMismatch) {
		t.Errorf("foreign hash: %v, want ErrHashMismatch", err)
	}
}

func TestInsertIfChangedRevertIsStored(t *testing.T) {
	e := newEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "cfg-revert")
	a1, _, _ := e.insert(e.ctx, ci, configA, t0)
	b, prev, created := e.insert(e.ctx, ci, configB, t0.Add(time.Hour))
	if !created || prev == nil || prev.ID != a1.ID {
		t.Fatalf("B: created %v, previous %v; want the previous A %s", created, prev, a1.ID)
	}
	a2, prev, created := e.insert(e.ctx, ci, configA, t0.Add(2*time.Hour))
	if !created || prev == nil || prev.ID != b.ID || a2.SHA256 != a1.SHA256 || a2.ID == a1.ID {
		t.Errorf("back to A: created %v, previous %v, %+v; want a new row after B", created, prev, a2)
	}
	if n := e.rows(ci); n != 3 {
		t.Errorf("A -> B -> A: %d rows, want 3", n)
	}
	// Newest first, in pages of two, without content.
	page, err := e.store.List(e.ctx, e.f.OrgA, ci, nil, 2)
	if err != nil || len(page.Items) != 2 || page.Items[0].ID != a2.ID || page.Next == nil || page.Items[0].Content != nil {
		t.Fatalf("first page: %+v, %v", page, err)
	}
	page, err = e.store.List(e.ctx, e.f.OrgA, ci, page.Next, 2)
	if err != nil || len(page.Items) != 1 || page.Items[0].ID != a1.ID || page.Next != nil {
		t.Errorf("second page: %+v, %v", page, err)
	}
}

func TestInsertIfChangedConcurrent(t *testing.T) {
	e := newEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "cfg-concurrent")
	const writers = 2
	var wg sync.WaitGroup
	results := make([]bool, writers)
	errs := make([]error, writers)
	for i := range writers {
		wg.Go(func() {
			// Different collection times: without the lock both would be
			// stored.
			_, _, created, err := e.store.InsertIfChanged(e.f.OrgCtx(e.f.OrgA), e.f.OrgA,
				snapshot(ci, configA, t0.Add(time.Duration(i)*time.Second)))
			results[i], errs[i] = created, err
		})
	}
	wg.Wait()
	created := 0
	for i := range writers {
		if errs[i] != nil {
			t.Fatalf("writer %d: %v", i, errs[i])
		}
		if results[i] {
			created++
		}
	}
	if created != 1 || e.rows(ci) != 1 {
		t.Errorf("parallel inserts of one hash: %d created, %d rows; want one", created, e.rows(ci))
	}
}

func TestSnapshotSizeLimit(t *testing.T) {
	e := newEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "cfg-size")
	line := "interface GigabitEthernet0/1\n"
	limit := strings.Repeat(line, configsnap.MaxSize/len(line)+1)[:configsnap.MaxSize]
	if _, _, created, err := e.store.InsertIfChanged(e.ctx, e.f.OrgA, snapshot(ci, limit, t0)); err != nil || !created {
		t.Fatalf("exactly 2 MiB: created %v, %v", created, err)
	}
	if _, _, _, err := e.store.InsertIfChanged(e.ctx, e.f.OrgA, snapshot(ci, limit+"!", t0.Add(time.Hour))); !errors.Is(err, configsnap.ErrTooLarge) {
		t.Errorf("2 MiB + 1: %v, want ErrTooLarge", err)
	}
	// The database refuses it as well, uncompressed and compressed.
	for name, sql := range map[string]string{
		"size_bytes": `INSERT INTO config_snapshot (organization_id, ci_id, collected_at, source, trigger, platform, sha256, size_bytes, content)
			VALUES ($1, $2, now(), 'ssh', 'schedule', 'cisco_ios', repeat('a', 64), 2097153, '\x00')`,
		"content": `INSERT INTO config_snapshot (organization_id, ci_id, collected_at, source, trigger, platform, sha256, size_bytes, content)
			VALUES ($1, $2, now(), 'ssh', 'schedule', 'cisco_ios', repeat('a', 64), 1, decode(repeat('00', 2097153), 'hex'))`,
	} {
		if _, err := e.f.Admin.Exec(context.Background(), sql, e.f.OrgA, ci); err == nil || !strings.Contains(err.Error(), "check") {
			t.Errorf("%s over 2 MiB: %v, want a check violation", name, err)
		}
	}
	got, err := e.store.Get(e.ctx, e.f.OrgA, mustLatest(t, e, ci))
	if err != nil || len(got.Content) != configsnap.MaxSize {
		t.Errorf("get of 2 MiB: %d bytes, %v", len(got.Content), err)
	}
}

func mustLatest(t *testing.T, e *env, ci string) string {
	t.Helper()
	latest, err := e.store.Latest(e.ctx, e.f.OrgA, ci)
	if err != nil {
		t.Fatal(err)
	}
	return latest.ID
}

func TestSnapshotScope(t *testing.T) {
	e := newEnv(t)
	own := e.f.CI(t, e.f.OrgA, e.f.Client1, "cfg-own")
	foreign := e.f.CI(t, e.f.OrgA, e.f.Client2, "cfg-foreign")
	e.insert(e.ctx, own, configA, t0)
	foreignSnap, _, _ := e.insert(e.ctx, foreign, configA, t0)

	client1 := e.f.ClientCtx(e.f.Client1)
	if page, err := e.store.List(client1, e.f.OrgA, own, nil, 0); err != nil || len(page.Items) != 1 {
		t.Errorf("own CI: %d versions, %v; want 1", len(page.Items), err)
	}
	if page, err := e.store.List(client1, e.f.OrgA, foreign, nil, 0); err != nil || len(page.Items) != 0 {
		t.Errorf("foreign CI: %d versions, %v; want none", len(page.Items), err)
	}
	if _, err := e.store.Get(client1, e.f.OrgA, foreignSnap.ID); !errors.Is(err, configsnap.ErrNotFound) {
		t.Errorf("get of a foreign version: %v, want ErrNotFound", err)
	}
	if _, err := e.store.Latest(client1, e.f.OrgA, foreign); !errors.Is(err, configsnap.ErrNotFound) {
		t.Errorf("latest of a foreign CI: %v, want ErrNotFound", err)
	}
	if _, _, _, err := e.store.InsertIfChanged(client1, e.f.OrgA, snapshot(foreign, configB, t0.Add(time.Hour))); !errors.Is(err, configsnap.ErrCINotFound) {
		t.Errorf("insert for a foreign CI: %v, want ErrCINotFound", err)
	}
	if _, _, created, err := e.store.InsertIfChanged(client1, e.f.OrgA, snapshot(own, configB, t0.Add(time.Hour))); err != nil || !created {
		t.Errorf("insert for the own CI: created %v, %v", created, err)
	}
}

func TestConfigSnapshotRLS(t *testing.T) {
	e := newEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "cfg-rls")
	snap, _, _ := e.insert(e.ctx, ci, configA, t0)
	var enabled, forced bool
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE relname = 'config_snapshot'`).
		Scan(&enabled, &forced); err != nil || !enabled || !forced {
		t.Fatalf("config_snapshot: RLS %v, FORCE %v, %v", enabled, forced, err)
	}
	// Organization B reads nothing of A.
	if _, err := e.store.Get(e.f.OrgCtx(e.f.OrgB), e.f.OrgB, snap.ID); !errors.Is(err, configsnap.ErrNotFound) {
		t.Errorf("organization B reads A's version: %v", err)
	}
	// A principal of client 2 neither writes a row of client 1 nor deletes one.
	client2 := e.f.ClientCtx(e.f.Client2)
	err := database.WithRequestTenant(client2, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `INSERT INTO config_snapshot (organization_id, client_id, ci_id, collected_at, source, trigger, platform, sha256, size_bytes, content)
			VALUES ($1, $2, $3, now(), 'ssh', 'schedule', 'cisco_ios', repeat('b', 64), 1, '\x00')`, e.f.OrgA, e.f.Client1, ci)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "row-level security") {
		t.Errorf("insert of client 1's row by client 2: %v, want the policy", err)
	}
	var deleted int64
	if err := database.WithRequestTenant(client2, e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM config_snapshot WHERE id = $1`, snap.ID)
		deleted = tag.RowsAffected()
		return err
	}); err != nil || deleted != 0 {
		t.Errorf("delete by client 2: %d rows, %v", deleted, err)
	}
	// The foreign keys carry the organization: a CI of B is refused.
	ciB := e.f.CI(t, e.f.OrgB, "", "cfg-rls-b")
	if _, err := e.f.Admin.Exec(context.Background(), `INSERT INTO config_snapshot (organization_id, ci_id, collected_at, source, trigger, platform, sha256, size_bytes, content)
		VALUES ($1, $2, now(), 'ssh', 'schedule', 'cisco_ios', repeat('c', 64), 1, '\x00')`, e.f.OrgA, ciB); err == nil {
		t.Error("a version of organization A referenced a CI of B")
	}
}

// TestInsertIfChangedLateDeliveryNotStored: a version collected before the
// latest one (A@t1, B@t3, then A@t2 from a replayed buffer) is not the
// current configuration and reports no change; the same collection time
// with another hash neither.
func TestInsertIfChangedLateDeliveryNotStored(t *testing.T) {
	e := newEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "cfg-late")
	e.insert(e.ctx, ci, configA, t0)
	b, _, _ := e.insert(e.ctx, ci, configB, t0.Add(2*time.Hour))
	for name, at := range map[string]time.Time{"earlier": t0.Add(time.Hour), "same time": t0.Add(2 * time.Hour)} {
		stored, previous, created, err := e.store.InsertIfChanged(e.ctx, e.f.OrgA, snapshot(ci, configA, at))
		if !errors.Is(err, configsnap.ErrOutOfOrder) || created || stored != nil || previous != nil {
			t.Errorf("%s: created %v, previous %v, %v; want ErrOutOfOrder", name, created, previous, err)
		}
	}
	// A late copy of the current configuration stays a no-op.
	if stored, _, created, err := e.store.InsertIfChanged(e.ctx, e.f.OrgA, snapshot(ci, configB, t0.Add(time.Hour))); err != nil || created || stored.ID != b.ID {
		t.Errorf("late copy of the latest: created %v, %v", created, err)
	}
	if n := e.rows(ci); n != 2 {
		t.Errorf("rows after the late deliveries: %d, want 2", n)
	}
	if latest, err := e.store.Latest(e.ctx, e.f.OrgA, ci); err != nil || latest.ID != b.ID {
		t.Errorf("latest: %+v, %v; want B", latest, err)
	}
}

// TestSnapshotScopeFollowsMovedCI: the versions carry the scope of their
// CI whatever the writer passes, and a CI moved to another client carries
// its versions along.
func TestSnapshotScopeFollowsMovedCI(t *testing.T) {
	e := newEnv(t)
	ci := e.f.CI(t, e.f.OrgA, e.f.Client1, "cfg-moved")
	snap, _, _ := e.insert(e.ctx, ci, configA, t0)
	ctx := context.Background()
	// A writer passing another client gets the CI's.
	var client string
	if err := e.f.Admin.QueryRow(ctx, `INSERT INTO config_snapshot (organization_id, client_id, ci_id, collected_at, source, trigger, platform, sha256, size_bytes, content)
		VALUES ($1, $2, $3, $4, 'ssh', 'schedule', 'cisco_ios', repeat('d', 64), 1, '\x00') RETURNING client_id::text`,
		e.f.OrgA, e.f.Client2, ci, t0.Add(-time.Hour)).Scan(&client); err != nil || client != e.f.Client1 {
		t.Errorf("version written with client 2: client %s, %v; want the CI's client 1", client, err)
	}
	if _, err := e.f.Admin.Exec(ctx, `UPDATE ci SET client_id = $2 WHERE id = $1`, ci, e.f.Client2); err != nil {
		t.Fatalf("move the CI to client 2: %v", err)
	}
	client1, client2 := e.f.ClientCtx(e.f.Client1), e.f.ClientCtx(e.f.Client2)
	if page, err := e.store.List(client1, e.f.OrgA, ci, nil, 0); err != nil || len(page.Items) != 0 {
		t.Errorf("former client: %d versions, %v; want none", len(page.Items), err)
	}
	if _, err := e.store.Get(client1, e.f.OrgA, snap.ID); !errors.Is(err, configsnap.ErrNotFound) {
		t.Errorf("former client reads a version: %v, want ErrNotFound", err)
	}
	page, err := e.store.List(client2, e.f.OrgA, ci, nil, 0)
	if err != nil || len(page.Items) != 2 || page.Items[0].ClientID == nil || *page.Items[0].ClientID != e.f.Client2 {
		t.Fatalf("new client: %+v, %v; want both versions of client 2", page.Items, err)
	}
	// The new client compares with the history: the same hash stores nothing.
	if _, _, created, err := e.store.InsertIfChanged(client2, e.f.OrgA, snapshot(ci, configA, t0.Add(time.Hour))); err != nil || created {
		t.Errorf("same hash after the move: created %v, %v; want no row", created, err)
	}
}
