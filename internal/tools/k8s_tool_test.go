package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"ai.engineer.runner/internal/types"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func pod(ns, name, rs, hash string, init []string, images ...string) *corev1.Pod {
	ctrl := true
	p := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: map[string]string{"app": name}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if rs != "" {
		p.Labels["pod-template-hash"] = hash
		p.OwnerReferences = []metav1.OwnerReference{{Kind: "ReplicaSet", Name: rs, Controller: &ctrl}}
	}
	for i, img := range images {
		n := string(rune('a' + i))
		p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: n, Image: img})
		p.Status.ContainerStatuses = append(p.Status.ContainerStatuses, corev1.ContainerStatus{
			Name: n, Ready: true, ImageID: "docker-pullable://" + img + "@sha256:abc" + n,
		})
	}
	for i, img := range init {
		p.Spec.InitContainers = append(p.Spec.InitContainers, corev1.Container{Name: "init" + string(rune('a'+i)), Image: img})
	}
	return p
}

func connectK8s(t *testing.T, suite *K8sToolsSuite) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	mcp.AddTool(srv, K8sListImagesTool, suite.RunListK8sImages)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func callK8s(t *testing.T, cs *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, types.K8sImageListResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "k8s_list_images", Arguments: args})
	if err != nil {
		t.Fatalf("%v: protocol error %v", args, err)
	}
	var out types.K8sImageListResult
	if !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
	}
	return res, out
}

func fakeCluster() *fake.Clientset {
	return fake.NewClientset(
		pod("prod", "api-1", "api-7f9c", "7f9c", []string{"busybox:1.36"}, "ghcr.io/acme/api:1.2.0", "nginx"),
		pod("prod", "api-2", "api-7f9c", "7f9c", nil, "ghcr.io/acme/api:1.2.0", "nginx"),
		pod("dev", "web", "", "", nil, "nginx:latest"),
	)
}

func TestK8sListImagesGroupByImage(t *testing.T) {
	cs := connectK8s(t, NewK8sToolsSuiteWithClient(fakeCluster()))
	res, out := callK8s(t, cs, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if out.PodsScanned != 3 || out.TotalImages != 4 {
		t.Fatalf("got pods=%d images=%d: %+v", out.PodsScanned, out.TotalImages, out)
	}
	top := out.Images[0] // ties on pods=2 sort by name
	if top.Image != "ghcr.io/acme/api:1.2.0" || top.Registry != "ghcr.io" || top.Repository != "acme/api" || top.Tag != "1.2.0" {
		t.Errorf("bad parse: %+v", top)
	}
	if len(top.Workloads) != 1 || top.Workloads[0] != "prod/Deployment/api" {
		t.Errorf("workloads = %v", top.Workloads)
	}
	if len(top.Running) != 1 {
		t.Errorf("running digests = %v", top.Running)
	}
}

func TestK8sListImagesFilters(t *testing.T) {
	cs := connectK8s(t, NewK8sToolsSuiteWithClient(fakeCluster()))

	_, out := callK8s(t, cs, map[string]any{"namespace": "dev"})
	if out.PodsScanned != 1 || out.TotalImages != 1 || out.Images[0].Repository != "library/nginx" {
		t.Errorf("namespace filter: %+v", out)
	}

	_, out = callK8s(t, cs, map[string]any{"container_kind": "init"})
	if out.TotalImages != 1 || out.Images[0].Image != "busybox:1.36" {
		t.Errorf("init filter: %+v", out)
	}

	_, out = callK8s(t, cs, map[string]any{"image_contains": "acme", "group_by": "pod"})
	if len(out.Pods) != 2 || len(out.Pods[0].Containers) != 1 {
		t.Errorf("group_by pod: %+v", out)
	}

	_, out = callK8s(t, cs, map[string]any{"label_selector": "app=web"})
	if out.PodsScanned != 1 {
		t.Errorf("label selector: %+v", out)
	}

	_, out = callK8s(t, cs, map[string]any{"max_results": 1})
	if len(out.Images) != 1 || !out.Truncated {
		t.Errorf("max_results: %+v", out)
	}
}

func TestK8sListImagesRejectsBadInput(t *testing.T) {
	cs := connectK8s(t, NewK8sToolsSuiteWithClient(fakeCluster()))
	for _, args := range []map[string]any{
		{"group_by": "node"},
		{"container_kind": "sidecar"},
		{"namespace": "Bad_NS"},
		{"label_selector": "app in ("},
		{"field_selector": "status.phase"},
		{"max_results": 5000},
	} {
		if res, _ := callK8s(t, cs, args); !res.IsError {
			t.Errorf("%v: expected tool error", args)
		}
	}
}

func TestK8sNoClusterIsToolError(t *testing.T) {
	suite := &K8sToolsSuite{newClient: func() (kubernetes.Interface, string, error) {
		return nil, "", errors.New("no kubeconfig")
	}}
	cs := connectK8s(t, suite)
	if res, _ := callK8s(t, cs, map[string]any{}); !res.IsError {
		t.Fatal("expected tool error without a cluster")
	}
}

func ns(name string, phase corev1.NamespacePhase, lbls map[string]string) *corev1.Namespace {
	l := map[string]string{"kubernetes.io/metadata.name": name}
	for k, v := range lbls {
		l[k] = v
	}
	return &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l, CreationTimestamp: metav1.Now()},
		Status:     corev1.NamespaceStatus{Phase: phase},
	}
}

