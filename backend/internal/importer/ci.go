package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// Codes of the CI rows (WP-302, IMP-IO-01) in the report: a failed row
// carries its refusals, a written or skipped row its notes.
const (
	// CodeAmbiguousIdentity: the row matches several CIs (REC-02); it is
	// not written.
	CodeAmbiguousIdentity = "ambiguous_identity"
	// CodeResurrectedDevice: the row matches only soft-deleted CIs
	// (CI-13); no CI is created, a resurrected_device review is opened.
	CodeResurrectedDevice = "resurrected_device"
	// CodeNoIdentityMatch: in mode update_existing the row matches no CI
	// (a host name or FQDN alone never matches).
	CodeNoIdentityMatch = "no_identity_match"
	// CodeProbableDuplicate: the new CI shares its host name or FQDN with
	// CIs (CH29); a probable_duplicate review is opened.
	CodeProbableDuplicate = "probable_duplicate"
	// CodeIdentityTaken: a new CI would take the serial number, hardware
	// UUID or primary MAC another CI holds; the row is not written.
	CodeIdentityTaken = "identity_taken"
	// CodeObservedAtInFuture: the observation time of the row lies in the
	// future; the row is not written.
	CodeObservedAtInFuture = "observed_at_in_future"
	// CodeProvenanceUnreadable: the provenance or the override of a field
	// cannot be read; the row is not written (fail-closed, REC-03).
	CodeProvenanceUnreadable = "provenance_unreadable"
	// CodeOverrideKept: a field with a manual override keeps its value
	// (OVR-02); a differing value opens an override_conflict review.
	CodeOverrideKept = "override_kept"
	// CodeLowerRank: a field keeps the value of a source of higher rank
	// (REC-03, a manual value) or of a newer observation (REC-05).
	CodeLowerRank = "lower_rank"
	// CodeInstanceField: an instance attribute of the CI is not written
	// by an import (MET-14).
	CodeInstanceField = "instance_field"
	// CodeAssetCanonical: the location and the serial number of a CI
	// linked to an asset are the asset's (DB-05); the import does not
	// write them.
	CodeAssetCanonical = "asset_canonical"
	// CodeFieldForbidden: the user who started the import may not write
	// the fields of the message (RBA-07, WP-265); the row is not written.
	CodeFieldForbidden = "field_forbidden"
	// CodeEntitlementLimit: the new CI would pass max_cis (ENT-03); the
	// row is not written.
	CodeEntitlementLimit = "entitlement-limit"
	// CodeUniqueTaken: another CI holds the value of a unique attribute
	// (MET-15); the row is not written.
	CodeUniqueTaken = "unique_taken"
)

// ciAnalyzeThreshold is the number of new CIs of a run after which the
// planner statistics are refreshed (database.AnalyzeAfterBulk, WP-834),
// as the ingest does after its windows.
const ciAnalyzeThreshold = 500

// CIWriter writes the rows of the entity ci (WP-302, IMP-IO-01): the
// identity of a row is resolved with the ingest's implementation
// (discovery.IdentityResolver, REC-02) under the identity locks of E-56,
// every field of a known CI is decided by the central write decision with
// source import (rank 95, override.Guard, REC-03, OVR-02), and new CIs are
// created through ci.Service with discovery_source import, admitted
// against max_cis in the row's transaction (ci.CIAdmitter, WP-835).
type CIWriter struct {
	cis       *ci.Service
	discovery *discovery.Handler
	guard     *override.Guard
	policies  permission.PolicyResolver
	instance  discovery.InstanceFieldSource
	fields    ci.FieldResolver
	locations locations.Repository
	analyzer  discovery.BulkAnalyzer
	relations *RelationshipWriter
}

// NewCIWriter returns the writer of CI rows: cis creates and changes the
// CIs, d resolves identities and opens the reviews of the ingest,
// provenance keeps the provenance and overrides of the fields and policies
// resolves the field and action policy of the user who started the run.
func NewCIWriter(cis *ci.Service, d *discovery.Handler, provenance override.Repository, policies permission.PolicyResolver) *CIWriter {
	return &CIWriter{cis: cis, discovery: d, guard: override.NewGuard(provenance), policies: policies}
}

// WithInstanceFields sets the source of the instance attributes of a CI
// (MET-14); without it every attribute of a known CI is refused
// (fail-closed).
func (w *CIWriter) WithInstanceFields(src discovery.InstanceFieldSource) *CIWriter {
	w.instance = src
	return w
}

