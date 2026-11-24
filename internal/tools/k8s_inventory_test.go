package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ai.engineer.runner/internal/service"
	"ai.engineer.runner/internal/types"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"
)

func ptr[T any](v T) *T { return &v }

func meta(ns, name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Namespace: ns, Name: name, CreationTimestamp: metav1.NewTime(time.Now().Add(-48 * time.Hour)), Generation: 1}
}

// connectInventory serves every inventory tool against the given clients.
func connectInventory(t *testing.T, c *service.K8sClients) *mcp.ClientSession {
	t.Helper()
	suite := NewK8sToolsSuiteWithSource(service.NewStaticK8sSourceClients("test", map[string]*service.K8sClients{"test": c}))
	srv := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	for _, tool := range k8sInventoryTools {
		suite.addInventoryTool(srv, tool.Name)
	}
	ctx := context.Background()
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

// call runs a tool and decodes its structured result into out. It fails the
// test on a tool error unless wantErr.
func call(t *testing.T, cs *mcp.ClientSession, tool string, args map[string]any, out any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s %v: protocol error %v", tool, args, err)
	}
	if res.IsError {
		return res
	}
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatal(err)
	}
	return res
}

func mustOK(t *testing.T, res *mcp.CallToolResult) {
	t.Helper()
	if res.IsError {
		t.Fatalf("unexpected tool error: %+v", res.Content[0])
	}
}

func node(name string, ready corev1.ConditionStatus, cordoned bool, roles ...string) *corev1.Node {
	n := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"topology.kubernetes.io/zone": "z1"}, CreationTimestamp: metav1.Now()},
		Spec:       corev1.NodeSpec{Unschedulable: cordoned},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: ready}},
			NodeInfo:   corev1.NodeSystemInfo{KubeletVersion: "v1.33.1", Architecture: "arm64"},
			Capacity:   corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi"), corev1.ResourcePods: resource.MustParse("110")},
			Allocatable: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourceMemory: resource.MustParse("16Gi"),
				corev1.ResourcePods: resource.MustParse("110")},
		},
	}
	for _, r := range roles {
		n.Labels["node-role.kubernetes.io/"+r] = ""
	}
	return n
}

func TestK8sListNodes(t *testing.T) {
	bad := node("w2", corev1.ConditionFalse, true, "worker")
	bad.Status.Conditions = append(bad.Status.Conditions, corev1.NodeCondition{Type: corev1.NodeMemoryPressure, Status: corev1.ConditionTrue})
	bad.Spec.Taints = []corev1.Taint{{Key: "dedicated", Value: "gpu", Effect: corev1.TaintEffectNoSchedule}}
	p := pod("prod", "api", "", "", nil, "api:1")
	p.Spec.NodeName = "w1"
	p.Spec.Containers[0].Resources.Requests = corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("4Gi")}
	cs := connectInventory(t, &service.K8sClients{Typed: fake.NewClientset(node("cp", corev1.ConditionTrue, false, "control-plane"), node("w1", corev1.ConditionTrue, false, "worker"), bad, p)})

	var out types.K8sNodeListResult
	mustOK(t, call(t, cs, "k8s_list_nodes", map[string]any{}, &out))
	if out.Total != 3 || !strings.Contains(out.Summary, "w2 (NotReady, MemoryPressure)") || !strings.Contains(out.Summary, "1 cordoned") {
		t.Fatalf("summary = %q", out.Summary)
	}
	w1 := out.Nodes[1]
	if w1.Name != "w1" || *w1.Pods != 1 || w1.CPURequested != "1" || *w1.CPURequestedPct != 25 || w1.MemRequested != "4.0Gi" || w1.Zone != "z1" {
		t.Errorf("w1 = %+v", w1)
	}
	if w2 := out.Nodes[2]; w2.Schedulable || len(w2.Taints) != 1 || w2.Taints[0] != "dedicated=gpu:NoSchedule" {
		t.Errorf("w2 = %+v", w2)
	}

	for args, want := range map[string]string{`{"role":"worker"}`: "w1,w2", `{"status":"not_ready"}`: "w2", `{"status":"cordoned"}`: "w2", `{"name_contains":"cp"}`: "cp"} {
		var a map[string]any
		_ = json.Unmarshal([]byte(args), &a)
		var o types.K8sNodeListResult
		mustOK(t, call(t, cs, "k8s_list_nodes", a, &o))
		var got []string
		for _, n := range o.Nodes {
			got = append(got, n.Name)
		}
		if strings.Join(got, ",") != want {
			t.Errorf("%s: got %v want %s", args, got, want)
		}
	}
	if !call(t, cs, "k8s_list_nodes", map[string]any{"status": "broken"}, &out).IsError {
		t.Error("bad status should be a tool error")
	}
}

