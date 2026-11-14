package alarm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The alarm settings of an organization (ALR-02, ALR-04, ALR-06, ALR-12,
// WP-1030): the correlation window, the flapping parameters, the limit of
// alarm notifications, how unknown counts in availability (E-181) and the
// notification after a maintenance window. One row of alarm_settings per
// organization; without one the Defaults apply.
//
//	GET /api/v1/monitoring/alarm-settings  monitoring:read
//	PUT /api/v1/monitoring/alarm-settings  monitoring:write, organization-wide principals only
//
// The PUT is audited by the route audit with the old and the new row
// (audit.RouteSnapshots, table alarm_settings).

// settingsPath is the path of the settings routes.
const settingsPath = "/api/v1/monitoring/alarm-settings"

// Values of unknown_mode (E-181): unknown is left out of the measured time,
// counts as down or as up.
const (
	UnknownExcluded = "excluded"
	UnknownDown     = "down"
	UnknownUp       = "up"
)

// UnknownModes lists the values of unknown_mode.
var UnknownModes = []string{UnknownExcluded, UnknownDown, UnknownUp}

// Settings are the alarm settings of an organization.
type Settings struct {
	SettleSeconds          int        `json:"settle_seconds"`
	FlapTransitions        int        `json:"flap_transitions"`
	FlapWindowSeconds      int        `json:"flap_window_seconds"`
	FlapHoldSeconds        int        `json:"flap_hold_seconds"`
	NotifyRatePerMin       int        `json:"notify_rate_per_min"`
	UnknownMode            string     `json:"unknown_mode"`
	NotifyAfterMaintenance bool       `json:"notify_after_maintenance"`
	UpdatedAt              *time.Time `json:"updated_at"`
	UpdatedBy              *string    `json:"updated_by"`
}

// Defaults are the settings of an organization without a row (the column
// defaults of alarm_settings).
func Defaults() Settings {
	return Settings{SettleSeconds: 30, FlapTransitions: 5, FlapWindowSeconds: 900, FlapHoldSeconds: 900,
		NotifyRatePerMin: 60, UnknownMode: UnknownExcluded, NotifyAfterMaintenance: true}
}

// settingBounds are the bounds of the numeric settings (the CHECKs of
// alarm_settings).
var settingBounds = []struct {
	field    string
	min, max int
	value    func(s *Settings) int
}{
	{"settle_seconds", 0, 300, func(s *Settings) int { return s.SettleSeconds }},
	{"flap_transitions", 2, 100, func(s *Settings) int { return s.FlapTransitions }},
	{"flap_window_seconds", 60, 86400, func(s *Settings) int { return s.FlapWindowSeconds }},
	{"flap_hold_seconds", 60, 86400, func(s *Settings) int { return s.FlapHoldSeconds }},
	{"notify_rate_per_min", 1, 10000, func(s *Settings) int { return s.NotifyRatePerMin }},
}

// Validate checks the settings against their bounds: 422 with the field.
// validation: field errors with fixed texts.
func (s *Settings) Validate() error {
	for _, b := range settingBounds {
		if v := b.value(s); v < b.min || v > b.max {
			return unprocessable(b.field, fmt.Sprintf("%s must be between %d and %d", b.field, b.min, b.max))
		}
	}
	if !slices.Contains(UnknownModes, s.UnknownMode) {
		return unprocessable("unknown_mode", "unknown_mode must be one of "+strings.Join(UnknownModes, ", "))
	}
	return nil
}

// Settings implements Store.
func (m *MemoryStore) Settings(_ context.Context, orgID string) (Settings, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.settings[orgID]; ok {
		return s, nil
	}
	return Defaults(), nil
}

// PutSettings implements Store.
func (m *MemoryStore) PutSettings(_ context.Context, orgID string, in *Settings, actor *string) (Settings, error) {
	if err := in.Validate(); err != nil {
		return Settings{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s := *in
	at := m.clock()
	s.UpdatedAt, s.UpdatedBy = &at, actor
	if m.settings == nil {
		m.settings = map[string]Settings{}
	}
	m.settings[orgID] = s
	return s, nil
}

// settingsRequest is the body of the PUT: every member optional, an absent
// one takes its default (the PUT replaces the whole settings).
type settingsRequest struct {
	SettleSeconds          *int    `json:"settle_seconds"`
	FlapTransitions        *int    `json:"flap_transitions"`
	FlapWindowSeconds      *int    `json:"flap_window_seconds"`
	FlapHoldSeconds        *int    `json:"flap_hold_seconds"`
	NotifyRatePerMin       *int    `json:"notify_rate_per_min"`
	UnknownMode            *string `json:"unknown_mode"`
	NotifyAfterMaintenance *bool   `json:"notify_after_maintenance"`
}

// settings is the request over the defaults.
func (r *settingsRequest) settings() Settings {
	s := Defaults()
	for _, f := range []struct {
		src *int
		dst *int
	}{
		{r.SettleSeconds, &s.SettleSeconds}, {r.FlapTransitions, &s.FlapTransitions},
		{r.FlapWindowSeconds, &s.FlapWindowSeconds}, {r.FlapHoldSeconds, &s.FlapHoldSeconds},
		{r.NotifyRatePerMin, &s.NotifyRatePerMin},
	} {
		if f.src != nil {
			*f.dst = *f.src
		}
	}
	if r.UnknownMode != nil {
		s.UnknownMode = *r.UnknownMode
	}
	if r.NotifyAfterMaintenance != nil {
		s.NotifyAfterMaintenance = *r.NotifyAfterMaintenance
	}
	return s
}

// msgSettingsOrgWide is the 403 of a principal restricted to clients or
// sites: the settings apply to the whole organization.
const msgSettingsOrgWide = "the alarm settings apply to the whole organization; monitoring:write must be held organization-wide"

// orgWideHolder reports whether the principal holds perm for the whole
// organization (like permission.orgWideHolder): in every dimension of the
// scope of its grant; an API key without resolved scope without a client
// scope.
func orgWideHolder(p *identity.Principal, perm identity.Permission) bool {
	if !p.Has(perm) {
		return false
	}
	if p.Scope == nil {
		_, restricted := p.PermissionScopes[perm]
		return !restricted && strings.TrimSpace(p.ClientScope) == ""
	}
	scope, ok := p.ScopeFor(perm)
	return ok && scope.OrgWide()
}

func (h *Handler) getSettings(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	s, err := h.svc.Store().Settings(r.Context(), org)
	if err != nil {
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

func (h *Handler) putSettings(w http.ResponseWriter, r *http.Request) {
	org, ok := requireOrg(w, r)
	if !ok {
		return
	}
	p, ok := identity.PrincipalFromContext(r.Context())
	if !ok || p.OrganizationID != org || !orgWideHolder(&p, identity.PermMonitoringWrite) {
		api.WriteError(w, http.StatusForbidden, "Forbidden", msgSettingsOrgWide)
		return
	}
	// The body is one JSON object: null (which Decode accepts and which
	// would reset every setting), another value or trailing data is 400.
	const msgBody = "request body must be a JSON object of the alarm settings"
	var req settingsRequest
	body, rerr := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
	if trimmed := bytes.TrimSpace(body); rerr != nil || len(trimmed) == 0 || trimmed[0] != '{' {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", msgBody)
		return
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", msgBody)
		return
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", msgBody)
		return
	}
	in := req.settings()
	if err := in.Validate(); err != nil {
		api.WriteFieldError(w, err)
		return
	}
	var actor *string
	if user := tenant.FromContext(r.Context()).UserID; api.IsUUID(user) {
		actor = &user
	}
	out, err := h.svc.Store().PutSettings(r.Context(), org, &in, actor)
	if err != nil {
		if api.WriteFieldError(w, err) {
			return
		}
		api.WriteRepoError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, out)
}
