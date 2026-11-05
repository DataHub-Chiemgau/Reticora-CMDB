package importer

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// The rows of assets, contracts, consumables and relationships (WP-776,
// IMP-IO-01): every entity has its identity key (asset: inventory_no,
// else serial_number; contract: number; consumable: sku; relationship:
// source, target, type and interfaces), the writers write through the
// repositories of their domain in the transaction of the row, under the
// scope and the field permissions of the user who started the run, and
// write the audit entry of the row themselves, since these repositories
// leave it to the route audit. Override protection and rank 95 are the
// CI fields' only (WP-302).

// Codes of the rows of assets, contracts, consumables and relationships.
const (
	// CodeReferenceNotFound: a CI the row names (an end of a
	// relationship, the primary CI of an asset) does not exist or lies
	// outside the scope of the user who started the import.
	CodeReferenceNotFound = "reference_not_found"
	// CodeReadOnlyField: the column has its own write path (asset
	// location and lifecycle state, holder, tags, stock); it is not
	// written, the other fields of the row are.
	CodeReadOnlyField = "read_only_field"
)

// EntityStore is the database side of the writers of assets, contracts,
// consumables and relationships: the lookups by identity key under the
// scope of the run in the transaction of the row, and the audit entry of a
// row as the last write of that transaction (database.BeforeCommit, K20).
// PGRepository implements it.
type EntityStore interface {
	// FindAssets returns the ids of the assets whose inventory_no (key
	// inventory_no) or serial number in normal form (key serial_number)
	// is value.
	FindAssets(ctx context.Context, orgID, key, value string) ([]string, error)
	FindContracts(ctx context.Context, orgID, number string) ([]string, error)
	FindConsumables(ctx context.Context, orgID, sku string) ([]string, error)
	// FindRelationships returns the ids of the stored edges with the key
	// of uq_ci_relationship_edge.
	FindRelationships(ctx context.Context, orgID string, edge *EdgeKey) ([]string, error)
	// RelationshipTypeID returns the id of the relationship type key as
	// the organization sees it, "" when there is none.
	RelationshipTypeID(ctx context.Context, orgID, key string) (string, error)
	// RecordRowAudit registers the audit entry of a row as the last write
	// of its transaction.
	RecordRowAudit(ctx context.Context, entry *audit.Entry) error
}

var _ EntityStore = (*PGRepository)(nil)

// findLimit bounds the ids of an identity lookup: more than one is
// ambiguous.
const findLimit = 10

// findIDs runs an identity lookup in the transaction of the row (row level
// security of the run's scope).
func (r *PGRepository) findIDs(ctx context.Context, orgID, query string, args ...any) ([]string, error) {
	var ids []string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, query, args...)
		if err != nil {
			return err
		}
		ids, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	return ids, err
}

// FindAssets implements EntityStore.
func (r *PGRepository) FindAssets(ctx context.Context, orgID, key, value string) ([]string, error) {
	switch key {
	case "inventory_no":
		return r.findIDs(ctx, orgID, `SELECT id::text FROM asset WHERE organization_id = $1 AND inventory_no = $2 ORDER BY id LIMIT $3`,
			orgID, value, findLimit)
	case "serial_number":
		// The normal form of the serial number (000241, lower(btrim())).
		return r.findIDs(ctx, orgID, `SELECT id::text FROM asset WHERE organization_id = $1 AND serial_number_norm = $2 ORDER BY id LIMIT $3`,
			orgID, strings.ToLower(strings.TrimSpace(value)), findLimit)
	}
	return nil, fmt.Errorf("find assets: unknown key %q", key)
}

// RecordRowAudit implements EntityStore.
func (r *PGRepository) RecordRowAudit(ctx context.Context, entry *audit.Entry) error {
	e := *entry
	return database.WithRequestTenant(ctx, r.pool, e.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return database.BeforeCommit(ctx, func() error {
			if _, err := audit.NewPGRecorder().Record(ctx, tx, e); err != nil {
				return fmt.Errorf("audit %s: %w", e.Action, err)
			}
			return nil
		})
	})
}

