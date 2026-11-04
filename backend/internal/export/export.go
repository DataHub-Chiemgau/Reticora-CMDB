// Package export provides CSV and JSON export for Reticora CMDB.
package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// streamBatchSize is the number of rows fetched per keyset page while
// streaming an export. It bounds the memory an export holds at any time,
// independent of how many CIs the tenant owns.
const streamBatchSize = 500

// Handler provides HTTP handlers for export endpoints.
type Handler struct {
	ciRepo    ci.Repository
	batchSize int
	// datev guards format=datev (WP-790); nil refuses it.
	datev *DATEVGate
}

// NewHandler creates a new export handler.
func NewHandler(ciRepo ci.Repository) *Handler {
	return &Handler{ciRepo: ciRepo, batchSize: streamBatchSize}
}

// WithDATEVGate sets the gate of format=datev; without one DATEV is refused
// (fail closed).
func (h *Handler) WithDATEVGate(gate *DATEVGate) *Handler {
	h.datev = gate
	return h
}

// RegisterRoutes registers export routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/export/cis", h.ExportCIs)
}

// ExportCIs handles GET /api/v1/export/cis?format=csv|json|datev. Until
// the sunset of the path (2027-10-15, E-141) format=datev keeps the CI
// layout, but only behind the DATEV gate (WP-790).
//
// The export is streamed: rows are fetched in keyset-paginated batches and
// flushed to the client as they are produced, so an export of a large tenant
// neither buffers the whole result set in memory nor hits a row cap.
func (h *Handler) ExportCIs(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	format := r.URL.Query().Get("format")
	if format == "" {
		format = "json"
	}
	if format != "csv" && format != "datev" && format != "json" {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "format must be 'csv', 'datev' or 'json'")
		return
	}
	if !guardDATEV(w, r, h.datev, t.OrganizationID, format) {
		return
	}

	filter := &ci.FilterParams{
		Status:   r.URL.Query().Get("status"),
		TypeID:   r.URL.Query().Get("ci_type_id"),
		ClientID: r.URL.Query().Get("client_id"),
		SortBy:   "created_at",
		SortDir:  "asc",
	}

	// The field permissions of the caller decide the columns and cells
	// (RBA-07, WP-729); a policy that cannot be resolved fails the
	// request before any byte is written (fail-closed).
	policy, err := permission.PolicyFrom(r.Context())
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	proj := newProjection(format, policy, typeFilter(filter.TypeID))

	// Probe the first batch before any header is written so that a repository
	// failure can still be reported as a proper problem+json response.
	first, err := h.fetch(r, t.OrganizationID, filter, nil)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	// The manifest names the CI fields the file leaves out (EXP-01), also
	// those the caller may not read (WP-729).
	w.Header().Set("X-Export-Omitted-Fields", strings.Join(proj.manifest.omittedNames(), ","))
	writer := newRowWriter(format, w, proj)
	writer.begin()

	batch := first
	for {
		for i := range batch {
			if rowErr := writer.row(&batch[i]); rowErr != nil {
				writer.abort(w)
				return
			}
		}
		writer.flushHTTP(w)

		if len(batch) < h.batchSize {
			break
		}
		cursor := ci.NextCursor(batch, filter, h.batchSize)
		if cursor == "" {
			break
		}
		decoded, decErr := api.DecodeCursor(cursor)
		if decErr != nil {
			break
		}
		batch, err = h.fetch(r, t.OrganizationID, filter, &decoded)
		if err != nil {
			// Headers are already sent; abort the stream so the client sees a
			// truncated (and therefore invalid) response instead of silently
			// receiving partial data that looks complete.
			writer.abort(w)
			return
		}
		if len(batch) == 0 {
			break
		}
	}

	writer.end()
	writer.flushHTTP(w)
}

// typeFilter returns the CI types an export is restricted to: the type of
// the filter, or none for every type of the organization.
func typeFilter(typeID string) []string {
	if typeID == "" {
		return nil
	}
	return []string{typeID}
}

func (h *Handler) fetch(r *http.Request, orgID string, filter *ci.FilterParams, cursor *api.Cursor) ([]ci.Item, error) {
	items, _, err := h.ciRepo.List(r.Context(), orgID, filter, api.PaginationParams{
		Limit:  h.batchSize,
		Cursor: cursor,
	})
	return items, err
}

