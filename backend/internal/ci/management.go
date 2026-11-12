package ci

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// ManagementAddressSyncer keeps the management IP of a CI and its IPAM
// address with role management consistent in the writing transaction
// (IPM-13, WP-913, E-215): ipam.ManagementSync. discovered marks a write of
// the ingest.
type ManagementAddressSyncer interface {
	SyncManagementAddressTx(ctx context.Context, tx pgx.Tx, orgID, ciID, vrfID, oldIP, newIP string, discovered bool) error
	// SyncManagementAddressesTx synchronises several new CIs set-based
	// (ciIDs, vrfIDs and ips by index): the statements are those of one CI.
	SyncManagementAddressesTx(ctx context.Context, tx pgx.Tx, orgID string, ciIDs, vrfIDs, ips []string, discovered bool) error
}

// WithManagementSync wires the synchronisation of the management address
// (router.go); without it a CI write touches no address.
func (r *PGRepository) WithManagementSync(s ManagementAddressSyncer) *PGRepository {
	r.mgmt = s
	return r
}

// recordWriteKey marks the context of a write of the ingest (UpdateRecord):
// its management address is discovered.
type recordWriteKey struct{}

// isRecordWrite reports a write of the ingest.
func isRecordWrite(ctx context.Context) bool {
	v, _ := ctx.Value(recordWriteKey{}).(bool)
	return v
}

// discoveredItem reports a CI written by discovery rather than by a user:
// not manual and with a discovery source.
func discoveredItem(item *Item) bool {
	return !item.IsManual && item.DiscoverySource != ""
}

// ManagementVRF is the VRF the management IP of item was reported in
// (attributes.raw_data.vrf_id, as the identity of the ingest reads it);
// empty means the default VRF.
func ManagementVRF(item *Item) string {
	if item == nil {
		return ""
	}
	if raw, ok := item.Attributes["raw_data"].(map[string]any); ok {
		if v, ok := raw["vrf_id"].(string); ok {
			return v
		}
	}
	return ""
}

// syncManagement synchronises the management address of after when its
// management IP or VRF differs from before (nil: a new CI) or force is set
// (the merge); otherwise it costs no statement (AK7).
func (r *PGRepository) syncManagement(ctx context.Context, tx pgx.Tx, orgID string, before, after *Item, discovered, force bool) error {
	if r.mgmt == nil || after == nil {
		return nil
	}
	oldIP, oldVRF := "", ""
	if before != nil {
		oldIP, oldVRF = before.ManagementIP, ManagementVRF(before)
	}
	newVRF := ManagementVRF(after)
	if !force && oldIP == after.ManagementIP && (after.ManagementIP == "" || oldVRF == newVRF) {
		return nil
	}
	return r.mgmt.SyncManagementAddressTx(ctx, tx, orgID, after.ID, newVRF, oldIP, after.ManagementIP, discovered)
}

// syncCreatedManagement synchronises the management addresses of the new
// CIs created with a management IP, set-based: one synchronisation per
// source (discovery or manual) whatever their number, none without a
// management IP (AK7).
func (r *PGRepository) syncCreatedManagement(ctx context.Context, tx pgx.Tx, orgID string, created []*Item) error {
	if r.mgmt == nil {
		return nil
	}
	type set struct{ ciIDs, vrfIDs, ips []string }
	var bySource [2]set
	for _, item := range created {
		if item == nil || item.ManagementIP == "" {
			continue
		}
		i := 0
		if discoveredItem(item) {
			i = 1
		}
		s := &bySource[i]
		s.ciIDs, s.vrfIDs, s.ips = append(s.ciIDs, item.ID), append(s.vrfIDs, ManagementVRF(item)), append(s.ips, item.ManagementIP)
	}
	for i, s := range bySource {
		if len(s.ciIDs) == 0 {
			continue
		}
		if err := r.mgmt.SyncManagementAddressesTx(ctx, tx, orgID, s.ciIDs, s.vrfIDs, s.ips, i == 1); err != nil {
			return err
		}
	}
	return nil
}
