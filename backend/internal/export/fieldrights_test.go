package export

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Field permissions in the export (RBA-07, WP-729): the streaming export,
// RenderFormat and the job path project CSV, DATEV and JSON with the same
// projection; a column not readable for one of the exported types drops out
// of the CSV (empty in DATEV, missing in JSON), a field not readable on one
// CI only leaves its cell empty; the manifest names both with reason
// field_permission next to layout, the header X-Export-Omitted-Fields
// matches it, and the job stores the manifest of its file next to it.

const (
	frOrg        = "00000000-0000-4000-8000-00000000f001"
	frUser       = "00000000-0000-4000-8000-00000000f002"
	frRole       = "00000000-0000-4000-8000-00000000f003"
	frRoleNarrow = "00000000-0000-4000-8000-00000000f004"
	frServer     = "00000000-0000-4000-8000-00000000f005"
	frSwitch     = "00000000-0000-4000-8000-00000000f006"
	frClient1    = "00000000-0000-4000-8000-00000000f007"
	frClient2    = "00000000-0000-4000-8000-00000000f008"
)

// frAccess resolves the role assignments of the user; grants can change
// between the creation and the rendering of a job.
type frAccess struct {
	grants []identity.Grant
	err    error
}

func (a *frAccess) AccessGrants(_ context.Context, _, userID string) ([]identity.Grant, error) {
	if userID != frUser {
		return nil, nil
	}
	return a.grants, a.err
}

func (a *frAccess) RoleGrants(context.Context, string, []string) ([]identity.Grant, error) {
	return nil, nil
}

func ptrTo(s string) *string { return &s }

// frWorld is a memory CI repository with a server CI of client 1 and a
// switch CI of client 2, and field permissions: serial_number is not
// readable for servers, attributes.cost_center is sensitive (no grant opens
// it), the switch CI is closed by an object grant none, and the role
// frRoleNarrow does not read manufacturer.
type frWorld struct {
	cis      *ci.MemoryRepository
	server   *ci.Item
	sw       *ci.Item
	grants   *permission.MemoryFieldGrants
	access   *frAccess
	resolver *permission.FieldResolver
}

func newFRWorld(t *testing.T) *frWorld {
	t.Helper()
	bg := context.Background()
	w := &frWorld{cis: ci.NewMemoryRepository(), grants: permission.NewMemoryFieldGrants()}
	w.server = &ci.Item{OrganizationID: frOrg, CITypeID: frServer, ClientID: frClient1, Name: "srv-1", Status: "active",
		Manufacturer: "Dell", Model: "R650", SerialNumber: "SN-SRV", ManagementIP: "10.0.0.1",
		Attributes: map[string]any{"cost_center": "4711", "location": "Berlin", "purchase_date": "2024-01-02"}}
	w.sw = &ci.Item{OrganizationID: frOrg, CITypeID: frSwitch, ClientID: frClient2, Name: "sw-1", Status: "active",
		Manufacturer: "Cisco", Model: "C9300", SerialNumber: "SN-SW",
		Attributes: map[string]any{"cost_center": "0815", "location": "Munich"}}
	for _, item := range []*ci.Item{w.server, w.sw} {
		if err := w.cis.Create(bg, item); err != nil {
			t.Fatal(err)
		}
	}
	for _, g := range []permission.FieldGrant{
		{RoleID: ptrTo(frRole), ObjectKind: permission.KindCI, CITypeID: ptrTo(frServer), FieldKey: ptrTo("core.serial_number"), Level: permission.LevelNone},
		{RoleID: ptrTo(frRoleNarrow), ObjectKind: permission.KindCI, FieldKey: ptrTo("core.manufacturer"), Level: permission.LevelNone},
	} {
		if _, err := w.grants.CreateFieldGrant(bg, frOrg, &g); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.grants.CreateSensitiveField(bg, frOrg, &permission.SensitiveField{ObjectKind: permission.KindCI,
		FieldKey: "attributes.cost_center"}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.grants.CreateObjectGrant(bg, frOrg, &permission.ObjectGrant{GranteeRoleID: ptrTo(frRole),
		ObjectKind: permission.KindCI, ObjectID: w.sw.ID, Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	w.access = &frAccess{grants: []identity.Grant{frGrant(frRole)}}
	w.resolver = permission.NewFieldResolver(w.access, nil, w.grants, nil)
	return w
}

func frGrant(roleID string) identity.Grant {
	return identity.Grant{RoleID: roleID, Permissions: []identity.Permission{identity.PermCIRead, identity.PermExportRun,
		identity.PermAssetRead}}
}

// policy resolves the policy of the user with its current grants.
func (w *frWorld) policy(t *testing.T) *permission.FieldPolicy {
	t.Helper()
	p, err := w.resolver.Resolve(context.Background(), identity.Principal{Subject: frUser, OrganizationID: frOrg,
		Type: identity.PrincipalTypeUser, Permissions: identity.AllPermissions()})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// request is a request of the user with the policy source of
// FieldPolicyMiddleware.
func (w *frWorld) request(method, target, body string, policy *permission.FieldPolicy) *http.Request {
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	ctx := tenant.WithTenant(r.Context(), tenant.TenantInfo{OrganizationID: frOrg, UserID: frUser})
	scope := database.OrgWideScope(frOrg, frUser)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	perms := []identity.Permission{identity.PermCIRead, identity.PermExportRun, identity.PermAssetRead}
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: frUser, OrganizationID: frOrg, Permissions: perms})
	return r.WithContext(permission.WithPolicy(ctx, policy))
}

func readCSV(t *testing.T, body string) [][]string {
	t.Helper()
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, utf8BOM)))
	r.Comma = ';'
	records, err := r.ReadAll()
	if err != nil {
		t.Fatalf("parse %q: %v", body, err)
	}
	return records
}

