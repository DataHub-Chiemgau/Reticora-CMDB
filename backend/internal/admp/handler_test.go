package admp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The read API of the ADMP cache (ADM-07, ADM-08; WP-930) on the memory
// store of --no-db: the connection with services and ports, the dedupe of
// refresh=true per minute and the open circuit breaker, and no personal
// value in plain text.
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestAdmpHandler' ./internal/admp/

const (
	handlerOrg    = "6f0b6f0e-0000-4000-8000-000000930000"
	handlerUser   = "6f0b6f0e-0000-4000-8000-000000930001"
	handlerCI     = "6f0b6f0e-0000-4000-8000-000000930010"
	handlerIface  = "6f0b6f0e-0000-4000-8000-000000930011"
	handlerHolder = "6f0b6f0e-0000-4000-8000-000000930020"
)

// queueOnly enqueues admp.refresh without running it (the dedupe of the
// job service by idempotency key).
type queueOnly struct{ svc *jobs.Service }

func (q queueOnly) Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error) {
	return q.svc.Enqueue(ctx, orgID, kind, params, opts)
}

// handlerEnv is the read API on the memory stores of one organization with
// an organization-wide account, a CI with an interface and a fake source.
type handlerEnv struct {
	t        *testing.T
	mux      *chi.Mux
	cache    *admp.MemoryRepository
	fake     *fakeADMP
	now      time.Time
	account  string
	accounts *admp.MemoryAccountRepository
}

func newHandlerEnv(t *testing.T) *handlerEnv {
	t.Helper()
	e := &handlerEnv{t: t, fake: newFakeADMP(), now: time.Date(2026, 11, 12, 10, 0, 5, 0, time.UTC)}
	e.accounts = admp.NewMemoryAccountRepository()
	a := &admp.Account{OrganizationID: handlerOrg, Name: "wp930-org", BaseURL: "https://admp.example.com", CredentialID: handlerUser,
		RateLimitRPS: 2, TimeoutMS: 10000, Enabled: true}
	if err := e.accounts.CreateAccount(e.ctx(), a); err != nil {
		t.Fatal(err)
	}
	e.account = a.ID
	e.cache = admp.NewMemoryRepository(e.accounts)
	svc := jobs.NewService(jobs.NewMemoryRepository())
	svc.Register(jobs.Kind{Name: admp.RefreshJobKind, MaxAttempts: 3, Timeout: 2 * time.Minute})
	sync := admp.NewSync(&admp.SyncOptions{Accounts: e.accounts, Cache: e.cache, Sources: e.fake,
		Now: func() time.Time { return e.now }}).WithSubmitter(queueOnly{svc: svc})
	targets := admp.TargetFuncs{
		Interface: func(_ context.Context, _, id string) (*admp.Target, error) {
			if id != handlerIface {
				return nil, admp.ErrTargetNotFound
			}
			return &admp.Target{CIID: handlerCI}, nil
		},
		CI: func(_ context.Context, _, id string) (*admp.Target, error) {
			if id != handlerCI {
				return nil, admp.ErrTargetNotFound
			}
			return &admp.Target{CIID: handlerCI}, nil
		},
	}
	e.mux = chi.NewRouter()
	admp.NewDataHandler(e.cache, targets, admp.NewAccountService(e.accounts, nil, egress.Options{}), sync).RegisterRoutes(e.mux)
	return e
}

func (e *handlerEnv) ctx() context.Context {
	scope := database.OrgWideScope(handlerOrg, handlerUser)
	ctx := database.ContextWithTenantScope(context.Background(), &scope)
	return tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: handlerOrg, UserID: handlerUser})
}

func (e *handlerEnv) get(path string) *httptest.ResponseRecorder {
	e.t.Helper()
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(e.ctx()))
	return rec
}

// seed stores a connection with a holder surrogate and a service with a
// port on the interface and one without match.
func (e *handlerEnv) seed() *admp.Connection {
	e.t.Helper()
	exp := e.now.Add(30 * 24 * time.Hour)
	c := &admp.Connection{OrganizationID: handlerOrg, AccountID: e.account, ExternalID: "4711", Carrier: "Netz AG",
		ContractID: "V-4711", HolderSubjectID: handlerHolder, Properties: map[string]any{"bandwidth_mbit": 100},
		FetchedAt: e.now, ExpiresAt: exp}
	if err := e.cache.UpsertConnection(e.ctx(), c); err != nil {
		e.t.Fatal(err)
	}
	s := &admp.Service{OrganizationID: handlerOrg, AccountID: e.account, ExternalID: "815", ConnectionID: c.ID, Kind: "internet",
		FetchedAt: e.now, ExpiresAt: exp}
	if err := e.cache.UpsertService(e.ctx(), s); err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.cache.ReplaceServicePorts(e.ctx(), handlerOrg, s.ID, []admp.ServicePort{
		{PortKey: "12", NetworkInterfaceID: handlerIface, CIID: handlerCI, MatchedBy: admp.MatchedByRule},
		{PortKey: "13", MatchedBy: admp.MatchedByNone}}); err != nil {
		e.t.Fatal(err)
	}
	return c
}

