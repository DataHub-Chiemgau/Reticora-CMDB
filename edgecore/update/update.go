// Package update provides shared auto-update contracts for edge components.
package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// ReleaseInfo describes an available software release.
type ReleaseInfo struct {
	Version     string            `json:"version"`
	Channel     string            `json:"channel,omitempty"`
	DownloadURL string            `json:"downloadUrl,omitempty"`
	Notes       string            `json:"notes,omitempty"`
	Checksums   map[string]string `json:"checksums,omitempty"`
	PublishedAt time.Time         `json:"publishedAt,omitempty"`
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

// Checker is a minimal implementation that compares the current version to the latest release.
type Checker struct {
	Source Source
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
func (c *Checker) Apply(ctx context.Context, release ReleaseInfo) error {
	if release.DownloadURL == "" {
		return fmt.Errorf("update: download URL is required")
	}

	// Download the release binary
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, release.DownloadURL, nil)
	if err != nil {
		return fmt.Errorf("update: build download request: %w", err)
	}

	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("update: download release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("update: download failed with status %d", resp.StatusCode)
	}

	// Write to a staging file
	execPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("update: resolve executable path: %w", err)
	}
	stagingPath := execPath + ".update"

	stagingFile, err := os.OpenFile(stagingPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return fmt.Errorf("update: create staging file: %w", err)
	}

	hasher := sha256.New()
	writer := io.MultiWriter(stagingFile, hasher)

	if _, err := io.Copy(writer, resp.Body); err != nil {
		stagingFile.Close()
		os.Remove(stagingPath)
		return fmt.Errorf("update: write staging file: %w", err)
	}
	stagingFile.Close()

	// Verify checksum if provided
	checksumKey := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)
	if expected, ok := release.Checksums[checksumKey]; ok {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if actual != expected {
			os.Remove(stagingPath)
			return fmt.Errorf("update: checksum mismatch: expected %s, got %s", expected, actual)
		}
	} else if expected, ok := release.Checksums["sha256"]; ok {
		actual := hex.EncodeToString(hasher.Sum(nil))
		if actual != expected {
			os.Remove(stagingPath)
			return fmt.Errorf("update: checksum mismatch: expected %s, got %s", expected, actual)
		}
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
