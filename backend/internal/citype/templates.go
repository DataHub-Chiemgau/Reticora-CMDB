package citype

import "github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"

// Templates returns the editable default type templates (spec §19). They
// initialize metadata only and carry no hardcoded domain restrictions: every
// template can be edited, cloned or deactivated by administrators.
//
// The legacy built-in types (migration 000021: switch, router, server, …)
// remain untouched; these templates extend the catalogue with the requested
// enterprise set. Templates keyed like existing system types (e.g. "server")
// are applied as enrichment metadata by the seeder instead of duplicating the
// type row.
func Templates() []Type {
	return []Type{
		template("physical_server", "Physical Server", "hardware", false,
			fields(
				manufacturerModelSerial(),
				field("hostname", "Hostname", fieldmeta.TypeText, false, "identity"),
				field("server_kind", "Server Kind", fieldmeta.TypeEnum, true, "identity").
					withEnum("physical", "virtual"),
				field("rack", "Rack", fieldmeta.TypeText, false, "placement").
					visibleWhen("server_kind", "eq", "physical"),
				field("rack_unit", "Rack Unit", fieldmeta.TypeInteger, false, "placement").
					visibleWhen("server_kind", "eq", "physical"),
				field("bmc_address", "BMC Address", fieldmeta.TypeIP, false, "management").
					visibleWhen("server_kind", "eq", "physical"),
				field("hypervisor", "Hypervisor", fieldmeta.TypeCIRef, false, "virtualization").
					withReference("ci").visibleWhen("server_kind", "eq", "virtual"),
				field("vm_id", "VM ID", fieldmeta.TypeText, false, "virtualization").
					visibleWhen("server_kind", "eq", "virtual"),
				field("vcpu", "vCPU", fieldmeta.TypeInteger, false, "virtualization").
					visibleWhen("server_kind", "eq", "virtual"),
				field("cpu_cores", "CPU Cores", fieldmeta.TypeInteger, false, "hardware"),
				field("ram_gb", "RAM (GB)", fieldmeta.TypeDecimal, false, "hardware"),
				field("operating_system", "Operating System", fieldmeta.TypeText, false, "software"),
			), withInventory(), withLifecycle()),
		template("virtual_server", "Virtual Server", "hardware", false,
			fields(
				field("hostname", "Hostname", fieldmeta.TypeText, true, "identity"),
				field("hypervisor", "Hypervisor", fieldmeta.TypeCIRef, false, "virtualization").withReference("ci"),
				field("vcpu", "vCPU", fieldmeta.TypeInteger, false, "virtualization"),
				field("ram_gb", "RAM (GB)", fieldmeta.TypeDecimal, false, "virtualization"),
				field("operating_system", "Operating System", fieldmeta.TypeText, false, "software"),
			)),
		template("client_laptop", "Client / Laptop", "end_user", false,
			fields(
				manufacturerModelSerial(),
				field("hostname", "Hostname", fieldmeta.TypeText, true, "identity"),
				field("operating_system", "Operating System", fieldmeta.TypeText, false, "software"),
				field("assigned_user", "Assigned User", fieldmeta.TypeUser, false, "assignment").withReference("user"),
			), withInventory(), withLifecycle()),
		template("mobile_device", "Mobile Device", "end_user", false,
			fields(
				manufacturerModelSerial(),
				field("imei", "IMEI", fieldmeta.TypeText, false, "identity"),
				field("phone_number", "Phone Number", fieldmeta.TypeText, false, "identity"),
				field("mdm_enrolled", "MDM Enrolled", fieldmeta.TypeBoolean, false, "management"),
			), withInventory(), withLifecycle()),
		template("switch", "Switch", "network", false,
			fields(
				manufacturerModelSerial(),
				field("management_ip", "Management IP", fieldmeta.TypeIP, false, "management"),
				field("port_count", "Port Count", fieldmeta.TypeInteger, false, "hardware"),
			), withInventory(), withLifecycle()),
		template("router", "Router", "network", false,
			fields(
				manufacturerModelSerial(),
				field("management_ip", "Management IP", fieldmeta.TypeIP, false, "management"),
			), withInventory(), withLifecycle()),
		template("firewall", "Firewall", "network", false,
			fields(
				manufacturerModelSerial(),
				field("management_ip", "Management IP", fieldmeta.TypeIP, false, "management"),
				field("throughput_gbps", "Throughput (Gbit/s)", fieldmeta.TypeDecimal, false, "hardware"),
			), withInventory(), withLifecycle()),
		template("access_point", "Access Point", "network", false,
			fields(
				manufacturerModelSerial(),
				field("management_ip", "Management IP", fieldmeta.TypeIP, false, "management"),
				field("ssid", "SSID", fieldmeta.TypeText, false, "wireless"),
			), withInventory(), withLifecycle()),
		template("pdu", "PDU", "power", false,
			fields(
				manufacturerModelSerial(),
				field("outlet_count", "Outlet Count", fieldmeta.TypeInteger, false, "hardware"),
			), withInventory(), withLifecycle()),
		template("ups", "UPS", "power", false,
			fields(
				manufacturerModelSerial(),
				field("capacity_va", "Capacity (VA)", fieldmeta.TypeInteger, false, "hardware"),
			), withInventory(), withLifecycle()),
		template("storage", "Storage", "storage", false,
			fields(
				manufacturerModelSerial(),
				field("total_capacity_tb", "Total Capacity (TB)", fieldmeta.TypeDecimal, false, "hardware"),
			), withInventory(), withLifecycle()),
		template("printer", "Printer", "peripheral", false,
			fields(
				manufacturerModelSerial(),
				field("management_ip", "Management IP", fieldmeta.TypeIP, false, "management"),
			), withInventory(), withLifecycle()),
		template("iot_device", "IoT Device", "iot", false,
			fields(
				manufacturerModelSerial(),
				field("protocol", "Protocol", fieldmeta.TypeText, false, "connectivity"),
			), withInventory(), withLifecycle()),
		template("application", "Application", "software", true,
			fields(
				field("version", "Version", fieldmeta.TypeText, false, "software"),
				field("url", "URL", fieldmeta.TypeURL, false, "access"),
				field("criticality", "Criticality", fieldmeta.TypeEnum, false, "classification").
					withEnum("low", "medium", "high", "critical"),
			)),
		template("database", "Database", "software", true,
			fields(
				field("engine", "Engine", fieldmeta.TypeText, false, "software"),
				field("version", "Version", fieldmeta.TypeText, false, "software"),
				field("size_gb", "Size (GB)", fieldmeta.TypeDecimal, false, "capacity"),
			)),
		template("vm", "VM", "infrastructure", true,
			fields(
				field("hypervisor", "Hypervisor", fieldmeta.TypeCIRef, false, "virtualization").withReference("ci"),
				field("vcpu", "vCPU", fieldmeta.TypeInteger, false, "virtualization"),
				field("ram_gb", "RAM (GB)", fieldmeta.TypeDecimal, false, "virtualization"),
			)),
		template("hypervisor", "Hypervisor", "infrastructure", false,
			fields(
				manufacturerModelSerial(),
				field("management_ip", "Management IP", fieldmeta.TypeIP, false, "management"),
			), withInventory(), withLifecycle()),
		template("cluster", "Cluster", "infrastructure", true,
			fields(
				field("ha_enabled", "HA Enabled", fieldmeta.TypeBoolean, false, "resilience"),
			)),
		template("business_service", "Business Service", "service", true,
			fields(
				field("criticality", "Criticality", fieldmeta.TypeEnum, false, "classification").
					withEnum("low", "medium", "high", "critical"),
				field("service_owner", "Service Owner", fieldmeta.TypeUser, false, "ownership").withReference("user"),
				field("sla", "SLA", fieldmeta.TypeText, false, "classification"),
			)),
		template("cloud_resource", "Cloud Resource", "cloud", true,
			fields(
				field("provider", "Provider", fieldmeta.TypeEnum, false, "cloud").
					withEnum("aws", "azure", "gcp", "other"),
				field("region", "Region", fieldmeta.TypeText, false, "cloud"),
				field("resource_id", "Resource ID", fieldmeta.TypeText, false, "cloud"),
			)),
		template("software_license", "Software License", "software", true,
			fields(
				field("license_key", "License Key", fieldmeta.TypeText, false, "license"),
				field("seats", "Seats", fieldmeta.TypeInteger, false, "license"),
				field("expires_at", "Expires At", fieldmeta.TypeDate, false, "license"),
				field("contract", "Contract", fieldmeta.TypeContractRef, false, "license").withReference("contract"),
			)),
		template("contract", "Contract", "commercial", true,
			fields(
				field("contract_number", "Contract Number", fieldmeta.TypeText, false, "contract"),
				field("vendor", "Vendor", fieldmeta.TypeText, false, "contract"),
				field("start_date", "Start Date", fieldmeta.TypeDate, false, "contract"),
				field("end_date", "End Date", fieldmeta.TypeDate, false, "contract"),
			)),
		template("generic_asset", "Generic Asset", "generic", false,
			fields(
				manufacturerModelSerial(),
			), withInventory(), withLifecycle()),
	}
}

