package signup

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestRouteThroughInternalURL: with an internal Keycloak URL the admin
// client sends token and admin requests for the public issuer host to the
// internal address, with Host and X-Forwarded-* of the public issuer. Before
// the fix a TLS origin answered POST /admin/realms/<realm>/users with 405
// from the reverse proxy, which does not expose /admin/ (WP-646).
func TestRouteThroughInternalURL(t *testing.T) {
	type seen struct{ path, host, fwdHost, fwdProto, fwdPort string }
	var got []seen
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, seen{r.URL.Path, r.Host, r.Header.Get("X-Forwarded-Host"),
			r.Header.Get("X-Forwarded-Proto"), r.Header.Get("X-Forwarded-Port")})
		switch {
		case strings.HasSuffix(r.URL.Path, "/protocol/openid-connect/token"):
			_, _ = w.Write([]byte(`{"access_token":"admin-token"}`))
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)

	issuer := "https://cmdb.example.org/realms/reticora"
	client, err := RouteThroughInternalURL(nil, issuer, srv.URL+"/")
	if err != nil {
		t.Fatalf("RouteThroughInternalURL: %v", err)
	}
	kc, err := NewKeycloakAccounts(issuer, "reticora-admin", "secret", client)
	if err != nil {
		t.Fatalf("NewKeycloakAccounts: %v", err)
	}
	if err := kc.DeleteAccount(t.Context(), "0a1b2c"); err != nil {
		t.Fatalf("DeleteAccount through the internal URL: %v", err)
	}
	want := []string{"/realms/reticora/protocol/openid-connect/token", "/admin/realms/reticora/users/0a1b2c"}
	if len(got) != len(want) {
		t.Fatalf("requests = %+v, want paths %v", got, want)
	}
	for i, s := range got {
		if s.path != want[i] || s.host != "cmdb.example.org" || s.fwdHost != "cmdb.example.org" ||
			s.fwdProto != "https" || s.fwdPort != "" {
			t.Errorf("request %d = %+v, want path %s with the public host and proto https", i, s, want[i])
		}
	}
}

func TestRouteThroughInternalURLKeepsPortAndOtherHosts(t *testing.T) {
	var fwdPort string
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		fwdPort = r.Header.Get("X-Forwarded-Port")
	}))
	t.Cleanup(srv.Close)
	client, err := RouteThroughInternalURL(nil, "http://localhost:8180/realms/reticora", srv.URL)
	if err != nil {
		t.Fatalf("RouteThroughInternalURL: %v", err)
	}
	resp, err := client.Get("http://localhost:8180/realms/reticora")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	_ = resp.Body.Close()
	if hits != 1 || fwdPort != "8180" {
		t.Errorf("hits = %d, X-Forwarded-Port = %q, want 1 and 8180", hits, fwdPort)
	}
	// Other hosts are not rewritten.
	resp, err = client.Get(srv.URL)
	if err != nil {
		t.Fatalf("get other host: %v", err)
	}
	_ = resp.Body.Close()
	if hits != 2 || fwdPort != "" {
		t.Errorf("other host: hits = %d, X-Forwarded-Port = %q, want 2 and empty", hits, fwdPort)
	}
}

func TestRouteThroughInternalURLRejectsInvalidURLs(t *testing.T) {
	for _, internal := range []string{"keycloak:8080", "ftp://keycloak", "http://keycloak:8080/auth", "http://"} {
		if _, err := RouteThroughInternalURL(nil, "https://cmdb.example.org/realms/reticora", internal); err == nil {
			t.Errorf("internal URL %q accepted", internal)
		}
	}
	client := &http.Client{}
	if got, err := RouteThroughInternalURL(client, "https://cmdb.example.org/realms/reticora", ""); err != nil || got != client {
		t.Errorf("empty internal URL: client changed (%v)", err)
	}
}
