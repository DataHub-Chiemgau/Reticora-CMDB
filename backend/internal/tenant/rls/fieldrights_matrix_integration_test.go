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

// frmKindField is one field of an object kind of the matrix: its field key
// (field_grant, field_sensitivity) and, in frField, the member path of the
// DTO, the GraphQL member ("" none) and the value the reader must or must
// not see.
type frmKindField struct {
	frField
	key string
}

// frmKind is an object kind of TestFieldRightsChannelMatrixObjectKinds:
// its three fields (readable, grant none, sensitive without grant) and the
// object the administrator wrote.
type frmKind struct {
	kind   string
	fields [3]frmKindField
	id     string
}

// frmKindFields are the fields of the object kinds besides ci (Konvention
// 11): readable, with the grant none and sensitive without grant.
func frmKindFields() []*frmKind {
	field := func(name, key, path, gql, value string) frmKindField {
		return frmKindField{frField: frField{name: name, path: path, gql: gql, value: value, readable: name == "readable"}, key: key}
	}
	return []*frmKind{
		{kind: "asset", fields: [3]frmKindField{
			field("readable", "attributes.rack", "custom_fields.rack", "", "fr326arack"),
			field("none", "core.serial_number", "serial_number", "serialNumber", "fr326asn"),
			field("sensitive", "attributes.salary", "custom_fields.salary", "", "fr326asal"),
		}},
		{kind: "contract", fields: [3]frmKindField{
			field("readable", "core.start_date", "start_date", "", "2019-03-26"),
			field("none", "core.notes", "notes", "", "fr326cnotes"),
			field("sensitive", "core.value", "value", "", "32617.55"),
		}},
		{kind: "ticket", fields: [3]frmKindField{
			field("readable", "core.description", "description", "", "fr326tdesc"),
			field("none", "core.tags", "tags", "", "fr326ttag"),
			field("sensitive", "core.due_date", "due_date", "", "2031-03-26"),
		}},
		{kind: "document", fields: [3]frmKindField{
			field("readable", "core.description", "description", "description", "fr326ddesc"),
			field("none", "core.tags", "tags", "tags", "fr326dtag"),
			field("sensitive", "core.file_name", "file_name", "fileName", "fr326dsens"),
		}},
		{kind: "relationship", fields: [3]frmKindField{
			field("readable", "core.notes", "notes", "", "fr326rnotes"),
			field("none", "attributes.port", "attributes.port", "", "fr326rport"),
			field("sensitive", "attributes.vlan", "attributes.vlan", "", "fr326rvlan"),
		}},
	}
}

// frmMember reports whether the decoded object holds the member of the path
// (one level of nesting: custom_fields.x, attributes.x).
func frmMember(obj map[string]any, path string) bool {
	if parent, name, nested := strings.Cut(path, "."); nested {
		inner, _ := obj[parent].(map[string]any)
		_, has := inner[name]
		return has
	}
	_, has := obj[path]
	return has
}

// frmItem decodes the object with id from a detail or a list answer (items,
// data or relationships).
func frmItem(body, id string) map[string]any {
	var obj map[string]any
	_ = json.Unmarshal([]byte(body), &obj)
	if obj["id"] == id {
		return obj
	}
	for _, list := range []string{"items", "data", "relationships"} {
		arr, _ := obj[list].([]any)
		for _, el := range arr {
			if m, _ := el.(map[string]any); m["id"] == id {
				return m
			}
		}
	}
	return nil
}

