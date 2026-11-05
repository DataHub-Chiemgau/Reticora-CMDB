package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
)

// ReviewKindUnlicensedCI holds a discovered device that would exceed max_cis
// (ENT-03, CH21, E-22): the device record is kept in the payload, nothing is
// lost; raising the limit adopts it as a CI, dismissing discards it.
const ReviewKindUnlicensedCI = reviews.KindUnlicensedCI

// QuotaGuard enforces licensed quotas before a resource is created
// (ENT-03): max_cis before a discovered device becomes a CI, max_collectors
// before a collector is registered or enrolled. current is the number of
// resources the organization holds; over the limit it returns an error with
// LimitExceeded (403 entitlement-limit). The entitlement service implements
// it.
type QuotaGuard interface {
	AllowCreate(ctx context.Context, orgID, quota string, current int64) error
}

// WithLimits enforces max_cis on new discovered CIs and max_collectors on
// POST /collectors and POST /collectors/enroll.
func (h *Handler) WithLimits(guard QuotaGuard) *Handler {
	h.limits = guard
	return h
}

var unlicensedQueued atomic.Int64

// UnlicensedQueued is the number of devices queued as unlicensed_ci since
// start.
func UnlicensedQueued() int64 { return unlicensedQueued.Load() }

// RegisterMetrics registers reticora_unlicensed_ci_total (ENT-03),
// reticora_ingest_future_records_total (GLO-12) and the metrics of the
// ingest lanes (WP-334).
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "ingest_future_records_total",
		Help:      "Ingested records rejected because observed_at is more than five minutes in the future.",
	}, func() float64 { return float64(FutureRecordsRejected()) }))
	reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "unlicensed_ci_total",
		Help:      "Discovered devices held as unlicensed_ci review items because max_cis is reached.",
	}, func() float64 { return float64(UnlicensedQueued()) }))
	// Discovery error states (DIS-08).
	reg.MustRegister(scopeRuns, discoveryErrors)
	// Losses and backpressure reported by the collectors (NFR-04, F-234).
	reg.MustRegister(collectorSpoolDropped, collectorSpoolBackpressure)
	reg.MustRegister(ingestLanesGauge, laneBacklog, laneRecords, recordRetries, deadLetters)
}

// discoveredAttributes are the attributes a discovered device stores on its
// CI: fingerprint, raw data and the dynamic discovery attributes.
func discoveredAttributes(item *IngestItem) map[string]any {
	attributes := map[string]any{
		"fingerprint": item.Fingerprint,
		"raw_data":    item.RawData,
	}
	for k, v := range item.Attributes {
		if k == "fingerprint" || k == "raw_data" {
			continue
		}
		attributes[k] = v
	}
	return attributes
}

// createDiscoveredCI creates the CI of a discovered device and records the
// provenance of every discovered field (spec §13). A non-empty assetID
// links the CI to that asset with the insert (WP-741).
func (h *Handler) createDiscoveredCI(ctx context.Context, orgID string, item *IngestItem, typeID, source, assetID string) (ci.Item, error) {
	newItem := discoveredCI(orgID, item, typeID, source)
	newItem.AssetID = assetID
	if err := h.ciRepo.Create(ctx, &newItem); err != nil {
		return ci.Item{}, err
	}
	// New CIs carry no overrides; every discovered field is recorded as
	// provenance so future drift is visible.
	if err := h.recordDiscoveredMany(ctx, orgID, newItem.ID, discoveredFields(item), source, item.ObservedAt); err != nil {
		return ci.Item{}, err
	}
	return newItem, nil
}

// discoveredCI is the CI of a discovered device, before it is created; the
// batch of WP-333 and createDiscoveredCI build it alike.
func discoveredCI(orgID string, item *IngestItem, typeID, source string) ci.Item {
	// First and last seen when the record observed the device (GLO-12).
	seen := seenAt(item)
	return ci.Item{
		OrganizationID: orgID,
		// The client of the discovery scope (REC-02).
		ClientID:        item.ClientID,
		CITypeID:        typeID,
		Name:            item.Name,
		Status:          "active",
		Manufacturer:    item.Manufacturer,
		Model:           item.Model,
		SerialNumber:    item.SerialNumber,
		HardwareUUID:    item.HardwareUUID,
		ManagementIP:    item.ManagementIP,
		PrimaryMAC:      item.PrimaryMAC,
		Hostname:        item.Hostname,
		FQDN:            item.FQDN,
		SysObjectID:     item.SysObjectID,
		Attributes:      discoveredAttributes(item),
		DiscoverySource: source,
		FirstSeenAt:     &seen,
		LastSeenAt:      &seen,
	}
}

