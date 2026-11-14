package compliance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The report generator (SEC-04, WP-1129, E-239 option 2): POST
// /api/v1/compliance/reports queues the job compliance.report, which
// collects the report of a standard for the organization or one client
// (under that client's scope), renders it as PDF (report_pdf.go) or CSV
// (report_csv.go) and stores it as a document of the category
// compliance_report, linked to the organization or the client, with its
// content in the object storage pending the malware scan (E-42). The job
// result names the document; the audit entry compliance.report.generated
// records standard, scope and document.

// ReportJobKind is the job of one report.
const ReportJobKind = "compliance.report"

// ReportJobKindConfig registers ReportJobKind: three attempts of 30
// minutes, cancellable between two sections.
var ReportJobKindConfig = jobs.Kind{Name: ReportJobKind, MaxAttempts: 3, Timeout: 30 * time.Minute, Cancellable: true,
	Backoff: jobs.ExponentialBackoff(time.Minute, 10*time.Minute)}

// CategoryComplianceReport is the document category of a report.
const CategoryComplianceReport = document.CategoryComplianceReport

// ActionReportGenerated is the audit action of a stored report.
const ActionReportGenerated = "compliance.report.generated"

// Formats and standards of a report.
const (
	FormatPDF = "pdf"
	FormatCSV = "csv"
)

// ReportStandards are the standards of the control catalog (WP-1128).
var ReportStandards = []string{"iso27001", "nis2", "kritis"}

// reportBlock is the number of results read per query (keyset).
const reportBlock = 500

// ReportParams are the parameters of a report job.
type ReportParams struct {
	Standard string `json:"standard"`
	Format   string `json:"format"`
	ClientID string `json:"client_id,omitempty"`
}

// ReportJobResult is the result of a report job.
type ReportJobResult struct {
	DocumentID string `json:"document_id"`
	Standard   string `json:"standard"`
	Format     string `json:"format"`
	Scope      string `json:"scope"`
	ClientID   string `json:"client_id,omitempty"`
	Size       int64  `json:"size"`
}

// Errors of the report API.
var (
	// ErrReportsUnavailable: the generator needs the database and the
	// object storage (503).
	ErrReportsUnavailable = errors.New("the compliance reports need the database and the object storage")
	// ErrReportClientNotFound: the client of the report is unknown or not
	// visible (404).
	ErrReportClientNotFound = errors.New("client not found")
	errReportCancelled      = errors.New("the compliance report was cancelled")
)

// ─── data ─────────────────────────────────────────────────────────────────────

// ReportResultRow is one result of a rule on a CI with names.
type ReportResultRow struct {
	RuleID          string
	RuleName        string
	Severity        string
	Category        string
	RemediationHint string
	CIID            string
	CIName          string
	Status          string
	EvaluatedAt     time.Time
}

// ReportControl is one control of the standard with its assigned rules.
type ReportControl struct {
	ControlID string
	Title     string
	Rules     []ReportControlRule
}

// ReportControlRule is a rule assigned to a control.
type ReportControlRule struct {
	RuleID   string
	RuleName string
	Severity string
	Active   bool
}

// ReportFindings are the open findings of one severity.
type ReportFindings struct {
	Severity string
	Open     int
	KEV      int
}

// ReportRule is a failing rule with the CIs it fails on.
type ReportRule struct {
	RuleID          string
	RuleName        string
	Severity        string
	Category        string
	RemediationHint string
	CIs             []ReportCI
}

// ReportCI names a CI of a failing rule.
type ReportCI struct {
	ID   string
	Name string
}

// ReportData is the content of a report in its order (AK2).
type ReportData struct {
	OrganizationName string
	ClientID         string
	ClientName       string
	Standard         string
	GeneratedAt      time.Time
	AuditIntegrity   *AuditIntegrity
	Score            ScoreGroup
	Trend            []HistoryPoint
	Controls         []ReportControl
	Failing          []ReportRule
	Findings         []ReportFindings
	Capabilities     []EntitlementStatus
	// Results are the results of the rules of the controls (every status),
	// the rows of the CSV.
	Results map[string][]ReportResultRow
}

