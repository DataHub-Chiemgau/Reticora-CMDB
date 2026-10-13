package collectorcmd

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
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
	code := fs.String("code", "", "one-time enrollment code from the UI (valid 24 h)")
	hostname, _ := os.Hostname()
	name := fs.String("name", hostname, "name of the collector")
	path := fs.String("credentials", credentialsPath(), "where the collector identity is stored")
	caFile := fs.String("ca-file", "", "PEM bundle to trust for the server certificate (private CA)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	serverURL := strings.TrimRight(strings.TrimSpace(*server), "/")
	if serverURL == "" || strings.TrimSpace(*code) == "" {
		fmt.Fprint(stderr, "enroll needs --server and --code\n\n"+usage)
		return 2
	}
	if !strings.HasPrefix(serverURL, "https://") && !strings.HasPrefix(serverURL, "http://") {
		serverURL = "https://" + serverURL
	}
	client := &http.Client{Timeout: 30 * time.Second}
	if *caFile != "" {
		pemData, err := os.ReadFile(*caFile)
		if err != nil {
			fmt.Fprintf(stderr, "read --ca-file: %v\n", err)
			return 1
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pemData) {
			fmt.Fprintln(stderr, "--ca-file holds no certificate")
			return 1
		}
		client.Transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}}
	}
	c := &enrollment.CollectorClient{BaseURL: serverURL, Transport: enrollment.HTTPSTransport{Client: client}}
	creds, err := c.Enroll(ctx, strings.TrimSpace(*code), *name, Version)
	if err != nil {
		fmt.Fprintf(stderr, "enrollment failed: %v\n", err)
		return 1
	}
	if creds.Metadata == nil {
		creds.Metadata = map[string]string{}
	}
	creds.Metadata["server_url"] = serverURL
	creds.Metadata["name"] = *name
	if err = (&keystore.FileStore{Path: *path}).Save(ctx, *creds); err != nil {
		fmt.Fprintf(stderr, "store credentials: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Collector %q enrolled as %s.\nIdentity stored in %s.\n"+
		"Start or restart the collector (systemctl restart reticora-collector); it reports its local subnets,\n"+
		"confirm them as discovery scopes on the Discovery page.\n", *name, creds.DeviceID, *path)
	return 0
}

// applyEnrolledIdentity fills server URL, organization and collector id
// from the credentials file when the environment does not set them.
func applyEnrolledIdentity(ctx context.Context, cfg *collectorConfig, serverFromEnv bool) error {
	creds, err := (&keystore.FileStore{Path: cfg.CredentialsPath}).Load(ctx)
	if err != nil {
		return err
	}
	if !serverFromEnv && creds.Metadata["server_url"] != "" {
		cfg.ServerURL = creds.Metadata["server_url"]
	}
	if cfg.CollectorID == "" {
		cfg.CollectorID = creds.DeviceID
	}
	if cfg.OrganizationID == "" {
		cert, cerr := leafCertificate(creds.ClientCertificatePEM)
		if cerr != nil {
			return cerr
		}
		if len(cert.Subject.Organization) != 1 {
			return errors.New("the collector certificate names no organization")
		}
		cfg.OrganizationID = cert.Subject.Organization[0]
	}
	return nil
}
