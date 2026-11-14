// Package vulnfeed is the global vulnerability catalog of the installation
// (SEC-02, WP-1117, E-234 option 3): the advisories of the CSAF, NVD/KEV
// and offline bundle feeds (WP-1118 to WP-1120) with their affected
// product and version ranges, against which the version match (WP-1121)
// compares the CIs of every organization.
//
// The catalog belongs to no organization (vuln_source, vuln_advisory,
// vuln_affected; rls.SystemCatalogTables): every context reads it, only
// the system context writes it (database.WithSystemWrite). A tenant
// transaction that writes it is refused by row level security.
package vulnfeed

import (
	"strings"
	"time"
	"unicode"
)

// Kinds of a feed source.
const (
	KindCSAF   = "csaf"
	KindNVD    = "nvd"
	KindKEV    = "kev"
	KindBundle = "bundle"
)

// Source is one feed and its sync state (vuln_source). ID is e.g.
// csaf:<provider URL>, nvd, kev or bundle.
type Source struct {
	ID             string
	Kind           string
	URL            string
	ETag           string
	LastModified   string
	Cursor         string
	SyncedAt       *time.Time
	DocumentsTotal int64
	ErrorsTotal    int64
	LastError      string
	UpdatedAt      time.Time
}

// Advisory is one advisory of a source (vuln_advisory); it is replaced as a
// whole with its ranges, there is no history.
type Advisory struct {
	ID          string
	SourceID    string
	CVEIDs      []string
	Title       string
	Severity    string // low | medium | high | critical, empty when unknown
	CVSSScore   *float64
	CVSSVector  string
	KEV         bool
	PublishedAt *time.Time
	ModifiedAt  *time.Time
	URL         string
	Withdrawn   bool
}

// Affected is one affected range of an advisory for a vendor and product
// (vuln_affected). Vendor and product are normalized (Normalize). A range
// is either one exact version or a start and an end, each inclusive or
// exclusive; empty means unbounded.
type Affected struct {
	ID                    int64
	AdvisoryID            string
	Vendor                string
	Product               string
	VersionScheme         string
	VersionExact          string
	VersionStart          string
	VersionStartInclusive bool
	VersionEnd            string
	VersionEndInclusive   bool
	FixedVersion          string
	CPE                   string
}

// Match is one affected range with its advisory (ByProducts).
type Match struct {
	Advisory Advisory
	Affected Affected
}

// Product is a vendor and a product of the catalog (SearchProducts).
type Product struct {
	Vendor  string
	Product string
}

// Normalize is the stored form of a vendor or product: lower case, every
// run of white space one _, trimmed.
func Normalize(s string) string {
	fields := strings.FieldsFunc(strings.ToLower(s), unicode.IsSpace)
	return strings.Join(fields, "_")
}
