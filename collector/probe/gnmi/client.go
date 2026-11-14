// Package gnmi is the gNMI client of the monitoring probes (WP-1015,
// PRT-16, E-193): Capabilities, Get and, as a fallback for devices without
// Get, Subscribe in the mode ONCE, always over TLS with the credential kind
// gnmi (username/password and/or client certificate, PRT-22). The client
// has no Set and no Subscribe STREAM/POLL; reading only.
//
// The package holds only the client and does not import collector/probe, so
// the executors in collector/probe (WP-1016, WP-1017) and
// collector/probe/routing use it without an import cycle. Mapping the errors
// to reason codes is the job of the executors (WP-1016).
package gnmi

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"

	"github.com/DataHub-Chiemgau/Reticora-CMDB/collector/plugins"
)

// Limits of PRT-16.
const (
	// MaxPaths is the largest number of paths of one request; more are
	// refused before anything is sent.
	MaxPaths = 32
	// MaxMessageBytes is the largest message the client receives.
	MaxMessageBytes = 4 << 20
	// MaxUpdates is the largest number of updates the client keeps of one
	// answer; the rest is cut (Result.Truncated).
	MaxUpdates = 10000
	// DefaultTimeout bounds one call when the configuration sets none.
	DefaultTimeout = 10 * time.Second
)

// Errors of the client besides plugins.ErrTLSUntrusted, plugins.ErrAuthFailed
// and plugins.ErrUnreachable.
var (
	// ErrMessageTooLarge: an answer was larger than MaxMessageBytes.
	ErrMessageTooLarge = errors.New("gnmi: message too large")
	// ErrTooManyPaths: a request named more than MaxPaths paths.
	ErrTooManyPaths = errors.New("gnmi: too many paths")
	// ErrTLSHandshake: the TLS handshake failed for another reason than an
	// untrusted certificate, e.g. a server without TLS. There is no
	// plaintext fallback.
	ErrTLSHandshake = errors.New("gnmi: tls handshake failed")
	// ErrNoEncoding: the device supports none of JSON_IETF, PROTO and JSON.
	ErrNoEncoding = errors.New("gnmi: no supported encoding")
	// ErrPath: a path could not be parsed.
	ErrPath = errors.New("gnmi: invalid path")
	// ErrCredential: the credential cannot be used (no PEM certificate in
	// ca, client_cert/client_key not a key pair).
	ErrCredential = errors.New("gnmi: unusable credential")
	// ErrPathNotFound: the device answered NotFound, at least one
	// requested path does not exist there (gNMI specification 3.3.4).
	ErrPathNotFound = errors.New("gnmi: path not found")
	// ErrRejected: the device answered the call with another gRPC error
	// (e.g. InvalidArgument, FailedPrecondition, Internal); it is
	// reachable but did not deliver values.
	ErrRejected = errors.New("gnmi: request rejected by the device")
)

// Mode selects how values are read (E-193 option 1).
type Mode string

// Modes: get uses Get and falls back to Subscribe ONCE when the device
// refuses Get with Unimplemented; once always uses Subscribe ONCE.
const (
	ModeGet  Mode = "get"
	ModeOnce Mode = "once"
)

// Config is the connection of a probe to one device.
type Config struct {
	// Target is host:port of the gNMI server.
	Target string
	// Credentials are those of the namespace gnmi (plugins.CredentialsFor):
	// username, password, client_cert, client_key and ca (PEM).
	Credentials map[string]string
	// TLSScopes are the TLS exceptions of the collector (CA bundle or SPKI
	// pin per network); used when the credential names no CA.
	TLSScopes []plugins.TLSScope
	// Timeout bounds every call (0: DefaultTimeout).
	Timeout time.Duration
	// Mode is get (default) or once.
	Mode Mode
	// Encoding, when set ("json_ietf" or "proto"), is the only encoding
	// the client uses; a device without it fails with ErrNoEncoding.
	// Empty chooses JSON_IETF, otherwise PROTO, otherwise JSON.
	Encoding string
	// Origin is set on every requested path (e.g. "openconfig"); empty
	// sends none.
	Origin string
}

