// CI type reference only to a system type or a type of the own organization
// (WP-396; F-144, backend part of F-016).
//
//   - TestAuditDefectForeignPrivateCITypeRejected – WP-120 (CI-01, TEN-05,
//     TEN-09; moved from audit_t6_router_auditdefect_test.go): organization
//     B's POST /cis with the private type of organization A is 422 and
//     stores no row; B's own type and a system type are 201.
//   - TestCITypeReferenceStaysInTheOrganization – WP-396 (CI-01, TEN-09):
//     POST /cis and POST /cis/{id}/type answer an unknown type ID and a
//     private type of another organization alike (422 on ci_type_id "CI
//     type not found", no oracle), a key such as "server" and a deactivated
//     type with 422 on ci_type_id; nothing is stored, audited or queued in
//     the outbox. The trigger ci_type_organization (migration 000153)
//     refuses a foreign type on the paths without the service check:
//     repository Create and CreateMany (discovery, agent), an UPDATE of the
//     application role and a write of the maintenance role that bypasses
//     row level security.
//
// The tests run the complete router of audit_t6_harness_test.go on
// PostgreSQL.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  go test -count=1 -v -p 1 -run 'TestAuditDefectForeignPrivateCIType|TestCITypeReferenceStaysInTheOrganization' ./internal/server/
package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
)

func TestAuditDefectForeignPrivateCITypeRejected(t *testing.T) {
	e := t6NewEnv(t, "f7", nil)
	f := e.F
	bg := context.Background()
	orgB := t6As{f.OrgB, f.AppUser(t, f.OrgB, "audit-da-b"), identity.AllPermissions()}
	orgType := func(org, key string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name) VALUES ($1, $2, $2) RETURNING id::text`, org, key).Scan(&id); err != nil {
			t.Fatalf("type %s: %v", key, err)
		}
		return id
	}
	privateA := orgType(f.OrgA, "audit-da-private-a")
	ownB := orgType(f.OrgB, "audit-da-own-b")
	var systemType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&systemType); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, typeID string
		want         int
	}{
		{"org A's private type", privateA, http.StatusUnprocessableEntity},
		{"system type", systemType, http.StatusCreated},
		{"org B's own type", ownB, http.StatusCreated},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := e.Call(t, orgB, http.MethodPost, "/api/v1/cis", `{"name":"audit-da-`+strings.ReplaceAll(tc.name, " ", "-")+`","ci_type_id":"`+tc.typeID+`"}`)
			rows := e.Count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND ci_type_id = $2`, f.OrgB, tc.typeID)
			wantRows := 0
			if tc.want == http.StatusCreated {
				wantRows = 1
			}
			if r.Code != tc.want || rows != wantRows {
				t.Errorf("org B POST /cis with %s = %d (org-B rows referencing it: %d), want %d and %d row(s): %s",
					tc.name, r.Code, rows, tc.want, wantRows, t6Short(r.Body))
			}
		})
	}
}

