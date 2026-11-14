package discovery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/observability"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reviews"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// ReviewKindScopeSuggested is the review of a subnet a collector reported
// after its enrollment (DIS-09, INS-06); accepting it creates the scope.
const ReviewKindScopeSuggested = reviews.KindScopeSuggested

// RegisterScopeRoutes mounts /api/v1/discovery-scopes (API-05).
func (h *Handler) RegisterScopeRoutes(r chi.Router) {
	r.Get("/api/v1/discovery-scopes", h.ListScopes)
	r.Post("/api/v1/discovery-scopes", h.CreateScope)
	r.Get("/api/v1/discovery-scopes/{id}", h.GetScope)
	r.Patch("/api/v1/discovery-scopes/{id}", h.UpdateScope)
	r.Delete("/api/v1/discovery-scopes/{id}", h.DeleteScope)
}

func (h *Handler) scopeRepo(w http.ResponseWriter) (ScopeRepository, bool) {
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		api.WriteError(w, http.StatusNotImplemented, "Not Implemented", "discovery scopes are not available")
	}
	return repo, ok
}

func orgOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	t := tenant.FromContext(r.Context())
	if t.OrganizationID == "" {
		api.WriteError(w, http.StatusUnauthorized, "Unauthorized", "missing tenant context")
		return "", false
	}
	return t.OrganizationID, true
}

// ListScopes handles GET /api/v1/discovery-scopes.
func (h *Handler) ListScopes(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	repo, ok := h.scopeRepo(w)
	if !ok {
		return
	}
	page := api.ParsePagination(r)
	scopes, total, err := repo.ListScopes(r.Context(), orgID, ScopeFilter{CollectorID: r.URL.Query().Get("collector_id")}, page)
	if err != nil {
		writeScopeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, api.ListResponse[Scope]{Data: scopes, Total: total, Limit: page.Limit, Offset: page.Offset,
		HasMore: page.Offset+page.Limit < total})
}

// CreateScope handles POST /api/v1/discovery-scopes.
func (h *Handler) CreateScope(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	repo, ok := h.scopeRepo(w)
	if !ok {
		return
	}
	var in ScopeInput
	if err := api.ReadJSON(r, &in); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	s := NewScope(orgID, &in)
	if err := s.Validate(); err != nil {
		writeScopeError(w, err)
		return
	}
	if err := repo.CreateScope(r.Context(), s); err != nil {
		writeScopeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusCreated, s)
}

