package permission

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// The central field projection of the REST channel (RBA-07, E-43,
// WP-265): an object DTO loses the fields the principal may not read,
// writes are checked field by field against the policy, and list filters
// on fields the principal cannot read in every scope are refused. The
// policy is resolved at most once per request (FieldPolicyMiddleware,
// PolicyFrom).

// ciStructural are the members of the CI DTO that field permissions do not
// cover: RLS and the scope decide whether the CI is visible, and with it
// these members (WP-265). templates and tags are lists of their own, not
// field values.
var ciStructural = map[string]bool{
	"id": true, "organization_id": true, "ci_type_id": true, "client_id": true, "location_id": true,
	"site_id": true, "room_id": true, "parent_ci_id": true, "asset_id": true, "merged_into_id": true,
	"name": true, "status": true, "lifecycle_state": true, "health": true, "health_updated_at": true,
	"discovery_source": true, "first_seen_at": true, "last_seen_at": true, "is_manual": true,
	"version": true, "schema_version": true, "deleted_at": true, "created_at": true, "updated_at": true,
	"templates": true, "tags": true,
}

// ciProjected are the typed columns of a CI the projection covers
// (core.<column>): the typed fields of the CI except name and status.
var ciProjected = map[string]bool{
	"manufacturer": true, "model": true, "serial_number": true, "hardware_uuid": true,
	"management_ip": true, "primary_mac": true, "hostname": true, "fqdn": true, "os_name": true,
	"os_version": true, "firmware_version": true, "sys_object_id": true,
}

// assetStructural are the members of the asset DTO and of the embedded
// asset facts the asset policy does not cover: identity, name, client scope
// and the timestamps (WP-271).
var assetStructural = map[string]bool{
	"id": true, "organization_id": true, "client_id": true, "name": true, "created_at": true, "updated_at": true,
}

// assetFieldAliases are the AST-01 names of asset columns (WP-738) whose
// field key is still that of the deprecated name until WP-739 renames the
// keys: a grant on core.asset_tag also covers inventory_no.
var assetFieldAliases = map[string]string{
	"inventory_no": "asset_tag", "purchase_price": "purchase_cost", "warranty_until": "warranty_end",
}

// assetCustomFields is the member of the asset DTO with the custom fields;
// custom_fields.<key> has the field key attributes.<key> (WP-271).
const (
	assetCustomFields       = "custom_fields"
	assetCustomFieldsPrefix = assetCustomFields + "."
)

// contractStructural are the members of the contract DTO field permissions
// do not cover (WP-269): identity, number, client scope, version and the
// timestamps.
var contractStructural = map[string]bool{
	"id": true, "organization_id": true, "client_id": true, "number": true, "version": true,
	"created_at": true, "updated_at": true,
}

// ticketStructural are the members of the ticket DTO field permissions do
// not cover (WP-765): identity, number, title and the timestamps; the
// other members are the core fields of the kind ticket.
var ticketStructural = map[string]bool{
	"id": true, "organization_id": true, "ticket_number": true, "title": true, "resolved_at": true,
	"closed_at": true, "created_at": true, "updated_at": true,
}

// Members of the CI DTO with a projection of their own.
const (
	memberAttributes = "attributes"
	memberAsset      = "asset"
	attributesPrefix = memberAttributes + "."
)

// ErrUnsupportedKind refuses a projection of an object kind without a DTO
// mapping (ci, asset, contract and ticket have one).
var ErrUnsupportedKind = errors.New("field projection: unsupported object kind")

// ErrNoPolicySource is the error of PolicyFrom for a request with a
// principal but without the policy source of FieldPolicyMiddleware: the
// caller answers 500 (fail-closed).
var ErrNoPolicySource = errors.New("field permissions: no policy source for the principal")

// ProjectOption tunes Project, CheckWrite and the path mapping.
type ProjectOption func(*projectConfig)

type projectConfig struct {
	instance map[string]bool
	asset    *assetScope
}

