package admp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp/readonly"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The refresh of the ADMP cache (ADM-03, E-225, WP-929) on PostgreSQL
// against a fake source: dedupe per object and window, the rate of the
// periodic refresh, stale on failure, the open circuit breaker, the expiry
// with crypto-shredding and the event admp.connection.refreshed.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -run 'TestAdmpRefresh|TestAdmpCircuit|TestAdmpExpire' ./internal/admp/

// fakeADMP is the fake source of every account: objects by external id,
// a forced failure class, an open circuit breaker; it counts the fetches.
type fakeADMP struct {
	mu          sync.Mutex
	connections map[string]admp.ConnectionData
	services    map[string]admp.ServiceData
	ports       map[string][]admp.PortData
	failClass   string
	// connFailClass fails only the fetches of connections.
	connFailClass string
	open          bool
	fetches       map[string]int // account → fetches
	// inventory and portServices are the port inventory and the services
	// of a port (WP-1073); nil: the source has no such read
	// (not_implemented), as the sources of WP-929.
	inventory    []admp.InventoryPort
	portServices map[string][]string
	// portFailClass fails the services read of single ports.
	portFailClass map[string]string
	// sessionClass fails CheckSession of a source with an inventory.
	sessionClass string
	// sessions counts the login checks per account.
	sessions map[string]int
}

func newFakeADMP() *fakeADMP {
	return &fakeADMP{connections: map[string]admp.ConnectionData{}, services: map[string]admp.ServiceData{},
		ports: map[string][]admp.PortData{}, fetches: map[string]int{}, sessions: map[string]int{}}
}

// sourceOf is the source of one account.
type sourceOf struct {
	f       *fakeADMP
	account string
}

func (f *fakeADMP) For(_ context.Context, a *admp.Account) (admp.Source, error) {
	return sourceOf{f: f, account: a.ID}, nil
}

func (f *fakeADMP) CircuitOpen(*admp.Account) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.open
}

func (f *fakeADMP) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

func (f *fakeADMP) fetched(account string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.fetches[account]
}

func (s sourceOf) fetch() error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	s.f.fetches[s.account]++
	if s.f.failClass != "" {
		return &admp.SourceError{Class: s.f.failClass}
	}
	return nil
}

func (s sourceOf) FetchConnection(_ context.Context, id string) (*admp.ConnectionData, error) {
	if err := s.fetch(); err != nil {
		return nil, err
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if s.f.connFailClass != "" {
		return nil, &admp.SourceError{Class: s.f.connFailClass}
	}
	c, ok := s.f.connections[id]
	if !ok {
		return nil, &admp.SourceError{Class: readonly.ClassClient}
	}
	return &c, nil
}

func (s sourceOf) FetchService(_ context.Context, id string) (*admp.ServiceData, error) {
	if err := s.fetch(); err != nil {
		return nil, err
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	sv, ok := s.f.services[id]
	if !ok {
		return nil, &admp.SourceError{Class: readonly.ClassClient}
	}
	return &sv, nil
}

func (s sourceOf) FetchServicePorts(_ context.Context, id string) ([]admp.PortData, error) {
	if err := s.fetch(); err != nil {
		return nil, err
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	return s.f.ports[id], nil
}

func (s sourceOf) FetchInventory(context.Context) ([]admp.InventoryPort, error) {
	s.f.mu.Lock()
	inv := s.f.inventory
	s.f.mu.Unlock()
	if inv == nil {
		return nil, &admp.SourceError{Class: admp.ClassNotImplemented}
	}
	if err := s.fetch(); err != nil {
		return nil, err
	}
	return inv, nil
}

func (s sourceOf) FetchPortServices(_ context.Context, key string) ([]string, error) {
	s.f.mu.Lock()
	known := s.f.portServices != nil
	s.f.mu.Unlock()
	if !known {
		return nil, &admp.SourceError{Class: admp.ClassNotImplemented}
	}
	if err := s.fetch(); err != nil {
		return nil, err
	}
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if class := s.f.portFailClass[key]; class != "" {
		return nil, &admp.SourceError{Class: class}
	}
	return s.f.portServices[key], nil
}

func (s sourceOf) CheckSession(context.Context) error {
	s.f.mu.Lock()
	defer s.f.mu.Unlock()
	if s.f.inventory == nil {
		return &admp.SourceError{Class: admp.ClassNotImplemented}
	}
	s.f.sessions[s.account]++
	if s.f.sessionClass != "" {
		return &admp.SourceError{Class: s.f.sessionClass}
	}
	return nil
}

// syncSubmitter queues on a job service and runs a created job at once
// (the in-process path of WithJobs, synchronous for the test).
type syncSubmitter struct {
	svc  *jobs.Service
	sync *admp.Sync
	mu   sync.Mutex
	keys []string
}

func (s *syncSubmitter) Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	job, created, err := s.svc.Enqueue(ctx, orgID, kind, params, opts)
	if err != nil || !created {
		return job, created, err
	}
	s.mu.Lock()
	s.keys = append(s.keys, opts.IdempotencyKey)
	s.mu.Unlock()
	_, err = s.sync.RunRefreshJob(ctx, job)
	return job, true, err
}

// syncEnv is organization A with a credential, an account of client 1 and
// an organization-wide one, a CI of client 1 with an interface, and the
// refresh on the PostgreSQL repositories with the fake source.
type syncEnv struct {
	t                   *testing.T
	f                   *scopetest.Fixture
	cache               *admp.PGRepository
	accounts            *admp.PGAccountRepository
	vault               *pii.Vault
	fake                *fakeADMP
	sync                *admp.Sync
	submitter           *syncSubmitter
	now                 time.Time
	credential          string
	account1, accountOr string
	ci1, iface1         string
}

func newSyncEnv(t *testing.T) *syncEnv {
	t.Helper()
	f := scopetest.Seed(t, "e7")
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	e := &syncEnv{t: t, f: f, cache: admp.NewPGRepository(f.App), accounts: admp.NewPGAccountRepository(f.App),
		vault: pii.NewVault(f.App, enc), fake: newFakeADMP(), now: time.Now().UTC().Truncate(time.Microsecond)}
	e.credential = f.ID()
	e.exec(`INSERT INTO credential (id, organization_id, name, kind, ciphertext) VALUES ($1, $2, 'wp929-token', 'api_token', '\x00')`,
		e.credential, f.OrgA)
	e.account1 = e.account("wp929-c1", f.Client1, e.credential, 2)
	e.accountOr = e.account("wp929-org", "", e.credential, 2)
	e.ci1 = f.CI(t, f.OrgA, f.Client1, "wp929-ci-1")
	e.iface1 = e.scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, 'ge-0/0/9') RETURNING id::text`,
		f.OrgA, e.ci1)
	e.sync = e.newSync(time.Minute)
	return e
}

// newSync builds the refresh with the fake source and a synchronous job
// submitter; runTime is the run time of admp.refresh_linked.
func (e *syncEnv) newSync(runTime time.Duration) *admp.Sync {
	s := admp.NewSync(&admp.SyncOptions{Accounts: e.accounts, Cache: e.cache, Sources: e.fake, Pool: e.f.App,
		Organizations: func(context.Context) ([]string, error) { return []string{e.f.OrgA}, nil },
		LinkedRunTime: runTime, Now: func() time.Time { return e.now }})
	svc := jobs.NewService(jobs.NewMemoryRepository())
	svc.Register(jobs.Kind{Name: admp.RefreshJobKind, MaxAttempts: 3, Timeout: 2 * time.Minute})
	e.submitter = &syncSubmitter{svc: svc, sync: s}
	return s.WithSubmitter(e.submitter)
}

func (e *syncEnv) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.f.Admin.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e *syncEnv) scalar(sql string, args ...any) string {
	e.t.Helper()
	var s string
	if err := e.f.Admin.QueryRow(context.Background(), sql, args...).Scan(&s); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (e *syncEnv) org() context.Context { return e.f.OrgCtx(e.f.OrgA) }

func (e *syncEnv) account(name, clientID, credentialID string, rps float64) string {
	e.t.Helper()
	a := &admp.Account{OrganizationID: e.f.OrgA, Name: name, ClientID: clientID, BaseURL: "https://admp.example.com/api",
		CredentialID: credentialID, RateLimitRPS: rps, TimeoutMS: 10000, Enabled: true}
	if err := e.accounts.CreateAccount(e.org(), a); err != nil {
		e.t.Fatalf("create account %s: %v", name, err)
	}
	return a.ID
}

func (e *syncEnv) refresh(account, kind, external string) admp.RefreshResult {
	e.t.Helper()
	res, err := e.sync.Refresh(e.org(), e.f.OrgA, admp.RefreshParams{AccountID: account, Kind: kind, ExternalID: external})
	if err != nil {
		e.t.Fatalf("refresh %s %s: %v", kind, external, err)
	}
	return res
}

// connectionRow reads the stored state of a connection.
type connectionRow struct {
	Carrier, ErrorClass string
	FetchedAt           time.Time
	ExpiresAt           time.Time
	Stale               bool
}

func (e *syncEnv) connectionRow(account, external string) connectionRow {
	e.t.Helper()
	var r connectionRow
	var class *string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(carrier, ''), last_error_class, fetched_at, expires_at, stale
		FROM admp_connection WHERE account_id = $1 AND external_id = $2`, account, external).
		Scan(&r.Carrier, &class, &r.FetchedAt, &r.ExpiresAt, &r.Stale); err != nil {
		e.t.Fatalf("read connection %s: %v", external, err)
	}
	if class != nil {
		r.ErrorClass = *class
	}
	return r
}

