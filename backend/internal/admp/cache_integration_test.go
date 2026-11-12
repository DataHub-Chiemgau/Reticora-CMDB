package admp_test

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
)

// The ADMP cache (ADM-03, ADM-04, ADM-07; WP-928) on PostgreSQL: scope of
// the rows from the account or the ports, cascade and crypto-shredding,
// interface and CI deletion, no personal properties, and the memory store
// of --no-db with the same results.

// cacheEnv is the cache of organization A with an account per client and
// an organization-wide one, a CI of each client and an interface on ci1.
type cacheEnv struct {
	t                      *testing.T
	f                      *scopetest.Fixture
	repo                   *admp.PGRepository
	accounts               *admp.AccountService
	vault                  *pii.Vault
	account1, accountOrg   string
	ci1, ci2, ci1If, ci2If string
	expires                time.Time
}

func newCacheEnv(t *testing.T) *cacheEnv {
	t.Helper()
	f := scopetest.Seed(t, "e6")
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	accountRepo := admp.NewPGAccountRepository(f.App)
	e := &cacheEnv{t: t, f: f, repo: admp.NewPGRepository(f.App), vault: pii.NewVault(f.App, enc),
		accounts: admp.NewAccountService(accountRepo, nil, egress.Options{}),
		expires:  time.Now().Add(30 * 24 * time.Hour).UTC().Truncate(time.Microsecond)}
	e.account1 = e.account("wp928-c1", f.Client1)
	e.accountOrg = e.account("wp928-org", "")
	e.ci1 = f.CI(t, f.OrgA, f.Client1, "wp928-ci-1")
	e.ci2 = f.CI(t, f.OrgA, f.Client2, "wp928-ci-2")
	e.ci1If = e.iface(e.ci1, "ge-0/0/1")
	e.ci2If = e.iface(e.ci2, "ge-0/0/2")
	return e
}

func (e *cacheEnv) account(name, clientID string) string {
	e.t.Helper()
	a := &admp.Account{Name: name, ClientID: clientID, BaseURL: "https://admp.example.com/api", RateLimitRPS: 2, TimeoutMS: 10000, Enabled: true}
	if _, err := e.accounts.Create(e.f.OrgCtx(e.f.OrgA), e.f.OrgA, a); err != nil {
		e.t.Fatalf("create account %s: %v", name, err)
	}
	return a.ID
}

func (e *cacheEnv) iface(ciID, name string) string {
	e.t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `INSERT INTO network_interface (organization_id, ci_id, name)
		VALUES ($1, $2, $3) RETURNING id::text`, e.f.OrgA, ciID, name).Scan(&id); err != nil {
		e.t.Fatal(err)
	}
	return id
}

// inTx runs fn in one organization-wide transaction, as the refresh stores
// a personal value and its connection together.
func (e *cacheEnv) inTx(fn func(ctx context.Context) error) {
	e.t.Helper()
	if err := database.WithRequestTenantTx(e.org(), e.f.App, e.f.OrgA, fn); err != nil {
		e.t.Fatal(err)
	}
}

// subject stores a person in the vault (in the transaction of ctx).
func (e *cacheEnv) subject(ctx context.Context, name string) string {
	e.t.Helper()
	id, err := e.vault.Create(ctx, e.f.OrgA, pii.KindPerson, pii.Data{"name": name})
	if err != nil {
		e.t.Fatal(err)
	}
	return id
}

// org is the context of the system refresh (organization-wide).
func (e *cacheEnv) org() context.Context { return e.f.OrgCtx(e.f.OrgA) }

func (e *cacheEnv) connection(account, external string) *admp.Connection {
	e.t.Helper()
	c := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: account, ExternalID: external, Carrier: "Carrier AG",
		ContractID: "V-" + external, AddressText: "Industriestraße 1, 83278 Traunstein", ExpiresAt: e.expires,
		Properties: map[string]any{"bandwidth_mbit": 1000}}
	if err := e.repo.UpsertConnection(e.org(), c); err != nil {
		e.t.Fatalf("upsert connection %s: %v", external, err)
	}
	return c
}

func (e *cacheEnv) service(account, external, connectionID string) *admp.Service {
	e.t.Helper()
	s := &admp.Service{OrganizationID: e.f.OrgA, AccountID: account, ExternalID: external, ConnectionID: connectionID,
		Kind: "internet", ExpiresAt: e.expires}
	if err := e.repo.UpsertService(e.org(), s); err != nil {
		e.t.Fatalf("upsert service %s: %v", external, err)
	}
	return s
}

