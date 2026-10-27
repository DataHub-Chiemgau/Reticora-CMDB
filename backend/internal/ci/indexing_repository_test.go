package ci

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

type recordingIndexer struct {
	indexed []Document
	deleted []string
	err     error
}

func (r *recordingIndexer) IndexDocument(_ context.Context, doc Document) error {
	if r.err != nil {
		return r.err
	}
	r.indexed = append(r.indexed, doc)
	return nil
}

func (r *recordingIndexer) DeleteDocument(_ context.Context, orgID, entityType, entityID string) error {
	if r.err != nil {
		return r.err
	}
	r.deleted = append(r.deleted, orgID+":"+entityType+":"+entityID)
	return nil
}

func TestIndexingRepository_CreateIndexesDocument(t *testing.T) {
	repo := NewMemoryRepository()
	indexer := &recordingIndexer{}
	svc := NewService(NewIndexingRepository(repo, indexer))

	item := &Item{
		OrganizationID: "org-1",
		CITypeID:       testSwitchType,
		Name:           "sw-core-01",
		Manufacturer:   "Cisco",
		Model:          "C9300",
		SerialNumber:   "SN-123",
		Hostname:       "sw-core-01.example.net",
	}
	if err := svc.Create(context.Background(), item); err != nil {
		t.Fatalf("create: %v", err)
	}

	if len(indexer.indexed) != 1 {
		t.Fatalf("expected 1 indexed document, got %d", len(indexer.indexed))
	}
	doc := indexer.indexed[0]
	if doc.EntityType != EntityTypeCI || doc.EntityID != item.ID || doc.OrganizationID != "org-1" {
		t.Errorf("unexpected document identity: %+v", doc)
	}
	if doc.Title != "sw-core-01" {
		t.Errorf("expected title sw-core-01, got %q", doc.Title)
	}
	if doc.URL != "/cmdb/"+item.ID {
		t.Errorf("unexpected url %q", doc.URL)
	}
	wantSummary := "sw-core-01.example.net Cisco C9300 SN-123"
	if doc.Summary != wantSummary {
		t.Errorf("expected summary %q, got %q", wantSummary, doc.Summary)
	}
}

