package asset

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// TestAssetLabel covers INV-03 (WP-849, E-52, E-38): GET
// /api/v1/assets/{id}/label answers SVG, PNG and PDF with their media
// types, an unknown format or template is 422, label.svg is gone (404),
// and the label carries no host from base_url or the Host header.
func TestAssetLabel(t *testing.T) {
	const org = "org-849"
	repo := NewMemoryRepository()
	a := &Asset{OrganizationID: org, AssetTag: "INV-849", Name: "Notebook 849", Manufacturer: "Acme", Model: "X1",
		Category: "hardware", Status: "in_stock"}
	if err := repo.Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	mux := chi.NewRouter()
	NewHandler(repo).RegisterRoutes(mux)
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org})

	get := func(ctx context.Context, target string, want int) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, target, nil).WithContext(ctx)
		req.Host = "attacker.example"
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("GET %s: %d, want %d: %s", target, rec.Code, want, rec.Body.String())
		}
		return rec
	}
	base := "/api/v1/assets/" + a.ID + "/label"

	for format, want := range map[string]string{"svg": "image/svg+xml", "png": "image/png", "pdf": "application/pdf"} {
		for _, template := range []string{"", "&template=bin_100x50", "&template=a4_24"} {
			rec := get(ctx, base+"?format="+format+template+"&base_url=https://phish.example", http.StatusOK)
			if got := rec.Header().Get("Content-Type"); got != want {
				t.Errorf("format %s: Content-Type %q, want %q", format, got, want)
			}
			for _, host := range []string{"attacker.example", "phish.example", "/assets/"} {
				if bytes.Contains(rec.Body.Bytes(), []byte(host)) {
					t.Errorf("format %s: label carries %q", format, host)
				}
			}
		}
	}
	// Without format the label is SVG with the inventory number.
	svg := get(ctx, base, http.StatusOK)
	if svg.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(svg.Body.String(), ">INV-849<") ||
		!strings.Contains(svg.Body.String(), ">Notebook 849<") {
		t.Errorf("default label: %s", svg.Body.String())
	}

	for _, query := range []string{"?format=gif", "?format=SVG", "?format=pdf&template=a5", "?format=svg&template=x"} {
		rec := get(ctx, base+query, http.StatusUnprocessableEntity)
		if !strings.Contains(rec.Body.String(), "format") && !strings.Contains(rec.Body.String(), "template") {
			t.Errorf("%s: 422 without the field: %s", query, rec.Body.String())
		}
	}
	get(ctx, "/api/v1/assets/"+a.ID+"/label.svg", http.StatusNotFound)
	get(ctx, "/api/v1/assets/00000000-0000-4000-8000-000000000849/label", http.StatusNotFound)
	get(context.Background(), base, http.StatusUnauthorized)

	// An inventory number the caller may not read is not on the label;
	// the URN of the asset is the code instead.
	const role = "00000000-0000-4000-8000-000000000850"
	grants := permission.NewMemoryFieldGrants()
	rolePtr, key := role, "core.asset_tag"
	if _, err := grants.CreateFieldGrant(context.Background(), org, &permission.FieldGrant{RoleID: &rolePtr,
		ObjectKind: permission.KindAsset, FieldKey: &key, Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	policy, err := permission.NewFieldResolver(nil, nil, grants, nil).PolicyForGrants(context.Background(), org,
		[]identity.Grant{{Permissions: []identity.Permission{"asset:read"}, RoleID: role}}, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	restricted := get(permission.WithPolicy(ctx, policy), base+"?format=svg", http.StatusOK).Body.String()
	if strings.Contains(restricted, "INV-849") || !strings.Contains(restricted, ">urn:reticora:") {
		t.Errorf("restricted label: %s", restricted)
	}
	visible, err := labelFields(policy, a)
	if err != nil {
		t.Fatal(err)
	}
	if s := LabelSubject(visible); s.Code != labels.URN(labels.KindAsset, a.ID) || s.Title != a.Name {
		t.Errorf("restricted subject: %+v", s)
	}
}

// TestLabelSubject: the code is the inventory number, without one the URN.
func TestLabelSubject(t *testing.T) {
	const id = "0B9F7F3E-6A51-4C2E-9D0E-3F5B8A1C2D41"
	s := LabelSubject(&Asset{ID: id, AssetTag: "INV-1", Name: "Drucker", Model: "M2"})
	if s.Kind != labels.KindAsset || s.Code != "INV-1" || s.Title != "Drucker" || s.Subtitle != "M2" {
		t.Errorf("subject: %+v", s)
	}
	s = LabelSubject(&Asset{ID: id})
	if want := "urn:reticora:asset:" + strings.ToLower(id); s.Code != want || s.Title != want {
		t.Errorf("subject without inventory number: %+v, want code %s", s, want)
	}
}
