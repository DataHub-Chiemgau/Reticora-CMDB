package admp

import (
	"context"
	"encoding/json"
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
	// anything else is ErrInvalidCacheEntry on PostgreSQL. PrivateAddress
	// is sealed into the PII vault in the same transaction (an unchanged
	// address keeps its surrogate, a changed one shreds the old); personal
	// keys of the properties are sealed as with a service (WP-1075). The
	// memory store keeps neither.
	UpsertConnection(ctx context.Context, c *Connection) error
	// ResolveHolders resolves the holders of a visible connection from the
	// PII vault and records the read in the audit log in the same
	// transaction (ADM-06, WP-1076). Unknown or invisible: ErrCacheNotFound;
	// without a holder: ErrHolderNotFound.
	ResolveHolders(ctx context.Context, orgID, connectionID string) ([]Holder, error)
	// DeleteConnection removes a connection with its personal surrogates
	// (ADMP answered 404, WP-1075); its services keep their row without
	// connection. Unknown or invisible: ErrCacheNotFound.
	DeleteConnection(ctx context.Context, orgID, id string) error
	// UpsertService stores a service by account and external id; its
	// connection must belong to the same account (ErrInvalidCacheEntry).
	// Personal values of its properties go into the PII vault in the same
	// transaction (WP-1074); the row holds only surrogate references.
	UpsertService(ctx context.Context, s *Service) error
	// ReplaceServicePorts replaces the ports of a service.
	ReplaceServicePorts(ctx context.Context, orgID, serviceID string, ports []ServicePort) ([]ServicePort, error)
	GetConnection(ctx context.Context, orgID, id string) (*Connection, error)
	// ListByInterface returns the services with a port on the interface,
	// each with its ports on it.
	ListByInterface(ctx context.Context, orgID, interfaceID string) ([]Service, error)
	// ListByCI returns the connections of the services with a port on the
	// CI, ordered by carrier and external id (WP-930: one set-based
	// statement).
	ListByCI(ctx context.Context, orgID, ciID string) ([]Connection, error)
	// ListByConnection returns the services of a connection, each with all
	// its ports, ordered by external id (WP-930).
	ListByConnection(ctx context.Context, orgID, connectionID string) ([]Service, error)
	// AffectedConnections returns the connections with a port on one of
	// ciIDs or interfaceIDs, one entry per connection and port, with their
	// redundancy (WP-931, impact.go); one set-based statement.
	AffectedConnections(ctx context.Context, orgID string, ciIDs, interfaceIDs []string) ([]AffectedConnection, error)
	// ConnectionsByID returns the visible connections among ids (WP-930);
	// an id the caller does not see is left out.
	ConnectionsByID(ctx context.Context, orgID string, ids []string) ([]Connection, error)
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

	// The port inventory (WP-1073, E-251):

	// ReplaceAccessPorts replaces the inventory of an account set-based:
	// a delivered port is stored with fetchedAt (a new one due at once),
	// its match kept; a port no longer delivered is deleted. fetchedAt is
	// the account's inventory time, also for an empty inventory.
	ReplaceAccessPorts(ctx context.Context, orgID, accountID string, ports []AccessPort, fetchedAt time.Time) error
	// MatchAccessPorts matches the non-manual ports of an account to
	// network interfaces (match.go); at most two statements.
	MatchAccessPorts(ctx context.Context, orgID, accountID string) (MatchResult, error)
	// AccessPortsByInterface returns the ports of an account matched to
	// the interface.
	AccessPortsByInterface(ctx context.Context, orgID, accountID, interfaceID string) ([]AccessPort, error)
	// InventoryFetchedAt returns the time of the account's last inventory
	// (an empty one too); zero without one.
	InventoryFetchedAt(ctx context.Context, orgID, accountID string) (time.Time, error)
	// TouchAccessPort sets expires_at of an inventory port without
	// changing its services: a port whose services fail with a lasting
	// class waits for its next turn instead of leading every run.
	TouchAccessPort(ctx context.Context, orgID, accountID, portKey string, expiresAt time.Time) error
	// ListDuePorts returns the matched ports of an account whose
	// expires_at lies before the time, the earliest first.
	ListDuePorts(ctx context.Context, orgID, accountID string, before time.Time) ([]AccessPort, error)
	// ReplacePortServices replaces the services of a port: the rows of
	// admp_service_port with the port key for the given services (ids of
	// admp_service of the account) take interface and match of the
	// inventory port, the rows of other services are deleted; a manual row
	// stays. The port's expires_at becomes expiresAt.
	ReplacePortServices(ctx context.Context, orgID, accountID, portKey string, serviceIDs []string, expiresAt time.Time) error
}

// DueObject is a cached object the periodic refresh renews.
type DueObject struct {
	Kind       string
	ExternalID string
	ExpiresAt  time.Time
}

