package vulnfeed_test

import (
	"context"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// TestVulnNVDCoverageAndKEVOnPostgreSQL covers WP-1119 AK4 and AK5 on
// PostgreSQL: CSAFCovered names the (vendor, product) pairs of the CSAF
// advisories of a CVE, RemoveCoveredNVDRanges deletes only the covered NVD
// ranges, SetKEV sets and clears the flag, and a re-import keeps a set
// flag until the next SetKEV.
//
// Retest:
//
//	cd backend && go test -count=1 -run TestVulnNVDCoverageAndKEVOnPostgreSQL ./internal/vulnfeed/
func TestVulnNVDCoverageAndKEVOnPostgreSQL(t *testing.T) {
	e := newCatalogEnv(t)
	ctx := context.Background()
	clean := func() { _, _ = e.f.Admin.Exec(ctx, `DELETE FROM vuln_source WHERE id = 'nvd'`) }
	clean()
	t.Cleanup(clean)
	if err := e.repo.UpdateSource(ctx, &vulnfeed.Source{ID: vulnfeed.KindNVD, Kind: vulnfeed.KindNVD}); err != nil {
		t.Fatal(err)
	}
	cve := "CVE-2026-77" + e.prefix[len(e.prefix)-3:]
	csaf := &vulnfeed.Advisory{ID: e.prefix + "-csaf", SourceID: e.source, CVEIDs: []string{cve}}
	if err := e.repo.ReplaceAdvisory(ctx, csaf, []vulnfeed.Affected{{Vendor: "MikroTik", Product: "SwitchOS", VersionExact: "7.1"}}); err != nil {
		t.Fatal(err)
	}
	covered, err := e.repo.CSAFCovered(ctx, []string{cve, "CVE-1999-0001"})
	if err != nil {
		t.Fatal(err)
	}
	if !covered[cve][[2]string{"mikrotik", "switchos"}] || len(covered) != 1 {
		t.Fatalf("covered %v", covered)
	}
	nvd := &vulnfeed.Advisory{ID: "nvd:" + cve, SourceID: vulnfeed.KindNVD, CVEIDs: []string{cve}}
	if rerr := e.repo.ReplaceAdvisory(ctx, nvd, []vulnfeed.Affected{
		{Vendor: "mikrotik", Product: "switchos", VersionEnd: "7.2"},
		{Vendor: "zyxel", Product: "usg_flex", VersionEnd: "5.0"},
	}); rerr != nil {
		t.Fatal(rerr)
	}
	removed, err := e.repo.RemoveCoveredNVDRanges(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := e.ranges(t, nvd.ID); removed < 1 || !slices.Equal(got, []string{"zyxel/usg_flex"}) {
		t.Fatalf("removed %d, NVD ranges %v; want only zyxel/usg_flex", removed, got)
	}
	if got := e.ranges(t, csaf.ID); !slices.Equal(got, []string{"mikrotik/switchos"}) {
		t.Fatalf("CSAF ranges %v", got)
	}

	kev := func(id string) bool {
		var v bool
		if err := e.f.Admin.QueryRow(ctx, `SELECT kev FROM vuln_advisory WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	if _, err := e.repo.SetKEV(ctx, []string{cve}); err != nil {
		t.Fatal(err)
	}
	if !kev(nvd.ID) || !kev(csaf.ID) {
		t.Fatal("SetKEV did not set the flag")
	}
	// A re-import with KEV false keeps the flag.
	if err := e.repo.ReplaceAdvisory(ctx, nvd, nil); err != nil {
		t.Fatal(err)
	}
	if !kev(nvd.ID) {
		t.Fatal("a re-import cleared the KEV flag")
	}
	// A new advisory of a flagged CVE is flagged at once.
	other := &vulnfeed.Advisory{ID: e.prefix + "-csaf-2", SourceID: e.source, CVEIDs: []string{cve}}
	if err := e.repo.ReplaceAdvisory(ctx, other, nil); err != nil {
		t.Fatal(err)
	}
	if !kev(other.ID) {
		t.Fatal("a new advisory of a KEV CVE is not flagged")
	}
	if _, err := e.repo.SetKEV(ctx, []string{"CVE-1999-0001"}); err != nil {
		t.Fatal(err)
	}
	if kev(nvd.ID) || kev(csaf.ID) || kev(other.ID) {
		t.Fatal("SetKEV did not clear the flag")
	}
}
