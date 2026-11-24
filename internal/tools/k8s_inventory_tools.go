package tools

import (
	"context"
	"fmt"

	"ai.engineer.runner/internal/service"
	"ai.engineer.runner/internal/types"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes"
)

// Read-only k8s inventory tools: nodes, workloads, pods, services, ingresses,
// generic resources, CRDs and Helm releases. All take an optional context.

func k8sReadOnly() *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: boolPtr(true)}
}

// k8sSchema infers the input schema and adds enums. For array properties the
// enum goes on the items.
func k8sSchema[T any](enums map[string][]string) *jsonschema.Schema {
	s, err := jsonschema.For[T](nil)
	if err != nil {
		panic(fmt.Sprintf("k8s input schema: %v", err))
	}
	for prop, values := range enums {
		p := s.Properties[prop]
		if p == nil {
			panic("k8s input schema: no property " + prop)
		}
		if p.Items != nil {
			p = p.Items
		}
		p.Enum = toAny(values)
	}
	return s
}

// k8sHandler resolves the context, applies the timeout, runs fn and stamps
// the context name on the result.
func k8sHandler[In, Out any](ks *K8sToolsSuite, tool string, ctxOf func(*In) string, setCtx func(*Out, string),
	fn func(context.Context, *service.K8sClients, In) (*Out, error)) mcp.ToolHandlerFor[In, *Out] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in In) (*mcp.CallToolResult, *Out, error) {
		src, err := ks.Source()
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", tool, err)
		}
		c, name, err := src.Clients(ctxOf(&in))
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", tool, err)
		}
		ctx, cancel := context.WithTimeout(ctx, K8sTimeout)
		defer cancel()
		out, err := fn(ctx, c, in)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", tool, err)
		}
		setCtx(out, name)
		return nil, out, nil
	}
}

// typed adapts a service function that only needs the typed client.
func typed[In, Out any](fn func(context.Context, kubernetes.Interface, In) (*Out, error)) func(context.Context, *service.K8sClients, In) (*Out, error) {
	return func(ctx context.Context, c *service.K8sClients, in In) (*Out, error) { return fn(ctx, c.Typed, in) }
}

var (
	K8sListNodesTool = &mcp.Tool{
		Name:  "k8s_list_nodes",
		Title: "List Kubernetes nodes",
		Description: "List cluster nodes (read-only): Ready/NotReady, cordoned, roles, pressure conditions, taints, kubelet version, " +
			"OS/arch/kernel/runtime, IPs, zone and instance type, CPU/memory capacity vs allocatable, and the CPU/memory requested " +
			"by pods on each node with percentages. The summary names unhealthy nodes and version skew. " +
			"Filter with label_selector, role (control-plane, worker), status (ready, not_ready, cordoned) or name_contains.",
		InputSchema: k8sSchema[types.K8sNodeListInput](map[string][]string{"status": service.K8sNodeStatuses}),
		Annotations: k8sReadOnly(),
	}

	K8sListWorkloadsTool = &mcp.Tool{
		Name:  "k8s_list_workloads",
		Title: "List Kubernetes workloads",
		Description: "List Deployments, StatefulSets, DaemonSets, CronJobs and Jobs (read-only) with a health status " +
			"(healthy, progressing, degraded, failed, scaled_to_zero, running, complete, suspended, scheduled), desired/ready/updated/available " +
			"replicas, images, and why it isn't healthy. CronJobs show schedule and last run times. Unhealthy workloads come first and are named in the summary. " +
			"Filter with namespace, kinds, label_selector, name_contains or unhealthy_only. Jobs created by CronJobs are hidden unless include_cronjob_jobs.",
		InputSchema: k8sSchema[types.K8sWorkloadListInput](map[string][]string{"kinds": service.K8sWorkloadKinds}),
		Annotations: k8sReadOnly(),
	}

	K8sListPodsTool = &mcp.Tool{
		Name:  "k8s_list_pods",
		Title: "List Kubernetes pods",
		Description: "List pods (read-only) with the kubectl STATUS (Running, Pending, CrashLoopBackOff, ImagePullBackOff, OOMKilled, Init:..., Evicted), " +
			"ready containers, restarts, the last container termination (reason, exit code, time), node, owning workload, IP and QoS class. " +
			"Problem pods come first and are named in the summary. Filter with namespace, label_selector, field_selector, node, name_contains or problems_only.",
		InputSchema: k8sSchema[types.K8sPodListInput](nil),
		Annotations: k8sReadOnly(),
	}

	K8sListServicesTool = &mcp.Tool{
		Name:  "k8s_list_services",
		Title: "List Kubernetes services",
		Description: "List services (read-only) with type, cluster IP, external addresses (load balancer IPs/hostnames, externalIPs, externalName), " +
			"ports (port:nodePort/protocol->targetPort), selector and ready endpoint count. The summary names services exposed outside the cluster " +
			"and services with no ready endpoints. Filter with namespace, label_selector, name_contains, type or exposed_only.",
		InputSchema: k8sSchema[types.K8sServiceListInput](map[string][]string{"type": service.K8sServiceTypes}),
		Annotations: k8sReadOnly(),
	}

	K8sListIngressesTool = &mcp.Tool{
		Name:  "k8s_list_ingresses",
		Title: "List Kubernetes ingresses",
		Description: "List ingresses (read-only) with ingress class, hosts, routing rules (host/path -> service:port), TLS hosts and secret names, " +
			"hosts served without TLS, and load balancer addresses. Filter with namespace, label_selector or host_contains.",
		InputSchema: k8sSchema[types.K8sIngressListInput](nil),
		Annotations: k8sReadOnly(),
	}

	K8sGetResourceTool = &mcp.Tool{
		Name:  "k8s_get_resource",
		Title: "Get any Kubernetes resource",
		Description: "Read any Kubernetes object, including custom resources, as YAML (read-only). kind accepts a kind, plural or short name, " +
			"optionally with group: Deployment, svc, certificates.cert-manager.io. With name (and namespace for namespaced kinds) it returns the object " +
			"without managedFields; Secret values, literal env values and the last-applied-configuration annotation are replaced by <redacted>. " +
			"Without name it lists objects of that kind (name, namespace, age). Use k8s_list_crds to find custom kinds.",
		InputSchema: k8sSchema[types.K8sResourceGetInput](nil),
		Annotations: k8sReadOnly(),
	}

	K8sListCRDsTool = &mcp.Tool{
		Name:  "k8s_list_crds",
		Title: "List Kubernetes CRDs",
		Description: "List CustomResourceDefinitions installed in the cluster (read-only): name, API group, kind, scope, versions " +
			"(storage, not served, deprecated), short names and whether each is established. Filter with group_contains. " +
			"Read the custom objects with k8s_get_resource kind=<plural.group>.",
		InputSchema: k8sSchema[types.K8sCRDListInput](nil),
		Annotations: k8sReadOnly(),
	}

	K8sListHelmReleasesTool = &mcp.Tool{
		Name:  "k8s_list_helm_releases",
		Title: "List Helm releases",
		Description: "List Helm 3 releases (read-only): namespace, name, latest revision, status (deployed, failed, pending-upgrade...), " +
			"revisions kept and last update. Read from release Secret labels only; chart contents and values are never fetched. " +
			"Failed or stuck releases are named in the summary. Filter with namespace, name_contains or problems_only.",
		InputSchema: k8sSchema[types.K8sHelmListInput](nil),
		Annotations: k8sReadOnly(),
	}
)

