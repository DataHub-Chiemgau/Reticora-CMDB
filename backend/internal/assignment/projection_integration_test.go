package assignment_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

// projectionMigration is the takeover of the history and the projection
// assignment_projection (WP-747).
const projectionMigration = "../../migrations/000257_assignment_projection"

// readProjectionMigration returns the up and the down part of 000257.
func readProjectionMigration(t *testing.T) (up, down string) {
	t.Helper()
	rawUp, err := os.ReadFile(projectionMigration + ".up.sql")
	if err != nil {
		t.Fatal(err)
	}
	rawDown, err := os.ReadFile(projectionMigration + ".down.sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(rawUp), string(rawDown)
}

// projectedAssignment is a row of assignment_projection.
type projectedAssignment struct {
	id, status, kind, user, condition, due string
	returned                               *time.Time
}

// ledgerMovement is an assignment or return movement of an asset.
type ledgerMovement struct {
	id, kind, user, actor, reason, notes string
	at                                   time.Time
	due                                  *time.Time
}

// TestAssignmentProjectionMigration covers the takeover of the history in
// 000257 (AST-02, AST-07, WP-747) in a rolled-back transaction: after the
// down part, rows of assignment from before WP-275 (returned, transferred,
// cancelled, two open rows of one asset, rows with only a ci_id with and
// without asset, a user of another organization) and best-effort movements
// (holderless movements of asset_movement, the copies of 000242, a return
// of the assignment service) get the up part. Expected: one assignment
// movement per row with its id, actor, notes and due_at, at most one
// return with the holder, no duplicates, the document link moved to the
// replacing return, the older open assignment superseded, the rows without
// asset or with a foreign user quarantined (without signatures) and kept in
// assignment; the projection equals the ledger (active, returned,
// transferred, overdue, transfer). Down and up again book nothing twice.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssignmentProjectionMigration(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	bg := context.Background()
	up, down := readProjectionMigration(t)
	alice := f.AppUser(t, f.OrgA, "wp747alice")
	bob := f.AppUser(t, f.OrgA, "wp747bob")
	stranger := f.AppUser(t, f.OrgB, "wp747stranger")
	asset := func(name string) string { return f.Asset(t, f.OrgA, f.Client1, "wp747-"+name) }
	aRet, aTrans, aCancel, aTwo, aViaCI, aDup, aCopy, aSvc, aKept, aForeign :=
		asset("ret"), asset("trans"), asset("cancel"), asset("two"), asset("viaci"), asset("dup"), asset("copy"),
		asset("svc"), asset("kept"), asset("foreign")
	ciWithAsset, ciAlone := f.CI(t, f.OrgA, f.Client1, "wp747-ci-asset"), f.CI(t, f.OrgA, f.Client1, "wp747-ci-alone")

	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	exec := func(query string, args ...any) {
		t.Helper()
		if _, execErr := tx.Exec(bg, query, args...); execErr != nil {
			t.Fatalf("%.70s...: %v", strings.TrimSpace(query), execErr)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if scanErr := tx.QueryRow(bg, query, args...).Scan(&n); scanErr != nil {
			t.Fatalf("%s: %v", query, scanErr)
		}
		return n
	}
	exec(down)
	exec(`UPDATE organization SET timezone = 'Europe/Berlin' WHERE id = $1`, f.OrgA)
	exec(`UPDATE ci SET asset_id = $2 WHERE id = $1`, ciWithAsset, aViaCI)

	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	at := func(d time.Duration) time.Time { return base.Add(d) }
	type legacy struct {
		id, asset, ci, to, by, status, condition, due, notes string
		assigned                                             time.Time
		returned                                             *time.Time
		updated                                              time.Time
	}
	ptr := func(v time.Time) *time.Time { return &v }
	insert := func(r *legacy) {
		t.Helper()
		exec(`INSERT INTO assignment (id, organization_id, asset_id, ci_id, assigned_to, assigned_by, status, assigned_at,
				returned_at, return_condition, due_date, notes, checkout_signature, created_at, updated_at)
			VALUES ($1, $2, NULLIF($3, '')::uuid, NULLIF($4, '')::uuid, $5, $6, $7, $8, $9, NULLIF($10, ''), NULLIF($11, '')::date,
				NULLIF($12, ''), '{"signer_name": "Sig Legacy", "method": "typed"}'::jsonb, $8, $13)`,
			r.id, f.OrgA, r.asset, r.ci, r.to, r.by, r.status, r.assigned, r.returned, r.condition, r.due, r.notes, r.updated)
	}
	book := func(id, assetID, kind, holder, notes string, when time.Time) {
		t.Helper()
		exec(`INSERT INTO stock_movement (id, organization_id, item_kind, asset_id, movement_type, assignee_user_id, occurred_at, notes, created_at)
			VALUES ($1, $2, 'asset', $3, $4, NULLIF($5, '')::uuid, $6, NULLIF($7, ''), $6)`, id, f.OrgA, assetID, kind, holder, when, notes)
	}

	rRet := &legacy{id: f.ID(), asset: aRet, to: alice, by: bob, status: "returned", condition: "ok", due: "2026-03-10", notes: "laptop",
		assigned: base, returned: ptr(at(time.Hour)), updated: at(time.Hour)}
	rT1 := &legacy{id: f.ID(), asset: aTrans, to: alice, by: bob, status: "transferred", assigned: base, updated: at(5 * time.Hour)}
	rT2 := &legacy{id: f.ID(), asset: aTrans, to: bob, by: bob, status: "active", assigned: at(2 * time.Hour), updated: at(2 * time.Hour)}
	rCancel := &legacy{id: f.ID(), asset: aCancel, to: alice, by: bob, status: "cancelled", assigned: base, updated: at(30 * time.Minute)}
	rOld := &legacy{id: f.ID(), asset: aTwo, to: alice, by: bob, status: "active", assigned: base, updated: base}
	rNew := &legacy{id: f.ID(), asset: aTwo, to: bob, by: bob, status: "overdue", due: "2026-03-02", assigned: at(time.Hour), updated: at(time.Hour)}
	rViaCI := &legacy{id: f.ID(), ci: ciWithAsset, to: alice, by: bob, status: "active", assigned: base, updated: base}
	rAlone := &legacy{id: f.ID(), ci: ciAlone, to: alice, by: bob, status: "active", assigned: base, updated: base}
	rForeign := &legacy{id: f.ID(), asset: aForeign, to: stranger, by: bob, status: "active", assigned: base, updated: base}
	rDup := &legacy{id: f.ID(), asset: aDup, to: alice, by: bob, status: "returned", assigned: base, returned: ptr(at(time.Hour)), updated: at(time.Hour)}
	rCopy := &legacy{id: f.ID(), asset: aCopy, to: alice, by: bob, status: "returned", notes: "copied", assigned: base,
		returned: ptr(at(time.Hour)), updated: at(time.Hour)}
	rSvc := &legacy{id: f.ID(), asset: aSvc, to: alice, by: bob, status: "returned", assigned: base, returned: ptr(at(time.Hour)), updated: at(time.Hour)}
	rKept := &legacy{id: f.ID(), asset: aKept, to: alice, by: bob, status: "returned", assigned: base, returned: ptr(at(3 * time.Hour)),
		updated: at(3 * time.Hour)}
	for _, r := range []*legacy{rRet, rT1, rT2, rCancel, rOld, rNew, rViaCI, rAlone, rForeign, rDup, rCopy, rSvc, rKept} {
		insert(r)
	}
	// The holder cache of 000254: the newest open row.
	exec(`UPDATE asset SET assigned_user_id = $2 WHERE id = $1`, aTwo, bob)

	// Best-effort movements of asset_movement without holder (within 60 s),
	// with a document link on the return.
	mDupA, mDupR := f.ID(), f.ID()
	book(mDupA, aDup, "assignment", "", "assigned", at(10*time.Second))
	book(mDupR, aDup, "return", "", "assignment returned", at(time.Hour+20*time.Second))
	var doc string
	if err = tx.QueryRow(bg, `INSERT INTO document (organization_id, title, file_name, uploaded_by, storage_key)
		VALUES ($1, 'wp747', 'wp747.pdf', $2, '') RETURNING id::text`, f.OrgA, bob).Scan(&doc); err != nil {
		t.Fatal(err)
	}
	exec(`INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'stock_movement', $3)`, f.OrgA, doc, mDupR)
	// The copies of 000242 (notes assignment:<id>).
	book(f.ID(), aCopy, "assignment", alice, "assignment:"+rCopy.id, base)
	book(f.ID(), aCopy, "return", "", "assignment:"+rCopy.id, at(time.Hour))
	// A transition row of the assignment service with its movements.
	svcReturn := f.ID()
	book(rSvc.id, aSvc, "assignment", alice, "", base)
	book(svcReturn, aSvc, "return", alice, "", at(time.Hour))
	// The return of the service that closed a legacy row.
	keptReturn := f.ID()
	book(keptReturn, aKept, "return", alice, "back", at(3*time.Hour))

	exec(up)

	ledger := func(assetID string) []ledgerMovement {
		t.Helper()
		rows, qerr := tx.Query(bg, `SELECT id::text, movement_type, COALESCE(assignee_user_id::text, ''), COALESCE(actor_id::text, ''),
				COALESCE(reason, ''), COALESCE(notes, ''), occurred_at, due_at
			FROM stock_movement WHERE asset_id = $1 ORDER BY occurred_at, movement_type, id`, assetID)
		if qerr != nil {
			t.Fatal(qerr)
		}
		out, cerr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ledgerMovement, error) {
			var m ledgerMovement
			return m, row.Scan(&m.id, &m.kind, &m.user, &m.actor, &m.reason, &m.notes, &m.at, &m.due)
		})
		if cerr != nil {
			t.Fatal(cerr)
		}
		return out
	}
	projected := func(assetID string) []projectedAssignment {
		t.Helper()
		rows, qerr := tx.Query(bg, `SELECT id::text, status, assignment_type, COALESCE(assigned_to::text, ''),
				COALESCE(return_condition, ''), COALESCE(to_char(due_date, 'YYYY-MM-DD'), ''), returned_at
			FROM assignment_projection WHERE asset_id = $1 ORDER BY assigned_at, id`, assetID)
		if qerr != nil {
			t.Fatal(qerr)
		}
		out, cerr := pgx.CollectRows(rows, func(row pgx.CollectableRow) (projectedAssignment, error) {
			var p projectedAssignment
			return p, row.Scan(&p.id, &p.status, &p.kind, &p.user, &p.condition, &p.due, &p.returned)
		})
		if cerr != nil {
			t.Fatal(cerr)
		}
		return out
	}
	// returnOf is the one return movement of the ledger.
	returnOf := func(l []ledgerMovement) ledgerMovement {
		t.Helper()
		var out []ledgerMovement
		for _, m := range l {
			if m.kind == "return" {
				out = append(out, m)
			}
		}
		if len(out) != 1 {
			t.Fatalf("return movements = %+v, want one", l)
		}
		return out[0]
	}

	// Returned: the assignment with id, actor, notes and due_at (end of the
	// day in Berlin), the return with holder and condition.
	l := ledger(aRet)
	endOfDay := time.Date(2026, 3, 10, 22, 59, 59, 999999000, time.UTC)
	if len(l) != 2 || l[0].id != rRet.id || l[0].kind != "assignment" || l[0].user != alice || l[0].actor != bob || l[0].notes != "laptop" ||
		!l[0].at.Equal(base) || l[0].due == nil || !l[0].due.Equal(endOfDay) {
		t.Fatalf("ledger of the returned row = %+v", l)
	}
	if r := returnOf(l); r.user != alice || r.reason != "ok" || !r.at.Equal(at(time.Hour)) {
		t.Fatalf("return of the returned row = %+v", r)
	}
	if p := projected(aRet); len(p) != 1 || p[0].id != rRet.id || p[0].status != "returned" || p[0].kind != "assignment" ||
		p[0].condition != "ok" || p[0].due != "2026-03-10" || p[0].returned == nil || !p[0].returned.Equal(at(time.Hour)) {
		t.Fatalf("projection of the returned row = %+v", p)
	}

	// Transferred: the return at the following assignment.
	l = ledger(aTrans)
	if r := returnOf(l); r.user != alice || r.reason != "" || !r.at.Equal(at(2*time.Hour)) {
		t.Fatalf("return of the transferred row = %+v", r)
	}
	if p := projected(aTrans); len(p) != 2 || p[0].id != rT1.id || p[0].status != "transferred" || p[0].kind != "assignment" ||
		p[1].id != rT2.id || p[1].status != "active" || p[1].kind != "transfer" || p[1].user != bob {
		t.Fatalf("projection of the transfer = %+v", p)
	}

	// Cancelled: the return at updated_at with reason cancelled.
	if r := returnOf(ledger(aCancel)); r.user != alice || r.reason != "cancelled" || !r.at.Equal(at(30*time.Minute)) {
		t.Fatalf("return of the cancelled row = %+v", r)
	}
	if p := projected(aCancel); len(p) != 1 || p[0].status != "returned" || p[0].condition != "cancelled" {
		t.Fatalf("projection of the cancelled row = %+v", p)
	}

	// Two open rows: the one of the holder cache stays open (overdue), the
	// other is superseded.
	if r := returnOf(ledger(aTwo)); r.user != alice || r.reason != "migration: superseded" {
		t.Fatalf("return of the superseded row = %+v", r)
	}
	if p := projected(aTwo); len(p) != 2 || p[0].id != rOld.id || p[0].status != "returned" || p[0].condition != "migration: superseded" ||
		p[1].id != rNew.id || p[1].status != "overdue" || p[1].due != "2026-03-02" {
		t.Fatalf("projection of the two open rows = %+v", p)
	}

	// A row with only a ci_id takes the asset of its CI.
	if p := projected(aViaCI); len(p) != 1 || p[0].id != rViaCI.id || p[0].status != "active" || p[0].user != alice {
		t.Fatalf("projection of the ci-only row = %+v", p)
	}

	// Best-effort duplicates are replaced, the document link follows the
	// return.
	l = ledger(aDup)
	if len(l) != 2 || l[0].id != rDup.id || l[0].user != alice {
		t.Fatalf("ledger with best-effort duplicates = %+v", l)
	}
	dupReturn := returnOf(l)
	if dupReturn.user != alice || dupReturn.id == mDupR || !dupReturn.at.Equal(at(time.Hour)) {
		t.Fatalf("replacing return = %+v", dupReturn)
	}
	if n := count(`SELECT count(*) FROM stock_movement WHERE id = ANY ($1::uuid[])`, []string{mDupA, mDupR}); n != 0 {
		t.Fatalf("%d replaced duplicates left", n)
	}
	if n := count(`SELECT count(*) FROM document_link WHERE document_id = $1 AND entity_type = 'stock_movement' AND entity_id = $2`, doc, dupReturn.id); n != 1 {
		t.Fatalf("document links on the replacing return = %d, want 1", n)
	}

	// The copies of 000242 are replaced.
	l = ledger(aCopy)
	if len(l) != 2 || l[0].id != rCopy.id || l[0].notes != "copied" || returnOf(l).user != alice || strings.HasPrefix(returnOf(l).notes, "assignment:") {
		t.Fatalf("ledger with the copies of 000242 = %+v", l)
	}

	// The movements of the service stay; a return with the holder is not
	// booked twice.
	if l = ledger(aSvc); len(l) != 2 || l[0].id != rSvc.id || returnOf(l).id != svcReturn {
		t.Fatalf("ledger of the service = %+v", l)
	}
	if l = ledger(aKept); len(l) != 2 || l[0].id != rKept.id || returnOf(l).id != keptReturn {
		t.Fatalf("ledger with the return of the service = %+v", l)
	}
	if p := projected(aKept); len(p) != 1 || p[0].status != "returned" {
		t.Fatalf("projection with the return of the service = %+v", p)
	}

	// Without asset or with a user of another organization: quarantined
	// without signatures, kept in assignment, not in the ledger.
	type quarantined struct{ id, reason string }
	rows, err := tx.Query(bg, `SELECT row_data ->> 'id', reason FROM migration_quarantine
		WHERE migration = '000257' AND source_table = 'assignment' AND NOT row_data ? 'checkout_signature' ORDER BY reason`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (quarantined, error) {
		var q quarantined
		return q, row.Scan(&q.id, &q.reason)
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []quarantined{{rForeign.id, "assignment_foreign_user"}, {rAlone.id, "assignment_without_asset"}}
	if !slices.Equal(got, want) || count(`SELECT count(*) FROM migration_quarantine WHERE migration = '000257'`) != 2 {
		t.Fatalf("quarantine = %+v, want %+v", got, want)
	}
	if n := count(`SELECT count(*) FROM assignment WHERE id = ANY ($1::uuid[])`, []string{rAlone.id, rForeign.id}); n != 2 {
		t.Fatalf("quarantined rows left in assignment: %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM stock_movement WHERE id = ANY ($1::uuid[]) OR asset_id = $2`, []string{rAlone.id, rForeign.id}, aForeign); n != 0 {
		t.Fatalf("quarantined rows in the ledger: %d", n)
	}

	// Every taken-over row has one assignment movement and at most one
	// return.
	assets := []string{aRet, aTrans, aCancel, aTwo, aViaCI, aDup, aCopy, aSvc, aKept}
	movements := count(`SELECT count(*) FROM stock_movement WHERE asset_id = ANY ($1::uuid[])`, assets)
	// 12 assignments (rT2 included) and 7 returns.
	if movements != 19 {
		t.Fatalf("ledger rows of the migrated assets: %d, want 19", movements)
	}
	if n := count(`SELECT count(*) FROM (SELECT asset_id FROM stock_movement WHERE asset_id = ANY ($1::uuid[]) AND movement_type = 'assignment'
			GROUP BY asset_id, assignee_user_id, occurred_at HAVING count(*) > 1) d`, assets); n != 0 {
		t.Fatalf("%d duplicate assignment movements", n)
	}

	// Down and up again: the view comes back, nothing is booked twice.
	exec(down)
	if n := count(`SELECT count(*) FROM migration_quarantine WHERE migration = '000257'`); n != 0 {
		t.Fatalf("quarantine entries after down: %d", n)
	}
	exec(up)
	if n := count(`SELECT count(*) FROM stock_movement WHERE asset_id = ANY ($1::uuid[])`, assets); n != movements {
		t.Fatalf("ledger rows after down and up: %d, want %d", n, movements)
	}
	if n := count(`SELECT count(*) FROM migration_quarantine WHERE migration = '000257'`); n != 2 {
		t.Fatalf("quarantine entries after down and up: %d, want 2", n)
	}
	if p := projected(aTwo); len(p) != 2 || p[1].status != "overdue" {
		t.Fatalf("projection after down and up = %+v", p)
	}
}

// TestAssignmentProjectionListAndOwnership covers the reads of the
// projection (AST-02, AST-07, WP-747) with assignments of the service:
// GET /api/v1/assignments filters by status, holder, actor, asset and
// notes, sorts by created_at and due_date and pages with the cursor over
// the whole result, GET /api/v1/assignments/{id} returns one assignment;
// TLC-04 refuses deleting a user holding an asset (holder cache) and stops
// refusing after the return, when only the closed history refers to the
// user.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestAssignmentProjectionListAndOwnership(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp747actor")
	a := newAssignFixture(t, f, actor, movement.NewService(movement.NewPGRepository(f.App)))
	alice := f.AppUser(t, f.OrgA, "wp747alice")
	bob := f.AppUser(t, f.OrgA, "wp747bob")
	ctx := a.org()
	repo := assignment.NewPGRepository(f.App)

	// Three assignments with descending due dates, one of them returned.
	assets := []string{f.Asset(t, f.OrgA, f.Client1, "wp747-l1"), f.Asset(t, f.OrgA, f.Client1, "wp747-l2"), f.Asset(t, f.OrgA, f.Client1, "wp747-l3")}
	dues := []string{"2027-03-01T10:00:00Z", "2027-02-01T10:00:00Z", "2027-01-01T10:00:00Z"}
	ids := make([]string, len(assets))
	for i, id := range assets {
		w := a.call(ctx, http.MethodPost, "/api/v1/assets/"+id+"/assign", `{"assigned_user_id":"`+alice+`","due_at":"`+dues[i]+`","notes":"wp747 list `+id+`"}`)
		if w.Code != http.StatusCreated {
			t.Fatalf("assign %s: %d %s", id, w.Code, w.Body.String())
		}
		ids[i] = decodeAssignment(t, w).ID
	}
	if w := a.call(ctx, http.MethodPost, "/api/v1/assets/"+assets[0]+"/return", `{"expected_holder":"`+alice+`","return_condition":"good"}`); w.Code != http.StatusOK {
		t.Fatalf("return: %d %s", w.Code, w.Body.String())
	}

	got, err := repo.GetByID(ctx, f.OrgA, ids[0])
	if err != nil || got.Status != "returned" || got.ReturnCondition != "good" || got.ReturnedAt == "" || got.AssignedTo != alice ||
		got.AssignedBy != actor || got.DueDate != "2027-03-01" || got.AssignmentType != "assignment" || got.CheckoutSignature != nil {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err = repo.GetByID(ctx, f.OrgA, f.ID()); err == nil {
		t.Fatal("unknown assignment found")
	}
	if _, err = repo.GetByID(ctx, f.OrgA, "not-a-uuid"); err == nil {
		t.Fatal("malformed id found")
	}

	list := func(filter *assignment.FilterParams, page api.PaginationParams) ([]string, int) {
		t.Helper()
		items, total, lerr := repo.List(ctx, f.OrgA, filter, page)
		if lerr != nil {
			t.Fatalf("list %+v: %v", filter, lerr)
		}
		out := make([]string, len(items))
		for i := range items {
			out[i] = items[i].ID
		}
		return out, total
	}
	if got, total := list(&assignment.FilterParams{Status: "active", AssignedTo: alice}, api.PaginationParams{Limit: 10}); total != 2 ||
		!slices.Equal(got, []string{ids[2], ids[1]}) {
		t.Fatalf("active of alice = %v (%d)", got, total)
	}
	if got, total := list(&assignment.FilterParams{AssignedBy: actor, AssetID: assets[0]}, api.PaginationParams{Limit: 10}); total != 1 || got[0] != ids[0] {
		t.Fatalf("by actor and asset = %v (%d)", got, total)
	}
	if got, total := list(&assignment.FilterParams{Search: "wp747 list " + assets[1]}, api.PaginationParams{Limit: 10}); total != 1 || got[0] != ids[1] {
		t.Fatalf("search = %v (%d)", got, total)
	}
	if got, total := list(&assignment.FilterParams{AssignedTo: "nobody"}, api.PaginationParams{Limit: 10}); total != 0 || len(got) != 0 {
		t.Fatalf("malformed holder = %v (%d)", got, total)
	}
	// Sorted by created_at (default, newest first) and by due_date, paged
	// with the cursor one by one.
	for _, c := range []struct {
		sort, dir string
		want      []string
	}{
		{"", "", []string{ids[2], ids[1], ids[0]}},
		{"due_date", "asc", []string{ids[2], ids[1], ids[0]}},
		{"due_date", "desc", []string{ids[0], ids[1], ids[2]}},
	} {
		filter := &assignment.FilterParams{AssignedTo: alice, SortBy: c.sort, SortDir: c.dir}
		var seen []string
		page := api.PaginationParams{Limit: 1}
		for range 4 {
			items, total, lerr := repo.List(ctx, f.OrgA, filter, page)
			if lerr != nil || total != 3 {
				t.Fatalf("page %s %s: total=%d err=%v", c.sort, c.dir, total, lerr)
			}
			for i := range items {
				seen = append(seen, items[i].ID)
			}
			next := assignment.NextCursor(items, filter, page.Limit)
			if next == "" {
				break
			}
			cursor, derr := api.DecodeCursor(next)
			if derr != nil {
				t.Fatal(derr)
			}
			page.Cursor = &cursor
		}
		if !slices.Equal(seen, c.want) {
			t.Errorf("pages by %q %s = %v, want %v", c.sort, c.dir, seen, c.want)
		}
	}

	// TLC-04 over the holder cache: bob holds an asset.
	users := user.NewPGRepository(f.App)
	held := f.Asset(t, f.OrgA, f.Client1, "wp747-held")
	if w := a.call(ctx, http.MethodPost, "/api/v1/assets/"+held+"/assign", `{"assigned_user_id":"`+bob+`"}`); w.Code != http.StatusCreated {
		t.Fatalf("assign to bob: %d %s", w.Code, w.Body.String())
	}
	if err = users.DeleteUser(ctx, f.OrgA, bob); !errors.Is(err, user.ErrUserOwnsOpenObjects) {
		t.Fatalf("delete of a holder: %v, want ErrUserOwnsOpenObjects", err)
	}
	if w := a.call(ctx, http.MethodPost, "/api/v1/assets/"+held+"/return", `{"expected_holder":"`+bob+`"}`); w.Code != http.StatusOK {
		t.Fatalf("return from bob: %d %s", w.Code, w.Body.String())
	}
	// After the return bob holds nothing open; the closed history in the
	// ledger keeps the user (deactivate or anonymize instead).
	if err = users.DeleteUser(ctx, f.OrgA, bob); errors.Is(err, user.ErrUserOwnsOpenObjects) || !errors.Is(err, user.ErrUserReferenced) {
		t.Fatalf("delete after the return: %v, want ErrUserReferenced", err)
	}
}
