package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const agentCols = `id::text, organization_id::text, agent_id, hostname, COALESCE(version,''), COALESCE(os,''), COALESCE(arch,''), COALESCE(ci_id::text,''), status, last_heartbeat, policy, created_at, updated_at,
	COALESCE(client_id::text,''), COALESCE(site_id::text,''), COALESCE(suggested_site_id::text,''), network_fingerprint, site_confirmed_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed agent repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func scanAgent(s pgx.Row) (*Agent, error) {
	a := &Agent{}
	var policy []byte
	if err := s.Scan(&a.ID, &a.OrganizationID, &a.AgentID, &a.Hostname, &a.Version, &a.OS, &a.Arch, &a.CIID, &a.Status, &a.LastHeartbeat, &policy, &a.CreatedAt, &a.UpdatedAt,
		&a.ClientID, &a.SiteID, &a.SuggestedSiteID, &a.NetworkFingerprint, &a.SiteConfirmedAt); err != nil {
		return nil, err
	}
	if len(policy) > 0 {
		json.Unmarshal(policy, &a.Policy)
	}
	if a.Policy.IntervalSeconds == 0 {
		a.Policy = DefaultPolicy()
	}
	return a, nil
}

// agentVisible restricts endpoint agents to those whose CI is visible under
// the transaction's tenant scope. The policy of endpoint_agent filters by the
// client and site bound at enrollment (migration 000070); the subquery keeps
// agents of hidden CIs out as well.
const agentVisible = "(endpoint_agent.ci_id IS NULL OR EXISTS (SELECT 1 FROM ci WHERE ci.id = endpoint_agent.ci_id))"

func (r *PGRepository) List(ctx context.Context, orgID string, page api.PaginationParams) ([]Agent, int, error) {
	out := []Agent{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM endpoint_agent WHERE "+agentVisible+" AND organization_id = $1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+agentCols+" FROM endpoint_agent WHERE "+agentVisible+" AND organization_id = $1 ORDER BY hostname ASC LIMIT $2 OFFSET $3", orgID, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAgent(rows)
			if err != nil {
				return err
			}
			out = append(out, *a)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Agent, error) {
	var a *Agent
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = scanAgent(tx.QueryRow(ctx, "SELECT "+agentCols+" FROM endpoint_agent WHERE "+agentVisible+" AND organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("agent not found")
		}
		return err
	})
	return a, err
}

func (r *PGRepository) GetByAgentID(ctx context.Context, orgID, agentID string) (*Agent, error) {
	var a *Agent
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = scanAgent(tx.QueryRow(ctx, "SELECT "+agentCols+" FROM endpoint_agent WHERE "+agentVisible+" AND organization_id = $1 AND agent_id = $2", orgID, agentID))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("agent not found")
		}
		return err
	})
	return a, err
}

// Register upserts the agent on (organization_id, agent_id): re-enrollment
// refreshes identity and re-onlines the agent.
func (r *PGRepository) Register(ctx context.Context, a *Agent) error {
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if a.Status == "" {
			a.Status = "online"
		}
		if a.Policy.IntervalSeconds == 0 {
			a.Policy = DefaultPolicy()
		}
		policyJSON, _ := json.Marshal(a.Policy)
		return tx.QueryRow(ctx, `
			INSERT INTO endpoint_agent (organization_id, agent_id, hostname, version, os, arch, status, last_heartbeat, policy)
			VALUES ($1, $2, $3, $4, $5, $6, $7, now(), $8::jsonb)
			ON CONFLICT (organization_id, agent_id) DO UPDATE SET
				hostname = EXCLUDED.hostname,
				version = EXCLUDED.version,
				os = EXCLUDED.os,
				arch = EXCLUDED.arch,
				status = 'online',
				last_heartbeat = now(),
				updated_at = now()
			RETURNING id::text, created_at, updated_at
		`, a.OrganizationID, a.AgentID, a.Hostname, a.Version, a.OS, a.Arch, a.Status, string(policyJSON)).
			Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	})
}

func (r *PGRepository) UpdatePolicy(ctx context.Context, orgID, id string, req UpdatePolicyRequest) (*Agent, error) {
	var a *Agent
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cur, err := scanAgent(tx.QueryRow(ctx, "SELECT "+agentCols+" FROM endpoint_agent WHERE "+agentVisible+" AND organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("agent not found")
		}
		if err != nil {
			return err
		}
		if req.IntervalSeconds != nil {
			cur.Policy.IntervalSeconds = *req.IntervalSeconds
		}
		if req.MetricsEnabled != nil {
			cur.Policy.MetricsEnabled = *req.MetricsEnabled
		}
		if req.InventoryEnabled != nil {
			cur.Policy.InventoryEnabled = *req.InventoryEnabled
		}
		policyJSON, _ := json.Marshal(cur.Policy)
		a, err = scanAgent(tx.QueryRow(ctx, "UPDATE endpoint_agent SET policy = $3::jsonb, updated_at = now() WHERE "+agentVisible+" AND organization_id = $1 AND id = $2 RETURNING "+agentCols, orgID, id, string(policyJSON)))
		return err
	})
	return a, err
}

func (r *PGRepository) SetStatus(ctx context.Context, orgID, id, status string) (*Agent, error) {
	var a *Agent
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = scanAgent(tx.QueryRow(ctx, "UPDATE endpoint_agent SET status = $3, updated_at = now() WHERE "+agentVisible+" AND organization_id = $1 AND id = $2 RETURNING "+agentCols, orgID, id, status))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("agent not found")
		}
		return err
	})
	return a, err
}

func (r *PGRepository) Heartbeat(ctx context.Context, orgID, agentID string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE endpoint_agent SET last_heartbeat = now(), status = CASE WHEN status = 'offline' THEN 'online' ELSE status END, updated_at = now() WHERE "+agentVisible+" AND organization_id = $1 AND agent_id = $2", orgID, agentID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("agent not found")
		}
		return nil
	})
}

func (r *PGRepository) SetCI(ctx context.Context, orgID, agentID, ciID string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "UPDATE endpoint_agent SET ci_id = $3::uuid, updated_at = now() WHERE "+agentVisible+" AND organization_id = $1 AND agent_id = $2", orgID, agentID, ciID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("agent not found")
		}
		// The new CI must be visible under the scope as well.
		var visible bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM ci WHERE id = $1::uuid)", ciID).Scan(&visible); err != nil {
			return fmt.Errorf("check ci visibility: %w", err)
		}
		if !visible {
			return fmt.Errorf("ci not found")
		}
		return nil
	})
}

// CreateEnrollmentToken stores the token; the secret itself is never stored.
func (r *PGRepository) CreateEnrollmentToken(ctx context.Context, orgID string, tok *EnrollmentToken, tokenHash string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		creator := ""
		if scope, ok := database.TenantScopeFromContext(ctx); ok {
			creator = scope.UserID
		}
		return tx.QueryRow(ctx, `
			INSERT INTO agent_enrollment_token (organization_id, client_id, site_id, token_hash, description, created_by, expires_at)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6, $7)
			RETURNING id::text, created_at`,
			orgID, tok.ClientID, nilIfEmpty(tok.SiteID), tokenHash, tok.Description, creator, tok.ExpiresAt).
			Scan(&tok.ID, &tok.CreatedAt)
	})
}

// Enroll consumes the token and upserts the agent with the token's client and
// site in one transaction, so a failed registration leaves the token unused.
func (r *PGRepository) Enroll(ctx context.Context, a *Agent, tokenHash, ipAddress string) error {
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `
			UPDATE agent_enrollment_token SET used_at = now(), used_by_agent = $3
			WHERE organization_id = $1 AND token_hash = $2 AND used_at IS NULL AND expires_at > now()
			RETURNING client_id::text, COALESCE(site_id::text, '')`,
			a.OrganizationID, tokenHash, a.AgentID).Scan(&a.ClientID, &a.SiteID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrInvalidEnrollmentToken
		}
		if err != nil {
			return fmt.Errorf("consume enrollment token: %w", err)
		}
		a.NetworkFingerprint = ipAddress
		a.SuggestedSiteID = ""
		if a.SiteID == "" {
			if a.SuggestedSiteID, err = suggestSite(ctx, tx, a.OrganizationID, a.ClientID, ipAddress); err != nil {
				return err
			}
		}
		if a.Policy.IntervalSeconds == 0 {
			a.Policy = DefaultPolicy()
		}
		policyJSON, _ := json.Marshal(a.Policy)
		return tx.QueryRow(ctx, `
			INSERT INTO endpoint_agent (organization_id, agent_id, hostname, version, os, arch, status, last_heartbeat, policy,
				client_id, site_id, suggested_site_id, network_fingerprint, site_confirmed_at)
			VALUES ($1, $2, $3, $4, $5, $6, 'online', now(), $7::jsonb,
				$8::uuid, $9::uuid, $10::uuid, $11, CASE WHEN $9::uuid IS NULL THEN NULL ELSE now() END)
			ON CONFLICT (organization_id, agent_id) DO UPDATE SET
				hostname = EXCLUDED.hostname,
				version = EXCLUDED.version,
				os = EXCLUDED.os,
				arch = EXCLUDED.arch,
				status = 'online',
				last_heartbeat = now(),
				client_id = EXCLUDED.client_id,
				site_id = EXCLUDED.site_id,
				suggested_site_id = EXCLUDED.suggested_site_id,
				network_fingerprint = EXCLUDED.network_fingerprint,
				site_confirmed_at = EXCLUDED.site_confirmed_at,
				updated_at = now()
			RETURNING `+agentCols,
			a.OrganizationID, a.AgentID, a.Hostname, a.Version, a.OS, a.Arch, string(policyJSON),
			a.ClientID, nilIfEmpty(a.SiteID), nilIfEmpty(a.SuggestedSiteID), ipAddress).
			Scan(scanTargets(a)...)
	})
}

// SuggestSite records the fingerprint and suggests the site of the subnet
// containing it when that differs from the agent's confirmed site.
func (r *PGRepository) SuggestSite(ctx context.Context, orgID, agentID, ipAddress string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var clientID string
		err := tx.QueryRow(ctx, `SELECT COALESCE(client_id::text, '') FROM endpoint_agent WHERE organization_id = $1 AND agent_id = $2`,
			orgID, agentID).Scan(&clientID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("agent not found")
		}
		if err != nil {
			return err
		}
		suggested := ""
		if clientID != "" {
			if suggested, err = suggestSite(ctx, tx, orgID, clientID, ipAddress); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `
			UPDATE endpoint_agent SET network_fingerprint = $3,
				suggested_site_id = CASE WHEN $4::uuid IS DISTINCT FROM site_id THEN $4::uuid END,
				updated_at = now()
			WHERE organization_id = $1 AND agent_id = $2`, orgID, agentID, ipAddress, nilIfEmpty(suggested))
		return err
	})
}

// ConfirmSite sets the site of the agent after a manual confirmation.
func (r *PGRepository) ConfirmSite(ctx context.Context, orgID, id, siteID string) (*Agent, error) {
	var a *Agent
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = scanAgent(tx.QueryRow(ctx, `
			UPDATE endpoint_agent SET site_id = $3::uuid, suggested_site_id = NULL, site_confirmed_at = now(), updated_at = now()
			WHERE `+agentVisible+` AND organization_id = $1 AND id = $2 AND client_id IS NOT NULL
			  AND EXISTS (SELECT 1 FROM site s WHERE s.id = $3::uuid AND s.client_id = endpoint_agent.client_id)
			RETURNING `+agentCols, orgID, id, siteID))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSiteNotOfClient
		}
		return err
	})
	return a, err
}

// suggestSite returns the site of the most specific subnet of the client
// containing ipAddress, or "" when none matches or the address is invalid.
func suggestSite(ctx context.Context, tx pgx.Tx, orgID, clientID, ipAddress string) (string, error) {
	if net.ParseIP(strings.TrimSpace(ipAddress)) == nil {
		return "", nil
	}
	var siteID string
	err := tx.QueryRow(ctx, `
		SELECT n.site_id::text FROM subnet n
		JOIN site s ON s.id = n.site_id AND s.client_id = $2::uuid
		WHERE n.organization_id = $1 AND $3::inet <<= n.cidr
		ORDER BY masklen(n.cidr) DESC LIMIT 1`, orgID, clientID, strings.TrimSpace(ipAddress)).Scan(&siteID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("suggest site: %w", err)
	}
	return siteID, nil
}

// scanTargets lists the scan destinations of agentCols for a.
func scanTargets(a *Agent) []any {
	return []any{&a.ID, &a.OrganizationID, &a.AgentID, &a.Hostname, &a.Version, &a.OS, &a.Arch, &a.CIID, &a.Status, &a.LastHeartbeat, &a.Policy, &a.CreatedAt, &a.UpdatedAt,
		&a.ClientID, &a.SiteID, &a.SuggestedSiteID, &a.NetworkFingerprint, &a.SiteConfirmedAt}
}

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
