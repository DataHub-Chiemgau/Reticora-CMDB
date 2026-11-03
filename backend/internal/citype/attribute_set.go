package citype

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// ScopeSet is the scope of a set attribute: a row of attribute_definition
// that belongs to an attribute set (WP-250).
const ScopeSet = "set"

// AttributeSet is a named, reusable group of attribute definitions (MET-16,
// WP-250). Its attributes are rows of attribute_definition with scope "set"
// (DB-03); a type takes sets in an ordered list (ci_type_attribute_set).
// organization_id NULL marks a read-only system set (E-10). The active sets
// of a type enter its composition after its type chain (WP-709).
type AttributeSet struct {
	ID             string        `json:"id"`
	OrganizationID string        `json:"organization_id,omitempty"`
	Key            string        `json:"key"`
	DisplayName    LocalizedText `json:"display_name"`
	Description    string        `json:"description,omitempty"`
	IsSystem       bool          `json:"is_system"`
	IsActive       bool          `json:"is_active"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// AttributeSetPatch changes a set; the key is immutable.
type AttributeSetPatch struct {
	DisplayName LocalizedText `json:"display_name,omitempty"`
	Description *string       `json:"description,omitempty"`
	IsActive    *bool         `json:"is_active,omitempty"`
}

// AttributeSetAssignment is one set of a type with its position in the
// ordered list.
type AttributeSetAssignment struct {
	AttributeSetID string `json:"attribute_set_id"`
	Position       int    `json:"position"`
}

// TypeAttributeSets is the assignment document of a type: its sets ordered
// by position (GET/PUT /api/v1/ci-types/{id}/attribute-sets). It has no
// data array and is no list.
type TypeAttributeSets struct {
	CITypeID      string                   `json:"ci_type_id"`
	AttributeSets []AttributeSetAssignment `json:"attribute_sets"`
}

// Attribute set errors.
var (
	ErrAttributeSetNotFound = errors.New("attribute set not found")
	// ErrAttributeSetReadOnly: system sets cannot be changed, deleted or
	// extended by an organization (E-10).
	ErrAttributeSetReadOnly = errors.New("system attribute sets are read-only")
	// ErrAttributeSetInUse: a set assigned to a type cannot be deleted.
	ErrAttributeSetInUse = errors.New("the attribute set is assigned to a ci type")
	// ErrCITypeNotFound: the type of an assignment document is unknown.
	ErrCITypeNotFound = errors.New("ci type not found")
)

// AttributeSetStore is the attribute-sets resource and the set assignment
// of the types (API-06, WP-250); the PostgreSQL repository implements it.
type AttributeSetStore interface {
	// ListAttributeSetPage returns a page newest first on (created_at, id),
	// at most one set more than the limit, and the number of sets.
	ListAttributeSetPage(ctx context.Context, orgID string, page api.PaginationParams) ([]AttributeSet, int, error)
	GetAttributeSet(ctx context.Context, orgID, id string) (*AttributeSet, error)
	CreateAttributeSet(ctx context.Context, orgID string, set *AttributeSet) error
	UpdateAttributeSet(ctx context.Context, orgID, id string, patch *AttributeSetPatch) (*AttributeSet, error)
	DeleteAttributeSet(ctx context.Context, orgID, id string) error
	GetTypeAttributeSets(ctx context.Context, orgID, typeID string) (*TypeAttributeSets, error)
	// SetTypeAttributeSets replaces the ordered set list of a type.
	SetTypeAttributeSets(ctx context.Context, orgID, typeID string, sets []AttributeSetAssignment) (*TypeAttributeSets, error)
}

// Validate checks a new set.
func (s *AttributeSet) Validate() error {
	s.Key = strings.TrimSpace(s.Key)
	if s.Key == "" || utf8.RuneCountInString(s.Key) > 100 {
		return &api.FieldError{Field: "key", Message: "key is required (at most 100 characters)"}
	}
	if err := s.DisplayName.Validate(); err != nil {
		return &api.FieldError{Field: "display_name", Message: err.Error()}
	}
	s.DisplayName = s.DisplayName.WithFallback(s.Key)
	return nil
}

// apply changes s.
func (p *AttributeSetPatch) apply(s *AttributeSet) error {
	if p.DisplayName != nil {
		if err := p.DisplayName.Validate(); err != nil {
			return &api.FieldError{Field: "display_name", Message: err.Error()}
		}
		s.DisplayName = p.DisplayName.WithFallback(s.Key)
	}
	setString(&s.Description, p.Description)
	setBool(&s.IsActive, p.IsActive)
	return nil
}

// attributeSetFields maps database constraints onto request fields.
var attributeSetFields = map[string]string{
	"attribute_set_org_key":           "key",
	"attribute_set_key_check":         "key",
	"attribute_set_display_name_i18n": "display_name",
}

const attributeSetColumns = `id::text, COALESCE(organization_id::text, ''), key, display_name,
	COALESCE(description, ''), is_system, is_active, created_at, updated_at`

func scanAttributeSet(row pgx.Row) (*AttributeSet, error) {
	var (
		s           AttributeSet
		displayName []byte
	)
	if err := row.Scan(&s.ID, &s.OrganizationID, &s.Key, &displayName, &s.Description, &s.IsSystem, &s.IsActive,
		&s.CreatedAt, &s.UpdatedAt); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(displayName, &s.DisplayName); err != nil {
		return nil, fmt.Errorf("decode display_name: %w", err)
	}
	return &s, nil
}

// getAttributeSetTx reads a set the transaction sees (the organization's
// and the system sets); lock locks an own set for a change.
func getAttributeSetTx(ctx context.Context, tx pgx.Tx, id string, lock bool) (*AttributeSet, error) {
	if !api.IsUUID(id) {
		return nil, ErrAttributeSetNotFound
	}
	query := `SELECT ` + attributeSetColumns + ` FROM attribute_set WHERE id = $1`
	if lock {
		query += ` FOR UPDATE`
	}
	s, err := scanAttributeSet(tx.QueryRow(ctx, query, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAttributeSetNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read attribute set: %w", err)
	}
	return s, nil
}

// lockOwnAttributeSetTx locks a set of the organization for a change; a
// system set is visible but read-only (the UPDATE policy hides it from FOR
// UPDATE), so it is recognized before locking.
func lockOwnAttributeSetTx(ctx context.Context, tx pgx.Tx, id string) (*AttributeSet, error) {
	s, err := getAttributeSetTx(ctx, tx, id, false)
	if err != nil {
		return nil, err
	}
	if s.OrganizationID == "" {
		return nil, ErrAttributeSetReadOnly
	}
	return getAttributeSetTx(ctx, tx, id, true)
}

// ListAttributeSetPage implements AttributeSetStore: the organization's and
// the system sets newest first (API-02).
func (r *PGRepository) ListAttributeSetPage(ctx context.Context, orgID string, page api.PaginationParams) ([]AttributeSet, int, error) {
	out := []AttributeSet{}
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM attribute_set`).Scan(&total); err != nil {
			return fmt.Errorf("count attribute sets: %w", err)
		}
		keyset, tail, args, err := api.NewestFirstSQL(page, 1)
		if err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+attributeSetColumns+` FROM attribute_set WHERE `+keyset+` `+tail, args...)
		if err != nil {
			return fmt.Errorf("list attribute sets: %w", err)
		}
		out, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (AttributeSet, error) {
			s, serr := scanAttributeSet(row)
			if serr != nil {
				return AttributeSet{}, serr
			}
			return *s, nil
		})
		if err != nil {
			return fmt.Errorf("list attribute sets: %w", err)
		}
		return nil
	})
	return out, total, err
}

