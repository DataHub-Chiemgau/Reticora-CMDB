package rls_test

// The channel rows of SR-63 (WP-875, TEN-09): disposal records, SLAs with
// their breaches, business calendars, ticket attachments and label
// batches over HTTP for every scope kind of rls.ScopeKinds. They run in
// TestChannelMatrix like the P2 rows (channel_matrix_p2_integration_test.go):
// newChannelMatrix seeds the scenario after the P2 scenario,
// channelMatrix.rows adds the rows.
//
// Every object lies on a side of the P2 scenario: index 0 is client 1 at
// site 1, 1 client 2 at site 2, 2 organization B. Disposal records carry
// client and site of their asset (migration 000145), SLAs and calendars a
// client only (their site scope does not apply), a ticket attachment is
// visible with its ticket, a label batch only to its creator within the
// scope snapshot of the job (WP-763).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"
)

// sr63Prefix starts every name of the SR-63 scenario.
const sr63Prefix = "chsr63"

// sr63Scenario holds the objects of the SR-63 rows per side.
type sr63Scenario struct {
	disposals   [3]object
	calendars   [3]object
	slas        [3]object
	tickets     [3]object
	attachments [3]object
	// batches are the label batches of the client principal (client 1),
	// the site principal (site 2) and organization B, seen by their
	// creator within the scope snapshot of the job.
	batches [3]object
}

// principal returns the matrix principal of a scope kind.
func (m *channelMatrix) principal(t *testing.T, kind string) *channelPrincipal {
	t.Helper()
	for _, p := range m.principals {
		if p.kind == kind {
			return p
		}
	}
	t.Fatalf("no matrix principal of kind %s", kind)
	return nil
}

// newSR63Scenario writes the SR-63 objects of both organizations through
// the API (AK3 of WP-875): per side a disposal record, a calendar, an SLA,
// a ticket with an attachment and a breached SLA, and a label batch.
func newSR63Scenario(t *testing.T, m *channelMatrix) *sr63Scenario {
	t.Helper()
	s2 := m.p2
	s := &sr63Scenario{}
	side := [3]string{"s1", "s2", "b"}
	past := time.Now().UTC().Add(-48 * time.Hour)
	for i := range 3 {
		p := s2.owners[i]
		asset := m.retiredAsset(t, i)
		s.disposals[i] = object{m.create(t, p, "/api/v1/disposal-records", map[string]any{"asset_id": asset, "method": "recycling",
			"performed_by": sr63Prefix + "-" + side[i], "performed_at": past.Format(time.RFC3339)}), s2.located[i]}
		s.calendars[i] = object{m.create(t, p, "/api/v1/business-calendars", map[string]any{"name": sr63Prefix + "-cal-" + side[i],
			"client_id": s2.clients[i]}), s2.byClient[i]}
		s.slas[i] = object{m.create(t, p, "/api/v1/slas", map[string]any{"name": sr63Prefix + "-sla-" + side[i], "priority": "high",
			"response_target_minutes": 60, "resolution_target_minutes": 240, "client_id": s2.clients[i]}), s2.byClient[i]}
		// A ticket of the side's asset without team: client and site of the
		// asset (migration 000272); the team principal sees tickets without
		// team (E-11).
		s.tickets[i] = object{m.create(t, p, "/api/v1/tickets", map[string]any{"title": sr63Prefix + "-ticket-" + side[i],
			"related_asset_id": s2.assets[i].id}), s2.located[i]}
		doc := m.create(t, p, "/api/v1/documents", map[string]any{"title": sr63Prefix + "-attachment-" + side[i],
			"file_name": sr63Prefix + "-" + side[i] + ".pdf"})
		m.create(t, p, "/api/v1/documents/"+doc+"/links", map[string]any{"entity_type": "ticket", "entity_id": s.tickets[i].id})
		s.attachments[i] = object{doc, s2.located[i]}
		// A breached SLA of the ticket: the breach list shows it where the
		// ticket is visible.
		m.exec(t, `INSERT INTO ticket_sla (organization_id, ticket_id, sla_id, response_due_at, resolution_due_at)
			VALUES ($1, $2, $3, $4, $4)`, s2.orgs[i], s.tickets[i].id, s.slas[i].id, past)
	}
	// Label batches of the assets of every side, started by principals
	// with a client, a site and the other organization's scope: the job
	// keeps the scope of its creator (job.scope_*), the PDF only its
	// creator downloads (WP-763).
	for i, starter := range []struct {
		p    *channelPrincipal
		seen seen
	}{
		{m.client, seenBy("org", "client")},
		{m.principal(t, "site"), seenBy("org", "site")},
		{m.orgB, m.onlyB},
	} {
		id := m.labelBatch(t, starter.p)
		if status := m.awaitJob(t, id); status != "succeeded" {
			t.Fatalf("setup: label batch %d of %s ended %s", i, starter.p.kind, status)
		}
		s.batches[i] = object{id, starter.seen}
	}
	return s
}