// discoveredFields are the fields of a new discovered CI whose provenance is
// recorded.
func discoveredFields(item *IngestItem) map[string]any {
	fields := map[string]any{
		"name": item.Name, "manufacturer": item.Manufacturer, "model": item.Model,
		"serial_number": item.SerialNumber, "management_ip": item.ManagementIP,
		"hostname": item.Hostname, "fqdn": item.FQDN,
	}
	for k, v := range item.Attributes {
		fields[k] = v
	}
	return fields
}

// deviceKey identifies a discovered device across ingests so it is queued
// once: the strongest identity field present.
func deviceKey(item *IngestItem) string {
	for _, c := range []struct{ name, value string }{
		{"hardware_uuid", item.HardwareUUID},
		{"serial", strings.TrimSpace(item.Manufacturer + "/" + item.SerialNumber)},
		{"mac", item.PrimaryMAC},
		{"fqdn", item.FQDN},
		{"ip", item.ManagementIP},
		{"hostname", item.Hostname},
		{"name", item.Name},
	} {
		if c.name == "serial" && item.SerialNumber == "" {
			continue
		}
		if v := strings.ToLower(strings.TrimSpace(c.value)); v != "" {
			return c.name + ":" + v
		}
	}
	return ""
}

// queuedDevices holds the device keys of open unlicensed_ci items of one
// ingest, loaded on first use.
type queuedDevices struct {
	keys map[string]bool
	// added lists the keys set since they were loaded, so that the keys of
	// a transaction that rolled back can be taken back (WP-334 retries).
	added []string
}

// queue marks the device key as queued.
func (q *queuedDevices) queue(key string) {
	if !q.keys[key] {
		q.keys[key] = true
		q.added = append(q.added, key)
	}
}

// mark returns the state rollback restores.
func (q *queuedDevices) mark() int {
	if q.keys == nil {
		return -1
	}
	return len(q.added)
}

// rollback forgets the keys queued after mark; keys loaded after it are
// dropped as a whole and loaded again on next use.
func (q *queuedDevices) rollback(mark int) {
	if mark < 0 {
		q.keys, q.added = nil, nil
		return
	}
	for _, key := range q.added[mark:] {
		delete(q.keys, key)
	}
	q.added = q.added[:mark]
}

// admitNewCI checks max_cis for a new discovered device. Over the limit the
// device is queued as unlicensed_ci (once per device) and admitNewCI reports
// true; any other entitlement failure is returned (no silent loss).
func (h *Handler) admitNewCI(ctx context.Context, orgID string, current int64, item *IngestItem, typeID, source string,
	queued *queuedDevices) (bool, error) {
	err := h.limits.AllowCreate(ctx, orgID, entitlement.LimitMaxCIs, current)
	if err == nil {
		return false, nil
	}
	var exceeded *entitlement.LimitExceededError
	if !errors.As(err, &exceeded) {
		return false, err
	}
	key := deviceKey(item)
	if queued.keys == nil {
		if queued.keys, err = h.openUnlicensedKeys(ctx, orgID); err != nil {
			return false, err
		}
	}
	if key != "" && queued.keys[key] {
		return true, nil
	}
	device, err := toMap(item)
	if err != nil {
		return false, err
	}
	clientID, siteID := h.deviceReviewScope(ctx, orgID, item)
	review := &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindUnlicensedCI,
		Status:         ReviewStatusOpen,
		ClientID:       clientID,
		SiteID:         siteID,
		Payload: map[string]any{
			"device_key":   key,
			"device":       device,
			"ci_type_id":   typeID,
			"ci_type_name": typeID,
			"name":         item.Name,
			"source":       source,
			"limit":        exceeded.Limit,
			"current":      exceeded.Current,
			// The CI created later is first seen at the record's time
			// (GLO-12); the device record carries no observed_at.
			"observed_at": seenAt(item).Format(time.RFC3339Nano),
		},
		ObjectKey: key,
	}
	// The savepoint keeps the record's transaction usable after a unique
	// violation of the open unlicensed_ci item of the device.
	var opened bool
	err = database.Savepoint(ctx, func(ctx context.Context) error {
		var openErr error
		opened, openErr = h.openReview(ctx, review)
		return openErr
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Queued concurrently by another ingest.
			queued.queue(key)
			return true, nil
		}
		return false, err
	}
	queued.queue(key)
	if !opened {
		return true, nil
	}
	// Counted once the item is committed.
	database.AfterCommit(ctx, func(ctx context.Context) {
		unlicensedQueued.Add(1)
		slog.WarnContext(ctx, "discovered device held as unlicensed_ci: max_cis reached",
			"event", "entitlement.unlicensed_ci", "organization_id", orgID, "review_item_id", review.ID,
			"limit", exceeded.Limit, "current", exceeded.Current)
	})
	return true, nil
}

