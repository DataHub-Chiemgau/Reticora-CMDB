package export

import (
	"bytes"
	"context"
	_ "embed" // the default DATEV mapping (E-50)
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"golang.org/x/text/encoding/charmap"
)

// The DATEV export of the asset accounting (EXP-01, E-50, E-141, WP-310):
// format datev of an export job renders the canonical asset data of the
// job's scope snapshot through a column mapping (the embedded default or
// the override organization.settings.datev_mapping) as Windows-1252 CSV
// with semicolons and CRLF. Rows with a missing required field or with
// characters Windows-1252 cannot encode stay out of the file and go to the
// error report <org>/<job>.errors.csv (UTF-8 with BOM). The streaming
// GET /api/v1/export/cis?format=datev keeps the CI layout of export.go
// until its sunset (2027-10-15).

//go:embed datev_mapping.json
var defaultDATEVMappingJSON []byte

// SettingDATEVMapping is the key of the mapping override in
// organization.settings.
const SettingDATEVMapping = "datev_mapping"

// The formats of a DATEV column: text runs through neutralizeFormula, an
// amount has a decimal comma without thousands separator, a date is
// DD.MM.YYYY (E-50).
const (
	DATEVFormatText   = "text"
	DATEVFormatAmount = "amount"
	DATEVFormatDate   = "date"
)

// DATEVColumn is one column of the DATEV file: its header, the asset field
// it reads (Source), whether a row without the value is rejected and the
// format of the cell.
type DATEVColumn struct {
	Header   string `json:"header"`
	Source   string `json:"source"`
	Required bool   `json:"required"`
	Format   string `json:"format,omitempty"`
}

// DATEVMapping is the column list of the DATEV file.
type DATEVMapping struct {
	Columns []DATEVColumn `json:"columns"`
}

// datevSource is a source field of the mapping: the asset DTO paths whose
// field permissions decide whether the creator reads it (all of them), its
// value and the formats it may have (the first is the default).
type datevSource struct {
	paths   []string
	formats []string
	value   func(*asset.DATEVRow) string
}

// Source fields of the mapping.
const (
	sourceInventoryNo = "inventory_no"
	sourceCurrency    = "currency"
	sourceContract    = "contract"
)

var textOnly = []string{DATEVFormatText}

// datevSources are the source fields a mapping may name (E-50) plus the
// contract number, the serial and the invoice number for an override; the
// supplier is the name of supplier_id (E-41), the location the name of
// location_id, the contract the number of contract_id.
var datevSources = map[string]datevSource{
	sourceInventoryNo: {paths: []string{"inventory_no"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.InventoryNo }},
	"name":            {paths: []string{"name"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.Name }},
	"manufacturer":    {paths: []string{"manufacturer"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.Manufacturer }},
	"model":           {paths: []string{"model"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.Model }},
	"serial_number":   {paths: []string{"serial_number"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.SerialNumber }},
	"invoice_number":  {paths: []string{"invoice_number"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.InvoiceNumber }},
	"purchase_date": {paths: []string{"purchase_date"}, formats: []string{DATEVFormatDate, DATEVFormatText},
		value: func(r *asset.DATEVRow) string { return r.PurchaseDate }},
	"purchase_price": {paths: []string{"purchase_price"}, formats: []string{DATEVFormatAmount, DATEVFormatText},
		value: func(r *asset.DATEVRow) string { return r.PurchasePrice }},
	sourceCurrency: {paths: []string{"currency"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.Currency }},
	// The supplier name mirrors into the column supplier (E-41): both
	// field keys must be readable.
	"supplier":    {paths: []string{"supplier_id", "supplier"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.Supplier }},
	"cost_center": {paths: []string{"cost_center"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.CostCenter }},
	"warranty_until": {paths: []string{"warranty_until"}, formats: []string{DATEVFormatDate, DATEVFormatText},
		value: func(r *asset.DATEVRow) string { return r.WarrantyUntil }},
	"location":        {paths: []string{"location_id"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.Location }},
	"lifecycle_state": {paths: []string{"lifecycle_state"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.LifecycleState }},
	sourceContract:    {paths: []string{"contract_id"}, formats: textOnly, value: func(r *asset.DATEVRow) string { return r.Contract }},
}

