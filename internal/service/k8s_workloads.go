package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"ai.engineer.runner/internal/types"
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var K8sWorkloadKinds = []string{"Deployment", "StatefulSet", "DaemonSet", "CronJob", "Job"}

// k8sUnhealthy are the statuses unhealthy_only keeps.
var k8sUnhealthy = map[string]bool{"degraded": true, "failed": true, "progressing": true}

const (
	k8sMaxWorkloadItems  = 20000 // per kind
	k8sMaxNamedInSummary = 10
)

// ListK8sWorkloads lists Deployments, StatefulSets, DaemonSets, CronJobs and
// Jobs with a health status. A kind that can't be listed (e.g. RBAC) is
// reported in errors instead of failing the whole call.
func ListK8sWorkloads(ctx context.Context, cs kubernetes.Interface, in types.K8sWorkloadListInput) (*types.K8sWorkloadListResult, error) {
	in.Namespace = strings.TrimSpace(in.Namespace)
	if in.Namespace != "" && !validDNSLabel(in.Namespace) {
		return nil, fmt.Errorf("namespace %q is not a valid namespace name", in.Namespace)
	}
	if err := checkCommon(in.LabelSelector, &in.MaxResults); err != nil {
		return nil, err
	}
	kinds := in.Kinds
	if len(kinds) == 0 {
		kinds = K8sWorkloadKinds
	}
	for _, k := range kinds {
		if !slices.Contains(K8sWorkloadKinds, k) {
			return nil, fmt.Errorf("kinds must be a subset of %v", K8sWorkloadKinds)
		}
	}

	res := &types.K8sWorkloadListResult{Namespace: in.Namespace, Counts: map[string]int{}}
	opts := metav1.ListOptions{LabelSelector: in.LabelSelector}
	now := time.Now()
	var all []types.K8sWorkload

	for _, kind := range K8sWorkloadKinds { // fixed order, whatever order kinds came in
		if !slices.Contains(kinds, kind) {
			continue
		}
		items, capped, err := listWorkloadKind(ctx, cs, kind, in.Namespace, opts, now, in.IncludeCronJobJobs)
		if err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			res.Errors = append(res.Errors, fmt.Sprintf("%s: %s", kind, trimErr(err)))
			continue
		}
		res.Truncated = res.Truncated || capped
		all = append(all, items...)
	}

	var unhealthy []string
	for _, w := range all {
		if in.NameContains != "" && !strings.Contains(w.Name, in.NameContains) ||
			in.UnhealthyOnly && !k8sUnhealthy[w.Status] {
			continue
		}
		res.Counts[w.Status]++
		if k8sUnhealthy[w.Status] {
			unhealthy = append(unhealthy, w.Namespace+"/"+w.Kind+"/"+w.Name)
		}
		res.Workloads = append(res.Workloads, w)
	}
	// Problems first, then by namespace, kind, name.
	slices.SortFunc(res.Workloads, func(a, b types.K8sWorkload) int {
		if ua, ub := k8sUnhealthy[a.Status], k8sUnhealthy[b.Status]; ua != ub {
			if ua {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Namespace+"/"+a.Kind+"/"+a.Name, b.Namespace+"/"+b.Kind+"/"+b.Name)
	})
	res.Total = len(res.Workloads)
	if res.Total > in.MaxResults {
		res.Workloads, res.Truncated = res.Workloads[:in.MaxResults], true
	}

	scope := "all namespaces"
	if in.Namespace != "" {
		scope = "namespace " + in.Namespace
	}
	res.Summary = fmt.Sprintf("%d workloads in %s", res.Total, scope)
	if len(res.Counts) > 0 {
		var parts []string
		for _, st := range sortedKeys(boolKeys(res.Counts)) {
			parts = append(parts, fmt.Sprintf("%d %s", res.Counts[st], st))
		}
		res.Summary += " (" + strings.Join(parts, ", ") + ")"
	}
	if len(unhealthy) > 0 {
		slices.Sort(unhealthy)
		shown := unhealthy
		if len(shown) > k8sMaxNamedInSummary {
			shown = shown[:k8sMaxNamedInSummary]
		}
		res.Summary += "; needs attention: " + strings.Join(shown, ", ")
		if extra := len(unhealthy) - len(shown); extra > 0 {
			res.Summary += fmt.Sprintf(" and %d more", extra)
		}
	}
	if len(res.Errors) > 0 {
		res.Summary += fmt.Sprintf("; %d kinds could not be listed, see errors", len(res.Errors))
	}
	if res.Truncated {
		res.Summary += "; output truncated, narrow with namespace, kinds, label_selector or unhealthy_only"
	}
	return res, nil
}

func boolKeys(m map[string]int) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// pageAll pages through one list call until done or max items.
func pageAll[T any](ctx context.Context, opts metav1.ListOptions, list func(context.Context, metav1.ListOptions) ([]T, string, error)) ([]T, bool, error) {
	opts.Limit = k8sPageSize
	var out []T
	for {
		items, cont, err := list(ctx, opts)
		if err != nil {
			return nil, false, err
		}
		out = append(out, items...)
		if cont == "" {
			return out, false, nil
		}
		if len(out) >= k8sMaxWorkloadItems {
			return out, true, nil
		}
		opts.Continue = cont
	}
}

