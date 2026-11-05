package assignment_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// do sends method path with the JSON body (nil: none) or raw bytes.
func (s *signFixture) do(ctx context.Context, method, path string, body any, header ...string) *httptest.ResponseRecorder {
	s.t.Helper()
	var reader *bytes.Reader
	switch b := body.(type) {
	case nil:
		reader = bytes.NewReader(nil)
	case []byte:
		reader = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			s.t.Fatal(err)
		}
		reader = bytes.NewReader(raw)
	}
	r := httptest.NewRequest(method, path, reader).WithContext(ctx)
	for i := 0; i+1 < len(header); i += 2 {
		r.Header.Set(header[i], header[i+1])
	}
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	return w
}

// verify calls GET /api/v1/stock-movements/{id}/signature.
func (s *signFixture) verify(ctx context.Context, id string) (*httptest.ResponseRecorder, assignment.SignatureVerification) {
	s.t.Helper()
	w := s.do(ctx, http.MethodGet, "/api/v1/stock-movements/"+id+"/signature", nil)
	var out assignment.SignatureVerification
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
			s.t.Fatalf("decode %s: %v", w.Body.String(), err)
		}
	}
	return w, out
}

// setScan sets the scan status of version 1 of document docID.
func (s *signFixture) setScan(docID, status string) {
	s.t.Helper()
	if _, err := s.f.Admin.Exec(context.Background(), `UPDATE document_version SET scan_status = $2, scanned_at = now()
		WHERE document_id = $1 AND version = 1`, docID, status); err != nil {
		s.t.Fatal(err)
	}
}

func isValid(v *assignment.SignatureVerification) bool { return v.Valid != nil && *v.Valid }

// chiRouter registers the routes of h on a new router.
func chiRouter(h *assignment.Handler) *chi.Mux {
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return mux
}

