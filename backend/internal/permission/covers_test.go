package permission

import (
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// TestCovers pins the scope rule of notification recipients (NTF-03,
// WP-138).
func TestCovers(t *testing.T) {
	read := []identity.Permission{identity.PermCIRead}
	orgWide := []identity.Grant{{Permissions: read}}
	client1 := []identity.Grant{{Permissions: read, Clients: []string{"c1"}}}
	site1 := []identity.Grant{{Permissions: read, Sites: []string{"s1"}}}
	clientAndSite := []identity.Grant{{Permissions: read, Clients: []string{"c1"}, Sites: []string{"s1"}}}
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
		"client and site, both":      {clientAndSite, "c1", "s1", true},
		"client and site, one":       {clientAndSite, "c1", "s2", false},
		"other permission":           {other, "", "", false},
		"no grant":                   {nil, "", "", false},
		"second grant covers": {append(append([]identity.Grant{}, client1...),
			identity.Grant{Permissions: read, Sites: []string{"s5"}}), "c3", "s5", true},
	} {
		if got := Covers(tc.grants, identity.PermCIRead, tc.client, tc.site); got != tc.want {
			t.Errorf("%s: %v, want %v", name, got, tc.want)
		}
	}
}
