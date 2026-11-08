package rls_test

// The phase-2 channels of the channel matrix (TEN-09, WP-828): the P2
// scenario and the matrix rows of the P2 channels of rls.Channels. They run
// in TestChannelMatrix (channel_matrix_integration_test.go): newChannelMatrix
// seeds the scenario, channelMatrix.rows adds the rows, so every P2 channel
// is read and written by every principal of rls.ScopeKinds and by another
// user of the organization, against the complete PostgreSQL router.
//
// Organization A writes its P2 objects through the API in client 1 at site 1
// ("side 1") and in client 2 at site 2 ("side 2"), organization B in its own
// client and site. An object with client and site is visible where both lie
// in the scope; the team scope restricts team-bound rows only (tickets and
// assets with an owner team) and intersects with client and site (E-11).
// Objects of tables with a client only (suppliers, contracts, consumables,
// orders) ignore the site scope. The other organization sees nothing of A:
// a detail, content, lookup, export or aggregate of an object of A answers
// exactly 404 (or null, an empty list) like an unknown id.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/notify"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/blob"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/server"
	"golang.org/x/text/encoding/charmap"
)

// p2Scenario holds the P2 objects of the matrix. Index 0 is side 1 (client
// 1, site 1), 1 side 2 (client 2, site 2), 2 organization B.
type p2Scenario struct {
	orgs    [3]string
	clients [3]string
	rooms   [3]string
	owners  [3]*channelPrincipal
	// located: visibility of an object with client and site; byClient: of
	// an object with a client only.
	located, byClient [3]seen

	assets       [3]object
	inventory    [3]string
	assetCI      object
	children     [2]object
	movements    [3]object
	reservations [3]object
	assignments  [3]object
	suppliers    [3]object
	contracts    [3]object
	consumables  [3]object
	barcodes     [3]string
	bins         [3]string
	binLocs      [3]string
	stockMoves   [3]object
	orders       [3]object
	stocktakes   [3]object
	documents    [3]object
	tickets      []object

	profiles     [3]string
	importJob    object
	bulkJob      object
	serialReview object
	notes        []object
	datevJob     object
	// datevOwn: the DATEV export of each principal with its file.
	datevOwn     map[string][2]string
	exportWorker *export.JobWorker
	blobs        blob.Store
}

// p2Prefix starts every name of the P2 scenario.
const p2Prefix = "chmp2"

