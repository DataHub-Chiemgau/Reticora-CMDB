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
	{Name: "webhook", Read: "GET /api/v1/webhooks, GET /api/v1/webhooks/{id}",
		Write: "POST /api/v1/webhooks, DELETE /api/v1/webhooks/{id}"},
	{Name: "notification", Read: "GET /api/v1/notifications",
		Write: "POST /api/v1/notifications/{id}/read, POST /api/v1/notifications/read-all"},
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
}

// ScopeKinds are the principal scopes of the matrix (TEN-04): the whole
// organization, one client, one site, one team, several clients with one
// site, and the whole other organization.
var ScopeKinds = []string{"org", "client", "site", "team", "multi", "other-org"}
