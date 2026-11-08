package main

import (
	"context"
	"crypto/md5" //nolint:gosec // deterministic serial numbers, not a security use
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contract"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// The assets and the stock of the Phase-2 seed (WP-819, SIM-01 [P2]):
// suppliers and contracts, assets linked to the mounted [B] CIs, two blade
// chassis with their configuration children, a storage branch below the
// site hq with assets and consumables in its bins, internal orders in every
// state and a running stocktake. Everything goes through the services of
// the Phase-2 modules, which join the transaction of stockSteps.

// stockContent is the asset and stock content of a Phase-2 level.
type stockContent struct {
	// suppliers and contracts: 2 and 3 (E-41, WP-269).
	suppliers bool
	// linked assets with the mounted [B] CIs 1..linked; chassis adds the
	// two blade chassis with an asset each and four blades each.
	linked  int
	chassis bool
	// stock assets in the bins, bins of the storage branch and consumables.
	stock, bins, consumables int
	// orders in every state, the running stocktake and the field grants of
	// the asset fields.
	orders, stocktake, assetGrants bool
	// fieldValues: the provenance of the [B] CIs in ci_field_value
	// (ensureFieldValues, WP-821).
	fieldValues bool
}

// stockContentOf is the content of the level: the full one for demo, a
// subset for acme (10 assets, 5 of them linked, a branch with one bin and
// 2 consumables with stock), none for the small organizations.
func stockContentOf(level phase2Level) *stockContent {
	switch level {
	case phase2Full:
		return &stockContent{suppliers: true, linked: baseObjects, chassis: true, stock: 18, bins: 10, consumables: 20,
			orders: true, stocktake: true, assetGrants: true, fieldValues: true}
	case phase2Subset:
		return &stockContent{linked: 5, stock: 5, bins: 1, consumables: 2}
	default:
		return nil
	}
}

// The composition of the demo organization (WP-273).
const (
	chassisCount     = 2
	bladesPerChassis = 4
)

// stockSeed holds the transaction of the asset and stock steps of an
// organization, the services they write through and the ids found or
// created, by key.
type stockSeed struct {
	org             *orgSeed
	tx              pgx.Tx
	admin, engineer string
	content         *stockContent

	suppliers   *contract.PGRepository
	contracts   *contract.PGContracts
	assets      *asset.PGRepository
	cis         *ci.Service
	grants      *permission.PGFieldGrants
	consumables *consumable.PGRepository
	movements   *movement.Service
	orders      *order.PGRepository
	stocktakes  *stocktake.PGRepository
	overrides   *override.PGRepository

	supplierIDs   []string
	contractIDs   []string
	assetIDs      map[int]string
	consumableIDs []string
}

// stockSteps runs the asset and stock steps of the organization after its
// Phase-2 metamodel (phase2Steps), in a transaction of their own through
// database.WithTenant, organization-wide as the administrator, the actor of
// the audit entries; the services join it (database.ContextWithTx). The
// CIs are written through the CI service of the server (computed
// attributes, history, audit, events and search documents), so the links
// leave ci.asset_id, ci_change and warranty_days_left as the application
// does. The assets and consumables beyond those of the level (WP-821) are
// written set-based before the stocktake freezes the branch, and the [B]
// CIs get their field values last. Every object is found again by its
// key, so a second run books nothing twice.
func (o *orgSeed) stockSteps(ctx context.Context, pool *pgxpool.Pool) error {
	content := stockContentOf(o.phase2)
	if content == nil {
		return nil
	}
	admin := o.uid("user", "admin")
	scope := database.OrgWideScope(o.id, admin)
	ctx = database.ContextWithTenantScope(ctx, &scope)
	ctx = tenant.WithTenant(ctx, tenant.TenantInfo{OrganizationID: o.id, UserID: admin})
	types := citype.NewPGRepository(pool)
	cis := ci.NewIndexingRepository(
		ci.NewPGRepositoryWithAudit(pool, audit.NewPGRecorder()).WithEvents(events.NewOutbox()).WithComputed(types),
		search.NewCIIndexer(search.NewPGRepository(pool)))
	return database.WithTenant(ctx, pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
		s := &stockSeed{org: o, tx: tx, admin: admin, content: content,
			suppliers: contract.NewPGRepository(pool), contracts: contract.NewPGContracts(pool),
			assets: asset.NewPGRepository(pool).WithEvents(events.NewOutbox()), cis: ci.NewService(cis).WithFieldResolver(types),
			grants: permission.NewPGFieldGrants(pool), consumables: consumable.NewPGRepository(pool),
			movements: movement.NewService(movement.NewPGRepository(pool)), orders: order.NewPGRepository(pool),
			stocktakes: stocktake.NewPGRepository(pool), overrides: override.NewPGRepository(pool), assetIDs: map[int]string{}}
		if content.orders {
			s.engineer = o.uid("user", "engineer")
		}
		ctx = database.ContextWithTx(ctx, tx)
		steps := []struct {
			name string
			fn   func(context.Context) error
		}{
			{"suppliers and contracts", s.ensureSuppliers},
			{"storage branch", s.ensureStorage},
			{"composition", s.ensureComposition},
			{"assets", s.ensureAssets},
			{"scaled assets", s.ensureScaledAssets},
			{"asset field permissions", s.ensureAssetGrants},
			{"consumables", s.ensureConsumables},
			{"scaled consumables", s.ensureScaledConsumables},
			{"orders", s.ensureOrders},
			{"stocktake", s.ensureStocktake},
			{"field values", s.ensureFieldValues},
		}
		for _, step := range steps {
			if err := step.fn(ctx); err != nil {
				return fmt.Errorf("%s: %w", step.name, err)
			}
		}
		return nil
	})
}

