package lifecycle

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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

// CurrentState returns the lifecycle_state column of the entity.
func (s *PGStateStore) CurrentState(ctx context.Context, orgID, entityType, entityID string) (string, error) {
	table, err := s.table(entityType)
	if err != nil {
		return "", err
	}
	var state string
	err = database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if scanErr := tx.QueryRow(ctx, fmt.Sprintf(
			"SELECT COALESCE(lifecycle_state, '') FROM %s WHERE id = $1", table), entityID).Scan(&state); scanErr != nil {
			return fmt.Errorf("read lifecycle state: %w", scanErr)
		}
		return nil
	})
	return state, err
}

// SetState updates the lifecycle_state column of the entity.
func (s *PGStateStore) SetState(ctx context.Context, orgID, entityType, entityID, state string) error {
	table, err := s.table(entityType)
	if err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, s.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		cmd, err := tx.Exec(ctx, fmt.Sprintf(
			"UPDATE %s SET lifecycle_state = $2 WHERE id = $1", table), entityID, state)
		if err != nil {
			return fmt.Errorf("set lifecycle state: %w", err)
		}
		if cmd.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return nil
	})
}

// Resolver implements EntityResolver by reading the entity's type lifecycle
// (asset: asset_type → ci_type.lifecycle_definition_id, falling back to the
// default physical_asset lifecycle for inventory-capable assets; ci: the CI
// type's lifecycle definition).
type Resolver struct {
	pool *pgxpool.Pool
}

// NewResolver creates a PostgreSQL-backed entity resolver.
func NewResolver(pool *pgxpool.Pool) *Resolver {
	return &Resolver{pool: pool}
}

// LifecycleKeyFor resolves the lifecycle definition key for an entity.
func (r *Resolver) LifecycleKeyFor(req *http.Request, orgID, entityType, entityID string) (string, error) {
	var key string
	err := database.WithRequestTenant(req.Context(), r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var definitionID string
		var err error
		switch entityType {
		case "asset":
			err = tx.QueryRow(ctx, `
				SELECT COALESCE(
					(SELECT ct.lifecycle_definition_id::text
					 FROM asset a JOIN ci_type ct ON ct.id = a.asset_type_id
					 WHERE a.id = $1),
					(SELECT ct.lifecycle_definition_id::text
					 FROM asset a JOIN ci c ON c.id = a.ci_id JOIN ci_type ct ON ct.id = c.ci_type_id
					 WHERE a.id = $1),
					(SELECT ld.id::text FROM lifecycle_definition ld
					 WHERE ld.key = 'physical_asset' AND ld.is_system)
				)`, entityID).Scan(&definitionID)
		case "ci":
			err = tx.QueryRow(ctx, `
				SELECT COALESCE(ct.lifecycle_definition_id::text, '')
				FROM ci c JOIN ci_type ct ON ct.id = c.ci_type_id
				WHERE c.id = $1`, entityID).Scan(&definitionID)
		default:
			return fmt.Errorf("unsupported entity type %q", entityType)
		}
		if err != nil || definitionID == "" {
			return fmt.Errorf("not found")
		}
		if err := tx.QueryRow(ctx,
			"SELECT key FROM lifecycle_definition WHERE id = $1", definitionID).Scan(&key); err != nil {
			return fmt.Errorf("lifecycle definition not found")
		}
		return nil
	})
	return key, err
}

// MemoryStateStore implements StateStore and EntityResolver in memory for
// tests and the --no-db development mode.
type MemoryStateStore struct {
	mu     sync.RWMutex
	states map[string]string // entityType/entityID -> state
	keys   map[string]string // entityType/entityID -> definition key
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
		return fmt.Errorf("not found")
	}
	s.states[entityType+"/"+entityID] = state
	return nil
}

// LifecycleKeyFor implements EntityResolver.
func (s *MemoryStateStore) LifecycleKeyFor(_ *http.Request, orgID, entityType, entityID string) (string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	key, ok := s.keys[entityType+"/"+entityID]
	if !ok {
		return "", fmt.Errorf("not found")
	}
	return key, nil
}
