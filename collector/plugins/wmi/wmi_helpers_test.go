package wmi

// Helpers of the WMI tests. The fake WinRM endpoints listen on a random
// 127.x.y.z loopback address on the fixed WinRM ports so parallel runs do not
// collide. winRMFake (WP-415) speaks WS-Management as DSP0226 specifies.

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"
	"sync"
	"testing"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

const (
	auditUser     = `CORP\audit-admin`
	auditPassword = "S3cret!-audit-wmi"
	// plainWinRMPort is WinRM over plain HTTP; the plugin never uses it.
	plainWinRMPort = 5985
)

// auditListen binds port on a random 127.x.y.z address.
func auditListen(t *testing.T, port int) (net.Listener, string) {
	t.Helper()
	var lastErr error
	for range 20 {
		ip := fmt.Sprintf("127.%d.%d.%d", 1+rand.IntN(250), rand.IntN(250), 1+rand.IntN(250))
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, fmt.Sprint(port)))
		if err == nil {
			return ln, ip
		}
		lastErr = err
	}
	t.Fatalf("cannot bind a loopback address on port %d: %v", port, lastErr)
	return nil, ""
}

// serveWinRM serves handler on ln, over TLS with the self-signed httptest
// certificate when useTLS is set.
func serveWinRM(t *testing.T, ln net.Listener, handler http.Handler, useTLS bool) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(handler)
	_ = srv.Listener.Close()
	srv.Listener = ln
	// Rejected handshakes are expected; keep them out of the test output.
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	if useTLS {
		srv.StartTLS()
	} else {
		srv.Start()
	}
	t.Cleanup(srv.Close)
	return srv
}

// pinScope is the TLS scope of ip that pins the SPKI of cert.
func pinScope(t *testing.T, ip string, cert *x509.Certificate) plugins.TLSScope {
	t.Helper()
	_, network, err := net.ParseCIDR(ip + "/32")
	if err != nil {
		t.Fatal(err)
	}
	pin := sha256.Sum256(cert.RawSubjectPublicKeyInfo)
	return plugins.TLSScope{Network: network, PinsSHA256: [][]byte{pin[:]}}
}

// auditRecorder records every request a fake WinRM endpoint receives.
type auditRecorder struct {
	mu   sync.Mutex
	dump []string
}

func (r *auditRecorder) add(req *http.Request) {
	raw, _ := httputil.DumpRequest(req, true)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dump = append(r.dump, string(raw))
}

// count is the number of requests received.
func (r *auditRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.dump)
}

// leaked reports the requests that carry the password readable (plain text
// or as HTTP Basic credentials).
func (r *auditRecorder) leaked() []string {
	basic := base64.StdEncoding.EncodeToString([]byte(auditUser + ":" + auditPassword))
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, d := range r.dump {
		if strings.Contains(d, auditPassword) || strings.Contains(d, basic) {
			first, _, _ := strings.Cut(d, "\r\n")
			out = append(out, first)
		}
	}
	return out
}

const (
	cimv2URI = "http://schemas.microsoft.com/wbem/wsman/1/wmi/root/cimv2/"
	// fakeNamespaces declares the prefixes of the fake's envelopes.
	fakeNamespaces = `xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" ` +
		`xmlns:n="http://schemas.xmlsoap.org/ws/2004/09/enumeration" xmlns:w="http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd"`
)

// winRMFake is a protocol-faithful WinRM endpoint (DSP0226 8.2,
// WS-Enumeration 2004/09). Enumerate opens an enumeration context for the
// class of the ResourceURI. With OptimizeEnumeration – honoured when
// optimize is set, otherwise ignored as the specification allows – the first
// batch comes in the EnumerateResponse (w:Items); every further batch comes
// with Pull under a fresh context, the last one with EndOfSequence. Like
// WinRM it answers a request that is no SOAP envelope, lacks the MessageID,
// repeats one, is addressed elsewhere, names an unknown action or resource,
// or continues an unknown or used context with a SOAP fault (HTTP 500).
type winRMFake struct {
	// instances holds the instances of each class: the properties (p:
	// elements) of one instance per entry.
	instances map[string][]string
	// batch is the number of instances per response (at least 1).
	batch    int
	optimize bool
	// pullFault answers every Pull with this fault subcode.
	pullFault string
	// endless answers every Pull with an empty batch and a fresh context.
	endless bool

	mu         sync.Mutex
	seq        int
	contexts   map[string]fakeEnumeration
	messageIDs map[string]bool
	requests   []string
}

