package admp

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// Errors of the cache.
var (
	ErrCacheNotFound = errors.New("admp cache entry not found")
	// ErrPersonalProperty: properties hold a personal key (pii.IsPersonalKey);
	// personal values reach the cache only as surrogates (Konvention 16).
	ErrPersonalProperty = errors.New("admp properties must not hold personal values")
	// ErrInvalidCacheEntry: a required member is missing or invalid.
	ErrInvalidCacheEntry = errors.New("invalid admp cache entry")
)

// Repository is the cache of ADMP data; PGRepository and MemoryRepository
// implement it. Every upsert runs in one transaction of its object.
type Repository interface {
	// UpsertConnection stores a connection by account and external id and
	// returns it with its id; a replaced holder or address surrogate is
	// deleted (crypto-shredding). A new surrogate must have been created
	// for the connection in the same transaction (the caller joins it,
	// database.WithRequestTenantTx) and be linked to no other connection;
	// anything else is ErrInvalidCacheEntry on PostgreSQL.
	UpsertConnection(ctx context.Context, c *Connection) error
	// UpsertService stores a service by account and external id; its
	// connection must belong to the same account (ErrInvalidCacheEntry).
	UpsertService(ctx context.Context, s *Service) error
	// ReplaceServicePorts replaces the ports of a service.
	ReplaceServicePorts(ctx context.Context, orgID, serviceID string, ports []ServicePort) ([]ServicePort, error)
	GetConnection(ctx context.Context, orgID, id string) (*Connection, error)
	// ListByInterface returns the services with a port on the interface,
	// each with its ports on it.
	ListByInterface(ctx context.Context, orgID, interfaceID string) ([]Service, error)
	// ListByCI returns the connections of the services with a port on the
	// CI.
	ListByCI(ctx context.Context, orgID, ciID string) ([]Connection, error)
	// MarkStale keeps an entry after a failed refresh with stale and the
	// error class (ADM-03).
	MarkStale(ctx context.Context, orgID, kind, id, errorClass string) error
	// DeleteExpired removes the connections and services with expires_at
	// before the time, the connections with their personal surrogates.
	DeleteExpired(ctx context.Context, orgID string, before time.Time) (Expired, error)

	// The refresh (WP-929):

	// ConnectionByExternal returns the cached connection of an account by
	// its external id (ErrCacheNotFound).
	ConnectionByExternal(ctx context.Context, orgID, accountID, externalID string) (*Connection, error)
	// ServiceByExternal returns the cached service of an account by its
	// external id with all its ports (ErrCacheNotFound).
	ServiceByExternal(ctx context.Context, orgID, accountID, externalID string) (*Service, error)
	// ListDueLinked returns the services of the account with a port link
	// (an interface or a CI) and their connections whose expires_at lies
	// before the time, the earliest first.
	ListDueLinked(ctx context.Context, orgID, accountID string, before time.Time) ([]DueObject, error)
}

// DueObject is a cached object the periodic refresh renews.
type DueObject struct {
	Kind       string
	ExternalID string
	ExpiresAt  time.Time
}

// checkProperties refuses a personal key at any depth of properties.
func checkProperties(props map[string]any) error {
	var walk func(v any) bool
	walk = func(v any) bool {
		switch t := v.(type) {
		case map[string]any:
			for k, inner := range t {
				if pii.IsPersonalKey(k) || walk(inner) {
					return true
				}
			}
		case []any:
			if slices.ContainsFunc(t, walk) {
				return true
			}
		}
		return false
	}
	if walk(props) {
		return ErrPersonalProperty
	}
	return nil
}

func checkSubject(id string) error {
	if id != "" && !api.IsUUID(id) {
		return ErrInvalidCacheEntry
	}
	return nil
}

// checkConnection validates a connection before it is stored.
func checkConnection(c *Connection) error {
	c.ExternalID = strings.TrimSpace(c.ExternalID)
	switch {
	case !api.IsUUID(c.OrganizationID) || !api.IsUUID(c.AccountID) || c.ExternalID == "" || c.ExpiresAt.IsZero():
		return ErrInvalidCacheEntry
	case c.AddressSubjectID != "" && c.AddressText != "":
		return ErrInvalidCacheEntry
	}
	if err := checkSubject(c.HolderSubjectID); err != nil {
		return err
	}
	if err := checkSubject(c.AddressSubjectID); err != nil {
		return err
	}
	if c.Properties == nil {
		c.Properties = map[string]any{}
	}
	if c.FetchedAt.IsZero() {
		c.FetchedAt = time.Now().UTC()
	}
	return checkProperties(c.Properties)
}

// checkService validates a service before it is stored.
func checkService(s *Service) error {
	s.ExternalID = strings.TrimSpace(s.ExternalID)
	s.Kind = strings.TrimSpace(s.Kind)
	if !api.IsUUID(s.OrganizationID) || !api.IsUUID(s.AccountID) || s.ExternalID == "" || s.Kind == "" || s.ExpiresAt.IsZero() ||
		(s.ConnectionID != "" && !api.IsUUID(s.ConnectionID)) {
		return ErrInvalidCacheEntry
	}
	if s.Properties == nil {
		s.Properties = map[string]any{}
	}
	if s.FetchedAt.IsZero() {
		s.FetchedAt = time.Now().UTC()
	}
	return checkProperties(s.Properties)
}

// checkPorts validates the ports of a service: unique keys, a known
// matching, valid references.
func checkPorts(ports []ServicePort) error {
	seen := map[string]bool{}
	for i := range ports {
		p := &ports[i]
		p.PortKey = strings.TrimSpace(p.PortKey)
		if p.MatchedBy == "" {
			p.MatchedBy = MatchedByNone
		}
		switch {
		case p.PortKey == "" || seen[p.PortKey]:
			return ErrInvalidCacheEntry
		case p.MatchedBy != MatchedByRule && p.MatchedBy != MatchedByManual && p.MatchedBy != MatchedByNone:
			return ErrInvalidCacheEntry
		case (p.NetworkInterfaceID != "" && !api.IsUUID(p.NetworkInterfaceID)) || (p.CIID != "" && !api.IsUUID(p.CIID)):
			return ErrInvalidCacheEntry
		}
		seen[p.PortKey] = true
	}
	return nil
}
