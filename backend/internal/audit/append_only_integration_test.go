package audit_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// TestRuntimeRoleCannotRewriteAuditChain covers WP-680 (AUD-04, WP-132 AK1
// "Einträge nicht editierbar"; F-409, probe verify-tamper-guc.sql): the
// runtime login, as itself, as reticora_app and after SET ROLE
// reticora_owner, declares app.audit_pii_migration, rewrites the changes of
// an entry and re-chains the organization with audit_rechain. Every attempt
// fails, nothing is stored, and the chain keeps its entries and hashes;
// audit.Verify reports it intact over the original entries only.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestRuntimeRoleCannotRewriteAuditChain(t *testing.T) {
	f := scopetest.Seed(t, "2a")
	bg := context.Background()
	recorder := audit.NewPGRecorder()
	scope := database.OrgWideScope(f.OrgA, "")
	if err := database.WithTenant(bg, f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		for _, changes := range []map[string]interface{}{{"name": "evidence-1"}, {"model": "R650"}, {"model": "R750"}} {
			if _, err := recorder.Record(ctx, tx, audit.Entry{OrganizationID: f.OrgA, ActorType: "user", Action: "ci.updated",
				ResourceType: "ci", Source: audit.SourceUI, Changes: changes}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("record entries: %v", err)
	}
	type stored struct{ changes, prev, hash string }
	chain := func() []stored {
		t.Helper()
		rows, err := f.Admin.Query(bg, `SELECT changes::text, previous_hash, hash FROM audit_log WHERE organization_id = $1 ORDER BY seq`, f.OrgA)
		if err != nil {
			t.Fatal(err)
		}
		out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (stored, error) {
			var s stored
			scanErr := r.Scan(&s.changes, &s.prev, &s.hash)
			return s, scanErr
		})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := chain()
	if len(before) != 3 {
		t.Fatalf("setup: %d entries, want 3", len(before))
	}

	// The runtime login as itself (as in the probe), as the application role
	// of the pool and as the owner role it may switch to.
	conn, err := pgx.Connect(bg, scopetest.AppDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(bg) }()
	for _, roles := range [][]string{nil, {database.DefaultAppRole}, {database.DefaultAppRole, database.DefaultOwnerRole}} {
		name := scopetest.AppLogin
		if len(roles) > 0 {
			name = roles[len(roles)-1]
		}
		tx, berr := conn.Begin(bg)
		if berr != nil {
			t.Fatal(berr)
		}
		for _, role := range roles {
			if _, err = tx.Exec(bg, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
				t.Fatalf("SET ROLE %s: %v", role, err)
			}
		}
		if _, err = tx.Exec(bg, `SELECT set_config('app.org_id', $1, true), set_config('app.audit_pii_migration', 'on', true)`, f.OrgA); err != nil {
			t.Fatal(err)
		}
		forged := true
		tag, uerr := tx.Exec(bg, `UPDATE audit_log SET changes = '{"name": "forged"}' WHERE organization_id = $1 AND seq = 1`, f.OrgA)
		if uerr == nil {
			t.Errorf("as %s: UPDATE of the changes under app.audit_pii_migration = %s, want it refused", name, tag)
			var rechained int
			if rerr := tx.QueryRow(bg, `SELECT audit_rechain($1, 1)`, f.OrgA).Scan(&rechained); rerr == nil {
				t.Errorf("as %s: audit_rechain re-chained %d entries, want it refused", name, rechained)
			} else {
				forged = false
			}
		} else {
			forged = false
		}
		if forged {
			if err = tx.Commit(bg); err != nil {
				t.Fatal(err)
			}
		} else {
			_ = tx.Rollback(bg)
		}
		// audit_rechain on its own is refused as well.
		tx, berr = conn.Begin(bg)
		if berr != nil {
			t.Fatal(berr)
		}
		for _, role := range roles {
			if _, err = tx.Exec(bg, "SET LOCAL ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
				t.Fatalf("SET ROLE %s: %v", role, err)
			}
		}
		if _, err = tx.Exec(bg, `SELECT set_config('app.org_id', $1, true), set_config('app.audit_pii_migration', 'on', true)`, f.OrgA); err != nil {
			t.Fatal(err)
		}
		var rechained int
		if rerr := tx.QueryRow(bg, `SELECT audit_rechain($1, 1)`, f.OrgA).Scan(&rechained); rerr == nil {
			t.Errorf("as %s: audit_rechain alone re-chained %d entries, want it refused", name, rechained)
		}
		_ = tx.Rollback(bg)
	}

	after := chain()
	for i := range before {
		if i >= len(after) || after[i] != before[i] {
			t.Errorf("entry %d changed by the runtime login: %+v, want %+v", i+1, after, before)
			break
		}
	}
	res, err := audit.Verify(bg, f.App, f.OrgA)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Intact || res.Checked != 3 || after[0].changes != `{"name": "evidence-1"}` {
		t.Errorf("chain after the attempts = %+v with first entry %s; want intact over the 3 original entries", res, after[0].changes)
	}
}