// reportReader reads the inputs of a report under the scope of ctx (the
// PostgreSQL reader; tests use a fake).
type reportReader interface {
	names(ctx context.Context, orgID, clientID string) (org, client string, err error)
	controls(ctx context.Context, orgID, standard string) ([]ReportControl, error)
	// results returns up to limit results after the keyset (rule, CI) of
	// the rules ruleIDs (nil: every rule) with status (empty: every); with
	// clientID only those of CIs of that client.
	results(ctx context.Context, orgID, clientID string, ruleIDs []string, status, afterRule, afterCI string, limit int) ([]ReportResultRow, error)
	// openFindings counts the open findings, with clientID only those of
	// that client.
	openFindings(ctx context.Context, orgID, clientID string) ([]ReportFindings, error)
}

// ─── generator ────────────────────────────────────────────────────────────────

// ReportGenerator collects, renders and stores the reports.
type ReportGenerator struct {
	pool         *pgxpool.Pool
	scores       ScoreStore
	reader       reportReader
	audit        AuditVerifier
	entitlements EntitlementLister
	blobs        blob.Store
	scans        document.ScanSubmitter
	// limits is the storage limit of the organization (max_storage_mb,
	// E-239: a report counts against it); nil is unlimited.
	limits document.StorageLimiter
	now    func() time.Time
}

// NewReportGenerator returns the generator on the application pool with the
// score store, the audit verifier, the entitlements and the object storage
// (audit and entitlements may be nil).
func NewReportGenerator(pool *pgxpool.Pool, scores ScoreStore, auditVerifier AuditVerifier, entitlements EntitlementLister, blobs blob.Store) *ReportGenerator {
	g := &ReportGenerator{pool: pool, scores: scores, audit: auditVerifier, entitlements: entitlements, blobs: blobs, now: time.Now}
	if pool != nil {
		g.reader = &pgReportReader{pool: pool}
	}
	return g
}

// WithScans submits the malware scan of each stored report (E-42); without
// it the version stays pending_scan for the scan sweep.
func (g *ReportGenerator) WithScans(scans document.ScanSubmitter) *ReportGenerator {
	g.scans = scans
	return g
}

// WithStorageLimit enforces the document storage limit of the organization
// on each stored report (E-239); a report beyond it fails the job with the
// limit error and leaves no object behind.
func (g *ReportGenerator) WithStorageLimit(limits document.StorageLimiter) *ReportGenerator {
	g.limits = limits
	return g
}

func (g *ReportGenerator) available() bool {
	return g != nil && g.pool != nil && g.reader != nil && g.blobs != nil
}

// reportContext is the context the report reads in: the scope of ctx, for
// a client report narrowed to that client (the creator saw it when the job
// was submitted; a scope that lost it reads nothing of it). The policies
// show a client-scoped principal also the rows without client (org-wide
// CIs, their results and findings, the org-wide snapshot rows), so the
// readers of a client report filter on the client explicitly (AK2).
func reportContext(ctx context.Context, orgID, clientID string) (context.Context, error) {
	scope, ok := database.TenantScopeFromContext(ctx)
	if !ok {
		scope = database.OrgWideScope(orgID, tenant.FromContext(ctx).UserID)
	}
	if clientID == "" {
		return ctx, nil
	}
	if !scope.Clients.IsAll() && !slices.Contains(scope.Clients.IDs(), clientID) {
		return nil, ErrReportClientNotFound
	}
	scope.Clients = database.ScopeIDs(clientID)
	return database.ContextWithTenantScope(ctx, &scope), nil
}

