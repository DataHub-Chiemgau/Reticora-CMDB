package ci

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// IncludeReferences is the include of GET /cis/{id} that resolves the
// reference values of the CI to their targets (MET-12, WP-713).
const IncludeReferences = "references"

// ReferenceView is one reference value of include=references: the target
// kind and the stored id and, when resolved, the name of the target or the
// marker of a target that is gone. resolved is false when the target cannot
// be resolved for the caller (no read permission on the target kind, memory
// mode): then only target and id are written.
type ReferenceView struct {
	Target string
	ID     string
	// DisplayName is the name of a present target; nil when it is missing.
	DisplayName *string
	Missing     bool
	// Kind is the kind of a location target (CH28, LOC-10).
	Kind string
	// MergedInto is the survivor of a merged CI target whose tombstone the
	// caller sees (CI-12).
	MergedInto string
	resolved   bool
}

// MarshalJSON writes {target, id} for an unresolved value and
// {target, id, display_name, missing[, kind][, merged_into]} for a resolved
// one; display_name is null for a missing target.
func (v *ReferenceView) MarshalJSON() ([]byte, error) {
	if !v.resolved {
		return json.Marshal(struct {
			Target string `json:"target"`
			ID     string `json:"id"`
		}{v.Target, v.ID})
	}
	return json.Marshal(struct {
		Target      string  `json:"target"`
		ID          string  `json:"id"`
		DisplayName *string `json:"display_name"`
		Missing     bool    `json:"missing"`
		Kind        string  `json:"kind,omitempty"`
		MergedInto  string  `json:"merged_into,omitempty"`
	}{v.Target, v.ID, v.DisplayName, v.Missing, v.Kind, v.MergedInto})
}

// Resolved reports whether the value names the state of its target.
func (v *ReferenceView) Resolved() bool { return v.resolved }

// ReferenceName is the display name of a present target; Kind is set for a
// location.
type ReferenceName struct {
	DisplayName string
	Kind        string
}

// ReferenceNamer resolves reference values to the names of their targets
// for include=references: one statement per target kind for all ids, and
// one for the survivors of merged CI targets. Like the existence check it
// runs in the caller's scope; an id that is not returned is missing for the
// caller.
type ReferenceNamer interface {
	ReferenceNames(ctx context.Context, orgID, target string, ids []string) (map[string]ReferenceName, error)
	MergedTargets(ctx context.Context, orgID string, ids []string) (map[string]string, error)
}

// The PostgreSQL lookup of the existence check resolves the names as well.
var _ ReferenceNamer = (*PGReferenceLookup)(nil)

// referenceReadPermission is the permission that shows the names of a
// target kind; teams are read with user:read.
var referenceReadPermission = map[string]identity.Permission{
	fieldmeta.TargetCI:       identity.PermCIRead,
	fieldmeta.TargetAsset:    identity.PermAssetRead,
	fieldmeta.TargetUser:     identity.PermUserRead,
	fieldmeta.TargetTeam:     identity.PermUserRead,
	fieldmeta.TargetLocation: identity.PermLocationRead,
	fieldmeta.TargetDocument: identity.PermDocumentRead,
}

// referenceNameQueries select, per target, the present targets among $2
// with their name: a CI that is neither deleted nor merged, a user, a team,
// a location with its kind, an asset and a document (policy
// document_tenant_isolation). Row level security of the caller's scope
// applies; organization_id is named as well. A deactivated user still
// exists and keeps its name.
var referenceNameQueries = map[string]string{
	fieldmeta.TargetCI: `SELECT id::text, name, '' FROM ci
		WHERE organization_id = $1 AND id = ANY($2::uuid[]) AND deleted_at IS NULL AND merged_into_id IS NULL`,
	fieldmeta.TargetUser: `SELECT id::text, display_name, '' FROM app_user
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	fieldmeta.TargetTeam: `SELECT id::text, name, '' FROM team
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	fieldmeta.TargetLocation: `SELECT id::text, name, kind FROM location
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	fieldmeta.TargetAsset: `SELECT id::text, name, '' FROM asset
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
	fieldmeta.TargetDocument: `SELECT id::text, title, '' FROM document
		WHERE organization_id = $1 AND id = ANY($2::uuid[])`,
}

// mergedTargetsQuery selects the survivors of merged CIs among $2 whose
// tombstone holds (the conditions of MergedInto); the latest merge wins.
const mergedTargetsQuery = `SELECT DISTINCT ON (victim_id) victim_id::text, survivor_id::text FROM ci_merge
	WHERE organization_id = $1 AND victim_id = ANY($2::uuid[]) AND unmerged_at IS NULL AND tombstone_until > now()
	ORDER BY victim_id, merged_at DESC`

