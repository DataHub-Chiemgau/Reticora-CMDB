package main

// The operator command of the collector CA (COL-04, WP-430):
//
//	server collector-ca status
//	server collector-ca rollover
//	server collector-ca retire [--force]
//
// It runs with the configuration of the server (RETICORA_DATABASE_URL,
// RETICORA_MASTER_KEY, RETICORA_PKI_DIR, RETICORA_NATS_EDGE_URL,
// RETICORA_NATS_EDGE_HOSTS, RETICORA_NATS_URL), so in the server container,
// for example docker compose exec server /app/server collector-ca status
// (docs/betrieb/collector-ca.md). Roll-over and retirement write the files of
// the collector NATS endpoint and have it reload them before they commit,
// are recorded in operator_audit (collector_ca.rolled_over,
// collector_ca.retired) and announced on the internal NATS.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/config"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/database"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/discovery"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/operator"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/crypto"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/httpx"
)

// collectorCACommand is the first argument of the command.
const collectorCACommand = "collector-ca"

// Audit actions of the command (operator_audit).
const (
	auditCARolledOver = "collector_ca.rolled_over"
	auditCARetired    = "collector_ca.retired"
)

const collectorCAUsage = `usage: server collector-ca status | rollover | retire [--force]

  status    the active and the previous CAs, the issuer of the edge server
            certificate and the number of active collectors that still hold
            a certificate of a previous CA
  rollover  a new CA signs from now on; the former one stays a trust anchor
            and issues the edge server certificate until retire
  retire    the previous CAs are no longer trusted and the edge server
            certificate is issued by the active CA; refused while active
            collectors hold a certificate of a previous CA, --force
            overrides that (audited with their number)
`

// runCollectorCA runs the command with args (after collector-ca) and returns
// its exit code: 0 done, 1 failed or refused, 2 wrong usage.
func runCollectorCA(ctx context.Context, args []string, cfg *config.Config, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, collectorCAUsage)
		return 2
	}
	cmd := args[0]
	fs := flag.NewFlagSet(collectorCACommand+" "+cmd, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	force := false
	switch cmd {
	case "status", "rollover":
	case "retire":
		fs.BoolVar(&force, "force", false, "")
	default:
		fmt.Fprintf(stderr, "unknown command %q\n%s", cmd, collectorCAUsage)
		return 2
	}
	if err := fs.Parse(args[1:]); err != nil || fs.NArg() != 0 {
		fmt.Fprintf(stderr, "invalid arguments for %s: %q\n%s", cmd, args[1:], collectorCAUsage)
		return 2
	}
	slog.SetDefault(slog.New(httpx.NewRedactingHandler(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))))
	fail := func(err error) int {
		fmt.Fprintf(stderr, "collector-ca %s: %v\n", cmd, err)
		return 1
	}
	if cfg.MasterKey == "" {
		return fail(errors.New("RETICORA_MASTER_KEY is required: the CA keys are encrypted under it"))
	}
	enc, err := crypto.NewEnvelopeEncryptor(cfg.MasterKey, strings.Split(cfg.MasterKeyPrevious, ",")...)
	if err != nil {
		return fail(err)
	}
	pool, err := database.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return fail(fmt.Errorf("connect to %s: %w", config.MaskDSN(cfg.DatabaseURL), err))
	}
	defer pool.Close()
	ca := discovery.NewCA(pool, enc)

	if cmd == "status" {
		st, serr := ca.Status(ctx)
		if serr != nil {
			return fail(serr)
		}
		printCAStatus(stdout, st)
		return 0
	}
	edge, err := collectorCAEdge(cfg, enc)
	if err != nil {
		return fail(err)
	}
	if edge == nil {
		fmt.Fprintln(stderr, "warning: no collector NATS endpoint configured here (RETICORA_PKI_DIR, RETICORA_NATS_EDGE_URL); "+
			"the edge workers write its files and have it reload them on the announcement")
	}
	opts := discovery.CAChangeOptions{Force: force, Edge: edge}
	var ch *discovery.CAChange
	if cmd == "rollover" {
		opts.Audit = collectorCAAudit(auditCARolledOver, false)
		ch, err = ca.Rollover(ctx, opts)
	} else {
		opts.Audit = collectorCAAudit(auditCARetired, force)
		ch, err = ca.RetirePrevious(ctx, opts)
	}
	var inUse *discovery.PreviousCAInUseError
	if errors.As(err, &inUse) {
		fmt.Fprintf(stderr, "collector-ca retire refused: %v\n"+
			"wait until they renewed (server collector-ca status) or override with --force: they can no longer connect afterwards\n", inUse)
		return 1
	}
	if err != nil {
		return fail(err)
	}
	printCAChange(stdout, cmd, ch, edge)
	announceCAChange(cfg, ca, ch, stdout, stderr)
	return 0
}