// rowWriter serialises exported rows incrementally for one output format.
type rowWriter struct {
	format string
	// proj is the field projection of the principal (WP-729).
	proj    *projection
	csv     *csv.Writer
	json    *json.Encoder
	out     io.Writer
	written int
	failed  bool
}

func newRowWriter(format string, w http.ResponseWriter, proj *projection) *rowWriter {
	rw := newFormatWriter(format, w, proj)
	switch format {
	case "csv":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=cis_export.csv")
	case "datev":
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=cis_export_datev.csv")
	default:
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", "attachment; filename=cis_export.json")
	}
	w.WriteHeader(http.StatusOK)
	return rw
}

// newFormatWriter serialises rows to an arbitrary writer without HTTP
// concerns; it backs both the streaming endpoint and export-job rendering so
// the output is identical.
func newFormatWriter(format string, out io.Writer, proj *projection) *rowWriter {
	rw := &rowWriter{format: format, out: out, proj: proj}
	switch format {
	case "csv", "datev":
		// UTF-8 with BOM and semicolons (EXP-01), so spreadsheet programs
		// in German locales open the file correctly.
		rw.csv = csv.NewWriter(out)
		rw.csv.Comma = ';'
	default:
		rw.json = json.NewEncoder(out)
	}
	return rw
}

func (rw *rowWriter) begin() {
	switch rw.format {
	case "csv":
		_, _ = io.WriteString(rw.out, utf8BOM)
		// The columns the principal may read (WP-729).
		_ = rw.csv.Write(rw.proj.manifest.Fields)
	case "datev":
		_, _ = io.WriteString(rw.out, utf8BOM)
		_ = rw.csv.Write([]string{"Inventarnummer", "Bezeichnung", "Hersteller", "Modell", "Seriennummer", "Anschaffungsdatum", "Standort", "Kostenstelle", "Status"})
	default:
		_, _ = io.WriteString(rw.out, `{"data":[`)
	}
}

// row writes one CI. The cells and the JSON object are projected by the
// field permissions of the principal (WP-729): a column of the manifest's
// field_permission entries is left out (csv) or empty (datev) for every
// CI, a field the principal may not read on this CI is an empty cell or
// missing from its object.
func (rw *rowWriter) row(item *ci.Item) error {
	switch rw.format {
	case "csv":
		cells := make([]string, 0, len(rw.proj.manifest.Fields))
		for _, column := range rw.proj.manifest.Fields {
			cells = append(cells, rw.proj.cell(item, column, csvCell(item, column)))
		}
		rw.writeCells(cells)
	case "datev":
		attrs := rw.proj.attributes(item)
		rw.writeCells([]string{
			item.ID,
			item.Name,
			rw.proj.cell(item, "manufacturer", item.Manufacturer),
			rw.proj.cell(item, "model", item.Model),
			rw.proj.cell(item, "serial_number", item.SerialNumber),
			attributeString(attrs, datevAttributeKeys[0]...),
			attributeString(attrs, datevAttributeKeys[1]...),
			attributeString(attrs, datevAttributeKeys[2]...),
			item.Status,
		})
	default:
		value, err := rw.proj.object(item)
		if err != nil {
			rw.failed = true
			return err
		}
		if rw.written > 0 {
			_, _ = io.WriteString(rw.out, ",")
		}
		_ = rw.json.Encode(value)
	}
	rw.written++
	return nil
}

// csvCell is the value of a CSV column of the CI.
func csvCell(item *ci.Item, column string) string {
	switch column {
	case "id":
		return item.ID
	case "name":
		return item.Name
	case "status":
		return item.Status
	case "ci_type_id":
		return item.CITypeID
	case "manufacturer":
		return item.Manufacturer
	case "model":
		return item.Model
	case "serial_number":
		return item.SerialNumber
	case "management_ip":
		return item.ManagementIP
	case "firmware_version":
		return item.FirmwareVersion
	case "discovery_source":
		return item.DiscoverySource
	case "created_at":
		return item.CreatedAt
	case "updated_at":
		return item.UpdatedAt
	case "tags":
		return strings.Join(item.Tags, "|")
	}
	return ""
}