func TestK8sListWorkloads(t *testing.T) {
	tmpl := corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "api:1"}}}}
	healthy := &appsv1.Deployment{ObjectMeta: meta("prod", "api"), Spec: appsv1.DeploymentSpec{Replicas: ptr[int32](2), Template: tmpl},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: 2, UpdatedReplicas: 2, AvailableReplicas: 2}}
	stuck := &appsv1.Deployment{ObjectMeta: meta("prod", "web"), Spec: appsv1.DeploymentSpec{Replicas: ptr[int32](3), Template: tmpl},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, ReadyReplicas: 1, UpdatedReplicas: 3, AvailableReplicas: 1,
			Conditions: []appsv1.DeploymentCondition{{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionFalse, Reason: "ProgressDeadlineExceeded", Message: "web exceeded its progress deadline"}}}}
	sts := &appsv1.StatefulSet{ObjectMeta: meta("data", "db"), Spec: appsv1.StatefulSetSpec{Replicas: ptr[int32](3), Template: tmpl},
		Status: appsv1.StatefulSetStatus{ObservedGeneration: 1, ReadyReplicas: 2, UpdatedReplicas: 3, AvailableReplicas: 2}}
	ds := &appsv1.DaemonSet{ObjectMeta: meta("kube-system", "proxy"), Spec: appsv1.DaemonSetSpec{Template: tmpl},
		Status: appsv1.DaemonSetStatus{ObservedGeneration: 1, DesiredNumberScheduled: 3, NumberReady: 3, UpdatedNumberScheduled: 3, NumberAvailable: 3}}
	cron := &batchv1.CronJob{ObjectMeta: meta("ops", "backup"), Spec: batchv1.CronJobSpec{Schedule: "0 2 * * *",
		JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: tmpl}}}}
	ctrl := true
	cronRun := &batchv1.Job{ObjectMeta: meta("ops", "backup-123"), Spec: batchv1.JobSpec{Template: tmpl},
		Status: batchv1.JobStatus{Succeeded: 1, Conditions: []batchv1.JobCondition{{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}}}}
	cronRun.OwnerReferences = []metav1.OwnerReference{{Kind: "CronJob", Name: "backup", Controller: &ctrl}}
	failedJob := &batchv1.Job{ObjectMeta: meta("ops", "migrate"), Spec: batchv1.JobSpec{Template: tmpl},
		Status: batchv1.JobStatus{Failed: 6, Conditions: []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue, Reason: "BackoffLimitExceeded", Message: "Job has reached the specified backoff limit"}}}}

	cs := connectInventory(t, &service.K8sClients{Typed: fake.NewClientset(healthy, stuck, sts, ds, cron, cronRun, failedJob)})
	var out types.K8sWorkloadListResult
	mustOK(t, call(t, cs, "k8s_list_workloads", map[string]any{}, &out))
	status := map[string]string{}
	for _, w := range out.Workloads {
		status[w.Kind+"/"+w.Name] = w.Status
	}
	want := map[string]string{"Deployment/api": "healthy", "Deployment/web": "failed", "StatefulSet/db": "degraded",
		"DaemonSet/proxy": "healthy", "CronJob/backup": "scheduled", "Job/migrate": "failed"}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("%s = %q, want %q (all: %v)", k, status[k], v, status)
		}
	}
	if _, ok := status["Job/backup-123"]; ok || out.Total != 6 {
		t.Errorf("cronjob jobs should be hidden by default: %v", status)
	}
	if out.Workloads[0].Status == "healthy" || !strings.Contains(out.Summary, "ops/Job/migrate") || out.Workloads[0].Message == "" {
		t.Errorf("unhealthy should sort first with a message: %+v / %q", out.Workloads[0], out.Summary)
	}

	mustOK(t, call(t, cs, "k8s_list_workloads", map[string]any{"unhealthy_only": true}, &out))
	if out.Total != 3 {
		t.Errorf("unhealthy_only total = %d: %+v", out.Total, out.Workloads)
	}
	mustOK(t, call(t, cs, "k8s_list_workloads", map[string]any{"kinds": []string{"Job"}, "include_cronjob_jobs": true}, &out))
	if out.Total != 2 || out.Workloads[1].Owner != "backup" {
		t.Errorf("jobs with cronjob jobs: %+v", out.Workloads)
	}
	if !call(t, cs, "k8s_list_workloads", map[string]any{"kinds": []string{"Pod"}}, &out).IsError {
		t.Error("bad kind should be a tool error")
	}
}