// WithFields sets the field definitions the texts of attributes are typed
// with (integer, decimal, boolean, multiselect, json); without them
// attributes stay texts.
func (w *CIWriter) WithFields(fields ci.FieldResolver) *CIWriter {
	w.fields = fields
	return w
}

// WithLocations resolves the location paths of CI rows (target location).
func (w *CIWriter) WithLocations(repo locations.Repository) *CIWriter {
	w.locations = repo
	return w
}

// WithAnalyzer refreshes the planner statistics after a run created many
// CIs (WP-834).
func (w *CIWriter) WithAnalyzer(a discovery.BulkAnalyzer) *CIWriter {
	w.analyzer = a
	return w
}

// WithRelations writes the relation columns of CI rows (relations.<type>,
// WP-776) after the CI, in the transaction of the row; without it a row
// that sets one fails.
func (w *CIWriter) WithRelations(rw *RelationshipWriter) *CIWriter {
	w.relations = rw
	return w
}

// Entity implements Writer.
func (w *CIWriter) Entity() string { return "ci" }

// ciRunKey is the key of the CI state of a run.
type ciRunKey struct{}

// ciRun is the state of a run of CI rows: the identity resolver (types,
// identity context), the policy of the starter and the committed new CIs.
type ciRun struct {
	once     sync.Once
	err      error
	resolver *discovery.IdentityResolver
	policy   *permission.FieldPolicy
	created  atomic.Int64
}

// state returns the state of the run, read once outside the transaction
// of the row (the types, the identity context and the policy precede the
// identity locks, E-56).
func (w *CIWriter) state(ctx context.Context, run *RunInfo) (*ciRun, error) {
	st := run.State(ciRunKey{}, func() any { return &ciRun{} }).(*ciRun)
	st.once.Do(func() {
		outside := database.WithoutTx(ctx)
		if st.resolver, st.err = w.discovery.NewIdentityResolver(outside, run.OrganizationID); st.err != nil {
			return
		}
		p, ok := identity.PrincipalFromContext(ctx)
		if !ok || w.policies == nil {
			st.err = errors.New("import ci rows: the policy of the user who started the import cannot be resolved")
			return
		}
		st.policy, st.err = w.policies.Resolve(outside, p)
	})
	return st, st.err
}

// ciRow is a CI row after its checks before the transaction work.
type ciRow struct {
	row        *WriteRow
	item       discovery.IngestItem
	typ        discovery.ResolvedType
	observedAt time.Time
	// texts are the typed columns the row sets (non-empty), attrs its
	// attributes (non-empty).
	texts map[string]string
	attrs map[string]Value
	notes []Issue
}

// Write implements Writer for a CI row; the transaction of the row is in
// ctx.
func (w *CIWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	st, err := w.state(ctx, row.Run)
	if err != nil {
		return RowResult{}, err
	}
	ctx = permission.WithPolicy(ctx, st.policy)
	r, refusal, err := w.prepare(ctx, st, row)
	if err != nil || refusal != nil {
		return derefResult(refusal), err
	}
	// The identity locks are the first statement of the transaction
	// (E-56, K21): a concurrent ingest or import of the same device waits.
	if lockErr := st.resolver.LockIdentities(ctx, &r.item); lockErr != nil {
		return RowResult{}, lockErr
	}
	resolved, err := st.resolver.Resolve(ctx, []discovery.IngestItem{r.item})
	if err != nil {
		return RowResult{}, err
	}
	res := &resolved[0]
	mode := row.Run.Mode
	switch {
	case res.Result.Action == discovery.ReconcileConflict:
		return failed(row.Line, "", CodeAmbiguousIdentity,
			fmt.Sprintf("the row matches %d CIs by %s", len(res.Result.CandidateCIIDs), res.Result.Criterion)), nil
	case res.Result.Action == discovery.ReconcileMatched:
		if mode == "create_missing" {
			return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the CI %s exists (mode create_missing)", res.Result.MatchedCIID)}, nil
		}
		out, updateErr := w.update(ctx, st, r, res)
		return w.relate(ctx, row, res.Result.MatchedCIID, out, updateErr)
	case len(res.Resurrected) > 0:
		return w.resurrected(ctx, st, r, res), nil
	case mode == "update_existing":
		return RowResult{Outcome: OutcomeSkipped, Message: "no CI matches the row (mode update_existing)",
			Issues: []Issue{{Code: CodeNoIdentityMatch, Message: "no CI matches the serial number, hardware UUID, MAC or management IP of the row"}}}, nil
	}
	out, err := w.create(ctx, st, r, res)
	return w.relate(ctx, row, out.Message, out, err)
}