// GetAttributeSet implements AttributeSetStore.
func (r *PGRepository) GetAttributeSet(ctx context.Context, orgID, id string) (*AttributeSet, error) {
	var out *AttributeSet
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = getAttributeSetTx(ctx, tx, id, false)
		return err
	})
	return out, err
}

// CreateAttributeSet implements AttributeSetStore: a set of the
// organization; a taken key is 409.
func (r *PGRepository) CreateAttributeSet(ctx context.Context, orgID string, s *AttributeSet) error {
	if err := s.Validate(); err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		created, err := scanAttributeSet(tx.QueryRow(ctx, `INSERT INTO attribute_set
				(organization_id, key, display_name, description, is_system, is_active)
			VALUES ($1, $2, $3, $4, false, $5) RETURNING `+attributeSetColumns,
			orgID, s.Key, s.DisplayName, nilIfEmpty(s.Description), s.IsActive))
		if err != nil {
			return api.FieldDBError(err, attributeSetFields)
		}
		*s = *created
		return recordMetamodelAudit(ctx, tx, orgID, "attribute_set.created", "attribute_set", s.ID, nil, s)
	})
}

// UpdateAttributeSet implements AttributeSetStore; a system set is
// read-only (403). Deactivating a set keeps its assignments; it only
// cannot be assigned anew.
func (r *PGRepository) UpdateAttributeSet(ctx context.Context, orgID, id string, patch *AttributeSetPatch) (*AttributeSet, error) {
	var out *AttributeSet
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		s, err := lockOwnAttributeSetTx(ctx, tx, id)
		if err != nil {
			return err
		}
		before := *s
		if perr := patch.apply(s); perr != nil {
			return perr
		}
		updated, err := scanAttributeSet(tx.QueryRow(ctx, `UPDATE attribute_set
			SET display_name = $2, description = $3, is_active = $4 WHERE id = $1 RETURNING `+attributeSetColumns,
			id, s.DisplayName, nilIfEmpty(s.Description), s.IsActive))
		if err != nil {
			return api.FieldDBError(err, attributeSetFields)
		}
		if !before.IsActive && updated.IsActive {
			if cerr := checkReactivatedSetTx(ctx, tx, id); cerr != nil {
				return cerr
			}
		}
		out = updated
		return recordMetamodelAudit(ctx, tx, orgID, "attribute_set.updated", "attribute_set", id, &before, updated)
	})
	return out, reactivationDBError(err)
}