func TestK8sListPods(t *testing.T) {
	ok := pod("prod", "ok", "", "", nil, "api:1")
	ok.Spec.NodeName = "w1"
	ok.Status.ContainerStatuses[0].State.Running = &corev1.ContainerStateRunning{}

	crash := pod("prod", "crash", "", "", nil, "api:1")
	crash.Spec.NodeName = "w2"
	crash.Status.ContainerStatuses[0].Ready = false
	crash.Status.ContainerStatuses[0].RestartCount = 7
	crash.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}
	crash.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "OOMKilled", ExitCode: 137, FinishedAt: metav1.Now()}

	pending := pod("dev", "pending", "", "", nil, "web:2")
	pending.Status.Phase = corev1.PodPending
	pending.Status.ContainerStatuses[0].Ready = false
	pending.Status.ContainerStatuses[0].State.Waiting = &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}

	done := pod("ops", "done", "", "", nil, "job:1")
	done.Status.Phase = corev1.PodSucceeded
	done.Status.ContainerStatuses[0].Ready = false
	done.Status.ContainerStatuses[0].State.Terminated = &corev1.ContainerStateTerminated{Reason: "Completed"}

	cs := connectInventory(t, &service.K8sClients{Typed: fake.NewClientset(ok, crash, pending, done)})
	var out types.K8sPodListResult
	mustOK(t, call(t, cs, "k8s_list_pods", map[string]any{}, &out))
	byName := map[string]types.K8sPod{}
	for _, p := range out.Pods {
		byName[p.Name] = p
	}
	if c := byName["crash"]; c.Status != "CrashLoopBackOff" || !c.Problem || c.Restarts != 7 || c.Ready != "0/1" ||
		c.LastTermination == nil || c.LastTermination.Reason != "OOMKilled" || c.LastTermination.ExitCode != 137 {
		t.Errorf("crash = %+v", c)
	}
	if p := byName["pending"]; p.Status != "ImagePullBackOff" || !p.Problem {
		t.Errorf("pending = %+v", p)
	}
	if byName["ok"].Problem || byName["done"].Problem || byName["done"].Status != "Completed" {
		t.Errorf("healthy pods flagged: %+v", byName)
	}
	if !out.Pods[0].Problem || !strings.Contains(out.Summary, "2 need attention") {
		t.Errorf("problems first / summary: %q", out.Summary)
	}

	mustOK(t, call(t, cs, "k8s_list_pods", map[string]any{"problems_only": true}, &out))
	if out.Total != 2 {
		t.Errorf("problems_only total = %d", out.Total)
	}
	mustOK(t, call(t, cs, "k8s_list_pods", map[string]any{"namespace": "prod", "name_contains": "ok"}, &out))
	if out.Total != 1 || out.Pods[0].Name != "ok" {
		t.Errorf("filters: %+v", out.Pods)
	}
	if !call(t, cs, "k8s_list_pods", map[string]any{"field_selector": "bad"}, &out).IsError {
		t.Error("bad field_selector should be a tool error")
	}
}

