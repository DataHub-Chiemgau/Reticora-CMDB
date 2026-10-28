package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
)

// Route snapshots (AUD-07, WP-505): a write route without a domain recorder
// names in this register the table row it changes. The route audit reads
// the row before the handler runs and after a successful response, in the
// principal's tenant scope, and stores per changed column {field, old, new}
// in the changes ("values"): a create the new values of the row the
// response names, a delete the old ones. Excluded columns (secrets,
// bookkeeping) never appear; the values of personal columns (CH16, AUD-04)
// appear only as a surrogate naming the row they belong to.

// SurrogateKey marks the surrogate of a personal value in the route audit:
// {"$surrogate": "<row id>"} stands for the value the row (a contact, a
// user) held; the value itself is not stored. A null value stays null.
const SurrogateKey = "$surrogate"

// maxSnapshotValue bounds the JSON form of one stored value; a larger one
// is stored as {"$omitted": <bytes>}.
const maxSnapshotValue = 8 << 10

// SnapshotKey selects the row of a route snapshot: Column equals the value
// From names. From is a route parameter ("id"), a field of the JSON
// response ("@id": the created row), the organization ("$org") or the
// acting user ("$user").
type SnapshotKey struct {
	Column string
	From   string
}

// RouteSnapshot is the register entry of a write route without a domain
// recorder: the table of the row the route changes and how the row is
// found, or the reason the route changes no row whose values the audit
// could show.
type RouteSnapshot struct {
	Table string
	// Alt is a second table for a route whose row lives in one of two
	// tables (role assignments).
	Alt  string
	Keys []SnapshotKey
	// Where is a further condition of the row (SQL, a constant).
	Where string
	// Select is the JSON object of the row (SQL over t, a constant) when
	// the values live in rows of another table; default to_jsonb(t).
	Select string
	// NoValues is why the route has no row values (runs, verifications,
	// notices); the entry keeps the route, the request and the fields.
	NoValues string
}

// FromResponse reports whether the row is the one the response names (a
// create): it has no state before the handler.
func (s *RouteSnapshot) FromResponse() bool {
	for _, k := range s.Keys {
		if strings.HasPrefix(k.From, "@") {
			return true
		}
	}
	return false
}

// KeyValues resolves the values of the row's keys; lookup answers a From.
// It reports false when a key has no value or a value its column cannot
// hold (an id that is no UUID): there is no row to read.
func (s *RouteSnapshot) KeyValues(lookup func(from string) string) ([]any, bool) {
	if s.Table == "" || len(s.Keys) == 0 {
		return nil, false
	}
	out := make([]any, len(s.Keys))
	for i, k := range s.Keys {
		v := lookup(k.From)
		if v == "" || (isIDColumn(k.Column) && !IsUUID(v)) {
			return nil, false
		}
		out[i] = v
	}
	return out, true
}

func isIDColumn(column string) bool { return column == "id" || strings.HasSuffix(column, "_id") }

// tableRule names the columns of a table the route audit treats apart:
// Exclude never appears (secrets, opaque payloads), Personal appears only
// as a surrogate. Columns named like a secret and the personal field names
// of internal/pii are treated so in every table.
type tableRule struct {
	Exclude  []string
	Personal []string
}

var tableRules = map[string]tableRule{
	// Personal data (CH16, AUD-04): the contact id stands for name, e-mail
	// and phone.
	"contact":         {Personal: []string{"display_name", "email", "phone"}},
	"app_user":        {Personal: []string{"display_name", "email", "oidc_subject", "avatar_url"}},
	"user_invitation": {Personal: []string{"email"}},
	"assignment":      {Personal: []string{"checkout_signature", "return_signature"}},
	"disposal_record": {Personal: []string{"performed_by"}},
	// Free-form values of people and accounts.
	"form_submission":       {Personal: []string{"values"}},
	"iga_provisioning_task": {Personal: []string{"payload", "result"}},
	"iga_drift_finding":     {Personal: []string{"expected", "observed"}},
	// Secrets and configuration that may carry them.
	"webhook_subscription": {Exclude: []string{"secret", "previous_secret", "headers"}},
	"webhook_delivery":     {Exclude: []string{"payload", "response_body"}},
	"notification_channel": {Exclude: []string{"config_ciphertext", "config_dek"}},
	"credential":           {Exclude: []string{"ciphertext"}},
	"iga_connector":        {Exclude: []string{"config"}},
	"collector":            {Exclude: []string{"config"}},
}