// Client is a gNMI connection to one device. It is safe for concurrent use.
type Client struct {
	conn    *grpc.ClientConn
	gnmi    gpb.GNMIClient
	creds   *recordingCreds
	timeout time.Duration

	prefer gpb.Encoding
	strict bool // prefer is the only encoding allowed
	origin string

	mu       sync.Mutex
	mode     Mode
	encoding gpb.Encoding
	known    bool // encoding chosen
}

// Dial prepares a client; the connection is made with the first call. TLS
// is mandatory: the trust anchor is the CA of the credential, otherwise the
// TLS scope of the target's network, otherwise the system roots.
func Dial(cfg *Config) (*Client, error) {
	tlsCfg, err := tlsConfig(cfg)
	if err != nil {
		return nil, err
	}
	rc := &recordingCreds{TransportCredentials: credentials.NewTLS(tlsCfg)}
	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(rc),
		grpc.WithContextDialer(rc.dial),
		grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(MaxMessageBytes)),
	}
	if user := cfg.Credentials["username"]; user != "" {
		opts = append(opts, grpc.WithPerRPCCredentials(passwordCreds{user: user, password: cfg.Credentials["password"]}))
	}
	conn, err := grpc.NewClient(cfg.Target, opts...)
	if err != nil {
		return nil, fmt.Errorf("gnmi: %s: %w", cfg.Target, err)
	}
	mode := cfg.Mode
	if mode != ModeOnce {
		mode = ModeGet
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	c := &Client{conn: conn, gnmi: gpb.NewGNMIClient(conn), creds: rc, timeout: timeout, mode: mode, origin: cfg.Origin}
	switch strings.ToLower(cfg.Encoding) {
	case "":
	case "json_ietf":
		c.prefer, c.strict = gpb.Encoding_JSON_IETF, true
	case "proto":
		c.prefer, c.strict = gpb.Encoding_PROTO, true
	default:
		_ = conn.Close()
		return nil, fmt.Errorf("%w: %q", ErrNoEncoding, cfg.Encoding)
	}
	return c, nil
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// tlsConfig builds the TLS verification of cfg (AK2).
func tlsConfig(cfg *Config) (*tls.Config, error) {
	var tlsCfg *tls.Config
	if ca := cfg.Credentials["ca"]; strings.TrimSpace(ca) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(ca)) {
			return nil, fmt.Errorf("%w: ca holds no PEM certificate", ErrCredential)
		}
		tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
	} else {
		tlsCfg = plugins.TLSConfigFor(cfg.Target, cfg.TLSScopes)
	}
	certPEM, keyPEM := cfg.Credentials["client_cert"], cfg.Credentials["client_key"]
	if certPEM != "" || keyPEM != "" {
		cert, err := tls.X509KeyPair([]byte(certPEM), []byte(keyPEM))
		if err != nil {
			return nil, fmt.Errorf("%w: client_cert/client_key is not a valid key pair", ErrCredential)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	}
	return tlsCfg, nil
}

// passwordCreds send username and password as gRPC metadata, only over a
// secure transport.
type passwordCreds struct{ user, password string }

func (p passwordCreds) GetRequestMetadata(context.Context, ...string) (map[string]string, error) {
	return map[string]string{"username": p.user, "password": p.password}, nil
}

func (passwordCreds) RequireTransportSecurity() bool { return true }

// recordingCreds keeps the last failed client handshake, so a call that
// fails with Unavailable can tell an untrusted certificate apart: gRPC
// reports the handshake only as text.
type recordingCreds struct {
	credentials.TransportCredentials
	mu  sync.Mutex
	err error
}

func (r *recordingCreds) ClientHandshake(ctx context.Context, authority string, raw net.Conn) (net.Conn, credentials.AuthInfo, error) {
	conn, info, err := r.TransportCredentials.ClientHandshake(ctx, authority, raw)
	r.mu.Lock()
	r.err = err
	r.mu.Unlock()
	return conn, info, err
}

