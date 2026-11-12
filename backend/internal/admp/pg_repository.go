package admp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
// (admp_connection_subject_check: ErrInvalidCacheEntry).
func (r *PGRepository) UpsertConnection(ctx context.Context, c *Connection) error {
	if err := checkConnection(c); err != nil {
		return err
	}
	props, err := json.Marshal(c.Properties)
	if err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
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

// UpsertService implements Repository.
func (r *PGRepository) UpsertService(ctx context.Context, s *Service) error {
	if err := checkService(s); err != nil {
		return err
	}
	props, err := json.Marshal(s.Properties)
	if err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
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
			ORDER BY c.external_id, c.id`, ciID)
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
