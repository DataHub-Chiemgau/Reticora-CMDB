package security

import (
	"errors"
	"strconv"
	"strings"
	"unicode"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

// Version schemes of the version match (SEC-02, WP-1121, E-236 option 2).
const (
	SchemeGeneric    = "generic"
	SchemeCiscoIOS   = "cisco_ios"
	SchemeCiscoIOSXE = "cisco_ios_xe"
	SchemeCiscoNXOS  = "cisco_nxos"
	SchemeJunos      = "junos"
)

// VersionSchemes lists the version schemes.
var VersionSchemes = []string{SchemeGeneric, SchemeCiscoIOS, SchemeCiscoIOSXE, SchemeCiscoNXOS, SchemeJunos}

// ErrUnparseableVersion: a version the scheme cannot read; it yields no
// finding and is counted (versions_unparseable).
var ErrUnparseableVersion = errors.New("security: unparseable version")

// token is one segment of a version: a number, a word or a separator rank.
type token struct {
	num   int64
	word  string
	isNum bool
}

// preRelease are the words that make a version smaller than the release
// without them (2.0rc1 < 2.0).
var preRelease = map[string]int{"dev": -4, "alpha": -3, "beta": -2, "pre": -1, "rc": -1}

// tokenize splits a version into numbers and words; the separators . - _ (
// ) , and spaces only split.
func tokenize(v string) []token {
	var out []token
	var cur strings.Builder
	mode := 0 // 1 digit, 2 letter
	flush := func() {
		if cur.Len() == 0 {
			return
		}
		s := cur.String()
		cur.Reset()
		if mode == 1 {
			n, err := strconv.ParseInt(s, 10, 64)
			if err != nil {
				n = 1<<62 - 1
			}
			out = append(out, token{num: n, isNum: true})
			return
		}
		out = append(out, token{word: strings.ToLower(s)})
	}
	for _, r := range v {
		switch {
		case unicode.IsDigit(r):
			if mode != 1 {
				flush()
			}
			mode = 1
			cur.WriteRune(r)
		case unicode.IsLetter(r):
			if mode != 2 {
				flush()
			}
			mode = 2
			cur.WriteRune(r)
		default:
			flush()
			mode = 0
		}
	}
	flush()
	return out
}

// compareGeneric compares dotted versions: numbers per segment
// numerically, a missing segment counts as 0 (1.2 = 1.2.0), letters after a
// number are a suffix bigger than none (17.9.4 < 17.9.4a), the pre-release
// words rc, beta, alpha, dev, pre make the version smaller (2.0rc1 < 2.0).
func compareGeneric(a, b []token) int {
	for i := 0; i < len(a) || i < len(b); i++ {
		var x, y *token
		if i < len(a) {
			x = &a[i]
		}
		if i < len(b) {
			y = &b[i]
		}
		if c := compareToken(x, y); c != 0 {
			return c
		}
	}
	return 0
}

// rank of a missing or a word token against a number: a pre-release word is
// below the end of the version, another word (a suffix letter) above it.
func compareToken(x, y *token) int {
	switch {
	case x == nil && y == nil:
		return 0
	case x == nil:
		return -compareToken(y, nil)
	case y == nil:
		if x.isNum {
			if x.num == 0 {
				return 0
			}
			return 1
		}
		if _, pre := preRelease[x.word]; pre {
			return -1
		}
		return 1
	case x.isNum && y.isNum:
		return cmp64(x.num, y.num)
	case x.isNum:
		// A number against a word: a pre-release word is below, a suffix
		// word below the next number as well (17.9.4a < 17.9.5 is decided
		// at the number before).
		if _, pre := preRelease[y.word]; pre {
			return 1
		}
		return 1
	case y.isNum:
		return -compareToken(y, x)
	}
	pa, okA := preRelease[x.word]
	pb, okB := preRelease[y.word]
	switch {
	case okA && okB:
		return cmp64(int64(pa), int64(pb))
	case okA:
		return -1
	case okB:
		return 1
	}
	return strings.Compare(x.word, y.word)
}

func cmp64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// parse reads a version of scheme into comparable tokens; ok is false for a
// version the scheme cannot read.
func parse(scheme, v string) ([]token, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, false
	}
	toks := tokenize(v)
	if len(toks) == 0 || !toks[0].isNum {
		return nil, false
	}
	switch scheme {
	case SchemeCiscoIOS:
		// 15.2(4)M7: major.minor(release)train rebuild.
		if !strings.Contains(v, "(") {
			return nil, false
		}
	case SchemeCiscoNXOS:
		// 9.3(10): major.minor(maintenance).
		if !strings.Contains(v, "(") {
			return nil, false
		}
	case SchemeJunos:
		// 22.4R3-S2: release, R/X level, service release S n.
		if !strings.ContainsAny(strings.ToUpper(v), "RX") {
			return nil, false
		}
		return junosTokens(toks), true
	}
	return toks, true
}

// junosTokens orders Junos versions: 22.4R3 < 22.4R3-S1 < 22.4R3-S2; the
// service release S counts as a suffix above the plain release.
func junosTokens(toks []token) []token {
	out := make([]token, 0, len(toks))
	for _, t := range toks {
		if !t.isNum && (t.word == "r" || t.word == "x") {
			// The R/X level is a separator of the number after it.
			continue
		}
		if !t.isNum && t.word == "s" {
			// S n of a service release: a number one level below.
			out = append(out, token{num: 1, isNum: true})
			continue
		}
		out = append(out, t)
	}
	return out
}

// CompareVersions compares a and b in scheme: -1, 0 or 1; an unparseable
// version is ErrUnparseableVersion.
func CompareVersions(scheme, a, b string) (int, error) {
	ta, okA := parse(scheme, a)
	tb, okB := parse(scheme, b)
	if !okA || !okB {
		return 0, ErrUnparseableVersion
	}
	return compareGeneric(ta, tb), nil
}

// InRange reports whether v lies in the affected range in scheme: an exact
// version, or the interval of start and end with their inclusivity (an
// empty bound is unbounded, a range without either affects every version).
// An unparseable version is ErrUnparseableVersion.
func InRange(scheme, v string, affected *vulnfeed.Affected) (bool, error) {
	if affected.VersionExact != "" {
		c, err := CompareVersions(scheme, v, affected.VersionExact)
		return err == nil && c == 0, err
	}
	if _, ok := parse(scheme, v); !ok {
		return false, ErrUnparseableVersion
	}
	if affected.VersionStart != "" {
		c, err := CompareVersions(scheme, v, affected.VersionStart)
		if err != nil {
			return false, err
		}
		if c < 0 || (c == 0 && !affected.VersionStartInclusive) {
			return false, nil
		}
	}
	if affected.VersionEnd != "" {
		c, err := CompareVersions(scheme, v, affected.VersionEnd)
		if err != nil {
			return false, err
		}
		if c > 0 || (c == 0 && !affected.VersionEndInclusive) {
			return false, nil
		}
	}
	// No exact version and no bound: the range names every version of the
	// product (vulnfeed: an empty bound is unbounded; CSAF products
	// without a version, vers:x/*, a CPE version * without bounds).
	return true, nil
}
