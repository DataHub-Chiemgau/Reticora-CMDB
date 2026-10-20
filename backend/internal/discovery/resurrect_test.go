package discovery_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/go-chi/chi/v5"
)

// deletedInventory adds soft-deleted CIs to the memory repository.
type deletedInventory struct {
	*discovery.MemoryRepository
	deleted []ci.Item
}

func (d *deletedInventory) DeletedIdentityCandidates(context.Context, string, *discovery.IdentityQuery) ([]ci.Item, error) {
	return d.deleted, nil
}

func ingestRecords(t *testing.T, mux http.Handler, ctx context.Context, records ...plugins.DeviceRecord) discovery.BulkIngestResponse {
	t.Helper()
	body, err := json.Marshal(plugins.DeviceRecordBatch{Records: records})
	if err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
	if w.Code != http.StatusAccepted {
		t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
	}
	var resp discovery.BulkIngestResponse
	if err = json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func resurrectRecord(serial string) plugins.DeviceRecord {
	return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "back-" + serial, IP: "192.0.2.87", Serial: serial},
		time.Now().Add(-time.Minute))
}

// TestDeletedDeviceSeenAgainIsReviewed covers WP-087 (CI-13) without a
// database: a device that matches a soft-deleted CI creates no CI but one
// resurrected_device review, also when it is seen again.
func TestDeletedDeviceSeenAgainIsReviewed(t *testing.T) {
	repo := &deletedInventory{MemoryRepository: discovery.NewMemoryRepository(),
		deleted: []ci.Item{{ID: "gone", CITypeID: "server", SerialNumber: "RES-SN"}}}
	repo.SeedCIType("server", "server")
	cis := ci.NewMemoryRepository()
	mux := chi.NewRouter()
	discovery.NewHandler(repo, cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"})

	if resp := ingestRecords(t, mux, ctx, resurrectRecord("RES-SN")); resp.Created != 0 || resp.Resurrected != 1 || resp.ReviewItems != 1 {
		t.Fatalf("deleted device seen again: %+v", resp)
	}
	if resp := ingestRecords(t, mux, ctx, resurrectRecord("RES-SN")); resp.Created != 0 || resp.Resurrected != 1 || resp.ReviewItems != 0 {
		t.Fatalf("seen once more: %+v", resp)
	}
	reviews, _, _ := repo.ListReviewItems(context.Background(), "org-1", discovery.ReviewFilter{Kind: discovery.ReviewKindResurrectedDevice},
		api.PaginationParams{Limit: 10})
	if len(reviews) != 1 || len(reviews[0].CandidateCIIDs) != 1 || reviews[0].CandidateCIIDs[0] != "gone" {
		t.Fatalf("reviews: %+v", reviews)
	}
	if _, total, _ := cis.List(context.Background(), "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 1}); total != 0 {
		t.Errorf("CIs created: %d", total)
	}
	// Another device still becomes a CI.
	if resp := ingestRecords(t, mux, ctx, resurrectRecord("OTHER-SN")); resp.Created != 1 || resp.Resurrected != 0 {
		t.Errorf("unrelated device: %+v", resp)
	}
}

// TestResurrectedDeviceIsReactivatedOrNew covers WP-087 (CI-13, PRI-03)
// against PostgreSQL: identity resolution includes soft-deleted CIs, a hit
// is reviewed instead of creating a duplicate, merge reactivates the deleted
// CI (change history and audit) and create makes a new CI.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestResurrectedDeviceIsReactivatedOrNew(t *testing.T) {
	f := scopetest.Seed(t, "79")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	repo := discovery.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(repo, cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})

	deleteDevice := func(name, serial string) string {
		t.Helper()
		id := f.CI(t, f.OrgA, "", name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = $2 WHERE id = $1`, id, serial); err != nil {
			t.Fatal(err)
		}
		if err := cis.Delete(ctx, f.OrgA, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	reviewOf := func(ciID string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'resurrected_device'
			AND status = 'open' AND $2::uuid = ANY(candidate_ci_ids)`, f.OrgA, ciID).Scan(&id); err != nil {
			t.Fatalf("review of %s: %v", ciID, err)
		}
		return id
	}
	resolve := func(reviewID, body string) {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/discovery/review-items/"+reviewID+"/resolve", strings.NewReader(body))
		mux.ServeHTTP(w, req.WithContext(ctx))
		if w.Code != http.StatusOK {
			t.Fatalf("resolve %s: %d %s", body, w.Code, w.Body.String())
		}
	}
	liveCIs := func(serial string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`,
			f.OrgA, serial).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	gone := deleteDevice("res-gone", "RES-SN-1")
	for range 2 {
		if resp := ingestRecords(t, mux, ctx, resurrectRecord("RES-SN-1")); resp.Created != 0 || resp.Resurrected != 1 {
			t.Fatalf("deleted device seen again: %+v", resp)
		}
	}
	if n := liveCIs("RES-SN-1"); n != 0 {
		t.Fatalf("duplicate created: %d live CIs", n)
	}

	// Merge reactivates the deleted CI.
	resolve(reviewOf(gone), `{"action":"merge","ci_id":"`+gone+`"}`)
	if n := liveCIs("RES-SN-1"); n != 1 {
		t.Fatalf("reactivated CIs: %d", n)
	}
	var audits, changes int
	if err := f.Admin.QueryRow(bg, `SELECT
		(SELECT count(*) FROM audit_log WHERE resource_id = $1 AND action = 'ci.restored'),
		(SELECT count(*) FROM ci_change WHERE ci_id = $1::uuid AND change_type = 'reconciliation_decision' AND field_name = 'deleted_at')`,
		gone).Scan(&audits, &changes); err != nil || audits != 1 || changes != 1 {
		t.Errorf("restore recorded: audit %d, change %d, %v", audits, changes, err)
	}
	// The reactivated CI matches the next record directly.
	if resp := ingestRecords(t, mux, ctx, resurrectRecord("RES-SN-1")); resp.Updated != 1 || resp.Resurrected != 0 {
		t.Errorf("after reactivation: %+v", resp)
	}

	// Create makes a new CI and leaves the deleted one deleted.
	other := deleteDevice("res-other", "RES-SN-2")
	ingestRecords(t, mux, ctx, resurrectRecord("RES-SN-2"))
	resolve(reviewOf(other), `{"action":"create"}`)
	var deletedStill bool
	if err := f.Admin.QueryRow(bg, `SELECT deleted_at IS NOT NULL FROM ci WHERE id = $1`, other).Scan(&deletedStill); err != nil || !deletedStill {
		t.Errorf("deleted CI after create: deleted=%v, %v", deletedStill, err)
	}
	if n := liveCIs("RES-SN-2"); n != 1 {
		t.Errorf("new CIs after create: %d", n)
	}
}

// TestAuditT3ResurrectedNewKeepsClientAndIdentity is the retest of F-022
// (WP-087: CI-13, REC-03, TEN-05), moved here from
// audit_t3_review_resolution_auditdefect_test.go by WP-369: new on a
// resurrected_device item creates the CI of the record's client with its
// identity (MAC), a principal of another client cannot read it, and the
// next sighting updates it without a new resurrected_device review.
func TestAuditT3ResurrectedNewKeepsClientAndIdentity(t *testing.T) {
	f := scopetest.Seed(t, "f4")
	bg := context.Background()
	const mac = "00:1b:21:a3:f4:01"
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	gone := f.CI(t, f.OrgA, f.Client1, "audit-t3-gone")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, gone, mac); err != nil {
		t.Fatal(err)
	}
	if err := cis.Delete(ctx, f.OrgA, gone); err != nil {
		t.Fatal(err)
	}
	rec := func() plugins.DeviceRecord {
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "audit-t3-back", IP: "192.0.2.244"}, time.Now().Add(-time.Minute))
		r.Identity.PrimaryMAC = mac
		r.ClientID = f.Client1
		return r
	}
	openReviews := func() int {
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'resurrected_device' AND status = 'open'`, f.OrgA).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ingestRecords(t, mux, ctx, rec())
	var reviewID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM review_item WHERE organization_id = $1 AND kind = 'resurrected_device' AND status = 'open'`, f.OrgA).Scan(&reviewID); err != nil {
		t.Fatalf("precondition: no open resurrected_device review: %v", err)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/review-items/"+reviewID+"/resolve", strings.NewReader(`{"action":"new"}`)).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("resolve new: %d %s", w.Code, w.Body.String())
	}
	var newID, client, gotMAC string
	if err := f.Admin.QueryRow(bg, `SELECT id::text, coalesce(client_id::text, '<NULL>'), coalesce(primary_mac::text, '<NULL>')
		FROM ci WHERE organization_id = $1 AND name = 'audit-t3-back' AND deleted_at IS NULL`, f.OrgA).Scan(&newID, &client, &gotMAC); err != nil {
		t.Fatalf("new CI: %v", err)
	}
	if client != f.Client1 {
		t.Errorf("new CI client_id = %s, want the record's client %s (org-wide CI of a client-bound device)", client, f.Client1)
	}
	if gotMAC != mac {
		t.Errorf("new CI primary_mac = %s, want %s (identity lost)", gotMAC, mac)
	}
	client2 := tenant.WithTenant(f.ClientCtx(f.Client2), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	if _, err := ci.NewPGRepository(f.App).GetByID(client2, f.OrgA, newID); err == nil {
		t.Errorf("a principal restricted to client 2 can read the new client-1 CI %s (TEN-05)", newID)
	}
	resp := ingestRecords(t, mux, ctx, rec())
	if resp.Updated != 1 || resp.Created != 0 || resp.Resurrected != 0 || openReviews() != 0 {
		t.Errorf("next sighting after 'new': created=%d updated=%d resurrected=%d open resurrected_device reviews=%d; want 0/1/0/0 (the device must update its new CI, not loop through review)",
			resp.Created, resp.Updated, resp.Resurrected, openReviews())
	}
}

// resolveAs posts a resolution of review item id as the principal of ctx.
func resolveAs(t *testing.T, mux http.Handler, ctx context.Context, id, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/review-items/"+id+"/resolve", strings.NewReader(body)).WithContext(ctx))
	return w
}

// TestReviewNewKeepsClientAndIdentity covers WP-369 (CI-13, TEN-05; F-022)
// for the three types new resolves: a resurrected_device, ambiguous_identity
// or unclassified_device (unknown type) item keeps the client, site and
// identity of the record in its payload; new creates the CI as the ingest
// does (provenance; an identity value another CI holds is left out) with
// the client and site of the item; a principal of another client gets 404;
// the next sighting updates the CI and opens no review.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReviewNewKeepsClientAndIdentity(t *testing.T) {
	f := scopetest.Seed(t, "0a")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).
		WithProvenance(provenance{repo: override.NewPGRepository(f.App)}).RegisterRoutes(mux)
	user := tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}
	org := tenant.WithTenant(f.OrgCtx(f.OrgA), user)
	client1 := tenant.WithTenant(f.ClientCtx(f.Client1), user)
	client2 := tenant.WithTenant(f.ClientCtx(f.Client2), user)
	var site string
	if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'wp369-site') RETURNING id::text`,
		f.OrgA, f.Client1).Scan(&site); err != nil {
		t.Fatal(err)
	}
	// A sighting of client 1.
	record := func(ciType, serial, uuid, mac, ip string) plugins.DeviceRecord {
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: ciType, Name: "wp369-" + strings.ToLower(serial), IP: ip, Serial: serial, MAC: mac},
			time.Now().Add(-time.Minute))
		r.Identity.HardwareUUID = uuid
		r.ClientID = f.Client1
		return r
	}
	type openItem struct {
		id, client, site string
		payload          map[string]any
	}
	open := func(kind, serial string) openItem {
		t.Helper()
		var it openItem
		if err := f.Admin.QueryRow(bg, `SELECT id::text, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), payload FROM review_item
			WHERE organization_id = $1 AND kind = $2 AND status = 'open' AND payload->>'serial_number' = $3`, f.OrgA, kind, serial).
			Scan(&it.id, &it.client, &it.site, &it.payload); err != nil {
			t.Fatalf("open %s item of %s: %v", kind, serial, err)
		}
		return it
	}
	// The payload keeps the record: client, site, identity and source.
	checkPayload := func(it openItem, serial, uuid, mac, site string) {
		t.Helper()
		p := it.payload
		fingerprint, _ := p["fingerprint"].(map[string]any)
		if p["client_id"] != f.Client1 || p["site_id"] != site || p["serial_number"] != serial || p["hardware_uuid"] != uuid ||
			p["primary_mac"] != mac || p["source"] != "snmp" || fingerprint["hardware_uuid"] != uuid ||
			fmt.Sprint(fingerprint["mac_addresses"]) != "["+mac+"]" {
			t.Errorf("payload of %s: %v", serial, p)
		}
	}
	type newCI struct{ id, client, site, location, typeID, mac, uuid, source, fingerprintUUID string }
	created := func(serial string) newCI {
		t.Helper()
		var c newCI
		if err := f.Admin.QueryRow(bg, `SELECT id::text, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''),
			COALESCE(location_id::text, ''), ci_type_id::text, COALESCE(primary_mac::text, ''), COALESCE(hardware_uuid::text, ''),
			COALESCE(discovery_source, ''), COALESCE(attributes->'fingerprint'->>'hardware_uuid', '')
			FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, f.OrgA, serial).
			Scan(&c.id, &c.client, &c.site, &c.location, &c.typeID, &c.mac, &c.uuid, &c.source, &c.fingerprintUUID); err != nil {
			t.Fatalf("new CI of %s: %v", serial, err)
		}
		return c
	}
	liveCIs := func(serial string) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`,
			f.OrgA, serial).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	provenanceOf := func(ciID, field string) string {
		t.Helper()
		var source string
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE(discovered_source, '') FROM ci_field_value WHERE ci_id = $1 AND field_name = $2`,
			ciID, field).Scan(&source); err != nil {
			t.Errorf("provenance of %s: %v", field, err)
		}
		return source
	}
	// refusedForClient2 checks that a principal of client 2 neither sees nor
	// resolves the item of client 1.
	refusedForClient2 := func(it openItem, serial string) {
		t.Helper()
		if w := resolveAs(t, mux, client2, it.id, `{"action":"new"}`); w.Code != http.StatusNotFound {
			t.Errorf("client 2 resolves the %s item of client 1: %d %s, want 404", serial, w.Code, w.Body.String())
		}
		if n := liveCIs(serial); n != 0 {
			t.Fatalf("CIs of %s after the refused resolution: %d", serial, n)
		}
	}

	t.Run("resurrected_device", func(t *testing.T) {
		const serial, uuid, mac = "WP369-RES", "4c4c4544-0a01-4369-8051-b4c04f4a0a01", "00:1b:21:a3:0a:01"
		gone := f.CI(t, f.OrgA, f.Client1, "wp369-gone")
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = $2, hardware_uuid = $3, primary_mac = $4 WHERE id = $1`,
			gone, serial, uuid, mac); err != nil {
			t.Fatal(err)
		}
		if err := cis.Delete(org, f.OrgA, gone); err != nil {
			t.Fatal(err)
		}
		if resp := ingestRecords(t, mux, org, record("server", serial, uuid, mac, "192.0.2.161")); resp.Resurrected != 1 || resp.ReviewItems != 1 {
			t.Fatalf("deleted device seen again: %+v", resp)
		}
		it := open(discovery.ReviewKindResurrectedDevice, serial)
		checkPayload(it, serial, uuid, mac, "")
		if it.client != f.Client1 {
			t.Errorf("item client_id = %q, want %s", it.client, f.Client1)
		}
		refusedForClient2(it, serial)
		// A principal of client 1 creates the CI of its client (E-09).
		if w := resolveAs(t, mux, client1, it.id, `{"action":"new"}`); w.Code != http.StatusOK {
			t.Fatalf("client 1 resolves new: %d %s", w.Code, w.Body.String())
		}
		c := created(serial)
		if c.client != f.Client1 || c.uuid != uuid || c.mac != mac || c.source != "snmp" || c.fingerprintUUID != uuid {
			t.Errorf("new CI %+v, want client %s, hardware UUID %s, MAC %s, source snmp, fingerprint", c, f.Client1, uuid, mac)
		}
		if source := provenanceOf(c.id, "serial_number"); source != "snmp" {
			t.Errorf("provenance of serial_number: %q, want snmp", source)
		}
		if _, err := ci.NewPGRepository(f.App).GetByID(client2, f.OrgA, c.id); err == nil {
			t.Errorf("a principal of client 2 reads the new CI of client 1 (TEN-05)")
		}
		if resp := ingestRecords(t, mux, org, record("server", serial, uuid, mac, "192.0.2.161")); resp.Updated != 1 || resp.Created != 0 ||
			resp.Resurrected != 0 || resp.ReviewItems != 0 {
			t.Errorf("next sighting: %+v, want the new CI updated", resp)
		}
	})

	t.Run("ambiguous_identity", func(t *testing.T) {
		const serial, uuid, mac = "WP369-AMB", "4c4c4544-0a02-4369-8051-b4c04f4a0a02", "00:1b:21:a3:0a:02"
		holder := f.CI(t, f.OrgA, f.Client1, "wp369-amb-holder")
		sharer := f.CI(t, f.OrgA, f.Client1, "wp369-amb-sharer")
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, holder, mac); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET attributes = jsonb_build_object('fingerprint',
			jsonb_build_object('mac_addresses', jsonb_build_array($2::text))) WHERE id = $1`, sharer, mac); err != nil {
			t.Fatal(err)
		}
		if resp := ingestRecords(t, mux, org, record("server", serial, uuid, mac, "192.0.2.162")); resp.Conflicts != 1 || resp.ReviewItems != 1 {
			t.Fatalf("ambiguous device: %+v", resp)
		}
		it := open(discovery.ReviewKindAmbiguousIdentity, serial)
		checkPayload(it, serial, uuid, mac, "")
		if it.client != f.Client1 {
			t.Errorf("item client_id = %q, want %s", it.client, f.Client1)
		}
		refusedForClient2(it, serial)
		if w := resolveAs(t, mux, org, it.id, `{"action":"new"}`); w.Code != http.StatusOK {
			t.Fatalf("resolve new: %d %s", w.Code, w.Body.String())
		}
		// The MAC stays with the CI that holds it (unique per organization).
		c := created(serial)
		if c.client != f.Client1 || c.uuid != uuid || c.mac != "" || c.source != "snmp" {
			t.Errorf("new CI %+v, want client %s, hardware UUID %s, no primary MAC, source snmp", c, f.Client1, uuid)
		}
		if resp := ingestRecords(t, mux, org, record("server", serial, uuid, mac, "192.0.2.162")); resp.Updated != 1 || resp.Created != 0 ||
			resp.Conflicts != 0 || resp.ReviewItems != 0 {
			t.Errorf("next sighting: %+v, want the new CI updated", resp)
		}
	})

	t.Run("unclassified_device", func(t *testing.T) {
		const serial, uuid, mac = "WP369-UNC", "4c4c4544-0a03-4369-8051-b4c04f4a0a03", "00:1b:21:a3:0a:03"
		sighting := func() plugins.DeviceRecord {
			r := record("wp369-gadget", serial, uuid, mac, "192.0.2.163")
			r.SiteID = site
			return r
		}
		if resp := ingestRecords(t, mux, org, sighting()); resp.ReviewItems != 1 || resp.Created != 0 {
			t.Fatalf("device of an unknown type: %+v", resp)
		}
		it := open(discovery.ReviewKindUnclassifiedDevice, serial)
		checkPayload(it, serial, uuid, mac, site)
		if it.client != f.Client1 || it.site != site || it.payload["reason"] != "unknown_ci_type" {
			t.Errorf("item client_id %q, site_id %q, reason %v; want %s, %s, unknown_ci_type", it.client, it.site, it.payload["reason"], f.Client1, site)
		}
		// The type is created; then the device can become a CI.
		var gadget string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'wp369-gadget', 'wp369-gadget') RETURNING id::text`,
			f.OrgA).Scan(&gadget); err != nil {
			t.Fatal(err)
		}
		refusedForClient2(it, serial)
		if w := resolveAs(t, mux, client1, it.id, `{"action":"new"}`); w.Code != http.StatusOK {
			t.Fatalf("client 1 resolves new: %d %s", w.Code, w.Body.String())
		}
		c := created(serial)
		if c.client != f.Client1 || c.site != site || c.location != site || c.typeID != gadget || c.uuid != uuid || c.mac != mac {
			t.Errorf("new CI %+v, want client %s, site and location %s, type %s, hardware UUID %s, MAC %s", c, f.Client1, site, gadget, uuid, mac)
		}
		if resp := ingestRecords(t, mux, org, sighting()); resp.Updated != 1 || resp.Created != 0 || resp.ReviewItems != 0 {
			t.Errorf("next sighting: %+v, want the new CI updated", resp)
		}
	})
}

