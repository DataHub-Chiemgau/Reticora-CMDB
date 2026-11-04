package discovery_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Retest (WP-741, AST-03, AST-04):
//
//	cd backend && go test -count=1 -p 1 -run 'TestIngestAssetSerialLink|TestIngestLanesSerialLink' ./internal/discovery/

// linkWorld is the ingest with the server's write path (audit, outbox) and
// the asset port of WP-741 against PostgreSQL.
type linkWorld struct {
	t        *testing.T
	f        *scopetest.Fixture
	notifier *countingNotifier
	seen     time.Time
	ip       int
}

// handler builds an ingest: batch with the batch creation (WP-333), lanes
// with the ingest locks (updateInWindow, WP-608), links with the asset port
// (the module inventory on).
func (w *linkWorld) handler(batch, links bool, lanes int) *discovery.Handler {
	base := ci.NewPGRepositoryWithAudit(w.f.App, audit.NewPGRecorder()).WithEvents(events.NewOutbox())
	var cis ci.Repository = base
	if !batch {
		cis = noBatch{base}
	}
	h := discovery.NewHandler(discovery.NewPGRepository(w.f.App), cis).
		WithProvenance(manyProvenance{provenance{repo: override.NewPGRepository(w.f.App)}}).
		WithReviewNotifier(w.notifier)
	if lanes > 0 {
		h.WithIngestLanes(lanes)
	}
	if links {
		h.WithAssetLinks(asset.NewPGRepository(w.f.App))
	}
	return h
}

// record is a server record of a device with serial and a management IP of
// its own, observed after every record before it.
func (w *linkWorld) record(serial string) plugins.DeviceRecord {
	w.ip++
	w.seen = w.seen.Add(time.Second)
	ip := fmt.Sprintf("198.19.%d.%d", w.ip/250, w.ip%250+1)
	return plugins.RecordFromResult("snmp", &plugins.Result{CIType: "server", Name: "autolink-" + ip, IP: ip, Serial: serial}, w.seen)
}

// again is rec reported once more, observed later.
func (w *linkWorld) again(rec plugins.DeviceRecord) plugins.DeviceRecord {
	w.seen = w.seen.Add(time.Second)
	rec.RecordID, rec.ObservedAt = plugins.NewRecordID(), w.seen
	return rec
}

// ingest processes recs as the HTTP ingest does.
func (w *linkWorld) ingest(h *discovery.Handler, recs ...plugins.DeviceRecord) discovery.BulkIngestResponse {
	w.t.Helper()
	resp, err := discovery.ProcessInLane(lanesCtx(w.f, w.f.OrgA), h, w.f.OrgA, 0, recs...)
	if err != nil {
		w.t.Fatalf("ingest: %v", err)
	}
	return resp
}

// asset inserts an asset of organization A with serial, client and location
// ("" for none) and returns its id.
func (w *linkWorld) asset(serial, clientID, locationID string) string {
	w.t.Helper()
	id := w.f.ID()
	if _, err := w.f.Admin.Exec(context.Background(), `INSERT INTO asset (id, organization_id, client_id, asset_tag, name, serial_number, location_id)
		VALUES ($1, $2, NULLIF($3, '')::uuid, $4, $4, $5, NULLIF($6, '')::uuid)`,
		id, w.f.OrgA, clientID, "AUTOLINK-"+id[len(id)-8:], serial, locationID); err != nil {
		w.t.Fatalf("insert asset %q: %v", serial, err)
	}
	return id
}

// ciOf returns the id and the asset of the live CI of rec (by its
// management IP) and fails unless there is exactly one.
func (w *linkWorld) ciOf(rec *plugins.DeviceRecord) (id, assetID string) {
	w.t.Helper()
	var n int
	if err := w.f.Admin.QueryRow(context.Background(), `SELECT count(*), COALESCE(min(id::text), ''), COALESCE(min(asset_id::text), '')
		FROM ci WHERE organization_id = $1 AND management_ip = $2 AND deleted_at IS NULL`, w.f.OrgA, rec.TargetIP).Scan(&n, &id, &assetID); err != nil {
		w.t.Fatal(err)
	}
	if n != 1 {
		w.t.Fatalf("%d CIs of the device %s, want 1", n, rec.TargetIP)
	}
	return id, assetID
}

