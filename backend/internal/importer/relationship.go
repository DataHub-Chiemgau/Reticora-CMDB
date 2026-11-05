package importer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// Targets of a relationship row. Each end is the CI id (<end>_ci_id) or
// the identity of the CI, resolved like a CI row (REC-02): serial number,
// hardware UUID, primary MAC, management IP with VRF and the client.
const (
	RelTypeTarget         = "rel_type"
	RelSourceTarget       = "source"
	RelTargetTarget       = "target"
	RelSourceIfaceTarget  = "source_interface_id"
	RelTargetIfaceTarget  = "target_interface_id"
	relCIIDSuffix         = "_ci_id"
	relationsTargetPrefix = "relations."
)

// relIdentitySuffixes are the identity columns of an end.
var relIdentitySuffixes = []string{"_serial_number", "_hardware_uuid", "_primary_mac", "_management_ip", "_vrf_id", "_client_id"}

// EdgeKey is the identity of a stored edge (uq_ci_relationship_edge): a
// row without interface columns names the edge without interfaces.
type EdgeKey struct {
	SourceCIID, TargetCIID, RelType      string
	SourceInterfaceID, TargetInterfaceID string
}

// FindRelationships implements EntityStore.
func (r *PGRepository) FindRelationships(ctx context.Context, orgID string, edge *EdgeKey) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM ci_relationship
		WHERE organization_id = $1 AND source_ci_id = $2::uuid AND target_ci_id = $3::uuid AND rel_type = $4
		  AND source_interface_id IS NOT DISTINCT FROM $5::uuid AND target_interface_id IS NOT DISTINCT FROM $6::uuid
		ORDER BY id LIMIT $7`, orgID, edge.SourceCIID, edge.TargetCIID, edge.RelType,
		nilIfBlank(edge.SourceInterfaceID), nilIfBlank(edge.TargetInterfaceID), findLimit)
}

// RelationshipTypeID implements EntityStore: the organization's row before
// the system one, as the relationship API resolves a key.
func (r *PGRepository) RelationshipTypeID(ctx context.Context, orgID, key string) (string, error) {
	ids, err := r.findIDs(ctx, orgID, `SELECT id::text FROM relationship_type
		WHERE key = $2 AND (organization_id = $1 OR organization_id IS NULL) ORDER BY organization_id NULLS LAST LIMIT 1`, orgID, key)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return ids[0], nil
}

func nilIfBlank(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// errEdgeUnchanged ends the update of an edge the row does not change.
var errEdgeUnchanged = errors.New("import: the relationship is unchanged")

// relOrgWideEnd stands for the missing client or site of an edge end in
// the field permissions of relationships (relationship.orgWideEnd, E-140):
// only grants without a client or site scope cover it.
const relOrgWideEnd = "\x00org-wide"

// Field keys of the core fields of an edge a row writes.
const (
	relFieldNotes      = permission.NamespaceCore + ".notes"
	relFieldConfidence = permission.NamespaceCore + ".confidence"
)

// RelationshipWriter writes the rows of the entity relationship and the
// relation columns of CI rows (WP-776): the ends are CI ids or CI
// identities (REC-02, reference_not_found, ambiguous_identity), the edge
// is found by the key of uq_ci_relationship_edge and created with source
// import through relationship.Repository (its history and event) or, in
// the modes update_existing and both, its notes and confidence changed;
// the field permissions of the starter apply to both ends, and every
// written edge gets one audit entry relationship.created or
// relationship.updated with source import.
type RelationshipWriter struct {
	repo       relationship.Repository
	store      EntityStore
	policies   permission.PolicyResolver
	identities *discovery.Handler
}

// NewRelationshipWriter returns the writer of relationship rows.
func NewRelationshipWriter(repo relationship.Repository, store EntityStore, policies permission.PolicyResolver) *RelationshipWriter {
	return &RelationshipWriter{repo: repo, store: store, policies: policies}
}

// WithIdentities resolves ends given by their identity with the identity
// resolution of the ingest (WP-302); without it only CI ids are accepted.
func (w *RelationshipWriter) WithIdentities(d *discovery.Handler) *RelationshipWriter {
	w.identities = d
	return w
}

// Entity implements Writer.
func (w *RelationshipWriter) Entity() string { return "relationship" }

// relRunKey is the key of the relationship state of a run.
type relRunKey struct{}

// relRun is the identity resolver of a run and the id of the generic
// device type the ends are resolved with.
type relRun struct {
	once     sync.Once
	err      error
	resolver *discovery.IdentityResolver
	typeID   string
}

// resolver returns the identity resolver of the run, created once outside
// the row transactions.
func (w *RelationshipWriter) resolver(ctx context.Context, run *RunInfo) (*relRun, error) {
	st := run.State(relRunKey{}, func() any { return &relRun{} }).(*relRun)
	st.once.Do(func() {
		if w.identities == nil {
			st.err = errors.New("import: CI identities cannot be resolved")
			return
		}
		outside := database.WithoutTx(ctx)
		if st.resolver, st.err = w.identities.NewIdentityResolver(outside, run.OrganizationID); st.err != nil {
			return
		}
		typ, ok, err := st.resolver.Type(outside, ci.GenericDeviceType)
		switch {
		case err != nil:
			st.err = err
		case !ok:
			st.err = fmt.Errorf("import: the CI type %s is missing", ci.GenericDeviceType)
		default:
			st.typeID = typ.ID
		}
	})
	return st, st.err
}

// resolveItem resolves the CI of an identity: one live match is the CI,
// several are ambiguous_identity, none reference_not_found. field names
// the end in the issue.
func (w *RelationshipWriter) resolveItem(ctx context.Context, run *RunInfo, item *discovery.IngestItem, field string) (string, *Issue, error) {
	if w.identities == nil {
		return "", &Issue{Field: field, Code: CodeInvalidValue, Message: "the CI must be given by its id"}, nil
	}
	st, err := w.resolver(ctx, run)
	if err != nil {
		return "", nil, err
	}
	item.CITypeName = st.typeID
	item.Source = ci.SourceImport
	resolved, err := st.resolver.Resolve(ctx, []discovery.IngestItem{*item})
	if err != nil {
		return "", nil, err
	}
	res := resolved[0].Result
	switch res.Action {
	case discovery.ReconcileMatched:
		return res.MatchedCIID, nil, nil
	case discovery.ReconcileConflict:
		return "", &Issue{Field: field, Code: CodeAmbiguousIdentity,
			Message: fmt.Sprintf("the CI matches %d CIs by %s", len(res.CandidateCIIDs), res.Criterion)}, nil
	}
	return "", &Issue{Field: field, Code: CodeReferenceNotFound,
		Message: "no CI in the scope of the user who started the import matches the identity"}, nil
}

// resolveEnd resolves an end (source or target) of a relationship row.
func (w *RelationshipWriter) resolveEnd(ctx context.Context, run *RunInfo, v *MappedRow, end string) (string, *Issue, error) {
	field := end + relCIIDSuffix
	if id := rowText(v, field); id != "" {
		if !api.IsUUID(id) {
			return "", &Issue{Field: field, Code: CodeInvalidValue, Message: field + " must be a UUID"}, nil
		}
		return strings.ToLower(id), nil, nil
	}
	item := discovery.IngestItem{SerialNumber: rowText(v, end+"_serial_number"), HardwareUUID: rowText(v, end+"_hardware_uuid"),
		PrimaryMAC: rowText(v, end+"_primary_mac"), ManagementIP: rowText(v, end+"_management_ip"), VRFID: rowText(v, end+"_vrf_id"),
		ClientID: rowText(v, end+"_client_id")}
	if item.SerialNumber == "" && item.HardwareUUID == "" && item.PrimaryMAC == "" && item.ManagementIP == "" {
		return "", &Issue{Field: field, Code: CodeInvalidValue,
			Message: fmt.Sprintf("the %s CI is required: %s or its serial number, hardware UUID, MAC or management IP", end, field)}, nil
	}
	return w.resolveItem(ctx, run, &item, field)
}

// edgeWrite is one edge a row writes; field names the edge in the issues
// (empty: the fields of a relationship row).
type edgeWrite struct {
	key        EdgeKey
	notes      *string
	confidence *float64
	field      string
}

// fieldOr returns the field of the issue: w.field, else fallback.
func (e *edgeWrite) fieldOr(fallback string) string {
	if e.field != "" {
		return e.field
	}
	return fallback
}

// parseRelationship reads a relationship row: the ends, the type, the
// interfaces, notes and confidence.
func (w *RelationshipWriter) parseRelationship(ctx context.Context, row *WriteRow) (*edgeWrite, *Issue, error) {
	v := row.Values
	allowed := map[string]bool{RelTypeTarget: true, RelSourceIfaceTarget: true, RelTargetIfaceTarget: true, "notes": true, "confidence": true}
	for _, end := range []string{RelSourceTarget, RelTargetTarget} {
		allowed[end+relCIIDSuffix] = true
		for _, s := range relIdentitySuffixes {
			allowed[end+s] = true
		}
	}
	targets := make([]string, 0, len(v.Values))
	for target := range v.Values {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		if !allowed[target] {
			return nil, &Issue{Field: target, Code: CodeInvalidValue, Message: fmt.Sprintf("%s is no field of a relationship", target)}, nil
		}
	}
	e := &edgeWrite{key: EdgeKey{RelType: rowText(v, RelTypeTarget), SourceInterfaceID: strings.ToLower(rowText(v, RelSourceIfaceTarget)),
		TargetInterfaceID: strings.ToLower(rowText(v, RelTargetIfaceTarget))}}
	if e.key.RelType == "" {
		return nil, &Issue{Field: RelTypeTarget, Code: CodeInvalidValue, Message: "the type of the relationship is required"}, nil
	}
	for _, f := range []struct{ field, value string }{
		{RelSourceIfaceTarget, e.key.SourceInterfaceID}, {RelTargetIfaceTarget, e.key.TargetInterfaceID},
	} {
		if f.value != "" && !api.IsUUID(f.value) {
			return nil, &Issue{Field: f.field, Code: CodeInvalidValue, Message: f.field + " must be a UUID"}, nil
		}
	}
	if notes := rowText(v, "notes"); notes != "" {
		e.notes = &notes
	}
	if text := rowText(v, "confidence"); text != "" {
		f, err := strconv.ParseFloat(strings.Replace(text, ",", ".", 1), 64)
		if err != nil || f < 0 || f > 1 {
			return nil, &Issue{Field: "confidence", Code: CodeInvalidValue, Message: "confidence must be a number between 0 and 1"}, nil
		}
		e.confidence = &f
	}
	var issue *Issue
	var err error
	if e.key.SourceCIID, issue, err = w.resolveEnd(ctx, row.Run, v, RelSourceTarget); err != nil || issue != nil {
		return nil, issue, err
	}
	if e.key.TargetCIID, issue, err = w.resolveEnd(ctx, row.Run, v, RelTargetTarget); err != nil || issue != nil {
		return nil, issue, err
	}
	return e, nil, nil
}

// Write implements Writer for a relationship row.
func (w *RelationshipWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	policy, err := runPolicy(ctx, row.Run, w.policies)
	if err != nil {
		return RowResult{}, err
	}
	ctx = permission.WithPolicy(ctx, policy)
	e, issue, err := w.parseRelationship(ctx, row)
	if err != nil {
		return RowResult{}, err
	}
	if issue != nil {
		return failed(row.Line, issue.Field, issue.Code, issue.Message), nil
	}
	return w.writeEdge(ctx, policy, row, e, row.Run.Mode)
}

// endScopes returns the client and site of both ends, or the issue of an
// end that is not visible (reference_not_found).
func (w *RelationshipWriter) endScopes(ctx context.Context, orgID string, e *edgeWrite) (src, tgt relationship.EndpointScope, issue *Issue, err error) {
	reader, ok := w.repo.(relationship.ScopeReader)
	if !ok {
		return src, tgt, nil, nil
	}
	scopes, err := reader.EndpointScopes(ctx, orgID, []string{e.key.SourceCIID, e.key.TargetCIID})
	if err != nil {
		return src, tgt, nil, err
	}
	var okSrc, okTgt bool
	src, okSrc = scopes[e.key.SourceCIID]
	tgt, okTgt = scopes[e.key.TargetCIID]
	switch {
	case !okSrc:
		return src, tgt, &Issue{Field: e.fieldOr(RelSourceTarget + relCIIDSuffix), Code: CodeReferenceNotFound,
			Message: "the source CI does not exist or lies outside the scope of the user who started the import"}, nil
	case !okTgt:
		return src, tgt, &Issue{Field: e.fieldOr(RelTargetTarget + relCIIDSuffix), Code: CodeReferenceNotFound,
			Message: "the target CI does not exist or lies outside the scope of the user who started the import"}, nil
	}
	return src, tgt, nil, nil
}

// edgeRef is the object reference of an end for the field permissions.
func edgeRef(id string, end relationship.EndpointScope) permission.ObjectRef {
	ref := permission.ObjectRef{ID: id, ClientID: end.ClientID, SiteID: end.SiteID}
	if ref.ClientID == "" {
		ref.ClientID = relOrgWideEnd
	}
	if ref.SiteID == "" {
		ref.SiteID = relOrgWideEnd
	}
	return ref
}

// checkEdgeFields checks the core fields an edge write sets against the
// field permissions of both ends (RBA-07, E-140): each must be editable
// at both ends.
func checkEdgeFields(policy *permission.FieldPolicy, typeID, id string, src, tgt relationship.EndpointScope, keys []string) error {
	if len(keys) == 0 || !policy.Restricted(permission.KindRelationship) {
		return nil
	}
	var refused []string
	for _, key := range keys {
		for _, end := range []relationship.EndpointScope{src, tgt} {
			if !policy.Level(permission.KindRelationship, typeID, key, edgeRef(id, end)).Editable() {
				refused = append(refused, permission.DTOPath(key))
				break
			}
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)
	return &api.FieldForbiddenError{Fields: refused}
}

// writeEdge finds the edge of e and creates or changes it by mode.
func (w *RelationshipWriter) writeEdge(ctx context.Context, policy *permission.FieldPolicy, row *WriteRow, e *edgeWrite, mode string) (RowResult, error) {
	orgID, line := row.Run.OrganizationID, row.Line
	if relationship.StructuralRelTypes[e.key.RelType] {
		return failed(line, e.fieldOr(RelTypeTarget), CodeInvalidValue,
			fmt.Sprintf("the type %s is projected from the CI and cannot be stored", e.key.RelType)), nil
	}
	typeID, err := w.store.RelationshipTypeID(ctx, orgID, e.key.RelType)
	if err != nil {
		return RowResult{}, err
	}
	if typeID == "" {
		return failed(line, e.fieldOr(RelTypeTarget), CodeInvalidValue, fmt.Sprintf("unknown relationship type %q", e.key.RelType)), nil
	}
	src, tgt, issue, err := w.endScopes(ctx, orgID, e)
	if err != nil {
		return RowResult{}, err
	}
	if issue != nil {
		return failed(line, issue.Field, issue.Code, issue.Message), nil
	}
	ids, err := w.store.FindRelationships(ctx, orgID, &e.key)
	if err != nil {
		return RowResult{}, err
	}
	switch {
	case len(ids) == 1 && mode == "create_missing":
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the relationship %s exists (mode create_missing)", ids[0])}, nil
	case len(ids) == 1:
		return w.updateEdge(ctx, policy, row, e, ids[0], typeID, src, tgt)
	case len(ids) > 1:
		return failed(line, e.fieldOr(RelTypeTarget), CodeAmbiguousIdentity, fmt.Sprintf("%d relationships have the key of the row", len(ids))), nil
	case mode == "update_existing":
		return RowResult{Outcome: OutcomeSkipped, Message: "no relationship has the key of the row (mode update_existing)",
			Issues: []Issue{{Field: e.fieldOr(RelTypeTarget), Code: CodeNoIdentityMatch, Message: "no relationship has the key of the row"}}}, nil
	}
	return w.createEdge(ctx, policy, row, e, typeID, src, tgt)
}

// keys are the field keys of the core fields e sets.
func (e *edgeWrite) keys() []string {
	var keys []string
	if e.notes != nil {
		keys = append(keys, relFieldNotes)
	}
	if e.confidence != nil {
		keys = append(keys, relFieldConfidence)
	}
	return keys
}

// createEdge creates the edge with source import, its provenance and the
// type lists and cardinality of its type checked (as POST /relationships).
func (w *RelationshipWriter) createEdge(ctx context.Context, policy *permission.FieldPolicy, row *WriteRow, e *edgeWrite,
	typeID string, src, tgt relationship.EndpointScope) (RowResult, error) {
	line, orgID := row.Line, row.Run.OrganizationID
	if err := checkEdgeFields(policy, typeID, "", src, tgt, e.keys()); err != nil {
		res, _ := refusalOf(line, err, nil)
		return res, nil
	}
	actor, actorType := auditActor(ctx, row.Run)
	provenanceActor := actor
	if provenanceActor == "" {
		provenanceActor = "import"
	}
	rel := &relationship.Relationship{OrganizationID: orgID, SourceCIID: e.key.SourceCIID, TargetCIID: e.key.TargetCIID,
		RelType: e.key.RelType, Source: ci.SourceImport, Confidence: e.confidence, VerificationState: "unverified",
		SourceInterfaceID: e.key.SourceInterfaceID, TargetInterfaceID: e.key.TargetInterfaceID,
		Attributes: map[string]any{relationship.ProvenanceAttribute: relationship.Provenance(provenanceActor, actorType, ci.SourceImport, time.Now())}}
	if e.notes != nil {
		rel.Notes = *e.notes
	}
	rel.CheckTypeLists()
	if err := w.repo.Create(ctx, rel); err != nil {
		if res, ok := edgeRefusal(line, e, err); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	changes := map[string]any{"source_ci_id": rel.SourceCIID, "target_ci_id": rel.TargetCIID, "rel_type": rel.RelType, "source": rel.Source}
	for field, value := range map[string]string{"source_interface_id": rel.SourceInterfaceID, "target_interface_id": rel.TargetInterfaceID, "notes": rel.Notes} {
		if value != "" {
			changes[field] = value
		}
	}
	if rel.Confidence != nil {
		changes["confidence"] = *rel.Confidence
	}
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "relationship.created", ResourceType: "relationship", ResourceID: rel.ID, Changes: changes}); err != nil {
		return RowResult{}, err
	}
	return RowResult{Outcome: OutcomeCreated, Message: rel.ID}, nil
}

// updateEdge changes notes and confidence of the edge id when the row
// sets them to other values; the repository writes the history and the
// event of the change (WP-296).
func (w *RelationshipWriter) updateEdge(ctx context.Context, policy *permission.FieldPolicy, row *WriteRow, e *edgeWrite,
	id, typeID string, src, tgt relationship.EndpointScope) (RowResult, error) {
	line, orgID := row.Line, row.Run.OrganizationID
	unchanged := RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the relationship %s is unchanged", id)}
	if e.notes == nil && e.confidence == nil {
		return unchanged, nil
	}
	actor, actorType := auditActor(ctx, row.Run)
	changes := map[string]any{}
	req := relationship.UpdateRequest{ActorID: actor, Check: func(_ context.Context, current *relationship.Relationship) error {
		if e.notes != nil && *e.notes != current.Notes {
			changes["notes"] = map[string]any{"old": current.Notes, "new": *e.notes}
		}
		if e.confidence != nil && (current.Confidence == nil || *current.Confidence != *e.confidence) {
			changes["confidence"] = map[string]any{"old": current.Confidence, "new": *e.confidence}
		}
		if len(changes) == 0 {
			return errEdgeUnchanged
		}
		// Every field the row sets must be editable at both ends; a field
		// the starter cannot read is not editable either (as PATCH).
		return checkEdgeFields(policy, typeID, id, src, tgt, e.keys())
	}}
	req.Notes, req.Confidence = e.notes, e.confidence
	if _, err := w.repo.Update(ctx, orgID, id, req); err != nil {
		if errors.Is(err, errEdgeUnchanged) {
			return unchanged, nil
		}
		if res, ok := edgeRefusal(line, e, err); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "relationship.updated", ResourceType: "relationship", ResourceID: id, Changes: changes}); err != nil {
		return RowResult{}, err
	}
	return RowResult{Outcome: OutcomeUpdated, Message: id}, nil
}

// edgeRefusal turns the refusal of the relationship repository into the
// issues of the row: an end that is not visible (reference_not_found), an
// end of a type the relationship type does not allow, the cardinality of
// the type and the field and database rules.
func edgeRefusal(line int, e *edgeWrite, err error) (RowResult, bool) {
	var issues []Issue
	for _, end := range []struct {
		field, code string
		err         error
	}{
		{RelSourceTarget + relCIIDSuffix, CodeReferenceNotFound, relationship.ErrSourceCINotFound},
		{RelSourceTarget + relCIIDSuffix, CodeInvalidValue, relationship.ErrSourceCITypeNotAllowed},
		{RelTargetTarget + relCIIDSuffix, CodeReferenceNotFound, relationship.ErrTargetCINotFound},
		{RelTargetTarget + relCIIDSuffix, CodeInvalidValue, relationship.ErrTargetCITypeNotAllowed},
		{RelTypeTarget, CodeInvalidValue, relationship.ErrCardinalityExceeded},
	} {
		if errors.Is(err, end.err) {
			issues = append(issues, Issue{Line: line, Field: e.fieldOr(end.field), Code: end.code, Message: end.err.Error()})
		}
	}
	if len(issues) > 0 {
		return RowResult{Outcome: OutcomeFailed, Issues: issues}, true
	}
	res, ok := refusalOf(line, err, map[string]string{"uq_ci_relationship_edge": e.fieldOr(RelTypeTarget)})
	if ok && e.field != "" {
		for i := range res.Issues {
			res.Issues[i].Field = e.field
		}
	}
	return res, ok
}

// relationValues are the CIs a relation column names: a list, or a text
// split at commas.
func relationValues(v Value) []string {
	items := v.List
	if !v.IsList {
		items = strings.Split(v.Text, ",")
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		if s := strings.TrimSpace(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// hasRelations reports whether a CI row sets a relation column.
func hasRelations(v *MappedRow) bool {
	for target, val := range v.Values {
		if strings.HasPrefix(target, relationsTargetPrefix) && len(relationValues(val)) > 0 {
			return true
		}
	}
	return false
}

// WriteRelations writes the relation columns of a CI row after its CI
// (WP-301 mapping "column → relation", relations.<type>): every CI the
// column names, by id or by serial number, gets the edge <type> from the
// CI of the row, in the transaction of the row; an edge that exists stays.
// The first refusal is the result of the row (the whole row fails).
func (w *RelationshipWriter) WriteRelations(ctx context.Context, row *WriteRow, ciID string) (*RowResult, error) {
	policy, err := runPolicy(ctx, row.Run, w.policies)
	if err != nil {
		return nil, err
	}
	targets := make([]string, 0, len(row.Values.Values))
	for target := range row.Values.Values {
		if strings.HasPrefix(target, relationsTargetPrefix) {
			targets = append(targets, target)
		}
	}
	sort.Strings(targets)
	for _, target := range targets {
		relType := strings.TrimPrefix(target, relationsTargetPrefix)
		for _, ref := range relationValues(row.Values.Values[target]) {
			to := strings.ToLower(ref)
			if !api.IsUUID(ref) {
				var issue *Issue
				if to, issue, err = w.resolveItem(ctx, row.Run, &discovery.IngestItem{SerialNumber: ref}, target); err != nil {
					return nil, err
				}
				if issue != nil {
					res := failed(row.Line, issue.Field, issue.Code, issue.Message)
					return &res, nil
				}
			}
			e := &edgeWrite{key: EdgeKey{SourceCIID: ciID, TargetCIID: to, RelType: relType}, field: target}
			res, werr := w.writeEdge(ctx, policy, row, e, "create_missing")
			if werr != nil {
				return nil, werr
			}
			if res.Outcome == OutcomeFailed {
				return &res, nil
			}
		}
	}
	return nil, nil //nolint:nilnil // nil result: every edge written
}
