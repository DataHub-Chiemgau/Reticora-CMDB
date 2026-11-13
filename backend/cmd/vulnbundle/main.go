// Package main is vulnbundle, the export tool of the offline bundle of the
// vulnerability catalog (SEC-02, TEC-16, WP-1120, E-235 option 2): it runs
// every importer of internal/vulnfeed (vulnfeed.BundleImporters: CSAF, NVD
// with KEV) against a catalog in memory, writes the bundle
// reticora-vulnfeed/1 (tar.gz) and signs manifest.json with the Ed25519 key
// of VULNBUNDLE_SIGNING_KEY (base64 seed or key). The release workflow
// builds it into dist/, where it gets the cosign signature of every
// artifact as well.
//
//	vulnbundle -out dist/reticora-vulnfeed.tar.gz
//
// Sources: RETICORA_VULNFEED_CSAF_PROVIDERS (comma separated, default the
// BSI provider), RETICORA_VULNFEED_NVD_API_KEY (optional),
// RETICORA_VULNFEED_KEV_URL. A source that fails is reported; the bundle
// is written from the others, unless -strict is set.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/platform/egress"
	"github.com/DataHub-Chiemgau/Reticora-CMDB/backend/internal/vulnfeed"
)

func main() {
	out := flag.String("out", "reticora-vulnfeed.tar.gz", "path of the bundle")
	strict := flag.Bool("strict", false, "fail when a source fails")
	timeout := flag.Duration("timeout", 6*time.Hour, "time limit of the export")
	flag.Parse()
	if err := run(*out, *strict, *timeout); err != nil {
		slog.Error("vulnbundle", "error", err)
		os.Exit(1)
	}
}

func run(out string, strict bool, timeout time.Duration) error {
	key, err := vulnfeed.ParsePrivateKey(os.Getenv("VULNBUNDLE_SIGNING_KEY"))
	if err != nil {
		return fmt.Errorf("VULNBUNDLE_SIGNING_KEY: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	store := vulnfeed.NewMemoryStore()
	var providers []string
	for _, p := range strings.Split(os.Getenv("RETICORA_VULNFEED_CSAF_PROVIDERS"), ",") {
		if p = strings.TrimSpace(p); p != "" {
			providers = append(providers, p)
		}
	}
	importers, err := vulnfeed.BundleImporters(store, &vulnfeed.BundleSources{CSAFProviders: providers,
		NVDAPIKey: os.Getenv("RETICORA_VULNFEED_NVD_API_KEY"), KEVURL: os.Getenv("RETICORA_VULNFEED_KEV_URL"), Egress: egress.Options{}})
	if err != nil {
		return err
	}
	var failures []error
	for _, imp := range importers {
		if ierr := imp(ctx); ierr != nil {
			slog.Warn("vulnbundle: source failed", "error", ierr)
			failures = append(failures, ierr)
		}
	}
	if strict && len(failures) > 0 {
		return errors.Join(failures...)
	}
	if store.Len() == 0 {
		return errors.New("no advisory imported; no bundle written")
	}
	if err = os.MkdirAll(filepath.Dir(out), 0o750); err != nil {
		return err
	}
	tmp := out + ".tmp"
	f, err := os.Create(tmp) //nolint:gosec // the path is the -out flag of the release workflow
	if err != nil {
		return err
	}
	manifest, err := vulnfeed.WriteBundle(f, store, key, time.Now().UTC())
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err = os.Rename(tmp, out); err != nil {
		return err
	}
	slog.Info("vulnbundle written", "path", out, "advisories", store.Len(), "files", len(manifest.Files),
		"created_at", manifest.CreatedAt.Format(time.RFC3339))
	return nil
}
