package maintenance

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// The recipients of a maintenance window (MGT-04, WP-1034, E-204): the
// clients the window affects over the configuration graph, not only the
// clients of its own CIs. The impact of the window's CIs failing together
// comes from the topology analyzer (WP-1025) in the scope of the caller,
// so a client outside that scope never appears.

// ImpactAnalyzer is the graph analysis the recipients need
// (topology.Analyzer).
type ImpactAnalyzer interface {
	Impact(ctx context.Context, orgID string, failed []string, opts topology.ImpactOptions) (*topology.ImpactResult, error)
}

// RecipientOptions narrow the impact of a window.
type RecipientOptions struct {
	// IncludeHeuristic adds the CIs reached only over heuristic paths
	// (connected_to); off by default.
	IncludeHeuristic bool `json:"include_heuristic"`
	// Categories limits the impact to these relationship categories;
	// empty: all.
	Categories []string `json:"categories"`
}

// Validate refuses an unknown relationship category with 422 on the field
// categories, like the topology impact: an unknown category would filter
// every edge and silently reach only the window's own clients.
func (o *RecipientOptions) Validate() error {
	for _, c := range o.Categories {
		if !slices.Contains(relationshiptype.Categories, c) {
			return &api.FieldError{Field: "categories", Message: "unknown category " + c + "; known: " +
				strings.Join(relationshiptype.Categories, ", "), Status: http.StatusUnprocessableEntity}
		}
	}
	return nil
}

// ClientImpact is what a window means for one client.
type ClientImpact struct {
	ClientID string `json:"client_id"`
	// WindowCIs are the window's own CIs of the client.
	WindowCIs int `json:"window_cis"`
	// CertainCIs and HeuristicCIs are the client's CIs the window affects
	// over certain or (with IncludeHeuristic) heuristic paths.
	CertainCIs   int `json:"certain_cis"`
	HeuristicCIs int `json:"heuristic_cis"`
	// Services are the client's affected services (IMP-08).
	Services int `json:"services"`
}

// AffectedCIs is the number of the client's CIs the window concerns: its
// own and the affected ones.
func (c *ClientImpact) AffectedCIs() int { return c.WindowCIs + c.CertainCIs + c.HeuristicCIs }

// AffectedService is a service the window affects.
type AffectedService struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ClientID  string `json:"client_id,omitempty"`
	Heuristic bool   `json:"heuristic"`
}

// ImpactPreview is the impact of a window: the affected clients with their
// numbers, sites and services (GET /maintenance-windows/{id}/impact).
type ImpactPreview struct {
	WindowID         string            `json:"maintenance_window_id"`
	IncludeHeuristic bool              `json:"include_heuristic"`
	Categories       []string          `json:"categories"`
	Clients          []ClientImpact    `json:"clients"`
	SiteIDs          []string          `json:"site_ids"`
	Services         []AffectedService `json:"services"`
	// Truncated reports a walk the node limit cut (NFR-01): the preview
	// may miss clients.
	Truncated bool `json:"truncated"`
}

// RecipientFinder computes the recipients of windows.
type RecipientFinder struct {
	repo     Repository
	analyzer ImpactAnalyzer
}

// NewRecipientFinder returns the finder on the window repository and the
// topology analyzer.
func NewRecipientFinder(repo Repository, analyzer ImpactAnalyzer) *RecipientFinder {
	return &RecipientFinder{repo: repo, analyzer: analyzer}
}

// Recipients computes the clients, sites and services the window affects
// in the scope of the caller. An unknown or invisible window is
// api.ErrNotFound.
func (f *RecipientFinder) Recipients(ctx context.Context, orgID, windowID string, opts RecipientOptions) (*ImpactPreview, error) {
	win, err := f.repo.GetByID(ctx, orgID, windowID)
	if err != nil {
		return nil, err
	}
	preview := &ImpactPreview{WindowID: win.ID, IncludeHeuristic: opts.IncludeHeuristic,
		Categories: api.NonNilSlice(opts.Categories), Clients: []ClientImpact{}, SiteIDs: []string{}, Services: []AffectedService{}}
	if len(win.CIIDs) == 0 {
		return preview, nil
	}
	res, err := f.analyzer.Impact(ctx, orgID, win.CIIDs, topology.ImpactOptions{Categories: opts.Categories, ServiceChain: true})
	if err != nil {
		return nil, fmt.Errorf("maintenance: impact of window %s: %w", windowID, err)
	}
	preview.Truncated = res.Truncated
	clients := map[string]*ClientImpact{}
	sites := map[string]bool{}
	client := func(id, site string) *ClientImpact {
		if site != "" {
			sites[site] = true
		}
		if id == "" {
			return nil // an organization-wide CI concerns no client
		}
		c, ok := clients[id]
		if !ok {
			c = &ClientImpact{ClientID: id}
			clients[id] = c
		}
		return c
	}
	for i := range res.Failed {
		if c := client(res.Failed[i].ClientID, res.Failed[i].SiteID); c != nil {
			c.WindowCIs++
		}
	}
	for i := range res.Impacted {
		n := &res.Impacted[i]
		if n.Heuristic && !opts.IncludeHeuristic {
			continue
		}
		c := client(n.ClientID, n.SiteID)
		switch {
		case c == nil:
		case n.Heuristic:
			c.HeuristicCIs++
		default:
			c.CertainCIs++
		}
	}
	for i := range res.Services {
		s := &res.Services[i]
		if s.Heuristic && !opts.IncludeHeuristic {
			continue
		}
		preview.Services = append(preview.Services, AffectedService{ID: s.ID, Name: s.Name, ClientID: s.ClientID, Heuristic: s.Heuristic})
		if c := client(s.ClientID, s.SiteID); c != nil {
			c.Services++
		}
	}
	for _, c := range clients {
		preview.Clients = append(preview.Clients, *c)
	}
	sort.Slice(preview.Clients, func(i, j int) bool { return preview.Clients[i].ClientID < preview.Clients[j].ClientID })
	for site := range sites {
		preview.SiteIDs = append(preview.SiteIDs, site)
	}
	slices.Sort(preview.SiteIDs)
	return preview, nil
}
