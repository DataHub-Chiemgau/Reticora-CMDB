package importer

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
)

// ConsumableSKUTarget is the identity of a consumable row: the SKU, unique
// per organization (000045).
const ConsumableSKUTarget = "sku"

// consumableText are the text fields of a consumable a row writes, by API
// name.
var consumableText = map[string]func(c *consumable.Consumable) *string{
	"name":        func(c *consumable.Consumable) *string { return &c.Name },
	"barcode":     func(c *consumable.Consumable) *string { return &c.Barcode },
	"supplier_id": func(c *consumable.Consumable) *string { return &c.SupplierID },
	"category":    func(c *consumable.Consumable) *string { return &c.Category },
	"unit":        func(c *consumable.Consumable) *string { return &c.Unit },
	"notes":       func(c *consumable.Consumable) *string { return &c.Notes },
	"client_id":   func(c *consumable.Consumable) *string { return &c.ClientID },
}

// consumableReadOnly are the stock columns of a consumable: stock changes
// only through bookings (WP-283, WP-756).
var consumableReadOnly = map[string]bool{"stock_level": true, "on_hand": true, "reserved": true, "location_id": true}

// consumableUniqueFields are the unique constraints of a consumable row
// the repository leaves unmapped.
var consumableUniqueFields = map[string]string{
	"consumable_organization_id_barcode_key":    "barcode",
	"barcode_registry_organization_id_code_key": "barcode",
}

