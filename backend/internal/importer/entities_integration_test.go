package importer

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contract"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
)

// entityPerms are the rights of the import user of the entity tests.
var entityPerms = []identity.Permission{identity.PermImportRun, identity.PermLocationWrite, identity.PermCIWrite,
	identity.PermAssetWrite, identity.PermContractWrite, identity.PermStockWrite, identity.PermRelationshipWrite}

// pgEntities is the import stack of the CI tests with the writers of
// assets, contracts, consumables and relationships (WP-776) as the router
// wires them, and the CI writer with relation columns.
type pgEntities struct {
	*pgCI
}

func newPGEntities(t *testing.T) *pgEntities {
	t.Helper()
	c := newPGCI(t)
	perms, err := json.Marshal(entityPerms)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.f.Admin.Exec(context.Background(), `UPDATE role SET permissions = $2::jsonb WHERE id = $1`, c.role, string(perms)); err != nil {
		t.Fatal(err)
	}
	policies := permission.NewFieldResolver(permission.NewPGRepository(c.f.App), nil, permission.NewPGFieldGrants(c.f.App), nil)
	svc := ci.NewServiceWithLimits(c.cis, nil).WithFieldResolver(c.types)
	rel := NewRelationshipWriter(relationship.NewPGRepository(c.f.App), c.profile, policies).WithIdentities(c.disc)
	c.h.WithWriter(NewAssetWriter(asset.NewPGRepository(c.f.App), c.profile, policies).WithCILinks(svc)).
		WithWriter(NewContractWriter(contract.NewPGContracts(c.f.App), c.profile, policies)).
		WithWriter(NewConsumableWriter(consumable.NewPGRepository(c.f.App), c.profile)).
		WithWriter(rel).
		WithWriter(NewCIWriter(svc, c.disc, c.ovr, policies).WithInstanceFields(c.types).WithFields(c.types).
			WithLocations(c.locs).WithRelations(rel))
	return &pgEntities{pgCI: c}
}

// runEntity imports the CSV rows into entity with a profile of mode that
// maps every column of header onto the target of its name.
func (e *pgEntities) runEntity(t *testing.T, entity, mode string, dryRun bool, header string, rows ...string) (JobResult, [][]string) {
	t.Helper()
	return e.finish(t, e.launchEntity(t, entity, mode, dryRun, header, rows...))
}

func (e *pgEntities) launchEntity(t *testing.T, entity, mode string, dryRun bool, header string, rows ...string) string {
	t.Helper()
	cols := map[string]ColumnMapping{}
	for _, target := range strings.Split(header, ";") {
		cols[target] = ColumnMapping{Target: target}
	}
	p := &Profile{OrganizationID: e.f.OrgA, Name: entity + "-" + e.f.ID(), Entity: entity, Format: FormatCSV, Mode: mode,
		Mapping: Mapping{Columns: cols}}
	if err := e.profile.Create(e.f.OrgCtx(e.f.OrgA), p); err != nil {
		t.Fatalf("profile: %v", err)
	}
	docID := e.document(t, header+"\n"+strings.Join(rows, "\n")+"\n")
	return e.start(t, requestContextWith(e.f.OrgA, e.user, nil, entityPerms...), docID, p.ID, dryRun)
}