// auditActor is the actor of the audit entry of a row: the user who
// started the run (source import and correlation come from the run's
// request info).
func auditActor(ctx context.Context, run *RunInfo) (id, actorType string) {
	id = strings.TrimSpace(tenant.FromContext(ctx).UserID)
	if id == "" {
		id = run.InitiatedBy
	}
	if !api.IsUUID(id) {
		return "", "system"
	}
	return id, "user"
}

// entityPolicyKey is the key of the policy of the starter in the state of
// a run.
type entityPolicyKey struct{}

type entityPolicy struct {
	once   sync.Once
	policy *permission.FieldPolicy
	err    error
}

// runPolicy returns the field policy of the user who started the run,
// resolved once outside the row transactions; the policy of ctx when it
// carries one (relation columns of a CI row). Without a resolver the run
// fails closed.
func runPolicy(ctx context.Context, run *RunInfo, policies permission.PolicyResolver) (*permission.FieldPolicy, error) {
	if p, err := permission.PolicyFrom(ctx); err == nil && p != nil {
		return p, nil
	}
	st := run.State(entityPolicyKey{}, func() any { return &entityPolicy{} }).(*entityPolicy)
	st.once.Do(func() {
		p, ok := identity.PrincipalFromContext(ctx)
		if !ok || policies == nil {
			st.err = errors.New("import: the policy of the user who started the import cannot be resolved")
			return
		}
		st.policy, st.err = policies.Resolve(database.WithoutTx(ctx), p)
	})
	return st.policy, st.err
}

// rowText returns the trimmed text of a target.
func rowText(v *MappedRow, target string) string {
	return strings.TrimSpace(v.Text(target))
}

// sortIssues orders the notes of a row by field.
func sortIssues(issues []Issue) {
	sort.SliceStable(issues, func(i, j int) bool { return issues[i].Field < issues[j].Field })
}

// refusalOf turns the refusal of a domain repository into the issues of
// the row: field permissions (field_forbidden), a taken unique value
// (unique_taken on its field, e.g. a barcode), a field error
// (invalid_value) and a database rule the row breaks. uniqueFields names
// the field of a unique constraint the domain does not map itself; ok is
// false for any other error.
func refusalOf(line int, err error, uniqueFields map[string]string) (RowResult, bool) {
	var forbidden *api.FieldForbiddenError
	var conflict *api.ConflictError
	var fe *api.FieldError
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &forbidden):
		return failed(line, strings.Join(forbidden.Fields, ","), CodeFieldForbidden, forbidden.Error()), true
	case errors.As(err, &conflict):
		return failed(line, conflict.Field, CodeUniqueTaken, "the value is already taken"), true
	case errors.As(err, &fe):
		code := CodeInvalidValue
		if fe.Status == http.StatusForbidden {
			code = CodeFieldForbidden
		}
		return failed(line, fe.Field, code, fe.Message), true
	case errors.As(err, &pgErr) && pgErr.Code == "23505":
		return failed(line, uniqueFields[pgErr.ConstraintName], CodeUniqueTaken, "the value is already taken"), true
	case errors.As(err, &pgErr) && (pgErr.Code == "23514" || pgErr.Code == "23503" || strings.HasPrefix(pgErr.Code, "22")):
		return failed(line, "", CodeInvalidValue, "a value of the row is not allowed"), true
	}
	return RowResult{}, false
}

// Targets of an asset row: the API names of the asset (WP-738).
const (
	AssetInventoryNoTarget = "inventory_no"
	AssetSerialTarget      = "serial_number"
	AssetPrimaryCITarget   = "primary_ci_id"
	AssetClientTarget      = "client_id"
)

