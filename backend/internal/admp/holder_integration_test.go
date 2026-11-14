package admp_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The holder of an ADMP connection (ADM-06, SEC-05, E-243, E-254;
// WP-1076) on PostgreSQL through the productive source against the mock of
// the documentation: one person subject per connection, never plain text
// in admp_connection, audit_log or event_outbox, the holder route with
// admp:holder_read and an audit entry per read, crypto-shredding.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -run TestAdmpHolder ./internal/admp/

const holderPath = "/api/v1/admp/connections/{id}/holder"

// holderEnv is the sync environment with the mock, the refresh on the
// productive source and the read API behind the route authorization of
// the server.
type holderEnv struct {
	*syncEnv
	m    *docADMP
	sync *admp.Sync
	mux  *chi.Mux
}

func newHolderEnv(t *testing.T) *holderEnv {
	t.Helper()
	e := newSyncEnv(t)
	m := newDocADMP(t)
	s, _ := e.docSync(m)
	h := admp.NewDataHandler(e.cache, e.cache, nil, nil)
	mux := chi.NewRouter()
	mux.With(server.AuthorizeRoute(http.MethodGet, holderPath)).Get(holderPath, func(w http.ResponseWriter, r *http.Request) {
		inner := chi.NewRouter()
		h.RegisterRoutes(inner)
		inner.ServeHTTP(w, r)
	})
	mux.With(server.AuthorizeRoute(http.MethodGet, "/api/v1/admp/connections/{id}")).Get("/api/v1/admp/connections/{id}",
		func(w http.ResponseWriter, r *http.Request) {
			inner := chi.NewRouter()
			h.RegisterRoutes(inner)
			inner.ServeHTTP(w, r)
		})
	return &holderEnv{syncEnv: e, m: m, sync: s, mux: mux}
}

// get serves path as a user of organization A with perms.
func (e *holderEnv) get(path string, perms ...identity.Permission) *httptest.ResponseRecorder {
	e.t.Helper()
	ctx := tenant.WithTenant(e.org(), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA, Permissions: perms})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx))
	return rec
}

