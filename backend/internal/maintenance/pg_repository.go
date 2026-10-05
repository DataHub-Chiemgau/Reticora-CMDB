package maintenance

import (
	"context"
	"fmt"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const windowCols = `id::text, organization_id::text, title, COALESCE(description,''), starts_at, ends_at, status, COALESCE(created_by::text,''), created_at, updated_at`

const notificationCols = `id::text, organization_id::text, maintenance_window_id::text, COALESCE(client_id::text,''), channel, status, sent_at, created_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed maintenance repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func scanWindow(s pgx.Row) (*Window, error) {
	w := &Window{}
	if err := s.Scan(&w.ID, &w.OrganizationID, &w.Title, &w.Description, &w.StartsAt, &w.EndsAt, &w.Status, &w.CreatedBy, &w.CreatedAt, &w.UpdatedAt); err != nil {
		return nil, err
	}
	return w, nil
}

// Maintenance windows carry no client or site column yet (WP-028). A window
// CI is visible when its CI is visible under the transaction's tenant scope,
// whose ci policy filters the subquery; a window is visible when it has no
// CI or at least one visible CI.
const (
	windowCIVisible = "EXISTS (SELECT 1 FROM ci WHERE ci.id = maintenance_window_ci.ci_id)"
	windowVisible   = "(NOT EXISTS (SELECT 1 FROM maintenance_window_ci WHERE maintenance_window_ci.maintenance_window_id = maintenance_window.id)" +
		" OR EXISTS (SELECT 1 FROM maintenance_window_ci WHERE maintenance_window_ci.maintenance_window_id = maintenance_window.id AND " + windowCIVisible + "))"
)

func (r *PGRepository) ciIDsTx(ctx context.Context, tx pgx.Tx, orgID, windowID string) ([]string, error) {
	rows, err := tx.Query(ctx, "SELECT ci_id::text FROM maintenance_window_ci WHERE "+windowCIVisible+" AND organization_id = $1 AND maintenance_window_id = $2", orgID, windowID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Window, int, error) {
	out := []Window{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id = $1"
		args := []any{orgID}
		if filter.Status != "" {
			where += " AND status = $2"
			args = append(args, filter.Status)
		}
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM maintenance_window WHERE "+windowVisible+" AND "+where, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+windowCols+" FROM maintenance_window WHERE "+windowVisible+" AND "+where+fmt.Sprintf(" ORDER BY starts_at ASC LIMIT $%d OFFSET $%d", len(args)+1, len(args)+2), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		ids := []string{}
		for rows.Next() {
			w, err := scanWindow(rows)
			if err != nil {
				return err
			}
			out = append(out, *w)
			ids = append(ids, w.ID)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for i, id := range ids {
			ciIDs, err := r.ciIDsTx(ctx, tx, orgID, id)
			if err != nil {
				return err
			}
			out[i].CIIDs = ciIDs
		}
		return nil
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Window, error) {
	var w *Window
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		w, err = scanWindow(tx.QueryRow(ctx, "SELECT "+windowCols+" FROM maintenance_window WHERE "+windowVisible+" AND organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("maintenance window not found")
		}
		if err != nil {
			return err
		}
		w.CIIDs, err = r.ciIDsTx(ctx, tx, orgID, id)
		return err
	})
	return w, err
}

func (r *PGRepository) Create(ctx context.Context, w *Window) error {
	return database.WithRequestTenant(ctx, r.pool, w.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if w.Status == "" {
			w.Status = "scheduled"
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO maintenance_window (organization_id, title, description, starts_at, ends_at, status, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, NULLIF($7,'')::uuid)
			RETURNING id::text, created_at, updated_at
		`, w.OrganizationID, w.Title, w.Description, w.StartsAt, w.EndsAt, w.Status, w.CreatedBy).
			Scan(&w.ID, &w.CreatedAt, &w.UpdatedAt); err != nil {
			return err
		}
		for _, ciID := range w.CIIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO maintenance_window_ci (organization_id, maintenance_window_id, ci_id)
				VALUES ($1, $2, $3::uuid) ON CONFLICT DO NOTHING
			`, w.OrganizationID, w.ID, ciID); err != nil {
				return fmt.Errorf("link ci %s: %w", ciID, err)
			}
		}
		// Every affected CI must be visible under the scope.
		var hidden bool
		if err := tx.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM maintenance_window_ci WHERE maintenance_window_id = $1 AND NOT "+windowCIVisible+")",
			w.ID).Scan(&hidden); err != nil {
			return fmt.Errorf("check window cis: %w", err)
		}
		if hidden {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateWindowRequest) (*Window, error) {
	var w *Window
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		sets := []string{}
		args := []any{orgID, id}
		pos := 3
		if req.Title != nil {
			sets = append(sets, fmt.Sprintf("title = $%d", pos))
			args = append(args, *req.Title)
			pos++
		}
		if req.Description != nil {
			sets = append(sets, fmt.Sprintf("description = $%d", pos))
			args = append(args, *req.Description)
			pos++
		}
		if req.Status != nil {
			sets = append(sets, fmt.Sprintf("status = $%d", pos))
			args = append(args, *req.Status)
			pos++
		}
		var err error
		if len(sets) == 0 {
			w, err = scanWindow(tx.QueryRow(ctx, "SELECT "+windowCols+" FROM maintenance_window WHERE "+windowVisible+" AND organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = now()")
			w, err = scanWindow(tx.QueryRow(ctx, "UPDATE maintenance_window SET "+strings.Join(sets, ", ")+" WHERE "+windowVisible+" AND organization_id = $1 AND id = $2 RETURNING "+windowCols, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("maintenance window not found")
		}
		if err != nil {
			return err
		}
		w.CIIDs, err = r.ciIDsTx(ctx, tx, orgID, id)
		return err
	})
	return w, err
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM maintenance_window WHERE "+windowVisible+" AND organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("maintenance window not found")
		}
		return nil
	})
}

// NotifyClients derives affected clients from the window's CIs and records
// one sent notification per client.
func (r *PGRepository) NotifyClients(ctx context.Context, orgID, windowID string) ([]Notification, error) {
	out := []Notification{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT DISTINCT ci.client_id::text
			FROM maintenance_window_ci mwc
			JOIN ci ON ci.id = mwc.ci_id
			JOIN maintenance_window ON maintenance_window.id = mwc.maintenance_window_id AND `+windowVisible+`
			WHERE mwc.maintenance_window_id = $1 AND mwc.organization_id = $2
			  AND ci.client_id IS NOT NULL
		`, windowID, orgID)
		if err != nil {
			return err
		}
		var clientIDs []string
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			clientIDs = append(clientIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		// Queued, not sent: no channel delivers yet, so neither status nor
		// sent_at may claim a delivery.
		for _, clientID := range clientIDs {
			n := Notification{OrganizationID: orgID, WindowID: windowID, ClientID: clientID, Channel: "webhook", Status: NotificationPending}
			if err := tx.QueryRow(ctx, `
				INSERT INTO maintenance_notification (organization_id, maintenance_window_id, client_id, channel, status)
				VALUES ($1, $2, $3::uuid, 'webhook', $4)
				RETURNING id::text, created_at
			`, orgID, windowID, clientID, NotificationPending).Scan(&n.ID, &n.CreatedAt); err != nil {
				return err
			}
			out = append(out, n)
		}
		return nil
	})
	return out, err
}

func (r *PGRepository) ListNotifications(ctx context.Context, orgID, windowID string) ([]Notification, error) {
	out := []Notification{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+notificationCols+" FROM maintenance_notification WHERE organization_id = $1 AND maintenance_window_id = $2 ORDER BY created_at ASC", orgID, windowID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			n := Notification{}
			if err := rows.Scan(&n.ID, &n.OrganizationID, &n.WindowID, &n.ClientID, &n.Channel, &n.Status, &n.SentAt, &n.CreatedAt); err != nil {
				return err
			}
			out = append(out, n)
		}
		return rows.Err()
	})
	return out, err
}
