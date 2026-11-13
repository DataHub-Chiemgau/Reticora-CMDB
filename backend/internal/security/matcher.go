package security

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// The version match CI → findings (SEC-02, LCY-06, WP-1121, E-236 option
// 2): the job vulnfeed.match maps the CIs of an organization to products of
// the vulnerability catalog (cpe_mapping, else the system mapping),
// compares their version with the affected ranges and writes the findings
// of source cve_feed through Upsert; a finding no longer hit on a checked CI
// is resolved (version_fixed), the one of a withdrawn advisory as well
// (advisory_withdrawn). It is queued every 15 minutes per organization after
// a sync of the catalog (vulnfeed.match.schedule) and per CI when a CI is
// created or one of its product fields changes (MatchConsumer).

// MatchJobKind is the job of the version match of an organization.
const MatchJobKind = "vulnfeed.match"

// MatchScheduleKind is the system job that queues the full matches.
const MatchScheduleKind = "vulnfeed.match.schedule"

// MatchJobKindConfig registers MatchJobKind: three attempts of 30 minutes,
// cancellable between two blocks.
var MatchJobKindConfig = jobs.Kind{Name: MatchJobKind, MaxAttempts: 3, Timeout: 30 * time.Minute, Cancellable: true,
	Backoff: jobs.ExponentialBackoff(time.Minute, 10*time.Minute)}

// matchBlock is the number of CIs read per block (keyset on id).
const matchBlock = 1000

// Status reasons of a finding the match resolves.
const (
	ReasonVersionFixed      = "version_fixed"
	ReasonAdvisoryWithdrawn = "advisory_withdrawn"
)

// matchFields are the CI fields whose change queues a match of the CI.
var matchFields = []string{"manufacturer", "model", "sys_object_id", "firmware_version", "os_version"}

var matchDuration = prometheus.NewHistogram(prometheus.HistogramOpts{
	Name:    "reticora_vulnfeed_match_duration_seconds",
	Help:    "Duration of the version match jobs vulnfeed.match.",
	Buckets: []float64{0.1, 0.5, 1, 5, 15, 60, 180, 600, 1800},
})

// RegisterMatchMetrics registers reticora_vulnfeed_match_duration_seconds.
func RegisterMatchMetrics(reg prometheus.Registerer) { reg.MustRegister(matchDuration) }

// MatchParams are the parameters of a match job; no CI ids is the full run.
type MatchParams struct {
	CIIDs []string `json:"ci_ids,omitempty"`
}

// MatchResult is the result of a match job (AK6).
type MatchResult struct {
	CIsChecked          int `json:"cis_checked"`
	CIsUnmapped         int `json:"cis_unmapped"`
	CIsWithoutData      int `json:"cis_without_data"`
	VersionsUnparseable int `json:"versions_unparseable"`
	// BoundsUnparseable counts the ranges of the catalog whose bounds the
	// version scheme of the CI cannot read (no finding).
	BoundsUnparseable int `json:"bounds_unparseable"`
	FindingsCreated   int `json:"findings_created"`
	FindingsResolved  int `json:"findings_resolved"`
}

// Catalog is the vulnerability catalog the match reads
// (vulnfeed.PGRepository).
type Catalog interface {
	ByProducts(ctx context.Context, vendor string, products []string) ([]vulnfeed.Match, error)
}

// MatchJobService runs a match job in this process and reports its
// cancellation (jobs.Service).
type MatchJobService interface {
	RunDetachedWith(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions,
		run func(ctx context.Context) (any, error)) (*jobs.Job, bool, error)
	IsCancelled(ctx context.Context, orgID, id string) (bool, error)
}

// MatchQueue submits a match job to the job queue (jobs.Queue).
type MatchQueue interface {
	Submit(ctx context.Context, orgID, kind string, params any, opts jobs.EnqueueOptions) (*jobs.Job, bool, error)
}

// Matcher is the version match.
type Matcher struct {
	pool     *pgxpool.Pool
	catalog  Catalog
	findings Repository
	svc      MatchJobService
	queue    MatchQueue
	system   []ProductMapping
}

// NewMatcher returns the match over the tenant pool, the catalog and the
// finding repository.
func NewMatcher(pool *pgxpool.Pool, catalog Catalog, findings *PGRepository) *Matcher {
	return &Matcher{pool: pool, catalog: catalog, findings: findings, system: SystemMappings()}
}

