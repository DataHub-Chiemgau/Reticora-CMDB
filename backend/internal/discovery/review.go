package discovery

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
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
		svc.WithEffect(ReviewKindScopeSuggested, h.resolveScopeSuggested)
		svc.WithEffect(ReviewKindUnknownRelationship, h.resolveUnknownRelationship)
		if h.reviewNotifier != nil {
			svc.WithNotifier(afterCommitNotifier{next: h.reviewNotifier})
		}
		if h.reviewJobs != nil {
			svc.WithJobs(h.reviewJobs)
		}
		h.reviews = svc
	})
	return h.reviews
}

// WithReviewJobs records the merge resolutions of review items as jobs
// (JOB-04).
func (h *Handler) WithReviewJobs(tracker jobs.Tracker) *Handler {
	h.reviewJobs = tracker
	return h
}

// WithReviewNotifier attaches the notification of opened review items
// (REC-08, NTF-05).
func (h *Handler) WithReviewNotifier(n reviews.Notifier) *Handler {
	h.reviewNotifier = n
	return h
}

// afterCommitNotifier announces an opened item once the transaction that
// opened it has committed (NFR-03): the notification writes the outbox in a
// transaction of its own, which would wait forever for the organization's
// event lock the ingest's record transaction holds, and an item rolled back
// with its record is never announced. Outside a transaction it notifies at
// once.
type afterCommitNotifier struct{ next reviews.Notifier }

func (n afterCommitNotifier) ReviewOpened(ctx context.Context, item *reviews.Item) {
	opened := *item
	database.AfterCommit(ctx, func(ctx context.Context) { n.next.ReviewOpened(ctx, &opened) })
}

// ReasonNoProfileMatch marks an unclassified_device item of a device that
// no collector profile matched (DIS-02); its generic_device CI exists.
const ReasonNoProfileMatch = "no_profile_match"

// unclassifiedDevice reports whether the collector's classification chain
// found no profile for the item (generic_device, classified_by fallback).
func unclassifiedDevice(item *IngestItem) bool {
	return item.CITypeName == "generic_device" && item.Attributes["classified_by"] == "fallback"
}

// openReview opens a review item, deduplicated per object and type, and
// reports whether it is new.
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
		// The CI, its provenance and the resolution commit together: a
		// resolution the principal may not write (E-09) leaves no CI.
		err := h.inTx(ctx, orgID, func(ctx context.Context) error {
			ciID, err := h.createReviewedDevice(ctx, item)
			if err != nil {
				return err
			}
			res.Note = "created new CI " + ciID
			_, err = h.repo.ResolveReviewItem(ctx, orgID, item.ID, res)
			return err
		})
		return err == nil, err
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

// createReviewedDevice creates the CI of the device whose record a review
// item keeps (devicePayload) as the ingest creates a new device (CI-13): the
// type name resolved, the reported values checked against the field rules,
// identity values that identify nothing or that another CI holds left out,
// the provenance of every field recorded. Client and site are those of the
// item (reviewedDeviceScope), so the next sighting matches the CI.
func (h *Handler) createReviewedDevice(ctx context.Context, item *ReviewItem) (string, error) {
	orgID := item.OrganizationID
	clientID, siteID, err := h.reviewedDeviceScope(ctx, item)
	if err != nil {
		return "", err
	}
	device := reviewedDevice(item.Payload)
	device.ClientID = clientID
	typeID := strings.TrimSpace(device.CITypeName)
	if typeID != "" && !isUUID(typeID) {
		// The type of an unknown_ci_type item may exist by now.
		types, lookupErr := h.lookupTypes(ctx, orgID, []string{typeID})
		if lookupErr != nil {
			return "", lookupErr
		}
		if id := types[typeID]; id != "" {
			typeID = id
		}
	}
	candidates, err := h.identityCandidates(ctx, orgID, &device)
	if err != nil {
		return "", err
	}
	if _, err = h.dropInvalidObserved(ctx, orgID, typeID, "", &device); err != nil {
		return "", err
	}
	withoutBlocklistedIdentity(&device)
	withoutTakenIdentity(&device, candidates)
	source := device.Source
	if source == "" {
		source = ci.SourceSweep
	}
	newItem := discoveredCI(orgID, &device, typeID, source)
	// The site is the root of its location tree; the database derives
	// site_id from the location (DB-05).
	newItem.LocationID = siteID
	newItem.Attributes["review_item_id"] = item.ID
	if err := h.ciRepo.Create(ctx, &newItem); err != nil {
		return "", err
	}
	if err := h.recordDiscoveredMany(ctx, orgID, newItem.ID, discoveredFields(&device), source, device.ObservedAt); err != nil {
		return "", err
	}
	return newItem.ID, nil
}

