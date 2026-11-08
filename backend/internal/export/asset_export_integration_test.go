package export_test

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// assetExportFeatures licenses export_csv while on is true.
type assetExportFeatures struct{ on bool }

func (f *assetExportFeatures) IsEnabled(context.Context, string, string) bool { return f.on }

// assetExportAccess gives every user the role of the asset field grants.
type assetExportAccess struct{ role string }

func (a assetExportAccess) AccessGrants(context.Context, string, string) ([]identity.Grant, error) {
	return []identity.Grant{{RoleID: a.role, Permissions: []identity.Permission{identity.PermAssetRead, identity.PermExportRun}}}, nil
}

func (assetExportAccess) RoleGrants(context.Context, string, []string) ([]identity.Grant, error) {
	return nil, nil
}

// TestAssetBulkExport covers WP-782 (BLK-02) on PostgreSQL: the action
// export of POST /api/v1/assets/bulk, wired as the server does
// (server.AssetBulkExporter), creates exactly one export job with
// filters.entity asset and filters.ids (202, Location
// /api/v1/exports/{id}); JobWorker.ProcessOne renders CSV (BOM,
// semicolons) and JSON of the ids visible in the scope snapshot and not
// deleted, through asset.PGRepository.GetByIDs; an asset field the
// creator may not read is missing and named in the manifest with
// field_permission; without export_csv the bulk is 403; POST
// /api/v1/exports with entity asset is 400; a worker without AssetRows
// fails the job.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssetBulkExport(t *testing.T) {
	f := scopetest.Seed(t, "1c")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	own := f.Asset(t, f.OrgA, f.Client1, "WP782-OWN")
	exec(`UPDATE asset SET serial_number = 'SN-782-OWN', created_at = now() - interval '1 hour' WHERE id = $1`, own)
	second := f.Asset(t, f.OrgA, f.Client1, "=WP782-SECOND")
	exec(`UPDATE asset SET serial_number = 'SN-782-SECOND' WHERE id = $1`, second)
	foreign := f.Asset(t, f.OrgA, f.Client2, "WP782-FOREIGN")
	deleted := f.Asset(t, f.OrgA, f.Client1, "WP782-DELETED")
	exec(`DELETE FROM asset WHERE id = $1`, deleted)

	// The creator may not read the serial number of any asset type.
	grants := permission.NewMemoryFieldGrants()
	role := f.ID()
	if _, err := grants.CreateFieldGrant(bg, f.OrgA, &permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindAsset,
		FieldKey: func() *string { s := "core.serial_number"; return &s }(), Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}

	jobRepo := export.NewPGJobRepository(f.App)
	blobs := blob.NewFileStore(t.TempDir())
	worker := export.NewJobWorker(jobRepo, ci.NewPGRepository(f.App), blobs)
	worker.Policies = permission.NewFieldResolver(assetExportAccess{role: role}, nil, grants, nil)
	worker.AssetRows = asset.NewPGRepository(f.App)
	jobHandler := export.NewJobHandler(jobRepo, worker, blobs)

	features := &assetExportFeatures{on: true}
	delegates := server.AssetBulkDelegates(nil, nil, nil, nil, nil)
	delegates.Exporter = server.AssetBulkExporter(jobHandler)
	delegates.Entitlements = features
	mux := chi.NewRouter()
	asset.NewHandler(asset.NewPGRepository(f.App)).WithBulkDelegates(&delegates).RegisterRoutes(mux)
	jobHandler.RegisterRoutes(mux)

	perms := []identity.Permission{identity.PermAssetRead, identity.PermAssetWrite, identity.PermExportRun}
	request := func(method, path string, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if body == nil {
			raw = nil
		}
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User, ClientID: f.Client1})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: f.OrgA, Type: identity.PrincipalTypeUser,
			Permissions: perms})
		ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(perms))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	ids := []string{strings.ToUpper(own), second, foreign, deleted}
	bulkExport := func(format string) string {
		t.Helper()
		var before int
		if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM export_job WHERE organization_id = $1`, f.OrgA).Scan(&before); err != nil {
			t.Fatal(err)
		}
		w := request(http.MethodPost, "/api/v1/assets/bulk", map[string]any{"action": "export", "ids": ids,
			"params": map[string]any{"format": format}})
		var body struct {
			JobID string `json:"job_id"`
			Kind  string `json:"kind"`
		}
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &body) != nil || body.JobID == "" {
			t.Fatalf("bulk export %s: %d %s, want 202 with the export job", format, w.Code, w.Body.String())
		}
		if loc := w.Header().Get("Location"); loc != "/api/v1/exports/"+body.JobID || body.Kind != ci.BulkExportJobKind {
			t.Errorf("bulk export %s: Location %q, kind %q", format, loc, body.Kind)
		}
		var after int
		var entity, format2 string
		var storedIDs []string
		if err := f.Admin.QueryRow(bg, `SELECT (SELECT count(*) FROM export_job WHERE organization_id = $1),
			filters->>'entity', format, ARRAY(SELECT jsonb_array_elements_text(filters->'ids'))
			FROM export_job WHERE id = $2`, f.OrgA, body.JobID).Scan(&after, &entity, &format2, &storedIDs); err != nil {
			t.Fatal(err)
		}
		if after != before+1 || entity != export.EntityAsset || format2 != format ||
			!slices.Equal(storedIDs, []string{own, second, foreign, deleted}) {
			t.Errorf("bulk export %s: %d new jobs, entity %q, format %q, ids %v; want one job of the canonical ids",
				format, after-before, entity, format2, storedIDs)
		}
		return body.JobID
	}
	content := func(jobID string) (status, errMsg, body string) {
		t.Helper()
		for worker.ProcessOne(bg) {
		}
		var key string
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(error_message, ''), COALESCE(object_key, '') FROM export_job WHERE id = $1`,
			jobID).Scan(&status, &errMsg, &key); err != nil {
			t.Fatal(err)
		}
		if key == "" {
			return status, errMsg, ""
		}
		rc, err := blobs.Get(bg, export.ExportBucket, key)
		if err != nil {
			t.Fatal(err)
		}
		defer rc.Close()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return status, errMsg, string(data)
	}
	manifestOf := func(jobID string) export.Manifest {
		t.Helper()
		w := request(http.MethodGet, "/api/v1/exports/"+jobID, nil)
		var body struct {
			Manifest export.Manifest `json:"manifest"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil {
			t.Fatalf("get export %s: %d %s", jobID, w.Code, w.Body.String())
		}
		return body.Manifest
	}
	const bom = "\ufeff"
	permOmitted := export.OmittedField{Field: "serial_number", Reason: export.ReasonFieldPermission}

	// CSV: BOM, semicolons, the visible, not deleted assets by created_at.
	csvJob := bulkExport("csv")
	status, msg, out := content(csvJob)
	if status != export.JobStatusCompleted {
		t.Fatalf("csv job: %s %s", status, msg)
	}
	if !strings.HasPrefix(out, bom) {
		t.Error("csv without BOM")
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(out, bom)))
	r.Comma = ';'
	records, err := r.ReadAll()
	if err != nil || len(records) != 3 {
		t.Fatalf("csv %q: %v, want header and two rows", out, err)
	}
	header := records[0]
	idCol, tagCol := slices.Index(header, "id"), slices.Index(header, "inventory_no")
	if idCol < 0 || tagCol < 0 || slices.Contains(header, "serial_number") || slices.Contains(header, "custom_fields") {
		t.Fatalf("csv header %v", header)
	}
	if records[1][idCol] != own || records[2][idCol] != second || records[2][tagCol] != "'=WP782-SECOND" {
		t.Errorf("csv rows %v, want own then second (formula neutralised)", records[1:])
	}
	if strings.Contains(out, "SN-782") || strings.Contains(out, "WP782-FOREIGN") || strings.Contains(out, "WP782-DELETED") {
		t.Errorf("csv carries a closed field, a foreign or a deleted asset: %s", out)
	}
	if m := manifestOf(csvJob); m.Format != "csv" || slices.Contains(m.Fields, "serial_number") || !slices.Contains(m.Omitted, permOmitted) ||
		!slices.Contains(m.Omitted, export.OmittedField{Field: "custom_fields", Reason: export.ReasonLayout}) {
		t.Errorf("csv manifest %+v", m)
	}

	// JSON: the DTO of each asset without the closed field.
	jsonJob := bulkExport("json")
	status, msg, out = content(jsonJob)
	var file struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	if status != export.JobStatusCompleted || json.Unmarshal([]byte(out), &file) != nil {
		t.Fatalf("json job: %s %s %s", status, msg, out)
	}
	if file.Total != 2 || len(file.Data) != 2 || file.Data[0]["id"] != own || file.Data[1]["id"] != second {
		t.Fatalf("json %s, want own and second", out)
	}
	for _, item := range file.Data {
		if _, ok := item["serial_number"]; ok || item["inventory_no"] == nil {
			t.Errorf("json asset %v", item)
		}
	}
	if m := manifestOf(jsonJob); m.Format != "json" || !slices.Contains(m.Omitted, permOmitted) {
		t.Errorf("json manifest %+v", m)
	}

	// Without export_csv the whole bulk is 403.
	features.on = false
	if w := request(http.MethodPost, "/api/v1/assets/bulk", map[string]any{"action": "export", "ids": []string{own},
		"params": map[string]any{"format": "csv"}}); w.Code != http.StatusForbidden {
		t.Errorf("bulk export without export_csv: %d %s, want 403", w.Code, w.Body.String())
	}
	features.on = true

	// POST /api/v1/exports takes no asset export.
	if w := request(http.MethodPost, "/api/v1/exports", map[string]any{"format": "csv",
		"filters": map[string]any{"entity": "asset", "ids": []string{own}}}); w.Code != http.StatusBadRequest {
		t.Errorf("POST /exports with entity asset: %d %s, want 400", w.Code, w.Body.String())
	}

	// A worker without AssetRows fails the asset job (fail closed).
	worker.AssetRows = nil
	failedJob := bulkExport("csv")
	if status, msg, _ = content(failedJob); status != export.JobStatusFailed || msg != export.ErrAssetExportUnavailable.Error() {
		t.Errorf("job without asset source: %s %q, want failed with %q", status, msg, export.ErrAssetExportUnavailable.Error())
	}

	// The scope of the job is the snapshot of its creator, client 1.
	var clients []string
	if err := f.Admin.QueryRow(bg, `SELECT scope_clients::text[] FROM export_job WHERE id = $1`, csvJob).Scan(&clients); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(clients, []string{f.Client1}) {
		t.Errorf("scope snapshot of the job: clients %v, want client 1", clients)
	}
}
