package config

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// fixture is one section of testdata/running-configs.txt: a running
// configuration of a platform with canary secrets (every value containing
// CANARY is a secret) and the expected masked_count.
type fixture struct {
	platform Platform
	masked   int
	text     string
}

var sectionHeader = regexp.MustCompile(`^=== (\S+) masked=(\d+) ===$`)

func loadFixtures(t *testing.T) []fixture {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "running-configs.txt"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck // read-only
	var out []fixture
	var b strings.Builder
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if m := sectionHeader.FindStringSubmatch(line); m != nil {
			if len(out) > 0 {
				out[len(out)-1].text = b.String()
			}
			b.Reset()
			n, _ := strconv.Atoi(m[2])
			out = append(out, fixture{platform: Platform(m[1]), masked: n})
			continue
		}
		b.WriteString(line)
		b.WriteString("\r\n") // devices answer with CRLF
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	if len(out) > 0 {
		out[len(out)-1].text = b.String()
	}
	if len(out) != len(Platforms) {
		t.Fatalf("fixtures for %d platforms, want %d", len(out), len(Platforms))
	}
	return out
}

// TestMaskPlatformFixtures covers WP-1053 AK3 (ALR-07): no canary value of
// any platform survives the masking, and masked_count is as expected.
func TestMaskPlatformFixtures(t *testing.T) {
	for _, fx := range loadFixtures(t) {
		t.Run(string(fx.platform), func(t *testing.T) {
			if !fx.platform.Valid() {
				t.Fatalf("unknown platform %q", fx.platform)
			}
			if !strings.Contains(fx.text, "CANARY") {
				t.Fatal("fixture without canary")
			}
			snap, err := Process(fx.platform, fx.text)
			if err != nil {
				t.Fatal(err)
			}
			for i, line := range strings.Split(snap.Text, "\n") {
				if strings.Contains(line, "CANARY") {
					t.Errorf("line %d keeps a secret: %q", i+1, line)
				}
			}
			if snap.MaskedCount != fx.masked {
				t.Errorf("masked_count = %d, want %d\n%s", snap.MaskedCount, fx.masked, snap.Text)
			}
			if strings.Contains(snap.Text, "\r") {
				t.Error("CR left after normalization")
			}
			if snap.SHA256 != Hash(snap.Text) || len(snap.SHA256) != 64 || snap.SizeBytes != len(snap.Text) {
				t.Errorf("hash or size wrong: %+v", snap)
			}
		})
	}
}

// TestMaskKeepsStructure covers WP-1053 AK3: masking keeps the number of
// lines, and every line without a secret is unchanged.
func TestMaskKeepsStructure(t *testing.T) {
	for _, fx := range loadFixtures(t) {
		t.Run(string(fx.platform), func(t *testing.T) {
			normalized := Normalize(fx.platform, fx.text)
			masked, _ := Mask(fx.platform, normalized)
			before, after := strings.Split(normalized, "\n"), strings.Split(masked, "\n")
			if len(before) != len(after) {
				t.Fatalf("lines %d → %d", len(before), len(after))
			}
			for i := range before {
				if !strings.Contains(before[i], "CANARY") && before[i] != after[i] {
					t.Errorf("line %d without a secret changed: %q → %q", i+1, before[i], after[i])
				}
				if strings.Contains(before[i], "CANARY") && !strings.Contains(after[i], Masked) {
					t.Errorf("line %d with a secret not masked: %q", i+1, after[i])
				}
			}
			for _, keep := range []string{"hostname", "sysname", "host-name", "identity"} {
				if strings.Contains(normalized, keep) && !strings.Contains(masked, keep) {
					t.Errorf("%s lost", keep)
				}
			}
		})
	}
}

// TestMaskPrivateKeyBlock covers WP-1053 AK3: a private key block keeps its
// BEGIN and END lines; every line between them is masked, the block
// counts once.
func TestMaskPrivateKeyBlock(t *testing.T) {
	text := strings.Join([]string{
		"hostname r1",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW",
		"QyNTUxOQAAACDzSECRETKEYMATERIAL",
		"-----END OPENSSH PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"MHcCAQEEIBsecretECkey",
		"-----END EC PRIVATE KEY-----",
		"-----BEGIN CERTIFICATE-----",
		"MIIBpublicCertificate",
		"-----END CERTIFICATE-----",
		"end",
	}, "\n")
	for _, p := range Platforms {
		masked, n := Mask(p, text)
		lines := strings.Split(masked, "\n")
		if n != 2 || len(lines) != 12 {
			t.Fatalf("%s: count %d, %d lines", p, n, len(lines))
		}
		if lines[1] != "-----BEGIN OPENSSH PRIVATE KEY-----" || lines[4] != "-----END OPENSSH PRIVATE KEY-----" ||
			lines[2] != Masked || lines[3] != Masked || lines[6] != Masked {
			t.Fatalf("%s: block = %q", p, lines)
		}
		if strings.Contains(masked, "SECRETKEY") || strings.Contains(masked, "secretECkey") {
			t.Fatalf("%s: key material left", p)
		}
		if lines[9] != "MIIBpublicCertificate" {
			t.Fatalf("%s: certificate masked", p)
		}
	}
}
