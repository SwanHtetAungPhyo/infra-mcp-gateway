package types

// K8sImageListInput is the argument schema exposed to MCP clients for k8s_list_images.
// Every field is optional: an empty call lists every image in every namespace.
type (
	K8sImageListInput struct {
		Context       string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		Namespace     string `json:"namespace,omitempty" jsonschema:"only look at pods in this namespace; empty means all namespaces"`
		LabelSelector string `json:"label_selector,omitempty" jsonschema:"Kubernetes label selector, e.g. app=nginx,tier!=cache or environment in (prod,staging)"`
		FieldSelector string `json:"field_selector,omitempty" jsonschema:"Kubernetes pod field selector, e.g. status.phase=Running or spec.nodeName=node-1"`
		ImageContains string `json:"image_contains,omitempty" jsonschema:"only return images whose reference contains this text, e.g. nginx or ghcr.io/acme"`
		ContainerKind string `json:"container_kind,omitempty" jsonschema:"only count this kind of container: container, init or ephemeral; default all"`
		GroupBy       string `json:"group_by,omitempty" jsonschema:"image (default) returns each unique image once with where it runs; pod returns each pod with its images"`
		MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum images (or pods for group_by=pod) to return, 1-1000; default 200"`
	}

	// K8sNameSpaceListInput is the argument schema for k8s_list_namespaces.
	// Every field is optional: an empty call lists every namespace with pod counts.
	K8sNameSpaceListInput struct {
		Context       string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		LabelSelector string `json:"label_selector,omitempty" jsonschema:"Kubernetes label selector on namespaces, e.g. team=payments or environment in (prod,staging)"`
		NameContains  string `json:"name_contains,omitempty" jsonschema:"only namespaces whose name contains this text, e.g. prod"`
		Phase         string `json:"phase,omitempty" jsonschema:"only namespaces in this phase: Active or Terminating; default both"`
		ExcludeSystem bool   `json:"exclude_system,omitempty" jsonschema:"hide kube-system, kube-public, kube-node-lease and default"`
		SkipPodCounts bool   `json:"skip_pod_counts,omitempty" jsonschema:"skip listing pods; faster on large clusters but pod counts stay empty"`
		MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum namespaces to return, 1-1000; default 200"`
	}

	// K8sNameSpaceListResult is the compact result of k8s_list_namespaces.
	K8sNameSpaceListResult struct {
		Context     string         `json:"context,omitempty"`
		Summary     string         `json:"summary"`
		Total       int            `json:"total"` // namespaces after filters
		PodsScanned int            `json:"pods_scanned,omitempty"`
		Truncated   bool           `json:"truncated,omitempty"`
		Namespaces  []K8sNamespace `json:"namespaces"`
	}

	// K8sNamespace is one namespace. Pod counts are nil when skip_pod_counts is set.
	K8sNamespace struct {
		Name      string            `json:"name"`
		Phase     string            `json:"phase"`
		Age       string            `json:"age"` // e.g. 41d, 3h
		CreatedAt string            `json:"created_at"`
		System    bool              `json:"system,omitempty"`
		Labels    map[string]string `json:"labels,omitempty"`
		Pods      *K8sPodCounts     `json:"pods,omitempty"`
	}

	// K8sPodCounts breaks a namespace's pods down by health.
	K8sPodCounts struct {
		Total        int `json:"total"`
		Running      int `json:"running"`
		Pending      int `json:"pending,omitempty"`
		Succeeded    int `json:"succeeded,omitempty"`
		Failed       int `json:"failed,omitempty"`
		NotReady     int `json:"not_ready,omitempty"`     // Running but a container isn't ready
		CrashLooping int `json:"crash_looping,omitempty"` // a container in CrashLoopBackOff
	}

	// K8sImageListResult is the compact, LLM-friendly result of k8s_list_images.
	K8sImageListResult struct {
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
	K8sImage struct {
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
	K8sPodImage struct {
		Namespace  string            `json:"namespace"`
		Pod        string            `json:"pod"`
		Workload   string            `json:"workload,omitempty"`
		Node       string            `json:"node,omitempty"`
		Phase      string            `json:"phase"`
		Containers []K8sContainerImg `json:"containers"`
	}

	K8sContainerImg struct {
		Name     string `json:"name"`
		Kind     string `json:"kind"`
		Image    string `json:"image"`
		ImageID  string `json:"image_id,omitempty"`
		Ready    bool   `json:"ready"`
		Restarts int32  `json:"restarts,omitempty"`
	}

	// K8sContextListInput is the argument schema for k8s_list_contexts.
	K8sContextListInput struct {
		NameContains      string `json:"name_contains,omitempty" jsonschema:"only contexts whose name contains this text, e.g. prod"`
		CheckConnectivity bool   `json:"check_connectivity,omitempty" jsonschema:"ask each context's API server for its version (5s timeout each) to see which clusters are reachable"`
	}

	// K8sContextListResult lists the contexts the agent may pass as context.
	K8sContextListResult struct {
		Source   string       `json:"source"`            // kubeconfig | in-cluster | static
		Default  string       `json:"default,omitempty"` // used when a call passes no context
		Summary  string       `json:"summary"`
		Hidden   int          `json:"hidden,omitempty"` // contexts not allowed by K8S_ALLOWED_CONTEXTS
		Contexts []K8sContext `json:"contexts"`
	}

	// K8sContext describes one context. Credentials are never included,
	// only the kind of authentication.
	K8sContext struct {
		Name      string `json:"name"`
		Default   bool   `json:"default,omitempty"`
		Cluster   string `json:"cluster,omitempty"`
		Server    string `json:"server,omitempty"`
		User      string `json:"user,omitempty"`
		Namespace string `json:"namespace,omitempty"` // kubeconfig default; the k8s tools don't use it
		Auth      string `json:"auth"`                // exec:<cmd> | token | client-certificate | auth-provider:<name> | basic | service-account | none
		Reachable *bool  `json:"reachable,omitempty"` // set by check_connectivity
		Version   string `json:"version,omitempty"`
		Error     string `json:"error,omitempty"`
	}

	// K8sNodeListInput is the argument schema for k8s_list_nodes.
	K8sNodeListInput struct {
		Context       string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		LabelSelector string `json:"label_selector,omitempty" jsonschema:"Kubernetes label selector on nodes, e.g. node.kubernetes.io/instance-type=m5.large or topology.kubernetes.io/zone=eu-west-1a"`
		NameContains  string `json:"name_contains,omitempty" jsonschema:"only nodes whose name contains this text"`
		Role          string `json:"role,omitempty" jsonschema:"only nodes with this role from node-role.kubernetes.io/<role> labels, e.g. control-plane or worker"`
		Status        string `json:"status,omitempty" jsonschema:"only nodes in this state: ready, not_ready (NotReady or Unknown) or cordoned (unschedulable); default all"`
		IncludeLabels bool   `json:"include_labels,omitempty" jsonschema:"also return every node label (verbose)"`
		SkipPodCounts bool   `json:"skip_pod_counts,omitempty" jsonschema:"skip listing pods; faster, but pod counts and CPU/memory requests stay empty"`
		MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum nodes to return, 1-1000; default 200"`
	}

	// K8sNodeListResult is the compact result of k8s_list_nodes.
	K8sNodeListResult struct {
		Context     string    `json:"context,omitempty"`
		Summary     string    `json:"summary"`
		Total       int       `json:"total"` // nodes after filters
		PodsScanned int       `json:"pods_scanned,omitempty"`
		Truncated   bool      `json:"truncated,omitempty"`
		Nodes       []K8sNode `json:"nodes"`
	}

	// K8sNode is one node. Requested values are the sum of pod requests on the
	// node (as kubectl describe node reports them), not live usage.
	K8sNode struct {
		Name             string            `json:"name"`
		Status           string            `json:"status"` // Ready | NotReady | Unknown
		Schedulable      bool              `json:"schedulable"`
		Roles            []string          `json:"roles,omitempty"`
		Problems         []string          `json:"problems,omitempty"` // e.g. MemoryPressure, DiskPressure, NetworkUnavailable
		Taints           []string          `json:"taints,omitempty"`   // key=value:Effect
		KubeletVersion   string            `json:"kubelet_version"`
		OSImage          string            `json:"os_image,omitempty"`
		Kernel           string            `json:"kernel,omitempty"`
		ContainerRuntime string            `json:"container_runtime,omitempty"`
		Arch             string            `json:"arch,omitempty"`
		InternalIP       string            `json:"internal_ip,omitempty"`
		ExternalIP       string            `json:"external_ip,omitempty"`
		Zone             string            `json:"zone,omitempty"`
		InstanceType     string            `json:"instance_type,omitempty"`
		Age              string            `json:"age"`
		CreatedAt        string            `json:"created_at"`
		CPUCapacity      string            `json:"cpu_capacity"`
		CPUAllocatable   string            `json:"cpu_allocatable"`
		CPURequested     string            `json:"cpu_requested,omitempty"`
		CPURequestedPct  *int              `json:"cpu_requested_pct,omitempty"`
		MemCapacity      string            `json:"memory_capacity"`
		MemAllocatable   string            `json:"memory_allocatable"`
		MemRequested     string            `json:"memory_requested,omitempty"`
		MemRequestedPct  *int              `json:"memory_requested_pct,omitempty"`
		PodsAllocatable  int64             `json:"pods_allocatable"`
		Pods             *int              `json:"pods,omitempty"` // non-terminated pods on the node
		Labels           map[string]string `json:"labels,omitempty"`
	}

	// K8sWorkloadListInput is the argument schema for k8s_list_workloads.
	K8sWorkloadListInput struct {
		Context            string   `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		Namespace          string   `json:"namespace,omitempty" jsonschema:"only workloads in this namespace; empty means all namespaces"`
		Kinds              []string `json:"kinds,omitempty" jsonschema:"subset of Deployment, StatefulSet, DaemonSet, CronJob, Job; default all"`
		LabelSelector      string   `json:"label_selector,omitempty" jsonschema:"Kubernetes label selector on the workload objects, e.g. app=api"`
		NameContains       string   `json:"name_contains,omitempty" jsonschema:"only workloads whose name contains this text"`
		UnhealthyOnly      bool     `json:"unhealthy_only,omitempty" jsonschema:"only workloads whose status is degraded, failed or progressing"`
		IncludeCronJobJobs bool     `json:"include_cronjob_jobs,omitempty" jsonschema:"also list Jobs created by CronJobs (hidden by default, they pile up)"`
		MaxResults         int      `json:"max_results,omitempty" jsonschema:"maximum workloads to return, 1-1000; default 200"`
	}

	// K8sWorkloadListResult is the compact result of k8s_list_workloads.
	K8sWorkloadListResult struct {
		Context   string         `json:"context,omitempty"`
		Namespace string         `json:"namespace"` // "" = all namespaces
		Summary   string         `json:"summary"`
		Total     int            `json:"total"`  // after filters
		Counts    map[string]int `json:"counts"` // by status
		Truncated bool           `json:"truncated,omitempty"`
		Errors    []string       `json:"errors,omitempty"` // kinds that could not be listed, e.g. RBAC
		Workloads []K8sWorkload  `json:"workloads"`
	}

	// K8sWorkload is one Deployment, StatefulSet, DaemonSet, Job or CronJob.
	//
	// status: healthy | progressing | degraded | failed | scaled_to_zero |
	// running | complete | suspended | scheduled
	K8sWorkload struct {
		Namespace string   `json:"namespace"`
		Kind      string   `json:"kind"`
		Name      string   `json:"name"`
		Status    string   `json:"status"`
		Message   string   `json:"message,omitempty"` // why it isn't healthy
		Images    []string `json:"images"`
		Age       string   `json:"age"`
		CreatedAt string   `json:"created_at"`

		// Deployment, StatefulSet, DaemonSet
		Desired   *int32 `json:"desired,omitempty"`
		Ready     *int32 `json:"ready,omitempty"`
		Updated   *int32 `json:"updated,omitempty"`
		Available *int32 `json:"available,omitempty"`
		Paused    bool   `json:"paused,omitempty"`

		// Job
		Completions *int32 `json:"completions,omitempty"`
		Succeeded   *int32 `json:"succeeded,omitempty"`
		Failed      *int32 `json:"failed,omitempty"`
		Active      *int32 `json:"active,omitempty"` // also CronJob
		Owner       string `json:"owner,omitempty"`  // CronJob that created the Job

		// CronJob
		Schedule       string `json:"schedule,omitempty"`
		Suspended      bool   `json:"suspended,omitempty"`
		LastSchedule   string `json:"last_schedule,omitempty"`
		LastSuccessful string `json:"last_successful,omitempty"`
	}
)
