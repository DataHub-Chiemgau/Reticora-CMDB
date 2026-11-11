// Credential bundle selection on PostgreSQL (WP-376, COL-02, DIS-09, INS-06,
// findings F-027 and F-264, decision E-67 with its transition rule option A).
//
//   - TestAuditBundleCredentialSelection (moved from
//     collector_pki_auditdefect_test.go): the bundle holds the union of the
//     effective credentials of the collector's enabled scopes. Without an
//     enabled scope it is empty; a scope without credential_ids (UI form,
//     accepted suggestion) uses the organization-wide credentials and those
//     of its client, never another client's; a scope with credential_ids
//     exactly those, without a credential of another client bound before the
//     F-264 fix. The scope channel sends each scope its effective
//     credential_ids, whose union is the bundle. Over the collector endpoint
//     the collector receives exactly these credentials, and the audit
//     credential.bundle_delivered names them (an empty list for an empty
//     bundle).
//
// The delivery runs an internal nats-server with nats-edge.conf bridged to it
// (startRenewStack, TEST_NATS_SERVER_BIN, WP-374; that subtest is skipped
// without it and fails in the CI). The end-to-end check of the onboarding
// with a real collector is WP-384.
//
// Retest:
//
//	cd backend && TEST_DATABASE_URL=postgres://reticora:reticora@localhost:55432/<db>?sslmode=disable \
//	  TEST_NATS_SERVER_BIN=<path to nats-server 2.10+> go test -count=1 -v -p 1 \
//	  -run 'TestAuditBundleCredentialSelection' ./internal/discovery/
package discovery_test

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
)

// bundleScope is a scope of a test case: created through the repository or,
// raw, inserted directly into the database past the credential check (a
// scope bound before the F-264 fix). channel are the credential_ids the
// scope channel sends for it (enabled scopes only).
type bundleScope struct {
	in      discovery.ScopeInput
	raw     bool
	channel []string
}

// bundleCollector is an enrolled collector of a test case and the
// credentials its bundle must hold.
type bundleCollector struct {
	id     string
	key    *ecdsa.PrivateKey
	issued *discovery.IssuedCert
	want   []string
}

