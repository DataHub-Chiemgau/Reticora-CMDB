package rls

// Channel is one way tenant data leaves or enters the system (TEN-09). The
// channel matrix (channel_matrix_integration_test.go, WP-208) checks every
// channel for every scope kind with reads and writes.
type Channel struct {
	Name string
	// Read is the request that reads the channel's data; {ci} is replaced by
	// the root CI of the scenario.
	Read string
	// Write names the write the matrix checks, empty for read-only channels.
	Write string
}

// Channels lists the channels of TEN-09 in the order the matrix runs them.
var Channels = []Channel{
	{Name: "rest", Read: "GET /api/v1/cis?limit=200", Write: "PATCH/DELETE /api/v1/cis/{id}, POST /api/v1/cis"},
	{Name: "rest-detail", Read: "GET /api/v1/cis/{id}"},
	{Name: "graphql", Read: "POST /bff/graphql { cis { nodes { id } } }"},
	{Name: "search", Read: "GET /api/v1/search?q=…"},
	{Name: "export", Read: "GET /api/v1/export/cis?format=json"},
	{Name: "topology", Read: "GET /api/v1/topology?root_ci_id={ci}&depth=3"},
	{Name: "impact", Read: "GET /api/v1/impact/{ci}"},
	{Name: "location", Read: "GET /api/v1/sites", Write: "POST /api/v1/sites"},
	{Name: "review", Read: "GET /api/v1/review-items"},
	{Name: "webhook", Read: "GET /api/v1/webhooks"},
	{Name: "notification", Read: "GET /api/v1/notifications"},
	{Name: "job", Read: "GET /api/v1/jobs"},
	{Name: "relationship", Write: "POST /api/v1/relationships"},
	{Name: "ingest", Write: "POST /api/v1/ingest/bulk"},
}

// ScopeKinds are the principal scopes of the matrix (TEN-04): the whole
// organization, one client, one site, one team, and a combination.
var ScopeKinds = []string{"org", "client", "site", "team", "multi", "other-org"}
