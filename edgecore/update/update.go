// Package update provides shared auto-update contracts for edge components.
package update

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// ErrApplyNotImplemented marks the Phase 5 update execution path as a scaffold only.
var ErrApplyNotImplemented = errors.New("update: apply not implemented")

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

// Updater defines the contract collector and agent processes will implement in Phase 5.
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

// Apply is a placeholder for the future self-update workflow.
func (c *Checker) Apply(context.Context, ReleaseInfo) error {
	return ErrApplyNotImplemented
}
