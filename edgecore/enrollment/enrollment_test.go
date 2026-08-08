package enrollment

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/transport"
)

// httpTransport adapts a plain *http.Client to the transport.Transport
// interface so tests can hit httptest servers without mTLS material.
type httpTransport struct{ client *http.Client }

func (h httpTransport) Do(req *http.Request) (*http.Response, error) { return h.client.Do(req) }

func (h httpTransport) DialContext(ctx context.Context, network, addr string) (conn net.Conn, err error) {
	d := &net.Dialer{}
	return d.DialContext(ctx, network, addr)
}

func (h httpTransport) HTTPClient() *http.Client { return h.client }

func testClient(t *testing.T, handler http.HandlerFunc) (*HTTPClient, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &HTTPClient{
		BaseURL:   srv.URL,
		Transport: httpTransport{client: srv.Client()},
	}, srv
}

func TestEnrollSuccess(t *testing.T) {
	expires := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
	var gotReq EnrollmentRequest
	var gotContentType string
	var gotMethod, gotPath string

	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Errorf("decode request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(EnrollmentResponse{
			DeviceID:             "edge-01",
			ClientCertificatePEM: "cert-pem",
			ClientPrivateKeyPEM:  "key-pem",
			CertificateAuthority: "ca-pem",
			AssignedLabels:       map[string]string{"role": "collector"},
			ExpiresAt:            expires,
		})
	})

	resp, err := client.Enroll(context.Background(), EnrollmentRequest{
		DeviceID:        "edge-01",
		EnrollmentToken: "tok",
		Hostname:        "pi-01",
		Site:            "munich",
		Labels:          map[string]string{"env": "prod"},
		Capabilities:    []string{"metrics"},
		CSRPEM:          "csr",
	})
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if gotPath != "/api/v1/enrollment" {
		t.Errorf("path = %q, want default endpoint /api/v1/enrollment", gotPath)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	if gotReq.DeviceID != "edge-01" || gotReq.EnrollmentToken != "tok" || gotReq.Site != "munich" {
		t.Errorf("server received wrong request body: %+v", gotReq)
	}
	if gotReq.Labels["env"] != "prod" || len(gotReq.Capabilities) != 1 {
		t.Errorf("labels/capabilities not transmitted: %+v", gotReq)
	}

	if resp.DeviceID != "edge-01" || resp.ClientCertificatePEM != "cert-pem" || resp.ClientPrivateKeyPEM != "key-pem" {
		t.Errorf("unexpected response: %+v", resp)
	}
	if resp.CertificateAuthority != "ca-pem" {
		t.Errorf("CertificateAuthority = %q, want ca-pem", resp.CertificateAuthority)
	}
	if resp.AssignedLabels["role"] != "collector" {
		t.Errorf("AssignedLabels[role] = %q, want collector", resp.AssignedLabels["role"])
	}
	if !resp.ExpiresAt.Equal(expires) {
		t.Errorf("ExpiresAt = %v, want %v", resp.ExpiresAt, expires)
	}
}

func TestEnrollCustomEndpointAndBaseURLTrailingSlash(t *testing.T) {
	var gotPath string
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		json.NewEncoder(w).Encode(EnrollmentResponse{DeviceID: "d"})
	})
	client.Endpoint = "/custom/enroll"
	client.BaseURL += "/" // trailing slash must not produce "//custom/enroll"

	if _, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if gotPath != "/custom/enroll" {
		t.Errorf("path = %q, want /custom/enroll", gotPath)
	}
}

func TestEnrollServerErrorIncludesStatusAndBody(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "token expired", http.StatusUnauthorized)
	})

	_, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected error on 401, got nil")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should include status, got: %v", err)
	}
	if !strings.Contains(err.Error(), "token expired") {
		t.Errorf("error should include server body, got: %v", err)
	}
}

func TestEnrollInvalidJSONResponse(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		io.WriteString(w, "{not json")
	})

	_, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected decode error, got nil")
	}
	if !strings.Contains(err.Error(), "decode") {
		t.Errorf("error should mention decode failure, got: %v", err)
	}
}

func TestEnrollNilTransport(t *testing.T) {
	client := &HTTPClient{BaseURL: "https://example.invalid"}

	_, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected error for nil transport, got nil")
	}
	if !strings.Contains(err.Error(), "transport is required") {
		t.Errorf("error should mention missing transport, got: %v", err)
	}
}

type failingTransport struct{ err error }

func (f failingTransport) Do(req *http.Request) (*http.Response, error) { return nil, f.err }
func (f failingTransport) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return nil, f.err
}
func (f failingTransport) HTTPClient() *http.Client { return nil }

func TestEnrollTransportFailure(t *testing.T) {
	client := &HTTPClient{
		BaseURL:   "https://control-plane.invalid",
		Transport: failingTransport{err: errors.New("connection refused")},
	}

	_, err := client.Enroll(context.Background(), EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected error for transport failure, got nil")
	}
	if !strings.Contains(err.Error(), "send request") {
		t.Errorf("error should wrap send failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "connection refused") {
		t.Errorf("error should preserve underlying cause, got: %v", err)
	}
}

func TestEnrollRespectsContextCancellation(t *testing.T) {
	// Server blocks until the client goes away.
	unblock := make(chan struct{})
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		<-unblock
	})
	t.Cleanup(func() { close(unblock) })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := client.Enroll(ctx, EnrollmentRequest{DeviceID: "d"})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("error should wrap context.DeadlineExceeded, got: %v", err)
	}
}

func TestEnrollRetryOnTransientFailure(t *testing.T) {
	// The client itself does not retry; verify it surfaces a transient 503
	// accurately so a caller's retry loop can react, and that a subsequent
	// attempt against the recovered server succeeds.
	var attempts int
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		attempts++
		if attempts == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			io.WriteString(w, "try again later")
			return
		}
		json.NewEncoder(w).Encode(EnrollmentResponse{DeviceID: "d"})
	})

	ctx := context.Background()
	_, err := client.Enroll(ctx, EnrollmentRequest{DeviceID: "d"})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("first attempt should surface 503, got: %v", err)
	}

	resp, err := client.Enroll(ctx, EnrollmentRequest{DeviceID: "d"})
	if err != nil {
		t.Fatalf("retry after transient failure: %v", err)
	}
	if resp.DeviceID != "d" {
		t.Errorf("DeviceID = %q, want d", resp.DeviceID)
	}
	if attempts != 2 {
		t.Errorf("server saw %d attempts, want 2", attempts)
	}
}

func TestHTTPClientImplementsClientInterface(t *testing.T) {
	var _ Client = (*HTTPClient)(nil)
}

// Ensure the test helper transport satisfies the shared interface.
var _ transport.Transport = httpTransport{}
var _ transport.Transport = failingTransport{}
