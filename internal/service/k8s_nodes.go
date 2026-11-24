package service

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"ai.engineer.runner/internal/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

var K8sNodeStatuses = []string{"ready", "not_ready", "cordoned"}

const nodeRolePrefix = "node-role.kubernetes.io/"

// checkCommon validates the label selector and fills/limits max_results.
func checkCommon(labelSelector string, maxResults *int) error {
	if labelSelector != "" {
		if _, err := labels.Parse(labelSelector); err != nil {
			return fmt.Errorf("label_selector: %w", err)
		}
	}
	switch {
	case *maxResults == 0:
		*maxResults = k8sDefaultMax
	case *maxResults < 0 || *maxResults > k8sHardMax:
		return fmt.Errorf("max_results must be between 1 and %d", k8sHardMax)
	}
	return nil
}

// ListK8sNodes lists nodes with health, versions, capacity and, unless
// skipped, pod counts and CPU/memory requested per node.
func ListK8sNodes(ctx context.Context, cs kubernetes.Interface, in types.K8sNodeListInput) (*types.K8sNodeListResult, error) {
	if err := checkCommon(in.LabelSelector, &in.MaxResults); err != nil {
		return nil, err
	}
	if in.Status != "" && !slices.Contains(K8sNodeStatuses, in.Status) {
		return nil, fmt.Errorf("status must be one of %v", K8sNodeStatuses)
	}

	var nodes []corev1.Node
	opts := metav1.ListOptions{LabelSelector: in.LabelSelector, Limit: k8sPageSize}
	for {
		list, err := cs.CoreV1().Nodes().List(ctx, opts)
		if err != nil {
			return nil, fmt.Errorf("list nodes: %w", err)
		}
		nodes = append(nodes, list.Items...)
		if list.Continue == "" {
			break
		}
		opts.Continue = list.Continue
	}

	res := &types.K8sNodeListResult{}
	now := time.Now()
	alloc := map[string]corev1.ResourceList{}
	for i := range nodes {
		alloc[nodes[i].Name] = nodes[i].Status.Allocatable
		n := nodeInfo(&nodes[i], now, in.IncludeLabels)
		if in.NameContains != "" && !strings.Contains(n.Name, in.NameContains) ||
			in.Role != "" && !slices.Contains(n.Roles, in.Role) ||
			!nodeMatchesStatus(n, in.Status) {
			continue
		}
		res.Nodes = append(res.Nodes, n)
	}
	slices.SortFunc(res.Nodes, func(a, b types.K8sNode) int { return strings.Compare(a.Name, b.Name) })
	res.Total = len(res.Nodes)

	var cpuReq, cpuAlloc, memReq, memAlloc int64
	if !in.SkipPodCounts && len(res.Nodes) > 0 {
		pods, capped, err := listPods(ctx, cs, "", metav1.ListOptions{})
		if err != nil {
			return nil, err
		}
		res.PodsScanned = len(pods)
		res.Truncated = res.Truncated || capped

		type usage struct {
			pods     int
			cpu, mem int64 // millicores, bytes
		}
		byNode := map[string]*usage{}
		for i := range res.Nodes {
			byNode[res.Nodes[i].Name] = &usage{}
		}
		for i := range pods {
			p := &pods[i]
			u := byNode[p.Spec.NodeName]
			if u == nil || p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
				continue
			}
			cpu, mem := podRequests(p)
			u.pods++
			u.cpu += cpu
			u.mem += mem
		}
		for i := range res.Nodes {
			n := &res.Nodes[i]
			u := byNode[n.Name]
			a := alloc[n.Name]
			ac, am := a.Cpu().MilliValue(), a.Memory().Value()
			n.Pods = &u.pods
			n.CPURequested = formatMilli(u.cpu)
			n.MemRequested = formatBytes(u.mem)
			n.CPURequestedPct = pct(u.cpu, ac)
			n.MemRequestedPct = pct(u.mem, am)
			cpuReq, cpuAlloc, memReq, memAlloc = cpuReq+u.cpu, cpuAlloc+ac, memReq+u.mem, memAlloc+am
		}
	}

	// Summarise every matching node, then cut the list.
	res.Summary = nodeSummary(res.Nodes, res.Total)
	if res.PodsScanned > 0 {
		if c, m := pct(cpuReq, cpuAlloc), pct(memReq, memAlloc); c != nil && m != nil {
			res.Summary += fmt.Sprintf("; requests use %d%% CPU and %d%% memory of allocatable", *c, *m)
		}
	}
	if res.Total > in.MaxResults {
		res.Nodes, res.Truncated = res.Nodes[:in.MaxResults], true
	}
	if res.Truncated {
		res.Summary += "; output truncated, narrow with label_selector, role or name_contains"
	}
	return res, nil
}

