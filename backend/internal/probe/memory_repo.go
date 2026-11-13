package probe

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
)

// MemoryRepository is the Store of --no-db and of the handler tests: the
// rules of PGRepository (scope of the context, references, collector group,
// parent chain, uniqueness, monitoring profiles and groups with the budget)
// on maps. It keeps no assignments and writes no events.
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
	// profiles, mgroups and members are the monitoring profiles, groups
	// and memberships (WP-946); nil until the first write.
	profiles map[string]*MonitoringProfile
	mgroups  map[string]*MonitoringGroup
	members  map[memMember]*MonitoringGroupMember
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
	x := m.index(orgID)
	x.planMin = planMin
	return admitBudget(ctx, m.budget, orgID, active, x.layers, current, next, x.layers(next))
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

// Layers implements Repository: the stored profiles and memberships with
// the plan minimum.
func (m *MemoryRepository) Layers(ctx context.Context, p *Probe) (Layers, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	planMin, err := m.planMin(ctx, p.OrganizationID)
	x := m.index(p.OrganizationID)
	x.planMin = planMin
	return x.layers(p), err
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
		x = m.index(s.OrgID)
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

func (t *memWriteTx) Layers(_ context.Context, p *Probe) (Layers, error) {
	return t.m.index(t.s.orgID).layers(p), nil
}

// --- monitoring profiles and groups (WP-946) --------------------------------

var _ ProfileStore = (*MemoryRepository)(nil)

// memMember is the key of a membership: group and CI.
type memMember struct{ groupID, ciID string }

// ensureMonitoring creates the maps of the profiles and groups; the lock
// is held.
func (m *MemoryRepository) ensureMonitoring() {
	if m.profiles == nil {
		m.profiles = map[string]*MonitoringProfile{}
		m.mgroups = map[string]*MonitoringGroup{}
		m.members = map[memMember]*MonitoringGroupMember{}
	}
}

// index builds the override levels of the organization from the stored
// profiles and memberships; the lock is held.
func (m *MemoryRepository) index(orgID string) *layerIndex {
	x := newLayerIndex()
	for _, p := range m.profiles {
		if p.OrganizationID == orgID {
			x.add(p)
		}
	}
	for _, mem := range m.members {
		g, ok := m.mgroups[mem.GroupID]
		if mem.OrganizationID == orgID && ok {
			x.groups[mem.CIID] = append(x.groups[mem.CIID], memberGroup{id: g.ID, name: g.Name, priority: g.Priority})
		}
	}
	return x
}

// memBudget counts the probe budget of the organization's active probes
// at their effective values; the lock is held.
func (m *MemoryRepository) memBudget(ctx context.Context, orgID string) (entitlement.ProbeBudget, error) {
	planMin, err := m.planMin(ctx, orgID)
	if err != nil {
		return entitlement.ProbeBudget{}, err
	}
	x := m.index(orgID)
	x.planMin = planMin
	var specs []entitlement.EffectiveSpec
	for _, p := range m.probes {
		if p.OrganizationID == orgID && p.Enabled {
			e := Resolve(p, x.layers(p))
			specs = append(specs, budgetSpec(p, &e))
		}
	}
	return entitlement.BudgetOf(specs), nil
}

// monitoringWrite runs a profile or group write under the lock with the
// budget rules of PGRepository.monitoringWrite: write changes the maps and
// returns how to undo the change, which runs when the budget refuses it.
func (m *MemoryRepository) monitoringWrite(ctx context.Context, write func(s *memScope) (undo func(), err error)) error {
	s, err := scopeOf(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureMonitoring()
	var before entitlement.ProbeBudget
	if m.budget != nil {
		if before, err = m.memBudget(ctx, s.orgID); err != nil {
			return err
		}
	}
	undo, err := write(&s)
	if err != nil {
		return err
	}
	if m.budget != nil {
		after, berr := m.memBudget(ctx, s.orgID)
		if berr == nil {
			berr = m.budget.AllowProbeChange(ctx, s.orgID, before, after)
		}
		if berr != nil {
			undo()
			return berr
		}
	}
	return nil
}

// profileVisible reports whether the scope reads the profile.
func (s *memScope) profileVisible(p *MonitoringProfile) bool {
	return p.OrganizationID == s.orgID && s.reads(p.ClientID, p.SiteID)
}

// ListProfiles implements ProfileStore.
func (m *MemoryRepository) ListProfiles(ctx context.Context, f *ProfileFilter) ([]MonitoringProfile, int, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, 0, err
	}
	if f == nil {
		f = &ProfileFilter{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MonitoringProfile, 0)
	for _, p := range m.profiles {
		if !s.profileVisible(p) || (f.Level != "" && p.Level != f.Level) || (f.LevelID != "" && p.TargetID() != f.LevelID) ||
			(f.ProbeType != "" && p.ProbeType != f.ProbeType) {
			continue
		}
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return pageOf(out, f.Limit, f.Offset), len(out), nil
}

// pageOf returns the page of items.
func pageOf[T any](items []T, limit, offset int) []T {
	start := min(max(offset, 0), len(items))
	end := len(items)
	if limit > 0 {
		end = min(start+limit, len(items))
	}
	return items[start:end]
}

// GetProfile implements ProfileStore.
func (m *MemoryRepository) GetProfile(ctx context.Context, id string) (*MonitoringProfile, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.profiles[id]
	if !ok || !s.profileVisible(p) {
		return nil, ErrProfileNotFound
	}
	out := *p
	return &out, nil
}

// profileScope sets client and site of the profile from its target (the
// trigger monitoring_profile_scope); ok false when the target is not
// visible.
func (m *MemoryRepository) profileScope(ctx context.Context, s *memScope, p *MonitoringProfile) bool {
	t := &memWriteTx{m: m, s: s}
	switch p.Level {
	case LevelOrg:
		p.ClientID, p.SiteID = "", ""
		return true
	case LevelClient:
		ok, _ := t.ClientVisible(ctx, p.ClientID)
		p.SiteID = ""
		return ok
	case LevelGroup:
		g, ok := m.mgroups[p.GroupID]
		if !ok || g.OrganizationID != s.orgID || !s.reads(g.ClientID, "") {
			return false
		}
		p.ClientID, p.SiteID = g.ClientID, ""
		return true
	case LevelCI:
		client, site, ok, _ := t.CI(ctx, p.CIID)
		p.ClientID, p.SiteID = client, site
		return ok
	}
	return false
}

// profileTaken reports whether another profile has the level, target and
// type of p.
func (m *MemoryRepository) profileTaken(p *MonitoringProfile) bool {
	for _, o := range m.profiles {
		if o.ID != p.ID && o.OrganizationID == p.OrganizationID && o.Level == p.Level && o.TargetID() == p.TargetID() &&
			o.ProbeType == p.ProbeType {
			return true
		}
	}
	return false
}

// CreateProfile implements ProfileStore.
func (m *MemoryRepository) CreateProfile(ctx context.Context, in *MonitoringProfile) (*MonitoringProfile, error) {
	if in.Level == LevelOrg && !orgWide(ctx) {
		return nil, errOrgWideOnly
	}
	var out MonitoringProfile
	err := m.monitoringWrite(ctx, func(s *memScope) (func(), error) {
		if err := checkPlanInterval(ctx, m.budget, s.orgID, in.Params); err != nil {
			return nil, err
		}
		p := *in
		p.OrganizationID = s.orgID
		if !m.profileScope(ctx, s, &p) {
			return nil, ErrLevelNotFound
		}
		if !s.writes(p.ClientID, p.SiteID) {
			return nil, errRowSecurity
		}
		if m.profileTaken(&p) {
			return nil, ErrProfileTaken
		}
		id, err := newID()
		if err != nil {
			return nil, err
		}
		p.ID, p.UpdatedAt = id, time.Now().UTC()
		stored := p
		m.profiles[id] = &stored
		out = p
		return func() { delete(m.profiles, id) }, nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// writableProfile returns the stored profile the scope may change.
func (m *MemoryRepository) writableProfile(ctx context.Context, s *memScope, id string) (*MonitoringProfile, error) {
	p, ok := m.profiles[id]
	if !ok || !s.profileVisible(p) {
		return nil, ErrProfileNotFound
	}
	if p.Level == LevelOrg && !orgWide(ctx) {
		return nil, errOrgWideOnly
	}
	if !s.writes(p.ClientID, p.SiteID) {
		return nil, ErrProfileNotFound
	}
	return p, nil
}

// UpdateProfile implements ProfileStore.
func (m *MemoryRepository) UpdateProfile(ctx context.Context, id string, params json.RawMessage) (*MonitoringProfile, error) {
	var out MonitoringProfile
	err := m.monitoringWrite(ctx, func(s *memScope) (func(), error) {
		p, err := m.writableProfile(ctx, s, id)
		if err != nil {
			return nil, err
		}
		if err := checkProfileParams(p.ProbeType, params); err != nil {
			return nil, err
		}
		if err := checkPlanInterval(ctx, m.budget, s.orgID, params); err != nil {
			return nil, err
		}
		old := *p
		p.Params, p.UpdatedAt = params, time.Now().UTC()
		out = *p
		return func() { *p = old }, nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteProfile implements ProfileStore.
func (m *MemoryRepository) DeleteProfile(ctx context.Context, id string) error {
	return m.monitoringWrite(ctx, func(s *memScope) (func(), error) {
		p, err := m.writableProfile(ctx, s, id)
		if err != nil {
			return nil, err
		}
		delete(m.profiles, id)
		return func() { m.profiles[id] = p }, nil
	})
}

// groupVisible reports whether the scope reads the group.
func (s *memScope) groupVisible(g *MonitoringGroup) bool {
	return g.OrganizationID == s.orgID && s.reads(g.ClientID, "")
}

// withCount returns the group with the members the scope sees; the lock is
// held.
func (m *MemoryRepository) withCount(s *memScope, g *MonitoringGroup) MonitoringGroup {
	out := *g
	out.MemberCount = 0
	for _, mem := range m.members {
		if mem.GroupID == g.ID && s.reads(mem.ClientID, mem.SiteID) {
			out.MemberCount++
		}
	}
	return out
}

// ListGroups implements ProfileStore.
func (m *MemoryRepository) ListGroups(ctx context.Context, clientID string, limit, offset int) ([]MonitoringGroup, int, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]MonitoringGroup, 0)
	for _, g := range m.mgroups {
		if s.groupVisible(g) && (clientID == "" || g.ClientID == clientID) {
			out = append(out, m.withCount(&s, g))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return pageOf(out, limit, offset), len(out), nil
}

// visibleGroup returns the stored group the scope reads.
func (m *MemoryRepository) visibleGroup(s *memScope, id string) (*MonitoringGroup, error) {
	g, ok := m.mgroups[id]
	if !ok || !s.groupVisible(g) {
		return nil, ErrGroupNotFound
	}
	return g, nil
}

// GetGroup implements ProfileStore.
func (m *MemoryRepository) GetGroup(ctx context.Context, id string) (*MonitoringGroup, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.visibleGroup(&s, id)
	if err != nil {
		return nil, err
	}
	out := m.withCount(&s, g)
	return &out, nil
}

// nameTaken reports whether another group of the organization has name.
func (m *MemoryRepository) nameTaken(orgID, id, name string) bool {
	for _, g := range m.mgroups {
		if g.ID != id && g.OrganizationID == orgID && g.Name == name {
			return true
		}
	}
	return false
}

// CreateGroup implements ProfileStore.
func (m *MemoryRepository) CreateGroup(ctx context.Context, in *MonitoringGroup) (*MonitoringGroup, error) {
	if in.ClientID == "" && !orgWide(ctx) {
		return nil, errOrgWideGroup
	}
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.ensureMonitoring()
	if in.ClientID != "" {
		if ok, _ := (&memWriteTx{m: m, s: &s}).ClientVisible(ctx, in.ClientID); !ok {
			return nil, unprocessable("client_id", "client not found")
		}
	}
	if !s.writes(in.ClientID, "") {
		return nil, errRowSecurity
	}
	if m.nameTaken(s.orgID, "", in.Name) {
		return nil, ErrGroupNameTaken
	}
	g := *in
	g.OrganizationID = s.orgID
	if g.ID, err = newID(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	g.CreatedAt, g.UpdatedAt = now, now
	stored := g
	m.mgroups[g.ID] = &stored
	return &g, nil
}

// writableGroup returns the stored group the scope may change.
func (m *MemoryRepository) writableGroup(ctx context.Context, s *memScope, id string) (*MonitoringGroup, error) {
	g, err := m.visibleGroup(s, id)
	if err != nil {
		return nil, err
	}
	if g.ClientID == "" && !orgWide(ctx) {
		return nil, errOrgWideOnly
	}
	if !s.writes(g.ClientID, "") {
		return nil, ErrGroupNotFound
	}
	return g, nil
}

// UpdateGroup implements ProfileStore.
func (m *MemoryRepository) UpdateGroup(ctx context.Context, id string, u *GroupUpdate) (*MonitoringGroup, error) {
	var out MonitoringGroup
	err := m.monitoringWrite(ctx, func(s *memScope) (func(), error) {
		g, err := m.writableGroup(ctx, s, id)
		if err != nil {
			return nil, err
		}
		if u.Name != nil && m.nameTaken(s.orgID, id, *u.Name) {
			return nil, ErrGroupNameTaken
		}
		old := *g
		if u.Name != nil {
			g.Name = *u.Name
		}
		if u.Priority != nil {
			g.Priority = *u.Priority
		}
		g.UpdatedAt = time.Now().UTC()
		out = m.withCount(s, g)
		return func() { *g = old }, nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteGroup implements ProfileStore.
func (m *MemoryRepository) DeleteGroup(ctx context.Context, id string) (int, error) {
	deleted := 0
	err := m.monitoringWrite(ctx, func(s *memScope) (func(), error) {
		g, err := m.writableGroup(ctx, s, id)
		if err != nil {
			return nil, err
		}
		profiles := map[string]*MonitoringProfile{}
		for pid, p := range m.profiles {
			if p.Level == LevelGroup && p.GroupID == id {
				profiles[pid] = p
				delete(m.profiles, pid)
			}
		}
		members := map[memMember]*MonitoringGroupMember{}
		for k, mem := range m.members {
			if k.groupID == id {
				members[k] = mem
				delete(m.members, k)
			}
		}
		delete(m.mgroups, id)
		deleted = len(profiles)
		return func() {
			m.mgroups[id] = g
			for k, v := range profiles {
				m.profiles[k] = v
			}
			for k, v := range members {
				m.members[k] = v
			}
		}, nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// ListGroupMembers implements ProfileStore.
func (m *MemoryRepository) ListGroupMembers(ctx context.Context, groupID string, limit, offset int) ([]MonitoringGroupMember, int, error) {
	s, err := scopeOf(ctx)
	if err != nil {
		return nil, 0, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, err := m.visibleGroup(&s, groupID); err != nil {
		return nil, 0, err
	}
	out := make([]MonitoringGroupMember, 0)
	for k, mem := range m.members {
		if k.groupID == groupID && s.reads(mem.ClientID, mem.SiteID) {
			out = append(out, *mem)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CIID < out[j].CIID })
	return pageOf(out, limit, offset), len(out), nil
}

// AddGroupMembers implements ProfileStore.
func (m *MemoryRepository) AddGroupMembers(ctx context.Context, groupID string, ciIDs []string) ([]MonitoringGroupMember, error) {
	var out []MonitoringGroupMember
	err := m.monitoringWrite(ctx, func(s *memScope) (func(), error) {
		g, err := m.writableGroup(ctx, s, groupID)
		if err != nil {
			return nil, err
		}
		t := &memWriteTx{m: m, s: s}
		type ciScope struct{ client, site string }
		scopes := make(map[string]ciScope, len(ciIDs))
		for _, id := range ciIDs {
			client, site, ok, _ := t.CI(ctx, id)
			if !ok {
				return nil, ErrCINotFound
			}
			if g.ClientID != "" && client != g.ClientID {
				return nil, errMemberOfOtherClient
			}
			if !s.writes(client, site) {
				return nil, errRowSecurity
			}
			scopes[id] = ciScope{client, site}
		}
		var added []memMember
		now := time.Now().UTC()
		out = make([]MonitoringGroupMember, 0, len(ciIDs))
		for _, id := range ciIDs {
			k := memMember{groupID, id}
			if _, ok := m.members[k]; !ok {
				m.members[k] = &MonitoringGroupMember{GroupID: groupID, CIID: id, OrganizationID: s.orgID,
					ClientID: scopes[id].client, SiteID: scopes[id].site, CreatedAt: now}
				added = append(added, k)
			}
			out = append(out, *m.members[k])
		}
		sort.Slice(out, func(i, j int) bool { return out[i].CIID < out[j].CIID })
		return func() {
			for _, k := range added {
				delete(m.members, k)
			}
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// RemoveGroupMember implements ProfileStore.
func (m *MemoryRepository) RemoveGroupMember(ctx context.Context, groupID, ciID string) error {
	return m.monitoringWrite(ctx, func(s *memScope) (func(), error) {
		if _, err := m.writableGroup(ctx, s, groupID); err != nil {
			return nil, err
		}
		k := memMember{groupID, ciID}
		mem, ok := m.members[k]
		if !ok || !s.writes(mem.ClientID, mem.SiteID) {
			return nil, ErrMemberNotFound
		}
		delete(m.members, k)
		return func() { m.members[k] = mem }, nil
	})
}
