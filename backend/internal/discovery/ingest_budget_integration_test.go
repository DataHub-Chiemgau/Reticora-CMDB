package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
// fields of a new CI are recorded in one statement.
type manyProvenance struct{ provenance }

func (p manyProvenance) RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error {
	return p.repo.RecordDiscoveredMany(ctx, orgID, ciID, values, source, observedAt)
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

// TestIngestStatementBudget covers WP-332 (NFR-03) against PostgreSQL: with
// the write path of the server (audit, outbox, search index, provenance in
// one statement) a batch of 20 new devices like the load test's runs every
// record in exactly one transaction of at most 12 statements, which holds
// identity candidates, creation, provenance and search index; the frame of
// the batch (ledger, one type lookup, VRF, ledger write) costs the same
// statements for 5 records as for 20.
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
	handler := func(production bool) http.Handler {
		var indexer ci.Indexer = search.NewCIIndexer(search.NewPGRepository(pool))
		cis := ci.NewIndexingRepository(ci.NewPGRepositoryWithAudit(pool, audit.NewPGRecorder()).WithEvents(events.NewOutbox()), indexer)
		if production {
			cis = ci.NewIndexingRepository(cis, ai.NewCIChunkIndexer(ai.NewPGRepository(pool), nil))
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
	ingest := func(t *testing.T, mux http.Handler, n int) ingestBatch {
		t.Helper()
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
		mux := handler(false)
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
		mux := handler(true)
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
}