// writeCells writes one CSV row (csv and datev) with every cell
// neutralised against spreadsheet formulas (F-338).
func (rw *rowWriter) writeCells(cells []string) {
	for i, cell := range cells {
		cells[i] = neutralizeFormula(cell)
	}
	_ = rw.csv.Write(cells)
}

// formulaTriggers are the leading characters that make Excel or LibreOffice
// read a cell as a formula (OWASP CSV injection): =, +, -, @, tab and CR.
const formulaTriggers = "=+-@\t\r"

// neutralizeFormula prefixes a cell that starts with a formula trigger with
// ', so the spreadsheet shows it as text; discovered values such as an SNMP
// sysName are attacker-controlled (F-338).
func neutralizeFormula(cell string) string {
	if cell != "" && strings.IndexByte(formulaTriggers, cell[0]) >= 0 {
		return "'" + cell
	}
	return cell
}

func (rw *rowWriter) end() {
	if rw.failed {
		return
	}
	if rw.format == "json" {
		_, _ = fmt.Fprintf(rw.out, `],"total":%d}`, rw.written)
	}
}

// abort stops the stream without a closing delimiter so a truncated export is
// detectable by the client.
func (rw *rowWriter) abort(w http.ResponseWriter) {
	rw.failed = true
	rw.flushHTTP(w)
}

func (rw *rowWriter) flushHTTP(w http.ResponseWriter) {
	_ = rw.flush()
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
}

// flush pushes buffered CSV data to the underlying writer and surfaces its
// error so job rendering can fail the job instead of persisting a corrupt
// object.
func (rw *rowWriter) flush() error {
	if rw.csv == nil {
		return nil
	}
	rw.csv.Flush()
	if err := rw.csv.Error(); err != nil {
		return fmt.Errorf("write export row: %w", err)
	}
	return nil
}

func attributeString(attrs map[string]any, keys ...string) string {
	for _, key := range keys {
		if attrs == nil {
			return ""
		}
		if value, ok := attrs[key]; ok {
			return strings.TrimSpace(fmt.Sprint(value))
		}
	}
	return ""
}

// utf8BOM starts every CSV export (EXP-01).
const utf8BOM = "\ufeff"

// csvColumns are the CI fields of the CSV export, in this order. tags holds
// the tag keys separated by | (WP-754).
var csvColumns = []string{"id", "name", "status", "ci_type_id", "manufacturer", "model", "serial_number", "management_ip",
	"firmware_version", "discovery_source", "created_at", "updated_at", "tags"}

// datevFields are the CI fields the DATEV layout reads.
var datevFields = []string{"id", "name", "manufacturer", "model", "serial_number", "attributes", "status"}

// datevAttributeKeys are the attributes of the DATEV columns
// Anschaffungsdatum, Standort and Kostenstelle, each with its aliases.
var datevAttributeKeys = [][]string{
	{"purchase_date", "acquisition_date", "anschaffungsdatum"},
	{"location", "standort"},
	{"cost_center", "kostenstelle"},
}

// Manifest describes an export (EXP-01): the CI fields it carries and the
// visible CI fields it leaves out, each with the reason (ReasonLayout,
// ReasonFieldPermission).
type Manifest struct {
	Format  string         `json:"format"`
	Fields  []string       `json:"fields"`
	Omitted []OmittedField `json:"omitted"`
}

// OmittedField is a CI field an export leaves out.
type OmittedField struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

// Reasons of an omitted field (ExportManifest.omitted[].reason, WP-729).
const (
	// ReasonLayout: the format has no column for the field.
	ReasonLayout = "layout"
	// ReasonFieldPermission: the principal may not read the field for
	// at least one of the exported CI types (RBA-07).
	ReasonFieldPermission = "field_permission"
)

// ciFieldNames are the JSON names of the CI fields, in declaration order.
var ciFieldNames = func() []string {
	t := reflect.TypeFor[ci.Item]()
	var out []string
	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name != "" && name != "-" {
			out = append(out, name)
		}
	}
	return out
}()