func (e *cacheEnv) ports(serviceID string, ports ...admp.ServicePort) []admp.ServicePort {
	e.t.Helper()
	out, err := e.repo.ReplaceServicePorts(e.org(), e.f.OrgA, serviceID, ports)
	if err != nil {
		e.t.Fatalf("replace ports: %v", err)
	}
	return out
}

func (e *cacheEnv) clientOf(table, id string) string {
	e.t.Helper()
	var client *string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT client_id::text FROM `+table+` WHERE id = $1`, id).Scan(&client); err != nil {
		e.t.Fatal(err)
	}
	if client == nil {
		return ""
	}
	return *client
}

// TestAdmpCacheClientScope: the cache rows of a client's account carry the
// client; a principal of the other client sees none of them, one of the
// client does; a port takes client and site of its CI.
func TestAdmpCacheClientScope(t *testing.T) {
	e := newCacheEnv(t)
	c := e.connection(e.account1, "wp928-line-1")
	s := e.service(e.account1, "wp928-svc-1", c.ID)
	ports := e.ports(s.ID, admp.ServicePort{PortKey: "port-1", NetworkInterfaceID: e.ci1If, MatchedBy: admp.MatchedByRule})
	if c.ClientID != e.f.Client1 || s.ClientID != e.f.Client1 || ports[0].ClientID != e.f.Client1 || ports[0].CIID != e.ci1 {
		t.Fatalf("scope: connection %q service %q port %+v, want client 1 and the CI of the interface", c.ClientID, s.ClientID, ports[0])
	}
	other := e.f.ClientCtx(e.f.Client2)
	if _, err := e.repo.GetConnection(other, e.f.OrgA, c.ID); !errors.Is(err, admp.ErrCacheNotFound) {
		t.Errorf("client 2 GetConnection = %v, want not found", err)
	}
	if list, err := e.repo.ListByInterface(other, e.f.OrgA, e.ci1If); err != nil || len(list) != 0 {
		t.Errorf("client 2 ListByInterface = %+v, %v, want none", list, err)
	}
	mine := e.f.ClientCtx(e.f.Client1)
	if got, err := e.repo.GetConnection(mine, e.f.OrgA, c.ID); err != nil || got.ContractID != "V-wp928-line-1" {
		t.Errorf("client 1 GetConnection = %+v, %v", got, err)
	}
	if list, err := e.repo.ListByInterface(mine, e.f.OrgA, e.ci1If); err != nil || len(list) != 1 || len(list[0].Ports) != 1 {
		t.Errorf("client 1 ListByInterface = %+v, %v, want the service with its port", list, err)
	}
	if list, err := e.repo.ListByCI(mine, e.f.OrgA, e.ci1); err != nil || len(list) != 1 || list[0].ID != c.ID {
		t.Errorf("client 1 ListByCI = %+v, %v, want the connection", list, err)
	}
	// A scoped writer cannot store a row of another client.
	foreign := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.account1, ExternalID: "wp928-line-x", ExpiresAt: e.expires}
	if err := e.repo.UpsertConnection(other, foreign); err == nil {
		t.Error("client 2 stored a connection of client 1's account")
	}
}

// TestAdmpCacheOrgAccountScopeFromPorts: a connection of the
// organization-wide account with ports only at client 1 is client 1's and
// invisible to client 2; with ports at two clients it is shared (NULL).
func TestAdmpCacheOrgAccountScopeFromPorts(t *testing.T) {
	e := newCacheEnv(t)
	c := e.connection(e.accountOrg, "wp928-line-org")
	s := e.service(e.accountOrg, "wp928-svc-org", c.ID)
	if c.ClientID != "" || s.ClientID != "" {
		t.Fatalf("without ports: connection %q service %q, want shared rows", c.ClientID, s.ClientID)
	}
	e.ports(s.ID, admp.ServicePort{PortKey: "port-1", NetworkInterfaceID: e.ci1If, MatchedBy: admp.MatchedByRule})
	if got := e.clientOf("admp_service", s.ID); got != e.f.Client1 {
		t.Errorf("service with a port at client 1: client %q, want client 1", got)
	}
	if got := e.clientOf("admp_connection", c.ID); got != e.f.Client1 {
		t.Errorf("connection with a port at client 1: client %q, want client 1", got)
	}
	if _, err := e.repo.GetConnection(e.f.ClientCtx(e.f.Client2), e.f.OrgA, c.ID); !errors.Is(err, admp.ErrCacheNotFound) {
		t.Errorf("client 2 sees the connection of client 1's ports: %v", err)
	}
	e.ports(s.ID,
		admp.ServicePort{PortKey: "port-1", NetworkInterfaceID: e.ci1If, MatchedBy: admp.MatchedByRule},
		admp.ServicePort{PortKey: "port-2", NetworkInterfaceID: e.ci2If, MatchedBy: admp.MatchedByRule})
	if got := e.clientOf("admp_connection", c.ID); got != "" {
		t.Errorf("connection with ports at two clients: client %q, want none", got)
	}
	if _, err := e.repo.GetConnection(e.f.ClientCtx(e.f.Client2), e.f.OrgA, c.ID); err != nil {
		t.Errorf("client 2 does not see the shared connection: %v", err)
	}
	// Moving the CI of client 2 to client 1 makes the connection client 1's again.
	if _, err := e.f.Admin.Exec(context.Background(), `UPDATE ci SET client_id = $2 WHERE id = $1`, e.ci2, e.f.Client1); err != nil {
		t.Fatal(err)
	}
	if got := e.clientOf("admp_connection", c.ID); got != e.f.Client1 {
		t.Errorf("after the CI moved: client %q, want client 1", got)
	}
}

// TestAdmpAccountDeleteShredsPII: deleting the account removes the cache
// rows and the holder and address subjects of its connections; the vault
// resolves them no more. DeleteExpired shreds the same way.
func TestAdmpAccountDeleteShredsPII(t *testing.T) {
	e := newCacheEnv(t)
	ctx := e.org()
	subject := e.subject
	var holder, address string
	c := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.account1, ExternalID: "wp928-private", ExpiresAt: e.expires}
	e.inTx(func(ctx context.Context) error {
		holder, address = subject(ctx, "Max Mustermann"), subject(ctx, "Hauptstraße 5")
		c.HolderSubjectID, c.AddressSubjectID = holder, address
		return e.repo.UpsertConnection(ctx, c)
	})
	s := e.service(e.account1, "wp928-svc-private", c.ID)
	e.ports(s.ID, admp.ServicePort{PortKey: "p", NetworkInterfaceID: e.ci1If, MatchedBy: admp.MatchedByRule})
	if err := e.accounts.Delete(ctx, e.f.OrgA, e.account1); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM admp_connection WHERE account_id = $1)
		+ (SELECT count(*) FROM admp_service WHERE account_id = $1)
		+ (SELECT count(*) FROM admp_service_port WHERE service_id = $2)
		+ (SELECT count(*) FROM pii_subject WHERE surrogate_id IN ($3, $4))`, e.account1, s.ID, holder, address).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d cache or subject rows remain after the account was deleted", rows)
	}
	for _, id := range []string{holder, address} {
		if _, err := e.vault.Resolve(ctx, e.f.OrgA, id); !errors.Is(err, pii.ErrNotFound) {
			t.Errorf("Resolve(%s) = %v, want ErrNotFound", id, err)
		}
	}

	// TTL expiry and a replaced holder shred too.
	var old, replaced string
	c = &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.accountOrg, ExternalID: "wp928-expiring",
		ExpiresAt: time.Now().Add(-time.Minute)}
	e.inTx(func(ctx context.Context) error {
		old = subject(ctx, "Erika Musterfrau")
		c.HolderSubjectID = old
		return e.repo.UpsertConnection(ctx, c)
	})
	// An upsert that keeps its subject passes in a later transaction.
	if err := e.repo.UpsertConnection(ctx, c); err != nil {
		t.Fatalf("upsert keeping the holder: %v", err)
	}
	e.inTx(func(ctx context.Context) error {
		replaced = subject(ctx, "Erika Neu")
		c.HolderSubjectID = replaced
		return e.repo.UpsertConnection(ctx, c)
	})
	if _, err := e.vault.Resolve(ctx, e.f.OrgA, old); !errors.Is(err, pii.ErrNotFound) {
		t.Errorf("the replaced holder resolves: %v", err)
	}
	gone, err := e.repo.DeleteExpired(ctx, e.f.OrgA, time.Now())
	if err != nil || gone.Connections != 1 {
		t.Fatalf("DeleteExpired = %+v, %v, want one connection", gone, err)
	}
	if _, err := e.vault.Resolve(ctx, e.f.OrgA, replaced); !errors.Is(err, pii.ErrNotFound) {
		t.Errorf("the holder of an expired connection resolves: %v", err)
	}
}

