package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api/speccheck"
)

// The REST contract of monitoring P3 (WP-938, PRB-01, PRB-03, E-160): the
// operations and schemas for probes, effective values, profiles, monitoring
// groups, probe types, collector groups, availability and monitoring
// events, marked x-status: planned until their implementing work package
// (WP-939, WP-946, WP-947, WP-973, WP-975, WP-1079) registers the route.

// monitoringContractOperations are the operations of WP-938 AK1 and AK3.
var monitoringContractOperations = []speccheck.Operation{
	{Method: "GET", Path: "/api/v1/monitoring/probes"},
	{Method: "POST", Path: "/api/v1/monitoring/probes"},
	{Method: "GET", Path: "/api/v1/monitoring/probes/{id}"},
	{Method: "PATCH", Path: "/api/v1/monitoring/probes/{id}"},
	{Method: "DELETE", Path: "/api/v1/monitoring/probes/{id}"},
	{Method: "GET", Path: "/api/v1/monitoring/probes/{id}/effective"},
	{Method: "GET", Path: "/api/v1/monitoring/probes/{id}/state"},
	{Method: "GET", Path: "/api/v1/monitoring/probes/{id}/results"},
	{Method: "GET", Path: "/api/v1/monitoring/probes/{id}/state-changes"},
	{Method: "GET", Path: "/api/v1/monitoring/probes/{id}/availability"},
	{Method: "GET", Path: "/api/v1/cis/{id}/availability"},
	{Method: "GET", Path: "/api/v1/monitoring/probe-types"},
	{Method: "GET", Path: "/api/v1/monitoring/profiles"},
	{Method: "POST", Path: "/api/v1/monitoring/profiles"},
	{Method: "GET", Path: "/api/v1/monitoring/profiles/{id}"},
	{Method: "PATCH", Path: "/api/v1/monitoring/profiles/{id}"},
	{Method: "DELETE", Path: "/api/v1/monitoring/profiles/{id}"},
	{Method: "GET", Path: "/api/v1/monitoring/groups"},
	{Method: "POST", Path: "/api/v1/monitoring/groups"},
	{Method: "GET", Path: "/api/v1/monitoring/groups/{id}"},
	{Method: "PATCH", Path: "/api/v1/monitoring/groups/{id}"},
	{Method: "DELETE", Path: "/api/v1/monitoring/groups/{id}"},
	{Method: "GET", Path: "/api/v1/monitoring/groups/{id}/members"},
	{Method: "POST", Path: "/api/v1/monitoring/groups/{id}/members"},
	{Method: "GET", Path: "/api/v1/monitoring/events"},
	{Method: "GET", Path: "/api/v1/collector-groups"},
	{Method: "POST", Path: "/api/v1/collector-groups"},
	{Method: "GET", Path: "/api/v1/collector-groups/{id}"},
	{Method: "PATCH", Path: "/api/v1/collector-groups/{id}"},
	{Method: "DELETE", Path: "/api/v1/collector-groups/{id}"},
	{Method: "POST", Path: "/api/v1/collector-groups/{id}/members"},
}