// collectorCAEdge is the collector NATS endpoint of the configuration: nil
// when neither RETICORA_PKI_DIR nor RETICORA_NATS_EDGE_URL is set, an error
// when only one of them is.
func collectorCAEdge(cfg *config.Config, enc *crypto.EnvelopeEncryptor) (*discovery.EdgeConfig, error) {
	if cfg.PKIDir == "" && cfg.NATSEdgeURL == "" {
		return nil, nil
	}
	if cfg.PKIDir == "" || cfg.NATSEdgeURL == "" {
		return nil, errors.New("RETICORA_PKI_DIR and RETICORA_NATS_EDGE_URL must both be set: the command writes the files " +
			"of the collector NATS endpoint and has it reload them (run it in the server container)")
	}
	secrets, err := discovery.DeriveEdgeSecrets(enc)
	if err != nil {
		return nil, err
	}
	return &discovery.EdgeConfig{PKIDir: cfg.PKIDir, Hosts: splitList(cfg.NATSEdgeHosts), URL: cfg.NATSEdgeURL, Secrets: secrets}, nil
}

// collectorCAAudit records a change in operator_audit within its
// transaction. The command has no personal operator identity: it runs with
// the server's credentials in its container, like the break-glass token
// outside the IdP, and is recorded with that kind.
func collectorCAAudit(action string, force bool) func(ctx context.Context, tx pgx.Tx, ch *discovery.CAChange) error {
	return func(ctx context.Context, tx pgx.Tx, ch *discovery.CAChange) error {
		details := map[string]any{
			"active":        ch.Active.Fingerprint,
			"previous":      caFingerprints(ch.Previous),
			"edge_issuer":   ch.EdgeIssuer.Fingerprint,
			"edge_reloaded": ch.EdgeReloaded,
		}
		if action == auditCARetired {
			details["retired"] = caFingerprints(ch.Retired)
			details["force"] = force
			details["overridden_collectors"] = ch.Overridden
		}
		return operator.RecordTx(ctx, tx, &operator.AuditEntry{OperatorID: collectorCAOperator(), OperatorKind: operator.KindBreakGlass,
			Action: action, Status: 200, Details: details})
	}
}

// collectorCAOperator names the command and its host in operator_audit.
func collectorCAOperator() string {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown"
	}
	return "server " + collectorCACommand + "@" + host
}

func caFingerprints(cas []discovery.CAInfo) []string {
	out := make([]string, 0, len(cas))
	for _, c := range cas {
		out = append(out, c.Fingerprint)
	}
	return out
}

// announceCAChange announces the change on the internal NATS; without it
// the replicas read the change at their next signature, export or callout.
func announceCAChange(cfg *config.Config, ca *discovery.CA, ch *discovery.CAChange, stdout, stderr io.Writer) {
	if !cfg.IsSet("RETICORA_NATS_URL") {
		fmt.Fprintln(stderr, "warning: RETICORA_NATS_URL is not set: the change was not announced; "+
			"the replicas read it at their next signature, export or callout")
		return
	}
	nc, err := nats.Connect(cfg.NATSUrl, nats.Name("reticora-collector-ca"), nats.Timeout(5*time.Second), nats.NoReconnect())
	if err == nil {
		err = ca.AnnounceCAChange(nc, ch)
		nc.Close()
	}
	if err != nil {
		fmt.Fprintf(stderr, "warning: the announcement on the internal NATS failed (%v); "+
			"the replicas read the change at their next signature, export or callout\n", err)
		return
	}
	fmt.Fprintln(stdout, "announced on the internal NATS: every replica reloads the CA state")
}

func printCA(w io.Writer, label string, c discovery.CAInfo) {
	fmt.Fprintf(w, "%-16s %s  sha256:%s  created %s  expires %s\n", label, c.Subject, c.Fingerprint,
		c.CreatedAt.Format(time.RFC3339), c.NotAfter.Format(time.RFC3339))
}

func printCAStatus(w io.Writer, st *discovery.CAStatus) {
	printCA(w, "active CA", st.Active)
	for _, p := range st.Previous {
		printCA(w, "previous CA", p)
	}
	fmt.Fprintf(w, "edge server certificate issued by %s (sha256:%s)\n", st.EdgeIssuer.Subject, st.EdgeIssuer.Fingerprint)
	if len(st.Previous) == 0 {
		fmt.Fprintln(w, "no roll-over in progress")
		return
	}
	fmt.Fprintf(w, "active collectors with a certificate of a previous CA: %d\n", st.Collectors)
	if st.Collectors == 0 {
		fmt.Fprintln(w, "retire: possible")
	} else {
		fmt.Fprintln(w, "retire: refused until they renewed (or --force)")
	}
}

func printCAChange(w io.Writer, cmd string, ch *discovery.CAChange, edge *discovery.EdgeConfig) {
	if cmd == "rollover" {
		fmt.Fprintln(w, "rolled over: the new CA signs from now on")
	} else {
		fmt.Fprintf(w, "retired %d previous CA(s)", len(ch.Retired))
		if ch.Overridden > 0 {
			fmt.Fprintf(w, " (forced: %d active collectors with a certificate of a previous CA can no longer connect)", ch.Overridden)
		}
		fmt.Fprintln(w)
	}
	printCA(w, "active CA", ch.Active)
	for _, p := range ch.Previous {
		printCA(w, "previous CA", p)
	}
	for _, r := range ch.Retired {
		printCA(w, "retired CA", r)
	}
	fmt.Fprintf(w, "edge server certificate issued by %s (sha256:%s)\n", ch.EdgeIssuer.Subject, ch.EdgeIssuer.Fingerprint)
	if edge != nil && ch.EdgeReloaded {
		fmt.Fprintf(w, "collector NATS endpoint: files written to %s and reloaded\n", edge.PKIDir)
	}
}
