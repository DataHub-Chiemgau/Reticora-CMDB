package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/go-chi/chi/v5"
)

// Module is a product module whose phase tag is later than G1 (API-06,
// RBA-05). After E-03 such modules are switched off in the G1 release: their
// routes are not registered, their workers do not start and the UI hides
// their navigation (WP-214).
type Module struct {
	Key string `json:"key"`
	// Phase is the phase tag of the module: P2, P3, P4 or A (add-on).
	Phase string `json:"phase"`
	// Prefixes are the route prefixes the module serves. A segment in
	// braces ({id}) matches any one path segment; a route belongs to the
	// module with the longest matching prefix (WP-706, E-105).
	Prefixes []string `json:"-"`
	// Workers are the background workers of the module.
	Workers []string `json:"-"`
	// Nav are the navigation pages of the UI that belong to the module;
	// empty, never nil, for a module without pages of its own (gps), so GET
	// /api/v1/modules answers nav: [].
	Nav []string `json:"nav"`
}

// Module keys that code outside the route table asks for.
const (
	// ModuleInventory is the inventory module (assets, reservations,
	// stock; P2); its reservation sweeper runs only while it is on.
	ModuleInventory = "inventory"
	// ModuleMetamodel is the metamodel module of phase 2 (attribute sets,
	// templates, rules, layouts, drafts; E-105). It has no feature key.
	ModuleMetamodel = "metamodel"
)

// PhaseP2 is the phase value of RETICORA_MODULES that switches on every
// module with phase P2 (release G2, WP-706).
const PhaseP2 = "P2"

// LateModules lists every module with a phase tag from P2 or [A], after
// API-06 and RBA-05. The P2 resources of E-38 without a module of their
// own follow E-105: modules_test.go keeps their register.
var LateModules = []Module{
	{Key: ModuleInventory, Phase: "P2", Prefixes: []string{
		"/api/v1/assets", "/api/v1/assignments", "/api/v1/reservations",
		"/api/v1/stock-movements", "/api/v1/compositions", "/api/v1/inventory",
	}, Nav: []string{"assets", "assignments", "inventory"}},
	{Key: "documents", Phase: "P2", Prefixes: []string{"/api/v1/documents"}, Nav: []string{"documents"}},
	// Metamodel extensions of phase 2 (E-105): attribute sets (WP-250),
	// templates (WP-251), rules (WP-716), layouts (WP-259), drafts, their
	// test run and publication (WP-260, WP-721, WP-261); field permissions
	// add their prefix with their first route. No
	// feature key (cmdb_core, E-44); the page follows with WP-313.
	{Key: ModuleMetamodel, Phase: "P2", Prefixes: []string{
		"/api/v1/attribute-sets", "/api/v1/ci-types/{id}/attribute-sets", "/api/v1/templates", "/api/v1/rules",
		"/api/v1/layouts", "/api/v1/metamodel",
	}, Nav: []string{"metamodel"}},
	// Import of CSV/XLSX files (IMP-IO-01, E-105; feature key import).
	{Key: "import", Phase: "P2", Prefixes: []string{"/api/v1/imports", "/api/v1/import-profiles"}, Nav: []string{"import"}},
	{Key: "stocktake", Phase: "P2", Prefixes: []string{"/api/v1/stocktakes", "/api/v1/consumables", "/api/v1/orders"},
		Nav: []string{"stocktake", "consumables", "orders"}},
	{Key: "ticketing", Phase: "P2", Prefixes: []string{"/api/v1/tickets", "/api/v1/slas", "/api/v1/business-calendars"},
		Nav: []string{"tickets", "slas"}},
	// Disposal is MGT-02 [P2] (E-38, E-44; formerly P4).
	{Key: "disposal", Phase: "P2", Prefixes: []string{"/api/v1/disposal-records"}, Nav: []string{"disposal"}},
	{Key: "maintenance", Phase: "P3", Prefixes: []string{"/api/v1/maintenance-windows"}, Nav: []string{"maintenance"}},
	{Key: "workflows", Phase: "P3", Prefixes: []string{
		"/api/v1/forms", "/api/v1/form-submissions", "/api/v1/workflows", "/api/v1/workflow-runs",
	}, Nav: []string{"forms", "workflows"}},
	{Key: "compliance", Phase: "P3", Prefixes: []string{"/api/v1/compliance"}, Nav: []string{"compliance"}},
	{Key: "findings", Phase: "P3", Prefixes: []string{"/api/v1/security"}, Nav: []string{"findings"}},
	// Alerting rules (MON-04); the metrics of MON-02 stay part of G1.
	{Key: "alerting", Phase: "P3", Prefixes: []string{"/api/v1/monitoring/alerts"},
		Workers: []string{config.WorkerMonitoring}, Nav: []string{"monitoring"}},
	{Key: "keys", Phase: "P4", Prefixes: []string{"/api/v1/keys"}, Nav: []string{"keys"}},
	{Key: "trainings", Phase: "P4", Prefixes: []string{"/api/v1/trainings", "/api/v1/training-assignments"},
		Nav: []string{"trainings"}},
	{Key: "desks", Phase: "P4", Prefixes: []string{"/api/v1/desks", "/api/v1/desk-bookings"}, Nav: []string{"desks"}},
	// The GPS history of assets (LOC-09 [P4], E-44) has no page of its own.
	{Key: "gps", Phase: "P4", Prefixes: []string{"/api/v1/asset-locations", "/api/v1/assets/{id}/locations"},
		Nav: []string{}},
	{Key: "agents", Phase: "P4", Prefixes: []string{"/api/v1/agents"}, Nav: []string{"agents"}},
	{Key: "iga", Phase: "A", Prefixes: []string{"/api/v1/iga", "/scim"}, Nav: []string{"iga"}},
	{Key: "ai", Phase: "A", Prefixes: []string{"/api/v1/ai"}, Nav: []string{"assistant"}},
}