// GetScope handles GET /api/v1/discovery-scopes/{id}.
func (h *Handler) GetScope(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	repo, ok := h.scopeRepo(w)
	if !ok {
		return
	}
	s, err := repo.GetScope(r.Context(), orgID, chi.URLParam(r, "id"))
	if err != nil {
		writeScopeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// UpdateScope handles PATCH /api/v1/discovery-scopes/{id}.
func (h *Handler) UpdateScope(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	repo, ok := h.scopeRepo(w)
	if !ok {
		return
	}
	var patch ScopePatch
	if err := api.ReadJSON(r, &patch); err != nil {
		api.WriteError(w, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	s, err := repo.UpdateScope(r.Context(), orgID, chi.URLParam(r, "id"), &patch)
	if err != nil {
		writeScopeError(w, err)
		return
	}
	api.WriteJSON(w, http.StatusOK, s)
}

// DeleteScope handles DELETE /api/v1/discovery-scopes/{id}.
func (h *Handler) DeleteScope(w http.ResponseWriter, r *http.Request) {
	orgID, ok := orgOf(w, r)
	if !ok {
		return
	}
	repo, ok := h.scopeRepo(w)
	if !ok {
		return
	}
	if err := repo.DeleteScope(r.Context(), orgID, chi.URLParam(r, "id")); err != nil {
		writeScopeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeScopeError answers field errors with the field, unknown scopes with
// 404 and everything else as repository error.
func writeScopeError(w http.ResponseWriter, err error) {
	if api.WriteFieldError(w, err) {
		return
	}
	if err.Error() == "not found" {
		api.WriteError(w, http.StatusNotFound, "Not Found", "discovery scope not found")
		return
	}
	api.WriteRepoError(w, err)
}

// --- scope_suggested (Zero-Config, INS-06) -------------------------------

// suggestableSubnet returns the canonical CIDR of a reported subnet, or
// false for loopback, link-local, multicast and subnets too large for a
// scope.
func suggestableSubnet(cidr string) (string, bool) {
	p, err := netip.ParsePrefix(strings.TrimSpace(cidr))
	if err != nil {
		return "", false
	}
	a := p.Addr()
	if a.IsLoopback() || a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsUnspecified() || p.Bits() == a.BitLen() {
		return "", false
	}
	target, err := NormalizeScopeTarget(ScopeKindCIDR, p.String())
	return target, err == nil
}

// SuggestScopes opens a scope_suggested review for every reported subnet
// that no scope of the collector covers; a dismissed suggestion stays
// dismissed. It returns the number of new suggestions; a disabled collector
// is refused.
func (h *Handler) SuggestScopes(ctx context.Context, orgID, collectorID string, subnets []string) (int, error) {
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return 0, errors.New("discovery scopes are not available")
	}
	col, err := repo.ScopeCollector(ctx, orgID, collectorID)
	if err != nil {
		return 0, err
	}
	if col.Status == CollectorDisabled {
		return 0, ErrCollectorDisabled
	}
	existing, _, err := repo.ListScopes(ctx, orgID, ScopeFilter{CollectorID: collectorID}, api.PaginationParams{Limit: 10000})
	if err != nil {
		return 0, err
	}
	// A suggestion belongs to the client and site of the collector.
	opened := 0
	for _, raw := range subnets {
		cidr, ok := suggestableSubnet(raw)
		if !ok || coveredByScope(existing, cidr) {
			continue
		}
		created, oerr := h.ReviewService().OpenUnlessDismissed(ctx, &ReviewItem{
			OrganizationID: orgID,
			Kind:           ReviewKindScopeSuggested,
			Status:         ReviewStatusOpen,
			ClientID:       col.ClientID,
			SiteID:         col.SiteID,
			ObjectKey:      "scope:" + collectorID + ":" + cidr,
			Payload: map[string]any{
				"collector_id":   collectorID,
				"collector_name": col.Name,
				"cidr":           cidr,
				"site_id":        col.SiteID,
				"client_id":      col.ClientID,
			},
		}, "cidr")
		if oerr != nil {
			return opened, oerr
		}
		if created {
			opened++
		}
	}
	return opened, nil
}

func coveredByScope(scopes []Scope, cidr string) bool {
	p, err := netip.ParsePrefix(cidr)
	if err != nil {
		return false
	}
	for i := range scopes {
		if scopes[i].Kind != ScopeKindCIDR {
			continue
		}
		if q, qerr := netip.ParsePrefix(scopes[i].Target); qerr == nil && q.Bits() <= p.Bits() && q.Contains(p.Addr()) {
			return true
		}
	}
	return false
}

// resolveScopeSuggested creates the scope of an accepted suggestion. Accept
// creates a scope and dismiss suppresses one, so both need discovery:manage
// besides review:resolve (DIS-09, F-031); without it the item stays open.
// For the same reason both need the discovery feature, as POST
// /api/v1/discovery-scopes does: not entitled is 403 feature-not-entitled
// and the item stays open, an expired license passes (CH21). Reading the
// queue is cmdb_core (F-400).
func (h *Handler) resolveScopeSuggested(ctx context.Context, item *ReviewItem, res *Resolution) (bool, error) {
	if perms, _ := identity.PermissionsFromContext(ctx); !identity.HasPermission(perms, identity.PermDiscoveryManage) {
		return false, refused(http.StatusForbidden, "missing required permission: "+string(identity.PermDiscoveryManage))
	}
	state, problem, err := h.edgeLicense(ctx, item.OrganizationID)
	if err != nil {
		return false, err
	}
	if state == LicenseNotEntitled {
		refusal := &entitlement.FeatureNotEntitledError{FeatureKey: entitlement.FeatureDiscovery}
		return false, &reviews.Error{Status: http.StatusForbidden, Problem: problem, Message: refusal.Error()}
	}
	if res.Action != reviews.ActionAccept {
		return false, nil
	}
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return false, refused(http.StatusNotImplemented, "discovery scopes are not available")
	}
	cidr := payloadString(item.Payload, "cidr")
	s := NewScope(item.OrganizationID, &ScopeInput{
		CollectorID: payloadString(item.Payload, "collector_id"),
		Name:        cidr,
		Kind:        ScopeKindCIDR,
		Target:      cidr,
		SiteID:      payloadString(item.Payload, "site_id"),
	})
	if err := s.Validate(); err != nil {
		return false, err
	}
	if err := repo.CreateScope(ctx, s); err != nil {
		return false, err
	}
	res.Note = "created scope " + s.ID
	return false, nil
}

// --- collector channel over NATS -------------------------------------------

// NATS subjects below collector.<org>.<collector-id> (the edge lets a
// collector use only its own subject tree, WP-146).
const (
	ScopesRequestSuffix = ".scopes.request"
	ScopesResultSuffix  = ".scopes.result"
	SubnetsReportSuffix = ".subnets.report"
	// The collector's ad-hoc discovery jobs (JOB-01, F-358): its open jobs
	// and the status it reports for one of them.
	JobsRequestSuffix = ".jobs.request"
	JobsStatusSuffix  = ".jobs.status"
	// The assignment set of the monitoring probes (PRB-11, WP-948): the
	// collector asks for its set, the server tells it a new version.
	ProbesRequestSuffix = ".probes.request"
	ProbesChangedSuffix = ".probes.changed"
)

// CollectorScope is a scope as the collector receives it; CredentialIDs are
// its effective credentials (E-67), not the stored list. LastRunAt and
// NextRunAt are the run state the server holds (RFC 3339; a new scope has
// no last run and is due at its creation): a (re)started collector
// schedules the sweep from them instead of at once (DIS-10, F-109). Timezone is the organization's time zone, in
// which the collector evaluates a cron schedule (E-84 option A).
type CollectorScope struct {
	ID            string   `json:"id"`
	Kind          string   `json:"kind"`
	Target        string   `json:"target"`
	ClientID      string   `json:"client_id,omitempty"`
	SiteID        string   `json:"site_id,omitempty"`
	VRFID         string   `json:"vrf_id,omitempty"`
	Schedule      string   `json:"schedule"`
	CredentialIDs []string `json:"credential_ids"`
	Plugins       []string `json:"plugins"`
	LastRunAt     string   `json:"last_run_at,omitempty"`
	NextRunAt     string   `json:"next_run_at,omitempty"`
	Timezone      string   `json:"timezone"`
}

// ScopesResponse answers a scopes request.
type ScopesResponse struct {
	Scopes []CollectorScope `json:"scopes"`
	// Intervals are the organization's sweep and poll intervals (DIS-10).
	Intervals *DiscoveryIntervals `json:"intervals,omitempty"`
	Error     string              `json:"error,omitempty"`
}

// SubnetsReport is what a collector reports after its enrollment.
type SubnetsReport struct {
	Subnets []string `json:"subnets"`
}

// ScopeResultReport is the result of one scope run. CredentialUsage are
// the credentials the run used per target and namespace (CRD-06, WP-898),
// never a value of a secret; at most credential.MaxUsageEntries count.
type ScopeResultReport struct {
	ScopeID         string                   `json:"scope_id"`
	At              time.Time                `json:"at"`
	NextRun         time.Time                `json:"next_run"`
	Result          map[string]any           `json:"result"`
	CredentialUsage []credential.UsageReport `json:"credential_usage,omitempty"`
}

// ScopeRunUsageRecorder stores a scope run together with its credential
// usage in one transaction (CRD-06): the PostgreSQL repository. Without it
// (--no-db) the usage of a run is dropped.
type ScopeRunUsageRecorder interface {
	RecordScopeRunUsage(ctx context.Context, orgID, collectorID, scopeID string, run *ScopeRun,
		usage []credential.UsageReport) (credential.UsageOutcome, error)
	// RecordScopeUsage records the usage of a scope pass of a discovery
	// job without touching the scope's run.
	RecordScopeUsage(ctx context.Context, orgID, collectorID, scopeID string, at time.Time,
		usage []credential.UsageReport) (credential.UsageOutcome, error)
}

// subjectCollector extracts org and collector id from
// collector.<org>.<id><suffix>.
func subjectCollector(subject, suffix string) (orgID, collectorID string, ok bool) {
	parts := strings.Split(strings.TrimSuffix(subject, suffix), ".")
	if len(parts) != 3 || parts[0] != "collector" || !api.IsUUID(parts[1]) || !api.IsUUID(parts[2]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

// systemContext is the organization-wide tenant scope of a system worker.
func systemContext(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "")
	return database.ContextWithTenantScope(ctx, &scope)
}

// CollectorScopes returns the enabled scopes of a collector that is not
// disabled.
func (h *Handler) CollectorScopes(ctx context.Context, orgID, collectorID string) ([]CollectorScope, error) {
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return nil, errors.New("discovery scopes are not available")
	}
	col, err := repo.ScopeCollector(ctx, orgID, collectorID)
	if err != nil {
		return nil, err
	}
	if col.Status == CollectorDisabled {
		return nil, ErrCollectorDisabled
	}
	scopes, _, err := repo.ListScopes(ctx, orgID, ScopeFilter{CollectorID: collectorID, EnabledOnly: true}, api.PaginationParams{Limit: 10000})
	if err != nil {
		return nil, err
	}
	// Each scope carries its effective credentials, the ones of the bundle
	// the collector resolves for it (E-67); a scope created after the
	// lookup gets none until the next request.
	creds, err := repo.ScopeCredentialIDs(ctx, orgID, collectorID)
	if err != nil {
		return nil, err
	}
	tz, err := repo.OrganizationTimezone(ctx, orgID)
	if err != nil {
		return nil, err
	}
	out := make([]CollectorScope, 0, len(scopes))
	for i := range scopes {
		s := &scopes[i]
		ids := creds[s.ID]
		if ids == nil {
			ids = []string{}
		}
		out = append(out, CollectorScope{ID: s.ID, Kind: s.Kind, Target: s.Target, ClientID: s.ClientID, SiteID: s.SiteID,
			VRFID: s.VRFID, Schedule: s.Schedule, CredentialIDs: ids, Plugins: s.Plugins,
			LastRunAt: s.LastRunAt, NextRunAt: s.NextRunAt, Timezone: tz})
	}
	return out, nil
}

// RecordCollectorScopeRun stores a run result of one of the collector's
// scopes; an unknown or disabled collector is refused.
func (h *Handler) RecordCollectorScopeRun(ctx context.Context, orgID, collectorID string, rep *ScopeResultReport) error {
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return errors.New("discovery scopes are not available")
	}
	if err := h.checkCollector(ctx, orgID, collectorID); err != nil {
		return err
	}
	s, err := repo.GetScope(ctx, orgID, rep.ScopeID)
	if err != nil {
		return err
	}
	if s.CollectorID != collectorID {
		return errors.New("the scope belongs to another collector")
	}
	at := rep.At
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result := NormalizeRunResult(rep.Result)
	if state, _ := result["status"].(string); state != ScopeStateOK {
		slog.Warn("discovery scope run failed", "event", "discovery.scope."+state, "organization_id", orgID,
			"collector_id", collectorID, "scope_id", rep.ScopeID, "errors", result["errors"])
	}
	run := &ScopeRun{At: at, Result: result, NextRun: rep.NextRun}
	if usageRepo, withUsage := h.repo.(ScopeRunUsageRecorder); withUsage && len(rep.CredentialUsage) > 0 {
		usage := rep.CredentialUsage
		if len(usage) > credential.MaxUsageEntries {
			usage = usage[:credential.MaxUsageEntries]
		}
		out, uerr := usageRepo.RecordScopeRunUsage(ctx, orgID, collectorID, rep.ScopeID, run, usage)
		if uerr == nil && (out.Unmatched > 0 || out.Rejected > 0) {
			slog.Info("credential usage of a scope run partly dropped", "event", "credential.usage.dropped",
				"organization_id", orgID, "collector_id", collectorID, "scope_id", rep.ScopeID,
				"recorded", out.Recorded, "unmatched", out.Unmatched, "rejected", out.Rejected)
		}
		return uerr
	}
	return repo.RecordScopeRun(ctx, orgID, rep.ScopeID, run)
}

// collectorsWithoutScopes holds the collectors this process logged as
// having no enabled scope (noteCollectorScopes).
var collectorsWithoutScopes sync.Map

// noteCollectorScopes logs at INFO that a collector received no enabled
// scope: an empty list tells it to discover nothing, while its subnet
// reports only wait as scope_suggested reviews on the Discovery page
// (INS-06). Once per collector and process while it has none, so the
// refresh of its scopes (every 15 minutes) does not repeat it; once it has
// a scope again, a later empty list is logged anew.
func noteCollectorScopes(orgID, collectorID string, scopes int) {
	if scopes > 0 {
		collectorsWithoutScopes.Delete(collectorID)
		return
	}
	if _, logged := collectorsWithoutScopes.LoadOrStore(collectorID, struct{}{}); logged {
		return
	}
	slog.Info("collector has no enabled discovery scope; it discovers nothing", "event", "discovery.collector.no_scopes",
		"organization_id", orgID, "collector_id", collectorID,
		"hint", "accept a subnet the collector suggested or create a scope for it on the Discovery page")
}

// scopeHandlers are the handlers of the scope channel subjects by suffix;
// they run only for a collector the dispatcher checked.
func (h *Handler) scopeHandlers() map[string]func(ctx context.Context, orgID, collectorID string, data []byte) any {
	return map[string]func(ctx context.Context, orgID, collectorID string, data []byte) any{
		ScopesRequestSuffix: func(ctx context.Context, orgID, collectorID string, _ []byte) any {
			scopes, err := h.CollectorScopes(ctx, orgID, collectorID)
			if err != nil {
				slog.Warn("collector scopes refused", "collector_id", collectorID, "error", err)
				return ScopesResponse{Scopes: []CollectorScope{}, Error: "not available"}
			}
			noteCollectorScopes(orgID, collectorID, len(scopes))
			resp := ScopesResponse{Scopes: scopes}
			if repo, ok := h.repo.(ScopeRepository); ok {
				if iv, ierr := repo.DiscoveryIntervals(ctx, orgID); ierr == nil {
					resp.Intervals = &iv
				}
			}
			return resp
		},
		ScopesResultSuffix: func(ctx context.Context, orgID, collectorID string, data []byte) any {
			var rep ScopeResultReport
			if err := json.Unmarshal(data, &rep); err != nil {
				return map[string]string{"error": "invalid report"}
			}
			if err := h.RecordCollectorScopeRun(ctx, orgID, collectorID, &rep); err != nil {
				slog.Warn("scope run result refused", "collector_id", collectorID, "scope_id", rep.ScopeID, "error", err)
				return map[string]string{"error": "not recorded"}
			}
			return map[string]string{"status": "recorded"}
		},
		SubnetsReportSuffix: func(ctx context.Context, orgID, collectorID string, data []byte) any {
			var rep SubnetsReport
			if err := json.Unmarshal(data, &rep); err != nil {
				return map[string]string{"error": "invalid report"}
			}
			n, err := h.SuggestScopes(ctx, orgID, collectorID, rep.Subnets)
			if err != nil {
				slog.Warn("scope suggestions failed", "collector_id", collectorID, "error", err)
				return map[string]string{"error": "not recorded"}
			}
			return map[string]int{"suggested": n}
		},
		JobsRequestSuffix: func(ctx context.Context, orgID, collectorID string, _ []byte) any {
			open, err := h.CollectorJobs(ctx, orgID, collectorID)
			if err != nil {
				slog.Warn("collector jobs refused", "collector_id", collectorID, "error", err)
				return JobsResponse{Jobs: []CollectorJob{}, Error: "not available"}
			}
			return JobsResponse{Jobs: open}
		},
		JobsStatusSuffix: func(ctx context.Context, orgID, collectorID string, data []byte) any {
			var rep JobStatusReport
			if err := json.Unmarshal(data, &rep); err != nil {
				return map[string]string{"error": "invalid report"}
			}
			if _, err := h.RecordCollectorJobStatus(ctx, orgID, collectorID, &rep); err != nil {
				slog.Warn("job status refused", "collector_id", collectorID, "job_id", rep.JobID, "status", rep.Status, "error", err)
				return map[string]string{"error": "not recorded"}
			}
			return map[string]string{"status": "recorded"}
		},
	}
}

// WithScopeCycles counts the scope requests that failed on the server's
// side (the collector could not be looked up, the reply could not be sent)
// into cycles, the cycles of the worker discovery_scopes (OPS-05). An
// unknown or disabled collector is no failure.
func (h *Handler) WithScopeCycles(cycles *observability.Cycles) *Handler {
	h.scopeCycles = cycles
	return h
}

// scopeResultSuffixes are the subjects on which a collector delivers
// discovery results; like the ingest they need an active license (CH21).
var scopeResultSuffixes = map[string]bool{ScopesResultSuffix: true, SubnetsReportSuffix: true}

// scopeChannelLicensed reports whether the organization's discovery license
// allows the scope channel subject, and otherwise the error of the reply
// (ENT-05, ENT-07, F-400): without the discovery entitlement no subject is
// answered, so the collector gets no scopes or jobs and opens no
// scope_suggested item; after the license expired the results (run
// results, subnet reports) are refused like the ingest, while scopes, jobs
// and job statuses are still answered (the collector is paused). A failed
// lookup refuses (fail-closed) and counts as a failed request.
func (h *Handler) scopeChannelLicensed(ctx context.Context, orgID, collectorID, suffix string) (string, bool) {
	state, _, err := h.edgeLicense(ctx, orgID)
	if err != nil {
		h.scopeCycles.Fail()
		slog.Error("collector scope request: discovery license lookup failed", "organization_id", orgID,
			"collector_id", collectorID, "suffix", suffix, "error", err)
		return "not available", false
	}
	if state == LicenseNotEntitled || (state != LicenseActive && scopeResultSuffixes[suffix]) {
		slog.Warn("collector scope request refused", "organization_id", orgID, "collector_id", collectorID,
			"suffix", suffix, "license", state)
		return "discovery license " + state, false
	}
	return "", true
}

// ServeCollectorScopes answers the scope requests, run results and subnet
// reports of the collectors on nc until ctx ends (DIS-09), and their
// requests for open discovery jobs and the job statuses they report
// (JOB-01, F-358). A request of an
// unknown or disabled collector is refused before its handler runs,
// whatever the suffix, so nothing of it is persisted (COL-04); so is a
// request the organization's discovery license does not allow
// (scopeChannelLicensed, F-400).
func (h *Handler) ServeCollectorScopes(ctx context.Context, nc *nats.Conn) error {
	var subs []*nats.Subscription
	for suffix, handle := range h.scopeHandlers() {
		sub, err := nc.Subscribe("collector.*.*"+suffix, func(msg *nats.Msg) {
			orgID, collectorID, ok := subjectCollector(msg.Subject, suffix)
			var resp any = map[string]string{"error": "invalid subject"}
			if ok {
				sctx := systemContext(ctx, orgID)
				if cerr := h.checkCollector(sctx, orgID, collectorID); cerr != nil {
					if !errors.Is(cerr, ErrCollectorDisabled) && !errors.Is(cerr, api.ErrNotFound) && !errors.Is(cerr, ErrOrgPendingDeletion) {
						h.scopeCycles.Fail()
					}
					slog.Warn("collector scope request refused", "organization_id", orgID, "collector_id", collectorID,
						"suffix", suffix, "error", cerr)
					resp = map[string]string{"error": "not available"}
				} else if refusal, licensed := h.scopeChannelLicensed(sctx, orgID, collectorID, suffix); !licensed {
					resp = map[string]string{"error": refusal}
				} else {
					resp = handle(sctx, orgID, collectorID, msg.Data)
				}
			}
			raw, _ := json.Marshal(resp)
			if msg.Reply != "" {
				if rerr := msg.Respond(raw); rerr != nil {
					h.scopeCycles.Fail()
					slog.Error("collector scope reply", "error", rerr)
				}
			}
		})
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", suffix, err)
		}
		subs = append(subs, sub)
	}
	<-ctx.Done()
	for _, sub := range subs {
		_ = sub.Unsubscribe()
	}
	return nil
}
