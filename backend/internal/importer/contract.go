package importer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contract"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// ContractNumberTarget is the identity of a contract row: the number,
// unique per organization (WP-269).
const ContractNumberTarget = "number"

// contractOptional are the optional text fields of a contract a row
// writes, by API name.
var contractOptional = map[string]func(c *contract.Contract) **string{
	"supplier_id": func(c *contract.Contract) **string { return &c.SupplierID },
	"kind":        func(c *contract.Contract) **string { return &c.Kind },
	"start_date":  func(c *contract.Contract) **string { return &c.StartDate },
	"end_date":    func(c *contract.Contract) **string { return &c.EndDate },
	"notes":       func(c *contract.Contract) **string { return &c.Notes },
}

// FindContracts implements EntityStore.
func (r *PGRepository) FindContracts(ctx context.Context, orgID, number string) ([]string, error) {
	return r.findIDs(ctx, orgID, `SELECT id::text FROM contract WHERE organization_id = $1 AND number = $2 ORDER BY id LIMIT $3`,
		orgID, number, findLimit)
}

// ContractWriter writes the rows of the entity contract (WP-776, E-105:
// module inventory): the identity is the number; the contract is created
// or changed through contract.ContractRepository in the row's transaction
// with the field permissions of the starter (object kind contract), and
// every written row gets one audit entry contract.created or
// contract.updated with source import.
type ContractWriter struct {
	repo     contract.ContractRepository
	store    EntityStore
	policies permission.PolicyResolver
}

// NewContractWriter returns the writer of contract rows.
func NewContractWriter(repo contract.ContractRepository, store EntityStore, policies permission.PolicyResolver) *ContractWriter {
	return &ContractWriter{repo: repo, store: store, policies: policies}
}

// Entity implements Writer.
func (w *ContractWriter) Entity() string { return "contract" }

// contractRow is a contract row after its parsing: the texts it sets.
type contractRow struct {
	number   string
	client   string
	texts    map[string]string
	currency string
	status   string
	notice   *int
	value    *json.Number
}

// parseContract reads the targets of a contract row.
func parseContract(row *WriteRow) (*contractRow, *RowResult) {
	r := &contractRow{texts: map[string]string{}}
	refuse := func(field, msg string) (*contractRow, *RowResult) {
		res := failed(row.Line, field, CodeInvalidValue, msg)
		return nil, &res
	}
	for target := range row.Values.Values {
		text := rowText(row.Values, target)
		switch {
		case target == ContractNumberTarget:
			r.number = text
		case target == "client_id":
			r.client = text
		case contractOptional[target] != nil:
			if text != "" {
				r.texts[target] = text
			}
		case target == "currency":
			r.currency = strings.ToUpper(text)
		case target == "status":
			r.status = strings.ToLower(text)
		case target == "notice_period_days":
			if text != "" {
				n, err := strconv.Atoi(text)
				if err != nil {
					return refuse(target, fmt.Sprintf("%q is no integer", text))
				}
				r.notice = &n
			}
		case target == "value":
			if text != "" {
				text = strings.Replace(text, ",", ".", 1)
				if _, err := strconv.ParseFloat(text, 64); err != nil {
					return refuse(target, fmt.Sprintf("%q is no number", text))
				}
				n := json.Number(text)
				r.value = &n
			}
		default:
			return refuse(target, fmt.Sprintf("%s is no field of a contract", target))
		}
	}
	if r.number == "" {
		return refuse(ContractNumberTarget, "the number of the contract is required (its identity)")
	}
	if r.client != "" && !api.IsUUID(r.client) {
		return refuse("client_id", "client_id must be a UUID")
	}
	return r, nil
}

// paths are the DTO paths of the fields the row sets.
func (r *contractRow) paths() []string {
	var out []string
	for field := range r.texts {
		out = append(out, field)
	}
	for _, f := range []struct {
		path string
		set  bool
	}{{"currency", r.currency != ""}, {"status", r.status != ""}, {"notice_period_days", r.notice != nil}, {"value", r.value != nil}} {
		if f.set {
			out = append(out, f.path)
		}
	}
	sort.Strings(out)
	return out
}