// relate writes the relation columns of a written row after its CI
// ciID, through the relationship writer in the transaction of the row: a
// refused edge fails the whole row.
func (w *CIWriter) relate(ctx context.Context, row *WriteRow, ciID string, res RowResult, err error) (RowResult, error) {
	if err != nil || res.Outcome == OutcomeFailed || w.relations == nil || !hasRelations(row.Values) {
		return res, err
	}
	refusal, err := w.relations.WriteRelations(ctx, row, ciID)
	if err != nil {
		return RowResult{}, err
	}
	if refusal != nil {
		return *refusal, nil
	}
	return res, nil
}

func derefResult(r *RowResult) RowResult {
	if r == nil {
		return RowResult{}
	}
	return *r
}

// prepare reads the row: type, client, location and observation time are
// resolved before the identity locks, outside the transaction of the row
// (database.WithoutTx); a refusal is the result of the row.
func (w *CIWriter) prepare(ctx context.Context, st *ciRun, row *WriteRow) (*ciRow, *RowResult, error) {
	refuse := func(field, code, msg string) (*ciRow, *RowResult, error) {
		res := failed(row.Line, field, code, msg)
		return nil, &res, nil
	}
	v := row.Values
	r := &ciRow{row: row, texts: map[string]string{}, attrs: map[string]Value{}}
	for target, val := range v.Values {
		if key, ok := strings.CutPrefix(target, "attributes."); ok {
			if !val.Empty() {
				r.attrs[key] = val
			}
			continue
		}
		if slices.Contains(ciTextTargets, target) {
			if s := strings.TrimSpace(v.Text(target)); s != "" {
				r.texts[target] = s
			}
			continue
		}
		switch target {
		case CITypeTarget, CITypeIDTarget, CIClientTarget, CIVRFTarget, CILocationTarget, CILocationKindTarget, CIObservedAtTarget:
		default:
			if !strings.HasPrefix(target, relationsTargetPrefix) {
				return refuse(target, CodeInvalidValue, fmt.Sprintf("%s is no field of a CI", target))
			}
			if w.relations == nil && !val.Empty() {
				return refuse(target, CodeInvalidValue, "relation columns cannot be written")
			}
		}
	}
	outside := database.WithoutTx(ctx)
	typeName := strings.TrimSpace(v.Text(CITypeTarget))
	if typeName == "" {
		typeName = strings.TrimSpace(v.Text(CITypeIDTarget))
	}
	if typeName == "" {
		return refuse(CITypeTarget, CodeInvalidValue, "the CI type of the row is required")
	}
	typ, ok, err := st.resolver.Type(outside, typeName)
	if err != nil {
		return nil, nil, err
	}
	if !ok {
		return refuse(CITypeTarget, CodeInvalidValue, fmt.Sprintf("unknown CI type %q", typeName))
	}
	r.typ = typ
	r.item = ingestItem(v, typ.ID)
	if client := r.item.ClientID; client != "" {
		if !api.IsUUID(client) {
			return refuse(CIClientTarget, CodeInvalidValue, "client_id must be a UUID")
		}
		if scope, inScope := database.TenantScopeFromContext(ctx); !inScope || !scopeCovers(scope.Clients, client) {
			return refuse(CIClientTarget, CodeOutOfScope, "the client of the row lies outside the scope of the user who started the import")
		}
	}
	if r.observedAt, err = observedAt(v.Text(CIObservedAtTarget), row.Run.StartedAt); err != nil {
		return refuse(CIObservedAtTarget, CodeInvalidValue, err.Error())
	}
	if r.observedAt.After(time.Now()) {
		return refuse(CIObservedAtTarget, CodeObservedAtInFuture, "the observation time of the row lies in the future")
	}
	if id := r.texts[CILocationIDTarget]; id != "" && !api.IsUUID(id) {
		return refuse(CILocationIDTarget, CodeInvalidValue, "location_id must be a UUID")
	}
	if path := ParsePath(v.Values[CILocationTarget]); len(path) > 0 {
		if w.locations == nil {
			return refuse(CILocationTarget, CodeInvalidValue, "location paths cannot be resolved")
		}
		id, issue := RunLocationResolver(row.Run, w.locations).Resolve(outside, ciLocationKind(v.Text(CILocationKindTarget), len(path)), path, CILocationTarget)
		if issue != nil {
			res := RowResult{Outcome: OutcomeFailed, Issues: []Issue{*issue}}
			return nil, &res, nil
		}
		r.texts[CILocationIDTarget] = id
	}
	return r, nil, nil
}