// ReferenceNames implements ReferenceNamer; the ids of the result are lower
// case.
func (l *PGReferenceLookup) ReferenceNames(ctx context.Context, orgID, target string, ids []string) (map[string]ReferenceName, error) {
	query, ok := referenceNameQueries[target]
	if !ok {
		return nil, fmt.Errorf("reference target %s has no names", target)
	}
	found := make(map[string]ReferenceName, len(ids))
	if len(ids) == 0 {
		return found, nil
	}
	err := database.WithRequestTenant(ctx, l.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, query, orgID, ids)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			var name ReferenceName
			if serr := rows.Scan(&id, &name.DisplayName, &name.Kind); serr != nil {
				return serr
			}
			found[strings.ToLower(id)] = name
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// MergedTargets implements ReferenceNamer: the survivor of each merged CI
// among ids whose tombstone the caller sees, in one statement.
func (l *PGReferenceLookup) MergedTargets(ctx context.Context, orgID string, ids []string) (map[string]string, error) {
	found := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return found, nil
	}
	err := database.WithRequestTenant(ctx, l.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, qerr := tx.Query(ctx, mergedTargetsQuery, orgID, ids)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var victim, survivor string
			if serr := rows.Scan(&victim, &survivor); serr != nil {
				return serr
			}
			found[strings.ToLower(victim)] = survivor
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}

// References is the include references of GET /cis/{id} (MET-12, WP-713):
// for every reference-typed or file attribute of the CI with a value (type,
// global and instance attributes) the target and id and, when the caller
// has the read permission of the target kind, its name or the marker of a
// missing target. A multi_value attribute yields a list ([]ReferenceView),
// any other a *ReferenceView; attributes keeps the ids. The names are
// resolved with one statement per target kind and one for merged CI
// targets (no N+1); without a namer (memory mode) every value is
// {target, id}.
func (s *Service) References(ctx context.Context, orgID, ciID string) (map[string]any, error) {
	out := map[string]any{}
	if s.fields == nil {
		return out, nil
	}
	item, err := s.repo.GetByID(ctx, orgID, ciID)
	if err != nil {
		return nil, err
	}
	defs, err := s.fields.ResolveFields(ctx, orgID, item.CITypeID, item.ID)
	if err != nil {
		return nil, err
	}
	namer, _ := s.references.(ReferenceNamer)
	perms, _ := identity.PermissionsFromContext(ctx)

	type use struct {
		name string
		list bool
		ids  []string
	}
	var uses []use
	byTarget := map[string][]string{}
	targetOf := map[string]string{}
	for i := range defs {
		def := &defs[i]
		target := fieldmeta.ReferenceTargetOf(def)
		if target == "" {
			continue
		}
		value, present := item.Attributes[def.Name]
		if !present || isBlank(value) {
			continue
		}
		_, list := value.([]any)
		ids := referenceIDs(value)
		uses = append(uses, use{name: def.Name, list: list, ids: ids})
		targetOf[def.Name] = target
		if namer == nil || !identity.HasPermission(perms, referenceReadPermission[target]) {
			continue
		}
		// A stored value without UUID form (G1) names no target: it is
		// missing without a query.
		for _, id := range ids {
			if key := strings.ToLower(id); api.IsUUID(id) && !slices.Contains(byTarget[target], key) {
				byTarget[target] = append(byTarget[target], key)
			}
		}
	}

	targets := make([]string, 0, len(byTarget))
	for target := range byTarget {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	names := map[string]map[string]ReferenceName{}
	var missingCIs []string
	for _, target := range targets {
		found, nerr := namer.ReferenceNames(ctx, orgID, target, byTarget[target])
		if nerr != nil {
			return nil, fmt.Errorf("resolve references to %s: %w", target, nerr)
		}
		names[target] = found
		if target != fieldmeta.TargetCI {
			continue
		}
		for _, id := range byTarget[target] {
			if _, ok := found[id]; !ok {
				missingCIs = append(missingCIs, id)
			}
		}
	}
	merged := map[string]string{}
	if len(missingCIs) > 0 {
		if merged, err = namer.MergedTargets(ctx, orgID, missingCIs); err != nil {
			return nil, fmt.Errorf("resolve merged references: %w", err)
		}
	}

	resolvable := func(target string) bool {
		return namer != nil && identity.HasPermission(perms, referenceReadPermission[target])
	}
	for _, u := range uses {
		target := targetOf[u.name]
		views := make([]ReferenceView, 0, len(u.ids))
		for _, id := range u.ids {
			view := ReferenceView{Target: target, ID: id}
			if resolvable(target) {
				view.resolved = true
				key := strings.ToLower(id)
				if name, ok := names[target][key]; ok {
					display := name.DisplayName
					view.DisplayName = &display
					view.Kind = name.Kind
				} else {
					view.Missing = true
					if target == fieldmeta.TargetCI {
						view.MergedInto = merged[key]
					}
				}
			}
			views = append(views, view)
		}
		switch {
		case u.list:
			out[u.name] = views
		case len(views) == 1:
			out[u.name] = &views[0]
		}
	}
	return out, nil
}