func nsCluster() *fake.Clientset {
	crash := pod("prod", "worker", "", "", nil, "acme/worker:2")
	crash.Status.ContainerStatuses[0].Ready = false
	crash.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}
	crash.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}}
	failed := pod("dev", "job", "", "", nil, "busybox")
	failed.Status.Phase = corev1.PodFailed
	return fake.NewClientset(
		ns("prod", corev1.NamespaceActive, map[string]string{"team": "payments"}),
		ns("dev", corev1.NamespaceActive, map[string]string{"team": "web"}),
		ns("old", corev1.NamespaceTerminating, nil),
		ns("kube-system", corev1.NamespaceActive, nil),
		pod("prod", "api-1", "api-7f9c", "7f9c", nil, "ghcr.io/acme/api:1.2.0"),
		crash, failed,
		pod("kube-system", "coredns", "", "", nil, "coredns:1.11"),
	)
}

func callNS(t *testing.T, cs *mcp.ClientSession, args map[string]any) (*mcp.CallToolResult, types.K8sNameSpaceListResult) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "k8s_list_namespaces", Arguments: args})
	if err != nil {
		t.Fatalf("%v: protocol error %v", args, err)
	}
	var out types.K8sNameSpaceListResult
	if !res.IsError {
		b, _ := json.Marshal(res.StructuredContent)
		if err := json.Unmarshal(b, &out); err != nil {
			t.Fatal(err)
		}
	}
	return res, out
}

func connectNS(t *testing.T, c kubernetes.Interface) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	suite := NewK8sToolsSuiteWithClient(c)
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	mcp.AddTool(srv, K8sListNameSpaceTool, suite.RunListK8sNameSpace)
	ct, st := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "c", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestK8sListNamespaces(t *testing.T) {
	cs := connectNS(t, nsCluster())
	res, out := callNS(t, cs, map[string]any{})
	if res.IsError {
		t.Fatalf("unexpected error: %+v", res.Content)
	}
	if out.Total != 4 || out.PodsScanned != 4 {
		t.Fatalf("total=%d pods=%d: %+v", out.Total, out.PodsScanned, out)
	}
	byName := map[string]types.K8sNamespace{}
	for _, n := range out.Namespaces {
		byName[n.Name] = n
	}
	prod := byName["prod"]
	if prod.Pods == nil || prod.Pods.Total != 2 || prod.Pods.Running != 2 || prod.Pods.CrashLooping != 1 || prod.Pods.NotReady != 1 {
		t.Errorf("prod pods = %+v", prod.Pods)
	}
	if _, ok := prod.Labels["kubernetes.io/metadata.name"]; ok || prod.Labels["team"] != "payments" {
		t.Errorf("prod labels = %v", prod.Labels)
	}
	if byName["dev"].Pods.Failed != 1 || !byName["kube-system"].System || byName["old"].Phase != "Terminating" {
		t.Errorf("namespaces = %+v", byName)
	}
	if !strings.Contains(out.Summary, "dev, prod") {
		t.Errorf("summary = %q", out.Summary)
	}
}

func TestK8sListNamespacesFilters(t *testing.T) {
	cs := connectNS(t, nsCluster())
	for _, tc := range []struct {
		args map[string]any
		want []string
	}{
		{map[string]any{"exclude_system": true}, []string{"dev", "old", "prod"}},
		{map[string]any{"label_selector": "team=payments"}, []string{"prod"}},
		{map[string]any{"name_contains": "kube"}, []string{"kube-system"}},
		{map[string]any{"phase": "Terminating"}, []string{"old"}},
		{map[string]any{"max_results": 2}, []string{"dev", "kube-system"}},
	} {
		_, out := callNS(t, cs, tc.args)
		var got []string
		for _, n := range out.Namespaces {
			got = append(got, n.Name)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("%v: got %v want %v", tc.args, got, tc.want)
		}
	}
	if _, out := callNS(t, cs, map[string]any{"skip_pod_counts": true}); out.PodsScanned != 0 || out.Namespaces[0].Pods != nil {
		t.Errorf("skip_pod_counts: %+v", out)
	}
	for _, args := range []map[string]any{{"phase": "Gone"}, {"label_selector": "a in ("}, {"max_results": 5000}} {
		if res, _ := callNS(t, cs, args); !res.IsError {
			t.Errorf("%v: expected tool error", args)
		}
	}
}
