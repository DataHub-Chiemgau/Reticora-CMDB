package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Managed probes (IPM-11, WP-1062): IPAM functions (the subnet scan, later
// the PTR check of WP-1064) keep probes of their own, marked with managed_by
// and a key managed_key unique per organization and function. They are
// created, changed and deleted only by SyncManaged, set-based in the
// transaction of the caller; PATCH and DELETE of the probe API refuse them
// with 409 (managed_by). Every spec passes the checks of the write path
// (checkStatic and validateEffective: probewire.Validate on the effective
// values) and the probe budget of the organization (WP-940) like a manual
// probe; the first violation ends the sync with its field error. After the
// commit the assignment of the touched collector groups runs (WP-941 AK4).

// Functions that manage probes (probe.managed_by).
const (
	ManagedByScan = "ipam_scan"
	ManagedByPTR  = "ipam_ptr"
)

// ManagedSpec is the wanted state of one managed probe: always enabled,
// without CI, with the client of its owner.
type ManagedSpec struct {
	Key              string
	ClientID         string
	CollectorGroupID string
	Type             string
	Target           string
	// Params are the overrides of the level probe (e.g. interval_s).
	Params json.RawMessage
}

// SyncStats is the outcome of SyncManaged: the probes created, changed and
// deleted, and the probe id of every key of the specs.
type SyncStats struct {
	Created, Updated, Deleted int
	IDs                       map[string]string
}

// LockBudget takes the probe budget lock of the organization (WP-940 AK3);
// a caller that locks rows before SyncManaged takes it first.
func (r *PGRepository) LockBudget(ctx context.Context, tx pgx.Tx, orgID string) error {
	return r.lockBudget(ctx, tx, orgID)
}

// SyncManaged makes the probes of managedBy whose key starts with keyPrefix
// the probes of specs (AK4): new keys are created, changed ones updated
// (client, collector group, type, target, params), missing ones deleted.
// The number of statements does not depend on the number of specs (AK4,
// Nachtrag 2026-11-13): one statement writes the probes and raises the
// config versions of the collectors of deleted assigned probes, one appends
// the events config.changed; the checks of the write path read the active
// probes and their profile layers (one more read when active probes carry
// CIs of monitoring groups), the plan minimum through the budget service,
// and widen the scope of a restricted principal (two statements). For an
// org-wide principal without CI layers that is four statements plus the
// plan minimum.
func (r *PGRepository) SyncManaged(ctx context.Context, tx pgx.Tx, orgID, managedBy, keyPrefix string, specs []ManagedSpec) (SyncStats, error) {
	st := SyncStats{IDs: map[string]string{}}
	if managedBy != ManagedByScan && managedBy != ManagedByPTR {
		return st, fmt.Errorf("probe: unknown managing function %q", managedBy)
	}
	if keyPrefix == "" {
		return st, errors.New("probe: a managed sync needs a key prefix")
	}
	next := make([]Probe, 0, len(specs))
	seen := map[string]bool{}
	for i := range specs {
		s := &specs[i]
		if !strings.HasPrefix(s.Key, keyPrefix) || seen[s.Key] {
			return st, fmt.Errorf("probe: managed key %q outside %q or twice", s.Key, keyPrefix)
		}
		seen[s.Key] = true
		p := Probe{OrganizationID: orgID, ClientID: s.ClientID, CollectorGroupID: s.CollectorGroupID, Type: s.Type,
			Target: s.Target, Params: s.Params, Enabled: true, ManagedBy: managedBy, ManagedKey: s.Key}
		if err := checkStatic(&p); err != nil {
			return st, err
		}
		next = append(next, p)
	}
	if err := r.admitManaged(ctx, tx, orgID, managedBy, keyPrefix, next); err != nil {
		return st, err
	}
	keys, clients, groups, types, targets, params := make([]string, len(next)), make([]*string, len(next)),
		make([]string, len(next)), make([]string, len(next)), make([]string, len(next)), make([]string, len(next))
	for i := range next {
		p := &next[i]
		keys[i], groups[i], types[i], targets[i], params[i] = p.ManagedKey, p.CollectorGroupID, p.Type, p.Target, string(p.Params)
		if p.ClientID != "" {
			c := p.ClientID
			clients[i] = &c
		}
	}
	// One statement: the deleted probes, the version raise of the
	// collectors they were assigned to (WP-941 AK3, like the delete of the
	// write path), the created or changed ones with the fields that changed
	// (an unchanged one is not touched) and the ids of the unchanged ones.
	rows, err := tx.Query(ctx, `WITH input AS (
			SELECT * FROM unnest($4::text[], $5::uuid[], $6::uuid[], $7::text[], $8::text[], $9::jsonb[])
			    AS i(key, client_id, group_id, type, target, params)
		), old AS (
			SELECT p.id, p.managed_key, p.collector_group_id, p.client_id, p.site_id, p.type, p.target, p.params FROM probe p
			 WHERE p.organization_id = $1 AND p.managed_by = $2 AND p.managed_key = ANY ($4::text[])
		), del AS (
			DELETE FROM probe p WHERE p.organization_id = $1 AND p.managed_by = $2 AND starts_with(p.managed_key, $3)
			   AND NOT p.managed_key = ANY ($4::text[])
			RETURNING p.id, p.managed_key, p.collector_group_id, p.client_id, p.site_id
		), raised AS (
			UPDATE collector_group_member m SET config_version = m.config_version + 1
			 WHERE m.organization_id = $1 AND m.collector_id IN (
			       SELECT a.collector_id FROM probe_assignment a
			        WHERE a.organization_id = $1 AND a.probe_id IN (SELECT id FROM del))
			RETURNING m.collector_id, m.config_version
		), up AS (
			INSERT INTO probe AS p (organization_id, client_id, collector_group_id, type, target, params, managed_by, managed_key)
			SELECT $1, i.client_id, i.group_id, i.type, i.target, i.params, $2, i.key FROM input i
			ON CONFLICT (organization_id, managed_by, managed_key) WHERE managed_by IS NOT NULL
			DO UPDATE SET client_id = EXCLUDED.client_id, collector_group_id = EXCLUDED.collector_group_id,
			    type = EXCLUDED.type, target = EXCLUDED.target, params = EXCLUDED.params
			 WHERE (p.client_id, p.collector_group_id, p.type, p.target, p.params)
			       IS DISTINCT FROM (EXCLUDED.client_id, EXCLUDED.collector_group_id, EXCLUDED.type, EXCLUDED.target, EXCLUDED.params)
			RETURNING p.id, p.managed_key, p.collector_group_id, p.client_id, p.site_id, p.type, p.target, p.params,
			    (p.xmax = 0) AS created
		)
		SELECT 'deleted', d.id::text, d.managed_key, d.collector_group_id::text, COALESCE(d.client_id::text, ''),
		       COALESCE(d.site_id::text, ''), '{}'::text[], 0::bigint
		  FROM del d
		UNION ALL
		SELECT 'raised', '', '', '', r.collector_id::text, '', '{}'::text[], r.config_version::bigint
		  FROM raised r
		UNION ALL
		SELECT CASE WHEN u.created THEN 'created' ELSE 'updated' END, u.id::text, u.managed_key, u.collector_group_id::text,
		       COALESCE(u.client_id::text, ''), COALESCE(u.site_id::text, ''),
		       array_remove(ARRAY[
		           CASE WHEN o.client_id IS DISTINCT FROM u.client_id THEN 'client_id' END,
		           CASE WHEN o.collector_group_id IS DISTINCT FROM u.collector_group_id THEN 'collector_group_id' END,
		           CASE WHEN o.type IS DISTINCT FROM u.type THEN 'type' END,
		           CASE WHEN o.target IS DISTINCT FROM u.target THEN 'target' END,
		           CASE WHEN o.params IS DISTINCT FROM u.params THEN 'params' END], NULL), 0::bigint
		  FROM up u LEFT JOIN old o ON o.managed_key = u.managed_key
		UNION ALL
		SELECT 'kept', o.id::text, o.managed_key, o.collector_group_id::text, COALESCE(o.client_id::text, ''), COALESCE(o.site_id::text, ''),
		       '{}'::text[], 0::bigint
		  FROM old o WHERE NOT EXISTS (SELECT 1 FROM up u WHERE u.managed_key = o.managed_key)`,
		orgID, managedBy, keyPrefix, keys, clients, groups, types, targets, params)
	if err != nil {
		return st, fmt.Errorf("sync managed probes: %w", probeWriteError(err))
	}
	var (
		touched []string
		raised  []CollectorVersion
		changes []*events.Event
	)
	for rows.Next() {
		var kind, id, key, group, client, site string
		var changed []string
		var version int64
		if err = rows.Scan(&kind, &id, &key, &group, &client, &site, &changed, &version); err != nil {
			rows.Close()
			return st, fmt.Errorf("sync managed probes: %w", err)
		}
		var fields []string
		switch kind {
		case "raised":
			// The collector id comes in the client column.
			raised = append(raised, CollectorVersion{CollectorID: client, ConfigVersion: version})
			continue
		case "deleted":
			st.Deleted++
			fields = []string{DeletedField}
		case "created":
			st.Created++
			st.IDs[key] = id
			fields = []string{"type", "target", "enabled", "collector_group_id"}
		case "updated":
			st.Updated++
			st.IDs[key] = id
			fields = changed
		default:
			st.IDs[key] = id
			continue
		}
		touched = append(touched, group)
		changes = append(changes, &events.Event{OrgID: orgID, Type: events.ProbeConfigChanged, EntityID: id,
			Data: events.ProbeConfigChangedData{ID: id, ClientID: client, SiteID: site, ChangedFields: fields}})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return st, fmt.Errorf("sync managed probes: %w", probeWriteError(err))
	}
	sort.Slice(raised, func(i, j int) bool { return raised[i].CollectorID < raised[j].CollectorID })
	if len(changes) > 0 {
		r.afterWrite(ctx, tx, orgID, touched, raised)
		// config.changed for every touched probe, one statement, as the
		// last write of the transaction (Konvention 20).
		if err := database.BeforeCommit(database.ContextWithTx(ctx, tx), func() error {
			_, aerr := events.NewOutbox().AppendMany(ctx, tx, changes)
			return aerr
		}); err != nil {
			return st, err
		}
	}
	return st, nil
}

// admitManaged checks the specs like the write path: the effective values
// over the layers (validateEffective) and, with a budget gate, the probe
// budget before and after the sync.
func (r *PGRepository) admitManaged(ctx context.Context, tx pgx.Tx, orgID, managedBy, keyPrefix string, next []Probe) error {
	var active []Probe
	var x *layerIndex
	read := func() error {
		var err error
		if r.budget != nil {
			active, err = collectProbes(ctx, tx, `SELECT `+probeColumns+` FROM probe p
				WHERE p.organization_id = $1 AND p.enabled AND p.suspended_reason IS NULL`, orgID)
			if err != nil {
				return err
			}
		}
		x, err = loadLayers(ctx, tx, orgID, append(slices.Clone(active), next...))
		return err
	}
	var err error
	if r.budget != nil && !orgWide(ctx) {
		// The budget counts every probe of the organization (WP-940).
		err = (&pgWriteTx{tx: tx, orgID: orgID}).widened(ctx, read)
	} else {
		err = read()
	}
	if err != nil {
		return fmt.Errorf("read managed probe layers: %w", err)
	}
	txCtx := database.ContextWithTx(ctx, tx)
	if r.budget != nil {
		if x.planMin, err = r.budget.MinProbeIntervalS(txCtx, orgID); err != nil {
			return err
		}
	}
	for i := range next {
		if err := validateEffective(&next[i], x.layers(&next[i])); err != nil {
			return err
		}
	}
	if r.budget == nil {
		return nil
	}
	before := make([]entitlement.EffectiveSpec, 0, len(active))
	after := make([]entitlement.EffectiveSpec, 0, len(active)+len(next))
	for i := range active {
		p := &active[i]
		e := Resolve(p, x.layers(p))
		spec := budgetSpec(p, &e)
		before = append(before, spec)
		if p.ManagedBy != managedBy || !strings.HasPrefix(p.ManagedKey, keyPrefix) {
			after = append(after, spec)
		}
	}
	for i := range next {
		e := Resolve(&next[i], x.layers(&next[i]))
		after = append(after, budgetSpec(&next[i], &e))
	}
	return r.budget.AllowProbeChange(txCtx, orgID, entitlement.BudgetOf(before), entitlement.BudgetOf(after))
}

// ScanProbes adapts SyncManaged to the subnet scan of IPAM (ipam.ScanProbes;
// ipam cannot import this package).
type ScanProbes struct {
	Repo *PGRepository
}

// LockProbeBudget implements ipam.ScanProbes.
func (s ScanProbes) LockProbeBudget(ctx context.Context, tx pgx.Tx, orgID string) error {
	return s.Repo.LockBudget(ctx, tx, orgID)
}

// SyncScanProbes implements ipam.ScanProbes.
func (s ScanProbes) SyncScanProbes(ctx context.Context, tx pgx.Tx, orgID, keyPrefix string, specs []ipam.ScanProbeSpec) (map[string]string, error) {
	var defTimeout int64 = 10
	if t, ok := probewire.Lookup("icmp"); ok {
		defTimeout = t.DefaultTimeoutS
	}
	in := make([]ManagedSpec, len(specs))
	for i, sp := range specs {
		params := json.RawMessage(`{}`)
		switch {
		case sp.IntervalS > 0 && int64(sp.IntervalS) <= defTimeout:
			// The timeout stays below the interval (PRT-02): an interval
			// at or below the default timeout takes the timeout one second
			// shorter.
			params = json.RawMessage(fmt.Sprintf(`{"interval_s":%d,"timeout_s":%d}`, sp.IntervalS, max(sp.IntervalS-1, 1)))
		case sp.IntervalS > 0:
			params = json.RawMessage(fmt.Sprintf(`{"interval_s":%d}`, sp.IntervalS))
		}
		in[i] = ManagedSpec{Key: sp.Key, ClientID: sp.ClientID, CollectorGroupID: sp.CollectorGroupID, Type: "icmp",
			Target: sp.Target, Params: params}
	}
	st, err := s.Repo.SyncManaged(ctx, tx, orgID, ManagedByScan, keyPrefix, in)
	var ve *ValidationError
	if errors.As(err, &ve) && len(ve.Violations) > 0 {
		// The scan knows its interval as scan_interval_s.
		field := ve.Violations[0].Field
		if field == "interval_s" || field == "timeout_s" || strings.HasPrefix(field, "params") {
			field = "scan_interval_s"
		}
		return nil, &api.FieldError{Field: field, Message: ve.Violations[0].Message, Status: http.StatusUnprocessableEntity}
	}
	if err != nil {
		return nil, err
	}
	return st.IDs, nil
}

var _ ipam.ScanProbes = ScanProbes{}
