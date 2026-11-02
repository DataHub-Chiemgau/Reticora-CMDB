package main

// Tests of the command server collector-ca (WP-430, COL-04, F-102). The
// command with the collector NATS endpoint (export and reload of nats-edge)
// runs as a process of its own in
// internal/discovery/natsedge_integration_test.go
// (TestCollectorCARolloverAcrossReplicas).

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database/scopetest"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
)

// runCA runs the command in this process with the configuration of the
// environment and returns its exit code, standard output and error output.
func runCA(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	prev := slog.Default()
	defer slog.SetDefault(prev)
	var out, errOut bytes.Buffer
	code = runCollectorCA(context.Background(), args, config.Load(), &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestCollectorCACommandUsage: unknown commands, unknown or misplaced flags
// and extra arguments are refused with exit code 2 before anything is
// connected.
func TestCollectorCACommandUsage(t *testing.T) {
	t.Setenv("RETICORA_DATABASE_URL", "postgres://nobody@127.0.0.1:1/none?sslmode=disable")
	for _, args := range [][]string{
		nil,
		{"rotate"},
		{"rollover", "--force"},
		{"status", "--force"},
		{"retire", "--yes"},
		{"retire", "now"},
		{"status", "extra"},
	} {
		code, _, stderr := runCA(t, args...)
		if code != 2 || !strings.Contains(stderr, "usage: server collector-ca status | rollover | retire [--force]") {
			t.Errorf("collector-ca %q: exit %d, want 2 with the usage\n%s", args, code, stderr)
		}
	}
}

// TestCollectorCACommand covers server collector-ca status|rollover|retire
// [--force] against PostgreSQL, without a collector NATS endpoint configured
// (the files are left to the edge workers): status shows the CAs and the
// collectors on a previous CA; rollover makes a new CA active, keeps the
// former one as previous, is audited as collector_ca.rolled_over and
// announced on the internal NATS (with TEST_NATS_URL); retire is refused
// with the number of active collectors on the previous CA, changes nothing
// and is not audited; retire --force retires it, audited as
// collector_ca.retired with force and the number it overrode; a further
// retire finds no previous CA. Without the master key or with only one of
// RETICORA_PKI_DIR and RETICORA_NATS_EDGE_URL nothing changes.
func TestCollectorCACommand(t *testing.T) {
	f := scopetest.Seed(t, "c6")
	bg := context.Background()
	// The command opens the derivation root of the database (WP-459), which
	// goes with the master key this test draws.
	clean := func() {
		_, _ = f.Admin.Exec(bg, `DELETE FROM collector_ca`)
		_, _ = f.Admin.Exec(bg, `DELETE FROM platform_secret`)
	}
	clean()
	t.Cleanup(clean)
	masterKey, err := crypto.GenerateMasterKey()
	if err != nil {
		t.Fatal(err)
	}
	enc, err := crypto.NewEnvelopeEncryptor(masterKey)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RETICORA_DATABASE_URL", scopetest.AppDSN(t))
	t.Setenv("RETICORA_MASTER_KEY", masterKey)
	t.Setenv("RETICORA_MASTER_KEY_PREVIOUS", "")
	t.Setenv("RETICORA_PKI_DIR", "")
	t.Setenv("RETICORA_NATS_EDGE_URL", "")
	t.Setenv("RETICORA_NATS_URL", os.Getenv("TEST_NATS_URL"))

	cas := func(state string) []string {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT cert_pem FROM collector_ca WHERE state = $1 ORDER BY created_at`, state)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var p string
			if serr := rows.Scan(&p); serr != nil {
				t.Fatal(serr)
			}
			block, _ := pem.Decode([]byte(p))
			sum := sha256.Sum256(block.Bytes)
			out = append(out, hex.EncodeToString(sum[:]))
		}
		return out
	}
	type auditRow struct {
		operatorID, kind string
		status           int
		details          map[string]any
	}
	audits := func(action, active string) []auditRow {
		t.Helper()
		rows, qerr := f.Admin.Query(bg, `SELECT operator_id, operator_kind, status, details FROM operator_audit
			WHERE action = $1 AND details->>'active' = $2 ORDER BY id`, action, active)
		if qerr != nil {
			t.Fatal(qerr)
		}
		defer rows.Close()
		var out []auditRow
		for rows.Next() {
			var r auditRow
			if serr := rows.Scan(&r.operatorID, &r.kind, &r.status, &r.details); serr != nil {
				t.Fatal(serr)
			}
			out = append(out, r)
		}
		return out
	}

	// status creates the first CA on an empty table, as the server does.
	code, out, stderr := runCA(t, "status")
	first := cas("active")
	if code != 0 || len(first) != 1 || !strings.Contains(out, "sha256:"+first[0]) || !strings.Contains(out, "no roll-over in progress") {
		t.Fatalf("status: exit %d\n%s%s", code, out, stderr)
	}

	// A collector enrolled with the first CA.
	ca := discovery.NewCA(f.App, enc)
	repo := discovery.NewPGRepository(f.App)
	raw := "scopetest-c6-collector-ca"
	enrollCode := &discovery.EnrollmentCode{OrganizationID: f.OrgA, Label: raw, ExpiresAt: time.Now().Add(time.Hour)}
	enrollCode.SetRawCode(raw)
	if err = repo.CreateEnrollmentCode(f.OrgCtx(f.OrgA), enrollCode); err != nil {
		t.Fatal(err)
	}
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{}}, key)
	if err != nil {
		t.Fatal(err)
	}
	csr := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	if _, err = repo.EnrollCollector(bg, raw, &discovery.Collector{Name: "collector-ca-cli"}, func(ctx context.Context, org, id string) (*discovery.IssuedCert, error) {
		return ca.Sign(ctx, csr, org, id)
	}); err != nil {
		t.Fatal(err)
	}

	// Refused before any change: no master key; only one of the endpoint's
	// settings.
	t.Setenv("RETICORA_MASTER_KEY", "")
	if code, _, stderr = runCA(t, "rollover"); code != 1 || !strings.Contains(stderr, "RETICORA_MASTER_KEY is required") {
		t.Fatalf("rollover without the master key: exit %d\n%s", code, stderr)
	}
	t.Setenv("RETICORA_MASTER_KEY", masterKey)
	t.Setenv("RETICORA_PKI_DIR", t.TempDir())
	if code, _, stderr = runCA(t, "rollover"); code != 1 || !strings.Contains(stderr, "RETICORA_PKI_DIR and RETICORA_NATS_EDGE_URL must both be set") {
		t.Fatalf("rollover with a PKI directory but no endpoint URL: exit %d\n%s", code, stderr)
	}
	t.Setenv("RETICORA_PKI_DIR", "")
	if got := cas("active"); len(got) != 1 || got[0] != first[0] || len(cas("previous")) != 0 {
		t.Fatal("a refused command changed collector_ca")
	}

	// rollover, audited and announced.
	var announced *nats.Subscription
	if url := os.Getenv("TEST_NATS_URL"); url != "" {
		nc, cerr := nats.Connect(url)
		if cerr != nil {
			t.Fatal(cerr)
		}
		defer nc.Close()
		if announced, err = nc.SubscribeSync(discovery.CollectorCAChangedSubject); err != nil {
			t.Fatal(err)
		}
		if err = nc.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	code, out, stderr = runCA(t, "rollover")
	active := cas("active")
	if code != 0 || !strings.Contains(out, "rolled over") || len(active) != 1 || active[0] == first[0] ||
		fmt.Sprint(cas("previous")) != fmt.Sprint(first) || !strings.Contains(stderr, "no collector NATS endpoint configured") {
		t.Fatalf("rollover: exit %d, active %v, previous %v\n%s%s", code, active, cas("previous"), out, stderr)
	}
	rolled := audits(auditCARolledOver, active[0])
	if len(rolled) != 1 || rolled[0].kind != "break_glass" || rolled[0].status != 200 ||
		!strings.HasPrefix(rolled[0].operatorID, "server collector-ca@") ||
		fmt.Sprint(rolled[0].details["previous"]) != fmt.Sprint([]any{first[0]}) || rolled[0].details["edge_issuer"] != first[0] ||
		rolled[0].details["edge_reloaded"] != false {
		t.Fatalf("audit of the roll-over: %+v", rolled)
	}
	if announced != nil {
		msg, nerr := announced.NextMsg(5 * time.Second)
		var a map[string]string
		if nerr != nil || json.Unmarshal(msg.Data, &a) != nil || a["version"] == "" || a["mac"] == "" || a["at"] == "" {
			t.Fatalf("the roll-over was not announced: %v", nerr)
		}
		if !strings.Contains(out, "announced on the internal NATS") {
			t.Fatalf("rollover output without the announcement:\n%s", out)
		}
	}
	if code, out, _ = runCA(t, "status"); code != 0 || !strings.Contains(out, "previous CA") ||
		!strings.Contains(out, "active collectors with a certificate of a previous CA: 1") || !strings.Contains(out, "retire: refused") ||
		!strings.Contains(out, "edge server certificate issued by") || !strings.Contains(out, "sha256:"+first[0]+")") {
		t.Fatalf("status during the roll-over: exit %d\n%s", code, out)
	}

	// retire: refused while the collector holds a certificate of the
	// previous CA; nothing changes, nothing is audited.
	if code, _, stderr = runCA(t, "retire"); code != 1 || !strings.Contains(stderr, "retire refused: 1 active collectors") {
		t.Fatalf("retire with a collector on the previous CA: exit %d\n%s", code, stderr)
	}
	if fmt.Sprint(cas("previous")) != fmt.Sprint(first) || len(audits(auditCARetired, active[0])) != 0 {
		t.Fatal("a refused retirement changed collector_ca or was audited")
	}
	// retire --force: audited with the number of overridden collectors.
	if code, out, stderr = runCA(t, "retire", "--force"); code != 0 || !strings.Contains(out, "retired 1 previous CA(s) (forced: 1 active collectors") {
		t.Fatalf("retire --force: exit %d\n%s%s", code, out, stderr)
	}
	retired := audits(auditCARetired, active[0])
	if len(retired) != 1 || retired[0].details["force"] != true || retired[0].details["overridden_collectors"] != float64(1) ||
		fmt.Sprint(retired[0].details["retired"]) != fmt.Sprint([]any{first[0]}) || retired[0].details["edge_issuer"] != active[0] ||
		retired[0].kind != "break_glass" {
		t.Fatalf("audit of the forced retirement: %+v", retired)
	}
	if len(cas("previous")) != 0 || len(cas("retired")) != 1 {
		t.Fatal("the forced retirement left a previous CA")
	}
	if code, _, stderr = runCA(t, "retire"); code != 1 || !strings.Contains(stderr, "no previous collector CA") {
		t.Fatalf("retire without a previous CA: exit %d\n%s", code, stderr)
	}
	if code, out, _ = runCA(t, "status"); code != 0 || !strings.Contains(out, "no roll-over in progress") {
		t.Fatalf("status after the retirement: exit %d\n%s", code, out)
	}
}
