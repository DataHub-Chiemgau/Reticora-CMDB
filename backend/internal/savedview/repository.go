package savedview

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ErrNotFound: the view is unknown, of another organization or a private
// view of somebody else (VIE-01); all answer the same 404.
var ErrNotFound = errors.New("saved view not found")

// ErrConflict: the owner has a view of this name already (409; PostgreSQL
// reports the unique violation, 23505).
var ErrConflict = errors.New("a saved view with this name already exists")

// Access is what the handler knows of the caller beyond the user id
// (VIE-02): the standard roles the caller holds only through IdP groups
// (identity.StandardRolesForGroups of Principal.Groups, E-110) and whether
// the effective permissions hold saved_view:manage. The handler puts it in
// the context of the repository calls (WithAccess); without it only stored
// role assignments count and nobody manages.
type Access struct {
	// IdPRoles are standard role names (org_admin, engineer, viewer).
	IdPRoles []string
	// Manage lets the caller change and delete every view they may read.
	Manage bool
}

type accessKey struct{}

// WithAccess stores the access of the caller for the repository calls.
func WithAccess(ctx context.Context, access Access) context.Context {
	access.IdPRoles = append([]string(nil), access.IdPRoles...)
	return context.WithValue(ctx, accessKey{}, access)
}

// AccessFrom returns the access WithAccess stored, the zero Access without.
func AccessFrom(ctx context.Context) Access {
	access, _ := ctx.Value(accessKey{}).(Access)
	return access
}

// Repository defines persistence for saved views. Reading follows VIE-01
// (own, shared org-wide, shared with a role of the user, also one held
// through an IdP group, AccessFrom); changing and deleting is for the owner
// or, on a view the caller may read, for a holder of saved_view:manage
// (Access.Manage, VIE-02); every other view is ErrNotFound.
type Repository interface {
	List(ctx context.Context, orgID, ownerID string, page api.PaginationParams) ([]View, int, error)
	// GetByID returns a view the user may read; a private view of
	// somebody else is not found (VIE-01).
	GetByID(ctx context.Context, orgID, id, userID string) (*View, error)
	Create(ctx context.Context, view *View) error
	Update(ctx context.Context, orgID, id, ownerID string, req *UpsertRequest) (*View, error)
	Delete(ctx context.Context, orgID, id, ownerID string) error
}

// MemoryRepository is an in-memory implementation (tests, --no-db).
type MemoryRepository struct {
	mu    sync.RWMutex
	views map[string]*View
	seq   int
}

// NewMemoryRepository creates an empty in-memory saved view repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{views: map[string]*View{}}
}

func (r *MemoryRepository) nextID() string {
	r.seq++
	return fmt.Sprintf("00000000-0000-4000-8000-%012d", r.seq)
}

// List returns the caller's own views plus shared views of the organization.
func (r *MemoryRepository) List(_ context.Context, orgID, ownerID string, page api.PaginationParams) ([]View, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []View
	for _, v := range r.views {
		if v.OrganizationID != orgID {
			continue
		}
		if !memoryVisible(v, ownerID) {
			continue
		}
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	total := len(out)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return out[start:end], total, nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id, userID string) (*View, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	v, ok := r.views[id]
	if !ok || v.OrganizationID != orgID || !memoryVisible(v, userID) {
		return nil, ErrNotFound
	}
	out := *v
	return &out, nil
}

func (r *MemoryRepository) Create(_ context.Context, view *View) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.views {
		if existing.OrganizationID == view.OrganizationID &&
			existing.OwnerID == view.OwnerID && existing.Name == view.Name {
			return ErrConflict
		}
	}
	normalizeView(view)
	view.ID = r.nextID()
	now := time.Now().UTC()
	view.CreatedAt = now
	view.UpdatedAt = now
	stored := *view
	r.views[view.ID] = &stored
	return nil
}

// memoryVisible reports whether the user reads the view: own or shared
// org-wide; the sharing with roles is not modelled in memory.
func memoryVisible(v *View, userID string) bool {
	return v.Shared || (v.OwnerID != "" && v.OwnerID == userID)
}

// memoryWritable reports whether the user changes the view: the owner, or
// a holder of saved_view:manage on a view they read (VIE-02).
func memoryWritable(ctx context.Context, v *View, userID string) bool {
	if v.OwnerID != "" && v.OwnerID == userID {
		return true
	}
	return AccessFrom(ctx).Manage && memoryVisible(v, userID)
}

// Update modifies a view of its owner or, with saved_view:manage, one the
// caller reads; for everybody else shared views are read-only (E-86,
// option 1, VIE-02). Role ids are not checked against roles here: the
// memory repository keeps none.
func (r *MemoryRepository) Update(ctx context.Context, orgID, id, ownerID string, req *UpsertRequest) (*View, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.views[id]
	if !ok || v.OrganizationID != orgID || !memoryWritable(ctx, v, ownerID) {
		return nil, ErrNotFound
	}
	if req.Name != "" && req.Name != v.Name {
		for _, existing := range r.views {
			if existing.OrganizationID == v.OrganizationID && existing.OwnerID == v.OwnerID && existing.Name == req.Name {
				return nil, ErrConflict
			}
		}
	}
	if req.Name != "" {
		v.Name = req.Name
	}
	if req.EntityKind != "" {
		v.EntityKind = req.EntityKind
	}
	if req.FilterSpec != nil {
		v.FilterSpec = req.FilterSpec
	}
	if req.Sort != nil {
		v.Sort = req.Sort
	}
	if req.Columns != nil {
		v.Columns = req.Columns
	}
	if req.SharedWithRoles != nil {
		v.SharedWithRoles = req.SharedWithRoles
	}
	if req.Shared != nil {
		v.Shared = *req.Shared
	}
	v.UpdatedAt = time.Now().UTC()
	out := *v
	return &out, nil
}

// Delete removes a view of its owner or, with saved_view:manage, one the
// caller reads (VIE-02).
func (r *MemoryRepository) Delete(ctx context.Context, orgID, id, ownerID string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	v, ok := r.views[id]
	if !ok || v.OrganizationID != orgID || !memoryWritable(ctx, v, ownerID) {
		return ErrNotFound
	}
	delete(r.views, id)
	return nil
}
