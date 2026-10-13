package discovery_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// TestCollectorStatusModel covers WP-143 (COL-01, COL-05) against
// PostgreSQL: a created collector is pending, an enrolled one active; a
// heartbeat makes it active; the status run sets it offline after five
// minutes without heartbeat and reports collector.offline, pauses all
// collectors while the discovery license is not active and resumes them;
// a disabled collector's heartbeat is refused; the site binds the client.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestCollectorStatusModel(t *testing.T) {
	f := scopetest.Seed(t, "a3")
	bg := context.Background()
	ctx := f.OrgCtx(f.OrgA)
	repo := discovery.NewPGRepository(f.App)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	status := func(id string) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, `SELECT status FROM collector WHERE id = $1`, id).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	created := &discovery.Collector{OrganizationID: f.OrgA, Name: "col-created", Status: discovery.CollectorPending, Config: map[string]any{}}
	enrolled := &discovery.Collector{OrganizationID: f.OrgA, Name: "col-enrolled", Status: discovery.CollectorActive, Config: map[string]any{}}
	for _, c := range []*discovery.Collector{created, enrolled} {
		if err := repo.RegisterCollector(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	if created.Status != discovery.CollectorPending || created.LastHeartbeatAt != "" ||
		enrolled.Status != discovery.CollectorActive || enrolled.LastHeartbeatAt == "" {
		t.Fatalf("registered: %+v / %+v", created, enrolled)
	}
	if got := status(created.ID); got != "pending" {
		t.Fatalf("created collector %s", got)
	}

	// The site decides the client; a site of another organization or client
	// is refused.
	site, otherSite := f.ID(), f.ID()
	exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'wp143-site')`, site, f.OrgA, f.Client1)
	clientB := f.ID()
	exec(`INSERT INTO client (id, organization_id, name, slug) VALUES ($1, $2, 'wp143-b', 'scopetest-a3-wp143-b')`, clientB, f.OrgB)
	exec(`INSERT INTO site (id, organization_id, client_id, name) VALUES ($1, $2, $3, 'wp143-b')`, otherSite, f.OrgB, clientB)
	updated, err := repo.UpdateCollector(ctx, f.OrgA, enrolled.ID, &discovery.CollectorPatch{SiteID: &site})
	if err != nil || updated.SiteID != site || updated.ClientID != f.Client1 {
		t.Fatalf("bind site = %+v, %v", updated, err)
	}
	var fe *api.FieldError
	if _, err = repo.UpdateCollector(ctx, f.OrgA, created.ID, &discovery.CollectorPatch{SiteID: &otherSite}); !errors.As(err, &fe) || fe.Field != "site_id" {
		t.Fatalf("site of another organization: %v", err)
	}
	exec(`UPDATE collector SET client_id = $2 WHERE id = $1`, created.ID, f.Client2)
	if _, err = repo.UpdateCollector(ctx, f.OrgA, created.ID, &discovery.CollectorPatch{SiteID: &site}); !errors.As(err, &fe) || fe.Field != "site_id" {
		t.Fatalf("site of another client: %v", err)
	}

	// Heartbeat: pending → active.
	if s, herr := repo.Heartbeat(ctx, f.OrgA, created.ID); herr != nil || s != discovery.CollectorActive {
		t.Fatalf("heartbeat = %s, %v", s, herr)
	}

	// Five minutes without heartbeat: offline with collector.offline.
	now := time.Now().UTC()
	monitor := discovery.NewStatusMonitor(f.App).WithClock(func() time.Time { return now })
	exec(`UPDATE collector SET last_heartbeat_at = $2 WHERE id = $1`, enrolled.ID, now.Add(-6*time.Minute))
	exec(`UPDATE collector SET last_heartbeat_at = $2 WHERE id = $1`, created.ID, now.Add(-4*time.Minute))
	changes, err := monitor.RunOrg(bg, f.OrgA)
	if err != nil || changes.Offline != 1 || status(enrolled.ID) != "offline" || status(created.ID) != "active" {
		t.Fatalf("offline run = %+v, %v (%s, %s)", changes, err, status(enrolled.ID), status(created.ID))
	}
	var reported int
	if err = f.Admin.QueryRow(bg, `SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'collector.offline'
		AND entity_id = $2 AND data ->> 'site_id' = $3`, f.OrgA, enrolled.ID, site).Scan(&reported); err != nil || reported != 1 {
		t.Fatalf("%d collector.offline events (%v)", reported, err)
	}
	if again, _ := monitor.RunOrg(bg, f.OrgA); again.Offline != 0 {
		t.Fatalf("second run set %d offline again", again.Offline)
	}
	if s, _ := repo.Heartbeat(ctx, f.OrgA, enrolled.ID); s != discovery.CollectorActive {
		t.Fatalf("heartbeat after offline = %s", s)
	}

	// License not active: paused (heartbeats keep working), then resumed.
	licensed := false
	monitor.Licensed = func(context.Context, string) (bool, error) { return licensed, nil }
	if changes, err = monitor.RunOrg(bg, f.OrgA); err != nil || changes.Paused != 2 {
		t.Fatalf("pause = %+v, %v", changes, err)
	}
	if s, herr := repo.Heartbeat(ctx, f.OrgA, enrolled.ID); herr != nil || s != discovery.CollectorPaused {
		t.Fatalf("heartbeat while paused = %s, %v", s, herr)
	}
	licensed = true
	if changes, err = monitor.RunOrg(bg, f.OrgA); err != nil || changes.Resumed != 2 || status(enrolled.ID) != "offline" {
		t.Fatalf("resume = %+v, %v", changes, err)
	}
	if s, _ := repo.Heartbeat(ctx, f.OrgA, enrolled.ID); s != discovery.CollectorActive {
		t.Fatalf("heartbeat after resume = %s", s)
	}

	// Disabled: heartbeats refused, enabling leaves it offline.
	disabled, active := discovery.CollectorDisabled, discovery.CollectorActive
	if _, err = repo.UpdateCollector(ctx, f.OrgA, enrolled.ID, &discovery.CollectorPatch{Status: &disabled}); err != nil {
		t.Fatal(err)
	}
	if _, err = repo.Heartbeat(ctx, f.OrgA, enrolled.ID); !errors.Is(err, discovery.ErrCollectorDisabled) {
		t.Fatalf("heartbeat of a disabled collector: %v", err)
	}
	if changes, _ = monitor.RunOrg(bg, f.OrgA); status(enrolled.ID) != "disabled" {
		t.Fatalf("the run changed a disabled collector: %+v", changes)
	}
	if c, uerr := repo.UpdateCollector(ctx, f.OrgA, enrolled.ID, &discovery.CollectorPatch{Status: &active}); uerr != nil || c.Status != "offline" {
		t.Fatalf("enable = %+v, %v", c, uerr)
	}
	if _, err = repo.Heartbeat(ctx, f.OrgA, "00000000-0000-4000-8000-000000000000"); err == nil || errors.Is(err, discovery.ErrCollectorDisabled) {
		t.Fatalf("heartbeat of an unknown collector: %v", err)
	}
}