type assetScope struct {
	typeID string
	ref    ObjectRef
}

func newProjectConfig(opts []ProjectOption) *projectConfig {
	cfg := &projectConfig{}
	for _, opt := range opts {
		opt(cfg)
	}
	return cfg
}

// WithInstanceKeys names the instance attributes of the CI: attributes.<key>
// of the DTO is the field _instance.<key> for these keys.
func WithInstanceKeys(keys []string) ProjectOption {
	return func(c *projectConfig) {
		if c.instance == nil {
			c.instance = make(map[string]bool, len(keys))
		}
		for _, k := range keys {
			c.instance[k] = true
		}
	}
}

// WithAsset gives the type and scope of the asset whose facts the CI DTO
// embeds (asset): its members are projected by the asset policy. Without
// it the facts are left out whenever the asset policy is restricted.
func WithAsset(typeID string, ref ObjectRef) ProjectOption {
	return func(c *projectConfig) { c.asset = &assetScope{typeID: typeID, ref: ref} }
}

// fieldKey maps a DTO path of the kind onto its field key: structural
// reports a member without field permission; ok is false for a path
// without mapping (fail-closed: callers treat it as not readable).
func (c *projectConfig) fieldKey(kind ObjectKind, path string) (key string, structural, ok bool) {
	switch kind {
	case KindCI:
		switch {
		case ciStructural[path]:
			return "", true, true
		case ciProjected[path]:
			return NamespaceCore + "." + path, false, true
		case strings.HasPrefix(path, attributesPrefix):
			name := strings.TrimPrefix(path, attributesPrefix)
			if name == "" {
				return "", false, false
			}
			if c.instance[name] {
				return NamespaceInstance + "." + name, false, true
			}
			return NamespaceAttributes + "." + name, false, true
		}
	case KindAsset:
		switch {
		case assetStructural[path]:
			return "", true, true
		case slices.Contains(coreFields[KindAsset], path):
			return NamespaceCore + "." + path, false, true
		case assetFieldAliases[path] != "":
			return NamespaceCore + "." + assetFieldAliases[path], false, true
		case strings.HasPrefix(path, assetCustomFieldsPrefix):
			if name := strings.TrimPrefix(path, assetCustomFieldsPrefix); name != "" {
				return NamespaceAttributes + "." + name, false, true
			}
		}
	case KindContract:
		switch {
		case contractStructural[path]:
			return "", true, true
		case slices.Contains(coreFields[KindContract], path):
			return NamespaceCore + "." + path, false, true
		}
	case KindTicket:
		switch {
		case ticketStructural[path]:
			return "", true, true
		case slices.Contains(coreFields[KindTicket], path):
			return NamespaceCore + "." + path, false, true
		}
	}
	return "", false, false
}

// FieldKeyOf returns the field key of a DTO path (hostname → core.hostname,
// attributes.cost_center → attributes.cost_center, an instance key given
// with WithInstanceKeys → _instance.<key>); structural is true for a member
// without field permission, ok false for a path without mapping.
func FieldKeyOf(kind ObjectKind, path string, opts ...ProjectOption) (key string, structural, ok bool) {
	return newProjectConfig(opts).fieldKey(kind, path)
}

// DTOPath returns the DTO path of a field key, the path errors name:
// core.<column> → <column>, attributes.<key> and _instance.<key> →
// attributes.<key>.
func DTOPath(fieldKey string) string {
	namespace, name, _ := strings.Cut(fieldKey, ".")
	switch namespace {
	case NamespaceCore:
		return name
	case NamespaceAttributes, NamespaceInstance:
		return attributesPrefix + name
	}
	return fieldKey
}

// CIFieldPath returns the DTO path of a CI field by the bare name the
// provenance, the overrides and the change history use: a structural
// member or typed column keeps its name, anything else is an attribute
// (attributes.<name>).
func CIFieldPath(name string) string {
	if ciStructural[name] || ciProjected[name] {
		return name
	}
	return attributesPrefix + name
}