// alwaysExcluded are bookkeeping columns of every table.
var alwaysExcluded = map[string]bool{"organization_id": true, "created_at": true, "updated_at": true}

// secretColumn matches columns named like a secret: they never appear,
// whatever the register says.
var secretColumn = regexp.MustCompile(`(secret|password|token|ciphertext|sealed|encrypted|private_key|_hash$|_dek$)`)

// excluded reports whether column of table never appears in the audit.
func excluded(table, column string) bool {
	if alwaysExcluded[column] || secretColumn.MatchString(column) {
		return true
	}
	for _, c := range tableRules[table].Exclude {
		if c == column {
			return true
		}
	}
	return false
}

// personal reports whether column of table holds personal data.
func personal(table, column string) bool {
	if pii.IsPersonalKey(column) {
		return true
	}
	for _, c := range tableRules[table].Personal {
		if c == column {
			return true
		}
	}
	return false
}

// RowValue is one changed column of a route snapshot.
type RowValue struct {
	Field string `json:"field"`
	Old   any    `json:"old"`
	New   any    `json:"new"`
}

// ValueChanges compares the row before and after the request (nil: no
// row) and returns {field, old, new} per changed column, sorted by field:
// a create gives the new values, a delete the old ones. Excluded columns
// are left out; a personal value becomes the surrogate of its row.
func (s *RouteSnapshot) ValueChanges(table string, before, after map[string]any) []map[string]any {
	if table == "" {
		table = s.Table
	}
	rowID := ""
	for _, row := range []map[string]any{after, before} {
		if id, ok := row["id"].(string); ok && rowID == "" {
			rowID = id
		}
	}
	columns := map[string]bool{}
	for k := range before {
		columns[k] = true
	}
	for k := range after {
		columns[k] = true
	}
	out := []map[string]any{}
	names := make([]string, 0, len(columns))
	for c := range columns {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		if excluded(table, c) {
			continue
		}
		oldV, newV := before[c], after[c]
		if reflect.DeepEqual(oldV, newV) {
			continue
		}
		if personal(table, c) {
			oldV, newV = surrogate(oldV, rowID), surrogate(newV, rowID)
		}
		out = append(out, map[string]any{"field": c, "old": bounded(oldV), "new": bounded(newV)})
	}
	return out
}

func surrogate(v any, rowID string) any {
	if v == nil {
		return nil
	}
	return map[string]any{SurrogateKey: rowID}
}

func bounded(v any) any {
	if v == nil {
		return nil
	}
	raw, err := json.Marshal(v)
	if err != nil || len(raw) > maxSnapshotValue {
		return map[string]any{"$omitted": len(raw)}
	}
	return v
}

// identifier is a table or column name the register may use.
var identifier = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// Validate checks an entry: a table and keys, or a reason without them.
func (s *RouteSnapshot) Validate() error {
	if s.Table == "" {
		if strings.TrimSpace(s.NoValues) == "" {
			return errors.New("neither a table nor a reason")
		}
		if len(s.Keys) != 0 || s.Alt != "" || s.Where != "" || s.Select != "" {
			return errors.New("keys without a table")
		}
		return nil
	}
	if s.NoValues != "" {
		return errors.New("a table and a reason")
	}
	if !identifier.MatchString(s.Table) || (s.Alt != "" && !identifier.MatchString(s.Alt)) {
		return fmt.Errorf("invalid table %q/%q", s.Table, s.Alt)
	}
	if len(s.Keys) == 0 {
		return errors.New("no keys")
	}
	for _, k := range s.Keys {
		if !identifier.MatchString(k.Column) || k.From == "" {
			return fmt.Errorf("invalid key %+v", k)
		}
	}
	return nil
}

