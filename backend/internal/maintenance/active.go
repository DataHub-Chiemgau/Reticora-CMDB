package maintenance

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/alarm"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/topology"
)

// Active maintenance windows and the alarm suppression (ALR-11, MGT-04,
// WP-1033, E-204 option 2). A window is active when starts_at <= now <
// ends_at and its status is not cancelled; the status itself stays manual.
// The CIs of an active window are its own CIs and the CIs their joint
// failure certainly affects over the configuration graph (topology impact,
// WP-1025); the CIs reached only over a heuristic path (connected_to) are
// kept apart: their alarms are marked, not suppressed. The CIs of a window
// are computed once per (window, updated_at) and held for ActiveCacheTTL;
// a window that starts or changes is computed anew.
//
// The system job maintenance.boundaries runs every minute per organization
// in its tenant transaction (org-wide scope, so it sees every window): for
// every window that started or ended within BoundaryHorizon it writes
// exactly one event maintenance.started or maintenance.ended (the outbox is
// the record of what was written, so two runs in the same minute write one
// event per transition), and at the end of a window it releases the alarms
// the window still suppresses (alarm.Service.ReleaseMaintenance). Every run
// also releases the alarms still suppressed for maintenance that no active
// window holds any more (a window cancelled or deleted while active, a late
// observation; alarm.Service.ReleaseStaleMaintenance).

// BoundariesKind is the system job of the window boundaries.
const BoundariesKind = "maintenance.boundaries"

// BoundariesSpec runs the job every minute.
const BoundariesSpec = "@every 1m"

// ActiveCacheTTL is how long the CIs of a window are held.
const ActiveCacheTTL = 60 * time.Second

// BoundaryHorizon bounds how far back a run looks for a start or an end it
// has not recorded yet (after a downtime of the scheduler).
const BoundaryHorizon = 24 * time.Hour

// WindowCIs are the CIs of one active window: its own and the certainly
// affected ones (Certain), and the heuristically affected ones.
type WindowCIs struct {
	WindowID  string
	Certain   []string
	Heuristic []string
}

// ActiveSet are the active windows at a time with their CIs.
type ActiveSet struct {
	Windows []WindowCIs
}

// Of returns the window a CI belongs to: certainly first (in any window),
// else heuristically; the zero hit for none.
func (s *ActiveSet) Of(ciID string) alarm.MaintenanceHit {
	for _, w := range s.Windows {
		for _, c := range w.Certain {
			if c == ciID {
				return alarm.MaintenanceHit{WindowID: w.WindowID}
			}
		}
	}
	for _, w := range s.Windows {
		for _, c := range w.Heuristic {
			if c == ciID {
				return alarm.MaintenanceHit{WindowID: w.WindowID, Heuristic: true}
			}
		}
	}
	return alarm.MaintenanceHit{}
}

type activeKey struct {
	org, window string
	updated     time.Time
}

type activeEntry struct {
	cis WindowCIs
	at  time.Time
}

// Active computes the CIs of the active windows; it implements
// alarm.MaintenanceChecker.
type Active struct {
	repo     Repository
	analyzer ImpactAnalyzer
	now      func() time.Time

	mu    sync.Mutex
	cache map[activeKey]activeEntry
}

// NewActive returns the active windows of repo with the graph impact of
// analyzer (nil: only the windows' own CIs).
func NewActive(repo Repository, analyzer ImpactAnalyzer) *Active {
	return &Active{repo: repo, analyzer: analyzer, now: time.Now, cache: map[activeKey]activeEntry{}}
}

// WithClock replaces the clock of the cache (tests).
func (a *Active) WithClock(now func() time.Time) *Active {
	a.now = now
	return a
}

// orgWide detaches ctx from its transaction and scope: the windows and their
// impact are read org-wide in a transaction of their own, whatever the
// scope of the caller (a raise in the client-bound scope of a collector,
// WP-1054, must not narrow what the cache holds for everybody).
func orgWide(ctx context.Context, orgID string) context.Context {
	scope := database.OrgWideScope(orgID, "")
	return database.ContextWithTenantScope(database.WithoutTx(ctx), &scope)
}

