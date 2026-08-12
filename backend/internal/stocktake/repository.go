package stocktake

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
)

// ErrAlreadyCompleted is returned when a stocktake that is already in a
// terminal state (completed or cancelled) is completed again.
var ErrAlreadyCompleted = errors.New("stocktake already completed or cancelled")

// deviatingResults are the scan results that make up the difference list.
var deviatingResults = []string{"missing", "surplus", "damaged", "wrong_location"}

func isDeviating(result string) bool {
	for _, r := range deviatingResults {
		if r == result {
			return true
		}
	}
	return false
}

// Repository defines persistence operations for stocktakes.
type Repository interface {
	List(ctx context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Stocktake, int, error)
	GetByID(ctx context.Context, orgID, id string) (*Stocktake, error)
	Create(ctx context.Context, s *Stocktake) error
	Update(ctx context.Context, orgID, id string, req UpdateRequest) (*Stocktake, error)
	Delete(ctx context.Context, orgID, id string) error
	AddScan(ctx context.Context, scan *StockScan) error
	ListScans(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]StockScan, int, error)
	// Difference returns the deviating scans of a stocktake (missing, surplus,
	// damaged, wrong_location), each enriched with the referenced asset when
	// the scan resolved to one.
	Difference(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]DifferenceEntry, int, error)
	// Complete finalizes a stocktake. When applyCorrections is true, the
	// recorded differences are applied to the inventory: missing assets are
	// marked lost, surplus assets return to stock, wrong_location moves the
	// asset to the found location and damaged assets go to maintenance.
	// Completing an already completed or cancelled stocktake fails with
	// ErrAlreadyCompleted.
	Complete(ctx context.Context, orgID, id string, applyCorrections bool) (*Completion, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu         sync.RWMutex
	stocktakes map[string]*Stocktake
	scans      map[string]*StockScan
	assets     asset.Lookup
	nextID     int
	nextScan   int
}

// NewMemoryRepository creates a new in-memory stocktake repository. assets
// provides the asset records used by the difference list and completion
// corrections; it may be nil, in which case scans never resolve to assets.
func NewMemoryRepository(assets asset.Lookup) *MemoryRepository {
	return &MemoryRepository{
		stocktakes: make(map[string]*Stocktake),
		scans:      make(map[string]*StockScan),
		assets:     assets,
	}
}

func (r *MemoryRepository) List(_ context.Context, orgID string, filter FilterParams, page api.PaginationParams) ([]Stocktake, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []Stocktake
	for _, s := range r.stocktakes {
		if s.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && s.Status != filter.Status {
			continue
		}
		if filter.Scope != "" && s.Scope != filter.Scope {
			continue
		}
		if filter.Search != "" && !strings.Contains(strings.ToLower(s.Title), strings.ToLower(filter.Search)) {
			continue
		}
		result = append(result, *s)
	}

	total := len(result)

	column, direction := NormalizeSort(filter)
	sort.Slice(result, func(i, j int) bool {
		vi, vj := sortValue(result[i], column), sortValue(result[j], column)
		if vi == vj {
			if direction == "asc" {
				return result[i].ID < result[j].ID
			}
			return result[i].ID > result[j].ID
		}
		if direction == "asc" {
			return vi < vj
		}
		return vi > vj
	})

	if page.Cursor != nil {
		if !page.Cursor.Matches(column, direction) {
			return nil, 0, fmt.Errorf("%w: sort order changed", api.ErrInvalidCursor)
		}
		remaining := make([]Stocktake, 0, len(result))
		for _, item := range result {
			if api.KeysetCompare(sortValue(item, column), item.ID, *page.Cursor, direction) {
				remaining = append(remaining, item)
			}
		}
		if len(remaining) > page.Limit {
			remaining = remaining[:page.Limit]
		}
		return remaining, total, nil
	}

	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}

func (r *MemoryRepository) GetByID(_ context.Context, orgID, id string) (*Stocktake, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, fmt.Errorf("stocktake not found")
	}
	return s, nil
}

func (r *MemoryRepository) Create(_ context.Context, s *Stocktake) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.nextID++
	s.ID = fmt.Sprintf("st-%d", r.nextID)
	now := time.Now().UTC()
	s.CreatedAt = now
	s.UpdatedAt = now
	r.stocktakes[s.ID] = s
	return nil
}

func (r *MemoryRepository) Update(_ context.Context, orgID, id string, req UpdateRequest) (*Stocktake, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, fmt.Errorf("stocktake not found")
	}

	if req.Title != nil {
		s.Title = *req.Title
	}
	if req.Description != nil {
		s.Description = *req.Description
	}
	if req.Status != nil {
		s.Status = *req.Status
		if *req.Status == "in_progress" && s.StartedAt == "" {
			s.StartedAt = time.Now().UTC().Format(time.RFC3339)
		}
		if *req.Status == "completed" {
			s.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	if req.DueDate != nil {
		s.DueDate = *req.DueDate
	}
	if req.TotalExpected != nil {
		s.TotalExpected = *req.TotalExpected
	}
	s.UpdatedAt = time.Now().UTC()
	return s, nil
}

func (r *MemoryRepository) Delete(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return fmt.Errorf("stocktake not found")
	}
	delete(r.stocktakes, id)
	// Remove associated scans
	for sid, scan := range r.scans {
		if scan.StocktakeID == id {
			delete(r.scans, sid)
		}
	}
	return nil
}

