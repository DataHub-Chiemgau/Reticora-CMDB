// Package ci provides the CI type system for the CMDB.
package ci

// TypeAttribute defines a custom attribute on a CI type.
type TypeAttribute struct {
	Name       string `json:"name"`
	DataType   string `json:"data_type"` // string, number, boolean, date, enum
	Required   bool   `json:"required"`
	DefaultVal string `json:"default_value,omitempty"`
}

// Type defines a CI type (e.g., server, switch, pdu).
type Type struct {
	ID             string          `json:"id"`
	OrganizationID string          `json:"organization_id"`
	Name           string          `json:"name"`
	Icon           string          `json:"icon,omitempty"`
	Attributes     []TypeAttribute `json:"attributes"`
}

// DefaultTypes returns the built-in CI types.
func DefaultTypes() []Type {
	return []Type{
		{
			Name: "server",
			Attributes: []TypeAttribute{
				{Name: "manufacturer", DataType: "string", Required: true},
				{Name: "model", DataType: "string", Required: true},
				{Name: "serial_number", DataType: "string", Required: true},
				{Name: "management_ip", DataType: "string", Required: false},
				{Name: "cpu_cores", DataType: "number", Required: false},
				{Name: "ram_gb", DataType: "number", Required: false},
			},
		},
		{
			Name: "switch",
			Attributes: []TypeAttribute{
				{Name: "manufacturer", DataType: "string", Required: true},
				{Name: "model", DataType: "string", Required: true},
				{Name: "serial_number", DataType: "string", Required: true},
				{Name: "management_ip", DataType: "string", Required: true},
				{Name: "port_count", DataType: "number", Required: false},
			},
		},
		{
			Name: "router",
			Attributes: []TypeAttribute{
				{Name: "manufacturer", DataType: "string", Required: true},
				{Name: "model", DataType: "string", Required: true},
				{Name: "serial_number", DataType: "string", Required: true},
				{Name: "management_ip", DataType: "string", Required: true},
			},
		},
		{
			Name: "firewall",
			Attributes: []TypeAttribute{
				{Name: "manufacturer", DataType: "string", Required: true},
				{Name: "model", DataType: "string", Required: true},
				{Name: "serial_number", DataType: "string", Required: true},
				{Name: "management_ip", DataType: "string", Required: true},
			},
		},
		{
			Name: "pdu",
			Attributes: []TypeAttribute{
				{Name: "manufacturer", DataType: "string", Required: true},
				{Name: "model", DataType: "string", Required: true},
				{Name: "serial_number", DataType: "string", Required: false},
				{Name: "outlet_count", DataType: "number", Required: false},
			},
		},
		{
			Name: "ups",
			Attributes: []TypeAttribute{
				{Name: "manufacturer", DataType: "string", Required: true},
				{Name: "model", DataType: "string", Required: true},
				{Name: "capacity_va", DataType: "number", Required: false},
			},
		},
		{
			Name: "nas",
			Attributes: []TypeAttribute{
				{Name: "manufacturer", DataType: "string", Required: true},
				{Name: "model", DataType: "string", Required: true},
				{Name: "serial_number", DataType: "string", Required: false},
				{Name: "total_capacity_tb", DataType: "number", Required: false},
			},
		},
		{
			Name: "client",
			Attributes: []TypeAttribute{
				{Name: "hostname", DataType: "string", Required: true},
				{Name: "os", DataType: "string", Required: false},
				{Name: "manufacturer", DataType: "string", Required: false},
				{Name: "model", DataType: "string", Required: false},
			},
		},
	}
}