// TestAdmpInterfaceDeleteUnlinksPort: deleting the interface of a port sets
// network_interface_id NULL and matched_by none; deleting its CI sets
// ci_id NULL; the service stays.
func TestAdmpInterfaceDeleteUnlinksPort(t *testing.T) {
	e := newCacheEnv(t)
	s := e.service(e.account1, "wp928-svc-if", "")
	port := e.ports(s.ID, admp.ServicePort{PortKey: "port-1", NetworkInterfaceID: e.ci1If, MatchedBy: admp.MatchedByRule})[0]
	if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM network_interface WHERE id = $1`, e.ci1If); err != nil {
		t.Fatal(err)
	}
	var iface, ci *string
	var matched string
	read := func() {
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT network_interface_id::text, ci_id::text, matched_by
			FROM admp_service_port WHERE id = $1`, port.ID).Scan(&iface, &ci, &matched); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if iface != nil || matched != admp.MatchedByNone || ci == nil || *ci != e.ci1 {
		t.Errorf("after the interface was deleted: interface %v, ci %v, matched_by %s; want none, the CI kept, none", iface, ci, matched)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM ci WHERE id = $1`, e.ci1); err != nil {
		t.Fatal(err)
	}
	read()
	if ci != nil {
		t.Errorf("after the CI was deleted: ci %v, want none", *ci)
	}
	if list, err := e.repo.ListByInterface(e.org(), e.f.OrgA, e.ci1If); err != nil || len(list) != 0 {
		t.Errorf("ListByInterface of the deleted interface = %+v, %v", list, err)
	}
}

// TestAdmpUpsertRejectsPersonalProperties: a personal key in the
// properties, also nested, is ErrPersonalProperty on PostgreSQL and in
// memory; nothing is stored.
func TestAdmpUpsertRejectsPersonalProperties(t *testing.T) {
	check := func(t *testing.T, repo admp.Repository, ctx context.Context, orgID, account string) {
		for _, props := range []map[string]any{
			{"email": "holder@example.org"},
			{"contact": map[string]any{"phone": "+49 861 0"}},
			{"lines": []any{map[string]any{"full_name": "Max Mustermann"}}},
		} {
			c := &admp.Connection{OrganizationID: orgID, AccountID: account, ExternalID: "wp928-pii", Properties: props,
				ExpiresAt: time.Now().Add(time.Hour)}
			if err := repo.UpsertConnection(ctx, c); !errors.Is(err, admp.ErrPersonalProperty) {
				t.Errorf("connection with %v: %v, want ErrPersonalProperty", props, err)
			}
			// A service seals its personal values since WP-1074
			// (TestAdmpServicePersonalPropertySealed); the refusal holds for
			// connections until WP-1075.
		}
	}
	t.Run("memory", func(t *testing.T) {
		m := newMemoryCache(t)
		check(t, m.repo, m.ctx, m.org, m.accountOrg)
	})
	t.Run("postgres", func(t *testing.T) {
		e := newCacheEnv(t)
		check(t, e.repo, e.org(), e.f.OrgA, e.accountOrg)
		var n int
		if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM admp_connection WHERE organization_id = $1`, e.f.OrgA).Scan(&n); err != nil || n != 0 {
			t.Errorf("connections stored: %d, %v", n, err)
		}
	})
}