// assetID returns the id of the asset with the inventory number, "" when
// there is none.
func (e *pgEntities) assetID(t *testing.T, inventoryNo string) string {
	t.Helper()
	var id string
	if err := e.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(max(id::text), '') FROM asset WHERE organization_id = $1 AND inventory_no = $2`,
		e.f.OrgA, inventoryNo).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func (e *pgEntities) text(t *testing.T, query string, args ...any) string {
	t.Helper()
	var v string
	if err := e.f.Admin.QueryRow(context.Background(), query, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return v
}

// audited counts the audit entries of a resource with action, source
// import and the import user as actor; all counts every entry of it.
func (e *pgEntities) audited(t *testing.T, resourceType, id, action string) (imported, all int) {
	t.Helper()
	imported = e.count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type = $2 AND resource_id = $3
		AND action = $4 AND source = 'import' AND actor_id = $5::uuid AND actor_type = 'user'`, e.f.OrgA, resourceType, id, action, e.user)
	all = e.count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type = $2 AND resource_id = $3`,
		e.f.OrgA, resourceType, id)
	return imported, all
}

// wantResult compares the counters of a run.
func wantResult(t *testing.T, got JobResult, created, updated, skipped, failedRows int, report [][]string) {
	t.Helper()
	if got.Created != created || got.Updated != updated || got.Skipped != skipped || got.Failed != failedRows {
		t.Fatalf("result %+v, want created %d updated %d skipped %d failed %d; report %v", got, created, updated, skipped, failedRows, report)
	}
}

// TestImportAssets covers asset rows (WP-776, IMP-IO-01): the identity is
// the inventory number, without one the serial number (two assets with it:
// ambiguous_identity); a taken barcode is a field error, the location is
// read_only_field while the other fields are written, the modes decide
// creation and change, and every written row has exactly one audit entry
// with source import and the starter as actor.
func TestImportAssets(t *testing.T) {
	e := newPGEntities(t)
	header := "inventory_no;name;serial_number;barcode;location_id"
	got, report := e.runEntity(t, "asset", "both", false, header,
		"IMP-INV-1;Laptop 1;IMP-SN-1;IMP-BC-1;",
		"IMP-INV-2;Laptop 2;IMP-SN-DUP;;",
		"IMP-INV-3;Laptop 3;IMP-SN-DUP;;",
		"IMP-INV-5;Laptop 5;;IMP-BC-1;",
		"IMP-INV-4;Laptop 4;;;"+e.f.ID())
	wantResult(t, got, 4, 0, 0, 1, report)
	if !hasLine(report, 5, "barcode", CodeUniqueTaken) || e.assetID(t, "IMP-INV-5") != "" {
		t.Fatalf("duplicate barcode: %v", report)
	}
	inv4 := e.assetID(t, "IMP-INV-4")
	if !hasLine(report, 6, "location_id", CodeReadOnlyField) || inv4 == "" {
		t.Fatalf("location column: %v", report)
	}
	if loc := e.text(t, `SELECT COALESCE(location_id::text, '') FROM asset WHERE id = $1`, inv4); loc != "" {
		t.Fatalf("location written: %q", loc)
	}
	inv1 := e.assetID(t, "IMP-INV-1")
	for _, inv := range []string{"IMP-INV-1", "IMP-INV-2", "IMP-INV-3", "IMP-INV-4"} {
		if imported, all := e.audited(t, "asset", e.assetID(t, inv), "asset.created"); imported != 1 || all != 1 {
			t.Fatalf("audit of %s: %d by the import, %d in all", inv, imported, all)
		}
	}

	// update_existing: by serial number without inventory number, by
	// inventory number; a serial number of two assets is ambiguous, an
	// unknown inventory number is skipped.
	got, report = e.runEntity(t, "asset", "update_existing", false, "inventory_no;name;serial_number",
		";Laptop 1 per SN;IMP-SN-1",
		";Twin;IMP-SN-DUP",
		"IMP-INV-2;Laptop 2 neu;",
		"IMP-INV-9;Neu;")
	wantResult(t, got, 0, 2, 1, 1, report)
	if !hasLine(report, 3, "serial_number", CodeAmbiguousIdentity) || !hasLine(report, 5, "inventory_no", CodeNoIdentityMatch) {
		t.Fatalf("report %v", report)
	}
	if name := e.text(t, `SELECT name FROM asset WHERE id = $1`, inv1); name != "Laptop 1 per SN" {
		t.Fatalf("name of IMP-INV-1 %q", name)
	}
	if name := e.text(t, `SELECT name FROM asset WHERE inventory_no = 'IMP-INV-2' AND organization_id = $1`, e.f.OrgA); name != "Laptop 2 neu" {
		t.Fatalf("name of IMP-INV-2 %q", name)
	}
	if imported, all := e.audited(t, "asset", inv1, "asset.updated"); imported != 1 || all != 2 {
		t.Fatalf("audit of the update: %d by the import, %d in all", imported, all)
	}

	// create_missing: an existing asset stays, a new one is created; the
	// dry run reports the same and writes nothing.
	for _, dry := range []bool{true, false} {
		got, report = e.runEntity(t, "asset", "create_missing", dry, "inventory_no;name", "IMP-INV-1;Anders", "IMP-INV-6;Laptop 6")
		wantResult(t, got, 1, 0, 1, 0, report)
		if exists := e.assetID(t, "IMP-INV-6") != ""; exists == dry {
			t.Fatalf("dry run %t: asset created %t", dry, exists)
		}
	}
	if name := e.text(t, `SELECT name FROM asset WHERE id = $1`, inv1); name != "Laptop 1 per SN" {
		t.Fatalf("create_missing changed IMP-INV-1: %q", name)
	}
	if n := e.count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1 AND resource_type = 'asset' AND action = 'asset.created'`,
		e.f.OrgA); n != 5 {
		t.Fatalf("%d asset.created entries, want 5 (the dry run writes none)", n)
	}
}