type connectionBody struct {
	ID              string         `json:"id"`
	ExternalID      string         `json:"external_id"`
	Carrier         string         `json:"carrier"`
	ContractID      string         `json:"contract_id"`
	HolderSubjectID string         `json:"holder_subject_id"`
	HolderPresent   bool           `json:"holder_present"`
	Properties      map[string]any `json:"properties"`
	Stale           bool           `json:"stale"`
	FetchedAt       time.Time      `json:"fetched_at"`
	RefreshJobID    string         `json:"refresh_job_id"`
	RefreshSkipped  string         `json:"refresh_skipped"`
	Services        []struct {
		ExternalID string `json:"external_id"`
		Ports      []struct {
			PortKey            string `json:"port_key"`
			NetworkInterfaceID string `json:"network_interface_id"`
			CIID               string `json:"ci_id"`
			MatchedBy          string `json:"matched_by"`
		} `json:"ports"`
	} `json:"services"`
}

func decodeConnection(t *testing.T, rec *httptest.ResponseRecorder) connectionBody {
	t.Helper()
	var b connectionBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return b
}

// TestAdmpHandlerConnection: GET /admp/connections/{id} answers the cached
// connection with carrier, contract, fetched_at and stale and its service
// with both ports (port_key, interface, CI, matched_by); an unknown id is
// 404; the lists of the interface and the CI carry the connection; an
// unknown interface or CI is 404.
func TestAdmpHandlerConnection(t *testing.T) {
	e := newHandlerEnv(t)
	c := e.seed()
	rec := e.get("/api/v1/admp/connections/" + c.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body.String())
	}
	b := decodeConnection(t, rec)
	if b.ExternalID != "4711" || b.Carrier != "Netz AG" || b.ContractID != "V-4711" || b.Stale || !b.FetchedAt.Equal(e.now) ||
		b.Properties["bandwidth_mbit"] != float64(100) {
		t.Errorf("connection %+v", b)
	}
	if len(b.Services) != 1 || b.Services[0].ExternalID != "815" || len(b.Services[0].Ports) != 2 {
		t.Fatalf("services %+v", b.Services)
	}
	p := b.Services[0].Ports[0]
	if p.PortKey != "12" || p.NetworkInterfaceID != handlerIface || p.CIID != handlerCI || p.MatchedBy != admp.MatchedByRule ||
		b.Services[0].Ports[1].MatchedBy != admp.MatchedByNone {
		t.Errorf("ports %+v", b.Services[0].Ports)
	}
	if rec = e.get("/api/v1/admp/connections/6f0b6f0e-0000-4000-8000-000000930099"); rec.Code != http.StatusNotFound {
		t.Errorf("unknown connection: %d", rec.Code)
	}

	var iface struct {
		Data []struct {
			ExternalID string `json:"external_id"`
			Stale      bool   `json:"stale"`
			Ports      []struct {
				PortKey string `json:"port_key"`
			} `json:"ports"`
			Connection *struct {
				ID      string `json:"id"`
				Carrier string `json:"carrier"`
			} `json:"connection"`
		} `json:"data"`
		NextCursor string `json:"next_cursor"`
	}
	rec = e.get("/api/v1/network-interfaces/" + handlerIface + "/admp")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &iface) != nil {
		t.Fatalf("interface: %d %s", rec.Code, rec.Body.String())
	}
	if len(iface.Data) != 1 || len(iface.Data[0].Ports) != 1 || iface.Data[0].Ports[0].PortKey != "12" ||
		iface.Data[0].Connection == nil || iface.Data[0].Connection.ID != c.ID {
		t.Errorf("interface list %s", rec.Body.String())
	}
	var ci struct {
		Data []struct {
			ID      string `json:"id"`
			Carrier string `json:"carrier"`
		} `json:"data"`
	}
	rec = e.get("/api/v1/cis/" + handlerCI + "/admp-connections")
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &ci) != nil || len(ci.Data) != 1 || ci.Data[0].ID != c.ID {
		t.Errorf("ci list: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{
		"/api/v1/network-interfaces/6f0b6f0e-0000-4000-8000-000000930098/admp",
		"/api/v1/cis/6f0b6f0e-0000-4000-8000-000000930098/admp-connections",
	} {
		if rec = e.get(path); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, rec.Code)
		}
	}
}

