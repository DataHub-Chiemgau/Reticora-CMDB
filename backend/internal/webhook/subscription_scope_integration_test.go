package webhook_test

// Webhook subscriptions in the scope of their creator against PostgreSQL
// (WP-601, E-98 option B; TEN-09, TEN-06, WHK-01). A subscription stores the
// client, site and team scope of its creator; row level security
// (webhook_sub_isolation, webhook_sub_scope_select of migration 000155) and
// the handler confine it to principals whose scope covers that snapshot.
// Outside the scope, detail, delete, test, secret rotation, deliveries and
// retry answer 404 exactly like an unknown id, the list leaves the
// subscription out and the dead-letter list shows the entries of visible
// subscriptions only. A binding needs a service account the creator sees
// (422 otherwise). Subscriptions from before the migration carry no
// snapshot: only org-wide principals see and manage them, and they receive
// the events as before. The dispatcher reads every subscription org-wide: a
// subscription with a client-1 snapshot receives only client-1 events, also
// when it is bound to an org-wide service account, and claiming and retrying
// work for subscriptions with and without snapshot.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestSubscriptionScope' ./internal/webhook/

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
)

// errRollback ends a transaction of the WITH CHECK cases after a write the
// policies admitted.
var errRollback = errors.New("rollback")

// scopePrincipal is a caller of the webhook API with its tenant scope.
type scopePrincipal struct {
	name  string
	scope database.TenantScope
}

