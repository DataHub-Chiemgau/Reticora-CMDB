package user_test

// The mobile number of a user (NTF-02, NTF-03, WP-1140, E-164) against
// PostgreSQL: it is stored only as PII vault subject (app_user keeps the
// surrogate), checked for E.164, written through PATCH /users/{id} and the
// own PATCH /me, answered masked and in plaintext only by the data export,
// referenced in the route audit only by its surrogate and shredded by the
// anonymization; the composite foreign key keeps the subject in the user's
// organization.
//
// Retest (needs a migrated database):
//
//	cd backend && TEST_DATABASE_URL=… go test -count=1 -v -run '^TestUserMobile|^TestPatchMe|^TestAnonymizeDeletesMobile' ./internal/user/

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/pii"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
)

const mobileNumber = "+4915112345689"

type mobileFixture struct {
	f     *scopetest.Fixture
	mux   chi.Router
	admin string
	vault *pii.Vault
}

func newMobileFixture(t *testing.T, withVault bool) *mobileFixture {
	t.Helper()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	f := scopetest.Seed(t, "b4")
	m := &mobileFixture{f: f, admin: f.AppUser(t, f.OrgA, "mobile-admin")}
	if withVault {
		key, err := crypto.GenerateMasterKey()
		if err != nil {
			t.Fatal(err)
		}
		enc, err := crypto.NewEnvelopeEncryptor(key)
		if err != nil {
			t.Fatal(err)
		}
		m.vault = pii.NewVault(f.App, enc)
		pii.SetDefault(m.vault)
		t.Cleanup(func() { pii.SetDefault(nil) })
	}
	m.mux = chi.NewRouter()
	user.NewHandler(user.NewPGRepository(f.App)).RegisterRoutes(m.mux)
	return m
}

// as sends the request as an org-wide principal of organization A with
// callerID as its user.
func (m *mobileFixture) as(callerID, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	scope := database.OrgWideScope(m.f.OrgA, callerID)
	ctx := tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: m.f.OrgA, UserID: callerID})
	w := httptest.NewRecorder()
	m.mux.ServeHTTP(w, req.WithContext(database.ContextWithTenantScope(ctx, &scope)))
	return w
}

// ctx is a context with the org-wide tenant scope of org (vault, snapshots).
func (m *mobileFixture) ctx(org string) context.Context {
	scope := database.OrgWideScope(org, "")
	return database.ContextWithTenantScope(context.Background(), &scope)
}

