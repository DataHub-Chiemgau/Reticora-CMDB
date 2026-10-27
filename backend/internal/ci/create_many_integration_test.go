package ci_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// TestCreateMany covers WP-333 (NFR-03, AUD-01, EVT-02, WP-207) against
// PostgreSQL with the write path of the server (audit, outbox, search index,
// provenance):
//   - a CI created in a batch (CreateMany + RecordDiscoveredBatch in one
//     transaction) is identical to one created on its own (Create +
//     RecordDiscoveredMany per CI) in its row, history, audit entry, event,
//     provenance and search document;
//   - the audit entries of a batch are chained consecutively in the order of
//     the items and the events numbered alike, with a statement count that
//     does not grow with the batch and no separate lock statement;
//   - a CI a unique index refuses (an existing CI, one the scope does not
//     show, another item of the same batch, a concurrent batch) is reported
//     by its position and nothing of it is written;
//   - any other error writes nothing for any item.
//
// The audit chain of the organization is verified after every batch.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCreateMany(t *testing.T) {
	f := scopetest.Seed(t, "f1")
	bg := context.Background()
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	pool, trace := tracingPool(t, f.App)
	newRepo := func() ci.Repository {
		pg := ci.NewPGRepositoryWithAudit(pool, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
		return ci.NewIndexingRepository(pg, search.NewCIIndexer(search.NewPGRepository(pool)))
	}
	repo := newRepo()
	creator, ok := repo.(ci.BatchCreator)
	if !ok {
		t.Fatal("the indexing repository does not offer CreateMany")
	}
	prov := override.NewPGRepository(pool)
	observed := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	seen := observed.Add(time.Minute)
	device := func(orgID, serial string, n int) *ci.Item {
		return &ci.Item{
			OrganizationID: orgID, CITypeID: serverType, Name: strings.ToLower(serial), Status: "active",
			Manufacturer: "Reticora Labs", Model: "R-1", SerialNumber: serial,
			HardwareUUID: fmt.Sprintf("4c4c4544-0000-4000-8000-%012x", n), ManagementIP: fmt.Sprintf("198.51.100.%d", n),
			PrimaryMAC: fmt.Sprintf("00:1b:21:f1:00:%02x", n), Hostname: strings.ToLower(serial),
			FQDN: strings.ToLower(serial) + ".example.test", SysObjectID: "1.3.6.1.4.1.9.1.1",
			Attributes: map[string]any{
				"fingerprint": map[string]any{"mac_addresses": []any{fmt.Sprintf("00:1b:21:f1:01:%02x", n)}},
				"raw_data":    map[string]any{"sys_descr": "Reticora " + serial},
				"rack":        "R7", "role": "database",
			},
			DiscoverySource: ci.SourceSNMP, FirstSeenAt: &seen, LastSeenAt: &seen,
		}
	}
	fields := func(item *ci.Item) map[string]any {
		return map[string]any{"name": item.Name, "manufacturer": item.Manufacturer, "model": item.Model,
			"serial_number": item.SerialNumber, "management_ip": item.ManagementIP, "rack": "R7", "role": "database"}
	}
	inTx := func(orgID string, fn func(ctx context.Context) error) error {
		return database.WithRequestTenantTx(f.OrgCtx(orgID), pool, orgID, fn)
	}
	verify := func(t *testing.T, orgID string) {
		t.Helper()
		if result, err := audit.Verify(bg, f.App, orgID); err != nil || !result.Intact {
			t.Fatalf("audit chain of %s: %+v, %v", orgID, result, err)
		}
	}
	count := func(t *testing.T, query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// written counts the rows a creation writes for the CIs with serials.
	written := func(t *testing.T, orgID string, serials []string) map[string]int {
		t.Helper()
		out := map[string]int{}
		for table, query := range map[string]string{
			"ci":              `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = ANY($2)`,
			"ci_change":       `SELECT count(*) FROM ci_change x JOIN ci c ON c.id = x.ci_id WHERE c.organization_id = $1 AND c.serial_number = ANY($2)`,
			"audit_log":       `SELECT count(*) FROM audit_log x JOIN ci c ON c.id = x.resource_id WHERE c.organization_id = $1 AND c.serial_number = ANY($2)`,
			"event_outbox":    `SELECT count(*) FROM event_outbox x JOIN ci c ON x.entity_id = c.id::text WHERE c.organization_id = $1 AND c.serial_number = ANY($2)`,
			"search_document": `SELECT count(*) FROM search_document x JOIN ci c ON c.id = x.entity_id WHERE c.organization_id = $1 AND c.serial_number = ANY($2)`,
		} {
			out[table] = count(t, query, orgID, serials)
		}
		return out
	}

	t.Run("equal to the single creation", func(t *testing.T) {
		serials := []string{"CM-EQ-1", "CM-EQ-2", "CM-EQ-3", "CM-EQ-4", "CM-EQ-5"}
		single, batch := make([]*ci.Item, len(serials)), make([]*ci.Item, len(serials))
		for i, serial := range serials {
			single[i], batch[i] = device(f.OrgA, serial, i+1), device(f.OrgB, serial, i+1)
		}
		for _, item := range single {
			if err := inTx(f.OrgA, func(ctx context.Context) error {
				if err := repo.Create(ctx, item); err != nil {
					return err
				}
				return prov.RecordDiscoveredMany(ctx, f.OrgA, item.ID, fields(item), item.DiscoverySource, observed)
			}); err != nil {
				t.Fatalf("single creation of %s: %v", item.SerialNumber, err)
			}
		}
		if err := inTx(f.OrgB, func(ctx context.Context) error {
			res, err := creator.CreateMany(ctx, f.OrgB, batch)
			if err != nil {
				return err
			}
			if len(res.Refused) != 0 {
				return fmt.Errorf("refused %v", res.Refused)
			}
			values := make([]override.DiscoveredValues, len(batch))
			for i, item := range batch {
				values[i] = override.DiscoveredValues{CIID: item.ID, Values: fields(item), Source: item.DiscoverySource, ObservedAt: observed}
			}
			return prov.RecordDiscoveredBatch(ctx, f.OrgB, values)
		}); err != nil {
			t.Fatalf("batch creation: %v", err)
		}
		for i := range serials {
			// What CreateMany sets on the item equals what Create sets.
			a, b := *single[i], *batch[i]
			if b.ID == "" || b.Version != a.Version || b.SchemaVersion != a.SchemaVersion || b.Health != a.Health ||
				b.SiteID != a.SiteID || b.RoomID != a.RoomID || b.LocationID != a.LocationID || b.CreatedAt == "" || b.UpdatedAt == "" {
				t.Errorf("%s: batch item %+v, single item %+v", serials[i], b, a)
			}
			want := persisted(t, f.Admin, f.OrgA, single[i].ID)
			got := persisted(t, f.Admin, f.OrgB, batch[i].ID)
			for table := range want {
				if !reflect.DeepEqual(got[table], want[table]) {
					w, _ := json.Marshal(want[table])
					g, _ := json.Marshal(got[table])
					t.Errorf("%s: %s of the batch differs from the single creation:\n batch  %s\n single %s", serials[i], table, g, w)
				}
			}
		}
		verify(t, f.OrgA)
		verify(t, f.OrgB)
	})

	t.Run("audit chain and event order", func(t *testing.T) {
		create := func(prefix string, n int) []*ci.Item {
			t.Helper()
			items := make([]*ci.Item, n)
			for i := range items {
				items[i] = device(f.OrgB, fmt.Sprintf("%s-%02d", prefix, i), 100+i)
				items[i].HardwareUUID, items[i].PrimaryMAC = "", ""
			}
			trace.take()
			if err := inTx(f.OrgB, func(ctx context.Context) error {
				_, err := creator.CreateMany(ctx, f.OrgB, items)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			return items
		}
		create("CM-ORDER-S", 5)
		smallTx := trace.take()
		items := create("CM-ORDER", 25)
		largeTx := trace.take()
		// BEGIN, INSERT ci, ci_change, audit_log, event_outbox,
		// search_document, COMMIT: the same for 5 and 25 CIs, and the
		// trigger of audit_log takes the chain lock (one lock per batch).
		if len(largeTx) != len(smallTx) || len(largeTx) > 7 {
			t.Errorf("CreateMany sent %d statements for 25 CIs and %d for 5, want the same and at most 7: %q", len(largeTx), len(smallTx), largeTx)
		}
		for _, sql := range largeTx {
			if strings.Contains(sql, "pg_advisory_xact_lock") {
				t.Errorf("CreateMany takes the audit lock with a statement of its own: %s", sql)
			}
		}
		rows, err := f.Admin.Query(bg, `SELECT a.resource_id::text, a.seq, e.sequence, e.entity_seq FROM audit_log a
			JOIN event_outbox e ON e.organization_id = a.organization_id AND e.entity_id = a.resource_id::text AND e.type = 'ci.created'
			JOIN ci c ON c.id = a.resource_id
			WHERE a.organization_id = $1 AND a.action = 'ci.created' AND c.serial_number LIKE 'CM-ORDER-%' AND c.serial_number NOT LIKE 'CM-ORDER-S%'
			ORDER BY a.seq`, f.OrgB)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		i := 0
		var prevSeq, prevSequence int64
		for ; rows.Next(); i++ {
			var id string
			var seq, sequence, entitySeq int64
			if err := rows.Scan(&id, &seq, &sequence, &entitySeq); err != nil {
				t.Fatal(err)
			}
			if i >= len(items) || id != items[i].ID || entitySeq != 1 || (i > 0 && (seq != prevSeq+1 || sequence != prevSequence+1)) {
				t.Errorf("entry %d: CI %s, audit seq %d after %d, event sequence %d after %d, entity_seq %d; want the item order, consecutive",
					i, id, seq, prevSeq, sequence, prevSequence, entitySeq)
			}
			prevSeq, prevSequence = seq, sequence
		}
		if err := rows.Err(); err != nil || i != len(items) {
			t.Errorf("%d audit entries with events for %d CIs: %v", i, len(items), err)
		}
		verify(t, f.OrgB)
	})

	t.Run("unique refusal", func(t *testing.T) {
		existing := device(f.OrgA, "CM-UQ-2", 52)
		if err := repo.Create(f.OrgCtx(f.OrgA), existing); err != nil {
			t.Fatal(err)
		}
		before := written(t, f.OrgA, []string{"CM-UQ-1", "CM-UQ-3"})
		// The second item takes the serial of the existing CI, the fourth
		// the hardware UUID of the third (same batch).
		items := []*ci.Item{device(f.OrgA, "CM-UQ-1", 51), device(f.OrgA, "CM-UQ-2", 62), device(f.OrgA, "CM-UQ-3", 53),
			device(f.OrgA, "CM-UQ-4", 54)}
		items[3].HardwareUUID = items[2].HardwareUUID
		var res ci.CreateManyResult
		if err := inTx(f.OrgA, func(ctx context.Context) error {
			var err error
			res, err = creator.CreateMany(ctx, f.OrgA, items)
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(res.Refused, []int{1, 3}) || items[1].ID != "" || items[3].ID != "" || items[0].ID == "" || items[2].ID == "" {
			t.Fatalf("refused %v, ids %q %q %q %q; want [1 3] and no id for them", res.Refused, items[0].ID, items[1].ID, items[2].ID, items[3].ID)
		}
		after := written(t, f.OrgA, []string{"CM-UQ-1", "CM-UQ-3"})
		for table, n := range after {
			if n-before[table] != 2 {
				t.Errorf("%s: %d rows for the 2 created CIs, want 2", table, n-before[table])
			}
		}
		if n := count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number IN ('CM-UQ-2', 'CM-UQ-4')`, f.OrgA); n != 1 {
			t.Errorf("%d CIs with the refused serials, want only the existing one", n)
		}
		verify(t, f.OrgA)

		// A CI of another client is invisible to a principal restricted to
		// Client1, and its serial is still refused by position.
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, serial_number)
			VALUES ($1, $2, $3, 'hidden', 'active', 'CM-UQ-HIDDEN')`, f.OrgA, f.Client2, serverType); err != nil {
			t.Fatal(err)
		}
		scoped := []*ci.Item{device(f.OrgA, "CM-UQ-5", 55), device(f.OrgA, "CM-UQ-HIDDEN", 56)}
		for _, item := range scoped {
			item.ClientID = f.Client1
		}
		res, err := creator.CreateMany(f.ClientCtx(f.Client1), f.OrgA, scoped)
		if err != nil || !slices.Equal(res.Refused, []int{1}) || scoped[0].ID == "" {
			t.Fatalf("restricted principal: refused %v, err %v, id %q; want [1]", res.Refused, err, scoped[0].ID)
		}
		verify(t, f.OrgA)
	})

	t.Run("concurrent batches", func(t *testing.T) {
		for round := 0; round < 5; round++ {
			serials := make([]string, 10)
			for i := range serials {
				serials[i] = fmt.Sprintf("CM-CC-%d-%02d", round, i)
			}
			// Each batch has items of its own: CreateMany sets their ids.
			batch := func(reverse bool) []*ci.Item {
				items := make([]*ci.Item, len(serials))
				for i, serial := range serials {
					items[i] = device(f.OrgB, serial, 200+i)
					items[i].HardwareUUID, items[i].PrimaryMAC = "", ""
				}
				if reverse {
					slices.Reverse(items)
				}
				return items
			}
			sets := [][]*ci.Item{batch(false), batch(true)}
			errs := make([]error, len(sets))
			start := make(chan struct{})
			var wg sync.WaitGroup
			for g := range sets {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					errs[g] = database.WithRequestTenantTx(f.OrgCtx(f.OrgB), pool, f.OrgB, func(ctx context.Context) error {
						_, err := creator.CreateMany(ctx, f.OrgB, sets[g])
						return err
					})
				}()
			}
			close(start)
			wg.Wait()
			created := map[string]int{}
			for g, items := range sets {
				if errs[g] != nil {
					t.Fatalf("round %d: batch %d: %v", round, g, errs[g])
				}
				for _, item := range items {
					if item.ID != "" {
						created[item.SerialNumber]++
					}
				}
			}
			for _, serial := range serials {
				if created[serial] != 1 {
					t.Errorf("round %d: %s created %d times by the two batches, want once", round, serial, created[serial])
				}
			}
			if n := count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = ANY($2)`, f.OrgB, serials); n != len(serials) {
				t.Errorf("round %d: %d CIs for %d serials", round, n, len(serials))
			}
		}
		verify(t, f.OrgB)
	})

	t.Run("all or nothing", func(t *testing.T) {
		serials := []string{"CM-ALL-1", "CM-ALL-2", "CM-ALL-3"}
		items := make([]*ci.Item, len(serials))
		for i, serial := range serials {
			items[i] = device(f.OrgA, serial, 70+i)
		}
		items[2].LocationID = "c0def1a0-0000-4000-8000-00000000dead"
		_, err := creator.CreateMany(f.OrgCtx(f.OrgA), f.OrgA, items)
		var invalid *ci.ValidationError
		if err == nil || !errors.As(err, &invalid) {
			t.Fatalf("batch with an unknown location: %v, want a validation error", err)
		}
		for table, n := range written(t, f.OrgA, serials) {
			if n != 0 {
				t.Errorf("%s: %d rows of the failed batch", table, n)
			}
		}
		verify(t, f.OrgA)
	})
}

// sqlTrace records the statements a pool sends, lower case; SET ROLE is the
// connection setup of the pool.
type sqlTrace struct {
	mu  sync.Mutex
	sql []string
}

func (tr *sqlTrace) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := strings.ToLower(strings.TrimSpace(data.SQL))
	if !strings.HasPrefix(sql, "set role") {
		tr.mu.Lock()
		tr.sql = append(tr.sql, sql)
		tr.mu.Unlock()
	}
	return ctx
}

func (tr *sqlTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// take returns the statements since the last call.
func (tr *sqlTrace) take() []string {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	out := tr.sql
	tr.sql = nil
	return out
}

// tracingPool is the application pool with a statement trace.
func tracingPool(t *testing.T, app *pgxpool.Pool) (*pgxpool.Pool, *sqlTrace) {
	t.Helper()
	cfg := app.Config()
	trace := &sqlTrace{}
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool, trace
}

// varying are the keys whose values differ between two creations of the
// same CI: ids, organizations, change sets (AUD-03), times and the hashes
// over them.
var varying = map[string]bool{
	"id": true, "organization_id": true, "ci_id": true, "entity_id": true, "resource_id": true, "change_set_id": true,
	"created_at": true, "updated_at": true, "first_seen_at": true, "last_seen_at": true, "discovered_at": true,
	"timestamp": true, "occurred_at": true, "hash": true, "previous_hash": true,
}

// persisted reads what the creation of a CI wrote, per table, with ids and
// times replaced so that two creations of the same CI compare equal.
func persisted(t *testing.T, admin *pgxpool.Pool, orgID, ciID string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for table, query := range map[string]string{
		"ci":              `SELECT jsonb_agg(to_jsonb(x)) FROM ci x WHERE x.id = $1`,
		"ci_change":       `SELECT jsonb_agg(to_jsonb(x) ORDER BY x.change_type, x.field_name) FROM ci_change x WHERE x.ci_id = $1`,
		"audit_log":       `SELECT jsonb_agg(to_jsonb(x) ORDER BY x.seq) FROM audit_log x WHERE x.resource_id = $1`,
		"event_outbox":    `SELECT jsonb_agg(to_jsonb(x) - 'published_at' - 'attempts' - 'last_error' ORDER BY x.sequence) FROM event_outbox x WHERE x.entity_id = $1::text`,
		"ci_field_value":  `SELECT jsonb_agg(to_jsonb(x) ORDER BY x.field_name) FROM ci_field_value x WHERE x.ci_id = $1`,
		"search_document": `SELECT jsonb_agg(to_jsonb(x)) FROM search_document x WHERE x.entity_id = $1`,
	} {
		var raw []byte
		if err := admin.QueryRow(context.Background(), query, ciID).Scan(&raw); err != nil {
			t.Fatalf("%s of %s: %v", table, ciID, err)
		}
		var v any
		if raw != nil {
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
		}
		if v == nil {
			t.Errorf("%s: no row for CI %s", table, ciID)
		}
		out[table] = normalized(v, ciID, orgID)
	}
	return out
}

// normalized replaces the values of the varying keys and the given ids
// inside strings by placeholders.
func normalized(v any, ids ...string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if varying[k] && val != nil {
				out[k] = "<" + k + ">"
				continue
			}
			out[k] = normalized(val, ids...)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = normalized(x[i], ids...)
		}
		return out
	case string:
		for _, id := range ids {
			x = strings.ReplaceAll(x, id, "<id>")
		}
		return x
	}
	return v
}
