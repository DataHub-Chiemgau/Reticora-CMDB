package readonly

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// The reads of WP-1073 against the fixtures (E-251, E-253, E-255): the
// port inventory without the credentials of the network elements, the
// services of a port, a service and the login state without LDAP data;
// answers of another form than assumed (A-86) are the class decode.

// canaries are the values of the fixtures no decoded result may carry.
var canaries = []string{"canary"}

// noCanary fails t when the printed value carries a canary of the
// fixtures.
func noCanary(t *testing.T, what string, v any) {
	t.Helper()
	text := fmt.Sprintf("%+v %#v", v, v)
	raw, _ := json.Marshal(v)
	for _, c := range canaries {
		if strings.Contains(text, c) || strings.Contains(string(raw), c) {
			t.Errorf("%s carries %q: %s", what, c, text)
		}
	}
}

// TestListAccessPortsDropsSecrets: the inventory fixture (two nodes with
// url, username, password, iv, organization and extra) decodes into three
// flat ports with node id and name, protocol, unit and port index; no
// value of the credentials or the extras is in the result, and the Go type
// has no member for them.
func TestListAccessPortsDropsSecrets(t *testing.T) {
	m := newFixtureADMP(t)
	ports, err := docClient(t, m, "", validLogin, nil).ListAccessPorts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []AccessPort{
		{ID: 301, NodeID: 3, NodeName: "olt-traunstein-01", Protocol: "KEYMILE_SSH", UnitIndex: 2, PortIndex: 1},
		{ID: 302, NodeID: 3, NodeName: "olt-traunstein-01", Protocol: "KEYMILE_SSH", UnitIndex: 2, PortIndex: 2},
		{ID: 401, NodeID: 4, NodeName: "dslam-siegsdorf", UnitIndex: 1, PortIndex: 7},
	}
	if !reflect.DeepEqual(ports, want) {
		t.Errorf("ports %+v, want %+v", ports, want)
	}
	noCanary(t, "the inventory", ports)
	typ := reflect.TypeFor[AccessPort]()
	for i := range typ.NumField() {
		name := strings.ToLower(typ.Field(i).Name)
		for _, secret := range []string{"url", "user", "password", "iv", "snmp", "organization", "extra"} {
			if name == secret || strings.Contains(name, "password") || strings.Contains(name, "user") {
				t.Errorf("AccessPort has the member %s", typ.Field(i).Name)
			}
		}
	}
	if n := m.count("GET " + fixtureBase + accessNodePickerPath); n != 1 {
		t.Errorf("picker reads %d", n)
	}
}

// TestListPortServiceIDsDecodesNumbers: the services of a port are whole
// numbers; a fraction, a string or a number beyond 2^53 is decode.
func TestListPortServiceIDsDecodesNumbers(t *testing.T) {
	m := newFixtureADMP(t)
	c := docClient(t, m, "", validLogin, nil)
	ctx := context.Background()
	ids, err := c.ListPortServiceIDs(ctx, 301)
	if err != nil || !reflect.DeepEqual(ids, []int64{815, 816}) {
		t.Fatalf("ids %v %v", ids, err)
	}
	if n := m.count("GET " + fixtureBase + "/V1/infrastructure/port/301/dienstIds"); n != 1 {
		t.Errorf("hits %v", m.hits)
	}
	for _, body := range []string{`[815.5]`, `["815"]`, `[18014398509481984]`, `[0]`} {
		m.set("getV1InfrastructurePort:portIdDienstIds", fixture{Status: 200, Body: json.RawMessage(body)})
		if _, err = c.ListPortServiceIDs(ctx, 301); ClassOf(err) != ClassDecode {
			t.Errorf("%s: %v, want decode", body, err)
		}
	}
	m.set("getV1InfrastructurePort:portIdDienstIds", fixture{Status: 200, Body: json.RawMessage(`[]`)})
	if ids, err = c.ListPortServiceIDs(ctx, 301); err != nil || len(ids) != 0 {
		t.Errorf("empty list: %v %v", ids, err)
	}
}

