// Package labels resolves scanned codes to the objects they name (INV-03,
// WP-762): GET /api/v1/labels/lookup?code= answers with kind, id and name
// of an asset, a consumable or a location.
//
// A code is either the URN urn:reticora:<asset|consumable|location>:<uuid>
// that the labels carry for objects without an inventory number (E-52), or
// a code of barcode_registry (WP-289: the barcode of an asset, consumable
// or location, or the inventory number of an asset). The URN is tried
// first. The object row is read again under the caller's tenant scope
// (row level security on client and site), and the caller needs the read
// permission and the feature of the object's kind. Every refusal answers
// 404 like an unknown code, so the lookup does not reveal whether a code
// exists outside the caller's reach (TEN-04).
//
// The package does not import the asset, consumable or locations
// packages: the PostgreSQL resolver reads the tables itself, the memory
// resolver reads the memory repositories through ObjectSource adapters
// (server/repositories.go).
package labels

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Object kinds a code can name.
const (
	KindAsset      = "asset"
	KindConsumable = "consumable"
	KindLocation   = "location"
)

// urnPrefix starts the URN of an object label (E-52).
const urnPrefix = "urn:reticora:"

// ErrNotFound: no object of the caller's reach carries the code.
var ErrNotFound = errors.New("no object found for the code")

// Ref names one object.
type Ref struct {
	Kind string
	ID   string
}

// Object is the answer of the lookup. LocationID is set for assets only:
// the asset's node in the location tree.
type Object struct {
	Kind       string `json:"kind"`
	ID         string `json:"id"`
	Name       string `json:"name"`
	LocationID string `json:"location_id,omitempty"`
}

// Repository resolves codes. Both methods read under the tenant scope of
// the request and return ErrNotFound for a code or object the caller
// cannot see.
type Repository interface {
	// Resolve returns the object a registered code names (barcode or
	// inventory number, barcode_registry).
	Resolve(ctx context.Context, orgID, code string) (Ref, error)
	// Get reads the object.
	Get(ctx context.Context, orgID string, ref Ref) (*Object, error)
}

// kindRule is what the caller needs for an object kind: the read
// permission (stock:read for consumables after WP-245) and the feature;
// locations are core and need no feature.
type kindRule struct {
	permission identity.Permission
	feature    string
}

var kindRules = map[string]kindRule{
	KindAsset:      {permission: identity.PermAssetRead, feature: entitlement.FeatureAssetsBasic},
	KindConsumable: {permission: identity.PermStockRead, feature: entitlement.FeatureStock},
	KindLocation:   {permission: identity.PermLocationRead},
}

// ParseURN reads an object URN. isURN reports whether code has the URN
// prefix; ok whether the URN names a known kind and a UUID.
func ParseURN(code string) (ref Ref, isURN, ok bool) {
	if len(code) < len(urnPrefix) || !strings.EqualFold(code[:len(urnPrefix)], urnPrefix) {
		return Ref{}, false, false
	}
	kind, id, found := strings.Cut(code[len(urnPrefix):], ":")
	kind = strings.ToLower(kind)
	if _, known := kindRules[kind]; !found || !known || !api.IsUUID(id) {
		return Ref{}, true, false
	}
	return Ref{Kind: kind, ID: strings.ToLower(id)}, true, true
}

// Handler serves GET /api/v1/labels/lookup.
type Handler struct {
	repo     Repository
	features entitlement.Checker
}

// NewHandler creates the lookup handler; a nil repository answers 503.
func NewHandler(repo Repository) *Handler {
	return &Handler{repo: repo}
}

// WithFeatures checks the feature of the object's kind (assets_basic for
// assets, stock for consumables, E-44); without it (nil) every
// organization is entitled. The gate barcode of the whole path is the
// entitlement middleware's.
func (h *Handler) WithFeatures(features entitlement.Checker) *Handler {
	h.features = features
	return h
}

// RegisterRoutes registers the lookup route.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/labels/lookup", h.Lookup)
}

// Lookup resolves the code of the query to the object it names.
func (h *Handler) Lookup(w http.ResponseWriter, r *http.Request) {
	orgID := tenant.FromContext(r.Context()).OrganizationID
	if orgID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	code := strings.TrimSpace(r.URL.Query().Get("code"))
	if code == "" {
		api.WriteFieldError(w, &api.FieldError{Field: "code", Message: "is required", Status: http.StatusUnprocessableEntity})
		return
	}
	if h.repo == nil {
		api.WriteUnavailable(w, "the label lookup is not available")
		return
	}
	obj, err := h.lookup(r.Context(), orgID, code)
	switch {
	case errors.Is(err, ErrNotFound):
		api.WriteError(w, http.StatusNotFound, "Not Found", "no object found for the code")
	case err != nil:
		api.WriteRepoError(w, err)
	default:
		api.WriteJSON(w, http.StatusOK, obj)
	}
}

// lookup resolves the code: the URN first, then the register. It returns
// ErrNotFound for a malformed URN, an unknown code, an object of a kind
// the caller may not read or whose feature the organization lacks, and an
// object outside the caller's scope.
func (h *Handler) lookup(ctx context.Context, orgID, code string) (*Object, error) {
	ref, isURN, ok := ParseURN(code)
	switch {
	case isURN && !ok:
		return nil, ErrNotFound
	case !isURN:
		// PostgreSQL text holds no NUL, so no code contains one.
		if strings.ContainsRune(code, 0) {
			return nil, ErrNotFound
		}
		var err error
		if ref, err = h.repo.Resolve(ctx, orgID, code); err != nil {
			return nil, err
		}
	}
	if !h.allowed(ctx, orgID, ref.Kind) {
		return nil, ErrNotFound
	}
	return h.repo.Get(ctx, orgID, ref)
}

