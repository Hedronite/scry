// Package fakecluster is a scripted loopback TLS Kubernetes apiserver for tests.
//
// It exists for two reasons. First, every request is logged with its connection
// identity, so a test can assert exactly what left the process: the method, the
// path, the query, whether the connection was closed, and which connection
// carried it. Second, the certificate authority is generated per fixture:
// net/http/httptest serves one baked-in certificate to every server, which
// cannot express "this CA is a stranger".
//
// This is a normal package rather than a _test.go file so tests in other
// packages can import it. Nothing in cmd/ imports it; it never opens a socket
// outside the loopback interface and never reads a real kubeconfig.
package fakecluster

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// Paths the fake answers.
const (
	ReadyzPath  = "/readyz"
	LivezPath   = "/livez"
	VersionPath = "/version"
	NodesPath   = "/api/v1/nodes"
)

// VersionH3S is what an h3s apiserver reports at /version.
const VersionH3S = `{"major":"1","minor":"34","gitVersion":"v1.34.0+h3s"}`

// VersionStock is what a stock Kubernetes apiserver reports.
const VersionStock = `{"major":"1","minor":"34","gitVersion":"v1.34.0"}`

// ContextAlias is the kubeconfig context name a generated kubeconfig uses: a
// valid alias, so a document's cluster will read "synthetic" unless a test says
// otherwise.
const ContextAlias = "synthetic"

// DefaultToken is the bearer token a generated kubeconfig carries.
const DefaultToken = "synthetic-token"

// Reply is the scripted answer for one path.
type Reply struct {
	Status      int    // 0 means 200
	Body        string // empty means the path's default body
	ContentType string // empty means text/plain for readyz, JSON otherwise
	// Hang answers nothing until the client gives up: the peer completed TCP
	// and TLS, then went silent.
	Hang bool
}

// Options script the fixture. A zero Options answers all three signals the way
// a stock apiserver with one ready node would.
type Options struct {
	Readyz  Reply
	Livez   Reply
	Version Reply
	Nodes   Reply
	// Handle runs before the scripted reply. Returning true means it handled the
	// request; the reply is then not used.
	Handle func(http.ResponseWriter, *http.Request) bool
}

// Request is one logged request.
type Request struct {
	Method        string
	Path          string
	Query         string
	Close         bool // the client asked for the connection to be closed
	Connection    int  // fixture-local connection number
	Authorization string
}

// Node scripts one node in the node list.
type Node struct {
	Name               string
	Ready              string // "True", "False", "Unknown", or "" for no condition
	Reason             string
	NetworkUnavailable string // "True" adds the condition
}

// NodeList renders the JSON body of a scripted node list.
func NodeList(nodes ...Node) string {
	type condition struct {
		Type   string `json:"type"`
		Status string `json:"status"`
		Reason string `json:"reason,omitempty"`
	}
	type items struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Status struct {
			Conditions []condition `json:"conditions"`
		} `json:"status"`
	}
	list := struct {
		APIVersion string  `json:"apiVersion"`
		Kind       string  `json:"kind"`
		Items      []items `json:"items"`
	}{APIVersion: "v1", Kind: "NodeList", Items: make([]items, 0, len(nodes))}
	for _, node := range nodes {
		var item items
		item.Metadata.Name = node.Name
		if node.Ready != "" {
			item.Status.Conditions = append(item.Status.Conditions, condition{
				Type: "Ready", Status: node.Ready, Reason: node.Reason,
			})
		}
		if node.NetworkUnavailable != "" {
			item.Status.Conditions = append(item.Status.Conditions, condition{
				Type: "NetworkUnavailable", Status: node.NetworkUnavailable,
			})
		}
		list.Items = append(list.Items, item)
	}
	body, err := json.Marshal(list)
	if err != nil {
		// Only reachable with a non-JSON-encodable field, which this type cannot
		// have; a panic here would be a fixture bug worth seeing.
		panic(err)
	}
	return string(body)
}

// Server is one running fixture.
type Server struct {
	srv         *httptest.Server
	caPEM       []byte
	mu          sync.Mutex
	requests    []Request
	connections atomic.Int64
}

// URL is the fixture's base URL: https on a loopback address.
func (s *Server) URL() string { return s.srv.URL }

// CAPEM is the fixture's certificate authority, PEM encoded: exactly what a
// kubeconfig needs to trust it.
func (s *Server) CAPEM() []byte { return s.caPEM }

type connectionKey struct{}