// IsProjectedCIField reports whether the CI DTO path is subject to field
// permissions (a typed column of ciProjected or an attribute).
func IsProjectedCIField(path string) bool {
	_, structural, ok := FieldKeyOf(KindCI, path)
	return ok && !structural
}

// NeedsProjection reports whether a DTO of the kind can lose members for
// the policy; without it callers answer the value unchanged. A CI DTO
// embeds the asset facts, so it also needs one when the asset policy is
// restricted. A nil policy always needs one (fail-closed).
func NeedsProjection(policy *FieldPolicy, kind ObjectKind) bool {
	if policy.Restricted(kind) {
		return true
	}
	return kind == KindCI && policy.Restricted(KindAsset)
}

// readable reports whether the principal may read the field at path.
func (c *projectConfig) readable(policy *FieldPolicy, kind ObjectKind, typeID string, ref *ObjectRef, path string) bool {
	if !policy.Restricted(kind) {
		return true
	}
	key, structural, ok := c.fieldKey(kind, path)
	if !ok {
		return false
	}
	return structural || policy.Level(kind, typeID, key, *ref).Readable()
}

// Readable reports whether the principal may read the field at the DTO
// path of an object: always for a structural member or when the policy
// does not restrict the kind, never for a path without mapping.
func Readable(policy *FieldPolicy, kind ObjectKind, typeID string, ref ObjectRef, path string, opts ...ProjectOption) bool { //nolint:gocritic // ObjectRef by value like FieldPolicy.Level
	return newProjectConfig(opts).readable(policy, kind, typeID, &ref, path)
}

// ProjectAttributes returns the attributes (a CI's attributes map or a map
// keyed by attribute name such as the resolved references) without the
// keys the principal may not read; attrs itself when the CI policy is not
// restricted.
func ProjectAttributes(policy *FieldPolicy, typeID string, ref ObjectRef, attrs map[string]any, opts ...ProjectOption) map[string]any { //nolint:gocritic // ObjectRef by value like FieldPolicy.Level
	if !policy.Restricted(KindCI) || attrs == nil {
		return attrs
	}
	cfg := newProjectConfig(opts)
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if cfg.readable(policy, KindCI, typeID, &ref, attributesPrefix+k) {
			out[k] = v
		}
	}
	return out
}

// ProjectValue returns v as the principal may read it: v itself, without
// a JSON round trip, when the policy does not restrict the kind
// (NeedsProjection), otherwise the projection of Project.
func ProjectValue(policy *FieldPolicy, kind ObjectKind, typeID string, ref ObjectRef, v any, opts ...ProjectOption) (any, error) { //nolint:gocritic // ObjectRef by value like FieldPolicy.Level
	if !NeedsProjection(policy, kind) {
		return v, nil
	}
	return Project(policy, kind, typeID, ref, v, opts...)
}

// Project encodes v, a DTO of the kind (a CI with its asset facts, asset
// facts, a contract or a ticket), as an object without the members the principal may not read
// (pattern of api.SelectFields): structural members always stay, a typed
// column or attribute the policy does not let it read is left out, the
// embedded asset facts follow the asset policy (WithAsset). A member
// without mapping is left out while the kind is restricted (fail-closed).
// The projection reads nothing from the database.
func Project(policy *FieldPolicy, kind ObjectKind, typeID string, ref ObjectRef, v any, opts ...ProjectOption) (map[string]json.RawMessage, error) { //nolint:gocritic // ObjectRef by value like FieldPolicy.Level
	if kind != KindCI && kind != KindAsset && kind != KindContract && kind != KindTicket {
		return nil, ErrUnsupportedKind
	}
	body, err := toObject(v)
	if err != nil {
		return nil, err
	}
	cfg := newProjectConfig(opts)
	if kind != KindCI {
		return cfg.projectObject(policy, kind, typeID, &ref, body), nil
	}
	return cfg.projectCI(policy, typeID, &ref, body)
}

