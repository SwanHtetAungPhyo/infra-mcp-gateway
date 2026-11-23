package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"ai.engineer.runner/internal/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

var (
	K8sGroupBy        = []string{"image", "pod"}
	K8sContainerKinds = []string{"container", "init", "ephemeral"}
)

const (
	k8sDefaultMax   = 200
	k8sHardMax      = 1000
	k8sPageSize     = 500
	k8sMaxPods      = 20000 // stop paging after this many pods
	k8sMaxWorkloads = 20    // workloads listed per image
)

// LoadKubernetesConfig builds a client: in-cluster service account first,
// otherwise the usual kubeconfig rules ($KUBECONFIG, then ~/.kube/config).
// K8S_CONTEXT picks a kubeconfig context other than the current one.
// It returns the context name in use ("" in-cluster).
func LoadKubernetesConfig() (kubernetes.Interface, string, error) {
	cfg, err := rest.InClusterConfig()
	ctxName := ""
	if errors.Is(err, rest.ErrNotInCluster) {
		rules := clientcmd.NewDefaultClientConfigLoadingRules()
		overrides := &clientcmd.ConfigOverrides{CurrentContext: os.Getenv("K8S_CONTEXT")}
		cc := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides)
		if raw, rerr := cc.RawConfig(); rerr == nil {
			ctxName = raw.CurrentContext
			if overrides.CurrentContext != "" {
				ctxName = overrides.CurrentContext
			}
		}
		cfg, err = cc.ClientConfig()
	}
	if err != nil {
		return nil, "", fmt.Errorf("load kubernetes config: %w", err)
	}

	cfg.QPS, cfg.Burst = 20, 40
	cfg.Timeout = 30 * time.Second
	cs, err := kubernetes.NewForConfig(cfg)
	return cs, ctxName, err
}

// normalizeK8sInput validates and fills defaults. Selectors are parsed
// locally so the agent gets a clear error instead of an API 400.
func normalizeK8sInput(in *types.K8sImageListInput) error {
	in.Namespace = strings.TrimSpace(in.Namespace)
	if in.Namespace != "" && !validDNSLabel(in.Namespace) {
		return fmt.Errorf("namespace %q is not a valid namespace name", in.Namespace)
	}
	if in.LabelSelector != "" {
		if _, err := labels.Parse(in.LabelSelector); err != nil {
			return fmt.Errorf("label_selector: %w", err)
		}
	}
	if in.FieldSelector != "" {
		if _, err := fields.ParseSelector(in.FieldSelector); err != nil {
			return fmt.Errorf("field_selector: %w", err)
		}
	}
	if in.GroupBy == "" {
		in.GroupBy = "image"
	}
	if !slices.Contains(K8sGroupBy, in.GroupBy) {
		return fmt.Errorf("group_by must be one of %v", K8sGroupBy)
	}
	if in.ContainerKind != "" && !slices.Contains(K8sContainerKinds, in.ContainerKind) {
		return fmt.Errorf("container_kind must be one of %v", K8sContainerKinds)
	}
	switch {
	case in.MaxResults == 0:
		in.MaxResults = k8sDefaultMax
	case in.MaxResults < 0 || in.MaxResults > k8sHardMax:
		return fmt.Errorf("max_results must be between 1 and %d", k8sHardMax)
	}
	return nil
}

func validDNSLabel(s string) bool {
	if len(s) > 63 {
		return false
	}
	for i, r := range s {
		ok := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' && i > 0 && i < len(s)-1
		if !ok {
			return false
		}
	}
	return true
}

// ListK8sImages lists pods (paging through the API) and summarises their images.
func ListK8sImages(ctx context.Context, cs kubernetes.Interface, in types.K8sImageListInput) (*types.K8sImageListResult, error) {
	if err := normalizeK8sInput(&in); err != nil {
		return nil, err
	}

	var pods []corev1.Pod
	opts := metav1.ListOptions{LabelSelector: in.LabelSelector, FieldSelector: in.FieldSelector, Limit: k8sPageSize}
	capped := false
	for {
		list, err := cs.CoreV1().Pods(in.Namespace).List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list pods: %w", err)
		}
		pods = append(pods, list.Items...)
		if list.Continue == "" {
			break
		}
		if len(pods) >= k8sMaxPods {
			capped = true
			break
		}
		opts.Continue = list.Continue
	}

	res := &types.K8sImageListResult{Namespace: in.Namespace, GroupBy: in.GroupBy, PodsScanned: len(pods), Truncated: capped}
	byImage := map[string]*imageAgg{}
	var order []string

	for i := range pods {
		p := &pods[i]
		workload := podWorkload(p)
		var podRow *types.K8sPodImage
		for _, c := range podContainers(p) {
			if in.ContainerKind != "" && c.Kind != in.ContainerKind {
				continue
			}
			if in.ImageContains != "" && !strings.Contains(c.Image, in.ImageContains) {
				continue
			}
			agg, ok := byImage[c.Image]
			if !ok {
				agg = newImageAgg(c.Image)
				byImage[c.Image] = agg
				order = append(order, c.Image)
			}
			agg.add(p, workload, c)

			if in.GroupBy == "pod" {
				if podRow == nil {
					podRow = &types.K8sPodImage{Namespace: p.Namespace, Pod: p.Name, Workload: workload, Node: p.Spec.NodeName, Phase: string(p.Status.Phase)}
				}
				podRow.Containers = append(podRow.Containers, c)
			}
		}
		if podRow != nil {
			res.Pods = append(res.Pods, *podRow)
		}
	}
	res.TotalImages = len(byImage)

	if in.GroupBy == "image" {
		for _, img := range order {
			res.Images = append(res.Images, byImage[img].result())
		}
		// Most widely used first, then by name for stable output.
		slices.SortFunc(res.Images, func(a, b types.K8sImage) int {
			if a.Pods != b.Pods {
				return b.Pods - a.Pods
			}
			return strings.Compare(a.Image, b.Image)
		})
		if len(res.Images) > in.MaxResults {
			res.Images, res.Truncated = res.Images[:in.MaxResults], true
		}
	} else if len(res.Pods) > in.MaxResults {
		res.Pods, res.Truncated = res.Pods[:in.MaxResults], true
	}

	scope := "all namespaces"
	if in.Namespace != "" {
		scope = "namespace " + in.Namespace
	}
	res.Summary = fmt.Sprintf("%d unique images across %d pods in %s", res.TotalImages, res.PodsScanned, scope)
	if latest := countLatest(byImage); latest > 0 {
		res.Summary += fmt.Sprintf("; %d use :latest or no tag", latest)
	}
	if res.Truncated {
		res.Summary += "; output truncated, narrow with namespace, selectors or image_contains"
	}
	return res, nil
}

