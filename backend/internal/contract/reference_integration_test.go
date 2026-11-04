package contract_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/contract"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/document"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/fieldmeta"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// Retest (WP-737, MGT-12, DB-05, MET-11, MET-12):
//
//	cd backend && go test -count=1 -p 1 -run 'TestContractReference' ./internal/contract/

// referenceViolation returns the detail of the violation of field, "" when
// err refuses nothing there. A *ci.ValidationError is the 422 of the CI
// routes.
func referenceViolation(err error, field string) string {
	var v *ci.ValidationError
	if !errors.As(err, &v) {
		return ""
	}
	for _, violation := range v.Violations {
		if violation.Field == field {
			return violation.Detail
		}
	}
	return ""
}

// TestContractReference covers WP-737 against PostgreSQL. References
// (MET-11, MET-12, MGT-12, DB-05): the contract is the canonical target of
// the reference attribute "contract" (contract_ref); the resolver of the
// contracts, hung on the CI service over the registry of WP-254, makes the
// target available. A reference to a contract of another organization, of
// another client or to a deleted contract is refused (422), an org-wide
// contract may be referenced from a client CI, and include=references
// names the contract by its number. Document links (MGT-12): a link to a
// contract of client 2 is invisible to a client-1 principal and its
// creation by that principal answers 404; a client change of the contract
// re-derives document_link.client_id; deleting a contract removes its
// links in the same transaction and keeps the document.
//
// The exclusion of the CI type contract from the templates (WP-115) is
// covered by citype/seed_integration_test.go.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestContractReference(t *testing.T) {
	f := scopetest.Seed(t, "73")
	bg := context.Background()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	scalar := func(sql string, args ...any) string {
		t.Helper()
		var out string
		if err := f.Admin.QueryRow(bg, sql, args...).Scan(&out); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return out
	}
	newContract := func(org, client, number string) string {
		t.Helper()
		var clientID any
		if client != "" {
			clientID = client
		}
		return scalar(`INSERT INTO contract (organization_id, client_id, number, currency) VALUES ($1, $2, $3, 'EUR') RETURNING id::text`,
			org, clientID, number)
	}
	cOrg := newContract(f.OrgA, "", "WP737-ORG")
	cC1 := newContract(f.OrgA, f.Client1, "WP737-C1")
	cC2 := newContract(f.OrgA, f.Client2, "WP737-C2")
	cB := newContract(f.OrgB, "", "WP737-B")
	cGone := newContract(f.OrgA, "", "WP737-GONE")
	contracts := contract.NewPGContracts(f.App)
	orgCtx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	client1Ctx := tenant.WithTenant(f.ClientCtx(f.Client1), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	if err := contracts.DeleteContract(orgCtx, f.OrgA, cGone); err != nil {
		t.Fatalf("delete contract: %v", err)
	}

	t.Run("references", func(t *testing.T) {
		types := citype.NewPGRepository(f.App)
		svc := ci.NewService(ci.NewPGRepository(f.App)).WithFieldResolver(types).
			WithReferences(ci.NewPGReferenceLookup(f.App)).
			WithReferenceTarget(fieldmeta.TargetContract, contract.NewReferenceResolver(contracts))
		if !fieldmeta.ReferenceTargetAvailable(fieldmeta.TargetContract) {
			t.Fatal("contract not available after the registration of its resolver")
		}
		// The definition of the attribute "contract" (DB-05): its target is
		// the table contract.
		const field = "wp737_contract"
		def := citype.AttributeDefinition{Key: field, DataType: fieldmeta.TypeContractRef}
		if err := types.CreateAttributeDefinition(orgCtx, f.OrgA, &def); err != nil {
			t.Fatalf("definition: %v", err)
		}
		var typeID string
		if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE organization_id IS NULL AND key = 'server'`).Scan(&typeID); err != nil {
			t.Fatal(err)
		}
		create := func(ctx context.Context, client, name, ref string) (*ci.Item, error) {
			item := &ci.Item{OrganizationID: f.OrgA, ClientID: client, CITypeID: typeID, Status: "active", Name: name,
				Attributes: map[string]any{field: ref}}
			return item, svc.Create(ctx, item)
		}

		for _, refused := range []struct {
			name, ref, want string
			ctx             context.Context
		}{
			{"contract of another organization", cB, "reference target not found", orgCtx},
			{"deleted contract", cGone, "reference target not found", orgCtx},
			{"contract of another client", cC2, "reference target belongs to another client", orgCtx},
			{"contract of a client outside the scope", cC2, "reference target not found", client1Ctx},
		} {
			_, err := create(refused.ctx, f.Client1, "wp737-"+strings.ReplaceAll(refused.name, " ", "-"), refused.ref)
			if got := referenceViolation(err, field); got != refused.want {
				t.Errorf("%s: %v, want violation %q", refused.name, err, refused.want)
			}
		}

		holder, err := create(client1Ctx, f.Client1, "wp737-holder", cOrg)
		if err != nil {
			t.Fatalf("org-wide contract from a client CI: %v", err)
		}
		if _, err = create(client1Ctx, f.Client1, "wp737-same-client", cC1); err != nil {
			t.Fatalf("contract of the same client: %v", err)
		}
		// A PATCH to a contract of another client is refused as well.
		if _, err = svc.Update(orgCtx, f.OrgA, holder.ID, ci.UpdateRequest{Attributes: map[string]any{field: cC2}}); referenceViolation(err, field) == "" {
			t.Errorf("patch to the contract of another client: %v, want a violation", err)
		}

		refCtx := identity.WithPermissions(orgCtx, []identity.Permission{identity.PermCIRead, identity.PermContractRead})
		refs, err := svc.References(refCtx, f.OrgA, holder.ID)
		if err != nil {
			t.Fatalf("references: %v", err)
		}
		raw, err := json.Marshal(refs[field])
		if err != nil {
			t.Fatal(err)
		}
		var view struct {
			Target      string  `json:"target"`
			ID          string  `json:"id"`
			DisplayName *string `json:"display_name"`
			Missing     bool    `json:"missing"`
		}
		if err = json.Unmarshal(raw, &view); err != nil {
			t.Fatalf("view %s: %v", raw, err)
		}
		if view.Target != fieldmeta.TargetContract || view.ID != cOrg || view.Missing || view.DisplayName == nil || *view.DisplayName != "WP737-ORG" {
			t.Errorf("effective view %s, want display_name WP737-ORG", raw)
		}
	})

	t.Run("document links", func(t *testing.T) {
		user := f.AppUser(t, f.OrgA, "wp737-author")
		doc := scalar(`INSERT INTO document (organization_id, title, file_name, storage_key, uploaded_by)
			VALUES ($1, 'wp737', 'wp737.pdf', '', $2) RETURNING id::text`, f.OrgA, user)
		// The link to the client-1 contract makes the document visible to the
		// client-1 principal.
		exec(`INSERT INTO document_link (organization_id, document_id, entity_type, entity_id) VALUES ($1, $2, 'contract', $3)`,
			f.OrgA, doc, cC1)

		mux := chi.NewRouter()
		document.NewHandler(document.NewPGRepository(f.App), nil).RegisterRoutes(mux)
		link := func(ctx context.Context, entityID string) int {
			t.Helper()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/documents/"+doc+"/links",
				strings.NewReader(`{"entity_type":"contract","entity_id":"`+entityID+`"}`))
			req.Header.Set("Content-Type", "application/json")
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req.WithContext(ctx))
			return rec.Code
		}
		if code := link(client1Ctx, cC2); code != http.StatusNotFound {
			t.Fatalf("client-1 link to the client-2 contract: %d, want 404", code)
		}
		if code := link(orgCtx, cC2); code != http.StatusCreated {
			t.Fatalf("org-wide link to the client-2 contract: %d, want 201", code)
		}
		linkClient := func() string {
			t.Helper()
			return scalar(`SELECT COALESCE(client_id::text, '') FROM document_link WHERE entity_type = 'contract' AND entity_id = $1`, cC2)
		}
		if got := linkClient(); got != f.Client2 {
			t.Fatalf("link client %q, want client 2", got)
		}
		visible := func(ctx context.Context, contractID string) int {
			t.Helper()
			var n int
			err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, `SELECT count(*) FROM document_link WHERE entity_type = 'contract' AND entity_id = $1`,
					contractID).Scan(&n)
			})
			if err != nil {
				t.Fatal(err)
			}
			return n
		}
		if n := visible(client1Ctx, cC2); n != 0 {
			t.Errorf("client-1 principal sees %d links to the client-2 contract, want 0", n)
		}
		if n := visible(orgCtx, cC2); n != 1 {
			t.Errorf("org-wide principal sees %d links to the client-2 contract, want 1", n)
		}

		// A client change of the contract re-derives the client of its links.
		exec(`UPDATE contract SET client_id = $2 WHERE id = $1`, cC2, f.Client1)
		if got := linkClient(); got != f.Client1 {
			t.Errorf("link client after the change %q, want client 1", got)
		}
		if n := visible(client1Ctx, cC2); n != 1 {
			t.Errorf("client-1 principal sees %d links after the change, want 1", n)
		}
		exec(`UPDATE contract SET client_id = NULL WHERE id = $1`, cC2)
		if got := linkClient(); got != "" {
			t.Errorf("link client after the change to org-wide %q, want none", got)
		}

		// Deleting a contract removes its links in the same transaction; the
		// document stays and link_count follows. The client-1 principal
		// deletes the client-1 contract.
		if err := contracts.DeleteContract(client1Ctx, f.OrgA, cC1); err != nil {
			t.Fatalf("delete client-1 contract: %v", err)
		}
		if n := scalar(`SELECT count(*)::text FROM document_link WHERE entity_type = 'contract' AND entity_id = $1`, cC1); n != "0" {
			t.Errorf("%s links to the deleted contract, want 0", n)
		}
		if n := scalar(`SELECT link_count::text FROM document WHERE id = $1`, doc); n != "1" {
			t.Errorf("link_count %s after the first deletion, want 1", n)
		}
		if err := contracts.DeleteContract(orgCtx, f.OrgA, cC2); err != nil {
			t.Fatalf("delete contract: %v", err)
		}
		if got := scalar(`SELECT link_count::text || '|' || (deleted_at IS NULL)::text FROM document WHERE id = $1`, doc); got != "0|true" {
			t.Errorf("document after the deletions %s, want link_count 0 and not deleted", got)
		}
	})
}