// memoryCache is the cache of --no-db with an organization-wide account.
type memoryCache struct {
	repo       *admp.MemoryRepository
	accounts   *admp.MemoryAccountRepository
	ctx        context.Context
	org        string
	accountOrg string
}

func newMemoryCache(t *testing.T) *memoryCache {
	t.Helper()
	const org = "6f0b6f0e-0000-4000-8000-000000928000"
	accounts := admp.NewMemoryAccountRepository()
	scope := database.OrgWideScope(org, "")
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	a := &admp.Account{OrganizationID: org, Name: "wp928-org", BaseURL: "https://admp.example.com", RateLimitRPS: 2,
		TimeoutMS: 10000, Enabled: true}
	if err := accounts.CreateAccount(ctx, a); err != nil {
		t.Fatal(err)
	}
	return &memoryCache{repo: admp.NewMemoryRepository(accounts), accounts: accounts, ctx: ctx, org: org, accountOrg: a.ID}
}

// TestAdmpCacheMemoryParity: the same sequence on PostgreSQL and in memory
// gives the same results: upsert by external id, the address rule, the
// ports of an interface and the connections of a CI, stale, expiry.
func TestAdmpCacheMemoryParity(t *testing.T) {
	type result struct {
		sameID, addressRefused, staleKept, cascaded bool
		byInterface, byCI                           int
		ports                                       []string
		expired                                     admp.Expired
	}
	run := func(t *testing.T, repo admp.Repository, ctx context.Context, org, account, ifaceID, ciID string,
		deleteAccount func(ctx context.Context, org, id string) error) result {
		var r result
		exp := time.Now().Add(time.Hour)
		c := &admp.Connection{OrganizationID: org, AccountID: account, ExternalID: "parity-line", Carrier: "A", ExpiresAt: exp}
		if err := repo.UpsertConnection(ctx, c); err != nil {
			t.Fatal(err)
		}
		again := &admp.Connection{OrganizationID: org, AccountID: account, ExternalID: "parity-line", Carrier: "B", ExpiresAt: exp}
		if err := repo.UpsertConnection(ctx, again); err != nil {
			t.Fatal(err)
		}
		r.sameID = again.ID == c.ID && again.Carrier == "B"
		bad := &admp.Connection{OrganizationID: org, AccountID: account, ExternalID: "parity-bad", AddressText: "x",
			AddressSubjectID: "6f0b6f0e-0000-4000-8000-000000928001", ExpiresAt: exp}
		r.addressRefused = errors.Is(repo.UpsertConnection(ctx, bad), admp.ErrInvalidCacheEntry)
		s := &admp.Service{OrganizationID: org, AccountID: account, ExternalID: "parity-svc", ConnectionID: c.ID, Kind: "internet", ExpiresAt: exp}
		if err := repo.UpsertService(ctx, s); err != nil {
			t.Fatal(err)
		}
		ports, err := repo.ReplaceServicePorts(ctx, org, s.ID, []admp.ServicePort{
			{PortKey: "b", NetworkInterfaceID: ifaceID, CIID: ciID, MatchedBy: admp.MatchedByRule},
			{PortKey: "a", MatchedBy: admp.MatchedByNone}})
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range ports {
			r.ports = append(r.ports, p.PortKey+"/"+p.MatchedBy)
		}
		if _, err = repo.ReplaceServicePorts(ctx, org, s.ID, []admp.ServicePort{{PortKey: "x"}, {PortKey: "x"}}); !errors.Is(err, admp.ErrInvalidCacheEntry) {
			t.Errorf("duplicate port keys: %v, want ErrInvalidCacheEntry", err)
		}
		list, err := repo.ListByInterface(ctx, org, ifaceID)
		if err != nil {
			t.Fatal(err)
		}
		r.byInterface = len(list)
		conns, err := repo.ListByCI(ctx, org, ciID)
		if err != nil {
			t.Fatal(err)
		}
		r.byCI = len(conns)
		if err = repo.MarkStale(ctx, org, admp.KindConnection, c.ID, "server"); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetConnection(ctx, org, c.ID)
		r.staleKept = err == nil && got.Stale && got.LastErrorClass == "server" && got.Carrier == "B"
		if r.expired, err = repo.DeleteExpired(ctx, org, exp.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		// Deleting the account removes its cache (the cascade).
		kept := &admp.Connection{OrganizationID: org, AccountID: account, ExternalID: "parity-kept", ExpiresAt: exp.Add(time.Hour)}
		if err = repo.UpsertConnection(ctx, kept); err != nil {
			t.Fatal(err)
		}
		if err = deleteAccount(ctx, org, account); err != nil {
			t.Fatal(err)
		}
		_, err = repo.GetConnection(ctx, org, kept.ID)
		r.cascaded = errors.Is(err, admp.ErrCacheNotFound)
		return r
	}
	m := newMemoryCache(t)
	const memIface, memCI = "6f0b6f0e-0000-4000-8000-000000928010", "6f0b6f0e-0000-4000-8000-000000928011"
	mem := run(t, m.repo, m.ctx, m.org, m.accountOrg, memIface, memCI, m.accounts.DeleteAccount)
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set; the PostgreSQL side of the parity is skipped")
	}
	e := newCacheEnv(t)
	pg := run(t, e.repo, e.org(), e.f.OrgA, e.accountOrg, e.ci1If, e.ci1, e.accounts.Delete)
	if !reflect.DeepEqual(mem, pg) {
		t.Errorf("memory %+v, PostgreSQL %+v", mem, pg)
	}
	want := result{sameID: true, addressRefused: true, staleKept: true, cascaded: true, byInterface: 1, byCI: 1,
		ports: []string{"b/rule", "a/none"}, expired: admp.Expired{Connections: 1, Services: 1}}
	if !reflect.DeepEqual(pg, want) {
		t.Errorf("PostgreSQL %+v, want %+v", pg, want)
	}
}