// toObject encodes v as a JSON object; an object map is copied.
func toObject(v any) (map[string]json.RawMessage, error) {
	if m, ok := v.(map[string]json.RawMessage); ok {
		out := make(map[string]json.RawMessage, len(m))
		for k, raw := range m {
			out[k] = raw
		}
		return out, nil
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var body map[string]json.RawMessage
	if uerr := json.Unmarshal(raw, &body); uerr != nil {
		return nil, uerr
	}
	if body == nil {
		body = map[string]json.RawMessage{}
	}
	return body, nil
}

// projectObject keeps the members of body the principal may read; the
// custom fields of an asset keep the readable keys.
func (c *projectConfig) projectObject(policy *FieldPolicy, kind ObjectKind, typeID string, ref *ObjectRef, body map[string]json.RawMessage) map[string]json.RawMessage {
	if !policy.Restricted(kind) {
		return body
	}
	for member, raw := range body {
		if kind == KindAsset && member == assetCustomFields && !isNull(raw) {
			body[member] = c.projectCustomFields(policy, typeID, ref, raw)
			continue
		}
		if !c.readable(policy, kind, typeID, ref, member) {
			delete(body, member)
		}
	}
	return body
}

// projectCustomFields returns the custom fields of an asset (a JSON
// object) without the keys the principal may not read; an empty object
// when they are no object (fail-closed).
func (c *projectConfig) projectCustomFields(policy *FieldPolicy, typeID string, ref *ObjectRef, raw json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return json.RawMessage("{}")
	}
	for k := range fields {
		if !c.readable(policy, KindAsset, typeID, ref, assetCustomFieldsPrefix+k) {
			delete(fields, k)
		}
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return json.RawMessage("{}")
	}
	return out
}

func isNull(raw json.RawMessage) bool {
	return len(raw) == 0 || string(raw) == "null"
}

// projectCI projects a CI DTO.
func (c *projectConfig) projectCI(policy *FieldPolicy, typeID string, ref *ObjectRef, body map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	ciRestricted := policy.Restricted(KindCI)
	for member, raw := range body {
		switch {
		case member == memberAsset:
			if isNull(raw) || !policy.Restricted(KindAsset) {
				continue
			}
			if c.asset == nil {
				// The asset is unknown: its facts are left out.
				delete(body, member)
				continue
			}
			var facts map[string]json.RawMessage
			if err := json.Unmarshal(raw, &facts); err != nil {
				return nil, err
			}
			projected, err := json.Marshal(c.projectObject(policy, KindAsset, c.asset.typeID, &c.asset.ref, facts))
			if err != nil {
				return nil, err
			}
			body[member] = projected
		case !ciRestricted:
		case member == memberAttributes:
			if isNull(raw) {
				continue
			}
			var attrs map[string]json.RawMessage
			if err := json.Unmarshal(raw, &attrs); err != nil {
				return nil, err
			}
			for k := range attrs {
				if !c.readable(policy, KindCI, typeID, ref, attributesPrefix+k) {
					delete(attrs, k)
				}
			}
			projected, err := json.Marshal(attrs)
			if err != nil {
				return nil, err
			}
			body[member] = projected
		case !c.readable(policy, KindCI, typeID, ref, member):
			delete(body, member)
		}
	}
	return body, nil
}