// FindConsumables implements EntityStore.
func (r *PGRepository) FindConsumables(ctx context.Context, orgID, sku string) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM consumable WHERE organization_id = $1 AND sku = $2 ORDER BY id LIMIT $3`,
		orgID, sku, findLimit)
}

// ConsumableWriter writes the rows of the entity consumable (WP-776,
// module stocktake): the identity is the SKU; the consumable is created
// or changed through consumable.Repository in the row's transaction under
// the scope of the starter, its stock never (stock_level changes only
// through bookings: read_only_field), and every written row gets one
// audit entry consumable.created or consumable.updated with source import.
type ConsumableWriter struct {
	repo  consumable.Repository
	store EntityStore
}

// NewConsumableWriter returns the writer of consumable rows.
func NewConsumableWriter(repo consumable.Repository, store EntityStore) *ConsumableWriter {
	return &ConsumableWriter{repo: repo, store: store}
}

// Entity implements Writer.
func (w *ConsumableWriter) Entity() string { return "consumable" }

// consumableRow is a consumable row after its parsing.
type consumableRow struct {
	sku      string
	texts    map[string]string
	minLevel *float64
	notes    []Issue
}

// parseConsumable reads the targets of a consumable row.
func parseConsumable(row *WriteRow) (*consumableRow, *RowResult) {
	r := &consumableRow{texts: map[string]string{}}
	refuse := func(field, msg string) (*consumableRow, *RowResult) {
		res := failed(row.Line, field, CodeInvalidValue, msg)
		return nil, &res
	}
	for target := range row.Values.Values {
		text := rowText(row.Values, target)
		switch {
		case target == ConsumableSKUTarget:
			r.sku = text
		case consumableText[target] != nil:
			if text != "" {
				r.texts[target] = text
			}
		case target == "min_level":
			if text != "" {
				f, err := strconv.ParseFloat(strings.Replace(text, ",", ".", 1), 64)
				if err != nil || f < 0 {
					return refuse(target, fmt.Sprintf("%q is no number of at least 0", text))
				}
				r.minLevel = &f
			}
		case consumableReadOnly[target]:
			if text != "" {
				r.notes = append(r.notes, Issue{Field: target, Code: CodeReadOnlyField,
					Message: "the stock of a consumable changes through bookings (POST /api/v1/stock-movements), not through an import"})
			}
		default:
			return refuse(target, fmt.Sprintf("%s is no field of a consumable", target))
		}
	}
	if r.sku == "" {
		return refuse(ConsumableSKUTarget, "the sku of the consumable is required (its identity)")
	}
	for _, field := range []string{"client_id", "supplier_id"} {
		if v := r.texts[field]; v != "" && !api.IsUUID(v) {
			return refuse(field, field+" must be a UUID")
		}
	}
	sortIssues(r.notes)
	return r, nil
}

// Write implements Writer.
func (w *ConsumableWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	r, refusal := parseConsumable(row)
	if refusal != nil {
		return *refusal, nil
	}
	ids, err := w.store.FindConsumables(ctx, row.Run.OrganizationID, r.sku)
	if err != nil {
		return RowResult{}, err
	}
	mode := row.Run.Mode
	switch {
	case len(ids) > 1:
		return failed(row.Line, ConsumableSKUTarget, CodeAmbiguousIdentity, fmt.Sprintf("%d consumables have the sku of the row", len(ids))), nil
	case len(ids) == 1 && mode == "create_missing":
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the consumable %s exists (mode create_missing)", ids[0])}, nil
	case len(ids) == 1:
		return w.update(ctx, row, r, ids[0])
	case mode == "update_existing":
		return RowResult{Outcome: OutcomeSkipped, Message: "no consumable has the sku of the row (mode update_existing)",
			Issues: []Issue{{Field: ConsumableSKUTarget, Code: CodeNoIdentityMatch, Message: "no consumable has the sku of the row"}}}, nil
	}
	return w.create(ctx, row, r)
}

// create creates the consumable of a row.
func (w *ConsumableWriter) create(ctx context.Context, row *WriteRow, r *consumableRow) (RowResult, error) {
	if r.texts["name"] == "" {
		return failed(row.Line, "name", CodeInvalidValue, "name is required to create a consumable"), nil
	}
	c := &consumable.Consumable{OrganizationID: row.Run.OrganizationID, SKU: r.sku}
	changes := map[string]any{ConsumableSKUTarget: r.sku}
	for field, value := range r.texts {
		*consumableText[field](c) = value
		changes[field] = value
	}
	if r.minLevel != nil {
		c.MinLevel = *r.minLevel
		changes["min_level"] = *r.minLevel
	}
	if err := w.repo.Create(ctx, c); err != nil {
		if res, ok := refusalOf(row.Line, err, consumableUniqueFields); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	actor, actorType := auditActor(ctx, row.Run)
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: c.OrganizationID, ActorID: actor, ActorType: actorType,
		Action: "consumable.created", ResourceType: "consumable", ResourceID: c.ID, Changes: changes}); err != nil {
		return RowResult{}, err
	}
	return RowResult{Outcome: OutcomeCreated, Message: c.ID, Issues: r.notes}, nil
}

// update changes the consumable id with the fields of the row that differ.
func (w *ConsumableWriter) update(ctx context.Context, row *WriteRow, r *consumableRow, id string) (RowResult, error) {
	orgID, line := row.Run.OrganizationID, row.Line
	current, err := w.repo.GetByID(ctx, orgID, id)
	if errors.Is(err, consumable.ErrNotFound) {
		return failed(line, "", CodeOutOfScope, "the consumable lies outside the scope of the user who started the import"), nil
	}
	if err != nil {
		return RowResult{}, err
	}
	req := consumable.UpdateConsumableRequest{}
	changes := map[string]any{}
	for field, value := range r.texts {
		old := *consumableText[field](current)
		// Ids compare without regard to case.
		if old == value || (field == "client_id" || field == "supplier_id") && strings.EqualFold(old, value) {
			continue
		}
		v := value
		switch field {
		case "name":
			req.Name = &v
		case "barcode":
			req.Barcode = &v
		case "supplier_id":
			req.SupplierID = &v
		case "category":
			req.Category = &v
		case "unit":
			req.Unit = &v
		case "notes":
			req.Notes = &v
		case "client_id":
			req.ClientID = &v
		}
		changes[field] = map[string]any{"old": old, "new": value}
	}
	if r.minLevel != nil && *r.minLevel != current.MinLevel {
		req.MinLevel = r.minLevel
		changes["min_level"] = map[string]any{"old": current.MinLevel, "new": *r.minLevel}
	}
	if len(changes) == 0 {
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the consumable %s is unchanged", id), Issues: r.notes}, nil
	}
	if _, err = w.repo.Update(ctx, orgID, id, req); err != nil {
		if errors.Is(err, consumable.ErrNotFound) {
			return failed(line, "", CodeOutOfScope, "the consumable may not be changed in the scope of the user who started the import"), nil
		}
		if res, ok := refusalOf(line, err, consumableUniqueFields); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	actor, actorType := auditActor(ctx, row.Run)
	if auditErr := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "consumable.updated", ResourceType: "consumable", ResourceID: id, Changes: changes}); auditErr != nil {
		return RowResult{}, auditErr
	}
	return RowResult{Outcome: OutcomeUpdated, Message: id, Issues: r.notes}, nil
}
