package discovery

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/go-chi/chi/v5"
)

// Review item kinds and statuses (mirrors the review_item table CHECKs).
const (
	ReviewKindAmbiguousIdentity  = "ambiguous_identity"
	ReviewKindConflictingValues  = "conflicting_values"
	ReviewKindUnclassifiedDevice = "unclassified_device"

	ReviewStatusOpen      = "open"
	ReviewStatusResolved  = "resolved"
	ReviewStatusDismissed = "dismissed"
)

// ReviewItem is a reconciliation ambiguity queued for human resolution.
type ReviewItem struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	Kind           string         `json:"kind"`
	Status         string         `json:"status"`
	Payload        map[string]any `json:"payload"`
	CandidateCIIDs []string       `json:"candidate_ci_ids,omitempty"`
	ResolvedBy     string         `json:"resolved_by,omitempty"`
	ResolvedAt     string         `json:"resolved_at,omitempty"`
	Resolution     string         `json:"resolution,omitempty"`
	CreatedAt      string         `json:"created_at"`
	UpdatedAt      string         `json:"updated_at"`
}

// ReviewFilter narrows a review-item listing.
type ReviewFilter struct {
	Status string
	Kind   string
}

// Resolution is the request body for resolving a review item.
type Resolution struct {
	// Action selects how to resolve: "merge" folds discovery data into an
	// existing candidate CI, "create" spawns a new CI from the payload,
	// "dismiss" closes the item without a CI change.
	Action     string `json:"action"`
	CIID       string `json:"ci_id,omitempty"`
	ResolvedBy string `json:"resolved_by,omitempty"`
	Note       string `json:"resolution,omitempty"`
	// Status is the terminal status to persist; derived from Action when empty.
	Status string `json:"-"`
}

// reviewItemFromValueConflicts maps contradicting identity values on a
// matched CI into a review item (spec §5.3: Konfliktlösung nach
// Quellenvertrauen + Aktualität; Unklarheiten in Review-Queue). It returns
// nil when there is nothing to review.
func reviewItemFromValueConflicts(orgID string, matched ci.Item, incoming IngestItem, result ReconcileResult) *ReviewItem {
	if len(result.ValueConflicts) == 0 {
		return nil
	}
	payload := map[string]any{
		"criterion":          result.Criterion,
		"matched_ci_id":      result.MatchedCIID,
		"conflict_fields":    result.ValueConflicts,
		"ci_type_name":       incoming.CITypeName,
		"name":               incoming.Name,
		"manufacturer":       incoming.Manufacturer,
		"model":              incoming.Model,
		"serial_number":      incoming.SerialNumber,
		"management_ip":      incoming.ManagementIP,
		"hardware_uuid":      incoming.HardwareUUID,
		"primary_mac":        incoming.PrimaryMAC,
		"hostname":           incoming.Hostname,
		"fqdn":               incoming.FQDN,
		"fingerprint":        incoming.Fingerprint,
		"raw_data":           incoming.RawData,
		"existing":           identitySnapshot(matched),
		"incoming_source":    incoming.Source,
		"existing_source":    matched.DiscoverySource,
		"existing_last_seen": matched.LastSeenAt,
	}
	return &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindConflictingValues,
		Status:         ReviewStatusOpen,
		Payload:        payload,
		CandidateCIIDs: []string{result.MatchedCIID},
	}
}

// identitySnapshot captures the stored identity fields of a matched CI for
// side-by-side comparison in the review queue.
func identitySnapshot(item ci.Item) map[string]any {
	return map[string]any{
		"name":          item.Name,
		"manufacturer":  item.Manufacturer,
		"model":         item.Model,
		"serial_number": item.SerialNumber,
		"management_ip": item.ManagementIP,
		"hardware_uuid": item.HardwareUUID,
		"primary_mac":   item.PrimaryMAC,
		"hostname":      item.Hostname,
		"fqdn":          item.FQDN,
	}
}

// reviewItemFromConflict maps a conflicting reconciliation result into a review
// item capturing the candidate CIs and the matching criterion.
func reviewItemFromConflict(orgID string, incoming IngestItem, result ReconcileResult) *ReviewItem {
	if len(result.CandidateCIIDs) == 0 {
		return nil
	}
	payload := map[string]any{
		"criterion":     result.Criterion,
		"ci_type_name":  incoming.CITypeName,
		"name":          incoming.Name,
		"manufacturer":  incoming.Manufacturer,
		"model":         incoming.Model,
		"serial_number": incoming.SerialNumber,
		"management_ip": incoming.ManagementIP,
		"hardware_uuid": incoming.HardwareUUID,
		"primary_mac":   incoming.PrimaryMAC,
		"hostname":      incoming.Hostname,
		"fqdn":          incoming.FQDN,
		"fingerprint":   incoming.Fingerprint,
		"raw_data":      incoming.RawData,
	}
	return &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindAmbiguousIdentity,
		Status:         ReviewStatusOpen,
		Payload:        payload,
		CandidateCIIDs: result.CandidateCIIDs,
	}
}

// ---- MemoryRepository review-item methods ----

func (r *MemoryRepository) CreateReviewItem(_ context.Context, item *ReviewItem) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.seq++
	item.ID = fmt.Sprintf("%08d-0000-0000-0000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	if item.Status == "" {
		item.Status = ReviewStatusOpen
	}
	if item.Payload == nil {
		item.Payload = map[string]any{}
	}
	item.CreatedAt = now
	item.UpdatedAt = now
	stored := *item
	r.reviewItems[item.ID] = &stored
	return nil
}