// datevSourceNames are the source fields in a fixed order (the manifest
// lists the ones a mapping leaves out in this order).
var datevSourceNames = []string{
	sourceInventoryNo, "name", "manufacturer", "model", "serial_number", "invoice_number", "purchase_date",
	"purchase_price", sourceCurrency, "supplier", "cost_center", "warranty_until", "location", "lifecycle_state",
	sourceContract,
}

// DATEVMappingError refuses a mapping override (unknown source field,
// empty column list, header or format that does not fit). Its message
// names no internals, so a failed job keeps it (jobs.PublicError, F-372).
type DATEVMappingError struct {
	Reason string
}

// validation: a fixed text with the reason.
func (e *DATEVMappingError) Error() string {
	return "invalid DATEV mapping in organization settings (" + SettingDATEVMapping + "): " + e.Reason
}

// PublicMessage is the error of a failed DATEV job.
func (e *DATEVMappingError) PublicMessage() string { return e.Error() }

// ErrDATEVUnavailable fails a DATEV job of a worker without asset source
// (memory profile): the export reads assets from PostgreSQL only.
var ErrDATEVUnavailable = &datevUnavailableError{}

type datevUnavailableError struct{}

func (*datevUnavailableError) Error() string {
	return "the DATEV export needs the asset database, which this installation does not provide"
}

// PublicMessage is the error of a failed DATEV job.
func (e *datevUnavailableError) PublicMessage() string { return e.Error() }

// defaultDATEVMapping is the parsed default mapping.
var defaultDATEVMapping = func() *DATEVMapping {
	m, err := decodeDATEVMapping(defaultDATEVMappingJSON)
	if err != nil {
		panic("export: default DATEV mapping: " + err.Error())
	}
	return m
}()

// DefaultDATEVMapping returns a copy of the default mapping (E-50).
func DefaultDATEVMapping() *DATEVMapping {
	return &DATEVMapping{Columns: slices.Clone(defaultDATEVMapping.Columns)}
}

// EffectiveDATEVMapping returns the mapping of an organization: the
// override in settings (SettingDATEVMapping) or, without one, the default.
// An invalid override is a *DATEVMappingError.
func EffectiveDATEVMapping(settings map[string]any) (*DATEVMapping, error) {
	raw, ok := settings[SettingDATEVMapping]
	if !ok || raw == nil {
		return DefaultDATEVMapping(), nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, &DATEVMappingError{Reason: "not a JSON object"}
	}
	return decodeDATEVMapping(encoded)
}

// decodeDATEVMapping parses and checks a mapping: at least one column,
// every header non-empty, on one line and encodable in Windows-1252, every
// source known, every format one of the source's formats (empty: its
// default).
func decodeDATEVMapping(raw []byte) (*DATEVMapping, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var m DATEVMapping
	if err := dec.Decode(&m); err != nil {
		return nil, &DATEVMappingError{Reason: "expected an object with columns of header, source, required and format"}
	}
	if len(m.Columns) == 0 {
		return nil, &DATEVMappingError{Reason: "the column list is empty"}
	}
	for i := range m.Columns {
		col := &m.Columns[i]
		col.Header = strings.TrimSpace(col.Header)
		if col.Header == "" || strings.ContainsAny(col.Header, "\r\n") {
			return nil, &DATEVMappingError{Reason: fmt.Sprintf("column %d has no header on one line", i+1)}
		}
		if !encodable(col.Header) {
			return nil, &DATEVMappingError{Reason: fmt.Sprintf("the header of column %d cannot be encoded in Windows-1252", i+1)}
		}
		src, ok := datevSources[col.Source]
		if !ok {
			return nil, &DATEVMappingError{Reason: fmt.Sprintf("column %d has the unknown source field %q", i+1, col.Source)}
		}
		if col.Format == "" {
			col.Format = src.formats[0]
		}
		if !slices.Contains(src.formats, col.Format) {
			return nil, &DATEVMappingError{Reason: fmt.Sprintf("column %d: source field %q takes the format %s",
				i+1, col.Source, strings.Join(src.formats, " or "))}
		}
	}
	return &m, nil
}

