package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
	"github.com/jackc/pgx/v5/pgxpool"
)

// auditVerifier adapts audit.Verify (which needs the pool) to the
// compliance.AuditVerifier port used by the security report.
type auditVerifier struct {
	pool *pgxpool.Pool
}

func (v auditVerifier) Verify(ctx context.Context, orgID string) (compliance.AuditIntegrity, error) {
	result, err := audit.Verify(ctx, v.pool, orgID)
	if err != nil {
		return compliance.AuditIntegrity{}, err
	}
	return compliance.AuditIntegrity{
		Intact:       result.Intact,
		Checked:      result.Checked,
		BrokenReason: result.BrokenReason,
	}, nil
}

// entitlementLister adapts entitlement.Service to the
// compliance.EntitlementLister port used by the security report.
type entitlementLister struct {
	svc *entitlement.Service
}

func (l entitlementLister) List(ctx context.Context, orgID string) ([]compliance.EntitlementStatus, error) {
	ents, err := l.svc.List(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]compliance.EntitlementStatus, len(ents))
	for i := range ents {
		out[i] = compliance.EntitlementStatus{FeatureKey: ents[i].FeatureKey, Enabled: ents[i].Enabled}
	}
	return out, nil
}

// assetCreator adapts asset.Repository to the movement.AssetCreator port used
// by the quantity→serialized conversion endpoint (spec §6).
type assetCreator struct {
	repo asset.Repository
}

func (c assetCreator) Create(ctx context.Context, ref *movement.AssetRef) (string, error) {
	a := &asset.Asset{
		OrganizationID: ref.OrganizationID,
		ClientID:       ref.ClientID,
		AssetTag:       ref.AssetTag,
		Name:           ref.Name,
		Category:       ref.Category,
		Status:         "in_stock",
		SerialNumber:   ref.SerialNumber,
		// The canonical location column (WP-271); the currency is the
		// organization's (trigger asset_default_currency).
		LocationID:   ref.LocationID,
		CustomFields: map[string]any{},
	}
	if err := c.repo.Create(ctx, a); err != nil {
		return "", err
	}
	return a.ID, nil
}

// overrideProvenance adapts override.Repository to the
// discovery.ProvenanceRecorder port (spec §13).
type overrideProvenance struct {
	repo override.Repository
}

func (a overrideProvenance) RecordDiscovered(ctx context.Context, orgID, ciID, fieldName string, value any, source string, observedAt time.Time) (*discovery.FieldProvenance, error) {
	fv, err := a.repo.RecordDiscovered(ctx, orgID, ciID, fieldName, value, source, observedAt)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

// RecordDiscoveredMany records the fields of a new CI in one statement when
// the repository can (NFR-03).
func (a overrideProvenance) RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error {
	if many, ok := a.repo.(interface {
		RecordDiscoveredMany(ctx context.Context, orgID, ciID string, values map[string]any, source string, observedAt time.Time) error
	}); ok {
		return many.RecordDiscoveredMany(ctx, orgID, ciID, values, source, observedAt)
	}
	for field, value := range values {
		if _, err := a.repo.RecordDiscovered(ctx, orgID, ciID, field, value, source, observedAt); err != nil {
			return err
		}
	}
	return nil
}

// RecordDiscoveredBatch records the fields of the new CIs of an ingest batch
// in one statement when the repository can (NFR-03, WP-333), else CI by CI.
func (a overrideProvenance) RecordDiscoveredBatch(ctx context.Context, orgID string, batch []override.DiscoveredValues) error {
	if many, ok := a.repo.(interface {
		RecordDiscoveredBatch(ctx context.Context, orgID string, batch []override.DiscoveredValues) error
	}); ok {
		return many.RecordDiscoveredBatch(ctx, orgID, batch)
	}
	for _, b := range batch {
		if err := a.RecordDiscoveredMany(ctx, orgID, b.CIID, b.Values, b.Source, b.ObservedAt); err != nil {
			return err
		}
	}
	return nil
}

// ClearOverride accepts the observed value of an override_conflict (REC-12).
func (a overrideProvenance) ClearOverride(ctx context.Context, orgID, ciID, field string) error {
	_, err := a.repo.ClearOverride(ctx, orgID, ciID, field)
	return err
}

func (a overrideProvenance) Decide(ctx context.Context, w *override.Write) override.Decision {
	return override.DecideAutomatedWrite(ctx, a.repo, w)
}

// edgeLicense adapts the entitlement service to the discovery license check
// of the collectors' edge data path, of their scope channel and of the
// resolution of scope_suggested items (F-400).
type edgeLicense struct{ svc *entitlement.Service }

// DiscoveryLicense returns the discovery license check of svc for
// discovery.Handler.WithEdgeLicense; the scope channel worker of
// cmd/server uses it as the router does.
func DiscoveryLicense(svc *entitlement.Service) discovery.EdgeLicense {
	return edgeLicense{svc: svc}
}

func (l edgeLicense) DiscoveryLicense(ctx context.Context, orgID string) (state, problemType string, err error) {
	ent, status, err := l.svc.Status(ctx, orgID, entitlement.FeatureDiscovery)
	if err != nil {
		return "", "", err
	}
	switch status {
	case entitlement.StatusActive:
		return discovery.LicenseActive, "", nil
	case entitlement.StatusExpired:
		expired := &entitlement.LicenseExpiredError{}
		if ent.ValidUntil != nil {
			expired.ValidUntil = *ent.ValidUntil
		}
		return discovery.LicenseExpired, expired.ProblemType(), nil
	default:
		return discovery.LicenseNotEntitled, (&entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureDiscovery}).ProblemType(), nil
	}
}

