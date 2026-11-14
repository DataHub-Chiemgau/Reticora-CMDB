package readonly

import (
	"context"
)

// The port inventory and the services of a port (ADM-04, E-251, WP-1073).
// Neither answer has a schema in the documentation 9.14.0 (A-86): the
// inventory is assumed to be AccessNode[] with modules[].ports[] after
// components.schemas, the services of a port an array of whole numbers. A
// different form is the class decode and counts
// reticora_admp_undocumented_shape_total{operation}.

// Paths and operations of this file (allowlist, E-248).
const (
	accessNodePickerPath = "/V1/infrastructure/accessnodepicker"
	portServicesPath     = "/V1/infrastructure/port/{portId}/dienstIds"

	opAccessNodePicker = "getV1InfrastructureAccessnodepicker"
	opPortServices     = "getV1InfrastructurePort:portIdDienstIds"
)

// The client sets neither technology nor organizationId of the picker.
var _ = allowGets(getRoute{path: accessNodePickerPath}, getRoute{path: portServicesPath})

// AccessPort is a port of the ADMP inventory, flat: the port (AccessPort.id
// and portIndex), its module (unitIndex) and its node (id, name and, when
// the answer embeds the type, its protocol). Address, user, password, iv,
// SNMP version, organization and extra of the node are never decoded.
type AccessPort struct {
	ID        int64
	NodeID    int64
	NodeName  string
	Protocol  string
	UnitIndex int64
	PortIndex int64
}

// accessNode is the part of AccessNode the inventory decodes.
type accessNode struct {
	ID   *docInt `json:"id"`
	Name *string `json:"name"`
	Type *struct {
		Protocol string `json:"protocol"`
	} `json:"type"`
	Modules []struct {
		UnitIndex *docInt `json:"unitIndex"`
		Ports     []struct {
			ID        *docInt `json:"id"`
			PortIndex *docInt `json:"portIndex"`
		} `json:"ports"`
	} `json:"modules"`
}

// ListAccessPorts reads the port inventory of the account: GET
// /V1/infrastructure/accessnodepicker without query.
func (c *Client) ListAccessPorts(ctx context.Context) ([]AccessPort, error) {
	var nodes []accessNode
	if err := c.getJSON(ctx, accessNodePickerPath, nil, &nodes); err != nil {
		return nil, undocumented(opAccessNodePicker, err)
	}
	out := []AccessPort{}
	for i := range nodes {
		n := &nodes[i]
		if n.ID == nil || n.Name == nil {
			return nil, undocumented(opAccessNodePicker, classErr(ClassDecode, 0, nil))
		}
		protocol := ""
		if n.Type != nil {
			protocol = n.Type.Protocol
		}
		for _, m := range n.Modules {
			if m.UnitIndex == nil {
				return nil, undocumented(opAccessNodePicker, classErr(ClassDecode, 0, nil))
			}
			for _, p := range m.Ports {
				if p.ID == nil || p.PortIndex == nil || *p.ID < 1 {
					return nil, undocumented(opAccessNodePicker, classErr(ClassDecode, 0, nil))
				}
				out = append(out, AccessPort{ID: int64(*p.ID), NodeID: int64(*n.ID), NodeName: *n.Name, Protocol: protocol,
					UnitIndex: int64(*m.UnitIndex), PortIndex: int64(*p.PortIndex)})
			}
		}
	}
	return out, nil
}

// ListPortServiceIDs reads the services of a port: GET
// /V1/infrastructure/port/{portId}/dienstIds, an array of whole numbers.
func (c *Client) ListPortServiceIDs(ctx context.Context, portID int64) ([]int64, error) {
	p, err := docPath(portServicesPath, portID)
	if err != nil {
		return nil, err
	}
	var ids []docInt
	if err = c.getJSON(ctx, p, nil, &ids); err != nil {
		return nil, undocumented(opPortServices, err)
	}
	out := make([]int64, 0, len(ids))
	for _, id := range ids {
		if id < 1 {
			return nil, undocumented(opPortServices, classErr(ClassDecode, 0, nil))
		}
		out = append(out, int64(id))
	}
	return out, nil
}