func TestAuditBundleCredentialSelection(t *testing.T) {
	e, enc := auditEdge(t, "0f")
	f := e.f
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)
	gate := discovery.NewBundleGate(f.App, e.ca)
	csvc := credential.NewService(credential.NewPGRepository(f.App), enc)
	h := discovery.NewHandler(e.repo, nil)
	mk := func(name, clientID, kind string, secret map[string]any) string {
		t.Helper()
		c, err := csvc.Create(org, credential.CreateRequest{OrganizationID: f.OrgA, ClientID: clientID, Name: name, Kind: kind, Secret: secret})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	// orgCred is organization-wide, scopeCred and c2Cred belong to client 1
	// and client 2.
	orgCred := mk("audit-org-snmp", "", credential.KindSNMPv2c, map[string]any{"community": "org-secret"})
	scopeCred := mk("audit-scope-ssh", f.Client1, credential.KindSSHPassword, map[string]any{"username": "scope", "password": "scope-secret"})
	c2Cred := mk("audit-c2-ssh", f.Client2, credential.KindSSHPassword, map[string]any{"username": "c2", "password": "c2-secret"})
	secrets := []string{"org-secret", "scope-secret", "c2-secret"}
	off := false
	cases := []struct {
		name   string
		scopes []bundleScope
		want   []string
		reason string
	}{
		{name: "no scopes", want: []string{},
			reason: "WP-147: a collector without enabled scopes gets no credentials"},
		{name: "only a disabled scope", scopes: []bundleScope{{in: discovery.ScopeInput{Kind: "cidr", Target: "10.231.1.0/24", ClientID: f.Client1,
			CredentialIDs: []string{scopeCred}, Enabled: &off}}}, want: []string{},
			reason: "WP-147: a collector without enabled scopes gets no credentials"},
		{name: "one enabled scope without credential_ids (UI form / accepted suggestion)", scopes: []bundleScope{{in: discovery.ScopeInput{Kind: "cidr",
			Target: "10.231.2.0/24"}, channel: []string{orgCred}}}, want: []string{orgCred},
			reason: "WP-153: a credential-less scope keeps the collector's client/org credentials"},
		{name: "one enabled scope with explicit credential_ids (control)", scopes: []bundleScope{{in: discovery.ScopeInput{Kind: "cidr",
			Target: "10.231.3.0/24", ClientID: f.Client1, CredentialIDs: []string{scopeCred}}, channel: []string{scopeCred}}}, want: []string{scopeCred},
			reason: "DIS-09: explicit credential_ids select exactly those"},
		{name: "scope of client 1 without credential_ids", scopes: []bundleScope{{in: discovery.ScopeInput{Kind: "cidr", Target: "10.231.4.0/24",
			ClientID: f.Client1}, channel: []string{orgCred, scopeCred}}}, want: []string{orgCred, scopeCred},
			reason: "E-67: a credential-less scope gets the organization-wide credentials and those of its client, none of client 2"},
		{name: "scope of client 1 bound to a credential of client 2 before F-264", scopes: []bundleScope{{in: discovery.ScopeInput{Kind: "cidr",
			Target: "10.231.5.0/24", ClientID: f.Client1, CredentialIDs: []string{c2Cred, scopeCred}}, raw: true, channel: []string{scopeCred}}},
			want: []string{scopeCred}, reason: "F-264: an explicit credential of another client never reaches the scope's collector"},
		{name: "scopes of two clients and a disabled scope", scopes: []bundleScope{
			{in: discovery.ScopeInput{Kind: "cidr", Target: "10.231.6.0/24", ClientID: f.Client1, CredentialIDs: []string{scopeCred}}, channel: []string{scopeCred}},
			{in: discovery.ScopeInput{Kind: "host", Target: "10.231.7.1", ClientID: f.Client2}, channel: []string{orgCred, c2Cred}},
			{in: discovery.ScopeInput{Kind: "host", Target: "10.231.7.2", Enabled: &off}},
		}, want: []string{orgCred, scopeCred, c2Cred},
			reason: "DIS-09: the bundle is the union of the effective credentials of the enabled scopes"},
	}
	sorted := func(ids []string) []string {
		out := slices.Clone(ids)
		slices.Sort(out)
		return out
	}
	var collectors []bundleCollector
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			colID, key, issued := auditEnroll(t, e, fmt.Sprintf("audit-bundle-%d", i))
			collectors = append(collectors, bundleCollector{id: colID, key: key, issued: issued, want: tc.want})
			channel := map[string][]string{}
			for _, sc := range tc.scopes {
				in := sc.in
				in.CollectorID = colID
				if sc.raw {
					if _, err := f.Admin.Exec(bg, `INSERT INTO discovery_scope (organization_id, client_id, collector_id, name, kind, target, credential_ids)
						VALUES ($1, $2, $3, $4, $5, $4, $6::uuid[])`, f.OrgA, in.ClientID, colID, in.Target, in.Kind, in.CredentialIDs); err != nil {
						t.Fatalf("insert scope: %v", err)
					}
				} else if err := e.repo.CreateScope(org, discovery.NewScope(f.OrgA, &in)); err != nil {
					t.Fatalf("create scope: %v", err)
				}
				if sc.in.Enabled == nil || *sc.in.Enabled {
					channel[in.Target] = sc.channel
				}
			}
			tctx, target, verr := gate.VerifyBundleTarget(bg, f.OrgA, colID, issued.CertificatePEM)
			if verr != nil {
				t.Fatalf("bundle target: %v", verr)
			}
			if target.CredentialIDs == nil {
				t.Errorf("%s: the bundle target has no credential list (nil), want %v", tc.reason, tc.want)
			}
			_, ids, berr := csvc.Bundle(tctx, target)
			if berr != nil {
				t.Fatalf("bundle: %v", berr)
			}
			if got, want := sorted(ids), sorted(tc.want); !slices.Equal(got, want) || !slices.Equal(sorted(target.CredentialIDs), want) {
				t.Errorf("%s: bundle delivered %v (target %v), want %v (org credential %s, client 1 credential %s, client 2 credential %s)",
					tc.reason, got, target.CredentialIDs, want, orgCred, scopeCred, c2Cred)
			}

			// The scope channel: each enabled scope with its effective
			// credential_ids, their union the bundle.
			scopes, serr := h.CollectorScopes(org, f.OrgA, colID)
			if serr != nil {
				t.Fatalf("collector scopes: %v", serr)
			}
			var union []string
			for _, s := range scopes {
				want, ok := channel[s.Target]
				if !ok {
					t.Errorf("scope channel delivers scope %s, which is not an enabled scope of the case", s.Target)
					continue
				}
				if s.CredentialIDs == nil || !slices.Equal(sorted(s.CredentialIDs), sorted(want)) {
					t.Errorf("scope channel: scope %s carries credential_ids %#v, want %v", s.Target, s.CredentialIDs, want)
				}
				for _, id := range s.CredentialIDs {
					if !slices.Contains(union, id) {
						union = append(union, id)
					}
				}
			}
			if len(scopes) != len(channel) {
				t.Errorf("scope channel delivers %d scopes, want %d", len(scopes), len(channel))
			}
			if !slices.Equal(sorted(union), sorted(tc.want)) {
				t.Errorf("the scope channel's credentials %v differ from the bundle %v", union, tc.want)
			}
		})
	}

	t.Run("delivery over the collector endpoint is audited with the delivered ids", func(t *testing.T) {
		s := startRenewStack(t, e, enc)
		ctx, cancel := context.WithCancel(bg)
		defer cancel()
		go func() { _ = csvc.ServeBundles(ctx, s.core, gate, gate) }()
		for _, c := range collectors {
			nc := s.connect(t, c.id, clientIdentity(t, c.key, c.issued))
			resp := askBundle(t, nc, discovery.CollectorSubject(f.OrgA, c.id)+credential.BundleRequestSuffix, c.issued.CertificatePEM, secrets)
			if resp.Bundle == nil {
				t.Fatalf("collector %s: no bundle (%q)", c.id, resp.Error)
			}
			content, err := credential.OpenBundle(resp.Bundle, c.key)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, cred := range content.Credentials {
				got = append(got, cred.ID)
			}
			if !slices.Equal(sorted(got), sorted(c.want)) {
				t.Errorf("collector %s received %v, want %v", c.id, got, c.want)
			}
			rows, err := f.Admin.Query(bg, `SELECT changes->'credential_ids' FROM audit_log WHERE organization_id = $1
				AND action = 'credential.bundle_delivered' AND resource_id = $2`, f.OrgA, c.id)
			if err != nil {
				t.Fatal(err)
			}
			var audited [][]string
			for rows.Next() {
				var raw []byte
				if err = rows.Scan(&raw); err != nil {
					t.Fatal(err)
				}
				var ids []string
				if err = json.Unmarshal(raw, &ids); err != nil || ids == nil {
					t.Fatalf("audited credential_ids %s: %v", raw, err)
				}
				audited = append(audited, ids)
			}
			rows.Close()
			if len(audited) != 1 || !slices.Equal(sorted(audited[0]), sorted(c.want)) {
				t.Errorf("collector %s: audit credential.bundle_delivered %v, want one entry naming %v", c.id, audited, c.want)
			}
		}
	})
}

