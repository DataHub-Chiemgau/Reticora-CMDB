package probe

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// MemoryRepository is the Store of --no-db and of the handler tests: the
// rules of PGRepository (scope of the context, references, collector group,
// parent chain, uniqueness) on maps. It keeps no monitoring profiles (the
// levels above a probe are empty) and writes no events.
type MemoryRepository struct {
	mu     sync.Mutex
	probes map[string]*Probe
	groups map[string]memGroup
	cis    func(ctx context.Context, orgID, id string) (clientID, siteID string, ok bool)
	// clients and sites are the known ones; nil accepts every id.
	clients map[string]string
	sites   map[string]memSite
	// budget admits the writes against the probe budget under the mutex
	// (WP-940 AK3); nil admits every write.
	budget BudgetGate
}

// SetBudget implements Store.
func (m *MemoryRepository) SetBudget(gate BudgetGate) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.budget = gate
}

// planMin returns min_probe_interval_s of the organization (0 without a
// budget gate).
func (m *MemoryRepository) planMin(ctx context.Context, orgID string) (int64, error) {
	if m.budget == nil {
		return 0, nil
	}
	return m.budget.MinProbeIntervalS(ctx, orgID)
}

// admit admits a write against the budget of the organization; the
// store's lock is held, so the count is exact under concurrency.
func (m *MemoryRepository) admit(ctx context.Context, orgID string, current, next *Probe) error {
	if m.budget == nil {
		return nil
	}
	planMin, err := m.planMin(ctx, orgID)
	if err != nil {
		return err
	}
	if !next.Enabled {
		return admitBudget(ctx, m.budget, orgID, nil, nil, current, next, Layers{PlanMinIntervalS: planMin})
	}
	active := make([]Probe, 0)
	for _, p := range m.probes {
		if p.OrganizationID == orgID && p.Enabled {
			active = append(active, *p)
		}
	}
	layers := Layers{PlanMinIntervalS: planMin}
	return admitBudget(ctx, m.budget, orgID, active, func(*Probe) Layers { return layers }, current, next, layers)
}

// memGroup is a collector group of the memory store.
type memGroup struct{ orgID, clientID string }

// memSite is a site of the memory store.
type memSite struct{ orgID, clientID string }

// NewMemoryRepository returns an empty store.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{probes: map[string]*Probe{}, groups: map[string]memGroup{}}
}

var _ Store = (*MemoryRepository)(nil)

// WithCIs sets the lookup of the CIs (client and site of a visible CI).
func (m *MemoryRepository) WithCIs(lookup func(ctx context.Context, orgID, id string) (clientID, siteID string, ok bool)) *MemoryRepository {
	m.cis = lookup
	return m
}

// AddCollectorGroup adds a collector group of the organization (clientID
// empty: org-wide).
func (m *MemoryRepository) AddCollectorGroup(orgID, id, clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.groups[id] = memGroup{orgID: orgID, clientID: clientID}
}

// RemoveCollectorGroup removes a collector group.
func (m *MemoryRepository) RemoveCollectorGroup(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.groups, id)
}

// AddClient makes the store check clients: only added ones exist.
func (m *MemoryRepository) AddClient(orgID, id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.clients == nil {
		m.clients = map[string]string{}
	}
	m.clients[id] = orgID
}

// AddSite makes the store check sites: only added ones exist.
func (m *MemoryRepository) AddSite(orgID, id, clientID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.sites == nil {
		m.sites = map[string]memSite{}
	}
	m.sites[id] = memSite{orgID: orgID, clientID: clientID}
}

// memScope is the tenant scope of a call.
type memScope struct {
	orgID          string
	clients, sites database.ScopeSet
}

func scopeOf(ctx context.Context) (memScope, error) {
	s, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		return memScope{}, database.ErrNoTenantScope
	}
	return memScope{orgID: s.OrgID, clients: s.Clients, sites: s.Sites}, nil
}

// in reports whether id lies in the set; empty is outside a restricted set.
func in(set database.ScopeSet, id string) bool {
	return set.IsAll() || (id != "" && slices.Contains(set.IDs(), id))
}

// reads reports whether the scope reads a row with client and site (rows
// without client or site are read by everyone, Konvention 5).
func (s *memScope) reads(clientID, siteID string) bool {
	return (clientID == "" || in(s.clients, clientID)) && (siteID == "" || in(s.sites, siteID))
}

