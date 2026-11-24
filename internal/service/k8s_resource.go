package service

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"ai.engineer.runner/internal/types"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery/cached/memory"
	"k8s.io/client-go/restmapper"
	"sigs.k8s.io/yaml"
)

const (
	k8sMaxYAMLBytes = 64 << 10
	redactedValue   = "<redacted>"
)

var crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}

// GetK8sResource gets one object of any kind (including CRDs) as trimmed,
// redacted YAML, or lists objects of that kind when name is empty.
func GetK8sResource(ctx context.Context, c *K8sClients, in types.K8sResourceGetInput) (*types.K8sResourceGetResult, error) {
	if c.Dynamic == nil {
		return nil, errors.New("dynamic client unavailable for this context")
	}
	in.Kind, in.Name = strings.TrimSpace(in.Kind), strings.TrimSpace(in.Name)
	if in.Kind == "" {
		return nil, errors.New("kind is required, e.g. Deployment, svc or certificates.cert-manager.io")
	}
	if err := checkNamespace(&in.Namespace); err != nil {
		return nil, err
	}
	if in.Name != "" && (len(in.Name) > 253 || strings.ContainsAny(in.Name, "/ \\")) {
		return nil, fmt.Errorf("name %q is not a valid object name", in.Name)
	}
	if err := checkCommon(in.LabelSelector, &in.MaxResults); err != nil {
		return nil, err
	}

	mapping, err := resolveKind(c, in.Kind, in.APIVersion)
	if err != nil {
		return nil, err
	}
	gvr := mapping.Resource
	res := &types.K8sResourceGetResult{
		APIVersion: mapping.GroupVersionKind.GroupVersion().String(),
		Kind:       mapping.GroupVersionKind.Kind,
		Resource:   gvr.GroupResource().String(),
		Namespaced: mapping.Scope.Name() == meta.RESTScopeNameNamespace,
	}
	ns := in.Namespace
	if !res.Namespaced {
		ns = ""
	}

	if in.Name != "" {
		if res.Namespaced && ns == "" {
			return nil, fmt.Errorf("%s is namespaced: pass namespace with name", res.Kind)
		}
		obj, err := c.Dynamic.Resource(gvr).Namespace(ns).Get(ctx, in.Name, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get %s %s: %w", res.Resource, in.Name, err)
		}
		res.Redacted = sanitizeObject(obj.Object, gvr.Group == "" && res.Kind == "Secret")
		b, err := yaml.Marshal(obj.Object)
		if err != nil {
			return nil, err
		}
		if len(b) > k8sMaxYAMLBytes {
			b, res.Truncated = b[:k8sMaxYAMLBytes], true
		}
		res.YAML = string(b)
		res.Summary = fmt.Sprintf("%s %s", res.Kind, qualified(ns, in.Name))
		if len(res.Redacted) > 0 {
			res.Summary += "; redacted: " + strings.Join(res.Redacted, ", ")
		}
		if res.Truncated {
			res.Summary += "; YAML truncated at 64 KiB"
		}
		return res, nil
	}

	items, capped, err := listBriefs(ctx, c, gvr, ns, in.LabelSelector)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", res.Resource, err)
	}
	sort.Slice(items, func(i, j int) bool {
		return qualified(items[i].Namespace, items[i].Name) < qualified(items[j].Namespace, items[j].Name)
	})
	res.Total, res.Truncated = len(items), capped
	if len(items) > in.MaxResults {
		items, res.Truncated = items[:in.MaxResults], true
	}
	res.Items = items
	res.Summary = fmt.Sprintf("%d %s", res.Total, res.Resource)
	if res.Namespaced {
		res.Summary += " in " + scopeOf(ns)
	}
	res.Summary += "; pass name to get one as YAML"
	if res.Truncated {
		res.Summary += "; list truncated"
	}
	return res, nil
}

