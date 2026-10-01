// Package rls holds the RLS catalog test (catalog_integration_test.go) and its
// managed list of known gaps (known_gaps.go).
//
// Tenant-scoped database work goes through database.WithTenant, the single
// entry point that sets app.org_id, app.user_id, app.client_scope,
// app.site_scope and app.team_scope transaction-locally (TEN-04, TEN-06).
package rls
