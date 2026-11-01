package collectorcmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/enrollment"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/keystore"
)

// Zero-config onboarding (INS-06): `reticora-collector enroll --server
// <url> --code <code>` redeems the one-time enrollment code, stores the
// collector's identity and the server URL in the credentials file (the one
// configuration path, RETICORA_CREDENTIALS_PATH) and the service then runs
// without further settings; the subnets it reports appear as suggested
// scopes to confirm in the UI.

const usage = `Usage:
  reticora-collector                 run the collector
  reticora-collector enroll --server <url> --code <code> [--name <name>] [--credentials <path>] [--ca-file <pem>]
                                     (without --code the code is read from RETICORA_ENROLL_TOKEN,
                                     which keeps it out of the process list)
  reticora-collector version         print the version
`

// runCommand handles the subcommands; handled is false for the plain
// collector start.
func runCommand(ctx context.Context, args []string, stdout, stderr io.Writer) (handled bool, code int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "version":
		fmt.Fprintln(stdout, updateArtifact, Version)
		return true, 0
	case "enroll":
		return true, runEnroll(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, usage)
		return true, 0
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", args[0], usage)
	return true, 2
}

func credentialsPath() string {
	if p := strings.TrimSpace(os.Getenv("RETICORA_CREDENTIALS_PATH")); p != "" {
		return p
	}
	return enrollment.DefaultCredentialsPath
}

func runEnroll(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	fs.SetOutput(stderr)
	server := fs.String("server", "", "URL of the Reticora server (https://cmdb.example.com)")
	code := fs.String("code", "", "one-time enrollment code from the UI (valid 24 h); default "+envEnrollToken)
	hostname, _ := os.Hostname()
	name := fs.String("name", hostname, "name of the collector")
	path := fs.String("credentials", credentialsPath(), "where the collector identity is stored")
	caFile := fs.String("ca-file", "", "PEM bundle to trust for the server certificate (private CA)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	serverURL := strings.TrimRight(strings.TrimSpace(*server), "/")
	// The code from the environment stays out of the process list
	// (install-vm.sh passes it so).
	enrollCode := strings.TrimSpace(*code)
	if enrollCode == "" {
		enrollCode = strings.TrimSpace(os.Getenv(envEnrollToken))
	}
	if serverURL == "" || enrollCode == "" {
		fmt.Fprint(stderr, "enroll needs --server and --code (or "+envEnrollToken+")\n\n"+usage)
		return 2
	}
	serverURL = normalizeServerURL(serverURL)
	var caPEM []byte
	if *caFile != "" {
		var err error
		if caPEM, err = os.ReadFile(*caFile); err != nil {
			fmt.Fprintf(stderr, "read --ca-file: %v\n", err)
			return 1
		}
	}
	client, err := enrollHTTPClient(caPEM)
	if err != nil {
		fmt.Fprintf(stderr, "--ca-file: %v\n", err)
		return 1
	}
	creds, err := enrollAndStore(ctx, client, serverURL, enrollCode, *name, *path)
	if err != nil {
		fmt.Fprintln(stderr, strings.ReplaceAll(err.Error(), enrollCode, "[redacted]"))
		return 1
	}
	fmt.Fprintf(stdout, "Collector %q enrolled as %s.\nIdentity stored in %s.\n"+
		"Start or restart the collector (systemctl restart reticora-collector); a running collector picks the identity up\n"+
		"within %s. It connects to %s (4222/TCP outbound) and reports its local subnets and RETICORA_SCAN_SUBNETS;\n"+
		"confirm them as discovery scopes on the Discovery page: nothing is discovered before.\n",
		*name, creds.DeviceID, *path, natsEnrollPoll, natsEndpoint(serverURL))
	return 0
}

// normalizeServerURL trims a trailing slash and defaults the scheme to
// https.
func normalizeServerURL(serverURL string) string {
	serverURL = strings.TrimRight(strings.TrimSpace(serverURL), "/")
	if !strings.HasPrefix(serverURL, "https://") && !strings.HasPrefix(serverURL, "http://") {
		serverURL = "https://" + serverURL
	}
	return serverURL
}

// enrollHTTPClient is the client of the enrollment request; caPEM, when
// set, replaces the system roots (private CA).
func enrollHTTPClient(caPEM []byte) (*http.Client, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	if len(caPEM) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caPEM) {
			return nil, errors.New("holds no certificate")
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}}
	}
	return client, nil
}

// enrollAndStore redeems the enrollment code at serverURL (the key is
// generated locally, the server receives the CSR) and stores the identity
// with server URL and name in the credentials file at path (mode 0600).
func enrollAndStore(ctx context.Context, client *http.Client, serverURL, code, name, path string) (*keystore.Credentials, error) {
	c := &enrollment.CollectorClient{BaseURL: serverURL, Transport: enrollment.HTTPSTransport{Client: client}}
	creds, err := c.Enroll(ctx, code, name, Version)
	if err != nil {
		return nil, fmt.Errorf("enrollment failed: %w", err)
	}
	if creds.Metadata == nil {
		creds.Metadata = map[string]string{}
	}
	creds.Metadata["server_url"] = serverURL
	creds.Metadata["name"] = name
	if err = (&keystore.FileStore{Path: path}).Save(ctx, *creds); err != nil {
		return nil, fmt.Errorf("store credentials: %w", err)
	}
	return creds, nil
}