// Write implements Writer.
func (w *ContractWriter) Write(ctx context.Context, row *WriteRow) (RowResult, error) {
	policy, err := runPolicy(ctx, row.Run, w.policies)
	if err != nil {
		return RowResult{}, err
	}
	ctx = permission.WithPolicy(ctx, policy)
	r, refusal := parseContract(row)
	if refusal != nil {
		return *refusal, nil
	}
	ids, err := w.store.FindContracts(ctx, row.Run.OrganizationID, r.number)
	if err != nil {
		return RowResult{}, err
	}
	mode := row.Run.Mode
	switch {
	case len(ids) > 1:
		return failed(row.Line, ContractNumberTarget, CodeAmbiguousIdentity, fmt.Sprintf("%d contracts have the number of the row", len(ids))), nil
	case len(ids) == 1 && mode == "create_missing":
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the contract %s exists (mode create_missing)", ids[0])}, nil
	case len(ids) == 1:
		return w.update(ctx, policy, row, r, ids[0])
	case mode == "update_existing":
		return RowResult{Outcome: OutcomeSkipped, Message: "no contract has the number of the row (mode update_existing)",
			Issues: []Issue{{Field: ContractNumberTarget, Code: CodeNoIdentityMatch, Message: "no contract has the number of the row"}}}, nil
	}
	return w.create(ctx, policy, row, r)
}

// create creates the contract of a row; a missing currency is the
// organization's, a missing status active.
func (w *ContractWriter) create(ctx context.Context, policy *permission.FieldPolicy, row *WriteRow, r *contractRow) (RowResult, error) {
	c := &contract.Contract{OrganizationID: row.Run.OrganizationID, Number: r.number, Currency: r.currency,
		Status: contract.DefaultContractStatus, NoticePeriodDays: r.notice, Value: r.value}
	if r.client != "" {
		c.ClientID = &r.client
	}
	if r.status != "" {
		c.Status = r.status
	}
	changes := map[string]any{ContractNumberTarget: r.number}
	for field, value := range r.texts {
		v := value
		*contractOptional[field](c) = &v
		changes[field] = value
	}
	ref := permission.ObjectRef{ClientID: r.client}
	if err := permission.CheckWrite(policy, permission.KindContract, "", ref, r.paths()); err != nil {
		res, _ := refusalOf(row.Line, err, nil)
		return res, nil
	}
	if err := w.repo.CreateContract(ctx, c); err != nil {
		if res, ok := refusalOf(row.Line, err, nil); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	for field, value := range map[string]any{"client_id": c.ClientID, "currency": c.Currency, "status": c.Status,
		"notice_period_days": c.NoticePeriodDays, "value": c.Value} {
		changes[field] = value
	}
	actor, actorType := auditActor(ctx, row.Run)
	if err := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: c.OrganizationID, ActorID: actor, ActorType: actorType,
		Action: "contract.created", ResourceType: "contract", ResourceID: c.ID, Changes: changes}); err != nil {
		return RowResult{}, err
	}
	return RowResult{Outcome: OutcomeCreated, Message: c.ID}, nil
}

