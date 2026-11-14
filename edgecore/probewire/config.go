package probewire

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"
)

// ConfigSnapshot is a running configuration the collector read from a
// device (ALR-07, WP-1053, E-207) on collector.<org>.<id>.config: masked
// before hashing in the collector, sent only when its hash differs from the
// last confirmed one of the CI. Content is the gzip of the normalized,
// masked text; SHA256 (hex) and SizeBytes describe that text. The
// collector keeps no secret value in it.
type ConfigSnapshot struct {
	Header
	CIID         string    `json:"ci_id"`
	ManagementIP string    `json:"management_ip"`
	CollectedAt  time.Time `json:"collected_at"`
	Source       string    `json:"source"`
	Trigger      string    `json:"trigger"`
	Platform     string    `json:"platform"`
	SHA256       string    `json:"sha256"`
	SizeBytes    int64     `json:"size_bytes"`
	MaskedCount  int       `json:"masked_count"`
	Content      []byte    `json:"content"`
}

// Values of ConfigSnapshot.Source and Trigger.
const (
	ConfigSourceSSH       = "ssh"
	ConfigTriggerSchedule = "schedule"
)

// MaxConfigBytes bounds the uncompressed configuration (2 MiB).
const MaxConfigBytes = 2 << 20

// ErrInvalidConfig is returned by ConfigSnapshot.Validate and Text.
var ErrInvalidConfig = errors.New("probewire: invalid config snapshot")

// GzipConfig compresses a configuration text for Content.
func GzipConfig(text string) ([]byte, error) {
	var b bytes.Buffer
	w, err := gzip.NewWriterLevel(&b, gzip.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := io.WriteString(w, text); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// Validate checks the fields of a snapshot without decompressing it.
func (c *ConfigSnapshot) Validate() error {
	switch {
	case c.CIID == "":
		return fmt.Errorf("%w: ci_id is required", ErrInvalidConfig)
	case c.CollectedAt.IsZero():
		return fmt.Errorf("%w: collected_at is required", ErrInvalidConfig)
	case c.Source != ConfigSourceSSH:
		return fmt.Errorf("%w: source %q", ErrInvalidConfig, c.Source)
	case c.Trigger == "":
		return fmt.Errorf("%w: trigger is required", ErrInvalidConfig)
	case c.Platform == "":
		return fmt.Errorf("%w: platform is required", ErrInvalidConfig)
	case len(c.SHA256) != 64:
		return fmt.Errorf("%w: sha256 must be 64 hex digits", ErrInvalidConfig)
	case c.SizeBytes < 0 || c.SizeBytes > MaxConfigBytes:
		return fmt.Errorf("%w: size_bytes out of range", ErrInvalidConfig)
	case c.MaskedCount < 0:
		return fmt.Errorf("%w: masked_count is negative", ErrInvalidConfig)
	case len(c.Content) == 0:
		return fmt.Errorf("%w: content is required", ErrInvalidConfig)
	}
	if _, err := hex.DecodeString(c.SHA256); err != nil {
		return fmt.Errorf("%w: sha256 must be 64 hex digits", ErrInvalidConfig)
	}
	return nil
}

// Text decompresses Content (at most MaxConfigBytes) and checks it against
// SizeBytes and SHA256.
func (c *ConfigSnapshot) Text() (string, error) {
	r, err := gzip.NewReader(bytes.NewReader(c.Content))
	if err != nil {
		return "", fmt.Errorf("%w: content is no gzip", ErrInvalidConfig)
	}
	defer r.Close() //nolint:errcheck // in-memory reader
	raw, err := io.ReadAll(io.LimitReader(r, MaxConfigBytes+1))
	if err != nil {
		return "", fmt.Errorf("%w: content: %v", ErrInvalidConfig, err)
	}
	if len(raw) > MaxConfigBytes {
		return "", fmt.Errorf("%w: content above 2 MiB", ErrInvalidConfig)
	}
	if int64(len(raw)) != c.SizeBytes {
		return "", fmt.Errorf("%w: size_bytes does not match the content", ErrInvalidConfig)
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != c.SHA256 {
		return "", fmt.Errorf("%w: sha256 does not match the content", ErrInvalidConfig)
	}
	return string(raw), nil
}