// count runs a count query with organization A as $1.
func (w *linkWorld) count(query string, args ...any) int {
	w.t.Helper()
	var n int
	if err := w.f.Admin.QueryRow(context.Background(), query, append([]any{w.f.OrgA}, args...)...).Scan(&n); err != nil {
		w.t.Fatalf("%s: %v", query, err)
	}
	return n
}

// autoLinks returns the automatic links (asset.linked with match serial) of
// the asset as CI id per entry, and fails when one lacks source collector
// or the serial number.
func (w *linkWorld) autoLinks(assetID string) []string {
	w.t.Helper()
	rows, err := w.f.Admin.Query(context.Background(), `SELECT changes->>'ci_id', source, COALESCE(changes->>'serial_number', ''),
			COALESCE(changes->>'match', ''), resource_type
		FROM audit_log WHERE organization_id = $1 AND action = 'asset.linked' AND resource_id = $2 ORDER BY seq`, w.f.OrgA, assetID)
	if err != nil {
		w.t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var ciID, source, serial, match, resourceType string
		if err := rows.Scan(&ciID, &source, &serial, &match, &resourceType); err != nil {
			w.t.Fatal(err)
		}
		if match != asset.MatchSerial {
			continue
		}
		if source != audit.SourceCollector || serial == "" || resourceType != "asset" {
			w.t.Errorf("asset.linked of asset %s: source %q, serial_number %q, resource_type %q; want collector, the serial, asset",
				assetID, source, serial, resourceType)
		}
		out = append(out, ciID)
	}
	if err := rows.Err(); err != nil {
		w.t.Fatal(err)
	}
	return out
}

// primary returns asset.ci_id and the serial number of the asset.
func (w *linkWorld) primary(assetID string) (ciID, serial string) {
	w.t.Helper()
	if err := w.f.Admin.QueryRow(context.Background(), `SELECT COALESCE(ci_id::text, ''), COALESCE(serial_number, '') FROM asset WHERE id = $1`,
		assetID).Scan(&ciID, &serial); err != nil {
		w.t.Fatal(err)
	}
	return ciID, serial
}

// expectLinked asserts that the CI of rec is linked to assetID with one
// automatic link, the asset's primary CI and its serial number unchanged.
func (w *linkWorld) expectLinked(rec *plugins.DeviceRecord, assetID, assetSerial string) string {
	w.t.Helper()
	id, linked := w.ciOf(rec)
	if linked != assetID {
		w.t.Errorf("CI of %s (serial %q) linked to %q, want asset %s", rec.TargetIP, rec.Identity.SerialNumber, linked, assetID)
	}
	if links := w.autoLinks(assetID); len(links) != 1 || links[0] != id {
		w.t.Errorf("automatic links of asset %s: %v, want one to %s", assetID, links, id)
	}
	if primary, serial := w.primary(assetID); primary != id || serial != assetSerial {
		w.t.Errorf("asset %s: primary CI %q serial %q, want %s and %q unchanged", assetID, primary, serial, id, assetSerial)
	}
	return id
}

