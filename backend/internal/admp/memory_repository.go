package admp

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// MemoryRepository is the ADMP cache of --no-db with the rules of the
// tables: unique external id per account, the address rule, no personal
// properties, a service only on a connection of its account, a surrogate on
// one connection only, the client of the account and, for an
// organization-wide account, the client derived from the ports; the rows of
// a deleted account go with it (the cascade). It knows no CIs: the client
// and site of a port are those the caller passes, and personal surrogates
// are not shredded (there is no vault without a database).
type MemoryRepository struct {
	mu          sync.Mutex
	accounts    AccountRepository
	connections map[string]*Connection
	services    map[string]*Service
	ports       map[string][]ServicePort
	// inventory holds the ports of the inventory by account and key.
	inventory map[string]map[string]*AccessPort
	// inventoryAt holds the time of the last inventory by account.
	inventoryAt map[string]time.Time
}

// NewMemoryRepository creates the in-memory cache; accounts gives the
// client of an account.
func NewMemoryRepository(accounts AccountRepository) *MemoryRepository {
	return &MemoryRepository{accounts: accounts, connections: map[string]*Connection{}, services: map[string]*Service{},
		ports: map[string][]ServicePort{}, inventory: map[string]map[string]*AccessPort{},
		inventoryAt: map[string]time.Time{}}
}

var _ Repository = (*MemoryRepository)(nil)

// accountClient returns the client of an account ("" for an
// organization-wide one) and whether the account exists, regardless of the
// caller's scope (the triggers of admp_cache read the account widened).
func (m *MemoryRepository) accountClient(ctx context.Context, orgID, accountID string) (string, bool) {
	if m.accounts == nil {
		return "", true
	}
	if mem, ok := m.accounts.(interface {
		clientOf(orgID, id string) (string, bool)
	}); ok {
		return mem.clientOf(orgID, accountID)
	}
	scope := database.OrgWideScope(orgID, "")
	a, err := m.accounts.GetAccount(database.ContextWithTenantScope(ctx, &scope), orgID, accountID)
	if err != nil {
		return "", false
	}
	return a.ClientID, true
}

// cacheVisible reports whether the caller of ctx reads a cache row of
// clientID: its clients' and the shared ones without client (E-09).
func cacheVisible(ctx context.Context, clientID string) bool {
	return clientID == "" || inScope(ctx, clientID)
}

// dropOrphans removes the rows whose account no longer exists, as the
// cascade of admp_account does on PostgreSQL; the caller holds the lock.
func (m *MemoryRepository) dropOrphans(ctx context.Context) {
	for id, s := range m.services {
		if _, ok := m.accountClient(ctx, s.OrganizationID, s.AccountID); !ok {
			delete(m.services, id)
			delete(m.ports, id)
		}
	}
	for id, c := range m.connections {
		if _, ok := m.accountClient(ctx, c.OrganizationID, c.AccountID); !ok {
			delete(m.connections, id)
		}
	}
	for account, ports := range m.inventory {
		for _, p := range ports {
			if _, ok := m.accountClient(ctx, p.OrganizationID, account); !ok {
				delete(m.inventory, account)
				delete(m.inventoryAt, account)
			}
			break
		}
	}
}

// derivedClient is the one client among the ports with a client, else "".
func derivedClient(ports []ServicePort) string {
	client := ""
	for i := range ports {
		switch {
		case ports[i].ClientID == "":
		case client == "":
			client = ports[i].ClientID
		case client != ports[i].ClientID:
			return ""
		}
	}
	return client
}

// refreshClients drops the rows of deleted accounts and re-derives the
// clients of the services and connections of organization-wide accounts;
// the caller holds the lock.
func (m *MemoryRepository) refreshClients(ctx context.Context) {
	m.dropOrphans(ctx)
	byConnection := map[string][]ServicePort{}
	for id, s := range m.services {
		if client, _ := m.accountClient(ctx, s.OrganizationID, s.AccountID); client != "" {
			s.ClientID = client
		} else {
			s.ClientID = derivedClient(m.ports[id])
		}
		if s.ConnectionID != "" {
			byConnection[s.ConnectionID] = append(byConnection[s.ConnectionID], m.ports[id]...)
		}
	}
	for id, c := range m.connections {
		if client, _ := m.accountClient(ctx, c.OrganizationID, c.AccountID); client != "" {
			c.ClientID = client
		} else {
			c.ClientID = derivedClient(byConnection[id])
		}
	}
}