// ciLocationKind is the kind of the location of a CI row: the mapped kind,
// else the kind of the path's depth from the site down.
func ciLocationKind(kind string, depth int) locations.Kind {
	if k := strings.ToLower(strings.TrimSpace(kind)); k != "" {
		return locations.Kind(k)
	}
	byDepth := []locations.Kind{locations.KindSite, locations.KindBuilding, locations.KindRoom, locations.KindRack}
	return byDepth[min(depth, len(byDepth))-1]
}

// observedAt parses the observation time of a row (RFC 3339 or a date);
// empty is the start of the run.
func observedAt(text string, start time.Time) (time.Time, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		if start.IsZero() {
			return time.Now().UTC(), nil
		}
		return start, nil
	}
	if t, err := time.Parse(time.RFC3339, text); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", text); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, fmt.Errorf("observed_at %q is no RFC 3339 time or date (YYYY-MM-DD)", text)
}

// resurrected holds a row that matches only soft-deleted CIs (CI-13): no CI
// is created, the resurrected_device review of the ingest is opened.
func (w *CIWriter) resurrected(ctx context.Context, st *ciRun, r *ciRow, res *discovery.RecordResolution) RowResult {
	msg := fmt.Sprintf("the row matches the deleted CI(s) %s; no CI is created", strings.Join(res.Resurrected, ", "))
	w.review(ctx, func(ctx context.Context) error {
		_, err := st.resolver.OpenResurrected(ctx, &r.item, res)
		return err
	})
	return RowResult{Outcome: OutcomeSkipped, Message: msg, Issues: []Issue{{Code: CodeResurrectedDevice, Message: msg}}}
}

// review opens a review under a savepoint: a review that fails does not
// fail the row (REC-08, as in the ingest).
func (w *CIWriter) review(ctx context.Context, open func(ctx context.Context) error) {
	if err := database.Savepoint(ctx, open); err != nil {
		slog.WarnContext(ctx, "import: review not opened", "error", err)
	}
}

// create creates the CI of a row without identity match: with
// discovery_source import (is_manual) through ci.Service (field rights,
// validation, unique attributes, max_cis admission in the row's
// transaction), then the provenance of every field and the
// probable_duplicate review of a host name hit.
func (w *CIWriter) create(ctx context.Context, st *ciRun, r *ciRow, res *discovery.RecordResolution) (RowResult, error) {
	line := r.row.Line
	if field := takenIdentity(&r.item, res.Candidates); field != "" {
		return failed(line, field, CodeIdentityTaken, fmt.Sprintf("another CI holds the %s of the row", field)), nil
	}
	if !r.typ.Active {
		return failed(line, CITypeTarget, CodeInvalidValue, "the CI type of the row is deactivated; it gets no new CIs"), nil
	}
	name := r.texts["name"]
	if name == "" {
		return failed(line, "name", CodeInvalidValue, "the name of a new CI is required"), nil
	}
	status := r.texts["status"]
	if status == "" {
		status = ci.StatusActive
	}
	item := &ci.Item{OrganizationID: r.row.Run.OrganizationID, ClientID: r.item.ClientID, CITypeID: r.typ.ID, Name: name, Status: status,
		DiscoverySource: ci.SourceImport, IsManual: ci.IsManualSource(ci.SourceImport)}
	for field, value := range r.texts {
		if p := textField(item, field); p != nil && field != "name" && field != "status" {
			*p = value
		}
	}
	attrs, issues, err := w.typedAttributes(ctx, r, "")
	if err != nil {
		return RowResult{}, err
	}
	if len(issues) > 0 {
		return RowResult{Outcome: OutcomeFailed, Issues: issues}, nil
	}
	item.Attributes = attrs
	if item.Attributes == nil {
		item.Attributes = map[string]any{}
	}
	if err = w.cis.Create(ctx, item); err != nil {
		return w.refusal(line, err)
	}
	for field, value := range writtenValues(r.texts, attrs) {
		wr := &override.Write{OrganizationID: item.OrganizationID, CIID: item.ID, Field: field, Value: value,
			Source: ci.SourceImport, ObservedAt: r.observedAt}
		if err = w.guard.Record(ctx, wr); err != nil {
			return RowResult{}, fmt.Errorf("record provenance of %s: %w", field, err)
		}
	}
	notes := r.notes
	if len(res.Result.ProbableDuplicates) > 0 {
		w.review(ctx, func(ctx context.Context) error {
			_, reviewErr := st.resolver.OpenProbableDuplicate(ctx, item.ID, &r.item, res)
			return reviewErr
		})
		notes = append(notes, Issue{Code: CodeProbableDuplicate,
			Message: fmt.Sprintf("the CI shares its host name or FQDN with %s", strings.Join(res.Result.ProbableDuplicates, ", "))})
	}
	database.AfterCommit(ctx, func(context.Context) { st.created.Add(1) })
	return RowResult{Outcome: OutcomeCreated, Message: item.ID, Issues: notes}, nil
}

