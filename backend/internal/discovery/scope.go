package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/netip"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// Discovery scopes (DIS-09): what one collector discovers – a CIDR, a host
// or an address range of a client (and site) in a VRF, on a schedule, with
// credentials and plugins. The collector receives its enabled scopes over
// NATS; the run result is written back.

// Scope kinds.
const (
	ScopeKindCIDR  = "cidr"
	ScopeKindHost  = "host"
	ScopeKindRange = "range"
)

// MaxScopeAddresses bounds the addresses of one CIDR or range scope.
const MaxScopeAddresses = 65536

// DefaultScopeSchedule is the sweep interval of a new scope (DIS-10).
const DefaultScopeSchedule = "24h"

// MinScopeInterval is the shortest interval a scope may use (DIS-10).
const MinScopeInterval = 5 * time.Minute

// Default intervals (DIS-10): sweep 24 h, polls 15 min; polls never more
// often than every 5 min.
const (
	DefaultSweepInterval = 24 * time.Hour
	DefaultPollInterval  = 15 * time.Minute
	MinPollInterval      = 5 * time.Minute
)

// DiscoveryIntervals are the organization's intervals (organization
// settings, key "discovery": sweep_interval, poll_interval and
// poll_intervals per protocol). A scope's schedule replaces the sweep
// interval for that scope.
type DiscoveryIntervals struct {
	Sweep     string            `json:"sweep"`
	Poll      string            `json:"poll"`
	Protocols map[string]string `json:"protocols,omitempty"`
}

// IntervalsFromSettings reads the discovery intervals of the organization
// settings; missing or invalid values use the defaults, poll intervals
// below MinPollInterval are raised to it.
func IntervalsFromSettings(settings map[string]any) DiscoveryIntervals {
	out := DiscoveryIntervals{Sweep: DefaultSweepInterval.String(), Poll: DefaultPollInterval.String(), Protocols: map[string]string{}}
	d, _ := settings["discovery"].(map[string]any)
	parse := func(v any, fallback, minimum time.Duration) string {
		str, _ := v.(string)
		dur, err := time.ParseDuration(strings.TrimSpace(str))
		if err != nil || dur <= 0 {
			dur = fallback
		}
		if dur < minimum {
			dur = minimum
		}
		return dur.String()
	}
	out.Sweep = parse(d["sweep_interval"], DefaultSweepInterval, MinScopeInterval)
	out.Poll = parse(d["poll_interval"], DefaultPollInterval, MinPollInterval)
	if per, ok := d["poll_intervals"].(map[string]any); ok {
		for proto, v := range per {
			if slices.Contains(ScopePlugins, proto) && proto != "sweep" {
				out.Protocols[proto] = parse(v, DefaultPollInterval, MinPollInterval)
			}
		}
	}
	return out
}

// ScopePlugins are the protocols a scope may name.
var ScopePlugins = []string{"sweep", "snmp", "ssh", "wmi", "ipmi", "redfish", "power", "nas"}

// DefaultScopePlugins are the plugins of a new scope.
var DefaultScopePlugins = []string{"sweep", "snmp"}

// Scope is a discovery scope.
type Scope struct {
	ID             string         `json:"id"`
	OrganizationID string         `json:"organization_id"`
	ClientID       string         `json:"client_id,omitempty"`
	SiteID         string         `json:"site_id,omitempty"`
	CollectorID    string         `json:"collector_id"`
	Name           string         `json:"name"`
	Kind           string         `json:"kind"`
	Target         string         `json:"target"`
	VRFID          string         `json:"vrf_id,omitempty"`
	Schedule       string         `json:"schedule"`
	CredentialIDs  []string       `json:"credential_ids"`
	Plugins        []string       `json:"plugins"`
	Enabled        bool           `json:"enabled"`
	LastRunAt      string         `json:"last_run_at,omitempty"`
	NextRunAt      string         `json:"next_run_at,omitempty"`
	LastResult     map[string]any `json:"last_result,omitempty"`
	// Health is the state of the last run with the causes of its errors
	// (DIS-08).
	Health    ScopeHealth `json:"health"`
	CreatedAt string      `json:"created_at"`
	UpdatedAt string      `json:"updated_at"`
}

