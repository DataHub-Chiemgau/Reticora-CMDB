package metamodel

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Capture of resource writes into a draft (WP-720, MET-53): a write to a
// metamodel resource route with ?draft=<id> is not run; CaptureDraft turns
// it into an item of the draft through the same service as POST
// …/items and answers 202. server.authorizingRouter attaches it as the
// innermost link of every route of DraftCapableRoutes (behind
// authorization, validation, idempotency and audit) and refuses ?draft at
// every other write route (fail-closed).

// DraftParam is the query parameter that names the draft.
const DraftParam = "draft"

// RouteItem is the kind and op of the item a resource route captures.
type RouteItem struct {
	Kind, Op string
}

// DraftCapableRoutes maps every metamodel write route ("METHOD pattern")
// to the kind and op of its item: the resource route of each pair of
// ItemKinds.
var DraftCapableRoutes = capableRoutes()

func capableRoutes() map[string]RouteItem {
	out := map[string]RouteItem{}
	for kind, ops := range ItemKinds {
		for op, sp := range ops {
			out[sp.Route] = RouteItem{Kind: kind, Op: op}
		}
	}
	return out
}

// DraftExemptRoutes are the write routes below the metamodel prefixes that
// change no metamodel and so cannot be captured, with the reason; ?draft
// there is refused like at any other write route.
var DraftExemptRoutes = map[string]string{
	"POST /api/v1/templates/{id}/migrations": "migrates CIs to a published template version (WP-712): " +
		"a job on the inventory, no change of the metamodel",
	"POST /api/v1/attribute-definitions/{id}/reindex": "rebuilds the index of a published definition (WP-796): " +
		"a job on the database, no change of the metamodel",
}

// TemplateVersionLookup resolves the version number of a template
// version route to the id of the version (target of a template_version
// item); PGRepository implements it.
type TemplateVersionLookup interface {
	TemplateVersionID(ctx context.Context, orgID, templateID, version string) (string, error)
}

// maxCaptureBody bounds the body of a captured request (like api.ReadJSON).
const maxCaptureBody = 1 << 20

// CaptureDraft returns the capture of the resource route method pattern:
// without ?draft the request goes on to the resource handler unchanged;
// with ?draft its path parameters and body become an item of the draft
// (POST …/items: payload check, base, tested → draft) and the answer is
// 202 with the item and the ETag of the draft. The resource handler does
// not run. The route permission was checked by the authorization; the
// capture needs citype:manage in addition (403). A draft value that is no
// UUID is 400, an unknown draft or one of another organization 404, a
// published or discarded one 409, no repository (no database) 503. A
// route outside DraftCapableRoutes gets no capture.
func CaptureDraft(repo Repository, method, pattern string) func(http.Handler) http.Handler {
	ri, capable := DraftCapableRoutes[method+" "+pattern]
	return func(next http.Handler) http.Handler {
		if !capable {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !r.URL.Query().Has(DraftParam) {
				next.ServeHTTP(w, r)
				return
			}
			captureItem(w, r, repo, ri)
		})
	}
}

// errDraftParam refuses a draft value that is no UUID (400).
func errDraftParam() error {
	return &api.FieldError{Field: DraftParam, Message: "draft must be the id of a metamodel draft"}
}

// captureItem builds the item of a captured request and adds it to the
// draft.
func captureItem(w http.ResponseWriter, r *http.Request, repo Repository, ri RouteItem) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}
	perms, _ := identity.PermissionsFromContext(r.Context())
	if !identity.HasPermission(perms, identity.PermCITypeManage) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", "a write into a metamodel draft requires citype:manage")
		return
	}
	draftID := strings.TrimSpace(r.URL.Query().Get(DraftParam))
	if !api.IsUUID(draftID) {
		api.WriteFieldError(w, errDraftParam())
		return
	}
	if repo == nil {
		api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "metamodel drafts need the database")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCaptureBody))
	if err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "the request body could not be read")
		return
	}
	in := ItemInput{Kind: ri.Kind, Op: ri.Op, TargetID: chi.URLParam(r, "id"), TargetKey: chi.URLParam(r, "name"),
		Payload: body}
	if version := chi.URLParam(r, "version"); version != "" {
		lookup, ok := repo.(TemplateVersionLookup)
		if !ok {
			api.WriteError(w, http.StatusServiceUnavailable, "Service Unavailable", "metamodel drafts need the database")
			return
		}
		if in.TargetID, err = lookup.TemplateVersionID(r.Context(), t.OrganizationID, in.TargetID, version); err != nil {
			writeDraftError(w, err)
			return
		}
	}
	c, err := checkItemPayload(&in)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	d, it, err := repo.AddItem(r.Context(), t.OrganizationID, draftID, c, nil)
	if err != nil {
		writeDraftError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(d.Version))
	api.WriteJSON(w, http.StatusAccepted, it)
}

