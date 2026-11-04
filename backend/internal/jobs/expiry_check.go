package jobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
)

// Expiry notices of contracts and warranties (NTF-05 [P2], MGT-12, WP-270):
// every day the run expiry.check reports each contract and each asset
// warranty that reaches a stage before its due date, contract.expiring and
// asset.warranty_expiring. The due date of a contract is end_date minus
// notice_period_days (without a notice period end_date), that of an asset
// its warranty_until. The days left are the due date minus the local date
// of the run in COALESCE(client.timezone, organization.timezone) (LOC-01,
// LOC-02). A stage is reached while 0 <= days left <= stage; only the
// smallest reached stage is reported, so a first run five days ahead
// reports the stage of 7 days, not that of 30 as well.
//
// Each notice is recorded in notice_sent (kind = event type, dedup_key =
// object/stage/due date) in the transaction that appends its event to the
// outbox (sendNotice, WP-425): it goes out exactly once per object, stage
// and due date, also after the outbox cleanup removed the published event.
// A moved due date starts the stages again; a missed day is caught up by
// the next run. Terminated and ended contracts and assets in an end state
// (retired, a terminal state of their lifecycle model, before a state the
// legacy statuses retired, disposed and lost) stay silent.

// ExpiryCheckKind is the scheduler kind of the run.
const ExpiryCheckKind = "expiry.check"

// ExpiryCheckBatch is the number of objects one transaction of the run
// reports at most.
const ExpiryCheckBatch = 500

// ContractExpiryStages and WarrantyExpiryStages are the stages in days
// before the due date, descending.
var (
	ContractExpiryStages = []int{30, 7, 1}
	WarrantyExpiryStages = []int{30, 7}
)

// ExpiryCheck is the expiry.check run of the scheduler.
type ExpiryCheck struct {
	pool *pgxpool.Pool
	// Licensed reports whether the organization holds an active
	// assets_basic; nil treats every organization as licensed. An
	// organization without it is skipped, for contracts and warranties
	// alike.
	Licensed func(ctx context.Context, orgID string) (bool, error)
	now      func() time.Time
	batch    int
}

// NewExpiryCheck creates the run on pool.
func NewExpiryCheck(pool *pgxpool.Pool) *ExpiryCheck {
	return &ExpiryCheck{pool: pool, now: func() time.Time { return time.Now().UTC() }, batch: ExpiryCheckBatch}
}

// WithClock replaces the clock (tests).
func (c *ExpiryCheck) WithClock(now func() time.Time) *ExpiryCheck {
	c.now = now
	return c
}

// Schedule runs the check daily at 05:07 UTC; a missed day is caught up
// once.
func (c *ExpiryCheck) Schedule() Schedule {
	return Schedule{Kind: ExpiryCheckKind, Spec: "7 5 * * *", MaxCatchUp: 1, Timeout: 30 * time.Minute,
		Run: func(ctx context.Context) error {
			sent, err := c.Run(ctx)
			if sent > 0 {
				slog.Info("expiry notices sent", "notices", sent)
			}
			return err
		}}
}

// Run checks every organization and returns the number of notices sent.
// A failing organization does not stop the others: its open batch is
// rolled back and sent by the next run; the errors are joined at the end.
func (c *ExpiryCheck) Run(ctx context.Context) (int, error) {
	// The organizations as system read (database.WithSystem), like
	// eachNoticeOrganization.
	orgs, err := database.OrganizationIDs(ctx, c.pool)
	if err != nil {
		return 0, fmt.Errorf("%s: list organizations: %w", ExpiryCheckKind, err)
	}
	total := 0
	var errs []error
	for _, orgID := range orgs {
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		sent, oerr := c.RunOrg(ctx, orgID)
		total += sent
		if oerr != nil {
			slog.Error("notice run", "kind", ExpiryCheckKind, "organization_id", orgID, "error", oerr)
			errs = append(errs, fmt.Errorf("%s for %s: %w", ExpiryCheckKind, orgID, oerr))
		}
	}
	return total, errors.Join(errs...)
}