// Collect gathers the content of a report in the scope of ctx (AK2).
func (g *ReportGenerator) Collect(ctx context.Context, orgID string, p *ReportParams) (*ReportData, error) {
	ctx, err := reportContext(ctx, orgID, p.ClientID)
	if err != nil {
		return nil, err
	}
	d := &ReportData{Standard: p.Standard, ClientID: p.ClientID, GeneratedAt: g.now().UTC(), Results: map[string][]ReportResultRow{}}
	if d.OrganizationName, d.ClientName, err = g.reader.names(ctx, orgID, p.ClientID); err != nil {
		return nil, err
	}
	if g.audit != nil {
		integrity, verr := g.audit.Verify(ctx, orgID)
		if verr != nil {
			return nil, fmt.Errorf("verify audit chain: %w", verr)
		}
		d.AuditIntegrity = &integrity
	}
	if g.scores != nil {
		if serr := g.collectScore(ctx, orgID, p.ClientID, d); serr != nil {
			return nil, serr
		}
	}
	if d.Controls, err = g.reader.controls(ctx, orgID, p.Standard); err != nil {
		return nil, fmt.Errorf("read the controls: %w", err)
	}
	failing, err := g.readAll(ctx, orgID, p.ClientID, nil, "fail")
	if err != nil {
		return nil, err
	}
	d.Failing = groupFailing(failing)
	var ruleIDs []string
	for i := range d.Controls {
		for _, r := range d.Controls[i].Rules {
			if !slices.Contains(ruleIDs, r.RuleID) {
				ruleIDs = append(ruleIDs, r.RuleID)
			}
		}
	}
	if len(ruleIDs) > 0 {
		all, rerr := g.readAll(ctx, orgID, p.ClientID, ruleIDs, "")
		if rerr != nil {
			return nil, rerr
		}
		for i := range all {
			d.Results[all[i].RuleID] = append(d.Results[all[i].RuleID], all[i])
		}
	}
	if d.Findings, err = g.reader.openFindings(ctx, orgID, p.ClientID); err != nil {
		return nil, fmt.Errorf("read the open findings: %w", err)
	}
	if g.entitlements != nil {
		if d.Capabilities, err = g.entitlements.List(ctx, orgID); err != nil {
			return nil, fmt.Errorf("list entitlements: %w", err)
		}
	}
	return d, nil
}

// collectScore puts the score and the trend over SnapshotRetention days
// into d: of the organization, or of the client of a client report only
// (its group of group_by client; the rows without client stay out).
func (g *ReportGenerator) collectScore(ctx context.Context, orgID, clientID string, d *ReportData) error {
	q, hq := &ScoreQuery{}, GroupOverall
	if clientID != "" {
		q.GroupBy, hq = GroupClient, GroupClient
	}
	page, err := g.scores.Score(ctx, orgID, q)
	if err != nil {
		return fmt.Errorf("read the score: %w", err)
	}
	for i := range page.Groups {
		if clientID == "" || page.Groups[i].Key == clientID {
			d.Score = page.Groups[i]
			break
		}
	}
	to := today()
	trend, err := g.scores.ScoreHistory(ctx, orgID, &HistoryQuery{GroupBy: hq, From: to.AddDate(0, 0, -(SnapshotRetention - 1)), To: to})
	switch {
	case errors.Is(err, ErrScoreHistoryUnavailable):
		return nil
	case err != nil:
		return fmt.Errorf("read the score history: %w", err)
	}
	for _, point := range trend {
		if clientID == "" || point.Key == clientID {
			d.Trend = append(d.Trend, point)
		}
	}
	return nil
}

// readAll reads every result of the rules with status by keyset blocks of
// reportBlock: no upper bound (AK2).
func (g *ReportGenerator) readAll(ctx context.Context, orgID, clientID string, ruleIDs []string, status string) ([]ReportResultRow, error) {
	var out []ReportResultRow
	afterRule, afterCI := "", ""
	for {
		block, err := g.reader.results(ctx, orgID, clientID, ruleIDs, status, afterRule, afterCI, reportBlock)
		if err != nil {
			return nil, fmt.Errorf("read the compliance results: %w", err)
		}
		out = append(out, block...)
		if len(block) < reportBlock {
			return out, nil
		}
		last := block[len(block)-1]
		afterRule, afterCI = last.RuleID, last.CIID
	}
}

// groupFailing groups the failing results per rule, most severe first.
func groupFailing(rows []ReportResultRow) []ReportRule {
	byRule := map[string]*ReportRule{}
	var order []string
	for i := range rows {
		r := &rows[i]
		rule, ok := byRule[r.RuleID]
		if !ok {
			rule = &ReportRule{RuleID: r.RuleID, RuleName: r.RuleName, Severity: r.Severity, Category: r.Category, RemediationHint: r.RemediationHint}
			byRule[r.RuleID] = rule
			order = append(order, r.RuleID)
		}
		rule.CIs = append(rule.CIs, ReportCI{ID: r.CIID, Name: r.CIName})
	}
	out := make([]ReportRule, 0, len(order))
	for _, id := range order {
		out = append(out, *byRule[id])
	}
	slices.SortStableFunc(out, func(a, b ReportRule) int {
		if c := severityRank(a.Severity) - severityRank(b.Severity); c != 0 {
			return c
		}
		return strings.Compare(a.RuleName, b.RuleName)
	})
	return out
}

