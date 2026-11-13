package export_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// TestIPAMExport covers WP-921 (IPM-09) on PostgreSQL: POST
// /api/v1/exports with filters.entity subnet or ip_address renders CSV
// through ipam.PGRepository under the scope of the job; the files go
// unchanged through the import job of WP-920 (POST /api/v1/imports,
// parser, mapper and writer in RunJob) as a dry run without a row error
// (round trip); a client principal exports only the rows of its client
// and the org-wide ones, and a reference it does not see (a section of
// another client) stays empty; without ipam:read the route is 403;
// filters.ipam section_id narrows the rows (q trimmed); the neighbouring
// organization never shows.
//
// Retest:
//
//	cd backend && go test -p 1 ./internal/export/ -run TestIPAMExport
func TestIPAMExport(t *testing.T) {
	f := scopetest.Seed(t, "1c")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	id := func(sql string, args ...any) string {
		t.Helper()
		var v string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	org := f.OrgA
	lab := id(`INSERT INTO ipam_section (organization_id, name) VALUES ($1, 'wp921-lab') RETURNING id::text`, org)
	ns := id(`INSERT INTO nameserver_set (organization_id, name, servers) VALUES ($1, 'wp921-dns', '{10.93.0.53}') RETURNING id::text`, org)
	vlan := id(`INSERT INTO vlan (organization_id, vlan_id, name) VALUES ($1, 921, 'wp921') RETURNING id::text`, org)
	office := id(`INSERT INTO subnet (organization_id, client_id, cidr, name, description, vlan_id, gateway, nameserver_set_id)
		VALUES ($1, $2, '10.93.1.0/24', 'office', 'Büro; 1. OG', $3, '10.93.1.1', $4) RETURNING id::text`, org, f.Client1, vlan, ns)
	foreign := id(`INSERT INTO subnet (organization_id, client_id, cidr, section_id, description)
		VALUES ($1, $2, '10.93.2.0/24', $3, 'client 2') RETURNING id::text`, org, f.Client2, lab)
	shared := id(`INSERT INTO subnet (organization_id, cidr, description) VALUES ($1, '10.93.3.0/24', 'shared') RETURNING id::text`, org)
	neighbour := id(`INSERT INTO subnet (organization_id, cidr) VALUES ($1, '10.93.9.0/24') RETURNING id::text`, f.OrgB)
	// An org-wide subnet in a section of client 2: client 1 sees the
	// subnet, not its section (AK5: the reference stays empty).
	hiddenSection := id(`INSERT INTO ipam_section (organization_id, client_id, name) VALUES ($1, $2, 'wp921-client2') RETURNING id::text`,
		org, f.Client2)
	hiddenRef := id(`INSERT INTO subnet (organization_id, cidr, section_id, description) VALUES ($1, '10.93.4.0/24', $2, 'hidden ref')
		RETURNING id::text`, org, hiddenSection)

	device := f.CI(t, org, f.Client1, "wp921-device")
	nic := f.ID()
	exec(`INSERT INTO network_interface (id, organization_id, ci_id, name, mac) VALUES ($1, $2, $3, 'eth0', '02:00:00:09:21:01')`, nic, org, device)
	bound := id(`INSERT INTO ip_address (organization_id, subnet_id, interface_id, address, type, dns_name)
		VALUES ($1, $2, $3, '10.93.1.10', 'static', 'srv.wp921.test') RETURNING id::text`, org, office, nic)
	gateway := id(`INSERT INTO ip_address (organization_id, subnet_id, address, type) VALUES ($1, $2, '10.93.1.1', 'static')
		RETURNING id::text`, org, office)
	// The repository marks the gateway address (ipam_subnet_sync_gateway).
	exec(`SELECT ipam_subnet_sync_gateway($1, $2, '10.93.1.1')`, org, office)
	other := id(`INSERT INTO ip_address (organization_id, subnet_id, address, type) VALUES ($1, $2, '10.93.2.5', 'discovered')
		RETURNING id::text`, org, foreign)
	reserved := id(`INSERT INTO ip_address (organization_id, subnet_id, address, type, description, state)
		VALUES ($1, $2, '10.93.3.7', 'reserved', 'printer', 'offline') RETURNING id::text`, org, shared)

	jobRepo := export.NewPGJobRepository(f.App)
	blobs := blob.NewFileStore(t.TempDir())
	worker := export.NewJobWorker(jobRepo, ci.NewPGRepository(f.App), blobs)
	repo := ipam.NewPGRepository(f.App)
	worker.IPAMRows = repo
	jobHandler := export.NewJobHandler(jobRepo, worker, blobs)
	mux := chi.NewRouter()
	jobHandler.RegisterRoutes(mux)

	reader := []identity.Permission{identity.PermExportRun, identity.PermIPAMRead}
	request := func(clientID string, perms []identity.Permission, body any) *httptest.ResponseRecorder {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/api/v1/exports", bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		ctx := f.OrgCtx(org)
		if clientID != "" {
			ctx = f.ClientCtx(clientID)
		}
		ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: org, UserID: f.User, ClientID: clientID})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: f.User, OrganizationID: org, Type: identity.PrincipalTypeUser,
			Permissions: perms})
		ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(perms))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w
	}
	// exportFile runs an export to its end and returns the file.
	exportFile := func(clientID, entity string, ipamFilters map[string]any) []byte {
		t.Helper()
		filters := map[string]any{"entity": entity}
		if ipamFilters != nil {
			filters["ipam"] = ipamFilters
		}
		w := request(clientID, reader, map[string]any{"format": "csv", "filters": filters})
		var job struct {
			ID       string          `json:"id"`
			Manifest export.Manifest `json:"manifest"`
		}
		if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &job) != nil {
			t.Fatalf("export %s: %d %s", entity, w.Code, w.Body.String())
		}
		if len(job.Manifest.Omitted) != 0 || job.Manifest.Fields[0] != "id" {
			t.Errorf("manifest of the queued %s export: %+v", entity, job.Manifest)
		}
		for worker.ProcessOne(bg) {
		}
		var status, msg, key string
		if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(error_message, ''), COALESCE(object_key, '') FROM export_job WHERE id = $1`,
			job.ID).Scan(&status, &msg, &key); err != nil {
			t.Fatal(err)
		}
		if status != export.JobStatusCompleted {
			t.Fatalf("export %s: %s %s", entity, status, msg)
		}
		rc, err := blobs.Get(bg, export.ExportBucket, key)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rc.Close() }()
		data, err := io.ReadAll(rc)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	// exportRows runs an export and returns the rows of the file, read by
	// the CSV parser of the import (BOM and separator detection).
	exportRows := func(clientID, entity string, ipamFilters map[string]any) [][]string {
		t.Helper()
		return readIPAMCSV(t, exportFile(clientID, entity, ipamFilters))
	}
	ids := func(rows [][]string) []string {
		var out []string
		for _, r := range rows[1:] {
			out = append(out, r[0])
		}
		slices.Sort(out)
		return out
	}
	sorted := func(v ...string) []string { slices.Sort(v); return v }

	subnets := exportRows("", export.EntitySubnet, nil)
	if !slices.Equal(subnets[0], export.SubnetExportColumns) {
		t.Fatalf("header %v", subnets[0])
	}
	if got := ids(subnets); !slices.Equal(got, sorted(office, foreign, shared, hiddenRef)) || slices.Contains(got, neighbour) {
		t.Fatalf("subnets %v, want the four of organization A", got)
	}
	for _, row := range subnets[1:] {
		switch row[0] {
		case office:
			want := []string{office, "default", "10.93.1.0/24", "Büro; 1. OG", "921", "global", "10.93.1.1", "Client 1", "wp921-dns"}
			if !slices.Equal(row, want) {
				t.Errorf("office row %q, want %q", row, want)
			}
		case hiddenRef:
			if row[1] != "wp921-client2" {
				t.Errorf("org-wide export: section of %q, want wp921-client2", row)
			}
		}
	}
	addresses := exportRows("", export.EntityIPAddress, nil)
	if got := ids(addresses); !slices.Equal(got, sorted(bound, gateway, other, reserved)) {
		t.Fatalf("addresses %v", got)
	}
	for _, row := range addresses[1:] {
		switch row[0] {
		case bound:
			if want := []string{bound, "10.93.1.10", "srv.wp921.test", "", "02:00:00:09:21:01", "unknown", "", "global", "10.93.1.0/24", "static"}; !slices.Equal(row, want) {
				t.Errorf("bound row %q, want %q", row, want)
			}
		case gateway:
			if row[6] != "1" {
				t.Errorf("gateway row %q, want is_gateway 1", row)
			}
		case other:
			if row[9] != "" {
				t.Errorf("discovered row %q, want an empty type", row)
			}
		}
	}

	// Round trip: each file reads back as a dry run of the import of the
	// same entity without a row error.
	// The files go unchanged through the import job (parser, mapper and
	// writer of WP-920) as a dry run.
	dry := newDryRunImport(t, f, repo)
	dry.run(t, export.EntitySubnet, exportFile("", export.EntitySubnet, nil), len(subnets)-1)
	dry.run(t, export.EntityIPAddress, exportFile("", export.EntityIPAddress, nil), len(addresses)-1)

	// A client principal exports the rows of its client and the org-wide;
	// a reference it does not see (the section of client 2) stays empty.
	clientSubnets := exportRows(f.Client1, export.EntitySubnet, nil)
	if got := ids(clientSubnets); !slices.Equal(got, sorted(office, shared, hiddenRef)) {
		t.Errorf("subnets of client 1 %v", got)
	}
	for _, row := range clientSubnets[1:] {
		if row[0] == hiddenRef {
			if want := []string{hiddenRef, "", "10.93.4.0/24", "hidden ref", "", "global", "", "", ""}; !slices.Equal(row, want) {
				t.Errorf("client 1: row with a hidden section %q, want %q", row, want)
			}
		}
	}
	if got := ids(exportRows(f.Client1, export.EntityIPAddress, nil)); !slices.Equal(got, sorted(bound, gateway, reserved)) {
		t.Errorf("addresses of client 1 %v", got)
	}

	// filters.ipam narrows the rows.
	if got := ids(exportRows("", export.EntitySubnet, map[string]any{"section_id": lab, "q": " 10.93 "})); !slices.Equal(got, []string{foreign}) {
		t.Errorf("subnets of section lab %v", got)
	}
	if got := ids(exportRows("", export.EntityIPAddress, map[string]any{"subnet_id": office, "type": "static"})); !slices.Equal(got, sorted(bound, gateway)) {
		t.Errorf("static addresses of the office %v", got)
	}

	// Without ipam:read 403; another format 400.
	if w := request("", []identity.Permission{identity.PermExportRun}, map[string]any{"format": "csv",
		"filters": map[string]any{"entity": export.EntitySubnet}}); w.Code != http.StatusForbidden {
		t.Errorf("without ipam:read: %d %s, want 403", w.Code, w.Body.String())
	}
	if w := request("", reader, map[string]any{"format": "json", "filters": map[string]any{"entity": export.EntityIPAddress}}); w.Code != http.StatusBadRequest {
		t.Errorf("json: %d, want 400", w.Code)
	}

	// Without the source (memory profile) the job fails (fail closed).
	worker.IPAMRows = nil
	w := request("", reader, map[string]any{"format": "csv", "filters": map[string]any{"entity": export.EntitySubnet}})
	var job struct {
		ID string `json:"id"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &job) != nil {
		t.Fatalf("export without source: %d", w.Code)
	}
	for worker.ProcessOne(bg) {
	}
	var status, msg string
	if err := f.Admin.QueryRow(bg, `SELECT status, COALESCE(error_message, '') FROM export_job WHERE id = $1`, job.ID).Scan(&status, &msg); err != nil {
		t.Fatal(err)
	}
	if status != export.JobStatusFailed || msg != export.ErrIPAMExportUnavailable.Error() {
		t.Errorf("without source: %s %q, want failed with ErrIPAMExportUnavailable", status, msg)
	}
}

