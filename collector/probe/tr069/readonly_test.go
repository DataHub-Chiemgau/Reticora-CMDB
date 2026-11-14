package tr069

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// countingServer counts the requests that reach it.
func countingServer(t *testing.T, h http.HandlerFunc) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		if h != nil {
			h(w, r)
			return
		}
		_, _ = w.Write([]byte("[]"))
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

// TestACSClientOnlyGet covers WP-1018 AK1 and AK2 (PRT-15 readonly test):
// the client of the adapter refuses POST, PUT, PATCH and DELETE with
// ErrWriteMethod before the network, the fake server counts no request;
// GET passes.
func TestACSClientOnlyGet(t *testing.T) {
	srv, n := countingServer(t, nil)
	client := newReadOnlyClient(5*time.Second, plugins.SystemTLSConfig())
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead, http.MethodOptions} {
		req, err := http.NewRequestWithContext(context.Background(), method, srv.URL+"/devices/", strings.NewReader(`{"name":"reboot"}`))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, ErrWriteMethod) {
			t.Fatalf("%s: err = %v, want ErrWriteMethod", method, err)
		}
	}
	if got := n.Load(); got != 0 {
		t.Fatalf("%d write requests reached the server", got)
	}
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/devices/", http.NoBody)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if n.Load() != 1 {
		t.Fatalf("GET did not reach the server")
	}
	// Reader has no method besides ListDevices: the interface is the only
	// way the executor reaches an ACS.
	var r Reader = newGenieACS(mustURL(t, srv.URL), client, "")
	if _, err := r.ListDevices(context.Background(), Selector{}, nil, Page{Limit: 1}); err != nil {
		t.Fatal(err)
	}
}

// TestACSClientFollowsNoRedirect covers WP-1018 AK2: a 307 to another URL
// is not followed, the Authorization header never reaches the other
// server, and the adapter reports the status.
func TestACSClientFollowsNoRedirect(t *testing.T) {
	other, otherHits := countingServer(t, nil)
	srv, _ := countingServer(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.RequestURI(), http.StatusTemporaryRedirect)
	})
	client := newReadOnlyClient(5*time.Second, plugins.SystemTLSConfig())
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/devices/", http.NoBody)
	req.Header.Set("Authorization", "Bearer acs-token")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want 307", resp.StatusCode)
	}
	g := newGenieACS(mustURL(t, srv.URL), client, "Bearer acs-token")
	_, err = g.ListDevices(context.Background(), Selector{Serial: "x"}, nil, Page{Limit: 100})
	var status *StatusError
	if !errors.As(err, &status) || status.Code != http.StatusTemporaryRedirect {
		t.Fatalf("err = %v, want StatusError 307", err)
	}
	if got := otherHits.Load(); got != 0 {
		t.Fatalf("redirect target reached %d times", got)
	}
	if r := failure(context.Background(), err); r.Code != "status_mismatch" {
		t.Fatalf("result = %+v", r)
	}
}

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