// ScopeInput is the body of POST /discovery-scopes.
type ScopeInput struct {
	CollectorID   string   `json:"collector_id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	VRFID         string   `json:"vrf_id,omitempty"`
	Schedule      string   `json:"schedule,omitempty"`
	CredentialIDs []string `json:"credential_ids,omitempty"`
	Plugins       []string `json:"plugins,omitempty"`
	Enabled       *bool    `json:"enabled,omitempty"`
}

// ScopePatch is the body of PATCH /discovery-scopes/{id}; nil fields stay.
type ScopePatch struct {
	Name          *string   `json:"name,omitempty"`
	Kind          *string   `json:"kind,omitempty"`
	Target        *string   `json:"target,omitempty"`
	SiteID        *string   `json:"site_id,omitempty"`
	VRFID         *string   `json:"vrf_id,omitempty"`
	Schedule      *string   `json:"schedule,omitempty"`
	CredentialIDs *[]string `json:"credential_ids,omitempty"`
	Plugins       *[]string `json:"plugins,omitempty"`
	Enabled       *bool     `json:"enabled,omitempty"`
}

// ScopeFilter selects scopes.
type ScopeFilter struct {
	CollectorID string
	EnabledOnly bool
}

// ScopeRun is the result of one run of a scope, reported by the collector.
type ScopeRun struct {
	At      time.Time      `json:"at"`
	Result  map[string]any `json:"result"`
	NextRun time.Time      `json:"next_run"`
}

// ScopeRepository stores discovery scopes.
type ScopeRepository interface {
	CreateScope(ctx context.Context, s *Scope) error
	ListScopes(ctx context.Context, orgID string, f ScopeFilter, page api.PaginationParams) ([]Scope, int, error)
	GetScope(ctx context.Context, orgID, id string) (*Scope, error)
	UpdateScope(ctx context.Context, orgID, id string, patch *ScopePatch) (*Scope, error)
	DeleteScope(ctx context.Context, orgID, id string) error
	RecordScopeRun(ctx context.Context, orgID, id string, run *ScopeRun) error
	// ScopeCollector returns a collector of the organization.
	ScopeCollector(ctx context.Context, orgID, collectorID string) (*Collector, error)
	// DiscoveryIntervals returns the organization's intervals (DIS-10).
	DiscoveryIntervals(ctx context.Context, orgID string) (DiscoveryIntervals, error)
}

func fieldError(field, msg string) error {
	return &api.FieldError{Field: field, Message: msg, Status: http.StatusUnprocessableEntity}
}

// NewScope builds a scope from the input with the defaults applied.
func NewScope(orgID string, in *ScopeInput) *Scope {
	s := &Scope{
		OrganizationID: orgID, CollectorID: in.CollectorID, Name: strings.TrimSpace(in.Name),
		Kind: strings.ToLower(strings.TrimSpace(in.Kind)), Target: strings.TrimSpace(in.Target),
		ClientID: in.ClientID, SiteID: in.SiteID, VRFID: in.VRFID, Schedule: strings.TrimSpace(in.Schedule),
		CredentialIDs: in.CredentialIDs, Plugins: in.Plugins, Enabled: in.Enabled == nil || *in.Enabled,
	}
	if s.Schedule == "" {
		s.Schedule = DefaultScopeSchedule
	}
	if len(s.Plugins) == 0 {
		s.Plugins = slices.Clone(DefaultScopePlugins)
	}
	if s.CredentialIDs == nil {
		s.CredentialIDs = []string{}
	}
	if s.Name == "" {
		s.Name = s.Target
	}
	return s
}

// Validate checks a scope (DIS-09).
func (s *Scope) Validate() error {
	for field, id := range map[string]string{"collector_id": s.CollectorID, "client_id": s.ClientID, "site_id": s.SiteID, "vrf_id": s.VRFID} {
		if (id != "" || field == "collector_id") && !audit.IsUUID(id) {
			return fieldError(field, "must be a UUID")
		}
	}
	if s.Name == "" {
		return fieldError("name", "is required")
	}
	target, err := NormalizeScopeTarget(s.Kind, s.Target)
	if err != nil {
		return err
	}
	s.Target = target
	if serr := ValidateSchedule(s.Schedule); serr != nil {
		return serr
	}
	if len(s.Plugins) == 0 {
		return fieldError("plugins", "at least one plugin is required")
	}
	seen := map[string]bool{}
	for _, p := range s.Plugins {
		if !slices.Contains(ScopePlugins, p) {
			return fieldError("plugins", fmt.Sprintf("unknown plugin %q", p))
		}
		if seen[p] {
			return fieldError("plugins", fmt.Sprintf("plugin %q is listed twice", p))
		}
		seen[p] = true
	}
	for _, id := range s.CredentialIDs {
		if !audit.IsUUID(id) {
			return fieldError("credential_ids", "must be UUIDs")
		}
	}
	return nil
}

// NormalizeScopeTarget validates the target of a scope kind and returns
// its canonical form.
func NormalizeScopeTarget(kind, target string) (string, error) {
	target = strings.TrimSpace(target)
	switch kind {
	case ScopeKindCIDR:
		p, err := netip.ParsePrefix(target)
		if err != nil {
			return "", fieldError("target", "not a CIDR")
		}
		p = p.Masked()
		if hostBits := p.Addr().BitLen() - p.Bits(); hostBits > 16 {
			return "", fieldError("target", fmt.Sprintf("the prefix is larger than %d addresses", MaxScopeAddresses))
		}
		return p.String(), nil
	case ScopeKindHost:
		if a, err := netip.ParseAddr(target); err == nil {
			return a.String(), nil
		}
		if !hostnamePattern.MatchString(target) {
			return "", fieldError("target", "not an IP address or host name")
		}
		return strings.ToLower(target), nil
	case ScopeKindRange:
		from, to, ok := strings.Cut(target, "-")
		a, errA := netip.ParseAddr(strings.TrimSpace(from))
		b, errB := netip.ParseAddr(strings.TrimSpace(to))
		if !ok || errA != nil || errB != nil || a.BitLen() != b.BitLen() || b.Less(a) {
			return "", fieldError("target", "not an address range (first-last)")
		}
		size := new(big.Int).Sub(new(big.Int).SetBytes(b.AsSlice()), new(big.Int).SetBytes(a.AsSlice()))
		if size.Cmp(big.NewInt(MaxScopeAddresses-1)) > 0 {
			return "", fieldError("target", fmt.Sprintf("the range is larger than %d addresses", MaxScopeAddresses))
		}
		return a.String() + "-" + b.String(), nil
	}
	return "", fieldError("kind", "must be cidr, host or range")
}

var hostnamePattern = regexp.MustCompile(`^(?i)[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// ValidateSchedule accepts an interval ("15m", "24h"; at least
// MinScopeInterval) or a five-field cron expression.
func ValidateSchedule(schedule string) error {
	if d, err := time.ParseDuration(schedule); err == nil {
		if d < MinScopeInterval {
			return fieldError("schedule", "the interval must be at least 5m")
		}
		return nil
	}
	fields := strings.Fields(schedule)
	if len(fields) != 5 {
		return fieldError("schedule", "an interval (e.g. 24h) or a cron expression with five fields")
	}
	bounds := [5][2]int{{0, 59}, {0, 23}, {1, 31}, {1, 12}, {0, 7}}
	for i, f := range fields {
		if !cronFieldValid(f, bounds[i][0], bounds[i][1]) {
			return fieldError("schedule", fmt.Sprintf("cron field %d (%q) is invalid", i+1, f))
		}
	}
	return nil
}

func cronFieldValid(field string, lo, hi int) bool {
	for _, part := range strings.Split(field, ",") {
		base, step, hasStep := strings.Cut(part, "/")
		if hasStep {
			if n, err := strconv.Atoi(step); err != nil || n < 1 || n > hi {
				return false
			}
		}
		if base == "*" {
			continue
		}
		from, to, isRange := strings.Cut(base, "-")
		a, err := strconv.Atoi(from)
		if err != nil || a < lo || a > hi {
			return false
		}
		if isRange {
			b, berr := strconv.Atoi(to)
			if berr != nil || b < a || b > hi {
				return false
			}
		} else if hasStep {
			return false
		}
	}
	return true
}

func (p *ScopePatch) apply(s *Scope) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = strings.TrimSpace(*v)
		}
	}
	set(&s.Name, p.Name)
	set(&s.Kind, p.Kind)
	set(&s.Target, p.Target)
	set(&s.SiteID, p.SiteID)
	set(&s.VRFID, p.VRFID)
	set(&s.Schedule, p.Schedule)
	if p.CredentialIDs != nil {
		s.CredentialIDs = *p.CredentialIDs
	}
	if p.Plugins != nil {
		s.Plugins = *p.Plugins
	}
	if p.Enabled != nil {
		s.Enabled = *p.Enabled
	}
	s.Kind = strings.ToLower(s.Kind)
}