func cloneConnection(c *Connection) Connection {
	out := *c
	out.Properties = maps.Clone(c.Properties)
	return out
}

func cloneService(s *Service) Service {
	out := *s
	out.Properties = maps.Clone(s.Properties)
	out.Ports = nil
	return out
}

// UpsertConnection implements Repository.
func (m *MemoryRepository) UpsertConnection(ctx context.Context, c *Connection) error {
	if err := checkConnection(c); err != nil {
		return err
	}
	// The account must exist and its rows be writable by the caller (the
	// write rule of the policies: a principal restricted to clients writes
	// only rows of its clients).
	if client, ok := m.accountClient(ctx, c.OrganizationID, c.AccountID); !ok || !inScope(ctx, client) {
		return ErrInvalidCacheEntry
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, other := range m.connections {
		if other.OrganizationID == c.OrganizationID && other.AccountID == c.AccountID && other.ExternalID == c.ExternalID {
			c.ID = id
		}
	}
	for id, other := range m.connections {
		if id == c.ID || other.OrganizationID != c.OrganizationID {
			continue
		}
		for _, subject := range []string{c.HolderSubjectID, c.AddressSubjectID} {
			if subject != "" && (subject == other.HolderSubjectID || subject == other.AddressSubjectID) {
				return ErrInvalidCacheEntry
			}
		}
	}
	if c.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		c.ID = id
	}
	stored := cloneConnection(c)
	m.connections[c.ID] = &stored
	m.refreshClients(ctx)
	*c = cloneConnection(m.connections[c.ID])
	return nil
}

// UpsertService implements Repository.
func (m *MemoryRepository) UpsertService(ctx context.Context, s *Service) error {
	if err := checkService(s); err != nil {
		return err
	}
	// The account must exist and its rows be writable by the caller (the
	// write rule of the policies: a principal restricted to clients writes
	// only rows of its clients).
	if client, ok := m.accountClient(ctx, s.OrganizationID, s.AccountID); !ok || !inScope(ctx, client) {
		return ErrInvalidCacheEntry
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.ConnectionID != "" {
		if c, ok := m.connections[s.ConnectionID]; !ok || c.OrganizationID != s.OrganizationID || c.AccountID != s.AccountID {
			return ErrInvalidCacheEntry
		}
	}
	for id, other := range m.services {
		if other.OrganizationID == s.OrganizationID && other.AccountID == s.AccountID && other.ExternalID == s.ExternalID {
			s.ID = id
		}
	}
	if s.ID == "" {
		id, err := newID()
		if err != nil {
			return err
		}
		s.ID = id
	}
	stored := cloneService(s)
	m.services[s.ID] = &stored
	m.refreshClients(ctx)
	*s = cloneService(m.services[s.ID])
	return nil
}

// ReplaceServicePorts implements Repository.
func (m *MemoryRepository) ReplaceServicePorts(ctx context.Context, orgID, serviceID string, ports []ServicePort) ([]ServicePort, error) {
	if err := checkPorts(ports); err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	s, ok := m.services[serviceID]
	if !ok || s.OrganizationID != orgID || !cacheVisible(ctx, s.ClientID) {
		return nil, ErrCacheNotFound
	}
	out := make([]ServicePort, 0, len(ports))
	for i := range ports {
		p := ports[i]
		id, err := newID()
		if err != nil {
			return nil, err
		}
		p.ID, p.OrganizationID, p.ServiceID = id, orgID, serviceID
		if p.ClientID == "" && p.CIID == "" {
			p.ClientID, _ = m.accountClient(ctx, orgID, s.AccountID)
		}
		out = append(out, p)
	}
	m.ports[serviceID] = out
	m.refreshClients(ctx)
	return slices.Clone(out), nil
}

// GetConnection implements Repository.
func (m *MemoryRepository) GetConnection(ctx context.Context, orgID, id string) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	c, ok := m.connections[id]
	if !ok || c.OrganizationID != orgID || !cacheVisible(ctx, c.ClientID) {
		return nil, ErrCacheNotFound
	}
	out := cloneConnection(c)
	return &out, nil
}

