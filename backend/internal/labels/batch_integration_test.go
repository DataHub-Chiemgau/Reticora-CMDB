package labels_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// batchBlobs keeps the objects in memory and signs URLs like the object
// store behind the frontend proxy (blob.S3Store with a public URL).
type batchBlobs struct {
	*blob.S3Store
	mu      sync.Mutex
	objects map[string][]byte
}

func newBatchBlobs(t *testing.T) *batchBlobs {
	t.Helper()
	signer, err := blob.NewS3Store("minio:9000", "access", "secret-key", false)
	if err != nil {
		t.Fatal(err)
	}
	if err = signer.SetPublicURL("https://cmdb.example/objects"); err != nil {
		t.Fatal(err)
	}
	return &batchBlobs{S3Store: signer, objects: map[string][]byte{}}
}

func (b *batchBlobs) Put(_ context.Context, bucket, key string, r io.Reader, _ int64, _ string) error {
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.objects[bucket+"/"+key] = raw
	return nil
}

func (b *batchBlobs) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return io.NopCloser(bytes.NewReader(b.objects[bucket+"/"+key])), nil
}

func (b *batchBlobs) Delete(_ context.Context, bucket, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.objects, bucket+"/"+key)
	return nil
}

// pdf returns the stored PDF of the job.
func (b *batchBlobs) pdf(orgID, jobID string) []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.objects[labels.BatchBucket+"/"+labels.BatchObjectKey(orgID, jobID)]
}

// batchDelivery is a job message handed to Queue.Process.
type batchDelivery struct {
	data  []byte
	acked bool
}

func (d *batchDelivery) Data() []byte                     { return d.data }
func (d *batchDelivery) Ack() error                       { d.acked = true; return nil }
func (d *batchDelivery) NakWithDelay(time.Duration) error { return nil }

