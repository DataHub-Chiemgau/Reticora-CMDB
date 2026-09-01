// Package training implements the training management module (spec §6.9):
// courses with assignments, due dates and proof documents.
package training

import "time"

// Course is a training course definition.
type Course struct {
	ID              string    `json:"id"`
	OrganizationID  string    `json:"organization_id"`
	Title           string    `json:"title"`
	Description     string    `json:"description,omitempty"`
	Category        string    `json:"category,omitempty"`
	ValidityMonths  *int      `json:"validity_months,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// Assignment is a user's enrollment in a course with due date and proof.
type Assignment struct {
	ID             string     `json:"id"`
	OrganizationID string     `json:"organization_id"`
	TrainingID     string     `json:"training_id"`
	UserID         string     `json:"user_id"`
	AssignedAt     time.Time  `json:"assigned_at"`
	DueAt          *time.Time `json:"due_at,omitempty"`
	CompletedAt    *time.Time `json:"completed_at,omitempty"`
	ProofObjectKey string     `json:"proof_object_key,omitempty"`
	Status         string     `json:"status"` // assigned | completed | overdue | expired
	CreatedAt      time.Time  `json:"created_at"`
}

// CreateCourseRequest is the payload for creating a course.
type CreateCourseRequest struct {
	Title          string `json:"title"`
	Description    string `json:"description,omitempty"`
	Category       string `json:"category,omitempty"`
	ValidityMonths *int   `json:"validity_months,omitempty"`
}

// UpdateCourseRequest is the payload for updating a course.
type UpdateCourseRequest struct {
	Title          *string `json:"title,omitempty"`
	Description    *string `json:"description,omitempty"`
	Category       *string `json:"category,omitempty"`
	ValidityMonths *int    `json:"validity_months,omitempty"`
}

// AssignRequest enrolls a user in a course.
type AssignRequest struct {
	UserID string `json:"user_id"`
	DueAt  string `json:"due_at,omitempty"`
}

// CompleteRequest marks an assignment completed with optional proof.
type CompleteRequest struct {
	ProofObjectKey string `json:"proof_object_key,omitempty"`
}

// FilterParams scopes course list queries.
type FilterParams struct {
	Category string
	Search   string
}
