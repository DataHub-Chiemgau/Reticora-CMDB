package discovery

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
)

// The review queue lives in package reviews (REC-08, REP-01); discovery
// opens items and carries out what resolving them means for CIs.
type (
	// ReviewItem is a reconciliation item queued for human resolution.
	ReviewItem = reviews.Item
	// ReviewFilter narrows a review-item listing.
	ReviewFilter = reviews.Filter
	// Resolution is the request body for resolving a review item.
	Resolution = reviews.Resolution
)

// Review types and statuses (reviews.Kinds).
const (
	ReviewKindAmbiguousIdentity  = reviews.KindAmbiguousIdentity
	ReviewKindConflictingValues  = reviews.KindConflictingValues
	ReviewKindUnclassifiedDevice = reviews.KindUnclassifiedDevice
	ReviewKindOverrideConflict   = reviews.KindOverrideConflict
	ReviewKindSerialMismatch     = reviews.KindSerialMismatch
	// ReviewKindProbableDuplicate: a new CI shares its hostname or FQDN with
	// existing CIs (REC-02, CH29); an operator decides whether they are one.
	ReviewKindProbableDuplicate = reviews.KindProbableDuplicate
	// ReviewKindResurrectedDevice: a device matches a soft-deleted CI
	// (CI-13); an operator reactivates that CI or creates a new one. No CI
	// is created automatically (PRI-03).
	ReviewKindResurrectedDevice = reviews.KindResurrectedDevice

	ReviewStatusOpen      = reviews.StatusOpen
	ReviewStatusResolved  = reviews.StatusResolved
	ReviewStatusDismissed = reviews.StatusDismissed
)

// ReviewService returns the review queue of this handler's repository with
// the discovery effects of merge, new and accept.
func (h *Handler) ReviewService() *reviews.Service {
	h.reviewsOnce.Do(func() {
		svc := reviews.NewService(h.repo)
		for _, kind := range []string{ReviewKindAmbiguousIdentity, ReviewKindUnclassifiedDevice, ReviewKindProbableDuplicate,
			ReviewKindResurrectedDevice, ReviewKindConflictingValues} {
			svc.WithEffect(kind, h.resolveCIReview)
		}
		svc.WithEffect(ReviewKindUnlicensedCI, h.resolveUnlicensed)
		svc.WithEffect(ReviewKindOverrideConflict, h.resolveOverrideConflict)
		if h.reviewNotifier != nil {
			svc.WithNotifier(h.reviewNotifier)
		}
		h.reviews = svc
	})
	return h.reviews
}

// WithReviewNotifier attaches the notification of opened review items
// (REC-08, NTF-05).
func (h *Handler) WithReviewNotifier(n reviews.Notifier) *Handler {
	h.reviewNotifier = n
	return h
}

// openReview opens a review item, deduplicated per object and type, and
// reports whether it is new.
// ReasonNoProfileMatch marks an unclassified_device item of a device that
// no collector profile matched (DIS-02); its generic_device CI exists.
const ReasonNoProfileMatch = "no_profile_match"

// unclassifiedDevice reports whether the collector's classification chain
// found no profile for the item (generic_device, classified_by fallback).
func unclassifiedDevice(item *IngestItem) bool {
	return item.CITypeName == "generic_device" && item.Attributes["classified_by"] == "fallback"
}

func (h *Handler) openReview(ctx context.Context, item *ReviewItem) (bool, error) {
	return h.ReviewService().Open(ctx, item)
}

func refused(status int, msg string) error {
	return &reviews.Error{Status: status, Message: msg}
}