// podContainers returns every container with its resolved image ID.
func podContainers(p *corev1.Pod) []types.K8sContainerImg {
	status := map[string]corev1.ContainerStatus{}
	for _, list := range [][]corev1.ContainerStatus{p.Status.ContainerStatuses, p.Status.InitContainerStatuses, p.Status.EphemeralContainerStatuses} {
		for _, s := range list {
			status[s.Name] = s
		}
	}
	mk := func(name, kind, image string) types.K8sContainerImg {
		s := status[name]
		return types.K8sContainerImg{Name: name, Kind: kind, Image: image, ImageID: s.ImageID, Ready: s.Ready, Restarts: s.RestartCount}
	}
	var out []types.K8sContainerImg
	for _, c := range p.Spec.InitContainers {
		out = append(out, mk(c.Name, "init", c.Image))
	}
	for _, c := range p.Spec.Containers {
		out = append(out, mk(c.Name, "container", c.Image))
	}
	for _, c := range p.Spec.EphemeralContainers {
		out = append(out, mk(c.Name, "ephemeral", c.Image))
	}
	return out
}

// podWorkload names the controller that owns the pod, e.g. "prod/Deployment/api".
// ReplicaSets owned by Deployments are reported as the Deployment, using the
// pod-template-hash label so no extra API call is needed.
func podWorkload(p *corev1.Pod) string {
	ref := metav1.GetControllerOf(p)
	if ref == nil {
		return p.Namespace + "/Pod/" + p.Name
	}
	kind, name := ref.Kind, ref.Name
	if kind == "ReplicaSet" {
		if h := p.Labels["pod-template-hash"]; h != "" && strings.HasSuffix(name, "-"+h) {
			kind, name = "Deployment", strings.TrimSuffix(name, "-"+h)
		}
	}
	return p.Namespace + "/" + kind + "/" + name
}

type imageAgg struct {
	img        types.K8sImage
	pods       map[string]bool
	kinds      map[string]bool
	namespaces map[string]bool
	workloads  map[string]bool
	digests    map[string]bool
}

func newImageAgg(image string) *imageAgg {
	reg, repo, tag, digest := ParseImageRef(image)
	return &imageAgg{
		img:  types.K8sImage{Image: image, Registry: reg, Repository: repo, Tag: tag, Digest: digest},
		pods: map[string]bool{}, kinds: map[string]bool{}, namespaces: map[string]bool{},
		workloads: map[string]bool{}, digests: map[string]bool{},
	}
}

func (a *imageAgg) add(p *corev1.Pod, workload string, c types.K8sContainerImg) {
	a.pods[p.Namespace+"/"+p.Name] = true
	a.img.Containers++
	a.kinds[c.Kind] = true
	a.namespaces[p.Namespace] = true
	a.workloads[workload] = true
	if i := strings.Index(c.ImageID, "sha256:"); i >= 0 {
		a.digests[c.ImageID[i:]] = true
	}
}

func (a *imageAgg) result() types.K8sImage {
	out := a.img
	out.Pods = len(a.pods)
	out.Kinds = sortedKeys(a.kinds)
	out.Namespaces = sortedKeys(a.namespaces)
	out.Running = sortedKeys(a.digests)
	out.Workloads = sortedKeys(a.workloads)
	if len(out.Workloads) > k8sMaxWorkloads {
		extra := len(out.Workloads) - k8sMaxWorkloads
		out.Workloads = append(out.Workloads[:k8sMaxWorkloads], fmt.Sprintf("... and %d more", extra))
	}
	return out
}

func countLatest(m map[string]*imageAgg) int {
	n := 0
	for _, a := range m {
		if a.img.Digest == "" && (a.img.Tag == "" || a.img.Tag == "latest") {
			n++
		}
	}
	return n
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// ParseImageRef splits an image reference the way the container runtime does:
// "nginx" -> docker.io, library/nginx; "ghcr.io/a/b:1@sha256:.." -> ghcr.io, a/b, 1, sha256:..
func ParseImageRef(ref string) (registry, repository, tag, digest string) {
	name := ref
	if i := strings.Index(name, "@"); i >= 0 {
		name, digest = name[:i], name[i+1:]
	}
	if i := strings.LastIndex(name, ":"); i > strings.LastIndex(name, "/") {
		name, tag = name[:i], name[i+1:]
	}
	registry = "docker.io"
	if i := strings.Index(name, "/"); i >= 0 {
		first := name[:i]
		if strings.ContainsAny(first, ".:") || first == "localhost" {
			registry, name = first, name[i+1:]
		}
	}
	if registry == "docker.io" && !strings.Contains(name, "/") {
		name = "library/" + name
	}
	return registry, name, tag, digest
}