func qualified(ns, name string) string {
	if ns == "" {
		return name
	}
	return ns + "/" + name
}

// resolveKind maps "Deployment", "deployments", "deploy" or
// "certificates.cert-manager.io" (plus an optional api_version) to a REST mapping
// using the cluster's discovery data, so CRDs work too.
func resolveKind(c *K8sClients, kind, apiVersion string) (*meta.RESTMapping, error) {
	dc := memory.NewMemCacheClient(c.Typed.Discovery())
	mapper := restmapper.NewShortcutExpander(restmapper.NewDeferredDiscoveryRESTMapper(dc), dc, nil)

	gr := schema.ParseGroupResource(strings.ToLower(kind))
	var version string
	if apiVersion != "" {
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil {
			return nil, fmt.Errorf("api_version: %w", err)
		}
		version = gv.Version
		if gr.Group == "" {
			gr.Group = gv.Group
		}
	}
	gvr, err := mapper.ResourceFor(gr.WithVersion(version))
	if err != nil {
		return nil, fmt.Errorf("unknown kind %q (see k8s_list_crds for custom kinds): %w", kind, err)
	}
	gvk, err := mapper.KindFor(gvr)
	if err != nil {
		return nil, fmt.Errorf("kind for %s: %w", gvr, err)
	}
	return mapper.RESTMapping(gvk.GroupKind(), gvk.Version)
}

// listBriefs lists names via the metadata client (object bodies never leave
// the API server), falling back to the dynamic client.
func listBriefs(ctx context.Context, c *K8sClients, gvr schema.GroupVersionResource, ns, sel string) ([]types.K8sObjectBrief, bool, error) {
	now := time.Now()
	brief := func(m metav1.Object) types.K8sObjectBrief {
		return types.K8sObjectBrief{Namespace: m.GetNamespace(), Name: m.GetName(), Age: shortAge(now.Sub(m.GetCreationTimestamp().Time))}
	}
	opts := metav1.ListOptions{LabelSelector: sel}
	if c.Metadata != nil {
		items, capped, err := pageAll(ctx, opts, func(ctx context.Context, o metav1.ListOptions) ([]metav1.PartialObjectMetadata, string, error) {
			l, err := c.Metadata.Resource(gvr).Namespace(ns).List(ctx, o)
			if err != nil {
				return nil, "", err
			}
			return l.Items, l.Continue, nil
		})
		out := make([]types.K8sObjectBrief, 0, len(items))
		for i := range items {
			out = append(out, brief(&items[i]))
		}
		return out, capped, err
	}
	items, capped, err := pageAll(ctx, opts, func(ctx context.Context, o metav1.ListOptions) ([]unstructured.Unstructured, string, error) {
		l, err := c.Dynamic.Resource(gvr).Namespace(ns).List(ctx, o)
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.GetContinue(), nil
	})
	out := make([]types.K8sObjectBrief, 0, len(items))
	for i := range items {
		out = append(out, brief(&items[i]))
	}
	return out, capped, err
}

// sanitizeObject trims noise and removes values that are often secret. It
// returns what it redacted.
func sanitizeObject(obj map[string]any, isSecret bool) []string {
	var redacted []string
	if md, ok := obj["metadata"].(map[string]any); ok {
		delete(md, "managedFields")
		if ann, ok := md["annotations"].(map[string]any); ok {
			// Holds the full applied object, Secret data included.
			if _, ok := ann["kubectl.kubernetes.io/last-applied-configuration"]; ok {
				ann["kubectl.kubernetes.io/last-applied-configuration"] = redactedValue
				redacted = append(redacted, "last-applied-configuration annotation")
			}
		}
	}
	if isSecret {
		for _, field := range []string{"data", "stringData"} {
			if m, ok := obj[field].(map[string]any); ok && len(m) > 0 {
				for k := range m {
					m[k] = redactedValue
				}
				redacted = append(redacted, "secret "+field+" values")
			}
		}
	}
	if n := redactEnv(obj); n > 0 {
		redacted = append(redacted, fmt.Sprintf("%d env values", n))
	}
	return redacted
}