func TestK8sListServicesAndIngresses(t *testing.T) {
	lb := &corev1.Service{ObjectMeta: meta("prod", "web"), Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeLoadBalancer, ClusterIP: "10.0.0.5",
		Selector: map[string]string{"app": "web"},
		Ports:    []corev1.ServicePort{{Name: "http", Port: 80, NodePort: 30080, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(8080)}}},
		Status: corev1.ServiceStatus{LoadBalancer: corev1.LoadBalancerStatus{Ingress: []corev1.LoadBalancerIngress{{IP: "203.0.113.7"}}}}}
	orphan := &corev1.Service{ObjectMeta: meta("prod", "orphan"), Spec: corev1.ServiceSpec{ClusterIP: "10.0.0.6", Selector: map[string]string{"app": "gone"},
		Ports: []corev1.ServicePort{{Port: 5432, Protocol: corev1.ProtocolTCP, TargetPort: intstr.FromInt32(5432)}}}}
	slice := &discoveryv1.EndpointSlice{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "web-abc", Labels: map[string]string{discoveryv1.LabelServiceName: "web"}},
		Endpoints: []discoveryv1.Endpoint{{Addresses: []string{"10.1.0.1"}}, {Addresses: []string{"10.1.0.2"}, Conditions: discoveryv1.EndpointConditions{Ready: ptr(false)}}}}
	ing := &networkingv1.Ingress{ObjectMeta: meta("prod", "web"), Spec: networkingv1.IngressSpec{
		IngressClassName: ptr("nginx"),
		TLS:              []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}, SecretName: "shop-tls"}},
		Rules: []networkingv1.IngressRule{
			{Host: "shop.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
				{Path: "/", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "web", Port: networkingv1.ServiceBackendPort{Number: 80}}}}}}}},
			{Host: "admin.example.com", IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
				{Path: "/admin", Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "admin", Port: networkingv1.ServiceBackendPort{Name: "http"}}}}}}}},
		}}}

	cs := connectInventory(t, &service.K8sClients{Typed: fake.NewClientset(lb, orphan, slice, ing)})
	var svcs types.K8sServiceListResult
	mustOK(t, call(t, cs, "k8s_list_services", map[string]any{}, &svcs))
	if len(svcs.Services) != 2 {
		t.Fatalf("services = %+v", svcs)
	}
	o, w := svcs.Services[0], svcs.Services[1]
	if w.Name != "web" || !w.Exposed || w.External[0] != "203.0.113.7" || w.Ports[0] != "http 80:30080/TCP->8080" || *w.ReadyEndpoints != 1 {
		t.Errorf("web = %+v", w)
	}
	if o.Exposed || *o.ReadyEndpoints != 0 || o.Ports[0] != "5432/TCP" || !strings.Contains(svcs.Summary, "no ready endpoints: prod/orphan") {
		t.Errorf("orphan = %+v / %q", o, svcs.Summary)
	}
	mustOK(t, call(t, cs, "k8s_list_services", map[string]any{"exposed_only": true}, &svcs))
	if svcs.Total != 1 {
		t.Errorf("exposed_only total = %d", svcs.Total)
	}

	var ings types.K8sIngressListResult
	mustOK(t, call(t, cs, "k8s_list_ingresses", map[string]any{}, &ings))
	i := ings.Ingresses[0]
	if i.Class != "nginx" || len(i.Rules) != 2 || i.Rules[1] != "admin.example.com/admin -> admin:http" ||
		len(i.NoTLSHosts) != 1 || i.NoTLSHosts[0] != "admin.example.com" || i.TLS[0] != "shop.example.com (shop-tls)" {
		t.Errorf("ingress = %+v", i)
	}
	mustOK(t, call(t, cs, "k8s_list_ingresses", map[string]any{"host_contains": "nomatch"}, &ings))
	if ings.Total != 0 {
		t.Errorf("host_contains: %+v", ings)
	}
}

