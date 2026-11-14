// Package config reads the running configuration of network devices over
// SSH for the configuration change detection (ALR-07, WP-1053, E-207). It
// is no plugins.Plugin: the collector calls it on its own schedule. The
// platforms are fixed (cisco_ios, cisco_nxos, junos, RouterOS,
// huawei_vrp), as are the commands per platform; they are the
// ConfigCommands of collector/plugins/ssh, never a text from the target, a
// profile or the server.
//
// A read normalizes the text (line endings, volatile lines), masks every
// secret value and hashes the masked text with SHA-256. Neither the
// configuration nor a secret goes into a log; callers log the CI, the
// platform, a hash prefix and the masked count only.
package config

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// MaxBytes bounds the normalized configuration (2 MiB).
const MaxBytes = 2 << 20

// ErrUnsupportedPlatform is returned for a device of another platform; no
// command beyond the detection is run.
var ErrUnsupportedPlatform = errors.New("unsupported_platform")

// ErrTooLarge is returned for a configuration above MaxBytes.
var ErrTooLarge = errors.New("config_too_large")

// Fetcher logs in to a device and runs the fixed commands; the SSH plugin
// (collector/plugins/ssh) implements it.
type Fetcher interface {
	ServerVersion(ctx context.Context, target string, creds map[string]string) (string, error)
	FetchConfig(ctx context.Context, target string, creds map[string]string, command string) (string, error)
}

// Snapshot is a read configuration: normalized and masked text, its hash
// and the number of masked values.
type Snapshot struct {
	Platform    Platform
	Text        string
	SHA256      string
	SizeBytes   int
	MaskedCount int
}

// Detect determines the platform of target (WP-1053 AK1): the SSH banner
// (ROSSSH, HUAWEI), else the output of show version (NX-OS, Cisco IOS,
// JUNOS). Another platform is ErrUnsupportedPlatform.
func Detect(ctx context.Context, f Fetcher, target string, creds map[string]string) (Platform, error) {
	banner, err := f.ServerVersion(ctx, target, creds)
	if err != nil {
		return "", err
	}
	if p, ok := PlatformFromBanner(banner); ok {
		return p, nil
	}
	out, err := f.FetchConfig(ctx, target, creds, cmdShowVersion)
	if err != nil {
		return "", err
	}
	if p, ok := PlatformFromVersion(out); ok {
		return p, nil
	}
	return "", ErrUnsupportedPlatform
}

// Collect reads the running configuration of target. platform is the
// platform detected before (state of the collector); empty or unknown
// detects it first.
func Collect(ctx context.Context, f Fetcher, target string, creds map[string]string, platform Platform) (*Snapshot, error) {
	if !platform.Valid() {
		var err error
		if platform, err = Detect(ctx, f, target, creds); err != nil {
			return nil, err
		}
	}
	raw, err := f.FetchConfig(ctx, target, creds, platform.Command())
	if err != nil {
		return nil, err
	}
	return Process(platform, raw)
}

// Process normalizes, masks and hashes a configuration of platform.
func Process(platform Platform, raw string) (*Snapshot, error) {
	text := Normalize(platform, raw)
	if len(text) > MaxBytes {
		return nil, ErrTooLarge
	}
	masked, n := Mask(platform, text)
	return &Snapshot{Platform: platform, Text: masked, SHA256: Hash(masked), SizeBytes: len(masked), MaskedCount: n}, nil
}

// Hash returns the SHA-256 of text in hex.
func Hash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// Normalize sets the line endings to LF and removes the volatile lines of
// the platform (WP-1053 AK3), so that the same configuration read at
// another time has the same hash.
func Normalize(platform Platform, raw string) string {
	text := strings.ReplaceAll(raw, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	patterns := volatileLines[platform]
	lines := strings.Split(text, "\n")
	out := lines[:0]
	for _, line := range lines {
		volatile := false
		for _, re := range patterns {
			if re.MatchString(line) {
				volatile = true
				break
			}
		}
		if !volatile {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
