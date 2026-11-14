package credential

import (
	"context"
	"net/netip"
	"slices"
	"testing"
)

// staticGroups is a DeviceGroupSource of the tests: fixed memberships.
type staticGroups map[string][]string

func (staticGroups) MembershipQuery() string { return NoDeviceGroups{}.MembershipQuery() }

func (g staticGroups) Memberships(_ context.Context, _ string, ciIDs []string) (map[string][]string, error) {
	out := map[string][]string{}
	for _, id := range ciIDs {
		out[id] = g[id]
	}
	return out, nil
}

// resolveFixture is an organization with a CI of client c1 in subnet
// 10.0.0.0/16 (VRF default) of a scope, and one SNMP credential per level.
type resolveFixture struct {
	data   ResolveData
	ci     ResolveCI
	groups staticGroups
}

func snmpCred(id, client string) Credential {
	return Credential{ID: id, ClientID: client, Name: "name-" + id, Kind: KindSNMPv2c}
}

func bindingOf(id, credID, kind, target string, priority int) Binding {
	return Binding{ID: id, CredentialID: credID, TargetKind: kind, TargetID: target, Namespace: "snmp", Priority: priority}
}

func newResolveFixture() *resolveFixture {
	return &resolveFixture{
		data: ResolveData{
			OrganizationID: "org",
			Credentials: []Credential{
				snmpCred("cred-ci", "c1"), snmpCred("cred-group", ""), snmpCred("cred-subnet", "c1"),
				snmpCred("cred-scope", "c1"), snmpCred("cred-client", "c1"), snmpCred("cred-org", ""),
			},
			Bindings: []Binding{
				bindingOf("b-ci", "cred-ci", TargetCI, "ci-1", 100),
				bindingOf("b-group", "cred-group", TargetDeviceGroup, "group-1", 100),
				bindingOf("b-subnet", "cred-subnet", TargetSubnet, "subnet-16", 100),
			},
			Subnets: []ResolveSubnet{{ID: "subnet-16", Prefix: netip.MustParsePrefix("10.0.0.0/16")}},
		},
		ci: ResolveCI{ID: "ci-1", ClientID: "c1", ManagementIP: netip.MustParseAddr("10.0.1.5"),
			Scope: &ResolveScope{ID: "scope-1", ClientID: "c1", CredentialIDs: []string{"cred-scope"}}},
		groups: staticGroups{"ci-1": {"group-1"}},
	}
}

func (f *resolveFixture) resolve(t *testing.T) []EffectiveCredential {
	t.Helper()
	res, err := Resolve(context.Background(), &f.data, f.groups, []ResolveCI{f.ci})
	if err != nil {
		t.Fatal(err)
	}
	return res[f.ci.ID]
}

func only(t *testing.T, list []EffectiveCredential, ns string) EffectiveCredential {
	t.Helper()
	for _, e := range list {
		if e.Namespace == ns {
			return e
		}
	}
	t.Fatalf("no effective credential for namespace %s in %+v", ns, list)
	return EffectiveCredential{}
}

func removeBinding(f *resolveFixture, id string) {
	f.data.Bindings = slices.DeleteFunc(f.data.Bindings, func(b Binding) bool { return b.ID == id })
}

