package webhook

import (
	"context"
	"maps"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
)

// TestWebhookAssetEventsProjection covers the asset events of WP-740
// (EVT-03) under the field permissions of WP-730: asset.created and
// asset.changed carry the asset projected by the asset policy (version and
// site stay), changed_fields of asset.changed names only readable fields,
// and an asset.changed whose changed fields are all unreadable is
// withheld from the subscription; the unbound subscription reads all.
func TestWebhookAssetEventsProjection(t *testing.T) {
	bg := context.Background()
	mem := permission.NewMemoryFieldGrants()
	grant := permission.FieldGrant{RoleID: strPtr(frRole), ObjectKind: permission.KindAsset,
		FieldKey: strPtr("core.serial_number"), Level: permission.LevelNone}
	if _, err := mem.CreateFieldGrant(bg, frOrg, &grant); err != nil {
		t.Fatal(err)
	}
	access := frAccess{
		fakeAccess: fakeAccess{"sa-1|asset:read|*": true, "sa-1|asset:read|" + frClient: true},
		resolver:   permission.NewFieldResolver(nil, nil, mem, nil),
		grants:     map[string][]identity.Grant{"sa-1": {{RoleID: frRole, Permissions: []identity.Permission{"asset:read"}}}},
	}
	e := newFREnv(t, access, nil, "asset.created", "asset.changed")
	asset := func(changed ...string) map[string]any {
		body := map[string]any{"id": "00000000-0000-4000-8000-0000000007a0", "organization_id": frOrg, "client_id": frClient,
			"site_id": "00000000-0000-4000-8000-0000000007a1", "name": "asset-740", "version": 4,
			"serial_number": frSerial, "model": "R650", "custom_fields": map[string]any{"rack": "r1"}}
		if changed != nil {
			body["changed_fields"] = changed
		}
		return body
	}

	got := e.dispatch("asset.created", asset())
	bound := decode(t, got["bound"])
	if bound["serial_number"] != nil || bound["model"] != "R650" || bound["version"] != float64(4) || bound["site_id"] == nil {
		t.Errorf("bound asset.created %v", bound)
	}
	if decode(t, got["unbound"])["serial_number"] != frSerial {
		t.Errorf("unbound asset.created %s", got["unbound"])
	}

	got = e.dispatch("asset.changed", asset("model", "serial_number"))
	bound = decode(t, got["bound"])
	changed, _ := bound["changed_fields"].([]any)
	if bound["serial_number"] != nil || len(changed) != 1 || changed[0] != "model" {
		t.Errorf("bound asset.changed %v", bound)
	}
	if unbound := decode(t, got["unbound"]); len(unbound["changed_fields"].([]any)) != 2 {
		t.Errorf("unbound asset.changed %v", unbound)
	}

	got = e.dispatch("asset.changed", asset("serial_number"))
	if _, delivered := got["bound"]; delivered {
		t.Errorf("asset.changed of an unreadable field reached the bound subscription: %s", got["bound"])
	}
	if names := slices.Sorted(maps.Keys(got)); !slices.Equal(names, []string{"unbound"}) {
		t.Errorf("deliveries %v, want unbound only", names)
	}
}
