package readonly

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// The connection reads of WP-1075 (ADM-07, E-252) against the fixtures of
// getV1Anschluss:id and getV1OrganizationByIds (E-255).
//
// Retest:
//
//	cd backend && go test -count=1 -run 'TestGetConnection|TestGetOrganization' ./internal/admp/readonly/

func ptr[T any](v T) *T { return &v }

// TestGetConnectionDecodesBusinessFixture: the ten required members of
// Anschluss, the four members of the embedded carrier and the typ of the
// holder decode; one GET of /V1/anschluss/4711, no read of byIds.
func TestGetConnectionDecodesBusinessFixture(t *testing.T) {
	m := newFixtureADMP(t)
	got, err := docClient(t, m, "", validLogin, nil).GetConnection(context.Background(), 4711)
	if err != nil {
		t.Fatal(err)
	}
	want := &Connection{ID: 4711, CarrierID: 9, ContractID: "V-2024-0815", Created: ptr(1690000000.0), Street: "Bahnhofstraße",
		HouseNumber: "12", HouseNumberSuffix: "a", PostalCode: "83278", City: "Traunstein",
		Carrier: &Organization{ID: 9, Name: "Netz Chiemgau GmbH", ShortName: "NCG", Number: ptr(4711.0)},
		Holders: []Holder{{Type: "Gewerblich"}}}
	if !reflect.DeepEqual(got.Holders[:1], want.Holders) {
		t.Errorf("holders %+v", got.Holders)
	}
	got.Holders, want.Holders = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("connection %+v (carrier %+v), want %+v", got, got.Carrier, want)
	}
	if n := m.count("GET " + fixtureBase + "/V1/anschluss/4711"); n != 1 || m.count("GET "+fixtureBase+organizationsPath) != 0 {
		t.Errorf("reads %v", m.hits)
	}
}

// TestGetConnectionDecodesPrivateFixture: a connection without the
// embedded carrier has Carrier nil; null members are empty; the holders
// are decoded with their typ; a connection without the relation inhaber
// has Holders nil, an empty relation an empty list; an answer for another
// id than the requested one is a decode failure.
func TestGetConnectionDecodesPrivateFixture(t *testing.T) {
	m := newFixtureADMP(t)
	c := docClient(t, m, "", validLogin, nil)
	ctx := context.Background()
	got, err := c.GetConnection(ctx, 4712)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != 4712 || got.Carrier != nil || got.CarrierID != 9 || got.HouseNumberSuffix != "" || got.Removed == nil ||
		*got.Removed != 1700000000 || *got.Created != 1690000000000 || got.City != "Siegsdorf" {
		t.Errorf("private connection %+v", got)
	}
	if len(got.Holders) != 2 || got.Holders[0].Type != "Privat" || got.Holders[1].Type != "Privat" {
		t.Errorf("holders %+v", got.Holders)
	}
	none, err := c.GetConnection(ctx, 4713)
	if err != nil || none.Holders != nil || none.Carrier != nil || none.ContractID != "" {
		t.Fatalf("without relations: %+v %v", none, err)
	}
	m.set("getV1Anschluss:id#4713", fixture{Status: 200, Body: []byte(`{"id":4713,"netzbetreiberId":9,"vertragId":null,"created":1,
		"removed":null,"strasse":null,"hausnummer":null,"hausnummerzusatz":null,"plz":null,"ort":null,"inhaber":[]}`)})
	empty, err := c.GetConnection(ctx, 4713)
	if err != nil || empty.Holders == nil || len(empty.Holders) != 0 || empty.Street != "" {
		t.Fatalf("empty holder list: %+v %v", empty, err)
	}
	m.set("getV1Anschluss:id#4713", fixture{Status: 200, Body: []byte(`{"id":4711,"netzbetreiberId":9,"vertragId":"V-X","created":1,
		"removed":null,"strasse":"Bahnhofstraße","hausnummer":"12","hausnummerzusatz":null,"plz":"83278","ort":"Traunstein"}`)})
	if other, oerr := c.GetConnection(ctx, 4713); ClassOf(oerr) != ClassDecode || other != nil {
		t.Errorf("answer for another id: %+v %v, want decode", other, oerr)
	}
	m.set("getV1Anschluss:id#4713", fixture{Status: 200, Body: []byte(`{"netzbetreiberId":9}`)})
	if _, err = c.GetConnection(ctx, 4713); ClassOf(err) != ClassDecode {
		t.Errorf("without id: %v, want decode", err)
	}
}

