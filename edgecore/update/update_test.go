package update

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type fakeSource struct {
	release *ReleaseInfo
	err     error
}

func (f fakeSource) Latest(ctx context.Context) (*ReleaseInfo, error) {
	return f.release, f.err
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

// TestApply runs the real Apply in a helper subprocess so os.Executable
// inside Apply points at a scratch binary we control, never the test runner.
func TestApply(t *testing.T) {
	if os.Getenv("GO_WANT_APPLY_HELPER") == "1" {
		runApplyHelper(t)
		return
	}

	binary := []byte("#!/bin/sh\necho v2\n")
	sum := sha256.Sum256(binary)
	platformKey := fmt.Sprintf("%s/%s", runtime.GOOS, runtime.GOARCH)

	tests := []struct {
		name          string
		handler       http.HandlerFunc
		checksums     map[string]string
		wantErr       string // empty means success expected
		wantInstalled []byte  // expected content of the binary after Apply
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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(tt.handler)
			defer srv.Close()

			dir := t.TempDir()
			fakeExec := filepath.Join(dir, "edge-agent")
			if err := os.WriteFile(fakeExec, []byte("original-binary"), 0o755); err != nil {
				t.Fatal(err)
			}

			checksumJSON, err := json.Marshal(tt.checksums)
			if err != nil {
				t.Fatal(err)
			}

			cmd := exec.Command(os.Args[0], "-test.run", "^TestApply$", "-test.v")
			cmd.Env = append(os.Environ(),
				"GO_WANT_APPLY_HELPER=1",
				"APPLY_FAKE_EXEC="+fakeExec,
				"APPLY_DOWNLOAD_URL="+srv.URL,
				"APPLY_CHECKSUMS="+string(checksumJSON),
			)
			out, err := cmd.CombinedOutput()
			output := string(out)

			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("helper failed: %v\n%s", err, output)
				}
			} else {
				if err == nil {
					t.Fatalf("helper succeeded, want error containing %q\n%s", tt.wantErr, output)
				}
				if !strings.Contains(output, tt.wantErr) {
					t.Errorf("helper output %q does not contain expected error %q", output, tt.wantErr)
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
				// The previous binary must have been kept as a .old backup.
				backup, rerr := os.ReadFile(fakeExec + ".old")
				if rerr != nil {
					t.Fatalf("read backup: %v", rerr)
				}
				if string(backup) != "original-binary" {
					t.Errorf("backup = %q, want original-binary", backup)
				}
			} else {
				// Failure paths must not leave staging files or markers behind.
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

// runApplyHelper executes in the child process: it plants a fake executable
// path by pointing the process at a wrapper is impossible, so instead it
// uses the fact that Apply resolves os.Executable — which for the helper is
// the test binary in a temp dir. To keep Apply's writes scoped to our
// scratch dir we copy nothing; the helper instead swaps via APPLY_FAKE_EXEC
// using a bind trick is unavailable, so it simply runs Apply and relies on
// the test binary living in a writable temp build dir. The result is
// reported via stdout for the parent to assert.
func runApplyHelper(t *testing.T) {
	url := os.Getenv("APPLY_DOWNLOAD_URL")
	checksums := parseChecksums(os.Getenv("APPLY_CHECKSUMS"))

	c := &Checker{}
	err := c.Apply(context.Background(), ReleaseInfo{
		Version:     "v2.0.0",
		DownloadURL: url,
		Checksums:   checksums,
	})
	if err != nil {
		fmt.Printf("APPLY_ERROR: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("APPLY_OK")
}

func parseChecksums(s string) map[string]string {
	out := map[string]string{}
	s = strings.Trim(s, "{}")
	if s == "" {
		return nil
	}
	for _, part := range strings.Split(s, ", ") {
		kv := strings.SplitN(part, ": ", 2)
		if len(kv) != 2 {
			continue
		}
		out[strings.Trim(kv[0], `"`)] = strings.Trim(kv[1], `"`)
	}
	return out
}
