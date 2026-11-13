package kube

import (
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"

	coreclient "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"

	"github.com/Hedronite/scry/internal/exitcode"
	"github.com/Hedronite/scry/internal/report"
)

const (
	// DefaultTimeout bounds one request (spec section 6).
	DefaultTimeout = 5 * time.Second

	userAgent = "scry"
	qps       = 2
	burst     = 3
)

// Options is the resolved flag and config state a client is built from. The CLI
// owns flag parsing and config-file lookup; this package owns everything the
// kubeconfig implies.
type Options struct {
	Kubeconfig string          // explicit path from --kubeconfig
	AllowEnv   bool            // --allow-env-kubeconfig
	Context    string          // --context; empty means the file's current-context
	Cluster    string          // --cluster alias override
	Expected   report.Expected // declared apiserver state; empty means light
	Timeout    time.Duration   // per request; zero means DefaultTimeout
}

// Client holds the allowlisted transport, the one typed client Scry uses, and the
// cluster identity the document carries. The client is the core v1 group alone:
// the clientset aggregate and discovery are not linked, so the binary and the
// module graph stay small.
type Client struct {
	base        *url.URL
	http        *http.Client
	nodesClient coreclient.CoreV1Interface
	cluster     report.Cluster
	timeout     time.Duration
}

// New loads the kubeconfig, applies every refusal, and returns a client whose
// only transport is the allowlist. Every failure is an *exitcode.Error; the
// message never contains the kubeconfig path, the server URL or file contents.
func New(o Options) (*Client, error) {
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	cfg, contextName, err := load(o)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(cfg.Host)
	if err != nil || base.Host == "" {
		return nil, exitcode.New(exitcode.KubeconfigInvalid, "the kubeconfig server field is not a usable URL")
	}
	if base.Scheme != "https" {
		return nil, exitcode.New(exitcode.KubeconfigInvalid, "the kubeconfig server must be an https URL: TLS is always on")
	}
	cfg.Timeout = timeout
	cfg.UserAgent = userAgent
	cfg.QPS = qps
	cfg.Burst = burst

	// The allowlist goes outermost: it sees every request any client makes,
	// including redirect hops, and refuses before the network.
	previousWrap := cfg.WrapTransport
	cfg.WrapTransport = func(rt http.RoundTripper) http.RoundTripper {
		if previousWrap != nil {
			rt = previousWrap(rt)
		}
		return allowedTransport{next: rt, scheme: base.Scheme, host: base.Host}
	}
	cfg.Dial = traceDial(cfg.Dial)
	// Never route through an environment proxy: the credential would travel to
	// a third party on its first request. A nil-returning Proxy func means "no
	// proxy", which is a different thing from a nil Proxy func (env lookup).
	cfg.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }

	transport, err := rest.TransportFor(cfg)
	if err != nil {
		return nil, tlsMaterialError(err)
	}
	nodesClient, err := coreclient.NewForConfig(cfg)
	if err != nil {
		return nil, tlsMaterialError(err)
	}
	expected := report.Light
	if o.Expected == report.Dark {
		expected = report.Dark
	}
	return &Client{
		base: base,
		http: &http.Client{
			Transport:     transport,
			CheckRedirect: refuseRedirect,
			Timeout:       timeout,
		},
		nodesClient: nodesClient,
		cluster:     report.Cluster{Alias: report.ResolveAlias(o.Cluster, contextName), Expected: expected},
		timeout:     timeout,
	}, nil
}

// Cluster is the identity the document carries.
func (c *Client) Cluster() report.Cluster { return c.cluster }

// load resolves the kubeconfig, refuses the auth kinds Scry will not use, and
// returns the rest config plus the context name (used for the alias).
func load(o Options) (*rest.Config, string, error) {
	rules, err := loadingRules(o)
	if err != nil {
		return nil, "", err
	}
	overrides := &clientcmd.ConfigOverrides{CurrentContext: o.Context}
	cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)

	raw, err := cc.RawConfig()
	if err != nil {
		return nil, "", kubeconfigError()
	}
	if err := checkRefusals(raw); err != nil {
		return nil, "", err
	}
	contextName := o.Context
	if contextName == "" {
		contextName = raw.CurrentContext
	}
	if contextName != "" {
		if _, ok := raw.Contexts[contextName]; !ok {
			return nil, "", missingContext(contextName, raw.Contexts)
		}
	}

	cfg, err := cc.ClientConfig()
	if err != nil {
		return nil, "", kubeconfigError()
	}
	// Defence in depth: a client-go path that slipped past the scan above still
	// cannot carry an exec plugin or an insecure TLS setting into a request.
	if cfg.ExecProvider != nil {
		return nil, "", execRefused()
	}
	if cfg.AuthProvider != nil {
		return nil, "", authProviderRefused(cfg.AuthProvider.Name)
	}
	if cfg.Insecure {
		return nil, "", insecureTLSRefused()
	}
	return cfg, contextName, nil
}

