package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type fakeSource struct {
	release *ReleaseInfo
	err     error
}

func (f fakeSource) Latest(ctx context.Context) (*ReleaseInfo, error) {
	return f.release, f.err
}

// fakeVerifier records the verified artifact and returns err.
type fakeVerifier struct {
	err      error
	artifact *string
}

func (f fakeVerifier) Verify(_ context.Context, artifactPath, bundlePath string) error {
	if f.artifact != nil {
		data, _ := os.ReadFile(artifactPath)
		*f.artifact = string(data)
	}
	if _, err := os.Stat(bundlePath); err != nil {
		return fmt.Errorf("bundle missing: %w", err)
	}
	return f.err
}

func TestCheckReturnsNilWhenUpToDate(t *testing.T) {
	c := &Checker{Source: fakeSource{release: &ReleaseInfo{Version: "v1.2.3"}}}

	rel, err := c.Check(context.Background(), "v1.2.3")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel != nil {
		t.Errorf("Check returned %+v, want nil when versions match", rel)
	}
}

func TestCheckReturnsNewerRelease(t *testing.T) {
	want := &ReleaseInfo{Version: "v2.0.0", DownloadURL: "https://example.invalid/bin"}
	c := &Checker{Source: fakeSource{release: want}}

	rel, err := c.Check(context.Background(), "v1.0.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rel != want {
		t.Errorf("Check = %+v, want the latest release %+v", rel, want)
	}
}

func TestCheckNilSource(t *testing.T) {
	c := &Checker{}
	if _, err := c.Check(context.Background(), "v1"); err == nil {
		t.Fatal("expected error for nil source, got nil")
	}
}

func TestCheckSourceErrorPropagates(t *testing.T) {
	c := &Checker{Source: fakeSource{err: errors.New("network down")}}

	_, err := c.Check(context.Background(), "v1")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "network down") {
		t.Errorf("error should wrap source failure, got: %v", err)
	}
}

func TestCheckEdgeCasesReturnNil(t *testing.T) {
	tests := []struct {
		name    string
		release *ReleaseInfo
	}{
		{"nil release", nil},
		{"empty version", &ReleaseInfo{Version: ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &Checker{Source: fakeSource{release: tt.release}}
			rel, err := c.Check(context.Background(), "v1")
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if rel != nil {
				t.Errorf("Check = %+v, want nil for %s", rel, tt.name)
			}
		})
	}
}