// dial starts a connection attempt: it forgets the handshake error of an
// earlier attempt, so an attempt that fails before the handshake (refused,
// host down) is reported as not reachable, not as the old TLS failure.
func (r *recordingCreds) dial(ctx context.Context, addr string) (net.Conn, error) {
	r.mu.Lock()
	r.err = nil
	r.mu.Unlock()
	var d net.Dialer
	return d.DialContext(ctx, "tcp", addr)
}

func (r *recordingCreds) Clone() credentials.TransportCredentials {
	return &recordingCreds{TransportCredentials: r.TransportCredentials.Clone()}
}

func (r *recordingCreds) handshakeErr() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// mapErr maps a failed call (AK3): untrusted certificate →
// plugins.ErrTLSUntrusted, another handshake failure → ErrTLSHandshake,
// Unauthenticated/PermissionDenied → plugins.ErrAuthFailed, a too large
// answer → ErrMessageTooLarge, not reachable → plugins.ErrUnreachable,
// NotFound → ErrPathNotFound, every other code → ErrRejected.
func (c *Client) mapErr(err error) error {
	if err == nil {
		return nil
	}
	st, ok := status.FromError(err)
	if !ok {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
			return fmt.Errorf("gnmi: %w", err)
		}
		return err
	}
	switch st.Code() {
	case codes.Unauthenticated, codes.PermissionDenied:
		return fmt.Errorf("gnmi: %w: %s", plugins.ErrAuthFailed, st.Code())
	case codes.ResourceExhausted:
		if strings.Contains(st.Message(), "larger than max") {
			return fmt.Errorf("%w: more than %d bytes", ErrMessageTooLarge, MaxMessageBytes)
		}
	case codes.Unavailable:
		if herr := c.creds.handshakeErr(); herr != nil {
			if plugins.UntrustedCertificate(herr) {
				return fmt.Errorf("gnmi: %w: %w", plugins.ErrTLSUntrusted, herr)
			}
			return fmt.Errorf("%w: %w", ErrTLSHandshake, herr)
		}
		return fmt.Errorf("gnmi: %w: %s", plugins.ErrUnreachable, st.Message())
	case codes.DeadlineExceeded:
		return fmt.Errorf("gnmi: %w", context.DeadlineExceeded)
	case codes.Canceled:
		return fmt.Errorf("gnmi: %w", context.Canceled)
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrPathNotFound, st.Message())
	}
	return fmt.Errorf("%w: %s: %s", ErrRejected, st.Code(), st.Message())
}

// Capabilities is the answer of the device to Capabilities.
type Capabilities struct {
	Version   string
	Models    []string
	Encodings []gpb.Encoding
	// Encoding is the encoding the client uses: JSON_IETF, otherwise
	// PROTO, otherwise JSON.
	Encoding gpb.Encoding
}

// Capabilities asks the device for its models and encodings and chooses
// the encoding of the following requests.
func (c *Client) Capabilities(ctx context.Context) (Capabilities, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.gnmi.Capabilities(ctx, &gpb.CapabilityRequest{})
	if err != nil {
		return Capabilities{}, c.mapErr(err)
	}
	out := Capabilities{Version: resp.GetGNMIVersion(), Encodings: resp.GetSupportedEncodings()}
	for _, m := range resp.GetSupportedModels() {
		out.Models = append(out.Models, m.GetName())
	}
	enc, ok := chooseEncoding(out.Encodings)
	if c.strict {
		enc, ok = c.prefer, slices.Contains(out.Encodings, c.prefer)
	}
	if !ok {
		return out, ErrNoEncoding
	}
	out.Encoding = enc
	c.mu.Lock()
	c.encoding, c.known = enc, true
	c.mu.Unlock()
	return out, nil
}

func chooseEncoding(supported []gpb.Encoding) (gpb.Encoding, bool) {
	for _, want := range []gpb.Encoding{gpb.Encoding_JSON_IETF, gpb.Encoding_PROTO, gpb.Encoding_JSON} {
		if slices.Contains(supported, want) {
			return want, true
		}
	}
	return 0, false
}