// assetText are the text fields of an asset a row writes, by API name.
var assetText = map[string]func(a *asset.Asset) *string{
	"inventory_no":   func(a *asset.Asset) *string { return &a.AssetTag },
	"name":           func(a *asset.Asset) *string { return &a.Name },
	"category":       func(a *asset.Asset) *string { return &a.Category },
	"status":         func(a *asset.Asset) *string { return &a.Status },
	"manufacturer":   func(a *asset.Asset) *string { return &a.Manufacturer },
	"model":          func(a *asset.Asset) *string { return &a.Model },
	"purchase_date":  func(a *asset.Asset) *string { return &a.PurchaseDate },
	"purchase_price": func(a *asset.Asset) *string { return &a.PurchaseCost },
	"currency":       func(a *asset.Asset) *string { return &a.Currency },
	"warranty_until": func(a *asset.Asset) *string { return &a.WarrantyEnd },
	"cost_center":    func(a *asset.Asset) *string { return &a.CostCenter },
	"supplier_id":    func(a *asset.Asset) *string { return &a.SupplierID },
	"contract_id":    func(a *asset.Asset) *string { return &a.ContractID },
	"owner_user_id":  func(a *asset.Asset) *string { return &a.OwnerUserID },
	"owner_team_id":  func(a *asset.Asset) *string { return &a.OwnerTeamID },
	"invoice_number": func(a *asset.Asset) *string { return &a.InvoiceNumber },
	"serial_number":  func(a *asset.Asset) *string { return &a.SerialNumber },
	"rfid_tag":       func(a *asset.Asset) *string { return &a.RFIDTag },
	"barcode":        func(a *asset.Asset) *string { return &a.Barcode },
	"location":       func(a *asset.Asset) *string { return &a.Location },
	"notes":          func(a *asset.Asset) *string { return &a.Notes },
}

// setAssetField puts a value of a text field into an update.
func setAssetField(req *asset.UpdateRequest, field, value string) {
	v := &value
	switch field {
	case "inventory_no":
		req.AssetTag = v
	case "name":
		req.Name = v
	case "category":
		req.Category = v
	case "status":
		req.Status = v
	case "manufacturer":
		req.Manufacturer = v
	case "model":
		req.Model = v
	case "purchase_date":
		req.PurchaseDate = v
	case "purchase_price":
		req.PurchaseCost = v
	case "currency":
		req.Currency = v
	case "warranty_until":
		req.WarrantyEnd = v
	case "cost_center":
		req.CostCenter = v
	case "supplier_id":
		req.SupplierID = v
	case "contract_id":
		req.ContractID = v
	case "owner_user_id":
		req.OwnerUserID = v
	case "owner_team_id":
		req.OwnerTeamID = v
	case "invoice_number":
		req.InvoiceNumber = v
	case "serial_number":
		req.SerialNumber = v
	case "rfid_tag":
		req.RFIDTag = v
	case "barcode":
		req.Barcode = v
	case "location":
		req.Location = v
	case "notes":
		req.Notes = v
	}
}

// assetReadOnly are the members of an asset with their own write path
// (E-120, WP-271, WP-275, WP-755): a row that sets one gets read_only_field
// with the path, its other fields are written.
var assetReadOnly = map[string]string{
	"location_id":       "the location of an asset changes through stock movements (POST /api/v1/stock-movements), not through an import",
	"lifecycle_state":   "the state of an asset changes through POST /api/v1/assets/{id}/lifecycle-transitions, not through an import",
	"assigned_user_id":  assetHolderReadOnly,
	"assigned_team_id":  assetHolderReadOnly,
	"assignment_due_at": assetHolderReadOnly,
	"tags":              "the tags of an asset change through PUT /api/v1/assets/{id}/tags, not through an import",
}

const assetHolderReadOnly = "the holder of an asset changes through POST /api/v1/assets/{id}/assign, /return or /transfer, not through an import"

// assetPriceFormat is a purchase price of NUMERIC(14,2), as the API takes
// it.
var assetPriceFormat = regexp.MustCompile(`^\d{1,12}(\.\d{1,2})?$`)