// --- PostgreSQL --------------------------------------------------------

const scopeColumns = `id, organization_id, COALESCE(client_id::text, ''), COALESCE(site_id::text, ''), collector_id,
	name, kind, target, COALESCE(vrf_id::text, ''), schedule, credential_ids::text[], plugins, enabled,
	last_run_at, next_run_at, last_result, created_at, updated_at`

func scanScope(row pgx.Row) (*Scope, error) {
	var s Scope
	var lastRun, nextRun *time.Time
	var created, updated time.Time
	var result []byte
	if err := row.Scan(&s.ID, &s.OrganizationID, &s.ClientID, &s.SiteID, &s.CollectorID, &s.Name, &s.Kind, &s.Target,
		&s.VRFID, &s.Schedule, &s.CredentialIDs, &s.Plugins, &s.Enabled, &lastRun, &nextRun, &result, &created, &updated); err != nil {
		return nil, err
	}
	if lastRun != nil {
		s.LastRunAt = lastRun.UTC().Format(time.RFC3339)
	}
	if nextRun != nil {
		s.NextRunAt = nextRun.UTC().Format(time.RFC3339)
	}
	if len(result) > 0 {
		_ = json.Unmarshal(result, &s.LastResult)
	}
	s.CreatedAt, s.UpdatedAt = created.UTC().Format(time.RFC3339), updated.UTC().Format(time.RFC3339)
	if s.CredentialIDs == nil {
		s.CredentialIDs = []string{}
	}
	s.Health = ScopeHealthOf(&s)
	return &s, nil
}

