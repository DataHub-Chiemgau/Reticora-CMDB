//go:build admp_smoke

package admp_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp/readonly"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestAdmpSmoke reads an ADMP test environment (WP-1077 AK7, A-56, A-86).
// It runs only by hand:
//
//	cd backend && RETICORA_ADMP_SMOKE_BASE_URL=https://… RETICORA_ADMP_SMOKE_CREDENTIAL_ID=<uuid> \
//	  RETICORA_ADMP_SMOKE_PORT_IDS=301,302 RETICORA_ADMP_SMOKE_CONNECTION_IDS=4711 \
//	  go test -tags admp_smoke -count=1 -v ./internal/admp/ -run TestAdmpSmoke
//
// The variables name no secret (smoke_config_test.go): username and token
// of the api_token credential (E-249) come from credential.Service.Decrypt
// (stored, or vault_ref resolved on the server over the egress policy,
// CRD-02, wired as in cmd/server) on the database and with the master key
// of the server configuration (config.Load, as an instance of the server
// reads them); the reads run in the organization-wide scope of the
// credential's organization, as the refresh of the server does. The test
// reads ADMP through the Get and List methods of readonly.Client; the only
// request beside them is one GET of /V1/user/authentication without a
// cookie (the status without cookie of A-86, egress policy of the server).
// It writes nothing into a database (the lookup of the credential's
// organization is a read-only transaction) and prints neither an answer
// nor a token: it reports calls per endpoint, error classes and the
// answers to A-86, the lifetime of the session cookie among them
// (readonly.TokenLifetimeOf, no request).
func TestAdmpSmoke(t *testing.T) {
	cfg, err := loadSmokeConfig(os.Getenv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Skip("RETICORA_ADMP_SMOKE_BASE_URL and RETICORA_ADMP_SMOKE_CREDENTIAL_ID not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	app := config.Load()
	pool, err := pgxpool.New(ctx, app.DatabaseURL)
	if err != nil {
		t.Fatal("database: class connect")
	}
	defer pool.Close()
	enc, err := crypto.NewEnvelopeEncryptor(app.MasterKey, strings.Split(app.MasterKeyPrevious, ",")...)
	if err != nil {
		t.Fatal("master key of the server configuration missing or invalid")
	}
	var orgID string
	if err = database.WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT organization_id::text FROM credential WHERE id = $1 AND kind = 'api_token'`, cfg.CredentialID).
			Scan(&orgID)
	}); err != nil {
		t.Fatal("the api_token credential of RETICORA_ADMP_SMOKE_CREDENTIAL_ID was not found")
	}
	// The scope of the reads: Decrypt reads the credential in a tenant
	// transaction (database.WithRequestTenant), like the refresh of the
	// server (Sync.RequestRefresh).
	scope := database.OrgWideScope(orgID, "")
	ctx = tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: orgID})
	// A vault_ref credential is resolved on the server over the egress
	// policy, as cmd/server wires it; the test of collector connections
	// over NATS is not needed here.
	policy := egress.Options{AllowPrivate: app.EgressAllowPrivate}
	vault := credential.NewVaultResolver(policy, nil)
	defer vault.Close()
	secrets := credential.NewService(credential.NewPGRepository(pool), enc).WithVault(vault)
	client, err := readonly.New(readonly.Config{AccountID: "admp-smoke", OrganizationID: orgID, CredentialID: cfg.CredentialID,
		BaseURL: cfg.BaseURL, RateLimitRPS: 1, TimeoutMS: 15000}, secrets, nil, policy)
	if err != nil {
		t.Fatalf("client: %v", err)
	}

	calls := map[string]int{}
	classes := map[string]map[string]int{}
	report := func(endpoint string, err error) bool {
		calls[endpoint]++
		class := "ok"
		if err != nil {
			class = readonly.ClassOf(err)
			var e *readonly.Error
			if errors.As(err, &e) && e.Status != 0 {
				class += "/" + itoa(e.Status)
			}
		}
		if classes[endpoint] == nil {
			classes[endpoint] = map[string]int{}
		}
		classes[endpoint][class]++
		return err == nil
	}
	a86 := map[string]string{}

	_, err = client.GetSession(ctx)
	report("GET /V1/user/authentication", err)
	// The lifetime of the session cookie of the login (Max-Age, Expires or
	// the exp claim, else the E-250 default), read from the client.
	if lt, ok := readonly.TokenLifetimeOf(client); ok {
		a86["cookie lifetime"] = strconv.FormatInt(int64(lt.Expires.Sub(lt.IssuedAt).Seconds()), 10) + " s from the login (source " + lt.Source + ")"
	} else {
		a86["cookie lifetime"] = "not measured: no login succeeded"
	}

	ports, err := client.ListAccessPorts(ctx)
	if report("GET /V1/infrastructure/accessnodepicker", err) {
		a86["accessnodepicker"] = "AccessNode[] with modules[].ports[] as assumed (" + itoa(len(ports)) + " ports)"
	} else {
		a86["accessnodepicker"] = "not the assumed form or not readable (class " + readonly.ClassOf(err) + ")"
	}
	var services []int64
	for _, id := range cfg.PortIDs {
		ids, perr := client.ListPortServiceIDs(ctx, id)
		if report("GET /V1/infrastructure/port/{portId}/dienstIds", perr) {
			a86["dienstIds"] = "JSON array of numbers as assumed"
			services = append(services, ids...)
		} else if readonly.ClassOf(perr) == readonly.ClassDecode {
			a86["dienstIds"] = "not a JSON array of numbers (class decode)"
		}
	}
	magnitudes := map[string]int{}
	for _, id := range services {
		s, serr := client.GetService(ctx, id)
		if report("GET /V1/dienst/{id}", serr) {
			for _, v := range []*float64{s.Booked, s.ProvisioningPlanned, s.Provisioned, s.Cancelled, s.ShutdownPlanned, s.ShutDown} {
				switch {
				case v == nil:
				case *v >= 1e11:
					magnitudes["milliseconds"]++
				default:
					magnitudes["seconds"]++
				}
			}
		}
		_, perr := client.ListServiceProperties(ctx, id)
		report("GET /V1/dienst/{id}/properties/public", perr)
	}
	withHolders, withCarrier := 0, 0
	for _, id := range cfg.ConnectionIDs {
		c, cerr := client.GetConnection(ctx, id)
		if !report("GET /V1/anschluss/{id}", cerr) {
			continue
		}
		if c.Holders != nil {
			withHolders++
		}
		if c.Carrier != nil {
			withCarrier++
		} else {
			_, oerr := client.GetOrganization(ctx, c.CarrierID)
			report("GET /V1/organization/byIds", oerr)
		}
		for _, v := range []*float64{c.Created, c.Removed} {
			switch {
			case v == nil:
			case *v >= 1e11:
				magnitudes["milliseconds"]++
			default:
				magnitudes["seconds"]++
			}
		}
	}
	if len(cfg.ConnectionIDs) > 0 {
		a86["anschluss.inhaber"] = itoa(withHolders) + " of " + itoa(len(cfg.ConnectionIDs)) + " connections carry inhaber"
		a86["anschluss.netzbetreiber"] = itoa(withCarrier) + " of " + itoa(len(cfg.ConnectionIDs)) + " connections carry netzbetreiber"
	}
	a86["time values"] = "seconds " + itoa(magnitudes["seconds"]) + ", milliseconds " + itoa(magnitudes["milliseconds"])

	// The status of a read with a cookie ADMP never issued: a client whose
	// authenticator hands such a token.
	invalid, err := readonly.New(readonly.Config{AccountID: "admp-smoke-invalid", OrganizationID: orgID, CredentialID: cfg.CredentialID,
		BaseURL: cfg.BaseURL, RateLimitRPS: 1, TimeoutMS: 15000}, secrets, invalidToken{}, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = invalid.GetSession(ctx)
	var e *readonly.Error
	switch {
	case err == nil:
		a86["status with invalid cookie"] = "200 (ADMP did not check the cookie)"
	case errors.As(err, &e) && e.Status != 0:
		a86["status with invalid cookie"] = "HTTP " + itoa(e.Status) + " (class " + e.Class + ")"
	default:
		a86["status with invalid cookie"] = "class " + readonly.ClassOf(err)
	}
	// The status of a read without any cookie: readonly.Client always
	// sends its session cookie, so this one GET of the session path goes
	// over an egress client of its own (GET only, no body, no credential).
	a86["status without cookie"] = statusWithoutCookie(ctx, policy, cfg.BaseURL)

	t.Log("ADMP smoke run (A-86): calls per endpoint and result classes")
	endpoints := make([]string, 0, len(calls))
	for k := range calls {
		endpoints = append(endpoints, k)
	}
	sort.Strings(endpoints)
	for _, k := range endpoints {
		t.Logf("  %s: %d calls, classes %v", k, calls[k], classes[k])
	}
	keys := make([]string, 0, len(a86))
	for k := range a86 {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  A-86 %s: %s", k, a86[k])
	}
}

// statusWithoutCookie sends GET <base>/V1/user/authentication without a
// cookie and reports the status; the answer is not read.
func statusWithoutCookie(ctx context.Context, policy egress.Options, base string) string {
	policy.Timeout = 15 * time.Second
	hc := egress.NewClient(policy)
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(base, "/")+"/V1/user/authentication", http.NoBody)
	if err != nil {
		return "request not built"
	}
	req.Header.Set("Accept", "application/json")
	resp, err := hc.Do(req)
	if err != nil {
		return "no answer (network or egress policy)"
	}
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return "200 (ADMP did not ask for the cookie)"
	}
	return "HTTP " + itoa(resp.StatusCode)
}

// invalidToken is an authenticator with a token ADMP never issued.
type invalidToken struct{}

func (invalidToken) Token(context.Context) (string, time.Time, error) {
	return "reticora-smoke-invalid", time.Now().Add(time.Hour), nil
}

func itoa(n int) string { return strconv.Itoa(n) }