// AssetWriter writes the rows of the entity asset (WP-776): the identity
// is the inventory number, without one the serial number (several assets
// with it: ambiguous_identity); the asset is created or changed through
// asset.Repository in the row's transaction with the field permissions of
// the starter, the primary CI only through the link path of WP-272 (the CI
// domain, then asset.ci_id), and every written row gets one audit entry
// asset.created or asset.updated with source import.
type AssetWriter struct {
	repo     asset.Repository
	store    EntityStore
	policies permission.PolicyResolver
	links    asset.CILinker
}

// NewAssetWriter returns the writer of asset rows.
func NewAssetWriter(repo asset.Repository, store EntityStore, policies permission.PolicyResolver) *AssetWriter {
	return &AssetWriter{repo: repo, store: store, policies: policies}
}

// WithCILinks sets the CI domain the primary CI is linked through
// (ci.Service, WP-272); without it primary_ci_id is read_only_field.
func (w *AssetWriter) WithCILinks(cis asset.CILinker) *AssetWriter {
	w.links = cis
	return w
}

// Entity implements Writer.
func (w *AssetWriter) Entity() string { return "asset" }

// assetRow is an asset row after its parsing.
type assetRow struct {
	texts     map[string]string
	custom    map[string]any
	client    string
	primaryCI string
	notes     []Issue
}

// parse reads the targets of an asset row; a refusal is the result of the
// row.
func (w *AssetWriter) parse(row *WriteRow) (*assetRow, *RowResult) {
	r := &assetRow{texts: map[string]string{}, custom: map[string]any{}}
	refuse := func(field, code, msg string) (*assetRow, *RowResult) {
		res := failed(row.Line, field, code, msg)
		return nil, &res
	}
	targets := make([]string, 0, len(row.Values.Values))
	for target := range row.Values.Values {
		targets = append(targets, target)
	}
	sort.Strings(targets)
	for _, target := range targets {
		val := row.Values.Values[target]
		text := rowText(row.Values, target)
		if key, ok := strings.CutPrefix(target, "attributes."); ok {
			if key == "tags" {
				r.notes = append(r.notes, Issue{Field: target, Code: CodeReadOnlyField, Message: assetReadOnly["tags"]})
				continue
			}
			if !val.Empty() {
				r.custom[key] = text
			}
			continue
		}
		if msg, ok := assetReadOnly[target]; ok {
			if text != "" {
				r.notes = append(r.notes, Issue{Field: target, Code: CodeReadOnlyField, Message: msg})
			}
			continue
		}
		switch {
		case assetText[target] != nil:
			if text != "" {
				r.texts[target] = text
			}
		case target == AssetClientTarget:
			r.client = text
		case target == AssetPrimaryCITarget:
			r.primaryCI = strings.ToLower(text)
		default:
			return refuse(target, CodeInvalidValue, fmt.Sprintf("%s is no field of an asset", target))
		}
	}
	if r.client != "" && !api.IsUUID(r.client) {
		return refuse(AssetClientTarget, CodeInvalidValue, "client_id must be a UUID")
	}
	if r.primaryCI != "" && w.links == nil {
		r.notes = append(r.notes, Issue{Field: AssetPrimaryCITarget, Code: CodeReadOnlyField,
			Message: "the primary CI of an asset is linked through POST /api/v1/assets/{id}/cis"})
		r.primaryCI = ""
	}
	if issue := normalizeAssetTexts(r.texts); issue != nil {
		res := RowResult{Outcome: OutcomeFailed, Issues: []Issue{*issue}}
		return nil, &res
	}
	return r, nil
}

