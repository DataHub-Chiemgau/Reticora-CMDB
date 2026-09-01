// Package security implements patch posture: findings per CI produced by
// matching discovered/agent-reported software and firmware versions against
// version/CVE feeds (spec §9.4). Findings feed tickets, the compliance score
// and the security report.
package security

import "time"

// Finding is one security finding attached to a CI.
type Finding struct {
	ID                string     `json:"id"`
	OrganizationID    string     `json:"organization_id"`
	CIID              string     `json:"ci_id,omitempty"`
	Kind              string     `json:"kind"` // vulnerability | outdated_software | outdated_firmware | missing_patch
	Severity          string     `json:"severity"` // low | medium | high | critical
	Title             string     `json:"title"`
	Detail            string     `json:"detail,omitempty"`
	PackageName       string     `json:"package_name,omitempty"`
	InstalledVersion  string     `json:"installed_version,omitempty"`
	FixedVersion      string     `json:"fixed_version,omitempty"`
	Reference         string     `json:"reference,omitempty"` // CVE id or advisory URL
	Status            string     `json:"status"` // open | acknowledged | resolved | false_positive
	DetectedAt        time.Time  `json:"detected_at"`
	ResolvedAt        *time.Time `json:"resolved_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
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
}

// FilterParams scopes finding list queries.
type FilterParams struct {
	CIID     string
	Kind     string
	Severity string
	Status   string
}
