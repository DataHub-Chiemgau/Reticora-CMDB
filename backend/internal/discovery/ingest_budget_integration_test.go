package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
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

// ingestRecordBudget is the statement budget of the transaction of one new
// record (WP-332, NFR-03), BEGIN to COMMIT: BEGIN with the GUCs, identity
// candidates, deleted candidates, INSERT ci, ci_change, audit lock,
// audit_log, event_outbox, search_document, provenance visibility check,
// ci_field_value, COMMIT.
const ingestRecordBudget = 12

// productionRecordBudget is the ceiling with the production extras: the
// field resolution of the observed-value check (two reads per record, once
// per batch from WP-333 on) and the AI chunk.
const productionRecordBudget = 15

// batchBudget is the statement budget of the transaction that creates the
// new devices of a batch (WP-333, NFR-03), BEGIN to COMMIT, whatever their
// number: BEGIN with the GUCs, identity hits, deleted identity hits, INSERT
// ci, ci_change, audit_log (the chain lock is taken by its trigger),
// event_outbox, search_document, ci_field_value, COMMIT; with the production
// extras the field resolution (once per CI type) and the AI chunks.
const batchBudget = 15

// frameBudget is the ceiling of the frame statements of a batch: the frame
// transaction (BEGIN with the GUCs, ledger read, type lookup, default VRF,
// COMMIT) and the ledger transaction (BEGIN, one INSERT, COMMIT).
const frameBudget = 8

// txTrace groups the statements of a pool per transaction: a statement that
// begins a transaction opens a group on its connection, COMMIT and ROLLBACK
// close it, everything else belongs to the open group of its connection or,
// without one, is loose. SET ROLE is the connection setup of the pool.
type txTrace struct {
	mu    sync.Mutex
	open  map[*pgx.Conn][]string
	done  [][]string
	loose []string
}

func (tr *txTrace) TraceQueryStart(ctx context.Context, conn *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := strings.ToLower(strings.TrimSpace(data.SQL))
	tr.mu.Lock()
	defer tr.mu.Unlock()
	group, inTx := tr.open[conn]
	switch {
	case strings.HasPrefix(sql, "begin"):
		tr.open[conn] = []string{sql}
	case inTx && (sql == "commit" || sql == "rollback"):
		tr.done = append(tr.done, append(group, sql))
		delete(tr.open, conn)
	case inTx:
		tr.open[conn] = append(group, sql)
	case !strings.HasPrefix(sql, "set role"):
		tr.loose = append(tr.loose, sql)
	}
	return ctx
}

func (tr *txTrace) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// take returns the finished transactions and the loose statements since the
// last call.
func (tr *txTrace) take() (txs [][]string, loose []string) {
	tr.mu.Lock()
	defer tr.mu.Unlock()
	txs, loose = tr.done, tr.loose
	tr.done, tr.loose = nil, nil
	return txs, loose
}

// count returns the statements of tx that match pattern (a regular
// expression on the lower-cased SQL).
func count(tx []string, pattern string) int {
	re := regexp.MustCompile(pattern)
	n := 0
	for _, sql := range tx {
		if re.MatchString(sql) {
			n++
		}
	}
	return n
}

// Statement patterns of the ingest.
const (
	insertCI      = `insert into ci \(`
	readsCITypes  = `(from|join)\s+ci_type\b`
	insertsLedger = `insert into ingest_record`
)

// manyProvenance is the server's override adapter (report_adapters.go): the
// fields of a new CI are recorded in one statement, those of the new CIs of
// a batch too.
type manyProvenance struct{ provenance }

func (p manyProvenance) RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error {
	return p.repo.RecordDiscoveredMany(ctx, orgID, ciID, values, source, observedAt)
}

func (p manyProvenance) RecordDiscoveredBatch(ctx context.Context, orgID string, batch []override.DiscoveredValues) error {
	return p.repo.RecordDiscoveredBatch(ctx, orgID, batch)
}

// noBatch hides the batch creation of a CI repository: the ingest runs
// every record in its own transaction, as before WP-333 and as for every
// record the batch leaves.
type noBatch struct{ ci.Repository }

func (r noBatch) GetByIDs(ctx context.Context, orgID string, ids []string) ([]ci.Item, error) {
	return r.Repository.(ci.BatchReader).GetByIDs(ctx, orgID, ids)
}

// UpdateRecord keeps the record update of the repository (WP-489): a known
// device is updated as one change of its record on both paths.
func (r noBatch) UpdateRecord(ctx context.Context, orgID, id string, rec *ci.RecordUpdate) (*ci.RecordUpdateResult, error) {
	return ci.UpdateRecord(ctx, r.Repository, orgID, id, rec)
}

// ingestBatch is the outcome of one ingest: the response and the record and
// frame transactions it sent.
type ingestBatch struct {
	resp    discovery.BulkIngestResponse
	records [][]string
	frame   [][]string
	loose   []string
}

// frameStatements is the number of statements outside the record
// transactions.
func (b *ingestBatch) frameStatements() int {
	n := len(b.loose)
	for _, tx := range b.frame {
		n += len(tx)
	}
	return n
}

// checkLockedTail asserts that the statements under the organization's
// audit chain and outbox locks come last (WP-334): from the first of them
// only audit entries, events and the commit follow.
func checkLockedTail(t *testing.T, tx []string) {
	t.Helper()
	locked := regexp.MustCompile(`insert into audit_log|insert into event_outbox|pg_advisory_xact_lock\(\$1, \$2\)|^commit$`)
	first := -1
	for i, sql := range tx {
		if locked.MatchString(sql) && sql != "commit" {
			first = i
			break
		}
	}
	if first < 0 {
		return
	}
	for _, sql := range tx[first:] {
		if !locked.MatchString(sql) {
			t.Errorf("%q runs under the audit or outbox lock; they must come last: %q", sql, tx[first:])
		}
	}
}

