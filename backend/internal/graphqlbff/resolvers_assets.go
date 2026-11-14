package graphqlbff

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/vektah/gqlparser/v2/gqlerror"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/graphqlbff/generated"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The asset detail aggregate (GQL-03, WP-311): asset(id) with its CIs
// (ci.asset_id), the child CIs of those CIs (ci.parent_ci_id with
// childMode, AST-05; not the legacy table composition), its movements in
// the ledger stock_movement (WP-274, E-38) and its documents. Permissions,
// entitlements and module gates are those of REST (GQL-04, E-03, WP-214):
// the asset, its CIs, children and movements need asset:read, assets_basic
// and the module inventory, the CIs in addition ci:read; the documents need
// document:read, documents and the module documents.

// assetEntityType is the link target of the documents of an asset.
const assetEntityType = "asset"

// errModuleOff answers a field of a module that is switched off (a new
// error per call, see errUnavailable).
func errModuleOff(module string) *gqlerror.Error {
	return gqlError("UNAVAILABLE", "module "+module+" is not enabled on this server")
}

// authorizeModule checks the module gate before the permission and the
// entitlement (authorize): a field of a module that is off answers
// UNAVAILABLE, without a module checker every module-bound field is
// refused (fail-closed).
func (r *Resolver) authorizeModule(ctx context.Context, module string, perm identity.Permission, feature string) (string, error) {
	if _, err := r.authorize(ctx, "", ""); err != nil {
		return "", err
	}
	if r.modules == nil || !r.modules.Enabled(module) {
		return "", errModuleOff(module)
	}
	return r.authorize(ctx, perm, feature)
}

// authorizeAsset is the gate of the asset and its inventory fields.
func (r *Resolver) authorizeAsset(ctx context.Context) (string, error) {
	return r.authorizeModule(ctx, ModuleInventory, identity.PermAssetRead, entitlement.FeatureAssetsBasic)
}

func (q *queryResolver) Asset(ctx context.Context, id string) (*asset.Asset, error) {
	orgID, err := q.authorizeAsset(ctx)
	if err != nil {
		return nil, err
	}
	if q.sources.Assets == nil {
		return nil, errUnavailable()
	}
	a, err := q.sources.Assets.GetByID(ctx, orgID, strings.TrimSpace(id))
	if err != nil {
		// Not visible or not there: null, as for a CI.
		return nil, nil //nolint:nilnil // GraphQL null for an invisible asset
	}
	return a, nil
}