// ActiveCIs returns the active windows at at with their CIs (AK2), read
// org-wide (orgWide).
func (a *Active) ActiveCIs(ctx context.Context, orgID string, at time.Time) (*ActiveSet, error) {
	ctx = orgWide(ctx, orgID)
	wins, err := a.repo.ActiveWindows(ctx, orgID, at)
	if err != nil {
		return nil, err
	}
	out := &ActiveSet{}
	for i := range wins {
		cis, err := a.windowCIs(ctx, orgID, &wins[i])
		if err != nil {
			return nil, err
		}
		out.Windows = append(out.Windows, cis)
	}
	return out, nil
}

// MaintenanceOf implements alarm.MaintenanceChecker.
func (a *Active) MaintenanceOf(ctx context.Context, orgID, ciID string, at time.Time) (alarm.MaintenanceHit, error) {
	set, err := a.ActiveCIs(ctx, orgID, at)
	if err != nil {
		return alarm.MaintenanceHit{}, err
	}
	return set.Of(ciID), nil
}

// windowCIs returns the CIs of a window, from the cache when it holds them
// for the same updated_at.
func (a *Active) windowCIs(ctx context.Context, orgID string, w *Window) (WindowCIs, error) {
	key := activeKey{org: orgID, window: w.ID, updated: w.UpdatedAt}
	now := a.now()
	a.mu.Lock()
	e, ok := a.cache[key]
	a.mu.Unlock()
	if ok && now.Sub(e.at) < ActiveCacheTTL {
		return e.cis, nil
	}
	cis := WindowCIs{WindowID: w.ID, Certain: append([]string(nil), w.CIIDs...)}
	if a.analyzer != nil && len(w.CIIDs) > 0 {
		res, err := a.analyzer.Impact(ctx, orgID, w.CIIDs, topology.ImpactOptions{})
		var nf *topology.NotFoundError
		switch {
		case errors.As(err, &nf):
			// A window CI that went: only the window's own CIs.
		case err != nil:
			return WindowCIs{}, fmt.Errorf("impact of window %s: %w", w.ID, err)
		default:
			for i := range res.Impacted {
				n := &res.Impacted[i]
				if n.Heuristic {
					cis.Heuristic = append(cis.Heuristic, n.ID)
				} else {
					cis.Certain = append(cis.Certain, n.ID)
				}
			}
		}
	}
	a.mu.Lock()
	// Drop the older versions of the window and every expired entry (the
	// windows that ended, were cancelled or deleted): the cache holds only
	// what was asked for within ActiveCacheTTL.
	for k, old := range a.cache {
		if (k.org == orgID && k.window == w.ID && !k.updated.Equal(w.UpdatedAt)) || now.Sub(old.at) >= ActiveCacheTTL {
			delete(a.cache, k)
		}
	}
	a.cache[key] = activeEntry{cis: cis, at: now}
	a.mu.Unlock()
	return cis, nil
}

// AlarmReleaser ends the suppression of the alarms of a window
// (alarm.Service.ReleaseMaintenance) and of the alarms no active window
// holds any more (alarm.Service.ReleaseStaleMaintenance).
type AlarmReleaser interface {
	ReleaseMaintenance(ctx context.Context, orgID, windowID string) (int, error)
	ReleaseStaleMaintenance(ctx context.Context, orgID string) (int, error)
}

// boundaryData is the data of maintenance.started and maintenance.ended:
// the frame of the window with the number of its CIs and, at the end, of
// the alarms released.
type boundaryData struct {
	events.MaintenanceWindowData
	CICount        int `json:"ci_count"`
	ReleasedAlarms int `json:"released_alarms,omitempty"`
}

// Boundaries is the system job maintenance.boundaries.
type Boundaries struct {
	pool     *pgxpool.Pool
	repo     Repository
	releaser AlarmReleaser
	now      func() time.Time
}

// NewBoundaries returns the job on pool with the windows of repo; releaser
// may be nil (no alarm is released).
func NewBoundaries(pool *pgxpool.Pool, repo Repository, releaser AlarmReleaser) *Boundaries {
	return &Boundaries{pool: pool, repo: repo, releaser: releaser, now: time.Now}
}

// WithClock replaces the clock (tests).
func (b *Boundaries) WithClock(now func() time.Time) *Boundaries {
	b.now = now
	return b
}

// Schedule is the system job (every minute, at most one missed run caught
// up).
func (b *Boundaries) Schedule() jobs.Schedule {
	return jobs.Schedule{Kind: BoundariesKind, Spec: BoundariesSpec, MaxCatchUp: 1, Run: func(ctx context.Context) error {
		_, err := b.Run(ctx)
		return err
	}}
}

