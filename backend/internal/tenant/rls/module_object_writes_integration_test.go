package rls_test

// Writes of documents and maintenance windows through the complete
// PostgreSQL router (WP-341, F-004, F-048; TEN-05, TEN-06, TEN-09, E-09,
// E-61). Both span several objects: a principal restricted in the client or
// site scope changes, deletes or uploads to a row only when it sees all of
// its links and may write every one of them. A row shared with another
// client or with an org-wide object (WP-353, migration 000146) and an
// unlinked (org-wide) row answer 404 and stay unchanged, including the other
// client's and the org-wide links (no foreign key cascade); creating an unlinked row is refused with 403,
// and linking an org-wide document does not take it away from other
// clients. Org-wide principals are unchanged.
//
// Retest:
//   cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/audit_db_01?sslmode=disable \
//     go test -count=1 -run '^(TestDocumentWritesOutsideScope|TestMaintenanceWindowWritesOutsideScope)$' ./internal/tenant/rls/

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// memoryBlobs is an in-memory blob.Store; the test reads the stored
// objects directly.
type memoryBlobs struct {
	mu      sync.Mutex
	objects map[string]string
}

func (s *memoryBlobs) Put(_ context.Context, bucket, key string, reader io.Reader, _ int64, _ string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[bucket+"/"+key] = string(data)
	return nil
}

func (s *memoryBlobs) Get(_ context.Context, bucket, key string) (io.ReadCloser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, ok := s.objects[bucket+"/"+key]
	if !ok {
		return nil, io.EOF
	}
	return io.NopCloser(bytes.NewReader([]byte(data))), nil
}

func (s *memoryBlobs) Delete(_ context.Context, bucket, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, bucket+"/"+key)
	return nil
}

func (s *memoryBlobs) PresignedGetURL(_ context.Context, bucket, key string) (string, error) {
	return "https://blob.example/" + bucket + "/" + key, nil
}

// object returns the content stored under key in any bucket.
func (s *memoryBlobs) object(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, v := range s.objects {
		if strings.HasSuffix(k, "/"+key) {
			return v
		}
	}
	return ""
}

