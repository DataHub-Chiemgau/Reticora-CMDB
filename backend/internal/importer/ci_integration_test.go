package importer

import (
	"context"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// ingestProvenance is the provenance of the ingest over the override
// repository, as the router wires it.
type ingestProvenance struct{ repo *override.PGRepository }

func (p ingestProvenance) RecordDiscovered(ctx context.Context, orgID, ciID, field string, value any, source string, at time.Time) (*discovery.FieldProvenance, error) {
	fv, err := p.repo.RecordDiscovered(ctx, orgID, ciID, field, value, source, at)
	if err != nil {
		return nil, err
	}
	return &discovery.FieldProvenance{Diverged: fv.Diverged}, nil
}

func (p ingestProvenance) Decide(ctx context.Context, w *override.Write) override.Decision {
	return override.DecideAutomatedWrite(ctx, p.repo, w)
}

// fixedLimit is a max_cis of n CIs.
type fixedLimit struct{ n int64 }

func (l fixedLimit) AllowCreate(_ context.Context, _, feature string, current int64) error {
	if current >= l.n {
		return &entitlement.LimitExceededError{FeatureKey: feature, Limit: l.n, Current: current}
	}
	return nil
}

// pgCI is the import stack with the CI writer (WP-302) over PostgreSQL:
// the CI service with audit, the discovery handler of the ingest (two
// lanes, so the ingest takes the identity locks) and the override
// repository.
type pgCI struct {
	*pgImport
	cis      *ci.PGRepository
	types    *citype.PGRepository
	ovr      *override.PGRepository
	disc     *discovery.Handler
	server   string
	switchID string
}

func newPGCI(t *testing.T) *pgCI {
	t.Helper()
	s := newPGImport(t)
	if _, err := s.f.Admin.Exec(context.Background(), `UPDATE role SET permissions = '["import:run","location:write","ci:write"]' WHERE id = $1`,
		s.role); err != nil {
		t.Fatal(err)
	}
	c := &pgCI{pgImport: s, cis: ci.NewPGRepositoryWithAudit(s.f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()), types: citype.NewPGRepository(s.f.App),
		ovr: override.NewPGRepository(s.f.App)}
	c.disc = discovery.NewHandler(discovery.NewPGRepository(s.f.App), c.cis).WithProvenance(ingestProvenance{repo: c.ovr}).
		WithInstanceFields(c.types).WithIngestLanes(2)
	c.writer(nil)
	for key, dst := range map[string]*string{"server": &c.server, "switch": &c.switchID} {
		if err := s.f.Admin.QueryRow(context.Background(), `SELECT id::text FROM ci_type WHERE key = $1 AND organization_id IS NULL`, key).
			Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// writer registers the CI writer with the max_cis limit (nil: none).
func (c *pgCI) writer(limit ci.LimitGuard) {
	svc := ci.NewServiceWithLimits(c.cis, limit).WithFieldResolver(c.types)
	policies := permission.NewFieldResolver(permission.NewPGRepository(c.f.App), nil, permission.NewPGFieldGrants(c.f.App), nil)
	c.h.WithWriter(NewCIWriter(svc, c.disc, c.ovr, policies).WithInstanceFields(c.types).WithFields(c.types).WithLocations(c.locs))
}

// seed inserts a CI of typeID with the columns of set and returns its id.
func (c *pgCI) seed(t *testing.T, typeID, name, source string, set map[string]any) string {
	t.Helper()
	id := c.f.ID()
	bg := context.Background()
	if _, err := c.f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, ci_type_id, name, status, discovery_source)
		VALUES ($1, $2, $3, $4, 'active', $5)`, id, c.f.OrgA, typeID, name, source); err != nil {
		t.Fatalf("seed %s: %v", name, err)
	}
	for col, v := range set {
		if _, err := c.f.Admin.Exec(bg, `UPDATE ci SET `+col+` = $2 WHERE id = $1`, id, v); err != nil {
			t.Fatalf("seed %s.%s: %v", name, col, err)
		}
	}
	return id
}

// run imports the CSV rows with a profile of mode that maps every column
// of header onto the target of its name; it returns the result and the
// report lines.
func (c *pgCI) run(t *testing.T, mode string, dryRun bool, header string, rows ...string) (JobResult, [][]string) {
	t.Helper()
	return c.finish(t, c.launch(t, mode, dryRun, header, rows...))
}

// launch starts the import of run and returns its job id.
func (c *pgCI) launch(t *testing.T, mode string, dryRun bool, header string, rows ...string) string {
	t.Helper()
	cols := map[string]ColumnMapping{}
	for _, target := range strings.Split(header, ";") {
		cols[target] = ColumnMapping{Target: target}
	}
	p := &Profile{OrganizationID: c.f.OrgA, Name: "ci-" + c.f.ID(), Entity: "ci", Format: FormatCSV, Mode: mode,
		Mapping: Mapping{Columns: cols}}
	if err := c.profile.Create(c.f.OrgCtx(c.f.OrgA), p); err != nil {
		t.Fatalf("profile: %v", err)
	}
	docID := c.document(t, header+"\n"+strings.Join(rows, "\n")+"\n")
	return c.start(t, c.ctx(), docID, p.ID, dryRun)
}

// finish waits for the import job and returns its result and report lines.
func (c *pgCI) finish(t *testing.T, jobID string) (JobResult, [][]string) {
	t.Helper()
	job := c.wait(t, jobID)
	if job.Status != jobs.StatusSucceeded {
		t.Fatalf("import: %s %s", job.Status, job.Error)
	}
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(c.report(t, jobID), reportBOM)))
	r.Comma, r.FieldsPerRecord = ';', -1
	lines, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	return resultOf(t, job), lines[1:]
}

// hasLine reports whether the report has a line of the file line with code
// (and field, when not empty).
func hasLine(report [][]string, line int, field, code string) bool {
	for _, l := range report {
		if l[0] == fmt.Sprint(line) && l[2] == code && (field == "" || l[1] == field) {
			return true
		}
	}
	return false
}

func (c *pgCI) count(t *testing.T, query string, args ...any) int {
	t.Helper()
	var n int
	if err := c.f.Admin.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func (c *pgCI) column(t *testing.T, id, col string) string {
	t.Helper()
	var v string
	if err := c.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(`+col+`::text, '') FROM ci WHERE id = $1`, id).Scan(&v); err != nil {
		t.Fatalf("%s of %s: %v", col, id, err)
	}
	return v
}

func (c *pgCI) liveCIs(t *testing.T) int {
	t.Helper()
	return c.count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND deleted_at IS NULL`, c.f.OrgA)
}

func (c *pgCI) reviews(t *testing.T, kind string) int {
	t.Helper()
	return c.count(t, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = $2`, c.f.OrgA, kind)
}

