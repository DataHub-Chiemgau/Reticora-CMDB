package audit

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
)

// RouteIntentsKind is the scheduler kind that turns overdue route intents
// into audit entries (WP-504).
const RouteIntentsKind = "audit.route_intents"

// RouteIntentGrace is the age from which the scheduler takes an intent: a
// request still running completes it itself before then.
const RouteIntentGrace = 2 * time.Minute

// OutcomeUnknown marks an entry the scheduler wrote from an intent whose
// request never completed it: the request may or may not have changed data.
const OutcomeUnknown = "unknown"

// routeIntentBatch bounds the intents of one organization one transaction
// of the scheduler takes.
const routeIntentBatch = 500

// RouteIntentsPending is the number of route intents that wait for their
// completion or the scheduler, read by every run of the scheduler (OPS-05).
var RouteIntentsPending = prometheus.NewGauge(prometheus.GaugeOpts{
	Namespace: "reticora",
	Name:      "audit_route_intents_pending",
	Help:      "Route audit intents not yet completed or turned into audit entries by the scheduler.",
})

// RegisterMetrics registers the audit metrics on reg.
func RegisterMetrics(reg prometheus.Registerer) {
	reg.MustRegister(RouteIntentsPending)
}

// RouteIntent is the audit intent of a write request of a route without a
// domain recorder (AUD-07, WP-504): stored before the handler runs, it
// names who (actor), what (route, action, resource, the names of the
// written fields) and the request; it holds no values (CH16).
type RouteIntent struct {
	ID             string
	OrganizationID string
	RequestID      string
	CorrelationID  string
	// Route is "METHOD pattern".
	Route        string
	Action       string
	ResourceType string
	// ResourceKey is the id, key or name the route addresses, if any.
	ResourceKey string
	ActorID     string
	ActorType   string
	Source      string
	Fields      []string
	CreatedAt   time.Time
}

// PGRouteAuditor stores the route audit in the organization's chain (E-08):
// intents in audit_route_intent, entries in audit_log, each in an org-wide
// tenant transaction.
type PGRouteAuditor struct {
	pool     *pgxpool.Pool
	recorder *PGRecorder
	now      func() time.Time
}

// NewPGRouteAuditor returns the route auditor on pool.
func NewPGRouteAuditor(pool *pgxpool.Pool) *PGRouteAuditor {
	return &PGRouteAuditor{pool: pool, recorder: NewPGRecorder(), now: time.Now}
}

func (a *PGRouteAuditor) inOrg(ctx context.Context, orgID string, fn func(ctx context.Context, tx pgx.Tx) error) error {
	scope := database.OrgWideScope(orgID, "")
	return database.WithTenant(ctx, a.pool, &scope, fn)
}

// RecordRoute writes the entry of a write request of a route with a domain
// recorder whose recorder wrote none.
func (a *PGRouteAuditor) RecordRoute(ctx context.Context, entry *Entry) error {
	return a.inOrg(ctx, entry.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := a.recorder.Record(ctx, tx, *entry)
		return err
	})
}

// Intend stores intent in a transaction of its own and sets its id.
func (a *PGRouteAuditor) Intend(ctx context.Context, intent *RouteIntent) error {
	fields := intent.Fields
	if fields == nil {
		fields = []string{}
	}
	return a.inOrg(ctx, intent.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			INSERT INTO audit_route_intent (organization_id, request_id, correlation_id, route, action, resource_type,
				resource_key, actor_id, actor_type, source, fields)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			RETURNING id::text, created_at`,
			intent.OrganizationID, nilIfEmpty(intent.RequestID), nilIfEmpty(intent.CorrelationID), intent.Route,
			intent.Action, intent.ResourceType, nilIfEmpty(intent.ResourceKey), nilIfEmpty(uuidOrEmpty(intent.ActorID)),
			intent.ActorType, sourceOrDefault(intent.Source), fields).Scan(&intent.ID, &intent.CreatedAt)
	})
}

// Complete takes intent with DELETE ... RETURNING and appends entry in the
// same transaction. An intent the scheduler took already gets no second
// entry; on an error the intent stays for the scheduler.
func (a *PGRouteAuditor) Complete(ctx context.Context, intent *RouteIntent, entry *Entry) error {
	return a.inOrg(ctx, intent.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `DELETE FROM audit_route_intent WHERE id = $1 RETURNING id::text`, intent.ID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("audit: take route intent: %w", err)
		}
		_, err = a.recorder.Record(ctx, tx, *entry)
		return err
	})
}

// Discard deletes intent without an entry: the request was refused or a
// domain recorder wrote its entry.
func (a *PGRouteAuditor) Discard(ctx context.Context, intent *RouteIntent) error {
	return a.inOrg(ctx, intent.OrganizationID, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM audit_route_intent WHERE id = $1`, intent.ID)
		return err
	})
}