// ciLookup adapts ci.Repository to the asset.CILookup port (spec §4: the
// asset reads the linked CI's technical identity read-only).
type ciLookup struct {
	repo ci.Repository
}

func (l ciLookup) GetByID(ctx context.Context, orgID, id string) (*asset.CIRef, error) {
	item, err := l.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	return &asset.CIRef{
		ID:              item.ID,
		Name:            item.Name,
		Status:          item.Status,
		Hostname:        item.Hostname,
		FQDN:            item.FQDN,
		ManagementIP:    item.ManagementIP,
		Manufacturer:    item.Manufacturer,
		Model:           item.Model,
		SerialNumber:    item.SerialNumber,
		OSName:          item.OSName,
		OSVersion:       item.OSVersion,
		DiscoverySource: item.DiscoverySource,
	}, nil
}

// reservationLookup adapts reservation.Repository to the
// asset.ReservationLookup port (API-06, E-38, WP-750): the active
// reservation of an asset in GET /api/v1/assets/{id}, under the caller's
// scope; a serial asset has at most one (idx_reservation_one_active_per_asset).
type reservationLookup struct {
	repo reservation.Repository
}

func (l reservationLookup) ActiveReservation(ctx context.Context, orgID, assetID string) (*asset.ReservationRef, error) {
	active, err := l.repo.ActiveForItem(ctx, orgID, "asset", assetID)
	if err != nil || len(active) == 0 {
		return nil, err
	}
	res := &active[0]
	return &asset.ReservationRef{ID: res.ID, ReservedUntil: res.ReservedUntil, ExpiresAt: res.ExpiresAt,
		AssigneeID: res.AssigneeID, ProjectRef: res.ProjectRef}, nil
}

// ciParentAssetLookup adapts asset.Repository to the ci.ParentAssetLookup
// port of GET /cis/{id}/parent (spec §5, WP-742: a child CI displays the
// parent asset's shared inventory identity read-only instead of duplicating
// it). It is the mirror image of ciLookup, which projects the opposite
// direction. ParentAssetRef keeps the old names and the float purchase cost
// of the deprecated path (API-01); the decimal string of the asset is
// converted here (an empty one is 0).
type ciParentAssetLookup struct {
	repo asset.Repository
}

func (l ciParentAssetLookup) GetByID(ctx context.Context, orgID, id string) (*ci.ParentAssetRef, error) {
	a, err := l.repo.GetByID(ctx, orgID, id)
	if err != nil {
		return nil, err
	}
	var purchaseCost float64
	if a.PurchaseCost != "" {
		if purchaseCost, err = strconv.ParseFloat(a.PurchaseCost, 64); err != nil {
			return nil, err
		}
	}
	return &ci.ParentAssetRef{
		ID:            a.ID,
		Name:          a.Name,
		AssetTag:      a.AssetTag,
		Status:        a.Status,
		SerialNumber:  a.SerialNumber,
		Barcode:       a.Barcode,
		RFIDTag:       a.RFIDTag,
		PurchaseDate:  a.PurchaseDate,
		PurchaseCost:  purchaseCost,
		Currency:      a.Currency,
		Supplier:      a.Supplier,
		InvoiceNumber: a.InvoiceNumber,
		WarrantyEnd:   a.WarrantyEnd,
		Location:      a.Location,
	}, nil
}

