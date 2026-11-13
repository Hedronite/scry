package kube

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

// clusterSpec is what a synthetic kubeconfig points at.
type clusterSpec struct {
	server  string
	ca      []byte // PEM; nil leaves verification to the system roots
	token   string
	context string
	mutate  func(*clientcmdapi.Config)
}

// writeKubeconfig writes a throwaway kubeconfig into the test's temp dir. No
// real address, credential or file ever leaves the process.
func writeKubeconfig(t *testing.T, spec clusterSpec) string {
	t.Helper()
	contextName := spec.context
	if contextName == "" {
		contextName = "synthetic"
	}
	cfg := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cluster": {Server: spec.server, CertificateAuthorityData: spec.ca},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"user": {Token: spec.token},
		},
		Contexts: map[string]*clientcmdapi.Context{
			contextName: {Cluster: "cluster", AuthInfo: "user"},
		},
		CurrentContext: contextName,
	}
	if spec.mutate != nil {
		spec.mutate(&cfg)
	}
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if err := clientcmd.WriteToFile(cfg, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

// tlsCluster starts a TLS server and returns it with its CA in PEM form.
func tlsCluster(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	t.Cleanup(srv.Close)
	return srv, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
}

// selfSignedPair returns a client certificate and key in PEM form.
func selfSignedPair(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "synthetic"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
}

func wantCode(t *testing.T, err error, want exitcode.Code) *exitcode.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error with exit code %d, got nil", want)
	}
	e, ok := exitcode.Of(err)
	if !ok {
		t.Fatalf("error is not typed: %v", err)
	}
	if e.Code != want {
		t.Fatalf("exit code = %d, want %d (reason %s)", e.Code, want, e.Reason)
	}
	return e
}

// wantReason returns the reason of a typed error, for refusals with no sentinel.
func wantReason(err error) exitcode.Reason {
	e, ok := exitcode.Of(err)
	if !ok {
		return ""
	}
	return e.Reason
}

func TestNewRequiresKubeconfig(t *testing.T) {
	_, err := New(Options{})
	e := wantCode(t, err, exitcode.Config)
	if e.Reason != exitcode.KubeconfigMissing {
		t.Fatalf("reason = %s, want %s", e.Reason, exitcode.KubeconfigMissing)
	}
}

func TestNewReportsMissingFile(t *testing.T) {
	_, err := New(Options{Kubeconfig: filepath.Join(t.TempDir(), "absent")})
	e := wantCode(t, err, exitcode.Config)
	if e.Reason != exitcode.KubeconfigMissing {
		t.Fatalf("reason = %s", e.Reason)
	}
}

func TestNewRefusesExecAuthAndNeverRunsIt(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "exec-ran")
	path := writeKubeconfig(t, clusterSpec{
		server: "https://192.0.2.10",
		mutate: func(cfg *clientcmdapi.Config) {
			cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{
				Exec: &clientcmdapi.ExecConfig{
					APIVersion: "client.authentication.k8s.io/v1",
					Command:    "/bin/sh",
					Args:       []string{"-c", "touch " + marker},
				},
			}
		},
	})
	_, err := New(Options{Kubeconfig: path})
	if !errors.Is(err, exitcode.ErrExecAuth) {
		t.Fatalf("err = %v, want the exec refusal", err)
	}
	_ = wantCode(t, err, exitcode.Config)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the exec plugin ran: marker exists (stat err %v)", statErr)
	}
}

func TestNewRefusesAuthProvider(t *testing.T) {
	path := writeKubeconfig(t, clusterSpec{
		server: "https://192.0.2.10",
		mutate: func(cfg *clientcmdapi.Config) {
			cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{
				AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "oidc"},
			}
		},
	})
	_, err := New(Options{Kubeconfig: path})
	if !errors.Is(err, exitcode.ErrAuthProvider) {
		t.Fatalf("err = %v, want the auth-provider refusal", err)
	}
	_ = wantCode(t, err, exitcode.Config)
}

