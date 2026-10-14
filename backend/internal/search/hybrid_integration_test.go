package search_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/search"
)

// remoteIndex records the documents sent to the remote index.
type remoteIndex struct {
	search.Backend
	indexed []string
	fail    error
}

func (r *remoteIndex) IndexDocument(_ context.Context, doc search.Document) error {
	r.indexed = append(r.indexed, doc.EntityID)
	return r.fail
}

// TestHybridIndexesRemoteAfterCommit covers WP-332 (NFR-03) against
// PostgreSQL: inside a transaction in the context (the ingest's record
// transaction) the remote index, an HTTP call, is updated only after the
// commit and not at all after a rollback; outside a transaction it is
// updated at once and its error is returned.
//
// It runs only against PostgreSQL (TEST_DATABASE_URL, see scopetest.Seed).
func TestHybridIndexesRemoteAfterCommit(t *testing.T) {
	f := scopetest.Seed(t, "e5")
	ctx := f.OrgCtx(f.OrgA)
	remote := &remoteIndex{}
	backend := &search.HybridBackend{Remote: remote, Source: search.NewPGRepository(f.App)}
	doc := func(id string) search.Document {
		return search.Document{OrganizationID: f.OrgA, EntityType: "ci", EntityID: id, Title: id}
	}

	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if err := backend.IndexDocument(ctx, doc("committed")); err != nil {
			return err
		}
		if len(remote.indexed) != 0 {
			t.Error("remote index called inside the transaction")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	errAbort := errors.New("abort")
	if err := database.WithRequestTenantTx(ctx, f.App, f.OrgA, func(ctx context.Context) error {
		if err := backend.IndexDocument(ctx, doc("rolled-back")); err != nil {
			return err
		}
		return errAbort
	}); !errors.Is(err, errAbort) {
		t.Fatal(err)
	}
	if len(remote.indexed) != 1 || remote.indexed[0] != "committed" {
		t.Errorf("remote index after commit and rollback: %v, want only the committed document", remote.indexed)
	}

	remote.fail = errors.New("remote down")
	if err := backend.IndexDocument(ctx, doc("direct")); !errors.Is(err, remote.fail) || len(remote.indexed) != 2 {
		t.Errorf("outside a transaction: %v, %v; want the remote call at once with its error", err, remote.indexed)
	}
}
