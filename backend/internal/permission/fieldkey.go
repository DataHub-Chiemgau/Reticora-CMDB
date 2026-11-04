package permission

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

// ObjectKind is the kind of object a field permission applies to (RBA-07,
// E-43).
type ObjectKind string

// The object kinds of field permissions; <kind>:read and <kind>:write are
// the object permissions that set their default level.
const (
	KindCI           ObjectKind = "ci"
	KindAsset        ObjectKind = "asset"
	KindContract     ObjectKind = "contract"
	KindTicket       ObjectKind = "ticket"
	KindDocument     ObjectKind = "document"
	KindRelationship ObjectKind = "relationship"
)

// ObjectKinds lists every object kind in a fixed order.
var ObjectKinds = []ObjectKind{KindCI, KindAsset, KindContract, KindTicket, KindDocument, KindRelationship}

// Valid reports whether k is one of ObjectKinds.
func (k ObjectKind) Valid() bool { return slices.Contains(ObjectKinds, k) }

// typed reports whether objects of the kind carry a CI type (ci.ci_type_id,
// asset.asset_type_id).
func (k ObjectKind) typed() bool { return k == KindCI || k == KindAsset }

// Field key namespaces.
const (
	// NamespaceCore names a typed column of the object table.
	NamespaceCore = "core"
	// NamespaceAttributes names a dynamic attribute: an attribute_definition
	// key of a CI, a custom_fields key of an asset, an attributes key of a
	// relationship.
	NamespaceAttributes = "attributes"
	// NamespaceInstance names an instance attribute of a CI
	// (ci_instance_field_definition).
	NamespaceInstance = "_instance"
)

// ErrUnknownFieldKey refuses a field key that is malformed, names a
// structural field, does not exist or does not fit the object kind.
var ErrUnknownFieldKey = errors.New("unknown field key")

// coreFields are the grantable typed columns per object kind. Structural
// fields (id, version, type, name or title, client_id, site_id and the
// timestamps) are not grantable: lists, routing and the tenant scope need
// them; for a contract also number (WP-269).
var coreFields = map[ObjectKind][]string{
	KindCI: {
		"asset_id", "discovery_source", "firmware_version", "fqdn", "hardware_uuid", "health", "hostname",
		"lifecycle_state", "location_id", "management_ip", "manufacturer", "model", "os_name", "os_version",
		"owner_team_id", "owner_user_id", "parent_ci_id", "primary_mac", "room_id", "serial_number", "status",
		"sys_object_id",
	},
	KindAsset: {
		"asset_tag", "barcode", "category", "ci_id", "contract_id", "cost_center", "currency", "invoice_number",
		"lifecycle_state", "location", "location_id", "manufacturer", "model", "notes", "owner_team_id",
		"owner_user_id", "parent_asset_id", "purchase_cost", "purchase_date", "rfid_tag", "serial_number", "status",
		"supplier", "supplier_id", "warranty_end",
	},
	KindContract: {
		"currency", "end_date", "kind", "notes", "notice_period_days", "start_date", "status", "supplier_id", "value",
	},
	KindTicket: {
		"assignee_id", "category", "description", "due_date", "priority", "related_asset_id", "related_ci_id",
		"reporter_id", "status", "tags", "team_id",
	},
	KindDocument: {"category", "description", "file_name", "file_size", "mime_type", "tags", "uploaded_by"},
	KindRelationship: {
		"confidence", "notes", "source", "source_interface_id", "source_system", "target_interface_id",
		"verification_state", "verified_by",
	},
}

// CoreFields returns the grantable typed columns of the kind, sorted.
func CoreFields(kind ObjectKind) []string { return slices.Clone(coreFields[kind]) }

// dynamicKey is the syntax of attribute and instance keys.
var dynamicKey = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,127}$`)

// FieldKey is a parsed field key: Namespace.Name.
type FieldKey struct {
	Namespace string
	Name      string
}

// String returns the key as stored (field_grant.field_key).
func (k FieldKey) String() string { return k.Namespace + "." + k.Name }

// ParseFieldKey checks a field key of the kind (RBA-07): core.<column> from
// the fixed list of the kind, attributes.<key> (ci, asset, relationship) or
// _instance.<key> (ci only). Attribute and instance keys are checked for
// syntax only; whether a CI attribute or instance key exists is checked by
// the repository. Anything else is ErrUnknownFieldKey.
func ParseFieldKey(kind ObjectKind, key string) (FieldKey, error) {
	namespace, name, ok := strings.Cut(key, ".")
	if !ok || name == "" || !kind.Valid() {
		return FieldKey{}, ErrUnknownFieldKey
	}
	switch namespace {
	case NamespaceCore:
		if !slices.Contains(coreFields[kind], name) {
			return FieldKey{}, ErrUnknownFieldKey
		}
	case NamespaceAttributes:
		if (kind != KindCI && kind != KindAsset && kind != KindRelationship) || !dynamicKey.MatchString(name) {
			return FieldKey{}, ErrUnknownFieldKey
		}
	case NamespaceInstance:
		if kind != KindCI || !dynamicKey.MatchString(name) {
			return FieldKey{}, ErrUnknownFieldKey
		}
	default:
		return FieldKey{}, ErrUnknownFieldKey
	}
	return FieldKey{Namespace: namespace, Name: name}, nil
}