// checkReactivatedSetTx checks a set that becomes active again (WP-709):
// its attributes enter the composition of every type it is assigned to and
// of their subtypes, so each of these types must still compose with the
// system base, the organization's global attributes, its type chain and its
// other sets. A conflict is a 422 on is_active with the code of the
// conflict; nothing changes.
func checkReactivatedSetTx(ctx context.Context, tx pgx.Tx, id string) error {
	rows, err := tx.Query(ctx, `SELECT DISTINCT s.id::text FROM ci_type_attribute_set cs
		CROSS JOIN LATERAL ci_type_subtree(cs.ci_type_id) s WHERE cs.attribute_set_id = $1`, id)
	if err != nil {
		return fmt.Errorf("read types of attribute set: %w", err)
	}
	targets, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return fmt.Errorf("read types of attribute set: %w", err)
	}
	if len(targets) == 0 {
		return nil
	}
	if cerr := chainCompositionTx(ctx, tx, targets, "", nil); cerr != nil {
		var conflict *CompositionError
		if errors.As(cerr, &conflict) {
			return &api.FieldError{Field: "is_active", Message: cerr.Error(),
				Status: http.StatusUnprocessableEntity, Code: conflict.Code}
		}
		return cerr
	}
	return nil
}

// reactivationDBError maps the composition check of the materialization
// (attribute_definition_composition, raised by the deferred regeneration at
// the commit) onto the 422 on is_active, with the code the database reports
// in DETAIL; any other error stays as it is.
func reactivationDBError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.ConstraintName == compositionConstraint {
		return &api.FieldError{Field: "is_active", Message: pgErr.Message, Code: pgErr.Detail,
			Status: http.StatusUnprocessableEntity}
	}
	return err
}

