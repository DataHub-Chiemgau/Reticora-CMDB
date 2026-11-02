package permission

import (
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestCovers pins the scope rule of notification recipients (NTF-03,
// WP-138): the scope of the permission as the session resolves it. Site
// grants name the client of their site, as permission.AccessGrants delivers
// them (WP-390); mixed grants combine per dimension (E-70 option 1, WP-391),
// like the RLS, and a team grant restricts no object of an audience (E-11).
func TestCovers(t *testing.T) {
	read := []identity.Permission{identity.PermCIRead}
	orgWide := []identity.Grant{{Permissions: read}}
	client1 := []identity.Grant{{Permissions: read, Clients: []string{"c1"}}}
	site1 := []identity.Grant{{Permissions: read, Clients: []string{"c7"}, Sites: []string{"s1"}}}
	unmappedSite := []identity.Grant{{Permissions: read, Sites: []string{"s1"}}}
	clientAndSite := []identity.Grant{{Permissions: read, Clients: []string{"c1"}, Sites: []string{"s1"}}}
	clientPlusSite := append(append([]identity.Grant{}, client1...), clientAndSite...)
	team1 := []identity.Grant{{Permissions: read, Teams: []string{"t1"}}}
	clientPlusTeam := append(append([]identity.Grant{}, client1...), team1...)
	other := []identity.Grant{{Permissions: []identity.Permission{identity.PermAssetRead}}}
	for name, tc := range map[string]struct {
		grants       []identity.Grant
		client, site string
		want         bool
	}{
		"org-wide, org object":       {orgWide, "", "", true},
		"org-wide, client object":    {orgWide, "c2", "s9", true},
		"client grant, its client":   {client1, "c1", "", true},
		"client grant, other client": {client1, "c2", "", false},
		"client grant, org object":   {client1, "", "", false},
		"site grant, its site":       {site1, "c7", "s1", true},
		"site grant, other site":     {site1, "c7", "s2", false},
		"site grant, other client":   {site1, "c8", "s1", false},
		"site without client (E-08)": {unmappedSite, "c7", "s1", false},
		"client and site, both":      {clientAndSite, "c1", "s1", true},
		"client and site, one":       {clientAndSite, "c1", "s2", false},
		"other permission":           {other, "", "", false},
		"no grant":                   {nil, "", "", false},
		"second grant covers": {append(append([]identity.Grant{}, client1...),
			identity.Grant{Permissions: read, Clients: []string{"c3"}, Sites: []string{"s5"}}), "c3", "s5", true},
		// E-70 option 1: the site grant narrows the sites of the client grant.
		"client plus site grant, the site":        {clientPlusSite, "c1", "s1", true},
		"client plus site grant, other site":      {clientPlusSite, "c1", "s2", false},
		"client plus site grant, object w/o site": {clientPlusSite, "c1", "", false},
		// E-11: the team restricts no object of an audience, but does not
		// widen the clients of a client grant beside it.
		"team grant, client object":         {team1, "c2", "s9", true},
		"team grant, org object":            {team1, "", "", true},
		"client plus team, its client":      {clientPlusTeam, "c1", "", true},
		"client plus team, other client":    {clientPlusTeam, "c2", "", false},
		"client plus team, org object":      {clientPlusTeam, "", "", false},
		"client grant, client id uppercase": {client1, "C1", "", true},
	} {
		if got := Covers(tc.grants, identity.PermCIRead, tc.client, tc.site); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
