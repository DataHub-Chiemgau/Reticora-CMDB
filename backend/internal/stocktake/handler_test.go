package stocktake

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The tests of this file run the handler on the in-memory repository
// (profile --no-db, WP-848, WP-287); the PostgreSQL behaviour is covered by
// stocktake_scope_integration_test.go.

const memoryArea = "00000000-0000-4000-8000-000000000001"

func doReq(mux chi.Router, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-1", UserID: "user-1"}))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)
	return w
}

func setup() (*MemoryRepository, *asset.MemoryRepository, chi.Router) {
	assetRepo := asset.NewMemoryRepository()
	repo := NewMemoryRepository(assetRepo)
	h := NewHandler(repo)
	mux := chi.NewRouter()
	h.RegisterRoutes(mux)
	return repo, assetRepo, mux
}

func createStocktake(t *testing.T, mux chi.Router, body string) Stocktake {
	t.Helper()
	w := doReq(mux, http.MethodPost, "/api/v1/stocktakes", body)
	if w.Code != http.StatusCreated {
		t.Fatalf("create stocktake: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var st Stocktake
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

// withStatus stores a stocktake with the given status directly in the
// repository, as an earlier release did (no snapshot).
func withStatus(t *testing.T, repo *MemoryRepository, status string) Stocktake {
	t.Helper()
	st := &Stocktake{OrganizationID: "org-1", Title: "legacy " + status, Status: status, Scope: "full"}
	if err := repo.Create(context.Background(), st); err != nil {
		t.Fatal(err)
	}
	return *st
}

// TestMemoryPatchRulesAndTransitions: status and totals are not writable
// (400), the area changes only while planned (409 otherwise), cancel moves
// planned to cancelled and a second cancel is 409.
func TestMemoryPatchRulesAndTransitions(t *testing.T) {
	_, _, mux := setup()
	st := createStocktake(t, mux, `{"title":"Q3 Inventur"}`)
	path := "/api/v1/stocktakes/" + st.ID
	steps := []struct {
		name, method, suffix, body string
		want                       int
	}{
		{"status is no input", http.MethodPatch, "", `{"status":"completed"}`, http.StatusBadRequest},
		{"totals are no input", http.MethodPatch, "", `{"total_scanned":3}`, http.StatusBadRequest},
		{"area no UUID", http.MethodPatch, "", `{"location_id":"room-1"}`, http.StatusUnprocessableEntity},
		{"area while planned", http.MethodPatch, "", `{"location_id":"` + memoryArea + `"}`, http.StatusOK},
		{"title", http.MethodPatch, "", `{"title":"renamed"}`, http.StatusOK},
		{"cancel planned", http.MethodPost, "/cancel", "", http.StatusOK},
		{"cancel cancelled", http.MethodPost, "/cancel", "", http.StatusConflict},
		{"area while cancelled", http.MethodPatch, "", `{"location_id":"` + memoryArea + `"}`, http.StatusConflict},
		{"start cancelled", http.MethodPost, "/start", "", http.StatusConflict},
	}
	for _, s := range steps {
		if w := doReq(mux, s.method, path+s.suffix, s.body); w.Code != s.want {
			t.Errorf("%s: %d %s, want %d", s.name, w.Code, w.Body.String(), s.want)
		}
	}
}

// TestMemoryScansNeedInProgressSnapshot: a scan of a planned, completed or
// cancelled stocktake is 409, so is one of an in-progress stocktake, which
// has no snapshot in memory; the input rules of WP-287 answer first
// (scan_result, ci_id and location_found are no input, 400; one object,
// the quantity of a consumable and the place found, 422).
func TestMemoryScansNeedInProgressSnapshot(t *testing.T) {
	repo, _, mux := setup()
	planned := createStocktake(t, mux, `{"title":"planned"}`)
	asset := `"asset_id":"00000000-0000-4000-8000-000000000002"`
	place := `"location_id":"` + memoryArea + `"`
	valid := `{` + asset + `,` + place + `}`
	for _, st := range []Stocktake{planned, withStatus(t, repo, StatusInProgress), withStatus(t, repo, StatusCompleted), withStatus(t, repo, StatusCancelled)} {
		if w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+st.ID+"/scans", valid); w.Code != http.StatusConflict {
			t.Errorf("scan of a %s stocktake: %d %s, want 409", st.Status, w.Code, w.Body.String())
		}
	}
	consumable := `"consumable_id":"00000000-0000-4000-8000-000000000003"`
	for _, c := range []struct {
		name, body string
		want       int
	}{
		{"scan_result", `{` + asset + `,` + place + `,"scan_result":"found"}`, http.StatusBadRequest},
		{"ci_id", `{` + asset + `,` + place + `,"ci_id":"00000000-0000-4000-8000-000000000004"}`, http.StatusBadRequest},
		{"location_found", `{` + asset + `,` + place + `,"location_found":"Raum A"}`, http.StatusBadRequest},
		{"no object", `{` + place + `}`, http.StatusUnprocessableEntity},
		{"two objects", `{` + asset + `,` + consumable + `,"quantity":1,` + place + `}`, http.StatusUnprocessableEntity},
		{"asset with quantity", `{` + asset + `,"quantity":1,` + place + `}`, http.StatusUnprocessableEntity},
		{"consumable without quantity", `{` + consumable + `,` + place + `}`, http.StatusUnprocessableEntity},
		{"consumable with quantity 0", `{` + consumable + `,"quantity":0,` + place + `}`, http.StatusUnprocessableEntity},
		{"no place found", `{` + asset + `}`, http.StatusUnprocessableEntity},
		{"unknown scan method", `{` + asset + `,` + place + `,"scan_method":"telepathy"}`, http.StatusUnprocessableEntity},
	} {
		if w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+planned.ID+"/scans", c.body); w.Code != c.want {
			t.Errorf("scan with %s: %d %s, want %d", c.name, w.Code, w.Body.String(), c.want)
		}
	}
	if w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/st-unknown/scans", valid); w.Code != http.StatusNotFound {
		t.Errorf("scan of an unknown stocktake: %d, want 404", w.Code)
	}
	if w := doReq(mux, http.MethodGet, "/api/v1/stocktakes/"+planned.ID+"/scans", ""); w.Code != http.StatusOK {
		t.Errorf("scans of a known stocktake: %d, want 200", w.Code)
	}
	if w := doReq(mux, http.MethodGet, "/api/v1/stocktakes/st-unknown/scans", ""); w.Code != http.StatusNotFound {
		t.Errorf("scans of an unknown stocktake: %d, want 404", w.Code)
	}
}

