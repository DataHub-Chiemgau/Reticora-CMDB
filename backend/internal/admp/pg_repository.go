package admp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// PGRepository is the ADMP cache in PostgreSQL under row level security
// (migration 000297).
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates the PostgreSQL cache.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

var _ Repository = (*PGRepository)(nil)

const connectionColumns = `c.id::text, c.organization_id::text, c.account_id::text, c.external_id, COALESCE(c.carrier, ''),
	COALESCE(c.contract_id, ''), COALESCE(c.address_subject_id::text, ''), COALESCE(c.address_text, ''),
	COALESCE(c.holder_subject_id::text, ''), c.properties, c.fetched_at, c.expires_at, c.stale,
	COALESCE(c.last_error_class, ''), COALESCE(c.client_id::text, '')`

func scanConnection(row pgx.Row) (*Connection, error) {
	var c Connection
	var props []byte
	err := row.Scan(&c.ID, &c.OrganizationID, &c.AccountID, &c.ExternalID, &c.Carrier, &c.ContractID, &c.AddressSubjectID,
		&c.AddressText, &c.HolderSubjectID, &props, &c.FetchedAt, &c.ExpiresAt, &c.Stale, &c.LastErrorClass, &c.ClientID)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(props, &c.Properties); err != nil {
		return nil, fmt.Errorf("admp connection properties: %w", err)
	}
	return &c, nil
}

const serviceColumns = `s.id::text, s.organization_id::text, s.account_id::text, s.external_id, COALESCE(s.connection_id::text, ''),
	s.kind, s.properties, s.fetched_at, s.expires_at, s.stale, COALESCE(s.last_error_class, ''), COALESCE(s.client_id::text, '')`

func scanService(row pgx.Row) (*Service, error) {
	var s Service
	var props []byte
	err := row.Scan(&s.ID, &s.OrganizationID, &s.AccountID, &s.ExternalID, &s.ConnectionID, &s.Kind, &props, &s.FetchedAt,
		&s.ExpiresAt, &s.Stale, &s.LastErrorClass, &s.ClientID)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(props, &s.Properties); err != nil {
		return nil, fmt.Errorf("admp service properties: %w", err)
	}
	return &s, nil
}

const portColumns = `p.id::text, p.organization_id::text, p.service_id::text, p.port_key, COALESCE(p.network_interface_id::text, ''),
	COALESCE(p.ci_id::text, ''), p.matched_by, COALESCE(p.client_id::text, ''), COALESCE(p.site_id::text, '')`

func scanPort(row pgx.Row) (*ServicePort, error) {
	var p ServicePort
	err := row.Scan(&p.ID, &p.OrganizationID, &p.ServiceID, &p.PortKey, &p.NetworkInterfaceID, &p.CIID, &p.MatchedBy,
		&p.ClientID, &p.SiteID)
	return &p, err
}

// cacheError maps the constraints of the cache tables: a reference the
// caller does not see or that does not exist is an invalid entry.
func cacheError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23503" || pgErr.Code == "23514") {
		return fmt.Errorf("%w: %s", ErrInvalidCacheEntry, pgErr.ConstraintName)
	}
	return err
}

func nilIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// UpsertConnection implements Repository. The triggers of admp_connection
// shred a replaced holder or address (also when a concurrent upsert of the
// same connection won the insert) and refuse a subject that was not
// created in the same transaction or is linked to another connection
// (admp_connection_subject_check: ErrInvalidCacheEntry). The private
// address and the personal keys of the properties go into the PII vault
// in this transaction (WP-1075); the trigger of migration 000328 shreds
// the property surrogates the row no longer carries.
func (r *PGRepository) UpsertConnection(ctx context.Context, c *Connection) error {
	if err := checkConnection(c); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var previous connectionState
		err := tx.QueryRow(ctx, `SELECT COALESCE(address_subject_id::text, ''), COALESCE(holder_subject_id::text, ''), properties
			FROM admp_connection WHERE account_id = $1 AND external_id = $2 FOR UPDATE`, c.AccountID, c.ExternalID).
			Scan(&previous.addressSubjectID, &previous.holderSubjectID, &previous.properties)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return cacheError(err)
		}
		if serr := sealConnection(ctx, tx, c, &previous); serr != nil {
			return serr
		}
		props, err := json.Marshal(c.Properties)
		if err != nil {
			return err
		}
		stored, err := scanConnection(tx.QueryRow(ctx, `INSERT INTO admp_connection AS c (organization_id, account_id, external_id,
				carrier, contract_id, address_subject_id, address_text, holder_subject_id, properties, fetched_at, expires_at,
				stale, last_error_class)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
			ON CONFLICT (account_id, external_id) DO UPDATE SET carrier = EXCLUDED.carrier, contract_id = EXCLUDED.contract_id,
				address_subject_id = EXCLUDED.address_subject_id, address_text = EXCLUDED.address_text,
				holder_subject_id = EXCLUDED.holder_subject_id, properties = EXCLUDED.properties,
				fetched_at = EXCLUDED.fetched_at, expires_at = EXCLUDED.expires_at, stale = EXCLUDED.stale,
				last_error_class = EXCLUDED.last_error_class
			RETURNING `+connectionColumns,
			c.OrganizationID, c.AccountID, c.ExternalID, nilIfEmpty(c.Carrier), nilIfEmpty(c.ContractID),
			nilIfEmpty(c.AddressSubjectID), nilIfEmpty(c.AddressText), nilIfEmpty(c.HolderSubjectID), props, c.FetchedAt,
			c.ExpiresAt, c.Stale, nilIfEmpty(c.LastErrorClass)))
		if err != nil {
			return cacheError(err)
		}
		*c = *stored
		return nil
	})
}

// connectionState is the stored state of a connection an upsert builds
// on: its surrogates and properties (empty for a new one).
type connectionState struct {
	addressSubjectID string
	holderSubjectID  string
	properties       []byte
}

// sealConnection moves the personal values of c into the PII vault in tx
// (WP-1075, E-243): the private address keeps its surrogate when it is
// unchanged (with the same holder), else it becomes a new value subject
// (anchored at the holder) and the old one is shredded first; without a vault the address is not
// stored. The personal keys of the properties are sealed; only the
// references the row carried may stay.
func sealConnection(ctx context.Context, tx pgx.Tx, c *Connection, previous *connectionState) error {
	kept := map[string]bool{}
	if len(previous.properties) > 0 {
		var old any
		if err := json.Unmarshal(previous.properties, &old); err != nil {
			return err
		}
		for _, id := range refIDs(old) {
			kept[id] = true
		}
	}
	if err := sealProperties(c.Properties, func(v string) (any, error) {
		return pii.SealValueTx(ctx, tx, c.OrganizationID, v)
	}, func(id string) bool { return kept[id] }); err != nil {
		return err
	}
	address := c.PrivateAddress
	c.PrivateAddress = ""
	if address == "" {
		return nil
	}
	vault := pii.Default()
	if vault == nil {
		return nil
	}
	if previous.addressSubjectID != "" {
		subjects, err := vault.ResolveTx(ctx, tx, c.OrganizationID, []string{previous.addressSubjectID})
		if err != nil {
			return err
		}
		// The anchor must stay too: an address anchored at a holder that
		// goes would go with it.
		if s := subjects[previous.addressSubjectID]; s != nil && s.Data["v"] == address &&
			c.HolderSubjectID == previous.holderSubjectID {
			c.AddressSubjectID = previous.addressSubjectID
			return nil
		}
		// The address (or its holder) changed: the old surrogate goes before
		// the row points to the new one (crypto-shredding).
		if err = pii.DeleteTx(ctx, tx, c.OrganizationID, previous.addressSubjectID); err != nil && !errors.Is(err, pii.ErrNotFound) {
			return err
		}
	}
	sealCtx := ctx
	if c.HolderSubjectID != "" {
		sealCtx = pii.WithAnchor(ctx, c.HolderSubjectID)
	}
	ref, err := pii.SealValueTx(sealCtx, tx, c.OrganizationID, address)
	if err != nil {
		return err
	}
	if id, ok := pii.RefID(ref); ok {
		c.AddressSubjectID = id
	}
	return nil
}