func (r *MemoryRepository) AddScan(_ context.Context, scan *StockScan) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Verify stocktake exists
	st, ok := r.stocktakes[scan.StocktakeID]
	if !ok || st.OrganizationID != scan.OrganizationID {
		return fmt.Errorf("stocktake not found")
	}

	r.nextScan++
	scan.ID = fmt.Sprintf("scan-%d", r.nextScan)
	scan.ScannedAt = time.Now().UTC()
	r.scans[scan.ID] = scan

	// Update counters
	st.TotalScanned++
	switch scan.ScanResult {
	case "missing":
		st.TotalMissing++
	case "surplus":
		st.TotalSurplus++
	}
	st.UpdatedAt = time.Now().UTC()

	return nil
}

func (r *MemoryRepository) Difference(ctx context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]DifferenceEntry, int, error) {
	r.mu.RLock()
	st, ok := r.stocktakes[stocktakeID]
	if !ok || st.OrganizationID != orgID {
		r.mu.RUnlock()
		return nil, 0, fmt.Errorf("stocktake not found")
	}
	var scans []StockScan
	for _, s := range r.scans {
		if s.OrganizationID == orgID && s.StocktakeID == stocktakeID && isDeviating(s.ScanResult) {
			scans = append(scans, *s)
		}
	}
	r.mu.RUnlock()

	sort.Slice(scans, func(i, j int) bool { return scans[i].ScannedAt.Before(scans[j].ScannedAt) })
	total := len(scans)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}

	entries := make([]DifferenceEntry, 0, end-start)
	for _, s := range scans[start:end] {
		entry := DifferenceEntry{Scan: s}
		if s.AssetID != "" && r.assets != nil {
			if a, err := r.assets.GetByID(ctx, orgID, s.AssetID); err == nil {
				entry.Asset = a
			}
		}
		entries = append(entries, entry)
	}
	return entries, total, nil
}

func (r *MemoryRepository) Complete(ctx context.Context, orgID, id string, applyCorrections bool) (*Completion, error) {
	r.mu.Lock()
	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		r.mu.Unlock()
		return nil, fmt.Errorf("stocktake not found")
	}
	if s.Status == "completed" || s.Status == "cancelled" {
		r.mu.Unlock()
		return nil, ErrAlreadyCompleted
	}
	// Transition first so a failing correction never leaves the stocktake in
	// a half-completed state visible to concurrent readers.
	s.Status = "completed"
	s.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	s.UpdatedAt = time.Now().UTC()
	var scans []StockScan
	for _, scan := range r.scans {
		if scan.StocktakeID == id && isDeviating(scan.ScanResult) {
			scans = append(scans, *scan)
		}
	}
	r.mu.Unlock()
	sort.Slice(scans, func(i, j int) bool { return scans[i].ScannedAt.Before(scans[j].ScannedAt) })

	completion := &Completion{Stocktake: *s, Corrections: []Correction{}}
	if !applyCorrections || r.assets == nil {
		return completion, nil
	}
	for _, scan := range scans {
		if scan.AssetID == "" {
			continue
		}
		c, err := r.applyCorrection(ctx, orgID, scan)
		if err != nil {
			continue
		}
		completion.Corrections = append(completion.Corrections, c)
		if c.Applied {
			completion.CorrectionsApplied++
		}
	}
	return completion, nil
}

// applyCorrection applies the inventory change implied by a deviating scan
// through the asset repository and records the outcome.
func (r *MemoryRepository) applyCorrection(ctx context.Context, orgID string, scan StockScan) (Correction, error) {
	a, err := r.assets.GetByID(ctx, orgID, scan.AssetID)
	if err != nil {
		return Correction{}, err
	}
	req, c := correctionFor(a, scan)
	if c.Detail != "" {
		return c, nil
	}
	if _, err := r.assets.Update(ctx, orgID, a.ID, req); err != nil {
		return Correction{}, err
	}
	c.Applied = true
	return c, nil
}

// correctionFor maps a deviating scan result to the asset update that
// corrects the inventory: missing → lost, surplus → in_stock, damaged →
// maintenance, wrong_location → move to the found location.
func correctionFor(a *asset.Asset, scan StockScan) (asset.UpdateRequest, Correction) {
	c := Correction{AssetID: a.ID, AssetTag: a.AssetTag, ScanResult: scan.ScanResult, PreviousStatus: a.Status, PreviousLocation: a.Location}
	var req asset.UpdateRequest
	switch scan.ScanResult {
	case "missing":
		status := "lost"
		req.Status = &status
		c.NewStatus = status
	case "surplus":
		status := "in_stock"
		req.Status = &status
		c.NewStatus = status
	case "damaged":
		status := "maintenance"
		req.Status = &status
		c.NewStatus = status
	case "wrong_location":
		if scan.LocationFound == "" {
			c.Detail = "scan did not record a found location"
			return req, c
		}
		req.Location = &scan.LocationFound
		c.NewLocation = scan.LocationFound
	}
	return req, c
}

func (r *MemoryRepository) ListScans(_ context.Context, orgID, stocktakeID string, page api.PaginationParams) ([]StockScan, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []StockScan
	for _, s := range r.scans {
		if s.OrganizationID == orgID && s.StocktakeID == stocktakeID {
			result = append(result, *s)
		}
	}

	total := len(result)
	start := page.Offset
	if start > total {
		start = total
	}
	end := start + page.Limit
	if end > total {
		end = total
	}
	return result[start:end], total, nil
}
