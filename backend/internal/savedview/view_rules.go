package savedview

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ValidateView checks a view against VIE-01: the filter (its AST in
// where and its ids, FilterSpec.Validate), the sort keys and columns are
// fields of the entity kind, sort directions are asc or desc, role ids are
// UUIDs.
func ValidateView(entityKind string, req *UpsertRequest) error {
	if entityKind == "" {
		entityKind = "ci"
	}
	if _, known := astFields[entityKind]; !known {
		return &api.FieldError{Field: "entity_kind", Message: "must be ci or asset", Status: http.StatusUnprocessableEntity}
	}
	if req.FilterSpec != nil {
		raw, err := json.Marshal(req.FilterSpec)
		if err != nil {
			return &api.FieldError{Field: "filter_spec", Message: err.Error()}
		}
		var spec FilterSpec
		if err = json.Unmarshal(raw, &spec); err != nil {
			return &api.FieldError{Field: "filter_spec", Message: "not a filter spec: " + err.Error(), Status: http.StatusUnprocessableEntity}
		}
		if err = spec.Validate(entityKind); err != nil {
			var fe *api.FieldError
			if errors.As(err, &fe) {
				return &api.FieldError{Field: "filter_spec." + fe.Field, Message: fe.Message, Status: fe.Status}
			}
			return err
		}
	}
	for i, key := range req.Sort {
		if !viewField(entityKind, key.Field) {
			return &api.FieldError{Field: fmt.Sprintf("sort[%d].field", i), Message: "unknown field " + key.Field, Status: http.StatusUnprocessableEntity}
		}
		if key.Dir == "" {
			req.Sort[i].Dir = "asc"
		} else if key.Dir != "asc" && key.Dir != "desc" {
			return &api.FieldError{Field: fmt.Sprintf("sort[%d].dir", i), Message: "must be asc or desc", Status: http.StatusUnprocessableEntity}
		}
	}
	for i, col := range req.Columns {
		if !viewField(entityKind, col) {
			return &api.FieldError{Field: fmt.Sprintf("columns[%d]", i), Message: "unknown field " + col, Status: http.StatusUnprocessableEntity}
		}
	}
	for i, role := range req.SharedWithRoles {
		if !isUUID(role) {
			return &api.FieldError{Field: fmt.Sprintf("shared_with_roles[%d]", i), Message: "must be a role id", Status: http.StatusUnprocessableEntity}
		}
	}
	return nil
}

// viewField reports whether field may be shown or sorted in a view of the
// entity kind: a whitelisted column, an attribute or the id.
func viewField(entityKind, field string) bool {
	if field == "id" {
		return true
	}
	if key, ok := strings.CutPrefix(field, "attributes."); ok {
		return validAttributeKey(key)
	}
	_, known := astFields[entityKind][field]
	return known
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F'):
			return false
		}
	}
	return true
}