// ManifestFor returns the manifest of an export format without field
// permissions. JSON carries every visible field; the CSV layouts carry
// their columns and list the rest with reason layout. ManifestForPolicy
// adds the fields a principal may not read.
func ManifestFor(format string) Manifest {
	m := Manifest{Format: format, Omitted: []OmittedField{}}
	switch format {
	case "csv":
		m.Fields = csvColumns
	case "datev":
		m.Fields = datevFields
	default:
		m.Fields = ciFieldNames
		return m
	}
	included := map[string]bool{}
	for _, f := range m.Fields {
		included[f] = true
	}
	for _, f := range ciFieldNames {
		if !included[f] {
			m.Omitted = append(m.Omitted, OmittedField{Field: f, Reason: ReasonLayout})
		}
	}
	return m
}

func (m Manifest) omittedNames() []string {
	out := make([]string, len(m.Omitted))
	for i := range m.Omitted {
		out[i] = m.Omitted[i].Field
	}
	return out
}

// ManifestForPolicy returns the manifest of an export format for the field
// permissions of a principal (RBA-07, WP-729): ManifestFor plus the fields
// the principal may not read for at least one of the exported CI types
// (typeIDs, none for every type of the organization), each with reason
// field_permission. A column of the layout drops out of fields; the
// attributes (attributes.<key>) and asset facts (asset.<field>) the
// principal may not read are listed one by one when the format carries
// them. Without field permissions it is ManifestFor.
func ManifestForPolicy(format string, policy *permission.FieldPolicy, typeIDs []string) Manifest {
	return newProjection(format, policy, typeIDs).manifest
}

// Members of the CI DTO with members of their own.
const (
	memberAttributes = "attributes"
	memberAsset      = "asset"
)

// projection is what an export leaves out for the field permissions of one
// principal (RBA-07, WP-729), computed once per export: the columns no CI
// of the export shows (closed) and, per CI, the fields the principal may
// not read on it (permission.Readable, permission.Project).
type projection struct {
	policy *permission.FieldPolicy
	// restricted is false without field permissions for CIs and assets:
	// cells and objects are those of phase 1.
	restricted bool
	manifest   Manifest
	// closed are the CI DTO paths (a typed column, attributes.<key>,
	// asset.<field>, or attributes as a whole) the principal may not read
	// for at least one of the exported types.
	closed map[string]bool
}

// assetFactFields are the members of the embedded asset facts the asset
// field permissions cover.
var assetFactFields = func() []string {
	t := reflect.TypeFor[ci.AssetFacts]()
	var out []string
	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if _, structural, ok := permission.FieldKeyOf(permission.KindAsset, name); ok && !structural {
			out = append(out, name)
		}
	}
	return out
}()

// newProjection resolves the closed columns of an export of the format for
// the policy; a nil policy reads nothing (fail-closed).
func newProjection(format string, policy *permission.FieldPolicy, typeIDs []string) *projection {
	p := &projection{policy: policy, restricted: permission.NeedsProjection(policy, permission.KindCI),
		manifest: ManifestFor(format), closed: map[string]bool{}}
	if !p.restricted {
		return p
	}
	byKey := map[string]string{}
	var keys []string
	for _, f := range ciFieldNames {
		if key, structural, ok := permission.FieldKeyOf(permission.KindCI, f); ok && !structural {
			keys = append(keys, key)
			byKey[key] = f
		}
	}
	for _, key := range permission.TypeUnreadableKeys(policy, permission.KindCI, typeIDs, keys) {
		p.closed[byKey[key]] = true
	}
	if permission.UnnamedAttributesUnreadable(policy, permission.KindCI, typeIDs) {
		// A grant none of a type or of the kind closes attributes whose
		// keys the policy does not know: no CI shows any attribute.
		p.closed[memberAttributes] = true
	} else {
		var attrKeys []string
		for _, key := range policy.NamedFieldKeys(permission.KindCI) {
			namespace, name, _ := strings.Cut(key, ".")
			if namespace == permission.NamespaceAttributes || namespace == permission.NamespaceInstance {
				attrKeys = append(attrKeys, permission.NamespaceAttributes+"."+name)
			}
		}
		for _, key := range permission.TypeUnreadableKeys(policy, permission.KindCI, typeIDs, attrKeys) {
			p.closed[permission.DTOPath(key)] = true
		}
	}
	// The asset of a CI may be of any asset type.
	assetKeys := make([]string, 0, len(assetFactFields))
	for _, f := range assetFactFields {
		assetKeys = append(assetKeys, permission.NamespaceCore+"."+f)
	}
	for _, key := range permission.TypeUnreadableKeys(policy, permission.KindAsset, nil, assetKeys) {
		p.closed[memberAsset+"."+permission.DTOPath(key)] = true
	}
	p.manifest = p.manifest.withClosed(p.closed)
	return p
}

