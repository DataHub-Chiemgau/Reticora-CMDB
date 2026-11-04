package export_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/go-chi/chi/v5"
	"golang.org/x/text/encoding/charmap"
)

// datevLicensed licenses every feature.
type datevLicensed struct{}

func (datevLicensed) IsEnabled(context.Context, string, string) bool { return true }

// datevPolicies resolves every creator without field permissions.
type datevPolicies struct{}

func (datevPolicies) Resolve(_ context.Context, p identity.Principal) (*permission.FieldPolicy, error) { //nolint:gocritic // the interface takes the principal by value
	return permission.UnrestrictedPolicy(p.Permissions), nil
}

// contentTypes records the content type of every stored object.
type contentTypes struct {
	blob.Store
	mu    sync.Mutex
	types map[string]string
}

func (c *contentTypes) Put(ctx context.Context, bucket, key string, r io.Reader, size int64, contentType string) error {
	c.mu.Lock()
	c.types[key] = contentType
	c.mu.Unlock()
	return c.Store.Put(ctx, bucket, key, r, size, contentType)
}

// TestDATEVExportFromAssets covers WP-310 (EXP-01, TST-01) on PostgreSQL:
// a DATEV job of the export pipeline renders the assets of the creator's
// scope snapshot through asset.PGRepository (supplier and location joined
// under RLS, no CI without asset, purchase_price exact), reads the mapping
// override from organization.settings, stores the file (Windows-1252), the
// error report (UTF-8) and the manifest, and GET /api/v1/exports/{id}
// answers error_report_url and the stored manifest after the override
// changed.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestDATEVExportFromAssets(t *testing.T) {
	f := scopetest.Seed(t, "1c")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	site := f.ID()
	exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'Lager Süd')`, site, f.OrgA, f.Client1)
	supplier := f.ID()
	exec(`INSERT INTO supplier (id, organization_id, name) VALUES ($1, $2, 'Müller & Söhne')`, supplier, f.OrgA)
	f.CI(t, f.OrgA, f.Client1, "datev-ci-ohne-asset")
	exec(`INSERT INTO asset (organization_id, client_id, inventory_no, name, purchase_price, purchase_date, warranty_until,
		supplier_id, location_id, cost_center) VALUES ($1, $2, 'DATEV-A1', 'Bürostuhl', 1234.50, '2024-03-15', '2027-03-14', $3, $4, '4711')`,
		f.OrgA, f.Client1, supplier, site)
	exec(`INSERT INTO asset (organization_id, client_id, inventory_no, name, purchase_price, purchase_date)
		VALUES ($1, $2, 'DATEV-A2', 'Großrechner', 999999999999.99, '2020-12-31')`, f.OrgA, f.Client2)
	exec(`INSERT INTO asset (organization_id, client_id, inventory_no, name, purchase_date)
		VALUES ($1, $2, 'DATEV-A3', 'Ohne Preis', '2024-01-01')`, f.OrgA, f.Client1)
	exec(`INSERT INTO asset (organization_id, inventory_no, name, purchase_price, purchase_date)
		VALUES ($1, 'DATEV-B1', 'Fremd', 1, '2024-01-01')`, f.OrgB)

	jobs := export.NewPGJobRepository(f.App)
	blobs := &contentTypes{Store: blob.NewFileStore(t.TempDir()), types: map[string]string{}}
	worker := export.NewJobWorker(jobs, ci.NewPGRepository(f.App), blobs)
	gate := export.NewDATEVGate(datevLicensed{}, true)
	worker.DATEV = gate
	worker.Policies = datevPolicies{}
	worker.Assets = asset.NewPGRepository(f.App)
	worker.Orgs = tenantapi.NewPGRepository(f.App)
	mux := chi.NewRouter()
	export.NewJobHandler(jobs, worker, blobs).WithDATEVGate(gate).RegisterRoutes(mux)

	perms := []identity.Permission{identity.PermExportRun, identity.PermAssetRead, identity.PermCIRead}
	request := func(method, path, body string, scope *database.TenantScope) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
		ctx = database.ContextWithTenantScope(ctx, scope)
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Permissions: perms})
		ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(perms))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	type jobBody struct {
		ID             string          `json:"id"`
		Status         string          `json:"status"`
		ObjectKey      string          `json:"object_key"`
		RowCount       int             `json:"row_count"`
		ErrorMessage   string          `json:"error_message"`
		DownloadURL    string          `json:"download_url"`
		ErrorReportURL string          `json:"error_report_url"`
		Manifest       export.Manifest `json:"manifest"`
	}
	get := func(id string, scope *database.TenantScope) jobBody {
		t.Helper()
		w := request(http.MethodGet, "/api/v1/exports/"+id, "", scope)
		var body jobBody
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("get job: %d %s", w.Code, w.Body.String())
		}
		return body
	}
	run := func(scope *database.TenantScope) jobBody {
		t.Helper()
		w := request(http.MethodPost, "/api/v1/exports", `{"format":"datev"}`, scope)
		var created jobBody
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &created) != nil {
			t.Fatalf("create job: %d %s", w.Code, w.Body.String())
		}
		for worker.ProcessOne(bg) {
		}
		return get(created.ID, scope)
	}
	read := func(key string) string {
		t.Helper()
		rc, err := blobs.Get(bg, export.ExportBucket, key)
		if err != nil {
			t.Fatalf("read %s: %v", key, err)
		}
		defer func() { _ = rc.Close() }()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	decode := func(data string) string {
		t.Helper()
		text, err := charmap.Windows1252.NewDecoder().String(data)
		if err != nil {
			t.Fatal(err)
		}
		return text
	}

	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	job := run(&client1)
	if job.Status != export.JobStatusCompleted || job.RowCount != 1 {
		t.Fatalf("client-1 job: %s %q rows %d", job.Status, job.ErrorMessage, job.RowCount)
	}
	file := decode(read(job.ObjectKey))
	wantRow := "DATEV-A1;Bürostuhl;;;15.03.2024;1234,50;EUR;Müller & Söhne;4711;14.03.2027;Lager Süd;"
	if !strings.Contains(file, wantRow) || strings.Contains(file, "DATEV-A2") || strings.Contains(file, "DATEV-B1") ||
		strings.Contains(file, "datev-ci-ohne-asset") || !strings.HasSuffix(file, "\r\n") {
		t.Errorf("client-1 file %q, want only DATEV-A1 as %q", file, wantRow)
	}
	report := read(export.ErrorReportKey(&export.Job{ID: job.ID, OrganizationID: f.OrgA, Format: export.FormatDATEV}))
	if !strings.HasPrefix(report, string([]byte{0xef, 0xbb, 0xbf})) || !strings.Contains(report, "DATEV-A3;Anschaffungskosten;Pflichtfeld fehlt") {
		t.Errorf("error report %q", report)
	}
	if !strings.HasSuffix(job.ErrorReportURL, job.ID+".errors.csv") || job.DownloadURL == "" {
		t.Errorf("urls %q %q", job.DownloadURL, job.ErrorReportURL)
	}
	if ct := blobs.types[job.ObjectKey]; ct != "text/csv; charset=windows-1252" {
		t.Errorf("content type %q", ct)
	}
	if ct := blobs.types[strings.TrimSuffix(job.ObjectKey, ".csv")+".errors.csv"]; ct != "text/csv; charset=utf-8" {
		t.Errorf("error report content type %q", ct)
	}

	// The org-wide job carries the client-2 asset with its exact price.
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	job = run(&orgWide)
	if file = decode(read(job.ObjectKey)); !strings.Contains(file, "DATEV-A2;Großrechner;;;31.12.2020;999999999999,99;EUR;") {
		t.Errorf("org-wide file %q, want DATEV-A2 with the exact price", file)
	}

	// The override of organization.settings decides the columns; the
	// completed job keeps the manifest of its file after a change.
	exec(`UPDATE organization SET settings = settings || '{"datev_mapping":{"columns":[
		{"header":"Nr","source":"inventory_no","required":true},{"header":"AK","source":"purchase_price"}]}}'::jsonb WHERE id = $1`, f.OrgA)
	job = run(&orgWide)
	if file = decode(read(job.ObjectKey)); !strings.HasPrefix(file, "Nr;AK\r\n") || !strings.Contains(file, "DATEV-A1;1234,50\r\n") {
		t.Errorf("override file %q", file)
	}
	var stored export.Manifest
	if err := json.Unmarshal([]byte(read(job.ObjectKey+".manifest.json")), &stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(stored.Fields, []string{"inventory_no", "purchase_price"}) || !slices.Equal(job.Manifest.Fields, stored.Fields) {
		t.Errorf("stored manifest %+v, answered %+v", stored, job.Manifest)
	}
	exec(`UPDATE organization SET settings = settings || '{"datev_mapping":{"columns":[{"header":"Name","source":"name"}]}}'::jsonb WHERE id = $1`, f.OrgA)
	if again := get(job.ID, &orgWide); !slices.Equal(again.Manifest.Fields, stored.Fields) {
		t.Errorf("manifest after the override changed %+v, want the stored %+v", again.Manifest, stored)
	}

	// An invalid override fails the job with its reason.
	exec(`UPDATE organization SET settings = settings || '{"datev_mapping":{"columns":[]}}'::jsonb WHERE id = $1`, f.OrgA)
	if job = run(&orgWide); job.Status != export.JobStatusFailed || !strings.Contains(job.ErrorMessage, "datev_mapping") {
		t.Errorf("invalid override: %s %q", job.Status, job.ErrorMessage)
	}
}