// rowsByName maps the rows of a CSV by the cell in column name to the
// cells by header.
func rowsByName(t *testing.T, records [][]string, name string) map[string]map[string]string {
	t.Helper()
	out := map[string]map[string]string{}
	for _, rec := range records[1:] {
		row := map[string]string{}
		for i, cell := range rec {
			row[records[0][i]] = cell
		}
		out[row[name]] = row
	}
	return out
}

func omittedOf(m *Manifest) map[string]string {
	out := map[string]string{}
	for _, o := range m.Omitted {
		out[o.Field] = o.Reason
	}
	return out
}

func TestExportFieldRightsCSV(t *testing.T) {
	w := newFRWorld(t)
	policy := w.policy(t)
	mux := chi.NewRouter()
	NewHandler(w.cis).RegisterRoutes(mux)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, w.request(http.MethodGet, "/api/v1/export/cis?format=csv", "", policy))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	records := readCSV(t, rec.Body.String())
	// serial_number is not readable for servers: the column drops out.
	if slices.Contains(records[0], "serial_number") || !slices.Contains(records[0], "manufacturer") {
		t.Fatalf("header %v, want manufacturer without serial_number", records[0])
	}
	rows := rowsByName(t, records, "name")
	if rows["srv-1"]["manufacturer"] != "Dell" || rows["srv-1"]["management_ip"] != "10.0.0.1" {
		t.Errorf("server row %v, want its readable cells", rows["srv-1"])
	}
	// The object grant none closes the fields of the switch only: its cells
	// are empty, the structural ones stay.
	if sw := rows["sw-1"]; sw["manufacturer"] != "" || sw["model"] != "" || sw["status"] != "active" || sw["id"] != w.sw.ID {
		t.Errorf("switch row %v, want empty projected cells and the structural ones", sw)
	}
	if strings.Contains(rec.Body.String(), "SN-") || strings.Contains(rec.Body.String(), "Cisco") {
		t.Errorf("export leaks a closed value: %s", rec.Body.String())
	}

	// The manifest and its header agree: serial_number with reason
	// field_permission, the layout omissions with reason layout.
	m := ManifestForPolicy("csv", policy, nil)
	header := rec.Header().Get("X-Export-Omitted-Fields")
	if header != strings.Join(m.omittedNames(), ",") {
		t.Errorf("X-Export-Omitted-Fields %q, manifest %v", header, m.omittedNames())
	}
	omitted := omittedOf(&m)
	if omitted["serial_number"] != ReasonFieldPermission || omitted["hostname"] != ReasonLayout || slices.Contains(m.Fields, "serial_number") {
		t.Errorf("csv manifest %+v", m)
	}
	if !slices.Equal(m.Fields, records[0]) {
		t.Errorf("manifest fields %v, header %v", m.Fields, records[0])
	}

	// Restricted to switches the column is readable for every exported type:
	// it stays, the switch's cell stays empty (object grant).
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, w.request(http.MethodGet, "/api/v1/export/cis?format=csv&ci_type_id="+frSwitch, "", policy))
	records = readCSV(t, rec.Body.String())
	if !slices.Contains(records[0], "serial_number") || len(records) != 2 {
		t.Fatalf("switch export %v, want serial_number and one row", records)
	}
	if cell := rowsByName(t, records, "name")["sw-1"]["serial_number"]; cell != "" {
		t.Errorf("switch serial_number %q, want an empty cell", cell)
	}
	if strings.Contains(rec.Header().Get("X-Export-Omitted-Fields"), "serial_number") {
		t.Errorf("omitted %q names serial_number", rec.Header().Get("X-Export-Omitted-Fields"))
	}
}

