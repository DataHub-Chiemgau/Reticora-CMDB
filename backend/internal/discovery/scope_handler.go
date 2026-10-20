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
// dismissed. It returns the number of new suggestions.
func (h *Handler) SuggestScopes(ctx context.Context, orgID, collectorID string, subnets []string) (int, error) {
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return 0, errors.New("discovery scopes are not available")
	}
	col, err := repo.ScopeCollector(ctx, orgID, collectorID)
	if err != nil {
		return 0, err
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

// resolveScopeSuggested creates the scope of an accepted suggestion.
func (h *Handler) resolveScopeSuggested(ctx context.Context, item *ReviewItem, res *Resolution) (bool, error) {
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
)

// CollectorScope is a scope as the collector receives it.
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
	out := make([]CollectorScope, 0, len(scopes))
	for i := range scopes {
		s := &scopes[i]
		out = append(out, CollectorScope{ID: s.ID, Kind: s.Kind, Target: s.Target, ClientID: s.ClientID, SiteID: s.SiteID,
			VRFID: s.VRFID, Schedule: s.Schedule, CredentialIDs: s.CredentialIDs, Plugins: s.Plugins})
	}
	return out, nil
}

// RecordCollectorScopeRun stores a run result of one of the collector's
// scopes.
func (h *Handler) RecordCollectorScopeRun(ctx context.Context, orgID, collectorID string, rep *ScopeResultReport) error {
	repo, ok := h.repo.(ScopeRepository)
	if !ok {
		return errors.New("discovery scopes are not available")
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

// ServeCollectorScopes answers the scope requests, run results and subnet
// reports of the collectors on nc until ctx ends (DIS-09).
func (h *Handler) ServeCollectorScopes(ctx context.Context, nc *nats.Conn) error {
	handlers := map[string]func(ctx context.Context, orgID, collectorID string, data []byte) any{
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
	}
	var subs []*nats.Subscription
	for suffix, handle := range handlers {
		sub, err := nc.Subscribe("collector.*.*"+suffix, func(msg *nats.Msg) {
			orgID, collectorID, ok := subjectCollector(msg.Subject, suffix)
			var resp any = map[string]string{"error": "invalid subject"}
			if ok {
				resp = handle(systemContext(ctx, orgID), orgID, collectorID, msg.Data)
			}
			raw, _ := json.Marshal(resp)
			if msg.Reply != "" {
				if rerr := msg.Respond(raw); rerr != nil {
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