// TestMaintenanceWindowWritesOutsideScope: a window that also covers a
// client-2 CI, and an org-wide window without CIs, must not be changed or
// deleted by a client-1 principal; a window of client 1 only stays editable
// by client 1, and client 1 still plans windows for its own CIs.
func TestMaintenanceWindowWritesOutsideScope(t *testing.T) {
	w := newT1World(t, "d1")
	f := w.f
	ci1 := f.CI(t, f.OrgA, f.Client1, "t1-mw-c1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "t1-mw-c2")
	site1 := w.siteOfRoom(f.Room(t, f.Client1, "t1-mw-s1"))
	client1 := w.as("client1", only(f.Client1), all())
	site := w.as("site1", all(), only(site1))
	window := func(p *channelPrincipal, title string, cis ...string) string {
		return w.create(p, "/api/v1/maintenance-windows", map[string]any{
			"title": title, "starts_at": "2030-01-01T00:00:00Z", "ends_at": "2030-01-01T02:00:00Z", "ci_ids": cis,
		})
	}
	ciOrg := f.CI(t, f.OrgA, "", "t1-mw-org")
	mixed := window(w.orgA, "t1-mw-mixed", ci1, ci2)
	orgLinked := window(w.orgA, "t1-mw-org-linked", ci1, ciOrg)
	orgWide := window(w.orgA, "t1-mw-orgwide")
	ownOnly := window(w.orgA, "t1-mw-own", ci1)

	for _, c := range []struct {
		name, id, title string
		linkedCI        string
	}{
		{"window covering client-1 and client-2 CIs (WP-020 P1, WP-028 P1)", mixed, "t1-mw-mixed", ci2},
		{"org-wide window without CIs (WP-028 P2, E-09)", orgWide, "t1-mw-orgwide", ""},
		{"window covering a client-1 and an org-wide CI (WP-353, E-09)", orgLinked, "t1-mw-org-linked", ciOrg},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, body := w.do(client1, http.MethodPatch, "/api/v1/maintenance-windows/"+c.id, map[string]any{"title": "t1-changed-by-c1", "status": "cancelled"})
			if !refused(code) {
				t.Errorf("PATCH as client 1: %d %s, want 403/404", code, trim(body))
			}
			if got := w.scalar(`SELECT title || '|' || status FROM maintenance_window WHERE id = $1`, c.id); !strings.HasPrefix(got, c.title+"|") || strings.HasSuffix(got, "|cancelled") {
				t.Errorf("window after PATCH by client 1: %q, want title %q and not cancelled", got, c.title)
			}
			code, body = w.do(client1, http.MethodDelete, "/api/v1/maintenance-windows/"+c.id, nil)
			if !refused(code) {
				t.Errorf("DELETE as client 1: %d %s, want 403/404", code, trim(body))
			}
			if n := w.scalar(`SELECT count(*)::text FROM maintenance_window WHERE id = $1`, c.id); n != "1" {
				t.Errorf("window rows after DELETE by client 1: %s, want 1", n)
			}
			if c.linkedCI != "" {
				if n := w.scalar(`SELECT count(*)::text FROM maintenance_window_ci WHERE maintenance_window_id = $1 AND ci_id = $2`, c.id, c.linkedCI); n != "1" {
					t.Errorf("client-2 link rows after DELETE by client 1: %s, want 1 (FK cascade removed another client's data)", n)
				}
			}
		})
	}
	t.Run("org-wide window created by a client- or site-scoped principal (E-09)", func(t *testing.T) {
		for _, p := range []*channelPrincipal{client1, site} {
			code, body := w.do(p, http.MethodPost, "/api/v1/maintenance-windows", map[string]any{
				"title": "t1-mw-created-by-" + p.kind, "starts_at": "2030-02-01T00:00:00Z", "ends_at": "2030-02-01T02:00:00Z",
			})
			if code != http.StatusForbidden {
				t.Errorf("POST an org-wide window (no CIs) as %s: %d %s, want 403", p.kind, code, trim(body))
			}
		}
		if n := w.scalar(`SELECT count(*)::text FROM maintenance_window WHERE organization_id = $1 AND title LIKE 't1-mw-created-by-%'`, f.OrgA); n != "0" {
			t.Errorf("org-wide windows created by scoped principals: %s, want 0", n)
		}
	})
	t.Run("positive control: client 1 plans and edits its own window", func(t *testing.T) {
		created := window(client1, "t1-mw-c1-created", ci1)
		if got := w.scalar(`SELECT ci_count::text FROM maintenance_window WHERE id = $1`, created); got != "1" {
			t.Errorf("ci_count of the window client 1 created: %s, want 1", got)
		}
		if code, body := w.do(client1, http.MethodPatch, "/api/v1/maintenance-windows/"+ownOnly, map[string]any{"title": "t1-mw-own-renamed"}); code != http.StatusOK {
			t.Errorf("PATCH own window as client 1: %d %s, want 200", code, trim(body))
		}
		if code, body := w.do(client1, http.MethodDelete, "/api/v1/maintenance-windows/"+ownOnly, nil); code != http.StatusNoContent {
			t.Errorf("DELETE own window as client 1: %d %s, want 204", code, trim(body))
		}
	})
	t.Run("positive control: org admin", func(t *testing.T) {
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/maintenance-windows/"+mixed, map[string]any{"title": "t1-mw-mixed-2"}); code != http.StatusOK {
			t.Errorf("PATCH shared window as org admin: %d %s, want 200", code, trim(body))
		}
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/maintenance-windows/"+orgWide, map[string]any{"title": "t1-mw-orgwide-2"}); code != http.StatusOK {
			t.Errorf("PATCH org-wide window as org admin: %d %s, want 200", code, trim(body))
		}
	})
}

