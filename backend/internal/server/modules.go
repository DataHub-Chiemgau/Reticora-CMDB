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
// RBA-05). A module that is off has no registered routes, its workers do
// not start and the UI hides its navigation (WP-214, E-03). The release
// default is G2 (WP-830): every module with phase P2 is on, the modules of
// P3, P4 and the add-ons [A] stay off (E-03, SEQ-01).
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
	// ModuleDocuments is the documents module (P2); without it the ticket
	// attachments are not served (SR-61, WP-874).
	ModuleDocuments = "documents"
)

// PhaseP2 is the phase value of RETICORA_MODULES that switches on every
// module with phase P2 (release G2, WP-706); since WP-830 it is also the
// default of an empty RETICORA_MODULES.
const PhaseP2 = "P2"

// PhaseP3 is the phase value of RETICORA_MODULES that switches on every
// module with phase P2 or P3 (release G3, WP-863); the empty value stays G2
// until WP-1166.
const PhaseP3 = "P3"

// ModuleMonitoring is the monitoring module of phase 3 (E-160); it replaces
// the module alerting of phase 1.
const ModuleMonitoring = "monitoring"

// moduleAliases maps former module keys to their successors; ParseModules
// accepts them, config.Load reports them as a configuration warning.
var moduleAliases = map[string]string{config.ModuleAliasAlerting: ModuleMonitoring}

