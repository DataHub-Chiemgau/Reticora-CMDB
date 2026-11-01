package discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// wp678Queue keeps the published ingest messages (the ingest.> stream).
type wp678Queue struct{ msgs []IngestMessage }

func (q *wp678Queue) Publish(_ context.Context, msgs []IngestMessage) error {
	q.msgs = append(q.msgs, msgs...)
	return nil
}

// TestEdgeIngestBindsRecordsToCollectorClient covers WP-678 (F-407; DIS-05,
// TEN-09) against PostgreSQL: the records a client-bound collector sends
// over the edge (EdgeIngest in the system context of the NATS endpoint)
// land only in its client.
//
//   - A record without client_id takes the collector's client; one naming
//     client 2, a site of client 2 or a discovery scope of client 2 is
//     rejected on its own (403, not retryable) while the valid records of
//     the batch are processed (WP-481). A record matching the serial of a
//     client-2 CI does not update that CI (identity resolution sees only
//     the collector's client); it is rejected with 409 (serial unique per
//     organization).
//   - A batch whose every record lies outside the client answers 403 with
//     the record errors and creates nothing.
//   - Through the queue (NATS consumer path) the messages carry the
//     collector's client scope and the bound client; the refused records
//     are not published; consumed, they create the CI in client 1.
//   - An organization-wide collector keeps the organization-wide behaviour:
//     the record's client decides (DIS-05, client from the scope).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestEdgeIngestBindsRecordsToCollectorClient(t *testing.T) {
	f := scopetest.Seed(t, "b1")
	bg := context.Background()
	orgCtx := f.OrgCtx(f.OrgA)
	repo := NewPGRepository(f.App)
	h := NewHandler(repo, ci.NewPGRepository(f.App))
	register := func(name, clientID string) *Collector {
		t.Helper()
		c := &Collector{OrganizationID: f.OrgA, ClientID: clientID, Name: name, Status: CollectorActive, Config: map[string]any{}}
		if err := repo.RegisterCollector(orgCtx, c); err != nil {
			t.Fatal(err)
		}
		return c
	}
	col1 := register("wp678-c1", f.Client1)
	col2 := register("wp678-c2", f.Client2)
	orgWide := register("wp678-org", "")
	newScope := func(col *Collector, target string) string {
		t.Helper()
		s := NewScope(f.OrgA, &ScopeInput{CollectorID: col.ID, Kind: "cidr", Target: target})
		if err := repo.CreateScope(orgCtx, s); err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	scope1 := newScope(col1, "10.78.1.0/24")
	scope2 := newScope(col2, "10.78.2.0/24")
	var site1, site2 string
	for _, s := range []struct {
		id     *string
		client string
	}{{&site1, f.Client1}, {&site2, f.Client2}} {
		if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, s.client, "wp678-site-"+s.client[len(s.client)-2:]).Scan(s.id); err != nil {
			t.Fatal(err)
		}
	}
	var serverType, foreign string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `INSERT INTO ci (organization_id, client_id, ci_type_id, name, status, serial_number, model)
		VALUES ($1, $2, $3, 'wp678-c2-server', 'active', 'WP678-C2-SERIAL', 'orig') RETURNING id::text`,
		f.OrgA, f.Client2, serverType).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	foreignState := func() string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, 'NULL') || ' model=' || COALESCE(model, '') || ' ip=' ||
			COALESCE(host(management_ip), '') || ' deleted=' || (deleted_at IS NOT NULL)::text FROM ci WHERE id = $1`, foreign).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := foreignState()
	clientsOf := func(serial string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(string_agg(COALESCE(client_id::text, 'NULL'), ',' ORDER BY client_id), 'none')
			FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, f.OrgA, serial).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	now := time.Now().UTC().Add(-time.Minute)
	ip := 0
	record := func(serial, model string) DeviceRecord {
		ip++
		return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "wp678-" + strings.ToLower(serial),
			Serial: serial, Model: model, IP: "10.78.0." + strconv.Itoa(ip)}, now)
	}
	edge := func(h *Handler, col *Collector, records ...DeviceRecord) EdgeReply {
		t.Helper()
		body, err := json.Marshal(DeviceRecordBatch{Records: records})
		if err != nil {
			t.Fatal(err)
		}
		// The NATS endpoint calls the handler in the organization's system
		// context (ServeCollectorEdge).
		return h.EdgeIngest(systemContext(bg, f.OrgA), f.OrgA, col.ID, body)
	}
	errorsOf := func(reply EdgeReply) map[string]IngestRecordError {
		out := map[string]IngestRecordError{}
		if reply.Result != nil {
			for _, e := range reply.Result.Errors {
				out[e.RecordID] = e
			}
		}
		return out
	}

	// 1. A mixed batch of the client-1 collector.
	foreignClient := record("WP678-NEW-C2", "m")
	foreignClient.ClientID = f.Client2
	none := record("WP678-NEW-NONE", "m")
	sameSerial := record("WP678-C2-SERIAL", "tampered-by-c1")
	foreignScope := record("WP678-SCOPE-C2", "m")
	foreignScope.ScopeID = scope2
	foreignSite := record("WP678-SITE-C2", "m")
	foreignSite.SiteID = site2
	own := record("WP678-OWN", "m")
	own.ClientID, own.ScopeID, own.SiteID = strings.ToUpper(f.Client1), scope1, site1
	reply := edge(h, col1, foreignClient, none, sameSerial, foreignScope, foreignSite, own)
	if reply.Status != http.StatusAccepted || reply.Result == nil {
		t.Fatalf("mixed batch of the client-1 collector: %+v, want 202", reply)
	}
	failed := errorsOf(reply)
	for _, r := range []DeviceRecord{foreignClient, foreignScope, foreignSite} {
		if e, ok := failed[r.RecordID]; !ok || e.Status != http.StatusForbidden || e.Retryable {
			t.Errorf("record %s (client %q scope %q site %q) of the client-1 collector: error %+v (listed %v), want 403 not retryable",
				r.Identity.SerialNumber, r.ClientID, r.ScopeID, r.SiteID, e, ok)
		}
	}
	// The client-2 CI is invisible to the collector: its record creates no
	// CI of client 1 with that serial (unique per organization) and is
	// rejected with 409 like the record of a client-1 principal on the API.
	if e, ok := failed[sameSerial.RecordID]; !ok || e.Status != http.StatusConflict {
		t.Errorf("record with the serial of the client-2 CI: error %+v (listed %v), want rejected with 409", e, ok)
	}
	if got := reply.Result.Accepted; got != 2 {
		t.Errorf("accepted %d records of the mixed batch, want 2 (%+v)", got, reply.Result)
	}
	for serial, want := range map[string]string{
		"WP678-NEW-C2": "none", "WP678-NEW-NONE": f.Client1, "WP678-SCOPE-C2": "none", "WP678-SITE-C2": "none",
		"WP678-OWN": f.Client1, "WP678-C2-SERIAL": f.Client2,
	} {
		if got := clientsOf(serial); got != want {
			t.Errorf("CIs with serial %s after the client-1 collector's batch: clients %s, want %s", serial, got, want)
		}
	}
	if after := foreignState(); after != before {
		t.Errorf("the client-1 collector changed the client-2 CI: %s, before %s", after, before)
	}
	var reviews int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND client_id IS DISTINCT FROM $2
		AND payload->>'serial_number' LIKE 'WP678-%'`, f.OrgA, f.Client1).Scan(&reviews); err != nil {
		t.Fatal(err)
	}
	if reviews != 0 {
		t.Errorf("%d review items of the client-1 collector's records outside client 1", reviews)
	}

	// 2. Only records outside the client: 403, nothing created.
	onlyForeign := record("WP678-ONLY-C2", "m")
	onlyForeign.ClientID = f.Client2
	if reply = edge(h, col1, onlyForeign); reply.Status != http.StatusForbidden || len(errorsOf(reply)) != 1 {
		t.Errorf("batch of only a client-2 record from the client-1 collector: %+v, want 403 with the record error", reply)
	}
	if got := clientsOf("WP678-ONLY-C2"); got != "none" {
		t.Errorf("CI of the refused batch: clients %s, want none", got)
	}

	// 3. The queue path: the messages carry the collector's scope.
	queue := &wp678Queue{}
	hq := NewHandler(repo, ci.NewPGRepository(f.App)).WithIngestQueue(queue)
	queuedForeign := record("WP678-Q-C2", "m")
	queuedForeign.ClientID = f.Client2
	queued := record("WP678-Q-NONE", "m")
	if reply = edge(hq, col1, queuedForeign, queued); reply.Status != http.StatusAccepted || reply.Result.Queued != 1 ||
		len(errorsOf(reply)) != 1 {
		t.Errorf("queued batch of the client-1 collector: %+v, want 202 with 1 queued and the client-2 record rejected", reply)
	}
	if len(queue.msgs) != 1 {
		t.Fatalf("published %d messages, want 1 (the client-2 record is not published)", len(queue.msgs))
	}
	m := queue.msgs[0]
	if m.Scope == nil || m.Scope.Clients == nil || len(*m.Scope.Clients) != 1 || !strings.EqualFold((*m.Scope.Clients)[0], f.Client1) ||
		!strings.EqualFold(m.ClientID, f.Client1) {
		t.Errorf("published message: scope %+v client %q, want the client-1 scope and client 1", m.Scope, m.ClientID)
	}
	if _, err := h.ProcessMessages(bg, queue.msgs); err != nil {
		t.Errorf("consume the published message: %v", err)
	}
	if got := clientsOf("WP678-Q-NONE"); got != f.Client1 {
		t.Errorf("CI of the consumed message: clients %s, want client 1", got)
	}

	// 4. The organization-wide collector keeps the record's client.
	orgForeign := record("WP678-ORG-C2", "m")
	orgForeign.ClientID = f.Client2
	orgNone := record("WP678-ORG-NONE", "m")
	if reply = edge(h, orgWide, orgForeign, orgNone); reply.Status != http.StatusAccepted || reply.Result.Accepted != 2 {
		t.Errorf("batch of the organization-wide collector: %+v, want 202 with 2 accepted", reply)
	}
	if got := clientsOf("WP678-ORG-C2"); got != f.Client2 {
		t.Errorf("organization-wide collector, record of client 2: clients %s, want client 2", got)
	}
	if got := clientsOf("WP678-ORG-NONE"); got != "NULL" {
		t.Errorf("organization-wide collector, record without client: clients %s, want NULL", got)
	}
}