// TestAdmpHandlerRefreshThrottle: refresh=true queues admp.refresh and
// answers 202 with refresh_job_id; a second request in the same minute
// names the same job, the next minute a new one; the interface refresh
// works alike; an open circuit breaker answers 200 with refresh_skipped
// circuit_open and queues nothing.
func TestAdmpHandlerRefreshThrottle(t *testing.T) {
	e := newHandlerEnv(t)
	c := e.seed()
	path := "/api/v1/admp/connections/" + c.ID + "?refresh=true"
	first := e.get(path)
	if first.Code != http.StatusAccepted {
		t.Fatalf("refresh: %d %s", first.Code, first.Body.String())
	}
	job := decodeConnection(t, first).RefreshJobID
	if job == "" || decodeConnection(t, first).Carrier != "Netz AG" {
		t.Fatalf("refresh answer %s", first.Body.String())
	}
	e.now = e.now.Add(40 * time.Second)
	second := e.get(path)
	if second.Code != http.StatusAccepted || decodeConnection(t, second).RefreshJobID != job {
		t.Errorf("second refresh in the minute: %d %s, want 202 with job %s", second.Code, second.Body.String(), job)
	}
	e.now = e.now.Add(time.Minute)
	third := e.get(path)
	if next := decodeConnection(t, third).RefreshJobID; third.Code != http.StatusAccepted || next == "" || next == job {
		t.Errorf("refresh in the next minute: %d %s, want a new job", third.Code, third.Body.String())
	}

	var list struct {
		RefreshJobID string `json:"refresh_job_id"`
	}
	rec := e.get("/api/v1/network-interfaces/" + handlerIface + "/admp?refresh=true")
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &list) != nil || list.RefreshJobID == "" {
		t.Errorf("interface refresh: %d %s", rec.Code, rec.Body.String())
	}
	if rec = e.get("/api/v1/admp/connections/" + c.ID + "?refresh=maybe"); rec.Code != http.StatusBadRequest {
		t.Errorf("refresh=maybe: %d, want 400", rec.Code)
	}

	e.fake.set(func() { e.fake.open = true })
	e.now = e.now.Add(time.Minute)
	open := e.get(path)
	b := decodeConnection(t, open)
	if open.Code != http.StatusOK || b.RefreshSkipped != admp.RefreshSkippedCircuitOpen || b.RefreshJobID != "" {
		t.Errorf("open circuit breaker: %d %s, want 200 with refresh_skipped circuit_open", open.Code, open.Body.String())
	}
}