// TestLabelBatch covers WP-763 (INV-03, E-135) against PostgreSQL: a batch
// over ids and over the subtree of a location (inventory area) is 202 with
// a job labels.batch that succeeds; its result names labels, pages and
// expiry, no object key and no URL. Objects outside the caller's scope are
// not in the PDF. Without the read permission of the kind 403, an
// invisible area 404. The creator downloads the PDF (200 with a URL below
// /objects/exports/, signed per request), another user gets 404, a failed
// batch 409. Without blob storage or job service 503. On the job queue
// the batch runs with the creator's scope and, without the creator's
// field policy, prints no restricted asset field (fail-closed).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
// The tag 2e is the one of the lookup test of this package; both run one
// after the other.
func TestLabelBatch(t *testing.T) {
	f := scopetest.Seed(t, "2e")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	// tree creates a site of the client with warehouse, zone, shelf and
	// bin and returns them.
	tree := func(clientID, name string) (site string, nodes []string) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			f.OrgA, clientID, name).Scan(&site); err != nil {
			t.Fatalf("seed site %s: %v", name, err)
		}
		parent := site
		for _, kind := range []string{"warehouse", "zone", "shelf", "bin"} {
			if err := f.Admin.QueryRow(bg, `INSERT INTO location (organization_id, kind, parent_id, name) VALUES ($1, $2, $3, $4) RETURNING id::text`,
				f.OrgA, kind, parent, name+"-"+kind).Scan(&parent); err != nil {
				t.Fatalf("seed %s of %s: %v", kind, name, err)
			}
			nodes = append(nodes, parent)
		}
		return site, nodes
	}
	_, treeA := tree(f.Client1, "bt-site-a")
	_, treeC := tree(f.Client2, "bt-site-c")
	whA, binA, whC, binC := treeA[0], treeA[3], treeC[0], treeC[3]

	asset := func(clientID, tag, name, location string) string {
		t.Helper()
		id := f.Asset(t, f.OrgA, clientID, tag)
		exec(`UPDATE asset SET name = $2, manufacturer = 'BTMAKER', model = 'BT1' WHERE id = $1`, id, name)
		if location != "" {
			exec(`UPDATE asset SET location_id = $2 WHERE id = $1`, id, location)
		}
		return id
	}
	alpha := asset(f.Client1, "BT-INV-1", "bt-alpha", whA)
	asset(f.Client1, "BT-INV-2", "bt-beta", binA)
	gamma := asset(f.Client2, "BT-INV-3", "bt-gamma", whC)
	delta := asset(f.Client1, "BT-INV-4", "bt-delta", "")
	consumable := func(clientID, name, bin string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO consumable (organization_id, client_id, name, sku) VALUES ($1, $2, $3, $3 || '-sku') RETURNING id::text`,
			f.OrgA, clientID, name).Scan(&id); err != nil {
			t.Fatalf("seed consumable %s: %v", name, err)
		}
		exec(`INSERT INTO stock_level (organization_id, consumable_id, location_id, on_hand) VALUES ($1, $2, $3, 3)`, f.OrgA, id, bin)
		return id
	}
	consumable(f.Client1, "bt-toner", binA)
	consumable(f.Client2, "bt-paper", binC)

	repo := labels.NewPGRepository(f.App)
	store := newBatchBlobs(t)
	svc := jobs.NewService(jobs.NewPGRepository(f.App))
	svc.Register(jobs.Kind{Name: labels.BatchJobKind, MaxAttempts: 1, Timeout: 10 * time.Minute, Cancellable: true})
	features := entitled(t, f.OrgA, entitlement.FeatureBarcode, entitlement.FeatureAssetsBasic, entitlement.FeatureStock)
	route := func(h *labels.BatchHandler) http.Handler {
		mux := chi.NewRouter()
		h.WithFeatures(features).RegisterRoutes(mux)
		return mux
	}
	inProcess := route(labels.NewBatchHandler(repo, store).WithJobs(svc))

	allPerms := []identity.Permission{identity.PermAssetRead, identity.PermStockRead, identity.PermLocationRead}
	type who struct {
		user  string
		scope *database.TenantScope
		perms []identity.Permission
	}
	orgWide := database.OrgWideScope(f.OrgA, f.User)
	client1 := database.OrgWideScope(f.OrgA, f.User)
	client1.Clients = database.ScopeIDs(f.Client1)
	client2 := database.OrgWideScope(f.OrgA, f.User)
	client2.Clients = database.ScopeIDs(f.Client2)
	creator := who{user: f.User, scope: &client1, perms: allPerms}
	serve := func(h http.Handler, method, target, body string, as who) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(method, target, strings.NewReader(body))
		ctx := tenant.WithTenant(database.ContextWithTenantScope(req.Context(), as.scope),
			tenant.TenantInfo{OrganizationID: f.OrgA, UserID: as.user})
		ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: as.user, OrganizationID: f.OrgA,
			Permissions: as.perms, Type: identity.PrincipalTypeUser})
		ctx = permission.WithPolicy(ctx, permission.UnrestrictedPolicy(as.perms))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req.WithContext(ctx))
		return rec
	}
	start := func(h http.Handler, body string, as who) string {
		t.Helper()
		rec := serve(h, http.MethodPost, "/api/v1/labels/batch", body, as)
		var out map[string]any
		if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("POST %s: %d %s, want 202", body, rec.Code, rec.Body)
		}
		id, _ := out["job_id"].(string)
		if id == "" || out["kind"] != labels.BatchJobKind || rec.Header().Get("Location") != "/api/v1/jobs/"+id {
			t.Fatalf("POST %s: Location %q, body %v", body, rec.Header().Get("Location"), out)
		}
		return id
	}
	wait := func(id string) *jobs.Job {
		t.Helper()
		deadline := time.Now().Add(time.Minute)
		for {
			job, err := svc.Get(f.OrgCtx(f.OrgA), f.OrgA, id)
			if err == nil && job.Status.Terminal() {
				return job
			}
			if time.Now().After(deadline) {
				t.Fatalf("job %s did not end: %+v %v", id, job, err)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// texts returns the text strings of the PDF of the job, sorted.
	texts := func(id string) string {
		t.Helper()
		raw := store.pdf(f.OrgA, id)
		if !bytes.HasPrefix(raw, []byte("%PDF-")) {
			t.Fatalf("job %s stored no PDF", id)
		}
		_, contents := pdfPages(t, raw)
		return strings.Join(contents, "\n")
	}
	// succeeded checks the job and its result: labels and pages, an
	// expiry, nothing else (no key, no URL).
	succeeded := func(name, id string, wantLabels, wantPages int) {
		t.Helper()
		job := wait(id)
		if job.Status != jobs.StatusSucceeded || job.Kind != labels.BatchJobKind {
			t.Fatalf("%s: job %+v, want a succeeded labels.batch", name, job)
		}
		var result map[string]any
		if err := json.Unmarshal(job.Result, &result); err != nil {
			t.Fatalf("%s: result %s: %v", name, job.Result, err)
		}
		keys := make([]string, 0, len(result))
		for k := range result {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if strings.Join(keys, ",") != "expires_at,labels,pages" {
			t.Errorf("%s: result members %v, want exactly expires_at, labels, pages", name, keys)
		}
		var typed labels.BatchResult
		if err := json.Unmarshal(job.Result, &typed); err != nil || typed.Labels != wantLabels || typed.Pages != wantPages ||
			!typed.ExpiresAt.After(time.Now()) {
			t.Errorf("%s: result %+v %v, want %d labels on %d pages", name, typed, err, wantLabels, wantPages)
		}
	}
	has := func(name, text, s string, want bool) {
		t.Helper()
		if got := strings.Contains(text, "("+s); got != want {
			t.Errorf("%s: PDF prints %q: %v, want %v", name, s, got, want)
		}
	}

	// ids: client 1 sees alpha and delta, not gamma of client 2; the
	// in-process run uses the caller's (unrestricted) field policy.
	byIDs := start(inProcess, `{"kind":"asset","template":"asset_50x25","ids":["`+alpha+`","`+gamma+`","`+delta+`"]}`, creator)
	succeeded("ids", byIDs, 2, 2)
	text := texts(byIDs)
	has("ids", text, "bt-alpha", true)
	has("ids", text, "bt-delta", true)
	has("ids", text, "BT-INV-1", true)
	has("ids", text, "BTMAKER BT1", true)
	has("ids", text, "bt-gamma", false)
	has("ids", text, "BT-INV-3", false)

	// Inventory area: the assets below the warehouse (alpha on it, beta in
	// its bin), its four nodes, the consumable with stock in its bin.
	area := func(kind string) string {
		return `{"kind":"` + kind + `","location_id":"` + whA + `"}`
	}
	areaAssets := start(inProcess, area("asset"), creator)
	succeeded("area assets", areaAssets, 2, 1)
	text = texts(areaAssets)
	has("area assets", text, "bt-alpha", true)
	has("area assets", text, "bt-beta", true)
	has("area assets", text, "bt-delta", false)
	areaNodes := start(inProcess, area("location"), creator)
	succeeded("area locations", areaNodes, 4, 1)
	has("area locations", texts(areaNodes), "bt-site-a-bin", true)
	areaStock := start(inProcess, area("consumable"), creator)
	succeeded("area consumables", areaStock, 1, 1)
	text = texts(areaStock)
	has("area consumables", text, "bt-toner", true)
	has("area consumables", text, "bt-paper", false)
	// The area of client 1 for client 2: 404, no job.
	if rec := serve(inProcess, http.MethodPost, "/api/v1/labels/batch", area("asset"), who{user: f.User, scope: &client2, perms: allPerms}); rec.Code != http.StatusNotFound {
		t.Errorf("area outside the scope: %d %s, want 404", rec.Code, rec.Body)
	}
	// Org-wide the ids include gamma.
	succeeded("ids org-wide", start(inProcess, `{"kind":"asset","ids":["`+alpha+`","`+gamma+`"]}`, who{user: f.User, scope: &orgWide, perms: allPerms}), 2, 1)

	// Without the read permission of the kind: 403.
	for kind, missing := range map[string]identity.Permission{"asset": identity.PermAssetRead, "consumable": identity.PermStockRead, "location": identity.PermLocationRead} {
		var perms []identity.Permission
		for _, p := range allPerms {
			if p != missing {
				perms = append(perms, p)
			}
		}
		if rec := serve(inProcess, http.MethodPost, "/api/v1/labels/batch", area(kind), who{user: f.User, scope: &client1, perms: perms}); rec.Code != http.StatusForbidden {
			t.Errorf("%s without %s: %d %s, want 403", kind, missing, rec.Code, rec.Body)
		}
	}
	// Invalid requests: 422.
	for _, body := range []string{
		`{"kind":"ticket","ids":["` + alpha + `"]}`,
		`{"kind":"asset","template":"a5","ids":["` + alpha + `"]}`,
		`{"kind":"asset"}`,
		`{"kind":"asset","ids":["` + alpha + `"],"location_id":"` + whA + `"}`,
		`{"kind":"asset","ids":["not-a-uuid"]}`,
	} {
		if rec := serve(inProcess, http.MethodPost, "/api/v1/labels/batch", body, creator); rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: %d %s, want 422", body, rec.Code, rec.Body)
		}
	}

	// Download: the creator gets a URL below /objects/exports/ that names
	// the object of the job; another user 404.
	download := func(h http.Handler, id string, as who) *httptest.ResponseRecorder {
		return serve(h, http.MethodGet, "/api/v1/labels/batch/"+id+"/download", "", as)
	}
	rec := download(inProcess, byIDs, creator)
	var got labels.BatchDownload
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil {
		t.Fatalf("download by the creator: %d %s, want 200", rec.Code, rec.Body)
	}
	signed, err := url.Parse(got.DownloadURL)
	if err != nil || signed.Host != "cmdb.example" || !strings.HasPrefix(signed.Path, "/objects/exports/") ||
		signed.Path != "/objects/"+export.ExportBucket+"/"+labels.BatchObjectKey(f.OrgA, byIDs) ||
		signed.Query().Get("X-Amz-Signature") == "" || got.JobID != byIDs || got.Labels != 2 || got.Pages != 2 {
		t.Errorf("download by the creator: %+v %v", got, err)
	}
	other := who{user: f.ID(), scope: &client1, perms: allPerms}
	if rec = download(inProcess, byIDs, other); rec.Code != http.StatusNotFound {
		t.Errorf("download by another user: %d %s, want 404", rec.Code, rec.Body)
	}
	if rec = download(inProcess, f.ID(), creator); rec.Code != http.StatusNotFound {
		t.Errorf("download of an unknown job: %d %s, want 404", rec.Code, rec.Body)
	}
	// A batch without a visible object fails; its download is 409.
	empty := start(inProcess, `{"kind":"asset","ids":["`+gamma+`"]}`, creator)
	if job := wait(empty); job.Status != jobs.StatusFailed {
		t.Errorf("batch without visible object: %+v, want failed", job)
	}
	if rec = download(inProcess, empty, creator); rec.Code != http.StatusConflict {
		t.Errorf("download of a failed batch: %d %s, want 409", rec.Code, rec.Body)
	}

	// Without blob storage or job service: 503 on both routes.
	for name, h := range map[string]http.Handler{
		"without blob storage": route(labels.NewBatchHandler(repo, nil).WithJobs(svc)),
		"without job service":  route(labels.NewBatchHandler(repo, store)),
	} {
		if rec = serve(h, http.MethodPost, "/api/v1/labels/batch", area("asset"), creator); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("POST %s: %d %s, want 503", name, rec.Code, rec.Body)
		}
		if rec = download(h, byIDs, creator); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("download %s: %d %s, want 503", name, rec.Code, rec.Body)
		}
	}

	// On the job queue: the job runs with the creator's scope (client 1:
	// alpha only), without the creator's field policy fail-closed (name
	// and URN, no inventory number, no manufacturer).
	queued := labels.NewBatchHandler(repo, store).WithJobs(svc)
	queue := jobs.NewQueue(svc, nil)
	queue.Handle(jobs.Kind{Name: labels.BatchJobKind, MaxAttempts: 1, Timeout: 10 * time.Minute, Cancellable: true}, queued.RunJob)
	queuedID := start(route(queued.WithQueue(queue)), `{"kind":"asset","ids":["`+alpha+`","`+gamma+`"]}`, creator)
	if job, _ := svc.Get(f.OrgCtx(f.OrgA), f.OrgA, queuedID); job == nil || job.Status != jobs.StatusQueued {
		t.Fatalf("queued batch after the request: %+v, want queued", job)
	}
	data, _ := json.Marshal(jobs.Message{OrganizationID: f.OrgA, JobID: queuedID, Kind: labels.BatchJobKind})
	if out := queue.Process(bg, &batchDelivery{data: data}); !out.Ran {
		t.Fatalf("queued batch not run: %+v", out)
	}
	succeeded("queued", queuedID, 1, 1)
	text = texts(queuedID)
	has("queued", text, "bt-alpha", true)
	has("queued", text, "bt-gamma", false)
	has("queued", text, "BT-INV-1", false)
	has("queued", text, "BTMAKER", false)
	has("queued", text, "urn:reticora:asset:", true)
}
