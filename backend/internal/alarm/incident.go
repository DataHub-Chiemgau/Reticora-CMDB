package alarm

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// The incident (ALR-03, ALR-09, E-196 option 2, WP-1027) bundles correlated
// alarms: one root alarm with its CI, the symptoms, the probable cause with
// class, confidence and reasons, and its state. Every change appends to the
// append-only log incident_transition (migration 000326). Correlation and
// the events incident.* follow in WP-1028 and WP-1036.

// States of an incident.
const (
	IncidentOpen         = "open"
	IncidentAcknowledged = "acknowledged"
	IncidentResolved     = "resolved"
)

// IncidentStates lists the states of an incident.
var IncidentStates = []string{IncidentOpen, IncidentAcknowledged, IncidentResolved}

// Cause classes of an incident (ALR-09); WP-1041 adds further classes.
const (
	CauseTopology             = "topology"
	CauseCollectorUnreachable = "collector_unreachable"
	CauseUnknown              = "unknown"
)

// CauseClasses lists the cause classes of the CHECK constraint.
var CauseClasses = []string{CauseTopology, CauseCollectorUnreachable, CauseUnknown}

// Roles of an alarm in an incident (AttachAlarms).
const (
	RoleRoot    = "root"
	RoleSymptom = "symptom"
)

// Reasons of the incident transitions.
const (
	IncidentReasonOpened         = "opened"
	IncidentReasonUpdated        = "updated"
	IncidentReasonAlarmsAttached = "alarms_attached"
	IncidentReasonRootChanged    = "root_changed"
	IncidentReasonMergedInto     = "merged_into"
	IncidentReasonAbsorbed       = "absorbed"
	IncidentReasonAcknowledged   = "acknowledged"
	IncidentReasonUnacknowledged = "unacknowledged"
	IncidentReasonResolved       = "resolved"
)

// ReasonsMaxLines is the largest number of reason lines an incident takes.
const ReasonsMaxLines = 50

var (
	// ErrIncidentNotFound is returned when an incident does not exist or is
	// not visible in the caller's scope.
	ErrIncidentNotFound = errors.New("incident not found")
	// ErrManualRoot is returned when an automatic root change meets a root
	// set by hand (manual_root); nothing is written.
	ErrManualRoot = errors.New("incident root was set manually")
	// ErrIncidentResolved is returned for a change of a resolved incident.
	ErrIncidentResolved = errors.New("incident is resolved")
	// ErrAlarmInOtherIncident is returned when AttachAlarms, SetRoot or the
	// root of MergeIncidents names an alarm of another incident; an alarm
	// changes its incident only through MergeIncidents. Nothing is written.
	ErrAlarmInOtherIncident = errors.New("alarm belongs to another incident")
	// ErrMergeScopeRestricted is returned when MergeIncidents runs in a
	// client or site scope: alarms of absorb outside that scope could not
	// be moved, so the merge needs the organization-wide scope. Nothing is
	// written.
	ErrMergeScopeRestricted = errors.New("merging incidents needs the organization-wide scope")
)

// Incident is one incident.
type Incident struct {
	ID             string  `json:"id"`
	OrganizationID string  `json:"organization_id"`
	ClientID       *string `json:"client_id"`
	SiteID         *string `json:"site_id"`
	RootAlarmID    *string `json:"root_alarm_id"`
	RootCIID       *string `json:"root_ci_id"`

	CauseClass string   `json:"cause_class"`
	Confidence int      `json:"confidence"`
	Reasons    []string `json:"reasons"`

	State          string     `json:"state"`
	OpenedAt       time.Time  `json:"opened_at"`
	AcknowledgedAt *time.Time `json:"acknowledged_at"`
	// AcknowledgedBy is the actor id without foreign key (Konvention 16).
	AcknowledgedBy *string        `json:"acknowledged_by"`
	ResolvedAt     *time.Time     `json:"resolved_at"`
	ManualRoot     bool           `json:"manual_root"`
	AffectedCounts map[string]int `json:"affected_counts"`
	UpdatedAt      time.Time      `json:"updated_at"`
}