// reviewedDeviceScope returns the client and site of the CI new creates. The
// columns of the item decide (WP-342): the database derives them from the
// candidate CIs or, without candidates, keeps those of the device's record
// or collector. Without a client column the record in the payload decides
// (client_id "" for an org-wide sighting). An item from before the payload
// kept the client takes the client its candidate CIs share; otherwise new
// is refused, since a guess could hand the device to another client.
func (h *Handler) reviewedDeviceScope(ctx context.Context, item *ReviewItem) (clientID, siteID string, err error) {
	if item.ClientID != "" {
		return item.ClientID, item.SiteID, nil
	}
	if recorded, ok := item.Payload["client_id"].(string); ok {
		siteID = item.SiteID
		if siteID == "" && recorded != "" {
			siteID = payloadString(item.Payload, "site_id")
		}
		return recorded, siteID, nil
	}
	clientID, ok, err := h.candidateClient(ctx, item.OrganizationID, item.CandidateCIIDs)
	if err != nil {
		return "", "", err
	}
	if !ok {
		return "", "", refused(http.StatusUnprocessableEntity,
			"the item does not record the client of the device; dismiss it and wait for the next sighting")
	}
	return clientID, item.SiteID, nil
}

// candidateClient returns the client all candidate CIs share ("" when all
// are org-wide). ok is false without candidates, when their clients differ
// or when one cannot be read (soft-deleted or not visible).
func (h *Handler) candidateClient(ctx context.Context, orgID string, ids []string) (clientID string, ok bool, err error) {
	for i, id := range ids {
		candidate, getErr := h.ciRepo.GetByID(ctx, orgID, id)
		if isNotFound(getErr) {
			return "", false, nil
		}
		if getErr != nil {
			return "", false, getErr
		}
		if i > 0 && candidate.ClientID != clientID {
			return "", false, nil
		}
		clientID = candidate.ClientID
	}
	return clientID, len(ids) > 0, nil
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
	payload := devicePayload(&incoming)
	payload["criterion"] = result.Criterion
	return &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindAmbiguousIdentity,
		Status:         ReviewStatusOpen,
		Payload:        payload,
		CandidateCIIDs: result.CandidateCIIDs,
		ObjectKey:      deviceKey(&incoming),
	}
}

// devicePayload keeps the record of a device in the payload of a review
// item that new may turn into a CI (ambiguous_identity, resurrected_device,
// unclassified_device of an unknown type): what the ingest would create the
// CI from, with the client and site of the record ("" for an org-wide
// sighting, CI-13).
func devicePayload(item *IngestItem) map[string]any {
	return map[string]any{
		"ci_type_name":  item.CITypeName,
		"name":          item.Name,
		"manufacturer":  item.Manufacturer,
		"model":         item.Model,
		"serial_number": item.SerialNumber,
		"management_ip": item.ManagementIP,
		"hardware_uuid": item.HardwareUUID,
		"primary_mac":   item.PrimaryMAC,
		"hostname":      item.Hostname,
		"fqdn":          item.FQDN,
		"sys_object_id": item.SysObjectID,
		"fingerprint":   item.Fingerprint,
		"raw_data":      item.RawData,
		"source":        item.Source,
		"client_id":     item.ClientID,
		"site_id":       item.SiteID,
	}
}

// reviewedDevice is the device record devicePayload kept; client and site
// are left to reviewedDeviceScope.
func reviewedDevice(payload map[string]any) IngestItem {
	fingerprint, _ := payload["fingerprint"].(map[string]any)
	rawData, _ := payload["raw_data"].(map[string]any)
	return IngestItem{
		CITypeName:   payloadString(payload, "ci_type_name"),
		Name:         payloadString(payload, "name"),
		Manufacturer: payloadString(payload, "manufacturer"),
		Model:        payloadString(payload, "model"),
		SerialNumber: payloadString(payload, "serial_number"),
		ManagementIP: payloadString(payload, "management_ip"),
		HardwareUUID: payloadString(payload, "hardware_uuid"),
		PrimaryMAC:   payloadString(payload, "primary_mac"),
		Hostname:     payloadString(payload, "hostname"),
		FQDN:         payloadString(payload, "fqdn"),
		SysObjectID:  payloadString(payload, "sys_object_id"),
		Fingerprint:  fingerprint,
		RawData:      rawData,
		Source:       payloadString(payload, "source"),
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
