package export

import (
	"bytes"
	"cmp"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"slices"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// AssetRows reads the assets of a csv or json export with entity asset by
// id (WP-782): one read under the scope of ctx, so ids the scope does not
// show and deleted ones are missing. asset.PGRepository implements it.
type AssetRows interface {
	GetByIDs(ctx context.Context, orgID string, ids []string) ([]asset.Asset, error)
}

// ErrAssetExportUnavailable fails an asset export of a worker without
// AssetRows (fail closed, WP-782).
var ErrAssetExportUnavailable = &assetExportUnavailableError{}

type assetExportUnavailableError struct{}

func (*assetExportUnavailableError) Error() string {
	return "the asset export is not available in this deployment"
}

// PublicMessage is the message of the failed job (jobs.PublicError).
func (e *assetExportUnavailableError) PublicMessage() string { return e.Error() }

// Members of the asset DTO the asset export treats apart: custom_fields
// (only in JSON) and the members only GET /api/v1/assets/{id} sets.
const assetMemberCustomFields = "custom_fields"

var assetDetailOnly = map[string]bool{"primary_ci": true, "active_reservation": true}

// assetFieldNames are the JSON names of the asset DTO the asset export
// carries, in declaration order: every member but those only GET
// /api/v1/assets/{id} sets.
var assetFieldNames = func() []string {
	t := reflect.TypeFor[asset.Asset]()
	var out []string
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" && !assetDetailOnly[name] {
			out = append(out, name)
		}
	}
	return out
}()

// assetCSVColumns are the columns of the CSV asset export: the JSON names
// of the asset DTO without custom_fields. tags holds the tag keys
// separated by |.
var assetCSVColumns = func() []string {
	out := make([]string, 0, len(assetFieldNames))
	for _, name := range assetFieldNames {
		if name != assetMemberCustomFields {
			out = append(out, name)
		}
	}
	return out
}()

// AssetManifestFor returns the manifest of an asset export of the format
// without field permissions: JSON carries every field of the DTO, CSV
// leaves out custom_fields (reason layout).
func AssetManifestFor(format string) Manifest {
	if format == "csv" {
		return Manifest{Format: format, Fields: slices.Clone(assetCSVColumns),
			Omitted: []OmittedField{{Field: assetMemberCustomFields, Reason: ReasonLayout}}}
	}
	return Manifest{Format: format, Fields: slices.Clone(assetFieldNames), Omitted: []OmittedField{}}
}

// AssetManifestForPolicy returns the manifest of an asset export for the
// field permissions of a principal (RBA-07, WP-729): AssetManifestFor
// plus the fields the principal may not read for at least one asset type,
// each with reason field_permission; for JSON also the custom fields
// (custom_fields.<key>) the policy names and closes.
func AssetManifestForPolicy(format string, policy *permission.FieldPolicy) Manifest {
	return newAssetProjection(format, policy).manifest
}

// assetProjection is what an asset export leaves out for the field
// permissions of one principal, computed once per export: the members no
// asset of the export shows (closed) and, per asset, the fields the
// principal may not read on it (permission.Project with the asset
// policy, as GET /api/v1/assets answers it).
type assetProjection struct {
	policy *permission.FieldPolicy
	// restricted is false without field permissions for assets: the DTO
	// is written as it is.
	restricted bool
	manifest   Manifest
	// closed are the DTO paths (a member or custom_fields.<key>) the
	// principal may not read for at least one asset type.
	closed map[string]bool
}

// newAssetProjection resolves the closed members of an asset export of
// the format for the policy; a nil policy reads no field with field
// permissions (fail-closed). A member the asset policy does not map (the
// holder cache, tags) is closed whenever the policy is restricted, as the
// asset API leaves it out then.
func newAssetProjection(format string, policy *permission.FieldPolicy) *assetProjection {
	p := &assetProjection{policy: policy, restricted: permission.NeedsProjection(policy, permission.KindAsset),
		manifest: AssetManifestFor(format), closed: map[string]bool{}}
	if !p.restricted {
		return p
	}
	byKey := map[string]string{}
	var keys []string
	for _, name := range assetFieldNames {
		if name == assetMemberCustomFields {
			continue
		}
		key, structural, ok := permission.FieldKeyOf(permission.KindAsset, name)
		switch {
		case !ok:
			p.closed[name] = true
		case !structural:
			keys = append(keys, key)
			byKey[key] = name
		}
	}
	for _, key := range permission.TypeUnreadableKeys(policy, permission.KindAsset, nil, keys) {
		p.closed[byKey[key]] = true
	}
	if permission.UnnamedAttributesUnreadable(policy, permission.KindAsset, nil) {
		p.closed[assetMemberCustomFields] = true
	} else {
		var custom []string
		for _, key := range policy.NamedFieldKeys(permission.KindAsset) {
			if namespace, _, _ := strings.Cut(key, "."); namespace == permission.NamespaceAttributes {
				custom = append(custom, key)
			}
		}
		for _, key := range permission.TypeUnreadableKeys(policy, permission.KindAsset, nil, custom) {
			_, name, _ := strings.Cut(key, ".")
			p.closed[assetMemberCustomFields+"."+name] = true
		}
	}
	p.manifest = p.manifest.withClosed(p.closed)
	return p
}