// builder helpers keep the template declarations declarative.

type fieldBuilder struct{ Field }

func field(name, label, dataType string, required bool, group string) fieldBuilder {
	return fieldBuilder{Field{
		Name: name, Label: label, DataType: dataType,
		Required: required, UIGroup: group, Scope: "type",
	}}
}

func (b fieldBuilder) withEnum(values ...string) fieldBuilder {
	b.EnumValues = values
	return b
}

func (b fieldBuilder) withReference(target string) fieldBuilder {
	b.ReferenceTarget = target
	return b
}

func (b fieldBuilder) visibleWhen(fieldName, op string, value any) fieldBuilder {
	if b.Conditional == nil {
		b.Conditional = &fieldmeta.ConditionalRules{}
	}
	b.Conditional.VisibleWhen = append(b.Conditional.VisibleWhen,
		fieldmeta.Predicate{Field: fieldName, Op: op, Value: value})
	return b
}

func fields(builders ...any) []Field {
	var out []Field
	for _, b := range builders {
		switch v := b.(type) {
		case fieldBuilder:
			out = append(out, v.Field)
		case []Field:
			out = append(out, v...)
		}
	}
	return out
}

func manufacturerModelSerial() []Field {
	return []Field{
		{Name: "manufacturer", Label: "Manufacturer", DataType: fieldmeta.TypeText, UIGroup: "identity", Scope: "type"},
		{Name: "model", Label: "Model", DataType: fieldmeta.TypeText, UIGroup: "identity", Scope: "type"},
		{Name: "serial_number", Label: "Serial Number", DataType: fieldmeta.TypeText, UIGroup: "identity", Scope: "type"},
	}
}

func template(key, displayName, category string, logical bool, flds []Field, opts ...func(*Type)) Type {
	t := Type{
		Key:                      key,
		Name:                     key,
		DisplayName:              displayName,
		Category:                 category,
		IsLogical:                logical,
		IsBuiltin:                true,
		IsSystem:                 true,
		IsActive:                 true,
		Version:                  1,
		TemplateKey:              key,
		Capabilities:             map[string]any{},
		AllowedRelationshipTypes: []string{},
		UISchema:                 map[string]any{},
		ComplianceRules:          []any{},
		DiscoveryMappings:        []any{},
		Fields:                   flds,
	}
	for _, opt := range opts {
		opt(&t)
	}
	return t
}

// withInventory marks a type as inventory/asset capable (spec §1: capabilities).
func withInventory() func(*Type) {
	return func(t *Type) {
		t.Capabilities["asset_tracking"] = true
	}
}

// withLifecycle assigns the default physical asset lifecycle to a type.
func withLifecycle() func(*Type) {
	return func(t *Type) {
		t.Capabilities["lifecycle"] = "physical_asset"
	}
}
