package export

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/go-chi/chi/v5"
	"golang.org/x/text/encoding/charmap"
)

// The DATEV export of the asset accounting (EXP-01, TST-01, E-50, WP-310):
// Windows-1252, semicolons, CRLF, header of the mapping, amounts with a
// decimal comma, dates DD.MM.YYYY, formula guard for text cells only,
// rejected rows in the error report, the mapping override, the currency of
// the organization and the asset field permissions of the creator.

// fakeAssets is an asset source over fixed rows, ordered by (created_at,
// id); it records the queries.
type fakeAssets struct {
	rows    []asset.DATEVRow
	queries []asset.DATEVQuery
}

func (f *fakeAssets) ListDATEV(_ context.Context, _ string, q *asset.DATEVQuery) ([]asset.DATEVRow, error) {
	f.queries = append(f.queries, *q)
	var out []asset.DATEVRow
	for i := range f.rows {
		r := f.rows[i]
		if q.ClientID != "" && r.ClientID != q.ClientID || q.LifecycleState != "" && r.LifecycleState != q.LifecycleState {
			continue
		}
		if q.AfterID != "" && (r.CreatedAt.Before(q.AfterCreatedAt) || r.CreatedAt.Equal(q.AfterCreatedAt) && r.ID <= q.AfterID) {
			continue
		}
		out = append(out, r)
		if len(out) == q.Limit {
			break
		}
	}
	return out, nil
}

// fakeOrgs answers one organization and counts the reads.
type fakeOrgs struct {
	org   tenantapi.Organization
	calls int
}

func (f *fakeOrgs) GetOrganization(context.Context, string) (*tenantapi.Organization, error) {
	f.calls++
	org := f.org
	return &org, nil
}

// allowDATEV licenses every feature.
type allowDATEV struct{}

func (allowDATEV) IsEnabled(context.Context, string, string) bool { return true }

var datevEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// datevRows are the golden assets: three rows of the file (umlauts, €,
// formulas, a negative and a large amount, an asset currency) and three
// rejected ones (missing price, a character outside Windows-1252, missing
// date with a formula as inventory number).
func datevRows() []asset.DATEVRow {
	rows := []asset.DATEVRow{
		{InventoryNo: "INV-001", Name: "Bürostuhl Müller", Manufacturer: "Grüße AG", Model: "X-1", PurchaseDate: "2024-03-15",
			PurchasePrice: "1234.50", Supplier: "Lieferant & Söhne", CostCenter: "4711", WarrantyUntil: "2027-03-14",
			Location: "Lager Süd", LifecycleState: "in_use"},
		{InventoryNo: "INV-002", Name: `=HYPERLINK("x")`, Manufacturer: "-Minus", Model: "Preis 5 €", PurchaseDate: "2023-12-01",
			PurchasePrice: "-12.00", Currency: "EUR", LifecycleState: "retired"},
		{InventoryNo: "INV-003", Name: "Ohne Preis", PurchaseDate: "2024-01-01", LifecycleState: "in_use"},
		{InventoryNo: "INV-004", Name: "Server ✓", PurchaseDate: "2024-01-01", PurchasePrice: "10.00", LifecycleState: "in_use"},
		{InventoryNo: "=evil", Name: "Ohne Datum", PurchasePrice: "1.00", LifecycleState: "in_use"},
		{InventoryNo: "INV-006", Name: "Großrechner", PurchaseDate: "2020-12-31", PurchasePrice: "123456789.99", Currency: "CHF",
			CostCenter: "+49", LifecycleState: "in_stock"},
	}
	for i := range rows {
		rows[i].ID = "00000000-0000-4000-8000-0000000a000" + string(rune('1'+i))
		rows[i].ClientID = frClient1
		rows[i].CreatedAt = datevEpoch.Add(time.Duration(i) * time.Minute)
	}
	return rows
}

