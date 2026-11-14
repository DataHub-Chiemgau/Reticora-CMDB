package config

import (
	"regexp"
	"sort"
	"strings"
)

// Masked replaces every secret value of a configuration (WP-1053 AK3,
// E-207: masking in the collector, before hashing and sending).
const Masked = "<masked>"

// maskRule replaces the groups named v of re in a line. skip excludes the
// lines it matches (a more specific rule covers them); parent restricts the
// rule to the lines below a top-level line it matches (an indented block
// such as "tacacs server T1").
type maskRule struct {
	re     *regexp.Regexp
	skip   *regexp.Regexp
	parent *regexp.Regexp
}

// value matches a secret value: a quoted string or one token.
const value = `(?P<v>"[^"]*"|\S+)`

func rule(pattern string) maskRule { return maskRule{re: regexp.MustCompile(pattern)} }

// ciscoRules cover IOS, IOS-XE and NX-OS: passwords and secrets with their
// optional encryption type (0, 5, 7, 8, 9), SNMP communities (also of trap
// and inform hosts) and SNMPv3 keys, TACACS+/RADIUS keys (also
// server-private), pre-shared keys (also of keyrings), key strings, OSPF,
// BGP and NTP authentication keys.
var ciscoRules = []maskRule{
	rule(`(?i)\bsecret(?: \d)? ` + value),
	rule(`(?i)\bpassword(?: \d)? ` + value),
	rule(`^\s*snmp-server community ` + value),
	rule(`^\s*snmp-server host \S+ (?:(?:traps|informs) )?(?:version (?:1|2c|3 (?:auth|noauth|priv)) )?` + value),
	rule(`^\s*snmp-server user .*?\bauth (?:md5|sha\S*) ` + value),
	rule(`^\s*snmp-server user .*?\bpriv (?:(?:des|3des|aes)(?:-?\d+)?(?: \d+)? )?` + value),
	rule(`^\s*(?:tacacs|radius)-server (?:host \S+ .*?)?key(?: \d)? ` + value),
	rule(`^\s+server-private \S+ .*?\bkey(?: \d)? ` + value),
	{re: regexp.MustCompile(`^\s+key(?: \d)? ` + value + `$`), parent: regexp.MustCompile(`^(?:tacacs|radius) server `)},
	rule(`(?i)\bcrypto isakmp key(?: \d)? ` + value),
	rule(`(?i)\bpre-shared-key (?:address|hostname) \S+(?: \S+)? key(?: \d)? ` + value),
	{re: regexp.MustCompile(`(?i)\bpre-shared-key(?: local| remote)?(?: \d)? ` + value), skip: regexp.MustCompile(`(?i)\bpre-shared-key (?:address|hostname) `)},
	rule(`(?i)\bkey-string(?: \d)? ` + value),
	rule(`(?i)\bmessage-digest-key \d+ md5(?: \d)? ` + value),
	rule(`(?i)\bauthentication-key \d+ md5 ` + value),
	{re: regexp.MustCompile(`(?i)\bauthentication-key(?: \d)? ` + value), skip: regexp.MustCompile(`(?i)\bauthentication-key \d+ md5 `)},
}

// junosRules cover the display set form: encrypted passwords, $9$ secrets,
// SNMP communities, SNMPv3 keys, pre-shared keys and authentication keys.
var junosRules = []maskRule{
	rule(`\bencrypted-password ` + value),
	rule(`\bsecret ` + value),
	rule(`\bsnmp community ` + value),
	rule(`\bauthentication-key \d+ type \S+ value ` + value),
	{re: regexp.MustCompile(`\bauthentication-key ` + value), skip: regexp.MustCompile(`\bauthentication-key \d+ type `)},
	rule(`\bauthentication md5 \d+ key ` + value),
	rule(`\bprivacy-key ` + value),
	rule(`\bauthentication-password ` + value),
	rule(`\bprivacy-password ` + value),
	rule(`\bpre-shared-key (?:ascii-text|hexadecimal) ` + value),
	rule(`\bplain-text-password-value ` + value),
}

// rosValue is a RouterOS value: a quoted string with backslash escapes or
// one token.
const rosValue = `(?P<v>"(?:[^"\\]|\\.)*"|\S+)`