// newP2Scenario writes the P2 objects of both organizations through the
// API (TEN-09, WP-828).
func newP2Scenario(t *testing.T, m *channelMatrix, blobs blob.Store) *p2Scenario {
	t.Helper()
	f := m.f
	s := &p2Scenario{
		orgs:     [3]string{f.OrgA, f.OrgA, f.OrgB},
		clients:  [3]string{f.Client1, f.Client2, m.clientB},
		rooms:    [3]string{m.rooms[0].id, m.rooms[1].id, m.rooms[2].id},
		owners:   [3]*channelPrincipal{m.org, m.org, m.orgB},
		located:  [3]seen{m.one, m.two, m.onlyB},
		byClient: [3]seen{m.allA, seenBy("org", "other-user", "site", "team", "multi"), m.onlyB},
		blobs:    blobs,
	}
	side := [3]string{"s1", "s2", "b"}
	// A bin per side below its site (warehouse, zone, shelf, bin): the
	// stock of articles lies in bins (WP-756).
	sites := [3]string{m.site1, m.site2, m.siteB}
	tree := locations.NewPGRepository(f.App)
	for i := range 3 {
		parent := sites[i]
		for _, kind := range []locations.Kind{locations.KindWarehouse, locations.KindZone, locations.KindShelf, locations.KindBin} {
			l, err := tree.Create(f.OrgCtx(s.orgs[i]), s.orgs[i], locations.CreateRequest{Kind: kind, ParentID: parent, Name: p2Prefix + "-" + side[i] + "-" + string(kind)})
			if err != nil {
				t.Fatalf("setup: %s of side %d: %v", kind, i+1, err)
			}
			parent = l.ID
		}
		s.binLocs[i] = parent
	}

	for i := range 3 {
		p := s.owners[i]
		s.suppliers[i] = object{m.create(t, p, "/api/v1/suppliers", map[string]any{"name": p2Prefix + "-sup-" + side[i], "client_id": s.clients[i]}), s.byClient[i]}
		// The contracts of side 2 and of organization B end in five days:
		// the expiry check reports them (WP-270).
		end := time.Now().UTC().AddDate(0, 0, 60).Format("2006-01-02")
		if i > 0 {
			end = time.Now().UTC().AddDate(0, 0, 5).Format("2006-01-02")
		}
		s.contracts[i] = object{m.create(t, p, "/api/v1/contracts", map[string]any{"number": p2Prefix + "-con-" + side[i], "client_id": s.clients[i],
			"supplier_id": s.suppliers[i].id, "start_date": "2024-01-01", "end_date": end}), s.byClient[i]}
		s.inventory[i] = p2Prefix + "-inv-" + side[i]
		body := map[string]any{"name": p2Prefix + "-asset-" + side[i], "inventory_no": s.inventory[i], "client_id": s.clients[i],
			"serial_number": "CHMP2-SN-" + side[i], "purchase_price": "100.00", "purchase_date": "2024-02-01",
			"supplier_id": s.suppliers[i].id, "contract_id": s.contracts[i].id}
		if i == 0 {
			body["owner_team_id"] = m.team
		}
		s.assets[i] = object{m.create(t, p, "/api/v1/assets", body), s.located[i]}
		// A movement of the asset in its ledger: the transfer to its room
		// gives it its location (E-142).
		s.movements[i] = object{m.create(t, p, "/api/v1/stock-movements", map[string]any{"movement_type": "transfer", "asset_id": s.assets[i].id,
			"to_location_id": s.rooms[i], "reason": p2Prefix}), s.located[i]}
		s.reservations[i] = object{m.create(t, p, "/api/v1/reservations", map[string]any{"asset_id": s.assets[i].id,
			"reserved_until": time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339)}), s.located[i]}
		held := s.freshAsset(t, m, i, "")
		s.assignments[i] = object{m.create(t, p, "/api/v1/assets/"+held+"/assign", map[string]any{"assigned_user_id": p.user}), s.located[i]}
		s.barcodes[i] = "CHMP2-BC-" + side[i]
		s.consumables[i] = object{m.create(t, p, "/api/v1/consumables", map[string]any{"name": p2Prefix + "-cons-" + side[i], "client_id": s.clients[i],
			"barcode": s.barcodes[i], "min_level": 5, "unit": "Stk"}), s.byClient[i]}
		s.stockMoves[i] = object{m.create(t, p, "/api/v1/stock-movements", map[string]any{"movement_type": "receipt", "consumable_id": s.consumables[i].id,
			"to_location_id": s.binLocs[i], "quantity": 10, "reason": p2Prefix}), s.located[i]}
		s.bins[i] = m.scalar(t, `SELECT id::text FROM stock_level WHERE consumable_id = $1`, s.consumables[i].id)
		s.orders[i] = object{m.create(t, p, "/api/v1/orders", map[string]any{"title": p2Prefix + "-order-" + side[i], "client_id": s.clients[i],
			"supplier_id": s.suppliers[i].id}), s.byClient[i]}
		s.stocktakes[i] = object{m.create(t, p, "/api/v1/stocktakes", map[string]any{"title": p2Prefix + "-stocktake-" + side[i], "location_id": s.rooms[i]}),
			s.located[i]}
		// A document of the asset with content (scanned clean).
		doc := m.create(t, p, "/api/v1/documents", map[string]any{"title": p2Prefix + "-doc-" + side[i], "file_name": p2Prefix + "-" + side[i] + ".txt"})
		m.create(t, p, "/api/v1/documents/"+doc+"/links", map[string]any{"entity_type": "asset", "entity_id": s.assets[i].id})
		if code, resp := m.env.call(p, http.MethodPut, "/api/v1/documents/"+doc+"/content", p2Prefix+"-content-"+side[i]); code != http.StatusOK {
			t.Fatalf("setup: upload of document %s: %d %s", doc, code, trim(resp))
		}
		m.exec(t, `UPDATE document_version SET scan_status = 'clean', scanned_at = now() WHERE document_id = $1`, doc)
		s.documents[i] = object{doc, s.located[i]}
		s.profiles[i] = m.create(t, p, "/api/v1/import-profiles", map[string]any{"name": m.name(p2Prefix + "-profile"), "entity": "asset",
			"format": "json", "mode": "create_missing", "mapping": map[string]any{"columns": map[string]any{
				"inventory_no": map[string]any{"target": "inventory_no"}, "name": map[string]any{"target": "name"},
				"client_id": map[string]any{"target": "client_id"}}}})
	}
	// The stock of every article falls below its minimum level: the booking
	// reports stock.below_minimum (WP-285).
	for i := range 3 {
		m.create(t, s.owners[i], "/api/v1/stock-movements", map[string]any{"movement_type": "retrieval", "consumable_id": s.consumables[i].id,
			"from_location_id": s.binLocs[i], "quantity": 8, "reason": p2Prefix})
	}

	// Tickets of the side-1 asset with the team, with the other team, of
	// the side-2 asset without team and of organization B (E-11: the team
	// principal sees team-bound tickets of its team and tickets without
	// team).
	ticket := func(p *channelPrincipal, title, assetID, team string) string {
		body := map[string]any{"title": p2Prefix + "-ticket-" + title, "related_asset_id": assetID}
		if team != "" {
			body["team_id"] = team
		}
		return m.create(t, p, "/api/v1/tickets", body)
	}
	s.tickets = []object{
		{ticket(m.org, "s1-team", s.assets[0].id, m.team), m.one},
		{ticket(m.org, "s1-team2", s.assets[0].id, m.team2), m.one.without("team")},
		{ticket(m.org, "s2", s.assets[1].id, ""), m.two},
		{ticket(m.orgB, "b", s.assets[2].id, ""), m.onlyB},
	}

	// The CIs of the side-1 asset with two child CIs, one of the team and
	// one of the other team (GQL-03, AST-05).
	s.assetCI = object{m.freshCI(t, f.OrgA, f.Client1, m.site1, m.team), m.one}
	m.exec(t, `UPDATE ci SET asset_id = $2 WHERE id = $1`, s.assetCI.id, s.assets[0].id)
	for i, team := range []string{m.team, m.team2} {
		id := m.scalar(t, `INSERT INTO ci (organization_id, client_id, location_id, ci_type_id, name, status, owner_team_id, parent_ci_id)
			VALUES ($1, $2, $3, $4, $5, 'active', $6, $7) RETURNING id::text`, f.OrgA, f.Client1, m.site1, m.serverType,
			fmt.Sprintf("%s-child-%d", p2Prefix, i), team, s.assetCI.id)
		s.children[i] = object{id, [2]seen{m.one, m.one.without("team")}[i]}
	}

	// A review of a serial number mismatch between CI and asset (WP-741)
	// in client 2 at site 2.
	s.serialReview = object{m.scalar(t, `INSERT INTO review_item (organization_id, kind, payload, candidate_ci_ids, client_id, site_id)
		VALUES ($1, 'serial_mismatch', $2, '{}', $3, $4) RETURNING id::text`, f.OrgA,
		fmt.Sprintf(`{"asset_id":%q,"name":"chmp2-serial"}`, s.assets[1].id), f.Client2, m.site2), m.two}

	// An import of the client principal (WP-303) and a bulk on CIs of the
	// client principal (WP-305): jobs with the scope snapshot of client 1.
	code, resp := m.env.call(m.client, http.MethodPost, "/api/v1/imports", map[string]any{"profile_id": s.profiles[0],
		"rows": []map[string]any{{"inventory_no": p2Prefix + "-imp-setup", "name": p2Prefix + "-imp-setup", "client_id": f.Client1}}})
	var started struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(resp), &started); err != nil || code != http.StatusAccepted || started.JobID == "" {
		t.Fatalf("setup: import as client: %d %s", code, trim(resp))
	}
	m.awaitJob(t, started.JobID)
	// An import run is its starter's within the scope of its snapshot, like
	// an export (WP-301): not the other user with an org-wide scope.
	s.importJob = object{started.JobID, seenBy("org", "client")}
	s.bulkJob = object{s.bulk(t, m, m.client, m.freshCI(t, f.OrgA, f.Client1, m.site1, "")), seenBy("org", "other-user", "client")}

	// A DATEV export of the client principal (WP-310), run by the export
	// worker of cmd/server: its creator within its scope only (EXP-01).
	repos := server.PostgresRepositories(f.App, audit.NewPGRecorder())
	worker := export.NewJobWorker(repos.ExportJobs, repos.CI, blobs)
	worker.Tracker = jobs.NewService(repos.Jobs)
	worker.DATEV = export.NewDATEVGate(entitlement.NewService(repos.Entitlement, entitlement.Options{Enforce: false}), true)
	worker.Policies = server.NewFieldResolver(&repos, nil)
	worker.Assets, _ = repos.Asset.(export.AssetSource)
	worker.Orgs = repos.TenantHierarchy
	s.exportWorker = worker
	s.datevJob = object{s.datev(t, m, m.client), seenBy("org", "client")}
	s.datevOwn = map[string][2]string{}
	for _, p := range m.principals {
		id := s.datev(t, m, p)
		s.datevOwn[p.kind] = [2]string{id, s.datevFile(t, m, p.org, id)}
	}

	// Notifications of the contract expiry (WP-270) and of the minimum
	// stock (WP-285): the outbox events of the run and of the bookings go
	// through publisher, event consumer and notifier to the inboxes of the
	// recipients.
	s.notes = s.notifications(t, m)

	// Tickets and documents reach the index through its rebuild
	// (search.reindex, WP-786), as after an upgrade.
	for _, org := range []string{f.OrgA, f.OrgB} {
		if _, err := search.NewPGRepository(f.App).ReindexTenant(f.OrgCtx(org), org); err != nil {
			t.Fatalf("setup: rebuild of the search index: %v", err)
		}
	}
	return s
}

