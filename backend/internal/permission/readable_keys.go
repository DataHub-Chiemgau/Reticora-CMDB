package permission

import (
	"slices"
	"sort"
	"strings"
)

// Checks on field keys for the structured query and the export (RBA-07,
// WP-729): both name fields without a DTO of their own (asset custom
// fields, the columns of an export layout), so they check field keys
// (core.<column>, attributes.<key>, _instance.<key>) instead of DTO paths.

// keyAlternatives returns the keys a field key of the kind stands for: an
// attribute of a CI may also be an instance attribute of the same name.
func keyAlternatives(kind ObjectKind, key string) []string {
	if name, ok := strings.CutPrefix(key, NamespaceAttributes+"."); ok && kind == KindCI {
		return []string{key, NamespaceInstance + "." + name}
	}
	return []string{key}
}

// UnreadableKeys returns the field keys among keys that the principal
// cannot read in every scope of its role assignments for one of the types
// (the rule of CheckListFilter on field keys): a filter, sort key or column
// on them would be an oracle. typeIDs are the queried types and stand for
// their subtrees; without types every type counts. An object grant none
// of the principal for the kind makes every key unreadable. Without a
// restriction of the kind nothing is unreadable; a nil policy reads
// nothing (fail-closed).
func UnreadableKeys(policy *FieldPolicy, kind ObjectKind, typeIDs, keys []string) []string {
	if len(keys) == 0 || !policy.Restricted(kind) {
		return nil
	}
	if policy == nil || policy.objectGrantNone(kind) {
		return sortedUnique(keys)
	}
	types := policy.listTypes(kind, typeIDs)
	points := policy.scopePoints()
	var out []string
	for _, key := range keys {
		if !policy.readableEverywhere(kind, types, points, keyAlternatives(kind, key)) {
			out = append(out, key)
		}
	}
	return sortedUnique(out)
}

// TypeUnreadableKeys returns the field keys among keys that no role of the
// principal may read for one of the types, in any scope and before object
// grants: an export leaves such a column out entirely (WP-729), while a key
// that is not readable for single objects only (object grant, scope of an
// assignment) leaves the cells of those objects empty (Readable). typeIDs
// as for UnreadableKeys. Without a restriction of the kind nothing is
// unreadable; a nil policy reads nothing (fail-closed).
func TypeUnreadableKeys(policy *FieldPolicy, kind ObjectKind, typeIDs, keys []string) []string {
	if len(keys) == 0 || !policy.Restricted(kind) {
		return nil
	}
	if policy == nil {
		return sortedUnique(keys)
	}
	types := policy.listTypes(kind, typeIDs)
	var out []string
	for _, key := range keys {
		if !policy.readableForTypes(kind, types, keyAlternatives(kind, key)) {
			out = append(out, key)
		}
	}
	return sortedUnique(out)
}

// unnamedAttribute is the name of an attribute no grant and no sensitivity
// can name (field keys follow dynamicKey): its level is the level of every
// attribute the policy does not name.
const unnamedAttribute = "\x00"

// UnnamedAttributesUnreadable reports whether the attributes of the kind
// that no grant and no sensitivity names are unreadable for one of the
// types (TypeUnreadableKeys): a grant none of a type or of the kind closes
// every attribute, whose keys the policy does not know.
func UnnamedAttributesUnreadable(policy *FieldPolicy, kind ObjectKind, typeIDs []string) bool {
	return len(TypeUnreadableKeys(policy, kind, typeIDs, []string{NamespaceAttributes + "." + unnamedAttribute})) > 0
}

// readableForTypes reports whether each key is readable for each type by
// at least one role of the principal: an object reference without id,
// client, site and team is in the scope of every assignment and meets no
// object grant.
func (p *FieldPolicy) readableForTypes(kind ObjectKind, types, keys []string) bool {
	for _, t := range types {
		for _, key := range keys {
			if !p.Level(kind, t, key, ObjectRef{}).Readable() {
				return false
			}
		}
	}
	return true
}

// NamedFieldKeys returns the field keys of the kind the policy names: a
// field grant of one of the principal's roles with a field key, or a field
// marked sensitive; sorted and without duplicates. Callers that list the
// attributes a principal may not read (the export manifest) check these;
// an attribute no grant and no sensitivity names follows the grants of its
// type or kind, which a check of the whole namespace covers. Without a
// restriction it is empty.
func (p *FieldPolicy) NamedFieldKeys(kind ObjectKind) []string {
	if p == nil || p.unrestricted {
		return nil
	}
	var out []string
	for i := range p.roles {
		key := p.roles[i].key
		if key == "" {
			continue
		}
		for _, g := range p.idx.field[key][kind] {
			if g.FieldKey != nil {
				out = append(out, *g.FieldKey)
			}
		}
	}
	for _, s := range p.idx.sensitive[kind] {
		out = append(out, s.FieldKey)
	}
	return sortedUnique(out)
}

func sortedUnique(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := slices.Clone(keys)
	sort.Strings(out)
	return slices.Compact(out)
}