// dynamicCluster returns typed (for discovery) and dynamic clients that know
// Deployments, Secrets, cert-manager Certificates and CRDs.
func dynamicCluster(t *testing.T, objs ...runtime.Object) *service.K8sClients {
	t.Helper()
	typedCS := fake.NewClientset()
	typedCS.Resources = []*metav1.APIResourceList{
		{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "secrets", SingularName: "secret", Kind: "Secret", Namespaced: true, Verbs: []string{"get", "list"}}}},
		{GroupVersion: "apps/v1", APIResources: []metav1.APIResource{{Name: "deployments", SingularName: "deployment", Kind: "Deployment", Namespaced: true, ShortNames: []string{"deploy"}, Verbs: []string{"get", "list"}}}},
		{GroupVersion: "cert-manager.io/v1", APIResources: []metav1.APIResource{{Name: "certificates", SingularName: "certificate", Kind: "Certificate", Namespaced: true, ShortNames: []string{"cert"}, Verbs: []string{"get", "list"}}}},
		{GroupVersion: "apiextensions.k8s.io/v1", APIResources: []metav1.APIResource{{Name: "customresourcedefinitions", SingularName: "customresourcedefinition", Kind: "CustomResourceDefinition", ShortNames: []string{"crd"}, Verbs: []string{"get", "list"}}}},
	}
	gvrs := map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "secrets"}:                                                  "SecretList",
		{Group: "apps", Version: "v1", Resource: "deployments"}:                               "DeploymentList",
		{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}:                   "CertificateList",
		{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}: "CustomResourceDefinitionList",
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, objs...)
	return &service.K8sClients{Typed: typedCS, Dynamic: dyn}
}

func obj(apiVersion, kind, ns, name string, fields map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name}}}
	if ns != "" {
		u.SetNamespace(ns)
	}
	for k, v := range fields {
		u.Object[k] = v
	}
	return u
}

func TestK8sGetResource(t *testing.T) {
	secret := obj("v1", "Secret", "prod", "db", map[string]any{"data": map[string]any{"password": "aHVudGVyMg=="}})
	secret.SetAnnotations(map[string]string{"kubectl.kubernetes.io/last-applied-configuration": `{"data":{"password":"aHVudGVyMg=="}}`})
	secret.Object["metadata"].(map[string]any)["managedFields"] = []any{map[string]any{"manager": "kubectl"}}
	deploy := obj("apps/v1", "Deployment", "prod", "api", map[string]any{"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
		"containers": []any{map[string]any{"name": "api", "image": "api:1", "env": []any{
			map[string]any{"name": "DB_PASSWORD", "value": "hunter2"},
			map[string]any{"name": "TOKEN", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "db", "key": "token"}}},
		}}}}}}})
	cert := obj("cert-manager.io/v1", "Certificate", "prod", "shop", map[string]any{"spec": map[string]any{"dnsNames": []any{"shop.example.com"}}})
	cs := connectInventory(t, dynamicCluster(t, secret, deploy, cert))

	var out types.K8sResourceGetResult
	mustOK(t, call(t, cs, "k8s_get_resource", map[string]any{"kind": "secret", "name": "db", "namespace": "prod"}, &out))
	for _, leak := range []string{"aHVudGVyMg==", "hunter2", "managedFields"} {
		if strings.Contains(out.YAML, leak) {
			t.Errorf("secret YAML leaks %q:\n%s", leak, out.YAML)
		}
	}
	if !strings.Contains(out.YAML, "password: <redacted>") || len(out.Redacted) != 2 {
		t.Errorf("secret redaction: %v\n%s", out.Redacted, out.YAML)
	}

	mustOK(t, call(t, cs, "k8s_get_resource", map[string]any{"kind": "deploy", "name": "api", "namespace": "prod"}, &out))
	if out.Kind != "Deployment" || out.APIVersion != "apps/v1" || strings.Contains(out.YAML, "hunter2") || !strings.Contains(out.YAML, "secretKeyRef") {
		t.Errorf("deployment get: %+v\n%s", out, out.YAML)
	}

	mustOK(t, call(t, cs, "k8s_get_resource", map[string]any{"kind": "certificates.cert-manager.io"}, &out))
	if out.Total != 1 || out.Items[0].Name != "shop" || out.Resource != "certificates.cert-manager.io" {
		t.Errorf("crd list: %+v", out)
	}

	for _, args := range []map[string]any{
		{"kind": "widgets"},                   // unknown kind
		{"kind": "deployment", "name": "api"}, // namespaced, no namespace
		{"kind": "deployment", "name": "a/b", "namespace": "prod"},
		{},
	} {
		if !call(t, cs, "k8s_get_resource", args, &out).IsError {
			t.Errorf("%v: expected tool error", args)
		}
	}
}

