package asset

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/jackc/pgx/v5"
)

// Tags of assets (API-06, WP-755): the assignments asset_tag_link to the
// tag catalog of WP-281 (migration 000249). PUT /api/v1/assets/{id}/tags
// replaces them; custom_fields.tags is no longer written.

// ReplaceTagsRequest is the body of PUT /api/v1/assets/{id}/tags.
type ReplaceTagsRequest struct {
	Tags []string `json:"tags"`
}

// TagStore replaces the tag assignments of an asset (PGRepository,
// MemoryRepository).
type TagStore interface {
	// ReplaceTags replaces the tags of asset id with the catalog tags keys
	// in one transaction, with If-Match (nil without the header); unknown
	// keys are a *TagsError.
	ReplaceTags(ctx context.Context, orgID, id string, keys []string, ifMatch *int64) (*Asset, error)
}

// fieldTags is the DTO member and trail field of the tags.
const fieldTags = "tags"

// ErrTagsUnsupported: the repository keeps no tag assignments (501).
var ErrTagsUnsupported = errors.New("asset tags are not supported by this repository")

// TagsError rejects the tags of a PUT /api/v1/assets/{id}/tags (422 with
// the entries).
type TagsError struct {
	Violations []api.Violation
}

// validation: a fixed rule text; the entries are in Violations.
func (e *TagsError) Error() string {
	return "tags must name catalog tags (POST /api/v1/tags creates them)"
}