// CheckWrite checks the fields a write changes (DTO paths) against the
// policy: each must be editable, otherwise the write is refused with an
// *api.FieldForbiddenError naming the refused paths. The caller passes
// every field of the request the principal may not read (whatever its
// value: no equality oracle) and every readable field whose value differs
// from the current effective value. Structural members pass; a path
// without mapping is refused. Without a restriction of the kind every
// write passes (phase 1).
func CheckWrite(policy *FieldPolicy, kind ObjectKind, typeID string, ref ObjectRef, changed []string, opts ...ProjectOption) error { //nolint:gocritic // ObjectRef by value like FieldPolicy.Level
	if len(changed) == 0 || !policy.Restricted(kind) {
		return nil
	}
	cfg := newProjectConfig(opts)
	var refused []string
	for _, path := range changed {
		key, structural, ok := cfg.fieldKey(kind, path)
		if structural {
			continue
		}
		if !ok || !policy.Level(kind, typeID, key, ref).Editable() {
			refused = append(refused, path)
		}
	}
	return forbidden(refused)
}

// forbidden returns the *api.FieldForbiddenError of the paths, nil for
// none.
func forbidden(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	out := slices.Clone(paths)
	sort.Strings(out)
	return &api.FieldForbiddenError{Fields: slices.Compact(out)}
}

// CheckListFilter refuses list filters (DTO paths) on fields the principal
// cannot read in every scope of its role assignments for one of the CI
// types the list covers: the list filters across scopes, so the lowest
// level of each field counts (WP-265). typeIDs are the queried types and
// stand for their subtrees; without types every type the policy restricts
// counts. An object grant none of the principal for the kind makes every
// filter on a projected field refused. Structural fields pass.
func CheckListFilter(policy *FieldPolicy, kind ObjectKind, typeIDs, paths []string) error {
	if len(paths) == 0 || !policy.Restricted(kind) {
		return nil
	}
	var projected []string
	for _, path := range paths {
		if _, structural, ok := FieldKeyOf(kind, path); !ok || !structural {
			projected = append(projected, path)
		}
	}
	if len(projected) == 0 {
		return nil
	}
	if policy == nil || policy.objectGrantNone(kind) {
		return forbidden(projected)
	}
	types := policy.listTypes(kind, typeIDs)
	points := policy.scopePoints()
	var refused []string
	for _, path := range projected {
		key, _, ok := FieldKeyOf(kind, path)
		keys := []string{key}
		if ok && strings.HasPrefix(key, NamespaceAttributes+".") {
			// An attribute filter may also meet an instance attribute.
			keys = append(keys, NamespaceInstance+"."+strings.TrimPrefix(key, NamespaceAttributes+"."))
		}
		if !ok || !policy.readableEverywhere(kind, types, points, keys) {
			refused = append(refused, path)
		}
	}
	return forbidden(refused)
}

// objectGrantNone reports whether an object grant none of the kind applies
// to a role or team of the principal.
func (p *FieldPolicy) objectGrantNone(kind ObjectKind) bool {
	if p.unrestricted {
		return false
	}
	for _, grants := range p.idx.object[kind] {
		for _, g := range grants {
			if g.Level != LevelNone {
				continue
			}
			if g.GranteeTeamID != nil && p.teams[*g.GranteeTeamID] {
				return true
			}
			key := roleKey(g.GranteeRoleID, g.GranteeCustomRoleID)
			for i := range p.roles {
				if p.roles[i].key != "" && p.roles[i].key == key {
					return true
				}
			}
		}
	}
	return false
}

