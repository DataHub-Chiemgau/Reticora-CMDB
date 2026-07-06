package stocktake

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// Repository defines persistence operations for stocktakes.
type Repository interface {
	List(orgID string, filter FilterParams, page api.PaginationParams) ([]Stocktake, int, error)
	GetByID(orgID, id string) (*Stocktake, error)
	Create(s *Stocktake) error
	Update(orgID, id string, req UpdateRequest) (*Stocktake, error)
	Delete(orgID, id string) error
	AddScan(scan *StockScan) error
	ListScans(orgID, stocktakeID string, page api.PaginationParams) ([]StockScan, int, error)
}

// MemoryRepository is an in-memory implementation of Repository.
type MemoryRepository struct {
	mu         sync.RWMutex
	stocktakes map[string]*Stocktake
	scans      map[string]*StockScan
	nextID     int
	nextScan   int
}

// NewMemoryRepository creates a new in-memory stocktake repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{
		stocktakes: make(map[string]*Stocktake),
		scans:      make(map[string]*StockScan),
	}
}

func (r *MemoryRepository) List(orgID string, filter FilterParams, page api.PaginationParams) ([]Stocktake, int, error) {
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

func (r *MemoryRepository) GetByID(orgID, id string) (*Stocktake, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	s, ok := r.stocktakes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, fmt.Errorf("stocktake not found")
	}
	return s, nil
}

func (r *MemoryRepository) Create(s *Stocktake) error {
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

func (r *MemoryRepository) Update(orgID, id string, req UpdateRequest) (*Stocktake, error) {
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

func (r *MemoryRepository) Delete(orgID, id string) error {
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

func (r *MemoryRepository) AddScan(scan *StockScan) error {
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

func (r *MemoryRepository) ListScans(orgID, stocktakeID string, page api.PaginationParams) ([]StockScan, int, error) {
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
