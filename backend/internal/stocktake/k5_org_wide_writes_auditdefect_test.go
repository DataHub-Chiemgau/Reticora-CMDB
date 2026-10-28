//go:build auditdefect

package stocktake_test

// Defect test found by WP-537 (t1-ev-asset-isolation, TEN-05, TEN-06, E-09;
// F-365): a stocktake has no client column; it is a row of the whole
// organization (migration 000012, policy on the organization only). E-09
// (option 1): only principals with an organization-wide scope write rows
// without client. A principal restricted to client 1 nevertheless updates,
// completes and deletes an organization-wide stocktake that also counts
// client-2 assets. After its completion the client-2 differences can no
// longer be corrected: the completion of an organization-wide principal is
// refused with ErrAlreadyCompleted, the client-2 asset stays in stock. The
// delete removes the client-2 scans with the stocktake (stock_scan
// ON DELETE CASCADE).
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=... go test -tags auditdefect -count=1 -v \
//	  -run '^TestAuditStocktakeOrgWideWritesNeedOrgWideScope$' ./internal/stocktake/

import (
	"context"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
)

// TestAuditStocktakeOrgWideWritesNeedOrgWideScope: update, completion and
// delete of an organization-wide stocktake by a client-1 principal are
// refused and change nothing; afterwards an organization-wide principal
// completes it and corrects the client-2 asset.
func TestAuditStocktakeOrgWideWritesNeedOrgWideScope(t *testing.T) {
	f := scopetest.Seed(t, "33")
	bg := context.Background()
	own := f.Asset(t, f.OrgA, f.Client1, "scopetest-33-k5-own")
	foreign := f.Asset(t, f.OrgA, f.Client2, "scopetest-33-k5-foreign")
	user := f.AppUser(t, f.OrgA, "k5-counter")
	repo := stocktake.NewPGRepository(f.App)
	orgCtx := f.OrgCtx(f.OrgA)

	newStocktake := func(title string) string {
		t.Helper()
		st := &stocktake.Stocktake{OrganizationID: f.OrgA, Title: title, Status: "in_progress", Scope: "full"}
		if err := repo.Create(orgCtx, st); err != nil {
			t.Fatalf("create stocktake: %v", err)
		}
		for _, a := range []string{own, foreign} {
			if err := repo.AddScan(orgCtx, &stocktake.StockScan{OrganizationID: f.OrgA, StocktakeID: st.ID, AssetID: a,
				ScannedBy: user, ScanMethod: "manual", ScanResult: "missing"}); err != nil {
				t.Fatalf("org-wide scan of %s: %v", a, err)
			}
		}
		return st.ID
	}
	type row struct {
		title, status string
		scans         int
	}
	stored := func(id string) row {
		t.Helper()
		var r row
		if err := f.Admin.QueryRow(bg, `SELECT COALESCE((SELECT title FROM stocktake WHERE id = $1), '<deleted>'),
			COALESCE((SELECT status FROM stocktake WHERE id = $1), ''), (SELECT count(*) FROM stock_scan WHERE stocktake_id = $1)`, id).
			Scan(&r.title, &r.status, &r.scans); err != nil {
			t.Fatalf("stored stocktake %s: %v", id, err)
		}
		return r
	}
	assetStatus := func(id string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM asset WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	client1 := f.ClientCtx(f.Client1)
	st := newStocktake("k5 inventory")
	want := stored(st)

	title := "renamed by client 1"
	if _, err := repo.Update(client1, f.OrgA, st, stocktake.UpdateRequest{Title: &title}); err == nil {
		t.Errorf("client-1 update of the organization-wide stocktake: accepted, want refused (E-09)")
	}
	if _, err := repo.Complete(client1, f.OrgA, st, true); err == nil {
		t.Errorf("client-1 completion of the organization-wide stocktake with client-2 differences: accepted, want refused (E-09)")
	}
	if got := stored(st); got != want {
		t.Errorf("organization-wide stocktake after the client-1 writes: %+v, want %+v", got, want)
	}
	// The organization-wide principal can still complete it and corrects
	// the client-2 difference.
	completion, err := repo.Complete(orgCtx, f.OrgA, st, true)
	if err != nil {
		t.Errorf("organization-wide completion after the client-1 writes: %v, want the completion with both corrections", err)
	} else if completion.CorrectionsApplied != 2 {
		t.Errorf("organization-wide completion: %d corrections applied, want 2", completion.CorrectionsApplied)
	}
	if got := assetStatus(foreign); got != "lost" {
		t.Errorf("client-2 asset after the organization-wide completion: %s, want lost", got)
	}

	doomed := newStocktake("k5 inventory delete")
	wantDoomed := stored(doomed)
	if err := repo.Delete(client1, f.OrgA, doomed); err == nil {
		t.Errorf("client-1 delete of the organization-wide stocktake: accepted, want refused (E-09)")
	}
	if got := stored(doomed); got != wantDoomed {
		t.Errorf("organization-wide stocktake after the client-1 delete: %+v, want %+v (client-2 scans included)", got, wantDoomed)
	}
}
