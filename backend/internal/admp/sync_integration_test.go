package admp_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

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
}

func newFakeADMP() *fakeADMP {
	return &fakeADMP{connections: map[string]admp.ConnectionData{}, services: map[string]admp.ServiceData{},
		ports: map[string][]admp.PortData{}, fetches: map[string]int{}}
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
		{AccountID: e.account1, Kind: "port", ExternalID: "a"}, {AccountID: e.account1, Kind: admp.KindService},
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

	// The productive source until WP-1073: not_implemented, cache as is.
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
