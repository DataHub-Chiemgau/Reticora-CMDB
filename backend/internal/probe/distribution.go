package probe

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/probewire"
)

// Distribution of the assignment set to the collector (PRB-11, PRB-01,
// WP-948, E-173): a collector asks on collector.<org>.<id>.probes.request
// (probewire.AssignmentRequest) and gets one page of its versioned set
// (probewire.AssignmentSet): every probe assigned to it (probe_assignment)
// and enabled, with its effective values (Resolve over the levels, read in
// one batch, WP-937), its epoch and the ids of its credentials. The version
// is collector_group_member.config_version of the collector: every change
// of its assignments (WP-941), of a probe (WP-939), of a profile or a group
// (WP-946) and of a credential binding of an assigned probe (trigger
// credential_binding_probe_version, migration 000346) raises it. A
// suspended probe (WP-942) has lost its assignment and is not in the set. A request with if_none_match equal to the version
// gets {"not_modified": true, "version": n}. A page holds at most
// probewire.MaxPageProbes probes and probewire.MaxPageBytes; a request for
// a later page of an outdated version gets version_changed, and the
// collector starts again with page 1. The pages of a version are kept for
// distributionCacheTTL, so a page after the first costs one statement.
//
// A probe carries only credential ids (its bindings of target kind probe,
// in the order of their priority), never a secret; an id missing from the
// collector's current credential bundle (BundleCredentials, in production
// discovery.BundleGate.CredentialIDs, the ids VerifyBundleTarget puts into
// the bundle, the probe bindings of the collector included) takes the
// probe out of the set and reports it in skipped with credential_missing.
//
// Before reading anything the request is refused like the other edge
// channels (discovery.CheckCollectorChannel): unknown 404, disabled or
// paused 403; an organization without the feature monitoring gets 403 with
// the problem type of the entitlement middleware. A failed lookup refuses
// as well (fail-closed) and counts as a failed cycle.
//
// After a commit that raised versions the NATSAssignmentNotifier sends
// collector.<org>.<id>.probes.changed with {"version": n}.

// distributionCacheTTL bounds how long the pages of one version are kept.
const distributionCacheTTL = time.Minute

// CollectorCheck refuses a collector before its channel reads
// (discovery.Handler.CheckCollectorChannel).
type CollectorCheck func(ctx context.Context, orgID, collectorID string) error

// BundleCredentials returns the credential ids of the collector's current
// credential bundle (credential.BundleTarget.CredentialIDs); ctx carries
// the transaction of the set, which the source may join.
type BundleCredentials func(ctx context.Context, orgID, collectorID string) ([]string, error)

// Distributor answers the assignment set requests of the collectors.
type Distributor struct {
	repo     *PGRepository
	check    CollectorCheck
	features FeatureStatus
	bundle   BundleCredentials
	cycles   *observability.Cycles
	now      func() time.Time

	mu    sync.Mutex
	cache map[[2]string]*cachedSet
}

// cachedSet are the pages of one version of a collector's set.
type cachedSet struct {
	version int64
	pages   []probewire.AssignmentSet
	at      time.Time
}

// NewDistributor returns the distribution on the probe repository; check
// refuses unknown, disabled and paused collectors.
func NewDistributor(repo *PGRepository, check CollectorCheck) *Distributor {
	return &Distributor{repo: repo, check: check, now: time.Now, cache: map[[2]string]*cachedSet{}}
}

// WithFeatures checks the feature monitoring (nil: every organization).
func (d *Distributor) WithFeatures(f FeatureStatus) *Distributor {
	d.features = f
	return d
}

// WithBundle sets the source of the bundle credentials; without one every
// probe with a credential is skipped (credential_missing).
func (d *Distributor) WithBundle(b BundleCredentials) *Distributor {
	d.bundle = b
	return d
}

// WithCycles counts failed lookups as failed cycles of the worker.
func (d *Distributor) WithCycles(c *observability.Cycles) *Distributor {
	d.cycles = c
	return d
}

func (d *Distributor) fail() {
	if d.cycles != nil {
		d.cycles.Fail()
	}
}