// writtenValues are the values a row wrote, by field name of the
// provenance: the typed columns and the attribute keys.
func writtenValues(texts map[string]string, attrs map[string]any) map[string]any {
	out := make(map[string]any, len(texts)+len(attrs))
	for field, value := range texts {
		out[field] = value
	}
	for key, value := range attrs {
		out[key] = value
	}
	return out
}

// update writes a row onto its matched CI: under the CI's row lock (after
// the identity locks) every field is decided by the central write decision
// with source import; the fields it permits go in one authoritative
// update, the provenance of written and overridden fields is recorded and
// a differing value under an override opens override_conflict.
func (w *CIWriter) update(ctx context.Context, st *ciRun, r *ciRow, res *discovery.RecordResolution) (RowResult, error) {
	line, orgID, id := r.row.Line, r.row.Run.OrganizationID, res.Result.MatchedCIID
	// The row lock of the CI before its provenance and overrides are read
	// (CILocker, as in the ingest).
	if err := st.resolver.LockCI(ctx, id); err != nil {
		return RowResult{}, err
	}
	current, err := w.cis.GetByID(ctx, orgID, id)
	if err != nil {
		return RowResult{}, err
	}
	notes := r.notes
	texts := r.texts
	// The client binds the identity; an import does not move a CI to
	// another client.
	if current.AssetID != "" {
		for _, field := range []string{CILocationIDTarget, "serial_number"} {
			if _, ok := texts[field]; ok {
				delete(texts, field)
				notes = append(notes, Issue{Field: field, Code: CodeAssetCanonical,
					Message: "the CI is linked to an asset, which holds its location and serial number"})
			}
		}
	}
	attrs, issues, err := w.typedAttributes(ctx, r, id)
	if err != nil {
		return RowResult{}, err
	}
	if len(issues) > 0 {
		return RowResult{Outcome: OutcomeFailed, Issues: issues}, nil
	}
	removed, err := w.withoutInstanceFields(ctx, orgID, id, attrs)
	if err != nil {
		return RowResult{}, err
	}
	for _, key := range removed {
		notes = append(notes, Issue{Field: "attributes." + key, Code: CodeInstanceField,
			Message: "an instance attribute of the CI is not written by an import"})
	}
	if refusal := w.checkFields(ctx, current, texts, attrs); refusal != nil {
		return failed(line, strings.Join(refusal.Fields, ","), CodeFieldForbidden, refusal.Error()), nil
	}
	req := ci.UpdateRequest{Authoritative: true}
	var record []*override.Write
	var conflicts []*override.Write
	for field, value := range writtenValues(texts, attrs) {
		wr := &override.Write{OrganizationID: orgID, CIID: id, Field: field, Value: value, Source: ci.SourceImport,
			ObservedAt: r.observedAt, Current: currentValue(current, field), FallbackSource: current.DiscoverySource}
		d := w.guard.Decide(ctx, wr)
		switch {
		case d.Reason == override.ReasonError:
			return failed(line, fieldPath(field, texts), CodeProvenanceUnreadable,
				"the provenance or the override of the field cannot be read; nothing is written"), nil
		case d.Write:
			if !sameValue(wr.Current, value) {
				setField(&req, field, value, texts)
			}
			record = append(record, wr)
		case d.Conflict || d.Reason == override.ReasonOverride:
			record = append(record, wr)
			if d.Conflict {
				conflicts = append(conflicts, wr)
			}
			notes = append(notes, Issue{Field: fieldPath(field, texts), Code: CodeOverrideKept,
				Message: "the field keeps its manual override"})
		case d.Reason == override.ReasonInstanceAttribute:
			notes = append(notes, Issue{Field: fieldPath(field, texts), Code: CodeInstanceField,
				Message: "an instance attribute of the CI is not written by an import"})
		case d.Reason == override.ReasonLowerRank || d.Reason == override.ReasonStale:
			notes = append(notes, Issue{Field: fieldPath(field, texts), Code: CodeLowerRank,
				Message: fmt.Sprintf("the field keeps the value of a source of higher rank or a newer observation (%s)", d.Reason)})
		}
	}
	changed := hasChanges(&req)
	if changed {
		if _, err = w.cis.Update(ctx, orgID, id, req); err != nil {
			return w.refusal(line, err)
		}
	}
	for _, wr := range record {
		if err = w.guard.Record(ctx, wr); err != nil {
			return RowResult{}, fmt.Errorf("record provenance of %s: %w", wr.Field, err)
		}
	}
	for _, wr := range conflicts {
		w.discovery.QueueOverrideConflict(ctx, orgID, current, wr.Field, wr.Value, ci.SourceImport)
	}
	sort.Slice(notes, func(i, j int) bool { return notes[i].Field < notes[j].Field })
	if !changed {
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the CI %s is unchanged", id), Issues: notes}, nil
	}
	return RowResult{Outcome: OutcomeUpdated, Message: id, Issues: notes}, nil
}