// tagKeyPattern is a catalog key (tag_key_check, 000218).
var tagKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,62}$`)

// tagKeys returns the sorted keys of a PUT without duplicates and the
// violations of the entries that are no catalog key in form; a missing
// list is a violation of tags.
func tagKeys(keys []string) ([]string, []api.Violation) {
	if keys == nil {
		return nil, []api.Violation{{Field: fieldTags, Detail: "is required (an empty list removes every tag)"}}
	}
	var violations []api.Violation
	out := make([]string, 0, len(keys))
	for i, key := range keys {
		if !tagKeyPattern.MatchString(key) {
			violations = append(violations, api.Violation{Field: fmt.Sprintf("tags[%d]", i), Detail: "unknown tag"})
			continue
		}
		if !slices.Contains(out, key) {
			out = append(out, key)
		}
	}
	slices.Sort(out)
	return out, violations
}

// unknownTags returns the violations of the well-formed keys the catalog
// lacks (known holds the catalog keys found).
func unknownTags(keys []string, known map[string]string) []api.Violation {
	var violations []api.Violation
	for i, key := range keys {
		if _, ok := known[key]; tagKeyPattern.MatchString(key) && !ok {
			violations = append(violations, api.Violation{Field: fmt.Sprintf("tags[%d]", i), Detail: "unknown tag"})
		}
	}
	return violations
}

// rejectCustomFieldTags refuses custom_fields.tags in POST and PATCH
// /api/v1/assets (400): tags are catalog assignments since WP-755.
func rejectCustomFieldTags(fields map[string]any) error {
	if _, sent := fields[fieldTags]; !sent {
		return nil
	}
	return &api.FieldError{Field: customFieldsPrefix + fieldTags,
		Message: "tags are no custom field; use PUT /api/v1/assets/{id}/tags with catalog tags"}
}

// ReplaceTags implements TagStore without a catalog: every well-formed key
// counts as a catalog tag. If-Match as Update.
func (r *MemoryRepository) ReplaceTags(_ context.Context, orgID, id string, keys []string, ifMatch *int64) (*Asset, error) {
	sorted, violations := tagKeys(keys)
	if len(violations) > 0 {
		return nil, &TagsError{Violations: violations}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.assets[id]
	if !ok || a.OrganizationID != orgID {
		return nil, ErrNotFound
	}
	if ifMatch != nil && *ifMatch != api.IfMatchAny && *ifMatch != a.Version {
		if *ifMatch > a.Version {
			return nil, ErrPreconditionFailed
		}
		return nil, &ConflictError{Fields: []string{fieldTags}, Current: a.Version}
	}
	if a.Tags == nil {
		a.Tags = []string{}
	}
	if slices.Equal(a.Tags, sorted) {
		return a, nil
	}
	a.Tags = sorted
	a.Version++
	a.UpdatedAt = time.Now().UTC()
	return a, nil
}

// ReplaceTags implements TagStore (PUT /api/v1/assets/{id}/tags): the
// asset is locked (FOR UPDATE, asset_scoped_update applies), the keys must
// be catalog tags (FOR SHARE keeps them until the commit), If-Match as
// PATCH. A changed list rewrites asset_tag_link, raises the version by one
// (touchVersion), writes the trail row of tags and asset.changed with
// changed_fields ["tags"]; an unchanged list writes nothing.
func (r *PGRepository) ReplaceTags(ctx context.Context, orgID, id string, keys []string, ifMatch *int64) (*Asset, error) {
	sorted, violations := tagKeys(keys)
	if keys == nil {
		return nil, &TagsError{Violations: violations}
	}
	var out *Asset
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := r.getLocked(ctx, tx, orgID, id, "FOR UPDATE")
		if err != nil {
			return err
		}
		// The revert of a bulk decides the tags on the locked row (WP-784).
		revert := tagRevertFrom(ctx)
		if revert != nil {
			sorted = revert.decide(current.Tags)
			keys = sorted
		}
		rows, err := tx.Query(ctx, `SELECT key, id::text FROM tag WHERE organization_id = $1 AND key = ANY($2) FOR SHARE`, orgID, sorted)
		if err != nil {
			return fmt.Errorf("read catalog tags: %w", err)
		}
		known := map[string]string{}
		var key, tagID string
		if _, err = pgx.ForEachRow(rows, []any{&key, &tagID}, func() error {
			known[key] = tagID
			return nil
		}); err != nil {
			return fmt.Errorf("read catalog tags: %w", err)
		}
		if revert != nil {
			// A tag deleted from the catalog since the bulk does not come
			// back.
			sorted = revert.dropUnknown(sorted, known)
			keys = sorted
		}
		if violations = append(violations, unknownTags(keys, known)...); len(violations) > 0 {
			return &TagsError{Violations: violations}
		}
		if checkErr := checkIfMatch(ctx, tx, orgID, current, ifMatch, []string{fieldTags}); checkErr != nil {
			return checkErr
		}
		if slices.Equal(current.Tags, sorted) {
			out = current
			return revert.mark(ctx, tx, orgID)
		}
		ids := make([]string, 0, len(sorted))
		for _, k := range sorted {
			ids = append(ids, known[k])
		}
		if _, err = tx.Exec(ctx, `DELETE FROM asset_tag_link WHERE asset_id = $1 AND NOT (tag_id = ANY($2::uuid[]))`, id, ids); err != nil {
			return fmt.Errorf("remove asset tags: %w", err)
		}
		var author any
		if user := tenant.FromContext(ctx).UserID; api.IsUUID(user) {
			author = user
		}
		if _, err = tx.Exec(ctx, `INSERT INTO asset_tag_link (organization_id, asset_id, tag_id, created_by)
			SELECT $1, $2, t, $4 FROM unnest($3::uuid[]) AS t
			ON CONFLICT (asset_id, tag_id) DO NOTHING`, orgID, id, ids, author); err != nil {
			return fmt.Errorf("assign asset tags: %w", err)
		}
		if out, err = r.touchVersion(ctx, tx, orgID, id); err != nil {
			return err
		}
		if csErr := recordTagChanges(ctx, tx, orgID, current, out); csErr != nil {
			return csErr
		}
		if markErr := revert.mark(ctx, tx, orgID); markErr != nil {
			return markErr
		}
		return r.recordUpdate(ctx, tx, orgID, current, out)
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// The change set of the tags (BLK-02, WP-784): under a bulk job
// (jobs.WithChangeSet) ReplaceTags records in its transaction each tag it
// actually added (op add, the key in new_value) or removed (op remove,
// the key in old_value), field tags, like the tags of a CI (WP-783). The
// revert passes the rows of one asset (withTagRevert): ReplaceTags then
// ignores the keys it is given and decides per row on the locked row; a
// tag the bulk added goes when the asset still has it, a tag the bulk
// removed comes back when the asset still lacks it and the catalog still
// holds it (read FOR SHARE). Each row is marked reverted or skipped in the
// same transaction.

// recordTagChanges writes the change set rows of a tag change from before
// to after under a bulk job; nothing outside one.
func recordTagChanges(ctx context.Context, tx pgx.Tx, orgID string, before, after *Asset) error {
	jobID, ok := jobs.ChangeSetFrom(ctx)
	if !ok {
		return nil
	}
	var changes []jobs.Change
	row := func(op, key string) error {
		raw, err := json.Marshal(key)
		if err != nil {
			return fmt.Errorf("encode bulk change set: %w", err)
		}
		c := jobs.Change{JobID: jobID, EntityType: jobs.EntityAsset, EntityID: after.ID, Op: op, FieldName: fieldTags,
			ClientID: after.ClientID, SiteID: after.SiteID}
		if op == jobs.OpAdd {
			c.NewValue = raw
		} else {
			c.OldValue = raw
		}
		changes = append(changes, c)
		return nil
	}
	for _, key := range after.Tags {
		if !slices.Contains(before.Tags, key) {
			if err := row(jobs.OpAdd, key); err != nil {
				return err
			}
		}
	}
	for _, key := range before.Tags {
		if !slices.Contains(after.Tags, key) {
			if err := row(jobs.OpRemove, key); err != nil {
				return err
			}
		}
	}
	return jobs.RecordChangeTx(ctx, tx, orgID, changes)
}

// tagRevert is the revert of the tag rows of one asset: the rows, the
// outcome per row and the keys each row brings back.
type tagRevert struct {
	rows     []jobs.Change
	marks    []jobs.ChangeMark
	reverted []string
	skipped  []string
}

type tagRevertKey struct{}

// withTagRevert returns ctx whose ReplaceTags reverts the tag rows of e.
func withTagRevert(ctx context.Context, e *tagRevert) context.Context {
	return context.WithValue(ctx, tagRevertKey{}, e)
}

func tagRevertFrom(ctx context.Context) *tagRevert {
	e, _ := ctx.Value(tagRevertKey{}).(*tagRevert)
	return e
}

// rowKey is the tag key of a tag row.
func rowKey(row *jobs.Change) string {
	raw := row.NewValue
	if row.Op == jobs.OpRemove {
		raw = row.OldValue
	}
	var key string
	if json.Unmarshal(raw, &key) != nil {
		return ""
	}
	return key
}

// decide returns the sorted keys after the revert of the rows on the tags
// of the locked row and records each row's outcome.
func (e *tagRevert) decide(tags []string) []string {
	e.marks, e.reverted, e.skipped = nil, nil, nil
	out := slices.Clone(tags)
	for i := range e.rows {
		row := &e.rows[i]
		key := rowKey(row)
		has := key != "" && slices.Contains(out, key)
		outcome := jobs.OutcomeSkipped
		switch {
		case row.Op == jobs.OpAdd && has:
			out = slices.DeleteFunc(out, func(k string) bool { return k == key })
			outcome = jobs.OutcomeReverted
		case row.Op == jobs.OpRemove && key != "" && !has:
			out = append(out, key)
			outcome = jobs.OutcomeReverted
		}
		e.marks = append(e.marks, jobs.ChangeMark{ID: row.ID, Outcome: outcome})
	}
	slices.Sort(out)
	e.tally()
	return out
}

// dropUnknown removes from keys the tags the catalog no longer holds and
// marks the rows that would have brought them back skipped.
func (e *tagRevert) dropUnknown(keys []string, known map[string]string) []string {
	out := slices.DeleteFunc(slices.Clone(keys), func(k string) bool { _, ok := known[k]; return !ok })
	for i := range e.rows {
		if _, ok := known[rowKey(&e.rows[i])]; !ok && e.rows[i].Op == jobs.OpRemove {
			e.marks[i].Outcome = jobs.OutcomeSkipped
		}
	}
	e.tally()
	return out
}

// tally lists the keys of the reverted and of the skipped rows.
func (e *tagRevert) tally() {
	e.reverted, e.skipped = nil, nil
	for i := range e.marks {
		if e.marks[i].Outcome == jobs.OutcomeReverted {
			e.reverted = append(e.reverted, fieldTags)
		} else {
			e.skipped = append(e.skipped, fieldTags)
		}
	}
}

// mark records the outcome of the rows in tx; nothing for nil.
func (e *tagRevert) mark(ctx context.Context, tx pgx.Tx, orgID string) error {
	if e == nil {
		return nil
	}
	return jobs.MarkChangeTx(ctx, tx, orgID, e.marks)
}

// touchVersion raises the version of asset id by one although no column
// changes (asset_version_bump with app.asset_version_touch, migration
// 000249) and returns the asset as stored.
func (r *PGRepository) touchVersion(ctx context.Context, tx pgx.Tx, orgID, id string) (*Asset, error) {
	if _, err := tx.Exec(ctx, `SELECT set_config('app.asset_version_touch', 'on', true)`); err != nil {
		return nil, fmt.Errorf("touch asset version: %w", err)
	}
	item, err := scanAsset(tx.QueryRow(ctx, fmt.Sprintf(`UPDATE asset SET updated_at = NOW()
		WHERE id = $1 AND organization_id = $2 RETURNING %s`, assetSelectColumns), id, orgID))
	if errors.Is(err, pgx.ErrNoRows) {
		err = ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("touch asset version: %w", err)
	}
	if _, err = tx.Exec(ctx, `SELECT set_config('app.asset_version_touch', '', true)`); err != nil {
		return nil, fmt.Errorf("touch asset version: %w", err)
	}
	return item, nil
}

// ReplaceTags handles PUT /api/v1/assets/{id}/tags: replaces the tags of
// the asset with catalog tags (If-Match, API-07) and answers the asset with
// its new ETag. Unknown keys are 422 with the entries.
func (h *Handler) ReplaceTags(w http.ResponseWriter, r *http.Request) {
	orgID, policy, ok := requestContext(w, r)
	if !ok {
		return
	}
	id, ok := api.ParseUUIDParam(w, r, "id", ErrNotFound.Error())
	if !ok {
		return
	}
	var req ReplaceTagsRequest
	if err := api.ReadJSON(r, &req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	var ifMatch *int64
	if version, sent, parseErr := api.ParseIfMatch(r); parseErr != nil {
		api.WritePreconditionFailed(w, parseErr.Error())
		return
	} else if sent {
		ifMatch = &version
	}
	store, ok := h.repo.(TagStore)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", ErrTagsUnsupported.Error())
		return
	}
	item, err := store.ReplaceTags(r.Context(), orgID, id, req.Tags, ifMatch)
	if err != nil {
		writeTagsError(w, r, err)
		return
	}
	body, err := project(policy, item)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	w.Header().Set("ETag", api.ETag(item.Version))
	api.WriteJSON(w, http.StatusOK, body)
}

// writeTagsError answers a failed PUT /api/v1/assets/{id}/tags: a
// *TagsError is 422 with the entries, everything else as writeAssetError.
func writeTagsError(w http.ResponseWriter, r *http.Request, err error) {
	var tagsErr *TagsError
	if !errors.As(err, &tagsErr) {
		writeAssetError(w, r, err)
		return
	}
	api.WriteProblemBody(w, http.StatusUnprocessableEntity, struct {
		api.ProblemDetail
		Violations []api.Violation `json:"violations"`
	}{
		ProblemDetail: api.ProblemDetail{Type: "https://reticora.io/problems/422", Title: "Unprocessable Entity",
			Status: http.StatusUnprocessableEntity, Detail: tagsErr.Error()},
		Violations: tagsErr.Violations,
	})
}