// listTypes returns the types a list filter is checked for: the named
// types and their descendants, or without names no type and every type
// the snapshot or the type chain knows.
func (p *FieldPolicy) listTypes(kind ObjectKind, named []string) []string {
	known := map[string]bool{}
	for child, parent := range p.parents {
		known[child] = true
		if parent != "" {
			known[parent] = true
		}
	}
	for _, grants := range p.idx.field {
		for _, g := range grants[kind] {
			if g.CITypeID != nil {
				known[*g.CITypeID] = true
			}
		}
	}
	for _, s := range p.idx.sensitive[kind] {
		if s.CITypeID != nil {
			known[*s.CITypeID] = true
		}
	}
	var out []string
	if len(named) == 0 {
		out = append(out, "")
		for t := range known {
			out = append(out, t)
		}
		sort.Strings(out)
		return out
	}
	set := map[string]bool{}
	for _, n := range named {
		set[n] = true
	}
	for t := range known {
		chain := p.chain(kind, t)
		for _, n := range named {
			if slices.Contains(chain, n) {
				set[t] = true
				break
			}
		}
	}
	for t := range set {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// otherScope stands for a client or site no assignment names.
const otherScope = "\x00other"

// scopePoints returns one object reference per combination of the clients
// and sites the assignments of the principal name, and one for a client or
// site none names: together they meet every scope an object can have.
func (p *FieldPolicy) scopePoints() []ObjectRef {
	clients := []string{otherScope}
	sites := []string{otherScope}
	for i := range p.roles {
		for _, c := range p.roles[i].grant.Clients {
			if !slices.Contains(clients, c) {
				clients = append(clients, c)
			}
		}
		for _, s := range p.roles[i].grant.Sites {
			if !slices.Contains(sites, s) {
				sites = append(sites, s)
			}
		}
	}
	out := make([]ObjectRef, 0, len(clients)*len(sites))
	for _, c := range clients {
		for _, s := range sites {
			out = append(out, ObjectRef{ClientID: c, SiteID: s})
		}
	}
	return out
}

// readableEverywhere reports whether each key is readable at every type
// and every scope point an assignment of the principal covers.
func (p *FieldPolicy) readableEverywhere(kind ObjectKind, types []string, points []ObjectRef, keys []string) bool {
	for i := range points {
		visible := false
		for j := range p.roles {
			if p.roles[j].inScope(&points[i]) {
				visible = true
				break
			}
		}
		if !visible {
			continue
		}
		for _, t := range types {
			for _, key := range keys {
				if !p.Level(kind, t, key, points[i]).Readable() {
					return false
				}
			}
		}
	}
	return true
}

// PolicyResolver resolves the field policy of a principal
// (*FieldResolver).
type PolicyResolver interface {
	Resolve(ctx context.Context, p identity.Principal) (*FieldPolicy, error)
}

// policySource resolves the policy of one request once, on first need.
type policySource struct {
	resolver PolicyResolver
	fixed    *FieldPolicy
	once     sync.Once
	policy   *FieldPolicy
	err      error
}

type policySourceKey struct{}

// FieldPolicyMiddleware puts the policy source of the request into its
// context (RBA-07, WP-265): PolicyFrom resolves the policy of the
// principal at most once per request and only when a handler or service
// needs it. It runs behind authentication and the route permission.
func FieldPolicyMiddleware(resolver PolicyResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			src := &policySource{resolver: resolver}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), policySourceKey{}, src)))
		})
	}
}

// WithPolicy puts a resolved policy into ctx (jobs that resolved the policy
// of their creator, tests); PolicyFrom returns it.
func WithPolicy(ctx context.Context, policy *FieldPolicy) context.Context {
	return context.WithValue(ctx, policySourceKey{}, &policySource{fixed: policy})
}

// PolicyFrom returns the field policy of the request: without a principal
// (ingest, system jobs) the unrestricted policy; with a principal the
// policy of the source in ctx, resolved at most once. A principal without
// source is ErrNoPolicySource and a resolution error is returned: callers
// answer 500 (fail-closed).
func PolicyFrom(ctx context.Context) (*FieldPolicy, error) {
	src, hasSource := ctx.Value(policySourceKey{}).(*policySource)
	if hasSource && src.fixed != nil {
		return src.fixed, nil
	}
	principal, ok := identity.PrincipalFromContext(ctx)
	if !ok {
		return UnrestrictedPolicy(nil), nil
	}
	if !hasSource || src.resolver == nil {
		return nil, ErrNoPolicySource
	}
	src.once.Do(func() {
		src.policy, src.err = src.resolver.Resolve(ctx, principal)
		if src.err == nil && src.policy == nil {
			src.err = ErrNoPolicySource
		}
	})
	return src.policy, src.err
}
