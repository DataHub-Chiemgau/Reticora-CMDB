package collectorcmd

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/update"
)

// Tests of the auto-updater as the running collector composes it (COL-06,
// WP-546): newUpdateChecker reads the channel manifest of
// RETICORA_UPDATE_URL, verifies with the cosign on PATH against the pinned
// release identity and probes the new binary with "version"; an unknown
// channel switches updates off. The binaries themselves are exercised by
// tests/collector-selfupdate.test.sh.

// TestNewUpdateCheckerComposesSourceVerifierAndProbe: the checker of the
// running collector fetches the manifest of the configured URL and channel
// for the artifact reticora-collector, runs cosign verify-blob with the
// pinned release identity of the GitHub Actions issuer and probes the new
// binary with "version".
func TestNewUpdateCheckerComposesSourceVerifierAndProbe(t *testing.T) {
	manifest := "/updates/beta/reticora-collector-" + runtime.GOOS + "-" + runtime.GOARCH + ".release.json"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != manifest {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(update.ReleaseInfo{
			Version: "v0.0.2", Channel: update.ChannelBeta,
			DownloadURL: "https://updates.example.test/reticora-collector-v0.0.2", SignatureBundleURL: "https://updates.example.test/reticora-collector-v0.0.2.sigstore.json",
		})
	}))
	defer srv.Close()

	// The cosign on PATH records its arguments.
	dir := t.TempDir()
	argsFile := filepath.Join(dir, "cosign.args")
	stub := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > '" + argsFile + "'\n"
	if err := os.WriteFile(filepath.Join(dir, "cosign"), []byte(stub), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	cfg := collectorConfig{UpdateURL: srv.URL + "/updates/", UpdateChannel: update.ChannelBeta}
	c := newUpdateChecker(&cfg)

	src, ok := c.Source.(*update.HTTPSource)
	if !ok {
		t.Fatalf("source = %#v, want *update.HTTPSource", c.Source)
	}
	if src.BaseURL != cfg.UpdateURL || src.Channel != update.ChannelBeta || src.Artifact != "reticora-collector" {
		t.Errorf("source = %+v, want base %s, channel beta, artifact reticora-collector", src, cfg.UpdateURL)
	}
	if c.Channel != update.ChannelBeta {
		t.Errorf("checker channel = %q, want beta", c.Channel)
	}
	if !slices.Equal(c.ProbeArgs, []string{"version"}) {
		t.Errorf("probe args = %q, want [version]", c.ProbeArgs)
	}

	// The source reads the manifest of the channel below RETICORA_UPDATE_URL.
	rel, err := c.Check(context.Background(), "v0.0.1")
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if rel == nil || rel.Version != "v0.0.2" || rel.Channel != update.ChannelBeta {
		t.Fatalf("release = %+v, want v0.0.2 of the channel beta", rel)
	}

	// The verifier runs the cosign of PATH with the pinned identity and issuer.
	v, ok := c.Verifier.(update.CosignVerifier)
	if !ok || v.Binary != "cosign" {
		t.Fatalf("verifier = %#v, want CosignVerifier running cosign", c.Verifier)
	}
	if v.IdentityRegexp != "" && v.IdentityRegexp != update.ReleaseIdentityRegexp {
		t.Errorf("verifier identity = %q, want the pinned release identity", v.IdentityRegexp)
	}
	if v.Issuer != "" && v.Issuer != update.GitHubActionsIssuer {
		t.Errorf("verifier issuer = %q, want the GitHub Actions issuer", v.Issuer)
	}
	if err = c.Verifier.Verify(context.Background(), "/staging/reticora-collector.update", "/staging/reticora-collector.update.sigstore.json"); err != nil {
		t.Fatalf("verify with the cosign stub: %v", err)
	}
	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatalf("cosign was not run: %v", err)
	}
	want := []string{
		"verify-blob",
		"--bundle", "/staging/reticora-collector.update.sigstore.json",
		"--certificate-identity-regexp", update.ReleaseIdentityRegexp,
		"--certificate-oidc-issuer", update.GitHubActionsIssuer,
		"/staging/reticora-collector.update",
	}
	if got := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n"); !slices.Equal(got, want) {
		t.Errorf("cosign arguments = %q, want %q", got, want)
	}

	// The pinned identity is the release workflow of this repository on a
	// version tag: a signature of another workflow or branch does not match.
	identity := regexp.MustCompile(update.ReleaseIdentityRegexp)
	for subject, match := range map[string]bool{
		"https://github.com/DataHub-Chiemgau/Reticora-CMDB/.github/workflows/release.yml@refs/tags/v1.4.0":        true,
		"https://github.com/DataHub-Chiemgau/Reticora-CMDB/.github/workflows/release.yml@refs/tags/v1.4.0-beta.1": true,
		"https://github.com/DataHub-Chiemgau/Reticora-CMDB/.github/workflows/ci.yml@refs/heads/main":              false,
		"https://github.com/DataHub-Chiemgau/Reticora-CMDB/.github/workflows/release.yml@refs/heads/main":         false,
		"https://github.com/other/Reticora-CMDB/.github/workflows/release.yml@refs/tags/v1.4.0":                   false,
	} {
		if identity.MatchString(subject) != match {
			t.Errorf("release identity matches %s = %v, want %v", subject, !match, match)
		}
	}
}

// TestUnknownUpdateChannelSwitchesUpdatesOff: an unknown
// RETICORA_UPDATE_CHANNEL disables auto-updates with an error in the log
// (no update loop starts), and a checker of an unknown channel fetches
// nothing.
func TestUnknownUpdateChannelSwitchesUpdatesOff(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	t.Setenv("RETICORA_CREDENTIALS_PATH", filepath.Join(t.TempDir(), "credentials.json"))
	t.Setenv("RETICORA_UPDATE_URL", srv.URL)
	t.Setenv("RETICORA_UPDATE_CHANNEL", "nightly")
	logs := captureLogs(t)
	cfg := loadCollectorConfig()
	if cfg.UpdateURL != "" {
		t.Errorf("update URL = %q with the channel nightly, want empty (updates off)", cfg.UpdateURL)
	}
	if lines := logLines(t, logs, "invalid RETICORA_UPDATE_CHANNEL (stable or beta); auto-updates are disabled"); len(lines) != 1 || lines[0]["level"] != "ERROR" {
		t.Errorf("unknown channel: log %s", logs.String())
	}

	c := newUpdateChecker(&collectorConfig{UpdateURL: srv.URL, UpdateChannel: "nightly"})
	rel, err := c.Check(context.Background(), "v0.0.1")
	if err == nil || rel != nil || !strings.Contains(err.Error(), `unknown channel "nightly"`) {
		t.Errorf("check with the channel nightly = %+v, %v; want refused", rel, err)
	}
	if n := requests.Load(); n != 0 {
		t.Errorf("a checker of an unknown channel sent %d requests", n)
	}
	if errors.Is(err, update.ErrChannelMismatch) {
		t.Errorf("unknown channel reported as a channel mismatch: %v", err)
	}
}
