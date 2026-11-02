package ci_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/citype"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestSchemaVersionFollowsTypedWrites covers MET-16 (WP-590, F-309)
// against PostgreSQL: a CI records the schema version of its type with
// every write of a typed column, not only with an attribute or type
// change. A CI written against v2 whose type moved on to v4 stores v4 after
// PATCH hostname; each other typed column of CI-01 written as reticora_app
// (tenant GUCs) stores the type's current version as well. A write of
// system fields alone (last_seen_at, first_seen_at) keeps the version.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestSchemaVersionFollowsTypedWrites(t *testing.T) {
	f := scopetest.Seed(t, "59")
	bg := context.Background()
	ctx := tenant.WithTenant(f.OrgCtx(f.OrgA), tenant.TenantInfo{OrganizationID: f.OrgA, UserID: f.User})
	types := citype.NewPGRepository(f.App)
	typ := &citype.Type{OrganizationID: f.OrgA, Key: "wp590-server", Name: "WP-590 Server"}
	if err := types.Create(ctx, typ); err != nil {
		t.Fatalf("create type: %v", err)
	}
	defs := 0
	// bump publishes a new attribute of the type: its schema gets the next
	// version.
	bump := func(want int) {
		t.Helper()
		defs++
		if err := types.CreateAttributeDefinition(ctx, f.OrgA, &citype.AttributeDefinition{
			CITypeID: typ.ID, Key: fmt.Sprintf("wp590_note_%d", defs), DataType: "text"}); err != nil {
			t.Fatalf("define attribute %d: %v", defs, err)
		}
		got, err := types.GetByID(ctx, f.OrgA, typ.ID)
		if err != nil || got.SchemaVersion != want {
			t.Fatalf("type schema version = %v (%v), want %d", got, err, want)
		}
	}
	stored := func(id string) int {
		t.Helper()
		var v int
		if err := f.Admin.QueryRow(bg, `SELECT schema_version FROM ci WHERE id = $1`, id).Scan(&v); err != nil {
			t.Fatalf("read schema_version: %v", err)
		}
		return v
	}

	cis := ci.NewService(ci.NewPGRepository(f.App)).WithFieldResolver(types)
	mux := chi.NewRouter()
	ci.NewHandler(cis).RegisterRoutes(mux)
	call := func(method, path string, body any) (int, map[string]any) {
		t.Helper()
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		mux.ServeHTTP(w, req.WithContext(ctx))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}

	// CI v2.
	bump(2)
	code, out := call(http.MethodPost, "/api/v1/cis", map[string]any{"name": "wp590-ci", "ci_type_id": typ.ID,
		"hostname": "wp590-old"})
	id, _ := out["id"].(string)
	if code != http.StatusCreated || id == "" || out["schema_version"] != float64(2) {
		t.Fatalf("create: %d %v, want 201 with schema_version 2", code, out)
	}

	// The type moves on to v4; the CI keeps v2 until its next write.
	bump(3)
	bump(4)
	if v := stored(id); v != 2 {
		t.Fatalf("CI before its next write: v%d, want v2", v)
	}

	// PATCH hostname alone (a typed column, no attribute) → v4.
	code, out = call(http.MethodPatch, "/api/v1/cis/"+id, map[string]any{"hostname": "wp590-new"})
	if code != http.StatusOK || out["schema_version"] != float64(4) {
		t.Errorf("PATCH hostname: %d schema_version %v, want 200 with 4", code, out["schema_version"])
	}
	if v := stored(id); v != 4 {
		t.Errorf("stored after PATCH hostname: v%d, want v4", v)
	}

	// System fields alone keep the version: a sighting through the service
	// (discovery path) and a direct write as reticora_app.
	bump(5)
	seen := time.Now().UTC().Add(time.Minute).Format(time.RFC3339)
	if _, err := cis.Update(ctx, f.OrgA, id, ci.UpdateRequest{LastSeenAt: &seen}); err != nil {
		t.Fatalf("update last_seen_at: %v", err)
	}
	if v := stored(id); v != 4 {
		t.Errorf("after the last_seen_at update: v%d, want v4 unchanged", v)
	}
	appExec := func(sql string, args ...any) {
		t.Helper()
		if err := database.WithRequestTenant(ctx, f.App, f.OrgA, func(ctx context.Context, tx pgx.Tx) error {
			tag, err := tx.Exec(ctx, sql, args...)
			if err == nil && tag.RowsAffected() != 1 {
				err = fmt.Errorf("%d rows updated, want 1", tag.RowsAffected())
			}
			return err
		}); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	appExec(`UPDATE ci SET last_seen_at = now() + interval '2 minutes', first_seen_at = now() - interval '1 day' WHERE id = $1`, id)
	if v := stored(id); v != 4 {
		t.Errorf("after the system field write as reticora_app: v%d, want v4 unchanged", v)
	}

	// Every typed column of CI-01 written alone stores the current version.
	version := 5
	for _, tc := range []struct{ column, value string }{
		{"name", "wp590-renamed"},
		{"manufacturer", "WP590 Inc"},
		{"model", "M590"},
		{"serial_number", "WP590-SN"},
		{"hardware_uuid", "4c4c4544-0000-1000-8000-000000059001"},
		{"management_ip", "10.59.0.1"},
		{"primary_mac", "00:11:22:33:59:01"},
		{"hostname", "wp590-typed"},
		{"fqdn", "wp590.example.org"},
		{"os_name", "Debian"},
		{"os_version", "12"},
		{"firmware_version", "5.9"},
		{"sys_object_id", "1.3.6.1.4.1.590"},
	} {
		if tc.column != "name" {
			version++
			bump(version)
		}
		appExec(fmt.Sprintf(`UPDATE ci SET %s = $2 WHERE id = $1`, pgx.Identifier{tc.column}.Sanitize()), id, tc.value)
		if v := stored(id); v != version {
			t.Errorf("after a write of %s alone: v%d, want v%d", tc.column, v, version)
		}
	}
}