// DeleteConnection implements Repository: the triggers of admp_connection
// shred the holder, the address and the property surrogates; the services
// keep their row (ON DELETE SET NULL).
func (r *PGRepository) DeleteConnection(ctx context.Context, orgID, id string) error {
	if !api.IsUUID(id) {
		return ErrCacheNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM admp_connection WHERE id = $1`, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCacheNotFound
		}
		return nil
	})
}

// UpsertService implements Repository.
func (r *PGRepository) UpsertService(ctx context.Context, s *Service) error {
	if err := checkService(s); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		// Personal values go into the vault in this transaction (WP-1074).
		// Only the references the row carries already may stay (the last
		// state kept after a failed fetch); the trigger of migration 000310
		// shreds those the new properties no longer carry.
		var previous []byte
		err := tx.QueryRow(ctx, `SELECT properties FROM admp_service WHERE account_id = $1 AND external_id = $2 FOR UPDATE`,
			s.AccountID, s.ExternalID).Scan(&previous)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return cacheError(err)
		}
		kept := map[string]bool{}
		if len(previous) > 0 {
			var old any
			if uerr := json.Unmarshal(previous, &old); uerr != nil {
				return uerr
			}
			for _, id := range refIDs(old) {
				kept[id] = true
			}
		}
		if serr := sealProperties(s.Properties, func(v string) (any, error) {
			return pii.SealValueTx(ctx, tx, s.OrganizationID, v)
		}, func(id string) bool { return kept[id] }); serr != nil {
			return serr
		}
		props, err := json.Marshal(s.Properties)
		if err != nil {
			return err
		}
		stored, err := scanService(tx.QueryRow(ctx, `INSERT INTO admp_service AS s (organization_id, account_id, external_id,
				connection_id, kind, properties, fetched_at, expires_at, stale, last_error_class)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (account_id, external_id) DO UPDATE SET connection_id = EXCLUDED.connection_id, kind = EXCLUDED.kind,
				properties = EXCLUDED.properties, fetched_at = EXCLUDED.fetched_at, expires_at = EXCLUDED.expires_at,
				stale = EXCLUDED.stale, last_error_class = EXCLUDED.last_error_class
			RETURNING `+serviceColumns,
			s.OrganizationID, s.AccountID, s.ExternalID, nilIfEmpty(s.ConnectionID), s.Kind, props, s.FetchedAt, s.ExpiresAt,
			s.Stale, nilIfEmpty(s.LastErrorClass)))
		if err != nil {
			return cacheError(err)
		}
		*s = *stored
		return nil
	})
}

// ReplaceServicePorts implements Repository.
func (r *PGRepository) ReplaceServicePorts(ctx context.Context, orgID, serviceID string, ports []ServicePort) ([]ServicePort, error) {
	if !api.IsUUID(serviceID) {
		return nil, ErrCacheNotFound
	}
	if err := checkPorts(ports); err != nil {
		return nil, err
	}
	out := []ServicePort{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var found bool
		if err := tx.QueryRow(ctx, `SELECT true FROM admp_service WHERE id = $1 FOR UPDATE`, serviceID).Scan(&found); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrCacheNotFound
			}
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM admp_service_port WHERE service_id = $1`, serviceID); err != nil {
			return err
		}
		for i := range ports {
			p := &ports[i]
			stored, err := scanPort(tx.QueryRow(ctx, `INSERT INTO admp_service_port AS p (organization_id, service_id, port_key,
					network_interface_id, ci_id, matched_by)
				VALUES ($1, $2, $3, $4, $5, $6) RETURNING `+portColumns,
				orgID, serviceID, p.PortKey, nilIfEmpty(p.NetworkInterfaceID), nilIfEmpty(p.CIID), p.MatchedBy))
			if err != nil {
				return cacheError(err)
			}
			out = append(out, *stored)
		}
		return nil
	})
	return out, err
}

// GetConnection implements Repository.
func (r *PGRepository) GetConnection(ctx context.Context, orgID, id string) (*Connection, error) {
	if !api.IsUUID(id) {
		return nil, ErrCacheNotFound
	}
	var out *Connection
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := scanConnection(tx.QueryRow(ctx, `SELECT `+connectionColumns+` FROM admp_connection c WHERE c.id = $1`, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCacheNotFound
		}
		out = c
		return err
	})
	return out, err
}

// ListByInterface implements Repository.
func (r *PGRepository) ListByInterface(ctx context.Context, orgID, interfaceID string) ([]Service, error) {
	if !api.IsUUID(interfaceID) {
		return []Service{}, nil
	}
	return r.listServices(ctx, orgID, `p.network_interface_id = $1`, interfaceID)
}

