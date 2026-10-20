package discovery

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Collector statuses (COL-01).
const (
	// CollectorPending: created, no heartbeat yet.
	CollectorPending = "pending"
	// CollectorActive: sends heartbeats.
	CollectorActive = "active"
	// CollectorOffline: no heartbeat for OfflineAfter (COL-05).
	CollectorOffline = "offline"
	// CollectorDisabled: switched off by an administrator; heartbeats are
	// refused.
	CollectorDisabled = "disabled"
	// CollectorPaused: the organization's discovery license is not active
	// (ENT-07); the collector keeps its heartbeat and stops scanning.
	CollectorPaused = "paused"
)

// OfflineAfter is how long a collector may stay silent before it is
// offline (COL-05: heartbeat every 60 s, offline after five minutes).
const OfflineAfter = 5 * time.Minute

// CollectorStatusHeader tells the collector its status on the heartbeat
// response.
const CollectorStatusHeader = "Reticora-Collector-Status"

// ErrCollectorDisabled: a disabled collector's heartbeat is refused.
var ErrCollectorDisabled = errors.New("collector is disabled")

// CollectorPatch changes a collector; nil fields stay. Status accepts
// disabled (switch off) and active (switch on again).
type CollectorPatch struct {
	Name   *string `json:"name,omitempty"`
	SiteID *string `json:"site_id,omitempty"`
	Status *string `json:"status,omitempty"`
}

func (p *CollectorPatch) validate() error {
	if p.Name != nil && *p.Name == "" {
		return &api.FieldError{Field: "name", Message: "name must not be empty"}
	}
	if p.Status != nil && *p.Status != CollectorDisabled && *p.Status != CollectorActive {
		return &api.FieldError{Field: "status", Message: "status can be set to disabled or active"}
	}
	return nil
}

// statusAfterHeartbeat is the status a heartbeat leaves: pending and
// offline collectors are active again; paused stays paused until the
// license is active.
func statusAfterHeartbeat(current string) string {
	switch current {
	case CollectorPending, CollectorOffline, CollectorActive:
		return CollectorActive
	}
	return current
}

// nextAdminStatus applies an administrator's switch: disabling always
// works, enabling a disabled collector leaves it offline until its next
// heartbeat; other statuses are not changed by enabling.
func nextAdminStatus(current, wanted string) string {
	if wanted == CollectorDisabled {
		return CollectorDisabled
	}
	if current == CollectorDisabled {
		return CollectorOffline
	}
	return current
}

// UpdateCollector handles PATCH /api/v1/collectors/{id}.
func (h *Handler) UpdateCollector(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	var patch CollectorPatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if err := patch.validate(); err != nil {
		api.WriteRepoError(w, err)
		return
	}
	c, err := h.repo.UpdateCollector(r.Context(), t.OrganizationID, chi.URLParam(r, "id"), &patch)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, c)
}

// StatusMonitor is the collector.status run of the scheduler: it pauses
// the collectors of organizations whose discovery license is not active
// and resumes them when it is again (ENT-07), and sets collectors offline
// after OfflineAfter without heartbeat, reporting collector.offline
// (COL-05, NTF-05).
type StatusMonitor struct {
	pool *pgxpool.Pool
	// Licensed reports whether the organization's discovery license is
	// active; nil treats every organization as licensed.
	Licensed func(ctx context.Context, orgID string) (bool, error)
	now      func() time.Time
}

// CollectorStatusKind is the scheduler kind of the run.
const CollectorStatusKind = "collector.status"

// NewStatusMonitor creates the run on pool.
func NewStatusMonitor(pool *pgxpool.Pool) *StatusMonitor {
	return &StatusMonitor{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (m *StatusMonitor) WithClock(now func() time.Time) *StatusMonitor {
	m.now = now
	return m
}

// Schedule runs the monitor every minute.
func (m *StatusMonitor) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: CollectorStatusKind, Spec: "@every 1m", Run: func(ctx context.Context) error {
		_, err := m.Run(ctx)
		return err
	}}
}

// StatusChanges counts what a run changed.
type StatusChanges struct {
	Offline, Paused, Resumed int
}

// Run checks every organization. A failing organization does not stop the
// others (F-256): Run returns the errors joined at the end, the totals
// count the organizations that succeeded.
func (m *StatusMonitor) Run(ctx context.Context) (StatusChanges, error) {
	var total StatusChanges
	var orgs []string
	err := database.WithSystem(ctx, m.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return total, fmt.Errorf("list organizations: %w", err)
	}
	var errs []error
	for _, org := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		c, oerr := m.RunOrg(ctx, org)
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
			continue
		}
		total.Offline += c.Offline
		total.Paused += c.Paused
		total.Resumed += c.Resumed
	}
	return total, errors.Join(errs...)
}

// RunOrg checks the collectors of one organization.
func (m *StatusMonitor) RunOrg(ctx context.Context, orgID string) (StatusChanges, error) {
	var c StatusChanges
	licensed := true
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	if m.Licensed != nil {
		var err error
		if licensed, err = m.Licensed(ctx, orgID); err != nil {
			return c, fmt.Errorf("discovery license: %w", err)
		}
	}
	now := m.now()
	err := database.WithTenant(ctx, m.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		if !licensed {
			tag, err := tx.Exec(ctx, `UPDATE collector SET status = 'paused'
				WHERE organization_id = $1 AND status IN ('pending', 'active', 'offline')`, orgID)
			if err != nil {
				return fmt.Errorf("pause collectors: %w", err)
			}
			c.Paused = int(tag.RowsAffected())
			return nil
		}
		// Resumed collectors are active again with their next heartbeat.
		tag, err := tx.Exec(ctx, `UPDATE collector SET status = 'offline'
			WHERE organization_id = $1 AND status = 'paused'`, orgID)
		if err != nil {
			return fmt.Errorf("resume collectors: %w", err)
		}
		c.Resumed = int(tag.RowsAffected())
		rows, err := tx.Query(ctx, `UPDATE collector SET status = 'offline'
			WHERE organization_id = $1 AND status = 'active'
			  AND COALESCE(last_heartbeat_at, status_changed_at) < $2
			RETURNING id::text, name, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''),
				COALESCE(last_heartbeat_at, status_changed_at)`, orgID, now.Add(-OfflineAfter))
		if err != nil {
			return fmt.Errorf("set collectors offline: %w", err)
		}
		type silent struct {
			id, name, client, site string
			lastSeen               time.Time
		}
		found, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (silent, error) {
			var s silent
			scanErr := r.Scan(&s.id, &s.name, &s.client, &s.site, &s.lastSeen)
			return s, scanErr
		})
		if err != nil {
			return fmt.Errorf("set collectors offline: %w", err)
		}
		outbox := events.NewOutbox()
		for _, s := range found {
			if _, err = outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.CollectorOffline, EntityID: s.id,
				Data: map[string]any{"id": s.id, "name": s.name, "client_id": s.client, "site_id": s.site,
					"last_seen_at": s.lastSeen.UTC().Format(time.RFC3339)}}); err != nil {
				return err
			}
			slog.Warn("collector offline", "organization_id", orgID, "collector_id", s.id, "last_seen_at", s.lastSeen)
		}
		c.Offline = len(found)
		return nil
	})
	return c, err
}
