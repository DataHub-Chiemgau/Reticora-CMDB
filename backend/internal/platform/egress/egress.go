// Package egress provides the outbound HTTP client for user-configured
// destinations (webhooks, SCIM connectors). It blocks private, loopback,
// link-local, cloud-metadata and IPv6 unique-local targets, connects to the
// address it checked (DNS pinning against rebinding) and re-checks every one
// of at most three redirects (SEC-08).
package egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// ErrBlockedDestination is returned for a destination outside the public
// internet.
var ErrBlockedDestination = errors.New("egress: destination is not allowed")

// MaxRedirects is the number of redirects a request may follow.
const MaxRedirects = 3

// DefaultTimeout bounds a whole request including redirects.
const DefaultTimeout = 10 * time.Second

// Resolver looks up the addresses of a host name.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Options configures a client.
type Options struct {
	// AllowPrivate permits private, loopback and link-local destinations,
	// for on-premises installations whose webhooks and connectors live in
	// the internal network (RETICORA_EGRESS_ALLOW_PRIVATE). Cloud metadata
	// endpoints stay blocked.
	AllowPrivate bool
	// Timeout bounds a request; zero uses DefaultTimeout.
	Timeout time.Duration
	// Resolver replaces the system resolver (tests).
	Resolver Resolver
}

var blockedPrefixes = mustPrefixes(
	"0.0.0.0/8",      // "this network"
	"10.0.0.0/8",     // private
	"100.64.0.0/10",  // carrier-grade NAT (also Alibaba metadata 100.100.100.200)
	"127.0.0.0/8",    // loopback
	"169.254.0.0/16", // link-local, cloud metadata 169.254.169.254
	"172.16.0.0/12",  // private
	"192.0.0.0/24",   // IETF protocol assignments
	"192.168.0.0/16", // private
	"198.18.0.0/15",  // benchmarking
	"224.0.0.0/4",    // multicast
	"240.0.0.0/4",    // reserved, broadcast
	"::/128",         // unspecified
	"::1/128",        // loopback
	"fc00::/7",       // unique local (incl. AWS metadata fd00:ec2::254)
	"fe80::/10",      // link-local
	"ff00::/8",       // multicast
	"64:ff9b:1::/48", // local-use NAT64
	"2001:db8::/32",  // documentation
	"100::/64",       // discard-only
)

// metadataPrefixes stay blocked even when private destinations are allowed.
var metadataPrefixes = mustPrefixes(
	"169.254.0.0/16",
	"100.100.100.200/32",
	"fd00:ec2::254/128",
)

func mustPrefixes(cidrs ...string) []netip.Prefix {
	out := make([]netip.Prefix, 0, len(cidrs))
	for _, c := range cidrs {
		out = append(out, netip.MustParsePrefix(c))
	}
	return out
}

// Allowed reports whether a connection to ip is permitted.
func (o Options) Allowed(ip netip.Addr) bool {
	ip = ip.Unmap()
	// NAT64 (64:ff9b::/96) embeds an IPv4 address: judge that address.
	if ip.Is6() && netip.MustParsePrefix("64:ff9b::/96").Contains(ip) {
		b := ip.As16()
		ip = netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	}
	if !ip.IsValid() {
		return false
	}
	for _, p := range metadataPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	if o.AllowPrivate {
		return !ip.IsUnspecified() && !ip.IsMulticast()
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// ValidateURL checks a destination URL when it is stored: http or https, a
// host, no credentials in the URL, and no host that is blocked without a DNS
// lookup (IP literals, localhost, internal suffixes). The authoritative
// check of the resolved address happens on every connection.
func (o Options) ValidateURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("%w: %v", ErrBlockedDestination, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q (http or https required)", ErrBlockedDestination, u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("%w: credentials in the URL", ErrBlockedDestination)
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "" {
		return fmt.Errorf("%w: missing host", ErrBlockedDestination)
	}
	if ip, parseErr := netip.ParseAddr(host); parseErr == nil {
		if !o.Allowed(ip) {
			return fmt.Errorf("%w: %s", ErrBlockedDestination, host)
		}
		return nil
	}
	if !o.AllowPrivate && (host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal")) {
		return fmt.Errorf("%w: %s", ErrBlockedDestination, host)
	}
	return nil
}

// NewClient returns the egress HTTP client. It never uses an environment
// proxy, which would bypass the destination check.
func NewClient(opts Options) *http.Client {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	resolver := opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	dialer := &net.Dialer{Timeout: timeout}
	transport := &http.Transport{
		Proxy: nil,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addrs, err := resolve(ctx, resolver, host)
			if err != nil {
				return nil, err
			}
			// Every resolved address must be allowed: a name that also
			// points into the internal network is rejected as a whole.
			for _, ip := range addrs {
				if !opts.Allowed(ip) {
					return nil, fmt.Errorf("%w: %s resolves to %s", ErrBlockedDestination, host, ip)
				}
			}
			// Pinning: connect to the checked address, never resolve again.
			return dialer.DialContext(ctx, network, net.JoinHostPort(addrs[0].Unmap().String(), port))
		},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		MaxIdleConns:          32,
		IdleConnTimeout:       90 * time.Second,
	}
	return &http.Client{
		Timeout:   timeout,
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > MaxRedirects {
				return fmt.Errorf("egress: more than %d redirects", MaxRedirects)
			}
			// The target is checked again when it is dialed; the scheme
			// and literal hosts are checked here.
			return opts.ValidateURL(req.URL.String())
		},
	}
}

func resolve(ctx context.Context, resolver Resolver, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip}, nil
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("egress: resolve %s: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("egress: %s has no address", host)
	}
	return addrs, nil
}