func TestCITypeReferenceStaysInTheOrganization(t *testing.T) {
	e := t6NewEnv(t, "0e", nil)
	f := e.F
	bg := context.Background()
	orgB := t6As{f.OrgB, f.AppUser(t, f.OrgB, "type-ref-b"), identity.AllPermissions()}
	orgType := func(org, key string) string {
		t.Helper()
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO ci_type (organization_id, key, name) VALUES ($1, $2, $2) RETURNING id::text`, org, key).Scan(&id); err != nil {
			t.Fatalf("type %s: %v", key, err)
		}
		return id
	}
	privateA, ownB, inactiveB := orgType(f.OrgA, "type-ref-private-a"), orgType(f.OrgB, "type-ref-own-b"), orgType(f.OrgB, "type-ref-inactive-b")
	if _, err := f.Admin.Exec(bg, `UPDATE ci_type SET is_active = false WHERE id = $1`, inactiveB); err != nil {
		t.Fatal(err)
	}
	var systemType string
	if err := f.Admin.QueryRow(bg, `SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`).Scan(&systemType); err != nil {
		t.Fatal(err)
	}
	unknown := f.ID()

	type problem struct {
		Status     int            `json:"status"`
		Title      string         `json:"title"`
		Detail     string         `json:"detail"`
		Violations []ci.Violation `json:"violations"`
	}
	// refused asserts a 422 with exactly one violation on ci_type_id and
	// returns the problem without its trace id.
	refused := func(t *testing.T, what string, r t6Response, detail string) problem {
		t.Helper()
		var p problem
		_ = json.Unmarshal(r.Body, &p)
		if r.Code != http.StatusUnprocessableEntity || len(p.Violations) != 1 ||
			p.Violations[0] != (ci.Violation{Field: "ci_type_id", Detail: detail}) {
			t.Errorf("%s: %d %s, want 422 on ci_type_id %q", what, r.Code, t6Short(r.Body), detail)
		}
		return p
	}
	// written counts what a write of organization B leaves: CIs, audit
	// entries and outbox events.
	written := func() [3]int {
		return [3]int{
			e.Count(t, `SELECT count(*) FROM ci WHERE organization_id = $1`, f.OrgB),
			e.Count(t, `SELECT count(*) FROM audit_log WHERE organization_id = $1`, f.OrgB),
			e.Count(t, `SELECT count(*) FROM event_outbox WHERE organization_id = $1`, f.OrgB),
		}
	}
	const (
		notFound    = "CI type not found"
		notAnID     = "not the ID (UUID) of a CI type"
		deactivated = "the CI type is deactivated"
	)

	t.Run("POST /cis", func(t *testing.T) {
		before := written()
		create := func(typeID string) t6Response {
			return e.Call(t, orgB, http.MethodPost, "/api/v1/cis", `{"name":"type-ref-create","ci_type_id":"`+typeID+`"}`)
		}
		foreign := refused(t, "private type of org A", create(privateA), notFound)
		if missing := refused(t, "unknown type ID", create(unknown), notFound); missing.Status != foreign.Status ||
			missing.Title != foreign.Title || missing.Detail != foreign.Detail {
			t.Errorf("unknown type %+v and foreign type %+v answer differently (existence oracle)", missing, foreign)
		}
		refused(t, `type key "server"`, create("server"), notAnID)
		refused(t, "deactivated type of org B", create(inactiveB), deactivated)
		if after := written(); after != before {
			t.Errorf("CIs, audit entries, outbox events of org B: %v before, %v after the refused creates", before, after)
		}
		for _, typeID := range []string{systemType, ownB} {
			if r := create(typeID); r.Code != http.StatusCreated {
				t.Errorf("create with type %s: %d %s, want 201", typeID, r.Code, t6Short(r.Body))
			}
		}
	})

	t.Run("POST /cis/{id}/type", func(t *testing.T) {
		id := f.CI(t, f.OrgB, "", "type-ref-change")
		before := written()
		change := func(typeID string) t6Response {
			return e.Call(t, orgB, http.MethodPost, "/api/v1/cis/"+id+"/type", `{"ci_type_id":"`+typeID+`"}`)
		}
		foreign := refused(t, "private type of org A", change(privateA), notFound)
		if missing := refused(t, "unknown type ID", change(unknown), notFound); missing.Status != foreign.Status ||
			missing.Title != foreign.Title || missing.Detail != foreign.Detail {
			t.Errorf("unknown type %+v and foreign type %+v answer differently (existence oracle)", missing, foreign)
		}
		refused(t, `type key "server"`, change("server"), notAnID)
		refused(t, "deactivated type of org B", change(inactiveB), deactivated)
		if n := e.Count(t, `SELECT count(*) FROM ci WHERE id = $1 AND ci_type_id = $2 AND version = 1`, id, systemType); n != 1 {
			t.Errorf("CI after the refused changes: type or version changed")
		}
		if after := written(); after != before {
			t.Errorf("CIs, audit entries, outbox events of org B: %v before, %v after the refused changes", before, after)
		}
		if r := change(ownB); r.Code != http.StatusOK {
			t.Errorf("change to org B's own type: %d %s, want 200", r.Code, t6Short(r.Body))
		}
		if n := e.Count(t, `SELECT count(*) FROM ci WHERE id = $1 AND ci_type_id = $2`, id, ownB); n != 1 {
			t.Errorf("CI does not have org B's own type after the change")
		}
	})

	t.Run("every write path", func(t *testing.T) {
		repo := ci.NewPGRepository(f.App)
		scopeB := database.OrgWideScope(f.OrgB, orgB.User)
		ctxB := database.ContextWithTenantScope(bg, &scopeB)
		item := func(name, typeID string) *ci.Item {
			return &ci.Item{OrganizationID: f.OrgB, CITypeID: typeID, Name: name, Status: "active", DiscoverySource: ci.SourceSNMP}
		}
		isRefused := func(err error) bool {
			v, ok := ci.AsValidationError(err)
			return ok && len(v.Violations) == 1 && v.Violations[0] == (ci.Violation{Field: "ci_type_id", Detail: notFound})
		}
		stored := func(names ...string) int {
			return e.Count(t, `SELECT count(*) FROM ci WHERE organization_id = $1 AND name = ANY($2)`, f.OrgB, names)
		}
		// Discovery and the agent create without the service check: the
		// trigger refuses the foreign type, unknown IDs fail the foreign key.
		for _, typeID := range []string{privateA, unknown} {
			if err := repo.Create(ctxB, item("type-ref-discovered", typeID)); !isRefused(err) {
				t.Errorf("repository Create with type %s: %v, want 422 on ci_type_id %q", typeID, err, notFound)
			}
			if _, err := repo.CreateMany(ctxB, f.OrgB, []*ci.Item{item("type-ref-batch-own", ownB), item("type-ref-batch-foreign", typeID)}); !isRefused(err) {
				t.Errorf("CreateMany with type %s: %v, want 422 on ci_type_id %q", typeID, err, notFound)
			}
		}
		if n := stored("type-ref-discovered", "type-ref-batch-own", "type-ref-batch-foreign"); n != 0 {
			t.Errorf("%d CIs of the refused repository writes stored", n)
		}
		if _, err := repo.ChangeType(ctxB, f.OrgB, f.CI(t, f.OrgB, "", "type-ref-reclassify"), privateA, ci.TypeChangeDiscovery); !errors.Is(err, ci.ErrUnknownType) {
			t.Errorf("discovery reclassification to org A's type: %v, want ErrUnknownType", err)
		}
		if err := repo.Create(ctxB, item("type-ref-discovered-own", ownB)); err != nil {
			t.Errorf("repository Create with org B's own type: %v", err)
		}

		// The trigger itself: an UPDATE of the application role under org B's
		// row level security, and writes of the maintenance role, which
		// bypasses row level security (scripts, imports in SQL).
		ciB := f.CI(t, f.OrgB, "", "type-ref-sql")
		violatesTrigger := func(what string, err error) {
			t.Helper()
			var pgErr *pgconn.PgError
			if !errors.As(err, &pgErr) || pgErr.Code != "23514" || pgErr.ConstraintName != "ci_type_organization" {
				t.Errorf("%s: %v, want 23514 ci_type_organization", what, err)
			}
		}
		violatesTrigger("UPDATE ci_type_id as reticora_app", database.WithRequestTenant(ctxB, f.App, f.OrgB, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, ciB, privateA)
			return err
		}))
		_, err := f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, ciB, privateA)
		violatesTrigger("UPDATE ci_type_id as maintenance role", err)
		_, err = f.Admin.Exec(bg, `INSERT INTO ci (organization_id, ci_type_id, name) VALUES ($1, $2, 'type-ref-sql-insert')`, f.OrgB, privateA)
		violatesTrigger("INSERT as maintenance role", err)
		if _, err = f.Admin.Exec(bg, `UPDATE ci SET ci_type_id = $2 WHERE id = $1`, ciB, ownB); err != nil {
			t.Errorf("UPDATE to org B's own type as maintenance role: %v", err)
		}

		// The check query of migration 000153 finds nothing.
		if n := e.Count(t, `SELECT count(*) FROM ci c JOIN ci_type t ON t.id = c.ci_type_id
			WHERE t.organization_id IS NOT NULL AND t.organization_id <> c.organization_id`); n != 0 {
			t.Errorf("%d CIs have a CI type of another organization", n)
		}
	})
}
