package credential_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/credential"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
)

// The device-group level of the credential resolution on PostgreSQL
// (CRD-04, WP-935 AK8): credential.PGDeviceGroupSource reads the
// memberships of monitoring_group_member.

// resolveGroups runs ResolveCollector with the membership source of
// monitoring_group_member in the organization-wide system scope.
func (e *bindingEnv) resolveGroups(collectorID string) map[string]credential.BundleTargetCredential {
	e.t.Helper()
	out := map[string]credential.BundleTargetCredential{}
	scope := database.OrgWideScope(e.f.OrgA, "")
	if err := database.WithTenant(context.Background(), e.f.App, &scope, func(ctx context.Context, tx pgx.Tx) error {
		targets, err := credential.ResolveCollector(ctx, tx, e.f.OrgA, collectorID, "", credential.NewPGDeviceGroupSource(nil))
		for _, x := range targets {
			out[x.CIID+"|"+x.Namespace] = x
		}
		return err
	}); err != nil {
		e.t.Fatalf("resolve collector: %v", err)
	}
	return out
}

// effectiveSNMP returns the snmp entry of GET /cis/{id}/credentials/effective.
func (e *bindingEnv) effectiveSNMP(ciID string) credential.EffectiveCredential {
	e.t.Helper()
	rec := e.do(http.MethodGet, "/api/v1/cis/"+ciID+"/credentials/effective", "", "")
	if rec.Code != http.StatusOK {
		e.t.Fatalf("effective: %d %s", rec.Code, rec.Body.String())
	}
	var view struct {
		Data []credential.EffectiveCredential `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		e.t.Fatal(err)
	}
	for _, x := range view.Data {
		if x.Namespace == "snmp" {
			return x
		}
	}
	return credential.EffectiveCredential{}
}

// TestResolveDeviceGroupLevel covers CRD-04 (WP-935 AK8): a binding to a
// monitoring group acts on its members (origin device_group, not on other
// CIs); the group wins over the subnet, the CI binding over the group.
func TestResolveDeviceGroupLevel(t *testing.T) {
	e := newBindingEnv(t)
	credGroup := e.createCredential(`{"name":"wp935-group","kind":"snmp_v2c","client_id":"` + e.f.Client1 + `","secret":{"community":"g"}}`)
	credSubnet := e.createCredential(`{"name":"wp935-subnet","kind":"snmp_v2c","secret":{"community":"s"}}`)
	e.setIP(e.ci1, "10.89.1.30")
	e.setIP(e.ci2, "10.89.2.30")
	group := e.group(e.f.OrgA, e.f.Client1, "wp935-resolve", 0)
	e.member(e.f.OrgA, group, e.ci1)
	groupBinding := e.mustBind(credGroup, "device_group", group, 100)
	e.mustBind(credSubnet, "subnet", e.subnet1, 100)
	e.mustBind(credSubnet, "subnet", e.subnet2, 100)
	collector := e.collector("wp935-resolve", "", "10.89.0.0/16")

	targets := e.resolveGroups(collector)
	if got := targets[e.ci1+"|snmp"]; got.CredentialID != credGroup || got.Origin != credential.OriginDeviceGroup {
		t.Errorf("member CI: %+v, want the group credential from origin device_group", got)
	}
	if got := targets[e.ci2+"|snmp"]; got.CredentialID != credSubnet || got.Origin != credential.OriginSubnet {
		t.Errorf("CI outside the group: %+v, want the subnet credential", got)
	}
	view := e.effectiveSNMP(e.ci1)
	if view.Credential.ID != credGroup || view.Origin != credential.OriginDeviceGroup || view.SourceID != groupBinding {
		t.Errorf("effective view: %+v, want the group binding %s", view.ResolvedCredential, groupBinding)
	}
	if len(view.Overridden) == 0 || view.Overridden[0].Origin != credential.OriginSubnet {
		t.Errorf("overridden %+v, want the subnet level after the group", view.Overridden)
	}

	// The CI binding wins over the group.
	ciBinding := e.mustBind(e.cred1, "ci", e.ci1, 100)
	targets = e.resolveGroups(collector)
	if got := targets[e.ci1+"|snmp"]; got.CredentialID != e.cred1 || got.Origin != credential.OriginCI {
		t.Errorf("with a CI binding: %+v, want the CI credential", got)
	}
	view = e.effectiveSNMP(e.ci1)
	if view.SourceID != ciBinding || len(view.Overridden) < 2 || view.Overridden[0].Origin != credential.OriginDeviceGroup {
		t.Errorf("effective view with a CI binding: %+v", view)
	}

	// Leaving the group removes the level.
	e.exec(`DELETE FROM monitoring_group_member WHERE group_id = $1`, group)
	if got := e.resolveGroups(collector)[e.ci1+"|snmp"]; got.Origin != credential.OriginCI {
		t.Errorf("after leaving the group: %+v", got)
	}
}

// TestDeviceGroupPriority covers WP-935 AK8 with the rule of WP-937: of two
// groups of a CI the one with the higher priority wins, on a tie the
// smaller name; the binding priority decides only within a group. The
// result does not depend on the read order, the memory resolution agrees.
func TestDeviceGroupPriority(t *testing.T) {
	e := newBindingEnv(t)
	credHigh := e.createCredential(`{"name":"wp935-high","kind":"snmp_v2c","secret":{"community":"h"}}`)
	credLow := e.createCredential(`{"name":"wp935-low","kind":"snmp_v2c","secret":{"community":"l"}}`)
	e.setIP(e.ci1, "10.89.1.40")
	high := e.group(e.f.OrgA, "", "wp935-zz-high", 10)
	low := e.group(e.f.OrgA, "", "wp935-aa-low", 5)
	e.member(e.f.OrgA, low, e.ci1)
	e.member(e.f.OrgA, high, e.ci1)
	// The binding of the higher group has the worse binding priority.
	e.mustBind(credHigh, "device_group", high, 900)
	e.mustBind(credLow, "device_group", low, 1)
	collector := e.collector("wp935-priority", "", "10.89.0.0/16")

	if got := e.resolveGroups(collector)[e.ci1+"|snmp"]; got.CredentialID != credHigh {
		t.Errorf("priority 10 against 5: %+v, want the credential of the higher group", got)
	}
	if view := e.effectiveSNMP(e.ci1); view.Credential.ID != credHigh || len(view.Overridden) == 0 || view.Overridden[0].Credential.ID != credLow {
		t.Errorf("effective view: %+v", view)
	}
	// Tie: the smaller name wins.
	e.exec(`UPDATE monitoring_group SET priority = 5 WHERE id = $1`, high)
	if got := e.resolveGroups(collector)[e.ci1+"|snmp"]; got.CredentialID != credLow {
		t.Errorf("tie: %+v, want the group with the smaller name", got)
	}

	// The memberships in rank order, and the memory resolution with them.
	src := credential.NewPGDeviceGroupSource(e.f.App)
	ctx := e.f.OrgCtx(e.f.OrgA)
	members, err := src.Memberships(ctx, e.f.OrgA, []string{e.ci1, e.ci2, "not-a-uuid"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(members[e.ci1], []string{low, high}) || len(members[e.ci2]) != 0 {
		t.Fatalf("memberships %v, want [low high] for ci1", members)
	}
	data := &credential.ResolveData{OrganizationID: e.f.OrgA,
		Credentials: []credential.Credential{
			{ID: credHigh, Name: "h", Kind: credential.KindSNMPv2c}, {ID: credLow, Name: "l", Kind: credential.KindSNMPv2c},
		},
		Bindings: []credential.Binding{
			{ID: "b-high", CredentialID: credHigh, TargetKind: credential.TargetDeviceGroup, TargetID: high, Namespace: "snmp", Priority: 900},
			{ID: "b-low", CredentialID: credLow, TargetKind: credential.TargetDeviceGroup, TargetID: low, Namespace: "snmp", Priority: 1},
		}}
	res, err := credential.Resolve(ctx, data, src, []credential.ResolveCI{{ID: e.ci1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res[e.ci1]) == 0 || res[e.ci1][0].Credential.ID != credLow {
		t.Errorf("memory resolution on a tie: %+v, want the group with the smaller name", res[e.ci1])
	}
	// A principal of client 2 sees no membership of the org-wide group of
	// client 1's CI.
	if m, err := src.Memberships(e.f.ClientCtx(e.f.Client2), e.f.OrgA, []string{e.ci1}); err != nil || len(m[e.ci1]) != 0 {
		t.Errorf("memberships for client 2: %v, %v", m, err)
	}
}

// TestDeviceGroupSourceStatementBudget covers WP-935 AK8 with WP-891 AK5:
// the device-group level adds no statement – the resolution of 1,000 member
// CIs needs as many statements as that of a handful, and exactly one reads
// monitoring_group_member and credential_binding.
func TestDeviceGroupSourceStatementBudget(t *testing.T) {
	e := newBindingEnv(t)
	credGroup := e.createCredential(`{"name":"wp935-budget","kind":"snmp_v2c","secret":{"community":"b"}}`)
	typeID := e.scalar(`SELECT id::text FROM ci_type WHERE key = 'server' AND organization_id IS NULL LIMIT 1`)
	e.exec(`INSERT INTO ci (organization_id, ci_type_id, name, status, management_ip)
		SELECT $1, $2, 'wp935-budget-' || n, 'active', ('10.93.0.0'::inet + n)
		FROM generate_series(1, 1000) AS n`, e.f.OrgA, typeID)
	groupA := e.group(e.f.OrgA, "", "wp935-budget-a", 1)
	groupB := e.group(e.f.OrgA, "", "wp935-budget-b", 2)
	for _, g := range []string{groupA, groupB} {
		e.exec(`INSERT INTO monitoring_group_member (organization_id, group_id, ci_id)
			SELECT $1, $2, c.id FROM ci c WHERE c.organization_id = $1 AND c.name LIKE 'wp935-budget-%'`, e.f.OrgA, g)
		e.mustBind(credGroup, "device_group", g, 100)
	}
	small := e.collector("wp935-budget-small", "", "10.93.0.0/29")
	large := e.collector("wp935-budget-large", "", "10.93.0.0/22")

	trace := scopetest.NewQueryTrace(t, e.f.App)
	run := func(collectorID string) ([]scopetest.LoggedStatement, []credential.BundleTargetCredential) {
		t.Helper()
		var targets []credential.BundleTargetCredential
		stmts := trace.Run(t, func() error {
			scope := database.OrgWideScope(e.f.OrgA, "")
			return database.WithTenant(context.Background(), trace.Pool, &scope, func(ctx context.Context, tx pgx.Tx) error {
				var err error
				targets, err = credential.ResolveCollector(ctx, tx, e.f.OrgA, collectorID, "", credential.NewPGDeviceGroupSource(nil))
				return err
			})
		})
		return stmts, targets
	}
	run(small) // warms the connection
	smallStmts, smallTargets := run(small)
	largeStmts, largeTargets := run(large)
	if len(smallTargets) != 7 || len(largeTargets) != 1000 {
		t.Fatalf("targets: %d for the /29, %d for the /22; want 7 and 1000", len(smallTargets), len(largeTargets))
	}
	for _, x := range largeTargets {
		if x.Origin != credential.OriginDeviceGroup {
			t.Fatalf("target %+v, want origin device_group", x)
		}
	}
	if len(largeStmts) != len(smallStmts) {
		t.Errorf("statements: %d for 1000 CIs, %d for 7 CIs; want the same", len(largeStmts), len(smallStmts))
	}
	members, bindings := 0, 0
	for _, s := range largeStmts {
		if strings.Contains(s.SQL, "monitoring_group_member") {
			members++
		}
		if strings.Contains(s.SQL, "credential_binding") {
			bindings++
		}
	}
	if members != 1 || bindings != 1 {
		t.Errorf("%d statements read monitoring_group_member, %d credential_binding; want one each", members, bindings)
	}
}