// LateModules lists every module with a phase tag from P2 or [A], after
// API-06 and RBA-05. The P2 resources of E-38 without a module of their
// own follow E-105: modules_test.go keeps their register.
var LateModules = []Module{
	{Key: ModuleInventory, Phase: "P2", Prefixes: []string{
		"/api/v1/assets", "/api/v1/assignments", "/api/v1/reservations",
		"/api/v1/stock-movements", "/api/v1/compositions", "/api/v1/inventory",
		// Suppliers (WP-736) and contracts (WP-269) of MGT-12 (E-105).
		"/api/v1/suppliers", "/api/v1/contracts",
		// The code lookup and the labels (WP-762, WP-763; E-105).
		"/api/v1/labels",
	}, Nav: []string{"assets", "assignments", "inventory", "contracts", "suppliers"}},
	{Key: "documents", Phase: "P2", Prefixes: []string{"/api/v1/documents"}, Nav: []string{"documents"}},
	// Metamodel extensions of phase 2 (E-105): attribute sets (WP-250),
	// templates (WP-251), rules (WP-716), layouts (WP-259), drafts, their
	// test run and publication (WP-260, WP-721, WP-261) and the field
	// permissions (WP-726). No feature key (cmdb_core, E-44); the page
	// follows with WP-313. The rule catalog /rules (WP-801) is a page of
	// its own and hides with the module.
	{Key: ModuleMetamodel, Phase: "P2", Prefixes: []string{
		"/api/v1/attribute-sets", "/api/v1/ci-types/{id}/attribute-sets", "/api/v1/templates", "/api/v1/rules",
		"/api/v1/layouts", "/api/v1/metamodel", "/api/v1/field-permissions",
	}, Nav: []string{"metamodel", "rules"}},
	// Import of CSV/XLSX files (IMP-IO-01, E-105; feature key import).
	{Key: "import", Phase: "P2", Prefixes: []string{"/api/v1/imports", "/api/v1/import-profiles"}, Nav: []string{"import"}},
	{Key: "stocktake", Phase: "P2", Prefixes: []string{"/api/v1/stocktakes", "/api/v1/consumables", "/api/v1/orders"},
		Nav: []string{"stocktake", "consumables", "orders"}},
	// Tickets from review items (WP-291): the queue itself stays core.
	{Key: "ticketing", Phase: "P2", Prefixes: []string{"/api/v1/tickets", "/api/v1/slas", "/api/v1/business-calendars",
		"/api/v1/review-items/{id}/ticket"}, Nav: []string{"tickets", "slas"}},
	// Disposal is MGT-02 [P2] (E-38, E-44; formerly P4).
	{Key: "disposal", Phase: "P2", Prefixes: []string{"/api/v1/disposal-records"}, Nav: []string{"disposal"}},
	{Key: "maintenance", Phase: "P3", Prefixes: []string{"/api/v1/maintenance-windows"}, Nav: []string{"maintenance"}},
	{Key: "workflows", Phase: "P3", Prefixes: []string{
		"/api/v1/forms", "/api/v1/form-submissions", "/api/v1/workflows", "/api/v1/workflow-runs",
	}, Nav: []string{"forms", "workflows"}},
	{Key: "compliance", Phase: "P3", Prefixes: []string{"/api/v1/compliance"}, Nav: []string{"compliance"}},
	{Key: "findings", Phase: "P3", Prefixes: []string{"/api/v1/security"}, Nav: []string{"findings"}},
	// Monitoring of phase 3 (E-160, WP-863): probes, alarms, incidents,
	// SLA, forecasts and the collector groups (not below /monitoring), the
	// availability and configuration history of a CI and the credential
	// bindings and vault connections (CRD-01, CRD-04). The metrics of MON-02
	// (/api/v1/monitoring/metrics) stay part of G1. Former key: alerting.
	{Key: ModuleMonitoring, Phase: "P3", Prefixes: []string{
		"/api/v1/monitoring/alerts", "/api/v1/monitoring/targets", "/api/v1/monitoring/probes",
		"/api/v1/monitoring/incidents", "/api/v1/monitoring/sla", "/api/v1/monitoring/forecasts",
		"/api/v1/monitoring/probe-types", "/api/v1/monitoring/profiles", "/api/v1/monitoring/groups",
		"/api/v1/monitoring/events", "/api/v1/monitoring/alarms", "/api/v1/monitoring/alarm-settings",
		"/api/v1/monitoring/alarm-playbooks", "/api/v1/collector-groups",
		"/api/v1/cis/{id}/availability", "/api/v1/cis/{id}/configs",
		"/api/v1/credential-bindings", "/api/v1/vault-connections", "/api/v1/cis/{id}/credentials",
	}, Workers: []string{config.WorkerMonitoring, config.WorkerProbeAssign, config.WorkerProbeIngest}, Nav: []string{"monitoring"}},
	// IPAM/ASN in the style of phpIPAM (IPM-01 ff., E-160). The base objects
	// of NET-05 (/api/v1/vrfs, /vlans, /subnets, /ips) stay core.
	{Key: "ipam", Phase: "P3", Prefixes: []string{
		"/api/v1/ipam", "/api/v1/ipam-sections", "/api/v1/asns", "/api/v1/asn-ranges", "/api/v1/bgp-peerings",
		"/api/v1/nat-translations", "/api/v1/nameserver-sets", "/api/v1/vlan-domains",
		"/api/v1/cis/{id}/addresses", "/api/v1/subnets/{id}/scan", "/api/v1/subnets/{id}/rpki",
	}, Nav: []string{}},
	// Read-only ADMP connection (ADM-01 ff., add-on admp).
	{Key: "admp", Phase: "P3", Prefixes: []string{
		"/api/v1/admp", "/api/v1/cis/{id}/admp-connections", "/api/v1/network-interfaces/{id}/admp",
	}, Nav: []string{}},
	// Escalation chains of SMS and pager (NTF-02); the public provider
	// callback of WP-1146 belongs to the module as well.
	{Key: "escalation", Phase: "P3", Prefixes: []string{
		"/api/v1/escalation-policies", "/api/v1/notification-escalations", "/api/v1/notification-callbacks",
	}, Nav: []string{}},
	// Full export and deletion of an organization (TLC-05, TLC-06).
	{Key: "org_lifecycle", Phase: "P3", Prefixes: []string{"/api/v1/org/exports", "/api/v1/org/deletion"},
		Nav: []string{}},
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

// NoModules switches every late module off: the release G1 after E-03
// (WP-214). Since WP-830 it is a test profile only; no value of
// RETICORA_MODULES selects it.
func NoModules() *ModuleSet {
	return &ModuleSet{enabled: map[string]bool{}}
}

// ParseModules reads RETICORA_MODULES: empty is the release default G2
// (WP-830), every module with phase P2 on and the modules of P3, P4 and the
// add-ons off, the same as the phase value P2; "all" switches every late
// module on, otherwise a comma list of module keys and the phase values P2,
// which switches on every module with phase P2 (WP-706), and P3, which
// switches on every module with phase P2 or P3 (WP-863). Only the modules
// listed are on. The former key alerting stands for monitoring. Unknown keys
// fail, so a typo does not silently leave a module off.
func ParseModules(spec string) (*ModuleSet, error) {
	spec = strings.TrimSpace(spec)
	if strings.EqualFold(spec, "all") {
		return AllModules(), nil
	}
	if spec == "" {
		spec = PhaseP2
	}
	s := NoModules()
	for _, key := range strings.Split(spec, ",") {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		if key == PhaseP2 || key == PhaseP3 {
			for _, m := range LateModules {
				if m.Phase == PhaseP2 || (key == PhaseP3 && m.Phase == PhaseP3) {
					s.enabled[m.Key] = true
				}
			}
			continue
		}
		if successor, ok := moduleAliases[key]; ok {
			key = successor
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