const datevExpectedReport = utf8BOM + "Inventarnummer;Spalte;Grund\n" +
	"INV-003;Anschaffungskosten;Pflichtfeld fehlt\n" +
	"INV-004;Bezeichnung;Zeichen nicht in Windows-1252 darstellbar\n" +
	"'=evil;Anschaffungsdatum;Pflichtfeld fehlt\n"

// TestDATEVRenderGolden: the file matches testdata/datev_expected.csv byte
// for byte; the rejected rows are in the error report only.
func TestDATEVRenderGolden(t *testing.T) {
	src := &fakeAssets{rows: datevRows()}
	var out, report bytes.Buffer
	res, err := RenderDATEV(context.Background(), src, frOrg, JobFilters{}, DefaultDATEVMapping(), "EUR",
		permission.UnrestrictedPolicy(identity.AllPermissions()), &out, &report)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/datev_expected.csv")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), want) {
		t.Errorf("datev file\n%q\nwant\n%q", out.Bytes(), want)
	}
	if res.Rows != 3 || res.Rejected != 3 {
		t.Errorf("rows %d rejected %d, want 3 and 3", res.Rows, res.Rejected)
	}
	if report.String() != datevExpectedReport {
		t.Errorf("error report\n%q\nwant\n%q", report.String(), datevExpectedReport)
	}
	if bytes.Contains(out.Bytes(), []byte("\xef\xbb\xbf")) || !bytes.Contains(out.Bytes(), []byte("\x80")) {
		t.Error("the file must be Windows-1252 without BOM (€ is 0x80)")
	}
	if src.queries[0].WithContract {
		t.Error("the default mapping must not join the contract")
	}
	if !slices.Equal(res.Manifest.Fields, []string{"inventory_no", "name", "manufacturer", "model", "purchase_date",
		"purchase_price", "currency", "supplier", "cost_center", "warranty_until", "location", "lifecycle_state"}) {
		t.Errorf("manifest fields %v", res.Manifest.Fields)
	}
	for _, o := range res.Manifest.Omitted {
		if o.Reason != ReasonLayout {
			t.Errorf("unrestricted manifest omits %s with %s", o.Field, o.Reason)
		}
	}
}

// TestDATEVCellFormats: amounts from numeric text without float and
// without thousands separator, dates DD.MM.YYYY, the formula guard for
// text cells only.
func TestDATEVCellFormats(t *testing.T) {
	for _, c := range []struct {
		format, value, cell, reason string
	}{
		{DATEVFormatAmount, "1234.50", "1234,50", ""},
		{DATEVFormatAmount, "-12.00", "-12,00", ""},
		{DATEVFormatAmount, "999999999999.99", "999999999999,99", ""},
		{DATEVFormatAmount, "0.10", "0,10", ""},
		{DATEVFormatAmount, "1e3", "", datevReasonBadAmount},
		{DATEVFormatDate, "2024-02-29", "29.02.2024", ""},
		{DATEVFormatDate, "29.02.2024", "", datevReasonBadDate},
		{DATEVFormatText, "=1+2", "'=1+2", ""},
		{DATEVFormatText, "-5", "'-5", ""},
		{DATEVFormatText, "@SUM", "'@SUM", ""},
		{DATEVFormatText, "Müller", "Müller", ""},
	} {
		cell, reason := formatDATEVCell(c.format, c.value)
		if cell != c.cell || reason != c.reason {
			t.Errorf("%s %q: %q %q, want %q %q", c.format, c.value, cell, reason, c.cell, c.reason)
		}
	}
}