func (m *mobileFixture) subject(t *testing.T, userID string) string {
	t.Helper()
	var id string
	if err := m.f.Admin.QueryRow(context.Background(),
		`SELECT COALESCE(mobile_pii_subject_id::text, '') FROM app_user WHERE id = $1`, userID).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func decode(t *testing.T, w *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out
}

func TestUserMobileStoredAsSurrogate(t *testing.T) {
	m := newMobileFixture(t, true)
	member := m.f.AppUser(t, m.f.OrgA, "mobile-member")
	w := m.as(m.admin, http.MethodPatch, "/api/v1/users/"+member, `{"mobile":"+49 151-1234 5689"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH mobile: %d %s", w.Code, w.Body.String())
	}
	subject := m.subject(t, member)
	if len(subject) != 36 {
		t.Fatalf("mobile_pii_subject_id = %q, want a UUID", subject)
	}
	var kind string
	var payload []byte
	if err := m.f.Admin.QueryRow(context.Background(), `SELECT kind, encrypted_payload FROM pii_subject
		WHERE surrogate_id = $1 AND organization_id = $2`, subject, m.f.OrgA).Scan(&kind, &payload); err != nil {
		t.Fatal(err)
	}
	if kind != pii.KindUser || strings.Contains(string(payload), "4915112345689") {
		t.Errorf("pii_subject kind %q, payload holds the plaintext: %v", kind, strings.Contains(string(payload), "4915112345689"))
	}
	var row string
	if err := m.f.Admin.QueryRow(context.Background(), `SELECT to_jsonb(u)::text FROM app_user u WHERE id = $1`, member).Scan(&row); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(row, "4915112345689") {
		t.Errorf("app_user holds the plaintext: %s", row)
	}
	got, err := m.vault.Resolve(m.ctx(m.f.OrgA), m.f.OrgA, subject)
	if err != nil || got.Data["mobile"] != mobileNumber {
		t.Errorf("vault subject = %+v (%v), want the normalized number", got, err)
	}

	// A new number replaces the subject; the former one is deleted.
	if w = m.as(m.admin, http.MethodPatch, "/api/v1/users/"+member, `{"mobile":"+43 664 1234567"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH second mobile: %d %s", w.Code, w.Body.String())
	}
	if next := m.subject(t, member); next == subject || next == "" {
		t.Errorf("subject after the change %q, want a new one", next)
	}
	if _, err = m.vault.Resolve(m.ctx(m.f.OrgA), m.f.OrgA, subject); err == nil {
		t.Error("the former number still resolves")
	}
	// An empty value clears it.
	if w = m.as(m.admin, http.MethodPatch, "/api/v1/users/"+member, `{"mobile":""}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH empty mobile: %d %s", w.Code, w.Body.String())
	}
	if s := m.subject(t, member); s != "" {
		t.Errorf("subject after clearing %q, want none", s)
	}
	if _, ok := decode(t, w)["mobile_masked"]; ok {
		t.Error("mobile_masked after clearing")
	}
}

func TestUserMobileInvalidE164Returns422(t *testing.T) {
	m := newMobileFixture(t, true)
	member := m.f.AppUser(t, m.f.OrgA, "mobile-invalid")
	for _, number := range []string{"0151 12345689", "+0151123456", "+4915112345678901", "+49 abc", "+12345"} {
		for _, path := range []string{"/api/v1/users/" + member, "/api/v1/me"} {
			caller := m.admin
			if path == "/api/v1/me" {
				caller = member
			}
			w := m.as(caller, http.MethodPatch, path, `{"mobile":"`+number+`"}`)
			if w.Code != http.StatusUnprocessableEntity {
				t.Errorf("%s %q: %d %s, want 422", path, number, w.Code, w.Body.String())
				continue
			}
			if field := decode(t, w)["field"]; field != "mobile" {
				t.Errorf("%s %q: field %v, want mobile", path, number, field)
			}
		}
	}
	if s := m.subject(t, member); s != "" {
		t.Errorf("subject after refused numbers %q", s)
	}
}

func TestPatchMeMobileOnlyOwnAccount(t *testing.T) {
	m := newMobileFixture(t, true)
	member := m.f.AppUser(t, m.f.OrgA, "mobile-self")
	other := m.f.AppUser(t, m.f.OrgA, "mobile-other")
	for _, body := range []string{`{"display_name":"x"}`, `{"mobile":"+4915112345689","status":"inactive"}`, `{"id":"` + other + `"}`} {
		w := m.as(member, http.MethodPatch, "/api/v1/me", body)
		if w.Code != http.StatusUnprocessableEntity {
			t.Errorf("PATCH /me %s: %d %s, want 422", body, w.Code, w.Body.String())
		}
	}
	w := m.as(member, http.MethodPatch, "/api/v1/me", `{"mobile":"+4915112345689"}`)
	if w.Code != http.StatusOK {
		t.Fatalf("PATCH /me mobile: %d %s", w.Code, w.Body.String())
	}
	if got := decode(t, w)["id"]; got != member {
		t.Errorf("PATCH /me answered user %v, want the caller %s", got, member)
	}
	if m.subject(t, member) == "" || m.subject(t, other) != "" {
		t.Error("PATCH /me must change exactly the own account")
	}
	// A principal without a user account has no own account.
	if w = m.as("", http.MethodPatch, "/api/v1/me", `{}`); w.Code != http.StatusForbidden {
		t.Errorf("PATCH /me without a user: %d, want 403", w.Code)
	}
}

func TestUserMobileMaskedInResponses(t *testing.T) {
	m := newMobileFixture(t, true)
	member := m.f.AppUser(t, m.f.OrgA, "mobile-masked")
	w := m.as(m.admin, http.MethodPatch, "/api/v1/users/"+member, `{"mobile":"`+mobileNumber+`"}`)
	const masked = "+49151******89"
	check := func(name string, w *httptest.ResponseRecorder, value any) {
		t.Helper()
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body.String())
		}
		if value != masked {
			t.Errorf("%s: mobile_masked %v, want %s", name, value, masked)
		}
		if strings.Contains(w.Body.String(), "4915112345689") {
			t.Errorf("%s answers the plaintext: %s", name, w.Body.String())
		}
	}
	check("PATCH /users/{id}", w, decode(t, w)["mobile_masked"])
	w = m.as(m.admin, http.MethodGet, "/api/v1/users/"+member, "")
	check("GET /users/{id}", w, decode(t, w)["mobile_masked"])
	w = m.as(m.admin, http.MethodGet, "/api/v1/users?limit=200", "")
	var found any
	for _, item := range decode(t, w)["data"].([]any) {
		if u := item.(map[string]any); u["id"] == member {
			found = u["mobile_masked"]
		}
	}
	check("GET /users", w, found)
	// PATCH /me with {} changes nothing and answers the own state.
	w = m.as(member, http.MethodPatch, "/api/v1/me", `{}`)
	check("PATCH /me {}", w, decode(t, w)["mobile_masked"])
	if got := user.MaskMobile("+12025550123"); got != "+1202*****23" {
		t.Errorf("MaskMobile(+12025550123) = %s", got)
	}
	// Short E.164 numbers (7 and 8 digits) keep at least two digits hidden.
	for number, want := range map[string]string{
		"+6831234":  "+683**34",
		"+4912345":  "+491**45",
		"+49123456": "+4912**56",
		"+1234567":  "+123**67",
	} {
		got := user.MaskMobile(number)
		if got != want || strings.Count(got, "*") < 2 {
			t.Errorf("MaskMobile(%s) = %s, want %s", number, got, want)
		}
	}
}

func TestUserMobileInDataExport(t *testing.T) {
	m := newMobileFixture(t, true)
	member := m.f.AppUser(t, m.f.OrgA, "mobile-export")
	if w := m.as(member, http.MethodPatch, "/api/v1/me", `{"mobile":"`+mobileNumber+`"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH /me: %d %s", w.Code, w.Body.String())
	}
	w := m.as(m.admin, http.MethodGet, "/api/v1/users/"+member+"/data-export", "")
	if w.Code != http.StatusOK {
		t.Fatalf("export: %d %s", w.Code, w.Body.String())
	}
	u := decode(t, w)["user"].(map[string]any)
	if u["mobile"] != mobileNumber || u["mobile_masked"] != "+49151******89" {
		t.Errorf("export user mobile %v masked %v, want the plaintext and the mask", u["mobile"], u["mobile_masked"])
	}
}

// TestUserMobileAuditSealed: the route audit of both write paths names the
// number only as reference {"$pii": <subject>} of its vault subject, never
// the plaintext; after the anonymization the reference resolves no more.
func TestUserMobileAuditSealed(t *testing.T) {
	m := newMobileFixture(t, true)
	member := m.f.AppUser(t, m.f.OrgA, "mobile-audit")
	auditor := audit.NewPGRouteAuditor(m.f.App)
	ctx := m.ctx(m.f.OrgA)
	for _, tc := range []struct {
		route, path, caller string
		key                 func(string) string
	}{
		{"PATCH /api/v1/users/{id}", "/api/v1/users/" + member, m.admin, func(string) string { return member }},
		{"PATCH /api/v1/me", "/api/v1/me", member, func(from string) string {
			if from == "$user" {
				return member
			}
			return ""
		}},
	} {
		spec, ok := audit.RouteSnapshotFor(tc.route)
		if !ok || spec.Table != "app_user" {
			t.Fatalf("%s: register entry %+v", tc.route, spec)
		}
		keys, ok := spec.KeyValues(tc.key)
		if !ok {
			t.Fatalf("%s: no key values", tc.route)
		}
		before, _, err := auditor.RouteSnapshot(ctx, m.f.OrgA, &spec, keys)
		if err != nil {
			t.Fatal(err)
		}
		number := "+4915112345689"
		if tc.caller == member {
			number = "+4915112345600"
		}
		if w := m.as(tc.caller, http.MethodPatch, tc.path, `{"mobile":"`+number+`"}`); w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.route, w.Code, w.Body.String())
		}
		after, _, err := auditor.RouteSnapshot(ctx, m.f.OrgA, &spec, keys)
		if err != nil {
			t.Fatal(err)
		}
		changes := spec.ValueChanges("app_user", before, after)
		raw, _ := json.Marshal(changes)
		if strings.Contains(string(raw), strings.TrimPrefix(number, "+")) {
			t.Errorf("%s: audit values hold the plaintext: %s", tc.route, raw)
		}
		var ref string
		for _, c := range changes {
			if c["field"] == "mobile_pii_subject_id" {
				t.Errorf("%s: the entry names the column instead of the field mobile: %s", tc.route, raw)
			}
			if c["field"] == "mobile" {
				ref, _ = pii.RefID(c["new"])
			}
		}
		if ref == "" || ref != m.subject(t, member) {
			t.Errorf("%s: audit reference %q, want {\"$pii\": %s} in %s", tc.route, ref, m.subject(t, member), raw)
		}
	}
	// After the anonymization the reference resolves nothing.
	subject := m.subject(t, member)
	if w := m.as(m.admin, http.MethodPost, "/api/v1/users/"+member+"/anonymize", ""); w.Code != http.StatusOK {
		t.Fatalf("anonymize: %d %s", w.Code, w.Body.String())
	}
	if _, err := m.vault.Resolve(ctx, m.f.OrgA, subject); err == nil {
		t.Error("the audit reference still resolves after the anonymization")
	}
}

func TestAnonymizeDeletesMobileSubject(t *testing.T) {
	m := newMobileFixture(t, true)
	member := m.f.AppUser(t, m.f.OrgA, "mobile-anon")
	if w := m.as(m.admin, http.MethodPatch, "/api/v1/users/"+member, `{"mobile":"`+mobileNumber+`"}`); w.Code != http.StatusOK {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body.String())
	}
	subject := m.subject(t, member)
	w := m.as(m.admin, http.MethodPost, "/api/v1/users/"+member+"/anonymize", "")
	if w.Code != http.StatusOK {
		t.Fatalf("anonymize: %d %s", w.Code, w.Body.String())
	}
	if _, ok := decode(t, w)["mobile_masked"]; ok {
		t.Error("mobile_masked after the anonymization")
	}
	var n int
	if err := m.f.Admin.QueryRow(context.Background(), `SELECT count(*) FROM pii_subject WHERE surrogate_id = $1`, subject).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 || m.subject(t, member) != "" {
		t.Errorf("after the anonymization: %d subjects, column %q; want both gone", n, m.subject(t, member))
	}
	w = m.as(m.admin, http.MethodGet, "/api/v1/users/"+member+"/data-export", "")
	if strings.Contains(w.Body.String(), "4915112345689") {
		t.Error("the export still shows the number")
	}
}

