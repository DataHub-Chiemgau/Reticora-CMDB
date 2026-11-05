package discovery

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
)

// fixedCandidates is a repository whose index query returns ids.
type fixedCandidates struct {
	*MemoryRepository
	ids []string
}

func (f fixedCandidates) IdentityCandidates(context.Context, string, *IdentityQuery) ([]string, error) {
	return f.ids, nil
}

func (fixedCandidates) DefaultVRF(context.Context, string) (string, error) { return "", nil }

// recordReader hides the batch read of a CI repository.
type recordReader struct{ ci.Repository }

// brokenBatchReader fails the batch read.
type brokenBatchReader struct{ *ci.MemoryRepository }

func (brokenBatchReader) GetByIDs(context.Context, string, []string) ([]ci.Item, error) {
	return nil, errors.New("boom")
}

// TestIdentityCandidatesLoad pins how identityCandidates reads the CIs of
// the index query: in its order, with and without a batch read, a missing
// CI failing the record, and an empty non-nil result without candidates.
func TestIdentityCandidatesLoad(t *testing.T) {
	ctx := context.Background()
	const org = "org-1"
	cis := ci.NewMemoryRepository()
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		item := &ci.Item{OrganizationID: org, CITypeID: "server", Name: name, Status: "active"}
		if err := cis.Create(ctx, item); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, item.ID)
	}
	item := &IngestItem{SerialNumber: "SN-12345"}
	names := func(items []ci.Item) []string {
		var out []string
		for i := range items {
			out = append(out, items[i].Name)
		}
		return out
	}
	readers := map[string]ci.Repository{"batch": cis, "per id": recordReader{cis}}
	for readerName, reader := range readers {
		for _, c := range []struct {
			name    string
			ids     []string
			want    []string
			wantErr string
		}{
			{"index order", []string{ids[2], ids[0], ids[1]}, []string{"c", "a", "b"}, ""},
			{"one", []string{ids[1]}, []string{"b"}, ""},
			{"missing", []string{ids[0], "missing-1", "missing-2"}, nil, map[string]string{
				"batch":  "load candidate missing-1: not found",
				"per id": "load candidate missing-1: " + ci.ErrNotFound.Error(),
			}[readerName]},
		} {
			h := NewHandler(fixedCandidates{NewMemoryRepository(), c.ids}, reader)
			got, err := h.identityCandidates(ctx, org, item)
			if c.wantErr != "" {
				if err == nil || err.Error() != c.wantErr || got != nil {
					t.Errorf("%s/%s: = %v, %v; want error %q", readerName, c.name, names(got), err, c.wantErr)
				}
				if readerName == "per id" && !errors.Is(err, ci.ErrNotFound) {
					t.Errorf("%s/%s: error %v does not wrap ci.ErrNotFound", readerName, c.name, err)
				}
				continue
			}
			if err != nil || !slices.Equal(names(got), c.want) {
				t.Errorf("%s/%s: = %v, %v; want %v", readerName, c.name, names(got), err, c.want)
			}
		}
		for _, none := range [][]string{nil, {}} {
			h := NewHandler(fixedCandidates{NewMemoryRepository(), none}, reader)
			got, err := h.identityCandidates(ctx, org, item)
			if err != nil || got == nil || len(got) != 0 {
				t.Errorf("%s/no candidates (%#v): = %#v, %v; want an empty non-nil slice", readerName, none, got, err)
			}
		}
	}
	h := NewHandler(fixedCandidates{NewMemoryRepository(), ids}, brokenBatchReader{cis})
	if _, err := h.identityCandidates(ctx, org, item); err == nil || err.Error() != "load candidates: boom" {
		t.Errorf("broken batch read: error %v, want load candidates: boom", err)
	}
	// Without an identifying value no index query runs.
	if got, err := h.identityCandidates(ctx, org, &IngestItem{}); got != nil || err != nil {
		t.Errorf("no identity: = %#v, %v; want nil, nil", got, err)
	}
}