// Render renders the report in its format.
func Render(d *ReportData, format string) ([]byte, error) {
	switch format {
	case FormatPDF:
		return renderPDF(d, true)
	case FormatCSV:
		return renderCSV(d)
	}
	return nil, fmt.Errorf("unknown report format %q", format)
}

// Generate collects, renders and stores one report as the job jobID (empty:
// none) and returns its result.
func (g *ReportGenerator) Generate(ctx context.Context, orgID, jobID string, p *ReportParams, cancelled func() bool) (*ReportJobResult, error) {
	if !g.available() {
		return nil, ErrReportsUnavailable
	}
	data, err := g.Collect(ctx, orgID, p)
	if err != nil {
		return nil, err
	}
	if cancelled != nil && cancelled() {
		return nil, errReportCancelled
	}
	content, err := Render(data, p.Format)
	if err != nil {
		return nil, err
	}
	if cancelled != nil && cancelled() {
		return nil, errReportCancelled
	}
	rctx, err := reportContext(ctx, orgID, p.ClientID)
	if err != nil {
		return nil, err
	}
	return g.store(rctx, orgID, jobID, p, data, content)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// store puts the content into the object storage and, in one transaction,
// the document of category compliance_report with version 1, its link to
// the organization or the client and the audit entry (AK4); the malware
// scan is submitted after the commit. A failed transaction removes the
// object again.
func (g *ReportGenerator) store(ctx context.Context, orgID, jobID string, p *ReportParams, data *ReportData, content []byte) (*ReportJobResult, error) {
	mime, ext := "application/pdf", "pdf"
	if p.Format == FormatCSV {
		mime, ext = "text/csv", "csv"
	}
	scope, linkType, linkID := "organization", "organization", orgID
	if p.ClientID != "" {
		scope, linkType, linkID = "client", "client", p.ClientID
	}
	user := tenant.FromContext(ctx).UserID
	docID, versionID := newUUID(), newUUID()
	sum := sha256.Sum256(content)
	checksum := hex.EncodeToString(sum[:])
	stamp := data.GeneratedAt.Format("20060102-150405")
	title := fmt.Sprintf("Compliance report %s (%s)", strings.ToUpper(p.Standard), data.GeneratedAt.Format(time.DateOnly))
	if data.ClientName != "" {
		title = fmt.Sprintf("Compliance report %s %s (%s)", strings.ToUpper(p.Standard), data.ClientName, data.GeneratedAt.Format(time.DateOnly))
	}
	tags := []string{"standard:" + p.Standard, "format:" + p.Format}
	if jobID != "" {
		tags = append(tags, "job:"+jobID)
	}
	d := &document.Document{ID: docID, OrganizationID: orgID, Title: title,
		FileName: fmt.Sprintf("compliance-%s-%s.%s", p.Standard, stamp, ext), Category: CategoryComplianceReport, Tags: tags,
		UploadedBy: uuidOr(user)}
	v := &document.Version{ID: versionID, StorageKey: blob.OrgKey(orgID, "documents", docID, versionID), Size: int64(len(content)),
		MimeType: mime, ChecksumSHA256: &checksum, UploadedBy: uuidOr(user)}
	var limit *int64
	if g.limits != nil {
		var lerr error
		if limit, lerr = g.limits.StorageLimitBytes(ctx, orgID); lerr != nil {
			return nil, fmt.Errorf("read the storage limit: %w", lerr)
		}
	}
	if err := g.blobs.Put(ctx, document.Bucket, v.StorageKey, bytes.NewReader(content), v.Size, mime); err != nil {
		return nil, fmt.Errorf("store the report content: %w", err)
	}
	link := &document.DocumentLink{OrganizationID: orgID, DocumentID: docID, EntityType: linkType, EntityID: linkID}
	err := database.WithRequestTenant(ctx, g.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if cerr := document.CreateWithFirstVersionTx(ctx, tx, d, v, limit); cerr != nil {
			return cerr
		}
		if lerr := document.LinkTx(ctx, tx, link); lerr != nil {
			return lerr
		}
		actorID, actorType := "", "system"
		if api.IsUUID(user) {
			actorID, actorType = user, "user"
		}
		changes := map[string]any{"standard": p.Standard, "format": p.Format, "scope": scope, "document_id": docID}
		if p.ClientID != "" {
			changes["client_id"] = p.ClientID
		}
		if jobID != "" {
			changes["job_id"] = jobID
		}
		return database.BeforeCommit(ctx, func() error {
			_, aerr := audit.NewPGRecorder().Record(ctx, tx, audit.Entry{OrganizationID: orgID, ActorID: actorID, ActorType: actorType,
				Action: ActionReportGenerated, ResourceType: "document", ResourceID: docID, Changes: changes})
			return aerr
		})
	})
	if err != nil {
		_ = g.blobs.Delete(context.WithoutCancel(ctx), document.Bucket, v.StorageKey)
		return nil, fmt.Errorf("store the report document: %w", err)
	}
	document.EnqueueScan(ctx, g.scans, v)
	return &ReportJobResult{DocumentID: docID, Standard: p.Standard, Format: p.Format, Scope: scope, ClientID: p.ClientID, Size: v.Size}, nil
}