// bffSources hands the read models of topology, rack layout, search, the
// review inbox and jobs (GQL-02), those of the asset detail (asset,
// movement ledger, documents; GQL-03) and those of the CI aggregates
// (WP-792: the history timeline, the metric store, the resolved forms with
// the field policy of formPolicy) to the GraphQL BFF. Without the job
// model (no database) the jobs query is unavailable, without a form source
// (in-memory CI types) the form.
func bffSources(repos *Repositories, topo *topology.Handler, jobSource graphqlbff.JobSource,
	formPolicy citype.FormPolicyFactory) *graphqlbff.Sources {
	sources := &graphqlbff.Sources{Topology: topo, Racks: repos.Rack, Search: repos.Search, Jobs: jobSource,
		Assets: repos.Asset, Movements: repos.Movement, Documents: repos.Document}
	if reviewSource, ok := repos.Discovery.(graphqlbff.ReviewSource); ok {
		sources.Reviews = reviewSource
	}
	if timeline, ok := repos.History.(history.CITimeline); ok {
		sources.History = timeline
	}
	if repos.Metrics != nil {
		sources.Metrics = repos.Metrics
	}
	if formSource, ok := repos.CIType.(citype.FormSource); ok {
		sources.Forms = bffForms{source: formSource, policy: formPolicy}
	}
	// The P3 read views at the CI (WP-1136): the batch reads of the
	// PostgreSQL repositories; the memory profile has none.
	if findings, ok := repos.Security.(graphqlbff.FindingBatchSource); ok {
		sources.Findings = findings
	}
	if results, ok := repos.Compliance.(graphqlbff.ComplianceBatchSource); ok {
		sources.Compliance = results
	}
	if runs, ok := repos.Workflow.(graphqlbff.WorkflowRunBatchSource); ok {
		sources.WorkflowRuns = runs
	}
	return sources
}

// bffForms resolves the form of ciForm (WP-792) as GET
// /api/v1/cis/{id}/form does: per request a citype.FormResolver over the
// repository with the field policy of the FormPolicyFactory (WP-727), the
// policy of the preview role in a preview. A CI or role that is no UUID
// gets NoopFieldPolicy: the resolution answers not found before it
// arranges a field.
type bffForms struct {
	source citype.FormSource
	policy citype.FormPolicyFactory
}

func (f bffForms) ResolveForm(ctx context.Context, orgID string, req citype.FormRequest) (*citype.ResolvedForm, error) {
	var policy citype.FieldPolicy = citype.NoopFieldPolicy{}
	if f.policy != nil && api.IsUUID(req.CIID) && (!req.Preview || req.RoleID == "" || api.IsUUID(req.RoleID)) {
		subject := citype.FormPolicySubject{CIID: req.CIID}
		if req.Preview {
			subject.PreviewRoleID = req.RoleID
		}
		p, err := f.policy(ctx, orgID, subject)
		if err != nil {
			return nil, err
		}
		policy = p
	}
	return citype.NewFormResolver(f.source, policy).ResolveForm(ctx, orgID, req)
}

// CIBulkDelegates wires the delegating actions of POST /api/v1/cis/bulk
// (WP-780) with adapters onto the packages they belong to: the manual
// edges of the relationship handler (CreateManual, DeleteWithPolicy), the
// transactional lifecycle transition (WP-724), the export jobs (export
// imports ci, so only this adapter reaches it) and the entitlements. A nil
// part leaves its port nil: its action answers 503.
func CIBulkDelegates(rels *relationship.Handler, relRepo relationship.Repository, transitions *lifecycle.Service,
	resolver lifecycle.EntityResolver, exports *export.JobHandler, features *entitlement.Service) ci.BulkDelegates {
	var d ci.BulkDelegates
	if rels != nil && relRepo != nil {
		d.Relationships = bulkRelationships{handler: rels, repo: relRepo}
	}
	if transitions != nil && resolver != nil {
		d.Lifecycle = bulkLifecycle{svc: transitions, resolver: resolver}
	}
	if exports != nil {
		d.Exporter = bulkExports{jobs: exports}
	}
	if features != nil {
		d.Entitlements = features
	}
	return d
}