// TestMemorySnapshotRoutesAnswer501: start of a stocktake with area, the
// items, the difference list and its deprecated alias need the location
// tree and the stock of the bins (501); an unknown stocktake is 404.
func TestMemorySnapshotRoutesAnswer501(t *testing.T) {
	_, _, mux := setup()
	st := createStocktake(t, mux, `{"title":"area","location_id":"`+memoryArea+`"}`)
	for _, c := range []struct{ method, suffix string }{
		{http.MethodPost, "/start"},
		{http.MethodGet, "/items"},
		{http.MethodGet, "/differences"},
		{http.MethodGet, "/difference"},
	} {
		if w := doReq(mux, c.method, "/api/v1/stocktakes/"+st.ID+c.suffix, ""); w.Code != http.StatusNotImplemented {
			t.Errorf("%s …%s: %d %s, want 501", c.method, c.suffix, w.Code, w.Body.String())
		}
		if w := doReq(mux, c.method, "/api/v1/stocktakes/st-unknown"+c.suffix, ""); w.Code != http.StatusNotFound {
			t.Errorf("%s …%s of an unknown stocktake: %d, want 404", c.method, c.suffix, w.Code)
		}
	}
	noArea := createStocktake(t, mux, `{"title":"no area"}`)
	if w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+noArea.ID+"/start", ""); w.Code != http.StatusUnprocessableEntity {
		t.Errorf("start without area: %d %s, want 422", w.Code, w.Body.String())
	}
}

