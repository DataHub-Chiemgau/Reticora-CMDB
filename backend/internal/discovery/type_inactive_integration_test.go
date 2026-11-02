package discovery_test

// WP-249 (MET-05): a deactivated CI type gets no new CIs from discovery.
// The record's type, a name or a UUID in any case, is resolved with its
// active flag; a new device of a deactivated type becomes a generic_device
// with an unclassified_device item (reason type_inactive, the reported
// type and the new CI), in the record path and in the batch path. A known
// CI of the deactivated type is updated as usual, without a type change and
// without an item; a generic_device is not raised to the deactivated type.
// An unknown UUID is unknown_ci_type without a CI. Resolution new of a
// type_inactive item is 409. A deactivated org type with the key of a
// system type does not hide it (LookupCITypeID, the agent's path). The
// adoption of a held device whose type is deactivated by now creates a
// generic_device and goes on with the next items.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed):
//
//	cd backend && TEST_DATABASE_URL=... TEST_APP_DATABASE_URL=... go test -count=1 -run '^TestDiscoveryDeactivatedType$' ./internal/discovery/

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

func TestDiscoveryDeactivatedType(t *testing.T) {
	f := scopetest.Seed(t, "26")
	bg := context.Background()
	org := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	str := func(sql string, args ...any) string {
		t.Helper()
		var s string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&s); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return s
	}
	generic := str(`SELECT id::text FROM ci_type WHERE key = 'generic_device' AND organization_id IS NULL`)
	server := str(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL`)
	widget := str(`INSERT INTO ci_type (organization_id, key, name, is_active) VALUES ($1, 'wp249-widget', 'wp249-widget', false)
		RETURNING id::text`, f.OrgA)

	cis := ci.NewPGRepositoryWithAudit(f.App, audit.NewPGRecorder())
	mux := func(batch bool) http.Handler {
		var repo ci.Repository = cis
		if !batch {
			repo = noBatch{cis}
		}
		m := chi.NewRouter()
		discovery.NewHandler(discovery.NewPGRepository(f.App), repo).
			WithProvenance(provenance{repo: override.NewPGRepository(f.App)}).RegisterRoutes(m)
		return m
	}
	seq := 0
	record := func(ciType, serial string) plugins.DeviceRecord {
		seq++
		return plugins.DeviceRecord{
			RecordID: plugins.NewRecordID(), ObservedAt: time.Now().UTC().Add(-time.Minute), Source: "snmp",
			TargetIP:       fmt.Sprintf("198.51.100.%d", seq),
			Identity:       plugins.DeviceIdentity{SerialNumber: serial},
			Classification: plugins.DeviceClassification{CIType: ciType, Manufacturer: "Reticora Labs", Model: "W-1"},
			System:         plugins.DeviceSystem{Name: strings.ToLower(serial)},
		}
	}
	type device struct{ id, typeID string }
	cisOf := func(serial string) []device {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT id::text, ci_type_id::text FROM ci WHERE organization_id = $1 AND serial_number = $2
			AND deleted_at IS NULL`, f.OrgA, serial)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []device
		for rows.Next() {
			var d device
			if err := rows.Scan(&d.id, &d.typeID); err != nil {
				t.Fatal(err)
			}
			out = append(out, d)
		}
		return out
	}
	type item struct{ id, reason, typeName, ciID string }
	itemsOf := func(serial string) []item {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT id::text, COALESCE(payload->>'reason', ''), COALESCE(payload->>'ci_type_name', ''),
				COALESCE(payload->>'ci_id', '')
			FROM review_item WHERE organization_id = $1 AND kind = 'unclassified_device' AND payload->>'serial_number' = $2`,
			f.OrgA, serial)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []item
		for rows.Next() {
			var it item
			if err := rows.Scan(&it.id, &it.reason, &it.typeName, &it.ciID); err != nil {
				t.Fatal(err)
			}
			out = append(out, it)
		}
		return out
	}

	var inactiveItem string
	for _, path := range []struct {
		name  string
		batch bool
	}{{"record path", false}, {"batch path", true}} {
		h := mux(path.batch)
		for _, form := range []struct{ name, ciType string }{
			{"name", "wp249-widget"},
			{"uuid", widget},
			{"upper-case uuid", strings.ToUpper(widget)},
		} {
			t.Run(path.name+"/new device, type as "+form.name, func(t *testing.T) {
				serial := fmt.Sprintf("WP249-NEW-%d", seq)
				// A second, unrelated device in the same batch keeps the
				// batch path busy.
				other := fmt.Sprintf("WP249-SRV-%d", seq)
				resp := ingestRecords(t, h, org, record(form.ciType, serial), record("server", other))
				if resp.Created != 2 || resp.ReviewItems != 1 {
					t.Errorf("response %+v, want 2 created and 1 review item", resp)
				}
				got := cisOf(serial)
				if len(got) != 1 || got[0].typeID != generic {
					t.Fatalf("CIs of the device: %+v, want one generic_device (%s)", got, generic)
				}
				if srv := cisOf(other); len(srv) != 1 || srv[0].typeID != server {
					t.Errorf("CIs of the server: %+v", srv)
				}
				items := itemsOf(serial)
				if len(items) != 1 || items[0].reason != discovery.ReasonTypeInactive || items[0].typeName != form.ciType ||
					items[0].ciID != got[0].id {
					t.Fatalf("items %+v, want one type_inactive item naming %s and CI %s", items, form.ciType, got[0].id)
				}
				inactiveItem = items[0].id
			})
		}

		t.Run(path.name+"/known CI of the deactivated type", func(t *testing.T) {
			serial := fmt.Sprintf("WP249-OLD-%d", seq)
			id := f.ID()
			if _, err := f.Admin.Exec(bg, `INSERT INTO ci (id, organization_id, ci_type_id, name, status, serial_number)
				VALUES ($1, $2, $3, 'wp249-old', 'active', $4)`, id, f.OrgA, widget, serial); err != nil {
				t.Fatal(err)
			}
			resp := ingestRecords(t, h, org, record("wp249-widget", serial))
			if resp.Updated != 1 || resp.Created != 0 || resp.ReviewItems != 0 {
				t.Errorf("response %+v, want 1 updated", resp)
			}
			if got := cisOf(serial); len(got) != 1 || got[0].id != id || got[0].typeID != widget {
				t.Errorf("CIs %+v, want %s of the deactivated type", got, id)
			}
			if items := itemsOf(serial); len(items) != 0 {
				t.Errorf("items %+v, want none", items)
			}
		})

		t.Run(path.name+"/generic_device is not raised", func(t *testing.T) {
			serial := fmt.Sprintf("WP249-GEN-%d", seq)
			ingestRecords(t, h, org, record("generic_device", serial))
			resp := ingestRecords(t, h, org, record("wp249-widget", serial))
			if resp.Updated != 1 || resp.Created != 0 {
				t.Errorf("response %+v, want 1 updated", resp)
			}
			if got := cisOf(serial); len(got) != 1 || got[0].typeID != generic {
				t.Errorf("CIs %+v, want the generic_device unchanged", got)
			}
			for _, it := range itemsOf(serial) {
				if it.reason == discovery.ReasonTypeInactive {
					t.Errorf("type_inactive item %+v for a known CI", it)
				}
			}
		})

		t.Run(path.name+"/unknown UUID", func(t *testing.T) {
			serial := fmt.Sprintf("WP249-UNK-%d", seq)
			resp := ingestRecords(t, h, org, record("c0de2600-ffff-4000-8000-00000000dead", serial))
			if resp.Created != 0 || resp.ReviewItems != 1 {
				t.Errorf("response %+v, want no CI and 1 review item", resp)
			}
			if got := cisOf(serial); len(got) != 0 {
				t.Errorf("CIs %+v, want none", got)
			}
			if items := itemsOf(serial); len(items) != 1 || items[0].reason != "unknown_ci_type" {
				t.Errorf("items %+v, want one unknown_ci_type item", items)
			}
		})
	}

	t.Run("resolution new of a type_inactive item", func(t *testing.T) {
		w := resolveAs(t, mux(true), org, inactiveItem, `{"action":"new"}`)
		if w.Code != http.StatusConflict {
			t.Errorf("new: %d %s, want 409", w.Code, w.Body.String())
		}
		if s := str(`SELECT status FROM review_item WHERE id = $1`, inactiveItem); s != "open" {
			t.Errorf("item after new: %s, want open", s)
		}
	})

	t.Run("adoption of a held device of a type deactivated by now", func(t *testing.T) {
		held := func(name, serial, age string) string {
			return str(`INSERT INTO review_item (organization_id, kind, status, payload, created_at) VALUES ($1, 'unlicensed_ci', 'open',
				jsonb_build_object('ci_type_id', $2::text, 'ci_type_name', $2::text, 'source', 'sweep', 'device',
					jsonb_build_object('name', $3::text, 'serial_number', $4::text)), now() - $5::interval)
				RETURNING id::text`, f.OrgA, widget, name, serial, age)
		}
		first, second := held("wp249-held-1", "WP249-HELD-1", "2 minutes"), held("wp249-held-2", "WP249-HELD-2", "1 minute")
		h := discovery.NewHandler(discovery.NewPGRepository(f.App), cis)
		adopted, err := h.AdoptUnlicensed(org, f.OrgA)
		if err != nil || adopted != 2 {
			t.Fatalf("AdoptUnlicensed: %d, %v, want 2 adopted", adopted, err)
		}
		for _, c := range []struct{ item, serial string }{{first, "WP249-HELD-1"}, {second, "WP249-HELD-2"}} {
			if s := str(`SELECT status FROM review_item WHERE id = $1`, c.item); s != "resolved" {
				t.Errorf("held item %s: %s, want resolved", c.item, s)
			}
			got := cisOf(c.serial)
			if len(got) != 1 || got[0].typeID != generic {
				t.Fatalf("CIs of %s: %+v, want one generic_device", c.serial, got)
			}
			if items := itemsOf(c.serial); len(items) != 1 || items[0].reason != discovery.ReasonTypeInactive ||
				items[0].ciID != got[0].id || items[0].typeName != widget {
				t.Errorf("items of %s: %+v, want one type_inactive item about %s", c.serial, items, got[0].id)
			}
		}
	})

	t.Run("a deactivated org type does not hide the system type of its key", func(t *testing.T) {
		str(`INSERT INTO ci_type (organization_id, key, name, is_active) VALUES ($1, 'server', 'wp249-server', false) RETURNING id::text`, f.OrgA)
		id, err := discovery.NewPGRepository(f.App).LookupCITypeID(org, f.OrgA, "server")
		if err != nil || id != server {
			t.Errorf("LookupCITypeID(server) = %q, %v, want the system type %s", id, err, server)
		}
		serial := fmt.Sprintf("WP249-SRVKEY-%d", seq)
		if resp := ingestRecords(t, mux(true), org, record("server", serial)); resp.Created != 1 || resp.ReviewItems != 0 {
			t.Errorf("response %+v, want 1 created", resp)
		}
		if got := cisOf(serial); len(got) != 1 || got[0].typeID != server {
			t.Errorf("CIs %+v, want one of the system type server", got)
		}
	})
}
