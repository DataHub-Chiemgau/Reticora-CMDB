package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestIPAMHistoryOnPostgreSQL covers the IPAM change history of IPM-08
// (WP-908) against PostgreSQL:
//   - create, field change and delete of a subnet write create, update
//     (name) and delete rows with source manual and the user as actor; the
//     deleted subnet keeps its history, an unknown one is 404;
//   - a reservation writes a create row with source manual;
//   - a discovery sync with a new, a moved (other VRF) and a removed
//     address writes rows with source discovery and without actor;
//   - an allocation writes source allocation;
//   - the VRF change of a subnet records the moved addresses as well;
//   - a client principal sees only the history of visible objects; UPDATE
//     on ipam_change fails for reticora_app.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIPAMHistoryOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "cc")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	created := func(path, body string) string {
		t.Helper()
		w := call(orgCtx, http.MethodPost, path, body)
		if w.Code != http.StatusCreated {
			t.Fatalf("POST %s %s: %d %s", path, body, w.Code, w.Body.String())
		}
		var v struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &v)
		return v.ID
	}
	history := func(ctx context.Context, path string, want int) []ipam.Change {
		t.Helper()
		w := call(ctx, http.MethodGet, path, "")
		if w.Code != want {
			t.Fatalf("GET %s = %d %s, want %d", path, w.Code, w.Body.String(), want)
		}
		var page api.ListResponse[ipam.Change]
		_ = json.Unmarshal(w.Body.Bytes(), &page)
		return page.Data
	}
	find := func(changes []ipam.Change, changeType, field string) *ipam.Change {
		for i := range changes {
			if changes[i].ChangeType == changeType && changes[i].FieldName == field {
				return &changes[i]
			}
		}
		return nil
	}

	// Create, change and delete a subnet.
	subnet := created("/api/v1/subnets", `{"cidr":"10.205.0.0/24","name":"before","client_id":"`+f.Client1+`"}`)
	if w := call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+subnet, `{"name":"after"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH subnet: %d %s", w.Code, w.Body.String())
	}
	changes := history(orgCtx, "/api/v1/subnets/"+subnet+"/history", http.StatusOK)
	if len(changes) != 2 || changes[0].ChangeType != "update" || changes[1].ChangeType != "create" {
		t.Fatalf("history after create and update = %+v, want update then create", changes)
	}
	update := changes[0]
	if update.FieldName != "name" || string(update.OldValue) != `"before"` || string(update.NewValue) != `"after"` ||
		update.Source != ipam.ChangeSourceManual || update.ActorID != f.User || update.ObjectKind != "subnet" || update.ObjectID != subnet {
		t.Errorf("update row = %+v", update)
	}
	var object map[string]any
	if err := json.Unmarshal(changes[1].NewValue, &object); err != nil || object["cidr"] != "10.205.0.0/24" || object["name"] != "before" {
		t.Errorf("create row value = %s (%v)", changes[1].NewValue, err)
	}
	if w := call(orgCtx, http.MethodDelete, "/api/v1/subnets/"+subnet, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE subnet: %d", w.Code)
	}
	changes = history(orgCtx, "/api/v1/subnets/"+subnet+"/history", http.StatusOK)
	if len(changes) != 3 || changes[0].ChangeType != "delete" || len(changes[0].OldValue) == 0 || changes[0].NewValue != nil {
		t.Errorf("history of the deleted subnet = %+v", changes)
	}
	history(orgCtx, "/api/v1/subnets/"+f.ID()+"/history", http.StatusNotFound)
	history(orgCtx, "/api/v1/subnets/not-a-uuid/history", http.StatusNotFound)

	// A reservation writes create with source manual.
	pool := created("/api/v1/subnets", `{"cidr":"10.205.1.0/24","client_id":"`+f.Client1+`"}`)
	reservation := created("/api/v1/subnets/"+pool+"/reservations", `{"address":"10.205.1.9"}`)
	for _, path := range []string{"/api/v1/ips/", "/api/v1/ip-addresses/"} {
		changes = history(orgCtx, path+reservation+"/history", http.StatusOK)
		if c := find(changes, "create", ""); c == nil || c.Source != ipam.ChangeSourceManual || c.ActorID != f.User || c.ObjectKind != "ip_address" {
			t.Errorf("history of the reservation under %s = %+v", path, changes)
		}
	}

	// An allocation writes source allocation.
	allocated := created("/api/v1/subnets/"+pool+"/allocate", `{}`)
	changes = history(orgCtx, "/api/v1/ips/"+allocated+"/history", http.StatusOK)
	if c := find(changes, "create", ""); c == nil || c.Source != ipam.ChangeSourceAllocation {
		t.Errorf("history of the allocated address = %+v", changes)
	}

	// The VRF change of a subnet records its moved addresses.
	vrf := created("/api/v1/vrfs", `{"name":"history-vrf"}`)
	if w := call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+pool, `{"vrf_id":"`+vrf+`"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH subnet vrf: %d %s", w.Code, w.Body.String())
	}
	changes = history(orgCtx, "/api/v1/ips/"+reservation+"/history", http.StatusOK)
	if c := find(changes, "update", "vrf_id"); c == nil || string(c.NewValue) != `"`+vrf+`"` || c.ActorID != f.User {
		t.Errorf("history of the address after the VRF change of its subnet = %+v", changes)
	}

	// A discovery sync: new, moved (other VRF) and removed addresses.
	ci := f.CI(t, f.OrgA, "", "history-ci")
	discoveryScope := database.OrgWideScope(f.OrgA, "")
	discoveryCtx := database.ContextWithTenantScope(bg, &discoveryScope)
	sync := func(vrfID string, ips ...string) {
		t.Helper()
		if _, err := repo.SyncDiscoveredInterfaces(discoveryCtx, f.OrgA, ci, []ipam.ObservedInterface{
			{Name: "eth0", IPs: ips, VRFID: vrfID}}); err != nil {
			t.Fatalf("sync: %v", err)
		}
	}
	sync("", "10.205.2.5/24", "10.205.2.6/24")
	var discovered, removed string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ip_address WHERE organization_id = $1 AND address_host = '10.205.2.5'`,
		f.OrgA).Scan(&discovered); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ip_address WHERE organization_id = $1 AND address_host = '10.205.2.6'`,
		f.OrgA).Scan(&removed); err != nil {
		t.Fatal(err)
	}
	sync(vrf, "10.205.2.5/24")
	for i := 0; i < ipam.MaxMissedObservations; i++ {
		sync(vrf, "10.205.2.5/24")
	}
	// last_seen_at and miss_count of the repeated syncs are not recorded
	// (E-213, Nachtrag 2026-11-12): create and the VRF move only.
	changes = history(orgCtx, "/api/v1/ips/"+discovered+"/history", http.StatusOK)
	if len(changes) != 2 {
		t.Errorf("history of the discovered address = %+v, want create and the VRF move", changes)
	}
	for _, c := range changes {
		if c.Source != ipam.ChangeSourceDiscovery || c.ActorID != "" || (c.ChangeType == "update" && c.FieldName != "vrf_id") {
			t.Errorf("discovery row = %+v, want source discovery without actor", c)
		}
	}
	changes = history(orgCtx, "/api/v1/ips/"+removed+"/history", http.StatusOK)
	if c := find(changes, "delete", ""); c == nil || c.Source != ipam.ChangeSourceDiscovery || c.ActorID != "" {
		t.Errorf("history of the removed address = %+v", changes)
	}

	// A client principal sees only the history of visible objects.
	other := created("/api/v1/subnets", `{"cidr":"10.205.3.0/24","client_id":"`+f.Client2+`"}`)
	orgWide := created("/api/v1/subnets", `{"cidr":"10.205.4.0/24"}`)
	history(client1Ctx, "/api/v1/subnets/"+other+"/history", http.StatusNotFound)
	if got := history(client1Ctx, "/api/v1/subnets/"+orgWide+"/history", http.StatusOK); len(got) != 1 {
		t.Errorf("client principal: history of an org-wide subnet = %+v", got)
	}
	if got := history(client1Ctx, "/api/v1/subnets/"+pool+"/history", http.StatusOK); len(got) == 0 {
		t.Errorf("client principal: history of its subnet is empty")
	}
	// A deleted address keeps the visibility of the live one: on the
	// interface of an org-wide CI in a subnet of client 2 its rows carry no
	// client (derive_ip_scope follows the interface), yet client 1 saw the
	// live address as 404 and gets 404 for its history after the deletion.
	nicCI := f.CI(t, f.OrgA, "", "history-nic-ci")
	var nic string
	if err := f.Admin.QueryRow(bg, `INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'eth9') RETURNING id::text`,
		f.OrgA, nicCI).Scan(&nic); err != nil {
		t.Fatal(err)
	}
	bound := created("/api/v1/ips", `{"address":"10.205.3.7","subnet_id":"`+other+`","interface_id":"`+nic+`"}`)
	history(client1Ctx, "/api/v1/ips/"+bound+"/history", http.StatusNotFound)
	if w := call(orgCtx, http.MethodDelete, "/api/v1/ips/"+bound, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE address: %d %s", w.Code, w.Body.String())
	}
	history(client1Ctx, "/api/v1/ips/"+bound+"/history", http.StatusNotFound)
	client2Ctx := tenant.WithTenant(f.ClientCtx(f.Client2), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	if got := history(client2Ctx, "/api/v1/ips/"+bound+"/history", http.StatusOK); find(got, "delete", "") == nil {
		t.Errorf("client 2: history of its deleted address = %+v", got)
	}
	// Once the subnet is deleted as well, its deletion row decides.
	if w := call(orgCtx, http.MethodDelete, "/api/v1/subnets/"+other, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE subnet: %d %s", w.Code, w.Body.String())
	}
	history(client1Ctx, "/api/v1/ips/"+bound+"/history", http.StatusNotFound)
	history(client2Ctx, "/api/v1/ips/"+bound+"/history", http.StatusOK)

	var leaked int
	if err := database.WithRequestTenant(f.ClientCtx(f.Client1), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM ipam_change WHERE object_id = $1`, other).Scan(&leaked)
	}); err != nil || leaked != 0 {
		t.Errorf("client principal reads %d rows of another client's subnet (%v)", leaked, err)
	}

	// reticora_app has no UPDATE on ipam_change.
	err := database.WithRequestTenant(f.OrgCtx(f.OrgA), f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE ipam_change SET source = 'import' WHERE organization_id = $1`, f.OrgA)
		return err
	})
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "42501" {
		t.Errorf("UPDATE ipam_change as reticora_app = %v, want permission denied", err)
	}
}