// namesSource reports whether a column of the mapping reads source.
func (m *DATEVMapping) namesSource(source string) bool {
	return slices.ContainsFunc(m.Columns, func(c DATEVColumn) bool { return c.Source == source })
}

// windows1252 encodes the DATEV file.
var windows1252 = charmap.Windows1252

// encodable reports whether Windows-1252 can encode s.
func encodable(s string) bool {
	_, err := windows1252.NewEncoder().String(s)
	return err == nil
}

// datevProjection is what the DATEV file leaves out for the asset field
// permissions of the job's creator (RBA-07, E-43, WP-729): sources no role
// of the creator may read for any asset type (closed, an empty column for
// every row) and, per asset, sources the creator may not read on it.
type datevProjection struct {
	policy     *permission.FieldPolicy
	restricted bool
	closed     map[string]bool
	manifest   Manifest
}

// newDATEVProjection resolves the closed sources of the mapping for the
// policy; a nil policy reads no field with field permissions (fail-closed).
func newDATEVProjection(m *DATEVMapping, policy *permission.FieldPolicy) *datevProjection {
	p := &datevProjection{policy: policy, restricted: policy.Restricted(permission.KindAsset), closed: map[string]bool{}}
	if p.restricted {
		keyOf := map[string][]string{}
		var keys []string
		for _, col := range m.Columns {
			for _, path := range datevSources[col.Source].paths {
				if key, structural, ok := permission.FieldKeyOf(permission.KindAsset, path); ok && !structural {
					keyOf[col.Source] = append(keyOf[col.Source], key)
					keys = append(keys, key)
				}
			}
		}
		unreadable := permission.TypeUnreadableKeys(policy, permission.KindAsset, nil, keys)
		for source, sourceKeys := range keyOf {
			if slices.ContainsFunc(sourceKeys, func(k string) bool { return slices.Contains(unreadable, k) }) {
				p.closed[source] = true
			}
		}
	}
	p.manifest = p.buildManifest(m)
	return p
}

// buildManifest lists the sources of the mapping as fields, the closed ones
// as omitted with reason field_permission, and the sources the mapping
// does not name with reason layout.
func (p *datevProjection) buildManifest(m *DATEVMapping) Manifest {
	out := Manifest{Format: FormatDATEV, Fields: []string{}, Omitted: []OmittedField{}}
	seen := map[string]bool{}
	var closed []string
	for _, col := range m.Columns {
		if seen[col.Source] {
			continue
		}
		seen[col.Source] = true
		if p.closed[col.Source] {
			closed = append(closed, col.Source)
			continue
		}
		out.Fields = append(out.Fields, col.Source)
	}
	for _, source := range datevSourceNames {
		if !seen[source] {
			out.Omitted = append(out.Omitted, OmittedField{Field: source, Reason: ReasonLayout})
		}
	}
	for _, source := range closed {
		out.Omitted = append(out.Omitted, OmittedField{Field: source, Reason: ReasonFieldPermission})
	}
	return out
}

// readable reports whether the creator reads source on the asset: not for
// a closed source, otherwise per asset (object grants, scope of an
// assignment).
func (p *datevProjection) readable(row *asset.DATEVRow, source string) bool {
	if !p.restricted {
		return true
	}
	if p.closed[source] {
		return false
	}
	ref := permission.ObjectRef{ID: row.ID, ClientID: row.ClientID, SiteID: row.SiteID}
	for _, path := range datevSources[source].paths {
		if !permission.Readable(p.policy, permission.KindAsset, row.AssetTypeID, ref, path) {
			return false
		}
	}
	return true
}

// DATEVManifest returns the manifest of a DATEV file of the mapping for
// the field permissions of policy: the preview of an open job and the
// manifest the worker stores with the file.
func DATEVManifest(m *DATEVMapping, policy *permission.FieldPolicy) Manifest {
	return newDATEVProjection(m, policy).manifest
}

// AssetSource reads the assets of the DATEV export in batches
// (asset.PGRepository.ListDATEV; the memory profile has none).
type AssetSource interface {
	ListDATEV(ctx context.Context, orgID string, q *asset.DATEVQuery) ([]asset.DATEVRow, error)
}