// bulkRelationships adapts the relationship handler to ci.BulkRelationships.
type bulkRelationships struct {
	handler *relationship.Handler
	repo    relationship.Repository
}

func (b bulkRelationships) CheckRelType(ctx context.Context, orgID, relType string) error {
	return b.handler.ManualRelType(ctx, orgID, relType)
}

// ends returns source and target of the edge.
func ends(edge *ci.BulkEdge) (source, target string) {
	if edge.Incoming {
		return edge.OtherID, edge.CIID
	}
	return edge.CIID, edge.OtherID
}

// AddRelationship creates the edge like POST /relationships with the run's
// author as actor. The other end the caller cannot see, or whose type the
// relationship type does not allow, is 422 on target_ci_id; the CI itself
// not visible is 404, its type not allowed or the cardinality 422 on
// rel_type; an existing edge (uq_ci_relationship_edge) stays a database
// conflict (409).
func (b bulkRelationships) AddRelationship(ctx context.Context, orgID string, edge *ci.BulkEdge) error {
	source, target := ends(edge)
	// Under a bulk job the edge is a change set row of the CI (WP-783).
	ctx = relationship.WithBulkEdge(ctx, &relationship.BulkEdgeContext{EntityID: edge.CIID})
	_, err := b.handler.CreateManual(ctx, orgID, bulkActor(edge.Actor), relationship.CreateRequest{SourceCIID: source, TargetCIID: target,
		RelType: edge.RelType})
	return addOutcome(err, edge.Incoming)
}

// bulkActor is the actor of an edge write of a bulk run by author.
func bulkActor(author string) relationship.Actor {
	if author == "" {
		return relationship.Actor{ID: "api", Type: "api_key"}
	}
	return relationship.Actor{ID: author, Type: "user"}
}

// addOutcome maps the error of an edge creation onto the outcome of the
// CI; incoming: the CI is the target of the edge.
func addOutcome(err error, incoming bool) error {
	if err == nil {
		return nil
	}
	otherMissing, ownMissing := relationship.ErrTargetCINotFound, relationship.ErrSourceCINotFound
	otherType, ownType := relationship.ErrTargetCITypeNotAllowed, relationship.ErrSourceCITypeNotAllowed
	if incoming {
		otherMissing, ownMissing = ownMissing, otherMissing
		otherType, ownType = ownType, otherType
	}
	var refused *relationship.RequestError
	switch {
	case errors.Is(err, otherMissing):
		return &ci.BulkItemError{Status: http.StatusUnprocessableEntity, Detail: "target CI not found", Fields: []string{"target_ci_id"}}
	case errors.Is(err, ownMissing):
		return ci.ErrNotFound
	case errors.Is(err, otherType):
		return &ci.BulkItemError{Status: http.StatusUnprocessableEntity, Detail: otherType.Error(), Fields: []string{"target_ci_id"}}
	case errors.Is(err, ownType), errors.Is(err, relationship.ErrCardinalityExceeded):
		detail := relationship.ErrCardinalityExceeded.Error()
		if errors.Is(err, ownType) {
			detail = ownType.Error()
		}
		return &ci.BulkItemError{Status: http.StatusUnprocessableEntity, Detail: detail, Fields: []string{"rel_type"}}
	case errors.As(err, &refused):
		return &ci.BulkItemError{Status: refused.Status, Detail: refused.Error(), Fields: refused.Fields()}
	}
	return err
}

