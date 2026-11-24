package tools

import (
	"context"
	"fmt"
	"sync"
	"time"

	"ai.engineer.runner/internal/service"
	"ai.engineer.runner/internal/types"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes"
)

// K8sTimeout caps one k8s tool call, including paging.
const K8sTimeout = 2 * time.Minute

var (
	K8sListImagesTool = &mcp.Tool{
		Name:  "k8s_list_images",
		Title: "List Kubernetes container images",
		Description: "List the container images used by pods in the Kubernetes cluster (read-only). " +
			"With no arguments it returns every unique image across all namespaces, most widely used first, with registry, " +
			"repository, tag, pinned digest, the digests actually running, and which namespaces and workloads (namespace/Kind/name) use it. " +
			"Narrow with namespace, label_selector (app=api), field_selector (status.phase=Running), image_contains (nginx, ghcr.io/acme) " +
			"or container_kind (container, init, ephemeral). Set group_by=pod to see each pod with its containers, readiness and restarts. " +
			"Good first step before scanning images with trivy_scan (scan_type=image, target=<image>).",
		InputSchema: k8sInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: boolPtr(true),
		},
	}

	K8sListContextsTool = &mcp.Tool{
		Name:  "k8s_list_contexts",
		Title: "List Kubernetes contexts",
		Description: "List the Kubernetes contexts (clusters) this gateway may use (read-only): name, cluster, API server, user, " +
			"kubeconfig default namespace and how it authenticates (never credentials). The default context is marked; " +
			"pass a name as context to k8s_list_namespaces or k8s_list_images to target another cluster. " +
			"Set check_connectivity to ask each API server for its version and see which clusters are reachable. " +
			"Only contexts allowed by the gateway's K8S_ALLOWED_CONTEXTS are listed.",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: boolPtr(true),
		},
	}

	K8sListNameSpaceTool = &mcp.Tool{
		Name:  "k8s_list_namespaces",
		Title: "List Kubernetes namespaces",
		Description: "List namespaces in the Kubernetes cluster (read-only) with phase (Active/Terminating), age, labels, " +
			"and per-namespace pod health: total, running, pending, succeeded, failed, not ready and crash-looping. " +
			"The summary names namespaces that have failing, crash-looping or not-ready pods. " +
			"Narrow with label_selector (team=payments), name_contains (prod), phase, or exclude_system to hide kube-system/kube-public/kube-node-lease/default. " +
			"Set skip_pod_counts on very large clusters. Good first step before k8s_list_images with namespace=<name>.",
		InputSchema: k8sNamespaceInputSchema(),
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: boolPtr(true),
		},
	}
	K8sNodeListTool = &mcp.Tool{
		Name:        "k8s_list_nodes",
		Title:       "List Kubernetes Nodes",
		Description: "",
		Annotations: &mcp.ToolAnnotations{
			ReadOnlyHint:  true,
			OpenWorldHint: boolPtr(true),
		},
	}
)

func k8sInputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[types.K8sImageListInput](nil)
	if err != nil {
		panic(fmt.Sprintf("k8s input schema: %v", err))
	}
	s.Properties["group_by"].Enum = toAny(service.K8sGroupBy)
	s.Properties["container_kind"].Enum = toAny(service.K8sContainerKinds)
	return s
}

func k8sNamespaceInputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[types.K8sNameSpaceListInput](nil)
	if err != nil {
		panic(fmt.Sprintf("k8s namespace input schema: %v", err))
	}
	s.Properties["phase"].Enum = toAny(service.K8sNamespacePhases)
	return s
}

// K8sToolsSuite holds the Kubernetes clients, one per context. Config is
// loaded on the first call, so the server still starts on machines without
// a cluster and the agent gets a clear error instead.
type K8sToolsSuite struct {
	load func() (*service.K8sSource, error)

	once sync.Once
	src  *service.K8sSource
	err  error
}

// NewK8sToolsSuite uses in-cluster config or kubeconfig (see service.LoadK8sSource).
func NewK8sToolsSuite() *K8sToolsSuite {
	return &K8sToolsSuite{load: service.LoadK8sSource}
}

// NewK8sToolsSuiteWithClient serves one ready client as the default context (tests).
func NewK8sToolsSuiteWithClient(c kubernetes.Interface) *K8sToolsSuite {
	return NewK8sToolsSuiteWithSource(service.NewStaticK8sSource("default", map[string]kubernetes.Interface{"default": c}))
}

// NewK8sToolsSuiteWithSource uses a prepared source (tests, custom setups).
func NewK8sToolsSuiteWithSource(src *service.K8sSource) *K8sToolsSuite {
	return &K8sToolsSuite{load: func() (*service.K8sSource, error) { return src, nil }}
}

func (ks *K8sToolsSuite) Source() (*service.K8sSource, error) {
	ks.once.Do(func() { ks.src, ks.err = ks.load() })
	return ks.src, ks.err
}

func (ks *K8sToolsSuite) clientset(name string) (kubernetes.Interface, string, error) {
	src, err := ks.Source()
	if err != nil {
		return nil, "", err
	}
	return src.Client(name)
}

// RunListK8sContexts is the MCP handler for k8s_list_contexts.
func (ks *K8sToolsSuite) RunListK8sContexts(_ context.Context, _ *mcp.CallToolRequest, in types.K8sContextListInput) (*mcp.CallToolResult, *types.K8sContextListResult, error) {
	src, err := ks.Source()
	if err != nil {
		return nil, nil, fmt.Errorf("k8s_list_contexts: %w", err)
	}
	res, err := service.ListK8sContexts(src, in)
	if err != nil {
		return nil, nil, fmt.Errorf("k8s_list_contexts: %w", err)
	}
	return nil, res, nil
}

// RunListK8sNameSpace is the MCP handler for k8s_list_namespaces.
func (ks *K8sToolsSuite) RunListK8sNameSpace(ctx context.Context, _ *mcp.CallToolRequest, in types.K8sNameSpaceListInput) (*mcp.CallToolResult, *types.K8sNameSpaceListResult, error) {
	cs, ctxName, err := ks.clientset(in.Context)
	if err != nil {
		return nil, nil, fmt.Errorf("k8s_list_namespaces: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, K8sTimeout)
	defer cancel()

	res, err := service.ListK8sNameSpace(ctx, cs, in)
	if err != nil {
		return nil, nil, fmt.Errorf("k8s_list_namespaces: %w", err)
	}
	res.Context = ctxName
	return nil, res, nil
}

// RunListK8sImages is the MCP handler for k8s_list_images.
func (ks *K8sToolsSuite) RunListK8sImages(ctx context.Context, _ *mcp.CallToolRequest, in types.K8sImageListInput) (*mcp.CallToolResult, *types.K8sImageListResult, error) {
	cs, ctxName, err := ks.clientset(in.Context)
	if err != nil {
		return nil, nil, fmt.Errorf("k8s_list_images: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, K8sTimeout)
	defer cancel()

	res, err := service.ListK8sImages(ctx, cs, in)
	if err != nil {
		return nil, nil, fmt.Errorf("k8s_list_images: %w", err)
	}
	res.Context = ctxName
	return nil, res, nil
}