// TestAdmpConnectionRefusesForeignSubject: a subject that was not created
// with the connection (a contact's, created earlier) or that another
// connection holds is ErrInvalidCacheEntry; nothing is stored and the
// subject stays resolvable.
func TestAdmpConnectionRefusesForeignSubject(t *testing.T) {
	e := newCacheEnv(t)
	ctx := e.org()
	foreign := e.subject(ctx, "Kontakt Person")
	c := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.accountOrg, ExternalID: "wp928-foreign", HolderSubjectID: foreign,
		ExpiresAt: e.expires}
	if err := e.repo.UpsertConnection(ctx, c); !errors.Is(err, admp.ErrInvalidCacheEntry) {
		t.Errorf("connection with an earlier subject: %v, want ErrInvalidCacheEntry", err)
	}
	var shared string
	first := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.accountOrg, ExternalID: "wp928-first", ExpiresAt: e.expires}
	e.inTx(func(ctx context.Context) error {
		shared = e.subject(ctx, "Geteilte Person")
		first.HolderSubjectID = shared
		return e.repo.UpsertConnection(ctx, first)
	})
	second := &admp.Connection{OrganizationID: e.f.OrgA, AccountID: e.account1, ExternalID: "wp928-second", AddressSubjectID: shared,
		ExpiresAt: e.expires}
	if err := e.repo.UpsertConnection(ctx, second); !errors.Is(err, admp.ErrInvalidCacheEntry) {
		t.Errorf("connection with the subject of another connection: %v, want ErrInvalidCacheEntry", err)
	}
	var n int
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM admp_connection
		WHERE external_id IN ('wp928-foreign', 'wp928-second')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("refused connections stored: %d, %v", n, err)
	}
	for _, id := range []string{foreign, shared} {
		if _, err := e.vault.Resolve(ctx, e.f.OrgA, id); err != nil {
			t.Errorf("Resolve(%s) after the refusal = %v", id, err)
		}
	}
}

