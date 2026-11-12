package readonly

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Two transports guard every request before the network (ADM-02, E-224):
// readOnlyTransport passes only GET without a body to the base URL,
// authTransport only the configured login request to the auth URL. Both
// wrap the transport of an egress client of their own (SSRF policy, DNS
// pinning); the client and the authenticator never share one.

// origin is scheme, host and port of a URL, the port made explicit.
type origin struct{ scheme, host, port string }

func originOf(u *url.URL) origin {
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return origin{scheme: scheme, host: strings.TrimSuffix(strings.ToLower(u.Hostname()), "."), port: port}
}

// hasBody reports whether a request carries a body.
func hasBody(req *http.Request) bool {
	return (req.Body != nil && req.Body != http.NoBody) || req.ContentLength > 0 || len(req.TransferEncoding) > 0
}

// cleanBelow reports whether the path p (decoded) stays at or below
// prefix: no dot segment, and the cleaned path starts with the prefix.
func cleanBelow(p, prefix string) bool {
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." {
			return false
		}
	}
	cleaned := path.Clean("/" + p)
	prefix = strings.TrimSuffix(prefix, "/")
	return prefix == "" || cleaned == prefix || strings.HasPrefix(cleaned, prefix+"/")
}

// readOnlyTransport passes only GET requests without a body to the origin
// of the base URL below its path prefix; everything else ends with
// ErrMethodNotAllowed or ErrDestinationNotAllowed without a network
// access, also a redirect hop to another host.
type readOnlyTransport struct {
	base   http.RoundTripper
	origin origin
	prefix string
}

func newReadOnlyTransport(base http.RoundTripper, baseURL *url.URL) *readOnlyTransport {
	return &readOnlyTransport{base: base, origin: originOf(baseURL), prefix: strings.TrimSuffix(baseURL.Path, "/")}
}

// RoundTrip implements http.RoundTripper.
func (t *readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet || hasBody(req) {
		closeBody(req)
		return nil, ErrMethodNotAllowed
	}
	if req.URL == nil || req.URL.User != nil || originOf(req.URL) != t.origin {
		return nil, ErrDestinationNotAllowed
	}
	// The raw path too: an encoded dot segment must not slip through.
	raw := req.URL.EscapedPath()
	decoded, err := url.PathUnescape(raw)
	if err != nil || !cleanBelow(req.URL.Path, t.prefix) || !cleanBelow(decoded, t.prefix) {
		return nil, ErrDestinationNotAllowed
	}
	return t.base.RoundTrip(req)
}

// authTransport passes exactly the login request: the configured method
// on exactly the auth URL (scheme, host, port, path and query). Every
// other request ends with ErrMethodNotAllowed or
// ErrDestinationNotAllowed without a network access.
type authTransport struct {
	base   http.RoundTripper
	method string
	url    *url.URL
}

// RoundTrip implements http.RoundTripper.
func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != t.method {
		closeBody(req)
		return nil, ErrMethodNotAllowed
	}
	if req.URL == nil || req.URL.User != nil || originOf(req.URL) != originOf(t.url) ||
		req.URL.EscapedPath() != t.url.EscapedPath() || req.URL.RawQuery != t.url.RawQuery {
		closeBody(req)
		return nil, ErrDestinationNotAllowed
	}
	return t.base.RoundTrip(req)
}

// closeBody closes the body of a refused request, as a RoundTripper must.
func closeBody(req *http.Request) {
	if req.Body != nil {
		_ = req.Body.Close()
	}
}
