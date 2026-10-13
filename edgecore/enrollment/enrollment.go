// Package enrollment implements the collector enrollment protocol of the
// edge components (INS-06, COL-04): a one-time enrollment code is redeemed
// at POST /api/v1/collectors/enroll with a CSR of a locally generated key;
// the issued identity is stored in one credentials file.
package enrollment

import (
	"context"
	"net"
	"net/http"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/edgecore/transport"
)

// DefaultCredentialsPath is the one place a collector keeps its identity:
// id, certificate, key, CA bundle and the server URL
// (RETICORA_CREDENTIALS_PATH overrides it).
const DefaultCredentialsPath = "/var/lib/reticora-collector/credentials.json"

// EnrollPath is the enrollment endpoint of the collector API.
const EnrollPath = "/api/v1/collectors/enroll"

// HTTPSTransport is the transport of the enrollment request: plain HTTPS
// against the server certificate (the collector has no client certificate
// yet).
type HTTPSTransport struct{ Client *http.Client }

// Do implements transport.Transport.
func (h HTTPSTransport) Do(req *http.Request) (*http.Response, error) { return h.client().Do(req) }

// DialContext implements transport.Transport.
func (h HTTPSTransport) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

// HTTPClient implements transport.Transport.
func (h HTTPSTransport) HTTPClient() *http.Client { return h.client() }

func (h HTTPSTransport) client() *http.Client {
	if h.Client != nil {
		return h.Client
	}
	return http.DefaultClient
}

var _ transport.Transport = HTTPSTransport{}
