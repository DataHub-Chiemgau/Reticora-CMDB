package discovery_test

// Resolution new of an unclassified_device item of a device whose type the
// ingest could not resolve (WP-528, UI-16; binding retest of F-176): the
// recorded type name is resolved as the ingest resolves it, a ci_type_id of
// the resolution takes precedence, and without a type that resolves new is
// 422 naming ci_type_id with the item left open. The CI is created on the
// path of WP-369 with the client of the item; a principal of another client
// gets 404.
//
// The test runs only against PostgreSQL (TEST_DATABASE_URL, see
// scopetest.Seed):
//   cd backend && TEST_DATABASE_URL=... go test -count=1 -run TestReviewNewUnclassifiedDeviceAgainstPostgres ./internal/discovery/

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestReviewNewUnclassifiedDeviceAgainstPostgres(t *testing.T) {
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

	// sighting ingests a device of client 1 of a type the organization does
	// not know and returns its unclassified_device item.
	sighting := func(ciType, serial string) string {
		t.Helper()
		r := plugins.RecordFromResult("snmp", &plugins.Result{CIType: ciType, Name: "wp528-" + serial, IP: "192.0.2.201", Serial: serial},
			time.Now().Add(-time.Minute))
		r.ClientID = f.Client1
		if resp := ingestRecords(t, mux, org, r); resp.ReviewItems != 1 || resp.Created != 0 {
			t.Fatalf("device of the unknown type %s: %+v", ciType, resp)
		}
		var id, client, reason string
		if err := f.Admin.QueryRow(bg, `SELECT id::text, COALESCE(client_id::text, ''), COALESCE(payload->>'reason', '') FROM review_item
			WHERE organization_id = $1 AND kind = 'unclassified_device' AND status = 'open' AND payload->>'serial_number' = $2`,
			f.OrgA, serial).Scan(&id, &client, &reason); err != nil {
			t.Fatalf("unclassified_device item of %s: %v", serial, err)
		}
		if client != f.Client1 || reason != "unknown_ci_type" {
			t.Fatalf("item of %s: client_id %q, reason %q; want %s and unknown_ci_type", serial, client, reason, f.Client1)
		}
		return id
	}
	status := func(itemID string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM review_item WHERE id = $1`, itemID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	type newCI struct{ id, client, typeID, reviewItem string }
	// created returns the live CIs of serial.
	created := func(serial string) []newCI {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT id::text, COALESCE(client_id::text, ''), ci_type_id::text, COALESCE(attributes->>'review_item_id', '')
			FROM ci WHERE organization_id = $1 AND serial_number = $2 AND deleted_at IS NULL`, f.OrgA, serial)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []newCI
		for rows.Next() {
			var c newCI
			if err := rows.Scan(&c.id, &c.client, &c.typeID, &c.reviewItem); err != nil {
				t.Fatal(err)
			}
			out = append(out, c)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// refused checks a 422 naming ci_type_id that leaves the item open and
	// creates no CI.
	refused := func(itemID, serial, body string) {
		t.Helper()
		w := resolveAs(t, mux, client1, itemID, body)
		var problem struct {
			Field  string `json:"field"`
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &problem)
		if w.Code != http.StatusUnprocessableEntity || problem.Field != "ci_type_id" || problem.Detail == "" {
			t.Errorf("resolve %s: %d %s, want 422 with field ci_type_id", body, w.Code, w.Body.String())
		}
		if s := status(itemID); s != "open" {
			t.Errorf("item after the refused resolution %s: %s, want open", body, s)
		}
		if c := created(serial); len(c) != 0 {
			t.Errorf("CIs after the refused resolution %s: %+v", body, c)
		}
	}
	// notForClient2 checks that a principal of client 2 neither sees nor
	// resolves the item of client 1.
	notForClient2 := func(itemID, serial, body string) {
		t.Helper()
		if w := resolveAs(t, mux, client2, itemID, body); w.Code != http.StatusNotFound {
			t.Errorf("client 2 resolves the item of client 1: %d %s, want 404", w.Code, w.Body.String())
		}
		if s := status(itemID); s != "open" {
			t.Errorf("item after the resolution of client 2: %s, want open", s)
		}
		if c := created(serial); len(c) != 0 {
			t.Errorf("CIs after the resolution of client 2: %+v", c)
		}
	}
	var serverType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}

	t.Run("type name resolvable by now", func(t *testing.T) {
		const serial = "WP528-NAME"
		itemID := sighting("wp528-widget", serial)
		var widget string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'wp528-widget', 'wp528-widget') RETURNING id::text`,
			f.OrgA).Scan(&widget); err != nil {
			t.Fatal(err)
		}
		notForClient2(itemID, serial, `{"action":"new"}`)
		if w := resolveAs(t, mux, client1, itemID, `{"action":"new"}`); w.Code != http.StatusOK {
			t.Fatalf("client 1 resolves new: %d %s", w.Code, w.Body.String())
		}
		c := created(serial)
		if len(c) != 1 || c[0].typeID != widget || c[0].client != f.Client1 || c[0].reviewItem != itemID {
			t.Fatalf("CIs of %s: %+v, want one of type %s and client %s", serial, c, widget, f.Client1)
		}
		if s := status(itemID); s != "resolved" {
			t.Errorf("item after new: %s, want resolved", s)
		}
	})

	t.Run("no resolvable type, then ci_type_id", func(t *testing.T) {
		const serial = "WP528-CHOSEN"
		itemID := sighting("wp528-mystery", serial)
		refused(itemID, serial, `{"action":"new"}`)
		refused(itemID, serial, `{"action":"new","ci_type_id":"server"}`)
		refused(itemID, serial, `{"action":"new","ci_type_id":"c0de0a00-0000-4000-8000-00000000dead"}`)
		// A private type of another organization is as unknown (TEN-09).
		var foreign string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name) VALUES ($1, 'wp528-foreign', 'wp528-foreign') RETURNING id::text`,
			f.OrgB).Scan(&foreign); err != nil {
			t.Fatal(err)
		}
		refused(itemID, serial, `{"action":"new","ci_type_id":"`+foreign+`"}`)
		notForClient2(itemID, serial, `{"action":"new","ci_type_id":"`+serverType+`"}`)
		if w := resolveAs(t, mux, client1, itemID, `{"action":"new","ci_type_id":"`+serverType+`"}`); w.Code != http.StatusOK {
			t.Fatalf("client 1 resolves new with ci_type_id: %d %s", w.Code, w.Body.String())
		}
		c := created(serial)
		if len(c) != 1 || c[0].typeID != serverType || c[0].client != f.Client1 || c[0].reviewItem != itemID {
			t.Fatalf("CIs of %s: %+v, want one of type %s and client %s", serial, c, serverType, f.Client1)
		}
		if s := status(itemID); s != "resolved" {
			t.Errorf("item after new: %s, want resolved", s)
		}
	})
}