// monitoringImplemented names the operations of the contract whose
// implementing work package registered the route: they lose x-status:
// planned (TestRoutesAndSpecificationAreInParity checks the route).
var monitoringImplemented = map[speccheck.Operation]string{
	{Method: "GET", Path: "/api/v1/monitoring/probes"}:                "WP-939",
	{Method: "POST", Path: "/api/v1/monitoring/probes"}:               "WP-939",
	{Method: "GET", Path: "/api/v1/monitoring/probes/{id}"}:           "WP-939",
	{Method: "PATCH", Path: "/api/v1/monitoring/probes/{id}"}:         "WP-939",
	{Method: "DELETE", Path: "/api/v1/monitoring/probes/{id}"}:        "WP-939",
	{Method: "GET", Path: "/api/v1/monitoring/probe-types"}:           "WP-939",
	{Method: "GET", Path: "/api/v1/monitoring/probes/{id}/effective"}: "WP-946",
	{Method: "GET", Path: "/api/v1/monitoring/profiles"}:              "WP-946",
	{Method: "POST", Path: "/api/v1/monitoring/profiles"}:             "WP-946",
	{Method: "GET", Path: "/api/v1/monitoring/profiles/{id}"}:         "WP-946",
	{Method: "PATCH", Path: "/api/v1/monitoring/profiles/{id}"}:       "WP-946",
	{Method: "DELETE", Path: "/api/v1/monitoring/profiles/{id}"}:      "WP-946",
	{Method: "GET", Path: "/api/v1/monitoring/groups"}:                "WP-946",
	{Method: "POST", Path: "/api/v1/monitoring/groups"}:               "WP-946",
	{Method: "GET", Path: "/api/v1/monitoring/groups/{id}"}:           "WP-946",
	{Method: "PATCH", Path: "/api/v1/monitoring/groups/{id}"}:         "WP-946",
	{Method: "DELETE", Path: "/api/v1/monitoring/groups/{id}"}:        "WP-946",
	{Method: "GET", Path: "/api/v1/monitoring/groups/{id}/members"}:   "WP-946",
	{Method: "POST", Path: "/api/v1/monitoring/groups/{id}/members"}:  "WP-946",
	{Method: "GET", Path: "/api/v1/collector-groups"}:                 "WP-947",
	{Method: "POST", Path: "/api/v1/collector-groups"}:                "WP-947",
	{Method: "GET", Path: "/api/v1/collector-groups/{id}"}:            "WP-947",
	{Method: "PATCH", Path: "/api/v1/collector-groups/{id}"}:          "WP-947",
	{Method: "DELETE", Path: "/api/v1/collector-groups/{id}"}:         "WP-947",
	{Method: "POST", Path: "/api/v1/collector-groups/{id}/members"}:   "WP-947",
}

// contractSchema is the part of a schema the contract test reads.
type contractSchema struct {
	Ref                  string                     `yaml:"$ref"`
	Type                 any                        `yaml:"type"`
	Required             []string                   `yaml:"required"`
	Properties           map[string]*contractSchema `yaml:"properties"`
	Enum                 []string                   `yaml:"enum"`
	Items                *contractSchema            `yaml:"items"`
	AllOf                []*contractSchema          `yaml:"allOf"`
	AdditionalProperties yaml.Node                  `yaml:"additionalProperties"`
	MaxItems             *int                       `yaml:"maxItems"`
}

type contractOperation struct {
	Parameters []struct {
		Ref    string          `yaml:"$ref"`
		Name   string          `yaml:"name"`
		In     string          `yaml:"in"`
		Schema *contractSchema `yaml:"schema"`
	} `yaml:"parameters"`
	RequestBody *struct {
		Content map[string]struct {
			Schema *contractSchema `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"requestBody"`
	Responses map[string]struct {
		Ref     string `yaml:"$ref"`
		Content map[string]struct {
			Schema *contractSchema `yaml:"schema"`
		} `yaml:"content"`
	} `yaml:"responses"`
}

type monitoringSpec struct {
	Paths      map[string]map[string]yaml.Node `yaml:"paths"`
	Components struct {
		Schemas map[string]*contractSchema `yaml:"schemas"`
	} `yaml:"components"`
}

func loadMonitoringSpec(t *testing.T) *monitoringSpec {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(specPath))
	if err != nil {
		t.Fatal(err)
	}
	var doc monitoringSpec
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	return &doc
}

func (d *monitoringSpec) operation(t *testing.T, op speccheck.Operation) *contractOperation {
	t.Helper()
	node, ok := d.Paths[op.Path][strings.ToLower(op.Method)]
	if !ok {
		t.Fatalf("%s is not documented", op)
	}
	var out contractOperation
	if err := node.Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", op, err)
	}
	return &out
}