// assetCIs returns the CIs of the asset through the request loader (read
// once per request); the CI objects need ci:read like mayReadCIs.
func (a *assetResolver) assetCIs(ctx context.Context, obj *asset.Asset) ([]ci.Item, error) {
	orgID, err := a.authorizeAsset(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = a.authorize(ctx, identity.PermCIRead, FeatureCMDBCore); err != nil {
		return nil, err
	}
	l := loaderFrom(ctx)
	if l == nil {
		l = loaderFrom(withLoader(ctx, a.ciRepo, orgID))
	}
	return l.AssetCIs(ctx, obj.ID)
}

// firstCIs returns at most the page of first entries of items.
func firstCIs(items []ci.Item, first *int) []*ci.Item {
	n := min(len(items), pageSize(first, defaultPage, maxPage))
	out := make([]*ci.Item, n)
	for i := range n {
		out[i] = &items[i]
	}
	return out
}

func (a *assetResolver) Cis(ctx context.Context, obj *asset.Asset, first *int) ([]*ci.Item, error) {
	items, err := a.assetCIs(ctx, obj)
	if err != nil {
		return nil, err
	}
	out := firstCIs(items, first)
	if l := loaderFrom(ctx); l != nil {
		l.registerSiblings(out)
	}
	return out, nil
}

// Children reads the child CIs of all CIs of the asset in one query
// (parent_ci_id in …) through the request loader.
func (a *assetResolver) Children(ctx context.Context, obj *asset.Asset, first *int) ([]*ci.Item, error) {
	items, err := a.assetCIs(ctx, obj)
	if err != nil {
		return nil, err
	}
	parents := make([]string, len(items))
	for i := range items {
		parents[i] = items[i].ID
	}
	l := loaderFrom(ctx)
	if l == nil {
		l = loaderFrom(withLoader(ctx, a.ciRepo, tenant.FromContext(ctx).OrganizationID))
	}
	kids, err := l.Children(ctx, parents)
	if err != nil {
		return nil, err
	}
	out := firstCIs(kids, first)
	l.registerSiblings(out)
	return out, nil
}

// Movements pages the movements of the asset newest first with the keyset
// cursor of the movement lists (occurred_at DESC, id DESC).
func (a *assetResolver) Movements(ctx context.Context, obj *asset.Asset, first *int, after *string) (*generated.MovementConnection, error) {
	orgID, err := a.authorizeAsset(ctx)
	if err != nil {
		return nil, err
	}
	if a.sources.Movements == nil {
		return nil, errUnavailable()
	}
	page := api.PaginationParams{Limit: pageSize(first, defaultPage, maxPage)}
	if raw := value(after); raw != "" {
		cursor, decodeErr := api.DecodeCursor(raw)
		if decodeErr != nil {
			return nil, gqlError(codeBadInput, "invalid cursor")
		}
		page.Cursor = &cursor
	}
	rows, total, err := a.sources.Movements.ListMovements(ctx, orgID, &movement.MovementFilter{AssetID: obj.ID}, page)
	if err != nil {
		if errors.Is(err, api.ErrInvalidCursor) {
			return nil, gqlError(codeBadInput, "invalid cursor")
		}
		return nil, err
	}
	list := movement.MovementPage(rows, total, page)
	conn := &generated.MovementConnection{TotalCount: total, PageInfo: &generated.PageInfo{HasNextPage: list.HasMore},
		Nodes: make([]*movement.Movement, len(list.Data))}
	for i := range list.Data {
		conn.Nodes[i] = &list.Data[i]
	}
	if list.NextCursor != "" {
		conn.PageInfo.EndCursor = &list.NextCursor
	}
	return conn, nil
}

func (a *assetResolver) Documents(ctx context.Context, obj *asset.Asset, first *int) ([]*document.Document, error) {
	orgID, err := a.authorizeModule(ctx, ModuleDocuments, identity.PermDocumentRead, entitlement.FeatureDocuments)
	if err != nil {
		return nil, err
	}
	if a.sources.Documents == nil {
		return nil, errUnavailable()
	}
	docs, _, err := a.sources.Documents.GetLinksForEntity(ctx, orgID, assetEntityType, obj.ID,
		api.PaginationParams{Limit: pageSize(first, defaultPage, maxPage)})
	if err != nil {
		return nil, err
	}
	out := make([]*document.Document, len(docs))
	for i := range docs {
		out[i] = &docs[i]
	}
	return out, nil
}

// Field permissions of the asset (RBA-07, WP-265, Konvention 11): the
// fields of the asset policy resolve with the policy of the request, an
// unreadable one is null, an unreadable custom field is left out. The
// structural members (id, name, clientId, version, timestamps) stay.

func assetRef(a *asset.Asset) permission.ObjectRef {
	return permission.ObjectRef{ID: a.ID, ClientID: a.ClientID, SiteID: a.SiteID}
}

// assetReadable reports whether the principal may read the asset field at
// the DTO path (the AST-01 name).
func assetReadable(ctx context.Context, obj *asset.Asset, path string) (bool, error) {
	p, err := policy(ctx)
	if err != nil {
		return false, err
	}
	return permission.Readable(p, permission.KindAsset, obj.AssetTypeID, assetRef(obj), path), nil
}

// assetField returns value when the field at path is readable and set,
// null otherwise.
func assetField(ctx context.Context, obj *asset.Asset, path, value string) (*string, error) {
	ok, err := assetReadable(ctx, obj, path)
	if err != nil || !ok || value == "" {
		return nil, err
	}
	return &value, nil
}

func (a *assetResolver) InventoryNo(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "inventory_no", obj.AssetTag)
}

func (a *assetResolver) Category(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "category", obj.Category)
}

func (a *assetResolver) Status(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "status", obj.Status)
}

func (a *assetResolver) Manufacturer(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "manufacturer", obj.Manufacturer)
}

func (a *assetResolver) Model(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "model", obj.Model)
}

func (a *assetResolver) SerialNumber(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "serial_number", obj.SerialNumber)
}

func (a *assetResolver) PurchaseDate(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "purchase_date", obj.PurchaseDate)
}

func (a *assetResolver) PurchasePrice(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "purchase_price", obj.PurchaseCost)
}

func (a *assetResolver) Currency(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "currency", obj.Currency)
}

func (a *assetResolver) WarrantyUntil(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "warranty_until", obj.WarrantyEnd)
}

func (a *assetResolver) CostCenter(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "cost_center", obj.CostCenter)
}

func (a *assetResolver) SupplierID(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "supplier_id", obj.SupplierID)
}

func (a *assetResolver) ContractID(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "contract_id", obj.ContractID)
}