// body returns the members of the asset the principal may read: the DTO
// without field permissions, otherwise its projection by the asset policy
// without the closed members and custom fields.
func (p *assetProjection) body(a *asset.Asset) (map[string]json.RawMessage, error) {
	var body map[string]json.RawMessage
	if !p.restricted {
		raw, merr := json.Marshal(a)
		if merr != nil {
			return nil, merr
		}
		if uerr := json.Unmarshal(raw, &body); uerr != nil {
			return nil, uerr
		}
		return body, nil
	}
	body, err := permission.Project(p.policy, permission.KindAsset, a.AssetTypeID,
		permission.ObjectRef{ID: a.ID, ClientID: a.ClientID, SiteID: a.SiteID}, a)
	if err != nil {
		return nil, err
	}
	for member := range body {
		if p.closed[member] {
			delete(body, member)
		}
	}
	if raw, ok := body[assetMemberCustomFields]; ok && string(raw) != "null" {
		var custom map[string]json.RawMessage
		if uerr := json.Unmarshal(raw, &custom); uerr != nil {
			return nil, uerr
		}
		for k := range custom {
			if p.closed[assetMemberCustomFields+"."+k] {
				delete(custom, k)
			}
		}
		encoded, merr := json.Marshal(custom)
		if merr != nil {
			return nil, merr
		}
		body[assetMemberCustomFields] = encoded
	}
	return body, nil
}

// assetCell is the CSV text of a member: a string as it is, a list of
// strings (tags) joined by |, null or a missing member empty, anything
// else (numbers) its JSON text.
func assetCell(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var list []string
	if json.Unmarshal(raw, &list) == nil {
		return strings.Join(list, "|")
	}
	return string(raw)
}

// RenderAssets writes the assets of ids in format (csv or json) to out,
// projected by policy (RBA-07, WP-729), and returns the number of rows and
// the manifest of the file (WP-782). The assets are read once through src
// under the scope of ctx, so invisible and deleted ids are missing, and
// sorted by created_at and id. CSV is UTF-8 with BOM and semicolons like
// the CI export (EXP-01) with every cell neutralised against formulas;
// JSON writes {"data":[<asset DTO>…],"total":n}. A nil policy reads no
// field with field permissions (fail-closed).
func RenderAssets(ctx context.Context, src AssetRows, orgID, format string, ids []string, policy *permission.FieldPolicy,
	out io.Writer) (int, Manifest, error) {
	proj := newAssetProjection(format, policy)
	items, err := src.GetByIDs(ctx, orgID, ids)
	if err != nil {
		return 0, proj.manifest, fmt.Errorf("export query: %w", err)
	}
	slices.SortFunc(items, func(a, b asset.Asset) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	if format == "csv" {
		return renderAssetCSV(items, proj, out)
	}
	return renderAssetJSON(items, proj, out)
}

func renderAssetCSV(items []asset.Asset, proj *assetProjection, out io.Writer) (int, Manifest, error) {
	if _, err := io.WriteString(out, utf8BOM); err != nil {
		return 0, proj.manifest, fmt.Errorf("write export row: %w", err)
	}
	file := csv.NewWriter(out)
	file.Comma = ';'
	_ = file.Write(proj.manifest.Fields)
	for i := range items {
		body, err := proj.body(&items[i])
		if err != nil {
			return i, proj.manifest, fmt.Errorf("export row: %w", err)
		}
		cells := make([]string, len(proj.manifest.Fields))
		for c, column := range proj.manifest.Fields {
			cells[c] = neutralizeFormula(assetCell(body[column]))
		}
		_ = file.Write(cells)
	}
	file.Flush()
	if err := file.Error(); err != nil {
		return len(items), proj.manifest, fmt.Errorf("write export row: %w", err)
	}
	return len(items), proj.manifest, nil
}

func renderAssetJSON(items []asset.Asset, proj *assetProjection, out io.Writer) (int, Manifest, error) {
	var buf bytes.Buffer
	buf.WriteString(`{"data":[`)
	enc := json.NewEncoder(&buf)
	for i := range items {
		var value any = &items[i]
		if proj.restricted {
			body, err := proj.body(&items[i])
			if err != nil {
				return i, proj.manifest, fmt.Errorf("export row: %w", err)
			}
			value = body
		}
		if i > 0 {
			buf.WriteString(",")
		}
		if err := enc.Encode(value); err != nil {
			return i, proj.manifest, fmt.Errorf("export row: %w", err)
		}
	}
	fmt.Fprintf(&buf, `],"total":%d}`, len(items))
	if _, err := out.Write(buf.Bytes()); err != nil {
		return len(items), proj.manifest, fmt.Errorf("write export row: %w", err)
	}
	return len(items), proj.manifest, nil
}

// renderAssets renders a csv or json job with entity asset (WP-782); a
// worker without AssetRows fails it (fail closed).
func (w *JobWorker) renderAssets(ctx context.Context, job *Job, policy *permission.FieldPolicy, out io.Writer) (int, Manifest, error) {
	if w.AssetRows == nil {
		return 0, Manifest{}, ErrAssetExportUnavailable
	}
	return RenderAssets(ctx, w.AssetRows, job.OrganizationID, job.Format, job.Filters.IDs, policy, out)
}