// schema returns the component schema name refers to ("#/components/schemas/X" or "X").
func (d *monitoringSpec) schema(t *testing.T, name string) *contractSchema {
	t.Helper()
	name = strings.TrimPrefix(name, "#/components/schemas/")
	s, ok := d.Components.Schemas[name]
	if !ok {
		t.Fatalf("schema %s is not defined", name)
	}
	return s
}

// properties collects the properties of s and of its allOf parts.
func (d *monitoringSpec) properties(t *testing.T, s *contractSchema) map[string]*contractSchema {
	t.Helper()
	out := map[string]*contractSchema{}
	if s.Ref != "" {
		return d.properties(t, d.schema(t, s.Ref))
	}
	for k, v := range s.Properties {
		out[k] = v
	}
	for _, part := range s.AllOf {
		for k, v := range d.properties(t, part) {
			out[k] = v
		}
	}
	return out
}

func requireProperties(t *testing.T, name string, props map[string]*contractSchema, want ...string) {
	t.Helper()
	for _, p := range want {
		if _, ok := props[p]; !ok {
			t.Errorf("%s: property %s missing", name, p)
		}
	}
}

// TestMonitoringContractOperations covers WP-938 AK1, AK2 and AK4: every
// operation of the monitoring contract is documented with x-status:
// planned, x-phase P3 and x-feature monitoring, with request and response
// schemas, 422 and the entitlement limit on the probe writes; collector
// groups lie only under /api/v1/collector-groups (E-160); EffectiveProbe
// names origin and clamped per field; ProbeType has every field of the
// register entry (WP-934).
func TestMonitoringContractOperations(t *testing.T) {
	spec := filepath.Clean(specPath)
	planned, err := speccheck.Planned(spec)
	if err != nil {
		t.Fatal(err)
	}
	phases, err := speccheck.Phases(spec)
	if err != nil {
		t.Fatal(err)
	}
	features, err := speccheck.Features(spec)
	if err != nil {
		t.Fatal(err)
	}
	documented, err := speccheck.Load(spec)
	if err != nil {
		t.Fatal(err)
	}
	doc := loadMonitoringSpec(t)

	for _, op := range monitoringContractOperations {
		if wp, done := monitoringImplemented[op]; done && planned[op] {
			t.Errorf("%s: implemented by %s but still marked x-status: planned", op, wp)
		} else if !done && !planned[op] {
			t.Errorf("%s: not marked x-status: planned", op)
		}
		if phases[op.Path] != PhaseP3 {
			t.Errorf("%s: x-phase %q, want P3", op, phases[op.Path])
		}
		if features[op] != "monitoring" {
			t.Errorf("%s: x-feature %q, want monitoring", op, features[op])
		}
		o := doc.operation(t, op)
		success := false
		for code, r := range o.Responses {
			if !strings.HasPrefix(code, "2") || code == "204" {
				continue
			}
			media, ok := r.Content["application/json"]
			if !ok || media.Schema == nil || media.Schema.Ref == "" {
				t.Errorf("%s %s: no schema reference", op, code)
				continue
			}
			doc.schema(t, media.Schema.Ref)
			success = true
		}
		if !success && op.Method != "DELETE" {
			t.Errorf("%s: no 2xx response with a schema", op)
		}
		if op.Method == "POST" || op.Method == "PATCH" {
			if o.RequestBody == nil || o.RequestBody.Content["application/json"].Schema == nil {
				t.Errorf("%s: no request body schema", op)
			}
			if _, ok := o.Responses["422"]; !ok {
				t.Errorf("%s: no 422 response", op)
			}
		}
	}

	// Probe, profile and group writes answer 422 with the field list and 403
	// entitlement-limit (quota and probe budget); a group's priority or name
	// decides which overrides win and so moves the budget of its members.
	for _, op := range []speccheck.Operation{
		{Method: "POST", Path: "/api/v1/monitoring/probes"}, {Method: "PATCH", Path: "/api/v1/monitoring/probes/{id}"},
		{Method: "POST", Path: "/api/v1/monitoring/profiles"}, {Method: "PATCH", Path: "/api/v1/monitoring/profiles/{id}"},
		{Method: "PATCH", Path: "/api/v1/monitoring/groups/{id}"},
	} {
		o := doc.operation(t, op)
		if r := o.Responses["403"]; r.Ref != "#/components/responses/EntitlementLimit" {
			t.Errorf("%s: 403 is %q, want EntitlementLimit", op, r.Ref)
		}
		if r := o.Responses["422"]; r.Ref != "#/components/responses/UnprocessableEntity" {
			t.Errorf("%s: 422 is %q, want UnprocessableEntity", op, r.Ref)
		}
	}
	problem := doc.properties(t, doc.schema(t, "ProblemDetail"))
	requireProperties(t, "ProblemDetail", problem, "field", "violations")
	if v := problem["violations"]; v != nil && (v.Items == nil || !slices.Contains(v.Items.Required, "field")) {
		t.Error("ProblemDetail.violations does not list fields")
	}

	// The list of probes filters as WP-939 implements it.
	list := doc.operation(t, speccheck.Operation{Method: "GET", Path: "/api/v1/monitoring/probes"})
	var filters []string
	for _, p := range list.Parameters {
		if p.In == "query" {
			filters = append(filters, p.Name)
		}
	}
	for _, f := range []string{"ci_id", "type", "collector_group_id", "client_id", "enabled"} {
		if !slices.Contains(filters, f) {
			t.Errorf("GET /api/v1/monitoring/probes: filter %s missing", f)
		}
	}

	// Collector groups only under /api/v1/collector-groups (E-160).
	for _, op := range documented {
		if strings.HasPrefix(op.Path, "/api/v1/monitoring/collector-groups") {
			t.Errorf("%s: collector groups belong under /api/v1/collector-groups", op)
		}
	}

	// Schemas of AK1.
	for _, name := range []string{"Probe", "ProbeCreateRequest", "ProbeUpdateRequest", "EffectiveProbe", "ProbeType",
		"MonitoringProfile", "MonitoringGroup", "CollectorGroup", "CollectorGroupMember", "MonitoringEvent"} {
		doc.schema(t, name)
	}
	requireProperties(t, "Probe", doc.properties(t, doc.schema(t, "Probe")),
		"id", "organization_id", "client_id", "site_id", "ci_id", "collector_group_id", "type", "target", "params", "enabled", "parent_probe_id")
	if req := doc.schema(t, "ProbeCreateRequest").Required; !slices.Contains(req, "type") || !slices.Contains(req, "target") {
		t.Errorf("ProbeCreateRequest requires %v, want type and target", req)
	}
	if _, ok := doc.properties(t, doc.schema(t, "ProbeUpdateRequest"))["type"]; ok {
		t.Error("ProbeUpdateRequest changes the type")
	}
	effective := doc.properties(t, doc.schema(t, "EffectiveProbe"))
	requireProperties(t, "EffectiveProbe", effective, "interval_s", "timeout_s", "down_count", "up_count", "thresholds", "params")
	field := doc.properties(t, doc.schema(t, "EffectiveProbeField"))
	requireProperties(t, "EffectiveProbeField", field, "value", "origin", "clamped", "group_id")
	if origin := doc.schema(t, "ProbeValueOrigin").Enum; !slices.Equal(origin, []string{"default", "org", "client", "group", "ci", "probe", "plan"}) {
		t.Errorf("ProbeValueOrigin = %v", origin)
	}
	for _, f := range []string{"interval_s", "timeout_s", "down_count", "up_count"} {
		if s := effective[f]; s == nil || s.Ref != "#/components/schemas/EffectiveProbeField" {
			t.Errorf("EffectiveProbe.%s is no EffectiveProbeField", f)
		}
	}
	requireProperties(t, "MonitoringProfile", doc.properties(t, doc.schema(t, "MonitoringProfile")),
		"level", "level_id", "probe_type", "params")
	requireProperties(t, "MonitoringGroup", doc.properties(t, doc.schema(t, "MonitoringGroup")), "name", "priority", "client_id")
	requireProperties(t, "CollectorGroup", doc.properties(t, doc.schema(t, "CollectorGroup")),
		"mode", "liveness_timeout_s", "anchor_probe_id", "status", "status_changed_at", "members")
	requireProperties(t, "CollectorGroupMember", doc.properties(t, doc.schema(t, "CollectorGroupMember")),
		"collector_id", "priority", "weight", "member_status", "last_alive_at", "clock_offset_ms", "assigned_probes")

	// ProbeType carries every field of a register entry.
	reg, err := os.ReadFile(filepath.Clean("../../../api/probe-types.json"))
	if err != nil {
		t.Fatal(err)
	}
	var register struct {
		Defs struct {
			Entry struct {
				Properties map[string]json.RawMessage `json:"properties"`
			} `json:"entry"`
		} `json:"$defs"`
	}
	if err := json.Unmarshal(reg, &register); err != nil {
		t.Fatal(err)
	}
	probeType := doc.properties(t, doc.schema(t, "ProbeType"))
	entryFields := make([]string, 0, len(register.Defs.Entry.Properties))
	for k := range register.Defs.Entry.Properties {
		entryFields = append(entryFields, k)
	}
	sort.Strings(entryFields)
	if len(entryFields) == 0 {
		t.Fatal("register entry has no fields")
	}
	requireProperties(t, "ProbeType", probeType, append(entryFields, "key")...)
}