func listWorkloadKind(ctx context.Context, cs kubernetes.Interface, kind, ns string, opts metav1.ListOptions, now time.Time, cronJobJobs bool) ([]types.K8sWorkload, bool, error) {
	var out []types.K8sWorkload
	switch kind {
	case "Deployment":
		items, capped, err := pageAll(ctx, opts, func(ctx context.Context, o metav1.ListOptions) ([]appsv1.Deployment, string, error) {
			l, err := cs.AppsV1().Deployments(ns).List(ctx, o)
			if err != nil {
				return nil, "", err
			}
			return l.Items, l.Continue, nil
		})
		for i := range items {
			out = append(out, deploymentInfo(&items[i], now))
		}
		return out, capped, err
	case "StatefulSet":
		items, capped, err := pageAll(ctx, opts, func(ctx context.Context, o metav1.ListOptions) ([]appsv1.StatefulSet, string, error) {
			l, err := cs.AppsV1().StatefulSets(ns).List(ctx, o)
			if err != nil {
				return nil, "", err
			}
			return l.Items, l.Continue, nil
		})
		for i := range items {
			out = append(out, statefulSetInfo(&items[i], now))
		}
		return out, capped, err
	case "DaemonSet":
		items, capped, err := pageAll(ctx, opts, func(ctx context.Context, o metav1.ListOptions) ([]appsv1.DaemonSet, string, error) {
			l, err := cs.AppsV1().DaemonSets(ns).List(ctx, o)
			if err != nil {
				return nil, "", err
			}
			return l.Items, l.Continue, nil
		})
		for i := range items {
			out = append(out, daemonSetInfo(&items[i], now))
		}
		return out, capped, err
	case "CronJob":
		items, capped, err := pageAll(ctx, opts, func(ctx context.Context, o metav1.ListOptions) ([]batchv1.CronJob, string, error) {
			l, err := cs.BatchV1().CronJobs(ns).List(ctx, o)
			if err != nil {
				return nil, "", err
			}
			return l.Items, l.Continue, nil
		})
		for i := range items {
			out = append(out, cronJobInfo(&items[i], now))
		}
		return out, capped, err
	case "Job":
		items, capped, err := pageAll(ctx, opts, func(ctx context.Context, o metav1.ListOptions) ([]batchv1.Job, string, error) {
			l, err := cs.BatchV1().Jobs(ns).List(ctx, o)
			if err != nil {
				return nil, "", err
			}
			return l.Items, l.Continue, nil
		})
		for i := range items {
			w := jobInfo(&items[i], now)
			if w.Owner != "" && !cronJobJobs {
				continue
			}
			out = append(out, w)
		}
		return out, capped, err
	}
	return nil, false, fmt.Errorf("unknown kind %q", kind)
}

func baseWorkload(kind string, m *metav1.ObjectMeta, spec *corev1.PodSpec, now time.Time) types.K8sWorkload {
	w := types.K8sWorkload{
		Namespace: m.Namespace, Kind: kind, Name: m.Name,
		Age:       shortAge(now.Sub(m.CreationTimestamp.Time)),
		CreatedAt: m.CreationTimestamp.UTC().Format(time.RFC3339),
	}
	seen := map[string]bool{}
	for _, list := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, c := range list {
			if !seen[c.Image] {
				seen[c.Image] = true
				w.Images = append(w.Images, c.Image)
			}
		}
	}
	return w
}

func i32(v int32) *int32 { return &v }

func replicas(p *int32) int32 {
	if p == nil {
		return 1
	}
	return *p
}

// rolloutStatus applies the same rules to Deployments, StatefulSets and DaemonSets.
func rolloutStatus(desired, ready, updated, available int32, stale bool) string {
	switch {
	case desired == 0:
		return "scaled_to_zero"
	case stale || updated < desired:
		if available < desired {
			return "degraded"
		}
		return "progressing"
	case available < desired || ready < desired:
		return "degraded"
	}
	return "healthy"
}

func deploymentInfo(d *appsv1.Deployment, now time.Time) types.K8sWorkload {
	w := baseWorkload("Deployment", &d.ObjectMeta, &d.Spec.Template.Spec, now)
	desired, st := replicas(d.Spec.Replicas), d.Status
	w.Desired, w.Ready, w.Updated, w.Available = i32(desired), i32(st.ReadyReplicas), i32(st.UpdatedReplicas), i32(st.AvailableReplicas)
	w.Paused = d.Spec.Paused
	w.Status = rolloutStatus(desired, st.ReadyReplicas, st.UpdatedReplicas, st.AvailableReplicas, st.ObservedGeneration < d.Generation)
	for _, c := range st.Conditions {
		switch {
		case c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded":
			w.Status, w.Message = "failed", c.Message
		case c.Type == appsv1.DeploymentReplicaFailure && c.Status == corev1.ConditionTrue:
			w.Status, w.Message = "failed", c.Message
		case c.Type == appsv1.DeploymentAvailable && c.Status != corev1.ConditionTrue && w.Message == "":
			w.Message = c.Message
		}
	}
	if w.Status != "healthy" && w.Message == "" {
		w.Message = fmt.Sprintf("%d/%d ready, %d updated, %d available", st.ReadyReplicas, desired, st.UpdatedReplicas, st.AvailableReplicas)
	}
	if w.Status == "healthy" {
		w.Message = ""
	}
	return w
}

