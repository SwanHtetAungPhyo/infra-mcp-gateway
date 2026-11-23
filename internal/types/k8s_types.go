package types

// K8sImageListInput is the argument schema exposed to MCP clients for k8s_list_images.
// Every field is optional: an empty call lists every image in every namespace.
type K8sImageListInput struct {
	Namespace     string `json:"namespace,omitempty" jsonschema:"only look at pods in this namespace; empty means all namespaces"`
	LabelSelector string `json:"label_selector,omitempty" jsonschema:"Kubernetes label selector, e.g. app=nginx,tier!=cache or environment in (prod,staging)"`
	FieldSelector string `json:"field_selector,omitempty" jsonschema:"Kubernetes pod field selector, e.g. status.phase=Running or spec.nodeName=node-1"`
	ImageContains string `json:"image_contains,omitempty" jsonschema:"only return images whose reference contains this text, e.g. nginx or ghcr.io/acme"`
	ContainerKind string `json:"container_kind,omitempty" jsonschema:"only count this kind of container: container, init or ephemeral; default all"`
	GroupBy       string `json:"group_by,omitempty" jsonschema:"image (default) returns each unique image once with where it runs; pod returns each pod with its images"`
	MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum images (or pods for group_by=pod) to return, 1-1000; default 200"`
}

// K8sImageListResult is the compact, LLM-friendly result of k8s_list_images.
type K8sImageListResult struct {
	Context     string        `json:"context,omitempty"` // kubeconfig context, when known
	Namespace   string        `json:"namespace"`         // "" = all namespaces
	GroupBy     string        `json:"group_by"`
	Summary     string        `json:"summary"`
	PodsScanned int           `json:"pods_scanned"`
	TotalImages int           `json:"total_images"` // unique images after filters
	Truncated   bool          `json:"truncated,omitempty"`
	Images      []K8sImage    `json:"images,omitempty"` // group_by=image
	Pods        []K8sPodImage `json:"pods,omitempty"`   // group_by=pod
}

// K8sImage is one unique image reference and where it runs.
type K8sImage struct {
	Image      string   `json:"image"` // as written in the pod spec
	Registry   string   `json:"registry"`
	Repository string   `json:"repository"`
	Tag        string   `json:"tag,omitempty"`
	Digest     string   `json:"digest,omitempty"`          // pinned in the spec
	Running    []string `json:"running_digests,omitempty"` // resolved by the kubelet
	Pods       int      `json:"pods"`
	Containers int      `json:"containers"`
	Kinds      []string `json:"kinds"` // container | init | ephemeral
	Namespaces []string `json:"namespaces"`
	Workloads  []string `json:"workloads"` // namespace/Kind/name, capped
}

// K8sPodImage is one pod and the images it uses (group_by=pod).
type K8sPodImage struct {
	Namespace  string            `json:"namespace"`
	Pod        string            `json:"pod"`
	Workload   string            `json:"workload,omitempty"`
	Node       string            `json:"node,omitempty"`
	Phase      string            `json:"phase"`
	Containers []K8sContainerImg `json:"containers"`
}

type K8sContainerImg struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Image    string `json:"image"`
	ImageID  string `json:"image_id,omitempty"`
	Ready    bool   `json:"ready"`
	Restarts int32  `json:"restarts,omitempty"`
}
