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
			downloadURL := "http://127.0.0.1:1/binary"
			if tt.handler != nil {
				srv := httptest.NewServer(tt.handler)
				defer srv.Close()
				downloadURL = srv.URL + "/binary"
			}

			dir := t.TempDir()
			fakeExec := filepath.Join(dir, "edge-agent")
			if err := os.WriteFile(fakeExec, []byte("original-binary"), 0o755); err != nil {
				t.Fatal(err)
			}

			c := &Checker{
				executablePath: func() (string, error) { return fakeExec, nil },
			}
			err := c.Apply(context.Background(), ReleaseInfo{
				Version:     "v2.0.0",
				DownloadURL: downloadURL,
				Checksums:   tt.checksums,
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

	c := &Checker{executablePath: func() (string, error) { return fakeExec, nil }}
	err := c.Apply(ctx, ReleaseInfo{Version: "v2", DownloadURL: srv.URL + "/binary"})
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
