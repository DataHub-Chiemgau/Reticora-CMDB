package document

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Audit actions of the link routes (WP-267, MGT-01) and of a creation with
// links (WP-851, E-61 option 1): written by the repository in the
// transaction of the change, so the route audit (WP-133) adds no second
// entry.
const (
	ActionCreated  = "document.created"
	ActionLinked   = "document.linked"
	ActionUnlinked = "document.unlinked"
)

// linkTargets is the registry of the entity types a document may be linked
// to (WP-267). Each entry returns the EXISTS predicate that a target with
// the id expression id is visible under the transaction's tenant scope: the
// policies of the target tables filter the subqueries. Every value must also
// stand in doc_link_entity_type_check (migrations 000217, 000238, 000266),
// and derive_document_link_scope derives client, site and team from the
// target:
//
//   - ci, asset, ticket and location are checked directly;
//   - disposal_record directly (its own client and site since 000145);
//   - contract directly (its own client; org-wide with client_id NULL,
//     WP-737);
//   - stocktake on existence only (org-wide);
//   - stock_movement directly (its own client and site, WP-744; the
//     signature images of WP-276; the links of the former assignment rows
//     since the drop of assignment, WP-843, by the id of their movement).
var linkTargets = map[string]func(id string) string{
	"stock_movement": func(id string) string {
		return "EXISTS (SELECT 1 FROM stock_movement WHERE stock_movement.id = " + id + ")"
	},
	"ci": func(id string) string {
		return "EXISTS (SELECT 1 FROM ci WHERE ci.id = " + id + ")"
	},
	"asset": func(id string) string {
		return "EXISTS (SELECT 1 FROM asset WHERE asset.id = " + id + ")"
	},
	"ticket": func(id string) string {
		return "EXISTS (SELECT 1 FROM ticket WHERE ticket.id = " + id + ")"
	},
	"location": func(id string) string {
		return "EXISTS (SELECT 1 FROM location WHERE location.id = " + id + ")"
	},
	"disposal_record": func(id string) string {
		return "EXISTS (SELECT 1 FROM disposal_record WHERE disposal_record.id = " + id + ")"
	},
	"contract": func(id string) string {
		return "EXISTS (SELECT 1 FROM contract WHERE contract.id = " + id + ")"
	},
	"stocktake": func(id string) string {
		return "EXISTS (SELECT 1 FROM stocktake WHERE stocktake.id = " + id + ")"
	},
}

