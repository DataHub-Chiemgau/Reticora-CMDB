package ci

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// CI merge (CI-12): POST /cis/{survivor}/merge {victim_id} moves the
// relationships, interfaces (with their IPs), contacts, history, documents,
// the endpoint agents and the asset link of the victim to the survivor. Attributes follow the
// provenance rank of the discovery sources, tags are united; the field
// values (provenance and overrides) of both stay, an override of the victim
// that contradicts one of the survivor opens an override_conflict review.
// The victim is soft-deleted with merged_into_id; GET on it answers 301 to
// the survivor for 12 months (tombstone). The snapshot of the merge allows
// an unmerge within 30 days. A merge of a CI another merge holds is 409.

// Retention of the tombstone and of the snapshot.
const (
	MergeTombstoneRetention = 365 * 24 * time.Hour
	MergeSnapshotRetention  = 30 * 24 * time.Hour
)

// MergeJobKind is the job kind of a merge (JOB-01).
const MergeJobKind = "ci.merge"

var (
	// ErrMergeConflict: one of the CIs is part of another merge running or
	// done (409).
	ErrMergeConflict = errors.New("the CI is part of another merge")
	// ErrMergeSelf: survivor and victim are the same CI.
	ErrMergeSelf = errors.New("a CI cannot be merged into itself")
	// ErrMergeUnsupported: the repository cannot merge.
	ErrMergeUnsupported = errors.New("merge is not supported by this repository")
	// ErrUnmergeConflict: the survivor changed after the merge; only an
	// unmerge with force restores the snapshot (409, CI-12, E-80).
	ErrUnmergeConflict = errors.New("the survivor changed after the merge")
)

// UnmergeConflictError names the fields of the survivor changed after the
// merge (the ci_change rows above the version of the merge) and its
// current version; the unmerge changed nothing (409, E-80).
type UnmergeConflictError struct {
	Fields  []string
	Current int64
}

// validation: a fixed rule text with the names of the changed fields.
func (e *UnmergeConflictError) Error() string {
	return fmt.Sprintf("%s; unmerge with force=true to restore the snapshot: %s", ErrUnmergeConflict, strings.Join(e.Fields, ", "))
}

func (e *UnmergeConflictError) Unwrap() error { return ErrUnmergeConflict }

// MergeResult is the outcome of a merge.
type MergeResult struct {
	// MergeID identifies the merge (and its job, ref ci_merge/MergeID).
	MergeID  string `json:"merge_id"`
	Survivor *Item  `json:"survivor"`
	VictimID string `json:"victim_id"`
	// Moved counts the moved rows per reference kind.
	Moved map[string]int `json:"moved"`
	// OverrideConflicts are the fields with contradicting overrides; each
	// opened an override_conflict review.
	OverrideConflicts []string  `json:"override_conflicts"`
	TombstoneUntil    time.Time `json:"tombstone_until"`
	SnapshotUntil     time.Time `json:"snapshot_until"`
}

// Merger merges a victim CI into a survivor (CI-12); mergeID names the
// merge row and may be empty.
type Merger interface {
	Merge(ctx context.Context, orgID, survivorID, victimID, mergeID string) (*MergeResult, error)
}

// TombstoneReader resolves a merged victim to its survivor while the
// tombstone lasts.
type TombstoneReader interface {
	MergedInto(ctx context.Context, orgID, victimID string) (survivorID string, found bool, err error)
}

// WithJobs records every merge as a job of kind ci.merge (JOB-01).
func (s *Service) WithJobs(tracker jobs.Tracker) *Service {
	s.jobs = tracker
	return s
}

// Merge merges victimID into survivorID (CI-12). With a job tracker the
// merge is a job: queued, running and finished with the merge row as its
// reference.
func (s *Service) Merge(ctx context.Context, orgID, survivorID, victimID string) (*MergeResult, error) {
	merger, ok := s.repo.(Merger)
	if !ok {
		return nil, ErrMergeUnsupported
	}
	if strings.EqualFold(survivorID, victimID) {
		return nil, ErrMergeSelf
	}
	if s.jobs == nil {
		return merger.Merge(ctx, orgID, survivorID, victimID, "")
	}
	mergeID := newMergeID()
	actor := tenant.FromContext(ctx).UserID
	if err := s.jobs.Track(ctx, orgID, MergeJobKind, "ci_merge", mergeID, actor); err != nil {
		return nil, fmt.Errorf("track merge job: %w", err)
	}
	_ = s.jobs.Mirror(ctx, orgID, "ci_merge", mergeID, jobs.StatusRunning, "")
	result, err := merger.Merge(ctx, orgID, survivorID, victimID, mergeID)
	if err != nil {
		_ = s.jobs.Mirror(ctx, orgID, "ci_merge", mergeID, jobs.StatusFailed, err.Error())
		return nil, err
	}
	_ = s.jobs.Mirror(ctx, orgID, "ci_merge", mergeID, jobs.StatusSucceeded, "")
	return result, nil
}

