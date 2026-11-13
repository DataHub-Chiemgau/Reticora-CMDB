// Package fakegnmi is an in-process gNMI server for the tests of the gNMI
// client and the gNMI probe types (WP-1015, PRT-21). It listens on loopback
// with TLS from a CA it creates itself and serves a static tree from JSON
// whose values can be changed at run time. Encodings, refusing Get with
// Unimplemented (devices that only support Subscribe ONCE), a
// username/password check, client certificates and an answer of a set size
// can be configured. It is test infrastructure: the release build
// (./cmd/collector) does not use it.
//
// Tree: a JSON object whose leaves are the values; a list entry is an
// element name with its keys, e.g.
//
//	{"interfaces": {"interface[name=eth0]": {"state": {"counters": {"in-octets": 42}}}}}
//
// serves /interfaces/interface[name=eth0]/state/counters/in-octets = 42.
package fakegnmi

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	gpb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// PaddingPath is the leaf the server adds to every answer when
// Config.ResponseBytes is set.
const PaddingPath = "/fakegnmi/padding"

// Config is what the server serves and to whom.
type Config struct {
	// Tree is the JSON tree of values (see the package comment).
	Tree []byte
	// Encodings are the supported encodings (default JSON_IETF and PROTO).
	Encodings []gpb.Encoding
	// RejectGet refuses Get with Unimplemented.
	RejectGet bool
	// Username and Password, when set, are required as metadata.
	Username, Password string
	// RequireClientCert requires a client certificate of the server's CA
	// (IssueClientCert).
	RequireClientCert bool
	// ResponseBytes, when set, adds a string leaf of that many bytes to
	// every answer (PaddingPath).
	ResponseBytes int
	// Plaintext serves without TLS (to prove the client does not fall back
	// to plaintext).
	Plaintext bool
}

// Server is a running fake gNMI server.
type Server struct {
	gpb.UnimplementedGNMIServer

	cfg    Config
	ca     *x509.Certificate
	caKey  *ecdsa.PrivateKey
	caPEM  []byte
	leaf   *x509.Certificate
	lis    net.Listener
	grpc   *grpc.Server
	calls  atomic.Int64
	served chan struct{}

	mu   sync.RWMutex
	tree map[string]any // leaf path → value
}

// Start starts a server on 127.0.0.1 with cfg; the server keeps its own
// copy of cfg.
func Start(cfg *Config) (*Server, error) {
	tree := map[string]any{}
	if len(cfg.Tree) > 0 {
		var root map[string]any
		dec := json.NewDecoder(strings.NewReader(string(cfg.Tree)))
		dec.UseNumber()
		if err := dec.Decode(&root); err != nil {
			return nil, fmt.Errorf("fakegnmi: tree: %w", err)
		}
		flatten("", root, tree)
	}
	s := &Server{cfg: *cfg, tree: tree, served: make(chan struct{})}
	if len(s.cfg.Encodings) == 0 {
		s.cfg.Encodings = []gpb.Encoding{gpb.Encoding_JSON_IETF, gpb.Encoding_PROTO}
	}
	var opts []grpc.ServerOption
	if !cfg.Plaintext {
		tlsCfg, err := s.serverTLS()
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.Creds(credentials.NewTLS(tlsCfg)))
	}
	opts = append(opts, grpc.UnaryInterceptor(s.unary), grpc.StreamInterceptor(s.stream))
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s.lis = lis
	s.grpc = grpc.NewServer(opts...)
	gpb.RegisterGNMIServer(s.grpc, s)
	go func() {
		defer close(s.served)
		_ = s.grpc.Serve(lis)
	}()
	return s, nil
}

// Addr is host:port of the server.
func (s *Server) Addr() string { return s.lis.Addr().String() }

// CAPEM is the PEM certificate of the server's CA.
func (s *Server) CAPEM() []byte { return s.caPEM }

// SPKISHA256 is the SHA-256 hash of the SubjectPublicKeyInfo of the server
// certificate (a TLS scope pin).
func (s *Server) SPKISHA256() []byte {
	sum := sha256.Sum256(s.leaf.RawSubjectPublicKeyInfo)
	return sum[:]
}

// Calls is the number of RPCs that reached the server.
func (s *Server) Calls() int64 { return s.calls.Load() }

// Close stops the server.
func (s *Server) Close() {
	s.grpc.Stop()
	<-s.served
}

// SetValue changes or adds the value of a leaf path (gNMI Set itself is
// not served).
func (s *Server) SetValue(path string, value any) {
	s.mu.Lock()
	s.tree[path] = value
	s.mu.Unlock()
}

// Value returns the value of a leaf path.
func (s *Server) Value(path string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.tree[path]
	return v, ok
}