// Update is one value of an answer.
type Update struct {
	// Path is the full path of the value (prefix and path), e.g.
	// /interfaces/interface[name=eth0]/state/counters/in-octets.
	Path      string
	Value     *gpb.TypedValue
	Timestamp time.Time
}

// Float returns the value as a number: the integer and float types and a
// JSON or JSON_IETF number (also as a string, as for 64-bit counters).
func (u Update) Float() (float64, bool) {
	switch v := u.Value.GetValue().(type) {
	case *gpb.TypedValue_IntVal:
		return float64(v.IntVal), true
	case *gpb.TypedValue_UintVal:
		return float64(v.UintVal), true
	case *gpb.TypedValue_DoubleVal:
		return v.DoubleVal, true
	case *gpb.TypedValue_FloatVal:
		return float64(v.FloatVal), true //nolint:staticcheck // older devices still send float_val
	case *gpb.TypedValue_JsonIetfVal:
		return jsonNumber(v.JsonIetfVal)
	case *gpb.TypedValue_JsonVal:
		return jsonNumber(v.JsonVal)
	}
	return 0, false
}

func jsonNumber(b []byte) (float64, bool) {
	var n json.Number
	if json.Unmarshal(b, &n) != nil {
		var s string
		if json.Unmarshal(b, &s) != nil {
			return 0, false
		}
		n = json.Number(s)
	}
	f, err := n.Float64()
	return f, err == nil
}

// Result is the answer to Get.
type Result struct {
	Updates []Update
	// Truncated is true when the answer held more than MaxUpdates updates.
	Truncated bool
	// Once is true when the values came from Subscribe ONCE.
	Once bool
}

// Get reads paths (at most MaxPaths, e.g. "/interfaces/interface[name=eth0]/state")
// with Get or, in the mode once or for a device that refuses Get with
// Unimplemented, with Subscribe ONCE.
func (c *Client) Get(ctx context.Context, paths []string) (Result, error) {
	if len(paths) > MaxPaths {
		return Result{}, fmt.Errorf("%w: %d paths, at most %d", ErrTooManyPaths, len(paths), MaxPaths)
	}
	parsed := make([]*gpb.Path, len(paths))
	for i, p := range paths {
		var err error
		if parsed[i], err = ParsePath(p); err != nil {
			return Result{}, err
		}
		parsed[i].Origin = c.origin
	}
	c.mu.Lock()
	known, mode := c.known, c.mode
	c.mu.Unlock()
	if !known {
		if _, err := c.Capabilities(ctx); err != nil {
			return Result{}, err
		}
	}
	c.mu.Lock()
	enc := c.encoding
	c.mu.Unlock()
	if mode == ModeGet {
		res, err := c.get(ctx, parsed, enc)
		if status.Code(err) != codes.Unimplemented {
			return res, c.mapErr(err)
		}
		c.mu.Lock()
		c.mode = ModeOnce // the device has no Get; use ONCE from now on
		c.mu.Unlock()
	}
	res, err := c.once(ctx, parsed, enc)
	return res, c.mapErr(err)
}

func (c *Client) get(ctx context.Context, paths []*gpb.Path, enc gpb.Encoding) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.gnmi.Get(ctx, &gpb.GetRequest{Path: paths, Encoding: enc, Type: gpb.GetRequest_ALL})
	if err != nil {
		return Result{}, err
	}
	var res Result
	for _, n := range resp.GetNotification() {
		res.add(n)
	}
	return res, nil
}

// once reads the paths with Subscribe in the mode ONCE: the device sends
// its values and a sync response, then the stream ends.
func (c *Client) once(ctx context.Context, paths []*gpb.Path, enc gpb.Encoding) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	stream, err := c.gnmi.Subscribe(ctx)
	if err != nil {
		return Result{}, err
	}
	subs := make([]*gpb.Subscription, len(paths))
	for i, p := range paths {
		subs[i] = &gpb.Subscription{Path: p}
	}
	err = stream.Send(&gpb.SubscribeRequest{Request: &gpb.SubscribeRequest_Subscribe{Subscribe: &gpb.SubscriptionList{
		Subscription: subs, Mode: gpb.SubscriptionList_ONCE, Encoding: enc,
	}}})
	if err != nil {
		return Result{}, err
	}
	_ = stream.CloseSend()
	res := Result{Once: true}
	for {
		resp, rerr := stream.Recv()
		if errors.Is(rerr, io.EOF) {
			return res, nil
		}
		if rerr != nil {
			return res, rerr
		}
		if resp.GetSyncResponse() {
			return res, nil
		}
		if n := resp.GetUpdate(); n != nil {
			res.add(n)
		}
	}
}