// WithJobs runs the jobs in this process (without the queue) and checks
// their cancellation.
func (m *Matcher) WithJobs(svc MatchJobService) *Matcher {
	m.svc = svc
	return m
}

// WithQueue queues the jobs on the job queue.
func (m *Matcher) WithQueue(q MatchQueue) *Matcher {
	m.queue = q
	return m
}

// ErrMatchNeedsDatabase: the match runs on PostgreSQL only.
var ErrMatchNeedsDatabase = errors.New("the version match needs the database")

// ErrMatchUnavailable: neither job service nor queue is wired.
var ErrMatchUnavailable = errors.New("the version match needs the job service")

// Submit queues a match of the organization (ciIDs empty: all CIs) under
// the idempotency key as the system.
func (m *Matcher) Submit(ctx context.Context, orgID string, ciIDs []string, key string) (*jobs.Job, error) {
	params := MatchParams{CIIDs: ciIDs}
	opts := jobs.EnqueueOptions{IdempotencyKey: key}
	if m.queue != nil {
		job, _, err := m.queue.Submit(ctx, orgID, MatchJobKind, params, opts)
		return job, err
	}
	if m.svc == nil {
		return nil, ErrMatchUnavailable
	}
	ids := make(chan string, 1)
	job, _, err := m.svc.RunDetachedWith(ctx, orgID, MatchJobKind, params, opts, func(ctx context.Context) (any, error) {
		return m.Run(systemContext(ctx, orgID), orgID, ciIDs, <-ids)
	})
	if job != nil {
		ids <- job.ID
	} else {
		ids <- ""
	}
	return job, err
}

// systemContext is the organization-wide tenant context of a job of the
// system.
func systemContext(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "")
	return tenant.WithTenant(database.ContextWithTenantScope(ctx, &scope), tenant.TenantInfo{OrganizationID: orgID})
}

// RunJob is the handler of MatchJobKind on the job queue.
func (m *Matcher) RunJob(ctx context.Context, job *jobs.Job) (any, error) {
	var params MatchParams
	if err := json.Unmarshal(job.Params, &params); len(job.Params) > 0 && err != nil {
		return nil, fmt.Errorf("decode %s params: %w", MatchJobKind, err)
	}
	ctx = jobs.CreatorContext(ctx, job)
	if job.CreatedBy == "" {
		ctx = systemContext(ctx, job.OrganizationID)
	}
	return m.Run(ctx, job.OrganizationID, params.CIIDs, job.ID)
}

// errMatchCancelled ends a cancelled match between two blocks.
var errMatchCancelled = errors.New("the version match was cancelled")

// matchCI is a CI with its client and site.
type matchCI struct {
	CIProduct
}

// Run matches the CIs of the organization (ciIDs empty: all) in the tenant
// context of ctx; jobID (empty: none) is checked for cancellation between
// blocks.
func (m *Matcher) Run(ctx context.Context, orgID string, ciIDs []string, jobID string) (*MatchResult, error) {
	if m.pool == nil || m.catalog == nil || m.findings == nil {
		return nil, ErrMatchNeedsDatabase
	}
	start := time.Now()
	defer func() { matchDuration.Observe(time.Since(start).Seconds()) }()
	org, err := m.orgMappings(ctx, orgID)
	if err != nil {
		return nil, err
	}
	res := &MatchResult{}
	ranges := map[string][]vulnfeed.Match{}
	after := ""
	for {
		block, err := m.block(ctx, orgID, after, ciIDs)
		if err != nil {
			return res, err
		}
		if len(block) == 0 {
			break
		}
		after = block[len(block)-1].ID
		for i := range block {
			if merr := m.matchOne(ctx, orgID, org, ranges, &block[i], res); merr != nil {
				return res, merr
			}
		}
		if len(block) < matchBlock {
			break
		}
		if m.svc != nil && jobID != "" {
			if cancelled, cerr := m.svc.IsCancelled(ctx, orgID, jobID); cerr == nil && cancelled {
				return res, errMatchCancelled
			}
		}
	}
	return res, nil
}