// withClosed moves the closed fields of the layout from fields to omitted
// (field_permission) and lists the closed attributes and asset facts the
// format carries; for DATEV the attributes of its columns.
func (m *Manifest) withClosed(closed map[string]bool) Manifest {
	out := Manifest{Format: m.Format, Fields: make([]string, 0, len(m.Fields)), Omitted: append([]OmittedField{}, m.Omitted...)}
	carries := map[string]bool{}
	for _, f := range m.Fields {
		if closed[f] {
			out.Omitted = append(out.Omitted, OmittedField{Field: f, Reason: ReasonFieldPermission})
			continue
		}
		out.Fields = append(out.Fields, f)
		carries[f] = true
	}
	var nested []string
	for path := range closed {
		member, name, ok := strings.Cut(path, ".")
		if !ok || !carries[member] {
			continue
		}
		if m.Format == "datev" && !slices.ContainsFunc(datevAttributeKeys, func(keys []string) bool { return slices.Contains(keys, name) }) {
			continue
		}
		nested = append(nested, path)
	}
	sort.Strings(nested)
	for _, path := range nested {
		out.Omitted = append(out.Omitted, OmittedField{Field: path, Reason: ReasonFieldPermission})
	}
	return out
}

// ciRef is the object reference of a CI for its field permissions.
func ciRef(item *ci.Item) permission.ObjectRef {
	return permission.ObjectRef{ID: item.ID, ClientID: item.ClientID, SiteID: item.SiteID}
}

// ciOptions name the instance attributes of the CI and the type and scope
// of its asset (as the CI API projects it).
func ciOptions(item *ci.Item) []permission.ProjectOption {
	opts := []permission.ProjectOption{permission.WithInstanceKeys(item.InstanceKeys)}
	if item.Asset != nil {
		opts = append(opts, permission.WithAsset(item.Asset.AssetTypeID,
			permission.ObjectRef{ID: item.Asset.ID, ClientID: item.Asset.ClientID, SiteID: item.Asset.SiteID}))
	}
	return opts
}

// cell returns the value of a CI column the principal may read, otherwise
// an empty cell: a closed column for every CI, a field not readable on
// this CI (object grant, scope of an assignment) for this CI.
func (p *projection) cell(item *ci.Item, column, value string) string {
	if !p.restricted || value == "" {
		return value
	}
	if p.closed[column] || !permission.Readable(p.policy, permission.KindCI, item.CITypeID, ciRef(item), column, ciOptions(item)...) {
		return ""
	}
	return value
}

// attributes returns the attributes of the CI the principal may read.
func (p *projection) attributes(item *ci.Item) map[string]any {
	if !p.restricted {
		return item.Attributes
	}
	if p.closed[memberAttributes] {
		return nil
	}
	attrs := permission.ProjectAttributes(p.policy, item.CITypeID, ciRef(item), item.Attributes, permission.WithInstanceKeys(item.InstanceKeys))
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if !p.closed[memberAttributes+"."+k] {
			out[k] = v
		}
	}
	return out
}

// object returns the JSON object of the CI: the CI itself without field
// permissions, otherwise its projected DTO (permission.Project, as the CI
// API answers it) without the closed fields.
func (p *projection) object(item *ci.Item) (any, error) {
	if !p.restricted {
		return item, nil
	}
	body, err := permission.Project(p.policy, permission.KindCI, item.CITypeID, ciRef(item), item, ciOptions(item)...)
	if err != nil {
		return nil, err
	}
	for member := range body {
		if p.closed[member] {
			delete(body, member)
		}
	}
	for _, member := range []string{memberAttributes, memberAsset} {
		raw, ok := body[member]
		if !ok || string(raw) == "null" {
			continue
		}
		var nested map[string]json.RawMessage
		if uerr := json.Unmarshal(raw, &nested); uerr != nil {
			return nil, uerr
		}
		for k := range nested {
			if p.closed[member+"."+k] {
				delete(nested, k)
			}
		}
		encoded, merr := json.Marshal(nested)
		if merr != nil {
			return nil, merr
		}
		body[member] = encoded
	}
	return body, nil
}
