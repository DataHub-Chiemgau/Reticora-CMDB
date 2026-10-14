package rack_test

import (
	"fmt"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
)

// TestTST01FaceAwareExclusion is the rack part of the TST-01 matrix
// (WP-202) against PostgreSQL: the exclusion constraint rack_mount_no_overlap
// lets a front and a rear mount share height units, refuses two mounts on
// the same face, and treats a mount on both faces as overlapping either.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestTST01FaceAwareExclusion(t *testing.T) {
	f := scopetest.Seed(t, "c2")
	ctx := f.OrgCtx(f.OrgA)
	tree := locations.NewPGRepository(f.App)
	parent := ""
	for _, kind := range []locations.Kind{locations.KindSite, locations.KindBuilding, locations.KindRoom, locations.KindRack} {
		req := locations.CreateRequest{Kind: kind, ParentID: parent, Name: "tst01-" + string(kind)}
		if kind == locations.KindSite {
			req.ClientID = f.Client1
		}
		loc, err := tree.Create(ctx, f.OrgA, req)
		if err != nil {
			t.Fatalf("create %s: %v", kind, err)
		}
		parent = loc.ID
	}
	rackID := parent
	repo := rack.NewPGRepository(f.App)
	n := 0
	mount := func(face string, position, height int) error {
		n++
		ciID := f.CI(t, f.OrgA, f.Client1, fmt.Sprintf("tst01-rack-%d", n))
		return repo.CreateMount(ctx, &rack.RackMount{OrganizationID: f.OrgA, RackID: rackID, CIID: ciID,
			PositionU: position, HeightU: height, Face: face})
	}

	cases := []struct {
		name           string
		face           string
		position, size int
		allowed        bool
	}{
		{"front mount on free units", "front", 10, 2, true},
		{"rear mount on the same units as the front mount", "rear", 10, 2, true},
		{"second front mount overlapping the first", "front", 11, 1, false},
		{"second rear mount overlapping the first", "rear", 10, 1, false},
		{"mount on both faces over a front mount", "both", 11, 2, false},
		{"mount on both faces on free units", "both", 20, 2, true},
		{"front mount inside the both-face mount", "front", 21, 1, false},
		{"rear mount inside the both-face mount", "rear", 20, 1, false},
		{"front mount directly above the both-face mount", "front", 22, 1, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := mount(c.face, c.position, c.size)
			if c.allowed && err != nil {
				t.Fatalf("refused: %v", err)
			}
			if !c.allowed && err == nil {
				t.Fatal("overlapping mount accepted")
			}
		})
	}
}