// SweepRouteIntents turns every intent older than RouteIntentGrace into an
// audit entry with outcome unknown and returns how many it wrote (the run
// of RouteIntentsKind). The system flag finds the organizations with due
// intents; each organization's intents are taken with DELETE ... RETURNING
// and recorded in one tenant transaction, so a failure keeps them for the
// next run (retry) and an intent a completion holds is skipped. Afterwards
// it sets RouteIntentsPending.
func (a *PGRouteAuditor) SweepRouteIntents(ctx context.Context) (int, error) {
	cutoff := a.now().Add(-RouteIntentGrace)
	var orgs []string
	err := database.WithSystem(ctx, a.pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT DISTINCT organization_id::text FROM audit_route_intent WHERE created_at < $1`, cutoff)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("%s: find due intents: %w", RouteIntentsKind, err)
	}
	total := 0
	var firstErr error
	for _, orgID := range orgs {
		n, err := a.sweepOrganization(ctx, orgID, cutoff)
		if err != nil {
			slog.Error("route audit intents of an organization stay for the next run", "kind", RouteIntentsKind,
				"organization_id", orgID, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s for %s: %w", RouteIntentsKind, orgID, err)
			}
			continue
		}
		total += n
	}
	if pending, err := a.PendingRouteIntents(ctx); err == nil {
		RouteIntentsPending.Set(float64(pending))
	} else if firstErr == nil {
		firstErr = err
	}
	return total, firstErr
}

// sweepOrganization records the due intents of one organization, a batch per
// transaction.
func (a *PGRouteAuditor) sweepOrganization(ctx context.Context, orgID string, cutoff time.Time) (int, error) {
	total := 0
	for {
		n := 0
		err := a.inOrg(ctx, orgID, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				DELETE FROM audit_route_intent WHERE id IN (
					SELECT id FROM audit_route_intent WHERE created_at < $1
					ORDER BY created_at LIMIT $2 FOR UPDATE SKIP LOCKED)
				RETURNING id::text, organization_id::text, COALESCE(request_id, ''), COALESCE(correlation_id, ''),
					route, action, resource_type, COALESCE(resource_key, ''), COALESCE(actor_id::text, ''),
					actor_type, source, fields, created_at`, cutoff, routeIntentBatch)
			if err != nil {
				return err
			}
			intents, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (RouteIntent, error) {
				var in RouteIntent
				scanErr := row.Scan(&in.ID, &in.OrganizationID, &in.RequestID, &in.CorrelationID, &in.Route, &in.Action,
					&in.ResourceType, &in.ResourceKey, &in.ActorID, &in.ActorType, &in.Source, &in.Fields, &in.CreatedAt)
				return in, scanErr
			})
			if err != nil {
				return err
			}
			for i := range intents {
				if _, err := a.recorder.Record(ctx, tx, intents[i].unknownOutcome()); err != nil {
					return err
				}
			}
			n = len(intents)
			return nil
		})
		if err != nil {
			return total, err
		}
		total += n
		if n < routeIntentBatch {
			return total, nil
		}
	}
}

// PendingRouteIntents counts the intents of every organization (system
// read).
func (a *PGRouteAuditor) PendingRouteIntents(ctx context.Context) (int, error) {
	var n int
	err := database.WithSystem(ctx, a.pool, func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, `SELECT count(*) FROM audit_route_intent`).Scan(&n)
	})
	if err != nil {
		return 0, fmt.Errorf("%s: count pending intents: %w", RouteIntentsKind, err)
	}
	return n, nil
}

// unknownOutcome is the entry of an intent its request never completed.
func (in *RouteIntent) unknownOutcome() Entry {
	changes := map[string]interface{}{"route": in.Route, "outcome": OutcomeUnknown,
		"requested_at": in.CreatedAt.UTC().Format(time.RFC3339Nano)}
	resourceID := in.ResourceKey
	if !IsUUID(resourceID) {
		if resourceID != "" {
			changes["resource_key"] = resourceID
		}
		resourceID = ""
	}
	if len(in.Fields) > 0 {
		changes["fields"] = in.Fields
	}
	return Entry{OrganizationID: in.OrganizationID, ActorID: in.ActorID, ActorType: in.ActorType, Action: in.Action,
		ResourceType: in.ResourceType, ResourceID: resourceID, Changes: changes, Source: in.Source,
		RequestID: in.RequestID, CorrelationID: in.CorrelationID}
}

func uuidOrEmpty(s string) string {
	if IsUUID(s) {
		return s
	}
	return ""
}