// Run handles every organization; a failing one does not stop the others.
// It returns the number of events written.
func (b *Boundaries) Run(ctx context.Context) (int, error) {
	orgs, err := database.OrganizationIDs(ctx, b.pool)
	if err != nil {
		return 0, err
	}
	n := 0
	var errs []error
	for _, org := range orgs {
		k, oerr := b.RunOrg(ctx, org)
		n += k
		if oerr != nil {
			errs = append(errs, fmt.Errorf("organization %s: %w", org, oerr))
		}
	}
	return n, errors.Join(errs...)
}

// RunOrg writes the boundary events of one organization in one tenant
// transaction under the lock maintenance_boundaries:<org>.
func (b *Boundaries) RunOrg(ctx context.Context, orgID string) (int, error) {
	now := b.now().UTC()
	scope := database.OrgWideScope(orgID, "")
	written, stale := 0, 0
	var ended []string
	err := database.WithTenant(ctx, b.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		written, stale, ended = 0, 0, nil
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended('maintenance_boundaries:' || $1, 0))`, orgID); err != nil {
			return fmt.Errorf("lock maintenance boundaries: %w", err)
		}
		txCtx := database.ContextWithTx(ctx, tx)
		wins, err := b.repo.WindowsBetween(txCtx, orgID, now.Add(-BoundaryHorizon), now.Add(time.Microsecond))
		if err != nil {
			return err
		}
		type pending struct {
			typ  string
			w    *Window
			rel  int
			emit bool
		}
		var todo []pending
		for i := range wins {
			w := &wins[i]
			if !w.StartsAt.After(now) && w.StartsAt.After(now.Add(-BoundaryHorizon)) {
				todo = append(todo, pending{typ: events.MaintenanceStarted, w: w})
			}
			if !w.EndsAt.After(now) && w.EndsAt.After(now.Add(-BoundaryHorizon)) {
				todo = append(todo, pending{typ: events.MaintenanceEnded, w: w})
			}
		}
		for i := range todo {
			var seen bool
			if err = tx.QueryRow(txCtx, `SELECT EXISTS (SELECT 1 FROM event_outbox WHERE organization_id = $1 AND type = $2 AND entity_id = $3)`,
				orgID, todo[i].typ, todo[i].w.ID).Scan(&seen); err != nil {
				return fmt.Errorf("read boundary events: %w", err)
			}
			if seen {
				continue
			}
			todo[i].emit = true
			if todo[i].typ == events.MaintenanceEnded && b.releaser != nil {
				if todo[i].rel, err = b.releaser.ReleaseMaintenance(txCtx, orgID, todo[i].w.ID); err != nil {
					return fmt.Errorf("release the alarms of window %s: %w", todo[i].w.ID, err)
				}
				ended = append(ended, todo[i].w.ID)
			}
		}
		// Every run: the alarms still suppressed for a window that is no
		// longer active (cancelled or deleted while active, an observation
		// raised after the release of its window) are released as well.
		if b.releaser != nil {
			n, rerr := b.releaser.ReleaseStaleMaintenance(txCtx, orgID)
			if rerr != nil {
				return fmt.Errorf("release stale maintenance alarms: %w", rerr)
			}
			stale = n
		}
		return database.BeforeCommit(txCtx, func() error {
			outbox := events.NewOutbox()
			for i := range todo {
				t := &todo[i]
				if !t.emit {
					continue
				}
				data := boundaryData{MaintenanceWindowData: events.MaintenanceWindowData{ID: t.w.ID,
					StartsAt: t.w.StartsAt.UTC().Format(time.RFC3339), EndsAt: t.w.EndsAt.UTC().Format(time.RFC3339)},
					CICount: len(t.w.CIIDs), ReleasedAlarms: t.rel}
				if _, err := outbox.Append(txCtx, tx, &events.Event{OrgID: orgID, Type: t.typ, EntityID: t.w.ID, Data: data}); err != nil {
					return err
				}
				written++
			}
			return nil
		})
	})
	if err != nil {
		return 0, err
	}
	for _, id := range ended {
		slog.Info("maintenance window ended", "organization_id", orgID, "maintenance_window_id", id)
	}
	if stale > 0 {
		slog.Info("maintenance suppression released without an active window", "organization_id", orgID, "alarms", stale)
	}
	return written, nil
}