// TestReviewNewOrgWideSighting covers WP-369 (CI-13, E-09): an org-wide
// sighting (payload client_id "") of a deleted org-wide CI becomes an
// org-wide CI. A principal restricted to a client may not write it: the
// resolution is refused and leaves neither a CI nor a resolved item.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReviewNewOrgWideSighting(t *testing.T) {
	f := scopetest.Seed(t, "0b")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).RegisterRoutes(mux)
	user := tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}
	org := tenant.WithTenant(f.OrgCtx(f.OrgA), user)
	client1 := tenant.WithTenant(f.ClientCtx(f.Client1), user)

	gone := f.CI(t, f.OrgA, "", "wp369-orgwide-gone")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET serial_number = 'WP369-OW' WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	if err := cis.Delete(org, f.OrgA, gone); err != nil {
		t.Fatal(err)
	}
	if resp := ingestRecords(t, mux, org, resurrectRecord("WP369-OW")); resp.Resurrected != 1 {
		t.Fatalf("deleted device seen again: %+v", resp)
	}
	var id, column string
	var recorded *string
	if err := f.Admin.QueryRow(bg, `SELECT id::text, COALESCE(client_id::text, ''), payload->>'client_id' FROM review_item
		WHERE organization_id = $1 AND kind = 'resurrected_device' AND status = 'open' AND $2::uuid = ANY(candidate_ci_ids)`,
		f.OrgA, gone).Scan(&id, &column, &recorded); err != nil {
		t.Fatal(err)
	}
	if column != "" || recorded == nil || *recorded != "" {
		t.Fatalf("item of an org-wide sighting: client_id %q, payload client_id %v; want none and \"\"", column, recorded)
	}
	state := func() (status string, live int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT (SELECT status FROM review_item WHERE id = $1),
			(SELECT count(*) FROM ci WHERE organization_id = $2 AND serial_number = 'WP369-OW' AND deleted_at IS NULL)`, id, f.OrgA).
			Scan(&status, &live); err != nil {
			t.Fatal(err)
		}
		return status, live
	}
	if w := resolveAs(t, mux, client1, id, `{"action":"new"}`); w.Code != http.StatusForbidden {
		t.Errorf("a principal of client 1 creates an org-wide CI: %d %s, want 403 (E-09)", w.Code, w.Body.String())
	}
	if status, live := state(); status != "open" || live != 0 {
		t.Fatalf("after the refused resolution: item %s, %d CIs; want open and none", status, live)
	}
	if w := resolveAs(t, mux, org, id, `{"action":"new"}`); w.Code != http.StatusOK {
		t.Fatalf("resolve new: %d %s", w.Code, w.Body.String())
	}
	var client string
	if err := f.Admin.QueryRow(bg, `SELECT COALESCE(client_id::text, '') FROM ci WHERE organization_id = $1 AND serial_number = 'WP369-OW'
		AND deleted_at IS NULL`, f.OrgA).Scan(&client); err != nil || client != "" {
		t.Errorf("new CI of an org-wide sighting: client_id %q, %v; want org-wide", client, err)
	}
	if status, live := state(); status != "resolved" || live != 1 {
		t.Errorf("after new: item %s, %d CIs", status, live)
	}
}

// TestReviewNewClientFromColumnOrPayload covers WP-369 (CI-13, TEN-05): the
// client column of the item (WP-342) decides the client of the new CI, the
// payload only without one; an item whose payload does not keep the client
// (before WP-369) takes the client its candidate CIs share, otherwise new
// is refused with 422 and the item stays open. An item without a client a
// principal of client 1 may not resolve (E-09) leaves no CI of client 1
// behind: the CI and the resolution commit together.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestReviewNewClientFromColumnOrPayload(t *testing.T) {
	f := scopetest.Seed(t, "0c")
	bg := context.Background()
	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), cis).RegisterRoutes(mux)
	user := tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User}
	org := tenant.WithTenant(f.OrgCtx(f.OrgA), user)
	client1 := tenant.WithTenant(f.ClientCtx(f.Client1), user)
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	deleted := func(clientID, name string) string {
		id := f.CI(t, f.OrgA, clientID, name)
		if err := cis.Delete(org, f.OrgA, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	for _, tc := range []struct {
		name, kind, column string
		// recorded is the payload client_id; nil leaves it out (an item
		// from before WP-369).
		recorded   *string
		candidates []string
		// wantColumn is the client column the database keeps or derives.
		wantColumn, wantClient string
		status                 int
		// client1Refused: a principal of client 1 tries first and is
		// refused (403).
		client1Refused bool
	}{
		{name: "column over payload, from the candidates", kind: discovery.ReviewKindResurrectedDevice, recorded: &f.Client2,
			candidates: []string{deleted(f.Client1, "wp369-c-gone")}, wantColumn: f.Client1, wantClient: f.Client1, status: http.StatusOK},
		{name: "column over payload, without candidates", kind: discovery.ReviewKindUnclassifiedDevice, column: f.Client1, recorded: &f.Client2,
			wantColumn: f.Client1, wantClient: f.Client1, status: http.StatusOK},
		{name: "payload without column", kind: discovery.ReviewKindAmbiguousIdentity, recorded: &f.Client1,
			candidates: []string{f.CI(t, f.OrgA, "", "wp369-p-orgwide")}, wantClient: f.Client1, status: http.StatusOK, client1Refused: true},
		{name: "before WP-369, candidates of client 1", kind: discovery.ReviewKindAmbiguousIdentity,
			candidates: []string{f.CI(t, f.OrgA, f.Client1, "wp369-l-c1a"), f.CI(t, f.OrgA, f.Client1, "wp369-l-c1b")},
			wantColumn: f.Client1, wantClient: f.Client1, status: http.StatusOK},
		{name: "before WP-369, org-wide candidates", kind: discovery.ReviewKindAmbiguousIdentity,
			candidates: []string{f.CI(t, f.OrgA, "", "wp369-l-owa"), f.CI(t, f.OrgA, "", "wp369-l-owb")}, status: http.StatusOK},
		{name: "before WP-369, candidates of two clients", kind: discovery.ReviewKindAmbiguousIdentity,
			candidates: []string{f.CI(t, f.OrgA, f.Client1, "wp369-l-m1"), f.CI(t, f.OrgA, f.Client2, "wp369-l-m2")},
			status:     http.StatusUnprocessableEntity},
		{name: "before WP-369, without candidates", kind: discovery.ReviewKindUnclassifiedDevice, status: http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			name := "wp369-new-" + strings.ReplaceAll(tc.name, " ", "-")
			payload := map[string]any{"ci_type_name": serverType, "name": name}
			if tc.kind == discovery.ReviewKindUnclassifiedDevice {
				payload["reason"] = "unknown_ci_type"
			}
			if tc.recorded != nil {
				payload["client_id"] = *tc.recorded
			}
			raw, err := json.Marshal(payload)
			if err != nil {
				t.Fatal(err)
			}
			candidates := append([]string{}, tc.candidates...)
			var id, column string
			if err = f.Admin.QueryRow(bg, `INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids, client_id)
				VALUES ($1, $2, 'open', $3::jsonb, $4::uuid[], NULLIF($5, '')::uuid) RETURNING id::text, COALESCE(client_id::text, '')`,
				f.OrgA, tc.kind, string(raw), candidates, tc.column).Scan(&id, &column); err != nil {
				t.Fatal(err)
			}
			if column != tc.wantColumn {
				t.Fatalf("client_id column %q, want %q", column, tc.wantColumn)
			}
			var status string
			var clients []string
			state := func() {
				t.Helper()
				if err := f.Admin.QueryRow(bg, `SELECT (SELECT status FROM review_item WHERE id = $1),
					ARRAY(SELECT COALESCE(client_id::text, '') FROM ci WHERE organization_id = $2 AND name = $3 AND deleted_at IS NULL)`,
					id, f.OrgA, name).Scan(&status, &clients); err != nil {
					t.Fatal(err)
				}
			}
			if tc.client1Refused {
				// The CI of client 1 would be allowed, the resolution of an
				// item without a client is not.
				if w := resolveAs(t, mux, client1, id, `{"action":"new"}`); w.Code != http.StatusForbidden {
					t.Errorf("client 1 resolves an item without a client: %d %s, want 403", w.Code, w.Body.String())
				}
				if state(); status != "open" || len(clients) != 0 {
					t.Fatalf("after the refused resolution: item %s, CIs %v; want open and none", status, clients)
				}
			}
			w := resolveAs(t, mux, org, id, `{"action":"new"}`)
			if w.Code != tc.status {
				t.Fatalf("resolve new: %d %s, want %d", w.Code, w.Body.String(), tc.status)
			}
			state()
			if tc.status != http.StatusOK {
				if !strings.Contains(w.Body.String(), "dismiss it and wait for the next sighting") || status != "open" || len(clients) != 0 {
					t.Errorf("refused new: %s; item %s, CIs %v; want the hint, the item open and no CI", w.Body.String(), status, clients)
				}
				return
			}
			if status != "resolved" || len(clients) != 1 || clients[0] != tc.wantClient {
				t.Errorf("item %s, clients of the new CI %v; want resolved and [%s]", status, clients, tc.wantClient)
			}
		})
	}
}

// TestReviewNewClientWithoutDatabase covers the client rules of WP-369
// (CI-13) on the memory repositories, which derive no client column: the
// column of the item, else the payload, else the client all candidate CIs
// share; candidates of two clients or one that cannot be read are refused.
func TestReviewNewClientWithoutDatabase(t *testing.T) {
	bg := context.Background()
	cis := ci.NewMemoryRepository()
	add := func(client string) string {
		item := &ci.Item{OrganizationID: "org-1", CITypeID: "server", Name: "candidate of " + client, ClientID: client}
		if err := cis.Create(bg, item); err != nil {
			t.Fatal(err)
		}
		return item.ID
	}
	a1, b1, a2 := add("client-1"), add("client-1"), add("client-2")
	repo := discovery.NewMemoryRepository()
	repo.SeedCIType("server", "server")
	mux := chi.NewRouter()
	discovery.NewHandler(repo, cis).RegisterRoutes(mux)
	ctx := tenant.WithTenant(bg, tenant.TenantInfo{OrganizationID: "org-1"})
	for _, tc := range []struct {
		name, column string
		// recorded is the payload client_id; nil leaves it out.
		recorded   any
		candidates []string
		status     int
		client     string
	}{
		{"column over payload", "client-1", "client-2", []string{a1}, http.StatusOK, "client-1"},
		{"payload without column", "", "client-2", []string{a1}, http.StatusOK, "client-2"},
		{"org-wide sighting", "", "", []string{a1}, http.StatusOK, ""},
		{"before WP-369, candidates of client 1", "", nil, []string{a1, b1}, http.StatusOK, "client-1"},
		{"before WP-369, candidates of two clients", "", nil, []string{a1, a2}, http.StatusUnprocessableEntity, ""},
		{"before WP-369, candidate not readable", "", nil, []string{"gone"}, http.StatusUnprocessableEntity, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := map[string]any{"ci_type_name": "server", "name": "new: " + tc.name}
			if tc.recorded != nil {
				payload["client_id"] = tc.recorded
			}
			item := &discovery.ReviewItem{OrganizationID: "org-1", Kind: discovery.ReviewKindAmbiguousIdentity, Status: discovery.ReviewStatusOpen,
				ClientID: tc.column, CandidateCIIDs: tc.candidates, Payload: payload}
			if err := repo.CreateReviewItem(bg, item); err != nil {
				t.Fatal(err)
			}
			if w := resolveAs(t, mux, ctx, item.ID, `{"action":"new"}`); w.Code != tc.status {
				t.Fatalf("resolve new: %d %s, want %d", w.Code, w.Body.String(), tc.status)
			}
			all, _, err := cis.List(bg, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			var clients []string
			for _, c := range all {
				if c.Name == "new: "+tc.name {
					clients = append(clients, c.ClientID)
				}
			}
			want := []string{tc.client}
			if tc.status != http.StatusOK {
				want = nil
			}
			if !slices.Equal(clients, want) {
				t.Errorf("clients of the new CI %q, want %q", clients, want)
			}
		})
	}
}
