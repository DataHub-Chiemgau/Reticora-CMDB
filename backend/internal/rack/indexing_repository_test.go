package rack

import (
	"context"
	"errors"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// recordingIndexer records the CIs it is asked to index again.
type recordingIndexer struct {
	cis  []string
	fail error
}

func (x *recordingIndexer) ReindexPlacement(_ context.Context, _, ciID string) error {
	x.cis = append(x.cis, ciID)
	return x.fail
}

// TestIndexingRepositoryReindexesPlacements covers WP-346 (TEC-12): mount,
// move and unmount index the placed CI again after the write; a failing
// index is counted and never fails the placement; failed writes index
// nothing.
func TestIndexingRepositoryReindexesPlacements(t *testing.T) {
	ctx := context.Background()
	idx := &recordingIndexer{}
	repo := NewIndexingRepository(NewMemoryRepository(), idx)
	rk := &Rack{OrganizationID: "org", RoomID: "room", Name: "r1", HeightUnits: 42}
	if err := repo.CreateRack(ctx, rk); err != nil {
		t.Fatal(err)
	}
	m := &RackMount{OrganizationID: "org", RackID: rk.ID, CIID: "ci-1", PositionU: 1, HeightU: 1, Face: "front"}
	if err := repo.CreateMount(ctx, m); err != nil {
		t.Fatal(err)
	}
	pos := 5
	if _, err := repo.UpdateMount(ctx, "org", m.ID, UpdateMountRequest{PositionU: &pos}); err != nil {
		t.Fatal(err)
	}
	idx.fail = errors.New("remote down")
	before := testutil.ToFloat64(SearchReindexFailures)
	if err := repo.DeleteMount(ctx, "org", m.ID); err != nil {
		t.Fatalf("unmount with a failing index: %v, want success", err)
	}
	if got := testutil.ToFloat64(SearchReindexFailures) - before; got != 1 {
		t.Errorf("failures counted: %v, want 1", got)
	}
	if len(idx.cis) != 3 || idx.cis[0] != "ci-1" || idx.cis[1] != "ci-1" || idx.cis[2] != "ci-1" {
		t.Errorf("indexed %v, want ci-1 after mount, move and unmount", idx.cis)
	}

	// A refused write indexes nothing.
	if err := repo.CreateMount(ctx, &RackMount{OrganizationID: "org", RackID: "missing", CIID: "ci-2", PositionU: 1, HeightU: 1, Face: "front"}); err == nil {
		t.Fatal("mount into a missing rack: want an error")
	}
	if err := repo.DeleteMount(ctx, "org", m.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unmount: %v, want ErrNotFound", err)
	}
	if len(idx.cis) != 3 {
		t.Errorf("refused writes indexed %v", idx.cis[3:])
	}
	if NewIndexingRepository(NewMemoryRepository(), nil) == nil {
		t.Error("nil indexer: want the repository unchanged")
	}
}