// TemplateVersionID implements TemplateVersionLookup: the version of the
// template the organization sees; none is ErrTargetNotFound.
func (r *PGRepository) TemplateVersionID(ctx context.Context, orgID, templateID, version string) (string, error) {
	number, err := strconv.Atoi(version)
	if err != nil || number < 1 || !api.IsUUID(templateID) {
		return "", ErrTargetNotFound
	}
	var id string
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		qerr := tx.QueryRow(ctx, `SELECT id::text FROM template_version WHERE template_id = $1 AND version = $2`,
			templateID, number).Scan(&id)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return ErrTargetNotFound
		}
		if qerr != nil {
			return fmt.Errorf("read template version: %w", qerr)
		}
		return nil
	})
	return id, err
}

// ─── Payload check ─────────────────────────────────────────────────────────

// checkItemPayload checks an item request like checkItem and its payload
// like the resource route without the stored metamodel (checkPayload).
func checkItemPayload(in *ItemInput) (*capture, error) {
	c, err := checkItem(in)
	if err != nil {
		return nil, err
	}
	return c, checkPayload(c)
}

// checkPayload runs the checks of the resource route that need no stored
// row on the payload of a checked item (citype/draft_payload.go): a field
// error with 400 or 422 as the route answers. Item references stand for
// the ids of the parts they create.
// validation: the field errors of the checks in citype/draft_payload.go;
// any other error text is one of their domain texts.
func checkPayload(c *capture) error {
	var generic any
	dec := json.NewDecoder(bytes.NewReader(c.Payload))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return &api.FieldError{Field: "payload", Message: "payload must be a JSON object"}
	}
	plain, _, err := substituteRefs(generic)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(plain)
	if err != nil {
		return &api.FieldError{Field: "payload", Message: "payload must be a JSON object"}
	}
	v := c.Spec.payload()
	if err = json.Unmarshal(raw, v); err != nil {
		return &api.FieldError{Field: "payload", Message: "payload must be a JSON object"}
	}
	checkErr := payloadCheck(v)
	var fe *api.FieldError
	if checkErr != nil && !errors.As(checkErr, &fe) {
		return &api.FieldError{Field: "payload", Message: checkErr.Error()}
	}
	return checkErr
}

// payloadCheck runs the check of the payload type of a resource route
// (citype/draft_payload.go); a type without one is valid.
// validation: the field errors of the checks in citype/draft_payload.go.
func payloadCheck(v any) error {
	switch p := v.(type) {
	case *citype.CreateTypeRequest:
		return citype.CheckCreateType(p)
	case *citype.UpdateTypeRequest:
		return citype.CheckUpdateType(p)
	case *citype.UpsertFieldRequest:
		return citype.CheckUpsertField(p)
	case *AttributeDefinitionCreate:
		return citype.CheckNewAttributeDefinition(&p.AttributeDefinitionPatch, p.CITypeID, p.AttributeSetID, p.Key)
	case *AttributeSetCreate:
		return citype.CheckNewAttributeSet(p.Key, p.DisplayName)
	case *citype.AttributeSetPatch:
		return citype.CheckAttributeSetPatch(p)
	case *TemplateCreate:
		return citype.CheckNewTemplate(p.Key, p.CITypeID, p.DisplayName)
	case *citype.TemplatePatch:
		return citype.CheckTemplatePatch(p)
	case *TemplateVersionItemsInput:
		return citype.CheckTemplateItems(p.Items)
	case *citype.RuleInput:
		return citype.CheckNewRule(p)
	case *citype.LayoutInput:
		_, err := citype.NewDraftLayout(p)
		return err
	case *citype.LayoutPatch:
		return citype.ApplyLayoutPatch(p, &citype.FormLayout{})
	}
	return nil
}

// ─── Draft preview of the form (E-113) ────────────────────────────────────

