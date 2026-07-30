package ipam

import (
	"fmt"
	"net/netip"
	"strings"
)

// validateCIDR parses and normalizes a CIDR prefix using net/netip.
func validateCIDR(cidr string) (string, error) {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return "", fmt.Errorf("invalid cidr: %v", err)
	}
	return p.Masked().String(), nil
}

// validateIP parses a bare IP address (no prefix) using net/netip.
func validateIP(ip string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", fmt.Errorf("invalid ip address: %v", err)
	}
	return addr.String(), nil
}

// ipInSubnet reports whether ip belongs to the given cidr.
func ipInSubnet(ip, cidr string) (bool, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return false, fmt.Errorf("invalid ip address: %v", err)
	}
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return false, fmt.Errorf("invalid cidr: %v", err)
	}
	return p.Contains(addr), nil
}