// TestIngestStatementBudget covers WP-332 and WP-333 (NFR-03) against
// PostgreSQL with the write path of the server (audit, outbox, search
// index, provenance in one statement) and a batch of 20 new devices like
// the load test's:
//   - record by record (WP-332, the path of every record the batch leaves):
//     every record runs in exactly one transaction of at most 12
//     statements, which holds identity candidates, creation, provenance and
//     search index; the frame of the batch (ledger, one type lookup, VRF,
//     ledger write) costs the same statements for 5 records as for 20;
//   - batch (WP-333): the 20 new devices are created in one transaction of
//     at most 15 statements, also with the production extras, and no other
//     transaction writes them; the audit chain stays intact and holds their
//     entries consecutively in record order;
//   - known devices (WP-333): 20 records of existing CIs read their
//     candidates with one batch query and update one by one.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestStatementBudget(t *testing.T) {
	f := scopetest.Seed(t, "f0")
	bg := context.Background()
	trace := &txTrace{open: map[*pgx.Conn][]string{}}
	cfg := f.App.Config()
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	// Every repository on the counting pool: a join runs on the
	// transaction's connection whatever pool the callee holds.
	build := func(production, batch bool, lanes ...int) *discovery.Handler {
		var indexer ci.Indexer = search.NewCIIndexer(search.NewPGRepository(pool))
		cis := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(pool, audit.NewPGRecorder()).WithEvents(events.NewOutbox()), indexer)
		if production {
			cis = ci.NewIndexingRepository(cis, ai.NewCIChunkIndexer(ai.NewPGRepository(pool), nil))
		}
		if !batch {
			cis = noBatch{cis}
		}
		h := discovery.NewHandler(discovery.NewPGRepository(pool), cis).
			WithProvenance(manyProvenance{provenance{repo: override.NewPGRepository(pool)}})
		if len(lanes) > 0 {
			h.WithIngestLanes(lanes[0])
		}
		if production {
			types := citype.NewPGRepository(pool)
			h.WithInstanceFields(types).WithObservedValidator(ci.NewService(cis).WithFieldResolver(types))
		}
		return h
	}
	handler := func(production, batch bool, lanes ...int) http.Handler {
		mux := chi.NewRouter()
		build(production, batch, lanes...).RegisterRoutes(mux)
		return mux
	}
	seq := 0
	devices := func(n int) []plugins.DeviceRecord {
		records := make([]plugins.DeviceRecord, n)
		for i := range records {
			seq++
			serial := fmt.Sprintf("BUDGET-%04d", seq)
			records[i] = plugins.DeviceRecord{
				RecordID: plugins.NewRecordID(), ObservedAt: time.Now().UTC().Add(-time.Second), Source: "snmp",
				TargetIP:       fmt.Sprintf("10.240.%d.%d", seq/256, seq%256),
				Identity:       plugins.DeviceIdentity{SerialNumber: serial, Hostname: strings.ToLower(serial)},
				Classification: plugins.DeviceClassification{CIType: "server", Manufacturer: "Reticora Labs", Model: "L-1U"},
				System:         plugins.DeviceSystem{Name: strings.ToLower(serial)},
			}
		}
		return records
	}
	var last []plugins.DeviceRecord
	post := func(t *testing.T, mux http.Handler, records []plugins.DeviceRecord) ingestBatch {
		t.Helper()
		last = records
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
		if err != nil {
			t.Fatal(err)
		}
		trace.take()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
		var b ingestBatch
		if err = json.Unmarshal(w.Body.Bytes(), &b.resp); err != nil {
			t.Fatal(err)
		}
		var txs [][]string
		txs, b.loose = trace.take()
		for _, tx := range txs {
			if count(tx, insertCI) > 0 {
				b.records = append(b.records, tx)
			} else {
				b.frame = append(b.frame, tx)
			}
		}
		return b
	}
	ingest := func(t *testing.T, mux http.Handler, n int) ingestBatch {
		t.Helper()
		b := post(t, mux, devices(n))
		if b.resp.Created != n {
			t.Fatalf("ingest of %d new devices: %+v", n, b.resp)
		}
		return b
	}
	// checkRecords asserts one transaction per new record with everything of
	// the record in it, and returns the largest one.
	checkRecords := func(t *testing.T, b *ingestBatch, n int) int {
		t.Helper()
		if len(b.records) != n {
			t.Errorf("%d record transactions for %d new records, want one each", len(b.records), n)
		}
		largest := 0
		for _, tx := range b.records {
			largest = max(largest, len(tx))
			for pattern, want := range map[string]int{
				insertCI:                        1,
				`deleted_at is null and`:        1, // identity candidates
				`deleted_at is not null and`:    1, // deleted candidates (CI-13)
				`insert into ci_change`:         1,
				`insert into audit_log`:         1,
				`insert into event_outbox`:      1,
				`insert into search_document`:   1,
				`insert into ci_field_value`:    1,
				readsCITypes:                    0,
				insertsLedger:                   0,
				`from relationship_suppression`: 0,
			} {
				if got := count(tx, pattern); got != want {
					t.Errorf("record transaction has %d statements matching %q, want %d: %q", got, pattern, want, tx)
				}
			}
			if tx[len(tx)-1] != "commit" {
				t.Errorf("record transaction ends with %q", tx[len(tx)-1])
			}
			checkLockedTail(t, tx)
		}
		return largest
	}

	t.Run("record transaction", func(t *testing.T) {
		mux := handler(false, false)
		ingest(t, mux, 1) // warm-up: the pool connects
		b := ingest(t, mux, 20)
		largest := checkRecords(t, &b, 20)
		if largest > ingestRecordBudget {
			t.Errorf("record transaction with %d statements, budget %d: %q", largest, ingestRecordBudget, b.records)
		}
		if len(b.loose) != 0 {
			t.Errorf("statements outside a transaction: %q", b.loose)
		}
		if len(b.frame) != 2 || b.frameStatements() > frameBudget {
			t.Errorf("frame: %d transactions with %d statements, want 2 with at most %d: %q",
				len(b.frame), b.frameStatements(), frameBudget, b.frame)
		}
		all := append(append([][]string{}, b.frame...), b.records...)
		types, ledger := 0, 0
		for _, tx := range all {
			types += count(tx, readsCITypes)
			ledger += count(tx, insertsLedger)
		}
		if types != 1 || ledger != 1 {
			t.Errorf("batch read the CI types %d times and wrote the ledger %d times, want once each", types, ledger)
		}
		recordStatements := 0
		for _, tx := range b.records {
			recordStatements += len(tx)
		}
		t.Logf("batch of 20 new records: %d statements per record transaction (largest %d), frame %d statements in %d transactions; "+
			"amortized %.2f statements and %.2f transactions per record",
			recordStatements/len(b.records), largest, b.frameStatements(), len(b.frame),
			float64(recordStatements+b.frameStatements())/20, float64(len(b.records)+len(b.frame))/20)

		// The frame does not grow with the batch (no N+1 per record).
		small := ingest(t, mux, 5)
		checkRecords(t, &small, 5)
		if small.frameStatements() != b.frameStatements() {
			t.Errorf("frame of 5 records %d statements, of 20 records %d (N+1)", small.frameStatements(), b.frameStatements())
		}
	})

	t.Run("production", func(t *testing.T) {
		mux := handler(true, false)
		ingest(t, mux, 1)
		b := ingest(t, mux, 20)
		largest := checkRecords(t, &b, 20)
		for _, tx := range b.records {
			if count(tx, `ai_chunk`) != 1 {
				t.Errorf("record transaction without the AI chunk: %q", tx)
			}
		}
		if largest > productionRecordBudget {
			t.Errorf("record transaction with %d statements, ceiling %d: %q", largest, productionRecordBudget, b.records)
		}
		t.Logf("production wiring: %d statements per record transaction, frame %d statements", largest, b.frameStatements())
	})

	// writes are the tables the creation of a CI writes.
	writes := []string{insertCI, `insert into ci_change`, `insert into audit_log`, `insert into event_outbox`,
		`insert into search_document`, `insert into ci_field_value`, `insert into ai_chunk`}
	// checkBatch asserts one transaction for the n new devices of b with
	// everything of them in it, and returns its statements.
	checkBatch := func(t *testing.T, b *ingestBatch, n int, production bool) []string {
		t.Helper()
		if len(b.records) != 1 {
			t.Fatalf("%d transactions create the %d new devices, want one: %q", len(b.records), n, b.records)
		}
		tx := b.records[0]
		aiChunks := 0
		if production {
			aiChunks = 1
		}
		for pattern, want := range map[string]int{
			insertCI:                        1,
			`ingest identity hits`:          1,
			`ingest deleted identity hits`:  1,
			`order by created_at, id limit`: 0, // the identity query of one record
			`insert into ci_change`:         1,
			`insert into audit_log`:         1,
			`insert into event_outbox`:      1,
			`insert into search_document`:   1,
			`insert into ci_field_value`:    1,
			`insert into ai_chunk`:          aiChunks,
			`pg_advisory_xact_lock`:         0, // the chain trigger takes the audit lock
			readsCITypes:                    0,
			insertsLedger:                   0,
		} {
			if got := count(tx, pattern); got != want {
				t.Errorf("batch transaction has %d statements matching %q, want %d: %q", got, pattern, want, tx)
			}
		}
		if len(tx) > batchBudget {
			t.Errorf("batch transaction with %d statements for %d new devices, budget %d: %q", len(tx), n, batchBudget, tx)
		}
		if tx[len(tx)-1] != "commit" {
			t.Errorf("batch transaction ends with %q", tx[len(tx)-1])
		}
		checkLockedTail(t, tx)
		for _, other := range b.frame {
			for _, pattern := range writes {
				if count(other, pattern) > 0 {
					t.Errorf("a transaction besides the batch writes %q: %q", pattern, other)
				}
			}
		}
		if len(b.loose) != 0 {
			t.Errorf("statements outside a transaction: %q", b.loose)
		}
		return tx
	}
	// checkChain asserts the intact audit chain with the entries of the last
	// records consecutive in record order.
	checkChain := func(t *testing.T) {
		t.Helper()
		result, err := audit.Verify(bg, f.App, f.OrgA)
		if err != nil || !result.Intact {
			t.Fatalf("audit chain after the batch: %+v, %v", result, err)
		}
		serials := make([]string, len(last))
		for i := range last {
			serials[i] = last[i].Identity.SerialNumber
		}
		rows, err := f.Admin.Query(bg, `SELECT c.serial_number, a.seq FROM audit_log a JOIN ci c ON c.id = a.resource_id
			WHERE a.organization_id = $1 AND a.action = 'ci.created' AND c.serial_number = ANY($2) ORDER BY a.seq`, f.OrgA, serials)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		i, prev := 0, int64(0)
		for ; rows.Next(); i++ {
			var serial string
			var seq int64
			if err := rows.Scan(&serial, &seq); err != nil {
				t.Fatal(err)
			}
			if i >= len(serials) || serial != serials[i] || (i > 0 && seq != prev+1) {
				t.Errorf("audit entry %d: %s at seq %d after seq %d, want %v in record order and consecutive", i, serial, seq, prev, serials)
			}
			prev = seq
		}
		if rows.Err() != nil || i != len(serials) {
			t.Errorf("%d audit entries for %d new devices: %v", i, len(serials), rows.Err())
		}
	}

	t.Run("batch", func(t *testing.T) {
		mux := handler(false, true)
		ingest(t, mux, 1)
		b := ingest(t, mux, 20)
		tx := checkBatch(t, &b, 20, false)
		checkChain(t)
		small := ingest(t, mux, 5)
		if smallTx := checkBatch(t, &small, 5, false); len(smallTx) != len(tx) {
			t.Errorf("batch transaction of 5 new devices %d statements, of 20 %d (N+1)", len(smallTx), len(tx))
		}
		t.Logf("batch of 20 new devices: %d statements in one transaction, frame %d statements in %d transactions; "+
			"amortized %.2f statements per record", len(tx), b.frameStatements(), len(b.frame), float64(len(tx)+b.frameStatements())/20)
	})

	t.Run("batch production", func(t *testing.T) {
		mux := handler(true, true)
		ingest(t, mux, 1)
		b := ingest(t, mux, 20)
		tx := checkBatch(t, &b, 20, true)
		checkChain(t)
		heads := make([]string, len(tx))
		for i, sql := range tx {
			heads[i] = strings.Join(strings.Fields(sql), " ")
			if len(heads[i]) > 48 {
				heads[i] = heads[i][:48]
			}
		}
		t.Logf("production wiring: 20 new devices in one transaction of %d statements, frame %d statements: %q",
			len(tx), b.frameStatements(), heads)
	})

	t.Run("known devices", func(t *testing.T) {
		mux := handler(true, true)
		ingest(t, mux, 1)
		created := ingest(t, mux, 20)
		checkBatch(t, &created, 20, true)
		again := make([]plugins.DeviceRecord, len(last))
		for i, rec := range last {
			rec.RecordID, rec.ObservedAt = plugins.NewRecordID(), time.Now().UTC().Add(-time.Second)
			rec.Classification.Model = "L-2U"
			again[i] = rec
		}
		b := post(t, mux, again)
		if b.resp.Updated != 20 || b.resp.Created != 0 {
			t.Fatalf("ingest of 20 known devices: %+v", b.resp)
		}
		hits, single, updates := 0, 0, 0
		for _, tx := range append(append([][]string{}, b.frame...), b.records...) {
			hits += count(tx, `ingest identity hits`)
			single += count(tx, `order by created_at, id limit`)
			if count(tx, `^update ci set`) > 0 {
				updates++
			}
		}
		if hits != 1 || single != 0 || updates != 20 || len(b.records) != 0 {
			t.Errorf("20 known devices: %d batch candidate queries, %d record candidate queries, %d update transactions, %d creating transactions; "+
				"want 1, 0, 20, 0", hits, single, updates, len(b.records))
		}
		var models int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND model = 'L-2U'`, f.OrgA).Scan(&models); err != nil || models != 20 {
			t.Errorf("%d CIs updated to the new model, want 20 (%v)", models, err)
		}
	})

	// With lanes (WP-334) every record and batch transaction first takes
	// the identity locks of its records: one statement more, right after
	// BEGIN. With one lane there is none (the subtests above).
	t.Run("lanes", func(t *testing.T) {
		lock := regexp.MustCompile(`(?s)^with prev as materialized .*pg_advisory_xact_lock_shared\(s\.c, s\.k\)`)
		locks := func(tx []string) int {
			n := 0
			for _, sql := range tx {
				if lock.MatchString(sql) {
					n++
				}
			}
			return n
		}
		mux := handler(false, true, 4)
		ingest(t, mux, 1)
		b := ingest(t, mux, 20)
		// One batch per lane of the 20 devices.
		lanes := map[int]bool{}
		for i := range last {
			lanes[discovery.IngestLane(&discovery.IngestItem{SerialNumber: last[i].Identity.SerialNumber}, 4)] = true
		}
		if len(b.records) != len(lanes) {
			t.Fatalf("%d transactions create the 20 new devices of %d lanes, want one batch per lane: %q", len(b.records), len(lanes), b.records)
		}
		var tx []string
		for _, batch := range b.records {
			if locks(batch) != 1 || !lock.MatchString(batch[1]) || len(batch) > batchBudget+1 {
				t.Errorf("batch transaction with lanes: %d statements, the identity locks %d times (want once, first): %q",
					len(batch), locks(batch), batch)
			}
			if len(batch) > len(tx) {
				tx = batch
			}
		}
		for _, other := range b.frame {
			if locks(other) != 0 {
				t.Errorf("a frame transaction takes identity locks: %q", other)
			}
		}
		perRecord := handler(false, false, 4)
		ingest(t, perRecord, 1)
		r := ingest(t, perRecord, 20)
		largest := checkRecords(t, &r, 20)
		for _, tx := range r.records {
			if locks(tx) != 1 || !lock.MatchString(tx[1]) {
				t.Errorf("record transaction with lanes takes the identity locks %d times (want once, first): %q", locks(tx), tx)
			}
		}
		if largest > ingestRecordBudget+1 {
			t.Errorf("record transaction with lanes: %d statements, budget %d plus the lock", largest, ingestRecordBudget)
		}
		t.Logf("with 4 lanes: 20 new devices in %d batch transactions (one per lane) of at most %d statements, a new record in %d statements",
			len(b.records), len(tx), largest)
	})

	// Records consumed from the ingest queue take the identity locks with
	// one lane too (holistic review of WP-334): other instances, the former
	// consumer and drain lanes may process the organization at the same
	// time. The same one statement more as with lanes; the HTTP ingest
	// without a queue (the subtests above) takes none.
	t.Run("queue with one lane", func(t *testing.T) {
		lock := regexp.MustCompile(`(?s)^with prev as materialized .*pg_advisory_xact_lock_shared\(s\.c, s\.k\)`)
		consume := func(t *testing.T, h *discovery.Handler, n int) ingestBatch {
			t.Helper()
			records := devices(n)
			trace.take()
			for i, result := range discovery.ConsumeRecords(ctx, h, f.OrgA, records...) {
				if result != "ack" {
					t.Fatalf("record %d: delivery %s", i, result)
				}
			}
			var b ingestBatch
			var txs [][]string
			txs, b.loose = trace.take()
			for _, tx := range txs {
				if count(tx, insertCI) > 0 {
					b.records = append(b.records, tx)
				} else {
					b.frame = append(b.frame, tx)
				}
			}
			return b
		}
		batched := build(false, true, 1)
		consume(t, batched, 1)
		b := consume(t, batched, 20)
		if len(b.records) != 1 {
			t.Fatalf("%d transactions create the 20 new devices of one lane, want one batch: %q", len(b.records), b.records)
		}
		if tx := b.records[0]; count(tx, lock.String()) != 1 || !lock.MatchString(tx[1]) || len(tx) > batchBudget+1 {
			t.Errorf("batch transaction from the queue with one lane: %d statements, want the identity locks once, first, within %d: %q",
				len(tx), batchBudget+1, tx)
		}
		perRecord := build(false, false, 1)
		consume(t, perRecord, 1)
		r := consume(t, perRecord, 20)
		largest := checkRecords(t, &r, 20)
		for _, tx := range r.records {
			if !lock.MatchString(tx[1]) {
				t.Errorf("record transaction from the queue with one lane does not take the identity locks first: %q", tx)
			}
		}
		if largest > ingestRecordBudget+1 {
			t.Errorf("record transaction from the queue with one lane: %d statements, budget %d plus the lock", largest, ingestRecordBudget)
		}
		t.Logf("from the queue with one lane: 20 new devices in one batch transaction of %d statements, a new record in %d statements",
			len(b.records[0]), largest)
	})

	// Two records of one device in a window: the first is created in the
	// batch, the second follows in its own transaction with a fresh query
	// and updates the CI the batch created.
	t.Run("same device twice", func(t *testing.T) {
		mux := handler(false, true)
		ingest(t, mux, 1)
		records := devices(5)
		again := records[2]
		again.RecordID, again.ObservedAt = plugins.NewRecordID(), records[2].ObservedAt.Add(500*time.Millisecond)
		again.Classification.Model = "L-3U"
		b := post(t, mux, append(records, again))
		if b.resp.Created != 5 || b.resp.Updated != 1 {
			t.Fatalf("ingest of 5 devices, one of them twice: %+v", b.resp)
		}
		if len(b.records) != 1 || count(b.records[0], insertCI) != 1 || len(b.records[0]) > batchBudget {
			t.Errorf("%d transactions create the devices, want one batch within the budget: %q", len(b.records), b.records)
		}
		followers := 0
		for _, tx := range b.frame {
			if count(tx, `^update ci set`) == 0 {
				continue
			}
			followers++
			if count(tx, `order by created_at, id limit`) != 1 {
				t.Errorf("the second record of the device reads its candidates %d times, want once on its own: %q",
					count(tx, `order by created_at, id limit`), tx)
			}
		}
		var n int
		var model string
		if err := f.Admin.QueryRow(bg, `SELECT count(*), max(model) FROM ci WHERE organization_id = $1 AND serial_number = $2`,
			f.OrgA, again.Identity.SerialNumber).Scan(&n, &model); err != nil || followers != 1 || n != 1 || model != "L-3U" {
			t.Errorf("%d update transactions, %d CIs for the device with model %q (%v); want 1, 1, L-3U", followers, n, model, err)
		}
		if result, err := audit.Verify(bg, f.App, f.OrgA); err != nil || !result.Intact {
			t.Errorf("audit chain: %+v, %v", result, err)
		}
	})
}

// TestIngestBatchMatchesRecordPath covers WP-333 (NFR-03) against
// PostgreSQL with the write path of the server (audit, outbox, search and AI
// index, provenance adapter, observed-value check, interfaces): the same
// new devices ingested record by record (organization A) and in a batch
// (organization B) leave identical CIs, history, audit entries, events,
// provenance, search documents, AI chunks and interfaces.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestBatchMatchesRecordPath(t *testing.T) {
	f := scopetest.Seed(t, "e6")
	bg := context.Background()
	handler := func(batch bool) http.Handler {
		cis := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()),
			search.NewCIIndexer(search.NewPGRepository(f.App)))
		cis = ci.NewIndexingRepository(cis, ai.NewCIChunkIndexer(ai.NewPGRepository(f.App), nil))
		if !batch {
			cis = noBatch{cis}
		}
		types := citype.NewPGRepository(f.App)
		h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis).
			WithProvenance(manyProvenance{provenance{repo: override.NewPGRepository(f.App)}}).
			WithInterfaces(ipam.NewPGRepository(f.App)).
			WithInstanceFields(types).WithObservedValidator(ci.NewService(cis).WithFieldResolver(types))
		mux := chi.NewRouter()
		h.RegisterRoutes(mux)
		return mux
	}
	observed := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	records := func() []plugins.DeviceRecord {
		out := make([]plugins.DeviceRecord, 4)
		for i := range out {
			serial := fmt.Sprintf("EQ-%d", i)
			out[i] = plugins.DeviceRecord{
				RecordID: plugins.NewRecordID(), ObservedAt: observed, Source: "snmp", TargetIP: fmt.Sprintf("203.0.113.%d", 10+i),
				Identity: plugins.DeviceIdentity{SerialNumber: serial, HardwareUUID: fmt.Sprintf("4c4c4544-0000-4000-8000-0000000e600%d", i),
					PrimaryMAC: fmt.Sprintf("00:1b:21:e6:00:0%d", i), Hostname: strings.ToLower(serial), FQDN: strings.ToLower(serial) + ".example.test"},
				Classification: plugins.DeviceClassification{CIType: "server", Manufacturer: "Reticora Labs", Model: "L-1U", SysObjectID: "1.3.6.1.4.1.9.1.1"},
				System:         plugins.DeviceSystem{Name: strings.ToLower(serial), Description: "Reticora " + serial, OSName: "Linux", OSVersion: "6.1"},
				Attributes:     map[string]any{"rack": "R3", "role": "database"},
				Interfaces: []plugins.DeviceInterface{{Name: "eth0", MAC: fmt.Sprintf("00:1b:21:e6:01:0%d", i),
					IPs: []plugins.DeviceIP{{Address: fmt.Sprintf("203.0.113.%d", 10+i), PrefixLength: 24}}}},
			}
		}
		return out
	}
	ingest := func(orgID string, mux http.Handler) {
		t.Helper()
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records()})
		if err != nil {
			t.Fatal(err)
		}
		ctx := tenant.WithTenant(f.OrgCtx(orgID), tenant.TenantInfo{OrganizationID: orgID, UserID: f.User})
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		var resp discovery.BulkIngestResponse
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &resp) != nil || resp.Created != 4 {
			t.Fatalf("ingest into %s: %d %s", orgID, w.Code, w.Body.String())
		}
	}
	ingest(f.OrgA, handler(false))
	ingest(f.OrgB, handler(true))
	// The batch created the four CIs in one transaction (one created_at),
	// the record path in four.
	for org, want := range map[string]int{f.OrgA: 4, f.OrgB: 1} {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(DISTINCT created_at) FROM ci WHERE organization_id = $1`, org).Scan(&n); err != nil || n != want {
			t.Fatalf("%s: CIs created in %d transactions, want %d (%v)", org, n, want, err)
		}
	}

	for i := range 4 {
		serial := fmt.Sprintf("EQ-%d", i)
		var single, batch string
		for org, id := range map[string]*string{f.OrgA: &single, f.OrgB: &batch} {
			if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = $2`, org, serial).Scan(id); err != nil {
				t.Fatalf("%s in %s: %v", serial, org, err)
			}
		}
		want, got := ingested(t, f, f.OrgA, single), ingested(t, f, f.OrgB, batch)
		for table := range want {
			if !reflect.DeepEqual(got[table], want[table]) {
				w, _ := json.Marshal(want[table])
				g, _ := json.Marshal(got[table])
				t.Errorf("%s: %s of the batch differs from the record path:\n batch  %s\n record %s", serial, table, g, w)
			}
		}
	}
	for _, org := range []string{f.OrgA, f.OrgB} {
		if result, err := audit.Verify(bg, f.App, org); err != nil || !result.Intact {
			t.Errorf("audit chain of %s: %+v, %v", org, result, err)
		}
	}
}

// ingestVarying are the keys whose values differ between two ingests of
// the same device: ids, organizations, change sets and record ids (AUD-03),
// times and the hashes over them.
var ingestVarying = map[string]bool{
	"id": true, "organization_id": true, "ci_id": true, "entity_id": true, "resource_id": true, "change_set_id": true,
	"created_at": true, "updated_at": true, "first_seen_at": true, "last_seen_at": true, "discovered_at": true,
	"timestamp": true, "occurred_at": true, "hash": true, "previous_hash": true, "record_id": true,
	// The record id is the correlation id of the change (AUD-03, WP-489).
	"correlation_id": true,
}

// ingested reads what the ingest of a new device wrote, per table, with
// ids and times replaced.
func ingested(t *testing.T, f *scopetest.Fixture, orgID, ciID string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for table, query := range map[string]string{
		"ci":                `SELECT jsonb_agg(to_jsonb(x)) FROM ci x WHERE x.id = $1`,
		"ci_change":         `SELECT jsonb_agg(to_jsonb(x) ORDER BY x.change_type, x.field_name) FROM ci_change x WHERE x.ci_id = $1`,
		"audit_log":         `SELECT jsonb_agg(to_jsonb(x) - 'seq' ORDER BY x.seq) FROM audit_log x WHERE x.resource_id = $1 AND x.action LIKE 'ci.%'`,
		"event_outbox":      `SELECT jsonb_agg(to_jsonb(x) - 'sequence' - 'published_at' - 'attempts' - 'last_error' ORDER BY x.sequence) FROM event_outbox x WHERE x.entity_id = $1::text`,
		"ci_field_value":    `SELECT jsonb_agg(to_jsonb(x) ORDER BY x.field_name) FROM ci_field_value x WHERE x.ci_id = $1`,
		"search_document":   `SELECT jsonb_agg(to_jsonb(x)) FROM search_document x WHERE x.entity_id = $1`,
		"ai_chunk":          `SELECT jsonb_agg(to_jsonb(x)) FROM ai_chunk x WHERE x.entity_id = $1`,
		"network_interface": `SELECT jsonb_agg(to_jsonb(x) ORDER BY x.name) FROM network_interface x WHERE x.ci_id = $1`,
	} {
		var raw []byte
		if err := f.Admin.QueryRow(context.Background(), query, ciID).Scan(&raw); err != nil {
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
		out[table] = withoutVarying(v, ciID, orgID)
	}
	return out
}

// withoutVarying replaces the values of the varying keys and the given ids
// inside strings by placeholders.
func withoutVarying(v any, ids ...string) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, val := range x {
			if ingestVarying[k] && val != nil {
				out[k] = "<" + k + ">"
				continue
			}
			out[k] = withoutVarying(val, ids...)
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = withoutVarying(x[i], ids...)
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

// rediscoverBudget is the statement budget of one re-discovered device on
// the queue path (WP-608, NFR-03, F-118), amortized over a fetch of about
// 100 records in four lanes: every statement the ingest sends (frame, the
// batch transaction of each window, the records the window leaves, the
// topology and the ledger), BEGIN with the tenant GUCs counted once. The
// window updates the known devices under its identity locks: per device
// the observed-value check (3), the row lock, the instance fields, the
// update with its reads (4), history, interfaces and addresses (5) and
// audit and outbox under their locks (3); the changed devices of a window
// are indexed together after its commit (WP-489), a sighting is not
// indexed. About 20.7 on the first re-discovery of the load test (eth1
// new), 16.6 after it. Before
// WP-608 every known device had a transaction of its own that took its
// identity locks and read its candidates again, and its neighbor and its
// edge three transactions more: 36 statements per device.
const rediscoverBudget = 22

// TestRediscoverStatementBudget covers WP-608 (NFR-03, F-118) against
// PostgreSQL with the write path of the server (audit, outbox, search index,
// provenance, observed-value check, instance fields, interfaces and
// topology), four lanes and the ingest locks of the queue: 100 servers like
// the seed of the load test (serial number, management IP, eth0 with its
// address, an LLDP edge to one of two switches) are re-discovered twice with
// the records of tests/load/ingest.js SCENARIO=rediscover (chassis MAC,
// eth0 and a new eth1, LLDP neighbor), one device twice in the first fetch:
//   - at most rediscoverBudget statements per record, the known devices
//     updated in the batch transaction of their window (one per lane), which
//     takes the identity locks of its records first and writes audit and
//     outbox last under their locks (E-56);
//   - the neighbor looked up once per fetch and lane, the edges read and
//     re-confirmed with one statement each;
//   - the result equals the path of one transaction per record (organization
//     B, the CI repository without batch creation): CI, history, audit
//     entries, events, search document, interfaces, addresses, identity MACs
//     and edges per device, the audit chains intact, no new CI;
//   - the address statements of the interface sync read ip_address through
//     idx_ip_address_interface (migration 000165) as reticora_app.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRediscoverStatementBudget(t *testing.T) {
	f := scopetest.Seed(t, "f0")
	bg := context.Background()
	const servers = 100
	for _, org := range []string{f.OrgA, f.OrgB} {
		seedKnownServers(t, f, org, servers)
	}
	type device struct{ serial, ip, mac, name, id, ifID string }
	devices := func(org string) map[int]device {
		rows, err := f.Admin.Query(bg, `SELECT i, c.serial_number, host(c.management_ip), n.mac::text, c.name, c.id::text, n.id::text
			FROM generate_series(1, $2::int) AS i JOIN ci c ON c.id = md5($1 || '/ci/' || i)::uuid
			JOIN network_interface n ON n.ci_id = c.id AND n.name = 'eth0'`, org, servers+2)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[int]device{}
		for rows.Next() {
			var i int
			var d device
			if err := rows.Scan(&i, &d.serial, &d.ip, &d.mac, &d.name, &d.id, &d.ifID); err != nil {
				t.Fatal(err)
			}
			out[i] = d
		}
		if rows.Err() != nil || len(out) != servers+2 {
			t.Fatalf("%d seeded devices of %s: %v", len(out), org, rows.Err())
		}
		return out
	}
	seeded := map[string]map[int]device{f.OrgA: devices(f.OrgA), f.OrgB: devices(f.OrgB)}
	// The record of ingest.js rediscoverRecord for device n of the seed.
	hex := func(prefix string, n int) string { return fmt.Sprintf("%s%02x:%02x", prefix, (n>>8)&255, n&255) }
	record := func(org string, n int, observed time.Time) plugins.DeviceRecord {
		d, sw := seeded[org][n], seeded[org][1+n%2]
		return plugins.DeviceRecord{
			RecordID: plugins.NewRecordID(), ObservedAt: observed, Source: "snmp", TargetIP: d.ip,
			Identity: plugins.DeviceIdentity{SerialNumber: d.serial, PrimaryMAC: hex("ac:1f:6b:00:", n), Hostname: d.name},
			Classification: plugins.DeviceClassification{CIType: "server", Manufacturer: "Reticora Labs", Model: "R-1U",
				SysObjectID: "1.3.6.1.4.1.99999.1.1"},
			System: plugins.DeviceSystem{Name: d.name, Description: "Reticora Labs R-1U"},
			Interfaces: []plugins.DeviceInterface{
				{Name: "eth0", IfIndex: 1, MAC: d.mac, SpeedMbps: 1000, AdminStatus: "up", OperStatus: "up",
					IPs: []plugins.DeviceIP{{Address: d.ip, PrefixLength: 16}}},
				{Name: "eth1", IfIndex: 2, MAC: hex("ac:1f:6b:01:", n), SpeedMbps: 1000, AdminStatus: "up", OperStatus: "up"},
			},
			Neighbors: []plugins.DeviceNeighbor{{Protocol: "lldp", LocalInterface: "eth0", RemoteMAC: hex("ac:1f:6b:00:", 1+n%2),
				RemoteIP: sw.ip, RemoteHostname: sw.name, RemoteInterface: fmt.Sprintf("Gi1/0/%d", n%48+1)}},
		}
	}

	trace := &txTrace{open: map[*pgx.Conn][]string{}}
	cfg := f.App.Config()
	cfg.ConnConfig.Tracer = trace
	cfg.MinConns = 0
	pool, err := pgxpool.NewWithConfig(bg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	// The handler of the server (internal/server/router.go) with four lanes;
	// without batch creation every record runs in a transaction of its own.
	build := func(pool *pgxpool.Pool, batch bool) *discovery.Handler {
		cis := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(pool, audit.NewPGRecorder()).WithEvents(events.NewOutbox()),
			search.NewCIIndexer(search.NewPGRepository(pool)))
		if !batch {
			cis = noBatch{cis}
		}
		types := citype.NewPGRepository(pool)
		return discovery.NewHandler(discovery.NewPGRepository(pool), cis, relationship.NewPGRepository(pool).WithEvents(events.NewOutbox())).
			WithProvenance(manyProvenance{provenance{repo: override.NewPGRepository(pool)}}).
			WithInstanceFields(types).
			WithObservedValidator(ci.NewService(cis).WithFieldResolver(types)).
			WithInterfaces(ipam.NewPGRepository(pool)).
			WithIngestLanes(4)
	}
	handlers := map[string]*discovery.Handler{f.OrgA: build(pool, true), f.OrgB: build(f.App, false)}
	consume := func(t *testing.T, org string, records []plugins.DeviceRecord) {
		t.Helper()
		ctx := tenant.WithTenant(f.OrgCtx(org), tenant.TenantInfo{OrganizationID: org, UserID: f.User})
		for i, result := range discovery.ConsumeRecords(ctx, handlers[org], org, records...) {
			if result != "ack" {
				t.Fatalf("%s: record %d: delivery %s", org, i, result)
			}
		}
	}
	lock := regexp.MustCompile(`(?s)^with prev as materialized .*pg_advisory_xact_lock_shared\(s\.c, s\.k\)`)
	edges := func(org string, since time.Time) (n, confirmed int) {
		if err := f.Admin.QueryRow(bg, `SELECT count(*), count(*) FILTER (WHERE last_confirmed_at >= $2) FROM ci_relationship
			WHERE organization_id = $1`, org, since).Scan(&n, &confirmed); err != nil {
			t.Fatal(err)
		}
		return n, confirmed
	}

	var addressStatements []string
	for pass := 1; pass <= 2; pass++ {
		t.Run(fmt.Sprintf("re-discovery %d", pass), func(t *testing.T) {
			// The records are observed at the start of the pass: a sighting
			// sets last_seen_at to the record's observed_at (GLO-12, WP-580).
			start := time.Now().UTC().Truncate(time.Second)
			observed := start
			for _, org := range []string{f.OrgA, f.OrgB} {
				var records []plugins.DeviceRecord
				for n := 3; n <= servers+2; n++ {
					records = append(records, record(org, n, observed))
				}
				if pass == 1 {
					// The same device twice in one fetch: the window updates it with
					// the first record, the second follows on its own.
					again := record(org, 7, observed.Add(500*time.Millisecond))
					again.Classification.Model = "R-2U"
					records = append(records, again)
				}
				trace.take()
				consume(t, org, records)
				if org != f.OrgA {
					continue
				}
				txs, loose := trace.take()
				statements, updates := len(loose), 0
				for _, tx := range txs {
					statements += len(tx)
					if count(tx, `^update ci set`) == 0 {
						continue
					}
					updates++
					if !lock.MatchString(tx[1]) {
						t.Errorf("a transaction that updates CIs does not take the identity locks first: %.200q", tx)
					}
					checkLockedTail(t, tx)
					for _, sql := range tx {
						if strings.Contains(sql, "update ip_address") {
							addressStatements = append(addressStatements, sql)
						}
					}
				}
				perRecord := float64(statements) / float64(len(records))
				if perRecord > rediscoverBudget {
					t.Errorf("re-discovery of %d records: %d statements, %.2f per record, budget %d", len(records), statements, perRecord, rediscoverBudget)
				}
				// One window per lane, plus the second record of the device sent
				// twice.
				if want := 4 + (2 - pass); updates > want {
					t.Errorf("%d transactions update the %d known devices, want the batch transaction of each window (at most %d)", updates, len(records), want)
				}
				neighbors, edgeReads, confirms := 0, 0, 0
				for _, tx := range txs {
					neighbors += count(tx, `(?s)^select id::text, created_at\s+from ci\s+where .*deleted_at is null and \(id = any \(array\(select ci_id from ci_identity_mac`)
					edgeReads += count(tx, `(?s)^select .*(from|join)\s+ci_relationship\b`)
					confirms += count(tx, `(?s)^update ci_relationship\b`)
				}
				if neighbors > 8 || edgeReads > 4 || confirms > 4 {
					t.Errorf("topology of %d records: %d neighbor lookups, %d edge reads, %d edge updates; want each neighbor once and the edges at once per lane",
						len(records), neighbors, edgeReads, confirms)
				}
				t.Logf("re-discovery %d of %d records: %d statements (%.2f per record) in %d transactions, %d of them update CIs; "+
					"topology: %d neighbor lookups, %d edge reads, %d edge updates", pass, len(records), statements, perRecord, len(txs), updates,
					neighbors, edgeReads, confirms)
			}

			for _, org := range []string{f.OrgA, f.OrgB} {
				var cis, seen int
				if err := f.Admin.QueryRow(bg, `SELECT count(*), count(*) FILTER (WHERE last_seen_at >= $2 AND name LIKE 'srv-%')
					FROM ci WHERE organization_id = $1`, org, start).Scan(&cis, &seen); err != nil {
					t.Fatal(err)
				}
				if cis != servers+2 || seen != servers {
					t.Errorf("%s: %d CIs (want %d, no new CI), %d servers seen (want %d)", org, cis, servers+2, seen, servers)
				}
				if n, confirmed := edges(org, start); n != servers || confirmed != servers {
					t.Errorf("%s: %d edges (want %d, none new), %d confirmed by the re-discovery", org, n, servers, confirmed)
				}
				if result, err := audit.Verify(bg, f.App, org); err != nil || !result.Intact {
					t.Errorf("%s: audit chain: %+v, %v", org, result, err)
				}
			}
			for n := 3; n <= servers+2; n++ {
				want := rediscovered(t, f, f.OrgB, seeded[f.OrgB][n].id)
				got := rediscovered(t, f, f.OrgA, seeded[f.OrgA][n].id)
				for table := range want {
					if !reflect.DeepEqual(got[table], want[table]) {
						w, _ := json.Marshal(want[table])
						g, _ := json.Marshal(got[table])
						t.Errorf("device %d: %s of the window differs from one transaction per record:\n window %s\n record %s", n, table, g, w)
					}
				}
			}
		})
	}

	// The interface sync confirms an address of an interface and counts a
	// miss for the addresses of the device's interfaces it did not report.
	d := seeded[f.OrgA][3]
	args := map[string][]any{
		"update ip_address set miss_count = 0":                  {d.ifID, d.ip},
		"update ip_address a set miss_count = a.miss_count + 1": {d.id, []string{}},
	}
	for prefix, a := range args {
		var sql string
		for _, s := range addressStatements {
			if strings.HasPrefix(strings.Join(strings.Fields(s), " "), prefix) {
				sql = s
				break
			}
		}
		if sql == "" {
			t.Errorf("the re-discovery sent no statement %q", prefix)
			continue
		}
		scans := 0
		for _, r := range relationReads(t, f, "ip_address", sql, a...) {
			if r.node == "ModifyTable" {
				continue
			}
			scans++
			if r.index != "idx_ip_address_interface" || !strings.Contains(r.cond, "interface_id") {
				t.Errorf("%q reads ip_address with %s (index condition %q), want idx_ip_address_interface on interface_id", prefix, r, r.cond)
			}
		}
		if scans == 0 {
			t.Errorf("no read of ip_address in the plan of %q", sql)
		}
	}
}

// TestDiscoveryEdgesMatchDiscoveryEdge covers WP-608 (TOP-02, NFR-03)
// against PostgreSQL: DiscoveryEdges reads the edges of several keys with
// one statement and finds for every key what DiscoveryEdge finds on its
// own, as the application role: the edge with its id and source, nothing
// for a key without one (another type, another direction, a CI of another
// organization) and, for a principal of client 1, nothing for the edge of
// client 2 (row level security, TEN-05).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDiscoveryEdgesMatchDiscoveryEdge(t *testing.T) {
	f := scopetest.Seed(t, "f0")
	bg := context.Background()
	repo := discovery.NewPGRepository(f.App)
	sw, a, b, c := f.CI(t, f.OrgA, "", "wp608-sw"), f.CI(t, f.OrgA, f.Client1, "wp608-a"), f.CI(t, f.OrgA, f.Client1, "wp608-b"),
		f.CI(t, f.OrgA, f.Client2, "wp608-c")
	foreign := f.CI(t, f.OrgB, "", "wp608-foreign")
	for _, e := range []struct{ src, dst, rel, source string }{
		{a, sw, "connected_to", "discovery"}, {b, sw, "connected_to", "manual"}, {a, b, "depends_on", "discovery"}, {c, sw, "connected_to", "discovery"},
	} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source)
			VALUES ($1, $2, $3, $4, $5)`, f.OrgA, e.src, e.dst, e.rel, e.source); err != nil {
			t.Fatal(err)
		}
	}
	keys := []discovery.EdgeKey{
		{SourceCIID: a, TargetCIID: sw, RelType: "connected_to"}, {SourceCIID: b, TargetCIID: sw, RelType: "connected_to"},
		{SourceCIID: a, TargetCIID: b, RelType: "depends_on"}, {SourceCIID: c, TargetCIID: sw, RelType: "connected_to"},
		{SourceCIID: a, TargetCIID: b, RelType: "connected_to"}, {SourceCIID: sw, TargetCIID: a, RelType: "connected_to"},
		{SourceCIID: foreign, TargetCIID: sw, RelType: "connected_to"}, {SourceCIID: a, TargetCIID: sw, RelType: "connected_to"},
	}
	for name, ctx := range map[string]context.Context{"org-wide": f.OrgCtx(f.OrgA), "client 1": f.ClientCtx(f.Client1)} {
		found, err := repo.DiscoveryEdges(ctx, f.OrgA, keys)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		hits := 0
		for _, k := range keys {
			id, source, ok, err := repo.DiscoveryEdge(ctx, f.OrgA, k.SourceCIID, k.TargetCIID, k.RelType)
			if err != nil {
				t.Fatal(err)
			}
			got, batched := found[k]
			if batched != ok || got.ID != id || got.Source != source {
				t.Errorf("%s: key %+v: DiscoveryEdges %+v (%v), DiscoveryEdge %q %q (%v)", name, k, got, batched, id, source, ok)
			}
			if ok {
				hits++
			}
		}
		if want := map[string]int{"org-wide": 5, "client 1": 4}[name]; hits != want {
			t.Errorf("%s: %d keys with an edge, want %d", name, hits, want)
		}
	}
}

// seedKnownServers creates the devices of a load test seed in org
// (cmd/seed, SIM-01): the switches 1 and 2 and the servers 3 … servers+2
// with serial number, management IP and host name, eth0 with a locally
// administered MAC and the management address, an LLDP edge from every
// server to switch 1 + n mod 2 (serial numbers and MACs the same in every
// organization), and 20,000 addresses of no interface in the
// organization, so that a read of every address of the organization costs
// what it costs at the volumes of the load test.
func seedKnownServers(t *testing.T, f *scopetest.Fixture, org string, servers int) {
	t.Helper()
	bg := context.Background()
	devices := servers + 2
	for _, s := range []struct {
		sql  string
		args []any
	}{
		{`INSERT INTO ci (id, organization_id, ci_type_id, name, status, manufacturer, model, serial_number, management_ip,
			hostname, discovery_source, first_seen_at, last_seen_at, attributes)
		SELECT md5($1 || '/ci/' || i)::uuid, $1::uuid, t.id,
			CASE WHEN i <= 2 THEN 'sw-' ELSE 'srv-' END || lpad(i::text, 5, '0'), 'active', 'Reticora Labs',
			CASE WHEN i <= 2 THEN 'RX-48' ELSE 'R-1U' END, 'SN' || upper(substr(md5('rediscover/serial/' || i), 1, 12)),
			('10.241.' || (i / 250) || '.' || (i % 250 + 1))::inet, CASE WHEN i <= 2 THEN 'sw-' ELSE 'srv-' END || lpad(i::text, 5, '0'),
			'snmp', now() - interval '30 days', now() - interval '1 day', '{}'::jsonb
		FROM generate_series(1, $2::int) AS i
		JOIN ci_type t ON t.organization_id IS NULL AND t.key = CASE WHEN i <= 2 THEN 'switch' ELSE 'server' END`, []any{org, devices}},
		{`INSERT INTO network_interface (id, organization_id, ci_id, name, mac, if_type, speed_mbps, is_management, oper_status, if_index)
		SELECT md5($1 || '/if/' || i)::uuid, $1::uuid, md5($1 || '/ci/' || i)::uuid, 'eth0',
			('02:' || substr(md5('rediscover/mac/' || i), 1, 2) || ':' || substr(md5('rediscover/mac/' || i), 3, 2) || ':' ||
			 substr(md5('rediscover/mac/' || i), 5, 2) || ':' || substr(md5('rediscover/mac/' || i), 7, 2) || ':' ||
			 substr(md5('rediscover/mac/' || i), 9, 2))::macaddr, 'ethernet', 1000, true, 'up', 1
		FROM generate_series(1, $2::int) AS i`, []any{org, devices}},
		{`INSERT INTO ip_address (organization_id, interface_id, address, type, last_seen_at)
		SELECT $1::uuid, md5($1 || '/if/' || i)::uuid, ('10.241.' || (i / 250) || '.' || (i % 250 + 1))::inet, 'discovered',
			now() - interval '1 day'
		FROM generate_series(1, $2::int) AS i`, []any{org, devices}},
		{`INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type, source, source_system, confidence,
			first_seen_at, last_seen_at, last_confirmed_at)
		SELECT $1::uuid, md5($1 || '/ci/' || i)::uuid, md5($1 || '/ci/' || (1 + i % 2))::uuid, 'connected_to', 'discovery', 'lldp', 1.0,
			now() - interval '30 days', now() - interval '1 day', now() - interval '1 day'
		FROM generate_series(3, $2::int) AS i`, []any{org, devices}},
		{`INSERT INTO ip_address (organization_id, address, type)
		SELECT $1::uuid, ('10.242.' || (i / 250) || '.' || (i % 250 + 1))::inet, 'discovered'
		FROM generate_series(1, 20000) AS i`, []any{org}},
	} {
		if _, err := f.Admin.Exec(bg, s.sql, s.args...); err != nil {
			t.Fatalf("seed %s: %v", org, err)
		}
	}
	for _, table := range []string{"ci", "ci_identity_mac", "network_interface", "ip_address", "ci_relationship"} {
		if _, err := f.Admin.Exec(bg, `ANALYZE `+table); err != nil {
			t.Fatal(err)
		}
	}
}

// rediscovered reads what the ingest keeps of a device per table, with ids,
// times, hashes and the ids of other rows replaced (WP-608): the CI, its
// history, audit entries, events, search document, interfaces, addresses,
// identity MACs and edges.
func rediscovered(t *testing.T, f *scopetest.Fixture, orgID, ciID string) map[string]any {
	t.Helper()
	out := map[string]any{}
	for table, query := range map[string]string{
		"ci":        `SELECT jsonb_agg(to_jsonb(x)) FROM ci x WHERE x.id = $1`,
		"ci_change": `SELECT jsonb_agg(to_jsonb(x) ORDER BY x.ci_version, x.change_type, x.field_name, x.new_value::text) FROM ci_change x WHERE x.ci_id = $1`,
		"audit_log": `SELECT jsonb_agg(to_jsonb(x) - 'seq' ORDER BY x.seq) FROM audit_log x WHERE x.resource_id = $1 AND x.action LIKE 'ci.%'`,
		"event_outbox": `SELECT jsonb_agg(to_jsonb(x) - 'sequence' - 'published_at' - 'attempts' - 'last_error' ORDER BY x.sequence)
			FROM event_outbox x WHERE x.entity_id = $1::text`,
		"search_document":   `SELECT jsonb_agg(to_jsonb(x)) FROM search_document x WHERE x.entity_id = $1`,
		"network_interface": `SELECT jsonb_agg(to_jsonb(x) ORDER BY x.name) FROM network_interface x WHERE x.ci_id = $1`,
		"ip_address": `SELECT jsonb_agg(to_jsonb(a) - 'interface_id' - 'vrf_id' - 'subnet_id' ORDER BY a.address)
			FROM ip_address a JOIN network_interface n ON n.id = a.interface_id WHERE n.ci_id = $1`,
		"ci_identity_mac": `SELECT jsonb_agg(m.mac::text ORDER BY m.mac) FROM ci_identity_mac m WHERE m.ci_id = $1`,
		"ci_relationship": `SELECT jsonb_agg(to_jsonb(r) - 'source_ci_id' - 'target_ci_id' - 'last_confirmed_at' ORDER BY r.rel_type)
			FROM ci_relationship r WHERE r.source_ci_id = $1`,
	} {
		var raw []byte
		if err := f.Admin.QueryRow(context.Background(), query, ciID).Scan(&raw); err != nil {
			t.Fatalf("%s of %s: %v", table, ciID, err)
		}
		var v any
		if raw != nil {
			if err := json.Unmarshal(raw, &v); err != nil {
				t.Fatal(err)
			}
		}
		out[table] = withoutVarying(v, ciID, orgID)
	}
	return out
}