// optionalText is the text of an optional field, "" without one.
func optionalText(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// update changes the contract id with the fields of the row that differ.
func (w *ContractWriter) update(ctx context.Context, policy *permission.FieldPolicy, row *WriteRow, r *contractRow, id string) (RowResult, error) {
	orgID, line := row.Run.OrganizationID, row.Line
	current, err := w.repo.GetContract(ctx, orgID, id)
	if errors.Is(err, contract.ErrContractNotFound) {
		return failed(line, "", CodeOutOfScope, "the contract lies outside the scope of the user who started the import"), nil
	}
	if err != nil {
		return RowResult{}, err
	}
	if r.client != "" && !strings.EqualFold(r.client, optionalText(current.ClientID)) {
		return failed(line, "client_id", CodeInvalidValue, "the client of a contract is not changed by an import"), nil
	}
	ref := permission.ObjectRef{ID: current.ID, ClientID: optionalText(current.ClientID)}
	req := &contract.UpdateContractRequest{}
	changes := map[string]any{}
	var paths []string
	mark := func(path string, old, value any, differs bool) {
		if differs {
			changes[path] = map[string]any{"old": old, "new": value}
		}
		if differs || !permission.Readable(policy, permission.KindContract, "", ref, path) {
			paths = append(paths, path)
		}
	}
	for field, value := range r.texts {
		old := optionalText(*contractOptional[field](current))
		v := value
		differs := old != value
		if differs {
			opt := contract.Optional{Set: true, Value: &v}
			switch field {
			case "supplier_id":
				req.SupplierID = opt
			case "kind":
				req.Kind = opt
			case "start_date":
				req.StartDate = opt
			case "end_date":
				req.EndDate = opt
			case "notes":
				req.Notes = opt
			}
		}
		mark(field, old, value, differs)
	}
	if r.currency != "" {
		v := r.currency
		differs := v != current.Currency
		if differs {
			req.Currency = contract.Optional{Set: true, Value: &v}
		}
		mark("currency", current.Currency, v, differs)
	}
	if r.status != "" {
		v := r.status
		differs := v != current.Status
		if differs {
			req.Status = contract.Optional{Set: true, Value: &v}
		}
		mark("status", current.Status, v, differs)
	}
	if r.notice != nil {
		differs := current.NoticePeriodDays == nil || *current.NoticePeriodDays != *r.notice
		if differs {
			req.NoticePeriodDays = contract.Nullable[int]{Set: true, Value: r.notice}
		}
		mark("notice_period_days", current.NoticePeriodDays, *r.notice, differs)
	}
	if r.value != nil {
		differs := current.Value == nil || !sameNumber(*current.Value, *r.value)
		if differs {
			req.Value = contract.Nullable[json.Number]{Set: true, Value: r.value}
		}
		mark("value", current.Value, *r.value, differs)
	}
	sort.Strings(paths)
	if err = permission.CheckWrite(policy, permission.KindContract, "", ref, paths); err != nil {
		res, _ := refusalOf(line, err, nil)
		return res, nil
	}
	if len(changes) == 0 {
		return RowResult{Outcome: OutcomeSkipped, Message: fmt.Sprintf("the contract %s is unchanged", id)}, nil
	}
	anyVersion := api.IfMatchAny
	req.IfMatch = &anyVersion
	if _, err = w.repo.UpdateContract(ctx, orgID, id, req); err != nil {
		if errors.Is(err, contract.ErrContractNotFound) {
			return failed(line, "", CodeOutOfScope, "the contract may not be changed in the scope of the user who started the import"), nil
		}
		if res, ok := refusalOf(line, err, nil); ok {
			return res, nil
		}
		return RowResult{}, err
	}
	actor, actorType := auditActor(ctx, row.Run)
	if auditErr := w.store.RecordRowAudit(ctx, &audit.Entry{OrganizationID: orgID, ActorID: actor, ActorType: actorType,
		Action: "contract.updated", ResourceType: "contract", ResourceID: id, Changes: changes}); auditErr != nil {
		return RowResult{}, auditErr
	}
	return RowResult{Outcome: OutcomeUpdated, Message: id}, nil
}

// sameNumber compares two decimals by value (1200 and 1200.00 are equal).
func sameNumber(a, b json.Number) bool {
	fa, errA := strconv.ParseFloat(a.String(), 64)
	fb, errB := strconv.ParseFloat(b.String(), 64)
	if errA != nil || errB != nil {
		return a == b
	}
	return fa == fb
}
