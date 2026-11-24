package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"ai.engineer.runner/internal/types"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
)

// checkNamespace trims and validates an optional namespace.
func checkNamespace(ns *string) error {
	*ns = strings.TrimSpace(*ns)
	if *ns != "" && !validDNSLabel(*ns) {
		return fmt.Errorf("namespace %q is not a valid namespace name", *ns)
	}
	return nil
}

func scopeOf(ns string) string {
	if ns == "" {
		return "all namespaces"
	}
	return "namespace " + ns
}

// namedList joins up to max names and says how many were left out.
func namedList(names []string, max int) string {
	slices.Sort(names)
	if len(names) <= max {
		return strings.Join(names, ", ")
	}
	return strings.Join(names[:max], ", ") + fmt.Sprintf(" and %d more", len(names)-max)
}

// ListK8sPods lists pods with a kubectl-style status, restarts and the last
// container termination, problem pods first.
func ListK8sPods(ctx context.Context, cs kubernetes.Interface, in types.K8sPodListInput) (*types.K8sPodListResult, error) {
	if err := checkNamespace(&in.Namespace); err != nil {
		return nil, err
	}
	if err := checkCommon(in.LabelSelector, &in.MaxResults); err != nil {
		return nil, err
	}
	fs := in.FieldSelector
	if fs != "" {
		if _, err := fields.ParseSelector(fs); err != nil {
			return nil, fmt.Errorf("field_selector: %w", err)
		}
	}
	if in.Node = strings.TrimSpace(in.Node); in.Node != "" {
		// Let the API server filter by node; escape like kubectl does.
		sel := "spec.nodeName=" + fields.EscapeValue(in.Node)
		if fs != "" {
			sel = fs + "," + sel
		}
		fs = sel
	}

	pods, capped, err := listPods(ctx, cs, in.Namespace, metav1.ListOptions{LabelSelector: in.LabelSelector, FieldSelector: fs})
	if err != nil {
		return nil, err
	}
	res := &types.K8sPodListResult{Namespace: in.Namespace, PodsScanned: len(pods), Truncated: capped, Counts: map[string]int{}}
	now := time.Now()
	var problems []string
	for i := range pods {
		p := podInfo(&pods[i], now)
		if in.NameContains != "" && !strings.Contains(p.Name, in.NameContains) || in.ProblemsOnly && !p.Problem {
			continue
		}
		res.Counts[p.Status]++
		if p.Problem {
			problems = append(problems, fmt.Sprintf("%s/%s (%s)", p.Namespace, p.Name, p.Status))
		}
		res.Pods = append(res.Pods, p)
	}
	slices.SortFunc(res.Pods, func(a, b types.K8sPod) int {
		if a.Problem != b.Problem {
			if a.Problem {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	res.Total = len(res.Pods)
	if res.Total > in.MaxResults {
		res.Pods, res.Truncated = res.Pods[:in.MaxResults], true
	}

	res.Summary = fmt.Sprintf("%d pods in %s", res.Total, scopeOf(in.Namespace))
	if len(res.Counts) > 0 {
		var parts []string
		for _, st := range sortedKeys(boolKeys(res.Counts)) {
			parts = append(parts, fmt.Sprintf("%d %s", res.Counts[st], st))
		}
		res.Summary += " (" + strings.Join(parts, ", ") + ")"
	}
	if len(problems) > 0 {
		res.Summary += fmt.Sprintf("; %d need attention: %s", len(problems), namedList(problems, k8sMaxNamedInSummary))
	}
	if res.Truncated {
		res.Summary += "; output truncated, narrow with namespace, selectors, node or problems_only"
	}
	return res, nil
}

// podInfo works out the pod's status the way kubectl get pods does: a
// container's waiting/terminated reason wins over the pod phase.
func podInfo(p *corev1.Pod, now time.Time) types.K8sPod {
	out := types.K8sPod{
		Namespace: p.Namespace, Name: p.Name, Workload: podWorkload(p),
		Node: p.Spec.NodeName, Phase: string(p.Status.Phase), Status: string(p.Status.Phase),
		PodIP: p.Status.PodIP, QOSClass: string(p.Status.QOSClass),
		Age: shortAge(now.Sub(p.CreationTimestamp.Time)), Message: p.Status.Message,
	}
	if p.Status.Reason != "" { // Evicted, NodeLost...
		out.Status = p.Status.Reason
	}

	// Init containers that haven't finished decide the status first.
	for i, s := range p.Status.InitContainerStatuses {
		if s.State.Terminated != nil && s.State.Terminated.ExitCode == 0 {
			continue
		}
		switch {
		case s.State.Waiting != nil && s.State.Waiting.Reason != "" && s.State.Waiting.Reason != "PodInitializing":
			out.Status = "Init:" + s.State.Waiting.Reason
		case s.State.Terminated != nil:
			out.Status = "Init:" + nonEmpty(s.State.Terminated.Reason, "Error")
		default:
			out.Status = fmt.Sprintf("Init:%d/%d", i, len(p.Spec.InitContainers))
		}
		break
	}

	ready, total := 0, len(p.Spec.Containers)
	var last *corev1.ContainerStateTerminated
	var lastName string
	for _, s := range append(slices.Clone(p.Status.InitContainerStatuses), p.Status.ContainerStatuses...) {
		out.Restarts += s.RestartCount
		for _, t := range []*corev1.ContainerStateTerminated{s.LastTerminationState.Terminated, s.State.Terminated} {
			if t != nil && (last == nil || t.FinishedAt.After(last.FinishedAt.Time)) {
				last, lastName = t, s.Name
			}
		}
	}
	if !strings.HasPrefix(out.Status, "Init:") {
		for _, s := range p.Status.ContainerStatuses {
			if s.Ready {
				ready++
			}
			switch {
			case s.State.Waiting != nil && s.State.Waiting.Reason != "":
				out.Status = s.State.Waiting.Reason // CrashLoopBackOff, ImagePullBackOff, CreateContainerConfigError...
			case s.State.Terminated != nil && s.State.Terminated.Reason != "":
				out.Status = s.State.Terminated.Reason // Completed, OOMKilled, Error
			}
		}
	} else {
		for _, s := range p.Status.ContainerStatuses {
			if s.Ready {
				ready++
			}
		}
	}
	if p.DeletionTimestamp != nil {
		out.Status = "Terminating"
	}
	out.Ready = fmt.Sprintf("%d/%d", ready, total)
	if last != nil {
		out.LastTermination = &types.K8sTermination{Container: lastName, Reason: last.Reason, ExitCode: last.ExitCode}
		if !last.FinishedAt.IsZero() {
			out.LastTermination.FinishedAt = last.FinishedAt.UTC().Format(time.RFC3339)
		}
	}

	switch {
	case p.Status.Phase == corev1.PodSucceeded || out.Status == "Completed" || out.Status == "Terminating":
	case p.Status.Phase == corev1.PodRunning && out.Status == "Running":
		// Running but unready or recently restarting containers still matter.
		out.Problem = ready < total || last != nil && last.Reason == "OOMKilled"
	default:
		out.Problem = true // Pending, Failed, CrashLoopBackOff, ImagePullBackOff, Init:..., Evicted, Unknown
	}
	return out
}

func nonEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
