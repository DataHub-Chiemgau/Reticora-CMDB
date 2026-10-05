package server

import (
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/agent"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/composition"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/consumable"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/desk"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/disposal"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/history"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/keymgmt"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/lifecycle"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/location"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/maintenance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/movement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/order"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/override"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/privacy"
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
	return Repositories{
		CI:                ci.NewMemoryRepository(),
		Relationship:      relationship.NewMemoryRepository(),
		Webhook:           webhook.NewMemoryRepository(),
		WebhookDeliveries: webhook.NewMemoryDeliveryStore(),
		Discovery:         discoveryRepo,
		CIType:            citype.NewMemoryRepository(),
		RelationshipType:  relationshiptype.NewMemoryRepository(),
		Lifecycle:         lifecycle.NewMemoryRepository(),
		LocationTree:      locations.NewMemoryRepository(),
		Movement:          movement.NewMemoryRepository(),
		Reservation:       reservation.NewMemoryRepository(),
		Composition:       composition.NewMemoryRepository(),
		Override:          override.NewMemoryRepository(),
		History:           history.NewMemoryRepository(),
		SavedView:         savedview.NewMemoryRepository(),
		FilterQuery:       nil, // memory mode: query engine requires SQL; endpoint reports 501
		LifecycleStates:   lifecycleStates,
		LifecycleResolver: lifecycleStates,
		Asset:             assets,
		Assignment:        assignment.NewMemoryRepository(),
		Document:          document.NewMemoryRepository(),
		Stocktake:         stocktake.NewMemoryRepository(assets),
		Consumable:        consumable.NewMemoryRepository(),
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
		User:              user.NewMemoryRepository(),
		Credential:        credential.NewMemoryRepository(),
		Entitlement:       entitlement.NewMemoryRepository(),
		TenantHierarchy:   tenantapi.NewMemoryRepository(),
		Rack:              rack.NewMemoryRepository(),
		Contact:           contact.NewMemoryRepository(),
		IPAM:              ipam.NewMemoryRepository(),
		Metrics:           monitoring.NewMemoryMetricStore(),
		Permission:        permission.NewMemoryRepository(),
		SLA:               sla.NewMemoryRepository(),
		Form:              form.NewMemoryRepository(),
		Workflow:          workflow.NewMemoryRepository(),
		Compliance:        compliance.NewMemoryRepository(),
		IGA:               iga.NewMemoryRepository(),
		Search:            search.NewMemoryRepository(),
		AI:                ai.NewMemoryRepository(),
		ExportJobs:        export.NewMemoryJobRepository(),
		Privacy:           privacy.NewMemoryRepository(),
	}
}

// PostgresRepositories returns the PostgreSQL-backed implementations. The
// audit recorder is passed to the CI repository so history is written inside
// the same transaction as the mutation.
func PostgresRepositories(pool *pgxpool.Pool, recorder audit.TxRecorder) Repositories {
	return Repositories{
		CI:                ci.NewPGRepositoryWithAudit(pool, recorder),
		Relationship:      relationship.NewPGRepository(pool),
		Webhook:           webhook.NewPGRepository(pool),
		WebhookDeliveries: webhook.NewPGDeliveryStore(pool),
		Discovery:         discovery.NewPGRepository(pool),
		Asset:             asset.NewPGRepository(pool),
		Assignment:        assignment.NewPGRepository(pool),
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
		Ticket:            ticket.NewPGRepository(pool),
		User:              user.NewPGRepository(pool),
		Credential:        credential.NewPGRepository(pool),
		Entitlement:       entitlement.NewPGRepository(pool),
		TenantHierarchy:   tenantapi.NewPGRepository(pool),
		Rack:              rack.NewPGRepository(pool),
		Contact:           contact.NewPGRepository(pool),
		IPAM:              ipam.NewPGRepository(pool),
		Metrics:           monitoring.NewPGMetricStore(pool),
		Permission:        permission.NewPGRepository(pool),
		SLA:               sla.NewPGRepository(pool),
		Form:              form.NewPGRepository(pool),
		Workflow:          workflow.NewPGRepository(pool),
		Compliance:        compliance.NewPGRepository(pool),
		IGA:               iga.NewPGRepository(pool),
		Search:            search.NewPGRepository(pool),
		AI:                ai.NewPGRepository(pool),
		ExportJobs:        export.NewPGJobRepository(pool),
		Privacy:           privacy.NewPGRepository(pool),
		CIType:            citype.NewPGRepository(pool),
		RelationshipType:  relationshiptype.NewPGRepository(pool),
		Lifecycle:         lifecycle.NewPGRepository(pool),
		LocationTree:      locations.NewPGRepository(pool),
		Movement:          movement.NewPGRepository(pool),
		Reservation:       reservation.NewPGRepository(pool),
		Composition:       composition.NewPGRepository(pool),
		Override:          override.NewPGRepository(pool),
		History:           history.NewPGRepository(pool),
		SavedView:         savedview.NewPGRepository(pool),
		FilterQuery:       savedview.NewPGQueryEngine(pool),
		LifecycleStates:   lifecycle.NewPGStateStore(pool),
		LifecycleResolver: lifecycle.NewResolver(pool),
		Availability:      reservation.NewPGAvailability(pool),
	}
}