// ModuleSet is the set of enabled late modules.
type ModuleSet struct {
	enabled map[string]bool
}

// AllModules enables every module (development and tests).
func AllModules() *ModuleSet {
	s := &ModuleSet{enabled: map[string]bool{}}
	for _, m := range LateModules {
		s.enabled[m.Key] = true
	}
	return s
}

// ParseModules reads RETICORA_MODULES: empty switches every late module off
// (the G1 default after E-03), "all" switches them all on, otherwise a comma
// list of module keys and the phase value P2, which switches on every module
// with phase P2 (release G2, WP-706). Unknown keys fail, so a typo does not
// silently leave a module off.
func ParseModules(spec string) (*ModuleSet, error) {
	spec = strings.TrimSpace(spec)
	if strings.EqualFold(spec, "all") {
		return AllModules(), nil
	}
	s := &ModuleSet{enabled: map[string]bool{}}
	for _, key := range strings.Split(spec, ",") {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if key == PhaseP2 {
			for _, m := range LateModules {
				if m.Phase == PhaseP2 {
					s.enabled[m.Key] = true
				}
			}
			continue
		}
		if _, ok := moduleByKey(key); !ok {
			return nil, fmt.Errorf("RETICORA_MODULES: unknown module %q", key)
		}
		s.enabled[key] = true
	}
	return s, nil
}

func moduleByKey(key string) (Module, bool) {
	for _, m := range LateModules {
		if m.Key == key {
			return m, true
		}
	}
	return Module{}, false
}

// Enabled reports whether a late module is on; a nil set enables all.
func (s *ModuleSet) Enabled(key string) bool {
	if s == nil {
		return true
	}
	return s.enabled[key]
}

// ModuleForRoute returns the late module serving a route pattern: the one
// with the longest matching prefix (in segments), so
// /api/v1/assets/{id}/locations belongs to gps, not inventory (WP-706,
// E-105). A prefix matches whole segments only.
func ModuleForRoute(pattern string) (Module, bool) {
	segments := strings.Split(pattern, "/")
	var (
		best    Module
		bestLen = -1
	)
	for _, m := range LateModules {
		for _, prefix := range m.Prefixes {
			if n := prefixSegments(prefix, segments); n > bestLen {
				best, bestLen = m, n
			}
		}
	}
	return best, bestLen >= 0
}

// prefixSegments returns the number of segments of prefix when it matches
// the leading segments of a route, else -1. A prefix segment in braces
// matches any non-empty segment.
func prefixSegments(prefix string, route []string) int {
	parts := strings.Split(prefix, "/")
	if len(parts) > len(route) {
		return -1
	}
	for i, part := range parts {
		if part == route[i] {
			continue
		}
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") && route[i] != "" {
			continue
		}
		return -1
	}
	return len(parts)
}

// RouteAllowed reports whether a route pattern is registered.
func (s *ModuleSet) RouteAllowed(pattern string) bool {
	m, ok := ModuleForRoute(pattern)
	return !ok || s.Enabled(m.Key)
}

// WorkerAllowed reports whether a worker may start.
func (s *ModuleSet) WorkerAllowed(name string) bool {
	for _, m := range LateModules {
		for _, w := range m.Workers {
			if w == name && !s.Enabled(m.Key) {
				return false
			}
		}
	}
	return true
}

// EnabledKeys lists the enabled late modules, sorted.
func (s *ModuleSet) EnabledKeys() []string {
	var keys []string
	for _, m := range LateModules {
		if s.Enabled(m.Key) {
			keys = append(keys, m.Key)
		}
	}
	sort.Strings(keys)
	return keys
}

type moduleStatus struct {
	Module
	Enabled bool `json:"enabled"`
}

// modulesHandler serves GET /api/v1/modules: the late modules and whether
// this release serves them, so the UI hides the navigation of switched-off
// modules.
type modulesHandler struct {
	set *ModuleSet
}

func (h modulesHandler) RegisterRoutes(r chi.Router) {
	r.Get("/api/v1/modules", h.list)
}

func (h modulesHandler) list(w http.ResponseWriter, _ *http.Request) {
	out := make([]moduleStatus, 0, len(LateModules))
	for _, m := range LateModules {
		out = append(out, moduleStatus{Module: m, Enabled: h.set.Enabled(m.Key)})
	}
	api.WriteJSON(w, http.StatusOK, map[string]any{"data": out})
}