// TestGetConnectionDropsCarrierRelations: authUsers, accessNodes and
// groupName of the carrier, dienste and actions of the connection and the
// connection of a holder never reach the result; the Go types have no
// member for them.
func TestGetConnectionDropsCarrierRelations(t *testing.T) {
	m := newFixtureADMP(t)
	got, err := docClient(t, m, "", validLogin, nil).GetConnection(context.Background(), 4711)
	if err != nil {
		t.Fatal(err)
	}
	noCanary(t, "the connection", got)
	noCanary(t, "the carrier", got.Carrier)
	for _, typ := range []reflect.Type{reflect.TypeFor[Connection](), reflect.TypeFor[Organization](), reflect.TypeFor[Holder]()} {
		for i := range typ.NumField() {
			name := strings.ToLower(typ.Field(i).Name)
			for _, dropped := range []string{"accessnode", "authuser", "authgroup", "service", "dienst", "action", "group", "template"} {
				if strings.Contains(name, dropped) {
					t.Errorf("%s has the member %s", typ.Name(), typ.Field(i).Name)
				}
			}
		}
	}
}

// TestGetConnection404IsNotFound: the 404 NamedError of ADMP is
// ErrNotFound (class client, HTTP 404); the name and message of ADMP are
// not in the error; a 404 without a NamedError body and another failure
// keep their class without ErrNotFound.
func TestGetConnection404IsNotFound(t *testing.T) {
	m := newFixtureADMP(t)
	c := docClient(t, m, "", validLogin, nil)
	_, err := c.GetConnection(context.Background(), 4714)
	var e *Error
	if !errors.Is(err, ErrNotFound) || !errors.As(err, &e) || e.Class != ClassClient || e.Status != http.StatusNotFound {
		t.Fatalf("404: %v", err)
	}
	if strings.Contains(err.Error(), "NotFoundError") || strings.Contains(err.Error(), "Anschluss") {
		t.Errorf("the error carries the answer: %v", err)
	}
	// Only a NamedError of ADMP is ErrNotFound: a 404 of a proxy, a
	// gateway or a wrong base path (empty, not JSON, no name) is not.
	for _, body := range []string{"", "<html>Not Found</html>", `{"message":"no route"}`, `{"name":""}`, `[]`} {
		m.set("getV1Anschluss:id", fixture{Status: http.StatusNotFound, Body: []byte(body)})
		_, err = c.GetConnection(context.Background(), 4714)
		if errors.Is(err, ErrNotFound) || !errors.As(err, &e) || e.Class != ClassClient || e.Status != http.StatusNotFound {
			t.Errorf("404 with body %q: %v, want class client without ErrNotFound", body, err)
		}
	}
	m.set("getV1Anschluss:id", fixture{Status: http.StatusBadRequest})
	if _, err = c.GetConnection(context.Background(), 4714); errors.Is(err, ErrNotFound) || ClassOf(err) != ClassClient {
		t.Errorf("400: %v", err)
	}
	if _, err = c.GetConnection(context.Background(), 0); err == nil {
		t.Error("id 0 was read")
	}
	if n := m.count("GET " + fixtureBase + "/V1/anschluss/0"); n != 0 {
		t.Errorf("the mock saw id 0 %d times", n)
	}
}

// TestGetOrganizationSendsSingleID: GET /V1/organization/byIds carries
// exactly id=<n> once; the element with that id is decoded (not the first
// of the answer), its relations dropped; an answer without it is
// ErrNotFound; a second id, another parameter or an id list never leaves
// the client.
func TestGetOrganizationSendsSingleID(t *testing.T) {
	m := newFixtureADMP(t)
	var mu sync.Mutex
	var queries []string
	m.get = func(_ http.ResponseWriter, r *http.Request) bool {
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		return false
	}
	c := docClient(t, m, "", validLogin, nil)
	ctx := context.Background()
	org, err := c.GetOrganization(ctx, 9)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(org, &Organization{ID: 9, Name: "Netz Chiemgau GmbH", ShortName: "NCG", Number: ptr(4711.0)}) {
		t.Errorf("organization %+v", org)
	}
	noCanary(t, "the organization", org)
	if len(queries) != 1 || queries[0] != "id=9" {
		t.Errorf("queries %v, want exactly id=9", queries)
	}
	other, err := c.GetOrganization(ctx, 8)
	if err != nil || other.Name != "Andere Netz AG" || other.Number != nil {
		t.Errorf("organization 8: %+v %v", other, err)
	}
	if _, err = c.GetOrganization(ctx, 77); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing organization: %v", err)
	}
	if _, err = c.GetOrganization(ctx, 0); err == nil {
		t.Error("id 0 was read")
	}
	for _, q := range []map[string][]string{{"id": {"8", "9"}}, {"id": {"9"}, "x": {"1"}}, {"ids": {"9"}}, {"id": {"9,8"}}} {
		if err = c.getJSON(ctx, organizationsPath, q, nil); !errors.Is(err, ErrPathNotAllowed) {
			t.Errorf("byIds with %v: %v, want ErrPathNotAllowed", q, err)
		}
	}
	if n := m.count("GET " + fixtureBase + organizationsPath); n != 3 {
		t.Errorf("byIds reads %d, want 3", n)
	}
}