// listServices returns the services with ports matching where ($1), each
// with those ports, ordered by external id and port key.
func (r *PGRepository) listServices(ctx context.Context, orgID, where string, arg any) ([]Service, error) {
	out := []Service{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+serviceColumns+`, `+portColumns+`
			FROM admp_service_port p JOIN admp_service s ON s.id = p.service_id AND s.organization_id = p.organization_id
			WHERE `+where+` ORDER BY s.external_id, s.id, p.port_key`, arg)
		if err != nil {
			return err
		}
		defer rows.Close()
		index := map[string]int{}
		for rows.Next() {
			var s Service
			var p ServicePort
			var props []byte
			if err := rows.Scan(&s.ID, &s.OrganizationID, &s.AccountID, &s.ExternalID, &s.ConnectionID, &s.Kind, &props,
				&s.FetchedAt, &s.ExpiresAt, &s.Stale, &s.LastErrorClass, &s.ClientID,
				&p.ID, &p.OrganizationID, &p.ServiceID, &p.PortKey, &p.NetworkInterfaceID, &p.CIID, &p.MatchedBy,
				&p.ClientID, &p.SiteID); err != nil {
				return err
			}
			i, seen := index[s.ID]
			if !seen {
				if err := json.Unmarshal(props, &s.Properties); err != nil {
					return err
				}
				i = len(out)
				index[s.ID] = i
				out = append(out, s)
			}
			out[i].Ports = append(out[i].Ports, p)
		}
		return rows.Err()
	})
	return out, err
}

// ListByCI implements Repository.
func (r *PGRepository) ListByCI(ctx context.Context, orgID, ciID string) ([]Connection, error) {
	out := []Connection{}
	if !api.IsUUID(ciID) {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+connectionColumns+` FROM admp_connection c
			WHERE c.id IN (SELECT s.connection_id FROM admp_service s
				JOIN admp_service_port p ON p.service_id = s.id AND p.organization_id = s.organization_id
				WHERE p.ci_id = $1)
			ORDER BY COALESCE(c.carrier, '') COLLATE "C", c.external_id COLLATE "C", c.id`, ciID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanConnection(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, err
}

// ListByConnection implements Repository: one statement, the services
// without ports included.
func (r *PGRepository) ListByConnection(ctx context.Context, orgID, connectionID string) ([]Service, error) {
	out := []Service{}
	if !api.IsUUID(connectionID) {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+serviceColumns+`, p.id::text, p.port_key, COALESCE(p.network_interface_id::text, ''),
				COALESCE(p.ci_id::text, ''), p.matched_by, COALESCE(p.client_id::text, ''), COALESCE(p.site_id::text, '')
			FROM admp_service s LEFT JOIN admp_service_port p ON p.service_id = s.id AND p.organization_id = s.organization_id
			WHERE s.connection_id = $1 ORDER BY s.external_id COLLATE "C", s.id, p.port_key COLLATE "C"`, connectionID)
		if err != nil {
			return err
		}
		defer rows.Close()
		index := map[string]int{}
		for rows.Next() {
			var sv Service
			var props []byte
			var portID, portKey, matchedBy *string
			var p ServicePort
			if err := rows.Scan(&sv.ID, &sv.OrganizationID, &sv.AccountID, &sv.ExternalID, &sv.ConnectionID, &sv.Kind, &props,
				&sv.FetchedAt, &sv.ExpiresAt, &sv.Stale, &sv.LastErrorClass, &sv.ClientID,
				&portID, &portKey, &p.NetworkInterfaceID, &p.CIID, &matchedBy, &p.ClientID, &p.SiteID); err != nil {
				return err
			}
			i, seen := index[sv.ID]
			if !seen {
				if err := json.Unmarshal(props, &sv.Properties); err != nil {
					return err
				}
				i = len(out)
				index[sv.ID] = i
				out = append(out, sv)
			}
			if portID != nil {
				p.ID, p.OrganizationID, p.ServiceID, p.PortKey, p.MatchedBy = *portID, sv.OrganizationID, sv.ID, *portKey, *matchedBy
				out[i].Ports = append(out[i].Ports, p)
			}
		}
		return rows.Err()
	})
	return out, err
}

