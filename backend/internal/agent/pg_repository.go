package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const agentCols = `id::text, organization_id::text, agent_id, hostname, COALESCE(version,''), COALESCE(os,''), COALESCE(arch,''), COALESCE(ci_id::text,''), status, last_heartbeat, policy, created_at, updated_at`

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
	if err := s.Scan(&a.ID, &a.OrganizationID, &a.AgentID, &a.Hostname, &a.Version, &a.OS, &a.Arch, &a.CIID, &a.Status, &a.LastHeartbeat, &policy, &a.CreatedAt, &a.UpdatedAt); err != nil {
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
// the transaction's tenant scope: the ci policy filters the subquery, while
// endpoint_agent carries no client or site column yet (WP-038).
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

func nilIfEmpty(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