// MergedInto returns the survivor of a merged victim while its tombstone
// lasts.
func (s *Service) MergedInto(ctx context.Context, orgID, victimID string) (survivorID string, found bool, err error) {
	reader, ok := s.repo.(TombstoneReader)
	if !ok {
		return "", false, nil
	}
	return reader.MergedInto(ctx, orgID, victimID)
}

// newMergeID returns a random UUID (version 4).
func newMergeID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// mergedAttributes unites the business attributes of both CIs: keys only
// the victim carries are taken, for keys both carry the value of the higher
// ranked discovery source wins (the survivor on a tie); tags are united.
// The reserved namespaces are left to the field values.
func mergedAttributes(survivor, victim *Item) map[string]any {
	out := map[string]any{}
	for k, v := range survivor.Attributes {
		if !IsReservedKey(k) {
			out[k] = v
		}
	}
	victimWins := override.SourceRank(victim.DiscoverySource) > override.SourceRank(survivor.DiscoverySource)
	for k, v := range victim.Attributes {
		if IsReservedKey(k) || v == nil {
			continue
		}
		current, ok := out[k]
		switch {
		case k == "tags":
			out[k] = unitedTags(current, v)
		case !ok || current == nil || current == "" || victimWins:
			out[k] = v
		}
	}
	return out
}

func unitedTags(a, b any) any {
	seen := map[string]bool{}
	var tags []string
	for _, list := range []any{a, b} {
		items, ok := list.([]any)
		if !ok {
			continue
		}
		for _, t := range items {
			if s, isString := t.(string); isString && !seen[s] {
				seen[s] = true
				tags = append(tags, s)
			}
		}
	}
	if tags == nil {
		if a != nil {
			return a
		}
		return b
	}
	sort.Strings(tags)
	out := make([]any, len(tags))
	for i, t := range tags {
		out[i] = t
	}
	return out
}

// mergeSnapshot is what an unmerge restores: both CI rows before the
// merge, the rows the merge changed (in their state before) and the rows
// it removed, each with its table.
type mergeSnapshot struct {
	Survivor json.RawMessage `json:"survivor"`
	Victim   json.RawMessage `json:"victim"`
	Changed  []snapshotRow   `json:"changed"`
	Removed  []snapshotRow   `json:"removed"`
}

type snapshotRow struct {
	Table string          `json:"table"`
	Row   json.RawMessage `json:"row"`
}

// rows collects to_jsonb of the rows a query returns.
func collectRows(ctx context.Context, tx pgx.Tx, table, query string, args ...any) ([]snapshotRow, error) {
	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("snapshot %s: %w", table, err)
	}
	defer rows.Close()
	var out []snapshotRow
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, snapshotRow{Table: table, Row: raw})
	}
	return out, rows.Err()
}

// mergeReference moves the rows of table whose column references the
// victim: dedupe removes the rows that would duplicate a row of the
// survivor (the snapshot keeps them), then the rest is moved.
type mergeReference struct {
	key    string
	table  string
	column string
	// where narrows the rows (document links of CIs).
	where string
	// dedupe deletes conflicting victim rows ($1 survivor, $2 victim) and
	// returns them as to_jsonb.
	dedupe string
	// prepare adjusts victim rows before the move ($1 survivor, $2 victim).
	prepare string
}