// TestImportCIIdentity covers the identity resolution of CI rows (WP-302,
// IMP-IO-01, REC-02, CI-13, CH29): a duplicate over every REC-02 key
// updates the CI, several matches are ambiguous_identity, a soft-deleted
// CI is held with resurrected_device, a host name hit creates a CI with
// probable_duplicate (no_identity_match in update_existing) and the serial
// number of a CI of another type is identity_taken. The dry run reports
// the same and writes nothing, no review and no event.
func TestImportCIIdentity(t *testing.T) {
	c := newPGCI(t)
	bySerial := c.seed(t, c.server, "imp-serial", "snmp", map[string]any{"serial_number": "IMP-SN-1"})
	byUUID := c.seed(t, c.server, "imp-uuid", "snmp", map[string]any{"hardware_uuid": "4c4c4544-0000-4000-8000-00000000a001"})
	byMAC := c.seed(t, c.switchID, "imp-mac", "snmp", map[string]any{"primary_mac": "00:1b:21:aa:00:01"})
	byIP := c.seed(t, c.switchID, "imp-ip", "snmp", map[string]any{"management_ip": "10.77.0.1", "sys_object_id": "1.3.6.1.4.1.9.1.1"})
	for i := range 2 {
		c.seed(t, c.switchID, fmt.Sprintf("imp-twin-%d", i), "snmp", map[string]any{"management_ip": "10.77.0.2", "sys_object_id": "1.3.6.1.4.1.9.1.1"})
	}
	gone := c.seed(t, c.server, "imp-gone", "snmp", map[string]any{"serial_number": "IMP-GONE"})
	if _, err := c.f.Admin.Exec(context.Background(), `UPDATE ci SET deleted_at = now() WHERE id = $1`, gone); err != nil {
		t.Fatal(err)
	}
	host := c.seed(t, c.server, "imp-host", "snmp", map[string]any{"hostname": "imp-host-1"})
	c.seed(t, c.switchID, "imp-switch-serial", "snmp", map[string]any{"serial_number": "IMP-SN-SW"})

	header := "ci_type;name;serial_number;hardware_uuid;primary_mac;management_ip;sys_object_id;hostname;manufacturer"
	rows := []string{
		"server;;IMP-SN-1;;;;;;Vendor-1",                             // 2: serial
		"server;;;4C4C4544-0000-4000-8000-00000000A001;;;;;Vendor-2", // 3: hardware UUID
		"switch;;;;00-1B-21-AA-00-01;;;;Vendor-3",                    // 4: MAC
		"switch;;;;;10.77.0.1;1.3.6.1.4.1.9.1.1;;Vendor-4",           // 5: management IP and sysObjectID
		"switch;;;;;10.77.0.2;1.3.6.1.4.1.9.1.1;;Vendor-5",           // 6: two CIs
		"server;imp-gone-again;IMP-GONE;;;;;;Vendor-6",               // 7: soft-deleted
		"server;imp-host-new;;;;;;imp-host-1;Vendor-7",               // 8: host name only
		"server;imp-taken;IMP-SN-SW;;;;;;Vendor-8",                   // 9: serial of a switch
	}
	before := c.liveCIs(t)
	const ciEvents = `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND (type LIKE 'ci.%' OR type LIKE 'review.%')`
	outbox := c.count(t, ciEvents, c.f.OrgA)

	dry, dryReport := c.run(t, "both", true, header, rows...)
	if c.liveCIs(t) != before || c.reviews(t, "resurrected_device")+c.reviews(t, "probable_duplicate") != 0 ||
		c.count(t, ciEvents, c.f.OrgA) != outbox {
		t.Fatalf("the dry run wrote CIs, reviews or events")
	}
	if c.column(t, bySerial, "manufacturer") != "" {
		t.Fatal("the dry run changed a CI")
	}

	res, report := c.run(t, "both", false, header, rows...)
	if res.Updated != 4 || res.Created != 1 || res.Skipped != 1 || res.Failed != 2 {
		t.Fatalf("result %+v, report %v", res, report)
	}
	if fmt.Sprint(dry.Created, dry.Updated, dry.Skipped, dry.Failed) != fmt.Sprint(res.Created, res.Updated, res.Skipped, res.Failed) {
		t.Errorf("dry run %+v, run %+v", dry, res)
	}
	for id, want := range map[string]string{bySerial: "Vendor-1", byUUID: "Vendor-2", byMAC: "Vendor-3", byIP: "Vendor-4"} {
		if got := c.column(t, id, "manufacturer"); got != want {
			t.Errorf("CI %s: manufacturer %q, want %q", id, got, want)
		}
	}
	for _, want := range []struct {
		line        int
		field, code string
	}{
		{2, "", "updated"}, {5, "", "updated"}, {6, "", CodeAmbiguousIdentity}, {7, "", CodeResurrectedDevice},
		{8, "", "created"}, {8, "", CodeProbableDuplicate}, {9, "serial_number", CodeIdentityTaken},
	} {
		if !hasLine(report, want.line, want.field, want.code) || !hasLine(dryReport, want.line, want.field, want.code) {
			t.Errorf("report lacks line %d %s %s:\n%v\ndry run:\n%v", want.line, want.field, want.code, report, dryReport)
		}
	}
	// Held for the deleted CI: no new CI, one review; the host name hit is
	// a new CI with a probable_duplicate review naming the old one.
	if n := c.count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number = 'IMP-GONE' AND deleted_at IS NULL`, c.f.OrgA); n != 0 {
		t.Errorf("%d CIs for the soft-deleted device", n)
	}
	if c.reviews(t, "resurrected_device") != 1 {
		t.Errorf("%d resurrected_device reviews", c.reviews(t, "resurrected_device"))
	}
	if n := c.count(t, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'probable_duplicate' AND $2 = ANY(candidate_ci_ids::text[])`,
		c.f.OrgA, host); n != 1 {
		t.Errorf("%d probable_duplicate reviews of the host name hit", n)
	}
	if c.liveCIs(t) != before+1 {
		t.Errorf("%d CIs, want %d", c.liveCIs(t), before+1)
	}

	// update_existing: a host name alone matches nothing.
	res, report = c.run(t, "update_existing", false, "ci_type;name;hostname", "server;imp-host-other;imp-host-1")
	if res.Skipped != 1 || !hasLine(report, 2, "", CodeNoIdentityMatch) || c.liveCIs(t) != before+1 {
		t.Errorf("update_existing with a host name: %+v %v", res, report)
	}
	// create_missing: a known CI is skipped.
	res, _ = c.run(t, "create_missing", false, "ci_type;serial_number;manufacturer", "server;IMP-SN-1;Vendor-X")
	if res.Skipped != 1 || c.column(t, bySerial, "manufacturer") != "Vendor-1" {
		t.Errorf("create_missing with a known CI: %+v", res)
	}
}

