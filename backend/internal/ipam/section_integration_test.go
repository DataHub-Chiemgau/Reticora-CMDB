package ipam_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// TestIPAMSectionsOnPostgreSQL covers IPM-01 (WP-900, E-209) against
// PostgreSQL:
//   - every organization has the org-wide default section "default", a new
//     one included (trigger), and the backfill of migration 000298 moves
//     existing subnets into it;
//   - POST /subnets without section_id lands in the default section;
//   - a section with subnets and the default section cannot be deleted
//     (409), a duplicate name is 409;
//   - a client principal sees only the sections of its client and the
//     org-wide ones, creates no org-wide section (403) and puts no subnet
//     into a section of another client (422 section_id);
//   - a principal restricted to sites reads the org-wide sections but
//     creates (403), changes and deletes (404) none;
//   - a client that owns a section is not deleted (409 id).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIPAMSectionsOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	bg := context.Background()
	repo := ipam.NewPGRepository(f.App)
	mux := chi.NewRouter()
	ipam.NewHandler(repo).RegisterRoutes(mux)
	call := func(ctx context.Context, method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, bytes.NewBufferString(body)).WithContext(ctx)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	str := func(q string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, q, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		return out
	}
	decode := func(w *httptest.ResponseRecorder, v any) {
		t.Helper()
		if err := json.Unmarshal(w.Body.Bytes(), v); err != nil {
			t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	create := func(ctx context.Context, body string, want int) ipam.Section {
		t.Helper()
		w := call(ctx, http.MethodPost, "/api/v1/ipam-sections", body)
		if w.Code != want {
			t.Fatalf("POST /ipam-sections %s = %d %s, want %d", body, w.Code, w.Body.String(), want)
		}
		var s ipam.Section
		if want == http.StatusCreated {
			decode(w, &s)
		}
		return s
	}

	// The default section of the seeded organization (trigger on insert).
	defaultA := str(`SELECT id::text FROM ipam_section WHERE organization_id = $1 AND is_default AND name = 'default' AND client_id IS NULL`, f.OrgA)
	if n := str(`SELECT count(*)::text FROM ipam_section WHERE organization_id = $1 AND is_default`, f.OrgB); n != "1" {
		t.Errorf("organization B has %s default sections, want 1", n)
	}
	w := call(orgCtx, http.MethodGet, "/api/v1/ipam-sections", "")
	if w.Code != http.StatusOK {
		t.Fatalf("GET /ipam-sections = %d %s", w.Code, w.Body.String())
	}
	var list struct {
		Data []ipam.Section `json:"data"`
	}
	decode(w, &list)
	if len(list.Data) == 0 || list.Data[0].ID != defaultA || !list.Data[0].IsDefault || !list.Data[0].ShowVLAN || list.Data[0].StrictMode {
		t.Fatalf("sections of org A = %+v, want the default section %s first", list.Data, defaultA)
	}

	// POST /subnets without section_id lands in the default section.
	w = call(orgCtx, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.201.0.0/24","client_id":"`+f.Client1+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("POST /subnets = %d %s", w.Code, w.Body.String())
	}
	var subnet ipam.Subnet
	decode(w, &subnet)
	if subnet.SectionID != defaultA {
		t.Errorf("subnet without section in %q, want the default section %q", subnet.SectionID, defaultA)
	}

	// Sections of a client and org-wide ones; a duplicate name is 409.
	sec1 := create(orgCtx, `{"name":"c9-client1","client_id":"`+f.Client1+`","strict_mode":true,"show_vrf":false}`, http.StatusCreated)
	if sec1.ClientID != f.Client1 || !sec1.StrictMode || sec1.ShowVRF || !sec1.ShowVLAN || sec1.IsDefault {
		t.Errorf("client section = %+v", sec1)
	}
	sec2 := create(orgCtx, `{"name":"c9-client2","client_id":"`+f.Client2+`"}`, http.StatusCreated)
	wide := create(orgCtx, `{"name":"c9-wide"}`, http.StatusCreated)
	create(orgCtx, `{"name":"c9-wide"}`, http.StatusConflict)

	// A subnet of client 1 in the section of client 2: 422 section_id; in
	// its own and in an org-wide section accepted.
	w = call(orgCtx, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.201.1.0/24","client_id":"`+f.Client1+`","section_id":"`+sec2.ID+`"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "section_id") {
		t.Errorf("subnet of client 1 in a section of client 2 = %d %s, want 422 section_id", w.Code, w.Body.String())
	}
	w = call(orgCtx, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.201.2.0/24","client_id":"`+f.Client1+`","section_id":"`+sec1.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("subnet in its client's section = %d %s", w.Code, w.Body.String())
	}
	var inSec1 ipam.Subnet
	decode(w, &inSec1)
	w = call(orgCtx, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.201.3.0/24","client_id":"`+f.Client2+`","section_id":"`+wide.ID+`"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("subnet in an org-wide section = %d %s", w.Code, w.Body.String())
	}
	// Moving the subnet of section 1 to client 2 is refused as well.
	w = call(orgCtx, http.MethodPatch, "/api/v1/subnets/"+inSec1.ID, `{"client_id":"`+f.Client2+`"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "section_id") {
		t.Errorf("subnet of section 1 moved to client 2 = %d %s, want 422 section_id", w.Code, w.Body.String())
	}

	// Deleting a section with subnets and the default section: 409.
	if w = call(orgCtx, http.MethodDelete, "/api/v1/ipam-sections/"+sec1.ID, ""); w.Code != http.StatusConflict {
		t.Errorf("DELETE section with subnets = %d %s, want 409", w.Code, w.Body.String())
	}
	if w = call(orgCtx, http.MethodDelete, "/api/v1/ipam-sections/"+defaultA, ""); w.Code != http.StatusConflict {
		t.Errorf("DELETE default section = %d %s, want 409", w.Code, w.Body.String())
	}
	// The default section keeps its flag also against a direct update.
	if _, err := f.Admin.Exec(bg, `UPDATE ipam_section SET is_default = false WHERE id = $1`, defaultA); err == nil {
		t.Error("the default section lost its flag")
	}

	// Client principal: its client's and the org-wide sections only.
	w = call(client1Ctx, http.MethodGet, "/api/v1/ipam-sections?limit=200", "")
	decode(w, &list)
	seen := map[string]bool{}
	for _, s := range list.Data {
		seen[s.ID] = true
	}
	if !seen[defaultA] || !seen[sec1.ID] || !seen[wide.ID] || seen[sec2.ID] {
		t.Errorf("client 1 sees %v, want default, own and org-wide sections, not client 2's", seen)
	}
	if w = call(client1Ctx, http.MethodGet, "/api/v1/ipam-sections/"+sec2.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("GET section of client 2 as client 1 = %d, want 404", w.Code)
	}
	create(client1Ctx, `{"name":"c9-wide-by-client"}`, http.StatusForbidden)
	create(client1Ctx, `{"name":"c9-other-by-client","client_id":"`+f.Client2+`"}`, http.StatusForbidden)
	own := create(client1Ctx, `{"name":"c9-own","client_id":"`+f.Client1+`"}`, http.StatusCreated)
	if w = call(client1Ctx, http.MethodPatch, "/api/v1/ipam-sections/"+wide.ID, `{"name":"c9-renamed"}`); w.Code != http.StatusNotFound {
		t.Errorf("PATCH org-wide section as client 1 = %d, want 404", w.Code)
	}
	if w = call(client1Ctx, http.MethodDelete, "/api/v1/ipam-sections/"+wide.ID, ""); w.Code != http.StatusNotFound {
		t.Errorf("DELETE org-wide section as client 1 = %d, want 404", w.Code)
	}
	w = call(client1Ctx, http.MethodPost, "/api/v1/subnets", `{"cidr":"10.201.4.0/24","client_id":"`+f.Client1+`","section_id":"`+sec2.ID+`"}`)
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "section_id") {
		t.Errorf("client 1 hangs a subnet into a section of client 2 = %d %s, want 422 section_id", w.Code, w.Body.String())
	}
	if w = call(client1Ctx, http.MethodPatch, "/api/v1/ipam-sections/"+own.ID, `{"description":"mine","show_vlan":false}`); w.Code != http.StatusOK {
		t.Errorf("PATCH own section = %d %s", w.Code, w.Body.String())
	}
	if w = call(client1Ctx, http.MethodDelete, "/api/v1/ipam-sections/"+own.ID, ""); w.Code != http.StatusNoContent {
		t.Errorf("DELETE own empty section = %d %s", w.Code, w.Body.String())
	}

	// A principal restricted to sites (no client restriction) reads the
	// sections but writes none: a section has no site (vlan pattern of
	// 000091 with site_id NULL).
	siteScope := database.OrgWideScope(f.OrgA, f.User)
	siteScope.Sites = database.ScopeIDs(f.ID())
	siteCtx := tenant.WithTenant(database.ContextWithTenantScope(bg, &siteScope), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	if w = call(siteCtx, http.MethodGet, "/api/v1/ipam-sections/"+wide.ID, ""); w.Code != http.StatusOK {
		t.Errorf("GET org-wide section as site principal = %d, want 200", w.Code)
	}
	create(siteCtx, `{"name":"c9-wide-by-site"}`, http.StatusForbidden)
	for _, id := range []string{wide.ID, defaultA} {
		if w = call(siteCtx, http.MethodPatch, "/api/v1/ipam-sections/"+id, `{"name":"c9-renamed-by-site","strict_mode":true}`); w.Code != http.StatusNotFound {
			t.Errorf("PATCH org-wide section %s as site principal = %d, want 404", id, w.Code)
		}
		if w = call(siteCtx, http.MethodDelete, "/api/v1/ipam-sections/"+id, ""); w.Code != http.StatusNotFound {
			t.Errorf("DELETE org-wide section %s as site principal = %d, want 404", id, w.Code)
		}
	}
	if got := str(`SELECT name || '/' || strict_mode::text FROM ipam_section WHERE id = $1`, defaultA); got != "default/false" {
		t.Errorf("default section after the site principal's writes = %s, want default/false", got)
	}

	// A client that owns a section stays (409 id instead of not found).
	owner := str(`INSERT INTO client (organization_id, name, slug) VALUES ($1, 'c9 owner', 'c9-owner') RETURNING id::text`, f.OrgA)
	ownerSec := create(orgCtx, `{"name":"c9-owner-section","client_id":"`+owner+`"}`, http.StatusCreated)
	err := tenantapi.NewPGRepository(f.App).DeleteClient(orgCtx, f.OrgA, owner)
	var conflict *api.ConflictError
	if !errors.As(err, &conflict) || conflict.Field != "id" {
		t.Errorf("delete client owning a section = %v, want 409 id", err)
	}
	if w = call(orgCtx, http.MethodDelete, "/api/v1/ipam-sections/"+ownerSec.ID, ""); w.Code != http.StatusNoContent {
		t.Fatalf("DELETE owner section = %d %s", w.Code, w.Body.String())
	}
	if err = tenantapi.NewPGRepository(f.App).DeleteClient(orgCtx, f.OrgA, owner); err != nil {
		t.Errorf("delete client without section: %v", err)
	}

	// A new organization gets its default section at once.
	newOrg := str(`INSERT INTO organization (name, slug) VALUES ('c9 section org', 'c9-section-org-'||substr(md5(random()::text), 1, 6)) RETURNING id::text`)
	t.Cleanup(func() { _, _ = f.Admin.Exec(bg, `DELETE FROM organization WHERE id = $1`, newOrg) })
	if n := str(`SELECT count(*)::text FROM ipam_section WHERE organization_id = $1 AND is_default AND name = 'default'`, newOrg); n != "1" {
		t.Errorf("new organization has %s default sections, want 1", n)
	}
}

// TestMigration350BackfillsSubnetSections runs the subnet backfill of
// migration 000298 on a subnet without section inside a rolled-back
// transaction: the subnet lands in the default section of its organization.
func TestMigration350BackfillsSubnetSections(t *testing.T) {
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	f := scopetest.Seed(t, "c9")
	bg := context.Background()
	raw, err := os.ReadFile("../../migrations/000298_ipam_section.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "UPDATE subnet s SET section_id")
	end := start + strings.Index(text[start:], ";")
	if start < 0 || end < start {
		t.Fatal("backfill of 000298 not found")
	}
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	var subnet, want string
	if err = tx.QueryRow(bg, `INSERT INTO subnet (organization_id, cidr) VALUES ($1, '10.202.0.0/24') RETURNING id::text`, f.OrgA).Scan(&subnet); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER TABLE subnet ALTER COLUMN section_id DROP NOT NULL`,
		`ALTER TABLE subnet DISABLE TRIGGER trg_subnet_default_section`,
		`UPDATE subnet SET section_id = NULL WHERE id = '` + subnet + `'`,
		text[start:end],
	} {
		if _, err = tx.Exec(bg, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	if err = tx.QueryRow(bg, `SELECT s.section_id::text FROM subnet s JOIN ipam_section sec ON sec.id = s.section_id AND sec.is_default
		WHERE s.id = $1`, subnet).Scan(&want); err != nil {
		t.Fatalf("subnet after the backfill is not in the default section: %v", err)
	}
}