// orgMappings reads the product mappings of the organization by priority.
func (m *Matcher) orgMappings(ctx context.Context, orgID string) ([]ProductMapping, error) {
	var out []ProductMapping
	err := database.WithRequestTenant(ctx, m.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, match_kind, COALESCE(manufacturer_pattern, ''), COALESCE(model_pattern, ''),
				COALESCE(sys_object_id_prefix, ''), vendor, products, version_field, version_scheme, priority
			FROM cpe_mapping WHERE organization_id = $1 ORDER BY priority, created_at, id`, orgID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p ProductMapping
			if serr := rows.Scan(&p.ID, &p.MatchKind, &p.ManufacturerPattern, &p.ModelPattern, &p.SysObjectIDPrefix, &p.Vendor,
				&p.Products, &p.VersionField, &p.VersionScheme, &p.Priority); serr != nil {
				return serr
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read the product mappings: %w", err)
	}
	return out, nil
}

// block reads the next CIs after id (keyset), at most matchBlock.
func (m *Matcher) block(ctx context.Context, orgID, after string, ciIDs []string) ([]matchCI, error) {
	var out []matchCI
	err := database.WithRequestTenant(ctx, m.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(manufacturer, ''), COALESCE(model, ''), COALESCE(sys_object_id, ''),
				COALESCE(firmware_version, ''), COALESCE(os_version, '')
			FROM ci WHERE organization_id = $1 AND deleted_at IS NULL AND ($2 = '' OR id > $2::uuid)
			  AND (cardinality($3::uuid[]) = 0 OR id = ANY ($3::uuid[]))
			ORDER BY id LIMIT $4`, orgID, after, validIDs(ciIDs), matchBlock)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var c matchCI
			if serr := rows.Scan(&c.ID, &c.Manufacturer, &c.Model, &c.SysObjectID, &c.FirmwareVersion, &c.OSVersion); serr != nil {
				return serr
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, fmt.Errorf("read the CIs: %w", err)
	}
	return out, nil
}

// validIDs keeps the UUIDs of ids (an id that is no UUID names no CI).
func validIDs(ids []string) []string {
	out := []string{}
	for _, id := range ids {
		if api.IsUUID(id) {
			out = append(out, id)
		}
	}
	if len(ids) > 0 && len(out) == 0 {
		// Only invalid ids: match nothing rather than every CI.
		out = append(out, "00000000-0000-0000-0000-000000000000")
	}
	return out
}

// matchOne matches one CI: findings for the advisories that affect it,
// resolution of those that no longer do.
func (m *Matcher) matchOne(ctx context.Context, orgID string, org []ProductMapping, ranges map[string][]vulnfeed.Match,
	c *matchCI, res *MatchResult) error {
	mapping := ResolveMapping(org, m.system, &c.CIProduct)
	if mapping == nil {
		if strings.TrimSpace(c.Manufacturer) != "" || strings.TrimSpace(c.SysObjectID) != "" {
			res.CIsUnmapped++
		} else {
			res.CIsWithoutData++
		}
		return nil
	}
	version := mapping.Version(&c.CIProduct)
	if version == "" {
		res.CIsWithoutData++
		return nil
	}
	if _, err := CompareVersions(mapping.VersionScheme, version, version); err != nil {
		res.VersionsUnparseable++
		return nil
	}
	key := vulnfeed.Normalize(mapping.Vendor) + "|" + strings.Join(mapping.Products, ",")
	matches, cached := ranges[key]
	if !cached {
		var err error
		if matches, err = m.catalog.ByProducts(ctx, mapping.Vendor, mapping.Products); err != nil {
			return fmt.Errorf("read the affected ranges of %s: %w", mapping.Vendor, err)
		}
		ranges[key] = matches
	}
	hits := map[string]*vulnfeed.Match{}
	withdrawn := map[string]bool{}
	for i := range matches {
		mt := &matches[i]
		if mt.Advisory.Withdrawn {
			withdrawn[mt.Advisory.ID] = true
			continue
		}
		if _, seen := hits[mt.Advisory.ID]; seen {
			continue
		}
		in, err := InRange(mapping.VersionScheme, version, &mt.Affected)
		if err != nil {
			// The version of the CI parsed (above): a bound of the
			// catalog the scheme cannot read. Counted and logged, never
			// dropped silently.
			res.BoundsUnparseable++
			slog.DebugContext(ctx, "vulnfeed match: catalog bound unparseable", "advisory_id", mt.Advisory.ID,
				"scheme", mapping.VersionScheme, "error", err)
			continue
		}
		if !in {
			continue
		}
		hits[mt.Advisory.ID] = mt
	}
	// A CVE that a CSAF advisory reports on the CI yields only the finding
	// of the CSAF advisory, not the one of the NVD advisory (AK4).
	csafCVEs := map[string]bool{}
	for _, h := range hits {
		if strings.HasPrefix(h.Advisory.SourceID, vulnfeed.KindCSAF) {
			for _, cve := range h.Advisory.CVEIDs {
				csafCVEs[cve] = true
			}
		}
	}
	kept := map[string]bool{}
	ids := make([]string, 0, len(hits))
	for id := range hits {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		h := hits[id]
		if h.Advisory.SourceID == vulnfeed.KindNVD && slices.ContainsFunc(h.Advisory.CVEIDs, func(c string) bool { return csafCVEs[c] }) {
			continue
		}
		f := findingOf(orgID, c, mapping, version, h)
		up, err := m.findings.Upsert(ctx, f)
		if err != nil {
			return fmt.Errorf("write the finding of %s: %w", id, err)
		}
		if up.Created {
			res.FindingsCreated++
		}
		kept[id] = true
	}
	res.CIsChecked++
	resolved, err := m.resolveStale(ctx, orgID, c.ID, kept, withdrawn)
	res.FindingsResolved += resolved
	return err
}

