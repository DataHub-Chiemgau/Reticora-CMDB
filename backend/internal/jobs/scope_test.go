package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

// TestScopeParamsRestoreTheTrigger covers the trigger scope of WP-712
// (TEN-09, E-08): a job queued in a client-scoped request captures request
// id and source in its parameters and the scope snapshot on its row; the
// worker context restores tenant, actor and scope through CreatorContext
// and the request info with the job id as correlation id. A job without
// scope snapshot is an error instead of running organization-wide.
func TestScopeParamsRestoreTheTrigger(t *testing.T) {
	const org, user, client = "c0de0712-0000-4000-8000-000000000001", "c0de0712-0000-4000-8000-0000000000f1",
		"c0de0712-0000-4000-8000-0000000000c1"
	s, _ := newService()
	s.Register(Kind{Name: "template.migrate"})

	// The request: a user scoped to one client, with request id and source.
	scope := database.OrgWideScope(org, user)
	scope.Clients = database.ScopeIDs(client)
	reqCtx := database.ContextWithTenantScope(context.Background(), &scope)
	reqCtx = audit.WithRequestInfo(reqCtx, audit.RequestInfo{RequestID: "req-712", CorrelationID: "corr-request", Source: "ui"})

	captured := CaptureScope(reqCtx)
	if captured != (ScopeParams{RequestID: "req-712", Source: "ui"}) {
		t.Fatalf("captured %+v", captured)
	}
	if empty := CaptureScope(context.Background()); empty != (ScopeParams{}) {
		t.Errorf("outside a request: %+v, want none", empty)
	}

	type params struct {
		ScopeParams
		TemplateID string `json:"template_id"`
	}
	job, _, err := s.Enqueue(reqCtx, org, "template.migrate", params{ScopeParams: captured, TemplateID: "t1"},
		EnqueueOptions{CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	var stored params
	if err = json.Unmarshal(job.Params, &stored); err != nil || stored.ScopeParams != captured {
		t.Fatalf("job parameters %s: %+v %v", job.Params, stored, err)
	}

	// The worker: a context without tenant, scope or request.
	worker, err := s.Get(context.Background(), org, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	runCtx, err := stored.Context(context.Background(), worker)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	if ti := tenant.FromContext(runCtx); ti.OrganizationID != org || ti.UserID != user {
		t.Errorf("tenant %+v, want org %s and actor %s", ti, org, user)
	}
	restored, ok := database.TenantScopeFromContext(runCtx)
	if !ok || restored.OrgID != org || restored.UserID != user || restored.Clients.IsAll() ||
		!slices.Equal(restored.Clients.IDs(), []string{client}) || !restored.Sites.IsAll() || !restored.Teams.IsAll() {
		t.Errorf("scope %+v (clients %v), want the client scope of the trigger", restored, restored.Clients.IDs())
	}
	info, ok := audit.RequestInfoFrom(runCtx)
	if !ok || info.RequestID != "req-712" || info.Source != "ui" || info.CorrelationID != job.ID {
		t.Errorf("request info %+v, want request req-712, source ui, correlation id %s", info, job.ID)
	}

	// Negative: without snapshot (system job, job from before 000154) the
	// job does not run organization-wide.
	system, _, err := s.Enqueue(context.Background(), org, "template.migrate", params{TemplateID: "t1"}, EnqueueOptions{CreatedBy: user})
	if err != nil {
		t.Fatal(err)
	}
	if system.Scope != nil {
		t.Fatalf("a job queued outside a request has the snapshot %+v", system.Scope)
	}
	if ctx, cerr := (ScopeParams{RequestID: "x"}).Context(context.Background(), system); !errors.Is(cerr, ErrNoScopeSnapshot) || ctx != nil {
		t.Errorf("job without snapshot: %v, want ErrNoScopeSnapshot", cerr)
	}
	if _, cerr := captured.Context(context.Background(), nil); !errors.Is(cerr, ErrNoScopeSnapshot) {
		t.Errorf("no job: %v, want ErrNoScopeSnapshot", cerr)
	}
}
