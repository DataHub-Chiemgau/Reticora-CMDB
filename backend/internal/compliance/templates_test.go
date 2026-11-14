package compliance

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rules"
)

// The shipped rule templates (SEC-03, WP-1130): MET-45 conditions over the
// inputs of a compliance rule, controls of the catalog of WP-1128 and texts
// in de and en.

func TestEveryTemplateParsesAsMET45(t *testing.T) {
	if len(Templates) != 5 {
		t.Errorf("%d templates, want the 5 of the confirmed list", len(Templates))
	}
	seen := map[string]bool{}
	for i := range Templates {
		tpl := &Templates[i]
		if tpl.Key == "" || seen[tpl.Key] || tpl.Version < 1 {
			t.Errorf("template %q: key empty, duplicate or version %d", tpl.Key, tpl.Version)
		}
		seen[tpl.Key] = true
		if !slices.Contains(Severities, tpl.Severity) || tpl.Category == "" {
			t.Errorf("%s: severity %q, category %q", tpl.Key, tpl.Severity, tpl.Category)
		}
		for name, cond := range map[string]JSONMap{"expression": tpl.Expression, "applies_when": tpl.AppliesWhen} {
			if cond == nil {
				if name == "expression" {
					t.Errorf("%s: no expression", tpl.Key)
				}
				continue
			}
			parsed, err := parseRuleCondition(cond)
			if err != nil {
				t.Errorf("%s.%s: %v", tpl.Key, name, err)
				continue
			}
			if _, err := rules.ParseCondition(mustJSON(t, cond)); err != nil {
				t.Errorf("%s.%s: rules.ParseCondition: %v", tpl.Key, name, err)
			}
			if at, msg := unknownInput(&parsed, name); at != "" {
				t.Errorf("%s: %s %s", tpl.Key, at, msg)
			}
			if err := ValidateCondition(name, cond); err != nil {
				t.Errorf("%s.%s: %v", tpl.Key, name, err)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// controlSeed matches a control of the catalog migration of WP-1128.
var controlSeed = regexp.MustCompile(`\('(iso27001|nis2|kritis)', '([0-9a-z.]+)', '`)

func TestEveryTemplateHasControlsAndTranslations(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/000355_compliance_controls.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	catalog := map[TemplateControl]bool{}
	for _, m := range controlSeed.FindAllStringSubmatch(string(raw), -1) {
		catalog[TemplateControl{Standard: m[1], ControlID: m[2]}] = true
	}
	if len(catalog) < 100 {
		t.Fatalf("read %d controls of the catalog", len(catalog))
	}
	for i := range Templates {
		tpl := &Templates[i]
		if len(tpl.Controls) == 0 {
			t.Errorf("%s: no control", tpl.Key)
		}
		for _, c := range tpl.Controls {
			if !catalog[c] {
				t.Errorf("%s: control %s %s is not in the catalog", tpl.Key, c.Standard, c.ControlID)
			}
		}
		for _, lang := range templateLanguages {
			if tpl.Name[lang] == "" || tpl.Description[lang] == "" || tpl.Remediation[lang] == "" {
				t.Errorf("%s: text missing in %s", tpl.Key, lang)
			}
		}
	}
}

func TestRequestLanguage(t *testing.T) {
	for header, want := range map[string]string{
		"": "de", "en-US,en;q=0.9": "en", "fr-FR, en;q=0.5, de;q=0.8": "de", "fr": "de", "de-AT": "de", "en;q=0": "de",
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.Header.Set("Accept-Language", header)
		if got := RequestLanguage(r); got != want {
			t.Errorf("Accept-Language %q: %s, want %s", header, got, want)
		}
	}
}