// ConnectionsByID implements Repository.
func (r *PGRepository) ConnectionsByID(ctx context.Context, orgID string, ids []string) ([]Connection, error) {
	out := []Connection{}
	valid := make([]string, 0, len(ids))
	for _, id := range ids {
		if api.IsUUID(id) {
			valid = append(valid, id)
		}
	}
	if len(valid) == 0 {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+connectionColumns+` FROM admp_connection c WHERE c.id = ANY($1::uuid[])
			ORDER BY c.external_id COLLATE "C", c.id`, valid)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanConnection(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, err
}

// MarkStale implements Repository.
func (r *PGRepository) MarkStale(ctx context.Context, orgID, kind, id, errorClass string) error {
	table := map[string]string{KindConnection: "admp_connection", KindService: "admp_service"}[kind]
	if table == "" || !api.IsUUID(id) {
		return ErrCacheNotFound
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE `+table+` SET stale = true, last_error_class = $2 WHERE id = $1`, id, nilIfEmpty(errorClass))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrCacheNotFound
		}
		return nil
	})
}

// ConnectionByExternal implements Repository.
func (r *PGRepository) ConnectionByExternal(ctx context.Context, orgID, accountID, externalID string) (*Connection, error) {
	if !api.IsUUID(accountID) {
		return nil, ErrCacheNotFound
	}
	var out *Connection
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := scanConnection(tx.QueryRow(ctx, `SELECT `+connectionColumns+` FROM admp_connection c
			WHERE c.account_id = $1 AND c.external_id = $2`, accountID, externalID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCacheNotFound
		}
		out = c
		return err
	})
	return out, err
}

// ServiceByExternal implements Repository.
func (r *PGRepository) ServiceByExternal(ctx context.Context, orgID, accountID, externalID string) (*Service, error) {
	if !api.IsUUID(accountID) {
		return nil, ErrCacheNotFound
	}
	var out *Service
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		s, err := scanService(tx.QueryRow(ctx, `SELECT `+serviceColumns+` FROM admp_service s
			WHERE s.account_id = $1 AND s.external_id = $2`, accountID, externalID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCacheNotFound
		}
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+portColumns+` FROM admp_service_port p WHERE p.service_id = $1 ORDER BY p.port_key`, s.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, perr := scanPort(rows)
			if perr != nil {
				return perr
			}
			s.Ports = append(s.Ports, *p)
		}
		out = s
		return rows.Err()
	})
	return out, err
}

// ListDueLinked implements Repository.
func (r *PGRepository) ListDueLinked(ctx context.Context, orgID, accountID string, before time.Time) ([]DueObject, error) {
	out := []DueObject{}
	if !api.IsUUID(accountID) {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH linked AS (
				SELECT s.id, s.external_id, s.expires_at, s.connection_id FROM admp_service s
				WHERE s.account_id = $1 AND EXISTS (SELECT 1 FROM admp_service_port p
					WHERE p.service_id = s.id AND (p.network_interface_id IS NOT NULL OR p.ci_id IS NOT NULL)))
			SELECT 'service', l.external_id, l.expires_at FROM linked l WHERE l.expires_at < $2
			UNION ALL
			SELECT 'connection', c.external_id, c.expires_at FROM admp_connection c
			WHERE c.account_id = $1 AND c.expires_at < $2 AND c.id IN (SELECT connection_id FROM linked)
			ORDER BY 3, 1, 2`, accountID, before)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d DueObject
			if err := rows.Scan(&d.Kind, &d.ExternalID, &d.ExpiresAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// DeleteExpired implements Repository; the trigger of admp_connection
// deletes the personal surrogates of the removed connections.
func (r *PGRepository) DeleteExpired(ctx context.Context, orgID string, before time.Time) (Expired, error) {
	var out Expired
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM admp_service WHERE organization_id = $1 AND expires_at < $2`, orgID, nilIfZero(before))
		if err != nil {
			return err
		}
		out.Services = int(tag.RowsAffected())
		tag, err = tx.Exec(ctx, `DELETE FROM admp_connection WHERE organization_id = $1 AND expires_at < $2`, orgID, nilIfZero(before))
		if err != nil {
			return err
		}
		out.Connections = int(tag.RowsAffected())
		return nil
	})
	return out, err
}

// AffectedConnections implements Repository: one statement; the ports of
// both lists are found through unnest, the redundancy is a lateral read of
// the other ports of the service and of the connection's other services
// that are not affected.
func (r *PGRepository) AffectedConnections(ctx context.Context, orgID string, ciIDs, interfaceIDs []string) ([]AffectedConnection, error) {
	out := []AffectedConnection{}
	if len(ciIDs) == 0 && len(interfaceIDs) == 0 {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `WITH cis AS (SELECT unnest($1::uuid[]) AS id),
				ifaces AS (SELECT unnest($2::uuid[]) AS id),
				hit AS (
					SELECT p.id, p.service_id, p.port_key, p.ci_id, p.network_interface_id FROM admp_service_port p
					WHERE p.ci_id IN (SELECT id FROM cis)
					UNION
					SELECT p.id, p.service_id, p.port_key, p.ci_id, p.network_interface_id FROM admp_service_port p
					WHERE p.network_interface_id IN (SELECT id FROM ifaces))
			SELECT c.id::text, c.external_id, c.account_id::text, COALESCE(c.carrier, ''), COALESCE(c.contract_id, ''),
				s.id::text, s.kind, h.port_key, COALESCE(h.ci_id::text, ''), COALESCE(h.network_interface_id::text, ''),
				c.stale, s.stale, s.external_id, LEAST(c.fetched_at, s.fetched_at),
				ARRAY(SELECT DISTINCT o.ci_id::text FROM admp_service_port o
					JOIN admp_service os ON os.id = o.service_id AND os.organization_id = o.organization_id
					WHERE (o.service_id = s.id OR os.connection_id = c.id) AND o.id <> h.id AND o.ci_id IS NOT NULL
						AND o.ci_id NOT IN (SELECT id FROM cis)
						AND (o.network_interface_id IS NULL OR o.network_interface_id NOT IN (SELECT id FROM ifaces))
					ORDER BY 1)
			FROM hit h
			JOIN admp_service s ON s.id = h.service_id
			JOIN admp_connection c ON c.id = s.connection_id AND c.organization_id = s.organization_id`, ciIDs, interfaceIDs)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var a AffectedConnection
			if err := rows.Scan(&a.ConnectionID, &a.ExternalID, &a.AccountID, &a.Carrier, &a.ContractID, &a.ServiceID, &a.ServiceKind,
				&a.PortKey, &a.CIID, &a.NetworkInterfaceID, &a.connectionStale, &a.serviceStale, &a.serviceExternalID, &a.FetchedAt,
				&a.RedundantVia); err != nil {
				return err
			}
			a.Stale = a.connectionStale || a.serviceStale
			if a.RedundantVia == nil {
				a.RedundantVia = []string{}
			}
			a.Redundant = len(a.RedundantVia) > 0
			out = append(out, a)
		}
		return rows.Err()
	})
	sortAffected(out)
	return out, err
}

const accessPortColumns = `a.id::text, a.organization_id::text, a.account_id::text, a.external_id, a.node_external_id, a.node_name,
	a.unit_index, a.port_index, COALESCE(a.protocol, ''), COALESCE(a.network_interface_id::text, ''), COALESCE(a.ci_id::text, ''),
	a.matched_by, COALESCE(a.client_id::text, ''), COALESCE(a.site_id::text, ''), a.fetched_at, a.expires_at`

func scanAccessPort(row pgx.Row) (*AccessPort, error) {
	var p AccessPort
	err := row.Scan(&p.ID, &p.OrganizationID, &p.AccountID, &p.ExternalID, &p.NodeExternalID, &p.NodeName, &p.UnitIndex,
		&p.PortIndex, &p.Protocol, &p.NetworkInterfaceID, &p.CIID, &p.MatchedBy, &p.ClientID, &p.SiteID, &p.FetchedAt, &p.ExpiresAt)
	return &p, err
}

// checkAccessPorts validates an inventory: a key per port, unique.
func checkAccessPorts(ports []AccessPort) error {
	seen := map[string]bool{}
	for i := range ports {
		p := &ports[i]
		p.ExternalID = strings.TrimSpace(p.ExternalID)
		if p.ExternalID == "" || seen[p.ExternalID] || strings.TrimSpace(p.NodeExternalID) == "" {
			return ErrInvalidCacheEntry
		}
		seen[p.ExternalID] = true
	}
	return nil
}

// ReplaceAccessPorts implements Repository: one upsert from arrays and one
// delete of the ports no longer delivered.
func (r *PGRepository) ReplaceAccessPorts(ctx context.Context, orgID, accountID string, ports []AccessPort, fetchedAt time.Time) error {
	if !api.IsUUID(accountID) {
		return ErrInvalidCacheEntry
	}
	if err := checkAccessPorts(ports); err != nil {
		return err
	}
	n := len(ports)
	keys, nodes, names, protocols := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	units, indexes := make([]int64, n), make([]int64, n)
	for i := range ports {
		p := &ports[i]
		keys[i], nodes[i], names[i], protocols[i], units[i], indexes[i] = p.ExternalID, p.NodeExternalID, p.NodeName, p.Protocol,
			p.UnitIndex, p.PortIndex
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO admp_access_port AS a (organization_id, account_id, external_id, node_external_id,
				node_name, unit_index, port_index, protocol, fetched_at, expires_at)
			SELECT $1, $2, k, nd, nm, u, ix, NULLIF(pr, ''), $9, $9
			FROM unnest($3::text[], $4::text[], $5::text[], $6::bigint[], $7::bigint[], $8::text[]) AS t(k, nd, nm, u, ix, pr)
			ON CONFLICT (account_id, external_id) DO UPDATE SET node_external_id = EXCLUDED.node_external_id,
				node_name = EXCLUDED.node_name, unit_index = EXCLUDED.unit_index, port_index = EXCLUDED.port_index,
				protocol = EXCLUDED.protocol, fetched_at = EXCLUDED.fetched_at`,
			orgID, accountID, keys, nodes, names, units, indexes, protocols, fetchedAt); err != nil {
			return cacheError(err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM admp_access_port WHERE account_id = $1 AND organization_id = $2
			AND NOT (external_id = ANY ($3::text[]))`, accountID, orgID, keys); err != nil {
			return err
		}
		// The inventory time lives at the account: an empty inventory has
		// no port row to carry it.
		_, err := tx.Exec(ctx, `UPDATE admp_account SET inventory_fetched_at = $3 WHERE id = $1 AND organization_id = $2`,
			accountID, orgID, fetchedAt)
		return err
	})
}

// MatchAccessPorts implements Repository: the match statement of match.go
// and a count of the manual matches.
func (r *PGRepository) MatchAccessPorts(ctx context.Context, orgID, accountID string) (MatchResult, error) {
	out := MatchResult{UnmatchedPorts: []UnmatchedPort{}, Rules: map[string]int{}}
	if !api.IsUUID(accountID) {
		return out, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, matchStatement, accountID, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var u UnmatchedPort
			var rule string
			if err := rows.Scan(&u.PortKey, &rule, &u.NodeName, &u.UnitIndex, &u.PortIndex); err != nil {
				return err
			}
			out.Rules[rule]++
			if rule != MatchRuleNone {
				out.Matched++
				continue
			}
			out.Unmatched++
			out.UnmatchedPorts = append(out.UnmatchedPorts, u)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		var manual int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM admp_access_port WHERE account_id = $1 AND organization_id = $2
			AND matched_by = 'manual' AND network_interface_id IS NOT NULL`, accountID, orgID).Scan(&manual); err != nil {
			return err
		}
		out.Matched += manual
		return nil
	})
	finishMatch(&out)
	return out, err
}

// finishMatch orders the unmatched ports by key and keeps the first
// maxUnmatchedPorts.
func finishMatch(r *MatchResult) {
	slices.SortFunc(r.UnmatchedPorts, func(a, b UnmatchedPort) int { return strings.Compare(a.PortKey, b.PortKey) })
	if len(r.UnmatchedPorts) > maxUnmatchedPorts {
		r.UnmatchedPorts = r.UnmatchedPorts[:maxUnmatchedPorts]
	}
}

// AccessPortsByInterface implements Repository.
func (r *PGRepository) AccessPortsByInterface(ctx context.Context, orgID, accountID, interfaceID string) ([]AccessPort, error) {
	if !api.IsUUID(accountID) || !api.IsUUID(interfaceID) {
		return []AccessPort{}, nil
	}
	return r.accessPorts(ctx, orgID, `a.account_id = $1 AND a.network_interface_id = $2 ORDER BY a.external_id`, accountID, interfaceID)
}

// ListDuePorts implements Repository.
func (r *PGRepository) ListDuePorts(ctx context.Context, orgID, accountID string, before time.Time) ([]AccessPort, error) {
	if !api.IsUUID(accountID) {
		return []AccessPort{}, nil
	}
	return r.accessPorts(ctx, orgID, `a.account_id = $1 AND a.network_interface_id IS NOT NULL AND a.expires_at < $2
		ORDER BY a.expires_at, a.external_id`, accountID, before)
}

func (r *PGRepository) accessPorts(ctx context.Context, orgID, where string, args ...any) ([]AccessPort, error) {
	out := []AccessPort{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT `+accessPortColumns+` FROM admp_access_port a WHERE `+where, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			p, err := scanAccessPort(rows)
			if err != nil {
				return err
			}
			out = append(out, *p)
		}
		return rows.Err()
	})
	return out, err
}

// InventoryFetchedAt implements Repository.
func (r *PGRepository) InventoryFetchedAt(ctx context.Context, orgID, accountID string) (time.Time, error) {
	var at *time.Time
	if !api.IsUUID(accountID) {
		return time.Time{}, nil
	}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT inventory_fetched_at FROM admp_account WHERE id = $1`, accountID).Scan(&at)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	})
	if err != nil || at == nil {
		return time.Time{}, err
	}
	return *at, nil
}