// IncidentTransition is one entry of the append-only log of an incident.
type IncidentTransition struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	ClientID       *string   `json:"client_id"`
	SiteID         *string   `json:"site_id"`
	IncidentID     string    `json:"incident_id"`
	FromState      *string   `json:"from_state"`
	ToState        string    `json:"to_state"`
	At             time.Time `json:"at"`
	// ActorID is the acting user; nil is the system.
	ActorID *string        `json:"actor_id"`
	Reason  string         `json:"reason"`
	Details map[string]any `json:"details"`
}

// IncidentInput creates an incident (ID empty) or updates the cause of an
// incident that is not resolved (UpsertIncident).
type IncidentInput struct {
	// ID names the incident to update; empty creates one.
	ID string
	// RootCIID is the CI of the root of a new incident; client and site
	// then follow the CI. Ignored on an update (SetRoot changes the root).
	RootCIID *string
	// ClientID and SiteID scope a new incident without root CI.
	ClientID, SiteID *string
	CauseClass       string
	Confidence       int
	Reasons          []string
	AffectedCounts   map[string]int
	// At is when the incident opened or changed; zero is now.
	At time.Time
}

// IncidentFilter selects incidents (ListIncidents). Empty fields do not
// filter.
type IncidentFilter struct {
	States       []string
	ClientID     string
	CauseClasses []string
	// From and To select the incidents active in the period: opened before
	// To and not resolved before From.
	From, To *time.Time
	After    *IncidentCursor
	// Limit is the page size; 0 or less is DefaultLimit.
	Limit int
}

// IncidentCursor is the position of an incident in the list order: newest
// first on (opened_at, id).
type IncidentCursor struct {
	OpenedAt time.Time
	ID       string
}

// IncidentPage is one page of incidents; Next is set when more follow.
type IncidentPage struct {
	Items []Incident
	Next  *IncidentCursor
}

// ValidateIncident checks the input of UpsertIncident against the value
// sets of the database, so both stores refuse the same input.
func ValidateIncident(in *IncidentInput) error {
	switch {
	case in.CauseClass != "" && !slices.Contains(CauseClasses, in.CauseClass):
		return fmt.Errorf("alarm: unknown cause class %q", in.CauseClass)
	case in.Confidence < 0 || in.Confidence > 100:
		return fmt.Errorf("alarm: confidence %d outside 0–100", in.Confidence)
	case len(in.Reasons) > ReasonsMaxLines:
		return fmt.Errorf("alarm: more than %d reason lines", ReasonsMaxLines)
	}
	return nil
}

// incidentTransitionAllowed reports whether an incident may move from from
// to to (TransitionIncident).
func incidentTransitionAllowed(from, to string) bool {
	switch from {
	case IncidentOpen:
		return to == IncidentAcknowledged || to == IncidentResolved
	case IncidentAcknowledged:
		return to == IncidentOpen || to == IncidentResolved
	}
	return false
}

// incidentReason is the reason of a state change to to.
func incidentReason(from, to string) string {
	switch {
	case to == IncidentAcknowledged:
		return IncidentReasonAcknowledged
	case to == IncidentResolved:
		return IncidentReasonResolved
	case from == IncidentAcknowledged:
		return IncidentReasonUnacknowledged
	}
	return IncidentReasonUpdated
}

func causeOrUnknown(c string) string {
	if c == "" {
		return CauseUnknown
	}
	return c
}

func incidentLimitOf(f *IncidentFilter) int {
	if f.Limit <= 0 {
		return DefaultLimit
	}
	return f.Limit
}

// pagedIncidents cuts items (at most limit+1) to the page.
func pagedIncidents(items []Incident, limit int) IncidentPage {
	if len(items) <= limit {
		return IncidentPage{Items: items}
	}
	last := items[limit-1]
	return IncidentPage{Items: items[:limit], Next: &IncidentCursor{OpenedAt: last.OpenedAt, ID: last.ID}}
}

// uniqueIDs returns ids without duplicates, in their order.
func uniqueIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}
