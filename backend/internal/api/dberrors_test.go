package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestWriteRepoErrorSanitizesPostgresErrors guards the defect where handlers
// passed err.Error() straight to the client, disclosing table, column and
// constraint names to anyone able to trigger a failed write.
func TestWriteRepoErrorSanitizesPostgresErrors(t *testing.T) {
	cases := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name: "unique violation becomes conflict",
			err: &pgconn.PgError{
				Code: "23505", Message: `duplicate key value violates unique constraint "ci_type_org_key_uniq"`,
				TableName: "ci_type", ColumnName: "key", ConstraintName: "ci_type_org_key_uniq",
			},
			wantStatus: http.StatusConflict,
		},
		{
			name: "foreign key violation becomes unprocessable",
			err: &pgconn.PgError{
				Code: "23503", Message: `insert or update on table "composition" violates foreign key constraint "composition_parent_asset_id_fkey"`,
				TableName: "composition", ConstraintName: "composition_parent_asset_id_fkey",
			},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name: "check violation becomes unprocessable",
			err: &pgconn.PgError{
				Code: "23514", Message: "composition cycle: an asset cannot be its own parent",
				TableName: "composition",
			},
			wantStatus: http.StatusUnprocessableEntity,
		},
		{
			name:       "unknown error becomes a generic internal error",
			err:        errors.New(`pq: relation "secret_internal_table" does not exist`),
			wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			WriteRepoError(w, tc.err)

			if w.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d (%s)", tc.wantStatus, w.Code, w.Body.String())
			}

			body := w.Body.String()
			for _, leak := range []string{
				"ci_type", "composition_parent_asset_id_fkey", "ci_type_org_key_uniq",
				"secret_internal_table", "duplicate key value", "SQLSTATE",
			} {
				if strings.Contains(body, leak) {
					t.Fatalf("response leaks schema detail %q: %s", leak, body)
				}
			}

			var problem map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &problem); err != nil {
				t.Fatalf("response is not JSON: %v (%s)", err, body)
			}
			if _, ok := problem["title"]; !ok {
				t.Fatalf("RFC 7807 response must carry a title: %s", body)
			}
		})
	}
}