// TestResolveOrder covers CRD-04 and E-219 (WP-891): the levels win in the
// order CI > device group (DeviceGroupSource) > subnet > scope > client >
// org; removing the winner of a level hands over to the next one, and the
// overridden candidates follow in resolution order.
func TestResolveOrder(t *testing.T) {
	f := newResolveFixture()
	steps := []struct {
		origin, cred, source string
		drop                 func(*resolveFixture)
	}{
		{OriginCI, "cred-ci", "b-ci", func(f *resolveFixture) { removeBinding(f, "b-ci") }},
		{OriginDeviceGroup, "cred-group", "b-group", func(f *resolveFixture) { removeBinding(f, "b-group") }},
		{OriginSubnet, "cred-subnet", "b-subnet", func(f *resolveFixture) { removeBinding(f, "b-subnet") }},
		{OriginScope, "cred-scope", "scope-1", func(f *resolveFixture) {
			// A scope without list: the client's and the org-wide
			// credentials; only cred-client stays of client c1.
			f.ci.Scope.CredentialIDs = nil
			f.data.Credentials = slices.DeleteFunc(f.data.Credentials, func(c Credential) bool {
				return c.ClientID == "c1" && c.ID != "cred-client"
			})
		}},
		{OriginClient, "cred-client", "c1", func(f *resolveFixture) {
			f.data.Credentials = slices.DeleteFunc(f.data.Credentials, func(c Credential) bool { return c.ID != "cred-org" })
		}},
		{OriginOrg, "cred-org", "", nil},
	}
	first := f.resolve(t)
	snmp := only(t, first, "snmp")
	var order []string
	for _, o := range snmp.Overridden {
		order = append(order, o.Origin)
	}
	// With the scope's list the levels client and org do not apply (E-67).
	if want := []string{OriginDeviceGroup, OriginSubnet, OriginScope}; !slices.Equal(order, want) {
		t.Errorf("overridden origins %v, want %v", order, want)
	}
	for _, step := range steps {
		got := only(t, f.resolve(t), "snmp")
		if got.Origin != step.origin || got.Credential.ID != step.cred || got.SourceID != step.source {
			t.Fatalf("level %s: effective %s from %s (source %q), want %s (source %q)", step.origin, got.Credential.ID, got.Origin,
				got.SourceID, step.cred, step.source)
		}
		if got.Credential.Name == "" || got.Credential.Kind != KindSNMPv2c {
			t.Errorf("level %s: credential reference %+v lacks name or kind", step.origin, got.Credential)
		}
		if step.drop != nil {
			step.drop(f)
		}
	}
}

// TestResolveEmptyDeviceGroupSource covers the source of WP-891 until
// WP-935: without memberships (NoDeviceGroups, nil) a device-group binding
// offers nothing and the subnet wins.
func TestResolveEmptyDeviceGroupSource(t *testing.T) {
	for name, groups := range map[string]DeviceGroupSource{"NoDeviceGroups": NoDeviceGroups{}, "nil": nil} {
		f := newResolveFixture()
		removeBinding(f, "b-ci")
		res, err := Resolve(context.Background(), &f.data, groups, []ResolveCI{f.ci})
		if err != nil {
			t.Fatal(err)
		}
		got := only(t, res[f.ci.ID], "snmp")
		if got.Origin != OriginSubnet {
			t.Errorf("%s: effective origin %s, want subnet", name, got.Origin)
		}
		for _, o := range got.Overridden {
			if o.Origin == OriginDeviceGroup {
				t.Errorf("%s: the empty source offered a device-group candidate", name)
			}
		}
	}
	if q := (NoDeviceGroups{}).MembershipQuery(); q == "" {
		t.Error("NoDeviceGroups has no membership query")
	}
}

// TestResolvePriorityWithinLevel covers the order within a level: the
// smallest priority wins, at equal priority the smallest binding ID.
func TestResolvePriorityWithinLevel(t *testing.T) {
	f := newResolveFixture()
	f.data.Credentials = append(f.data.Credentials, snmpCred("cred-ci-2", ""), snmpCred("cred-ci-3", ""))
	f.data.Bindings = append(f.data.Bindings,
		bindingOf("b-ci-2", "cred-ci-2", TargetCI, "ci-1", 10),
		bindingOf("b-ci-3", "cred-ci-3", TargetCI, "ci-1", 10))
	got := only(t, f.resolve(t), "snmp")
	if got.Credential.ID != "cred-ci-2" || got.Priority == nil || *got.Priority != 10 {
		t.Fatalf("effective %s (priority %v), want cred-ci-2 with priority 10", got.Credential.ID, got.Priority)
	}
	if o := got.Overridden[0]; o.Credential.ID != "cred-ci-3" || o.Origin != OriginCI {
		t.Errorf("first overridden %s from %s, want cred-ci-3 from ci (same priority, larger binding id)", o.Credential.ID, o.Origin)
	}
	if o := got.Overridden[1]; o.Credential.ID != "cred-ci" {
		t.Errorf("second overridden %s, want cred-ci (priority 100)", o.Credential.ID)
	}
}

