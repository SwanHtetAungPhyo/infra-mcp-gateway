package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"ai.engineer.runner/internal/types"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var K8sServiceTypes = []string{"ClusterIP", "NodePort", "LoadBalancer", "ExternalName"}

// ListK8sServices lists services with ports, external addresses and how many
// ready endpoints back each one.
func ListK8sServices(ctx context.Context, cs kubernetes.Interface, in types.K8sServiceListInput) (*types.K8sServiceListResult, error) {
	if err := checkNamespace(&in.Namespace); err != nil {
		return nil, err
	}
	if err := checkCommon(in.LabelSelector, &in.MaxResults); err != nil {
		return nil, err
	}
	if in.Type != "" && !slices.Contains(K8sServiceTypes, in.Type) {
		return nil, fmt.Errorf("type must be one of %v", K8sServiceTypes)
	}

	svcs, capped, err := pageAll(ctx, metav1.ListOptions{LabelSelector: in.LabelSelector}, func(ctx context.Context, o metav1.ListOptions) ([]corev1.Service, string, error) {
		l, err := cs.CoreV1().Services(in.Namespace).List(ctx, o)
		if err != nil {
			return nil, "", fmt.Errorf("list services: %w", err)
		}
		return l.Items, l.Continue, nil
	})
	if err != nil {
		return nil, err
	}
	res := &types.K8sServiceListResult{Namespace: in.Namespace, Truncated: capped}

	// Ready endpoints per service, from EndpointSlices. Optional: without
	// RBAC for them the call still succeeds with a warning.
	ready := map[string]int{}
	slices_, _, err := pageAll(ctx, metav1.ListOptions{}, func(ctx context.Context, o metav1.ListOptions) ([]discoveryv1.EndpointSlice, string, error) {
		l, err := cs.DiscoveryV1().EndpointSlices(in.Namespace).List(ctx, o)
		if err != nil {
			return nil, "", err
		}
		return l.Items, l.Continue, nil
	})
	haveEndpoints := err == nil
	if err != nil {
		res.Warnings = append(res.Warnings, "ready_endpoints unavailable: "+trimErr(err))
	}
	for _, es := range slices_ {
		svc := es.Labels[discoveryv1.LabelServiceName]
		for _, ep := range es.Endpoints {
			if ep.Conditions.Ready == nil || *ep.Conditions.Ready {
				ready[es.Namespace+"/"+svc] += len(ep.Addresses)
			}
		}
	}

	now := time.Now()
	var exposed, noEndpoints []string
	for i := range svcs {
		s := serviceInfo(&svcs[i], now)
		if in.NameContains != "" && !strings.Contains(s.Name, in.NameContains) ||
			in.Type != "" && s.Type != in.Type || in.ExposedOnly && !s.Exposed {
			continue
		}
		key := s.Namespace + "/" + s.Name
		if haveEndpoints && s.Type != "ExternalName" {
			n := ready[key]
			s.ReadyEndpoints = &n
			if n == 0 && len(s.Selector) > 0 {
				noEndpoints = append(noEndpoints, key)
			}
		}
		if s.Exposed {
			exposed = append(exposed, key)
		}
		res.Services = append(res.Services, s)
	}
	slices.SortFunc(res.Services, func(a, b types.K8sService) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	res.Total = len(res.Services)
	if res.Total > in.MaxResults {
		res.Services, res.Truncated = res.Services[:in.MaxResults], true
	}

	res.Summary = fmt.Sprintf("%d services in %s", res.Total, scopeOf(in.Namespace))
	if len(exposed) > 0 {
		res.Summary += fmt.Sprintf("; %d exposed outside the cluster: %s", len(exposed), namedList(exposed, k8sMaxNamedInSummary))
	}
	if len(noEndpoints) > 0 {
		res.Summary += fmt.Sprintf("; %d with no ready endpoints: %s", len(noEndpoints), namedList(noEndpoints, k8sMaxNamedInSummary))
	}
	if res.Truncated {
		res.Summary += "; output truncated, narrow with namespace, type or name_contains"
	}
	return res, nil
}

func serviceInfo(s *corev1.Service, now time.Time) types.K8sService {
	out := types.K8sService{
		Namespace: s.Namespace, Name: s.Name, Type: string(s.Spec.Type),
		Selector: s.Spec.Selector, Age: shortAge(now.Sub(s.CreationTimestamp.Time)),
	}
	if out.Type == "" {
		out.Type = "ClusterIP"
	}
	if s.Spec.ClusterIP != "" && s.Spec.ClusterIP != corev1.ClusterIPNone {
		out.ClusterIP = s.Spec.ClusterIP
	} else if s.Spec.ClusterIP == corev1.ClusterIPNone {
		out.ClusterIP = "None (headless)"
	}
	for _, ing := range s.Status.LoadBalancer.Ingress {
		out.External = append(out.External, nonEmpty(ing.IP, ing.Hostname))
	}
	out.External = append(out.External, s.Spec.ExternalIPs...)
	if s.Spec.ExternalName != "" {
		out.External = append(out.External, s.Spec.ExternalName)
	}
	out.Exposed = out.Type == "LoadBalancer" || out.Type == "NodePort" || len(s.Spec.ExternalIPs) > 0
	for _, p := range s.Spec.Ports {
		port := fmt.Sprintf("%d", p.Port)
		if p.NodePort != 0 {
			port += fmt.Sprintf(":%d", p.NodePort)
		}
		port += "/" + string(p.Protocol)
		if t := p.TargetPort.String(); t != "" && t != "0" && t != fmt.Sprint(p.Port) {
			port += "->" + t
		}
		if p.Name != "" {
			port = p.Name + " " + port
		}
		out.Ports = append(out.Ports, port)
	}
	return out
}

// ListK8sIngresses lists ingresses with hosts, routing rules, TLS and the
// hosts served without TLS.
func ListK8sIngresses(ctx context.Context, cs kubernetes.Interface, in types.K8sIngressListInput) (*types.K8sIngressListResult, error) {
	if err := checkNamespace(&in.Namespace); err != nil {
		return nil, err
	}
	if err := checkCommon(in.LabelSelector, &in.MaxResults); err != nil {
		return nil, err
	}
	ings, capped, err := pageAll(ctx, metav1.ListOptions{LabelSelector: in.LabelSelector}, func(ctx context.Context, o metav1.ListOptions) ([]networkingv1.Ingress, string, error) {
		l, err := cs.NetworkingV1().Ingresses(in.Namespace).List(ctx, o)
		if err != nil {
			return nil, "", fmt.Errorf("list ingresses: %w", err)
		}
		return l.Items, l.Continue, nil
	})
	if err != nil {
		return nil, err
	}

	res := &types.K8sIngressListResult{Namespace: in.Namespace, Truncated: capped}
	now := time.Now()
	hosts := map[string]bool{}
	var noTLS []string
	for i := range ings {
		ing := ingressInfo(&ings[i], now)
		if in.HostContains != "" && !slices.ContainsFunc(ing.Hosts, func(h string) bool { return strings.Contains(h, in.HostContains) }) {
			continue
		}
		for _, h := range ing.Hosts {
			hosts[h] = true
		}
		for _, h := range ing.NoTLSHosts {
			noTLS = append(noTLS, h)
		}
		res.Ingresses = append(res.Ingresses, ing)
	}
	slices.SortFunc(res.Ingresses, func(a, b types.K8sIngress) int {
		return strings.Compare(a.Namespace+"/"+a.Name, b.Namespace+"/"+b.Name)
	})
	res.Total = len(res.Ingresses)
	if res.Total > in.MaxResults {
		res.Ingresses, res.Truncated = res.Ingresses[:in.MaxResults], true
	}

	res.Summary = fmt.Sprintf("%d ingresses in %s serving %d hosts", res.Total, scopeOf(in.Namespace), len(hosts))
	if len(noTLS) > 0 {
		res.Summary += fmt.Sprintf("; %d hosts without TLS: %s", len(noTLS), namedList(noTLS, k8sMaxNamedInSummary))
	}
	if res.Truncated {
		res.Summary += "; output truncated, narrow with namespace or host_contains"
	}
	return res, nil
}

func ingressInfo(ing *networkingv1.Ingress, now time.Time) types.K8sIngress {
	out := types.K8sIngress{Namespace: ing.Namespace, Name: ing.Name, Age: shortAge(now.Sub(ing.CreationTimestamp.Time))}
	if ing.Spec.IngressClassName != nil {
		out.Class = *ing.Spec.IngressClassName
	} else if c := ing.Annotations["kubernetes.io/ingress.class"]; c != "" {
		out.Class = c
	}
	backend := func(b *networkingv1.IngressBackend) string {
		switch {
		case b == nil:
			return "?"
		case b.Service != nil:
			port := b.Service.Port.Name
			if port == "" {
				port = fmt.Sprint(b.Service.Port.Number)
			}
			return b.Service.Name + ":" + port
		case b.Resource != nil:
			return b.Resource.Kind + "/" + b.Resource.Name
		}
		return "?"
	}
	if b := ing.Spec.DefaultBackend; b != nil {
		out.Rules = append(out.Rules, "* -> "+backend(b))
	}
	seen := map[string]bool{}
	for _, r := range ing.Spec.Rules {
		host := nonEmpty(r.Host, "*")
		if r.Host != "" && !seen[r.Host] {
			seen[r.Host] = true
			out.Hosts = append(out.Hosts, r.Host)
		}
		if r.HTTP == nil {
			continue
		}
		for _, p := range r.HTTP.Paths {
			out.Rules = append(out.Rules, host+nonEmpty(p.Path, "/")+" -> "+backend(&p.Backend))
		}
	}
	tlsHosts := map[string]bool{}
	for _, t := range ing.Spec.TLS {
		entry := strings.Join(t.Hosts, ",")
		if t.SecretName != "" {
			entry += " (" + t.SecretName + ")"
		}
		out.TLS = append(out.TLS, entry)
		for _, h := range t.Hosts {
			tlsHosts[h] = true
		}
	}
	for _, h := range out.Hosts {
		if !tlsHosts[h] {
			out.NoTLSHosts = append(out.NoTLSHosts, h)
		}
	}
	for _, a := range ing.Status.LoadBalancer.Ingress {
		out.Addresses = append(out.Addresses, nonEmpty(a.IP, a.Hostname))
	}
	return out
}