func TestK8sListCRDs(t *testing.T) {
	crd := obj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", "certificates.cert-manager.io", map[string]any{
		"spec": map[string]any{"group": "cert-manager.io", "scope": "Namespaced",
			"names":    map[string]any{"kind": "Certificate", "shortNames": []any{"cert"}},
			"versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}, map[string]any{"name": "v1alpha2", "served": false, "storage": false}}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Established", "status": "True"}}},
	})
	pending := obj("apiextensions.k8s.io/v1", "CustomResourceDefinition", "", "widgets.acme.io", map[string]any{
		"spec": map[string]any{"group": "acme.io", "scope": "Cluster", "names": map[string]any{"kind": "Widget"},
			"versions": []any{map[string]any{"name": "v1", "served": true, "storage": true}}},
	})
	cs := connectInventory(t, dynamicCluster(t, crd, pending))
	var out types.K8sCRDListResult
	mustOK(t, call(t, cs, "k8s_list_crds", map[string]any{}, &out))
	if out.Total != 2 || len(out.Groups) != 2 || !strings.Contains(out.Summary, "not established: widgets.acme.io") {
		t.Fatalf("crds = %+v", out)
	}
	c := out.CRDs[0]
	if c.Kind != "Certificate" || !c.Established || c.Versions[0] != "v1 (storage)" || c.Versions[1] != "v1alpha2 (not served)" || c.ShortNames[0] != "cert" {
		t.Errorf("crd = %+v", c)
	}
	mustOK(t, call(t, cs, "k8s_list_crds", map[string]any{"group_contains": "acme"}, &out))
	if out.Total != 1 {
		t.Errorf("group_contains: %+v", out)
	}
}

func TestK8sListHelmReleases(t *testing.T) {
	scheme := metadatafake.NewTestScheme()
	if err := metav1.AddMetaToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	rel := func(ns, name, rev, status string) runtime.Object {
		return &metav1.PartialObjectMetadata{
			TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "sh.helm.release.v1." + name + ".v" + rev,
				Labels: map[string]string{"owner": "helm", "name": name, "version": rev, "status": status, "modifiedAt": "1760000000"}},
		}
	}
	other := &metav1.PartialObjectMetadata{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "db"}}
	md := metadatafake.NewSimpleMetadataClient(scheme,
		rel("prod", "api", "1", "superseded"), rel("prod", "api", "2", "deployed"),
		rel("prod", "web", "4", "failed"), rel("ops", "monitoring", "1", "pending-upgrade"), other)
	cs := connectInventory(t, &service.K8sClients{Typed: fake.NewClientset(), Metadata: md})

	var out types.K8sHelmListResult
	mustOK(t, call(t, cs, "k8s_list_helm_releases", map[string]any{}, &out))
	if out.Total != 3 {
		t.Fatalf("releases = %+v", out)
	}
	api := out.Releases[1]
	if api.Name != "api" || api.Revision != 2 || api.Status != "deployed" || api.Revisions != 2 || api.Problem || api.Updated != "2025-10-09T08:53:20Z" {
		t.Errorf("api = %+v", api)
	}
	if !strings.Contains(out.Summary, "ops/monitoring (pending-upgrade)") || !strings.Contains(out.Summary, "prod/web (failed)") {
		t.Errorf("summary = %q", out.Summary)
	}
	mustOK(t, call(t, cs, "k8s_list_helm_releases", map[string]any{"problems_only": true, "namespace": "prod"}, &out))
	if out.Total != 1 || out.Releases[0].Name != "web" {
		t.Errorf("problems_only: %+v", out.Releases)
	}
}
