package main

import (
	"bytes"
	"io"
	"net"
	"net/http"
	"testing"
)

// TestProfilingListener covers WP-438 (NFR-02): the profiling listener
// serves the CPU profile and the named profiles of the server on a loopback
// address, not the command line and nothing of the API, and refuses an
// address that is not a loopback address.
func TestProfilingListener(t *testing.T) {
	srv, addr, err := startProfiling("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	if tcp, ok := addr.(*net.TCPAddr); !ok || !tcp.IP.IsLoopback() {
		t.Fatalf("listener on %v, want a loopback address", addr)
	}
	get := func(path string) (int, []byte) {
		t.Helper()
		res, gerr := http.Get("http://" + addr.String() + path)
		if gerr != nil {
			t.Fatal(gerr)
		}
		defer res.Body.Close()
		body, rerr := io.ReadAll(res.Body)
		if rerr != nil {
			t.Fatal(rerr)
		}
		return res.StatusCode, body
	}
	// A CPU profile is a gzip-compressed protobuf.
	if code, body := get("/debug/pprof/profile?seconds=1"); code != http.StatusOK || !bytes.HasPrefix(body, []byte{0x1f, 0x8b}) {
		t.Errorf("CPU profile: %d, %d bytes; want 200 with a gzip-compressed profile", code, len(body))
	}
	for _, path := range []string{"/debug/pprof/", "/debug/pprof/goroutine?debug=1", "/debug/pprof/heap"} {
		if code, _ := get(path); code != http.StatusOK {
			t.Errorf("GET %s: %d, want 200", path, code)
		}
	}
	for _, path := range []string{"/debug/pprof/cmdline", "/api/v1/cis", "/healthz", "/"} {
		if code, _ := get(path); code != http.StatusNotFound {
			t.Errorf("GET %s: %d, want 404", path, code)
		}
	}

	for _, bad := range []string{"0.0.0.0:0", "[::]:0"} {
		if s, a, berr := startProfiling(bad); berr == nil {
			_ = s.Close()
			t.Errorf("startProfiling(%q) listened on %v, want an error", bad, a)
		}
	}
}