// fakeEnumeration is an open enumeration context.
type fakeEnumeration struct {
	class string
	pos   int
}

// fakeRequest is the part of a WS-Management request the fake checks, in
// the namespaces DSP0226 prescribes.
type fakeRequest struct {
	XMLName xml.Name `xml:"http://www.w3.org/2003/05/soap-envelope Envelope"`
	Header  struct {
		Action      string `xml:"http://schemas.xmlsoap.org/ws/2004/08/addressing Action"`
		MessageID   string `xml:"http://schemas.xmlsoap.org/ws/2004/08/addressing MessageID"`
		To          string `xml:"http://schemas.xmlsoap.org/ws/2004/08/addressing To"`
		ResourceURI string `xml:"http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd ResourceURI"`
	} `xml:"http://www.w3.org/2003/05/soap-envelope Header"`
	Body struct {
		Enumerate *struct {
			Optimize *struct{} `xml:"http://schemas.dmtf.org/wbem/wsman/1/wsman.xsd OptimizeEnumeration"`
		} `xml:"http://schemas.xmlsoap.org/ws/2004/09/enumeration Enumerate"`
		Pull *struct {
			Context string `xml:"http://schemas.xmlsoap.org/ws/2004/09/enumeration EnumerationContext"`
		} `xml:"http://schemas.xmlsoap.org/ws/2004/09/enumeration Pull"`
	} `xml:"http://www.w3.org/2003/05/soap-envelope Body"`
}

