package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PGStateStore implements StateStore against the ci/asset tables.
type PGStateStore struct {
	pool *pgxpool.Pool
}

// NewPGStateStore creates a PostgreSQL-backed lifecycle state store.
func NewPGStateStore(pool *pgxpool.Pool) *PGStateStore {
	return &PGStateStore{pool: pool}
}

func (s *PGStateStore) table(entityType string) (string, error) {
	switch entityType {
	case "asset":
		return "asset", nil
	case "ci":
		return "ci", nil
	}
	return "", fmt.Errorf("unsupported entity type %q", entityType)
}

// CurrentState returns the lifecycle_state column of the entity. In a
// transaction (RunInTx) it locks the row (SELECT … FOR UPDATE), so parallel
// transitions of the entity are serialized (WP-724). An entity outside the
// scope is "not found".
func (s *PGStateStore) CurrentState(ctx context.Context, orgID, entityType, entityID string) (string, error) {
	table, err := s.table(entityType)
	if err != nil {
		return "", err
	}
	lock := ""
	if database.InTx(ctx) {
		lock = " FOR UPDATE"
	}
	var state string
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		scanErr := tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT COALESCE(lifecycle_state, '') FROM %s WHERE id = $1%s", table, lock), entityID).Scan(&state)
		if errors.Is(scanErr, pgx.ErrNoRows) {
			return errEntityNotFound
		}
		if scanErr != nil {
			return fmt.Errorf("read lifecycle state: %w", scanErr)
		}
		return nil
	})
	return state, err
}

// SetState updates the lifecycle_state column of the entity. A CI also
// gets version + 1 and updated_at now(), so its ETag changes (API-07); an
// asset has no version column until WP-271.
func (s *PGStateStore) SetState(ctx context.Context, orgID, entityType, entityID, state string) error {
	table, err := s.table(entityType)
	if err != nil {
		return err
	}
	set := "lifecycle_state = $2"
	if table == "ci" {
		set += ", version = version + 1, updated_at = now()"
	}
	return database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, execErr := tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s WHERE id = $1", table, set), entityID, state)
		if execErr != nil {
			return fmt.Errorf("set lifecycle state: %w", execErr)
		}
		if cmd.RowsAffected() == 0 {
			return errEntityNotFound
		}
		return nil
	})
}

// RunInTx runs fn in one transaction of the request's tenant scope
// (database.WithRequestTenantTx); a transaction in ctx is joined (NFR-03,
// WP-332), so a caller such as the disposal path (WP-278) commits or rolls
// back the transition with its own writes.
func (s *PGStateStore) RunInTx(ctx context.Context, orgID string, fn func(ctx context.Context) error) error {
	return database.WithRequestTenantTx(ctx, s.pool, orgID, fn)
}

// RecordTransition writes the audit entry (ci.lifecycle_changed or
// asset.lifecycle_changed) and the event lifecycle.changed of a transition
// in the transaction of ctx. Both take an organization-wide lock until the
// commit (audit chain, outbox), so they run last, right before the commit
// (database.BeforeCommit); their failure rolls the transition back.
func (s *PGStateStore) RecordTransition(ctx context.Context, orgID string, rec *TransitionRecord) error {
	table, err := s.table(rec.EntityType)
	if err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var clientID, siteID string
		if scanErr := tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT COALESCE(client_id::text, ''), COALESCE(site_id::text, '') FROM %s WHERE id = $1", table),
			rec.EntityID).Scan(&clientID, &siteID); scanErr != nil {
			if errors.Is(scanErr, pgx.ErrNoRows) {
				return errEntityNotFound
			}
			return fmt.Errorf("read lifecycle entity: %w", scanErr)
		}
		var from any
		if rec.From != "" {
			from = rec.From
		}
		actor := strings.TrimSpace(tenant.FromContext(ctx).UserID)
		actorType := "system"
		if actor != "" {
			actorType = "user"
		}
		entry := audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
			Action: rec.EntityType + ".lifecycle_changed", ResourceType: rec.EntityType, ResourceID: rec.EntityID,
			Changes: map[string]any{"lifecycle_state": []any{from, rec.To}, "reason": rec.Reason}}
		event := &events.Event{OrgID: orgID, Type: events.LifecycleChanged, EntityID: rec.EntityID,
			Data: events.LifecycleChangedData{EntityType: rec.EntityType, EntityID: rec.EntityID, ClientID: clientID,
				SiteID: siteID, From: rec.From, To: rec.To, ModelID: rec.ModelID, ModelKey: rec.ModelKey, Reason: rec.Reason}}
		return database.BeforeCommit(ctx, func() error {
			if _, recErr := audit.NewPGRecorder().Record(ctx, tx, entry); recErr != nil {
				return fmt.Errorf("audit lifecycle transition: %w", recErr)
			}
			if _, appendErr := events.NewOutbox().Append(ctx, tx, event); appendErr != nil {
				return fmt.Errorf("event of the lifecycle transition: %w", appendErr)
			}
			return nil
		})
	})
}

// Resolver implements EntityResolver with the effective lifecycle model of
// the entity's type chain (lifecycle_model_effective, MET-06, LCY-02): a CI
// has the nearest model of its ci_type chain that applies to ci, none
// otherwise (ErrNoLifecycleModel, DB-05); an asset has the nearest model of
// the chain of its asset_type_id that applies to asset, otherwise the
// system model physical_asset. A linked CI (asset.ci_id) does not count.
type Resolver struct {
	pool *pgxpool.Pool
}

// NewResolver creates a PostgreSQL-backed entity resolver.
func NewResolver(pool *pgxpool.Pool) *Resolver {
	return &Resolver{pool: pool}
}

