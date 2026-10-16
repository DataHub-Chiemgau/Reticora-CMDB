//go:build auditdefect

// Phase-1 acceptance audit, group T6-epic-b – DEFECT tests (must fail until fixed).
//
// WP-137 (NTF-01: channel config with encrypted secrets; SEC-08 egress
// protection for user-supplied destinations)
//
//   - TestAuditDefectSMTPSecretNotReleasedToNewHost
//     Finding P2 "Stored SMTP password is sent to a new host set by a PATCH
//     that never knew the password": handler.go UpdateChannel replaces the
//     placeholder "********" with the stored secret regardless of a changed
//     destination, so a caller who can edit channels but never saw the
//     password redirects it to a host of its choice.
//     Gap (high): "Secret-preserving update must not accept a destination
//     change". Requirement encoded fix-agnostically: after such a PATCH the
//     stored password never reaches the new host (refusing the PATCH or
//     dropping the secret both satisfy it). Control: a rename with the
//     placeholder keeps the secret (documented behaviour).
//   - TestAuditDefectSMTPChannelCloudMetadataBlocked
//     Finding P2 "Own SMTP channel host bypasses the egress policy" (smtp.go
//     dials with a plain net.Dialer; egress is wired only into the webhook
//     provider). Gap (high): "Egress check for own SMTP hosts". Only the
//     unambiguous part is encoded: the link-local cloud-metadata address is
//     never a valid mail server (NTF-01 allows customer SMTP on private
//     networks on-prem, so private/loopback hosts are not asserted here).
//     The IPv6 metadata address fd00:ec2::254 is used so the unfixed dial
//     fails locally (no IPv6 route) instead of reaching 169.254.169.254.
//
// Retest:
//
//	cd backend && go test -tags auditdefect -count=1 -v -run 'TestAuditDefectSMTP' ./internal/notify/
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/tenant"
)

const auditSMTPSecret = "audit-e8-corp-secret"

func auditChannelAPI(t *testing.T) (*Center, func(method, path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	center := NewMemoryCenter()
	mux := chi.NewRouter()
	NewHandler(center).RegisterRoutes(mux)
	return center, func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-e8", UserID: "user-e8"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
}

func TestAuditDefectSMTPSecretNotReleasedToNewHost(t *testing.T) {
	center, call := auditChannelAPI(t)
	w := call(http.MethodPost, "/api/v1/notification-channels", `{"name":"audit-e8-corp","provider":"smtp",
		"config":{"host":"mail.corp.example.invalid","port":587,"tls":"starttls","from":"cmdb@corp.example.invalid",
		          "username":"svc-cmdb","password":"`+auditSMTPSecret+`"}}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create channel = %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == "" {
		t.Fatalf("create body %s: %v", w.Body.String(), err)
	}
	path := "/api/v1/notification-channels/" + created.ID

	t.Run("control: rename with the placeholder keeps the secret", func(t *testing.T) {
		if w := call(http.MethodPatch, path, `{"name":"audit-e8-corp-renamed"}`); w.Code != http.StatusOK {
			t.Fatalf("rename = %d %s", w.Code, w.Body.String())
		}
		ch, err := center.Channels.Get(context.Background(), "org-e8", created.ID)
		if err != nil {
			t.Fatal(err)
		}
		if ch.Config["password"] != auditSMTPSecret {
			t.Errorf("password after a rename = %v, want the stored secret", ch.Config["password"])
		}
	})

	t.Run("redirect to a new host with the placeholder does not release the password", func(t *testing.T) {
		attacker := newFakeSMTP(t, false)
		w := call(http.MethodPatch, path, fmt.Sprintf(`{"config":{"host":"127.0.0.1","port":%d,"tls":"none",
			"from":"cmdb@corp.example.invalid","username":"svc-cmdb","password":"%s"}}`, attacker.port(), secretPlaceholder))
		t.Logf("PATCH destination change with the placeholder = %d %s", w.Code, w.Body.String())
		ch, err := center.Channels.Get(context.Background(), "org-e8", created.ID)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, sendErr := NewEmailProvider(nil).Send(ctx, ch, &Message{Subject: "audit", Body: "audit", To: []Recipient{{Email: "ops@corp.example.invalid"}}})
		attacker.mu.Lock()
		auth := append([]string(nil), attacker.auth...)
		attacker.mu.Unlock()
		t.Logf("send after the PATCH: err=%v, AUTH sessions at the new host: %d", sendErr, len(auth))
		for _, a := range auth {
			if strings.Contains(a, auditSMTPSecret) {
				t.Errorf("the stored SMTP password was sent to the new host %s set by a PATCH that never supplied it (PATCH status %d)",
					ch.Config["host"], w.Code)
			}
		}
	})
}

func TestAuditDefectSMTPChannelCloudMetadataBlocked(t *testing.T) {
	// fd00:ec2::254 is the IPv6 cloud-metadata endpoint, blocked by
	// egress.metadataPrefixes in every mode. The sandbox has no IPv6 route,
	// so the unfixed provider fails locally without contacting anything.
	ch := &Channel{Name: "audit-e8-metadata", Provider: ProviderSMTP, Enabled: true,
		Config: map[string]any{"host": "fd00:ec2::254", "port": float64(25), "tls": "none", "from": "cmdb@corp.example.invalid"}}
	verr := ch.Validate()
	t.Logf("Validate() = %v", verr)
	if verr != nil {
		return // refused at validation: requirement met
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := NewEmailProvider(nil).Send(ctx, ch, &Message{Subject: "audit", Body: "audit", To: []Recipient{{Email: "ops@corp.example.invalid"}}})
	if !errors.Is(err, egress.ErrBlockedDestination) {
		t.Errorf("SMTP channel host fd00:ec2::254 (cloud metadata): Validate() = nil and Send error = %v, want a validation error or %v (SEC-08)",
			err, egress.ErrBlockedDestination)
	}
}