// TestImportAssetPrimaryCI: the primary CI of an asset row is linked
// through the CI domain (WP-272); an unknown CI is reference_not_found and
// the row is not written.
func TestImportAssetPrimaryCI(t *testing.T) {
	e := newPGEntities(t)
	ciID := e.seed(t, e.server, "imp-asset-ci", "manual", nil)
	got, report := e.runEntity(t, "asset", "both", false, "inventory_no;name;primary_ci_id",
		"IMP-INV-CI-1;Server;"+ciID,
		"IMP-INV-CI-2;Server 2;"+e.f.ID())
	wantResult(t, got, 1, 0, 0, 1, report)
	id := e.assetID(t, "IMP-INV-CI-1")
	if primary := e.text(t, `SELECT COALESCE(ci_id::text, '') FROM asset WHERE id = $1`, id); primary != ciID {
		t.Fatalf("primary CI %q, want %s", primary, ciID)
	}
	if linked := e.column(t, ciID, "asset_id"); linked != id {
		t.Fatalf("ci.asset_id %q, want %s", linked, id)
	}
	if !hasLine(report, 3, "primary_ci_id", CodeReferenceNotFound) || e.assetID(t, "IMP-INV-CI-2") != "" {
		t.Fatalf("unknown CI: %v", report)
	}
}

// TestImportAssetModuleDisabled: with the module of /api/v1/assets
// (inventory) switched off the run ends before its first row.
func TestImportAssetModuleDisabled(t *testing.T) {
	e := newPGEntities(t)
	e.h.WithGates(denyPaths{"/api/v1/assets": true}, nil)
	jobID := e.launchEntity(t, "asset", "both", false, "inventory_no;name", "IMP-INV-OFF;Aus")
	job := e.wait(t, jobID)
	if job.Status != jobs.StatusFailed || errorCode(job.Error) != CodeModuleDisabled {
		t.Fatalf("job %s %q, want %s", job.Status, job.Error, CodeModuleDisabled)
	}
	if e.assetID(t, "IMP-INV-OFF") != "" {
		t.Fatal("asset written with the module switched off")
	}
}

// denyPaths switches off the modules of the paths.
type denyPaths map[string]bool

func (d denyPaths) RouteAllowed(pattern string) bool { return !d[pattern] }

// TestImportContractsAndConsumables: contracts by number and consumables
// by SKU are created and changed by mode, the stock of a consumable is
// read_only_field, and every written row has one audit entry.
func TestImportContractsAndConsumables(t *testing.T) {
	e := newPGEntities(t)
	got, report := e.runEntity(t, "contract", "both", false, "number;kind;status;notes", "IMP-V-1;support;active;erster", "IMP-V-2;lease;draft;")
	wantResult(t, got, 2, 0, 0, 0, report)
	v1 := e.text(t, `SELECT id::text FROM contract WHERE organization_id = $1 AND number = 'IMP-V-1'`, e.f.OrgA)
	got, report = e.runEntity(t, "contract", "update_existing", false, "number;status;notes", "IMP-V-1;terminated;gekündigt",
		"IMP-V-2;draft;", "IMP-V-3;active;")
	wantResult(t, got, 0, 1, 2, 0, report)
	if status := e.text(t, `SELECT status || '/' || notes FROM contract WHERE id = $1`, v1); status != "terminated/gekündigt" {
		t.Fatalf("contract %q", status)
	}
	if e.count(t, `SELECT count(*) FROM contract WHERE organization_id = $1 AND number = 'IMP-V-3'`, e.f.OrgA) != 0 {
		t.Fatal("update_existing created a contract")
	}
	for action, want := range map[string]int{"contract.created": 1, "contract.updated": 1} {
		if imported, _ := e.audited(t, "contract", v1, action); imported != want {
			t.Fatalf("%s of IMP-V-1: %d", action, imported)
		}
	}

	got, report = e.runEntity(t, "consumable", "both", false, "sku;name;unit;min_level;stock_level", "IMP-SKU-1;Patchkabel;pcs;5;12")
	wantResult(t, got, 1, 0, 0, 0, report)
	if !hasLine(report, 2, "stock_level", CodeReadOnlyField) {
		t.Fatalf("stock column: %v", report)
	}
	sku := e.text(t, `SELECT id::text FROM consumable WHERE organization_id = $1 AND sku = 'IMP-SKU-1'`, e.f.OrgA)
	if n := e.count(t, `SELECT count(*) FROM stock_level WHERE consumable_id = $1`, sku); n != 0 {
		t.Fatalf("%d stock rows written", n)
	}
	got, report = e.runEntity(t, "consumable", "both", false, "sku;name;min_level", "IMP-SKU-1;Patchkabel Cat6;8", "IMP-SKU-1;Patchkabel Cat6;8")
	wantResult(t, got, 0, 1, 1, 0, report)
	if v := e.text(t, `SELECT name || '/' || (min_level = 8)::text FROM consumable WHERE id = $1`, sku); v != "Patchkabel Cat6/true" {
		t.Fatalf("consumable %q", v)
	}
	got, report = e.runEntity(t, "consumable", "create_missing", false, "sku;name", "IMP-SKU-1;Anders")
	wantResult(t, got, 0, 0, 1, 0, report)
	for action, want := range map[string]int{"consumable.created": 1, "consumable.updated": 1} {
		if imported, _ := e.audited(t, "consumable", sku, action); imported != want {
			t.Fatalf("%s of IMP-SKU-1: %d", action, imported)
		}
	}
}

