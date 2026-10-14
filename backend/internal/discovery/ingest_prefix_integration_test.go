package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// logCapture collects the structured log records of the default logger
// while a test runs.
type logCapture struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.Write(p)
}

// events returns the records with the given event attribute.
func (c *logCapture) events(t *testing.T, event string) []map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []map[string]any
	for _, line := range bytes.Split(c.buf.Bytes(), []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal(line, &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["event"] == event {
			out = append(out, rec)
		}
	}
	return out
}

func captureLogs(t *testing.T) *logCapture {
	t.Helper()
	c := &logCapture{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(c, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return c
}

// TestIngestKeepsPrefixOfFailedBatch covers decision D-3 of WP-332 (NFR-03,
// REC-01) against PostgreSQL with the write path of the server: a batch of
// three new devices whose second record fails late in its transaction
//   - record by record: the first record stays committed, gets its topology
//     edge and its ledger entry; the failed record and the one after it leave
//     nothing; the redelivered batch skips the first as a duplicate and
//     creates the other two;
//   - with the batch of WP-333: the new devices of the window (first and
//     third) are created together before the unclassified second runs on its
//     own, so both stay committed and in the ledger.
//
// The review the failed record opened is rolled back with it and never
// logged as opened; the redelivered record logs it once, after its commit.
// The ledger takes a record_id in either case of its hex digits as one id.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestKeepsPrefixOfFailedBatch(t *testing.T) {
	f := scopetest.Seed(t, "f0")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	logs := captureLogs(t)

	// The LLDP neighbor of the first device exists already.
	const peerMAC = "00:1b:21:f0:00:01"
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	peer := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, ci_type_id, name, status, primary_mac) VALUES ($1, $2, $3, 'd3-peer', 'active', $4)`,
		peer, f.OrgA, serverType, peerMAC); err != nil {
		t.Fatal(err)
	}

	syncer := &failOnce{next: ipam.NewPGRepository(f.App)}
	handler := func(batch bool) http.Handler {
		cis := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()),
			search.NewCIIndexer(search.NewPGRepository(f.App)))
		if !batch {
			cis = noBatch{cis}
		}
		mux := chi.NewRouter()
		discovery.NewHandler(discovery.NewPGRepository(f.App), cis, relationship.NewPGRepository(f.App)).
			WithProvenance(manyProvenance{provenance{repo: override.NewPGRepository(f.App)}}).
			WithInterfaces(syncer).RegisterRoutes(mux)
		return mux
	}
	post := func(mux http.Handler, records []plugins.DeviceRecord) (int, discovery.BulkIngestResponse) {
		t.Helper()
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		var resp discovery.BulkIngestResponse
		if w.Code == http.StatusAccepted {
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
		}
		return w.Code, resp
	}
	ciOf := func(serial string) string {
		t.Helper()
		var id string
		err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = $2`, f.OrgA, serial).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	counted := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, query, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ledgered := func(recordID string) bool {
		return counted(`SELECT count(*) FROM ingest_record WHERE organization_id = $1 AND record_id = $2::uuid`, f.OrgA, recordID) == 1
	}
	edges := func(source string) int {
		return counted(`SELECT count(*) FROM ci_relationship WHERE organization_id = $1 AND source_ci_id = $2 AND target_ci_id = $3`,
			f.OrgA, source, peer)
	}
	unclassified := func(serial string) int {
		return counted(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'unclassified_device'
			AND payload->>'serial_number' = $2`, f.OrgA, serial)
	}
	openedLogs := func() int { return len(logs.events(t, "review.opened")) }

	for _, tc := range []struct {
		name  string
		batch bool
		// committed lists which of the three records the failed batch
		// leaves committed.
		committed [3]bool
	}{
		{"record by record", false, [3]bool{true, false, false}},
		{"batch", true, [3]bool{true, false, true}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := handler(tc.batch)
			prefix := strings.ToUpper(strings.ReplaceAll(tc.name, " ", "-"))
			at := time.Now().UTC().Add(-time.Minute)
			records := make([]plugins.DeviceRecord, 3)
			serials := make([]string, 3)
			for i := range records {
				serials[i] = fmt.Sprintf("D3-%s-%d", prefix, i)
				ip := fmt.Sprintf("198.51.100.%d", 230+i)
				if tc.batch {
					ip = fmt.Sprintf("198.51.100.%d", 240+i)
				}
				records[i] = plugins.DeviceRecord{
					RecordID: plugins.NewRecordID(), ObservedAt: at.Add(time.Duration(i) * time.Second), Source: "snmp", TargetIP: ip,
					Identity:       plugins.DeviceIdentity{SerialNumber: serials[i]},
					Classification: plugins.DeviceClassification{CIType: "server", Manufacturer: "Reticora Labs", Model: "D3-1U"},
					System:         plugins.DeviceSystem{Name: strings.ToLower(serials[i])},
				}
			}
			records[0].Neighbors = []plugins.DeviceNeighbor{{Protocol: "lldp", LocalInterface: "Gi1/0/1", RemoteMAC: peerMAC}}
			// The second device: unclassified (its review is written in its
			// transaction), with an interface whose sync fails after its
			// writes.
			records[1].Classification.CIType = "generic_device"
			records[1].Attributes = map[string]any{"classified_by": "fallback"}
			records[1].Interfaces = []plugins.DeviceInterface{{Name: "eth0", MAC: fmt.Sprintf("00:1b:21:f0:01:%02x", len(prefix)),
				IPs: []plugins.DeviceIP{{Address: records[1].TargetIP, PrefixLength: 24}}}}

			logsBefore := openedLogs()
			syncer.arm()
			if code, _ := post(mux, records); code == http.StatusAccepted || syncer.armed.Load() {
				t.Fatalf("ingest with a failing second record: %d, failure injected %v", code, !syncer.armed.Load())
			}
			want := struct{ created, duplicates int }{}
			for i, serial := range serials {
				id := ciOf(serial)
				if (id != "") != tc.committed[i] || ledgered(records[i].RecordID) != tc.committed[i] {
					t.Errorf("record %d after the failure: CI %q, ledger %v; want both %v", i, id, ledgered(records[i].RecordID), tc.committed[i])
				}
				if tc.committed[i] {
					want.duplicates++
				} else {
					want.created++
				}
			}
			if source := ciOf(serials[0]); source != "" && edges(source) != 1 {
				t.Errorf("topology of the committed first record: %d edges to its LLDP neighbor, want 1", edges(source))
			}
			if n := unclassified(serials[1]); n != 0 || openedLogs() != logsBefore {
				t.Errorf("failed record: %d reviews, %d review.opened logs; want none", n, openedLogs()-logsBefore)
			}

			// The queue delivers the batch again: the committed records are
			// duplicates, the others are created once.
			code, resp := post(mux, records)
			if code != http.StatusAccepted || resp.Duplicates != want.duplicates || resp.Created != want.created {
				t.Fatalf("redelivery: %d %+v; want %d duplicates and %d created", code, resp, want.duplicates, want.created)
			}
			for i, serial := range serials {
				if ciOf(serial) == "" || !ledgered(records[i].RecordID) {
					t.Errorf("record %d after the redelivery: CI %q, ledger %v", i, ciOf(serial), ledgered(records[i].RecordID))
				}
			}
			if n := edges(ciOf(serials[0])); n != 1 {
				t.Errorf("after the redelivery: %d edges from the first device, want 1", n)
			}
			opened := logs.events(t, "review.opened")[logsBefore:]
			if n := unclassified(serials[1]); n != 1 || len(opened) != 1 || opened[0]["kind"] != "unclassified_device" {
				t.Errorf("redelivered record: %d reviews, review.opened logs %v; want one each", n, opened)
			}
		})
	}

	// One record_id in both cases of its hex digits is one record: the
	// batch keeps the first spelling, the ledger statement sees one id, and
	// a later delivery in the other spelling is a duplicate.
	t.Run("record_id spellings", func(t *testing.T) {
		mux := handler(true)
		rec := plugins.DeviceRecord{
			RecordID: strings.ToUpper(plugins.NewRecordID()), ObservedAt: time.Now().UTC().Add(-time.Minute), Source: "snmp",
			TargetIP:       "198.51.100.250",
			Identity:       plugins.DeviceIdentity{SerialNumber: "D3-SPELLING"},
			Classification: plugins.DeviceClassification{CIType: "server", Manufacturer: "Reticora Labs", Model: "D3-1U"},
			System:         plugins.DeviceSystem{Name: "d3-spelling"},
		}
		lower := rec
		lower.RecordID = strings.ToLower(rec.RecordID)
		code, resp := post(mux, []plugins.DeviceRecord{rec, lower})
		if code != http.StatusAccepted || resp.Created != 1 || resp.Duplicates != 1 {
			t.Fatalf("one record_id in two spellings: %d %+v; want 1 created and 1 duplicate", code, resp)
		}
		if !ledgered(lower.RecordID) {
			t.Error("record not in the ledger")
		}
		if code, resp = post(mux, []plugins.DeviceRecord{rec}); code != http.StatusAccepted || resp.Duplicates != 1 || resp.Updated+resp.Created != 0 {
			t.Errorf("redelivery in upper case: %d %+v; want a duplicate", code, resp)
		}
	})
}

// TestOverrideWaitsForIngestRecordLock covers the lock order of the
// override API against the ingest's record transaction (WP-332, NFR-03)
// against PostgreSQL. The record transaction locks the CI row first
// (LockCI) and then writes the field values; SetOverride and ClearOverride
// write a field value and, through syncCIField and the projection trigger,
// the CI row. Taken in opposite orders the two deadlock (40P01). The
// override is started while the record transaction holds the CI row and
// must wait for it before it touches a field value: the record transaction
// then writes the same field value and commits, and the override follows.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOverrideWaitsForIngestRecordLock(t *testing.T) {
	f := scopetest.Seed(t, "f0")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := discovery.NewPGRepository(f.App)
	overrides := override.NewPGRepository(f.App)
	id := f.CI(t, f.OrgA, "", "lock-order")
	if _, err := overrides.RecordDiscovered(ctx, f.OrgA, id, "model", "first", "snmp", time.Now()); err != nil {
		t.Fatal(err)
	}
	blocked := func(pid uint32) bool {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM pg_stat_activity WHERE $1::int = ANY(pg_blocking_pids(pid))`, pid).Scan(&n); err != nil {
			t.Error(err)
		}
		return n > 0
	}

	for _, tc := range []struct {
		name string
		run  func(ctx context.Context) error
		// model is the CI's model afterwards; empty: the discovered value of
		// the record transaction.
		model string
	}{
		{"set override", func(ctx context.Context) error {
			_, err := overrides.SetOverride(ctx, f.OrgA, id, "model", "manual", "", "lock order", true)
			return err
		}, "manual"},
		{"clear override", func(ctx context.Context) error {
			_, err := overrides.ClearOverride(ctx, f.OrgA, id, "model")
			return err
		}, ""},
	} {
		discovered := "discovered-" + strings.ReplaceAll(tc.name, " ", "-")
		done := make(chan error, 1)
		waited := false
		recordErr := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(txCtx context.Context) error {
			if err := repo.LockCI(txCtx, f.OrgA, id); err != nil {
				return err
			}
			var pid uint32
			if err := database.WithRequestTenant(txCtx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
				pid = tx.Conn().PgConn().PID()
				return nil
			}); err != nil {
				return err
			}
			go func() { done <- tc.run(ctx) }()
			for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline) && !waited; {
				time.Sleep(10 * time.Millisecond)
				waited = blocked(pid)
			}
			_, err := overrides.RecordDiscovered(txCtx, f.OrgA, id, "model", discovered, "snmp", time.Now())
			return err
		})
		overrideErr := <-done
		var pgErr *pgconn.PgError
		if errors.As(recordErr, &pgErr) && pgErr.Code == "40P01" || errors.As(overrideErr, &pgErr) && pgErr.Code == "40P01" {
			t.Fatalf("%s: deadlock: record transaction %v, override %v", tc.name, recordErr, overrideErr)
		}
		if recordErr != nil || overrideErr != nil || !waited {
			t.Fatalf("%s: record transaction %v, override %v, override waited for the CI lock %v", tc.name, recordErr, overrideErr, waited)
		}
		want := tc.model
		if want == "" {
			want = discovered
		}
		var model string
		if err := f.Admin.QueryRow(bg, `SELECT model FROM ci WHERE id = $1`, id).Scan(&model); err != nil || model != want {
			t.Errorf("%s: model %q, %v; want %q", tc.name, model, err, want)
		}
	}
}