func TestIndexingRepository_UpdateAndDelete(t *testing.T) {
	repo := NewMemoryRepository()
	indexer := &recordingIndexer{}
	svc := NewService(NewIndexingRepository(repo, indexer))

	item := &Item{OrganizationID: "org-1", CITypeID: testServerType, Name: "srv-01"}
	if err := svc.Create(context.Background(), item); err != nil {
		t.Fatalf("create: %v", err)
	}

	name := "srv-01-renamed"
	if _, err := svc.Update(context.Background(), "org-1", item.ID, UpdateRequest{Name: &name}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(indexer.indexed) != 2 {
		t.Fatalf("expected 2 indexed documents (create+update), got %d", len(indexer.indexed))
	}
	if indexer.indexed[1].Title != "srv-01-renamed" {
		t.Errorf("expected reindexed title srv-01-renamed, got %q", indexer.indexed[1].Title)
	}

	if _, err := svc.Delete(context.Background(), "org-1", item.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(indexer.deleted) != 1 || indexer.deleted[0] != "org-1:ci:"+item.ID {
		t.Errorf("unexpected deletes: %v", indexer.deleted)
	}
}

func TestIndexingRepository_IndexFailureDoesNotFailMutation(t *testing.T) {
	repo := NewMemoryRepository()
	indexer := &recordingIndexer{err: fmt.Errorf("search backend down")}
	svc := NewService(NewIndexingRepository(repo, indexer))

	item := &Item{OrganizationID: "org-1", CITypeID: testServerType, Name: "srv-02"}
	if err := svc.Create(context.Background(), item); err != nil {
		t.Fatalf("create must succeed despite index failure: %v", err)
	}
	if _, err := svc.GetByID(context.Background(), "org-1", item.ID); err != nil {
		t.Fatalf("CI must be persisted despite index failure: %v", err)
	}
	if _, err := svc.Delete(context.Background(), "org-1", item.ID); err != nil {
		t.Fatalf("delete must succeed despite index failure: %v", err)
	}
}

func TestIndexingRepository_NilIndexerPassesThrough(t *testing.T) {
	repo := NewMemoryRepository()
	if got := NewIndexingRepository(repo, nil); got != repo {
		t.Fatal("nil indexer must return the repository unchanged")
	}
}

func TestIndexingRepository_ForwardsChangeHistory(t *testing.T) {
	repo := NewMemoryRepository()
	indexer := &recordingIndexer{}
	decorated := NewIndexingRepository(repo, indexer)

	reader, ok := decorated.(ChangeReader)
	if !ok {
		t.Fatal("decorated repository must keep implementing ChangeReader")
	}
	changes, total, err := reader.ListChanges(context.Background(), "org-1", "any-id", api.PaginationParams{Limit: 10})
	if err != nil {
		t.Fatalf("list changes through decorator: %v", err)
	}
	if total != 0 || len(changes) != 0 {
		t.Errorf("expected empty forwarded history from memory repo, got %d rows (total %d)", len(changes), total)
	}
}

// TestIndexingRepository_ForwardsListNodes: the decorated repository keeps
// the node projection of topology and impact (NFR-02, WP-335), also when
// it is wrapped twice as in production; over a repository without it the
// nodes come from the batch read.
func TestIndexingRepository_ForwardsListNodes(t *testing.T) {
	ctx := context.Background()
	repo := NewMemoryRepository()
	item := &Item{OrganizationID: "org-1", CITypeID: "type-switch", Name: "sw-1", Status: "active", ClientID: "client-1",
		SiteID: "site-1", ManagementIP: "10.0.0.1", Attributes: map[string]any{"ports": 48}}
	gone := &Item{OrganizationID: "org-1", CITypeID: "type-switch", Name: "gone", Status: "active"}
	for _, it := range []*Item{item, gone} {
		if err := repo.Create(ctx, it); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.Delete(ctx, "org-1", gone.ID); err != nil {
		t.Fatal(err)
	}
	want := []Node{{ID: item.ID, Name: "sw-1", CITypeID: "type-switch", Status: item.Status, ClientID: "client-1", SiteID: "site-1",
		ManagementIP: "10.0.0.1"}}
	for name, decorated := range map[string]Repository{
		"twice":    NewIndexingRepository(NewIndexingRepository(repo, &recordingIndexer{}), &recordingIndexer{}),
		"fallback": NewIndexingRepository(struct{ Repository }{repo}, &recordingIndexer{}),
	} {
		reader, ok := decorated.(NodeReader)
		if !ok {
			t.Fatalf("%s: the decorated repository must implement NodeReader", name)
		}
		nodes, err := reader.ListNodes(ctx, "org-1", []string{item.ID, gone.ID, "missing"})
		if err != nil || !reflect.DeepEqual(nodes, want) {
			t.Errorf("%s: %+v %v, want %+v", name, nodes, err, want)
		}
		if nodes, err = reader.ListNodes(ctx, "org-2", []string{item.ID}); err != nil || len(nodes) != 0 {
			t.Errorf("%s: another organization reads %+v %v", name, nodes, err)
		}
	}
}

// optionalRepository offers every optional interface callers assert on a
// CI repository, as the PostgreSQL repository does, and counts the calls
// that reach it.
type optionalRepository struct {
	Repository
	calls map[string]int
}

func newOptionalRepository() *optionalRepository {
	return &optionalRepository{Repository: NewMemoryRepository(), calls: map[string]int{}}
}

func (r *optionalRepository) Restore(_ context.Context, orgID, id string) (*Item, error) {
	r.calls["Restore"]++
	return &Item{ID: id, OrganizationID: orgID, Name: "reactivated"}, nil
}

func (r *optionalRepository) RestoreDeleted(_ context.Context, orgID, id string, _ time.Duration) (*Item, error) {
	r.calls["RestoreDeleted"]++
	return &Item{ID: id, OrganizationID: orgID, Name: "restored"}, nil
}

func (r *optionalRepository) UniqueConflicts(context.Context, string, string, string, string, map[string]any) ([]string, error) {
	r.calls["UniqueConflicts"]++
	return []string{"inventory_no"}, nil
}

func (r *optionalRepository) CreateMany(context.Context, string, []*Item) (CreateManyResult, error) {
	r.calls["CreateMany"]++
	return CreateManyResult{}, nil
}

func (r *optionalRepository) GetByIDs(context.Context, string, []string) ([]Item, error) {
	r.calls["GetByIDs"]++
	return []Item{}, nil
}

func (r *optionalRepository) ListNodes(context.Context, string, []string) ([]Node, error) {
	r.calls["ListNodes"]++
	return []Node{}, nil
}

func (r *optionalRepository) ChangeType(_ context.Context, orgID, ciID, _, _ string) (*TypeChangeResult, error) {
	r.calls["ChangeType"]++
	return &TypeChangeResult{CI: &Item{ID: ciID, OrganizationID: orgID}, Changed: true}, nil
}

func (r *optionalRepository) Merge(_ context.Context, orgID, survivorID, victimID, mergeID string) (*MergeResult, error) {
	r.calls["Merge"]++
	return &MergeResult{MergeID: mergeID, Survivor: &Item{ID: survivorID, OrganizationID: orgID}, VictimID: victimID}, nil
}

func (r *optionalRepository) Unmerge(_ context.Context, orgID, victimID string, _ bool) (*UnmergeResult, error) {
	r.calls["Unmerge"]++
	return &UnmergeResult{Survivor: &Item{ID: "survivor", OrganizationID: orgID}, Victim: &Item{ID: victimID, OrganizationID: orgID}}, nil
}

func (r *optionalRepository) MergedInto(context.Context, string, string) (string, bool, error) {
	r.calls["MergedInto"]++
	return "survivor", true, nil
}

func (r *optionalRepository) MergeVictim(context.Context, string, string) (string, error) {
	r.calls["MergeVictim"]++
	return "victim", nil
}

func (r *optionalRepository) ListChanges(context.Context, string, string, api.PaginationParams) ([]Change, int, error) {
	r.calls["ListChanges"]++
	return []Change{{ID: "change"}}, 1, nil
}

// discoveryRestorer is the interface the discovery review handler asserts
// to reactivate a resurrected device (ciRestorer, review.go).
type discoveryRestorer interface {
	Restore(ctx context.Context, orgID, id string) (*Item, error)
}

// TestIndexingRepositoryForwardsOptionalInterfaces covers SR-11 (F-037,
// WP-418): the repository decorated once (search index) and twice, as
// cmd/server decorates it with the module ai (search index, AI chunks),
// keeps every optional interface callers find by type assertion, and each
// call reaches the wrapped repository exactly once. Reactivation and
// restore index the CI again in every index; over a repository without
// them the reactivation fails and no unique attribute is reported.
func TestIndexingRepositoryForwardsOptionalInterfaces(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		iface  string // the interface and where it is asserted
		method string
		call   func(Repository) (bool, error)
	}{
		{"ciRestorer (discovery/review.go)", "Restore", func(r Repository) (bool, error) {
			x, ok := r.(discoveryRestorer)
			if !ok {
				return false, nil
			}
			_, err := x.Restore(ctx, "org-1", "reactivated-ci")
			return true, err
		}},
		{"Restorer (Service.Restore)", "RestoreDeleted", func(r Repository) (bool, error) {
			x, ok := r.(Restorer)
			if !ok {
				return false, nil
			}
			_, err := x.RestoreDeleted(ctx, "org-1", "restored-ci", RestoreRetention)
			return true, err
		}},
		{"UniqueChecker (Service.Create, Service.Update)", "UniqueConflicts", func(r Repository) (bool, error) {
			x, ok := r.(UniqueChecker)
			if !ok {
				return false, nil
			}
			fields, err := x.UniqueConflicts(ctx, "org-1", testServerType, "", "", map[string]any{"inventory_no": "INV-1"})
			if err == nil && !slices.Equal(fields, []string{"inventory_no"}) {
				err = fmt.Errorf("reported %v, want the wrapped repository's [inventory_no]", fields)
			}
			return true, err
		}},
		{"BatchCreator (discovery bulk ingest)", "CreateMany", func(r Repository) (bool, error) {
			x, ok := r.(BatchCreator)
			if !ok {
				return false, nil
			}
			_, err := x.CreateMany(ctx, "org-1", []*Item{{OrganizationID: "org-1", Name: "batch"}})
			return true, err
		}},
		{"BatchReader (graphqlbff, discovery identity)", "GetByIDs", func(r Repository) (bool, error) {
			x, ok := r.(BatchReader)
			if !ok {
				return false, nil
			}
			_, err := x.GetByIDs(ctx, "org-1", []string{"a"})
			return true, err
		}},
		{"NodeReader (topology, impact)", "ListNodes", func(r Repository) (bool, error) {
			x, ok := r.(NodeReader)
			if !ok {
				return false, nil
			}
			_, err := x.ListNodes(ctx, "org-1", []string{"a"})
			return true, err
		}},
		{"TypeChanger (Service.ChangeType, discovery)", "ChangeType", func(r Repository) (bool, error) {
			x, ok := r.(TypeChanger)
			if !ok {
				return false, nil
			}
			_, err := x.ChangeType(ctx, "org-1", "typed-ci", testSwitchType, "keep")
			return true, err
		}},
		{"Merger (Service.Merge)", "Merge", func(r Repository) (bool, error) {
			x, ok := r.(Merger)
			if !ok {
				return false, nil
			}
			_, err := x.Merge(ctx, "org-1", "survivor", "victim", "merge-1")
			return true, err
		}},
		{"Unmerger (Service.Unmerge)", "Unmerge", func(r Repository) (bool, error) {
			x, ok := r.(Unmerger)
			if !ok {
				return false, nil
			}
			_, err := x.Unmerge(ctx, "org-1", "victim", false)
			return true, err
		}},
		{"TombstoneReader (Service.GetByID of a merged CI)", "MergedInto", func(r Repository) (bool, error) {
			x, ok := r.(TombstoneReader)
			if !ok {
				return false, nil
			}
			survivor, found, err := x.MergedInto(ctx, "org-1", "victim")
			if err == nil && (!found || survivor != "survivor") {
				err = fmt.Errorf("resolved to %q (%v), want the wrapped repository's survivor", survivor, found)
			}
			return true, err
		}},
		{"ChangeReader (Service.ListChanges)", "ListChanges", func(r Repository) (bool, error) {
			x, ok := r.(ChangeReader)
			if !ok {
				return false, nil
			}
			changes, total, err := x.ListChanges(ctx, "org-1", "a", api.PaginationParams{Limit: 10})
			if err == nil && (total != 1 || len(changes) != 1) {
				err = fmt.Errorf("listed %d of %d changes, want the wrapped repository's one", len(changes), total)
			}
			return true, err
		}},
		{"mergeVictimReader (Service.RevertMerge)", "MergeVictim", func(r Repository) (bool, error) {
			x, ok := r.(mergeVictimReader)
			if !ok {
				return false, nil
			}
			_, err := x.MergeVictim(ctx, "org-1", "merge-1")
			return true, err
		}},
	}
	for _, variant := range []string{"once", "twice"} {
		inner := newOptionalRepository()
		indexers := []*recordingIndexer{{}}
		repo := NewIndexingRepository(inner, indexers[0])
		if variant == "twice" {
			indexers = append(indexers, &recordingIndexer{})
			repo = NewIndexingRepository(repo, indexers[1])
		}
		for _, tc := range cases {
			ok, err := tc.call(repo)
			if !ok {
				t.Errorf("decorated %s: the repository does not implement %s (%s)", variant, tc.iface, tc.method)
				continue
			}
			if err != nil {
				t.Errorf("decorated %s: %s: %v", variant, tc.method, err)
			}
			if n := inner.calls[tc.method]; n != 1 {
				t.Errorf("decorated %s: %s reached the wrapped repository %d time(s), want 1", variant, tc.method, n)
			}
		}
		for i, idx := range indexers {
			var ids []string
			for _, doc := range idx.indexed {
				ids = append(ids, doc.EntityID)
			}
			for _, id := range []string{"reactivated-ci", "restored-ci"} {
				if !slices.Contains(ids, id) {
					t.Errorf("decorated %s: index %d did not index %s again, indexed %v", variant, i+1, id, ids)
				}
			}
		}
	}

	// Over a repository without the optional interfaces.
	plain := NewIndexingRepository(struct{ Repository }{NewMemoryRepository()}, &recordingIndexer{})
	if restorer, ok := plain.(discoveryRestorer); ok {
		if item, err := restorer.Restore(ctx, "org-1", "a"); err == nil {
			t.Errorf("reactivation over a repository without it: %+v, want an error", item)
		}
	}
	if checker, ok := plain.(UniqueChecker); ok {
		if fields, err := checker.UniqueConflicts(ctx, "org-1", testServerType, "", "", map[string]any{"k": "v"}); err != nil || len(fields) != 0 {
			t.Errorf("unique check over a repository without it: %v %v, want none", fields, err)
		}
	}
}

// uncoveredMethods lists the exported methods of base, except skip, that
// decorator lacks or offers with another signature.
func uncoveredMethods(base, decorator reflect.Type, skip ...string) []string {
	var missing []string
	for i := 0; i < base.NumMethod(); i++ {
		m := base.Method(i)
		if slices.Contains(skip, m.Name) {
			continue
		}
		d, ok := decorator.MethodByName(m.Name)
		if !ok || !sameSignature(m.Type, d.Type) {
			missing = append(missing, m.Name)
		}
	}
	return missing
}

// sameSignature compares two method types of concrete types without their
// receiver (the first argument).
func sameSignature(a, b reflect.Type) bool {
	if a.NumIn() != b.NumIn() || a.NumOut() != b.NumOut() || a.IsVariadic() != b.IsVariadic() {
		return false
	}
	for i := 1; i < a.NumIn(); i++ {
		if a.In(i) != b.In(i) {
			return false
		}
	}
	for i := 0; i < a.NumOut(); i++ {
		if a.Out(i) != b.Out(i) {
			return false
		}
	}
	return true
}

// pgRepositoryWithExtra is a PostgreSQL repository with one more exported
// method, which the decorator does not forward.
type pgRepositoryWithExtra struct{ *PGRepository }

func (pgRepositoryWithExtra) Reindex(context.Context, string) error { return nil }

// pgRepositoryWithOtherSignature offers MergedInto with another signature.
type pgRepositoryWithOtherSignature struct{ *PGRepository }

func (pgRepositoryWithOtherSignature) MergedInto(context.Context, string) error { return nil }

// TestIndexingRepositoryCoversPGRepositoryMethods guards SR-11: every
// exported method of *PGRepository except WithEvents (construction) exists
// on the indexing decorator with the same signature, so an optional
// interface the PostgreSQL repository gains is not lost behind the
// decorator in production. The checker reports a method the decorator
// lacks and one it offers with another signature.
func TestIndexingRepositoryCoversPGRepositoryMethods(t *testing.T) {
	pg := reflect.TypeOf(&PGRepository{})
	decorator := reflect.TypeOf(NewIndexingRepository(NewMemoryRepository(), &recordingIndexer{}))
	if decorator == reflect.TypeOf(NewMemoryRepository()) {
		t.Fatal("NewIndexingRepository returned the repository undecorated")
	}
	for _, name := range []string{"Restore", "UniqueConflicts", "List", "Create"} {
		if _, ok := pg.MethodByName(name); !ok {
			t.Fatalf("*PGRepository has no method %s; the guard would check nothing", name)
		}
	}
	if missing := uncoveredMethods(pg, decorator, "WithEvents"); len(missing) > 0 {
		t.Errorf("the indexing decorator does not forward %v of *PGRepository; callers asserting them fail in production (SR-11)", missing)
	}

	// Negative: the checker reports what the decorator does not offer.
	if got := uncoveredMethods(reflect.TypeOf(pgRepositoryWithExtra{}), decorator, "WithEvents"); !slices.Equal(got, []string{"Reindex"}) {
		t.Errorf("extra method without forwarding: checker reported %v, want [Reindex]", got)
	}
	if got := uncoveredMethods(reflect.TypeOf(pgRepositoryWithOtherSignature{}), decorator, "WithEvents"); !slices.Equal(got, []string{"MergedInto"}) {
		t.Errorf("method with another signature: checker reported %v, want [MergedInto]", got)
	}
	if got := uncoveredMethods(pg, decorator); !slices.Equal(got, []string{"WithEvents"}) {
		t.Errorf("without the exception: checker reported %v, want [WithEvents]", got)
	}
}
