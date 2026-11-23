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

// K8sTimeout caps one k8s_list_images call, including paging.
const K8sTimeout = 2 * time.Minute

var K8sListImagesTool = &mcp.Tool{
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

func k8sInputSchema() *jsonschema.Schema {
	s, err := jsonschema.For[types.K8sImageListInput](nil)
	if err != nil {
		panic(fmt.Sprintf("k8s input schema: %v", err))
	}
	s.Properties["group_by"].Enum = toAny(service.K8sGroupBy)
	s.Properties["container_kind"].Enum = toAny(service.K8sContainerKinds)
	return s
}

// K8sToolsSuite holds the Kubernetes client. The client is created on the
// first call, so the server still starts on machines without a cluster and
// the agent gets a clear error instead.
type K8sToolsSuite struct {
	newClient func() (kubernetes.Interface, string, error)

	once    sync.Once
	client  kubernetes.Interface
	ctxName string
	err     error
}

// NewK8sToolsSuite uses in-cluster config or kubeconfig (see service.LoadKubernetesConfig).
func NewK8sToolsSuite() *K8sToolsSuite {
	return &K8sToolsSuite{newClient: service.LoadKubernetesConfig}
}

// NewK8sToolsSuiteWithClient uses a ready client (tests, custom setups).
func NewK8sToolsSuiteWithClient(c kubernetes.Interface) *K8sToolsSuite {
	return &K8sToolsSuite{newClient: func() (kubernetes.Interface, string, error) { return c, "", nil }}
}

func (ks *K8sToolsSuite) clientset() (kubernetes.Interface, string, error) {
	ks.once.Do(func() { ks.client, ks.ctxName, ks.err = ks.newClient() })
	return ks.client, ks.ctxName, ks.err
}

// RunListK8sImages is the MCP handler for k8s_list_images.
func (ks *K8sToolsSuite) RunListK8sImages(ctx context.Context, _ *mcp.CallToolRequest, in types.K8sImageListInput) (*mcp.CallToolResult, *types.K8sImageListResult, error) {
	cs, ctxName, err := ks.clientset()
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
