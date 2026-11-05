package discovery

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/wire"
)

// neighborFinder is a repository whose index query returns the ids listed
// for the management IP of the query and fails for the IPs in failing.
type neighborFinder struct {
	*MemoryRepository
	byIP    map[string][]string
	failing map[string]bool
	queries *int
}

func (f neighborFinder) IdentityCandidates(_ context.Context, _ string, q *IdentityQuery) ([]string, error) {
	*f.queries++
	if f.failing[q.ManagementIP] {
		return nil, errors.New("boom")
	}
	return f.byIP[q.ManagementIP], nil
}

func (neighborFinder) DefaultVRF(context.Context, string) (string, error) { return "", nil }

// countingReader counts the reads of a CI repository with a batch read,
// which returns the CIs in reverse order as PostgreSQL may return them in
// any.
type countingReader struct {
	*ci.MemoryRepository
	single, batch *int
}

func (r countingReader) GetByID(ctx context.Context, orgID, id string) (*ci.Item, error) {
	*r.single++
	return r.MemoryRepository.GetByID(ctx, orgID, id)
}

func (r countingReader) GetByIDs(ctx context.Context, orgID string, ids []string) ([]ci.Item, error) {
	*r.batch++
	items, err := r.MemoryRepository.GetByIDs(ctx, orgID, ids)
	slices.Reverse(items)
	return items, err
}

// TestNeighborCandidatesLoad pins how neighborCandidates reads the CIs of
// the index queries: in the order they were first named, each once, a key
// queried once, a failed query, a missing and a deleted CI skipped; the
// same with the batch read (one read), without it and with a failing one.
func TestNeighborCandidatesLoad(t *testing.T) {
	ctx := context.Background()
	const org = "org-1"
	cis := ci.NewMemoryRepository()
	ids := map[string]string{}
	for _, name := range []string{"a", "b", "c", "d"} {
		item := &ci.Item{OrganizationID: org, CITypeID: "server", Name: name, Status: "active"}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		ids[name] = item.ID
	}
	if err := cis.Delete(ctx, org, ids["d"]); err != nil {
		t.Fatal(err)
	}
	byIP := map[string][]string{
		"10.0.0.1": {ids["c"], ids["a"]},
		"10.0.0.2": {ids["a"], "missing-1", ids["b"]},
		"10.0.0.4": {ids["d"]},
	}
	items := []IngestItem{
		{Relationships: []wire.IngestRelationship{{TargetIP: "10.0.0.1"}, {TargetIP: "10.0.0.3"}}},
		{Relationships: []wire.IngestRelationship{{TargetIP: "10.0.0.2"}, {TargetIP: "10.0.0.1"}, {TargetIP: "10.0.0.4"}, {TargetIP: "10.0.0.3"}}},
	}
	var single, batch int
	readers := map[string]ci.Repository{
		"batch":        cis,
		"per id":       recordReader{cis},
		"broken batch": brokenBatchReader{cis},
		"counted":      countingReader{cis, &single, &batch},
	}
	for name, reader := range readers {
		queries := 0
		h := NewHandler(neighborFinder{NewMemoryRepository(), byIP, map[string]bool{"10.0.0.3": true}, &queries}, reader)
		got := h.neighborCandidates(ctx, org, items)
		var names []string
		for i := range got {
			names = append(names, got[i].Name)
		}
		if want := []string{"c", "a", "b"}; !slices.Equal(names, want) {
			t.Errorf("%s: candidates = %v, want %v", name, names, want)
		}
		// 10.0.0.1 is queried once; the failed 10.0.0.3 again.
		if queries != 5 {
			t.Errorf("%s: %d index queries, want 5", name, queries)
		}
	}
	// One batch read for the CIs of all queries (NFR-03).
	if single != 0 || batch != 1 {
		t.Errorf("counted reader: %d reads by id, %d batch reads; want 0 and 1", single, batch)
	}
}