// askBundle requests the collector's bundle over nc; it waits while the
// request finds no responder yet (leaf node and subscription starting). The
// reply must not carry a secret in plaintext.
func askBundle(t *testing.T, nc *nats.Conn, subject, certPEM string, secrets []string) credential.BundleResponse {
	t.Helper()
	data, err := json.Marshal(credential.BundleRequest{CertificatePEM: certPEM})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		msg, rerr := nc.Request(subject, data, 5*time.Second)
		if errors.Is(rerr, nats.ErrNoResponders) && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		if rerr != nil {
			t.Fatalf("bundle request on %s: %v", subject, rerr)
		}
		for _, secret := range secrets {
			if strings.Contains(string(msg.Data), secret) {
				t.Fatalf("the bundle reply carries %q in plaintext", secret)
			}
		}
		var resp credential.BundleResponse
		if err = json.Unmarshal(msg.Data, &resp); err != nil {
			t.Fatalf("bundle answer %s: %v", msg.Data, err)
		}
		return resp
	}
}

// TestBundleGateDeliversClientTargets covers CRD-04 AK2 and AK3 (WP-891)
// over the real path: BundleGate.VerifyBundleTarget resolves the targets
// with the collector's client (id.ClientID) in its transaction, the bundle
// carries them, and the audit credential.bundle_delivered names their
// credentials. The collector of client 2 receives the CI binding of its CI
// (cred2) in its targets and credential ids and never the one of client 1
// (cred1); an organization-wide collector receives both, each for the CI
// and scope of its client.
func TestBundleGateDeliversClientTargets(t *testing.T) {
	e, enc := auditEdge(t, "0f")
	f := e.f
	bg := context.Background()
	org := f.OrgCtx(f.OrgA)
	gate := discovery.NewBundleGate(f.App, e.ca)
	csvc := credential.NewService(credential.NewPGRepository(f.App), enc)
	mk := func(name, clientID string) string {
		t.Helper()
		c, err := csvc.Create(org, credential.CreateRequest{OrganizationID: f.OrgA, ClientID: clientID, Name: name,
			Kind: credential.KindSNMPv2c, Secret: map[string]any{"community": name + "-secret"}})
		if err != nil {
			t.Fatal(err)
		}
		return c.ID
	}
	cred1, cred2 := mk("wp891-gate-c1", f.Client1), mk("wp891-gate-c2", f.Client2)
	ci1 := f.CI(t, f.OrgA, f.Client1, "wp891-gate-ci1")
	ci2 := f.CI(t, f.OrgA, f.Client2, "wp891-gate-ci2")
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.Admin.Exec(bg, sql, args...); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	exec(`UPDATE ci SET management_ip = '10.94.1.10' WHERE id = $1`, ci1)
	exec(`UPDATE ci SET management_ip = '10.94.2.10' WHERE id = $1`, ci2)
	for ci, cred := range map[string]string{ci1: cred1, ci2: cred2} {
		exec(`INSERT INTO credential_binding (organization_id, credential_id, target_kind, ci_id, namespace, priority)
			VALUES ($1, $2, 'ci', $3, 'snmp', 100)`, f.OrgA, cred, ci)
	}
	scope := func(colID, clientID, cidr string) string {
		t.Helper()
		var client any
		if clientID != "" {
			client = clientID
		}
		var id string
		if err := f.Admin.QueryRow(bg, `INSERT INTO discovery_scope (organization_id, client_id, collector_id, name, kind, target, credential_ids)
			VALUES ($1, $2, $3, $4, 'cidr', $4, '{}') RETURNING id::text`, f.OrgA, client, colID, cidr).Scan(&id); err != nil {
			t.Fatalf("insert scope: %v", err)
		}
		return id
	}

	c2Col, _, c2Cert := auditEnroll(t, e, "wp891-gate-c2")
	exec(`UPDATE collector SET client_id = $2 WHERE id = $1`, c2Col, f.Client2)
	c2Scope := scope(c2Col, f.Client2, "10.94.0.0/16")
	orgCol, _, orgCert := auditEnroll(t, e, "wp891-gate-org")
	org1Scope := scope(orgCol, f.Client1, "10.94.1.0/24")
	org2Scope := scope(orgCol, f.Client2, "10.94.2.0/24")

	deliver := func(colID, certPEM string) (*credential.BundleTarget, []string) {
		t.Helper()
		tctx, target, err := gate.VerifyBundleTarget(bg, f.OrgA, colID, certPEM)
		if err != nil {
			t.Fatalf("bundle target: %v", err)
		}
		_, ids, err := csvc.Bundle(tctx, target)
		if err != nil {
			t.Fatalf("bundle: %v", err)
		}
		if err = gate.BundleDelivered(bg, target, ids); err != nil {
			t.Fatalf("bundle delivered: %v", err)
		}
		var raw []byte
		if err = f.Admin.QueryRow(bg, `SELECT changes->'credential_ids' FROM audit_log WHERE organization_id = $1
			AND action = 'credential.bundle_delivered' AND resource_id = $2 ORDER BY created_at DESC LIMIT 1`, f.OrgA, colID).Scan(&raw); err != nil {
			t.Fatalf("audit credential.bundle_delivered: %v", err)
		}
		var audited []string
		if err = json.Unmarshal(raw, &audited); err != nil {
			t.Fatal(err)
		}
		return target, audited
	}

	target, audited := deliver(c2Col, c2Cert.CertificatePEM)
	if target.ClientID != f.Client2 {
		t.Fatalf("bundle target of the client 2 collector has client %q, want %s", target.ClientID, f.Client2)
	}
	if !slices.Contains(target.CredentialIDs, cred2) || slices.Contains(target.CredentialIDs, cred1) {
		t.Errorf("credential ids of the client 2 collector %v: want cred2, never cred1", target.CredentialIDs)
	}
	if len(target.Targets) != 1 || target.Targets[0].CIID != ci2 || target.Targets[0].CredentialID != cred2 || target.Targets[0].ScopeID != c2Scope {
		t.Errorf("targets of the client 2 collector %+v, want only ci2 with cred2 in scope %s", target.Targets, c2Scope)
	}
	for _, x := range target.Targets {
		if x.CredentialID == cred1 || x.CIID == ci1 {
			t.Fatalf("the collector of client 2 receives %+v of client 1", x)
		}
	}
	if !slices.Contains(audited, cred2) || slices.Contains(audited, cred1) {
		t.Errorf("audit credential.bundle_delivered of the client 2 collector names %v, want cred2 (target), never cred1", audited)
	}

	target, audited = deliver(orgCol, orgCert.CertificatePEM)
	want := map[string]credential.BundleTargetCredential{
		ci1: {CIID: ci1, ScopeID: org1Scope, ManagementIP: "10.94.1.10", Namespace: "snmp", CredentialID: cred1, Origin: credential.OriginCI},
		ci2: {CIID: ci2, ScopeID: org2Scope, ManagementIP: "10.94.2.10", Namespace: "snmp", CredentialID: cred2, Origin: credential.OriginCI},
	}
	if len(target.Targets) != 2 || want[target.Targets[0].CIID] != target.Targets[0] || want[target.Targets[1].CIID] != target.Targets[1] {
		t.Errorf("targets of the org-wide collector %+v, want %+v", target.Targets, want)
	}
	for _, id := range []string{cred1, cred2} {
		if !slices.Contains(audited, id) || !slices.Contains(target.CredentialIDs, id) {
			t.Errorf("org-wide collector: credential ids %v, audited %v; want both target credentials", target.CredentialIDs, audited)
		}
	}
}
