// Package export provides CSV and JSON export for Reticora CMDB.
package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
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
}

// NewHandler creates a new export handler.
func NewHandler(ciRepo ci.Repository) *Handler {
	return &Handler{ciRepo: ciRepo, batchSize: streamBatchSize}
}

// RegisterRoutes registers export routes.
func (h *Handler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/export/cis", h.ExportCIs)
}

// ExportCIs handles GET /api/v1/export/cis?format=csv|json|datev.
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

	filter := ci.FilterParams{
		Status:   r.URL.Query().Get("status"),
		TypeID:   r.URL.Query().Get("ci_type_id"),
		ClientID: r.URL.Query().Get("client_id"),
		SortBy:   "created_at",
		SortDir:  "asc",
	}

	// Probe the first batch before any header is written so that a repository
	// failure can still be reported as a proper problem+json response.
	first, err := h.fetch(r, t.OrganizationID, filter, nil)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	// The manifest names the CI fields the file leaves out (EXP-01).
	w.Header().Set("X-Export-Omitted-Fields", strings.Join(ManifestFor(format).omittedNames(), ","))
	writer := newRowWriter(format, w)
	writer.begin()

	batch := first
	for {
		for _, item := range batch {
			writer.row(item)
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

func (h *Handler) fetch(r *http.Request, orgID string, filter ci.FilterParams, cursor *api.Cursor) ([]ci.Item, error) {
	items, _, err := h.ciRepo.List(r.Context(), orgID, filter, api.PaginationParams{
		Limit:  h.batchSize,
		Cursor: cursor,
	})
	return items, err
}

// rowWriter serialises exported rows incrementally for one output format.
type rowWriter struct {
	format  string
	csv     *csv.Writer
	json    *json.Encoder
	out     io.Writer
	written int
	failed  bool
}

func newRowWriter(format string, w http.ResponseWriter) *rowWriter {
	rw := newFormatWriter(format, w)
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
func newFormatWriter(format string, out io.Writer) *rowWriter {
	rw := &rowWriter{format: format, out: out}
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
		_ = rw.csv.Write(csvColumns)
	case "datev":
		_, _ = io.WriteString(rw.out, utf8BOM)
		_ = rw.csv.Write([]string{"Inventarnummer", "Bezeichnung", "Hersteller", "Modell", "Seriennummer", "Anschaffungsdatum", "Standort", "Kostenstelle", "Status"})
	default:
		_, _ = io.WriteString(rw.out, `{"data":[`)
	}
}

func (rw *rowWriter) row(item ci.Item) {
	switch rw.format {
	case "csv":
		rw.writeCells([]string{
			item.ID,
			item.Name,
			item.Status,
			item.CITypeID,
			item.Manufacturer,
			item.Model,
			item.SerialNumber,
			item.ManagementIP,
			item.FirmwareVersion,
			item.DiscoverySource,
			item.CreatedAt,
			item.UpdatedAt,
		})
	case "datev":
		rw.writeCells([]string{
			item.ID,
			item.Name,
			item.Manufacturer,
			item.Model,
			item.SerialNumber,
			attributeString(item.Attributes, "purchase_date", "acquisition_date", "anschaffungsdatum"),
			attributeString(item.Attributes, "location", "standort"),
			attributeString(item.Attributes, "cost_center", "kostenstelle"),
			item.Status,
		})
	default:
		if rw.written > 0 {
			_, _ = io.WriteString(rw.out, ",")
		}
		_ = rw.json.Encode(item)
	}
	rw.written++
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
		_, _ = io.WriteString(rw.out, fmt.Sprintf(`],"total":%d}`, rw.written))
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

// csvColumns are the CI fields of the CSV export, in this order.
var csvColumns = []string{"id", "name", "status", "ci_type_id", "manufacturer", "model", "serial_number", "management_ip",
	"firmware_version", "discovery_source", "created_at", "updated_at"}

// datevFields are the CI fields the DATEV layout reads.
var datevFields = []string{"id", "name", "manufacturer", "model", "serial_number", "attributes", "status"}

// Manifest describes an export (EXP-01): the CI fields it carries and the
// visible CI fields it leaves out, each with the reason.
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

// ManifestFor returns the manifest of an export format. JSON carries every
// visible field; the CSV layouts carry their columns and list the rest.
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
			m.Omitted = append(m.Omitted, OmittedField{Field: f, Reason: "not part of the " + format + " layout"})
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