// redactEnv replaces literal env values (env[].value) anywhere in the object,
// e.g. in pod templates. valueFrom references are kept: they hold no secret.
func redactEnv(v any) int {
	n := 0
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			if k == "env" {
				if list, ok := child.([]any); ok {
					for _, e := range list {
						if m, ok := e.(map[string]any); ok {
							if _, has := m["value"]; has {
								m["value"] = redactedValue
								n++
							}
						}
					}
					continue
				}
			}
			n += redactEnv(child)
		}
	case []any:
		for _, child := range t {
			n += redactEnv(child)
		}
	}
	return n
}

// ListK8sCRDs lists CustomResourceDefinitions with group, kind, scope and versions.
func ListK8sCRDs(ctx context.Context, c *K8sClients, in types.K8sCRDListInput) (*types.K8sCRDListResult, error) {
	if c.Dynamic == nil {
		return nil, errors.New("dynamic client unavailable for this context")
	}
	if err := checkCommon("", &in.MaxResults); err != nil {
		return nil, err
	}
	items, capped, err := pageAll(ctx, metav1.ListOptions{}, func(ctx context.Context, o metav1.ListOptions) ([]unstructured.Unstructured, string, error) {
		l, err := c.Dynamic.Resource(crdGVR).List(ctx, o)
		if err != nil {
			return nil, "", fmt.Errorf("list crds: %w", err)
		}
		return l.Items, l.GetContinue(), nil
	})
	if err != nil {
		return nil, err
	}

	res := &types.K8sCRDListResult{Truncated: capped}
	now := time.Now()
	groups := map[string]bool{}
	var notEstablished []string
	for i := range items {
		crd := crdInfo(&items[i], now)
		if in.GroupContains != "" && !strings.Contains(crd.Group, in.GroupContains) {
			continue
		}
		groups[crd.Group] = true
		if !crd.Established {
			notEstablished = append(notEstablished, crd.Name)
		}
		res.CRDs = append(res.CRDs, crd)
	}
	slices.SortFunc(res.CRDs, func(a, b types.K8sCRD) int { return strings.Compare(a.Name, b.Name) })
	res.Groups = sortedKeys(groups)
	res.Total = len(res.CRDs)
	if res.Total > in.MaxResults {
		res.CRDs, res.Truncated = res.CRDs[:in.MaxResults], true
	}
	res.Summary = fmt.Sprintf("%d CRDs in %d API groups", res.Total, len(res.Groups))
	if len(notEstablished) > 0 {
		res.Summary += "; not established: " + namedList(notEstablished, k8sMaxNamedInSummary)
	}
	res.Summary += "; read objects with k8s_get_resource kind=<plural.group>"
	if res.Truncated {
		res.Summary += "; output truncated, narrow with group_contains"
	}
	return res, nil
}

func crdInfo(u *unstructured.Unstructured, now time.Time) types.K8sCRD {
	str := func(f ...string) string { s, _, _ := unstructured.NestedString(u.Object, f...); return s }
	crd := types.K8sCRD{
		Name:  u.GetName(),
		Group: str("spec", "group"),
		Kind:  str("spec", "names", "kind"),
		Scope: str("spec", "scope"),
		Age:   shortAge(now.Sub(u.GetCreationTimestamp().Time)),
	}
	crd.ShortNames, _, _ = unstructured.NestedStringSlice(u.Object, "spec", "names", "shortNames")
	versions, _, _ := unstructured.NestedSlice(u.Object, "spec", "versions")
	for _, v := range versions {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		name, _ := m["name"].(string)
		var tags []string
		if s, _ := m["storage"].(bool); s {
			tags = append(tags, "storage")
		}
		if s, _ := m["served"].(bool); !s {
			tags = append(tags, "not served")
		}
		if d, _ := m["deprecated"].(bool); d {
			tags = append(tags, "deprecated")
		}
		if len(tags) > 0 {
			name += " (" + strings.Join(tags, ", ") + ")"
		}
		crd.Versions = append(crd.Versions, name)
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		if m, ok := c.(map[string]any); ok && m["type"] == "Established" && m["status"] == "True" {
			crd.Established = true
		}
	}
	return crd
}