// addInventoryTool registers one inventory tool by name.
func (ks *K8sToolsSuite) addInventoryTool(server *mcp.Server, name string) {
	switch name {
	case K8sListNodesTool.Name:
		mcp.AddTool(server, K8sListNodesTool, k8sHandler(ks, name,
			func(in *types.K8sNodeListInput) string { return in.Context },
			func(out *types.K8sNodeListResult, c string) { out.Context = c },
			typed(service.ListK8sNodes)))
	case K8sListWorkloadsTool.Name:
		mcp.AddTool(server, K8sListWorkloadsTool, k8sHandler(ks, name,
			func(in *types.K8sWorkloadListInput) string { return in.Context },
			func(out *types.K8sWorkloadListResult, c string) { out.Context = c },
			typed(service.ListK8sWorkloads)))
	case K8sListPodsTool.Name:
		mcp.AddTool(server, K8sListPodsTool, k8sHandler(ks, name,
			func(in *types.K8sPodListInput) string { return in.Context },
			func(out *types.K8sPodListResult, c string) { out.Context = c },
			typed(service.ListK8sPods)))
	case K8sListServicesTool.Name:
		mcp.AddTool(server, K8sListServicesTool, k8sHandler(ks, name,
			func(in *types.K8sServiceListInput) string { return in.Context },
			func(out *types.K8sServiceListResult, c string) { out.Context = c },
			typed(service.ListK8sServices)))
	case K8sListIngressesTool.Name:
		mcp.AddTool(server, K8sListIngressesTool, k8sHandler(ks, name,
			func(in *types.K8sIngressListInput) string { return in.Context },
			func(out *types.K8sIngressListResult, c string) { out.Context = c },
			typed(service.ListK8sIngresses)))
	case K8sGetResourceTool.Name:
		mcp.AddTool(server, K8sGetResourceTool, k8sHandler(ks, name,
			func(in *types.K8sResourceGetInput) string { return in.Context },
			func(out *types.K8sResourceGetResult, c string) { out.Context = c },
			service.GetK8sResource))
	case K8sListCRDsTool.Name:
		mcp.AddTool(server, K8sListCRDsTool, k8sHandler(ks, name,
			func(in *types.K8sCRDListInput) string { return in.Context },
			func(out *types.K8sCRDListResult, c string) { out.Context = c },
			service.ListK8sCRDs))
	case K8sListHelmReleasesTool.Name:
		mcp.AddTool(server, K8sListHelmReleasesTool, k8sHandler(ks, name,
			func(in *types.K8sHelmListInput) string { return in.Context },
			func(out *types.K8sHelmListResult, c string) { out.Context = c },
			service.ListK8sHelmReleases))
	default:
		panic("unknown k8s inventory tool " + name)
	}
}

// k8sInventoryTools is the registration order for the registry.
var k8sInventoryTools = []*mcp.Tool{
	K8sListNodesTool, K8sListWorkloadsTool, K8sListPodsTool, K8sListServicesTool,
	K8sListIngressesTool, K8sGetResourceTool, K8sListCRDsTool, K8sListHelmReleasesTool,
}
