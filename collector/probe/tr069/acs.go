package tr069

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"time"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Reader is the read-only view of an ACS northbound API (E-192): it lists
// devices with their last inform and selected parameters. It has no
// method that writes, so an adapter behind it cannot create tasks, set
// parameters or trigger a connection request. Every adapter is built on
// newReadOnlyClient.
type Reader interface {
	// ListDevices returns one page of the devices matching selector with
	// the values of parameters (TR-069 paths such as
	// Device.DeviceInfo.UpTime).
	ListDevices(ctx context.Context, selector Selector, parameters []string, page Page) ([]Device, error)
}

// Selector selects devices by structured fields only; free query texts do
// not exist. Empty fields do not restrict.
type Selector struct {
	Serial       string `json:"serial,omitempty"`
	OUI          string `json:"oui,omitempty"`
	ProductClass string `json:"product_class,omitempty"`
	Tag          string `json:"tag,omitempty"`
}

// Page is a window of the device list.
type Page struct {
	Skip  int
	Limit int
}

// Device is one device of the ACS.
type Device struct {
	// ID is the device id of the ACS.
	ID string
	// Serial is the serial number (the ID if the ACS reports none).
	Serial string
	// LastInform is the time of the last inform (zero: none reported).
	LastInform time.Time
	// Values are the requested parameters the device reported, by path;
	// a value is a number or a string.
	Values map[string]any
}

// ErrWriteMethod is returned by the read-only client for any request whose
// method is not GET; the request never reaches the network.
var ErrWriteMethod = errors.New("tr069: only GET requests are allowed towards the ACS")

// readOnlyTransport refuses every method except GET before the network.
type readOnlyTransport struct {
	next http.RoundTripper
}

func (t readOnlyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method != http.MethodGet && req.Method != "" {
		if req.Body != nil {
			_ = req.Body.Close()
		}
		return nil, ErrWriteMethod
	}
	return t.next.RoundTrip(req)
}

// newReadOnlyClient returns the only HTTP client of the ACS adapters: the
// client of plugins.NewTLSClient (certificates verified with cfg, no
// redirects followed, so Authorization never reaches another URL) whose
// transport refuses every method except GET with ErrWriteMethod.
func newReadOnlyClient(timeout time.Duration, cfg *tls.Config) *http.Client {
	c := plugins.NewTLSClient(timeout, cfg)
	c.Transport = readOnlyTransport{next: c.Transport}
	return c
}