func TestApplyRequiresDownloadURL(t *testing.T) {
	c := &Checker{}
	err := c.Apply(context.Background(), ReleaseInfo{Version: "v2"})
	if err == nil {
		t.Fatal("expected error for missing download URL, got nil")
	}
	if !strings.Contains(err.Error(), "download URL is required") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestApply drives Apply against a fake executable in a temp dir via the
// package-internal executablePath seam. Each case serves a binary over
// httptest and asserts the swap/backup/marker or failure-cleanup behavior.
func TestApply(t *testing.T) {
	binary := []byte("#!/bin/sh\necho v2\n")
	sum := sha256.Sum256(binary)
	platformKey := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)

	tests := []struct {
		name          string
		handler       http.HandlerFunc
		checksums     map[string]string
		wantErr       string // empty means success expected
		wantInstalled []byte // expected content of the binary after Apply
		wantMarker    string // expected .update-applied content (success only)
	}{
		{
			name: "success with platform checksum",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write(binary)
			},
			checksums:     map[string]string{platformKey: hex.EncodeToString(sum[:])},
			wantInstalled: binary,
			wantMarker:    "v2.0.0",
		},
		{
			name: "success with generic sha256 checksum",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write(binary)
			},
			checksums:     map[string]string{"sha256": hex.EncodeToString(sum[:])},
			wantInstalled: binary,
			wantMarker:    "v2.0.0",
		},
		{
			name: "success without checksums",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write(binary)
			},
			wantInstalled: binary,
			wantMarker:    "v2.0.0",
		},
		{
			name: "checksum mismatch keeps original",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Write(binary)
			},
			checksums:     map[string]string{platformKey: strings.Repeat("0", 64)},
			wantErr:       "checksum mismatch",
			wantInstalled: []byte("original-binary"),
		},
		{
			name: "download 404 keeps original",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantErr:       "status 404",
			wantInstalled: []byte("original-binary"),
		},
		{
			name:          "unreachable server keeps original",
			handler:       nil, // no server; port 1 is never listening
			wantErr:       "download release",
			wantInstalled: []byte("original-binary"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			downloadURL, bundleURL := "http://127.0.0.1:1/binary", "http://127.0.0.1:1/bundle"
			if tt.handler != nil {
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == "/bundle" {
						_, _ = w.Write([]byte(`{"mediaType":"application/vnd.dev.sigstore.bundle+json"}`))
						return
					}
					tt.handler(w, r)
				}))
				defer srv.Close()
				downloadURL, bundleURL = srv.URL+"/binary", srv.URL+"/bundle"
			}

			dir := t.TempDir()
			fakeExec := filepath.Join(dir, "edge-agent")
			if err := os.WriteFile(fakeExec, []byte("original-binary"), 0o755); err != nil {
				t.Fatal(err)
			}

			c := &Checker{
				Verifier:       fakeVerifier{},
				executablePath: func() (string, error) { return fakeExec, nil },
			}
			err := c.Apply(context.Background(), ReleaseInfo{
				Version:            "v2.0.0",
				DownloadURL:        downloadURL,
				SignatureBundleURL: bundleURL,
				Checksums:          tt.checksums,
			})

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Apply: %v", err)
				}
			} else {
				if err == nil {
					t.Fatalf("Apply succeeded, want error containing %q", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("Apply error = %q, want it to contain %q", err, tt.wantErr)
				}
			}

			installed, rerr := os.ReadFile(fakeExec)
			if rerr != nil {
				t.Fatalf("read installed binary: %v", rerr)
			}
			if string(installed) != string(tt.wantInstalled) {
				t.Errorf("installed content = %q, want %q", installed, tt.wantInstalled)
			}

			marker := filepath.Join(dir, ".update-applied")
			if tt.wantMarker != "" {
				data, rerr := os.ReadFile(marker)
				if rerr != nil {
					t.Fatalf("read version marker: %v", rerr)
				}
				if string(data) != tt.wantMarker {
					t.Errorf("marker = %q, want %q", data, tt.wantMarker)
				}
				// The previous binary must have been kept as a .old backup
				// so the service manager can roll back.
				backup, rerr := os.ReadFile(fakeExec + ".old")
				if rerr != nil {
					t.Fatalf("read backup: %v", rerr)
				}
				if string(backup) != "original-binary" {
					t.Errorf("backup = %q, want original-binary", backup)
				}
			} else {
				// Failure paths must not leave markers or staging files behind.
				if _, rerr := os.Stat(marker); !os.IsNotExist(rerr) {
					t.Errorf("marker should not exist on failure, stat err = %v", rerr)
				}
				if _, rerr := os.Stat(fakeExec + ".update"); !os.IsNotExist(rerr) {
					t.Errorf("staging file should be cleaned up on failure, stat err = %v", rerr)
				}
			}
		})
	}
}

func TestApplyRespectsContextCancellation(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
	}))
	defer srv.Close()
	defer close(unblock)

	dir := t.TempDir()
	fakeExec := filepath.Join(dir, "edge-agent")
	if err := os.WriteFile(fakeExec, []byte("original-binary"), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	c := &Checker{Verifier: fakeVerifier{}, executablePath: func() (string, error) { return fakeExec, nil }}
	err := c.Apply(ctx, ReleaseInfo{Version: "v2", DownloadURL: srv.URL + "/binary", SignatureBundleURL: srv.URL + "/bundle"})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}

	// Original binary must remain untouched.
	installed, rerr := os.ReadFile(fakeExec)
	if rerr != nil {
		t.Fatalf("read binary: %v", rerr)
	}
	if string(installed) != "original-binary" {
		t.Errorf("installed content = %q, want original-binary", installed)
	}
}

