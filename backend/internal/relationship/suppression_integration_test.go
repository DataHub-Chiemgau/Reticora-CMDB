package relationship_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// TestSuppression covers WP-160 (REL-03, REL-04, REL-05) against
// PostgreSQL: deleting a discovery edge needs force and records a
// suppression with its type; discovery then does not recreate that edge
// but still creates another type between the same CIs; discovery neither
// changes nor removes a manual edge.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSuppression(t *testing.T) {
	f := scopetest.Seed(t, "ac")
	bg := context.Background()
	mac := map[string]string{"sw": "00:1b:21:16:00:01", "peer": "00:1b:21:16:00:02", "pdu": "00:1b:21:16:00:03", "manual": "00:1b:21:16:00:04"}
	ids := map[string]string{}
	for name, m := range mac {
		id := f.CI(t, f.OrgA, "", "wp160-"+name)
		if _, err := f.Admin.Exec(bg, `UPDATE ci SET primary_mac = $2 WHERE id = $1`, id, m); err != nil {
			t.Fatal(err)
		}
		ids[name] = id
	}
	rels := relationship.NewPGRepository(f.App)
	mux := chi.NewRouter()
	discovery.NewHandler(discovery.NewPGRepository(f.App), ci.NewPGRepository(f.App), rels).RegisterRoutes(mux)
	relationship.NewHandler(rels).RegisterRoutes(mux)
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	call := func(method, path string, body []byte) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewReader(body)).WithContext(ctx))
		return w
	}

	// A manual edge sw → manual exists before discovery sees that neighbor.
	manual := &relationship.Relationship{OrganizationID: f.OrgA, SourceCIID: ids["sw"], TargetCIID: ids["manual"], RelType: "connected_to",
		Source: "manual", VerificationState: "verified", Attributes: map[string]any{"note": "patched by hand"}}
	if err := rels.Create(ctx, manual); err != nil {
		t.Fatal(err)
	}

	ingest := func() {
		t.Helper()
		rec := plugins.RecordFromResult("snmp", &plugins.Result{CIType: "switch", Name: "wp160-sw", IP: "192.0.2.160", MAC: mac["sw"]}, time.Now())
		rec.Neighbors = []plugins.DeviceNeighbor{
			{Protocol: "lldp", LocalInterface: "Gi1/0/1", RemoteMAC: mac["peer"]},
			{Protocol: "lldp", LocalInterface: "Gi1/0/2", RemoteMAC: mac["manual"]},
		}
		body, err := json.Marshal(plugins.DeviceRecordBatch{Records: []plugins.DeviceRecord{rec}})
		if err != nil {
			t.Fatal(err)
		}
		if w := call(http.MethodPost, "/api/v1/discovery/ingest", body); w.Code != http.StatusAccepted {
			t.Fatalf("ingest: %d %s", w.Code, w.Body.String())
		}
	}
	edgeOf := func(src, dst, relType string) (id, source string, confidence *float64, lastSeen *time.Time) {
		t.Helper()
		err := f.Admin.QueryRow(bg, `SELECT id::text, source, confidence::float8, last_seen_at FROM ci_relationship
			WHERE source_ci_id = $1 AND target_ci_id = $2 AND rel_type = $3`, src, dst, relType).Scan(&id, &source, &confidence, &lastSeen)
		if err != nil {
			return "", "", nil, nil
		}
		return id, source, confidence, lastSeen
	}

	ingest()
	discovered, source, _, _ := edgeOf(ids["sw"], ids["peer"], "connected_to")
	if discovered == "" || source != "discovery" {
		t.Fatalf("discovered edge %q (%s)", discovered, source)
	}
	// REL-05: the manual edge is untouched by discovery.
	manualID, manualSource, manualConf, manualSeen := edgeOf(ids["sw"], ids["manual"], "connected_to")
	if manualID != manual.ID || manualSource != "manual" || manualConf != nil || manualSeen != nil {
		t.Errorf("manual edge after discovery: id %s source %s confidence %v last_seen %v", manualID, manualSource, manualConf, manualSeen)
	}

	// REL-04: a discovery edge needs force; then it is suppressed (REL-03).
	if w := call(http.MethodDelete, "/api/v1/relationships/"+discovered, nil); w.Code != http.StatusConflict {
		t.Fatalf("delete without force: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodDelete, "/api/v1/relationships/"+discovered+"?force=true", nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete with force: %d %s", w.Code, w.Body.String())
	}
	var suppressedType string
	if err := f.Admin.QueryRow(bg, `SELECT rt.key FROM relationship_suppression s JOIN relationship_type rt ON rt.id = s.relationship_type_id
		WHERE s.source_ci_id = $1 AND s.target_ci_id = $2`, ids["sw"], ids["peer"]).Scan(&suppressedType); err != nil || suppressedType != "connected_to" {
		t.Fatalf("suppression = %q, %v", suppressedType, err)
	}
	ingest()
	if id, _, _, _ := edgeOf(ids["sw"], ids["peer"], "connected_to"); id != "" {
		t.Error("discovery recreated a suppressed edge")
	}
	// The suppression is per type: powered_by between the same CIs is new.
	body, _ := json.Marshal(map[string]any{"collector_id": "c", "items": []map[string]any{{"ci_type_name": "switch", "name": "wp160-sw",
		"primary_mac": mac["sw"], "management_ip": "192.0.2.160",
		"relationships": []map[string]any{{"target_mac": mac["peer"], "type_key": "powered_by"}}}}})
	if w := call(http.MethodPost, "/api/v1/ingest/bulk", body); w.Code != http.StatusAccepted {
		t.Fatalf("legacy ingest: %d %s", w.Code, w.Body.String())
	}
	if id, _, _, _ := edgeOf(ids["sw"], ids["peer"], "powered_by"); id == "" {
		t.Error("a suppression of connected_to blocked powered_by")
	}

	// A manual edge is deleted without force and leaves no suppression.
	if w := call(http.MethodDelete, "/api/v1/relationships/"+manual.ID, nil); w.Code != http.StatusNoContent {
		t.Fatalf("delete manual: %d", w.Code)
	}
	var n int
	if err := f.Admin.QueryRow(bg, `SELECT count(*) FROM relationship_suppression WHERE source_ci_id = $1 AND target_ci_id = $2`, ids["sw"], ids["manual"]).Scan(&n); err != nil || n != 0 {
		t.Errorf("manual deletion suppressed: %d %v", n, err)
	}
}