// TestDATEVMappingOverride: a valid override sets the columns (and joins
// the contract when it names it); an invalid one is a DATEVMappingError.
func TestDATEVMappingOverride(t *testing.T) {
	m, err := EffectiveDATEVMapping(map[string]any{SettingDATEVMapping: map[string]any{"columns": []any{
		map[string]any{"header": "Inv.-Nr.", "source": "inventory_no", "required": true},
		map[string]any{"header": "AK", "source": "purchase_price", "format": "amount"},
		map[string]any{"header": "Vertrag", "source": "contract"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	src := &fakeAssets{rows: datevRows()[:1]}
	src.rows[0].Contract = "V-17"
	var out, report bytes.Buffer
	if _, err = RenderDATEV(context.Background(), src, frOrg, JobFilters{}, m, "", nil, &out, &report); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Inv.-Nr.;AK;Vertrag\r\n;;\r\n" {
		// A nil policy reads no field with field permissions.
		t.Errorf("override with nil policy: %q", got)
	}
	out.Reset()
	report.Reset()
	if _, err = RenderDATEV(context.Background(), src, frOrg, JobFilters{}, m, "",
		permission.UnrestrictedPolicy(identity.AllPermissions()), &out, &report); err != nil {
		t.Fatal(err)
	}
	if got := out.String(); got != "Inv.-Nr.;AK;Vertrag\r\nINV-001;1234,50;V-17\r\n" {
		t.Errorf("override: %q", got)
	}
	if !src.queries[0].WithContract {
		t.Error("a mapping naming the contract must join it")
	}

	for name, settings := range map[string]any{
		"unknown source": map[string]any{"columns": []any{map[string]any{"header": "X", "source": "secret"}}},
		"empty columns":  map[string]any{"columns": []any{}},
		"no object":      "csv",
		"bad format":     map[string]any{"columns": []any{map[string]any{"header": "X", "source": "name", "format": "amount"}}},
		"bad header":     map[string]any{"columns": []any{map[string]any{"header": "✓", "source": "name"}}},
		"unknown member": map[string]any{"columns": []any{map[string]any{"header": "X", "source": "name", "width": 3}}},
	} {
		_, err := EffectiveDATEVMapping(map[string]any{SettingDATEVMapping: settings})
		if !isDATEVMappingError(err) {
			t.Errorf("%s: %v, want a DATEVMappingError", name, err)
		}
	}
}

// datevWorld is a job handler and worker over fake assets with an
// organization reader.
type datevWorld struct {
	mux    chi.Router
	worker *JobWorker
	jobs   *MemoryJobRepository
	blobs  blob.Store
	assets *fakeAssets
	orgs   *fakeOrgs
}

func newDATEVWorld(t *testing.T) *datevWorld {
	t.Helper()
	w := &datevWorld{jobs: NewMemoryJobRepository(), blobs: blob.NewFileStore(t.TempDir()),
		assets: &fakeAssets{rows: datevRows()}, orgs: &fakeOrgs{org: tenantapi.Organization{ID: frOrg, Currency: "EUR"}}}
	w.worker = NewJobWorker(w.jobs, ci.NewMemoryRepository(), w.blobs)
	gate := NewDATEVGate(allowDATEV{}, true)
	w.worker.DATEV = gate
	w.worker.Assets = w.assets
	w.worker.Orgs = w.orgs
	w.worker.Policies = staticPolicies{policy: permission.UnrestrictedPolicy(identity.AllPermissions())}
	w.mux = chi.NewRouter()
	NewJobHandler(w.jobs, w.worker, w.blobs).WithDATEVGate(gate).RegisterRoutes(w.mux)
	return w
}

// staticPolicies resolves every principal to one policy.
type staticPolicies struct{ policy *permission.FieldPolicy }

func (s staticPolicies) Resolve(context.Context, identity.Principal) (*permission.FieldPolicy, error) {
	return s.policy, nil
}

func (w *datevWorld) serve(t *testing.T, method, target, body string, policy *permission.FieldPolicy) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: frOrg, UserID: frUser})
	scope := database.OrgWideScope(frOrg, frUser)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: frUser, OrganizationID: frOrg,
		Permissions: []identity.Permission{identity.PermExportRun, identity.PermAssetRead}})
	rec := httptest.NewRecorder()
	w.mux.ServeHTTP(rec, r.WithContext(permission.WithPolicy(ctx, policy)))
	return rec
}