var mergeReferences = []mergeReference{
	{
		key: "relationships", table: "ci_relationship", column: "source_ci_id",
		dedupe: `DELETE FROM ci_relationship v WHERE (v.source_ci_id = $2 AND v.target_ci_id = $1)
			OR (v.target_ci_id = $2 AND v.source_ci_id = $1)
			OR ((v.source_ci_id = $2 OR v.target_ci_id = $2) AND EXISTS (
				SELECT 1 FROM ci_relationship s WHERE s.organization_id = v.organization_id AND s.id <> v.id
					AND s.rel_type = v.rel_type
					AND s.source_ci_id = CASE WHEN v.source_ci_id = $2 THEN $1 ELSE v.source_ci_id END
					AND s.target_ci_id = CASE WHEN v.target_ci_id = $2 THEN $1 ELSE v.target_ci_id END
					AND s.source_interface_id IS NOT DISTINCT FROM v.source_interface_id
					AND s.target_interface_id IS NOT DISTINCT FROM v.target_interface_id))
			RETURNING to_jsonb(v)`,
	},
	{key: "relationships", table: "ci_relationship", column: "target_ci_id"},
	{
		key: "interfaces", table: "network_interface", column: "ci_id",
		// A name the survivor uses already gets the victim's id as suffix.
		prepare: `UPDATE network_interface v SET name = v.name || ' (' || left(v.ci_id::text, 8) || ')'
			WHERE v.ci_id = $2 AND EXISTS (SELECT 1 FROM network_interface s WHERE s.ci_id = $1 AND s.name = v.name)`,
	},
	{
		key: "contacts", table: "ci_contact", column: "ci_id",
		dedupe: `DELETE FROM ci_contact v WHERE v.ci_id = $2 AND EXISTS (SELECT 1 FROM ci_contact s
			WHERE s.ci_id = $1 AND s.contact_id = v.contact_id AND s.relationship_type = v.relationship_type)
			RETURNING to_jsonb(v)`,
	},
	{key: "history", table: "ci_change", column: "ci_id"},
	{key: "documents", table: "document_link", column: "entity_id", where: "entity_type = 'ci'"},
	// The endpoint agent follows its CI, so its next telemetry updates the
	// survivor instead of creating a new CI (F-083).
	{key: "agents", table: "endpoint_agent", column: "ci_id"},
}

