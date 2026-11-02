package iga

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type PGRepository struct{ pool *pgxpool.Pool }

func NewPGRepository(pool *pgxpool.Pool) *PGRepository { return &PGRepository{pool: pool} }

// RunInTx implements TxRunner: fn runs in one transaction of the context's
// tenant scope, which every repository call with fn's context joins.
func (r *PGRepository) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return database.WithRequestTenantTx(ctx, r.pool, orgID, fn)
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// requireRef fails with a 422 field error on field when id (if set) is no
// UUID or names no row of table visible under the caller's scope. The
// foreign keys of the IGA tables (app_user, credential, collector,
// iga_connector) are checked without row level security: an object of
// another organization passed them and an unknown one failed with the
// constraint text, so the two could be told apart (TEN-04, API-03, F-047).
// The check runs before the write, in its transaction.
func requireRef(ctx context.Context, tx pgx.Tx, table, field, missing, orgID, id string) error {
	if id == "" {
		return nil
	}
	if err := api.RequireUUID(field, id); err != nil {
		return err
	}
	var visible bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM "+table+" WHERE id = $1 AND organization_id = $2)", id, orgID).Scan(&visible); err != nil {
		return fmt.Errorf("check %s: %w", field, err)
	}
	if !visible {
		return &api.FieldError{Field: field, Message: missing, Status: http.StatusUnprocessableEntity}
	}
	return nil
}

// requireConnectorRefs checks the credential and the collector a connector
// names (empty: none).
func requireConnectorRefs(ctx context.Context, tx pgx.Tx, orgID, credentialID, collectorID string) error {
	if err := requireRef(ctx, tx, "credential", "credential_id", "credential not found", orgID, credentialID); err != nil {
		return err
	}
	return requireRef(ctx, tx, "collector", "collector_id", "collector not found", orgID, collectorID)
}

const connectorCols = `id::text, organization_id::text, name, type, COALESCE(base_url,''), COALESCE(credential_id::text,''), COALESCE(collector_id::text,''), capabilities, config, status, last_sync_at, created_at, updated_at`

