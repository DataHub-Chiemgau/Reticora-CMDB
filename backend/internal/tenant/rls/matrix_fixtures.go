package rls

// Channel is one way tenant data leaves or enters the system (TEN-09). The
// channel matrix (channel_matrix_integration_test.go, WP-208, WP-409) runs
// every channel for every scope kind, reading and, where the channel writes,
// writing, and checks the exact status of every request; a channel of this
// table without a matrix row, or a row without a channel, fails the matrix.
type Channel struct {
	Name string
	// Read lists the requests that read the channel's data; {id} is an object
	// of the scenario: visible, out of scope, of the other organization and
	// unknown.
	Read string
	// Write lists the writes the matrix checks, empty for read-only channels.
	Write string
	// GraphQL is the query field of the BFF the channel reads, empty for REST
	// channels. Every query field of the schema has exactly one channel.
	GraphQL string
}

// Channels lists the channels of TEN-09 in the order the matrix runs them.
var Channels = []Channel{
	{Name: "ci", Read: "GET /api/v1/cis?limit=200, GET /api/v1/cis/{id}",
		Write: "POST /api/v1/cis, PATCH /api/v1/cis/{id}, DELETE /api/v1/cis/{id}"},
	{Name: "search", Read: "GET /api/v1/search?q=…"},
	{Name: "export", Read: "GET /api/v1/export/cis?format=json"},
	{Name: "topology", Read: "GET /api/v1/topology, GET /api/v1/topology?root_ci_id={id}&depth=3, GET /api/v1/topology/cis/{id}/neighbors"},
	{Name: "impact", Read: "GET /api/v1/impact/{id}, GET /api/v1/impact?ci_ids={id}, GET /api/v1/topology/cis/{id}/impact"},
	{Name: "metrics", Read: "GET /api/v1/cis/{id}/metrics"},
	{Name: "relationship", Read: "GET /api/v1/relationships, GET /api/v1/cis/{id}/relationships",
		Write: "POST /api/v1/relationships, DELETE /api/v1/relationships/{id}"},
	{Name: "site", Read: "GET /api/v1/sites, GET /api/v1/sites/{id}",
		Write: "POST /api/v1/sites, PATCH /api/v1/sites/{id}, DELETE /api/v1/sites/{id}"},
	{Name: "building", Read: "GET /api/v1/buildings, GET /api/v1/buildings/{id}",
		Write: "POST /api/v1/buildings, PATCH /api/v1/buildings/{id}, DELETE /api/v1/buildings/{id}"},
	{Name: "room", Read: "GET /api/v1/rooms, GET /api/v1/rooms/{id}",
		Write: "POST /api/v1/rooms, PATCH /api/v1/rooms/{id}, DELETE /api/v1/rooms/{id}"},
	{Name: "rack", Read: "GET /api/v1/racks, GET /api/v1/racks/{id}, GET /api/v1/racks/{id}/layout",
		Write: "POST /api/v1/racks, PATCH /api/v1/racks/{id}, DELETE /api/v1/racks/{id}"},
	{Name: "mount", Read: "GET /api/v1/racks/{id}/mounts, GET /api/v1/mounts/{id}",
		Write: "POST /api/v1/racks/{id}/mounts, DELETE /api/v1/mounts/{id}"},
	{Name: "review", Read: "GET /api/v1/review-items, GET /api/v1/review-items/{id} (items with candidate CIs)",
		Write: "POST /api/v1/review-items/{id}/resolve"},
	{Name: "review-without-candidates", Read: "GET /api/v1/review-items, GET /api/v1/review-items/{id} (items without candidates)",
		Write: "POST /api/v1/review-items/{id}/resolve"},
	{Name: "webhook", Read: "GET /api/v1/webhooks, GET /api/v1/webhooks/{id}, GET /api/v1/webhooks/{id}/deliveries, GET /api/v1/webhooks/dead-letters",
		Write: "POST /api/v1/webhooks (also with service_account_id), POST /api/v1/webhooks/{id}/test, POST /api/v1/webhooks/{id}/rotate-secret, " +
			"POST /api/v1/webhooks/{id}/deliveries/{delivery_id}/retry, DELETE /api/v1/webhooks/{id}"},
	{Name: "notification", Read: "GET /api/v1/notifications",
		Write: "POST /api/v1/notifications/{id}/read, POST /api/v1/notifications/read-all"},
	{Name: "notification-channels", Read: "GET /api/v1/notification-channels, GET /api/v1/notification-channels/{id}, " +
		"GET /api/v1/notification-deliveries (org-wide principals only, F-418)",
		Write: "POST /api/v1/notification-channels, PATCH /api/v1/notification-channels/{id}, DELETE /api/v1/notification-channels/{id}"},
	{Name: "job", Read: "GET /api/v1/jobs, GET /api/v1/jobs/{id}, GET /api/v1/jobs/{id}/result",
		Write: "DELETE /api/v1/jobs/{id}, POST /api/v1/jobs/{id}/revert"},
	{Name: "export-job", Read: "GET /api/v1/jobs?kind=export, GET /api/v1/jobs/{id}, GET /api/v1/export/jobs, GET /api/v1/export/jobs/{id} (export job mirrored into the job model)",
		Write: "DELETE /api/v1/jobs/{id}, POST /api/v1/jobs/{id}/revert"},
	{Name: "ingest", Write: "POST /api/v1/ingest/bulk"},
	{Name: "reference", Write: "POST /api/v1/contacts, /subnets, /cis/{id}/contacts, /discovery-scopes, /role-assignments with references of the other organization"},
	{Name: "graphql-cis", Read: "POST /bff/graphql { cis }", GraphQL: "cis"},
	{Name: "graphql-ci", Read: "POST /bff/graphql { ci(id) }", GraphQL: "ci"},
	{Name: "graphql-relationships", Read: "POST /bff/graphql { relationships(ciId) }", GraphQL: "relationships"},
	{Name: "graphql-current-user", Read: "POST /bff/graphql { currentUser }", GraphQL: "currentUser"},
	{Name: "graphql-topology", Read: "POST /bff/graphql { topology, topology(rootCiId) }", GraphQL: "topology"},
	{Name: "graphql-rack-layout", Read: "POST /bff/graphql { rackLayout(rackId) }", GraphQL: "rackLayout"},
	{Name: "graphql-search", Read: "POST /bff/graphql { searchEverything(q) }", GraphQL: "searchEverything"},
	{Name: "graphql-review-inbox", Read: "POST /bff/graphql { reviewInbox }", GraphQL: "reviewInbox"},
	{Name: "graphql-jobs", Read: "POST /bff/graphql { jobs }", GraphQL: "jobs"},
	// The asset aggregate (WP-311, module inventory).
	{Name: "graphql-asset", Read: "POST /bff/graphql { asset(id) }", GraphQL: "asset"},
	// The CI aggregates (WP-792): form, history with relationship changes,
	// metrics.
	{Name: "graphql-ci-form", Read: "POST /bff/graphql { ciForm(id) }", GraphQL: "ciForm"},
	{Name: "graphql-ci-history", Read: "POST /bff/graphql { ciHistory(id) }", GraphQL: "ciHistory"},
	{Name: "graphql-ci-metrics", Read: "POST /bff/graphql { ciMetrics(id) }", GraphQL: "ciMetrics"},

	// The channels of phase 2 (WP-828); their rows and scenario are in
	// channel_matrix_p2_integration_test.go. The P2 objects of the channels
	// review (serial_mismatch), notification (contract expiry, minimum
	// stock) and job (import, bulk) are read there as well.
	{Name: "asset", Read: "GET /api/v1/assets?limit=200, GET /api/v1/assets/{id}, GET /api/v1/assets/{id}/movements",
		Write: "POST /api/v1/assets, PATCH /api/v1/assets/{id}"},
	{Name: "reservation", Read: "GET /api/v1/reservations?limit=200, GET /api/v1/reservations/{id}",
		Write: "POST /api/v1/reservations, POST /api/v1/assets/{id}/reserve"},
	{Name: "assignment", Read: "GET /api/v1/assignments?limit=200, GET /api/v1/assignments/{id}",
		Write: "POST /api/v1/assets/{id}/assign"},
	{Name: "supplier", Read: "GET /api/v1/suppliers?limit=200, GET /api/v1/suppliers/{id}"},
	{Name: "contract", Read: "GET /api/v1/contracts?limit=200, GET /api/v1/contracts/{id}"},
	{Name: "document", Read: "GET /api/v1/documents?limit=200, GET /api/v1/documents/{id}, GET /api/v1/documents/{id}/content",
		Write: "POST /api/v1/documents/{id}/links"},
	{Name: "consumable", Read: "GET /api/v1/consumables?limit=200, GET /api/v1/consumables/{id}, GET /api/v1/consumables/{id}/stock"},
	{Name: "stock-movement", Read: "GET /api/v1/stock-movements?limit=200",
		Write: "POST /api/v1/stock-movements (receipt of an article, transfer of an asset)"},
	{Name: "order", Read: "GET /api/v1/orders?limit=200, GET /api/v1/orders/{id}", Write: "POST /api/v1/orders"},
	{Name: "stocktake", Read: "GET /api/v1/stocktakes?limit=200, GET /api/v1/stocktakes/{id}"},
	{Name: "ticket", Read: "GET /api/v1/tickets?limit=200, GET /api/v1/tickets/{id}", Write: "POST /api/v1/tickets"},
	{Name: "import", Read: "GET /api/v1/imports/{id}, GET /api/v1/imports/{id}/report, GET /api/v1/imports/{id}/errors", Write: "POST /api/v1/imports (rows of every client, report)"},
	{Name: "bulk", Write: "POST /api/v1/cis/bulk, POST /api/v1/assets/bulk (partial failure report), POST /api/v1/jobs/{id}/revert"},
	{Name: "label-lookup", Read: "GET /api/v1/labels/lookup?code=… (inventory number, barcode, URN)"},
	{Name: "search-p2", Read: "GET /api/v1/search?q=<inventory number>"},
	{Name: "datev-export", Read: "POST /api/v1/exports {format: datev} (file of the own export), GET /api/v1/exports/{id}"},
}

// ScopeKinds are the principal scopes of the matrix (TEN-04): the whole
// organization, one client, one site, one team, several clients with one
// site, and the whole other organization.
var ScopeKinds = []string{"org", "client", "site", "team", "multi", "other-org"}

// ClientMatrixTables lists tables the RLS matrix (TestRLSMatrix, WP-065)
// must run with their client and site dimension, with the reason. The matrix
// finds the tenant tables in the catalog; a listed table it does not run
// (no RLS, no privilege of the application role, inherited scope) or runs
// without client_id or site_id fails it, so a derived table cannot drop out
// of the client matrix unnoticed.
var ClientMatrixTables = map[string]string{
	"ci_identity_mac": "identity MACs of ci with the client and site of their CI, kept by the trigger on ci " +
		"(migration 000161, WP-434); the identity search of discovery reads it under the scope of the principal",
}
