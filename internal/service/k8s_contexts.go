package service

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"ai.engineer.runner/internal/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	clientcmdapi "k8s.io/client-go/tools/clientcmd/api"
)

// K8sInClusterContext is the context name reported when the gateway runs
// inside a cluster with its service account.
const K8sInClusterContext = "in-cluster"

const (
	k8sClientTimeout = 30 * time.Second
	k8sCheckTimeout  = 5 * time.Second
	k8sCheckParallel = 4
)

// K8sSource knows which contexts exist, which one is the default, which ones
// the agent may use, and how to build a client for each. Clients are cached.
type K8sSource struct {
	kind     string // "kubeconfig" | "in-cluster" | "static"
	contexts []types.K8sContext
	def      string
	allowed  func(string) bool
	build    func(name string, timeout time.Duration) (kubernetes.Interface, error)

	mu      sync.Mutex
	clients map[string]kubernetes.Interface
}

// LoadK8sSource uses the in-cluster service account when running in a pod,
// otherwise kubeconfig ($KUBECONFIG, then ~/.kube/config).
//
//	K8S_CONTEXT           default context (else kubeconfig's current-context)
//	K8S_ALLOWED_CONTEXTS  comma-separated contexts the agent may use, or "*";
//	                      unset means only the default context
func LoadK8sSource() (*K8sSource, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return inClusterSource(cfg), nil
	}
	if !errors.Is(err, rest.ErrNotInCluster) {
		return nil, fmt.Errorf("in-cluster config: %w", err)
	}

	raw, err := clientcmd.NewDefaultClientConfigLoadingRules().Load()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	if len(raw.Contexts) == 0 {
		return nil, errors.New("kubeconfig has no contexts (set KUBECONFIG or create ~/.kube/config)")
	}
	def := os.Getenv("K8S_CONTEXT")
	if def == "" {
		def = raw.CurrentContext
	}
	if def != "" && raw.Contexts[def] == nil {
		return nil, fmt.Errorf("default context %q (K8S_CONTEXT or current-context) is not in kubeconfig", def)
	}
	return kubeconfigSource(raw, def, parseAllowedContexts(os.Getenv("K8S_ALLOWED_CONTEXTS"), def)), nil
}

func inClusterSource(cfg *rest.Config) *K8sSource {
	return &K8sSource{
		kind:     "in-cluster",
		contexts: []types.K8sContext{{Name: K8sInClusterContext, Server: cfg.Host, Auth: "service-account"}},
		def:      K8sInClusterContext,
		allowed:  func(n string) bool { return n == K8sInClusterContext },
		build: func(_ string, timeout time.Duration) (kubernetes.Interface, error) {
			c := rest.CopyConfig(cfg)
			return newClientset(c, timeout)
		},
	}
}

func kubeconfigSource(raw *clientcmdapi.Config, def string, allowed func(string) bool) *K8sSource {
	var ctxs []types.K8sContext
	for name, c := range raw.Contexts {
		k := types.K8sContext{Name: name, Cluster: c.Cluster, User: c.AuthInfo, Namespace: c.Namespace}
		if cl := raw.Clusters[c.Cluster]; cl != nil {
			k.Server = cl.Server
		}
		k.Auth = authKind(raw.AuthInfos[c.AuthInfo])
		ctxs = append(ctxs, k)
	}
	return &K8sSource{
		kind: "kubeconfig", contexts: ctxs, def: def, allowed: allowed,
		build: func(name string, timeout time.Duration) (kubernetes.Interface, error) {
			cfg, err := clientcmd.NewNonInteractiveClientConfig(*raw, name, &clientcmd.ConfigOverrides{}, nil).ClientConfig()
			if err != nil {
				return nil, fmt.Errorf("context %q: %w", name, err)
			}
			return newClientset(cfg, timeout)
		},
	}
}

// NewStaticK8sSource serves ready clients by context name (tests, custom setups).
// Every given context is allowed; def is used when a call names none.
func NewStaticK8sSource(def string, clients map[string]kubernetes.Interface) *K8sSource {
	var ctxs []types.K8sContext
	for name := range clients {
		ctxs = append(ctxs, types.K8sContext{Name: name, Auth: "static"})
	}
	return &K8sSource{
		kind: "static", contexts: ctxs, def: def,
		allowed: func(n string) bool { _, ok := clients[n]; return ok },
		build: func(name string, _ time.Duration) (kubernetes.Interface, error) {
			return clients[name], nil
		},
	}
}

func newClientset(cfg *rest.Config, timeout time.Duration) (kubernetes.Interface, error) {
	cfg.QPS, cfg.Burst = 20, 40
	cfg.Timeout = timeout
	return kubernetes.NewForConfig(cfg)
}