// withoutInstanceFields removes the instance attributes of the CI from
// attrs (MET-14) and returns their names; without the definitions every
// attribute is removed (fail-closed, like the ingest).
func (w *CIWriter) withoutInstanceFields(ctx context.Context, orgID, id string, attrs map[string]any) ([]string, error) {
	if len(attrs) == 0 {
		return nil, nil
	}
	if w.instance == nil {
		removed := make([]string, 0, len(attrs))
		for key := range attrs {
			removed = append(removed, key)
			delete(attrs, key)
		}
		sort.Strings(removed)
		return removed, nil
	}
	names, err := w.instance.InstanceFieldNames(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return discovery.WithoutInstanceFields(attrs, names), nil
}

// checkFields refuses the row when the starter may not write a field it
// changes on the CI, as PATCH does (RBA-07, WP-265): an unreadable field
// whatever its value, a readable one when the value differs.
func (w *CIWriter) checkFields(ctx context.Context, current *ci.Item, texts map[string]string, attrs map[string]any) *api.FieldForbiddenError {
	policy, err := permission.PolicyFrom(ctx)
	if err != nil || !policy.Restricted(permission.KindCI) {
		return nil
	}
	ref := permission.ObjectRef{ID: current.ID, ClientID: current.ClientID, SiteID: current.SiteID}
	opts := permission.WithInstanceKeys(current.InstanceKeys)
	var changed []string
	for field, value := range writtenValues(texts, attrs) {
		path := fieldPath(field, texts)
		if !permission.Readable(policy, permission.KindCI, current.CITypeID, ref, path, opts) ||
			!sameValue(currentValue(current, field), value) {
			changed = append(changed, path)
		}
	}
	sort.Strings(changed)
	var forbidden *api.FieldForbiddenError
	if errors.As(permission.CheckWrite(policy, permission.KindCI, current.CITypeID, ref, changed, opts), &forbidden) {
		return forbidden
	}
	return nil
}

// fieldPath is the DTO path of a field: a typed column by its name, an
// attribute as attributes.<key>.
func fieldPath(field string, texts map[string]string) string {
	if _, ok := texts[field]; ok {
		return field
	}
	return "attributes." + field
}

// currentValue is the value of a field on the CI.
func currentValue(item *ci.Item, field string) any {
	if p := textField(item, field); p != nil {
		return *p
	}
	return item.Attributes[field]
}

// textField returns the typed column of a CI by its name, nil for an
// attribute.
func textField(item *ci.Item, field string) *string {
	switch field {
	case "name":
		return &item.Name
	case "status":
		return &item.Status
	case "manufacturer":
		return &item.Manufacturer
	case "model":
		return &item.Model
	case "serial_number":
		return &item.SerialNumber
	case "hardware_uuid":
		return &item.HardwareUUID
	case "management_ip":
		return &item.ManagementIP
	case "primary_mac":
		return &item.PrimaryMAC
	case "hostname":
		return &item.Hostname
	case "fqdn":
		return &item.FQDN
	case "os_name":
		return &item.OSName
	case "os_version":
		return &item.OSVersion
	case "firmware_version":
		return &item.FirmwareVersion
	case "sys_object_id":
		return &item.SysObjectID
	case CILocationIDTarget:
		return &item.LocationID
	}
	return nil
}

// setField puts a decided value into the update.
func setField(req *ci.UpdateRequest, field string, value any, texts map[string]string) {
	if _, typed := texts[field]; !typed {
		if req.Attributes == nil {
			req.Attributes = map[string]any{}
		}
		req.Attributes[field] = value
		return
	}
	s := fmt.Sprint(value)
	p := &s
	switch field {
	case "name":
		req.Name = p
	case "status":
		req.Status = p
	case "manufacturer":
		req.Manufacturer = p
	case "model":
		req.Model = p
	case "serial_number":
		req.SerialNumber = p
	case "hardware_uuid":
		req.HardwareUUID = p
	case "management_ip":
		req.ManagementIP = p
	case "primary_mac":
		req.PrimaryMAC = p
	case "hostname":
		req.Hostname = p
	case "fqdn":
		req.FQDN = p
	case "os_name":
		req.OSName = p
	case "os_version":
		req.OSVersion = p
	case "firmware_version":
		req.FirmwareVersion = p
	case "sys_object_id":
		req.SysObjectID = p
	case CILocationIDTarget:
		req.LocationID = p
	}
}

// hasChanges reports whether the update sets anything.
func hasChanges(req *ci.UpdateRequest) bool {
	for _, p := range []*string{req.Name, req.Status, req.Manufacturer, req.Model, req.SerialNumber, req.HardwareUUID,
		req.ManagementIP, req.PrimaryMAC, req.Hostname, req.FQDN, req.OSName, req.OSVersion, req.FirmwareVersion,
		req.SysObjectID, req.LocationID} {
		if p != nil {
			return true
		}
	}
	return len(req.Attributes) > 0
}

// sameValue compares two values in their JSON form.
func sameValue(a, b any) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && bytes.Equal(ja, jb)
}

