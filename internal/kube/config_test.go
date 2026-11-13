package kube

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/fakecluster"
	"github.com/Hedronite/scry/internal/report"
)

// writeKubeconfigAt writes a minimal synthetic kubeconfig at path. No credential
// or address it holds is real.
func writeKubeconfigAt(t *testing.T, path string, mutate func(*clientcmdapi.Config)) string {
	t.Helper()
	config := clientcmdapi.Config{
		Clusters: map[string]*clientcmdapi.Cluster{
			"cluster": {Server: "https://apiserver.invalid"},
		},
		AuthInfos: map[string]*clientcmdapi.AuthInfo{
			"user": {Token: "synthetic-token"},
		},
		Contexts: map[string]*clientcmdapi.Context{
			"lab-one": {Cluster: "cluster", AuthInfo: "user"},
		},
		CurrentContext: "lab-one",
	}
	if mutate != nil {
		mutate(&config)
	}
	if err := clientcmd.WriteToFile(config, path); err != nil {
		t.Fatalf("write kubeconfig: %v", err)
	}
	return path
}

func writeKubeconfigFile(t *testing.T, mutate func(*clientcmdapi.Config)) string {
	t.Helper()
	return writeKubeconfigAt(t, filepath.Join(t.TempDir(), "kubeconfig"), mutate)
}

// writeBareKubeconfig points a kubeconfig at server with no fixture behind it.
func writeBareKubeconfig(t *testing.T, server string, mutate func(*clientcmdapi.Config)) string {
	t.Helper()
	return writeKubeconfigFile(t, func(config *clientcmdapi.Config) {
		config.Clusters["cluster"].Server = server
		if mutate != nil {
			mutate(config)
		}
	})
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
	if reason := wantCode(t, err, exitcode.Config).Reason; reason != exitcode.KubeconfigMissing {
		t.Fatalf("reason = %s, want %s", reason, exitcode.KubeconfigMissing)
	}
}

func TestNewReportsMissingFile(t *testing.T) {
	_, err := New(Options{Kubeconfig: filepath.Join(t.TempDir(), "absent")})
	if reason := wantCode(t, err, exitcode.Config).Reason; reason != exitcode.KubeconfigMissing {
		t.Fatalf("reason = %s", reason)
	}
}

func TestNewRefusesExecAuthAndNeverRunsIt(t *testing.T) {
	server, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)
	marker := filepath.Join(t.TempDir(), "exec-ran")
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(path, func(config *clientcmdapi.Config) {
		config.AuthInfos["user"] = &clientcmdapi.AuthInfo{
			Exec: &clientcmdapi.ExecConfig{
				APIVersion: "client.authentication.k8s.io/v1",
				Command:    "/bin/sh",
				Args:       []string{"-c", "touch " + marker},
			},
		}
	}); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}

	_, err = New(Options{Kubeconfig: path})
	if !errors.Is(err, exitcode.ErrExecAuth) {
		t.Fatalf("err = %v, want the exec refusal", err)
	}
	_ = wantCode(t, err, exitcode.Config)
	if _, statErr := os.Stat(marker); !os.IsNotExist(statErr) {
		t.Fatalf("the exec plugin ran: marker exists (stat err %v)", statErr)
	}
	if connections := server.Connections(); connections != 0 {
		t.Fatalf("a refused kubeconfig opened %d connection(s)", connections)
	}
}

func TestNewRefusesAuthProvider(t *testing.T) {
	path := writeKubeconfigFile(t, func(config *clientcmdapi.Config) {
		config.AuthInfos["user"] = &clientcmdapi.AuthInfo{
			AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "oidc"},
		}
	})
	_, err := New(Options{Kubeconfig: path})
	if !errors.Is(err, exitcode.ErrAuthProvider) {
		t.Fatalf("err = %v, want the auth-provider refusal", err)
	}
	_ = wantCode(t, err, exitcode.Config)
}

func TestNewRefusesInsecureTLS(t *testing.T) {
	path := writeKubeconfigFile(t, func(config *clientcmdapi.Config) {
		config.Clusters["cluster"].InsecureSkipTLSVerify = true
	})
	_, err := New(Options{Kubeconfig: path})
	if !errors.Is(err, exitcode.ErrInsecureTLS) {
		t.Fatalf("err = %v, want the insecure-TLS refusal", err)
	}
	_ = wantCode(t, err, exitcode.Config)
}

func TestNewRefusesProxyURL(t *testing.T) {
	path := writeKubeconfigFile(t, func(config *clientcmdapi.Config) {
		config.Clusters["cluster"].ProxyURL = "https://proxy.invalid:8443"
	})
	_, err := New(Options{Kubeconfig: path})
	e := wantCode(t, err, exitcode.Config)
	if e.Reason != exitcode.KubeconfigInvalid {
		t.Fatalf("reason = %s", e.Reason)
	}
	if strings.Contains(e.Message, "proxy.invalid") {
		t.Fatalf("message leaks the proxy address: %q", e.Message)
	}
}

func TestNewRefusesPlaintextServer(t *testing.T) {
	path := writeBareKubeconfig(t, "http://apiserver.invalid", nil)
	_, err := New(Options{Kubeconfig: path})
	if reason := wantCode(t, err, exitcode.Config).Reason; reason != exitcode.KubeconfigInvalid {
		t.Fatalf("reason = %s", reason)
	}
}