// TestDocumentWritesOutsideScope: a document shared by a client-1 CI and a
// client-2 asset, and an org-wide (unlinked) document, must not be changed,
// deleted, overwritten or taken over by a client-1 principal.
func TestDocumentWritesOutsideScope(t *testing.T) {
	w := newT1World(t, "d2")
	f := w.f
	blobs := &memoryBlobs{objects: map[string]string{}}
	w.env = newChannelEnvWithBlobs(t, f, blobs)
	ci1 := f.CI(t, f.OrgA, f.Client1, "t1-doc-c1")
	asset2 := f.Asset(t, f.OrgA, f.Client2, "t1-doc-a2")
	site1 := w.siteOfRoom(f.Room(t, f.Client1, "t1-doc-s1"))
	client1 := w.as("client1", only(f.Client1), all())
	client2 := w.as("client2", only(f.Client2), all())
	site := w.as("site1", all(), only(site1))
	doc := func(title string) string {
		return w.create(w.orgA, "/api/v1/documents", map[string]any{"title": title, "file_name": title + ".pdf"})
	}
	link := func(id, entityType, entity string) {
		t.Helper()
		w.create(w.orgA, "/api/v1/documents/"+id+"/links", map[string]any{"entity_type": entityType, "entity_id": entity})
	}
	upload := func(p *channelPrincipal, id, content string) (int, string) {
		t.Helper()
		return w.do(p, http.MethodPut, "/api/v1/documents/"+id+"/content", content)
	}
	shared := doc("t1-doc-shared")
	link(shared, "ci", ci1)
	link(shared, "asset", asset2)
	ciOrg := f.CI(t, f.OrgA, "", "t1-doc-org")
	orgLinked := doc("t1-doc-org-linked")
	link(orgLinked, "ci", ci1)
	link(orgLinked, "ci", ciOrg)
	orgWide := doc("t1-doc-orgwide")
	orgWideCtl := doc("t1-doc-orgwide-ctl")
	ownOnly := doc("t1-doc-own")
	link(ownOnly, "ci", ci1)
	for _, id := range []string{shared, orgWide, orgLinked} {
		if code, body := upload(w.orgA, id, "original-"+id); code != http.StatusOK {
			t.Fatalf("setup upload as org admin: %d %s", code, trim(body))
		}
	}
	stored := func(id string) string {
		t.Helper()
		return w.scalar(`SELECT storage_key || '|' || file_size::text FROM document WHERE id = $1`, id)
	}
	// uploadRefused checks that client 1 cannot overwrite the content of id.
	uploadRefused := func(t *testing.T, id string) {
		t.Helper()
		before := stored(id)
		key, _, _ := strings.Cut(before, "|")
		if code, body := upload(client1, id, "overwritten-by-c1"); code != http.StatusNotFound {
			t.Errorf("PUT content as client 1: %d %s, want 404", code, trim(body))
		}
		if got := blobs.object(key); got != `"original-`+id+`"` {
			t.Errorf("blob object after PUT by client 1: %q, want the original content", got)
		}
		if after := stored(id); after != before {
			t.Errorf("storage_key|file_size after PUT by client 1: %s, want %s", after, before)
		}
	}

	t.Run("shared document of client 1 and client 2 (WP-028 P1)", func(t *testing.T) {
		code, body := w.do(client1, http.MethodPatch, "/api/v1/documents/"+shared, map[string]any{"title": "t1-renamed-by-c1"})
		if !refused(code) {
			t.Errorf("PATCH as client 1: %d %s, want 403/404", code, trim(body))
		}
		if got := w.scalar(`SELECT title FROM document WHERE id = $1`, shared); got != "t1-doc-shared" {
			t.Errorf("title after PATCH by client 1: %q", got)
		}
		uploadRefused(t, shared)
		code, body = w.do(client1, http.MethodDelete, "/api/v1/documents/"+shared, nil)
		if !refused(code) {
			t.Errorf("DELETE as client 1: %d %s, want 403/404", code, trim(body))
		}
		if n := w.scalar(`SELECT count(*)::text FROM document_link WHERE document_id = $1 AND entity_type = 'asset' AND entity_id = $2`, shared, asset2); n != "1" {
			t.Errorf("client-2 asset links after DELETE by client 1: %s, want 1", n)
		}
		if code, _ := w.do(client2, http.MethodGet, "/api/v1/documents/"+shared, nil); code != http.StatusOK {
			t.Errorf("GET the shared document as client 2: %d, want 200", code)
		}
	})
	t.Run("document linked to a client-1 and an org-wide CI (WP-353, E-09)", func(t *testing.T) {
		if code, body := w.do(client1, http.MethodPatch, "/api/v1/documents/"+orgLinked, map[string]any{"title": "t1-renamed-by-c1"}); !refused(code) {
			t.Errorf("PATCH as client 1: %d %s, want 403/404", code, trim(body))
		}
		uploadRefused(t, orgLinked)
		if code, body := w.do(client1, http.MethodDelete, "/api/v1/documents/"+orgLinked, nil); !refused(code) {
			t.Errorf("DELETE as client 1: %d %s, want 403/404", code, trim(body))
		}
		if got := w.scalar(`SELECT title || '|' || (SELECT count(*) FROM document_link WHERE document_id = $1 AND entity_id = $2)::text FROM document WHERE id = $1`, orgLinked, ciOrg); got != "t1-doc-org-linked|1" {
			t.Errorf("title|org-wide links after the client-1 writes: %s, want t1-doc-org-linked|1", got)
		}
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/documents/"+orgLinked, map[string]any{"title": "t1-doc-org-linked-2"}); code != http.StatusOK {
			t.Errorf("PATCH as org admin: %d %s, want 200", code, trim(body))
		}
	})
	t.Run("org-wide unlinked document (WP-028 P2, TEN-05, E-09)", func(t *testing.T) {
		code, body := w.do(client1, http.MethodPatch, "/api/v1/documents/"+orgWide, map[string]any{"title": "t1-renamed-by-c1"})
		if !refused(code) {
			t.Errorf("PATCH as client 1: %d %s, want 403/404", code, trim(body))
		}
		if got := w.scalar(`SELECT title FROM document WHERE id = $1`, orgWide); got != "t1-doc-orgwide" {
			t.Errorf("title after PATCH by client 1: %q", got)
		}
		uploadRefused(t, orgWide)
		for _, p := range []*channelPrincipal{client1, site} {
			code, body = w.do(p, http.MethodPost, "/api/v1/documents", map[string]any{"title": "t1-doc-created-by-" + p.kind, "file_name": "x.pdf"})
			if code != http.StatusForbidden {
				t.Errorf("POST an org-wide (unlinked) document as %s: %d %s, want 403", p.kind, code, trim(body))
			}
		}
		if n := w.scalar(`SELECT count(*)::text FROM document WHERE organization_id = $1 AND title LIKE 't1-doc-created-by-%'`, f.OrgA); n != "0" {
			t.Errorf("documents created by scoped principals: %s, want 0", n)
		}
		// Linking the org-wide document to an own CI must not take it away
		// from client 2.
		if code, body = w.do(client1, http.MethodPost, "/api/v1/documents/"+orgWide+"/links", map[string]any{"entity_type": "ci", "entity_id": ci1}); !refused(code) {
			t.Errorf("link the org-wide document as client 1: %d %s, want 403/404", code, trim(body))
		}
		if n := w.scalar(`SELECT count(*)::text || '|' || (SELECT link_count FROM document WHERE id = $1)::text FROM document_link WHERE document_id = $1`, orgWide); n != "0|0" {
			t.Errorf("links|link_count of the org-wide document after client 1 linked it: %s, want 0|0", n)
		}
		if got, _ := w.do(client2, http.MethodGet, "/api/v1/documents/"+orgWide, nil); got != http.StatusOK {
			t.Errorf("GET the org-wide document as client 2 after client 1 linked it: %d, want 200", got)
		}
		code, body = w.do(client1, http.MethodDelete, "/api/v1/documents/"+orgWide, nil)
		if !refused(code) {
			t.Errorf("DELETE as client 1: %d %s, want 403/404", code, trim(body))
		}
		if n := w.scalar(`SELECT count(*)::text FROM document WHERE id = $1`, orgWide); n != "1" {
			t.Errorf("org-wide document rows after DELETE by client 1: %s, want 1", n)
		}
	})
	t.Run("positive control", func(t *testing.T) {
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/documents/"+orgWideCtl, map[string]any{"title": "t1-doc-orgwide-ctl-2"}); code != http.StatusOK {
			t.Errorf("PATCH org-wide document as org admin: %d %s, want 200", code, trim(body))
		}
		if code, body := w.do(w.orgA, http.MethodPatch, "/api/v1/documents/"+shared, map[string]any{"title": "t1-doc-shared-2"}); code != http.StatusOK {
			t.Errorf("PATCH shared document as org admin: %d %s, want 200", code, trim(body))
		}
		doc("t1-doc-orgwide-by-admin")
		if code, body := w.do(client1, http.MethodPatch, "/api/v1/documents/"+ownOnly, map[string]any{"title": "t1-doc-own-2"}); code != http.StatusOK {
			t.Errorf("PATCH own document as client 1: %d %s, want 200", code, trim(body))
		}
		if code, body := upload(client1, ownOnly, "own-content"); code != http.StatusOK {
			t.Errorf("PUT content of own document as client 1: %d %s, want 200", code, trim(body))
		}
		if code, body := w.do(client1, http.MethodDelete, "/api/v1/documents/"+ownOnly, nil); code != http.StatusNoContent {
			t.Errorf("DELETE own document as client 1: %d %s, want 204", code, trim(body))
		}
	})
}