// RunOrg checks one organization: its contracts, then its asset
// warranties, in batches of at most ExpiryCheckBatch objects, each in its
// own transaction with the org-wide scope. It returns the number of
// notices sent.
func (c *ExpiryCheck) RunOrg(ctx context.Context, orgID string) (int, error) {
	scope := database.OrgWideScope(orgID, "")
	ctx = database.ContextWithTenantScope(ctx, &scope)
	if c.Licensed != nil {
		licensed, err := c.Licensed(ctx, orgID)
		if err != nil {
			return 0, fmt.Errorf("assets_basic license: %w", err)
		}
		if !licensed {
			return 0, nil
		}
	}
	now := c.now()
	total := 0
	for _, check := range []func(ctx context.Context, tx pgx.Tx, orgID, after string, now time.Time) (int, int, string, error){
		c.contractBatch, c.warrantyBatch,
	} {
		after := firstID
		for {
			var rows, sent int
			err := database.WithTenant(ctx, c.pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
				var berr error
				rows, sent, after, berr = check(ctx, tx, orgID, after, now)
				return berr
			})
			if err != nil {
				return total, err
			}
			total += sent
			if rows < c.batch {
				break
			}
		}
	}
	return total, nil
}

// expiryStage returns the smallest of the descending stages that daysLeft
// reaches (0 <= daysLeft <= stage), 0 for none.
func expiryStage(stages []int, daysLeft int) int {
	if daysLeft < 0 {
		return 0
	}
	stage := 0
	for _, s := range stages {
		if daysLeft <= s {
			stage = s
		}
	}
	return stage
}

// localToday is the SQL of the local date of the run ($2) in the time zone
// of the object's client, else of its organization (aliases cl and o).
const localToday = `(($2::timestamptz) AT TIME ZONE COALESCE(cl.timezone, o.timezone))::date`

// contractBatch reports the contracts after the id after whose due date
// lies within the largest stage; rows is the number of contracts read,
// last the id of the last one.
func (c *ExpiryCheck) contractBatch(ctx context.Context, tx pgx.Tx, orgID, after string, now time.Time) (rows, sent int, last string, err error) {
	result, err := tx.Query(ctx, `
		SELECT k.id::text, COALESCE(k.client_id::text, ''), COALESCE(k.supplier_id::text, ''), k.number,
		       COALESCE(k.kind, ''), COALESCE(k.notice_period_days, 0), k.end_date, d.due, d.due - `+localToday+`
		  FROM contract k
		  JOIN organization o ON o.id = k.organization_id
		  LEFT JOIN client cl ON cl.id = k.client_id AND cl.organization_id = k.organization_id
		 CROSS JOIN LATERAL (SELECT k.end_date - COALESCE(k.notice_period_days, 0) AS due) d
		 WHERE k.organization_id = $1 AND k.end_date IS NOT NULL AND k.status NOT IN ('terminated', 'ended')
		   AND k.id > $3::uuid
		   AND d.due - `+localToday+` BETWEEN 0 AND $4
		 ORDER BY k.id
		 LIMIT $5`, orgID, now, after, ContractExpiryStages[0], c.batch)
	if err != nil {
		return 0, 0, after, fmt.Errorf("list expiring contracts: %w", err)
	}
	type contractRow struct {
		data     events.ContractExpiringData
		end, due time.Time
	}
	list, err := pgx.CollectRows(result, func(row pgx.CollectableRow) (contractRow, error) {
		var r contractRow
		scanErr := row.Scan(&r.data.ID, &r.data.ClientID, &r.data.SupplierID, &r.data.Number, &r.data.Kind,
			&r.data.NoticePeriodDays, &r.end, &r.due, &r.data.DaysLeft)
		return r, scanErr
	})
	if err != nil {
		return 0, 0, after, fmt.Errorf("list expiring contracts: %w", err)
	}
	for i := range list {
		r := &list[i]
		last = r.data.ID
		r.data.Stage = expiryStage(ContractExpiryStages, r.data.DaysLeft)
		if r.data.Stage == 0 {
			continue
		}
		r.data.DueDate, r.data.ExpiresAt = r.due.Format(time.DateOnly), r.end.Format(time.DateOnly)
		key := fmt.Sprintf("%s/%d/%s", r.data.ID, r.data.Stage, r.data.DueDate)
		ok, serr := sendNotice(ctx, tx, now, key, &events.Event{OrgID: orgID, Type: events.ContractExpiring, EntityID: r.data.ID, Data: r.data})
		if serr != nil {
			return len(list), sent, last, serr
		}
		if ok {
			sent++
		}
	}
	return len(list), sent, last, nil
}

