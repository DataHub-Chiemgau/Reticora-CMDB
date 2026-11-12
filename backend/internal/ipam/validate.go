package ipam

import (
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// maxRouteTargets is the upper bound of import_targets and export_targets
// (IPM-07, vrf_route_targets_check).
const maxRouteTargets = 64

// normalizeRouteDistinguisher checks a route distinguisher or route target
// in one of the three forms of RFC 4364 and returns it in canonical form:
// ASN16:nn32 (type 0), IPv4:nn16 (type 1) or ASN32:nn16 (type 2), the ASN
// 1–4294967295 (IPM-07). ok is false for any other value.
func normalizeRouteDistinguisher(v string) (canonical string, ok bool) {
	admin, number, found := strings.Cut(strings.TrimSpace(v), ":")
	if !found || !allDigits(number) {
		return "", false
	}
	nn, err := strconv.ParseUint(number, 10, 32)
	if err != nil {
		return "", false
	}
	if ip, ipErr := netip.ParseAddr(admin); ipErr == nil {
		if !ip.Is4() || nn > 0xffff {
			return "", false
		}
		return ip.String() + ":" + strconv.FormatUint(nn, 10), true
	}
	if !allDigits(admin) {
		return "", false
	}
	asn, err := strconv.ParseUint(admin, 10, 32)
	if err != nil || asn == 0 || (asn > 0xffff && nn > 0xffff) {
		return "", false
	}
	return strconv.FormatUint(asn, 10) + ":" + strconv.FormatUint(nn, 10), true
}

// allDigits reports whether s is a non-empty run of decimal digits.
func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// errRouteDistinguisher refuses a route distinguisher outside RFC 4364.
var errRouteDistinguisher = &api.FieldError{Field: "route_distinguisher",
	Message: "a route distinguisher has the form ASN16:nn32, IPv4:nn16 or ASN32:nn16", Status: http.StatusUnprocessableEntity}

// normalizeRouteTargets checks a list of route targets (IPM-07): every entry
// in a form of normalizeRouteDistinguisher, no duplicates, at most 64
// entries; field names the list in the 422 answer.
func normalizeRouteTargets(field string, targets []string) ([]string, error) {
	if len(targets) > maxRouteTargets {
		return nil, &api.FieldError{Field: field, Message: "at most 64 route targets are allowed",
			Status: http.StatusUnprocessableEntity}
	}
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		canonical, ok := normalizeRouteDistinguisher(t)
		if !ok {
			return nil, &api.FieldError{Field: field,
				Message: "a route target has the form ASN16:nn32, IPv4:nn16 or ASN32:nn16", Status: http.StatusUnprocessableEntity}
		}
		if slices.Contains(out, canonical) {
			return nil, &api.FieldError{Field: field, Message: "route targets must not repeat",
				Status: http.StatusUnprocessableEntity}
		}
		out = append(out, canonical)
	}
	return out, nil
}

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
