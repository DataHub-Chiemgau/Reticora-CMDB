package importer

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
)

// Targets of a location row (entity location) and of a location reference
// in the rows of other entities.
const (
	// LocationKindTarget is the kind of the location of the row (site,
	// building, room, rack, warehouse, zone, shelf, bin).
	LocationKindTarget = "kind"
	// LocationNameTarget is the name of the location of the row; its
	// parent is named by LocationParentTarget.
	LocationNameTarget = "name"
	// LocationParentTarget is the name path of the parent of the row,
	// from the site down.
	LocationParentTarget = "parent"
	// LocationPathTarget is the full name path of the location of the row
	// (instead of name and parent), from the site down.
	LocationPathTarget = "path"
	// LocationClientTarget is the client of a site the row creates.
	LocationClientTarget = "client_id"
)

// PathSeparator separates the levels of a name path given as text; a list
// value (transformation split) gives the levels itself.
const PathSeparator = "/"

// ParsePath returns the levels of a name path: the elements of a list, or
// the text split at PathSeparator; spaces around a level are dropped,
// empty levels left out.
func ParsePath(v Value) []string {
	items := v.List
	if !v.IsList {
		items = strings.Split(v.Text, PathSeparator)
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// pathKinds returns the kinds of the levels of a path to a location of
// kind leaf, from the site down: the parent matrix of LOC-10 gives every
// kind exactly one parent kind. levels must match its length.
func pathKinds(leaf locations.Kind, levels int) ([]locations.Kind, error) {
	if !leaf.Valid() {
		return nil, fmt.Errorf("unknown location kind %q (site, building, room, rack, warehouse, zone, shelf or bin)", leaf)
	}
	chain := []locations.Kind{leaf}
	for k := leaf; ; {
		parent, ok := k.ParentKind()
		if !ok {
			break
		}
		chain = append(chain, parent)
		k = parent
	}
	slices.Reverse(chain)
	if levels != len(chain) {
		names := make([]string, len(chain))
		for i, k := range chain {
			names[i] = string(k)
		}
		return nil, fmt.Errorf("the path of a %s has %d levels (%s), not %d", leaf, len(chain), strings.Join(names, PathSeparator), levels)
	}
	return chain, nil
}

// nodeKey is a level of a name path: the parent (empty for a site), the
// kind and the name in lower case.
type nodeKey struct {
	parent string
	kind   locations.Kind
	name   string
}

// LocationResolver resolves name paths in the location tree for one run:
// per level kind and name without regard to case below the parent found
// for the level above (locations.Repository.List with Filter{ParentID,
// Kind, Search} and an exact comparison). Hits are kept for the run; a
// location a row created joins them only once its row committed, so a dry
// run never sees the creation of an earlier row.
type LocationResolver struct {
	repo  locations.Repository
	orgID string
	mu    sync.Mutex
	hits  map[nodeKey][]string
}

// NewLocationResolver returns the resolver of one run of orgID.
func NewLocationResolver(repo locations.Repository, orgID string) *LocationResolver {
	return &LocationResolver{repo: repo, orgID: orgID, hits: map[nodeKey][]string{}}
}

// lookup returns the ids of the locations of kind named name below parent
// (empty: the sites). It reads under the caller's row level security, in
// the transaction of the row when ctx carries one.
func (r *LocationResolver) lookup(ctx context.Context, parent string, kind locations.Kind, name string) ([]string, error) {
	key := nodeKey{parent: parent, kind: kind, name: strings.ToLower(name)}
	r.mu.Lock()
	ids, ok := r.hits[key]
	r.mu.Unlock()
	if ok {
		return ids, nil
	}
	list, err := r.repo.List(ctx, r.orgID, locations.Filter{ParentID: parent, Kind: kind, Search: name})
	if err != nil {
		return nil, err
	}
	for i := range list {
		l := &list[i]
		if l.Kind == kind && l.ParentID == parent && strings.EqualFold(strings.TrimSpace(l.Name), name) {
			ids = append(ids, l.ID)
		}
	}
	if len(ids) > 0 {
		r.mu.Lock()
		r.hits[key] = ids
		r.mu.Unlock()
	}
	return ids, nil
}

// remember adds a location the row created once the row committed.
func (r *LocationResolver) remember(ctx context.Context, parent string, kind locations.Kind, name, id string) {
	key := nodeKey{parent: parent, kind: kind, name: strings.ToLower(name)}
	database.AfterCommit(ctx, func(context.Context) {
		r.mu.Lock()
		defer r.mu.Unlock()
		if !slices.Contains(r.hits[key], id) {
			r.hits[key] = append(r.hits[key], id)
		}
	})
}

// Resolve resolves a location reference of a CI or asset row: the name
// path of a location of kind leaf from the site down. A reference creates
// no location: a level without match is location_not_found, one with
// several matches ambiguous_location. field names the target in the issue.
func (r *LocationResolver) Resolve(ctx context.Context, leaf locations.Kind, path []string, field string) (string, *Issue) {
	kinds, err := pathKinds(leaf, len(path))
	if err != nil {
		return "", &Issue{Field: field, Code: CodeInvalidValue, Message: err.Error()}
	}
	parent := ""
	for i, name := range path {
		ids, lerr := r.lookup(ctx, parent, kinds[i], name)
		if lerr != nil {
			return "", &Issue{Field: field, Code: CodeWriteFailed, Message: "the location could not be read"}
		}
		switch len(ids) {
		case 0:
			return "", &Issue{Field: field, Code: CodeLocationNotFound,
				Message: fmt.Sprintf("no %s %q at %s", kinds[i], name, strings.Join(path[:i+1], PathSeparator))}
		case 1:
			parent = ids[0]
		default:
			return "", &Issue{Field: field, Code: CodeAmbiguousLocation,
				Message: fmt.Sprintf("%d locations of kind %s are named %q at %s", len(ids), kinds[i], name, strings.Join(path[:i+1], PathSeparator))}
		}
	}
	return parent, nil
}

// locationResolverKey is the key of the resolver in the state of a run.
type locationResolverKey struct{}

// RunLocationResolver returns the location resolver of a run, created on
// first use: writers of CI and asset rows resolve their references with
// it (WP-302, WP-776) and share its hits with the location writer.
func RunLocationResolver(run *RunInfo, repo locations.Repository) *LocationResolver {
	return run.State(locationResolverKey{}, func() any { return NewLocationResolver(repo, run.OrganizationID) }).(*LocationResolver)
}

// LocationWriter writes the rows of the entity location: it resolves the
// name path of the row and creates the missing levels with
// locations.Repository.Create along the parent matrix of LOC-10; a site
// needs client_id (column or default). The repository writes the audit of
// every created location (source import from the run's request info).
type LocationWriter struct {
	repo locations.Repository
}

// NewLocationWriter returns the writer of location rows.
func NewLocationWriter(repo locations.Repository) *LocationWriter {
	return &LocationWriter{repo: repo}
}

// Entity implements Writer.
func (w *LocationWriter) Entity() string { return "location" }

// failed is the result of a row with one issue.
func failed(line int, field, code, message string) RowResult {
	return RowResult{Outcome: OutcomeFailed, Issues: []Issue{{Line: line, Field: field, Code: code, Message: message}}}
}

// Write implements Writer: the levels that exist are found, the missing
// ones created in mode create_missing and both. The row is created when
// its location was created, skipped when it existed (a location has no
// further field an import changes) or when it is missing in mode
// update_existing.
func (w *LocationWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	kind := locations.Kind(strings.ToLower(strings.TrimSpace(row.Values.Text(LocationKindTarget))))
	if kind == "" {
		return failed(row.Line, LocationKindTarget, CodeInvalidValue, "the kind of the location is required"), nil
	}
	field := LocationPathTarget
	path := ParsePath(row.Values.Values[LocationPathTarget])
	if len(path) == 0 {
		field = LocationNameTarget
		name := strings.TrimSpace(row.Values.Text(LocationNameTarget))
		if name == "" {
			return failed(row.Line, LocationNameTarget, CodeInvalidValue, "the name or the path of the location is required"), nil
		}
		path = append(ParsePath(row.Values.Values[LocationParentTarget]), name)
	}
	kinds, err := pathKinds(kind, len(path))
	if err != nil {
		if !kind.Valid() {
			field = LocationKindTarget
		}
		return failed(row.Line, field, CodeInvalidValue, err.Error()), nil
	}
	resolver := RunLocationResolver(row.Run, w.repo)
	parent := ""
	created := false
	for i, name := range path {
		at := strings.Join(path[:i+1], PathSeparator)
		ids, lerr := resolver.lookup(ctx, parent, kinds[i], name)
		if lerr != nil {
			return RowResult{}, lerr
		}
		if len(ids) > 1 {
			return failed(row.Line, field, CodeAmbiguousLocation,
				fmt.Sprintf("%d locations of kind %s are named %q at %s", len(ids), kinds[i], name, at)), nil
		}
		if len(ids) == 1 {
			parent = ids[0]
			continue
		}
		if row.Run.Mode == "update_existing" {
			return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("no %s at %s (mode update_existing)", kinds[i], at)}, nil
		}
		id, res, cerr := w.create(ctx, row, kinds[i], parent, name, at)
		if cerr != nil {
			return RowResult{}, cerr
		}
		if res != nil {
			return *res, nil
		}
		resolver.remember(ctx, parent, kinds[i], name, id)
		parent, created = id, true
	}
	full := strings.Join(path, PathSeparator)
	if !created {
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the %s %s exists", kind, full)}, nil
	}
	return RowResult{Outcome: OutcomeCreated, Message: fmt.Sprintf("%s %s", kind, full)}, nil
}

// create creates one level of the path; a refusal is the result of the
// row. A site needs client_id within the caller's client scope, and a
// caller restricted to sites creates none (out_of_scope).
func (w *LocationWriter) create(ctx context.Context, row *WriteRow, kind locations.Kind, parent, name, at string) (string, *RowResult, error) {
	req := locations.CreateRequest{Kind: kind, ParentID: parent, Name: name}
	if kind == locations.KindSite {
		req.ClientID = strings.TrimSpace(row.Values.Text(LocationClientTarget))
		if req.ClientID == "" {
			res := failed(row.Line, LocationClientTarget, CodeInvalidValue, fmt.Sprintf("the site %s is missing; client_id is required to create it", at))
			return "", &res, nil
		}
		if !api.IsUUID(req.ClientID) {
			res := failed(row.Line, LocationClientTarget, CodeInvalidValue, "client_id must be a UUID")
			return "", &res, nil
		}
		if scope, ok := database.TenantScopeFromContext(ctx); !ok || !scopeCovers(scope.Clients, req.ClientID) || !scope.Sites.IsAll() {
			res := failed(row.Line, LocationClientTarget, CodeOutOfScope, fmt.Sprintf("the site %s lies outside the scope of the user who started the import", at))
			return "", &res, nil
		}
	}
	loc, err := w.repo.Create(ctx, row.Run.OrganizationID, req)
	if err == nil {
		return loc.ID, nil, nil
	}
	var conflict *api.ConflictError
	var fe *locations.FieldError
	switch {
	case kind == locations.KindSite && errors.Is(err, locations.ErrInvalidParent):
		// The client is not visible: outside the scope.
		res := failed(row.Line, LocationClientTarget, CodeOutOfScope, fmt.Sprintf("the site %s lies outside the scope of the user who started the import", at))
		return "", &res, nil
	case errors.As(err, &conflict):
		res := failed(row.Line, LocationNameTarget, CodeInvalidValue, fmt.Sprintf("a location named %q exists at %s", name, at))
		return "", &res, nil
	case errors.As(err, &fe):
		res := failed(row.Line, fe.Field, CodeInvalidValue, fe.Message)
		return "", &res, nil
	case errors.Is(err, locations.ErrInvalidParent), errors.Is(err, locations.ErrInvalidInput), errors.Is(err, locations.ErrInvalidKind):
		res := failed(row.Line, LocationPathTarget, CodeInvalidValue, err.Error())
		return "", &res, nil
	}
	return "", nil, err
}

// scopeCovers reports whether a scope dimension covers id.
func scopeCovers(s database.ScopeSet, id string) bool {
	if s.IsAll() {
		return true
	}
	return slices.ContainsFunc(s.IDs(), func(x string) bool { return strings.EqualFold(x, id) })
}