// RemoveRelationship deletes the edge of the CI like DELETE
// /relationships/{id} without force: a discovery edge is 409
// (ErrForceRequired), a missing edge 404. Of several edges between the
// two CIs with the type (interfaces), the one without interfaces goes.
func (b bulkRelationships) RemoveRelationship(ctx context.Context, orgID string, edge *ci.BulkEdge) error {
	source, target := ends(edge)
	// Under a bulk job the deleted edge is a change set row of the CI
	// (WP-783).
	ctx = relationship.WithBulkEdge(ctx, &relationship.BulkEdgeContext{EntityID: edge.CIID})
	rels, _, err := b.repo.ListFiltered(ctx, orgID, relationship.ListFilter{SourceCIID: source, TargetCIID: target, RelType: edge.RelType},
		api.PaginationParams{Limit: 100})
	if err != nil {
		return err
	}
	if len(rels) == 0 {
		return &ci.BulkItemError{Status: http.StatusNotFound, Detail: "relationship not found"}
	}
	pick := slices.IndexFunc(rels, func(r relationship.Relationship) bool {
		return r.SourceInterfaceID == "" && r.TargetInterfaceID == ""
	})
	pick = max(pick, 0)
	if deleter, ok := b.repo.(relationship.PolicyDeleter); ok {
		actor := relationship.Actor{ID: edge.Actor, Type: "user"}
		if !api.IsUUID(actor.ID) {
			actor.ID = ""
		}
		err = deleter.DeleteWithPolicy(ctx, orgID, rels[pick].ID, relationship.DeleteOptions{Actor: actor})
	} else {
		err = b.repo.Delete(ctx, orgID, rels[pick].ID)
	}
	switch {
	case errors.Is(err, relationship.ErrForceRequired):
		return &ci.BulkItemError{Status: http.StatusConflict, Detail: err.Error()}
	case errors.Is(err, api.ErrNotFound):
		return &ci.BulkItemError{Status: http.StatusNotFound, Detail: "relationship not found"}
	}
	return err
}

// RevertRelationship implements ci.BulkRelationshipReverter (WP-783): the
// edge of add_relationship is deleted like DELETE /relationships/{id}
// without force, only as the bulk created it (409 otherwise, 404 when it
// is gone); the edge of remove_relationship is created again like POST
// /relationships with its type, ends, origin, verification, notes and
// attributes (the provenance is the revert's); an equal edge that exists
// meanwhile (uq_ci_relationship_edge) is a database conflict (409). The
// write marks rev.Marks reverted in its transaction.
func (b bulkRelationships) RevertRelationship(ctx context.Context, orgID string, rev *ci.BulkEdgeRevert) error {
	var edge relationship.Relationship
	if err := json.Unmarshal(rev.Edge, &edge); err != nil || edge.SourceCIID == "" || edge.TargetCIID == "" {
		return &ci.BulkItemError{Status: http.StatusUnprocessableEntity, Detail: "the change set holds no relationship"}
	}
	if rev.Op == jobs.OpRemove {
		attributes := maps.Clone(edge.Attributes)
		delete(attributes, "provenance")
		recreate := relationship.WithBulkEdge(ctx, &relationship.BulkEdgeContext{Marks: rev.Marks})
		_, err := b.handler.CreateManual(recreate, orgID, bulkActor(rev.Actor), relationship.CreateRequest{SourceCIID: edge.SourceCIID,
			TargetCIID: edge.TargetCIID, RelType: edge.RelType, Attributes: attributes, Source: edge.Source, Confidence: edge.Confidence,
			VerificationState: edge.VerificationState, SourceSystem: edge.SourceSystem, Notes: edge.Notes,
			SourceInterfaceID: edge.SourceInterfaceID, TargetInterfaceID: edge.TargetInterfaceID})
		return addOutcome(err, edge.TargetCIID == rev.CIID && edge.SourceCIID != rev.CIID)
	}
	deleter, ok := b.repo.(relationship.PolicyDeleter)
	if !ok || edge.ID == "" {
		return ci.ErrBulkActionUnavailable
	}
	actor := bulkActor(rev.Actor)
	if !api.IsUUID(actor.ID) {
		actor.ID = ""
	}
	remove := relationship.WithBulkEdge(ctx, &relationship.BulkEdgeContext{Expect: &edge, Marks: rev.Marks})
	err := deleter.DeleteWithPolicy(remove, orgID, edge.ID, relationship.DeleteOptions{Actor: actor})
	switch {
	case errors.Is(err, relationship.ErrEdgeChanged), errors.Is(err, relationship.ErrForceRequired):
		return &ci.BulkItemError{Status: http.StatusConflict, Detail: relationship.ErrEdgeChanged.Error()}
	case errors.Is(err, api.ErrNotFound):
		return &ci.BulkItemError{Status: http.StatusNotFound, Detail: "relationship not found"}
	}
	return err
}