// openUnlicensedKeys returns the device keys of the open unlicensed_ci items.
func (h *Handler) openUnlicensedKeys(ctx context.Context, orgID string) (map[string]bool, error) {
	items, err := h.openUnlicensed(ctx, orgID)
	if err != nil {
		return nil, err
	}
	keys := make(map[string]bool, len(items))
	for i := range items {
		keys[payloadString(items[i].Payload, "device_key")] = true
	}
	return keys, nil
}

// openUnlicensed lists every open unlicensed_ci item, oldest first.
func (h *Handler) openUnlicensed(ctx context.Context, orgID string) ([]ReviewItem, error) {
	var all []ReviewItem
	for offset := 0; ; offset += api.MaxPageLimit {
		page, total, err := h.repo.ListReviewItems(ctx, orgID, ReviewFilter{Status: ReviewStatusOpen, Kind: ReviewKindUnlicensedCI},
			api.PaginationParams{Limit: api.MaxPageLimit, Offset: offset})
		if err != nil {
			return nil, err
		}
		all = append(all, page...)
		if len(page) == 0 || offset+len(page) >= total {
			break
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].CreatedAt < all[j].CreatedAt })
	return all, nil
}

func toMap(v any) (map[string]any, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(raw, &out)
	return out, err
}

// allowActiveCI checks max_cis before a review resolution makes a CI
// active (ENT-03): adopting an unlicensed_ci item and new create one, the
// merge of a resurrected_device item reactivates the deleted CI. It counts
// the CIs as ci.Service.Create does, every active CI of the organization
// whatever the resolver's scope (ci.CountActive, F-403); over the limit it
// returns the entitlement error (403 entitlement-limit) and the item stays
// open. In the transaction that locks the item, creates or reactivates the
// CI and resolves the item (adoptUnlicensed, the review resolution) it
// admits through ci.CIAdmitter: the organization's max_cis lock, which the
// ingest lanes take too, and the count under it, so that AdoptUnlicensed
// after a grant and the ingest at the same time never pass the limit
// (WP-835, SR-09). Without a transaction (memory repository) it counts as
// before.
func (h *Handler) allowActiveCI(ctx context.Context, orgID string) error {
	if h.limits == nil {
		return nil
	}
	if database.InTx(ctx) {
		return ci.NewCIAdmitter(h.limits).AdmitNew(ctx, orgID, 1)
	}
	total, err := ci.CountActive(ctx, h.ciRepo, orgID)
	if err != nil {
		return err
	}
	return h.limits.AllowCreate(ctx, orgID, entitlement.LimitMaxCIs, total)
}

// adoptUnlicensed creates the CI of an unlicensed_ci item when max_cis
// allows it and resolves the item. It returns the entitlement error when the
// limit is still reached. The item is locked, the CI created and the item
// resolved in one transaction (F-404): of two adoptions of the same item
// (a request and the adoption after the limit was raised) one creates the
// CI, the other gets reviews.ErrAlreadyResolved and creates nothing.
func (h *Handler) adoptUnlicensed(ctx context.Context, orgID, actorID string, item *ReviewItem) (string, error) {
	if h.ciRepo == nil {
		return "", errors.New("discovery: no CI repository")
	}
	var created string
	err := h.inTx(ctx, orgID, func(ctx context.Context) error {
		if locker, ok := h.repo.(reviews.Locker); ok {
			locked, err := locker.LockReviewItem(ctx, orgID, item.ID)
			if err != nil {
				return err
			}
			if locked.Status != ReviewStatusOpen {
				return reviews.ErrAlreadyResolved
			}
			item = locked
		}
		var err error
		created, err = h.adoptHeld(ctx, orgID, actorID, item)
		return err
	})
	return created, err
}

// errNoDeviceRecord refuses the adoption of an unlicensed_ci item that holds
// no device record to create a CI from (F-405); the item stays open.
var errNoDeviceRecord = &reviews.Error{Status: http.StatusUnprocessableEntity,
	Message: "the item holds no device record to create a CI from; dismiss it"}

