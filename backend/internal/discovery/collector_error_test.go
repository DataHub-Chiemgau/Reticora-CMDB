package discovery

import (
	"errors"
	"net/http"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api"
)

// TestCollectorErrorMapsGroupRules: the rules of the collector groups
// (000308) that refuse a collector move become 422 on site_id, like
// collector_site_client, without the database text.
func TestCollectorErrorMapsGroupRules(t *testing.T) {
	for _, constraint := range []string{"collector_site_client", "collector_group_member_client_rule", "probe_collector_group_client"} {
		err := collectorError(&pgconn.PgError{Code: "23514", ConstraintName: constraint, Message: "internal detail"})
		var fe *api.FieldError
		if !errors.As(err, &fe) || fe.Field != "site_id" || fe.Status != http.StatusUnprocessableEntity {
			t.Errorf("%s: %v, want 422 on site_id", constraint, err)
			continue
		}
		if fe.Message == "internal detail" {
			t.Errorf("%s: database text in the message", constraint)
		}
	}
	other := &pgconn.PgError{Code: "23514", ConstraintName: "something_else"}
	if err := collectorError(other); err != other {
		t.Errorf("unknown constraint: %v, want unchanged", err)
	}
}
