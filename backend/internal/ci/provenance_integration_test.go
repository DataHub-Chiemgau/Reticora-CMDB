package ci_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestProvenanceAgainstPostgres covers REC-10 (WP-198) against PostgreSQL:
// GET /cis/{id}/provenance answers every typed field of the CI with its
// source, time, rank and the reason the value is shown: an observed value
// by its source rank, a manual override with author and reason over the
// observed value it covers, and the other fields from the CI record.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestProvenanceAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "be")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	repo := ci.NewPGRepository(f.App)
	fields := override.NewPGRepository(f.App)

	id := f.CI(t, f.OrgA, f.Client1, "prov-1")
	if _, err := f.Admin.Exec(bg, `UPDATE ci SET manufacturer = 'Cisco', model = 'C9300', firmware_version = '17.9',
		discovery_source = 'snmp', is_manual = false WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	observed := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
	if _, err := fields.RecordDiscovered(ctx, f.OrgA, id, "firmware_version", "17.9", "snmp", observed); err != nil {
		t.Fatal(err)
	}
	if _, err := fields.RecordDiscovered(ctx, f.OrgA, id, "model", "C9300-48P", "redfish", observed); err != nil {
		t.Fatal(err)
	}
	if _, err := fields.SetOverride(ctx, f.OrgA, id, "model", "C9300-48P-E", f.User, "Typenschild abgelesen", true); err != nil {
		t.Fatal(err)
	}

	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(repo)).WithProvenance(fields).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+id+"/provenance", http.NoBody).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("provenance: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Data []ci.FieldProvenance `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	byField := map[string]ci.FieldProvenance{}
	for _, p := range body.Data {
		byField[p.Field] = p
	}

	// Observed: source, rank and the observation time.
	fw := byField["firmware_version"]
	if fw.Reason != ci.ReasonSourceRank || fw.Source != "snmp" || fw.Rank != override.SourceRank("snmp") ||
		fw.Value != "17.9" || fw.ObservedAt == nil || !fw.ObservedAt.Equal(observed) || fw.RecordedAt == nil {
		t.Errorf("firmware_version = %+v", fw)
	}
	// Override: wins with author and reason, the covered observation stays visible.
	model := byField["model"]
	if model.Reason != ci.ReasonOverride || model.Value != "C9300-48P-E" || model.OverrideReason != "Typenschild abgelesen" ||
		model.OverrideAuthor != f.User || !model.Protected || model.DiscoveredValue != "C9300-48P" ||
		model.DiscoveredSource != "redfish" || model.RecordedAt == nil || model.Rank != 100 {
		t.Errorf("model = %+v", model)
	}
	// Typed fields without a provenance row come from the CI record.
	for _, field := range []string{"name", "status", "manufacturer"} {
		p, ok := byField[field]
		if !ok || p.Reason != ci.ReasonRecord || p.Source != "snmp" || p.RecordedAt == nil {
			t.Errorf("%s = %+v (present %v)", field, p, ok)
		}
	}
	if byField["manufacturer"].Value != "Cisco" {
		t.Errorf("manufacturer value = %v", byField["manufacturer"].Value)
	}
	// Empty typed fields are not listed.
	if _, ok := byField["fqdn"]; ok {
		t.Error("fqdn without a value is listed")
	}

	// A CI of another organization is not found.
	other := f.CI(t, f.OrgB, "", "prov-other")
	w = httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+other+"/provenance", http.NoBody).WithContext(ctx))
	if w.Code != http.StatusNotFound {
		t.Errorf("foreign CI: %d", w.Code)
	}
}

