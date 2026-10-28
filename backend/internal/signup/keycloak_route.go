package signup

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// RouteThroughInternalURL sends the admin client's requests for the public
// issuer host to internalURL (e.g. http://keycloak:8080) instead. The
// public reverse proxy deliberately does not expose /admin/ (COL-08), so a
// TLS origin answers the admin REST API with 405. Keycloak runs with
// KC_PROXY=edge and builds issuer, token iss and the links of action mails
// from Host and X-Forwarded-*; the rewritten requests carry the public
// values, so tokens and links stay those of the public issuer. An empty
// internalURL returns client unchanged.
func RouteThroughInternalURL(client *http.Client, issuerURL, internalURL string) (*http.Client, error) {
	if internalURL == "" {
		return client, nil
	}
	public, err := url.Parse(issuerURL)
	if err != nil || public.Host == "" {
		return nil, fmt.Errorf("keycloak: issuer %q is not an absolute URL", issuerURL)
	}
	internal, err := url.Parse(strings.TrimRight(internalURL, "/"))
	if err != nil || internal.Host == "" || (internal.Scheme != "http" && internal.Scheme != "https") ||
		(internal.Path != "" || internal.RawQuery != "") {
		return nil, fmt.Errorf("keycloak: internal URL %q must be of the form http(s)://host[:port]", internalURL)
	}
	if client == nil {
		client = &http.Client{}
	}
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	routed := *client
	routed.Transport = internalRoute{public: public, internal: internal, base: base}
	return &routed, nil
}

type internalRoute struct {
	public   *url.URL
	internal *url.URL
	base     http.RoundTripper
}

func (r internalRoute) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host != r.public.Host || req.URL.Scheme != r.public.Scheme {
		return r.base.RoundTrip(req)
	}
	out := req.Clone(req.Context())
	out.URL.Scheme = r.internal.Scheme
	out.URL.Host = r.internal.Host
	out.Host = r.public.Host
	out.Header.Set("X-Forwarded-Host", r.public.Host)
	out.Header.Set("X-Forwarded-Proto", r.public.Scheme)
	if port := r.public.Port(); port != "" {
		out.Header.Set("X-Forwarded-Port", port)
	} else {
		out.Header.Del("X-Forwarded-Port")
	}
	return r.base.RoundTrip(out)
}
