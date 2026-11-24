package types

// Input/output types for the k8s inventory tools: pods, services, ingresses,
// generic resources, CRDs and Helm releases.

type (
	// K8sPodListInput is the argument schema for k8s_list_pods.
	K8sPodListInput struct {
		Context       string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		Namespace     string `json:"namespace,omitempty" jsonschema:"only pods in this namespace; empty means all namespaces"`
		LabelSelector string `json:"label_selector,omitempty" jsonschema:"Kubernetes label selector, e.g. app=api"`
		FieldSelector string `json:"field_selector,omitempty" jsonschema:"Kubernetes pod field selector, e.g. status.phase=Pending"`
		Node          string `json:"node,omitempty" jsonschema:"only pods scheduled on this node"`
		NameContains  string `json:"name_contains,omitempty" jsonschema:"only pods whose name contains this text"`
		ProblemsOnly  bool   `json:"problems_only,omitempty" jsonschema:"only pods that are not healthy: pending, failed, not ready, restarting, crash-looping, image pull errors, OOMKilled"`
		MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum pods to return, 1-1000; default 200"`
	}

	K8sPodListResult struct {
		Context     string         `json:"context,omitempty"`
		Namespace   string         `json:"namespace"`
		Summary     string         `json:"summary"`
		PodsScanned int            `json:"pods_scanned"`
		Total       int            `json:"total"`  // after filters
		Counts      map[string]int `json:"counts"` // by status
		Truncated   bool           `json:"truncated,omitempty"`
		Pods        []K8sPod       `json:"pods"`
	}

	// K8sPod is one pod. Status is the kubectl STATUS column: Running,
	// Pending, Completed, CrashLoopBackOff, ImagePullBackOff, OOMKilled...
	K8sPod struct {
		Namespace       string          `json:"namespace"`
		Name            string          `json:"name"`
		Workload        string          `json:"workload,omitempty"`
		Node            string          `json:"node,omitempty"`
		Phase           string          `json:"phase"`
		Status          string          `json:"status"`
		Problem         bool            `json:"problem,omitempty"`
		Ready           string          `json:"ready"` // e.g. 1/2
		Restarts        int32           `json:"restarts"`
		LastTermination *K8sTermination `json:"last_termination,omitempty"`
		Message         string          `json:"message,omitempty"`
		PodIP           string          `json:"pod_ip,omitempty"`
		QOSClass        string          `json:"qos_class,omitempty"`
		Age             string          `json:"age"`
	}

	// K8sTermination is the most recent container termination in the pod.
	K8sTermination struct {
		Container  string `json:"container"`
		Reason     string `json:"reason,omitempty"` // OOMKilled, Error, Completed...
		ExitCode   int32  `json:"exit_code"`
		FinishedAt string `json:"finished_at,omitempty"`
	}

	// K8sServiceListInput is the argument schema for k8s_list_services.
	K8sServiceListInput struct {
		Context       string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		Namespace     string `json:"namespace,omitempty" jsonschema:"only services in this namespace; empty means all namespaces"`
		LabelSelector string `json:"label_selector,omitempty" jsonschema:"Kubernetes label selector on services"`
		NameContains  string `json:"name_contains,omitempty" jsonschema:"only services whose name contains this text"`
		Type          string `json:"type,omitempty" jsonschema:"only this service type: ClusterIP, NodePort, LoadBalancer or ExternalName"`
		ExposedOnly   bool   `json:"exposed_only,omitempty" jsonschema:"only services reachable from outside the cluster: LoadBalancer, NodePort or with externalIPs"`
		MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum services to return, 1-1000; default 200"`
	}

	K8sServiceListResult struct {
		Context   string       `json:"context,omitempty"`
		Namespace string       `json:"namespace"`
		Summary   string       `json:"summary"`
		Total     int          `json:"total"`
		Truncated bool         `json:"truncated,omitempty"`
		Warnings  []string     `json:"warnings,omitempty"` // e.g. endpoints could not be listed
		Services  []K8sService `json:"services"`
	}

	K8sService struct {
		Namespace      string            `json:"namespace"`
		Name           string            `json:"name"`
		Type           string            `json:"type"`
		ClusterIP      string            `json:"cluster_ip,omitempty"`
		External       []string          `json:"external,omitempty"` // LB IPs/hostnames, externalIPs, externalName
		Exposed        bool              `json:"exposed,omitempty"`
		Ports          []string          `json:"ports,omitempty"` // name 80:30080/TCP->8080
		Selector       map[string]string `json:"selector,omitempty"`
		ReadyEndpoints *int              `json:"ready_endpoints,omitempty"`
		Age            string            `json:"age"`
	}

	// K8sIngressListInput is the argument schema for k8s_list_ingresses.
	K8sIngressListInput struct {
		Context       string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		Namespace     string `json:"namespace,omitempty" jsonschema:"only ingresses in this namespace; empty means all namespaces"`
		LabelSelector string `json:"label_selector,omitempty" jsonschema:"Kubernetes label selector on ingresses"`
		HostContains  string `json:"host_contains,omitempty" jsonschema:"only ingresses with a host containing this text, e.g. example.com"`
		MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum ingresses to return, 1-1000; default 200"`
	}

	K8sIngressListResult struct {
		Context   string       `json:"context,omitempty"`
		Namespace string       `json:"namespace"`
		Summary   string       `json:"summary"`
		Total     int          `json:"total"`
		Truncated bool         `json:"truncated,omitempty"`
		Ingresses []K8sIngress `json:"ingresses"`
	}

	K8sIngress struct {
		Namespace  string   `json:"namespace"`
		Name       string   `json:"name"`
		Class      string   `json:"class,omitempty"`
		Hosts      []string `json:"hosts,omitempty"`
		Rules      []string `json:"rules"`         // host/path -> service:port
		TLS        []string `json:"tls,omitempty"` // host,host (secret name)
		NoTLSHosts []string `json:"hosts_without_tls,omitempty"`
		Addresses  []string `json:"addresses,omitempty"`
		Age        string   `json:"age"`
	}

	// K8sResourceGetInput is the argument schema for k8s_get_resource.
	K8sResourceGetInput struct {
		Context       string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		Kind          string `json:"kind" jsonschema:"kind, resource name or short name, optionally with group: Deployment, deployments, deploy, certificates.cert-manager.io"`
		APIVersion    string `json:"api_version,omitempty" jsonschema:"pin a version, e.g. apps/v1 or v1; default the server's preferred version"`
		Name          string `json:"name,omitempty" jsonschema:"object name; empty lists objects of this kind (name, namespace, age)"`
		Namespace     string `json:"namespace,omitempty" jsonschema:"namespace for namespaced kinds; empty with no name lists all namespaces; required with name"`
		LabelSelector string `json:"label_selector,omitempty" jsonschema:"when listing: Kubernetes label selector"`
		MaxResults    int    `json:"max_results,omitempty" jsonschema:"when listing: maximum objects, 1-1000; default 200"`
	}

	K8sResourceGetResult struct {
		Context    string           `json:"context,omitempty"`
		Summary    string           `json:"summary"`
		APIVersion string           `json:"api_version"`
		Kind       string           `json:"kind"`
		Resource   string           `json:"resource"` // plural, with group
		Namespaced bool             `json:"namespaced"`
		YAML       string           `json:"yaml,omitempty"` // get: trimmed object
		Redacted   []string         `json:"redacted,omitempty"`
		Truncated  bool             `json:"truncated,omitempty"`
		Items      []K8sObjectBrief `json:"items,omitempty"` // list
		Total      int              `json:"total,omitempty"`
	}

	K8sObjectBrief struct {
		Namespace string `json:"namespace,omitempty"`
		Name      string `json:"name"`
		Age       string `json:"age"`
	}

	// K8sCRDListInput is the argument schema for k8s_list_crds.
	K8sCRDListInput struct {
		Context       string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		GroupContains string `json:"group_contains,omitempty" jsonschema:"only CRDs whose API group contains this text, e.g. cert-manager or istio"`
		MaxResults    int    `json:"max_results,omitempty" jsonschema:"maximum CRDs to return, 1-1000; default 200"`
	}

	K8sCRDListResult struct {
		Context   string   `json:"context,omitempty"`
		Summary   string   `json:"summary"`
		Total     int      `json:"total"`
		Groups    []string `json:"groups"`
		Truncated bool     `json:"truncated,omitempty"`
		CRDs      []K8sCRD `json:"crds"`
	}

	K8sCRD struct {
		Name        string   `json:"name"` // plural.group
		Group       string   `json:"group"`
		Kind        string   `json:"kind"`
		Scope       string   `json:"scope"`
		Versions    []string `json:"versions"` // v1 (storage), v1beta1 (served)...
		ShortNames  []string `json:"short_names,omitempty"`
		Established bool     `json:"established"`
		Age         string   `json:"age"`
	}

	// K8sHelmListInput is the argument schema for k8s_list_helm_releases.
	K8sHelmListInput struct {
		Context      string `json:"context,omitempty" jsonschema:"kubeconfig context to use, from k8s_list_contexts; default is the gateway's default context"`
		Namespace    string `json:"namespace,omitempty" jsonschema:"only releases in this namespace; empty means all namespaces"`
		NameContains string `json:"name_contains,omitempty" jsonschema:"only releases whose name contains this text"`
		ProblemsOnly bool   `json:"problems_only,omitempty" jsonschema:"only releases whose latest revision is failed or stuck pending"`
		MaxResults   int    `json:"max_results,omitempty" jsonschema:"maximum releases to return, 1-1000; default 200"`
	}

	K8sHelmListResult struct {
		Context   string           `json:"context,omitempty"`
		Namespace string           `json:"namespace"`
		Summary   string           `json:"summary"`
		Total     int              `json:"total"`
		Truncated bool             `json:"truncated,omitempty"`
		Releases  []K8sHelmRelease `json:"releases"`
	}

	// K8sHelmRelease comes from release Secret labels only: chart contents and
	// values are never read.
	K8sHelmRelease struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
		Revision  int    `json:"revision"`
		Status    string `json:"status"` // deployed, failed, pending-upgrade...
		Problem   bool   `json:"problem,omitempty"`
		Revisions int    `json:"revisions_kept"`
		Updated   string `json:"updated,omitempty"`
	}
)