// ListByInterface implements Repository.
func (m *MemoryRepository) ListByInterface(ctx context.Context, orgID, interfaceID string) ([]Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	out := []Service{}
	for id, s := range m.services {
		if s.OrganizationID != orgID {
			continue
		}
		var linked []ServicePort
		for i := range m.ports[id] {
			if p := &m.ports[id][i]; p.NetworkInterfaceID == interfaceID && cacheVisible(ctx, p.ClientID) {
				linked = append(linked, *p)
			}
		}
		if len(linked) == 0 || !cacheVisible(ctx, s.ClientID) {
			continue
		}
		slices.SortFunc(linked, func(a, b ServicePort) int { return cmp.Compare(a.PortKey, b.PortKey) })
		row := cloneService(s)
		row.Ports = linked
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b Service) int {
		return cmp.Or(cmp.Compare(a.ExternalID, b.ExternalID), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// ListByCI implements Repository.
func (m *MemoryRepository) ListByCI(ctx context.Context, orgID, ciID string) ([]Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	ids := map[string]bool{}
	for id, s := range m.services {
		if s.OrganizationID != orgID || s.ConnectionID == "" {
			continue
		}
		for i := range m.ports[id] {
			if p := &m.ports[id][i]; p.CIID == ciID && cacheVisible(ctx, p.ClientID) {
				ids[s.ConnectionID] = true
			}
		}
	}
	out := []Connection{}
	for id := range ids {
		if c, ok := m.connections[id]; ok && cacheVisible(ctx, c.ClientID) {
			out = append(out, cloneConnection(c))
		}
	}
	slices.SortFunc(out, func(a, b Connection) int {
		return cmp.Or(cmp.Compare(a.Carrier, b.Carrier), cmp.Compare(a.ExternalID, b.ExternalID), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// ListByConnection implements Repository.
func (m *MemoryRepository) ListByConnection(ctx context.Context, orgID, connectionID string) ([]Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	out := []Service{}
	for id, s := range m.services {
		if s.OrganizationID != orgID || s.ConnectionID != connectionID || connectionID == "" || !cacheVisible(ctx, s.ClientID) {
			continue
		}
		row := cloneService(s)
		for i := range m.ports[id] {
			if p := m.ports[id][i]; cacheVisible(ctx, p.ClientID) {
				row.Ports = append(row.Ports, p)
			}
		}
		slices.SortFunc(row.Ports, func(a, b ServicePort) int { return cmp.Compare(a.PortKey, b.PortKey) })
		out = append(out, row)
	}
	slices.SortFunc(out, func(a, b Service) int {
		return cmp.Or(cmp.Compare(a.ExternalID, b.ExternalID), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// AffectedConnections implements Repository with the rules of the
// PostgreSQL statement.
func (m *MemoryRepository) AffectedConnections(ctx context.Context, orgID string, ciIDs, interfaceIDs []string) ([]AffectedConnection, error) {
	out := []AffectedConnection{}
	if len(ciIDs) == 0 && len(interfaceIDs) == 0 {
		return out, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	affected := func(p *ServicePort) bool {
		return (p.CIID != "" && slices.Contains(ciIDs, p.CIID)) ||
			(p.NetworkInterfaceID != "" && slices.Contains(interfaceIDs, p.NetworkInterfaceID))
	}
	// visiblePorts are the ports of a service the caller sees.
	visiblePorts := func(serviceID string) []*ServicePort {
		var ports []*ServicePort
		for i := range m.ports[serviceID] {
			if p := &m.ports[serviceID][i]; cacheVisible(ctx, p.ClientID) {
				ports = append(ports, p)
			}
		}
		return ports
	}
	for id, s := range m.services {
		if s.OrganizationID != orgID || s.ConnectionID == "" || !cacheVisible(ctx, s.ClientID) {
			continue
		}
		c, ok := m.connections[s.ConnectionID]
		if !ok || !cacheVisible(ctx, c.ClientID) {
			continue
		}
		for _, hit := range visiblePorts(id) {
			if !affected(hit) {
				continue
			}
			via := []string{}
			for otherID, other := range m.services {
				if otherID != id && (other.ConnectionID != c.ID || !cacheVisible(ctx, other.ClientID)) {
					continue
				}
				for _, p := range visiblePorts(otherID) {
					if p == hit || p.CIID == "" || slices.Contains(ciIDs, p.CIID) ||
						(p.NetworkInterfaceID != "" && slices.Contains(interfaceIDs, p.NetworkInterfaceID)) {
						continue
					}
					if !slices.Contains(via, p.CIID) {
						via = append(via, p.CIID)
					}
				}
			}
			slices.Sort(via)
			fetched := c.FetchedAt
			if s.FetchedAt.Before(fetched) {
				fetched = s.FetchedAt
			}
			out = append(out, AffectedConnection{ConnectionID: c.ID, ExternalID: c.ExternalID, AccountID: c.AccountID,
				Carrier: c.Carrier, ContractID: c.ContractID, ServiceID: s.ID, ServiceKind: s.Kind, PortKey: hit.PortKey,
				CIID: hit.CIID, NetworkInterfaceID: hit.NetworkInterfaceID, Stale: c.Stale || s.Stale, FetchedAt: fetched,
				Redundant: len(via) > 0, RedundantVia: via, serviceExternalID: s.ExternalID, connectionStale: c.Stale,
				serviceStale: s.Stale})
		}
	}
	sortAffected(out)
	return out, nil
}

// ReplaceAccessPorts implements Repository.
func (m *MemoryRepository) ReplaceAccessPorts(ctx context.Context, orgID, accountID string, ports []AccessPort, fetchedAt time.Time) error {
	if err := checkAccessPorts(ports); err != nil {
		return err
	}
	client, ok := m.accountClient(ctx, orgID, accountID)
	if !ok || !inScope(ctx, client) {
		return ErrInvalidCacheEntry
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.inventory[accountID]
	next := map[string]*AccessPort{}
	for i := range ports {
		p := ports[i]
		if old, found := prev[p.ExternalID]; found {
			p.ID, p.NetworkInterfaceID, p.CIID, p.MatchedBy, p.ExpiresAt = old.ID, old.NetworkInterfaceID, old.CIID, old.MatchedBy, old.ExpiresAt
			p.ClientID, p.SiteID = old.ClientID, old.SiteID
		} else {
			id, err := newID()
			if err != nil {
				return err
			}
			p.ID, p.MatchedBy, p.ExpiresAt, p.ClientID = id, MatchedByNone, fetchedAt, client
			p.NetworkInterfaceID, p.CIID, p.SiteID = "", "", ""
		}
		p.OrganizationID, p.AccountID, p.FetchedAt = orgID, accountID, fetchedAt
		next[p.ExternalID] = &p
	}
	m.inventory[accountID] = next
	m.inventoryAt[accountID] = fetchedAt
	return nil
}

// MatchAccessPorts implements Repository. The memory store knows no CIs
// and interfaces: every non-manual port is unmatched.
func (m *MemoryRepository) MatchAccessPorts(_ context.Context, _, accountID string) (MatchResult, error) {
	out := MatchResult{UnmatchedPorts: []UnmatchedPort{}, Rules: map[string]int{}}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, p := range m.inventory[accountID] {
		if p.MatchedBy == MatchedByManual && p.NetworkInterfaceID != "" {
			out.Matched++
			continue
		}
		if p.MatchedBy == MatchedByManual {
			continue
		}
		p.MatchedBy, p.NetworkInterfaceID, p.CIID = MatchedByNone, "", ""
		out.Rules[MatchRuleNone]++
		out.Unmatched++
		out.UnmatchedPorts = append(out.UnmatchedPorts, UnmatchedPort{PortKey: p.ExternalID, NodeName: p.NodeName,
			UnitIndex: p.UnitIndex, PortIndex: p.PortIndex})
	}
	finishMatch(&out)
	return out, nil
}

// accessPorts returns the visible ports of an account that keep holds,
// ordered by order.
func (m *MemoryRepository) accessPorts(ctx context.Context, accountID string, keep func(p *AccessPort) bool,
	order func(a, b AccessPort) int) []AccessPort {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []AccessPort{}
	for _, p := range m.inventory[accountID] {
		if keep(p) && cacheVisible(ctx, p.ClientID) {
			out = append(out, *p)
		}
	}
	slices.SortFunc(out, order)
	return out
}

var byExternalID = func(a, b AccessPort) int { return cmp.Compare(a.ExternalID, b.ExternalID) }

// AccessPortsByInterface implements Repository.
func (m *MemoryRepository) AccessPortsByInterface(ctx context.Context, _, accountID, interfaceID string) ([]AccessPort, error) {
	return m.accessPorts(ctx, accountID, func(p *AccessPort) bool { return p.NetworkInterfaceID == interfaceID && interfaceID != "" },
		byExternalID), nil
}

// ListDuePorts implements Repository.
func (m *MemoryRepository) ListDuePorts(ctx context.Context, _, accountID string, before time.Time) ([]AccessPort, error) {
	return m.accessPorts(ctx, accountID, func(p *AccessPort) bool { return p.NetworkInterfaceID != "" && p.ExpiresAt.Before(before) },
		func(a, b AccessPort) int { return cmp.Or(a.ExpiresAt.Compare(b.ExpiresAt), byExternalID(a, b)) }), nil
}

// InventoryFetchedAt implements Repository.
func (m *MemoryRepository) InventoryFetchedAt(_ context.Context, _, accountID string) (time.Time, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.inventoryAt[accountID], nil
}

// TouchAccessPort implements Repository.
func (m *MemoryRepository) TouchAccessPort(_ context.Context, _, accountID, portKey string, expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if p := m.inventory[accountID][strings.TrimSpace(portKey)]; p != nil {
		p.ExpiresAt = expiresAt
	}
	return nil
}

// ReplacePortServices implements Repository.
func (m *MemoryRepository) ReplacePortServices(ctx context.Context, orgID, accountID, portKey string, serviceIDs []string,
	expiresAt time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	port := m.inventory[accountID][portKey]
	for id, s := range m.services {
		if s.OrganizationID != orgID || s.AccountID != accountID {
			continue
		}
		wanted := slices.Contains(serviceIDs, id)
		kept := m.ports[id][:0:0]
		found := false
		for i := range m.ports[id] {
			p := &m.ports[id][i]
			if p.PortKey != portKey {
				kept = append(kept, *p)
				continue
			}
			found = true
			switch {
			case p.MatchedBy == MatchedByManual:
				kept = append(kept, *p)
			case wanted:
				kept = append(kept, portRow(p, port))
			}
		}
		if wanted && !found {
			rowID, err := newID()
			if err != nil {
				return err
			}
			kept = append(kept, portRow(&ServicePort{ID: rowID, OrganizationID: orgID, ServiceID: id, PortKey: portKey}, port))
		}
		m.ports[id] = kept
	}
	if port != nil {
		port.ExpiresAt = expiresAt
	}
	m.refreshClients(ctx)
	return nil
}

// portRow takes interface, CI, match and scope of the inventory port into
// a row of admp_service_port.
func portRow(row *ServicePort, port *AccessPort) ServicePort {
	p := *row
	p.NetworkInterfaceID, p.CIID, p.MatchedBy, p.ClientID, p.SiteID = "", "", MatchedByNone, "", ""
	if port != nil {
		p.ClientID = port.ClientID
		if port.NetworkInterfaceID != "" {
			p.NetworkInterfaceID, p.CIID, p.MatchedBy, p.SiteID = port.NetworkInterfaceID, port.CIID, port.MatchedBy, port.SiteID
		}
	}
	return p
}

// ConnectionsByID implements Repository.
func (m *MemoryRepository) ConnectionsByID(ctx context.Context, orgID string, ids []string) ([]Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	out := []Connection{}
	seen := map[string]bool{}
	for _, id := range ids {
		if c, ok := m.connections[id]; ok && !seen[id] && c.OrganizationID == orgID && cacheVisible(ctx, c.ClientID) {
			seen[id] = true
			out = append(out, cloneConnection(c))
		}
	}
	slices.SortFunc(out, func(a, b Connection) int {
		return cmp.Or(cmp.Compare(a.ExternalID, b.ExternalID), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// MarkStale implements Repository.
func (m *MemoryRepository) MarkStale(ctx context.Context, orgID, kind, id, errorClass string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	switch kind {
	case KindConnection:
		if c, ok := m.connections[id]; ok && c.OrganizationID == orgID && inScope(ctx, c.ClientID) {
			c.Stale, c.LastErrorClass = true, errorClass
			return nil
		}
	case KindService:
		if s, ok := m.services[id]; ok && s.OrganizationID == orgID && inScope(ctx, s.ClientID) {
			s.Stale, s.LastErrorClass = true, errorClass
			return nil
		}
	}
	return ErrCacheNotFound
}

// DeleteExpired implements Repository.
func (m *MemoryRepository) DeleteExpired(ctx context.Context, orgID string, before time.Time) (Expired, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out Expired
	for id, s := range m.services {
		if s.OrganizationID == orgID && s.ExpiresAt.Before(before) {
			delete(m.services, id)
			delete(m.ports, id)
			out.Services++
		}
	}
	for id, c := range m.connections {
		if c.OrganizationID == orgID && c.ExpiresAt.Before(before) {
			delete(m.connections, id)
			out.Connections++
			for _, s := range m.services {
				if s.ConnectionID == id {
					s.ConnectionID = ""
				}
			}
		}
	}
	m.refreshClients(ctx)
	return out, nil
}

// ConnectionByExternal implements Repository.
func (m *MemoryRepository) ConnectionByExternal(ctx context.Context, orgID, accountID, externalID string) (*Connection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	for _, c := range m.connections {
		if c.OrganizationID == orgID && c.AccountID == accountID && c.ExternalID == externalID && cacheVisible(ctx, c.ClientID) {
			out := cloneConnection(c)
			return &out, nil
		}
	}
	return nil, ErrCacheNotFound
}

// ServiceByExternal implements Repository.
func (m *MemoryRepository) ServiceByExternal(ctx context.Context, orgID, accountID, externalID string) (*Service, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	for id, s := range m.services {
		if s.OrganizationID == orgID && s.AccountID == accountID && s.ExternalID == externalID && cacheVisible(ctx, s.ClientID) {
			out := cloneService(s)
			out.Ports = slices.Clone(m.ports[id])
			slices.SortFunc(out.Ports, func(a, b ServicePort) int { return cmp.Compare(a.PortKey, b.PortKey) })
			return &out, nil
		}
	}
	return nil, ErrCacheNotFound
}

// ListDueLinked implements Repository.
func (m *MemoryRepository) ListDueLinked(ctx context.Context, orgID, accountID string, before time.Time) ([]DueObject, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.refreshClients(ctx)
	out := []DueObject{}
	connections := map[string]bool{}
	for id, s := range m.services {
		if s.OrganizationID != orgID || s.AccountID != accountID || !cacheVisible(ctx, s.ClientID) {
			continue
		}
		if !slices.ContainsFunc(m.ports[id], func(p ServicePort) bool { return p.NetworkInterfaceID != "" || p.CIID != "" }) {
			continue
		}
		if s.ConnectionID != "" {
			connections[s.ConnectionID] = true
		}
		if s.ExpiresAt.Before(before) {
			out = append(out, DueObject{Kind: KindService, ExternalID: s.ExternalID, ExpiresAt: s.ExpiresAt})
		}
	}
	for id := range connections {
		if c, ok := m.connections[id]; ok && c.ExpiresAt.Before(before) && cacheVisible(ctx, c.ClientID) {
			out = append(out, DueObject{Kind: KindConnection, ExternalID: c.ExternalID, ExpiresAt: c.ExpiresAt})
		}
	}
	slices.SortFunc(out, func(a, b DueObject) int {
		return cmp.Or(a.ExpiresAt.Compare(b.ExpiresAt), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.ExternalID, b.ExternalID))
	})
	return out, nil
}