// TestSubscriptionScopeAgainstPostgres covers the management of
// subscriptions in the scope of their creator through the webhook API over
// the PostgreSQL repository and delivery store: stored snapshot, visibility
// per scope (list, detail, plain SELECT), identical 404 outside the scope
// for every route with a subscription id, the dead-letter list, the binding
// of a service account, subscriptions without snapshot, WITH CHECK and the
// catalog of migration 000155.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSubscriptionScopeAgainstPostgres(t *testing.T) {
	f := scopetest.Seed(t, "29")
	bg := context.Background()
	repo := webhook.NewPGRepository(f.App)
	var mu sync.Mutex
	received := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		received[strings.TrimPrefix(r.URL.Path, "/")]++
		mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	d := webhook.NewDispatcher(repo, srv.Client(), webhook.DispatcherOptions{Deliveries: webhook.NewPGDeliveryStore(f.App), PollInterval: -1})
	defer func() {
		ctx, cancel := context.WithTimeout(bg, 5*time.Second)
		defer cancel()
		_ = d.Shutdown(ctx)
	}()
	mux := chi.NewRouter()
	webhook.NewHandler(repo, d).WithEgress(egress.Options{AllowPrivate: true}).RegisterRoutes(mux)

	siteOf := func(room string) string {
		t.Helper()
		var site string
		if err := f.Admin.QueryRow(bg, `SELECT b.site_id::text FROM room r JOIN building b ON b.id = r.building_id WHERE r.id = $1`,
			room).Scan(&site); err != nil {
			t.Fatal(err)
		}
		return site
	}
	site1, site2 := siteOf(f.Room(t, f.Client1, "wp601-s1")), siteOf(f.Room(t, f.Client1, "wp601-s2"))
	creator, other, admin := f.AppUser(t, f.OrgA, "wp601-creator"), f.AppUser(t, f.OrgA, "wp601-other"), f.AppUser(t, f.OrgA, "wp601-admin")
	team := f.ID()
	as := func(name, org, user string, clients, sites, teams []string) scopePrincipal {
		scope := database.OrgWideScope(org, user)
		if clients != nil {
			scope.Clients = database.ScopeIDs(clients...)
		}
		if sites != nil {
			scope.Sites = database.ScopeIDs(sites...)
		}
		if teams != nil {
			scope.Teams = database.ScopeIDs(teams...)
		}
		return scopePrincipal{name: name, scope: scope}
	}
	orgWide := as("org-wide", f.OrgA, admin, nil, nil, nil)
	otherOrgWide := as("other org-wide user", f.OrgA, other, nil, nil, nil)
	client1 := as("client 1", f.OrgA, creator, []string{f.Client1}, nil, nil)
	otherClient1 := as("other user of client 1", f.OrgA, other, []string{f.Client1}, nil, nil)
	client2 := as("client 2", f.OrgA, creator, []string{f.Client2}, nil, nil)
	onSite1 := as("site 1", f.OrgA, creator, []string{f.Client1}, []string{site1}, nil)
	onSite2 := as("site 2", f.OrgA, other, []string{f.Client1}, []string{site2}, nil)
	inTeam := as("team", f.OrgA, creator, nil, nil, []string{team})
	orgB := as("organization B", f.OrgB, "", nil, nil, nil)

	// call sends one request as p with the tenant context the tenant
	// middleware attaches.
	call := func(p scopePrincipal, method, path string, body any) (int, string) {
		t.Helper()
		var raw []byte
		if body != nil {
			var err error
			if raw, err = json.Marshal(body); err != nil {
				t.Fatal(err)
			}
		}
		req := httptest.NewRequest(method, path, bytes.NewReader(raw))
		req.Header.Set("Content-Type", "application/json")
		ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: p.scope.OrgID, UserID: p.scope.UserID})
		ctx = database.ContextWithTenantScope(ctx, &p.scope)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req.WithContext(ctx))
		return w.Code, w.Body.String()
	}
	create := func(p scopePrincipal, name string, extra map[string]any) (int, string, string) {
		t.Helper()
		body := map[string]any{"name": "wp601-" + name, "url": srv.URL + "/" + name, "secret": "wp601-secret", "events": []string{"ci.created"}}
		maps.Copy(body, extra)
		code, resp := call(p, http.MethodPost, "/api/v1/webhooks", body)
		var out struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(resp), &out)
		return code, resp, out.ID
	}
	mustCreate := func(p scopePrincipal, name string) string {
		t.Helper()
		code, resp, id := create(p, name, nil)
		if code != http.StatusCreated || id == "" {
			t.Fatalf("create %s as %s: %d %s", name, p.name, code, resp)
		}
		return id
	}
	hookC1, hookC2 := mustCreate(client1, "c1"), mustCreate(client2, "c2")
	hookS1, hookS2 := mustCreate(onSite1, "s1"), mustCreate(onSite2, "s2")
	hookTeam, hookOrg, hookB := mustCreate(inTeam, "team"), mustCreate(orgWide, "org"), mustCreate(orgB, "b")
	// A subscription from before migration 000155: no snapshot.
	var legacy string
	if err := f.Admin.QueryRow(bg, `INSERT INTO webhook_subscription (organization_id, name, url, secret, events)
		VALUES ($1, 'wp601-legacy', $2, 'wp601-secret', '{ci.created}') RETURNING id::text`, f.OrgA, srv.URL+"/legacy").Scan(&legacy); err != nil {
		t.Fatal(err)
	}
	names := map[string]string{hookC1: "client-1 subscription", hookC2: "client-2 subscription", hookS1: "site-1 subscription",
		hookS2: "site-2 subscription", hookTeam: "team subscription", hookOrg: "org-wide subscription", hookB: "subscription of organization B",
		legacy: "subscription without snapshot"}

	// The snapshot of the creator is stored; the old row has none.
	for id, want := range map[string]string{
		hookC1:   "true|{" + f.Client1 + "}|NULL|NULL",
		hookS1:   "true|{" + f.Client1 + "}|{" + site1 + "}|NULL",
		hookTeam: "true|NULL|NULL|{" + team + "}",
		hookOrg:  "true|NULL|NULL|NULL",
		legacy:   "false|NULL|NULL|NULL",
	} {
		var stored string
		if err := f.Admin.QueryRow(bg, `SELECT scope_recorded::text || '|' || COALESCE(scope_clients::text, 'NULL') || '|' ||
			COALESCE(scope_sites::text, 'NULL') || '|' || COALESCE(scope_teams::text, 'NULL') FROM webhook_subscription WHERE id = $1`,
			id).Scan(&stored); err != nil || stored != want {
			t.Errorf("stored snapshot of the %s = %q (%v), want %q", names[id], stored, err, want)
		}
	}

	// Visibility per scope: list with total, detail and a plain SELECT under
	// the principal's scope (the policies alone).
	allIDs := []string{hookC1, hookC2, hookS1, hookS2, hookTeam, hookOrg, legacy, hookB}
	allA := allIDs[:7]
	for _, c := range []struct {
		p    scopePrincipal
		want []string
	}{
		{orgWide, allA},
		{otherOrgWide, allA},
		{client1, []string{hookC1, hookS1, hookS2}},
		{otherClient1, []string{hookC1, hookS1, hookS2}},
		{client2, []string{hookC2}},
		{onSite1, []string{hookS1}},
		{onSite2, []string{hookS2}},
		{inTeam, []string{hookTeam}},
		{orgB, []string{hookB}},
	} {
		code, body := call(c.p, http.MethodGet, "/api/v1/webhooks?limit=100", nil)
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal([]byte(body), &list); err != nil || code != http.StatusOK {
			t.Fatalf("list as %s: %d %s", c.p.name, code, body)
		}
		var listed []string
		for _, s := range list.Data {
			listed = append(listed, s.ID)
		}
		var selected []string
		if err := database.WithTenant(bg, f.App, &c.p.scope, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `SELECT id::text FROM webhook_subscription WHERE id = ANY($1::uuid[])`, allIDs)
			if err != nil {
				return err
			}
			selected, err = pgx.CollectRows(rows, pgx.RowTo[string])
			return err
		}); err != nil {
			t.Fatalf("select as %s: %v", c.p.name, err)
		}
		for _, id := range allIDs {
			want := slices.Contains(c.want, id)
			if got := slices.Contains(listed, id); got != want {
				t.Errorf("%s lists the %s: %v, want %v", c.p.name, names[id], got, want)
			}
			if got := slices.Contains(selected, id); got != want {
				t.Errorf("SELECT as %s shows the %s: %v, want %v", c.p.name, names[id], got, want)
			}
			wantCode := http.StatusNotFound
			if want {
				wantCode = http.StatusOK
			}
			if code, body := call(c.p, http.MethodGet, "/api/v1/webhooks/"+id, nil); code != wantCode {
				t.Errorf("GET the %s as %s: %d %s, want %d", names[id], c.p.name, code, body, wantCode)
			}
		}
		if list.Total != len(c.want) {
			t.Errorf("%s counts %d subscriptions, want %d", c.p.name, list.Total, len(c.want))
		}
	}

	// Deliveries and dead letters of the subscriptions.
	delivery := func(sub, status string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO webhook_delivery (organization_id, subscription_id, event, payload, status, attempt, max_attempts)
			VALUES ($1, $2, 'ci.created', '{}', $3, 6, 6) RETURNING id::text`, f.OrgA, sub, status).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	deadLetter := func(sub string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO webhook_dead_letter (delivery_id, organization_id, subscription_id, event, payload, attempts)
			VALUES ($1, $2, $3, 'ci.created', '{}', 6) RETURNING id::text`, delivery(sub, "dead"), f.OrgA, sub).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	failedC1, failedLegacy := delivery(hookC1, "failed"), delivery(legacy, "failed")
	deadC1, deadC2, deadS2, deadLegacy := deadLetter(hookC1), deadLetter(hookC2), deadLetter(hookS2), deadLetter(legacy)
	for _, c := range []struct {
		p    scopePrincipal
		want []string
	}{
		{orgWide, []string{deadC1, deadC2, deadS2, deadLegacy}},
		{client1, []string{deadC1, deadS2}},
		{client2, []string{deadC2}},
		{onSite2, []string{deadS2}},
		{onSite1, nil},
		{inTeam, nil},
	} {
		code, body := call(c.p, http.MethodGet, "/api/v1/webhooks/dead-letters?limit=100", nil)
		var list struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal([]byte(body), &list); err != nil || code != http.StatusOK {
			t.Fatalf("dead letters as %s: %d %s", c.p.name, code, body)
		}
		var got []string
		for _, dl := range list.Data {
			got = append(got, dl.ID)
		}
		slices.Sort(got)
		want := slices.Sorted(slices.Values(c.want))
		if !slices.Equal(got, want) || list.Total != len(want) {
			t.Errorf("dead letters as %s: %v (total %d), want %v", c.p.name, got, list.Total, want)
		}
	}

	// Outside the scope every route with a subscription id answers exactly
	// as for an unknown id, and nothing changes.
	secretOf := func(id string) string {
		t.Helper()
		var secret string
		if err := f.Admin.QueryRow(bg, `SELECT secret FROM webhook_subscription WHERE id = $1`, id).Scan(&secret); err != nil {
			t.Fatal(err)
		}
		return secret
	}
	unknown := f.ID()
	routes := []struct {
		method, path string
		body         any
	}{
		{http.MethodGet, "/api/v1/webhooks/%s", nil},
		{http.MethodPost, "/api/v1/webhooks/%s/test", nil},
		{http.MethodPost, "/api/v1/webhooks/%s/rotate-secret", map[string]string{"secret": "wp601-rotated-secret"}},
		{http.MethodGet, "/api/v1/webhooks/%s/deliveries", nil},
		{http.MethodPost, "/api/v1/webhooks/%s/deliveries/%s/retry", nil},
		{http.MethodDelete, "/api/v1/webhooks/%s", nil},
	}
	failedOf := map[string]string{hookC1: failedC1, legacy: failedLegacy}
	for _, c := range []struct {
		p  scopePrincipal
		id string
	}{
		{client2, hookC1}, {onSite1, hookC1}, {onSite2, hookS1}, {inTeam, hookC1}, {orgB, hookC1},
		{client1, legacy}, {client1, hookOrg}, {client1, hookTeam}, {inTeam, legacy},
	} {
		// The retry names a failed delivery of the subscription, or of the
		// client-1 subscription where it has none.
		failedDelivery, ok := failedOf[c.id]
		if !ok {
			failedDelivery = failedC1
		}
		for _, route := range routes {
			path := func(id string) string {
				if strings.Count(route.path, "%s") == 2 {
					return fmt.Sprintf(route.path, id, failedDelivery)
				}
				return fmt.Sprintf(route.path, id)
			}
			code, body := call(c.p, route.method, path(c.id), route.body)
			unknownCode, unknownBody := call(c.p, route.method, path(unknown), route.body)
			if code != http.StatusNotFound || code != unknownCode || withoutTrace(body) != withoutTrace(unknownBody) {
				t.Errorf("%s %s of the %s as %s: %d %s; an unknown id: %d %s; want the same 404",
					route.method, route.path, names[c.id], c.p.name, code, body, unknownCode, unknownBody)
			}
		}
	}
	var remaining int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM webhook_subscription WHERE id = ANY($1::uuid[])`, allIDs).Scan(&remaining); err != nil ||
		remaining != len(allIDs) {
		t.Errorf("subscriptions after the refused deletes: %d (%v), want %d", remaining, err, len(allIDs))
	}
	for _, id := range []string{hookC1, hookS1, legacy, hookOrg} {
		if secret := secretOf(id); secret != "wp601-secret" {
			t.Errorf("the refused rotation changed the secret of the %s", names[id])
		}
	}
	var failed int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM webhook_delivery WHERE id = ANY($1::uuid[]) AND status = 'failed'`,
		[]string{failedC1, failedLegacy}).Scan(&failed); err != nil || failed != 2 {
		t.Errorf("failed deliveries after the refused retries: %d (%v), want 2", failed, err)
	}
	mu.Lock()
	tested := maps.Clone(received)
	mu.Unlock()
	if len(tested) != 0 {
		t.Errorf("the refused tests reached the receiver: %v", tested)
	}

	// In its scope the creator, and another principal of the same scope,
	// manage the subscription.
	if code, body := call(otherClient1, http.MethodGet, "/api/v1/webhooks/"+hookC1+"/deliveries", nil); code != http.StatusOK ||
		!strings.Contains(body, failedC1) {
		t.Errorf("deliveries of the client-1 subscription as another user of client 1: %d %s", code, body)
	}
	if code, body := call(client1, http.MethodPost, "/api/v1/webhooks/"+hookC1+"/test", nil); code != http.StatusOK || !strings.Contains(body, `"success":true`) {
		t.Errorf("test of the client-1 subscription as client 1: %d %s", code, body)
	}
	if code, body := call(client1, http.MethodPost, "/api/v1/webhooks/"+hookC1+"/rotate-secret", map[string]string{"secret": "wp601-rotated-secret"}); code != http.StatusOK ||
		secretOf(hookC1) != "wp601-rotated-secret" {
		t.Errorf("rotation of the client-1 subscription as client 1: %d %s", code, body)
	}
	if code, body := call(client1, http.MethodPost, "/api/v1/webhooks/"+hookC1+"/deliveries/"+failedC1+"/retry", nil); code != http.StatusAccepted {
		t.Errorf("retry of a delivery of the client-1 subscription as client 1: %d %s", code, body)
	}
	if code, body := call(orgWide, http.MethodPost, "/api/v1/webhooks/"+legacy+"/deliveries/"+failedLegacy+"/retry", nil); code != http.StatusAccepted {
		t.Errorf("retry of a delivery of the subscription without snapshot as org-wide principal: %d %s", code, body)
	}
	if code, body := call(client1, http.MethodDelete, "/api/v1/webhooks/"+hookS2, nil); code != http.StatusNoContent {
		t.Errorf("client 1 deletes the site-2 subscription of its client: %d %s", code, body)
	}
	if code, body := call(orgWide, http.MethodDelete, "/api/v1/webhooks/"+legacy, nil); code != http.StatusNoContent {
		t.Errorf("the org-wide principal deletes the subscription without snapshot: %d %s", code, body)
	}

	// Binding a service account: one of the organization is bound, one the
	// creator does not see is 422 on service_account_id, nothing is stored.
	var saA, saB string
	for org, id := range map[string]*string{f.OrgA: &saA, f.OrgB: &saB} {
		if err := f.Admin.QueryRow(bg, `INSERT INTO service_account (organization_id, name) VALUES ($1, 'wp601-account') RETURNING id::text`,
			org).Scan(id); err != nil {
			t.Fatal(err)
		}
	}
	if code, body, id := create(client1, "bound", map[string]any{"service_account_id": saA}); code != http.StatusCreated ||
		!strings.Contains(body, `"service_account_id":"`+saA+`"`) || id == "" {
		t.Errorf("client 1 binds a service account of its organization: %d %s", code, body)
	}
	for name, sa := range map[string]string{"of organization B": saB, "unknown": f.ID(), "malformed": "not-a-uuid"} {
		code, body, _ := create(client1, "refused-binding", map[string]any{"service_account_id": sa})
		if code != http.StatusUnprocessableEntity || !strings.Contains(body, `"field":"service_account_id"`) {
			t.Errorf("binding a service account %s: %d %s, want 422 on service_account_id", name, code, body)
		}
	}
	var refused int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM webhook_subscription WHERE name = 'wp601-refused-binding'`).Scan(&refused); err != nil || refused != 0 {
		t.Errorf("refused bindings stored %d subscriptions (%v)", refused, err)
	}

	// WITH CHECK: a client-1 writer stores subscriptions of its own scope
	// only and cannot move its own out of it.
	write := func(p scopePrincipal, sql string, args ...any) error {
		t.Helper()
		werr := database.WithTenant(bg, f.App, &p.scope, func(ctx context.Context, tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, sql, args...); e != nil {
				return e
			}
			return errRollback
		})
		if errors.Is(werr, errRollback) {
			return nil
		}
		return werr
	}
	insert := `INSERT INTO webhook_subscription (organization_id, name, url, secret, scope_recorded, scope_clients)
		VALUES ($1, 'wp601-check', 'https://check.example.invalid/hook', 's', $2, $3::uuid[])`
	for _, c := range []struct {
		name     string
		p        scopePrincipal
		recorded bool
		clients  []string
		ok       bool
	}{
		{"own client", client1, true, []string{f.Client1}, true},
		{"no snapshot", client1, false, nil, false},
		{"org-wide in the client dimension", client1, true, nil, false},
		{"client 2", client1, true, []string{f.Client2}, false},
		{"clients 1 and 2", client1, true, []string{f.Client1, f.Client2}, false},
		{"site writer, sites org-wide", onSite1, true, []string{f.Client1}, false},
		{"team writer, teams org-wide", inTeam, true, nil, false},
		{"org-wide writer, no snapshot", orgWide, false, nil, true},
	} {
		ierr := write(c.p, insert, f.OrgA, c.recorded, c.clients)
		if c.ok && ierr != nil {
			t.Errorf("INSERT %s as %s refused: %v", c.name, c.p.name, ierr)
		}
		if !c.ok && !rlsRefused(ierr) {
			t.Errorf("INSERT %s as %s not refused by row level security: %v", c.name, c.p.name, ierr)
		}
	}
	for name, set := range map[string]string{
		"move to client 2":    `scope_clients = ARRAY['` + f.Client2 + `'::uuid]`,
		"drop the snapshot":   `scope_recorded = false, scope_clients = NULL`,
		"widen to org-wide":   `scope_clients = NULL`,
		"move to another org": `organization_id = '` + f.OrgB + `'`,
	} {
		if uerr := write(client1, `UPDATE webhook_subscription SET `+set+` WHERE id = $1`, hookC1); !rlsRefused(uerr) {
			t.Errorf("UPDATE %s as client 1 not refused by row level security: %v", name, uerr)
		}
	}
	if uerr := write(client1, `UPDATE webhook_subscription SET name = 'wp601-renamed' WHERE id = $1`, hookC1); uerr != nil {
		t.Errorf("UPDATE of the own subscription as client 1: %v", uerr)
	}
	var pgErr *pgconn.PgError
	if cerr := write(client1, `INSERT INTO webhook_subscription (organization_id, name, url, secret, scope_recorded, scope_clients)
		VALUES ($1, 'wp601-check', 'https://check.example.invalid/hook', 's', false, ARRAY[$2::uuid])`, f.OrgA, f.Client1); !errors.As(cerr, &pgErr) ||
		pgErr.Code != "23514" || pgErr.ConstraintName != "webhook_subscription_scope_check" {
		t.Errorf("a subscription without snapshot that names clients: %v, want webhook_subscription_scope_check", cerr)
	}

	// The catalog: ENABLE and FORCE, the organization and the scope GUCs in
	// USING and WITH CHECK of webhook_sub_isolation, the restrictive SELECT
	// policy.
	var rls, force bool
	if err := f.Admin.QueryRow(bg, `SELECT relrowsecurity, relforcerowsecurity FROM pg_class WHERE oid = 'webhook_subscription'::regclass`).Scan(&rls, &force); err != nil ||
		!rls || !force {
		t.Errorf("webhook_subscription: rls %v, force %v (%v)", rls, force, err)
	}
	rows, err := f.Admin.Query(bg, `SELECT policyname, permissive, cmd, COALESCE(qual, ''), COALESCE(with_check, '') FROM pg_policies WHERE tablename = 'webhook_subscription'`)
	if err != nil {
		t.Fatal(err)
	}
	policies := map[string][4]string{}
	for rows.Next() {
		var name, permissive, cmd, qual, check string
		if err = rows.Scan(&name, &permissive, &cmd, &qual, &check); err != nil {
			t.Fatal(err)
		}
		policies[name] = [4]string{permissive, cmd, qual, check}
	}
	rows.Close()
	if len(policies) != 2 {
		t.Errorf("webhook_subscription policies %v, want webhook_sub_isolation and webhook_sub_scope_select", policies)
	}
	iso, sel := policies["webhook_sub_isolation"], policies["webhook_sub_scope_select"]
	for _, guc := range []string{database.OrgGUC, database.ClientScopeGUC, database.SiteScopeGUC, database.TeamScopeGUC} {
		if iso[0] != "PERMISSIVE" || iso[1] != "ALL" || !strings.Contains(iso[2], "'"+guc+"'") || !strings.Contains(iso[3], "'"+guc+"'") {
			t.Errorf("webhook_sub_isolation does not check %s in USING and WITH CHECK: %v", guc, iso)
		}
		if guc != database.OrgGUC && (sel[0] != "RESTRICTIVE" || sel[1] != "SELECT" || !strings.Contains(sel[2], "'"+guc+"'")) {
			t.Errorf("webhook_sub_scope_select does not check %s: %v", guc, sel)
		}
	}
}

// TestSubscriptionScopeDelivery covers the delivery of E-98 through
// Dispatcher.Consume with the PostgreSQL repository and delivery store, the
// real object resolver and service account rights: the dispatcher reads
// the subscriptions org-wide; a subscription with a client-1 snapshot
// receives the event of a client-1 CI and none of a client-2 CI, a CI
// without client, an organizational event or an unknown CI; bound to an
// org-wide service account it receives the same, bound to a client-2
// account nothing (intersection). Subscriptions with an org-wide snapshot
// and without snapshot receive every event. A failed delivery is claimed
// and retried by the retry worker for subscriptions with and without
// snapshot.
func TestSubscriptionScopeDelivery(t *testing.T) {
	f := newBoundFixture(t, "29")
	bg := context.Background()
	repo := webhook.NewPGRepository(f.App)
	store := webhook.NewPGDeliveryStore(f.App)
	var mu sync.Mutex
	failing := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		fail := failing[strings.TrimPrefix(r.URL.Path, "/")]
		mu.Unlock()
		if fail {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	newDispatcher := func() *webhook.Dispatcher {
		return webhook.NewDispatcher(repo, srv.Client(), webhook.DispatcherOptions{Deliveries: store,
			Access: user.ServiceAccountAccess{Repo: user.NewPGServiceAccounts(f.App)}, Objects: webhook.NewPGObjectResolver(f.App), PollInterval: -1})
	}

	snapshotOf := func(ctx context.Context) *webhook.Scope {
		scope, _ := database.TenantScopeFromContext(ctx)
		return webhook.ScopeOf(&scope)
	}
	client1, orgWide := f.ClientCtx(f.Client1), f.OrgCtx(f.OrgA)
	sa2 := f.account("wp601-client2", f.Client2)
	nameOf := map[string]string{}
	for _, s := range []struct {
		name  string
		ctx   context.Context
		scope *webhook.Scope
		sa    string
	}{
		{"client1", client1, snapshotOf(client1), ""},
		{"bound-orgwide", client1, snapshotOf(client1), f.saAll},
		{"bound-client2", client1, snapshotOf(client1), sa2},
		{"orgwide", orgWide, snapshotOf(orgWide), ""},
		{"legacy", orgWide, nil, ""},
	} {
		sub := &webhook.Subscription{OrganizationID: f.OrgA, Name: "wp601-" + s.name, URL: srv.URL + "/" + s.name, Secret: "s",
			Events: []string{events.CIChanged, "ci_type.created"}, IsActive: true, ServiceAccountID: s.sa, Scope: s.scope}
		if err := repo.Create(s.ctx, sub); err != nil {
			t.Fatalf("create subscription %s: %v", s.name, err)
		}
		nameOf[sub.ID] = s.name
	}

	// deliveries returns the status of the deliveries of one event per
	// subscription, found by the marker of its payload.
	deliveries := func(marker string) map[string]string {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT subscription_id::text, status FROM webhook_delivery WHERE organization_id = $1 AND payload->>'marker' = $2`,
			f.OrgA, marker)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]string{}
		for rows.Next() {
			var sub, status string
			if err = rows.Scan(&sub, &status); err != nil {
				t.Fatal(err)
			}
			out[nameOf[sub]] = status
		}
		return out
	}
	consume := func(event string, payload map[string]any) string {
		t.Helper()
		marker := f.ID()
		payload["marker"] = marker
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		d := newDispatcher()
		// The acting principal's scope does not matter for the lookup.
		if err = d.Consume(f.ClientCtx(f.Client2), &events.Envelope{OrgID: f.OrgA, Type: event, Data: data}); err != nil {
			t.Fatalf("consume %s: %v", event, err)
		}
		ctx, cancel := context.WithTimeout(bg, 5*time.Second)
		defer cancel()
		if err = d.Shutdown(ctx); err != nil {
			t.Fatal(err)
		}
		return marker
	}

	c1 := f.ciIn(t, f.Client1, f.site1, "wp601-c1")
	c2 := f.ciIn(t, f.Client2, "", "wp601-c2")
	shared := f.ciIn(t, "", "", "wp601-shared")
	delivered := func(names ...string) map[string]string {
		out := map[string]string{"orgwide": webhook.StatusDelivered, "legacy": webhook.StatusDelivered}
		for _, name := range names {
			out[name] = webhook.StatusDelivered
		}
		return out
	}
	for _, c := range []struct {
		name    string
		event   string
		payload map[string]any
		want    map[string]string
	}{
		{"CI of client 1", events.CIChanged, map[string]any{"id": c1}, delivered("client1", "bound-orgwide")},
		{"CI of client 2", events.CIChanged, map[string]any{"id": c2}, delivered()},
		{"CI without client", events.CIChanged, map[string]any{"id": shared}, delivered()},
		{"organizational", "ci_type.created", map[string]any{"id": f.ID(), "key": "wp601"}, delivered()},
		{"unknown CI", events.CIChanged, map[string]any{"id": f.ID()}, delivered()},
	} {
		if got := deliveries(consume(c.event, c.payload)); !maps.Equal(got, c.want) {
			t.Errorf("%s: deliveries %v, want %v", c.name, got, c.want)
		}
	}

	// Claim and retry: the receivers of the client-1 subscription and of the
	// subscription without snapshot fail once; the retry worker claims the
	// due deliveries (system path) and delivers them.
	mu.Lock()
	failing["client1"], failing["legacy"] = true, true
	mu.Unlock()
	marker := consume(events.CIChanged, map[string]any{"id": c1})
	if got, want := deliveries(marker), map[string]string{"client1": webhook.StatusFailed, "legacy": webhook.StatusFailed,
		"bound-orgwide": webhook.StatusDelivered, "orgwide": webhook.StatusDelivered}; !maps.Equal(got, want) {
		t.Fatalf("first attempt: deliveries %v, want %v", got, want)
	}
	mu.Lock()
	failing = map[string]bool{}
	mu.Unlock()
	if _, err := f.Admin.Exec(bg, `UPDATE webhook_delivery SET next_retry_at = now() - interval '1 second'
		WHERE organization_id = $1 AND payload->>'marker' = $2 AND status = 'failed'`, f.OrgA, marker); err != nil {
		t.Fatal(err)
	}
	newDispatcher().ProcessDue(bg)
	if got := deliveries(marker); !maps.Equal(got, delivered("client1", "bound-orgwide")) {
		t.Errorf("after the retry worker: deliveries %v, want every one delivered", got)
	}
}

// withoutTrace returns a problem body without its trace_id, which differs
// per response.
func withoutTrace(body string) string {
	var problem map[string]any
	if json.Unmarshal([]byte(body), &problem) != nil {
		return body
	}
	delete(problem, "trace_id")
	out, _ := json.Marshal(problem)
	return string(out)
}

// rlsRefused reports whether err is a row level security violation.
func rlsRefused(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501" && strings.Contains(pgErr.Message, "row-level security")
}