// Unattended enrollment (COL-04): container and automated deployments
// start the collector with RETICORA_ENROLL_TOKEN (the one-time enrollment
// code) and RETICORA_CLOUD_URL (the server URL). Without a keystore at
// RETICORA_CREDENTIALS_PATH the first start enrolls like `enroll --server
// --code` and then runs normally; later starts find the keystore and do
// not enroll again. A private CA for the server certificate comes from
// RETICORA_TLS_CA_FILE or RETICORA_TLS_CA. The token is never logged.
const (
	envEnrollToken = "RETICORA_ENROLL_TOKEN"
	envCloudURL    = "RETICORA_CLOUD_URL"
)

// errEnrollEnvIncomplete: only one of the two bootstrap variables is set.
var errEnrollEnvIncomplete = errors.New("RETICORA_ENROLL_TOKEN and RETICORA_CLOUD_URL must be set together for the unattended enrollment; " +
	"set both, or neither when the collector is already enrolled (or enrolled with `reticora-collector enroll --server --code`)")

// bootstrapEnrollment performs the unattended enrollment from the
// environment when it is configured and no keystore exists yet; enrolled
// reports whether it did. The returned error never contains the token.
func bootstrapEnrollment(ctx context.Context, getenv func(string) string) (enrolled bool, err error) {
	token, cloudURL := strings.TrimSpace(getenv(envEnrollToken)), strings.TrimSpace(getenv(envCloudURL))
	if token == "" && cloudURL == "" {
		return false, nil
	}
	if token == "" || cloudURL == "" {
		return false, errEnrollEnvIncomplete
	}
	path := credentialsPath()
	switch _, lerr := (&keystore.FileStore{Path: path}).Load(ctx); {
	case lerr == nil:
		return false, nil // already enrolled: the keystore wins
	case !errors.Is(lerr, keystore.ErrNotFound):
		// A damaged keystore is not overwritten by a new identity.
		return false, fmt.Errorf("read keystore %s: %w", path, lerr)
	}
	caPEM := []byte(strings.TrimSpace(getenv("RETICORA_TLS_CA")))
	if f := strings.TrimSpace(getenv("RETICORA_TLS_CA_FILE")); f != "" {
		if caPEM, err = os.ReadFile(f); err != nil {
			return false, fmt.Errorf("read RETICORA_TLS_CA_FILE: %w", err)
		}
	}
	client, err := enrollHTTPClient(caPEM)
	if err != nil {
		return false, fmt.Errorf("server CA: %w", err)
	}
	name, _ := os.Hostname()
	if _, err = enrollAndStore(ctx, client, normalizeServerURL(cloudURL), token, name, path); err != nil {
		return false, errors.New(strings.ReplaceAll(err.Error(), token, "[redacted]"))
	}
	return true, nil
}

// applyEnrolledIdentity applies the identity of the credentials file. The
// organization (O) and collector id (CN) of the enrolled certificate win
// over RETICORA_ORGANIZATION_ID and RETICORA_COLLECTOR_ID, which are logged
// as a warning when they differ: the edge authorizes the collector by its
// certificate, another value would only mislabel the HTTP path and the
// metric samples. A collector with a client certificate of its own
// (RETICORA_TLS_CLIENT_CERT[_FILE]) keeps the values of the environment;
// the credentials file only fills empty ones. The server URL of the
// enrollment applies unless serverFromEnv: RETICORA_SERVER_URL still wins,
// with a warning when its host differs from the enrolled one, because the
// NATS endpoint follows it.
func applyEnrolledIdentity(ctx context.Context, cfg *collectorConfig, serverFromEnv bool) error {
	creds, err := (&keystore.FileStore{Path: cfg.CredentialsPath}).Load(ctx)
	if err != nil {
		return err
	}
	enrolledURL := creds.Metadata["server_url"]
	switch {
	case enrolledURL == "":
	case !serverFromEnv:
		cfg.ServerURL = enrolledURL
	case urlHost(cfg.ServerURL) != urlHost(enrolledURL):
		slog.Warn("RETICORA_SERVER_URL differs from the server URL of the enrollment; the collector and its NATS endpoint follow RETICORA_SERVER_URL",
			"event", "collector.identity.mismatch", "server_url", cfg.ServerURL, "enrolled_server_url", enrolledURL,
			"nats_url", natsEndpoint(cfg.ServerURL),
			"hint", "remove RETICORA_SERVER_URL from collector.env or set it to the URL of the enrollment")
	}
	cert, err := leafCertificate(creds.ClientCertificatePEM)
	if err != nil {
		return err
	}
	if len(cert.Subject.Organization) != 1 {
		return errors.New("the collector certificate names no organization")
	}
	collectorID := cert.Subject.CommonName
	if collectorID == "" {
		collectorID = creds.DeviceID
	}
	ownCert := len(cfg.TLSCertPEM) > 0 || cfg.TLSCertFile != ""
	cfg.OrganizationID = enrolledValue("RETICORA_ORGANIZATION_ID", cfg.OrganizationID, cert.Subject.Organization[0], ownCert)
	cfg.CollectorID = enrolledValue("RETICORA_COLLECTOR_ID", cfg.CollectorID, collectorID, ownCert)
	return nil
}

// enrolledValue is the value of an identity setting: the enrolled one,
// unless the collector has a client certificate of its own (ownCert) and
// the environment sets it.
func enrolledValue(env, configured, enrolled string, ownCert bool) string {
	switch {
	case configured == "" || configured == enrolled:
		return enrolled
	case ownCert:
		return configured
	}
	slog.Warn(env+" differs from the enrolled identity; the identity of the collector certificate is used",
		"event", "collector.identity.mismatch", "configured", configured, "enrolled", enrolled,
		"hint", "remove "+env+" from collector.env; the enrollment sets it")
	return enrolled
}

// urlHost is the lower-case host of a server URL (scheme optional).
func urlHost(serverURL string) string {
	u, err := url.Parse(normalizeServerURL(serverURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}