// TestSignatureVerification covers AST-02 (E-51, WP-748) against
// PostgreSQL and a file blob store: a fresh signature is valid (pending
// scan: 200 with scan_status pending_scan; clean as well); an exchanged
// blob is invalid; infected answers 409 document-quarantined, scan_failed
// 409 document-pending-scan; an unsigned movement, an unknown one and one
// outside the client scope answer 404; every verification writes
// stock_movement.signature_verified without image data.
func TestSignatureVerification(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp748actor")
	s := newSignFixture(t, f, actor, withSignature())
	alice := f.AppUser(t, f.OrgA, "wp748alice")
	asset := f.Asset(t, f.OrgA, f.Client1, "wp748-verify")
	ctx := s.org()
	path := "/api/v1/assets/" + asset

	w := s.call(ctx, path+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(pngImage(2048), nil)})
	if w.Code != http.StatusCreated {
		t.Fatalf("signed assign: %d %s", w.Code, w.Body.String())
	}
	a := decodeAssignment(t, w)
	stored := s.signed(a.ID)

	w, v := s.verify(ctx, a.ID)
	if w.Code != http.StatusOK || !isValid(&v) || v.ScanStatus != document.ScanPending || v.Legacy || v.Version != 1 {
		t.Fatalf("fresh signature: %d %s, want 200 valid pending_scan", w.Code, w.Body.String())
	}
	if v.MovementID != a.ID || v.DocumentID != stored.m.DocumentID || v.SignatureHash != stored.m.SignatureHash ||
		v.SignerUserID != alice || v.SignerSurrogate != "" || !v.SignedAt.Equal(a.Signature.SignedAt) {
		t.Errorf("verification %+v, stored %+v", v, stored.m)
	}
	if strings.Contains(w.Body.String(), pngPrefix) {
		t.Error("the verification holds image data")
	}
	audits := func() int {
		return s.count(`SELECT count(*) FROM audit_log WHERE organization_id = $1 AND action = $2
			AND resource_type = 'stock_movement' AND resource_id = $3`, f.OrgA, assignment.ActionSignatureVerified, a.ID)
	}
	if n := audits(); n != 1 {
		t.Fatalf("audit entries after one verification: %d", n)
	}
	var changes map[string]any
	var raw []byte
	if err := f.Admin.QueryRow(context.Background(), `SELECT changes FROM audit_log WHERE action = $1 AND resource_id = $2`,
		assignment.ActionSignatureVerified, a.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &changes); err != nil {
		t.Fatal(err)
	}
	if changes["document_id"] != stored.m.DocumentID || changes["valid"] != true || changes["scan_status"] != document.ScanPending ||
		len(changes) != 3 {
		t.Errorf("audit changes %v", changes)
	}

	s.setScan(stored.m.DocumentID, document.ScanClean)
	if w, v = s.verify(ctx, a.ID); w.Code != http.StatusOK || !isValid(&v) || v.ScanStatus != document.ScanClean {
		t.Fatalf("clean signature: %d %s", w.Code, w.Body.String())
	}

	// An exchanged image of the same size is detected.
	forged := pngImage(2048)
	forged[100] ^= 0xff
	if err := s.blobs.Put(context.Background(), document.Bucket, stored.key, bytes.NewReader(forged), int64(len(forged)), "image/png"); err != nil {
		t.Fatal(err)
	}
	if w, v = s.verify(ctx, a.ID); w.Code != http.StatusOK || v.Valid == nil || *v.Valid {
		t.Fatalf("exchanged blob: %d %s, want valid false", w.Code, w.Body.String())
	}
	if n := s.count(`SELECT count(*) FROM audit_log WHERE action = $1 AND resource_id = $2 AND changes->>'valid' = 'false'`,
		assignment.ActionSignatureVerified, a.ID); n != 1 {
		t.Errorf("audit entries with valid false: %d", n)
	}

	s.setScan(stored.m.DocumentID, document.ScanInfected)
	if w, _ = s.verify(ctx, a.ID); w.Code != http.StatusConflict || problemType(t, w) != document.ProblemQuarantined {
		t.Errorf("infected: %d %s, want 409 document-quarantined", w.Code, w.Body.String())
	}
	s.setScan(stored.m.DocumentID, document.ScanFailed)
	if w, _ = s.verify(ctx, a.ID); w.Code != http.StatusConflict || problemType(t, w) != document.ProblemPendingScan {
		t.Errorf("scan_failed: %d %s, want 409 document-pending-scan", w.Code, w.Body.String())
	}

	// An unsigned movement, an unknown one and one outside the scope.
	if w = s.call(ctx, path+"/return", map[string]any{"expected_holder": alice}); w.Code != http.StatusOK {
		t.Fatalf("unsigned return: %d %s", w.Code, w.Body.String())
	}
	unsigned := decodeMovement(t, w)
	for name, id := range map[string]string{"unsigned": unsigned.ID, "unknown": f.ID(), "malformed": "x"} {
		if w, _ = s.verify(ctx, id); w.Code != http.StatusNotFound {
			t.Errorf("%s movement: %d %s, want 404", name, w.Code, w.Body.String())
		}
	}
	if w, _ = s.verify(s.client(f.Client2), a.ID); w.Code != http.StatusNotFound {
		t.Errorf("client 2: %d %s, want 404", w.Code, w.Body.String())
	}
	s.setScan(stored.m.DocumentID, document.ScanClean)
	if w, _ = s.verify(s.client(f.Client1), a.ID); w.Code != http.StatusOK {
		t.Errorf("client 1: %d %s, want 200", w.Code, w.Body.String())
	}
	if n := audits(); n != 4 {
		t.Errorf("audit entries after four verifications: %d", n)
	}
	if n := s.count(`SELECT count(*) FROM audit_log t WHERE row_to_json(t)::text LIKE '%` + pngPrefix + `%'`); n != 0 {
		t.Errorf("audit_log holds image data in %d rows", n)
	}

	// Without service (profile without database) the route answers 501.
	mem := chiRouter(assignment.NewHandler(assignment.NewMemoryRepository()))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/stock-movements/"+a.ID+"/signature", http.NoBody).
		WithContext(tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: f.OrgA}))
	mw := httptest.NewRecorder()
	mem.ServeHTTP(mw, r)
	if mw.Code != http.StatusNotImplemented {
		t.Errorf("without service: %d, want 501", mw.Code)
	}
}