// query is the statement that reads the row of table.
func (s *RouteSnapshot) query(table string) string {
	conds := make([]string, len(s.Keys))
	for i, k := range s.Keys {
		conds[i] = fmt.Sprintf("t.%s = $%d", k.Column, i+1)
	}
	if s.Where != "" {
		conds = append(conds, s.Where)
	}
	sel := "to_jsonb(t)"
	if s.Select != "" {
		sel = s.Select
	}
	return "SELECT " + sel + " FROM " + table + " t WHERE " + strings.Join(conds, " AND ") + " LIMIT 1"
}

// RouteSnapshot reads the row of spec with the key values in the tenant
// scope of the request (database.WithRequestTenant: RLS shows the row only
// if the principal sees it) and returns it with its table; nil when there
// is none. A row of another organization is none.
func (a *PGRouteAuditor) RouteSnapshot(ctx context.Context, orgID string, spec *RouteSnapshot, keys []any) (row map[string]any, table string, err error) {
	err = database.WithRequestTenant(ctx, a.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		for _, t := range []string{spec.Table, spec.Alt} {
			if t == "" {
				continue
			}
			var raw []byte
			qErr := tx.QueryRow(ctx, spec.query(t), keys...).Scan(&raw)
			if errors.Is(qErr, pgx.ErrNoRows) {
				continue
			}
			if qErr != nil {
				return fmt.Errorf("audit: snapshot of %s: %w", t, qErr)
			}
			var got map[string]any
			if qErr = json.Unmarshal(raw, &got); qErr != nil {
				return fmt.Errorf("audit: snapshot of %s: %w", t, qErr)
			}
			if org, ok := got["organization_id"].(string); ok && !strings.EqualFold(org, orgID) {
				continue
			}
			row, table = got, t
			return nil
		}
		return nil
	})
	return row, table, err
}

// RouteSnapshotFor returns the register entry of a write route
// ("METHOD pattern").
func RouteSnapshotFor(route string) (RouteSnapshot, bool) {
	s, ok := routeSnapshots[route]
	return s, ok
}

// RouteSnapshotRoutes lists the routes of the register (architecture test).
func RouteSnapshotRoutes() []string {
	out := make([]string, 0, len(routeSnapshots))
	for r := range routeSnapshots {
		out = append(out, r)
	}
	sort.Strings(out)
	return out
}

// row is the row of table the route parameter id names.
func row(table string) RouteSnapshot {
	return RouteSnapshot{Table: table, Keys: []SnapshotKey{{"id", "id"}}}
}

// created is the row of table the response's id names.
func created(table string) RouteSnapshot {
	return RouteSnapshot{Table: table, Keys: []SnapshotKey{{"id", "@id"}}}
}

func noValues(reason string) RouteSnapshot { return RouteSnapshot{NoValues: reason} }

// crud registers create, update and delete of a collection on table.
func crud(m map[string]RouteSnapshot, collection, table string, methods ...string) {
	for _, method := range methods {
		switch method {
		case "POST":
			m["POST "+collection] = created(table)
		default:
			m[method+" "+collection+"/{id}"] = row(table)
		}
	}
}

