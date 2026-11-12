// Package configsnap stores the versions of the device configurations
// (ALR-07, E-207 option 2): per CI the hash of the normalized, masked
// running configuration and its text, gzip-compressed, at most 2 MiB, kept
// 400 days (retention.config_snapshot in package jobs). A version is
// stored only when its hash differs from the latest version of the CI; the
// collector (WP-1053) masks secrets before hashing and sending, the ingest
// (WP-1054) writes through InsertIfChanged, the version and diff API
// follows in WP-1056.
package configsnap

import (
	"context"
	"errors"
	"time"
)

// MaxSize is the largest configuration a version takes, uncompressed and
// compressed (2 MiB).
const MaxSize = 2 << 20

// Sources of a configuration.
const (
	SourceSSH  = "ssh"
	SourceGNMI = "gnmi"
)

// Triggers of a collection.
const (
	TriggerSchedule = "schedule"
	TriggerSNMP     = "snmp"
	TriggerTrap     = "trap"
	TriggerGNMI     = "gnmi"
)

// Errors of the store.
var (
	// ErrNotFound: the version does not exist or is not visible.
	ErrNotFound = errors.New("configuration version not found")
	// ErrCINotFound: the CI does not exist or is not visible in the
	// caller's scope.
	ErrCINotFound = errors.New("ci not found")
	// ErrTooLarge: the configuration exceeds MaxSize.
	ErrTooLarge = errors.New("configuration larger than 2 MiB")
	// ErrHashMismatch: the given SHA-256 is not the hash of the content.
	ErrHashMismatch = errors.New("sha256 does not match the content")
	// ErrInvalid: a field is outside its value set (source, trigger,
	// platform, masked_count) or the content is empty.
	ErrInvalid = errors.New("invalid configuration version")
	// ErrOutOfOrder: the version was collected at or before the latest
	// stored version of the CI with a different hash (a late delivery,
	// e.g. a buffer replayed after a collector outage). It is not stored:
	// it is not the current configuration, and comparing it with the
	// latest one would report a change that did not happen.
	ErrOutOfOrder = errors.New("configuration version older than the latest one")
)

// Snapshot is one version of the configuration of a CI.
type Snapshot struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organization_id"`
	ClientID       *string `json:"client_id"`
	SiteID         *string `json:"site_id"`
	CIID           string  `json:"ci_id"`
	// CollectorID is the collecting collector, without foreign key.
	CollectorID *string   `json:"collector_id"`
	CollectedAt time.Time `json:"collected_at"`
	ReceivedAt  time.Time `json:"received_at"`
	Source      string    `json:"source"`
	Trigger     string    `json:"trigger"`
	Platform    string    `json:"platform"`
	// SHA256 is the hex hash of Content; empty on InsertIfChanged computes
	// it.
	SHA256      string `json:"sha256"`
	SizeBytes   int    `json:"size_bytes"`
	MaskedCount int    `json:"masked_count"`
	// Content is the masked configuration text, uncompressed; set by Get
	// and on InsertIfChanged, empty in lists.
	Content []byte `json:"-"`
}

// Cursor is the position of a version in the list order: newest first on
// (collected_at, id).
type Cursor struct {
	CollectedAt time.Time
	ID          string
}

// Page is one page of versions without content; Next is set when more
// follow.
type Page struct {
	Items []Snapshot
	Next  *Cursor
}

// DefaultLimit is the page size of List without a limit.
const DefaultLimit = 50

// Store is the port of the configuration versions. Every method runs in
// the transaction the context carries (database.ContextWithTx), or opens
// one of the context's tenant scope.
type Store interface {
	// InsertIfChanged takes an advisory lock per CI in the transaction,
	// compares the hash with the latest version of the CI and stores snap
	// only when it differs. created reports a new row: stored is then the
	// new version and previous the former latest one (nil for the first);
	// otherwise stored is the unchanged latest version and previous nil.
	// A differing version collected at or before the latest one is
	// ErrOutOfOrder.
	InsertIfChanged(ctx context.Context, orgID string, snap *Snapshot) (stored, previous *Snapshot, created bool, err error)
	// Latest returns the latest version of the CI without content, or
	// ErrNotFound.
	Latest(ctx context.Context, orgID, ciID string) (*Snapshot, error)
	// List returns the versions of the CI newest first, without content.
	List(ctx context.Context, orgID, ciID string, after *Cursor, limit int) (Page, error)
	// Get returns one version with its decompressed content, or
	// ErrNotFound.
	Get(ctx context.Context, orgID, id string) (*Snapshot, error)
}