// TestSignatureVerificationGate covers the org gate (E-44, WP-748) without
// database: GET …/stock-movements/{id}/signature requires the feature
// signature; without it the middleware answers 403 feature-not-entitled.
func TestSignatureVerificationGate(t *testing.T) {
	if key, ok := entitlement.RequiredFeature(http.MethodGet, "/api/v1/stock-movements/x/signature"); !ok || key != entitlement.FeatureSignature {
		t.Fatalf("RequiredFeature = %q, %v; want signature", key, ok)
	}
	svc := entitlement.NewService(entitlement.NewMemoryRepository(), entitlement.Options{Enforce: true})
	h := svc.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	r := httptest.NewRequest(http.MethodGet, "/api/v1/stock-movements/x/signature", http.NoBody).
		WithContext(tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: "org-1"}))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden || problemType(t, w) != "https://reticora.io/problems/feature-not-entitled" {
		t.Fatalf("without signature: %d %s, want 403 feature-not-entitled", w.Code, w.Body.String())
	}
}

// TestSignatureDocumentLocked covers the lock of signature documents
// (E-51, WP-748): PATCH, content PUT, a new version, the soft delete, the
// removal of a link and a new link answer 409; creating a document or
// patching one with the category signature 422; afterwards the
// verification stays valid.
func TestSignatureDocumentLocked(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp748lactor")
	s := newSignFixture(t, f, actor, withSignature())
	document.NewHandler(document.NewPGRepository(f.App), s.blobs).RegisterRoutes(s.mux)
	alice := f.AppUser(t, f.OrgA, "wp748lalice")
	asset := f.Asset(t, f.OrgA, f.Client1, "wp748-lock")
	other := f.Asset(t, f.OrgA, f.Client1, "wp748-lock-other")
	ctx := s.org()

	w := s.call(ctx, "/api/v1/assets/"+asset+"/assign", map[string]any{"assigned_user_id": alice, "signature": sig(pngImage(2048), nil)})
	if w.Code != http.StatusCreated {
		t.Fatalf("signed assign: %d %s", w.Code, w.Body.String())
	}
	a := decodeAssignment(t, w)
	doc := a.Signature.DocumentID
	var linkID string
	if err := f.Admin.QueryRow(context.Background(), `SELECT id::text FROM document_link WHERE document_id = $1`, doc).Scan(&linkID); err != nil {
		t.Fatal(err)
	}
	base := "/api/v1/documents/" + doc
	png := pngImage(1024)
	for _, c := range []struct {
		name, method, path string
		body               any
		header             []string
	}{
		{"PATCH", http.MethodPatch, base, map[string]any{"title": "renamed"}, nil},
		{"content PUT", http.MethodPut, base + "/content", png, []string{"Content-Type", "image/png"}},
		{"new version", http.MethodPost, base + "/versions", png, []string{"Content-Type", "image/png"}},
		{"soft delete", http.MethodDelete, base, nil, nil},
		{"unlink", http.MethodDelete, base + "/links/" + linkID, nil, nil},
		{"new link", http.MethodPost, base + "/links", map[string]any{"entity_type": "asset", "entity_id": other}, nil},
	} {
		w = s.do(ctx, c.method, c.path, c.body, c.header...)
		if w.Code != http.StatusConflict || problemType(t, w) != "https://reticora.io/problems/409" {
			t.Errorf("%s: %d %s, want 409", c.name, w.Code, w.Body.String())
		}
	}

	w = s.do(ctx, http.MethodPost, "/api/v1/documents", map[string]any{"title": "fake", "file_name": "fake.png", "category": "signature"})
	if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), `"category"`) {
		t.Errorf("create with category signature: %d %s, want 422 on category", w.Code, w.Body.String())
	}
	w = s.do(ctx, http.MethodPost, "/api/v1/documents", map[string]any{"title": "plain", "file_name": "plain.txt"})
	if w.Code != http.StatusCreated {
		t.Fatalf("create plain document: %d %s", w.Code, w.Body.String())
	}
	var plain document.Document
	if err := json.Unmarshal(w.Body.Bytes(), &plain); err != nil {
		t.Fatal(err)
	}
	w = s.do(ctx, http.MethodPatch, "/api/v1/documents/"+plain.ID, map[string]any{"category": "signature"})
	if w.Code != http.StatusUnprocessableEntity {
		t.Errorf("patch to category signature: %d %s, want 422", w.Code, w.Body.String())
	}
	if w = s.do(ctx, http.MethodPatch, "/api/v1/documents/"+plain.ID, map[string]any{"title": "still plain"}); w.Code != http.StatusOK {
		t.Errorf("patch of a plain document: %d %s", w.Code, w.Body.String())
	}

	if n := s.count(`SELECT count(*) FROM document_version WHERE document_id = $1`, doc); n != 1 {
		t.Errorf("versions of the signature document: %d, want 1", n)
	}
	if n := s.count(`SELECT count(*) FROM document WHERE id = $1 AND deleted_at IS NULL AND title LIKE 'Signature %'`, doc); n != 1 {
		t.Error("the signature document was changed or deleted")
	}
	if n := s.count(`SELECT count(*) FROM document_link WHERE document_id = $1`, doc); n != 1 {
		t.Errorf("links of the signature document: %d, want 1", n)
	}
	if w, v := s.verify(ctx, a.ID); w.Code != http.StatusOK || !isValid(&v) {
		t.Errorf("verification after the refused changes: %d %s", w.Code, w.Body.String())
	}
}