// TestApplyRefusesUnsignedAndWronglySignedReleases covers WP-102 (TEC-15,
// COL-06): an update is installed only after its signature verified; a
// release without a bundle, an unreachable bundle, a failing verification
// or a missing verifier leave the running binary untouched.
func TestApplyRefusesUnsignedAndWronglySignedReleases(t *testing.T) {
	binary := []byte("#!/bin/sh\necho v3\n")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/binary":
			_, _ = w.Write(binary)
		case "/bundle":
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	var verified string
	tests := []struct {
		name     string
		verifier Verifier
		bundle   string
		wantErr  string
	}{
		{"unsigned release", fakeVerifier{}, "", "unsigned updates are refused"},
		{"no verifier", nil, srv.URL + "/bundle", "signature verifier is required"},
		{"bundle not found", fakeVerifier{}, srv.URL + "/missing", "signature bundle"},
		{"wrong signature", fakeVerifier{err: errors.New("none of the expected identities matched")}, srv.URL + "/bundle", "signature verification failed, update refused"},
		{"valid signature", fakeVerifier{artifact: &verified}, srv.URL + "/bundle", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			fakeExec := filepath.Join(dir, "reticora-collector")
			if err := os.WriteFile(fakeExec, []byte("original-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			c := &Checker{Verifier: tt.verifier, executablePath: func() (string, error) { return fakeExec, nil }}
			err := c.Apply(context.Background(), ReleaseInfo{Version: "v3.0.0", DownloadURL: srv.URL + "/binary", SignatureBundleURL: tt.bundle})
			installed, _ := os.ReadFile(fakeExec)
			if tt.wantErr == "" {
				if err != nil || string(installed) != string(binary) || verified != string(binary) {
					t.Fatalf("valid signature: %v, installed %q, verified %q", err, installed, verified)
				}
			} else {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Apply error = %v, want %q", err, tt.wantErr)
				}
				if string(installed) != "original-binary" {
					t.Errorf("installed content = %q, want the original", installed)
				}
			}
			for _, leftover := range []string{fakeExec + ".update", fakeExec + ".update.sigstore.json"} {
				if tt.wantErr != "" || strings.HasSuffix(leftover, ".json") {
					if _, serr := os.Stat(leftover); !os.IsNotExist(serr) {
						t.Errorf("%s left behind", filepath.Base(leftover))
					}
				}
			}
		})
	}
}

// TestCosignVerifierPinsTheReleaseIdentity: the CLI call verifies the
// bundle against the release workflow identity and the GitHub OIDC issuer;
// a failing cosign refuses with its output.
func TestCosignVerifierPinsTheReleaseIdentity(t *testing.T) {
	var got []string
	v := CosignVerifier{run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		got = append([]string{name}, args...)
		return nil, nil
	}}
	if err := v.Verify(context.Background(), "/tmp/bin", "/tmp/bin.sigstore.json"); err != nil {
		t.Fatal(err)
	}
	want := []string{"cosign", "verify-blob", "--bundle", "/tmp/bin.sigstore.json",
		"--certificate-identity-regexp", ReleaseIdentityRegexp,
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com", "/tmp/bin"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("cosign call:\n got %v\nwant %v", got, want)
	}
	if !strings.Contains(ReleaseIdentityRegexp, `release\.yml@refs/tags/v`) {
		t.Errorf("identity does not pin the release workflow: %s", ReleaseIdentityRegexp)
	}

	v.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("error: no matching signatures\n"), errors.New("exit status 1")
	}
	if err := v.Verify(context.Background(), "a", "b"); err == nil || !strings.Contains(err.Error(), "no matching signatures") {
		t.Errorf("failing cosign: %v", err)
	}
}

func TestCompareVersions(t *testing.T) {
	tests := []struct {
		a, b string
		want int
		ok   bool
	}{
		{"v1.2.3", "v1.2.3", 0, true},
		{"v1.10.0", "v1.9.9", 1, true},
		{"1.2.3", "v1.2.4", -1, true},
		{"v2.0.0-beta.1", "v2.0.0", -1, true},
		{"v2.0.0-beta.2", "v2.0.0-beta.10", -1, true},
		{"v2.0.0-beta", "v2.0.0-alpha", 1, true},
		{"v2.0.0-beta.1", "v2.0.0-beta", 1, true},
		{"v1.0.0+build.5", "v1.0.0", 0, true},
		{"dev", "v1.0.0", 0, false},
		{"v1.0", "v1.0.0", 0, false},
	}
	for _, tt := range tests {
		got, ok := CompareVersions(tt.a, tt.b)
		if got != tt.want || ok != tt.ok {
			t.Errorf("CompareVersions(%q, %q) = %d, %v; want %d, %v", tt.a, tt.b, got, ok, tt.want, tt.ok)
		}
	}
}

