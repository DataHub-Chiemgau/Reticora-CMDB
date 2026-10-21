// Phase-1 acceptance audit, group T6-epic-b – former defect tests, moved
// from channel_secret_auditdefect_test.go with the fix (WP-399; F-152,
// F-153).
//
// WP-137 (NTF-01: channel config with encrypted secrets; SEC-08 egress
// protection for user-supplied destinations)
//
//   - TestAuditDefectSMTPSecretNotReleasedToNewHost
//     F-152: a PATCH that changes host, port, tls or username of an SMTP
//     channel and sends the password back as the placeholder "********" is
//     refused with 400 (field password): the stored configuration stays and
//     the stored password never reaches the new host. A rename, or a
//     configuration with the same destination, keeps the secret with the
//     placeholder; a new destination with a new password is accepted.
//   - TestAuditDefectSMTPChannelCloudMetadataBlocked
//     F-153: the server of an own SMTP channel is held to the egress policy.
//     The cloud metadata endpoints (169.254.169.254, fd00:ec2::254, a name
//     resolving to one) are refused in every mode, also with
//     AllowPrivate: when the channel is stored (422, field host) and when
//     the provider connects (egress.ErrBlockedDestination). Private,
//     loopback and unique-local servers and other ports in the SaaS mode,
//     public servers and AllowPrivate: TestOwnSMTPServerEgressPolicy
//     (channel_test.go).
//
// Retest:
//
//	cd backend && go test -count=1 -v -run 'TestAuditDefectSMTP' ./internal/notify/
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