func scanConnector(s interface{ Scan(...any) error }) (*ConnectorConfig, error) {
	c := &ConnectorConfig{}
	var last sql.NullTime
	err := s.Scan(&c.ID, &c.OrganizationID, &c.Name, &c.Type, &c.BaseURL, &c.CredentialID, &c.CollectorID, &c.Capabilities, &c.Config, &c.Status, &last, &c.CreatedAt, &c.UpdatedAt)
	if last.Valid {
		c.LastSyncAt = &last.Time
	}
	if c.Config == nil {
		c.Config = JSONMap{}
	}
	return c, err
}
func (r *PGRepository) ListConnectors(ctx context.Context, orgID string, p api.PaginationParams) ([]ConnectorConfig, int, error) {
	out := []ConnectorConfig{}
	total := 0
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM iga_connector WHERE organization_id=$1", orgID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+connectorCols+" FROM iga_connector WHERE organization_id=$1 ORDER BY name LIMIT $2 OFFSET $3", orgID, p.Limit, p.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanConnector(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) GetConnector(ctx context.Context, orgID, id string) (*ConnectorConfig, error) {
	var out *ConnectorConfig
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		v, err := scanConnector(tx.QueryRow(ctx, "SELECT "+connectorCols+" FROM iga_connector WHERE organization_id=$1 AND id=$2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrConnectorNotFound
		}
		if err != nil {
			return fmt.Errorf("get connector: %w", err)
		}
		out = v
		return nil
	})
	return out, err
}
func (r *PGRepository) CreateConnector(ctx context.Context, c *ConnectorConfig) error {
	if c.Status == "" {
		c.Status = "active"
	}
	if c.Capabilities == (ConnectorCapabilities{}) {
		c.Capabilities = DefaultCapabilities(c.Type)
	}
	if c.Config == nil {
		c.Config = JSONMap{}
	}
	return database.WithRequestTenant(ctx, r.pool, c.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireConnectorRefs(ctx, tx, c.OrganizationID, c.CredentialID, c.CollectorID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO iga_connector (organization_id,name,type,base_url,credential_id,collector_id,capabilities,config,status) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text, created_at, updated_at`, c.OrganizationID, c.Name, c.Type, nullString(c.BaseURL), nullString(c.CredentialID), nullString(c.CollectorID), c.Capabilities, c.Config, c.Status).Scan(&c.ID, &c.CreatedAt, &c.UpdatedAt)
	})
}
func (r *PGRepository) UpdateConnector(ctx context.Context, orgID, id string, req UpdateConnectorRequest) (*ConnectorConfig, error) {
	cur, err := r.GetConnector(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	if req.Name != nil {
		cur.Name = *req.Name
	}
	if req.BaseURL != nil {
		cur.BaseURL = *req.BaseURL
	}
	if req.CredentialID != nil {
		cur.CredentialID = *req.CredentialID
	}
	if req.CollectorID != nil {
		cur.CollectorID = *req.CollectorID
	}
	if req.Capabilities != nil {
		cur.Capabilities = *req.Capabilities
	}
	if req.Config != nil {
		cur.Config = req.Config
	}
	if req.Status != nil {
		cur.Status = *req.Status
	}
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var credentialID, collectorID string
		if req.CredentialID != nil {
			credentialID = *req.CredentialID
		}
		if req.CollectorID != nil {
			collectorID = *req.CollectorID
		}
		if refErr := requireConnectorRefs(ctx, tx, orgID, credentialID, collectorID); refErr != nil {
			return refErr
		}
		_, err := tx.Exec(ctx, `UPDATE iga_connector SET name=$3,base_url=$4,credential_id=$5,collector_id=$6,capabilities=$7,config=$8,status=$9,updated_at=now() WHERE organization_id=$1 AND id=$2`, orgID, id, cur.Name, nullString(cur.BaseURL), nullString(cur.CredentialID), nullString(cur.CollectorID), cur.Capabilities, cur.Config, cur.Status)
		return err
	})
	if err != nil {
		return nil, err
	}
	return r.GetConnector(ctx, orgID, id)
}
func (r *PGRepository) DeleteConnector(ctx context.Context, orgID, id string) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, "DELETE FROM iga_connector WHERE organization_id=$1 AND id=$2", orgID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrConnectorNotFound
		}
		return nil
	})
}
func (r *PGRepository) MarkConnectorSynced(ctx context.Context, orgID, id string, at time.Time) error {
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, "UPDATE iga_connector SET last_sync_at=$3,updated_at=now() WHERE organization_id=$1 AND id=$2", orgID, id, at)
		return err
	})
}

const taskCols = `id::text, organization_id::text, connector_id::text, COALESCE(user_id::text,''), COALESCE(external_id,''), action, status, payload, result, COALESCE(error,''), attempts, max_attempts, next_run_at, last_run_at, created_at, updated_at`

func scanTask(s interface{ Scan(...any) error }) (*ProvisioningTask, error) {
	t := &ProvisioningTask{}
	var last sql.NullTime
	err := s.Scan(&t.ID, &t.OrganizationID, &t.ConnectorID, &t.UserID, &t.ExternalID, &t.Action, &t.Status, &t.Payload, &t.Result, &t.Error, &t.Attempts, &t.MaxAttempts, &t.NextRunAt, &last, &t.CreatedAt, &t.UpdatedAt)
	if last.Valid {
		t.LastRunAt = &last.Time
	}
	if t.Payload == nil {
		t.Payload = JSONMap{}
	}
	if t.Result == nil {
		t.Result = JSONMap{}
	}
	return t, err
}
func (r *PGRepository) ListTasks(ctx context.Context, orgID, status string, p api.PaginationParams) ([]ProvisioningTask, int, error) {
	out := []ProvisioningTask{}
	total := 0
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id=$1"
		args := []any{orgID}
		if status != "" {
			where += " AND status=$2"
			args = append(args, status)
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM iga_provisioning_task WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		args = append(args, p.Limit, p.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM iga_provisioning_task WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", taskCols, where, len(args)-1, len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanTask(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) GetTask(ctx context.Context, orgID, id string) (*ProvisioningTask, error) {
	var out *ProvisioningTask
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		v, err := scanTask(tx.QueryRow(ctx, "SELECT "+taskCols+" FROM iga_provisioning_task WHERE organization_id=$1 AND id=$2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTaskNotFound
		}
		if err != nil {
			return fmt.Errorf("get task: %w", err)
		}
		out = v
		return nil
	})
	return out, err
}
func (r *PGRepository) CreateTask(ctx context.Context, t *ProvisioningTask) error {
	if t.Status == "" {
		t.Status = TaskStatusPending
	}
	if t.MaxAttempts == 0 {
		t.MaxAttempts = 3
	}
	if t.NextRunAt.IsZero() {
		t.NextRunAt = time.Now().UTC()
	}
	if t.Payload == nil {
		t.Payload = JSONMap{}
	}
	return database.WithRequestTenant(ctx, r.pool, t.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireRef(ctx, tx, "iga_connector", "connector_id", "connector not found", t.OrganizationID, t.ConnectorID); err != nil {
			return err
		}
		if err := requireRef(ctx, tx, "app_user", "user_id", "user not found", t.OrganizationID, t.UserID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO iga_provisioning_task (organization_id,connector_id,user_id,external_id,action,status,payload,max_attempts,next_run_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text,created_at,updated_at`, t.OrganizationID, t.ConnectorID, nullString(t.UserID), nullString(t.ExternalID), t.Action, t.Status, t.Payload, t.MaxAttempts, t.NextRunAt).Scan(&t.ID, &t.CreatedAt, &t.UpdatedAt)
	})
}
func (r *PGRepository) UpdateTask(ctx context.Context, t *ProvisioningTask) error {
	return database.WithRequestTenant(ctx, r.pool, t.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iga_provisioning_task SET status=$3,result=$4,error=$5,attempts=$6,next_run_at=$7,last_run_at=$8,updated_at=now() WHERE organization_id=$1 AND id=$2`, t.OrganizationID, t.ID, t.Status, t.Result, t.Error, t.Attempts, t.NextRunAt, t.LastRunAt)
		return err
	})
}
func (r *PGRepository) DueTasks(ctx context.Context, orgID string, now time.Time, limit int) ([]ProvisioningTask, error) {
	out := []ProvisioningTask{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+taskCols+" FROM iga_provisioning_task WHERE organization_id=$1 AND status='pending' AND next_run_at <= $2 ORDER BY next_run_at LIMIT $3", orgID, now, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanTask(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, err
}

const policyCols = `id::text,organization_id::text,name,event,priority,active,conditions,actions,created_at,updated_at`

func scanPolicy(s interface{ Scan(...any) error }) (*LifecyclePolicy, error) {
	p := &LifecyclePolicy{}
	return p, s.Scan(&p.ID, &p.OrganizationID, &p.Name, &p.Event, &p.Priority, &p.Active, &p.Conditions, &p.Actions, &p.CreatedAt, &p.UpdatedAt)
}
func (r *PGRepository) ListPolicies(ctx context.Context, orgID, event string, activeOnly bool, p api.PaginationParams) ([]LifecyclePolicy, int, error) {
	out := []LifecyclePolicy{}
	total := 0
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id=$1"
		args := []any{orgID}
		if event != "" {
			where += fmt.Sprintf(" AND event=$%d", len(args)+1)
			args = append(args, event)
		}
		if activeOnly {
			where += " AND active"
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM iga_lifecycle_policy WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		args = append(args, p.Limit, p.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM iga_lifecycle_policy WHERE %s ORDER BY priority DESC LIMIT $%d OFFSET $%d", policyCols, where, len(args)-1, len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanPolicy(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) CreatePolicy(ctx context.Context, p *LifecyclePolicy) error {
	if p.Conditions == nil {
		p.Conditions = JSONMap{}
	}
	return database.WithRequestTenant(ctx, r.pool, p.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iga_lifecycle_policy (organization_id,name,event,priority,active,conditions,actions) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id::text,created_at,updated_at`, p.OrganizationID, p.Name, p.Event, p.Priority, p.Active, p.Conditions, p.Actions).Scan(&p.ID, &p.CreatedAt, &p.UpdatedAt)
	})
}

