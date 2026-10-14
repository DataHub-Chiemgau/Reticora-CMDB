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
	handler := func(production, batch bool) http.Handler {
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
		if production {
			types := citype.NewPGRepository(pool)
			h.WithInstanceFields(types).WithObservedValidator(ci.NewService(cis).WithFieldResolver(types))
		}
		mux := chi.NewRouter()
		h.RegisterRoutes(mux)
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
// the same device: ids, organizations, times and the hashes over them.
var ingestVarying = map[string]bool{
	"id": true, "organization_id": true, "ci_id": true, "entity_id": true, "resource_id": true,
	"created_at": true, "updated_at": true, "first_seen_at": true, "last_seen_at": true, "discovered_at": true,
	"timestamp": true, "occurred_at": true, "hash": true, "previous_hash": true, "record_id": true,
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