func auditChannelAPI(t *testing.T, policy egress.Options) (*Center, func(method, path, body string) *httptest.ResponseRecorder) {
	t.Helper()
	center := NewMemoryCenter()
	mux := chi.NewRouter()
	NewHandler(center).WithEgress(policy).RegisterRoutes(mux)
	return center, func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(tenant.WithTenant(req.Context(), tenant.TenantInfo{OrganizationID: "org-e8", UserID: "user-e8"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, req)
		return w
	}
}

// problemField returns the field a problem response names.
func problemField(w *httptest.ResponseRecorder) string {
	var p struct {
		Field string `json:"field"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	return p.Field
}

func TestAuditDefectSMTPSecretNotReleasedToNewHost(t *testing.T) {
	center, call := auditChannelAPI(t, egress.Options{})
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
	stored := func(t *testing.T) *Channel {
		t.Helper()
		ch, err := center.Channels.Get(context.Background(), "org-e8", created.ID)
		if err != nil {
			t.Fatal(err)
		}
		return ch
	}

	t.Run("control: rename with the placeholder keeps the secret", func(t *testing.T) {
		if w := call(http.MethodPatch, path, `{"name":"audit-e8-corp-renamed"}`); w.Code != http.StatusOK {
			t.Fatalf("rename = %d %s", w.Code, w.Body.String())
		}
		if ch := stored(t); ch.Config["password"] != auditSMTPSecret {
			t.Errorf("password after a rename = %v, want the stored secret", ch.Config["password"])
		}
	})

	t.Run("redirect to a new host with the placeholder does not release the password", func(t *testing.T) {
		attacker := newFakeSMTP(t, false)
		w := call(http.MethodPatch, path, fmt.Sprintf(`{"config":{"host":"127.0.0.1","port":%d,"tls":"none",
			"from":"cmdb@corp.example.invalid","username":"svc-cmdb","password":"%s"}}`, attacker.port(), secretPlaceholder))
		t.Logf("PATCH destination change with the placeholder = %d %s", w.Code, w.Body.String())
		if w.Code != http.StatusBadRequest || problemField(w) != "password" {
			t.Errorf("PATCH destination change with the placeholder = %d field %q, want 400 field password", w.Code, problemField(w))
		}
		ch := stored(t)
		if ch.Config["host"] != "mail.corp.example.invalid" || ch.Config["password"] != auditSMTPSecret {
			t.Errorf("stored configuration after the refused PATCH: host %v, password kept %v", ch.Config["host"],
				ch.Config["password"] == auditSMTPSecret)
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

	t.Run("port, tls or username changed with the placeholder are refused", func(t *testing.T) {
		for name, cfg := range map[string]string{
			"port":            `"host":"mail.corp.example.invalid","port":465,"tls":"starttls","username":"svc-cmdb"`,
			"tls none":        `"host":"mail.corp.example.invalid","port":587,"tls":"none","username":"svc-cmdb"`,
			"username":        `"host":"mail.corp.example.invalid","port":587,"tls":"starttls","username":"other"`,
			"default port":    `"host":"mail.corp.example.invalid","tls":"tls","username":"svc-cmdb"`,
			"host, same port": `"host":"mail.attacker.example.invalid","port":587,"tls":"starttls","username":"svc-cmdb"`,
		} {
			w := call(http.MethodPatch, path, `{"config":{`+cfg+`,"from":"cmdb@corp.example.invalid","password":"`+secretPlaceholder+`"}}`)
			if w.Code != http.StatusBadRequest || problemField(w) != "password" {
				t.Errorf("%s: PATCH = %d %s, want 400 field password", name, w.Code, w.Body.String())
			}
			if strings.Contains(w.Body.String(), auditSMTPSecret) {
				t.Errorf("%s: the refusal reveals the password", name)
			}
		}
		if ch := stored(t); ch.Config["host"] != "mail.corp.example.invalid" || ch.Config["port"] != float64(587) ||
			ch.Config["username"] != "svc-cmdb" || ch.Config["password"] != auditSMTPSecret {
			t.Errorf("stored configuration changed by refused PATCHes: %v", ch.Config["host"])
		}
	})

	t.Run("the same destination keeps the secret with the placeholder", func(t *testing.T) {
		// The default port 587 and TLS mode starttls written out are the
		// same destination; the sender address is no destination.
		w := call(http.MethodPatch, path, `{"config":{"host":"mail.corp.example.invalid","username":"svc-cmdb",
			"from":"alerts@corp.example.invalid","password":"`+secretPlaceholder+`"}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH with the same destination = %d %s", w.Code, w.Body.String())
		}
		if ch := stored(t); ch.Config["password"] != auditSMTPSecret || ch.Config["from"] != "alerts@corp.example.invalid" {
			t.Errorf("stored after the PATCH: from %v, password kept %v", ch.Config["from"], ch.Config["password"] == auditSMTPSecret)
		}
	})

	t.Run("a new destination with a new password is accepted", func(t *testing.T) {
		w := call(http.MethodPatch, path, `{"config":{"host":"mail2.corp.example.invalid","port":465,"tls":"tls",
			"from":"cmdb@corp.example.invalid","username":"svc-cmdb","password":"new-secret"}}`)
		if w.Code != http.StatusOK {
			t.Fatalf("PATCH with a new password = %d %s", w.Code, w.Body.String())
		}
		if ch := stored(t); ch.Config["host"] != "mail2.corp.example.invalid" || ch.Config["password"] != "new-secret" {
			t.Errorf("stored after the PATCH: host %v, new password %v", ch.Config["host"], ch.Config["password"] == "new-secret")
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
	if verr == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, err := NewEmailProvider(nil).Send(ctx, ch, &Message{Subject: "audit", Body: "audit", To: []Recipient{{Email: "ops@corp.example.invalid"}}})
		if !errors.Is(err, egress.ErrBlockedDestination) {
			t.Errorf("SMTP channel host fd00:ec2::254 (cloud metadata): Validate() = nil and Send error = %v, want a validation error or %v (SEC-08)",
				err, egress.ErrBlockedDestination)
		}
	}

	for _, policy := range []egress.Options{{}, {AllowPrivate: true}} {
		mode := fmt.Sprintf("AllowPrivate=%v", policy.AllowPrivate)
		t.Run("stored "+mode, func(t *testing.T) {
			center, call := auditChannelAPI(t, policy)
			for _, host := range []string{"169.254.169.254", "fd00:ec2::254", "100.100.100.200"} {
				w := call(http.MethodPost, "/api/v1/notification-channels", `{"name":"audit-e8-`+host+`","provider":"smtp",
					"config":{"host":"`+host+`","port":25,"tls":"none","from":"cmdb@corp.example.invalid"}}`)
				if w.Code != http.StatusUnprocessableEntity || problemField(w) != "host" {
					t.Errorf("POST smtp host %s = %d %s, want 422 field host", host, w.Code, w.Body.String())
				}
				// The setting does not open metadata endpoints: it is not offered.
				if strings.Contains(w.Body.String(), "RETICORA_EGRESS_ALLOW_PRIVATE") {
					t.Errorf("POST smtp host %s: the refusal offers RETICORA_EGRESS_ALLOW_PRIVATE: %s", host, w.Body.String())
				}
			}
			if list, _ := center.Channels.List(context.Background(), "org-e8"); len(list) != 0 {
				t.Errorf("%d channels stored", len(list))
			}
		})
		t.Run("sent "+mode, func(t *testing.T) {
			policy.Resolver = staticResolver{"metadata.example.test": "169.254.169.254"}
			provider := NewEmailProvider(nil).WithEgress(policy)
			for _, host := range []string{"fd00:ec2::254", "metadata.example.test"} {
				ch := &Channel{Name: "metadata", Provider: ProviderSMTP, Enabled: true,
					Config: map[string]any{"host": host, "port": float64(25), "tls": "none", "from": "cmdb@corp.example.invalid"}}
				_, err := provider.Send(context.Background(), ch, &Message{Subject: "audit", Body: "audit",
					To: []Recipient{{Email: "ops@corp.example.invalid"}}})
				if !errors.Is(err, egress.ErrBlockedDestination) {
					t.Errorf("send to %s: %v, want %v", host, err, egress.ErrBlockedDestination)
				}
			}
		})
	}
}