// TestProvenanceOverrideAuthorName covers F-349 (UI-15, REC-10; WP-850)
// against PostgreSQL: GET /cis/{id}/provenance names the author of an
// override, the display name of a user and the name of a service account,
// resolved on read in the reader's tenant transaction. An unknown or
// deleted author has no name, the id stays; a client-scoped reader never
// sees the name of a user of another organization.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestProvenanceOverrideAuthorName(t *testing.T) {
	f := scopetest.Seed(t, "be")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	fields := override.NewPGRepository(f.App)

	id := f.CI(t, f.OrgA, f.Client1, "prov-author")
	anna := f.AppUser(t, f.OrgA, "anna")
	deleted := f.AppUser(t, f.OrgA, "gone")
	if _, err := f.Admin.Exec(bg, `DELETE FROM app_user WHERE id = $1`, deleted); err != nil {
		t.Fatal(err)
	}
	foreign := f.AppUser(t, f.OrgB, "foreign")
	account := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO service_account (id, organization_id, name) VALUES ($1, $2, 'wp850-ingest-bot')`,
		account, f.OrgA); err != nil {
		t.Fatal(err)
	}
	unknown := f.ID()
	authors := map[string]string{
		"model": anna, "manufacturer": account, "hostname": unknown, "fqdn": deleted, "os_name": foreign,
	}
	for field, author := range authors {
		if _, err := fields.SetOverride(ctx, f.OrgA, id, field, "v-"+field, author, "WP-850", true); err != nil {
			t.Fatalf("override %s: %v", field, err)
		}
	}
	want := map[string]string{
		"model": "scopetest-be-anna", "manufacturer": "wp850-ingest-bot", "hostname": "", "fqdn": "", "os_name": "",
	}

	// The API: the name next to the unchanged id, 200 also without a name.
	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).WithProvenance(fields).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/cis/"+id+"/provenance", http.NoBody).WithContext(ctx))
	if w.Code != http.StatusOK {
		t.Fatalf("provenance: %d %s", w.Code, w.Body.String())
	}
	var body struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, p := range body.Data {
		field, _ := p["field"].(string)
		name, ok := want[field]
		if !ok {
			continue
		}
		seen++
		if p["override_author"] != authors[field] {
			t.Errorf("%s: override_author = %v, want %s", field, p["override_author"], authors[field])
		}
		got, present := p["override_author_name"]
		switch {
		case name == "" && present:
			t.Errorf("%s: override_author_name = %v, want absent", field, got)
		case name != "" && got != name:
			t.Errorf("%s: override_author_name = %v, want %s", field, got, name)
		}
	}
	if seen != len(want) {
		t.Errorf("provenance lists %d of %d overridden fields: %s", seen, len(want), w.Body.String())
	}

	// A client-scoped reader of the CI's client: the names of its own
	// organization, never one of another organization.
	values, err := fields.ListForCI(f.ClientCtx(f.Client1), f.OrgA, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(values) != len(want) {
		t.Fatalf("client reader sees %d field values, want %d", len(values), len(want))
	}
	for i := range values {
		v := values[i]
		if v.OverrideAuthorName != want[v.FieldName] {
			t.Errorf("client reader %s: name = %q, want %q", v.FieldName, v.OverrideAuthorName, want[v.FieldName])
		}
		if v.OverrideAuthor != authors[v.FieldName] {
			t.Errorf("client reader %s: author = %q, want %q", v.FieldName, v.OverrideAuthor, authors[v.FieldName])
		}
	}
}

// TestProvenanceOverrideAuthorNameMemory: the memory repository (--no-db)
// names the author like PostgreSQL through its resolver, and the
// provenance answers the id alone when there is no name (WP-850).
func TestProvenanceOverrideAuthorNameMemory(t *testing.T) {
	ctx := context.Background()
	names := map[string]string{"org-1/user-1": "Anna Admin"}
	repo := override.NewMemoryRepository().WithAuthorNames(func(_ context.Context, orgID, id string) (string, bool) {
		name, ok := names[orgID+"/"+id]
		return name, ok
	})
	if _, err := repo.SetOverride(ctx, "org-1", "ci-1", "model", "X", "user-1", "Typenschild", true); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetOverride(ctx, "org-1", "ci-1", "os_name", "Y", "user-2", "Typenschild", true); err != nil {
		t.Fatal(err)
	}
	records, err := repo.ListForCI(ctx, "org-1", "ci-1")
	if err != nil {
		t.Fatal(err)
	}
	byField := map[string]ci.FieldProvenance{}
	for _, p := range ci.BuildProvenance(&ci.Item{ID: "ci-1", OrganizationID: "org-1"}, records) {
		byField[p.Field] = p
	}
	if p := byField["model"]; p.OverrideAuthor != "user-1" || p.OverrideAuthorName != "Anna Admin" {
		t.Errorf("model = %+v", p)
	}
	if p := byField["os_name"]; p.OverrideAuthor != "user-2" || p.OverrideAuthorName != "" {
		t.Errorf("os_name = %+v", p)
	}
	raw, err := json.Marshal(byField["os_name"])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "override_author_name") {
		t.Errorf("os_name without a name answers the field: %s", raw)
	}
}