func (a *assetResolver) OwnerUserID(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "owner_user_id", obj.OwnerUserID)
}

func (a *assetResolver) OwnerTeamID(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "owner_team_id", obj.OwnerTeamID)
}

func (a *assetResolver) InvoiceNumber(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "invoice_number", obj.InvoiceNumber)
}

func (a *assetResolver) RfidTag(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "rfid_tag", obj.RFIDTag)
}

func (a *assetResolver) Barcode(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "barcode", obj.Barcode)
}

func (a *assetResolver) LocationID(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "location_id", obj.LocationID)
}

func (a *assetResolver) LifecycleState(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "lifecycle_state", obj.LifecycleState)
}

func (a *assetResolver) PrimaryCiID(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "primary_ci_id", obj.CIID)
}

func (a *assetResolver) Notes(ctx context.Context, obj *asset.Asset) (*string, error) {
	return assetField(ctx, obj, "notes", obj.Notes)
}

// CustomFields returns the custom fields without the keys the principal
// may not read (custom_fields.<key>, field key attributes.<key>).
func (a *assetResolver) CustomFields(ctx context.Context, obj *asset.Asset) (map[string]any, error) {
	p, err := policy(ctx)
	if err != nil {
		return nil, err
	}
	if !p.Restricted(permission.KindAsset) || obj.CustomFields == nil {
		return obj.CustomFields, nil
	}
	out := maps.Clone(obj.CustomFields)
	for key := range obj.CustomFields {
		if !permission.Readable(p, permission.KindAsset, obj.AssetTypeID, assetRef(obj), "custom_fields."+key) {
			delete(out, key)
		}
	}
	return out, nil
}

// Tags returns the tag keys; null when the asset policy hides them (the
// member has no field key, so a restricted policy leaves it out like the
// REST projection).
func (a *assetResolver) Tags(ctx context.Context, obj *asset.Asset) ([]string, error) {
	ok, err := assetReadable(ctx, obj, "tags")
	if err != nil || !ok {
		return nil, err
	}
	if obj.Tags == nil {
		return []string{}, nil
	}
	return obj.Tags, nil
}

// Field permissions of the document (RBA-07, WP-267, finding F-1 of
// WP-326): the fields of the document policy resolve with the policy of
// the request like the REST projection (document.Project), an unreadable
// one is null. The structural members (id, title, version, scanStatus,
// timestamps) stay.

type documentResolver struct{ *Resolver }

// documentReadable reports whether the principal may read the document
// field at the DTO path.
func documentReadable(ctx context.Context, obj *document.Document, path string) (bool, error) {
	p, err := policy(ctx)
	if err != nil {
		return false, err
	}
	return document.Readable(p, obj, path), nil
}

// documentField returns value when the field at path is readable, null
// otherwise.
func documentField[T any](ctx context.Context, obj *document.Document, path string, value T) (*T, error) {
	ok, err := documentReadable(ctx, obj, path)
	if err != nil || !ok {
		return nil, err
	}
	return &value, nil
}

func (d *documentResolver) Description(ctx context.Context, obj *document.Document) (*string, error) {
	return documentField(ctx, obj, "description", obj.Description)
}

func (d *documentResolver) FileName(ctx context.Context, obj *document.Document) (*string, error) {
	return documentField(ctx, obj, "file_name", obj.FileName)
}

func (d *documentResolver) FileSize(ctx context.Context, obj *document.Document) (*int, error) {
	return documentField(ctx, obj, "file_size", int(obj.FileSize))
}

func (d *documentResolver) MimeType(ctx context.Context, obj *document.Document) (*string, error) {
	return documentField(ctx, obj, "mime_type", obj.MimeType)
}

func (d *documentResolver) Category(ctx context.Context, obj *document.Document) (*string, error) {
	return documentField(ctx, obj, "category", obj.Category)
}

// Tags returns the tag keys; null when the document policy hides them.
func (d *documentResolver) Tags(ctx context.Context, obj *document.Document) ([]string, error) {
	ok, err := documentReadable(ctx, obj, "tags")
	if err != nil || !ok {
		return nil, err
	}
	if obj.Tags == nil {
		return []string{}, nil
	}
	return obj.Tags, nil
}

// UploadedBy returns the uploader; null when unreadable or unknown.
func (d *documentResolver) UploadedBy(ctx context.Context, obj *document.Document) (*string, error) {
	if obj.UploadedBy == "" {
		return nil, nil //nolint:nilnil // GraphQL null for an unknown uploader
	}
	return documentField(ctx, obj, "uploaded_by", obj.UploadedBy)
}