// retiredAsset inserts an asset of side i at its room in the lifecycle
// state retired, from which a disposal may move it to disposed (WP-278).
func (m *channelMatrix) retiredAsset(t *testing.T, i int) string {
	t.Helper()
	id := m.p2.freshAsset(t, m, i, "")
	m.exec(t, `UPDATE asset SET lifecycle_state = 'retired' WHERE id = $1`, id)
	return id
}

// labelBatch starts a label batch of the assets of every side as p and
// returns the job id.
func (m *channelMatrix) labelBatch(t *testing.T, p *channelPrincipal) string {
	t.Helper()
	ids := []string{m.p2.assets[0].id, m.p2.assets[1].id, m.p2.assets[2].id}
	code, resp := m.env.call(p, http.MethodPost, "/api/v1/labels/batch", map[string]any{"kind": "asset", "template": "asset_50x25", "ids": ids})
	var out struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal([]byte(resp), &out); err != nil || code != http.StatusAccepted || out.JobID == "" {
		t.Fatalf("setup: label batch as %s: %d %s", p.kind, code, trim(resp))
	}
	return out.JobID
}

// sr63Rows adds the rows of SR-63.
func (m *channelMatrix) sr63Rows(rows map[string]matrixRow) {
	s := m.sr63
	s2 := m.p2
	f := m.f
	get := func(path string, want int) matrixProbe {
		return matrixProbe{method: http.MethodGet, path: path, want: want}
	}
	list := func(p *channelPrincipal, path string, objs ...object) matrixProbe {
		show, hide := listed(p, objs...)
		return matrixProbe{method: http.MethodGet, path: path, want: http.StatusOK, show: show, hide: hide}
	}
	details := func(p *channelPrincipal, prefix string, objs ...object) []matrixProbe {
		var out []matrixProbe
		for _, o := range objs {
			out = append(out, get(prefix+o.id, o.seen.status(p, http.StatusOK)))
		}
		return append(out, get(prefix+unknownID, http.StatusNotFound))
	}
	readRow := func(path string, objs []object) func(p *channelPrincipal) []matrixProbe {
		return func(p *channelPrincipal) []matrixProbe {
			return append([]matrixProbe{list(p, path+"?limit=200", objs...)}, details(p, path+"/", objs...)...)
		}
	}
	// byClientWrite is the answer to a write of a row with the client of
	// side i, as for orders: a client outside the scope is refused by RLS
	// (403), a client of the other organization fails the composite
	// foreign key (422) for a principal unrestricted in the client scope.
	byClientWrite := func(p *channelPrincipal, i int) int {
		want := s2.byClient[i].status(p, http.StatusCreated)
		if want == http.StatusNotFound {
			want = m.foreignRef(p, http.StatusForbidden)
			if p.org == f.OrgA && p.scope.Clients.All {
				want = http.StatusUnprocessableEntity
			}
		}
		return want
	}

	// Disposal records (MGT-02): the record of an asset the principal may
	// not write (outside the scope, of the other organization, or a
	// team-less asset for the team principal, E-11) answers 404 like an
	// unknown asset, nothing is written. performed_by lies in the PII
	// vault, so the check counts by asset.
	rows["disposal"] = matrixRow{
		read: readRow("/api/v1/disposal-records", s.disposals[:]),
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			for i := range 3 {
				asset := m.retiredAsset(t, i)
				want := s2.located[i].without("team").status(p, http.StatusCreated)
				out = append(out, matrixProbe{name: fmt.Sprintf("asset of side %d", i+1), method: http.MethodPost, path: "/api/v1/disposal-records",
					body: map[string]any{"asset_id": asset, "method": "recycling", "performed_by": sr63Prefix,
						"performed_at": time.Now().UTC().Format(time.RFC3339)},
					want: want, after: m.count(want == http.StatusCreated, `SELECT count(*) FROM disposal_record WHERE asset_id = $1`, asset)})
			}
			return out
		},
	}

	// SLAs and their breaches (TKT-03): the breach list shows the
	// breaches of the visible tickets only.
	rows["sla"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			out := readRow("/api/v1/slas", s.slas[:])(p)
			return append(out, list(p, "/api/v1/slas/breaches?status=breached&limit=200", s.tickets[:]...))
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			for i := range 3 {
				name := m.name(sr63Prefix + "-sla")
				want := byClientWrite(p, i)
				out = append(out, matrixProbe{name: fmt.Sprintf("in the client of side %d", i+1), method: http.MethodPost, path: "/api/v1/slas",
					body: map[string]any{"name": name, "priority": "low", "response_target_minutes": 30, "resolution_target_minutes": 120,
						"client_id": s2.clients[i]}, want: want,
					after: m.count(want == http.StatusCreated, `SELECT count(*) FROM sla WHERE name = $1`, name)})
			}
			return out
		},
	}

	// Business calendars: a client outside the scope or of the other
	// organization is refused like an unknown client (400 on client_id).
	rows["business-calendar"] = matrixRow{
		read: readRow("/api/v1/business-calendars", s.calendars[:]),
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			for i := range 3 {
				name := m.name(sr63Prefix + "-cal")
				want := s2.byClient[i].status(p, http.StatusCreated)
				var show []string
				if want == http.StatusNotFound {
					want, show = http.StatusBadRequest, []string{`"field":"client_id"`}
				}
				out = append(out, matrixProbe{name: fmt.Sprintf("in the client of side %d", i+1), method: http.MethodPost,
					path: "/api/v1/business-calendars", body: map[string]any{"name": name, "client_id": s2.clients[i]}, want: want, show: show,
					after: m.count(want == http.StatusCreated, `SELECT count(*) FROM business_calendar WHERE name = $1`, name)})
			}
			return out
		},
	}

	// Ticket attachments (WP-765): the documents of a visible ticket;
	// a ticket outside the scope answers 404 like an unknown one.
	rows["ticket-attachment"] = matrixRow{read: func(p *channelPrincipal) []matrixProbe {
		var out []matrixProbe
		for i, tk := range s.tickets {
			pr := get("/api/v1/tickets/"+tk.id+"/attachments", tk.seen.status(p, http.StatusOK))
			if tk.seen[p.kind] {
				pr.show = []string{s.attachments[i].id}
			} else {
				pr.hide = []string{s.attachments[i].id}
			}
			out = append(out, pr)
		}
		return append(out, get("/api/v1/tickets/"+unknownID+"/attachments", http.StatusNotFound))
	}}

	// Label batches (WP-763, INV-03): the download is its creator's within
	// the scope of the job; a new batch of the assets of every side prints
	// only the assets the principal sees.
	rows["label-batch"] = matrixRow{
		read: func(p *channelPrincipal) []matrixProbe {
			var out []matrixProbe
			for _, b := range s.batches {
				pr := get("/api/v1/labels/batch/"+b.id+"/download", b.seen.status(p, http.StatusOK))
				if b.seen[p.kind] {
					pr.show = []string{b.id, `"download_url"`}
				}
				out = append(out, pr)
			}
			return append(out, get("/api/v1/labels/batch/"+unknownID+"/download", http.StatusNotFound))
		},
		write: func(t *testing.T, p *channelPrincipal) []matrixProbe {
			ids := []string{s2.assets[0].id, s2.assets[1].id, s2.assets[2].id}
			visible := 0
			for _, a := range s2.assets {
				if a.seen[p.kind] {
					visible++
				}
			}
			return []matrixProbe{{method: http.MethodPost, path: "/api/v1/labels/batch",
				body: map[string]any{"kind": "asset", "template": "asset_50x25", "ids": ids}, want: http.StatusAccepted,
				after: func(t *testing.T) {
					t.Helper()
					job := m.scalar(t, `SELECT id::text FROM job WHERE kind = 'labels.batch' AND organization_id = $1 AND created_by = $2
						ORDER BY created_at DESC LIMIT 1`, p.org, p.user)
					m.awaitJob(t, job)
					labels := m.scalar(t, `SELECT COALESCE(result->>'labels', '0') FROM job WHERE id = $1`, job)
					if labels != fmt.Sprint(visible) {
						t.Errorf("TEN-09: the label batch of %s printed %s labels, want the %d assets it sees", p.kind, labels, visible)
					}
				}}}
		},
	}
}
