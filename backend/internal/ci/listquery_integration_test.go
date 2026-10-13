package ci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestListFiltersAgainstPostgres covers the list query of API-02 (WP-165)
// on GET /cis against PostgreSQL: filter[field][op] on columns and on
// searchable JSONB attributes with their data type, 400 with a hint for an
// attribute that is not searchable, 422 for unknown fields, sort and
// fields.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestListFiltersAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "ae")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	seed := f.CI(t, f.OrgA, f.Client1, "lq-seed")
	var typeID string
	if err := f.Admin.QueryRow(bg, `SELECT ci_type_id::text FROM ci WHERE id = $1`, seed).Scan(&typeID); err != nil {
		t.Fatal(err)
	}
	// The seeded type may be shared by other tests: the definitions are
	// replaced on a rerun and removed afterwards.
	if _, err := f.Admin.Exec(bg, `INSERT INTO attribute_definition (ci_type_id, key, data_type, searchable)
		VALUES ($1, 'lq_ram_gb', 'integer', true), ($1, 'lq_note', 'text', false)
		ON CONFLICT (ci_type_id, key) DO UPDATE SET data_type = EXCLUDED.data_type, searchable = EXCLUDED.searchable`, typeID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = f.Admin.Exec(bg, `DELETE FROM attribute_definition WHERE ci_type_id = $1 AND key IN ('lq_ram_gb', 'lq_note')`, typeID)
	})

	mux := chi.NewRouter()
	ci.NewHandler(ci.NewService(ci.NewPGRepository(f.App))).RegisterRoutes(mux)
	call := func(method, target string, body any) (int, map[string]any) {
		t.Helper()
		var raw []byte
		if body != nil {
			raw, _ = json.Marshal(body)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, target, bytes.NewReader(raw)).WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for name, ram := range map[string]int{"lq-a": 8, "lq-b": 16, "lq-c": 32} {
		if code, out := call(http.MethodPost, "/api/v1/cis", map[string]any{"ci_type_id": typeID, "name": name, "client_id": f.Client1,
			"attributes": map[string]any{"lq_ram_gb": ram, "lq_note": "n-" + name}}); code != http.StatusCreated {
			t.Fatalf("create %s: %d %v", name, code, out)
		}
	}
	names := func(query url.Values) []string {
		t.Helper()
		code, out := call(http.MethodGet, "/api/v1/cis?"+query.Encode(), nil)
		if code != http.StatusOK {
			t.Fatalf("%s: %d %v", query.Encode(), code, out)
		}
		var got []string
		for _, row := range out["data"].([]any) {
			got = append(got, row.(map[string]any)["name"].(string))
		}
		return got
	}

	// A numeric attribute compares as a number: 16 and 32, not "8" > "16".
	if got := names(url.Values{"filter[attributes.lq_ram_gb][gte]": {"16"}, "filter[name][like]": {"lq-*"}, "sort": {"name"}}); strings.Join(got, ",") != "lq-b,lq-c" {
		t.Errorf("ram >= 16: %v", got)
	}
	if got := names(url.Values{"filter[name][in]": {"lq-a,lq-c"}, "sort": {"-name"}}); strings.Join(got, ",") != "lq-c,lq-a" {
		t.Errorf("in, sort desc: %v", got)
	}
	code, out := call(http.MethodGet, "/api/v1/cis?"+url.Values{"filter[name]": {"lq-b"}, "fields": {"name"}}.Encode(), nil)
	if rows := out["data"].([]any); code != http.StatusOK || len(rows) != 1 || len(rows[0].(map[string]any)) != 2 {
		t.Errorf("fields=name: %d %v", code, out)
	}

	for query, want := range map[string]int{
		url.Values{"filter[attributes.lq_note]": {"x"}}.Encode():        http.StatusBadRequest,
		url.Values{"filter[attributes.lq_missing]": {"1"}}.Encode():     http.StatusUnprocessableEntity,
		url.Values{"filter[attributes.lq_ram_gb][gte]": {"x"}}.Encode(): http.StatusBadRequest,
		url.Values{"filter[unknown]": {"1"}}.Encode():                   http.StatusUnprocessableEntity,
		url.Values{"sort": {"serial_number"}}.Encode():                  http.StatusUnprocessableEntity,
		url.Values{"filter[client_id]": {"not-a-uuid"}}.Encode():        http.StatusBadRequest,
	} {
		code, out := call(http.MethodGet, "/api/v1/cis?"+query, nil)
		if code != want {
			t.Errorf("%s: %d %v, want %d", query, code, out, want)
		}
		if strings.Contains(query, "lq_note") && !strings.Contains(out["detail"].(string), "searchable") {
			t.Errorf("no searchable hint: %v", out)
		}
	}
}