// TestAdmpHandlerNoPersonalData: the holder appears only as its surrogate
// and holder_present, a CI only as ci_id; no answer carries a member that
// names a person or a CI field.
func TestAdmpHandlerNoPersonalData(t *testing.T) {
	e := newHandlerEnv(t)
	c := e.seed()
	for _, path := range []string{
		"/api/v1/admp/connections/" + c.ID,
		"/api/v1/network-interfaces/" + handlerIface + "/admp",
		"/api/v1/cis/" + handlerCI + "/admp-connections",
	} {
		rec := e.get(path)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"holder_subject_id":"`+handlerHolder+`"`) || !strings.Contains(body, `"holder_present":true`) {
			t.Errorf("GET %s: holder surrogate or holder_present missing: %s", path, body)
		}
		for _, member := range []string{`"holder":`, `"holder_name"`, `"name":`, `"hostname"`, `"address":`, `"ci":`} {
			if strings.Contains(body, member) {
				t.Errorf("GET %s carries %s: %s", path, member, body)
			}
		}
	}
	// A personal key reaches the cache only redacted (no vault without a
	// database, WP-1075), so no answer can carry it.
	bad := &admp.Connection{OrganizationID: handlerOrg, AccountID: e.account, ExternalID: "4712", ExpiresAt: e.now.Add(time.Hour),
		Properties: map[string]any{"full_name": "Erika Mustermann"}, PrivateAddress: "Hauptstraße 3, 83313 Siegsdorf"}
	if err := e.cache.UpsertConnection(e.ctx(), bad); err != nil {
		t.Fatal(err)
	}
	rec := e.get("/api/v1/admp/connections/" + bad.ID)
	if body := rec.Body.String(); rec.Code != http.StatusOK || strings.Contains(body, "Mustermann") || strings.Contains(body, "Hauptstraße") ||
		!strings.Contains(body, `"address_form":"none"`) {
		t.Errorf("connection with personal values: %d %s", rec.Code, body)
	}
}

// TestAdmpHandlerTypedServiceFields: every service of the connection and
// of the interface carries status, the times of its life cycle,
// template_id and the public properties ordered by order as {key, label,
// value}; a missing value leaves its field out, a sealed value its value.
func TestAdmpHandlerTypedServiceFields(t *testing.T) {
	e := newHandlerEnv(t)
	c := e.seed()
	exp := e.now.Add(30 * 24 * time.Hour)
	s := &admp.Service{OrganizationID: handlerOrg, AccountID: e.account, ExternalID: "815", ConnectionID: c.ID, Kind: "FTTH 1000",
		FetchedAt: e.now, ExpiresAt: exp, Properties: map[string]any{
			"template_id": 3,
			"lifecycle":   map[string]any{"booked_at": "2023-11-14T22:13:20Z", "provisioned_at": "2023-11-20T08:00:00Z"},
			"values": map[string]any{
				"technology": map[string]any{"value": "FTTH", "label": "Technologie", "order": 1},
				"bandwidth":  map[string]any{"value": 1000, "label": "Bandbreite", "order": 2},
				"note":       map[string]any{"value": nil, "label": "Notiz"},
				"email":      map[string]any{"value": "erika@example.org", "label": "E-Mail", "order": 3},
				// A personal object, a personal number and a source value
				// shaped like a reference give no value either.
				"contact_name": map[string]any{"value": map[string]any{"first": "Erika", "last": "Musterfrau"}, "order": 4},
				"phone":        map[string]any{"value": 4917012345678, "order": 5},
				"ticket":       map[string]any{"value": map[string]any{"$pii": "6f0b6f0e-0000-4000-8000-000001074001"}, "order": 6},
			}}}
	if err := e.cache.UpsertService(e.ctx(), s); err != nil {
		t.Fatal(err)
	}
	type serviceJSON struct {
		ExternalID    string `json:"external_id"`
		Status        string `json:"status"`
		BookedAt      string `json:"booked_at"`
		ProvisionedAt string `json:"provisioned_at"`
		TemplateID    *int64 `json:"template_id"`
		Properties    []struct {
			Key   string `json:"key"`
			Label string `json:"label"`
			Value any    `json:"value"`
		} `json:"properties"`
	}
	check := func(what string, svc serviceJSON, raw string) {
		t.Helper()
		if svc.Status != admp.ServiceStatusActive || svc.BookedAt != "2023-11-14T22:13:20Z" ||
			svc.ProvisionedAt != "2023-11-20T08:00:00Z" || svc.TemplateID == nil || *svc.TemplateID != 3 {
			t.Errorf("%s: %+v", what, svc)
		}
		keys := []string{}
		for _, p := range svc.Properties {
			keys = append(keys, p.Key)
		}
		if strings.Join(keys, ",") != "technology,bandwidth,email,contact_name,phone,ticket,note" || svc.Properties[0].Value != "FTTH" ||
			svc.Properties[1].Value != float64(1000) {
			t.Errorf("%s properties: %+v", what, svc.Properties)
		}
		for _, p := range svc.Properties[2:] {
			if p.Value != nil {
				t.Errorf("%s: property %s carries the value %v", what, p.Key, p.Value)
			}
		}
		for _, absent := range []string{`"cancelled_at"`, `"shut_down_at"`, `"shutdown_planned_at"`, "erika", "Erika", "Musterfrau",
			"4917012345678", "$pii", "6f0b6f0e-0000-4000-8000-000001074001"} {
			if strings.Contains(raw, absent) {
				t.Errorf("%s carries %s: %s", what, absent, raw)
			}
		}
	}
	rec := e.get("/api/v1/admp/connections/" + c.ID)
	var conn struct {
		Services []serviceJSON `json:"services"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &conn); err != nil {
		t.Fatal(err)
	}
	if len(conn.Services) != 1 {
		t.Fatalf("services %s", rec.Body.String())
	}
	check("connection", conn.Services[0], rec.Body.String())
	rec = e.get("/api/v1/network-interfaces/" + handlerIface + "/admp")
	var list struct {
		Data []serviceJSON `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Data) != 1 {
		t.Fatalf("interface %s", rec.Body.String())
	}
	check("interface", list.Data[0], rec.Body.String())
	// A service without life cycle and values: unknown, no fields.
	bare := &admp.Service{OrganizationID: handlerOrg, AccountID: e.account, ExternalID: "999", ConnectionID: c.ID, Kind: "unknown",
		FetchedAt: e.now, ExpiresAt: exp}
	if err := e.cache.UpsertService(e.ctx(), bare); err != nil {
		t.Fatal(err)
	}
	rec = e.get("/api/v1/admp/connections/" + c.ID)
	if !strings.Contains(rec.Body.String(), `"external_id":"999","connection_id":"`+c.ID+`","kind":"unknown","status":"unknown","properties":[]`) {
		t.Errorf("bare service: %s", rec.Body.String())
	}
}
