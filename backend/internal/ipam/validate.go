package ipam

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// validateCIDR parses and normalizes a CIDR prefix using net/netip.
// validation: the parse error of the request value.
func validateCIDR(cidr string) (string, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return "", fmt.Errorf("invalid cidr: %v", err)
	}
	return p.Masked().String(), nil
}

// validateIP parses a bare IP address (no prefix) using net/netip.
// validation: the parse error of the request value.
func validateIP(ip string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", fmt.Errorf("invalid ip address: %v", err)
	}
	return addr.String(), nil
}

// ipInSubnet reports whether ip belongs to the given cidr. ip may be in
// the host prefix form of a stored inet ("10.0.0.1/32", as address::text
// reads it); that counts as its address.
// validation: the parse errors of the values.
func ipInSubnet(ip, cidr string) (bool, error) {
	ip = strings.TrimSpace(ip)
	if p, perr := netip.ParsePrefix(ip); perr == nil && p.IsSingleIP() {
		ip = p.Addr().String()
	}
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false, fmt.Errorf("invalid ip address: %v", err)
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return false, fmt.Errorf("invalid cidr: %v", err)
	}
	return p.Contains(addr), nil
}

// Fixed answers of the gateway and state rules (IPM-04).
var (
	errGatewayOutside = &api.FieldError{Field: "gateway", Message: "the gateway must lie inside the subnet",
		Status: http.StatusUnprocessableEntity}
	errInvalidState = &api.FieldError{Field: "state", Message: "state must be active, offline or unknown",
		Status: http.StatusUnprocessableEntity}
	errAddressesOutsideCIDR = &api.FieldError{Field: "cidr", Message: "addresses of the subnet would lie outside the new prefix",
		Status: http.StatusUnprocessableEntity}
	// The gateway address keeps its subnet and stays until the subnet's
	// gateway changes or is cleared.
	errGatewayAddressSubnet = &api.FieldError{Field: "subnet_id",
		Message: "the address is the gateway of its subnet; change or clear the subnet's gateway first",
		Status:  http.StatusUnprocessableEntity}
	errGatewayAddressDelete = &api.ConflictError{Field: "is_gateway",
		Message: "the address is the gateway of its subnet; change or clear the subnet's gateway first"}
)

// checkGateway refuses a gateway outside cidr (422 gateway); an empty
// gateway passes. Both values are already valid; a gateway in the host
// prefix form of an inet ("10.0.0.1/32") counts as its address.
func checkGateway(gateway, cidr string) error {
	if strings.TrimSpace(gateway) == "" {
		return nil
	}
	in, err := ipInSubnet(gateway, cidr)
	if err != nil || !in {
		return errGatewayOutside
	}
	return nil
}

// StateFromPHPIPAMTag maps a tag of phpIPAM onto the state and type of
// IPM-04: Used is active, Offline offline; Reserved and DHCP keep the state
// unknown and set the type reserved or dhcp. ipType is empty when the tag
// does not decide the type; ok is false for an unknown tag. The comparison
// ignores case and surrounding blanks.
func StateFromPHPIPAMTag(tag string) (state, ipType string, ok bool) {
	switch strings.ToLower(strings.TrimSpace(tag)) {
	case "used":
		return IPStateActive, "", true
	case "offline":
		return IPStateOffline, "", true
	case "reserved":
		return IPStateUnknown, IPTypeReserved, true
	case "dhcp":
		return IPStateUnknown, IPTypeDHCP, true
	}
	return "", "", false
}