func TestUserMobileForeignOrgSubjectRejected(t *testing.T) {
	m := newMobileFixture(t, true)
	ctx := context.Background()
	member := m.f.AppUser(t, m.f.OrgA, "mobile-foreign")
	foreign, err := m.vault.Create(m.ctx(m.f.OrgB), m.f.OrgB, pii.KindUser, pii.Data{"mobile": mobileNumber})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.f.Admin.Exec(ctx, `UPDATE app_user SET mobile_pii_subject_id = $2 WHERE id = $1`, member, foreign); err == nil {
		t.Fatal("a subject of another organization was accepted")
	} else if !strings.Contains(err.Error(), "app_user_mobile_pii_subject_id_fkey") {
		t.Errorf("refused with %v, want the composite foreign key", err)
	}
}

// TestUserMobileWithoutVault: without a vault a number cannot be stored
// (503) and nothing changes; clearing stays possible.
func TestUserMobileWithoutVault(t *testing.T) {
	m := newMobileFixture(t, false)
	pii.SetDefault(nil)
	member := m.f.AppUser(t, m.f.OrgA, "mobile-novault")
	if w := m.as(member, http.MethodPatch, "/api/v1/me", `{"mobile":"`+mobileNumber+`"}`); w.Code != http.StatusServiceUnavailable {
		t.Errorf("PATCH /me without vault: %d %s, want 503", w.Code, w.Body.String())
	}
	if w := m.as(member, http.MethodPatch, "/api/v1/me", `{"mobile":""}`); w.Code != http.StatusOK {
		t.Errorf("clearing without vault: %d %s", w.Code, w.Body.String())
	}
}