// draftForm is the form source of the preview GET
// /api/v1/ci-types/{id}/form?draft=<id>: the published metamodel with the
// items of the draft laid over it (the overlay of the test run, WP-721)
// for the fields, the catalog rules and the layouts; the CI, the template
// and the roles come from the published source. An item that does not fit
// is left out, as the test run reports it.
type draftForm struct {
	citype.FormSource
	repo  *PGRepository
	o     *overlay
	items []Item
	// systemRules are the ids of the system's catalog rules.
	systemRules map[string]bool
}

var _ citype.DraftForms = (*PGRepository)(nil)

// DraftFormSource implements citype.DraftForms.
func (r *PGRepository) DraftFormSource(ctx context.Context, orgID, draftID string, published citype.FormSource) (citype.FormSource, error) {
	if !api.IsUUID(draftID) {
		return nil, citype.ErrFormDraftNotFound
	}
	f := &draftForm{FormSource: published, repo: r, systemRules: map[string]bool{}}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		d, err := scanDraft(tx.QueryRow(ctx, `SELECT `+draftColumns+` FROM metamodel_draft WHERE id = $1`, draftID))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return citype.ErrFormDraftNotFound
		case err != nil:
			return fmt.Errorf("read metamodel draft: %w", err)
		case d.Status == StatusPublished || d.Status == StatusDiscarded:
			return citype.ErrFormDraftFinal
		}
		f.items = d.Items
		if f.o, err = loadOverlay(database.ContextWithTx(ctx, tx), tx, r.catalog, orgID); err != nil {
			return err
		}
		for i := range f.items {
			if aerr := f.o.apply(ctx, tx, &f.items[i]); aerr != nil {
				var blocked *itemError
				if !errors.As(aerr, &blocked) {
					return aerr
				}
			}
		}
		return queryEach(ctx, tx, `SELECT id::text FROM metamodel_rule WHERE organization_id IS NULL`, func(rows pgx.Rows) error {
			var id string
			if serr := rows.Scan(&id); serr != nil {
				return serr
			}
			f.systemRules[id] = true
			return nil
		})
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// FormTypeChain implements citype.FormSource: the chain of the overlay,
// so a type the draft creates has a form as well.
func (f *draftForm) FormTypeChain(_ context.Context, _, typeID string) ([]string, error) {
	if _, ok := f.o.types[typeID]; !ok {
		return nil, citype.ErrFormTypeNotFound
	}
	return f.o.chain(typeID), nil
}