// accountStatus reads last_ok_at and last_error_class of an account.
func (e *syncEnv) accountStatus(account string) (okAt *time.Time, class string) {
	e.t.Helper()
	var c *string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT last_ok_at, last_error_class FROM admp_account WHERE id = $1`, account).
		Scan(&okAt, &c); err != nil {
		e.t.Fatal(err)
	}
	if c != nil {
		class = *c
	}
	return okAt, class
}

// events returns the data of the admp.connection.refreshed events of a
// connection.
func (e *syncEnv) events(connectionID string) []map[string]any {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox WHERE organization_id = $1 AND type = $2
		AND entity_id = $3 ORDER BY sequence`, e.f.OrgA, events.ADMPConnectionRefreshed, connectionID)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			e.t.Fatal(err)
		}
		var d map[string]any
		if err := json.Unmarshal(raw, &d); err != nil {
			e.t.Fatal(err)
		}
		out = append(out, d)
	}
	return out
}

// linked seeds a cached service of account with a port on the interface
// and its connection, both expiring at expires.
func (e *syncEnv) linked(account, service, connection string, expires time.Time) {
	e.t.Helper()
	ctx := e.org()
	c := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: account, ExternalID: connection, ExpiresAt: expires,
		FetchedAt: expires.Add(-30 * 24 * time.Hour)}
	if err := e.cache.UpsertConnection(ctx, c); err != nil {
		e.t.Fatal(err)
	}
	s := &admp.Service{OrganizationID: e.f.OrgA, AccountID: account, ExternalID: service, ConnectionID: c.ID, Kind: "internet",
		ExpiresAt: expires, FetchedAt: expires.Add(-30 * 24 * time.Hour)}
	if err := e.cache.UpsertService(ctx, s); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.cache.ReplaceServicePorts(ctx, e.f.OrgA, s.ID, []admp.ServicePort{
		{PortKey: "port-" + service, NetworkInterfaceID: e.iface1, MatchedBy: admp.MatchedByManual}}); err != nil {
		e.t.Fatal(err)
	}
	e.fake.set(func() {
		e.fake.connections[connection] = admp.ConnectionData{ExternalID: connection, Carrier: "Carrier AG"}
		e.fake.services[service] = admp.ServiceData{ExternalID: service, ConnectionExternalID: connection, Kind: "internet"}
		e.fake.ports[service] = []admp.PortData{{PortKey: "port-" + service}}
	})
}

// TestAdmpRefreshDedupesPerObject: admp.refresh is queued once per object
// and window (IdempotencyKey admp.refresh:<account>:<kind>:<id>:<window>);
// a finished job does not block a later window, another object or account
// gets its own job.
func TestAdmpRefreshDedupesPerObject(t *testing.T) {
	e := newSyncEnv(t)
	e.fake.set(func() { e.fake.connections["wp929-line-1"] = admp.ConnectionData{Carrier: "Carrier AG"} })
	ctx := e.org()
	p := admp.RefreshParams{AccountID: e.account1, Kind: admp.KindConnection, ExternalID: "wp929-line-1"}
	minute := admp.MinuteWindow(e.now)
	first, created, err := e.sync.Submit(ctx, e.f.OrgA, p, minute)
	if err != nil || !created {
		t.Fatalf("first submit: created %v, %v", created, err)
	}
	again, created, err := e.sync.Submit(ctx, e.f.OrgA, p, minute)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("second submit in the minute: created %v, job %s (first %s), %v", created, again.ID, first.ID, err)
	}
	if n := e.fake.fetched(e.account1); n != 1 {
		t.Fatalf("fetches = %d, want 1", n)
	}
	if want := "admp.refresh:" + e.account1 + ":connection:wp929-line-1:" + minute; first.IdempotencyKey != want {
		t.Fatalf("key %q, want %q", first.IdempotencyKey, want)
	}
	// The next minute queues again: the finished job does not block it.
	if _, created, err = e.sync.Submit(ctx, e.f.OrgA, p, admp.MinuteWindow(e.now.Add(time.Minute))); err != nil || !created {
		t.Fatalf("next minute: created %v, %v", created, err)
	}
	// Another object and another account in the same minute.
	other := p
	other.ExternalID = "wp929-line-2"
	if _, created, err = e.sync.Submit(ctx, e.f.OrgA, other, minute); err != nil || !created {
		t.Fatalf("other object: created %v, %v", created, err)
	}
	otherAccount := p
	otherAccount.AccountID = e.accountOr
	if _, created, err = e.sync.Submit(ctx, e.f.OrgA, otherAccount, minute); err != nil || !created {
		t.Fatalf("other account: created %v, %v", created, err)
	}
	if n := e.fake.fetched(e.account1); n != 3 {
		t.Fatalf("fetches of account 1 = %d, want 3", n)
	}
	// Invalid parameters are refused before a job exists.
	for _, bad := range []admp.RefreshParams{{AccountID: "x", Kind: admp.KindConnection, ExternalID: "a"},
		{AccountID: e.account1, Kind: "nope", ExternalID: "a"}, {AccountID: e.account1, Kind: admp.KindService},
		{AccountID: e.account1, Kind: admp.KindPort},
		{AccountID: e.account1, Kind: admp.KindInterface, NetworkInterfaceID: "nope"}} {
		if _, _, err = e.sync.Submit(ctx, e.f.OrgA, bad, minute); err == nil {
			t.Errorf("submit %+v accepted", bad)
		}
	}
	// Without a job service no refresh is queued.
	bare := admp.NewSync(&admp.SyncOptions{Accounts: e.accounts, Cache: e.cache, Sources: e.fake})
	if _, _, err = bare.Submit(ctx, e.f.OrgA, p, minute); !errors.Is(err, admp.ErrNoRefreshJobs) {
		t.Fatalf("without jobs: %v", err)
	}
}