// lookup returns the id the query finds, "" for none.
func (s *stockSeed) lookup(ctx context.Context, query string, args ...any) (string, error) {
	var id string
	err := s.tx.QueryRow(ctx, query, args...).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// day is the date days after the anchor, YYYY-MM-DD.
func (s *stockSeed) day(days int) string {
	return s.org.at.AddDate(0, 0, days).Format(time.DateOnly)
}

// seedSerial is the serial number of a seeded CI, as orgSeed.cis derives
// it in PostgreSQL: 'SN' || upper(substr(md5(<prefix>/serial/<key>), 1, 12)).
func seedSerial(prefix, key string) string {
	sum := md5.Sum([]byte(prefix + "/serial/" + key)) //nolint:gosec // deterministic serial numbers
	return "SN" + strings.ToUpper(hex.EncodeToString(sum[:])[:12])
}

// The suppliers and contracts of the demo organization (E-41, WP-269),
// dates relative to the anchor: one contract runs, one ends within 30 days
// and one has ended.
var (
	seedSuppliers = []string{"Reticora Supply GmbH", "Northwind Hardware AG"}
	seedContracts = []struct {
		number, kind, status string
		supplier, start, end int
	}{
		{"SEED-C-001", "maintenance", "active", 0, -365, 365},
		{"SEED-C-002", "support", "active", 1, -345, 20},
		{"SEED-C-003", "lease", "ended", 0, -1095, -30},
	}
)

// ensureSuppliers creates the suppliers and contracts, found again by
// supplier name and contract number.
func (s *stockSeed) ensureSuppliers(ctx context.Context) error {
	if !s.content.suppliers {
		return nil
	}
	for _, name := range seedSuppliers {
		id, err := s.lookup(ctx, `SELECT id::text FROM supplier WHERE organization_id = $1 AND name = $2`, s.org.id, name)
		if err != nil {
			return err
		}
		if id == "" {
			notes := "Seed (WP-819)"
			sup := &contract.Supplier{OrganizationID: s.org.id, Name: name, Notes: &notes}
			if err = s.suppliers.CreateSupplier(ctx, sup); err != nil {
				return fmt.Errorf("supplier %s: %w", name, err)
			}
			id = sup.ID
		}
		s.supplierIDs = append(s.supplierIDs, id)
	}
	for _, c := range seedContracts {
		id, err := s.lookup(ctx, `SELECT id::text FROM contract WHERE organization_id = $1 AND number = $2`, s.org.id, c.number)
		if err != nil {
			return err
		}
		if id == "" {
			kind, start, end, supplier := c.kind, s.day(c.start), s.day(c.end), s.supplierIDs[c.supplier]
			notice := 90
			item := &contract.Contract{OrganizationID: s.org.id, SupplierID: &supplier, Number: c.number, Kind: &kind,
				StartDate: &start, EndDate: &end, NoticePeriodDays: &notice, Status: c.status}
			if err = s.contracts.CreateContract(ctx, item); err != nil {
				return fmt.Errorf("contract %s: %w", c.number, err)
			}
			id = item.ID
		}
		s.contractIDs = append(s.contractIDs, id)
	}
	return nil
}

// chassisID and bladeID are the deterministic ids of the Phase-2 CIs.
func (s *stockSeed) chassisID(n int) string { return s.org.uid("ci", "chassis", fmt.Sprint(n)) }
func (s *stockSeed) bladeID(n, k int) string {
	return s.org.uid("ci", "blade", fmt.Sprint(n), fmt.Sprint(k))
}

// ensureComposition creates the blade chassis of the demo organization and
// their blades (WP-273): every chassis a CI of the type blade_chassis in the
// server room hq of the client hq, every blade a CI of the type
// blade_server below it with child_mode configuration and no asset of its
// own (ci_configuration_child_asset); its location and client are the
// chassis' (ci_configuration_child). They have deterministic ids, no
// relationship, interface or address; the chassis get their assets in
// ensureAssets.
func (s *stockSeed) ensureComposition(ctx context.Context) error {
	if !s.content.chassis {
		return nil
	}
	typeOf := func(key string) (string, error) {
		id, err := s.lookup(ctx, `SELECT id::text FROM ci_type WHERE organization_id = $1 AND key = $2`, s.org.id, key)
		if err == nil && id == "" {
			err = fmt.Errorf("type %s not found", key)
		}
		return id, err
	}
	chassisType, err := typeOf(ChassisTypeKey)
	if err != nil {
		return err
	}
	bladeType, err := typeOf(BladeTypeKey)
	if err != nil {
		return err
	}
	client, room := s.org.uid("client", "hq"), s.org.uid("room", "hq")
	for n := 1; n <= chassisCount; n++ {
		name := fmt.Sprintf("bc-%02d", n)
		if _, err = s.tx.Exec(ctx, `INSERT INTO ci (id, organization_id, client_id, location_id, ci_type_id, name, status,
				manufacturer, model, serial_number, hostname, attributes)
			VALUES ($1, $2, $3, $4, $5, $6, 'active', 'Reticora Labs', 'BX-8', $7, $6,
				jsonb_build_object('chassis_slots', $8::int, 'chassis_asset_tag', $9::text))
			ON CONFLICT (id) DO NOTHING`, s.chassisID(n), s.org.id, client, room, chassisType, name,
			seedSerial(s.org.prefix, "chassis/"+fmt.Sprint(n)), bladesPerChassis, fmt.Sprintf("BC-%04d", n)); err != nil {
			return fmt.Errorf("chassis %s: %w", name, err)
		}
		for k := 1; k <= bladesPerChassis; k++ {
			blade := fmt.Sprintf("%s-blade-%d", name, k)
			if _, err = s.tx.Exec(ctx, `INSERT INTO ci (id, organization_id, client_id, ci_type_id, name, status, manufacturer,
					model, serial_number, hostname, parent_ci_id, child_mode, attributes)
				VALUES ($1, $2, $3, $4, $5, 'active', 'Reticora Labs', 'BX-B1', $6, $5, $7, 'configuration',
					jsonb_build_object('blade_slot', $8::int, 'blade_role', 'compute'))
				ON CONFLICT (id) DO NOTHING`, s.bladeID(n, k), s.org.id, client, bladeType, blade,
				seedSerial(s.org.prefix, fmt.Sprintf("blade/%d/%d", n, k)), s.chassisID(n), k); err != nil {
				return fmt.Errorf("blade %s: %w", blade, err)
			}
		}
	}
	return nil
}

// seedAsset is an asset of the seed: its number, its kind (the start of
// its name), the CI it is linked to ("" for a stock asset), its model,
// serial number and location.
type seedAsset struct {
	n                 int
	kind, ciID, model string
	serial, location  string
}

// name is the name of the asset: its kind and inventory number. It never
// holds the name of its CI, so the global search for a CI name finds the
// CI alone (F-277).
func (a *seedAsset) name() string { return fmt.Sprintf("%s %s", a.kind, a.inventory()) }

// inventory is the inventory number of the asset, its key.
func (a *seedAsset) inventory() string { return fmt.Sprintf("INV-%05d", a.n) }

// assetPlan lists the assets of the organization: 1..linked with the
// mounted [B] CIs of the same number (client hq, the serial of the CI, the
// rack of its mount: R01 for 1..20, R02 for 21..40), then the blade
// chassis (server room hq), then the stock assets in turn in the bins.
func (s *stockSeed) assetPlan() []seedAsset {
	c := s.content
	out := make([]seedAsset, 0, c.linked+chassisCount+c.stock)
	for i := 1; i <= c.linked; i++ {
		kind, model := "Server", "R-1U"
		switch {
		case i <= 2:
			kind, model = "Switch", "RX-48"
		case i <= 4:
			kind, model = "PDU", "PX-16"
		}
		rack := "1"
		if i > 20 {
			rack = "2"
		}
		out = append(out, seedAsset{n: i, kind: kind, ciID: s.org.uid("ci", fmt.Sprint(i)),
			model: model, serial: seedSerial(s.org.prefix, fmt.Sprint(i)), location: s.org.uid("rack", rack)})
	}
	if c.chassis {
		for n := 1; n <= chassisCount; n++ {
			out = append(out, seedAsset{n: len(out) + 1, kind: "Blade chassis", ciID: s.chassisID(n), model: "BX-8",
				serial: seedSerial(s.org.prefix, "chassis/"+fmt.Sprint(n)), location: s.org.uid("room", "hq")})
		}
	}
	for k := 0; k < c.stock; k++ {
		n := len(out) + 1
		out = append(out, seedAsset{n: n, kind: "Spare server", model: "R-1U",
			serial: seedSerial(s.org.prefix, "spare/"+fmt.Sprint(n)), location: s.binID(k%c.bins + 1)})
	}
	return out
}

// warrantyDays is the warranty end of asset n in days after the anchor:
// the first four linked assets end within 30 days (warranty_expiring), two
// have ended, the others run for more than a year.
func warrantyDays(n int) int {
	switch {
	case n <= 4:
		return 7 * n
	case n <= 6:
		return -30 * n
	default:
		return 365 + 10*n
	}
}

// ensureAssets creates the assets with the AST-01 fields (WP-271), found
// again by inventory number, and links each linked asset to its CI the way
// of WP-272: the CI service writes ci.asset_id as a manual change (version,
// ci_change, audit, computed warranty_days_left, ci.changed), then the
// primary CI asset.ci_id and the audit entry asset.linked. The asset has
// the client, serial and location of its CI, so neither a serial_mismatch
// review nor a ci_location_client error arises.
func (s *stockSeed) ensureAssets(ctx context.Context) error {
	client := s.org.uid("client", "hq")
	plan := s.assetPlan()
	for i := range plan {
		a := &plan[i]
		inventory := a.inventory()
		id, err := s.lookup(ctx, `SELECT id::text FROM asset WHERE organization_id = $1 AND inventory_no = $2`, s.org.id, inventory)
		if err != nil {
			return err
		}
		if id == "" {
			item := &asset.Asset{OrganizationID: s.org.id, ClientID: client, AssetTag: inventory, Name: a.name(),
				Category: "hardware", Status: "in_stock", Manufacturer: "Reticora Labs", Model: a.model,
				PurchaseDate: s.day(-400 - a.n), PurchaseCost: fmt.Sprintf("%d.00", 1500+10*a.n),
				WarrantyEnd: s.day(warrantyDays(a.n)), CostCenter: fmt.Sprintf("CC-%03d", a.n%CostCenters+1),
				OwnerUserID: s.admin, SerialNumber: a.serial, LocationID: a.location, Notes: "Seed (WP-819)"}
			if s.engineer != "" && a.n%4 == 0 {
				// The system view my_devices_asset of engineer (WP-307).
				item.OwnerUserID = s.engineer
			}
			if len(s.supplierIDs) > 0 {
				item.SupplierID = s.supplierIDs[a.n%len(s.supplierIDs)]
				item.ContractID = s.contractIDs[a.n%len(s.contractIDs)]
			}
			if err = s.assets.Create(ctx, item); err != nil {
				return fmt.Errorf("asset %s: %w", inventory, err)
			}
			id = item.ID
		}
		s.assetIDs[a.n] = id
		if a.ciID != "" {
			if err = s.link(ctx, id, a.ciID); err != nil {
				return fmt.Errorf("link %s to CI %s: %w", inventory, a.ciID, err)
			}
		}
	}
	return nil
}

// link links the CI ciID to the asset assetID as primary CI unless it is
// linked already (asset.Handler.LinkCI with primary).
func (s *stockSeed) link(ctx context.Context, assetID, ciID string) error {
	current, err := s.lookup(ctx, `SELECT COALESCE(asset_id::text, '') FROM ci WHERE id = $1`, ciID)
	if err != nil {
		return err
	}
	if strings.EqualFold(current, assetID) {
		return nil
	}
	anyVersion := api.IfMatchAny
	if _, err = s.cis.Update(ctx, s.org.id, ciID, ci.UpdateRequest{AssetID: &assetID, IfMatch: &anyVersion,
		Manual: &ci.ManualChange{Author: s.admin}}); err != nil {
		return err
	}
	if _, err = s.assets.SetPrimaryCI(ctx, s.org.id, assetID, ciID); err != nil {
		return err
	}
	return s.assets.RecordLink(ctx, s.org.id, asset.ActionLinked, assetID, ciID, true)
}

// assetGrantFields are the asset fields the role viewer does not see
// (WP-264, E-43): the purchase price (core.purchase_cost, the grantable
// name of the column purchase_price) and the cost center.
var assetGrantFields = []string{"core.purchase_cost", "core.cost_center"}

// ensureAssetGrants creates the field grants of viewer on the asset
// fields, level none for every asset type.
func (s *stockSeed) ensureAssetGrants(ctx context.Context) error {
	if !s.content.assetGrants {
		return nil
	}
	role, err := s.lookup(ctx, `SELECT id::text FROM role WHERE organization_id = $1 AND name = 'viewer'`, s.org.id)
	if err != nil {
		return err
	}
	if role == "" {
		return errors.New("role viewer not found")
	}
	for _, field := range assetGrantFields {
		id, lookupErr := s.lookup(ctx, `SELECT id::text FROM field_grant WHERE organization_id = $1 AND role_id = $2
			AND object_kind = 'asset' AND ci_type_id IS NULL AND field_key = $3`, s.org.id, role, field)
		if lookupErr != nil {
			return lookupErr
		}
		if id != "" {
			continue
		}
		key := field
		if _, err = s.grants.CreateFieldGrant(ctx, s.org.id, &permission.FieldGrant{RoleID: &role, ObjectKind: permission.KindAsset,
			FieldKey: &key, Level: permission.LevelNone, CreatedBy: &s.admin}); err != nil {
			return fmt.Errorf("grant viewer %s: %w", field, err)
		}
	}
	return nil
}

// ensureScaledAssets writes the assets beyond the asset plan up to
// orgSeed.assets (WP-821) set-based in one statement: further stock assets
// like the stock assets of the plan, in turn in the bins, without a CI,
// with the AST-01 fields, owner, supplier and contract by the rules of
// ensureAssets and the deterministic id <prefix>/asset/<n>. The lifecycle
// state is the one the database gave the stock assets of the plan. Unlike
// Create they emit no asset.created event: the load seed needs the rows,
// not their consumers. An existing asset is skipped (ON CONFLICT).
func (s *stockSeed) ensureScaledAssets(ctx context.Context) error {
	plan := s.assetPlan()
	first := len(plan) + 1
	if s.org.assets < first || s.content.stock == 0 {
		return nil
	}
	template := s.assetIDs[plan[len(plan)-1].n]
	owner := []string{s.admin, s.admin, s.admin, s.admin}
	if s.engineer != "" {
		// The system view my_devices_asset of engineer (WP-307).
		owner[0] = s.engineer
	}
	_, err := s.tx.Exec(ctx, `
		INSERT INTO asset (id, organization_id, client_id, inventory_no, name, category, status, purchase_date,
			purchase_price, warranty_until, serial_number, notes, custom_fields, manufacturer, model, cost_center,
			supplier_id, contract_id, owner_user_id, location_id, lifecycle_state)
		SELECT md5($1 || '/asset/' || n)::uuid, $2, $3::uuid, 'INV-' || lpad(n::text, 5, '0'),
			'Spare server INV-' || lpad(n::text, 5, '0'), 'hardware', 'in_stock', $4::date - (400 + n),
			(1500 + 10 * n)::numeric, $4::date + (365 + 10 * n),
			'SN' || upper(substr(md5($1 || '/serial/spare/' || n), 1, 12)), 'Seed (WP-821)', '{}'::jsonb,
			'Reticora Labs', 'R-1U', 'CC-' || lpad((n % $5::int + 1)::text, 3, '0'),
			CASE WHEN cardinality($6::uuid[]) > 0 THEN ($6::uuid[])[n % cardinality($6::uuid[]) + 1] END,
			CASE WHEN cardinality($7::uuid[]) > 0 THEN ($7::uuid[])[n % cardinality($7::uuid[]) + 1] END,
			($8::uuid[])[n % 4 + 1], md5($1 || '/location/bin/' || ((n - 1) % $9::int + 1))::uuid, t.lifecycle_state
		FROM generate_series($10::int, $11::int) AS n
		CROSS JOIN (SELECT lifecycle_state FROM asset WHERE id = $12::uuid) t
		ON CONFLICT DO NOTHING`,
		s.org.prefix, s.org.id, s.org.uid("client", "hq"), s.day(0), CostCenters, s.supplierIDs, s.contractIDs, owner,
		s.content.bins, first, s.org.assets, template)
	return err
}

// The field values of the [B] CIs (WP-821).
const (
	// FieldValueSource is the source of the seeded discovered values.
	FieldValueSource = "snmp"
	// OverrideEvery: every OverrideEvery-th [B] CI has an override on
	// OverrideField.
	OverrideEvery = 20
	OverrideField = "hostname"
	// fieldValueBatch is the number of CIs of one RecordDiscoveredBatch.
	fieldValueBatch = 1000
)

// FieldValueFields are the discovered fields of every [B] CI.
var FieldValueFields = []string{"hostname", "serial_number", "management_ip"}

// ensureFieldValues writes the provenance of the [B] CIs of the
// organization (CI-10, OVR-01) the way the ingest and the API do, through
// the override repository, which joins this transaction
// (database.WithRequestTenant): every [B] CI without values of
// FieldValueSource gets its hostname, serial number and management address
// as discovered values of FieldValueSource observed at the anchor
// (RecordDiscoveredBatch, in batches of fieldValueBatch CIs), and every
// OverrideEvery-th CI without an override on OverrideField gets one with
// the value of its column (ensureOverrides). The values equal the columns,
// so the effective values stay the columns and no serial_mismatch review
// arises.
func (s *stockSeed) ensureFieldValues(ctx context.Context) error {
	if !s.content.fieldValues {
		return nil
	}
	rows, err := s.tx.Query(ctx, `
		SELECT c.id::text, COALESCE(c.hostname, ''), COALESCE(c.serial_number, ''), COALESCE(host(c.management_ip), '')
		FROM generate_series(1, $3::int) AS i
		JOIN ci c ON c.id = md5($2 || '/ci/' || i)::uuid AND c.organization_id = $1
		WHERE NOT EXISTS (SELECT 1 FROM ci_field_value v WHERE v.ci_id = c.id AND v.discovered_source = $4)
		ORDER BY i`, s.org.id, s.org.prefix, s.org.objects, FieldValueSource)
	if err != nil {
		return err
	}
	var batch []override.DiscoveredValues
	for rows.Next() {
		var id, hostname, serial, address string
		if err = rows.Scan(&id, &hostname, &serial, &address); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, override.DiscoveredValues{CIID: id, Source: FieldValueSource, ObservedAt: s.org.at,
			Values: map[string]any{FieldValueFields[0]: hostname, FieldValueFields[1]: serial, FieldValueFields[2]: address}})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for start := 0; start < len(batch); start += fieldValueBatch {
		if err = s.overrides.RecordDiscoveredBatch(ctx, s.org.id, batch[start:min(start+fieldValueBatch, len(batch))]); err != nil {
			return fmt.Errorf("discovered values: %w", err)
		}
	}
	return s.ensureOverrides(ctx)
}

// ensureOverrides sets the override on OverrideField of every
// OverrideEvery-th [B] CI that has none, with the value of its column,
// through SetOverride (protected, like an override of the API): the CI
// keeps its value, its version rises and its history and audit record the
// override like any manual write.
func (s *stockSeed) ensureOverrides(ctx context.Context) error {
	rows, err := s.tx.Query(ctx, `
		SELECT c.id::text, c.`+OverrideField+`
		FROM generate_series(1, $3::int / $4::int) AS k
		JOIN ci c ON c.id = md5($2 || '/ci/' || (k * $4::int))::uuid AND c.organization_id = $1
		WHERE c.`+OverrideField+` IS NOT NULL AND NOT EXISTS (SELECT 1 FROM ci_field_value v
			WHERE v.ci_id = c.id AND v.field_name = $5 AND v.override_value IS NOT NULL)
		ORDER BY k`, s.org.id, s.org.prefix, s.org.objects, OverrideEvery, OverrideField)
	if err != nil {
		return err
	}
	type pending struct{ id, value string }
	var todo []pending
	for rows.Next() {
		var p pending
		if err = rows.Scan(&p.id, &p.value); err != nil {
			rows.Close()
			return err
		}
		todo = append(todo, p)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, p := range todo {
		if _, err = s.overrides.SetOverride(ctx, s.org.id, p.id, OverrideField, p.value, s.admin,
			"Seed (WP-821): confirmed "+OverrideField, true); err != nil {
			return fmt.Errorf("override %s of CI %s: %w", OverrideField, p.id, err)
		}
	}
	return nil
}