func TestChannelAccepts(t *testing.T) {
	tests := []struct {
		sub, rel string
		want     bool
	}{
		{ChannelStable, ChannelStable, true},
		{ChannelStable, "", true},
		{ChannelStable, ChannelBeta, false},
		{ChannelBeta, ChannelBeta, true},
		{ChannelBeta, ChannelStable, true},
		{ChannelStable, "nightly", false},
	}
	for _, tt := range tests {
		if got := ChannelAccepts(tt.sub, tt.rel); got != tt.want {
			t.Errorf("ChannelAccepts(%q, %q) = %v, want %v", tt.sub, tt.rel, got, tt.want)
		}
	}
}

func TestCheckHonoursChannelVersionAndRollback(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "collector")
	check := func(channel, current string, rel *ReleaseInfo) (*ReleaseInfo, error) {
		c := &Checker{Source: fakeSource{release: rel}, Channel: channel, executablePath: func() (string, error) { return execPath, nil }}
		return c.Check(context.Background(), current)
	}

	if _, err := check(ChannelStable, "v1.0.0", &ReleaseInfo{Version: "v1.1.0-beta.1", Channel: ChannelBeta}); !errors.Is(err, ErrChannelMismatch) {
		t.Fatalf("beta release on stable channel: err = %v, want ErrChannelMismatch", err)
	}
	if got, err := check(ChannelBeta, "v1.0.0", &ReleaseInfo{Version: "v1.1.0-beta.1", Channel: ChannelBeta}); err != nil || got == nil {
		t.Fatalf("beta release on beta channel = %v, %v", got, err)
	}
	if got, err := check(ChannelStable, "v1.2.0", &ReleaseInfo{Version: "v1.1.0", Channel: ChannelStable}); err != nil || got != nil {
		t.Fatalf("older release must not be installed: %v, %v", got, err)
	}
	if got, err := check(ChannelStable, "dev", &ReleaseInfo{Version: "v1.1.0", Channel: ChannelStable}); err != nil || got == nil {
		t.Fatalf("development build must update: %v, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(dir, rolledBackFile), []byte("v1.1.0"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := check(ChannelStable, "v1.0.0", &ReleaseInfo{Version: "v1.1.0", Channel: ChannelStable}); err != nil || got != nil {
		t.Fatalf("rolled-back release must be skipped: %v, %v", got, err)
	}
	if got, err := check(ChannelStable, "v1.0.0", &ReleaseInfo{Version: "v1.1.1", Channel: ChannelStable}); err != nil || got == nil {
		t.Fatalf("the next release after a rollback must install: %v, %v", got, err)
	}
}