// adoptHeld creates the CI of the device an unlicensed_ci item holds and
// resolves the item, in the transaction of ctx.
func (h *Handler) adoptHeld(ctx context.Context, orgID, actorID string, item *ReviewItem) (string, error) {
	typeID := payloadString(item.Payload, "ci_type_id")
	record, ok := item.Payload["device"].(map[string]any)
	if !ok || !api.IsUUID(typeID) {
		return "", errNoDeviceRecord
	}
	if err := h.allowActiveCI(ctx, orgID); err != nil {
		return "", err
	}
	var device IngestItem
	raw, err := json.Marshal(record)
	if err != nil {
		return "", err
	}
	if err = json.Unmarshal(raw, &device); err != nil {
		return "", fmt.Errorf("unlicensed_ci %s: device record unreadable: %w", item.ID, err)
	}
	// Seen when the record observed it (GLO-12); an item held before the
	// payload carried observed_at was held when it was created.
	for _, at := range []string{payloadString(item.Payload, "observed_at"), item.CreatedAt} {
		if t, parseErr := time.Parse(time.RFC3339Nano, at); parseErr == nil {
			device.ObservedAt = t
			break
		}
	}
	// The held type may be deactivated by now (MET-05): resolved together
	// with generic_device in one lookup, the device then becomes a
	// generic_device and a reviewer classifies it.
	inactiveType := ""
	types, err := h.lookupTypes(ctx, orgID, []string{typeID, ci.GenericDeviceType})
	if err != nil {
		return "", err
	}
	if held, generic := types[typeID], types[ci.GenericDeviceType]; held.ID != "" && !held.Active && generic.ID != "" {
		inactiveType = payloadString(item.Payload, "ci_type_name")
		if inactiveType == "" {
			inactiveType = typeID
		}
		typeID = generic.ID
	}
	// The held values pass the field rules like a direct ingest.
	if _, err = h.dropInvalidObserved(ctx, orgID, typeID, "", &device); err != nil {
		return "", err
	}
	// The unique attributes are checked as POST /cis checks them, also
	// before their index exists: a value another active CI holds is 409
	// with the violations and the item stays open (MET-15, F-429).
	if uerr := ci.CheckUnique(ctx, h.ciRepo, orgID, typeID, device.ClientID, "", discoveredAttributes(&device)); uerr != nil {
		return "", uerr
	}
	created, err := h.createDiscoveredCI(ctx, orgID, &device, typeID, payloadString(item.Payload, "source"), "")
	if err != nil {
		if ve, ok := ci.AsValidationError(err); ok && len(ve.Violations) > 0 {
			return "", &reviews.Error{Status: http.StatusUnprocessableEntity, Field: ve.Violations[0].Field,
				Message: ve.Violations[0].Detail}
		}
		return "", err
	}
	if inactiveType != "" {
		if _, err = h.reviewTypeInactive(ctx, orgID, &device, &created, inactiveType); err != nil {
			return "", err
		}
	}
	if _, err = h.repo.ResolveReviewItem(ctx, orgID, item.ID, &Resolution{Status: ReviewStatusResolved, Action: reviews.ActionNew,
		ResolvedBy: actorID, Note: adoptedNote(created.ID)}); err != nil {
		return "", err
	}
	return created.ID, nil
}

// adoptedNote is the note of an unlicensed_ci item whose CI was created.
func adoptedNote(ciID string) string { return "created CI " + ciID + " within max_cis" }

// AdoptUnlicensed creates the CIs of open unlicensed_ci items, oldest first,
// as long as max_cis allows (ENT-03: raising the limit creates the CIs). It
// returns the number adopted; the remaining items stay open. The open
// tickets of every adopted item get a system comment (WP-767), whoever the
// context names: the adoption has no actor. adoptUnlicensed itself does
// not comment, so that the resolution new through the review service (its
// effect) is commented once.
func (h *Handler) AdoptUnlicensed(ctx context.Context, orgID string) (int, error) {
	items, err := h.openUnlicensed(ctx, orgID)
	if err != nil {
		return 0, err
	}
	adopted := 0
	for i := range items {
		var created string
		if created, err = h.adoptUnlicensed(ctx, orgID, "", &items[i]); err != nil {
			if errors.Is(err, reviews.ErrAlreadyResolved) {
				// Resolved meanwhile by a principal or another replica.
				continue
			}
			// Another active CI holds a value of a unique attribute of the
			// device (MET-15, F-429): the item stays open for a principal,
			// the others are adopted.
			var taken *ci.UniqueError
			if errors.As(err, &taken) {
				slog.WarnContext(ctx, "unlicensed_ci item not adopted: a unique value is taken", "organization_id", orgID,
					"review_item_id", items[i].ID, "attributes", taken.Fields)
				continue
			}
			var exceeded *entitlement.LimitExceededError
			if errors.As(err, &exceeded) {
				break
			}
			return adopted, err
		}
		adopted++
		resolved := items[i]
		resolved.Status, resolved.Action, resolved.Resolution = ReviewStatusResolved, reviews.ActionNew, adoptedNote(created)
		h.ReviewService().CommentResolved(ctx, &resolved, "")
	}
	if adopted > 0 {
		slog.InfoContext(ctx, "unlicensed_ci items adopted", "organization_id", orgID, "adopted", adopted,
			"remaining", len(items)-adopted)
	}
	return adopted, nil
}