// SetEncodings, SetRejectGet and SetResponseBytes change the behaviour
// while the server runs.
func (s *Server) SetEncodings(enc ...gpb.Encoding) {
	s.mu.Lock()
	s.cfg.Encodings = enc
	s.mu.Unlock()
}

func (s *Server) SetRejectGet(reject bool) {
	s.mu.Lock()
	s.cfg.RejectGet = reject
	s.mu.Unlock()
}

func (s *Server) SetResponseBytes(n int) {
	s.mu.Lock()
	s.cfg.ResponseBytes = n
	s.mu.Unlock()
}

func flatten(prefix string, node, out map[string]any) {
	for k, v := range node {
		p := prefix + "/" + k
		if child, ok := v.(map[string]any); ok {
			flatten(p, child, out)
			continue
		}
		out[canonical(p)] = v
	}
}

// canonical renders a path string with its keys sorted per element.
func canonical(p string) string {
	if parsed, err := parse(p); err == nil {
		return pathString(parsed)
	}
	return p
}

// newKey returns an ECDSA P-256 key.
func newKey() (*ecdsa.PrivateKey, error) { return ecdsa.GenerateKey(elliptic.P256(), rand.Reader) }

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	return n
}

// serverTLS creates the CA and the server certificate (127.0.0.1,
// localhost).
func (s *Server) serverTLS() (*tls.Config, error) {
	caKey, err := newKey()
	if err != nil {
		return nil, err
	}
	caTmpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "fakegnmi CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	s.ca, _ = x509.ParseCertificate(caDER)
	s.caKey = caKey
	s.caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	key, err := newKey()
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "fakegnmi"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.ca, &key.PublicKey, caKey)
	if err != nil {
		return nil, err
	}
	s.leaf, _ = x509.ParseCertificate(der)
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key, Leaf: s.leaf}}}
	if s.cfg.RequireClientCert {
		pool := x509.NewCertPool()
		pool.AddCert(s.ca)
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// IssueClientCert issues a client certificate of the server's CA and
// returns it and its key as PEM.
func (s *Server) IssueClientCert(name string) (certPEM, keyPEM []byte, err error) {
	if s.ca == nil {
		return nil, nil, errors.New("fakegnmi: no CA (plaintext server)")
	}
	key, err := newKey()
	if err != nil {
		return nil, nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, s.ca, &key.PublicKey, s.caKey)
	if err != nil {
		return nil, nil, err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), nil
}

// authorize counts the call and checks username and password.
func (s *Server) authorize(ctx context.Context) error {
	s.calls.Add(1)
	if s.cfg.Username == "" {
		return nil
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if first(md.Get("username")) != s.cfg.Username || first(md.Get("password")) != s.cfg.Password {
		return status.Error(codes.Unauthenticated, "invalid username or password")
	}
	return nil
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return v[0]
}

func (s *Server) unary(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
	if err := s.authorize(ctx); err != nil {
		return nil, err
	}
	return h(ctx, req)
}

func (s *Server) stream(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
	if err := s.authorize(ss.Context()); err != nil {
		return err
	}
	return h(srv, ss)
}

// Capabilities answers with the configured encodings.
func (s *Server) Capabilities(context.Context, *gpb.CapabilityRequest) (*gpb.CapabilityResponse, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return &gpb.CapabilityResponse{
		GNMIVersion:        "0.10.0",
		SupportedEncodings: append([]gpb.Encoding(nil), s.cfg.Encodings...),
		SupportedModels:    []*gpb.ModelData{{Name: "openconfig-interfaces", Organization: "OpenConfig working group", Version: "3.0.0"}},
	}, nil
}

// Get answers the leaves below the requested paths.
func (s *Server) Get(_ context.Context, req *gpb.GetRequest) (*gpb.GetResponse, error) {
	s.mu.RLock()
	reject := s.cfg.RejectGet
	s.mu.RUnlock()
	if reject {
		return nil, status.Error(codes.Unimplemented, "Get is not supported, use Subscribe ONCE")
	}
	n, err := s.notification(req.GetPath(), req.GetEncoding())
	if err != nil {
		return nil, err
	}
	return &gpb.GetResponse{Notification: []*gpb.Notification{n}}, nil
}

// Subscribe supports only the mode ONCE: the values, then a sync response.
func (s *Server) Subscribe(stream gpb.GNMI_SubscribeServer) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}
	list := req.GetSubscribe()
	if list == nil || list.GetMode() != gpb.SubscriptionList_ONCE {
		return status.Error(codes.Unimplemented, "only Subscribe ONCE is supported")
	}
	paths := make([]*gpb.Path, 0, len(list.GetSubscription()))
	for _, sub := range list.GetSubscription() {
		paths = append(paths, sub.GetPath())
	}
	n, err := s.notification(paths, list.GetEncoding())
	if err != nil {
		return err
	}
	if err := stream.Send(&gpb.SubscribeResponse{Response: &gpb.SubscribeResponse_Update{Update: n}}); err != nil {
		return err
	}
	return stream.Send(&gpb.SubscribeResponse{Response: &gpb.SubscribeResponse_SyncResponse{SyncResponse: true}})
}