// Merge implements Merger in one transaction.
func (r *PGRepository) Merge(ctx context.Context, orgID, survivorID, victimID, mergeID string) (*MergeResult, error) {
	if strings.EqualFold(survivorID, victimID) {
		return nil, ErrMergeSelf
	}
	result := &MergeResult{VictimID: victimID, Moved: map[string]int{}, OverrideConflicts: []string{}}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		survivor, victim, err := lockMergePair(ctx, tx, orgID, survivorID, victimID)
		if err != nil {
			return err
		}
		snap := mergeSnapshot{}
		if err = tx.QueryRow(ctx, "SELECT to_jsonb(c) FROM ci c WHERE id = $1", survivorID).Scan(&snap.Survivor); err != nil {
			return fmt.Errorf("snapshot survivor: %w", err)
		}
		if err = tx.QueryRow(ctx, "SELECT to_jsonb(c) FROM ci c WHERE id = $1", victimID).Scan(&snap.Victim); err != nil {
			return fmt.Errorf("snapshot victim: %w", err)
		}

		// The victim leaves first, so its unique values (serial number,
		// hardware UUID, MAC) can pass to the survivor.
		if _, err = tx.Exec(ctx, `UPDATE ci SET deleted_at = NOW(), updated_at = NOW(), merged_into_id = $2, asset_id = NULL,
				version = version + 1 WHERE id = $1`, victimID, survivorID); err != nil {
			return fmt.Errorf("retire victim: %w", err)
		}
		assetID := survivor.AssetID
		if victim.AssetID != "" {
			changed, cerr := collectRows(ctx, tx, "asset", "SELECT to_jsonb(a) FROM asset a WHERE id = $1", victim.AssetID)
			if cerr != nil {
				return cerr
			}
			snap.Changed = append(snap.Changed, changed...)
			if survivor.AssetID == "" {
				assetID = victim.AssetID
				_, err = tx.Exec(ctx, "UPDATE asset SET ci_id = $1 WHERE id = $2 AND ci_id = $3", survivorID, victim.AssetID, victimID)
			} else {
				_, err = tx.Exec(ctx, "UPDATE asset SET ci_id = NULL WHERE id = $1 AND ci_id = $2", victim.AssetID, victimID)
			}
			if err != nil {
				return fmt.Errorf("move asset link: %w", err)
			}
			if assetID == victim.AssetID {
				result.Moved["asset"] = 1
			}
		}
		attrs, err := json.Marshal(mergedAttributes(survivor, victim))
		if err != nil {
			return fmt.Errorf("encode merged attributes: %w", err)
		}
		victimWins := override.SourceRank(victim.DiscoverySource) > override.SourceRank(survivor.DiscoverySource)
		pick := func(col string) string {
			if victimWins {
				return fmt.Sprintf("COALESCE(NULLIF(v.%[1]s, ''), s.%[1]s)", col)
			}
			return fmt.Sprintf("COALESCE(NULLIF(s.%[1]s, ''), v.%[1]s)", col)
		}
		sets := []string{}
		for _, col := range []string{"manufacturer", "model", "serial_number", "firmware_version", "hostname", "fqdn",
			"os_name", "os_version", "sys_object_id"} {
			sets = append(sets, col+" = "+pick(col))
		}
		for _, col := range []string{"management_ip", "hardware_uuid", "primary_mac"} {
			sets = append(sets, fmt.Sprintf("%[1]s = COALESCE(s.%[1]s, v.%[1]s)", col))
		}
		_, err = tx.Exec(ctx, fmt.Sprintf(`UPDATE ci s SET %s,
				attributes = s.attributes - (SELECT coalesce(array_agg(k), '{}') FROM jsonb_object_keys(s.attributes) k WHERE left(k, 1) <> '_') || $3::jsonb,
				asset_id = NULLIF($4, '')::uuid,
				first_seen_at = LEAST(s.first_seen_at, v.first_seen_at), last_seen_at = GREATEST(s.last_seen_at, v.last_seen_at),
				version = s.version + 1, updated_at = NOW()
			FROM ci v WHERE s.id = $1 AND v.id = $2`, strings.Join(sets, ", ")), survivorID, victimID, attrs, assetID)
		if err != nil {
			return locationError(fmt.Errorf("merge into survivor: %w", err))
		}

		for i := range mergeReferences {
			ref := &mergeReferences[i]
			n, rerr := moveReference(ctx, tx, ref, survivorID, victimID, &snap)
			if rerr != nil {
				return rerr
			}
			result.Moved[ref.key] += n
		}
		conflicts, err := mergeFieldValues(ctx, tx, orgID, survivorID, victimID, &snap)
		if err != nil {
			return err
		}
		result.OverrideConflicts = conflicts

		merged, err := scanCI(tx.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM ci WHERE id = $1", ciSelectColumns), survivorID))
		if err != nil {
			return fmt.Errorf("read survivor: %w", err)
		}
		result.Survivor = merged
		rawSnap, err := json.Marshal(snap)
		if err != nil {
			return fmt.Errorf("encode merge snapshot: %w", err)
		}
		actor := nilIfEmpty(tenant.FromContext(ctx).UserID)
		err = tx.QueryRow(ctx, `INSERT INTO ci_merge (id, organization_id, client_id, site_id, survivor_id, victim_id, actor_id, snapshot,
				tombstone_until, snapshot_until)
			VALUES (COALESCE(NULLIF($1, '')::uuid, gen_random_uuid()), $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5, $6, $7, $8::jsonb,
				now() + $9 * interval '1 second', now() + $10 * interval '1 second')
			RETURNING id::text, tombstone_until, snapshot_until`,
			mergeID, orgID, merged.ClientID, merged.SiteID, survivorID, victimID, actor, rawSnap,
			int64(MergeTombstoneRetention/time.Second), int64(MergeSnapshotRetention/time.Second)).
			Scan(&result.MergeID, &result.TombstoneUntil, &result.SnapshotUntil)
		if err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23505" {
				return ErrMergeConflict
			}
			return fmt.Errorf("record merge: %w", err)
		}

		if cerr := r.recordCIChange(ctx, tx, orgID, survivorID, merged.Version, "reconciliation_decision", "merged_ci",
			nil, map[string]any{"victim_id": victimID, "merge_id": result.MergeID, "moved": result.Moved}); cerr != nil {
			return cerr
		}
		if cerr := r.recordCIChange(ctx, tx, orgID, victimID, victim.Version+1, "delete", "merged_into_id",
			map[string]any{"status": victim.Status}, map[string]any{"merged_into_id": survivorID, "merge_id": result.MergeID}); cerr != nil {
			return cerr
		}
		if aerr := r.recordAudit(ctx, tx, "ci.merged", merged, map[string]interface{}{
			"victim_id": victimID, "merge_id": result.MergeID, "moved": result.Moved, "override_conflicts": result.OverrideConflicts,
		}); aerr != nil {
			return aerr
		}
		if r.outbox == nil {
			return nil
		}
		_, err = r.outbox.Append(ctx, tx, &events.Event{OrgID: orgID, Type: events.CIMerged, EntityID: survivorID,
			Data: map[string]any{"survivor_id": survivorID, "victim_id": victimID, "merge_id": result.MergeID}})
		return err
	})
	if err != nil {
		return nil, r.uniqueField(ctx, orgID, err)
	}
	return result, nil
}

