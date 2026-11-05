package savedview

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// TestMemoryRepositoryWriteProtection covers the rule of VIE-02 (WP-307)
// in memory: the owner changes and deletes an org-wide shared view, so
// does a caller with saved_view:manage (Access.Manage); anybody else gets
// ErrNotFound and the view stays; manage on a private view of somebody
// else is ErrNotFound. A PATCH without shared keeps the sharing (F-166).
func TestMemoryRepositoryWriteProtection(t *testing.T) {
	repo := NewMemoryRepository()
	bg := context.Background()
	manage := WithAccess(bg, Access{Manage: true})
	create := func(name, owner string, shared bool) string {
		t.Helper()
		v := &View{OrganizationID: "org", OwnerID: owner, Name: name, EntityKind: "ci", Shared: shared, SharedWithRoles: []string{"r1"}}
		if err := repo.Create(bg, v); err != nil {
			t.Fatal(err)
		}
		return v.ID
	}
	shared := create("geteilt", "owner", true)
	private := create("privat", "owner", false)
	get := func(id string) *View {
		t.Helper()
		v, err := repo.GetByID(bg, "org", id, "owner")
		if err != nil {
			t.Fatal(err)
		}
		return v
	}

	if _, err := repo.Update(bg, "org", shared, "other", &UpsertRequest{Name: "gekapert"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update by another user: %v, want ErrNotFound", err)
	}
	if err := repo.Delete(bg, "org", shared, "other"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete by another user: %v, want ErrNotFound", err)
	}
	if v := get(shared); v.Name != "geteilt" {
		t.Errorf("view after the refused writes: %+v", v)
	}
	v, err := repo.Update(manage, "org", shared, "admin", &UpsertRequest{Name: "übernommen"})
	if err != nil || v.Name != "übernommen" || !v.Shared || len(v.SharedWithRoles) != 1 || v.OwnerID != "owner" {
		t.Errorf("update with saved_view:manage: %+v %v, want the name changed and the sharing kept", v, err)
	}
	if v, err = repo.Update(bg, "org", shared, "owner", &UpsertRequest{Name: "zurück"}); err != nil || !v.Shared {
		t.Errorf("update by the owner: %+v %v", v, err)
	}
	off := false
	if v, err = repo.Update(bg, "org", shared, "owner", &UpsertRequest{Shared: &off}); err != nil || v.Shared || v.Name != "zurück" {
		t.Errorf("update shared=false: %+v %v", v, err)
	}
	// Unshared, the view is private: manage no longer reaches it.
	if _, err = repo.Update(manage, "org", shared, "admin", &UpsertRequest{Name: "x"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update of an unshared view with saved_view:manage: %v, want ErrNotFound", err)
	}
	for _, id := range []string{private, shared} {
		if _, err = repo.GetByID(manage, "org", id, "admin"); !errors.Is(err, ErrNotFound) {
			t.Errorf("get of a private view with saved_view:manage: %v, want ErrNotFound", err)
		}
		if err = repo.Delete(manage, "org", id, "admin"); !errors.Is(err, ErrNotFound) {
			t.Errorf("delete of a private view with saved_view:manage: %v, want ErrNotFound", err)
		}
	}
	on := true
	if _, err = repo.Update(bg, "org", shared, "owner", &UpsertRequest{Shared: &on}); err != nil {
		t.Fatal(err)
	}
	if err = repo.Delete(WithAccess(bg, Access{Manage: true}), "other-org", shared, "admin"); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete in another organization: %v, want ErrNotFound", err)
	}
	if err = repo.Delete(manage, "org", shared, "admin"); err != nil {
		t.Errorf("delete with saved_view:manage: %v", err)
	}
	if err = repo.Delete(bg, "org", private, "owner"); err != nil {
		t.Errorf("delete by the owner: %v", err)
	}
	if _, total, _ := repo.List(bg, "org", "owner", api.PaginationParams{Limit: 10}); total != 0 {
		t.Errorf("views left: %d", total)
	}
}
