package rls_test

// WP-730 (RBA-07, TST-01, E-43): the channel matrix of the field
// permissions. One CI carries a readable field (core.hostname), a field
// with the grant none (core.serial_number) and a sensitive attribute
// without grant (attributes.salary); a user and a service account hold the
// same role. Every channel that projects CI fields is probed for every
// field: REST (/api/v1/cis), GraphQL (ci, cis, searchEverything), the
// search (/api/v1/search), the structured query (/api/v1/search/query),
// the export (/api/v1/export/cis, /api/v1/exports with the worker) and the
// webhook (stored deliveries in webhook_delivery). Each probe checks the
// value and its existence: no hit, no snippet, the field named in the
// manifest, no filter on it, no change naming it.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestFieldRightsChannelMatrix$' ./internal/tenant/rls/

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// frField is one field of the matrix: its DTO path, GraphQL name, the
// value the reader must or must not see and whether it may.
type frField struct {
	name     string // readable, none, sensitive
	path     string // DTO path (hostname, serial_number, attributes.salary)
	gql      string // GraphQL field of CI ("" for an attribute)
	value    string
	readable bool
	// unbound is whether an unbound webhook subscription reads it (default
	// policy without role: role grants do not apply, sensitive fields are
	// closed).
	unbound bool
}

// Values of the matrix CI: the final ones, the intermediate serial number
// of the first change and the values before the changes.
const (
	frmHost      = "fr730readhost"
	frmSerial    = "fr730nonesn"
	frmSerialMid = "fr730midsn"
	frmSalary    = "fr730sensal"
)

var frmFields = []frField{
	{name: "readable", path: "hostname", gql: "hostname", value: frmHost, readable: true, unbound: true},
	{name: "none", path: "serial_number", gql: "serialNumber", value: frmSerial, readable: false, unbound: true},
	{name: "sensitive", path: "attributes.salary", value: frmSalary, readable: false, unbound: false},
}

// frmEnv is the router of the PostgreSQL profile as newChannelEnvWithBlobs
// builds it, with the webhook dispatcher of cmd/server (Access with the
// field policies of the service accounts, Objects, CIs), the outbox
// publisher handing the CI events to it and the export worker with the
// policy resolver.
type frmEnv struct {
	t          *testing.T
	f          *scopetest.Fixture
	router     http.Handler
	repos      server.Repositories
	blobs      blob.Store
	dispatcher *webhook.Dispatcher
	publisher  *events.Publisher
}