func (f *winRMFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var req fakeRequest
	if err := xml.Unmarshal(body, &req); err != nil {
		writeFault(w, "", "w:SchemaValidationError", "The SOAP XML in the message does not match the corresponding XML schema definition: "+err.Error())
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.messageIDs == nil {
		f.messageIDs, f.contexts = map[string]bool{}, map[string]fakeEnumeration{}
	}
	id := req.Header.MessageID
	switch {
	case id == "":
		writeFault(w, id, "a:InvalidMessageInformationHeader", "The wsa:MessageID header is missing.")
		return
	case f.messageIDs[id]:
		writeFault(w, id, "a:InvalidMessageInformationHeader", "The wsa:MessageID "+id+" was used before.")
		return
	case req.Header.To != "https://"+r.Host+"/wsman":
		writeFault(w, id, "a:DestinationUnreachable", "The wsa:To header "+req.Header.To+" does not name this endpoint.")
		return
	}
	f.messageIDs[id] = true
	class, ok := strings.CutPrefix(req.Header.ResourceURI, cimv2URI)
	if !ok || class == "" {
		writeFault(w, id, "w:DestinationUnreachable", "The WS-Management service cannot process the request. The resource URI is not supported.")
		return
	}
	switch {
	case req.Header.Action == wsmanActionEnumerate && req.Body.Enumerate != nil:
		f.requests = append(f.requests, fmt.Sprintf("Enumerate %s optimize=%v", class, req.Body.Enumerate.Optimize != nil))
		e := fakeEnumeration{class: class}
		optimized, end := "", false
		if f.optimize && req.Body.Enumerate.Optimize != nil {
			var items string
			items, end = f.next(&e)
			optimized = "<w:Items>" + items + "</w:Items>"
			if end {
				optimized += "<w:EndOfSequence/>"
			}
		}
		// The schema requires a context; after EndOfSequence it is closed.
		enumContext := "uuid:6f0b6f0e-0000-4000-8000-000000000000"
		if !end {
			enumContext = f.open(e)
		}
		writeEnvelope(w, http.StatusOK, wsmanActionEnumerate+"Response", id, class,
			"<n:EnumerateResponse><n:EnumerationContext>"+enumContext+"</n:EnumerationContext>"+optimized+"</n:EnumerateResponse>")
	case req.Header.Action == wsmanActionPull && req.Body.Pull != nil:
		f.requests = append(f.requests, "Pull "+class)
		e, ok := f.contexts[req.Body.Pull.Context]
		delete(f.contexts, req.Body.Pull.Context)
		switch {
		case !ok || e.class != class:
			writeFault(w, id, "n:InvalidEnumerationContext", "The enumeration context supplied in the message is not valid.")
		case f.pullFault != "":
			writeFault(w, id, f.pullFault, "The WS-Management service cannot process the request.")
		case f.endless:
			writeEnvelope(w, http.StatusOK, wsmanActionPull+"Response", id, class,
				"<n:PullResponse><n:EnumerationContext>"+f.open(e)+"</n:EnumerationContext><n:Items/></n:PullResponse>")
		default:
			items, end := f.next(&e)
			if end {
				writeEnvelope(w, http.StatusOK, wsmanActionPull+"Response", id, class,
					"<n:PullResponse><n:Items>"+items+"</n:Items><n:EndOfSequence/></n:PullResponse>")
				return
			}
			writeEnvelope(w, http.StatusOK, wsmanActionPull+"Response", id, class,
				"<n:PullResponse><n:EnumerationContext>"+f.open(e)+"</n:EnumerationContext><n:Items>"+items+"</n:Items></n:PullResponse>")
		}
	default:
		writeFault(w, id, "a:ActionNotSupported", "The action "+req.Header.Action+" is not supported by the service.")
	}
}

// next returns the next batch of e's instances and whether it ends the
// sequence.
func (f *winRMFake) next(e *fakeEnumeration) (string, bool) {
	all := f.instances[e.class]
	end := min(e.pos+max(f.batch, 1), len(all))
	var b strings.Builder
	for _, props := range all[e.pos:end] {
		fmt.Fprintf(&b, `<p:%[1]s xmlns:p="%[2]s%[1]s" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">%[3]s</p:%[1]s>`, e.class, cimv2URI, props)
	}
	e.pos = end
	return b.String(), end == len(all)
}

// open registers e under a fresh enumeration context.
func (f *winRMFake) open(e fakeEnumeration) string {
	f.seq++
	id := fmt.Sprintf("uuid:6f0b6f0e-0000-4000-8000-%012d", f.seq)
	f.contexts[id] = e
	return id
}

// log returns the requests received ("Enumerate <class> optimize=…",
// "Pull <class>") and the number of enumeration contexts still open.
func (f *winRMFake) log() ([]string, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.requests...), len(f.contexts)
}

// writeEnvelope writes a WS-Management response to the request relatesTo.
func writeEnvelope(w http.ResponseWriter, status int, action, relatesTo, class, body string) {
	w.Header().Set("Content-Type", "application/soap+xml;charset=UTF-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, `<s:Envelope xml:lang="en-US" %s xmlns:p="%s%s"><s:Header>`+
		`<a:To>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</a:To><a:Action>%s</a:Action>`+
		`<a:MessageID>uuid:6f0b6f0e-0000-4000-8000-00000000ffff</a:MessageID><a:RelatesTo>%s</a:RelatesTo></s:Header>`+
		`<s:Body>%s</s:Body></s:Envelope>`, fakeNamespaces, cimv2URI, class, action, relatesTo, body)
}

// writeFault writes a SOAP fault the way WinRM does: HTTP 500, the error in
// the subcode.
func writeFault(w http.ResponseWriter, relatesTo, subcode, reason string) {
	var text strings.Builder
	_ = xml.EscapeText(&text, []byte(reason))
	writeEnvelope(w, http.StatusInternalServerError, "http://schemas.dmtf.org/wbem/wsman/1/wsman/fault", relatesTo, "",
		`<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>`+subcode+`</s:Value></s:Subcode></s:Code>`+
			`<s:Reason><s:Text xml:lang="en-US">`+text.String()+`</s:Text></s:Reason></s:Fault>`)
}