// EntityTypes returns the entity types of the registry, sorted.
func EntityTypes() []string {
	out := make([]string, 0, len(linkTargets))
	for t := range linkTargets {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// checkEntityType returns a 422 field error when entityType is not in the
// registry.
//
// validation: the text names the allowed entity types.
func checkEntityType(entityType string) error {
	if _, ok := linkTargets[entityType]; ok {
		return nil
	}
	return &api.FieldError{Field: "entity_type", Status: http.StatusUnprocessableEntity,
		Message: "must be one of " + strings.Join(EntityTypes(), ", ")}
}

// errInvalidEntityID answers a link whose entity id is no UUID with 400; the
// id never reaches SQL.
var errInvalidEntityID = &api.FieldError{Field: "entity_id", Message: "must be a UUID"}

// linkVisible is the SQL condition "the row of document_link links a target
// visible under the tenant scope". An entity type outside the registry is
// never visible (no ELSE true).
var linkVisible = func() string {
	var b strings.Builder
	b.WriteString("CASE document_link.entity_type")
	for _, t := range EntityTypes() {
		b.WriteString(" WHEN '" + t + "' THEN " + linkTargets[t]("document_link.entity_id"))
	}
	b.WriteString(" ELSE false END")
	return b.String()
}()

// teamWriteSQL is the SQL condition "team_id of the row is in the
// transaction's team scope" (unqualified column), like the WITH CHECK of
// document_link (migration 000217).
const teamWriteSQL = "(NULLIF(current_setting('" + database.TeamScopeGUC + "', true), '') IS NULL" +
	" OR team_id = ANY (string_to_array(current_setting('" + database.TeamScopeGUC + "', true), ',')::uuid[]))"

// orgWideDocumentSQL is the SQL condition "the transaction is restricted in
// none of the client, site and team scope": only such a principal writes
// every document (migration 000217; a team-restricted principal is scoped).
const orgWideDocumentSQL = "(" + database.OrgWideScopeSQL +
	" AND NULLIF(current_setting('" + database.TeamScopeGUC + "', true), '') IS NULL)"

// orgWideDocumentScope reports whether scope may create unlinked
// (org-wide) documents: no client, site or team restriction (E-09, E-11).
func orgWideDocumentScope(scope *database.TenantScope) bool {
	return scope.Clients.IsAll() && scope.Sites.IsAll() && scope.Teams.IsAll()
}

// targetVisible checks before the INSERT that the target of link exists and
// is visible under the tenant scope.
func targetVisible(ctx context.Context, tx pgx.Tx, link *DocumentLink) error {
	var ok bool
	if err := tx.QueryRow(ctx, "SELECT "+linkTargets[link.EntityType]("$1::uuid"), link.EntityID).Scan(&ok); err != nil {
		return err
	}
	if !ok {
		return api.ErrNotFound
	}
	return nil
}

// LinkTx links a document to its target in tx, the transaction of the
// caller (WP-276): the entity type must stand in the registry, the target
// must be visible under the tenant scope (api.ErrNotFound otherwise), the
// trigger derive_document_link_scope sets client, site and team, and the
// WITH CHECK of document_link refuses a link outside a scoped principal's
// scope. It sets the id and creation time of link.
func LinkTx(ctx context.Context, tx pgx.Tx, link *DocumentLink) error {
	if err := checkEntityType(link.EntityType); err != nil {
		return err
	}
	if !api.IsUUID(link.EntityID) {
		return errInvalidEntityID
	}
	if err := targetVisible(ctx, tx, link); err != nil {
		return err
	}
	if err := insertLink(ctx, tx, link); err != nil {
		return err
	}
	return requireVisible(ctx, tx, "document_link", linkVisible, link.ID)
}

// refuseSignatureChange answers a change that would break the evidence of
// a signature (AST-02, E-51, WP-748) and reports whether it answered: a
// document of the category signature (d) takes no PATCH, content, new
// version, soft delete, new link and no removal of a link (409); no
// creation or PATCH sets the category signature (category, 422), only the
// signature path of WP-276 does (CreateWithFirstVersionTx). The signature
// hash reveals an exchanged image, this lock prevents it. d or category
// may be nil.
func refuseSignatureChange(w http.ResponseWriter, d *Document, category *string) bool {
	switch {
	case d != nil && d.Category == CategorySignature:
		api.WriteError(w, http.StatusConflict, "Conflict", "a signature document is locked and cannot be changed")
	case category != nil && *category == CategorySignature:
		api.WriteFieldError(w, &api.FieldError{Field: "category", Status: http.StatusUnprocessableEntity,
			Message: "the category signature is set only by a signed assignment, return or transfer"})
	default:
		return false
	}
	return true
}

// recordLinkAudit writes document.linked or document.unlinked as the last
// step of the transaction (WP-267). Record marks the request as audited.
func recordLinkAudit(ctx context.Context, tx pgx.Tx, action string, link *DocumentLink) error {
	if _, err := audit.NewPGRecorder().Record(ctx, tx, linkAuditEntry(ctx, action, link)); err != nil {
		return fmt.Errorf("audit %s: %w", action, err)
	}
	return nil
}

// recordCreateAudit writes document.created with the new document and one
// document.linked per link, in this order, right before the commit of the
// transaction (WP-851, convention 20): the creation and its links fail or
// commit together with their entries. The entries mark the request as
// audited, so the route audit of POST /api/v1/documents writes none of its
// own; a creation without links keeps the route entry.
func recordCreateAudit(ctx context.Context, tx pgx.Tx, d *Document, links []DocumentLink) error {
	// The entries capture copies: the caller may change d and links before
	// the commit.
	after := *d
	after.Tags = append([]string{}, d.Tags...)
	actorID, actorType := auditActor(ctx)
	entries := make([]audit.Entry, 0, 1+len(links))
	entries = append(entries, audit.Entry{
		OrganizationID: d.OrganizationID,
		ActorID:        actorID,
		ActorType:      actorType,
		Action:         ActionCreated,
		ResourceType:   "document",
		ResourceID:     d.ID,
		Changes:        map[string]any{"after": &after},
	})
	for i := range links {
		entries = append(entries, linkAuditEntry(ctx, ActionLinked, &links[i]))
	}
	return database.BeforeCommit(ctx, func() error {
		if _, err := audit.NewPGRecorder().RecordMany(ctx, tx, entries); err != nil {
			return fmt.Errorf("audit %s: %w", ActionCreated, err)
		}
		return nil
	})
}

// linkAuditEntry is the entry document.linked or document.unlinked of link.
func linkAuditEntry(ctx context.Context, action string, link *DocumentLink) audit.Entry {
	actorID, actorType := auditActor(ctx)
	return audit.Entry{
		OrganizationID: link.OrganizationID,
		ActorID:        actorID,
		ActorType:      actorType,
		Action:         action,
		ResourceType:   "document",
		ResourceID:     link.DocumentID,
		Changes: map[string]any{
			"link_id":     link.ID,
			"entity_type": link.EntityType,
			"entity_id":   link.EntityID,
		},
	}
}

// auditActor names the user of the request (actor id only when it is a
// UUID), or the system without one.
func auditActor(ctx context.Context) (actorID, actorType string) {
	user := strings.TrimSpace(tenant.FromContext(ctx).UserID)
	if user == "" {
		return "", "system"
	}
	if audit.IsUUID(user) {
		actorID = user
	}
	return actorID, "user"
}
