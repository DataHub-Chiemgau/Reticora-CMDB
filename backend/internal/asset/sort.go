package asset

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// sortColumn is the SQL of a sortable field: the expression (a column, a
// nullable one as text with the empty string for NULL) and its PostgreSQL type. Only
// expressions of sortColumns are interpolated into SQL.
type sortColumn struct {
	expr, cast string
}

// sortColumns maps the sortable fields (API names, AST-01) onto their SQL
// (WP-740). A nullable column sorts as text, NULL as the empty string like sortValue, so
// keyset cursors of both repositories agree.
var sortColumns = map[string]sortColumn{
	"created_at":      {"created_at", "timestamptz"},
	"updated_at":      {"updated_at", "timestamptz"},
	"name":            {"name", "text"},
	"inventory_no":    {"inventory_no", "text"},
	"category":        {"category", "text"},
	"status":          {"status", "text"},
	"manufacturer":    {"COALESCE(manufacturer, '')", "text"},
	"model":           {"COALESCE(model, '')", "text"},
	"serial_number":   {"COALESCE(serial_number, '')", "text"},
	"lifecycle_state": {"COALESCE(lifecycle_state, '')", "text"},
	"purchase_date":   {"COALESCE(purchase_date::text, '')", "text"},
	"warranty_until":  {"COALESCE(warranty_until::text, '')", "text"},
}

// filterColumns are the fields of filter[field][op] with their kind
// (WP-740); the field is the column.
var filterColumns = map[string]string{
	"inventory_no":    api.KindText,
	"barcode":         api.KindText,
	"rfid_tag":        api.KindText,
	"serial_number":   api.KindText,
	"manufacturer":    api.KindText,
	"model":           api.KindText,
	"cost_center":     api.KindText,
	"supplier_id":     api.KindUUID,
	"contract_id":     api.KindUUID,
	"owner_user_id":   api.KindUUID,
	"owner_team_id":   api.KindUUID,
	"lifecycle_state": api.KindText,
	"location_id":     api.KindUUID,
	"client_id":       api.KindUUID,
	"purchase_date":   api.KindTime,
	"warranty_until":  api.KindTime,
}

// ListFields declares the list query of GET /api/v1/assets (API-02,
// WP-740): the filterable fields, the sort whitelist (created_at, the
// default, first) and the selectable response fields (the members of the
// asset DTO in the list).
var ListFields = api.ListFields{
	Filterable: filterColumns,
	Sortable: []string{
		"created_at", "updated_at", "name", "inventory_no", "category", "status", "manufacturer", "model",
		"serial_number", "lifecycle_state", "purchase_date", "warranty_until",
	},
	Selectable: listMembers(),
}

// listMembers are the JSON members of the asset DTO without the
// primary_ci of GET /api/v1/assets/{id}.
func listMembers() []string {
	t := reflect.TypeOf(Asset{})
	out := make([]string, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name != "" && name != "-" && name != "primary_ci" {
			out = append(out, name)
		}
	}
	return out
}

// NormalizeSort maps the requested sort onto the column/direction pair:
// created_at descending by default. The handler refuses an unknown field
// (422, api.ParseListQuery); the repositories check it again (checkSort).
func NormalizeSort(filter *FilterParams) (column, direction string) {
	column = "created_at"
	if _, ok := sortColumns[filter.SortBy]; ok {
		column = filter.SortBy
	}
	direction = "desc"
	if strings.EqualFold(filter.SortDir, "asc") {
		direction = "asc"
	}
	return column, direction
}

// checkSort refuses a sort field outside the whitelist (422), instead of
// the silent fallback of the former list.
func checkSort(filter *FilterParams) error {
	if _, ok := sortColumns[filter.SortBy]; filter.SortBy != "" && !ok {
		return &api.FieldError{Field: "sort", Status: http.StatusUnprocessableEntity,
			Message: fmt.Sprintf("%q is not sortable; allowed: %s", filter.SortBy, strings.Join(ListFields.Sortable, ", "))}
	}
	return nil
}

// sortValue returns the value of the sort column for one item, formatted the
// same way for the in-memory and the PostgreSQL repository so cursors are
// interchangeable.
func sortValue(item *Asset, column string) string {
	switch column {
	case "inventory_no":
		return item.AssetTag
	case "name":
		return item.Name
	case "category":
		return item.Category
	case "status":
		return item.Status
	case "manufacturer":
		return item.Manufacturer
	case "model":
		return item.Model
	case "serial_number":
		return item.SerialNumber
	case "lifecycle_state":
		return item.LifecycleState
	case "purchase_date":
		return item.PurchaseDate
	case "warranty_until":
		return item.WarrantyEnd
	case "updated_at":
		return item.UpdatedAt.UTC().Format(time.RFC3339Nano)
	default:
		return item.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
}

// filterValue returns the value of a filter field of the item, as the
// in-memory repository compares it.
func filterValue(item *Asset, field string) string {
	switch field {
	case "inventory_no":
		return item.AssetTag
	case "barcode":
		return item.Barcode
	case "rfid_tag":
		return item.RFIDTag
	case "serial_number":
		return item.SerialNumber
	case "manufacturer":
		return item.Manufacturer
	case "model":
		return item.Model
	case "cost_center":
		return item.CostCenter
	case "supplier_id":
		return item.SupplierID
	case "contract_id":
		return item.ContractID
	case "owner_user_id":
		return item.OwnerUserID
	case "owner_team_id":
		return item.OwnerTeamID
	case "lifecycle_state":
		return item.LifecycleState
	case "location_id":
		return item.LocationID
	case "client_id":
		return item.ClientID
	case "purchase_date":
		return item.PurchaseDate
	case "warranty_until":
		return item.WarrantyEnd
	}
	return ""
}

// NextCursor returns the opaque cursor pointing after the last returned item,
// or an empty string when the page was not full (i.e. no more rows follow).
func NextCursor(items []Asset, filter *FilterParams, limit int) string {
	if len(items) == 0 || len(items) < limit {
		return ""
	}
	column, direction := NormalizeSort(filter)
	last := &items[len(items)-1]
	return api.EncodeCursor(api.Cursor{
		Sort:  column,
		Dir:   direction,
		Value: sortValue(last, column),
		ID:    last.ID,
	})
}