// OrganizationReader reads the organization of a job: its currency and
// its mapping override (tenantapi.Repository).
type OrganizationReader interface {
	GetOrganization(ctx context.Context, orgID string) (*tenantapi.Organization, error)
}

// Reasons of the error report.
const (
	datevReasonMissing    = "Pflichtfeld fehlt"
	datevReasonEncoding   = "Zeichen nicht in Windows-1252 darstellbar"
	datevReasonBadAmount  = "Betrag ungültig"
	datevReasonBadDate    = "Datum ungültig"
	datevReportIdentifier = "Inventarnummer"
)

// DATEVResult is the outcome of a DATEV rendering: the rows in the file,
// the rows only in the error report and the manifest of the file.
type DATEVResult struct {
	Rows     int
	Rejected int
	Manifest Manifest
}

// RenderDATEV writes the assets of the organization visible in the scope
// of ctx (filters client_id and lifecycle_state) to out as DATEV file of
// the mapping, and every rejected row to report (one line per failing
// column). orgCurrency fills an empty asset currency. Sources the policy
// does not let the creator read stay empty (and are not checked as
// required); a nil policy reads no field with field permissions.
func RenderDATEV(ctx context.Context, src AssetSource, orgID string, filters JobFilters, m *DATEVMapping,
	orgCurrency string, policy *permission.FieldPolicy, out, report io.Writer) (DATEVResult, error) {
	proj := newDATEVProjection(m, policy)
	res := DATEVResult{Manifest: proj.manifest}

	var line bytes.Buffer
	file := csv.NewWriter(&line)
	file.Comma = ';'
	file.UseCRLF = true
	headers := make([]string, len(m.Columns))
	for i, col := range m.Columns {
		headers[i] = neutralizeFormula(col.Header)
	}
	if err := writeEncoded(file, &line, out, headers); err != nil {
		return res, err
	}

	if _, err := io.WriteString(report, utf8BOM); err != nil {
		return res, fmt.Errorf("write datev error report: %w", err)
	}
	rep := csv.NewWriter(report)
	rep.Comma = ';'
	_ = rep.Write([]string{datevReportIdentifier, "Spalte", "Grund"})

	q := &asset.DATEVQuery{ClientID: filters.ClientID, LifecycleState: filters.LifecycleState,
		WithContract: m.namesSource(sourceContract), Limit: renderBatchSize}
	for {
		rows, err := src.ListDATEV(ctx, orgID, q)
		if err != nil {
			return res, fmt.Errorf("datev export query: %w", err)
		}
		for i := range rows {
			cells, issues := datevCells(m, proj, &rows[i], orgCurrency)
			if len(issues) > 0 {
				res.Rejected++
				id := rows[i].ID
				if proj.readable(&rows[i], sourceInventoryNo) && strings.TrimSpace(rows[i].InventoryNo) != "" {
					id = rows[i].InventoryNo
				}
				for _, is := range issues {
					_ = rep.Write([]string{neutralizeFormula(id), neutralizeFormula(is.column), is.reason})
				}
				continue
			}
			if werr := writeEncoded(file, &line, out, cells); werr != nil {
				return res, werr
			}
			res.Rows++
		}
		rep.Flush()
		if err := rep.Error(); err != nil {
			return res, fmt.Errorf("write datev error report: %w", err)
		}
		if len(rows) < renderBatchSize {
			break
		}
		last := &rows[len(rows)-1]
		q.AfterCreatedAt, q.AfterID = last.CreatedAt, last.ID
	}
	return res, nil
}

// writeEncoded writes one CSV record through line and appends it to out in
// Windows-1252; the cells are encodable (datevCells, headers checked by
// the mapping).
func writeEncoded(w *csv.Writer, line *bytes.Buffer, out io.Writer, cells []string) error {
	line.Reset()
	_ = w.Write(cells)
	w.Flush()
	if err := w.Error(); err != nil {
		return fmt.Errorf("write datev row: %w", err)
	}
	encoded, err := windows1252.NewEncoder().Bytes(line.Bytes())
	if err != nil {
		return fmt.Errorf("encode datev row: %w", err)
	}
	if _, err := out.Write(encoded); err != nil {
		return fmt.Errorf("write datev row: %w", err)
	}
	return nil
}

