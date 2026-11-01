package lifecycle_test

// Offline detector against the real ingest (WP-683, k7n-offline-detector-race,
// F-412; REC-09, LCY-05, WP-157): the detector reads the observable CIs of
// an organization and then sets the status of the stale ones. A sighting
// that the ingest writes between the read and the status change must not
// end as unknown: the change applies only to the row as it was read.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=… TEST_APP_DATABASE_URL=… \
//	  go test -count=1 -v -run '^TestOfflineDetectorSkipsCISeenDuringRun$' ./internal/lifecycle/

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestOfflineDetectorSkipsCISeenDuringRun is the coding test of F-412: 200
// discovered CIs in a scope of 1 h, last seen 4 h ago and active; four
// parallel ingest requests (POST /discovery/ingest, the production handler
// and repositories) report all of them again while RunOrg runs, started
// 0, 20 and 40 ms after the ingest in three rounds. On e5c2d86 about a
// quarter of the CIs ends unknown although the stored sighting is fresh,
// with ci.offline. Now no CI with a sighting of the round ends unknown:
// the detector changes only a row as it read it, and a sighting that
// reaches a CI after the detector set it unknown brings it back (decided
// on the locked row). ci.offline is raised only for the CIs whose status
// the detector changed, each followed by ci.online from the ingest.
func TestOfflineDetectorSkipsCISeenDuringRun(t *testing.T) {
	f := scopetest.Seed(t, "4f")
	bg := context.Background()
	// 600 CI lines of the detector and the ingest are not the point here.
	defer slog.SetDefault(slog.Default())
	slog.SetDefault(slog.New(slog.NewTextHandler(io.Discard, nil)))
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	one := func(sql string, args ...any) int {
		t.Helper()
		var n int
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return n
	}
	mux := chi.NewRouter()
	// The repositories as server.PostgresRepositories builds them: the CI
	// writes record audit entries and events (ci.online) in the ingest's
	// transaction.
	discovery.NewHandler(discovery.NewPGRepository(f.App),
		ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox()),
		relationship.NewPGRepository(f.App).WithEvents(events.NewOutbox())).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	ingest := func(recs []plugins.DeviceRecord) int {
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: recs})
		if err != nil {
			t.Error(err)
			return 0
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/v1/discovery/ingest", bytes.NewReader(body)).WithContext(ctx))
		return w.Code
	}
	collector := f.ID()
	exec(`INSERT INTO collector (id, organization_id, client_id, name, status) VALUES ($1, $2, $3, 'wp683', 'active')`, collector, f.OrgA, f.Client1)
	exec(`INSERT INTO discovery_scope (organization_id, collector_id, name, kind, target, schedule) VALUES ($1, $2, 'wp683', 'cidr', '10.68.0.0/16', '1h')`,
		f.OrgA, collector)

	const n = 200
	bad := 0
	for round := 0; round < 3; round++ {
		name := fmt.Sprintf("wp683-%d-%%", round)
		recs := make([]plugins.DeviceRecord, 0, n)
		for i := 0; i < n; i++ {
			recs = append(recs, plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: fmt.Sprintf("wp683-%d-%03d", round, i),
				IP: fmt.Sprintf("10.68.%d.%d", round, 1+i), Serial: fmt.Sprintf("WP683%d%04d", round, i)}, time.Now().Add(-5*time.Hour)))
		}
		if code := ingest(recs); code != http.StatusAccepted {
			t.Fatalf("round %d: first ingest = %d", round, code)
		}
		// Last seen 4 h ago (more than 3 x 1 h), still active; the CIs of
		// the earlier rounds are fresh and stay as they are.
		exec(`UPDATE ci SET last_seen_at = now() - interval '4 hours', status = 'active' WHERE organization_id = $1 AND name LIKE $2`, f.OrgA, name)
		exec(`DELETE FROM event_outbox WHERE organization_id = $1`, f.OrgA)
		seen := time.Now().UTC().Truncate(time.Microsecond)
		for i := range recs {
			recs[i].ObservedAt = seen
			recs[i].RecordID = plugins.NewRecordID()
		}
		var wg sync.WaitGroup
		for l := 0; l < 4; l++ {
			wg.Add(1)
			go func(l int) {
				defer wg.Done()
				if code := ingest(recs[l*n/4 : (l+1)*n/4]); code != http.StatusAccepted {
					t.Errorf("round %d: ingest lane %d = %d", round, l, code)
				}
			}(l)
		}
		var changes lifecycle.OfflineChanges
		var runErr error
		wg.Add(1)
		go func() {
			defer wg.Done()
			time.Sleep(time.Duration(round*20) * time.Millisecond)
			changes, runErr = lifecycle.NewOfflineDetector(f.App).RunOrg(bg, f.OrgA)
		}()
		wg.Wait()
		if runErr != nil {
			t.Fatalf("round %d: RunOrg: %v", round, runErr)
		}
		fresh := one(`SELECT count(*) FROM ci WHERE organization_id = $1 AND name LIKE $2 AND last_seen_at >= $3`, f.OrgA, name, seen)
		unknown := one(`SELECT count(*) FROM ci WHERE organization_id = $1 AND name LIKE $2 AND status = 'unknown' AND last_seen_at >= $3`,
			f.OrgA, name, seen)
		offline := one(`SELECT count(*) FROM event_outbox o JOIN ci c ON c.id::text = o.entity_id::text
			WHERE o.organization_id = $1 AND o.type = 'ci.offline' AND c.name LIKE $2`, f.OrgA, name)
		online := one(`SELECT count(*) FROM event_outbox o JOIN ci c ON c.id::text = o.entity_id::text
			WHERE o.organization_id = $1 AND o.type = 'ci.online' AND c.name LIKE $2`, f.OrgA, name)
		t.Logf("round %d (detector +%d ms): detector %+v; %d of %d CIs seen in the round, %d of them unknown; %d ci.offline, %d ci.online",
			round, round*20, changes, fresh, n, unknown, offline, online)
		if fresh != n {
			t.Errorf("round %d: %d of %d CIs carry the sighting of the round", round, fresh, n)
		}
		if unknown > 0 {
			bad += unknown
			t.Errorf("round %d: %d of %d CIs unknown although seen at %s, during the run", round, unknown, n, seen.Format(time.RFC3339Nano))
		}
		if offline != changes.Offline {
			t.Errorf("round %d: %d ci.offline, but the detector changed %d CIs to unknown", round, offline, changes.Offline)
		}
		// A CI the detector set unknown before the sighting reached it is
		// active again with ci.online from the ingest.
		if online != offline {
			t.Errorf("round %d: %d ci.online for %d ci.offline", round, online, offline)
		}
	}
	if bad > 0 {
		t.Logf("CIs unknown after a sighting during the run: %d of %d", bad, 3*n)
	}
}