// routeSnapshots is the register: every audited write route without a
// domain recorder (server.domainRecordedRoutes) has an entry
// (TestEveryWriteRouteHasASnapshotEntry).
var routeSnapshots = func() map[string]RouteSnapshot {
	m := map[string]RouteSnapshot{}
	const v1 = "/api/v1/"
	all := []string{"POST", "PATCH", "DELETE"}
	for collection, table := range map[string]string{
		"assets": "asset", "buildings": "building", "cables": "cable", "clients": "client",
		"compliance/rules": "compliance_rule", "compositions": "composition", "consumables": "consumable",
		"contacts": "contact", "desks": "desk", "discovery-scopes": "discovery_scope", "documents": "document",
		"forms": "form_def", "iga/connectors": "iga_connector", "inventory/items": "quantity_item",
		"ip-addresses": "ip_address", "ips": "ip_address", "keys": "key_item", "maintenance-windows": "maintenance_window",
		"monitoring/alerts": "alert_rule", "notification-channels": "notification_channel", "orders": "internal_order",
		"racks": "rack", "relationships": "ci_relationship", "roles": "custom_role", "rooms": "room",
		"saved-views": "saved_view", "sites": "site", "slas": "sla", "stocktakes": "stocktake", "subnets": "subnet",
		"teams": "team", "tickets": "ticket", "trainings": "training", "users": "app_user", "vlans": "vlan", "vrfs": "vrf",
		"workflows": "workflow_def",
	} {
		crud(m, v1+collection, table, all...)
	}
	crud(m, v1+"collectors", "collector", "POST", "PATCH")
	crud(m, v1+"credentials", "credential", "DELETE")
	m["POST "+v1+"credentials/"] = created("credential")
	crud(m, v1+"webhooks", "webhook_subscription", "POST", "DELETE")
	crud(m, v1+"lifecycle-definitions", "lifecycle_definition", "POST", "DELETE")
	crud(m, v1+"security/findings", "security_finding", "POST", "PATCH")
	crud(m, v1+"invitations", "user_invitation", "POST", "DELETE")
	crud(m, v1+"network-interfaces", "network_interface", "PATCH", "DELETE")
	crud(m, v1+"rack-mounts", "rack_mount", "PATCH", "DELETE")
	crud(m, v1+"ci-contacts", "ci_contact", "DELETE")
	crud(m, v1+"ci-types", "ci_type", "PATCH")
	crud(m, v1+"locations", "location", "PATCH")
	crud(m, v1+"jobs", "job", "DELETE")
	crud(m, "/scim/v2/Groups", "team", all...)
	m["PUT /scim/v2/Groups/{id}"] = row("team")
	crud(m, "/scim/v2/Users", "app_user", "POST", "PATCH")
	m["PUT /scim/v2/Users/{id}"] = row("app_user")

	// Creates of further rows.
	for route, table := range map[string]string{
		"POST /api/v1/agents/enroll":               "endpoint_agent",
		"POST /api/v1/agents/enrollment-tokens":    "agent_enrollment_token",
		"POST /api/v1/ai/conversations":            "ai_conversation",
		"POST /api/v1/asset-locations":             "asset_location",
		"POST /api/v1/assignments":                 "assignment",
		"POST /api/v1/cis/{id}/contacts":           "ci_contact",
		"POST /api/v1/cis/{id}/interfaces":         "network_interface",
		"POST /api/v1/collectors/enrollment-codes": "collector_enrollment_code",
		"POST /api/v1/desks/{id}/bookings":         "desk_booking",
		"POST /api/v1/discovery/jobs":              "discovery_job",
		"POST /api/v1/disposal-records":            "disposal_record",
		"POST /api/v1/documents/{id}/links":        "document_link",
		"POST /api/v1/export/jobs":                 "export_job",
		"POST /api/v1/exports":                     "export_job",
		"POST /api/v1/forms/{id}/submissions":      "form_submission",
		"POST /api/v1/iga/access-requests":         "iga_access_request",
		"POST /api/v1/iga/access-reviews":          "iga_access_review",
		"POST /api/v1/iga/jml-policies":            "iga_lifecycle_policy",
		"POST /api/v1/relationship-types":          "relationship_type",
		"POST /api/v1/reservations":                "reservation",
		"POST /api/v1/stock-movements":             "asset_movement",
		"POST /api/v1/stocktakes/{id}/scans":       "stock_scan",
		"POST /api/v1/teams/{id}/members":          "team_member",
		"POST /api/v1/tickets/{id}/comments":       "ticket_comment",
		"POST /api/v1/trainings/{id}/assignments":  "training_assignment",
		"POST /api/v1/workflows/{id}/runs":         "workflow_run",
	} {
		m[route] = created(table)
	}
	// The snapshot job: the answer names it as job_id.
	m["POST /api/v1/history/snapshots"] = RouteSnapshot{Table: "job", Keys: []SnapshotKey{{"id", "@job_id"}}}
	// A role assignment is a custom-role grant or a standard-role
	// assignment.
	for _, route := range []string{"POST /api/v1/role-assignments", "POST /api/v1/roles/assign"} {
		s := created("user_custom_role")
		s.Alt = "role_assignment"
		m[route] = s
	}

	// Actions on the row the route names: its state before and after.
	for route, table := range map[string]string{
		"POST /api/v1/agents/{id}/disable":                 "endpoint_agent",
		"POST /api/v1/agents/{id}/enable":                  "endpoint_agent",
		"PATCH /api/v1/agents/{id}/policy":                 "endpoint_agent",
		"POST /api/v1/agents/{id}/site":                    "endpoint_agent",
		"POST /api/v1/assets/{id}/lifecycle-transitions":   "asset",
		"POST /api/v1/assignments/{id}/return":             "assignment",
		"POST /api/v1/assignments/{id}/transfer":           "assignment",
		"DELETE /api/v1/assignments/{id}":                  "assignment",
		"POST /api/v1/cis/{id}/lifecycle-transitions":      "ci",
		"POST /api/v1/cis/{id}/unmerge":                    "ci",
		"POST /api/v1/consumables/{id}/movements":          "consumable",
		"POST /api/v1/desk-bookings/{id}/cancel":           "desk_booking",
		"POST /api/v1/discovery/review-items/{id}/resolve": "review_item",
		"PUT /api/v1/documents/{id}/content":               "document",
		"POST /api/v1/iga/access-requests/{id}/approve":    "iga_access_request",
		"POST /api/v1/iga/access-requests/{id}/reject":     "iga_access_request",
		"POST /api/v1/iga/connectors/{id}/sync":            "iga_connector",
		"POST /api/v1/iga/connectors/{id}/test":            "iga_connector",
		"POST /api/v1/iga/drift/{id}/remediate":            "iga_drift_finding",
		"POST /api/v1/iga/tasks/{id}/retry":                "iga_provisioning_task",
		"POST /api/v1/inventory/items/{id}/convert":        "quantity_item",
		"POST /api/v1/invitations/{id}/renew":              "user_invitation",
		"POST /api/v1/jobs/{id}/revert":                    "job",
		"POST /api/v1/keys/{id}/issue":                     "key_item",
		"POST /api/v1/keys/{id}/return":                    "key_item",
		"POST /api/v1/orders/{id}/approve":                 "internal_order",
		"POST /api/v1/orders/{id}/items":                   "internal_order",
		"POST /api/v1/orders/{id}/reject":                  "internal_order",
		"POST /api/v1/orders/{id}/submit":                  "internal_order",
		"POST /api/v1/reservations/{id}/release":           "reservation",
		"POST /api/v1/review-items/{id}/resolve":           "review_item",
		"POST /api/v1/stocktakes/{id}/complete":            "stocktake",
		"POST /api/v1/training-assignments/{id}/complete":  "training_assignment",
		"POST /api/v1/webhooks/{id}/rotate-secret":         "webhook_subscription",
		"POST /api/v1/workflow-runs/{id}/approval":         "workflow_run",
	} {
		m[route] = row(table)
	}
	m["POST /api/v1/iga/access-reviews/{id}/items/{itemId}/decision"] = RouteSnapshot{Table: "iga_access_review_item",
		Keys: []SnapshotKey{{"id", "itemId"}, {"review_id", "id"}}}
	m["POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/retry"] = RouteSnapshot{Table: "webhook_delivery",
		Keys: []SnapshotKey{{"id", "delivery_id"}, {"subscription_id", "id"}}}
	m["DELETE /api/v1/teams/{teamId}/members/{userId}"] = RouteSnapshot{Table: "team_member",
		Keys: []SnapshotKey{{"team_id", "teamId"}, {"user_id", "userId"}}}
	// The permissions of a standard role are rows of role_permission.
	m["PUT /api/v1/roles/{id}/permissions"] = RouteSnapshot{Table: "role", Keys: []SnapshotKey{{"id", "id"}},
		Select: `jsonb_build_object('id', t.id, 'organization_id', t.organization_id, 'permission_keys',
			(SELECT COALESCE(jsonb_agg(rp.permission_key ORDER BY rp.permission_key), '[]'::jsonb) FROM role_permission rp
			 WHERE rp.organization_id = t.organization_id AND rp.role_id = t.id))`}
	m["POST /api/v1/tickets/{id}/sla"] = RouteSnapshot{Table: "ticket_sla", Keys: []SnapshotKey{{"ticket_id", "id"}}}
	// Relationship types are addressed by key; the organization's own type,
	// not the built-in one of the same key.
	for _, method := range []string{"PATCH", "DELETE"} {
		m[method+" /api/v1/relationship-types/{key}"] = RouteSnapshot{Table: "relationship_type",
			Keys: []SnapshotKey{{"key", "key"}, {"organization_id", "$org"}}}
	}
	// Settings of the organization and of the acting user.
	m["PATCH /api/v1/org"] = RouteSnapshot{Table: "organization", Keys: []SnapshotKey{{"id", "$org"}}}
	m["PUT /api/v1/privacy/retention"] = RouteSnapshot{Table: "privacy_retention_policy", Keys: []SnapshotKey{{"organization_id", "$org"}}}
	m["PUT /api/v1/reconciliation/source-policy"] = RouteSnapshot{Table: "source_priority_policy",
		Keys: []SnapshotKey{{"organization_id", "$org"}}, Where: "t.is_default"}
	// The notification language of the preferences lives in app_user.locale
	// (NTF-03, F-383).
	m["PUT /api/v1/notifications/preferences"] = RouteSnapshot{Table: "notification_preference",
		Keys: []SnapshotKey{{"organization_id", "$org"}, {"user_id", "$user"}},
		Select: `to_jsonb(t) || jsonb_build_object('locale',
			(SELECT u.locale FROM app_user u WHERE u.organization_id = t.organization_id AND u.id = t.user_id))`}

	// Routes that change no single row whose values the audit could show.
	for route, reason := range map[string]string{
		"POST /api/v1/audit-log/verify":                "verifies the hash chain; changes no data",
		"POST /api/v1/compliance/evaluations":          "evaluation run over many CIs; its results are measurements",
		"POST /api/v1/credentials/rotate-keys":         "re-encrypts every credential; no readable column changes",
		"POST /api/v1/iga/drift/reconcile":             "reconciliation run over every connector",
		"POST /api/v1/iga/jml/resolve":                 "resolves a joiner/mover/leaver event into provisioning tasks",
		"POST /api/v1/maintenance-windows/{id}/notify": "sends notices; the window is unchanged",
		"POST /api/v1/privacy/erasure":                 "erasure of personal data, which must not enter the audit",
		"POST /api/v1/search/reindex":                  "rebuilds the search index; no business data changes",
		"POST /api/v1/webhooks/{id}/test":              "test delivery; the subscription is unchanged",
	} {
		m[route] = noValues(reason)
	}
	return m
}()