// TestRetentionDeletesOldIPAMChanges: the history run of the retention
// (jobs.Retention.RunHistory) deletes the IPAM history rows before the
// cut-off and keeps the newer ones (IPM-08, AUD-09).
func TestRetentionDeletesOldIPAMChanges(t *testing.T) {
	f := scopetest.Seed(t, "cc")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App)
	s := &ipam.Subnet{OrganizationID: f.OrgA, CIDR: "10.206.0.0/24"}
	if err := repo.CreateSubnet(f.OrgCtx(f.OrgA), s); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO ipam_change (organization_id, object_kind, object_id, change_type, changed_at)
		VALUES ($1, 'subnet', $2, 'update', now() - interval '500 days')`, f.OrgA, s.ID); err != nil {
		t.Fatal(err)
	}
	count := func() (old, recent int) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FILTER (WHERE changed_at < now() - interval '400 days'),
			count(*) FILTER (WHERE changed_at >= now() - interval '400 days') FROM ipam_change WHERE object_id = $1`, s.ID).Scan(&old, &recent); err != nil {
			t.Fatal(err)
		}
		return old, recent
	}
	if old, recent := count(); old != 1 || recent != 1 {
		t.Fatalf("before the retention: %d old, %d recent rows, want 1 and 1", old, recent)
	}
	if err := jobs.NewRetention(f.App, jobs.RetentionPolicy{History: 400 * 24 * time.Hour}).RunHistory(bg); err != nil {
		t.Fatal(err)
	}
	if old, recent := count(); old != 0 || recent != 1 {
		t.Errorf("after the retention: %d old, %d recent rows, want 0 and 1", old, recent)
	}
}