// bulkLifecycle adapts the lifecycle service to ci.BulkLifecycle: the
// effective model of the CI (EntityResolver, here without the HTTP request
// of a route: a request carrying ctx) and the transactional transition.
type bulkLifecycle struct {
	svc      *lifecycle.Service
	resolver lifecycle.EntityResolver
}

// noLifecycleModel is the outcome of a CI whose type has no model: 422 on
// to_state like the single transition.
func noLifecycleModel() error {
	return &ci.BulkItemError{Status: http.StatusUnprocessableEntity, Detail: lifecycle.ErrNoLifecycleModel.Error(), Fields: []string{"to_state"}}
}

func (b bulkLifecycle) Transition(ctx context.Context, orgID, ciID string, req *ci.BulkTransition) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/cis/"+ciID+"/lifecycle-transitions", http.NoBody)
	if err != nil {
		return err
	}
	modelID, err := b.resolver.LifecycleModelFor(r, orgID, "ci", ciID)
	switch {
	case errors.Is(err, lifecycle.ErrNoLifecycleModel):
		return noLifecycleModel()
	case err != nil:
		return ci.ErrNotFound
	}
	_, err = b.svc.Transition(ctx, orgID, "ci", ciID, modelID, lifecycle.TransitionRequest{ToState: req.ToState,
		FromState: req.FromState, Context: req.Context, Reason: req.Reason})
	return transitionOutcome(err)
}

// RevertTransition implements ci.BulkLifecycleReverter (WP-783): the
// transition back runs like Transition, with the guards of the model, and
// marks the change set rows in its transaction.
func (b bulkLifecycle) RevertTransition(ctx context.Context, orgID, ciID string, req *ci.BulkTransition, marks []jobs.ChangeMark) error {
	return b.Transition(lifecycle.WithRevertMarks(ctx, marks), orgID, ciID, req)
}

// transitionOutcome maps a refused transition onto the outcome of the id
// with the problem types of the single transition (WP-724): the guard with
// the fields of its violations (and the missing references of a system
// guard), a missing edge invalid-transition on to_state, another current
// state 409 on from_state.
func transitionOutcome(err error) error {
	var guard *lifecycle.GuardError
	var invalid *lifecycle.InvalidTransitionError
	var conflict *lifecycle.StateConflictError
	switch {
	case err == nil:
		return nil
	case errors.As(err, &guard):
		fields := make([]string, 0, len(guard.Violations)+len(guard.Missing))
		for _, v := range guard.Violations {
			if !slices.Contains(fields, v.Field) {
				fields = append(fields, v.Field)
			}
		}
		for _, missing := range guard.Missing {
			if !slices.Contains(fields, missing) {
				fields = append(fields, missing)
			}
		}
		return &ci.BulkItemError{Status: http.StatusUnprocessableEntity, Detail: guard.Error(), Problem: lifecycle.ProblemLifecycleGuard, Fields: fields}
	case errors.As(err, &invalid):
		return &ci.BulkItemError{Status: http.StatusUnprocessableEntity, Detail: invalid.Error(), Problem: lifecycle.ProblemInvalidTransition,
			Fields: []string{"to_state"}}
	case errors.As(err, &conflict):
		return &ci.BulkItemError{Status: http.StatusConflict, Detail: conflict.Error(), Problem: lifecycle.ProblemConflict,
			Fields: []string{"from_state"}}
	case errors.Is(err, lifecycle.ErrNoLifecycleModel):
		return noLifecycleModel()
	case err.Error() == "not found":
		return ci.ErrNotFound
	}
	return err
}

// bulkExports adapts the export jobs to ci.BulkExporter: one job of the
// ids like POST /api/v1/exports with filters.ids (export.JobHandler.Enqueue).
type bulkExports struct {
	jobs *export.JobHandler
}

func (b bulkExports) CreateExport(ctx context.Context, orgID, format string, ids []string) (*ci.BulkExportJob, error) {
	req := export.CreateJobRequest{Format: format, Filters: export.JobFilters{IDs: ids}}
	if err := req.Validate(); err != nil {
		return nil, &api.FieldError{Field: "ids", Message: fmt.Sprintf("an export takes 1 to %d CI ids", export.MaxExportIDs)}
	}
	job, err := b.jobs.Enqueue(ctx, orgID, &req)
	switch {
	case errors.Is(err, export.ErrExportUnavailable):
		return nil, ci.ErrBulkActionUnavailable
	case err != nil:
		return nil, err
	}
	return &ci.BulkExportJob{ID: job.ID, Status: job.Status}, nil
}

