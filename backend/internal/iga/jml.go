package iga

import (
	"context"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

type LifecycleService struct{ repo Repository }

func NewLifecycleService(repo Repository) *LifecycleService { return &LifecycleService{repo: repo} }
func (s *LifecycleService) Resolve(ctx context.Context, orgID string, change IdentityChange) ([]ProvisioningTask, error) {
	policies, _, err := s.repo.ListPolicies(ctx, orgID, change.Event, true, api.PaginationParams{Limit: 100})
	if err != nil {
		return nil, err
	}
	tasks := []ProvisioningTask{}
	for _, p := range policies {
		if !matchConditions(p.Conditions, change.Attributes) {
			continue
		}
		for _, a := range p.Actions {
			connectorID, _ := a["connector_id"].(string)
			action, _ := a["action"].(string)
			if connectorID == "" || action == "" {
				continue
			}
			payload := JSONMap{"identity_change": change, "policy_id": p.ID}
			for k, v := range a {
				payload[k] = v
			}
			t := ProvisioningTask{OrganizationID: orgID, ConnectorID: connectorID, UserID: change.UserID, Action: action, Status: TaskStatusPending, Payload: payload, MaxAttempts: 3, NextRunAt: time.Now().UTC()}
			if err := s.repo.CreateTask(ctx, &t); err != nil {
				return nil, err
			}
			tasks = append(tasks, t)
		}
	}
	return tasks, nil
}

func BackoffDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 6 {
		attempt = 6
	}
	return time.Duration(1<<uint(attempt-1)) * time.Minute
}

type TaskRunner struct {
	repo     Repository
	registry *Registry
	decrypt  func(context.Context, string, string) (JSONMap, error)
}

func NewTaskRunner(repo Repository, registry *Registry, decrypt func(context.Context, string, string) (JSONMap, error)) *TaskRunner {
	return &TaskRunner{repo: repo, registry: registry, decrypt: decrypt}
}
func (r *TaskRunner) RunDue(ctx context.Context, orgID string, limit int) error {
	tasks, err := r.repo.DueTasks(ctx, orgID, time.Now().UTC(), limit)
	if err != nil {
		return err
	}
	for i := range tasks {
		if err := r.RunTask(ctx, &tasks[i]); err != nil {
			return err
		}
	}
	return nil
}
func (r *TaskRunner) RunTask(ctx context.Context, t *ProvisioningTask) error {
	now := time.Now().UTC()
	t.Attempts++
	t.LastRunAt = &now
	t.Status = TaskStatusRunning
	_ = r.repo.UpdateTask(ctx, t)
	cfg, err := r.repo.GetConnector(ctx, t.OrganizationID, t.ConnectorID)
	if err == nil {
		var secret JSONMap
		if r.decrypt != nil && cfg.CredentialID != "" {
			secret, err = r.decrypt(ctx, t.OrganizationID, cfg.CredentialID)
		}
		if err == nil {
			var conn Connector
			conn, err = r.registry.Build(*cfg, secret)
			if err == nil {
				t.Result = JSONMap{}
				err = executeConnectorTask(ctx, conn, t)
			}
		}
	}
	if err != nil {
		t.Error = err.Error()
		if t.Attempts >= t.MaxAttempts {
			t.Status = TaskStatusFailed
		} else {
			t.Status = TaskStatusPending
			t.NextRunAt = now.Add(BackoffDelay(t.Attempts))
		}
		return r.repo.UpdateTask(ctx, t)
	}
	t.Status = TaskStatusSucceeded
	t.Error = ""
	t.NextRunAt = now
	return r.repo.UpdateTask(ctx, t)
}
func executeConnectorTask(ctx context.Context, conn Connector, t *ProvisioningTask) error {
	account := Account{ID: t.ExternalID}
	if v, ok := t.Payload["userName"].(string); ok {
		account.UserName = v
	}
	if v, ok := t.Payload["displayName"].(string); ok {
		account.DisplayName = v
	}
	if v, ok := t.Payload["active"].(bool); ok {
		account.Active = v
	} else {
		account.Active = true
	}
	switch t.Action {
	case TaskActionCreateAccount:
		a, err := conn.CreateAccount(ctx, account)
		t.Result = JSONMap{"account_id": a.ID}
		return err
	case TaskActionUpdateAccount:
		_, err := conn.UpdateAccount(ctx, t.ExternalID, account)
		return err
	case TaskActionDisableAccount:
		return conn.DisableAccount(ctx, t.ExternalID)
	case TaskActionDeleteAccount:
		return conn.DeleteAccount(ctx, t.ExternalID)
	case TaskActionAddGroupMember:
		return conn.AddGroupMember(ctx, fmt.Sprint(t.Payload["group_id"]), fmt.Sprint(t.Payload["account_id"]))
	case TaskActionRemoveGroupMember:
		return conn.RemoveGroupMember(ctx, fmt.Sprint(t.Payload["group_id"]), fmt.Sprint(t.Payload["account_id"]))
	default:
		return fmt.Errorf("unsupported provisioning action %q", t.Action)
	}
}
