package asset

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// fakeReservations is the reservation port of the asset handler: the
// active reservation per asset id, or err.
type fakeReservations struct {
	active map[string]*ReservationRef
	err    error
}

func (f *fakeReservations) ActiveReservation(_ context.Context, _, assetID string) (*ReservationRef, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.active[assetID], nil
}

// fakeCIs is the CI port of the asset handler with one CI.
type fakeCIs struct{ ref *CIRef }

func (f fakeCIs) GetByID(_ context.Context, _, id string) (*CIRef, error) {
	if f.ref == nil || f.ref.ID != id {
		return nil, errors.New("ci not found")
	}
	return f.ref, nil
}

// TestGetAssetActiveReservation covers active_reservation in GET
// /api/v1/assets/{id} (API-06, E-38, WP-750): absent without an active
// reservation, with id, reserved_until, expires_at and assignee_id or
// project_ref otherwise, in both forms (with and without primary CI);
// the list does not carry it, and a restricted asset field policy leaves
// it out (fail-closed, convention 11). A failing lookup is a 500.
func TestGetAssetActiveReservation(t *testing.T) {
	const org = "org-750"
	repo := NewMemoryRepository()
	mk := func(tag, ciID string) string {
		t.Helper()
		a := &Asset{OrganizationID: org, AssetTag: tag, Name: tag, Category: "hardware", Status: "in_stock", CIID: ciID}
		if err := repo.Create(context.Background(), a); err != nil {
			t.Fatalf("create asset %s: %v", tag, err)
		}
		return a.ID
	}
	const ciID = "00000000-0000-4000-8000-000000000750"
	free := mk("free", "")
	user := mk("user", "")
	project := mk("project", ciID)

	until := time.Date(2027, 1, 31, 12, 0, 0, 0, time.UTC)
	expires := until.Add(time.Hour)
	reservations := &fakeReservations{active: map[string]*ReservationRef{
		user:    {ID: "00000000-0000-4000-8000-000000000751", ReservedUntil: &until, ExpiresAt: &until, AssigneeID: "00000000-0000-4000-8000-000000000752"},
		project: {ID: "00000000-0000-4000-8000-000000000753", ReservedUntil: &until, ExpiresAt: &expires, ProjectRef: "PRJ-750"},
	}}
	mux := chi.NewRouter()
	NewHandler(repo).WithCIs(fakeCIs{ref: &CIRef{ID: ciID, Name: "srv-750", Status: "active"}}).
		WithReservations(reservations).RegisterRoutes(mux)

	get := func(ctx context.Context, path string, want int) map[string]json.RawMessage {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("GET %s: %d, want %d: %s", path, rec.Code, want, rec.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode %s: %v", rec.Body.String(), err)
		}
		return body
	}
	ctx := tenant.WithTenant(context.Background(), tenant.TenantInfo{OrganizationID: org})
	reservationOf := func(body map[string]json.RawMessage) *ReservationRef {
		t.Helper()
		raw, ok := body["active_reservation"]
		if !ok {
			return nil
		}
		var ref ReservationRef
		if err := json.Unmarshal(raw, &ref); err != nil {
			t.Fatalf("decode active_reservation %s: %v", raw, err)
		}
		return &ref
	}

	if got := reservationOf(get(ctx, "/api/v1/assets/"+free, http.StatusOK)); got != nil {
		t.Errorf("asset without reservation answers active_reservation %+v", got)
	}

	got := reservationOf(get(ctx, "/api/v1/assets/"+user, http.StatusOK))
	want := reservations.active[user]
	if got == nil || got.ID != want.ID || got.AssigneeID != want.AssigneeID || got.ProjectRef != "" ||
		got.ReservedUntil == nil || !got.ReservedUntil.Equal(until) || got.ExpiresAt == nil || !got.ExpiresAt.Equal(until) {
		t.Errorf("user reservation: %+v, want %+v", got, want)
	}

	// With primary CI both members stand side by side.
	body := get(ctx, "/api/v1/assets/"+project, http.StatusOK)
	if _, ok := body["primary_ci"]; !ok {
		t.Errorf("asset with CI lacks primary_ci: %v", body)
	}
	got = reservationOf(body)
	if got == nil || got.ProjectRef != "PRJ-750" || got.AssigneeID != "" || got.ExpiresAt == nil || !got.ExpiresAt.Equal(expires) {
		t.Errorf("project reservation: %+v", got)
	}

	// The list does not carry it, and the stored asset stays unchanged.
	req := httptest.NewRequest(http.MethodGet, "/api/v1/assets", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var list struct {
		Data []map[string]json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || rec.Code != http.StatusOK || len(list.Data) != 3 {
		t.Fatalf("list: %d %s (%v)", rec.Code, rec.Body.String(), err)
	}
	for _, item := range list.Data {
		if _, ok := item["active_reservation"]; ok {
			t.Errorf("list carries active_reservation: %v", item)
		}
	}

	// A restricted asset policy leaves the member out (no grantable field
	// key, fail-closed), with and without primary CI.
	const role = "00000000-0000-4000-8000-000000000754"
	grants := permission.NewMemoryFieldGrants()
	rolePtr, key := role, "core.notes"
	if _, err := grants.CreateFieldGrant(context.Background(), org, &permission.FieldGrant{RoleID: &rolePtr,
		ObjectKind: permission.KindAsset, FieldKey: &key, Level: permission.LevelNone}); err != nil {
		t.Fatal(err)
	}
	policy, err := permission.NewFieldResolver(nil, nil, grants, nil).PolicyForGrants(context.Background(), org,
		[]identity.Grant{{Permissions: []identity.Permission{"asset:read"}, RoleID: role}}, nil)
	if err != nil {
		t.Fatalf("policy: %v", err)
	}
	restricted := permission.WithPolicy(ctx, policy)
	for _, id := range []string{user, project} {
		if body := get(restricted, "/api/v1/assets/"+id, http.StatusOK); reservationOf(body) != nil {
			t.Errorf("restricted policy reads active_reservation of %s: %v", id, body)
		}
	}

	// A failing lookup does not answer the asset without its reservation.
	reservations.err = errors.New("lookup failed")
	get(ctx, "/api/v1/assets/"+user, http.StatusInternalServerError)
}
