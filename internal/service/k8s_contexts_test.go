package service

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai.engineer.runner/internal/types"
)

const testKubeconfig = `apiVersion: v1
kind: Config
current-context: kind-dev
clusters:
- name: kind-dev
  cluster: { server: "https://127.0.0.1:6443", insecure-skip-tls-verify: true }
- name: prod
  cluster: { server: "https://prod.example.com" }
users:
- name: kind-dev
  user: { token: SUPER-SECRET-TOKEN }
- name: prod-ro
  user:
    exec: { apiVersion: client.authentication.k8s.io/v1beta1, command: /usr/local/bin/aws, args: [eks, get-token] }
- name: certs
  user: { client-certificate-data: Y2VydA==, client-key-data: a2V5 }
contexts:
- name: kind-dev
  context: { cluster: kind-dev, user: kind-dev }
- name: prod
  context: { cluster: prod, user: prod-ro, namespace: payments }
- name: staging
  context: { cluster: prod, user: certs }
`

func loadTestSource(t *testing.T, allowed, def string) *K8sSource {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(testKubeconfig), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("KUBECONFIG", path)
	t.Setenv("K8S_ALLOWED_CONTEXTS", allowed)
	t.Setenv("K8S_CONTEXT", def)
	t.Setenv("KUBERNETES_SERVICE_HOST", "") // never pick in-cluster config in tests
	src, err := LoadK8sSource()
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func names(cs []types.K8sContext) string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return strings.Join(out, ",")
}

func TestK8sAllowedContexts(t *testing.T) {
	for _, tc := range []struct {
		allowed, def, want string
		hidden             int
	}{
		{"", "", "kind-dev", 2},               // unset: only current-context
		{"", "prod", "prod", 2},               // unset: only K8S_CONTEXT
		{"*", "", "kind-dev,prod,staging", 0}, // everything
		{"prod, staging", "", "prod,staging", 1},
	} {
		src := loadTestSource(t, tc.allowed, tc.def)
		res, err := ListK8sContexts(src, types.K8sContextListInput{})
		if err != nil {
			t.Fatal(err)
		}
		if got := names(res.Contexts); got != tc.want || res.Hidden != tc.hidden {
			t.Errorf("allowed=%q def=%q: got %s hidden=%d, want %s hidden=%d", tc.allowed, tc.def, got, res.Hidden, tc.want, tc.hidden)
		}
	}
}

func TestK8sContextDetailsHideCredentials(t *testing.T) {
	src := loadTestSource(t, "*", "")
	res, _ := ListK8sContexts(src, types.K8sContextListInput{})
	auth := map[string]string{}
	for _, c := range res.Contexts {
		auth[c.Name] = c.Auth
	}
	if auth["kind-dev"] != "token" || auth["prod"] != "exec:aws" || auth["staging"] != "client-certificate" {
		t.Errorf("auth kinds = %v", auth)
	}
	b, _ := json.Marshal(res)
	for _, secret := range []string{"SUPER-SECRET-TOKEN", "Y2VydA==", "a2V5", "get-token"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("output leaks %q: %s", secret, b)
		}
	}
	if res.Contexts[1].Namespace != "payments" || res.Contexts[1].Server != "https://prod.example.com" || !res.Contexts[0].Default {
		t.Errorf("details = %+v", res.Contexts)
	}
}

func TestK8sResolveAndClient(t *testing.T) {
	src := loadTestSource(t, "prod", "")
	// current-context kind-dev exists but isn't allowed: no usable default.
	if _, err := src.Resolve(""); err == nil || !strings.Contains(err.Error(), "K8S_ALLOWED_CONTEXTS") {
		t.Errorf("default not allowed: err = %v", err)
	}
	// Disallowed and unknown contexts get the same error, so names don't leak.
	_, errStaging := src.Resolve("staging")
	_, errNope := src.Resolve("nope")
	if errStaging == nil || errNope == nil || strings.Replace(errStaging.Error(), "staging", "X", 1) != strings.Replace(errNope.Error(), "nope", "X", 1) {
		t.Errorf("staging=%v nope=%v", errStaging, errNope)
	}
	c1, name, err := src.Client("prod")
	if err != nil || name != "prod" {
		t.Fatalf("client prod: %v %q", err, name)
	}
	if c2, _, _ := src.Client("prod"); c2 != c1 {
		t.Error("client not cached")
	}
}

func TestK8sDefaultContextMustExist(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config")
	_ = os.WriteFile(path, []byte(testKubeconfig), 0o600)
	t.Setenv("KUBECONFIG", path)
	t.Setenv("K8S_CONTEXT", "missing")
	t.Setenv("KUBERNETES_SERVICE_HOST", "")
	if _, err := LoadK8sSource(); err == nil {
		t.Fatal("expected error for unknown K8S_CONTEXT")
	}
}