// normalizeAssetTexts checks and normalizes the typed texts as the API
// does: currency (ISO 4217), purchase price (two decimals) and dates
// (YYYY-MM-DD).
func normalizeAssetTexts(texts map[string]string) *Issue {
	if c, ok := texts["currency"]; ok {
		c = strings.ToUpper(c)
		if err := tenantapi.ValidCurrency(c); err != nil {
			return &Issue{Field: "currency", Code: CodeInvalidValue, Message: "currency must be an active ISO 4217 code"}
		}
		texts["currency"] = c
	}
	if p, ok := texts["purchase_price"]; ok {
		if !assetPriceFormat.MatchString(p) {
			return &Issue{Field: "purchase_price", Code: CodeInvalidValue,
				Message: "purchase_price must be a decimal with up to 12 integer digits and 2 decimals, e.g. 1234.50"}
		}
		whole, frac, _ := strings.Cut(p, ".")
		whole = strings.TrimLeft(whole, "0")
		if whole == "" {
			whole = "0"
		}
		texts["purchase_price"] = whole + "." + frac + strings.Repeat("0", 2-len(frac))
	}
	for _, field := range []string{"purchase_date", "warranty_until"} {
		if d, ok := texts[field]; ok {
			if _, err := time.Parse(time.DateOnly, d); err != nil {
				return &Issue{Field: field, Code: CodeInvalidValue, Message: field + " must be a date YYYY-MM-DD"}
			}
		}
	}
	return nil
}

// Write implements Writer: the asset of the row's identity is changed
// (modes update_existing, both) or a new one created (create_missing,
// both).
func (w *AssetWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	policy, err := runPolicy(ctx, row.Run, w.policies)
	if err != nil {
		return RowResult{}, err
	}
	ctx = permission.WithPolicy(ctx, policy)
	r, refusal := w.parse(row)
	if refusal != nil {
		return *refusal, nil
	}
	orgID, line := row.Run.OrganizationID, row.Line
	key, value := AssetInventoryNoTarget, r.texts[AssetInventoryNoTarget]
	if value == "" {
		key, value = AssetSerialTarget, r.texts[AssetSerialTarget]
	}
	if value == "" {
		return failed(line, AssetInventoryNoTarget, CodeInvalidValue, "the row needs inventory_no or serial_number to identify the asset"), nil
	}
	ids, err := w.store.FindAssets(ctx, orgID, key, value)
	if err != nil {
		return RowResult{}, err
	}
	mode := row.Run.Mode
	switch {
	case len(ids) > 1:
		return failed(line, key, CodeAmbiguousIdentity, fmt.Sprintf("%d assets have the %s of the row", len(ids), key)), nil
	case len(ids) == 1 && mode == "create_missing":
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the asset %s exists (mode create_missing)", ids[0])}, nil
	case len(ids) == 1:
		return w.update(ctx, policy, row, r, ids[0])
	case mode == "update_existing":
		return RowResult{Outcome: OutcomeSkipped, Message: "no asset matches the row (mode update_existing)",
			Issues: []Issue{{Field: key, Code: CodeNoIdentityMatch, Message: fmt.Sprintf("no asset has the %s of the row", key)}}}, nil
	}
	return w.create(ctx, policy, row, r)
}

// assetFieldPaths are the DTO paths of the fields a row sets.
func assetFieldPaths(r *assetRow) []string {
	paths := make([]string, 0, len(r.texts)+len(r.custom)+1)
	for field := range r.texts {
		paths = append(paths, field)
	}
	for key := range r.custom {
		paths = append(paths, "custom_fields."+key)
	}
	if r.primaryCI != "" {
		paths = append(paths, AssetPrimaryCITarget)
	}
	sort.Strings(paths)
	return paths
}

// assetUniqueFields are the unique constraints of an asset row the asset
// repository leaves unmapped: the code registry holds the inventory
// number and the barcode (WP-289).
func assetUniqueFields(r *assetRow) map[string]string {
	field := "barcode"
	if r.texts["barcode"] == "" {
		field = AssetInventoryNoTarget
	}
	return map[string]string{"barcode_registry_organization_id_code_key": field}
}