func nodeInfo(node *corev1.Node, now time.Time, withLabels bool) types.K8sNode {
	info := node.Status.NodeInfo
	n := types.K8sNode{
		Name:             node.Name,
		Status:           "Unknown",
		Schedulable:      !node.Spec.Unschedulable,
		KubeletVersion:   info.KubeletVersion,
		OSImage:          info.OSImage,
		Kernel:           info.KernelVersion,
		ContainerRuntime: info.ContainerRuntimeVersion,
		Arch:             info.Architecture,
		Zone:             node.Labels["topology.kubernetes.io/zone"],
		InstanceType:     node.Labels["node.kubernetes.io/instance-type"],
		Age:              shortAge(now.Sub(node.CreationTimestamp.Time)),
		CreatedAt:        node.CreationTimestamp.UTC().Format(time.RFC3339),
		CPUCapacity:      formatMilli(node.Status.Capacity.Cpu().MilliValue()),
		CPUAllocatable:   formatMilli(node.Status.Allocatable.Cpu().MilliValue()),
		MemCapacity:      formatBytes(node.Status.Capacity.Memory().Value()),
		MemAllocatable:   formatBytes(node.Status.Allocatable.Memory().Value()),
		PodsAllocatable:  node.Status.Allocatable.Pods().Value(),
	}
	for k := range node.Labels {
		if role, ok := strings.CutPrefix(k, nodeRolePrefix); ok && role != "" {
			n.Roles = append(n.Roles, role)
		}
	}
	slices.Sort(n.Roles)
	for _, c := range node.Status.Conditions {
		switch {
		case c.Type == corev1.NodeReady:
			switch c.Status {
			case corev1.ConditionTrue:
				n.Status = "Ready"
			case corev1.ConditionFalse:
				n.Status = "NotReady"
			}
		case c.Status == corev1.ConditionTrue: // MemoryPressure, DiskPressure, PIDPressure, NetworkUnavailable...
			n.Problems = append(n.Problems, string(c.Type))
		}
	}
	for _, t := range node.Spec.Taints {
		s := t.Key
		if t.Value != "" {
			s += "=" + t.Value
		}
		n.Taints = append(n.Taints, s+":"+string(t.Effect))
	}
	for _, a := range node.Status.Addresses {
		switch a.Type {
		case corev1.NodeInternalIP:
			if n.InternalIP == "" {
				n.InternalIP = a.Address
			}
		case corev1.NodeExternalIP:
			if n.ExternalIP == "" {
				n.ExternalIP = a.Address
			}
		}
	}
	if withLabels {
		n.Labels = maps.Clone(node.Labels)
	}
	return n
}

func nodeMatchesStatus(n types.K8sNode, status string) bool {
	switch status {
	case "ready":
		return n.Status == "Ready"
	case "not_ready":
		return n.Status != "Ready"
	case "cordoned":
		return !n.Schedulable
	}
	return true
}

func nodeSummary(nodes []types.K8sNode, total int) string {
	ready, cordoned := 0, 0
	versions := map[string]bool{}
	var problems []string
	for _, n := range nodes {
		if n.Status == "Ready" {
			ready++
		}
		if !n.Schedulable {
			cordoned++
		}
		versions[n.KubeletVersion] = true
		var why []string
		if n.Status != "Ready" {
			why = append(why, n.Status)
		}
		why = append(why, n.Problems...)
		if len(why) > 0 {
			problems = append(problems, fmt.Sprintf("%s (%s)", n.Name, strings.Join(why, ", ")))
		}
	}
	s := fmt.Sprintf("%d nodes: %d ready", total, ready)
	if nr := len(nodes) - ready; nr > 0 {
		s += fmt.Sprintf(", %d not ready", nr)
	}
	if cordoned > 0 {
		s += fmt.Sprintf(", %d cordoned", cordoned)
	}
	if v := sortedKeys(versions); len(v) > 0 {
		s += "; kubelet " + strings.Join(v, ", ")
		if len(v) > 1 {
			s += " (version skew)"
		}
	}
	if len(problems) > 0 {
		s += "; problems: " + strings.Join(problems, "; ")
	}
	return s
}

// podRequests is the pod's effective CPU (millicores) and memory (bytes)
// request: max(sum of app containers, largest init container), plus overhead.
func podRequests(p *corev1.Pod) (cpu, mem int64) {
	for _, c := range p.Spec.Containers {
		cpu += c.Resources.Requests.Cpu().MilliValue()
		mem += c.Resources.Requests.Memory().Value()
	}
	for _, c := range p.Spec.InitContainers {
		cpu = max(cpu, c.Resources.Requests.Cpu().MilliValue())
		mem = max(mem, c.Resources.Requests.Memory().Value())
	}
	if o := p.Spec.Overhead; o != nil {
		cpu += o.Cpu().MilliValue()
		mem += o.Memory().Value()
	}
	return cpu, mem
}

func pct(part, whole int64) *int {
	if whole <= 0 {
		return nil
	}
	v := int(part * 100 / whole)
	return &v
}

// formatMilli shows CPU like kubectl: whole cores as "4", otherwise "3800m".
func formatMilli(m int64) string {
	if m%1000 == 0 {
		return fmt.Sprintf("%d", m/1000)
	}
	return fmt.Sprintf("%dm", m)
}

// formatBytes shows memory in binary units with one decimal: 15.6Gi, 512.0Mi.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d", b)
	}
	v, suffix := float64(b), ""
	for _, s := range []string{"Ki", "Mi", "Gi", "Ti", "Pi"} {
		if v < unit {
			break
		}
		v, suffix = v/unit, s
	}
	return fmt.Sprintf("%.1f%s", v, suffix)
}
