package server

import (
	"context"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/admp"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contract"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/events"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/importer"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/jobs"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/keymgmt"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/labels"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/location"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/metamodel"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/privacy"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/probe"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationshiptype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/reservation"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/savedview"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/security"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/training"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/workflow"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MemoryRepositories returns the in-memory implementations used by the
// explicit --no-db development mode and by tests. All state is lost on
// restart; it is never a fallback for an unreachable database.
func MemoryRepositories() Repositories {
	assets := asset.NewMemoryRepository()
	discoveryRepo := discovery.NewMemoryRepository()
	// Seed the system CI types so name-based ingest works in --no-db mode just
	// like against the migrated database (migration 000021 seeds them there).
	for _, typ := range []string{
		"switch", "router", "firewall", "access_point", "server", "hypervisor",
		"vm", "client", "pdu", "ups", "nas", "storage_array", "printer",
		"ip_phone", "camera", "generic_device", "patch_panel",
	} {
		discoveryRepo.SeedCIType(typ, typ)
	}
	lifecycleStates := lifecycle.NewMemoryStateStore()
	cis := ci.NewMemoryRepository()
	users := user.NewMemoryRepository()
	serviceAccounts := user.NewMemoryServiceAccounts()
	// Consumable movements book on the stock per bin of the same store
	// (WP-756).
	consumables := consumable.NewMemoryRepository()
	locationTree := locations.NewMemoryRepository()
	// The label lookup reads the codes of the memory repositories (WP-762,
	// API-01).
	labelLookup := labels.NewMemoryRepository(assetLabels{repo: assets}, consumableLabels{repo: consumables},
		locationLabels{repo: locationTree})
	// The ADMP cache takes the client of an account from the account store
	// (WP-928).
	admpAccounts := admp.NewMemoryAccountRepository()
	return Repositories{
		CI: cis,
		// Graph walks pass only through visible, not deleted CIs (IMP-07),
		// as in PostgreSQL.
		Relationship:      relationship.NewMemoryRepository().WithNodeVisibility(ci.VisibleIDs(cis)),
		Webhook:           webhook.NewMemoryRepository(),
		WebhookDeliveries: webhook.NewMemoryDeliveryStore(),
		Discovery:         discoveryRepo,
		CIType:            citype.NewMemoryRepository(),
		Import:            importer.NewMemoryRepository(),
		Supplier:          contract.NewMemoryRepository(),
		Contract:          contract.NewMemoryContracts(),
		RelationshipType:  relationshiptype.NewMemoryRepository(),
		Lifecycle:         lifecycle.NewMemoryRepository(),
		LocationTree:      locationTree,
		Labels:            labelLookup,
		Movement:          movement.NewMemoryRepository().WithConsumables(consumables),
		Reservation:       reservation.NewMemoryRepository(),
		// The provenance names override authors as PostgreSQL does from
		// app_user and service_account (UI-15, WP-850).
		Override: override.NewMemoryRepository().WithAuthorNames(func(ctx context.Context, orgID, id string) (string, bool) {
			if u, err := users.GetUser(ctx, orgID, id); err == nil && u.DisplayName != "" {
				return u.DisplayName, true
			}
			if sa, err := serviceAccounts.GetServiceAccount(ctx, orgID, id); err == nil && sa.Name != "" {
				return sa.Name, true
			}
			return "", false
		}),
		History:           history.NewMemoryRepository(),
		SavedView:         savedview.NewMemoryRepository(),
		FilterQuery:       nil, // memory mode: query engine requires SQL; endpoint reports 501
		LifecycleStates:   lifecycleStates,
		LifecycleResolver: lifecycleStates,
		Asset:             assets,
		Assignment:        assignment.NewMemoryRepository(),
		Document:          document.NewMemoryRepository(),
		Stocktake:         stocktake.NewMemoryRepository(assets),
		Consumable:        consumables,
		Order:             order.NewMemoryRepository(),
		Maintenance:       maintenance.NewMemoryRepository(),
		Disposal:          disposal.NewMemoryRepository(),
		Key:               keymgmt.NewMemoryRepository(),
		Training:          training.NewMemoryRepository(),
		Desk:              desk.NewMemoryRepository(),
		Location:          location.NewMemoryRepository(),
		Agent:             agent.NewMemoryRepository(),
		Security:          security.NewMemoryRepository(),
		Ticket:            ticket.NewMemoryRepository(),
		User:              users,
		Credential:        credential.NewMemoryRepository(),
		ADMPAccounts:      admpAccounts,
		ADMP:              admp.NewMemoryRepository(admpAccounts),
		Entitlement:       entitlement.NewMemoryRepository(),
		TenantHierarchy:   tenantapi.NewMemoryRepository(),
		Rack:              rack.NewMemoryRepository(),
		Contact:           contact.NewMemoryRepository(),
		IPAM:              ipam.NewMemoryRepository(),
		Metrics:           monitoring.NewMemoryMetricStore(),
		// The memory probes read client and site of a CI from the CI store
		// (WP-939).
		Probes: probe.NewMemoryRepository().WithCIs(func(ctx context.Context, orgID, id string) (string, string, bool) {
			item, err := cis.GetByID(ctx, orgID, id)
			if err != nil {
				return "", "", false
			}
			return item.ClientID, item.SiteID, true
		}),
		Permission:      permission.NewMemoryRepository(),
		FieldGrants:     permission.NewMemoryFieldGrants(),
		APIKeys:         identity.NewMemoryAPIKeyStore(),
		ServiceAccounts: serviceAccounts,
		SLA:             sla.NewMemoryRepository(),
		Form:            form.NewMemoryRepository(),
		Workflow:        workflow.NewMemoryRepository(),
		Compliance:      compliance.NewMemoryRepository(),
		IGA:             iga.NewMemoryRepository(),
		Search:          search.NewMemoryRepository(),
		AI:              ai.NewMemoryRepository(),
		ExportJobs:      export.NewMemoryJobRepository(),
		Jobs:            jobs.NewMemoryRepository(),
		Privacy:         privacy.NewMemoryRepository(),
	}
}

// PostgresRepositories returns the PostgreSQL-backed implementations. The
// audit recorder is passed to the CI repository so history is written inside
// the same transaction as the mutation; CI and relationship mutations write
// their domain events into the outbox of their transaction (EVT-02).
func PostgresRepositories(pool *pgxpool.Pool, recorder audit.TxRecorder) Repositories {
	// The CI repository computes the computed attributes in its write
	// transactions from the definitions of the CI type repository, and an
	// override recomputes them in its own (WP-717).
	types := citype.NewPGRepository(pool)
	cis := ci.NewPGRepositoryWithAudit(pool, recorder).WithEvents(events.NewOutbox()).WithComputed(types)
	// The management address of a CI follows its management IP in the
	// CI's transaction (WP-913); router.go attaches the review queue of
	// the duplicate check.
	ipamRepo := ipam.NewPGRepository(pool).WithEvents(events.NewOutbox())
	management := ipam.NewManagementSync(ipamRepo, nil)
	cis.WithManagementSync(management)
	movements := movement.NewService(movement.NewPGRepository(pool))
	assignments := assignment.NewService(pool, movements, events.NewOutbox())
	return Repositories{
		CI:                cis,
		EventOutbox:       true,
		Relationship:      relationship.NewPGRepository(pool).WithEvents(events.NewOutbox()),
		Webhook:           webhook.NewPGRepository(pool),
		WebhookDeliveries: webhook.NewPGDeliveryStore(pool),
		Discovery:         discovery.NewPGRepository(pool),
		Asset:             asset.NewPGRepository(pool).WithEvents(events.NewOutbox()),
		Assignment:        assignment.NewPGRepository(pool),
		// The assignment service books over the movement service (WP-275).
		AssignmentService: assignments,
		Document:          document.NewPGRepository(pool),
		Stocktake:         stocktake.NewPGRepository(pool),
		Consumable:        consumable.NewPGRepository(pool),
		Order:             order.NewPGRepository(pool),
		Maintenance:       maintenance.NewPGRepository(pool),
		Disposal:          disposal.NewPGRepository(pool),
		Key:               keymgmt.NewPGRepository(pool),
		Training:          training.NewPGRepository(pool),
		Desk:              desk.NewPGRepository(pool),
		Location:          location.NewPGRepository(pool),
		Agent:             agent.NewPGRepository(pool),
		Security:          security.NewPGRepository(pool),
		Ticket:            ticket.NewPGRepository(pool).WithEvents(events.NewOutbox()),
		User:              user.NewPGRepository(pool),
		Credential:        credential.NewPGRepository(pool),
		ADMPAccounts:      admp.NewPGAccountRepository(pool),
		ADMP:              admp.NewPGRepository(pool),
		Entitlement:       entitlement.NewPGRepository(pool),
		TenantHierarchy:   tenantapi.NewPGRepository(pool),
		Rack:              rack.NewPGRepository(pool),
		Contact:           contact.NewPGRepository(pool),
		IPAM:              ipamRepo,
		IPAMManagement:    management,
		Metrics:           monitoring.NewPGMetricStore(pool),
		Probes:            probe.NewPGRepository(pool),
		Permission:        permission.NewPGRepository(pool),
		FieldGrants:       permission.NewPGFieldGrants(pool),
		APIKeys:           identity.NewPGAPIKeyStore(pool),
		ServiceAccounts:   user.NewPGServiceAccounts(pool),
		Invitations:       user.NewPGInvitations(pool),
		SLA:               sla.NewPGRepository(pool),
		Form:              form.NewPGRepository(pool),
		Workflow:          workflow.NewPGRepository(pool),
		Compliance:        compliance.NewPGRepository(pool),
		IGA:               iga.NewPGRepository(pool),
		Search:            search.NewPGRepository(pool),
		AI:                ai.NewPGRepository(pool),
		ExportJobs:        export.NewPGJobRepository(pool),
		Jobs: jobs.NewPGRepository(pool).WithReadCondition(importer.JobKind, importer.JobReadSQL()).
			WithReadCondition(labels.BatchJobKind, labels.JobReadSQL),
		Privacy:          privacy.NewPGRepository(pool),
		CIType:           types,
		MetamodelDraft:   metamodel.NewPGRepository(pool),
		Import:           importer.NewPGRepository(pool),
		Supplier:         contract.NewPGRepository(pool),
		Contract:         contract.NewPGContracts(pool),
		RelationshipType: relationshiptype.NewPGRepository(pool),
		Lifecycle:        lifecycle.NewPGRepository(pool),
		LocationTree:     locations.NewPGRepository(pool),
		Labels:           labels.NewPGRepository(pool),
		Movement:         movement.NewPGRepository(pool),
		// Reservations lock, book, convert, audit their expiry and write
		// their events in one transaction (WP-277).
		Reservation: reservation.NewPGRepository(pool).WithAudit(recorder).WithEvents(events.NewOutbox()).
			WithMovements(movements).WithAssigner(assignments),
		Override:          override.NewPGRepository(pool).WithRecompute(cis),
		History:           history.NewPGRepository(pool),
		SavedView:         savedview.NewPGRepository(pool),
		FilterQuery:       savedview.NewPGQueryEngine(pool),
		LifecycleStates:   lifecycle.NewPGStateStore(pool),
		LifecycleResolver: lifecycle.NewResolver(pool),
		LifecycleGuards:   lifecycle.NewPGGuards(pool),
		Availability:      reservation.NewPGAvailability(pool),
	}
}

// memoryLabelPage is the page the memory label sources list with: every
// object of the organization.
var memoryLabelPage = api.PaginationParams{Limit: 1 << 30}

// assetLabels lists the assets with their barcode and inventory number
// for the memory label lookup (WP-762).
type assetLabels struct{ repo asset.Repository }

func (a assetLabels) Entries(ctx context.Context, orgID string) ([]labels.Entry, error) {
	items, _, err := a.repo.List(ctx, orgID, &asset.FilterParams{}, memoryLabelPage)
	if err != nil {
		return nil, err
	}
	out := make([]labels.Entry, 0, len(items))
	for i := range items {
		it := &items[i]
		out = append(out, labels.Entry{Object: labels.Object{ID: it.ID, Name: it.Name, LocationID: it.LocationID},
			Codes: []string{it.AssetTag, it.Barcode}})
	}
	return out, nil
}

// consumableLabels lists the consumables with their barcode for the memory
// label lookup (WP-762).
type consumableLabels struct{ repo consumable.Repository }

func (c consumableLabels) Entries(ctx context.Context, orgID string) ([]labels.Entry, error) {
	items, _, err := c.repo.List(ctx, orgID, consumable.FilterParams{}, memoryLabelPage)
	if err != nil {
		return nil, err
	}
	out := make([]labels.Entry, 0, len(items))
	for i := range items {
		it := &items[i]
		out = append(out, labels.Entry{Object: labels.Object{ID: it.ID, Name: it.Name}, Codes: []string{it.Barcode}})
	}
	return out, nil
}

// locationLabels lists the nodes of the location tree with their barcode
// for the memory label lookup (WP-762).
type locationLabels struct{ repo locations.Repository }

func (l locationLabels) Entries(ctx context.Context, orgID string) ([]labels.Entry, error) {
	items, err := l.repo.List(ctx, orgID, locations.Filter{})
	if err != nil {
		return nil, err
	}
	out := make([]labels.Entry, 0, len(items))
	for i := range items {
		it := &items[i]
		out = append(out, labels.Entry{Object: labels.Object{ID: it.ID, Name: it.Name}, Codes: []string{it.Barcode}})
	}
	return out, nil
}