// warrantyBatch reports the assets after the id after whose warranty ends
// within the largest stage; assets in an end state are left out.
func (c *ExpiryCheck) warrantyBatch(ctx context.Context, tx pgx.Tx, orgID, after string, now time.Time) (rows, sent int, last string, err error) {
	result, err := tx.Query(ctx, `
		SELECT a.id::text, COALESCE(a.client_id::text, ''), COALESCE(a.site_id::text, ''), a.name, a.inventory_no,
		       COALESCE(a.owner_user_id::text, ''), COALESCE(a.owner_team_id::text, ''), COALESCE(a.ci_id::text, ''),
		       a.warranty_until, a.warranty_until - `+localToday+`
		  FROM asset a
		  JOIN organization o ON o.id = a.organization_id
		  LEFT JOIN client cl ON cl.id = a.client_id AND cl.organization_id = a.organization_id
		 WHERE a.organization_id = $1 AND a.warranty_until IS NOT NULL
		   AND a.id > $3::uuid
		   AND a.warranty_until - `+localToday+` BETWEEN 0 AND $4
		   AND CASE WHEN a.lifecycle_state IS NULL THEN a.status NOT IN ('retired', 'disposed', 'lost')
		            ELSE a.lifecycle_state <> 'retired' AND NOT EXISTS (
		                SELECT 1 FROM lifecycle_state s
		                 WHERE s.definition_id = COALESCE(lifecycle_model_effective(a.asset_type_id, 'asset'), $6::uuid)
		                   AND s.key = a.lifecycle_state AND s.is_terminal)
		       END
		 ORDER BY a.id
		 LIMIT $5`, orgID, now, after, WarrantyExpiryStages[0], c.batch, physicalAssetModelID)
	if err != nil {
		return 0, 0, after, fmt.Errorf("list expiring warranties: %w", err)
	}
	type warrantyRow struct {
		data events.AssetWarrantyData
		end  time.Time
	}
	list, err := pgx.CollectRows(result, func(row pgx.CollectableRow) (warrantyRow, error) {
		var r warrantyRow
		scanErr := row.Scan(&r.data.ID, &r.data.ClientID, &r.data.SiteID, &r.data.Name, &r.data.InventoryNo,
			&r.data.OwnerUserID, &r.data.OwnerTeamID, &r.data.PrimaryCIID, &r.end, &r.data.DaysLeft)
		return r, scanErr
	})
	if err != nil {
		return 0, 0, after, fmt.Errorf("list expiring warranties: %w", err)
	}
	for i := range list {
		r := &list[i]
		last = r.data.ID
		r.data.Stage = expiryStage(WarrantyExpiryStages, r.data.DaysLeft)
		if r.data.Stage == 0 {
			continue
		}
		r.data.ExpiresAt = r.end.Format(time.DateOnly)
		r.data.DueDate = r.data.ExpiresAt
		key := fmt.Sprintf("%s/%d/%s", r.data.ID, r.data.Stage, r.data.DueDate)
		ok, serr := sendNotice(ctx, tx, now, key, &events.Event{OrgID: orgID, Type: events.AssetWarrantyExpiring, EntityID: r.data.ID, Data: r.data})
		if serr != nil {
			return len(list), sent, last, serr
		}
		if ok {
			sent++
		}
	}
	return len(list), sent, last, nil
}

// firstID is below every object id: the keyset start of a batch walk.
const firstID = "00000000-0000-0000-0000-000000000000"

// physicalAssetModelID is the system lifecycle model of assets whose type
// binds none (DefaultPhysicalAssetLifecycle).
const physicalAssetModelID = "00000000-0000-0000-0000-000000000101"