func scopeError(err error) error {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return err
	}
	switch pgErr.ConstraintName {
	case "discovery_scope_collector_id_fkey":
		return fieldError("collector_id", "collector not found")
	case "discovery_scope_site_id_fkey":
		return fieldError("site_id", "site not found")
	case "discovery_scope_client_id_fkey":
		return fieldError("client_id", "client not found")
	case "discovery_scope_vrf_fkey":
		return fieldError("vrf_id", "VRF not found")
	case "discovery_scope_site_client":
		return fieldError("site_id", "the site belongs to another client")
	case "discovery_scope_collector_client":
		return fieldError("client_id", "the collector belongs to another client")
	case "discovery_scope_unique_target":
		return &api.FieldError{Field: "target", Message: "the collector already has a scope for this target", Status: http.StatusConflict}
	}
	return err
}

// checkCredentials verifies that the credentials belong to the organization.
func checkCredentials(ctx context.Context, tx pgx.Tx, orgID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM credential WHERE organization_id = $1 AND id = ANY($2::uuid[])`, orgID, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return fieldError("credential_ids", "unknown credential")
	}
	return nil
}

// CreateScope implements ScopeRepository.
func (r *PGRepository) CreateScope(ctx context.Context, s *Scope) error {
	return database.WithRequestTenant(ctx, r.pool, s.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		if err := checkCredentials(ctx, tx, s.OrganizationID, s.CredentialIDs); err != nil {
			return err
		}
		out, err := scanScope(tx.QueryRow(ctx, `INSERT INTO discovery_scope (organization_id, client_id, site_id, collector_id,
				name, kind, target, vrf_id, schedule, credential_ids, plugins, enabled, next_run_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10::uuid[], $11, $12, now())
			RETURNING `+scopeColumns,
			s.OrganizationID, nilIfEmpty(s.ClientID), nilIfEmpty(s.SiteID), s.CollectorID, s.Name, s.Kind, s.Target,
			nilIfEmpty(s.VRFID), s.Schedule, s.CredentialIDs, s.Plugins, s.Enabled))
		if err != nil {
			return scopeError(err)
		}
		*s = *out
		return nil
	})
}

// ListScopes implements ScopeRepository.
func (r *PGRepository) ListScopes(ctx context.Context, orgID string, f ScopeFilter, page api.PaginationParams) ([]Scope, int, error) {
	var out []Scope
	var total int
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		where := `organization_id = $1 AND ($2 = '' OR collector_id::text = $2) AND (NOT $3 OR enabled)`
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM discovery_scope WHERE `+where, orgID, f.CollectorID, f.EnabledOnly).Scan(&total); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT `+scopeColumns+` FROM discovery_scope WHERE `+where+
			` ORDER BY name, id LIMIT $4 OFFSET $5`, orgID, f.CollectorID, f.EnabledOnly, page.Limit, page.Offset)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			s, serr := scanScope(rows)
			if serr != nil {
				return serr
			}
			out = append(out, *s)
		}
		return rows.Err()
	})
	if out == nil {
		out = []Scope{}
	}
	return out, total, err
}

// GetScope implements ScopeRepository.
func (r *PGRepository) GetScope(ctx context.Context, orgID, id string) (*Scope, error) {
	if !audit.IsUUID(id) {
		return nil, fmt.Errorf("not found")
	}
	var out *Scope
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		s, err := scanScope(tx.QueryRow(ctx, `SELECT `+scopeColumns+` FROM discovery_scope WHERE id = $1 AND organization_id = $2`, id, orgID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("not found")
		}
		out = s
		return err
	})
	return out, err
}

// UpdateScope implements ScopeRepository.
func (r *PGRepository) UpdateScope(ctx context.Context, orgID, id string, patch *ScopePatch) (*Scope, error) {
	if !audit.IsUUID(id) {
		return nil, fmt.Errorf("not found")
	}
	var out *Scope
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		current, err := scanScope(tx.QueryRow(ctx, `SELECT `+scopeColumns+` FROM discovery_scope WHERE id = $1 AND organization_id = $2 FOR UPDATE`, id, orgID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("not found")
		}
		if err != nil {
			return err
		}
		patch.apply(current)
		if verr := current.Validate(); verr != nil {
			return verr
		}
		if cerr := checkCredentials(ctx, tx, orgID, current.CredentialIDs); cerr != nil {
			return cerr
		}
		out, err = scanScope(tx.QueryRow(ctx, `UPDATE discovery_scope SET name = $3, kind = $4, target = $5, site_id = $6,
				vrf_id = $7, schedule = $8, credential_ids = $9::uuid[], plugins = $10, enabled = $11
			WHERE id = $1 AND organization_id = $2 RETURNING `+scopeColumns,
			id, orgID, current.Name, current.Kind, current.Target, nilIfEmpty(current.SiteID), nilIfEmpty(current.VRFID),
			current.Schedule, current.CredentialIDs, current.Plugins, current.Enabled))
		return scopeError(err)
	})
	return out, err
}

// DeleteScope implements ScopeRepository.
func (r *PGRepository) DeleteScope(ctx context.Context, orgID, id string) error {
	if !audit.IsUUID(id) {
		return fmt.Errorf("not found")
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `DELETE FROM discovery_scope WHERE id = $1 AND organization_id = $2`, id, orgID)
		if err == nil && tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return err
	})
}

// RecordScopeRun implements ScopeRepository.
func (r *PGRepository) RecordScopeRun(ctx context.Context, orgID, id string, run *ScopeRun) error {
	result, err := json.Marshal(run.Result)
	if err != nil {
		return err
	}
	return database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE discovery_scope SET last_run_at = $3, last_result = $4, next_run_at = $5
			WHERE id = $1 AND organization_id = $2`, id, orgID, run.At, result, nilIfZero(run.NextRun))
		if err == nil && tag.RowsAffected() == 0 {
			return fmt.Errorf("not found")
		}
		return err
	})
}

