package ci_test

import (
	"context"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestCIListSearchHostnameAndIP covers SRC-02 and UI-15 (WP-859): search=
// of the CI list, which the object picker sends for a relationship target,
// matches the name, the host name and the management IP as a substring
// without regard to case; the IP is matched without its prefix length
// (host()). A CI outside the principal's scope (another client, another
// organization) is no hit.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCIListSearchHostnameAndIP(t *testing.T) {
	f := scopetest.Seed(t, "cf")
	bg := context.Background()
	place := func(id, hostname, ip string) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET hostname = $2, management_ip = $3::inet WHERE id = $1`, id, hostname, ip); err != nil {
			t.Fatalf("set hostname and IP of %s: %v", id, err)
		}
	}
	core := f.CI(t, f.OrgA, f.Client1, "wp859-core-switch")
	place(core, "SW-Core-01", "10.85.9.17")
	access := f.CI(t, f.OrgA, f.Client1, "wp859-access")
	place(access, "ap-lager", "192.168.85.9/24")
	foreign := f.CI(t, f.OrgA, f.Client2, "wp859-foreign")
	place(foreign, "sw-core-02", "10.85.9.18")
	otherOrg := f.CI(t, f.OrgB, "", "wp859-other-org")
	place(otherOrg, "sw-core-03", "10.85.9.19")

	repo := ci.NewPGRepository(f.App)
	search := func(ctx context.Context, term string) []string {
		t.Helper()
		items, total, err := repo.List(ctx, f.OrgA, &ci.FilterParams{Search: term}, api.PaginationParams{Limit: 50})
		if err != nil {
			t.Fatalf("search %q: %v", term, err)
		}
		if total != len(items) {
			t.Errorf("search %q: total %d, %d items", term, total, len(items))
		}
		ids := make([]string, 0, len(items))
		for i := range items {
			ids = append(ids, items[i].ID)
		}
		slices.Sort(ids)
		return ids
	}
	sorted := func(ids ...string) []string {
		out := slices.Clone(ids)
		slices.Sort(out)
		return out
	}

	client1 := f.ClientCtx(f.Client1)
	for _, tc := range []struct {
		name, term string
		want       []string
	}{
		{"name, other case", "WP859-CORE", sorted(core)},
		{"host name, other case", "sw-core", sorted(core)},
		{"host name substring", "lager", sorted(access)},
		{"IP prefix", "10.85.", sorted(core)},
		{"IP substring", "85.9", sorted(core, access)},
		{"IP suffix", ".9.17", sorted(core)},
		{"no prefix length", "/24", sorted()},
		{"no hit", "wp859-none", sorted()},
	} {
		if got := search(client1, tc.term); !slices.Equal(got, tc.want) {
			t.Errorf("client 1, %s (%q): %v, want %v", tc.name, tc.term, got, tc.want)
		}
	}

	// An org-wide principal sees both clients, never organization B.
	orgWide := f.OrgCtx(f.OrgA)
	if got, want := search(orgWide, "SW-CORE"), sorted(core, foreign); !slices.Equal(got, want) {
		t.Errorf("org-wide, host name: %v, want %v (not the CI of organization B)", got, want)
	}
	if got, want := search(orgWide, "10.85.9."), sorted(core, foreign); !slices.Equal(got, want) {
		t.Errorf("org-wide, IP prefix: %v, want %v (not the CI of organization B)", got, want)
	}
}
