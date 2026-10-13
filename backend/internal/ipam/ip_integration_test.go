package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestIPAddressesFollowNET03 covers NET-03 (WP-124) against PostgreSQL:
// ip_address has type (discovered | reserved | static | dhcp) instead of
// status, migration 000111 transfers the former statuses, a manual
// address is static and one discovery reports is discovered, and an
// address is unique per organization, VRF and interface (partial indexes
// with vrf_id).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIPAddressesFollowNET03(t *testing.T) {
	f := scopetest.Seed(t, "8e")
	bg := context.Background()

	// Schema: type with its check, no status; partial unique indexes with
	// vrf_id.
	var defs []string
	if err := f.Admin.QueryRow(bg, `SELECT array_agg(d ORDER BY d) FROM (
			SELECT pg_get_constraintdef(oid) AS d FROM pg_constraint WHERE conrelid = 'ip_address'::regclass AND contype = 'c'
			UNION ALL SELECT indexdef FROM pg_indexes WHERE tablename = 'ip_address'
			UNION ALL SELECT 'column ' || column_name FROM information_schema.columns
			WHERE table_schema = 'public' AND table_name = 'ip_address') x`).Scan(&defs); err != nil {
		t.Fatal(err)
	}
	all := strings.Join(defs, "\n")
	for _, want := range []string{
		"CHECK ((type = ANY (ARRAY['discovered'::text, 'reserved'::text, 'static'::text, 'dhcp'::text])))",
		"UNIQUE INDEX uq_ip_address_org_vrf_interface_address ON public.ip_address USING btree (organization_id, vrf_id, interface_id, address) WHERE (interface_id IS NOT NULL)",
		"UNIQUE INDEX uq_ip_address_org_vrf_address_no_interface ON public.ip_address USING btree (organization_id, vrf_id, address) WHERE (interface_id IS NULL)",
		"column type",
	} {
		if !strings.Contains(all, want) {
			t.Errorf("missing %q in\n%s", want, all)
		}
	}
	if strings.Contains(all, "column status") {
		t.Error("former column status still exists")
	}

	// Data transfer of migration 000111: undo it in a transaction, store
	// the former statuses, apply it again.
	t.Run("data transfer", func(t *testing.T) {
		down, err := os.ReadFile("../../migrations/000111_ip_address_net03.down.sql")
		if err != nil {
			t.Fatal(err)
		}
		up, err := os.ReadFile("../../migrations/000111_ip_address_net03.up.sql")
		if err != nil {
			t.Fatal(err)
		}
		tx, err := f.Admin.Begin(bg)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(bg) }()
		if _, err = tx.Exec(bg, string(down)); err != nil {
			t.Fatalf("down: %v", err)
		}
		cases := []struct{ address, status, seen, want string }{
			{"10.81.0.1", "active", "now()", "discovered"},
			{"10.81.0.2", "active", "NULL", "static"},
			{"10.81.0.3", "deprecated", "NULL", "static"},
			{"10.81.0.4", "deprecated", "now()", "discovered"},
			{"10.81.0.5", "reserved", "NULL", "reserved"},
			{"10.81.0.6", "dhcp", "now()", "dhcp"},
			{"10.81.0.7", "available", "NULL", "reserved"},
		}
		for _, c := range cases {
			if _, err = tx.Exec(bg, `INSERT INTO ip_address (organization_id, address, status, last_seen_at)
				VALUES ($1, $2::inet, $3, `+c.seen+`)`, f.OrgA, c.address, c.status); err != nil {
				t.Fatalf("insert %s: %v", c.address, err)
			}
		}
		if _, err = tx.Exec(bg, string(up)); err != nil {
			t.Fatalf("up: %v", err)
		}
		for _, c := range cases {
			var got string
			if err = tx.QueryRow(bg, `SELECT type FROM ip_address WHERE organization_id = $1 AND address = $2::inet`,
				f.OrgA, c.address).Scan(&got); err != nil || got != c.want {
				t.Errorf("%s (%s): type %q (%v), want %s", c.address, c.status, got, err, c.want)
			}
		}
	})

	// API and discovery.
	repo := ipam.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	decode := func(w *httptest.ResponseRecorder) map[string]any {
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}

	w := call(http.MethodPost, "/api/v1/ip-addresses", `{"address":"10.82.0.10"}`)
	manual := decode(w)
	if w.Code != http.StatusCreated || manual["type"] != ipam.IPTypeStatic {
		t.Fatalf("manual address: %d %s, want static", w.Code, w.Body.String())
	}
	if w = call(http.MethodPost, "/api/v1/ip-addresses", `{"address":"10.82.0.11","type":"dhcp"}`); w.Code != http.StatusCreated ||
		decode(w)["type"] != ipam.IPTypeDHCP {
		t.Errorf("dhcp address: %d %s", w.Code, w.Body.String())
	}
	for _, body := range []string{`{"address":"10.82.0.12","type":"active"}`, `{"address":"10.82.0.12","status":"active"}`} {
		if w = call(http.MethodPost, "/api/v1/ip-addresses", body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, w.Code)
		}
	}
	if w = call(http.MethodPatch, "/api/v1/ip-addresses/"+manual["id"].(string), `{"type":"reserved"}`); w.Code != http.StatusOK ||
		decode(w)["type"] != ipam.IPTypeReserved {
		t.Errorf("patch type: %d %s", w.Code, w.Body.String())
	}

	ciID := f.CI(t, f.OrgA, "", "net03-host")
	if _, err := repo.SyncDiscoveredInterfaces(ctx, f.OrgA, ciID, []ipam.ObservedInterface{
		{Name: "eth0", IPs: []string{"10.82.1.5"}},
		{Name: "eth1", IPs: []string{"10.82.1.5"}},
	}); err != nil {
		t.Fatalf("sync: %v", err)
	}
	var discovered int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ip_address WHERE organization_id = $1 AND address = '10.82.1.5'
		AND type = 'discovered' AND last_seen_at IS NOT NULL`, f.OrgA).Scan(&discovered); err != nil || discovered != 2 {
		t.Errorf("discovered addresses: %d (%v), want 2 — one per interface", discovered, err)
	}

	// Unique per organization, VRF and interface.
	var ifID string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM network_interface WHERE ci_id = $1 AND name = 'eth0'`, ciID).Scan(&ifID); err != nil {
		t.Fatal(err)
	}
	if w = call(http.MethodPost, "/api/v1/ip-addresses", `{"address":"10.82.1.5","interface_id":"`+ifID+`"}`); w.Code < 400 {
		t.Errorf("same address on the same interface: %d, want an error", w.Code)
	}
	if w = call(http.MethodPost, "/api/v1/ip-addresses", `{"address":"10.82.0.10"}`); w.Code < 400 {
		t.Errorf("same address without interface twice: %d, want an error", w.Code)
	}
}