// TestAdmpRefreshLinkedRespectsRate: admp.refresh_linked refreshes only
// linked services and their connections expiring within seven days, per
// account at most rate_limit_rps × run time fetches; the rest follows in
// the next run. An account without credential is skipped.
func TestAdmpRefreshLinkedRespectsRate(t *testing.T) {
	e := newSyncEnv(t)
	e.sync = e.newSync(3 * time.Second) // 2/s × 3 s = 6 fetches per run
	soon := e.now.Add(48 * time.Hour)
	for i := range 5 {
		e.linked(e.account1, fmt.Sprintf("wp929-svc-%d", i), fmt.Sprintf("wp929-line-%d", i), soon.Add(time.Duration(i)*time.Minute))
	}
	// Not due: expiring in 20 days; not linked: no port link.
	e.linked(e.account1, "wp929-svc-late", "wp929-line-late", e.now.Add(20*24*time.Hour))
	unlinked := &admp.Service{OrganizationID: e.f.OrgA, AccountID: e.account1, ExternalID: "wp929-svc-unlinked", Kind: "x",
		ExpiresAt: soon}
	if err := e.cache.UpsertService(e.org(), unlinked); err != nil {
		t.Fatal(err)
	}
	noCredential := e.account("wp929-nocred", e.f.Client2, "", 2)

	run, err := e.sync.RefreshLinked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	budget := 6
	if n := e.fake.fetched(e.account1); n > budget || n < budget-1 {
		t.Fatalf("fetches in the first run = %d, want at most %d (rate_limit_rps × run time) and the budget used", n, budget)
	}
	if run.Skipped[noCredential] != admp.ClassCredentialMissing {
		t.Errorf("account without credential: skipped %q", run.Skipped[noCredential])
	}
	for _, key := range e.submitter.keys {
		if strings.Contains(key, "wp929-svc-late") || strings.Contains(key, "wp929-svc-unlinked") || strings.Contains(key, "wp929-line-late") {
			t.Errorf("refreshed what is not due or not linked: %s", key)
		}
	}
	firstFetches := e.fake.fetched(e.account1)
	// The next run takes the rest; refreshed entries now expire in 30 days.
	e.now = e.now.Add(15 * time.Minute)
	if _, err = e.sync.RefreshLinked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.fetched(e.account1) - firstFetches; n == 0 || n > budget {
		t.Fatalf("fetches in the second run = %d, want 1..%d", n, budget)
	}
	for e.fake.fetched(e.account1) < 15 { // 5 services × 2 + 5 connections
		e.now = e.now.Add(15 * time.Minute)
		before := e.fake.fetched(e.account1)
		if _, err = e.sync.RefreshLinked(context.Background()); err != nil {
			t.Fatal(err)
		}
		if e.fake.fetched(e.account1) == before {
			t.Fatalf("a run refreshed nothing although entries are due (%d fetches)", before)
		}
	}
	due, err := e.cache.ListDueLinked(e.org(), e.f.OrgA, e.account1, e.now.Add(7*24*time.Hour))
	if err != nil || len(due) != 0 {
		t.Fatalf("due after the runs: %+v, %v", due, err)
	}
	// The manual port link survived the refreshes.
	sv, err := e.cache.ServiceByExternal(e.org(), e.f.OrgA, e.account1, "wp929-svc-0")
	if err != nil || len(sv.Ports) != 1 || sv.Ports[0].NetworkInterfaceID != e.iface1 || sv.Ports[0].MatchedBy != admp.MatchedByManual {
		t.Fatalf("ports after the refresh: %+v, %v", sv, err)
	}

	// A slow account: 0.005/s × 300 s = 1.5 fetches per run, less than a
	// service costs. No minimum of one: a run fetches at most what the
	// budget allows; the service does not block its cheaper connection,
	// and the saved-up budget lets it through in a later run.
	e.sync = e.newSync(5 * time.Minute)
	slow := e.account1
	e.exec(`UPDATE admp_account SET rate_limit_rps = 0.005 WHERE id = $1`, slow)
	e.linked(slow, "wp929-svc-slow", "wp929-line-slow", e.now.Add(24*time.Hour))
	start := e.fake.fetched(slow)
	if _, err = e.sync.RefreshLinked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.fetched(slow) - start; n != 1 {
		t.Fatalf("slow account, first run: %d fetches, want 1 (the connection; the service costs 2 of 1.5)", n)
	}
	e.now = e.now.Add(15 * time.Minute)
	if _, err = e.sync.RefreshLinked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.fetched(slow) - start; n != 3 {
		t.Fatalf("slow account, second run: %d fetches in all, want 3 (the service with the saved-up budget)", n)
	}
	if due, err = e.cache.ListDueLinked(e.org(), e.f.OrgA, slow, e.now.Add(7*24*time.Hour)); err != nil || len(due) != 0 {
		t.Fatalf("slow account, due after two runs: %+v, %v", due, err)
	}
	// Nothing due: the budget is not saved up beyond a service.
	for range 3 {
		e.now = e.now.Add(15 * time.Minute)
		if _, err = e.sync.RefreshLinked(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	e.linked(slow, "wp929-svc-slow2", "wp929-line-slow2", e.now.Add(24*time.Hour))
	before := e.fake.fetched(slow)
	if _, err = e.sync.RefreshLinked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.fetched(slow) - before; n > 2 {
		t.Fatalf("slow account after idle runs: %d fetches in one run, want at most 2", n)
	}
}

// TestAdmpRefreshInterfaceKeepsAccountStatus: a refresh of an interface
// without a service of the account fetches nothing: with a class that
// forbids the fetch (no credential) the result is that class, without one
// the account keeps last_ok_at and last_error_class. A service whose
// connection fails to load carries the connection's class: transient, the
// job retries, and the account records it.
func TestAdmpRefreshInterfaceKeepsAccountStatus(t *testing.T) {
	e := newSyncEnv(t)
	ctx := e.org()
	noCredential := e.account("wp929-if-nocred", e.f.Client2, "", 2)
	res, err := e.sync.Refresh(ctx, e.f.OrgA, admp.RefreshParams{AccountID: noCredential, Kind: admp.KindInterface,
		NetworkInterfaceID: e.iface1})
	if err != nil || res.Result != admp.ClassCredentialMissing || res.Refreshed != 0 {
		t.Fatalf("interface without services, no credential: %+v, %v", res, err)
	}
	if okAt, class := e.accountStatus(noCredential); okAt != nil || class != admp.ClassCredentialMissing {
		t.Fatalf("account without credential: last_ok_at %v, class %q", okAt, class)
	}

	// An earlier failure stays at the account when the interface has no
	// service of it.
	e.fake.set(func() { e.fake.failClass = readonly.ClassTimeout })
	if r := e.refresh(e.account1, admp.KindConnection, "wp929-if-line"); r.Result != readonly.ClassTimeout {
		t.Fatalf("failing refresh: %+v", r)
	}
	e.fake.set(func() { e.fake.failClass = "" })
	res, err = e.sync.Refresh(ctx, e.f.OrgA, admp.RefreshParams{AccountID: e.account1, Kind: admp.KindInterface,
		NetworkInterfaceID: e.iface1})
	if err != nil || res.Result != "ok" || res.Refreshed+res.Stale != 0 {
		t.Fatalf("interface without services: %+v, %v", res, err)
	}
	if okAt, class := e.accountStatus(e.account1); okAt != nil || class != readonly.ClassTimeout {
		t.Fatalf("account after a refresh that fetched nothing: last_ok_at %v, class %q", okAt, class)
	}

	// A service whose uncached connection fails transiently: the class is
	// the result, the job fails the attempt, the account records it.
	e.fake.set(func() {
		e.fake.services["wp929-if-svc"] = admp.ServiceData{ExternalID: "wp929-if-svc", ConnectionExternalID: "wp929-if-conn", Kind: "internet"}
		e.fake.connFailClass = readonly.ClassServer
	})
	if r := e.refresh(e.account1, admp.KindService, "wp929-if-svc"); r.Result != readonly.ClassServer {
		t.Fatalf("service with a failing connection: %+v", r)
	}
	if okAt, class := e.accountStatus(e.account1); okAt != nil || class != readonly.ClassServer {
		t.Fatalf("account after the failing connection: last_ok_at %v, class %q", okAt, class)
	}
	params, _ := json.Marshal(admp.RefreshParams{AccountID: e.account1, Kind: admp.KindService, ExternalID: "wp929-if-svc"})
	if _, err = e.sync.RunRefreshJob(ctx, &jobs.Job{OrganizationID: e.f.OrgA, Params: params}); err == nil {
		t.Fatal("a transient class of the connection did not fail the attempt")
	}
	e.fake.set(func() {
		e.fake.connFailClass = ""
		e.fake.connections["wp929-if-conn"] = admp.ConnectionData{ExternalID: "wp929-if-conn", Carrier: "Carrier AG"}
	})
	if r := e.refresh(e.account1, admp.KindService, "wp929-if-svc"); r.Result != "ok" || r.Refreshed != 2 {
		t.Fatalf("service after the connection recovered: %+v", r)
	}
	if okAt, class := e.accountStatus(e.account1); okAt == nil || class != "" {
		t.Fatalf("account after the success: last_ok_at %v, class %q", okAt, class)
	}
}

// TestAdmpRefreshFailureKeepsCacheStale: a failed fetch keeps the entry
// with stale=true and fetched_at unchanged, the class at the object and at
// the account; a later success clears both. The productive source fetches
// nothing (not_implemented) and leaves the cache as it is.
func TestAdmpRefreshFailureKeepsCacheStale(t *testing.T) {
	e := newSyncEnv(t)
	e.fake.set(func() {
		e.fake.connections["wp929-line-s"] = admp.ConnectionData{Carrier: "Carrier AG", Properties: map[string]any{"mbit": 100}}
	})
	if res := e.refresh(e.account1, admp.KindConnection, "wp929-line-s"); res.Result != "ok" || res.Refreshed != 1 {
		t.Fatalf("first refresh: %+v", res)
	}
	before := e.connectionRow(e.account1, "wp929-line-s")
	okAt, class := e.accountStatus(e.account1)
	if okAt == nil || class != "" || before.Stale || !before.ExpiresAt.Equal(e.now.Add(30*24*time.Hour)) {
		t.Fatalf("after success: row %+v, account ok %v class %q", before, okAt, class)
	}

	e.now = e.now.Add(time.Hour)
	e.fake.set(func() { e.fake.failClass = readonly.ClassServer })
	res := e.refresh(e.account1, admp.KindConnection, "wp929-line-s")
	if res.Result != readonly.ClassServer || res.Stale != 1 {
		t.Fatalf("failed refresh: %+v", res)
	}
	after := e.connectionRow(e.account1, "wp929-line-s")
	if !after.Stale || !after.FetchedAt.Equal(before.FetchedAt) || after.ErrorClass != readonly.ClassServer || after.Carrier != "Carrier AG" {
		t.Fatalf("after failure: %+v, want the last state with stale and class server", after)
	}
	if _, class = e.accountStatus(e.account1); class != readonly.ClassServer {
		t.Fatalf("account class %q, want server", class)
	}
	// The job handler fails a transient class (the queue retries), and
	// ends any other with its class as result.
	params, _ := json.Marshal(admp.RefreshParams{AccountID: e.account1, Kind: admp.KindConnection, ExternalID: "wp929-line-s"})
	job := &jobs.Job{OrganizationID: e.f.OrgA, Kind: admp.RefreshJobKind, Params: params}
	if _, err := e.sync.RunRefreshJob(context.Background(), job); err == nil {
		t.Fatal("a transient class did not fail the attempt")
	}
	e.fake.set(func() { e.fake.failClass = readonly.ClassClient })
	if out, err := e.sync.RunRefreshJob(context.Background(), job); err != nil || out.(admp.RefreshResult).Result != readonly.ClassClient {
		t.Fatalf("non-transient: %+v, %v", out, err)
	}

	// Success again clears stale and the account's class.
	e.fake.set(func() { e.fake.failClass = "" })
	e.refresh(e.account1, admp.KindConnection, "wp929-line-s")
	if r := e.connectionRow(e.account1, "wp929-line-s"); r.Stale || r.ErrorClass != "" || !r.FetchedAt.Equal(e.now) {
		t.Fatalf("after recovery: %+v", r)
	}
	if okAt, class = e.accountStatus(e.account1); class != "" || okAt == nil || !okAt.Equal(e.now) {
		t.Fatalf("account after recovery: ok %v class %q", okAt, class)
	}
	// An account without credential: credential_missing, nothing fetched.
	fetches := e.fake.fetched(e.accountOr)
	e.exec(`UPDATE admp_account SET credential_id = NULL WHERE id = $1`, e.accountOr)
	if res = e.refresh(e.accountOr, admp.KindConnection, "wp929-line-s"); res.Result != admp.ClassCredentialMissing ||
		e.fake.fetched(e.accountOr) != fetches {
		t.Fatalf("account without credential: %+v", res)
	}

	// The productive source fetches no connection until WP-1075:
	// not_implemented, cache as is.
	productive := admp.NewSync(&admp.SyncOptions{Accounts: e.accounts, Cache: e.cache, Pool: e.f.App,
		Sources: admp.NewClientSources(nil, egress.Options{})})
	got, err := productive.Refresh(e.org(), e.f.OrgA, admp.RefreshParams{AccountID: e.account1, Kind: admp.KindConnection,
		ExternalID: "wp929-line-new"})
	if err != nil || got.Result != admp.ClassNotImplemented {
		t.Fatalf("productive source: %+v, %v", got, err)
	}
	if n := e.scalar(`SELECT count(*)::text FROM admp_connection WHERE external_id = 'wp929-line-new'`); n != "0" {
		t.Fatal("the productive source stored a connection")
	}

	// --no-db: the same on the memory repositories.
	memAccounts := admp.NewMemoryAccountRepository()
	memCache := admp.NewMemoryRepository(memAccounts)
	org := e.f.OrgA
	ctx := e.org()
	a := &admp.Account{OrganizationID: org, Name: "mem", BaseURL: "https://admp.example.com", CredentialID: e.credential,
		RateLimitRPS: 2, TimeoutMS: 10000, Enabled: true}
	if err = memAccounts.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	mem := admp.NewSync(&admp.SyncOptions{Accounts: memAccounts, Cache: memCache, Sources: e.fake, Now: func() time.Time { return e.now }})
	p := admp.RefreshParams{AccountID: a.ID, Kind: admp.KindConnection, ExternalID: "wp929-line-s"}
	if res, err = mem.Refresh(ctx, org, p); err != nil || res.Result != "ok" {
		t.Fatalf("memory refresh: %+v, %v", res, err)
	}
	e.fake.set(func() { e.fake.failClass = readonly.ClassTimeout })
	if res, err = mem.Refresh(ctx, org, p); err != nil || res.Result != readonly.ClassTimeout {
		t.Fatalf("memory failure: %+v, %v", res, err)
	}
	c, err := memCache.ConnectionByExternal(ctx, org, a.ID, "wp929-line-s")
	if err != nil || !c.Stale || c.LastErrorClass != readonly.ClassTimeout || !c.FetchedAt.Equal(e.now) {
		t.Fatalf("memory stale: %+v, %v", c, err)
	}
	if stored, _ := memAccounts.GetAccount(ctx, org, a.ID); stored.LastErrorClass == nil || *stored.LastErrorClass != readonly.ClassTimeout {
		t.Fatalf("memory account: %+v", stored)
	}
}

// TestAdmpCircuitOpenSkipsAccount: with the circuit breaker of the account
// open, a refresh ends without a fetch with class circuit_open (the entry
// stale), and admp.refresh_linked skips the account.
func TestAdmpCircuitOpenSkipsAccount(t *testing.T) {
	e := newSyncEnv(t)
	e.linked(e.account1, "wp929-svc-c", "wp929-line-c", e.now.Add(24*time.Hour))
	e.fake.set(func() { e.fake.open = true })
	res := e.refresh(e.account1, admp.KindConnection, "wp929-line-c")
	if res.Result != readonly.ClassCircuitOpen || e.fake.fetched(e.account1) != 0 {
		t.Fatalf("refresh while open: %+v, %d fetches", res, e.fake.fetched(e.account1))
	}
	if r := e.connectionRow(e.account1, "wp929-line-c"); !r.Stale || r.ErrorClass != readonly.ClassCircuitOpen {
		t.Fatalf("entry while open: %+v", r)
	}
	run, err := e.sync.RefreshLinked(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if run.Skipped[e.account1] != readonly.ClassCircuitOpen || run.Submitted[e.account1] != 0 || e.fake.fetched(e.account1) != 0 {
		t.Fatalf("refresh_linked while open: %+v, %d fetches", run, e.fake.fetched(e.account1))
	}
	e.fake.set(func() { e.fake.open = false })
	if run, err = e.sync.RefreshLinked(context.Background()); err != nil || run.Submitted[e.account1] == 0 {
		t.Fatalf("refresh_linked after the breaker closed: %+v, %v", run, err)
	}
	// The read-only client's breaker is what ClientSources reports: a
	// fresh client is closed.
	sources := admp.NewClientSources(nil, egress.Options{})
	acc, err := e.accounts.GetAccount(e.org(), e.f.OrgA, e.account1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = sources.For(e.org(), acc); err != nil || sources.CircuitOpen(acc) {
		t.Fatalf("fresh client: %v, open %v", err, sources.CircuitOpen(acc))
	}
}

// TestAdmpExpireShredsPII: admp.expire removes the entries past their TTL
// and with them the personal surrogates of their connections; the vault
// resolves them no more. Entries within their TTL stay.
func TestAdmpExpireShredsPII(t *testing.T) {
	e := newSyncEnv(t)
	ctx := e.org()
	var holder string
	past := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.account1, ExternalID: "wp929-expired",
		ExpiresAt: e.now.Add(-time.Hour)}
	if err := database.WithRequestTenantTx(ctx, e.f.App, e.f.OrgA, func(ctx context.Context) error {
		var err error
		if holder, err = e.vault.Create(ctx, e.f.OrgA, pii.KindPerson, pii.Data{"name": "Max Mustermann"}); err != nil {
			return err
		}
		past.HolderSubjectID = holder
		return e.cache.UpsertConnection(ctx, past)
	}); err != nil {
		t.Fatal(err)
	}
	pastService := &admp.Service{OrganizationID: e.f.OrgA, AccountID: e.account1, ExternalID: "wp929-svc-expired", Kind: "x",
		ConnectionID: past.ID, ExpiresAt: e.now.Add(-time.Hour)}
	if err := e.cache.UpsertService(ctx, pastService); err != nil {
		t.Fatal(err)
	}
	fresh := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.account1, ExternalID: "wp929-fresh", ExpiresAt: e.now.Add(time.Hour)}
	if err := e.cache.UpsertConnection(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	before := expiredCount(t, admp.KindConnection)
	gone, err := e.sync.Expire(context.Background())
	if err != nil || gone.Connections != 1 || gone.Services != 1 {
		t.Fatalf("expire = %+v, %v; want one connection and one service", gone, err)
	}
	if _, err = e.vault.Resolve(ctx, e.f.OrgA, holder); !errors.Is(err, pii.ErrNotFound) {
		t.Fatalf("the holder of the expired connection resolves: %v", err)
	}
	if n := e.scalar(`SELECT count(*)::text FROM pii_subject WHERE surrogate_id = $1`, holder); n != "0" {
		t.Fatal("the pii_subject row of the holder remains")
	}
	if _, err = e.cache.GetConnection(ctx, e.f.OrgA, fresh.ID); err != nil {
		t.Fatalf("the entry within its TTL was removed: %v", err)
	}
	if got := expiredCount(t, admp.KindConnection) - before; got != 1 {
		t.Fatalf("reticora_admp_expired_total{kind=connection} grew by %v, want 1", got)
	}
}

// TestAdmpRefreshEmitsEvent: a refresh that changes a connection writes
// admp.connection.refreshed (id, account_id, client_id, stale; no personal
// value) in its transaction; an unchanged refresh writes none; a
// connection turning stale writes one with stale true.
func TestAdmpRefreshEmitsEvent(t *testing.T) {
	e := newSyncEnv(t)
	e.fake.set(func() {
		e.fake.connections["wp929-line-e"] = admp.ConnectionData{Carrier: "Carrier AG", ContractID: "V-1",
			AddressText: "Industriestraße 1, 83278 Traunstein"}
	})
	e.refresh(e.account1, admp.KindConnection, "wp929-line-e")
	c, err := e.cache.ConnectionByExternal(e.org(), e.f.OrgA, e.account1, "wp929-line-e")
	if err != nil {
		t.Fatal(err)
	}
	got := e.events(c.ID)
	if len(got) != 1 || got[0]["id"] != c.ID || got[0]["account_id"] != e.account1 || got[0]["client_id"] != e.f.Client1 ||
		got[0]["stale"] != false {
		t.Fatalf("events after the first refresh: %v", got)
	}
	e.refresh(e.account1, admp.KindConnection, "wp929-line-e")
	if n := len(e.events(c.ID)); n != 1 {
		t.Fatalf("an unchanged refresh wrote an event (%d)", n)
	}
	e.fake.set(func() {
		d := e.fake.connections["wp929-line-e"]
		d.Carrier = "Other Carrier"
		e.fake.connections["wp929-line-e"] = d
	})
	e.refresh(e.account1, admp.KindConnection, "wp929-line-e")
	if n := len(e.events(c.ID)); n != 2 {
		t.Fatalf("events after a change = %d, want 2", n)
	}
	e.fake.set(func() { e.fake.failClass = readonly.ClassTimeout })
	e.refresh(e.account1, admp.KindConnection, "wp929-line-e")
	e.refresh(e.account1, admp.KindConnection, "wp929-line-e")
	got = e.events(c.ID)
	if len(got) != 3 || got[2]["stale"] != true {
		t.Fatalf("events after turning stale: %v, want a third with stale true", got)
	}
	for _, d := range got {
		raw, _ := json.Marshal(d)
		if strings.Contains(string(raw), "Industriestraße") || strings.Contains(string(raw), "Carrier") {
			t.Fatalf("the event carries connection data: %s", raw)
		}
	}
}

// expiredCount reads reticora_admp_expired_total{kind} through a fresh
// registry (the vectors are package state of admp).
func expiredCount(t *testing.T, kind string) float64 {
	t.Helper()
	reg := prometheus.NewRegistry()
	admp.RegisterMetrics(reg)
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != "reticora_admp_expired_total" {
			continue
		}
		for _, m := range mf.GetMetric() {
			for _, l := range m.GetLabel() {
				if l.GetName() == "kind" && l.GetValue() == kind {
					return m.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// --- The port inventory, the match and the services of a port (ADM-04,
// E-251, WP-1073) against the fake source.

// device inserts a CI of client with hostname and its interfaces and
// returns the CI and the interface ids by name.
func (e *syncEnv) device(clientID, hostname string, interfaces ...string) (string, map[string]string) {
	e.t.Helper()
	ci := e.f.CI(e.t, e.f.OrgA, clientID, "wp1073-"+hostname)
	e.exec(`UPDATE ci SET hostname = $2 WHERE id = $1`, ci, hostname)
	ifaces := map[string]string{}
	for _, name := range interfaces {
		ifaces[name] = e.scalar(`INSERT INTO network_interface (organization_id, ci_id, name) VALUES ($1, $2, $3) RETURNING id::text`,
			e.f.OrgA, ci, name)
	}
	return ci, ifaces
}

// setInventory gives the fake source an inventory (and with it the
// session and port reads).
func (e *syncEnv) setInventory(ports ...admp.InventoryPort) {
	e.fake.set(func() {
		e.fake.inventory = append([]admp.InventoryPort{}, ports...)
		if e.fake.portServices == nil {
			e.fake.portServices = map[string][]string{}
		}
	})
}

func invPort(key, node string, unit, port int64) admp.InventoryPort {
	return admp.InventoryPort{PortKey: key, NodeExternalID: "n-" + node, NodeName: node, Protocol: "KEYMILE_SSH", UnitIndex: unit,
		PortIndex: port}
}

// refreshKind runs admp.refresh of kind for the account.
func (e *syncEnv) refreshKind(account, kind, external string) admp.RefreshResult {
	e.t.Helper()
	res, err := e.sync.Refresh(e.org(), e.f.OrgA, admp.RefreshParams{AccountID: account, Kind: kind, ExternalID: external})
	if err != nil {
		e.t.Fatalf("refresh %s %s: %v", kind, external, err)
	}
	return res
}

// accessPortRow reads a port of the inventory: interface, CI, client and
// match.
type accessPortRow struct {
	Interface, CI, Client, MatchedBy string
}

func (e *syncEnv) accessPort(account, key string) (accessPortRow, bool) {
	e.t.Helper()
	var r accessPortRow
	err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(network_interface_id::text, ''), COALESCE(ci_id::text, ''),
		COALESCE(client_id::text, ''), matched_by FROM admp_access_port WHERE account_id = $1 AND external_id = $2`, account, key).
		Scan(&r.Interface, &r.CI, &r.Client, &r.MatchedBy)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, false
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return r, true
}

// TestAdmpInventoryMatchByHostname: a port matches the interface of the
// CI whose hostname equals the node name (case-insensitive) and whose
// name ends with <unit>/<port> after a non-digit; a longer index (12/1 for
// 2/1) does not match; the trigger sets CI and client; a port without such
// an interface stays none. The job result counts both.
func TestAdmpInventoryMatchByHostname(t *testing.T) {
	e := newSyncEnv(t)
	ci, ifs := e.device(e.f.Client1, "OLT-Traunstein-01", "ge-2/1", "ge-12/1", "Gi 2/2")
	e.setInventory(invPort("301", "olt-traunstein-01", 2, 1), invPort("302", "OLT-TRAUNSTEIN-01", 2, 2),
		invPort("303", "olt-traunstein-01", 2, 9))
	res := e.refreshKind(e.accountOr, admp.KindInventory, "")
	if res.Result != "ok" || res.MatchResult == nil || res.Matched != 2 || res.Unmatched != 1 {
		t.Fatalf("inventory: %+v", res)
	}
	for key, want := range map[string]accessPortRow{
		"301": {Interface: ifs["ge-2/1"], CI: ci, Client: e.f.Client1, MatchedBy: admp.MatchedByRule},
		"302": {Interface: ifs["Gi 2/2"], CI: ci, Client: e.f.Client1, MatchedBy: admp.MatchedByRule},
		"303": {MatchedBy: admp.MatchedByNone},
	} {
		if got, ok := e.accessPort(e.accountOr, key); !ok || got != want {
			t.Errorf("port %s: %+v, want %+v", key, got, want)
		}
	}
}

// TestAdmpInventoryMatchByHostnameLabel: without a CI of the full hostname
// the first label of the hostname matches the node name.
func TestAdmpInventoryMatchByHostnameLabel(t *testing.T) {
	e := newSyncEnv(t)
	ci, ifs := e.device(e.f.Client1, "dslam-siegsdorf.net.example", "port 1/7")
	e.setInventory(invPort("401", "DSLAM-Siegsdorf", 1, 7))
	if res := e.refreshKind(e.account1, admp.KindInventory, ""); res.Matched != 1 {
		t.Fatalf("inventory: %+v", res)
	}
	if got, _ := e.accessPort(e.account1, "401"); got.Interface != ifs["port 1/7"] || got.CI != ci || got.MatchedBy != admp.MatchedByRule {
		t.Errorf("port 401: %+v", got)
	}
}

// TestAdmpInventoryAmbiguousIsNone: two CIs of the node name, or two
// interfaces of the same ending on one CI, leave the port unmatched.
func TestAdmpInventoryAmbiguousIsNone(t *testing.T) {
	e := newSyncEnv(t)
	e.device(e.f.Client1, "dup-node", "ge-1/1")
	e.device(e.f.Client1, "DUP-NODE", "ge-1/1")
	e.device(e.f.Client1, "twin-iface", "eth1/1", "port1/1")
	e.setInventory(invPort("501", "dup-node", 1, 1), invPort("502", "twin-iface", 1, 1))
	if res := e.refreshKind(e.account1, admp.KindInventory, ""); res.Matched != 0 || res.Unmatched != 2 {
		t.Fatalf("inventory: %+v", res)
	}
	for _, key := range []string{"501", "502"} {
		if got, _ := e.accessPort(e.account1, key); got.MatchedBy != admp.MatchedByNone || got.Interface != "" || got.CI != "" {
			t.Errorf("port %s: %+v, want none", key, got)
		}
	}
}

// TestAdmpInventoryKeepsManualMatch: a manual match survives a new
// inventory and its match; a port no longer delivered is deleted.
func TestAdmpInventoryKeepsManualMatch(t *testing.T) {
	e := newSyncEnv(t)
	_, ifs := e.device(e.f.Client1, "olt-manual", "ge-1/1", "ge-9/9")
	e.setInventory(invPort("601", "olt-manual", 1, 1), invPort("602", "olt-manual", 3, 3))
	e.refreshKind(e.account1, admp.KindInventory, "")
	e.exec(`UPDATE admp_access_port SET network_interface_id = $2, matched_by = 'manual' WHERE account_id = $1 AND external_id = '601'`,
		e.account1, ifs["ge-9/9"])
	e.setInventory(invPort("601", "olt-manual", 1, 1))
	res := e.refreshKind(e.account1, admp.KindInventory, "")
	if res.Matched != 1 || res.Unmatched != 0 {
		t.Fatalf("second inventory: %+v", res)
	}
	if got, _ := e.accessPort(e.account1, "601"); got.Interface != ifs["ge-9/9"] || got.MatchedBy != admp.MatchedByManual {
		t.Errorf("manual port: %+v", got)
	}
	if _, ok := e.accessPort(e.account1, "602"); ok {
		t.Error("a port no longer delivered is still stored")
	}
}

// servicePortRows reads the rows of admp_service_port of a port key:
// service external id → interface/matched_by.
func (e *syncEnv) servicePortRows(account, key string) map[string]string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT s.external_id, COALESCE(p.network_interface_id::text, ''), p.matched_by,
		COALESCE(p.ci_id::text, '') FROM admp_service_port p JOIN admp_service s ON s.id = p.service_id
		WHERE s.account_id = $1 AND p.port_key = $2`, account, key)
	if err != nil {
		e.t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var ext, iface, by, ci string
		if err := rows.Scan(&ext, &iface, &by, &ci); err != nil {
			e.t.Fatal(err)
		}
		out[ext] = iface + "/" + by + "/" + ci
	}
	return out
}

// TestAdmpPortRefreshStoresServicesAndPorts: the refresh of a port reads
// its services and each service; admp_service holds kind and life cycle,
// the connection by its external id, admp_service_port the port with the
// interface and CI of the inventory. A connection the source cannot fetch
// yet (not_implemented) is a stale stub.
func TestAdmpPortRefreshStoresServicesAndPorts(t *testing.T) {
	e := newSyncEnv(t)
	ci, ifs := e.device(e.f.Client1, "olt-ports", "ge-2/1")
	e.setInventory(invPort("301", "olt-ports", 2, 1))
	e.refreshKind(e.accountOr, admp.KindInventory, "")
	e.fake.set(func() {
		e.fake.portServices["301"] = []string{"815", "816"}
		e.fake.services["815"] = admp.ServiceData{ExternalID: "815", ConnectionExternalID: "4711", Kind: "FTTH 1000",
			Properties: map[string]any{"template_id": 3, "lifecycle": map[string]any{"booked_at": "2023-11-14T22:13:20Z"}}}
		e.fake.services["816"] = admp.ServiceData{ExternalID: "816", ConnectionExternalID: "4711", Kind: "template:4"}
		e.fake.connections["4711"] = admp.ConnectionData{ExternalID: "4711", Carrier: "Netz AG"}
	})
	res := e.refreshKind(e.accountOr, admp.KindPort, "301")
	if res.Result != "ok" {
		t.Fatalf("port refresh: %+v", res)
	}
	want := map[string]string{"815": ifs["ge-2/1"] + "/rule/" + ci, "816": ifs["ge-2/1"] + "/rule/" + ci}
	if got := e.servicePortRows(e.accountOr, "301"); !reflect.DeepEqual(got, want) {
		t.Errorf("service ports %v, want %v", got, want)
	}
	sv, err := e.cache.ServiceByExternal(e.org(), e.f.OrgA, e.accountOr, "815")
	if err != nil || sv.Kind != "FTTH 1000" || sv.ConnectionID == "" {
		t.Fatalf("service 815: %+v %v", sv, err)
	}
	if lc, _ := sv.Properties["lifecycle"].(map[string]any); lc["booked_at"] != "2023-11-14T22:13:20Z" {
		t.Errorf("life cycle %v", sv.Properties)
	}
	if c := e.connectionRow(e.accountOr, "4711"); c.Carrier != "Netz AG" || c.Stale {
		t.Errorf("connection 4711: %+v", c)
	}
	// 4712 is no connection of the fake: with a source that cannot fetch
	// connections yet it becomes a stale stub.
	e.fake.set(func() {
		e.fake.connFailClass = admp.ClassNotImplemented
		e.fake.services["816"] = admp.ServiceData{ExternalID: "816", ConnectionExternalID: "4712", Kind: "template:4"}
	})
	e.exec(`DELETE FROM admp_service WHERE account_id = $1 AND external_id = '816'`, e.accountOr)
	if res = e.refreshKind(e.accountOr, admp.KindPort, "301"); res.Result != "ok" {
		t.Fatalf("port refresh with a stub: %+v", res)
	}
	if c := e.connectionRow(e.accountOr, "4712"); !c.Stale || c.ErrorClass != admp.ClassNotImplemented || c.Carrier != "" {
		t.Errorf("stub 4712: %+v", c)
	}
	if sv, err = e.cache.ServiceByExternal(e.org(), e.f.OrgA, e.accountOr, "816"); err != nil || sv.ConnectionID == "" {
		t.Errorf("service 816 without its stub: %+v %v", sv, err)
	}
}

// TestAdmpPortRefreshRemovesVanishedService: a service the port no longer
// names loses its row of the port; a manual row stays.
func TestAdmpPortRefreshRemovesVanishedService(t *testing.T) {
	e := newSyncEnv(t)
	_, ifs := e.device(e.f.Client1, "olt-vanish", "ge-2/1")
	e.setInventory(invPort("301", "olt-vanish", 2, 1))
	e.refreshKind(e.account1, admp.KindInventory, "")
	e.fake.set(func() {
		e.fake.portServices["301"] = []string{"815", "816", "817"}
		for _, id := range []string{"815", "816", "817"} {
			e.fake.services[id] = admp.ServiceData{ExternalID: id, Kind: "internet"}
		}
	})
	e.refreshKind(e.account1, admp.KindPort, "301")
	e.exec(`UPDATE admp_service_port p SET matched_by = 'manual' FROM admp_service s
		WHERE s.id = p.service_id AND s.external_id = '817' AND p.port_key = '301'`)
	e.fake.set(func() { e.fake.portServices["301"] = []string{"815"} })
	e.refreshKind(e.account1, admp.KindPort, "301")
	got := e.servicePortRows(e.account1, "301")
	if _, ok := got["816"]; ok || len(got) != 2 || got["817"] != ifs["ge-2/1"]+"/manual/"+strings.Split(got["815"], "/")[2] {
		t.Errorf("service ports after 816 vanished: %v", got)
	}
}

// TestAdmpInterfaceRefreshUsesInventory: the refresh of an interface
// without a matched port loads the inventory first, then refreshes the
// ports matched to it; within an hour an interface without port loads no
// inventory again.
func TestAdmpInterfaceRefreshUsesInventory(t *testing.T) {
	e := newSyncEnv(t)
	_, ifs := e.device(e.f.Client1, "olt-iface", "ge-2/1", "ge-3/1")
	e.setInventory(invPort("301", "olt-iface", 2, 1))
	e.fake.set(func() {
		e.fake.portServices["301"] = []string{"815"}
		e.fake.services["815"] = admp.ServiceData{ExternalID: "815", Kind: "internet"}
	})
	interfaceRefresh := func(iface string) admp.RefreshResult {
		res, err := e.sync.Refresh(e.org(), e.f.OrgA, admp.RefreshParams{AccountID: e.account1, Kind: admp.KindInterface,
			NetworkInterfaceID: iface})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	if res := interfaceRefresh(ifs["ge-2/1"]); res.Result != "ok" || res.Refreshed != 2 {
		t.Fatalf("interface refresh: %+v, want the inventory and the service", res)
	}
	if got := e.servicePortRows(e.account1, "301"); len(got) != 1 {
		t.Fatalf("service ports %v", got)
	}
	fetches := e.fake.fetched(e.account1)
	e.now = e.now.Add(30 * time.Minute)
	if res := interfaceRefresh(ifs["ge-3/1"]); res.Refreshed != 0 || e.fake.fetched(e.account1) != fetches {
		t.Fatalf("interface without port within the hour: %+v, %d fetches", res, e.fake.fetched(e.account1)-fetches)
	}
	e.now = e.now.Add(time.Hour)
	interfaceRefresh(ifs["ge-3/1"])
	if e.fake.fetched(e.account1) != fetches+1 {
		t.Fatalf("interface without port after an hour: %d fetches, want the inventory", e.fake.fetched(e.account1)-fetches)
	}
}

// TestAdmpUnmatchedPortsInJobResult: the job admp.refresh of kind
// inventory, queued and run like the job queue does, names matched,
// unmatched and the first 100 unmatched ports (port_key, node_name,
// unit_index, port_index) in its result, read through GET
// /api/v1/jobs/{id}. An unmatched port with a service appears in GET
// /api/v1/admp/connections/{id} with matched_by none and no interface.
func TestAdmpUnmatchedPortsInJobResult(t *testing.T) {
	e := newSyncEnv(t)
	e.device(e.f.Client1, "olt-job", "ge-1/1")
	ports := []admp.InventoryPort{invPort("1", "olt-job", 1, 1)}
	for i := range 150 {
		ports = append(ports, invPort(fmt.Sprintf("9%03d", i), "unknown-node", 4, int64(i)))
	}
	e.setInventory(ports...)
	svc := jobs.NewService(jobs.NewPGRepository(e.f.App))
	svc.Register(jobs.Kind{Name: admp.RefreshJobKind, MaxAttempts: 3, Timeout: 2 * time.Minute})
	mux := chi.NewRouter()
	jobs.NewAPIHandler(svc).RegisterRoutes(mux)
	admp.NewDataHandler(e.cache, e.cache, admp.NewAccountService(e.accounts, nil, egress.Options{}), e.sync).RegisterRoutes(mux)
	get := func(path string) *httptest.ResponseRecorder {
		t.Helper()
		ctx := tenant.WithTenant(e.org(), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, rec.Code, rec.Body.String())
		}
		return rec
	}
	// runJob queues admp.refresh, claims and runs it and stores its
	// result, the steps of jobs.Queue.Process.
	runJob := func(p admp.RefreshParams) string {
		t.Helper()
		job, _, err := svc.Enqueue(e.org(), e.f.OrgA, admp.RefreshJobKind, p, jobs.EnqueueOptions{})
		if err != nil {
			t.Fatal(err)
		}
		claimed, err := svc.ClaimJob(e.org(), e.f.OrgA, job.ID)
		if err != nil || claimed == nil {
			t.Fatalf("claim: %v %v", claimed, err)
		}
		out, err := e.sync.RunRefreshJob(e.org(), claimed)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = svc.Succeed(e.org(), e.f.OrgA, job.ID, out); err != nil {
			t.Fatal(err)
		}
		return job.ID
	}
	id := runJob(admp.RefreshParams{AccountID: e.account1, Kind: admp.KindInventory})
	rec := get("/api/v1/jobs/" + id)
	var job struct {
		Status string `json:"status"`
		Result struct {
			Result         string `json:"result"`
			Matched        int    `json:"matched"`
			Unmatched      int    `json:"unmatched"`
			UnmatchedPorts []struct {
				PortKey   string `json:"port_key"`
				NodeName  string `json:"node_name"`
				UnitIndex *int64 `json:"unit_index"`
				PortIndex *int64 `json:"port_index"`
			} `json:"unmatched_ports"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &job); err != nil {
		t.Fatal(err)
	}
	result := job.Result
	if job.Status != "succeeded" || result.Result != "ok" || result.Matched != 1 || result.Unmatched != 150 ||
		len(result.UnmatchedPorts) != 100 {
		t.Fatalf("job %s", rec.Body.String())
	}
	for i, p := range result.UnmatchedPorts {
		if p.PortKey != fmt.Sprintf("9%03d", i) || p.NodeName != "unknown-node" || p.UnitIndex == nil || *p.UnitIndex != 4 ||
			p.PortIndex == nil || *p.PortIndex != int64(i) {
			t.Fatalf("unmatched port %d: %+v", i, p)
		}
	}
	// A port refresh names no inventory members.
	id = runJob(admp.RefreshParams{AccountID: e.account1, Kind: admp.KindPort, ExternalID: "1"})
	if body := get("/api/v1/jobs/" + id).Body.String(); strings.Contains(body, "unmatched") {
		t.Errorf("port job %s", body)
	}
	// The unmatched port 9000 carries a service: its connection lists the
	// port with matched_by none and no interface.
	e.fake.set(func() {
		e.fake.portServices["9000"] = []string{"815"}
		e.fake.services["815"] = admp.ServiceData{ExternalID: "815", ConnectionExternalID: "4711", Kind: "internet"}
		e.fake.connections["4711"] = admp.ConnectionData{ExternalID: "4711", Carrier: "Netz AG"}
	})
	runJob(admp.RefreshParams{AccountID: e.account1, Kind: admp.KindPort, ExternalID: "9000"})
	c, err := e.cache.ConnectionByExternal(e.org(), e.f.OrgA, e.account1, "4711")
	if err != nil {
		t.Fatal(err)
	}
	var conn struct {
		Services []struct {
			ExternalID string `json:"external_id"`
			Ports      []struct {
				PortKey            string  `json:"port_key"`
				MatchedBy          string  `json:"matched_by"`
				NetworkInterfaceID *string `json:"network_interface_id"`
			} `json:"ports"`
		} `json:"services"`
	}
	rec = get("/api/v1/admp/connections/" + c.ID)
	if err = json.Unmarshal(rec.Body.Bytes(), &conn); err != nil {
		t.Fatal(err)
	}
	if len(conn.Services) != 1 || len(conn.Services[0].Ports) != 1 {
		t.Fatalf("connection %s", rec.Body.String())
	}
	if p := conn.Services[0].Ports[0]; p.PortKey != "9000" || p.MatchedBy != admp.MatchedByNone || p.NetworkInterfaceID != nil {
		t.Errorf("unmatched port in the connection: %+v", p)
	}
}

// TestAdmpPortLastingFailurePostponed: a port whose services fail with a
// lasting class (client: deleted in ADMP since the inventory) moves to its
// next turn and no longer leads the due ports; a transient class (timeout)
// keeps it due for the retry.
func TestAdmpPortLastingFailurePostponed(t *testing.T) {
	e := newSyncEnv(t)
	e.device(e.f.Client1, "olt-gone", "ge-2/1", "ge-2/2", "ge-2/3")
	e.setInventory(invPort("301", "olt-gone", 2, 1), invPort("302", "olt-gone", 2, 2), invPort("303", "olt-gone", 2, 3))
	e.refreshKind(e.account1, admp.KindInventory, "")
	e.fake.set(func() {
		e.fake.portFailClass = map[string]string{"301": readonly.ClassClient, "302": readonly.ClassTimeout}
	})
	if res := e.refreshKind(e.account1, admp.KindPort, "301"); res.Result != readonly.ClassClient {
		t.Fatalf("port 301: %+v", res)
	}
	if res := e.refreshKind(e.account1, admp.KindPort, "302"); res.Result != readonly.ClassTimeout {
		t.Fatalf("port 302: %+v", res)
	}
	due, err := e.cache.ListDuePorts(e.org(), e.f.OrgA, e.account1, e.now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{}
	for _, p := range due {
		keys = append(keys, p.ExternalID)
	}
	if !reflect.DeepEqual(keys, []string{"302", "303"}) {
		t.Errorf("due ports %v, want [302 303] (301 postponed)", keys)
	}
}

// TestAdmpEmptyInventoryKeepsFetchedAt: an empty inventory still records
// its time at the account: an interface refresh within the hour loads no
// new inventory, admp.refresh_linked within the day queues none.
func TestAdmpEmptyInventoryKeepsFetchedAt(t *testing.T) {
	e := newSyncEnv(t)
	_, ifs := e.device(e.f.Client1, "olt-empty", "ge-2/1")
	e.setInventory()
	if res := e.refreshKind(e.account1, admp.KindInventory, ""); res.Result != "ok" || res.Matched != 0 || res.Unmatched != 0 {
		t.Fatalf("empty inventory: %+v", res)
	}
	if at, err := e.cache.InventoryFetchedAt(e.org(), e.f.OrgA, e.account1); err != nil || !at.Equal(e.now) {
		t.Fatalf("inventory time %v (%v), want %v", at, err, e.now)
	}
	fetches := e.fake.fetched(e.account1)
	e.now = e.now.Add(30 * time.Minute)
	if _, err := e.sync.Refresh(e.org(), e.f.OrgA, admp.RefreshParams{AccountID: e.account1, Kind: admp.KindInterface,
		NetworkInterfaceID: ifs["ge-2/1"]}); err != nil {
		t.Fatal(err)
	}
	if n := e.fake.fetched(e.account1) - fetches; n != 0 {
		t.Errorf("interface refresh within the hour: %d fetches, want no inventory", n)
	}
	if _, err := e.sync.RefreshLinked(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, k := range e.submitter.keys {
		if strings.Contains(k, e.account1) && strings.Contains(k, ":inventory:") {
			t.Errorf("inventory queued within the day: %v", e.submitter.keys)
		}
	}
}

// TestAdmpMatchAccessPortsStatementBudget: the match of an inventory costs
// at most two statements besides the tenant frame, the same for 300 ports
// as for 2.
func TestAdmpMatchAccessPortsStatementBudget(t *testing.T) {
	e := newSyncEnv(t)
	e.device(e.f.Client1, "olt-budget", "ge-1/1", "ge-1/2")
	trace := scopetest.NewQueryTrace(t, e.f.App)
	cache := admp.NewPGRepository(trace.Pool)
	count := func(n int) int {
		t.Helper()
		ports := make([]admp.AccessPort, 0, n)
		for i := range n {
			ports = append(ports, admp.AccessPort{ExternalID: strconv.Itoa(i + 1), NodeExternalID: "n", NodeName: "olt-budget",
				UnitIndex: 1, PortIndex: int64(i + 1)})
		}
		if err := cache.ReplaceAccessPorts(e.org(), e.f.OrgA, e.account1, ports, e.now); err != nil {
			t.Fatal(err)
		}
		stmts := trace.Run(t, func() error {
			res, err := cache.MatchAccessPorts(e.org(), e.f.OrgA, e.account1)
			if err == nil && res.Matched != 2 {
				t.Errorf("%d ports: %d matched, want 2", n, res.Matched)
			}
			return err
		})
		k := 0
		for _, s := range stmts {
			if !frameStatement(s.SQL) {
				k++
			}
		}
		return k
	}
	count(2) // warms the connection
	small := count(2)
	large := count(300)
	if large > 2 || large != small {
		t.Errorf("statements: %d for 300 ports, %d for 2; want the same and at most 2", large, small)
	}
}

// TestAdmpAccessPortClientScope: a port matched to a CI of client 1 takes
// that client; a principal of client 2 does not see it, one of client 1
// does; a port of client 1's account without match carries client 1.
func TestAdmpAccessPortClientScope(t *testing.T) {
	e := newSyncEnv(t)
	_, ifs := e.device(e.f.Client1, "olt-scope", "ge-1/1")
	e.setInventory(invPort("701", "olt-scope", 1, 1))
	e.refreshKind(e.accountOr, admp.KindInventory, "")
	e.setInventory(invPort("702", "olt-scope", 5, 5))
	e.refreshKind(e.account1, admp.KindInventory, "")
	if got, _ := e.accessPort(e.accountOr, "701"); got.Client != e.f.Client1 {
		t.Errorf("matched port of the organization's account: client %q, want client 1", got.Client)
	}
	if got, _ := e.accessPort(e.account1, "702"); got.Client != e.f.Client1 || got.MatchedBy != admp.MatchedByNone {
		t.Errorf("unmatched port of client 1's account: %+v", got)
	}
	for client, want := range map[string]int{e.f.Client1: 1, e.f.Client2: 0} {
		ports, err := e.cache.AccessPortsByInterface(e.f.ClientCtx(client), e.f.OrgA, e.accountOr, ifs["ge-1/1"])
		if err != nil || len(ports) != want {
			t.Errorf("client %s: %d ports (%v), want %d", client, len(ports), err, want)
		}
		due, err := e.cache.ListDuePorts(e.f.ClientCtx(client), e.f.OrgA, e.accountOr, e.now.Add(time.Hour))
		if err != nil || len(due) != want {
			t.Errorf("client %s: %d due ports (%v), want %d", client, len(due), err, want)
		}
	}
}

// TestAdmpRefreshLinkedChecksSessionAndInventory: admp.refresh_linked
// reads the login state of an account first (a success sets last_ok_at, a
// failure skips the account), loads the inventory when it is older than a
// day and refreshes the matched ports that are due.
func TestAdmpRefreshLinkedChecksSessionAndInventory(t *testing.T) {
	e := newSyncEnv(t)
	e.sync = e.newSync(time.Minute)
	e.device(e.f.Client1, "olt-linked", "ge-2/1")
	e.setInventory(invPort("301", "olt-linked", 2, 1))
	e.fake.set(func() {
		e.fake.portServices["301"] = []string{"815"}
		e.fake.services["815"] = admp.ServiceData{ExternalID: "815", Kind: "internet"}
	})
	keys := func(part string) int {
		n := 0
		for _, k := range e.submitter.keys {
			if strings.Contains(k, e.account1) && strings.Contains(k, part) {
				n++
			}
		}
		return n
	}
	if _, err := e.sync.RefreshLinked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if okAt, class := e.accountStatus(e.account1); okAt == nil || class != "" || e.fake.sessions[e.account1] != 1 {
		t.Fatalf("after the session check: ok %v class %q sessions %d", okAt, class, e.fake.sessions[e.account1])
	}
	if keys(":inventory:") != 1 {
		t.Fatalf("inventory not queued: %v", e.submitter.keys)
	}
	e.now = e.now.Add(15 * time.Minute)
	if _, err := e.sync.RefreshLinked(context.Background()); err != nil {
		t.Fatal(err)
	}
	if keys(":inventory:") != 1 || keys(":port:301:") != 1 {
		t.Fatalf("second run: %v, want the due port and no new inventory", e.submitter.keys)
	}
	if got := e.servicePortRows(e.account1, "301"); len(got) != 1 {
		t.Fatalf("service ports after the linked refresh: %v", got)
	}
	e.fake.set(func() { e.fake.sessionClass = readonly.ClassAuth })
	e.now = e.now.Add(15 * time.Minute)
	run, err := e.sync.RefreshLinked(context.Background())
	if err != nil || run.Skipped[e.account1] != readonly.ClassAuth {
		t.Fatalf("failing session: %+v %v", run, err)
	}
	if _, class := e.accountStatus(e.account1); class != readonly.ClassAuth {
		t.Fatalf("account class %q, want auth", class)
	}
}