// TestFieldRightsChannelMatrixObjectKinds is the channel matrix of WP-730
// for the object kinds besides ci (WP-326, TST-01 [P2], RBA-07, Konvention
// 11): asset, contract, ticket, document and relationship, each with a
// readable field, a field with the grant none and a sensitive field
// without grant, probed in every channel that delivers the kind (the role
// may write documents, so the readable document field is editable):
//
//   - asset: REST detail and list, GraphQL asset, search, structured query
//     (column), webhook (asset.created and asset.changed, bound
//     subscription);
//   - contract: REST detail and list;
//   - ticket: REST detail and list, search;
//   - document: REST detail, list and the attachments of the ticket,
//     GraphQL Asset.documents, search; writes through POST and PATCH
//     (the readable field passes, the others are 403 with the field);
//   - relationship: REST list of the CI, webhook (relationship.created,
//     bound subscription).
//
// The export of POST /api/v1/exports is CI-only (assets export through
// POST /api/v1/assets/bulk, DATEV in export/datev_test.go), GraphQL has no
// member of these fields for contract, ticket or relationship, and the
// structured query knows ci and asset only. Documents were bound to the
// field permissions in REST and GraphQL with finding F-1 of
// docs/acceptance/tst-01-phase2.md (WP-267).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestFieldRightsChannelMatrixObjectKinds$' ./internal/tenant/rls/
func TestFieldRightsChannelMatrixObjectKinds(t *testing.T) {
	f := scopetest.Seed(t, "f7")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	e := newFRMEnv(t, f)
	kinds := frmKindFields()
	byKind := map[string]*frmKind{}
	for _, k := range kinds {
		byKind[k.kind] = k
	}

	// The role of the reader and of the service account: every kind
	// readable, per kind one field with the grant none and one sensitive
	// field without grant (marked once the administrator wrote the
	// objects: without a grant of its key nobody writes it, E-43).
	readerPerms := []string{"ci:read", "asset:read", "contract:read", "ticket:read", "document:read", "document:write",
		"relationship:read", "search:read"}
	roleID := f.ID()
	raw, _ := json.Marshal(readerPerms)
	exec(`INSERT INTO role (id, organization_id, name, permissions) VALUES ($1, $2, 'wp326-reader', $3::jsonb)`, roleID, f.OrgA, string(raw))
	for _, k := range kinds {
		exec(`INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, $3, $4, 'none')`,
			f.OrgA, roleID, k.kind, k.fields[1].key)
	}
	reader := f.AppUser(t, f.OrgA, "wp326-reader")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, reader, roleID)
	admin := f.AppUser(t, f.OrgA, "wp326-admin")
	exec(`INSERT INTO role_assignment (organization_id, user_id, role_id)
		SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA, admin)
	account := f.ID()
	exec(`INSERT INTO service_account (id, organization_id, name) VALUES ($1, $2, 'wp326-hook')`, account, f.OrgA)
	exec(`INSERT INTO service_account_role (organization_id, service_account_id, role_id) VALUES ($1, $2, $3)`, f.OrgA, account, roleID)
	sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "wp326-bound", URL: "https://hooks.example.org/wp326",
		Secret: "wp326-secret", IsActive: true, ServiceAccountID: account,
		Events: []string{events.AssetCreated, events.AssetChanged, events.RelationshipCreated}}
	if err := e.repos.Webhook.Create(f.OrgCtx(f.OrgA), sub); err != nil {
		t.Fatalf("subscription: %v", err)
	}

	// The objects, written by the administrator through the API.
	create := func(path string, body map[string]any) string {
		t.Helper()
		r := e.call(admin, http.MethodPost, path, body)
		var obj struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal([]byte(r.body), &obj); err != nil || r.code != http.StatusCreated || obj.ID == "" {
			t.Fatalf("setup: POST %s as admin: %d %s", path, r.code, trim(r.body))
		}
		return obj.ID
	}
	asset := byKind["asset"]
	asset.id = create("/api/v1/assets", map[string]any{"inventory_no": "wp326-inv", "name": "wp326-asset", "client_id": f.Client1,
		"custom_fields": map[string]any{"rack": asset.fields[0].value, "salary": asset.fields[2].value}})
	if r := e.call(admin, http.MethodPatch, "/api/v1/assets/"+asset.id, map[string]any{"serial_number": asset.fields[1].value, "name": "wp326-asset-2"}); r.code != http.StatusOK {
		t.Fatalf("setup: PATCH asset as admin: %d %s", r.code, trim(r.body))
	}
	contract := byKind["contract"]
	contract.id = create("/api/v1/contracts", map[string]any{"number": "wp326-con", "client_id": f.Client1, "currency": "EUR",
		"start_date": contract.fields[0].value, "notes": contract.fields[1].value, "value": json.Number(contract.fields[2].value)})
	// Ticket and document tags name catalog tags.
	exec(`INSERT INTO tag (organization_id, key, display_name) VALUES ($1, 'fr326ttag', '{"de":"wp326 t"}'), ($1, 'fr326dtag', '{"de":"wp326 d"}')`, f.OrgA)
	ticket := byKind["ticket"]
	ticket.id = create("/api/v1/tickets", map[string]any{"title": "wp326-ticket", "description": ticket.fields[0].value,
		"tags": []string{ticket.fields[1].value}, "due_date": ticket.fields[2].value + "T12:00:00Z"})
	document := byKind["document"]
	document.id = create("/api/v1/documents", map[string]any{"title": "wp326-doc", "description": document.fields[0].value,
		"tags": []string{document.fields[1].value}, "file_name": document.fields[2].value + ".txt",
		"links": []map[string]any{{"entity_type": "asset", "entity_id": asset.id}, {"entity_type": "ticket", "entity_id": ticket.id}}})
	rel := byKind["relationship"]
	source, target := f.CI(t, f.OrgA, f.Client1, "wp326-source"), f.CI(t, f.OrgA, f.Client1, "wp326-target")
	rel.id = create("/api/v1/relationships", map[string]any{"source_ci_id": source, "target_ci_id": target, "rel_type": "depends_on",
		"notes": rel.fields[0].value, "attributes": map[string]any{"port": rel.fields[1].value, "vlan": rel.fields[2].value, "speed": "s326"}})
	// The marks go through the API: it drops the cached snapshot of the
	// organization's field permissions.
	for _, k := range kinds {
		if r := e.call(admin, http.MethodPost, "/api/v1/field-permissions/sensitive-fields",
			map[string]any{"object_kind": k.kind, "field_key": k.fields[2].key}); r.code != http.StatusCreated {
			t.Fatalf("setup: mark %s %s sensitive: %d %s", k.kind, k.fields[2].key, r.code, trim(r.body))
		}
	}
	if _, err := e.publisher.PublishPending(bg); err != nil {
		t.Fatalf("setup: publish the events: %v", err)
	}
	if _, err := search.NewPGRepository(f.App).ReindexTenant(f.OrgCtx(f.OrgA), f.OrgA); err != nil {
		t.Fatalf("setup: reindex: %v", err)
	}

	// The stored deliveries of the bound subscription per event type.
	deliveries := map[string][]map[string]any{}
	rows, err := f.Admin.Query(bg, `SELECT event, payload FROM webhook_delivery WHERE subscription_id = $1 ORDER BY event_sequence`, sub.ID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var event string
		var payload []byte
		if err = rows.Scan(&event, &payload); err != nil {
			t.Fatal(err)
		}
		var m map[string]any
		if err = json.Unmarshal(payload, &m); err != nil {
			t.Fatal(err)
		}
		deliveries[event] = append(deliveries[event], m)
	}
	rows.Close()

	get := func(path string) frmResponse {
		t.Helper()
		r := e.call(reader, http.MethodGet, path, nil)
		if r.code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, r.code, trim(r.body))
		}
		return r
	}
	rest := func(k *frmKind, path string) func(fl *frmKindField) frmProbe {
		return func(fl *frmKindField) frmProbe {
			r := get(path)
			obj := frmItem(r.body, k.id)
			if obj == nil {
				t.Fatalf("GET %s misses %s %s: %s", path, k.kind, k.id, trim(r.body))
			}
			encoded, _ := json.Marshal(obj)
			return frmProbe{visible: strings.Contains(string(encoded), fl.value), exists: frmMember(obj, fl.path), detail: trim(string(encoded))}
		}
	}
	searchProbe := func(k *frmKind) func(fl *frmKindField) frmProbe {
		return func(fl *frmKindField) frmProbe {
			r := get("/api/v1/search?" + url.Values{"q": {fl.value}, "highlight": {"true"}}.Encode())
			hit := strings.Contains(r.body, k.id)
			return frmProbe{visible: hit || strings.Contains(r.body, fl.value), exists: hit, detail: trim(r.body)}
		}
	}
	hook := func(event string) func(fl *frmKindField) frmProbe {
		return func(fl *frmKindField) frmProbe {
			got := deliveries[event]
			if len(got) == 0 {
				t.Fatalf("webhook: no %s delivery to the bound subscription", event)
			}
			encoded, _ := json.Marshal(got)
			exists := false
			for _, d := range got {
				obj := d
				if inner, ok := d["asset"].(map[string]any); ok {
					obj = inner
				}
				changed, _ := d["changed_fields"].([]any)
				exists = exists || frmMember(obj, fl.path) || slices.Contains(changed, any(fl.path))
			}
			return frmProbe{visible: strings.Contains(string(encoded), fl.value), exists: exists, detail: trim(string(encoded))}
		}
	}
	graphQLAsset := func(fl *frmKindField) frmProbe {
		sel := fl.gql
		if sel == "" {
			sel = "customFields"
		}
		r := e.call(reader, http.MethodPost, "/bff/graphql", map[string]any{"query": fmt.Sprintf(`{ asset(id: %q) { id %s } }`, asset.id, sel)})
		if r.code != http.StatusOK || strings.Contains(r.body, `"errors"`) {
			t.Fatalf("graphql asset: %d %s", r.code, trim(r.body))
		}
		var body struct {
			Data struct {
				Asset map[string]any `json:"asset"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(r.body), &body)
		exists := body.Data.Asset[fl.gql] != nil
		if fl.gql == "" {
			custom, _ := body.Data.Asset["customFields"].(map[string]any)
			_, exists = custom[strings.TrimPrefix(fl.path, "custom_fields.")]
		}
		return frmProbe{visible: strings.Contains(r.body, fl.value), exists: exists, detail: trim(r.body)}
	}
	graphQLDocuments := func(fl *frmKindField) frmProbe {
		r := e.call(reader, http.MethodPost, "/bff/graphql", map[string]any{"query": fmt.Sprintf(`{ asset(id: %q) { documents { id title %s } } }`, asset.id, fl.gql)})
		if r.code != http.StatusOK || strings.Contains(r.body, `"errors"`) {
			t.Fatalf("graphql asset documents: %d %s", r.code, trim(r.body))
		}
		var body struct {
			Data struct {
				Asset struct {
					Documents []map[string]any `json:"documents"`
				} `json:"asset"`
			} `json:"data"`
		}
		_ = json.Unmarshal([]byte(r.body), &body)
		var doc map[string]any
		for _, d := range body.Data.Asset.Documents {
			if d["id"] == document.id {
				doc = d
			}
		}
		if doc == nil || doc["title"] != "wp326-doc" {
			t.Fatalf("graphql asset documents miss %s: %s", document.id, trim(r.body))
		}
		return frmProbe{visible: strings.Contains(r.body, fl.value), exists: doc[fl.gql] != nil, detail: trim(r.body)}
	}
	structuredAsset := func(fl *frmKindField) frmProbe {
		// Custom fields are the attributes of the query.
		column := strings.Replace(fl.path, "custom_fields.", "attributes.", 1)
		r := e.call(reader, http.MethodPost, "/api/v1/search/query?limit=200", map[string]any{"entity_kind": "asset", "columns": []string{column}})
		found := r.code == http.StatusOK && strings.Contains(r.body, fl.value)
		refused := r.code == http.StatusForbidden && strings.Contains(r.body, column) && !strings.Contains(r.body, `"total"`)
		if !found && !refused {
			t.Errorf("structured query column %s: %d %s, want 200 with the value or 403", column, r.code, trim(r.body))
		}
		return frmProbe{visible: found, exists: found, detail: trim(r.body)}
	}

	type channel struct {
		name  string
		probe func(fl *frmKindField) frmProbe
	}
	matrix := map[string][]channel{
		"asset": {
			{"REST GET /api/v1/assets/{id}", rest(asset, "/api/v1/assets/"+asset.id)},
			{"REST GET /api/v1/assets", rest(asset, "/api/v1/assets?limit=200&q=wp326")},
			{"GraphQL asset", graphQLAsset},
			{"search /api/v1/search", searchProbe(asset)},
			{"structured query /api/v1/search/query", structuredAsset},
			{"webhook asset.created", hook(events.AssetCreated)},
			{"webhook asset.changed", hook(events.AssetChanged)},
		},
		"contract": {
			{"REST GET /api/v1/contracts/{id}", rest(contract, "/api/v1/contracts/"+contract.id)},
			{"REST GET /api/v1/contracts", rest(contract, "/api/v1/contracts?limit=200")},
		},
		"ticket": {
			{"REST GET /api/v1/tickets/{id}", rest(ticket, "/api/v1/tickets/"+ticket.id)},
			{"REST GET /api/v1/tickets", rest(ticket, "/api/v1/tickets?limit=200")},
			{"search /api/v1/search", searchProbe(ticket)},
		},
		"document": {
			{"REST GET /api/v1/documents/{id}", rest(document, "/api/v1/documents/"+document.id)},
			{"REST GET /api/v1/documents", rest(document, "/api/v1/documents?limit=200")},
			{"REST GET /api/v1/tickets/{id}/attachments", rest(document, "/api/v1/tickets/"+ticket.id+"/attachments")},
			{"GraphQL Asset.documents", graphQLDocuments},
			{"search /api/v1/search", searchProbe(document)},
		},
		"relationship": {
			{"REST GET /api/v1/cis/{id}/relationships", rest(rel, "/api/v1/cis/"+source+"/relationships")},
			{"webhook relationship.created", hook(events.RelationshipCreated)},
		},
	}
	for _, k := range kinds {
		for _, ch := range matrix[k.kind] {
			for i := range k.fields {
				fl := &k.fields[i]
				t.Run(k.kind+"/"+ch.name+"/"+fl.name, func(t *testing.T) {
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
	}
	// The writes of documents (after the reads: the new values would hit
	// the search): POST with each field, PATCH with the patchable ones (a
	// document has no file_name to patch). The readable field passes, the
	// field with the grant none and the sensitive field are 403 with the
	// field and change nothing. The POST needs a file_name, the sensitive
	// field: it is refused always, with file_name among the fields.
	type frmWrite struct {
		method, path string
		body         map[string]any
		refused      []string
	}
	for i := range document.fields {
		fl := &document.fields[i]
		value := any(fl.value + "w")
		if fl.path == "tags" {
			value = []string{fl.value}
		}
		post := map[string]any{"title": "wp326-doc-w", "file_name": "wp326-w.txt"}
		post[fl.path] = value
		postRefused := []string{"file_name"}
		if !fl.readable && fl.path != "file_name" {
			postRefused = append(postRefused, fl.path)
			slices.Sort(postRefused)
		}
		writes := []frmWrite{{http.MethodPost, "/api/v1/documents", post, postRefused}}
		if fl.path != "file_name" {
			var refused []string
			if !fl.readable {
				refused = []string{fl.path}
			}
			writes = append(writes, frmWrite{http.MethodPatch, "/api/v1/documents/" + document.id, map[string]any{fl.path: value}, refused})
		}
		for _, wr := range writes {
			t.Run("document/write "+wr.method+"/"+fl.name, func(t *testing.T) {
				r := e.call(reader, wr.method, wr.path, wr.body)
				var problem struct {
					Fields []string `json:"fields"`
				}
				_ = json.Unmarshal([]byte(r.body), &problem)
				switch {
				case len(wr.refused) == 0 && r.code/100 != 2:
					t.Errorf("%s %s with %s: %d %s, want success", wr.method, wr.path, fl.path, r.code, trim(r.body))
				case len(wr.refused) > 0 && (r.code != http.StatusForbidden || !slices.Equal(problem.Fields, wr.refused)):
					t.Errorf("%s %s with %s: %d %s, want 403 with fields %v", wr.method, wr.path, fl.path, r.code, trim(r.body), wr.refused)
				}
			})
		}
	}
	var docTags []string
	var docFile string
	if err = f.Admin.QueryRow(bg, `SELECT tags, file_name FROM document WHERE id = $1`, document.id).Scan(&docTags, &docFile); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(docTags, []string{document.fields[1].value}) || docFile != document.fields[2].value+".txt" {
		t.Errorf("refused writes changed the document: tags %v, file_name %q", docTags, docFile)
	}

	// Control: the readable attribute of the edge stays in the projected
	// answer.
	if r := get("/api/v1/cis/" + source + "/relationships"); !strings.Contains(r.body, "s326") {
		t.Errorf("relationship list lost the readable attribute: %s", trim(r.body))
	}
}