// uuidOr returns id when it is a UUID, else empty (a system job).
func uuidOr(id string) string {
	if api.IsUUID(id) {
		return id
	}
	return ""
}

// ClientVisible reports whether the client is visible in the scope of ctx.
func (g *ReportGenerator) ClientVisible(ctx context.Context, orgID, clientID string) (bool, error) {
	if !g.available() {
		return false, ErrReportsUnavailable
	}
	var ok bool
	err := database.WithRequestTenant(ctx, g.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM client WHERE organization_id = $1 AND id = $2::uuid)`, orgID, clientID).Scan(&ok)
	})
	return ok, err
}

// ─── jobs ─────────────────────────────────────────────────────────────────────

// WithReports sets the generator of the report jobs.
func (j *Jobs) WithReports(g *ReportGenerator) *Jobs {
	j.reports = g
	return j
}

// SubmitReport queues a report job for createdBy (AK1); the caller checked
// standard, format, client and scope.
func (j *Jobs) SubmitReport(ctx context.Context, orgID, createdBy string, p *ReportParams) (*jobs.Job, error) {
	if j == nil || j.svc == nil || !j.reports.available() {
		return nil, ErrReportsUnavailable
	}
	opts := jobs.EnqueueOptions{CreatedBy: createdBy}
	if j.queue != nil {
		job, _, err := j.queue.Submit(ctx, orgID, ReportJobKind, p, opts)
		return job, err
	}
	ids := make(chan string, 1)
	params := *p
	job, _, err := j.svc.RunDetachedWith(ctx, orgID, ReportJobKind, p, opts, func(ctx context.Context) (any, error) {
		id := <-ids
		return j.reports.Generate(ctx, orgID, id, &params, j.cancelled(ctx, orgID, id))
	})
	if job != nil {
		ids <- job.ID
	} else {
		ids <- ""
	}
	return job, err
}

func (j *Jobs) cancelled(ctx context.Context, orgID, jobID string) func() bool {
	return func() bool {
		if j.svc == nil || jobID == "" {
			return false
		}
		c, err := j.svc.IsCancelled(ctx, orgID, jobID)
		return err == nil && c
	}
}

// RunReportJob is the handler of ReportJobKind on the job queue: the report
// as the creator of the job, in the creator's scope.
func (j *Jobs) RunReportJob(ctx context.Context, job *jobs.Job) (any, error) {
	var p ReportParams
	if err := json.Unmarshal(job.Params, &p); err != nil {
		return nil, fmt.Errorf("decode %s params: %w", ReportJobKind, err)
	}
	ctx = jobs.CreatorContext(ctx, job)
	return j.reports.Generate(ctx, job.OrganizationID, job.ID, &p, j.cancelled(ctx, job.OrganizationID, job.ID))
}

// ─── report list ──────────────────────────────────────────────────────────────

// ReportEntry is one stored report of GET /api/v1/compliance/reports (AK5).
type ReportEntry struct {
	DocumentID string    `json:"document_id"`
	Title      string    `json:"title"`
	FileName   string    `json:"file_name"`
	Standard   string    `json:"standard"`
	Format     string    `json:"format"`
	Scope      string    `json:"scope"`
	ClientID   string    `json:"client_id,omitempty"`
	ScanStatus string    `json:"scan_status"`
	JobID      string    `json:"job_id,omitempty"`
	JobStatus  string    `json:"job_status,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

// maxReportsListed bounds the report documents the list reads.
const maxReportsListed = 1000

// ListReports returns the report documents visible in the scope of ctx,
// newest first, with standard, format, scope and the status of their job;
// capped reports more than maxReportsListed.
func (g *ReportGenerator) ListReports(ctx context.Context, orgID string) (out []ReportEntry, capped bool, err error) {
	if !g.available() {
		return nil, false, ErrReportsUnavailable
	}
	out = []ReportEntry{}
	err = database.WithRequestTenant(ctx, g.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		// document.VisibleSQL: the visibility of GET /documents/{id} (an
		// organization report only for org-wide principals), also without
		// the policies.
		rows, qerr := tx.Query(ctx, `SELECT document.id::text, document.title, document.file_name, document.tags, document.created_at,
				COALESCE(v.scan_status, ''), COALESCE(l.entity_type, ''), COALESCE(l.entity_id::text, '')
			FROM document
			LEFT JOIN document_version v ON v.document_id = document.id AND v.version = document.version
			LEFT JOIN LATERAL (SELECT dl.entity_type, dl.entity_id FROM document_link dl WHERE dl.document_id = document.id
				ORDER BY dl.created_at, dl.id LIMIT 1) l ON true
			WHERE document.organization_id = $1 AND document.category = $2 AND `+document.VisibleSQL()+`
			ORDER BY document.created_at DESC, document.id DESC LIMIT $3`, orgID, CategoryComplianceReport, maxReportsListed+1)
		if qerr != nil {
			return qerr
		}
		defer rows.Close()
		for rows.Next() {
			var e ReportEntry
			var tags []string
			var linkType, linkID string
			if serr := rows.Scan(&e.DocumentID, &e.Title, &e.FileName, &tags, &e.CreatedAt, &e.ScanStatus, &linkType, &linkID); serr != nil {
				return serr
			}
			for _, tag := range tags {
				switch k, v, _ := strings.Cut(tag, ":"); k {
				case "standard":
					e.Standard = v
				case "format":
					e.Format = v
				case "job":
					e.JobID = v
				}
			}
			e.Scope = "organization"
			if linkType == "client" {
				e.Scope, e.ClientID = "client", linkID
			}
			out = append(out, e)
		}
		if rerr := rows.Err(); rerr != nil {
			return rerr
		}
		var jobIDs []string
		for i := range out {
			if api.IsUUID(out[i].JobID) {
				jobIDs = append(jobIDs, out[i].JobID)
			}
		}
		if len(jobIDs) == 0 {
			return nil
		}
		status := map[string]string{}
		jrows, jerr := tx.Query(ctx, `SELECT id::text, status FROM job WHERE organization_id = $1 AND id = ANY ($2::uuid[])`, orgID, jobIDs)
		if jerr != nil {
			return jerr
		}
		defer jrows.Close()
		for jrows.Next() {
			var id, s string
			if serr := jrows.Scan(&id, &s); serr != nil {
				return serr
			}
			status[id] = s
		}
		if rerr := jrows.Err(); rerr != nil {
			return rerr
		}
		for i := range out {
			out[i].JobStatus = status[out[i].JobID]
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	if len(out) > maxReportsListed {
		return out[:maxReportsListed], true, nil
	}
	return out, false, nil
}

// ─── PostgreSQL reader ────────────────────────────────────────────────────────

type pgReportReader struct {
	pool *pgxpool.Pool
}

func (r *pgReportReader) names(ctx context.Context, orgID, clientID string) (org, client string, err error) {
	err = database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		if qerr := tx.QueryRow(ctx, `SELECT name FROM organization WHERE id = $1`, orgID).Scan(&org); qerr != nil {
			return fmt.Errorf("read the organization: %w", qerr)
		}
		if clientID == "" {
			return nil
		}
		qerr := tx.QueryRow(ctx, `SELECT name FROM client WHERE organization_id = $1 AND id = $2::uuid`, orgID, clientID).Scan(&client)
		if errors.Is(qerr, pgx.ErrNoRows) {
			return ErrReportClientNotFound
		}
		return qerr
	})
	return org, client, err
}

func (r *pgReportReader) controls(ctx context.Context, orgID, standard string) ([]ReportControl, error) {
	var out []ReportControl
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT c.id::text, c.control_id, c.title_en, COALESCE(ru.id::text, ''), COALESCE(ru.name, ''),
				COALESCE(ru.severity, ''), COALESCE(ru.active, false)
			FROM compliance_control c
			LEFT JOIN compliance_rule_control rc ON rc.control_id = c.id AND rc.organization_id = $1
			LEFT JOIN compliance_rule ru ON ru.id = rc.rule_id AND ru.organization_id = $1
			WHERE c.standard = $2
			ORDER BY c.sort_order, c.control_id, ru.name, ru.id`, orgID, standard)
		if err != nil {
			return err
		}
		defer rows.Close()
		last := ""
		for rows.Next() {
			var id string
			var c ReportControl
			var rule ReportControlRule
			if err := rows.Scan(&id, &c.ControlID, &c.Title, &rule.RuleID, &rule.RuleName, &rule.Severity, &rule.Active); err != nil {
				return err
			}
			if id != last {
				out = append(out, c)
				last = id
			}
			if rule.RuleID != "" {
				out[len(out)-1].Rules = append(out[len(out)-1].Rules, rule)
			}
		}
		return rows.Err()
	})
	return out, err
}

func (r *pgReportReader) results(ctx context.Context, orgID, clientID string, ruleIDs []string, status, afterRule, afterCI string, limit int) ([]ReportResultRow, error) {
	var out []ReportResultRow
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT r.rule_id::text, ru.name, ru.severity, ru.category, ru.remediation_hint, r.ci_id::text, ci.name,
				r.status, r.evaluated_at
			FROM compliance_result r
			JOIN compliance_rule ru ON ru.id = r.rule_id AND ru.organization_id = r.organization_id
			JOIN ci ON ci.id = r.ci_id AND ci.organization_id = r.organization_id
			WHERE r.organization_id = $1 AND ($2 = '' OR r.status = $2)
			  AND ($3::uuid[] IS NULL OR r.rule_id = ANY ($3::uuid[]))
			  AND ($4 = '' OR (r.rule_id, r.ci_id) > ($4::uuid, $5::uuid))
			  AND ($7 = '' OR ci.client_id = NULLIF($7, '')::uuid)
			ORDER BY r.rule_id, r.ci_id LIMIT $6`, orgID, status, nullableIDs(ruleIDs), afterRule, nullUUID(afterCI), limit, clientID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row ReportResultRow
			if err := rows.Scan(&row.RuleID, &row.RuleName, &row.Severity, &row.Category, &row.RemediationHint, &row.CIID, &row.CIName,
				&row.Status, &row.EvaluatedAt); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	return out, err
}

func nullableIDs(ids []string) any {
	if ids == nil {
		return nil
	}
	return ids
}

func nullUUID(id string) any {
	if id == "" {
		return "00000000-0000-0000-0000-000000000000"
	}
	return id
}

func (r *pgReportReader) openFindings(ctx context.Context, orgID, clientID string) ([]ReportFindings, error) {
	var out []ReportFindings
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT severity, count(*)::int, (count(*) FILTER (WHERE kev))::int FROM security_finding
			WHERE organization_id = $1 AND status IN ('open', 'acknowledged') AND ($2 = '' OR client_id = NULLIF($2, '')::uuid)
			GROUP BY severity`, orgID, clientID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var f ReportFindings
			if err := rows.Scan(&f.Severity, &f.Open, &f.KEV); err != nil {
				return err
			}
			out = append(out, f)
		}
		return rows.Err()
	})
	slices.SortFunc(out, func(a, b ReportFindings) int { return severityRank(a.Severity) - severityRank(b.Severity) })
	return out, err
}