// helmProblemStatuses are release states that need a human.
var helmProblemStatuses = map[string]bool{"failed": true, "pending-install": true, "pending-upgrade": true, "pending-rollback": true, "uninstalling": true}

// ListK8sHelmReleases lists Helm 3 releases from the labels on their release
// Secrets, read through the metadata client: release contents (chart, values)
// are never fetched.
func ListK8sHelmReleases(ctx context.Context, c *K8sClients, in types.K8sHelmListInput) (*types.K8sHelmListResult, error) {
	if c.Metadata == nil {
		return nil, errors.New("metadata client unavailable for this context")
	}
	if err := checkNamespace(&in.Namespace); err != nil {
		return nil, err
	}
	if err := checkCommon("", &in.MaxResults); err != nil {
		return nil, err
	}
	secrets := schema.GroupVersionResource{Version: "v1", Resource: "secrets"}
	items, capped, err := pageAll(ctx, metav1.ListOptions{LabelSelector: "owner=helm"}, func(ctx context.Context, o metav1.ListOptions) ([]metav1.PartialObjectMetadata, string, error) {
		l, err := c.Metadata.Resource(secrets).Namespace(in.Namespace).List(ctx, o)
		if err != nil {
			return nil, "", fmt.Errorf("list helm release secrets: %w", err)
		}
		return l.Items, l.Continue, nil
	})
	if err != nil {
		return nil, err
	}

	byRelease := map[string]*types.K8sHelmRelease{}
	for _, s := range items {
		l := s.Labels
		name := l["name"]
		rev, _ := strconv.Atoi(l["version"])
		if name == "" {
			continue
		}
		key := s.Namespace + "/" + name
		r := byRelease[key]
		if r == nil {
			r = &types.K8sHelmRelease{Namespace: s.Namespace, Name: name}
			byRelease[key] = r
		}
		r.Revisions++
		if rev >= r.Revision {
			r.Revision, r.Status = rev, l["status"]
			if ts, err := strconv.ParseInt(l["modifiedAt"], 10, 64); err == nil {
				r.Updated = time.Unix(ts, 0).UTC().Format(time.RFC3339)
			} else {
				r.Updated = s.CreationTimestamp.UTC().Format(time.RFC3339)
			}
		}
	}

	res := &types.K8sHelmListResult{Namespace: in.Namespace, Truncated: capped}
	var problems []string
	for _, r := range byRelease {
		r.Problem = helmProblemStatuses[r.Status]
		if in.NameContains != "" && !strings.Contains(r.Name, in.NameContains) || in.ProblemsOnly && !r.Problem {
			continue
		}
		if r.Problem {
			problems = append(problems, fmt.Sprintf("%s/%s (%s)", r.Namespace, r.Name, r.Status))
		}
		res.Releases = append(res.Releases, *r)
	}
	slices.SortFunc(res.Releases, func(a, b types.K8sHelmRelease) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	res.Total = len(res.Releases)
	if res.Total > in.MaxResults {
		res.Releases, res.Truncated = res.Releases[:in.MaxResults], true
	}
	res.Summary = fmt.Sprintf("%d Helm releases in %s", res.Total, scopeOf(in.Namespace))
	if len(problems) > 0 {
		res.Summary += "; need attention: " + namedList(problems, k8sMaxNamedInSummary)
	}
	if res.Truncated {
		res.Summary += "; output truncated, narrow with namespace or name_contains"
	}
	return res, nil
}