func TestHTTPSourceReadsTheChannelManifest(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if strings.Contains(r.URL.Path, "/beta/") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"version":"v1.4.0","channel":"stable","downloadUrl":"https://x/bin","signatureBundleUrl":"https://x/bin.sigstore.json"}`))
	}))
	defer srv.Close()

	src := &HTTPSource{BaseURL: srv.URL + "/", Channel: ChannelStable, Artifact: "reticora-collector"}
	rel, err := src.Latest(context.Background())
	if err != nil || rel == nil || rel.Version != "v1.4.0" || rel.SignatureBundleURL == "" {
		t.Fatalf("Latest = %+v, %v", rel, err)
	}
	if want := "/stable/reticora-collector-" + runtime.GOOS + "-" + runtime.GOARCH + ".release.json"; path != want {
		t.Errorf("manifest path = %q, want %q", path, want)
	}
	src.Channel = ChannelBeta
	if rel, err = src.Latest(context.Background()); err != nil || rel != nil {
		t.Errorf("missing manifest = %+v, %v; want no release", rel, err)
	}
	src.Channel = "nightly"
	if _, err = src.Latest(context.Background()); err == nil {
		t.Error("an unknown channel must be refused")
	}
}

// serveRelease serves binary and a signature bundle.
func serveRelease(t *testing.T, binary []byte) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bundle" {
			_, _ = w.Write([]byte(`{}`))
			return
		}
		_, _ = w.Write(binary)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFailedStartsRollBackTheUpdate(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "collector")
	if err := os.WriteFile(execPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := serveRelease(t, []byte("new-binary"))
	c := &Checker{Verifier: fakeVerifier{}, executablePath: func() (string, error) { return execPath, nil }}
	if err := c.Apply(context.Background(), ReleaseInfo{Version: "v2.0.0", DownloadURL: srv.URL + "/bin", SignatureBundleURL: srv.URL + "/bundle"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The new binary starts MaxStartAttempts times without confirming.
	for i := 1; i <= MaxStartAttempts; i++ {
		if v, err := CheckPending(execPath, 0); err != nil || v != "v2.0.0" {
			t.Fatalf("start %d: CheckPending = %q, %v", i, v, err)
		}
	}
	if _, err := CheckPending(execPath, 0); !errors.Is(err, ErrRolledBack) {
		t.Fatalf("start %d: err = %v, want ErrRolledBack", MaxStartAttempts+1, err)
	}
	if got, _ := os.ReadFile(execPath); string(got) != "old-binary" {
		t.Errorf("after rollback the binary is %q, want the previous one", got)
	}
	if got, _ := os.ReadFile(execPath + ".failed"); string(got) != "new-binary" {
		t.Errorf("failed binary kept as %q", got)
	}
	if RolledBackVersion(execPath) != "v2.0.0" {
		t.Errorf("rolled-back version = %q", RolledBackVersion(execPath))
	}
	// The restored binary starts normally.
	if v, err := CheckPending(execPath, 0); err != nil || v != "" {
		t.Errorf("after rollback CheckPending = %q, %v", v, err)
	}
}

func TestConfirmKeepsTheUpdate(t *testing.T) {
	dir := t.TempDir()
	execPath := filepath.Join(dir, "collector")
	if err := os.WriteFile(execPath, []byte("old-binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	srv := serveRelease(t, []byte("new-binary"))
	c := &Checker{Verifier: fakeVerifier{}, executablePath: func() (string, error) { return execPath, nil }}
	if err := c.Apply(context.Background(), ReleaseInfo{Version: "v2.0.0", DownloadURL: srv.URL + "/bin", SignatureBundleURL: srv.URL + "/bundle"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := CheckPending(execPath, 0); err != nil {
		t.Fatal(err)
	}
	if err := Confirm(execPath); err != nil {
		t.Fatal(err)
	}
	for range MaxStartAttempts + 2 {
		if v, err := CheckPending(execPath, 0); err != nil || v != "" {
			t.Fatalf("a confirmed update must stay: %q, %v", v, err)
		}
	}
	if got, _ := os.ReadFile(execPath); string(got) != "new-binary" {
		t.Errorf("binary = %q, want the update", got)
	}
}

func TestApplyRefusesABinaryThatDoesNotStart(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts as test binaries need a POSIX shell")
	}
	tests := []struct {
		name    string
		binary  string
		wantErr string
	}{
		{"exits with an error", "#!/bin/sh\nexit 3\n", "does not start"},
		{"reports another version", "#!/bin/sh\necho v1.9.0\n", "instead of version"},
		{"starts", "#!/bin/sh\necho reticora-collector v2.0.0\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			execPath := filepath.Join(dir, "collector")
			if err := os.WriteFile(execPath, []byte("old-binary"), 0o755); err != nil {
				t.Fatal(err)
			}
			srv := serveRelease(t, []byte(tt.binary))
			c := &Checker{Verifier: fakeVerifier{}, ProbeArgs: []string{"version"}, executablePath: func() (string, error) { return execPath, nil }}
			err := c.Apply(context.Background(), ReleaseInfo{Version: "v2.0.0", DownloadURL: srv.URL + "/bin", SignatureBundleURL: srv.URL + "/bundle"})
			got, _ := os.ReadFile(execPath)
			if tt.wantErr == "" {
				if err != nil || string(got) != tt.binary {
					t.Fatalf("Apply = %v, installed %q", err, got)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Apply = %v, want %q", err, tt.wantErr)
			}
			if string(got) != "old-binary" {
				t.Errorf("a refused update replaced the binary: %q", got)
			}
		})
	}
}
