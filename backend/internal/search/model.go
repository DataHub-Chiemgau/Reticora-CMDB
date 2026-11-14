// Package search provides tenant-scoped full-text search backends.
package search

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

const MaxQueryLength = 512

type Document struct {
	ID             string            `json:"id"`
	OrganizationID string            `json:"organization_id"`
	EntityType     string            `json:"entity_type"`
	EntityID       string            `json:"entity_id"`
	Title          string            `json:"title"`
	Summary        string            `json:"summary,omitempty"`
	URL            string            `json:"url"`
	Metadata       map[string]string `json:"metadata,omitempty"`
	// ClientID and SiteID are the scope of the entity (empty: org-wide). The
	// PostgreSQL index derives them itself; remote indexes store what the
	// writer passes, and every query filters on them (CH11, CH25).
	ClientID  string    `json:"client_id,omitempty"`
	SiteID    string    `json:"site_id,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	// Fields are the summary columns by field key (SummaryFields, RBA-07,
	// WP-266), the attributes (attributes.<key>) and the identifiers of
	// identifierKinds (WP-308). The PostgreSQL index stores the columns and
	// the searchable attributes as field rows, the identifiers in
	// search_identifier; they are never serialized, so neither a hit nor a
	// remote index carries them.
	Fields map[string]string `json:"-"`
}

type Query struct {
	OrganizationID string
	UserID         string
	Text           string
	EntityTypes    []string
	Limit          int
	Offset         int
	Highlight      bool
	// Policy is the field policy of the principal (RBA-07, WP-266): with a
	// restriction of ci, asset, document or ticket, a hit of that type
	// matches, ranks and shows only its title and the summary fields
	// readable for its type and scope. The REST handler and the GraphQL
	// resolver always set it. nil is reserved for internal callers (the AI
	// retriever) and answers without field permissions.
	Policy *permission.FieldPolicy
	// NetworkReadable lets the identifiers network.ip and network.mac (the
	// addresses and MACs of the interfaces of a CI) count for a hit: the
	// principal holds ipam:read, the permission of GET
	// /api/v1/cis/{id}/interfaces (SRC-02, WP-308). ApplyPrincipal sets it.
	NetworkReadable bool
	// UsersReadable lets the user fields (userFields) count for a hit: the
	// principal holds user:read, the permission of GET /api/v1/users
	// (SRC-02, WP-788). The search text is resolved to the active users
	// whose name or e-mail address it matches; the index holds their ids
	// only. ApplyPrincipal sets it.
	UsersReadable bool
	// HoldersSearchable lets the holder fields (core.assigned_user_id,
	// asset.assigned_user_id) count: the organization has assets_basic
	// (WP-788). ApplyPrincipal sets it.
	HoldersSearchable bool
	// userIDs are the users the text resolved to (PGRepository.ResolveUsers),
	// set by the backend for one query.
	userIDs []string
}

// ApplyPrincipal sets the field policy of the request context, the network
// and user permissions of the principal and the entitlement of the holder
// fields on the query (RBA-07, SRC-02); has reports whether the principal
// holds a permission, enabled whether the organization has a feature. The
// REST handler and the GraphQL resolver share it. A policy that cannot be
// resolved is an error (fail-closed); without has or enabled the query
// gets none of them.
func ApplyPrincipal(ctx context.Context, q *Query, has, enabled func(key string) bool) error {
	policy, err := permission.PolicyFrom(ctx)
	if err != nil {
		return err
	}
	q.Policy = policy
	q.NetworkReadable = has != nil && has(string(identity.PermIPAMRead))
	q.UsersReadable = has != nil && has(string(identity.PermUserRead))
	q.HoldersSearchable = enabled != nil && enabled(FeatureAssetsBasic)
	return nil
}

// Feature keys and modules of the gated hit types (WP-244, WP-214). The
// search package names them itself: it imports neither entitlement nor
// server.
const (
	FeatureAssetsBasic        = "assets_basic"
	FeatureDocuments          = "documents"
	FeatureTicketingEssential = "ticketing_essential"
	ModuleInventory           = "inventory"
	ModuleDocuments           = "documents"
	ModuleTicketing           = "ticketing"
)

// hitGates are the feature and the module a hit of the entity type needs
// (SRC-02, WP-788). Types missing here are core (E-105).
var hitGates = map[string]struct{ feature, module string }{
	"asset":       {FeatureAssetsBasic, ModuleInventory},
	"reservation": {FeatureAssetsBasic, ModuleInventory},
	"document":    {FeatureDocuments, ModuleDocuments},
	"ticket":      {FeatureTicketingEssential, ModuleTicketing},
}

// HitAllowed reports whether a hit of the entity type may be shown: a type
// of hitGates needs its feature (enabled; nil never) and, when module is
// set, its module (REST: ModuleSet.Enabled); GraphQL checks no module
// (WP-311). Fail-closed: without a check the gated hits are dropped.
func HitAllowed(entityType string, enabled, module func(key string) bool) bool {
	gate, gated := hitGates[entityType]
	if !gated {
		return true
	}
	if enabled == nil || !enabled(gate.feature) {
		return false
	}
	return module == nil || module(gate.module)
}

// SummaryFields are the field keys of the summary columns per entity type,
// in the order of the summary (WP-266). The title (name or title) is a
// structural field and no field row. Other entity types have no field
// permissions: their summary is their only hit basis.
var SummaryFields = map[string][]string{
	"ci": {"core.hostname", "core.manufacturer", "core.model", "core.serial_number", "core.os_name", "core.os_version"},
	"asset": {"core.asset_tag", "core.category", "core.status", "core.supplier", "core.serial_number", "core.location",
		"core.notes"},
	"document": {"core.description", "core.file_name", "core.category", "core.tags"},
	"ticket":   {"core.description", "core.status", "core.priority", "core.category", "core.tags"},
}

// fieldTypes are the entity types with field rows, in a fixed order.
var fieldTypes = []string{"ci", "asset", "document", "ticket"}

// restrictedTypes returns the queried entity types with field rows whose
// fields the policy restricts; none without a policy.
func (q *Query) restrictedTypes() []string {
	if q.Policy == nil {
		return nil
	}
	var out []string
	for _, t := range fieldTypes {
		if len(q.EntityTypes) > 0 && !slices.Contains(q.EntityTypes, t) {
			continue
		}
		if q.Policy.Restricted(permission.ObjectKind(t)) {
			out = append(out, t)
		}
	}
	return out
}

// readableFields returns the summary fields of the entity type that the
// policy lets the principal read for an object of the type and scope, in
// summary order.
func readableFields(policy *permission.FieldPolicy, entityType, typeID string, ref *permission.ObjectRef) []string {
	var out []string
	for _, key := range SummaryFields[entityType] {
		if policy.Level(permission.ObjectKind(entityType), typeID, key, *ref).Readable() {
			out = append(out, key)
		}
	}
	return out
}

// identifierPermission returns the object kind and the field key of the
// field permission of an identifier field (WP-308): the typed column of
// the CI, the inventory number under the key of the asset policy
// (permission.FieldKeyOf), also at the CI document.
func identifierPermission(field string) (kind permission.ObjectKind, key string) {
	column := ""
	kind = permission.KindCI
	switch field {
	case IdentifierManagementIP:
		column = "management_ip"
	case IdentifierPrimaryMAC:
		column = "primary_mac"
	case IdentifierInventory, IdentifierAssetInventory:
		kind, column = permission.KindAsset, "inventory_no"
	}
	key, _, _ = permission.FieldKeyOf(kind, column)
	return kind, key
}

// readableIdentifiers returns the identifier fields of the entity type the
// principal may read for an object of the type and scope: network.* with
// Query.NetworkReadable, the inventory number of the linked asset at a CI
// under the asset policy for that asset (assetRef: hitAssetRef), the
// others under the policy of the type. A key without mapping is not
// readable (fail-closed).
func readableIdentifiers(q *Query, entityType, typeID string, ref *permission.ObjectRef, assetRef string) []string {
	var out []string
	for _, field := range identifierFields[entityType] {
		if strings.HasPrefix(field, "network.") {
			if q.NetworkReadable {
				out = append(out, field)
			}
			continue
		}
		kind, key := identifierPermission(field)
		if key == "" {
			continue
		}
		readable := false
		switch {
		case field == IdentifierAssetInventory && !q.Policy.Restricted(permission.KindAsset):
			readable = true
		case field == IdentifierAssetInventory:
			parts := strings.Split(assetRef, "|")
			if len(parts) == 4 {
				asset := permission.ObjectRef{ID: parts[0], ClientID: parts[2], SiteID: parts[3]}
				readable = q.Policy.Level(kind, parts[1], key, asset).Readable()
			}
		default:
			readable = q.Policy.Level(kind, typeID, key, *ref).Readable()
		}
		if readable {
			out = append(out, field)
		}
	}
	return out
}

// User fields of search_identifier (SRC-02, WP-788): the surrogate
// (app_user.id) of the owner of a ci or asset, of the holder of an asset
// and, at a CI, of the holder of its asset (ci.asset_id, source_id =
// asset). Name and e-mail are never indexed (SEC-05); the text is resolved
// to ids at query time.
const (
	UserFieldOwner       = "core.owner_user_id"
	UserFieldHolder      = "core.assigned_user_id"
	UserFieldAssetHolder = "asset.assigned_user_id"
	maxResolvedUsers     = 50
	tagField             = "core.tags"
	tagPath              = "tags"
)

// userFields are the user fields per entity type.
var userFields = map[string][]string{
	"ci":    {UserFieldOwner, UserFieldAssetHolder},
	"asset": {UserFieldOwner, UserFieldHolder},
}

// allUserFields are the user fields of all types, in a fixed order.
var allUserFields = []string{UserFieldOwner, UserFieldHolder, UserFieldAssetHolder}

// userFieldCounts reports whether a user field may count for a hit at all:
// with user:read, a holder field only with assets_basic.
func userFieldCounts(q *Query, field string) bool {
	if !q.UsersReadable {
		return false
	}
	return q.HoldersSearchable || (field != UserFieldHolder && field != UserFieldAssetHolder)
}

// openUserFields are the user fields whose rows count for a hit without a
// check per document, like openIdentifierFields: the holder of the asset at
// a CI only when the policy does not restrict assets; a type in restricted
// keeps all its fields, the restricted query checks them per document.
func openUserFields(q *Query, restricted []string) []string {
	out := []string{}
	for _, t := range fieldTypes {
		if len(q.EntityTypes) > 0 && !slices.Contains(q.EntityTypes, t) {
			continue
		}
		for _, f := range userFields[t] {
			switch {
			case !userFieldCounts(q, f), slices.Contains(out, f):
			case f == UserFieldAssetHolder && !slices.Contains(restricted, t) && q.Policy != nil && q.Policy.Restricted(permission.KindAsset):
			default:
				out = append(out, f)
			}
		}
	}
	return out
}

// readableUsers returns the user fields of the entity type the principal
// may read for an object of the type and scope, under the rule of the DTO
// projection (WP-265, WP-271): owner_user_id under the policy of the type,
// assigned_user_id of the asset (at a CI: of the linked asset, assetRef:
// hitAssetRef) under the asset policy.
func readableUsers(q *Query, entityType, typeID string, ref *permission.ObjectRef, assetRef string) []string {
	var out []string
	for _, field := range userFields[entityType] {
		if !userFieldCounts(q, field) {
			continue
		}
		readable := false
		switch field {
		case UserFieldOwner:
			readable = permission.Readable(q.Policy, permission.ObjectKind(entityType), typeID, *ref, "owner_user_id")
		case UserFieldHolder:
			readable = permission.Readable(q.Policy, permission.KindAsset, typeID, *ref, "assigned_user_id")
		case UserFieldAssetHolder:
			if !q.Policy.Restricted(permission.KindAsset) {
				readable = true
			} else if parts := strings.Split(assetRef, "|"); len(parts) == 4 {
				asset := permission.ObjectRef{ID: parts[0], ClientID: parts[2], SiteID: parts[3]}
				readable = permission.Readable(q.Policy, permission.KindAsset, parts[1], asset, "assigned_user_id")
			}
		}
		if readable {
			out = append(out, field)
		}
	}
	return out
}

// tagsReadable reports whether the field row core.tags of a ci or asset
// counts for a hit: like the DTO member tags in the projection (WP-265,
// E-105; structural at a CI).
func tagsReadable(q *Query, entityType, typeID string, ref *permission.ObjectRef) bool {
	if entityType != "ci" && entityType != "asset" {
		return false
	}
	return permission.Readable(q.Policy, permission.ObjectKind(entityType), typeID, *ref, tagPath)
}

type Hit struct {
	Document
	Score      float64  `json:"score"`
	Highlights []string `json:"highlights,omitempty"`
	// MatchedFields are the fields that matched the search text (SRC-02,
	// WP-786), with field permissions only the readable ones (RBA-07,
	// WP-266). The PostgreSQL index and, from its full documents, the
	// remote index of HybridBackend (WP-787) fill them, for a query with
	// highlighting.
	MatchedFields []MatchedField `json:"matched_fields,omitempty"`
}

// MatchedField is a field of a hit that matched the search text: the key of
// the field row or identifier (title, summary, core.<column>,
// attributes.<key>, asset.inventory_no, network.ip, network.mac) and its
// highlight, the ts_headline fragment of a text field or the value of an
// identifier in canonical form (a MAC with colons, an IP as host()).
type MatchedField struct {
	Field     string `json:"field"`
	Highlight string `json:"highlight"`
}

type Result struct {
	Data  []Hit `json:"data"`
	Total int   `json:"total"`
	// TotalCapped marks a Total of the PostgreSQL index that stopped at
	// api.TotalCap (E-104): more hits match. The handler answers total as
	// the hits of the page the caller may read, so it never reaches the
	// cap and the response does not carry the mark.
	TotalCapped bool `json:"-"`
	Limit       int  `json:"limit"`
	Offset      int  `json:"offset"`
	HasMore     bool `json:"has_more"`
	// NextCursor continues after this page (API-02).
	NextCursor string `json:"next_cursor,omitempty"`
}

// ReindexResult is the result of a rebuild of the index: the number of
// documents written. It is the result of the job search.reindex.
type ReindexResult struct {
	Indexed int `json:"indexed"`
}

// ReindexJobKind is the job kind of the rebuild of the index of an
// organization (SRC-02, WP-786).
const ReindexJobKind = "search.reindex"

// ReindexBatchSize is the number of entities of one transaction of
// PGRepository.ReindexTenant (WP-786).
const ReindexBatchSize = 1000

type Backend interface {
	Ping(ctx context.Context) error
	IndexDocument(ctx context.Context, doc *Document) error
	Delete(ctx context.Context, orgID, entityType, entityID string) error
	Query(ctx context.Context, q *Query) (Result, error)
	ReindexTenant(ctx context.Context, orgID string) (ReindexResult, error)
}

// ReadPermission maps each indexed entity type to the permission a caller
// needs to see its hits. Types missing here are never returned.
var ReadPermission = map[string]string{
	"ci":          "ci:read",
	"asset":       "asset:read",
	"document":    "document:read",
	"ticket":      "ticket:read",
	"contact":     "contact:read",
	"compliance":  "compliance:read",
	"location":    "location:read",
	"reservation": "reservation:read", // RBA-05, E-106 (WP-245)
	// Subnets and IP addresses (IPM-20, WP-909, migration 000303).
	"subnet":     "ipam:read",
	"ip_address": "ipam:read",
}