// expectUnlinked asserts that the CI of rec has no asset and assetID no
// automatic link and no primary CI, and that no review was opened for it.
func (w *linkWorld) expectUnlinked(rec *plugins.DeviceRecord, assetIDs ...string) {
	w.t.Helper()
	id, linked := w.ciOf(rec)
	if linked != "" {
		w.t.Errorf("CI of %s linked to %s, want none", rec.TargetIP, linked)
	}
	for _, a := range assetIDs {
		if links := w.autoLinks(a); len(links) != 0 {
			w.t.Errorf("asset %s linked automatically to %v, want none", a, links)
		}
		if primary, _ := w.primary(a); primary != "" {
			w.t.Errorf("asset %s has the primary CI %s, want none", a, primary)
		}
	}
	if n := w.count(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND $2::uuid = ANY (candidate_ci_ids)`, id); n != 0 {
		w.t.Errorf("%d review items for the unlinked CI %s, want none", n, id)
	}
}

// TestIngestAssetSerialLink covers the automatic serial link of the ingest
// (WP-741, AST-03, AST-04) against PostgreSQL with the server's write path:
//   - a new device with the serial number of an asset is linked with its
//     insert, on the record path and in the batch (CreateMany); a known
//     device in the CI update, on the record path and in the window
//     (updateInWindow); case and blanks do not matter; each link has its
//     audit entry asset.linked (source collector, from the queue too), sets
//     the empty primary CI and changes no asset data; the matched CI gets
//     its ci_change row asset_id and ci.changed;
//   - no link (and no review): two assets with the normal form, an asset
//     with a linked CI, an asset or asset location of another client (no
//     ingest.batch.fallback), the module inventory off (no port);
//   - a serial number reported for a linked CI that differs from the
//     asset's: the asset keeps its serial number, the CI stores the
//     observed one, exactly one serial_mismatch with exactly one
//     review.created and one notification after the commit;
//   - repeated ingest over the record path, the batch and lanes: one CI,
//     one asset, no new asset row (AST-03).
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestIngestAssetSerialLink(t *testing.T) {
	f := scopetest.Seed(t, "74")
	w := &linkWorld{t: t, f: f, notifier: &countingNotifier{}, seen: time.Now().Add(-time.Hour)}
	bg := context.Background()
	single, batch := w.handler(false, true, 0), w.handler(true, true, 0)

	t.Run("new device, record path", func(t *testing.T) {
		w.t = t
		a := w.asset("  AL-Single-1 ", "", "")
		rec := w.record("al-single-1")
		if resp := w.ingest(single, rec); resp.Created != 1 {
			t.Fatalf("ingest: %+v", resp)
		}
		id := w.expectLinked(&rec, a, "  AL-Single-1 ")
		if n := w.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'ci.created' AND entity_id::text = $2
			AND data->>'asset_id' = $3`, id, a); n != 1 {
			t.Errorf("%d events ci.created with the asset, want 1", n)
		}
	})

	t.Run("new devices, batch", func(t *testing.T) {
		w.t = t
		logs := captureLogs(t)
		assets := []string{w.asset("AL-BATCH-1", "", ""), w.asset("AL-BATCH-2", f.Client1, ""), w.asset("al-batch-3", "", "")}
		recs := []plugins.DeviceRecord{w.record("AL-BATCH-1"), w.record("al-Batch-2"), w.record(" AL-BATCH-3"), w.record("AL-BATCH-NONE")}
		recs[1].ClientID = f.Client1
		if resp := w.ingest(batch, recs...); resp.Created != 4 {
			t.Fatalf("ingest: %+v", resp)
		}
		for i, a := range assets {
			_, serial := w.primary(a)
			w.expectLinked(&recs[i], a, serial)
		}
		w.expectUnlinked(&recs[3])
		if fallback := logs.events(t, "ingest.batch.fallback"); len(fallback) != 0 {
			t.Errorf("the batch fell back to records: %v", fallback)
		}
		if result, err := audit.Verify(bg, f.App, f.OrgA); err != nil || !result.Intact {
			t.Errorf("audit chain after the batch: %+v, %v", result, err)
		}
	})

	// A known device: created while the asset was not yet linked (the
	// module was off), linked by its next record.
	known := func(t *testing.T, h *discovery.Handler, serial string) {
		w.t = t
		rec := w.record(serial)
		w.ingest(w.handler(false, false, 0), rec)
		id, _ := w.ciOf(&rec)
		a := w.asset(serial, "", "")
		changedBefore := w.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'ci.changed' AND entity_id::text = $2`, id)
		if resp := w.ingest(h, w.again(rec)); resp.Updated != 1 {
			t.Fatalf("ingest of the known device: %+v", resp)
		}
		w.expectLinked(&rec, a, serial)
		if n := w.count(`SELECT count(*) FROM ci_change WHERE organization_id = $1 AND ci_id = $2 AND field_name = 'asset_id'
			AND new_value #>> '{}' = $3`, id, a); n != 1 {
			t.Errorf("%d ci_change rows asset_id for the link, want 1", n)
		}
		if n := w.count(`SELECT count(*) FROM event_outbox WHERE organization_id = $1 AND type = 'ci.changed' AND entity_id::text = $2`,
			id); n != changedBefore+1 {
			t.Errorf("%d events ci.changed after the link, want %d", n, changedBefore+1)
		}
		// Linked once: the next record links nothing more.
		w.ingest(h, w.again(rec))
		w.expectLinked(&rec, a, serial)
	}
	t.Run("known device, record path", func(t *testing.T) { known(t, single, "AL-KNOWN-1") })
	t.Run("known device, window", func(t *testing.T) { known(t, w.handler(true, true, 2), "AL-KNOWN-2") })

	t.Run("from the queue", func(t *testing.T) {
		w.t = t
		a := w.asset("AL-QUEUE-1", "", "")
		rec := w.record("AL-QUEUE-1")
		for _, result := range discovery.ConsumeRecords(lanesCtx(f, f.OrgA), w.handler(true, true, 1), f.OrgA, rec) {
			if result != "ack" {
				t.Fatalf("delivery %s", result)
			}
		}
		w.expectLinked(&rec, a, "AL-QUEUE-1")
	})

	// A room of client 2: the location of an org-wide asset.
	site := f.Room(t, f.Client2, "autolink")
	noLink := func(t *testing.T, h *discovery.Handler, prefix string) {
		w.t = t
		logs := captureLogs(t)
		twinA, twinB := w.asset(prefix+"-TWIN", "", ""), w.asset(" "+strings.ToLower(prefix)+"-twin", "", "")
		taken := w.asset(prefix+"-TAKEN", "", "")
		other := f.CI(t, f.OrgA, "", "autolink-taken-"+prefix)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET asset_id = $1 WHERE id = $2`, taken, other); err != nil {
			t.Fatal(err)
		}
		foreign := w.asset(prefix+"-FOREIGN", f.Client2, "")
		placed := w.asset(prefix+"-PLACED", "", site)
		recs := []plugins.DeviceRecord{w.record(prefix + "-TWIN"), w.record(prefix + "-TAKEN"), w.record(prefix + "-FOREIGN"),
			w.record(prefix + "-PLACED")}
		recs[2].ClientID, recs[3].ClientID = f.Client1, f.Client1
		if resp := w.ingest(h, recs...); resp.Created != 4 || resp.ReviewItems != 0 {
			t.Fatalf("ingest: %+v", resp)
		}
		w.expectUnlinked(&recs[0], twinA, twinB)
		w.expectUnlinked(&recs[1])
		if links := w.autoLinks(taken); len(links) != 0 {
			t.Errorf("asset with a linked CI linked automatically to %v", links)
		}
		w.expectUnlinked(&recs[2], foreign)
		w.expectUnlinked(&recs[3], placed)
		if fallback := logs.events(t, "ingest.batch.fallback"); len(fallback) != 0 {
			t.Errorf("the batch fell back to records: %v", fallback)
		}
	}
	t.Run("no link, batch", func(t *testing.T) { noLink(t, batch, "AL-NB") })
	t.Run("no link, record path", func(t *testing.T) { noLink(t, single, "AL-NS") })

	t.Run("module inventory off", func(t *testing.T) {
		w.t = t
		a := w.asset("AL-OFF-1", "", "")
		rec := w.record("AL-OFF-1")
		w.ingest(w.handler(true, false, 0), rec)
		w.expectUnlinked(&rec, a)
	})

	t.Run("serial mismatch", func(t *testing.T) {
		w.t = t
		a := w.asset("AL-MISMATCH-1", "", "")
		rec := w.record("AL-MISMATCH-1")
		rec.Identity.HardwareUUID = "4c4c4544-0074-4000-8000-000000000741"
		w.ingest(single, rec)
		id := w.expectLinked(&rec, a, "AL-MISMATCH-1")
		notifiedBefore, _ := w.notifier.state()
		reviews := func() (open, announced int) {
			open = w.count(`SELECT count(*) FROM review_item WHERE organization_id = $1 AND kind = 'serial_mismatch' AND status = 'open'
				AND payload->>'ci_id' = $2`, id)
			announced = w.count(`SELECT count(*) FROM review_item r JOIN event_outbox e ON e.organization_id = r.organization_id
				AND e.entity_id::text = r.id::text AND e.type = 'review.created'
				WHERE r.organization_id = $1 AND r.kind = 'serial_mismatch' AND r.payload->>'ci_id' = $2`, id)
			return open, announced
		}
		for i, h := range []*discovery.Handler{single, w.handler(true, true, 2), single} {
			changed := w.again(rec)
			changed.Identity.SerialNumber = fmt.Sprintf("AL-MISMATCH-OTHER-%d", i)
			resp := w.ingest(h, changed)
			if resp.Updated != 1 {
				t.Fatalf("report %d of another serial number: %+v", i, resp)
			}
			var ciSerial string
			if err := f.Admin.QueryRow(bg, `SELECT serial_number FROM ci WHERE id = $1`, id).Scan(&ciSerial); err != nil {
				t.Fatal(err)
			}
			if _, assetSerial := w.primary(a); assetSerial != "AL-MISMATCH-1" || ciSerial != changed.Identity.SerialNumber {
				t.Errorf("report %d: asset serial %q, CI serial %q; want the asset's unchanged and the observed one on the CI", i, assetSerial, ciSerial)
			}
			open, announced := reviews()
			notified, inTx := w.notifier.state()
			if open != 1 || announced != 1 || notified != notifiedBefore+1 || inTx != 0 {
				t.Errorf("report %d: %d open serial_mismatch, %d review.created, %d notifications (%d inside a transaction); want 1, 1, 1 after the commit",
					i, open, announced, notified-notifiedBefore, inTx)
			}
			if (resp.ReviewItems == 1) != (i == 0) {
				t.Errorf("report %d: %d review items counted, want 1 for the first only", i, resp.ReviewItems)
			}
		}
	})

	t.Run("no duplicate physical objects", func(t *testing.T) {
		w.t = t
		a := w.asset("AL-ONE-1", "", "")
		assetsBefore := w.count(`SELECT count(*) FROM asset WHERE organization_id = $1`)
		rec := w.record("AL-ONE-1")
		w.ingest(batch, rec)
		w.ingest(single, w.again(rec))
		w.ingest(w.handler(true, true, 4), w.again(rec))
		variant := w.again(rec)
		variant.Identity.SerialNumber = " al-one-1 "
		for lane := range 3 {
			if _, err := discovery.ProcessInLane(lanesCtx(f, f.OrgA), w.handler(true, true, 3), f.OrgA, lane, w.again(variant)); err != nil {
				t.Fatalf("lane %d: %v", lane, err)
			}
		}
		w.expectLinked(&rec, a, "AL-ONE-1")
		if n := w.count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND serial_number_lc = 'al-one-1' AND deleted_at IS NULL`); n != 1 {
			t.Errorf("%d CIs of the device, want 1", n)
		}
		if n := w.count(`SELECT count(*) FROM ci WHERE organization_id = $1 AND asset_id = $2`, a); n != 1 {
			t.Errorf("%d CIs linked to the asset, want 1", n)
		}
		if n := w.count(`SELECT count(*) FROM asset WHERE organization_id = $1`); n != assetsBefore {
			t.Errorf("%d assets after the ingests, want %d (discovery creates none)", n, assetsBefore)
		}
	})
}
