// Package security implements patch posture: findings per CI produced by
// matching discovered/agent-reported software and firmware versions against
// version/CVE feeds (spec §9.4). Findings feed tickets, the compliance score
// and the security report.
//
// A finding carries its origin (source), the CVE fields and a
// deduplication key (fingerprint, unique per organization): a renewed
// report of the same finding updates it instead of adding a row (Upsert,
// SEC-02, WP-1116). Status changes follow fixed transitions
// (CheckTransition); every new finding, status change, reappearance and
// change of severity writes finding.* to the outbox in the same
// transaction (EVT-03).
package security

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Statuses of a finding.
const (
	StatusOpen          = "open"
	StatusAcknowledged  = "acknowledged"
	StatusResolved      = "resolved"
	StatusFalsePositive = "false_positive"
)

// Sources of a finding (SEC-02).
const (
	SourceCVEFeed      = "cve_feed"
	SourceAgent        = "agent"
	SourceCompliance   = "compliance"
	SourceConfigChange = "config_change"
	SourceManual       = "manual"
)

// Sources lists the sources of a finding.
var Sources = []string{SourceCVEFeed, SourceAgent, SourceCompliance, SourceConfigChange, SourceManual}

// Kinds lists the kinds of a finding; unauthorized_change comes from the
// configuration change detection (CMP-01), compliance_violation from the
// compliance rules.
var Kinds = []string{"vulnerability", "outdated_software", "outdated_firmware", "missing_patch",
	"unauthorized_change", "compliance_violation"}

// ReasonReappeared is the status reason of a resolved finding that was
// reported again.
const ReasonReappeared = "reappeared"

// StatusReasonMaxLen is the longest status reason (characters).
const StatusReasonMaxLen = 1000

// transitions are the allowed status changes (AK3 of WP-1116).
var transitions = map[string][]string{
	StatusOpen:          {StatusAcknowledged, StatusFalsePositive, StatusResolved},
	StatusAcknowledged:  {StatusOpen, StatusFalsePositive, StatusResolved},
	StatusFalsePositive: {StatusOpen},
	StatusResolved:      {StatusOpen},
}

// ErrNotFound is returned for a finding that does not exist or is not
// visible in the caller's scope; it is api.ErrNotFound (404).
var ErrNotFound = api.ErrNotFound

// errCINotFound refuses a finding about a CI the caller cannot see.
var errCINotFound = &api.FieldError{Field: "ci_id", Message: "the CI does not exist", Status: 422}

// CheckTransition validates a status change and its reason: 422 on status
// for a transition outside the allowed ones, 422 on status_reason for
// false_positive without a reason or a reason longer than 1,000
// characters.
func CheckTransition(from, to string, reason *string) error {
	if !slices.Contains(transitions[from], to) {
		return &api.FieldError{Field: "status", Message: "the status cannot change from " + from + " to " + to, Status: 422}
	}
	if reason != nil && len([]rune(*reason)) > StatusReasonMaxLen {
		return &api.FieldError{Field: "status_reason", Message: "status_reason is longer than 1000 characters", Status: 422}
	}
	if to == StatusFalsePositive && (reason == nil || strings.TrimSpace(*reason) == "") {
		return &api.FieldError{Field: "status_reason", Message: "false_positive requires a status_reason", Status: 422}
	}
	return nil
}

// Fingerprint returns the deduplication key of a finding: SHA-256 (hex)
// over source|ci_id|kind|key, the key being advisory_id, else reference,
// else package_name and installed_version, else the title.
func Fingerprint(f *Finding) string {
	key := f.AdvisoryID
	switch {
	case key != "":
	case f.Reference != "":
		key = f.Reference
	case f.PackageName != "" || f.InstalledVersion != "":
		key = f.PackageName + "@" + f.InstalledVersion
	default:
		key = "title:" + f.Title
	}
	sum := sha256.Sum256([]byte(f.Source + "|" + f.CIID + "|" + f.Kind + "|" + key))
	return hex.EncodeToString(sum[:])
}