func nilIfZero(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// ScopeCollector implements ScopeRepository.
func (r *PGRepository) ScopeCollector(ctx context.Context, orgID, collectorID string) (*Collector, error) {
	if !audit.IsUUID(collectorID) {
		return nil, fmt.Errorf("not found")
	}
	var out *Collector
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		c, err := scanCollector(tx.QueryRow(ctx, `SELECT `+collectorSelectColumns+` FROM collector WHERE id = $1 AND organization_id = $2`, collectorID, orgID))
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("not found")
		}
		out = c
		return err
	})
	return out, err
}

// DiscoveryIntervals implements ScopeRepository.
func (r *PGRepository) DiscoveryIntervals(ctx context.Context, orgID string) (DiscoveryIntervals, error) {
	var settings map[string]any
	err := database.WithRequestTenant(ctx, r.pool, orgID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT settings FROM organization WHERE id = $1`, orgID).Scan(&settings)
	})
	if err != nil {
		return DiscoveryIntervals{}, err
	}
	return IntervalsFromSettings(settings), nil
}

// --- memory --------------------------------------------------------------

func (r *MemoryRepository) scopeMap() map[string]*Scope {
	if r.scopes == nil {
		r.scopes = map[string]*Scope{}
	}
	return r.scopes
}

// CreateScope implements ScopeRepository.
func (r *MemoryRepository) CreateScope(_ context.Context, s *Scope) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	col, ok := r.collectors[s.CollectorID]
	if !ok || col.OrganizationID != s.OrganizationID {
		return fieldError("collector_id", "collector not found")
	}
	if col.ClientID != "" && s.ClientID != "" && s.ClientID != col.ClientID {
		return fieldError("client_id", "the collector belongs to another client")
	}
	if s.ClientID == "" {
		s.ClientID = col.ClientID
	}
	if s.SiteID == "" {
		s.SiteID = col.SiteID
	}
	for _, other := range r.scopeMap() {
		if other.CollectorID == s.CollectorID && other.Kind == s.Kind && other.Target == s.Target {
			return &api.FieldError{Field: "target", Message: "the collector already has a scope for this target", Status: http.StatusConflict}
		}
	}
	r.seq++
	s.ID = fmt.Sprintf("%08d-0000-4000-8000-%012d", r.seq, r.seq)
	now := time.Now().UTC().Format(time.RFC3339)
	s.CreatedAt, s.UpdatedAt, s.NextRunAt = now, now, now
	cp := *s
	r.scopeMap()[s.ID] = &cp
	return nil
}

// ListScopes implements ScopeRepository.
func (r *MemoryRepository) ListScopes(_ context.Context, orgID string, f ScopeFilter, page api.PaginationParams) ([]Scope, int, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := []Scope{}
	for _, s := range r.scopes {
		if s.OrganizationID == orgID && (f.CollectorID == "" || s.CollectorID == f.CollectorID) && (!f.EnabledOnly || s.Enabled) {
			cp := *s
			cp.Health = ScopeHealthOf(&cp)
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name+out[i].ID < out[j].Name+out[j].ID })
	total := len(out)
	if page.Offset < len(out) {
		out = out[page.Offset:]
	} else {
		out = []Scope{}
	}
	if page.Limit > 0 && len(out) > page.Limit {
		out = out[:page.Limit]
	}
	return out, total, nil
}

// GetScope implements ScopeRepository.
func (r *MemoryRepository) GetScope(_ context.Context, orgID, id string) (*Scope, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	s, ok := r.scopes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	cp := *s
	cp.Health = ScopeHealthOf(&cp)
	return &cp, nil
}

// UpdateScope implements ScopeRepository.
func (r *MemoryRepository) UpdateScope(_ context.Context, orgID, id string, patch *ScopePatch) (*Scope, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.scopes[id]
	if !ok || s.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	next := *s
	patch.apply(&next)
	if err := next.Validate(); err != nil {
		return nil, err
	}
	next.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	*s = next
	cp := next
	return &cp, nil
}

// DeleteScope implements ScopeRepository.
func (r *MemoryRepository) DeleteScope(_ context.Context, orgID, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.scopes[id]; !ok || s.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	delete(r.scopes, id)
	return nil
}

// RecordScopeRun implements ScopeRepository.
func (r *MemoryRepository) RecordScopeRun(_ context.Context, orgID, id string, run *ScopeRun) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.scopes[id]
	if !ok || s.OrganizationID != orgID {
		return fmt.Errorf("not found")
	}
	s.LastRunAt = run.At.UTC().Format(time.RFC3339)
	// Round-trip through JSON like the database, so readers see one form.
	raw, err := json.Marshal(run.Result)
	if err != nil {
		return err
	}
	s.LastResult = nil
	if uerr := json.Unmarshal(raw, &s.LastResult); uerr != nil {
		return uerr
	}
	if !run.NextRun.IsZero() {
		s.NextRunAt = run.NextRun.UTC().Format(time.RFC3339)
	}
	return nil
}

// ScopeCollector implements ScopeRepository.
func (r *MemoryRepository) ScopeCollector(_ context.Context, orgID, collectorID string) (*Collector, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.collectors[collectorID]
	if !ok || c.OrganizationID != orgID {
		return nil, fmt.Errorf("not found")
	}
	cp := *c
	return &cp, nil
}

// SetOrgSettings stores the organization settings the memory repository
// reads the discovery intervals from (tests, --no-db).
func (r *MemoryRepository) SetOrgSettings(orgID string, settings map[string]any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.orgSettings == nil {
		r.orgSettings = map[string]map[string]any{}
	}
	r.orgSettings[orgID] = settings
}

// DiscoveryIntervals implements ScopeRepository.
func (r *MemoryRepository) DiscoveryIntervals(_ context.Context, orgID string) (DiscoveryIntervals, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return IntervalsFromSettings(r.orgSettings[orgID]), nil
}
