package asset

import (
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
)

// LabelSubject maps an asset onto the neutral label subject (INV-03,
// WP-849, E-52): the code is the inventory number, without one the URN
// urn:reticora:asset:<uuid>; both resolve through GET
// /api/v1/labels/lookup (WP-762). The title is the name, the subtitle
// manufacturer and model. The label carries no deep link and no host.
func LabelSubject(a *Asset) labels.Subject {
	code := a.AssetTag
	if strings.TrimSpace(code) == "" {
		code = labels.URN(labels.KindAsset, a.ID)
	}
	title := a.Name
	if title == "" {
		title = code
	}
	return labels.Subject{
		Kind:     labels.KindAsset,
		ID:       a.ID,
		Code:     code,
		Title:    title,
		Subtitle: strings.TrimSpace(a.Manufacturer + " " + a.Model),
	}
}
