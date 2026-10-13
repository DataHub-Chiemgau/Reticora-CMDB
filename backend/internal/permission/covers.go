package permission

import (
	"slices"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

// Covers reports whether grants hold the permission for an object in the
// given client and site (empty: an object of the whole organization). An
// org-wide grant covers every object; a client or site grant covers the
// objects of its clients or sites only, and no organization-wide object
// (RBA-03, fail-closed). Notification recipients are resolved with it
// (NTF-03): only who may read the object hears of it.
func Covers(grants []identity.Grant, key identity.Permission, clientID, siteID string) bool {
	for i := range grants {
		g := &grants[i]
		if !slices.Contains(g.Permissions, key) {
			continue
		}
		if len(g.Clients) == 0 && len(g.Sites) == 0 {
			return true
		}
		clientOK := len(g.Clients) == 0 || (clientID != "" && slices.Contains(g.Clients, clientID))
		siteOK := len(g.Sites) == 0 || (siteID != "" && slices.Contains(g.Sites, siteID))
		if clientOK && siteOK {
			return true
		}
	}
	return false
}