// FormFields implements citype.FormSource: the layers of the type in the
// overlay, with the current published version of templateID (or the
// version the draft publishes) on top, composed like citype.ResolveFields,
// and the catalog rules of the overlay. The form of a stored CI has no
// draft view.
func (f *draftForm) FormFields(ctx context.Context, orgID, typeID, ciID, templateID string) ([]fieldmeta.FieldDefinition, []fieldmeta.CatalogRule, error) {
	if ciID != "" {
		return f.FormSource.FormFields(ctx, orgID, typeID, ciID, templateID)
	}
	if _, ok := f.o.types[typeID]; !ok {
		return nil, nil, citype.ErrFormTypeNotFound
	}
	layers := f.o.typeLayers(typeID)
	var versions []string
	if templateID != "" {
		err := database.WithRequestTenant(ctx, f.repo.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
			versionID, verr := f.templateVersion(ctx, tx, templateID)
			if verr != nil || versionID == "" {
				return verr
			}
			items, verr := f.o.versionItems(ctx, tx, []string{versionID})
			if verr != nil {
				return verr
			}
			upper, verr := f.o.templateLayers(layers, items)
			if verr != nil {
				var blocked *itemError
				if errors.As(verr, &blocked) {
					return &api.FieldError{Field: "templates[0]", Status: http.StatusUnprocessableEntity,
						Message: "an item of the template version does not fit the metamodel of the draft"}
				}
				return verr
			}
			layers = append(layers, upper...)
			versions = append(versions, versionID)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	defs, err := citype.Compose(layers...)
	if err != nil {
		return nil, nil, err
	}
	catalog, err := f.o.catalogRules(typeID, versions)
	if err != nil {
		return nil, nil, err
	}
	return defs, catalog, nil
}

// templateVersion returns the version of a template the form uses: the
// last one the draft publishes, otherwise the current published one (""
// for none).
func (f *draftForm) templateVersion(ctx context.Context, tx pgx.Tx, templateID string) (string, error) {
	for i := len(f.items) - 1; i >= 0; i-- {
		it := &f.items[i]
		if it.Kind != KindTemplateVersion || it.Op != OpPublish {
			continue
		}
		if node, ok := f.o.versions[itemTarget(it)]; ok && strings.EqualFold(node.templateID, templateID) {
			return itemTarget(it), nil
		}
	}
	var current string
	err := tx.QueryRow(ctx, `SELECT COALESCE((SELECT v.id::text FROM template_version v JOIN template t ON t.id = v.template_id
		WHERE t.id = $1 AND v.version = t.current_version), '')`, templateID).Scan(&current)
	if err != nil {
		return "", fmt.Errorf("read the current template version: %w", err)
	}
	return current, nil
}

// FormLayouts implements citype.FormSource: the stored layouts with the
// layout items of the draft laid over them (a new layout under the id of
// its item), filtered like the stored ones: active, of the types or
// templates, without a role or with one of roleIDs.
func (f *draftForm) FormLayouts(ctx context.Context, orgID string, typeIDs, templateIDs, roleIDs []string) ([]citype.FormLayout, error) {
	stored, err := f.FormSource.FormLayouts(ctx, orgID, typeIDs, templateIDs, roleIDs)
	if err != nil {
		return nil, err
	}
	byID := map[string]*citype.FormLayout{}
	for i := range stored {
		l := stored[i]
		byID[l.ID] = &l
	}
	for i := range f.items {
		it := &f.items[i]
		if it.Kind != KindLayout {
			continue
		}
		if lerr := f.applyLayout(ctx, orgID, it, byID); lerr != nil {
			return nil, lerr
		}
	}
	in := func(list []string, id string) bool {
		return id != "" && slices.ContainsFunc(list, func(x string) bool { return strings.EqualFold(x, id) })
	}
	out := []citype.FormLayout{}
	for _, l := range byID {
		if l == nil || !l.IsActive || (!in(typeIDs, l.CITypeID) && !in(templateIDs, l.TemplateID)) {
			continue
		}
		if l.RoleID != "" && !in(roleIDs, l.RoleID) {
			continue
		}
		out = append(out, *l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// applyLayout lays one layout item over the layouts byID (nil: deleted);
// an item that no longer fits is left out.
func (f *draftForm) applyLayout(ctx context.Context, orgID string, it *Item, byID map[string]*citype.FormLayout) error {
	id := itemTarget(it)
	switch it.Op {
	case OpCreate:
		var in citype.LayoutInput
		if decodeItem(it, &in) != nil {
			return nil
		}
		if l, err := citype.NewDraftLayout(&in); err == nil {
			l.ID, l.OrganizationID = it.ID, orgID
			byID[it.ID] = l
		}
	case OpUpdate:
		current, known := byID[id]
		if !known {
			store, ok := f.FormSource.(citype.LayoutStore)
			if !ok {
				return nil
			}
			read, err := store.GetLayout(ctx, orgID, id)
			if errors.Is(err, citype.ErrLayoutNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			current = read
		}
		if current == nil {
			return nil
		}
		var patch citype.LayoutPatch
		if decodeItem(it, &patch) != nil {
			return nil
		}
		next := *current
		if citype.ApplyLayoutPatch(&patch, &next) != nil {
			return nil
		}
		byID[id] = &next
	case OpDelete:
		byID[id] = nil
	}
	return nil
}

// FormSectionRules implements citype.FormSource over the catalog rules of
// the overlay: per key the active rule like the stored lookup, an own rule
// before a system rule, a rule without template version first, then by id.
func (f *draftForm) FormSectionRules(_ context.Context, _ string, keys []string) (map[string]*rules.Rule, error) {
	out := map[string]*rules.Rule{}
	for _, key := range keys {
		var best *ruleNode
		for _, n := range f.o.rules {
			if !n.active || n.key != key {
				continue
			}
			if best == nil || f.ruleBefore(n, best) {
				best = n
			}
		}
		if best == nil || len(best.condition) == 0 || string(best.condition) == "null" {
			continue
		}
		cond, err := rules.ParseCondition(best.condition)
		if err != nil {
			return nil, fmt.Errorf("rule %s: condition: %w", best.key, err)
		}
		out[key] = &cond
	}
	return out, nil
}

// ruleBefore orders the rules of one key like citype.FormSectionRules.
func (f *draftForm) ruleBefore(a, b *ruleNode) bool {
	if sa, sb := f.systemRules[a.id], f.systemRules[b.id]; sa != sb {
		return !sa
	}
	if (a.versionID == "") != (b.versionID == "") {
		return a.versionID == ""
	}
	return a.id < b.id
}