// create creates the asset of a row.
func (w *AssetWriter) create(ctx context.Context, policy *permission.FieldPolicy, row *WriteRow, r *assetRow) (RowResult, error) {
	line := row.Line
	for _, field := range []string{AssetInventoryNoTarget, "name"} {
		if r.texts[field] == "" {
			return failed(line, field, CodeInvalidValue, field+" is required to create an asset"), nil
		}
	}
	a := &asset.Asset{OrganizationID: row.Run.OrganizationID, ClientID: r.client, Category: "hardware", Status: "in_stock",
		CustomFields: r.custom, Tags: []string{}}
	for field, value := range r.texts {
		*assetText[field](a) = value
	}
	ref := permission.ObjectRef{ClientID: a.ClientID}
	if err := permission.CheckWrite(policy, permission.KindAsset, "", ref, assetFieldPaths(r)); err != nil {
		res, _ := refusalOf(line, err, nil)
		return res, nil
	}
	if err := w.repo.Create(ctx, a); err != nil {
		if res, ok := refusalOf(line, err, assetUniqueFields(r)); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	changes := map[string]any{}
	for field, value := range r.texts {
		changes[field] = value
	}
	for key, value := range r.custom {
		changes["custom_fields."+key] = value
	}
	if r.primaryCI != "" {
		if res, err := w.link(ctx, row, a.ID, r.primaryCI); err != nil || res != nil {
			return derefResult(res), err
		}
		changes[AssetPrimaryCITarget] = r.primaryCI
	}
	if a.ClientID != "" {
		changes[AssetClientTarget] = a.ClientID
	}
	actor, actorType := auditActor(ctx, row.Run)
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: a.OrganizationID, ActorID: actor, ActorType: actorType,
		Action: "asset.created", ResourceType: "asset", ResourceID: a.ID, Changes: changes}); err != nil {
		return RowResult{}, err
	}
	sortIssues(r.notes)
	return RowResult{Outcome: OutcomeCreated, Message: a.ID, Issues: r.notes}, nil
}

// update changes the asset id with the fields of the row that differ; the
// starter must be allowed to write every field it changes and every field
// it cannot read (as PATCH does, RBA-07).
func (w *AssetWriter) update(ctx context.Context, policy *permission.FieldPolicy, row *WriteRow, r *assetRow, id string) (RowResult, error) {
	line, orgID := row.Line, row.Run.OrganizationID
	current, err := w.repo.GetByID(ctx, orgID, id)
	if errors.Is(err, asset.ErrNotFound) {
		return failed(line, "", CodeOutOfScope, "the asset lies outside the scope of the user who started the import"), nil
	}
	if err != nil {
		return RowResult{}, err
	}
	if r.client != "" && !strings.EqualFold(r.client, current.ClientID) {
		return failed(line, AssetClientTarget, CodeInvalidValue, "the client of an asset is not changed by an import"), nil
	}
	ref := permission.ObjectRef{ID: current.ID, ClientID: current.ClientID, SiteID: current.SiteID}
	req := &asset.UpdateRequest{}
	changes := map[string]any{}
	var paths []string
	for field, value := range r.texts {
		old := *assetText[field](current)
		if old != value {
			setAssetField(req, field, value)
			changes[field] = map[string]any{"old": old, "new": value}
		}
		if old != value || !permission.Readable(policy, permission.KindAsset, current.AssetTypeID, ref, field) {
			paths = append(paths, field)
		}
	}
	for key, value := range r.custom {
		path := "custom_fields." + key
		old, had := current.CustomFields[key]
		differs := !had || !sameValue(old, value)
		if differs {
			if req.CustomFields == nil {
				req.CustomFields = map[string]any{}
			}
			req.CustomFields[key] = value
			changes[path] = map[string]any{"old": old, "new": value}
		}
		if differs || !permission.Readable(policy, permission.KindAsset, current.AssetTypeID, ref, path) {
			paths = append(paths, path)
		}
	}
	link := r.primaryCI != "" && !strings.EqualFold(r.primaryCI, current.CIID)
	if link {
		paths = append(paths, AssetPrimaryCITarget)
		changes[AssetPrimaryCITarget] = map[string]any{"old": current.CIID, "new": r.primaryCI}
	}
	sort.Strings(paths)
	if err = permission.CheckWrite(policy, permission.KindAsset, current.AssetTypeID, ref, paths); err != nil {
		res, _ := refusalOf(line, err, nil)
		return res, nil
	}
	sortIssues(r.notes)
	if len(changes) == 0 {
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the asset %s is unchanged", id), Issues: r.notes}, nil
	}
	if link {
		if res, linkErr := w.link(ctx, row, id, r.primaryCI); linkErr != nil || res != nil {
			return derefResult(res), linkErr
		}
	}
	anyVersion := api.IfMatchAny
	req.IfMatch = &anyVersion
	if _, err = w.repo.Update(ctx, orgID, id, req); err != nil {
		if res, ok := refusalOf(line, err, assetUniqueFields(r)); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	actor, actorType := auditActor(ctx, row.Run)
	if auditErr := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "asset.updated", ResourceType: "asset", ResourceID: id, Changes: changes}); auditErr != nil {
		return RowResult{}, auditErr
	}
	return RowResult{Outcome: OutcomeUpdated, Message: id, Issues: r.notes}, nil
}