func checkSubject(id string) error {
	if id != "" && !api.IsUUID(id) {
		return ErrInvalidCacheEntry
	}
	return nil
}

// checkConnection validates a connection before it is stored; its
// personal values are sealed by the store (sealProperties, PrivateAddress).
func checkConnection(c *Connection) error {
	c.ExternalID = strings.TrimSpace(c.ExternalID)
	c.PrivateAddress = strings.TrimSpace(c.PrivateAddress)
	switch {
	case !api.IsUUID(c.OrganizationID) || !api.IsUUID(c.AccountID) || c.ExternalID == "" || c.ExpiresAt.IsZero():
		return ErrInvalidCacheEntry
	case c.AddressSubjectID != "" && c.AddressText != "":
		return ErrInvalidCacheEntry
	case c.PrivateAddress != "" && (c.AddressText != "" || c.AddressSubjectID != ""):
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
	generic, err := pii.Generic(c.Properties)
	if err != nil {
		return ErrInvalidCacheEntry
	}
	props, ok := generic.(map[string]any)
	if !ok {
		return ErrInvalidCacheEntry
	}
	c.Properties = props
	return nil
}

// sealProperties replaces every value below a personal key
// (pii.IsPersonalKey) of props with the reference seal returns (WP-1074,
// Konvention 16); of a property entry {value, label, order} only the
// value. A string is sealed as it is, a number or boolean as its JSON
// text, so no personal scalar stays in plaintext. A surrogate reference
// already in props stays only when kept reports it (a reference the row
// carried before, kept with the last state); any other reference (one a
// source delivered) is neutralised to a redacted one, so a row never
// points to, reveals or shreds a subject of another object.
func sealProperties(props map[string]any, seal func(value string) (any, error), kept func(id string) bool) error {
	var walk func(v any, personal bool) (any, error)
	walk = func(v any, personal bool) (any, error) {
		switch t := v.(type) {
		case map[string]any:
			if id, ok := pii.RefID(t); ok {
				if id == "" || kept(id) {
					return t, nil
				}
				return redactedRef("")
			}
			if _, isRef := t[pii.RefKey]; isRef {
				// A malformed reference (more keys, no string) is no
				// value of the service either.
				return redactedRef("")
			}
			for k, inner := range t {
				// A property entry {value, label, order} of a personal key
				// seals its value; label and order describe the property.
				if entry, isEntry := inner.(map[string]any); isEntry && !personal && pii.IsPersonalKey(k) {
					if value, hasValue := entry["value"]; hasValue {
						out, err := walk(value, true)
						if err != nil {
							return nil, err
						}
						entry["value"] = out
						continue
					}
				}
				out, err := walk(inner, personal || pii.IsPersonalKey(k))
				if err != nil {
					return nil, err
				}
				t[k] = out
			}
			return t, nil
		case []any:
			for i, inner := range t {
				out, err := walk(inner, personal)
				if err != nil {
					return nil, err
				}
				t[i] = out
			}
			return t, nil
		case string:
			if personal && t != "" {
				return seal(t)
			}
		case float64, bool, json.Number:
			if personal {
				text, err := json.Marshal(t)
				if err != nil {
					return nil, err
				}
				return seal(string(text))
			}
		}
		return v, nil
	}
	_, err := walk(props, false)
	return err
}

// refIDs returns the surrogate ids referenced in v at any depth.
func refIDs(v any) []string {
	var out []string
	var walk func(v any)
	walk = func(v any) {
		if id, ok := pii.RefID(v); ok {
			if id != "" {
				out = append(out, id)
			}
			return
		}
		switch t := v.(type) {
		case map[string]any:
			for _, inner := range t {
				walk(inner)
			}
		case []any:
			for _, inner := range t {
				walk(inner)
			}
		}
	}
	walk(v)
	return out
}

// containsRef reports whether v holds a surrogate reference at any depth
// (also a redacted one).
func containsRef(v any) bool {
	switch t := v.(type) {
	case map[string]any:
		if _, ok := t[pii.RefKey]; ok {
			return true
		}
		for _, inner := range t {
			if containsRef(inner) {
				return true
			}
		}
	case []any:
		return slices.ContainsFunc(t, containsRef)
	}
	return false
}

// redactedRef is a sealed value without a vault (pii.SealTx without a
// default vault, the memory store).
func redactedRef(string) (any, error) { return map[string]any{pii.RefKey: ""}, nil }

// noKeptRefs keeps no reference (the memory store has no vault).
func noKeptRefs(string) bool { return false }

// checkService validates a service before it is stored; its personal
// values are sealed by the store (sealProperties).
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
	generic, err := pii.Generic(s.Properties)
	if err != nil {
		return ErrInvalidCacheEntry
	}
	props, ok := generic.(map[string]any)
	if !ok {
		return ErrInvalidCacheEntry
	}
	s.Properties = props
	return nil
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