// checkEventData validates data against the $defs entry typ of
// api/events.schema.json: required fields, only declared fields beside
// the open ones of the producer, strings with their uuid and date-time
// formats.
func checkEventData(t *testing.T, typ string, data map[string]any) {
	t.Helper()
	raw, err := os.ReadFile("../../../api/events.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Defs map[string]struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Type   string `json:"type"`
				Format string `json:"format"`
			} `json:"properties"`
		} `json:"$defs"`
	}
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	def, ok := schema.Defs[typ]
	if !ok {
		t.Fatalf("no $defs entry %s", typ)
	}
	for _, name := range def.Required {
		if _, present := data[name]; !present {
			t.Errorf("%s: required %s missing in %v", typ, name, data)
		}
	}
	for name, value := range data {
		prop, declared := def.Properties[name]
		if !declared {
			t.Errorf("%s: field %s is not declared", typ, name)
			continue
		}
		str, isString := value.(string)
		if prop.Type == "string" && !isString {
			t.Errorf("%s.%s = %v, want a string", typ, name, value)
			continue
		}
		switch prop.Format {
		case "uuid":
			if !uuidPattern.MatchString(str) {
				t.Errorf("%s.%s is no UUID", typ, name)
			}
		case "date-time":
			if _, parseErr := time.Parse(time.RFC3339Nano, str); parseErr != nil {
				t.Errorf("%s.%s is no date-time: %v", typ, name, parseErr)
			}
		}
	}
}

// outboxData returns the data of the events typ of the asset, oldest
// first.
func (s *signFixture) outboxData(typ, assetID string) []map[string]any {
	s.t.Helper()
	rows, err := s.f.Admin.Query(context.Background(), `SELECT data FROM event_outbox
		WHERE type = $1 AND entity_id = $2 ORDER BY sequence`, typ, assetID)
	if err != nil {
		s.t.Fatal(err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			s.t.Fatal(err)
		}
		var data map[string]any
		if err = json.Unmarshal(raw, &data); err != nil {
			s.t.Fatal(err)
		}
		out = append(out, data)
	}
	if err = rows.Err(); err != nil {
		s.t.Fatal(err)
	}
	return out
}