func (r *MemoryRepository) ListReviewItems(_ context.Context, orgID string, filter ReviewFilter, page api.PaginationParams) ([]ReviewItem, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var result []ReviewItem
	for _, item := range r.reviewItems {
		if item.OrganizationID != orgID {
			continue
		}
		if filter.Status != "" && item.Status != filter.Status {
			continue
		}
		if filter.Kind != "" && item.Kind != filter.Kind {
			continue
		}
		result = append(result, *item)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })

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

func (r *MemoryRepository) GetReviewItem(_ context.Context, orgID, id string) (*ReviewItem, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	item, ok := r.reviewItems[id]
	if !ok || item.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	clone := *item
	return &clone, nil
}

func (r *MemoryRepository) ResolveReviewItem(_ context.Context, orgID, id string, resolution Resolution) (*ReviewItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	item, ok := r.reviewItems[id]
	if !ok || item.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	item.Status = resolution.Status
	item.Resolution = resolution.Note
	item.ResolvedBy = resolution.ResolvedBy
	item.ResolvedAt = time.Now().UTC().Format(time.RFC3339)
	item.UpdatedAt = item.ResolvedAt
	clone := *item
	return &clone, nil
}

// ---- Handlers ----

// ListReviewItems handles GET /api/v1/discovery/review-items
func (h *Handler) ListReviewItems(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	page := api.ParsePagination(r)
	filter := ReviewFilter{
		Status: r.URL.Query().Get("status"),
		Kind:   r.URL.Query().Get("kind"),
	}
	items, total, err := h.repo.ListReviewItems(r.Context(), t.OrganizationID, filter, page)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}

	api.WriteJSON(w, http.StatusOK, api.ListResponse[ReviewItem]{
		Data:    items,
		Total:   total,
		Limit:   page.Limit,
		Offset:  page.Offset,
		HasMore: page.Offset+page.Limit < total,
	})
}

// ResolveReviewItem handles POST /api/v1/discovery/review-items/{id}/resolve
func (h *Handler) ResolveReviewItem(w http.ResponseWriter, r *http.Request) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return
	}

	id := chi.URLParam(r, "id")
	var body Resolution
	if err := api.ReadJSON(r, &body); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	if body.ResolvedBy == "" {
		body.ResolvedBy = t.UserID
	}

	item, err := h.repo.GetReviewItem(r.Context(), t.OrganizationID, id)
	if err != nil {
		api.WriteError(w, http.StatusNotFound, "Not Found", "review item not found")
		return
	}
	if item.Status != ReviewStatusOpen {
		api.WriteError(w, http.StatusConflict, "Conflict", "review item already resolved")
		return
	}

	switch body.Action {
	case "merge":
		if body.CIID == "" {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "ci_id is required to merge")
			return
		}
		if !containsString(item.CandidateCIIDs, body.CIID) {
			api.WriteError(w, http.StatusBadRequest, "Bad Request", "ci_id is not one of the candidate CIs")
			return
		}
		if h.ciRepo != nil {
			now := time.Now().UTC().Format(time.RFC3339)
			source := ci.SourceSweep
			if _, err := h.ciRepo.Update(r.Context(), t.OrganizationID, body.CIID, ci.UpdateRequest{
				ManagementIP:    payloadStringPtr(item.Payload, "management_ip"),
				DiscoverySource: &source,
				LastSeenAt:      &now,
			}); err != nil {
				api.WriteRepoError(w, err)
				return
			}
		}
		body.Status = ReviewStatusResolved
		if body.Note == "" {
			body.Note = "merged into " + body.CIID
		}
	case "create":
		newID := ""
		if h.ciRepo != nil {
			nowTime := time.Now().UTC()
			source := ci.SourceSweep
			newItem := ci.Item{
				OrganizationID:  t.OrganizationID,
				CITypeID:        payloadString(item.Payload, "ci_type_name"),
				Name:            payloadString(item.Payload, "name"),
				Status:          "active",
				Manufacturer:    payloadString(item.Payload, "manufacturer"),
				Model:           payloadString(item.Payload, "model"),
				SerialNumber:    payloadString(item.Payload, "serial_number"),
				ManagementIP:    payloadString(item.Payload, "management_ip"),
				Hostname:        payloadString(item.Payload, "hostname"),
				FQDN:            payloadString(item.Payload, "fqdn"),
				Attributes:      map[string]any{"review_item_id": item.ID},
				DiscoverySource: source,
				FirstSeenAt:     &nowTime,
				LastSeenAt:      &nowTime,
			}
			if err := h.ciRepo.Create(r.Context(), &newItem); err != nil {
				api.WriteRepoError(w, err)
				return
			}
			newID = newItem.ID
		}
		body.Status = ReviewStatusResolved
		if body.Note == "" {
			body.Note = "created new CI " + newID
		}
	case "dismiss":
		body.Status = ReviewStatusDismissed
		if body.Note == "" {
			body.Note = "dismissed"
		}
	default:
		api.WriteError(w, http.StatusBadRequest, "Bad Request", "action must be one of merge, create, dismiss")
		return
	}

	resolved, err := h.repo.ResolveReviewItem(r.Context(), t.OrganizationID, id, body)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, resolved)
}

func containsString(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

func payloadString(payload map[string]any, key string) string {
	if payload == nil {
		return ""
	}
	if s, ok := payload[key].(string); ok {
		return s
	}
	return ""
}

func payloadStringPtr(payload map[string]any, key string) *string {
	if s := payloadString(payload, key); s != "" {
		return &s
	}
	return nil
}
