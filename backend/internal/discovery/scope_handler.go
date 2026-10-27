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
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
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
func (h *Handler) resolveScopeSuggested(ctx context.Context, item *ReviewItem, res *Resolution) (bool, error) {
	if perms, _ := identity.PermissionsFromContext(ctx); !identity.HasPermission(perms, identity.PermDiscoveryManage) {
		return false, refused(http.StatusForbidden, "missing required permission: "+string(identity.PermDiscoveryManage))
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

// ScopeResultReport is the result of one scope run.
type ScopeResultReport struct {
	ScopeID string         `json:"scope_id"`
	At      time.Time      `json:"at"`
	NextRun time.Time      `json:"next_run"`
	Result  map[string]any `json:"result"`
}

// subjectCollector extracts org and collector id from
// collector.<org>.<id><suffix>.
func subjectCollector(subject, suffix string) (orgID, collectorID string, ok bool) {
	parts := strings.Split(strings.TrimSuffix(subject, suffix), ".")
	if len(parts) != 3 || parts[0] != "collector" || !isUUIDText(parts[1]) || !isUUIDText(parts[2]) {
		return "", "", false
	}
	return parts[1], parts[2], true
}

func isUUIDText(s string) bool { return audit.IsUUID(s) }

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
	return repo.RecordScopeRun(ctx, orgID, rep.ScopeID, &ScopeRun{At: at, Result: result, NextRun: rep.NextRun})
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

// ServeCollectorScopes answers the scope requests, run results and subnet
// reports of the collectors on nc until ctx ends (DIS-09), and their
// requests for open discovery jobs and the job statuses they report
// (JOB-01, F-358). A request of an
// unknown or disabled collector is refused before its handler runs,
// whatever the suffix, so nothing of it is persisted (COL-04).
func (h *Handler) ServeCollectorScopes(ctx context.Context, nc *nats.Conn) error {
	var subs []*nats.Subscription
	for suffix, handle := range h.scopeHandlers() {
		sub, err := nc.Subscribe("collector.*.*"+suffix, func(msg *nats.Msg) {
			orgID, collectorID, ok := subjectCollector(msg.Subject, suffix)
			var resp any = map[string]string{"error": "invalid subject"}
			if ok {
				sctx := systemContext(ctx, orgID)
				if cerr := h.checkCollector(sctx, orgID, collectorID); cerr != nil {
					if !errors.Is(cerr, ErrCollectorDisabled) && !errors.Is(cerr, api.ErrNotFound) {
						h.scopeCycles.Fail()
					}
					slog.Warn("collector scope request refused", "organization_id", orgID, "collector_id", collectorID,
						"suffix", suffix, "error", cerr)
					resp = map[string]string{"error": "not available"}
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