// typedAttributes types the attribute texts of the row with the field
// definitions of the CI (ciID) or of its type: numbers, booleans, lists
// and JSON; an attribute without definition stays text, the validation of
// ci.Service refuses what does not fit. A text that cannot be typed is an
// issue.
func (w *CIWriter) typedAttributes(ctx context.Context, r *ciRow, ciID string) (map[string]any, []Issue, error) {
	if len(r.attrs) == 0 {
		return nil, nil, nil
	}
	types := map[string]string{}
	if w.fields != nil {
		defs, err := w.fields.ResolveFields(ctx, r.row.Run.OrganizationID, r.typ.ID, ciID)
		if err != nil {
			return nil, nil, err
		}
		for i := range defs {
			types[defs[i].Name] = fieldmeta.CanonicalType(defs[i].DataType)
		}
	}
	out := make(map[string]any, len(r.attrs))
	var issues []Issue
	for key, v := range r.attrs {
		value, err := typedValue(types[key], v)
		if err != nil {
			issues = append(issues, Issue{Line: r.row.Line, Field: "attributes." + key, Code: CodeInvalidValue, Message: err.Error()})
			continue
		}
		out[key] = value
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Field < issues[j].Field })
	return out, issues, nil
}

// typedValue converts the mapped value of an attribute to its data type.
func typedValue(dataType string, v Value) (any, error) {
	text := strings.TrimSpace(v.Text)
	switch dataType {
	case fieldmeta.TypeInteger:
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%q is no integer", text)
		}
		return n, nil
	case fieldmeta.TypeDecimal:
		f, err := strconv.ParseFloat(strings.Replace(text, ",", ".", 1), 64)
		if err != nil {
			return nil, fmt.Errorf("%q is no number", text)
		}
		return f, nil
	case fieldmeta.TypeBoolean:
		switch strings.ToLower(text) {
		case "true", "1", "yes", "ja", "wahr", "x":
			return true, nil
		case "false", "0", "no", "nein", "falsch":
			return false, nil
		}
		return nil, fmt.Errorf("%q is no boolean (true or false)", text)
	case fieldmeta.TypeMultiselect:
		items := v.List
		if !v.IsList {
			items = strings.Split(v.Text, ",")
		}
		list := []any{}
		for _, item := range items {
			if s := strings.TrimSpace(item); s != "" {
				list = append(list, s)
			}
		}
		return list, nil
	case fieldmeta.TypeJSON:
		var out any
		if err := json.Unmarshal([]byte(text), &out); err != nil {
			return nil, fmt.Errorf("the value is no JSON: %v", err)
		}
		return out, nil
	}
	if v.IsList {
		return strings.Join(v.List, ","), nil
	}
	return v.Text, nil
}