func newFRMEnv(t *testing.T, f *scopetest.Fixture) *frmEnv {
	t.Helper()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	e := &frmEnv{t: t, f: f, blobs: blob.NewFileStore(t.TempDir())}
	e.repos = server.PostgresRepositories(f.App, audit.NewPGRecorder())
	e.repos.CI = ci.NewIndexingRepository(e.repos.CI, search.NewCIIndexer(e.repos.Search))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(srv.Close)
	e.dispatcher = webhook.NewDispatcher(e.repos.Webhook, srv.Client(), webhook.DispatcherOptions{
		Deliveries: e.repos.WebhookDeliveries,
		Access:     user.ServiceAccountAccess{Repo: e.repos.ServiceAccounts, Fields: server.NewFieldResolver(&e.repos, nil)},
		Objects:    webhook.NewPGObjectResolver(f.App),
		CIs:        frmCIs{repos: &e.repos},
		// Retries are not part of the matrix.
		PollInterval: -1,
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.dispatcher.Shutdown(ctx)
	})
	e.publisher = events.NewPublisher(f.App, nil, e.dispatcher)
	entitlements := entitlement.NewService(e.repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(e.repos, server.Options{
		Version:      "fr-matrix",
		Blobs:        e.blobs,
		Entitlements: entitlements,
		Dispatcher:   e.dispatcher,
		CIService: ci.NewServiceWithLimits(e.repos.CI, entitlements).WithFieldResolver(e.repos.CIType).
			WithReferences(ci.NewPGReferenceLookup(f.App)),
		Credentials: credential.NewService(e.repos.Credential, encryptor),
		OIDC:        identity.NewOIDCProvider(identity.OIDCConfig{}),
		Notifications: &notify.Center{
			Channels:    notify.NewPGRepository(f.App, notify.NewSealer(encryptor)),
			Inbox:       notify.NewPGInbox(f.App),
			Preferences: notify.NewPGPreferences(f.App),
			Deliveries:  notify.NewPGDeliveryStore(f.App),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	e.router = middleware.TenantMiddleware(mux)
	return e
}

// frmCIs is the CI lookup of the dispatcher as in cmd/server.
type frmCIs struct{ repos *server.Repositories }

func (c frmCIs) CIFieldScope(ctx context.Context, orgID, ciID string) (*webhook.CIFieldScope, error) {
	facts, err := server.HistoryCIs(c.repos).CIFacts(ctx, orgID, ciID)
	if err != nil || facts == nil {
		return nil, err
	}
	return &webhook.CIFieldScope{TypeID: facts.TypeID, ClientID: facts.ClientID, SiteID: facts.SiteID, InstanceKeys: facts.InstanceKeys}, nil
}

// frmResponse is the answer of one request.
type frmResponse struct {
	code   int
	body   string
	header http.Header
}

// call sends one request as the user with every permission: the field
// policy follows the user's role assignments.
func (e *frmEnv) call(userID, method, path string, body any) frmResponse {
	e.t.Helper()
	var reader io.Reader = http.NoBody
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = strings.NewReader(string(raw))
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	scope := identity.Scope{Clients: all(), Sites: all(), Teams: all()}
	principal := identity.Principal{Subject: userID, OrganizationID: e.f.OrgA, Scope: &scope, Type: identity.PrincipalTypeUser,
		Permissions: identity.AllPermissions(), Credential: "fr-matrix"}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req.WithContext(ctx))
	return frmResponse{code: w.Code, body: w.Body.String(), header: w.Header()}
}

// frmProbe is the outcome of one channel for one field.
type frmProbe struct {
	// visible: the value (or a hit, a snippet) of the field reached the
	// reader.
	visible bool
	// exists: the field showed up otherwise (member, filter, column,
	// manifest without it, a change naming it).
	exists bool
	detail string
}

func TestFieldRightsChannelMatrix(t *testing.T) {
	f := scopetest.Seed(t, "f7")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	e := newFRMEnv(t, f)

	// The role of the reader and of the service account: CIs readable,
	// serial_number with grant none; salary is sensitive without grant.
	readerPerms := []string{"ci:read", "search:read", "export:run", "job:read"}
	roleID := f.ID()
	raw, _ := json.Marshal(readerPerms)
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp730-reader', $3::jsonb)`, roleID, f.OrgA, string(raw))
	exec(`INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', 'core.serial_number', 'none')`,
		f.OrgA, roleID)
	exec(`INSERT INTO field_sensitivity (organization_id, object_kind, field_key) VALUES ($1, 'ci', 'attributes.salary')`, f.OrgA)
	reader := f.AppUser(t, f.OrgA, "wp730-reader")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, reader, roleID)
	admin := f.AppUser(t, f.OrgA, "wp730-admin")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, admin)
	account := f.ID()
	exec(`INSERT INTO service_account (id, organization_id, name) VALUES ($1, $2, 'wp730-hook')`, account, f.OrgA)
	exec(`INSERT INTO service_account_role (organization_id, service_account_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, account, roleID)

	// The subscriptions: one bound to the service account, one unbound;
	// both of the whole organization.
	subs := map[string]string{}
	for name, sa := range map[string]string{"bound": account, "unbound": ""} {
		sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "wp730-" + name, URL: "https://hooks.example.org/" + name,
			Secret: "wp730-secret", Events: []string{events.CIChanged}, IsActive: true, ServiceAccountID: sa}
		if err := e.repos.Webhook.Create(f.OrgCtx(f.OrgA), sub); err != nil {
			t.Fatalf("subscription %s: %v", name, err)
		}
		subs[name] = sub.ID
	}

	// The CI, then two changes by the administrator: hostname and serial
	// number, then the serial number alone.
	ciID := f.CI(t, f.OrgA, f.Client1, "wp730-ci")
	exec(`UPDATE ci SET hostname = 'fr730oldhost', serial_number = 'fr730oldsn',
		attributes = jsonb_build_object('salary', $2::text, 'rack', 'r730') WHERE id = $1`, ciID, frmSalary)
	for _, patch := range []map[string]any{
		{"hostname": frmHost, "serial_number": frmSerialMid},
		{"serial_number": frmSerial},
	} {
		if r := e.call(admin, http.MethodPatch, "/api/v1/cis/"+ciID, patch); r.code != http.StatusOK {
			t.Fatalf("setup: PATCH as admin: %d %s", r.code, trim(r.body))
		}
	}
	if _, err := e.publisher.PublishPending(bg); err != nil {
		t.Fatalf("setup: publish the CI events: %v", err)
	}
	if _, err := search.NewPGRepository(f.App).ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("setup: reindex: %v", err)
	}

	// The stored deliveries per subscription (the payload the subscriber
	// gets and every retry sends).
	deliveries := func(name string) []map[string]any {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT payload FROM webhook_delivery WHERE subscription_id = $1 ORDER BY event_sequence`, subs[name])
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []map[string]any
		for rows.Next() {
			var payload []byte
			if err := rows.Scan(&payload); err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(payload, &m); err != nil {
				t.Fatal(err)
			}
			out = append(out, m)
		}
		return out
	}
	bound, unbound := deliveries("bound"), deliveries("unbound")
	if len(unbound) != 2 {
		t.Fatalf("setup: unbound subscription has %d deliveries, want both changes", len(unbound))
	}
	// The second change names only the serial number: withheld from the
	// account, which cannot read it.
	if len(bound) != 1 {
		t.Errorf("webhook: bound subscription has %d deliveries, want 1 (the change of serial_number alone is withheld)", len(bound))
	}

	// The export job of the reader, rendered by the worker of cmd/server.
	created := e.call(reader, http.MethodPost, "/api/v1/exports", map[string]string{"format": "json"})
	var job struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(created.body), &job); err != nil || created.code != http.StatusAccepted || job.ID == "" {
		t.Fatalf("setup: POST /api/v1/exports as reader: %d %s", created.code, trim(created.body))
	}
	worker := export.NewJobWorker(export.NewPGJobRepository(f.App), ci.NewPGRepository(f.App), e.blobs)
	worker.Tracker = jobs.NewService(jobs.NewPGRepository(f.App))
	worker.Policies = server.NewFieldResolver(&e.repos, nil)
	for worker.ProcessOne(bg) {
	}
	jobResp := e.call(reader, http.MethodGet, "/api/v1/exports/"+job.ID, nil)
	var jobBody struct {
		Status    string `json:"status"`
		ObjectKey string `json:"object_key"`
		Manifest  struct {
			Fields  []string `json:"fields"`
			Omitted []struct {
				Field  string `json:"field"`
				Reason string `json:"reason"`
			} `json:"omitted"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal([]byte(jobResp.body), &jobBody); err != nil || jobResp.code != http.StatusOK || jobBody.ObjectKey == "" {
		t.Fatalf("setup: GET /api/v1/exports/%s: %d %s", job.ID, jobResp.code, trim(jobResp.body))
	}
	rc, err := e.blobs.Get(bg, export.ExportBucket, jobBody.ObjectKey)
	if err != nil {
		t.Fatalf("setup: export file: %v", err)
	}
	jobFile, _ := io.ReadAll(rc)
	_ = rc.Close()
	omittedFromJob := func(path string) bool {
		for _, o := range jobBody.Manifest.Omitted {
			if o.Field == path && o.Reason == export.ReasonFieldPermission {
				return true
			}
		}
		return false
	}

	// contains reports whether s holds the value of the field.
	contains := func(s string, fl *frField) bool { return strings.Contains(s, fl.value) }
	// member reports whether the decoded CI holds the member of the path.
	member := func(obj map[string]any, path string) bool {
		if name, ok := strings.CutPrefix(path, "attributes."); ok {
			attrs, _ := obj["attributes"].(map[string]any)
			_, has := attrs[name]
			return has
		}
		_, has := obj[path]
		return has
	}
	ok := func(r frmResponse, what string) {
		t.Helper()
		if r.code != http.StatusOK {
			t.Fatalf("%s: %d %s", what, r.code, trim(r.body))
		}
	}
	gql := func(query string) frmResponse {
		t.Helper()
		r := e.call(reader, http.MethodPost, "/bff/graphql", map[string]any{"query": query})
		ok(r, "graphql "+query)
		if strings.Contains(r.body, `"errors"`) {
			t.Fatalf("graphql %s: %s", query, trim(r.body))
		}
		return r
	}

	channels := []struct {
		name  string
		probe func(fl *frField) frmProbe
	}{
		{"REST GET /api/v1/cis/{id}", func(fl *frField) frmProbe {
			r := e.call(reader, http.MethodGet, "/api/v1/cis/"+ciID, nil)
			ok(r, "GET /cis/{id}")
			var obj map[string]any
			_ = json.Unmarshal([]byte(r.body), &obj)
			return frmProbe{visible: contains(r.body, fl), exists: member(obj, fl.path), detail: trim(r.body)}
		}},
		{"REST GET /api/v1/cis (list and filter)", func(fl *frField) frmProbe {
			list := e.call(reader, http.MethodGet, "/api/v1/cis?limit=200", nil)
			ok(list, "GET /cis")
			filter := e.call(reader, http.MethodGet, "/api/v1/cis?"+url.Values{"filter[" + fl.path + "][exact]": {fl.value}}.Encode(), nil)
			// A filter on a field the reader cannot read is refused (403
			// with the field, no total): no value oracle.
			found := filter.code == http.StatusOK && strings.Contains(filter.body, ciID)
			refused := filter.code == http.StatusForbidden && strings.Contains(filter.body, fl.path) && !strings.Contains(filter.body, `"total"`)
			if !found && !refused {
				t.Errorf("REST filter on %s: %d %s, want 200 with the CI or 403", fl.path, filter.code, trim(filter.body))
			}
			return frmProbe{visible: contains(list.body, fl) || found, exists: found, detail: trim(filter.body)}
		}},
		{"GraphQL ci", func(fl *frField) frmProbe {
			sel := fl.gql
			if sel == "" {
				sel = "attributes"
			}
			r := gql(fmt.Sprintf(`{ ci(id: %q) { id %s } }`, ciID, sel))
			var body struct {
				Data struct {
					CI map[string]any `json:"ci"`
				} `json:"data"`
			}
			_ = json.Unmarshal([]byte(r.body), &body)
			exists := body.Data.CI[fl.gql] != nil
			if fl.gql == "" {
				exists = member(body.Data.CI, fl.path)
			}
			return frmProbe{visible: contains(r.body, fl), exists: exists, detail: trim(r.body)}
		}},
		{"GraphQL cis", func(fl *frField) frmProbe {
			sel := fl.gql
			if sel == "" {
				sel = "attributes"
			}
			r := gql(fmt.Sprintf(`{ cis(first: 100) { nodes { id %s } } }`, sel))
			return frmProbe{visible: contains(r.body, fl), exists: contains(r.body, fl), detail: trim(r.body)}
		}},
		{"GraphQL searchEverything", func(fl *frField) frmProbe {
			r := gql(fmt.Sprintf(`{ searchEverything(q: %q, first: 50) { total hits { entityId title } } }`, fl.value))
			return frmProbe{visible: strings.Contains(r.body, ciID), exists: strings.Contains(r.body, ciID), detail: trim(r.body)}
		}},
		{"search /api/v1/search", func(fl *frField) frmProbe {
			r := e.call(reader, http.MethodGet, "/api/v1/search?"+url.Values{"q": {fl.value}, "highlight": {"true"}}.Encode(), nil)
			ok(r, "GET /search")
			// A hit or a snippet with the value.
			return frmProbe{visible: strings.Contains(r.body, ciID) || contains(r.body, fl), exists: strings.Contains(r.body, ciID), detail: trim(r.body)}
		}},
		{"structured query /api/v1/search/query", func(fl *frField) frmProbe {
			plain := e.call(reader, http.MethodPost, "/api/v1/search/query?limit=200", map[string]any{"entity_kind": "ci"})
			ok(plain, "POST /search/query")
			if !strings.Contains(plain.body, ciID) {
				t.Errorf("structured query misses the CI: %s", trim(plain.body))
			}
			column := e.call(reader, http.MethodPost, "/api/v1/search/query?limit=200", map[string]any{"entity_kind": "ci", "columns": []string{fl.path}})
			found := column.code == http.StatusOK && contains(column.body, fl)
			refused := column.code == http.StatusForbidden && strings.Contains(column.body, fl.path) && !strings.Contains(column.body, `"total"`)
			if !found && !refused {
				t.Errorf("structured query column %s: %d %s, want 200 with the value or 403", fl.path, column.code, trim(column.body))
			}
			return frmProbe{visible: contains(plain.body, fl) || found, exists: found, detail: trim(column.body)}
		}},
		{"export /api/v1/export/cis", func(fl *frField) frmProbe {
			r := e.call(reader, http.MethodGet, "/api/v1/export/cis?format=json", nil)
			ok(r, "GET /export/cis")
			omitted := slices.Contains(strings.Split(r.header.Get("X-Export-Omitted-Fields"), ","), fl.path)
			return frmProbe{visible: contains(r.body, fl), exists: !omitted, detail: r.header.Get("X-Export-Omitted-Fields")}
		}},
		{"export job /api/v1/exports", func(fl *frField) frmProbe {
			if jobBody.Status != "completed" {
				t.Errorf("export job %s, want completed", jobBody.Status)
			}
			return frmProbe{visible: contains(string(jobFile), fl), exists: !omittedFromJob(fl.path), detail: trim(jobResp.body)}
		}},
		{"webhook (bound subscription)", func(fl *frField) frmProbe {
			raw, _ := json.Marshal(bound)
			exists := false
			for _, d := range bound {
				changed, _ := d["changed_fields"].([]any)
				exists = exists || member(d, fl.path) || slices.Contains(changed, any(fl.path))
			}
			visible := contains(string(raw), fl) || (fl.path == "serial_number" && strings.Contains(string(raw), frmSerialMid))
			return frmProbe{visible: visible, exists: exists, detail: trim(string(raw))}
		}},
	}

	for _, ch := range channels {
		for i := range frmFields {
			fl := &frmFields[i]
			t.Run(ch.name+"/"+fl.name, func(t *testing.T) {
				got := ch.probe(fl)
				if got.visible != fl.readable {
					t.Errorf("value of %s visible = %v, want %v: %s", fl.path, got.visible, fl.readable, got.detail)
				}
				if got.exists != fl.readable {
					t.Errorf("existence of %s = %v, want %v: %s", fl.path, got.exists, fl.readable, got.detail)
				}
			})
		}
	}

	// The unbound subscription: the default policy without role. The
	// sensitive attribute is closed, the grant none of the role does not
	// apply.
	t.Run("webhook (unbound subscription)", func(t *testing.T) {
		raw, _ := json.Marshal(unbound)
		for i := range frmFields {
			fl := &frmFields[i]
			if got := contains(string(raw), fl); got != fl.unbound {
				t.Errorf("unbound delivery: value of %s visible = %v, want %v: %s", fl.path, got, fl.unbound, trim(string(raw)))
			}
			if got := member(unbound[len(unbound)-1], fl.path); got != fl.unbound {
				t.Errorf("unbound delivery: member %s = %v, want %v", fl.path, got, fl.unbound)
			}
		}
		if attrs, _ := unbound[0]["attributes"].(map[string]any); attrs["rack"] != "r730" {
			t.Errorf("unbound delivery lost the attribute without sensitivity: %v", attrs)
		}
	})
	// Control: the readable attribute and structural fields stay in the
	// bound delivery.
	if len(bound) > 0 {
		attrs, _ := bound[0]["attributes"].(map[string]any)
		if attrs["rack"] != "r730" || bound[0]["id"] != ciID || bound[0]["name"] != "wp730-ci" {
			t.Errorf("bound delivery lost readable or structural fields: %v", bound[0])
		}
		if changed, _ := bound[0]["changed_fields"].([]any); !slices.Equal(changed, []any{"hostname"}) {
			t.Errorf("bound delivery changed_fields %v, want [hostname]", changed)
		}
	}
}
