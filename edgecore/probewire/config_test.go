package probewire

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// goldenConfigContent is the gzip of goldenConfigText (fixed bytes, so the
// fixture does not depend on the compressor).
const (
	goldenConfigText    = "hostname r1\nenable secret 9 <masked>\nend\n"
	goldenConfigContent = "H4sIAAAAAAAC/8vILy7JS8xNVSgy5ErNS0zKSVUoTk0uSi1RsFSwyU0szk5NsQNKpHABAHKXZ8MpAAAA"
	goldenConfigSHA     = "efa3faf5b51da89250c286d194125af8c3dd9d6d95004179f1414fa3c14d0821"
)

func goldenConfig(t *testing.T) *ConfigSnapshot {
	t.Helper()
	content, err := base64.StdEncoding.DecodeString(goldenConfigContent)
	if err != nil {
		t.Fatal(err)
	}
	return &ConfigSnapshot{
		Header:       Header{SchemaVersion: SchemaVersion},
		CIID:         "6a1f8c2e-3b4d-4e5f-8a9b-000000000042",
		ManagementIP: "192.0.2.1",
		CollectedAt:  time.Date(2026, 11, 14, 8, 0, 0, 0, time.UTC),
		Source:       ConfigSourceSSH,
		Trigger:      ConfigTriggerSchedule,
		Platform:     "cisco_ios",
		SHA256:       goldenConfigSHA,
		SizeBytes:    int64(len(goldenConfigText)),
		MaskedCount:  1,
		Content:      content,
	}
}

// TestConfigSnapshotGolden covers WP-1053 AK5: encode equals the fixture
// byte for byte, decoding it yields the value, the content decompresses to
// the masked text with matching size and hash.
func TestConfigSnapshotGolden(t *testing.T) {
	msg := goldenConfig(t)
	got, err := Encode(msg)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "config_snapshot.json")
	if os.Getenv("RETICORA_UPDATE_GOLDEN") == "1" {
		if werr := os.WriteFile(path, append(got, '\n'), 0o644); werr != nil {
			t.Fatal(werr)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSuffix(want, []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Fatalf("encoding differs from %s:\n got %s\nwant %s", path, got, want)
	}
	var back ConfigSnapshot
	if err = Decode(want, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&back, msg) {
		t.Fatalf("decode differs:\n got %#v\nwant %#v", back, msg)
	}
	if err = back.Validate(); err != nil {
		t.Fatal(err)
	}
	text, err := back.Text()
	if err != nil || text != goldenConfigText {
		t.Fatalf("Text = %q, %v", text, err)
	}
}

// TestConfigSnapshotUnknownMajorVersion covers WP-1053 AK5: another major
// version is ErrUnsupportedVersion.
func TestConfigSnapshotUnknownMajorVersion(t *testing.T) {
	raw, err := Encode(goldenConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	v2 := bytes.Replace(raw, []byte(`"schema_version":"1.0"`), []byte(`"schema_version":"2.0"`), 1)
	if err := Decode(v2, &ConfigSnapshot{}); !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("err = %v, want ErrUnsupportedVersion", err)
	}
	// A newer minor version is read.
	v11 := bytes.Replace(raw, []byte(`"schema_version":"1.0"`), []byte(`"schema_version":"1.1"`), 1)
	if err := Decode(v11, &ConfigSnapshot{}); err != nil {
		t.Fatalf("minor version: %v", err)
	}
}

// TestConfigSnapshotChecks: Validate and Text refuse inconsistent snapshots.
func TestConfigSnapshotChecks(t *testing.T) {
	for name, mutate := range map[string]func(*ConfigSnapshot){
		"no ci":        func(c *ConfigSnapshot) { c.CIID = "" },
		"bad source":   func(c *ConfigSnapshot) { c.Source = "snmp" },
		"short hash":   func(c *ConfigSnapshot) { c.SHA256 = "abc" },
		"too large":    func(c *ConfigSnapshot) { c.SizeBytes = MaxConfigBytes + 1 },
		"no content":   func(c *ConfigSnapshot) { c.Content = nil },
		"no platform":  func(c *ConfigSnapshot) { c.Platform = "" },
		"no collected": func(c *ConfigSnapshot) { c.CollectedAt = time.Time{} },
	} {
		c := goldenConfig(t)
		mutate(c)
		if err := c.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Errorf("%s: Validate = %v", name, err)
		}
	}
	c := goldenConfig(t)
	c.SHA256 = strings.Repeat("0", 64)
	if _, err := c.Text(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("wrong hash: %v", err)
	}
	c = goldenConfig(t)
	c.SizeBytes++
	if _, err := c.Text(); !errors.Is(err, ErrInvalidConfig) {
		t.Errorf("wrong size: %v", err)
	}
	gz, err := GzipConfig(goldenConfigText)
	if err != nil {
		t.Fatal(err)
	}
	c = goldenConfig(t)
	c.Content = gz
	if text, err := c.Text(); err != nil || text != goldenConfigText {
		t.Fatalf("GzipConfig round trip: %q, %v", text, err)
	}
}