func (w *datevWorld) create(t *testing.T, body string) jobResponse {
	t.Helper()
	rec := w.serve(t, http.MethodPost, "/api/v1/exports", body, permission.UnrestrictedPolicy(identity.AllPermissions()))
	var resp jobResponse
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
		t.Fatalf("create %s: %d %s", body, rec.Code, rec.Body.String())
	}
	return resp
}

func (w *datevWorld) object(t *testing.T, key string) []byte {
	t.Helper()
	rc, err := w.blobs.Get(context.Background(), ExportBucket, key)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestDATEVJob: the worker renders assets (no CI), stores file, error
// report and manifest; the job answers download and error report URLs,
// and row_count counts the written rows only; the currency comes from the
// organization.
func TestDATEVJob(t *testing.T) {
	w := newDATEVWorld(t)
	created := w.create(t, `{"format":"datev","filters":{"client_id":"`+frClient1+`","lifecycle_state":"in_use"}}`)
	if !slices.Contains(created.Manifest.Fields, "purchase_price") || w.orgs.calls != 1 {
		t.Errorf("preview manifest %+v (org reads %d)", created.Manifest, w.orgs.calls)
	}
	if !w.worker.ProcessOne(context.Background()) {
		t.Fatal("no job claimed")
	}
	job, err := w.jobs.GetJob(context.Background(), frOrg, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != JobStatusCompleted || job.RowCount == nil || *job.RowCount != 1 {
		t.Fatalf("job %s %q rows %v, want completed with 1 row", job.Status, job.ErrorMessage, job.RowCount)
	}
	if q := w.assets.queries[0]; q.ClientID != frClient1 || q.LifecycleState != "in_use" {
		t.Errorf("query %+v, want the filters of the job", q)
	}
	file := w.object(t, job.ObjectKey)
	if !bytes.HasSuffix(file, []byte("INV-001;B\xfcrostuhl M\xfcller;Gr\xfc\xdfe AG;X-1;15.03.2024;1234,50;EUR;Lieferant & S\xf6hne;4711;14.03.2027;Lager S\xfcd;in_use\r\n")) {
		t.Errorf("file %q", file)
	}
	report := string(w.object(t, ErrorReportKey(job)))
	if !strings.HasPrefix(report, utf8BOM) || !strings.Contains(report, "INV-004;Bezeichnung;Zeichen nicht in Windows-1252 darstellbar") {
		t.Errorf("error report %q", report)
	}
	if ErrorReportKey(job) != frOrg+"/"+job.ID+".errors.csv" {
		t.Errorf("error report key %s", ErrorReportKey(job))
	}
	rec := w.serve(t, http.MethodGet, "/api/v1/exports/"+job.ID, "", permission.UnrestrictedPolicy(identity.AllPermissions()))
	var got jobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.DownloadURL == "" || !strings.HasSuffix(got.ErrorReportURL, ".errors.csv") {
		t.Errorf("urls %q %q", got.DownloadURL, got.ErrorReportURL)
	}
	if exportContentTypes[FormatDATEV] != "text/csv; charset=windows-1252" || exportExtensions[FormatDATEV] != "csv" {
		t.Error("datev content type and extension")
	}
	// A CSV job has no error report URL.
	csvJob := w.create(t, `{"format":"csv"}`)
	w.worker.ProcessOne(context.Background())
	rec = w.serve(t, http.MethodGet, "/api/v1/exports/"+csvJob.ID, "", permission.UnrestrictedPolicy(identity.AllPermissions()))
	if strings.Contains(rec.Body.String(), "error_report_url") {
		t.Errorf("csv job with error report: %s", rec.Body.String())
	}
}

// TestDATEVJobOverride: the worker reads the override of the organization;
// an invalid override fails the job with a message of its own (not
// jobs.FailedMessage), and the preview shows no fields.
func TestDATEVJobOverride(t *testing.T) {
	w := newDATEVWorld(t)
	w.orgs.org.Settings = map[string]any{SettingDATEVMapping: map[string]any{"columns": []any{
		map[string]any{"header": "Nr", "source": "inventory_no"}, map[string]any{"header": "Wert", "source": "purchase_price"}}}}
	created := w.create(t, `{"format":"datev","filters":{"lifecycle_state":"in_stock"}}`)
	if !slices.Equal(created.Manifest.Fields, []string{"inventory_no", "purchase_price"}) {
		t.Errorf("preview of the override %+v", created.Manifest)
	}
	w.worker.ProcessOne(context.Background())
	job, _ := w.jobs.GetJob(context.Background(), frOrg, created.ID)
	if job.Status != JobStatusCompleted || string(w.object(t, job.ObjectKey)) != "Nr;Wert\r\nINV-006;123456789,99\r\n" {
		t.Fatalf("override job %s %q", job.Status, job.ErrorMessage)
	}

	w.orgs.org.Settings = map[string]any{SettingDATEVMapping: map[string]any{"columns": []any{
		map[string]any{"header": "Nr", "source": "password"}}}}
	created = w.create(t, `{"format":"datev"}`)
	if len(created.Manifest.Fields) != 0 {
		t.Errorf("preview of an invalid override %+v", created.Manifest)
	}
	w.worker.ProcessOne(context.Background())
	job, _ = w.jobs.GetJob(context.Background(), frOrg, created.ID)
	if job.Status != JobStatusFailed || job.ErrorMessage == jobs.FailedMessage || !strings.Contains(job.ErrorMessage, "password") {
		t.Fatalf("invalid override: %s %q, want failed naming the source", job.Status, job.ErrorMessage)
	}
}

// TestDATEVJobWithoutAssetSource: the memory profile fails a DATEV job
// with a message of its own.
func TestDATEVJobWithoutAssetSource(t *testing.T) {
	w := newDATEVWorld(t)
	w.worker.Assets = nil
	created := w.create(t, `{"format":"datev"}`)
	w.worker.ProcessOne(context.Background())
	job, _ := w.jobs.GetJob(context.Background(), frOrg, created.ID)
	if job.Status != JobStatusFailed || job.ErrorMessage != ErrDATEVUnavailable.Error() {
		t.Fatalf("job %s %q", job.Status, job.ErrorMessage)
	}
}

// TestDATEVFilters: status and ci_type_id do not apply to datev,
// lifecycle_state applies to datev only (400).
func TestDATEVFilters(t *testing.T) {
	w := newDATEVWorld(t)
	for _, body := range []string{
		`{"format":"datev","filters":{"status":"active"}}`,
		`{"format":"datev","filters":{"ci_type_id":"x"}}`,
		`{"format":"csv","filters":{"lifecycle_state":"in_use"}}`,
	} {
		if rec := w.serve(t, http.MethodPost, "/api/v1/exports", body, permission.UnrestrictedPolicy(identity.AllPermissions())); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", body, rec.Code)
		}
	}
}

// TestDATEVFieldPermissions: a source the creator may not read for any
// asset type stays an empty column (not checked as required) and is in the
// manifest with reason field_permission; a source closed on one asset by
// an object grant is empty for that asset only. The stored manifest is
// answered for the completed job, the preview for the open one.
func TestDATEVFieldPermissions(t *testing.T) {
	bg := context.Background()
	grants := permission.NewMemoryFieldGrants()
	if _, err := grants.CreateFieldGrant(bg, frOrg, &permission.FieldGrant{RoleID: ptrTo(frRole), ObjectKind: permission.KindAsset,
		FieldKey: ptrTo("core.purchase_cost"), Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	rows := datevRows()
	if _, err := grants.CreateObjectGrant(bg, frOrg, &permission.ObjectGrant{GranteeRoleID: ptrTo(frRole),
		ObjectKind: permission.KindAsset, ObjectID: rows[0].ID, Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	resolver := permission.NewFieldResolver(&frAccess{grants: []identity.Grant{frGrant(frRole)}}, nil, grants, nil)
	policy, err := resolver.Resolve(bg, identity.Principal{Subject: frUser, OrganizationID: frOrg,
		Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()})
	if err != nil {
		t.Fatal(err)
	}

	var out, report bytes.Buffer
	res, err := RenderDATEV(bg, &fakeAssets{rows: rows}, frOrg, JobFilters{}, DefaultDATEVMapping(), "EUR", policy, &out, &report)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(res.Manifest.Fields, "purchase_price") ||
		!slices.Contains(res.Manifest.Omitted, OmittedField{Field: "purchase_price", Reason: ReasonFieldPermission}) {
		t.Errorf("manifest %+v, want purchase_price omitted for field_permission", res.Manifest)
	}
	if strings.Contains(out.String(), "1234,50") || strings.Contains(out.String(), "123456789") {
		t.Errorf("the closed price leaks: %q", out.String())
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\r\n"), "\r\n")
	// The asset closed by its object grant keeps its name only.
	if !slices.Contains(lines, ";B\xfcrostuhl M\xfcller;;;;;;;;;;") {
		t.Errorf("object-closed asset not empty: %q", lines)
	}
	// INV-003 (no price) is no longer rejected: its price is not read.
	if !strings.Contains(out.String(), "INV-003") || strings.Contains(report.String(), "INV-003") {
		t.Errorf("file %q report %q", out.String(), report.String())
	}

	w := newDATEVWorld(t)
	w.worker.Policies = resolver
	created := w.create(t, `{"format":"datev"}`)
	rec := w.serve(t, http.MethodGet, "/api/v1/exports/"+created.ID, "", policy)
	var pending jobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(pending.Manifest.Omitted, OmittedField{Field: "purchase_price", Reason: ReasonFieldPermission}) {
		t.Errorf("preview %+v, want the policy of the caller", pending.Manifest)
	}
	w.worker.ProcessOne(bg)
	job, _ := w.jobs.GetJob(bg, frOrg, created.ID)
	var stored Manifest
	if err := json.Unmarshal(w.object(t, ManifestKey(job)), &stored); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stored.Omitted, OmittedField{Field: "purchase_price", Reason: ReasonFieldPermission}) {
		t.Errorf("stored manifest %+v", stored)
	}
	// The completed job answers the stored manifest, whatever the caller.
	rec = w.serve(t, http.MethodGet, "/api/v1/exports/"+created.ID, "", permission.UnrestrictedPolicy(identity.AllPermissions()))
	var done jobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &done); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(done.Manifest.Omitted, OmittedField{Field: "purchase_price", Reason: ReasonFieldPermission}) {
		t.Errorf("completed job manifest %+v, want the stored one", done.Manifest)
	}
}

// TestDATEVBatches: the renderer pages with the keyset (created_at, id).
func TestDATEVBatches(t *testing.T) {
	src := &fakeAssets{}
	for i := range renderBatchSize + 3 {
		src.rows = append(src.rows, asset.DATEVRow{ID: fmt.Sprintf("id-%04d", i),
			InventoryNo: fmt.Sprintf("N%d", i), Name: "A", PurchaseDate: "2024-01-01", PurchasePrice: "1.00", Currency: "EUR",
			CreatedAt: datevEpoch})
	}
	var out, report bytes.Buffer
	res, err := RenderDATEV(context.Background(), src, frOrg, JobFilters{}, DefaultDATEVMapping(), "",
		permission.UnrestrictedPolicy(identity.AllPermissions()), &out, &report)
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != renderBatchSize+3 || len(src.queries) != 2 || src.queries[1].AfterID != src.rows[renderBatchSize-1].ID {
		t.Errorf("rows %d after %d queries", res.Rows, len(src.queries))
	}
}

// TestDATEVHeaderAndClosedColumn covers the TST-01 [P2] DATEV cases that
// the golden file shows only implicitly (WP-326, EXP-01, E-50): the
// header is the headers of export/datev_mapping.json (the default
// mapping) in their order, Windows-1252 encoded and closed with CRLF,
// also for an export without rows; a source the creator may not read
// stays an empty column in every row (the column count is unchanged) and
// is named in the manifest with reason field_permission, while the other
// cells keep their formats (amount with decimal comma, date DD.MM.YYYY).
func TestDATEVHeaderAndClosedColumn(t *testing.T) {
	raw, err := os.ReadFile("datev_mapping.json")
	if err != nil {
		t.Fatal(err)
	}
	var file DATEVMapping
	if err = json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	headers := make([]string, 0, len(file.Columns))
	for _, c := range file.Columns {
		headers = append(headers, c.Header)
	}
	header, err := charmap.Windows1252.NewEncoder().String(strings.Join(headers, ";") + "\r\n")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(DefaultDATEVMapping().Columns, file.Columns) {
		t.Errorf("default mapping %+v, want the columns of datev_mapping.json", DefaultDATEVMapping().Columns)
	}
	all := permission.UnrestrictedPolicy(identity.AllPermissions())
	var out, report bytes.Buffer
	if _, err = RenderDATEV(context.Background(), &fakeAssets{}, frOrg, JobFilters{}, DefaultDATEVMapping(), "EUR", all, &out, &report); err != nil {
		t.Fatal(err)
	}
	if out.String() != header {
		t.Errorf("header without rows %q, want %q", out.String(), header)
	}
	if !strings.Contains(header, "W\xe4hrung") {
		t.Errorf("header %q: the umlaut is not Windows-1252", header)
	}

	// The creator may not read the supplier of any asset.
	bg := context.Background()
	grants := permission.NewMemoryFieldGrants()
	if _, err = grants.CreateFieldGrant(bg, frOrg, &permission.FieldGrant{RoleID: ptrTo(frRole), ObjectKind: permission.KindAsset,
		FieldKey: ptrTo("core.supplier"), Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	policy, err := permission.NewFieldResolver(&frAccess{grants: []identity.Grant{frGrant(frRole)}}, nil, grants, nil).
		Resolve(bg, identity.Principal{Subject: frUser, OrganizationID: frOrg, Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()})
	if err != nil {
		t.Fatal(err)
	}
	out.Reset()
	report.Reset()
	res, err := RenderDATEV(bg, &fakeAssets{rows: datevRows()}, frOrg, JobFilters{}, DefaultDATEVMapping(), "EUR", policy, &out, &report)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(res.Manifest.Omitted, OmittedField{Field: "supplier", Reason: ReasonFieldPermission}) || slices.Contains(res.Manifest.Fields, "supplier") {
		t.Errorf("manifest %+v, want supplier omitted for field_permission", res.Manifest)
	}
	col := slices.IndexFunc(file.Columns, func(c DATEVColumn) bool { return c.Source == "supplier" })
	if col < 0 {
		t.Fatal("datev_mapping.json has no supplier column")
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\r\n"), "\r\n")
	if len(lines) != 1+res.Rows || !strings.HasPrefix(out.String(), header) {
		t.Fatalf("file %q: want the header and %d rows", out.String(), res.Rows)
	}
	for _, line := range lines[1:] {
		cells := strings.Split(line, ";")
		if len(cells) != len(file.Columns) || cells[col] != "" {
			t.Errorf("row %q: want %d cells with an empty supplier column", line, len(file.Columns))
		}
	}
	if strings.Contains(strings.Join(lines[1:], "\n"), "S\xf6hne") {
		t.Errorf("the closed supplier leaks: %q", out.String())
	}
	if !strings.Contains(lines[1], ";15.03.2024;1234,50;") {
		t.Errorf("first row %q: date and amount lost their formats", lines[1])
	}
}
