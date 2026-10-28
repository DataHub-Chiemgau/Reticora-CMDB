package rls_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant/rls"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// channelPrincipal is a caller of the channel matrix: a user session with
// every permission and a scope (TEN-04).
type channelPrincipal struct {
	kind  string
	org   string
	user  string
	scope identity.Scope
}

// channelEnv is the complete router of the PostgreSQL profile with the
// authentication replaced by a fixed principal per request.
type channelEnv struct {
	t      *testing.T
	router http.Handler
}

func newChannelEnv(t *testing.T, f *scopetest.Fixture) *channelEnv {
	t.Helper()
	return newChannelEnvWithBlobs(t, f, nil)
}

// newChannelEnvWithBlobs is newChannelEnv with a blob store for document
// content (nil: content endpoints answer 503).
func newChannelEnvWithBlobs(t *testing.T, f *scopetest.Fixture, blobs blob.Store) *channelEnv {
	t.Helper()
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	// As in cmd/server: CI writes keep the search index in sync.
	repos.CI = ci.NewIndexingRepository(repos.CI, search.NewCIIndexer(repos.Search))
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false})
	mux, _, err := server.NewRouter(repos, server.Options{
		Version:      "matrix",
		Blobs:        blobs,
		Entitlements: entitlements,
		Dispatcher:   webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		CIService:    ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials:  credential.NewService(repos.Credential, encryptor),
		OIDC:         identity.NewOIDCProvider(identity.OIDCConfig{}),
		// As in cmd/server: the notification stores of the PostgreSQL profile.
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
	return &channelEnv{t: t, router: middleware.TenantMiddleware(mux)}
}

// call sends one request as p and returns status and body.
func (e *channelEnv) call(p *channelPrincipal, method, path string, body any) (int, string) {
	e.t.Helper()
	var reader *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	scope := p.scope
	principal := identity.Principal{Subject: p.user, OrganizationID: p.org, Scope: &scope, Type: identity.PrincipalTypeUser,
		Permissions: identity.AllPermissions(), Credential: "matrix-" + p.kind}
	ctx := identity.WithPermissions(identity.WithPrincipal(req.Context(), principal), principal.Permissions)
	w := httptest.NewRecorder()
	e.router.ServeHTTP(w, req.WithContext(ctx))
	return w.Code, w.Body.String()
}

func all() identity.ScopeSet               { return identity.ScopeSet{All: true} }
func only(ids ...string) identity.ScopeSet { return identity.ScopeSet{IDs: ids} }

// seen is the set of principal kinds of the matrix that see an object.
type seen map[string]bool

func seenBy(kinds ...string) seen {
	s := seen{}
	for _, k := range kinds {
		s[k] = true
	}
	return s
}

// and is the set of the kinds in both sets (an object over two others).
func (s seen) and(o seen) seen {
	out := seen{}
	for k := range s {
		if o[k] {
			out[k] = true
		}
	}
	return out
}

// status is ok for a principal that sees the object and 404 otherwise: an
// object outside the scope or of the other organization is not found, like
// an unknown one (TEN-06, API-03).
func (s seen) status(p *channelPrincipal, ok int) int {
	if s[p.kind] {
		return ok
	}
	return http.StatusNotFound
}

// object is a row of the scenario and the principals that see it.
type object struct {
	id   string
	seen seen
}

// listed splits the ids of objs into those p must and must not find.
func listed(p *channelPrincipal, objs ...object) (show, hide []string) {
	for _, o := range objs {
		if o.seen[p.kind] {
			show = append(show, o.id)
		} else {
			hide = append(hide, o.id)
		}
	}
	return show, hide
}

// matrixProbe is one request of the channel matrix with its exact expected
// answer for one principal.
type matrixProbe struct {
	name         string
	method, path string
	body         any
	want         int
	// show must appear in the answer, hide must not.
	show, hide []string
	// after checks the stored state once the request is answered.
	after func(t *testing.T)
}

// matrixRow implements one channel of rls.Channels: the requests that read
// it and, for a channel that writes, the writes, per principal. write
// creates the objects it consumes, so every principal meets the same state.
type matrixRow struct {
	read  func(p *channelPrincipal) []matrixProbe
	write func(t *testing.T, p *channelPrincipal) []matrixProbe
}

// unknownID is a well-formed id of no object.
const unknownID = "00000000-0000-4000-8000-000000000409"

// TestChannelMatrix covers TEN-09 and TST-02 (WP-208, WP-409): every
// channel of rls.Channels (REST lists and details, GraphQL with every query
// field of the schema, search, export, topology, impact, metrics,
// relationships, sites, buildings, rooms, racks, mounts, review items with
// and without candidates, webhooks, notifications, jobs with data, the
// mirrored export job, ingest and references across organizations) is read
// and, where it writes, written by a principal of every scope kind of
// rls.ScopeKinds (organization, client, site, team, several clients with one
// site, the other organization) and by another user of the organization,
// against the complete PostgreSQL router. Every request names its exact
// status: 200/201/202/204 in the scope, 404 for an object outside the scope
// or of the other organization exactly as for an unknown id, 422 for a
// reference to such an object in a body, 403 for a row the scope may not
// write, 409 for a state conflict. Invisible roots and foreign references
// are requested, never skipped, and a 5xx is always an error.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestChannelMatrix(t *testing.T) {
	m := newChannelMatrix(t)
	rows := m.rows()

	// The table and the rows match; every GraphQL query field has a row.
	names := map[string]bool{}
	fields := map[string]string{}
	for _, ch := range rls.Channels {
		names[ch.Name] = true
		row, ok := rows[ch.Name]
		switch {
		case !ok:
			t.Errorf("rls.Channels lists %q, the matrix has no row for it", ch.Name)
		case (ch.Read != "") != (row.read != nil) || (ch.Write != "") != (row.write != nil):
			t.Errorf("channel %q: rls.Channels reads %v and writes %v, the matrix row reads %v and writes %v",
				ch.Name, ch.Read != "", ch.Write != "", row.read != nil, row.write != nil)
		}
		if ch.GraphQL != "" {
			if other, dup := fields[ch.GraphQL]; dup {
				t.Errorf("GraphQL field %s has the channels %s and %s", ch.GraphQL, other, ch.Name)
			}
			fields[ch.GraphQL] = ch.Name
		}
	}
	for name := range rows {
		if !names[name] {
			t.Errorf("the matrix row %q is not listed in rls.Channels", name)
		}
	}
	code, body := m.env.call(m.org, http.MethodPost, "/bff/graphql", map[string]any{"query": "{ __schema { queryType { fields { name } } } }"})
	var schema struct {
		Data struct {
			Schema struct {
				QueryType struct {
					Fields []struct {
						Name string `json:"name"`
					} `json:"fields"`
				} `json:"queryType"`
			} `json:"__schema"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &schema); err != nil || code != http.StatusOK || len(schema.Data.Schema.QueryType.Fields) == 0 {
		t.Fatalf("introspection of the BFF schema: %d %s", code, trim(body))
	}
	inSchema := map[string]bool{}
	for _, field := range schema.Data.Schema.QueryType.Fields {
		inSchema[field.Name] = true
		if _, ok := fields[field.Name]; !ok {
			t.Errorf("TEN-09/GQL-04: the GraphQL query field %s has no channel in rls.Channels", field.Name)
		}
	}
	for field, ch := range fields {
		if !inSchema[field] {
			t.Errorf("channel %s names the GraphQL field %s, which the schema does not have", ch, field)
		}
	}

	// Every read on the seeded state first, then the writes.
	for _, ch := range rls.Channels {
		row, ok := rows[ch.Name]
		if !ok || row.read == nil {
			continue
		}
		for _, p := range m.principals {
			t.Run("read/"+ch.Name+"/"+p.kind, func(t *testing.T) {
				m.run(t, p, row.read(p))
			})
		}
	}
	for _, ch := range rls.Channels {
		row, ok := rows[ch.Name]
		if !ok || row.write == nil {
			continue
		}
		for _, p := range m.principals {
			t.Run("write/"+ch.Name+"/"+p.kind, func(t *testing.T) {
				m.run(t, p, row.write(t, p))
			})
		}
	}
}

// channelMatrix is the scenario of TestChannelMatrix. Organization A has
// client 1 with site 1 (building, room, rack) and client 2 with site 2;
// "one" objects lie in client 1 and site 1, "two" objects in client 2 and
// site 2. Organization B has its own client, site and objects.
type channelMatrix struct {
	f   *scopetest.Fixture
	env *channelEnv
	bg  context.Context

	userA, otherA, userB, otherB, team, serverType string
	clientB, site1, site2                          string

	// principals runs every channel: one per rls.ScopeKinds and another
	// user of organization A with an org-wide scope.
	principals                    []*channelPrincipal
	org, client, orgB, otherUserP *channelPrincipal

	// Visibility of the objects by scope (TEN-05, TEN-06, CH25).
	one, two, onlyB, orgWide, allA seen

	ci1, ci1b, ci2, ci2b, ciB, ciB2     object
	rel12, rel11, rel22, relB           object
	sites, buildings, rooms, racks      [3]object
	mounts                              [3]object
	metric                              map[string]string
	reviews, reviewsNoCandidates        []object
	hooks                               []matrixHook
	noteOwn, noteOther, noteB           object
	jobC1, jobS2, jobTeam, jobMulti     object
	jobSystem, jobB, exportJob          object
	exportID                            string
	vrfA, vrfB, roleA, roleB            string
	contactA, contactB                  string
	collectorA, collectorB, userForRole string
	seq                                 int
}

func newChannelMatrix(t *testing.T) *channelMatrix {
	f := scopetest.Seed(t, "c9")
	blobs := blob.NewFileStore(t.TempDir())
	m := &channelMatrix{f: f, env: newChannelEnvWithBlobs(t, f, blobs), bg: context.Background(), metric: map[string]string{}}
	t.Cleanup(func() {
		// metric_sample has no foreign key to the organization.
		_, _ = f.Admin.Exec(m.bg, `DELETE FROM metric_sample WHERE organization_id = ANY($1::uuid[])`, []string{f.OrgA, f.OrgB})
	})
	m.userA = f.AppUser(t, f.OrgA, "matrix-a")
	m.otherA = f.AppUser(t, f.OrgA, "matrix-a2")
	m.userB = f.AppUser(t, f.OrgB, "matrix-b")
	m.otherB = f.AppUser(t, f.OrgB, "matrix-b2")
	m.userForRole = f.AppUser(t, f.OrgA, "matrix-a3")
	// Channels that resolve the caller's grants from the database (search)
	// see the users as administrators; the scope of the matrix comes from
	// the principal.
	for user, org := range map[string]string{m.userA: f.OrgA, m.otherA: f.OrgA, m.userB: f.OrgB} {
		m.exec(t, `INSERT INTO role_assignment (organization_id, user_id, role_id)
			SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, org, user)
	}
	m.team = m.scalar(t, `INSERT INTO team (organization_id, name) VALUES ($1, 'matrix team') RETURNING id::text`, f.OrgA)
	m.serverType = m.scalar(t, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`)
	room1, room2 := f.Room(t, f.Client1, "chm-s1"), f.Room(t, f.Client2, "chm-s2")
	bld1 := m.scalar(t, `SELECT building_id::text FROM room WHERE id = $1`, room1)
	bld2 := m.scalar(t, `SELECT building_id::text FROM room WHERE id = $1`, room2)
	m.site1 = m.scalar(t, `SELECT site_id::text FROM building WHERE id = $1`, bld1)
	m.site2 = m.scalar(t, `SELECT site_id::text FROM building WHERE id = $1`, bld2)
	m.clientB = m.scalar(t, `INSERT INTO client (organization_id, name, slug) VALUES ($1, 'chm client b', 'chm-client-b') RETURNING id::text`, f.OrgB)
	siteB := m.scalar(t, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'chm-sb') RETURNING id::text`, f.OrgB, m.clientB)
	bldB := m.scalar(t, `INSERT INTO building (organization_id, site_id, name) VALUES ($1, $2, 'chm-sb') RETURNING id::text`, f.OrgB, siteB)
	roomB := m.scalar(t, `INSERT INTO room (organization_id, building_id, name) VALUES ($1, $2, 'chm-sb') RETURNING id::text`, f.OrgB, bldB)

	m.org = &channelPrincipal{kind: "org", org: f.OrgA, user: m.userA, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	m.client = &channelPrincipal{kind: "client", org: f.OrgA, user: m.userA, scope: identity.Scope{Clients: only(f.Client1), Sites: all(), Teams: all()}}
	m.orgB = &channelPrincipal{kind: "other-org", org: f.OrgB, user: m.userB, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	m.otherUserP = &channelPrincipal{kind: "other-user", org: f.OrgA, user: m.otherA, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	byKind := map[string]*channelPrincipal{
		"org":       m.org,
		"client":    m.client,
		"site":      {kind: "site", org: f.OrgA, user: m.userA, scope: identity.Scope{Clients: all(), Sites: only(m.site2), Teams: all()}},
		"team":      {kind: "team", org: f.OrgA, user: m.userA, scope: identity.Scope{Clients: all(), Sites: all(), Teams: only(m.team)}},
		"multi":     {kind: "multi", org: f.OrgA, user: m.userA, scope: identity.Scope{Clients: only(f.Client1, f.Client2), Sites: only(m.site1), Teams: all()}},
		"other-org": m.orgB,
	}
	for _, kind := range rls.ScopeKinds {
		p, ok := byKind[kind]
		if !ok {
			t.Fatalf("rls.ScopeKinds lists %q, the matrix has no principal for it", kind)
		}
		m.principals = append(m.principals, p)
	}
	if len(byKind) != len(rls.ScopeKinds) {
		t.Fatalf("the matrix has %d scope kinds, rls.ScopeKinds lists %d", len(byKind), len(rls.ScopeKinds))
	}
	m.principals = append(m.principals, m.otherUserP)

	// Client and site of an object decide who sees it; the team scope
	// restricts team-bound rows only (TEN-04, CH25). An object without a
	// client or site is visible to principals unrestricted in that
	// dimension only (E-08, E-09).
	m.one = seenBy("org", "other-user", "client", "team", "multi")
	m.two = seenBy("org", "other-user", "site", "team")
	m.orgWide = seenBy("org", "other-user", "team")
	m.allA = seenBy("org", "other-user", "client", "site", "team", "multi")
	m.onlyB = seenBy("other-org")

	// CIs and relationships through the API, so search and the outbox see
	// them as in production.
	newCI := func(p *channelPrincipal, name, client, site string) string {
		body := map[string]any{"ci_type_id": m.serverType, "name": name}
		if client != "" {
			body["client_id"], body["location_id"] = client, site
		}
		return m.create(t, p, "/api/v1/cis", body)
	}
	m.ci1 = object{newCI(m.org, "chmci-c1-a", f.Client1, m.site1), m.one}
	m.ci1b = object{newCI(m.org, "chmci-c1-b", f.Client1, m.site1), m.one}
	m.ci2 = object{newCI(m.org, "chmci-c2-a", f.Client2, m.site2), m.two}
	m.ci2b = object{newCI(m.org, "chmci-c2-b", f.Client2, m.site2), m.two}
	m.ciB = object{newCI(m.orgB, "chmci-b-a", "", ""), m.onlyB}
	m.ciB2 = object{newCI(m.orgB, "chmci-b-b", "", ""), m.onlyB}
	edge := func(p *channelPrincipal, src, dst object) object {
		return object{m.create(t, p, "/api/v1/relationships", map[string]any{"source_ci_id": src.id, "target_ci_id": dst.id, "rel_type": "depends_on"}),
			src.seen.and(dst.seen)}
	}
	m.rel12, m.rel11, m.rel22 = edge(m.org, m.ci1, m.ci2), edge(m.org, m.ci1, m.ci1b), edge(m.org, m.ci2, m.ci2b)
	m.relB = edge(m.orgB, m.ciB, m.ciB2)

	// Locations: site, building, room and rack per side.
	m.sites = [3]object{{m.site1, m.one}, {m.site2, m.two}, {siteB, m.onlyB}}
	m.buildings = [3]object{{bld1, m.one}, {bld2, m.two}, {bldB, m.onlyB}}
	m.rooms = [3]object{{room1, m.one}, {room2, m.two}, {roomB, m.onlyB}}
	m.racks = [3]object{
		{m.create(t, m.org, "/api/v1/racks", map[string]any{"name": "chm-rack-1", "room_id": room1}), m.one},
		{m.create(t, m.org, "/api/v1/racks", map[string]any{"name": "chm-rack-2", "room_id": room2}), m.two},
		{m.create(t, m.orgB, "/api/v1/racks", map[string]any{"name": "chm-rack-b", "room_id": roomB}), m.onlyB},
	}
	m.mounts = [3]object{
		{m.create(t, m.org, "/api/v1/racks/"+m.racks[0].id+"/mounts", map[string]any{"ci_id": m.ci1.id, "position_u": 1}), m.one},
		{m.create(t, m.org, "/api/v1/racks/"+m.racks[1].id+"/mounts", map[string]any{"ci_id": m.ci2.id, "position_u": 1}), m.two},
		{m.create(t, m.orgB, "/api/v1/racks/"+m.racks[2].id+"/mounts", map[string]any{"ci_id": m.ciB.id, "position_u": 1}), m.onlyB},
	}

	// Metric series per CI (WP-344, WP-402).
	at := time.Now().UTC().Add(-time.Hour)
	for org, series := range map[string][]monitoring.Metric{
		f.OrgA: {{OrgID: f.OrgA, CIID: m.ci1.id, Name: "chm_metric_c1", Value: 1, Timestamp: at},
			{OrgID: f.OrgA, CIID: m.ci2.id, Name: "chm_metric_c2", Value: 2, Timestamp: at}},
		f.OrgB: {{OrgID: f.OrgB, CIID: m.ciB.id, Name: "chm_metric_b", Value: 3, Timestamp: at}},
	} {
		if err := monitoring.NewPGMetricStore(f.App).Ingest(f.OrgCtx(org), series); err != nil {
			t.Fatal(err)
		}
		for _, s := range series {
			m.metric[s.CIID] = s.Name
		}
	}

	// Review items with candidate CIs (scope from the candidates) and
	// without (scope set by the producer; none: org-wide principals only).
	m.reviews = []object{
		{m.review(t, f.OrgA, m.ci1.id, "", ""), m.one},
		{m.review(t, f.OrgA, m.ci2.id, "", ""), m.two},
		{m.review(t, f.OrgB, m.ciB.id, "", ""), m.onlyB},
	}
	m.reviewsNoCandidates = []object{
		{m.review(t, f.OrgA, "", f.Client1, m.site1), m.one},
		{m.review(t, f.OrgA, "", f.Client2, m.site2), m.two},
		{m.review(t, f.OrgA, "", "", ""), m.orgWide},
		{m.review(t, f.OrgB, "", "", ""), m.onlyB},
	}

	// Webhook subscriptions with the scope snapshot of their creator
	// (WP-601, E-98), each with a failed delivery and a dead letter;
	// user-level: the inbox.
	for _, s := range m.hookSnapshots() {
		m.hooks = append(m.hooks, m.hook(t, s))
	}
	m.noteOwn = object{m.note(t, f.OrgA, m.userA), seenBy("org", "client", "site", "team", "multi")}
	m.noteOther = object{m.note(t, f.OrgA, m.otherA), seenBy("other-user")}
	m.noteB = object{m.note(t, f.OrgB, m.userB), m.onlyB}

	// Jobs with the scope snapshot of their creator (WP-404, F-163): a job
	// is visible where its snapshot lies in the caller's scope; a dimension
	// without snapshot needs an unrestricted caller. The client-1 job has
	// data (params and result name a client-1 CI).
	m.jobC1 = object{m.job(t, f.OrgA, m.userA, "succeeded", f.Client1, "", ""), seenBy("org", "other-user", "client")}
	m.jobS2 = object{m.job(t, f.OrgA, m.userA, "queued", "", m.site2, ""), seenBy("org", "other-user", "site")}
	m.jobTeam = object{m.job(t, f.OrgA, m.userA, "queued", "", "", m.team), seenBy("org", "other-user", "team")}
	m.jobMulti = object{m.job(t, f.OrgA, m.userA, "queued", f.Client1+","+f.Client2, m.site1, ""), seenBy("org", "other-user", "multi")}
	m.jobSystem = object{m.job(t, f.OrgA, "", "queued", "-", "", ""), seenBy("org", "other-user")}
	m.jobB = object{m.job(t, f.OrgB, m.userB, "queued", "-", "", ""), m.onlyB}

	// An export of the client principal, run by the worker of cmd/server
	// with the generic job model as tracker (WP-347, F-059): the mirrored
	// job keeps creator and snapshot of the export job; only its creator
	// sees it, within its scope.
	code, body := m.env.call(m.client, http.MethodPost, "/api/v1/export/jobs", map[string]string{"format": "json"})
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil || code != http.StatusAccepted || created.ID == "" {
		t.Fatalf("setup: export as client: %d %s", code, trim(body))
	}
	worker := export.NewJobWorker(export.NewPGJobRepository(f.App), ci.NewPGRepository(f.App), blobs)
	worker.Tracker = jobs.NewService(jobs.NewPGRepository(f.App))
	for worker.ProcessOne(m.bg) {
	}
	m.exportID = created.ID
	m.exportJob = object{m.scalar(t, `SELECT id::text FROM job WHERE ref_table = 'export_job' AND ref_id = $1 AND status = 'succeeded'`, created.ID),
		seenBy("org", "client")}

	// References of the other organization (TEN-05).
	m.vrfA = m.scalar(t, `SELECT id::text FROM vrf WHERE organization_id = $1 AND is_default`, f.OrgA)
	m.vrfB = m.scalar(t, `SELECT id::text FROM vrf WHERE organization_id = $1 AND is_default`, f.OrgB)
	m.roleA = m.scalar(t, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgA)
	m.roleB = m.scalar(t, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, f.OrgB)
	m.contactA = m.scalar(t, `INSERT INTO contact (organization_id, client_id, display_name, email) VALUES ($1, $2, 'chm-contact-a', 'chm-a@example.org') RETURNING id::text`, f.OrgA, f.Client1)
	m.contactB = m.scalar(t, `INSERT INTO contact (organization_id, client_id, display_name, email) VALUES ($1, $2, 'chm-contact-b', 'chm-b@example.org') RETURNING id::text`, f.OrgB, m.clientB)
	m.collectorA = m.scalar(t, `INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'chm-collector-a', 'active') RETURNING id::text`, f.OrgA, f.Client1)
	m.collectorB = m.scalar(t, `INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'chm-collector-b', 'active') RETURNING id::text`, f.OrgB, m.clientB)
	return m
}

// run sends the probes as p and checks status, body and stored state.
func (m *channelMatrix) run(t *testing.T, p *channelPrincipal, probes []matrixProbe) {
	t.Helper()
	for _, pr := range probes {
		code, body := m.env.call(p, pr.method, pr.path, pr.body)
		label := pr.method + " " + pr.path
		if pr.name != "" {
			label += " (" + pr.name + ")"
		}
		switch {
		case code >= 500:
			t.Errorf("TEN-09: %s as %s answered %d, a 5xx is never a refusal: %s", label, p.kind, code, trim(body))
		case code != pr.want:
			t.Errorf("%s as %s: %d, want %d: %s", label, p.kind, code, pr.want, trim(body))
		}
		for _, s := range pr.show {
			if s == "" || !strings.Contains(body, s) {
				t.Errorf("%s as %s: %q missing: %s", label, p.kind, s, trim(body))
			}
		}
		for _, h := range pr.hide {
			if h == "" || strings.Contains(body, h) {
				t.Errorf("TEN-09: %s as %s discloses %q: %s", label, p.kind, h, trim(body))
			}
		}
		if pr.after != nil {
			pr.after(t)
		}
	}
}

// rows are the matrix rows of rls.Channels by name.
func (m *channelMatrix) rows() map[string]matrixRow {
	get := func(path string, want int) matrixProbe {
		return matrixProbe{method: http.MethodGet, path: path, want: want}
	}
	// list reads a list and finds exactly the visible objects.
	list := func(p *channelPrincipal, path string, objs ...object) matrixProbe {
		show, hide := listed(p, objs...)
		return matrixProbe{method: http.MethodGet, path: path, want: http.StatusOK, show: show, hide: hide}
	}
	// details reads every object and an unknown id.
	details := func(p *channelPrincipal, prefix, suffix string, objs ...object) []matrixProbe {
		var out []matrixProbe
		for _, o := range objs {
			out = append(out, get(prefix+o.id+suffix, o.seen.status(p, http.StatusOK)))
		}
		return append(out, get(prefix+unknownID+suffix, http.StatusNotFound))
	}
	gql := func(name, query string, show, hide []string) matrixProbe {
		return matrixProbe{name: name, method: http.MethodPost, path: "/bff/graphql", body: map[string]any{"query": query},
			want: http.StatusOK, show: show, hide: hide}
	}
	cis := []object{m.ci1, m.ci1b, m.ci2, m.ci2b, m.ciB, m.ciB2}
	names := map[string]string{m.ci1.id: "chmci-c1-a", m.ci1b.id: "chmci-c1-b", m.ci2.id: "chmci-c2-a", m.ci2b.id: "chmci-c2-b",
		m.ciB.id: "chmci-b-a", m.ciB2.id: "chmci-b-b"}
	// nameOf is the name of a CI of the scenario, none for the unknown id.
	nameOf := func(id string) []string {
		if name, ok := names[id]; ok {
			return []string{name}
		}
		return nil
	}
	// hiddenCIs are the ids and names of the CIs p does not see.
	hiddenCIs := func(p *channelPrincipal) []string {
		var out []string
		for _, c := range cis {
			if !c.seen[p.kind] {
				out = append(out, c.id, names[c.id])
			}
		}
		return out
	}
	// roots are the CIs a graph starts from: one per side and an unknown id.
	roots := []object{m.ci1, m.ci2, m.ciB, {unknownID, seen{}}}
	jobsAll := []object{m.jobC1, m.jobS2, m.jobTeam, m.jobMulti, m.jobSystem, m.jobB, m.exportJob}
	reviewsAll := append(slices.Clone(m.reviews), m.reviewsNoCandidates...)
	f := m.f

	rows := map[string]matrixRow{}
	rows["ci"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			return append([]matrixProbe{list(p, "/api/v1/cis?limit=200", cis...)}, details(p, "/api/v1/cis/", "", cis...)...)
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			for _, c := range []object{m.ci1, m.ci2, m.ciB} {
				model := "chm-model-" + p.kind
				out = append(out, matrixProbe{method: http.MethodPatch, path: "/api/v1/cis/" + c.id, body: map[string]any{"model": model},
					want: c.seen.status(p, http.StatusOK), after: m.unchanged(c, p, `SELECT COALESCE(model, '') <> $2 FROM ci WHERE id = $1`, model)})
			}
			// New CIs: at a site outside the scope the location is not found
			// (422 on location_id, as for an unknown one); a row in a client
			// outside the scope, or without client or site for a principal
			// restricted in that dimension (E-09), is refused by RLS (403);
			// a client of the other organization fails its composite foreign
			// key (422).
			const location = `"field":"location_id"`
			atSite1, atSite1Field := http.StatusForbidden, ""
			if !m.one[p.kind] {
				atSite1, atSite1Field = http.StatusUnprocessableEntity, location
			}
			for _, c := range []struct {
				name, client, site string
				seen               seen
				refused            int
				field              string
			}{
				{"in client 1 at site 1", f.Client1, m.site1, m.one, http.StatusUnprocessableEntity, location},
				{"in client 2 at site 2", f.Client2, m.site2, m.two, http.StatusUnprocessableEntity, location},
				{"at site 1 without client", "", m.site1, m.orgWide, atSite1, atSite1Field},
				{"in client 2 without site", f.Client2, "", m.orgWide, m.foreignRef(p, http.StatusForbidden), ""},
				{"without client and site", "", "", seenBy("org", "other-user", "team", "other-org"), http.StatusForbidden, ""},
			} {
				name := m.name("chmtmp")
				body := map[string]any{"ci_type_id": m.serverType, "name": name}
				if c.client != "" {
					body["client_id"] = c.client
				}
				if c.site != "" {
					body["location_id"] = c.site
				}
				pr := matrixProbe{name: c.name, method: http.MethodPost, path: "/api/v1/cis", body: body, want: c.refused}
				if c.seen[p.kind] {
					pr.want = http.StatusCreated
				} else if c.field != "" {
					pr.show = []string{c.field}
				}
				pr.after = m.count(pr.want == http.StatusCreated, `SELECT count(*) FROM ci WHERE name = $1`, name)
				out = append(out, pr)
			}
			// DELETE of a CI per side.
			for _, c := range []struct {
				org, client, site string
				seen              seen
			}{{f.OrgA, f.Client1, m.site1, m.one}, {f.OrgA, f.Client2, m.site2, m.two}, {f.OrgB, "", "", m.onlyB}} {
				id := m.freshCI(t, c.org, c.client, c.site)
				out = append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/cis/" + id, want: c.seen.status(p, http.StatusNoContent),
					after: m.count(!c.seen[p.kind], `SELECT count(*) FROM ci WHERE id = $1 AND deleted_at IS NULL`, id)})
			}
			return append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/cis/" + unknownID, want: http.StatusNotFound})
		},
	}
	rows["search"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		return []matrixProbe{list(p, "/api/v1/search?q=chmci", cis...)}
	}}
	rows["export"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		return []matrixProbe{list(p, "/api/v1/export/cis?format=json", cis...)}
	}}
	// graph reads a graph per root: the visible root answers with its
	// visible neighbourhood, every other root 404 "ci not found" without
	// naming it (WP-403, F-161).
	graph := func(p *channelPrincipal, prefix, suffix string) []matrixProbe {
		var out []matrixProbe
		for _, r := range roots {
			pr := get(prefix+r.id+suffix, r.seen.status(p, http.StatusOK))
			if r.seen[p.kind] {
				pr.show, pr.hide = []string{r.id}, hiddenCIs(p)
			} else {
				pr.show, pr.hide = []string{"ci not found"}, nameOf(r.id)
			}
			out = append(out, pr)
		}
		return out
	}
	rows["topology"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		out := []matrixProbe{list(p, "/api/v1/topology", cis...)}
		out = append(out, graph(p, "/api/v1/topology?depth=3&root_ci_id=", "")...)
		return append(out, graph(p, "/api/v1/topology/cis/", "/neighbors")...)
	}}
	rows["impact"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		out := graph(p, "/api/v1/impact/", "")
		out = append(out, graph(p, "/api/v1/impact?ci_ids=", "")...)
		return append(out, graph(p, "/api/v1/topology/cis/", "/impact")...)
	}}
	rows["metrics"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for _, c := range []object{m.ci1, m.ci2, m.ciB} {
			pr := get("/api/v1/cis/"+c.id+"/metrics", c.seen.status(p, http.StatusOK))
			if c.seen[p.kind] {
				pr.show = []string{m.metric[c.id]}
			} else {
				pr.hide = []string{m.metric[c.id]}
			}
			out = append(out, pr)
		}
		return append(out, get("/api/v1/cis/"+unknownID+"/metrics", http.StatusNotFound), get("/api/v1/cis/not-a-uuid/metrics", http.StatusBadRequest))
	}}
	rels := []object{m.rel12, m.rel11, m.rel22, m.relB}
	relEnds := [][2]string{{m.ci1.id, m.ci2.id}, {m.ci1.id, m.ci1b.id}, {m.ci2.id, m.ci2b.id}, {m.ciB.id, m.ciB2.id}}
	rows["relationship"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			out := []matrixProbe{list(p, "/api/v1/relationships?limit=200", rels...)}
			// The relationships of a CI are a filter of the list (OpenAPI
			// listCIRelationships: 200 only): a CI outside the scope has
			// none, like an unknown one.
			for _, r := range roots {
				pr := get("/api/v1/cis/"+r.id+"/relationships", http.StatusOK)
				for j, rel := range rels {
					if r.seen[p.kind] && rel.seen[p.kind] && slices.Contains(relEnds[j][:], r.id) {
						pr.show = append(pr.show, rel.id)
					}
				}
				if !r.seen[p.kind] {
					pr.show = []string{`"total":0`}
				}
				pr.hide = hiddenCIs(p)
				out = append(out, pr)
			}
			return out
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			// A relationship needs both ends in the scope; an end outside
			// it is a 422 on its field, as for an unknown CI (WP-401).
			for _, e := range [][2]object{{m.ci1, m.ci1b}, {m.ci2, m.ci2b}, {m.ci1, m.ci2}, {m.ciB, m.ciB2}, {m.ciB, m.ci1}, {m.ci1, m.ciB}} {
				src, dst := e[0], e[1]
				pr := matrixProbe{method: http.MethodPost, path: "/api/v1/relationships", name: names[src.id] + " -> " + names[dst.id],
					body: map[string]any{"source_ci_id": src.id, "target_ci_id": dst.id, "rel_type": "connected_to"}}
				if src.seen[p.kind] && dst.seen[p.kind] {
					pr.want = http.StatusCreated
				} else {
					pr.want = http.StatusUnprocessableEntity
					for field, end := range map[string]object{`"field":"source_ci_id"`: src, `"field":"target_ci_id"`: dst} {
						if end.seen[p.kind] {
							pr.hide = append(pr.hide, field)
						} else {
							pr.show = append(pr.show, field)
						}
					}
				}
				stored := pr.want == http.StatusCreated
				pr.after = func(t *testing.T) {
					m.count(stored, `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2 AND rel_type = 'connected_to'`, src.id, dst.id)(t)
					m.exec(t, `DELETE FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2 AND rel_type = 'connected_to'`, src.id, dst.id)
				}
				out = append(out, pr)
			}
			for _, e := range []struct {
				org      string
				src, dst object
			}{{f.OrgA, m.ci1, m.ci1b}, {f.OrgA, m.ci2, m.ci2b}, {f.OrgA, m.ci1, m.ci2}, {f.OrgB, m.ciB, m.ciB2}} {
				id := m.scalar(t, `INSERT INTO ci_relationship (organization_id, source_ci_id, target_ci_id, rel_type) VALUES ($1, $2, $3, 'monitors') RETURNING id::text`,
					e.org, e.src.id, e.dst.id)
				vis := e.src.seen.and(e.dst.seen)
				kept := m.count(!vis[p.kind], `SELECT count(*) FROM ci_relationship WHERE id = $1`, id)
				out = append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/relationships/" + id, want: vis.status(p, http.StatusNoContent),
					after: func(t *testing.T) {
						kept(t)
						m.exec(t, `DELETE FROM ci_relationship WHERE id = $1`, id)
					}})
			}
			return append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/relationships/" + unknownID, want: http.StatusNotFound})
		},
	}

	// Locations: list, detail, rename, create below a parent and delete per
	// side (TEN-05, CH25). A new site lies in no site of a site scope. A
	// parent outside the scope or of the other organization is a 422 for
	// buildings, rooms and racks (as for an unknown parent). A new site
	// outside the scope of a principal restricted in client or site is
	// refused by RLS before its foreign key is checked (403); org-wide, a
	// client of the other organization fails the composite foreign key
	// (422).
	location := func(kind, table, parentField string, objs, parents [3]object, newSeen [3]seen, rlsFirst bool,
		fresh func(t *testing.T, side int) string) matrixRow {
		return matrixRow{
			read: func(p *channelPrincipal) []matrixProbe {
				return append([]matrixProbe{list(p, "/api/v1/"+kind+"?limit=200", objs[:]...)}, details(p, "/api/v1/"+kind+"/", "", objs[:]...)...)
			},
			write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
				var out []matrixProbe
				for _, o := range objs {
					name := m.name("chm-renamed")
					out = append(out, matrixProbe{method: http.MethodPatch, path: "/api/v1/" + kind + "/" + o.id, body: map[string]any{"name": name},
						want: o.seen.status(p, http.StatusOK), after: m.unchanged(o, p, `SELECT name <> $2 FROM `+table+` WHERE id = $1`, name)})
				}
				for side, parent := range parents {
					name := m.name("chm-new")
					want := http.StatusUnprocessableEntity
					if rlsFirst && restricted(p) {
						want = http.StatusForbidden
					}
					if newSeen[side][p.kind] {
						want = http.StatusCreated
					}
					out = append(out, matrixProbe{name: fmt.Sprintf("%s of side %d", parentField, side+1), method: http.MethodPost, path: "/api/v1/" + kind,
						body: map[string]any{"name": name, parentField: parent.id}, want: want,
						after: m.count(want == http.StatusCreated, `SELECT count(*) FROM `+table+` WHERE name = $1`, name)})
				}
				for side := range objs {
					id := fresh(t, side)
					out = append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/" + kind + "/" + id, want: newSeen[side].status(p, http.StatusNoContent),
						after: m.count(!newSeen[side][p.kind], `SELECT count(*) FROM `+table+` WHERE id = $1`, id)})
				}
				return append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/" + kind + "/" + unknownID, want: http.StatusNotFound})
			},
		}
	}
	clients := [3]object{{f.Client1, m.one}, {f.Client2, m.two}, {m.clientB, m.onlyB}}
	orgOf := [3]string{f.OrgA, f.OrgA, f.OrgB}
	newSites := [3]seen{seenBy("org", "other-user", "client", "team"), seenBy("org", "other-user", "team"), m.onlyB}
	rows["site"] = location("sites", "site", "client_id", m.sites, clients, newSites, true, func(t *testing.T, side int) string {
		return m.scalar(t, `INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, $3) RETURNING id::text`, orgOf[side], clients[side].id, m.name("chm-del"))
	})
	sides := [3]seen{m.one, m.two, m.onlyB}
	rows["building"] = location("buildings", "building", "site_id", m.buildings, m.sites, sides, false, func(t *testing.T, side int) string {
		return m.scalar(t, `INSERT INTO building (organization_id, site_id, name) VALUES ($1, $2, $3) RETURNING id::text`, orgOf[side], m.sites[side].id, m.name("chm-del"))
	})
	rows["room"] = location("rooms", "room", "building_id", m.rooms, m.buildings, sides, false, func(t *testing.T, side int) string {
		return m.scalar(t, `INSERT INTO room (organization_id, building_id, name) VALUES ($1, $2, $3) RETURNING id::text`, orgOf[side], m.buildings[side].id, m.name("chm-del"))
	})
	rack := location("racks", "rack", "room_id", m.racks, m.rooms, sides, false, func(t *testing.T, side int) string {
		return m.scalar(t, `INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, $3) RETURNING id::text`, orgOf[side], m.rooms[side].id, m.name("chm-del"))
	})
	// The rack detail includes its layout; a refused delete keeps the rack
	// and its location row (WP-340, F-002).
	rack.read = func(p *channelPrincipal) []matrixProbe {
		out := append([]matrixProbe{list(p, "/api/v1/racks?limit=200", m.racks[:]...)}, details(p, "/api/v1/racks/", "", m.racks[:]...)...)
		return append(out, details(p, "/api/v1/racks/", "/layout", m.racks[:]...)...)
	}
	rackWrite := rack.write
	rows["rack"] = matrixRow{read: rack.read, write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
		out := rackWrite(t, p)
		for _, r := range m.racks {
			if r.seen[p.kind] {
				continue
			}
			out = append(out, matrixProbe{name: "out of scope", method: http.MethodDelete, path: "/api/v1/racks/" + r.id, want: http.StatusNotFound,
				after: m.count(true, `SELECT count(*) FROM rack JOIN location ON location.id = rack.id WHERE rack.id = $1`, r.id)})
		}
		return out
	}}

	// Mounts: visible with rack and CI (WP-340, F-180, WP-406).
	rows["mount"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			for i, r := range m.racks {
				pr := get("/api/v1/racks/"+r.id+"/mounts", r.seen.status(p, http.StatusOK))
				if r.seen[p.kind] {
					pr.show = []string{m.mounts[i].id}
				} else {
					pr.hide = []string{m.mounts[i].id}
				}
				out = append(out, pr)
			}
			out = append(out, get("/api/v1/racks/"+unknownID+"/mounts", http.StatusNotFound))
			return append(out, details(p, "/api/v1/mounts/", "", m.mounts[:]...)...)
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			clientOf, siteOf := [3]string{f.Client1, f.Client2, ""}, [3]string{m.site1, m.site2, ""}
			for side, r := range m.racks {
				// Three free units per principal and rack (42U, U1 is taken).
				pos := 3 + 5*slices.Index(m.principals, p)
				// A CI of the rack's side and one of the next side: a rack
				// outside the scope is not found, a CI outside it is a 422 on
				// ci_id like an unknown one (WP-406).
				for i, other := range []int{side, (side + 1) % 3} {
					id := m.freshCI(t, orgOf[other], clientOf[other], siteOf[other])
					want := r.seen.status(p, http.StatusCreated)
					var show []string
					if r.seen[p.kind] && !m.racks[other].seen[p.kind] {
						want, show = http.StatusUnprocessableEntity, []string{`"field":"ci_id"`}
					}
					out = append(out, matrixProbe{name: fmt.Sprintf("CI of side %d", other+1), method: http.MethodPost, path: "/api/v1/racks/" + r.id + "/mounts",
						body: map[string]any{"ci_id": id, "position_u": pos + i}, want: want, show: show,
						after: m.count(want == http.StatusCreated, `SELECT count(*) FROM rack_mount WHERE ci_id = $1`, id)})
				}
				owner := m.org
				if side == 2 {
					owner = m.orgB
				}
				mount := m.create(t, owner, "/api/v1/racks/"+r.id+"/mounts",
					map[string]any{"ci_id": m.freshCI(t, orgOf[side], clientOf[side], siteOf[side]), "position_u": pos + 2})
				out = append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/mounts/" + mount, want: r.seen.status(p, http.StatusNoContent),
					after: m.count(!r.seen[p.kind], `SELECT count(*) FROM rack_mount WHERE id = $1`, mount)})
			}
			return append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/mounts/" + unknownID, want: http.StatusNotFound})
		},
	}

	// Review items (WP-342, F-003): resolving needs the item in the scope.
	review := func(objs []object, fresh func(t *testing.T, i int) string) matrixRow {
		return matrixRow{
			read: func(p *channelPrincipal) []matrixProbe {
				return append([]matrixProbe{list(p, "/api/v1/review-items?limit=200", objs...)}, details(p, "/api/v1/review-items/", "", objs...)...)
			},
			write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
				var out []matrixProbe
				for i, o := range objs {
					id := fresh(t, i)
					out = append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/review-items/" + id + "/resolve", body: map[string]any{"action": "dismiss"},
						want: o.seen.status(p, http.StatusOK), after: m.count(!o.seen[p.kind], `SELECT count(*) FROM review_item WHERE id = $1 AND status = 'open'`, id)})
				}
				return append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/review-items/" + unknownID + "/resolve",
					body: map[string]any{"action": "dismiss"}, want: http.StatusNotFound})
			},
		}
	}
	rows["review"] = review(m.reviews, func(t *testing.T, i int) string {
		return m.review(t, orgOf[i], [3]string{m.ci1.id, m.ci2.id, m.ciB.id}[i], "", "")
	})
	rows["review-without-candidates"] = review(m.reviewsNoCandidates, func(t *testing.T, i int) string {
		return m.review(t, [4]string{f.OrgA, f.OrgA, f.OrgA, f.OrgB}[i], "", [4]string{f.Client1, f.Client2, "", ""}[i], [4]string{m.site1, m.site2, "", ""}[i])
	})

	// Webhook subscriptions carry the scope snapshot of their creator
	// (WP-601, E-98): list, detail, deliveries and dead letters show a
	// subscription where its snapshot lies in the caller's scope; test,
	// secret rotation, retry and delete of any other answer 404 like an
	// unknown id and change nothing. Every principal creates subscriptions
	// with its own scope as snapshot; a service account of the other
	// organization is not found (422 on service_account_id).
	hookObjects := func(dead bool) []object {
		var out []object
		for _, h := range m.hooks {
			id := h.id
			if dead {
				id = h.dead
			}
			out = append(out, object{id, h.seen})
		}
		return out
	}
	rows["webhook"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			out := append([]matrixProbe{list(p, "/api/v1/webhooks?limit=200", hookObjects(false)...)},
				details(p, "/api/v1/webhooks/", "", hookObjects(false)...)...)
			for _, h := range m.hooks {
				probe := get("/api/v1/webhooks/"+h.id+"/deliveries", h.seen.status(p, http.StatusOK))
				if h.seen[p.kind] {
					probe.show = []string{h.failed}
				} else {
					probe.hide = []string{h.failed}
				}
				out = append(out, probe)
			}
			return append(out, get("/api/v1/webhooks/"+unknownID+"/deliveries", http.StatusNotFound),
				list(p, "/api/v1/webhooks/dead-letters?limit=200", hookObjects(true)...))
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			// snapshot is the scope of p as the creator's snapshot is stored.
			dim := func(s identity.ScopeSet) string {
				if s.All {
					return "NULL"
				}
				return "{" + strings.Join(s.IDs, ",") + "}"
			}
			snapshot := "true|" + dim(p.scope.Clients) + "|" + dim(p.scope.Sites) + "|" + dim(p.scope.Teams)
			stored := `SELECT COALESCE((SELECT scope_recorded::text || '|' || COALESCE(scope_clients::text, 'NULL') || '|' ||
				COALESCE(scope_sites::text, 'NULL') || '|' || COALESCE(scope_teams::text, 'NULL')
				FROM webhook_subscription WHERE organization_id = $1 AND name = $2), 'not stored')`
			otherOrg := map[string]string{f.OrgA: f.OrgB, f.OrgB: f.OrgA}[p.org]
			account := func(org string) string {
				return m.scalar(t, `INSERT INTO service_account (organization_id, name) VALUES ($1, $2) RETURNING id::text`, org, m.name("chm-sa"))
			}
			var out []matrixProbe
			for _, c := range []struct {
				sa     string
				want   int
				stored bool
			}{{"", http.StatusCreated, true}, {account(p.org), http.StatusCreated, true}, {account(otherOrg), http.StatusUnprocessableEntity, false}} {
				name := m.name("chm-hook")
				body := map[string]any{"name": name, "url": "https://hooks.example.org/" + name, "secret": "s", "events": []string{"ci.created"}}
				var show []string
				if c.sa != "" {
					body["service_account_id"] = c.sa
				}
				if !c.stored {
					show = []string{`"field":"service_account_id"`}
				}
				out = append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/webhooks", body: body, want: c.want, show: show,
					after: func(t *testing.T) {
						t.Helper()
						if c.stored {
							if got := m.scalar(t, stored, p.org, name); got != snapshot {
								t.Errorf("snapshot of the subscription %s created by %s: %s, want %s", name, p.kind, got, snapshot)
							}
							return
						}
						m.count(false, `SELECT count(*) FROM webhook_subscription WHERE name = $1`, name)(t)
					}})
			}
			for _, s := range m.hookSnapshots() {
				h := m.hook(t, s)
				status := func(ok int) int { return h.seen.status(p, ok) }
				out = append(out,
					matrixProbe{method: http.MethodPost, path: "/api/v1/webhooks/" + h.id + "/test", want: status(http.StatusOK)},
					matrixProbe{method: http.MethodPost, path: "/api/v1/webhooks/" + h.id + "/rotate-secret", body: map[string]any{"secret": "chm-rotated-secret"},
						want:  status(http.StatusOK),
						after: m.count(h.seen[p.kind], `SELECT count(*) FROM webhook_subscription WHERE id = $1 AND secret = 'chm-rotated-secret'`, h.id)},
					matrixProbe{method: http.MethodPost, path: "/api/v1/webhooks/" + h.id + "/deliveries/" + h.failed + "/retry", want: status(http.StatusAccepted),
						after: m.count(!h.seen[p.kind], `SELECT count(*) FROM webhook_delivery WHERE id = $1 AND status = 'failed'`, h.failed)},
					matrixProbe{method: http.MethodDelete, path: "/api/v1/webhooks/" + h.id, want: status(http.StatusNoContent),
						after: m.count(!h.seen[p.kind], `SELECT count(*) FROM webhook_subscription WHERE id = $1`, h.id)})
			}
			for _, probe := range []struct{ method, path string }{
				{http.MethodPost, "/test"}, {http.MethodPost, "/rotate-secret"}, {http.MethodPost, "/deliveries/" + unknownID + "/retry"}, {http.MethodDelete, ""},
			} {
				out = append(out, matrixProbe{method: probe.method, path: "/api/v1/webhooks/" + unknownID + probe.path, want: http.StatusNotFound})
			}
			return out
		},
	}

	// The notification inbox belongs to its user.
	notes := []object{m.noteOwn, m.noteOther, m.noteB}
	rows["notification"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			return []matrixProbe{list(p, "/api/v1/notifications?limit=200", notes...)}
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			inboxes := []struct {
				org, user string
				seen      seen
			}{{f.OrgA, m.userA, m.noteOwn.seen}, {f.OrgA, m.otherA, m.noteOther.seen}, {f.OrgB, m.userB, m.onlyB}}
			for _, n := range inboxes {
				id := m.note(t, n.org, n.user)
				out = append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/notifications/" + id + "/read", body: map[string]any{},
					want: n.seen.status(p, http.StatusNoContent), after: m.count(!n.seen[p.kind], `SELECT count(*) FROM notification_inbox WHERE id = $1 AND read_at IS NULL`, id)})
			}
			// Marking everything read marks the caller's entries only.
			var checks []func(t *testing.T)
			for _, n := range inboxes {
				id := m.note(t, n.org, n.user)
				checks = append(checks, m.count(!n.seen[p.kind], `SELECT count(*) FROM notification_inbox WHERE id = $1 AND read_at IS NULL`, id))
			}
			return append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/notifications/read-all", body: map[string]any{}, want: http.StatusOK,
				after: func(t *testing.T) {
					for _, check := range checks {
						check(t)
					}
				}})
		},
	}

	// Jobs (WP-404, F-163): list, detail and the result with the data of a
	// client-1 job; cancelling needs the job in the scope and its creator
	// or the permission of its kind (none for this kind: 403); a job that
	// is not revertible is 409, after the scope check.
	snapshots := []struct {
		org, creator, clients, sites, teams string
		seen                                seen
	}{
		{f.OrgA, m.userA, f.Client1, "", "", m.jobC1.seen},
		{f.OrgA, m.userA, "", m.site2, "", m.jobS2.seen},
		{f.OrgA, m.userA, "", "", m.team, m.jobTeam.seen},
		{f.OrgA, m.userA, f.Client1 + "," + f.Client2, m.site1, "", m.jobMulti.seen},
		{f.OrgA, m.userA, "-", "", "", m.jobSystem.seen},
		{f.OrgB, m.userB, "-", "", "", m.jobB.seen},
	}
	rows["job"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			out := []matrixProbe{list(p, "/api/v1/jobs?limit=200", jobsAll[:6]...)}
			out = append(out, details(p, "/api/v1/jobs/", "", jobsAll[:6]...)...)
			result := get("/api/v1/jobs/"+m.jobC1.id+"/result", m.jobC1.seen.status(p, http.StatusOK))
			if m.jobC1.seen[p.kind] {
				result.show = []string{m.ci1.id}
			} else {
				result.hide = []string{m.ci1.id}
			}
			return append(out, result, get("/api/v1/jobs/"+unknownID+"/result", http.StatusNotFound))
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			for _, s := range snapshots {
				id := m.job(t, s.org, s.creator, "queued", s.clients, s.sites, s.teams)
				want := s.seen.status(p, http.StatusOK)
				if want == http.StatusOK && p.user != s.creator {
					want = http.StatusForbidden
				}
				out = append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/jobs/" + id, want: want,
					after: m.count(want != http.StatusOK, `SELECT count(*) FROM job WHERE id = $1 AND status = 'queued'`, id)})
			}
			for _, j := range jobsAll[:6] {
				out = append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/jobs/" + j.id + "/revert", body: map[string]any{},
					want: j.seen.status(p, http.StatusConflict)})
			}
			return append(out, matrixProbe{method: http.MethodDelete, path: "/api/v1/jobs/" + unknownID, want: http.StatusNotFound})
		},
	}
	// The export job mirrored into the job model (WP-347, F-059): its
	// creator within its scope only, also not the other user with an
	// org-wide scope (EXP-01).
	rows["export-job"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			exp := object{m.exportID, m.exportJob.seen}
			out := []matrixProbe{list(p, "/api/v1/jobs?kind=export&limit=200", m.exportJob), list(p, "/api/v1/export/jobs?limit=200", exp)}
			out = append(out, details(p, "/api/v1/jobs/", "", m.exportJob)...)
			// The mirrored export job carries {export_id, row_count} as its
			// result (WP-515, F-347).
			out = append(out, get("/api/v1/jobs/"+m.exportJob.id+"/result", m.exportJob.seen.status(p, http.StatusOK)))
			return append(out, details(p, "/api/v1/export/jobs/", "", exp)...)
		},
		write: func(_ *testing.T, p *channelPrincipal) []matrixProbe {
			unchanged := m.count(true, `SELECT count(*) FROM job WHERE id = $1 AND status = 'succeeded'`, m.exportJob.id)
			return []matrixProbe{
				{method: http.MethodDelete, path: "/api/v1/jobs/" + m.exportJob.id, want: m.exportJob.seen.status(p, http.StatusConflict), after: unchanged},
				{method: http.MethodPost, path: "/api/v1/jobs/" + m.exportJob.id + "/revert", body: map[string]any{},
					want: m.exportJob.seen.status(p, http.StatusConflict), after: unchanged},
			}
		},
	}

	// Ingest (TEN-05): a record without client creates an org-wide CI,
	// which a principal restricted in client or site may not write (RLS,
	// 403); a record of client 1 needs client 1 and an unrestricted site
	// scope, and for the other organization it names an unknown client
	// (422). The CI lands in the caller's organization.
	rows["ingest"] = matrixRow{write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i, r := range []struct {
			name, client string
			seen         seen
			refused      int
		}{
			{"without client", "", seenBy("org", "other-user", "team", "other-org"), http.StatusForbidden},
			{"of client 1", f.Client1, seenBy("org", "other-user", "client", "team"), m.foreignRef(p, http.StatusForbidden)},
		} {
			serial := fmt.Sprintf("CHM-INGEST-%s-%d", p.kind, i)
			rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "chm-ingested-" + serial,
				IP: fmt.Sprintf("192.0.2.%d", 10+2*slices.Index(m.principals, p)+i), Serial: serial}, time.Now().Add(-time.Minute))
			rec.ClientID = r.client
			want := r.refused
			if r.seen[p.kind] {
				want = http.StatusAccepted
			}
			out = append(out, matrixProbe{name: r.name, method: http.MethodPost, path: "/api/v1/ingest/bulk", body: plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}},
				want: want, after: func(t *testing.T) {
					m.count(want == http.StatusAccepted, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = $2`, p.org, serial)(t)
					m.count(false, `SELECT count(*) FROM ci WHERE organization_id <> $1 AND serial_number = $2`, p.org, serial)(t)
				}})
		}
		return out
	}}

	// References of the other organization in a body (TEN-05): foreign keys
	// are checked without row level security, so every write refuses what
	// the caller cannot see, exactly as an unknown id; the org-wide
	// principals write the same rows with their own references.
	rows["reference"] = matrixRow{write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
		own := struct{ client, vrf, contact, collector, ci, user, role string }{f.Client1, m.vrfA, m.contactA, m.collectorA, m.ci1.id, m.userForRole, m.roleA}
		foreign := struct{ client, vrf, contact, collector, ci, user, role string }{m.clientB, m.vrfB, m.contactB, m.collectorB, m.ciB.id, m.otherB, m.roleB}
		if p.org == f.OrgB {
			own, foreign = foreign, own
			own.user = m.otherB
			foreign.user = m.userForRole
		}
		if !m.ci1.seen[p.kind] && p.org == f.OrgA {
			own.ci = m.ci2.id
		}
		idx := slices.Index(m.principals, p)
		cidr := func(i int) string { return fmt.Sprintf("10.%d.%d.0/24", 100+idx, i) }
		// A row outside the scope of a principal restricted in client (or,
		// for subnets, in site: a subnet without site, E-09) is refused by
		// RLS before the foreign key is checked (403); org-wide the
		// composite foreign key fails (422).
		clientRefused, subnetRefused := http.StatusUnprocessableEntity, http.StatusUnprocessableEntity
		if !p.scope.Clients.All {
			clientRefused = http.StatusForbidden
		}
		if restricted(p) {
			subnetRefused = http.StatusForbidden
		}
		var out []matrixProbe
		n := 0
		subnet := func(field string) func(ref string) map[string]any {
			return func(ref string) map[string]any {
				n++
				return map[string]any{"cidr": cidr(n), field: ref}
			}
		}
		for _, w := range []struct {
			name, path   string
			body         func(ref string) map[string]any
			foreign, own string
			refused      int
		}{
			{"contact in a client", "/api/v1/contacts", func(ref string) map[string]any {
				return map[string]any{"display_name": m.name("chm-x"), "client_id": ref}
			}, foreign.client, own.client, clientRefused},
			{"subnet in a client", "/api/v1/subnets", subnet("client_id"), foreign.client, own.client, subnetRefused},
			{"subnet in a VRF", "/api/v1/subnets", subnet("vrf_id"), foreign.vrf, own.vrf, subnetRefused},
			// The CI of the path is the caller's; an invisible contact is
			// not found like the CI would be.
			{"contact link", "/api/v1/cis/" + own.ci + "/contacts", func(ref string) map[string]any {
				return map[string]any{"contact_id": ref, "relationship_type": "owner"}
			}, foreign.contact, own.contact, http.StatusNotFound},
			{"discovery scope of a collector", "/api/v1/discovery-scopes", func(ref string) map[string]any {
				n++
				return map[string]any{"collector_id": ref, "kind": "cidr", "target": cidr(n), "name": m.name("chm-x"), "schedule": "24h"}
			}, foreign.collector, own.collector, http.StatusUnprocessableEntity},
			{"role assignment of a user", "/api/v1/role-assignments", func(ref string) map[string]any {
				return map[string]any{"user_id": ref, "role_id": own.role}
			}, foreign.user, own.user, http.StatusNotFound},
			// Without positive control: own user and own role are assigned
			// above, the same pair again is a conflict.
			{"role assignment of a role", "/api/v1/role-assignments", func(ref string) map[string]any {
				return map[string]any{"user_id": own.user, "role_id": ref}
			}, foreign.role, "", http.StatusNotFound},
		} {
			for _, ref := range []struct{ id, name string }{{w.foreign, " of the other organization"}, {unknownID, " unknown"}} {
				out = append(out, matrixProbe{name: w.name + ref.name, method: http.MethodPost, path: w.path, body: w.body(ref.id), want: w.refused})
			}
			if w.own != "" && (p.kind == "org" || p.kind == "other-org") {
				out = append(out, matrixProbe{name: w.name + " of the own organization", method: http.MethodPost, path: w.path, body: w.body(w.own), want: http.StatusCreated})
			}
		}
		return out
	}}

	// GraphQL BFF: one row per query field (GQL-04).
	rows["graphql-cis"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		show, hide := listed(p, cis...)
		return []matrixProbe{gql("cis", `{ cis(first: 100) { nodes { id name } } }`, show, append(hide, append(hiddenCIs(p), `"errors"`)...))}
	}}
	rows["graphql-ci"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for _, c := range append(slices.Clone(cis), object{unknownID, seen{}}) {
			q := fmt.Sprintf(`{ ci(id: %q) { id name } }`, c.id)
			if c.seen[p.kind] {
				out = append(out, gql("ci", q, []string{c.id, names[c.id]}, []string{`"errors"`}))
			} else {
				out = append(out, gql("ci", q, []string{`"ci":null`}, append(nameOf(c.id), `"errors"`)))
			}
		}
		return out
	}}
	rows["graphql-relationships"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for _, r := range roots {
			q := fmt.Sprintf(`{ relationships(ciId: %q) { id sourceCiId targetCiId sourceCi { id name } targetCi { id name } } }`, r.id)
			var show []string
			for i, rel := range rels {
				if rel.seen[p.kind] && slices.Contains(relEnds[i][:], r.id) {
					show = append(show, rel.id)
				}
			}
			if !r.seen[p.kind] {
				show = []string{`"relationships":[]`}
			}
			out = append(out, gql("relationships", q, show, append(hiddenCIs(p), `"errors"`)))
		}
		return out
	}}
	rows["graphql-current-user"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		other := f.OrgB
		if p.org == f.OrgB {
			other = f.OrgA
		}
		return []matrixProbe{gql("currentUser", `{ currentUser { id organizationId clientId } }`, []string{p.user, p.org}, []string{other, `"errors"`})}
	}}
	rows["graphql-topology"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		show, _ := listed(p, cis...)
		var shownNames []string
		for _, id := range show {
			shownNames = append(shownNames, nameOf(id)...)
		}
		hide := append(hiddenCIs(p), `"errors"`)
		// The complexity of a topology grows with the nodes it may hold
		// (F-333, WP-593): without a root 2,000, so each probe selects
		// one field per node or edge; from a root of depth 3 1,111, so
		// nodes and edges are probed apart.
		out := []matrixProbe{
			gql("topology", `{ topology { nodes { id } } }`, show, hide),
			gql("topology", `{ topology { nodes { name } } }`, shownNames, hide),
			gql("topology", `{ topology { edges { sourceCiId } } }`, nil, hide),
			gql("topology", `{ topology { edges { targetCiId } } }`, nil, hide),
		}
		for _, r := range roots {
			nodes := fmt.Sprintf(`{ topology(rootCiId: %q, depth: 3) { nodes { id name } } }`, r.id)
			edges := fmt.Sprintf(`{ topology(rootCiId: %q, depth: 3) { edges { sourceCiId targetCiId } } }`, r.id)
			if r.seen[p.kind] {
				out = append(out, gql("topology", nodes, []string{r.id}, hide), gql("topology", edges, nil, hide))
			} else {
				out = append(out, gql("topology", nodes, []string{`"message":"ci not found"`}, nameOf(r.id)),
					gql("topology", edges, []string{`"message":"ci not found"`}, nameOf(r.id)))
			}
		}
		return out
	}}
	rows["graphql-rack-layout"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i, r := range append(m.racks[:], object{unknownID, seen{}}) {
			q := fmt.Sprintf(`{ rackLayout(rackId: %q) { rack { id name } mounts { id ciId ci { id name } } } }`, r.id)
			if r.seen[p.kind] {
				out = append(out, gql("rackLayout", q, []string{r.id, m.mounts[i].id}, append(hiddenCIs(p), `"errors"`)))
			} else {
				out = append(out, gql("rackLayout", q, []string{`"rackLayout":null`}, []string{`"errors"`}))
			}
		}
		return out
	}}
	rows["graphql-search"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		show, hide := listed(p, cis...)
		return []matrixProbe{gql("searchEverything", `{ searchEverything(q: "chmci", first: 100) { total hits { entityId title } } }`, show, append(hide, `"errors"`))}
	}}
	rows["graphql-review-inbox"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		show, hide := listed(p, reviewsAll...)
		return []matrixProbe{gql("reviewInbox", `{ reviewInbox(first: 100) { id payload candidates { id name } } }`, show, append(append(hide, hiddenCIs(p)...), `"errors"`))}
	}}
	rows["graphql-jobs"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		show, hide := listed(p, jobsAll...)
		return []matrixProbe{gql("jobs", `{ jobs(first: 100) { id kind status } }`, show, append(hide, `"errors"`))}
	}}
	return rows
}

// foreignRef is the refusal of a write that names an object of the other
// organization or one outside the scope: for the other organization the
// reference is unknown (422, the composite foreign key with the
// organization), in organization A the row lies outside the scope (refused).
func (m *channelMatrix) foreignRef(p *channelPrincipal, refused int) int {
	if p.org == m.f.OrgB {
		return http.StatusUnprocessableEntity
	}
	return refused
}

// restricted reports whether p is restricted in client or site: such a
// principal writes no row outside its clients and sites, and none without
// client or site (E-09).
func restricted(p *channelPrincipal) bool { return !p.scope.Clients.All || !p.scope.Sites.All }

// name returns a unique name for a row of a write.
func (m *channelMatrix) name(prefix string) string {
	m.seq++
	return fmt.Sprintf("%s-%d", prefix, m.seq)
}

func (m *channelMatrix) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := m.f.Admin.Exec(m.bg, query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

// scalar runs a query on the maintenance pool (no RLS) and returns the
// single value as text.
func (m *channelMatrix) scalar(t *testing.T, query string, args ...any) string {
	t.Helper()
	var v string
	if err := m.f.Admin.QueryRow(m.bg, query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

// count returns a check that query counts one row if want, else none.
func (m *channelMatrix) count(want bool, query string, args ...any) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		n := m.scalar(t, `SELECT (`+query+`)::text`, args...)
		if exp := map[bool]string{true: "1", false: "0"}[want]; n != exp {
			t.Errorf("state after the request: %s with %v counts %s, want %s", query, args, n, exp)
		}
	}
}

// unchanged returns a check that a refused write left o as it was: query
// (with $1 = id, $2 = the written value) is true.
func (m *channelMatrix) unchanged(o object, p *channelPrincipal, query string, value string) func(t *testing.T) {
	return func(t *testing.T) {
		t.Helper()
		if !o.seen[p.kind] && m.scalar(t, `SELECT (`+query+`)::text`, o.id, value) != "true" {
			t.Errorf("the refused write of %s by %s changed it", o.id, p.kind)
		}
	}
}

// create sends a POST that must answer 201 or 202 and returns the new id.
func (m *channelMatrix) create(t *testing.T, p *channelPrincipal, path string, body any) string {
	t.Helper()
	code, resp := m.env.call(p, http.MethodPost, path, body)
	var out struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal([]byte(resp), &out)
	if (code != http.StatusCreated && code != http.StatusAccepted) || out.ID == "" {
		t.Fatalf("setup POST %s as %s: %d %s", path, p.kind, code, trim(resp))
	}
	return out.ID
}

// freshCI inserts an active server CI in a client and at a site (both may
// be empty) and returns its id.
func (m *channelMatrix) freshCI(t *testing.T, org, client, site string) string {
	t.Helper()
	return m.scalar(t, `INSERT INTO ci (organization_id, client_id, location_id, ci_type_id, name, status)
		VALUES ($1, NULLIF($2, '')::uuid, NULLIF($3, '')::uuid, $4, $5, 'active') RETURNING id::text`, org, client, site, m.serverType, m.name("chmtmp"))
}

// review inserts an open review item with one candidate CI, or without
// candidates and the client and site its producer set.
func (m *channelMatrix) review(t *testing.T, org, candidate, client, site string) string {
	t.Helper()
	candidates := "{}"
	if candidate != "" {
		candidates = "{" + candidate + "}"
	}
	return m.scalar(t, `INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids, client_id, site_id)
		VALUES ($1, 'probable_duplicate', $2, $3::uuid[], NULLIF($4, '')::uuid, NULLIF($5, '')::uuid) RETURNING id::text`,
		org, fmt.Sprintf(`{"name":%q}`, m.name("chm-review")), candidates, client, site)
}

// hookSnapshot is the scope snapshot of a webhook subscription of the
// matrix and the principals that see it. clients, sites and teams are
// comma-separated ("" = unrestricted in the dimension); clients "-" stores
// no snapshot, as for the subscriptions from before migration 000155.
type hookSnapshot struct {
	org, clients, sites, teams string
	seen                       seen
}

// matrixHook is a webhook subscription of the matrix with a failed delivery
// and a dead letter.
type matrixHook struct {
	object
	failed, dead string
}

// hookSnapshots are the webhook subscriptions of the matrix (WP-601, E-98):
// a subscription is visible where its snapshot lies in the caller's scope;
// a dimension without snapshot needs a caller unrestricted in it, a
// subscription without snapshot an org-wide one.
func (m *channelMatrix) hookSnapshots() []hookSnapshot {
	f := m.f
	return []hookSnapshot{
		{f.OrgA, f.Client1, "", "", seenBy("org", "other-user", "client")},
		{f.OrgA, "", m.site2, "", seenBy("org", "other-user", "site")},
		{f.OrgA, "", "", m.team, seenBy("org", "other-user", "team")},
		{f.OrgA, f.Client1 + "," + f.Client2, m.site1, "", seenBy("org", "other-user", "multi")},
		{f.OrgA, "-", "", "", seenBy("org", "other-user")},
		{f.OrgB, "-", "", "", m.onlyB},
	}
}

// hook inserts a webhook subscription with the snapshot s, a failed
// delivery and a dead letter. Its URL is a loopback address the egress
// protection refuses at once, so a test delivery needs no network.
func (m *channelMatrix) hook(t *testing.T, s hookSnapshot) matrixHook {
	t.Helper()
	arr := func(v string) any {
		if v == "" {
			return nil
		}
		return strings.Split(v, ",")
	}
	clients, recorded := s.clients, s.clients != "-"
	if !recorded {
		clients = ""
	}
	h := matrixHook{object: object{seen: s.seen}}
	h.id = m.scalar(t, `INSERT INTO webhook_subscription (organization_id, name, url, secret, events, scope_recorded, scope_clients, scope_sites, scope_teams)
		VALUES ($1, $2, 'https://127.0.0.1/chm-hook', 's', '{ci.created}', $3, $4::uuid[], $5::uuid[], $6::uuid[]) RETURNING id::text`,
		s.org, m.name("chm-hook"), recorded, arr(clients), arr(s.sites), arr(s.teams))
	delivery := func(status string) string {
		return m.scalar(t, `INSERT INTO webhook_delivery (organization_id, subscription_id, event, payload, status, attempt, max_attempts)
			VALUES ($1, $2, 'ci.created', '{}', $3, 6, 6) RETURNING id::text`, s.org, h.id, status)
	}
	h.failed = delivery("failed")
	h.dead = m.scalar(t, `INSERT INTO webhook_dead_letter (delivery_id, organization_id, subscription_id, event, payload, attempts)
		VALUES ($1, $2, $3, 'ci.created', '{}', 6) RETURNING id::text`, delivery("dead"), s.org, h.id)
	return h
}

func (m *channelMatrix) note(t *testing.T, org, user string) string {
	t.Helper()
	return m.scalar(t, `INSERT INTO notification_inbox (organization_id, user_id, category, subject, body)
		VALUES ($1, $2, 'review', $3, 'x') RETURNING id::text`, org, user, m.name("chm-note"))
}

// job inserts a job of a kind without permission and revert. clients,
// sites and teams are the comma-separated snapshot ("" = unrestricted in
// the dimension); clients "-" stores no snapshot. A succeeded job carries
// params and result with the client-1 CI.
func (m *channelMatrix) job(t *testing.T, org, creator, status, clients, sites, teams string) string {
	t.Helper()
	arr := func(s string) any {
		if s == "" {
			return nil
		}
		return strings.Split(s, ",")
	}
	recorded := clients != "-"
	if !recorded {
		clients = ""
	}
	var result any
	params := "{}"
	if status == "succeeded" {
		params = fmt.Sprintf(`{"ids":[%q]}`, m.ci1.id)
		result = params
	}
	return m.scalar(t, `INSERT INTO job (organization_id, kind, status, params, result, created_by, scope_recorded, scope_clients, scope_sites, scope_teams, finished_at)
		VALUES ($1, 'chm.matrix', $2, $3::jsonb, $4::jsonb, NULLIF($5, '')::uuid, $6, $7::uuid[], $8::uuid[], $9::uuid[],
		        CASE WHEN $2 = 'succeeded' THEN now() END) RETURNING id::text`,
		org, status, params, result, creator, recorded, arr(clients), arr(sites), arr(teams))
}

func trim(s string) string {
	if len(s) > 300 {
		return s[:300] + "…"
	}
	return s
}

// TestClientReferencesCarryOrganization keeps the fix of the channel matrix
// (migration 000136): every foreign key from a tenant table to client
// includes the organization, so no row references a client of another
// organization.
func TestClientReferencesCarryOrganization(t *testing.T) {
	f := scopetest.Seed(t, "cb")
	rows, err := f.Admin.Query(context.Background(), `
		SELECT c.conrelid::regclass::text, c.conname FROM pg_constraint c
		WHERE c.contype = 'f' AND c.confrelid = 'client'::regclass
		  AND EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.conrelid AND a.attname = 'organization_id')
		  AND NOT EXISTS (SELECT 1 FROM pg_attribute a WHERE a.attrelid = c.conrelid AND a.attnum = ANY(c.conkey)
		                  AND a.attname = 'organization_id')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			t.Fatal(err)
		}
		t.Errorf("%s.%s references client without the organization", table, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}