const reqCols = `id::text,organization_id::text,requester_id::text,subject_user_id::text,COALESCE(connector_id::text,''),entitlement,COALESCE(reason,''),status,COALESCE(workflow_run_id::text,''),COALESCE(decision_by::text,''),COALESCE(decision_comment,''),decided_at,created_at,updated_at`

func scanReq(s interface{ Scan(...any) error }) (*AccessRequest, error) {
	a := &AccessRequest{}
	var d sql.NullTime
	err := s.Scan(&a.ID, &a.OrganizationID, &a.RequesterID, &a.SubjectUserID, &a.ConnectorID, &a.Entitlement, &a.Reason, &a.Status, &a.WorkflowRunID, &a.DecisionBy, &a.DecisionComment, &d, &a.CreatedAt, &a.UpdatedAt)
	if d.Valid {
		a.DecidedAt = &d.Time
	}
	return a, err
}
func (r *PGRepository) ListAccessRequests(ctx context.Context, orgID, status string, p api.PaginationParams) ([]AccessRequest, int, error) {
	out := []AccessRequest{}
	total := 0
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id=$1"
		args := []any{orgID}
		if status != "" {
			where += " AND status=$2"
			args = append(args, status)
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM iga_access_request WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		args = append(args, p.Limit, p.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM iga_access_request WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", reqCols, where, len(args)-1, len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanReq(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) CreateAccessRequest(ctx context.Context, a *AccessRequest) error {
	if a.Status == "" {
		a.Status = "pending"
	}
	return database.WithRequestTenant(ctx, r.pool, a.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := requireRef(ctx, tx, "app_user", "subject_user_id", "user not found", a.OrganizationID, a.SubjectUserID); err != nil {
			return err
		}
		if err := requireRef(ctx, tx, "iga_connector", "connector_id", "connector not found", a.OrganizationID, a.ConnectorID); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `INSERT INTO iga_access_request (organization_id,requester_id,subject_user_id,connector_id,entitlement,reason,status,workflow_run_id) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) RETURNING id::text,created_at,updated_at`, a.OrganizationID, a.RequesterID, a.SubjectUserID, nullString(a.ConnectorID), a.Entitlement, a.Reason, a.Status, nullString(a.WorkflowRunID)).Scan(&a.ID, &a.CreatedAt, &a.UpdatedAt)
	})
}
func (r *PGRepository) DecideAccessRequest(ctx context.Context, orgID, id, status, actor, comment string) (*AccessRequest, error) {
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE iga_access_request SET status=$3,decision_by=$4,decision_comment=$5,decided_at=now(),updated_at=now() WHERE organization_id=$1 AND id=$2`, orgID, id, status, nullString(actor), comment)
		if err == nil && tag.RowsAffected() == 0 {
			return ErrAccessRequestNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var out *AccessRequest
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		v, err := scanReq(tx.QueryRow(ctx, "SELECT "+reqCols+" FROM iga_access_request WHERE organization_id=$1 AND id=$2", orgID, id))
		out = v
		return err
	})
	return out, err
}

// ApproveAccessRequest approves the request and stores its provisioning task
// in one transaction of the request's tenant scope: a task that cannot be
// written (connector gone, database error) rolls the approval back, the
// request stays pending and the caller gets the error (F-056).
func (r *PGRepository) ApproveAccessRequest(ctx context.Context, orgID, id, actor, comment string) (*AccessRequest, error) {
	var out *AccessRequest
	err := r.RunInTx(ctx, orgID, func(ctx context.Context) error {
		ar, err := r.DecideAccessRequest(ctx, orgID, id, "approved", actor, comment)
		if err != nil {
			return err
		}
		if task := ApprovalTask(ar); task != nil {
			if err := r.CreateTask(ctx, task); err != nil {
				return fmt.Errorf("provisioning task of access request: %w", err)
			}
		}
		out = ar
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

const reviewCols = `id::text,organization_id::text,name,COALESCE(description,''),status,due_at,created_at,updated_at`

func scanReview(s interface{ Scan(...any) error }) (*AccessReview, error) {
	v := &AccessReview{}
	var due sql.NullTime
	err := s.Scan(&v.ID, &v.OrganizationID, &v.Name, &v.Description, &v.Status, &due, &v.CreatedAt, &v.UpdatedAt)
	if due.Valid {
		v.DueAt = &due.Time
	}
	return v, err
}

const itemCols = `id::text,organization_id::text,review_id::text,user_id::text,COALESCE(connector_id::text,''),entitlement,COALESCE(decision,''),COALESCE(decision_by::text,''),decided_at,created_at,updated_at`

func scanItem(s interface{ Scan(...any) error }) (*AccessReviewItem, error) {
	v := &AccessReviewItem{}
	var d sql.NullTime
	err := s.Scan(&v.ID, &v.OrganizationID, &v.ReviewID, &v.UserID, &v.ConnectorID, &v.Entitlement, &v.Decision, &v.DecisionBy, &d, &v.CreatedAt, &v.UpdatedAt)
	if d.Valid {
		v.DecidedAt = &d.Time
	}
	return v, err
}
func (r *PGRepository) ListReviews(ctx context.Context, orgID, status string, p api.PaginationParams) ([]AccessReview, int, error) {
	out := []AccessReview{}
	total := 0
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id=$1"
		args := []any{orgID}
		if status != "" {
			where += " AND status=$2"
			args = append(args, status)
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM iga_access_review WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		args = append(args, p.Limit, p.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM iga_access_review WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", reviewCols, where, len(args)-1, len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanReview(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) CreateReview(ctx context.Context, rv *AccessReview, items []AccessReviewItem) error {
	if rv.Status == "" {
		rv.Status = "active"
	}
	return database.WithRequestTenant(ctx, r.pool, rv.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `INSERT INTO iga_access_review (organization_id,name,description,status,due_at) VALUES ($1,$2,$3,$4,$5) RETURNING id::text,created_at,updated_at`, rv.OrganizationID, rv.Name, rv.Description, rv.Status, rv.DueAt).Scan(&rv.ID, &rv.CreatedAt, &rv.UpdatedAt); err != nil {
			return err
		}
		for i := range items {
			it := &items[i]
			it.OrganizationID = rv.OrganizationID
			it.ReviewID = rv.ID
			if err := requireRef(ctx, tx, "app_user", fmt.Sprintf("items[%d].user_id", i), "user not found", it.OrganizationID, it.UserID); err != nil {
				return err
			}
			if err := requireRef(ctx, tx, "iga_connector", fmt.Sprintf("items[%d].connector_id", i), "connector not found", it.OrganizationID, it.ConnectorID); err != nil {
				return err
			}
			if err := tx.QueryRow(ctx, `INSERT INTO iga_access_review_item (organization_id,review_id,user_id,connector_id,entitlement) VALUES ($1,$2,$3,$4,$5) RETURNING id::text,created_at,updated_at`, it.OrganizationID, it.ReviewID, it.UserID, nullString(it.ConnectorID), it.Entitlement).Scan(&it.ID, &it.CreatedAt, &it.UpdatedAt); err != nil {
				return err
			}
		}
		return nil
	})
}
func (r *PGRepository) ListReviewItems(ctx context.Context, orgID, reviewID string, p api.PaginationParams) ([]AccessReviewItem, int, error) {
	out := []AccessReviewItem{}
	total := 0
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM iga_access_review_item WHERE organization_id=$1 AND review_id=$2", orgID, reviewID).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT "+itemCols+" FROM iga_access_review_item WHERE organization_id=$1 AND review_id=$2 ORDER BY created_at LIMIT $3 OFFSET $4", orgID, reviewID, p.Limit, p.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanItem(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) DecideReviewItem(ctx context.Context, orgID, id, decision, actor string) (*AccessReviewItem, error) {
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE iga_access_review_item SET decision=$3,decision_by=$4,decided_at=now(),updated_at=now() WHERE organization_id=$1 AND id=$2`, orgID, id, decision, nullString(actor))
		if err == nil && tag.RowsAffected() == 0 {
			return ErrReviewItemNotFound
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	var out *AccessReviewItem
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		v, err := scanItem(tx.QueryRow(ctx, "SELECT "+itemCols+" FROM iga_access_review_item WHERE organization_id=$1 AND id=$2", orgID, id))
		out = v
		return err
	})
	return out, err
}

