package export

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

func tenantCtx(r *http.Request) *http.Request {
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"})
	scope := database.OrgWideScope("org-1", "user-1")
	return r.WithContext(database.ContextWithTenantScope(ctx, &scope))
}

func TestExportJSON(t *testing.T) {
	repo := ci.NewMemoryRepository()
	repo.Create(context.Background(), &ci.Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "srv-01",
		Status:         "active",
		Attributes:     map[string]any{},
	})

	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cis?format=json", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp struct {
		Data  []ci.Item `json:"data"`
		Total int       `json:"total"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if resp.Total != 1 {
		t.Errorf("expected 1, got %d", resp.Total)
	}
}

func TestExportCSV(t *testing.T) {
	repo := ci.NewMemoryRepository()
	repo.Create(context.Background(), &ci.Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "srv-01",
		Status:         "active",
		Manufacturer:   "Dell",
		Attributes:     map[string]any{},
	})

	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cis?format=csv", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	if ct := w.Header().Get("Content-Type"); ct != "text/csv" {
		t.Errorf("expected text/csv, got %s", ct)
	}

	body := w.Body.String()
	if !strings.Contains(body, "srv-01") {
		t.Error("expected CSV to contain srv-01")
	}
	if !strings.Contains(body, "Dell") {
		t.Error("expected CSV to contain Dell")
	}
}

func TestExportDATEV(t *testing.T) {
	repo := ci.NewMemoryRepository()
	repo.Create(context.Background(), &ci.Item{
		OrganizationID: "org-1",
		CITypeID:       "type-server",
		Name:           "srv-01",
		Status:         "active",
		Manufacturer:   "Dell",
		Model:          "R740",
		SerialNumber:   "SN-1",
		Attributes: map[string]any{
			"purchase_date": "2024-01-15",
			"location":      "HQ",
			"cost_center":   "IT-100",
		},
	})

	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cis?format=datev", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "Inventarnummer;Bezeichnung;Hersteller") {
		t.Fatalf("expected DATEV header, got %s", body)
	}
	if !strings.Contains(body, "HQ") || !strings.Contains(body, "IT-100") {
		t.Fatalf("expected DATEV body to contain mapped fields, got %s", body)
	}
}

func TestExportStreamsBeyondOneBatch(t *testing.T) {
	repo := ci.NewMemoryRepository()
	const count = 12
	for i := 0; i < count; i++ {
		err := repo.Create(context.Background(), &ci.Item{
			OrganizationID: "org-1",
			CITypeID:       "type-server",
			Name:           fmt.Sprintf("srv-%02d", i),
			Status:         "active",
			Attributes:     map[string]any{},
		})
		if err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	h := NewHandler(repo)
	h.batchSize = 5 // force several streaming batches
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	for _, format := range []string{"json", "csv", "datev"} {
		t.Run(format, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/api/v1/export/cis?format="+format, nil)
			req = tenantCtx(req)
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", w.Code)
			}
			body := w.Body.String()
			for i := 0; i < count; i++ {
				if !strings.Contains(body, fmt.Sprintf("srv-%02d", i)) {
					t.Fatalf("streamed %s export is missing srv-%02d", format, i)
				}
			}

			if format == "json" {
				var resp struct {
					Data  []ci.Item `json:"data"`
					Total int       `json:"total"`
				}
				if err := json.Unmarshal([]byte(body), &resp); err != nil {
					t.Fatalf("streamed JSON is not valid: %v (%s)", err, body)
				}
				if resp.Total != count || len(resp.Data) != count {
					t.Fatalf("expected %d rows, got total=%d len=%d", count, resp.Total, len(resp.Data))
				}
			}
		})
	}
}

func TestExportRejectsUnknownFormat(t *testing.T) {
	h := NewHandler(ci.NewMemoryRepository())
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)

	req := httptest.NewRequest("GET", "/api/v1/export/cis?format=xml", nil)
	req = tenantCtx(req)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// TestCSVNeutralizesFormulas is the retest of F-338 (WP-593, EXP-01): a
// cell of the csv and datev layouts that starts with =, +, -, @, a tab or
// a CR is prefixed with ', so a spreadsheet shows it as text; the rows stay
// the same in number and the job export (RenderFormat) writes the same
// cells as the streaming endpoint. JSON is not touched.
func TestCSVNeutralizesFormulas(t *testing.T) {
	bg := context.Background()
	repo := ci.NewMemoryRepository()
	for _, item := range []*ci.Item{
		{OrganizationID: "org-1", CITypeID: "type-server", Name: "=1+1", Status: "active",
			Manufacturer: "+cmd|' /C calc'!A0", Model: "-2+3", SerialNumber: "@SUM(A1:A2)", FirmwareVersion: "\t=1",
			Attributes: map[string]any{"location": "=HYPERLINK(\"http://evil.example\")", "cost_center": "-1"}},
		{OrganizationID: "org-1", CITypeID: "type-server", Name: "srv a=1", Status: "active",
			Manufacturer: "Dell", Model: "R740-XD", Attributes: map[string]any{}},
	} {
		if err := repo.Create(bg, item); err != nil {
			t.Fatal(err)
		}
	}
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)

	read := func(t *testing.T, body string) [][]string {
		t.Helper()
		r := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, utf8BOM)))
		r.Comma = ';'
		records, err := r.ReadAll()
		if err != nil {
			t.Fatalf("parse %q: %v", body, err)
		}
		return records
	}
	byName := func(records [][]string, column int) map[string][]string {
		rows := map[string][]string{}
		for _, rec := range records[1:] {
			rows[rec[column]] = rec
		}
		return rows
	}
	for _, c := range []struct {
		format string
		// the name column and the cells of the first CI in layout order
		name  int
		cells map[int]string
	}{
		{"csv", 1, map[int]string{1: "'=1+1", 4: "'+cmd|' /C calc'!A0", 5: "'-2+3", 6: "'@SUM(A1:A2)", 8: "'\t=1"}},
		{"datev", 1, map[int]string{1: "'=1+1", 2: "'+cmd|' /C calc'!A0", 3: "'-2+3", 4: "'@SUM(A1:A2)",
			6: "'=HYPERLINK(\"http://evil.example\")", 7: "'-1"}},
	} {
		t.Run(c.format, func(t *testing.T) {
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/export/cis?format="+c.format, nil)))
			if w.Code != http.StatusOK {
				t.Fatalf("export: %d %s", w.Code, w.Body.String())
			}
			records := read(t, w.Body.String())
			if len(records) != 3 {
				t.Fatalf("%d records, want the header and 2 rows: %q", len(records), records)
			}
			rows := byName(records, c.name)
			evil, plain := rows["'=1+1"], rows["srv a=1"]
			if evil == nil || plain == nil {
				t.Fatalf("rows %q, want the neutralised and the plain CI", records)
			}
			for col, want := range c.cells {
				if evil[col] != want {
					t.Errorf("column %s: %q, want %q", records[0][col], evil[col], want)
				}
			}
			// A cell without a leading trigger stays as it is.
			for _, cell := range plain {
				if strings.HasPrefix(cell, "'") {
					t.Errorf("plain row %q has a prefixed cell %q", plain, cell)
				}
			}

			// The job export renders the same cells.
			var out strings.Builder
			n, err := RenderFormat(tenantCtx(httptest.NewRequest("GET", "/", nil)).Context(), repo, "org-1", c.format, JobFilters{}, &out)
			if err != nil || n != 2 {
				t.Fatalf("RenderFormat: %d rows, %v", n, err)
			}
			job := read(t, out.String())
			if len(job) != 3 || byName(job, c.name)["'=1+1"][1] != "'=1+1" {
				t.Errorf("job export %q, want the same neutralised rows", job)
			}
			for col, want := range c.cells {
				if got := byName(job, c.name)["'=1+1"][col]; got != want {
					t.Errorf("job export column %s: %q, want %q", job[0][col], got, want)
				}
			}
		})
	}

	// Each trigger of the OWASP guidance, and nothing else.
	for in, want := range map[string]string{"=1+1": "'=1+1", "+1": "'+1", "-1": "'-1", "@x": "'@x", "\tx": "'\tx",
		"\rx": "'\rx", "": "", "a=1": "a=1", "'=1": "'=1", " =1": " =1", "Ä=1": "Ä=1"} {
		if got := neutralizeFormula(in); got != want {
			t.Errorf("neutralizeFormula(%q) = %q, want %q", in, got, want)
		}
	}

	// JSON keeps the values: it is no spreadsheet format.
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/export/cis?format=json", nil)))
	if !strings.Contains(w.Body.String(), `"name":"=1+1"`) {
		t.Errorf("json export: %s", w.Body.String())
	}
}

// TestCSVFormatAndManifest covers EXP-01 (WP-189): CSV in UTF-8 with BOM
// and semicolons; the manifest lists the fields of the layout and the
// visible CI fields it leaves out, also in a response header.
func TestCSVFormatAndManifest(t *testing.T) {
	repo := ci.NewMemoryRepository()
	if err := repo.Create(context.Background(), &ci.Item{OrganizationID: "org-1", CITypeID: "type-server",
		Name: "srv; \"quoted\"", Status: "active", Hostname: "srv01", Attributes: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, tenantCtx(httptest.NewRequest("GET", "/api/v1/export/cis?format=csv", nil)))
	body := w.Body.String()
	if !strings.HasPrefix(body, "\ufeffid;name;status;ci_type_id;") {
		t.Errorf("header: %q", body[:min(60, len(body))])
	}
	// A value with the separator is quoted.
	if !strings.Contains(body, `;"srv; ""quoted""";active;`) {
		t.Errorf("row: %q", body)
	}
	if omitted := w.Header().Get("X-Export-Omitted-Fields"); !strings.Contains(omitted, "hostname") || strings.Contains(omitted, "serial_number") {
		t.Errorf("omitted header %q", omitted)
	}

	m := ManifestFor("csv")
	if len(m.Fields) != len(csvColumns) || len(m.Omitted) == 0 || len(m.Fields)+len(m.Omitted) != len(ciFieldNames) {
		t.Errorf("csv manifest %+v", m)
	}
	for _, o := range m.Omitted {
		if o.Reason == "" {
			t.Errorf("omitted %s without reason", o.Field)
		}
	}
	if j := ManifestFor("json"); len(j.Omitted) != 0 || len(j.Fields) != len(ciFieldNames) {
		t.Errorf("json manifest %+v", j)
	}
}