func statefulSetInfo(s *appsv1.StatefulSet, now time.Time) types.K8sWorkload {
	w := baseWorkload("StatefulSet", &s.ObjectMeta, &s.Spec.Template.Spec, now)
	desired, st := replicas(s.Spec.Replicas), s.Status
	w.Desired, w.Ready, w.Updated, w.Available = i32(desired), i32(st.ReadyReplicas), i32(st.UpdatedReplicas), i32(st.AvailableReplicas)
	stale := st.ObservedGeneration < s.Generation || st.UpdateRevision != "" && st.CurrentRevision != st.UpdateRevision
	w.Status = rolloutStatus(desired, st.ReadyReplicas, st.UpdatedReplicas, st.AvailableReplicas, stale)
	if w.Status != "healthy" && w.Status != "scaled_to_zero" {
		w.Message = fmt.Sprintf("%d/%d ready, %d updated", st.ReadyReplicas, desired, st.UpdatedReplicas)
	}
	return w
}

func daemonSetInfo(d *appsv1.DaemonSet, now time.Time) types.K8sWorkload {
	w := baseWorkload("DaemonSet", &d.ObjectMeta, &d.Spec.Template.Spec, now)
	st := d.Status
	desired := st.DesiredNumberScheduled
	w.Desired, w.Ready, w.Updated, w.Available = i32(desired), i32(st.NumberReady), i32(st.UpdatedNumberScheduled), i32(st.NumberAvailable)
	w.Status = rolloutStatus(desired, st.NumberReady, st.UpdatedNumberScheduled, st.NumberAvailable, st.ObservedGeneration < d.Generation)
	if desired == 0 {
		w.Status = "healthy" // no node matches: nothing to run
	}
	var notes []string
	if w.Status != "healthy" {
		notes = append(notes, fmt.Sprintf("%d/%d ready, %d updated", st.NumberReady, desired, st.UpdatedNumberScheduled))
	}
	if st.NumberMisscheduled > 0 {
		notes = append(notes, fmt.Sprintf("%d misscheduled", st.NumberMisscheduled))
	}
	w.Message = strings.Join(notes, "; ")
	return w
}

func jobInfo(j *batchv1.Job, now time.Time) types.K8sWorkload {
	w := baseWorkload("Job", &j.ObjectMeta, &j.Spec.Template.Spec, now)
	if ref := metav1.GetControllerOf(j); ref != nil && ref.Kind == "CronJob" {
		w.Owner = ref.Name
	}
	st := j.Status
	w.Completions = i32(replicas(j.Spec.Completions))
	w.Succeeded, w.Failed, w.Active = i32(st.Succeeded), i32(st.Failed), i32(st.Active)
	w.Status = "running"
	if j.Spec.Suspend != nil && *j.Spec.Suspend {
		w.Status = "suspended"
	}
	for _, c := range st.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			w.Status = "complete"
		case batchv1.JobFailed:
			w.Status, w.Message = "failed", strings.TrimSpace(c.Reason+": "+c.Message)
		case batchv1.JobSuspended:
			w.Status = "suspended"
		}
	}
	return w
}

func cronJobInfo(c *batchv1.CronJob, now time.Time) types.K8sWorkload {
	w := baseWorkload("CronJob", &c.ObjectMeta, &c.Spec.JobTemplate.Spec.Template.Spec, now)
	w.Schedule = c.Spec.Schedule
	w.Suspended = c.Spec.Suspend != nil && *c.Spec.Suspend
	w.Active = i32(int32(len(c.Status.Active)))
	if t := c.Status.LastScheduleTime; t != nil {
		w.LastSchedule = t.UTC().Format(time.RFC3339)
	}
	if t := c.Status.LastSuccessfulTime; t != nil {
		w.LastSuccessful = t.UTC().Format(time.RFC3339)
	}
	switch {
	case w.Suspended:
		w.Status = "suspended"
	case len(c.Status.Active) > 0:
		w.Status = "running"
	default:
		w.Status = "scheduled"
	}
	// The last run started after the last success and nothing is running:
	// it most likely failed. Its Job (include_cronjob_jobs) has the reason.
	if ls, ok := c.Status.LastScheduleTime, c.Status.LastSuccessfulTime; ls != nil && len(c.Status.Active) == 0 && (ok == nil || ok.Before(ls)) {
		w.Message = "last scheduled run has not succeeded; list its Jobs with include_cronjob_jobs for details"
	}
	return w
}