// TouchAccessPort implements Repository.
func (r *PGRepository) TouchAccessPort(ctx context.Context, orgID, accountID, portKey string, expiresAt time.Time) error {
	if !api.IsUUID(accountID) {
		return nil
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE admp_access_port SET expires_at = $3 WHERE account_id = $1 AND external_id = $2`,
			accountID, strings.TrimSpace(portKey), expiresAt)
		return err
	})
}

// ReplacePortServices implements Repository: three set-based statements
// in one transaction.
func (r *PGRepository) ReplacePortServices(ctx context.Context, orgID, accountID, portKey string, serviceIDs []string,
	expiresAt time.Time) error {
	portKey = strings.TrimSpace(portKey)
	if !api.IsUUID(accountID) || portKey == "" {
		return ErrInvalidCacheEntry
	}
	for _, id := range serviceIDs {
		if !api.IsUUID(id) {
			return ErrInvalidCacheEntry
		}
	}
	if serviceIDs == nil {
		serviceIDs = []string{}
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM admp_service_port p USING admp_service s
			WHERE s.id = p.service_id AND s.organization_id = p.organization_id AND s.account_id = $1
				AND p.port_key = $2 AND p.matched_by <> 'manual' AND NOT (p.service_id = ANY ($3::uuid[]))`,
			accountID, portKey, serviceIDs); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO admp_service_port AS p (organization_id, service_id, port_key, network_interface_id,
				matched_by)
			SELECT $1, s.id, $3, a.network_interface_id, COALESCE(CASE WHEN a.network_interface_id IS NULL THEN 'none' ELSE a.matched_by END, 'none')
			FROM admp_service s
			LEFT JOIN admp_access_port a ON a.account_id = s.account_id AND a.organization_id = s.organization_id AND a.external_id = $3
			WHERE s.account_id = $2 AND s.id = ANY ($4::uuid[])
			ON CONFLICT (service_id, port_key) DO UPDATE SET network_interface_id = EXCLUDED.network_interface_id,
				ci_id = NULL, matched_by = EXCLUDED.matched_by
			WHERE p.matched_by <> 'manual'`, orgID, accountID, portKey, serviceIDs); err != nil {
			return cacheError(err)
		}
		_, err := tx.Exec(ctx, `UPDATE admp_access_port SET expires_at = $3 WHERE account_id = $1 AND external_id = $2`,
			accountID, portKey, expiresAt)
		return err
	})
}

var _ Targets = (*PGRepository)(nil)

// InterfaceTarget implements Targets: the interface with its CI under the
// caller's row level security; a deleted CI hides it.
func (r *PGRepository) InterfaceTarget(ctx context.Context, orgID, id string) (*Target, error) {
	return r.target(ctx, orgID, id, `SELECT ci.id::text, COALESCE(ci.client_id::text, '') FROM network_interface ni
		JOIN ci ON ci.id = ni.ci_id AND ci.organization_id = ni.organization_id AND ci.deleted_at IS NULL
		WHERE ni.id = $1`)
}

// CITarget implements Targets.
func (r *PGRepository) CITarget(ctx context.Context, orgID, id string) (*Target, error) {
	return r.target(ctx, orgID, id, `SELECT ci.id::text, COALESCE(ci.client_id::text, '') FROM ci
		WHERE ci.id = $1 AND ci.deleted_at IS NULL`)
}

func (r *PGRepository) target(ctx context.Context, orgID, id, query string) (*Target, error) {
	if !api.IsUUID(id) {
		return nil, ErrTargetNotFound
	}
	var t Target
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, query, id).Scan(&t.CIID, &t.ClientID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTargetNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &t, nil
}