// TestAdmpServiceDeleteRederivesConnection: a connection of the
// organization-wide account with a service at client 1 and one at client 2
// is shared; when the service at client 2 expires, the connection becomes
// client 1's, and without services it is shared again.
func TestAdmpServiceDeleteRederivesConnection(t *testing.T) {
	e := newCacheEnv(t)
	c := e.connection(e.accountOrg, "wp928-line-two")
	s1 := e.service(e.accountOrg, "wp928-svc-c1", c.ID)
	e.ports(s1.ID, admp.ServicePort{PortKey: "p1", NetworkInterfaceID: e.ci1If, MatchedBy: admp.MatchedByRule})
	s2 := &admp.Service{OrganizationID: e.f.OrgA, AccountID: e.accountOrg, ExternalID: "wp928-svc-c2", ConnectionID: c.ID,
		Kind: "internet", ExpiresAt: time.Now().Add(-time.Minute)}
	if err := e.repo.UpsertService(e.org(), s2); err != nil {
		t.Fatal(err)
	}
	e.ports(s2.ID, admp.ServicePort{PortKey: "p2", NetworkInterfaceID: e.ci2If, MatchedBy: admp.MatchedByRule})
	if got := e.clientOf("admp_connection", c.ID); got != "" {
		t.Fatalf("connection with services at two clients: client %q, want none", got)
	}
	if gone, err := e.repo.DeleteExpired(e.org(), e.f.OrgA, time.Now()); err != nil || gone.Services != 1 {
		t.Fatalf("DeleteExpired = %+v, %v, want the service of client 2", gone, err)
	}
	if got := e.clientOf("admp_connection", c.ID); got != e.f.Client1 {
		t.Errorf("after the service of client 2 expired: client %q, want client 1", got)
	}
	if _, err := e.repo.GetConnection(e.f.ClientCtx(e.f.Client2), e.f.OrgA, c.ID); !errors.Is(err, admp.ErrCacheNotFound) {
		t.Errorf("client 2 still reads the connection: %v", err)
	}
	if _, err := e.f.Admin.Exec(context.Background(), `DELETE FROM admp_service WHERE id = $1`, s1.ID); err != nil {
		t.Fatal(err)
	}
	if got := e.clientOf("admp_connection", c.ID); got != "" {
		t.Errorf("without services: client %q, want none", got)
	}
}