// AssetBulkExporter wires the action export of POST /api/v1/assets/bulk
// (WP-782): one export job of the asset ids through the creation of POST
// /api/v1/exports (export.JobHandler.Enqueue, WP-780) with filters.entity
// asset and filters.ids. nil without export jobs: export stays unknown.
func AssetBulkExporter(exports *export.JobHandler) asset.BulkExporter {
	if exports == nil {
		return nil
	}
	return assetExports{jobs: exports}
}

// assetExports adapts the export jobs to asset.BulkExporter.
type assetExports struct {
	jobs *export.JobHandler
}

func (a assetExports) CreateExport(ctx context.Context, orgID, format string, ids []string) (*ci.BulkExportJob, error) {
	req := export.CreateJobRequest{Format: format, Filters: export.JobFilters{Entity: export.EntityAsset, IDs: ids}}
	if err := req.Validate(); err != nil {
		return nil, &api.FieldError{Field: "ids", Message: fmt.Sprintf("an export takes 1 to %d asset ids", export.MaxExportIDs)}
	}
	job, err := a.jobs.Enqueue(ctx, orgID, &req)
	switch {
	case errors.Is(err, export.ErrExportUnavailable):
		return nil, ci.ErrBulkActionUnavailable
	case err != nil:
		return nil, err
	}
	return &ci.BulkExportJob{ID: job.ID, Status: job.Status}, nil
}

// AssetBulkDelegates wires the delegating actions of POST
// /api/v1/assets/bulk (WP-781) onto the services of their single
// operations: the transfer of the movement service (WP-274), the
// assignment service (WP-275) and the transactional lifecycle transition
// with the system guards (WP-279). The exporter is AssetBulkExporter
// (WP-782). A nil part leaves its port nil: its action answers 503.
func AssetBulkDelegates(movements *movement.Service, assignments *assignment.Service, transitions *lifecycle.Service,
	resolver lifecycle.EntityResolver, features *entitlement.Service) asset.BulkDelegates {
	var d asset.BulkDelegates
	if movements != nil {
		d.Movements = assetMovements{svc: movements}
	}
	if assignments != nil {
		d.Assignments = assetAssignments{svc: assignments}
	}
	if transitions != nil && resolver != nil {
		d.Lifecycle = assetLifecycle{svc: transitions, resolver: resolver}
	}
	if features != nil {
		d.Entitlements = features
	}
	return d
}

// assetMovements adapts the movement service to asset.BulkMovements.
type assetMovements struct {
	svc *movement.Service
}

// Transfer books the transfer like POST /api/v1/stock-movements with an
// asset: lock, location checks (a location the caller cannot see is 422 on
// to_location_id), the row of the ledger, the location of the asset and
// its CIs, stock.movement.
func (a assetMovements) Transfer(ctx context.Context, orgID, assetID, toLocationID, actor string) error {
	err := a.svc.Record(ctx, &movement.Movement{OrganizationID: orgID, ItemKind: "asset", AssetID: assetID,
		MovementType: "transfer", ToLocationID: toLocationID, ActorID: actor})
	if errors.Is(err, movement.ErrAssetNotFound) {
		return asset.ErrNotFound
	}
	return err
}

// assetAssignments adapts the assignment service to asset.BulkAssignments.
type assetAssignments struct {
	svc *assignment.Service
}

// Assign assigns the asset like POST /api/v1/assets/{id}/assign: an asset
// with a holder or reserved for another is 409 with the problem type of
// the single route.
func (a assetAssignments) Assign(ctx context.Context, orgID, assetID string, in *asset.BulkAssignment) error {
	_, err := a.svc.Assign(ctx, orgID, &assignment.AssignInput{AssetID: assetID,
		Holder: assignment.Holder{UserID: in.UserID, TeamID: in.TeamID}, DueAt: in.DueAt, Notes: in.Notes})
	switch {
	case err == nil:
		return nil
	case errors.Is(err, assignment.ErrAssetNotFound), errors.Is(err, movement.ErrAssetNotFound), errors.Is(err, api.ErrNotFound):
		return asset.ErrNotFound
	case errors.Is(err, assignment.ErrAlreadyAssigned):
		return &ci.BulkItemError{Status: http.StatusConflict, Detail: assignment.ErrAlreadyAssigned.Error(), Problem: assignment.ProblemAssetAssigned}
	case errors.Is(err, assignment.ErrAssetReserved):
		return &ci.BulkItemError{Status: http.StatusConflict, Detail: assignment.ErrAssetReserved.Error(), Problem: assignment.ProblemAssetReserved}
	}
	return err
}

