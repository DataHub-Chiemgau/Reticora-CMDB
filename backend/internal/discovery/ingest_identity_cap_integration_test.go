package discovery

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// capRecord is a server record with n identity values: serial number,
// hardware UUID and target_ip (3), the rest as interface MACs; each record
// of a test has its own name length, so its IP and MACs are its own.
func capRecord(name string, n int, observed time.Time) DeviceRecord {
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: name, IP: fmt.Sprintf("192.0.2.%d", len(name)),
		Serial: "SN-" + name}, observed)
	rec.Identity.HardwareUUID = fmt.Sprintf("4c4c4544-0042-3510-8051-%06d%06d", len(name), n)
	for i := 0; i < n-3; i++ {
		rec.Interfaces = append(rec.Interfaces, plugins.DeviceInterface{
			Name: fmt.Sprintf("eth%d", i), MAC: fmt.Sprintf("02:%02x:00:00:%02x:%02x", len(name), i/256, i%256)})
	}
	return rec
}

// TestIngestRejectsRecordOverIdentityCap covers SR-10 (WP-878): a record
// with more identity values than plugins.MaxIdentityKeys is refused on its
// own, counted in rejected_identity_limit and in
// reticora_ingest_records_rejected_total{reason="identity_keys"}; the other
// records of the batch are processed. A record with exactly the limit is
// processed and its identity locks stay below the exclusive organization
// lock.
func TestIngestRejectsRecordOverIdentityCap(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	discoveryRepo.SeedCIType("server", "server")
	ciRepo := ci.NewMemoryRepository()
	mux := chi.NewRouter()
	NewHandler(discoveryRepo, ciRepo).RegisterRoutes(mux)

	observed := time.Now().UTC().Add(-time.Minute)
	first, over, last := capRecord("cap-first", 4, observed), capRecord("cap-over", plugins.MaxIdentityKeys+1, observed),
		capRecord("cap-lst", 4, observed)
	if got := over.IdentityKeyCount(); got != plugins.MaxIdentityKeys+1 {
		t.Fatalf("identity values of the record over the limit: %d", got)
	}
	before := testutil.ToFloat64(recordsRejected.WithLabelValues("identity_keys"))

	post := func(records ...DeviceRecord) BulkIngestResponse {
		t.Helper()
		raw, err := json.Marshal(DeviceRecordBatch{Records: records})
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, discoveryTenantCtx(httptest.NewRequest(http.MethodPost, "/api/v1/ingest/bulk", bytes.NewReader(raw))))
		if w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
		var resp BulkIngestResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		return resp
	}

	resp := post(first, over, last)
	if resp.RejectedIdentityLimit != 1 || resp.Received != 3 || resp.Accepted != 2 || resp.Rejected != 0 {
		t.Errorf("answer %+v, want received 3, accepted 2, rejected_identity_limit 1", resp)
	}
	if got := testutil.ToFloat64(recordsRejected.WithLabelValues("identity_keys")) - before; got != 1 {
		t.Errorf("reticora_ingest_records_rejected_total{reason=identity_keys} rose by %v, want 1", got)
	}
	names := map[string]bool{}
	items, _, err := ciRepo.List(context.Background(), "org-1", &ci.FilterParams{}, api.PaginationParams{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for i := range items {
		names[items[i].Name] = true
	}
	if !names["cap-first"] || !names["cap-lst"] || names["cap-over"] || len(items) != 2 {
		t.Errorf("CIs after the batch: %v, want cap-first and cap-lst only (no record shortened)", names)
	}

	// Exactly the limit: processed, and its locks are no exclusive lock.
	exact := capRecord("cap-exact", plugins.MaxIdentityKeys, observed)
	if got := exact.IdentityKeyCount(); got != plugins.MaxIdentityKeys {
		t.Fatalf("identity values of the record at the limit: %d", got)
	}
	if resp = post(exact); resp.RejectedIdentityLimit != 0 || resp.Accepted != 1 {
		t.Errorf("record at the limit: %+v, want accepted", resp)
	}
	item := itemFromRecord(&exact)
	if locks := IdentityLocksFor("org-1", nil, &item); locks.Exclusive || len(locks.Keys) == 0 || len(locks.Keys) > identityLockLimit {
		t.Errorf("locks of a record at the limit: exclusive %v, %d keys (limit %d)", locks.Exclusive, len(locks.Keys), identityLockLimit)
	}

	// A batch of records all over the limit is answered, nothing processed.
	if resp = post(capRecord("cap-over-2", plugins.MaxIdentityKeys+10, observed)); resp.RejectedIdentityLimit != 1 || resp.Accepted != 0 {
		t.Errorf("batch over the limit only: %+v", resp)
	}
}
