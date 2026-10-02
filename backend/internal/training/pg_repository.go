package training

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const courseCols = `id::text, organization_id::text, title, COALESCE(description,''), COALESCE(category,''), validity_months, created_at, updated_at`

const assignmentCols = `id::text, organization_id::text, training_id::text, user_id::text, assigned_at, due_at, completed_at, COALESCE(proof_object_key,''), status, created_at`

// PGRepository implements Repository backed by PostgreSQL with RLS.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates a PostgreSQL-backed training repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

func scanCourse(s pgx.Row) (*Course, error) {
	c := &Course{}
	if err := s.Scan(&c.ID, &c.OrganizationID, &c.Title, &c.Description, &c.Category, &c.ValidityMonths, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	return c, nil
}

func scanAssignment(s pgx.Row) (*Assignment, error) {
	a := &Assignment{}
	if err := s.Scan(&a.ID, &a.OrganizationID, &a.TrainingID, &a.UserID, &a.AssignedAt, &a.DueAt, &a.CompletedAt, &a.ProofObjectKey, &a.Status, &a.CreatedAt); err != nil {
		return nil, err
	}
	return a, nil
}

func (r *PGRepository) List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Course, int, error) {
	out := []Course{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := []string{"organization_id = $1"}
		args := []any{orgID}
		pos := 2
		if filter.Category != "" {
			where = append(where, fmt.Sprintf("category = $%d", pos))
			args = append(args, filter.Category)
			pos++
		}
		if filter.Search != "" {
			where = append(where, fmt.Sprintf("title ILIKE $%d", pos))
			args = append(args, "%"+filter.Search+"%")
			pos++
		}
		clause := strings.Join(where, " AND ")
		if err := tx.QueryRow(ctx, "SELECT COUNT(*) FROM training WHERE "+clause, args...).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+courseCols+" FROM training WHERE "+clause+fmt.Sprintf(" ORDER BY title ASC LIMIT $%d OFFSET $%d", pos, pos+1), append(args, page.Limit, page.Offset)...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, err := scanCourse(rows)
			if err != nil {
				return err
			}
			out = append(out, *c)
		}
		return rows.Err()
	})
	return out, total, err
}

func (r *PGRepository) GetByID(ctx context.Context, orgID, id string) (*Course, error) {
	var c *Course
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		c, err = scanCourse(tx.QueryRow(ctx, "SELECT "+courseCols+" FROM training WHERE organization_id = $1 AND id = $2", orgID, id))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("training not found")
		}
		return err
	})
	return c, err
}

func (r *PGRepository) Create(ctx context.Context, c *Course) error {
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO training (organization_id, title, description, category, validity_months)
			VALUES ($1, $2, $3, $4, $5)
			RETURNING id::text, created_at, updated_at
		`, c.OrganizationID, c.Title, c.Description, c.Category, c.ValidityMonths).
			Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}

func (r *PGRepository) Update(ctx context.Context, orgID, id string, req UpdateCourseRequest) (*Course, error) {
	var c *Course
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
		if req.Category != nil {
			sets = append(sets, fmt.Sprintf("category = $%d", pos))
			args = append(args, *req.Category)
			pos++
		}
		if req.ValidityMonths != nil {
			sets = append(sets, fmt.Sprintf("validity_months = $%d", pos))
			args = append(args, *req.ValidityMonths)
			pos++
		}
		var err error
		if len(sets) == 0 {
			c, err = scanCourse(tx.QueryRow(ctx, "SELECT "+courseCols+" FROM training WHERE organization_id = $1 AND id = $2", orgID, id))
		} else {
			sets = append(sets, "updated_at = now()")
			c, err = scanCourse(tx.QueryRow(ctx, "UPDATE training SET "+strings.Join(sets, ", ")+" WHERE organization_id = $1 AND id = $2 RETURNING "+courseCols, args...))
		}
		if err == pgx.ErrNoRows {
			return fmt.Errorf("training not found")
		}
		return err
	})
	return c, err
}

func (r *PGRepository) Delete(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM training WHERE organization_id = $1 AND id = $2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("training not found")
		}
		return nil
	})
}

func (r *PGRepository) Assign(ctx context.Context, a *Assignment) (*Assignment, error) {
	err := database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if a.Status == "" {
			a.Status = "assigned"
		}
		a.AssignedAt = time.Now().UTC()
		return tx.QueryRow(ctx, `
			INSERT INTO training_assignment (organization_id, training_id, user_id, assigned_at, due_at, status)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5, $6)
			RETURNING id::text, created_at
		`, a.OrganizationID, a.TrainingID, a.UserID, a.AssignedAt, a.DueAt, a.Status).
			Scan(&a.ID, &a.CreatedAt)
	})
	return a, err
}

func (r *PGRepository) Complete(ctx context.Context, orgID, assignmentID, proofObjectKey string) (*Assignment, error) {
	var a *Assignment
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		a, err = scanAssignment(tx.QueryRow(ctx, `
			UPDATE training_assignment SET completed_at = now(), proof_object_key = NULLIF($3,''), status = 'completed'
			WHERE organization_id = $1 AND id = $2
			RETURNING `+assignmentCols, orgID, assignmentID, proofObjectKey))
		if err == pgx.ErrNoRows {
			return fmt.Errorf("training assignment not found")
		}
		return err
	})
	return a, err
}

func (r *PGRepository) ListAssignments(ctx context.Context, orgID, trainingID string) ([]Assignment, error) {
	out := []Assignment{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+assignmentCols+" FROM training_assignment WHERE organization_id = $1 AND training_id = $2 ORDER BY assigned_at DESC", orgID, trainingID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			a, err := scanAssignment(rows)
			if err != nil {
				return err
			}
			out = append(out, *a)
		}
		return rows.Err()
	})
	return out, err
}