// ServeProbeRequests answers collector.*.*.probes.request until ctx ends.
func (d *Distributor) ServeProbeRequests(ctx context.Context, nc *nats.Conn) error {
	sub, err := nc.QueueSubscribe("collector.*.*"+discovery.ProbesRequestSuffix, "reticora-probe-distribution", func(msg *nats.Msg) {
		var resp any = Reply{Status: http.StatusBadRequest, Error: "invalid subject"}
		if orgID, collectorID, ok := probeSubject(msg.Subject, discovery.ProbesRequestSuffix); ok {
			resp = d.Answer(ctx, orgID, collectorID, msg.Data)
		}
		raw, merr := json.Marshal(resp)
		if merr != nil {
			raw = []byte(`{"status":500,"error":"encode failed"}`)
		}
		if msg.Reply != "" {
			if rerr := msg.Respond(raw); rerr != nil {
				d.fail()
				slog.Error("probe distribution reply", "error", rerr)
			}
		}
	})
	if err != nil {
		return fmt.Errorf("subscribe %s: %w", discovery.ProbesRequestSuffix, err)
	}
	<-ctx.Done()
	_ = sub.Unsubscribe()
	return nil
}

// probeSubject reads organization and collector of collector.<org>.<id><suffix>.
func probeSubject(subject, suffix string) (orgID, collectorID string, ok bool) {
	parts := strings.Split(strings.TrimSuffix(subject, suffix), ".")
	if len(parts) != 3 || parts[0] != "collector" || !api.IsUUID(parts[1]) || !api.IsUUID(parts[2]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// refusal answers a request the collector check refused.
func (d *Distributor) refusal(err error) Reply {
	switch {
	case errors.Is(err, api.ErrNotFound):
		return Reply{Status: http.StatusNotFound, Error: "collector not found"}
	case errors.Is(err, discovery.ErrCollectorDisabled), errors.Is(err, discovery.ErrCollectorPaused):
		return Reply{Status: http.StatusForbidden, Error: err.Error()}
	}
	d.fail()
	return Reply{Status: http.StatusInternalServerError, Error: "collector lookup failed"}
}

// Answer answers one request of the collector.
func (d *Distributor) Answer(ctx context.Context, orgID, collectorID string, data []byte) any {
	var req probewire.AssignmentRequest
	if strings.TrimSpace(string(data)) != "" {
		if err := json.Unmarshal(data, &req); err != nil {
			return Reply{Status: http.StatusBadRequest, Error: "invalid request"}
		}
	}
	if req.Page == 0 {
		req.Page = 1
	}
	if req.Page < 1 {
		return Reply{Status: http.StatusBadRequest, Error: "page must be at least 1"}
	}
	if d.check == nil {
		d.fail()
		return Reply{Status: http.StatusInternalServerError, Error: "collector lookup failed"}
	}
	scope := database.OrgWideScope(orgID, "")
	sctx := database.ContextWithTenantScope(ctx, &scope)
	if err := d.check(sctx, orgID, collectorID); err != nil {
		return d.refusal(err)
	}
	if d.features != nil {
		_, status, err := d.features.Status(sctx, orgID, entitlement.FeatureMonitoring)
		if err != nil {
			d.fail()
			return Reply{Status: http.StatusInternalServerError, Error: "entitlement lookup failed"}
		}
		if status != entitlement.StatusActive {
			return Reply{Status: http.StatusForbidden, Type: api.ProblemFeatureNotEntitled, Error: "monitoring is not licensed"}
		}
	}
	set, err := d.page(sctx, orgID, collectorID, &req)
	if err != nil {
		d.fail()
		slog.Error("probe distribution", "organization_id", orgID, "collector_id", collectorID, "error", err)
		return Reply{Status: http.StatusInternalServerError, Error: "assignment set unavailable"}
	}
	return set
}

// page returns the page the request asks for, NotModified or a
// version_changed page.
func (d *Distributor) page(ctx context.Context, orgID, collectorID string, req *probewire.AssignmentRequest) (any, error) {
	var (
		out     any
		version int64
	)
	scope := database.OrgWideScope(orgID, "")
	err := database.WithTenant(ctx, d.repo.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT config_version FROM collector_group_member WHERE organization_id = $1 AND collector_id = $2`,
			orgID, collectorID).Scan(&version)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("read config version: %w", err)
		}
		out, err = respond(collectorID, version, req, func() ([]probewire.AssignmentSet, error) {
			if pages := d.cached(orgID, collectorID, version); pages != nil {
				return pages, nil
			}
			pages, berr := d.build(ctx, tx, orgID, collectorID, version)
			if berr == nil {
				d.store(orgID, collectorID, version, pages)
			}
			return pages, berr
		})
		return err
	})
	return out, err
}

// respond answers a request at the current version: NotModified for
// if_none_match equal to it, version_changed for a later page of another
// version, else the page of pages (built only then).
func respond(collectorID string, version int64, req *probewire.AssignmentRequest,
	pages func() ([]probewire.AssignmentSet, error)) (any, error) {
	if req.IfNoneMatch != 0 && req.IfNoneMatch == version {
		return probewire.NotModified{NotModified: true, Version: version}, nil
	}
	if req.Page > 1 && req.Version != version {
		return probewire.AssignmentSet{Header: probewire.Header{SchemaVersion: probewire.SchemaVersion}, CollectorID: collectorID,
			Version: version, Page: req.Page, NextPage: 1, VersionChanged: true, Probes: []probewire.ProbeSpec{}}, nil
	}
	all, err := pages()
	if err != nil {
		return nil, err
	}
	set, ok := probewire.PageOf(all, req.Version, req.Page)
	if !ok {
		return Reply{Status: http.StatusNotFound, Error: "page not found"}, nil
	}
	return set, nil
}

func (d *Distributor) cached(orgID, collectorID string, version int64) []probewire.AssignmentSet {
	d.mu.Lock()
	defer d.mu.Unlock()
	c, ok := d.cache[[2]string{orgID, collectorID}]
	if !ok || c.version != version || d.now().Sub(c.at) > distributionCacheTTL {
		return nil
	}
	return c.pages
}

func (d *Distributor) store(orgID, collectorID string, version int64, pages []probewire.AssignmentSet) {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	for k, c := range d.cache {
		if now.Sub(c.at) > distributionCacheTTL {
			delete(d.cache, k)
		}
	}
	d.cache[[2]string{orgID, collectorID}] = &cachedSet{version: version, pages: pages, at: now}
}

// build reads the assigned enabled probes with their credential ids, their
// levels and the plan minimum and paginates the set: three statements (the
// probes, loadLayers) with the plan minimum from the entitlement cache
// (ENT-03). A credential bound to the probe is in the collector's bundle
// when it meets discovery.ProbeCredentialInBundle (the bundle holds every
// such credential of its assigned probes); only for another one the bundle
// itself is read (BundleCredentials), in the same transaction.
func (d *Distributor) build(ctx context.Context, tx pgx.Tx, orgID, collectorID string, version int64) ([]probewire.AssignmentSet, error) {
	rows, err := tx.Query(ctx, `SELECT `+probeColumns+`, a.epoch,
			COALESCE((SELECT array_agg(b.credential_id::text ORDER BY b.priority, b.credential_id) FROM credential_binding b
				WHERE b.organization_id = p.organization_id AND b.probe_id = p.id), '{}'),
			COALESCE((SELECT array_agg(b.credential_id::text) FROM credential_binding b
				LEFT JOIN credential c ON c.organization_id = b.organization_id AND c.id = b.credential_id
				WHERE b.organization_id = p.organization_id AND b.probe_id = p.id
				  AND NOT COALESCE(`+discovery.ProbeCredentialInBundle+`, false)), '{}')
		FROM probe_assignment a
		JOIN probe p ON p.organization_id = a.organization_id AND p.id = a.probe_id
		WHERE a.organization_id = $1 AND a.collector_id = $2 AND p.enabled
		ORDER BY p.id`, orgID, collectorID)
	if err != nil {
		return nil, fmt.Errorf("read assigned probes: %w", err)
	}
	probes := make([]Probe, 0)
	epochs := map[string]int64{}
	creds := map[string][]string{}
	var outside []string
	for rows.Next() {
		var p Probe
		var epoch int64
		var ids, notInBundle []string
		if scanErr := rows.Scan(probeDest(&p, &epoch, &ids, &notInBundle)...); scanErr != nil {
			rows.Close()
			return nil, fmt.Errorf("scan assigned probe: %w", scanErr)
		}
		probes = append(probes, p)
		epochs[p.ID] = epoch
		for _, id := range ids {
			if !slices.Contains(creds[p.ID], id) {
				creds[p.ID] = append(creds[p.ID], id)
			}
		}
		outside = append(outside, notInBundle...)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("read assigned probes: %w", err)
	}
	x, err := loadLayers(ctx, tx, orgID, probes)
	if err != nil {
		return nil, err
	}
	if x.planMin, err = d.repo.planMin(database.ContextWithTx(ctx, tx), orgID); err != nil {
		return nil, err
	}
	missing := map[string]bool{}
	if len(outside) > 0 {
		var bundle []string
		if d.bundle != nil {
			if bundle, err = d.bundle(database.ContextWithTx(ctx, tx), orgID, collectorID); err != nil {
				return nil, fmt.Errorf("read bundle credentials: %w", err)
			}
		}
		for _, id := range outside {
			if !slices.Contains(bundle, id) {
				missing[id] = true
			}
		}
	}
	specs, skipped := assignmentSpecs(x.resolveAll(probes, epochs), creds, missing)
	pages, err := probewire.Paginate(collectorID, version, specs)
	if err != nil {
		return nil, err
	}
	if len(skipped) > 0 {
		pages[0].Skipped = skipped
	}
	return pages, nil
}

// assignmentSpecs turns the resolved probes into the specifications of the
// set with their credential ids; a probe with an id in missing (not in the
// collector's bundle) is skipped (credential_missing).
func assignmentSpecs(resolved []Resolved, creds map[string][]string, missing map[string]bool) ([]probewire.ProbeSpec, []probewire.SkippedProbe) {
	specs := make([]probewire.ProbeSpec, 0, len(resolved))
	var skipped []probewire.SkippedProbe
	for i := range resolved {
		r := &resolved[i]
		ids := creds[r.Probe.ID]
		if slices.ContainsFunc(ids, func(id string) bool { return missing[id] }) {
			skipped = append(skipped, probewire.SkippedProbe{ProbeID: r.Probe.ID, Reason: probewire.SkipCredentialMissing})
			continue
		}
		spec := r.Effective.ProbeSpec(r.Epoch)
		spec.CredentialIDs = ids
		specs = append(specs, spec)
	}
	return specs, skipped
}

// NATSAssignmentNotifier is the AssignmentNotifier of WP-948: after the
// commit it sends every changed collector collector.<org>.<id>.probes.changed
// with its new version. A collector that misses it learns the version on
// its next request (if_none_match).
type NATSAssignmentNotifier struct {
	nc *nats.Conn
}

// NewNATSAssignmentNotifier returns the notifier on nc.
func NewNATSAssignmentNotifier(nc *nats.Conn) *NATSAssignmentNotifier {
	return &NATSAssignmentNotifier{nc: nc}
}

// AssignmentsChanged implements AssignmentNotifier.
func (n *NATSAssignmentNotifier) AssignmentsChanged(_ context.Context, orgID string, collectors []CollectorVersion) {
	for _, c := range collectors {
		raw, err := json.Marshal(probewire.ProbesChanged{Version: c.ConfigVersion})
		if err == nil {
			err = n.nc.Publish("collector."+orgID+"."+c.CollectorID+discovery.ProbesChangedSuffix, raw)
		}
		if err != nil {
			slog.Warn("probes changed notice failed", "organization_id", orgID, "collector_id", c.CollectorID, "error", err)
		}
	}
}
