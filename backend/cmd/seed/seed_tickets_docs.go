package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
)

// The tickets and documents of the Phase-2 seed (WP-820, SIM-01 [P2]): a
// business calendar of the demo organization in Europe/Berlin with the
// client hq in the same zone, an SLA per priority bound to it, tickets in
// the states of WP-290 whose due times the SLA package computes for their
// creation relative to the anchor, and documents with two versions each,
// linked to an asset, a contract and a ticket. The content of a version is
// deterministic; it is stored in the blob store only with Options.Blobs
// (-document-content), outside the database transaction (K20).

// CalendarName is the name of the business calendar of the seed.
const CalendarName = "Seed: office hours Europe/Berlin"

// CalendarTimezone is the zone of the calendar and of the client hq.
const CalendarTimezone = "Europe/Berlin"

// SeedHolidays are the holidays of the calendar in days after the anchor.
var SeedHolidays = []int{-1, 4}

// seedSLA is the SLA policy of a priority: the targets in business minutes
// (TKT-02).
type seedSLA struct {
	priority             string
	response, resolution int
}

// seedSLAs are the policies of the seed, one per priority of WP-290.
var seedSLAs = []seedSLA{
	{"low", 480, 2880}, {"medium", 240, 1440}, {"high", 60, 480}, {"critical", 15, 240},
}

// SLAName is the name of the seeded policy of a priority.
func SLAName(priority string) string { return "Seed SLA " + priority }

// seedTicket is a ticket of the seed: its number in the seed (the key of
// its title), its target state, priority, the [B] CI (ci > 0) or the asset
// (inventory number asset > 0) it relates to and its creation before the
// anchor.
type seedTicket struct {
	n                int
	status, priority string
	ci, asset        int
	age              time.Duration
}

// TicketTitle is the title of seeded ticket n, its key.
func TicketTitle(n int) string { return fmt.Sprintf("Seed ticket %02d", n) }

// The times of the clock of a seeded ticket after its creation: the first
// response of a ticket in work and the resolution and closing of a
// resolved or closed one.
const (
	ticketFirstResponse = 10 * time.Minute
	ticketResolved      = 4 * time.Hour
	ticketClosed        = 8 * time.Hour
)

// demoTickets are the tickets of the demo organization: every state of
// WP-290, on [B] CIs and on assets (41 is the asset of the first blade
// chassis); the new critical ticket of two hours ago is overdue at the
// anchor.
var demoTickets = []seedTicket{
	{1, ticket.StatusNew, "critical", 1, 0, 2 * time.Hour},
	{2, ticket.StatusNew, "low", 5, 0, time.Hour},
	{3, ticket.StatusOpen, "high", 10, 0, 26 * time.Hour},
	{4, ticket.StatusOpen, "medium", 0, 21, 50 * time.Hour},
	{5, ticket.StatusInProgress, "high", 21, 0, 72 * time.Hour},
	{6, ticket.StatusInProgress, "medium", 0, 22, 5 * time.Hour},
	{7, ticket.StatusWaiting, "low", 30, 0, 120 * time.Hour},
	{8, ticket.StatusResolved, "medium", 35, 0, 96 * time.Hour},
	{9, ticket.StatusResolved, "critical", 0, 41, 30 * time.Hour},
	{10, ticket.StatusClosed, "low", 40, 0, 240 * time.Hour},
}

// acmeTickets are the tickets of acme.
var acmeTickets = []seedTicket{
	{1, ticket.StatusNew, "medium", 1, 0, 3 * time.Hour},
	{2, ticket.StatusInProgress, "high", 0, 1, 48 * time.Hour},
}

// ticketPath is the way of the state machine (WP-290) from new to status.
func ticketPath(status string) []string {
	switch status {
	case ticket.StatusOpen:
		return []string{ticket.StatusOpen}
	case ticket.StatusInProgress:
		return []string{ticket.StatusOpen, ticket.StatusInProgress}
	case ticket.StatusWaiting:
		return []string{ticket.StatusOpen, ticket.StatusInProgress, ticket.StatusWaiting}
	case ticket.StatusResolved:
		return []string{ticket.StatusOpen, ticket.StatusInProgress, ticket.StatusResolved}
	case ticket.StatusClosed:
		return []string{ticket.StatusOpen, ticket.StatusInProgress, ticket.StatusResolved, ticket.StatusClosed}
	}
	return nil
}

