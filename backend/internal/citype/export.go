package citype

import (
	"context"
	"fmt"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// ExportFormatVersion is the version of the metamodel export format.
const ExportFormatVersion = 1

// MetamodelExport is the metamodel of an organization as one JSON document
// (MET-54, phase 1): every CI type the organization sees — system and own,
// active or not — with its attribute definitions, and the global attribute
// definitions.
type MetamodelExport struct {
	FormatVersion        int                   `json:"format_version"`
	ExportedAt           time.Time             `json:"exported_at"`
	OrganizationID       string                `json:"organization_id"`
	CITypes              []Type                `json:"ci_types"`
	AttributeDefinitions []AttributeDefinition `json:"attribute_definitions"`
}

// exportPage is the page size the export reads types with.
const exportPage = 200

// BuildExport reads the metamodel of orgID.
func BuildExport(ctx context.Context, repo Repository, orgID string, now time.Time) (*MetamodelExport, error) {
	out := &MetamodelExport{FormatVersion: ExportFormatVersion, ExportedAt: now.UTC(), OrganizationID: orgID,
		CITypes: []Type{}, AttributeDefinitions: []AttributeDefinition{}}
	for offset := 0; ; offset += exportPage {
		types, total, err := repo.List(ctx, orgID, FilterParams{IncludeInactive: true}, api.PaginationParams{Limit: exportPage, Offset: offset})
		if err != nil {
			return nil, fmt.Errorf("list ci types: %w", err)
		}
		for i := range types {
			full, err := repo.GetByID(ctx, orgID, types[i].ID)
			if err != nil {
				return nil, fmt.Errorf("read ci type %s: %w", types[i].Key, err)
			}
			out.CITypes = append(out.CITypes, *full)
		}
		if len(types) == 0 || offset+len(types) >= total {
			break
		}
	}
	if store, ok := repo.(AttributeDefinitionStore); ok {
		defs, err := store.ListAttributeDefinitions(ctx, orgID, AttributeFilter{Scope: "global"})
		if err != nil {
			return nil, fmt.Errorf("list attribute definitions: %w", err)
		}
		out.AttributeDefinitions = append(out.AttributeDefinitions, defs...)
	}
	return out, nil
}