// resolveCIReview carries out merge, new and accept of the CI review types.
func (h *Handler) resolveCIReview(ctx context.Context, item *ReviewItem, res *Resolution) (bool, error) {
	orgID := item.OrganizationID
	if h.ciRepo == nil || res.Action == reviews.ActionDismiss {
		return false, nil
	}
	switch res.Action {
	case reviews.ActionMerge:
		if res.CIID == "" {
			return false, refused(http.StatusBadRequest, "ci_id is required to merge")
		}
		if !containsString(item.CandidateCIIDs, res.CIID) {
			return false, refused(http.StatusBadRequest, "ci_id is not one of the candidate CIs")
		}
		switch item.Kind {
		case ReviewKindResurrectedDevice:
			// The device seen again is the deleted CI (CI-13).
			restorer, ok := h.ciRepo.(ciRestorer)
			if !ok {
				return false, refused(http.StatusNotImplemented, "this repository cannot reactivate CIs")
			}
			if _, err := restorer.Restore(ctx, orgID, res.CIID); err != nil {
				return false, err
			}
			res.Note = "reactivated " + res.CIID
		case ReviewKindProbableDuplicate:
			// The new CI is the existing one: it is removed (CH29).
			if dup := payloadString(item.Payload, "ci_id"); dup != "" && dup != res.CIID {
				if err := h.ciRepo.Delete(ctx, orgID, dup); err != nil && !isNotFound(err) {
					return false, err
				}
				res.Note = "merged " + dup + " into " + res.CIID
				return false, nil
			}
		}
		now := time.Now().UTC().Format(time.RFC3339)
		source := ci.SourceSweep
		if _, err := h.ciRepo.Update(ctx, orgID, res.CIID, ci.UpdateRequest{
			ManagementIP:    payloadStringPtr(item.Payload, "management_ip"),
			DiscoverySource: &source,
			LastSeenAt:      &now,
		}); err != nil {
			return false, err
		}
		if res.Note == "" {
			res.Note = "merged into " + res.CIID
		}
	case reviews.ActionNew:
		if payloadString(item.Payload, "reason") == ReasonNoProfileMatch {
			// The device already exists as generic_device CI.
			return false, refused(http.StatusConflict, "the device is already recorded as generic_device; set its type on the CI and dismiss the item")
		}
		nowTime := time.Now().UTC()
		newItem := ci.Item{
			OrganizationID:  orgID,
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
			DiscoverySource: ci.SourceSweep,
			FirstSeenAt:     &nowTime,
			LastSeenAt:      &nowTime,
		}
		if err := h.ciRepo.Create(ctx, &newItem); err != nil {
			return false, err
		}
		res.Note = "created new CI " + newItem.ID
	case reviews.ActionAccept:
		// The observed identity values replace the stored ones.
		ciID := payloadString(item.Payload, "matched_ci_id")
		if ciID == "" {
			return false, refused(http.StatusUnprocessableEntity, "the item names no CI")
		}
		// The reported values, kept unchanged in the item (REC-06).
		incoming, _ := item.Payload["incoming"].(map[string]any)
		value := func(field string) *string {
			if v, ok := incoming[field].(string); ok && v != "" {
				return &v
			}
			return nil
		}
		update := ci.UpdateRequest{SerialNumber: value("serial_number"), HardwareUUID: value("hardware_uuid")}
		if update.SerialNumber == nil && update.HardwareUUID == nil {
			return false, refused(http.StatusUnprocessableEntity, "the item reports no value to accept")
		}
		if _, err := h.ciRepo.Update(ctx, orgID, ciID, update); err != nil {
			return false, err
		}
		res.Note = "accepted the observed values on " + ciID
	}
	return false, nil
}

func isNotFound(err error) bool {
	return err != nil && (err.Error() == "not found" || errors.Is(err, reviews.ErrNotFound))
}

// ciRestorer reactivates soft-deleted CIs; the PostgreSQL CI repository
// implements it.
type ciRestorer interface {
	Restore(ctx context.Context, orgID, id string) (*ci.Item, error)
}

// OverrideClearer removes the manual override of a field; the CI returns to
// the observed value (OVR-01). The override repository implements it.
type OverrideClearer interface {
	ClearOverride(ctx context.Context, orgID, ciID, field string) error
}

// resolveOverrideConflict accepts the observed value of an override_conflict
// item: the override is removed and the CI takes the observed value
// (REC-12). Dismissing keeps the override; the dismissed value is then
// suppressed until the source reports another (queueOverrideConflict).
func (h *Handler) resolveOverrideConflict(ctx context.Context, item *ReviewItem, res *Resolution) (bool, error) {
	if res.Action != reviews.ActionAccept {
		res.Note = "override kept"
		return false, nil
	}
	clearer, ok := h.provenance.(OverrideClearer)
	if !ok {
		return false, refused(http.StatusNotImplemented, "overrides cannot be cleared here")
	}
	ciID, field := payloadString(item.Payload, "ci_id"), payloadString(item.Payload, "field")
	if err := clearer.ClearOverride(ctx, item.OrganizationID, ciID, field); err != nil {
		return false, err
	}
	res.Note = "override of " + field + " removed, observed value accepted"
	return false, nil
}

// resolveUnlicensed adopts (new) or discards (dismiss) an unlicensed_ci
// item; adoption is refused while max_cis is still reached (ENT-03).
func (h *Handler) resolveUnlicensed(ctx context.Context, item *ReviewItem, res *Resolution) (bool, error) {
	if res.Action != reviews.ActionNew {
		return false, nil
	}
	if _, err := h.adoptUnlicensed(ctx, item.OrganizationID, res.ResolvedBy, item); err != nil {
		return false, err
	}
	return true, nil
}

// reviewItemFromValueConflicts maps contradicting identity values on a
// matched CI into a review item (spec §5.3: Konfliktlösung nach
// Quellenvertrauen + Aktualität; Unklarheiten in Review-Queue). It returns
// nil when there is nothing to review.
func reviewItemFromValueConflicts(orgID string, matched ci.Item, incoming IngestItem, result ReconcileResult) *ReviewItem {
	if len(result.ValueConflicts) == 0 {
		return nil
	}
	// The reported values stay unchanged next to the stored ones (REC-06).
	incomingValues := map[string]any{}
	for _, field := range result.ValueConflicts {
		switch field {
		case "serial_number":
			incomingValues[field] = incoming.SerialNumber
		case "hardware_uuid":
			incomingValues[field] = incomingUUID(&incoming)
		}
	}
	payload := map[string]any{
		"criterion":          result.Criterion,
		"matched_ci_id":      result.MatchedCIID,
		"conflict_fields":    result.ValueConflicts,
		"incoming":           incomingValues,
		"ci_type_name":       incoming.CITypeName,
		"name":               incoming.Name,
		"manufacturer":       incoming.Manufacturer,
		"model":              incoming.Model,
		"serial_number":      incoming.SerialNumber,
		"management_ip":      incoming.ManagementIP,
		"hardware_uuid":      incomingUUID(&incoming),
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
		ObjectKey:      "ci:" + result.MatchedCIID,
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
		ObjectKey:      deviceKey(&incoming),
	}
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