// Entity types of the targets of the seeded documents.
const (
	docTargetAsset    = "asset"
	docTargetContract = "contract"
	docTargetTicket   = "ticket"
)

// DocumentVersions is the number of versions of every seeded document.
const DocumentVersions = 2

// seedDocument is a document of the seed: its key, title, file name, type,
// category and the object it is linked to (an inventory number, a contract
// number or the number of a seeded ticket).
type seedDocument struct {
	key, title, fileName, mime, category string
	target, ref                          string
}

var (
	demoDocuments = []seedDocument{
		{"manual", "Seed: operating manual INV-00001", "operating-manual.txt", "text/plain", "manual", docTargetAsset, "INV-00001"},
		{"contract", "Seed: contract SEED-C-001", "contract-SEED-C-001.pdf", "application/pdf", "contract", docTargetContract, "SEED-C-001"},
		{"report", "Seed: incident report", "incident-report.txt", "text/plain", "other", docTargetTicket, "1"},
	}
	acmeDocuments = []seedDocument{
		{"manual", "Seed: operating manual INV-00001", "operating-manual.txt", "text/plain", "manual", docTargetAsset, "INV-00001"},
	}
)

// ticketDocContent is the ticket and document content of a Phase-2 level.
type ticketDocContent struct {
	// calendar: the business calendar in CalendarTimezone and the zone of
	// the client hq; without it the policies count business time in the
	// built-in standard calendar of the organization's zone.
	calendar  bool
	tickets   []seedTicket
	documents []seedDocument
}

// ticketDocContentOf is the content of the level: the full one for demo,
// 2 tickets and 1 document for acme, none for the small organizations.
func ticketDocContentOf(level phase2Level) *ticketDocContent {
	switch level {
	case phase2Full:
		return &ticketDocContent{calendar: true, tickets: demoTickets, documents: demoDocuments}
	case phase2Subset:
		return &ticketDocContent{tickets: acmeTickets, documents: acmeDocuments}
	default:
		return nil
	}
}

// documentID and versionID are the deterministic ids of a seeded document
// and its version n; the storage key follows the upload path.
func (o *orgSeed) documentID(key string) string { return o.uid("document", key) }
func (o *orgSeed) versionID(key string, n int) string {
	return o.uid("document", key, "version", fmt.Sprint(n))
}

// documentStorageKey is the key of version n of a seeded document below
// the organization's prefix, like the upload path
// (blob.OrgKey(<org>, "documents", <document>, <version id>)).
func (o *orgSeed) documentStorageKey(key string, n int) string {
	return blob.OrgKey(o.id, "documents", o.documentID(key), o.versionID(key, n))
}

// documentContent is the deterministic content of version n: a small text
// or PDF file naming the organization, the document and the version.
func (o *orgSeed) documentContent(d *seedDocument, n int) []byte {
	text := fmt.Sprintf("Reticora CMDB seed document\nOrganization: %s\nDocument: %s\nVersion: %d\n", o.slug, d.title, n)
	if d.mime == "application/pdf" {
		return minimalPDF(text)
	}
	return []byte(text)
}