// ReturnAsset implements asset.BulkAssignmentReverter (WP-784): the return
// like POST /api/v1/assets/{id}/return with expected_holder holder; another
// holder or none is 409 with the problem type of the single route.
func (a assetAssignments) ReturnAsset(ctx context.Context, orgID, assetID, holder string) error {
	_, err := a.svc.Return(ctx, orgID, &assignment.ReturnInput{AssetID: assetID, ExpectedHolder: holder, Notes: "bulk.revert"})
	return assignmentRevertOutcome(err)
}

// Reassign implements asset.BulkAssignmentReverter (WP-784): the transfer
// like POST /api/v1/assets/{id}/transfer from holder to the holder before
// the bulk; errors as ReturnAsset.
func (a assetAssignments) Reassign(ctx context.Context, orgID, assetID, holder string, to *asset.BulkAssignment) error {
	_, err := a.svc.Transfer(ctx, orgID, &assignment.TransferInput{AssetID: assetID, ExpectedHolder: holder,
		Holder: assignment.Holder{UserID: to.UserID, TeamID: to.TeamID}, DueAt: to.DueAt, Notes: to.Notes})
	return assignmentRevertOutcome(err)
}

// assignmentRevertOutcome maps a refused return or transfer of a revert
// onto the outcome of the asset.
func assignmentRevertOutcome(err error) error {
	switch {
	case errors.Is(err, assignment.ErrHolderMismatch):
		return &ci.BulkItemError{Status: http.StatusConflict, Detail: "the asset was assigned otherwise since the bulk",
			Problem: assignment.ProblemHolderMismatch, Fields: []string{movement.ChangeFieldHolder}}
	case errors.Is(err, assignment.ErrNotAssigned):
		return &ci.BulkItemError{Status: http.StatusConflict, Detail: "the asset was returned since the bulk",
			Problem: assignment.ProblemAssetNotAssigned, Fields: []string{movement.ChangeFieldHolder}}
	case errors.Is(err, assignment.ErrAssetNotFound), errors.Is(err, movement.ErrAssetNotFound):
		return asset.ErrNotFound
	}
	return err
}

// assetLifecycle adapts the lifecycle service to asset.BulkLifecycle: the
// effective model of the asset and the transition, both in the
// transaction of ctx when it carries one (archive).
type assetLifecycle struct {
	svc      *lifecycle.Service
	resolver lifecycle.EntityResolver
}

func (a assetLifecycle) Transition(ctx context.Context, orgID, assetID string, req *ci.BulkTransition) error {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/assets/"+assetID+"/lifecycle-transitions", http.NoBody)
	if err != nil {
		return err
	}
	modelID, err := a.resolver.LifecycleModelFor(r, orgID, "asset", assetID)
	switch {
	case errors.Is(err, lifecycle.ErrNoLifecycleModel):
		return noLifecycleModel()
	case err != nil:
		return asset.ErrNotFound
	}
	_, err = a.svc.Transition(ctx, orgID, "asset", assetID, modelID, lifecycle.TransitionRequest{ToState: req.ToState,
		FromState: req.FromState, Context: req.Context, Reason: req.Reason})
	if out := transitionOutcome(err); !errors.Is(out, ci.ErrNotFound) {
		return out
	}
	return asset.ErrNotFound
}

// RevertTransition implements ci.BulkLifecycleReverter for assets
// (WP-784): the transition back runs like Transition, with the guards of
// the model and the system guards (WP-279), and marks the change set rows
// in its transaction.
func (a assetLifecycle) RevertTransition(ctx context.Context, orgID, assetID string, req *ci.BulkTransition, marks []jobs.ChangeMark) error {
	return a.Transition(lifecycle.WithRevertMarks(ctx, marks), orgID, assetID, req)
}