// findingOf is the finding of advisory hit h on the CI.
func findingOf(orgID string, c *matchCI, mapping *ProductMapping, version string, h *vulnfeed.Match) *Finding {
	sev := h.Advisory.Severity
	if sev == "" {
		sev = "medium"
	}
	ref := h.Advisory.URL
	if len(h.Advisory.CVEIDs) > 0 {
		ref = h.Advisory.CVEIDs[0]
	}
	title := h.Advisory.Title
	if title == "" {
		title = h.Advisory.ID
	}
	return &Finding{OrganizationID: orgID, CIID: c.ID, Kind: "vulnerability", Severity: sev, Title: title,
		PackageName: h.Affected.Vendor + "/" + h.Affected.Product, InstalledVersion: version, FixedVersion: h.Affected.FixedVersion,
		Reference: ref, Source: SourceCVEFeed, CVEIDs: slices.Clone(h.Advisory.CVEIDs), CVSSScore: h.Advisory.CVSSScore,
		CVSSVector: h.Advisory.CVSSVector, KEV: h.Advisory.KEV, AdvisoryID: h.Advisory.ID, Status: StatusOpen}
}

// resolveStale resolves the open or acknowledged cve_feed findings of the
// CI whose advisory the run did not keep: version_fixed, or
// advisory_withdrawn for a withdrawn advisory.
func (m *Matcher) resolveStale(ctx context.Context, orgID, ciID string, kept, withdrawn map[string]bool) (int, error) {
	type stale struct{ id, advisory string }
	var open []stale
	err := database.WithRequestTenant(ctx, m.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text, COALESCE(advisory_id, '') FROM security_finding
			WHERE organization_id = $1 AND ci_id = $2::uuid AND source = 'cve_feed' AND status IN ('open', 'acknowledged')`, orgID, ciID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s stale
			if serr := rows.Scan(&s.id, &s.advisory); serr != nil {
				return serr
			}
			open = append(open, s)
		}
		return rows.Err()
	})
	if err != nil {
		return 0, fmt.Errorf("read the findings of the CI: %w", err)
	}
	resolved := 0
	status := StatusResolved
	for _, s := range open {
		if kept[s.advisory] {
			continue
		}
		reason := ReasonVersionFixed
		if withdrawn[s.advisory] {
			reason = ReasonAdvisoryWithdrawn
		}
		if _, uerr := m.findings.Update(ctx, orgID, s.id, UpdateFindingRequest{Status: &status, StatusReason: &reason}); uerr != nil {
			// Resolved, marked false positive or deleted since the read
			// (a concurrent run or a user): already handled, not the end
			// of the job.
			var fe *api.FieldError
			if errors.Is(uerr, ErrNotFound) || (errors.As(uerr, &fe) && fe.Field == "status") {
				continue
			}
			return resolved, fmt.Errorf("resolve finding %s: %w", s.id, uerr)
		}
		resolved++
	}
	return resolved, nil
}

// CatalogSources lists the sources of the catalog with their sync state
// (vulnfeed.PGRepository).
type CatalogSources interface {
	ListSources(ctx context.Context) ([]vulnfeed.Source, error)
}

// MatchSchedule is the system job vulnfeed.match.schedule (AK5).
type MatchSchedule struct {
	matcher *Matcher
	sources CatalogSources
	orgs    func(ctx context.Context) ([]string, error)
	enabled func(ctx context.Context, orgID string) bool
}

// NewMatchSchedule returns the schedule over the organizations orgs lists
// that enabled admits (module findings, feature compliance_findings).
func NewMatchSchedule(m *Matcher, sources CatalogSources, orgs func(ctx context.Context) ([]string, error),
	enabled func(ctx context.Context, orgID string) bool) *MatchSchedule {
	return &MatchSchedule{matcher: m, sources: sources, orgs: orgs, enabled: enabled}
}

// Schedule is the registration with the scheduler: every 15 minutes.
func (s *MatchSchedule) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: MatchScheduleKind, Spec: "@every 15m", MaxCatchUp: 1, Timeout: 10 * time.Minute,
		Run: func(ctx context.Context) error {
			_, err := s.Run(ctx)
			return err
		}}
}

// Run queues a full match per enabled organization for the newest sync of
// the catalog (max(vuln_source.synced_at)); the idempotency key
// vulnfeed.match:full:<org>:<synced_at> admits one per sync. It returns the
// number of organizations it submitted for.
func (s *MatchSchedule) Run(ctx context.Context) (int, error) {
	var synced *time.Time
	sources, err := s.sources.ListSources(ctx)
	if err != nil {
		return 0, fmt.Errorf("%s: read the catalog state: %w", MatchScheduleKind, err)
	}
	for i := range sources {
		if at := sources[i].SyncedAt; at != nil && (synced == nil || at.After(*synced)) {
			synced = at
		}
	}
	if synced == nil {
		return 0, nil
	}
	orgs, oerr := s.orgs(ctx)
	if oerr != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", MatchScheduleKind, oerr)
	}
	queued := 0
	var errs []error
	for _, orgID := range orgs {
		octx := systemContext(ctx, orgID)
		if s.enabled == nil || !s.enabled(octx, orgID) {
			continue
		}
		key := "vulnfeed.match:full:" + orgID + ":" + synced.UTC().Format(time.RFC3339Nano)
		if _, serr := s.matcher.Submit(octx, orgID, nil, key); serr != nil {
			errs = append(errs, fmt.Errorf("%s: organization %s: %w", MatchScheduleKind, orgID, serr))
			continue
		}
		queued++
	}
	return queued, errors.Join(errs...)
}

// MatchConsumer queues the match of a CI that was created or whose product
// fields changed (AK5); it fails only when the job cannot be queued.
type MatchConsumer struct {
	matcher *Matcher
	enabled func(ctx context.Context, orgID string) bool
}

// NewMatchConsumer returns the consumer of the event publisher.
func NewMatchConsumer(m *Matcher, enabled func(ctx context.Context, orgID string) bool) *MatchConsumer {
	return &MatchConsumer{matcher: m, enabled: enabled}
}

// Consume implements events.Consumer.
func (c *MatchConsumer) Consume(ctx context.Context, env *events.Envelope) error {
	ciID := matchedCI(env)
	if ciID == "" {
		return nil
	}
	octx := systemContext(ctx, env.OrgID)
	if c.enabled != nil && !c.enabled(octx, env.OrgID) {
		return nil
	}
	if _, err := c.matcher.Submit(octx, env.OrgID, []string{ciID}, "vulnfeed.match:"+env.ID); err != nil {
		return fmt.Errorf("queue the version match of CI %s: %w", ciID, err)
	}
	slog.DebugContext(ctx, "version match queued", "ci_id", ciID, "event", env.ID)
	return nil
}

// matchedCI is the CI of a ci.created, or of a ci.changed with a product
// field among changed_fields; empty otherwise.
func matchedCI(env *events.Envelope) string {
	switch env.Type {
	case events.CICreated:
	case events.CIChanged:
	default:
		return ""
	}
	var data struct {
		ID            string   `json:"id"`
		ChangedFields []string `json:"changed_fields"`
	}
	if err := json.Unmarshal(env.Data, &data); err != nil || !api.IsUUID(data.ID) {
		return ""
	}
	if env.Type == events.CIChanged && !slices.ContainsFunc(data.ChangedFields, func(f string) bool { return slices.Contains(matchFields, f) }) {
		return ""
	}
	return data.ID
}