// minimalPDF is a one-page PDF 1.4 showing the lines of text, with a
// correct cross-reference table.
func minimalPDF(text string) []byte {
	var stream strings.Builder
	stream.WriteString("BT /F1 12 Tf 72 760 Td 14 TL\n")
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		line = strings.NewReplacer(`\`, `\\`, "(", `\(`, ")", `\)`).Replace(line)
		stream.WriteString("(" + line + ") Tj T*\n")
	}
	stream.WriteString("ET\n")
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>",
		fmt.Sprintf("<< /Length %d >>\nstream\n%sendstream", stream.Len(), stream.String()),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects))
	for i, obj := range objects {
		offsets[i] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

// sha256Hex is the hex sha256 of content, the checksum of a version.
func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// uploadDocuments stores the content of every version of the seeded
// documents in the bucket of the documents under its storage key, before
// and outside the database transaction (K20). It is idempotent: an object
// that holds the same content already is left alone. It returns the number
// of contents in the store.
func (o *orgSeed) uploadDocuments(ctx context.Context, store blob.Store, docs []seedDocument) (int, error) {
	stored := 0
	for i := range docs {
		d := &docs[i]
		for n := 1; n <= DocumentVersions; n++ {
			content := o.documentContent(d, n)
			key := o.documentStorageKey(d.key, n)
			same, err := blobHolds(ctx, store, key, content)
			if err != nil {
				return stored, err
			}
			if !same {
				if err = store.Put(ctx, document.Bucket, key, bytes.NewReader(content), int64(len(content)), d.mime); err != nil {
					return stored, fmt.Errorf("store %s version %d: %w", d.key, n, err)
				}
			}
			stored++
		}
	}
	return stored, nil
}

// blobHolds reports whether the object key holds content; a missing object
// holds nothing.
func blobHolds(ctx context.Context, store blob.Store, key string, content []byte) (bool, error) {
	r, err := store.Get(ctx, document.Bucket, key)
	if err != nil {
		// FileStore and S3Store report a missing object as an error of
		// their own; any read failure is answered by a fresh upload.
		return false, nil //nolint:nilerr // a missing object is stored anew
	}
	defer r.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		return false, nil //nolint:nilerr // an unreadable object is stored anew
	}
	return bytes.Equal(got, content), nil
}

// ticketDocSeed holds the transaction of the ticket and document steps of
// an organization and the repositories they write through.
type ticketDocSeed struct {
	org      *orgSeed
	tx       pgx.Tx
	admin    string
	assignee string
	content  *ticketDocContent

	slas      *sla.PGRepository
	tickets   *ticket.Service
	documents *document.PGRepository

	calendarID string
	ticketIDs  map[int]string
}

// ticketDocSteps runs the ticket and document steps of the organization
// after its assets and stock (stockSteps): with blobs it first stores the
// content of the documents (uploadDocuments), then it runs one
// transaction through database.WithTenant, organization-wide as the
// administrator; the repositories join it (database.ContextWithTx).
// Calendar, policies, tickets and documents are found again by name,
// title or id, so a second run creates no version and leaves
// organization_usage as it is. It returns the number of document contents
// in the store and of the seeded versions.
func (o *orgSeed) ticketDocSteps(ctx context.Context, pool *pgxpool.Pool, blobs blob.Store) (stored, versions int, err error) {
	content := ticketDocContentOf(o.phase2)
	if content == nil {
		return 0, 0, nil
	}
	for i := range content.documents {
		d := &content.documents[i]
		for n := 1; n <= DocumentVersions; n++ {
			if verifyErr := document.VerifyContent(d.mime, o.documentContent(d, n)); verifyErr != nil {
				return 0, 0, fmt.Errorf("document %s: %w", d.key, verifyErr)
			}
		}
	}
	versions = len(content.documents) * DocumentVersions
	if blobs != nil {
		if stored, err = o.uploadDocuments(ctx, blobs, content.documents); err != nil {
			return stored, versions, fmt.Errorf("document content: %w", err)
		}
	}
	admin := o.uid("user", "admin")
	scope := database.OrgWideScope(o.id, admin)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: o.id, UserID: admin})
	err = database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		s := &ticketDocSeed{org: o, tx: tx, admin: admin, assignee: admin, content: content,
			slas: sla.NewPGRepository(pool), tickets: ticket.NewService(ticket.NewPGRepository(pool).WithEvents(events.NewOutbox()), nil),
			documents: document.NewPGRepository(pool), ticketIDs: map[int]string{}}
		if content.calendar {
			s.assignee = o.uid("user", "engineer")
		}
		ctx = database.ContextWithTx(ctx, tx)
		steps := []struct {
			name string
			fn   func(context.Context) error
		}{
			{"business calendar", s.ensureCalendar},
			{"sla policies", s.ensureSLAs},
			{"tickets", s.ensureTickets},
			{"documents", s.ensureDocuments},
		}
		for _, step := range steps {
			if stepErr := step.fn(ctx); stepErr != nil {
				return fmt.Errorf("%s: %w", step.name, stepErr)
			}
		}
		return nil
	})
	return stored, versions, err
}

// lookup returns the id the query finds, "" for none.
func (s *ticketDocSeed) lookup(ctx context.Context, query string, args ...any) (string, error) {
	var id string
	err := s.tx.QueryRow(ctx, query, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// SeedCalendar is the business calendar of the seed for the anchor at:
// Monday to Friday 08:00–18:00 in CalendarTimezone and the holidays of
// SeedHolidays.
func SeedCalendar(at time.Time) sla.Calendar {
	cal := sla.DefaultCalendar()
	cal.Timezone = CalendarTimezone
	for k, days := range SeedHolidays {
		cal.Holidays = append(cal.Holidays, sla.Holiday{Date: at.AddDate(0, 0, days).Format(time.DateOnly),
			Name: fmt.Sprintf("Seed holiday %d", k+1)})
	}
	return cal
}

// ensureCalendar creates the business calendar of the demo organization
// (WP-292, WP-768), the organization's default, found again by name, and
// sets the zone of the client hq to CalendarTimezone.
func (s *ticketDocSeed) ensureCalendar(ctx context.Context) error {
	if !s.content.calendar {
		return nil
	}
	if _, err := s.tx.Exec(ctx, `UPDATE client SET timezone = $3 WHERE organization_id = $1 AND id = $2
		AND timezone IS DISTINCT FROM $3`, s.org.id, s.org.uid("client", "hq"), CalendarTimezone); err != nil {
		return fmt.Errorf("time zone of the client hq: %w", err)
	}
	id, err := s.lookup(ctx, `SELECT id::text FROM business_calendar WHERE organization_id = $1 AND name = $2`, s.org.id, CalendarName)
	if err != nil {
		return err
	}
	if id == "" {
		cal := SeedCalendar(s.org.at)
		tz := cal.Timezone
		item := &sla.BusinessCalendar{OrganizationID: s.org.id, Name: CalendarName, Timezone: &tz,
			WeeklyHours: cal.WeeklyHours, Holidays: cal.Holidays, IsDefault: true}
		if err = s.slas.CreateCalendar(ctx, item); err != nil {
			return err
		}
		id = item.ID
	}
	s.calendarID = id
	return nil
}

// ensureSLAs creates an org-wide policy per priority, found again by name.
// It counts business time (business_calendar, the switch of WP-292) and is
// bound to the calendar of the seed when there is one; otherwise the
// calendar chain ends in the built-in standard.
func (s *ticketDocSeed) ensureSLAs(ctx context.Context) error {
	for _, p := range seedSLAs {
		id, err := s.lookup(ctx, `SELECT id::text FROM sla WHERE organization_id = $1 AND client_id IS NULL AND name = $2`,
			s.org.id, SLAName(p.priority))
		if err != nil || id != "" {
			if err != nil {
				return err
			}
			continue
		}
		policy := &sla.Policy{OrganizationID: s.org.id, Name: SLAName(p.priority), Priority: p.priority,
			ResponseTargetMinutes: p.response, ResolutionTargetMinutes: p.resolution, BusinessCalendar: true}
		if s.calendarID != "" {
			calendar := s.calendarID
			policy.CalendarID = &calendar
		}
		if err = s.slas.CreatePolicy(ctx, policy); err != nil {
			return fmt.Errorf("policy %s: %w", p.priority, err)
		}
	}
	return nil
}

// assetID is the id of the asset with the inventory number of n.
func (s *ticketDocSeed) assetID(ctx context.Context, n int) (string, error) {
	inventory := fmt.Sprintf("INV-%05d", n)
	id, err := s.lookup(ctx, `SELECT id::text FROM asset WHERE organization_id = $1 AND inventory_no = $2`, s.org.id, inventory)
	if err == nil && id == "" {
		err = fmt.Errorf("asset %s not found", inventory)
	}
	return id, err
}

// ensureTickets creates the tickets, found again by title, through the
// ticket service (WP-766) without its SLA hooks: the ticket is created as
// new, its creation moved to the anchor minus its age, and the SLA package
// chooses the policy and computes the due times for that moment
// (sla.PGRepository.ApplyForTicket, WP-292). The ticket then follows the
// state machine (Service.Update); first response, resolution and closing
// are recorded at fixed times after the creation.
func (s *ticketDocSeed) ensureTickets(ctx context.Context) error {
	for i := range s.content.tickets {
		spec := &s.content.tickets[i]
		title := TicketTitle(spec.n)
		id, err := s.lookup(ctx, `SELECT id::text FROM ticket WHERE organization_id = $1 AND title = $2`, s.org.id, title)
		if err != nil {
			return err
		}
		if id != "" {
			s.ticketIDs[spec.n] = id
			continue
		}
		t := &ticket.Ticket{OrganizationID: s.org.id, Title: title, Description: "Seed (WP-820)", Priority: spec.priority,
			Category: ticket.DefaultCategory, ReporterID: s.admin, AssigneeID: s.assignee}
		if spec.ci > 0 {
			t.RelatedCIID = s.org.uid("ci", fmt.Sprint(spec.ci))
		} else if t.RelatedAssetID, err = s.assetID(ctx, spec.asset); err != nil {
			return err
		}
		if err = s.tickets.Create(ctx, t, nil, ticket.CreateOptions{}); err != nil {
			return fmt.Errorf("ticket %s: %w", title, err)
		}
		created := s.org.at.Add(-spec.age).UTC()
		if _, err = s.tx.Exec(ctx, `UPDATE ticket SET created_at = $2, updated_at = $2 WHERE id = $1`, t.ID, created); err != nil {
			return fmt.Errorf("ticket %s: creation: %w", title, err)
		}
		t.CreatedAt = created
		if _, err = s.slas.ApplyForTicket(ctx, s.org.id, t, ""); err != nil {
			return fmt.Errorf("ticket %s: sla: %w", title, err)
		}
		if err = s.advance(ctx, t, spec.status); err != nil {
			return fmt.Errorf("ticket %s: %w", title, err)
		}
		s.ticketIDs[spec.n] = t.ID
	}
	return nil
}

// advance moves ticket t along the state machine to status and records its
// clock: the first response once it is in work, the resolution once it is
// resolved, resolved_at and closed_at at fixed times after its creation.
func (s *ticketDocSeed) advance(ctx context.Context, t *ticket.Ticket, status string) error {
	for _, next := range ticketPath(status) {
		step := next
		if _, err := s.tickets.Update(ctx, s.org.id, t.ID, &ticket.UpdateRequest{Status: &step}); err != nil {
			return fmt.Errorf("status %s: %w", step, err)
		}
		switch step {
		case ticket.StatusInProgress:
			if err := s.slas.MarkFirstResponse(ctx, s.org.id, t.ID, t.CreatedAt.Add(ticketFirstResponse)); err != nil {
				return err
			}
		case ticket.StatusResolved:
			if err := s.slas.MarkResolved(ctx, s.org.id, t.ID, t.CreatedAt.Add(ticketResolved)); err != nil {
				return err
			}
		}
	}
	var resolved, closed any
	switch status {
	case ticket.StatusResolved:
		resolved = t.CreatedAt.Add(ticketResolved)
	case ticket.StatusClosed:
		resolved, closed = t.CreatedAt.Add(ticketResolved), t.CreatedAt.Add(ticketClosed)
	default:
		return nil
	}
	_, err := s.tx.Exec(ctx, `UPDATE ticket SET resolved_at = $2, closed_at = $3 WHERE id = $1`, t.ID, resolved, closed)
	return err
}

// target is the id of the object a seeded document is linked to.
func (s *ticketDocSeed) target(ctx context.Context, d *seedDocument) (string, error) {
	switch d.target {
	case docTargetAsset:
		var n int
		if _, err := fmt.Sscanf(d.ref, "INV-%05d", &n); err != nil {
			return "", fmt.Errorf("inventory number %s: %w", d.ref, err)
		}
		return s.assetID(ctx, n)
	case docTargetContract:
		id, err := s.lookup(ctx, `SELECT id::text FROM contract WHERE organization_id = $1 AND number = $2`, s.org.id, d.ref)
		if err == nil && id == "" {
			err = fmt.Errorf("contract %s not found", d.ref)
		}
		return id, err
	case docTargetTicket:
		var n int
		if _, err := fmt.Sscan(d.ref, &n); err != nil {
			return "", err
		}
		if id, ok := s.ticketIDs[n]; ok {
			return id, nil
		}
		return "", fmt.Errorf("ticket %s not found", d.ref)
	}
	return "", fmt.Errorf("unknown target %s", d.target)
}

// ensureDocuments creates the documents (WP-267) with their versions
// (WP-731) in the way of the upload path: version 1 with the document and
// its link (document.CreateWithFirstVersionTx, document.LinkTx), the
// further versions through PGRepository.CreateVersion, each with size and
// sha256 of its content, the storage key of documentStorageKey and the
// usage counted in organization_usage. The fixed contents of the seed are
// marked clean like a finished scan (SetScanResult, E-42). A version that
// exists is skipped, so a second run counts no byte twice.
func (s *ticketDocSeed) ensureDocuments(ctx context.Context) error {
	for i := range s.content.documents {
		d := &s.content.documents[i]
		docID := s.org.documentID(d.key)
		for n := 1; n <= DocumentVersions; n++ {
			existing, err := s.lookup(ctx, `SELECT id::text FROM document_version WHERE document_id = $1 AND version = $2`, docID, n)
			if err != nil {
				return err
			}
			if existing != "" {
				continue
			}
			content := s.org.documentContent(d, n)
			checksum := sha256Hex(content)
			v := &document.Version{ID: s.org.versionID(d.key, n), OrganizationID: s.org.id, DocumentID: docID,
				StorageKey: s.org.documentStorageKey(d.key, n), Size: int64(len(content)), MimeType: d.mime,
				ChecksumSHA256: &checksum, ScanStatus: document.ScanPending, UploadedBy: s.admin}
			if n == 1 {
				err = s.createDocument(ctx, d, v)
			} else {
				_, err = s.documents.CreateVersion(ctx, v, nil)
			}
			if err != nil {
				return fmt.Errorf("document %s version %d: %w", d.key, n, err)
			}
			if v.Version != n {
				return fmt.Errorf("document %s: stored as version %d, want %d", d.key, v.Version, n)
			}
			if err = s.documents.SetScanResult(ctx, s.org.id, docID, n, document.ScanClean, ""); err != nil {
				return fmt.Errorf("document %s version %d: scan status: %w", d.key, n, err)
			}
		}
	}
	return nil
}

// createDocument inserts document d with its first version v and links it
// to its target.
func (s *ticketDocSeed) createDocument(ctx context.Context, d *seedDocument, v *document.Version) error {
	targetID, err := s.target(ctx, d)
	if err != nil {
		return err
	}
	doc := &document.Document{ID: v.DocumentID, OrganizationID: s.org.id, Title: d.title, Description: "Seed (WP-820)",
		FileName: d.fileName, Category: d.category, UploadedBy: s.admin}
	if err = document.CreateWithFirstVersionTx(ctx, s.tx, doc, v, nil); err != nil {
		return err
	}
	return document.LinkTx(ctx, s.tx, &document.DocumentLink{OrganizationID: s.org.id, DocumentID: doc.ID,
		EntityType: d.target, EntityID: targetID})
}