// lockMergePair locks both CIs without waiting: a CI another merge (or
// write) holds is ErrMergeConflict, a merged victim as well.
func lockMergePair(ctx context.Context, tx pgx.Tx, orgID, survivorID, victimID string) (survivor, victim *Item, err error) {
	if _, err = tx.Exec(ctx, "SELECT id FROM ci WHERE id = ANY($1::uuid[]) AND organization_id = $2 ORDER BY id FOR UPDATE NOWAIT",
		[]string{survivorID, victimID}, orgID); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
			return nil, nil, ErrMergeConflict
		}
		return nil, nil, fmt.Errorf("lock merge pair: %w", err)
	}
	read := func(id string) (*Item, error) {
		item, rerr := scanCI(tx.QueryRow(ctx, fmt.Sprintf("SELECT %s FROM ci WHERE id = $1 AND organization_id = $2", ciSelectColumns), id, orgID))
		if errors.Is(rerr, pgx.ErrNoRows) {
			return nil, fmt.Errorf("not found")
		}
		return item, rerr
	}
	if survivor, err = read(survivorID); err != nil {
		return nil, nil, err
	}
	if victim, err = read(victimID); err != nil {
		return nil, nil, err
	}
	if victim.MergedIntoID != "" || survivor.MergedIntoID != "" {
		return nil, nil, ErrMergeConflict
	}
	if victim.DeletedAt != nil || survivor.DeletedAt != nil {
		return nil, nil, fmt.Errorf("not found")
	}
	return survivor, victim, nil
}

// moveReference moves one reference of the victim to the survivor and
// keeps the rows before the move in the snapshot.
func moveReference(ctx context.Context, tx pgx.Tx, ref *mergeReference, survivorID, victimID string, snap *mergeSnapshot) (int, error) {
	where := fmt.Sprintf("%s = $1", ref.column)
	if ref.where != "" {
		where += " AND " + ref.where
	}
	if ref.dedupe != "" {
		removed, err := collectRows(ctx, tx, ref.table, ref.dedupe, survivorID, victimID)
		if err != nil {
			return 0, err
		}
		snap.Removed = append(snap.Removed, removed...)
	}
	changed, err := collectRows(ctx, tx, ref.table, fmt.Sprintf("SELECT to_jsonb(t) FROM %s t WHERE %s", ref.table, where), victimID)
	if err != nil {
		return 0, err
	}
	snap.Changed = append(snap.Changed, changed...)
	if ref.prepare != "" {
		if _, err = tx.Exec(ctx, ref.prepare, survivorID, victimID); err != nil {
			return 0, fmt.Errorf("prepare %s: %w", ref.table, err)
		}
	}
	tag, err := tx.Exec(ctx, fmt.Sprintf("UPDATE %s SET %s = $2 WHERE %s", ref.table, ref.column, where), victimID, survivorID)
	if err != nil {
		return 0, fmt.Errorf("move %s: %w", ref.table, err)
	}
	return int(tag.RowsAffected()), nil
}

// fieldValue is one row of ci_field_value as the merge compares it.
type mergeFieldValue struct {
	id       string
	override []byte
}

