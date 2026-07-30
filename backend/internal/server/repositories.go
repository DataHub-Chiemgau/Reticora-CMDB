package server

import (
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/asset"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/assignment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contact"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ipam"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/monitoring"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/rack"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/relationship"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/stocktake"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ticket"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/user"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MemoryRepositories returns the in-memory implementations used by the
// explicit --no-db development mode and by tests. All state is lost on
// restart; it is never a fallback for an unreachable database.
func MemoryRepositories() Repositories {
	return Repositories{
		CI:                ci.NewMemoryRepository(),
		Relationship:      relationship.NewMemoryRepository(),
		Webhook:           webhook.NewMemoryRepository(),
		WebhookDeliveries: webhook.NewMemoryDeliveryStore(),
		Discovery:         discovery.NewMemoryRepository(),
		Asset:             asset.NewMemoryRepository(),
		Assignment:        assignment.NewMemoryRepository(),
		Document:          document.NewMemoryRepository(),
		Stocktake:         stocktake.NewMemoryRepository(),
		Ticket:            ticket.NewMemoryRepository(),
		User:              user.NewMemoryRepository(),
		Credential:        credential.NewMemoryRepository(),
		Entitlement:       entitlement.NewMemoryRepository(),
		TenantHierarchy:   tenantapi.NewMemoryRepository(),
		Rack:              rack.NewMemoryRepository(),
		Contact:           contact.NewMemoryRepository(),
		IPAM:              ipam.NewMemoryRepository(),
		Metrics:           monitoring.NewMemoryMetricStore(),
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
		Metrics:           monitoring.NewMemoryMetricStore(),
	}
}
