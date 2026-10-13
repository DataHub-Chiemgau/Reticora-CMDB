package tenantapi_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenantapi"
)

// TestOrgClientSiteFieldsOnPostgreSQL covers WP-107 (LOC-01–LOC-03,
// TEN-01, API-05) against PostgreSQL:
//   - organization time zone, currency and locale are stored; the database
//     rejects a time zone outside the tz database (field error);
//   - client names are unique per organization (conflict on name, also on
//     rename), slugs stay unique; external_ref and time zone round-trip and
//     clear;
//   - site names are unique per (organization, client): two clients may
//     each have a site "HQ", the same client not;
//   - migration 000101 renames clients with a duplicate name before the
//     unique key is added.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestOrgClientSiteFieldsOnPostgreSQL(t *testing.T) {
	f := scopetest.Seed(t, "84")
	bg := context.Background()
	repo := tenantapi.NewPGRepository(f.App)
	ctx := f.OrgCtx(f.OrgA)
	str := func(s string) *string { return &s }

	// Organization (LOC-01).
	org, err := repo.GetOrganization(ctx, f.OrgA)
	if err != nil || org.Timezone != "UTC" || org.Currency != "EUR" || org.Locale != "de-DE" {
		t.Fatalf("organization defaults: %+v %v", org, err)
	}
	org, err = repo.UpdateOrganization(ctx, f.OrgA, tenantapi.UpdateOrganizationRequest{
		Timezone: str("America/New_York"), Currency: str("USD"), Locale: str("en-US")})
	if err != nil || org.Timezone != "America/New_York" || org.Currency != "USD" || org.Locale != "en-US" {
		t.Fatalf("organization update: %+v %v", org, err)
	}
	var fieldErr *tenantapi.FieldError
	if _, err = repo.UpdateOrganization(ctx, f.OrgA, tenantapi.UpdateOrganizationRequest{Timezone: str("Mars/Base")}); !errors.As(err, &fieldErr) || fieldErr.Field != "timezone" {
		t.Errorf("database accepted a non-IANA time zone: %v", err)
	}
	if other, otherErr := repo.GetOrganization(ctx, f.OrgB); otherErr == nil {
		t.Errorf("organization B visible from organization A: %+v", other)
	}

	// Clients (LOC-02).
	alpha := &tenantapi.Client{OrganizationID: f.OrgA, Name: "Alpha-107", Slug: "alpha-107", ExternalRef: str("CRM-1"), Timezone: str("Europe/Berlin")}
	beta := &tenantapi.Client{OrganizationID: f.OrgA, Name: "Beta-107", Slug: "beta-107"}
	for _, c := range []*tenantapi.Client{alpha, beta} {
		if err = repo.CreateClient(ctx, c); err != nil {
			t.Fatalf("client %s: %v", c.Name, err)
		}
	}
	var conflict *tenantapi.ConflictError
	if err = repo.CreateClient(ctx, &tenantapi.Client{OrganizationID: f.OrgA, Name: "Alpha-107", Slug: "alpha-107-b"}); !errors.As(err, &conflict) || conflict.Field != "name" {
		t.Errorf("duplicate client name: %v", err)
	}
	if err = repo.CreateClient(ctx, &tenantapi.Client{OrganizationID: f.OrgA, Name: "Gamma-107", Slug: "alpha-107"}); !errors.As(err, &conflict) || conflict.Field != "slug" {
		t.Errorf("duplicate client slug: %v", err)
	}
	if _, err = repo.UpdateClient(ctx, f.OrgA, beta.ID, tenantapi.UpdateClientRequest{Name: str("Alpha-107")}); !errors.As(err, &conflict) || conflict.Field != "name" {
		t.Errorf("rename onto an existing client name: %v", err)
	}
	got, err := repo.GetClient(ctx, f.OrgA, alpha.ID)
	if err != nil || got.ExternalRef == nil || *got.ExternalRef != "CRM-1" || got.Timezone == nil || *got.Timezone != "Europe/Berlin" {
		t.Fatalf("client fields: %+v %v", got, err)
	}
	got, err = repo.UpdateClient(ctx, f.OrgA, alpha.ID, tenantapi.UpdateClientRequest{ExternalRef: str(""), Timezone: str("")})
	if err != nil || got.ExternalRef != nil || got.Timezone != nil {
		t.Errorf("clearing client fields: %+v %v", got, err)
	}
	if err = repo.CreateClient(f.OrgCtx(f.OrgB), &tenantapi.Client{OrganizationID: f.OrgB, Name: "Alpha-107", Slug: "alpha-107"}); err != nil {
		t.Errorf("same client name in another organization: %v", err)
	}

	// Sites (LOC-03): unique per client, not per organization.
	for _, s := range []*tenantapi.Site{
		{OrganizationID: f.OrgA, ClientID: alpha.ID, Name: "HQ", Timezone: str("Europe/Berlin")},
		{OrganizationID: f.OrgA, ClientID: beta.ID, Name: "HQ"},
	} {
		if err = repo.CreateSite(ctx, s); err != nil {
			t.Fatalf("site HQ of client %s: %v", s.ClientID, err)
		}
	}
	if err = repo.CreateSite(ctx, &tenantapi.Site{OrganizationID: f.OrgA, ClientID: alpha.ID, Name: "HQ"}); !errors.As(err, &conflict) || conflict.Field != "name" {
		t.Errorf("duplicate site name per client: %v", err)
	}
	if err = repo.CreateSite(ctx, &tenantapi.Site{OrganizationID: f.OrgA, ClientID: alpha.ID, Name: "Lab", Timezone: str("Nowhere/City")}); !errors.As(err, &fieldErr) || fieldErr.Field != "timezone" {
		t.Errorf("site with a non-IANA time zone: %v", err)
	}

	// Migration 000101 renames duplicate client names first.
	raw, err := os.ReadFile("../../migrations/000101_org_client_site_fields.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	start := strings.Index(text, "UPDATE client c SET name")
	rename := text[start : start+strings.Index(text[start:], ";")]
	tx, err := f.Admin.Begin(bg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(bg) }()
	if _, err = tx.Exec(bg, `ALTER TABLE client DROP CONSTRAINT client_org_name_key`); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, `UPDATE client SET name = 'Alpha-107' WHERE id = $1`, beta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(bg, rename); err != nil {
		t.Fatal(err)
	}
	var first, second string
	if err = tx.QueryRow(bg, `SELECT (SELECT name FROM client WHERE id = $1), (SELECT name FROM client WHERE id = $2)`, alpha.ID, beta.ID).Scan(&first, &second); err != nil ||
		first != "Alpha-107" || second != "Alpha-107 (beta-107)" {
		t.Errorf("migrated names: %q %q %v", first, second, err)
	}
}