// dryRunImport is the import stack of WP-920 on PostgreSQL: profiles,
// documents, the job service and the IPAM writers. A run goes through
// POST /api/v1/imports and the job handler (RunJob) like a queued import.
type dryRunImport struct {
	f     *scopetest.Fixture
	user  string
	jobs  *jobs.Service
	h     *importer.ImportHandler
	mux   chi.Router
	files blob.Store
	docs  *document.PGRepository
	prof  *importer.PGRepository
}

// enqueueOnly queues the job without running it; the test runs it.
type enqueueOnly struct{ svc *jobs.Service }

func (q enqueueOnly) Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	return q.svc.Enqueue(ctx, orgID, kind, params, opts)
}

func newDryRunImport(t *testing.T, f *scopetest.Fixture, repo *ipam.PGRepository) *dryRunImport {
	t.Helper()
	bg := context.Background()
	d := &dryRunImport{f: f, user: f.AppUser(t, f.OrgA, "wp921-import"), files: blob.NewFileStore(t.TempDir()),
		docs: document.NewPGRepository(f.App), prof: importer.NewPGRepository(f.App)}
	role := f.ID()
	if _, err := f.Admin.Exec(bg, `INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp921-import', '["import:run","ipam:write"]')`,
		role, f.OrgA); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, d.user, role); err != nil {
		t.Fatal(err)
	}
	d.jobs = jobs.NewService(jobs.NewPGRepository(f.App))
	d.jobs.Register(jobs.Kind{Name: importer.JobKind, MaxAttempts: 1, Timeout: 30 * time.Minute, Cancellable: true})
	d.h = importer.NewImportHandler(d.prof, d.docs, d.files).WithJobs(d.jobs).WithQueue(enqueueOnly{svc: d.jobs}).
		WithStarters(user.NewPGRepository(f.App), permission.NewPGRepository(f.App)).
		WithWriter(importer.NewSubnetWriter(repo, d.prof)).
		WithWriter(importer.NewAddressWriter(repo, d.prof, reviews.NewService(reviews.NewPGRepository(f.App))))
	d.mux = chi.NewRouter()
	d.h.RegisterRoutes(d.mux)
	return d
}