// mergeFieldValues keeps the field values of both CIs: fields only the
// victim has move to the survivor; an override of the victim on a field
// the survivor has without override passes to it; contradicting overrides
// keep the survivor's and open an override_conflict review with the
// victim's value. Victim rows that do not move are removed (snapshot).
func mergeFieldValues(ctx context.Context, tx pgx.Tx, orgID, survivorID, victimID string, snap *mergeSnapshot) ([]string, error) {
	load := func(ciID string) (map[string]mergeFieldValue, error) {
		rows, err := tx.Query(ctx, "SELECT id::text, field_name, override_value FROM ci_field_value WHERE ci_id = $1", ciID)
		if err != nil {
			return nil, fmt.Errorf("read field values: %w", err)
		}
		defer rows.Close()
		out := map[string]mergeFieldValue{}
		for rows.Next() {
			var v mergeFieldValue
			var field string
			if err := rows.Scan(&v.id, &field, &v.override); err != nil {
				return nil, err
			}
			out[field] = v
		}
		return out, rows.Err()
	}
	survivorValues, err := load(survivorID)
	if err != nil {
		return nil, err
	}
	victimValues, err := load(victimID)
	if err != nil {
		return nil, err
	}
	fields := make([]string, 0, len(victimValues))
	for f := range victimValues {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	conflicts := []string{}
	for _, field := range fields {
		v := victimValues[field]
		rowSnap := func(into *[]snapshotRow, id string) error {
			rows, cerr := collectRows(ctx, tx, "ci_field_value", "SELECT to_jsonb(f) FROM ci_field_value f WHERE id = $1", id)
			*into = append(*into, rows...)
			return cerr
		}
		s, onSurvivor := survivorValues[field]
		switch {
		case !onSurvivor:
			if serr := rowSnap(&snap.Changed, v.id); serr != nil {
				return nil, serr
			}
			_, err = tx.Exec(ctx, "UPDATE ci_field_value SET ci_id = $1 WHERE id = $2", survivorID, v.id)
		case v.override != nil && s.override == nil:
			if serr := rowSnap(&snap.Changed, s.id); serr != nil {
				return nil, serr
			}
			_, err = tx.Exec(ctx, `UPDATE ci_field_value s SET override_value = v.override_value, override_author = v.override_author,
					override_reason = v.override_reason, override_at = v.override_at, protected = s.protected OR v.protected
				FROM ci_field_value v WHERE s.id = $1 AND v.id = $2`, s.id, v.id)
		case v.override != nil && !sameJSON(v.override, s.override):
			conflicts = append(conflicts, field)
			err = openMergeConflict(ctx, tx, orgID, survivorID, victimID, field, v.override)
		}
		if err != nil {
			return nil, fmt.Errorf("merge field value %s: %w", field, err)
		}
		if onSurvivor {
			if serr := rowSnap(&snap.Removed, v.id); serr != nil {
				return nil, serr
			}
			if _, err = tx.Exec(ctx, "DELETE FROM ci_field_value WHERE id = $1", v.id); err != nil {
				return nil, fmt.Errorf("remove field value %s: %w", field, err)
			}
		}
	}
	return conflicts, nil
}

func sameJSON(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// openMergeConflict opens the override_conflict review of a field whose
// overrides contradict (REC-12): the survivor's override stays.
func openMergeConflict(ctx context.Context, tx pgx.Tx, orgID, survivorID, victimID, field string, value []byte) error {
	payload, err := json.Marshal(map[string]any{
		"ci_id": survivorID, "field": field, "observed_value": json.RawMessage(value), "source": "merge", "victim_id": victimID,
	})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `INSERT INTO review_item (organization_id, kind, status, payload, candidate_ci_ids, object_key)
		VALUES ($1, 'override_conflict', 'open', $2::jsonb, ARRAY[$3::uuid], $4)
		ON CONFLICT (organization_id, kind, object_key) WHERE status = 'open' AND object_key IS NOT NULL DO NOTHING`,
		orgID, payload, survivorID, "ci:"+survivorID+"/"+field)
	return err
}

// MergedInto implements TombstoneReader.
func (r *PGRepository) MergedInto(ctx context.Context, orgID, victimID string) (survivorID string, found bool, err error) {
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT survivor_id::text FROM ci_merge
			WHERE organization_id = $1 AND victim_id = $2 AND unmerged_at IS NULL AND tombstone_until > now()
			ORDER BY merged_at DESC LIMIT 1`, orgID, victimID).Scan(&survivorID)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return survivorID, true, nil
}

// ErrSnapshotExpired: the snapshot of the merge is older than 30 days; the
// unmerge is no longer possible (410).
var ErrSnapshotExpired = errors.New("the merge snapshot has expired")

// Unmerger reverts a merge from its snapshot (CI-12). Without force a
// survivor changed after the merge refuses the unmerge
// (*UnmergeConflictError); force restores the snapshot over those changes
// (E-80).
type Unmerger interface {
	Unmerge(ctx context.Context, orgID, victimID string, force bool) (*UnmergeResult, error)
}

// UnmergeResult names both CIs after the unmerge.
type UnmergeResult struct {
	MergeID  string `json:"merge_id"`
	Survivor *Item  `json:"survivor"`
	Victim   *Item  `json:"victim"`
}

// Unmerge reverts the merge of victimID within the snapshot retention;
// force overrides later changes of the survivor (E-80).
func (s *Service) Unmerge(ctx context.Context, orgID, victimID string, force bool) (*UnmergeResult, error) {
	unmerger, ok := s.repo.(Unmerger)
	if !ok {
		return nil, ErrMergeUnsupported
	}
	return unmerger.Unmerge(ctx, orgID, victimID, force)
}

// snapshotTables are the tables a merge snapshot may name; only these are
// written by the unmerge.
var snapshotTables = map[string]bool{
	"asset": true, "ci_relationship": true, "network_interface": true, "ci_contact": true,
	"ci_change": true, "document_link": true, "ci_field_value": true, "ci": true, "endpoint_agent": true,
}

// snapshotRestoreColumns limits what the unmerge writes back for a table:
// of an endpoint agent only the CI link, so its later state (kill-switch,
// policy, heartbeat) stays.
var snapshotRestoreColumns = map[string][]string{
	"endpoint_agent": {"ci_id"},
}

// tableColumns returns the writable columns of a snapshot table.
func tableColumns(ctx context.Context, tx pgx.Tx, table string) ([]string, error) {
	if !snapshotTables[table] {
		return nil, fmt.Errorf("unexpected snapshot table %q", table)
	}
	rows, err := tx.Query(ctx, `SELECT column_name FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = $1 AND is_generated = 'NEVER' ORDER BY ordinal_position`, table)
	if err != nil {
		return nil, fmt.Errorf("columns of %s: %w", table, err)
	}
	defer rows.Close()
	var cols []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		cols = append(cols, pgx.Identifier{c}.Sanitize())
	}
	return cols, rows.Err()
}

// restoreRow writes a snapshot row back: an existing row (by id) takes the
// snapshot state, a removed one is inserted again.
func restoreRow(ctx context.Context, tx pgx.Tx, cols map[string][]string, row *snapshotRow, insert bool) error {
	c, ok := cols[row.Table]
	if !ok {
		var err error
		if c, err = tableColumns(ctx, tx, row.Table); err != nil {
			return err
		}
		cols[row.Table] = c
	}
	if only, ok := snapshotRestoreColumns[row.Table]; ok && !insert {
		c = only
	}
	list := strings.Join(c, ", ")
	table := pgx.Identifier{row.Table}.Sanitize()
	var query string
	if insert {
		query = fmt.Sprintf("INSERT INTO %[1]s (%[2]s) SELECT %[2]s FROM jsonb_populate_record(NULL::%[1]s, $1::jsonb)", table, list)
	} else {
		query = fmt.Sprintf("UPDATE %[1]s SET (%[2]s) = (SELECT %[2]s FROM jsonb_populate_record(NULL::%[1]s, $1::jsonb)) WHERE id = ($1::jsonb ->> 'id')::uuid",
			table, list)
	}
	if _, err := tx.Exec(ctx, query, []byte(row.Row)); err != nil {
		return fmt.Errorf("restore %s: %w", row.Table, err)
	}
	return nil
}

// laterSurvivorChanges returns the fields of the survivor changed after the
// merge: the ci_change rows above the version the merge recorded
// (reconciliation_decision/merged_ci of this merge). conflict is false
// while the survivor still has that version. A survivor without that row
// counts as changed, without fields.
func laterSurvivorChanges(ctx context.Context, tx pgx.Tx, survivorID, mergeID string) (fields []string, current int64, conflict bool, err error) {
	var merged *int64
	err = tx.QueryRow(ctx, `SELECT c.version,
			(SELECT max(ch.ci_version) FROM ci_change ch WHERE ch.ci_id = c.id AND ch.change_type = 'reconciliation_decision'
				AND ch.field_name = 'merged_ci' AND ch.new_value ->> 'merge_id' = $2)
		FROM ci c WHERE c.id = $1`, survivorID, mergeID).Scan(&current, &merged)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// A survivor outside the caller's scope is not found (404).
		return nil, 0, false, fmt.Errorf("not found")
	case err != nil:
		return nil, 0, false, fmt.Errorf("read survivor version: %w", err)
	}
	if merged != nil && current <= *merged {
		return nil, current, false, nil
	}
	since := int64(0)
	if merged != nil {
		since = *merged
	}
	rows, err := tx.Query(ctx, `SELECT DISTINCT COALESCE(field_name, change_type) FROM ci_change
		WHERE ci_id = $1 AND ci_version > $2 ORDER BY 1`, survivorID, since)
	if err != nil {
		return nil, 0, false, fmt.Errorf("read survivor changes since the merge: %w", err)
	}
	if fields, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
		return nil, 0, false, fmt.Errorf("read survivor changes since the merge: %w", err)
	}
	if merged == nil {
		fields = nil
	}
	return fields, current, true, nil
}

// Unmerge implements Unmerger: both CI rows, the moved rows and the removed
// rows return to their state before the merge. A survivor changed after the
// merge refuses it unless force is set (E-80); the audit names force and
// the overwritten fields.
func (r *PGRepository) Unmerge(ctx context.Context, orgID, victimID string, force bool) (*UnmergeResult, error) {
	result := &UnmergeResult{}
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		var survivorID string
		var expired bool
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT id::text, survivor_id::text, snapshot_until < now(), snapshot FROM ci_merge
			WHERE organization_id = $1 AND victim_id = $2 AND unmerged_at IS NULL FOR UPDATE NOWAIT`, orgID, victimID).
			Scan(&result.MergeID, &survivorID, &expired, &raw)
		var pgErr *pgconn.PgError
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			return fmt.Errorf("not found")
		case errors.As(err, &pgErr) && pgErr.Code == "55P03":
			return ErrMergeConflict
		case err != nil:
			return fmt.Errorf("read merge: %w", err)
		case expired:
			return ErrSnapshotExpired
		}
		if _, err = tx.Exec(ctx, "SELECT id FROM ci WHERE id = ANY($1::uuid[]) ORDER BY id FOR UPDATE NOWAIT",
			[]string{survivorID, victimID}); err != nil {
			if errors.As(err, &pgErr) && pgErr.Code == "55P03" {
				return ErrMergeConflict
			}
			return fmt.Errorf("lock merge pair: %w", err)
		}
		// Later changes of the survivor are not lost silently (F-097).
		overwritten, current, changed, err := laterSurvivorChanges(ctx, tx, survivorID, result.MergeID)
		if err != nil {
			return err
		}
		if changed && !force {
			return &UnmergeConflictError{Fields: append([]string{}, overwritten...), Current: current}
		}
		if overwritten == nil {
			overwritten = []string{}
		}
		var snap mergeSnapshot
		if err = json.Unmarshal(raw, &snap); err != nil {
			return fmt.Errorf("decode merge snapshot: %w", err)
		}
		cols := map[string][]string{}
		// The survivor first gives up the unique values of the victim.
		for _, row := range []snapshotRow{{Table: "ci", Row: snap.Survivor}, {Table: "ci", Row: snap.Victim}} {
			if err = restoreRow(ctx, tx, cols, &row, false); err != nil {
				return locationError(err)
			}
		}
		for i := range snap.Changed {
			if rerr := restoreRow(ctx, tx, cols, &snap.Changed[i], false); rerr != nil {
				return rerr
			}
		}
		for i := range snap.Removed {
			if rerr := restoreRow(ctx, tx, cols, &snap.Removed[i], true); rerr != nil {
				return rerr
			}
		}
		// Both CIs get a new version: the snapshot state is a new write.
		if _, err = tx.Exec(ctx, `UPDATE ci SET version = version + 2, updated_at = NOW() WHERE id = ANY($1::uuid[])`,
			[]string{survivorID, victimID}); err != nil {
			return fmt.Errorf("bump versions: %w", err)
		}
		if _, err = tx.Exec(ctx, "UPDATE ci_merge SET unmerged_at = now() WHERE id = $1", result.MergeID); err != nil {
			return fmt.Errorf("close merge: %w", err)
		}
		query := fmt.Sprintf("SELECT %s FROM ci WHERE id = $1", ciSelectColumns)
		if result.Survivor, err = scanCI(tx.QueryRow(ctx, query, survivorID)); err != nil {
			return fmt.Errorf("read survivor: %w", err)
		}
		if result.Victim, err = scanCI(tx.QueryRow(ctx, query, victimID)); err != nil {
			return fmt.Errorf("read victim: %w", err)
		}
		for _, item := range []*Item{result.Survivor, result.Victim} {
			if cerr := r.recordCIChange(ctx, tx, orgID, item.ID, item.Version, "reconciliation_decision", "unmerged_ci",
				nil, map[string]any{"merge_id": result.MergeID, "survivor_id": survivorID, "victim_id": victimID}); cerr != nil {
				return cerr
			}
		}
		if aerr := r.recordAudit(ctx, tx, "ci.unmerged", result.Survivor, map[string]interface{}{
			"victim_id": victimID, "merge_id": result.MergeID, "force": force, "overwritten_fields": overwritten,
		}); aerr != nil {
			return aerr
		}
		// EVT-02 has no unmerge event: the victim is restored, the survivor
		// changed.
		if eerr := r.recordEvent(ctx, tx, events.CIRestored, result.Victim, nil); eerr != nil {
			return eerr
		}
		return r.recordEvent(ctx, tx, events.CIChanged, result.Survivor, []string{"merged_ci"})
	})
	if err != nil {
		return nil, r.uniqueField(ctx, orgID, err)
	}
	return result, nil
}

// mergeVictimReader resolves a merge id to its victim.
type mergeVictimReader interface {
	MergeVictim(ctx context.Context, orgID, mergeID string) (string, error)
}

// RevertMerge reverts the merge mergeID (the job ci.merge, JOB-03 revert)
// through the unmerge of its victim.
func (s *Service) RevertMerge(ctx context.Context, orgID, mergeID string) (*UnmergeResult, error) {
	reader, ok := s.repo.(mergeVictimReader)
	if !ok {
		return nil, ErrMergeUnsupported
	}
	victim, err := reader.MergeVictim(ctx, orgID, mergeID)
	if err != nil {
		return nil, err
	}
	// The revert never overrides later changes of the survivor (E-80).
	return s.Unmerge(ctx, orgID, victim, false)
}

// MergeVictim implements mergeVictimReader.
func (r *PGRepository) MergeVictim(ctx context.Context, orgID, mergeID string) (string, error) {
	var victim string
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, "SELECT victim_id::text FROM ci_merge WHERE organization_id = $1 AND id = $2", orgID, mergeID).Scan(&victim)
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return "", fmt.Errorf("not found")
	}
	return victim, err
}