// refusal turns the refusal of ci.Service into the issues of the row:
// field rights, validation, unique attributes, max_cis and the identity
// and asset constraints of the database. Any other error is no refusal.
func (w *CIWriter) refusal(line int, err error) (RowResult, error) {
	var forbidden *api.FieldForbiddenError
	var validation *ci.ValidationError
	var unique *ci.UniqueError
	var limit *entitlement.LimitExceededError
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &forbidden):
		return failed(line, strings.Join(forbidden.Fields, ","), CodeFieldForbidden, forbidden.Error()), nil
	case errors.As(err, &validation):
		res := RowResult{Outcome: OutcomeFailed}
		for _, v := range validation.Violations {
			res.Issues = append(res.Issues, Issue{Line: line, Field: v.Field, Code: CodeInvalidValue, Message: v.Detail})
		}
		return res, nil
	case errors.As(err, &unique):
		res := RowResult{Outcome: OutcomeFailed}
		for _, f := range unique.Fields {
			res.Issues = append(res.Issues, Issue{Line: line, Field: "attributes." + f, Code: CodeUniqueTaken,
				Message: "another CI already holds this value of the unique attribute"})
		}
		return res, nil
	case errors.As(err, &limit):
		return failed(line, "", CodeEntitlementLimit, "the organization has reached the CI limit of its plan (max_cis)"), nil
	case errors.As(err, &pgErr) && pgErr.ConstraintName == "ci_location_from_asset":
		return failed(line, CILocationIDTarget, CodeAssetCanonical, "the location of a CI linked to an asset is the asset's"), nil
	case errors.As(err, &pgErr) && pgErr.Code == "23505" && identityConstraints[pgErr.ConstraintName] != "":
		field := identityConstraints[pgErr.ConstraintName]
		return failed(line, field, CodeIdentityTaken, fmt.Sprintf("another CI holds the %s of the row", field)), nil
	case errors.Is(err, ci.ErrNotFound):
		return failed(line, "", CodeOutOfScope, "the CI lies outside the scope of the user who started the import"), nil
	}
	return RowResult{}, err
}

// identityConstraints are the unique indexes of the identity columns.
var identityConstraints = map[string]string{
	"uq_ci_org_serial":        "serial_number",
	"uq_ci_org_hardware_uuid": "hardware_uuid",
	"uq_ci_org_primary_mac":   "primary_mac",
}

// Finish implements Finisher: after a real run that created at least
// ciAnalyzeThreshold CIs the planner statistics are refreshed, as the
// ingest does (database.AnalyzeAfterBulk, WP-834); the run never fails
// for it.
func (w *CIWriter) Finish(ctx context.Context, run *RunInfo) error {
	st := run.State(ciRunKey{}, func() any { return &ciRun{} }).(*ciRun)
	if run.DryRun || w.analyzer == nil || st.created.Load() < ciAnalyzeThreshold {
		return nil
	}
	tables, err := w.analyzer.AnalyzeAfterBulk(database.WithoutTx(ctx))
	if err != nil {
		slog.WarnContext(ctx, "import: analyze after new CIs failed", "job", run.JobID, "error", err)
		return nil
	}
	slog.InfoContext(ctx, "import: statistics refreshed after new CIs", "job", run.JobID, "created", st.created.Load(), "tables", tables)
	return nil
}

// takenIdentity returns the identity column (serial number, hardware UUID,
// primary MAC) of item that one of the live candidates already holds, ""
// when none: the values are unique per organization
// (uq_ci_org_serial, uq_ci_org_hardware_uuid, uq_ci_org_primary_mac). The
// comparison is that of the ingest, which clears such a value of a new CI
// (withoutTakenIdentity); the import refuses the row instead.
func takenIdentity(item *discovery.IngestItem, candidates []ci.Item) string {
	for i := range candidates {
		c := &candidates[i]
		switch {
		case item.SerialNumber != "" && strings.EqualFold(strings.TrimSpace(c.SerialNumber), item.SerialNumber):
			return "serial_number"
		case item.HardwareUUID != "" && strings.EqualFold(c.HardwareUUID, item.HardwareUUID):
			return "hardware_uuid"
		case item.PrimaryMAC != "" && c.PrimaryMAC != "" && normalizedMAC(c.PrimaryMAC) == normalizedMAC(item.PrimaryMAC):
			return "primary_mac"
		}
	}
	return ""
}

// normalizedMAC is a MAC address in lower case without separators.
func normalizedMAC(mac string) string {
	return strings.NewReplacer(":", "", "-", "", ".", "").Replace(strings.ToLower(strings.TrimSpace(mac)))
}