// DeleteAttributeSet implements AttributeSetStore: a set assigned to a type
// is 409; otherwise its set attributes go first, each with its audit entry
// attribute_definition.deleted, and their indexes are released after the
// commit (DB-04). A system set is read-only (403).
func (r *PGRepository) DeleteAttributeSet(ctx context.Context, orgID, id string) error {
	var deleted []AttributeDefinition
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		s, err := lockOwnAttributeSetTx(ctx, tx, id)
		if err != nil {
			return err
		}
		var assigned bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM ci_type_attribute_set WHERE attribute_set_id = $1)`,
			id).Scan(&assigned); err != nil {
			return fmt.Errorf("read attribute set assignments: %w", err)
		}
		if assigned {
			return ErrAttributeSetInUse
		}
		defs, err := queryAttributes(ctx, tx, `SELECT `+attributeColumns+` FROM attribute_definition
			WHERE attribute_set_id = $1 ORDER BY sort_order, key FOR UPDATE`, []any{id})
		if err != nil {
			return err
		}
		for i := range defs {
			if _, err = tx.Exec(ctx, `DELETE FROM attribute_definition WHERE id = $1`, defs[i].ID); err != nil {
				return fmt.Errorf("delete set attribute: %w", err)
			}
			if aerr := recordAttributeAudit(ctx, tx, orgID, &defs[i], nil); aerr != nil {
				return aerr
			}
		}
		if _, err = tx.Exec(ctx, `DELETE FROM attribute_set WHERE id = $1`, id); err != nil {
			if isAssignmentReference(err) {
				return ErrAttributeSetInUse
			}
			return fmt.Errorf("delete attribute set: %w", err)
		}
		deleted = defs
		return recordMetamodelAudit(ctx, tx, orgID, "attribute_set.deleted", "attribute_set", id, s, nil)
	})
	if err == nil {
		for i := range deleted {
			r.scheduleIndex(ctx, orgID, &deleted[i], nil)
		}
	}
	return err
}

// assignmentConstraint is the reference of an assignment to its set: the
// foreign key, and the name ci_type_attribute_set_binding raises for a set
// of another organization.
const assignmentConstraint = "ci_type_attribute_set_attribute_set_id_fkey"

// isAssignmentReference reports a violation of assignmentConstraint.
func isAssignmentReference(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.ConstraintName == assignmentConstraint
}

// typeAttributeSetsTx reads the ordered set list of a type.
func typeAttributeSetsTx(ctx context.Context, tx pgx.Tx, typeID string) (*TypeAttributeSets, error) {
	rows, err := tx.Query(ctx, `SELECT attribute_set_id::text, position FROM ci_type_attribute_set
		WHERE ci_type_id = $1 ORDER BY position, attribute_set_id`, typeID)
	if err != nil {
		return nil, fmt.Errorf("read attribute sets of ci type: %w", err)
	}
	sets, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (AttributeSetAssignment, error) {
		var a AttributeSetAssignment
		return a, row.Scan(&a.AttributeSetID, &a.Position)
	})
	if err != nil {
		return nil, fmt.Errorf("read attribute sets of ci type: %w", err)
	}
	return &TypeAttributeSets{CITypeID: typeID, AttributeSets: sets}, nil
}

// GetTypeAttributeSets implements AttributeSetStore; a type the
// organization does not see is ErrCITypeNotFound.
func (r *PGRepository) GetTypeAttributeSets(ctx context.Context, orgID, typeID string) (*TypeAttributeSets, error) {
	var out *TypeAttributeSets
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if !api.IsUUID(typeID) {
			return ErrCITypeNotFound
		}
		_, found, err := ciTypeOrganizationTx(ctx, tx, typeID)
		switch {
		case err != nil:
			return err
		case !found:
			return ErrCITypeNotFound
		}
		out, err = typeAttributeSetsTx(ctx, tx, typeID)
		return err
	})
	return out, err
}

// SetTypeAttributeSets implements AttributeSetStore: checkSetAssignmentTx
// checks type and sets, then the list replaces the former one as a whole
// and the change is audited as ci_type.attribute_sets_changed with the
// ordered list before and after. Until WP-260 the change applies at once
// (MET-04, MET-53).
func (r *PGRepository) SetTypeAttributeSets(ctx context.Context, orgID, typeID string, sets []AttributeSetAssignment) (*TypeAttributeSets, error) {
	var out *TypeAttributeSets
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// Concurrent replacements of one type's list run one after the other.
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('ci_type_attribute_set:' || $1, 0))`,
			typeID); err != nil {
			return fmt.Errorf("lock attribute sets of ci type: %w", err)
		}
		before, err := checkSetAssignmentTx(ctx, tx, typeID, sets)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `DELETE FROM ci_type_attribute_set WHERE ci_type_id = $1`, typeID); err != nil {
			return fmt.Errorf("replace attribute sets of ci type: %w", err)
		}
		for _, a := range sets {
			if _, err = tx.Exec(ctx, `INSERT INTO ci_type_attribute_set (ci_type_id, attribute_set_id, position)
				VALUES ($1, $2, $3)`, typeID, a.AttributeSetID, a.Position); err != nil {
				if isAssignmentReference(err) {
					return errSetNotAssignable("attribute set not found")
				}
				return fmt.Errorf("assign attribute set: %w", err)
			}
		}
		if out, err = typeAttributeSetsTx(ctx, tx, typeID); err != nil {
			return err
		}
		return recordMetamodelAudit(ctx, tx, orgID, "ci_type.attribute_sets_changed", "ci_type", typeID, before, out)
	})
	return out, err
}