// allowed reports whether the caller holds the read permission of the
// kind and the organization its feature.
func (h *Handler) allowed(ctx context.Context, orgID, kind string) bool {
	rule, known := kindRules[kind]
	if !known {
		return false
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok || !principal.Has(rule.permission) {
		return false
	}
	return rule.feature == "" || h.features == nil || h.features.IsEnabled(ctx, orgID, rule.feature)
}

// PGRepository resolves codes in PostgreSQL under the request's tenant
// scope (database.WithRequestTenant): barcode_registry and the object
// tables are read with their row level security on client and site.
type PGRepository struct {
	pool *pgxpool.Pool
}

// NewPGRepository creates the PostgreSQL resolver.
func NewPGRepository(pool *pgxpool.Pool) *PGRepository {
	return &PGRepository{pool: pool}
}

var _ Repository = (*PGRepository)(nil)

// Resolve reads the register row of the code; the UNIQUE (organization_id,
// code) of WP-289 makes it at most one.
func (r *PGRepository) Resolve(ctx context.Context, orgID, code string) (Ref, error) {
	var ref Ref
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var assetID, consumableID, locationID *string
		if err := tx.QueryRow(ctx, `
			SELECT asset_id::text, consumable_id::text, location_id::text
			  FROM barcode_registry WHERE organization_id = $1 AND code = $2`, orgID, code).
			Scan(&assetID, &consumableID, &locationID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("resolve label code: %w", err)
		}
		switch {
		case assetID != nil:
			ref = Ref{Kind: KindAsset, ID: *assetID}
		case consumableID != nil:
			ref = Ref{Kind: KindConsumable, ID: *consumableID}
		case locationID != nil:
			ref = Ref{Kind: KindLocation, ID: *locationID}
		default:
			return ErrNotFound
		}
		return nil
	})
	return ref, err
}

// objectQueries read kind, name and (assets) location of one object.
var objectQueries = map[string]string{
	KindAsset:      `SELECT id::text, name, COALESCE(location_id::text, '') FROM asset WHERE organization_id = $1 AND id = $2`,
	KindConsumable: `SELECT id::text, name, '' FROM consumable WHERE organization_id = $1 AND id = $2`,
	KindLocation:   `SELECT id::text, name, '' FROM location WHERE organization_id = $1 AND id = $2`,
}

// Get reads the object row.
func (r *PGRepository) Get(ctx context.Context, orgID string, ref Ref) (*Object, error) {
	query, known := objectQueries[ref.Kind]
	if !known || !api.IsUUID(ref.ID) {
		return nil, ErrNotFound
	}
	obj := &Object{Kind: ref.Kind}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, query, orgID, ref.ID).Scan(&obj.ID, &obj.Name, &obj.LocationID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNotFound
			}
			return fmt.Errorf("read labelled %s: %w", ref.Kind, err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return obj, nil
}

// Entry is one object of a memory source with the codes it carries (its
// barcode, for an asset also its inventory number).
type Entry struct {
	Object
	Codes []string
}

// ObjectSource lists the objects of one kind of an organization for the
// memory resolver; the adapters over the memory repositories of asset,
// consumable and locations live in server/repositories.go.
type ObjectSource interface {
	Entries(ctx context.Context, orgID string) ([]Entry, error)
}

// MemoryRepository resolves codes over the memory repositories (API-01:
// the same contract as PostgreSQL). The memory profile knows no scope
// below the organization.
type MemoryRepository struct {
	sources map[string]ObjectSource
}

// NewMemoryRepository creates the memory resolver over the sources of the
// three kinds; a nil source has no objects.
func NewMemoryRepository(assets, consumables, locations ObjectSource) *MemoryRepository {
	return &MemoryRepository{sources: map[string]ObjectSource{
		KindAsset: assets, KindConsumable: consumables, KindLocation: locations,
	}}
}

var _ Repository = (*MemoryRepository)(nil)

// memoryKinds is the order the memory resolver searches the sources in.
var memoryKinds = []string{KindAsset, KindConsumable, KindLocation}

func (m *MemoryRepository) entries(ctx context.Context, orgID, kind string) ([]Entry, error) {
	source := m.sources[kind]
	if source == nil {
		return nil, nil
	}
	entries, err := source.Entries(ctx, orgID)
	if err != nil {
		return nil, fmt.Errorf("list %s codes: %w", kind, err)
	}
	return entries, nil
}

// Resolve finds the object carrying the code; codes compare exactly like
// the UNIQUE (organization_id, code) of the register.
func (m *MemoryRepository) Resolve(ctx context.Context, orgID, code string) (Ref, error) {
	for _, kind := range memoryKinds {
		entries, err := m.entries(ctx, orgID, kind)
		if err != nil {
			return Ref{}, err
		}
		for i := range entries {
			for _, c := range entries[i].Codes {
				if c != "" && c == code {
					return Ref{Kind: kind, ID: entries[i].ID}, nil
				}
			}
		}
	}
	return Ref{}, ErrNotFound
}

// Get reads the object of the reference.
func (m *MemoryRepository) Get(ctx context.Context, orgID string, ref Ref) (*Object, error) {
	if _, known := kindRules[ref.Kind]; !known {
		return nil, ErrNotFound
	}
	entries, err := m.entries(ctx, orgID, ref.Kind)
	if err != nil {
		return nil, err
	}
	for i := range entries {
		if strings.EqualFold(entries[i].ID, ref.ID) {
			obj := entries[i].Object
			obj.Kind = ref.Kind
			if ref.Kind != KindAsset {
				obj.LocationID = ""
			}
			return &obj, nil
		}
	}
	return nil, ErrNotFound
}