// New starts the fixture on the loopback interface with a fresh CA.
func New(opts Options) (*Server, error) {
	caPEM, leaf, err := serverCertificate()
	if err != nil {
		return nil, err
	}
	s := &Server{caPEM: caPEM}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(s.serve(opts)))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{leaf}, MinVersion: tls.VersionTLS12}
	srv.Config.ConnContext = func(ctx context.Context, _ net.Conn) context.Context {
		return context.WithValue(ctx, connectionKey{}, int(s.connections.Add(1)))
	}
	srv.StartTLS()
	s.srv = srv
	return s, nil
}

// Requests returns a copy of the request log in arrival order.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Paths returns the logged request paths in arrival order.
func (s *Server) Paths() []string {
	requests := s.Requests()
	paths := make([]string, 0, len(requests))
	for _, request := range requests {
		paths = append(paths, request.Path)
	}
	return paths
}

// Count returns how many requests reached the given path.
func (s *Server) Count(path string) int {
	count := 0
	for _, request := range s.Requests() {
		if request.Path == path {
			count++
		}
	}
	return count
}

// Connections returns how many connections the fixture accepted.
func (s *Server) Connections() int { return int(s.connections.Load()) }

// Kubeconfig writes a kubeconfig for this fixture at path and returns it. The
// mutate hook may replace or adjust anything, which is how refusal tests script
// an exec plugin, an auth provider, insecure TLS or a proxy.
func (s *Server) Kubeconfig(path string, mutate func(*clientcmdapi.Config)) (string, error) {
	config := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cluster": {Server: s.srv.URL, CertificateAuthorityData: s.caPEM},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"user": {Token: DefaultToken},
		},
		Contexts: map[string]*clientcmdapi.Context{
			ContextAlias: {Cluster: "cluster", AuthInfo: "user"},
		},
		CurrentContext: ContextAlias,
	}
	if mutate != nil {
		mutate(&config)
	}
	if err := clientcmd.WriteToFile(config, path); err != nil {
		return "", err
	}
	return path, nil
}

// Close stops the fixture.
func (s *Server) Close() { s.srv.Close() }

func (s *Server) serve(opts Options) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.record(r)
		if opts.Handle != nil && opts.Handle(w, r) {
			return
		}
		reply, ok := opts.reply(r.URL.Path)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if reply.Hang {
			<-r.Context().Done()
			return
		}
		status := reply.Status
		if status == 0 {
			status = http.StatusOK
		}
		contentType := reply.ContentType
		if contentType == "" {
			contentType = "application/json"
			switch r.URL.Path {
			case ReadyzPath, LivezPath:
				contentType = "text/plain"
			}
		}
		if reply.Body != "" {
			w.Header().Set("Content-Type", contentType)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(reply.Body))
	}
}

func (s *Server) record(r *http.Request) {
	connection, _ := r.Context().Value(connectionKey{}).(int)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, Request{
		Method:        r.Method,
		Path:          r.URL.Path,
		Query:         r.URL.RawQuery,
		Close:         r.Close,
		Connection:    connection,
		Authorization: r.Header.Get("Authorization"),
	})
}

func (opts Options) reply(path string) (Reply, bool) {
	switch path {
	case ReadyzPath:
		return withDefault(opts.Readyz, "ok\n"), true
	case LivezPath:
		return withDefault(opts.Livez, "ok\n"), true
	case VersionPath:
		return withDefault(opts.Version, VersionH3S), true
	case NodesPath:
		return withDefault(opts.Nodes, NodeList(Node{Name: "node-a", Ready: "True", Reason: "KubeletReady"})), true
	default:
		return Reply{}, false
	}
}

func withDefault(reply Reply, body string) Reply {
	if reply.Body == "" && !reply.Hang {
		reply.Body = body
	}
	return reply
}

// serverCertificate mints a CA and a leaf for 127.0.0.1 signed by it.
func serverCertificate() (caPEM []byte, leaf tls.Certificate, err error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, leaf, err
	}
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "synthetic-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, leaf, err
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		return nil, leaf, err
	}

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, leaf, err
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "synthetic-apiserver"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	if err != nil {
		return nil, leaf, err
	}
	leafKeyDER, err := x509.MarshalECPrivateKey(leafKey)
	if err != nil {
		return nil, leaf, err
	}
	leaf, err = tls.X509KeyPair(
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: leafKeyDER}),
	)
	if err != nil {
		return nil, leaf, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), leaf, nil
}

// NewClientCert returns a self-signed client certificate and key in PEM form,
// for a kubeconfig that authenticates with a certificate.
func NewClientCert() (certPEM, keyPEM []byte, err error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(3),
		Subject:               pkix.Name{CommonName: "synthetic-client"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
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
