package discovery

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/profiles"
)

// TestProfileSystemTemplatesMapToCatalog covers WP-581 (DIS-02, retest
// F-263): every systemTemplate value of the shipped collector profiles maps
// to a template key of the type catalog; a new profile value without a
// mapping turns this test red.
func TestProfileSystemTemplatesMapToCatalog(t *testing.T) {
	catalog := map[string]bool{}
	for _, tpl := range citype.Templates() {
		catalog[tpl.TemplateKey] = true
	}
	files, err := filepath.Glob("../../../collector/profiles/data/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("profile data: %v (%d files)", err, len(files))
	}
	values := 0
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var list []struct {
			Name           string `yaml:"name"`
			SystemTemplate string `yaml:"systemTemplate"`
		}
		if err := yaml.Unmarshal(raw, &list); err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		for _, p := range list {
			if p.SystemTemplate == "" {
				continue
			}
			values++
			key, ok := SystemTemplateKey(p.SystemTemplate)
			if !ok || !catalog[key] {
				t.Errorf("%s profile %s: systemTemplate %q maps to %q, no template of the catalog", filepath.Base(file), p.Name, p.SystemTemplate, key)
			}
		}
	}
	// The shipped profiles name a template (TestEmbeddedProfileData); a
	// glob that found none would prove nothing.
	if values < 20 {
		t.Errorf("only %d systemTemplate values found in %d files", values, len(files))
	}
	for name, key := range profileTemplates {
		if !catalog[key] {
			t.Errorf("mapping %q -> %q: no template of the catalog", name, key)
		}
	}
}

// TestIngestStoresCatalogSystemTemplate covers WP-581 (DIS-02, F-263): the
// ingest stores the catalog key of a classified record's system template
// as attribute system_template of the CI, also when the record goes through
// the ingest again; an unknown name is logged as
// discovery.profile.unknown_template and stored as no attribute.
func TestIngestStoresCatalogSystemTemplate(t *testing.T) {
	discoveryRepo := NewMemoryRepository()
	ciRepo := ci.NewMemoryRepository()
	for _, key := range []string{"server", "switch"} {
		discoveryRepo.SeedCIType(key, key)
		ciRepo.SetType(key, key)
	}
	h := NewHandler(discoveryRepo, ciRepo)
	ctx := discoveryTenantCtx(httptest.NewRequest(http.MethodGet, "/", http.NoBody)).Context()

	logs := &logRecorder{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	// A Dell iDRAC classified by the shipped profile (server-bmc).
	rec := plugins.RecordFromResult("redfish", &plugins.Result{
		CIType: "bmc", Name: "r750", IP: "10.0.0.20", MAC: "aa:bb:cc:00:05:81", Serial: "SN-WP581-1",
		Manufacturer: "Dell Inc.", Model: "PowerEdge R750",
	}, time.Now())
	profiles.Default().ClassifyRecord(&rec)
	if rec.Attributes["system_template"] != "server-bmc" {
		t.Fatalf("profile template = %v, want server-bmc", rec.Attributes["system_template"])
	}
	// A record whose profile (RETICORA_PROFILES_DIR) names no known template.
	unknown := plugins.RecordFromResult("snmp", &plugins.Result{
		CIType: "switch", Name: "sw-wp581", IP: "10.0.0.21", MAC: "aa:bb:cc:00:05:82", Serial: "SN-WP581-2",
		Attributes: map[string]any{"system_template": "campus-switch-x"},
	}, time.Now())

	items := []IngestItem{itemFromRecord(&rec), itemFromRecord(&unknown)}
	for round := range 2 {
		if _, err := h.processItems(ctx, "org-1", items); err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		list, _, err := ciRepo.List(ctx, "org-1", ci.FilterParams{}, api.PaginationParams{Limit: 10})
		if err != nil || len(list) != 2 {
			t.Fatalf("round %d: CIs = %+v, %v", round, list, err)
		}
		for _, item := range list {
			got, has := item.Attributes["system_template"]
			switch item.Name {
			case "r750":
				if got != "physical_server" {
					t.Errorf("round %d: r750 system_template = %v, want physical_server", round, got)
				}
			case "sw-wp581":
				if has {
					t.Errorf("round %d: unknown template stored: %v", round, got)
				}
			default:
				t.Errorf("round %d: unexpected CI %+v", round, item)
			}
		}
	}
	warned := logs.events(t, "discovery.profile.unknown_template")
	if len(warned) != 1 || warned[0]["level"] != "WARN" || warned[0]["system_template"] != "campus-switch-x" ||
		warned[0]["organization_id"] != "org-1" {
		t.Errorf("discovery.profile.unknown_template log = %+v, want one warning for campus-switch-x", warned)
	}
}