// holderAudits returns the changes of the admp.holder.read entries of a
// connection as text, one per entry.
func (e *holderEnv) holderAudits(connectionID string) []string {
	e.t.Helper()
	rows, err := e.f.Admin.Query(context.Background(), `SELECT action || ' ' || COALESCE(actor_id::text, '') || ' ' || changes::text
		FROM audit_log WHERE organization_id = $1 AND resource_type = 'admp_holder' AND resource_id = $2 ORDER BY seq`,
		e.f.OrgA, connectionID)
	if err != nil {
		e.t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

// holderSubject resolves the holder subject of a stored connection.
func (e *holderEnv) holderSubject(external string) (storedConnection, *pii.Subject) {
	e.t.Helper()
	c := e.storedConnection(external)
	if c.HolderSubject == "" {
		return c, nil
	}
	s, err := e.vault.Resolve(e.org(), e.f.OrgA, c.HolderSubject)
	if errors.Is(err, pii.ErrNotFound) {
		return c, nil
	}
	if err != nil {
		e.t.Fatal(err)
	}
	return c, s
}

// holderPlain are values of the holders of fixture 4712 that never appear
// outside the vault and the holder route.
var holderPlain = []string{"Musterfrau", "Mustermann", "Erika", "Dr."}

// TestAdmpHolderStoredOnlyAsSurrogate: a refresh keeps the holders of the
// connection as one subject of the kind person; admp_connection, audit_log
// and event_outbox hold no value of them; the connection answers only
// holder_subject_id, holder_present and holder_count.
func TestAdmpHolderStoredOnlyAsSurrogate(t *testing.T) {
	e := newHolderEnv(t)
	if res := e.docRefresh(e.sync, admp.KindConnection, "4712"); res.Result != "ok" {
		t.Fatalf("refresh: %+v", res)
	}
	c, subject := e.holderSubject("4712")
	if subject == nil || subject.Kind != pii.KindPerson {
		t.Fatalf("holder subject %+v of %+v", subject, c)
	}
	rec := e.get("/api/v1/admp/connections/"+c.ID+"/holder", identity.PermADMPHolderRead)
	if rec.Code != http.StatusOK {
		t.Fatalf("holder read: %d %s", rec.Code, rec.Body.String())
	}
	audit := e.scalar(`SELECT COALESCE(string_agg(changes::text, ' '), '') FROM audit_log WHERE organization_id = $1`, e.f.OrgA)
	outbox := e.outboxText()
	for _, plain := range holderPlain {
		for where, text := range map[string]string{"admp_connection": c.Row, "audit_log": audit, "event_outbox": outbox} {
			if strings.Contains(text, plain) {
				t.Errorf("%s holds %q: %s", where, plain, text)
			}
		}
	}
	rec = e.get("/api/v1/admp/connections/"+c.ID, identity.PermADMPRead)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `"holder_subject_id":"`+c.HolderSubject+`"`) ||
		!strings.Contains(body, `"holder_present":true`) || !strings.Contains(body, `"holder_count":2`) {
		t.Errorf("connection: %d %s", rec.Code, body)
	}
	for _, plain := range holderPlain {
		if strings.Contains(body, plain) {
			t.Errorf("the connection route carries %q: %s", plain, body)
		}
	}
	if e.m.nonGet() != 0 {
		t.Errorf("writes at the mock: %v", e.m.hits)
	}
}

// TestAdmpHolderWithoutBirthDate: the subject carries no value of
// geburtsdatum and no key for it, nor the ids of ADMP.
func TestAdmpHolderWithoutBirthDate(t *testing.T) {
	e := newHolderEnv(t)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	_, subject := e.holderSubject("4712")
	if subject == nil {
		t.Fatal("no holder subject")
	}
	for k, v := range subject.Data {
		if strings.Contains(k, "geburt") || strings.Contains(k, "birth") || strings.HasSuffix(k, ".id") ||
			v == "315532800" || v == "-123456789" || v == "31" || v == "32" {
			t.Errorf("the subject carries %s = %s", k, v)
		}
	}
}

// TestAdmpHolderMultipleHolders: two holders lie in one subject (count
// and <n>.<field> in the order of the ADMP id), holder_count is 2; the
// route lists both with the address as one line.
func TestAdmpHolderMultipleHolders(t *testing.T) {
	e := newHolderEnv(t)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	c, subject := e.holderSubject("4712")
	if subject == nil || subject.Data["count"] != "2" || subject.Data["1.name"] != "Musterfrau" || subject.Data["1.titel"] != "Dr." ||
		subject.Data["2.name"] != "Mustermann" || subject.Data["2.vorname"] != "Max" || subject.Data["1.typ"] != "Privat" {
		t.Fatalf("subject %+v", subject)
	}
	if c.Properties["holder_count"] != float64(2) {
		t.Errorf("holder_count %v", c.Properties["holder_count"])
	}
	rec := e.get("/api/v1/admp/connections/"+c.ID+"/holder", identity.PermADMPHolderRead)
	var v struct {
		ConnectionID string           `json:"connection_id"`
		Holders      []map[string]any `json:"holders"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &v) != nil || v.ConnectionID != c.ID || len(v.Holders) != 2 {
		t.Fatalf("holder read: %d %s", rec.Code, rec.Body.String())
	}
	first := v.Holders[0]
	if first["type"] != "Privat" || first["title"] != "Dr." || first["first_name"] != "Erika" || first["last_name"] != "Musterfrau" ||
		first["address"] != "Hauptstraße 3, 83313 Siegsdorf" || first["register_court"] != nil {
		t.Errorf("first holder %v", first)
	}
	if v.Holders[1]["last_name"] != "Mustermann" {
		t.Errorf("second holder %v", v.Holders[1])
	}
	// A business holder carries register court and number.
	e.docRefresh(e.sync, admp.KindConnection, "4711")
	business := e.storedConnection("4711")
	rec = e.get("/api/v1/admp/connections/"+business.ID+"/holder", identity.PermADMPHolderRead)
	if !strings.Contains(rec.Body.String(), `"register_court":"Amtsgericht Traunstein"`) ||
		!strings.Contains(rec.Body.String(), `"register_number":"HRB 1234"`) {
		t.Errorf("business holder: %s", rec.Body.String())
	}
}

// TestAdmpHolderMissingRelationKeepsSubject: an answer without the
// relation inhaber leaves subject, reference, holder_count and the private
// address as they are.
func TestAdmpHolderMissingRelationKeepsSubject(t *testing.T) {
	e := newHolderEnv(t)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	before, subject := e.holderSubject("4712")
	if subject == nil {
		t.Fatal("no holder subject")
	}
	e.m.set("getV1Anschluss:id#4712", http.StatusOK, `{"id":4712,"netzbetreiberId":9,"vertragId":"V-2024-0816","created":1690000000000,
		"removed":1700000000,"strasse":"Hauptstraße","hausnummer":"3","hausnummerzusatz":null,"plz":"83313","ort":"Siegsdorf"}`)
	e.now = e.now.Add(time.Hour)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	after, kept := e.holderSubject("4712")
	if kept == nil || after.HolderSubject != before.HolderSubject || kept.Data["count"] != "2" ||
		after.Properties["holder_count"] != float64(2) || after.AddressSubject != before.AddressSubject {
		t.Errorf("after an answer without inhaber: %+v (subject %+v), before %+v", after, kept, before)
	}
}

// TestAdmpHolderEmptyListShredsSubject: an empty inhaber[] deletes the
// subject (pii.DeleteTx): the reference is NULL, the subject no longer
// resolves, the route answers 404; the private address stays as a new
// surrogate without the shredded anchor.
func TestAdmpHolderEmptyListShredsSubject(t *testing.T) {
	e := newHolderEnv(t)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	before, _ := e.holderSubject("4712")
	e.m.set("getV1Anschluss:id#4712", http.StatusOK, `{"id":4712,"netzbetreiberId":9,"vertragId":"V-2024-0816","created":1690000000000,
		"removed":null,"strasse":"Hauptstraße","hausnummer":"3","hausnummerzusatz":null,"plz":"83313","ort":"Siegsdorf","inhaber":[]}`)
	e.now = e.now.Add(time.Hour)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	after, subject := e.holderSubject("4712")
	if after.HolderSubject != "" || subject != nil || after.Properties["holder_count"] != nil {
		t.Fatalf("after an empty holder list: %+v %+v", after, subject)
	}
	if _, err := e.vault.Resolve(e.org(), e.f.OrgA, before.HolderSubject); !errors.Is(err, pii.ErrNotFound) {
		t.Errorf("the old holder still resolves (%v)", err)
	}
	if rec := e.get("/api/v1/admp/connections/"+after.ID+"/holder", identity.PermADMPHolderRead); rec.Code != http.StatusNotFound {
		t.Errorf("holder route without holder: %d", rec.Code)
	}
	if address, err := e.vault.Resolve(e.org(), e.f.OrgA, after.AddressSubject); err != nil || address.Data["v"] != "Hauptstraße 3, 83313 Siegsdorf" {
		t.Errorf("address after the holder went: %+v %v", address, err)
	}
	if n := len(e.holderAudits(after.ID)); n != 0 {
		t.Errorf("a 404 was audited %d times", n)
	}
}

// TestAdmpHolderReadRequiresPermission: without admp:holder_read the route
// answers 403 without any holder value and writes no audit entry; with
// admp:read alone too; an unknown connection is 404.
func TestAdmpHolderReadRequiresPermission(t *testing.T) {
	e := newHolderEnv(t)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	c := e.storedConnection("4712")
	for _, perms := range [][]identity.Permission{nil, {identity.PermADMPRead}, {identity.PermADMPRead, identity.PermADMPManage}} {
		rec := e.get("/api/v1/admp/connections/"+c.ID+"/holder", perms...)
		if rec.Code != http.StatusForbidden {
			t.Errorf("permissions %v: %d, want 403", perms, rec.Code)
		}
		for _, plain := range holderPlain {
			if strings.Contains(rec.Body.String(), plain) {
				t.Errorf("the 403 carries %q: %s", plain, rec.Body.String())
			}
		}
	}
	if n := len(e.holderAudits(c.ID)); n != 0 {
		t.Errorf("refused reads audited: %d", n)
	}
	if rec := e.get("/api/v1/admp/connections/"+e.f.ID()+"/holder", identity.PermADMPHolderRead); rec.Code != http.StatusNotFound {
		t.Errorf("unknown connection: %d", rec.Code)
	}
	if perm, _ := server.PermissionForRoute(http.MethodGet, "/api/v1/admp/connections/x/holder"); perm != identity.PermADMPHolderRead {
		t.Errorf("route permission %s", perm)
	}
}

// TestAdmpHolderReadIsAudited: every successful read writes exactly one
// audit entry (admp.holder.read, resource admp_holder, the connection id,
// the reading user) without a holder value; a client principal outside the
// connection's client gets 404 and no entry.
func TestAdmpHolderReadIsAudited(t *testing.T) {
	e := newHolderEnv(t)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	c := e.storedConnection("4712")
	for i := 1; i <= 2; i++ {
		if rec := e.get("/api/v1/admp/connections/"+c.ID+"/holder", identity.PermADMPHolderRead); rec.Code != http.StatusOK {
			t.Fatalf("read %d: %d", i, rec.Code)
		}
		audits := e.holderAudits(c.ID)
		if len(audits) != i {
			t.Fatalf("after %d reads %d entries: %v", i, len(audits), audits)
		}
		last := audits[len(audits)-1]
		if !strings.HasPrefix(last, admp.HolderAuditAction+" "+e.f.User+" ") || !strings.Contains(last, c.HolderSubject) {
			t.Errorf("entry %s", last)
		}
		for _, plain := range holderPlain {
			if strings.Contains(last, plain) {
				t.Errorf("the entry holds %q: %s", plain, last)
			}
		}
	}
	// account1 belongs to client 1: a principal of client 2 does not see
	// the connection.
	ctx := tenant.WithTenant(e.f.ClientCtx(e.f.Client2), tenant.TenantInfo{OrganizationID: e.f.OrgA, UserID: e.f.User})
	ctx = identity.WithPrincipal(ctx, identity.Principal{Subject: e.f.User, OrganizationID: e.f.OrgA,
		Permissions: []identity.Permission{identity.PermADMPHolderRead}})
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admp/connections/"+c.ID+"/holder", nil).WithContext(ctx))
	if rec.Code != http.StatusNotFound || len(e.holderAudits(c.ID)) != 2 {
		t.Errorf("client 2: %d, %d entries", rec.Code, len(e.holderAudits(c.ID)))
	}
}

// TestAdmpHolderCryptoShredding: pii.DeleteTx on the holder subject takes
// the anchored private address along; holder_subject_id and
// address_subject_id are NULL, the route answers 404 and ResolveTx finds
// nothing; a later refresh creates a new subject.
func TestAdmpHolderCryptoShredding(t *testing.T) {
	e := newHolderEnv(t)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	c := e.storedConnection("4712")
	if c.HolderSubject == "" || c.AddressSubject == "" {
		t.Fatalf("connection %+v", c)
	}
	if parent := e.scalar(`SELECT COALESCE(parent_id::text, '') FROM pii_subject WHERE surrogate_id = $1`, c.AddressSubject); parent != c.HolderSubject {
		t.Fatalf("address anchored at %q, want the holder %s", parent, c.HolderSubject)
	}
	if err := database.WithRequestTenant(e.org(), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		return pii.DeleteTx(ctx, tx, e.f.OrgA, c.HolderSubject)
	}); err != nil {
		t.Fatal(err)
	}
	after := e.storedConnection("4712")
	if after.HolderSubject != "" || after.AddressSubject != "" {
		t.Fatalf("references after the shredding: %+v", after)
	}
	if err := database.WithRequestTenant(e.org(), e.f.App, e.f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
		found, rerr := e.vault.ResolveTx(ctx, tx, e.f.OrgA, []string{c.HolderSubject, c.AddressSubject})
		if rerr == nil && len(found) != 0 {
			t.Errorf("ResolveTx after the shredding: %v", found)
		}
		return rerr
	}); err != nil {
		t.Fatal(err)
	}
	if rec := e.get("/api/v1/admp/connections/"+c.ID+"/holder", identity.PermADMPHolderRead); rec.Code != http.StatusNotFound {
		t.Errorf("holder route after the shredding: %d", rec.Code)
	}
	e.now = e.now.Add(time.Hour)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	again, subject := e.holderSubject("4712")
	if subject == nil || again.HolderSubject == c.HolderSubject || again.AddressSubject == "" {
		t.Errorf("after a new refresh: %+v %+v", again, subject)
	}
}

// TestAdmpHolderUpdateKeepsSubject: changed holder data update the subject
// in place (same surrogate, UpdateTx); unchanged data leave it; the
// private address keeps its surrogate.
func TestAdmpHolderUpdateKeepsSubject(t *testing.T) {
	e := newHolderEnv(t)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	before, _ := e.holderSubject("4712")
	e.m.set("getV1Anschluss:id#4712", http.StatusOK, `{"id":4712,"netzbetreiberId":9,"vertragId":"V-2024-0816","created":1690000000000,
		"removed":null,"strasse":"Hauptstraße","hausnummer":"3","hausnummerzusatz":null,"plz":"83313","ort":"Siegsdorf",
		"inhaber":[{"id":31,"anschlussId":4712,"typ":"Privat","titel":null,"name":"Beispiel","vorname":"Erika","geburtsdatum":315532800,
		"registerinstanz":null,"registernummer":null,"strasse":"Hauptstraße","hausnummer":"3","hausnummerzusatz":null,"plz":"83313","ort":"Siegsdorf"}]}`)
	e.now = e.now.Add(time.Hour)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	after, subject := e.holderSubject("4712")
	if subject == nil || after.HolderSubject != before.HolderSubject || subject.Data["count"] != "1" || subject.Data["1.name"] != "Beispiel" ||
		subject.Data["2.name"] != "" || after.Properties["holder_count"] != float64(1) || after.AddressSubject != before.AddressSubject {
		t.Errorf("after the change: %+v %+v, before %+v", after, subject, before)
	}
	if strings.Contains(after.Row, "Beispiel") {
		t.Errorf("admp_connection holds the new name: %s", after.Row)
	}
	e.now = e.now.Add(time.Hour)
	e.docRefresh(e.sync, admp.KindConnection, "4712")
	if again, _ := e.holderSubject("4712"); again.HolderSubject != before.HolderSubject {
		t.Errorf("an unchanged refresh replaced the subject: %s", again.HolderSubject)
	}
}
