// Package update provides shared auto-update contracts for edge components.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// ReleaseInfo describes an available software release.
type ReleaseInfo struct {
	Version     string            `json:"version"`
	Channel     string            `json:"channel,omitempty"`
	DownloadURL string            `json:"downloadUrl,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Checksums   map[string]string `json:"checksums,omitempty"`
	// SignatureBundleURL points at the Sigstore bundle (cosign sign-blob
	// --bundle) of the artifact at DownloadURL. A release without it is
	// refused (TEC-15).
	SignatureBundleURL string    `json:"signatureBundleUrl,omitempty"`
	PublishedAt        time.Time `json:"publishedAt,omitempty"`
}

// Source retrieves release metadata from a remote control plane or artifact service.
type Source interface {
	Latest(ctx context.Context) (*ReleaseInfo, error)
}

// Updater defines the contract collector and agent processes will implement.
type Updater interface {
	Check(ctx context.Context, currentVersion string) (*ReleaseInfo, error)
	Apply(ctx context.Context, release ReleaseInfo) error
}

// Verifier checks the signature of a downloaded release artifact against
// its Sigstore bundle (TEC-15).
type Verifier interface {
	Verify(ctx context.Context, artifactPath, bundlePath string) error
}

// ErrUnsigned refuses a release without a signature bundle.
var ErrUnsigned = errors.New("update: release carries no signature bundle; unsigned updates are refused")

// Checker is a minimal implementation that compares the current version to the latest release.
type Checker struct {
	Source Source
	// Verifier is mandatory for Apply: every artifact is installed only
	// after its signature verified (TEC-15).
	Verifier Verifier

	// executablePath, when set, overrides os.Executable as the binary to
	// replace during Apply. It is a test seam; production code leaves it nil.
	executablePath func() (string, error)
}

// Check returns the latest release when it differs from the currently running version.
func (c *Checker) Check(ctx context.Context, currentVersion string) (*ReleaseInfo, error) {
	if c.Source == nil {
		return nil, fmt.Errorf("update: source is required")
	}

	latest, err := c.Source.Latest(ctx)
	if err != nil {
		return nil, fmt.Errorf("update: fetch latest release: %w", err)
	}
	if latest == nil || latest.Version == "" || latest.Version == currentVersion {
		return nil, nil
	}
	return latest, nil
}

// Apply downloads, verifies, and stages the new binary for the self-update workflow.
// The actual process restart is expected to be handled by the service manager (systemd, etc.).
// The artifact is installed only when its signature bundle verifies; an
// unsigned or wrongly signed release leaves the running binary untouched.
func (c *Checker) Apply(ctx context.Context, release ReleaseInfo) error {
	if release.DownloadURL == "" {
		return fmt.Errorf("update: download URL is required")
	}
	if c.Verifier == nil {
		return fmt.Errorf("update: a signature verifier is required")
	}
	if release.SignatureBundleURL == "" {
		return ErrUnsigned
	}

	resolveExec := c.executablePath
	if resolveExec == nil {
		resolveExec = os.Executable
	}
	execPath, err := resolveExec()
	if err != nil {
		return fmt.Errorf("update: resolve executable path: %w", err)
	}
	stagingPath := execPath + ".update"
	bundlePath := stagingPath + ".sigstore.json"
	defer os.Remove(bundlePath)

	client := &http.Client{Timeout: 5 * time.Minute}
	sum, err := download(ctx, client, release.DownloadURL, stagingPath, 0o755)
	if err != nil {
		os.Remove(stagingPath)
		return fmt.Errorf("update: %w", err)
	}

	// Verify checksum if provided
	checksumKey := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	expected, ok := release.Checksums[checksumKey]
	if !ok {
		expected, ok = release.Checksums["sha256"]
	}
	if ok && sum != expected {
		os.Remove(stagingPath)
		return fmt.Errorf("update: checksum mismatch: expected %s, got %s", expected, sum)
	}

	// Mandatory signature check before anything is swapped.
	if _, err = download(ctx, client, release.SignatureBundleURL, bundlePath, 0o644); err != nil {
		os.Remove(stagingPath)
		return fmt.Errorf("update: signature bundle: %w", err)
	}
	if err = c.Verifier.Verify(ctx, stagingPath, bundlePath); err != nil {
		os.Remove(stagingPath)
		return fmt.Errorf("update: signature verification failed, update refused: %w", err)
	}

	// Atomic swap: move current binary to .old, then staging to current
	backupPath := execPath + ".old"
	if err := os.Remove(backupPath); err != nil && !os.IsNotExist(err) {
		slog.Warn("update: failed to remove previous backup", "path", backupPath, "error", err)
	}

	if err := os.Rename(execPath, backupPath); err != nil {
		os.Remove(stagingPath)
		return fmt.Errorf("update: backup current binary: %w", err)
	}

	if err := os.Rename(stagingPath, execPath); err != nil {
		// Attempt rollback
		_ = os.Rename(backupPath, execPath)
		return fmt.Errorf("update: install new binary: %w", err)
	}

	// Write version marker for the service manager
	markerPath := filepath.Join(filepath.Dir(execPath), ".update-applied")
	_ = os.WriteFile(markerPath, []byte(release.Version), 0o644)

	return nil
}

// download writes url to path and returns the hex SHA-256 of the content.
func download(ctx context.Context, client *http.Client, url, path string, mode os.FileMode) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return "", fmt.Errorf("build download request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download release: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return "", fmt.Errorf("create staging file: %w", err)
	}
	hasher := sha256.New()
	if _, err = io.Copy(io.MultiWriter(file, hasher), resp.Body); err != nil {
		file.Close()
		return "", fmt.Errorf("write staging file: %w", err)
	}
	if err = file.Close(); err != nil {
		return "", fmt.Errorf("write staging file: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// Keyless signatures of the release workflow (E-31): the certificate must
// name the release workflow of this repository on a version tag and come
// from the GitHub Actions OIDC issuer.
const (
	ReleaseIdentityRegexp = `^https://github\.com/DataHub-Chiemgau/Reticora-CMDB/\.github/workflows/release\.yml@refs/tags/v.+$`
	GitHubActionsIssuer   = "https://token.actions.githubusercontent.com"
)