// link makes ciID the primary CI of the asset through the link path of
// WP-272: the CI is linked as a manual change of the CI domain (ci:write,
// field permissions, the client check of ci_asset_client), then asset.ci_id
// is written. A refusal is the result of the row.
func (w *AssetWriter) link(ctx context.Context, row *WriteRow, assetID, ciID string) (*RowResult, error) {
	line, orgID := row.Line, row.Run.OrganizationID
	refuse := func(code, msg string) (*RowResult, error) {
		res := failed(line, AssetPrimaryCITarget, code, msg)
		return &res, nil
	}
	if p, ok := identity.PrincipalFromContext(ctx); !ok || !p.Has(identity.PermCIWrite) {
		return refuse(CodeFieldForbidden, "linking a CI needs the permission ci:write")
	}
	if !api.IsUUID(ciID) {
		return refuse(CodeInvalidValue, "primary_ci_id must be a UUID")
	}
	store, ok := w.repo.(asset.LinkStore)
	if !ok {
		return refuse(CodeReadOnlyField, "the primary CI of an asset is linked through POST /api/v1/assets/{id}/cis")
	}
	item, err := w.links.GetByID(ctx, orgID, ciID)
	if errors.Is(err, ci.ErrNotFound) {
		return refuse(CodeReferenceNotFound, "the CI does not exist or lies outside the scope of the user who started the import")
	}
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(item.AssetID, assetID) {
		anyVersion := api.IfMatchAny
		_, err = w.links.Update(ctx, orgID, item.ID, ci.UpdateRequest{AssetID: &assetID, IfMatch: &anyVersion,
			Manual: &ci.ManualChange{Author: tenant.FromContext(ctx).UserID}})
		if ve, isValidation := ci.AsValidationError(err); isValidation && len(ve.Violations) > 0 {
			return refuse(CodeInvalidValue, ve.Violations[0].Detail)
		}
		if err != nil {
			if res, refused := refusalOf(line, err, nil); refused {
				return &res, nil
			}
			return nil, err
		}
	}
	if _, err = store.SetPrimaryCI(ctx, orgID, assetID, ciID); err != nil {
		if errors.Is(err, asset.ErrNotWritable) {
			return refuse(CodeOutOfScope, "the asset may not be changed in the scope of the user who started the import")
		}
		if res, refused := refusalOf(line, err, nil); refused {
			return &res, nil
		}
		return nil, err
	}
	return nil, nil //nolint:nilnil // nil result: linked
}