// routerOSRules cover /export terse: properties holding secrets (BGP
// tcp-md5-key and OSPF auth-key included) and the names of SNMP
// communities (the community is the secret).
var routerOSRules = []maskRule{
	rule(`\b(?:password|secret|passphrase|wpa-pre-shared-key|wpa2-pre-shared-key|authentication-key|authentication-password|encryption-password|private-key|ipsec-secret|pre-shared-key|shared-secret|tcp-md5-key|auth-key)=` + rosValue),
	rule(`^/snmp community (?:add|set) .*?\bname=` + rosValue),
}

// huaweiRules cover display current-configuration: passwords in every
// cipher form, SNMP communities and USM keys, HWTACACS/RADIUS shared keys,
// pre-shared keys and routing authentication keys.
var huaweiRules = []maskRule{
	rule(`\bpassword (?:irreversible-cipher|cipher|simple) ` + value),
	rule(`\bsuper password (?:level \d+ )?(?:irreversible-cipher|cipher|simple) ` + value),
	rule(`\bsnmp-agent community (?:read|write) (?:cipher )?` + value),
	// The security name of a v1/v2c trap target is the community.
	rule(`\bsecurityname (?:cipher )?` + value),
	rule(`\b(?:authentication-mode|privacy-mode) \S+(?: \d+)? (?:cipher|simple|plain) ` + value),
	rule(`\bshared-key (?:cipher |simple )?` + value),
	rule(`\bpre-shared-key (?:cipher |simple )?` + value),
}

var platformRules = map[Platform][]maskRule{
	CiscoIOS:  ciscoRules,
	CiscoNXOS: ciscoRules,
	Junos:     junosRules,
	RouterOS:  routerOSRules,
	HuaweiVRP: huaweiRules,
}

var (
	privateKeyBegin = regexp.MustCompile(`-{5}BEGIN [A-Z0-9 ]*PRIVATE KEY-{5}`)
	privateKeyEnd   = regexp.MustCompile(`-{5}END [A-Z0-9 ]*PRIVATE KEY-{5}`)
)

// Mask replaces every secret value of a normalized configuration of
// platform by Masked and returns the text and the number of replacements
// (masked_count). The lines stay where they are: a private key block keeps
// its BEGIN and END lines, each line between them becomes Masked (one
// replacement per block).
func Mask(platform Platform, text string) (masked string, count int) {
	rules := platformRules[platform]
	lines := strings.Split(text, "\n")
	inKey := false
	parent := ""
	for i, line := range lines {
		switch {
		case inKey:
			if privateKeyEnd.MatchString(line) {
				inKey = false
				continue
			}
			lines[i] = Masked
			continue
		case privateKeyBegin.MatchString(line) && !privateKeyEnd.MatchString(line):
			inKey = true
			count++
			continue
		}
		if line != "" && line[0] != ' ' && line[0] != '\t' {
			parent = line
		}
		var n int
		lines[i], n = maskLine(rules, line, parent)
		count += n
	}
	return strings.Join(lines, "\n"), count
}

// span is a part of a line to replace.
type span struct{ start, end int }

// maskLine applies the rules to one line.
func maskLine(rules []maskRule, line, parent string) (out string, n int) {
	var spans []span
	for i := range rules {
		r := &rules[i]
		if r.parent != nil && !r.parent.MatchString(parent) {
			continue
		}
		if r.skip != nil && r.skip.MatchString(line) {
			continue
		}
		names := r.re.SubexpNames()
		for _, m := range r.re.FindAllStringSubmatchIndex(line, -1) {
			for g, name := range names {
				if name != "v" || m[2*g] < 0 {
					continue
				}
				s := span{m[2*g], m[2*g+1]}
				if line[s.start:s.end] == Masked || overlaps(spans, s) {
					continue
				}
				spans = append(spans, s)
			}
		}
	}
	if len(spans) == 0 {
		return line, 0
	}
	sort.Slice(spans, func(a, b int) bool { return spans[a].start > spans[b].start })
	for _, s := range spans {
		line = line[:s.start] + Masked + line[s.end:]
	}
	return line, len(spans)
}

func overlaps(spans []span, s span) bool {
	for _, x := range spans {
		if s.start < x.end && x.start < s.end {
			return true
		}
	}
	return false
}