// physicalAssetModelID is the system model physical_asset (000055).
const physicalAssetModelID = "00000000-0000-0000-0000-000000000101"

// LifecycleModelFor resolves the id of the effective lifecycle model of an
// entity: an entity outside the caller's scope is "not found", a CI whose
// type has no effective ci model ErrNoLifecycleModel.
func (r *Resolver) LifecycleModelFor(req *http.Request, orgID, entityType, entityID string) (string, error) {
	var definitionID string
	err := database.WithRequestTenant(req.Context(), r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		definitionID, err = effectiveModelTx(ctx, tx, entityType, entityID)
		return err
	})
	return definitionID, err
}

// LifecycleKeyFor resolves the key of the effective lifecycle model of an
// entity. The transition loads the model by its id (LifecycleModelFor,
// WP-724); the key is only a reading aid.
func (r *Resolver) LifecycleKeyFor(req *http.Request, orgID, entityType, entityID string) (string, error) {
	var key string
	err := database.WithRequestTenant(req.Context(), r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		definitionID, err := effectiveModelTx(ctx, tx, entityType, entityID)
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx,
			"SELECT key FROM lifecycle_definition WHERE id = $1", definitionID).Scan(&key); err != nil {
			return fmt.Errorf("lifecycle definition not found")
		}
		return nil
	})
	return key, err
}

// effectiveModelTx returns the id of the effective lifecycle model of the
// entity (lifecycle_model_effective; an asset falls back to
// physical_asset).
func effectiveModelTx(ctx context.Context, tx pgx.Tx, entityType, entityID string) (string, error) {
	var definitionID string
	var err error
	switch entityType {
	case "asset":
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(lifecycle_model_effective(a.asset_type_id, 'asset'), $2::uuid)::text
			FROM asset a WHERE a.id = $1`, entityID, physicalAssetModelID).Scan(&definitionID)
	case "ci":
		err = tx.QueryRow(ctx, `
			SELECT COALESCE(lifecycle_model_effective(c.ci_type_id, 'ci')::text, '')
			FROM ci c WHERE c.id = $1`, entityID).Scan(&definitionID)
		if err == nil && definitionID == "" {
			// Only CI types with an effective ci lifecycle model have
			// a lifecycle state (DB-05).
			return "", ErrNoLifecycleModel
		}
	default:
		return "", fmt.Errorf("unsupported entity type %q", entityType)
	}
	if err != nil || definitionID == "" {
		return "", fmt.Errorf("not found")
	}
	return definitionID, nil
}

// MemoryStateStore implements StateStore and EntityResolver in memory for
// tests and the --no-db development mode.
type MemoryStateStore struct {
	// tx serializes the transitions (RunInTx); there is no rollback.
	tx     sync.Mutex
	mu     sync.RWMutex
	states map[string]string // entityType/entityID -> state
	keys   map[string]string // entityType/entityID -> definition key
	// defs resolves a definition key to the model id (LifecycleModelFor);
	// NewHandler links the memory repository it serves.
	defs *MemoryRepository
}

// NewMemoryStateStore creates an empty in-memory state store.
func NewMemoryStateStore() *MemoryStateStore {
	return &MemoryStateStore{states: map[string]string{}, keys: map[string]string{}}
}

// SeedEntity registers an entity with a lifecycle definition key (tests,
// --no-db defaults).
func (s *MemoryStateStore) SeedEntity(entityType, entityID, definitionKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys[entityType+"/"+entityID] = definitionKey
}

// CurrentState returns the in-memory lifecycle state of the entity.
func (s *MemoryStateStore) CurrentState(_ context.Context, orgID, entityType, entityID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.states[entityType+"/"+entityID], nil
}

// SetState updates the in-memory lifecycle state of the entity.
func (s *MemoryStateStore) SetState(_ context.Context, orgID, entityType, entityID, state string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.keys[entityType+"/"+entityID]; !ok {
		return errEntityNotFound
	}
	s.states[entityType+"/"+entityID] = state
	return nil
}

// RunInTx runs fn under the store's lock: transitions in memory are
// serialized, but not rolled back (--no-db).
func (s *MemoryStateStore) RunInTx(ctx context.Context, _ string, fn func(ctx context.Context) error) error {
	s.tx.Lock()
	defer s.tx.Unlock()
	return fn(ctx)
}

// RecordTransition implements StateStore: in memory the route audit
// writes the entry and there is no outbox (--no-db).
func (s *MemoryStateStore) RecordTransition(context.Context, string, *TransitionRecord) error {
	return nil
}

// LifecycleKeyFor implements EntityResolver.
func (s *MemoryStateStore) LifecycleKeyFor(_ *http.Request, orgID, entityType, entityID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.keys[entityType+"/"+entityID]
	if !ok {
		return "", errEntityNotFound
	}
	return key, nil
}

// LifecycleModelFor implements EntityResolver: the id of the model whose
// key SeedEntity registered, looked up in the linked memory repository.
func (s *MemoryStateStore) LifecycleModelFor(r *http.Request, orgID, entityType, entityID string) (string, error) {
	key, err := s.LifecycleKeyFor(r, orgID, entityType, entityID)
	if err != nil {
		return "", err
	}
	s.mu.RLock()
	defs := s.defs
	s.mu.RUnlock()
	if defs == nil {
		return "", ErrNoLifecycleModel
	}
	id, ok := defs.lookupKey(orgID, key)
	if !ok {
		return "", ErrNoLifecycleModel
	}
	return id, nil
}

// linkDefinitions makes the store resolve model ids from repo.
func (s *MemoryStateStore) linkDefinitions(repo *MemoryRepository) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.defs == nil {
		s.defs = repo
	}
}