const driftCols = `id::text,organization_id::text,connector_id::text,external_id,COALESCE(user_id::text,''),drift_type,severity,expected,observed,status,COALESCE(remediation_task_id::text,''),created_at,updated_at`

func scanDrift(s interface{ Scan(...any) error }) (*DriftFinding, error) {
	v := &DriftFinding{}
	err := s.Scan(&v.ID, &v.OrganizationID, &v.ConnectorID, &v.ExternalID, &v.UserID, &v.DriftType, &v.Severity, &v.Expected, &v.Observed, &v.Status, &v.RemediationTaskID, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}
func (r *PGRepository) ListDrift(ctx context.Context, orgID, status string, p api.PaginationParams) ([]DriftFinding, int, error) {
	out := []DriftFinding{}
	total := 0
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := "organization_id=$1"
		args := []any{orgID}
		if status != "" {
			where += " AND status=$2"
			args = append(args, status)
		}
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM iga_drift_finding WHERE "+where, args...).Scan(&total); err != nil {
			return err
		}
		args = append(args, p.Limit, p.Offset)
		rows, err := tx.Query(ctx, fmt.Sprintf("SELECT %s FROM iga_drift_finding WHERE %s ORDER BY created_at DESC LIMIT $%d OFFSET $%d", driftCols, where, len(args)-1, len(args)), args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, err := scanDrift(rows)
			if err != nil {
				return err
			}
			out = append(out, *v)
		}
		return rows.Err()
	})
	return out, total, err
}
func (r *PGRepository) CreateDrift(ctx context.Context, f *DriftFinding) error {
	if f.Status == "" {
		f.Status = "open"
	}
	if f.Severity == "" {
		f.Severity = "medium"
	}
	return database.WithRequestTenant(ctx, r.pool, f.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `INSERT INTO iga_drift_finding (organization_id,connector_id,external_id,user_id,drift_type,severity,expected,observed,status) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id::text,created_at,updated_at`, f.OrganizationID, f.ConnectorID, f.ExternalID, nullString(f.UserID), f.DriftType, f.Severity, f.Expected, f.Observed, f.Status).Scan(&f.ID, &f.CreatedAt, &f.UpdatedAt)
	})
}
func (r *PGRepository) UpdateDrift(ctx context.Context, f *DriftFinding) error {
	return database.WithRequestTenant(ctx, r.pool, f.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE iga_drift_finding SET status=$3,remediation_task_id=$4,updated_at=now() WHERE organization_id=$1 AND id=$2`, f.OrganizationID, f.ID, f.Status, nullString(f.RemediationTaskID))
		return err
	})
}
func (r *PGRepository) GetDrift(ctx context.Context, orgID, id string) (*DriftFinding, error) {
	var out *DriftFinding
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		v, err := scanDrift(tx.QueryRow(ctx, "SELECT "+driftCols+" FROM iga_drift_finding WHERE organization_id=$1 AND id=$2", orgID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrDriftNotFound
		}
		if err != nil {
			return fmt.Errorf("get drift: %w", err)
		}
		out = v
		return nil
	})
	return out, err
}
