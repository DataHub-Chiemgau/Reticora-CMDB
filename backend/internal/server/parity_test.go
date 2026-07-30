package server

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/api/speccheck"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/audit"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/ci"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/entitlement"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/identity"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/webhook"
	"github.com/go-chi/chi/v5"
)

// specPath points at the single source of truth for the public API contract.
const specPath = "../../../api/openapi.yaml"

func testRouter(t *testing.T) *chi.Mux {
	t.Helper()

	key, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatalf("generate master key: %v", err)
	}
	encryptor, err := crypto.NewEnvelopeEncryptor(key)
	if err != nil {
		t.Fatalf("new encryptor: %v", err)
	}

	repos := MemoryRepositories()
	entitlements := entitlement.NewService(repos.Entitlement, entitlement.Options{
		DefaultPlan: entitlement.PlanEnterprise,
		Enforce:     false,
	})

	mux, err := NewRouter(repos, Options{
		Version:      "test",
		Entitlements: entitlements,
		Dispatcher:   webhook.NewDispatcher(repos.Webhook, nil, webhook.DispatcherOptions{Deliveries: repos.WebhookDeliveries}),
		CIService:    ci.NewServiceWithLimits(repos.CI, entitlements),
		Credentials:  credential.NewService(repos.Credential, encryptor),
		OIDC:         identity.NewOIDCProvider(identity.OIDCConfig{}),
		Audit:        &audit.Handler{},
	})
	if err != nil {
		t.Fatalf("build router: %v", err)
	}
	return mux
}

// routerOperations walks the chi route tree and normalises the routes into the
// notation used by the OpenAPI specification.
func routerOperations(t *testing.T, mux *chi.Mux) []speccheck.Operation {
	t.Helper()

	var operations []speccheck.Operation
	err := chi.Walk(mux, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		route = strings.TrimSuffix(route, "/*")
		if route == "" {
			route = "/"
		}
		if route != "/" {
			route = strings.TrimSuffix(route, "/")
		}
		operations = append(operations, speccheck.Operation{Method: strings.ToUpper(method), Path: route})
		return nil
	})
	if err != nil {
		t.Fatalf("walk routes: %v", err)
	}
	return operations
}

func TestRoutesAndSpecificationAreInParity(t *testing.T) {
	documented, err := speccheck.Load(filepath.Clean(specPath))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}

	registered := routerOperations(t, testRouter(t))

	// /metrics is the Prometheus scrape endpoint, not part of the tenant API
	// contract, and is therefore excluded from the specification.
	filtered := registered[:0]
	for _, op := range registered {
		if op.Path == "/metrics" {
			continue
		}
		filtered = append(filtered, op)
	}

	undocumented, unimplemented := speccheck.Diff(filtered, documented)

	if len(undocumented) > 0 {
		t.Errorf("routes registered but missing from api/openapi.yaml:\n%s", format(undocumented))
	}
	if len(unimplemented) > 0 {
		t.Errorf("operations documented in api/openapi.yaml but not registered:\n%s", format(unimplemented))
	}
}

func format(ops []speccheck.Operation) string {
	lines := make([]string, 0, len(ops))
	for _, op := range ops {
		lines = append(lines, "  "+op.String())
	}
	return strings.Join(lines, "\n")
}

func TestNewRouterRequiresCoreDependencies(t *testing.T) {
	if _, err := NewRouter(Repositories{}, Options{}); err == nil {
		t.Fatal("expected an error when required dependencies are missing")
	}
}
