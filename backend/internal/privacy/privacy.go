// Package privacy implements the DSGVO (GDPR) data-lifecycle controls of
// Epic F: per-tenant retention policies and the erasure workflow that
// enforces them (Art. 5 Abs. 1 lit. e — Speicherbegrenzung, Art. 17 —
// Recht auf Löschung).
package privacy

import (
	"context"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// RetentionPolicy is the per-tenant data-retention configuration.
type RetentionPolicy struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	// RetentionDays is the retention window for personal data in days;
	// 0 means "keep forever".
	RetentionDays int `json:"retention_days"`
	// Mode decides what the erasure workflow does with expired records:
	// "anonymize" (keep rows with surrogate values) or "delete".
	Mode      string    `json:"mode"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// UpdatePolicyRequest is the payload for configuring the retention policy.
type UpdatePolicyRequest struct {
	RetentionDays *int    `json:"retention_days,omitempty"`
	Mode          *string `json:"mode,omitempty"`
}

// Repository persists the retention policy.
type Repository interface {
	GetPolicy(ctx context.Context, orgID string) (*RetentionPolicy, error)
	UpsertPolicy(ctx context.Context, policy *RetentionPolicy) (*RetentionPolicy, error)
}

// ErrNoPolicy is returned when the tenant has not configured a policy yet.
var ErrNoPolicy = fmt.Errorf("privacy: no retention policy configured")

// ContactEraser removes or anonymizes expired contact records.
type ContactEraser interface {
	List(ctx context.Context, orgID, clientID string, page api.PaginationParams) ([]contact.Contact, int, error)
	Delete(ctx context.Context, orgID, id string) error
	Update(ctx context.Context, orgID, id string, req contact.UpdateContactRequest) (*contact.Contact, error)
}

// UserEraser removes or anonymizes expired, already-inactive user accounts.
type UserEraser interface {
	ListUsers(ctx context.Context, orgID, search string, page api.PaginationParams) ([]user.User, int, error)
	DeleteUser(ctx context.Context, orgID, id string) error
	AnonymizeUser(ctx context.Context, orgID, id string) (*user.User, error)
}

// ErasureSummary reports what the erasure workflow did.
type ErasureSummary struct {
	// Cutoff is the point in time before which personal data was expired.
	Cutoff time.Time `json:"cutoff"`
	// Mode applied to expired records (anonymize/delete).
	Mode string `json:"mode"`
	// ContactsAffected counts erased contact records.
	ContactsAffected int `json:"contacts_affected"`
	// UsersAffected counts erased inactive user accounts.
	UsersAffected int `json:"users_affected"`
}

// Service runs the retention policy against the tenant's personal data.
type Service struct {
	repo     Repository
	contacts ContactEraser
	users    UserEraser
	now      func() time.Time
}

// NewService creates the privacy service. contacts and users may be nil;
// the erasure workflow then skips the corresponding section.
func NewService(repo Repository, contacts ContactEraser, users UserEraser) *Service {
	return &Service{
		repo:     repo,
		contacts: contacts,
		users:    users,
		now:      func() time.Time { return time.Now().UTC() },
	}
}

// GetPolicy returns the tenant's policy or ErrNoPolicy.
func (s *Service) GetPolicy(ctx context.Context, orgID string) (*RetentionPolicy, error) {
	return s.repo.GetPolicy(ctx, orgID)
}

// Configure creates or updates the tenant's retention policy.
func (s *Service) Configure(ctx context.Context, orgID string, req UpdatePolicyRequest) (*RetentionPolicy, error) {
	policy, err := s.repo.GetPolicy(ctx, orgID)
	if err == ErrNoPolicy {
		policy = &RetentionPolicy{OrganizationID: orgID, Mode: "anonymize"}
	} else if err != nil {
		return nil, err
	}
	if req.RetentionDays != nil {
		if *req.RetentionDays < 0 {
			return nil, fmt.Errorf("privacy: retention_days must be >= 0")
		}
		policy.RetentionDays = *req.RetentionDays
	}
	if req.Mode != nil {
		if *req.Mode != "anonymize" && *req.Mode != "delete" {
			return nil, fmt.Errorf("privacy: mode must be anonymize or delete")
		}
		policy.Mode = *req.Mode
	}
	return s.repo.UpsertPolicy(ctx, policy)
}

// RunErasure enforces the configured retention policy: every contact record
// and every already-inactive user account whose last update predates the
// retention window is anonymized (default) or deleted, depending on the
// policy mode. Running the workflow requires an explicit policy so a tenant
// can never purge data by accident.
func (s *Service) RunErasure(ctx context.Context, orgID string) (*ErasureSummary, error) {
	policy, err := s.repo.GetPolicy(ctx, orgID)
	if err != nil {
		return nil, err
	}
	if policy.RetentionDays <= 0 {
		return nil, fmt.Errorf("privacy: retention policy keeps data forever; erasure would be a no-op")
	}

	cutoff := s.now().AddDate(0, 0, -policy.RetentionDays)
	summary := &ErasureSummary{Cutoff: cutoff, Mode: policy.Mode}

	if s.contacts != nil {
		affected, err := s.eraseContacts(ctx, orgID, cutoff, policy.Mode)
		if err != nil {
			return nil, fmt.Errorf("erase contacts: %w", err)
		}
		summary.ContactsAffected = affected
	}

	if s.users != nil {
		affected, err := s.eraseUsers(ctx, orgID, cutoff, policy.Mode)
		if err != nil {
			return nil, fmt.Errorf("erase users: %w", err)
		}
		summary.UsersAffected = affected
	}

	return summary, nil
}

func (s *Service) eraseContacts(ctx context.Context, orgID string, cutoff time.Time, mode string) (int, error) {
	affected := 0
	page := api.PaginationParams{Limit: 200}
	for {
		items, total, err := s.contacts.List(ctx, orgID, "", page)
		if err != nil {
			return affected, err
		}
		for _, c := range items {
			if c.UpdatedAt.After(cutoff) {
				continue
			}
			if mode == "delete" {
				if err := s.contacts.Delete(ctx, orgID, c.ID); err != nil {
					return affected, err
				}
			} else {
				name := user.SurrogateDisplayName(c.ID)
				email := user.SurrogateEmail(c.ID)
				empty := ""
				req := contact.UpdateContactRequest{
					DisplayName: &name,
					Email:       &email,
					Phone:       &empty,
					Role:        &empty,
					Department:  &empty,
					Notes:       &empty,
				}
				if _, err := s.contacts.Update(ctx, orgID, c.ID, req); err != nil {
					return affected, err
				}
			}
			affected++
		}
		page.Offset += len(items)
		if len(items) == 0 || page.Offset >= total {
			break
		}
	}
	return affected, nil
}

func (s *Service) eraseUsers(ctx context.Context, orgID string, cutoff time.Time, mode string) (int, error) {
	affected := 0
	page := api.PaginationParams{Limit: 200}
	for {
		items, total, err := s.users.ListUsers(ctx, orgID, "", page)
		if err != nil {
			return affected, err
		}
		for _, u := range items {
			// Only accounts that are already deactivated/anonymized are
			// expired by retention — active staff is not personal data "no
			// longer necessary" while the employment lasts.
			if u.Status == "active" || u.UpdatedAt.After(cutoff) {
				continue
			}
			if mode == "delete" {
				if err := s.users.DeleteUser(ctx, orgID, u.ID); err != nil {
					return affected, err
				}
			} else {
				if u.Status == "anonymized" {
					continue
				}
				if _, err := s.users.AnonymizeUser(ctx, orgID, u.ID); err != nil {
					return affected, err
				}
			}
			affected++
		}
		page.Offset += len(items)
		if len(items) == 0 || page.Offset >= total {
			break
		}
	}
	return affected, nil
}