// TestAdmpConnectionClientConcurrentPorts: two transactions that replace
// the ports of two services of the same connection at the same time (one
// at client 1, one at client 2) leave the connection shared: the second
// derivation waits for the first and counts its ports (no lost update).
func TestAdmpConnectionClientConcurrentPorts(t *testing.T) {
	e := newCacheEnv(t)
	c := e.connection(e.accountOrg, "wp928-line-race")
	s1 := e.service(e.accountOrg, "wp928-svc-race-1", c.ID)
	s2 := e.service(e.accountOrg, "wp928-svc-race-2", c.ID)
	ready, release := make(chan struct{}), make(chan struct{})
	first := make(chan error, 1)
	go func() {
		first <- database.WithRequestTenantTx(e.org(), e.f.App, e.f.OrgA, func(ctx context.Context) error {
			_, err := e.repo.ReplaceServicePorts(ctx, e.f.OrgA, s1.ID,
				[]admp.ServicePort{{PortKey: "p1", NetworkInterfaceID: e.ci1If, MatchedBy: admp.MatchedByRule}})
			close(ready)
			<-release
			return err
		})
	}()
	<-ready
	second := make(chan error, 1)
	go func() {
		_, err := e.repo.ReplaceServicePorts(e.org(), e.f.OrgA, s2.ID,
			[]admp.ServicePort{{PortKey: "p2", NetworkInterfaceID: e.ci2If, MatchedBy: admp.MatchedByRule}})
		second <- err
	}()
	time.Sleep(300 * time.Millisecond)
	close(release)
	if err := <-first; err != nil {
		t.Fatalf("first transaction: %v", err)
	}
	if err := <-second; err != nil {
		t.Fatalf("second transaction: %v", err)
	}
	if got := e.clientOf("admp_connection", c.ID); got != "" {
		t.Errorf("connection with ports at two clients after concurrent writes: client %q, want none", got)
	}
}

// TestAdmpServiceOnConnectionOfOtherAccount: a service references only a
// connection of its own account, on PostgreSQL and in memory.
func TestAdmpServiceOnConnectionOfOtherAccount(t *testing.T) {
	t.Run("postgres", func(t *testing.T) {
		e := newCacheEnv(t)
		c := e.connection(e.account1, "wp928-line-c1")
		s := &admp.Service{OrganizationID: e.f.OrgA, AccountID: e.accountOrg, ExternalID: "wp928-svc-cross", ConnectionID: c.ID,
			Kind: "internet", ExpiresAt: e.expires}
		if err := e.repo.UpsertService(e.org(), s); !errors.Is(err, admp.ErrInvalidCacheEntry) {
			t.Errorf("service of the organization account on client 1's connection: %v, want ErrInvalidCacheEntry", err)
		}
	})
	t.Run("memory", func(t *testing.T) {
		m := newMemoryCache(t)
		other := &admp.Account{OrganizationID: m.org, Name: "wp928-other", BaseURL: "https://admp.example.com", RateLimitRPS: 2,
			TimeoutMS: 10000, Enabled: false}
		if err := m.accounts.CreateAccount(m.ctx, other); err != nil {
			t.Fatal(err)
		}
		c := &admp.Connection{OrganizationID: m.org, AccountID: other.ID, ExternalID: "line", ExpiresAt: time.Now().Add(time.Hour)}
		if err := m.repo.UpsertConnection(m.ctx, c); err != nil {
			t.Fatal(err)
		}
		s := &admp.Service{OrganizationID: m.org, AccountID: m.accountOrg, ExternalID: "svc", ConnectionID: c.ID, Kind: "internet",
			ExpiresAt: time.Now().Add(time.Hour)}
		if err := m.repo.UpsertService(m.ctx, s); !errors.Is(err, admp.ErrInvalidCacheEntry) {
			t.Errorf("memory: service on a connection of another account: %v, want ErrInvalidCacheEntry", err)
		}
	})
}
