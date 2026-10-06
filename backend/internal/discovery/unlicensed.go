package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
)

// ReviewKindUnlicensedCI holds a discovered device that would exceed max_cis
// (ENT-03, CH21, E-22): the device record is kept in the payload, nothing is
// lost; raising the limit adopts it as a CI, dismissing discards it.
const ReviewKindUnlicensedCI = "unlicensed_ci"

// LimitGuard enforces licensed quotas before a discovered device becomes a
// CI. The entitlement service implements it.
type LimitGuard interface {
	AllowCreate(ctx context.Context, orgID, quota string, current int64) error
}

// WithLimits enforces max_cis on new discovered CIs.
func (h *Handler) WithLimits(guard LimitGuard) *Handler {
	h.limits = guard
	return h
}

var unlicensedQueued atomic.Int64

// UnlicensedQueued is the number of devices queued as unlicensed_ci since
// start.
func UnlicensedQueued() int64 { return unlicensedQueued.Load() }

// RegisterMetrics registers reticora_unlicensed_ci_total (ENT-03).
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
		Namespace: "reticora",
		Name:      "unlicensed_ci_total",
		Help:      "Discovered devices held as unlicensed_ci review items because max_cis is reached.",
	}, func() float64 { return float64(UnlicensedQueued()) }))
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
// provenance of every discovered field (spec §13).
func (h *Handler) createDiscoveredCI(ctx context.Context, orgID string, item *IngestItem, typeID, source string) (ci.Item, error) {
	now := time.Now().UTC()
	newItem := ci.Item{
		OrganizationID:  orgID,
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
		Attributes:      discoveredAttributes(item),
		DiscoverySource: source,
		FirstSeenAt:     &now,
		LastSeenAt:      &now,
	}
	if err := h.ciRepo.Create(ctx, &newItem); err != nil {
		return ci.Item{}, err
	}
	// New CIs carry no overrides; every discovered field is recorded as
	// provenance so future drift is visible.
	for field, value := range map[string]any{
		"name": item.Name, "manufacturer": item.Manufacturer, "model": item.Model,
		"serial_number": item.SerialNumber, "management_ip": item.ManagementIP,
		"hostname": item.Hostname, "fqdn": item.FQDN,
	} {
		h.recordDiscovered(ctx, orgID, newItem.ID, field, value, source)
	}
	for k, v := range item.Attributes {
		h.recordDiscovered(ctx, orgID, newItem.ID, k, v, source)
	}
	return newItem, nil
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
type queuedDevices struct{ keys map[string]bool }

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
	review := &ReviewItem{
		OrganizationID: orgID,
		Kind:           ReviewKindUnlicensedCI,
		Status:         ReviewStatusOpen,
		Payload: map[string]any{
			"device_key":   key,
			"device":       device,
			"ci_type_id":   typeID,
			"ci_type_name": typeID,
			"name":         item.Name,
			"source":       source,
			"limit":        exceeded.Limit,
			"current":      exceeded.Current,
		},
	}
	if err = h.repo.CreateReviewItem(ctx, review); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			// Queued concurrently by another ingest.
			queued.keys[key] = true
			return true, nil
		}
		return false, err
	}
	queued.keys[key] = true
	unlicensedQueued.Add(1)
	slog.WarnContext(ctx, "discovered device held as unlicensed_ci: max_cis reached",
		"event", "entitlement.unlicensed_ci", "organization_id", orgID, "review_item_id", review.ID,
		"limit", exceeded.Limit, "current", exceeded.Current)
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

// adoptUnlicensed creates the CI of an unlicensed_ci item when max_cis
// allows it and resolves the item. It returns the entitlement error when the
// limit is still reached.
func (h *Handler) adoptUnlicensed(ctx context.Context, orgID, actorID string, item *ReviewItem) (string, error) {
	if h.ciRepo == nil {
		return "", errors.New("discovery: no CI repository")
	}
	if h.limits != nil {
		_, total, err := h.ciRepo.List(ctx, orgID, ci.FilterParams{}, api.PaginationParams{Limit: 1})
		if err != nil {
			return "", err
		}
		if limitErr := h.limits.AllowCreate(ctx, orgID, entitlement.LimitMaxCIs, int64(total)); limitErr != nil {
			return "", limitErr
		}
	}
	var device IngestItem
	raw, err := json.Marshal(item.Payload["device"])
	if err != nil {
		return "", err
	}
	if err = json.Unmarshal(raw, &device); err != nil {
		return "", fmt.Errorf("unlicensed_ci %s: device record unreadable: %w", item.ID, err)
	}
	created, err := h.createDiscoveredCI(ctx, orgID, &device, payloadString(item.Payload, "ci_type_id"),
		payloadString(item.Payload, "source"))
	if err != nil {
		return "", err
	}
	if _, err = h.repo.ResolveReviewItem(ctx, orgID, item.ID, Resolution{Status: ReviewStatusResolved,
		ResolvedBy: actorID, Note: "created CI " + created.ID + " within max_cis"}); err != nil {
		return "", err
	}
	return created.ID, nil
}

// AdoptUnlicensed creates the CIs of open unlicensed_ci items, oldest first,
// as long as max_cis allows (ENT-03: raising the limit creates the CIs). It
// returns the number adopted; the remaining items stay open.
func (h *Handler) AdoptUnlicensed(ctx context.Context, orgID string) (int, error) {
	items, err := h.openUnlicensed(ctx, orgID)
	if err != nil {
		return 0, err
	}
	adopted := 0
	for i := range items {
		if _, err = h.adoptUnlicensed(ctx, orgID, "", &items[i]); err != nil {
			var exceeded *entitlement.LimitExceededError
			if errors.As(err, &exceeded) {
				break
			}
			return adopted, err
		}
		adopted++
	}
	if adopted > 0 {
		slog.InfoContext(ctx, "unlicensed_ci items adopted", "organization_id", orgID, "adopted", adopted,
			"remaining", len(items)-adopted)
	}
	return adopted, nil
}