// freshAsset inserts an asset of side i at its room (owner team may be
// empty) and returns its id.
func (s *p2Scenario) freshAsset(t *testing.T, m *channelMatrix, i int, team string) string {
	t.Helper()
	name := m.name(p2Prefix + "-fresh")
	return m.scalar(t, `INSERT INTO asset (organization_id, client_id, location_id, asset_tag, inventory_no, name, owner_team_id)
		VALUES ($1, $2, $3, $4, $4, $4, NULLIF($5, '')::uuid) RETURNING id::text`, s.orgs[i], s.clients[i], s.rooms[i], name, team)
}

// bulk sets the status of the CIs as p (below 100 ids: in the request) and
// returns the revertible bulk job.
func (s *p2Scenario) bulk(t *testing.T, m *channelMatrix, p *channelPrincipal, ids ...string) string {
	t.Helper()
	code, resp := m.env.call(p, http.MethodPost, "/api/v1/cis/bulk", map[string]any{"action": "set_status", "ids": ids, "status": "inactive"})
	var out struct {
		JobID string `json:"bulk_job_id"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil || code != http.StatusOK || out.JobID == "" {
		t.Fatalf("setup: bulk as %s: %d %s", p.kind, code, trim(resp))
	}
	return out.JobID
}

// datev starts a DATEV export as p, runs the export worker and returns the
// export job.
func (s *p2Scenario) datev(t *testing.T, m *channelMatrix, p *channelPrincipal) string {
	t.Helper()
	code, resp := m.env.call(p, http.MethodPost, "/api/v1/exports", map[string]any{"format": "datev"})
	var out struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil || code != http.StatusAccepted || out.ID == "" {
		t.Fatalf("setup: DATEV export as %s: %d %s", p.kind, code, trim(resp))
	}
	for s.exportWorker.ProcessOne(m.bg) {
	}
	return out.ID
}

// datevFile returns the DATEV file and the error report of an export job.
func (s *p2Scenario) datevFile(t *testing.T, m *channelMatrix, org, id string) string {
	t.Helper()
	if status := m.scalar(t, `SELECT status FROM export_job WHERE id = $1`, id); status != export.JobStatusCompleted {
		t.Fatalf("setup: DATEV export %s: %s", id, status)
	}
	var out string
	for _, k := range []string{m.scalar(t, `SELECT COALESCE(object_key, '') FROM export_job WHERE id = $1`, id),
		export.ErrorReportKey(&export.Job{ID: id, OrganizationID: org, Format: export.FormatDATEV})} {
		if k == "" {
			continue
		}
		text, err := charmap.Windows1252.NewDecoder().String(s.read(t, m, k))
		if err != nil {
			t.Fatal(err)
		}
		out += text
	}
	return out
}

// read returns the object key of the bucket exports; empty when there is
// none.
func (s *p2Scenario) read(t *testing.T, m *channelMatrix, key string) string {
	t.Helper()
	rc, err := s.blobs.Get(m.bg, export.ExportBucket, key)
	if err != nil {
		return ""
	}
	defer func() { _ = rc.Close() }()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// notifications delivers the P2 notification events of both organizations
// to the inboxes and returns the inbox entries of the P2 categories: the
// org_admins of an organization receive those of its objects, a user with
// stock:write in client 1 only those of the article of client 1 (NTF-05).
func (s *p2Scenario) notifications(t *testing.T, m *channelMatrix) []object {
	t.Helper()
	f := m.f
	stockC1 := f.AppUser(t, f.OrgA, "matrix-a-stock-c1")
	role := m.scalar(t, `INSERT INTO role (organization_id, name, scope, permissions) VALUES ($1, 'chmp2-stock-c1', 'client', '["stock:write"]'::jsonb)
		RETURNING id::text`, f.OrgA)
	m.exec(t, `INSERT INTO role_assignment (organization_id, user_id, role_id, scope_client_id) VALUES ($1, $2, $3, $4)`, f.OrgA, stockC1, role, f.Client1)
	if _, err := jobs.NewExpiryCheck(f.App).Run(m.bg); err != nil {
		t.Fatalf("setup: expiry check: %v", err)
	}
	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatal(err)
	}
	center := &notify.Center{Channels: notify.NewPGRepository(f.App, notify.NewSealer(enc)), Inbox: notify.NewPGInbox(f.App),
		Preferences: notify.NewPGPreferences(f.App), Deliveries: notify.NewPGDeliveryStore(f.App)}
	// A clock in the past: the run never claims deliveries of other tests.
	now := time.Date(2020, 1, 6, 10, 5, 0, 0, time.UTC)
	notifier := notify.NewNotifier(center.Channels, center.Deliveries, center.Preferences, notify.NewSender(map[string]notify.Provider{
		notify.ProviderInApp: notify.NewInAppProvider(center.Inbox),
	})).WithClock(func() time.Time { return now })
	consumer := notify.NewEventConsumer(notify.NewPGResolver(f.App, permission.NewPGRepository(f.App)), notifier)
	if _, err := events.NewPublisher(f.App, nil, consumer).PublishPending(m.bg); err != nil {
		t.Fatalf("setup: publish the outbox: %v", err)
	}
	for range 3 {
		if _, err := notifier.ProcessDue(m.bg); err != nil {
			t.Fatalf("setup: deliver the notifications: %v", err)
		}
	}
	// inbox counts the notifications of the object in the inbox of user.
	inbox := func(user, category, object string) string {
		return m.scalar(t, `SELECT count(*)::text FROM notification_inbox WHERE user_id = $1 AND category = $2 AND data->>'id' = $3`, user, category, object)
	}
	var out []object
	for _, n := range []struct {
		org, user string
		seen      seen
	}{
		{f.OrgA, m.userA, seenBy("org", "client", "site", "team", "multi")},
		{f.OrgA, m.otherA, seenBy("other-user")},
		{f.OrgB, m.userB, m.onlyB},
	} {
		side := map[string]int{f.OrgA: 1, f.OrgB: 2}[n.org]
		for _, o := range []struct{ category, id string }{
			{"contract", s.contracts[side].id}, {"stock", s.consumables[side].id}, {"stock", s.consumables[0].id},
		} {
			if n.org == f.OrgB && o.id == s.consumables[0].id {
				continue
			}
			id := m.scalar(t, `SELECT COALESCE(max(id::text), '') FROM notification_inbox WHERE organization_id = $1 AND user_id = $2
				AND category = $3 AND data->>'id' = $4`, n.org, n.user, o.category, o.id)
			if id == "" {
				t.Fatalf("setup: no %s notification of %s for %s", o.category, o.id, n.user)
			}
			out = append(out, object{id, n.seen})
		}
	}
	// Nobody receives a notification of an object outside the
	// organization, the client-1 user none of the article of client 2.
	for _, c := range []struct {
		name, user, category, object, want string
	}{
		{"organization B of the contract of A", m.userB, "contract", s.contracts[1].id, "0"},
		{"organization B of the article of A", m.userB, "stock", s.consumables[1].id, "0"},
		{"organization A of the contract of B", m.userA, "contract", s.contracts[2].id, "0"},
		{"organization A of the article of B", m.userA, "stock", s.consumables[2].id, "0"},
		{"the client-1 user of the article of client 2", stockC1, "stock", s.consumables[1].id, "0"},
		{"the client-1 user of the article of client 1", stockC1, "stock", s.consumables[0].id, "1"},
	} {
		if got := inbox(c.user, c.category, c.object); got != c.want {
			t.Errorf("TEN-09: notifications to %s: %s, want %s", c.name, got, c.want)
		}
	}
	return out
}

// awaitJob waits until the job is terminal.
func (m *channelMatrix) awaitJob(t *testing.T, id string) string {
	t.Helper()
	for deadline := time.Now().Add(60 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		status := m.scalar(t, `SELECT status FROM job WHERE id = $1`, id)
		if jobs.Status(status).Terminal() {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s not finished after 60 s: %s", id, status)
		}
	}
}

// p2Rows adds the rows of the P2 channels and the P2 objects of the
// channels review, notification and job.
func (m *channelMatrix) p2Rows(rows map[string]matrixRow) {
	s := m.p2
	f := m.f
	get := func(path string, want int) matrixProbe {
		return matrixProbe{method: http.MethodGet, path: path, want: want}
	}
	list := func(p *channelPrincipal, path string, objs ...object) matrixProbe {
		show, hide := listed(p, objs...)
		return matrixProbe{method: http.MethodGet, path: path, want: http.StatusOK, show: show, hide: hide}
	}
	details := func(p *channelPrincipal, prefix, suffix string, objs ...object) []matrixProbe {
		var out []matrixProbe
		for _, o := range objs {
			out = append(out, get(prefix+o.id+suffix, o.seen.status(p, http.StatusOK)))
		}
		return append(out, get(prefix+unknownID+suffix, http.StatusNotFound))
	}
	// readRow lists and reads every object of a resource.
	readRow := func(path string, objs []object) matrixRow {
		return matrixRow{read: func(p *channelPrincipal) []matrixProbe {
			return append([]matrixProbe{list(p, path+"?limit=200", objs...)}, details(p, path+"/", "", objs...)...)
		}}
	}
	assets := s.assets[:]
	// writeSeen: who writes an object of the side; the team principal
	// writes only rows of its team (TEN-05, E-11), the asset of side 1
	// belongs to it.
	writeSeen := [3]seen{m.one, m.two.without("team"), m.onlyB}

	asset := readRow("/api/v1/assets", assets)
	assetRead := asset.read
	asset.read = func(p *channelPrincipal) []matrixProbe {
		out := assetRead(p)
		// The movements of an asset are a filter of the ledger (OpenAPI
		// listAssetsIdMovements: 404 only for an id that is no UUID): an
		// asset outside the scope has none, like an unknown one.
		for i, a := range assets {
			pr := get("/api/v1/assets/"+a.id+"/movements", http.StatusOK)
			if a.seen[p.kind] {
				pr.show = []string{s.movements[i].id}
			} else {
				pr.show, pr.hide = []string{`"total":0`}, []string{s.movements[i].id}
			}
			out = append(out, pr)
		}
		return append(out, matrixProbe{method: http.MethodGet, path: "/api/v1/assets/" + unknownID + "/movements", want: http.StatusOK,
			show: []string{`"total":0`}}, get("/api/v1/assets/not-a-uuid/movements", http.StatusNotFound))
	}
	asset.write = func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i := range 3 {
			id := s.freshAsset(t, m, i, map[int]string{0: m.team}[i])
			notes := m.name(p2Prefix + "-notes")
			o := object{id, writeSeen[i]}
			out = append(out, matrixProbe{name: fmt.Sprintf("asset of side %d", i+1), method: http.MethodPatch, path: "/api/v1/assets/" + id,
				body: map[string]any{"notes": notes}, want: o.seen.status(p, http.StatusOK),
				after: m.unchanged(o, p, `SELECT COALESCE(notes, '') <> $2 FROM asset WHERE id = $1`, notes)})
		}
		// A new asset has a client and no location yet (it gets one by a
		// transfer, E-142): a principal restricted in the site scope writes
		// none (E-09), a client outside the scope is refused by RLS (403),
		// a client of the other organization is not found (422).
		for i := range 3 {
			for _, team := range []string{m.team, ""} {
				if i == 2 && team != "" {
					continue
				}
				inv := m.name(p2Prefix + "-new")
				body := map[string]any{"name": inv, "inventory_no": inv, "client_id": s.clients[i]}
				if team != "" {
					body["owner_team_id"] = team
				}
				want := newAssetStatus(p, s.orgs[i], s.clients[i], team)
				out = append(out, matrixProbe{name: fmt.Sprintf("in side %d team %v", i+1, team != ""), method: http.MethodPost, path: "/api/v1/assets",
					body: body, want: want, after: m.count(want == http.StatusCreated, `SELECT count(*) FROM asset WHERE name = $1`, inv)})
			}
		}
		return append(out, matrixProbe{method: http.MethodPatch, path: "/api/v1/assets/" + unknownID, body: map[string]any{"notes": "x"},
			want: http.StatusNotFound})
	}
	rows["asset"] = asset

	reservation := readRow("/api/v1/reservations", s.reservations[:])
	reservation.write = func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		until := time.Now().UTC().AddDate(0, 1, 0).Format(time.RFC3339)
		// A reservation writes its asset (the active reservation): an
		// asset the principal may not write, in the body or in the path, is
		// not found (404, OpenAPI createReservation), like an unknown one;
		// the team principal writes the assets of its team only.
		for i := range 3 {
			team := map[int]string{0: m.team}[i]
			id := s.freshAsset(t, m, i, team)
			want := writeSeen[i].status(p, http.StatusCreated)
			out = append(out, matrixProbe{name: fmt.Sprintf("asset of side %d", i+1), method: http.MethodPost, path: "/api/v1/reservations",
				body: map[string]any{"asset_id": id, "reserved_until": until}, want: want,
				after: m.count(want == http.StatusCreated, `SELECT count(*) FROM reservation WHERE asset_id = $1`, id)})
			id = s.freshAsset(t, m, i, team)
			want = writeSeen[i].status(p, http.StatusCreated)
			out = append(out, matrixProbe{name: fmt.Sprintf("asset of side %d", i+1), method: http.MethodPost, path: "/api/v1/assets/" + id + "/reserve",
				body: map[string]any{"reserved_until": until, "assignee_id": p.user}, want: want,
				after: m.count(want == http.StatusCreated, `SELECT count(*) FROM reservation WHERE asset_id = $1`, id)})
		}
		return append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/assets/" + unknownID + "/reserve",
			body: map[string]any{"reserved_until": until, "assignee_id": p.user}, want: http.StatusNotFound},
			matrixProbe{name: "unknown asset", method: http.MethodPost, path: "/api/v1/reservations",
				body: map[string]any{"asset_id": unknownID, "reserved_until": until}, want: http.StatusNotFound})
	}
	rows["reservation"] = reservation

	assignment := readRow("/api/v1/assignments", s.assignments[:])
	assignment.write = func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i := range 3 {
			id := s.freshAsset(t, m, i, map[int]string{0: m.team}[i])
			want := writeSeen[i].status(p, http.StatusCreated)
			out = append(out, matrixProbe{name: fmt.Sprintf("asset of side %d", i+1), method: http.MethodPost, path: "/api/v1/assets/" + id + "/assign",
				body: map[string]any{"assigned_user_id": p.user}, want: want,
				after: m.count(want == http.StatusCreated, `SELECT count(*) FROM asset WHERE id = $1 AND assigned_user_id IS NOT NULL`, id)})
		}
		return append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/assets/" + unknownID + "/assign",
			body: map[string]any{"assigned_user_id": p.user}, want: http.StatusNotFound})
	}
	rows["assignment"] = assignment

	rows["supplier"] = readRow("/api/v1/suppliers", s.suppliers[:])
	rows["contract"] = readRow("/api/v1/contracts", s.contracts[:])

	document := readRow("/api/v1/documents", s.documents[:])
	documentRead := document.read
	document.read = func(p *channelPrincipal) []matrixProbe {
		out := documentRead(p)
		// The content of a document: the presigned URL of its clean
		// version for a visible document, 404 like an unknown id otherwise.
		for _, d := range s.documents {
			pr := get("/api/v1/documents/"+d.id+"/content", d.seen.status(p, http.StatusOK))
			if d.seen[p.kind] {
				pr.show = []string{`"download_url"`, d.id}
			} else {
				pr.hide = []string{d.id}
			}
			out = append(out, pr)
		}
		return append(out, get("/api/v1/documents/"+unknownID+"/content", http.StatusNotFound))
	}
	document.write = func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		// A document of each side with a link to an asset of its side gets
		// a link to an asset of each side, also of the other organization:
		// a document outside the scope is not found (404); the team
		// principal sees a document whose links name no team but may not
		// write it (403, migration 000217); a target outside the scope is
		// not found first (WP-851, 404) like an unknown one.
		for i := range 3 {
			for j := range 3 {
				doc := m.create(t, s.owners[i], "/api/v1/documents", map[string]any{"title": m.name(p2Prefix + "-ldoc"), "file_name": "x.txt"})
				m.create(t, s.owners[i], "/api/v1/documents/"+doc+"/links", map[string]any{"entity_type": "asset", "entity_id": s.freshAsset(t, m, i, "")})
				target := s.freshAsset(t, m, j, "")
				want := http.StatusCreated
				switch {
				case !s.located[i][p.kind] || !s.located[j][p.kind]:
					want = http.StatusNotFound
				case !p.scope.Teams.All:
					want = http.StatusForbidden
				}
				out = append(out, matrixProbe{name: fmt.Sprintf("document of side %d to an asset of side %d", i+1, j+1), method: http.MethodPost,
					path: "/api/v1/documents/" + doc + "/links", body: map[string]any{"entity_type": "asset", "entity_id": target}, want: want,
					after: m.count(want == http.StatusCreated, `SELECT count(*) FROM document_link WHERE document_id = $1 AND entity_id = $2`, doc, target)})
			}
		}
		return append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/documents/" + unknownID + "/links",
			body: map[string]any{"entity_type": "asset", "entity_id": s.assets[0].id}, want: http.StatusNotFound})
	}
	rows["document"] = document

	consumable := readRow("/api/v1/consumables", s.consumables[:])
	consumableRead := consumable.read
	consumable.read = func(p *channelPrincipal) []matrixProbe {
		out := consumableRead(p)
		// The bins of an article: the article by client, its bins by
		// client and site (stock_level).
		for i, c := range s.consumables {
			pr := get("/api/v1/consumables/"+c.id+"/stock", c.seen.status(p, http.StatusOK))
			if s.located[i][p.kind] {
				pr.show = []string{s.binLocs[i]}
			} else {
				pr.hide = []string{s.bins[i]}
			}
			out = append(out, pr)
		}
		return append(out, get("/api/v1/consumables/"+unknownID+"/stock", http.StatusNotFound))
	}
	rows["consumable"] = consumable

	stockMovement := matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		return []matrixProbe{list(p, "/api/v1/stock-movements?limit=200", append(slices.Clone(s.stockMoves[:]), s.movements[:]...)...)}
	}}
	stockMovement.write = func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i := range 3 {
			// A receipt of an article into a bin of its side: an article
			// outside the scope is not found (404), a bin outside it is a
			// 422 on to_location_id like an unknown location.
			want, show := http.StatusCreated, []string(nil)
			switch {
			case !s.byClient[i][p.kind]:
				want = http.StatusNotFound
			case !s.located[i][p.kind]:
				want, show = http.StatusUnprocessableEntity, []string{`"field":"to_location_id"`}
			}
			reason := m.name(p2Prefix + "-booking")
			out = append(out, matrixProbe{name: fmt.Sprintf("receipt of side %d", i+1), method: http.MethodPost, path: "/api/v1/stock-movements",
				body: map[string]any{"movement_type": "receipt", "consumable_id": s.consumables[i].id, "to_location_id": s.binLocs[i], "quantity": 1, "reason": reason},
				want: want, show: show, after: m.count(want == http.StatusCreated, `SELECT count(*) FROM stock_movement WHERE reason = $1`, reason)})
			// A transfer of an asset to the room of its side: an asset the
			// principal may not write is not found (404).
			id := s.freshAsset(t, m, i, map[int]string{0: m.team}[i])
			reason = m.name(p2Prefix + "-transfer")
			want = writeSeen[i].status(p, http.StatusCreated)
			out = append(out, matrixProbe{name: fmt.Sprintf("transfer of side %d", i+1), method: http.MethodPost, path: "/api/v1/stock-movements",
				body: map[string]any{"movement_type": "transfer", "asset_id": id, "to_location_id": s.rooms[i], "reason": reason},
				want: want, after: m.count(want == http.StatusCreated, `SELECT count(*) FROM stock_movement WHERE reason = $1`, reason)})
		}
		return out
	}
	rows["stock-movement"] = stockMovement

	order := readRow("/api/v1/orders", s.orders[:])
	order.write = func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i := range 3 {
			title := m.name(p2Prefix + "-order")
			// A client outside the scope is refused by RLS (403), a client
			// of the other organization fails the composite foreign key
			// (422) for a principal unrestricted in the client scope.
			want := s.byClient[i].status(p, http.StatusCreated)
			if want == http.StatusNotFound {
				want = m.foreignRef(p, http.StatusForbidden)
				if p.org == f.OrgA && p.scope.Clients.All {
					want = http.StatusUnprocessableEntity
				}
			}
			out = append(out, matrixProbe{name: fmt.Sprintf("in the client of side %d", i+1), method: http.MethodPost, path: "/api/v1/orders",
				body: map[string]any{"title": title, "client_id": s.clients[i]}, want: want,
				after: m.count(want == http.StatusCreated, `SELECT count(*) FROM internal_order WHERE title = $1`, title)})
		}
		return out
	}
	rows["order"] = order

	rows["stocktake"] = readRow("/api/v1/stocktakes", s.stocktakes[:])

	ticket := readRow("/api/v1/tickets", s.tickets)
	ticket.write = func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i := range 3 {
			for _, team := range []string{m.team, ""} {
				if i == 2 && team != "" {
					continue
				}
				title := m.name(p2Prefix + "-ticket")
				body := map[string]any{"title": title, "related_asset_id": s.assets[i].id}
				vis := s.located[i]
				if team != "" {
					body["team_id"] = team
				} else {
					vis = vis.without("team")
				}
				// A team or asset outside the scope is not found (422 on
				// its field). A ticket takes client and site of its asset
				// (migration 000272); the team principal writes tickets of
				// its team only (403).
				want := http.StatusCreated
				var show []string
				switch {
				case p.org != s.orgs[i] && team != "":
					want, show = http.StatusUnprocessableEntity, []string{`"field":"team_id"`}
				case !s.located[i][p.kind]:
					want, show = http.StatusUnprocessableEntity, []string{`"field":"related_asset_id"`}
				case !vis[p.kind]:
					want = http.StatusForbidden
				}
				out = append(out, matrixProbe{name: fmt.Sprintf("of side %d team %v", i+1, team != ""), method: http.MethodPost, path: "/api/v1/tickets",
					body: body, want: want, show: show, after: m.count(want == http.StatusCreated, `SELECT count(*) FROM ticket WHERE title = $1`, title)})
			}
		}
		return out
	}
	rows["ticket"] = ticket

	// Import (WP-303): the run reads its rows with the scope of its
	// starter; a row outside it is a row error of the report and creates
	// nothing.
	rows["import"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			// The run and its report (a presigned URL), 404 like an unknown
			// id for every other principal.
			return append(details(p, "/api/v1/imports/", "", s.importJob), details(p, "/api/v1/imports/", "/report", s.importJob)...)
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			profile := s.profiles[0]
			if p.org == f.OrgB {
				profile = s.profiles[2]
			}
			var rowsIn []map[string]any
			var names []string
			for i := range 3 {
				name := m.name(p2Prefix + "-imp")
				names = append(names, name)
				rowsIn = append(rowsIn, map[string]any{"inventory_no": name, "name": name, "client_id": s.clients[i]})
			}
			return []matrixProbe{{method: http.MethodPost, path: "/api/v1/imports", body: map[string]any{"profile_id": profile, "rows": rowsIn},
				want: http.StatusAccepted, after: func(t *testing.T) {
					t.Helper()
					job := m.scalar(t, `SELECT id::text FROM job WHERE kind = $1 AND organization_id = $2 ORDER BY created_at DESC LIMIT 1`, importer.JobKind, p.org)
					if status := m.awaitJob(t, job); status != string(jobs.StatusSucceeded) {
						t.Fatalf("import of %s ended %s", p.kind, status)
					}
					report := s.read(t, m, importer.ReportKey(p.org, job))
					for i, name := range names {
						// A row is written where a new asset of its client
						// (without location and owner team) passes the
						// scope of the starter, as for POST /assets; the
						// report names every other row: out_of_scope where
						// RLS refuses it, invalid_value for a client of the
						// other organization (its foreign key).
						code := map[int]string{http.StatusCreated: "created", http.StatusForbidden: "out_of_scope",
							http.StatusUnprocessableEntity: "invalid_value"}[newAssetStatus(p, s.orgs[i], s.clients[i], "")]
						m.count(code == "created", `SELECT count(*) FROM asset WHERE inventory_no = $1`, name)(t)
						if line := fmt.Sprintf("\n%d;;%s;", i+1, code); !strings.Contains(report, line) {
							t.Errorf("TEN-09: the import report of %s lacks %q for the row of side %d: %q", p.kind, line, i+1, report)
						}
					}
				}}}
		},
	}

	// Bulk (WP-304, WP-305, WP-781): the partial failure report names every
	// id outside the scope with 404; a bulk job is reverted only within the
	// scope of its snapshot.
	rows["bulk"] = matrixRow{write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		var ciIDs []string
		ciSeen := map[string]bool{}
		for i, team := range [3]string{m.team, "", ""} {
			client, site := s.clients[i], [3]string{m.site1, m.site2, m.siteB}[i]
			id := m.freshCI(t, s.orgs[i], client, site, team)
			ciIDs = append(ciIDs, id)
			ciSeen[id] = writeSeen[i][p.kind]
		}
		out = append(out, s.bulkProbe(m, "/api/v1/cis/bulk", map[string]any{"action": "set_status", "ids": ciIDs, "status": "inactive"}, ciSeen,
			`SELECT count(*) FROM ci WHERE id = $1 AND status = 'inactive'`))
		var assetIDs []string
		assetSeen := map[string]bool{}
		for i := range 3 {
			id := s.freshAsset(t, m, i, map[int]string{0: m.team}[i])
			assetIDs = append(assetIDs, id)
			assetSeen[id] = writeSeen[i][p.kind]
		}
		out = append(out, s.bulkProbe(m, "/api/v1/assets/bulk", map[string]any{"action": "set_status", "ids": assetIDs, "status": "maintenance"}, assetSeen,
			`SELECT count(*) FROM asset WHERE id = $1 AND status = 'maintenance'`))
		// A bulk job of the client principal: reverted within its scope
		// snapshot (202), 404 outside it and for the other organization.
		ci := m.freshCI(t, f.OrgA, f.Client1, m.site1, "")
		job := object{s.bulk(t, m, m.client, ci), s.bulkJob.seen}
		out = append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/jobs/" + job.id + "/revert", body: map[string]any{},
			want: job.seen.status(p, http.StatusAccepted)})
		return append(out, matrixProbe{method: http.MethodPost, path: "/api/v1/jobs/" + unknownID + "/revert", body: map[string]any{}, want: http.StatusNotFound})
	}}

	// The label lookup (WP-762): inventory number, barcode and URN of an
	// object resolve within the scope, every other code is 404 like an
	// unknown one.
	rows["label-lookup"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i := range 3 {
			for _, c := range []struct {
				code string
				o    object
			}{
				{s.inventory[i], s.assets[i]},
				{"urn:reticora:asset:" + s.assets[i].id, s.assets[i]},
				{s.barcodes[i], s.consumables[i]},
				{"urn:reticora:consumable:" + s.consumables[i].id, s.consumables[i]},
			} {
				pr := get("/api/v1/labels/lookup?code="+c.code, c.o.seen.status(p, http.StatusOK))
				if c.o.seen[p.kind] {
					pr.show = []string{c.o.id}
				} else {
					pr.hide = []string{c.o.id}
				}
				out = append(out, pr)
			}
		}
		return append(out, get("/api/v1/labels/lookup?code=CHMP2-UNKNOWN", http.StatusNotFound))
	}}

	// The search over the P2 fields (WP-308): an inventory number finds the
	// asset within the scope only.
	rows["search-p2"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i, a := range assets {
			out = append(out, list(p, "/api/v1/search?q="+s.inventory[i], a))
		}
		// The P2 entities of the index: tickets (also of the other team),
		// documents and reservations, with the scope of their rows.
		out = append(out, list(p, "/api/v1/search?limit=100&type=ticket&q="+p2Prefix, s.tickets...),
			list(p, "/api/v1/search?limit=100&type=document&q="+p2Prefix, s.documents[:]...))
		return out
	}}

	// The DATEV export (WP-310): every principal exports the assets of its
	// scope; the export job of another principal is its creator's only.
	rows["datev-export"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		out := []matrixProbe{get("/api/v1/exports/"+s.datevJob.id, s.datevJob.seen.status(p, http.StatusOK)), get("/api/v1/exports/"+unknownID, http.StatusNotFound)}
		own := s.datevOwn[p.kind]
		id, file := own[0], own[1]
		pr := get("/api/v1/exports/"+id, http.StatusOK)
		pr.name = "own DATEV export"
		pr.after = func(t *testing.T) {
			t.Helper()
			for i, a := range assets {
				if got := strings.Contains(file, s.inventory[i]); got != a.seen[p.kind] {
					t.Errorf("TEN-09: the DATEV export of %s holds the asset %s: %v, want %v", p.kind, s.inventory[i], got, a.seen[p.kind])
				}
			}
		}
		return append(out, pr)
	}}

	// The asset aggregate (WP-311): an asset with its CIs, child CIs,
	// movements and documents; null for an asset outside the scope, and no
	// part of it names an object the principal does not see.
	rows["graphql-asset"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		var hidden []string
		for i, a := range assets {
			if !a.seen[p.kind] {
				hidden = append(hidden, a.id, s.inventory[i], s.movements[i].id, s.documents[i].id)
			}
		}
		for _, c := range append([]object{s.assetCI}, s.children[:]...) {
			if !c.seen[p.kind] {
				hidden = append(hidden, c.id)
			}
		}
		for i, a := range append(slices.Clone(assets), object{unknownID, seen{}}) {
			q := fmt.Sprintf(`{ asset(id: %q) { id name inventoryNo cis { id name } children { id name } movements(first: 100) { totalCount nodes { id } } documents { id title } } }`, a.id)
			if a.seen[p.kind] {
				show := []string{a.id, s.inventory[i], s.movements[i].id, s.documents[i].id}
				if i == 0 {
					for _, c := range append([]object{s.assetCI}, s.children[:]...) {
						if c.seen[p.kind] {
							show = append(show, c.id)
						}
					}
				}
				out = append(out, matrixProbe{name: "asset", method: http.MethodPost, path: "/bff/graphql", body: map[string]any{"query": q},
					want: http.StatusOK, show: show, hide: append(slices.Clone(hidden), `"errors"`)})
				continue
			}
			out = append(out, matrixProbe{name: "asset", method: http.MethodPost, path: "/bff/graphql", body: map[string]any{"query": q},
				want: http.StatusOK, show: []string{`"asset":null`}, hide: hidden})
		}
		return out
	}}

	// The P2 objects of the channels review, notification and job: a
	// review of a serial number mismatch, the notifications of contract
	// expiry and minimum stock, an import and a bulk job.
	extend := func(name string, probes func(p *channelPrincipal) []matrixProbe) {
		row := rows[name]
		read := row.read
		row.read = func(p *channelPrincipal) []matrixProbe { return append(read(p), probes(p)...) }
		rows[name] = row
	}
	extend("review", func(p *channelPrincipal) []matrixProbe {
		return append([]matrixProbe{list(p, "/api/v1/review-items?limit=200&kind=serial_mismatch", s.serialReview)},
			details(p, "/api/v1/review-items/", "", s.serialReview)...)
	})
	extend("notification", func(p *channelPrincipal) []matrixProbe {
		return []matrixProbe{list(p, "/api/v1/notifications?limit=200", s.notes...)}
	})
	extend("job", func(p *channelPrincipal) []matrixProbe {
		// The job model shows a job where its snapshot lies in the scope
		// (WP-404); an import job like GET /imports/{id} (WP-828): a run
		// over rows to its starter only, not to the other user with an
		// org-wide scope (404 like an unknown job).
		objs := []object{s.importJob, s.bulkJob}
		return append([]matrixProbe{list(p, "/api/v1/jobs?limit=200", objs...)}, details(p, "/api/v1/jobs/", "", objs...)...)
	})
}

// bulkProbe sends a bulk as the principal of the probe and checks the
// partial failure report: every id the principal may write is ok and
// changed, every other one is 404 (like an unknown id) and unchanged.
func (s *p2Scenario) bulkProbe(m *channelMatrix, path string, body map[string]any, writable map[string]bool, changed string) matrixProbe {
	ids, _ := body["ids"].([]string)
	var show []string
	for _, id := range ids {
		if writable[id] {
			show = append(show, fmt.Sprintf(`{"id":%q,"result":"ok"`, id))
		} else {
			show = append(show, fmt.Sprintf(`{"id":%q,"result":"error","code":404`, id))
		}
	}
	return matrixProbe{name: "partial failure report", method: http.MethodPost, path: path, body: body, want: http.StatusOK, show: show,
		after: func(t *testing.T) {
			t.Helper()
			for _, id := range ids {
				m.count(writable[id], changed, id)(t)
			}
		}}
}

// newAssetStatus is the answer to a new asset of org with client and owner
// team (may be empty) and without location (it gets one by a transfer,
// E-142) written by p: 201 where the row passes WITH CHECK of the scope
// (TEN-05, E-09, E-11), 422 for a client of the other organization (its
// composite foreign key) by an unrestricted principal, else 403 (RLS).
func newAssetStatus(p *channelPrincipal, org, client, team string) int {
	if p.org != org {
		// A principal restricted in any dimension is refused by RLS before
		// the foreign key is checked.
		if p.scope.Clients.All && p.scope.Sites.All && p.scope.Teams.All {
			return http.StatusUnprocessableEntity
		}
		return http.StatusForbidden
	}
	in := func(s identity.ScopeSet, id string) bool { return s.All || (id != "" && slices.Contains(s.IDs, id)) }
	if in(p.scope.Clients, client) && p.scope.Sites.All && in(p.scope.Teams, team) {
		return http.StatusCreated
	}
	return http.StatusForbidden
}
