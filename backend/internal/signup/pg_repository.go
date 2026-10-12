package signup

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
)

// AuditAction is the operator_audit action of a completed signup.
const AuditAction = "signup.complete"

// PGRepository stores signup requests in signup_request (an operator table)
// and creates organizations in one organization-setup transaction.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates the repository.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// CreateRequest stores a pending request.
func (r *PGRepository) CreateRequest(ctx context.Context, req *PendingRequest) error {
	return database.WithOperator(ctx, r.pool, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO signup_request (email, organization_name, display_name, token_hash, expires_at)
			VALUES ($1, $2, $3, $4, $5) RETURNING id::text`,
			req.Email, req.OrganizationName, req.DisplayName, req.TokenHash, req.ExpiresAt).Scan(&req.ID); err != nil {
			return fmt.Errorf("insert signup request: %w", err)
		}
		return nil
	})
}

// Complete implements Repository: in one transaction it locks the request,
// creates the organization (its triggers seed the standard roles and the
// default VRF), the first user as org_admin, the trial entitlements, runs
// createAccount, writes operator_audit and marks the request completed.
func (r *PGRepository) Complete(ctx context.Context, tokenHash string, now time.Time, setup *Setup,
	createAccount func(ctx context.Context, req *PendingRequest) error) (Result, error) {
	result := Result{OrganizationID: setup.OrganizationID, Plan: setup.Plan, TrialEndsAt: setup.TrialEndsAt}
	err := database.WithOrganizationSetup(ctx, r.pool, setup.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		req, err := lockRequest(ctx, tx, tokenHash, now)
		if err != nil {
			return err
		}
		slug, err := insertOrganization(ctx, tx, req.OrganizationName, setup)
		if err != nil {
			return err
		}
		result.OrganizationSlug = slug

		var userID string
		if err = tx.QueryRow(ctx, `INSERT INTO app_user (organization_id, email, display_name)
			VALUES ($1, $2, $3) RETURNING id::text`,
			setup.OrganizationID, req.Email, req.DisplayName).Scan(&userID); err != nil {
			return fmt.Errorf("insert first user: %w", err)
		}
		tag, err := tx.Exec(ctx, `INSERT INTO role_assignment (organization_id, user_id, role_id)
			SELECT $1, $2, id FROM role WHERE organization_id = $1 AND name = 'org_admin'`,
			setup.OrganizationID, userID)
		if err != nil {
			return fmt.Errorf("assign org_admin: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return errors.New("assign org_admin: the standard role org_admin was not seeded")
		}
		for i := range setup.Entitlements {
			ent := &setup.Entitlements[i]
			if _, err := tx.Exec(ctx, `INSERT INTO entitlement (organization_id, feature_key, plan, enabled, limits, valid_until, source)
				VALUES ($1, $2, $3, $4, $5, $6, $7)`,
				setup.OrganizationID, ent.FeatureKey, string(ent.Plan), ent.Enabled, ent.Limits, ent.ValidUntil, ent.Source); err != nil {
				return fmt.Errorf("insert trial entitlement %s: %w", ent.FeatureKey, err)
			}
		}

		if err := createAccount(ctx, req); err != nil {
			return err
		}

		// No address in the organization-spanning audit: the request id
		// identifies the signup (PII stays in the tenant, E-15).
		if err := operator.RecordTx(ctx, tx, &operator.AuditEntry{
			OperatorID: req.ID, OperatorKind: "signup", Action: AuditAction,
			TargetOrg: setup.OrganizationID, Status: 201,
			Details: map[string]any{"slug": slug, "plan": string(setup.Plan),
				"trial_ends_at": setup.TrialEndsAt.Format(time.RFC3339)},
		}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE signup_request SET status = 'completed', completed_at = $2, completed_org_id = $3
			WHERE id = $1`, req.ID, now, setup.OrganizationID); err != nil {
			return fmt.Errorf("complete signup request: %w", err)
		}
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

// lockRequest reads and locks the pending request of tokenHash.
func lockRequest(ctx context.Context, tx pgx.Tx, tokenHash string, now time.Time) (*PendingRequest, error) {
	var (
		req    PendingRequest
		status string
	)
	err := tx.QueryRow(ctx, `SELECT id::text, email, organization_name, display_name, token_hash, expires_at, status
		FROM signup_request WHERE token_hash = $1 FOR UPDATE`, tokenHash).
		Scan(&req.ID, &req.Email, &req.OrganizationName, &req.DisplayName, &req.TokenHash, &req.ExpiresAt, &status)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return nil, ErrInvalidToken
	case err != nil:
		return nil, fmt.Errorf("read signup request: %w", err)
	case status != "pending":
		return nil, ErrInvalidToken
	case !now.Before(req.ExpiresAt):
		return nil, ErrExpired
	}
	return &req, nil
}

// insertOrganization creates the organization under the first free slug
// derived from its name (slug, slug-2 … slug-20). The slug is unique across
// organizations; a taken slug is detected by the unique violation inside a
// savepoint, since other organizations are invisible here.
func insertOrganization(ctx context.Context, tx pgx.Tx, name string, setup *Setup) (string, error) {
	base := Slugify(name)
	settings := map[string]any{"trial": map[string]any{
		"plan": string(setup.Plan), "ends_at": setup.TrialEndsAt.Format(time.RFC3339),
		"fallback_plan": string(setup.FallbackPlan),
	}}
	for n := 1; n <= 20; n++ {
		slug := base
		if n > 1 {
			slug = base + "-" + strconv.Itoa(n)
		}
		sp, err := tx.Begin(ctx)
		if err != nil {
			return "", fmt.Errorf("savepoint: %w", err)
		}
		_, err = sp.Exec(ctx, `INSERT INTO organization (id, name, slug, plan, settings) VALUES ($1, $2, $3, $4, $5)`,
			setup.OrganizationID, name, slug, string(setup.Plan), settings)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "organization_slug_key" {
			if rerr := sp.Rollback(ctx); rerr != nil {
				return "", fmt.Errorf("rollback savepoint: %w", rerr)
			}
			continue
		}
		if err != nil {
			_ = sp.Rollback(ctx)
			return "", fmt.Errorf("insert organization: %w", err)
		}
		if err := sp.Commit(ctx); err != nil {
			return "", fmt.Errorf("release savepoint: %w", err)
		}
		return slug, nil
	}
	return "", fmt.Errorf("no free organization slug for %q", base)
}