// Finding is one security finding attached to a CI.
type Finding struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	CIID           string `json:"ci_id,omitempty"`
	// ClientID and SiteID are the scope of the finding (those of its CI);
	// they travel in the finding.* events, not in the API answer.
	ClientID         string     `json:"-"`
	SiteID           string     `json:"-"`
	Kind             string     `json:"kind"`     // see Kinds
	Severity         string     `json:"severity"` // low | medium | high | critical
	Title            string     `json:"title"`
	Detail           string     `json:"detail,omitempty"`
	PackageName      string     `json:"package_name,omitempty"`
	InstalledVersion string     `json:"installed_version,omitempty"`
	FixedVersion     string     `json:"fixed_version,omitempty"`
	Reference        string     `json:"reference,omitempty"` // CVE id or advisory URL
	Status           string     `json:"status"`              // open | acknowledged | resolved | false_positive
	StatusReason     string     `json:"status_reason,omitempty"`
	Source           string     `json:"source"` // see Sources
	CVEIDs           []string   `json:"cve_ids"`
	CVSSScore        *float64   `json:"cvss_score,omitempty"`
	CVSSVector       string     `json:"cvss_vector,omitempty"`
	KEV              bool       `json:"kev"`
	AdvisoryID       string     `json:"advisory_id,omitempty"`
	Fingerprint      string     `json:"fingerprint"`
	DetectedAt       time.Time  `json:"detected_at"`
	LastSeenAt       time.Time  `json:"last_seen_at"`
	ResolvedAt       *time.Time `json:"resolved_at,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"updated_at"`
}

// prepare fills the defaults of a reported finding: status open,
// severity medium, source manual, no CVE ids, the fingerprint. The CI id
// is normalized first (lower case, as PostgreSQL returns a uuid), so the
// same CI spelled in upper case yields the same fingerprint.
func prepare(f *Finding) {
	f.CIID = strings.ToLower(strings.TrimSpace(f.CIID))
	if f.Status == "" {
		f.Status = StatusOpen
	}
	if f.Severity == "" {
		f.Severity = "medium"
	}
	if f.Source == "" {
		f.Source = SourceManual
	}
	if f.CVEIDs == nil {
		f.CVEIDs = []string{}
	}
	if f.Fingerprint == "" {
		f.Fingerprint = Fingerprint(f)
	}
}

// UpsertResult says what an Upsert did.
type UpsertResult struct {
	// Created: a new finding.
	Created bool
	// Reappeared: a resolved finding was reported again and is open.
	Reappeared bool
	// SeverityChanged: the severity of the existing finding changed.
	SeverityChanged bool
}

// EventType returns the finding.* type an Upsert writes, or "" for a
// renewed report that changed nothing but last_seen_at and the details.
func (r UpsertResult) EventType() string {
	switch {
	case r.Created:
		return events.FindingCreated
	case r.Reappeared, r.SeverityChanged:
		return events.FindingChanged
	}
	return ""
}

// EventData is the data of the finding.* events: the frame of WP-868
// (events.FindingData) with status, source and CVE ids (the schema allows
// further properties).
type EventData struct {
	events.FindingData
	Status string   `json:"status"`
	Source string   `json:"source"`
	CVEIDs []string `json:"cve_ids"`
}

// eventData returns the event data of a finding.
func eventData(f *Finding) EventData {
	cves := f.CVEIDs
	if cves == nil {
		cves = []string{}
	}
	return EventData{
		FindingData: events.FindingData{ID: f.ID, CIID: f.CIID, ClientID: f.ClientID, SiteID: f.SiteID, Severity: f.Severity},
		Status:      f.Status, Source: f.Source, CVEIDs: cves,
	}
}

// statusEventType returns the event of a status change.
func statusEventType(to string) string {
	if to == StatusResolved {
		return events.FindingResolved
	}
	return events.FindingChanged
}

// CreateFindingRequest is the payload for recording a finding.
type CreateFindingRequest struct {
	CIID             string `json:"ci_id,omitempty"`
	Kind             string `json:"kind"`
	Severity         string `json:"severity,omitempty"`
	Title            string `json:"title"`
	Detail           string `json:"detail,omitempty"`
	PackageName      string `json:"package_name,omitempty"`
	InstalledVersion string `json:"installed_version,omitempty"`
	FixedVersion     string `json:"fixed_version,omitempty"`
	Reference        string `json:"reference,omitempty"`
}

// UpdateFindingRequest transitions a finding's status.
type UpdateFindingRequest struct {
	Status *string `json:"status,omitempty"`
	// StatusReason explains the change; required for false_positive.
	StatusReason *string `json:"status_reason,omitempty"`
}

// FilterParams scopes finding list queries.
type FilterParams struct {
	CIID     string
	Kind     string
	Severity string
	Status   string
	Source   string
	// CVE selects findings whose cve_ids contain it.
	CVE string
}

// errNoStatus refuses a PATCH without status.
var errNoStatus = &api.FieldError{Field: "status", Message: "status is required", Status: 422}

// isFieldError reports whether err is a field error for the client.
func isFieldError(err error) bool {
	var fe *api.FieldError
	return errors.As(err, &fe)
}