// writes reports whether the scope writes a row with client and site.
func (s *memScope) writes(clientID, siteID string) bool {
	return in(s.clients, clientID) && in(s.sites, siteID)
}

// errRowSecurity is the refusal of a write outside the scope, as row level
// security reports it (403 at the API).
var errRowSecurity = &pgconn.PgError{Code: "42501", Message: "new row violates row-level security policy for table \"probe\""}

func (m *MemoryRepository) visible(s *memScope, id string) (*Probe, bool) {
	p, ok := m.probes[id]
	if !ok || p.OrganizationID != s.orgID || !s.reads(p.ClientID, p.SiteID) {
		return nil, false
	}
	return p, true
}

// Get implements Repository.
func (m *MemoryRepository) Get(ctx context.Context, id string) (*Probe, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.visible(&s, id)
	if !ok {
		return nil, ErrNotFound
	}
	out := *p
	return &out, nil
}

func (m *MemoryRepository) matching(s *memScope, f *ListFilter) []Probe {
	out := make([]Probe, 0)
	for id := range m.probes {
		p, ok := m.visible(s, id)
		if !ok {
			continue
		}
		if f != nil && ((f.CIID != "" && p.CIID != f.CIID) || (f.Type != "" && p.Type != f.Type) ||
			(f.CollectorGroupID != "" && p.CollectorGroupID != f.CollectorGroupID) ||
			(f.ClientID != "" && p.ClientID != f.ClientID) || (f.Enabled != nil && p.Enabled != *f.Enabled)) {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// List implements Repository.
func (m *MemoryRepository) List(ctx context.Context, f *ListFilter) ([]Probe, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.matching(&s, f)
	if f != nil {
		start := min(f.Offset, len(out))
		end := len(out)
		if f.Limit > 0 {
			end = min(start+f.Limit, len(out))
		}
		out = out[start:end]
	}
	return out, nil
}

// Count implements Store.
func (m *MemoryRepository) Count(ctx context.Context, f *ListFilter) (int, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.matching(&s, f)), nil
}

// Layers implements Repository: the memory store keeps no profiles; only
// the plan minimum applies.
func (m *MemoryRepository) Layers(ctx context.Context, p *Probe) (Layers, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	planMin, err := m.planMin(ctx, p.OrganizationID)
	return Layers{PlanMinIntervalS: planMin}, err
}

// ResolveForCollector implements Repository: the memory store keeps no
// assignments.
func (m *MemoryRepository) ResolveForCollector(context.Context, string) ([]Resolved, error) {
	return []Resolved{}, nil
}

// ResolveForGroup implements Repository.
func (m *MemoryRepository) ResolveForGroup(ctx context.Context, groupID string) ([]Resolved, error) {
	probes, err := m.List(ctx, &ListFilter{CollectorGroupID: groupID})
	if err != nil {
		return nil, err
	}
	x := newLayerIndex()
	if s, ok := database.TenantScopeFromContext(ctx); ok {
		m.mu.Lock()
		x.planMin, err = m.planMin(ctx, s.OrgID)
		m.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
	return x.resolveAll(probes, nil), nil
}

// taken reports whether another probe holds the key of p.
func (m *MemoryRepository) taken(p *Probe) bool {
	for _, o := range m.probes {
		if o.ID != p.ID && o.OrganizationID == p.OrganizationID && o.ClientID == p.ClientID && o.CIID == p.CIID &&
			o.Type == p.Type && o.Target == p.Target {
			return true
		}
	}
	return false
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Create implements Store.
func (m *MemoryRepository) Create(ctx context.Context, in *Probe, check Check) (*Probe, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p := *in
	p.OrganizationID = s.orgID
	if check != nil {
		if werr := check(ctx, &memWriteTx{m: m, s: &s}, &p, nil); werr != nil {
			return nil, werr
		}
	}
	if !s.writes(p.ClientID, p.SiteID) {
		return nil, errRowSecurity
	}
	if m.taken(&p) {
		return nil, ErrTargetTaken
	}
	if werr := m.admit(ctx, s.orgID, nil, &p); werr != nil {
		return nil, werr
	}
	if p.ID, err = newID(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	p.CreatedAt, p.UpdatedAt = now, now
	stored := p
	m.probes[p.ID] = &stored
	return &p, nil
}

// Update implements Store.
func (m *MemoryRepository) Update(ctx context.Context, id string, change func(*Probe) (*Probe, []string, error), check Check) (*Probe, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.visible(&s, id)
	if !ok {
		return nil, ErrNotFound
	}
	current := *cur
	if !s.writes(current.ClientID, current.SiteID) {
		// Row level security hides the row from the update.
		return nil, ErrNotFound
	}
	next, fields, err := change(&current)
	if err != nil {
		return nil, err
	}
	if check != nil {
		if err := check(ctx, &memWriteTx{m: m, s: &s}, next, &current); err != nil {
			return nil, err
		}
	}
	if len(fields) == 0 {
		return &current, nil
	}
	if m.taken(next) {
		return nil, ErrTargetTaken
	}
	if err := m.admit(ctx, s.orgID, &current, next); err != nil {
		return nil, err
	}
	next.UpdatedAt = time.Now().UTC()
	stored := *next
	m.probes[id] = &stored
	return next, nil
}

// Delete implements Store; children lose their parent.
func (m *MemoryRepository) Delete(ctx context.Context, id string) error {
	s, err := scopeOf(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.visible(&s, id)
	if !ok || !s.writes(p.ClientID, p.SiteID) {
		return ErrNotFound
	}
	delete(m.probes, id)
	for _, o := range m.probes {
		if o.ParentProbeID == id {
			o.ParentProbeID = ""
		}
	}
	return nil
}

// memWriteTx is the WriteTx of the memory store; the store's lock is held.
type memWriteTx struct {
	m *MemoryRepository
	s *memScope
}

func (t *memWriteTx) CI(ctx context.Context, id string) (clientID, siteID string, ok bool, err error) {
	if t.m.cis == nil || !api.IsUUID(id) {
		return "", "", false, nil
	}
	clientID, siteID, ok = t.m.cis(ctx, t.s.orgID, id)
	if !ok || !t.s.reads(clientID, siteID) {
		return "", "", false, nil
	}
	return clientID, siteID, true, nil
}

func (t *memWriteTx) ClientVisible(_ context.Context, id string) (bool, error) {
	if t.m.clients == nil {
		return in(t.s.clients, id), nil
	}
	org, ok := t.m.clients[id]
	return ok && org == t.s.orgID && in(t.s.clients, id), nil
}

func (t *memWriteTx) Site(_ context.Context, id string) (clientID string, ok bool, err error) {
	if t.m.sites == nil {
		return "", true, nil
	}
	site, found := t.m.sites[id]
	if !found || site.orgID != t.s.orgID || !t.s.reads(site.clientID, id) {
		return "", false, nil
	}
	return site.clientID, true, nil
}

func (t *memWriteTx) CollectorGroup(_ context.Context, id string) (clientID string, ok bool, err error) {
	g, found := t.m.groups[id]
	if !found || g.orgID != t.s.orgID || !t.s.reads(g.clientID, "") {
		return "", false, nil
	}
	return g.clientID, true, nil
}

func (t *memWriteTx) CollectorGroupsOfClient(_ context.Context, clientID string) ([]string, error) {
	var out []string
	for id, g := range t.m.groups {
		if g.orgID == t.s.orgID && g.clientID == clientID && t.s.reads(g.clientID, "") {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out, nil
}

func (t *memWriteTx) ProbeVisible(_ context.Context, id string) (bool, error) {
	_, ok := t.m.visible(t.s, id)
	return ok, nil
}

func (t *memWriteTx) Ancestors(_ context.Context, id string, limit int) ([]string, error) {
	var out []string
	for cur := id; cur != "" && len(out) < limit; {
		p, ok := t.m.probes[cur]
		if !ok || p.OrganizationID != t.s.orgID || slices.Contains(out, cur) {
			break
		}
		out = append(out, cur)
		cur = p.ParentProbeID
	}
	return out, nil
}

func (t *memWriteTx) SubtreeHeight(_ context.Context, id string, limit int) (int, error) {
	level := []string{id}
	seen := map[string]bool{id: true}
	height := 0
	for height < limit {
		var next []string
		for _, p := range t.m.probes {
			if p.OrganizationID == t.s.orgID && !seen[p.ID] && slices.Contains(level, p.ParentProbeID) {
				seen[p.ID] = true
				next = append(next, p.ID)
			}
		}
		if len(next) == 0 {
			break
		}
		height++
		level = next
	}
	return height, nil
}

func (t *memWriteTx) Layers(context.Context, *Probe) (Layers, error) { return Layers{}, nil }
