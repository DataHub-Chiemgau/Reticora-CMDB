package readonly

import (
	"net/http"
	"net/url"
	"path"
	"strings"
)

// Two transports guard every request before the network (ADM-02, E-224,
// E-248): readOnlyTransport passes only GET without a body on a path of the
// allowlist below the base URL, authTransport only the login request (POST
// on exactly base URL + /V1/user/authentication). Both wrap the transport
// of an egress client of their own (SSRF policy, DNS pinning); the client
// and the authenticator never share one.

// getRoute is a GET of the allowlist (E-248): a path template relative to
// the path prefix of the base URL, whose segments are literals or {name}
// for a decimal id, and the query parameters it may carry (each value a
// decimal id). A route without query takes none.
type getRoute struct {
	path  string
	query []string
}

// allowedGets is the allowlist: every method file adds the routes of its
// methods (allowGets), so a new endpoint does not change this file.
var allowedGets []getRoute

// allowGets adds routes to the allowlist; the method files call it in a
// package-level declaration.
func allowGets(routes ...getRoute) bool {
	allowedGets = append(allowedGets, routes...)
	return true
}

// decimalID reports whether s is a decimal id of the documentation:
// 1 to 16 digits without a leading zero.
func decimalID(s string) bool {
	if s == "" || len(s) > 16 || s[0] == '0' {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// matches reports whether the segments of a relative path fit the route.
func (r *getRoute) matches(segs []string) bool {
	tmpl := strings.Split(strings.TrimPrefix(r.path, "/"), "/")
	if len(tmpl) != len(segs) {
		return false
	}
	for i, t := range tmpl {
		if strings.HasPrefix(t, "{") && strings.HasSuffix(t, "}") {
			if !decimalID(segs[i]) {
				return false
			}
			continue
		}
		if t != segs[i] {
			return false
		}
	}
	return true
}

// queryAllowed reports whether the query of u holds only the parameters of
// the route, each once with a decimal id.
func (r *getRoute) queryAllowed(u *url.URL) bool {
	if u.RawQuery == "" {
		return !u.ForceQuery
	}
	if len(r.query) == 0 {
		return false
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return false
	}
	for name, values := range q {
		known := false
		for _, allowed := range r.query {
			known = known || allowed == name
		}
		if !known || len(values) != 1 || !decimalID(values[0]) {
			return false
		}
	}
	return true
}

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
	// routes is the allowlist (allowedGets).
	routes []getRoute
}

func newReadOnlyTransport(base http.RoundTripper, baseURL *url.URL) *readOnlyTransport {
	return &readOnlyTransport{base: base, origin: originOf(baseURL), prefix: strings.TrimSuffix(baseURL.Path, "/"),
		routes: append([]getRoute(nil), allowedGets...)}
}

// allowed reports whether u is a GET route of the allowlist below the
// prefix: no escaped character in the path, the segments and the query of
// one route.
func (t *readOnlyTransport) allowed(u *url.URL) bool {
	if u.RawPath != "" && u.RawPath != u.Path {
		return false
	}
	rel, ok := strings.CutPrefix(u.Path, t.prefix)
	if !ok || !strings.HasPrefix(rel, "/") {
		return false
	}
	segs := strings.Split(strings.TrimPrefix(rel, "/"), "/")
	for i := range t.routes {
		if t.routes[i].matches(segs) && t.routes[i].queryAllowed(u) {
			return true
		}
	}
	return false
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
	if !t.allowed(req.URL) {
		return nil, ErrPathNotAllowed
	}
	return t.base.RoundTrip(req)
}

// authTransport passes exactly the login request: its method (authMethod
// of auth.go) on exactly the login URL (scheme, host, port, path and
// query). Every other request ends with ErrMethodNotAllowed or
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