// notification collects the leaves below paths in enc.
func (s *Server) notification(paths []*gpb.Path, enc gpb.Encoding) (*gpb.Notification, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n := &gpb.Notification{Timestamp: time.Now().UnixNano()}
	var leaves []string
	for leaf := range s.tree {
		for _, p := range paths {
			prefix := pathString(p)
			if prefix == "/" || leaf == prefix || strings.HasPrefix(leaf, prefix+"/") {
				leaves = append(leaves, leaf)
				break
			}
		}
	}
	sort.Strings(leaves)
	for _, leaf := range leaves {
		val, err := typed(s.tree[leaf], enc)
		if err != nil {
			return nil, err
		}
		p, _ := parse(leaf)
		n.Update = append(n.Update, &gpb.Update{Path: p, Val: val})
	}
	if s.cfg.ResponseBytes > 0 {
		p, _ := parse(PaddingPath)
		n.Update = append(n.Update, &gpb.Update{Path: p, Val: &gpb.TypedValue{Value: &gpb.TypedValue_StringVal{
			StringVal: strings.Repeat("x", s.cfg.ResponseBytes)}}})
	}
	return n, nil
}

// typed encodes a value: JSON_IETF and JSON as JSON, PROTO as a scalar.
func typed(v any, enc gpb.Encoding) (*gpb.TypedValue, error) {
	switch enc {
	case gpb.Encoding_JSON_IETF, gpb.Encoding_JSON:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		if enc == gpb.Encoding_JSON {
			return &gpb.TypedValue{Value: &gpb.TypedValue_JsonVal{JsonVal: b}}, nil
		}
		return &gpb.TypedValue{Value: &gpb.TypedValue_JsonIetfVal{JsonIetfVal: b}}, nil
	case gpb.Encoding_PROTO:
		switch x := v.(type) {
		case json.Number:
			if i, err := x.Int64(); err == nil {
				return &gpb.TypedValue{Value: &gpb.TypedValue_IntVal{IntVal: i}}, nil
			}
			f, _ := x.Float64()
			return &gpb.TypedValue{Value: &gpb.TypedValue_DoubleVal{DoubleVal: f}}, nil
		case int:
			return &gpb.TypedValue{Value: &gpb.TypedValue_IntVal{IntVal: int64(x)}}, nil
		case int64:
			return &gpb.TypedValue{Value: &gpb.TypedValue_IntVal{IntVal: x}}, nil
		case uint64:
			return &gpb.TypedValue{Value: &gpb.TypedValue_UintVal{UintVal: x}}, nil
		case float64:
			return &gpb.TypedValue{Value: &gpb.TypedValue_DoubleVal{DoubleVal: x}}, nil
		case bool:
			return &gpb.TypedValue{Value: &gpb.TypedValue_BoolVal{BoolVal: x}}, nil
		case string:
			return &gpb.TypedValue{Value: &gpb.TypedValue_StringVal{StringVal: x}}, nil
		}
		return &gpb.TypedValue{Value: &gpb.TypedValue_StringVal{StringVal: fmt.Sprint(v)}}, nil
	}
	return nil, status.Errorf(codes.InvalidArgument, "encoding %s not supported", enc)
}

// parse reads a path string /a/b[k=v]/c (no escapes; enough for the fake).
func parse(s string) (*gpb.Path, error) {
	p := &gpb.Path{}
	for _, part := range splitPath(s) {
		name, keys, _ := strings.Cut(part, "[")
		e := &gpb.PathElem{Name: name}
		if keys != "" {
			e.Key = map[string]string{}
			for _, kv := range strings.Split(strings.TrimSuffix(keys, "]"), "][") {
				k, v, ok := strings.Cut(kv, "=")
				if !ok {
					return nil, fmt.Errorf("fakegnmi: key %q", kv)
				}
				e.Key[k] = v
			}
		}
		p.Elem = append(p.Elem, e)
	}
	return p, nil
}

// splitPath splits at "/" outside of keys.
func splitPath(s string) []string {
	var parts []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '[':
			depth++
		case ']':
			depth--
		case '/':
			if depth == 0 {
				if i > start {
					parts = append(parts, s[start:i])
				}
				start = i + 1
			}
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

// pathString renders a path with sorted keys (as the client's PathString).
func pathString(p *gpb.Path) string {
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
			b.WriteString("[" + k + "=" + e.GetKey()[k] + "]")
		}
	}
	if b.Len() == 0 {
		return "/"
	}
	return b.String()
}