// TestImportCIWriteDecision covers the write decision and the creation of
// CI rows (WP-302, REC-03, REC-05, OVR-02, MET-14, DB-05, RBA-07): an
// override is kept (override_conflict for another value), the import
// overwrites a snmp value but no manual one, a later SNMP ingest does not
// overwrite an imported value, a new CI is manual with source import, the
// version rises with one ci_change per changed field, one audit entry and
// one event per row; instance attribute, asset-owned serial number, a
// field without write right and an observation time in the future are
// reported.
func TestImportCIWriteDecision(t *testing.T) {
	c := newPGCI(t)
	bg := context.Background()
	orgCtx := c.f.OrgCtx(c.f.OrgA)
	pinned := c.seed(t, c.server, "imp-pinned", "snmp", map[string]any{"serial_number": "IMP-PIN", "model": "M-1"})
	if _, err := c.ovr.SetOverride(orgCtx, c.f.OrgA, pinned, "model", "M-Pinned", c.user, "pinned", true); err != nil {
		t.Fatal(err)
	}
	snmp := c.seed(t, c.server, "imp-snmp", "snmp", map[string]any{"serial_number": "IMP-SNMP", "manufacturer": "SNMP-Vendor", "model": "S-1"})
	manual := c.seed(t, c.server, "imp-manual", "manual", map[string]any{"serial_number": "IMP-MAN", "manufacturer": "Manual-Vendor"})
	inst := c.seed(t, c.server, "imp-inst", "snmp", map[string]any{"serial_number": "IMP-INST"})
	if _, err := c.f.Admin.Exec(bg, `INSERT INTO ci_instance_field_definition (organization_id, ci_id, name, data_type) VALUES ($1, $2, 'imp_inst', 'string')`,
		c.f.OrgA, inst); err != nil {
		t.Fatal(err)
	}
	linked := c.seed(t, c.server, "imp-asset", "snmp", map[string]any{"hardware_uuid": "4c4c4544-0000-4000-8000-00000000b001", "serial_number": "IMP-ASSET"})
	if _, err := c.f.Admin.Exec(bg, `UPDATE ci SET asset_id = $2 WHERE id = $1`, linked, c.f.Asset(t, c.f.OrgA, "", "IMP-ASSET-TAG")); err != nil {
		t.Fatal(err)
	}
	snmpVersion := c.column(t, snmp, "version")

	header := "ci_type;name;serial_number;hardware_uuid;manufacturer;model;attributes.imp_inst;location_id;observed_at"
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)
	res, report := c.run(t, "both", false, header,
		"server;;IMP-PIN;;;M-Other;;;",                                                // 2: override kept, conflict
		"server;;IMP-SNMP;;Import-Vendor;S-2;;;",                                      // 3: over snmp
		"server;;IMP-MAN;;Import-Vendor;;;;",                                          // 4: under manual
		"server;;IMP-INST;;;;value;;",                                                 // 5: instance attribute
		"server;;IMP-ASSET-NEW;4C4C4544-0000-4000-8000-00000000B001;;;;"+c.f.ID()+";", // 6: asset-owned
		"server;imp-new;IMP-NEW;;New-Vendor;N-1;;;",                                   // 7: new CI
		"server;imp-future;IMP-FUTURE;;;;;;"+future,                                   // 8: observed in the future
	)
	for _, want := range []struct {
		line        int
		field, code string
	}{
		{2, "model", CodeOverrideKept}, {3, "", "updated"}, {4, "manufacturer", CodeLowerRank}, {5, "attributes.imp_inst", CodeInstanceField},
		{6, "serial_number", CodeAssetCanonical}, {6, "location_id", CodeAssetCanonical}, {7, "", "created"},
		{8, "observed_at", CodeObservedAtInFuture},
	} {
		if !hasLine(report, want.line, want.field, want.code) {
			t.Errorf("report lacks line %d %s %s:\n%v", want.line, want.field, want.code, report)
		}
	}
	if res.Failed != 1 || res.Created != 1 {
		t.Errorf("result %+v", res)
	}
	if got := c.column(t, pinned, "model"); got == "M-Other" {
		t.Error("the import overwrote an override")
	}
	if n := c.count(t, `SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'override_conflict' AND object_key = $2`,
		c.f.OrgA, "ci:"+pinned+"/model"); n != 1 {
		t.Errorf("%d override_conflict reviews", n)
	}
	if got := c.column(t, snmp, "manufacturer"); got != "Import-Vendor" {
		t.Errorf("snmp value: %q, want the imported one", got)
	}
	if got := c.column(t, manual, "manufacturer"); got != "Manual-Vendor" {
		t.Errorf("manual value: %q, want it kept", got)
	}
	if got := c.column(t, inst, "attributes->>'imp_inst'"); got != "" {
		t.Errorf("instance attribute written: %q", got)
	}
	if got := c.column(t, linked, "serial_number"); got != "IMP-ASSET" {
		t.Errorf("serial number of the asset-linked CI: %q", got)
	}
	// Version and history: one authoritative write, one ci_change per
	// changed field with the new version, one audit entry and one event
	// with source import.
	if got := c.column(t, snmp, "version"); got == snmpVersion {
		t.Errorf("version stayed %s", got)
	}
	version := c.column(t, snmp, "version")
	if n := c.count(t, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND ci_version = $2::bigint`, snmp, version); n != 2 {
		t.Errorf("%d ci_change rows of version %s, want 2 (manufacturer, model)", n, version)
	}
	if n := c.count(t, `SELECT count(*) FROM audit_log WHERE resource_id = $1 AND source = 'import'`, snmp); n != 1 {
		t.Errorf("%d import audit entries of the CI", n)
	}
	if n := c.count(t, `SELECT count(*) FROM event_outbox WHERE entity_id = $1 AND type = 'ci.changed'`, snmp); n != 1 {
		t.Errorf("%d ci.changed events", n)
	}
	var created string
	if err := c.f.Admin.QueryRow(bg, `SELECT id::text FROM ci WHERE organization_id = $1 AND serial_number = 'IMP-NEW'`, c.f.OrgA).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if c.column(t, created, "discovery_source") != "import" || c.column(t, created, "is_manual") != "true" {
		t.Errorf("new CI: source %s, manual %s", c.column(t, created, "discovery_source"), c.column(t, created, "is_manual"))
	}
	if n := c.count(t, `SELECT count(*) FROM ci_change WHERE ci_id = $1 AND change_type = 'create'`, created); n != 1 {
		t.Errorf("%d create changes of the new CI", n)
	}
	if n := c.count(t, `SELECT count(*) FROM ci_field_value WHERE ci_id = $1 AND discovered_source = 'import'`, created); n < 3 {
		t.Errorf("%d fields of the new CI with provenance import", n)
	}
	if n := c.count(t, `SELECT count(*) FROM event_outbox WHERE entity_id = $1 AND type = 'ci.created'`, created); n != 1 {
		t.Errorf("%d ci.created events", n)
	}

	// A later SNMP ingest does not overwrite imported values (rank 80 < 95).
	scope := &discovery.MessageScope{UserID: c.user}
	if _, err := c.disc.ProcessMessages(bg, []discovery.IngestMessage{{OrganizationID: c.f.OrgA, Scope: scope, ObservedAt: time.Now(),
		Item: discovery.IngestItem{CITypeName: "server", Name: "imp-new", SerialNumber: "IMP-NEW", Manufacturer: "SNMP-Later", Source: "snmp"}}}); err != nil {
		t.Fatal(err)
	}
	if got := c.column(t, created, "manufacturer"); got != "New-Vendor" {
		t.Errorf("a later SNMP ingest overwrote the imported value: %q", got)
	}

	// A field the starter may not write refuses the row.
	if _, err := c.f.Admin.Exec(bg, `INSERT INTO field_grant (organization_id, role_id, object_kind, field_key, level) VALUES ($1, $2, 'ci', 'core.model', 'read')`,
		c.f.OrgA, c.role); err != nil {
		t.Fatal(err)
	}
	c.writer(nil)
	res, report = c.run(t, "both", false, "ci_type;serial_number;model", "server;IMP-SNMP;S-3")
	if res.Failed != 1 || !hasLine(report, 2, "model", CodeFieldForbidden) || c.column(t, snmp, "model") != "S-2" {
		t.Errorf("field without write right: %+v %v", res, report)
	}
}

// TestImportCIConcurrencyAndLimit covers the locks and the admission of CI
// rows (WP-302, E-56, K21, PRI-10, ENT-03): an import and an ingest of the
// same device at the same time give one CI (identity lock on management IP
// and sysObjectID, which no unique index guards), and an import at max_cis
// creates exactly as many CIs as allowed, the rest with entitlement-limit.
func TestImportCIConcurrencyAndLimit(t *testing.T) {
	c := newPGCI(t)
	bg := context.Background()
	scope := &discovery.MessageScope{UserID: c.user}
	for round := range 3 {
		ip := fmt.Sprintf("10.78.0.%d", round+1)
		// The job runs in the background while the ingest processes the
		// same device.
		jobID := c.launch(t, "both", false, "ci_type;name;management_ip;sys_object_id", fmt.Sprintf("switch;imp-race-%d;%s;1.3.6.1.4.1.9.1.1", round, ip))
		if _, err := c.disc.ProcessMessages(bg, []discovery.IngestMessage{{OrganizationID: c.f.OrgA, Scope: scope, ObservedAt: time.Now(),
			Item: discovery.IngestItem{CITypeName: "switch", Name: fmt.Sprintf("ing-race-%d", round), ManagementIP: ip,
				SysObjectID: "1.3.6.1.4.1.9.1.1", Source: "snmp"}}}); err != nil {
			t.Error(err)
		}
		c.finish(t, jobID)
		if n := c.count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND management_ip = $2 AND deleted_at IS NULL`, c.f.OrgA, ip); n != 1 {
			t.Errorf("round %d: %d CIs of one device, want 1", round, n)
		}
	}

	limit := int64(c.liveCIs(t) + 3)
	c.writer(fixedLimit{n: limit})
	rows := make([]string, 5)
	for i := range rows {
		rows[i] = fmt.Sprintf("server;imp-limit-%d;IMP-LIMIT-%d", i, i)
	}
	res, report := c.run(t, "both", false, "ci_type;name;serial_number", rows...)
	if res.Created != 3 || res.Failed != 2 || int64(c.liveCIs(t)) != limit {
		t.Errorf("at max_cis: %+v, %d CIs, limit %d", res, c.liveCIs(t), limit)
	}
	if !hasLine(report, 5, "", CodeEntitlementLimit) || !hasLine(report, 6, "", CodeEntitlementLimit) {
		t.Errorf("report: %v", report)
	}
}

// TestImportCIThousandRows imports 1,000 new CIs in one run (WP-302) and
// updates them in a second.
func TestImportCIThousandRows(t *testing.T) {
	c := newPGCI(t)
	rows := make([]string, 1000)
	for i := range rows {
		rows[i] = fmt.Sprintf("server;imp-bulk-%04d;IMP-BULK-%04d;10.79.%d.%d", i, i, i/250, i%250+1)
	}
	started := time.Now()
	res, _ := c.run(t, "both", false, "ci_type;name;serial_number;management_ip", rows...)
	if res.Created != 1000 || res.Failed != 0 {
		t.Fatalf("result %+v", res)
	}
	t.Logf("1,000 new CIs in %s", time.Since(started))
	for i := range rows {
		rows[i] = fmt.Sprintf("server;imp-bulk-%04d;IMP-BULK-%04d;10.80.%d.%d", i, i, i/250, i%250+1)
	}
	res, _ = c.run(t, "update_existing", false, "ci_type;name;serial_number;management_ip", rows...)
	if res.Updated != 1000 {
		t.Fatalf("second run %+v", res)
	}
}