// TestResolveNamespacesAreSeparate covers the resolution per namespace: a
// CI binding for snmp does not displace the scope's ssh credential.
func TestResolveNamespacesAreSeparate(t *testing.T) {
	f := newResolveFixture()
	f.data.Credentials = append(f.data.Credentials, Credential{ID: "cred-ssh", ClientID: "c1", Name: "ssh", Kind: KindSSHPassword})
	f.ci.Scope.CredentialIDs = append(f.ci.Scope.CredentialIDs, "cred-ssh")
	list := f.resolve(t)
	if len(list) != 2 || list[0].Namespace != "snmp" || list[1].Namespace != "ssh" {
		t.Fatalf("namespaces %+v, want snmp and ssh", list)
	}
	if ssh := only(t, list, "ssh"); ssh.Origin != OriginScope || ssh.Credential.ID != "cred-ssh" || len(ssh.Overridden) != 0 {
		t.Errorf("ssh: %+v, want cred-ssh from the scope without overridden candidates", ssh)
	}
	if snmp := only(t, list, "snmp"); snmp.Origin != OriginCI {
		t.Errorf("snmp: origin %s, want ci", snmp.Origin)
	}
}

// TestResolveNarrowestSubnet covers the subnet level: the narrowest subnet
// containing the management address wins regardless of priority; a subnet
// of another VRF than the CI's scope and one not containing the address
// offer nothing.
func TestResolveNarrowestSubnet(t *testing.T) {
	f := newResolveFixture()
	removeBinding(f, "b-ci")
	removeBinding(f, "b-group")
	f.data.Credentials = append(f.data.Credentials, snmpCred("cred-24", ""), snmpCred("cred-vrf", ""), snmpCred("cred-other", ""))
	f.data.Subnets = append(f.data.Subnets,
		ResolveSubnet{ID: "subnet-24", Prefix: netip.MustParsePrefix("10.0.1.0/24")},
		ResolveSubnet{ID: "subnet-vrf", VRFID: "vrf-2", Prefix: netip.MustParsePrefix("10.0.1.0/28")},
		ResolveSubnet{ID: "subnet-other", Prefix: netip.MustParsePrefix("10.0.2.0/24")})
	f.data.Bindings = append(f.data.Bindings,
		bindingOf("b-24", "cred-24", TargetSubnet, "subnet-24", 500),
		bindingOf("b-vrf", "cred-vrf", TargetSubnet, "subnet-vrf", 1),
		bindingOf("b-other", "cred-other", TargetSubnet, "subnet-other", 1))
	got := only(t, f.resolve(t), "snmp")
	if got.Credential.ID != "cred-24" || got.SourceID != "b-24" {
		t.Fatalf("effective %s (binding %s), want cred-24 of the /24", got.Credential.ID, got.SourceID)
	}
	for _, o := range got.Overridden {
		if o.Credential.ID == "cred-vrf" || o.Credential.ID == "cred-other" {
			t.Errorf("candidate %s of another VRF or subnet offered", o.Credential.ID)
		}
	}
	if o := got.Overridden[0]; o.Credential.ID != "cred-subnet" {
		t.Errorf("first overridden %s, want cred-subnet of the /16", o.Credential.ID)
	}
	// In the scope's VRF vrf-2 only the /28 applies.
	f.ci.Scope.VRFID = "vrf-2"
	if got = only(t, f.resolve(t), "snmp"); got.Credential.ID != "cred-vrf" {
		t.Errorf("CI in VRF vrf-2: effective %s, want cred-vrf", got.Credential.ID)
	}
}

// TestResolveClientBoundary covers AK2: a credential of another client acts
// on no level, also not through a device group or as an org-level
// candidate.
func TestResolveClientBoundary(t *testing.T) {
	f := newResolveFixture()
	f.data.Credentials = append(f.data.Credentials, snmpCred("cred-foreign", "c2"))
	f.data.Bindings = []Binding{bindingOf("b-foreign", "cred-foreign", TargetDeviceGroup, "group-1", 0)}
	f.ci.Scope = nil
	got := only(t, f.resolve(t), "snmp")
	if got.Credential.ID == "cred-foreign" {
		t.Fatal("a credential of client c2 resolved for a CI of client c1")
	}
	for _, o := range got.Overridden {
		if o.Credential.ID == "cred-foreign" {
			t.Fatal("a credential of client c2 is a candidate of a CI of client c1")
		}
	}
	if got.Origin != OriginClient {
		t.Errorf("CI without scope: origin %s, want client", got.Origin)
	}
}