func TestExportFieldRightsDATEV(t *testing.T) {
	w := newFRWorld(t)
	policy := w.policy(t)
	var out bytes.Buffer
	n, m, err := RenderProjected(context.Background(), w.cis, frOrg, FormatDATEV, JobFilters{}, policy, &out)
	if err != nil || n != 2 {
		t.Fatalf("render: %d %v", n, err)
	}
	records := readCSV(t, out.String())
	// The fixed layout keeps every column.
	if len(records[0]) != 9 {
		t.Fatalf("datev header %v", records[0])
	}
	rows := rowsByName(t, records, "Bezeichnung")
	srv := rows["srv-1"]
	if srv["Seriennummer"] != "" || srv["Kostenstelle"] != "" || srv["Hersteller"] != "Dell" || srv["Standort"] != "Berlin" ||
		srv["Anschaffungsdatum"] != "2024-01-02" {
		t.Errorf("server row %v, want empty Seriennummer and Kostenstelle", srv)
	}
	if sw := rows["sw-1"]; sw["Hersteller"] != "" || sw["Standort"] != "" || sw["Status"] != "active" {
		t.Errorf("switch row %v, want empty cells under the object grant", sw)
	}
	for _, leak := range []string{"SN-", "4711", "0815", "Cisco", "Munich"} {
		if strings.Contains(out.String(), leak) {
			t.Errorf("datev export leaks %q: %s", leak, out.String())
		}
	}
	omitted := omittedOf(&m)
	if omitted["serial_number"] != ReasonFieldPermission || omitted["attributes.cost_center"] != ReasonFieldPermission ||
		!slices.Contains(m.Fields, "attributes") {
		t.Errorf("datev manifest %+v", m)
	}
}