// TestImportRelationships: an edge is found by source, target and type
// (an end by id or by serial number), created with source import, skipped
// when it exists (idempotent) and its notes changed in update_existing; a
// relation column of a CI row writes the edge after the CI, and a failing
// edge fails the whole row.
func TestImportRelationships(t *testing.T) {
	e := newPGEntities(t)
	app := e.seed(t, e.server, "imp-rel-app", "manual", nil)
	db := e.seed(t, e.server, "imp-rel-db", "manual", map[string]any{"serial_number": "IMP-REL-SN-DB"})
	header := "source_ci_id;target_serial_number;rel_type;notes"
	got, report := e.runEntity(t, "relationship", "both", false, header, app+";IMP-REL-SN-DB;depends_on;")
	wantResult(t, got, 1, 0, 0, 0, report)
	edges := `SELECT count(*) FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2 AND rel_type = 'depends_on' AND source = 'import'`
	if n := e.count(t, edges, app, db); n != 1 {
		t.Fatalf("%d edges", n)
	}
	edge := e.text(t, `SELECT id::text FROM ci_relationship WHERE source_ci_id = $1 AND target_ci_id = $2`, app, db)
	got, report = e.runEntity(t, "relationship", "both", false, header, app+";IMP-REL-SN-DB;depends_on;")
	wantResult(t, got, 0, 0, 1, 0, report)
	got, report = e.runEntity(t, "relationship", "update_existing", false, header, app+";IMP-REL-SN-DB;depends_on;geprüft",
		app+";IMP-REL-SN-NONE;depends_on;")
	wantResult(t, got, 0, 1, 0, 1, report)
	if !hasLine(report, 3, "target_ci_id", CodeReferenceNotFound) {
		t.Fatalf("unknown target: %v", report)
	}
	if n := e.count(t, edges, app, db); n != 1 || e.text(t, `SELECT notes FROM ci_relationship WHERE id = $1`, edge) != "geprüft" {
		t.Fatalf("%d edges after the update", n)
	}
	for action, want := range map[string]int{"relationship.created": 1, "relationship.updated": 1} {
		if imported, _ := e.audited(t, "relationship", edge, action); imported != want {
			t.Fatalf("%s: %d", action, imported)
		}
	}

	// Relation column of a CI row.
	got, report = e.run(t, "both", false, "name;ci_type;serial_number;relations.depends_on",
		"imp-rel-web;server;IMP-REL-SN-WEB;"+db,
		"imp-rel-bad;server;IMP-REL-SN-BAD;"+e.f.ID())
	wantResult(t, got, 1, 0, 0, 1, report)
	web := e.text(t, `SELECT id::text FROM ci WHERE organization_id = $1 AND name = 'imp-rel-web'`, e.f.OrgA)
	if n := e.count(t, edges, web, db); n != 1 {
		t.Fatalf("relation column: %d edges", n)
	}
	if !hasLine(report, 3, "relations.depends_on", CodeReferenceNotFound) {
		t.Fatalf("failing relation: %v", report)
	}
	if n := e.count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND name = 'imp-rel-bad'`, e.f.OrgA); n != 0 {
		t.Fatal("the CI of a row with a failing relation was written")
	}
}