func TestNewRefusesInsecureTLS(t *testing.T) {
	path := writeKubeconfig(t, clusterSpec{
		server: "https://192.0.2.10",
		mutate: func(cfg *clientcmdapi.Config) {
			cfg.Clusters["cluster"].InsecureSkipTLSVerify = true
		},
	})
	_, err := New(Options{Kubeconfig: path})
	if !errors.Is(err, exitcode.ErrInsecureTLS) {
		t.Fatalf("err = %v, want the insecure-TLS refusal", err)
	}
	_ = wantCode(t, err, exitcode.Config)
}

func TestNewRefusesProxyURL(t *testing.T) {
	path := writeKubeconfig(t, clusterSpec{
		server: "https://192.0.2.10",
		mutate: func(cfg *clientcmdapi.Config) {
			cfg.Clusters["cluster"].ProxyURL = "https://203.0.113.9:8443"
		},
	})
	_, err := New(Options{Kubeconfig: path})
	e := wantCode(t, err, exitcode.Config)
	if e.Reason != exitcode.KubeconfigInvalid {
		t.Fatalf("reason = %s", e.Reason)
	}
	if strings.Contains(e.Message, "203.0.113.9") {
		t.Fatalf("message leaks the proxy address: %q", e.Message)
	}
}

func TestNewRefusesPlaintextServer(t *testing.T) {
	path := writeKubeconfig(t, clusterSpec{server: "http://192.0.2.10"})
	_, err := New(Options{Kubeconfig: path})
	e := wantCode(t, err, exitcode.Config)
	if e.Reason != exitcode.KubeconfigInvalid {
		t.Fatalf("reason = %s", e.Reason)
	}
}

func TestEnvKubeconfigNeedsOptIn(t *testing.T) {
	srv, ca := tlsCluster(t, readyzHandler("ok\n"))
	path := writeKubeconfig(t, clusterSpec{server: srv.URL, ca: ca})
	t.Setenv("KUBECONFIG", path)

	if _, err := New(Options{}); wantReason(err) != exitcode.KubeconfigMissing {
		t.Fatalf("err = %v, want a kubeconfig_missing refusal without the opt-in", err)
	}
	if _, err := New(Options{AllowEnv: true}); err != nil {
		t.Fatalf("opt-in load failed: %v", err)
	}
}

func TestNewListsAvailableContexts(t *testing.T) {
	path := writeKubeconfig(t, clusterSpec{server: "https://192.0.2.10", context: "present"})
	_, err := New(Options{Kubeconfig: path, Context: "absent"})
	e := wantCode(t, err, exitcode.Config)
	if len(e.Available) != 1 || e.Available[0] != "present" {
		t.Fatalf("available = %v", e.Available)
	}
	if strings.Contains(e.Message, "present") {
		t.Fatalf("context names belong in Available, not in the printable message: %q", e.Message)
	}
}

func TestRefusalMessagesCarryNoLocation(t *testing.T) {
	const server = "https://192.0.2.10"
	// The kubeconfig path below a directory whose name would be an obvious tell.
	dir := filepath.Join(t.TempDir(), "private-marker")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dir, "kubeconfig")
	cfg := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cluster": {Server: server, InsecureSkipTLSVerify: true},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"user": {AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "gcp"}},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"synthetic": {Cluster: "cluster", AuthInfo: "user"},
		},
		CurrentContext: "synthetic",
	}
	if err := clientcmd.WriteToFile(cfg, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	_, err := New(Options{Kubeconfig: path})
	e := wantCode(t, err, exitcode.Config)
	for _, secret := range []string{"192.0.2.10", "private-marker"} {
		if strings.Contains(e.Message, secret) {
			t.Fatalf("message leaks %q: %q", secret, e.Message)
		}
	}
}