func TestExportFieldRightsJSON(t *testing.T) {
	w := newFRWorld(t)
	policy := w.policy(t)
	mux := chi.NewRouter()
	NewHandler(w.cis).RegisterRoutes(mux)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, w.request(http.MethodGet, "/api/v1/export/cis?format=json", "", policy))
	if rec.Code != http.StatusOK {
		t.Fatalf("export: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data  []map[string]any `json:"data"`
		Total int              `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Total != 2 {
		t.Fatalf("json export %s: %v", rec.Body.String(), err)
	}
	byName := map[string]map[string]any{}
	for _, obj := range body.Data {
		byName[obj["name"].(string)] = obj
	}
	srv, sw := byName["srv-1"], byName["sw-1"]
	attrs, _ := srv["attributes"].(map[string]any)
	if _, has := srv["serial_number"]; has || srv["manufacturer"] != "Dell" || attrs["location"] != "Berlin" {
		t.Errorf("server object %v", srv)
	}
	if _, has := attrs["cost_center"]; has {
		t.Errorf("server attributes %v carry the sensitive cost_center", attrs)
	}
	if _, has := sw["manufacturer"]; has || sw["id"] != w.sw.ID {
		t.Errorf("switch object %v, want the structural members only", sw)
	}
	if swAttrs, _ := sw["attributes"].(map[string]any); len(swAttrs) != 0 {
		t.Errorf("switch attributes %v under the object grant none", swAttrs)
	}
	m := ManifestForPolicy("json", policy, nil)
	if rec.Header().Get("X-Export-Omitted-Fields") != strings.Join(m.omittedNames(), ",") {
		t.Errorf("header %q, manifest %+v", rec.Header().Get("X-Export-Omitted-Fields"), m)
	}
	omitted := omittedOf(&m)
	if omitted["serial_number"] != ReasonFieldPermission || omitted["attributes.cost_center"] != ReasonFieldPermission || len(omitted) != 2 {
		t.Errorf("json manifest %+v", m)
	}

	// A nil policy reads no field with field permissions (fail-closed).
	var out bytes.Buffer
	if _, _, err := RenderProjected(context.Background(), w.cis, frOrg, "json", JobFilters{}, nil, &out); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"Dell", "Cisco", "R650", "SN-", "4711", "Berlin", "10.0.0.1"} {
		if strings.Contains(out.String(), leak) {
			t.Errorf("export with a nil policy leaks %q: %s", leak, out.String())
		}
	}
}

// TestExportWithoutFieldRightsUnchanged: without field permissions the
// export is the one of phase 1: the JSON objects are the encoded CIs, the
// CSV carries every column of its layout, the manifest only layout reasons.
func TestExportWithoutFieldRightsUnchanged(t *testing.T) {
	w := newFRWorld(t)
	empty := permission.NewFieldResolver(w.access, nil, permission.NewMemoryFieldGrants(), nil)
	policy, err := empty.Resolve(context.Background(), identity.Principal{Subject: frUser, OrganizationID: frOrg,
		Type: identity.PrincipalTypeUser, Permissions: frGrant(frRole).Permissions})
	if err != nil {
		t.Fatal(err)
	}
	var got, want bytes.Buffer
	if _, _, rerr := RenderProjected(context.Background(), w.cis, frOrg, "json", JobFilters{}, policy, &got); rerr != nil {
		t.Fatal(rerr)
	}
	items, _, err := w.cis.List(context.Background(), frOrg, &ci.FilterParams{SortBy: "created_at", SortDir: "asc"}, api.PaginationParams{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	want.WriteString(`{"data":[`)
	enc := json.NewEncoder(&want)
	for i := range items {
		if i > 0 {
			want.WriteString(",")
		}
		_ = enc.Encode(&items[i])
	}
	want.WriteString(`],"total":2}`)
	if got.String() != want.String() {
		t.Errorf("json without field permissions:\n%s\nwant\n%s", got.String(), want.String())
	}
	for _, format := range []string{"csv", "datev", "json"} {
		m := ManifestForPolicy(format, policy, nil)
		if base := ManifestFor(format); !slices.Equal(m.Fields, base.Fields) || len(m.Omitted) != len(base.Omitted) {
			t.Errorf("%s manifest %+v, want %+v", format, m, base)
		}
		for _, o := range m.Omitted {
			if o.Reason != ReasonLayout {
				t.Errorf("%s omits %s with reason %q", format, o.Field, o.Reason)
			}
		}
	}
}

// TestExportJobFieldRights: the worker resolves the policy of the creator
// with its roles at render time (no rights are stored with the job),
// stores the manifest next to the file, and GET /api/v1/exports/{id}
// answers it; a pending job shows the preview for the caller. A resolution
// error fails the job.
func TestExportJobFieldRights(t *testing.T) {
	w := newFRWorld(t)
	jobs := NewMemoryJobRepository()
	blobs := blob.NewFileStore(t.TempDir())
	worker := NewJobWorker(jobs, w.cis, blobs)
	worker.Policies = w.resolver
	mux := chi.NewRouter()
	NewJobHandler(jobs, worker, blobs).RegisterRoutes(mux)
	bg := context.Background()

	scope := database.OrgWideScope(frOrg, frUser)
	job := &Job{Format: "csv", InitiatedBy: frUser, Scope: SnapshotScope(&scope)}
	if err := jobs.CreateJob(bg, frOrg, job); err != nil {
		t.Fatal(err)
	}
	// The pending job shows the preview with the caller's policy.
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, w.request(http.MethodGet, "/api/v1/exports/"+job.ID, "", w.policy(t)))
	var pending jobResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &pending); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("get pending job: %d %s", rec.Code, rec.Body.String())
	}
	if omittedOf(&pending.Manifest)["serial_number"] != ReasonFieldPermission {
		t.Errorf("pending manifest %+v, want the preview of the caller", pending.Manifest)
	}

	// The creator's role changes before the job runs: the narrow role reads
	// no manufacturer but every serial number.
	w.access.grants = []identity.Grant{frGrant(frRoleNarrow)}
	if !worker.ProcessOne(bg) {
		t.Fatal("no job claimed")
	}
	stored, err := jobs.GetJob(bg, frOrg, job.ID)
	if err != nil || stored.Status != JobStatusCompleted {
		t.Fatalf("job %+v: %v", stored, err)
	}
	read := func(key string) string {
		t.Helper()
		rc, gerr := blobs.Get(bg, ExportBucket, key)
		if gerr != nil {
			t.Fatalf("read %s: %v", key, gerr)
		}
		defer func() { _ = rc.Close() }()
		raw, _ := io.ReadAll(rc)
		return string(raw)
	}
	records := readCSV(t, read(stored.ObjectKey))
	if slices.Contains(records[0], "manufacturer") || !slices.Contains(records[0], "serial_number") {
		t.Errorf("rendered header %v, want the narrow role's columns at render time", records[0])
	}
	var manifest Manifest
	if err := json.Unmarshal([]byte(read(ManifestKey(stored))), &manifest); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(manifest.Fields, records[0]) || omittedOf(&manifest)["manufacturer"] != ReasonFieldPermission {
		t.Errorf("stored manifest %+v, header %v", manifest, records[0])
	}

	// GET answers the stored manifest, not the preview of the caller's
	// current policy (which hides serial_number again).
	w.access.grants = []identity.Grant{frGrant(frRole)}
	for _, path := range []string{"/api/v1/exports/" + job.ID, "/api/v1/export/jobs/" + job.ID} {
		rec = httptest.NewRecorder()
		mux.ServeHTTP(rec, w.request(http.MethodGet, path, "", w.policy(t)))
		var got jobResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK {
			t.Fatalf("get %s: %d %s", path, rec.Code, rec.Body.String())
		}
		if !slices.Equal(got.Manifest.Fields, manifest.Fields) || len(got.Manifest.Omitted) != len(manifest.Omitted) {
			t.Errorf("%s manifest %+v, want the stored %+v", path, got.Manifest, manifest)
		}
	}
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, w.request(http.MethodGet, "/api/v1/exports", "", w.policy(t)))
	if !strings.Contains(rec.Body.String(), `"field":"manufacturer","reason":"field_permission"`) {
		t.Errorf("list: %s, want the stored manifest", rec.Body.String())
	}

	// The job row holds no rights of its creator.
	raw, _ := json.Marshal(stored)
	for _, leak := range []string{"permissions", "grants", "roles", frRole} {
		if strings.Contains(string(raw), leak) {
			t.Errorf("job row %s carries %q", raw, leak)
		}
	}

	// A policy that cannot be resolved fails the job (fail-closed).
	w.access.err = errors.New("role store down")
	w.resolver.Invalidate(bg, frOrg)
	failing := &Job{Format: "json", InitiatedBy: frUser, Scope: SnapshotScope(&scope)}
	if err := jobs.CreateJob(bg, frOrg, failing); err != nil {
		t.Fatal(err)
	}
	worker.ProcessOne(bg)
	if got, _ := jobs.GetJob(bg, frOrg, failing.ID); got.Status != JobStatusFailed {
		t.Errorf("job with an unresolvable policy: %s, want failed", got.Status)
	}
	if _, err := blobs.Get(bg, ExportBucket, ObjectKey(failing)); err == nil {
		t.Error("the failed job stored an object")
	}
}