// datevIssue is why a row is rejected: the header of the column and the
// reason.
type datevIssue struct {
	column string
	reason string
}

// datevCells returns the cells of one asset or the issues that keep it
// out of the file.
func datevCells(m *DATEVMapping, proj *datevProjection, row *asset.DATEVRow, orgCurrency string) ([]string, []datevIssue) {
	cells := make([]string, len(m.Columns))
	var issues []datevIssue
	for i, col := range m.Columns {
		if !proj.readable(row, col.Source) {
			continue
		}
		value := strings.TrimSpace(datevSources[col.Source].value(row))
		if value == "" && col.Source == sourceCurrency {
			value = orgCurrency
		}
		if value == "" {
			if col.Required {
				issues = append(issues, datevIssue{column: col.Header, reason: datevReasonMissing})
			}
			continue
		}
		cell, reason := formatDATEVCell(col.Format, value)
		if reason == "" && !encodable(cell) {
			reason = datevReasonEncoding
		}
		if reason != "" {
			issues = append(issues, datevIssue{column: col.Header, reason: reason})
			continue
		}
		cells[i] = cell
	}
	return cells, issues
}

// numericText is the text of a PostgreSQL numeric (purchase_price::text).
var numericText = regexp.MustCompile(`^-?\d+(\.\d+)?$`)

// formatDATEVCell formats a value for its column, or names why it cannot:
// an amount gets a decimal comma without thousands separator (from the
// numeric text, no float), a date DD.MM.YYYY; both are exempt from the
// formula guard, so a negative amount does not start with '. A text cell
// runs through neutralizeFormula (F-338).
func formatDATEVCell(format, value string) (cell, reason string) {
	switch format {
	case DATEVFormatAmount:
		if !numericText.MatchString(value) {
			return "", datevReasonBadAmount
		}
		return strings.Replace(value, ".", ",", 1), ""
	case DATEVFormatDate:
		d, err := time.Parse(time.DateOnly, value)
		if err != nil {
			return "", datevReasonBadDate
		}
		return d.Format("02.01.2006"), ""
	default:
		return neutralizeFormula(value), ""
	}
}

// ErrorReportKey returns the blob key of the error report of a DATEV job,
// derived from its object key: <org>/<job>.errors.csv.
func ErrorReportKey(job *Job) string {
	key := ObjectKey(job)
	return strings.TrimSuffix(key, "."+exportExtensions[job.Format]) + ".errors.csv"
}

// errorReportContentType is the content type of the error report (UTF-8
// with BOM like the CSV export).
const errorReportContentType = "text/csv; charset=utf-8"

// datevSettings returns the effective mapping and the currency of the
// organization, read in the scope of ctx. Without an organization reader
// the default mapping applies and an empty asset currency stays empty.
func datevSettings(ctx context.Context, orgs OrganizationReader, orgID string) (*DATEVMapping, string, error) {
	if orgs == nil {
		return DefaultDATEVMapping(), "", nil
	}
	org, err := orgs.GetOrganization(ctx, orgID)
	if err != nil {
		return nil, "", fmt.Errorf("read organization of the datev export: %w", err)
	}
	m, err := EffectiveDATEVMapping(org.Settings)
	if err != nil {
		return nil, "", err
	}
	return m, org.Currency, nil
}

// renderDATEV renders a DATEV job into out and its error report into
// report with the policy of the creator.
func (w *JobWorker) renderDATEV(ctx context.Context, job *Job, policy *permission.FieldPolicy, out, report io.Writer) (int, Manifest, error) {
	if w.Assets == nil {
		return 0, Manifest{}, ErrDATEVUnavailable
	}
	m, currency, err := datevSettings(ctx, w.Orgs, job.OrganizationID)
	if err != nil {
		return 0, Manifest{}, err
	}
	res, err := RenderDATEV(ctx, w.Assets, job.OrganizationID, job.Filters, m, currency, policy, out, report)
	if err != nil {
		return 0, Manifest{}, err
	}
	return res.Rows, res.Manifest, nil
}

// isDATEVMappingError reports whether err refuses the mapping override.
func isDATEVMappingError(err error) bool {
	var me *DATEVMappingError
	return errors.As(err, &me)
}
