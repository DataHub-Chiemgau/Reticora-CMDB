package search

import (
	"net/netip"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Identifier fields of search_identifier (SRC-02, WP-308). The indexer
// writes the ones of a CI document from Document.Fields; network.* are
// written by the triggers of network_interface and ip_address, the
// inventory number of an asset by the trigger of asset (migration 000233).
const (
	IdentifierManagementIP = "core.management_ip"
	IdentifierPrimaryMAC   = "core.primary_mac"
	// IdentifierAssetInventory is the inventory number of the asset linked
	// to a CI (ci.asset_id), at the CI document.
	IdentifierAssetInventory = "asset.inventory_no"
	IdentifierNetworkIP      = "network.ip"
	IdentifierNetworkMAC     = "network.mac"
	// IdentifierInventory is the inventory number at the asset document.
	IdentifierInventory = "core.inventory_no"
)

// identifierKinds are the identifier fields the indexer writes from
// Document.Fields with the kind of their value. They are never field rows.
// The owner of a CI (UserFieldOwner, WP-788) is a user id.
var identifierKinds = map[string]string{
	IdentifierManagementIP:   identifierIP,
	IdentifierPrimaryMAC:     identifierMAC,
	IdentifierAssetInventory: identifierText,
	UserFieldOwner:           identifierUser,
}

const (
	identifierIP   = "ip"
	identifierMAC  = "mac"
	identifierText = "text"
	identifierUser = "user"
)

// indexerIdentifiers are the identifier fields the indexer owns at a CI, in
// a fixed order: the holder of the asset (UserFieldAssetHolder) is read in
// the statement of IndexDocuments.
var indexerIdentifiers = []string{IdentifierManagementIP, IdentifierPrimaryMAC, IdentifierAssetInventory, UserFieldOwner,
	UserFieldAssetHolder}

// macFields are the identifier fields that hold a MAC.
var macFields = []string{IdentifierPrimaryMAC, IdentifierNetworkMAC}

// identifierFields are the identifier fields per entity type.
var identifierFields = map[string][]string{
	"ci":    {IdentifierManagementIP, IdentifierPrimaryMAC, IdentifierAssetInventory, IdentifierNetworkIP, IdentifierNetworkMAC},
	"asset": {IdentifierInventory},
}

// Minimum lengths of a prefix search on the identifiers.
const (
	minPrefixLen    = 3
	minMACPrefixHex = 6
)

// Identifiers are the conditions a search text gives on search_identifier.
// Exactly one of MAC, Addr, the CIDR range (Low/High) and the prefixes is
// set when ParseIdentifiers reports a match.
type Identifiers struct {
	// MAC is a full MAC in normal form (twelve lower-case hex digits):
	// equality on norm.
	MAC string
	// Addr is an IPv4 or IPv6 address: equality on addr.
	Addr string
	// Low and High bound a CIDR prefix (network and broadcast address):
	// addr between them.
	Low, High string
	// Prefix is a prefix of norm for the fields that hold no MAC, PrefixEnd
	// its exclusive upper bound ("" without one).
	Prefix, PrefixEnd string
	// MACPrefix is a prefix of norm for the MAC fields (at least six hex
	// digits), MACPrefixEnd its exclusive upper bound.
	MACPrefix, MACPrefixEnd string
}

// macFormats are the input formats of PostgreSQL's macaddr.
var macFormats = regexp.MustCompile(`^(?i:[0-9a-f]{2}(:[0-9a-f]{2}){5}|[0-9a-f]{2}(-[0-9a-f]{2}){5}|[0-9a-f]{6}[:-][0-9a-f]{6}|` +
	`[0-9a-f]{4}\.[0-9a-f]{4}\.[0-9a-f]{4}|[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}|[0-9a-f]{12})$`)

// macPrefix is a beginning of a MAC: hex digits and the separators of the
// MAC formats.
var macPrefix = regexp.MustCompile(`^(?i:[0-9a-f:.\-]+)$`)

// ParseIdentifiers reads the identifier conditions of a search text without
// white space (SRC-02, WP-308): a MAC in a macaddr input format, an IPv4 or
// IPv6 address, a CIDR prefix, else from three characters a prefix of the
// normal form (for the MAC fields from six hex digits). ok is false for a
// text with white space or too short.
func ParseIdentifiers(text string) (ids Identifiers, ok bool) {
	text = strings.TrimSpace(text)
	if text == "" || strings.IndexFunc(text, unicode.IsSpace) >= 0 {
		return Identifiers{}, false
	}
	if macFormats.MatchString(text) {
		return Identifiers{MAC: hexDigits(text)}, true
	}
	if addr, err := netip.ParseAddr(text); err == nil && addr.Zone() == "" {
		return Identifiers{Addr: addr.String()}, true
	}
	if prefix, err := netip.ParsePrefix(text); err == nil {
		prefix = prefix.Masked()
		return Identifiers{Low: prefix.Addr().String(), High: lastAddr(prefix).String()}, true
	}
	if utf8.RuneCountInString(text) < minPrefixLen {
		return Identifiers{}, false
	}
	ids.Prefix = strings.ToLower(text)
	ids.PrefixEnd = prefixEnd(ids.Prefix)
	if macPrefix.MatchString(text) {
		if hex := hexDigits(text); len(hex) >= minMACPrefixHex && len(hex) <= 12 {
			ids.MACPrefix, ids.MACPrefixEnd = hex, prefixEnd(hex)
		}
	}
	return ids, true
}

// NormalizeMAC returns the normal form of a MAC in a macaddr input format,
// ok false for anything else.
func NormalizeMAC(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if !macFormats.MatchString(s) {
		return "", false
	}
	return hexDigits(s), true
}

// hexDigits returns the hex digits of s in lower case.
func hexDigits(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// lastAddr returns the last address of a prefix (the broadcast address of
// an IPv4 network).
func lastAddr(p netip.Prefix) netip.Addr {
	raw := p.Addr().AsSlice()
	bits := p.Bits()
	for i := range raw {
		for b := 0; b < 8; b++ {
			if i*8+b >= bits {
				raw[i] |= 0x80 >> b
			}
		}
	}
	addr, _ := netip.AddrFromSlice(raw)
	return addr
}

// prefixEnd returns the least string greater than every string starting
// with prefix in byte order: the last character incremented. UTF-8 keeps
// the order of the code points, so the bound stays valid text. "" means no
// upper bound.
func prefixEnd(prefix string) string {
	runes := []rune(prefix)
	for len(runes) > 0 {
		last := runes[len(runes)-1] + 1
		if last >= 0xD800 && last <= 0xDFFF {
			last = 0xE000
		}
		if last <= utf8.MaxRune {
			runes[len(runes)-1] = last
			return string(runes)
		}
		runes = runes[:len(runes)-1]
	}
	return ""
}
