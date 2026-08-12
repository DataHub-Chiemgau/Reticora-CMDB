package stocktake

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

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

func createStocktake(t *testing.T, mux chi.Router) Stocktake {
	t.Helper()
	w := doReq(mux, http.MethodPost, "/api/v1/stocktakes", `{"title":"Q3 Inventur"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create stocktake: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var st Stocktake
	if err := json.Unmarshal(w.Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	return st
}

func addAsset(t *testing.T, repo *asset.MemoryRepository, tag, status, location string) asset.Asset {
	t.Helper()
	a := &asset.Asset{OrganizationID: "org-1", AssetTag: tag, Name: "Asset " + tag, Category: "hardware", Status: status, Location: location}
	if err := repo.Create(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return *a
}

func addScan(t *testing.T, mux chi.Router, stocktakeID, assetID, result, location string) {
	t.Helper()
	payload := map[string]string{"asset_id": assetID, "scan_result": result}
	if location != "" {
		payload["location_found"] = location
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+stocktakeID+"/scans", string(raw))
	if w.Code != http.StatusCreated {
		t.Fatalf("add scan: expected 201, got %d: %s", w.Code, w.Body.String())
	}
}

func TestDifferenceListsDeviatingScansWithAssets(t *testing.T) {
	_, assetRepo, mux := setup()
	st := createStocktake(t, mux)
	found := addAsset(t, assetRepo, "OK-1", "assigned", "Raum A")
	missing := addAsset(t, assetRepo, "MISS-1", "assigned", "Raum A")
	wrongLoc := addAsset(t, assetRepo, "WL-1", "assigned", "Raum A")

	addScan(t, mux, st.ID, found.ID, "found", "")
	addScan(t, mux, st.ID, missing.ID, "missing", "")
	addScan(t, mux, st.ID, wrongLoc.ID, "wrong_location", "Raum B")

	w := doReq(mux, http.MethodGet, "/api/v1/stocktakes/"+st.ID+"/difference", "")
	if w.Code != http.StatusOK {
		t.Fatalf("difference: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data  []DifferenceEntry `json:"data"`
		Total int               `json:"total"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Total != 2 || len(resp.Data) != 2 {
		t.Fatalf("expected 2 deviating scans (found excluded), got total=%d len=%d", resp.Total, len(resp.Data))
	}
	byResult := map[string]DifferenceEntry{}
	for _, e := range resp.Data {
		byResult[e.Scan.ScanResult] = e
	}
	miss, ok := byResult["missing"]
	if !ok || miss.Asset == nil || miss.Asset.AssetTag != "MISS-1" {
		t.Fatalf("missing entry should carry asset MISS-1: %#v", byResult)
	}
	wl, ok := byResult["wrong_location"]
	if !ok || wl.Asset == nil || wl.Asset.AssetTag != "WL-1" || wl.Scan.LocationFound != "Raum B" {
		t.Fatalf("wrong_location entry mismatch: %#v", wl)
	}
}

func TestCompleteAppliesInventoryCorrections(t *testing.T) {
	_, assetRepo, mux := setup()
	st := createStocktake(t, mux)
	missing := addAsset(t, assetRepo, "MISS-1", "assigned", "Raum A")
	surplus := addAsset(t, assetRepo, "SUR-1", "lost", "")
	damaged := addAsset(t, assetRepo, "DMG-1", "assigned", "Raum A")
	wrongLoc := addAsset(t, assetRepo, "WL-1", "assigned", "Raum A")
	untouched := addAsset(t, assetRepo, "OK-1", "assigned", "Raum A")

	addScan(t, mux, st.ID, missing.ID, "missing", "")
	addScan(t, mux, st.ID, surplus.ID, "surplus", "")
	addScan(t, mux, st.ID, damaged.ID, "damaged", "")
	addScan(t, mux, st.ID, wrongLoc.ID, "wrong_location", "Raum B")
	addScan(t, mux, st.ID, untouched.ID, "found", "")

	w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+st.ID+"/complete", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var completion Completion
	if err := json.Unmarshal(w.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.Stocktake.Status != "completed" || completion.Stocktake.CompletedAt == "" {
		t.Fatalf("stocktake not completed: %#v", completion.Stocktake)
	}
	if completion.CorrectionsApplied != 4 {
		t.Fatalf("expected 4 applied corrections, got %d (%#v)", completion.CorrectionsApplied, completion.Corrections)
	}

	cases := []struct{ id, status, location string }{
		{missing.ID, "lost", "Raum A"},
		{surplus.ID, "in_stock", ""},
		{damaged.ID, "maintenance", "Raum A"},
		{wrongLoc.ID, "assigned", "Raum B"},
		{untouched.ID, "assigned", "Raum A"},
	}
	for _, c := range cases {
		a, err := assetRepo.GetByID(context.Background(), "org-1", c.id)
		if err != nil {
			t.Fatal(err)
		}
		if a.Status != c.status || a.Location != c.location {
			t.Fatalf("asset %s: expected (%s,%q), got (%s,%q)", c.id, c.status, c.location, a.Status, a.Location)
		}
	}
}

func TestCompleteWithoutCorrections(t *testing.T) {
	_, assetRepo, mux := setup()
	st := createStocktake(t, mux)
	missing := addAsset(t, assetRepo, "MISS-1", "assigned", "Raum A")
	addScan(t, mux, st.ID, missing.ID, "missing", "")

	w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+st.ID+"/complete", `{"apply_corrections":false}`)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var completion Completion
	if err := json.Unmarshal(w.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.Stocktake.Status != "completed" || completion.CorrectionsApplied != 0 || len(completion.Corrections) != 0 {
		t.Fatalf("expected completion without corrections: %#v", completion)
	}
	a, err := assetRepo.GetByID(context.Background(), "org-1", missing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "assigned" {
		t.Fatalf("asset must remain untouched, got status %q", a.Status)
	}
}

func TestCompleteIsRejectedWhenAlreadyTerminal(t *testing.T) {
	_, assetRepo, mux := setup()
	st := createStocktake(t, mux)
	missing := addAsset(t, assetRepo, "MISS-1", "assigned", "Raum A")
	addScan(t, mux, st.ID, missing.ID, "missing", "")

	w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+st.ID+"/complete", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("first complete: expected 200, got %d", w.Code)
	}
	w = doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+st.ID+"/complete", `{}`)
	if w.Code != http.StatusConflict {
		t.Fatalf("second complete: expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// Corrections must have been applied exactly once.
	a, err := assetRepo.GetByID(context.Background(), "org-1", missing.ID)
	if err != nil {
		t.Fatal(err)
	}
	if a.Status != "lost" {
		t.Fatalf("expected status lost after single completion, got %q", a.Status)
	}
}

func TestCompleteAndDifferenceOfUnknownStocktake(t *testing.T) {
	_, _, mux := setup()
	if w := doReq(mux, http.MethodGet, "/api/v1/stocktakes/nope/difference", ""); w.Code != http.StatusNotFound {
		t.Fatalf("difference unknown: expected 404, got %d", w.Code)
	}
	if w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/nope/complete", `{}`); w.Code != http.StatusNotFound {
		t.Fatalf("complete unknown: expected 404, got %d", w.Code)
	}
}

func TestWrongLocationScanWithoutLocationIsSkipped(t *testing.T) {
	_, assetRepo, mux := setup()
	st := createStocktake(t, mux)
	a := addAsset(t, assetRepo, "WL-1", "assigned", "Raum A")
	addScan(t, mux, st.ID, a.ID, "wrong_location", "")

	w := doReq(mux, http.MethodPost, "/api/v1/stocktakes/"+st.ID+"/complete", `{}`)
	if w.Code != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d", w.Code)
	}
	var completion Completion
	if err := json.Unmarshal(w.Body.Bytes(), &completion); err != nil {
		t.Fatal(err)
	}
	if completion.CorrectionsApplied != 0 || len(completion.Corrections) != 1 || completion.Corrections[0].Applied {
		t.Fatalf("expected one skipped correction: %#v", completion)
	}
}