// run imports data as a dry run of entity with the identity mapping of its
// header (id unmapped) and fails unless all want rows pass without a row
// error.
func (d *dryRunImport) run(t *testing.T, entity string, data []byte, want int) {
	t.Helper()
	f, org, bg := d.f, d.f.OrgA, context.Background()
	octx := f.OrgCtx(org)
	header := readIPAMCSV(t, data)[0]
	cols := map[string]importer.ColumnMapping{}
	for _, c := range header {
		if c != "id" {
			cols[c] = importer.ColumnMapping{Target: c}
		}
	}
	p := &importer.Profile{OrganizationID: org, Name: "wp921-" + entity + "-" + f.ID(), Entity: entity, Format: importer.FormatCSV,
		Mode: "both", Mapping: importer.Mapping{Columns: cols}}
	if err := d.prof.Create(octx, p); err != nil {
		t.Fatalf("profile: %v", err)
	}
	doc := &document.Document{OrganizationID: org, Title: "wp921", FileName: entity + ".csv", UploadedBy: d.user, Category: "general"}
	if err := d.docs.Create(octx, doc); err != nil {
		t.Fatalf("document: %v", err)
	}
	versionID := f.ID()
	key := blob.OrgKey(org, "documents", doc.ID, versionID)
	if _, err := d.docs.CreateVersion(octx, &document.Version{ID: versionID, OrganizationID: org, DocumentID: doc.ID, StorageKey: key,
		Size: int64(len(data)), MimeType: "text/csv", UploadedBy: d.user}, nil); err != nil {
		t.Fatalf("version: %v", err)
	}
	if err := d.docs.SetScanResult(octx, org, doc.ID, 1, document.ScanClean, ""); err != nil {
		t.Fatalf("scan: %v", err)
	}
	if err := d.files.Put(bg, document.Bucket, key, bytes.NewReader(data), int64(len(data)), "text/csv"); err != nil {
		t.Fatal(err)
	}

	scope := database.OrgWideScope(org, d.user)
	ctx := tenant.WithTenant(bg, tenant.TenantInfo{OrganizationID: org, UserID: d.user})
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = audit.WithRequestInfo(ctx, audit.RequestInfo{RequestID: "req-wp921", Source: audit.SourceUI})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: d.user, OrganizationID: org, Type: identity.PrincipalTypeUser,
		Permissions: []identity.Permission{identity.PermImportRun, identity.PermIPAMWrite}})
	body := fmt.Sprintf(`{"document_id":%q,"profile_id":%q,"dry_run":true}`, doc.ID, p.ID)
	w := httptest.NewRecorder()
	d.mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/imports", strings.NewReader(body)).WithContext(ctx))
	var accepted struct {
		JobID string `json:"job_id"`
	}
	if w.Code != http.StatusAccepted || json.Unmarshal(w.Body.Bytes(), &accepted) != nil {
		t.Fatalf("start %s import: %d %s", entity, w.Code, w.Body.String())
	}
	claimed, err := d.jobs.ClaimJob(octx, org, accepted.JobID)
	if err != nil || claimed == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err = d.h.RunJob(bg, claimed); err != nil {
		t.Fatalf("run %s import: %v", entity, err)
	}
	job, err := d.jobs.Get(octx, org, accepted.JobID)
	if err != nil {
		t.Fatal(err)
	}
	var res importer.JobResult
	if job.Status != jobs.StatusSucceeded || json.Unmarshal(job.Result, &res) != nil {
		t.Fatalf("%s import: %s %s", entity, job.Status, job.Error)
	}
	if !res.DryRun || res.Total != want || res.Failed != 0 || res.Created+res.Updated+res.Skipped != want {
		rc, gerr := d.files.Get(bg, export.ExportBucket, importer.ReportKey(org, job.ID))
		report := []byte{}
		if gerr == nil {
			report, _ = io.ReadAll(rc)
			_ = rc.Close()
		}
		t.Fatalf("%s dry run %+v, want %d rows without a row error; report:\n%s", entity, res, want, report)
	}
	// The dry run wrote nothing.
	var n int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM ipam_change WHERE organization_id = $1 AND source = 'import'`, org).Scan(&n); err != nil || n != 0 {
		t.Errorf("ipam_change of the dry run: %d %v, want none", n, err)
	}
}
