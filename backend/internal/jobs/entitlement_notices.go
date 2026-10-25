package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Entitlement notices of ENT-07 on the scheduler (JOB-02, WP-425): every
// day each enabled entitlement with valid_until is warned of 14, 7 and 1
// day before its end (entitlement.warning) and reported once after it
// (entitlement.expired). This run is the only producer of
// entitlement.warning. Each notice is recorded in notice_sent in the
// transaction that appends its event to the outbox, so it goes out exactly
// once per entitlement, valid_until and threshold, also after the outbox
// cleanup removed the published event (KeepPublished). A changed
// valid_until starts the thresholds again. After missed runs only the
// youngest due threshold is sent (bounded catch-up): a first run six days
// ahead warns once, for 7 days, not for 14 days as well.

// EntitlementNoticesKind is the scheduler kind of the run.
const EntitlementNoticesKind = "entitlement.notices"

// EntitlementWarningDays are the thresholds of entitlement.warning in days
// before valid_until (ENT-07), descending.
var EntitlementWarningDays = []int{14, 7, 1}

// EntitlementNotice is the data of entitlement.warning (feature,
// valid_until, days_left) and entitlement.expired (feature, expired_at);
// the entitlement is the entity of the event.
type EntitlementNotice struct {
	Feature    string `json:"feature"`
	ValidUntil string `json:"valid_until,omitempty"`
	// DaysLeft is the number of days until valid_until, rounded up.
	DaysLeft  int    `json:"days_left,omitempty"`
	ExpiredAt string `json:"expired_at,omitempty"`
}

// EntitlementNotices sends the entitlement notices.
type EntitlementNotices struct {
	pool *pgxpool.Pool
	now  func() time.Time
}

// NewEntitlementNotices creates the run on pool.
func NewEntitlementNotices(pool *pgxpool.Pool) *EntitlementNotices {
	return &EntitlementNotices{pool: pool, now: func() time.Time { return time.Now().UTC() }}
}

// WithClock replaces the clock (tests).
func (n *EntitlementNotices) WithClock(now func() time.Time) *EntitlementNotices {
	n.now = now
	return n
}

// Schedule runs the notices daily; a missed day is caught up once.
func (n *EntitlementNotices) Schedule() Schedule {
	return Schedule{Kind: EntitlementNoticesKind, Spec: "13 4 * * *", MaxCatchUp: 1, Timeout: 15 * time.Minute,
		Run: func(ctx context.Context) error {
			sent, err := n.Run(ctx)
			if sent > 0 {
				slog.Info("entitlement notices sent", "notices", sent)
			}
			return err
		}}
}

// Run sends the due notices of every organization and returns how many it
// sent.
func (n *EntitlementNotices) Run(ctx context.Context) (int, error) {
	now := n.now()
	return eachNoticeOrganization(ctx, n.pool, EntitlementNoticesKind, func(ctx context.Context, tx pgx.Tx, orgID string) (int, error) {
		rows, err := tx.Query(ctx, `SELECT id::text, feature_key, valid_until FROM entitlement
			WHERE organization_id = $1 AND enabled AND valid_until IS NOT NULL ORDER BY feature_key`, orgID)
		if err != nil {
			return 0, fmt.Errorf("list entitlements: %w", err)
		}
		type entitlementRow struct {
			id, feature string
			validUntil  time.Time
		}
		list, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (entitlementRow, error) {
			var e entitlementRow
			scanErr := row.Scan(&e.id, &e.feature, &e.validUntil)
			return e, scanErr
		})
		if err != nil {
			return 0, fmt.Errorf("list entitlements: %w", err)
		}
		sent := 0
		for _, e := range list {
			typ, key, data := entitlementNotice(e.id, e.feature, e.validUntil, now)
			if typ == "" {
				continue
			}
			ok, err := sendNotice(ctx, tx, now, key, &events.Event{OrgID: orgID, Type: typ, EntityID: e.id, Data: data})
			if err != nil {
				return sent, err
			}
			if ok {
				sent++
			}
		}
		return sent, nil
	})
}

// entitlementNotice returns the notice an entitlement is due for at now:
// its event type (empty: none), dedup key and data. Before valid_until it
// is the warning of the youngest threshold reached, from valid_until on the
// expiry.
func entitlementNotice(id, feature string, validUntil, now time.Time) (typ, key string, data *EntitlementNotice) {
	until := validUntil.UTC().Format(time.RFC3339)
	left := validUntil.Sub(now)
	if left <= 0 {
		return events.EntitlementExpired, id + "/" + until, &EntitlementNotice{Feature: feature, ExpiredAt: until}
	}
	const day = 24 * time.Hour
	daysLeft := int((left + day - 1) / day)
	threshold := 0
	for _, days := range EntitlementWarningDays {
		if daysLeft <= days {
			threshold = days
		}
	}
	if threshold == 0 {
		return "", "", nil
	}
	return events.EntitlementWarning, fmt.Sprintf("%s/%s/%d", id, until, threshold),
		&EntitlementNotice{Feature: feature, ValidUntil: until, DaysLeft: daysLeft}
}

// sendNotice records the notice of ev in notice_sent (kind = event type,
// dedup_key = key) and appends ev to the outbox in tx, the transaction of
// the event's organization. A notice sent before appends nothing and
// reports false.
func sendNotice(ctx context.Context, tx pgx.Tx, now time.Time, key string, ev *events.Event) (bool, error) {
	tag, err := tx.Exec(ctx, `INSERT INTO notice_sent (organization_id, kind, dedup_key, sent_at) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, ev.OrgID, ev.Type, key, now)
	if err != nil {
		return false, fmt.Errorf("record notice %s: %w", ev.Type, err)
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	if _, err = events.NewOutbox().Append(ctx, tx, ev); err != nil {
		return false, err
	}
	return true, nil
}

// eachNoticeOrganization runs fn for every organization in its own
// organization-wide transaction. A failing organization does not stop the
// others; its notices are rolled back and sent by the next run.
func eachNoticeOrganization(ctx context.Context, pool *pgxpool.Pool, kind string,
	fn func(ctx context.Context, tx pgx.Tx, orgID string) (int, error)) (int, error) {
	var orgs []string
	err := database.WithSystem(ctx, pool, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT id::text FROM organization ORDER BY id`)
		if err != nil {
			return err
		}
		orgs, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", kind, err)
	}
	total := 0
	var firstErr error
	for _, orgID := range orgs {
		sent := 0
		scope := database.OrgWideScope(orgID, "")
		err := database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			var ferr error
			sent, ferr = fn(ctx, tx, orgID)
			return ferr
		})
		if err != nil {
			slog.Error("notice run", "kind", kind, "organization_id", orgID, "error", err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s for %s: %w", kind, orgID, err)
			}
			continue
		}
		total += sent
	}
	return total, firstErr
}