// TestUndocumentedShapeIsDecodeError: answers of the picker and of
// dienstIds in another form than assumed (A-86) are the class decode and
// count reticora_admp_undocumented_shape_total{operation}.
func TestUndocumentedShapeIsDecodeError(t *testing.T) {
	m := newFixtureADMP(t)
	c := docClient(t, m, "", validLogin, nil)
	ctx := context.Background()
	picker := testutil.ToFloat64(undocumentedShape.WithLabelValues(opAccessNodePicker))
	services := testutil.ToFloat64(undocumentedShape.WithLabelValues(opPortServices))
	for _, body := range []string{`{}`, `[{"id":1}]`, `[{"id":1,"name":"n","modules":[{"unitIndex":1,"ports":[{"id":"x","portIndex":1}]}]}]`,
		`[{"id":1,"name":"n","modules":[{"ports":[]}]}]`, `"text"`} {
		m.set("getV1InfrastructureAccessnodepicker", fixture{Status: 200, Body: json.RawMessage(body)})
		if _, err := c.ListAccessPorts(ctx); ClassOf(err) != ClassDecode {
			t.Errorf("picker %s: %v, want decode", body, err)
		}
	}
	m.set("getV1InfrastructurePort:portIdDienstIds", fixture{Status: 200, Body: json.RawMessage(`{"ids":[1]}`)})
	if _, err := c.ListPortServiceIDs(ctx, 1); ClassOf(err) != ClassDecode {
		t.Errorf("dienstIds object: %v, want decode", err)
	}
	if got := testutil.ToFloat64(undocumentedShape.WithLabelValues(opAccessNodePicker)) - picker; got != 5 {
		t.Errorf("undocumented picker shapes counted %v, want 5", got)
	}
	if got := testutil.ToFloat64(undocumentedShape.WithLabelValues(opPortServices)) - services; got != 1 {
		t.Errorf("undocumented dienstIds shapes counted %v, want 1", got)
	}
}

// TestGetServiceDecodesFixture: GET /V1/dienst/{id} gives id, connection,
// template id and name and the six times as sent; the embedded connection
// (contract, address) and the properties are dropped.
func TestGetServiceDecodesFixture(t *testing.T) {
	m := newFixtureADMP(t)
	s, err := docClient(t, m, "", validLogin, nil).GetService(context.Background(), 815)
	if err != nil {
		t.Fatal(err)
	}
	f := func(v float64) *float64 { return &v }
	three := int64(3)
	want := &Service{ID: 815, ConnectionID: 4711, TemplateID: &three, TemplateName: "FTTH 1000", Booked: f(1700000000000),
		ProvisioningPlanned: f(1700100000), Provisioned: f(1700200000)}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("service %+v, want %+v", s, want)
	}
	noCanary(t, "the service", s)
	if n := m.count("GET " + fixtureBase + "/V1/dienst/815"); n != 1 {
		t.Errorf("hits %v", m.hits)
	}
	// Without template and with a fraction as id: decode.
	m.set("getV1Dienst:id", fixture{Status: 200, Body: json.RawMessage(`{"id":815.5,"anschlussId":1}`)})
	if _, err = docClient(t, m, "", validLogin, nil).GetService(context.Background(), 815); ClassOf(err) != ClassDecode {
		t.Errorf("fractional id: %v, want decode", err)
	}
	m.set("getV1Dienst:id", fixture{Status: 200, Body: json.RawMessage(`{"id":815,"anschlussId":1,"templateId":null}`)})
	s, err = docClient(t, m, "", validLogin, nil).GetService(context.Background(), 815)
	if err != nil || s.TemplateID != nil || s.TemplateName != "" {
		t.Errorf("without template: %+v %v", s, err)
	}
}

// TestGetSessionDropsLdapFields: GET /V1/user/authentication gives the
// user and organization ids only; the LDAP data of the service account
// are dropped.
func TestGetSessionDropsLdapFields(t *testing.T) {
	m := newFixtureADMP(t)
	s, err := docClient(t, m, "", validLogin, nil).GetSession(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if *s != (Session{UserID: 42, OrganizationID: 7}) {
		t.Errorf("session %+v", s)
	}
	noCanary(t, "the session", s)
	if typ := reflect.TypeFor[Session](); typ.NumField() != 2 {
		t.Errorf("Session has %d members, want the two ids", typ.NumField())
	}
}
