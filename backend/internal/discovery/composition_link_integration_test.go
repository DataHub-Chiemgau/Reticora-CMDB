package discovery_test

import (
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// TestIngestSerialLinkSkipsConfigurationChild covers WP-273 (AST-05,
// AST-04) on the match path of the automatic serial link (WP-741): a known
// device that is a configuration child (no asset of its own) reporting the
// serial number of a free asset is processed without a link, on the record
// path and in the window (updateInWindow); the record is not refused and
// opens no review.
//
// Retest:
//
//	cd backend && go test -count=1 -p 1 -run 'TestIngestSerialLinkSkipsConfigurationChild' ./internal/discovery/
func TestIngestSerialLinkSkipsConfigurationChild(t *testing.T) {
	f := scopetest.Seed(t, "76")
	w := &linkWorld{t: t, f: f, notifier: &countingNotifier{}, seen: time.Now().Add(-time.Hour)}
	parent := f.CI(t, f.OrgA, "", "composition-parent")
	for _, c := range []struct {
		name   string
		h      *discovery.Handler
		serial string
	}{
		{"record path", w.handler(false, true, 0), "COMP-LINK-1"},
		{"window", w.handler(true, true, 2), "COMP-LINK-2"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w.t = t
			rec := w.record(c.serial)
			w.ingest(w.handler(false, false, 0), rec)
			id, _ := w.ciOf(&rec)
			if _, err := f.Admin.Exec(t.Context(), `UPDATE ci SET parent_ci_id = $1, child_mode = 'configuration' WHERE id = $2`,
				parent, id); err != nil {
				t.Fatal(err)
			}
			a := w.asset(c.serial, "", "")
			resp := w.ingest(c.h, w.again(rec))
			if resp.Updated != 1 || resp.ReviewItems != 0 {
				t.Fatalf("ingest of the configuration child: %+v", resp)
			}
			w.expectUnlinked(&rec, a)
		})
	}
}