// TestAssignmentEventFields covers the event contract (EVT-03, WP-246,
// WP-748): asset.assigned of a team assignment carries movement_id,
// assigned_team_id, due_at and client_id; asset.returned of a transfer
// the previous holder; asset.assigned of a signed assignment the
// signature references and validates against api/events.schema.json; no
// event holds image data.
func TestAssignmentEventFields(t *testing.T) {
	f := scopetest.Seed(t, "a6")
	actor := f.AppUser(t, f.OrgA, "wp748eactor")
	s := newSignFixture(t, f, actor, withSignature())
	alice := f.AppUser(t, f.OrgA, "wp748ealice")
	asset := f.Asset(t, f.OrgA, f.Client1, "wp748-events")
	crew := f.ID()
	if _, err := f.Admin.Exec(context.Background(), `INSERT INTO team (id, organization_id, name) VALUES ($1, $2, 'wp748-crew')`, crew, f.OrgA); err != nil {
		t.Fatal(err)
	}
	ctx := s.org()
	path := "/api/v1/assets/" + asset
	due := time.Date(2026, 12, 1, 17, 0, 0, 0, time.UTC)

	w := s.call(ctx, path+"/assign", map[string]any{"assigned_team_id": crew, "due_at": due})
	if w.Code != http.StatusCreated {
		t.Fatalf("team assign: %d %s", w.Code, w.Body.String())
	}
	team := decodeAssignment(t, w)
	w = s.call(ctx, path+"/transfer", map[string]any{"expected_holder": crew, "assigned_user_id": alice,
		"signature": sig(pngImage(2048), nil)})
	if w.Code != http.StatusCreated {
		t.Fatalf("signed transfer: %d %s", w.Code, w.Body.String())
	}
	next := decodeAssignment(t, w)

	assigned := s.outboxData("asset.assigned", asset)
	returned := s.outboxData("asset.returned", asset)
	if len(assigned) != 2 || len(returned) != 1 {
		t.Fatalf("events: %d asset.assigned, %d asset.returned", len(assigned), len(returned))
	}
	first := assigned[0]
	if first["movement_id"] != team.ID || first["assigned_team_id"] != crew || first["client_id"] != f.Client1 ||
		first["assigned_user_id"] != nil || first["signature_hash"] != nil {
		t.Errorf("asset.assigned of the team assignment: %v", first)
	}
	if at, err := time.Parse(time.RFC3339Nano, first["due_at"].(string)); err != nil || !at.Equal(due) {
		t.Errorf("due_at = %v, want %s", first["due_at"], due)
	}
	if returned[0]["assigned_team_id"] != crew || returned[0]["assigned_user_id"] != nil || returned[0]["movement_id"] == next.ID {
		t.Errorf("asset.returned of the transfer: %v, want the previous holder", returned[0])
	}
	signed := assigned[1]
	stored := s.signed(next.ID)
	if signed["movement_id"] != next.ID || signed["assigned_user_id"] != alice ||
		signed["signature_document_id"] != stored.m.DocumentID || signed["signature_hash"] != stored.m.SignatureHash ||
		signed["signer_user_id"] != alice || signed["signer_surrogate"] != nil {
		t.Errorf("asset.assigned of the signed transfer: %v", signed)
	}
	for _, data := range assigned {
		checkEventData(t, "asset.assigned", data)
	}
	checkEventData(t, "asset.returned", returned[0])
	if n := s.count(`SELECT count(*) FROM event_outbox t WHERE row_to_json(t)::text LIKE '%` + pngPrefix + `%'`); n != 0 {
		t.Errorf("event_outbox holds image data in %d rows", n)
	}
}