// add takes the updates of a notification, at most MaxUpdates in all.
func (r *Result) add(n *gpb.Notification) {
	prefix := n.GetPrefix()
	ts := time.Unix(0, n.GetTimestamp()).UTC()
	for _, u := range n.GetUpdate() {
		if len(r.Updates) >= MaxUpdates {
			r.Truncated = true
			return
		}
		r.Updates = append(r.Updates, Update{Path: PathString(join(prefix, u.GetPath())), Value: u.GetVal(), Timestamp: ts})
	}
}

func join(prefix, p *gpb.Path) *gpb.Path {
	if prefix == nil {
		return p
	}
	return &gpb.Path{Origin: prefix.GetOrigin(), Elem: append(slices.Clone(prefix.GetElem()), p.GetElem()...)}
}

// ParsePath parses a path in the gNMI string form
// (/a/b[k=v][k2=v2]/c, a key value may hold "/" and escaped "]").
func ParsePath(s string) (*gpb.Path, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "/") {
		return nil, fmt.Errorf("%w: %q must start with /", ErrPath, s)
	}
	p := &gpb.Path{}
	rest := s[1:]
	for rest != "" {
		var elem gpb.PathElem
		i := 0
		for i < len(rest) && rest[i] != '/' && rest[i] != '[' {
			i++
		}
		elem.Name = rest[:i]
		if elem.Name == "" {
			return nil, fmt.Errorf("%w: %q has an empty element", ErrPath, s)
		}
		rest = rest[i:]
		for strings.HasPrefix(rest, "[") {
			eq := strings.IndexByte(rest, '=')
			if eq < 0 {
				return nil, fmt.Errorf("%w: %q has a key without value", ErrPath, s)
			}
			key := rest[1:eq]
			var val strings.Builder
			j := eq + 1
			for ; j < len(rest) && rest[j] != ']'; j++ {
				if rest[j] == '\\' && j+1 < len(rest) {
					j++
				}
				val.WriteByte(rest[j])
			}
			if j >= len(rest) || key == "" {
				return nil, fmt.Errorf("%w: %q has an unclosed key", ErrPath, s)
			}
			if elem.Key == nil {
				elem.Key = map[string]string{}
			}
			elem.Key[key] = val.String()
			rest = rest[j+1:]
		}
		p.Elem = append(p.Elem, &elem)
		if rest != "" {
			if rest[0] != '/' {
				return nil, fmt.Errorf("%w: %q", ErrPath, s)
			}
			rest = rest[1:]
		}
	}
	return p, nil
}

// keyEscaper escapes a key value as ParsePath reads it: the escape
// character itself first, then "]".
var keyEscaper = strings.NewReplacer(`\`, `\\`, "]", `\]`)

// PathString renders a path in the gNMI string form, keys sorted.
func PathString(p *gpb.Path) string {
	var b strings.Builder
	for _, e := range p.GetElem() {
		b.WriteByte('/')
		b.WriteString(e.GetName())
		keys := make([]string, 0, len(e.GetKey()))
		for k := range e.GetKey() {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			b.WriteString("[" + k + "=" + keyEscaper.Replace(e.GetKey()[k]) + "]")
		}
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}

// String names the encoding for logs.
func encodingName(e gpb.Encoding) string {
	if name, ok := gpb.Encoding_name[int32(e)]; ok {
		return name
	}
	return strconv.Itoa(int(e))
}

// Encoding is the encoding the client uses (empty before Capabilities).
func (c *Client) Encoding() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.known {
		return ""
	}
	return encodingName(c.encoding)
}