// TestMemoryCompleteWithoutService: without the completion service
// (profile --no-db, WP-761) POST …/complete follows the status rules (409
// for planned, completed and cancelled, 404 for an unknown stocktake) and
// answers an in-progress stocktake with 501 like start; apply_corrections
// is ignored, the answer is the same with and without it. Nothing changes,
// neither the stocktake nor an asset.
func TestMemoryCompleteWithoutService(t *testing.T) {
	repo, assetRepo, mux := setup()
	a := &asset.Asset{OrganizationID: "org-1", AssetTag: "MISS-1", Name: "Asset", Category: "hardware", Status: "assigned", Location: "Raum A"}
	if err := assetRepo.Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	planned := createStocktake(t, mux, `{"title":"planned"}`)
	for _, c := range []struct {
		st   Stocktake
		want int
	}{
		{planned, http.StatusConflict},
		{withStatus(t, repo, StatusInProgress), http.StatusNotImplemented},
		{withStatus(t, repo, StatusCompleted), http.StatusConflict},
		{withStatus(t, repo, StatusCancelled), http.StatusConflict},
	} {
		for _, body := range []string{"", `{"apply_corrections":true}`, `{"apply_corrections":false}`} {
			w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+c.st.ID+"/complete", body)
			if w.Code != c.want {
				t.Errorf("complete of a %s stocktake with %q: %d %s, want %d", c.st.Status, body, w.Code, w.Body.String(), c.want)
			}
		}
		stored, err := repo.GetByID(context.Background(), "org-1", c.st.ID)
		if err != nil || stored.Status != c.st.Status || stored.CompletedAt != "" {
			t.Errorf("%s stocktake after the refused completion: %+v %v", c.st.Status, stored, err)
		}
	}
	got, err := assetRepo.GetByID(context.Background(), "org-1", a.ID)
	if err != nil || got.Status != "assigned" || got.Location != "Raum A" {
		t.Errorf("asset after the refused completions: %+v %v, want unchanged", got, err)
	}
	if w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/st-unknown/complete", `{}`); w.Code != http.StatusNotFound {
		t.Errorf("complete of an unknown stocktake: %d, want 404", w.Code)
	}
	if NewService(repo, nil) != nil {
		t.Error("NewService over the in-memory repository: want nil")
	}
}

// TestMemoryCompletedStocktakeIsImmutable: PATCH, DELETE, a scan and
// cancel of a completed or cancelled stocktake answer 409
// (ErrAlreadyCompleted, WP-761), and the stocktake stays.
func TestMemoryCompletedStocktakeIsImmutable(t *testing.T) {
	repo, _, mux := setup()
	scan := `{"asset_id":"00000000-0000-4000-8000-000000000002","location_id":"` + memoryArea + `"}`
	for _, status := range []string{StatusCompleted, StatusCancelled} {
		st := withStatus(t, repo, status)
		path := "/api/v1/stocktakes/" + st.ID
		for _, c := range []struct{ method, suffix, body string }{
			{http.MethodPatch, "", `{"title":"renamed"}`},
			{http.MethodDelete, "", ""},
			{http.MethodPost, "/scans", scan},
			{http.MethodPost, "/cancel", ""},
		} {
			w := doReq(mux, c.method, path+c.suffix, c.body)
			if w.Code != http.StatusConflict {
				t.Errorf("%s …%s of a %s stocktake: %d %s, want 409", c.method, c.suffix, status, w.Code, w.Body.String())
			}
		}
		stored, err := repo.GetByID(context.Background(), "org-1", st.ID)
		if err != nil || stored.Status != status || stored.Title != st.Title {
			t.Errorf("%s stocktake after the refused writes: %+v %v", status, stored, err)
		}
	}
}

// TestAlreadyCompletedIs409: ErrAlreadyCompleted, also wrapped, and the
// refusal of the immutability trigger answer 409.
func TestAlreadyCompletedIs409(t *testing.T) {
	trigger := &pgconn.PgError{Code: "23514", ConstraintName: "stocktake_immutable"}
	for name, err := range map[string]error{
		"sentinel": ErrAlreadyCompleted,
		"wrapped":  fmt.Errorf("complete: %w", ErrAlreadyCompleted),
		"trigger":  immutableError(fmt.Errorf("add stock scan: %w", trigger)),
	} {
		w := httptest.NewRecorder()
		writeStocktakeError(w, err)
		if w.Code != http.StatusConflict {
			t.Errorf("%s: %d %s, want 409", name, w.Code, w.Body.String())
		}
	}
	other := &pgconn.PgError{Code: "23514", ConstraintName: "stocktake_item_counted_check"}
	if err := immutableError(other); errors.Is(err, ErrAlreadyCompleted) {
		t.Errorf("another check violation: %v, want unchanged", err)
	}
}
