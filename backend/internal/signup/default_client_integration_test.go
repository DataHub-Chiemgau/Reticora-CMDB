package signup

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/locations"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
)

// TestDefaultClientForEveryOrganization covers WP-854 (UI-15, TEN-01)
// against PostgreSQL with the application role: an organization from the
// signup (Verify → Complete) and one from the operator creation
// (CreateOrganization) each hold exactly one client, slug default, named
// like the organization (trigger trg_organization_default_client of
// migration 000282), and a site for it can be created at once.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestDefaultClientForEveryOrganization(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	appPool, err := database.NewPool(ctx, scopetest.AppDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(appPool.Close)
	adminPool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	// Closed after the cleanups below, which delete through it.
	t.Cleanup(adminPool.Close)

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	tag := hex.EncodeToString(suffix)
	signupName, operatorName := "Default Client Signup "+tag, "Default Client Operator "+tag
	var created []string
	t.Cleanup(func() {
		for _, id := range created {
			_, _ = adminPool.Exec(context.Background(), `DELETE FROM organization WHERE id = $1`, id)
		}
		_, _ = adminPool.Exec(context.Background(), `DELETE FROM signup_request WHERE organization_name = $1`, signupName)
	})

	// Signup: Submit and Verify run the creation chain populate.
	mailer, accounts := &stubMailer{}, &stubAccounts{}
	cfg := DefaultConfig()
	cfg.VerifyURL = "https://app.example/signup/verify"
	svc := NewService(NewPGRepository(appPool), mailer, accounts, cfg)
	if err = svc.Submit(ctx, Request{OrganizationName: signupName, Email: "ada@default-client.example", DisplayName: "Ada"}); err != nil {
		t.Fatalf("Submit: %v", err)
	}
	res, err := svc.Verify(ctx, Verification{Token: tokenOf(t, mailer.link), Password: "correct horse battery"})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	created = append(created, res.OrganizationID)

	// Operator: the same chain without signup request.
	opSvc := NewService(NewPGRepository(appPool), nil, &stubInviter{}, DefaultConfig())
	org, err := opSvc.CreateOrganization(ctx, operator.Identity{ID: "op-wp854", Kind: "oidc"},
		&operator.NewOrganization{Name: operatorName, Slug: "default-client-" + tag, Plan: "pro", AdminEmail: "admin@default-client.example"})
	if err != nil {
		t.Fatalf("CreateOrganization: %v", err)
	}
	created = append(created, org.ID)

	for _, c := range []struct{ kind, orgID, name string }{
		{"signup", res.OrganizationID, signupName},
		{"operator", org.ID, operatorName},
	} {
		clients := clientsOf(ctx, t, adminPool, c.orgID)
		if len(clients) != 1 || clients[0] != "default/"+c.name {
			t.Errorf("%s: clients = %v, want exactly default/%s", c.kind, clients, c.name)
			continue
		}
		// The application role sees the client in the organization's scope
		// and creates a site for it without any preparation.
		var clientID string
		scope := database.OrgWideScope(c.orgID, "")
		if err = database.WithTenant(ctx, appPool, &scope, func(ctx context.Context, tx pgx.Tx) error {
			return tx.QueryRow(ctx, `SELECT id::text FROM client WHERE slug = 'default'`).Scan(&clientID)
		}); err != nil {
			t.Fatalf("%s: read the default client as the application role: %v", c.kind, err)
		}
		site, serr := locations.NewPGRepository(appPool).Create(database.ContextWithTenantScope(ctx, &scope), c.orgID,
			locations.CreateRequest{Kind: locations.KindSite, ClientID: clientID, Name: "Hauptsitz"})
		if serr != nil {
			t.Fatalf("%s: create a site for the default client: %v", c.kind, serr)
		}
		if site.ClientID != clientID {
			t.Errorf("%s: site client = %v, want %s", c.kind, site.ClientID, clientID)
		}
	}
}

// TestDefaultClientBackfill covers the backfill of migration 000282 (WP-854,
// UI-15), executed from the migration file in a transaction that is rolled
// back: an organization without a client gets exactly the default client,
// an organization with a client gets no further one, a second run changes
// nothing. The installer organization "Reticora Demo" holds its default
// client since the migration.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL).
func TestDefaultClientBackfill(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping PostgreSQL integration test")
	}
	ctx := context.Background()
	adminPool, err := database.NewMaintenancePool(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(adminPool.Close)

	if n := countRows(ctx, t, adminPool, `SELECT count(*) FROM client
		WHERE organization_id = '00000000-0000-0000-0000-000000000001' AND slug = 'default' AND name = 'Reticora Demo'`); n != 1 {
		t.Errorf("default clients of Reticora Demo = %d, want 1", n)
	}

	raw, err := os.ReadFile("../../migrations/000282_default_client.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "-- ─── 2. Backfill")
	end := strings.Index(text, "-- ─── end of backfill")
	if start < 0 || end < start {
		t.Fatal("backfill markers not found in 000282_default_client.up.sql")
	}
	backfill := text[start:end]

	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	tag := hex.EncodeToString(suffix)

	tx, err := adminPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	// Two organizations as before the migration: the trigger is not there
	// yet, so neither holds a client; the second one gets an own client.
	if _, err = tx.Exec(ctx, `ALTER TABLE organization DISABLE TRIGGER trg_organization_default_client`); err != nil {
		t.Fatal(err)
	}
	var bare, served string
	if err = tx.QueryRow(ctx, `INSERT INTO organization (name, slug) VALUES ($1, $2) RETURNING id::text`,
		"Backfill Bare "+tag, "backfill-bare-"+tag).Scan(&bare); err != nil {
		t.Fatal(err)
	}
	if err = tx.QueryRow(ctx, `INSERT INTO organization (name, slug) VALUES ($1, $2) RETURNING id::text`,
		"Backfill Served "+tag, "backfill-served-"+tag).Scan(&served); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `ALTER TABLE organization ENABLE TRIGGER trg_organization_default_client`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO client (organization_id, name, slug) VALUES ($1, 'Eigener Kunde', 'eigen')`, served); err != nil {
		t.Fatal(err)
	}
	if got := clientsOf(ctx, t, tx, bare); len(got) != 0 {
		t.Fatalf("clients before the backfill = %v, want none", got)
	}

	for _, round := range []string{"first run", "second run"} {
		if _, err = tx.Exec(ctx, backfill); err != nil {
			t.Fatalf("%s: backfill: %v", round, err)
		}
		if got := clientsOf(ctx, t, tx, bare); len(got) != 1 || got[0] != "default/Backfill Bare "+tag {
			t.Errorf("%s: clients of the organization without one = %v, want exactly default/Backfill Bare %s", round, got, tag)
		}
		if got := clientsOf(ctx, t, tx, served); len(got) != 1 || got[0] != "eigen/Eigener Kunde" {
			t.Errorf("%s: clients of the organization with one = %v, want only eigen/Eigener Kunde", round, got)
		}
	}
}

// clientsOf lists the clients of orgID as slug/name.
func clientsOf(ctx context.Context, t *testing.T, q interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}, orgID string) []string {
	t.Helper()
	rows, err := q.Query(ctx, `SELECT slug || '/' || name FROM client WHERE organization_id = $1 ORDER BY slug`, orgID)
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatalf("clients: %v", err)
	}
	return got
}