func TestNewAcceptsTokenAndClientCertificate(t *testing.T) {
	srv, ca := tlsCluster(t, readyzHandler("ok\n"))
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	withToken := writeKubeconfig(t, clusterSpec{
		server: srv.URL,
		ca:     ca,
		mutate: func(cfg *clientcmdapi.Config) {
			cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{TokenFile: tokenPath}
		},
	})
	if _, err := New(Options{Kubeconfig: withToken}); err != nil {
		t.Fatalf("tokenFile load failed: %v", err)
	}

	certPEM, keyPEM := selfSignedPair(t)
	withCert := writeKubeconfig(t, clusterSpec{
		server: srv.URL,
		ca:     ca,
		mutate: func(cfg *clientcmdapi.Config) {
			cfg.AuthInfos["user"] = &clientcmdapi.AuthInfo{
				ClientCertificateData: certPEM,
				ClientKeyData:         keyPEM,
			}
		},
	})
	if _, err := New(Options{Kubeconfig: withCert}); err != nil {
		t.Fatalf("client certificate load failed: %v", err)
	}
}

func TestAliasComesFromFlagOrContext(t *testing.T) {
	srv, ca := tlsCluster(t, readyzHandler("ok\n"))
	cases := []struct {
		name    string
		context string
		cluster string
		want    string
	}{
		{"flag wins", "synthetic", "tower", "tower"},
		{"valid context", "tower", "", "tower"},
		{"context with a host drops to unnamed", "admin@192.0.2.10", "", report.UnnamedAlias},
		{"flag skips an invalid context", "admin@192.0.2.10", "apiary", "apiary"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKubeconfig(t, clusterSpec{server: srv.URL, ca: ca, context: tc.context})
			client, err := New(Options{Kubeconfig: path, Cluster: tc.cluster})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := client.Cluster().Alias; got != tc.want {
				t.Fatalf("alias = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExpectedDefaultsToLight(t *testing.T) {
	srv, ca := tlsCluster(t, readyzHandler("ok\n"))
	path := writeKubeconfig(t, clusterSpec{server: srv.URL, ca: ca})

	client, err := New(Options{Kubeconfig: path})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if got := client.Cluster().Expected; got != report.Light {
		t.Fatalf("expected = %q, want light", got)
	}
	dark, err := New(Options{Kubeconfig: path, Expected: report.Dark})
	if err != nil {
		t.Fatalf("New dark: %v", err)
	}
	if got := dark.Cluster().Expected; got != report.Dark {
		t.Fatalf("expected = %q, want dark", got)
	}
}

func TestSplitPathList(t *testing.T) {
	got := splitPathList("a" + string(os.PathListSeparator) + string(os.PathListSeparator) + "b")
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("splitPathList = %v", got)
	}
}

// FuzzKubeconfigLoad proves a hostile kubeconfig cannot panic the loader, cannot
// slip an untyped error out, and cannot make the refusal message echo the path.
func FuzzKubeconfigLoad(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("apiVersion: v1\nkind: Config\n"))
	f.Add([]byte("apiVersion: v1\nkind: Config\nusers:\n- name: u\n  user:\n    token: t\n"))
	f.Add([]byte("apiVersion: v1\nkind: Config\nusers:\n- name: u\n  user:\n    exec:\n      command: /bin/sh\n"))
	f.Add([]byte("apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster:\n    server: http://192.0.2.10\n"))

	f.Fuzz(func(t *testing.T, data []byte) {
		dir := t.TempDir()
		path := filepath.Join(dir, "kubeconfig")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Skip()
		}
		_, err := New(Options{Kubeconfig: path, Timeout: 50 * time.Millisecond})
		if err == nil {
			return
		}
		if _, ok := exitcode.Of(err); !ok {
			t.Fatalf("untyped error: %v", err)
		}
		if strings.Contains(err.Error(), dir) {
			t.Fatalf("refusal leaks the kubeconfig path: %q", err.Error())
		}
	})
}