// CosignVerifier verifies keyless cosign signatures with the cosign CLI
// (cosign verify-blob --bundle), which checks the Fulcio certificate chain,
// the Rekor transparency-log entry and the signer identity.
type CosignVerifier struct {
	// Binary is the cosign executable (default "cosign" from PATH).
	Binary string
	// IdentityRegexp and Issuer pin the signer; empty values use
	// ReleaseIdentityRegexp and GitHubActionsIssuer.
	IdentityRegexp string
	Issuer         string

	// run executes the command; a test seam, nil uses os/exec.
	run func(ctx context.Context, name string, args ...string) ([]byte, error)
}

// Verify implements Verifier.
func (v CosignVerifier) Verify(ctx context.Context, artifactPath, bundlePath string) error {
	binary, identity, issuer := v.Binary, v.IdentityRegexp, v.Issuer
	if binary == "" {
		binary = "cosign"
	}
	if identity == "" {
		identity = ReleaseIdentityRegexp
	}
	if issuer == "" {
		issuer = GitHubActionsIssuer
	}
	run := v.run
	if run == nil {
		run = func(ctx context.Context, name string, args ...string) ([]byte, error) {
			return exec.CommandContext(ctx, name, args...).CombinedOutput()
		}
	}
	out, err := run(ctx, binary, "verify-blob",
		"--bundle", bundlePath,
		"--certificate-identity-regexp", identity,
		"--certificate-oidc-issuer", issuer,
		artifactPath)
	if err != nil {
		return fmt.Errorf("cosign verify-blob: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}
