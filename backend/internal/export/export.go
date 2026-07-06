// Package export provides CSV and JSON export for Reticora CMDB.
package export

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Handler provides HTTP handlers for export endpoints.
type Handler struct {
	ciRepo ci.Repository
}

// NewHandler creates a new export handler.
func NewHandler(ciRepo ci.Repository) *Handler {
	return &Handler{ciRepo: ciRepo}
}

// RegisterRoutes registers export routes.
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/export/cis", h.ExportCIs)
}

// ExportCIs handles GET /api/v1/export/cis?format=csv|json|datev
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

	filter := ci.FilterParams{
		Status:   r.URL.Query().Get("status"),
		TypeID:   r.URL.Query().Get("ci_type_id"),
		ClientID: r.URL.Query().Get("client_id"),
	}

	items, _, err := h.ciRepo.List(t.OrganizationID, filter, api.PaginationParams{Limit: 10000, Offset: 0})
	if err != nil {
		api.WriteError(w, http.StatusInternalServerError, "Internal Error", err.Error())
		return
	}

	switch format {
	case "csv":
		h.writeCSV(w, items)
	case "datev":
		h.writeDATEV(w, items)
	case "json":
		h.writeJSON(w, items)
	default:
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "format must be 'csv', 'datev' or 'json'")
	}
}

func (h *Handler) writeCSV(w http.ResponseWriter, items []ci.Item) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=cis_export.csv")

	writer := csv.NewWriter(w)
	defer writer.Flush()

	_ = writer.Write([]string{"id", "name", "status", "ci_type_id", "manufacturer", "model", "serial_number", "management_ip", "firmware_version", "source", "created_at", "updated_at"})
	for _, item := range items {
		_ = writer.Write([]string{
			item.ID,
			item.Name,
			item.Status,
			item.CITypeID,
			item.Manufacturer,
			item.Model,
			item.SerialNumber,
			item.ManagementIP,
			item.FirmwareVersion,
			item.Source,
			item.CreatedAt,
			item.UpdatedAt,
		})
	}
}

func (h *Handler) writeDATEV(w http.ResponseWriter, items []ci.Item) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=cis_export_datev.csv")

	writer := csv.NewWriter(w)
	writer.Comma = ';'
	defer writer.Flush()

	_ = writer.Write([]string{"Inventarnummer", "Bezeichnung", "Hersteller", "Modell", "Seriennummer", "Anschaffungsdatum", "Standort", "Kostenstelle", "Status"})
	for _, item := range items {
		_ = writer.Write([]string{
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
	}
}

func (h *Handler) writeJSON(w http.ResponseWriter, items []ci.Item) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", "attachment; filename=cis_export.json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"data":  items,
		"total": len(items),
	})
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
