package server

import (
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ai"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/compliance"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/export"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/form"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/iga"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/permission"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/sla"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
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
	return Repositories{
		CI:                ci.NewMemoryRepository(),
		Relationship:      relationship.NewMemoryRepository(),
		Webhook:           webhook.NewMemoryRepository(),
		WebhookDeliveries: webhook.NewMemoryDeliveryStore(),
		Discovery:         discovery.NewMemoryRepository(),
		Asset:             assets,
		Assignment:        assignment.NewMemoryRepository(),
		Document:          document.NewMemoryRepository(),
		Stocktake:         stocktake.NewMemoryRepository(assets),
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
	}
}
