package rls_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/middleware"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
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

// TestChannelMatrix covers TEN-09 and TST-02 (WP-208): every channel
// (REST list and detail, GraphQL, search, export, topology, impact,
// locations, reviews, webhooks, notifications, jobs, relationships, ingest)
// is read and, where it writes, written by principals with an organization,
// client, site, team, combined and foreign-organization scope against the
// complete PostgreSQL router. A CI is visible exactly where its client and
// site are in scope; a write outside the scope is refused; nothing of
// another organization is ever visible.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestChannelMatrix(t *testing.T) {
	f := scopetest.Seed(t, "c9")
	bg := context.Background()
	env := newChannelEnv(t, f)
	userA := f.AppUser(t, f.OrgA, "matrix-a")
	otherA := f.AppUser(t, f.OrgA, "matrix-a2")
	var userB string
	if err := f.Admin.QueryRow(bg, `INSERT INTO app_user (organization_id, email, display_name) VALUES ($1, 'matrix-b@example.org', 'matrix-b')
		RETURNING id::text`, f.OrgB).Scan(&userB); err != nil {
		t.Fatal(err)
	}
	// Channels that resolve the caller's grants from the database (search)
	// see the users as administrators; the scope of the matrix comes from
	// the principal.
	for org, user := range map[string]string{f.OrgA: userA, f.OrgB: userB} {
		if _, err := f.Admin.Exec(bg, `INSERT INTO role_assignment (organization_id, user_id, role_id)
			SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`, org, user); err != nil {
			t.Fatal(err)
		}
	}
	var team, serverType string
	if err := f.Admin.QueryRow(bg, `INSERT INTO team (organization_id, name) VALUES ($1, 'matrix team') RETURNING id::text`, f.OrgA).Scan(&team); err != nil {
		t.Fatal(err)
	}
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`).Scan(&serverType); err != nil {
		t.Fatal(err)
	}
	siteOf := func(room string) string {
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`, room).Scan(&site); err != nil {
			t.Fatal(err)
		}
		return site
	}
	room1, room2 := f.Room(t, f.Client1, "matrix-s1"), f.Room(t, f.Client2, "matrix-s2")
	site1, site2 := siteOf(room1), siteOf(room2)

	orgA := &channelPrincipal{kind: "org", org: f.OrgA, user: userA, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	principals := []*channelPrincipal{
		orgA,
		{kind: "client", org: f.OrgA, user: userA, scope: identity.Scope{Clients: only(f.Client1), Sites: all(), Teams: all()}},
		{kind: "site", org: f.OrgA, user: userA, scope: identity.Scope{Clients: all(), Sites: only(site2), Teams: all()}},
		{kind: "team", org: f.OrgA, user: userA, scope: identity.Scope{Clients: all(), Sites: all(), Teams: only(team)}},
		{kind: "multi", org: f.OrgA, user: userA, scope: identity.Scope{Clients: only(f.Client1, f.Client2), Sites: only(site1), Teams: all()}},
		{kind: "other-org", org: f.OrgB, user: userB, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}},
	}
	if len(principals) != len(rls.ScopeKinds) {
		t.Fatalf("matrix covers %d scope kinds, rls.ScopeKinds lists %d", len(principals), len(rls.ScopeKinds))
	}

	// The CIs are created through the API, so search and the outbox see them.
	create := func(p *channelPrincipal, name, client, site string) (int, string) {
		body := map[string]any{"ci_type_id": serverType, "name": name, "client_id": client}
		if site != "" {
			body["location_id"] = site
		}
		code, resp := env.call(p, http.MethodPost, "/api/v1/cis", body)
		var out struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(resp), &out)
		return code, out.ID
	}
	mustCreate := func(name, client, site string) string {
		code, id := create(orgA, name, client, site)
		if code != http.StatusCreated || id == "" {
			t.Fatalf("create %s: %d", name, code)
		}
		return id
	}
	ci1 := mustCreate("chm-one", f.Client1, site1)
	ci2 := mustCreate("chm-two", f.Client2, site2)
	other := &channelPrincipal{kind: "other-org", org: f.OrgB, user: userB, scope: identity.Scope{Clients: all(), Sites: all(), Teams: all()}}
	codeB, ciB := create(other, "chm-foreign", "", "")
	if codeB != http.StatusCreated {
		t.Fatalf("create the foreign CI: %d", codeB)
	}
	if code, body := env.call(orgA, http.MethodPost, "/api/v1/relationships",
		map[string]any{"source_ci_id": ci1, "target_ci_id": ci2, "rel_type": "depends_on"}); code != http.StatusCreated {
		t.Fatalf("relationship ci1 -> ci2: %d %s", code, body)
	}

	// Per-channel data that is not a CI: a review about ci2, a webhook, a
	// notification for userA and one for another user, and a job.
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	exec(`INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids) VALUES ($1, 'probable_duplicate', $2, ARRAY[$3::uuid])`,
		f.OrgA, fmt.Sprintf(`{"name":"chm-review","ci_id":%q}`, ci2), ci2)
	exec(`INSERT INTO notification_inbox (organization_id, user_id, category, subject, body) VALUES
		($1, $2, 'review', 'chm-note-own', 'x'), ($1, $3, 'review', 'chm-note-other', 'x')`, f.OrgA, userA, otherA)
	exec(`INSERT INTO webhook_subscription (organization_id, name, url, secret, events) VALUES ($1, 'chm-hook-a', 'https://a.example/h', 's', '{ci.created}'),
		($2, 'chm-hook-b', 'https://b.example/h', 's', '{ci.created}')`, f.OrgA, f.OrgB)

	// visible is the expected visibility of a CI for a principal: its
	// organization, client and site must be in scope (no team dimension on
	// CIs).
	type ciInfo struct{ id, org, client, site string }
	cis := []ciInfo{{ci1, f.OrgA, f.Client1, site1}, {ci2, f.OrgA, f.Client2, site2}, {ciB, f.OrgB, "", ""}}
	inSet := func(s identity.ScopeSet, v string) bool {
		if s.All {
			return true
		}
		for _, id := range s.IDs {
			if id == v {
				return true
			}
		}
		return false
	}
	visible := func(p *channelPrincipal, c ciInfo) bool {
		if c.org != p.org {
			return false
		}
		return (c.client == "" || inSet(p.scope.Clients, c.client)) && (c.site == "" || inSet(p.scope.Sites, c.site))
	}

	// Read channels: each returns a body in which a visible CI's id must
	// appear and an invisible one's must not.
	reads := map[string]func(p *channelPrincipal) (int, string){
		"rest": func(p *channelPrincipal) (int, string) {
			return env.call(p, http.MethodGet, "/api/v1/cis?limit=200", nil)
		},
		"graphql": func(p *channelPrincipal) (int, string) {
			return env.call(p, http.MethodPost, "/bff/graphql", map[string]any{"query": "{ cis(first: 100) { nodes { id } } }"})
		},
		"search": func(p *channelPrincipal) (int, string) {
			return env.call(p, http.MethodGet, "/api/v1/search?q=chm", nil)
		},
		"export": func(p *channelPrincipal) (int, string) {
			return env.call(p, http.MethodGet, "/api/v1/export/cis?format=json", nil)
		},
	}
	for _, ch := range []string{"rest", "graphql", "search", "export"} {
		for _, p := range principals {
			code, body := reads[ch](p)
			if code != http.StatusOK {
				t.Errorf("%s as %s: %d %s", ch, p.kind, code, trim(body))
				continue
			}
			for _, c := range cis {
				if got, want := strings.Contains(body, c.id), visible(p, c); got != want {
					t.Errorf("%s as %s: CI %s visible %v, want %v", ch, p.kind, c.id, got, want)
				}
			}
		}
	}

	// Detail, topology and impact: a CI out of scope is not found, and the
	// graph of ci1 never reaches an invisible ci2 (IMP-07).
	for _, p := range principals {
		for _, c := range cis {
			code, _ := env.call(p, http.MethodGet, "/api/v1/cis/"+c.id, nil)
			if want := visible(p, c); (code == http.StatusOK) != want {
				t.Errorf("rest-detail as %s: CI %s answered %d, visible %v", p.kind, c.id, code, want)
			}
		}
		if !visible(p, cis[0]) {
			continue
		}
		for _, path := range []string{"/api/v1/topology?root_ci_id=" + ci1 + "&depth=3", "/api/v1/impact/" + ci2} {
			if strings.Contains(path, "impact") && !visible(p, cis[1]) {
				continue
			}
			code, body := env.call(p, http.MethodGet, path, nil)
			if code != http.StatusOK {
				t.Errorf("%s as %s: %d %s", path, p.kind, code, trim(body))
				continue
			}
			for _, c := range cis {
				if strings.Contains(body, c.id) && !visible(p, c) {
					t.Errorf("%s as %s leaks CI %s", path, p.kind, c.id)
				}
			}
		}
	}

	// Locations: a site is visible where its client and the site itself are
	// in scope.
	for _, p := range principals {
		code, body := env.call(p, http.MethodGet, "/api/v1/sites?limit=200", nil)
		if code != http.StatusOK {
			t.Errorf("sites as %s: %d", p.kind, code)
			continue
		}
		for site, client := range map[string]string{site1: f.Client1, site2: f.Client2} {
			want := p.org == f.OrgA && inSet(p.scope.Clients, client) && inSet(p.scope.Sites, site)
			if got := strings.Contains(body, site); got != want {
				t.Errorf("sites as %s: site %s visible %v, want %v", p.kind, site, got, want)
			}
		}
	}

	// Organization-level channels: reviews, webhooks and jobs never cross
	// the organization; the notification inbox is the caller's own.
	for _, p := range principals {
		for _, ch := range []struct{ path, marker string }{
			{"/api/v1/review-items", "chm-review"},
			{"/api/v1/webhooks", "chm-hook-a"},
		} {
			code, body := env.call(p, http.MethodGet, ch.path, nil)
			if code != http.StatusOK {
				t.Errorf("%s as %s: %d %s", ch.path, p.kind, code, trim(body))
				continue
			}
			if p.org == f.OrgB && strings.Contains(body, ch.marker) {
				t.Errorf("%s as %s shows organization A's %s", ch.path, p.kind, ch.marker)
			}
			if p.org == f.OrgB && ch.marker == "chm-hook-a" && !strings.Contains(body, "chm-hook-b") {
				t.Errorf("webhooks as %s: own subscription missing", p.kind)
			}
		}
		// A review about ci2 names ci2: only principals that see ci2 see it.
		if code, body := env.call(p, http.MethodGet, "/api/v1/review-items", nil); code == http.StatusOK {
			if strings.Contains(body, "chm-review") && !visible(p, cis[1]) {
				t.Errorf("review-items as %s shows a review about the invisible CI %s", p.kind, ci2)
			}
		}
		code, body := env.call(p, http.MethodGet, "/api/v1/jobs", nil)
		if code != http.StatusOK {
			t.Errorf("jobs as %s: %d %s", p.kind, code, trim(body))
		}
		code, body = env.call(p, http.MethodGet, "/api/v1/notifications", nil)
		if code != http.StatusOK {
			t.Errorf("notifications as %s: %d %s", p.kind, code, trim(body))
		}
		if strings.Contains(body, "chm-note-other") {
			t.Errorf("notifications as %s show another user's inbox", p.kind)
		}
		if own := strings.Contains(body, "chm-note-own"); own != (p.org == f.OrgA) {
			t.Errorf("notifications as %s: own inbox entry visible %v", p.kind, own)
		}
	}

	// Writes: a principal changes, deletes and links only CIs in its scope
	// and creates nothing outside it.
	for _, p := range principals {
		for _, c := range cis {
			code, _ := env.call(p, http.MethodPatch, "/api/v1/cis/"+c.id, map[string]any{"model": "matrix-" + p.kind})
			if want := visible(p, c); (code == http.StatusOK) != want {
				t.Errorf("PATCH as %s: CI %s answered %d, writable %v", p.kind, c.id, code, want)
			}
		}
		for client, site := range map[string]string{f.Client1: site1, f.Client2: site2} {
			code, id := create(p, "chm-new-"+p.kind, client, site)
			want := p.org == f.OrgA && inSet(p.scope.Clients, client) && inSet(p.scope.Sites, site)
			if (code == http.StatusCreated) != want {
				t.Errorf("POST /cis as %s in client %s: %d, allowed %v", p.kind, client, code, want)
			}
			if id != "" {
				if delCode, _ := env.call(orgA, http.MethodDelete, "/api/v1/cis/"+id, nil); delCode >= 300 {
					t.Errorf("clean up %s: %d", id, delCode)
				}
			}
			code, body := env.call(p, http.MethodPost, "/api/v1/sites", map[string]any{"name": "chm-site-" + p.kind, "client_id": client})
			if allowed := p.org == f.OrgA && inSet(p.scope.Clients, client) && p.scope.Sites.All; code == http.StatusCreated && !allowed {
				t.Errorf("POST /sites as %s in client %s: %d, want refused: %s", p.kind, client, code, trim(body))
			}
		}
		// 409: an earlier principal created the same edge, the write itself
		// was admitted.
		code, _ := env.call(p, http.MethodPost, "/api/v1/relationships",
			map[string]any{"source_ci_id": ci1, "target_ci_id": ci2, "rel_type": "connected_to"})
		if want := visible(p, cis[0]) && visible(p, cis[1]); (code == http.StatusCreated || code == http.StatusConflict) != want {
			t.Errorf("POST /relationships as %s: %d, allowed %v", p.kind, code, want)
		}
		for _, c := range cis {
			if visible(p, c) {
				continue
			}
			if code, _ := env.call(p, http.MethodDelete, "/api/v1/cis/"+c.id, nil); code != http.StatusNotFound {
				t.Errorf("DELETE as %s: invisible CI %s answered %d, want 404", p.kind, c.id, code)
			}
		}
	}

	// Foreign references (TEN-05): organization B writes rows that point at
	// objects of organization A. Foreign keys are checked without row level
	// security, so every write path must refuse a reference it cannot see.
	var buildingA, vrfA, roleA, contactA, collectorA, clientB, roomB, rackB string
	scan := func(dst *string, query string, args ...any) {
		t.Helper()
		if err := f.Admin.QueryRow(bg, query, args...).Scan(dst); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	scan(&buildingA, `SELECT building_id::text FROM room WHERE id = $1`, room1)
	scan(&vrfA, `SELECT id::text FROM vrf WHERE organization_id = $1 AND is_default`, f.OrgA)
	scan(&roleA, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'org_admin'`, f.OrgA)
	scan(&contactA, `INSERT INTO contact (organization_id, client_id, display_name, email) VALUES ($1, $2, 'chm-contact', 'chm@example.org') RETURNING id::text`, f.OrgA, f.Client1)
	scan(&collectorA, `INSERT INTO collector (organization_id, client_id, name, status) VALUES ($1, $2, 'chm-collector', 'active') RETURNING id::text`, f.OrgA, f.Client1)
	scan(&clientB, `INSERT INTO client (organization_id, name, slug) VALUES ($1, 'chm client b', 'chm-client-b') RETURNING id::text`, f.OrgB)
	scan(&roomB, `WITH s AS (INSERT INTO site (organization_id, client_id, name) VALUES ($1, $2, 'chm-b') RETURNING id),
		b AS (INSERT INTO building (organization_id, site_id, name) SELECT $1, id, 'chm-b' FROM s RETURNING id)
		INSERT INTO room (organization_id, building_id, name) SELECT $1, id, 'chm-b' FROM b RETURNING id::text`, f.OrgB, clientB)
	scan(&rackB, `INSERT INTO rack (organization_id, room_id, name) VALUES ($1, $2, 'chm-rack-b') RETURNING id::text`, f.OrgB, roomB)
	for _, w := range []struct {
		name, path string
		body       map[string]any
	}{
		{"site in a foreign client", "/api/v1/sites", map[string]any{"name": "chm-x", "client_id": f.Client1}},
		{"building in a foreign site", "/api/v1/buildings", map[string]any{"name": "chm-x", "site_id": site1}},
		{"room in a foreign building", "/api/v1/rooms", map[string]any{"name": "chm-x", "building_id": buildingA}},
		{"rack in a foreign room", "/api/v1/racks", map[string]any{"name": "chm-x", "room_id": room1}},
		{"mount of a foreign CI", "/api/v1/racks/" + rackB + "/mounts", map[string]any{"ci_id": ci1, "position_u": 1}},
		{"CI in a foreign client", "/api/v1/cis", map[string]any{"ci_type_id": serverType, "name": "chm-x", "client_id": f.Client1}},
		{"CI at a foreign location", "/api/v1/cis", map[string]any{"ci_type_id": serverType, "name": "chm-x", "location_id": site1}},
		{"edge to a foreign CI", "/api/v1/relationships", map[string]any{"source_ci_id": ciB, "target_ci_id": ci1, "rel_type": "depends_on"}},
		{"contact in a foreign client", "/api/v1/contacts", map[string]any{"display_name": "chm-x", "client_id": f.Client1}},
		{"subnet in a foreign client", "/api/v1/subnets", map[string]any{"cidr": "10.209.0.0/24", "client_id": f.Client1}},
		{"subnet in a foreign VRF", "/api/v1/subnets", map[string]any{"cidr": "10.209.1.0/24", "vrf_id": vrfA}},
		{"contact link of a foreign contact", "/api/v1/cis/" + ciB + "/contacts", map[string]any{"contact_id": contactA, "relationship_type": "owner"}},
		{"scope of a foreign collector", "/api/v1/discovery-scopes", map[string]any{"collector_id": collectorA, "kind": "cidr", "target": "10.209.2.0/24", "name": "chm-x", "schedule": "24h"}},
		{"role of a foreign user", "/api/v1/role-assignments", map[string]any{"user_id": userA, "role_id": roleA}},
	} {
		code, body := env.call(other, http.MethodPost, w.path, w.body)
		if code >= 200 && code < 300 {
			t.Errorf("foreign reference, %s: %d %s", w.name, code, trim(body))
		}
	}
	// The same writes with the organization's own references succeed.
	if code, body := env.call(other, http.MethodPost, "/api/v1/sites", map[string]any{"name": "chm-own", "client_id": clientB}); code != http.StatusCreated {
		t.Errorf("own site: %d %s", code, trim(body))
	}

	// Ingest: a client-scoped principal cannot create the org-wide CI of an
	// unscoped record (RLS refuses it, TEN-05); the organization-wide
	// principal ingests it, and the other organization never sees it.
	rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "chm-ingested", IP: "192.0.2.209", Serial: "CHM-INGEST-1"},
		time.Now().Add(-time.Minute))
	batch := plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}}
	if code, body := env.call(principals[1], http.MethodPost, "/api/v1/ingest/bulk", batch); code != http.StatusForbidden {
		t.Errorf("ingest of an org-wide record as client: %d %s, want 403", code, trim(body))
	}
	if code, body := env.call(orgA, http.MethodPost, "/api/v1/ingest/bulk", batch); code != http.StatusAccepted {
		t.Fatalf("ingest as org: %d %s", code, trim(body))
	}
	if _, body := env.call(other, http.MethodGet, "/api/v1/cis?limit=200", nil); strings.Contains(body, "chm-ingested") {
		t.Error("the ingested CI is visible to the other organization")
	}
	if _, body := env.call(orgA, http.MethodGet, "/api/v1/cis?limit=200", nil); !strings.Contains(body, "chm-ingested") {
		t.Error("the ingested CI is not visible to its organization")
	}
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