// TestMonitoringEventsContract covers WP-938 AK3: GET
// /api/v1/monitoring/events lists the monitoring events of the table
// monitoring_event (schema after WP-979) with the filters ci_id, probe_id,
// kind, from and to and the cursor; there is no filter on the domain event
// types (no parameter type).
func TestMonitoringEventsContract(t *testing.T) {
	doc := loadMonitoringSpec(t)
	o := doc.operation(t, speccheck.Operation{Method: "GET", Path: "/api/v1/monitoring/events"})
	var params, refs []string
	for _, p := range o.Parameters {
		if p.Ref != "" {
			refs = append(refs, p.Ref)
			continue
		}
		params = append(params, p.Name)
	}
	sort.Strings(params)
	if want := []string{"ci_id", "from", "kind", "probe_id", "to"}; !slices.Equal(params, want) {
		t.Errorf("filters = %v, want %v", params, want)
	}
	if slices.Contains(params, "type") {
		t.Error("the monitoring events filter by the domain event type")
	}
	if !slices.Contains(refs, "#/components/parameters/Cursor") || !slices.Contains(refs, "#/components/parameters/Limit") {
		t.Errorf("parameters %v, want the cursor contract", refs)
	}
	media := o.Responses["200"].Content["application/json"]
	if media.Schema == nil || media.Schema.Ref != "#/components/schemas/MonitoringEventListResponse" {
		t.Fatalf("200 schema %+v, want MonitoringEventListResponse", media.Schema)
	}
	listProps := doc.properties(t, doc.schema(t, "MonitoringEventListResponse"))
	requireProperties(t, "MonitoringEventListResponse", listProps, "data", "next_cursor")
	if items := listProps["data"].Items; items == nil || items.Ref != "#/components/schemas/MonitoringEvent" {
		t.Error("MonitoringEventListResponse.data is no list of MonitoringEvent")
	}
	event := doc.properties(t, doc.schema(t, "MonitoringEvent"))
	requireProperties(t, "MonitoringEvent", event,
		"id", "ci_id", "probe_id", "collector_id", "source_ip", "kind", "trap_oid", "varbinds", "observed_at", "received_at")
	if kinds := doc.schema(t, "MonitoringEventKind").Enum; !slices.Equal(kinds,
		[]string{"link_down", "link_up", "cold_start", "warm_start", "auth_failure", "other"}) {
		t.Errorf("MonitoringEventKind = %v", kinds)
	}
	for _, p := range o.Parameters {
		if p.Name == "kind" && (p.Schema == nil || p.Schema.Ref != "#/components/schemas/MonitoringEventKind") {
			t.Error("filter kind does not use MonitoringEventKind")
		}
	}
}