// parseAllowedContexts: "" = only def, "*" = all, else the listed names.
func parseAllowedContexts(env, def string) func(string) bool {
	env = strings.TrimSpace(env)
	switch env {
	case "":
		return func(n string) bool { return n != "" && n == def }
	case "*":
		return func(string) bool { return true }
	}
	set := map[string]bool{}
	for _, n := range strings.Split(env, ",") {
		if n = strings.TrimSpace(n); n != "" {
			set[n] = true
		}
	}
	return func(n string) bool { return set[n] }
}

// authKind describes how a kubeconfig user authenticates, without credentials.
func authKind(a *clientcmdapi.AuthInfo) string {
	switch {
	case a == nil:
		return "unknown"
	case a.Exec != nil:
		return "exec:" + filepath.Base(a.Exec.Command)
	case a.AuthProvider != nil:
		return "auth-provider:" + a.AuthProvider.Name
	case a.Token != "" || a.TokenFile != "":
		return "token"
	case len(a.ClientCertificateData) > 0 || a.ClientCertificate != "":
		return "client-certificate"
	case a.Username != "":
		return "basic"
	default:
		return "none"
	}
}

// Default is the context used when a call doesn't name one.
func (s *K8sSource) Default() string { return s.def }

// Resolve turns the caller's context ("" = default) into an allowed, existing name.
// Errors never reveal contexts that aren't allowed.
func (s *K8sSource) Resolve(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		if s.def == "" {
			return "", errors.New("no default context (kubeconfig has no current-context and K8S_CONTEXT is unset); pass context, see k8s_list_contexts")
		}
		name = s.def
	}
	exists := slices.ContainsFunc(s.contexts, func(c types.K8sContext) bool { return c.Name == name })
	if !exists || !s.allowed(name) {
		if name == s.def && exists {
			return "", fmt.Errorf("default context %q is not in K8S_ALLOWED_CONTEXTS; pass context, see k8s_list_contexts", name)
		}
		return "", fmt.Errorf("context %q is unknown or not allowed; see k8s_list_contexts", name)
	}
	return name, nil
}

// Client returns a cached client for the context ("" = default) and its resolved name.
func (s *K8sSource) Client(name string) (kubernetes.Interface, string, error) {
	name, err := s.Resolve(name)
	if err != nil {
		return nil, "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c := s.clients[name]; c != nil {
		return c, name, nil
	}
	c, err := s.build(name, k8sClientTimeout)
	if err != nil {
		return nil, "", err // not cached: a fixed kubeconfig or login works on retry
	}
	if s.clients == nil {
		s.clients = map[string]kubernetes.Interface{}
	}
	s.clients[name] = c
	return c, name, nil
}

// ListK8sContexts lists the allowed contexts and, optionally, checks each one
// by asking its API server for its version.
func ListK8sContexts(s *K8sSource, in types.K8sContextListInput) (*types.K8sContextListResult, error) {
	res := &types.K8sContextListResult{Source: s.kind, Default: s.def}
	for _, c := range s.contexts {
		if !s.allowed(c.Name) {
			res.Hidden++
			continue
		}
		if in.NameContains != "" && !strings.Contains(c.Name, in.NameContains) {
			continue
		}
		c.Default = c.Name == s.def
		res.Contexts = append(res.Contexts, c)
	}
	slices.SortFunc(res.Contexts, func(a, b types.K8sContext) int { return strings.Compare(a.Name, b.Name) })

	reachable := 0
	if in.CheckConnectivity {
		sem := make(chan struct{}, k8sCheckParallel)
		var wg sync.WaitGroup
		for i := range res.Contexts {
			wg.Add(1)
			go func(c *types.K8sContext) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				checkContext(s, c)
			}(&res.Contexts[i])
		}
		wg.Wait()
		for _, c := range res.Contexts {
			if c.Reachable != nil && *c.Reachable {
				reachable++
			}
		}
	}

	res.Summary = fmt.Sprintf("%d contexts available from %s", len(res.Contexts), s.kind)
	if s.def != "" && s.allowed(s.def) {
		res.Summary += ", default " + s.def
	} else {
		res.Summary += ", no usable default: pass context to the k8s tools"
	}
	if in.CheckConnectivity {
		res.Summary += fmt.Sprintf("; %d of %d reachable", reachable, len(res.Contexts))
	}
	if res.Hidden > 0 {
		res.Summary += fmt.Sprintf("; %d more not allowed by K8S_ALLOWED_CONTEXTS", res.Hidden)
	}
	return res, nil
}

// checkContext asks the API server for its version, using a client with a
// short timeout so one dead cluster doesn't stall the whole call.
func checkContext(s *K8sSource, c *types.K8sContext) {
	ok := false
	c.Reachable = &ok
	cs, err := s.build(c.Name, k8sCheckTimeout)
	if err != nil {
		c.Error = trimErr(err)
		return
	}
	v, err := cs.Discovery().ServerVersion()
	if err != nil {
		c.Error = trimErr(err)
		return
	}
	ok, c.Version = true, v.GitVersion
}

func trimErr(err error) string {
	s := err.Error()
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}