// loadingRules honours --kubeconfig first, then KUBECONFIG only when the caller
// opted in. There is no home-directory fallback in either case.
func loadingRules(o Options) (*clientcmd.ClientConfigLoadingRules, error) {
	if o.Kubeconfig != "" {
		if _, err := os.Stat(o.Kubeconfig); err != nil {
			return nil, exitcode.New(exitcode.KubeconfigMissing,
				"--kubeconfig points at a file that cannot be read")
		}
		return &clientcmd.ClientConfigLoadingRules{ExplicitPath: o.Kubeconfig}, nil
	}
	if o.AllowEnv {
		if env := os.Getenv("KUBECONFIG"); env != "" {
			return &clientcmd.ClientConfigLoadingRules{Precedence: splitPathList(env)}, nil
		}
		return nil, exitcode.New(exitcode.KubeconfigMissing,
			"--allow-env-kubeconfig was given but KUBECONFIG is not set")
	}
	return nil, exitcode.New(exitcode.KubeconfigMissing,
		"pass --kubeconfig <path> (or --allow-env-kubeconfig to read KUBECONFIG)")
}

// checkRefusals scans the merged kubeconfig before any client is built. Keys are
// sorted so the reported refusal is deterministic.
func checkRefusals(raw clientcmdapi.Config) error {
	for _, name := range sortedKeys(raw.AuthInfos) {
		user := raw.AuthInfos[name]
		if user == nil {
			continue
		}
		if user.Exec != nil {
			return execRefused()
		}
		if user.AuthProvider != nil {
			return authProviderRefused(user.AuthProvider.Name)
		}
	}
	for _, name := range sortedKeys(raw.Clusters) {
		cluster := raw.Clusters[name]
		if cluster == nil {
			continue
		}
		if cluster.InsecureSkipTLSVerify {
			return insecureTLSRefused()
		}
		if cluster.ProxyURL != "" {
			// A proxy would carry the credential and every request to a host the
			// operator did not name as the cluster.
			return exitcode.New(exitcode.KubeconfigInvalid,
				"the kubeconfig sets clusters[].proxy-url; scry connects to the apiserver directly and uses no proxy")
		}
	}
	return nil
}

func execRefused() *exitcode.Error {
	return exitcode.New(exitcode.ExecAuthRefused,
		"the kubeconfig uses an exec credential plugin (users[].exec); scry refuses exec auth and accepts a client certificate or a static token only")
}

func authProviderRefused(name string) *exitcode.Error {
	provider := ""
	if name != "" {
		provider = " " + printableToken(name)
	}
	return exitcode.New(exitcode.AuthProviderRefuse,
		"the kubeconfig uses an auth-provider plugin (users[].auth-provider%s); scry accepts a client certificate or a static token only", provider)
}

func insecureTLSRefused() *exitcode.Error {
	return exitcode.New(exitcode.InsecureTLSRefused,
		"the kubeconfig sets insecure-skip-tls-verify; scry always verifies TLS and has no unsafe flag")
}

// kubeconfigError is the single message for an unreadable or malformed file. It
// deliberately omits the path: the path is not the server's business.
func kubeconfigError() *exitcode.Error {
	return exitcode.New(exitcode.KubeconfigInvalid,
		"the kubeconfig given with --kubeconfig could not be loaded")
}

// missingContext names the miss and lists what is there.
func missingContext(name string, contexts map[string]*clientcmdapi.Context) *exitcode.Error {
	available := make([]string, 0, len(contexts))
	for contextName := range contexts {
		available = append(available, contextName)
	}
	sort.Strings(available)
	// Context names are kubeconfig contents (they often embed a host or an
	// email), so they travel in Available for the CLI to render and never in the
	// message, which may be logged.
	message := "the kubeconfig has no such context"
	if len(available) == 0 {
		message = "the kubeconfig defines no contexts"
	}
	e := exitcode.New(exitcode.KubeconfigInvalid, "%s", message)
	e.Available = available
	return e
}

// tlsMaterialError maps a client build failure. A bad CA file or certificate in
// the kubeconfig is a config error (3), not a server verdict (5).
func tlsMaterialError(err error) *exitcode.Error {
	return exitcode.Wrap(exitcode.KubeconfigInvalid, err,
		"the kubeconfig TLS material could not be loaded")
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// splitPathList splits KUBECONFIG the way the OS does, dropping empty entries.
func splitPathList(list string) []string {
	parts := strings.Split(list, string(os.PathListSeparator))
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}