func TestEnvKubeconfigNeedsOptIn(t *testing.T) {
	server, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(path, nil); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	t.Setenv("KUBECONFIG", path)

	if _, err := New(Options{}); wantReason(err) != exitcode.KubeconfigMissing {
		t.Fatalf("err = %v, want a kubeconfig_missing refusal without the opt-in", err)
	}
	if _, err := New(Options{AllowEnv: true}); err != nil {
		t.Fatalf("opt-in load failed: %v", err)
	}
}

func TestNewListsAvailableContexts(t *testing.T) {
	path := writeKubeconfigFile(t, func(config *clientcmdapi.Config) {
		config.Contexts["lab-two"] = &clientcmdapi.Context{Cluster: "cluster", AuthInfo: "user"}
	})
	_, err := New(Options{Kubeconfig: path, Context: "absent"})
	e := wantCode(t, err, exitcode.Config)
	if len(e.Available) != 2 || e.Available[0] != "lab-one" || e.Available[1] != "lab-two" {
		t.Fatalf("available = %v", e.Available)
	}
	if strings.Contains(e.Message, "lab-one") {
		t.Fatalf("context names belong in Available, not in the printable message: %q", e.Message)
	}
}

func TestRefusalMessagesCarryNoLocation(t *testing.T) {
	const server = "https://apiserver.invalid"
	dir := filepath.Join(t.TempDir(), "secret-dir")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := writeKubeconfigAt(t, filepath.Join(dir, "kubeconfig"), func(config *clientcmdapi.Config) {
		config.Clusters["cluster"] = &clientcmdapi.Cluster{Server: server, InsecureSkipTLSVerify: true}
		config.AuthInfos["user"] = &clientcmdapi.AuthInfo{
			AuthProvider: &clientcmdapi.AuthProviderConfig{Name: "gcp"},
		}
	})
	_, err := New(Options{Kubeconfig: path})
	e := wantCode(t, err, exitcode.Config)
	for _, secret := range []string{"apiserver.invalid", "secret-dir"} {
		if strings.Contains(e.Message, secret) {
			t.Fatalf("message leaks %q: %q", secret, e.Message)
		}
	}
}

func TestNewAcceptsTokenAndClientCertificate(t *testing.T) {
	server, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)
	tokenPath := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenPath, []byte("synthetic-token"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	withToken := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(withToken, func(config *clientcmdapi.Config) {
		config.AuthInfos["user"] = &clientcmdapi.AuthInfo{TokenFile: tokenPath}
	}); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	if _, err := New(Options{Kubeconfig: withToken}); err != nil {
		t.Fatalf("tokenFile load failed: %v", err)
	}

	certPEM, keyPEM, err := fakecluster.NewClientCert()
	if err != nil {
		t.Fatalf("client certificate: %v", err)
	}
	withCert := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(withCert, func(config *clientcmdapi.Config) {
		config.AuthInfos["user"] = &clientcmdapi.AuthInfo{
			ClientCertificateData: certPEM,
			ClientKeyData:         keyPEM,
		}
	}); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}
	if _, err := New(Options{Kubeconfig: withCert}); err != nil {
		t.Fatalf("client certificate load failed: %v", err)
	}
}

func TestAliasComesFromFlagOrContext(t *testing.T) {
	server, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)

	cases := []struct {
		name    string
		context string
		cluster string
		want    string
	}{
		{name: "flag wins", context: "lab-one", cluster: "lab-two", want: "lab-two"},
		{name: "valid context", context: "lab-one", want: "lab-one"},
		{name: "context with a host drops to unnamed", context: "admin@apiserver.invalid", want: report.UnnamedAlias},
		{name: "flag skips an invalid context", context: "admin@apiserver.invalid", cluster: "lab-two", want: "lab-two"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "kubeconfig")
			if _, err := server.Kubeconfig(path, func(config *clientcmdapi.Config) {
				config.Contexts = map[string]*clientcmdapi.Context{
					tc.context: {Cluster: "cluster", AuthInfo: "user"},
				}
				config.CurrentContext = tc.context
			}); err != nil {
				t.Fatalf("kubeconfig: %v", err)
			}
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
	server, err := fakecluster.New(fakecluster.Options{})
	if err != nil {
		t.Fatalf("fake cluster: %v", err)
	}
	t.Cleanup(server.Close)
	path := filepath.Join(t.TempDir(), "kubeconfig")
	if _, err := server.Kubeconfig(path, nil); err != nil {
		t.Fatalf("kubeconfig: %v", err)
	}

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
	if timeout := client.timeout; timeout != DefaultTimeout {
		t.Fatalf("timeout = %s, want %s", timeout, DefaultTimeout)
	}
	if timeout := mustClient(t, Options{Kubeconfig: path, Timeout: 3 * time.Second}).timeout; timeout != 3*time.Second {
		t.Fatalf("timeout = %s, want 3s", timeout)
	}
}

func mustClient(t *testing.T, o Options) *Client {
	t.Helper()
	client, err := New(o)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
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
	f.Add([]byte("apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster:\n    server: http://apiserver.invalid\n"))

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
